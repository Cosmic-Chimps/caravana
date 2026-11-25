package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Backups (feature 010). The jobs that dump, upload and restore are in
// package deploy; this file keeps the destination, records what the runs
// did, and starts backups and restores on request.

// Errors the API maps to their own codes.
var (
	ErrNoBackupDestination = errors.New("no backup destination is set")
	ErrBackupRunning       = errors.New("a backup or restore of this service is running")
)

// Two kinds of backup, two ways to run (feature 016). A dump engine's backup
// (Postgres, MySQL, MongoDB: feature 046) is a Nomad job, which this file starts
// and whose runs the watcher records. A volume backup runs inside the service's
// own copy, and the run itself records it (volumebackup.go), so the Nomad-job
// code skips those services.
func dumpBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Dumps()
}

// restoreTimeout is how long a restore may stay unfinished before it is
// recorded failed. A restore job whose variable is missing never fails by
// itself: its copy waits for it (research R3).
const restoreTimeout = 10 * time.Minute

// checkBackups refuses backups the installation can't run: none without a
// destination, and none that names a destination that doesn't exist, as its own
// or as a copy, or copies to the destination its backups already go to. That is
// all a volume backup needs: no port and no credentials (app.Validate checks a
// dump engine's own, and the shape of the names).
func (s *Service) checkBackups(ctx context.Context, a app.App) error {
	for i, svc := range a.Services {
		if svc.Backup == nil {
			continue
		}
		field := fmt.Sprintf("services[%d].backup", i)
		primary, copies, err := s.backupPlaces(ctx, a.ID, *svc.Backup)
		if errors.Is(err, ErrNoBackupDestination) {
			return app.NewFieldError(field, "no backup destination is set: set one first (Settings, or PUT /v1/backups/destination)")
		}
		if err != nil {
			return err
		}
		primaryField := field
		if svc.Backup.Destination != "" {
			primaryField = field + ".destination"
		}
		if err := s.requireDestination(ctx, primaryField, primary); err != nil {
			return err
		}
		for k, name := range copies {
			copyField := fmt.Sprintf("%s.copyTo[%d]", field, k)
			if err := s.requireDestination(ctx, copyField, name); err != nil {
				return err
			}
			if name == primary {
				return app.NewFieldError(copyField, "%q is where this service's backups already go", name)
			}
		}
	}
	return nil
}

// requireDestination is a *app.FieldError on field unless a destination is
// named name.
func (s *Service) requireDestination(ctx context.Context, field, name string) error {
	if _, err := s.store.GetBackupDestinationNamed(ctx, name); errors.Is(err, store.ErrNotFound) {
		return app.NewFieldError(field, "no backup destination is named %q; add it in Settings › Backup storage", name)
	} else if err != nil {
		return err
	}
	return nil
}

// syncBackups makes the app's backup jobs match a, with the credentials the
// database's official image reads (app.FindDumpLogin), from the service's env or
// its secrets (sets). Only the dump engines' backups are jobs; volume backups
// have none, and a service that changed to one loses its old job. It answers a
// warning when it couldn't: the app itself is deployed by then.
func (s *Service) syncBackups(ctx context.Context, a app.App, digests map[string]string, sets secrets.Sets) []app.Warning {
	var err error
	creds := map[string]deploy.BackupCredentials{}
	dests := map[string]deploy.BackupDestination{}
	for _, svc := range a.Services {
		if !dumpBackup(svc) {
			continue
		}
		// The service's env first, then its secrets: the same lookup for every engine.
		login, _ := app.FindDumpLogin(svc.Backup.Engine, func(name string) (string, bool) {
			for _, e := range svc.Env {
				if e.Name == name {
					return e.Value, true
				}
			}
			v, ok := sets[svc.Name][name]
			return v, ok
		})
		creds[svc.Name] = deploy.BackupCredentials{User: login.User, Password: login.Password, Database: login.Database}
		// Each service's backups go where its own policy says.
		var dest deploy.BackupDestination
		var found bool
		if dest, found, err = s.destinationFor(ctx, a.ID, *svc.Backup); err != nil {
			break
		} else if found {
			dests[svc.Name] = dest
		}
	}
	if err == nil {
		var private map[string]bool
		var pins map[string][]string
		if private, err = s.privateRegistries(ctx); err == nil {
			// The pinned backups' timestamps go into each dump job's variable
			// for its retention (feature 036).
			if pins, err = s.pinStampsByService(ctx, a.ID); err == nil {
				err = s.deployer.SyncBackups(ctx, a, digests, creds, dests, private, pins)
			}
		}
	}
	if err != nil {
		s.log.Error("schedule backups", "app", a.ID, "error", err)
		return []app.Warning{{Message: "backups weren't scheduled: " + err.Error() + "; deploy again to retry"}}
	}
	return nil
}

