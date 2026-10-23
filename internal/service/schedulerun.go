package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Starting and stopping a schedule's runs (feature 022, research R1 and R3).
// A run is a Nomad batch job of its own, made from the app's live version:
// the newest healthy deployment's pinned image of the service, the service's
// env as that version had it and its secret values as they are now, the
// schedule's command in place of the service's own. Its row is written before
// anything is registered (write-ahead), so a run is never in Nomad without
// Caravana knowing; the watcher (schedulewatch.go) follows it to its end.

// liveVersion is the newest healthy deployment of the app that has a digest
// for service, and that digest: what the service runs now. Without one it is
// ErrNoLiveDeployment.
func (s *Service) liveVersion(ctx context.Context, appID, service string) (store.DeploymentRecord, string, error) {
	deps, err := s.store.ListDeployments(ctx, appID, 0)
	if err != nil {
		return store.DeploymentRecord{}, "", err
	}
	for _, d := range deps {
		if d.Status == store.StatusHealthy && d.Deployment.Digests[service] != "" {
			return d, d.Deployment.Digests[service], nil
		}
	}
	return store.DeploymentRecord{}, "", fmt.Errorf("service %q of %q: %w", service, appID, ErrNoLiveDeployment)
}

// runBlocker says why the app can't have a run now, or nil: it is stopped
// (ErrAppStopped), or it has no live version to run (ErrNoLiveDeployment:
// never defined, never healthy, or a deploy is going, which would mix the old
// image with the new secrets the deploy has already written).
func (s *Service) runBlocker(ctx context.Context, rec store.AppRecord, service string) error {
	if rec.StoppedAt != nil {
		return fmt.Errorf("app %q: %w", rec.App.ID, ErrAppStopped)
	}
	if !rec.App.Defined() {
		return fmt.Errorf("app %q has no definition: %w", rec.App.ID, ErrNoLiveDeployment)
	}
	newest, err := s.store.ListDeployments(ctx, rec.App.ID, 1)
	if err != nil {
		return err
	}
	if len(newest) == 1 && (newest[0].Status == store.StatusPending || newest[0].Status == store.StatusRunning) {
		return fmt.Errorf("app %q is being deployed; a run waits for the deploy to end: %w", rec.App.ID, ErrNoLiveDeployment)
	}
	_, _, err = s.liveVersion(ctx, rec.App.ID, service)
	return err
}

// overlapReason is why a schedule can't start another run while open runs
// are going, in Caravana's words, or "" when it can.
func overlapReason(sched store.ScheduleRecord, open int) string {
	switch {
	case open > 0 && sched.SkipIfRunning:
		return "the run before was still going"
	case open >= maxOpenRuns:
		return fmt.Sprintf("%d runs already going", maxOpenRuns)
	}
	return ""
}

// startRun starts one run of sched. trigger is RunTriggerTimer or
// RunTriggerOwner; slot is the cron instant a timer run answers (nil for an
// owner run).
//
// When the schedule's rule refuses another run now (one is going and it
// skips, or 3 are going), an owner run is ErrRunGoing and a timer run is a
// skipped row for its slot, returned without an error. Otherwise the row is
// written starting, then the job is registered and the row is running. If
// Nomad refuses the job the run is settled could_not_start with the reason
// and returned without an error; if Nomad (or the secret store) couldn't be
// reached it is settled could_not_start too, and the error is returned.
// Without a live version it is ErrNoLiveDeployment and nothing is written:
// callers check runBlocker first.
func (s *Service) startRun(ctx context.Context, sched store.ScheduleRecord, trigger string, slot *time.Time) (ScheduleRun, error) {
	rec, err := s.store.GetApp(ctx, sched.AppID)
	if err != nil {
		return ScheduleRun{}, err
	}
	dep, image, err := s.liveVersion(ctx, sched.AppID, sched.Service)
	if err != nil {
		return ScheduleRun{}, err
	}
	// The service as its live version was deployed: its env and, for the
	// names sidecar, the app's other services.
	def := rec.App
	if dep.Definition != nil {
		def = *dep.Definition
	}
	i := slices.IndexFunc(def.Services, func(svc app.Service) bool { return svc.Name == sched.Service })
	if i < 0 {
		return ScheduleRun{}, fmt.Errorf("service %q of %q isn't in its live version: %w", sched.Service, sched.AppID, ErrNoLiveDeployment)
	}
	svc := def.Services[i]

	id, err := newID("")
	if err != nil {
		return ScheduleRun{}, err
	}
	run := store.ScheduleRunRecord{ID: id, ScheduleID: sched.ID, AppID: sched.AppID, Service: sched.Service,
		Trigger: trigger, SlotAt: slot, Command: sched.Command, DeploymentID: dep.Deployment.ID,
		Status: store.RunStarting, StartedAt: s.clock.Now().UTC()}
	if dep.JobVersion != nil {
		run.JobVersion = new(int(*dep.JobVersion))
	}
	skipped, err := s.claimRun(ctx, sched, &run)
	if err != nil || skipped {
		return runOf(run), err
	}
	defer func() {
		s.runMu.Lock()
		delete(s.starting, id)
		s.runMu.Unlock()
	}()
	s.schedulesChanged(sched.AppID)

	// The row says starting: from here the run ends recorded, whatever the
	// caller does.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.budget)
	defer cancel()
	jobID := deploy.ScheduleRunJobID(sched.AppID, id)

	sets, err := s.secrets.Read(ctx, sched.AppID)
	if err != nil {
		return s.couldNotStart(ctx, run, "the secret store couldn't be read", err)
	}
	private, err := s.privateRegistries(ctx)
	if err != nil {
		return s.couldNotStart(ctx, run, "Caravana couldn't read its registries", err)
	}
	job := s.deployer.ScheduleRunJob(def, sched.ID, id, svc, image, sched.Command, sched.MemoryMB, private)
	if err := s.deployer.StartScheduleRun(ctx, job, sets[svc.Name]); err != nil {
		var refused *deploy.RunStartError
		if errors.As(err, &refused) {
			s.log.Warn("Nomad refused a schedule's run", "schedule", sched.ID, "run", id, "job", jobID, "error", err)
			out, _ := s.couldNotStart(ctx, run, refused.Reason, nil)
			return out, nil
		}
		return s.couldNotStart(ctx, run, "Nomad couldn't be reached", err)
	}
	if err := s.store.SetRunRunning(ctx, id); errors.Is(err, store.ErrNotFound) {
		// The schedule was deleted while the job was being registered: no row
		// is left to settle it by, so it goes now, with its secrets.
		s.purgeRun(ctx, jobID)
		return ScheduleRun{}, fmt.Errorf("schedule %q: %w", sched.ID, ErrScheduleNotFound)
	} else if err != nil {
		// The job runs; the watcher brings the row up to date.
		s.log.Warn("record a schedule's run as running", "run", id, "error", err)
	} else {
		run.Status = store.RunRunning
	}
	s.schedulesChanged(sched.AppID)
	return runOf(run), nil
}

