package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/httpx"
)

// copyBody is the part of a Copy the tests read.
type copyBody struct {
	ID          string  `json:"id"`
	Service     string  `json:"service"`
	Node        string  `json:"node"`
	Version     uint64  `json:"version"`
	Status      string  `json:"status"`
	Restarts    int     `json:"restarts"`
	StartedAt   *string `json:"startedAt"`
	Description string  `json:"description"`
	LastRestart *string `json:"lastRestartAt"`
	OutOfMemory bool    `json:"outOfMemory"`
	Usage       *struct {
		CPUMHz           *int   `json:"cpuMHz"`
		MemoryMB         *int   `json:"memoryMB"`
		CPUReservedMHz   *int   `json:"cpuReservedMHz"`
		MemoryReservedMB *int   `json:"memoryReservedMB"`
		MemoryCeilingMB  *int   `json:"memoryCeilingMB"`
		NearCeiling      *bool  `json:"nearCeiling"`
		Error            string `json:"error"`
	} `json:"usage"`
}

// US4: the Running tab's twin.
func TestListCopies(t *testing.T) {
	e := newTestEnv(t, "")
	seedHealthy(t, e, "whoami", 2, 7)
	started := time.Date(2026, 10, 4, 9, 5, 0, 0, time.UTC)
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "a1", TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-1",
			ClientDescription: "Tasks are running",
			TaskStates: map[string]*api.TaskState{"web": {Restarts: 2, StartedAt: started, LastRestart: started.Add(time.Minute),
				Events: []*api.TaskEvent{{Type: api.TaskTerminated, Details: map[string]string{"oom_killed": "true"}}}}}},
		api.AllocationListStub{ID: "a2", TaskGroup: "web", JobVersion: 7, ClientStatus: "pending", NodeName: "node-2"},
		api.AllocationListStub{ID: "a0", TaskGroup: "web", JobVersion: 6, ClientStatus: "failed", NodeName: "node-1"})

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/copies", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET copies = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "CopyList")
	got := decodeAs[struct {
		Copies []copyBody `json:"copies"`
	}](t, raw).Copies

	// The ended copy of an older version is left out; the others are newest version first, started before unstarted.
	if len(got) != 2 || got[0].ID != "a1" || got[1].ID != "a2" {
		t.Fatalf("copies = %+v, want a1 then a2", got)
	}
	first := got[0]
	if first.Service != "web" || first.Node != "node-1" || first.Version != 7 || first.Status != "healthy" || first.Restarts != 2 ||
		first.Description != "Tasks are running" || first.StartedAt == nil || *first.StartedAt != "2026-10-04T09:05:00Z" ||
		first.LastRestart == nil || *first.LastRestart != "2026-10-04T09:06:00Z" || !first.OutOfMemory {
		t.Errorf("first copy = %+v", first)
	}
	if second := got[1]; second.Status != "starting" || second.StartedAt != nil || second.LastRestart != nil || second.OutOfMemory {
		t.Errorf("second copy = %+v, want starting with no start time", second)
	}

	t.Run("an app with no copies answers an empty list", func(t *testing.T) {
		seedHealthy(t, e, "quiet", 1, 1)
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/quiet/copies", "", "")
		if status != http.StatusOK {
			t.Fatalf("status = %d %s", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "CopyList")
		if string(raw) == "" || decodeAs[map[string][]copyBody](t, raw)["copies"] == nil {
			t.Errorf("body = %s, want \"copies\": []", raw)
		}
	})

	t.Run("an unknown app is 404", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/ghost/copies", "", "")
		if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeNotFound {
			t.Errorf("status %d body %s, want 404 not_found", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Error")
	})

	t.Run("Nomad down is 502 nomad_unavailable", func(t *testing.T) {
		e.Nomad.Down()
		defer e.Nomad.Up()
		status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/copies", "", "")
		if status != http.StatusBadGateway || decodeAs[errorBody](t, raw).Error.Code != codeNomadUnavailable {
			t.Errorf("status %d body %s, want 502 nomad_unavailable", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Error")
		if loc := header.Get("Location"); loc != "" {
			t.Errorf("Location = %q, want none: no deployment record is involved", loc)
		}
	})
}

// US4: the Running tab's CPU and memory.
func TestListCopiesUsage(t *testing.T) {
	e := newTestEnv(t, "")
	seedHealthy(t, e, "whoami", 3, 7)
	started := time.Date(2026, 10, 4, 9, 5, 0, 0, time.UTC)
	running := func(id string) api.AllocationListStub {
		return api.AllocationListStub{ID: id, TaskGroup: "web", JobVersion: 7, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {StartedAt: started}}}
	}
	usage := func(ticks float64, mb uint64) api.AllocResourceUsage {
		return api.AllocResourceUsage{Tasks: map[string]*api.TaskResourceUsage{"web": {ResourceUsage: &api.ResourceUsage{
			CpuStats:    &api.CpuStats{TotalTicks: ticks},
			MemoryStats: &api.MemoryStats{Usage: mb << 20, Measured: []string{"Usage"}},
		}}}}
	}
	e.Nomad.SetCopies("whoami", running("a1"), running("a2"), running("a3"),
		api.AllocationListStub{ID: "a4", TaskGroup: "web", JobVersion: 7, ClientStatus: "pending"})
	e.Nomad.SetStats("a1", usage(0, 40))
	e.Nomad.SetStats("a2", usage(75, 240)) // 90 % of the 256 MB ceiling is 230.4
	// a3 has no stats: Nomad answers 404 for it.

	get := func(t *testing.T, query string) map[string]copyBody {
		t.Helper()
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/copies"+query, "", "")
		if status != http.StatusOK {
			t.Fatalf("GET copies%s = %d %s", query, status, raw)
		}
		checkSchema(t, loadContract(t), raw, "CopyList")
		byID := map[string]copyBody{}
		for _, c := range decodeAs[struct {
			Copies []copyBody `json:"copies"`
		}](t, raw).Copies {
			byID[c.ID] = c
		}
		return byID
	}

	t.Run("with usage=true running copies have numbers, with the reservation", func(t *testing.T) {
		got := get(t, "?usage=true")
		// The test service reserves 100 MHz and 64 MB, with a 256 MB ceiling.
		u := got["a1"].Usage
		if u == nil || u.CPUMHz == nil || *u.CPUMHz != 0 || u.MemoryMB == nil || *u.MemoryMB != 40 ||
			*u.CPUReservedMHz != 100 || *u.MemoryReservedMB != 64 || *u.MemoryCeilingMB != 256 || *u.NearCeiling || u.Error != "" {
			t.Errorf("a1 usage = %+v, want 0 MHz (sent, not left out) and 40 MB, not near the ceiling", u)
		}
		if u := got["a2"].Usage; u == nil || *u.CPUMHz != 75 || *u.MemoryMB != 240 || !*u.NearCeiling {
			t.Errorf("a2 usage = %+v, want 75 MHz, 240 MB, near the ceiling", u)
		}
	})
	t.Run("a copy Nomad can't answer for has only an error", func(t *testing.T) {
		got := get(t, "?usage=true")
		u := got["a3"].Usage
		if u == nil || u.Error != "Nomad didn't answer for this copy." || u.CPUMHz != nil || u.MemoryMB != nil || u.NearCeiling != nil {
			t.Errorf("a3 usage = %+v, want only an error", u)
		}
	})
	t.Run("a copy that hasn't started has no usage", func(t *testing.T) {
		if u := get(t, "?usage=true")["a4"].Usage; u != nil {
			t.Errorf("a4 usage = %+v, want none", u)
		}
	})
	t.Run("without the parameter nothing changes and Nomad isn't asked for stats", func(t *testing.T) {
		before := e.Nomad.StatsRequests()
		for _, q := range []string{"", "?usage=false"} {
			for id, c := range get(t, q) {
				if c.Usage != nil {
					t.Errorf("copies%s: %s has usage %+v", q, id, c.Usage)
				}
			}
		}
		if n := e.Nomad.StatsRequests() - before; n != 0 {
			t.Errorf("%d stats requests, want 0", n)
		}
	})
	t.Run("a bad value is 400 invalid_field", func(t *testing.T) {
		for _, q := range []string{"?usage=yes", "?usage=", "?usage=TRUE", "?usage=1"} {
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/copies"+q, "", "")
			body := decodeAs[errorBody](t, raw)
			if status != http.StatusBadRequest || body.Error.Code != codeInvalidField || body.Error.Field != "usage" {
				t.Errorf("copies%s = %d %s, want 400 invalid_field on usage", q, status, raw)
			}
			checkSchema(t, loadContract(t), raw, "Error")
		}
	})
	t.Run("an unknown app is 404 and Nomad down is 502", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/ghost/copies?usage=true", "", "")
		if status != http.StatusNotFound {
			t.Errorf("ghost = %d %s", status, raw)
		}
		e.Nomad.Down()
		defer e.Nomad.Up()
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/copies?usage=true", "", "")
		if status != http.StatusBadGateway || decodeAs[errorBody](t, raw).Error.Code != codeNomadUnavailable {
			t.Errorf("Nomad down = %d %s, want 502 nomad_unavailable", status, raw)
		}
	})
}

// logLineBody is a LogLine, decoded.
type logLineBody struct {
	Copy    string  `json:"copy"`
	Service string  `json:"service"`
	Stream  string  `json:"stream"`
	Text    string  `json:"text"`
	Time    *string `json:"time"`
}

// logsEnv is the app whoami with two copies that wrote to both streams. w1 started before w2.
func logsEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, "")
	seedHealthy(t, e, "whoami", 2, 7)
	at := func(minute int) map[string]*api.TaskState {
		return map[string]*api.TaskState{"web": {StartedAt: time.Date(2026, 10, 4, 9, minute, 0, 0, time.UTC)}}
	}
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "w2", TaskGroup: "web", JobVersion: 7, ClientStatus: "running", TaskStates: at(9)},
		api.AllocationListStub{ID: "w1", TaskGroup: "web", JobVersion: 7, ClientStatus: "running", TaskStates: at(5)})
	e.Nomad.WriteLog("w1", "stdout", "w1 out 1\nw1 out 2\n")
	e.Nomad.WriteLog("w1", "stderr", "w1 err 1\n")
	e.Nomad.WriteLog("w2", "stdout", "w2 out 1\n")
	return e
}

