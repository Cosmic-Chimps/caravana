package deploy

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/hashicorp/nomad/api"
)

// Traefik's job and allocation, read for the Requests page (feature 047). The
// access log is Traefik's stdout, which Nomad keeps like any task's output, so
// the service reads it with LogFiles and ReadLogAt of the allocation ProxyCopy
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
	stubs, _, err := d.nomad.Allocations().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return Copy{}, d.callError("list Traefik's allocations", err)
	}
	var best *api.AllocationListStub
	for _, s := range stubs {
		if s.JobID != ProxyJob || s.ClientStatus != api.AllocClientStatusRunning ||
			(s.DesiredStatus != "" && s.DesiredStatus != api.AllocDesiredStatusRun) {
			continue
		}
		if best == nil || s.CreateTime > best.CreateTime || (s.CreateTime == best.CreateTime && s.ID > best.ID) {
			best = s
		}
	}
	if best == nil {
		return Copy{}, ErrNoProxy
	}
	return copyOf(best, nil), nil
}

// ProxyAccessLog says whether Traefik's job writes its access log: one of its
// tasks' arguments is --accesslog=true. A host the installer made before
// feature 047 has the job without it, and the page says to run the installer
// again. A host with no job named traefik is ErrNoProxy.
func (d *Deployer) ProxyAccessLog(ctx context.Context) (bool, error) {
	job, _, err := d.nomad.Jobs().Info(ProxyJob, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if statusOf(err) == http.StatusNotFound {
			return false, ErrNoProxy
		}
		return false, d.callError("read the traefik job", err)
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
