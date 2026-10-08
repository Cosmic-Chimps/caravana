package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// Why a past deployment can't be redeployed or exported.
var (
	ErrDeploymentNotHealthy  = errors.New("only a healthy deployment can be redeployed")
	ErrDefinitionNotRecorded = errors.New("this deployment was recorded before Caravana saved what each deployment deployed")
	ErrNoLiveDeployment      = errors.New("the app has no healthy deployment yet")
)

// version returns appID's deployment deploymentID with the definition it
// deployed. Another app's deployment is store.ErrNotFound, as if it didn't
// exist; one recorded before definitions were saved is
// ErrDefinitionNotRecorded.
func (s *Service) version(ctx context.Context, appID, deploymentID string) (store.DeploymentRecord, error) {
	rec, err := s.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	if rec.AppID != appID {
		return store.DeploymentRecord{}, fmt.Errorf("deployment %q of app %q: %w", deploymentID, appID, store.ErrNotFound)
	}
	if rec.Definition == nil {
		return store.DeploymentRecord{}, fmt.Errorf("deployment %q: %w", deploymentID, ErrDefinitionNotRecorded)
	}
	return rec, nil
}
