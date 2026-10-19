package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Volume backups (feature 016, research R1–R3, R6, R7). A volume backup runs
// inside the app's own copy, as its caravana-volumes task (package nomadjob).
// Caravana asks for a run by writing a request into the task's Nomad Variable,
// waits until the copy has rendered it, restarts the copy's tasks (the service
// stops cleanly, the task archives its volumes, the service starts again) and
// reads the result file the task leaves. The job doesn't change, so a backup
// is no Nomad deployment and no new version. A restore is the same run with a
// different request (the task loads an earlier backup's archives instead of
// writing new ones), so the two share everything but their records. This file
// is the run: who may start one, what it records, and how it ends.

// ErrNoRunningCopy is the refusal of a volume run when the service has no copy
// to restart: it hasn't been deployed, is still starting, or has stopped.
var ErrNoRunningCopy = errors.New("the service has no running copy")

// hasVolumeBackup reports whether svc is backed up with the volume engine.
func hasVolumeBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Engine == app.BackupVolume
}

// volumeRequest is one run Caravana asks a copy's task for.
type volumeRequest struct {
	// ID is "<service>-<unix seconds>", like a Postgres run's ID: it names the
	// record and the result file.
	ID string
	// Action is deploy.VolumeBackup or deploy.VolumeRestore.
	Action string
	// Object is the backup a restore loads. Empty for a backup.
	Object string
	// TS is the unix seconds of the start, as text: the directory a backup's
	// archives go into, and the end of ID.
	TS string
	// NotAfter is when the task stops honoring the request. It outlasts the
	// run's limit by a grace, which is also how long Caravana waits for a
	// result.
	NotAfter time.Time
	// Limit is how long the task lets the run take.
	Limit time.Duration
}

// newVolumeRequest makes the request of a run started at now for service.
func newVolumeRequest(service, action, object string, now time.Time, limit, grace time.Duration) volumeRequest {
	ts := now.Unix()
	return volumeRequest{
		ID:       fmt.Sprintf("%s-%d", service, ts),
		Action:   action,
		Object:   object,
		TS:       strconv.FormatInt(ts, 10),
		NotAfter: now.Add(limit + grace),
		Limit:    limit,
	}
}

// forNomad is the request as package deploy writes it into the variable.
func (r volumeRequest) forNomad() *deploy.VolumeRequest {
	return &deploy.VolumeRequest{ID: r.ID, Action: r.Action, TS: r.TS, Object: r.Object, NotAfter: r.NotAfter, Limit: r.Limit}
}

// volumeJobID is the record's job_id for a volume run: no Nomad job has it,
// but it says which service and run, as a Postgres run's does.
func volumeJobID(appID, service, ts string) string {
	return fmt.Sprintf("%s_%s_volumes/%s", appID, service, ts)
}

// claimVolumeRun claims the app for a volume run and takes its deploy lock;
// release gives both back, and may be called more than once.
//
// The claim comes first and is checked without waiting: the lock is held for
// the whole run, so a second run that simply waited for it would hang for as
// long as the first lasts. It is refused with ErrBackupRunning instead (the
// scheduler tries again on its next pass). Taking the lock then waits for a
// deploy in progress, the same as any deploy does. An app deleted while we
// waited is store.ErrNotFound.
func (s *Service) claimVolumeRun(appID string) (release func(), err error) {
	state := s.stateOf(appID)
	s.mu.Lock()
	if state.volumeRun {
		s.mu.Unlock()
		return nil, ErrBackupRunning
	}
	state.volumeRun = true
	s.mu.Unlock()

	state.mu.Lock()
	var once sync.Once
	release = func() {
		once.Do(func() {
			s.mu.Lock()
			state.volumeRun = false
			s.mu.Unlock()
			state.mu.Unlock() // a sync.Mutex may be unlocked by another goroutine than the one that locked it
		})
	}
	if state.deleted {
		release()
		return nil, fmt.Errorf("app %q was deleted: %w", appID, store.ErrNotFound)
	}
	return release, nil
}

// volumeRun is a run (a backup or a restore) whose request is out: what the
// goroutine that carries it to its end needs.
type volumeRun struct {
	appID, service string
	copyID         string
	req            volumeRequest
	started        time.Time
	release        func() // the app's deploy lock and claim; the owner of the run calls it

	// settle records how the run ended, in the record of a backup or a restore.
	// The caller of prepareVolumeRun sets it once its running record is saved.
	settle func(volumeOutcome)
}

