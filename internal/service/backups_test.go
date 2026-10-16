package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// db is whoami (which the fake registry resolves) acting as a postgres
// service with a backup policy and a password secret.
func db() app.App {
	a := whoami()
	a.Services[0].Replicas = nil
	a.Services[0].Secrets = []app.Secret{set("POSTGRES_PASSWORD", marker)}
	a.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres, Keep: 2}
	return a
}

func setDestination(t *testing.T, e *env) {
	t.Helper()
	_, err := e.svc.SetBackupDestination(context.Background(), DestinationInput{
		Endpoint: "http://10.0.0.5:9000", Region: "us-east-1", Bucket: "backups", Prefix: "/caravana/",
		AccessKeyID: "AK", SecretAccessKey: new("SK-" + marker)})
	if err != nil {
		t.Fatalf("SetBackupDestination: %v", err)
	}
}

// US3: the destination keeps its secret out of every read, and a second save
// without it keeps it.
func TestBackupDestination(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.GetBackupDestination(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	_, err := e.svc.SetBackupDestination(ctx, DestinationInput{Endpoint: "http://x:9000", Region: "r", Bucket: "backups", AccessKeyID: "AK"})
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "secretAccessKey" {
		t.Fatalf("first save without a secret = %v, want a field error on secretAccessKey", err)
	}
	for _, bad := range []struct {
		in    DestinationInput
		field string
	}{
		{DestinationInput{Endpoint: "ftp://x", Region: "r", Bucket: "b1", AccessKeyID: "AK", SecretAccessKey: new("s")}, "endpoint"},
		{DestinationInput{Endpoint: "http://x", Region: "r", Bucket: "Bad_Bucket", AccessKeyID: "AK", SecretAccessKey: new("s")}, "bucket"},
	} {
		if _, err := e.svc.SetBackupDestination(ctx, bad.in); !errors.As(err, &fe) || fe.Field != bad.field {
			t.Errorf("Set(%+v) = %v, want a field error on %s", bad.in, err, bad.field)
		}
	}

	setDestination(t, e)
	got, err := e.svc.GetBackupDestination(ctx)
	if err != nil || got.Bucket != "backups" || got.Prefix != "caravana" || got.AccessKeyID != "AK" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := e.svc.SetBackupDestination(ctx, DestinationInput{Endpoint: "http://10.0.0.6:9000", Region: "us-east-1", Bucket: "backups2", AccessKeyID: "AK"}); err != nil {
		t.Fatalf("second save without the secret: %v", err)
	}
	if secret, _ := e.svc.deployer.BackupSecret(ctx); secret != "SK-"+marker {
		t.Errorf("secret after a save without one = %q, want it kept", secret)
	}
}

// US1: a backed-up service gets its job; runs are recorded; retention marks
// the old ones; back up now refuses a second run.
func TestBackups(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	_, err := e.svc.Deploy(ctx, db(), app.TriggerManual)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "services[0].backup" {
		t.Fatalf("Deploy without a destination = %v, want a field error on services[0].backup", err)
	}

	setDestination(t, e)
	deployHealthy(t, e, db())
	v := e.nomad.Variable("nomad/jobs/whoami_web_backup")
	if v["PGPASSWORD"] != marker || v["PGUSER"] != "postgres" || v["BACKUP_PATH"] != "backups/caravana/whoami/web" {
		t.Fatalf("backup variable = %v", v)
	}
	if job := e.nomad.Job("whoami_web_backup"); job == nil || strings.Contains(mustMarshal(t, job), marker) {
		t.Fatal("no backup job, or it holds the password")
	}

	first, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil || first.Status != store.BackupRunning {
		t.Fatalf("BackUpNow = %+v, %v", first, err)
	}
	if _, err := e.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrBackupRunning) {
		t.Errorf("second BackUpNow = %v, want ErrBackupRunning", err)
	}
	e.nomad.FinishRun(first.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/1.dump bytes=1499\n", "")
	waitFor(t, "the first backup to be recorded", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", first.ID)
		return err == nil && b.Status == store.BackupSucceeded
	})

	second, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	firstFile := first.JobID[strings.LastIndex(first.JobID, "-")+1:] + ".dump"
	e.nomad.FinishRun(second.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/2.dump bytes=10\ncaravana-expired "+firstFile+"\n", "")
	waitFor(t, "retention to be recorded", func() bool {
		live, _, err := e.svc.ListBackups(ctx, "whoami")
		return err == nil && len(live) == 1 && live[0].ID == second.ID && live[0].Bytes == 10
	})

	third, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	e.nomad.FinishRun(third.JobID, false, "dump", "", "pg_dump: error: password authentication failed\n")
	waitFor(t, "the failure to be recorded", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", third.ID)
		return err == nil && b.Status == store.BackupFailed && strings.Contains(b.Message, "password authentication failed")
	})

	// Without the policy, the job goes; the records stay with the app.
	plain := db()
	plain.Services[0].Backup = nil
	deployHealthy(t, e, plain)
	if e.nomad.Job("whoami_web_backup") != nil {
		t.Error("the backup job is still there")
	}
	if _, err := e.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("BackUpNow without a policy = %v, want ErrNotFound", err)
	}
}

