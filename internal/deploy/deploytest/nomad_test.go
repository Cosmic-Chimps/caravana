package deploytest

import (
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

func newClient(t *testing.T, n *Nomad) *api.Client {
	t.Helper()
	cfg := api.DefaultConfig()
	cfg.Address = n.Addr()
	c, err := api.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSetCopiesAndReads(t *testing.T) {
	n := New(t)
	c := newClient(t, n)
	n.SetCopies("blog",
		api.AllocationListStub{ClientStatus: "running", NodeName: "n1", TaskGroup: "web"},
		api.AllocationListStub{ClientStatus: "pending", TaskGroup: "web"})
	n.SetCopies("whoami", api.AllocationListStub{ID: "w1", ClientStatus: "running"})

	all, meta, err := c.Allocations().List(nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("List = %d stubs, %v; want 3", len(all), err)
	}
	if meta.LastIndex <= 1 {
		t.Errorf("LastIndex = %d, want it moved by SetCopies", meta.LastIndex)
	}
	byJob, _, err := c.Jobs().Allocations("blog", false, nil)
	if err != nil || len(byJob) != 2 {
		t.Fatalf("Jobs().Allocations = %d, %v; want 2", len(byJob), err)
	}
	if byJob[0].ID != "blog-alloc-0" || byJob[0].JobID != "blog" || byJob[0].Namespace != "default" {
		t.Errorf("filled stub = %+v", byJob[0])
	}
	full, _, err := c.Allocations().Info("blog-alloc-0", nil)
	if err != nil || full.NodeName != "n1" || full.ClientStatus != "running" || full.TaskGroup != "web" {
		t.Errorf("Info = %+v, %v", full, err)
	}
	if _, _, err := c.Allocations().Info("nope", nil); err == nil {
		t.Error("Info of an unknown allocation = nil error")
	}
}

func TestAllocationsBlock(t *testing.T) {
	n := New(t)
	c := newClient(t, n)
	n.SetCopies("blog", api.AllocationListStub{ClientStatus: "running"})
	_, meta, _ := c.Allocations().List(nil)

	t.Run("an older index answers at once", func(t *testing.T) {
		start := time.Now()
		_, m, err := c.Allocations().List(&api.QueryOptions{WaitIndex: meta.LastIndex - 1, WaitTime: 5 * time.Second})
		if err != nil || time.Since(start) > time.Second || m.LastIndex != meta.LastIndex {
			t.Errorf("answered after %v, index %d, %v", time.Since(start), m.LastIndex, err)
		}
	})

	t.Run("the current index waits for the wait time", func(t *testing.T) {
		start := time.Now()
		_, m, err := c.Allocations().List(&api.QueryOptions{WaitIndex: meta.LastIndex, WaitTime: 100 * time.Millisecond})
		if err != nil || time.Since(start) < 90*time.Millisecond || m.LastIndex != meta.LastIndex {
			t.Errorf("answered after %v, index %d, %v; want ~100ms and the same index", time.Since(start), m.LastIndex, err)
		}
	})

	t.Run("a change wakes it", func(t *testing.T) {
		go func() {
			time.Sleep(50 * time.Millisecond)
			n.SetCopies("blog", api.AllocationListStub{ClientStatus: "failed"})
		}()
		start := time.Now()
		got, m, err := c.Allocations().List(&api.QueryOptions{WaitIndex: meta.LastIndex, WaitTime: 5 * time.Second})
		if err != nil || time.Since(start) > 2*time.Second || m.LastIndex <= meta.LastIndex || got[0].ClientStatus != "failed" {
			t.Errorf("answered after %v, index %d, %v", time.Since(start), m.LastIndex, err)
		}
	})

	t.Run("setting the same copies again changes nothing", func(t *testing.T) {
		_, before, _ := c.Allocations().List(nil)
		n.SetCopies("blog", api.AllocationListStub{ClientStatus: "failed"})
		_, after, _ := c.Allocations().List(nil)
		if after.LastIndex != before.LastIndex {
			t.Errorf("index moved from %d to %d", before.LastIndex, after.LastIndex)
		}
	})

	t.Run("Down drops a waiting query", func(t *testing.T) {
		_, cur, _ := c.Allocations().List(nil)
		go func() {
			time.Sleep(50 * time.Millisecond)
			n.Down()
		}()
		start := time.Now()
		_, _, err := c.Allocations().List(&api.QueryOptions{WaitIndex: cur.LastIndex, WaitTime: 5 * time.Second})
		n.Up()
		if err == nil || time.Since(start) > 2*time.Second {
			t.Errorf("err = %v after %v, want a network error soon", err, time.Since(start))
		}
	})
}

// A scripted rollout state carries the counts of its task groups, so the
// code that reads rollout progress can be tested end to end.
func TestScriptedRolloutCarriesTaskGroups(t *testing.T) {
	n := New(t)
	c := newClient(t, n)
	job := &api.Job{ID: new("blog")}
	n.AddForeignJob(job)
	n.Script("blog", 0, Rolling("Deployment is running", map[string]api.DeploymentState{
		"web":    {DesiredTotal: 3, DesiredCanaries: 1, PlacedAllocs: 2, HealthyAllocs: 1, UnhealthyAllocs: 1, Promoted: true},
		"worker": {DesiredTotal: 1, PlacedAllocs: 1},
	}))

	deps, _, err := c.Jobs().Deployments("blog", false, nil)
	if err != nil || len(deps) != 1 {
		t.Fatalf("Deployments = %d, %v; want 1", len(deps), err)
	}
	d := deps[0]
	if d.Status != api.DeploymentStatusRunning || d.StatusDescription != "Deployment is running" {
		t.Errorf("status = %q %q, want running with the description", d.Status, d.StatusDescription)
	}
	web := d.TaskGroups["web"]
	if web == nil || web.DesiredTotal != 3 || web.DesiredCanaries != 1 || web.PlacedAllocs != 2 ||
		web.HealthyAllocs != 1 || web.UnhealthyAllocs != 1 || !web.Promoted {
		t.Errorf("web = %+v, want the scripted counts", web)
	}
	if worker := d.TaskGroups["worker"]; worker == nil || worker.DesiredTotal != 1 {
		t.Errorf("worker = %+v, want the scripted counts", worker)
	}
}

func TestLogsFrameStream(t *testing.T) {
	n := New(t)
	c := newClient(t, n)
	n.SetCopies("blog", api.AllocationListStub{ID: "c1", ClientStatus: "running", TaskGroup: "web"})
	n.WriteLog("c1", "stdout", "hello\nworld\n")
	n.WriteLog("c1", "stderr", "oops\n")
	alloc, _, err := c.Allocations().Info("c1", nil)
	if err != nil {
		t.Fatal(err)
	}

	read := func(follow bool, origin string, offset int64, stream string) (data string, frames []*api.StreamFrame) {
		t.Helper()
		cancel := make(chan struct{})
		defer close(cancel)
		ch, errCh := c.AllocFS().Logs(alloc, follow, "web", stream, origin, offset, cancel, nil)
		timeout := time.After(2 * time.Second)
		for {
			select {
			case f, ok := <-ch:
				if !ok {
					return data, frames
				}
				data += string(f.Data)
				frames = append(frames, f)
				if follow && len(data) >= 1 {
					return data, frames
				}
			case err := <-errCh:
				t.Fatalf("log stream: %v", err)
			case <-timeout:
				t.Fatalf("log stream still open; read %q", data)
			}
		}
	}

	t.Run("the whole output, ending at its end", func(t *testing.T) {
		data, frames := read(false, "start", 0, "stdout")
		if data != "hello\nworld\n" || len(frames) != 1 {
			t.Fatalf("data %q in %d frames, want everything in one", data, len(frames))
		}
		if f := frames[0]; f.Offset != 12 || f.File != "alloc/logs/web.stdout.0" {
			t.Errorf("frame = offset %d file %q, want the offset after the data and the file's name", f.Offset, f.File)
		}
	})
	t.Run("origin end counts back from the end", func(t *testing.T) {
		if data, _ := read(false, "end", 6, "stdout"); data != "world\n" {
			t.Errorf("data = %q, want the last 6 bytes", data)
		}
		if data, _ := read(false, "end", 1000, "stdout"); data != "hello\nworld\n" {
			t.Errorf("data = %q, want all of it when the offset passes the start", data)
		}
	})
	t.Run("the streams are separate", func(t *testing.T) {
		if data, _ := read(false, "start", 0, "stderr"); data != "oops\n" {
			t.Errorf("stderr = %q", data)
		}
	})
	t.Run("a frame size splits the data", func(t *testing.T) {
		n.SetLogFrameSize(5)
		defer n.SetLogFrameSize(0)
		_, frames := read(false, "start", 0, "stdout")
		if len(frames) != 3 || frames[0].Offset != 5 || frames[2].Offset != 12 {
			t.Errorf("frames = %d, first offset %d; want 3 frames ending at 12", len(frames), frames[0].Offset)
		}
	})
	t.Run("following sends what is written next, and counts the open streams", func(t *testing.T) {
		cancel := make(chan struct{})
		ch, _ := c.AllocFS().Logs(alloc, true, "web", "stdout", "end", 0, cancel, nil)
		deadline := time.Now().Add(2 * time.Second)
		for n.OpenLogStreams() != 1 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		n.WriteLog("c1", "stdout", "later\n")
		select {
		case f := <-ch:
			if string(f.Data) != "later\n" {
				t.Errorf("frame data = %q, want only the new output", f.Data)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no frame after a write")
		}
		close(cancel)
	})
	t.Run("the requests are recorded", func(t *testing.T) {
		reqs := n.LogRequests()
		if len(reqs) == 0 || reqs[0] != (LogRequest{Copy: "c1", Task: "web", Stream: "stdout", Origin: "start"}) {
			t.Errorf("first request = %+v", reqs)
		}
	})
	t.Run("an unknown copy is a 404", func(t *testing.T) {
		_, errCh := c.AllocFS().Logs(&api.Allocation{ID: "nope"}, false, "web", "stdout", "end", 0, make(chan struct{}), nil)
		if err := <-errCh; err == nil {
			t.Error("no error for an unknown copy")
		}
	})
}
