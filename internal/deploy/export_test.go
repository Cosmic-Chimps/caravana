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
		got, err := d.Export(w.App, w.Deployment, deployed)
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		res, err := nomadjob.Translate(w.App, w.Deployment, labSettings)
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
		got, err := d.Export(vol.App, vol.Deployment, deployed)
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

	t.Run("a worker needs nothing more", func(t *testing.T) {
		wk := caseNamed(t, "worker")
		got, err := d.Export(wk.App, wk.Deployment, deployed)
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		if !strings.Contains(string(got), "# - Nothing beyond a Nomad client with Docker.") {
			t.Errorf("header:\n%s", got)
		}
	})

	t.Run("digests that don't fit are an error", func(t *testing.T) {
		w := apptest.Whoami()
		if _, err := d.Export(w.App, app.Deployment{ID: "x", Trigger: app.TriggerManual}, deployed); err == nil {
			t.Error("Export() with no digests = nil, want an error")
		}
	})
}
