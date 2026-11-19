package nomadjob

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/vulns"
)

const (
	scanPublicImage  = "traefik/whoami@sha256:1699d99cb4b9ba7d1d9a54ad9a3a9ed8e1a4ee0a0bb1d1d1c1e5a1b1d5e0f4d"
	scanPrivateImage = "registry.example.com:5000/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// scanCases are the four shapes of scan job.
func scanCases() []struct {
	name string
	job  *api.Job
} {
	private := map[string]bool{"registry.example.com:5000": true}
	opt := ScanOptions{NodeID: "node-7"}
	return []struct {
		name string
		job  *api.Job
	}{
		{"scan", ScanJob("r1", ScanSource{Image: scanPublicImage}, opt, labSettings(), private)},
		{"scan-private", ScanJob("r2", ScanSource{Image: scanPrivateImage}, ScanOptions{NodeID: "node-7", DatabaseURL: "https://mirror.example.com/grype/v6/latest.json"}, labSettings(), private)},
		{"scan-sbom", ScanJob("r3", ScanSource{Image: scanPrivateImage, FromSBOM: true}, opt, labSettings(), private)},
		{"scan-update", ScanJob("r4", ScanSource{}, ScanOptions{NodeID: "node-7", UpdateOnly: true}, labSettings(), private)},
	}
}

// TestScanJob compares scan jobs with their golden files: a registry scan of a
// public image, of a private one (with a mirror's database URL), a rescan from
// the kept SBOM and the install's database update.
func TestScanJob(t *testing.T) {
	for _, tc := range scanCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(tc.job, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", tc.name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs; run with -update and read the diff", path)
			}
		})
	}
}

func TestSBOMPath(t *testing.T) {
	if got, want := SBOMPath(scanPublicImage), "/cache/sbom/"+strings.TrimPrefix(scanPublicImage[strings.Index(scanPublicImage, "@"):], "@sha256:")+".cdx.json"; got != want {
		t.Errorf("SBOMPath = %q, want %q", got, want)
	}
	// One file per digest, whatever the repository: the digest is the image.
	if SBOMPath("a/b@sha256:"+strings.Repeat("c", 64)) != SBOMPath("x/y@sha256:"+strings.Repeat("c", 64)) {
		t.Error("the same digest must have the same SBOM path")
	}
	if SBOMPath("alpine:3.14") != "" || SBOMPath("alpine@sha256:") != "" {
		t.Error("an image not pinned by digest has no SBOM path")
	}
}

