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

// Schedules that run in the running copy (feature 040, spec D3 and D4). A
// schedule with target "copy" runs its command with Nomad's alloc exec in the
// service's task, in the oldest healthy copy (or in every healthy copy, one
// after the other, with allCopies), the way Run a command does (020.5). It
// registers no job, so a service with volumes is no problem: nothing else
// writes to them.
//
// The row of a run is the schedule's usual one, written ahead as starting and
// settled at the end, with the copies it ran in. What the command printed is
// read and dropped at once (research R2 of 020.5: it may hold anything): it is
// never stored, logged, put in an error, a notice, a page or an answer, so
// output_kept stays 0 and GetScheduleRunOutput says ErrOutputNotKept. Each copy
// also gets an app_actions row "run" with the trigger "schedule".
//
// A goroutine does the execs, so the clock's pass doesn't wait for them. The
// run stays in s.starting until it is settled, which keeps the watcher away
// (there is no job to look at), and has a cancel function in s.copyRuns that
// Stop, an app stop and a schedule's removal call after writing why. A process
// that ends takes the execs with it: the watcher of the next one settles the
// run it finds open (settleOrphanCopyRun).

// copyRunLost is the words of a copy run a process left open when it ended.
const copyRunLost = "lost while Caravana was restarting"

// pickCopies are the copies a run of service goes in: the healthy ones that
// Nomad hasn't told to stop, oldest first (Nomad's create time, then the ID,
// so the order never changes between calls), the first alone unless all is
// true. None is ErrNoRunningCopy.
func pickCopies(copies []Copy, service string, all bool) ([]Copy, error) {
	var live []Copy
	for _, c := range copies {
		if c.Service == service && c.Status == deploy.CopyHealthy && !c.Terminal && !c.Stopping {
			live = append(live, c)
		}
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("service %q has no healthy copy: %w", service, ErrNoRunningCopy)
	}
	slices.SortStableFunc(live, func(a, b Copy) int {
		switch {
		case a.CreatedAt != nil && b.CreatedAt != nil:
			if c := a.CreatedAt.Compare(*b.CreatedAt); c != 0 {
				return c
			}
		case a.CreatedAt != nil: // a copy with a known time comes before one without
			return -1
		case b.CreatedAt != nil:
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	if !all {
		live = live[:1]
	}
	return live, nil
}

// startCopyRun is startRun for a schedule that runs in the running copy: the
// copies are picked, the row is written starting and then running, and a
// goroutine runs the command. It returns the run as it is when the goroutine
// has been started. Without a healthy copy it is ErrNoRunningCopy and nothing
// is written (the clock lets the slot wait, an owner's Run now is refused);
// Nomad being unreachable when the copies are listed is that error, also with
// nothing written. Callers check runBlocker first.
func (s *Service) startCopyRun(ctx context.Context, sched store.ScheduleRecord, trigger string, slot *time.Time) (ScheduleRun, error) {
	listed, err := s.deployer.Copies(ctx, sched.AppID)
	if err != nil {
		return ScheduleRun{}, fmt.Errorf("list copies of %q: %w", sched.AppID, err)
	}
	copies, err := pickCopies(listed, sched.Service, sched.AllCopies)
	if err != nil {
		return ScheduleRun{}, err
	}
	id, err := newID("")
	if err != nil {
		return ScheduleRun{}, err
	}
	run := store.ScheduleRunRecord{ID: id, ScheduleID: sched.ID, AppID: sched.AppID, Service: sched.Service,
		Trigger: trigger, SlotAt: slot, Command: sched.Command, Target: store.TargetCopy,
		Status: store.RunStarting, StartedAt: s.clock.Now().UTC()}
	for _, c := range copies {
		run.Copies = append(run.Copies, c.ID)
	}
	skipped, err := s.claimRun(ctx, sched, &run)
	if err != nil || skipped {
		return runOf(run), err
	}

	// The row says starting: from here the run ends recorded, whatever the
	// caller does, so the goroutine's context is not the caller's.
	bg := s.background()
	if bg == nil {
		bg = context.Background()
	}
	runCtx, cancel := context.WithCancel(bg)
	s.runMu.Lock()
	s.copyRuns[id] = cancel
	s.runMu.Unlock()
	s.schedulesChanged(sched.AppID)

	if err := s.store.SetRunRunning(ctx, id); errors.Is(err, store.ErrNotFound) {
		// The schedule was deleted while the run was being started: no row is
		// left to settle, so nothing runs.
		s.forgetCopyRun(id)
		cancel()
		return ScheduleRun{}, fmt.Errorf("schedule %q: %w", sched.ID, ErrScheduleNotFound)
	} else if err != nil {
		// The command runs; the row says starting until it is settled.
		s.log.Warn("record a schedule's run as running", "run", id, "error", err)
	} else {
		run.Status = store.RunRunning
	}
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.execInCopies(runCtx, cancel, run, sched, copies)
	}()
	return runOf(run), nil
}

// copyFailure is the first copy of a run that didn't do well: what the row will
// say if nothing worse (the limit, a stop) happens.
type copyFailure struct {
	status string
	exit   *int
	words  string
}

// execInCopies runs the command in each of copies, one after the other, and
// settles the run. A copy that fails doesn't keep the others from running; the
// first failure is the run's. The run's limit is one deadline for all of them.
// The output is dropped as it arrives.
func (s *Service) execInCopies(ctx context.Context, cancel context.CancelFunc, run store.ScheduleRunRecord, sched store.ScheduleRecord, copies []Copy) {
	defer func() {
		s.forgetCopyRun(run.ID)
		cancel()
	}()

	limit := s.runLimit(sched)
	execCtx, stopTimer := context.WithTimeout(ctx, limit)
	defer stopTimer()

	var first *copyFailure
	limitReached := false
	for _, c := range copies {
		if execCtx.Err() != nil && ctx.Err() == nil {
			limitReached = true // the limit passed while an earlier copy ran
			break
		}
		res, err := s.execOnSchedule(execCtx, run, c, limit)
		if ctx.Err() != nil {
			break // stopped, or the process is ending: settled below
		}
		var failure *copyFailure
		switch {
		case err == nil && res.TimedOut:
			limitReached = true
		case err == nil && res.ExitCode != nil && *res.ExitCode == 0:
			// ok
		case err == nil && res.ExitCode != nil:
			words := fmt.Sprintf("exit %d", *res.ExitCode)
			if len(copies) > 1 {
				words += " in copy " + shortCopyID(c.ID)
			}
			failure = &copyFailure{status: store.RunFailed, exit: res.ExitCode, words: words}
		case errors.Is(err, deploy.ErrExecFailed):
			failure = &copyFailure{status: store.RunCouldNotStart, words: "the command couldn't start in its copy"}
		default:
			words := execFailure(err)
			if len(copies) > 1 {
				words += " (copy " + shortCopyID(c.ID) + ")"
			}
			failure = &copyFailure{status: store.RunFailed, words: words}
		}
		if limitReached {
			break
		}
		if failure != nil && first == nil {
			first = failure
		}
	}

	// What the run is settled with, from what happened: the stop Caravana asked
	// for first (as for a run with a job), then the limit, then the first
	// failure, else ok.
	settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), s.budget)
	defer cancelSettle()
	st := store.RunSettlement{FinishedAt: s.clock.Now().UTC()}
	switch {
	case ctx.Err() != nil:
		fresh, err := s.store.GetRun(settleCtx, run.ID)
		if err != nil || fresh.StopReason == "" {
			// The schedule was removed, or the process is ending: nobody is left
			// to tell, or the next process settles the run it finds open.
			return
		}
		st.Status, st.Error = runOutcome(fresh, deploy.RunState{}, limit)
	case limitReached:
		st.Status, st.Error = runOutcome(store.ScheduleRunRecord{StopReason: stopLimit}, deploy.RunState{}, limit)
	case first != nil:
		st.Status, st.ExitCode, st.Error = first.status, first.exit, first.words
	default:
		st.Status, st.ExitCode = store.RunOK, new(0)
	}
	s.settleRun(settleCtx, run, st)
}

