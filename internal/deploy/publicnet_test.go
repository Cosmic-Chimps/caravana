package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// publishing is whoami with a fixed port published beside its dynamic http one.
func publishing(volumes ...string) app.App {
	a := withVolume(volumes...)
	a.Services[0].Ports = append(a.Services[0].Ports, app.Port{Name: "turn", ContainerPort: 3478, Protocol: app.ProtocolTCPUDP, Published: 3478})
	return a
}

func TestCheckPublicNetwork(t *testing.T) {
	ctx := context.Background()
	res := api.NodeResources{Memory: api.NodeMemoryResources{MemoryMB: 2048}}

	t.Run("an app that publishes nothing asks nothing", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		ids, err := d.checkPublicNetwork(ctx, whoami())
		if err != nil || ids != nil {
			t.Errorf("= %v, %v, want nil, nil", ids, err)
		}
		if list, info, _ := nomad.NodeRequests(); list+info != 0 {
			t.Errorf("read %d nodes, want none", list+info)
		}
	})

	t.Run("no node at all", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		_, err := d.checkPublicNetwork(ctx, publishing())
		var pe *PublicNetworkError
		if !errors.As(err, &pe) || !errors.Is(err, ErrNoPublicNetwork) {
			t.Fatalf("err = %v, want a *PublicNetworkError", err)
		}
		if pe.Field != "services[0].ports[1].published" || !strings.Contains(pe.Message, "rerun the installer") {
			t.Errorf("error = %+v", pe)
		}
	})

	t.Run("a node without the network", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetNode("n1", "one", "ready", res, nil)
		if _, err := d.checkPublicNetwork(ctx, publishing()); !errors.Is(err, ErrNoPublicNetwork) {
			t.Errorf("err = %v, want ErrNoPublicNetwork", err)
		}
	})

	t.Run("a down node with it doesn't count", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetNode("n1", "one", "ready", res, nil)
		nomad.AddNode("n2", "two", "down", "10.99.0.2")
		nomad.SetPublicNetwork("n2", true)
		if _, err := d.checkPublicNetwork(ctx, publishing()); !errors.Is(err, ErrNoPublicNetwork) {
			t.Errorf("err = %v, want ErrNoPublicNetwork", err)
		}
	})

	t.Run("an ineligible node with it doesn't count", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetPublicNetwork("n2", true)
		nomad.SetNodeEligibilityOf("n2", "ineligible", false)
		if _, err := d.checkPublicNetwork(ctx, publishing()); !errors.Is(err, ErrNoPublicNetwork) {
			t.Errorf("err = %v, want ErrNoPublicNetwork", err)
		}
	})

	t.Run("a ready node with it", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetNode("n1", "one", "ready", res, nil)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetPublicNetwork("n2", true)
		ids, err := d.checkPublicNetwork(ctx, publishing())
		if err != nil || !slices.Equal(ids, []string{"n2"}) {
			t.Errorf("= %v, %v, want [n2]", ids, err)
		}
	})

	t.Run("a refused read is the node read error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetNode("n1", "one", "ready", res, nil)
		nomad.FailNodeReads(403)
		if _, err := d.checkPublicNetwork(ctx, publishing()); !errors.Is(err, ErrNodeReadDenied) {
			t.Errorf("err = %v, want ErrNodeReadDenied", err)
		}
	})
}

// A publishing service's volumes go to a node that offers published ports, and
// volumes that exist elsewhere refuse the deploy on the port's field.
func TestEnsureVolumesPublishing(t *testing.T) {
	ctx := context.Background()
	res := api.NodeResources{Memory: api.NodeMemoryResources{MemoryMB: 2048}}
	setup := func(t *testing.T) (*Deployer, *deploytest.Nomad) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetNode("n1", "one", "ready", res, nil)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetPublicNetwork("n2", true)
		nomad.SetVolumeNodes("n1") // Nomad's own pick would be the wrong server
		return d, nomad
	}

	t.Run("new volumes go to the public node", func(t *testing.T) {
		d, nomad := setup(t)
		if err := d.EnsureVolumes(ctx, publishing("data", "cache")); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		for _, name := range []string{"whoami_data", "whoami_cache"} {
			if got := nomad.HostVolumeNode(name); got != "n2" {
				t.Errorf("%s is on %q, want n2", name, got)
			}
		}
	})

	t.Run("existing volumes on the public node are used", func(t *testing.T) {
		d, nomad := setup(t)
		nomad.AddHostVolumeOn("whoami_data", "n2")
		if err := d.EnsureVolumes(ctx, publishing("data", "cache")); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		if got := nomad.HostVolumeNode("whoami_cache"); got != "n2" {
			t.Errorf("whoami_cache is on %q, want n2", got)
		}
	})

	t.Run("existing volumes elsewhere refuse the deploy", func(t *testing.T) {
		d, nomad := setup(t)
		nomad.AddHostVolumeOn("whoami_data", "n1")
		err := d.EnsureVolumes(ctx, publishing("data"))
		var pe *PublicNetworkError
		if !errors.As(err, &pe) || pe.Field != "services[0].ports[1].published" {
			t.Fatalf("err = %v, want a *PublicNetworkError on the port", err)
		}
		if got := nomad.VolumeCreates(); len(got) != 0 {
			t.Errorf("creates = %+v, want none", got)
		}
	})

	t.Run("a service that doesn't publish keeps Nomad's pick", func(t *testing.T) {
		d, nomad := setup(t)
		if err := d.EnsureVolumes(ctx, withVolume("data")); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		if got := nomad.HostVolumeNode("whoami_data"); got != "n1" {
			t.Errorf("whoami_data is on %q, want n1", got)
		}
	})
}

// Register refuses a publishing app before it creates anything when no server
// offers the network, and registers it, with the static port, when one does.
func TestRegisterPublishing(t *testing.T) {
	ctx := context.Background()

	t.Run("no public network", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		_, err := d.Register(ctx, publishing(), deployment("d1"), ownsIDs(), nil)
		if !errors.Is(err, ErrNoPublicNetwork) {
			t.Fatalf("Register() = %v, want ErrNoPublicNetwork", err)
		}
		if nomad.Job("whoami") != nil {
			t.Error("a job was registered")
		}
	})

	t.Run("with the network", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		nomad.SetPublicNetwork("n2", true)
		if _, err := d.Register(ctx, publishing(), deployment("d1"), ownsIDs(), nil); err != nil {
			t.Fatalf("Register() = %v", err)
		}
		job := nomad.Job("whoami")
		if job == nil {
			t.Fatal("no job registered")
		}
		reserved := job.TaskGroups[0].Networks[0].ReservedPorts
		if len(reserved) != 1 || reserved[0].Value != 3478 || reserved[0].HostNetwork != "public" {
			t.Errorf("reserved ports = %+v, want 3478 on public", reserved)
		}
	})
}