func logTexts(lines []logLineBody) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

// US5: the Logs tab's twin, without follow.
func TestGetLogsJSON(t *testing.T) {
	e := logsEnv(t)
	get := func(t *testing.T, query string) []logLineBody {
		t.Helper()
		status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/logs"+query, "", "")
		if status != http.StatusOK || header.Get("Content-Type") != "application/json" {
			t.Fatalf("GET logs%s = %d %q: %s", query, status, header.Get("Content-Type"), raw)
		}
		checkSchema(t, loadContract(t), raw, "LogLines")
		return decodeAs[struct {
			Lines []logLineBody `json:"lines"`
		}](t, raw).Lines
	}

	t.Run("every copy and both streams, oldest first", func(t *testing.T) {
		lines := get(t, "")
		if got, want := strings.Join(logTexts(lines), "|"), "w1 out 1|w1 out 2|w1 err 1|w2 out 1"; got != want {
			t.Fatalf("lines = %s, want %s", got, want)
		}
		first := lines[0]
		if first.Copy != "w1" || first.Service != "web" || first.Stream != "stdout" || first.Time != nil {
			t.Errorf("first = %+v, want w1's web stdout line with no time", first)
		}
		if lines[2].Stream != "stderr" {
			t.Errorf("third line's stream = %q, want stderr", lines[2].Stream)
		}
	})
	t.Run("filters and the cap", func(t *testing.T) {
		tests := []struct{ query, want string }{
			{"?stream=stdout", "w1 out 1|w1 out 2|w2 out 1"},
			{"?stream=stderr", "w1 err 1"},
			{"?stream=both", "w1 out 1|w1 out 2|w1 err 1|w2 out 1"},
			{"?copy=w2", "w2 out 1"},
			{"?lines=2", "w1 err 1|w2 out 1"},
			{"?copy=w1&stream=stdout&lines=1", "w1 out 2"},
			{"?follow=false", "w1 out 1|w1 out 2|w1 err 1|w2 out 1"},
		}
		for _, tc := range tests {
			if got := strings.Join(logTexts(get(t, tc.query)), "|"); got != tc.want {
				t.Errorf("logs%s = %s, want %s", tc.query, got, tc.want)
			}
		}
	})
	t.Run("no output is an empty list", func(t *testing.T) {
		seedHealthy(t, e, "quiet", 1, 1)
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/quiet/logs", "", "")
		if status != http.StatusOK || decodeAs[map[string][]logLineBody](t, raw)["lines"] == nil {
			t.Errorf("status %d body %s, want 200 and \"lines\": []", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "LogLines")
	})
}

func TestGetLogsErrors(t *testing.T) {
	e := logsEnv(t)
	tests := []struct {
		name, query string
		status      int
		code, field string
	}{
		{"unknown stream", "?stream=both,stdout", 400, codeInvalidField, "stream"},
		{"stream in capitals", "?stream=STDOUT", 400, codeInvalidField, "stream"},
		{"lines zero", "?lines=0", 400, codeInvalidField, "lines"},
		{"lines too many", "?lines=501", 400, codeInvalidField, "lines"},
		{"lines not a number", "?lines=many", 400, codeInvalidField, "lines"},
		{"lines empty", "?lines=", 400, codeInvalidField, "lines"},
		{"follow not a boolean", "?follow=sure", 400, codeInvalidField, "follow"},
		{"a copy of no app of ours", "?copy=nope", 404, codeNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/logs"+tc.query, "", "")
			body := decodeAs[errorBody](t, raw)
			if status != tc.status || body.Error.Code != tc.code || body.Error.Field != tc.field {
				t.Errorf("status %d body %s, want %d %s field %q", status, raw, tc.status, tc.code, tc.field)
			}
			checkSchema(t, loadContract(t), raw, "Error")
		})
		t.Run(tc.name+" with follow is refused before any stream starts", func(t *testing.T) {
			status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/logs"+tc.query+"&follow=true", "", "")
			if tc.query == "?follow=sure" {
				return // the query is the follow value itself
			}
			if status != tc.status || strings.HasPrefix(header.Get("Content-Type"), "text/event-stream") {
				t.Errorf("status %d %q body %s, want a plain %d error", status, header.Get("Content-Type"), raw, tc.status)
			}
		})
	}

	t.Run("an unknown app", func(t *testing.T) {
		for _, q := range []string{"", "?follow=true"} {
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/ghost/logs"+q, "", "")
			if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeNotFound {
				t.Errorf("logs%s of a ghost: status %d body %s", q, status, raw)
			}
		}
	})
	t.Run("Nomad down", func(t *testing.T) {
		e.Nomad.Down()
		defer e.Nomad.Up()
		for _, q := range []string{"", "?follow=true"} {
			status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/logs"+q, "", "")
			if status != http.StatusBadGateway || decodeAs[errorBody](t, raw).Error.Code != codeNomadUnavailable {
				t.Errorf("logs%s: status %d body %s, want 502 nomad_unavailable", q, status, raw)
			}
			if header.Get("Content-Type") != "application/json" {
				t.Errorf("logs%s: Content-Type %q, want JSON", q, header.Get("Content-Type"))
			}
		}
	})
	t.Run("without credentials", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.RawURL+"/v1/apps/whoami/logs", "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("status %d body %s, want 401", status, raw)
		}
	})
}