// claimRun writes run's row, under runMu so that two starts can't both take
// the last place. When the schedule's rule refuses another run now, a timer
// run (one with a slot) is written as a skipped slot with the reason, and
// skipped is true; an owner run is ErrRunGoing and nothing is written.
// Otherwise the row is written starting and the run is marked as being
// started, which the watcher leaves alone.
func (s *Service) claimRun(ctx context.Context, sched store.ScheduleRecord, run *store.ScheduleRunRecord) (skipped bool, err error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	open, err := s.store.OpenRuns(ctx, sched.ID)
	if err != nil {
		return false, err
	}
	if reason := overlapReason(sched, len(open)); reason != "" {
		if run.SlotAt == nil {
			if len(open) >= maxOpenRuns {
				return false, fmt.Errorf("schedule %q: %d runs are going: %w", sched.Name, len(open), ErrRunGoing)
			}
			return false, fmt.Errorf("schedule %q: %w", sched.Name, ErrRunGoing)
		}
		run.Status, run.Error, run.FinishedAt = store.RunSkipped, reason, new(run.StartedAt)
		run.DeploymentID, run.JobVersion = "", nil
		if err := s.store.InsertRun(ctx, *run); err != nil {
			return false, err
		}
		s.schedulesChanged(sched.AppID)
		s.publish(Change{What: ChangeAttention}) // three skips in a row are an item
		return true, nil
	}
	if err := s.store.InsertRun(ctx, *run); err != nil {
		return false, err
	}
	s.starting[run.ID] = true
	return false, nil
}

// couldNotStart settles a run that never got going as could_not_start with
// reason, logs cause (when there is one) and returns the run and cause.
func (s *Service) couldNotStart(ctx context.Context, run store.ScheduleRunRecord, reason string, cause error) (ScheduleRun, error) {
	if cause != nil {
		s.log.Warn("a schedule's run couldn't start", "schedule", run.ScheduleID, "run", run.ID, "reason", reason, "error", cause)
	}
	st := store.RunSettlement{Status: store.RunCouldNotStart, Error: reason, FinishedAt: s.clock.Now().UTC()}
	s.settleRun(ctx, run, st)
	run.Status, run.Error, run.FinishedAt = st.Status, st.Error, new(st.FinishedAt)
	return runOf(run), cause
}

// settleRun records how a run ended, keeps the newest 100 runs of its
// schedule, and tells open pages and the needs-you list. A run that was
// already settled keeps its first settlement. Errors are logged: the watcher
// settles the run when this couldn't.
func (s *Service) settleRun(ctx context.Context, run store.ScheduleRunRecord, st store.RunSettlement) {
	if err := s.store.SettleRun(ctx, run.ID, st); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) {
			s.log.Error("record the end of a schedule's run", "run", run.ID, "status", st.Status, "error", err)
		}
		return
	}
	if _, err := s.store.PruneRuns(ctx, run.ScheduleID, keptRuns); err != nil {
		s.log.Warn("prune a schedule's runs", "schedule", run.ScheduleID, "error", err)
	}
	s.schedulesChanged(run.AppID)
	s.publish(Change{What: ChangeAttention})
}