// TestScanJobShape checks what the golden files can't say by looking.
func TestScanJobShape(t *testing.T) {
	byName := map[string]*api.Job{}
	for _, tc := range scanCases() {
		byName[tc.name] = tc.job
	}
	for name, j := range byName {
		if !strings.HasPrefix(*j.ID, "_scan-") || *j.Type != "batch" || j.Periodic != nil {
			t.Errorf("%s: job = %s %s periodic %v", name, *j.ID, *j.Type, j.Periodic)
		}
		if _, ok := j.Meta["caravana.app"]; ok {
			t.Errorf("%s: meta %v names an app: a scan belongs to an image", name, j.Meta)
		}
		g := j.TaskGroups[0]
		if len(g.Networks) != 1 || g.Networks[0].Mode != "host" {
			t.Errorf("%s: networks = %+v, want one in host mode", name, g.Networks)
		}
		if *g.RestartPolicy.Attempts != 0 || *g.ReschedulePolicy.Attempts != 0 {
			t.Errorf("%s: a scan must not be retried by Nomad", name)
		}
		if len(g.Constraints) != 1 || g.Constraints[0].LTarget != "${node.unique.id}" || g.Constraints[0].Operand != "=" || g.Constraints[0].RTarget != "node-7" {
			t.Errorf("%s: constraints = %+v, want the node that holds the volume", name, g.Constraints)
		}
		if v := g.Volumes["cache"]; v == nil || v.Source != ScanVolume || v.Type != "host" || v.ReadOnly {
			t.Errorf("%s: volume = %+v, want the writable host volume %s", name, v, ScanVolume)
		}
		task := g.Tasks[0]
		if task.Name != ScanTask || task.Config["image"] != DefaultScannerImage {
			t.Errorf("%s: task = %s %v", name, task.Name, task.Config["image"])
		}
		if _, ok := task.Config["auth"]; ok {
			t.Errorf("%s: the scanner image is public: the task needs no docker auth block", name)
		}
		if m := task.VolumeMounts; len(m) != 1 || *m[0].Destination != "/cache" || *m[0].Volume != "cache" {
			t.Errorf("%s: mounts = %+v", name, m)
		}
		if *task.Resources.MemoryMB != ScanMemoryMB {
			t.Errorf("%s: memory = %d, want ScanMemoryMB", name, *task.Resources.MemoryMB)
		}
		if task.Env["GRYPE_CHECK_FOR_APP_UPDATE"] != "false" || task.Env["GRYPE_DB_CACHE_DIR"] != "/cache/db" {
			t.Errorf("%s: env = %v", name, task.Env)
		}
	}

	args := func(name string) []string { return byName[name].TaskGroups[0].Tasks[0].Config["args"].([]string) }
	templates := func(name string) int { return len(byName[name].TaskGroups[0].Tasks[0].Templates) }
	sbom := SBOMPath(scanPrivateImage)

	// A registry scan writes the findings and the SBOM in one run.
	if got, want := strings.Join(args("scan"), " "), "registry:"+scanPublicImage+" -o template=/alloc/data/scan.txt -t /local/scan.tmpl -o cyclonedx-json="+SBOMPath(scanPublicImage); got != want {
		t.Errorf("registry scan args = %q, want %q", got, want)
	}
	// An SBOM rescan reads the kept file and writes only the findings.
	if got, want := strings.Join(args("scan-sbom"), " "), "sbom:"+sbom+" -o template=/alloc/data/scan.txt -t /local/scan.tmpl"; got != want {
		t.Errorf("sbom scan args = %q, want %q", got, want)
	}
	if got := strings.Join(args("scan-update"), " "); got != "db update" {
		t.Errorf("update args = %q", got)
	}

	// Credentials only for a registry scan of a private image: one template for
	// the findings, one for the job's variable.
	if templates("scan") != 1 || templates("scan-sbom") != 1 || templates("scan-update") != 0 || templates("scan-private") != 2 {
		t.Errorf("templates: scan %d, private %d, sbom %d, update %d", templates("scan"), templates("scan-private"), templates("scan-sbom"), templates("scan-update"))
	}
	ts := byName["scan-private"].TaskGroups[0].Tasks[0].Templates
	if !strings.Contains(*ts[1].EmbeddedTmpl, ScanVariablePath("_scan-r2")) || !*ts[1].Envvars {
		t.Errorf("private templates = %+v, want the job's variable as env", ts)
	}
	// The findings template is written with delimiters Nomad's own engine
	// doesn't use, and holds none of them.
	tmpl := byName["scan"].TaskGroups[0].Tasks[0].Templates[0]
	if *tmpl.LeftDelim != "[[%" || *tmpl.RightDelim != "%]]" || *tmpl.EmbeddedTmpl != vulns.Template ||
		strings.Contains(vulns.Template, "[[%") || strings.Contains(vulns.Template, "%]]") {
		t.Error("the result template must be written with [[% %]] and hold neither")
	}
	if ScanVariablePath("_scan-r2") != "nomad/jobs/_scan-r2" {
		t.Errorf("variable path = %q", ScanVariablePath("_scan-r2"))
	}

	// The database URL is passed only when one is set.
	if byName["scan-private"].TaskGroups[0].Tasks[0].Env["GRYPE_DB_UPDATE_URL"] != "https://mirror.example.com/grype/v6/latest.json" {
		t.Error("the database URL was not passed")
	}
	if _, ok := byName["scan"].TaskGroups[0].Tasks[0].Env["GRYPE_DB_UPDATE_URL"]; ok {
		t.Error("no database URL was set, and one was passed")
	}
}

func TestScanJobWithoutANodeHasNoConstraint(t *testing.T) {
	j := ScanJob("r1", ScanSource{Image: scanPublicImage}, ScanOptions{}, labSettings(), nil)
	if len(j.TaskGroups[0].Constraints) != 0 {
		t.Errorf("constraints = %+v", j.TaskGroups[0].Constraints)
	}
}

func TestScanJobUsesTheConfiguredScanner(t *testing.T) {
	s := labSettings()
	s.ScannerImage = "ghcr.io/acme/grype@sha256:" + strings.Repeat("b", 64)
	j := ScanJob("r1", ScanSource{Image: scanPublicImage}, ScanOptions{}, s, nil)
	if got := j.TaskGroups[0].Tasks[0].Config["image"]; got != s.ScannerImage {
		t.Errorf("image = %v, want %s", got, s.ScannerImage)
	}
}

func TestScanRegistryItems(t *testing.T) {
	got := ScanRegistryItems(scanPrivateImage, "deploy", "pw")
	want := map[string]string{
		"GRYPE_REGISTRY_AUTH_AUTHORITY": "registry.example.com:5000",
		"GRYPE_REGISTRY_AUTH_USERNAME":  "deploy",
		"GRYPE_REGISTRY_AUTH_PASSWORD":  "pw",
	}
	if len(got) != len(want) {
		t.Fatalf("items = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