// sseEvent is one event of a stream, as the client sees it.
type sseEvent struct{ name, data string }

// sseClient reads a stream's events. A comment line becomes an event named ":".
type sseClient struct {
	t      *testing.T
	resp   *http.Response
	events chan sseEvent // closed when the stream ends
	cancel context.CancelFunc
}

// openSSE connects to url with the given request mutator (credentials).
func openSSE(t *testing.T, url string, mutate func(*http.Request)) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", url, err)
	}
	c := &sseClient{t: t, resp: resp, events: make(chan sseEvent, 256), cancel: cancel}
	go func() {
		defer close(c.events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.name != "" {
					c.events <- cur
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, ": "):
				c.events <- sseEvent{name: ":", data: line[2:]}
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
	return c
}

// next returns the next event that isn't a comment; ok is false when the stream ended.
func (c *sseClient) next() (ev sseEvent, ok bool) {
	c.t.Helper()
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				return sseEvent{}, false
			}
			if ev.name == ":" {
				continue
			}
			return ev, true
		case <-time.After(5 * time.Second):
			c.t.Fatal("no event within 5s")
		}
	}
}

// nextLog returns the next log event's line.
func (c *sseClient) nextLog() logLineBody {
	c.t.Helper()
	ev, ok := c.next()
	if !ok || ev.name != "log" {
		c.t.Fatalf("next event = %+v (stream open: %v), want a log event", ev, ok)
	}
	return decodeAs[logLineBody](c.t, []byte(ev.data))
}

