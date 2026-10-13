package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const mb = 1 << 20

// startedStub is a running copy whose service task has started, so usage is read.
func startedStub(id, group string) api.AllocationListStub {
	return api.AllocationListStub{ID: id, TaskGroup: group, JobVersion: 1, ClientStatus: "running", NodeName: "node-1",
		TaskStates: map[string]*api.TaskState{group: {State: "running", StartedAt: time.Now()}}}
}

// statsFor is Nomad's stats for the service task: ticks MHz and usedMB of memory.
func statsFor(group string, ticks float64, usedMB uint64) api.AllocResourceUsage {
	return api.AllocResourceUsage{Tasks: map[string]*api.TaskResourceUsage{group: {ResourceUsage: &api.ResourceUsage{
		CpuStats:    &api.CpuStats{TotalTicks: ticks},
		MemoryStats: &api.MemoryStats{Usage: usedMB * mb, Measured: []string{"Usage"}},
	}}}}
}

func saveApp(t *testing.T, e *env, a app.App) {
	t.Helper()
	if _, err := e.store.SaveApp(context.Background(), a); err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
}

func TestCopiesWithUsage(t *testing.T) {
	ctx := context.Background()

	t.Run("defaults are applied to a service with no resources", func(t *testing.T) {
		e := newEnv(t)
		saveApp(t, e, appWith("blog", 1))
		e.nomad.SetCopies("blog", startedStub("c1", "web"))
		e.nomad.SetStats("c1", statsFor("web", 12.4, 30))

		copies, usage, err := e.svc.CopiesWithUsage(ctx, "blog")
		if err != nil || len(copies) != 1 {
			t.Fatalf("CopiesWithUsage = %v, %v", copies, err)
		}
		// newDeployer's settings: 100 MHz, 64 MB, ceiling 256 MB.
		want := CopyUsage{CPUMHz: 12, MemoryMB: 30, CPUReservedMHz: 100, MemoryReservedMB: 64, MemoryCeilingMB: 256}
		if usage["c1"] != want {
			t.Errorf("usage = %+v, want %+v", usage["c1"], want)
		}
	})

	t.Run("the service's own values are used", func(t *testing.T) {
		e := newEnv(t)
		a := appWith("blog", 1)
		a.Services[0].Resources = app.Resources{CPUMHz: 500, MemoryMB: 128, MemoryMaxMB: 512}
		saveApp(t, e, a)
		e.nomad.SetCopies("blog", startedStub("c1", "web"))
		e.nomad.SetStats("c1", statsFor("web", 5, 10))

		_, usage, err := e.svc.CopiesWithUsage(ctx, "blog")
		if err != nil {
			t.Fatal(err)
		}
		if u := usage["c1"]; u.CPUReservedMHz != 500 || u.MemoryReservedMB != 128 || u.MemoryCeilingMB != 512 {
			t.Errorf("usage = %+v", u)
		}
	})

	t.Run("close to the ceiling starts at 90 percent", func(t *testing.T) {
		tests := []struct {
			usedMB uint64
			near   bool
		}{{225, false}, {230, false}, {231, true}, {256, true}} // 90% of 256 MB is 230.4
		for _, tc := range tests {
			e := newEnv(t)
			saveApp(t, e, appWith("blog", 1))
			e.nomad.SetCopies("blog", startedStub("c1", "web"))
			e.nomad.SetStats("c1", statsFor("web", 1, tc.usedMB))
			_, usage, err := e.svc.CopiesWithUsage(ctx, "blog")
			if err != nil {
				t.Fatal(err)
			}
			if got := usage["c1"].NearCeiling; got != tc.near {
				t.Errorf("%d MB of 256: NearCeiling = %v, want %v", tc.usedMB, got, tc.near)
			}
		}
	})

	t.Run("the boundary is exact", func(t *testing.T) {
		e := newEnv(t)
		a := appWith("blog", 1)
		a.Services[0].Resources = app.Resources{MemoryMB: 100, MemoryMaxMB: 100}
		saveApp(t, e, a)
		e.nomad.SetCopies("blog", startedStub("c1", "web"))
		for used, near := range map[uint64]bool{89: false, 90: true} {
			e.nomad.SetStats("c1", statsFor("web", 1, used))
			_, usage, err := e.svc.CopiesWithUsage(ctx, "blog")
			if err != nil {
				t.Fatal(err)
			}
			if got := usage["c1"].NearCeiling; got != near {
				t.Errorf("%d of 100 MB: NearCeiling = %v, want %v", used, got, near)
			}
		}
	})

	t.Run("a copy Nomad can't answer for has an error and the others their numbers", func(t *testing.T) {
		e := newEnv(t)
		saveApp(t, e, appWith("blog", 2))
		e.nomad.SetCopies("blog", startedStub("c1", "web"), startedStub("c2", "web"))
		e.nomad.SetStats("c2", statsFor("web", 7, 70)) // c1 has no stats: 404

		_, usage, err := e.svc.CopiesWithUsage(ctx, "blog")
		if err != nil {
			t.Fatal(err)
		}
		if u := usage["c1"]; u.Error != "Nomad didn't answer for this copy." || u.MemoryMB != 0 || u.MemoryCeilingMB != 256 {
			t.Errorf("c1 = %+v", u)
		}
		if u := usage["c2"]; u.Error != "" || u.MemoryMB != 70 || u.CPUMHz != 7 {
			t.Errorf("c2 = %+v", u)
		}
	})

	t.Run("copies that haven't started have no entry", func(t *testing.T) {
		e := newEnv(t)
		saveApp(t, e, appWith("blog", 1))
		e.nomad.SetCopies("blog", api.AllocationListStub{ID: "c1", TaskGroup: "web", JobVersion: 1, ClientStatus: "pending"})
		copies, usage, err := e.svc.CopiesWithUsage(ctx, "blog")
		if err != nil || len(copies) != 1 || len(usage) != 0 {
			t.Errorf("copies %d, usage %v, err %v", len(copies), usage, err)
		}
		if n := e.nomad.StatsRequests(); n != 0 {
			t.Errorf("stats requests = %d, want 0", n)
		}
	})

	t.Run("a service the stored app no longer has has no entry", func(t *testing.T) {
		e := newEnv(t)
		saveApp(t, e, appWith("blog", 1))
		e.nomad.SetCopies("blog", startedStub("c1", "worker"))
		e.nomad.SetStats("c1", statsFor("worker", 1, 1))
		_, usage, err := e.svc.CopiesWithUsage(ctx, "blog")
		if err != nil || len(usage) != 0 {
			t.Errorf("usage = %v, err %v", usage, err)
		}
	})

	t.Run("an unknown app is not found, and Nomad is not asked", func(t *testing.T) {
		e := newEnv(t)
		_, _, err := e.svc.CopiesWithUsage(ctx, "ghost")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		if n := e.nomad.StatsRequests(); n != 0 {
			t.Errorf("stats requests = %d", n)
		}
	})
}
