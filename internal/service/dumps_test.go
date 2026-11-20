package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// dumpDBOf is whoami acting as a MySQL or MongoDB service with a backup policy
// and the login its image reads, as env (the user) and secrets (the password).
func dumpDBOf(engine string) app.App {
	a := whoami()
	a.Services[0].Replicas = nil
	a.Services[0].Backup = &app.Backup{Engine: engine, Keep: 2}
	switch engine {
	case app.BackupMySQL:
		a.Services[0].Env = []app.EnvVar{{Name: "MARIADB_USER", Value: "app"}}
		a.Services[0].Secrets = []app.Secret{set("MARIADB_PASSWORD", marker)}
	case app.BackupMongoDB:
		a.Services[0].Env = []app.EnvVar{{Name: "MONGO_INITDB_ROOT_USERNAME", Value: "root"}}
		a.Services[0].Secrets = []app.Secret{set("MONGO_INITDB_ROOT_PASSWORD", marker)}
	}
	return a
}

// The dump engines of feature 046 run as Postgres' do: a job with the login in
// DB_USER and DB_PASSWORD of its own variable, runs and restores recorded with
// their engine, retention read back from the extension of the file it deleted.
func TestDumpEngineBackupsAndRestores(t *testing.T) {
	for _, tc := range []struct{ engine, ext, user string }{
		{app.BackupMySQL, ".sql.gz", "app"},
		{app.BackupMongoDB, ".archive.gz", "root"},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t)
			setDestination(t, e)
			deployHealthy(t, e, dumpDBOf(tc.engine))

			v := e.nomad.Variable("nomad/jobs/whoami_web_backup")
			if v["DB_USER"] != tc.user || v["DB_PASSWORD"] != marker || v["BACKUP_PATH"] != "backups/caravana/whoami/web" {
				t.Fatalf("backup variable = %v", v)
			}
			if _, has := v["PGPASSWORD"]; has {
				t.Error("the variable has Postgres' items")
			}
			job := e.nomad.Job("whoami_web_backup")
			if job == nil || strings.Contains(mustMarshal(t, job), marker) {
				t.Fatal("no backup job, or it holds the password")
			}
			if ext := job.TaskGroups[0].Tasks[1].Env["BACKUP_EXT"]; ext != tc.ext {
				t.Errorf("BACKUP_EXT = %q, want %q", ext, tc.ext)
			}

			first, err := e.svc.BackUpNow(ctx, "whoami", "web")
			if err != nil || first.Engine != tc.engine {
				t.Fatalf("BackUpNow = %+v, %v; want a record of engine %s", first, err, tc.engine)
			}
			object := "backups/caravana/whoami/web/1" + tc.ext
			e.nomad.FinishRun(first.JobID, true, "", "caravana-backup key="+object+" bytes=1499\n", "")
			waitFor(t, "the first backup to be recorded", func() bool {
				b, err := e.store.GetBackup(ctx, "whoami", first.ID)
				return err == nil && b.Status == store.BackupSucceeded && b.Engine == tc.engine && b.Object == object
			})

			// Retention names the file; the record is found by the digits before the first dot.
			second, err := e.svc.BackUpNow(ctx, "whoami", "web")
			if err != nil {
				t.Fatal(err)
			}
			firstFile := first.JobID[strings.LastIndex(first.JobID, "-")+1:] + tc.ext
			e.nomad.FinishRun(second.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/2"+tc.ext+" bytes=10\ncaravana-expired "+firstFile+"\n", "")
			waitFor(t, "retention to be recorded", func() bool {
				live, _, err := e.svc.ListBackups(ctx, "whoami")
				return err == nil && len(live) == 1 && live[0].ID == second.ID
			})

			// A restore of the newest one is a job of the engine, recorded with it.
			r, err := e.svc.Restore(ctx, "whoami", second.ID)
			if err != nil || r.Engine != tc.engine || r.Status != store.BackupRunning {
				t.Fatalf("Restore = %+v, %v", r, err)
			}
			if rj := e.nomad.Job("whoami_web_restore"); rj == nil || rj.Meta["backup_key"] != "backups/caravana/whoami/web/2"+tc.ext {
				t.Fatalf("restore job = %+v", rj)
			}
			if _, err := e.svc.Restore(ctx, "whoami", second.ID); !errors.Is(err, ErrBackupRunning) {
				t.Errorf("a second restore = %v, want ErrBackupRunning", err)
			}
			e.nomad.FinishRun("whoami_web_restore", false, "restore", "", "the image has no mongorestore\n")
			waitFor(t, "the restore to be recorded", func() bool {
				_, restores, err := e.svc.ListBackups(ctx, "whoami")
				return err == nil && len(restores) == 1 && restores[0].Status == store.BackupFailed &&
					restores[0].Message == "restore: the image has no mongorestore" && restores[0].Engine == tc.engine
			})
		})
	}
}

