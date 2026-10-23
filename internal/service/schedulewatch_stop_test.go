package service

import (
	"context"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A watcher pass reads the open runs, then each one's state in Nomad. When
// the owner stops a run between the two reads, the pass holds the row as it
// was (no stop reason) and Nomad's job already stopped. The run is the
// owner's stop, not "stopped outside Caravana" (found by the API's tests
// under -race).
func TestEndRunReadsAStopAskedDuringThePass(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	sc := l.create("x", "0 3 * * *")
	r := l.runNow(sc.ID)
	l.nomad.StartRun(job(r.ID))

	stale := l.stored(r.ID) // what the pass read: running, no reason
	if _, err := l.svc.StopScheduleRun(ctx, sc.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	sched, err := l.store.GetSchedule(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Second)
	l.svc.endRun(ctx, stale, sched, deploy.RunState{Found: true, DesiredStatus: "stop", ExitCode: new(143),
		TaskStartedAt: &started, Terminal: true}, time.Now())

	if row := l.stored(r.ID); row.Status != store.RunStopped || row.Error != "stopped by the owner" {
		t.Errorf("settled %s %q, want stopped by the owner", row.Status, row.Error)
	}
}