// ListBackups returns an app's backups still in the destination and its
// restores, newest first. An unknown app is store.ErrNotFound.
func (s *Service) ListBackups(ctx context.Context, appID string) ([]store.BackupRecord, []store.RestoreRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	// Record what Nomad knows first, so a read is never a watcher pass behind.
	s.recordApp(ctx, rec.App)
	s.recordRestores(ctx)
	all, err := s.store.ListBackups(ctx, appID, 0)
	if err != nil {
		return nil, nil, err
	}
	// A backup is listed while any place holds it: its own destination or a copy.
	live := slices.DeleteFunc(all, func(b store.BackupRecord) bool { return b.Gone() })
	restores, err := s.store.ListRestores(ctx, appID, 20)
	if err != nil {
		return nil, nil, err
	}
	return live, restores, nil
}

// backedUpService returns the service of appID that has a backup policy, or
// store.ErrNotFound.
func (s *Service) backedUpService(ctx context.Context, appID, service string) (app.App, app.Service, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return app.App{}, app.Service{}, err
	}
	for _, svc := range rec.App.Services {
		if svc.Name == service && svc.Backup != nil {
			return rec.App, svc, nil
		}
	}
	return app.App{}, app.Service{}, fmt.Errorf("service %q of %q has no backups: %w", service, appID, store.ErrNotFound)
}

// BackUpNow starts a backup of one service at once. A backup or restore of
// the service already running is ErrBackupRunning: two runs started within
// the same second would be one run in Nomad.
//
// A dump engine's backup (Postgres, MySQL, MongoDB) is a Nomad job. A volume backup restarts the service's
// own copy and stops the service while it runs (see startVolumeBackup), which
// can also be ErrNoRunningCopy.
func (s *Service) BackUpNow(ctx context.Context, appID, service string) (store.BackupRecord, error) {
	rec, err := s.backUpNow(ctx, appID, service, store.BackupTriggerOwner)
	if err != nil {
		return rec, err
	}
	// Only the owner's: a backup on its schedule is the clock's, which the log leaves out (plan D14).
	s.audit(ctx, audit.Entry{Action: audit.ActionBackedUp, Resource: backupResource(appID, rec.ID, service),
		Record: &audit.Record{Kind: audit.RecordBackup, ID: rec.ID}})
	return rec, nil
}

// backUpNow is BackUpNow for a trigger, recorded on the run:
// store.BackupTriggerOwner for Back up now, or store.BackupTriggerTimer from
// the scheduler of volume backups. (Postgres backups on their schedule are
// Nomad's periodic jobs, which the watcher records as timer.)
func (s *Service) backUpNow(ctx context.Context, appID, service, trigger string) (store.BackupRecord, error) {
	_, svc, err := s.backedUpService(ctx, appID, service)
	if err != nil {
		return store.BackupRecord{}, err
	}
	// A stopped app has nothing to back up from (feature 020.5).
	if err := s.refuseIfStopped(ctx, appID); err != nil {
		return store.BackupRecord{}, err
	}
	primary, _, err := s.backupPlaces(ctx, appID, *svc.Backup)
	if err != nil {
		return store.BackupRecord{}, err // ErrNoBackupDestination when none resolves
	}
	if _, ok, err := s.destinationNamed(ctx, primary); err != nil {
		return store.BackupRecord{}, err
	} else if !ok {
		return store.BackupRecord{}, ErrNoBackupDestination
	}
	if busy, err := s.backupBusy(ctx, appID, svc); err != nil {
		return store.BackupRecord{}, err
	} else if busy {
		return store.BackupRecord{}, ErrBackupRunning
	}
	if hasVolumeBackup(svc) {
		return s.startVolumeBackup(ctx, appID, service, trigger)
	}
	if hasSQLiteBackup(svc) {
		return s.startSQLiteBackup(ctx, appID, service, trigger)
	}
	jobID, err := s.deployer.ForceBackup(ctx, appID, service)
	if err != nil {
		return store.BackupRecord{}, err
	}
	rec := runRecord(appID, service, svc.Backup.Engine, deploy.BackupRun{JobID: jobID, Status: deploy.RunRunning, Started: runTime(jobID)})
	rec.Trigger = trigger
	rec.Destination = primary // the run's own word replaces it once the job says where it went
	rec.Actor = callerEmail(ctx)
	if err := s.store.SaveBackup(ctx, rec); err != nil {
		return store.BackupRecord{}, err
	}
	s.backupsChanged(appID)
	return rec, nil
}

