package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// appState orders the deploys of one app (research R9). mu is held from
// image resolution through registration, so two deploys of the same app
// never interleave (DeleteApp takes it too). The other fields belong to
// Service.mu, except deleted, which belongs to mu.
type appState struct {
	mu sync.Mutex
	// deleted is set by DeleteApp while it holds mu. A deploy that was waiting
	// for mu sees it and gives up, instead of registering a job for an app that
	// is gone.
	deleted bool

	latest string // the most recently requested deployment

	// watch is the context of the app's rollout watchers: a child of the
	// process context, created with the first watcher. stopWatchers ends it,
	// which is how DeleteApp stops them all (research R16).
	watch        context.Context
	stopWatchers context.CancelFunc
}

// Deploy validates the app, saves it and deploys it. It returns as soon as
// Nomad has accepted the job; a watcher records how the rollout ends.
//
// Validation errors are *app.FieldError, and nothing is saved. Once the
// deployment record exists, errors are *DeployError and the record says
// failed.
func (s *Service) Deploy(ctx context.Context, a app.App, trigger app.Trigger) (store.DeploymentRecord, error) {
	return s.saveAndDeploy(ctx, a, trigger, nil)
}

// saveAndDeploy is Deploy with warnings from the input (a Compose file) to
// record with the deployment.
func (s *Service) saveAndDeploy(ctx context.Context, a app.App, trigger app.Trigger, warnings []app.Warning) (store.DeploymentRecord, error) {
	if err := s.deployer.Validate(a); err != nil {
		return store.DeploymentRecord{}, err
	}
	if err := s.checkSecrets(ctx, a); err != nil {
		return store.DeploymentRecord{}, err
	}
	if old, err := s.store.GetApp(ctx, a.ID); err == nil {
		warnings = append(warnings, unmountedVolumes(old.App, a)...)
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.DeploymentRecord{}, err
	}
	// Only names are stored: the values go to the secret store, in deploy.
	if _, err := s.store.SaveApp(ctx, a.WithoutSecretValues()); err != nil {
		return store.DeploymentRecord{}, err
	}
	s.appChanged(a.ID)
	return s.deployWarned(ctx, a, trigger, warnings)
}

// unmountedVolumes warns about each volume the stored app has and the new
// one no longer mounts: its data stays, out of reach, until the app is
// deleted (or the volume is mounted again).
func unmountedVolumes(old, next app.App) []app.Warning {
	mounted := map[string]bool{}
	for _, svc := range next.Services {
		for _, v := range svc.Volumes {
			mounted[v.Name] = true
		}
	}
	var out []app.Warning
	for _, svc := range old.Services {
		for _, v := range svc.Volumes {
			if !mounted[v.Name] {
				out = append(out, app.Warning{Message: fmt.Sprintf(
					"volume %q is no longer mounted; its data stays until the app is deleted or a service mounts it again", v.Name)})
			}
		}
	}
	return out
}

// Redeploy deploys the stored app again, exactly as Deploy would. An unknown
// app is store.ErrNotFound. Feature 004's webhook calls it.
func (s *Service) Redeploy(ctx context.Context, appID string, trigger app.Trigger) (store.DeploymentRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	// The settings may have changed since the app was saved.
	if err := s.deployer.Validate(rec.App); err != nil {
		return store.DeploymentRecord{}, err
	}
	return s.deploy(ctx, rec.App, trigger)
}

// deploy runs one deploy of a.
func (s *Service) deploy(ctx context.Context, a app.App, trigger app.Trigger) (store.DeploymentRecord, error) {
	return s.deployWarned(ctx, a, trigger, nil)
}

