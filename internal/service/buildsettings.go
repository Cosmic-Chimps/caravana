package service

import (
	"context"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// BuildSettings are the owner's limits for builds (feature 050, plan D8),
// shown in Settings › Builds and by the API.
type BuildSettings struct {
	// MemoryMB and CPUMHz are what a build reserves (512 to 16384 MB, 500 to
	// 32000 MHz).
	MemoryMB, CPUMHz int
	// TimeoutMinutes stops a build that takes longer (5 to 180).
	TimeoutMinutes int
	// Concurrency is how many builds run at once in the installation (1 to 4);
	// the others wait queued.
	Concurrency int
	// CacheMB is how big an app's build cache may grow before it is emptied
	// (0 to 102400; 0 means no cache).
	CacheMB int
}

// BuildSettings returns the settings (the defaults before the owner saved any).
func (s *Service) BuildSettings(ctx context.Context) (BuildSettings, error) {
	set, err := s.store.GetBuildSettings(ctx)
	if err != nil {
		return BuildSettings{}, err
	}
	return BuildSettings(set), nil
}

// SetBuildSettings checks and stores the settings. A value out of its range is
// an *app.FieldError naming it (memoryMB, cpuMHz, timeoutMinutes, concurrency,
// cacheMB). A change reaches the next build; one going keeps what it started with.
func (s *Service) SetBuildSettings(ctx context.Context, in BuildSettings) (BuildSettings, error) {
	for _, f := range []struct {
		field     string
		v, lo, hi int
		unit      string
	}{
		{"memoryMB", in.MemoryMB, 512, 16384, "MB"},
		{"cpuMHz", in.CPUMHz, 500, 32000, "MHz"},
		{"timeoutMinutes", in.TimeoutMinutes, 5, 180, "minutes"},
		{"concurrency", in.Concurrency, 1, 4, "builds at once"},
		{"cacheMB", in.CacheMB, 0, 102400, "MB (0 means no cache)"},
	} {
		if f.v < f.lo || f.v > f.hi {
			return BuildSettings{}, app.NewFieldError(f.field, "must be from %d to %d %s", f.lo, f.hi, f.unit)
		}
	}
	if err := s.store.SaveBuildSettings(ctx, store.BuildSettings(in)); err != nil {
		return BuildSettings{}, err
	}
	s.buildSlotsChanged() // a higher concurrency lets queued builds start
	return in, nil
}