// Credentials are found by the names the engine's image reads, in the env or
// in the secrets, and the service that has none can't be deployed.
func TestDumpEngineCredentials(t *testing.T) {
	tests := []struct {
		name         string
		app          func() app.App
		wantUser     string
		wantPassword string
		wantDeployOK bool
	}{
		{"mysql root password as a secret", func() app.App {
			a := dumpDBOf(app.BackupMySQL)
			a.Services[0].Env = nil
			a.Services[0].Secrets = []app.Secret{set("MYSQL_ROOT_PASSWORD", marker)}
			return a
		}, "root", marker, true},
		{"mariadb root password wins over a user", func() app.App {
			a := dumpDBOf(app.BackupMySQL)
			a.Services[0].Secrets = append(a.Services[0].Secrets, set("MARIADB_ROOT_PASSWORD", "root-"+marker))
			return a
		}, "root", "root-" + marker, true},
		{"mysql user and password", func() app.App { return dumpDBOf(app.BackupMySQL) }, "app", marker, true},
		{"mongodb root", func() app.App { return dumpDBOf(app.BackupMongoDB) }, "root", marker, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			setDestination(t, e)
			deployHealthy(t, e, tc.app())
			v := e.nomad.Variable("nomad/jobs/whoami_web_backup")
			if v["DB_USER"] != tc.wantUser || v["DB_PASSWORD"] != tc.wantPassword {
				t.Errorf("variable = %v, want user %s and password %s", v, tc.wantUser, tc.wantPassword)
			}
		})
	}

	t.Run("no login is a field error", func(t *testing.T) {
		e := newEnv(t)
		setDestination(t, e)
		a := dumpDBOf(app.BackupMySQL)
		a.Services[0].Env, a.Services[0].Secrets = nil, nil
		_, err := e.svc.Deploy(context.Background(), a, app.TriggerManual)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].backup" || !strings.Contains(fe.Message, "MARIADB_ROOT_PASSWORD") {
			t.Errorf("Deploy = %v, want a field error on services[0].backup naming the variables", err)
		}
	})
}

// A password given as plain env is a warning for every dump engine, naming the
// variable; a secret of the same name is not.
func TestPlainBackupPasswordsForEveryEngine(t *testing.T) {
	plain := func(engine string, env ...app.EnvVar) app.App {
		a := dumpDBOf(engine)
		a.Services[0].Env = env
		a.Services[0].Secrets = nil
		return a
	}
	tests := []struct {
		name string
		app  app.App
		want []string // variable names in the warnings
	}{
		{"postgres", plain(app.BackupPostgres, app.EnvVar{Name: "POSTGRES_PASSWORD", Value: "pw"}), []string{"POSTGRES_PASSWORD"}},
		{"mysql root", plain(app.BackupMySQL, app.EnvVar{Name: "MARIADB_ROOT_PASSWORD", Value: "pw"}), []string{"MARIADB_ROOT_PASSWORD"}},
		{"mysql user password", plain(app.BackupMySQL, app.EnvVar{Name: "MYSQL_USER", Value: "u"}, app.EnvVar{Name: "MYSQL_PASSWORD", Value: "pw"}), []string{"MYSQL_PASSWORD"}},
		{"mongodb", plain(app.BackupMongoDB, app.EnvVar{Name: "MONGO_INITDB_ROOT_USERNAME", Value: "root"}, app.EnvVar{Name: "MONGO_INITDB_ROOT_PASSWORD", Value: "pw"}), []string{"MONGO_INITDB_ROOT_PASSWORD"}},
		{"mongodb user is no password", plain(app.BackupMongoDB, app.EnvVar{Name: "MONGO_INITDB_ROOT_USERNAME", Value: "root"}), nil},
		{"a password name of another engine", plain(app.BackupMongoDB, app.EnvVar{Name: "POSTGRES_PASSWORD", Value: "pw"}), nil},
		{"a secret is fine", dumpDBOf(app.BackupMySQL), nil},
		{"no backup", func() app.App {
			a := plain(app.BackupMySQL, app.EnvVar{Name: "MYSQL_ROOT_PASSWORD", Value: "pw"})
			a.Services[0].Backup = nil
			return a
		}(), nil},
		{"a volume backup", func() app.App {
			a := plain(app.BackupMySQL, app.EnvVar{Name: "MYSQL_ROOT_PASSWORD", Value: "pw"})
			a.Services[0].Backup = &app.Backup{Engine: app.BackupVolume}
			return a
		}(), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PlainBackupPasswords(tc.app)
			if len(got) != len(tc.want) {
				t.Fatalf("warnings = %+v, want one per %v", got, tc.want)
			}
			for i, w := range got {
				if !strings.Contains(w.Message, tc.want[i]) || w.Field != "services[0].env[0]" && w.Field != "services[0].env[1]" {
					t.Errorf("warning = %+v, want it to name %s", w, tc.want[i])
				}
			}
		})
	}
}

// A backup made by one dump engine isn't restored into a service that now backs
// up with another, and a backup file's record is found whatever its extension.
func TestRestoreRefusesAnotherDumpEngine(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, dumpDBOf(app.BackupMySQL))
	done := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupPostgres, Status: store.BackupSucceeded,
		Object: "backups/caravana/whoami/web/1.dump", StartedAt: time.Now().UTC()}
	if err := e.store.SaveBackup(ctx, done); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Restore(ctx, "whoami", "web-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Restore of a postgres dump into a mysql service = %v, want ErrNotFound", err)
	}
}

func TestExpiredRecordID(t *testing.T) {
	for file, want := range map[string]string{
		"1791386036.dump":       "db-1791386036",
		"1791386036.sql.gz":     "db-1791386036",
		"1791386036.archive.gz": "db-1791386036",
		"1791386036":            "db-1791386036",
	} {
		if got := expiredRecordID("db", file); got != want {
			t.Errorf("expiredRecordID(%q) = %q, want %q", file, got, want)
		}
	}
}
