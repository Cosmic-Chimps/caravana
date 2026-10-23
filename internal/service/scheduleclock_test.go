package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// clockAt is 2026-10-15 at hh:mm:ss UTC.
func clockAt(hh, mm, ss int) time.Time { return time.Date(2026, 10, 15, hh, mm, ss, 0, time.UTC) }

// tick is one pass of the schedules' clock at t.
func (l *schedLab) tick(t time.Time) {
	l.clock.Set(t)
	l.svc.runDueSchedules(context.Background(), map[string]string{})
}

// slots are a schedule's rows as "<slot hh:mm> <status>", newest first.
func (l *schedLab) slots(id string) string {
	var out []string
	for _, r := range l.runs(id) {
		slot := "-"
		if r.SlotAt != nil {
			slot = r.SlotAt.Format("15:04")
		}
		out = append(out, slot+" "+r.Status)
	}
	return strings.Join(out, ", ")
}

// endAll ends every open run of whoami with exit 0 and settles them.
func (l *schedLab) endAll() {
	l.t.Helper()
	open, err := l.store.AllOpenRuns(context.Background())
	if err != nil {
		l.t.Fatal(err)
	}
	for _, r := range open {
		l.nomad.FinishRunExit(job(r.ID), 0)
	}
	l.follow()
}