func waitLogStreams(t *testing.T, e *testEnv, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.Nomad.OpenLogStreams() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d log streams open, want %d", e.Nomad.OpenLogStreams(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGetLogsFollow(t *testing.T) {
	e := logsEnv(t)
	c := openSSE(t, e.URL+"/v1/apps/whoami/logs?follow=true&stream=stdout", nil)
	if c.resp.StatusCode != http.StatusOK || c.resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response = %d %q, want 200 text/event-stream", c.resp.StatusCode, c.resp.Header.Get("Content-Type"))
	}

	// The tail first, as log events with the LogLine shape and no time.
	var tail []string
	for range 3 {
		l := c.nextLog()
		tail = append(tail, l.Text)
		if l.Time != nil || l.Stream != "stdout" || l.Service != "web" {
			t.Errorf("tail line = %+v, want a stdout line of web with no time", l)
		}
	}
	if got := strings.Join(tail, "|"); got != "w1 out 1|w1 out 2|w2 out 1" {
		t.Errorf("tail = %s", got)
	}

	// Then new lines, stamped.
	waitLogStreams(t, e, 2)
	e.Nomad.WriteLog("w2", "stdout", "w2 out 2\n")
	l := c.nextLog()
	if l.Text != "w2 out 2" || l.Copy != "w2" || l.Time == nil {
		t.Errorf("followed line = %+v, want w2's new line with a time", l)
	}
	checkSchema(t, loadContract(t), mustJSON(t, l), "LogLine")

	// The client leaving ends the reads.
	c.cancel()
	waitLogStreams(t, e, 0)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// logServer serves the API with short timings for the streams, on the same store, service and
