package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// sqliteApp is whoami as a worker that keeps a SQLite database on its volume,
// backed up with the sqlite engine (feature 029).
func sqliteApp() app.App {
	a := withData()
	svc := &a.Services[0]
	svc.Ports, svc.Domains, svc.Health = nil, nil, nil
	svc.Backup = &app.Backup{Engine: app.BackupSQLite, File: "/data/app.db", Keep: 3}
	return a
}

// newSQLiteLab is the volume lab (volumebackup_test.go) with sqliteApp
// deployed: one running copy, "c1", whose sidecars the test plays through
// onSQLite, and whose caravana-volumes task it plays for the swap.
func newSQLiteLab(t *testing.T) *volumeLab {
	t.Helper()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, sqliteApp())
	e.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
	e.nomad.SetRestartStop(20 * time.Millisecond)
	clock := e.svc.clock.(*fakeClock)
	return &volumeLab{env: e, t: t, t0: clock.Now(), clock: clock}
}

// sqliteReplies are what the sidecars' four commands do.
type sqliteReplies struct {
	copy, upload, download, stage deploytest.ExecReply
}

// onSQLite plays the sidecars: each command gets its script's reply.
func (l *volumeLab) onSQLite(r sqliteReplies) {
	l.nomad.OnExec(func(req deploytest.ExecRequest) deploytest.ExecReply {
		script := ""
		if len(req.Command) > 2 {
			script = req.Command[2]
		}
		switch script {
		case nomadjob.SQLiteCopyScript:
			return r.copy
		case nomadjob.SQLiteUploadScript:
			return r.upload
		case nomadjob.SQLiteDownloadScript:
			return r.download
		case nomadjob.SQLiteStageScript:
			return r.stage
		}
		return deploytest.ExecReply{ExitCode: 99, Stderr: "unexpected command"}
	})
}

// sqliteOK is a run where every command works.
func (l *volumeLab) sqliteOK(id string) sqliteReplies {
	ts := strings.TrimPrefix(id, "web-")
	return sqliteReplies{
		copy:     deploytest.ExecReply{Stdout: "caravana-sqlite-copy bytes=4096\n"},
		upload:   deploytest.ExecReply{Stdout: "caravana-backup key=backups/caravana/whoami/web/" + ts + ".sqlite.gz bytes=1000\ncaravana-expired 1700000000.sqlite.gz\n"},
		download: deploytest.ExecReply{Stdout: "caravana-sqlite-download bytes=4096\n"},
	}
}

// scripts are the scripts the fake was asked to run, in order.
func (l *volumeLab) scripts() []string {
	var out []string
	for _, e := range l.nomad.Execs() {
		switch e.Command[2] {
		case nomadjob.SQLiteCopyScript:
			out = append(out, "copy@"+e.Task)
		case nomadjob.SQLiteUploadScript:
			out = append(out, "upload@"+e.Task)
		case nomadjob.SQLiteDownloadScript:
			out = append(out, "download@"+e.Task)
		case nomadjob.SQLiteStageScript:
			out = append(out, "stage@"+e.Task)
		}
	}
	return out
}

