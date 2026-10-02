package service

import (
	"context"
	"fmt"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// resume picks up every deployment a previous run left pending or running
// (research R10). Each is handled in its own goroutine, so startup never
// waits on Nomad; while Nomad is unreachable they keep retrying.
func (s *Service) resume(ctx context.Context) error {
	unfinished, err := s.store.ListUnfinishedDeployments(ctx)
	if err != nil {
		return fmt.Errorf("resume deployments: %w", err)
	}
	for _, rec := range unfinished {
		s.log.Info("resuming deployment", "app", rec.AppID, "deployment", rec.Deployment.ID, "status", rec.Status)
		s.watchers.Add(1)
		go func() {
			defer s.watchers.Done()
			s.settle(ctx, rec)
		}()
	}
	return nil
}

// settle follows one unfinished deployment to its end.
func (s *Service) settle(ctx context.Context, rec store.DeploymentRecord) {
	id := rec.Deployment.ID
	running := store.DeploymentUpdate{Status: store.StatusRunning}
	if rec.JobVersion != nil {
		s.follow(ctx, rec.AppID, id, rec.JobVersion, running)
		return
	}
	// No version recorded: either Caravana stopped before registering, or
	// between registering and recording the version. The job carries the
	// deployment ID, so Nomad can tell which.
	v, found := s.findVersion(ctx, rec.AppID, id)
	if ctx.Err() != nil {
		return
	}
	if !found {
		msg := "Caravana stopped before registering this deployment"
		if rec.Status == store.StatusRunning {
			msg = "Nomad has no job version for this deployment"
		}
		s.finish(id, store.StatusFailed, msg)
		return
	}
	s.follow(ctx, rec.AppID, id, &v, running)
}