// execOnSchedule runs the schedule's command in one copy, with an
// app_actions row of its own (action "run", trigger "schedule"), and returns
// what the exec did. The output in the result stays with the caller, which
// drops it: only its exit code and whether it timed out are used. The row
// says what ended the command in words of ours, never the output and never
// Nomad's text.
func (s *Service) execOnSchedule(ctx context.Context, run store.ScheduleRunRecord, c Copy, limit time.Duration) (deploy.ExecResult, error) {
	id, err := newID("act_")
	if err != nil {
		return deploy.ExecResult{}, err
	}
	rec := store.ActionRecord{ID: id, AppID: run.AppID, Action: store.ActionRun, Trigger: string(app.TriggerSchedule),
		Status: store.ActionRunning, CopyID: c.ID, Service: run.Service, Program: programOf(run.Command),
		StartedAt: time.Now().UTC()}
	if err := s.store.InsertAction(ctx, rec); err != nil {
		s.log.Warn("record a schedule's command in a copy", "run", run.ID, "copy", c.ID, "error", err)
	} else {
		s.actionsChanged(run.AppID)
	}

	res, err := s.deployer.Exec(ctx, c.ID, run.Service, run.Command, nil, commandOutputLimit)
	// Whatever came back, only what is below is used: the streams end here.
	res.Stdout, res.Stderr = nil, nil

	switch {
	case err == nil && res.TimedOut:
		s.finishAction(rec.ID, run.AppID, store.ActionFailed, nil, "timed out after "+durationWords(limit))
	case err == nil:
		s.finishAction(rec.ID, run.AppID, store.ActionSucceeded, res.ExitCode, "")
	case errors.Is(err, deploy.ErrExecFailed):
		s.finishAction(rec.ID, run.AppID, store.ActionFailed, nil, "failed to start")
	default:
		s.finishAction(rec.ID, run.AppID, store.ActionFailed, nil, execFailure(err))
	}
	if err != nil {
		// Nomad's text can quote an argument, so only our words are logged.
		s.log.Warn("a schedule's command didn't run in its copy", "run", run.ID, "copy", c.ID, "reason", execFailure(err))
	}
	return res, err
}

