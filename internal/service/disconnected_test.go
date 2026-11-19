package service

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A copy whose server stopped answering (Nomad's "unknown", kept for the service's lost_after) is
// no problem of its own: it isn't unhealthy, it isn't a crash loop however many times it restarted
// before the cut, and the server's item speaks for it (task ctbb).
func TestADisconnectedCopyIsNoCopyItem(t *testing.T) {
	now := attNow
	old := now.Add(-10 * time.Minute)
	copies := []Copy{
		{ID: "d1", Service: "web", Version: 1, Status: deploy.CopyDisconnected, Restarts: 5, LastRestart: &old, StartedAt: &old},
		{ID: "d2", Service: "web", Version: 1, Status: deploy.CopyDisconnected, Description: "the node is disconnected"},
	}
	if got := copyItems(now, "blog", copies, nil, false); len(got) != 0 {
		t.Errorf("items for disconnected copies = %+v, want none", got)
	}
	if consideredCopy(now, copies[0]) {
		t.Error("a disconnected copy counts for the crash rules")
	}
	if n := crashesInHour(now, copies); n != 0 {
		t.Errorf("crashesInHour = %d, want 0", n)
	}
	// An unhealthy copy beside them still is one.
	copies = append(copies, Copy{ID: "u1", Service: "web", Version: 1, Status: deploy.CopyUnhealthy, StartedAt: &old})
	got := copyItems(now, "blog", copies, nil, false)
	if len(got) != 1 || got[0].Kind != AttentionUnhealthy || len(got[0].Copies) != 1 || got[0].Copies[0] != "u1" {
		t.Errorf("items = %+v, want one unhealthy item over u1 alone", got)
	}
}

func TestCopyPhaseOfADisconnectedCopy(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    deploy.Copy
		want string
	}{
		{"disconnected", deploy.Copy{Status: deploy.CopyDisconnected}, CopyPhaseDisconnected},
		{"being stopped wins", deploy.Copy{Status: deploy.CopyDisconnected, Stopping: true}, CopyPhaseStopping},
		{"healthy", deploy.Copy{Status: deploy.CopyHealthy}, CopyPhaseRunning},
	} {
		if got := copyPhase(tc.c); got != tc.want {
			t.Errorf("%s: phase = %s, want %s", tc.name, got, tc.want)
		}
	}
	if phaseRank(CopyPhaseDisconnected) >= phaseRank(CopyPhaseRunning) || phaseRank(CopyPhaseDisconnected) <= phaseRank(CopyPhaseStarting) {
		t.Error("a disconnected copy sorts after starting ones and before the running ones")
	}
}

// A move leaves a disconnected copy alone: it can't be stopped from here.
func TestClassifyCopiesSkipsADisconnectedCopy(t *testing.T) {
	apps := []store.AppRecord{{App: app.App{ID: "blog", Services: []app.Service{{Name: "web", Image: "x/y:1"}}}}}
	byJob := map[string][]deploy.Copy{"blog": {
		{ID: "b1", Service: "web", Node: "two", Status: deploy.CopyDisconnected},
		{ID: "b2", Service: "web", Node: "two", Status: deploy.CopyHealthy},
	}}
	movables, stays := classifyCopies(apps, byJob, "two", []moveTarget{{Name: "one", Managed: true, Labels: map[string]string{}}})
	if len(movables) != 1 || movables[0].CopyID != "b2" || len(stays) != 0 {
		t.Errorf("movables = %+v, stays = %+v, want only b2 movable", movables, stays)
	}
}

// A service with a disconnected copy has no CPU or memory in the sample: its server doesn't answer, so
// a sum of the other copies would be a smaller number that reads like idle (and could scale it down).
func TestSamplerLeavesAGapForADisconnectedCopy(t *testing.T) {
	key := deploy.ServiceKey{JobID: "blog", Group: "web"}
	copy1 := deploy.Copy{ID: "c1", Service: "web", Status: deploy.CopyHealthy}
	copy2 := deploy.Copy{ID: "c2", Service: "web", Status: deploy.CopyDisconnected}
	reads := func(copies ...deploy.Copy) *samplerReads {
		return &samplerReads{
			copies: map[string][]deploy.Copy{"blog": copies},
			usage:  map[string]deploy.CopyUsage{"c1": {CPUMHz: 100, MemoryMB: 50}},
		}
	}
	var m store.MetricsSample
	fillNomad(&m, key, reads(copy1))
	if m.CPUMHz == nil || *m.CPUMHz != 100 || m.MemoryMB == nil || *m.MemoryMB != 50 {
		t.Fatalf("one healthy copy: cpu %v, memory %v, want 100 and 50", m.CPUMHz, m.MemoryMB)
	}
	m = store.MetricsSample{}
	fillNomad(&m, key, reads(copy1, copy2))
	if m.CPUMHz != nil || m.MemoryMB != nil {
		t.Errorf("with a disconnected copy: cpu %v, memory %v, want a gap", m.CPUMHz, m.MemoryMB)
	}
}
