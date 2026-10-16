package nomadjob

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// registrySentinel stands for a registry password. The job never receives the
// real one, so it must never show up in a job, an export or a template.
const registrySentinel = "s3cr3t-SENTINEL-registry-pw"

// privateHosts is the set Caravana has credentials for in these cases.
func privateHosts() map[string]bool {
	return map[string]bool{"ghcr.io": true, "registry.example.com:5000": true}
}

// privateRegistryCases are the apps of feature 017: one private service, a
// Compose app that mixes private and public images, and a private Postgres
// with a volume and a backup.
func privateRegistryCases() []goldenCase {
	pin := func(repo string, hex string) string { return repo + "@sha256:" + strings.Repeat(hex, 64) }
	db := backedUpDB()
	db.Image = "ghcr.io/acme/postgres:17"
	return []goldenCase{
		{
			name: "private-registry",
			app: app.App{ID: "shop", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
				Name:    "web",
				Image:   "ghcr.io/acme/shop:v1",
				Ports:   []app.Port{{Name: "http", ContainerPort: 8080}},
				Domains: []app.Domain{{Host: "shop.example.com", Port: "http"}},
				Health:  &app.HealthCheck{Path: "/"},
				Secrets: []app.Secret{{Name: "API_KEY", Value: new(registrySentinel)}},
			}}},
			deployment: app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{"web": pin("ghcr.io/acme/shop", "a")}},
			settings:   labSettings(),
			private:    privateHosts(),
		},
		{
			// web and api come from private registries (one with a port), cache is a
			// public Docker Hub image: only the first two get the login.
			name: "private-mixed",
			app: app.App{ID: "mix", Source: app.Source{Kind: app.SourceCompose}, Services: []app.Service{
				{
					Name: "web", Image: "ghcr.io/acme/web:1",
					Ports:   []app.Port{{Name: "http", ContainerPort: 3000}},
					Domains: []app.Domain{{Host: "mix.example.com", Port: "http"}},
					Health:  &app.HealthCheck{Path: "/"},
				},
				{
					Name: "api", Image: "Registry.Example.com:5000/acme/api:2",
					Ports:  []app.Port{{Name: "http", ContainerPort: 4000}},
					Health: &app.HealthCheck{Path: "/healthz"},
				},
				{Name: "cache", Image: "redis:7", Ports: []app.Port{{Name: "redis", ContainerPort: 6379}}, Health: &app.HealthCheck{}},
			}},
			deployment: app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
				"web":   pin("ghcr.io/acme/web", "b"),
				"api":   pin("registry.example.com:5000/acme/api", "c"),
				"cache": pin("redis", "d"),
			}},
			settings: labSettings(),
			private:  privateHosts(),
		},
		{
			name:       "private-postgres",
			app:        app.App{ID: "shop", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{db}},
			deployment: app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{"db": pin("ghcr.io/acme/postgres", "e")}},
			settings:   labSettings(),
			private:    privateHosts(),
		},
	}
}

// caseNamed is the golden case with this name.
func goldenCaseNamed(t *testing.T, name string) goldenCase {
	t.Helper()
	for _, tc := range goldenCases() {
		if tc.name == name {
			return tc
		}
	}
	t.Fatalf("no golden case %q", name)
	return goldenCase{}
}

// taskNamed is the group's task with this name.
func taskNamed(t *testing.T, tg *api.TaskGroup, name string) *api.Task {
	t.Helper()
	for _, task := range tg.Tasks {
		if task.Name == name {
			return task
		}
	}
	t.Fatalf("group %s has no task %s", *tg.Name, name)
	return nil
}

// wantAuth is the docker auth block of a private image from host.
func wantAuth(host string) []map[string]any {
	return []map[string]any{{
		"username":       "${CARAVANA_REGISTRY_USERNAME}",
		"password":       "${CARAVANA_REGISTRY_PASSWORD}",
		"server_address": host,
	}}
}

