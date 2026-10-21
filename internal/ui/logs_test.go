package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

const (
	logCopyA = "aaaa1111-0000-4000-8000-00000000000a" // started first
	logCopyB = "bbbb2222-0000-4000-8000-00000000000b"
)

// logsEnv is whoami at v7 with two copies of its one service, both with output on both streams.
func logsEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: "console", Status: "healthy", Version: 7})
	start := func(minute int) map[string]*api.TaskState {
		return map[string]*api.TaskState{"web": {StartedAt: time.Date(2026, 10, 4, 9, minute, 0, 0, time.UTC)}}
	}
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: logCopyB, TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-2", TaskStates: start(9)},
		api.AllocationListStub{ID: logCopyA, TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-1", TaskStates: start(5)})
	e.Nomad.WriteLog(logCopyA, "stdout", "GET / 200\nGET /health 200\n")
	e.Nomad.WriteLog(logCopyA, "stderr", "warning: slow\n")
	e.Nomad.WriteLog(logCopyB, "stdout", "listening on :80\n")
	return e
}

// sseEvent is one event of a stream with its data.
type sseEvent struct{ name, data string }

// eventStream is an open stream read event by event.
type eventStream struct {
	t      *testing.T
	events chan sseEvent // closed when the stream ends; comments are left out
	cancel context.CancelFunc
}

func openEvents(t *testing.T, base, path, cookie string) *eventStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "caravana_session", Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", path, err)
	}
	es := &eventStream{t: t, events: make(chan sseEvent, 512), cancel: cancel}
	go func() {
		defer close(es.events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.name != "" {
					es.events <- cur
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				cur.name = line[len("event: "):]
			case strings.HasPrefix(line, "data: "):
				if cur.data != "" {
					cur.data += "\n"
				}
				cur.data += line[len("data: "):]
			}
		}
	}()
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return es
}

// next returns the next event; ok is false when the stream ended.
func (es *eventStream) next() (sseEvent, bool) {
	es.t.Helper()
	select {
	case ev, ok := <-es.events:
		return ev, ok
	case <-time.After(5 * time.Second):
		es.t.Fatal("no event within 5s")
	}
	return sseEvent{}, false
}

// expect returns the next event, which must have the name.
func (es *eventStream) expect(name string) sseEvent {
	es.t.Helper()
	ev, ok := es.next()
	if !ok || ev.name != name {
		es.t.Fatalf("next event = %q (stream open: %v), want %q", ev.name, ok, name)
	}
	return ev
}

// logEvent is a "log" event's data.
type logEvent struct {
	Copy    string  `json:"copy"`
	Service string  `json:"service"`
	Stream  string  `json:"stream"`
	Text    string  `json:"text"`
	Time    *string `json:"time"`
}

func (es *eventStream) expectLog() logEvent {
	es.t.Helper()
	ev := es.expect("log")
	var l logEvent
	if err := json.Unmarshal([]byte(ev.data), &l); err != nil {
		es.t.Fatalf("log event data %q is not JSON: %v", ev.data, err)
	}
	return l
}

// logSkippingNotices is expectLog for streams where the copies watcher may interleave an
// "app" notice before the line.
func (es *eventStream) logSkippingNotices() logEvent {
	es.t.Helper()
	ev := es.skipUntil("log")
	var l logEvent
	if err := json.Unmarshal([]byte(ev.data), &l); err != nil {
		es.t.Fatalf("log event data %q is not JSON: %v", ev.data, err)
	}
	return l
}

// skipUntil reads events until one with the name, and returns it.
func (es *eventStream) skipUntil(name string) sseEvent {
	es.t.Helper()
	for {
		ev, ok := es.next()
		if !ok {
			es.t.Fatalf("the stream ended before a %q event", name)
		}
		if ev.name == name {
			return ev
		}
	}
}

func waitForLogStreams(t *testing.T, e *consoleEnv, n int) {
	t.Helper()
	waitFor(t, "the open log streams", func() bool { return e.Nomad.OpenLogStreams() == n })
}

