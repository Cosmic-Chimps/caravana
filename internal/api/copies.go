package api

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// copyJSON is one copy as the API shows it (schema Copy).
type copyJSON struct {
	ID          string     `json:"id"`
	Service     string     `json:"service"`
	Node        string     `json:"node"`
	Version     uint64     `json:"version"`
	Status      string     `json:"status"`
	Restarts    int        `json:"restarts"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	Description string     `json:"description,omitempty"`
	LastRestart *time.Time `json:"lastRestartAt,omitempty"`
	OutOfMemory bool       `json:"outOfMemory"`
	Usage       *usageJSON `json:"usage,omitempty"`
}

// usageJSON is a copy's CPU and memory (schema CopyUsage). A copy Nomad
// couldn't give numbers for has only Error, so the numbers are pointers: nil
// leaves them out, and a real 0 is still sent.
type usageJSON struct {
	CPUMHz           *int   `json:"cpuMHz,omitempty"`
	MemoryMB         *int   `json:"memoryMB,omitempty"`
	CPUReservedMHz   *int   `json:"cpuReservedMHz,omitempty"`
	MemoryReservedMB *int   `json:"memoryReservedMB,omitempty"`
	MemoryCeilingMB  *int   `json:"memoryCeilingMB,omitempty"`
	NearCeiling      *bool  `json:"nearCeiling,omitempty"`
	Error            string `json:"error,omitempty"`
}

func toUsageJSON(u service.CopyUsage) *usageJSON {
	if u.Error != "" {
		return &usageJSON{Error: u.Error}
	}
	return &usageJSON{CPUMHz: &u.CPUMHz, MemoryMB: &u.MemoryMB, CPUReservedMHz: &u.CPUReservedMHz,
		MemoryReservedMB: &u.MemoryReservedMB, MemoryCeilingMB: &u.MemoryCeilingMB, NearCeiling: &u.NearCeiling}
}

func toCopyJSON(c service.Copy) copyJSON {
	return copyJSON{ID: c.ID, Service: c.Service, Node: c.Node, Version: c.Version, Status: string(c.Status),
		Restarts: c.Restarts, StartedAt: c.StartedAt, Description: c.Description,
		LastRestart: c.LastRestart, OutOfMemory: c.OutOfMemory}
}

// listCopies answers GET /v1/apps/{id}/copies: the app's copies, as the
// Running tab shows them. A Nomad that can't be read is 502, since the list
// has nothing stored to fall back on. With usage=true each running copy also
// carries its CPU and memory, which costs a request to Nomad per copy; without
// it Nomad is asked for the list only.
func (s *server) listCopies(w http.ResponseWriter, r *http.Request) error {
	withUsage := false
	if r.URL.Query().Has("usage") {
		switch u := r.URL.Query().Get("usage"); u {
		case "true":
			withUsage = true
		case "false":
		default:
			return app.NewFieldError("usage", "must be true or false, not %q", u)
		}
	}

	var (
		copies []service.Copy
		usage  map[string]service.CopyUsage
		err    error
	)
	if withUsage {
		copies, usage, err = s.svc.CopiesWithUsage(r.Context(), r.PathValue("id"))
	} else {
		copies, err = s.svc.Copies(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		return err
	}
	out := struct {
		Copies []copyJSON `json:"copies"`
	}{Copies: make([]copyJSON, 0, len(copies))}
	for _, c := range copies {
		cj := toCopyJSON(c)
		if u, ok := usage[c.ID]; ok {
			cj.Usage = toUsageJSON(u)
		}
		out.Copies = append(out.Copies, cj)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// logLineJSON is one line of output (schema LogLine).
type logLineJSON struct {
	Copy    string     `json:"copy"`
	Service string     `json:"service"`
	Stream  string     `json:"stream"`
	Text    string     `json:"text"`
	Time    *time.Time `json:"time,omitempty"`
}

func toLogLineJSON(l service.LogLine) logLineJSON {
	return logLineJSON{Copy: l.Copy, Service: l.Service, Stream: string(l.Stream), Text: l.Text, Time: l.Time}
}

// The defaults for an event stream's timings (research R11, R12).
const (
	defaultKeepalive = 20 * time.Second // a comment line, so proxies keep an idle stream
	defaultRecheck   = 30 * time.Second // how often the credentials are checked again
)

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// logQuery reads and checks the query of getLogs. Every problem is an
// *app.FieldError naming the parameter, which the adapter answers as 400
// invalid_field.
func logQuery(r *http.Request) (q service.LogQuery, err error) {
	values := r.URL.Query()
	q.Copy = values.Get("copy")
	switch st := values.Get("stream"); st {
	case "", "both":
	case "stdout", "stderr":
		q.Stream = deploy.LogStream(st)
	default:
		return q, app.NewFieldError("stream", "must be stdout, stderr or both, not %q", st)
	}
	q.Lines = 500
	if values.Has("lines") {
		n, convErr := strconv.Atoi(values.Get("lines"))
		if convErr != nil || n < 1 || n > 500 {
			return q, app.NewFieldError("lines", "must be a whole number from 1 to 500, not %q", values.Get("lines"))
		}
		q.Lines = n
	}
	if values.Has("follow") {
		switch f := values.Get("follow"); f {
		case "true":
			q.Follow = true
		case "false":
		default:
			return q, app.NewFieldError("follow", "must be true or false, not %q", f)
		}
	}
	return q, nil
}

// getLogs answers GET /v1/apps/{id}/logs: the app's recent output as JSON, or,
// with follow=true, as an event stream that goes on with each new line. The
// route is quiet: following doesn't renew a browser session.
//
// Everything that can be wrong with the request, the app, the copy or Nomad is
// found before the stream starts, so it is a plain JSON error like any other
// (a stream's status is sent once, at its start).
func (s *server) getLogs(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	q, err := logQuery(r)
	if err != nil {
		return err
	}
	if _, err := s.svc.LogCopies(r.Context(), id, q.Copy); err != nil {
		return err
	}
	if q.Follow {
		s.followLogs(w, r, id, q)
		return nil
	}

	lines := []logLineJSON{}
	out := make(chan service.LogLine)
	done := make(chan error, 1)
	go func() { done <- s.svc.Logs(r.Context(), id, q, out) }()
	for read := true; read; {
		select {
		case l := <-out:
			lines = append(lines, toLogLineJSON(l))
		case err := <-done:
			if err != nil {
				return err
			}
			read = false
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Lines []logLineJSON `json:"lines"`
	}{lines})
	return nil
}

// followLogs answers a followed getLogs with an event stream: each line is a
// "log" event whose data is a LogLine as JSON. It sends a keepalive comment
// while idle, checks every so often that the credentials still hold (an
// "ended" event and the end of the stream when they don't), and returns when the
// client leaves, the server shuts down, or Nomad stops serving. The text of
// the lines goes to the client and nowhere else: not to the log, not to errors.
func (s *server) followLogs(w http.ResponseWriter, r *http.Request, appID string, q service.LogQuery) {
	ev, err := httpx.Stream(w)
	if err != nil {
		s.log.Error("start log stream", "app", appID, "error", err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	out := make(chan service.LogLine)
	done := make(chan error, 1)
	go func() { done <- s.svc.Logs(ctx, appID, q, out) }()
	// Whichever way this ends, Logs is told to stop and has stopped before the handler returns.
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()

	keepalive := time.NewTicker(orDefault(s.keepalive, defaultKeepalive))
	defer keepalive.Stop()
	recheck := time.NewTicker(orDefault(s.recheck, defaultRecheck))
	defer recheck.Stop()
	stillValid := s.credentialCheck(r)

	for {
		select {
		case l := <-out:
			data, err := json.Marshal(toLogLineJSON(l), encodeOptions)
			if err != nil || ev.Event("log", data) != nil {
				return // the client is gone
			}
		case err := <-done:
			finished = true
			if err != nil && ctx.Err() == nil {
				// What went wrong, without any of the output.
				s.log.Warn("log stream ended", "app", appID, "error", err)
			}
			return
		case <-keepalive.C:
			if ev.Comment("keepalive") != nil {
				return
			}
		case <-recheck.C:
			switch err := stillValid(ctx); {
			case err == nil:
			case errors.Is(err, service.ErrNotAuthenticated), errors.Is(err, service.ErrUnauthorized):
				_ = ev.Event("ended", nil)
				return
			case ctx.Err() != nil:
				return
			default:
				// The store failed, not the credentials: try again next time.
				s.log.Warn("re-check log stream credentials", "error", err)
			}
		case <-ctx.Done():
			return
		}
	}
}
