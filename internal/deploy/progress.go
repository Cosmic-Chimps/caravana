package deploy

import (
	"context"
	"net/http"

	"github.com/hashicorp/nomad/api"
)

// Progress is how far one rollout has got, summed over the job's task
// groups (research R9). Nomad's types stay in this package, so these are
// plain numbers.
type Progress struct {
	Wanted         int  // copies the rollout wants in all
	Placed         int  // new copies Nomad has placed
	Healthy        int  // new copies that passed their checks
	Unhealthy      int  // new copies that failed them
	CanariesWanted int  // copies that wait for promotion (blue/green)
	Promoted       bool // every group that waits for promotion has been promoted

	// Description is Nomad's own status description, e.g. "Deployment is running".
	Description string
}

// Progress reads the rollout of jobID at jobVersion: one request for the
// job's deployments, like Watch makes, without waiting for a change. found is
// false when Nomad has no deployment for that version, which is not an error:
// the rollout may not have started, or the job may not exist.
func (d *Deployer) Progress(ctx context.Context, jobID string, jobVersion uint64) (p Progress, found bool, err error) {
	deployments, _, err := d.nomad.Jobs().Deployments(jobID, false, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return Progress{}, false, nil
		}
		return Progress{}, false, d.callError("read the rollout of "+jobID, err)
	}
	for _, dep := range deployments {
		if dep.JobVersion == jobVersion {
			return progressOf(dep), true, nil
		}
	}
	return Progress{}, false, nil
}

// progressOf sums a deployment's task groups. Promoted is true when at least
// one group waits for canaries and every group that does has been promoted: a
// group without canaries has nothing to promote, so it never holds the rest back.
func progressOf(dep *api.Deployment) Progress {
	p := Progress{Description: dep.StatusDescription}
	waiting, promoted := 0, 0
	for _, g := range dep.TaskGroups {
		if g == nil {
			continue
		}
		p.Wanted += g.DesiredTotal
		p.Placed += g.PlacedAllocs
		p.Healthy += g.HealthyAllocs
		p.Unhealthy += g.UnhealthyAllocs
		p.CanariesWanted += g.DesiredCanaries
		if g.DesiredCanaries > 0 {
			waiting++
			if g.Promoted {
				promoted++
			}
		}
	}
	p.Promoted = waiting > 0 && promoted == waiting
	return p
}