// backupBusy reports whether a backup or restore of the service is running.
// For a service with a dump engine that is Nomad's word for its backup jobs, and the
// store's for restores. A volume run is no Nomad job, so for a volume service
// it is the store's word for both: the run records itself as running before
// it starts, and settles the record when it ends.
func (s *Service) backupBusy(ctx context.Context, appID string, svc app.Service) (bool, error) {
	if dumpBackup(svc) {
		runs, err := s.deployer.BackupRuns(ctx, appID, svc.Name)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(runs, func(r deploy.BackupRun) bool { return r.Status == deploy.RunRunning }) {
			return true, nil
		}
	} else {
		backups, err := s.store.ListBackups(ctx, appID, 0)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(backups, func(b store.BackupRecord) bool {
			return b.Service == svc.Name && b.Status == store.BackupRunning
		}) {
			return true, nil
		}
	}
	restores, err := s.store.ListRestores(ctx, appID, 0)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(restores, func(r store.RestoreRecord) bool {
		return r.Service == svc.Name && r.Status == store.BackupRunning
	}), nil
}

// Restore loads a backup into the running service. Only a succeeded backup
// still in one of its places can be restored, one backup or restore per service
// at a time. It is the owner's restore: the record says so.
//
// from names the destination to read from (a *app.FieldError on "from" when the
// backup isn't there). Without it a backup in one place is read from there, and
// one in several is looked for with a check job, in the order its own
// destination, then its copies; ErrBackupMissing says none still has it.
//
// A dump engine's restore is a Nomad job that loads the dump into the running
// database. A volume restore restarts the service's own copy and stops the
// service while its volumes are replaced with the archives' files (see
// startVolumeRestore), which can also be ErrNoRunningCopy.
func (s *Service) Restore(ctx context.Context, appID, backupID, from string) (store.RestoreRecord, error) {
	b, err := s.store.GetBackup(ctx, appID, backupID)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	if b.Status != store.BackupSucceeded || b.Gone() {
		return store.RestoreRecord{}, fmt.Errorf("backup %s can't be restored: it %s: %w", backupID, map[bool]string{true: "was deleted by retention", false: "didn't succeed"}[b.Expired], store.ErrNotFound)
	}
	rec, err := s.restoreBackup(ctx, appID, b, store.RestoreTriggerOwner, "", from)
	if err != nil {
		return rec, err
	}
	s.audit(ctx, audit.Entry{Action: audit.ActionRestored, Resource: backupResource(appID, backupID, b.Service),
		Record: &audit.Record{Kind: audit.RecordRestore, ID: rec.ID}})
	return rec, nil
}