// US2: a restore runs once at a time and is recorded when it ends; FR-011:
// deleting the app takes its jobs.
func TestRestore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, db())
	b, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Restore(ctx, "whoami", b.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Restore of a running backup = %v, want ErrNotFound", err)
	}
	e.nomad.FinishRun(b.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/1.dump bytes=1499\n", "")
	waitFor(t, "the backup to be recorded", func() bool {
		got, err := e.store.GetBackup(ctx, "whoami", b.ID)
		return err == nil && got.Status == store.BackupSucceeded
	})

	r, err := e.svc.Restore(ctx, "whoami", b.ID)
	if err != nil || r.Status != store.BackupRunning {
		t.Fatalf("Restore = %+v, %v", r, err)
	}
	if job := e.nomad.Job("whoami_web_restore"); job == nil || job.Meta["backup_key"] != "backups/caravana/whoami/web/1.dump" {
		t.Fatalf("restore job = %+v", job)
	}
	if _, err := e.svc.Restore(ctx, "whoami", b.ID); !errors.Is(err, ErrBackupRunning) {
		t.Errorf("a second restore = %v, want ErrBackupRunning", err)
	}
	e.nomad.FinishRun("whoami_web_restore", true, "", "", "")
	waitFor(t, "the restore to be recorded", func() bool {
		_, restores, err := e.svc.ListBackups(ctx, "whoami")
		return err == nil && len(restores) == 1 && restores[0].Status == store.BackupSucceeded
	})

	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if e.nomad.Job("whoami_web_backup") != nil || e.nomad.Job("whoami_web_restore") != nil || len(e.nomad.Runs("whoami_web_backup")) != 0 {
		t.Error("the app's backup or restore jobs are still there")
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// kuma is whoami as a service that keeps its data in a volume, backed up with
// the volume engine. It has no password to give and, when bare, no port.
func kuma(bare bool) app.App {
	a := withData()
	if bare {
		a.Services[0].Ports, a.Services[0].Domains, a.Services[0].Health = nil, nil, nil
	}
	a.Services[0].Backup = &app.Backup{Engine: app.BackupVolume, Keep: 3}
	return a
}

// A volume backup needs a destination and nothing else (feature 016): no
// port, no credentials, and no Nomad job.
func TestVolumeBackupDeploys(t *testing.T) {
	ctx := context.Background()
	for _, bare := range []bool{false, true} {
		e := newEnv(t)
		_, err := e.svc.Deploy(ctx, kuma(bare), app.TriggerManual)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].backup" || !strings.Contains(fe.Message, "no backup destination") {
			t.Fatalf("bare=%v: Deploy without a destination = %v, want the destination's field error", bare, err)
		}

		setDestination(t, e)
		final := deployHealthy(t, e, kuma(bare))
		if len(final.Warnings) != 0 {
			t.Errorf("bare=%v: warnings = %+v, want none", bare, final.Warnings)
		}
		if e.nomad.Job("whoami_web_backup") != nil || e.nomad.Variable("nomad/jobs/whoami_web_backup") != nil {
			t.Errorf("bare=%v: a volume backup made a Nomad backup job", bare)
		}
		v := e.nomad.Variable("nomad/jobs/whoami/web/caravana-volumes")
		if v == nil || v["KEEP"] != "3" || v["REQUEST"] != "" {
			t.Errorf("bare=%v: the task's variable = %v, want KEEP 3 and no request", bare, v)
		}
	}
}

// A service that moves from Postgres to volume backups loses its Nomad job.
func TestSwitchingEngineDropsTheJob(t *testing.T) {
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, db())
	if e.nomad.Job("whoami_web_backup") == nil {
		t.Fatal("no backup job for the postgres service")
	}
	switched := db()
	switched.Services[0].Secrets = nil
	switched.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	switched.Services[0].Backup = &app.Backup{Engine: app.BackupVolume, Keep: 2}
	deployHealthy(t, e, switched)
	if e.nomad.Job("whoami_web_backup") != nil || e.nomad.Variable("nomad/jobs/whoami_web_backup") != nil {
		t.Error("the postgres backup job or its variable is still there")
	}
}

