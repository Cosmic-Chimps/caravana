package service

import (
	"context"
	"time"

	"github.com/hashicorp/cronexpr"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The scheduler of volume backups (feature 016, research R5). A Postgres backup
// is a periodic Nomad job, and Nomad keeps its clock. A volume backup restarts
// the app's own copy, which a Nomad job could only do with a token bound to
// that copy, so Caravana keeps the clock: a goroutine looks every
// scheduleGap for services whose cron slot has passed.

// clock is where the service gets the time from. Tests set their own.
type clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// catchUp is how late a missed slot may still be run. Caravana down at 03:00
// and back at 03:20 backs up at once; back at noon, it skips the slot and
// waits for the next one: a backup of the middle of the day, run for the night,
// would only surprise.
const catchUp = time.Hour

// maxSlots caps how many slots dueSlot steps through: a schedule that fires
// every minute has 60 in the catch-up window, so this is far above any real
// one and only stops an odd expression from looping.
const maxSlots = 10000

// dueSlot says whether something on the cron schedule spec owes a run at now,
// given base: the slots up to base are not owed. Slots are in UTC. The volume
// backups give the start of the service's newest run (or when the app was
// saved); a schedule (feature 022) gives the later of when it was armed and
// its newest slot, run or skipped.
//
//   - due: a slot fell in (base, now] and is under catchUp old; slot is the
//     latest such one.
//   - skipped: no slot is that recent, but one fell in (base, now-catchUp]: a
//     run was missed and is too late to start. It is only for the log.
//
// A spec that doesn't parse owes nothing (validation refuses one, so this is
// a stored record from an older version at worst).
func dueSlot(spec string, base, now time.Time) (slot time.Time, due, skipped bool) {
	expr, err := cronexpr.Parse(spec)
	if err != nil {
		return time.Time{}, false, false
	}
	now = now.UTC()
	from := base.UTC()
	window := now.Add(-catchUp)
	if from.Before(window) {
		from = window
	}
	t := expr.Next(from)
	for i := 0; i < maxSlots && !t.IsZero() && !t.After(now); i++ {
		slot, due = t, true
		t = expr.Next(t)
	}
	if due {
		return slot, true, false
	}
	// Nothing recent. Was something missed before the window?
	if base.UTC().Before(window) {
		if missed := expr.Next(base.UTC()); !missed.IsZero() && !missed.After(window) {
			return time.Time{}, false, true
		}
	}
	return time.Time{}, false, false
}

// scheduleVolumeBackups runs due volume backups, every gap, until ctx ends. Like
// watchBackups, it does a pass first, so a Caravana that starts at 03:05 doesn't
// wait gap to notice 03:00 went by.
func (s *Service) scheduleVolumeBackups(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	noted := map[string]string{}
	for {
		s.runDueVolumeBackups(ctx, noted)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runDueVolumeBackups is one pass of scheduleVolumeBackups. noted remembers what
// was last logged for each service, so a slot that is skipped, or can't start
// yet, is logged once and not on every pass.
func (s *Service) runDueVolumeBackups(ctx context.Context, noted map[string]string) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		s.log.Warn("schedule volume backups: list apps", "error", err)
		return
	}
	now := s.clock.Now()
	for _, rec := range apps {
		if rec.StoppedAt != nil {
			continue // a stopped app's backups wait for Start: no run, and nothing reported (feature 020.5)
		}
		for _, svc := range rec.App.Services {
			if !hasVolumeBackup(svc) {
				continue
			}
			key := rec.App.ID + "/" + svc.Name
			last, err := s.lastBackupStart(ctx, rec, svc)
			if err != nil {
				s.log.Warn("schedule volume backups: read backups", "app", rec.App.ID, "service", svc.Name, "error", err)
				continue
			}
			slot, due, skipped := dueSlot(svc.Backup.ScheduleOrDefault(), last, now)
			switch {
			case skipped:
				s.noteOnce(noted, key, "skipped:"+last.String(), "a volume backup slot was missed and is too old to run; waiting for the next one",
					"app", rec.App.ID, "service", svc.Name, "schedule", svc.Backup.ScheduleOrDefault())
			case due:
				if _, err := s.backUpNow(ctx, rec.App.ID, svc.Name, "schedule"); err != nil {
					s.noteOnce(noted, key, "failed:"+slot.String()+err.Error(), "scheduled volume backup didn't start; it is tried again until its slot is an hour old",
						"app", rec.App.ID, "service", svc.Name, "slot", slot, "error", err)
				}
			}
		}
	}
}

// lastBackupStart is when the service's newest backup run started, whatever
// became of it: a failed run isn't run again every pass. A service never backed
// up is counted from when its app was last saved, so the first run waits for
// the first slot after that and doesn't claim a slot from before it existed.
func (s *Service) lastBackupStart(ctx context.Context, rec store.AppRecord, svc app.Service) (time.Time, error) {
	backups, err := s.store.ListBackups(ctx, rec.App.ID, 0) // newest first
	if err != nil {
		return time.Time{}, err
	}
	for _, b := range backups {
		if b.Service == svc.Name && b.Engine == app.BackupVolume {
			return b.StartedAt, nil
		}
	}
	return rec.UpdatedAt, nil
}

// noteOnce logs msg at Info unless the last thing logged for key was the same
// state.
func (s *Service) noteOnce(noted map[string]string, key, state, msg string, args ...any) {
	if noted[key] == state {
		return
	}
	noted[key] = state
	s.log.Info(msg, args...)
}
