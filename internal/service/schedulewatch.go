package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The watcher of schedule runs (feature 022, research R2, R3 and R6). It is
// stateless, like watchBackups: every scheduleWatchGap it reads each open run
// from Nomad and does what its state calls for. A run that ended is settled by
// the outcome table (runOutcome), with its output read and masked, and then
// its job is purged with its variable (the secrets). A run going is stopped
// when it passes its limit, and given up when its task hasn't started within
// the start deadline. A run Nomad no longer has is lost. Because nothing is
// kept in memory, the first pass after a restart is the recovery.

// runStopGrace is how long a run that was asked to stop may stay without a
// copy before it is settled by its stop reason: Nomad never placed it, and
// now never will.
const runStopGrace = 30 * time.Second

// watchScheduleRuns follows the open runs, every gap, until ctx ends. It does
// a pass first and then closes settled (when not nil), so the clock can wait
// for the runs a previous process left to be settled before it counts the
// ones going.
func (s *Service) watchScheduleRuns(ctx context.Context, gap time.Duration, settled chan<- struct{}) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		s.followScheduleRuns(ctx)
		if settled != nil {
			close(settled)
			settled = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// followScheduleRuns is one pass of the watcher. Errors are logged; the next
// pass tries again.
func (s *Service) followScheduleRuns(ctx context.Context) {
	s.retryPurges(ctx)
	runs, err := s.store.AllOpenRuns(ctx)
	if err != nil {
		s.log.Warn("follow schedule runs: list open runs", "error", err)
		return
	}
	scheds := map[string]store.ScheduleRecord{}
	for _, r := range runs {
		s.runMu.Lock()
		busy := s.starting[r.ID]
		s.runMu.Unlock()
		if busy {
			continue // startRun is registering it
		}
		sched, ok := scheds[r.ScheduleID]
		if !ok {
			if sched, err = s.store.GetSchedule(ctx, r.ScheduleID); err != nil {
				s.log.Warn("follow schedule runs: read schedule", "schedule", r.ScheduleID, "error", err)
				continue
			}
			scheds[r.ScheduleID] = sched
		}
		s.followRun(ctx, r, sched)
	}
}

// followRun does what one open run's state in Nomad calls for.
func (s *Service) followRun(ctx context.Context, r store.ScheduleRunRecord, sched store.ScheduleRecord) {
	jobID := runJobID(r)
	st, err := s.deployer.ScheduleRunState(ctx, jobID, r.Service)
	now := s.clock.Now().UTC()
	switch {
	case errors.Is(err, deploy.ErrRunGone):
		// Nomad can't say how it ended, and the slot is recorded, so it isn't
		// run again. Its variable may still be there.
		s.settleRun(ctx, r, store.RunSettlement{Status: store.RunFailed, Error: "lost while Caravana was restarting", FinishedAt: now})
		s.purgeRun(ctx, jobID)
		return
	case err != nil:
		s.log.Warn("follow a schedule's run", "run", r.ID, "job", jobID, "error", err)
		return
	}
	if r.Status == store.RunStarting {
		// Registered by a process that ended before it could say so.
		if err := s.store.SetRunRunning(ctx, r.ID); err == nil {
			s.schedulesChanged(r.AppID)
		}
	}

	switch {
	case st.Terminal:
		s.endRun(ctx, r, sched, st, now)
	case r.StopReason != "":
		// Asked to stop. A run Nomad never placed won't be; one it did is
		// stopped again if the first stop didn't reach it.
		if st.AllocID == "" && r.StopRequestedAt != nil && now.Sub(*r.StopRequestedAt) > runStopGrace {
			s.endRun(ctx, r, sched, st, now)
			return
		}
		if st.DesiredStatus != "stop" {
			s.stopRun(ctx, r, jobID)
		}
	case st.TaskStartedAt != nil:
		if now.Sub(*st.TaskStartedAt) > s.runLimit(sched) {
			if err := s.store.SetRunStopIntent(ctx, r.ID, stopLimit, now); err != nil {
				s.log.Warn("record a run's stop at its limit", "run", r.ID, "error", err)
				return
			}
			s.stopRun(ctx, r, jobID)
		}
	case now.Sub(r.StartedAt) > s.scheduleStartDeadline:
		s.settleRun(ctx, r, store.RunSettlement{Status: store.RunCouldNotStart,
			Error: "didn't start within " + durationWords(s.scheduleStartDeadline), FinishedAt: now})
		s.purgeRun(ctx, jobID)
	}
}

// runLimit is how long a run of sched may go: the schedule's stop-after, or
// the lab's shortcut when it is set.
func (s *Service) runLimit(sched store.ScheduleRecord) time.Duration {
	if s.scheduleLimit > 0 {
		return s.scheduleLimit
	}
	return sched.StopAfter
}

// stopRun stops a run's job. A job that is gone already is no error: the next
// pass settles the run.
func (s *Service) stopRun(ctx context.Context, r store.ScheduleRunRecord, jobID string) {
	if err := s.deployer.StopScheduleRun(ctx, jobID); err != nil && !errors.Is(err, deploy.ErrRunGone) {
		s.log.Warn("stop a schedule's run", "run", r.ID, "job", jobID, "reason", r.StopReason, "error", err)
	}
}

// endRun settles a run that ended: its outcome from the table, its output
// (read, masked of the service's secret values, and kept only when both
// worked), then purges its job.
//
// r was read at the start of the pass, Nomad's state after it: a stop the
// owner asked for in between left the job stopped and r without its reason.
// So a stopped job whose row says no reason is read again, and isn't recorded
// as "stopped outside Caravana" when the reason is there now.
func (s *Service) endRun(ctx context.Context, r store.ScheduleRunRecord, sched store.ScheduleRecord, st deploy.RunState, now time.Time) {
	if r.StopReason == "" && st.DesiredStatus == "stop" {
		if fresh, err := s.store.GetRun(ctx, r.ID); err == nil {
			r.StopReason, r.StopRequestedAt = fresh.StopReason, fresh.StopRequestedAt
		}
	}
	status, words := runOutcome(r, st, s.runLimit(sched))
	settlement := store.RunSettlement{Status: status, ExitCode: st.ExitCode, Error: words,
		TaskStartedAt: st.TaskStartedAt, FinishedAt: now}
	if st.AllocID != "" && st.TaskStartedAt != nil {
		settlement.Stdout, settlement.Stderr, settlement.StdoutCut, settlement.StderrCut, settlement.OutputKept =
			s.runOutput(ctx, r, st.AllocID)
	}
	s.settleRun(ctx, r, settlement)
	s.purgeRun(ctx, runJobID(r))
}

// outputTries is how many times the output of a run that just ended is read
// before it is given up: the job is purged right after, so a read that fails
// once (the copy's log file still being closed, a blip reading the secrets)
// would lose the output for good.
const outputTries = 3

// runOutput reads what a run printed and masks the service's secret values
// in it. kept is false, and nothing is returned, when either step failed after
// outputTries tries: an output that couldn't be masked isn't kept. Nothing of
// the output is logged.
func (s *Service) runOutput(ctx context.Context, r store.ScheduleRunRecord, allocID string) (stdout, stderr []byte, stdoutCut, stderrCut, kept bool) {
	var err error
	for try := 1; ; try++ {
		if stdout, stderr, stdoutCut, stderrCut, kept, err = s.readRunOutput(ctx, r, allocID); err == nil {
			return stdout, stderr, stdoutCut, stderrCut, kept
		}
		if try == outputTries || ctx.Err() != nil {
			s.log.Warn("read a schedule run's output; it isn't kept", "run", r.ID, "tries", try, "error", err)
			return nil, nil, false, false, false
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
}

// readRunOutput is one try of runOutput.
func (s *Service) readRunOutput(ctx context.Context, r store.ScheduleRunRecord, allocID string) (stdout, stderr []byte, stdoutCut, stderrCut, kept bool, err error) {
	stdout, stderr, stdoutCut, stderrCut, err = s.deployer.ScheduleRunOutput(ctx, allocID, r.Service)
	if err != nil {
		return nil, nil, false, false, false, fmt.Errorf("read the output: %w", err)
	}
	sets, err := s.secrets.Read(ctx, r.AppID)
	if err != nil {
		return nil, nil, false, false, false, fmt.Errorf("read the secrets to mask the output: %w", err)
	}
	values := sets[r.Service]
	return maskSecrets(stdout, values), maskSecrets(stderr, values), stdoutCut, stderrCut, true, nil
}

// runOutcome is how a run that ended is recorded: its status and the words
// that say why (empty for ok). The stop reason Caravana wrote comes first,
// since a stopped task's exit code (143, 137) says nothing about who stopped
// it; then what Nomad says of the task.
func runOutcome(r store.ScheduleRunRecord, st deploy.RunState, limit time.Duration) (status, words string) {
	switch r.StopReason {
	case stopLimit:
		return store.RunStoppedAtLimit, "stopped after " + durationWords(limit)
	case stopOwner:
		return store.RunStopped, "stopped by the owner"
	case stopAppStopped:
		return store.RunStopped, "the app was stopped"
	case stopScheduleRemoved:
		return store.RunStopped, "the schedule was removed"
	}
	switch {
	case st.OOMKilled:
		return store.RunFailed, "ran out of memory"
	case st.ExitCode != nil && *st.ExitCode == 0:
		return store.RunOK, ""
	case st.StartFailed && st.ExitCode == nil:
		return store.RunCouldNotStart, startFailureWords(r, st)
	case st.DesiredStatus == "stop":
		return store.RunFailed, "stopped outside Caravana"
	case st.ExitCode != nil:
		return store.RunFailed, fmt.Sprintf("exit %d", *st.ExitCode)
	case st.TaskStartedAt == nil:
		return store.RunCouldNotStart, startFailureWords(r, st)
	}
	return store.RunFailed, "its copy was lost"
}

// startFailureWords says why a run couldn't start, in Caravana's words and
// never Nomad's text, which can quote the image or a path. The console shows
// it as "Couldn't start: <words>." (the Schedules page, the run's page).
func startFailureWords(r store.ScheduleRunRecord, st deploy.RunState) string {
	switch st.StartFailure {
	case deploy.StartProgramNotFound:
		program := "program"
		if len(r.Command) > 0 && r.Command[0] != "" {
			program = r.Command[0]
		}
		return "no `" + program + "` in the image"
	case deploy.StartImagePull:
		return "the image couldn't be pulled"
	}
	return "Nomad couldn't start its copy"
}

// durationWords says a limit in words: "5 minutes", "1 minute", or Go's
// notation for one that isn't whole minutes (the lab's short limits).
func durationWords(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "1 minute"
	case d > 0 && d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.String()
}

// purgeRun erases a settled run's job and its variable. When Nomad can't, the
// job is remembered and tried again on each pass: the variable holds the
// service's secrets.
func (s *Service) purgeRun(ctx context.Context, jobID string) {
	if err := s.deployer.PurgeScheduleRun(ctx, jobID); err != nil {
		s.log.Warn("purge a schedule run's job; trying again on the next pass", "job", jobID, "error", err)
		s.runMu.Lock()
		s.unpurged[jobID] = true
		s.runMu.Unlock()
	}
}

// retryPurges tries the purges that failed again.
func (s *Service) retryPurges(ctx context.Context) {
	s.runMu.Lock()
	var jobs []string
	for id := range s.unpurged {
		jobs = append(jobs, id)
	}
	s.runMu.Unlock()
	for _, id := range jobs {
		if err := s.deployer.PurgeScheduleRun(ctx, id); err != nil {
			continue
		}
		s.runMu.Lock()
		delete(s.unpurged, id)
		s.runMu.Unlock()
	}
}
