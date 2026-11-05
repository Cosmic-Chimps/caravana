package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// SQLite backups (feature 029, research R1–R6). A sqlite backup runs inside the
// app's own copy while the service keeps running: Caravana runs a command in
// the copy's caravana-sqlite sidecar that makes a consistent copy of the
// database (VACUUM INTO) and checks it, then one in caravana-upload that gzips
// and uploads it and applies retention. A restore downloads and checks the
// backup in the same two sidecars first, then asks the copy's caravana-volumes
// task to swap the file in, through feature 016's restart: the service is
// stopped only for the renames.
//
// The runs share the volume engine's claim on the app (its deploy lock, held
// until the run is settled, so a deploy waits instead of replacing the copy
// mid-run), its run IDs and its records. The package deploy functions do the
// Nomad side (sqliterun.go).

// hasSQLiteBackup reports whether svc is backed up with the sqlite engine.
func hasSQLiteBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Engine == app.BackupSQLite
}

// runsInCopy reports whether svc's backups run inside its own copy, on
// Caravana's schedule (the volume and sqlite engines), rather than as a Nomad
// periodic job (Postgres).
func runsInCopy(svc app.Service) bool {
	return hasVolumeBackup(svc) || hasSQLiteBackup(svc)
}

// sqliteJobID is the record's job_id for a sqlite run: no Nomad job has it,
// but it says which service and run, as a volume run's does.
func sqliteJobID(appID, service, ts string) string {
	return fmt.Sprintf("%s_%s_sqlite/%s", appID, service, ts)
}

// sqliteRun is a claimed sqlite backup or restore: what the goroutine that
// carries it to its end needs.
type sqliteRun struct {
	app     app.App
	svc     app.Service
	copyID  string
	req     volumeRequest
	dest    deploy.BackupDestination
	started time.Time
	release func() // the app's deploy lock and claim; the owner of the run calls it
}

// prepareSQLiteRun claims the app and checks what may have changed since the
// caller looked, as prepareVolumeRun does: the service is still backed up with
// sqlite, the app isn't stopped, there is a destination, nothing else runs, and
// a copy is running. Nothing is asked of the copy yet. action and object (a
// restore only) go into the request, whose ID names the run.
//
// On an error the claim is given back; on success the caller owns it, and
// run.release gives it back.
func (s *Service) prepareSQLiteRun(ctx context.Context, appID, service, action, object string) (sqliteRun, error) {
	release, err := s.claimVolumeRun(appID)
	if err != nil {
		return sqliteRun{}, err
	}
	handedOver := false
	defer func() {
		if !handedOver {
			release()
		}
	}()

	a, svc, err := s.backedUpService(ctx, appID, service)
	if err != nil {
		return sqliteRun{}, err
	}
	if err := s.refuseIfStopped(ctx, appID); err != nil {
		return sqliteRun{}, err
	}
	if !hasSQLiteBackup(svc) {
		return sqliteRun{}, fmt.Errorf("service %q of %q no longer has a sqlite backup: %w", service, appID, store.ErrNotFound)
	}
	dest, ok, err := s.backupDestination(ctx)
	if err != nil {
		return sqliteRun{}, err
	}
	if !ok {
		return sqliteRun{}, ErrNoBackupDestination
	}
	if busy, err := s.backupBusy(ctx, appID, svc); err != nil {
		return sqliteRun{}, err
	} else if busy {
		return sqliteRun{}, ErrBackupRunning
	}
	copyID, ok, err := s.deployer.RunningCopy(ctx, appID, service)
	if err != nil {
		return sqliteRun{}, err
	}
	if !ok {
		return sqliteRun{}, ErrNoRunningCopy
	}
	req, err := s.freshVolumeRequest(ctx, appID, service, action, object)
	if err != nil {
		return sqliteRun{}, err
	}
	handedOver = true
	return sqliteRun{app: a, svc: svc, copyID: copyID, req: req, dest: dest, started: s.clock.Now().UTC(), release: release}, nil
}

