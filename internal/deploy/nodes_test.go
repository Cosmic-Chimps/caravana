package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

func TestNodes(t *testing.T) {
	ctx := context.Background()

	t.Run("no node is an empty list", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		got, err := d.Nodes(ctx)
		if err != nil || len(got) != 0 {
			t.Errorf("Nodes = %+v, %v; want none", got, err)
		}
	})

	t.Run("the lab's node has no app address; added nodes have theirs, oldest first", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
		nomad.AddNode("node-2", "fsn-2", "ready", "10.99.0.2")
		nomad.AddNode("node-3", "fsn-3", "down", "10.99.0.3")
		nomad.SetNodeEligibilityOf("node-2", "ineligible", false)
		got, err := d.Nodes(ctx)
		if err != nil {
			t.Fatalf("Nodes: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("Nodes = %+v, want 3", got)
		}
		if got[0].ID != "node-1" || got[0].AppAddress != "" || !got[0].Eligible || got[0].Down() {
			t.Errorf("first = %+v", got[0])
		}
		if got[1].ID != "node-2" || got[1].AppAddress != "10.99.0.2" || got[1].Eligible || got[1].Down() {
			t.Errorf("second = %+v, want ineligible and up", got[1])
		}
		if got[2].ID != "node-3" || got[2].AppAddress != "10.99.0.3" || !got[2].Down() || got[2].Name != "fsn-3" {
			t.Errorf("third = %+v, want down", got[2])
		}
		if got[1].MemoryMB != 4096 || got[1].CPUMHz != 4000 || got[1].Cores != 2 {
			t.Errorf("second's totals = %+v", got[1])
		}
		nomad.SetNodeStatusOf("node-3", "ready")
		nomad.RemoveNode("node-2")
		got, _ = d.Nodes(ctx)
		if len(got) != 2 || got[1].ID != "node-3" || got[1].Down() {
			t.Errorf("after a change = %+v", got)
		}
	})

	t.Run("a refused token says what to do", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FailNodeReads(403)
		if _, err := d.Nodes(ctx); !errors.Is(err, ErrNodeReadDenied) {
			t.Errorf("Nodes = %v, want ErrNodeReadDenied", err)
		}
	})
}

func TestClusterVariables(t *testing.T) {
	ctx := context.Background()
	const key = "-----BEGIN EC PRIVATE KEY-----\nsentinel-ca-key\n-----END EC PRIVATE KEY-----\n"

	t.Run("missing is ErrNoCluster", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if _, _, err := d.ClusterCA(ctx); !errors.Is(err, ErrNoCluster) {
			t.Errorf("ClusterCA = %v, want ErrNoCluster", err)
		}
		if _, err := d.GossipKey(ctx); !errors.Is(err, ErrNoCluster) {
			t.Errorf("GossipKey = %v, want ErrNoCluster", err)
		}
	})
	t.Run("the CA with one item missing is ErrNoCluster", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": "c"})
		if _, _, err := d.ClusterCA(ctx); !errors.Is(err, ErrNoCluster) {
			t.Errorf("ClusterCA = %v, want ErrNoCluster", err)
		}
	})
	t.Run("both are read; the gossip key is trimmed", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": "cert-pem", "key": key})
		nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": "cGFkZGVkLWdvc3NpcC1rZXk=\n"})
		cert, k, err := d.ClusterCA(ctx)
		if err != nil || string(cert) != "cert-pem" || string(k) != key {
			t.Errorf("ClusterCA = %q, %q, %v", cert, k, err)
		}
		if g, err := d.GossipKey(ctx); err != nil || g != "cGFkZGVkLWdvc3NpcC1rZXk=" {
			t.Errorf("GossipKey = %q, %v", g, err)
		}
	})
	t.Run("a refusal never quotes the items", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": "cert-pem", "key": key})
		nomad.FailVariables(403)
		_, _, err := d.ClusterCA(ctx)
		if err == nil || strings.Contains(err.Error(), "sentinel-ca-key") {
			t.Errorf("ClusterCA with variables refused = %v", err)
		}
	})
}
