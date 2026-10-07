package service

import (
	"context"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// This file is how a deploy moves secrets (research R4–R6): build the full
// sets from the definition and the values in the store, replace them before
// Nomad gets the job, and once the rollout ends either commit them (healthy)
// or put the previous ones back (failed, reverted).

// buildSecrets returns the sets a deploy of a must make live: a's secrets
// applied to the values in the store now. A keep with no value is an
// *app.FieldError; an unreachable store matches secrets.ErrUnavailable.
func (s *Service) buildSecrets(ctx context.Context, a app.App) (secrets.Sets, error) {
	current, err := s.secrets.Read(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	return secrets.Build(a, current)
}

// checkSecrets builds a's sets once before anything is saved, so a keep
// without a value, an oversized set or an unreachable store saves nothing.
// deploy builds them again under the app's lock, because the values may
// change in between.
func (s *Service) checkSecrets(ctx context.Context, a app.App) error {
	for _, svc := range a.Services {
		if len(svc.Secrets) > 0 {
			_, err := s.buildSecrets(ctx, a)
			return err
		}
	}
	return nil
}

// restoreSecrets puts back the secrets a deploy replaced before Nomad
// accepted its job. The caller holds the app's lock. One try: if it fails,
// the kept copy stays owned by this deployment, and the app's next deploy
// takes it over.
func (s *Service) restoreSecrets(appID, deploymentID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.secrets.Restore(ctx, appID, deploymentID); err != nil {
		s.log.Error("put back the secrets of a failed deploy", "app", appID, "deployment", deploymentID, "error", err)
	}
}

// settleSecrets tells the store how a deployment ended: a healthy one's
// secrets are the running ones now (Commit), a failed or reverted one's
// never ran, so the previous ones go back (Restore). A superseded one does
// nothing: the newer deploy owns the kept copy. It retries until it works or
// the process stops, taking the app's lock per try, so a newer deploy's
// Replace never interleaves with it and a struggling store doesn't hold the
// app's deploys up.
//
// simplified: when the process stops while a Restore keeps failing, the
// failed deploy's values stay live until the app's next deploy, which takes
// the kept copy over and settles it. Persisting pending settles would close
// that gap if it ever matters.
func (s *Service) settleSecrets(appID, deploymentID string, status store.Status) {
	var call func(context.Context, string, string) error
	switch status {
	case store.StatusHealthy:
		call = s.secrets.Commit
	case store.StatusFailed, store.StatusReverted:
		call = s.secrets.Restore
	default:
		return
	}
	ctx := s.background()
	gap := s.retryGap
	for {
		state := s.stateOf(appID)
		state.mu.Lock()
		err := call(ctx, appID, deploymentID)
		state.mu.Unlock()
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			s.log.Error("settle secrets: stopping; the app's next deploy settles them", "app", appID, "deployment", deploymentID, "error", err)
			return
		}
		s.log.Warn("settle secrets; retrying", "app", appID, "deployment", deploymentID, "status", status, "error", err)
		select {
		case <-time.After(gap):
			gap = min(2*gap, 30*time.Second)
		case <-ctx.Done():
		}
	}
}
