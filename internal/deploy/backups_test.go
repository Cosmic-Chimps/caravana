package deploy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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

	if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil, nil); err != nil {
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
	if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil, nil); err != nil {
		t.Fatal(err)
	}
	if nomad.Job("shop_db_backup") != nil || len(nomad.Runs("shop_db_backup")) != 0 || nomad.Variable("nomad/jobs/shop_db_backup") != nil {
		t.Error("the backup job, its runs or its variable is still there")
	}
}

// The pin list reaches the Postgres backup job's variable with a deploy, and a
// pin or unpin rewrites only that item.
func TestBackupPins(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := backedUp()
	digests := map[string]string{"db": "postgres@sha256:" + whoamiDigest[7:]}
	creds := map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}
	const path = "nomad/jobs/shop_db_backup"

	// Without a variable there is no job: a pin has nothing to write, and that
	// is not an error.
	if err := d.SetBackupPins(ctx, "shop", "db", []string{"1700"}); err != nil {
		t.Fatalf("SetBackupPins before the first deploy = %v, want nil", err)
	}
	if nomad.Variable(path) != nil {
		t.Fatal("SetBackupPins made a variable for a job that isn't there")
	}

	if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, has := nomad.Variable(path)["BACKUP_PINNED"]; has {
		t.Errorf("a service with no pins has BACKUP_PINNED: %v", nomad.Variable(path))
	}

	// A deploy writes the pins of the service.
	if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil, map[string][]string{"db": {"1700", "1750"}, "other": {"1"}}); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable(path); v["BACKUP_PINNED"] != "1700 1750" || v["PGPASSWORD"] != "pw" {
		t.Errorf("variable = %v, want BACKUP_PINNED 1700 1750 and the credentials", v)
	}

	// A pin change rewrites that item and keeps the rest.
	if err := d.SetBackupPins(ctx, "shop", "db", []string{"1800"}); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable(path); v["BACKUP_PINNED"] != "1800" || v["PGPASSWORD"] != "pw" || v["BACKUP_PATH"] != "backups/caravana/shop/db" {
		t.Errorf("variable after a pin = %v", v)
	}
	// The destination moving keeps the pins.
	moved := testDest
	moved.Bucket = "other"
	if err := d.SetBackupDestination(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable(path); v["BACKUP_PINNED"] != "1800" || v["BACKUP_PATH"] != "other/caravana/shop/db" {
		t.Errorf("variable after a new destination = %v, want the pin kept", v)
	}
	// No pins removes the item.
	if err := d.SetBackupPins(ctx, "shop", "db", nil); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable(path); v["PGPASSWORD"] != "pw" {
		t.Errorf("variable = %v, want the credentials kept", v)
	} else if _, has := v["BACKUP_PINNED"]; has {
		t.Errorf("variable = %v, want BACKUP_PINNED removed", v)
	}
}

