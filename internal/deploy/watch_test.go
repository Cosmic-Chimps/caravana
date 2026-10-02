package deploy

import (
	"context"
	"errors"
	"testing"
	"time"

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
	if _, err := d.Register(context.Background(), whoami(), deployment("d1"), ownsIDs()); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	return d, nomad
}

func TestWatch(t *testing.T) {
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
			got, err := d.Watch(context.Background(), "whoami", 0)
			if err != nil || got != tc.want {
				t.Errorf("Watch() = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestWatchNoDeploymentYet(t *testing.T) {
	d, nomad := registered(t)
	// No script for version 0 yet: Watch must keep waiting, then see it.
	done := make(chan Outcome, 1)
	go func() {
		out, _ := d.Watch(context.Background(), "whoami", 0)
		done <- out
	}()
	time.Sleep(20 * time.Millisecond)
	nomad.Script("whoami", 0, state(api.DeploymentStatusSuccessful, "ok"))
	select {
	case out := <-done:
		if out.Result != OutcomeHealthy {
			t.Errorf("Watch() = %+v, want healthy", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch() did not return")
	}
}

func TestWatchNewerVersionWithoutOurs(t *testing.T) {
	d, nomad := registered(t)
	if _, err := d.Register(context.Background(), whoami(), deployment("d2"), ownsIDs("d1")); err != nil {
		t.Fatalf("Register(d2) = %v", err)
	}
	nomad.Script("whoami", 1, state(api.DeploymentStatusRunning, ""))
	got, err := d.Watch(context.Background(), "whoami", 0)
	if err != nil || got.Result != OutcomeSuperseded {
		t.Errorf("Watch() = %+v, %v; want superseded", got, err)
	}
}

func TestWatchSurvivesNomadDown(t *testing.T) {
	d, nomad := registered(t)
	nomad.Script("whoami", 0, state(api.DeploymentStatusSuccessful, "ok"))
	nomad.Down()
	done := make(chan Outcome, 1)
	go func() {
		out, _ := d.Watch(context.Background(), "whoami", 0)
		done <- out
	}()
	time.Sleep(30 * time.Millisecond)
	select {
	case out := <-done:
		t.Fatalf("Watch() returned %+v while Nomad was down", out)
	default:
	}
	nomad.Up()
	select {
	case out := <-done:
		if out.Result != OutcomeHealthy {
			t.Errorf("Watch() = %+v, want healthy", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch() did not return after Nomad came back")
	}
}

func TestWatchStopsWithContext(t *testing.T) {
	d, nomad := registered(t)
	nomad.Down()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := d.Watch(ctx, "whoami", 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Watch() error = %v, want the context's", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Watch() took %v to notice the context", time.Since(start))
	}
}
