package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
)

// Schedule runs (feature 022). Each run of a schedule is a Nomad batch job of
// its own, built by nomadjob.ScheduleRunJob. This file is the Nomad side: it
// starts a run, reads how far it got, reads its output and stops or purges it.
// Package service decides when, writes the run's row before it registers
// anything and the intent before it stops one, and settles the row.
//
// Every call takes the run's job ID as a plain string: this package doesn't
// know how the ID is made.

// ErrRunGone: Nomad has no job for the run (it was purged, or never
// registered), so it can't be waited for.
var ErrRunGone = errors.New("nomad has no job for the run")

// RunStartError is a run Nomad refused to start (it answered, and said no), as
// opposed to a Nomad that couldn't be asked, which is ErrNomadUnavailable.
// Reason is in Caravana's words and is what the run's record keeps; Err is
// what Nomad said, for logs, and may quote the job but never a secret value.
type RunStartError struct {
	Reason string
	Err    error
}

func (e *RunStartError) Error() string { return e.Reason + ": " + e.Err.Error() }
func (e *RunStartError) Unwrap() error { return e.Err }

// ScheduleRunJobID is the Nomad job of run runID of an app, which the service
// needs to name a run to StopApp, StopScheduleRun and the others without
// knowing how jobs are named.
func ScheduleRunJobID(appID, runID string) string { return nomadjob.ScheduleRunJobID(appID, runID) }

// ScheduleRunJob is the job of run runID of schedule scheduleID, built by
// nomadjob.ScheduleRunJob with this deployer's settings (the defaults the
// service doesn't hold), with the schedule's ID in its meta. a is the app as
// its live version was deployed, svc the schedule's service in it, image that
// version's pinned digest of the service, private the registry hosts with
// credentials. It is pure.
func (d *Deployer) ScheduleRunJob(a app.App, scheduleID, runID string, svc app.Service, image string, command []string, memoryMB int, private map[string]bool) *api.Job {
	job := nomadjob.ScheduleRunJob(a, runID, svc, image, command, memoryMB, d.settings, private)
	job.Meta["caravana.schedule"] = scheduleID
	return job
}

// runMarkerItem is the one item a run's variable gets when it would otherwise
// have none: Nomad refuses a variable with every item empty, and a template
// reading a variable that isn't there blocks the copy.
const runMarkerItem = "CARAVANA_RUN"

// StartScheduleRun writes the run's variable and registers its job.
//
// The variable is at the job's own path (nomad/jobs/<job ID>), where only that
// job's task can read it. It holds secrets, the service's secret values by
// name, plus the registry credentials when a task of the job pulls from a
// private registry (a task whose docker config has an auth block: the job
// holds placeholders and these are the values, as for backups). With nothing
// to hold it gets a marker item. A variable left behind by a failed start is
// deleted again.
//
// Nomad saying no is a *RunStartError; a Nomad that couldn't be reached is
// ErrNomadUnavailable, unwrapped.
func (d *Deployer) StartScheduleRun(ctx context.Context, job *api.Job, secrets map[string]string) error {
	if job == nil || job.ID == nil {
		return errors.New("start schedule run: the job has no ID")
	}
	id := *job.ID
	path := nomadjob.ScheduleRunVariablePath(id)

	items := maps.Clone(secrets)
	if items == nil {
		items = map[string]string{}
	}
	for _, t := range job.TaskGroups {
		for _, task := range t.Tasks {
			image, _ := task.Config["image"].(string)
			if _, private := task.Config["auth"]; !private || image == "" {
				continue
			}
			ref, err := registry.Parse(image)
			if err != nil {
				return fmt.Errorf("start schedule run %s: %w", id, err)
			}
			if items, err = d.withPullCredentials(ctx, items, image, map[string]bool{ref.Registry: true}); err != nil {
				return err
			}
		}
	}
	if !anyValue(items) {
		items[runMarkerItem] = id
	}

	if err := d.writeVariable(ctx, path, items); err != nil {
		if errors.Is(err, ErrNomadRejected) {
			return &RunStartError{Reason: "Nomad refused the run's variables", Err: err}
		}
		return err
	}
	if _, _, err := d.nomad.Jobs().Register(job, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		// The job isn't there, so nothing will settle the run and purge it:
		// don't leave the secrets in Nomad.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = d.deleteVariable(cleanup, path)
		err = d.registerError(id, err)
		if errors.Is(err, ErrNomadRejected) || errors.Is(err, ErrJobConflict) {
			return &RunStartError{Reason: "Nomad refused the job", Err: err}
		}
		return err
	}
	return nil
}

