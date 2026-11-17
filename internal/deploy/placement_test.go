package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// placed is whoami with a placement on its one service.
func placed(p app.Placement) app.App {
	a := whoami()
	a.Services[0].Placement = &p
	return a
}

func TestSetNodeLabels(t *testing.T) {
	ctx := context.Background()

	t.Run("writes the name and the labels and reads them back", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		if err := d.SetNodeLabels(ctx, "n2", "two", map[string]string{"ssd": "true", "region": "eu"}); err != nil {
			t.Fatalf("SetNodeLabels: %v", err)
		}
		meta := nomad.NodeMeta("n2")
		if meta["caravana.server"] != "two" || meta["caravana.label.ssd"] != "true" || meta["caravana.label.region"] != "eu" {
			t.Errorf("node meta = %v", meta)
		}
		server, labels, err := d.NodeLabels(ctx, "n2")
		if err != nil || server != "two" || len(labels) != 2 || labels["ssd"] != "true" {
			t.Errorf("NodeLabels = %q, %v, %v", server, labels, err)
		}
	})

	t.Run("unsets labels that are gone in one write, and leaves other metadata alone", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetNodeStaticMeta("n2", map[string]string{"caravana_ingress": "true"})
		nomad.SetNodeMeta("n2", map[string]string{"caravana.server": "two", "caravana.label.a": "1", "caravana.label.b": "2", "other": "x"})
		if err := d.SetNodeLabels(ctx, "n2", "two", map[string]string{"c": "3"}); err != nil {
			t.Fatalf("SetNodeLabels: %v", err)
		}
		want := map[string]string{"caravana.server": "two", "caravana.label.c": "3", "other": "x", "caravana_ingress": "true"}
		got := nomad.NodeMeta("n2")
		if len(got) != len(want) {
			t.Fatalf("meta = %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("meta[%s] = %q, want %q", k, got[k], v)
			}
		}
		if nomad.NodeMetaWrites() != 1 {
			t.Errorf("writes = %d, want 1 (both labels unset in one write)", nomad.NodeMetaWrites())
		}
	})

	t.Run("writes nothing when the node already has them", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetNodeMeta("n2", map[string]string{"caravana.server": "two", "caravana.label.ssd": "true"})
		if err := d.SetNodeLabels(ctx, "n2", "two", map[string]string{"ssd": "true"}); err != nil {
			t.Fatal(err)
		}
		if nomad.NodeMetaWrites() != 0 {
			t.Errorf("writes = %d, want 0", nomad.NodeMetaWrites())
		}
	})

	t.Run("waits for Nomad's servers to show the write", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetNodeMetaDelay(40 * time.Millisecond)
		start := time.Now()
		if err := d.SetNodeLabels(ctx, "n2", "two", map[string]string{"ssd": "true"}); err != nil {
			t.Fatalf("SetNodeLabels: %v", err)
		}
		if time.Since(start) < 40*time.Millisecond {
			t.Errorf("answered after %s, before the servers showed the labels", time.Since(start))
		}
		if _, labels, _ := d.NodeLabels(ctx, "n2"); labels["ssd"] != "true" {
			t.Errorf("NodeLabels after the wait = %v", labels)
		}
	})

	t.Run("a lag past the wait is pending, not a failure of the write", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		d.Timing.LabelWait = 30 * time.Millisecond
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetNodeMetaDelay(time.Hour)
		err := d.SetNodeLabels(ctx, "n2", "two", map[string]string{"ssd": "true"})
		if !errors.Is(err, ErrLabelsPending) {
			t.Fatalf("err = %v, want ErrLabelsPending", err)
		}
		if nomad.NodeMeta("n2")["caravana.label.ssd"] != "true" {
			t.Error("the write was not made")
		}
	})

	t.Run("a cancelled context ends the wait with its error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetNodeMetaDelay(time.Hour)
		cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		if err := d.SetNodeLabels(cctx, "n2", "two", map[string]string{"a": "1"}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want the context's", err)
		}
	})

	t.Run("a refused token says to run the installer", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.FailNodeWrites(403)
		err := d.SetNodeLabels(ctx, "n2", "two", map[string]string{"a": "1"})
		if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "node:write") || !strings.Contains(err.Error(), "run the installer again") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("an unknown node", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if err := d.SetNodeLabels(ctx, "nope", "x", nil); !errors.Is(err, ErrNodeNotFound) {
			t.Errorf("err = %v, want ErrNodeNotFound", err)
		}
	})
}

