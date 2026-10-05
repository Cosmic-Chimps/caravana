package deploy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/nomad/api"
)

// Purge stops jobID and erases it from Nomad, with its rollouts and copies
// (Nomad's "deregister with purge"), but only when it is Caravana's: the job's
// deployment meta must be one owns recognizes, as for Register. A job someone
// else created under the same name is left alone, because deleting an app must
// never touch what Caravana didn't register. A job Nomad doesn't have counts as
// done: the point is that it is gone, and a delete that is repeated, or whose
// job was removed by hand, must still succeed. Other failures are
// ErrNomadUnavailable, as for every Nomad call.
func (d *Deployer) Purge(ctx context.Context, jobID string,
	owns func(ctx context.Context, deploymentID string) (bool, error)) error {
	current, _, err := d.nomad.Jobs().Info(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return nil
		}
		return d.callError("read job "+jobID, err)
	}
	ours := false
	if id := current.Meta[metaDeployment]; id != "" {
		if ours, err = owns(ctx, id); err != nil {
			return fmt.Errorf("check who owns job %q: %w", jobID, err)
		}
	}
	if !ours {
		return nil
	}

	_, _, err = d.nomad.Jobs().Deregister(jobID, true, (&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return nil
		}
		return d.callError("purge job "+jobID, err)
	}
	return nil
}