func TestBackupRuns(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := backedUp()
	if err := d.SyncBackups(ctx, a, map[string]string{"db": "postgres@sha256:" + whoamiDigest[7:]},
		map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}, testDest, nil, nil); err != nil {
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
		map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}, testDest, nil, nil); err != nil {
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

// dumpedApp is an app with one MySQL or MongoDB service that has a backup policy.
func dumpedApp(engine string) app.App {
	a := backedUp()
	a.Services[0].Backup = &app.Backup{Engine: engine, Keep: 2}
	a.Services[0].Env = nil
	switch engine {
	case app.BackupMySQL:
		a.Services[0].Image = "mariadb:11.4"
		a.Services[0].Ports = []app.Port{{Name: "db", ContainerPort: 3306}}
	case app.BackupMongoDB:
		a.Services[0].Image = "mongo:8.0"
		a.Services[0].Ports = []app.Port{{Name: "db", ContainerPort: 27017}}
	}
	return a
}

// The MySQL and MongoDB engines (feature 046) are backup jobs like Postgres's,
// with the login in DB_USER and DB_PASSWORD of the job's own variable and in no
// job; a pin and a restore reach them the same way.
func TestDumpEngineBackupsAndRestore(t *testing.T) {
	for _, tc := range []struct{ engine, image, object string }{
		{app.BackupMySQL, "mariadb@sha256:" + whoamiDigest[7:], "backups/caravana/shop/db/1.sql.gz"},
		{app.BackupMongoDB, "mongo@sha256:" + whoamiDigest[7:], "backups/caravana/shop/db/1.archive.gz"},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			ctx := context.Background()
			d, _, nomad := newTestDeployer(t)
			a := dumpedApp(tc.engine)
			digests := map[string]string{"db": tc.image}
			creds := map[string]BackupCredentials{"db": {User: "root", Password: "pw-marker"}}
			const path = "nomad/jobs/shop_db_backup"

			if got := BackupJobIDs(a); len(got) != 1 || got[0] != "shop_db_backup" {
				t.Errorf("BackupJobIDs = %v, want the dump job", got)
			}
			if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil, map[string][]string{"db": {"1700"}}); err != nil {
				t.Fatalf("SyncBackups: %v", err)
			}
			job := nomad.Job("shop_db_backup")
			if job == nil || job.Periodic == nil {
				t.Fatalf("backup job = %+v", job)
			}
			if raw := fmt.Sprintf("%+v", job); strings.Contains(raw, "pw-marker") {
				t.Error("the backup job holds the password")
			}
			v := nomad.Variable(path)
			if v["DB_USER"] != "root" || v["DB_PASSWORD"] != "pw-marker" || v["BACKUP_PATH"] != "backups/caravana/shop/db" ||
				v["BACKUP_PINNED"] != "1700" || v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK" {
				t.Errorf("variable = %v", v)
			}
			for _, pg := range []string{"PGUSER", "PGPASSWORD", "PGDATABASE"} {
				if _, has := v[pg]; has {
					t.Errorf("the variable has Postgres' %s", pg)
				}
			}

			// A pin change rewrites its item and keeps the login.
			if err := d.SetBackupPins(ctx, "shop", "db", []string{"1800"}); err != nil {
				t.Fatal(err)
			}
			if v := nomad.Variable(path); v["BACKUP_PINNED"] != "1800" || v["DB_PASSWORD"] != "pw-marker" {
				t.Errorf("variable after a pin = %v", v)
			}

			// A restore gets its own copy of the variable, and a job of its engine.
			if err := d.Restore(ctx, "shop", a.Services[0], tc.image, tc.object, nil); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if v := nomad.Variable("nomad/jobs/shop_db_restore"); v["DB_PASSWORD"] != "pw-marker" || v["DB_USER"] != "root" {
				t.Errorf("the restore job has no login of its own: %v", v)
			}
			restore := nomad.Job("shop_db_restore")
			if restore == nil || restore.Meta["backup_key"] != tc.object {
				t.Fatalf("restore job = %+v", restore)
			}
			nomad.FinishRun("shop_db_restore", false, "restore", "", "the image has no tool\n")
			if status, msg, err := d.RestoreStatus(ctx, "shop", "db"); err != nil || status != RunFailed || msg != "restore: the image has no tool" {
				t.Errorf("RestoreStatus = %s %q, %v", status, msg, err)
			}

			// A run's outcome is read from the upload task's output, whatever the extension.
			run, err := d.ForceBackup(ctx, "shop", "db")
			if err != nil {
				t.Fatal(err)
			}
			nomad.FinishRun(run, true, "", "caravana-backup key="+tc.object+" bytes=77\ncaravana-expired 0"+filepath.Ext(tc.object)+"\n", "")
			runs, err := d.BackupRuns(ctx, "shop", "db")
			if err != nil || len(runs) != 1 || runs[0].Object != tc.object || runs[0].Bytes != 77 || len(runs[0].Expired) != 1 {
				t.Errorf("BackupRuns = %+v, %v", runs, err)
			}

			// A service that changes to the volume engine loses its job and variable.
			a.Services[0].Backup = &app.Backup{Engine: app.BackupVolume}
			if got := BackupJobIDs(a); len(got) != 0 {
				t.Errorf("BackupJobIDs for a volume backup = %v", got)
			}
			if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil, nil); err != nil {
				t.Fatal(err)
			}
			if nomad.Job("shop_db_backup") != nil || nomad.Variable(path) != nil {
				t.Error("the dump job or its variable is still there after the engine changed")
			}
		})
	}
}
