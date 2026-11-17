package deploy

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

func movableCopy(id, node string, createIndex uint64) api.AllocationListStub {
	return api.AllocationListStub{
		ID: id, NodeID: node, NodeName: node, TaskGroup: "web", Name: "whoami.web[0]",
		DesiredStatus: "run", ClientStatus: "running", CreateIndex: createIndex,
		TaskStates: map[string]*api.TaskState{"web": {State: "running"}},
	}
}

func TestMoveCopy(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	nomad.SetCopies("whoami", movableCopy("c1", "n2", 1))

	if err := d.MoveCopy(ctx, "c1"); err != nil {
		t.Fatalf("MoveCopy: %v", err)
	}
	if got := nomad.StoppedCopies(); !slices.Equal(got, []string{"c1"}) {
		t.Errorf("stopped = %v", got)
	}
	if err := d.MoveCopy(ctx, "ghost"); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("unknown copy: %v, want ErrCopyNotFound", err)
	}
	nomad.FailAllocStops(403)
	if err := d.MoveCopy(ctx, "c1"); err == nil || !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("refused: %v, want the capability error", err)
	}
}

func TestWaitCopyMoved(t *testing.T) {
	ctx := context.Background()

	t.Run("returns the replacement, not the group's other copies", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		// Two copies of the group: one on n1 (healthy, older), the one on n2 that moves.
		nomad.SetCopies("whoami", movableCopy("keep", "n1", 1), movableCopy("c1", "n2", 2))
		nomad.MoveCopiesTo("n3", "three")
		nomad.SetStartDelay(30 * time.Millisecond)
		if err := d.MoveCopy(ctx, "c1"); err != nil {
			t.Fatal(err)
		}
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		start := time.Now()
		id, err := d.WaitCopyMoved(wctx, "whoami", "web", "c1", "n2", false)
		if err != nil {
			t.Fatalf("WaitCopyMoved: %v", err)
		}
		if id == "keep" || id == "c1" || id == "" {
			t.Errorf("replacement = %q, want the new copy", id)
		}
		if time.Since(start) < 30*time.Millisecond {
			t.Errorf("returned after %s, before the replacement ran", time.Since(start))
		}
	})

	t.Run("a replacement that is still pending is waited for until the deadline", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("whoami", movableCopy("c1", "n2", 1))
		// No MoveCopiesTo: nowhere to go, the replacement stays pending.
		if err := d.MoveCopy(ctx, "c1"); err != nil {
			t.Fatal(err)
		}
		wctx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
		defer cancel()
		if _, err := d.WaitCopyMoved(wctx, "whoami", "web", "c1", "n2", false); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want the context's deadline", err)
		}
	})

	t.Run("with checked, waits until the checks pass", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("whoami", movableCopy("c1", "n2", 1))
		nomad.MoveCopiesTo("n3", "three")
		if err := d.MoveCopy(ctx, "c1"); err != nil {
			t.Fatal(err)
		}
		repl := "c1-moved-1"
		nomad.SetChecks(repl, api.AllocCheckStatuses{"x": {Status: "pending"}})
		go func() {
			time.Sleep(40 * time.Millisecond)
			nomad.SetChecks(repl, api.AllocCheckStatuses{"x": {Status: "success"}})
		}()
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		start := time.Now()
		id, err := d.WaitCopyMoved(wctx, "whoami", "web", "c1", "n2", true)
		if err != nil || id != repl {
			t.Fatalf("WaitCopyMoved = %q, %v, want %q", id, err, repl)
		}
		if time.Since(start) < 40*time.Millisecond {
			t.Errorf("returned before the checks passed (%s)", time.Since(start))
		}
	})
}
