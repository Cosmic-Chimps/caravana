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
	latest, _, err := s.store.LatestBackupRuns(ctx)
	if err != nil {
		return nil, err
	}
	var last *store.BackupRecord
	for i := range latest {
		b := &latest[i]
		if b.Status == store.BackupRunning {
			continue
		}
		if last == nil || b.StartedAt.After(last.StartedAt) {
			last = b
		}
	}
	return last, nil
}
