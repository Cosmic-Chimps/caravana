package service

import (
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

// App actions (feature 020.5): run a command in a copy, stop and start an app,
// restart its copies. Stop, start and restart are owner decisions about an
// app that already exists, so each is refused unless the app has been
// deployed; none is a deploy and none records a deployment. Every action
// leaves a record (store.ActionRecord) and tells open pages "actions changed".
//
// A command's output is shown to whoever asked and nowhere else. It is never
// logged, never put in a record, an error or a notice: RunCommand returns it
// in its result and forgets it (research R2). The tests search every row, log
// line and answer for a sentinel the command prints.

// Errors the API maps to their own codes.
var (
	// ErrAppStopped is the refusal of every deploy of a stopped app, and of its
	// backups and restores: start it first.
	ErrAppStopped = errors.New("the app is stopped: start it first")
	// ErrAppNotStopped is Start on an app that isn't stopped.
	ErrAppNotStopped = errors.New("the app is not stopped")
	// ErrAppBusy is stop, start or restart while a deploy, a volume backup or
	// another action of the app holds its lock.
	ErrAppBusy = errors.New("the app is busy: a deploy, a volume backup or another action is running; wait for it to finish")
	// ErrCopyNotRunning is a command for a copy that isn't running. It is
	// deploy's error, re-exported so the API and the console never import
	// deploy.
	ErrCopyNotRunning = deploy.ErrCopyNotRunning
)

// The limits of a command and its answer (research R1).
const (
	maxCommandArgs     = 64
	maxCommandArgBytes = 4096
	// commandOutputLimit is how much of each of stdout and stderr is kept.
	commandOutputLimit = 64 << 10
	// defaultCommandTimeout is how long a command may run.
	defaultCommandTimeout = 60 * time.Second
	// defaultRestartWait is how long a restart waits for one copy to come back.
	defaultRestartWait = 2 * time.Minute
	// maxProgramBytes shortens the first word kept in the record.
	maxProgramBytes = 128
)

// CommandResult is what one command did, for the one answer that shows it.
// Nothing in it is kept.
type CommandResult struct {
	// ExitCode is nil when the command timed out or didn't start.
	ExitCode *int
	// Stdout and Stderr are the first 64 KiB of each stream. For a command that
	// didn't start (FailedToStart) Stderr holds Nomad's own reason.
	Stdout, Stderr                   string
	StdoutTruncated, StderrTruncated bool
	TimedOut                         bool
	FailedToStart                    bool
}

// checkCommand applies the limits of a command: one to 64 items, each at most
// 4 KiB, none with a NUL byte, the program (the first) not empty. The error is
// a *app.FieldError on "command".
func checkCommand(command []string) error {
	if len(command) == 0 || len(command) > maxCommandArgs {
		return app.NewFieldError("command", "must have 1 to %d items: the program, then its arguments", maxCommandArgs)
	}
	for i, arg := range command {
		switch {
		case len(arg) > maxCommandArgBytes:
			return app.NewFieldError("command", "item %d is longer than %d bytes", i, maxCommandArgBytes)
		case strings.IndexByte(arg, 0) >= 0:
			return app.NewFieldError("command", "item %d has a NUL byte", i)
		}
	}
	if strings.TrimSpace(command[0]) == "" {
		return app.NewFieldError("command", "the first item, the program, must not be empty")
	}
	return nil
}

// programOf is the word the record keeps of a command: the first word of its
// first item, so that even a whole line put in one item leaves its arguments
// out, shortened to maxProgramBytes.
func programOf(command []string) string {
	words := strings.Fields(command[0])
	if len(words) == 0 {
		return ""
	}
	return strings.ToValidUTF8(truncateBytes(words[0], maxProgramBytes), "")
}

// truncateBytes cuts s to at most n bytes.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// RunCommand runs one command in a copy of the app and returns what it
// printed, its exit code, or that it timed out (60 s) or couldn't start. The
// command is the program and its arguments, run without a shell, a terminal or
// input, in the copy's service task. Each stream is cut at 64 KiB and says so.
//
// What is recorded is that a command ran, in which copy and service, how it
// was asked for (trigger), its first word and its exit code, or "timed out" or
// "failed to start": never the arguments or the output. The record is made
// when the command starts and settled when it ends, whatever the answer.
//
// Errors: an unknown app, or a copy that isn't the app's, is store.ErrNotFound;
// a bad command is a *app.FieldError on "command"; a copy that has ended or
// isn't running is ErrCopyNotRunning; Nomad's own failures are deploy's.
// A command that doesn't exist in the image is a result with FailedToStart.
// No lock is taken: a command doesn't change the app.
func (s *Service) RunCommand(ctx context.Context, appID, copyID string, command []string, trigger app.Trigger) (CommandResult, error) {
	if _, err := s.store.GetApp(ctx, appID); err != nil {
		return CommandResult{}, err
	}
	if err := checkCommand(command); err != nil {
		return CommandResult{}, err
	}
	copies, err := s.deployer.Copies(ctx, appID)
	if err != nil {
		return CommandResult{}, fmt.Errorf("list copies of %q: %w", appID, err)
	}
	i := slices.IndexFunc(copies, func(c Copy) bool { return c.ID == copyID })
	if i < 0 {
		return CommandResult{}, fmt.Errorf("app %q has no copy %q: %w", appID, copyID, store.ErrNotFound)
	}
	c := copies[i]
	if c.Terminal || c.Stopping {
		return CommandResult{}, fmt.Errorf("copy %q is %s: %w", copyID, c.Status, ErrCopyNotRunning)
	}

	id, err := newID("act_")
	if err != nil {
		return CommandResult{}, err
	}
	rec := store.ActionRecord{ID: id, AppID: appID, Action: store.ActionRun, Trigger: string(trigger),
		Status: store.ActionRunning, CopyID: copyID, Service: c.Service, Program: programOf(command),
		StartedAt: time.Now().UTC()}
	if err := s.store.InsertAction(ctx, rec); err != nil {
		return CommandResult{}, err
	}
	s.actionsChanged(appID)

	runCtx, cancel := context.WithTimeout(ctx, s.commandTimeout)
	defer cancel()
	res, err := s.deployer.Exec(runCtx, copyID, c.Service, command, commandOutputLimit)

	// The record says how it ended in words of ours, never the output and
	// never Nomad's text (which could repeat an argument).
	var out CommandResult
	switch {
	case err == nil && res.TimedOut:
		s.finishAction(rec.ID, appID, store.ActionFailed, nil, fmt.Sprintf("timed out after %s", s.commandTimeout))
	case err == nil:
		s.finishAction(rec.ID, appID, store.ActionSucceeded, res.ExitCode, "")
	case errors.Is(err, deploy.ErrExecFailed):
		s.finishAction(rec.ID, appID, store.ActionFailed, nil, "failed to start")
		return CommandResult{FailedToStart: true,
			Stderr: strings.TrimPrefix(err.Error(), deploy.ErrExecFailed.Error()+": ")}, nil
	default:
		s.finishAction(rec.ID, appID, store.ActionFailed, nil, execFailure(err))
		return CommandResult{}, err
	}
	out = CommandResult{
		ExitCode:        res.ExitCode,
		Stdout:          strings.ToValidUTF8(string(res.Stdout), "�"),
		Stderr:          strings.ToValidUTF8(string(res.Stderr), "�"),
		StdoutTruncated: res.StdoutTruncated,
		StderrTruncated: res.StderrTruncated,
		TimedOut:        res.TimedOut,
	}
	return out, nil
}

// execFailure names why a command couldn't be run, in words of ours.
func execFailure(err error) string {
	switch {
	case errors.Is(err, deploy.ErrCopyNotRunning):
		return "the copy was not running"
	case errors.Is(err, deploy.ErrCopyNotFound):
		return "the copy is gone"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, deploy.ErrNomadUnavailable):
		return "Nomad refused or could not be reached"
	}
	return "Nomad could not run it"
}

