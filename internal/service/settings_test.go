package service

import (
	"context"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// StoredApps reads the apps without asking Nomad, so Settings draws when Nomad is down.
func TestStoredApps(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if apps, err := e.svc.StoredApps(ctx); err != nil || len(apps) != 0 {
		t.Fatalf("StoredApps with none = %v, %v", apps, err)
	}
	if _, err := e.store.SaveApp(ctx, kuma(false), nil); err != nil {
		t.Fatal(err)
	}
	apps, err := e.svc.StoredApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].App.ID != "whoami" || apps[0].App.Services[0].Backup == nil {
		t.Errorf("StoredApps = %+v, %v; want the stored app with its backup policy", apps, err)
	}
}

// LastBackupRun is the newest run that ended, across apps and services; a run still going doesn't count.
func TestLastBackupRun(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.store.SaveApp(ctx, kuma(false), nil); err != nil {
		t.Fatal(err)
	}
	if last, err := e.svc.LastBackupRun(ctx); err != nil || last != nil {
		t.Fatalf("LastBackupRun with no runs = %v, %v", last, err)
	}
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	save := func(id, svc, status string, at time.Time) {
		t.Helper()
		rec := store.BackupRecord{AppID: "whoami", ID: id, Service: svc, Engine: app.BackupVolume, Status: status, StartedAt: at}
		if err := e.store.SaveBackup(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	save("web-1", "web", store.BackupSucceeded, t0)
	save("db-1", "db", store.BackupFailed, t0.Add(time.Hour))
	last, err := e.svc.LastBackupRun(ctx)
	if err != nil || last == nil || last.ID != "db-1" || last.Status != store.BackupFailed {
		t.Errorf("LastBackupRun = %+v, %v; want db-1, the newest run that ended", last, err)
	}
	// A run still going says nothing yet: the newest of each service is what is compared.
	save("db-2", "db", store.BackupRunning, t0.Add(2*time.Hour))
	last, err = e.svc.LastBackupRun(ctx)
	if err != nil || last == nil || last.ID != "web-1" {
		t.Errorf("with db's newest still running, LastBackupRun = %+v, %v; want web-1", last, err)
	}
}

// BackupOutcome: the destination's newest run, and apart from it the newest failed one among the
// services' newest runs, so a failing service shows though another service ran well later.
func TestBackupOutcome(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.store.SaveApp(ctx, kuma(false), nil); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	save := func(id, svc, status string, at time.Time) {
		t.Helper()
		rec := store.BackupRecord{AppID: "whoami", ID: id, Service: svc, Engine: app.BackupVolume, Status: status, StartedAt: at}
		if err := e.store.SaveBackup(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	if last, failing, err := e.svc.BackupOutcome(ctx); err != nil || last != nil || failing != nil {
		t.Fatalf("with no runs: %v, %v, %v", last, failing, err)
	}
	save("db-1", "db", store.BackupFailed, t0)
	save("files-1", "files", store.BackupSucceeded, t0.Add(time.Hour))
	last, failing, err := e.svc.BackupOutcome(ctx)
	if err != nil || last == nil || last.ID != "files-1" || failing == nil || failing.ID != "db-1" {
		t.Errorf("BackupOutcome = %+v, %+v, %v; want files-1 as the last run and db-1 as the failing service's", last, failing, err)
	}
	// db's next run went well: nothing is failing any more.
	save("db-2", "db", store.BackupSucceeded, t0.Add(2*time.Hour))
	if _, failing, _ := e.svc.BackupOutcome(ctx); failing != nil {
		t.Errorf("db's newest run succeeded, yet %s is failing", failing.ID)
	}
	// Two failing services: the newest failure is the one reported.
	save("files-2", "files", store.BackupFailed, t0.Add(3*time.Hour))
	save("db-3", "db", store.BackupFailed, t0.Add(4*time.Hour))
	if last, failing, _ := e.svc.BackupOutcome(ctx); last.ID != "db-3" || failing.ID != "db-3" {
		t.Errorf("last %s, failing %s; want db-3 for both", last.ID, failing.ID)
	}
}