// forgetCopyRun takes a run of this process out of the books that keep the
// watcher away from it and let it be cancelled.
func (s *Service) forgetCopyRun(runID string) {
	s.runMu.Lock()
	delete(s.starting, runID)
	delete(s.copyRuns, runID)
	s.runMu.Unlock()
}

// cancelCopyRun ends the exec of a run going in a running copy, when this
// process has it. The run's stop reason is written first by the caller, so
// the goroutine settles it by that reason. A run that isn't here (it ended, or
// belongs to a process that ended) is no error.
func (s *Service) cancelCopyRun(runID string) {
	s.runMu.Lock()
	cancel := s.copyRuns[runID]
	s.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// settleOrphanCopyRun settles a run in a running copy that the watcher finds
// open and that no goroutine of this process has: a process that ended left
// it, and the exec went with the process. There is no job to ask Nomad about
// or to purge. A stop that was asked for is its outcome (the owner stopped
// it, the app was stopped); otherwise it was lost.
func (s *Service) settleOrphanCopyRun(ctx context.Context, r store.ScheduleRunRecord, sched store.ScheduleRecord) {
	st := store.RunSettlement{Status: store.RunFailed, Error: copyRunLost, FinishedAt: s.clock.Now().UTC()}
	if r.StopReason != "" {
		st.Status, st.Error = runOutcome(r, deploy.RunState{}, s.runLimit(sched))
	}
	s.settleRun(ctx, r, st)
}

// shortCopyID is the first 8 characters of a copy's ID, as the console and
// Nomad's own tools shorten it.
func shortCopyID(id string) string {
	return id[:min(8, len(id))]
}
