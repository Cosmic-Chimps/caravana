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
	"net/http"
	"strings"
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
	// backupWatcher counts the goroutines that live as long as Start's context,
	// apart from the rollout watchers: the one recording backup runs, the volume
	// backup scheduler and the notifier (and the sender, see sender.go).
	backupWatcher sync.WaitGroup

	changes   changeHub     // live notices for open pages, see changes.go
	watchIdle time.Duration // how long the copies watcher outlives its last subscriber

	budget     time.Duration // for one deploy's registry and Nomad work
	retryGap   time.Duration // between retries of a store write
	backupPoll time.Duration // between two passes of the backup watcher

	// Volume runs and their scheduler (feature 016), see volumebackup.go and
	// schedule.go.
	clock       clock         // the time, for the scheduler and run IDs
	scheduleGap time.Duration // between two passes of the volume backup scheduler
	volume      volumeTiming

	// Notifications (feature 018), see notifications.go.
	publicURL    string        // the console's address for links; empty means no links
	resolveAfter time.Duration // how long a problem must be gone before "resolved" is sent
	notifyClient *http.Client  // what deliveries go through; tests may replace it
	notifyGap    time.Duration // between two checks of the needs-you list, see notifier.go
	senderGap    time.Duration // between two looks for due deliveries, see sender.go
	queue        *deliveryQueue

	// App actions (feature 020.5), see actions.go.
	commandTimeout time.Duration // how long a command may run
	restartWait    time.Duration // how long a restart waits for one copy to come back

	// Schedules (feature 022), see schedules.go, schedulerun.go,
	// scheduleclock.go and schedulewatch.go.
	scheduleScanGap       time.Duration // between two passes of the schedules' clock
	scheduleWatchGap      time.Duration // between two passes of the run watcher
	scheduleStartDeadline time.Duration // how long a run's task may take to start
	scheduleLimit         time.Duration // when positive, every run's stop-after (the lab's shortcut)
	// runMu is held while a run is checked against the ones going and its
	// row is written, so two starts can't both take the last place. It also
	// guards starting and unpurged.
	runMu    sync.Mutex
	starting map[string]bool // runs startRun is registering; the watcher leaves them alone
	unpurged map[string]bool // run jobs whose purge failed; the watcher tries again

	// Terminals (feature 020.6), see terminal.go.
	termMu        sync.Mutex
	terminalsOpen int           // sessions open now
	maxTerminals  int           // sessions that may be open at once
	terminalIdle  time.Duration // how long a session may go without input
	terminalLimit time.Duration // how long a session may stay open
}

// volumeTiming holds the waits of a volume run, so tests can shorten them.
type volumeTiming struct {
	limit    time.Duration // how long a run may take (CARAVANA_VOLUME_BACKUP_TIMEOUT)
	grace    time.Duration // how long past the limit Caravana waits for the result
	poll     time.Duration // between two reads of the result file
	comeBack time.Duration // how long to wait for the service's task to start again
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
		store:      st,
		deployer:   d,
		secrets:    sec,
		log:        log,
		apps:       map[string]*appState{},
		bg:         context.Background(),
		watchIdle:  defaultWatchIdle,
		budget:     25 * time.Second,
		retryGap:   time.Second,
		backupPoll: 15 * time.Second,

		clock:       realClock{},
		scheduleGap: 30 * time.Second,
		volume: volumeTiming{
			limit:    deploy.DefaultVolumeLimit,
			grace:    5 * time.Minute,
			poll:     2 * time.Second,
			comeBack: time.Minute,
		},

		resolveAfter: defaultResolveAfter,
		notifyClient: defaultNotifyClient(),
		notifyGap:    30 * time.Second,
		senderGap:    defaultSenderGap,
		queue:        newDeliveryQueue(),

		commandTimeout: defaultCommandTimeout,
		restartWait:    defaultRestartWait,

		scheduleScanGap:       defaultScheduleScanGap,
		scheduleWatchGap:      defaultScheduleWatchGap,
		scheduleStartDeadline: defaultScheduleStartDeadline,
		starting:              map[string]bool{},
		unpurged:              map[string]bool{},

		maxTerminals:  defaultMaxTerminals,
		terminalIdle:  defaultTerminalIdle,
		terminalLimit: defaultTerminalLimit,
	}
}

// defaultResolveAfter is how long a problem must be gone from the needs-you
// list before a notification says it is resolved.
const defaultResolveAfter = 5 * time.Minute

// SetPublicURL sets the console's own address (CARAVANA_PUBLIC_URL), which
// notifications use to link to the page about an event. Empty means messages
// carry no links. Call it before Start.
func (s *Service) SetPublicURL(u string) {
	s.publicURL = strings.TrimRight(u, "/")
}