// US1: a service from a private registry pulls with the credentials its own
// task renders from its own variable, and the job names placeholders only.
func TestPrivateServiceShape(t *testing.T) {
	tc := goldenCaseNamed(t, "private-registry")
	job := translated(t, tc)
	task := taskNamed(t, job.TaskGroups[0], "web")

	if got := task.Config["auth"]; !reflect.DeepEqual(got, wantAuth("ghcr.io")) {
		t.Errorf("config.auth = %#v, want %#v", got, wantAuth("ghcr.io"))
	}

	// The secrets template (feature 008) and the registry one, side by side.
	if len(task.Templates) != 2 {
		t.Fatalf("templates = %d, want the secrets one and the registry one", len(task.Templates))
	}
	reg := task.Templates[1]
	if *reg.DestPath != "secrets/registry.env" || !*reg.Envvars || *reg.ChangeMode != "noop" {
		t.Errorf("registry template = dest %s, env %v, change_mode %s", *reg.DestPath, *reg.Envvars, *reg.ChangeMode)
	}
	if !strings.HasPrefix(*reg.EmbeddedTmpl, `{{ with nomadVar "nomad/jobs/shop/web/web" }}`) || !strings.HasSuffix(*reg.EmbeddedTmpl, `{{ end }}`) {
		t.Errorf("the registry template doesn't render only inside {{ with nomadVar <the task's own path> }}:\n%s", *reg.EmbeddedTmpl)
	}
	if got := PullCredentialsPath("shop", "web"); got != "nomad/jobs/shop/web/web" {
		t.Errorf("PullCredentialsPath = %q", got)
	}

	// No secret in the job, in the JSON Nomad gets or in the export.
	b, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	hcl := HCL(job, "")
	for name, out := range map[string][]byte{"job JSON": b, "export": hcl} {
		if bytes.Contains(out, []byte(registrySentinel)) {
			t.Errorf("the %s holds a secret value", name)
		}
	}
	if !bytes.Contains(b, []byte("${CARAVANA_REGISTRY_PASSWORD}")) {
		t.Error("the job JSON lacks the password placeholder")
	}
}

// The other tasks of a private service's group, and every service of a public
// image, are not touched.
func TestPrivateMixedOnlyTouchesPrivateServices(t *testing.T) {
	tc := goldenCaseNamed(t, "private-mixed")
	job := translated(t, tc)

	web := taskNamed(t, job.TaskGroups[0], "web")
	api := taskNamed(t, job.TaskGroups[1], "api")
	cache := taskNamed(t, job.TaskGroups[2], "cache")
	if !reflect.DeepEqual(web.Config["auth"], wantAuth("ghcr.io")) {
		t.Errorf("web auth = %#v", web.Config["auth"])
	}
	// Written in capitals, the host is the canonical lowercase one.
	if !reflect.DeepEqual(api.Config["auth"], wantAuth("registry.example.com:5000")) {
		t.Errorf("api auth = %#v, want the host with its port, lowercase", api.Config["auth"])
	}
	if _, has := cache.Config["auth"]; has || len(cache.Templates) != 0 {
		t.Errorf("the public cache service has auth or a template: %+v", cache)
	}
	for _, tg := range job.TaskGroups {
		names := taskNamed(t, tg, "names")
		if _, has := names.Config["auth"]; has {
			t.Errorf("group %s: the names sidecar got a login", *tg.Name)
		}
	}
	// Each private service reads its own variable.
	for _, svc := range []string{"web", "api"} {
		tg := job.TaskGroups[map[string]int{"web": 0, "api": 1}[svc]]
		tmpl := taskNamed(t, tg, svc).Templates[0]
		if !strings.Contains(*tmpl.EmbeddedTmpl, `nomadVar "nomad/jobs/mix/`+svc+`/`+svc+`"`) {
			t.Errorf("service %s reads %s", svc, *tmpl.EmbeddedTmpl)
		}
	}
}

