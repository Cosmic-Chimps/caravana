package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Copy is one running (or ended) copy of a service. It is deploy's type,
// re-exported so the API and the UI never import deploy.
type Copy = deploy.Copy

// Progress is how far a rollout has got. It is deploy's type, re-exported so
// the API and the UI never import deploy for it.
type Progress = deploy.Progress

// AppState is the one-word answer to "is this app up?" (research R7).
type AppState string

// The states of an app. The first matching rule of statusOf wins.
const (
	StateRollingOut AppState = "rolling_out" // the latest deployment is pending or running
	StateHealthy    AppState = "healthy"     // healthy copies of the live version >= wanted
	StateDegraded   AppState = "degraded"    // some, but not all, wanted copies are healthy
	StateNotRunning AppState = "not_running" // no healthy copy, at least one wanted
	StateUnknown    AppState = "unknown"     // Nomad couldn't be read
)

// AppStatus is what is true about an app right now, from the store and
// Nomad together.
type AppStatus struct {
	State         AppState
	WantedCopies  int     // the sum of each service's replicas (nil replicas = 1)
	HealthyCopies int     // healthy copies of the live version; 0 when Nomad is unreadable
	LiveVersion   *uint64 // the job version the healthy copies run; nil if none is healthy
	NomadError    string  // why the state is unknown (also set while rolling out, if Nomad is unreadable)
}

// AppSummary is an app with its status and newest deployment, for lists and
// headers.
type AppSummary struct {
	store.AppRecord
	Status         AppStatus
	LastDeployment *store.DeploymentRecord // nil if the app was never deployed
}

// wantedCopies is how many copies the stored app asks for.
func wantedCopies(a app.App) int {
	n := 0
	for _, svc := range a.Services {
		if svc.Replicas == nil {
			n++
		} else {
			n += *svc.Replicas
		}
	}
	return n
}

// statusOf derives an app's status (data-model "App status"). It is pure: no
// I/O, so the table test calls it directly. nomadErr is non-nil when copies
// couldn't be read; copies is then ignored.
//
// Rules, in order: latest deployment pending or running is rolling_out;
// unreadable Nomad is unknown; healthy >= wanted is healthy; healthy > 0 is
// degraded; otherwise not_running.
//
// A copy is healthy when deploy marked it so. The live version is the newest
// version that has a healthy copy, and only copies of that version count, so
// a healthy copy of an old version never hides a broken new one.
func statusOf(a app.App, latest *store.DeploymentRecord, copies []deploy.Copy, nomadErr error) AppStatus {
	st := AppStatus{WantedCopies: wantedCopies(a)}
	if nomadErr != nil {
		st.NomadError = nomadErr.Error()
		copies = nil
	}

	for _, c := range copies {
		if c.Status != deploy.CopyHealthy {
			continue
		}
		if st.LiveVersion == nil || c.Version > *st.LiveVersion {
			st.LiveVersion = new(c.Version)
			st.HealthyCopies = 0
		}
		if c.Version == *st.LiveVersion {
			st.HealthyCopies++
		}
	}

	switch {
	case latest != nil && (latest.Status == store.StatusPending || latest.Status == store.StatusRunning):
		st.State = StateRollingOut
	case nomadErr != nil:
		st.State = StateUnknown
	case st.HealthyCopies >= st.WantedCopies:
		st.State = StateHealthy
	case st.HealthyCopies > 0:
		st.State = StateDegraded
	default:
		st.State = StateNotRunning
	}
	return st
}

// ListApps returns every app with its status, by ID. It makes one Nomad
// request for all apps. When Nomad can't be read, the error is logged once
// and every summary says unknown: the stored data is still worth showing, so
// this never returns a Nomad error.
func (s *Service) ListApps(ctx context.Context) ([]AppSummary, error) {
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	latest, err := s.store.LatestDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	byJob, nomadErr := s.deployer.AllCopies(ctx)
	if nomadErr != nil {
		s.log.Warn("can't read copies from Nomad; apps show as unknown", "error", nomadErr)
	}

	out := make([]AppSummary, 0, len(recs))
	for _, rec := range recs {
		sum := AppSummary{AppRecord: rec}
		if d, ok := latest[rec.App.ID]; ok {
			sum.LastDeployment = &d
		}
		// The job ID is the app ID (constitution II). A missing key is a nil slice.
		sum.Status = statusOf(rec.App, sum.LastDeployment, byJob[rec.App.ID], nomadErr)
		out = append(out, sum)
	}
	return out, nil
}