func anyValue(items map[string]string) bool {
	for _, v := range items {
		if v != "" {
			return true
		}
	}
	return false
}

// RunState is what Nomad knows of a run, read from its newest allocation.
type RunState struct {
	// Found says the run's job exists.
	Found bool
	// AllocID is the newest allocation's ID; empty until Nomad has placed one.
	AllocID string
	// ClientStatus is pending, running, complete, failed or lost, as the
	// allocation says; empty without an allocation.
	ClientStatus string
	// DesiredStatus is run or stop; stop once the job was deregistered.
	DesiredStatus string
	// TaskStartedAt is when the service's task started; nil if it never did.
	TaskStartedAt *time.Time
	// ExitCode is the exit status in the task's newest Terminated event; nil
	// while it hasn't ended, or when it ended without one.
	ExitCode *int
	// OOMKilled says the kernel killed the task for passing its memory ceiling.
	OOMKilled bool
	// StartFailed says a task of the run couldn't start: a Driver Failure
	// (the image couldn't be pulled, the program isn't there) or a Setup
	// Failure (a template, an artifact).
	StartFailed bool
	// StartFailure classifies the newest start failure event, the service
	// task's first and any task's otherwise: StartProgramNotFound,
	// StartImagePull or StartOther. Empty when there is no start failure.
	// It is a class, never Nomad's text, which may quote the image.
	StartFailure string
	// Terminal says the allocation ended: complete, failed or lost.
	Terminal bool
}

// The classes of RunState.StartFailure.
const (
	// StartProgramNotFound: the container's program isn't in the image.
	StartProgramNotFound = "program_not_found"
	// StartImagePull: the image couldn't be pulled or found.
	StartImagePull = "image_pull"
	// StartOther: Nomad couldn't start the copy, for any other reason.
	StartOther = "other"
)

// ScheduleRunState reads the state of the run jobID, whose service task is
// task. A job Nomad doesn't have is ErrRunGone. A job with no allocation yet
// is Found and not Terminal: Nomad hasn't placed it.
func (d *Deployer) ScheduleRunState(ctx context.Context, jobID, task string) (RunState, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	if _, _, err := d.nomad.Jobs().Info(jobID, q); err != nil {
		if statusOf(err) == http.StatusNotFound {
			return RunState{}, fmt.Errorf("%w: %s", ErrRunGone, jobID)
		}
		return RunState{}, d.callError("read job "+jobID, err)
	}
	allocs, _, err := d.nomad.Jobs().Allocations(jobID, true, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return RunState{}, fmt.Errorf("%w: %s", ErrRunGone, jobID)
		}
		return RunState{}, d.callError("read the copies of "+jobID, err)
	}

	st := RunState{Found: true}
	var a *api.AllocationListStub
	for _, c := range allocs {
		if c != nil && (a == nil || c.CreateIndex > a.CreateIndex ||
			(c.CreateIndex == a.CreateIndex && c.CreateTime > a.CreateTime)) {
			a = c
		}
	}
	if a == nil {
		return st, nil
	}
	st.AllocID, st.ClientStatus, st.DesiredStatus = a.ID, a.ClientStatus, a.DesiredStatus
	switch a.ClientStatus {
	case api.AllocClientStatusComplete, api.AllocClientStatusFailed, api.AllocClientStatusLost:
		st.Terminal = true
	}

	for _, ts := range a.TaskStates {
		if ts == nil {
			continue
		}
		for _, e := range ts.Events {
			if isStartFailure(e) {
				st.StartFailed = true
			}
		}
	}
	if st.StartFailed {
		st.StartFailure = startFailureOf(a.TaskStates, task)
	}
	if ts := a.TaskStates[task]; ts != nil {
		if !ts.StartedAt.IsZero() {
			st.TaskStartedAt = new(ts.StartedAt)
		}
		st.OOMKilled = killedForMemory(ts)
		st.ExitCode = exitCodeOf(ts)
	}
	return st, nil
}

func isStartFailure(e *api.TaskEvent) bool {
	return e != nil && (e.Type == api.TaskDriverFailure || e.Type == api.TaskSetupFailure)
}

// startFailureOf classifies the newest Driver Failure or Setup Failure event
// of task, or of the other tasks (by name) when task has none.
func startFailureOf(states map[string]*api.TaskState, task string) string {
	names := []string{task}
	for _, n := range slices.Sorted(maps.Keys(states)) {
		if n != task {
			names = append(names, n)
		}
	}
	for _, n := range names {
		ts := states[n]
		if ts == nil {
			continue
		}
		for i := len(ts.Events) - 1; i >= 0; i-- { // Nomad appends events, newest last
			if e := ts.Events[i]; isStartFailure(e) {
				return classifyStartFailure(e)
			}
		}
	}
	return StartOther
}

