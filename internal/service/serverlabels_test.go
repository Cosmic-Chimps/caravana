package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// addReadyServer adds a ready server row and its Nomad node (ID node-<index>, app address 10.99.0.<index>).
func (e *clusterEnv) addReadyServer(t *testing.T, name string) store.Server {
	t.Helper()
	v, err := e.store.InsertServer(context.Background(), store.Server{ID: "id-" + name, Name: name, State: store.ServerReady,
		CreatedAt: e.svc.clock.Now(), JoinedAt: e.svc.clock.Now(), ReadyAt: e.svc.clock.Now()})
	if err != nil {
		t.Fatalf("InsertServer(%s): %v", name, err)
	}
	e.nomad.AddNode(fmt.Sprintf("node-%d", v.Index), name, "ready", fmt.Sprintf("10.99.0.%d", v.Index))
	return v
}

func TestSetServerLabels(t *testing.T) {
	ctx := context.Background()

	t.Run("stored, written to the node and shown", func(t *testing.T) {
		e := newClusterEnv(t)
		two := e.addReadyServer(t, "two")
		v, err := e.svc.SetServerLabels(ctx, "two", map[string]string{"ssd": "true", "region": "eu"})
		if err != nil {
			t.Fatalf("SetServerLabels: %v", err)
		}
		if v.Labels["ssd"] != "true" || v.Labels["region"] != "eu" || !v.LabelsApplied {
			t.Errorf("view labels = %v, applied %v", v.Labels, v.LabelsApplied)
		}
		meta := e.nomad.NodeMeta(fmt.Sprintf("node-%d", two.Index))
		if meta["caravana.server"] != "two" || meta["caravana.label.ssd"] != "true" || meta["caravana.label.region"] != "eu" {
			t.Errorf("node meta = %v", meta)
		}
		row, _ := e.store.ServerByName(ctx, "two")
		if row.Labels["ssd"] != "true" {
			t.Errorf("stored labels = %v", row.Labels)
		}
		// A label removed leaves the node with only what is stored.
		v, err = e.svc.SetServerLabels(ctx, "two", map[string]string{"ssd": "true"})
		if err != nil || len(v.Labels) != 1 || !v.LabelsApplied {
			t.Fatalf("second save = %+v, %v", v.Labels, err)
		}
		if _, has := e.nomad.NodeMeta("node-2")["caravana.label.region"]; has {
			t.Error("the removed label is still on the node")
		}
		// Clearing all of them keeps the server's name.
		if v, err = e.svc.SetServerLabels(ctx, "two", nil); err != nil || len(v.Labels) != 0 || !v.LabelsApplied {
			t.Fatalf("clear = %+v, %v", v, err)
		}
		if got := e.nomad.NodeMeta("node-2"); got["caravana.server"] != "two" || got["caravana.label.ssd"] != "" {
			t.Errorf("node meta after clearing = %v", got)
		}
	})

	t.Run("a bad name or value is a field error and changes nothing", func(t *testing.T) {
		e := newClusterEnv(t)
		e.addReadyServer(t, "two")
		for name, labels := range map[string]map[string]string{
			"labels":     {"Bad Name": "x"},
			"labels.ssd": {"ssd": "has space"},
		} {
			_, err := e.svc.SetServerLabels(ctx, "two", labels)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != name {
				t.Errorf("labels %v: err = %v, want a field error on %s", labels, err, name)
			}
		}
		if e.nomad.NodeMetaWrites() != 0 {
			t.Error("Nomad was written")
		}
	})

	t.Run("unknown, unmanaged and waiting servers", func(t *testing.T) {
		e := newClusterEnv(t)
		e.nomad.AddNode("node-9", "stray", "ready", "")
		if _, err := e.svc.SetServerLabels(ctx, "nope", nil); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("unknown: %v, want ErrServerNotFound", err)
		}
		if _, err := e.svc.SetServerLabels(ctx, "stray", map[string]string{"a": "1"}); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("unmanaged: %v, want ErrServerNotFound", err)
		}
		j := e.newJoin(t, "later")
		if _, err := e.svc.SetServerLabels(ctx, j.Server.Name, map[string]string{"a": "1"}); !errors.Is(err, ErrServerNotReady) {
			t.Errorf("waiting: %v, want ErrServerNotReady", err)
		}
	})

	t.Run("a refused token leaves the labels stored and says so", func(t *testing.T) {
		e := newClusterEnv(t)
		e.addReadyServer(t, "two")
		e.nomad.FailNodeWrites(403)
		_, err := e.svc.SetServerLabels(ctx, "two", map[string]string{"ssd": "true"})
		if err == nil {
			t.Fatal("want the capability error")
		}
		row, _ := e.store.ServerByName(ctx, "two")
		if row.Labels["ssd"] != "true" {
			t.Errorf("stored labels = %v, want ssd=true kept", row.Labels)
		}
		// The next reconcile writes them once the policy allows.
		e.nomad.FailNodeWrites(0)
		e.svc.reconcileNodeLabels(ctx)
		if e.nomad.NodeMeta("node-2")["caravana.label.ssd"] != "true" {
			t.Error("the reconcile didn't write the labels")
		}
	})

	t.Run("a server whose node is down keeps its labels for later", func(t *testing.T) {
		e := newClusterEnv(t)
		e.addReadyServer(t, "two")
		e.nomad.SetNodeStatusOf("node-2", "down")
		// The periodic reconcile may have written the server's name while the node was still
		// up: count the writes from here, once any pass in flight has ended (it holds labelMu).
		e.svc.labelMu.Lock()
		writes := e.nomad.NodeMetaWrites()
		e.svc.labelMu.Unlock()
		v, err := e.svc.SetServerLabels(ctx, "two", map[string]string{"ssd": "true"})
		if err != nil {
			t.Fatalf("SetServerLabels: %v", err)
		}
		if v.LabelsApplied || v.Labels["ssd"] != "true" || e.nomad.NodeMetaWrites() != writes {
			t.Errorf("view = %+v, writes %d", v, e.nomad.NodeMetaWrites()-writes)
		}
		e.nomad.SetNodeStatusOf("node-2", "ready")
		e.svc.reconcileNodeLabels(ctx)
		if e.nomad.NodeMeta("node-2")["caravana.label.ssd"] != "true" {
			t.Error("the reconcile didn't write the labels once the node was back")
		}
	})

	t.Run("a lag in Nomad's servers is no failure", func(t *testing.T) {
		e := newClusterEnv(t)
		e.addReadyServer(t, "two")
		e.svc.deployer.Timing.LabelWait = 20 * time.Millisecond
		e.nomad.SetNodeMetaDelay(time.Hour)
		v, err := e.svc.SetServerLabels(ctx, "two", map[string]string{"ssd": "true"})
		if err != nil {
			t.Fatalf("SetServerLabels: %v", err)
		}
		if v.LabelsApplied {
			t.Error("LabelsApplied is true before Nomad's servers showed the labels")
		}
	})
}

