package nomadjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// Run `go test ./internal/nomadjob -update` to rewrite the golden files, then
// review the diff before committing.
var update = flag.Bool("update", false, "rewrite golden files")

// whoamiApp and whoamiDeployment are the equivalent of deploy/dev/whoami.json.
func whoamiApp() app.App { return apptest.Whoami().App }

func whoamiDeployment() app.Deployment { return apptest.Whoami().Deployment }

// labSettings matches the local Nomad lab: no Let's Encrypt, Traefik's
// default certificate.
func labSettings() Settings {
	return Settings{
		Entrypoint:      "websecure",
		CertResolver:    "",
		DefaultCPUMHz:   100,
		DefaultMemoryMB: 64,
	}
}

// marshalJob returns the exact body Nomad's register endpoint takes.
func marshalJob(t *testing.T, job *api.Job) []byte {
	t.Helper()
	b, err := json.MarshalIndent(struct{ Job *api.Job }{job}, "", "  ")
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	return append(b, '\n')
}

type goldenCase struct {
	name       string
	app        app.App
	deployment app.Deployment
	settings   Settings
}

// goldenCases is every apptest shape with the lab settings, plus the whoami
// app with a Let's Encrypt resolver.
func goldenCases() []goldenCase {
	var cases []goldenCase
	for _, c := range apptest.Cases() {
		cases = append(cases, goldenCase{name: c.Name, app: c.App, deployment: c.Deployment, settings: labSettings()})
	}
	letsEncrypt := labSettings()
	letsEncrypt.CertResolver = "letsencrypt"
	w := apptest.Whoami()
	return append(cases, goldenCase{name: "letsencrypt", app: w.App, deployment: w.Deployment, settings: letsEncrypt})
}

func TestTranslate(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Translate(tc.app, tc.deployment, tc.settings)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("warnings = %v, want none", res.Warnings)
			}

			got := marshalJob(t, res.Job)
			path := filepath.Join("testdata", tc.name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("output differs from %s; if the change is intended, run with -update and review the diff.\ngot:\n%s", path, got)
			}
		})
	}
}

// TestTranslateMatchesLabJob checks the translator against the job the lab
// is known to run. Reading the lab's own file means the two can't drift.
func TestTranslateMatchesLabJob(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "deploy", "dev", "whoami.json"))
	if err != nil {
		t.Fatalf("open lab job: %v", err)
	}
	defer f.Close()

	var want struct{ Job *api.Job }
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&want); err != nil {
		t.Fatalf("decode lab job: %v", err)
	}

	res, err := Translate(whoamiApp(), whoamiDeployment(), labSettings())
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	// DeepEqual follows pointers, so it compares values, not addresses.
	if !reflect.DeepEqual(want.Job, res.Job) {
		t.Errorf("translated job differs from deploy/dev/whoami.json\nwant:\n%s\ngot:\n%s",
			marshalJob(t, want.Job), marshalJob(t, res.Job))
	}
}

func TestTranslateDeterministic(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			var first []byte
			for i := range 100 {
				res, err := Translate(tc.app, tc.deployment, tc.settings)
				if err != nil {
					t.Fatalf("Translate: %v", err)
				}
				got := marshalJob(t, res.Job)
				if i == 0 {
					first = got
					continue
				}
				if !bytes.Equal(got, first) {
					t.Fatalf("run %d differs from run 0", i)
				}
			}
		})
	}
}

func TestTranslateNewDeploymentOnlyChangesMeta(t *testing.T) {
	d1 := whoamiDeployment()
	d1.ID = "d1"
	d2 := whoamiDeployment()
	d2.ID = "d2"

	r1, err := Translate(whoamiApp(), d1, labSettings())
	if err != nil {
		t.Fatalf("Translate d1: %v", err)
	}
	r2, err := Translate(whoamiApp(), d2, labSettings())
	if err != nil {
		t.Fatalf("Translate d2: %v", err)
	}

	if r1.Job.Meta["caravana.deployment"] == r2.Job.Meta["caravana.deployment"] {
		t.Fatalf("both jobs have deployment meta %q, want different", r1.Job.Meta["caravana.deployment"])
	}
	r2.Job.Meta["caravana.deployment"] = "d1"
	if !reflect.DeepEqual(r1.Job, r2.Job) {
		t.Errorf("jobs differ in more than the deployment meta\nd1:\n%s\nd2:\n%s",
			marshalJob(t, r1.Job), marshalJob(t, r2.Job))
	}
}

