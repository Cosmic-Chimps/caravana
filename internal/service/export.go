package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// Export returns a deployment of appID as a Nomad jobspec in HCL, with the
// file name to save it as ("<app>.nomad.hcl"). An empty deploymentID means
// the live version, the newest healthy deployment (ErrNoLiveDeployment when
// there is none). A deployment recorded before definitions were saved is
// ErrDefinitionNotRecorded; another app's is store.ErrNotFound.
func (s *Service) Export(ctx context.Context, appID, deploymentID string) (name string, hcl []byte, err error) {
	if deploymentID == "" {
		if _, err := s.store.GetApp(ctx, appID); err != nil {
			return "", nil, err
		}
		live, err := s.store.LatestHealthyDeployment(ctx, appID)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, fmt.Errorf("export %q: %w", appID, ErrNoLiveDeployment)
		}
		if err != nil {
			return "", nil, err
		}
		deploymentID = live.Deployment.ID
	}
	rec, err := s.version(ctx, appID, deploymentID)
	if err != nil {
		return "", nil, err
	}
	hcl, err = s.deployer.Export(*rec.Definition, rec.Deployment, rec.CreatedAt)
	if err != nil {
		return "", nil, err
	}
	return appID + ".nomad.hcl", hcl, nil
}
