package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Schedules (feature 022): a command run on a timer in a fresh copy of one
// service of an app. The answers are the service's own types (Schedule,
// ScheduleRun), whose JSON names are the contract's.
//
// A run's output is in one answer only, getScheduleRunOutput's, as plain text
// sent with Cache-Control: no-store. No other answer, error or log line of
// this file holds it: Schedule and ScheduleRun have no field for it.

// The codes only the schedules give.
const (
	codeAppNotFound      = "app_not_found"
	codeScheduleNotFound = "schedule_not_found"
	codeRunNotFound      = "run_not_found"
	codeOutputNotKept    = "output_not_kept"
	codeBuiltinSchedule  = "builtin_schedule"
	codeScheduleExists   = "schedule_exists"
	codeRunGoing         = "run_going"
	codeRunNotRunning    = "run_not_running"
)

// maxScheduleRunsLimit bounds listScheduleRuns' ?limit=.
const maxScheduleRunsLimit = 100

// scheduleError maps the schedule errors to their codes; mapError calls it.
func scheduleError(err error) *Error {
	switch {
	case errors.Is(err, service.ErrScheduleNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeScheduleNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrRunNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeRunNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrOutputNotKept):
		return &Error{Status: http.StatusNotFound, Code: codeOutputNotKept, Message: err.Error()}
	case errors.Is(err, service.ErrScheduleBuiltin):
		return &Error{Status: http.StatusConflict, Code: codeBuiltinSchedule, Message: err.Error()}
	case errors.Is(err, service.ErrScheduleExists):
		return &Error{Status: http.StatusConflict, Code: codeScheduleExists, Field: "name", Message: err.Error()}
	case errors.Is(err, service.ErrRunGoing):
		return &Error{Status: http.StatusConflict, Code: codeRunGoing, Message: err.Error()}
	case errors.Is(err, service.ErrRunNotRunning):
		return &Error{Status: http.StatusConflict, Code: codeRunNotRunning, Message: err.Error()}
	}
	return nil
}

// scheduleInvalid answers a field error of the schedule calls as 422, as the
// contract says, where the rest of the API answers 400. Other errors pass
// through for mapError.
func scheduleInvalid(err error) error {
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	}
	return err
}

// schedulePath is a schedule's URL.
func schedulePath(id string) string { return "/v1/schedules/" + url.PathEscape(id) }

// listSchedules answers GET /v1/schedules?app=&project=: every schedule, the
// built-in backup ones included, in the list's order.
func (s *server) listSchedules(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	list, err := s.svc.ListSchedules(r.Context(), service.ScheduleFilter{App: q.Get("app"), Project: q.Get("project")})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		Schedules []service.Schedule `json:"schedules"`
	}{Schedules: list})
	return nil
}

// createSchedule answers POST /v1/schedules: 201 with the schedule and its
// URL as Location. An unknown app is 404 app_not_found.
func (s *server) createSchedule(w http.ResponseWriter, r *http.Request) error {
	var in service.NewSchedule
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	sc, err := s.svc.CreateSchedule(r.Context(), in)
	if errors.Is(err, store.ErrNotFound) {
		return &Error{Status: http.StatusNotFound, Code: codeAppNotFound, Field: "app",
			Message: fmt.Sprintf("no app %q", in.App)}
	}
	if err != nil {
		return scheduleInvalid(err)
	}
	w.Header().Set("Location", schedulePath(sc.ID))
	writeJSON(w, http.StatusCreated, sc)
	return nil
}

// getSchedule answers GET /v1/schedules/{scheduleId}.
func (s *server) getSchedule(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.svc.GetSchedule(r.Context(), r.PathValue("scheduleId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sc)
	return nil
}

// updateSchedule answers PUT /v1/schedules/{scheduleId}: the body is the
// whole schedule, as for createSchedule.
func (s *server) updateSchedule(w http.ResponseWriter, r *http.Request) error {
	var in service.NewSchedule
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	sc, err := s.svc.UpdateSchedule(r.Context(), r.PathValue("scheduleId"), in)
	if err != nil {
		return scheduleInvalid(err)
	}
	writeJSON(w, http.StatusOK, sc)
	return nil
}

// deleteSchedule answers DELETE /v1/schedules/{scheduleId}: 204. A run that
// is going is stopped first.
func (s *server) deleteSchedule(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteSchedule(r.Context(), r.PathValue("scheduleId")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// runSchedule answers POST /v1/schedules/{scheduleId}/run: 202 with the run,
// which goes on in the background. A run Nomad refused is answered 202 too,
// already could_not_start.
func (s *server) runSchedule(w http.ResponseWriter, r *http.Request) error {
	run, err := s.svc.RunScheduleNow(r.Context(), r.PathValue("scheduleId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, run)
	return nil
}

// pauseSchedule answers POST /v1/schedules/{scheduleId}/pause.
func (s *server) pauseSchedule(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.svc.PauseSchedule(r.Context(), r.PathValue("scheduleId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sc)
	return nil
}

// resumeSchedule answers POST /v1/schedules/{scheduleId}/resume.
func (s *server) resumeSchedule(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.svc.ResumeSchedule(r.Context(), r.PathValue("scheduleId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sc)
	return nil
}

// listScheduleRuns answers GET /v1/schedules/{scheduleId}/runs?limit=: the
// newest runs first, 14 when limit is absent.
func (s *server) listScheduleRuns(w http.ResponseWriter, r *http.Request) error {
	limit := 0 // the service's default
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxScheduleRunsLimit {
			return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: "limit",
				Message: fmt.Sprintf("must be a whole number from 1 to %d, got %q", maxScheduleRunsLimit, v)}
		}
		limit = n
	}
	runs, err := s.svc.ListScheduleRuns(r.Context(), r.PathValue("scheduleId"), limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		Runs []service.ScheduleRun `json:"runs"`
	}{Runs: runs})
	return nil
}

// getScheduleRun answers GET /v1/schedules/{scheduleId}/runs/{runId}, without
// the run's output.
func (s *server) getScheduleRun(w http.ResponseWriter, r *http.Request) error {
	run, err := s.svc.GetScheduleRun(r.Context(), r.PathValue("scheduleId"), r.PathValue("runId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, run)
	return nil
}

// stopScheduleRun answers POST /v1/schedules/{scheduleId}/runs/{runId}/stop:
// 202, the stop is asked and the run settles stopped within the grace.
func (s *server) stopScheduleRun(w http.ResponseWriter, r *http.Request) error {
	run, err := s.svc.StopScheduleRun(r.Context(), r.PathValue("scheduleId"), r.PathValue("runId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, run)
	return nil
}

// getScheduleRunOutput answers GET
// /v1/schedules/{scheduleId}/runs/{runId}/output?stream=stdout|stderr: what
// the run printed on that stream, as plain text that is never cached, and
// whether its start was cut (Caravana-Output-Cut). The only answer that holds
// a run's output.
func (s *server) getScheduleRunOutput(w http.ResponseWriter, r *http.Request) error {
	// Before anything else, so no answer of this operation can be stored.
	w.Header().Set("Cache-Control", "no-store")
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "stdout"
	}
	out, cut, err := s.svc.GetScheduleRunOutput(r.Context(), r.PathValue("scheduleId"), r.PathValue("runId"), stream)
	if err != nil {
		return scheduleInvalid(err)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Caravana-Output-Cut", strconv.FormatBool(cut))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out) // the status is sent; a client that went away can't be told
	return nil
}
