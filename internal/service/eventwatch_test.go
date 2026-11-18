package service

import (
	"context"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A rollout ends when Nomad's event stream says it moved, not when a timer
// reads it: with the resync set to an hour only the event can wake the loop.
func TestRolloutEndsOnADeploymentEvent(t *testing.T) {
	e := newEnv(t)
	e.svc.rolloutResync = time.Hour
	connected(t, e)

	start := time.Now()
	res, err := e.svc.Deploy(context.Background(), whoami(), "manual")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	rec := waitFinal(t, e.store, res.Deployment.ID)
	if rec.Status != store.StatusHealthy {
		t.Errorf("status = %s, want healthy", rec.Status)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the rollout took %v to end; the event should wake it at once", took)
	}
}

// With no event stream at all, the slow read still ends the rollout.
func TestRolloutEndsWithoutTheEventStream(t *testing.T) {
	e := newEnvPrepared(t, func(n *deploytest.Nomad) { n.RefuseEventStream(403) })
	e.svc.rolloutResync = 30 * time.Millisecond

	res, err := e.svc.Deploy(context.Background(), whoami(), "manual")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if rec := waitFinal(t, e.store, res.Deployment.ID); rec.Status != store.StatusHealthy {
		t.Errorf("status = %s, want healthy", rec.Status)
	}
}

// The run watcher's own gap is an hour: only Nomad's events (and the run's due
// time) make it look, and they settle the run.
func TestRunWatcherFollowsEvents(t *testing.T) {
	l := newSchedLabEvents(t, true)
	connected(t, l.env)
	sc := l.create("events", "0 3 * * *", "echo", "hi")
	r := l.runNow(sc.ID)

	l.nomad.StartRun(job(r.ID))
	l.nomad.FinishRunExit(job(r.ID), 0)

	waitFor(t, "the run to be settled by the watcher", func() bool {
		return l.stored(r.ID).Status == store.RunOK
	})
}

func TestRunDue(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := t0.Add(d); return &t }
	const limit, startDeadline = 5 * time.Minute, 2 * time.Minute

	tests := []struct {
		name string
		run  store.ScheduleRunRecord
		st   deploy.RunState
		want time.Time
	}{
		{"not started: the start deadline", store.ScheduleRunRecord{StartedAt: t0}, deploy.RunState{},
			t0.Add(startDeadline)},
		{"started: the limit", store.ScheduleRunRecord{StartedAt: t0}, deploy.RunState{TaskStartedAt: at(time.Minute)},
			t0.Add(time.Minute + limit)},
		{"asked to stop, never placed: the grace of the stop",
			store.ScheduleRunRecord{StartedAt: t0, StopReason: stopOwner, StopRequestedAt: at(10 * time.Second)}, deploy.RunState{},
			t0.Add(10*time.Second + runStopGrace)},
		{"asked to stop, has a copy: Nomad's events say when it ends",
			store.ScheduleRunRecord{StartedAt: t0, StopReason: stopOwner, StopRequestedAt: at(10 * time.Second)},
			deploy.RunState{AllocID: "a1", TaskStartedAt: at(time.Minute)}, time.Time{}},
		{"asked to stop with no time recorded", store.ScheduleRunRecord{StartedAt: t0, StopReason: stopOwner}, deploy.RunState{}, time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := runDue(tc.run, tc.st, limit, startDeadline); !got.Equal(tc.want) {
				t.Errorf("runDue = %v, want %v", got, tc.want)
			}
		})
	}
}

// The rollout wake hub: every waiter of a key wakes once, and a woken key
// starts fresh.
func TestWakeHub(t *testing.T) {
	var h wakeHub
	a, b, other := h.channel("k"), h.channel("k"), h.channel("other")
	h.wake("k")
	for name, ch := range map[string]<-chan struct{}{"first": a, "second": b} {
		select {
		case <-ch:
		default:
			t.Errorf("the %s waiter of k wasn't woken", name)
		}
	}
	select {
	case <-other:
		t.Error("waking k woke another key")
	default:
	}
	fresh := h.channel("k")
	select {
	case <-fresh:
		t.Error("a channel taken after the wake is already closed")
	default:
	}
	h.wake("never-waited") // costs nothing
	h.wakeAll()
	for name, ch := range map[string]<-chan struct{}{"fresh": fresh, "other": other} {
		select {
		case <-ch:
		default:
			t.Errorf("wakeAll missed %s", name)
		}
	}
}