func TestSQLiteBackupRun(t *testing.T) {
	ctx := context.Background()

	t.Run("success records the object and size, expires the oldest, and never restarts the service", func(t *testing.T) {
		l := newSQLiteLab(t)
		id := l.id(0)
		l.onSQLite(l.sqliteOK(id))
		old := store.BackupRecord{AppID: "whoami", ID: "web-1700000000", Service: "web", Engine: app.BackupSQLite,
			Status: store.BackupSucceeded, Object: "backups/caravana/whoami/web/1700000000.sqlite.gz", StartedAt: l.t0.Add(-100 * time.Hour)}
		if err := l.store.SaveBackup(ctx, old); err != nil {
			t.Fatal(err)
		}

		rec, err := l.svc.BackUpNow(ctx, "whoami", "web")
		if err != nil {
			t.Fatal(err)
		}
		if rec.ID != id || rec.Engine != app.BackupSQLite || rec.Status != store.BackupRunning ||
			rec.JobID != "whoami_web_sqlite/"+strings.TrimPrefix(id, "web-") || rec.Trigger != store.BackupTriggerOwner {
			t.Fatalf("running record = %+v", rec)
		}
		got := l.settled(id)
		if got.Status != store.BackupSucceeded || got.Bytes != 1000 || got.Message != "" || got.Stopped != nil ||
			got.Object != "backups/caravana/whoami/web/"+strings.TrimPrefix(id, "web-")+".sqlite.gz" {
			t.Errorf("record = %+v", got)
		}
		if b, _ := l.store.GetBackup(ctx, "whoami", old.ID); !b.Expired {
			t.Error("the backup retention removed isn't marked expired")
		}
		l.released()
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %v, want none: the service keeps running", rs)
		}
		if got, want := strings.Join(l.scripts(), " "), "copy@caravana-sqlite upload@caravana-upload"; got != want {
			t.Errorf("commands = %s, want %s", got, want)
		}

		// The secret key goes on the upload's standard input and nowhere else.
		for _, e := range l.nomad.Execs() {
			if strings.Contains(strings.Join(e.Command, " "), marker) {
				t.Errorf("a command's arguments hold the secret key: %s", e.Task)
			}
			if e.Command[2] == nomadjob.SQLiteUploadScript && !strings.Contains(e.Stdin, "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY=SK-"+marker) {
				t.Errorf("the upload's standard input lacks the secret key: %q", e.Stdin)
			}
			if e.Command[2] == nomadjob.SQLiteCopyScript && e.Stdin != "" {
				t.Errorf("the copy got a standard input: %q", e.Stdin)
			}
		}
		if bs, _ := l.store.ListBackups(ctx, "whoami", 0); strings.Contains(fmt.Sprintf("%+v", bs), marker) {
			t.Error("a backup record holds the secret key")
		}
	})

	t.Run("a retention warning is the message of a run that succeeded", func(t *testing.T) {
		l := newSQLiteLab(t)
		id := l.id(0)
		r := l.sqliteOK(id)
		r.upload.Stdout = "caravana-backup key=b/whoami/web/1.sqlite.gz bytes=10\ncaravana-warning the backup is kept, but old backups were not removed: listing: denied\n"
		l.onSQLite(r)
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		got := l.settled(id)
		if got.Status != store.BackupSucceeded || !strings.Contains(got.Message, "old backups were not removed") {
			t.Errorf("record = %+v", got)
		}
		l.released()
	})

	failures := []struct {
		name     string
		replies  func(r *sqliteReplies)
		wantMsg  string
		uploaded bool
	}{
		{"the database file is missing", func(r *sqliteReplies) {
			r.copy = deploytest.ExecReply{ExitCode: 1, Stderr: "copy: there is no database file at /data/app.db\n"}
		}, "copy: there is no database file at /data/app.db", false},
		{"the copy fails its check", func(r *sqliteReplies) {
			r.copy = deploytest.ExecReply{ExitCode: 1, Stderr: "check: the copy is damaged: row 7 missing\n"}
		}, "check: the copy is damaged: row 7 missing", false},
		{"the upload fails", func(r *sqliteReplies) {
			r.upload = deploytest.ExecReply{ExitCode: 1, Stderr: "upload: 403 Forbidden\n"}
		}, "upload: 403 Forbidden", true},
		{"Nomad can't start the command", func(r *sqliteReplies) {
			r.copy = deploytest.ExecReply{Fail: "task not running"}
		}, "", false},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			l := newSQLiteLab(t)
			id := l.id(0)
			r := l.sqliteOK(id)
			tt.replies(&r)
			l.onSQLite(r)
			if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
				t.Fatal(err)
			}
			got := l.settled(id)
			if got.Status != store.BackupFailed || (tt.wantMsg != "" && got.Message != tt.wantMsg) || got.Message == "" {
				t.Errorf("record = %+v, want failed with %q", got, tt.wantMsg)
			}
			if uploaded := len(l.scripts()) == 2; uploaded != tt.uploaded {
				t.Errorf("commands = %v", l.scripts())
			}
			l.released()
		})
	}

	t.Run("a second run while one goes is refused, and the run's limit ends a stuck command", func(t *testing.T) {
		l := newSQLiteLab(t)
		l.svc.volume.limit, l.svc.volume.grace = 300*time.Millisecond, 0
		id := l.id(0)
		r := l.sqliteOK(id)
		r.copy = deploytest.ExecReply{Hold: true}
		l.onSQLite(r)
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrBackupRunning) {
			t.Errorf("a second backup = %v, want ErrBackupRunning", err)
		}
		got := l.settled(id)
		if got.Status != store.BackupFailed || !strings.Contains(got.Message, "timed out") {
			t.Errorf("record = %+v, want failed: timed out", got)
		}
		l.released()
	})

	t.Run("no running copy is refused before anything is recorded", func(t *testing.T) {
		l := newSQLiteLab(t)
		l.nomad.SetCopies("whoami")
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrNoRunningCopy) {
			t.Errorf("BackUpNow = %v, want ErrNoRunningCopy", err)
		}
		if bs, _ := l.store.ListBackups(ctx, "whoami", 0); len(bs) != 0 {
			t.Errorf("records = %+v, want none", bs)
		}
		l.released()
	})
}