// RunScheduleNow starts a run of the schedule now, whatever its clock (a
// paused schedule too). Refusals: a built-in schedule (ErrScheduleBuiltin), a
// stopped app (ErrAppStopped), no live version or a deploy going
// (ErrNoLiveDeployment), and a run going while the schedule skips
// overlapping runs, or 3 going (ErrRunGoing). A run Nomad refuses comes back
// could_not_start, without an error.
func (s *Service) RunScheduleNow(ctx context.Context, id string) (ScheduleRun, error) {
	sched, err := s.storedSchedule(ctx, id)
	if err != nil {
		return ScheduleRun{}, err
	}
	rec, err := s.store.GetApp(ctx, sched.AppID)
	if err != nil {
		return ScheduleRun{}, err
	}
	if err := s.runBlocker(ctx, rec, sched.Service); err != nil {
		return ScheduleRun{}, err
	}
	return s.startRun(ctx, sched, RunTriggerOwner, nil)
}

// StopScheduleRun stops a run that is going: why is written first (the
// owner), then its job is stopped. Nomad gives the command 10 s to end; the
// watcher then records it stopped. A run that has ended is
// ErrRunNotRunning; a built-in schedule's is ErrScheduleBuiltin.
func (s *Service) StopScheduleRun(ctx context.Context, scheduleID, runID string) (ScheduleRun, error) {
	run, err := s.storedRun(ctx, scheduleID, runID)
	if err != nil {
		return ScheduleRun{}, err
	}
	if !run.Open() {
		return ScheduleRun{}, fmt.Errorf("run %q is %s: %w", runID, run.Status, ErrRunNotRunning)
	}
	if err := s.store.SetRunStopIntent(ctx, runID, stopOwner, s.clock.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrInvalidTransition) {
			return ScheduleRun{}, fmt.Errorf("run %q: %w", runID, ErrRunNotRunning)
		}
		return ScheduleRun{}, err
	}
	// A run whose job isn't registered yet (or is gone) has nothing to stop
	// here: the watcher sees the intent and settles it.
	if err := s.deployer.StopScheduleRun(ctx, runJobID(run)); err != nil && !errors.Is(err, deploy.ErrRunGone) {
		return ScheduleRun{}, err
	}
	s.schedulesChanged(run.AppID)
	stopped, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return ScheduleRun{}, err
	}
	return runOf(stopped), nil
}

// stopAppRuns ends every run of the app that is going, once the app itself is
// stopped: app_stopped is written as each run's stop reason, then its job is
// stopped.
func (s *Service) stopAppRuns(ctx context.Context, appID string) error {
	open, err := s.store.AllOpenRuns(ctx)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	for _, r := range open {
		if r.AppID != appID {
			continue
		}
		if err := s.store.SetRunStopIntent(ctx, r.ID, stopAppStopped, now); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return err
		}
		// A job Nomad doesn't have is settled by the watcher; one it couldn't
		// stop now is stopped again by the watcher, which sees the intent.
		if err := s.deployer.StopScheduleRun(ctx, runJobID(r)); err != nil && !errors.Is(err, deploy.ErrRunGone) {
			s.log.Warn("stop a schedule run with its app; the watcher tries again", "run", r.ID, "error", err)
		}
	}
	return nil
}

// minSecretLen is the shortest secret value maskSecrets replaces: replacing
// "1" or "on" would wreck the output and protect nothing.
const minSecretLen = 6

// maskSecrets replaces every secret value of 6 bytes or more in out with
// "[secret NAME]" (secrets is name → value). It goes through out once, trying
// the longest values first at each place, so a value inside a longer one, or
// one that appears in a replacement's text, can't leave part of a secret
// behind. It is exact: a value that was transformed (base64, URL encoding)
// isn't found.
func maskSecrets(out []byte, secrets map[string]string) []byte {
	type secret struct{ name, value string }
	var list []secret
	for name, value := range secrets {
		if len(value) >= minSecretLen {
			list = append(list, secret{name, value})
		}
	}
	if len(list) == 0 || len(out) == 0 {
		return out
	}
	// strings.Replacer prefers the pair given first where two match at one
	// place, so the order is longest value first (then by name, so it is
	// always the same).
	slices.SortFunc(list, func(a, b secret) int {
		return cmp.Or(cmp.Compare(len(b.value), len(a.value)), cmp.Compare(a.name, b.name))
	})
	pairs := make([]string, 0, 2*len(list))
	for _, s := range list {
		pairs = append(pairs, s.value, "[secret "+s.name+"]")
	}
	return []byte(strings.NewReplacer(pairs...).Replace(string(out)))
}
