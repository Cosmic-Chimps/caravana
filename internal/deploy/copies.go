package deploy

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// CopyStatus is how one copy of a service is doing, in Caravana's words.
type CopyStatus string

// The statuses of a copy (research R8).
const (
	CopyStarting  CopyStatus = "starting"  // placed, not yet passing its checks
	CopyHealthy   CopyStatus = "healthy"   // running and, in a rollout, marked healthy
	CopyUnhealthy CopyStatus = "unhealthy" // running but failing its checks
	CopyStopped   CopyStatus = "stopped"   // finished or told to stop
	CopyFailed    CopyStatus = "failed"    // crashed, or its node was lost
)

// Copy is one Nomad allocation as Caravana shows it. Nomad's types stay
// inside this package (constitution VI), so everything here is a plain value.
type Copy struct {
	ID          string // the Nomad allocation ID
	Service     string // the task group, which is the service name
	Node        string // the node's name
	Description string // Nomad's client description, e.g. "Tasks are running"
	Version     uint64 // the job version
	Status      CopyStatus
	Restarts    int // restarts, summed over the allocation's tasks
	// RequestedRestarts is how many of those Caravana asked for: a volume
	// backup restarts the copy's tasks. Nomad's event "Restart Signaled" says
	// so, and it is counted over the same tasks as Restarts, so that the
	// difference is the restarts nobody asked for (the needs-you list's
	// crash-loop rule). Nomad keeps only each task's last events, so it is an
	// undercount for a copy restarted many times.
	RequestedRestarts int
	StartedAt         *time.Time // the first start of any task; nil while none has started
	LastRestart       *time.Time // the newest restart of any task; nil if none restarted
	FinishedAt        *time.Time // the newest end of any task; nil if none ended. Only meaningful for terminal copies
	OutOfMemory       bool       // the service's last stop was the kernel killing it for passing its memory ceiling
	Terminal          bool       // true for stopped and failed: it will never run again
	// Stopping is true when Nomad has told the copy to stop (its desired
	// status isn't "run") while it may still say it is running. Nomad refuses
	// to restart or exec into such a copy, so the app actions skip it.
	Stopping bool
}

// copyOf maps one allocation to a Copy (research R8):
//
//	client pending                              starting
//	client running, deployment health not set   starting
//	client running, healthy or no deployment    healthy
//	client running, health false                unhealthy
//	client complete                             stopped
//	client failed or lost                       failed
//
// Any other client status (Nomad's "unknown", for a node that dropped out)
// counts as unhealthy: the copy isn't known to be serving, but may come back.
func copyOf(a *api.AllocationListStub) Copy {
	c := Copy{
		ID:          a.ID,
		Service:     a.TaskGroup,
		Node:        a.NodeName,
		Description: a.ClientDescription,
		Version:     a.JobVersion,
	}

	switch a.ClientStatus {
	case api.AllocClientStatusPending:
		c.Status = CopyStarting
	case api.AllocClientStatusRunning:
		switch {
		case a.DeploymentStatus == nil:
			c.Status = CopyHealthy // not part of a rollout: running is healthy
		case a.DeploymentStatus.Healthy == nil:
			c.Status = CopyStarting
		case *a.DeploymentStatus.Healthy:
			c.Status = CopyHealthy
		default:
			c.Status = CopyUnhealthy
		}
	case api.AllocClientStatusComplete:
		c.Status = CopyStopped
	case api.AllocClientStatusFailed, api.AllocClientStatusLost:
		c.Status = CopyFailed
	default:
		c.Status = CopyUnhealthy
	}
	c.Terminal = c.Status == CopyStopped || c.Status == CopyFailed
	c.Stopping = a.DesiredStatus != "" && a.DesiredStatus != api.AllocDesiredStatusRun

	for _, ts := range a.TaskStates {
		if ts == nil {
			continue
		}
		c.Restarts += int(ts.Restarts)
		c.RequestedRestarts += requestedRestarts(ts)
		if !ts.StartedAt.IsZero() && (c.StartedAt == nil || ts.StartedAt.Before(*c.StartedAt)) {
			t := ts.StartedAt
			c.StartedAt = &t
		}
		if !ts.LastRestart.IsZero() && (c.LastRestart == nil || ts.LastRestart.After(*c.LastRestart)) {
			t := ts.LastRestart
			c.LastRestart = &t
		}
		if !ts.FinishedAt.IsZero() && (c.FinishedAt == nil || ts.FinishedAt.After(*c.FinishedAt)) {
			t := ts.FinishedAt
			c.FinishedAt = &t
		}
	}
	c.OutOfMemory = killedForMemory(a.TaskStates[a.TaskGroup])
	return c
}

// requestedRestartMessage is what a task's "Restart Signaled" event says when
// someone asked for all of the allocation's tasks to restart (lab finding L5).
// A restart Nomad decides on itself, after a crash, has no such event.
const requestedRestartMessage = "User requested all tasks to restart"

// requestedRestarts counts the restarts of one task that were asked for.
func requestedRestarts(ts *api.TaskState) int {
	n := 0
	for _, e := range ts.Events {
		if e != nil && e.Type == api.TaskRestartSignal &&
			strings.Contains(e.DisplayMessage+" "+e.Message+" "+e.RestartReason, requestedRestartMessage) {
			n++
		}
	}
	return n
}