// With no private hosts, or none of them in use, the job is exactly the one
// before feature 017. The set is keyed by canonical host, so a lookalike or
// another spelling of a host doesn't match.
func TestPrivateSetOfNothingChangesNothing(t *testing.T) {
	for _, name := range []string{"private-registry", "private-mixed", "private-postgres"} {
		tc := goldenCaseNamed(t, name)
		base, err := Translate(tc.app, tc.deployment, tc.settings, nil)
		if err != nil {
			t.Fatal(err)
		}
		for what, set := range map[string]map[string]bool{
			"empty":         {},
			"false":         {"ghcr.io": false, "registry.example.com:5000": false},
			"other hosts":   {"quay.io": true},
			"a lookalike":   {"ghcr.io:443": true, "ghcr.io.evil.com": true},
			"a wrong case":  {"GHCR.io": true},
			"the host only": {"registry.example.com": true},
		} {
			got, err := Translate(tc.app, tc.deployment, tc.settings, set)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(base.Job, got.Job) {
				t.Errorf("%s: the set %v changed the job of %s", what, set, name)
			}
		}
	}
}

// The registry template renders both lines from the variable's items, quoted
// like the secrets, and a missing or empty item renders as "" and never as
// "<no value>" (which the docker driver would send as a password).
func TestPullCredentialsTemplateRenders(t *testing.T) {
	tmpl := pullCredentialsTemplate("shop", "web")
	for _, tc := range []struct {
		name  string
		items map[string]string
		want  string
	}{
		{"both items",
			map[string]string{"registry": "ghcr.io", "CARAVANA_REGISTRY_USERNAME": "acme-bot", "CARAVANA_REGISTRY_PASSWORD": "tok-123"},
			"CARAVANA_REGISTRY_USERNAME=\"acme-bot\"\nCARAVANA_REGISTRY_PASSWORD=\"tok-123\"\n"},
		{"quotes, backslashes and unicode survive",
			map[string]string{"registry": "ghcr.io", "CARAVANA_REGISTRY_USERNAME": "a\"b", "CARAVANA_REGISTRY_PASSWORD": `p\w d é`},
			"CARAVANA_REGISTRY_USERNAME=\"a\\\"b\"\nCARAVANA_REGISTRY_PASSWORD=\"p\\\\w d é\"\n"},
		{"removed credentials: items empty, registry kept",
			map[string]string{"registry": "ghcr.io", "CARAVANA_REGISTRY_USERNAME": "", "CARAVANA_REGISTRY_PASSWORD": ""},
			"CARAVANA_REGISTRY_USERNAME=\"\"\nCARAVANA_REGISTRY_PASSWORD=\"\"\n"},
		{"items missing",
			map[string]string{"registry": "ghcr.io"},
			"CARAVANA_REGISTRY_USERNAME=\"\"\nCARAVANA_REGISTRY_PASSWORD=\"\"\n"},
		{"only the password missing",
			map[string]string{"registry": "ghcr.io", "CARAVANA_REGISTRY_USERNAME": "u"},
			"CARAVANA_REGISTRY_USERNAME=\"u\"\nCARAVANA_REGISTRY_PASSWORD=\"\"\n"},
		{"other items are never rendered",
			map[string]string{"registry": "ghcr.io", "API_KEY": "k", "CARAVANA_REGISTRY_USERNAME": "u", "CARAVANA_REGISTRY_PASSWORD": "p"},
			"CARAVANA_REGISTRY_USERNAME=\"u\"\nCARAVANA_REGISTRY_PASSWORD=\"p\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := render(t, tmpl, tc.items)
			if got != tc.want {
				t.Errorf("rendered:\n%q\nwant:\n%q", got, tc.want)
			}
			if strings.Contains(got, "no value") {
				t.Errorf("the template rendered a missing value: %q", got)
			}
		})
	}
	if strings.Contains(*tmpl.EmbeddedTmpl, "${") {
		t.Error(`the template contains "${", which Nomad interpolates`)
	}
}

