package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

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
	StateStopped    AppState = "stopped"     // the owner stopped the app (feature 020.5); it wins over every other state
)

// AppStatus is what is true about an app right now, from the store and
// Nomad together.
type AppStatus struct {
	State         AppState
	WantedCopies  int     // the sum of each service's replicas (nil replicas = 1)
	HealthyCopies int     // healthy copies of the live version; 0 when Nomad is unreadable
	LiveVersion   *uint64 // the job version the healthy copies run; nil if none is healthy
	NomadError    string  // why the state is unknown (also set while rolling out, if Nomad is unreadable)
	// Services is the same two counts per service, in the app's service order,
	// so a page can say which service is short. Empty for an app with no services.
	Services []ServiceStatus
}

// ServiceStatus is one service's share of an AppStatus: the copies it asks
// for and the healthy ones of the live version.
type ServiceStatus struct {
	Name    string
	Wanted  int
	Healthy int
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
		n += serviceCopies(svc)
	}
	return n
}

// serviceCopies is how many copies one service asks for (nil replicas = 1).
func serviceCopies(svc app.Service) int {
	if svc.Replicas == nil {
		return 1
	}
	return *svc.Replicas
}

// statusOf derives an app's status (data-model "App status"). It is pure: no
// I/O, so the table test calls it directly. nomadErr is non-nil when copies
// couldn't be read; copies is then ignored.
//
// Rules, in order: latest deployment pending or running is rolling_out;
// unreadable Nomad is unknown; healthy >= wanted is healthy; healthy > 0 is
// degraded; otherwise not_running.
//
// A copy is healthy when deploy marked it so, and Nomad hasn't told it to stop
// (a copy on its way out, after a Stop or while a Start replaces it, serves
// nothing for long). The live version is the newest version that has a
// healthy copy, and only copies of that version count, so a healthy copy of
// an old version never hides a broken new one.
func statusOf(a app.App, latest *store.DeploymentRecord, copies []deploy.Copy, nomadErr error) AppStatus {
	st := AppStatus{WantedCopies: wantedCopies(a)}
	if nomadErr != nil {
		st.NomadError = nomadErr.Error()
		copies = nil
	}

	for _, c := range copies {
		if c.Status != deploy.CopyHealthy || c.Stopping {
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

	// The same count per service: healthy copies of the live version, as above.
	for _, svc := range a.Services {
		ss := ServiceStatus{Name: svc.Name, Wanted: serviceCopies(svc)}
		if st.LiveVersion != nil {
			for _, c := range copies {
				if c.Service == svc.Name && c.Status == deploy.CopyHealthy && !c.Stopping && c.Version == *st.LiveVersion {
					ss.Healthy++
				}
			}
		}
		st.Services = append(st.Services, ss)
	}

	switch {
	case latest != nil && (latest.Status == store.StatusPending || latest.Status == store.StatusRunning):
		st.State = StateRollingOut
	case nomadErr != nil:
		st.State = StateUnknown
	case st.WantedCopies > 0 && st.HealthyCopies >= st.WantedCopies:
		st.State = StateHealthy
	case st.HealthyCopies > 0:
		st.State = StateDegraded
	default:
		st.State = StateNotRunning
	}
	return st
}

// statusOfRecord is statusOf for a stored app: a stopped app (feature 020.5) is
// stopped whatever its copies or Nomad say, since stopping is the owner's
// decision and the store knows it. The counts stay as statusOf made them.
func statusOfRecord(rec store.AppRecord, latest *store.DeploymentRecord, copies []deploy.Copy, nomadErr error) AppStatus {
	st := statusOf(rec.App, latest, copies, nomadErr)
	if rec.StoppedAt != nil {
		st.State = StateStopped
	}
	return st
}

// ListApps returns every app with its status, by ID. It makes one Nomad
// request for all apps. When Nomad can't be read, the error is logged once
// and every summary says unknown: the stored data is still worth showing, so
// this never returns a Nomad error.
func (s *Service) ListApps(ctx context.Context) ([]AppSummary, error) {
	return s.ListAppsIn(ctx, "", "")
}

// ListAppsIn is ListApps for one project and/or one environment ("" means
// any; an environment name alone matches that name in every project). A
// project, or an environment of the project, that doesn't exist is
// ErrProjectNotFound or ErrEnvironmentNotFound, so a typo is not an empty list.
func (s *Service) ListAppsIn(ctx context.Context, project, environment string) ([]AppSummary, error) {
	switch {
	case project != "" && environment != "":
		if err := s.requireEnvironment(ctx, project, environment); err != nil {
			return nil, err
		}
	case project != "":
		if err := s.requireProject(ctx, project); err != nil {
			return nil, err
		}
	}
	recs, err := s.store.ListAppsIn(ctx, project, environment)
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
		sum.Status = statusOfRecord(rec, sum.LastDeployment, byJob[rec.App.ID], nomadErr)
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
	sum.Status = statusOfRecord(rec, sum.LastDeployment, copies, nomadErr)
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
	if err := s.checkConsoleDomain(a); err != nil {
		return store.DeploymentRecord{}, err
	}
	if err := s.checkPlace(ctx, a.ID); err != nil {
		return store.DeploymentRecord{}, err
	}
	// Only names are stored, as saveApp does; the values go to the secret store, in deploy.
	if _, err := s.store.InsertApp(ctx, a.WithoutSecretValues(), nil); errors.Is(err, store.ErrAlreadyExists) {
		return store.DeploymentRecord{}, fmt.Errorf("app %q: %w", a.ID, ErrAppExists)
	} else if err != nil {
		return store.DeploymentRecord{}, err
	}
	s.appChanged(a.ID)
	return s.deploy(ctx, a, trigger)
}

// ErrAppNotDefined is the refusal of a deploy "as stored" of an app that has
// no services yet (one saved by CreateAppOnly with only a name). Nothing is
// recorded; a deploy that brings a definition works as for any app.
var ErrAppNotDefined = errors.New("the app has no definition yet: deploy it with one (a body, a Compose file or Configure)")

// CreateAppOnly saves a new app without deploying it: no deployment is
// recorded and nothing reaches Nomad. The app may carry a full definition
// (its first redeploy then deploys it as stored, which is what a webhook does)
// or none: with no services it is only a name, and the first deploy that sends
// a definition gives it one. An existing ID is ErrAppExists, as for CreateApp.
//
// A definition is checked as a deploy would check it (*app.FieldError), except
// for what a deploy needs from the installation (secret values, a backup
// destination), which the deploy that comes later checks. Secret values are
// only ever sent with a deploy, so one here is refused naming its field, never
// dropped; secret names are kept. The ID's project and environment must exist.
func (s *Service) CreateAppOnly(ctx context.Context, a app.App) (store.AppRecord, error) {
	if a.Defined() {
		if err := s.deployer.Validate(a); err != nil {
			return store.AppRecord{}, err
		}
		if err := s.checkConsoleDomain(a); err != nil {
			return store.AppRecord{}, err
		}
	} else {
		if strings.ContainsFunc(a.Name, unicode.IsControl) {
			return store.AppRecord{}, app.NewFieldError("name", "must not contain control characters")
		}
		// The stored document says "services": [], not null.
		a.Source = app.Source{Kind: app.SourceImage}
		a.Services = []app.Service{}
	}
	// A new app is placed by its ID, so it also checks the ID's own form
	// (app.PlaceOf), which is all an app without services needs.
	if err := s.checkPlace(ctx, a.ID); err != nil {
		return store.AppRecord{}, err
	}
	for i, svc := range a.Services {
		for j, sec := range svc.Secrets {
			if sec.Value != nil {
				return store.AppRecord{}, app.NewFieldError(fmt.Sprintf("services[%d].secrets[%d].value", i, j),
					"secret values are sent with a deploy; create the app with the secret's name only")
			}
		}
	}
	rec, err := s.store.InsertApp(ctx, a.WithoutSecretValues(), nil)
	if errors.Is(err, store.ErrAlreadyExists) {
		return store.AppRecord{}, fmt.Errorf("app %q: %w", a.ID, ErrAppExists)
	}
	if err != nil {
		return store.AppRecord{}, err
	}
	s.appChanged(a.ID)
	return rec, nil
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
// It takes the app's deploy lock, so it waits for a deploy in progress, and for
// a volume backup or restore (which holds the same lock until it is settled, at
// most the run's time limit plus a few minutes): the run's own steps never wait
// for the delete, so the two can't deadlock, and deleting the app's volumes
// under a run that is archiving them would be worse than waiting. A deploy that
// was waiting for the lock finds the app deleted and gives up
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