// prepareVolumeRun is the first half of a backup or a restore of one service's
// volumes, the part the two share: it claims the app, checks what may have
// changed since the caller looked, finds the running copy, writes the request
// into the service's variable and waits until the copy has rendered it.
// action is deploy.VolumeBackup or deploy.VolumeRestore, and object (a restore
// only) the backup to load. The caller then saves its own running record and
// hands the run to startVolumeRun.
//
// It holds the app's deploy lock from before it writes the request until the
// run is settled, which is what keeps a deploy from registering a new job
// while the service is stopped, and DeleteApp from deleting the volumes under
// the archive. On an error it has released the lock and cleared the request;
// on success the caller owns both, and run.release gives the lock back.
//
// The errors are ErrBackupRunning (a volume run of the app has the lock, or the
// service's records say running), ErrNoBackupDestination, ErrNoRunningCopy and
// Nomad's. A copy that never shows the request is restarted all the same.
func (s *Service) prepareVolumeRun(ctx context.Context, appID, service, action, object string) (volumeRun, error) {
	release, err := s.claimVolumeRun(appID)
	if err != nil {
		return volumeRun{}, err
	}
	handedOver := false
	defer func() {
		if !handedOver {
			release()
		}
	}()

	// What was checked before the lock may have changed while a deploy held it
	// (or a stop: feature 020.5).
	a, svc, err := s.backedUpService(ctx, appID, service)
	if err != nil {
		return volumeRun{}, err
	}
	if err := s.refuseIfStopped(ctx, appID); err != nil {
		return volumeRun{}, err
	}
	if !hasVolumeBackup(svc) {
		return volumeRun{}, fmt.Errorf("service %q of %q no longer has a volume backup: %w", service, appID, store.ErrNotFound)
	}
	dest, ok, err := s.backupDestination(ctx)
	if err != nil {
		return volumeRun{}, err
	}
	if !ok {
		return volumeRun{}, ErrNoBackupDestination
	}
	if busy, err := s.backupBusy(ctx, appID, svc); err != nil {
		return volumeRun{}, err
	} else if busy {
		return volumeRun{}, ErrBackupRunning
	}
	copyID, ok, err := s.deployer.RunningCopy(ctx, appID, service)
	if err != nil {
		return volumeRun{}, err
	}
	if !ok {
		return volumeRun{}, ErrNoRunningCopy
	}

	req, err := s.freshVolumeRequest(ctx, appID, service, action, object)
	if err != nil {
		return volumeRun{}, err
	}
	if err := s.deployer.WriteVolumeVariable(ctx, a, svc, &dest, svc.Backup.KeepOrDefault(), req.forNomad()); err != nil {
		s.clearVolumeRequest(appID, service) // a write that failed may still have gone through
		return volumeRun{}, err
	}
	run := volumeRun{appID: appID, service: service, copyID: copyID, req: req, started: s.clock.Now().UTC(), release: release}

	if err := s.deployer.WaitRequestRendered(ctx, copyID, req.ID); err != nil {
		if !errors.Is(err, deploy.ErrRequestNotRendered) {
			s.clearVolumeRequest(appID, service)
			return volumeRun{}, err
		}
		// Nomad stops rendering a finished prestart task's template once its
		// agent restarts (lab, 2026-10-19), so a copy that started before the
		// agent's last restart never shows the request. Restarting the copy
		// renders it fresh as the task starts, and the result file says
		// whether this request ran: it is named after it, and an older
		// request is past its NOT_AFTER. So the run goes on.
		s.log.Info("the copy didn't show the volume request; restarting it renders it", "app", appID, "service", service,
			"copy", copyID, "run", req.ID)
	}
	handedOver = true
	return run, nil
}

// startVolumeRun hands a prepared run, whose running record is saved and whose
// settle is set, to a goroutine that restarts the copy and follows the run to
// its end. The goroutine is tracked in s.watchers, and gives the lock back and
// clears the request when the run ends.
func (s *Service) startVolumeRun(run volumeRun) {
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.driveVolumeRun(run)
	}()
}