// startSQLiteBackup starts a backup of one service's SQLite database now and
// returns its record, running. trigger (store.BackupTriggerOwner or Timer) is
// recorded on the run. The errors are prepareSQLiteRun's.
func (s *Service) startSQLiteBackup(ctx context.Context, appID, service, trigger string) (store.BackupRecord, error) {
	// A browser that goes away must not cancel the run halfway.
	ctx = context.WithoutCancel(ctx)
	run, err := s.prepareSQLiteRun(ctx, appID, service, deploy.VolumeBackup, "")
	if err != nil {
		return store.BackupRecord{}, err
	}
	rec := store.BackupRecord{AppID: appID, ID: run.req.ID, Service: service, JobID: sqliteJobID(appID, service, run.req.TS),
		Engine: app.BackupSQLite, Status: store.BackupRunning, StartedAt: run.started, Trigger: trigger}
	if err := s.store.SaveBackup(ctx, rec); err != nil {
		run.release()
		return store.BackupRecord{}, err
	}
	s.backupsChanged(appID)
	s.log.Info("sqlite backup started", "app", appID, "service", service, "run", rec.ID, "trigger", trigger)

	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.driveSQLiteBackup(run, rec)
	}()
	return rec, nil
}

// driveSQLiteBackup copies, checks and uploads the database, then records how
// the run ended, gives the lock back and tells the open pages. When Caravana
// is shutting down it leaves the record running: the next start settles it.
func (s *Service) driveSQLiteBackup(run sqliteRun, rec store.BackupRecord) {
	base := s.watchContext(run.app.ID)
	defer func() {
		run.release()
		s.backupsChanged(run.app.ID)
	}()
	ctx, cancel := context.WithTimeout(base, run.req.Limit+s.volume.grace)
	defer cancel()

	if _, err := s.deployer.SQLiteCopy(ctx, run.copyID, run.svc, run.req.ID); err != nil {
		if base.Err() == nil {
			s.settleSQLiteBackup(rec, nomadjob.SQLiteOutput{}, s.sqliteFailure(ctx, run, err))
		}
		return
	}
	out, err := s.deployer.SQLiteUpload(ctx, run.copyID, run.req.ID, run.app.ID, run.svc, run.dest, run.req.TS, run.svc.Backup.KeepOrDefault())
	if err != nil {
		if base.Err() == nil {
			s.settleSQLiteBackup(rec, nomadjob.SQLiteOutput{}, s.sqliteFailure(ctx, run, err))
		}
		return
	}
	s.settleSQLiteBackup(rec, out, "")
}

// sqliteFailure is the message of a run whose step failed with err: the
// script's own words, or Caravana's for the copy going away and the run's time
// running out.
func (s *Service) sqliteFailure(ctx context.Context, run sqliteRun, err error) string {
	var se *deploy.SQLiteError
	switch {
	case errors.As(err, &se):
		return se.Message
	case errors.Is(err, deploy.ErrCopyNotFound), errors.Is(err, deploy.ErrCopyNotRunning):
		return "the copy went away during the run"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("timed out (the limit is %s)", run.req.Limit)
	}
	return err.Error()
}

// settleSQLiteBackup records how a run ended: failed with failure, or
// succeeded with the object, its size and, when retention failed after a good
// upload, why, as the message. The backups retention removed are marked
// expired first. The write is retried, as a volume run's is.
func (s *Service) settleSQLiteBackup(rec store.BackupRecord, out nomadjob.SQLiteOutput, failure string) {
	now := time.Now().UTC()
	rec.FinishedAt = &now
	if failure != "" {
		rec.Status, rec.Message = store.BackupFailed, failure
	} else {
		rec.Status, rec.Message, rec.Object, rec.Bytes = store.BackupSucceeded, out.Warning, out.Object, out.Bytes
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The record goes last: once it isn't running, everything else is there.
	var expired []string
	for _, ts := range out.Expired {
		expired = append(expired, rec.Service+"-"+ts)
	}
	if err := s.store.ExpireBackups(ctx, rec.AppID, expired); err != nil {
		s.log.Warn("record the backups retention removed", "app", rec.AppID, "error", err)
	}
	if !s.saveOutcome(ctx, "backup", rec.AppID, rec.ID, func(ctx context.Context) error { return s.store.SaveBackup(ctx, rec) }) {
		return
	}
	s.log.Info("sqlite backup finished", "app", rec.AppID, "service", rec.Service, "run", rec.ID, "status", rec.Status, "message", rec.Message)
}

// startSQLiteRestore starts the restore of the sqlite backup b into the
// running service and returns the restore's record, running. The errors are
// prepareSQLiteRun's.
func (s *Service) startSQLiteRestore(ctx context.Context, appID string, b store.BackupRecord) (store.RestoreRecord, error) {
	ctx = context.WithoutCancel(ctx)
	run, err := s.prepareSQLiteRun(ctx, appID, b.Service, deploy.VolumeRestoreSQLite, b.Object)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	rec := store.RestoreRecord{ID: restoreRecordID(appID, run.req.ID), AppID: appID, Service: b.Service, BackupID: b.ID,
		Engine: app.BackupSQLite, Status: store.BackupRunning, CreatedAt: run.started}
	if err := s.store.SaveRestore(ctx, rec); err != nil {
		run.release()
		return store.RestoreRecord{}, err
	}
	s.backupsChanged(appID)
	s.log.Info("sqlite restore started", "app", appID, "service", b.Service, "run", rec.ID, "backup", b.ID)

	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.driveSQLiteRestore(run, rec)
	}()
	return rec, nil
}

