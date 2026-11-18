package deploy

import (
	"context"
	"net/http"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Autoscaling (feature 043): reading a job's counts, changing one group's
// count with Nomad's scale API, and keeping a deploy at the count a scaled
// service has now.

// metaScale marks a job version made by a scale step; Caravana's own record
// (an app action) is what maps it to a deployment, this is for people reading
// Nomad.
const metaScale = "caravana.scale"

// ScaleState is what the autoscaler needs to know about a job.
type ScaleState struct {
	// Exists is false when Nomad has no such job; Stopped says its newest
	// version is stopped.
	Exists, Stopped bool
	// Version is the job's newest version.
	Version uint64
	// Counts is each group's count, by group (service) name.
	Counts map[string]int
	// Rolling says the job's latest Nomad deployment is still running: a
	// deploy, or the copies of the last scale still being placed.
	Rolling bool
}

// ScaleState reads the job's counts and whether its latest Nomad deployment
// still goes.
func (d *Deployer) ScaleState(ctx context.Context, jobID string) (ScaleState, error) {
	job, _, err := d.nomad.Jobs().Info(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if statusOf(err) == http.StatusNotFound {
		return ScaleState{}, nil
	}
	if err != nil {
		return ScaleState{}, d.callError("read job "+jobID, err)
	}
	st := ScaleState{Exists: true, Counts: map[string]int{}}
	st.Stopped = job.Stop != nil && *job.Stop
	if job.Version != nil {
		st.Version = *job.Version
	}
	for _, g := range job.TaskGroups {
		if g.Name != nil && g.Count != nil {
			st.Counts[*g.Name] = *g.Count
		}
	}
	dep, _, err := d.nomad.Jobs().LatestDeployment(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return ScaleState{}, d.callError("read the latest deployment of "+jobID, err)
	}
	active := rollouts{}
	active.add(dep)
	st.Rolling = len(active) > 0
	return st, nil
}

// Scale sets one group's count with Nomad's scale API, with message as the
// reason Nomad keeps in its scale events, and returns the job version Nomad
// made. found is false when the version can't be told: the job changed again
// between the scale and the read (the version is then not the scale's).
func (d *Deployer) Scale(ctx context.Context, jobID, group string, count int, message string) (version uint64, found bool, err error) {
	resp, _, err := d.nomad.Jobs().Scale(jobID, group, &count, message, false,
		map[string]any{metaScale: "1"}, (&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		return 0, false, d.registerError(jobID, err)
	}
	job, _, err := d.nomad.Jobs().Info(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		// The scale is done; only its version is unknown.
		return 0, false, nil
	}
	if job.JobModifyIndex == nil || job.Version == nil || *job.JobModifyIndex != resp.JobModifyIndex {
		return 0, false, nil
	}
	return *job.Version, true, nil
}

// withCurrentCounts returns the app to translate: a copy in which each scaled
// service has Replicas set to the count its group runs now, kept within the
// scale's Min and Max (Service.CountFor), and the counts it chose. current is
// the job as Nomad has it, nil when there is none; a stopped job has no count
// to keep (the deploy starts it again at the starting count). Unscaled
// services are left as they are, and the stored app is never changed: its
// Replicas stays the starting count.
func withCurrentCounts(a app.App, current *api.Job) (app.App, map[string]int) {
	var counts map[string]int
	for i, svc := range a.Services {
		if svc.Scale == nil {
			continue
		}
		var now *int
		if current != nil && (current.Stop == nil || !*current.Stop) {
			for _, g := range current.TaskGroups {
				if g.Name != nil && *g.Name == svc.Name && g.Count != nil {
					now = new(*g.Count)
				}
			}
		}
		if counts == nil {
			counts = map[string]int{}
			a.Services = append([]app.Service(nil), a.Services...) // the caller's slice stays as it is
		}
		n := svc.CountFor(now)
		a.Services[i].Replicas = new(n)
		counts[svc.Name] = n
	}
	return a, counts
}

// versionAliases reads the job versions that scale steps made, with the
// deployment version each one scaled (Deployer.VersionAliases), or nil when
// there is no hook or it fails: the versions are then shown as Nomad has them.
func (d *Deployer) versionAliases(ctx context.Context) map[string]map[uint64]uint64 {
	if d.VersionAliases == nil {
		return nil
	}
	aliases, err := d.VersionAliases(ctx)
	if err != nil {
		return nil
	}
	return aliases
}

// aliasVersion is the version a job's copy is shown with: the deployment's,
// when its job version was made by a scale.
func aliasVersion(aliases map[string]map[uint64]uint64, jobID string, version uint64) uint64 {
	if base, ok := aliases[jobID][version]; ok {
		return base
	}
	return version
}

// aliased shows each copy of jobID with the deployment's version where its job
// version was made by a scale. The slice is changed in place.
func aliased(copies []Copy, jobID string, aliases map[string]map[uint64]uint64) []Copy {
	for i := range copies {
		copies[i].Version = aliasVersion(aliases, jobID, copies[i].Version)
	}
	sortCopies(copies) // equal versions may now sit beside each other
	return copies
}
