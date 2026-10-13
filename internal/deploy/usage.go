package deploy

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// CopyUsage is what one copy's service task uses right now (research R6).
// When Err is set the numbers are zero: Nomad didn't answer for this copy.
type CopyUsage struct {
	CPUMHz   int
	MemoryMB int
	Err      error
}

// usageParallel is how many stats requests Usage has open at once. Each one
// goes to the node that runs the copy, so a few at a time is enough.
const usageParallel = 4

// defaultUsageTimeout bounds one copy's stats request, so one slow node can't
// hold the page up.
const defaultUsageTimeout = 2 * time.Second

// Reserved is what a service reserves, with the defaults applied: its CPU in
// MHz, its memory in MB, and the memory ceiling in MB. It is the same rule
// the job is built with.
func (d *Deployer) Reserved(r app.Resources) (cpuMHz, memoryMB, memoryMaxMB int) {
	return nomadjob.Reserved(r, d.settings)
}

// Usage reads the CPU and memory in use for each of the copies that is running
// (research R6), keyed by copy ID. Copies that have ended or have not started
// a task get no entry. It reads the service's own task, not the whole
// allocation, so the names sidecar doesn't count against the service.
//
// Nomad is asked once per copy, at most usageParallel at a time, each with its
// own timeout. A copy Nomad can't answer for gets an entry with Err set (as
// callError builds it); the others are not affected. Usage returns no error of
// its own. When ctx ends, the copies not yet answered have an Err.
func (d *Deployer) Usage(ctx context.Context, copies []Copy) map[string]CopyUsage {
	var (
		mu  sync.Mutex
		out = map[string]CopyUsage{}
		wg  sync.WaitGroup
		sem = make(chan struct{}, usageParallel) // a buffered channel as a semaphore
	)
	timeout := d.Timing.UsageTimeout
	if timeout == 0 {
		timeout = defaultUsageTimeout
	}
	for _, c := range copies {
		if c.Terminal || c.StartedAt == nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{} // blocks while usageParallel requests are open
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			u := d.usageOf(ctx, c, timeout)
			mu.Lock()
			out[c.ID] = u
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// usageOf asks Nomad for one copy's stats.
func (d *Deployer) usageOf(ctx context.Context, c Copy, timeout time.Duration) CopyUsage {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stats, err := d.nomad.Allocations().Stats(&api.Allocation{ID: c.ID}, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return CopyUsage{Err: d.callError("read usage of copy "+c.ID, err)}
	}
	return usageFromStats(c, stats)
}

// usageFromStats picks the service task's numbers out of Nomad's answer.
// Memory is Usage when Nomad measured it (cgroups v2), else RSS (cgroups v1).
func usageFromStats(c Copy, stats *api.AllocResourceUsage) CopyUsage {
	task := stats.Tasks[c.Service]
	if task == nil || task.ResourceUsage == nil {
		return CopyUsage{Err: fmt.Errorf("%w: no usage reported for task %q of copy %s", ErrNomadUnavailable, c.Service, c.ID)}
	}
	var u CopyUsage
	if cpu := task.ResourceUsage.CpuStats; cpu != nil {
		u.CPUMHz = int(math.Round(cpu.TotalTicks))
	}
	if mem := task.ResourceUsage.MemoryStats; mem != nil {
		bytes := mem.RSS
		if slices.Contains(mem.Measured, "Usage") {
			bytes = mem.Usage
		}
		u.MemoryMB = int(math.Round(float64(bytes) / (1 << 20)))
	}
	return u
}
