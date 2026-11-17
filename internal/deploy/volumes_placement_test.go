package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// A service's volumes follow its placement rules (spec 042): a new volume is
// created under the service's constraints, so Nomad puts it on a server the
// service may run on, and a volume that exists elsewhere refuses the deploy on
// the rules, since a volume never moves. Found by the cluster test: a volume
// made on any node left a copy requiring a label with nowhere to go.
func TestEnsureVolumesFollowPlacement(t *testing.T) {
	ctx := context.Background()
	placed := func(names ...string) app.App {
		a := withVolume(names...)
		a.Services[0].Placement = &app.Placement{Require: map[string]string{"ssd": "true"}, Avoid: []string{"three"}}
		return a
	}

	t.Run("a new volume carries the rules as constraints", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVolumeNodes("node-a")
		if err := d.EnsureVolumes(ctx, placed("data", "cache")); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		want := []deploytest.VolumeCreate{
			{Name: "whoami_data", NodePool: "default", Constraints: "${meta.caravana.label.ssd} = true; ${meta.caravana.server} != three"},
			{Name: "whoami_cache", NodeID: "node-a"},
		}
		if got := nomad.VolumeCreates(); !slices.Equal(got, want) {
			t.Errorf("creates = %+v, want %+v", got, want)
		}
	})

	t.Run("a service without rules sends no constraints", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.EnsureVolumes(ctx, withVolume("data")); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		if got := nomad.VolumeCreates(); len(got) != 1 || got[0].Constraints != "" {
			t.Errorf("creates = %+v, want one without constraints", got)
		}
	})

	t.Run("an existing volume on a server the rules allow is kept", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("node-b", "two", "ready", "10.99.0.2")
		if err := d.SetNodeLabels(ctx, "node-b", "two", map[string]string{"ssd": "true"}); err != nil {
			t.Fatalf("SetNodeLabels: %v", err)
		}
		nomad.AddHostVolumeOn("whoami_data", "node-b")
		if err := d.EnsureVolumes(ctx, placed("data", "cache")); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		want := []deploytest.VolumeCreate{{Name: "whoami_cache", NodeID: "node-b"}}
		if got := nomad.VolumeCreates(); !slices.Equal(got, want) {
			t.Errorf("creates = %+v, want %+v", got, want)
		}
	})

	t.Run("an existing volume on a server the rules forbid refuses naming the rules", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddNode("node-c", "three", "ready", "10.99.0.3")
		if err := d.SetNodeLabels(ctx, "node-c", "three", map[string]string{"ssd": "true"}); err != nil {
			t.Fatalf("SetNodeLabels: %v", err)
		}
		nomad.AddHostVolumeOn("whoami_data", "node-c")
		err := d.EnsureVolumes(ctx, placed("data", "cache"))
		var pe *PlacementError
		if !errors.As(err, &pe) || pe.Field != "services[0].placement" || !strings.Contains(pe.Message, "three") {
			t.Fatalf("EnsureVolumes = %v, want a PlacementError on services[0].placement naming three", err)
		}
		if !errors.Is(err, ErrPlacementImpossible) {
			t.Errorf("the error isn't ErrPlacementImpossible: %v", err)
		}
		if got := nomad.VolumeCreates(); len(got) != 0 {
			t.Errorf("a refused deploy created volumes: %+v", got)
		}
	})
}