// startVolumeBackup starts a backup of one service's volumes now, and returns
// its record, running. trigger ("manual" or "schedule") is for the log. See
// prepareVolumeRun for what it checks and holds; a copy that never shows the
// request is restarted all the same.
func (s *Service) startVolumeBackup(ctx context.Context, appID, service, trigger string) (store.BackupRecord, error) {
	// Once the request is written the run is under way: a browser that goes
	// away while it waits for the copy must not cancel it halfway.
	// WithoutCancel keeps the request's values but drops its cancellation.
	ctx = context.WithoutCancel(ctx)
	run, err := s.prepareVolumeRun(ctx, appID, service, deploy.VolumeBackup, "")
	if err != nil {
		return store.BackupRecord{}, err
	}
	rec := store.BackupRecord{AppID: appID, ID: run.req.ID, Service: service, JobID: volumeJobID(appID, service, run.req.TS),
		Engine: app.BackupVolume, Status: store.BackupRunning, StartedAt: run.started}

	if err := s.store.SaveBackup(ctx, rec); err != nil {
		s.clearVolumeRequest(appID, service)
		run.release()
		return store.BackupRecord{}, err
	}
	s.backupsChanged(appID)
	s.log.Info("volume backup started", "app", appID, "service", service, "run", rec.ID, "trigger", trigger)

	run.settle = func(out volumeOutcome) { s.settleVolumeBackup(rec, out) }
	s.startVolumeRun(run)
	return rec, nil
}

// startVolumeRestore starts the restore of the volume backup b, whose service
// and volumes it loads back into the running service: the copy restarts, its
// task replaces the volumes' files with the archives', and the service starts
// again. It returns the restore's record, running, with the same errors and
// holds as startVolumeBackup (see prepareVolumeRun).
//
// The restore's record ID is the request's ID with the app's: request IDs
// are only unique within an app, restore IDs across all of them.
func (s *Service) startVolumeRestore(ctx context.Context, appID string, b store.BackupRecord) (store.RestoreRecord, error) {
	ctx = context.WithoutCancel(ctx) // as for a backup: the client going away doesn't stop it
	run, err := s.prepareVolumeRun(ctx, appID, b.Service, deploy.VolumeRestore, b.Object)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	rec := store.RestoreRecord{ID: restoreRecordID(appID, run.req.ID), AppID: appID, Service: b.Service, BackupID: b.ID,
		Engine: app.BackupVolume, Status: store.BackupRunning, CreatedAt: run.started}

	if err := s.store.SaveRestore(ctx, rec); err != nil {
		s.clearVolumeRequest(appID, b.Service)
		run.release()
		return store.RestoreRecord{}, err
	}
	s.backupsChanged(appID)
	s.log.Info("volume restore started", "app", appID, "service", b.Service, "run", rec.ID, "backup", b.ID)

	run.settle = func(out volumeOutcome) { s.settleVolumeRestore(rec, out) }
	s.startVolumeRun(run)
	return rec, nil
}

// restoreRecordID is the ID of the restore record of the run whose request ID
// is requestID, and requestIDOfRestore reads it back (resume needs the request
// to find its result file).
func restoreRecordID(appID, requestID string) string { return appID + "-" + requestID }

func requestIDOfRestore(appID, restoreID string) string {
	return strings.TrimPrefix(restoreID, appID+"-")
}

// freshVolumeRequest is a request for a run started now, whose ID no record has
// yet: two runs of one service in the same second would share an ID, and with
// it a result file, whether they are two backups, two restores or one of each.
// It moves on by a second until the ID is free.
func (s *Service) freshVolumeRequest(ctx context.Context, appID, service, action, object string) (volumeRequest, error) {
	restores, err := s.store.ListRestores(ctx, appID, 0)
	if err != nil {
		return volumeRequest{}, err
	}
	restoreIDs := map[string]bool{}
	for _, r := range restores {
		restoreIDs[r.ID] = true
	}
	now := s.clock.Now()
	for range 100 {
		req := newVolumeRequest(service, action, object, now, s.volume.limit, s.volume.grace)
		_, err := s.store.GetBackup(ctx, appID, req.ID)
		if errors.Is(err, store.ErrNotFound) && !restoreIDs[restoreRecordID(appID, req.ID)] {
			return req, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return volumeRequest{}, err
		}
		now = now.Add(time.Second)
	}
	return volumeRequest{}, fmt.Errorf("volume %s of %q: no free run ID near %s", action, service, now.Format(time.RFC3339))
}

