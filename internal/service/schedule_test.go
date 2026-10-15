package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestDueSlot(t *testing.T) {
	at := func(day, hour, minute, second int) time.Time {
		return time.Date(2026, 10, day, hour, minute, second, 0, time.UTC)
	}
	daily := "0 3 * * *"
	tests := []struct {
		name        string
		spec        string
		lastStart   time.Time
		now         time.Time
		wantSlot    time.Time
		wantDue     bool
		wantSkipped bool
	}{
		{name: "daily, the slot has just passed", spec: daily, lastStart: at(14, 3, 0, 5), now: at(15, 3, 0, 20),
			wantSlot: at(15, 3, 0, 0), wantDue: true},
		{name: "daily, before the slot", spec: daily, lastStart: at(14, 3, 0, 5), now: at(15, 2, 59, 59)},
		{name: "daily, already run for this slot", spec: daily, lastStart: at(15, 3, 0, 5), now: at(15, 3, 30, 0)},
		{name: "a manual run just before the slot doesn't cover it", spec: daily, lastStart: at(15, 2, 0, 0), now: at(15, 3, 0, 10),
			wantSlot: at(15, 3, 0, 0), wantDue: true},
		{name: "a manual run after the slot does", spec: daily, lastStart: at(15, 3, 10, 0), now: at(15, 3, 20, 0)},
		{name: "hourly, the latest of several missed slots", spec: "0 * * * *", lastStart: at(15, 1, 5, 0), now: at(15, 3, 30, 0),
			wantSlot: at(15, 3, 0, 0), wantDue: true},
		{name: "after a restart within the hour", spec: daily, lastStart: at(14, 3, 0, 5), now: at(15, 3, 59, 0),
			wantSlot: at(15, 3, 0, 0), wantDue: true},
		{name: "exactly an hour late is too late", spec: daily, lastStart: at(14, 3, 0, 5), now: at(15, 4, 0, 0),
			wantSkipped: true},
		{name: "missed by more than an hour", spec: daily, lastStart: at(14, 3, 0, 5), now: at(15, 9, 0, 0), wantSkipped: true},
		{name: "missed by days, with several slots behind", spec: daily, lastStart: at(1, 3, 0, 5), now: at(15, 9, 0, 0), wantSkipped: true},
		{name: "never run, counted from when the app was saved: no slot yet", spec: daily, lastStart: at(15, 3, 30, 0), now: at(15, 5, 0, 0)},
		{name: "a schedule that doesn't parse owes nothing", spec: "every day", lastStart: at(14, 3, 0, 5), now: at(15, 3, 0, 20)},
		{name: "a weekly schedule, the slot is Monday", spec: "0 3 * * 1", lastStart: at(5, 3, 0, 5), now: at(12, 3, 10, 0),
			wantSlot: at(12, 3, 0, 0), wantDue: true},
		{name: "a time zone other than UTC is read as UTC", spec: daily, lastStart: at(14, 3, 0, 5).In(time.FixedZone("x", 5*3600)),
			now: at(15, 3, 0, 20).In(time.FixedZone("y", -3*3600)), wantSlot: at(15, 3, 0, 0), wantDue: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slot, due, skipped := dueSlot(tt.spec, tt.lastStart, tt.now)
			if due != tt.wantDue || skipped != tt.wantSkipped || !slot.Equal(tt.wantSlot) {
				t.Errorf("dueSlot = (%v, %v, %v), want (%v, %v, %v)", slot, due, skipped, tt.wantSlot, tt.wantDue, tt.wantSkipped)
			}
		})
	}
}

