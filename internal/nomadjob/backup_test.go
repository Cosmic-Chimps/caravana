package nomadjob

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/cronexpr"
	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// backedUpDB is a postgres service with a backup policy.
func backedUpDB() app.Service {
	return app.Service{
		Name:    "db",
		Image:   "postgres:17",
		Ports:   []app.Port{{Name: "pg", ContainerPort: 5432}},
		Env:     []app.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "s3cr3t-marker"}},
		Health:  &app.HealthCheck{},
		Volumes: []app.Volume{{Name: "data", Path: "/var/lib/postgresql/data"}},
		Backup:  &app.Backup{Engine: app.BackupPostgres, Keep: 3},
	}
}

const pinnedPostgres = "postgres@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

// A Postgres backup with schedule "never" (feature 039) stays a periodic,
// enabled job, so PeriodicForce (Back up now) still works, with a cron line
// that has no next time, so Nomad never starts a run by itself. The cron line
// of every other policy is its own.
func TestBackupJobPeriodicSpec(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		want     string
	}{
		{"never", app.BackupScheduleNever, neverSpec},
		{"empty is the default", "", app.DefaultBackupSchedule},
		{"a cron line", "*/15 * * * *", "*/15 * * * *"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := backedUpDB()
			svc.Backup.Schedule = tc.schedule
			p := BackupJob("shop", svc, pinnedPostgres, labSettings(), nil).Periodic
			if p == nil || p.Enabled == nil || !*p.Enabled || len(p.Specs) != 1 || p.Specs[0] != tc.want ||
				*p.SpecType != "cron" || !*p.ProhibitOverlap || *p.TimeZone != "UTC" {
				t.Errorf("periodic = %+v, want an enabled cron %q in UTC", p, tc.want)
			}
		})
	}

	// The never spec really has no next time, in the library Nomad parses with.
	expr, err := cronexpr.Parse(neverSpec)
	if err != nil {
		t.Fatalf("neverSpec %q doesn't parse: %v", neverSpec, err)
	}
	if next := expr.Next(time.Now().UTC()); !next.IsZero() {
		t.Errorf("neverSpec has a next time, %v", next)
	}
}

// TestBackupJobs compares the backup and restore jobs with their golden
// files, and checks they hold no credential.
func TestBackupJobs(t *testing.T) {
	jobs := map[string]*api.Job{
		"backup":  BackupJob("shop", backedUpDB(), pinnedPostgres, labSettings(), nil),
		"restore": RestoreJob("shop", backedUpDB(), pinnedPostgres, "bucket/shop/db/1791386036.dump", labSettings(), nil),
	}
	for name, job := range jobs {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(job, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			if bytes.Contains(got, []byte("s3cr3t-marker")) {
				t.Fatal("the job holds the database password")
			}
			path := filepath.Join("testdata", "backup-"+name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs; run with -update and read the diff", path)
			}
		})
	}

	b := jobs["backup"]
	if *b.ID != "shop_db_backup" || *b.Type != "batch" || b.Periodic == nil || b.Periodic.Specs[0] != app.DefaultBackupSchedule {
		t.Errorf("backup job = %s %s %+v", *b.ID, *b.Type, b.Periodic)
	}
	tg := b.TaskGroups[0]
	if *tg.RestartPolicy.Attempts != 0 || *tg.ReschedulePolicy.Attempts != 0 {
		t.Error("a failed run would be retried")
	}
	if tg.Tasks[0].Lifecycle.Hook != "prestart" || tg.Tasks[0].Config["image"] != pinnedPostgres {
		t.Errorf("dump task = %+v", tg.Tasks[0])
	}
	if !strings.Contains(*tg.Tasks[0].Templates[0].EmbeddedTmpl, `nomadVar "nomad/jobs/shop_db_backup"`) {
		t.Error("the dump doesn't read the job's own variable")
	}
	if tg.Tasks[1].Env["BACKUP_KEEP"] != "3" || strings.Contains(tg.Tasks[1].Config["args"].([]string)[0], "${") {
		t.Errorf("upload task = %+v", tg.Tasks[1])
	}
	r := jobs["restore"]
	if *r.ID != "shop_db_restore" || r.Periodic != nil || r.Meta["backup_key"] != "bucket/shop/db/1791386036.dump" {
		t.Errorf("restore job = %s %+v %v", *r.ID, r.Periodic, r.Meta)
	}
}

