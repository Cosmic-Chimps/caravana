package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
