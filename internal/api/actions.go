package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// App actions (feature 020.5): run a command in a copy, stop, start and
// restart an app, and the record of them. Everything goes through the service.
//
// runCommand's answer holds the command's output, which is shown to the caller
// once. It is sent with Cache-Control: no-store, and nothing in this file logs,
// stores or repeats it; the request log has the method, path and status only.

// codes of the refusals only the actions make.
const (
	codeAppStopped     = "app_stopped"
	codeAppNotStopped  = "app_not_stopped"
	codeAppBusy        = "app_busy"
	codeCopyNotRunning = "copy_not_running"
)

// actionError maps the action errors to their codes; mapError calls it.
func actionError(err error) *Error {
	switch {
	case errors.Is(err, service.ErrAppStopped):
		return &Error{Status: http.StatusConflict, Code: codeAppStopped, Message: err.Error()}
	case errors.Is(err, service.ErrAppNotStopped):
		return &Error{Status: http.StatusConflict, Code: codeAppNotStopped, Message: err.Error()}
	case errors.Is(err, service.ErrAppBusy):
		return &Error{Status: http.StatusConflict, Code: codeAppBusy, Message: err.Error()}
	case errors.Is(err, service.ErrCopyNotRunning):
		return &Error{Status: http.StatusConflict, Code: codeCopyNotRunning, Message: err.Error()}
	}
	return nil
}

// appActionJSON is one action (schema AppAction). It never has command output
// or arguments: a run has its program (the first word) and its exit code.
type appActionJSON struct {
	ID         string     `json:"id"`
	Action     string     `json:"action"`
	Trigger    string     `json:"trigger"`
	Status     string     `json:"status"`
	CopyID     string     `json:"copyId,omitempty"`
	Service    string     `json:"service,omitempty"`
	Program    string     `json:"program,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func toAppActionJSON(a store.ActionRecord) appActionJSON {
	return appActionJSON{ID: a.ID, Action: a.Action, Trigger: a.Trigger, Status: a.Status, CopyID: a.CopyID,
		Service: a.Service, Program: a.Program, ExitCode: a.ExitCode, Error: a.Error,
		StartedAt: a.StartedAt, FinishedAt: a.FinishedAt}
}

// commandResultJSON is what runCommand answers (schema CommandResult).
type commandResultJSON struct {
	ExitCode        *int   `json:"exitCode,omitempty"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
	StderrTruncated bool   `json:"stderrTruncated"`
	TimedOut        bool   `json:"timedOut"`
	FailedToStart   bool   `json:"failedToStart"`
}

// runCommand answers POST /v1/apps/{id}/copies/{copyId}/run: one command, run
// in that copy without a shell, a terminal or input. The answer is 200 with the
// output, the exit code, or that it timed out or didn't start; it is the only
// place the output is ever shown, and it is never cached.
func (s *server) runCommand(w http.ResponseWriter, r *http.Request) error {
	// Before anything else, so no answer of this operation can be stored.
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Command []string `json:"command"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.svc.RunCommand(r.Context(), r.PathValue("id"), r.PathValue("copyId"), in.Command, app.TriggerManual)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, commandResultJSON{ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr,
		StdoutTruncated: res.StdoutTruncated, StderrTruncated: res.StderrTruncated,
		TimedOut: res.TimedOut, FailedToStart: res.FailedToStart})
	return nil
}

// stopApp answers POST /v1/apps/{id}/stop with the app and its status.
func (s *server) stopApp(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, err := s.svc.StopApp(r.Context(), id, app.TriggerManual); err != nil {
		return err
	}
	return s.writeAppSummary(w, r, id)
}

// startApp answers POST /v1/apps/{id}/start with the app and its status.
func (s *server) startApp(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, err := s.svc.StartApp(r.Context(), id, app.TriggerManual); err != nil {
		return err
	}
	return s.writeAppSummary(w, r, id)
}

func (s *server) writeAppSummary(w http.ResponseWriter, r *http.Request, id string) error {
	sum, err := s.svc.AppSummary(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAppSummaryJSON(sum))
	return nil
}

// restartApp answers POST /v1/apps/{id}/restart: 202 with the action, running.
// The copies restart one at a time in the background; list the actions to
// follow it.
func (s *server) restartApp(w http.ResponseWriter, r *http.Request) error {
	act, err := s.svc.RestartApp(r.Context(), r.PathValue("id"), app.TriggerManual)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toAppActionJSON(act))
	return nil
}

// actionsListed is how many actions listAppActions answers.
const actionsListed = 20

// listAppActions answers GET /v1/apps/{id}/actions: the newest 20, newest first.
func (s *server) listAppActions(w http.ResponseWriter, r *http.Request) error {
	acts, err := s.svc.ListActions(r.Context(), r.PathValue("id"), actionsListed)
	if err != nil {
		return err
	}
	out := make([]appActionJSON, 0, len(acts))
	for _, a := range acts {
		out = append(out, toAppActionJSON(a))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