func TestFreshNodeLabelsSeesAChangeMadeOutside(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	nomad.AddNode("n2", "two", "ready", "10.99.0.2")
	nomad.SetNodeMeta("n2", map[string]string{"caravana.label.a": "1"})
	if _, labels, _ := d.NodeLabels(ctx, "n2"); labels["a"] != "1" {
		t.Fatalf("labels = %v", labels)
	}
	nomad.SetNodeMeta("n2", map[string]string{"caravana.label.a": "2"})
	if _, labels, _ := d.NodeLabels(ctx, "n2"); labels["a"] != "1" {
		t.Errorf("the cached read changed: %v", labels)
	}
	if _, labels, _ := d.FreshNodeLabels(ctx, "n2"); labels["a"] != "2" {
		t.Errorf("FreshNodeLabels = %v, want a=2", labels)
	}
}

func TestSetNodeEligible(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	nomad.AddNode("n2", "two", "ready", "10.99.0.2")
	if err := d.SetNodeEligible(ctx, "n2", false); err != nil {
		t.Fatalf("SetNodeEligible: %v", err)
	}
	nodes, _ := d.Nodes(ctx)
	if len(nodes) != 1 || nodes[0].Eligible {
		t.Errorf("nodes = %+v, want n2 ineligible", nodes)
	}
	if err := d.SetNodeEligible(ctx, "n2", true); err != nil {
		t.Fatal(err)
	}
	if nodes, _ = d.Nodes(ctx); !nodes[0].Eligible {
		t.Error("n2 is still ineligible")
	}
	if err := d.SetNodeEligible(ctx, "nope", true); !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("unknown node: %v, want ErrNodeNotFound", err)
	}
	nomad.FailNodeWrites(403)
	if err := d.SetNodeEligible(ctx, "n2", false); err == nil || !strings.Contains(err.Error(), "node:write") {
		t.Errorf("refused: %v, want the capability error", err)
	}
}

func TestCheckPlacement(t *testing.T) {
	ctx := context.Background()
	job := &api.Job{ID: new("whoami"), TaskGroups: []*api.TaskGroup{{Name: new("web")}}}

	t.Run("an app without placement never plans", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetPlanFailure("web", "${meta.caravana.label.ssd} = true", 2)
		if err := d.checkPlacement(ctx, job, whoami()); err != nil || nomad.PlanRequests() != 0 {
			t.Errorf("err = %v, plans = %d, want nil and 0", err, nomad.PlanRequests())
		}
	})

	t.Run("a label no server has is refused on the require field", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetPlanFailure("web", "${meta.caravana.label.gpu} = true", 2)
		err := d.checkPlacement(ctx, job, placed(app.Placement{Require: map[string]string{"gpu": "true"}}))
		var pe *PlacementError
		if !errors.As(err, &pe) || !errors.Is(err, ErrPlacementImpossible) {
			t.Fatalf("err = %v, want a *PlacementError", err)
		}
		if pe.Field != "services[0].placement.require.gpu" || pe.Message != "no server that accepts work has the label gpu=true" {
			t.Errorf("error = %+v", pe)
		}
	})

	t.Run("avoiding every server is refused on the avoid field", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetPlanFailure("web", "${meta.caravana.server} != two", 1)
		err := d.checkPlacement(ctx, job, placed(app.Placement{Avoid: []string{"two"}}))
		var pe *PlacementError
		if !errors.As(err, &pe) || pe.Field != "services[0].placement.avoid" || pe.Message != "every server that accepts work is avoided" {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("the field is the service's index", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a := whoami()
		a.Services = append(a.Services, a.Services[0])
		a.Services[1].Name = "worker"
		a.Services[1].Placement = &app.Placement{Require: map[string]string{"ssd": "true"}}
		two := &api.Job{ID: new("whoami"), TaskGroups: []*api.TaskGroup{{Name: new("web")}, {Name: new("worker")}}}
		nomad.SetPlanFailure("worker", "${meta.caravana.label.ssd} = true", 1)
		var pe *PlacementError
		if err := d.checkPlacement(ctx, two, a); !errors.As(err, &pe) || pe.Field != "services[1].placement.require.ssd" {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("lack of room is no refusal", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetPlanFailureMetric("web", api.AllocationMetric{
			NodesEvaluated: 2, NodesFiltered: 1, NodesExhausted: 1,
			ConstraintFiltered: map[string]int{"${meta.caravana.label.ssd} = true": 1},
			DimensionExhausted: map[string]int{"memory": 1},
		})
		if err := d.checkPlacement(ctx, job, placed(app.Placement{Require: map[string]string{"ssd": "true"}})); err != nil {
			t.Errorf("err = %v, want none", err)
		}
	})

	t.Run("a failure other constraints explain is no refusal", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetPlanFailureMetric("web", api.AllocationMetric{
			NodesEvaluated: 2, NodesFiltered: 2,
			ConstraintFiltered: map[string]int{"${meta.caravana.label.ssd} = true": 1, "missing compatible host volumes": 1},
		})
		if err := d.checkPlacement(ctx, job, placed(app.Placement{Require: map[string]string{"ssd": "true"}})); err != nil {
			t.Errorf("err = %v, want none", err)
		}
	})

	t.Run("a placeable job passes", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.checkPlacement(ctx, job, placed(app.Placement{Prefer: []app.Preference{{Label: "ssd", Value: "true"}}})); err != nil {
			t.Errorf("err = %v", err)
		}
		if nomad.PlanRequests() != 1 {
			t.Errorf("plans = %d, want 1", nomad.PlanRequests())
		}
	})
}

