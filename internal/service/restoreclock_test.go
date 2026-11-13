package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The restore clock (feature 036), on the volume lab: kuma with a volume backup
// whose stored policy gets a restore schedule. The fake clock starts on
// 2026-10-15 03:00 UTC.

// setRestoreSchedule gives the stored app's service a restore schedule. It does
// not deploy, so a test that deploys again (released) loses it.
func (l *volumeLab) setRestoreSchedule(spec, from string) {
	l.t.Helper()
	rec, err := l.store.GetApp(context.Background(), "whoami")
	if err != nil {
		l.t.Fatal(err)
	}
	rec.App.Services[0].Backup.Restore = &app.RestoreSchedule{Schedule: spec, From: from}
	if _, err := l.store.SaveApp(context.Background(), rec.App, nil); err != nil {
		l.t.Fatal(err)
	}
}

// pinGolden stores a succeeded volume backup of web, pins it as golden, and
// returns it.
func (l *volumeLab) pinGolden() store.BackupRecord {
	l.t.Helper()
	id := l.saveBackupToRestore()
	if _, err := l.store.PinBackup(context.Background(), "whoami", id, "golden", l.t0); err != nil {
		l.t.Fatal(err)
	}
	b, err := l.store.GetBackup(context.Background(), "whoami", id)
	if err != nil {
		l.t.Fatal(err)
	}
	return b
}

// seedScheduled records a finished scheduled restore at "at": the clock counts
// slots from it.
func (l *volumeLab) seedScheduled(at time.Time) {
	l.t.Helper()
	done := at.Add(time.Second)
	err := l.store.SaveRestore(context.Background(), store.RestoreRecord{ID: fmt.Sprintf("seed-%d", at.Unix()), AppID: "whoami",
		Service: "web", BackupID: "web-1791990000", Engine: app.BackupVolume, Status: store.BackupSucceeded, CreatedAt: at,
		FinishedAt: &done, Trigger: store.RestoreTriggerSchedule, Pin: "golden"})
	if err != nil {
		l.t.Fatal(err)
	}
}

// restores returns the app's restore records, newest first.
func (l *volumeLab) restores() []store.RestoreRecord {
	l.t.Helper()
	list, err := l.store.ListRestores(context.Background(), "whoami", 0)
	if err != nil {
		l.t.Fatal(err)
	}
	return list
}

// waitReleased waits for the run's goroutine to give the app's lock back,
// without deploying (which would replace the stored policy).
func (l *volumeLab) waitReleased() {
	l.t.Helper()
	state := l.svc.stateOf("whoami")
	state.mu.Lock()
	defer state.mu.Unlock() // taking the lock is the wait
}

