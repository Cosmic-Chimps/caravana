package service

import (
	"context"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// OpenSearch backups (feature 061). An opensearch backup runs inside the app's own
// copy while the cluster keeps running, and never stops or restarts it: the
// cluster takes a snapshot of itself through its API, into a fresh folder of the
// service's repository volume (a command in the service's own task), and the
// copy's caravana-upload sidecar tars the folder to the destination and applies
// retention. A restore extracts the backup into a fresh folder there and asks the
// cluster to restore it, closing the indices it replaces.
//
// The runs share the sqlite engine's claim on the app, run IDs, records,
// scheduler, pins and copies (sqlitebackup.go); the Nomad side is in package
// deploy (opensearchrun.go).

// hasOpenSearchBackup reports whether svc is backed up with the opensearch engine.
func hasOpenSearchBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Engine == app.BackupOpenSearch
}

// openSearchJobID is the record's job_id for an opensearch run: no Nomad job has
// it, but it says which service and run.
func openSearchJobID(appID, service, ts string) string {
	return fmt.Sprintf("%s_%s_opensearch/%s", appID, service, ts)
}

// openSearchRun is a claimed opensearch backup or restore: the claim and what
// the cluster is logged in with.
type openSearchRun struct {
	sidecarRun
	login deploy.OpenSearchLogin
}

// openSearchLogin is the account the service's snapshot and restore calls log in
// with: app.FindDumpLogin's, from the service's environment first and then its
// secrets, read now and not at deploy time (a restart, a refreshed secret or a
// password changed since all count). No login is found for a cluster with its
// security plugin off, which is fine.
func (s *Service) openSearchLogin(ctx context.Context, a app.App, svc app.Service) (deploy.OpenSearchLogin, error) {
	sets, err := s.secrets.Read(ctx, a.ID)
	if err != nil {
		return deploy.OpenSearchLogin{}, fmt.Errorf("read the secrets of %q: %w", a.ID, err)
	}
	login, _ := app.FindDumpLogin(app.BackupOpenSearch, func(name string) (string, bool) {
		for _, e := range svc.Env {
			if e.Name == name {
				return e.Value, true
			}
		}
		v, ok := sets[svc.Name][name]
		return v, ok
	})
	return deploy.OpenSearchLogin{User: login.User, Password: login.Password}, nil
}

// prepareOpenSearchRun is prepareSidecarRun for an opensearch service, with the
// login read under the claim.
func (s *Service) prepareOpenSearchRun(ctx context.Context, appID, service, action, object, from string) (openSearchRun, error) {
	run, err := s.prepareSidecarRun(ctx, app.BackupOpenSearch, appID, service, action, object, from)
	if err != nil {
		return openSearchRun{}, err
	}
	login, err := s.openSearchLogin(ctx, run.app, run.svc)
	if err != nil {
		run.release()
		return openSearchRun{}, err
	}
	return openSearchRun{sidecarRun: run, login: login}, nil
}

// startOpenSearchBackup starts a backup of one service's cluster now and returns
// its record, running. trigger is recorded on the run. The errors are
// prepareOpenSearchRun's.
func (s *Service) startOpenSearchBackup(ctx context.Context, appID, service, trigger string) (store.BackupRecord, error) {
	// A browser that goes away must not cancel the run halfway.
	ctx = context.WithoutCancel(ctx)
	run, err := s.prepareOpenSearchRun(ctx, appID, service, deploy.VolumeBackup, "", "")
	if err != nil {
		return store.BackupRecord{}, err
	}
	rec := store.BackupRecord{AppID: appID, ID: run.req.ID, Service: service, JobID: openSearchJobID(appID, service, run.req.TS),
		Engine: app.BackupOpenSearch, Status: store.BackupRunning, StartedAt: run.started, Trigger: trigger, Destination: run.dest.Name, Actor: callerEmail(ctx)}
	if err := s.store.SaveBackup(ctx, rec); err != nil {
		run.release()
		return store.BackupRecord{}, err
	}
	s.backupsChanged(appID)
	s.log.Info("opensearch backup started", "app", appID, "service", service, "run", rec.ID, "trigger", trigger)

	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.driveOpenSearchBackup(run, rec)
	}()
	return rec, nil
}

// driveOpenSearchBackup prepares the repository, cleans, snapshots and uploads, then records how the run
// ended, gives the lock back and tells the open pages. When Caravana is shutting
// down it leaves the record running: the next start settles it.
func (s *Service) driveOpenSearchBackup(run openSearchRun, rec store.BackupRecord) {
	base := s.watchContext(run.app.ID)
	defer func() {
		run.release()
		s.backupsChanged(run.app.ID)
	}()
	ctx, cancel := context.WithTimeout(base, run.req.Limit+s.volume.grace)
	defer cancel()
	fail := func(err error) {
		if base.Err() == nil {
			s.settleSidecarBackup(rec, nomadjob.SQLiteOutput{}, s.sidecarFailure(ctx, run.sidecarRun, err))
		}
	}

	owner, err := s.deployer.OpenSearchPrepare(ctx, run.copyID, run.svc)
	if err != nil {
		fail(err)
		return
	}
	if err := s.deployer.OpenSearchClean(ctx, run.copyID, run.svc, owner, true); err != nil {
		fail(err)
		return
	}
	if _, err := s.deployer.OpenSearchSnapshot(ctx, run.copyID, run.svc, run.req.ID, run.req.TS, run.login); err != nil {
		// The upload would have removed the folder; nothing will.
		s.cleanOpenSearchFolder(base, run, owner)
		fail(err)
		return
	}
	out, err := s.deployer.OpenSearchUpload(ctx, run.copyID, run.req.ID, run.app.ID, run.svc, run.dest, run.req.TS, run.svc.Backup.KeepOrDefault(), run.pinned)
	if err != nil {
		fail(err)
		return
	}
	s.settleSidecarBackup(rec, out, "")
}

