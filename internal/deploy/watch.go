package deploy

import (
	"context"
	"strings"
	"time"

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

// blockingWait is how long one blocking query may wait for a change.
const blockingWait = 5 * time.Minute

// Watch follows the Nomad deployment of jobVersion until it ends (research
// R8). Failed Nomad calls are retried with backoff for as long as it takes:
// a lost connection never ends a rollout. It returns an error only when ctx
// ends.
func (d *Deployer) Watch(ctx context.Context, appID string, jobVersion uint64) (Outcome, error) {
	timing := d.Timing.withDefaults()
	var waitIndex uint64
	backoff := timing.BackoffStart

	for {
		start := time.Now()
		q := (&api.QueryOptions{WaitIndex: waitIndex, WaitTime: blockingWait}).WithContext(ctx)
		deployments, meta, err := d.nomad.Jobs().Deployments(appID, false, q)
		if err != nil {
			if !sleep(ctx, backoff) {
				return Outcome{}, ctx.Err()
			}
			backoff = min(2*backoff, timing.BackoffMax)
			continue
		}
		backoff = timing.BackoffStart
		// Nomad's advice for blocking queries: if the index goes backwards,
		// start again from 0.
		if meta.LastIndex < waitIndex {
			waitIndex = 0
		} else {
			waitIndex = meta.LastIndex
		}

		if out, done := outcomeOf(deployments, jobVersion); done {
			return out, nil
		}
		// A minimum gap keeps a server that answers at once from turning
		// this into a busy loop.
		if !sleep(ctx, timing.PollGap-time.Since(start)) {
			return Outcome{}, ctx.Err()
		}
	}
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