// Nomad as e. The credentials come from e's Auth.
func logServer(t *testing.T, e *testEnv, recheck, keepalive time.Duration) string {
	t.Helper()
	s := newServer(Deps{Service: e.Service, Auth: e.Auth, Store: e.Store, Log: e.log})
	s.recheck, s.keepalive = recheck, keepalive
	srv := httptest.NewServer(httpx.LogRequests(e.log, s.handler(s.routes())))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestGetLogsFollowEndsWhenCredentialsEnd(t *testing.T) {
	e := logsEnv(t)
	url := logServer(t, e, 50*time.Millisecond, time.Hour)

	t.Run("a revoked token", func(t *testing.T) {
		made, err := e.Auth.CreateAPIToken(context.Background(), "follower", nil)
		if err != nil {
			t.Fatal(err)
		}
		c := openSSE(t, url+"/v1/apps/whoami/logs?follow=true", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+made.Token)
		})
		c.nextLog() // the stream is up
		if err := e.Auth.RevokeAPIToken(context.Background(), made.Info.ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			ev, ok := c.next()
			if ok && ev.name == "ended" {
				break
			}
			if !ok {
				t.Fatal("the stream closed without an ended event")
			}
			if time.Now().After(deadline) {
				t.Fatal("no ended event within 3s of the revocation")
			}
		}
		if _, ok := c.next(); ok {
			t.Error("the stream stayed open after ended")
		}
		waitLogStreams(t, e, 0)
	})

	t.Run("a session that ends", func(t *testing.T) {
		sess, err := e.Auth.SignIn(context.Background(), testAdminEmail, testAdminPassword, "203.0.113.9")
		if err != nil {
			t.Fatal(err)
		}
		c := openSSE(t, url+"/v1/apps/whoami/logs?follow=true", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "caravana_session", Value: sess.Value})
		})
		c.nextLog()
		if err := e.Auth.SignOut(context.Background(), sess.Value); err != nil {
			t.Fatal(err)
		}
		for {
			ev, ok := c.next()
			if !ok {
				t.Fatal("the stream closed without an ended event")
			}
			if ev.name == "ended" {
				break
			}
		}
	})
}