// TestTranslateErrors covers the rules that live in this package (settings
// and Nomad's limits) and proves app.Validate runs. The model's own rules are
// tested in package app.
func TestTranslateErrors(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(a *app.App, d *app.Deployment, s *Settings)
		wantField string
	}{
		{"entrypoint empty", func(a *app.App, d *app.Deployment, s *Settings) { s.Entrypoint = "" }, "settings.entrypoint"},
		{"entrypoint with space", func(a *app.App, d *app.Deployment, s *Settings) { s.Entrypoint = "web secure" }, "settings.entrypoint"},
		{"entrypoint list", func(a *app.App, d *app.Deployment, s *Settings) { s.Entrypoint = "a,b" }, "settings.entrypoint"},
		{"cert resolver with space", func(a *app.App, d *app.Deployment, s *Settings) { s.CertResolver = "le tls" }, "settings.certResolver"},
		{"default cpu zero", func(a *app.App, d *app.Deployment, s *Settings) { s.DefaultCPUMHz = 0 }, "settings.defaultCPUMHz"},
		{"default memory below minimum", func(a *app.App, d *app.Deployment, s *Settings) { s.DefaultMemoryMB = 9 }, "settings.defaultMemoryMB"},

		{"memory below minimum", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Resources.MemoryMB = 5
		}, "services[0].resources.memoryMB"},
		{"interval below minimum", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Health.Interval = 500 * time.Millisecond
		}, "services[0].health.interval"},
		{"timeout below minimum", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Health.Timeout = 500 * time.Millisecond
		}, "services[0].health.timeout"},
		{"timeout equal to default interval", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Health.Timeout = 10 * time.Second
		}, "services[0].health.timeout"},
		{"timeout longer than interval", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Health.Interval = 2 * time.Second
			a.Services[0].Health.Timeout = 3 * time.Second
		}, "services[0].health.timeout"},

		{"app rules run", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Image = ""
		}, "services[0].image"},
		{"model rules run", func(a *app.App, d *app.Deployment, s *Settings) {
			d.Digests["web"] = "traefik/whoami:latest"
		}, "deployment.digests[web]"},
		{"settings checked first", func(a *app.App, d *app.Deployment, s *Settings) {
			s.Entrypoint = ""
			d.Digests["web"] = "traefik/whoami:latest"
		}, "settings.entrypoint"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, d, s := whoamiApp(), whoamiDeployment(), labSettings()
			tc.mutate(&a, &d, &s)

			res, err := Translate(a, d, s)
			var fe *app.FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("Translate() error = %v, want a *app.FieldError for %q", err, tc.wantField)
			}
			if fe.Field != tc.wantField {
				t.Errorf("Field = %q, want %q (error: %v)", fe.Field, tc.wantField, err)
			}
			if fe.Message == "" {
				t.Errorf("Message is empty for %q", fe.Field)
			}
			if res.Job != nil {
				t.Errorf("got a job together with an error")
			}

			// Check reports the same error for everything that doesn't
			// need the deployment, and nothing for deployment problems.
			checkErr := Check(a, s)
			if strings.HasPrefix(tc.wantField, "deployment.") {
				if checkErr != nil {
					t.Errorf("Check() = %v, want nil", checkErr)
				}
			} else if checkErr == nil || checkErr.Error() != err.Error() {
				t.Errorf("Check() = %v, want the same error as Translate: %v", checkErr, err)
			}
		})
	}
}

// TestSecretsStayOutOfTheJob checks that a job names each service's variable
// and holds none of the values, and that a service without secrets gets no
// template.
func TestSecretsStayOutOfTheJob(t *testing.T) {
	marker := "s3cr3t-marker"
	a := apptest.Whoami().App
	a.Services[0].Secrets = []app.Secret{{Name: "API_KEY", Value: new(marker)}}
	d := apptest.Whoami().Deployment

	res, err := Translate(a, d, labSettings())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(res.Job)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatal("the job holds a secret value")
	}
	tmpls := res.Job.TaskGroups[0].Tasks[0].Templates
	if len(tmpls) != 1 || !strings.Contains(*tmpls[0].EmbeddedTmpl, `"nomad/jobs/whoami/web"`) || *tmpls[0].ChangeMode != "noop" {
		t.Errorf("templates = %+v, want one noop template reading nomad/jobs/whoami/web", tmpls)
	}

	plain, err := Translate(apptest.Whoami().App, d, labSettings())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(plain.Job.TaskGroups[0].Tasks[0].Templates); n != 0 {
		t.Errorf("a service without secrets got %d templates", n)
	}
}
