package service

import (
	"context"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// What the Settings pages count (feature 021 page 3). Both are plain reads of
// SQLite: Settings must draw when Nomad is down, which is when it is needed.

// StoredApps returns every app as stored, with no status and no Nomad call.
func (s *Service) StoredApps(ctx context.Context) ([]store.AppRecord, error) {
	return s.store.ListApps(ctx)
}

// LastBackupRun returns the newest backup run that ended, of any service of
// any app: the run that says whether the backup destination works. nil when no
// run has ended yet. A run still going doesn't say either way.
func (s *Service) LastBackupRun(ctx context.Context) (*store.BackupRecord, error) {
	last, _, err := s.BackupOutcome(ctx)
	return last, err
}

// BackupOutcome returns the destination's newest run that ended (as LastBackupRun)
// and, apart, the newest of the services' newest runs that failed: a service whose
// latest backup failed, even when another service's later run went well. failing
// is nil when no service's newest run failed. Both are read from the stored runs,
// with no Nomad call.
func (s *Service) BackupOutcome(ctx context.Context) (last, failing *store.BackupRecord, err error) {
	latest, _, err := s.store.LatestBackupRuns(ctx)
	if err != nil {
		return nil, nil, err
	}
	for i := range latest {
		b := &latest[i]
		if b.Status == store.BackupRunning {
			continue
		}
		if last == nil || b.StartedAt.After(last.StartedAt) {
			last = b
		}
		if b.Status == store.BackupFailed && (failing == nil || b.StartedAt.After(failing.StartedAt)) {
			failing = b
		}
	}
	return last, failing, nil
}
