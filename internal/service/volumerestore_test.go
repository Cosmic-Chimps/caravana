package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A volume restore is the same run as a volume backup (see volumebackup_test.go
// for the lab), asked for with the object of an earlier backup.

const labObject = "backups/caravana/whoami/web/1791990000/"

// saveBackupToRestore stores a succeeded volume backup of "web" and returns its ID.
func (l *volumeLab) saveBackupToRestore() string {
	l.t.Helper()
	done := l.t0.Add(-time.Hour)
	rec := store.BackupRecord{AppID: "whoami", ID: "web-1791990000", Service: "web", Engine: app.BackupVolume,
		Status: store.BackupSucceeded, Object: labObject, StartedAt: done.Add(-time.Minute), FinishedAt: &done}
	if err := l.store.SaveBackup(context.Background(), rec); err != nil {
		l.t.Fatal(err)
	}
	return rec.ID
}

// restoreID is the ID of the restore of the n-th run of the clock's day.
func (l *volumeLab) restoreID(n int) string { return restoreRecordID("whoami", l.id(n)) }

// settledRestore waits until the restore is no longer running and returns it.
func (l *volumeLab) settledRestore(id string) store.RestoreRecord {
	l.t.Helper()
	var rec store.RestoreRecord
	waitFor(l.t, "restore "+id+" to settle", func() bool {
		restores, err := l.store.ListRestores(context.Background(), "whoami", 0)
		if err != nil {
			return false
		}
		for _, r := range restores {
			if r.ID == id && r.Status != store.BackupRunning {
				rec = r
				return true
			}
		}
		return false
	})
	return rec
}

// blockRestart makes the copy's restart wait until release is called, and then
// leave result for the request. inRestart is closed when the restart begins.
func (l *volumeLab) blockRestart(requestID, result string) (inRestart <-chan struct{}, release func()) {
	in, out := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(out) }) }
	l.t.Cleanup(release)
	l.nomad.OnRestart(func(copyID string) {
		close(in)
		<-out
		l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+requestID+".result", result)
	})
	return in, release
}