// driveSQLiteRestore carries a restore to its record. While the service still
// runs, it downloads the backup and stages it, checked, on the database's
// volume; a failure there ends the restore with nothing stopped. Then it asks
// the copy's caravana-volumes task for the swap, restarts the copy and reads
// the result, as a volume run does. Whatever happened, it clears the request
// (when it wrote one), gives the lock back and tells the open pages. When
// Caravana is shutting down it leaves the record running: the next start
// settles it (resumeVolumeRuns).
func (s *Service) driveSQLiteRestore(run sqliteRun, rec store.RestoreRecord) {
	base := s.watchContext(run.app.ID)
	requested := false
	defer func() {
		if requested && base.Err() == nil {
			s.clearVolumeRequest(run.app.ID, run.svc.Name)
		}
		run.release()
		s.backupsChanged(run.app.ID)
	}()
	settle := func(out volumeOutcome) {
		if base.Err() == nil {
			s.settleVolumeRestore(rec, out)
		}
	}
	ctx, cancel := context.WithTimeout(base, run.req.Limit+s.volume.grace)
	defer cancel()

	if _, err := s.deployer.SQLiteDownload(ctx, run.copyID, run.req.ID, run.req.Object, run.dest); err != nil {
		settle(failedOutcome(s.sqliteFailure(ctx, run, err), nil))
		return
	}
	if err := s.deployer.SQLiteStage(ctx, run.copyID, run.svc, run.req.ID); err != nil {
		settle(failedOutcome(s.sqliteFailure(ctx, run, err), nil))
		return
	}

	vol, inVolume, _ := run.svc.SQLiteFile()
	req := run.req.forNomad()
	req.File = nomadjob.SQLiteRestoreFile(vol.Name, inVolume)
	requested = true
	if err := s.deployer.WriteVolumeVariable(ctx, run.app, run.svc, nil, 0, req); err != nil {
		settle(failedOutcome("couldn't ask the copy for the swap: "+err.Error(), nil))
		return
	}
	if err := s.deployer.WaitRequestRendered(ctx, run.copyID, req.ID); err != nil && !errors.Is(err, deploy.ErrRequestNotRendered) {
		settle(failedOutcome("couldn't ask the copy for the swap: "+err.Error(), nil))
		return
	}
	// A copy that never showed the request is restarted all the same: the
	// template renders afresh as the task starts (see prepareVolumeRun).

	restartedAt := time.Now()
	if err := s.deployer.RestartCopy(base, run.copyID); err != nil {
		settle(failedOutcome("couldn't restart the service: "+err.Error(), nil))
		return
	}
	res, found, aborted, failure := s.awaitVolumeResult(base, run.copyID, req.ID, run.req.Limit+s.volume.grace)
	switch {
	case aborted:
	case !found:
		waited := time.Since(restartedAt)
		settle(failedOutcome(failure, &waited))
	default:
		settle(outcomeOf(res, s.awaitStop(base, run.copyID, run.svc.Name, restartedAt)))
	}
}