func TestScheduleClock(t *testing.T) {
	ctx := context.Background()

	t.Run("a due slot runs once, and not again", func(t *testing.T) {
		l := newSchedLab(t)
		l.clock.Set(clockAt(3, 0, 0))
		sc := l.create("five", "*/5 * * * *")
		l.tick(clockAt(3, 4, 59))
		if got := l.slots(sc.ID); got != "" {
			t.Fatalf("before the slot: %s", got)
		}
		l.tick(clockAt(3, 5, 10))
		l.tick(clockAt(3, 5, 20))
		if got := l.slots(sc.ID); got != "03:05 running" {
			t.Fatalf("after the slot: %s", got)
		}
		if r := l.runs(sc.ID)[0]; r.Trigger != RunTriggerTimer {
			t.Errorf("trigger = %s", r.Trigger)
		}
		l.endAll()
		l.tick(clockAt(3, 9, 0))
		if got := l.slots(sc.ID); got != "03:05 ok" {
			t.Errorf("after the run ended: %s", got)
		}
	})

	t.Run("a paused schedule doesn't run; resumed, it takes only the next slot", func(t *testing.T) {
		l := newSchedLab(t)
		l.clock.Set(clockAt(3, 0, 0))
		sc := l.create("five", "*/5 * * * *")
		l.clock.Set(clockAt(3, 1, 0))
		if _, err := l.svc.PauseSchedule(ctx, sc.ID); err != nil {
			t.Fatal(err)
		}
		l.tick(clockAt(3, 12, 0))
		if got := l.slots(sc.ID); got != "" {
			t.Fatalf("paused: %s", got)
		}
		if _, err := l.svc.ResumeSchedule(ctx, sc.ID); err != nil {
			t.Fatal(err)
		}
		l.tick(clockAt(3, 12, 30)) // 03:05 and 03:10 passed while paused
		if got := l.slots(sc.ID); got != "" {
			t.Fatalf("right after resume: %s", got)
		}
		l.tick(clockAt(3, 15, 5))
		if got := l.slots(sc.ID); got != "03:15 running" {
			t.Errorf("after the next slot: %s", got)
		}
	})

	t.Run("a new cron line doesn't owe the old one's slots", func(t *testing.T) {
		l := newSchedLab(t)
		l.clock.Set(clockAt(3, 0, 0))
		sc := l.create("tidy", "0 * * * *")
		l.clock.Set(clockAt(3, 30, 0))
		if _, err := l.svc.UpdateSchedule(ctx, sc.ID, NewSchedule{Name: "tidy", Command: []string{"true"}, Cron: "*/10 * * * *"}); err != nil {
			t.Fatal(err)
		}
		l.tick(clockAt(3, 30, 5)) // 03:10, 03:20 and 03:30 are before the edit
		if got := l.slots(sc.ID); got != "" {
			t.Fatalf("right after the edit: %s", got)
		}
		l.tick(clockAt(3, 40, 5))
		if got := l.slots(sc.ID); got != "03:40 running" {
			t.Errorf("after the next slot: %s", got)
		}
	})

	t.Run("the catch-up window", func(t *testing.T) {
		l := newSchedLab(t)
		l.clock.Set(clockAt(3, 0, 0))
		late := l.create("late", "0 4 * * *")
		missed := l.create("missed", "30 3 * * *")
		// Caravana was down from 03:20 to 04:50: 04:00 is under an hour
		// old, 03:30 isn't.
		l.tick(clockAt(4, 50, 0))
		if got := l.slots(late.ID); got != "04:00 running" {
			t.Errorf("a slot 50 minutes late: %s", got)
		}
		if got := l.slots(missed.ID); got != "" {
			t.Errorf("a slot 80 minutes late: %s", got)
		}
	})

	t.Run("skip if running", func(t *testing.T) {
		l := newSchedLab(t)
		l.clock.Set(clockAt(3, 0, 0))
		sc := l.create("five", "*/5 * * * *")
		l.tick(clockAt(3, 5, 1))
		l.tick(clockAt(3, 10, 1))
		l.tick(clockAt(3, 10, 30))
		if got := l.slots(sc.ID); got != "03:10 skipped, 03:05 running" {
			t.Fatalf("slots: %s", got)
		}
		if r := l.runs(sc.ID)[0]; r.Error != "the run before was still going" || r.FinishedAt == nil || r.DeploymentID != "" {
			t.Errorf("skipped row = %+v", r)
		}
		l.endAll()
		l.tick(clockAt(3, 15, 1))
		if got := l.slots(sc.ID); got != "03:15 running, 03:10 skipped, 03:05 ok" {
			t.Errorf("after the run ended: %s", got)
		}
	})

	t.Run("overlap allowed: at most three at once", func(t *testing.T) {
		l := newSchedLab(t)
		l.clock.Set(clockAt(3, 0, 0))
		skip := false
		sc, err := l.svc.CreateSchedule(ctx, NewSchedule{App: "whoami", Name: "many", Command: []string{"sleep", "600"},
			Cron: "* * * * *", SkipIfRunning: &skip})
		if err != nil {
			t.Fatal(err)
		}
		for m := 1; m <= 4; m++ {
			l.tick(clockAt(3, m, 1))
		}
		if got := l.slots(sc.ID); got != "03:04 skipped, 03:03 running, 03:02 running, 03:01 running" {
			t.Fatalf("slots: %s", got)
		}
		if r := l.runs(sc.ID)[0]; r.Error != "3 runs already going" {
			t.Errorf("skipped because %q", r.Error)
		}
	})

	t.Run("a slot waits while the app can't run, and nothing is recorded", func(t *testing.T) {
		l := newSchedLab(t)
		l.clock.Set(clockAt(3, 0, 0))
		sc := l.create("five", "*/5 * * * *")

		// A deploy going.
		if _, err := l.store.CreateDeployment(ctx, "whoami", app.Deployment{ID: "dep-going", Trigger: app.TriggerManual,
			Digests: map[string]string{"web": "traefik/whoami@" + whoamiDigest}}, nil, nil, ""); err != nil {
			t.Fatal(err)
		}
		l.tick(clockAt(3, 5, 1))
		if got := l.slots(sc.ID); got != "" {
			t.Fatalf("during a deploy: %s", got)
		}
		// It ends healthy; the slot is still owed.
		if _, err := l.store.UpdateDeployment(ctx, "dep-going", store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: new(uint64(1))}); err != nil {
			t.Fatal(err)
		}
		l.tick(clockAt(3, 6, 0))
		if got := l.slots(sc.ID); got != "" {
			t.Fatalf("while the deploy rolls out: %s", got)
		}
		if _, err := l.store.UpdateDeployment(ctx, "dep-going", store.DeploymentUpdate{Status: store.StatusHealthy}); err != nil {
			t.Fatal(err)
		}
		l.tick(clockAt(3, 7, 0))
		if got := l.slots(sc.ID); got != "03:05 running" {
			t.Fatalf("after the deploy: %s", got)
		}
		if r := l.runs(sc.ID)[0]; r.DeploymentID != "dep-going" {
			t.Errorf("the run used deployment %q, want the new one", r.DeploymentID)
		}
		l.endAll()

		// A stopped app.
		if err := l.store.SetAppStopped(ctx, "whoami", new(clockAt(3, 8, 0))); err != nil {
			t.Fatal(err)
		}
		l.tick(clockAt(3, 10, 1))
		if got := l.slots(sc.ID); got != "03:05 ok" {
			t.Errorf("while stopped: %s", got)
		}
	})
}
