package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// App actions (feature 020.5): stop and start an app, restart its copies and
// run one command in a copy. This file is the Nomad side of that; package
// service decides when, holds the app's lock and records what happened.
//
// A command's output passes through this package and nowhere else: it is
// returned to the caller in ExecResult and never logged, put in an error, or
// kept (research R2). Nomad's own error text is not the command's output and
// may be returned, shortened.

// Errors callers can check with errors.Is.
var (
	// ErrCopyNotRunning: the copy exists but isn't running, so there is nothing
	// to run a command in.
	ErrCopyNotRunning = errors.New("the copy is not running")
	// ErrJobNotFound: Nomad has no job for the app.
	ErrJobNotFound = errors.New("nomad has no job for the app")
	// ErrExecFailed: Nomad couldn't run the command (the program isn't in the
	// image, say). The wrapped text is Nomad's.
	ErrExecFailed = errors.New("the command could not be run")
)

// ExecResult is what one command did. Output is bytes as the program wrote
// them, each stream cut at the caller's limit.
type ExecResult struct {
	// ExitCode is the program's exit status; nil when it timed out.
	ExitCode *int
	Stdout   []byte
	Stderr   []byte
	// StdoutTruncated and StderrTruncated say the stream had more than the
	// limit.
	StdoutTruncated, StderrTruncated bool
	// TimedOut says the caller's context ran out before the program ended.
	TimedOut bool
}

// cappedWriter keeps the first limit bytes written to it and counts that
// there was more. It is safe for concurrent use: after a timeout the Nomad
// client's receiving goroutine may still be writing while the result is read.
type cappedWriter struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func newCappedWriter(limit int) *cappedWriter { return &cappedWriter{limit: limit} }

// Write keeps what fits and reports all of p as written, so the sender goes on
// (a short write would be an error to it).
func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	room := max(w.limit-len(w.buf), 0)
	w.buf = append(w.buf, p[:min(room, len(p))]...)
	if len(p) > room {
		w.truncated = true
	}
	return len(p), nil
}

// result returns a copy of what was kept, and whether anything was cut.
func (w *cappedWriter) result() (data []byte, truncated bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf...), w.truncated
}

// Exec runs command in the service's task of the copy, without a terminal and
// with an empty stdin, and returns what it wrote (each stream cut at limit
// bytes) and its exit code. The caller's context carries the time limit: when
// it ends because its deadline passed, the result says TimedOut and has no
// exit code, and the error is nil. Any other end of the context is its error.
//
// A copy Nomad doesn't have is ErrCopyNotFound and one that isn't running is
// ErrCopyNotRunning. When Nomad can't start the command the error wraps
// ErrExecFailed.
func (d *Deployer) Exec(ctx context.Context, copyID, task string, command []string, limit int) (ExecResult, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	alloc, _, err := d.nomad.Allocations().Info(copyID, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return ExecResult{}, fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return ExecResult{}, d.callError("read copy "+copyID, err)
	}
	if alloc.ClientStatus != api.AllocClientStatusRunning {
		return ExecResult{}, fmt.Errorf("%w: %s is %s", ErrCopyNotRunning, copyID, alloc.ClientStatus)
	}

	stdout, stderr := newCappedWriter(limit), newCappedWriter(limit)
	// A nil terminal-size channel is never ready: no terminal, nothing to resize.
	var noSizes <-chan api.TerminalSize
	code, err := d.nomad.Allocations().Exec(ctx, alloc, task, false, command,
		strings.NewReader(""), stdout, stderr, noSizes, (&api.QueryOptions{}).WithContext(ctx))

	res := ExecResult{}
	res.Stdout, res.StdoutTruncated = stdout.result()
	res.Stderr, res.StderrTruncated = stderr.result()
	switch {
	case err == nil:
		res.ExitCode = &code
		return res, nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		return res, nil
	case ctx.Err() != nil:
		return ExecResult{}, ctx.Err()
	}
	// The output of a command that didn't start is empty; drop it so no path
	// returns text next to an error.
	return ExecResult{}, d.execError(copyID, err)
}

// execError turns a failed exec into one of ours. Nomad's text is short and
// is its own words, never the command's output.
func (d *Deployer) execError(copyID string, err error) error {
	switch statusOf(err) {
	case http.StatusForbidden:
		return d.capabilityError("run a command in copy "+copyID, "alloc-exec", err)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
	case 0:
	default:
		return d.callError("run a command in copy "+copyID, err)
	}
	msg, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return fmt.Errorf("%w: %s", ErrExecFailed, msg)
}

