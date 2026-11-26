package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
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

// SetRestoreSchedule (feature 036) changes only the restore schedule of a service that has a
// backup policy, on the revision the caller read, and removes it with nil.
func TestSetRestoreSchedule(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, kuma(false))
	rec, _ := e.svc.GetApp(ctx, "whoami")
	schedule := &app.RestoreSchedule{Schedule: "0 4 * * *", From: "golden"}

	got, err := e.svc.SetRestoreSchedule(ctx, "whoami", "web", schedule, Condition{IfRevision: rec.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	if got.Deployment.Trigger != app.TriggerConsole {
		t.Errorf("trigger = %s, want console", got.Deployment.Trigger)
	}
	waitFinal(t, e.store, got.Deployment.ID)
	stored, _ := e.svc.GetApp(ctx, "whoami")
	want := kuma(false)
	want.Services[0].Backup.Restore = schedule
	if !reflect.DeepEqual(stored.App, want.WithoutSecretValues()) {
		t.Errorf("stored app\n got: %+v\nwant: %+v", stored.App, want)
	}
	warned := false
	for _, w := range got.Warnings {
		warned = warned || w.Field == "services[0].backup.restore.from"
	}
	if !warned {
		t.Errorf("warnings = %+v, want the one for a label nothing is pinned under", got.Warnings)
	}

	t.Run("nil removes it", func(t *testing.T) {
		got, err := e.svc.SetRestoreSchedule(ctx, "whoami", "web", nil, Condition{}, app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, got.Deployment.ID)
		stored, _ := e.svc.GetApp(ctx, "whoami")
		if stored.App.Services[0].Backup == nil || stored.App.Services[0].Backup.Restore != nil || stored.App.Services[0].Backup.Engine != app.BackupVolume {
			t.Errorf("backup = %+v, want the policy without a restore schedule", stored.App.Services[0].Backup)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		n := len(mustDeployments(t, e))
		var fe *app.FieldError
		if _, err := e.svc.SetRestoreSchedule(ctx, "whoami", "nope", schedule, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
			t.Errorf("an unknown service: %v", err)
		}
		if _, err := e.svc.SetRestoreSchedule(ctx, "ghost", "web", schedule, Condition{}, app.TriggerConsole); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an unknown app: %v", err)
		}
		bad := &app.RestoreSchedule{Schedule: "nightly", From: "golden"}
		if _, err := e.svc.SetRestoreSchedule(ctx, "whoami", "web", bad, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "services[0].backup.restore.schedule" {
			t.Errorf("a bad schedule: %v", err)
		}
		if _, err := e.svc.SetRestoreSchedule(ctx, "whoami", "web", schedule, Condition{IfRevision: "0000000000000000"}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
			t.Errorf("a stale revision: %v", err)
		}
		if len(mustDeployments(t, e)) != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("a service with no policy", func(t *testing.T) {
		e := newEnv(t)
		a := db()
		a.Services[0].Backup = nil
		deployHealthy(t, e, a)
		var fe *app.FieldError
		if _, err := e.svc.SetRestoreSchedule(ctx, "whoami", "web", schedule, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
			t.Errorf("no policy: %v, want a field error on service", err)
		}
	})
}

// A kept Compose file keeps its text, and its overlay's backup follows the schedule.
func TestSetRestoreScheduleKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	const file = "# kuma\nservices:\n  web:\n    image: traefik/whoami:latest\n    volumes: [\"data:/data\"]\nvolumes:\n  data: {}\n"
	req := compose.Request{Compose: file, Services: map[string]compose.Overlay{
		"web": {Backup: &app.Backup{Engine: app.BackupVolume, Keep: 3}},
	}}
	composeHealthy(t, e, req)
	got, err := e.svc.SetRestoreSchedule(ctx, "whoami", "web", &app.RestoreSchedule{Schedule: "0 4 * * *", From: "golden"}, Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, got.Deployment.ID)
	stored, _ := e.svc.GetApp(ctx, "whoami")
	if stored.Compose == nil || stored.Compose.Compose != file {
		t.Fatalf("kept file = %+v, want the same text", stored.Compose)
	}
	web := stored.Compose.Services["web"]
	if web.Backup == nil || web.Backup.Restore == nil || web.Backup.Restore.From != "golden" || web.Backup.Keep != 3 {
		t.Errorf("overlay = %+v, want the schedule set and the rest kept", web)
	}
}

// SetBackupPlaces (feature 052) changes only where a service's backups go and are copied, on the
// revision the caller read, and refuses a destination that doesn't exist at its field.
func TestSetBackupPlaces(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	saveDestination(t, e, "offsite", destinationSentinel)
	deployHealthy(t, e, kuma(false))
	rec, _ := e.svc.GetApp(ctx, "whoami")

	got, err := e.svc.SetBackupPlaces(ctx, "whoami", "web", "", []string{"offsite"}, 14, Condition{IfRevision: rec.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	if got.Deployment.Trigger != app.TriggerConsole {
		t.Errorf("trigger = %s, want console", got.Deployment.Trigger)
	}
	waitFinal(t, e.store, got.Deployment.ID)
	stored, _ := e.svc.GetApp(ctx, "whoami")
	b := stored.App.Services[0].Backup
	if b == nil || b.Engine != app.BackupVolume || b.Destination != "" || len(b.CopyTo) != 1 || b.CopyTo[0] != "offsite" || b.CopyKeep != 14 {
		t.Errorf("backup = %+v, want the policy with offsite as a copy kept 14", b)
	}

	t.Run("removing the copies", func(t *testing.T) {
		got, err := e.svc.SetBackupPlaces(ctx, "whoami", "web", "", nil, 0, Condition{}, app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, got.Deployment.ID)
		stored, _ := e.svc.GetApp(ctx, "whoami")
		if b := stored.App.Services[0].Backup; b == nil || len(b.CopyTo) != 0 || b.CopyKeep != 0 || b.Engine != app.BackupVolume {
			t.Errorf("backup = %+v, want no copies and the rest kept", b)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		n := len(mustDeployments(t, e))
		var fe *app.FieldError
		if _, err := e.svc.SetBackupPlaces(ctx, "whoami", "web", "nowhere", nil, 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "services[0].backup.destination" {
			t.Errorf("an unknown destination: %v, want a field error on its field", err)
		}
		if _, err := e.svc.SetBackupPlaces(ctx, "whoami", "web", "", []string{"nowhere"}, 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "services[0].backup.copyTo[0]" {
			t.Errorf("an unknown copy: %v", err)
		}
		if _, err := e.svc.SetBackupPlaces(ctx, "whoami", "nope", "", nil, 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
			t.Errorf("an unknown service: %v", err)
		}
		if _, err := e.svc.SetBackupPlaces(ctx, "ghost", "web", "", nil, 0, Condition{}, app.TriggerConsole); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an unknown app: %v", err)
		}
		if _, err := e.svc.SetBackupPlaces(ctx, "whoami", "web", "", nil, 0, Condition{IfRevision: "0000000000000000"}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
			t.Errorf("a stale revision: %v", err)
		}
		if len(mustDeployments(t, e)) != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("a kept file follows", func(t *testing.T) {
		e := newEnv(t)
		setDestination(t, e)
		saveDestination(t, e, "offsite", destinationSentinel)
		const file = "# kuma\nservices:\n  web:\n    image: traefik/whoami:latest\n    volumes: [\"data:/data\"]\nvolumes:\n  data: {}\n"
		composeHealthy(t, e, compose.Request{Compose: file, Services: map[string]compose.Overlay{
			"web": {Backup: &app.Backup{Engine: app.BackupVolume, Keep: 3}},
		}})
		got, err := e.svc.SetBackupPlaces(ctx, "whoami", "web", "", []string{"offsite"}, 0, Condition{}, app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, got.Deployment.ID)
		stored, _ := e.svc.GetApp(ctx, "whoami")
		web := stored.Compose.Services["web"]
		if stored.Compose.Compose != file || web.Backup == nil || len(web.Backup.CopyTo) != 1 || web.Backup.Keep != 3 {
			t.Errorf("kept file = %+v, want the same text and the overlay's backup with the copy", stored.Compose)
		}
	})
}

// ChangeBackupPolicy (feature 059) replaces the engine, file, schedule and keep of a policy the
// service already has, and leaves its restore schedule, destination and copies as they were.
func TestChangeBackupPolicy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	saveDestination(t, e, "offsite", destinationSentinel)
	a := kuma(false)
	a.Services[0].Backup = &app.Backup{Engine: app.BackupVolume, Keep: 3, Schedule: "0 5 * * *",
		Restore: &app.RestoreSchedule{Schedule: "0 6 * * *", From: "golden"}, CopyTo: []string{"offsite"}, CopyKeep: 4}
	deployHealthy(t, e, a)
	rec, _ := e.svc.GetApp(ctx, "whoami")

	got, err := e.svc.ChangeBackupPolicy(ctx, "whoami", "web", app.BackupVolume, "", app.BackupScheduleNever, 9, Condition{IfRevision: rec.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("ChangeBackupPolicy: %v", err)
	}
	if got.Deployment.Trigger != app.TriggerConsole {
		t.Errorf("trigger = %s, want console", got.Deployment.Trigger)
	}
	waitFinal(t, e.store, got.Deployment.ID)
	stored, _ := e.svc.GetApp(ctx, "whoami")
	b := stored.App.Services[0].Backup
	want := app.Backup{Engine: app.BackupVolume, Keep: 9, Schedule: app.BackupScheduleNever,
		Restore: &app.RestoreSchedule{Schedule: "0 6 * * *", From: "golden"}, CopyTo: []string{"offsite"}, CopyKeep: 4}
	if b == nil || !reflect.DeepEqual(*b, want) {
		t.Errorf("backup = %+v, want %+v", b, want)
	}

	t.Run("refusals", func(t *testing.T) {
		n := len(mustDeployments(t, e))
		var fe *app.FieldError
		if _, err := e.svc.ChangeBackupPolicy(ctx, "whoami", "nope", app.BackupVolume, "", "", 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
			t.Errorf("an unknown service: %v", err)
		}
		if _, err := e.svc.ChangeBackupPolicy(ctx, "ghost", "web", app.BackupVolume, "", "", 0, Condition{}, app.TriggerConsole); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an unknown app: %v", err)
		}
		if _, err := e.svc.ChangeBackupPolicy(ctx, "whoami", "web", app.BackupVolume, "", "nightly", 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "services[0].backup.schedule" {
			t.Errorf("a bad schedule: %v, want a field error on services[0].backup.schedule", err)
		}
		if _, err := e.svc.ChangeBackupPolicy(ctx, "whoami", "web", app.BackupVolume, "/data/app.db", "", 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || !strings.HasPrefix(fe.Field, "services[0].backup") {
			t.Errorf("a file for a volume policy: %v, want a field error on the backup", err)
		}
		if _, err := e.svc.ChangeBackupPolicy(ctx, "whoami", "web", app.BackupVolume, "", "", 0, Condition{IfRevision: "0000000000000000"}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
			t.Errorf("a stale revision: %v", err)
		}
		if len(mustDeployments(t, e)) != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("a service with no policy", func(t *testing.T) {
		e := newEnv(t)
		a := db()
		a.Services[0].Backup = nil
		deployHealthy(t, e, a)
		var fe *app.FieldError
		if _, err := e.svc.ChangeBackupPolicy(ctx, "whoami", "web", app.BackupPostgres, "", "", 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
			t.Errorf("no policy: %v, want a field error on service", err)
		}
	})
}

// A kept Compose file keeps its text, and its overlay's backup follows the change.
func TestChangeBackupPolicyKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	const file = "# kuma\nservices:\n  web:\n    image: traefik/whoami:latest\n    volumes: [\"data:/data\"]\nvolumes:\n  data: {}\n"
	req := compose.Request{Compose: file, Services: map[string]compose.Overlay{
		"web": {Backup: &app.Backup{Engine: app.BackupVolume, Keep: 3, Restore: &app.RestoreSchedule{Schedule: "0 4 * * *", From: "golden"}}},
	}}
	composeHealthy(t, e, req)
	got, err := e.svc.ChangeBackupPolicy(ctx, "whoami", "web", app.BackupVolume, "", app.BackupScheduleNever, 8, Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, got.Deployment.ID)
	stored, _ := e.svc.GetApp(ctx, "whoami")
	if stored.Compose == nil || stored.Compose.Compose != file {
		t.Fatalf("kept file = %+v, want the same text", stored.Compose)
	}
	web := stored.Compose.Services["web"]
	if web.Backup == nil || web.Backup.Keep != 8 || web.Backup.Schedule != app.BackupScheduleNever || web.Backup.Restore == nil || web.Backup.Restore.From != "golden" {
		t.Errorf("overlay = %+v, want the new policy with the restore schedule kept", web)
	}
}