// clearVolumeRequest takes the request out of the service's variable. It uses
// a context of its own: it runs when the caller's has ended, too. A failure is
// logged and not worse than it sounds: the request expires by itself
// (NOT_AFTER), and the next deploy clears it.
func (s *Service) clearVolumeRequest(appID, service string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.deployer.ClearVolumeRequest(ctx, appID, service); err != nil {
		s.log.Warn("clear the volume run's request; it expires by itself", "app", appID, "service", service, "error", err)
	}
}

// volumeResult is what a run's result file says, in the service's own types.
type volumeResult struct {
	succeeded bool
	message   string
	object    string
	volumes   []store.VolumeArchive
	expired   []string // timestamps retention removed
}

// readVolumeResult reads the run's result from its copy. found is false until
// the task has written it.
func (s *Service) readVolumeResult(ctx context.Context, copyID, requestID string) (res volumeResult, found bool, err error) {
	r, found, err := s.deployer.VolumeResult(ctx, copyID, requestID)
	if err != nil || !found {
		return volumeResult{}, false, err
	}
	res = volumeResult{succeeded: r.Succeeded, message: r.Message, object: r.Object, expired: r.Expired}
	for _, v := range r.Volumes {
		res.volumes = append(res.volumes, store.VolumeArchive{Name: v.Name, Bytes: v.Bytes, SizeKB: v.SizeKB})
	}
	return res, true, nil
}

// volumeOutcome is how a run ended, for its record.
type volumeOutcome struct {
	status  string // store.BackupSucceeded or store.BackupFailed
	message string
	result  *volumeResult // nil when there is no result file
	stopped *time.Duration
}

func failedOutcome(message string, stopped *time.Duration) volumeOutcome {
	return volumeOutcome{status: store.BackupFailed, message: message, stopped: stopped}
}

// awaitVolumeResult polls the copy for the run's result until wait is up. It
// gives a failed outcome (and found false) when the copy is gone or nothing
// came in time, and aborted when ctx ended first: Caravana is shutting down,
// and the next start settles the run.
func (s *Service) awaitVolumeResult(ctx context.Context, copyID, requestID string, wait time.Duration) (res volumeResult, found, aborted bool, failure string) {
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var lastErr error
	for {
		r, ok, err := s.readVolumeResult(waitCtx, copyID, requestID)
		switch {
		case err == nil && ok:
			return r, true, false, ""
		case errors.Is(err, deploy.ErrCopyNotFound):
			return volumeResult{}, false, false, "the copy went away during the run"
		case err != nil && waitCtx.Err() == nil:
			lastErr = err
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return volumeResult{}, false, true, ""
			}
			msg := fmt.Sprintf("timed out: no result after %s", wait.Round(time.Second))
			if lastErr != nil {
				msg += " (the last read failed: " + lastErr.Error() + ")"
			}
			return volumeResult{}, false, false, msg
		case <-time.After(s.volume.poll):
		}
	}
}

// awaitStop is how long the service's task was stopped by the restart made at
// since: from its "Terminated" event to its next "Started". Until the task has
// come back there is no answer, so it waits up to volume.comeBack; when the
// task hasn't come back by then, the answer is how long Caravana has waited,
// and the needs-you list picks the copy up.
func (s *Service) awaitStop(ctx context.Context, copyID, service string, since time.Time) *time.Duration {
	deadline := time.Now().Add(s.volume.comeBack)
	for {
		d, ok, err := s.deployer.StopDuration(ctx, copyID, service, since)
		if err == nil && ok {
			return &d
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			waited := time.Since(since)
			return &waited
		}
		select {
		case <-ctx.Done():
		case <-time.After(s.volume.poll):
		}
	}
}

// driveVolumeRun carries a run from the restart to its record, and then,
// whatever happened, clears the request, gives the lock back and tells the open
// pages. If Caravana is shutting down it leaves the request and the record as
// they are: the next start settles the run (resumeVolumeRuns).
func (s *Service) driveVolumeRun(run volumeRun) {
	// The app's own context: it ends at shutdown, and a DeleteApp can't end
	// it while this run holds the lock it needs first.
	ctx := s.watchContext(run.appID)
	defer func() {
		if ctx.Err() == nil {
			s.clearVolumeRequest(run.appID, run.service)
		}
		run.release()
		s.backupsChanged(run.appID)
	}()

	restartedAt := time.Now()
	if err := s.deployer.RestartCopy(ctx, run.copyID); err != nil {
		if ctx.Err() == nil {
			run.settle(failedOutcome("couldn't restart the service: "+err.Error(), nil))
		}
		return
	}
	res, found, aborted, failure := s.awaitVolumeResult(ctx, run.copyID, run.req.ID, run.req.Limit+s.volume.grace)
	if aborted {
		return
	}
	if !found {
		waited := time.Since(restartedAt)
		run.settle(failedOutcome(failure, &waited))
		return
	}
	run.settle(outcomeOf(res, s.awaitStop(ctx, run.copyID, run.service, restartedAt)))
}

