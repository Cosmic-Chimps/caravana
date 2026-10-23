package service

import (
	"context"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// The schedules' clock (feature 022, research R1 and R6). Caravana keeps it,
// as it does for volume backups: every scheduleScanGap it looks at each
// schedule that isn't paused and starts the run its latest slot owes, using
// dueSlot. A slot is owed once: the run (or the skipped row) records it as
// its slot_at, and the clock counts from the later of that and armed_at, which
// is set when the schedule is created, resumed or given a new cron line. A
// slot missed by more than an hour (Caravana was down) is not run.
//
// A slot whose app can't have a run now (stopped, no live version, a deploy
// going) waits: nothing is recorded and the next pass tries again, until the
// slot is an hour old.

// runScheduleClock starts due runs, every gap, until ctx ends. It does a pass
// first.
func (s *Service) runScheduleClock(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	noted := map[string]string{}
	for {
		s.runDueSchedules(ctx, noted)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runDueSchedules is one pass of the clock. noted remembers what was last
// logged for each schedule, so a slot that waits is logged once and not on
// every pass.
func (s *Service) runDueSchedules(ctx context.Context, noted map[string]string) {
	scheds, err := s.store.ListSchedules(ctx, "")
	if err != nil {
		s.log.Warn("schedules' clock: list schedules", "error", err)
		return
	}
	now := s.clock.Now().UTC()
	apps := map[string]store.AppRecord{}
	for _, sc := range scheds {
		if sc.PausedAt != nil {
			continue
		}
		base, err := s.scheduleBase(ctx, sc)
		if err != nil {
			s.log.Warn("schedules' clock: read runs", "schedule", sc.ID, "error", err)
			continue
		}
		slot, due, skipped := dueSlot(sc.Cron, base, now)
		if skipped {
			s.noteOnce(noted, sc.ID, "missed:"+base.String(), "a schedule's slot was missed and is too old to run; waiting for the next one",
				"schedule", sc.ID, "app", sc.AppID, "name", sc.Name)
			continue
		}
		if !due {
			continue
		}
		rec, ok := apps[sc.AppID]
		if !ok {
			if rec, err = s.store.GetApp(ctx, sc.AppID); err != nil {
				s.log.Warn("schedules' clock: read app", "schedule", sc.ID, "app", sc.AppID, "error", err)
				continue
			}
			apps[sc.AppID] = rec
		}
		if blocked := s.runBlocker(ctx, rec, sc.Service); blocked != nil {
			s.noteOnce(noted, sc.ID, "waits:"+slot.String()+blocked.Error(),
				"a schedule's run waits; it is tried again until its slot is an hour old",
				"schedule", sc.ID, "app", sc.AppID, "name", sc.Name, "slot", slot, "reason", blocked)
			continue
		}
		if _, err := s.startRun(ctx, sc, RunTriggerTimer, &slot); err != nil {
			s.noteOnce(noted, sc.ID, "failed:"+slot.String(), "a schedule's run didn't start",
				"schedule", sc.ID, "app", sc.AppID, "name", sc.Name, "slot", slot, "error", err)
		}
	}
}

// scheduleBase is where the clock counts a schedule's slots from: the later
// of when it was armed and its newest slot that has a row (run or skipped).
func (s *Service) scheduleBase(ctx context.Context, sc store.ScheduleRecord) (time.Time, error) {
	runs, err := s.store.ListRuns(ctx, sc.ID, 0)
	if err != nil {
		return time.Time{}, err
	}
	base := sc.ArmedAt
	for _, r := range runs {
		if r.SlotAt != nil && r.SlotAt.After(base) {
			base = *r.SlotAt
		}
	}
	return base, nil
}
