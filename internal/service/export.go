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
	private, err := s.privateRegistries(ctx)
	if err != nil {
		return "", nil, err
	}
	hcl, err = s.deployer.Export(*rec.Definition, rec.Deployment, rec.CreatedAt, private)
	if err != nil {
		return "", nil, err
	}
	return appID + ".nomad.hcl", hcl, nil
}

// privateRegistries is the set of registry hosts Caravana holds credentials
// for, as nomadjob.Translate takes it. It reads the store's list, not Nomad:
// the secrets don't matter to the shape of a job.
func (s *Service) privateRegistries(ctx context.Context) (map[string]bool, error) {
	regs, err := s.store.ListRegistries(ctx)
	if err != nil {
		return nil, err
	}
	if len(regs) == 0 {
		return nil, nil
	}
	private := make(map[string]bool, len(regs))
	for _, r := range regs {
		private[r.Host] = true
	}
	return private, nil
}
