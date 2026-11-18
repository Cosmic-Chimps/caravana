package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// Result is how a rollout ended.
type Result string

// The four ways a rollout ends. They match the store's final statuses.
const (
	OutcomeHealthy    Result = "healthy"
	OutcomeFailed     Result = "failed"
	OutcomeReverted   Result = "reverted"
	OutcomeSuperseded Result = "superseded"
)

// Outcome is a finished rollout.
type Outcome struct {
	Result Result
	// Message is Nomad's description, e.g. "Failed due to unhealthy
	// allocations - rolling back to job version 3".
	Message string
}

// Nomad's deployment object has no field saying it rolled back, so the
// outcome is read from its description. These texts come from Nomad's
// nomad/structs package (DeploymentStatusDescriptionNewerJob and
// DeploymentStatusDescriptionRollback), which the api module doesn't
// export. The opt-in lab test confirms them against a real Nomad.
const (
	descNewerJob       = "Cancelled due to newer version of job"
	descRollbackMarker = " - rolling back to job version "
)

// RolloutOutcome reads the Nomad deployment of jobVersion once and reports
// how it ended, or false while it is still going (research R8). The service
// asks again when Nomad's event stream says the rollout moved, and every so
// often anyway, so this is one plain query and never waits.
func (d *Deployer) RolloutOutcome(ctx context.Context, appID string, jobVersion uint64) (Outcome, bool, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	deployments, _, err := d.nomad.Jobs().Deployments(appID, false, q)
	if err != nil {
		return Outcome{}, false, fmt.Errorf("read the rollouts of %q: %w", appID, err)
	}
	out, done := outcomeOf(deployments, jobVersion)
	return out, done, nil
}

// outcomeOf finds the deployment for jobVersion and reports whether it has
// ended, and how.
func outcomeOf(deployments []*api.Deployment, jobVersion uint64) (Outcome, bool) {
	newer := false
	for _, dep := range deployments {
		if dep.JobVersion > jobVersion {
			newer = true
		}
		if dep.JobVersion != jobVersion {
			continue
		}
		desc := dep.StatusDescription
		switch dep.Status {
		case api.DeploymentStatusSuccessful:
			return Outcome{Result: OutcomeHealthy, Message: desc}, true
		case api.DeploymentStatusFailed:
			if strings.Contains(desc, descRollbackMarker) {
				return Outcome{Result: OutcomeReverted, Message: desc}, true
			}
			return Outcome{Result: OutcomeFailed, Message: desc}, true
		case api.DeploymentStatusCancelled:
			if desc == descNewerJob {
				return Outcome{Result: OutcomeSuperseded, Message: desc}, true
			}
			return Outcome{Result: OutcomeFailed, Message: desc}, true
		default: // pending, running, paused, blocked, unblocking, initializing
			return Outcome{}, false
		}
	}
	if newer {
		// A newer version took over before Nomad started a rollout for ours.
		return Outcome{Result: OutcomeSuperseded, Message: "a newer job version replaced it before its rollout started"}, true
	}
	return Outcome{}, false
}
