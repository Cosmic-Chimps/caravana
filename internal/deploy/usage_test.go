package deploy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

const mb = 1 << 20

// taskUsage is Nomad's stats for one task.
func taskUsage(ticks float64, mem api.MemoryStats) *api.TaskResourceUsage {
	return &api.TaskResourceUsage{ResourceUsage: &api.ResourceUsage{
		CpuStats:    &api.CpuStats{TotalTicks: ticks},
		MemoryStats: &mem,
	}}
}

func runningCopy(id, service string) Copy {
	t := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	return Copy{ID: id, Service: service, Status: CopyHealthy, StartedAt: &t}
}

func TestUsageFromStats(t *testing.T) {
	tests := []struct {
		name  string
		tasks map[string]*api.TaskResourceUsage
		want  CopyUsage
	}{
		{"cgroups v2 measures Usage", map[string]*api.TaskResourceUsage{
			"web": taskUsage(41.6, api.MemoryStats{Usage: 120 * mb, RSS: 80 * mb, Measured: []string{"Usage", "Cache"}}),
		}, CopyUsage{CPUMHz: 42, MemoryMB: 120}},
		{"cgroups v1 measures RSS", map[string]*api.TaskResourceUsage{
			"web": taskUsage(7, api.MemoryStats{RSS: 64 * mb, Usage: 999 * mb, Measured: []string{"RSS", "Cache", "Swap"}}),
		}, CopyUsage{CPUMHz: 7, MemoryMB: 64}},
		{"the sidecar is not the service", map[string]*api.TaskResourceUsage{
			"names": taskUsage(500, api.MemoryStats{Usage: 900 * mb, Measured: []string{"Usage"}}),
			"web":   taskUsage(3, api.MemoryStats{Usage: 10 * mb, Measured: []string{"Usage"}}),
		}, CopyUsage{CPUMHz: 3, MemoryMB: 10}},
		{"memory rounds to the nearest MB", map[string]*api.TaskResourceUsage{
			"web": taskUsage(0, api.MemoryStats{Usage: 10*mb + mb/2, Measured: []string{"Usage"}}),
		}, CopyUsage{MemoryMB: 11}},
		{"a task with no stats of one kind", map[string]*api.TaskResourceUsage{
			"web": {ResourceUsage: &api.ResourceUsage{}},
		}, CopyUsage{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := usageFromStats(runningCopy("c1", "web"), &api.AllocResourceUsage{Tasks: tc.tasks})
			if got != tc.want {
				t.Errorf("usage = %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("an answer with no task and no time says Nomad isn't collecting", func(t *testing.T) {
		got := usageFromStats(runningCopy("c1", "web"), &api.AllocResourceUsage{ResourceUsage: &api.ResourceUsage{}})
		if !errors.Is(got.Err, ErrUsageNotCollected) || errors.Is(got.Err, ErrNomadUnavailable) {
			t.Errorf("Err = %v, want ErrUsageNotCollected only", got.Err)
		}
	})

	t.Run("no stats for the service's task is an error", func(t *testing.T) {
		got := usageFromStats(runningCopy("c1", "web"), &api.AllocResourceUsage{Tasks: map[string]*api.TaskResourceUsage{
			"names": taskUsage(1, api.MemoryStats{}),
		}})
		if !errors.Is(got.Err, ErrNomadUnavailable) {
			t.Errorf("Err = %v, want ErrNomadUnavailable", got.Err)
		}
	})
}

func TestUsage(t *testing.T) {
	stats := func(ticks float64, usageMB uint64) api.AllocResourceUsage {
		return api.AllocResourceUsage{Tasks: map[string]*api.TaskResourceUsage{
			"web":   taskUsage(ticks, api.MemoryStats{Usage: usageMB * mb, Measured: []string{"Usage"}}),
			"names": taskUsage(1000, api.MemoryStats{Usage: 800 * mb, Measured: []string{"Usage"}}),
		}}
	}

	t.Run("reads each running copy's service task", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetStats("c1", stats(10, 100))
		nomad.SetStats("c2", stats(20, 200))
		got := d.Usage(context.Background(), []Copy{runningCopy("c1", "web"), runningCopy("c2", "web")})
		want := map[string]CopyUsage{"c1": {CPUMHz: 10, MemoryMB: 100}, "c2": {CPUMHz: 20, MemoryMB: 200}}
		if len(got) != 2 || got["c1"] != want["c1"] || got["c2"] != want["c2"] {
			t.Errorf("usage = %+v, want %+v", got, want)
		}
	})

	t.Run("one copy failing doesn't affect the others", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetStats("c1", stats(10, 100))
		// c2 has no stats: the fake answers 404.
		nomad.SetStats("c3", stats(30, 300))
		got := d.Usage(context.Background(), []Copy{runningCopy("c1", "web"), runningCopy("c2", "web"), runningCopy("c3", "web")})
		if got["c1"].Err != nil || got["c1"].MemoryMB != 100 || got["c3"].Err != nil || got["c3"].MemoryMB != 300 {
			t.Errorf("c1, c3 = %+v, %+v", got["c1"], got["c3"])
		}
		if !errors.Is(got["c2"].Err, ErrNomadUnavailable) {
			t.Errorf("c2 = %+v, want an ErrNomadUnavailable error", got["c2"])
		}
	})

	t.Run("a slow copy times out alone", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		d.Timing.UsageTimeout = 30 * time.Millisecond
		nomad.SetStats("c1", stats(10, 100))
		nomad.SetStatsDelay(time.Second)
		start := time.Now()
		got := d.Usage(context.Background(), []Copy{runningCopy("c1", "web")})
		if got["c1"].Err == nil {
			t.Errorf("usage = %+v, want an error", got["c1"])
		}
		if took := time.Since(start); took > 500*time.Millisecond {
			t.Errorf("took %s, want about the timeout", took)
		}
	})

	t.Run("ended and not yet started copies are skipped", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetStats("run", stats(1, 1))
		stopped, failed, pending := runningCopy("stopped", "web"), runningCopy("failed", "web"), runningCopy("pending", "web")
		stopped.Status, stopped.Terminal = CopyStopped, true
		failed.Status, failed.Terminal = CopyFailed, true
		pending.Status, pending.StartedAt = CopyStarting, nil
		got := d.Usage(context.Background(), []Copy{stopped, failed, pending, runningCopy("run", "web")})
		if len(got) != 1 || got["run"].Err != nil {
			t.Errorf("usage = %+v, want only run", got)
		}
		if n := nomad.StatsRequests(); n != 1 {
			t.Errorf("stats requests = %d, want 1", n)
		}
	})

	t.Run("at most four requests are open at once", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetStatsDelay(40 * time.Millisecond)
		var copies []Copy
		for i := range 12 {
			id := fmt.Sprintf("c%d", i)
			nomad.SetStats(id, stats(1, 1))
			copies = append(copies, runningCopy(id, "web"))
		}
		got := d.Usage(context.Background(), copies)
		if len(got) != 12 {
			t.Fatalf("usage for %d copies, want 12", len(got))
		}
		if peak := nomad.MaxStatsInFlight(); peak > 4 || peak < 2 {
			t.Errorf("most in flight = %d, want 2 to 4", peak)
		}
	})

	t.Run("a node that doesn't collect is told apart from a failed call", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetStats("c1", api.AllocResourceUsage{}) // 200, no tasks, Timestamp 0
		got := d.Usage(context.Background(), []Copy{runningCopy("c1", "web")})
		if !errors.Is(got["c1"].Err, ErrUsageNotCollected) {
			t.Errorf("usage = %+v, want ErrUsageNotCollected", got["c1"])
		}
	})

	t.Run("Nomad down is an error for each copy", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		got := d.Usage(context.Background(), []Copy{runningCopy("c1", "web")})
		if !errors.Is(got["c1"].Err, ErrNomadUnavailable) {
			t.Errorf("usage = %+v", got["c1"])
		}
	})
}

func TestReserved(t *testing.T) {
	d, _, _ := newTestDeployer(t)
	cpu, mem, ceiling := d.Reserved(app.Resources{})
	if cpu != labSettings.DefaultCPUMHz || mem != labSettings.DefaultMemoryMB ||
		ceiling != max(labSettings.DefaultMemoryMB, labSettings.DefaultMemoryMaxMB) {
		t.Errorf("defaults = %d, %d, %d", cpu, mem, ceiling)
	}
	cpu, mem, ceiling = d.Reserved(app.Resources{CPUMHz: 300, MemoryMB: 128, MemoryMaxMB: 512})
	if cpu != 300 || mem != 128 || ceiling != 512 {
		t.Errorf("explicit = %d, %d, %d", cpu, mem, ceiling)
	}
}