// cleanOpenSearchFolder removes the folders runs left in the snapshot repository,
// once a run is over. It has a minute of its own, since the run's time may be
// spent, and does nothing when Caravana is shutting down: the next run's first
// step cleans. A failure is logged and no worse than it sounds.
func (s *Service) cleanOpenSearchFolder(base context.Context, run openSearchRun, owner string) {
	if base.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(base, time.Minute)
	defer cancel()
	if err := s.deployer.OpenSearchClean(ctx, run.copyID, run.svc, owner, false); err != nil {
		s.log.Warn("clean the opensearch snapshot folder; the next run does", "app", run.app.ID, "service", run.svc.Name, "error", err)
	}
}

// startOpenSearchRestore starts the restore of the opensearch backup b into the
// running cluster and returns the restore's record, running. trigger and label
// are recorded on it, as for restoreBackup. The errors are prepareOpenSearchRun's.
func (s *Service) startOpenSearchRestore(ctx context.Context, appID string, b store.BackupRecord, place restorePlace, trigger, label string) (store.RestoreRecord, error) {
	ctx = context.WithoutCancel(ctx)
	run, err := s.prepareOpenSearchRun(ctx, appID, b.Service, deploy.VolumeRestore, place.Object, place.Destination)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	rec := store.RestoreRecord{ID: restoreRecordID(appID, run.req.ID), AppID: appID, Service: b.Service, BackupID: b.ID,
		Engine: app.BackupOpenSearch, Status: store.BackupRunning, CreatedAt: run.started, Trigger: trigger, Pin: label, Destination: run.dest.Name, Actor: callerEmail(ctx)}
	if err := s.store.SaveRestore(ctx, rec); err != nil {
		run.release()
		return store.RestoreRecord{}, err
	}
	s.backupsChanged(appID)
	s.log.Info("opensearch restore started", "app", appID, "service", b.Service, "run", rec.ID, "backup", b.ID)

	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.driveOpenSearchRestore(run, rec)
	}()
	return rec, nil
}

// driveOpenSearchRestore prepares the repository, cleans, downloads the backup into a fresh folder and
// restores it into the cluster, then removes the folder whatever happened. It
// records how the run ended, gives the lock back and tells the open pages. When
// Caravana is shutting down it leaves the record running: the next start settles
// it.
func (s *Service) driveOpenSearchRestore(run openSearchRun, rec store.RestoreRecord) {
	base := s.watchContext(run.app.ID)
	defer func() {
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
	failed := func(err error) { settle(failedOutcome(s.sidecarFailure(ctx, run.sidecarRun, err), nil)) }

	owner, err := s.deployer.OpenSearchPrepare(ctx, run.copyID, run.svc)
	if err != nil {
		failed(err)
		return
	}
	if err := s.deployer.OpenSearchClean(ctx, run.copyID, run.svc, owner, true); err != nil {
		failed(err)
		return
	}
	// From here the folder is removed whatever happens, before the record says how it ended.
	finish := func(out volumeOutcome) {
		s.cleanOpenSearchFolder(base, run, owner)
		settle(out)
	}
	if _, err := s.deployer.OpenSearchDownload(ctx, run.copyID, run.req.ID, run.req.Object, run.svc, run.dest, owner); err != nil {
		finish(failedOutcome(s.sidecarFailure(ctx, run.sidecarRun, err), nil))
		return
	}
	if _, err := s.deployer.OpenSearchRestore(ctx, run.copyID, run.svc, run.req.ID, run.login); err != nil {
		finish(failedOutcome(s.sidecarFailure(ctx, run.sidecarRun, err), nil))
		return
	}
	finish(volumeOutcome{status: store.BackupSucceeded})
}

// openSearchRestartedMessage is the failure of an opensearch backup Caravana's
// restart cut off: what it left in the repository is removed by the next run.
const openSearchRestartedMessage = restartedMessage

// openSearchRestoreRestartedMessage is the same for a restore, which may have
// closed indices before it was cut off.
const openSearchRestoreRestartedMessage = restartedMessage + "; the indices it closed may still be closed"

// settleOpenSearchRuns settles an app's opensearch runs a restart left running
// (plan D7): it fails them, with nothing asked of Nomad, since what they did
// happened in the cluster and in a folder, and no request or result outlives
// them.
func (s *Service) settleOpenSearchRuns(backups []store.BackupRecord, restores []store.RestoreRecord) {
	for _, b := range backups {
		if b.Engine == app.BackupOpenSearch && b.Status == store.BackupRunning {
			s.settleSidecarBackup(b, nomadjob.SQLiteOutput{}, openSearchRestartedMessage)
		}
	}
	for _, r := range restores {
		if r.Engine == app.BackupOpenSearch && r.Status == store.BackupRunning {
			s.settleVolumeRestore(r, failedOutcome(openSearchRestoreRestartedMessage, nil))
		}
	}
}