// saveSQLiteBackup stores a succeeded sqlite backup of "web" and returns it.
func (l *volumeLab) saveSQLiteBackup() store.BackupRecord {
	l.t.Helper()
	done := l.t0.Add(-time.Hour)
	rec := store.BackupRecord{AppID: "whoami", ID: "web-1791990000", Service: "web", Engine: app.BackupSQLite,
		Status: store.BackupSucceeded, Object: "backups/caravana/whoami/web/1791990000.sqlite.gz",
		StartedAt: done.Add(-time.Minute), FinishedAt: &done}
	if err := l.store.SaveBackup(context.Background(), rec); err != nil {
		l.t.Fatal(err)
	}
	return rec
}

func TestSQLiteRestoreRun(t *testing.T) {
	ctx := context.Background()

	t.Run("success downloads and stages first, then swaps through caravana-volumes", func(t *testing.T) {
		l := newSQLiteLab(t)
		b := l.saveSQLiteBackup()
		id := l.id(0)
		l.onSQLite(l.sqliteOK(id))
		l.render(id)
		var asked map[string]string
		l.nomad.OnRestart(func(copyID string) {
			asked = l.nomad.Variable(labVar)
			l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\nmessage=\n")
		})

		rec, err := l.svc.Restore(ctx, "whoami", b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Engine != app.BackupSQLite || rec.Status != store.BackupRunning || rec.ID != restoreRecordID("whoami", id) {
			t.Fatalf("running record = %+v", rec)
		}
		got := l.settledRestore(rec.ID)
		if got.Status != store.BackupSucceeded || got.Stopped == nil {
			t.Errorf("record = %+v, want succeeded with a stop time", got)
		}
		if asked["REQUEST"] != id || asked["ACTION"] != "restore-sqlite" || asked["FILE"] != "data/app.db" {
			t.Errorf("the request = %v", asked)
		}
		if _, has := asked["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"]; has {
			t.Error("the caravana-volumes variable of a sqlite service holds the destination")
		}
		if got, want := strings.Join(l.scripts(), " "), "download@caravana-upload stage@caravana-sqlite"; got != want {
			t.Errorf("commands = %s, want %s", got, want)
		}
		for _, e := range l.nomad.Execs() {
			if e.Command[2] == nomadjob.SQLiteDownloadScript && (e.Command[4] != b.Object || !strings.Contains(e.Stdin, marker)) {
				t.Errorf("the download = %v (stdin has the key: %v)", e.Command[4:], strings.Contains(e.Stdin, marker))
			}
		}
		waitFor(t, "the request to be cleared", func() bool { return l.nomad.Variable(labVar)["REQUEST"] == "" })
		l.released()
	})

	for _, tt := range []struct {
		name    string
		replies func(r *sqliteReplies)
		wantMsg string
	}{
		{"a download that fails", func(r *sqliteReplies) {
			r.download = deploytest.ExecReply{ExitCode: 1, Stderr: "download: the backup is not a SQLite database\n"}
		}, "download: the backup is not a SQLite database"},
		{"a backup that fails its check", func(r *sqliteReplies) {
			r.stage = deploytest.ExecReply{ExitCode: 1, Stderr: "check: the backup is damaged: page 3\n"}
		}, "check: the backup is damaged: page 3"},
	} {
		t.Run(tt.name+" ends the restore with the service never stopped", func(t *testing.T) {
			l := newSQLiteLab(t)
			b := l.saveSQLiteBackup()
			r := l.sqliteOK(l.id(0))
			tt.replies(&r)
			l.onSQLite(r)
			rec, err := l.svc.Restore(ctx, "whoami", b.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := l.settledRestore(rec.ID)
			if got.Status != store.BackupFailed || got.Message != tt.wantMsg || got.Stopped != nil {
				t.Errorf("record = %+v, want failed with %q and no stop time", got, tt.wantMsg)
			}
			if rs := l.nomad.Restarts(); len(rs) != 0 {
				t.Errorf("restarts = %v, want none", rs)
			}
			if req := l.request(); req != "" {
				t.Errorf("request = %q, want none written", req)
			}
			l.released()
		})
	}

	t.Run("a backup of another engine is refused", func(t *testing.T) {
		l := newSQLiteLab(t)
		b := l.saveSQLiteBackup()
		b.ID, b.Engine = "web-1791990001", app.BackupVolume
		if err := l.store.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.Restore(ctx, "whoami", b.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Restore = %v, want ErrNotFound", err)
		}
	})
}

// The scheduler runs sqlite backups on their slot, as it does volume ones.
func TestRunDueSQLiteBackups(t *testing.T) {
	ctx := context.Background()
	day := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.UTC) }
	l := newSQLiteLab(t)
	last := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupSQLite,
		Status: store.BackupSucceeded, StartedAt: day(4, 3, 0).Add(5 * time.Second)}
	if err := l.store.SaveBackup(ctx, last); err != nil {
		t.Fatal(err)
	}
	l.clock.Set(day(5, 3, 20))
	id := fmt.Sprintf("web-%d", day(5, 3, 20).Unix())
	l.onSQLite(l.sqliteOK(id))

	l.svc.runDueVolumeBackups(ctx, map[string]string{})
	got := l.settled(id)
	if got.Status != store.BackupSucceeded || got.Engine != app.BackupSQLite || got.Trigger != store.BackupTriggerTimer {
		t.Fatalf("the scheduled backup = %+v", got)
	}
	l.released()
}

