package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

func TestProgress(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		groups map[string]api.DeploymentState
		desc   string
		want   Progress
	}{
		{"one group", map[string]api.DeploymentState{
			"web": {DesiredTotal: 2, PlacedAllocs: 2, HealthyAllocs: 1},
		}, "Deployment is running", Progress{Wanted: 2, Placed: 2, Healthy: 1, Description: "Deployment is running"}},

		{"sums over task groups", map[string]api.DeploymentState{
			"web":    {DesiredTotal: 3, PlacedAllocs: 2, HealthyAllocs: 1, UnhealthyAllocs: 1},
			"worker": {DesiredTotal: 1, PlacedAllocs: 1, HealthyAllocs: 1},
		}, "", Progress{Wanted: 4, Placed: 3, Healthy: 2, Unhealthy: 1}},

		{"canaries wanted, none promoted", map[string]api.DeploymentState{
			"web": {DesiredTotal: 2, DesiredCanaries: 2, PlacedAllocs: 2},
		}, "", Progress{Wanted: 2, Placed: 2, CanariesWanted: 2}},

		{"canaries promoted", map[string]api.DeploymentState{
			"web": {DesiredTotal: 2, DesiredCanaries: 2, PlacedAllocs: 2, HealthyAllocs: 2, Promoted: true},
		}, "", Progress{Wanted: 2, Placed: 2, Healthy: 2, CanariesWanted: 2, Promoted: true}},

		// Every group that waits for promotion has to be promoted.
		{"one group promoted, the other not", map[string]api.DeploymentState{
			"web":    {DesiredTotal: 2, DesiredCanaries: 2, Promoted: true},
			"worker": {DesiredTotal: 1, DesiredCanaries: 1},
		}, "", Progress{Wanted: 3, CanariesWanted: 3}},

		// A group without canaries has nothing to promote, so it doesn't hold the others back.
		{"a group without canaries doesn't count", map[string]api.DeploymentState{
			"web":    {DesiredTotal: 2, DesiredCanaries: 2, Promoted: true},
			"worker": {DesiredTotal: 1},
		}, "", Progress{Wanted: 3, CanariesWanted: 2, Promoted: true}},

		{"no task groups yet", nil, "Deployment is pending", Progress{Description: "Deployment is pending"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, nomad := registered(t)
			nomad.Script("whoami", 0, deploytest.Rolling(tc.desc, tc.groups))
			got, found, err := d.Progress(ctx, "whoami", 0)
			if err != nil || !found {
				t.Fatalf("Progress = %v, found %v; want it found", err, found)
			}
			if got != tc.want {
				t.Errorf("Progress = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestProgressNotFound(t *testing.T) {
	ctx := context.Background()

	t.Run("Nomad has no deployment for the version", func(t *testing.T) {
		d, _ := registered(t) // registered, but no rollout was scripted
		if p, found, err := d.Progress(ctx, "whoami", 0); err != nil || found || p != (Progress{}) {
			t.Errorf("Progress = %+v, found %v, %v; want the zero value, not found, no error", p, found, err)
		}
	})

	t.Run("only other versions have one", func(t *testing.T) {
		d, nomad := registered(t)
		nomad.Script("whoami", 0, deploytest.Rolling("", map[string]api.DeploymentState{"web": {DesiredTotal: 1}}))
		if _, found, err := d.Progress(ctx, "whoami", 5); err != nil || found {
			t.Errorf("Progress of v5 = found %v, %v; want not found", found, err)
		}
	})

	t.Run("a job Nomad doesn't have", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if _, found, err := d.Progress(ctx, "ghost", 0); err != nil || found {
			t.Errorf("Progress of an unknown job = found %v, %v; want not found and no error", found, err)
		}
	})
}

func TestProgressPicksItsVersion(t *testing.T) {
	d, nomad := registered(t)
	if _, err := d.Register(context.Background(), whoami(), deployment("d2"), ownsIDs("d1"), nil); err != nil {
		t.Fatalf("second Register: %v", err)
	}
	nomad.Script("whoami", 0, deploytest.Rolling("old", map[string]api.DeploymentState{"web": {DesiredTotal: 9}}))
	nomad.Script("whoami", 1, deploytest.Rolling("new", map[string]api.DeploymentState{"web": {DesiredTotal: 2, PlacedAllocs: 1}}))

	got, found, err := d.Progress(context.Background(), "whoami", 1)
	if err != nil || !found || got.Wanted != 2 || got.Placed != 1 || got.Description != "new" {
		t.Errorf("Progress of v1 = %+v, found %v, %v; want the v1 rollout", got, found, err)
	}
}

func TestProgressNomadDown(t *testing.T) {
	d, nomad := registered(t)
	nomad.Down()
	if _, _, err := d.Progress(context.Background(), "whoami", 0); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("Progress with Nomad down = %v, want ErrNomadUnavailable", err)
	}
}