func TestRunDueRestores(t *testing.T) {
	ctx := context.Background()
	day := func(d, h, m, s int) time.Time { return time.Date(2026, 10, d, h, m, s, 0, time.UTC) }
	const daily = "0 4 * * *"

	t.Run("a due slot restores the pinned backup once, as the schedule", func(t *testing.T) {
		l := newVolumeLab(t)
		b := l.pinGolden()
		l.setRestoreSchedule(daily, "golden")
		l.seedScheduled(day(14, 4, 0, 5))
		l.clock.Set(day(15, 4, 0, 20))
		id := fmt.Sprintf("web-%d", day(15, 4, 0, 20).Unix())
		l.render(id)
		l.answer(id, "status=succeeded\nmessage=\n")
		noted := map[string]string{}

		l.svc.runDueRestores(ctx, noted)
		got := l.settledRestore(restoreRecordID("whoami", id))
		if got.Status != store.BackupSucceeded || got.Trigger != store.RestoreTriggerSchedule || got.Pin != "golden" ||
			got.BackupID != b.ID || got.Engine != app.BackupVolume || !got.CreatedAt.Equal(day(15, 4, 0, 20)) {
			t.Fatalf("the scheduled restore = %+v", got)
		}
		if got.Stopped == nil {
			t.Error("a scheduled volume restore records how long the service was stopped, as the owner's does")
		}
		l.waitReleased()

		// The request is the owner's restore of that backup.
		if rs := l.nomad.Restarts(); len(rs) != 1 || !rs[0].AllTasks {
			t.Errorf("restarts = %+v, want one restart of the copy", rs)
		}

		// Another pass in the same slot: it already ran.
		l.clock.Set(day(15, 4, 1, 0))
		l.svc.runDueRestores(ctx, noted)
		time.Sleep(30 * time.Millisecond)
		if n := len(l.restores()); n != 2 { // the seed and this slot's
			t.Errorf("restores = %d after two passes of one slot, want 2", n)
		}
		if rs := l.nomad.Restarts(); len(rs) != 1 {
			t.Errorf("restarts = %d after two passes of one slot, want 1", len(rs))
		}
	})

	t.Run("an owner's restore doesn't move the clock's base", func(t *testing.T) {
		l := newVolumeLab(t)
		l.pinGolden()
		l.setRestoreSchedule(daily, "golden")
		l.seedScheduled(day(14, 4, 0, 5))
		done := day(15, 3, 30, 0)
		if err := l.store.SaveRestore(ctx, store.RestoreRecord{ID: "owner-1", AppID: "whoami", Service: "web", BackupID: "web-1791990000",
			Engine: app.BackupVolume, Status: store.BackupSucceeded, CreatedAt: day(15, 3, 29, 0), FinishedAt: &done}); err != nil {
			t.Fatal(err)
		}
		l.clock.Set(day(15, 4, 0, 20))
		id := fmt.Sprintf("web-%d", day(15, 4, 0, 20).Unix())
		l.render(id)
		l.answer(id, "status=succeeded\nmessage=\n")
		l.svc.runDueRestores(ctx, map[string]string{})
		if got := l.settledRestore(restoreRecordID("whoami", id)); got.Trigger != store.RestoreTriggerSchedule {
			t.Errorf("record = %+v, want the slot to run", got)
		}
		l.waitReleased()
	})

	t.Run("after Caravana was down, the latest missed slot within the hour runs once", func(t *testing.T) {
		l := newVolumeLab(t)
		l.pinGolden()
		l.setRestoreSchedule("0 * * * *", "golden") // hourly
		l.seedScheduled(day(15, 0, 0, 5))
		l.clock.Set(day(15, 3, 30, 0)) // slots at 01:00, 02:00 and 03:00 went by
		id := fmt.Sprintf("web-%d", day(15, 3, 30, 0).Unix())
		l.render(id)
		l.answer(id, "status=succeeded\nmessage=\n")
		noted := map[string]string{}
		l.svc.runDueRestores(ctx, noted)
		l.settledRestore(restoreRecordID("whoami", id))
		l.waitReleased()
		l.svc.runDueRestores(ctx, noted)
		time.Sleep(30 * time.Millisecond)
		if n := len(l.restores()); n != 2 {
			t.Errorf("restores = %d, want the seed and one run for the three missed slots", n)
		}
	})

	t.Run("a missed slot over an hour old runs nothing, records nothing, and is said once", func(t *testing.T) {
		l := newVolumeLab(t)
		l.pinGolden()
		l.setRestoreSchedule(daily, "golden")
		l.seedScheduled(day(14, 4, 0, 5))
		l.clock.Set(day(15, 9, 0, 0))
		noted := map[string]string{}
		l.svc.runDueRestores(ctx, noted)
		l.svc.runDueRestores(ctx, noted)
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %+v, want none", rs)
		}
		if n := len(l.restores()); n != 1 {
			t.Errorf("restores = %d, want only the seed", n)
		}
		if len(noted) != 1 {
			t.Errorf("noted = %v, want the skip noted once", noted)
		}
	})

	t.Run("a service never restored on a schedule counts from when the app was saved", func(t *testing.T) {
		l := newVolumeLab(t)
		l.pinGolden()
		l.setRestoreSchedule(daily, "golden")
		rec, err := l.store.GetApp(ctx, "whoami")
		if err != nil {
			t.Fatal(err)
		}
		next := time.Date(rec.UpdatedAt.Year(), rec.UpdatedAt.Month(), rec.UpdatedAt.Day(), 4, 0, 0, 0, time.UTC)
		for !next.After(rec.UpdatedAt) {
			next = next.AddDate(0, 0, 1)
		}
		l.clock.Set(next.Add(-time.Minute))
		l.svc.runDueRestores(ctx, map[string]string{})
		if n := len(l.restores()); n != 0 {
			t.Fatalf("restores = %d before the first slot, want none", n)
		}
		l.clock.Set(next.Add(time.Minute))
		id := fmt.Sprintf("web-%d", next.Add(time.Minute).Unix())
		l.render(id)
		l.answer(id, "status=succeeded\nmessage=\n")
		l.svc.runDueRestores(ctx, map[string]string{})
		if got := l.settledRestore(restoreRecordID("whoami", id)); got.Status != store.BackupSucceeded {
			t.Errorf("record = %+v", got)
		}
		l.waitReleased()
	})

	// Each refusal leaves one failed record with Caravana's words, starts
	// nothing, and isn't tried again in the same slot.
	refusals := []struct {
		name    string
		prep    func(l *volumeLab)
		status  string
		message string
		backup  bool // the record names the backup the label had
	}{
		{name: "no backup is pinned under the label", prep: func(l *volumeLab) { l.setRestoreSchedule(daily, "golden") },
			status: store.BackupFailed, message: "no backup of web is pinned as golden"},
		{name: "retention deleted the pinned backup", prep: func(l *volumeLab) {
			b := l.pinGolden()
			l.setRestoreSchedule(daily, "golden")
			if err := l.store.ExpireBackups(ctx, "whoami", []string{b.ID}); err != nil {
				t.Fatal(err)
			}
		}, status: store.BackupFailed, message: "the backup pinned as golden was deleted by retention", backup: true},
		{name: "the engine changed", prep: func(l *volumeLab) {
			// A backup made by the engine the service used before, pinned.
			old := store.BackupRecord{AppID: "whoami", ID: "web-1791990000", Service: "web", Engine: app.BackupSQLite,
				Status: store.BackupSucceeded, Object: "backups/caravana/whoami/web/1791990000.sqlite.gz", StartedAt: l.t0.Add(-time.Hour)}
			if err := l.store.SaveBackup(ctx, old); err != nil {
				t.Fatal(err)
			}
			if _, err := l.store.PinBackup(ctx, "whoami", old.ID, "golden", l.t0); err != nil {
				t.Fatal(err)
			}
			l.setRestoreSchedule(daily, "golden")
		}, status: store.BackupFailed, message: "the backup pinned as golden was made by the sqlite engine; web now backs up with volume", backup: true},
		{name: "the service has no running copy", prep: func(l *volumeLab) {
			l.pinGolden()
			l.setRestoreSchedule(daily, "golden")
			l.nomad.SetCopies("whoami")
		}, status: store.BackupFailed, message: "the service has no running copy", backup: true},
		{name: "a backup is running", prep: func(l *volumeLab) {
			l.pinGolden()
			l.setRestoreSchedule(daily, "golden")
			running := store.BackupRecord{AppID: "whoami", ID: "web-1792000000", Service: "web", Engine: app.BackupVolume,
				Status: store.BackupRunning, StartedAt: day(15, 3, 59, 0)}
			if err := l.store.SaveBackup(ctx, running); err != nil {
				t.Fatal(err)
			}
		}, status: store.RestoreSkipped, message: "a backup or restore of web was running", backup: true},
		{name: "a restore is running", prep: func(l *volumeLab) {
			l.pinGolden()
			l.setRestoreSchedule(daily, "golden")
			err := l.store.SaveRestore(ctx, store.RestoreRecord{ID: "owner-1", AppID: "whoami", Service: "web", BackupID: "web-1791990000",
				Engine: app.BackupVolume, Status: store.BackupRunning, CreatedAt: day(15, 3, 59, 0)})
			if err != nil {
				t.Fatal(err)
			}
		}, status: store.RestoreSkipped, message: "a backup or restore of web was running", backup: true},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			l := newVolumeLab(t)
			tt.prep(l)
			l.seedScheduled(day(14, 4, 0, 5))
			l.clock.Set(day(15, 4, 0, 20))
			noted := map[string]string{}
			l.svc.runDueRestores(ctx, noted)

			var got []store.RestoreRecord
			for _, r := range l.restores() {
				if r.Trigger == store.RestoreTriggerSchedule && !strings.HasPrefix(r.ID, "seed-") {
					got = append(got, r)
				}
			}
			if len(got) != 1 {
				t.Fatalf("scheduled restores = %+v, want exactly one record of the refusal", got)
			}
			r := got[0]
			if r.Status != tt.status || r.Message != tt.message || r.Pin != "golden" || r.Engine == "" ||
				r.FinishedAt == nil || !r.CreatedAt.Equal(day(15, 4, 0, 20)) {
				t.Errorf("record = %+v, want %s %q, pin golden, finished", r, tt.status, tt.message)
			}
			if tt.backup && r.BackupID != "web-1791990000" {
				t.Errorf("backup = %q, want the pinned one named", r.BackupID)
			}
			if !tt.backup && r.BackupID != "" {
				t.Errorf("backup = %q, want none named", r.BackupID)
			}
			if rs := l.nomad.Restarts(); len(rs) != 0 {
				t.Errorf("restarts = %+v, want none", rs)
			}

			// The same slot isn't tried again.
			l.clock.Set(day(15, 4, 1, 0))
			l.svc.runDueRestores(ctx, noted)
			n := 0
			for _, r := range l.restores() {
				if r.Trigger == store.RestoreTriggerSchedule && !strings.HasPrefix(r.ID, "seed-") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%d scheduled restores after a second pass, want 1", n)
			}
		})
	}

	t.Run("a stopped app's slots wait: nothing is recorded, and the latest runs once it starts", func(t *testing.T) {
		l := newVolumeLab(t)
		l.pinGolden()
		l.setRestoreSchedule(daily, "golden")
		l.seedScheduled(day(14, 4, 0, 5))
		if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		l.clock.Set(day(15, 4, 0, 20))
		l.svc.runDueRestores(ctx, map[string]string{})
		if n := len(l.restores()); n != 1 {
			t.Fatalf("restores = %d while stopped, want only the seed", n)
		}
		if _, err := l.svc.StartApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		l.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
		l.clock.Set(day(15, 4, 30, 0))
		id := fmt.Sprintf("web-%d", day(15, 4, 30, 0).Unix())
		l.render(id)
		l.answer(id, "status=succeeded\nmessage=\n")
		l.svc.runDueRestores(ctx, map[string]string{})
		if got := l.settledRestore(restoreRecordID("whoami", id)); got.Trigger != store.RestoreTriggerSchedule || got.Status != store.BackupSucceeded {
			t.Errorf("record = %+v, want the slot to run once the app started", got)
		}
		l.waitReleased()
	})

	t.Run("a service without a restore schedule is left alone", func(t *testing.T) {
		l := newVolumeLab(t)
		l.pinGolden()
		l.clock.Set(day(15, 4, 0, 20))
		l.svc.runDueRestores(ctx, map[string]string{})
		if n := len(l.restores()); n != 0 {
			t.Errorf("restores = %d, want none", n)
		}
	})
}