func TestLogsTabRendersTheToolbarAndTheTail(t *testing.T) {
	e := logsEnv(t)
	resp, body := e.get(t, "/apps/whoami/logs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/whoami/logs = %d:\n%s", resp.StatusCode, body)
	}

	for _, want := range []string{
		// the tab, in the header and the breadcrumbs
		`<a href="/apps/whoami/logs" class="cv-tab" aria-current="page">Logs`,
		`hx-get="/parts/apps/whoami/header?tab=logs"`,
		`aria-current="page">production</span>`,
		// the toolbar is a GET form, so it works without script
		`<form method="get" action="/apps/whoami/logs"`,
		`<select`, `name="copy"`, `name="stream"`,
		`>All copies<`, `>stdout<`, `>stderr<`, `>Apply<`,
		// the buttons only script can use start out hidden and not pressed
		`data-log-pause`, `data-log-wrap`, `aria-pressed="false"`,
		// the stream
		`sse-connect="/live/apps/whoami?logs=1"`,
		`sse-swap="log,logs-reset"`, `hx-swap="none"`, `data-log-sink`,
		// the note for readers without script
		`<noscript>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Logs tab lacks %q:\n%s", want, body)
		}
	}

	// Copy options: each copy's short ID and service, the full ID as the value.
	for _, want := range []string{`value="` + logCopyA + `"`, `value="` + logCopyB + `"`, "aaaa1111 · web", "bbbb2222 · web"} {
		if !strings.Contains(body, want) {
			t.Errorf("the copy select lacks %q", want)
		}
	}

	// The tail, rendered on the server, in the merge order: copy A's stdout, its stderr, then B's.
	got := text(body)
	order := []string{"GET / 200", "GET /health 200", "warning: slow", "listening on :80"}
	last := -1
	for _, line := range order {
		at := strings.Index(got, line)
		if at < 0 || at < last {
			t.Fatalf("the tail lacks %q, or has it out of order: %s", line, got)
		}
		last = at
	}
	if !strings.Contains(body, `title="`+logCopyA+`"`) || !strings.Contains(body, ">aaaa1111<") {
		t.Errorf("a line doesn't show its copy's short ID with the whole one in the title:\n%s", body)
	}
	if n := strings.Count(body, `<span class="cv-log-stream">stderr</span>`); n != 1 {
		t.Errorf("%d stderr tags, want 1 (only the stderr line carries one, and it says stderr in full)", n)
	}
	// An ordinary stderr line isn't a warning: its tag is muted, with no status color.
	if strings.Contains(body, "cv-log-stream-err") {
		t.Error("the stderr tag still has the amber class")
	}
}

func TestLogsTabShowsLinesAsText(t *testing.T) {
	e := logsEnv(t)
	e.Nomad.WriteLog(logCopyA, "stdout", "<b>x</b> <script>alert(1)</script> &amp; \"quoted\" <img src=x onerror=alert(2)>\n")
	_, body := e.get(t, "/apps/whoami/logs")
	for _, raw := range []string{"<b>x</b>", "<script>alert(1)", "<img src=x"} {
		if strings.Contains(body, raw) {
			t.Errorf("the page has %q as markup", raw)
		}
	}
	if !strings.Contains(body, "&lt;b&gt;x&lt;/b&gt;") || !strings.Contains(text(body), "<b>x</b> <script>alert(1)</script> &amp; \"quoted\"") {
		t.Errorf("the line isn't shown as its own text:\n%s", body)
	}
}

func TestLogsTabFilters(t *testing.T) {
	e := logsEnv(t)
	tests := []struct {
		query       string
		want, wants []string // lines shown, lines absent
		selected    []string // options marked selected
		stream      string   // the part of the stream URL after logs=1
	}{
		{"?copy=" + logCopyB, []string{"listening on :80"}, []string{"GET / 200", "warning: slow"}, []string{`value="` + logCopyB + `" selected`}, "&amp;copy=" + logCopyB},
		{"?stream=stderr", []string{"warning: slow"}, []string{"GET / 200", "listening on :80"}, []string{`value="stderr" selected`}, "&amp;stream=stderr"},
		{"?copy=" + logCopyA + "&stream=stdout", []string{"GET / 200", "GET /health 200"}, []string{"warning: slow", "listening on :80"},
			[]string{`value="` + logCopyA + `" selected`, `value="stdout" selected`}, "&amp;copy=" + logCopyA + "&amp;stream=stdout"},
		{"?stream=both", []string{"GET / 200", "warning: slow", "listening on :80"}, nil, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			resp, body := e.get(t, "/apps/whoami/logs"+tc.query)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			got := text(body)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing line %q", w)
				}
			}
			for _, w := range tc.wants {
				if strings.Contains(got, w) {
					t.Errorf("unexpected line %q", w)
				}
			}
			for _, s := range tc.selected {
				if !strings.Contains(body, s) {
					t.Errorf("not marked selected: %s", s)
				}
			}
			if want := `sse-connect="/live/apps/whoami?logs=1` + tc.stream + `"`; !strings.Contains(body, want) {
				t.Errorf("the page's stream doesn't carry the choice; want %s", want)
			}
		})
	}
}

func TestLogsTabSentences(t *testing.T) {
	t.Run("no copies", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp())
		resp, body := e.get(t, "/apps/whoami/logs")
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "This app has no copies, so there are no logs to show.") {
			t.Errorf("status %d, text %s", resp.StatusCode, text(body))
		}
		if strings.Contains(body, "data-log-pause") {
			t.Error("the toolbar is shown with nothing to read")
		}
	})
	t.Run("copies without output", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp())
		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: logCopyA, TaskGroup: "web", ClientStatus: "running"})
		_, body := e.get(t, "/apps/whoami/logs")
		if !strings.Contains(text(body), "No output yet.") || !strings.Contains(body, "data-log-pause") {
			t.Errorf("text %s", text(body))
		}
	})
	t.Run("Nomad down", func(t *testing.T) {
		e := logsEnv(t)
		e.Nomad.Down()
		resp, body := e.get(t, "/apps/whoami/logs")
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "Nomad can't be reached right now, so logs can't be shown.") {
			t.Errorf("status %d, text %s", resp.StatusCode, text(body))
		}
	})
	t.Run("a copy that isn't the app's", func(t *testing.T) {
		e := logsEnv(t)
		resp, body := e.get(t, "/apps/whoami/logs?copy=nope")
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "isn't one of this app's copies") {
			t.Errorf("status %d, text %s", resp.StatusCode, text(body))
		}
	})
	t.Run("a stream that isn't one", func(t *testing.T) {
		e := logsEnv(t)
		resp, _ := e.get(t, "/apps/whoami/logs?stream=banana")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status %d, want 400", resp.StatusCode)
		}
	})
	t.Run("an unknown app", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.get(t, "/apps/nope/logs")
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "There's no app called nope.") {
			t.Errorf("status %d, text %s", resp.StatusCode, text(body))
		}
	})
	t.Run("signed out", func(t *testing.T) {
		e := logsEnv(t)
		resp, _ := e.do(t, request{path: "/apps/whoami/logs"})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login?next=") {
			t.Errorf("status %d to %q, want a redirect to sign in", resp.StatusCode, location(t, resp))
		}
	})
}

func TestLogsTabObeysThePolicy(t *testing.T) {
	e := logsEnv(t)
	_, body := e.get(t, "/apps/whoami/logs")
	for _, banned := range []string{" style=", "<style", "hx-on", "javascript:", " onclick="} {
		if strings.Contains(body, banned) {
			t.Errorf("the Logs tab contains %q", banned)
		}
	}
	for _, tag := range regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(body, -1) {
		if !strings.Contains(tag, ` src="/static/js/`) {
			t.Errorf("the Logs tab has an inline script: %s", tag)
		}
	}
}

