package nomadjob

import (
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Schedules (feature 022, research R1). Caravana keeps the clock; each run of a
// schedule is one Nomad batch job of its own, made from the service's live
// version: the same pinned image and variables, a command in place of the
// service's own, and no traffic. Like Translate, this is pure.

// ScheduleRunJobID is the job of one run. App IDs never contain "_", so it
// can't collide with an app's own job, and it starts with AppJobPrefix like
// every other job of the app.
func ScheduleRunJobID(appID, runID string) string { return appID + "_run-" + runID }

// ScheduleRunVariablePath is where a run's job reads its secrets and registry
// credentials: its own variable, the only one a batch job with this ID can
// read (see BackupVariablePath).
func ScheduleRunVariablePath(jobID string) string { return BackupVariablePath(jobID) }

// Resources of a run's job that don't depend on the service.
const (
	scheduleKillTimeout = 10 * time.Second
	scheduleLogFiles    = 2
	scheduleLogFileMB   = 1
)

// ScheduleRunJob is the job of run runID: a command in service svc of app a,
// run with image (the service's pinned image from its newest healthy
// deployment) and command (the program, then its arguments; Caravana has
// checked it isn't empty).
//
//   - The task is named after the service and runs image with the command as
//     its entrypoint and arguments, so the service's own command and
//     entrypoint don't apply. It has the service's plain env; the secrets (and
//     the registry credentials, for a private image) come through the job's own
//     variable, ScheduleRunVariablePath, which Caravana writes before it
//     registers the job.
//   - memoryMB is the reservation of this run. The ceiling is the service's own
//     (as Translate computes it), but never below the reservation.
//   - There are no services and no ports: nothing routes to a run and nothing
//     checks it.
//   - In an app whose services reach each other by name, the run gets the same
//     names sidecar as the app's own copies.
//   - The job keeps the run's output small (two 1 MB files) and gives the
//     task 10 s to stop.
//
// The job must be purged after its run has ended: it is a batch job that
// nothing else removes, and its variable holds the secrets. Meta carries
// caravana.app, caravana.service and caravana.run; the caller adds the
// schedule's ID if it wants it there.
func ScheduleRunJob(a app.App, runID string, svc app.Service, image string, command []string, memoryMB int, s Settings, private map[string]bool) *api.Job {
	id := ScheduleRunJobID(a.ID, runID)

	j := batchJob(id, a.ID, svc.Name)
	j.Meta["caravana.run"] = runID

	config := map[string]any{"image": image}
	if len(command) > 0 {
		config["entrypoint"] = []string{command[0]}
		if len(command) > 1 {
			config["args"] = append([]string(nil), command[1:]...)
		}
	}
	config = withPullAuth(config, image, private)

	var env map[string]string // nil when there are none, so the job has no Env
	if len(svc.Env) > 0 {
		env = make(map[string]string, len(svc.Env))
		for _, e := range svc.Env {
			env[e.Name] = e.Value
		}
	}

	cpu, _, ceiling := Reserved(svc.Resources, s)
	t := &api.Task{
		Name:   svc.Name,
		Driver: "docker",
		Config: config,
		Env:    env,
		Resources: &api.Resources{
			CPU:         new(cpu),
			MemoryMB:    new(memoryMB),
			MemoryMaxMB: new(max(memoryMB, ceiling)),
		},
		Templates:   []*api.Template{variableTemplate(id)},
		KillTimeout: new(scheduleKillTimeout),
		LogConfig:   &api.LogConfig{MaxFiles: new(scheduleLogFiles), MaxFileSizeMB: new(scheduleLogFileMB)},
	}

	tasks := []*api.Task{t}
	if sharesNames(a) {
		tasks = append(tasks, namesTask(a, s))
	}
	j.TaskGroups = []*api.TaskGroup{batchGroup(svc.Name, tasks...)}
	return j
}