// At start, a sqlite backup left running is failed at once, and a sqlite
// restore is settled from its result file only when it got as far as asking
// the copy for the swap.
func TestResumeSQLiteRuns(t *testing.T) {
	ctx := context.Background()

	t.Run("a backup left running", func(t *testing.T) {
		l := newSQLiteLab(t)
		rec := store.BackupRecord{AppID: "whoami", ID: l.id(0), Service: "web", JobID: "whoami_web_sqlite/1", Engine: app.BackupSQLite,
			Status: store.BackupRunning, StartedAt: time.Now().UTC()}
		if err := l.store.SaveBackup(ctx, rec); err != nil {
			t.Fatal(err)
		}
		startService(t, l.store, l.registry, l.nomad, l.secrets)
		got := l.settled(rec.ID)
		if got.Status != store.BackupFailed || got.Message != sqliteRestartedMessage {
			t.Errorf("record = %+v", got)
		}
	})

	for _, tt := range []struct {
		name       string
		requested  bool
		wantStatus string
		wantMsg    string
	}{
		{"a restore that asked for the swap", true, store.BackupSucceeded, ""},
		{"a restore that never got to the swap", false, store.BackupFailed, restartedMessage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := newSQLiteLab(t)
			id := l.id(0)
			rec := store.RestoreRecord{ID: restoreRecordID("whoami", id), AppID: "whoami", Service: "web", BackupID: "web-1",
				Engine: app.BackupSQLite, Status: store.BackupRunning, CreatedAt: time.Now().UTC()}
			if err := l.store.SaveRestore(ctx, rec); err != nil {
				t.Fatal(err)
			}
			if tt.requested {
				items := l.nomad.Variable(labVar)
				items["REQUEST"], items["ACTION"], items["FILE"] = id, "restore-sqlite", "data/app.db"
				l.nomad.SetVariable(labVar, items)
				l.nomad.WriteAllocFile(labCopy, "alloc/data/caravana/"+id+".result", "status=succeeded\n")
			}
			start := time.Now()
			startService(t, l.store, l.registry, l.nomad, l.secrets)
			got := l.settledRestore(rec.ID)
			if got.Status != tt.wantStatus || got.Message != tt.wantMsg {
				t.Errorf("record = %+v, want %s %q", got, tt.wantStatus, tt.wantMsg)
			}
			if !tt.requested && time.Since(start) > 5*time.Second {
				t.Errorf("settling took %s: a restore that never asked for the swap waited for a result", time.Since(start))
			}
			l.released()
		})
	}
}