// finishAction settles an action's record and tells open pages. It uses a
// context of its own, so an outcome already known is written even when the
// request has gone away; a failed write is logged without the outcome's text.
func (s *Service) finishAction(id, appID, status string, exit *int, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.FinishAction(ctx, id, status, exit, msg, time.Now().UTC()); err != nil {
		s.log.Error("record the end of an app action", "action", id, "app", appID, "error", err)
	}
	s.actionsChanged(appID)
}

// actionsChanged tells open pages the app's actions changed. Like every
// notice it carries no content.
func (s *Service) actionsChanged(appID string) {
	s.publish(Change{AppID: appID, What: ChangeActions})
}

// ListActions returns the newest limit actions of the app, newest first. An
// unknown app is store.ErrNotFound.
func (s *Service) ListActions(ctx context.Context, appID string, limit int) ([]store.ActionRecord, error) {
	if _, err := s.store.GetApp(ctx, appID); err != nil {
		return nil, err
	}
	return s.store.ListActions(ctx, appID, limit)
}

// refuseIfStopped is ErrAppStopped when the stored app is stopped. An app that
// doesn't exist is no refusal: the caller creates it or reports it missing.
func (s *Service) refuseIfStopped(ctx context.Context, appID string) error {
	rec, err := s.store.GetApp(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.StoppedAt != nil {
		return fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
	return nil
}

// tryLockApp takes the app's deploy lock without waiting, which is what stop,
// start and restart do: a deploy or a volume run that holds it is a reason to
// say "busy", not to hang. The caller unlocks state.mu (any goroutine may). An
// app deleted in the meantime is store.ErrNotFound.
func (s *Service) tryLockApp(appID string) (*appState, error) {
	state := s.stateOf(appID)
	if !state.mu.TryLock() {
		return nil, fmt.Errorf("app %q: %w", appID, ErrAppBusy)
	}
	if state.deleted {
		state.mu.Unlock()
		return nil, fmt.Errorf("app %q was deleted: %w", appID, store.ErrNotFound)
	}
	return state, nil
}

// requireDeployed is the check the actions share: the app has a definition and
// at least one deployment, so Nomad has a job for it. Otherwise
// ErrAppNotDefined or ErrNoLiveDeployment.
func (s *Service) requireDeployed(ctx context.Context, rec store.AppRecord) error {
	if !rec.App.Defined() {
		return fmt.Errorf("app %q: %w", rec.App.ID, ErrAppNotDefined)
	}
	deps, err := s.store.ListDeployments(ctx, rec.App.ID, 1)
	if err != nil {
		return err
	}
	if len(deps) == 0 {
		return fmt.Errorf("app %q: %w", rec.App.ID, ErrNoLiveDeployment)
	}
	return nil
}

// StopApp stops every copy of the app and its scheduled backup jobs, keeping
// everything else: its definition, secrets, volumes, backups, webhook token and
// history. Nomad keeps the job (stopped), so StartApp can run exactly what ran.
// Until then every deploy of the app is refused (ErrAppStopped) and its
// scheduled backups are skipped. The action is recorded; it is no deployment.
//
// Refusals: an unknown app (store.ErrNotFound), one with no definition or no
// deployment (ErrAppNotDefined, ErrNoLiveDeployment), one already stopped
// (ErrAppStopped), and one whose lock is held by a deploy, a volume run or
// another action (ErrAppBusy).
func (s *Service) StopApp(ctx context.Context, appID string, trigger app.Trigger) (store.ActionRecord, error) {
	return s.setStopped(ctx, appID, trigger, true)
}

// StartApp starts a stopped app again with exactly what was running: the same
// job, the same pinned images, no tag looked up. It is StopApp's counterpart,
// with ErrAppNotStopped for an app that isn't stopped.
func (s *Service) StartApp(ctx context.Context, appID string, trigger app.Trigger) (store.ActionRecord, error) {
	return s.setStopped(ctx, appID, trigger, false)
}

// setStopped is StopApp (stop true) and StartApp.
func (s *Service) setStopped(ctx context.Context, appID string, trigger app.Trigger, stop bool) (store.ActionRecord, error) {
	// Checked before the lock, so asking about an ID that doesn't exist creates
	// no state.
	if _, err := s.store.GetApp(ctx, appID); err != nil {
		return store.ActionRecord{}, err
	}
	state, err := s.tryLockApp(appID)
	if err != nil {
		return store.ActionRecord{}, err
	}
	defer state.mu.Unlock()

	// What was read before the lock may have changed while a deploy held it.
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.ActionRecord{}, err
	}
	if err := s.requireDeployed(ctx, rec); err != nil {
		return store.ActionRecord{}, err
	}
	switch {
	case stop && rec.StoppedAt != nil:
		return store.ActionRecord{}, fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	case !stop && rec.StoppedAt == nil:
		return store.ActionRecord{}, fmt.Errorf("app %q: %w", appID, ErrAppNotStopped)
	}

	kind := store.ActionStart
	if stop {
		kind = store.ActionStop
	}
	action, err := s.beginAction(ctx, appID, kind, trigger)
	if err != nil {
		return store.ActionRecord{}, err
	}

	// The change is Nomad's and the store's together: past this point it
	// finishes even if the client goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.budget)
	defer cancel()
	backups := deploy.BackupJobIDs(rec.App)
	if stop {
		err = s.deployer.StopApp(ctx, appID, backups)
	} else {
		err = s.deployer.StartApp(ctx, appID, backups)
	}
	if errors.Is(err, deploy.ErrJobNotFound) {
		err = fmt.Errorf("app %q: %w", appID, ErrNoLiveDeployment)
	}
	if err == nil {
		var at *time.Time
		if stop {
			at = new(time.Now().UTC())
		}
		err = s.store.SetAppStopped(ctx, appID, at)
	}
	if err != nil {
		s.finishAction(action.ID, appID, store.ActionFailed, nil, truncateBytes(err.Error(), 300))
		action.Status, action.Error = store.ActionFailed, err.Error()
		return action, err
	}
	s.finishAction(action.ID, appID, store.ActionSucceeded, nil, "")
	s.appChanged(appID)
	s.publish(Change{AppID: appID, What: ChangeCopies})
	s.publish(Change{What: ChangeAttention})
	done, err := s.store.ListActions(ctx, appID, 1)
	if err == nil && len(done) == 1 && done[0].ID == action.ID {
		action = done[0]
	}
	return action, nil
}

// beginAction records an action as running and tells open pages.
func (s *Service) beginAction(ctx context.Context, appID, kind string, trigger app.Trigger) (store.ActionRecord, error) {
	id, err := newID("act_")
	if err != nil {
		return store.ActionRecord{}, err
	}
	rec := store.ActionRecord{ID: id, AppID: appID, Action: kind, Trigger: string(trigger),
		Status: store.ActionRunning, StartedAt: time.Now().UTC()}
	if err := s.store.InsertAction(ctx, rec); err != nil {
		return store.ActionRecord{}, err
	}
	s.actionsChanged(appID)
	return rec, nil
}

// RestartApp restarts the running copies of the app's live version one at a
// time, waiting for each to be running again (and passing its checks, when the
// service has a health check) before the next, so an app with two copies never
// goes dark. It returns as soon as the action is recorded, running; a
// goroutine does the restarts holding the app's lock, so a deploy that comes
// meanwhile waits. The record ends succeeded, or failed naming the copy that
// didn't come back within two minutes, and the restarts that stopped there stay
// done. The restarts count as asked for, never as a crash loop.
//
// Refusals: the same as StopApp's, ErrAppStopped for a stopped app, and
// ErrNoRunningCopy when no copy of the live version is running.
func (s *Service) RestartApp(ctx context.Context, appID string, trigger app.Trigger) (store.ActionRecord, error) {
	if _, err := s.store.GetApp(ctx, appID); err != nil {
		return store.ActionRecord{}, err
	}
	state, err := s.tryLockApp(appID)
	if err != nil {
		return store.ActionRecord{}, err
	}
	handedOver := false
	defer func() {
		if !handedOver {
			state.mu.Unlock()
		}
	}()

	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.ActionRecord{}, err
	}
	if err := s.requireDeployed(ctx, rec); err != nil {
		return store.ActionRecord{}, err
	}
	if rec.StoppedAt != nil {
		return store.ActionRecord{}, fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
	copies, err := s.deployer.Copies(ctx, appID)
	if err != nil {
		return store.ActionRecord{}, fmt.Errorf("list copies of %q: %w", appID, err)
	}
	targets := restartTargets(copies)
	if len(targets) == 0 {
		return store.ActionRecord{}, fmt.Errorf("app %q: %w", appID, ErrNoRunningCopy)
	}
	action, err := s.beginAction(ctx, appID, store.ActionRestart, trigger)
	if err != nil {
		return store.ActionRecord{}, err
	}

	handedOver = true
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		defer state.mu.Unlock()
		s.runRestart(s.background(), rec.App, action.ID, targets)
	}()
	return action, nil
}