// The scheduler starts due volume backups once, leaves a Postgres service to
// its Nomad job, and doesn't start a run whose slot is too old.
func TestRunDueVolumeBackups(t *testing.T) {
	ctx := context.Background()
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC) }

	t.Run("a due slot starts one backup, not a second", func(t *testing.T) {
		l := newVolumeLab(t)
		last := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume,
			Status: store.BackupSucceeded, StartedAt: day(14, 3, 0).Add(5 * time.Second)}
		if err := l.store.SaveBackup(ctx, last); err != nil {
			t.Fatal(err)
		}
		l.clock.Set(day(15, 3, 20))
		id := fmt.Sprintf("web-%d", day(15, 3, 20).Unix())
		l.render(id)
		l.answer(id, "status=succeeded\n")
		noted := map[string]string{}

		l.svc.runDueVolumeBackups(ctx, noted)
		got := l.settled(id)
		if got.Status != store.BackupSucceeded || !got.StartedAt.Equal(day(15, 3, 20)) {
			t.Fatalf("the scheduled backup = %+v", got)
		}
		l.released()

		// Another pass in the same slot: it already ran.
		l.clock.Set(day(15, 3, 21))
		l.svc.runDueVolumeBackups(ctx, noted)
		time.Sleep(30 * time.Millisecond)
		if rs := l.nomad.Restarts(); len(rs) != 1 {
			t.Errorf("restarts = %d after two passes of one slot, want 1", len(rs))
		}
	})

	t.Run("a missed slot over an hour old is skipped, and said once", func(t *testing.T) {
		l := newVolumeLab(t)
		last := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume,
			Status: store.BackupSucceeded, StartedAt: day(14, 3, 0).Add(5 * time.Second)}
		if err := l.store.SaveBackup(ctx, last); err != nil {
			t.Fatal(err)
		}
		l.clock.Set(day(15, 12, 0))
		noted := map[string]string{}
		l.svc.runDueVolumeBackups(ctx, noted)
		l.svc.runDueVolumeBackups(ctx, noted)
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %+v, want none", rs)
		}
		if backups, _ := l.store.ListBackups(ctx, "whoami", 0); len(backups) != 1 {
			t.Errorf("backups = %d, want only the old one", len(backups))
		}
		if len(noted) != 1 {
			t.Errorf("noted = %v, want the skip noted once", noted)
		}
	})

	t.Run("a failed earlier run isn't retried on every pass", func(t *testing.T) {
		l := newVolumeLab(t)
		failed := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume,
			Status: store.BackupFailed, StartedAt: day(15, 3, 0).Add(5 * time.Second)}
		if err := l.store.SaveBackup(ctx, failed); err != nil {
			t.Fatal(err)
		}
		l.clock.Set(day(15, 3, 20))
		l.svc.runDueVolumeBackups(ctx, map[string]string{})
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %+v, want none: the slot's run failed, and the next slot is tomorrow", rs)
		}
	})

	t.Run("a due slot with no running copy is tried again, and noted once", func(t *testing.T) {
		l := newVolumeLab(t)
		l.nomad.SetCopies("whoami")
		last := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume,
			Status: store.BackupSucceeded, StartedAt: day(14, 3, 0).Add(5 * time.Second)}
		if err := l.store.SaveBackup(ctx, last); err != nil {
			t.Fatal(err)
		}
		l.clock.Set(day(15, 3, 20))
		noted := map[string]string{}
		l.svc.runDueVolumeBackups(ctx, noted)
		l.svc.runDueVolumeBackups(ctx, noted)
		if len(noted) != 1 {
			t.Errorf("noted = %v, want one entry for the failed start", noted)
		}
		// The copy comes back within the hour: the next pass runs the backup.
		l.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
		id := fmt.Sprintf("web-%d", day(15, 3, 20).Unix())
		l.render(id)
		l.answer(id, "status=succeeded\n")
		l.svc.runDueVolumeBackups(ctx, noted)
		if got := l.settled(id); got.Status != store.BackupSucceeded {
			t.Errorf("record = %+v", got)
		}
	})

	t.Run("a service never backed up waits for the first slot after its app was saved", func(t *testing.T) {
		l := newVolumeLab(t)
		rec, err := l.store.GetApp(ctx, "whoami")
		if err != nil {
			t.Fatal(err)
		}
		// The slot after the save: tomorrow at 03:00, whatever day the test runs.
		saved := rec.UpdatedAt
		next := time.Date(saved.Year(), saved.Month(), saved.Day(), 3, 0, 0, 0, time.UTC)
		for !next.After(saved) {
			next = next.AddDate(0, 0, 1)
		}
		l.clock.Set(next.Add(-time.Minute))
		l.svc.runDueVolumeBackups(ctx, map[string]string{})
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Fatalf("restarts = %+v before the first slot", rs)
		}
		l.clock.Set(next.Add(time.Minute))
		id := fmt.Sprintf("web-%d", next.Add(time.Minute).Unix())
		l.render(id)
		l.answer(id, "status=succeeded\n")
		l.svc.runDueVolumeBackups(ctx, map[string]string{})
		if got := l.settled(id); got.Status != store.BackupSucceeded {
			t.Errorf("record = %+v", got)
		}
	})

	t.Run("a postgres service is left to its Nomad job", func(t *testing.T) {
		e := newEnv(t)
		setDestination(t, e)
		deployHealthy(t, e, db())
		e.svc.clock.(*fakeClock).Set(day(15, 3, 20))
		e.svc.runDueVolumeBackups(ctx, map[string]string{})
		if backups, _ := e.store.ListBackups(ctx, "whoami", 0); len(backups) != 0 {
			t.Errorf("the scheduler started %+v for a postgres service", backups)
		}
	})
}