// A refused deploy registers nothing and creates no volume; an accepted one registers the job with
// the group's constraints.
func TestRegisterChecksPlacement(t *testing.T) {
	ctx := context.Background()
	rule := placed(app.Placement{Require: map[string]string{"gpu": "true"}})

	d, _, nomad := newTestDeployer(t)
	nomad.SetPlanFailure("web", "${meta.caravana.label.gpu} = true", 2)
	_, err := d.Register(ctx, rule, deployment("d1"), ownsIDs(), nil)
	var pe *PlacementError
	if !errors.As(err, &pe) || pe.Field != "services[0].placement.require.gpu" {
		t.Fatalf("Register = %v, want a *PlacementError on require.gpu", err)
	}
	if nomad.Registrations() != 0 || nomad.Job("whoami") != nil {
		t.Error("a job was registered")
	}

	nomad.ClearPlanFailures()
	if _, err := d.Register(ctx, rule, deployment("d2"), ownsIDs(), nil); err != nil {
		t.Fatalf("Register after the rule can be met: %v", err)
	}
	planned := nomad.PlannedJob()
	if planned == nil || len(planned.TaskGroups[0].Constraints) != 1 || planned.TaskGroups[0].Constraints[0].LTarget != "${meta.caravana.label.gpu}" {
		t.Errorf("the planned job's constraints = %+v", planned)
	}

	// An app without a rule is registered without a plan.
	d2, _, nomad2 := newTestDeployer(t)
	if _, err := d2.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil); err != nil || nomad2.PlanRequests() != 0 {
		t.Errorf("Register = %v, plans = %d, want nil and 0", err, nomad2.PlanRequests())
	}
}

func TestQueuedCopies(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	for _, id := range []string{"a", "b", "c"} {
		nomad.AddForeignJob(&api.Job{ID: new(id), Name: new(id)})
	}
	nomad.SetQueued("a", "web", 2)
	nomad.SetQueued("b", "web", 1)
	nomad.SetQueued("b", "web", 0) // cleared
	got, err := d.QueuedCopies(ctx)
	if err != nil {
		t.Fatalf("QueuedCopies: %v", err)
	}
	if len(got) != 1 || got["a"]["web"] != 2 {
		t.Errorf("QueuedCopies = %v, want only a/web = 2", got)
	}
}
