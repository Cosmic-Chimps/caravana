package store

import (
	"context"
	"fmt"
)

// BuildSettings are the owner's limits for builds (Settings › Builds).
type BuildSettings struct {
	// MemoryMB and CPUMHz are what a build reserves.
	MemoryMB, CPUMHz int
	// TimeoutMinutes stops a build that takes longer.
	TimeoutMinutes int
	// Concurrency is how many builds run at once in the installation.
	Concurrency int
	// CacheMB is how big an app's build cache may grow before it is emptied;
	// 0 means no cache.
	CacheMB int
}

// GetBuildSettings returns the settings (the defaults before the owner saved any).
func (s *Store) GetBuildSettings(ctx context.Context) (BuildSettings, error) {
	var set BuildSettings
	err := s.db.QueryRowContext(ctx, `SELECT memory_mb, cpu_mhz, timeout_minutes, concurrency, cache_mb
		FROM build_settings WHERE id = 1`).Scan(&set.MemoryMB, &set.CPUMHz, &set.TimeoutMinutes, &set.Concurrency, &set.CacheMB)
	if err != nil {
		return BuildSettings{}, fmt.Errorf("build settings: %w", err)
	}
	return set, nil
}

// SaveBuildSettings writes the settings. The service checks the ranges; the
// table refuses a value outside them too.
func (s *Store) SaveBuildSettings(ctx context.Context, set BuildSettings) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE build_settings SET memory_mb = ?, cpu_mhz = ?, timeout_minutes = ?,
		concurrency = ?, cache_mb = ? WHERE id = 1`,
		set.MemoryMB, set.CPUMHz, set.TimeoutMinutes, set.Concurrency, set.CacheMB); err != nil {
		return fmt.Errorf("save build settings: %w", err)
	}
	return nil
}