func TestGetLogsWithASessionDoesNotRenewIt(t *testing.T) {
	e := logsEnv(t)
	url := logServer(t, e, 20*time.Millisecond, time.Hour)
	sess, err := e.Auth.SignIn(context.Background(), testAdminEmail, testAdminPassword, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(sess.Value))
	id := hex.EncodeToString(sum[:])
	before, err := e.Store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "caravana_session", Value: sess.Value}

	// Ten minutes pass, well beyond the once-a-minute touch of a checked session.
	e.Clock.Advance(10 * time.Minute)
	c := openSSE(t, url+"/v1/apps/whoami/logs?follow=true", func(r *http.Request) { r.AddCookie(cookie) })
	c.nextLog()
	time.Sleep(100 * time.Millisecond) // several re-checks
	if status, _ := cookieCall(t, cookie, http.MethodGet, url+"/v1/apps/whoami/logs", "", nil); status != http.StatusOK {
		t.Fatalf("JSON read with the cookie = %d", status)
	}

	after, err := e.Store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LastSeenAt.Equal(before.LastSeenAt) {
		t.Errorf("LastSeenAt moved from %v to %v: reading logs renewed the session", before.LastSeenAt, after.LastSeenAt)
	}
}

func TestGetLogsFollowKeepalive(t *testing.T) {
	e := logsEnv(t)
	url := logServer(t, e, time.Hour, 20*time.Millisecond)
	c := openSSE(t, url+"/v1/apps/whoami/logs?follow=true", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+e.Token) })
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-c.events:
			if ev.name == ":" {
				return
			}
		case <-deadline:
			t.Fatal("no keepalive comment within 3s")
		}
	}
}

func TestGetLogsKeepsLineTextOutOfTheLog(t *testing.T) {
	e := logsEnv(t)
	e.Nomad.WriteLog("w1", "stdout", "password=hunter2-not-for-logs\n")
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/logs", "", "")
	if status != http.StatusOK || !strings.Contains(string(raw), "hunter2") {
		t.Fatalf("status %d body %s, want the line in the response", status, raw)
	}
	c := openSSE(t, e.URL+"/v1/apps/whoami/logs?follow=true", nil)
	c.nextLog()
	c.cancel()
	waitLogStreams(t, e, 0)
	time.Sleep(50 * time.Millisecond)
	if out := e.Logs.String(); strings.Contains(out, "hunter2") {
		t.Errorf("a log line's text reached the server log:\n%s", out)
	}
	if !strings.Contains(e.Logs.String(), "/v1/apps/whoami/logs") {
		t.Errorf("the requests are not in the request log at all:\n%s", e.Logs.String())
	}
}
