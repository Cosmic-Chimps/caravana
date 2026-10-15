package deploytest

import (
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

func TestAllocFiles(t *testing.T) {
	n := New(t)
	c := newClient(t, n)
	n.SetCopies("blog", api.AllocationListStub{ID: "c1", ClientStatus: "running", TaskGroup: "web"})
	alloc, _, err := c.Allocations().Info("c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	cat := func(path string) (string, error) {
		r, err := c.AllocFS().Cat(alloc, path, nil)
		if err != nil {
			return "", err
		}
		defer r.Close()
		b, err := io.ReadAll(r)
		return string(b), err
	}

	if _, err := cat("alloc/data/caravana/sizes"); err == nil {
		t.Error("Cat of a file that isn't there = nil, want an error")
	}
	n.WriteAllocFile("c1", "/alloc/data/caravana/sizes", "data 5\n")
	if got, err := cat("alloc/data/caravana/sizes"); err != nil || got != "data 5\n" {
		t.Errorf("Cat = %q, %v; a leading slash in the write must not matter", got, err)
	}
	if text, ok := n.AllocFile("c1", "alloc/data/caravana/sizes"); !ok || text != "data 5\n" {
		t.Errorf("AllocFile = %q, %v", text, ok)
	}
	n.RemoveAllocFile("c1", "alloc/data/caravana/sizes")
	if _, ok := n.AllocFile("c1", "alloc/data/caravana/sizes"); ok {
		t.Error("the file is still there after RemoveAllocFile")
	}

	n.WriteAllocFile("c1", "x", "y")
	n.FailFS(403)
	if _, err := cat("x"); err == nil {
		t.Error("Cat with FailFS(403) = nil, want an error")
	}
	n.FailFS(0)

	gone := &api.Allocation{ID: "nope"}
	if _, err := c.AllocFS().Cat(gone, "x", nil); err == nil {
		t.Error("Cat of an unknown copy = nil, want an error")
	}
}

func TestRestart(t *testing.T) {
	n := New(t)
	c := newClient(t, n)
	n.SetCopies("blog", api.AllocationListStub{ID: "c1", ClientStatus: "running", TaskGroup: "web",
		TaskStates: map[string]*api.TaskState{"web": {}, "caravana-volumes": {}}})
	alloc := &api.Allocation{ID: "c1"}

	var hooked []string
	n.OnRestart(func(id string) { hooked = append(hooked, id) })
	n.SetRestartStop(7 * time.Second)

	before := time.Now()
	if err := c.Allocations().RestartAllTasks(alloc, nil); err != nil {
		t.Fatalf("RestartAllTasks: %v", err)
	}
	if got := n.Restarts(); len(got) != 1 || got[0] != (RestartRequest{Copy: "c1", AllTasks: true}) {
		t.Errorf("Restarts = %+v", got)
	}
	if !slices.Equal(hooked, []string{"c1"}) {
		t.Errorf("hook calls = %v, want one for c1", hooked)
	}

	full, _, err := c.Allocations().Info("c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, ts := range full.TaskStates {
		if len(ts.Events) == 0 || ts.Events[0].Type != api.TaskRestartSignal ||
			!strings.Contains(ts.Events[0].DisplayMessage, "User requested all tasks to restart") {
			t.Errorf("task %s events = %+v, want the restart signal first", name, ts.Events)
		}
	}
	web := full.TaskStates["web"].Events
	if len(web) != 3 || web[1].Type != api.TaskTerminated || web[2].Type != api.TaskStarted {
		t.Fatalf("service task events = %+v, want signal, terminated, started", web)
	}
	if gap := time.Duration(web[2].Time - web[1].Time); gap != 7*time.Second {
		t.Errorf("the service stayed stopped for %v, want 7s", gap)
	}
	if len(full.TaskStates["caravana-volumes"].Events) != 1 {
		t.Error("only the service's own task stops and starts")
	}
	if time.Unix(0, web[1].Time).Before(before.Add(-time.Second)) {
		t.Error("the events are not dated now")
	}

	t.Run("a copy without task states gets the service's", func(t *testing.T) {
		n.SetCopies("shop", api.AllocationListStub{ID: "c2", ClientStatus: "running", TaskGroup: "db"})
		if err := c.Allocations().RestartAllTasks(&api.Allocation{ID: "c2"}, nil); err != nil {
			t.Fatal(err)
		}
		a, _, _ := c.Allocations().Info("c2", nil)
		if len(a.TaskStates["db"].Events) != 3 {
			t.Errorf("events = %+v", a.TaskStates["db"].Events)
		}
	})
	t.Run("the hook may block, and the request waits", func(t *testing.T) {
		release := make(chan struct{})
		n.OnRestart(func(string) { <-release })
		done := make(chan error, 1)
		go func() { done <- c.Allocations().RestartAllTasks(alloc, nil) }()
		select {
		case <-done:
			t.Fatal("the restart answered while the hook was blocked")
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		if err := <-done; err != nil {
			t.Errorf("RestartAllTasks after release: %v", err)
		}
	})
	t.Run("errors", func(t *testing.T) {
		n.OnRestart(nil)
		if err := c.Allocations().RestartAllTasks(&api.Allocation{ID: "nope"}, nil); err == nil {
			t.Error("restart of an unknown copy = nil")
		}
		n.FailRestart(403)
		if err := c.Allocations().RestartAllTasks(alloc, nil); err == nil {
			t.Error("restart with FailRestart(403) = nil")
		}
	})
}

func TestAddHostVolume(t *testing.T) {
	n := New(t)
	c := newClient(t, n)
	path := n.AddHostVolume("kuma_data")
	if path == "" || n.AddHostVolume("kuma_data") != path {
		t.Fatalf("AddHostVolume = %q, and again %q; want the same path", path, n.AddHostVolume("kuma_data"))
	}
	if got := n.HostVolumes(); !slices.Equal(got, []string{"kuma_data"}) {
		t.Errorf("HostVolumes = %v", got)
	}
	stubs, _, err := c.HostVolumes().List(&api.HostVolumeListRequest{}, &api.QueryOptions{Prefix: "kuma_"})
	if err != nil || len(stubs) != 1 {
		t.Fatalf("List = %v, %v", stubs, err)
	}
	v, _, err := c.HostVolumes().Get(stubs[0].ID, nil)
	if err != nil || v.HostPath != path || v.State != api.HostVolumeStateReady {
		t.Errorf("Get = %+v, %v; want the path %q, ready on the first read", v, err, path)
	}
}