// The watcher records Nomad's runs of Postgres backups, and leaves a volume
// service's records to the run that makes them.
func TestRecordingIgnoresVolumeBackups(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, db())
	run, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	e.nomad.FinishRun(run.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/1.dump bytes=7\n", "")

	// A pass over a volume service reads no Nomad job: the record stays as it
	// was, though Nomad's run of the old job has finished.
	e.svc.recordApp(ctx, kuma(false))
	got, err := e.store.GetBackup(ctx, "whoami", run.ID)
	if err != nil || got.Status != store.BackupRunning {
		t.Fatalf("record after a pass over a volume service = %+v, %v; want it left running", got, err)
	}
	// Over the Postgres definition, the same pass settles it.
	e.svc.recordApp(ctx, db())
	if got, _ := e.store.GetBackup(ctx, "whoami", run.ID); got.Status != store.BackupSucceeded {
		t.Errorf("record after a pass over a postgres service = %+v, want succeeded", got)
	}

	// A running volume restore isn't looked up as a Nomad job either.
	r := store.RestoreRecord{ID: "r1", AppID: "whoami", Service: "web", BackupID: "x", Engine: app.BackupVolume,
		Status: store.BackupRunning, CreatedAt: time.Now().UTC()}
	if err := e.store.SaveRestore(ctx, r); err != nil {
		t.Fatal(err)
	}
	e.svc.recordRestores(ctx)
	restores, err := e.store.ListRestores(ctx, "whoami", 0)
	if err != nil || len(restores) != 1 || restores[0].Status != store.BackupRunning {
		t.Errorf("restores after a pass = %+v, %v; want the volume restore left running", restores, err)
	}
}

// A volume service is busy when the store says a run of it is going; a
// Postgres one, when Nomad does (restores: the store, for both).
func TestBackupBusy(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	volumeSvc := kuma(false).Services[0]
	postgresSvc := db().Services[0]

	tests := []struct {
		name    string
		svc     app.Service
		backup  string // status of a stored backup record of the service; "" for none
		restore string // status of a stored restore record
		want    bool
	}{
		{"volume, nothing recorded", volumeSvc, "", "", false},
		{"volume, a backup running", volumeSvc, store.BackupRunning, "", true},
		{"volume, a backup done", volumeSvc, store.BackupSucceeded, "", false},
		{"volume, a restore running", volumeSvc, "", store.BackupRunning, true},
		{"volume, a restore done", volumeSvc, "", store.BackupFailed, false},
		{"postgres, a stored backup running is Nomad's to say", postgresSvc, store.BackupRunning, "", false},
		{"postgres, a restore running", postgresSvc, "", store.BackupRunning, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if _, err := e.store.SaveApp(ctx, kuma(false), nil); err != nil {
				t.Fatal(err)
			}
			if tt.backup != "" {
				rec := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume, Status: tt.backup, StartedAt: t0}
				if err := e.store.SaveBackup(ctx, rec); err != nil {
					t.Fatal(err)
				}
			}
			if tt.restore != "" {
				r := store.RestoreRecord{ID: "r1", AppID: "whoami", Service: "web", BackupID: "web-1", Engine: app.BackupVolume, Status: tt.restore, CreatedAt: t0}
				if err := e.store.SaveRestore(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			got, err := e.svc.backupBusy(ctx, "whoami", tt.svc)
			if err != nil || got != tt.want {
				t.Errorf("backupBusy = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// Restore refuses a volume restore with no running copy, a
// second run and a backup of the other engine, and the list shows both
// engines' records.
func TestVolumeRunRefusals(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, kuma(false))
	t0 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	done := t0.Add(time.Minute)

	saved := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume, Status: store.BackupSucceeded,
		Object: "backups/caravana/whoami/web/1/", StartedAt: t0, FinishedAt: &done}
	if err := e.store.SaveBackup(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Restore(ctx, "whoami", "web-1"); !errors.Is(err, ErrNoRunningCopy) {
		t.Errorf("Restore = %v, want ErrNoRunningCopy", err)
	}

	// A backup made by the other engine isn't restored into this service.
	old := saved
	old.ID, old.Engine = "web-0", "postgres"
	if err := e.store.SaveBackup(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Restore(ctx, "whoami", "web-0"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Restore of a postgres backup into a volume service = %v, want ErrNotFound", err)
	}

	// A running volume run blocks another.
	running := store.BackupRecord{AppID: "whoami", ID: "web-2", Service: "web", Engine: app.BackupVolume, Status: store.BackupRunning, StartedAt: t0.Add(time.Hour)}
	if err := e.store.SaveBackup(ctx, running); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrBackupRunning) {
		t.Errorf("BackUpNow during a run = %v, want ErrBackupRunning", err)
	}
	if _, err := e.svc.Restore(ctx, "whoami", "web-1"); !errors.Is(err, ErrBackupRunning) {
		t.Errorf("Restore during a run = %v, want ErrBackupRunning", err)
	}

	live, _, err := e.svc.ListBackups(ctx, "whoami")
	if err != nil || len(live) != 3 {
		t.Fatalf("ListBackups = %d records, %v; want all 3", len(live), err)
	}
	engines := map[string]string{}
	for _, b := range live {
		engines[b.ID] = b.Engine
	}
	if engines["web-1"] != "volume" || engines["web-0"] != "postgres" || engines["web-2"] != "volume" {
		t.Errorf("engines = %v", engines)
	}
}
