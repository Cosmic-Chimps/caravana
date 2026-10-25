package metrics_test

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
)

func TestDiskState(t *testing.T) {
	tests := []struct {
		name  string
		pct   float64
		prev  bool
		state metrics.DiskLevel
		open  bool
		level metrics.DiskLevel
	}{
		{"well under", 40, false, metrics.DiskOK, false, ""},
		{"just under 85", 84.99, false, metrics.DiskOK, false, ""},
		{"exactly 85", 85, false, metrics.DiskWarning, true, metrics.DiskWarning},
		{"just over 85", 85.01, false, metrics.DiskWarning, true, metrics.DiskWarning},
		{"just under 95", 94.99, false, metrics.DiskWarning, true, metrics.DiskWarning},
		{"exactly 95", 95, false, metrics.DiskFailed, true, metrics.DiskFailed},
		{"just over 95", 95.01, false, metrics.DiskFailed, true, metrics.DiskFailed},
		{"full", 100, true, metrics.DiskFailed, true, metrics.DiskFailed},
		// Hysteresis: open stays open down to 83 %.
		{"84, was open: stays open, the meter is ok", 84, true, metrics.DiskOK, true, metrics.DiskWarning},
		{"exactly 83, was open: stays open", 83, true, metrics.DiskOK, true, metrics.DiskWarning},
		{"just under 83, was open: resolves", 82.99, true, metrics.DiskOK, false, ""},
		{"82, was open: resolves", 82, true, metrics.DiskOK, false, ""},
		{"84, was closed: stays closed", 84, false, metrics.DiskOK, false, ""},
		{"83, was closed: stays closed", 83, false, metrics.DiskOK, false, ""},
		// The level follows the current percent, not the peak.
		{"back to 90 after 96", 90, true, metrics.DiskWarning, true, metrics.DiskWarning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := metrics.DiskState(tc.pct, tc.prev)
			if got.State != tc.state || got.Open != tc.open || got.Level != tc.level {
				t.Errorf("DiskState(%v, %v) = %+v, want state %s open %v level %s", tc.pct, tc.prev, got, tc.state, tc.open, tc.level)
			}
		})
	}
}

func TestDiskRun(t *testing.T) {
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	series := func(pcts ...float64) []metrics.DiskSample {
		var out []metrics.DiskSample
		for i, p := range pcts {
			out = append(out, metrics.DiskSample{At: at(i), UsedPercent: p})
		}
		return out
	}
	tests := []struct {
		name string
		in   []metrics.DiskSample
		open bool
		when int // minute of the first sample of the run
	}{
		{"nothing", nil, false, 0},
		{"below", series(50, 60, 84.9), false, 0},
		{"85 opens", series(70, 85), true, 1},
		{"86 then 96: one run, the first sample", series(70, 86, 91, 96), true, 1},
		{"84 after 86 stays open since 86", series(70, 86, 84), true, 1},
		{"83 stays", series(86, 83), true, 0},
		{"82 resolves", series(86, 84, 82), false, 0},
		{"82.99 resolves", series(86, 82.99), false, 0},
		{"a new run after resolving begins at its own first sample", series(86, 80, 70, 88), true, 3},
		{"84 never opened, 83.5 neither", series(84, 83.5, 84), false, 0},
		{"hovering at 85.0 doesn't blink", series(85, 84.9, 85, 84.8, 85), true, 0},
		{"history begins inside the band: closed", series(84, 84), false, 0},
		{"opens late in a long history", series(10, 20, 30, 40, 84, 85, 84, 83), true, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			open, when := metrics.DiskRun(tc.in)
			if open != tc.open {
				t.Fatalf("open = %v, want %v", open, tc.open)
			}
			if tc.open && !when.Equal(at(tc.when)) {
				t.Errorf("when = %v, want minute %d", when, tc.when)
			}
			if !tc.open && !when.IsZero() {
				t.Errorf("a closed run has no start, got %v", when)
			}
		})
	}
}

func TestStale(t *testing.T) {
	now := base.Add(time.Hour)
	for _, tc := range []struct {
		age  time.Duration
		want bool
	}{
		{0, false}, {4*time.Minute + 59*time.Second, false}, {5 * time.Minute, false},
		{5*time.Minute + time.Nanosecond, true}, {time.Hour, true},
	} {
		if got := metrics.Stale(now.Add(-tc.age), now); got != tc.want {
			t.Errorf("age %v: Stale = %v, want %v", tc.age, got, tc.want)
		}
	}
}

func TestUsedPercent(t *testing.T) {
	for _, tc := range []struct {
		used, free int64
		want       float64
	}{
		{86, 14, 86}, {18000, 22000, 45}, {0, 100, 0}, {100, 0, 100}, {0, 0, 0},
	} {
		if got := metrics.UsedPercent(tc.used, tc.free); got != tc.want {
			t.Errorf("UsedPercent(%d, %d) = %v, want %v", tc.used, tc.free, got, tc.want)
		}
	}
	// 85 % is exactly the threshold, with no float drift.
	if metrics.DiskState(metrics.UsedPercent(85, 15), false).State != metrics.DiskWarning {
		t.Error("85 used of 100 should be at the warning threshold")
	}
}
