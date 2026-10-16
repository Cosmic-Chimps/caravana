package service

import (
	"context"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// resume picks up every deployment a previous run left pending or running
// (research R10). Each is handled in its own goroutine, so startup never
// waits on Nomad; while Nomad is unreachable they keep retrying.
func (s *Service) resume(ctx context.Context) error {
	unfinished, err := s.store.ListUnfinishedDeployments(ctx)
	if err != nil {
		return fmt.Errorf("resume deployments: %w", err)
	}
	for _, rec := range unfinished {
		s.log.Info("resuming deployment", "app", rec.AppID, "deployment", rec.Deployment.ID, "status", rec.Status)
		// The app's own watch context, so deleting the app stops this one too.
		watch := s.watchContext(rec.AppID)
		s.watchers.Add(1)
		go func() {
			defer s.watchers.Done()
			s.settle(watch, rec)
		}()
	}
	return nil
}

// settle follows one unfinished deployment to its end.
func (s *Service) settle(ctx context.Context, rec store.DeploymentRecord) {
	id := rec.Deployment.ID
	running := store.DeploymentUpdate{Status: store.StatusRunning}
	if rec.JobVersion != nil {
		s.follow(ctx, rec.AppID, id, rec.JobVersion, running)
		return
	}
	// No version recorded: either Caravana stopped before registering, or
	// between registering and recording the version. The job carries the
	// deployment ID, so Nomad can tell which.
	v, found := s.findVersion(ctx, rec.AppID, id)
	if ctx.Err() != nil {
		return
	}
	if !found {
		msg := "Caravana stopped before registering this deployment"
		if rec.Status == store.StatusRunning {
			msg = "Nomad has no job version for this deployment"
		}
		s.finish(rec.AppID, id, store.StatusFailed, msg)
		return
	}
	s.follow(ctx, rec.AppID, id, &v, running)
}

// unsettledRun is a volume run, a backup or a restore, that a previous run of
// Caravana left running.
type unsettledRun struct {
	// requestID names the run's request and its result file.
	requestID string
	service   string
	startedAt time.Time
	// settle records how the run ended, in its backup or restore record.
	settle func(volumeOutcome)
}

// resumeVolumeRuns settles each volume backup and restore a previous run of
// Caravana left running (research R7), each app in the background: Start never
// waits for Nomad.
func (s *Service) resumeVolumeRuns(ctx context.Context) error {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return fmt.Errorf("resume volume runs: %w", err)
	}
	for _, rec := range apps {
		appID := rec.App.ID
		backups, err := s.store.ListBackups(ctx, appID, 0)
		if err != nil {
			return fmt.Errorf("resume volume runs: %w", err)
		}
		restores, err := s.store.ListRestores(ctx, appID, 0)
		if err != nil {
			return fmt.Errorf("resume volume runs: %w", err)
		}
		var running []unsettledRun
		for _, b := range backups {
			if b.Engine == app.BackupVolume && b.Status == store.BackupRunning {
				running = append(running, unsettledRun{requestID: b.ID, service: b.Service, startedAt: b.StartedAt,
					settle: func(out volumeOutcome) { s.settleVolumeBackup(b, out) }})
			}
		}
		for _, r := range restores {
			if r.Engine == app.BackupVolume && r.Status == store.BackupRunning {
				running = append(running, unsettledRun{requestID: requestIDOfRestore(appID, r.ID), service: r.Service, startedAt: r.CreatedAt,
					settle: func(out volumeOutcome) { s.settleVolumeRestore(r, out) }})
			}
		}
		if len(running) == 0 {
			continue
		}
		s.log.Info("resuming volume runs", "app", appID, "runs", len(running))
		s.watchers.Add(1)
		go func() {
			defer s.watchers.Done()
			s.settleVolumeRuns(s.watchContext(appID), appID, running)
		}()
	}
	return nil
}

// settleVolumeRuns settles one app's unfinished runs, one after another,
// under one claim of the app: the run may still be going in the copy, and a
// deploy must wait for it, as for any run. Per run: the result file, if the
// copy still has it, within what is left of the run's time; otherwise the run
// failed with Caravana's restart. Either way its request is cleared.
func (s *Service) settleVolumeRuns(ctx context.Context, appID string, runs []unsettledRun) {
	release, err := s.claimVolumeRun(appID)
	if err != nil {
		s.log.Warn("resume volume runs: can't take the app", "app", appID, "error", err)
		return
	}
	defer func() {
		release()
		s.backupsChanged(appID)
	}()
	for _, run := range runs {
		s.settleOneVolumeRun(ctx, appID, run)
		if ctx.Err() != nil {
			return
		}
		s.clearVolumeRequest(appID, run.service)
	}
}

// restartedMessage is the failure of a run that Caravana's own restart cut off
// and that left no result.
const restartedMessage = "Caravana restarted during the run"

func (s *Service) settleOneVolumeRun(ctx context.Context, appID string, run unsettledRun) {
	// What is left of the time the run was given, from when it was recorded.
	until := run.startedAt.Add(s.volume.limit + s.volume.grace)
	copyID, ok := s.liveCopy(ctx, appID, run.service, until)
	if !ok {
		if ctx.Err() == nil {
			run.settle(failedOutcome(restartedMessage, nil))
		}
		return
	}
	res, found, aborted, _ := s.awaitVolumeResult(ctx, copyID, run.requestID, max(time.Until(until), time.Millisecond))
	switch {
	case aborted:
	case found:
		run.settle(outcomeOf(res, s.awaitStop(ctx, copyID, run.service, run.startedAt)))
	default:
		run.settle(failedOutcome(restartedMessage, nil))
	}
}

// liveCopy is the ID of the service's newest copy that hasn't ended, whatever
// it is doing: a run in progress has its service stopped, and that is not
// "running" to RunningCopy. When Nomad can't be read (it may be starting up
// too) it tries again until the time is up, and then says there is none.
func (s *Service) liveCopy(ctx context.Context, appID, service string, until time.Time) (string, bool) {
	for {
		copies, err := s.deployer.Copies(ctx, appID) // newest version first
		if err == nil {
			for _, c := range copies {
				if c.Service == service && !c.Terminal {
					return c.ID, true
				}
			}
			return "", false
		}
		if ctx.Err() != nil || !time.Now().Before(until) {
			return "", false
		}
		s.log.Warn("resume volume runs: read copies; retrying", "app", appID, "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(s.volume.poll):
		}
	}
}