// restartTargets are the copies a restart goes through: those of the live
// version (the newest version with a healthy copy) that haven't ended and that
// Nomad hasn't told to stop, in the order they are listed. None when no copy
// is healthy: there is nothing live to restart. (Right after a Start, Nomad
// still lists copies it is replacing as running; it refuses to restart them.)
func restartTargets(copies []Copy) []Copy {
	var live *uint64
	for _, c := range copies {
		if c.Status == deploy.CopyHealthy && !c.Stopping && (live == nil || c.Version > *live) {
			live = new(c.Version)
		}
	}
	if live == nil {
		return nil
	}
	var out []Copy
	for _, c := range copies {
		if c.Version == *live && !c.Terminal && !c.Stopping {
			out = append(out, c)
		}
	}
	return out
}

// runRestart restarts each target in turn and records how it ended. ctx is the
// process's: when it ends (shutdown) the record stays running, and the next
// start settles it as interrupted (FailRunningActions).
func (s *Service) runRestart(ctx context.Context, a app.App, actionID string, targets []Copy) {
	for _, c := range targets {
		checked := false
		for _, svc := range a.Services {
			if svc.Name == c.Service {
				checked = svc.Health != nil
			}
		}
		// A second before: the events of Nomad's node and our clock needn't agree.
		since := time.Now().Add(-time.Second)
		err := s.deployer.RestartCopy(ctx, c.ID)
		if err == nil {
			waitCtx, cancel := context.WithTimeout(ctx, s.restartWait)
			err = s.deployer.WaitCopyRestarted(waitCtx, c.ID, c.Service, since, checked)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				err = fmt.Errorf("it was not running again after %s", s.restartWait)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.finishAction(actionID, a.ID, store.ActionFailed, nil,
				truncateBytes(fmt.Sprintf("copy %s of service %s didn't come back: %v", c.ID, c.Service, err), 300))
			return
		}
	}
	s.finishAction(actionID, a.ID, store.ActionSucceeded, nil, "")
	s.publish(Change{AppID: a.ID, What: ChangeCopies})
}

// failInterruptedActions settles the actions a previous run left running: a
// restart or a command needs the process that started it. Called from Start.
func (s *Service) failInterruptedActions(ctx context.Context) error {
	n, err := s.store.FailRunningActions(ctx, "Caravana restarted during it", time.Now().UTC())
	if err != nil {
		return fmt.Errorf("settle interrupted actions: %w", err)
	}
	if n > 0 {
		s.log.Info("settled app actions a previous run left running", "count", n)
	}
	return nil
}