// AppSummary returns one app with its status. An unknown app is
// store.ErrNotFound. As with ListApps, a Nomad failure is logged and shows as
// an unknown status, not an error.
func (s *Service) AppSummary(ctx context.Context, id string) (AppSummary, error) {
	rec, err := s.store.GetApp(ctx, id)
	if err != nil {
		return AppSummary{}, err
	}
	sum := AppSummary{AppRecord: rec}
	if recs, err := s.store.ListDeployments(ctx, id, 1); err != nil {
		return AppSummary{}, fmt.Errorf("app summary %q: %w", id, err)
	} else if len(recs) > 0 {
		sum.LastDeployment = &recs[0]
	}
	copies, nomadErr := s.deployer.Copies(ctx, id)
	if nomadErr != nil {
		s.log.Warn("can't read copies from Nomad; app shows as unknown", "app", id, "error", nomadErr)
	}
	sum.Status = statusOf(rec.App, sum.LastDeployment, copies, nomadErr)
	return sum, nil
}

// RecentDeployments returns the newest deployments across all apps, newest
// first, at most limit of them (the caller validates limit).
func (s *Service) RecentDeployments(ctx context.Context, limit int) ([]store.DeploymentRecord, error) {
	return s.store.ListRecentDeployments(ctx, limit)
}

// ListDeploymentsBefore returns the page of an app's deployments that comes
// after one ending at before: the deployments created earlier, newest first,
// at most limit of them (the caller validates limit). An unknown app is
// store.ErrNotFound.
func (s *Service) ListDeploymentsBefore(ctx context.Context, appID string, before time.Time, limit int) ([]store.DeploymentRecord, error) {
	return s.store.ListDeploymentsBefore(ctx, appID, before, limit)
}

// Progress reads how far a deployment's rollout has got. It is nil unless the
// deployment is running, has a job version, and Nomad has a rollout for that
// version; a final deployment never has progress, even if Nomad still keeps
// its rollout. A Nomad failure is an error: callers that can do without
// progress (the API, the pages) log it and leave progress out.
func (s *Service) Progress(ctx context.Context, rec store.DeploymentRecord) (*Progress, error) {
	if rec.Status != store.StatusRunning || rec.JobVersion == nil {
		return nil, nil
	}
	p, found, err := s.deployer.Progress(ctx, rec.AppID, *rec.JobVersion)
	if err != nil {
		return nil, fmt.Errorf("progress of deployment %q: %w", rec.Deployment.ID, err)
	}
	if !found {
		return nil, nil
	}
	return &p, nil
}

// ErrAppExists is CreateApp's refusal of an ID that is already stored.
var ErrAppExists = errors.New("an app with this ID already exists")

// CreateApp saves a new app and starts its first deployment, as Deploy does
// for the same app, except that it never replaces a stored one: an existing
// ID is ErrAppExists, and nothing changes (research R14). The console's New
// app and POST /v1/apps call it.
//
// Validation errors are *app.FieldError and nothing is saved. The store, not a
// look-before-you-leap check, decides who owns the ID, so of two creates at
// once exactly one wins. Once the app is saved, errors are *DeployError, as
// for Deploy: the app stays, and its failed deployment is recorded.
func (s *Service) CreateApp(ctx context.Context, a app.App, trigger app.Trigger) (store.DeploymentRecord, error) {
	if err := s.deployer.Validate(a); err != nil {
		return store.DeploymentRecord{}, err
	}
	if _, err := s.store.InsertApp(ctx, a); errors.Is(err, store.ErrAlreadyExists) {
		return store.DeploymentRecord{}, fmt.Errorf("app %q: %w", a.ID, ErrAppExists)
	} else if err != nil {
		return store.DeploymentRecord{}, err
	}
	s.appChanged(a.ID)
	return s.deploy(ctx, a, trigger)
}

