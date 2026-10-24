package deploy

import (
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

func caseNamed(t *testing.T, name string) apptest.Case {
	t.Helper()
	for _, c := range apptest.Cases() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no apptest case %q", name)
	return apptest.Case{}
}

func TestExport(t *testing.T) {
	d, _, _ := newTestDeployer(t)
	deployed := time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC)

	t.Run("the job Register submits, after the header", func(t *testing.T) {
		w := apptest.Whoami()
		got, err := d.Export(w.App, w.Deployment, deployed, nil)
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		res, err := nomadjob.Translate(w.App, w.Deployment, labSettings, nil)
		if err != nil {
			t.Fatal(err)
		}
		body := string(nomadjob.HCL(res.Job, ""))
		if !strings.HasSuffix(string(got), body) {
			t.Errorf("export doesn't end with the translated job:\n%s", got)
		}
		for _, want := range []string{
			`# Nomad job for app "whoami", exported by Caravana.`,
			"# Deployment " + w.Deployment.ID + ", deployed 2026-10-08T12:30:00Z.",
			"# Run it with: nomad job run whoami.nomad.hcl",
			`entrypoint "websecure", Traefik's default certificate`,
			"#   whoami.localhost -> service web, port http",
		} {
			if !strings.Contains(string(got), want) {
				t.Errorf("header lacks %q:\n%s", want, got)
			}
		}
	})

	t.Run("volumes, secrets and backups are named, no secret value", func(t *testing.T) {
		vol := caseNamed(t, "volumes")
		vol.App.Services[1].Secrets = []app.Secret{{Name: "POSTGRES_PASSWORD", Value: new("hunter2-value")}}
		vol.App.Services[1].Backup = &app.Backup{Engine: app.BackupPostgres}
		got, err := d.Export(vol.App, vol.Deployment, deployed, nil)
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		for _, want := range []string{
			`# - Host volume "shop_db-data", mounted by service db.`,
			"# - Nomad Variable nomad/jobs/shop/db/caravana-volumes, which the caravana-volumes task of service db reads",
			"#   Create it before running the job, e.g. nomad var put nomad/jobs/shop/db/caravana-volumes LIMIT=1800",
			"# - Nomad Variable nomad/jobs/shop/db with the secrets POSTGRES_PASSWORD.",
			"Backups run as separate Nomad jobs (shop_db_backup)",
		} {
			if !strings.Contains(string(got), want) {
				t.Errorf("header lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(string(got), "hunter2-value") {
			t.Error("the export holds a secret value")
		}
	})

	t.Run("a private image names its credentials variable, never a credential", func(t *testing.T) {
		// Both services of the apptest "multi-service" case come from ghcr.io.
		blog := caseNamed(t, "multi-service")
		got, err := d.Export(blog.App, blog.Deployment, deployed, map[string]bool{"ghcr.io": true})
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		for _, want := range []string{
			"# - Nomad Variable nomad/jobs/blog/web/web with CARAVANA_REGISTRY_USERNAME and CARAVANA_REGISTRY_PASSWORD, which the web task reads to pull its image from ghcr.io.",
			"#   Create it before running the job, e.g. nomad var put nomad/jobs/blog/web/web CARAVANA_REGISTRY_USERNAME=<user> CARAVANA_REGISTRY_PASSWORD=<token>",
			"        auth {\n",
			`server_address = "ghcr.io"`,
		} {
			if !strings.Contains(string(got), want) {
				t.Errorf("export lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(string(got), registrySecret) {
			t.Error("the export holds the password")
		}
		// Not private: the same export as before, with neither.
		plain, err := d.Export(blog.App, blog.Deployment, deployed, nil)
		if err != nil || strings.Contains(string(plain), "CARAVANA_REGISTRY") || strings.Contains(string(plain), "auth {") {
			t.Errorf("an export with no private registry mentions one (err %v):\n%s", err, plain)
		}
	})

	t.Run("a Team-only domain names its middleware, never a hash", func(t *testing.T) {
		a := app.App{
			ID:     "shop",
			Source: app.Source{Kind: app.SourceImage},
			Services: []app.Service{{
				Name:  "web",
				Image: "traefik/whoami:v1.10",
				Ports: []app.Port{{Name: "http", ContainerPort: 80}},
				Domains: []app.Domain{
					{Host: "shop.example.com", Port: "http", Path: "/api"},
					{Host: "example.com", Port: "http", WWWRedirect: true},
					{Host: "staging.example.com", Port: "http", TeamOnly: true},
				},
				Health: &app.HealthCheck{Path: "/"},
			}},
		}
		dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
			"web": "traefik/whoami@sha256:" + strings.Repeat("a", 64),
		}}
		got, err := d.Export(a, dep, deployed, nil)
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		router := nomadjob.DomainRouterName("shop", "web", "staging.example.com")
		for _, want := range []string{
			"#   shop.example.com -> service web, port http (path /api)",
			"#   example.com -> service web, port http (redirects www.example.com here)",
			"#   staging.example.com -> service web, port http (team only)",
			"# - Traefik file-provider middleware " + router + "-auth@file (basic auth) for staging.example.com",
			"Nomad Variable nomad/jobs/traefik, item " + router + "-auth = team:<bcrypt hash>",
		} {
			if !strings.Contains(string(got), want) {
				t.Errorf("header lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(string(got), "$2a$") || strings.Contains(string(got), "$2b$") {
			t.Error("the export holds a bcrypt hash")
		}
		// No Team-only domain: no middleware line.
		a.Services[0].Domains = a.Services[0].Domains[:2]
		plain, err := d.Export(a, dep, deployed, nil)
		if err != nil || strings.Contains(string(plain), "file-provider") {
			t.Errorf("an export without a Team-only domain mentions a middleware (err %v)", err)
		}
	})

	t.Run("a worker needs nothing more", func(t *testing.T) {
		wk := caseNamed(t, "worker")
		got, err := d.Export(wk.App, wk.Deployment, deployed, nil)
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		if !strings.Contains(string(got), "# - Nothing beyond a Nomad client with Docker.") {
			t.Errorf("header:\n%s", got)
		}
	})

	t.Run("digests that don't fit are an error", func(t *testing.T) {
		w := apptest.Whoami()
		if _, err := d.Export(w.App, app.Deployment{ID: "x", Trigger: app.TriggerManual}, deployed, nil); err == nil {
			t.Error("Export() with no digests = nil, want an error")
		}
	})
}
