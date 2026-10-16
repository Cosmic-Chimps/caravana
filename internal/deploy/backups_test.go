package deploy

import (
	"context"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// backedUp is an app with a postgres service that has a backup policy.
func backedUp() app.App {
	return app.App{ID: "shop", Source: app.Source{Kind: app.SourceCompose}, Services: []app.Service{{
		Name: "db", Image: "postgres:17",
		Ports:  []app.Port{{Name: "pg", ContainerPort: 5432}},
		Env:    []app.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pw"}},
		Health: &app.HealthCheck{},
		Backup: &app.Backup{Engine: app.BackupPostgres, Keep: 2},
	}}}
}

var testDest = BackupDestination{Endpoint: "http://10.0.0.5:9000", Region: "us-east-1", Bucket: "backups", Prefix: "caravana",
	AccessKeyID: "AK", SecretAccessKey: "SK"}

func TestSyncBackups(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := backedUp()
	digests := map[string]string{"db": "postgres@sha256:" + whoamiDigest[7:]}
	creds := map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}

	if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil); err != nil {
		t.Fatalf("SyncBackups: %v", err)
	}
	job := nomad.Job("shop_db_backup")
	if job == nil || job.Periodic == nil {
		t.Fatalf("backup job = %+v", job)
	}
	v := nomad.Variable("nomad/jobs/shop_db_backup")
	if v["PGPASSWORD"] != "pw" || v["BACKUP_PATH"] != "backups/caravana/shop/db" || v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK" {
		t.Errorf("variable = %v", v)
	}

	// A new destination reaches the variable without a deploy, keeping the
	// credentials.
	moved := testDest
	moved.Bucket, moved.Prefix = "other", ""
	if err := d.SetBackupDestination(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable("nomad/jobs/shop_db_backup"); v["BACKUP_PATH"] != "other/shop/db" || v["PGPASSWORD"] != "pw" {
		t.Errorf("variable after the move = %v", v)
	}

	// Without the policy, the job goes, with its runs and its variable.
	if _, err := d.ForceBackup(ctx, "shop", "db"); err != nil {
		t.Fatal(err)
	}
	a.Services[0].Backup = nil
	if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil); err != nil {
		t.Fatal(err)
	}
	if nomad.Job("shop_db_backup") != nil || len(nomad.Runs("shop_db_backup")) != 0 || nomad.Variable("nomad/jobs/shop_db_backup") != nil {
		t.Error("the backup job, its runs or its variable is still there")
	}
}

func TestBackupRuns(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := backedUp()
	if err := d.SyncBackups(ctx, a, map[string]string{"db": "postgres@sha256:" + whoamiDigest[7:]},
		map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}, testDest, nil); err != nil {
		t.Fatal(err)
	}
	ok, err := d.ForceBackup(ctx, "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := d.ForceBackup(ctx, "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	running, err := d.ForceBackup(ctx, "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	nomad.FinishRun(ok, true, "", "caravana-backup key=backups/caravana/shop/db/1.dump bytes=1499\ncaravana-expired 0.dump\n", "")
	nomad.FinishRun(bad, false, "dump", "", "pg_dump: error: connection failed: FATAL:  password authentication failed for user \"postgres\"\n")

	runs, err := d.BackupRuns(ctx, "shop", "db")
	if err != nil || len(runs) != 3 {
		t.Fatalf("BackupRuns = %+v, %v", runs, err)
	}
	byID := map[string]BackupRun{}
	for _, r := range runs {
		byID[r.JobID] = r
	}
	if r := byID[ok]; r.Status != RunSucceeded || r.Object != "backups/caravana/shop/db/1.dump" || r.Bytes != 1499 || len(r.Expired) != 1 || r.Expired[0] != "0.dump" {
		t.Errorf("succeeded run = %+v", r)
	}
	if r := byID[bad]; r.Status != RunFailed || r.Message != `dump: pg_dump: error: connection failed: FATAL:  password authentication failed for user "postgres"` {
		t.Errorf("failed run = %+v", r)
	}
	if r := byID[running]; r.Status != RunRunning || r.Started.IsZero() {
		t.Errorf("running run = %+v", r)
	}
}

func TestRestoreJob(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := backedUp()
	if err := d.SyncBackups(ctx, a, map[string]string{"db": "postgres@sha256:" + whoamiDigest[7:]},
		map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}, testDest, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Restore(ctx, "shop", a.Services[0], "postgres@sha256:"+whoamiDigest[7:], "backups/caravana/shop/db/1.dump", nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if v := nomad.Variable("nomad/jobs/shop_db_restore"); v["PGPASSWORD"] != "pw" {
		t.Errorf("the restore job has no credentials of its own: %v", v)
	}
	if status, _, err := d.RestoreStatus(ctx, "shop", "db"); err != nil || status != RunRunning {
		t.Errorf("RestoreStatus = %s, %v; want running", status, err)
	}
	nomad.FinishRun("shop_db_restore", false, "restore", "", "pg_restore: error: could not open input file\n")
	if status, msg, err := d.RestoreStatus(ctx, "shop", "db"); err != nil || status != RunFailed || msg != "restore: pg_restore: error: could not open input file" {
		t.Errorf("RestoreStatus = %s %q, %v", status, msg, err)
	}

	// Deleting the app's jobs takes the backup job, its runs, the restore job
	// and their variables, and nothing of another app.
	nomad.AddForeignJob(&api.Job{ID: new("shop2_db_backup"), Name: new("x")})
	if _, err := d.ForceBackup(ctx, "shop", "db"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteAppJobs(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"shop_db_backup", "shop_db_restore"} {
		if nomad.Job(id) != nil || nomad.Variable("nomad/jobs/"+id) != nil {
			t.Errorf("%s or its variable is still there", id)
		}
	}
	if len(nomad.Runs("shop_db_backup")) != 0 || nomad.Job("shop2_db_backup") == nil {
		t.Error("runs remain, or another app's job went")
	}
}
