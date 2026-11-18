package deploy

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

func TestBatchOf(t *testing.T) {
	alloc := func(job string) api.Event {
		return api.Event{Topic: api.TopicAllocation, Key: "a-" + job,
			Payload: map[string]any{"Allocation": map[string]any{"ID": "a-" + job, "JobID": job}}}
	}
	deployment := func(job, typ, status string) api.Event {
		return api.Event{Topic: api.TopicDeployment, Type: typ, Key: "d-" + job,
			Payload: map[string]any{"Deployment": map[string]any{"ID": "d-" + job, "JobID": job, "Status": status}}}
	}
	tests := []struct {
		name   string
		events []api.Event
		want   EventBatch
	}{
		{"nothing", nil, EventBatch{}},
		{"allocations are sorted and distinct", []api.Event{alloc("whoami"), alloc("blog"), alloc("whoami")},
			EventBatch{Copies: []string{"blog", "whoami"}}},
		{"a rollout's status moves it", []api.Event{deployment("blog", "DeploymentStatusUpdate", "running")},
			EventBatch{Deployments: []string{"blog"}, Rollouts: []string{"blog"}}},
		{"a new rollout's plan moves it", []api.Event{deployment("blog", "PlanResult", "running")},
			EventBatch{Deployments: []string{"blog"}, Rollouts: []string{"blog"}}},
		{"one more healthy copy is only progress", []api.Event{deployment("blog", "AllocationUpdated", "running")},
			EventBatch{Deployments: []string{"blog"}}},
		{"unless it ends the rollout", []api.Event{deployment("blog", "AllocationUpdated", "successful")},
			EventBatch{Deployments: []string{"blog"}, Rollouts: []string{"blog"}}},
		{"a job's ID is the event's key", []api.Event{{Topic: api.TopicJob, Key: "blog_run-1"}},
			EventBatch{Jobs: []string{"blog_run-1"}}},
		{"or the payload's when the key is empty", []api.Event{{Topic: api.TopicJob,
			Payload: map[string]any{"Job": map[string]any{"ID": "blog"}}}}, EventBatch{Jobs: []string{"blog"}}},
		{"a periodic child keeps its whole ID", []api.Event{{Topic: api.TopicJob, Key: "blog_db_backup/periodic-123"}},
			EventBatch{Jobs: []string{"blog_db_backup/periodic-123"}}},
		{"a node", []api.Event{{Topic: api.TopicNode, Key: "n1"}}, EventBatch{Nodes: true}},
		{"an unreadable ID is skipped", []api.Event{
			{Topic: api.TopicAllocation, Payload: map[string]any{"Allocation": "not an object"}},
			{Topic: api.TopicAllocation, Payload: map[string]any{"Allocation": map[string]any{"JobID": 7}}},
			{Topic: api.TopicDeployment, Payload: nil},
			{Topic: api.TopicJob}},
			EventBatch{}},
		{"other topics are ignored", []api.Event{{Topic: api.TopicEvaluation, Key: "e1"}}, EventBatch{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := batchOf(tc.events); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("batchOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMergeBatches(t *testing.T) {
	a := EventBatch{Copies: []string{"blog", "whoami"}, Jobs: []string{"x"}}
	b := EventBatch{Copies: []string{"api", "blog"}, Deployments: []string{"blog"}, Rollouts: []string{"blog"}, Nodes: true}
	want := EventBatch{Copies: []string{"api", "blog", "whoami"}, Deployments: []string{"blog"}, Rollouts: []string{"blog"}, Jobs: []string{"x"}, Nodes: true}
	if got := merge(a, b); !reflect.DeepEqual(got, want) {
		t.Errorf("merge = %+v, want %+v", got, want)
	}
	if got := merge(EventBatch{}, EventBatch{Resync: true}); !got.Resync || got.Empty() {
		t.Errorf("merge keeps Resync: %+v", got)
	}
	if !(EventBatch{}).Empty() || (EventBatch{Nodes: true}).Empty() {
		t.Error("Empty is wrong")
	}
}

// followed collects what FollowEvents hands out.
type followed struct {
	mu      sync.Mutex
	batches []EventBatch
	errs    []error
}

func (f *followed) handle(b EventBatch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, b)
}

func (f *followed) failed(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs = append(f.errs, err)
}

func (f *followed) snapshot() ([]EventBatch, []error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.batches), slices.Clone(f.errs)
}

// resyncs counts the batches that say to look at everything.
func (f *followed) resyncs() int {
	batches, _ := f.snapshot()
	n := 0
	for _, b := range batches {
		if b.Resync {
			n++
		}
	}
	return n
}

// reported is every job ID the batches named as having changed copies, in order.
func (f *followed) reported() []string {
	batches, _ := f.snapshot()
	var out []string
	for _, b := range batches {
		out = append(out, b.Copies...)
	}
	return out
}

// follow starts FollowEvents on d and returns what it hands out and a stop
// function that fails the test if it doesn't return.
func follow(t *testing.T, d *Deployer) (*followed, func()) {
	t.Helper()
	got := &followed{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.FollowEvents(ctx, got.handle, got.failed)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("FollowEvents did not return after its context ended")
		}
	}
	t.Cleanup(stop)
	return got, stop
}

func aRunningCopy(id string) api.AllocationListStub {
	return api.AllocationListStub{ID: id, TaskGroup: "web", ClientStatus: "running"}
}

func TestFollowEvents(t *testing.T) {
	t.Run("the first connect is a Resync, then changes arrive by topic", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		got, _ := follow(t, d)
		waitUntil(t, "the first Resync", func() bool { return got.resyncs() == 1 })

		nomad.SetCopies("blog", aRunningCopy("b1"))
		nomad.Script("blog", 0, state(api.DeploymentStatusRunning, ""))
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.AddForeignJob(&api.Job{ID: new("blog_run-1"), Name: new("blog_run-1")})

		waitUntil(t, "every kind of change", func() bool {
			batches, _ := got.snapshot()
			var copies, deployments, jobs, nodes bool
			for _, b := range batches {
				copies = copies || slices.Contains(b.Copies, "blog")
				deployments = deployments || slices.Contains(b.Deployments, "blog")
				jobs = jobs || slices.Contains(b.Jobs, "blog_run-1")
				nodes = nodes || b.Nodes
			}
			return copies && deployments && jobs && nodes
		})
		if n := got.resyncs(); n != 1 {
			t.Errorf("Resync batches = %d, want 1: nothing was missed", n)
		}
	})

	t.Run("changes made together come out as fewer batches than events", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		got, _ := follow(t, d)
		waitUntil(t, "the first Resync", func() bool { return got.resyncs() == 1 })
		for i := range 30 {
			nomad.SetCopies("blog", aRunningCopy("b1"), aRunningCopy("b"+string(rune('2'+i%5))))
		}
		waitUntil(t, "the changes", func() bool { return slices.Contains(got.reported(), "blog") })
		batches, _ := got.snapshot()
		for _, b := range batches {
			if !slices.IsSorted(b.Copies) || len(slices.Compact(slices.Clone(b.Copies))) != len(b.Copies) {
				t.Errorf("batch %+v isn't sorted and distinct", b)
			}
		}
	})

	t.Run("a lost stream is reported, reconnected, resynced and loses nothing", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		got, _ := follow(t, d)
		waitUntil(t, "the first Resync", func() bool { return got.resyncs() == 1 })

		nomad.SetCopies("before", aRunningCopy("x1"))
		waitUntil(t, "the first change", func() bool { return slices.Contains(got.reported(), "before") })

		nomad.Down()
		waitUntil(t, "the failure to be reported", func() bool { _, errs := got.snapshot(); return len(errs) > 0 })
		nomad.SetCopies("while-down", aRunningCopy("y1")) // happens while nobody can hear
		nomad.Up()

		waitUntil(t, "the second Resync", func() bool { return got.resyncs() == 2 })
		waitUntil(t, "the change made while down", func() bool { return slices.Contains(got.reported(), "while-down") })
		nomad.SetCopies("after", aRunningCopy("z1"))
		waitUntil(t, "the change after", func() bool { return slices.Contains(got.reported(), "after") })

		// No duplicates: what was handed out before the loss isn't sent again.
		counts := map[string]int{}
		for _, id := range got.reported() {
			counts[id]++
		}
		for id, n := range counts {
			if n != 1 {
				t.Errorf("%s reported %d times, want once: %v", id, n, got.reported())
			}
		}
	})

	t.Run("a connection renewed at its lifetime resumes without a Resync", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		d.Timing.StreamLifetime = 40 * time.Millisecond
		got, _ := follow(t, d)
		waitUntil(t, "three connections", func() bool { return nomad.EventStreamRequests() >= 3 })

		nomad.SetCopies("blog", aRunningCopy("b1"))
		waitUntil(t, "the change", func() bool { return slices.Contains(got.reported(), "blog") })
		waitUntil(t, "another renewal", func() bool { return nomad.EventStreamRequests() >= 5 })
		nomad.SetCopies("blog", aRunningCopy("b1"), aRunningCopy("b2"))
		waitUntil(t, "the second change", func() bool { return len(got.reported()) >= 2 })

		if n := got.resyncs(); n != 1 {
			t.Errorf("Resync batches = %d, want 1 (the first connect only)", n)
		}
		if _, errs := got.snapshot(); len(errs) != 0 {
			t.Errorf("failed called for a planned renewal: %v", errs)
		}
		if r := got.reported(); !reflect.DeepEqual(r, []string{"blog", "blog"}) {
			t.Errorf("reported = %v, want blog once for each change", r)
		}
	})

	t.Run("a refused token is reported and retried", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.RequireToken("s3cr3t")
		got, _ := follow(t, d)
		waitUntil(t, "two failures", func() bool { _, errs := got.snapshot(); return len(errs) >= 2 })
		if got.resyncs() != 0 {
			t.Error("a Resync was handed out without a connection")
		}
	})

	t.Run("cancelling the context returns", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		_, stop := follow(t, d)
		waitUntil(t, "a connection", func() bool { return nomad.EventStreamRequests() >= 1 })
		stop() // fails the test if FollowEvents doesn't return within 5 s
	})
}