// restoreBackup starts the restore of b, a succeeded backup that is still in one
// of its places, into its running service, and records it with trigger (the
// owner's, or the service's restore schedule's, for which label is the one it
// asked for; "" otherwise). from names the place to read from, "" for the first
// place that has it. It is what Restore and the restore clock share, so a
// scheduled restore does exactly what the owner's does. The errors include
// ErrAppStopped, ErrNoBackupDestination, ErrBackupRunning, ErrNoRunningCopy,
// ErrBackupMissing and Nomad's.
func (s *Service) restoreBackup(ctx context.Context, appID string, b store.BackupRecord, trigger, label, from string) (store.RestoreRecord, error) {
	_, svc, err := s.backedUpService(ctx, appID, b.Service)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	if err := s.refuseIfStopped(ctx, appID); err != nil {
		return store.RestoreRecord{}, err
	}
	if b.Engine != svc.Backup.Engine {
		return store.RestoreRecord{}, fmt.Errorf("backup %s was made by the %s engine, and the service now backs up with %s: %w",
			b.ID, b.Engine, svc.Backup.Engine, store.ErrNotFound)
	}
	if busy, err := s.backupBusy(ctx, appID, svc); err != nil {
		return store.RestoreRecord{}, err
	} else if busy {
		return store.RestoreRecord{}, ErrBackupRunning
	}
	place, source, err := s.chooseRestorePlace(ctx, appID, b, svc, from)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	if hasVolumeBackup(svc) {
		return s.startVolumeRestore(ctx, appID, b, place, trigger, label)
	}
	if hasSQLiteBackup(svc) {
		return s.startSQLiteRestore(ctx, appID, b, place, trigger, label)
	}
	image, err := s.runningImage(ctx, appID, b.Service)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	private, err := s.privateRegistries(ctx)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	if err := s.deployer.Restore(ctx, appID, svc, image, place.Object, private, source); err != nil {
		return store.RestoreRecord{}, err
	}
	r := store.RestoreRecord{ID: strings.ToLower(rand.Text()), AppID: appID, Service: b.Service, BackupID: b.ID,
		Engine: svc.Backup.Engine, Status: store.BackupRunning, CreatedAt: time.Now().UTC(), Trigger: trigger, Pin: label,
		Destination: place.Destination, Actor: callerEmail(ctx)}
	if err := s.store.SaveRestore(ctx, r); err != nil {
		return store.RestoreRecord{}, err
	}
	s.backupsChanged(appID)
	return r, nil
}

// runningImage is the pinned image of the service's newest deployment that
// has one: what runs now, so the restore's tools match the server.
func (s *Service) runningImage(ctx context.Context, appID, service string) (string, error) {
	_, image, err := s.liveVersion(ctx, appID, service)
	if errors.Is(err, ErrNoLiveDeployment) {
		return "", fmt.Errorf("service %q of %q has no healthy deployment to restore into: %w", service, appID, store.ErrNotFound)
	}
	return image, err
}

// runTime is the start time in a run's job ID (<job>/periodic-<ts>).
func runTime(jobID string) time.Time {
	ts, _ := strconv.ParseInt(jobID[strings.LastIndex(jobID, "-")+1:], 10, 64)
	return time.Unix(ts, 0).UTC()
}

// runRecord is the store's record of one run of a dump engine's backup job.
func runRecord(appID, service, engine string, r deploy.BackupRun) store.BackupRecord {
	rec := store.BackupRecord{AppID: appID, ID: fmt.Sprintf("%s-%d", service, r.Started.Unix()), Service: service,
		JobID: r.JobID, Engine: engine, Status: r.Status, Object: r.Object, Bytes: r.Bytes, Message: r.Message, StartedAt: r.Started,
		Destination: r.Destination}
	if r.Status != deploy.RunRunning {
		now := time.Now().UTC()
		rec.FinishedAt = &now
	}
	return rec
}

// watchBackups records what backup runs and restores did until ctx ends: Nomad
// forgets finished batch jobs after a few hours, Caravana keeps them. It makes a
// pass when Nomad's events say a backup or restore job changed (backupsKey) and
// at least every gap, as the safety net.
func (s *Service) watchBackups(ctx context.Context, gap time.Duration) {
	for {
		woken := s.wakes.channel(backupsKey) // before the pass, so a change during it isn't lost
		s.recordBackups(ctx)
		timer := time.NewTimer(gap)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-woken:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// recordBackups is one pass of watchBackups. Errors are logged; the next
// pass tries again.
func (s *Service) recordBackups(ctx context.Context) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		s.log.Warn("record backups: list apps", "error", err)
		return
	}
	for _, rec := range apps {
		s.recordApp(ctx, rec.App)
	}
	s.recordRestores(ctx)
	s.copyPass(ctx)
}

