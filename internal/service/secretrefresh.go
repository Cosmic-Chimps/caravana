package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// ErrNoSecretReferences is an app whose live version has no secret reference:
// there is nothing to refresh.
var ErrNoSecretReferences = errors.New("the app's live version has no secret references: there is nothing to refresh")

// RefreshResult is what RefreshSecrets did.
type RefreshResult struct {
	// Changed is false when every reference still has the value the app has,
	// and nothing was deployed.
	Changed bool
	// Deployment is the deployment that rolled the new values out, when
	// Changed. It is a record, so it may still be pending: a watcher follows
	// the rollout as for any deploy.
	Deployment *store.DeploymentRecord
}

// RefreshSecrets reads every reference of the app's running version again
// (feature 037). When a value changed in its provider, it rolls the running
// version out again, with its images pinned to the digests it runs and the
// values read now: a normal health-checked deployment, recorded as a refresh
// of the live version. When nothing changed it says so and deploys nothing.
//
// The app must be defined (ErrAppNotDefined), not stopped (ErrAppStopped),
// have a healthy version with its definition (ErrNoLiveDeployment,
// ErrDefinitionNotRecorded) and a reference in it (ErrNoSecretReferences). A
// provider that refuses or is down is a *ReferenceError, with nothing
// deployed or recorded. Once the deploy starts, errors are as for Deploy:
// *DeployError, with a record saying failed.
//
// The running version's definition becomes the stored app, as for a rollback:
// a definition saved after it that never got healthy is replaced by what runs.
func (s *Service) RefreshSecrets(ctx context.Context, appID string, trigger app.Trigger) (RefreshResult, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return RefreshResult{}, err
	}
	if !rec.App.Defined() {
		return RefreshResult{}, fmt.Errorf("app %q: %w", appID, ErrAppNotDefined)
	}
	if rec.StoppedAt != nil {
		return RefreshResult{}, fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
	live, err := s.store.LatestHealthyDeployment(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return RefreshResult{}, fmt.Errorf("app %q: %w", appID, ErrNoLiveDeployment)
	}
	if err != nil {
		return RefreshResult{}, err
	}
	if live.Definition == nil {
		return RefreshResult{}, fmt.Errorf("deployment %q: %w", live.Deployment.ID, ErrDefinitionNotRecorded)
	}
	a := *live.Definition
	if !HasSecretReferences(a) {
		return RefreshResult{}, fmt.Errorf("app %q: %w", appID, ErrNoSecretReferences)
	}

	// Read now, to see whether anything changed. The deploy reads again under
	// the app's lock, so a value that moves between the two is the newer one.
	resolved, err := s.resolveReferences(ctx, a)
	if err != nil {
		return RefreshResult{}, err
	}
	current, err := s.secrets.Read(ctx, appID)
	if err != nil {
		return RefreshResult{}, err
	}
	changed := false
	for service, set := range resolved {
		for name, value := range set {
			if have, ok := current[service][name]; !ok || have != value {
				changed = true
			}
		}
	}
	if !changed {
		return RefreshResult{}, nil
	}

	digests := live.Deployment.Digests
	if err := s.deployer.CheckDigests(ctx, a, digests); err != nil {
		return RefreshResult{}, err
	}
	res, err := s.saveAndDeploy(ctx, a, Condition{}, trigger, made{file: live.Compose},
		&pinned{digests: digests, source: live.Deployment.ID, refresh: true})
	if err != nil {
		return RefreshResult{}, err
	}
	return RefreshResult{Changed: true, Deployment: &res.DeploymentRecord}, nil
}
