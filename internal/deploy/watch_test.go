package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

func state(status, desc string) api.Deployment {
	return api.Deployment{Status: status, StatusDescription: desc}
}

// registered registers whoami once and returns the deployer and the fake.
func registered(t *testing.T) (*Deployer, *deploytest.Nomad) {
	t.Helper()
	d, _, nomad := newTestDeployer(t)
	if _, err := d.Register(context.Background(), whoami(), deployment("d1"), ownsIDs(), nil); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	return d, nomad
}

// untilDone asks RolloutOutcome again, as the service does, until the rollout
// has ended. The fake moves a script one state on at each deployments read.
func untilDone(t *testing.T, d *Deployer, appID string, version uint64) Outcome {
	t.Helper()
	for range 20 {
		out, done, err := d.RolloutOutcome(context.Background(), appID, version)
		if err != nil {
			t.Fatalf("RolloutOutcome() = %v", err)
		}
		if done {
			return out
		}
	}
	t.Fatal("the rollout never ended")
	return Outcome{}
}

func TestRolloutOutcome(t *testing.T) {
	running := state(api.DeploymentStatusRunning, "Deployment is running")
	tests := []struct {
		name   string
		states []api.Deployment
		want   Outcome
	}{
		{"successful", []api.Deployment{running, state(api.DeploymentStatusSuccessful, "Deployment completed successfully")},
			Outcome{OutcomeHealthy, "Deployment completed successfully"}},
		{"keeps watching every unfinished status", []api.Deployment{
			state(api.DeploymentStatusPending, ""), running, state(api.DeploymentStatusPaused, ""),
			state(api.DeploymentStatusBlocked, ""), state(api.DeploymentStatusUnblocking, ""),
			state("initializing", ""), state(api.DeploymentStatusSuccessful, "done")},
			Outcome{OutcomeHealthy, "done"}},

		// US2: a newer version cancels the older rollout.
		{"cancelled by a newer version", []api.Deployment{running, state(api.DeploymentStatusCancelled, "Cancelled due to newer version of job")},
			Outcome{OutcomeSuperseded, "Cancelled due to newer version of job"}},

		// US3: the descriptions Nomad uses (nomad/structs).
		{"unhealthy, rolled back", []api.Deployment{running, state(api.DeploymentStatusFailed, "Failed due to unhealthy allocations - rolling back to job version 3")},
			Outcome{OutcomeReverted, "Failed due to unhealthy allocations - rolling back to job version 3"}},
		{"progress deadline, rolled back", []api.Deployment{state(api.DeploymentStatusFailed, "Failed due to progress deadline - rolling back to job version 3")},
			Outcome{OutcomeReverted, "Failed due to progress deadline - rolling back to job version 3"}},
		{"no stable version to revert to", []api.Deployment{state(api.DeploymentStatusFailed, "Failed due to unhealthy allocations - no stable job version to auto revert to")},
			Outcome{OutcomeFailed, "Failed due to unhealthy allocations - no stable job version to auto revert to"}},
		{"progress deadline", []api.Deployment{state(api.DeploymentStatusFailed, "Failed due to progress deadline")},
			Outcome{OutcomeFailed, "Failed due to progress deadline"}},
		{"marked as failed", []api.Deployment{state(api.DeploymentStatusFailed, "Deployment marked as failed")},
			Outcome{OutcomeFailed, "Deployment marked as failed"}},
		{"cancelled because the job stopped", []api.Deployment{state(api.DeploymentStatusCancelled, "Cancelled because job is stopped")},
			Outcome{OutcomeFailed, "Cancelled because job is stopped"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, nomad := registered(t)
			nomad.Script("whoami", 0, tc.states...)
			if got := untilDone(t, d, "whoami", 0); got != tc.want {
				t.Errorf("RolloutOutcome() = %+v; want %+v", got, tc.want)
			}
		})
	}
}

func TestRolloutOutcomeNoDeploymentYet(t *testing.T) {
	d, nomad := registered(t)
	// No script for version 0 yet: the rollout is not over.
	if out, done, err := d.RolloutOutcome(context.Background(), "whoami", 0); err != nil || done {
		t.Fatalf("RolloutOutcome() = %+v, %v, %v; want not done", out, done, err)
	}
	nomad.Script("whoami", 0, state(api.DeploymentStatusSuccessful, "ok"))
	if out := untilDone(t, d, "whoami", 0); out.Result != OutcomeHealthy {
		t.Errorf("RolloutOutcome() = %+v, want healthy", out)
	}
}

func TestRolloutOutcomeNewerVersionWithoutOurs(t *testing.T) {
	d, nomad := registered(t)
	if _, err := d.Register(context.Background(), whoami(), deployment("d2"), ownsIDs("d1"), nil); err != nil {
		t.Fatalf("Register(d2) = %v", err)
	}
	nomad.Script("whoami", 1, state(api.DeploymentStatusRunning, ""))
	if got := untilDone(t, d, "whoami", 0); got.Result != OutcomeSuperseded {
		t.Errorf("RolloutOutcome() = %+v, want superseded", got)
	}
}

// A failed read is the caller's to retry: it is an error, not an outcome.
func TestRolloutOutcomeNomadDown(t *testing.T) {
	d, nomad := registered(t)
	nomad.Script("whoami", 0, state(api.DeploymentStatusSuccessful, "ok"))
	nomad.Down()
	if _, done, err := d.RolloutOutcome(context.Background(), "whoami", 0); err == nil || done {
		t.Errorf("RolloutOutcome() = done %v, error %v; want an error", done, err)
	}
	nomad.Up()
	if out := untilDone(t, d, "whoami", 0); out.Result != OutcomeHealthy {
		t.Errorf("RolloutOutcome() = %+v, want healthy", out)
	}
}

func TestRolloutOutcomeStopsWithContext(t *testing.T) {
	d, _ := registered(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := d.RolloutOutcome(ctx, "whoami", 0); !errors.Is(err, context.Canceled) {
		t.Errorf("RolloutOutcome() error = %v, want the context's", err)
	}
}
