package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
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

// Two kinds of backup, two ways to run (feature 016). A Postgres backup is a
// Nomad job, which this file starts and whose runs the watcher records. A
// volume backup runs inside the service's own copy, and the run itself
// records it (volumebackup.go), so the Nomad-job code skips those services.
func postgresBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Engine == app.BackupPostgres
}

// restoreTimeout is how long a restore may stay unfinished before it is
// recorded failed. A restore job whose variable is missing never fails by
// itself: its copy waits for it (research R3).
const restoreTimeout = 10 * time.Minute

// DestinationInput is a new backup destination. A nil SecretAccessKey keeps
// the one set before.
type DestinationInput struct {
	Endpoint, Region, Bucket, Prefix, AccessKeyID string
	SecretAccessKey                               *string
}

var (
	bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixRE = regexp.MustCompile(`^[A-Za-z0-9._/-]*$`)
)

// GetBackupDestination returns the destination without its secret key, or
// store.ErrNotFound when none is set.
func (s *Service) GetBackupDestination(ctx context.Context) (store.BackupDestination, error) {
	return s.store.GetBackupDestination(ctx)
}

// SetBackupDestination validates and saves the destination, and points every
// backup job at it from its next run (no deploy). Errors are
// *app.FieldError naming the input field, or Nomad's.
func (s *Service) SetBackupDestination(ctx context.Context, in DestinationInput) (store.BackupDestination, error) {
	u, err := url.Parse(in.Endpoint)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return store.BackupDestination{}, app.NewFieldError("endpoint", "%q must be an http or https URL", in.Endpoint)
	case strings.TrimSpace(in.Region) == "":
		return store.BackupDestination{}, app.NewFieldError("region", "is required (us-east-1 when the provider doesn't care)")
	case !bucketRE.MatchString(in.Bucket):
		return store.BackupDestination{}, app.NewFieldError("bucket", "%q must be a bucket name: lowercase letters, digits, '.' and '-'", in.Bucket)
	case !prefixRE.MatchString(in.Prefix):
		return store.BackupDestination{}, app.NewFieldError("prefix", "may hold letters, digits, '.', '_', '-' and '/'")
	case strings.TrimSpace(in.AccessKeyID) == "":
		return store.BackupDestination{}, app.NewFieldError("accessKeyId", "is required")
	}

	secret := ""
	if in.SecretAccessKey != nil {
		secret = *in.SecretAccessKey
		if secret == "" {
			return store.BackupDestination{}, app.NewFieldError("secretAccessKey", "is empty; leave it out to keep the one set before")
		}
		if err := s.deployer.SaveBackupSecret(ctx, secret); err != nil {
			return store.BackupDestination{}, err
		}
	} else if secret, err = s.deployer.BackupSecret(ctx); err != nil {
		return store.BackupDestination{}, err
	} else if secret == "" {
		return store.BackupDestination{}, app.NewFieldError("secretAccessKey", "is required the first time")
	}

	d := store.BackupDestination{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket,
		Prefix: strings.Trim(in.Prefix, "/"), AccessKeyID: in.AccessKeyID, UpdatedAt: time.Now().UTC()}
	if err := s.store.SetBackupDestination(ctx, d); err != nil {
		return store.BackupDestination{}, err
	}
	s.publish(Change{What: ChangeAttention}) // "no backup destination" may be resolved
	if err := s.deployer.SetBackupDestination(ctx, toDeployDestination(d, secret)); err != nil {
		return d, fmt.Errorf("saved, but the backup jobs still point at the old destination: %w", err)
	}
	return d, nil
}

func toDeployDestination(d store.BackupDestination, secret string) deploy.BackupDestination {
	return deploy.BackupDestination{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: d.Prefix,
		AccessKeyID: d.AccessKeyID, SecretAccessKey: secret}
}

// backupDestination is the full destination, secret included, for jobs.
// ok is false when none is set.
func (s *Service) backupDestination(ctx context.Context) (deploy.BackupDestination, bool, error) {
	d, err := s.store.GetBackupDestination(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return deploy.BackupDestination{}, false, nil
	}
	if err != nil {
		return deploy.BackupDestination{}, false, err
	}
	secret, err := s.deployer.BackupSecret(ctx)
	if err != nil {
		return deploy.BackupDestination{}, false, err
	}
	return toDeployDestination(d, secret), true, nil
}