// expiredRecordID is the ID of the record of the backup file that retention
// deleted: "<service>-<unix seconds>". The file is named <unix seconds> and the
// engine's extension (".dump", ".sql.gz", ".archive.gz"), and the digits end at
// the first dot.
func expiredRecordID(service, file string) string {
	stamp, _, _ := strings.Cut(file, ".")
	return service + "-" + stamp
}

// recordApp records the runs of a's dump-backed services that Nomad knows and
// the store doesn't have as final yet. A volume backup isn't a Nomad job: its
// run records itself.
func (s *Service) recordApp(ctx context.Context, a app.App) {
	changed := false
	for _, svc := range a.Services {
		if !dumpBackup(svc) {
			continue
		}
		runs, err := s.deployer.BackupRuns(ctx, a.ID, svc.Name)
		if err != nil {
			s.log.Warn("record backups: read runs", "app", a.ID, "service", svc.Name, "error", err)
			continue
		}
		known, err := s.store.ListBackups(ctx, a.ID, 0)
		if err != nil {
			s.log.Warn("record backups: read records", "app", a.ID, "error", err)
			continue
		}
		final := map[string]bool{}
		for _, k := range known {
			final[k.ID] = k.Status != store.BackupRunning
		}
		// A run that doesn't say where it went (a job from before jobs did, or one
		// still running) went where the policy sends backups now.
		current, _, _ := s.backupPlaces(ctx, a.ID, *svc.Backup)
		for _, run := range runs {
			r := runRecord(a.ID, svc.Name, svc.Backup.Engine, run)
			if final[r.ID] {
				continue
			}
			if r.Destination == "" {
				r.Destination = current
			}
			if err := s.store.SaveBackup(ctx, r); err != nil {
				s.log.Warn("record backups: save", "app", a.ID, "backup", r.ID, "error", err)
				continue
			}
			changed = true
			var expired []string
			for _, f := range run.Expired {
				expired = append(expired, expiredRecordID(svc.Name, f))
			}
			if err := s.store.ExpireBackups(ctx, a.ID, expired); err != nil {
				s.log.Warn("record backups: expire", "app", a.ID, "error", err)
			}
			// A backup that first shows up as succeeded gets its copies queued.
			s.queueCopies(ctx, r)
		}
	}
	if changed {
		s.backupsChanged(a.ID)
	}
}

// recordRestores records the end of every restore Nomad has finished.
func (s *Service) recordRestores(ctx context.Context) {
	running, err := s.store.RunningRestores(ctx)
	if err != nil {
		s.log.Warn("record restores", "error", err)
		return
	}
	for _, r := range running {
		if !app.DumpEngine(r.Engine) {
			continue // a volume or sqlite restore has no Nomad job: its run settles it
		}
		status, msg, err := s.deployer.RestoreStatus(ctx, r.AppID, r.Service)
		if err != nil {
			s.log.Warn("record restores: read", "app", r.AppID, "restore", r.ID, "error", err)
			continue
		}
		if status == deploy.RunRunning && time.Since(r.CreatedAt) > restoreTimeout {
			status, msg = deploy.RunFailed, fmt.Sprintf("the restore didn't finish in %s", restoreTimeout)
		}
		if status == deploy.RunRunning {
			continue
		}
		now := time.Now().UTC()
		r.Status, r.Message, r.FinishedAt = status, msg, &now
		if err := s.store.SaveRestore(ctx, r); err != nil {
			s.log.Warn("record restores: save", "app", r.AppID, "restore", r.ID, "error", err)
			continue
		}
		s.backupsChanged(r.AppID)
	}
}

// backupsChanged tells the open pages of the app, and the needs-you list. The
// Apps headline counts the apps on that list, so the list of apps hears of it
// too.
func (s *Service) backupsChanged(appID string) {
	s.publish(Change{AppID: appID, What: ChangeDeployments})
	s.publish(Change{What: ChangeApps})
	s.publish(Change{What: ChangeAttention})
}
