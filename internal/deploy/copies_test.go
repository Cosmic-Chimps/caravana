package deploy

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

func TestCopyOf(t *testing.T) {
	yes, no := true, false
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		stub     api.AllocationListStub
		want     CopyStatus
		terminal bool
	}{
		{"pending", api.AllocationListStub{ClientStatus: "pending"}, CopyStarting, false},
		{"running, no deployment status", api.AllocationListStub{ClientStatus: "running"}, CopyHealthy, false},
		{"running, health not set yet", api.AllocationListStub{ClientStatus: "running",
			DeploymentStatus: &api.AllocDeploymentStatus{}}, CopyStarting, false},
		{"running, healthy", api.AllocationListStub{ClientStatus: "running",
			DeploymentStatus: &api.AllocDeploymentStatus{Healthy: &yes}}, CopyHealthy, false},
		{"running, unhealthy", api.AllocationListStub{ClientStatus: "running",
			DeploymentStatus: &api.AllocDeploymentStatus{Healthy: &no}}, CopyUnhealthy, false},
		{"complete", api.AllocationListStub{ClientStatus: "complete"}, CopyStopped, true},
		{"failed", api.AllocationListStub{ClientStatus: "failed"}, CopyFailed, true},
		{"lost", api.AllocationListStub{ClientStatus: "lost"}, CopyFailed, true},
		{"unknown", api.AllocationListStub{ClientStatus: "unknown"}, CopyUnhealthy, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.stub.ID, tc.stub.TaskGroup, tc.stub.NodeName = "a1", "web", "node-1"
			tc.stub.JobVersion, tc.stub.ClientDescription = 7, "Tasks are running"
			got := copyOf(&tc.stub)
			if got.Status != tc.want || got.Terminal != tc.terminal {
				t.Errorf("status = %s, terminal = %v; want %s, %v", got.Status, got.Terminal, tc.want, tc.terminal)
			}
			if got.ID != "a1" || got.Service != "web" || got.Node != "node-1" || got.Version != 7 ||
				got.Description != "Tasks are running" {
				t.Errorf("copy = %+v", got)
			}
		})
	}

	t.Run("restarts are summed over tasks and StartedAt is the first start", func(t *testing.T) {
		got := copyOf(&api.AllocationListStub{ClientStatus: "running", TaskStates: map[string]*api.TaskState{
			"web":     {Restarts: 2, StartedAt: t0.Add(time.Minute)},
			"sidecar": {Restarts: 1, StartedAt: t0},
			"gone":    nil,
		}})
		if got.Restarts != 3 {
			t.Errorf("Restarts = %d, want 3", got.Restarts)
		}
		if got.StartedAt == nil || !got.StartedAt.Equal(t0) {
			t.Errorf("StartedAt = %v, want %v", got.StartedAt, t0)
		}
	})

	t.Run("LastRestart is the newest restart of any task", func(t *testing.T) {
		got := copyOf(&api.AllocationListStub{ClientStatus: "running", TaskGroup: "web", TaskStates: map[string]*api.TaskState{
			"web":     {Restarts: 2, LastRestart: t0.Add(2 * time.Minute)},
			"sidecar": {Restarts: 1, LastRestart: t0.Add(time.Minute)},
			"never":   {},
			"gone":    nil,
		}})
		if got.LastRestart == nil || !got.LastRestart.Equal(t0.Add(2*time.Minute)) {
			t.Errorf("LastRestart = %v, want %v", got.LastRestart, t0.Add(2*time.Minute))
		}
		if got := copyOf(&api.AllocationListStub{TaskGroup: "web", TaskStates: map[string]*api.TaskState{"web": {}}}); got.LastRestart != nil {
			t.Errorf("LastRestart = %v, want nil when nothing restarted", got.LastRestart)
		}
	})

	t.Run("FinishedAt is the newest end of any task", func(t *testing.T) {
		got := copyOf(&api.AllocationListStub{ClientStatus: "failed", TaskGroup: "web", TaskStates: map[string]*api.TaskState{
			"web":     {FinishedAt: t0.Add(2 * time.Minute)},
			"sidecar": {FinishedAt: t0.Add(time.Minute)},
			"never":   {},
			"gone":    nil,
		}})
		if got.FinishedAt == nil || !got.FinishedAt.Equal(t0.Add(2*time.Minute)) {
			t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, t0.Add(2*time.Minute))
		}
		if got := copyOf(&api.AllocationListStub{TaskGroup: "web", TaskStates: map[string]*api.TaskState{"web": {}}}); got.FinishedAt != nil {
			t.Errorf("FinishedAt = %v, want nil when nothing ended", got.FinishedAt)
		}
	})

	t.Run("OutOfMemory", func(t *testing.T) {
		terminated := func(oom string) *api.TaskEvent {
			return &api.TaskEvent{Type: api.TaskTerminated, Details: map[string]string{"oom_killed": oom, "exit_code": "137"}}
		}
		started := &api.TaskEvent{Type: api.TaskStarted}
		tests := []struct {
			name   string
			states map[string]*api.TaskState
			want   bool
		}{
			{"the service's newest terminated event is an OOM kill",
				map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{started, terminated("true"), started}}}, true},
			{"a terminated event that wasn't an OOM kill",
				map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{started, terminated("false")}}}, false},
			{"an older OOM kill, then an ordinary stop",
				map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{terminated("true"), started, terminated("false")}}}, false},
			{"a terminated event without details",
				map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{{Type: api.TaskTerminated}}}}, false},
			{"no events", map[string]*api.TaskState{"web": {}}, false},
			{"the names sidecar's OOM kill is not the service's",
				map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{started}}, "names": {Events: []*api.TaskEvent{terminated("true")}}}, false},
			{"no task named like the service", map[string]*api.TaskState{"names": {Events: []*api.TaskEvent{terminated("true")}}}, false},
			{"a nil event", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{nil}}}, false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := copyOf(&api.AllocationListStub{ClientStatus: "running", TaskGroup: "web", TaskStates: tc.states})
				if got.OutOfMemory != tc.want {
					t.Errorf("OutOfMemory = %v, want %v", got.OutOfMemory, tc.want)
				}
			})
		}
	})

	t.Run("StartedAt is nil while no task has started", func(t *testing.T) {
		got := copyOf(&api.AllocationListStub{ClientStatus: "pending", TaskStates: map[string]*api.TaskState{
			"web": {State: "pending"},
		}})
		if got.StartedAt != nil {
			t.Errorf("StartedAt = %v, want nil", got.StartedAt)
		}
	})
}

