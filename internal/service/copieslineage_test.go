package service

import (
	"context"
	"testing"

	"github.com/hashicorp/nomad/api"
)

// A failed copy Nomad has replaced is history: only the newest copy of each lineage is listed. The
// replaced one says so itself (NextAllocation) or the next one names it (the reschedule tracker); a
// failed copy nobody replaced stays, since it is what needs the owner.
func TestHostCopiesListTheNewestOfEachLineage(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	saveApp(t, e, appWith("crashy", 1))
	failed, run := api.AllocClientStatusFailed, api.AllocClientStatusRunning

	// failedCopy is a failed copy that Nomad told to stop; next is the copy it says replaced it, and
	// replaced the copy its own reschedule tracker names.
	failedCopy := func(id, next, replaced string) api.AllocationListStub {
		s := hostStub(id, "web", failed, api.AllocDesiredStatusStop, nil)
		s.NextAllocation = next
		if replaced != "" {
			s.RescheduleTracker = &api.RescheduleTracker{Events: []*api.RescheduleEvent{{PrevAllocID: replaced}}}
		}
		return s
	}
	running := hostStub("r1", "web", run, api.AllocDesiredStatusRun, map[string][3]int{"web": {100, 64, 64}})
	running.RescheduleTracker = &api.RescheduleTracker{Events: []*api.RescheduleEvent{{PrevAllocID: "g1"}}}

	e.nomad.SetCopies("crashy",
		failedCopy("f1", "f2", ""),   // replaced: it names its next copy
		failedCopy("f2", "f3", "f1"), // replaced by f3
		failedCopy("f3", "", "f2"),   // the newest of its lineage and failed: listed
		failedCopy("f4", "", ""),     // a lineage of its own that nobody replaced: listed
		failedCopy("g1", "", ""),     // replaced only because r1 names it as the copy it replaced
		running,
	)

	got, err := e.svc.HostCopies(ctx)
	if err != nil {
		t.Fatalf("HostCopies: %v", err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID+":"+c.Phase)
	}
	want := []string{"f3:failed", "f4:failed", "r1:running"}
	if len(ids) != len(want) {
		t.Fatalf("copies = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("copies = %v, want %v", ids, want)
		}
	}
}

// A copy of a rollout that hasn't decided its health is starting on the server page too, and a
// decided one runs.
func TestHostCopiesStartingWhileTheRolloutIsUndecided(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	saveApp(t, e, appWith("blog", 1))
	e.nomad.SetDeployment("blog", 7, api.DeploymentStatusRunning)
	yes := true
	reserve := map[string][3]int{"web": {100, 64, 64}}
	undecided := hostStub("c-new", "web", api.AllocClientStatusRunning, api.AllocDesiredStatusRun, reserve)
	undecided.JobVersion = 7
	decided := hostStub("c-ok", "web", api.AllocClientStatusRunning, api.AllocDesiredStatusRun, reserve)
	decided.JobVersion, decided.DeploymentStatus = 7, &api.AllocDeploymentStatus{Healthy: &yes}
	e.nomad.SetCopies("blog", undecided, decided)

	got, err := e.svc.HostCopies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phases := map[string]string{}
	for _, c := range got {
		phases[c.ID] = c.Phase
	}
	if phases["c-new"] != CopyPhaseStarting || phases["c-ok"] != CopyPhaseRunning {
		t.Errorf("phases = %v, want c-new starting and c-ok running", phases)
	}
}
