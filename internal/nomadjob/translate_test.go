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
)

// Run `go test ./internal/nomadjob -update` to rewrite the golden files, then
// review the diff before committing.
var update = flag.Bool("update", false, "rewrite golden files")

const whoamiDigest = "traefik/whoami@sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"

// whoamiApp is the app equivalent of deploy/dev/whoami.json.
func whoamiApp() app.App {
	return app.App{
		ID:     "whoami",
		Name:   "whoami",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{
			Name:     "web",
			Image:    "traefik/whoami:latest",
			Ports:    []app.Port{{Name: "http", ContainerPort: 80}},
			Domains:  []app.Domain{{Host: "whoami.localhost", Port: "http"}},
			Replicas: new(2),
			Health:   &app.HealthCheck{Path: "/"},
		}},
	}
}

func whoamiDeployment() app.Deployment {
	return app.Deployment{
		ID:      "lab-manual",
		Trigger: app.TriggerManual,
		Digests: map[string]string{"web": whoamiDigest},
	}
}

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

// digest returns a valid pinned reference for repo, with a fixed fake hash.
func digest(repo string, hex byte) string {
	return repo + "@sha256:" + strings.Repeat(string(hex), 64)
}

func deployment(digests map[string]string) app.Deployment {
	return app.Deployment{ID: "d1", Trigger: app.TriggerWebhook, Digests: digests}
}

func imageApp(id string, services ...app.Service) app.App {
	return app.App{ID: id, Source: app.Source{Kind: app.SourceImage}, Services: services}
}

// blogWeb has only what's required: the replica count, resources and check
// timing all come from defaults, and the check is TCP.
func blogWeb() app.Service {
	return app.Service{
		Name:    "web",
		Image:   "ghcr.io/acme/blog:main",
		Ports:   []app.Port{{Name: "http", ContainerPort: 8080}},
		Domains: []app.Domain{{Host: "blog.example.com", Port: "http"}},
		Health:  &app.HealthCheck{},
	}
}

// jobsWorker has no ports, so it gets no service registration and no check.
func jobsWorker() app.Service {
	return app.Service{
		Name:    "worker",
		Image:   "ghcr.io/acme/jobs:main",
		Command: "/app/worker",
		Args:    []string{"--queue", "default"},
		Env: []app.EnvVar{
			{Name: "QUEUE_URL", Value: "redis://queue:6379"},
			{Name: "LOG_LEVEL", Value: "info"},
		},
	}
}

func goldenCases() []goldenCase {
	letsEncrypt := labSettings()
	letsEncrypt.CertResolver = "letsencrypt"

	customResources := whoamiApp()
	customResources.Services[0].Resources = app.Resources{CPUMHz: 500, MemoryMB: 256}
	customResources.Services[0].Health = &app.HealthCheck{Path: "/", Interval: 30 * time.Second, Timeout: 5 * time.Second}

	return []goldenCase{
		{name: "whoami", app: whoamiApp(), deployment: whoamiDeployment(), settings: labSettings()},
		{
			name:       "minimal-web",
			app:        imageApp("blog", blogWeb()),
			deployment: deployment(map[string]string{"web": digest("ghcr.io/acme/blog", 'b')}),
			settings:   labSettings(),
		},
		{
			name: "multi-domain",
			app: imageApp("shop", app.Service{
				Name:  "web",
				Image: "ghcr.io/acme/shop:main",
				Ports: []app.Port{{Name: "http", ContainerPort: 3000}, {Name: "metrics", ContainerPort: 9100}},
				Domains: []app.Domain{
					{Host: "shop.example.com", Port: "http"},
					{Host: "www.shop.example.com", Port: "http"},
				},
				Health: &app.HealthCheck{Path: "/healthz", Port: "metrics"},
			}),
			deployment: deployment(map[string]string{"web": digest("ghcr.io/acme/shop", 'c')}),
			settings:   labSettings(),
		},
		{
			name:       "worker",
			app:        imageApp("jobs", jobsWorker()),
			deployment: deployment(map[string]string{"worker": digest("ghcr.io/acme/jobs", 'd')}),
			settings:   labSettings(),
		},
		{
			name: "internal-service",
			app: imageApp("api", app.Service{
				Name:   "grpc",
				Image:  "ghcr.io/acme/api:main",
				Ports:  []app.Port{{Name: "grpc", ContainerPort: 50051}},
				Health: &app.HealthCheck{},
			}),
			deployment: deployment(map[string]string{"grpc": digest("ghcr.io/acme/api", 'e')}),
			settings:   labSettings(),
		},
		{name: "letsencrypt", app: whoamiApp(), deployment: whoamiDeployment(), settings: letsEncrypt},
		{name: "custom-resources", app: customResources, deployment: whoamiDeployment(), settings: labSettings()},
		{
			name: "multi-service",
			app:  imageApp("blog", blogWeb(), jobsWorker()),
			deployment: deployment(map[string]string{
				"web":    digest("ghcr.io/acme/blog", 'b'),
				"worker": digest("ghcr.io/acme/jobs", 'd'),
			}),
			settings: labSettings(),
		},
	}
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
		})
	}
}
