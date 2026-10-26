package deploy

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// Reservation is one live copy and what Nomad holds for it (feature 025,
// research R3). The sampler maps JobID and Group to a stored app and service;
// a copy it can't map still counts in the host's total.
type Reservation struct {
	CopyID  string
	JobID   string // the Nomad job: the app's ID for a Caravana app
	Group   string // the task group: the service's name
	NodeID  string
	Version uint64
	Status  CopyStatus

	// CPUMHz and MemoryMB are the copy's reservation after Nomad's lifecycle
	// rule (FlattenReserved): the one number the host's capacity is measured
	// with. The memory is the reservation, not the ceiling (memory_max): with
	// oversubscription on, Nomad places by the reservation.
	CPUMHz   int
	MemoryMB int

	// ServiceMemoryMB and ServiceMemoryMaxMB are the service's own task (the
	// task named like the group) alone: its reservation and its ceiling, which
	// is 0 when the task sets none. ServiceMemoryLimitMB is the one to use.
	ServiceMemoryMB    int
	ServiceMemoryMaxMB int

	Restarts          int
	RequestedRestarts int
	// ExitCode and ExitedAt come from the service task's newest Terminated
	// event; nil when it has none (or no exit code).
	ExitCode *int
	ExitedAt *time.Time
}

// ServiceMemoryLimitMB is what the service's task may use: its ceiling when it
// has one, else its reservation.
func (r Reservation) ServiceMemoryLimitMB() int {
	if r.ServiceMemoryMaxMB > r.ServiceMemoryMB {
		return r.ServiceMemoryMaxMB
	}
	return r.ServiceMemoryMB
}

// ServiceKey names one service of one job.
type ServiceKey struct{ JobID, Group string }

// Reserved is a sum of reservations over copies.
type Reserved struct {
	CPUMHz   int
	MemoryMB int
	Copies   int
}

// Reservations is every live copy's reservation and the sums: over all of
// them (what Nomad calls allocated, and what the host's capacity is measured
// against) and per job's service.
type Reservations struct {
	Copies []Reservation // ordered by job, group, then copy ID
	Total  Reserved
	ByKey  map[ServiceKey]Reserved
}

// jobVersion keys the cache of a job's task lifecycles.
type jobVersion struct {
	job     string
	version uint64
}

// taskHooks is a job version's tasks' lifecycles: group, then task. A task with
// no lifecycle block is nil.
type taskHooks map[string]map[string]*api.TaskLifecycle

// TaskReservation is one task's reservation and when it runs, for
// FlattenReserved. Hook is "" for a main task, else Nomad's "prestart",
// "poststart" or "poststop".
type TaskReservation struct {
	CPUMHz, MemoryMB int
	Hook             string
	Sidecar          bool
}

// FlattenReserved is what a copy reserves in Nomad's eyes (the rule of
// structs.AllocatedResources.Comparable, checked against the lab's
// nomad.client.allocated gauges). The tasks of a copy don't all run together:
// prestart tasks run first and the ephemeral ones end before the main tasks
// start, and a poststop task runs alone at the end. So per dimension the
// copy's reservation is the largest of three sums:
//
//	prestart: the prestart tasks, sidecar or not
//	main:     the main tasks, the poststart tasks and the prestart sidecars
//	stop:     the poststop tasks
//
// A job with a prestart task that ends before main (Caravana's
// caravana-volumes) isn't charged for it on top of the main tasks, which is
// what a plain sum would do. It is pure.
func FlattenReserved(tasks []TaskReservation) (cpuMHz, memoryMB int) {
	var pre, main, stop Reserved
	for _, t := range tasks {
		switch {
		case t.Hook == api.TaskLifecycleHookPrestart && t.Sidecar:
			pre.CPUMHz += t.CPUMHz
			pre.MemoryMB += t.MemoryMB
			main.CPUMHz += t.CPUMHz
			main.MemoryMB += t.MemoryMB
		case t.Hook == api.TaskLifecycleHookPrestart:
			pre.CPUMHz += t.CPUMHz
			pre.MemoryMB += t.MemoryMB
		case t.Hook == api.TaskLifecycleHookPoststop:
			stop.CPUMHz += t.CPUMHz
			stop.MemoryMB += t.MemoryMB
		default: // a main task or a poststart one
			main.CPUMHz += t.CPUMHz
			main.MemoryMB += t.MemoryMB
		}
	}
	return max(pre.CPUMHz, main.CPUMHz, stop.CPUMHz), max(pre.MemoryMB, main.MemoryMB, stop.MemoryMB)
}