// deployWarned is deploy with warnings about how a was made (a Compose
// file's changed meanings); they come first in the record.
func (s *Service) deployWarned(ctx context.Context, a app.App, trigger app.Trigger, input []app.Warning) (store.DeploymentRecord, error) {
	dep := app.Deployment{ID: newDeploymentID(), Trigger: trigger, Digests: map[string]string{}}
	rec, err := s.store.CreateDeployment(ctx, a.ID, dep)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	s.deploymentChanged(a.ID)
	state := s.markLatest(a.ID, dep.ID)

	// From here on the deploy finishes even if the client goes away:
	// WithoutCancel keeps the request's values but drops its cancellation.
	ctx = context.WithoutCancel(ctx)

	// sync.Mutex isn't first-come, first-served: a newer deploy may get the
	// lock before an older one. The latest check below is what keeps an
	// older deploy from ever registering after a newer one.
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		// DeleteApp ran while this deploy waited, and erased its record with the app.
		return store.DeploymentRecord{}, fmt.Errorf("app %q was deleted: %w", a.ID, store.ErrNotFound)
	}
	// The budget starts once the lock is held, so waiting behind another
	// deploy of the same app doesn't eat into it (research R11).
	ctx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()

	digests, warnings, err := s.deployer.Resolve(ctx, a)
	if err != nil {
		return s.fail(rec, err)
	}
	if latest := s.latestOf(a.ID); latest != dep.ID {
		msg := fmt.Sprintf("replaced by deployment %s before it was registered", latest)
		if upd, err := s.write(dep.ID, store.DeploymentUpdate{Status: store.StatusSuperseded, Error: msg}); err == nil {
			rec = upd
		} else {
			s.log.Error("record superseded deployment", "deployment", dep.ID, "error", err)
		}
		return rec, nil
	}

	// The secrets go live before Nomad gets the job, and only for the newest
	// deploy (the check above): the new copies read them when they start.
	// Running copies keep the env they started with.
	sets, err := s.buildSecrets(ctx, a)
	if err != nil {
		return s.fail(rec, err)
	}
	if err := s.secrets.Replace(ctx, a.ID, dep.ID, sets); err != nil {
		s.restoreSecrets(a.ID, dep.ID) // Replace may have written some of them
		return s.fail(rec, err)
	}

	dep.Digests = digests
	reg, err := s.deployer.Register(ctx, a, dep, s.owns(a.ID))
	if err != nil {
		s.restoreSecrets(a.ID, dep.ID)
		return s.fail(rec, err)
	}

	// Nomad has accepted the job: from now on nothing is returned as an
	// error. A failed write is logged, and the watcher brings the record up
	// to date (research R6).
	warnings = append(append(slices.Clone(input), warnings...), reg.Warnings...)
	if warnings == nil {
		warnings = []app.Warning{}
	}
	running := store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: reg.JobVersion, Digests: digests, Warnings: warnings}
	// One try only: the watcher retries this write, and the client
	// shouldn't wait on a struggling store.
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 5*time.Second)
	upd, err := s.store.UpdateDeployment(writeCtx, dep.ID, running)
	cancelWrite()
	if err == nil {
		rec = upd
		s.deploymentChanged(a.ID)
	} else {
		s.log.Error("record running deployment; the watcher will retry", "deployment", dep.ID, "error", err)
		rec.Status, rec.JobVersion, rec.Deployment.Digests, rec.Warnings = store.StatusRunning, reg.JobVersion, digests, warnings
	}
	s.startWatcher(a.ID, dep.ID, reg.JobVersion, running)
	return rec, nil
}

// fail records a deploy that failed before Nomad accepted the job and
// returns the error for the client.
func (s *Service) fail(rec store.DeploymentRecord, cause error) (store.DeploymentRecord, error) {
	id := rec.Deployment.ID
	if upd, err := s.write(id, store.DeploymentUpdate{Status: store.StatusFailed, Error: cause.Error()}); err == nil {
		rec = upd
	} else {
		s.log.Error("record failed deployment", "deployment", id, "error", err)
	}
	return rec, &DeployError{AppID: rec.AppID, DeploymentID: id, Err: cause}
}

// startWatcher follows the rollout in the background and records how it
// ends. running is the update that marks the record running; the watcher
// writes it again first, in case the deploy's own write failed.
func (s *Service) startWatcher(appID, deploymentID string, version *uint64, running store.DeploymentUpdate) {
	ctx := s.watchContext(appID)
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.follow(ctx, appID, deploymentID, version, running)
	}()
}

