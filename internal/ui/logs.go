package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// This file is the Logs tab (/apps/{id}/logs) and the log events of
// /live/apps/{id}?logs=1. The page draws the tail on the server, so it reads
// without script; with script, console.js clears the list when the stream says
// "logs-reset" and draws each "log" event as it arrives. Line text only ever
// goes through html/template here and through textContent there, never as
// markup (FR-018), and never into a logger (constitution VII).

// logOption is one choice of a select in the toolbar.
type logOption struct {
	Value, Label string
	Selected     bool
}

// logsForm is the toolbar's GET form: which copy and which stream.
type logsForm struct {
	Action  string
	Copies  []logOption
	Streams []logOption
}

// logEntry is one line of output as the page draws it.
type logEntry struct {
	CopyID string // for the title attribute
	Short  string // the copy's first 8 characters
	Stderr bool
	Text   string
}

// logsTab is the body of the Logs tab. With a Notice, there is no panel: the
// sentence says why. The toolbar stays whenever there are copies to choose
// from. HasTail says there is a panel to fill, with Lines, which may be none yet.
type logsTab struct {
	Form    *logsForm
	Lines   []logEntry
	Notice  sentence
	Stream  string // the stream's URL, which carries the choice: "/live/apps/blog?logs=1&copy=…"
	HasTail bool
}

// logChoice is the copy and stream the request asks for, checked.
type logChoice struct {
	copy   string
	stream deploy.LogStream // "" means both
}

// logChoiceOf reads ?copy= and ?stream=. ok is false for a stream that isn't
// stdout, stderr or both.
func logChoiceOf(values url.Values) (c logChoice, ok bool) {
	c.copy = values.Get("copy")
	switch st := values.Get("stream"); st {
	case "", "both":
	case "stdout", "stderr":
		c.stream = deploy.LogStream(st)
	default:
		return c, false
	}
	return c, true
}

// liveURL is the stream of the Logs tab for a choice.
func (c logChoice) liveURL(appID string) string {
	u := "/live/apps/" + appID + "?logs=1"
	if c.copy != "" {
		u += "&copy=" + url.QueryEscape(c.copy)
	}
	if c.stream != "" {
		u += "&stream=" + string(c.stream)
	}
	return u
}

func (c logChoice) query(lines int, follow bool) service.LogQuery {
	return service.LogQuery{Copy: c.copy, Stream: c.stream, Lines: lines, Follow: follow}
}

// loadLogs builds the tab for an app. The status is 200, or 404 for a copy
// that isn't the app's. A Nomad that can't be read is a Notice, not an error:
// the tab still opens.
func (s *server) loadLogs(ctx context.Context, appID string, c logChoice) (tab logsTab, status int, err error) {
	status = http.StatusOK
	copies, err := s.svc.LogCopies(ctx, appID, "")
	switch {
	case isNomadDown(err):
		s.log.Warn("can't read copies from Nomad", "app", appID, "error", err)
		tab.Notice = sentence{plain("Nomad can't be reached right now, so logs can't be shown.")}
		return tab, status, nil
	case err != nil:
		return tab, 0, err
	case len(copies) == 0:
		tab.Notice = sentence{plain("This app has no copies, so there are no logs to show.")}
		return tab, status, nil
	}

	form := &logsForm{Action: "/apps/" + appID + "/logs",
		Copies:  []logOption{{Value: "", Label: "All copies", Selected: c.copy == ""}},
		Streams: []logOption{{Value: "both", Label: "both", Selected: c.stream == ""}}}
	known := false
	for _, cp := range copies {
		known = known || cp.ID == c.copy
		form.Copies = append(form.Copies, logOption{Value: cp.ID, Label: shortID(cp.ID) + " · " + cp.Service, Selected: cp.ID == c.copy})
	}
	for _, st := range []deploy.LogStream{deploy.LogStdout, deploy.LogStderr} {
		form.Streams = append(form.Streams, logOption{Value: string(st), Label: string(st), Selected: c.stream == st})
	}
	tab.Form = form
	tab.Stream = c.liveURL(appID)

	if c.copy != "" && !known {
		tab.Notice = sentence{plain("The copy "), mono(c.copy), plain(" isn't one of this app's copies. A newer deploy may have replaced it; pick another above.")}
		tab.Stream = "" // nothing to follow
		return tab, http.StatusNotFound, nil
	}

	lines, err := s.readLogs(ctx, appID, c.query(500, false))
	switch {
	case isNomadDown(err):
		s.log.Warn("can't read logs from Nomad", "app", appID, "error", err)
		tab.Notice = sentence{plain("Nomad can't be reached right now, so logs can't be shown.")}
		return tab, status, nil
	case err != nil:
		return tab, 0, err
	}
	tab.HasTail = true
	for _, l := range lines {
		tab.Lines = append(tab.Lines, logEntry{CopyID: l.Copy, Short: shortID(l.Copy), Stderr: l.Stream == deploy.LogStderr, Text: l.Text})
	}
	return tab, status, nil
}

