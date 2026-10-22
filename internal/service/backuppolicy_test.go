package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 021: SetBackupPolicy is a deploy of the stored app with one service's backup set, on
// the revision the caller read; the secrets and the rest of the app stay.
func TestSetBackupPolicy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	a := db()
	a.Services[0].Backup = nil
	deployHealthy(t, e, a)
	rec, _ := e.svc.GetApp(ctx, "whoami")

	policy := app.Backup{Engine: app.BackupPostgres, Schedule: "0 3 * * *", Keep: 7}
	got, err := e.svc.SetBackupPolicy(ctx, "whoami", "web", policy, Condition{IfRevision: rec.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	if got.Deployment.Trigger != app.TriggerConsole {
		t.Errorf("trigger = %s, want console", got.Deployment.Trigger)
	}
	if final := waitFinal(t, e.store, got.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("deploy = %s (%s)", final.Status, final.Error)
	}
	stored, _ := e.svc.GetApp(ctx, "whoami")
	want := a.WithoutSecretValues()
	want.Services[0].Backup = &policy
	if !reflect.DeepEqual(stored.App, want) {
		t.Errorf("stored app\n got: %+v\nwant: %+v", stored.App, want)
	}
	if live := e.secrets.Live("whoami"); !live.Equal(secrets.Sets{"web": {"POSTGRES_PASSWORD": marker}}) {
		t.Errorf("the secret's value didn't survive: %v", live)
	}

	t.Run("a stale revision", func(t *testing.T) {
		e := newEnv(t)
		setDestination(t, e)
		a := db()
		a.Services[0].Backup = nil
		deployHealthy(t, e, a)
		read, _ := e.svc.GetApp(ctx, "whoami")
		a.Services[0].Resources.MemoryMB = 99 // another writer
		deployHealthy(t, e, a)
		n := len(mustDeployments(t, e))
		if _, err := e.svc.SetBackupPolicy(ctx, "whoami", "web", policy, Condition{IfRevision: read.Revision}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
			t.Errorf("a stale revision: %v, want ErrAppChanged", err)
		}
		if len(mustDeployments(t, e)) != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("refusals", func(t *testing.T) {
		n := len(mustDeployments(t, e))
		var fe *app.FieldError
		if _, err := e.svc.SetBackupPolicy(ctx, "whoami", "web", policy, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
			t.Errorf("a service that has one already: %v, want a field error on service", err)
		}
		if _, err := e.svc.SetBackupPolicy(ctx, "whoami", "nope", policy, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
			t.Errorf("an unknown service: %v", err)
		}
		if _, err := e.svc.SetBackupPolicy(ctx, "ghost", "web", policy, Condition{}, app.TriggerConsole); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an unknown app: %v", err)
		}
		if len(mustDeployments(t, e)) != n {
			t.Error("a refused change deployed")
		}
	})
}

func mustDeployments(t *testing.T, e *env) []store.DeploymentRecord {
	t.Helper()
	recs, err := e.store.ListDeployments(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// An app that keeps its Compose file keeps it through SetBackupPolicy: the overlay's backup follows
// the app, the rest of the overlay stays, and the file's text is as it was.
func TestSetBackupPolicyKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	const file = "# a database\nservices:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"5432\"]\n"
	req := compose.Request{Compose: file, Services: map[string]compose.Overlay{
		"web": {Secrets: []app.Secret{{Name: "POSTGRES_PASSWORD", Value: new(keptSentinel)}}, Health: &compose.Health{}},
	}}
	composeHealthy(t, e, req)

	rec, err := e.svc.SetBackupPolicy(ctx, "whoami", "web", app.Backup{Engine: app.BackupPostgres, Schedule: "0 3 * * *", Keep: 7}, Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("SetBackupPolicy: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if n := droppedFile(rec); n != 0 {
		t.Errorf("warnings = %+v, want no drop warning", rec.Warnings)
	}
	stored, _ := e.svc.GetApp(ctx, "whoami")
	if stored.Compose == nil || stored.Compose.Compose != file {
		t.Fatalf("kept file = %+v, want the same text", stored.Compose)
	}
	web := stored.Compose.Services["web"]
	if web.Backup == nil || web.Backup.Engine != app.BackupPostgres || web.Backup.Keep != 7 || web.Health == nil || len(web.Secrets) != 1 || web.Secrets[0].Name != "POSTGRES_PASSWORD" || web.Secrets[0].Value != nil {
		t.Errorf("the overlay = %+v, want the backup set and the health check and the secret's name kept", web)
	}
}

// SetSecretIf and RemoveSecretIf refuse a revision that isn't the stored app's, and change nothing.
func TestSecretEditsOnARevision(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))
	rec, _ := e.svc.GetApp(ctx, "whoami")

	if _, err := e.svc.SetSecretIf(ctx, "whoami", "web", "NEW", "v", Condition{IfRevision: "0000000000000000"}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
		t.Errorf("SetSecretIf on a stale revision: %v", err)
	}
	if _, err := e.svc.RemoveSecretIf(ctx, "whoami", "web", "API_KEY", Condition{IfRevision: "0000000000000000"}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
		t.Errorf("RemoveSecretIf on a stale revision: %v", err)
	}
	if live := e.secrets.Live("whoami"); !live.Equal(secrets.Sets{"web": {"API_KEY": "k1"}}) {
		t.Errorf("a refused edit touched the secrets: %v", live)
	}
	got, err := e.svc.SetSecretIf(ctx, "whoami", "web", "NEW", "v", Condition{IfRevision: rec.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, got.Deployment.ID)
	if live := e.secrets.Live("whoami"); !live.Equal(secrets.Sets{"web": {"API_KEY": "k1", "NEW": "v"}}) {
		t.Errorf("after the edit on the right revision: %v", live)
	}
}