// outcomeOf is the outcome of a run whose result file says res.
func outcomeOf(res volumeResult, stopped *time.Duration) volumeOutcome {
	out := volumeOutcome{status: store.BackupSucceeded, message: res.message, result: &res, stopped: stopped}
	if !res.succeeded {
		out.status = store.BackupFailed
	}
	return out
}

// settleVolumeBackup records how a run ended: the status and message, a
// success's object, volumes and size, how long the service was stopped, the
// earlier backups retention removed, and the volumes' sizes. The write is
// retried (like a deployment's outcome) so one failed write doesn't leave the
// run "running" for good.
func (s *Service) settleVolumeBackup(rec store.BackupRecord, out volumeOutcome) {
	now := time.Now().UTC()
	rec.Status, rec.Message, rec.FinishedAt, rec.Stopped = out.status, out.message, &now, out.stopped
	if out.result != nil && out.status == store.BackupSucceeded {
		rec.Object, rec.Volumes = out.result.object, out.result.volumes
		for _, v := range out.result.volumes {
			rec.Bytes += v.Bytes
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The record goes last: once it isn't running, everything else is there.
	if out.result != nil {
		var expired []string
		for _, ts := range out.result.expired {
			expired = append(expired, rec.Service+"-"+ts)
		}
		if err := s.store.ExpireBackups(ctx, rec.AppID, expired); err != nil {
			s.log.Warn("record the backups retention removed", "app", rec.AppID, "error", err)
		}
		if out.status == store.BackupSucceeded {
			for _, v := range out.result.volumes {
				if err := s.store.SaveVolumeSize(ctx, rec.AppID, v.Name, v.SizeKB, now); err != nil {
					s.log.Warn("record a volume's size", "app", rec.AppID, "volume", v.Name, "error", err)
				}
			}
		}
	}
	if !s.saveOutcome(ctx, "backup", rec.AppID, rec.ID, func(ctx context.Context) error { return s.store.SaveBackup(ctx, rec) }) {
		return
	}
	s.log.Info("volume backup finished", "app", rec.AppID, "service", rec.Service, "run", rec.ID, "status", out.status, "message", out.message)
}

// settleVolumeRestore records how a restore ended: the status and message (a
// success may still carry one, when the old files couldn't be cleaned up), and
// how long the service was stopped. The volumes' sizes are measured by the
// task at the service's next start, so nothing is written for them here. The
// write is retried, as a backup's is.
func (s *Service) settleVolumeRestore(rec store.RestoreRecord, out volumeOutcome) {
	now := time.Now().UTC()
	rec.Status, rec.Message, rec.FinishedAt, rec.Stopped = out.status, out.message, &now, out.stopped

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !s.saveOutcome(ctx, "restore", rec.AppID, rec.ID, func(ctx context.Context) error { return s.store.SaveRestore(ctx, rec) }) {
		return
	}
	s.log.Info("volume restore finished", "app", rec.AppID, "service", rec.Service, "run", rec.ID, "status", out.status, "message", out.message)
}

// saveOutcome calls save until it works or ctx ends, so one failed write doesn't
// leave a finished run "running" for good. kind ("backup" or "restore") and id
// are for the log. It reports whether the record was saved.
func (s *Service) saveOutcome(ctx context.Context, kind, appID, id string, save func(context.Context) error) bool {
	for {
		err := save(ctx)
		if err == nil {
			return true
		}
		s.log.Warn("record the volume "+kind+"'s outcome; retrying", "app", appID, "run", id, "error", err)
		select {
		case <-time.After(s.retryGap):
		case <-ctx.Done():
			s.log.Error("record the volume "+kind+"'s outcome", "app", appID, "run", id, "error", err)
			return false
		}
	}
}
