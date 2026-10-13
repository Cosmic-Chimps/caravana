package service

import (
	"context"
	"fmt"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// RedeployVersion deploys a past healthy deployment of appID again: the
// definition it deployed, with the images it ran, pinned by the same
// digests (the tags are not resolved again). The definition becomes the
// app's stored one, and the new deployment names deploymentID as its source.
//
// It is refused before anything changes when the deployment isn't healthy
// (ErrDeploymentNotHealthy), has no recorded definition
// (ErrDefinitionNotRecorded), belongs to another app (store.ErrNotFound), uses
// an image that is gone from its registry (*deploy.ImageError), or fails what
// any deploy is checked for (a secret with no value, today's rules:
// *app.FieldError). Secrets keep their current values; ones the current app
// has and that version doesn't are deleted, with a warning.
func (s *Service) RedeployVersion(ctx context.Context, appID, deploymentID string, trigger app.Trigger) (store.DeploymentRecord, error) {
	rec, err := s.version(ctx, appID, deploymentID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	if rec.Status != store.StatusHealthy {
		return store.DeploymentRecord{}, fmt.Errorf("deployment %q is %s: %w", deploymentID, rec.Status, ErrDeploymentNotHealthy)
	}
	a := *rec.Definition
	if err := s.deployer.CheckDigests(ctx, a, rec.Deployment.Digests); err != nil {
		return store.DeploymentRecord{}, err
	}
	return s.saveAndDeploy(ctx, a, Condition{}, trigger, nil, &pinned{digests: rec.Deployment.Digests, source: deploymentID})
}