func TestLiveLogsStream(t *testing.T) {
	e := logsEnv(t)
	es := openEvents(t, e.URL, "/live/apps/whoami?logs=1", e.Cookie.Value)
	es.expect("sync")
	es.expect("logs-reset")

	// The tail, as log events in the merge order; the tail has no times.
	var tail []string
	for range 4 {
		l := es.expectLog()
		tail = append(tail, l.Text)
		if l.Time != nil || l.Service != "web" {
			t.Errorf("tail line = %+v, want no time and the service", l)
		}
		if l.Text == "warning: slow" && (l.Stream != "stderr" || l.Copy != logCopyA) {
			t.Errorf("stderr line = %+v", l)
		}
	}
	if got := strings.Join(tail, "|"); got != "GET / 200|GET /health 200|warning: slow|listening on :80" {
		t.Errorf("tail = %s", got)
	}

	// New lines follow, each with a time.
	waitForLogStreams(t, e, 4) // both streams of both copies
	e.Nomad.WriteLog(logCopyB, "stderr", "boom <b>\n")
	l := es.expectLog()
	if l.Text != "boom <b>" || l.Copy != logCopyB || l.Stream != "stderr" || l.Time == nil {
		t.Errorf("followed line = %+v", l)
	}

	// Leaving ends the reads of Nomad.
	es.cancel()
	waitForLogStreams(t, e, 0)
}