// readLogs collects the lines of a query that doesn't follow.
func (s *server) readLogs(ctx context.Context, appID string, q service.LogQuery) ([]service.LogLine, error) {
	out := make(chan service.LogLine)
	done := make(chan error, 1)
	go func() { done <- s.svc.Logs(ctx, appID, q, out) }()
	var lines []service.LogLine
	for {
		select {
		case l := <-out:
			lines = append(lines, l)
		case err := <-done:
			return lines, err
		}
	}
}

func isNomadDown(err error) bool {
	return errors.Is(err, deploy.ErrNomadUnavailable) || errors.Is(err, deploy.ErrNomadRejected)
}

// showLogs answers GET /apps/{id}/logs?copy=&stream=.
func (s *server) showLogs(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	choice, ok := logChoiceOf(r.URL.Query())
	if !ok {
		s.message(w, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.",
			"The \"stream\" value must be both, stdout or stderr. Go back to the Logs tab and use the choices there.")
		return
	}
	tab, status, err := s.loadLogs(ctx, id, choice)
	if errors.Is(err, store.ErrNotFound) { // the app went away while we looked
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read logs", err)
		return
	}
	head, err := s.shellPage(r, id+" logs", navProjects, s.appCrumbs(r.Context(), id)...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id
	if tab.Stream != "" {
		head.LiveURL = tab.Stream
	}
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(r.Context(), sum, "logs"), Logs: &tab})
}

// logEventData is the data of a "log" event: a LogLine of the API, as JSON.
type logEventData struct {
	Copy    string     `json:"copy"`
	Service string     `json:"service"`
	Stream  string     `json:"stream"`
	Text    string     `json:"text"`
	Time    *time.Time `json:"time,omitempty"`
}

// logStart returns the Start hook of a live stream that carries log lines
// (liveRoute.Start): it sends "logs-reset", then the tail and every new line
// as "log" events. When the app has no copies yet, or Nomad can't be read, it
// waits and tries again, so lines start flowing once there is something to
// read; it stops when the app or the chosen copy is gone, or the stream ends.
// A read that was going and then failed starts over, with a reset: the page
// clears its list and gets the tail again.
func (s *server) logStart(appID string, c logChoice) func(context.Context, *httpx.EventWriter) {
	return func(ctx context.Context, ev *httpx.EventWriter) {
		for {
			copies, err := s.svc.LogCopies(ctx, appID, c.copy)
			switch {
			case ctx.Err() != nil, errors.Is(err, store.ErrNotFound):
				return
			case err != nil:
				s.log.Warn("can't read copies for the log view", "app", appID, "error", err)
			case len(copies) > 0:
				if ev.Event("logs-reset", nil) != nil {
					return
				}
				err := s.pumpLogs(ctx, ev, appID, c)
				if ctx.Err() != nil || err == nil {
					return
				}
				s.log.Warn("the log view's read ended", "app", appID, "error", err)
			}
			select {
			case <-time.After(s.live.logRetryEvery()):
			case <-ctx.Done():
				return
			}
		}
	}
}

// pumpLogs follows the app's output into the stream until it ends. It returns
// the reading's error, or nil when the stream (ctx) or the output ended.
func (s *server) pumpLogs(ctx context.Context, ev *httpx.EventWriter, appID string, c logChoice) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan service.LogLine)
	done := make(chan error, 1)
	go func() { done <- s.svc.Logs(ctx, appID, c.query(500, true), out) }()
	for {
		select {
		case l := <-out:
			data, err := json.Marshal(logEventData{Copy: l.Copy, Service: l.Service, Stream: string(l.Stream), Text: l.Text, Time: l.Time})
			if err != nil || ev.Event("log", data) != nil {
				cancel()
				<-done
				return nil // the client is gone
			}
		case err := <-done:
			return err
		}
	}
}