// killedForMemory reports whether the service's task last stopped because the
// kernel killed it for passing its memory ceiling: its newest "Terminated"
// event carries oom_killed, which is what the Docker driver records. The
// other tasks of the group (the names sidecar) are not the service.
func killedForMemory(ts *api.TaskState) bool {
	if ts == nil {
		return false
	}
	for i := len(ts.Events) - 1; i >= 0; i-- { // Nomad appends events, newest last
		if e := ts.Events[i]; e != nil && e.Type == api.TaskTerminated {
			return e.Details["oom_killed"] == "true"
		}
	}
	return false
}

// Copies lists jobID's copies: every copy that isn't terminal, plus the
// terminal copies of the newest job version Nomad still has for it, so a
// failed deploy's crashed copies stay visible (research R8). They are ordered
// by service, then version (newest first), then start time.
func (d *Deployer) Copies(ctx context.Context, jobID string) ([]Copy, error) {
	stubs, _, err := d.nomad.Jobs().Allocations(jobID, false, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list copies of "+jobID, err)
	}
	copies := make([]Copy, 0, len(stubs))
	for _, s := range stubs {
		copies = append(copies, copyOf(s))
	}
	return listed(copies), nil
}

// AllCopies lists every job's copies, by job ID, in one request, for the Apps
// page. Each job's list is filtered and ordered as Copies does.
func (d *Deployer) AllCopies(ctx context.Context) (map[string][]Copy, error) {
	stubs, _, err := d.nomad.Allocations().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list copies", err)
	}
	byJob := map[string][]Copy{}
	for _, s := range stubs {
		byJob[s.JobID] = append(byJob[s.JobID], copyOf(s))
	}
	for id, copies := range byJob {
		byJob[id] = listed(copies)
	}
	return byJob, nil
}

// listed keeps the copies worth showing and puts them in display order.
func listed(copies []Copy) []Copy {
	var newest uint64
	for _, c := range copies {
		newest = max(newest, c.Version)
	}
	out := make([]Copy, 0, len(copies))
	for _, c := range copies {
		if !c.Terminal || c.Version == newest {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Copy) int {
		if c := strings.Compare(a.Service, b.Service); c != 0 {
			return c
		}
		if a.Version != b.Version {
			if a.Version > b.Version {
				return -1
			}
			return 1
		}
		switch {
		case a.StartedAt != nil && b.StartedAt != nil:
			if c := a.StartedAt.Compare(*b.StartedAt); c != 0 {
				return c
			}
		case a.StartedAt != nil:
			return -1 // copies that haven't started go last
		case b.StartedAt != nil:
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// WatchCopies calls changed with the job IDs whose allocations changed
// (added, changed or gone), deduplicated and sorted, until ctx ends. It runs
// one blocking query over all allocations (research R6), like Watch does for
// one deployment, with the same backoff after errors. The first answer is the
// starting point: it reports nothing.
func (d *Deployer) WatchCopies(ctx context.Context, changed func(jobIDs []string)) {
	timing := d.Timing.withDefaults()
	var waitIndex uint64
	backoff := timing.BackoffStart
	var seen map[string]allocMark // nil until the first answer

	for {
		start := time.Now()
		q := (&api.QueryOptions{WaitIndex: waitIndex, WaitTime: blockingWait}).WithContext(ctx)
		stubs, meta, err := d.nomad.Allocations().List(q)
		if err != nil {
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(2*backoff, timing.BackoffMax)
			continue
		}
		backoff = timing.BackoffStart
		// Nomad's advice for blocking queries: if the index goes backwards,
		// start again from 0.
		if meta.LastIndex < waitIndex {
			waitIndex = 0
		} else {
			waitIndex = meta.LastIndex
		}

		now := make(map[string]allocMark, len(stubs))
		for _, s := range stubs {
			now[s.ID] = allocMark{jobID: s.JobID, modifyIndex: s.ModifyIndex}
		}
		if seen != nil {
			if jobs := changedJobs(seen, now); len(jobs) > 0 {
				changed(jobs)
			}
		}
		seen = now

		// A minimum gap keeps a server that answers at once from turning
		// this into a busy loop.
		if !sleep(ctx, timing.PollGap-time.Since(start)) {
			return
		}
	}
}

// allocMark is what WatchCopies remembers of an allocation.
type allocMark struct {
	jobID       string
	modifyIndex uint64
}

// changedJobs returns the sorted, distinct job IDs of allocations that were
// added, changed or removed between two answers.
func changedJobs(before, after map[string]allocMark) []string {
	set := map[string]bool{}
	for id, a := range after {
		if b, ok := before[id]; !ok || b.modifyIndex != a.modifyIndex {
			set[a.jobID] = true
		}
	}
	for id, b := range before {
		if _, ok := after[id]; !ok {
			set[b.jobID] = true
		}
	}
	jobs := make([]string, 0, len(set))
	for id := range set {
		jobs = append(jobs, id)
	}
	slices.Sort(jobs)
	return jobs
}
