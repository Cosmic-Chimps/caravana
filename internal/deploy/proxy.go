package deploy

import (
	"context"
	"errors"
	"slices"

	"github.com/hashicorp/nomad/api"
)

// Traefik's job and allocation, read for the Requests page (feature 047). The
// access log is Traefik's stdout, which Nomad keeps like any task's output, so
// the service reads it with LogFiles and ReadLogAt of the allocations ProxyCopies
// finds. Nothing here looks at a line.

// ProxyJob is the ID of Traefik's job, and ProxyTask the name of its task, as
// the installer and the lab register them.
const (
	ProxyJob  = "traefik"
	ProxyTask = "traefik"
)

// ErrNoProxy: no Traefik is running: there is no job named traefik (Caravana
// isn't on a host the installer made), or it has no running allocation.
var ErrNoProxy = errors.New("no Traefik is running")

// ProxyCopy is Traefik's running allocation, the newest when there are several
// (a replacement starts before the old one is gone). Its Service is the task
// group, which is also the task whose output files LogFiles lists (use
// ProxyTask). With no such allocation it is ErrNoProxy.
func (d *Deployer) ProxyCopy(ctx context.Context) (Copy, error) {
	copies, err := d.ProxyCopies(ctx)
	if err != nil {
		return Copy{}, err
	}
	return copies[0], nil
}

// ProxyCopies is the allocations whose Traefik output Nomad may still keep.
// The first is the one ProxyCopy picks: the running one, the newest when there
// are several. After it come the others of the job, newest first: one being
// replaced or stopped that still runs, and the finished ones (complete, failed
// or lost, with Gone set) until Nomad collects them. Pending and unknown ones
// have no output and are left out. The requests served before a Traefik
// restart are in the files of those older allocations. With no running
// allocation it is ErrNoProxy.
func (d *Deployer) ProxyCopies(ctx context.Context) ([]Copy, error) {
	stubs, _, err := d.nomad.Allocations().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list Traefik's allocations", err)
	}
	newer := func(a, b *api.AllocationListStub) bool {
		return a.CreateTime > b.CreateTime || (a.CreateTime == b.CreateTime && a.ID > b.ID)
	}
	var best *api.AllocationListStub
	for _, s := range stubs {
		if s.JobID != ProxyJob || s.ClientStatus != api.AllocClientStatusRunning ||
			(s.DesiredStatus != "" && s.DesiredStatus != api.AllocDesiredStatusRun) {
			continue
		}
		if best == nil || newer(s, best) {
			best = s
		}
	}
	if best == nil {
		return nil, ErrNoProxy
	}
	var older []*api.AllocationListStub
	for _, s := range stubs {
		if s == best || s.JobID != ProxyJob {
			continue
		}
		switch s.ClientStatus {
		case api.AllocClientStatusRunning, api.AllocClientStatusComplete,
			api.AllocClientStatusFailed, api.AllocClientStatusLost:
			older = append(older, s)
		}
	}
	slices.SortFunc(older, func(a, b *api.AllocationListStub) int {
		switch {
		case newer(a, b):
			return -1
		case newer(b, a):
			return 1
		}
		return 0
	})
	copies := []Copy{copyOf(best, nil)}
	for _, s := range older {
		c := copyOf(s, nil)
		c.Gone = c.Terminal
		copies = append(copies, c)
	}
	return copies, nil
}

// ProxyAccessLog says whether Traefik's job writes its access log: one of its
// tasks' arguments is --accesslog=true, or the job renders its static
// configuration from a file (feature 034's meta caravana_certificates), which
// always has the access log. A host the installer made before feature 047 has
// the job without it, and the page says to run the installer again. A host with
// no job named traefik is ErrNoProxy.
func (d *Deployer) ProxyAccessLog(ctx context.Context) (bool, error) {
	job, err := d.proxyJob(ctx)
	if err != nil {
		return false, err
	}
	if job.Meta[certificatesMeta] == "1" {
		return true, nil
	}
	for _, g := range job.TaskGroups {
		if g == nil {
			continue
		}
		for _, t := range g.Tasks {
			if t == nil {
				continue
			}
			args, _ := t.Config["args"].([]any)
			if slices.ContainsFunc(args, func(a any) bool { s, _ := a.(string); return s == "--accesslog=true" }) {
				return true, nil
			}
		}
	}
	return false, nil
}