// StopApp stops the app's job and each of backupJobIDs and runJobIDs that
// Nomad has: a deregister without purge, so Nomad keeps the job and its
// versions for StartApp. runJobIDs are the jobs of schedule runs still going
// (feature 022): they are stopped, not purged, so the service can still read
// how they ended. The app's job being gone is ErrJobNotFound; a backup or run
// job that isn't there is skipped (the service stores no record of which
// backup jobs exist, and a run may have just ended).
func (d *Deployer) StopApp(ctx context.Context, appID string, backupJobIDs, runJobIDs []string) error {
	w := (&api.WriteOptions{}).WithContext(ctx)
	if _, _, err := d.nomad.Jobs().Deregister(appID, false, w); err != nil {
		if statusOf(err) == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrJobNotFound, appID)
		}
		return d.callError("stop job "+appID, err)
	}
	for _, id := range slices.Concat(backupJobIDs, runJobIDs) {
		if _, _, err := d.nomad.Jobs().Deregister(id, false, w); err != nil && statusOf(err) != http.StatusNotFound {
			return d.callError("stop job "+id, err)
		}
	}
	return nil
}

// StartApp starts the stopped job of the app and each of backupJobIDs that
// Nomad has, as "nomad job start" does: it reads the job as Nomad kept it,
// clears Stop and registers it again. That is the same definition and the same
// pinned images (no tag is looked up), as a new job version. A job that isn't
// stopped is left alone. The app's job being gone is ErrJobNotFound; a backup
// job that isn't there is skipped.
func (d *Deployer) StartApp(ctx context.Context, appID string, backupJobIDs []string) error {
	if err := d.startJob(ctx, appID); err != nil {
		return err
	}
	for _, id := range backupJobIDs {
		if err := d.startJob(ctx, id); err != nil && !errors.Is(err, ErrJobNotFound) {
			return err
		}
	}
	return nil
}

func (d *Deployer) startJob(ctx context.Context, jobID string) error {
	job, _, err := d.nomad.Jobs().Info(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrJobNotFound, jobID)
		}
		return d.callError("read job "+jobID, err)
	}
	if job.Stop == nil || !*job.Stop {
		return nil
	}
	job.Stop = new(false)
	opts := &api.RegisterOptions{EnforceIndex: true}
	if job.JobModifyIndex != nil {
		opts.ModifyIndex = *job.JobModifyIndex
	}
	if _, _, err := d.nomad.Jobs().RegisterOpts(job, opts, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		return d.registerError(jobID, err)
	}
	return nil
}

// WaitCopyRestarted waits until the copy's task, restarted at or after since,
// is running again: its newest "Started" event is not before since. When
// checked is true it also waits until every service check of the copy passes.
// It polls every Timing.PollGap until the context ends, and returns the
// context's error then, so the caller's deadline is the time limit.
//
// A restart doesn't make Nomad judge the copy's health again (that happens
// once per deployment), so the task's events and the checks are what say it is
// back. A copy that has ended is ErrCopyNotRunning; one Nomad lost,
// ErrCopyNotFound.
func (d *Deployer) WaitCopyRestarted(ctx context.Context, copyID, task string, since time.Time, checked bool) error {
	gap := d.Timing.withDefaults().PollGap
	for {
		ready, err := d.copyRestarted(ctx, copyID, task, since, checked)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Nomad not answering for a moment is not the copy failing: keep
			// waiting, the context bounds it.
			if !errors.Is(err, ErrNomadUnavailable) {
				return err
			}
		} else if ready {
			return nil
		}
		if !sleep(ctx, gap) {
			return ctx.Err()
		}
	}
}

func (d *Deployer) copyRestarted(ctx context.Context, copyID, task string, since time.Time, checked bool) (bool, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	alloc, _, err := d.nomad.Allocations().Info(copyID, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return false, fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return false, d.callError("read copy "+copyID, err)
	}
	switch alloc.ClientStatus {
	case api.AllocClientStatusRunning, api.AllocClientStatusPending:
	default:
		return false, fmt.Errorf("%w: %s is %s", ErrCopyNotRunning, copyID, alloc.ClientStatus)
	}
	ts := alloc.TaskStates[task]
	if ts == nil || ts.State != taskRunning || !startedSince(ts.Events, since) {
		return false, nil
	}
	if !checked {
		return true, nil
	}
	results, err := d.nomad.Allocations().Checks(copyID, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return false, fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return false, d.capabilityError("read the checks of copy "+copyID, "read-job", err)
	}
	for _, c := range results {
		if c.Status != "success" {
			return false, nil
		}
	}
	return true, nil
}

// BackupJobIDs are the Nomad jobs StopApp and StartApp take along with the app:
// the periodic backup job of each service with a Postgres backup. (A volume
// backup is no job, and restore jobs aren't periodic.)
func BackupJobIDs(a app.App) []string {
	var ids []string
	for _, svc := range a.Services {
		if svc.Backup != nil && svc.Backup.Engine == app.BackupPostgres {
			ids = append(ids, nomadjob.BackupJobID(a.ID, svc.Name))
		}
	}
	return ids
}

// taskRunning is a task's state while it runs (Nomad's TaskState.State).
const taskRunning = "running"

// startedSince reports whether the events hold a "Started" at or after since.
func startedSince(events []*api.TaskEvent, since time.Time) bool {
	for _, e := range events {
		if e != nil && e.Type == api.TaskStarted && !time.Unix(0, e.Time).Before(since) {
			return true
		}
	}
	return false
}
