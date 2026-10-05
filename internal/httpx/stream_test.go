package httpx

import (
	"bufio"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventFormat(t *testing.T) {
	tests := []struct {
		name string
		send func(e *EventWriter) error
		want string
	}{
		{"nil data", func(e *EventWriter) error { return e.Event("sync", nil) }, "event: sync\ndata: \n\n"},
		{"one line", func(e *EventWriter) error { return e.Event("log", []byte("hello")) }, "event: log\ndata: hello\n\n"},
		{"multi-line", func(e *EventWriter) error { return e.Event("log", []byte("a\nb\n")) },
			"event: log\ndata: a\ndata: b\ndata: \n\n"},
		{"carriage returns", func(e *EventWriter) error { return e.Event("log", []byte("a\r\nb\rc")) },
			"event: log\ndata: a\ndata: b\ndata: c\n\n"},
		{"comment", func(e *EventWriter) error { return e.Comment("keepalive\nx") }, ": keepalive x\n\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e, err := Stream(rec)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if err := tc.send(e); err != nil {
				t.Fatalf("send: %v", err)
			}
			want := "retry: 3000\n\n" + tc.want
			if got := rec.Body.String(); got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
			if !rec.Flushed {
				t.Error("response was not flushed")
			}
		})
	}
}

func TestEventRejectsBadNames(t *testing.T) {
	e, err := Stream(httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "a\nb", "a\rb"} {
		if err := e.Event(name, nil); err == nil {
			t.Errorf("Event(%q) = nil, want an error", name)
		}
	}
}

func TestStreamHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	if _, err := Stream(rec); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-store",
		"X-Accel-Buffering": "no",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// noFlush is a ResponseWriter that can't flush and doesn't unwrap.
type noFlush struct{ h http.Header }

func (n noFlush) Header() http.Header         { return n.h }
func (n noFlush) Write(b []byte) (int, error) { return len(b), nil }
func (n noFlush) WriteHeader(int)             {}

func TestStreamNeedsFlush(t *testing.T) {
	if _, err := Stream(noFlush{h: http.Header{}}); err == nil {
		t.Error("Stream on a writer that can't flush = nil error, want an error")
	}
}

// A server WriteTimeout of 100 ms must not cut a stream that lasts 300 ms,
// even with LogRequests wrapping the writer in front.
func TestStreamSurvivesWriteTimeout(t *testing.T) {
	var logged strings.Builder
	log := slog.New(slog.NewTextHandler(&logged, nil))

	h := LogRequests(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, err := Stream(w)
		if err != nil {
			t.Errorf("Stream: %v", err)
			return
		}
		_ = e.Event("first", []byte("1"))
		time.Sleep(300 * time.Millisecond)
		_ = e.Event("second", []byte("a\nb"))
	}))
	srv := httptest.NewUnstartedServer(h)
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}

	// retry, first, second. The first event must arrive before the sleep ends
	// (flushing through LogRequests works), the second after the deadline.
	start := time.Now()
	sc := bufio.NewScanner(resp.Body)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) == 1 && !strings.HasPrefix(lines[0], "retry:") {
			t.Fatalf("first line = %q, want retry", lines[0])
		}
		if sc.Text() == "data: b" {
			break
		}
		if len(lines) == 4 && time.Since(start) > 200*time.Millisecond {
			t.Errorf("first event took %v, it was not flushed", time.Since(start))
		}
	}
	got := strings.Join(lines, "|")
	want := "retry: 3000||event: first|data: 1||event: second|data: a|data: b"
	if got != want {
		t.Errorf("stream = %q, want %q", got, want)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Errorf("stream lasted %v, want it to outlive the 100 ms write timeout", time.Since(start))
	}
}