func TestReconcileNodeLabels(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.addReadyServer(t, "two")
	if _, err := e.store.InsertServer(ctx, store.Server{ID: "w", Name: "waiting", State: store.ServerWaiting}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetServerLabels(ctx, 2, map[string]string{"ssd": "true"}); err != nil {
		t.Fatal(err)
	}
	// Somebody edited the node outside Caravana.
	e.nomad.SetNodeMeta("node-2", map[string]string{"caravana.server": "wrong", "caravana.label.old": "1"})

	e.svc.reconcileNodeLabels(ctx)
	got := e.nomad.NodeMeta("node-2")
	if got["caravana.server"] != "two" || got["caravana.label.ssd"] != "true" || got["caravana.label.old"] != "" {
		t.Errorf("node 2's meta = %v", got)
	}
	if first := e.nomad.NodeMeta("node-1"); first["caravana.server"] != firstServerName {
		t.Errorf("the first server's meta = %v, want its name", first)
	}
	// A second pass with everything equal writes nothing.
	writes := e.nomad.NodeMetaWrites()
	e.svc.reconcileNodeLabels(ctx)
	if e.nomad.NodeMetaWrites() != writes {
		t.Errorf("a pass over equal metadata wrote %d times", e.nomad.NodeMetaWrites()-writes)
	}
}

// A server becoming ready gets its name and labels without waiting for the periodic pass.
func TestNodeSyncReconcilesLabelsWhenAServerBecomesReady(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	_, ans := e.join(t, "fsn-2", "fsn-2")
	e.nomad.AddNode("node-2", "fsn-2", "ready", "10.99.0.2")
	if err := e.store.SetServerLabels(ctx, 2, map[string]string{"ssd": "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.NodeSync(ctx, ans.NodeToken, healthyReport(1)); err != nil {
		t.Fatalf("NodeSync: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.nomad.NodeMeta("node-2")["caravana.label.ssd"] != "true" {
		if time.Now().After(deadline) {
			t.Fatalf("node 2's meta = %v", e.nomad.NodeMeta("node-2"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