// follow makes sure the record is running with its job version, waits for
// the rollout to end and records the outcome. If ctx ends first (shutdown),
// the record stays running and the next start resumes it.
func (s *Service) follow(ctx context.Context, appID, deploymentID string, version *uint64, running store.DeploymentUpdate) {
	if version == nil {
		v, found := s.findVersion(ctx, appID, deploymentID)
		if !found {
			if ctx.Err() == nil {
				s.finish(appID, deploymentID, store.StatusFailed, "Nomad accepted the job, but has no job version for this deployment")
			}
			return
		}
		version = &v
	}
	running.JobVersion = version
	if _, err := s.update(ctx, deploymentID, running); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) {
			s.log.Error("record running deployment", "deployment", deploymentID, "error", err)
			return
		}
		// Already final (a restart raced us): nothing left to follow.
		return
	}

	out, err := s.deployer.Watch(ctx, appID, *version)
	if err != nil {
		return // shutdown
	}
	status := map[deploy.Result]store.Status{
		deploy.OutcomeHealthy:    store.StatusHealthy,
		deploy.OutcomeFailed:     store.StatusFailed,
		deploy.OutcomeReverted:   store.StatusReverted,
		deploy.OutcomeSuperseded: store.StatusSuperseded,
	}[out.Result]
	msg := out.Message
	if status == store.StatusHealthy {
		msg = "" // error holds why it didn't work; a healthy rollout has none
	}
	s.finish(appID, deploymentID, status, msg)
}

// findVersion asks Nomad for the job version carrying deploymentID until it
// answers (it retries while Nomad is unreachable). found is false when Nomad
// answers that no version has it, or ctx ends.
func (s *Service) findVersion(ctx context.Context, appID, deploymentID string) (uint64, bool) {
	gap := s.retryGap
	for {
		v, found, err := s.deployer.FindVersion(ctx, appID, deploymentID)
		if err == nil {
			return v, found
		}
		s.log.Warn("look up job version; retrying", "deployment", deploymentID, "error", err)
		select {
		case <-time.After(gap):
			gap = min(2*gap, 30*time.Second)
		case <-ctx.Done():
			return 0, false
		}
	}
}

// finish records a final status, even during shutdown, then settles the
// deployment's secrets. Settling is safe to repeat, so it runs even when the
// record couldn't be written (the next start resumes the deployment and
// settles it again).
func (s *Service) finish(appID, deploymentID string, status store.Status, msg string) {
	if _, err := s.write(deploymentID, store.DeploymentUpdate{Status: status, Error: msg}); errors.Is(err, store.ErrNotFound) {
		// The app was deleted while its rollout ended: nothing is left to
		// record, and its secrets went with it.
		s.log.Info("deployment gone before its outcome was recorded", "deployment", deploymentID, "status", status)
		return
	} else if err != nil {
		s.log.Error("record deployment outcome", "deployment", deploymentID, "status", status, "error", err)
	}
	s.settleSecrets(appID, deploymentID, status)
}

// write records a deployment change whatever the request or shutdown is
// doing, with its own short deadline, so an outcome already known is never
// lost.
func (s *Service) write(id string, u store.DeploymentUpdate) (store.DeploymentRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.update(ctx, id, u)
}

// owns reports whether deploymentID is a deployment of appID this Caravana
// recorded: the test for "is this Nomad job ours" (research R7).
func (s *Service) owns(appID string) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, deploymentID string) (bool, error) {
		rec, err := s.store.GetDeployment(ctx, deploymentID)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return rec.AppID == appID, nil
	}
}

// stateOf returns appID's state, creating it on first use.
func (s *Service) stateOf(appID string) *appState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked(appID)
}

// stateLocked is stateOf for a caller that holds s.mu.
func (s *Service) stateLocked(appID string) *appState {
	st := s.apps[appID]
	if st == nil {
		st = &appState{}
		s.apps[appID] = st
	}
	return st
}

// markLatest records deploymentID as appID's newest deploy and returns the
// app's state.
func (s *Service) markLatest(appID, deploymentID string) *appState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(appID)
	st.latest = deploymentID
	return st
}

// latestOf is the newest deployment requested for appID, or "" when the app
// has no state (it was deleted).
func (s *Service) latestOf(appID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.apps[appID]; st != nil {
		return st.latest
	}
	return ""
}

// watchContext is the context appID's rollout watchers run under. It lives
// as long as the process, or until DeleteApp stops the app's watchers.
func (s *Service) watchContext(appID string) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(appID)
	if st.watch == nil {
		st.watch, st.stopWatchers = context.WithCancel(s.bg)
	}
	return st.watch
}

// newDeploymentID returns 26 random lowercase base32 characters.
func newDeploymentID() string {
	return strings.ToLower(rand.Text())
}