func TestCopies(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	started := func(m int) map[string]*api.TaskState {
		return map[string]*api.TaskState{"main": {StartedAt: t0.Add(time.Duration(m) * time.Minute)}}
	}

	t.Run("lists the job's copies, ordered, without old terminal ones", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("blog",
			api.AllocationListStub{ID: "old-done", TaskGroup: "web", JobVersion: 1, ClientStatus: "complete"},
			api.AllocationListStub{ID: "old-run", TaskGroup: "web", JobVersion: 1, ClientStatus: "running", TaskStates: started(1)},
			api.AllocationListStub{ID: "new-late", TaskGroup: "web", JobVersion: 2, ClientStatus: "running", TaskStates: started(5)},
			api.AllocationListStub{ID: "new-early", TaskGroup: "web", JobVersion: 2, ClientStatus: "running", TaskStates: started(3)},
			api.AllocationListStub{ID: "new-pending", TaskGroup: "web", JobVersion: 2, ClientStatus: "pending"},
			api.AllocationListStub{ID: "new-crashed", TaskGroup: "web", JobVersion: 2, ClientStatus: "failed"},
			api.AllocationListStub{ID: "db-1", TaskGroup: "db", JobVersion: 2, ClientStatus: "running", TaskStates: started(0)},
		)
		nomad.SetCopies("other", api.AllocationListStub{ID: "not-mine", TaskGroup: "web", ClientStatus: "running"})

		got, err := d.Copies(ctx, "blog")
		if err != nil {
			t.Fatalf("Copies: %v", err)
		}
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		// db before web; v2 before v1; by start time, unstarted last, then ID.
		want := []string{"db-1", "new-early", "new-late", "new-crashed", "new-pending", "old-run"}
		if !reflect.DeepEqual(ids, want) {
			t.Errorf("copies = %v, want %v", ids, want)
		}
	})

	t.Run("restart time and an OOM kill survive the trip through Nomad's API", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("blog", api.AllocationListStub{ID: "a1", TaskGroup: "web", ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {Restarts: 1, LastRestart: t0, Events: []*api.TaskEvent{
				{Type: api.TaskTerminated, Details: map[string]string{"oom_killed": "true"}},
			}}}})
		got, err := d.Copies(ctx, "blog")
		if err != nil || len(got) != 1 {
			t.Fatalf("Copies = %v, %v", got, err)
		}
		if !got[0].OutOfMemory || got[0].LastRestart == nil || !got[0].LastRestart.Equal(t0) {
			t.Errorf("copy = %+v, want OutOfMemory and LastRestart %v", got[0], t0)
		}
	})

	t.Run("a job without copies is an empty list", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		got, err := d.Copies(ctx, "blog")
		if err != nil || len(got) != 0 {
			t.Errorf("Copies = %v, %v; want none", got, err)
		}
	})

	t.Run("Nomad down is ErrNomadUnavailable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		if _, err := d.Copies(ctx, "blog"); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("Copies = %v, want ErrNomadUnavailable", err)
		}
		if _, err := d.AllCopies(ctx); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("AllCopies = %v, want ErrNomadUnavailable", err)
		}
	})

	t.Run("a refused token says so, not the token", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.RequireToken("s3cr3t")
		_, err := d.Copies(ctx, "blog")
		if !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("Copies = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestAllCopies(t *testing.T) {
	d, _, nomad := newTestDeployer(t)
	nomad.SetCopies("blog",
		api.AllocationListStub{ID: "b1", TaskGroup: "web", JobVersion: 2, ClientStatus: "running"},
		api.AllocationListStub{ID: "b0", TaskGroup: "web", JobVersion: 1, ClientStatus: "complete"})
	nomad.SetCopies("whoami", api.AllocationListStub{ID: "w1", TaskGroup: "web", ClientStatus: "pending"})

	before := nomad.AllocationRequests()
	got, err := d.AllCopies(context.Background())
	if err != nil {
		t.Fatalf("AllCopies: %v", err)
	}
	if n := nomad.AllocationRequests() - before; n != 1 {
		t.Errorf("AllCopies made %d requests, want 1", n)
	}
	if len(got) != 2 || len(got["blog"]) != 1 || got["blog"][0].ID != "b1" ||
		len(got["whoami"]) != 1 || got["whoami"][0].Status != CopyStarting {
		t.Errorf("AllCopies = %+v", got)
	}
}

func TestChangedJobs(t *testing.T) {
	mark := func(job string, i uint64) allocMark { return allocMark{jobID: job, modifyIndex: i} }
	before := map[string]allocMark{"a": mark("blog", 1), "b": mark("blog", 1), "c": mark("whoami", 1)}

	tests := []struct {
		name  string
		after map[string]allocMark
		want  []string
	}{
		{"nothing changed", map[string]allocMark{"a": mark("blog", 1), "b": mark("blog", 1), "c": mark("whoami", 1)}, []string{}},
		{"one changed", map[string]allocMark{"a": mark("blog", 1), "b": mark("blog", 1), "c": mark("whoami", 2)}, []string{"whoami"}},
		{"added", map[string]allocMark{"a": mark("blog", 1), "b": mark("blog", 1), "c": mark("whoami", 1), "d": mark("shop", 3)}, []string{"shop"}},
		{"removed", map[string]allocMark{"a": mark("blog", 1), "c": mark("whoami", 1)}, []string{"blog"}},
		{"deduplicated and sorted", map[string]allocMark{"a": mark("blog", 2), "b": mark("blog", 2), "x": mark("api", 2)}, []string{"api", "blog", "whoami"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := changedJobs(before, tc.after); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("changedJobs = %v, want %v", got, tc.want)
			}
		})
	}
}