func TestVolumeRestoreRun(t *testing.T) {
	ctx := context.Background()

	t.Run("success records the restore, its stop time, and the request it sent", func(t *testing.T) {
		l := newVolumeLab(t)
		backupID := l.saveBackupToRestore()
		id := l.id(0)
		l.render(id)
		inRestart, release := l.blockRestart(id, "status=succeeded\nmessage=\n")

		rec, err := l.svc.Restore(ctx, "whoami", backupID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.ID != l.restoreID(0) || rec.Engine != app.BackupVolume || rec.Status != store.BackupRunning ||
			rec.BackupID != backupID || rec.Service != "web" || !rec.CreatedAt.Equal(l.t0) {
			t.Fatalf("started record = %+v", rec)
		}
		<-inRestart
		// While the service is stopped the variable holds the restore request,
		// naming the backup.
		v := l.nomad.Variable(labVar)
		if v["REQUEST"] != id || v["ACTION"] != "restore" || v["OBJECT"] != labObject || v["NOT_AFTER"] == "" {
			t.Errorf("variable during the run = %v", v)
		}
		release()

		got := l.settledRestore(rec.ID)
		if got.Status != store.BackupSucceeded || got.Message != "" || got.FinishedAt == nil {
			t.Fatalf("settled record = %+v", got)
		}
		if got.Stopped == nil || *got.Stopped != 20*time.Millisecond {
			t.Errorf("stopped = %v, want 20ms from the task's events", got.Stopped)
		}
		if rs := l.nomad.Restarts(); len(rs) != 1 || rs[0].Copy != labCopy || !rs[0].AllTasks {
			t.Errorf("restarts = %+v, want one restart of all tasks of %s", rs, labCopy)
		}
		// A restore adds no backup record.
		if backups, _ := l.store.ListBackups(ctx, "whoami", 0); len(backups) != 1 {
			t.Errorf("backups = %+v, want only the one restored", backups)
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q after the run, want none", l.request())
		}
		if v := l.nomad.Variable(labVar); v["BACKUP_PATH"] != "backups/caravana/whoami/web" || v["RCLONE_CONFIG_DEST_ACCESS_KEY_ID"] != "AK" {
			t.Errorf("variable after the run = %v, want the destination kept", v)
		}
	})

	t.Run("a failed result records its message, and the service is back", func(t *testing.T) {
		l := newVolumeLab(t)
		backupID := l.saveBackupToRestore()
		id := l.id(0)
		l.render(id)
		l.answer(id, "status=failed\nmessage=download of logs: object not found\n")
		rec, err := l.svc.Restore(ctx, "whoami", backupID)
		if err != nil {
			t.Fatal(err)
		}
		got := l.settledRestore(rec.ID)
		if got.Status != store.BackupFailed || got.Message != "download of logs: object not found" {
			t.Errorf("record = %+v, want failed with the script's message", got)
		}
		if got.Stopped == nil {
			t.Error("a failed restore still records how long the service was stopped")
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q, want it cleared", l.request())
		}
	})

	t.Run("no result in time is a timeout, with the request cleared and the lock released", func(t *testing.T) {
		l := newVolumeLab(t)
		backupID := l.saveBackupToRestore()
		l.svc.volume.limit = 20 * time.Millisecond
		l.render(l.id(0))
		rec, err := l.svc.Restore(ctx, "whoami", backupID)
		if err != nil {
			t.Fatal(err)
		}
		got := l.settledRestore(rec.ID)
		if got.Status != store.BackupFailed || !strings.Contains(got.Message, "timed out") {
			t.Errorf("record = %+v, want failed with a timeout", got)
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q, want it cleared", l.request())
		}
	})

	t.Run("a request the copy never rendered fails the restore without a restart", func(t *testing.T) {
		l := newVolumeLab(t) // nothing renders the request
		backupID := l.saveBackupToRestore()
		rec, err := l.svc.Restore(ctx, "whoami", backupID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != store.BackupFailed || !strings.Contains(rec.Message, "didn't pick up the request") || rec.FinishedAt == nil {
			t.Errorf("record = %+v, want failed because the copy didn't pick up the request", rec)
		}
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %+v, want none", rs)
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q, want it cleared", l.request())
		}
	})

	t.Run("no running copy is refused and leaves nothing behind", func(t *testing.T) {
		l := newVolumeLab(t)
		backupID := l.saveBackupToRestore()
		l.nomad.SetCopies("whoami")
		if _, err := l.svc.Restore(ctx, "whoami", backupID); !errors.Is(err, ErrNoRunningCopy) {
			t.Fatalf("Restore = %v, want ErrNoRunningCopy", err)
		}
		if restores, _ := l.store.ListRestores(ctx, "whoami", 0); len(restores) != 0 {
			t.Errorf("restores = %+v, want none", restores)
		}
		l.released()
	})

	t.Run("only a succeeded backup that is still there can be restored", func(t *testing.T) {
		l := newVolumeLab(t)
		done := l.t0
		for _, rec := range []store.BackupRecord{
			{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume, Status: store.BackupFailed, StartedAt: l.t0, FinishedAt: &done},
			{AppID: "whoami", ID: "web-2", Service: "web", Engine: app.BackupVolume, Status: store.BackupSucceeded, Expired: true, Object: labObject, StartedAt: l.t0, FinishedAt: &done},
		} {
			if err := l.store.SaveBackup(ctx, rec); err != nil {
				t.Fatal(err)
			}
			if _, err := l.svc.Restore(ctx, "whoami", rec.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Restore of %s = %v, want ErrNotFound", rec.ID, err)
			}
		}
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %+v, want none", rs)
		}
	})

	t.Run("a second restore during the first is refused at once", func(t *testing.T) {
		l := newVolumeLab(t)
		backupID := l.saveBackupToRestore()
		id := l.id(0)
		l.render(id)
		inRestart, release := l.blockRestart(id, "status=succeeded\n")
		if _, err := l.svc.Restore(ctx, "whoami", backupID); err != nil {
			t.Fatal(err)
		}
		<-inRestart
		done := make(chan error, 1)
		go func() { _, err := l.svc.Restore(ctx, "whoami", backupID); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrBackupRunning) {
				t.Errorf("second Restore = %v, want ErrBackupRunning", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the second Restore waited for the first run instead of being refused")
		}
		// A backup is refused too, until the restore is over.
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrBackupRunning) {
			t.Errorf("BackUpNow during a restore = %v, want ErrBackupRunning", err)
		}
		release()
		l.settledRestore(l.restoreID(0))
		l.released()
	})

	t.Run("a restore is refused while a backup runs", func(t *testing.T) {
		l := newVolumeLab(t)
		backupID := l.saveBackupToRestore()
		id := l.id(0)
		l.render(id)
		inRestart, release := l.blockRestart(id, "status=succeeded\n")
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		<-inRestart
		if _, err := l.svc.Restore(ctx, "whoami", backupID); !errors.Is(err, ErrBackupRunning) {
			t.Errorf("Restore during a backup = %v, want ErrBackupRunning", err)
		}
		release()
		l.settled(id)
		l.released()
	})

	t.Run("a backup and a restore in the same second keep their own request IDs", func(t *testing.T) {
		l := newVolumeLab(t)
		backupID := l.saveBackupToRestore()
		l.render(l.id(0))
		l.answer(l.id(0), "status=succeeded\n")
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		l.settled(l.id(0))
		l.released()

		// The fake clock hasn't moved: the restore's request moves on a second.
		l.render(l.id(1))
		l.answer(l.id(1), "status=succeeded\n")
		rec, err := l.svc.Restore(ctx, "whoami", backupID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.ID != l.restoreID(1) {
			t.Errorf("restore ID = %s, want %s: the second's request ID", rec.ID, l.restoreID(1))
		}
		l.settledRestore(rec.ID)
		l.released()
	})
}

func TestRestoreRecordID(t *testing.T) {
	// Request IDs repeat across apps; restore IDs must not.
	if restoreRecordID("a", "web-1") == restoreRecordID("b", "web-1") {
		t.Error("two apps' restores of the same request ID share a record ID")
	}
	for _, tt := range []struct{ app, request string }{{"whoami", "web-1792033207"}, {"my-app", "db-1-5"}, {"a", "a-a-1"}} {
		if got := requestIDOfRestore(tt.app, restoreRecordID(tt.app, tt.request)); got != tt.request {
			t.Errorf("requestIDOfRestore(%q) = %q, want %q", tt.app, got, tt.request)
		}
	}
}

// Caravana restarting during a restore settles the restore the way it settles a
// backup, from its result file.
func TestResumeVolumeRestores(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		startedAgo time.Duration
		copy       bool
		result     string
		wantStatus string
		wantMsg    string
	}{
		{name: "the result file is there", startedAgo: time.Second, copy: true, result: "status=succeeded\n", wantStatus: store.BackupSucceeded},
		{name: "a failed result file", startedAgo: time.Second, copy: true, result: "status=failed\nmessage=boom\n",
			wantStatus: store.BackupFailed, wantMsg: "boom"},
		{name: "no result and the run's time is up", startedAgo: 10 * time.Minute, copy: true,
			wantStatus: store.BackupFailed, wantMsg: restartedMessage},
		{name: "the copy is gone", startedAgo: time.Second, copy: false,
			wantStatus: store.BackupFailed, wantMsg: restartedMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newVolumeLab(t)
			id := l.id(0)
			started := time.Now().Add(-tt.startedAgo).UTC()
			rec := store.RestoreRecord{ID: restoreRecordID("whoami", id), AppID: "whoami", Service: "web", BackupID: "web-1",
				Engine: app.BackupVolume, Status: store.BackupRunning, CreatedAt: started}
			if err := l.store.SaveRestore(ctx, rec); err != nil {
				t.Fatal(err)
			}
			items := l.nomad.Variable(labVar)
			items["REQUEST"], items["ACTION"] = id, "restore"
			l.nomad.SetVariable(labVar, items)
			if !tt.copy {
				l.nomad.SetCopies("whoami")
			}
			if tt.result != "" {
				l.nomad.WriteAllocFile(labCopy, "alloc/data/caravana/"+id+".result", tt.result)
			}

			startService(t, l.store, l.registry, l.nomad, l.secrets)
			got := l.settledRestore(rec.ID)
			if got.Status != tt.wantStatus || got.Message != tt.wantMsg {
				t.Errorf("record = %+v, want %s %q", got, tt.wantStatus, tt.wantMsg)
			}
			waitFor(t, "the request to be cleared", func() bool { return l.nomad.Variable(labVar)["REQUEST"] == "" })
			l.released() // the lock was taken and given back
		})
	}

	// A Postgres restore is Nomad's to settle (the watcher, which finds no job
	// here and says so): the volume resume leaves it alone.
	t.Run("a postgres restore is not settled as a volume run", func(t *testing.T) {
		l := newVolumeLab(t)
		rec := store.RestoreRecord{ID: "r1", AppID: "whoami", Service: "web", BackupID: "web-1", Status: store.BackupRunning, CreatedAt: l.t0}
		if err := l.store.SaveRestore(ctx, rec); err != nil {
			t.Fatal(err)
		}
		startService(t, l.store, l.registry, l.nomad, l.secrets)
		got := l.settledRestore("r1")
		if got.Message == restartedMessage || got.Stopped != nil {
			t.Errorf("a postgres restore was settled by the volume resume: %+v", got)
		}
	})
}