// Copies lists an app's copies: every copy that hasn't ended, plus the ended
// copies of the newest job version Nomad still keeps, ordered by service,
// then version (newest first), then start time (research R8). The filter and
// the order are deploy's; the service adds the app check, so an unknown app
// is store.ErrNotFound before Nomad is asked. When Nomad can't be read the
// error is one of deploy's (ErrNomadUnavailable): a list of copies has no
// stored fallback, so callers say that Nomad can't be reached.
func (s *Service) Copies(ctx context.Context, appID string) ([]Copy, error) {
	_, copies, err := s.appCopies(ctx, appID)
	return copies, err
}

// appCopies is Copies, with the stored app it checked.
func (s *Service) appCopies(ctx context.Context, appID string) (app.App, []Copy, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return app.App{}, nil, err
	}
	copies, err := s.deployer.Copies(ctx, appID)
	if err != nil {
		return app.App{}, nil, fmt.Errorf("list copies of %q: %w", appID, err)
	}
	return rec.App, copies, nil
}

// DeleteApp erases an app for good (research R16): its Nomad job is stopped
// and purged, its volumes and secrets are deleted, then the webhook token, the
// deployments and the app leave the store. Nomad goes first, so a Nomad,
// volume or secret store failure leaves the app in place, and
// the same call can simply be made again; a job Nomad no longer has counts as
// purged. An unknown app is store.ErrNotFound.
//
// It takes the app's deploy lock, so it waits for a deploy in progress, and a
// deploy that was waiting for the lock finds the app deleted and gives up
// (also store.ErrNotFound). Rollout watchers of the app are stopped before
// the app goes, or they would ask Nomad about a job that no longer exists for
// as long as the process runs. Then the open pages hear of it: deleted for
// the app's own page, apps, activity and attention for the lists.
func (s *Service) DeleteApp(ctx context.Context, id string) error {
	// Checked before the lock, so asking about an ID that doesn't exist
	// creates no state.
	if _, err := s.store.GetApp(ctx, id); err != nil {
		return err
	}

	state := s.stateOf(id)
	state.mu.Lock()
	defer state.mu.Unlock()

	if err := s.deployer.Purge(ctx, id, s.owns(id)); err != nil {
		return fmt.Errorf("delete app %q: %w", id, err)
	}
	// The volumes go once the job is gone (Nomad refuses while a copy uses
	// one), and before the app's record, so a failure keeps the app and the
	// delete can be tried again.
	// Its other jobs (backup schedules, their runs, restores) go too. Its
	// backups stay in the destination: they exist to outlive mistakes.
	if err := s.deployer.DeleteAppJobs(ctx, id); err != nil {
		return fmt.Errorf("delete the backup jobs of app %q: %w", id, err)
	}
	if err := s.deployer.DeleteVolumes(ctx, id); err != nil {
		return fmt.Errorf("delete the volumes of app %q: %w", id, err)
	}
	// The secrets go before the app's record, so a failure here keeps the app
	// and the delete can be tried again (the purge above is safe to repeat).
	if err := s.secrets.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete the secrets of app %q: %w", id, err)
	}

	// Nomad let go of the job: from here on the delete finishes even if the
	// client goes away, because the job is already gone and the app must follow.
	s.mu.Lock()
	if state.stopWatchers != nil {
		state.stopWatchers()
	}
	s.mu.Unlock()
	if err := s.store.DeleteApp(context.WithoutCancel(ctx), id); err != nil {
		return err
	}
	state.deleted = true

	s.mu.Lock()
	if s.apps[id] == state {
		delete(s.apps, id)
	}
	s.mu.Unlock()

	s.publish(Change{AppID: id, What: ChangeDeleted})
	s.publish(Change{What: ChangeApps})
	s.publish(Change{What: ChangeActivity})
	s.publish(Change{What: ChangeAttention})
	return nil
}
