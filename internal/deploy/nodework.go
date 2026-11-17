package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/nomad/api"
)

// Moving work off a server (feature 042). Caravana makes the node ineligible (SetNodeEligible) and
// moves each copy itself with Nomad's allocation stop, which is "stop and reschedule": the
// scheduler places a replacement on an eligible node and the old copy ends (research R4).

// MoveCopy tells Nomad to stop the copy and place a replacement elsewhere. It returns as soon as
// Nomad accepted it; WaitCopyMoved waits for the replacement. A copy Nomad doesn't know is
// ErrCopyNotFound; a 403 is the capability error (alloc-lifecycle).
func (d *Deployer) MoveCopy(ctx context.Context, copyID string) error {
	_, err := d.nomad.Allocations().Stop(&api.Allocation{ID: copyID}, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return d.capabilityError("move copy "+copyID, "alloc-lifecycle", err)
	}
	return nil
}

// WaitCopyMoved waits until the copy MoveCopy stopped has a replacement: a copy of the same job
// and group that Nomad made in its place, on a node other than nodeID, that is running and, when
// checked is true, passes every service check. It returns the replacement's ID.
//
// The replacement is told from the group's other copies by its lineage (a copy's
// PreviousAllocation, read for the copies made after the old one), so the group's copies on other
// servers never count as it. The context's deadline is the time limit: its error is returned when
// it ends. Nomad not answering for a moment is not a failure; the wait goes on.
func (d *Deployer) WaitCopyMoved(ctx context.Context, jobID, group, oldCopyID, nodeID string, checked bool) (string, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	var after uint64 // the replacement is made after the old copy
	if old, _, err := d.nomad.Allocations().Info(oldCopyID, q); err == nil {
		after = old.CreateIndex
	}
	gap := d.Timing.withDefaults().PollGap
	for {
		id, err := d.replacementOf(ctx, jobID, group, oldCopyID, nodeID, after, checked)
		switch {
		case err == nil && id != "":
			return id, nil
		case err != nil && ctx.Err() != nil:
			return "", ctx.Err()
		case err != nil && !errors.Is(err, ErrNomadUnavailable) && !errors.Is(err, ErrCopyNotFound):
			return "", err
		}
		if !sleep(ctx, gap) {
			return "", ctx.Err()
		}
	}
}

// replacementOf returns the ID of the running replacement of oldCopyID, or "" when there is none
// yet.
func (d *Deployer) replacementOf(ctx context.Context, jobID, group, oldCopyID, nodeID string, after uint64, checked bool) (string, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	stubs, _, err := d.nomad.Jobs().Allocations(jobID, false, q)
	if err != nil {
		return "", d.callError("list copies of "+jobID, err)
	}
	for _, s := range stubs {
		if s.TaskGroup != group || s.ID == oldCopyID || s.NodeID == nodeID || s.NodeID == "" ||
			s.DesiredStatus != api.AllocDesiredStatusRun || s.ClientStatus != api.AllocClientStatusRunning ||
			s.CreateIndex < after {
			continue
		}
		full, _, err := d.nomad.Allocations().Info(s.ID, q)
		if err != nil {
			if statusOf(err) == http.StatusNotFound {
				continue
			}
			return "", d.callError("read copy "+s.ID, err)
		}
		if full.PreviousAllocation != oldCopyID {
			continue
		}
		if checked {
			if ok, err := d.checksPass(ctx, s.ID); err != nil || !ok {
				return "", err
			}
		}
		return s.ID, nil
	}
	return "", nil
}