// PublicURL is the console's own address as SetPublicURL was given it, without
// a trailing slash, or "" when none was set.
func (s *Service) PublicURL() string { return s.publicURL }

// SetNotifyResolveAfter sets how long a problem must be gone before "resolved"
// is sent (CARAVANA_NOTIFY_RESOLVE_AFTER, 5 minutes by default). Call it
// before Start; a value that isn't positive keeps the default.
func (s *Service) SetNotifyResolveAfter(d time.Duration) {
	if d > 0 {
		s.resolveAfter = d
	}
}

// SetVolumeBackupTimeout sets how long a volume backup or restore may take
// before Caravana gives it up (CARAVANA_VOLUME_BACKUP_TIMEOUT, 30 minutes by
// default). The service stays stopped while a run lasts, so it bounds that
// too. Call it before Start; a value that isn't positive keeps the default.
func (s *Service) SetVolumeBackupTimeout(d time.Duration) {
	if d > 0 {
		s.volume.limit = d
	}
}

// The schedules' timing (feature 022).
const (
	// defaultScheduleScanGap is how often the clock looks for due slots: a
	// run starts within this of its time (SC-002 asks for a minute).
	defaultScheduleScanGap = 10 * time.Second
	// defaultScheduleWatchGap is how often the watcher reads the runs going.
	defaultScheduleWatchGap = 5 * time.Second
	// defaultScheduleStartDeadline is how long a run's task may take to start
	// (an image pull, a copy Nomad can't place) before the run is given up.
	defaultScheduleStartDeadline = 5 * time.Minute
)

// SetScheduleScanGap sets how often the schedules' clock looks for due slots
// (10 s by default); the lab shortens it. Call it before Start; a value that
// isn't positive keeps the default.
func (s *Service) SetScheduleScanGap(d time.Duration) {
	if d > 0 {
		s.scheduleScanGap = d
	}
}

// SetScheduleWatchGap sets how often the runs going are read (5 s by
// default). Call it before Start; a value that isn't positive keeps the
// default.
func (s *Service) SetScheduleWatchGap(d time.Duration) {
	if d > 0 {
		s.scheduleWatchGap = d
	}
}

// SetScheduleStartDeadline sets how long a run's task may take to start
// before the run is given up (5 minutes by default). Call it before Start; a
// value that isn't positive keeps the default.
func (s *Service) SetScheduleStartDeadline(d time.Duration) {
	if d > 0 {
		s.scheduleStartDeadline = d
	}
}

// SetScheduleLimitForTests makes d every run's stop-after, in place of each
// schedule's own (300 s at the least), so the lab can see a run stopped at its
// limit in seconds. Zero gives the schedules theirs back. Call it before Start.
func (s *Service) SetScheduleLimitForTests(d time.Duration) {
	s.scheduleLimit = max(d, 0)
}

// Start resumes every deployment a previous run left unfinished (research
// R10), each in the background, and keeps ctx for the watchers deploys start
// later. Cancelling ctx stops every watcher; Wait then returns. Start never
// waits for Nomad.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	s.bg = ctx
	s.mu.Unlock()
	// A restart or a command that was running when the last process ended has
	// nobody left to finish it.
	if err := s.failInterruptedActions(ctx); err != nil {
		return err
	}
	if err := s.resume(ctx); err != nil {
		return err
	}
	if err := s.resumeVolumeRuns(ctx); err != nil {
		return err
	}
	s.backupWatcher.Add(1)
	go func() {
		defer s.backupWatcher.Done()
		s.watchBackups(ctx, s.backupPoll)
	}()
	s.backupWatcher.Add(1)
	go func() {
		defer s.backupWatcher.Done()
		s.scheduleVolumeBackups(ctx, s.scheduleGap)
	}()
	// Schedules: the watcher settles the runs a previous process left.
	s.backupWatcher.Add(1)
	go func() {
		defer s.backupWatcher.Done()
		s.watchScheduleRuns(ctx, s.scheduleWatchGap, nil)
	}()
	// The notifier starts no watchers, so it can live in the same group.
	s.backupWatcher.Add(1)
	go func() {
		defer s.backupWatcher.Done()
		s.runNotifier(ctx, s.notifyGap)
	}()
	// Messages queued before a restart are sent by the first pass.
	s.backupWatcher.Add(1)
	go func() {
		defer s.backupWatcher.Done()
		s.runSender(ctx, s.senderGap)
	}()
	return nil
}

// Wait blocks until every watcher has returned. Call it after cancelling
// Start's context. The backup scheduler goes first: it can start volume runs,
// which are watchers, so none can be added once it has stopped.
func (s *Service) Wait() {
	s.backupWatcher.Wait()
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