// Reservations reads every copy that isn't over (starting, running, unhealthy)
// in one Allocations().List, with each copy's resources, and says what each
// reserves. The tasks' lifecycles are not in that list: they come from the
// job (Jobs().Info), once per job version, and a job that is gone counts its
// tasks as main tasks. Errors are ErrNomadUnavailable.
func (d *Deployer) Reservations(ctx context.Context) (Reservations, error) {
	q := (&api.QueryOptions{Params: map[string]string{"resources": "true"}}).WithContext(ctx)
	stubs, _, err := d.nomad.Allocations().List(q)
	if err != nil {
		return Reservations{}, d.callError("list copies and their resources", err)
	}

	out := Reservations{ByKey: map[ServiceKey]Reserved{}}
	seen := map[jobVersion]bool{}
	for _, s := range stubs {
		c := copyOf(s)
		if c.Terminal {
			continue
		}
		key := jobVersion{s.JobID, s.JobVersion}
		seen[key] = true
		hooks, err := d.lifecycles(ctx, key)
		if err != nil {
			return Reservations{}, err
		}
		r := reservationOf(s, c, hooks[s.TaskGroup])
		out.Copies = append(out.Copies, r)
		out.Total.CPUMHz += r.CPUMHz
		out.Total.MemoryMB += r.MemoryMB
		out.Total.Copies++
		k := ServiceKey{s.JobID, s.TaskGroup}
		sum := out.ByKey[k]
		sum.CPUMHz += r.CPUMHz
		sum.MemoryMB += r.MemoryMB
		sum.Copies++
		out.ByKey[k] = sum
	}
	d.forgetLifecycles(seen)

	slices.SortFunc(out.Copies, func(a, b Reservation) int {
		if a.JobID != b.JobID {
			return strings.Compare(a.JobID, b.JobID)
		}
		if a.Group != b.Group {
			return strings.Compare(a.Group, b.Group)
		}
		return strings.Compare(a.CopyID, b.CopyID)
	})
	return out, nil
}

// reservationOf builds one copy's Reservation from its list stub, with the
// lifecycles of its group's tasks (nil when unknown: every task is main).
func reservationOf(s *api.AllocationListStub, c Copy, group map[string]*api.TaskLifecycle) Reservation {
	r := Reservation{
		CopyID: s.ID, JobID: s.JobID, Group: s.TaskGroup, NodeID: s.NodeID,
		Version: s.JobVersion, Status: c.Status,
		Restarts: c.Restarts, RequestedRestarts: c.RequestedRestarts,
	}
	if s.AllocatedResources != nil {
		var tasks []TaskReservation
		for name, t := range s.AllocatedResources.Tasks {
			if t == nil {
				continue
			}
			tr := TaskReservation{CPUMHz: int(t.Cpu.CpuShares), MemoryMB: int(t.Memory.MemoryMB)}
			if lc := group[name]; lc != nil {
				tr.Hook, tr.Sidecar = lc.Hook, lc.Sidecar
			}
			tasks = append(tasks, tr)
		}
		r.CPUMHz, r.MemoryMB = FlattenReserved(tasks)
		if svc := s.AllocatedResources.Tasks[s.TaskGroup]; svc != nil {
			r.ServiceMemoryMB = int(svc.Memory.MemoryMB)
			r.ServiceMemoryMaxMB = int(svc.Memory.MemoryMaxMB)
		}
	}
	if ts := s.TaskStates[s.TaskGroup]; ts != nil {
		r.ExitCode, r.ExitedAt = lastExit(ts)
	}
	return r
}

// lastExit is the exit status and the time of a task's newest Terminated
// event. A Terminated event without a readable exit code gives a nil code and
// still its time.
func lastExit(ts *api.TaskState) (*int, *time.Time) {
	for i := len(ts.Events) - 1; i >= 0; i-- { // Nomad appends events, newest last
		e := ts.Events[i]
		if e == nil || e.Type != api.TaskTerminated {
			continue
		}
		var at *time.Time
		if e.Time > 0 {
			t := time.Unix(0, e.Time).UTC() // Nomad's event times are Unix nanoseconds
			at = &t
		}
		return exitCodeOf(&api.TaskState{Events: []*api.TaskEvent{e}}), at
	}
	return nil, nil
}

// lifecycles returns the lifecycles of a job version's tasks, from the cache or
// from Nomad. Nomad's Info answers with the job's newest version, so for an
// older copy the version list is read for its own version; either way the
// answer is kept under the version it is for. A job Nomad no longer has gives
// no lifecycles.
func (d *Deployer) lifecycles(ctx context.Context, key jobVersion) (taskHooks, error) {
	c := &d.cache
	c.mu.Lock()
	hit, ok := c.tasks[key]
	c.mu.Unlock()
	if ok {
		return hit, nil
	}

	q := (&api.QueryOptions{}).WithContext(ctx)
	job, _, err := d.nomad.Jobs().Info(key.job, q)
	if err != nil && statusOf(err) != http.StatusNotFound {
		return nil, d.callError("read job "+key.job, err)
	}
	if err == nil && job.Version != nil && *job.Version != key.version {
		versions, _, _, verr := d.nomad.Jobs().Versions(key.job, false, q)
		switch {
		case verr != nil && statusOf(verr) != http.StatusNotFound:
			return nil, d.callError("read the versions of job "+key.job, verr)
		case verr == nil:
			for _, v := range versions {
				if v.Version != nil && *v.Version == key.version {
					job = v
					break
				}
			}
		}
	}

	hooks := taskHooks{}
	if err == nil {
		for _, g := range job.TaskGroups {
			if g == nil || g.Name == nil {
				continue
			}
			tasks := map[string]*api.TaskLifecycle{}
			for _, t := range g.Tasks {
				if t != nil && t.Lifecycle != nil && t.Lifecycle.Hook != "" {
					tasks[t.Name] = t.Lifecycle
				}
			}
			hooks[*g.Name] = tasks
		}
	}
	c.mu.Lock()
	if c.tasks == nil {
		c.tasks = map[jobVersion]taskHooks{}
	}
	c.tasks[key] = hooks
	c.mu.Unlock()
	return hooks, nil
}

// forgetLifecycles drops the cached versions that no live copy has, so the
// cache holds no more than the copies of the last call.
func (d *Deployer) forgetLifecycles(keep map[jobVersion]bool) {
	c := &d.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.tasks {
		if !keep[k] {
			delete(c.tasks, k)
		}
	}
}