// checkBackups refuses backups the installation can't run: none without a
// destination. That is all a volume backup needs: no port and no credentials
// (app.Validate checks a Postgres service's own).
func (s *Service) checkBackups(ctx context.Context, a app.App) error {
	for i, svc := range a.Services {
		if svc.Backup == nil {
			continue
		}
		if _, err := s.store.GetBackupDestination(ctx); errors.Is(err, store.ErrNotFound) {
			return app.NewFieldError(fmt.Sprintf("services[%d].backup", i),
				"no backup destination is set: set one first (Settings, or PUT /v1/backups/destination)")
		} else if err != nil {
			return err
		}
	}
	return nil
}

// syncBackups makes the app's backup jobs match a, with the credentials the
// official postgres image reads, from the service's env or its secrets
// (sets). Only Postgres backups are jobs; volume backups have none, and a
// service that changed to one loses its old job. It answers a warning when it
// couldn't: the app itself is deployed by then.
func (s *Service) syncBackups(ctx context.Context, a app.App, digests map[string]string, sets secrets.Sets) []app.Warning {
	dest, _, err := s.backupDestination(ctx)
	if err == nil {
		creds := map[string]deploy.BackupCredentials{}
		for _, svc := range a.Services {
			if !postgresBackup(svc) {
				continue
			}
			value := func(name, def string) string {
				for _, e := range svc.Env {
					if e.Name == name {
						return e.Value
					}
				}
				if v, ok := sets[svc.Name][name]; ok {
					return v
				}
				return def
			}
			user := value("POSTGRES_USER", "postgres")
			creds[svc.Name] = deploy.BackupCredentials{User: user, Password: value("POSTGRES_PASSWORD", ""), Database: value("POSTGRES_DB", user)}
		}
		err = s.deployer.SyncBackups(ctx, a, digests, creds, dest)
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
	live := slices.DeleteFunc(all, func(b store.BackupRecord) bool { return b.Expired })
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
// A Postgres backup is a Nomad job. A volume backup restarts the service's
// own copy and stops the service while it runs (see startVolumeBackup), which
// can also be ErrNoRunningCopy.
func (s *Service) BackUpNow(ctx context.Context, appID, service string) (store.BackupRecord, error) {
	return s.backUpNow(ctx, appID, service, "manual")
}

// backUpNow is BackUpNow for a trigger: "manual", or "schedule" from the
// scheduler of volume backups.
func (s *Service) backUpNow(ctx context.Context, appID, service, trigger string) (store.BackupRecord, error) {
	_, svc, err := s.backedUpService(ctx, appID, service)
	if err != nil {
		return store.BackupRecord{}, err
	}
	if _, ok, err := s.backupDestination(ctx); err != nil {
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
	jobID, err := s.deployer.ForceBackup(ctx, appID, service)
	if err != nil {
		return store.BackupRecord{}, err
	}
	rec := runRecord(appID, service, deploy.BackupRun{JobID: jobID, Status: deploy.RunRunning, Started: runTime(jobID)})
	if err := s.store.SaveBackup(ctx, rec); err != nil {
		return store.BackupRecord{}, err
	}
	s.backupsChanged(appID)
	return rec, nil
}

// backupBusy reports whether a backup or restore of the service is running.
// For a Postgres service that is Nomad's word for its backup jobs, and the
// store's for restores. A volume run is no Nomad job, so for a volume service
// it is the store's word for both: the run records itself as running before
// it starts, and settles the record when it ends.
func (s *Service) backupBusy(ctx context.Context, appID string, svc app.Service) (bool, error) {
	if postgresBackup(svc) {
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
// still in the destination can be restored, one backup or restore per service
// at a time.
//
// A Postgres restore is a Nomad job that loads the dump into the running
// database. A volume restore restarts the service's own copy and stops the
// service while its volumes are replaced with the archives' files (see
// startVolumeRestore), which can also be ErrNoRunningCopy.
func (s *Service) Restore(ctx context.Context, appID, backupID string) (store.RestoreRecord, error) {
	b, err := s.store.GetBackup(ctx, appID, backupID)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	if b.Status != store.BackupSucceeded || b.Expired {
		return store.RestoreRecord{}, fmt.Errorf("backup %s can't be restored: it %s: %w", backupID, map[bool]string{true: "was deleted by retention", false: "didn't succeed"}[b.Expired], store.ErrNotFound)
	}
	_, svc, err := s.backedUpService(ctx, appID, b.Service)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	if _, ok, err := s.backupDestination(ctx); err != nil {
		return store.RestoreRecord{}, err
	} else if !ok {
		return store.RestoreRecord{}, ErrNoBackupDestination
	}
	if b.Engine != svc.Backup.Engine {
		return store.RestoreRecord{}, fmt.Errorf("backup %s was made by the %s engine, and the service now backs up with %s: %w",
			backupID, b.Engine, svc.Backup.Engine, store.ErrNotFound)
	}
	if busy, err := s.backupBusy(ctx, appID, svc); err != nil {
		return store.RestoreRecord{}, err
	} else if busy {
		return store.RestoreRecord{}, ErrBackupRunning
	}
	if hasVolumeBackup(svc) {
		return s.startVolumeRestore(ctx, appID, b)
	}
	image, err := s.runningImage(ctx, appID, b.Service)
	if err != nil {
		return store.RestoreRecord{}, err
	}
	if err := s.deployer.Restore(ctx, appID, svc, image, b.Object); err != nil {
		return store.RestoreRecord{}, err
	}
	r := store.RestoreRecord{ID: strings.ToLower(rand.Text()), AppID: appID, Service: b.Service, BackupID: backupID,
		Status: store.BackupRunning, CreatedAt: time.Now().UTC()}
	if err := s.store.SaveRestore(ctx, r); err != nil {
		return store.RestoreRecord{}, err
	}
	s.backupsChanged(appID)
	return r, nil
}

// runningImage is the pinned image of the service's newest deployment that
// has one: what runs now, so the restore's tools match the server.
func (s *Service) runningImage(ctx context.Context, appID, service string) (string, error) {
	deps, err := s.store.ListDeployments(ctx, appID, 0)
	if err != nil {
		return "", err
	}
	for _, d := range deps {
		if d.Status == store.StatusHealthy && d.Deployment.Digests[service] != "" {
			return d.Deployment.Digests[service], nil
		}
	}
	return "", fmt.Errorf("service %q of %q has no healthy deployment to restore into: %w", service, appID, store.ErrNotFound)
}

// runTime is the start time in a run's job ID (<job>/periodic-<ts>).
func runTime(jobID string) time.Time {
	ts, _ := strconv.ParseInt(jobID[strings.LastIndex(jobID, "-")+1:], 10, 64)
	return time.Unix(ts, 0).UTC()
}

// runRecord is the store's record of one run.
func runRecord(appID, service string, r deploy.BackupRun) store.BackupRecord {
	rec := store.BackupRecord{AppID: appID, ID: fmt.Sprintf("%s-%d", service, r.Started.Unix()), Service: service,
		JobID: r.JobID, Status: r.Status, Object: r.Object, Bytes: r.Bytes, Message: r.Message, StartedAt: r.Started}
	if r.Status != deploy.RunRunning {
		now := time.Now().UTC()
		rec.FinishedAt = &now
	}
	return rec
}

// watchBackups records what backup runs and restores did, every gap, until
// ctx ends: Nomad forgets finished batch jobs after a few hours, Caravana
// keeps them.
func (s *Service) watchBackups(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		s.recordBackups(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
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
}

// recordApp records the runs of a's Postgres-backed services that Nomad knows
// and the store doesn't have as final yet. A volume backup isn't a Nomad job:
// its run records itself.
func (s *Service) recordApp(ctx context.Context, a app.App) {
	changed := false
	for _, svc := range a.Services {
		if !postgresBackup(svc) {
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
		for _, run := range runs {
			r := runRecord(a.ID, svc.Name, run)
			if final[r.ID] {
				continue
			}
			if err := s.store.SaveBackup(ctx, r); err != nil {
				s.log.Warn("record backups: save", "app", a.ID, "backup", r.ID, "error", err)
				continue
			}
			changed = true
			var expired []string
			for _, f := range run.Expired {
				expired = append(expired, svc.Name+"-"+strings.TrimSuffix(f, ".dump"))
			}
			if err := s.store.ExpireBackups(ctx, a.ID, expired); err != nil {
				s.log.Warn("record backups: expire", "app", a.ID, "error", err)
			}
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
		if r.Engine != app.BackupPostgres {
			continue // a volume restore has no Nomad job: its run settles it
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
