package deploy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// ErrUsageNotCollected is the Err of a copy whose node answered, but has no
// usage to give: the answer has no task and no time, which is what Nomad sends
// when its client isn't collecting task stats (telemetry's
// publish_allocation_metrics is off). It is the host's setup, not a failed call.
var ErrUsageNotCollected = errors.New("nomad isn't collecting usage on this node")

// ErrUsageNone is the Err of a copy whose stats Nomad answered with no
// measurement: a copy that isn't running has an answer with no time and a
// zeroed entry per task, and a task can have no memory kind measured. It is
// no value, never 0 MB (feature 025, lab: research R8.11). It isn't an
// ErrNomadUnavailable: Nomad answered, there is nothing to show.
var ErrUsageNone = errors.New("nomad has no usage for this copy")

// CopyUsage is what one copy's service task uses right now (research R6).
// When Err is set the numbers are zero: Nomad didn't answer for this copy, or
// had nothing to say (ErrUsageNotCollected, ErrUsageNone).
//
// MemoryMB is memory in use without the page cache (Usage less Cache, clamped
// at 0; RSS when Nomad measures no Usage): the number to compare with a
// ceiling, since the kernel gives cache back before it kills anything.
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
// Memory is Usage less Cache when Nomad measured Usage (cgroups v2, Cache is
// the reclaimable page cache), else RSS (cgroups v1, which has no page cache
// in it). An answer with no time (Timestamp 0) or a task with no memory kind
// measured is ErrUsageNone, not a zero.
func usageFromStats(c Copy, stats *api.AllocResourceUsage) CopyUsage {
	if len(stats.Tasks) == 0 && stats.Timestamp == 0 {
		return CopyUsage{Err: ErrUsageNotCollected}
	}
	if stats.Timestamp == 0 {
		return CopyUsage{Err: ErrUsageNone}
	}
	task := stats.Tasks[c.Service]
	if task == nil || task.ResourceUsage == nil {
		return CopyUsage{Err: fmt.Errorf("%w: no usage reported for task %q of copy %s", ErrNomadUnavailable, c.Service, c.ID)}
	}
	mem := task.ResourceUsage.MemoryStats
	var bytes uint64
	switch {
	case mem == nil:
		return CopyUsage{Err: ErrUsageNone}
	case slices.Contains(mem.Measured, "Usage"):
		bytes = mem.Usage
		if slices.Contains(mem.Measured, "Cache") {
			bytes -= min(mem.Cache, bytes) // clamped at 0
		}
	case slices.Contains(mem.Measured, "RSS"):
		bytes = mem.RSS
	default:
		return CopyUsage{Err: ErrUsageNone}
	}
	u := CopyUsage{MemoryMB: int(math.Round(float64(bytes) / (1 << 20)))}
	if cpu := task.ResourceUsage.CpuStats; cpu != nil {
		u.CPUMHz = int(math.Round(cpu.TotalTicks))
	}
	return u
}
