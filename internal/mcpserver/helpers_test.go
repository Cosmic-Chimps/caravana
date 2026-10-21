package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sentinelToken is the token of every test: if it shows up in an answer, an
// error or a log line, a test fails.
const sentinelToken = "cvat_s3cretSentinel0265"

// recorded is one request the fake API received.
type recorded struct {
	Method string
	Path   string // escaped, as sent
	Query  string
	Header http.Header
	Body   string
}

// fakeAPI is a stand-in for Caravana's API: it answers from a table of
// "METHOD /path" and records every request.
type fakeAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	reqs   []recorded
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), string(body)})
	h := f.routes[r.Method+" "+r.URL.EscapedPath()]
	f.mu.Unlock()
	if h == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"not_found","message":"no such endpoint"}}`)
		return
	}
	h(w, r)
}

// on sets the handler of a route, like "GET /v1/apps/web".
func (f *fakeAPI) on(route string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = h
}

// reply makes a route answer with a status and a body.
func (f *fakeAPI) reply(route string, status int, body string) {
	f.on(route, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func (f *fakeAPI) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// session starts the server (with tuning t and log) and connects a client to
// it over in-memory transports.
func (f *fakeAPI) session(t *testing.T, allowActions bool, opts *mcp.ClientOptions, t2 tuning, log *slog.Logger) *mcp.ClientSession {
	t.Helper()
	cfg := Config{URL: f.srv.URL, Token: sentinelToken, AllowActions: allowActions, Version: "9.9.9"}
	return connect(t, cfg, opts, t2, log)
}

// fastTuning makes wait_for_deployment poll every millisecond and count a
// "second" of its timeout as a millisecond.
var fastTuning = tuning{poll: time.Millisecond, waitUnit: time.Millisecond}

func connect(t *testing.T, cfg Config, opts *mcp.ClientOptions, t2 tuning, log *slog.Logger) *mcp.ClientSession {
	t.Helper()
	srv, err := newServer(cfg, nil, log, t2)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, opts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	return cs
}

// callTool calls a tool and returns its result.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	return cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
}

// text is the tool result's text content.
func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// mustCall calls a tool that must succeed and returns its text.
func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := callTool(t, cs, name, args)
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, text(res))
	}
	return text(res)
}

// mustFail calls a tool that must give a tool error and returns its text.
func mustFail(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := callTool(t, cs, name, args)
	if err != nil {
		t.Fatalf("%s: protocol error, want a tool error: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("%s: succeeded with %s, want a tool error", name, text(res))
	}
	return text(res)
}

// jsonEqual compares two JSON documents by value.
func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("not JSON: %q: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("not JSON: %q: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}

// logBuffer is a bytes.Buffer safe for the server's goroutines to write while
// the test reads.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestLog() (*slog.Logger, *logBuffer) {
	buf := &logBuffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}
