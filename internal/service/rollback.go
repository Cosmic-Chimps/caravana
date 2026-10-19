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
// The Compose file that version was deployed from, if it had one, becomes the
// app's kept file; if it had none, the kept file is dropped with a warning.
//
// It is refused before anything changes when the deployment isn't healthy
// (ErrDeploymentNotHealthy), has no recorded definition
// (ErrDefinitionNotRecorded), belongs to another app (store.ErrNotFound), uses
// an image that is gone from its registry (*deploy.ImageError), or fails what
// any deploy is checked for (a secret with no value, today's rules:
// *app.FieldError), or when the app has no definition yet (ErrAppNotDefined,
// checked first). Secrets keep their current values; ones the current app
// has and that version doesn't are deleted, with a warning.
func (s *Service) RedeployVersion(ctx context.Context, appID, deploymentID string, trigger app.Trigger) (store.DeploymentRecord, error) {
	stored, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	if !stored.App.Defined() {
		return store.DeploymentRecord{}, fmt.Errorf("app %q: %w", appID, ErrAppNotDefined)
	}
	if stored.StoppedAt != nil { // before the registry is asked about the images
		return store.DeploymentRecord{}, fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
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
	// The version's Compose file comes back with its definition, so the editor
	// shows what that version was made from. A version made from a definition has
	// none: the kept file is dropped, with the warning saveAndDeploy adds.
	return s.saveAndDeploy(ctx, a, Condition{}, trigger, made{file: rec.Compose}, &pinned{digests: rec.Deployment.Digests, source: deploymentID})
}