// US1: the Postgres backup's dump and the restore pull the service's image
// with the same login. The tools' own image (rclone) is public.
func TestPrivateBackupAndRestoreJobs(t *testing.T) {
	tc := goldenCaseNamed(t, "private-postgres")
	svc, image := tc.app.Services[0], tc.deployment.Digests["db"]
	jobs := map[string]*api.Job{
		"private-backup":  BackupJob("shop", svc, image, labSettings(), privateHosts()),
		"private-restore": RestoreJob("shop", svc, image, "bucket/shop/db/1791386036.dump", labSettings(), privateHosts()),
	}
	for name, job := range jobs {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(job, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			for _, secret := range []string{"s3cr3t-marker", registrySentinel} {
				if bytes.Contains(got, []byte(secret)) {
					t.Fatalf("the job holds %q", secret)
				}
			}
			path := filepath.Join("testdata", "backup-"+name+".golden.json")
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

	// Which tasks get the login: the ones that run the service's image.
	backup, restore := jobs["private-backup"].TaskGroups[0], jobs["private-restore"].TaskGroups[0]
	for _, tk := range []*api.Task{taskNamed(t, backup, "dump"), taskNamed(t, restore, "restore")} {
		if !reflect.DeepEqual(tk.Config["auth"], wantAuth("ghcr.io")) {
			t.Errorf("task %s: auth = %#v", tk.Name, tk.Config["auth"])
		}
		// Their template already exports every item of the job's variable, which
		// is where the credential items go.
		if !strings.Contains(*tk.Templates[0].EmbeddedTmpl, `range .Tuples`) {
			t.Errorf("task %s: the variable template doesn't export every item", tk.Name)
		}
	}
	for _, tk := range []*api.Task{taskNamed(t, backup, "upload"), taskNamed(t, restore, "download")} {
		if _, has := tk.Config["auth"]; has {
			t.Errorf("task %s runs the public rclone image but has a login", tk.Name)
		}
	}

	// A public image, or nil: neither task has one.
	for _, set := range []map[string]bool{nil, {"quay.io": true}} {
		for _, job := range []*api.Job{
			BackupJob("shop", svc, image, labSettings(), set),
			RestoreJob("shop", svc, image, "x.dump", labSettings(), set),
		} {
			for _, tk := range job.TaskGroups[0].Tasks {
				if _, has := tk.Config["auth"]; has {
					t.Errorf("job %s task %s has a login for set %v", *job.ID, tk.Name, set)
				}
			}
		}
	}
}

// The export writes the auth block, with its placeholders escaped for HCL, and
// no secret.
func TestPrivateExportWritesTheAuthBlock(t *testing.T) {
	tc := goldenCaseNamed(t, "private-registry")
	hcl := string(HCL(translated(t, tc), ""))
	for _, want := range []string{
		"      config {\n        auth {\n          password = \"$${CARAVANA_REGISTRY_PASSWORD}\"\n          server_address = \"ghcr.io\"\n          username = \"$${CARAVANA_REGISTRY_USERNAME}\"\n        }\n        image = ",
		`destination = "secrets/registry.env"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("export lacks %q:\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, registrySentinel) {
		t.Error("the export holds a secret value")
	}
}

func TestPrivateRegistry(t *testing.T) {
	private := privateHosts()
	for image, want := range map[string]string{
		"ghcr.io/acme/shop@sha256:" + strings.Repeat("a", 64): "ghcr.io",
		"GHCR.io/acme/shop:v1":                                "ghcr.io",
		"registry.example.com:5000/acme/api:2":                "registry.example.com:5000",
		"redis:7":                                             "",
		"acme/shop":                                           "",
		"registry.example.com/acme/api":                       "",
		"not a reference":                                     "",
		"":                                                    "",
		"ghcr.io:443/acme/shop":                               "",
		"registry.example.com:5000.evil.net/x":                "",
		"ghcr.io/acme/shop@sha256:" + strings.Repeat("a", 63): "",
	} {
		host, ok := PrivateRegistry(image, private)
		if host != want || ok != (want != "") {
			t.Errorf("PrivateRegistry(%q) = %q, %v; want %q", image, host, ok, want)
		}
	}
	if _, ok := PrivateRegistry("ghcr.io/acme/shop", nil); ok {
		t.Error("PrivateRegistry with no set answered true")
	}
}
