package deploy

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
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
	// ExitCode is the exit status in the service task's newest end event
	// (Terminated); nil when its newest end is a start failure, or it has none.
	// StartFailed says that newest end is a start failure (the image couldn't
	// be pulled, the program isn't there: Driver or Setup Failure) and the task
	// never ran after it. The server page says why a failed copy failed from
	// these two (feature 027); neither carries Nomad's text.
	ExitCode    *int
	StartFailed bool
	Terminal    bool // true for stopped and failed: it will never run again
	// Replaced is true when Nomad has already made another copy to take this one's
	// place (the stub's NextAllocation is set): a rescheduled failure. PreviousID is
	// the copy this one replaced (the newest event of its reschedule tracker), ""
	// for the first of its lineage. Together they let a list keep the newest copy of
	// each lineage (the server page, feature 027).
	Replaced   bool
	PreviousID string
	// Stopping is true when Nomad has told the copy to stop (its desired
	// status isn't "run") while it may still say it is running. Nomad refuses
	// to restart or exec into such a copy, so the app actions skip it.
	Stopping bool
	// Gone is set only by LogCopies, on the copies that are terminal: they
	// no longer run, but Nomad still keeps their output (feature 024).
	Gone bool
}

// copyOf maps one allocation to a Copy (research R8):
//
//	client pending                                        starting
//	client running, deployment health not set             starting
//	client running, no deployment status, its version's
//	  deployment still running                            starting
//	client running, healthy or no deployment              healthy
//	client running, health false                          unhealthy
//	client complete                                       stopped
//	client failed or lost                                 failed
//
// Nomad leaves a copy's DeploymentStatus nil until its health is decided, and the
// list stub doesn't say which deployment it belongs to, so a nil status alone
// can't tell "not part of a rollout" from "a rollout that hasn't decided yet".
// active (the job versions with a deployment still going) tells them apart: nil
// means none is known, and a nil status is then read as healthy.
//
// Any other client status (Nomad's "unknown", for a node that dropped out)
// counts as unhealthy: the copy isn't known to be serving, but may come back.
func copyOf(a *api.AllocationListStub, active rollouts) Copy {
	c := Copy{
		ID:          a.ID,
		Service:     a.TaskGroup,
		Node:        a.NodeName,
		Description: a.ClientDescription,
		Version:     a.JobVersion,
		Replaced:    a.NextAllocation != "",
		PreviousID:  previousOf(a),
	}

	switch a.ClientStatus {
	case api.AllocClientStatusPending:
		c.Status = CopyStarting
	case api.AllocClientStatusRunning:
		switch {
		case a.DeploymentStatus == nil && active[jobVersion{a.JobID, a.JobVersion}]:
			c.Status = CopyStarting // its rollout hasn't decided its health yet
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
	c.ExitCode, c.StartFailed = lastEnd(a.TaskStates[a.TaskGroup])
	return c
}

// lastEnd is how the service's task last ended: the exit status of its newest
// Terminated event, or startFailed when a Driver Failure or Setup Failure came
// after it (or it never ended at all). Nomad appends events, newest last.
func lastEnd(ts *api.TaskState) (exit *int, startFailed bool) {
	if ts == nil {
		return nil, false
	}
	for i := len(ts.Events) - 1; i >= 0; i-- {
		e := ts.Events[i]
		switch {
		case e == nil:
		case isStartFailure(e):
			return nil, true
		case e.Type == api.TaskTerminated:
			if code, err := strconv.Atoi(e.Details["exit_code"]); err == nil {
				return &code, false
			}
			return nil, false
		}
	}
	return nil, false
}

// previousOf is the copy a rescheduled copy replaced, "" if it replaced none.
func previousOf(a *api.AllocationListStub) string {
	if a.RescheduleTracker == nil || len(a.RescheduleTracker.Events) == 0 {
		return ""
	}
	if last := a.RescheduleTracker.Events[len(a.RescheduleTracker.Events)-1]; last != nil {
		return last.PrevAllocID
	}
	return ""
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
//
// A copy's status needs more than its stub says (see copyOf and settle): the
// job's latest deployment, and for a copy running outside a rollout, its
// service checks.
func (d *Deployer) Copies(ctx context.Context, jobID string) ([]Copy, error) {
	stubs, _, err := d.nomad.Jobs().Allocations(jobID, false, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list copies of "+jobID, err)
	}
	dep, _, err := d.nomad.Jobs().LatestDeployment(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("read the latest deployment of "+jobID, err)
	}
	active := rollouts{}
	active.add(dep)
	return listed(d.copiesOf(ctx, stubs, active)), nil
}

// AllCopies lists every job's copies, by job ID, in one request, for the Apps
// page. Each job's list is filtered and ordered as Copies does.
func (d *Deployer) AllCopies(ctx context.Context) (map[string][]Copy, error) {
	stubs, _, err := d.nomad.Allocations().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list copies", err)
	}
	deps, _, err := d.nomad.Deployments().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list deployments", err)
	}
	active := rollouts{}
	for _, dep := range deps {
		active.add(dep)
	}
	copies := d.copiesOf(ctx, stubs, active)
	byJob := map[string][]Copy{}
	for i, s := range stubs {
		byJob[s.JobID] = append(byJob[s.JobID], copies[i])
	}
	for id, list := range byJob {
		byJob[id] = listed(list)
	}
	return byJob, nil
}

// copiesOf maps stubs to copies, in the same order, and settles the status of
// the running ones that no rollout speaks for (settle).
func (d *Deployer) copiesOf(ctx context.Context, stubs []*api.AllocationListStub, active rollouts) []Copy {
	copies := make([]Copy, len(stubs))
	for i, s := range stubs {
		copies[i] = copyOf(s, active)
	}
	d.settle(ctx, copies, stubs, active)
	return copies
}

// The limits of the check reads: how many run at once and how long each may take.
// A slow client delays the list by one timeout at most, and never fails it.
const (
	checkReads   = 8
	checkTimeout = 3 * time.Second
)

// settle corrects the status of copies that run outside the rollout of their own
// job version (a rescheduled copy, one Caravana restarted, one back after its node
// returned). For them the deployment status says nothing about now: its health
// was decided at the copy's first start, or never. What says it is the copy's
// service checks (Nomad reads them on the client). Only a copy that would
// otherwise be healthy, whose group has a service with checks, is read; an error
// for one copy keeps its status and never fails the list.
func (d *Deployer) settle(ctx context.Context, copies []Copy, stubs []*api.AllocationListStub, active rollouts) {
	var todo []int
	for i, c := range copies {
		key := jobVersion{stubs[i].JobID, c.Version}
		if c.Status != CopyHealthy || active[key] {
			continue
		}
		if has, err := d.groupHasChecks(ctx, key, c.Service); err == nil && has {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 {
		return
	}
	now := d.cache.clock()
	sem := make(chan struct{}, checkReads)
	var wg sync.WaitGroup
	for _, i := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			checks, err := d.nomad.Allocations().Checks(copies[i].ID, (&api.QueryOptions{}).WithContext(cctx))
			if err != nil {
				return // keep today's rule for this copy
			}
			copies[i].Status = statusByChecks(copies[i], checks, now)
		}()
	}
	wg.Wait()
}

// statusByChecks is the status of a copy running outside a rollout, from its
// service checks (Nomad's "success", "failure" and "pending"): all passing is
// healthy; any other result is starting while the copy's last start or restart is
// within the healthy deadline a deploy allows (nomadjob.HealthyDeadline), and
// unhealthy after it. A copy with no checks keeps its status. It is pure.
func statusByChecks(c Copy, checks api.AllocCheckStatuses, now time.Time) CopyStatus {
	passing := true
	for _, ch := range checks {
		if ch.Status != "success" {
			passing = false
		}
	}
	if passing {
		return c.Status
	}
	var last *time.Time
	for _, t := range []*time.Time{c.StartedAt, c.LastRestart} {
		if t != nil && (last == nil || t.After(*last)) {
			last = t
		}
	}
	if last == nil || now.Sub(*last) < nomadjob.HealthyDeadline {
		return CopyStarting
	}
	return CopyUnhealthy
}

// rollouts is the job versions that have a deployment still going.
type rollouts map[jobVersion]bool

// add notes dep when Nomad hasn't finished with it: running, or waiting on
// something (a promotion, a pause, an earlier rollout).
func (r rollouts) add(dep *api.Deployment) {
	if dep == nil {
		return
	}
	switch dep.Status {
	case api.DeploymentStatusRunning, api.DeploymentStatusPending, api.DeploymentStatusPaused,
		api.DeploymentStatusBlocked, api.DeploymentStatusUnblocking, "initializing":
		r[jobVersion{dep.JobID, dep.JobVersion}] = true
	}
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
	sortCopies(out)
	return out
}

// sortCopies puts copies in display order: by service, then version (newest
// first), then start time (copies that haven't started last), then ID.
func sortCopies(copies []Copy) {
	slices.SortFunc(copies, func(a, b Copy) int {
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