func TestLiveLogsStreamFilters(t *testing.T) {
	e := logsEnv(t)
	es := openEvents(t, e.URL, "/live/apps/whoami?logs=1&copy="+logCopyB+"&stream=stdout", e.Cookie.Value)
	es.expect("sync")
	es.expect("logs-reset")
	if l := es.expectLog(); l.Text != "listening on :80" {
		t.Errorf("line = %+v", l)
	}
	waitForLogStreams(t, e, 1)
	e.Nomad.WriteLog(logCopyA, "stdout", "not for this stream\n")
	e.Nomad.WriteLog(logCopyB, "stderr", "nor this\n")
	e.Nomad.WriteLog(logCopyB, "stdout", "this one\n")
	if l := es.expectLog(); l.Text != "this one" {
		t.Errorf("followed line = %+v, want only the chosen copy's stdout", l)
	}
}

func TestLiveWithoutLogsSendsNoLines(t *testing.T) {
	e := logsEnv(t)
	es := openEvents(t, e.URL, "/live/apps/whoami", e.Cookie.Value)
	es.expect("sync")
	e.Nomad.WriteLog(logCopyA, "stdout", "more\n")
	select {
	case ev, ok := <-es.events:
		if ok {
			t.Errorf("event %q on a stream that didn't ask for logs", ev.name)
		}
	case <-time.After(300 * time.Millisecond):
	}
	if n := e.Nomad.OpenLogStreams(); n != 0 {
		t.Errorf("%d log streams opened for a page without logs", n)
	}
}

func TestLiveLogsStreamWaitsForCopies(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp())
	e.Server.live.logRetry = 20 * time.Millisecond
	es := openEvents(t, e.URL, "/live/apps/whoami?logs=1", e.Cookie.Value)
	es.expect("sync")

	// No copies yet: no reset, no lines. When one appears, the stream starts.
	time.Sleep(100 * time.Millisecond)
	e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: logCopyA, TaskGroup: "web", ClientStatus: "running"})
	e.Nomad.WriteLog(logCopyA, "stdout", "first light\n")
	es.skipUntil("logs-reset")
	if l := es.logSkippingNotices(); l.Text != "first light" {
		t.Errorf("line = %+v", l)
	}
}

func TestLiveLogsStreamSurvivesNomadGoingAway(t *testing.T) {
	e := logsEnv(t)
	e.Server.live.logRetry = 20 * time.Millisecond
	e.Nomad.Down()
	es := openEvents(t, e.URL, "/live/apps/whoami?logs=1", e.Cookie.Value)
	es.expect("sync")
	time.Sleep(100 * time.Millisecond)
	e.Nomad.Up()
	es.skipUntil("logs-reset") // it tries again, and starts over
	if l := es.expectLog(); l.Text != "GET / 200" {
		t.Errorf("first line after the comeback = %+v", l)
	}
}

func TestLogLinesStayOutOfTheServerLog(t *testing.T) {
	e := logsEnv(t)
	e.Nomad.WriteLog(logCopyA, "stdout", "api_key=sk-live-not-for-logs\n")
	_, body := e.get(t, "/apps/whoami/logs")
	if !strings.Contains(body, "sk-live-not-for-logs") {
		t.Fatal("the line isn't on the page")
	}
	es := openEvents(t, e.URL, "/live/apps/whoami?logs=1&copy="+url.QueryEscape(logCopyA), e.Cookie.Value)
	es.skipUntil("logs-reset")
	for range 4 {
		es.expectLog()
	}
	es.cancel()
	waitForLogStreams(t, e, 0)
	time.Sleep(50 * time.Millisecond)
	if out := e.Logs.String(); strings.Contains(out, "sk-live-not-for-logs") {
		t.Errorf("a log line's text reached the server log:\n%s", out)
	}
}