// The owner's restore is recorded as the owner's, with no label.
func TestOwnerRestoreIsRecordedAsTheOwners(t *testing.T) {
	ctx := context.Background()
	l := newVolumeLab(t)
	backupID := l.saveBackupToRestore()
	id := l.id(0)
	l.render(id)
	l.answer(id, "status=succeeded\nmessage=\n")
	rec, err := l.svc.Restore(ctx, "whoami", backupID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Trigger != store.RestoreTriggerOwner || rec.Pin != "" {
		t.Errorf("record = %+v, want trigger owner and no label", rec)
	}
	got := l.settledRestore(rec.ID)
	if got.Trigger != store.RestoreTriggerOwner || got.Pin != "" {
		t.Errorf("settled record = %+v, want trigger owner and no label", got)
	}
	l.released()
}

// A scheduled sqlite restore is the owner's sqlite restore with the schedule's
// trigger: it downloads, stages and swaps.
func TestScheduledSQLiteRestore(t *testing.T) {
	ctx := context.Background()
	l := newSQLiteLab(t)
	b := l.saveSQLiteBackup()
	if _, err := l.store.PinBackup(ctx, "whoami", b.ID, "golden", l.t0); err != nil {
		t.Fatal(err)
	}
	rec, err := l.store.GetApp(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	rec.App.Services[0].Backup.Restore = &app.RestoreSchedule{Schedule: "0 4 * * *", From: "golden"}
	if _, err := l.store.SaveApp(ctx, rec.App, nil); err != nil {
		t.Fatal(err)
	}
	l.seedScheduled(time.Date(2026, 10, 14, 4, 0, 5, 0, time.UTC))
	at := time.Date(2026, 10, 15, 4, 0, 20, 0, time.UTC)
	l.clock.Set(at)
	id := fmt.Sprintf("web-%d", at.Unix())
	l.onSQLite(l.sqliteOK(id))
	l.render(id)
	l.answer(id, "status=succeeded\nmessage=\n")

	l.svc.runDueRestores(ctx, map[string]string{})
	got := l.settledRestore(restoreRecordID("whoami", id))
	if got.Status != store.BackupSucceeded || got.Trigger != store.RestoreTriggerSchedule || got.Pin != "golden" ||
		got.Engine != app.BackupSQLite || got.BackupID != b.ID {
		t.Errorf("the scheduled sqlite restore = %+v", got)
	}
	if got, want := strings.Join(l.scripts(), " "), "download@caravana-upload stage@caravana-sqlite"; got != want {
		t.Errorf("commands = %s, want %s", got, want)
	}
	l.waitReleased()
}

// A deploy whose restore schedule names a label with no backup pinned warns on
// restore.from, and doesn't once one is pinned.
func TestRestoreLabelWarning(t *testing.T) {
	ctx := context.Background()
	l := newVolumeLab(t)
	a := kuma(false)
	a.Services[0].Backup.Restore = &app.RestoreSchedule{Schedule: "0 4 * * *", From: "golden"}

	res, err := l.svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	var found []app.Warning
	for _, w := range res.Warnings {
		if w.Field == "services[0].backup.restore.from" {
			found = append(found, w)
		}
	}
	if len(found) != 1 || !strings.Contains(found[0].Message, "no backup of web is pinned as golden yet") ||
		!strings.Contains(found[0].Message, "/v1/apps/whoami/backups/<backupId>/pin") {
		t.Fatalf("warnings = %+v, want one on restore.from naming the label", res.Warnings)
	}
	waitFinal(t, l.store, res.Deployment.ID)

	// Pinned: no warning. A pinned backup that retention deleted warns again.
	b := l.pinGolden()
	res, err = l.svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if w.Field == "services[0].backup.restore.from" {
			t.Errorf("warning with the backup pinned: %+v", w)
		}
	}
	waitFinal(t, l.store, res.Deployment.ID)
	if err := l.store.ExpireBackups(ctx, "whoami", []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	res, err = l.svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, w := range res.Warnings {
		warned = warned || w.Field == "services[0].backup.restore.from"
	}
	if !warned {
		t.Errorf("warnings = %+v, want one for the pinned backup retention deleted", res.Warnings)
	}
}
