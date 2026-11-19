package nomadjob

import (
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// Image scans (feature 044, research R2 and R3). Grype scans one image per Nomad
// batch job, run by Caravana one after another, and keeps its vulnerability
// database in a host volume so it is downloaded once a day, not once a scan.
// Like Translate, this is pure.

// ScanJobID is the job of scan run runID. It starts with "_", which no app ID
// does, so it is no app's job and has no app's prefix (AppJobPrefix).
func ScanJobID(runID string) string { return "_scan-" + runID }

// ScanVolume is the host volume that keeps the scanner's database. It has no
// "_", so it is no app's volume (<app>_<volume>).
const ScanVolume = "caravana-scanner"

// ScanResultFile is where the task writes its result, relative to the
// allocation's directory (the task sees it as /alloc/data/scan.txt).
const ScanResultFile = "alloc/data/scan.txt"

// ScanTask is the name of the scanner's task.
const ScanTask = "grype"

// ScanMemoryMB is what a scan reserves. The service checks a server has this
// much free before it starts one. The ceiling is higher (scanTaskMemoryMaxMB),
// for the images that need it.
const ScanMemoryMB = 512

// ScanVariablePath is where a scan job reads its registry credentials: its
// own variable, the only one a job with this ID can read (see
// BackupVariablePath).
func ScanVariablePath(jobID string) string { return BackupVariablePath(jobID) }

// The environment variables Grype reads the credentials of the image's
// registry from. They are the items of a private scan's variable, which the
// job's template exports as env; the job itself holds no value.
const (
	ScanRegistryAuthorityEnv = "GRYPE_REGISTRY_AUTH_AUTHORITY"
	ScanRegistryUsernameEnv  = "GRYPE_REGISTRY_AUTH_USERNAME"
	ScanRegistryPasswordEnv  = "GRYPE_REGISTRY_AUTH_PASSWORD"
)

// Resources of the scan job.
const (
	scanCPUMHz          = 500
	scanTaskMemoryMaxMB = 2048
	scanKillTimeout     = 10 * time.Second
	scanLogFiles        = 2
	scanLogFileMB       = 1

	// scanCachePath is where the task sees ScanVolume.
	scanCachePath = "/cache"
	// scanTemplatePath is where the task finds the result template.
	scanTemplatePath = "local/scan.tmpl"
)

// ScanSource says what a scan run reads.
type ScanSource struct {
	// Image is the digest scanned: repo@sha256:...
	Image string
	// FromSBOM reads the SBOM an earlier scan of Image kept on the volume
	// (SBOMPath) instead of pulling the image from its registry: no pull and
	// no credentials. A scan that reads the registry keeps the SBOM.
	FromSBOM bool
}

// ScanOptions are what a run needs besides what it scans.
type ScanOptions struct {
	// NodeID is the Nomad node that holds ScanVolume. The job is constrained to
	// it, which is where Nomad would place a job that needs the volume anyway:
	// the constraint says it in the job. Empty: no constraint.
	NodeID string
	// DatabaseURL is Grype's update URL (the "latest.json" listing of a mirror
	// of its database). Empty: Grype's own.
	DatabaseURL string
	// UpdateOnly makes the run download the vulnerability database and scan
	// nothing: the first run, when the scanner is installed.
	UpdateOnly bool
}

// SBOMPath is where the SBOM of image is kept, as the task sees it:
// /cache/sbom/<hex of the digest>.cdx.json. It is empty for an image that
// isn't pinned by digest.
func SBOMPath(image string) string {
	_, digest, ok := strings.Cut(image, "@sha256:")
	if !ok || digest == "" {
		return ""
	}
	return scanCachePath + "/sbom/" + digest + ".cdx.json"
}

// ScanJob is the job that runs the scanner for run runID.
//
//   - A registry scan (the default) reads src.Image from its registry. It writes
//     the findings with vulns.Template to ScanResultFile and, in the same run,
//     the image's CycloneDX SBOM to SBOMPath, so a later scan of the digest can
//     read that instead (FromSBOM).
//   - An SBOM scan reads SBOMPath and writes only the findings.
//   - An update-only run (opt.UpdateOnly) runs "db update" and scans nothing.
//
// private is as for Translate: a registry scan of an image from one of those
// registries gets the credentials Grype reads from the job's own variable,
// which Caravana writes before it registers the job. The other shapes never
// read a registry's credentials and get no variable.
//
//   - The group has host networking, so the task reaches the registry the way
//     the node's Docker does (a registry on the node's loopback included).
//     Grype reads the image from its registry itself; it needs no Docker
//     daemon, which is why the task mounts nothing of the node's.
//   - The database is cached in ScanVolume, mounted at /cache (the database in
//     /cache/db, the SBOMs in /cache/sbom). The check for a newer database runs
//     at most once a day, and a database older than five days is refused rather
//     than scanned against.
//   - The findings template is a file of the task, written by Nomad with its own
//     delimiters ("[[%" "%]]") so Nomad leaves Grype's "{{ }}" alone.
//   - Nothing about the host or the image leaves it: no update check, and the
//     only network calls are the database download and the image's registry.
//
// The job must be purged after the scan is read: it is a batch job that
// nothing else removes, and its variable may hold registry credentials.
// Meta carries caravana.scan and caravana.image, and no caravana.app: a scan
// belongs to an image, not to an app.
func ScanJob(runID string, src ScanSource, opt ScanOptions, s Settings, private map[string]bool) *api.Job {
	id := ScanJobID(runID)
	j := &api.Job{
		ID:          new(id),
		Name:        new(id),
		Type:        new("batch"),
		Datacenters: []string{"*"},
		Meta:        map[string]string{"caravana.scan": runID, "caravana.image": src.Image},
	}

	env := map[string]string{
		"GRYPE_DB_CACHE_DIR":                  scanCachePath + "/db",
		"GRYPE_CHECK_FOR_APP_UPDATE":          "false",
		"GRYPE_DB_UPDATE_CHECK_MAX_FREQUENCY": "24h",
		"GRYPE_DB_MAX_ALLOWED_BUILT_AGE":      "120h",
	}
	if opt.DatabaseURL != "" {
		env["GRYPE_DB_UPDATE_URL"] = opt.DatabaseURL
	}

	var args []string
	var templates []*api.Template
	findings := "template=/" + ScanResultFile
	switch {
	case opt.UpdateOnly:
		args = []string{"db", "update"}
	case src.FromSBOM:
		args = []string{"sbom:" + SBOMPath(src.Image), "-o", findings, "-t", "/" + scanTemplatePath}
		templates = append(templates, scanTemplate())
	default:
		args = []string{"registry:" + src.Image, "-o", findings, "-t", "/" + scanTemplatePath, "-o", "cyclonedx-json=" + SBOMPath(src.Image)}
		templates = append(templates, scanTemplate())
		if _, ok := PrivateRegistry(src.Image, private); ok {
			templates = append(templates, variableTemplate(id))
		}
	}

	t := &api.Task{
		Name:   ScanTask,
		Driver: "docker",
		Config: map[string]any{
			"image": s.scannerImage(),
			"args":  args,
		},
		Env:       env,
		Templates: templates,
		VolumeMounts: []*api.VolumeMount{{
			Volume:      new("cache"),
			Destination: new(scanCachePath),
			ReadOnly:    new(false),
		}},
		Resources: &api.Resources{
			CPU:         new(scanCPUMHz),
			MemoryMB:    new(ScanMemoryMB),
			MemoryMaxMB: new(scanTaskMemoryMaxMB),
		},
		KillTimeout: new(scanKillTimeout),
		LogConfig:   &api.LogConfig{MaxFiles: new(scanLogFiles), MaxFileSizeMB: new(scanLogFileMB)},
	}

	g := batchGroup("scan", t)
	g.Networks = []*api.NetworkResource{{Mode: "host"}}
	g.Volumes = map[string]*api.VolumeRequest{"cache": {
		Name:           "cache",
		Type:           "host",
		Source:         ScanVolume,
		AccessMode:     "single-node-single-writer",
		AttachmentMode: "file-system",
	}}
	if opt.NodeID != "" {
		g.Constraints = []*api.Constraint{{LTarget: "${node.unique.id}", Operand: "=", RTarget: opt.NodeID}}
	}
	j.TaskGroups = []*api.TaskGroup{g}
	return j
}

// scanTemplate is the file the findings template is written to, with
// delimiters Nomad's own template engine doesn't use.
func scanTemplate() *api.Template {
	return &api.Template{
		EmbeddedTmpl: new(vulns.Template),
		DestPath:     new(scanTemplatePath),
		LeftDelim:    new("[[%"),
		RightDelim:   new("%]]"),
		ChangeMode:   new("noop"),
	}
}

// ScanRegistryItems are the items of a private scan's variable for image: the
// registry's host (Grype's "authority", which limits where the credentials are
// sent) and the user name and secret. It is pure; Caravana reads the user and
// the secret from where it keeps them.
func ScanRegistryItems(image, user, pass string) map[string]string {
	host := ""
	if ref, err := registry.Parse(image); err == nil {
		host = ref.Registry
	}
	return map[string]string{
		ScanRegistryAuthorityEnv: host,
		ScanRegistryUsernameEnv:  user,
		ScanRegistryPasswordEnv:  pass,
	}
}
