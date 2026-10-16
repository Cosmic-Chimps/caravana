package nomadjob

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
