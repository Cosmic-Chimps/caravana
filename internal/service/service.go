// Package service is Caravana's business logic: deploying apps, following
// their rollouts and reading them back. The HTTP API calls it, and so will
// the webhook (feature 004) and the web UI (007): no feature exists only in
// one of them (constitution V).
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Service deploys apps and records what happens. It is safe for concurrent
// use.
type Service struct {
	store    *store.Store
	deployer *deploy.Deployer
	secrets  secrets.Provider
	log      *slog.Logger

	mu   sync.Mutex
	apps map[string]*appState // per app ID, see deploy.go
	bg   context.Context      // lives as long as the process; set by Start

	// watchers counts the goroutines following rollouts, so Wait can let
	// them finish on shutdown (like awaiting background Tasks in C#).
	watchers sync.WaitGroup

	changes   changeHub     // live notices for open pages, see changes.go
	watchIdle time.Duration // how long the copies watcher outlives its last subscriber

	budget   time.Duration // for one deploy's registry and Nomad work
	retryGap time.Duration // between retries of a store write
}

// DeployError is returned by Deploy and Redeploy once a deployment record
// exists, so callers can point at it. Err is the cause.
type DeployError struct {
	AppID, DeploymentID string
	Err                 error
}

func (e *DeployError) Error() string { return e.Err.Error() }
func (e *DeployError) Unwrap() error { return e.Err }

// New returns a Service. sec holds the apps' secret values (deploy.Variables
// in production). Call Start before deploying.
func New(st *store.Store, d *deploy.Deployer, sec secrets.Provider, log *slog.Logger) *Service {
	return &Service{
		store:     st,
		deployer:  d,
		secrets:   sec,
		log:       log,
		apps:      map[string]*appState{},
		bg:        context.Background(),
		watchIdle: defaultWatchIdle,
		budget:    25 * time.Second,
		retryGap:  time.Second,
	}
}

// Start resumes every deployment a previous run left unfinished (research
// R10), each in the background, and keeps ctx for the watchers deploys start
// later. Cancelling ctx stops every watcher; Wait then returns. Start never
// waits for Nomad.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	s.bg = ctx
	s.mu.Unlock()
	return s.resume(ctx)
}

// Wait blocks until every watcher has returned. Call it after cancelling
// Start's context.
func (s *Service) Wait() {
	s.watchers.Wait()
}

// GetApp returns the stored app, or store.ErrNotFound.
func (s *Service) GetApp(ctx context.Context, id string) (store.AppRecord, error) {
	return s.store.GetApp(ctx, id)
}

// ListDeployments returns an app's newest deployments, newest first, at most
// limit of them (all when limit <= 0). An unknown app is store.ErrNotFound.
func (s *Service) ListDeployments(ctx context.Context, appID string, limit int) ([]store.DeploymentRecord, error) {
	return s.store.ListDeployments(ctx, appID, limit)
}

// GetDeployment returns one deployment of an app. A deployment that belongs
// to another app is store.ErrNotFound too.
func (s *Service) GetDeployment(ctx context.Context, appID, deploymentID string) (store.DeploymentRecord, error) {
	rec, err := s.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	if rec.AppID != appID {
		return store.DeploymentRecord{}, fmt.Errorf("deployment %q of app %q: %w", deploymentID, appID, store.ErrNotFound)
	}
	return rec, nil
}

// background is the context watchers run under.
func (s *Service) background() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bg
}

// update writes a deployment change, retrying until it succeeds or ctx ends.
// A change the store refuses (the record already ended) isn't retried.
func (s *Service) update(ctx context.Context, id string, u store.DeploymentUpdate) (store.DeploymentRecord, error) {
	for {
		rec, err := s.store.UpdateDeployment(ctx, id, u)
		if err == nil {
			s.deploymentChanged(rec.AppID)
			return rec, nil
		}
		if errors.Is(err, store.ErrInvalidTransition) || errors.Is(err, store.ErrNotFound) {
			return rec, err
		}
		s.log.Warn("deployment update failed; retrying", "deployment", id, "status", u.Status, "error", err)
		select {
		case <-time.After(s.retryGap):
		case <-ctx.Done():
			return store.DeploymentRecord{}, err
		}
	}
}
