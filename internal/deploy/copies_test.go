package deploy

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// A copy Nomad has told to stop may still say it is running; Stopping says so.
func TestCopyOfStopping(t *testing.T) {
	for desired, want := range map[string]bool{"": false, "run": false, "stop": true, "evict": true} {
		got := plainCopyOf(&api.AllocationListStub{ClientStatus: "running", DesiredStatus: desired})
		if got.Stopping != want || got.Status != CopyHealthy {
			t.Errorf("desired %q: stopping = %v, status = %s; want %v, healthy", desired, got.Stopping, got.Status, want)
		}
	}
}

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
			got := plainCopyOf(&tc.stub)
			if got.Status != tc.want || got.Terminal != tc.terminal {
				t.Errorf("status = %s, terminal = %v; want %s, %v", got.Status, got.Terminal, tc.want, tc.terminal)
			}
			if got.ID != "a1" || got.Service != "web" || got.Node != "node-1" || got.Version != 7 ||
				got.Description != "Tasks are running" {
				t.Errorf("copy = %+v", got)
			}
		})
	}

	t.Run("restarts are summed over tasks and StartedAt is the earliest task start", func(t *testing.T) {
		got := plainCopyOf(&api.AllocationListStub{ClientStatus: "running", TaskStates: map[string]*api.TaskState{
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

	t.Run("CreatedAt is when Nomad made the copy, nil when it didn't say", func(t *testing.T) {
		got := plainCopyOf(&api.AllocationListStub{ClientStatus: "running", CreateTime: t0.UnixNano(),
			TaskStates: map[string]*api.TaskState{"web": {StartedAt: t0.Add(time.Hour)}}})
		if got.CreatedAt == nil || !got.CreatedAt.Equal(t0) {
			t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, t0)
		}
		if got := plainCopyOf(&api.AllocationListStub{ClientStatus: "running"}); got.CreatedAt != nil {
			t.Errorf("CreatedAt = %v, want nil without a CreateTime", got.CreatedAt)
		}
	})

	t.Run("LastRestart is the newest restart of any task", func(t *testing.T) {
		got := plainCopyOf(&api.AllocationListStub{ClientStatus: "running", TaskGroup: "web", TaskStates: map[string]*api.TaskState{
			"web":     {Restarts: 2, LastRestart: t0.Add(2 * time.Minute)},
			"sidecar": {Restarts: 1, LastRestart: t0.Add(time.Minute)},
			"never":   {},
			"gone":    nil,
		}})
		if got.LastRestart == nil || !got.LastRestart.Equal(t0.Add(2*time.Minute)) {
			t.Errorf("LastRestart = %v, want %v", got.LastRestart, t0.Add(2*time.Minute))
		}
		if got := plainCopyOf(&api.AllocationListStub{TaskGroup: "web", TaskStates: map[string]*api.TaskState{"web": {}}}); got.LastRestart != nil {
			t.Errorf("LastRestart = %v, want nil when nothing restarted", got.LastRestart)
		}
	})

	t.Run("FinishedAt is the newest end of any task", func(t *testing.T) {
		got := plainCopyOf(&api.AllocationListStub{ClientStatus: "failed", TaskGroup: "web", TaskStates: map[string]*api.TaskState{
			"web":     {FinishedAt: t0.Add(2 * time.Minute)},
			"sidecar": {FinishedAt: t0.Add(time.Minute)},
			"never":   {},
			"gone":    nil,
		}})
		if got.FinishedAt == nil || !got.FinishedAt.Equal(t0.Add(2*time.Minute)) {
			t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, t0.Add(2*time.Minute))
		}
		if got := plainCopyOf(&api.AllocationListStub{TaskGroup: "web", TaskStates: map[string]*api.TaskState{"web": {}}}); got.FinishedAt != nil {
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
				got := plainCopyOf(&api.AllocationListStub{ClientStatus: "running", TaskGroup: "web", TaskStates: tc.states})
				if got.OutOfMemory != tc.want {
					t.Errorf("OutOfMemory = %v, want %v", got.OutOfMemory, tc.want)
				}
			})
		}
	})

	t.Run("RequestedRestarts counts the restarts someone asked for, over all tasks", func(t *testing.T) {
		asked := &api.TaskEvent{Type: api.TaskRestartSignal, DisplayMessage: "User requested all tasks to restart"}
		askedViaReason := &api.TaskEvent{Type: api.TaskRestartSignal, RestartReason: "User requested all tasks to restart"}
		oneTask := &api.TaskEvent{Type: api.TaskRestartSignal, DisplayMessage: "Task signaled to restart"}
		crash := &api.TaskEvent{Type: api.TaskRestarting, DisplayMessage: "Task restarting in 5s"}
		tests := []struct {
			name   string
			states map[string]*api.TaskState
			want   int
		}{
			{"none", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{{Type: api.TaskStarted}}}}, 0},
			{"one restart of three tasks signals each", map[string]*api.TaskState{
				"web": {Events: []*api.TaskEvent{asked}}, "names": {Events: []*api.TaskEvent{asked}}, "caravana-volumes": {Events: []*api.TaskEvent{asked}}}, 3},
			{"two restarts of one task", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{asked, {Type: api.TaskStarted}, asked}}}, 2},
			{"the reason field counts too", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{askedViaReason}}}, 1},
			{"a signal for one task isn't a request for all", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{oneTask}}}, 0},
			{"a crash restart isn't one", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{crash}}}, 0},
			{"a nil event and a nil state", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{nil, asked}}, "gone": nil}, 1},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := plainCopyOf(&api.AllocationListStub{ClientStatus: "running", TaskGroup: "web", TaskStates: tc.states})
				if got.RequestedRestarts != tc.want {
					t.Errorf("RequestedRestarts = %d, want %d", got.RequestedRestarts, tc.want)
				}
			})
		}
	})

	t.Run("StartedAt is nil while no task has started", func(t *testing.T) {
		got := plainCopyOf(&api.AllocationListStub{ClientStatus: "pending", TaskStates: map[string]*api.TaskState{
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

// plainCopyOf is copyOf with no rollout known: what the status rules say from the stub alone.
func plainCopyOf(a *api.AllocationListStub) Copy { return copyOf(a, nil) }

// A copy says how its service's task last ended (feature 027's server page): the exit status of the
// newest Terminated event, or that a start failure came after it. Never Nomad's text.
func TestCopyLastEnd(t *testing.T) {
	terminated := func(code string) *api.TaskEvent {
		return &api.TaskEvent{Type: api.TaskTerminated, Details: map[string]string{"exit_code": code}}
	}
	started := &api.TaskEvent{Type: api.TaskStarted}
	driver := &api.TaskEvent{Type: api.TaskDriverFailure, DisplayMessage: "Failed to pull `secret/image`"}
	setup := &api.TaskEvent{Type: api.TaskSetupFailure}
	tests := []struct {
		name     string
		states   map[string]*api.TaskState
		wantExit *int
		wantFail bool
	}{
		{"the newest exit", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{terminated("0"), started, terminated("3")}}}, new(3), false},
		{"exit 0 is an exit", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{started, terminated("0")}}}, new(0), false},
		{"a driver failure after the exit", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{terminated("1"), driver}}}, nil, true},
		{"a setup failure and nothing else", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{setup}}}, nil, true},
		{"an exit after an old start failure", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{driver, started, terminated("2")}}}, new(2), false},
		{"an exit without a readable code", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{terminated("")}}}, nil, false},
		{"no events", map[string]*api.TaskState{"web": {}}, nil, false},
		{"only the sidecar ended", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{started}}, "names": {Events: []*api.TaskEvent{terminated("9")}}}, nil, false},
		{"a nil event", map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{nil, terminated("4")}}}, new(4), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := plainCopyOf(&api.AllocationListStub{ClientStatus: "failed", TaskGroup: "web", TaskStates: tc.states})
			if (got.ExitCode == nil) != (tc.wantExit == nil) || (got.ExitCode != nil && *got.ExitCode != *tc.wantExit) || got.StartFailed != tc.wantFail {
				t.Errorf("ExitCode = %v, StartFailed = %v; want %v, %v", got.ExitCode, got.StartFailed, tc.wantExit, tc.wantFail)
			}
		})
	}
}