// reports collects what WatchCopies calls back with.
type reports struct {
	mu   sync.Mutex
	list [][]string
}

func (r *reports) add(jobs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, jobs)
}

func (r *reports) snapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.list...)
}

// watchCopies starts WatchCopies and returns what it reports, a function that
// waits for the watcher's first answer, and one that stops it.
func watchCopies(t *testing.T, d *Deployer, requests func() int) (*reports, func()) {
	t.Helper()
	got := &reports{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.WatchCopies(ctx, got.add)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("WatchCopies did not return after its context ended")
		}
	}
	t.Cleanup(stop)
	// The first answer is the starting point: changes made before it are not
	// reported, so wait for it.
	waitUntil(t, "the first answer", func() bool { return requests() >= 1 })
	return got, stop
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWatchCopies(t *testing.T) {
	running := func(id string) api.AllocationListStub {
		return api.AllocationListStub{ID: id, TaskGroup: "web", ClientStatus: "running"}
	}
	yes, no := true, false

	t.Run("a new copy, a health change and a removed copy each report the job", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("blog", running("b1"))
		got, _ := watchCopies(t, d, nomad.AllocationRequests)

		waitReports := func(n int) [][]string {
			waitUntil(t, "a report", func() bool { return len(got.snapshot()) >= n })
			return got.snapshot()
		}

		nomad.SetCopies("blog", running("b1"), running("b2")) // added
		if r := waitReports(1); !reflect.DeepEqual(r[0], []string{"blog"}) {
			t.Fatalf("after adding a copy: %v", r)
		}

		h := running("b1")
		h.DeploymentStatus = &api.AllocDeploymentStatus{Healthy: &yes}
		nomad.SetCopies("blog", h, running("b2")) // changed
		if r := waitReports(2); !reflect.DeepEqual(r[1], []string{"blog"}) {
			t.Fatalf("after a health change: %v", r)
		}
		h.DeploymentStatus = &api.AllocDeploymentStatus{Healthy: &no}
		nomad.SetCopies("blog", h, running("b2"))
		waitReports(3)

		nomad.SetCopies("blog", h) // removed b2
		if r := waitReports(4); !reflect.DeepEqual(r[3], []string{"blog"}) {
			t.Fatalf("after removing a copy: %v", r)
		}
	})

	t.Run("several jobs at once are one sorted, distinct report", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		got, _ := watchCopies(t, d, nomad.AllocationRequests)
		nomad.SetCopies("whoami", running("w1"))
		nomad.SetCopies("blog", running("b1"), running("b2"))
		waitUntil(t, "both jobs reported", func() bool {
			seen := map[string]bool{}
			for _, r := range got.snapshot() {
				for _, j := range r {
					seen[j] = true
				}
			}
			return seen["blog"] && seen["whoami"]
		})
		for _, r := range got.snapshot() {
			if !slices.IsSorted(r) || len(r) != len(distinct(r)) {
				t.Errorf("report %v isn't sorted and distinct", r)
			}
		}
	})

	t.Run("the starting point is not a change", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("blog", running("b1"))
		got, stop := watchCopies(t, d, nomad.AllocationRequests)
		time.Sleep(50 * time.Millisecond)
		stop()
		if r := got.snapshot(); len(r) != 0 {
			t.Errorf("reports = %v, want none for allocations that were already there", r)
		}
	})

	t.Run("nothing changed, no call", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("blog", running("b1"))
		got, stop := watchCopies(t, d, nomad.AllocationRequests)
		nomad.SetCopies("blog", running("b1")) // the same again: Nomad's index doesn't move
		nomad.SetCopies("other")               // nothing there to remove
		time.Sleep(50 * time.Millisecond)
		stop()
		if r := got.snapshot(); len(r) != 0 {
			t.Errorf("reports = %v, want none", r)
		}
	})

	t.Run("Nomad down, then up, resumes", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("blog", running("b1"))
		got, _ := watchCopies(t, d, nomad.AllocationRequests)

		nomad.Down()
		nomad.SetCopies("blog", running("b1"), running("b2")) // changes while it can't hear
		time.Sleep(30 * time.Millisecond)
		nomad.Up()

		waitUntil(t, "a report after Nomad came back", func() bool { return len(got.snapshot()) >= 1 })
		if r := got.snapshot(); !reflect.DeepEqual(r[0], []string{"blog"}) {
			t.Errorf("reports = %v, want blog", r)
		}
		// And it keeps watching.
		nomad.SetCopies("blog", running("b1"))
		waitUntil(t, "a second report", func() bool { return len(got.snapshot()) >= 2 })
	})

	t.Run("cancelling the context returns, even while waiting on Nomad", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		_, stop := watchCopies(t, d, nomad.AllocationRequests)
		stop() // fails the test if WatchCopies doesn't return within 5 s
	})
}

func distinct(s []string) map[string]bool {
	m := map[string]bool{}
	for _, v := range s {
		m[v] = true
	}
	return m
}