// classifyStartFailure reads what the docker driver says when a container
// can't start. A missing program comes from the container runtime
// ("... exec: \"nosuch\": executable file not found in $PATH: unknown", or
// "no such file or directory"); a missing image from the pull ("Failed to
// pull `x`: ...") or the lookup ("Failed to find docker image ..."). The
// program is checked first: "executable file not found" also says "not found".
func classifyStartFailure(e *api.TaskEvent) string {
	msg := strings.ToLower(strings.Join([]string{e.DisplayMessage, e.Message,
		e.Details["driver_error"], e.Details["error"]}, " "))
	switch {
	case strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file or directory"):
		return StartProgramNotFound
	case strings.Contains(msg, "pull") || strings.Contains(msg, "failed to find") ||
		strings.Contains(msg, "manifest unknown") ||
		(e.Type == api.TaskDriverFailure && strings.Contains(msg, "image")):
		return StartImagePull
	}
	return StartOther
}

// exitCodeOf is the exit status in a task's newest Terminated event, or nil.
// The documented key is Details["exit_code"]; the event's own ExitCode field
// is deprecated.
func exitCodeOf(ts *api.TaskState) *int {
	for i := len(ts.Events) - 1; i >= 0; i-- { // Nomad appends events, newest last
		e := ts.Events[i]
		if e == nil || e.Type != api.TaskTerminated {
			continue
		}
		code, err := strconv.Atoi(e.Details["exit_code"])
		if err != nil {
			return nil
		}
		return &code
	}
	return nil
}

// runOutputBytes is how much of each stream of a run's output is kept.
const runOutputBytes = 64 << 10

// ScheduleRunOutput reads the last 64 KiB of the stdout and of the stderr of
// task in the run's allocation allocID. stdoutCut and stderrCut say the stream
// had more than that, so its first lines are missing. Lines come back joined
// with newlines, invalid UTF-8 replaced, as Logs sends them. An allocation or
// file Nomad no longer has is no output, not an error.
//
// The output is returned and not logged: it can hold anything the command
// printed, secrets included. The caller masks it before keeping it.
func (d *Deployer) ScheduleRunOutput(ctx context.Context, allocID, task string) (stdout, stderr []byte, stdoutCut, stderrCut bool, err error) {
	if stdout, stdoutCut, err = d.tail(ctx, allocID, task, LogStdout); err != nil {
		return nil, nil, false, false, err
	}
	if stderr, stderrCut, err = d.tail(ctx, allocID, task, LogStderr); err != nil {
		return nil, nil, false, false, err
	}
	return stdout, stderr, stdoutCut, stderrCut, nil
}

// tail is the last runOutputBytes of one stream, and whether it was cut.
func (d *Deployer) tail(ctx context.Context, allocID, task string, stream LogStream) ([]byte, bool, error) {
	lines := make(chan LogLine, 64)
	errCh := make(chan error, 1)
	var cut bool
	go func() {
		errCh <- d.readLogs(ctx, allocID, task, stream, runOutputBytes, false, lines, &cut)
		close(lines)
	}()
	var out bytes.Buffer
	for l := range lines {
		out.WriteString(l.Text)
		out.WriteByte('\n')
	}
	// cut is written by the goroutine before it sends the first line and read
	// here after the channel closed, which orders the two.
	if err := <-errCh; err != nil {
		return nil, false, err
	}
	return out.Bytes(), cut, nil
}

// StopScheduleRun stops the run jobID: a deregister without purge, so Nomad
// kills the task (after its kill timeout) and keeps the allocation, whose
// events and output the service still reads. It is never Allocations().Stop,
// which for a batch job would place a replacement. A run Nomad doesn't have is
// ErrRunGone. The service writes why the run is stopped before it calls this.
func (d *Deployer) StopScheduleRun(ctx context.Context, jobID string) error {
	if _, _, err := d.nomad.Jobs().Deregister(jobID, false, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		if statusOf(err) == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrRunGone, jobID)
		}
		return d.callError("stop job "+jobID, err)
	}
	return nil
}

// PurgeScheduleRun erases the run jobID with its allocations and deletes its
// variable, which holds the service's secrets. A job or variable Nomad doesn't
// have is done. It is called after the run's outcome and output are recorded:
// the purge takes the allocation's output with it.
func (d *Deployer) PurgeScheduleRun(ctx context.Context, jobID string) error {
	if err := d.purgeJob(ctx, jobID); err != nil {
		return err
	}
	return d.deleteVariable(ctx, nomadjob.ScheduleRunVariablePath(jobID))
}