// dumpedDB is a mysql or mongodb service with a backup policy (feature 046).
func dumpedDB(engine string) app.Service {
	svc := app.Service{
		Name:    "db",
		Ports:   []app.Port{{Name: "db", ContainerPort: 3306}},
		Health:  &app.HealthCheck{},
		Volumes: []app.Volume{{Name: "data", Path: "/var/lib/data"}},
		Backup:  &app.Backup{Engine: engine, Keep: 3},
	}
	switch engine {
	case app.BackupMySQL:
		svc.Image = "mariadb:11.4"
		svc.Env = []app.EnvVar{{Name: "MARIADB_ROOT_PASSWORD", Value: "s3cr3t-marker"}}
	case app.BackupMongoDB:
		svc.Image = "mongo:8.0"
		svc.Ports[0].ContainerPort = 27017
		svc.Env = []app.EnvVar{{Name: "MONGO_INITDB_ROOT_USERNAME", Value: "root"}, {Name: "MONGO_INITDB_ROOT_PASSWORD", Value: "s3cr3t-marker"}}
	}
	return svc
}

// TestDumpEngineJobs compares the MySQL and MongoDB backup and restore jobs
// with their golden files and checks what every dump engine's jobs must have:
// no credential, the engine's object extension, one variable template and
// scripts without ${...}, which Nomad would interpolate.
func TestDumpEngineJobs(t *testing.T) {
	const digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	tests := []struct {
		engine, image, object, ext, gzip, hostVar string
	}{
		{app.BackupMySQL, "mariadb@" + digest, "bucket/shop/db/1791386036.sql.gz", ".sql.gz", "1", "DB_HOST"},
		{app.BackupMongoDB, "mongo@" + digest, "bucket/shop/db/1791386036.archive.gz", ".archive.gz", "0", "DB_HOST"},
	}
	for _, tc := range tests {
		t.Run(tc.engine, func(t *testing.T) {
			jobs := map[string]*api.Job{
				"backup":  BackupJob("shop", dumpedDB(tc.engine), tc.image, labSettings(), nil),
				"restore": RestoreJob("shop", dumpedDB(tc.engine), tc.image, tc.object, labSettings(), nil),
			}
			for name, job := range jobs {
				got, err := json.MarshalIndent(job, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, '\n')
				if bytes.Contains(got, []byte("s3cr3t-marker")) {
					t.Fatalf("%s: the job holds the database password", name)
				}
				path := filepath.Join("testdata", "dump-"+tc.engine+"-"+name+".golden.json")
				if *update {
					if err := os.WriteFile(path, got, 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden (run with -update to create it): %v", err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s differs; run with -update and read the diff", path)
				}
			}

			b := jobs["backup"]
			tg := b.TaskGroups[0]
			if *b.ID != "shop_db_backup" || b.Periodic == nil || tg.Tasks[0].Config["image"] != tc.image {
				t.Errorf("backup job = %s %+v, dump image %v", *b.ID, b.Periodic, tg.Tasks[0].Config["image"])
			}
			up := tg.Tasks[1]
			if up.Env["BACKUP_EXT"] != tc.ext || up.Env["BACKUP_GZIP"] != tc.gzip || up.Env["BACKUP_KEEP"] != "3" {
				t.Errorf("upload env = %v, want ext %s and gzip %s", up.Env, tc.ext, tc.gzip)
			}
			// The dump and restore tasks render the host and port the engine's
			// scripts read, and neither names a password or a user.
			for _, job := range jobs {
				for _, task := range job.TaskGroups[0].Tasks {
					for _, a := range task.Config["args"].([]string) {
						if tc.engine == app.BackupMongoDB && strings.HasPrefix(a, "copyto") {
							continue
						}
						if strings.Contains(a, "${") && !strings.Contains(a, "NOMAD_META_backup_key") {
							t.Errorf("task %s has a ${...} Nomad would interpolate: %q", task.Name, a)
						}
					}
				}
			}
			dumpTmpl := *tg.Tasks[0].Templates[1].EmbeddedTmpl
			if !strings.Contains(dumpTmpl, tc.hostVar+"={{ .Address }}") || !strings.Contains(dumpTmpl, "DB_PORT={{ .Port }}") || strings.Contains(dumpTmpl, "PG") {
				t.Errorf("database template = %q", dumpTmpl)
			}
			r := jobs["restore"]
			if *r.ID != "shop_db_restore" || r.Periodic != nil || r.Meta["backup_key"] != tc.object {
				t.Errorf("restore job = %s %+v %v", *r.ID, r.Periodic, r.Meta)
			}
		})
	}

	// The Postgres job keeps its own variable names and object.
	pg := BackupJob("shop", backedUpDB(), pinnedPostgres, labSettings(), nil).TaskGroups[0]
	if tmpl := *pg.Tasks[0].Templates[1].EmbeddedTmpl; !strings.Contains(tmpl, "PGHOST={{ .Address }}") || !strings.Contains(tmpl, "PGPORT={{ .Port }}") {
		t.Errorf("postgres template = %q", tmpl)
	}
	if pg.Tasks[1].Env["BACKUP_EXT"] != ".dump" || pg.Tasks[1].Env["BACKUP_GZIP"] != "0" {
		t.Errorf("postgres upload env = %v", pg.Tasks[1].Env)
	}
}
