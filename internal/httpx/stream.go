package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// EventWriter writes server-sent events to one response. It is safe to use
// from several goroutines; each event is written and flushed as one piece.
type EventWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	rc *http.ResponseController
}

// Stream prepares w for server-sent events and returns a writer for them.
//
// The server's WriteTimeout would cut every stream, so Stream clears the write
// deadline of this one response (C# comparison: there is no single equivalent;
// Kestrel limits request body rates, not total response time). It then sets
// the event-stream headers, tells the browser to retry after 3 s, and flushes
// so the client sees the response at once.
//
// The error is returned when w can't flush, since a stream that stays in a
// buffer is useless. A writer that has no write deadline to clear (for
// example httptest.ResponseRecorder) is fine.
func Stream(w http.ResponseWriter) (*EventWriter, error) {
	rc := http.NewResponseController(w)
	// ErrNotSupported only means "no deadline to clear"; anything else is a
	// real failure to report.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, fmt.Errorf("clear write deadline: %w", err)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // ask proxies such as nginx not to buffer
	w.WriteHeader(http.StatusOK)

	e := &EventWriter{w: w, rc: rc}
	if err := e.write("retry: 3000\n\n"); err != nil {
		return nil, fmt.Errorf("start event stream: %w", err)
	}
	return e, nil
}

// Event sends one named event. data may be nil; newlines in it become
// separate "data:" lines, which the browser joins back with "\n".
func (e *EventWriter) Event(name string, data []byte) error {
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return fmt.Errorf("invalid event name %q", name)
	}
	var b strings.Builder
	b.WriteString("event: " + name + "\n")
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for line := range strings.SplitSeq(text, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	return e.write(b.String())
}

// Comment sends a comment line, which browsers ignore. It keeps an idle
// connection open through proxies.
func (e *EventWriter) Comment(text string) error {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	return e.write(": " + text + "\n\n")
}

// write sends s and flushes it.
func (e *EventWriter) write(s string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write([]byte(s)); err != nil {
		return err
	}
	if err := e.rc.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}
