package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// syncBuffer is a bytes.Buffer safe for the server's goroutines to write and
// the test to read.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// whoamiDigest is the digest part of apptest.WhoamiDigest.
const whoamiDigest = "sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"

// fakeClock is the clock the test Auth uses; tests move it by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const (
	testAdminEmail    = "admin@example.com"
	testAdminPassword = "correct horse battery"
)

// testEnv is the API wired to a real store and service, with a fake
// registry and a fake Nomad behind it.
//
// The API is served twice. URL has a tiny wrapper that adds the admin's API
// token to requests that carry no Authorization header, so tests of apps and
// deployments call http.Get(e.URL+...) unchanged. RawURL has no wrapper,
// for tests of authentication itself.
type testEnv struct {
	URL      string
	RawURL   string
	Store    *store.Store
	Logs     *syncBuffer
	Registry *registrytest.Server
	Nomad    *deploytest.Nomad
	Service  *service.Service
	Auth     *service.Auth
	Clock    *fakeClock
	Token    string // an active API token of the admin
	log      *slog.Logger
}

// newTestEnv starts the API on a fresh store. The fake registry knows
// traefik/whoami:latest and the fake Nomad's rollouts succeed. nomadToken is
// the token Caravana sends to Nomad.
func newTestEnv(t *testing.T, nomadToken string) *testEnv {
	t.Helper()
	return newTestEnvWith(t, nomadToken, nil)
}

// newTestEnvWith is newTestEnv with setup called on the service before it
// starts, for the settings that must be made then (a watcher's gap).
func newTestEnvWith(t *testing.T, nomadToken string, setup func(*service.Service)) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	e := &testEnv{Store: st, Logs: newSyncBuffer(), Registry: registrytest.New(t), Nomad: deploytest.New(t)}
	e.Registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	e.Nomad.Succeed()

	log := slog.New(slog.NewJSONHandler(e.Logs, nil))
	client, err := deploy.NewNomadClient(e.Nomad.Addr(), nomadToken)
	if err != nil {
		t.Fatalf("nomad client: %v", err)
	}
	d := deploy.New(client, &registry.Client{Endpoints: e.Registry.Endpoints()},
		nomadjob.Settings{Entrypoint: "websecure", DefaultCPUMHz: 100, DefaultMemoryMB: 64, DefaultMemoryMaxMB: 256})
	d.Timing = deploy.Timing{PollGap: time.Millisecond, BackoffStart: time.Millisecond, BackoffMax: 5 * time.Millisecond,
		VersionRetries: []time.Duration{time.Millisecond}}
	svc := service.New(st, d, deploy.NewVariables(client), log)
	e.Service, e.log = svc, log

	e.Clock = &fakeClock{t: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)}
	e.Auth = service.NewAuth(st, log, service.AuthOptions{Now: e.Clock.Now, PasswordIterations: 1000})
	if err := e.Auth.InitAdmin(context.Background(), testAdminEmail, testAdminPassword); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	created, err := e.Auth.CreateAPIToken(context.Background(), "tests", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	e.Token = created.Token
	// Domains (feature 023): no watcher pass, and checks that never leave the
	// process (a resolver that finds nothing, a dialer that is refused). Tests
	// of the checks give their own fakes with SetEdge.
	svc.SetDomainWatchGaps(time.Hour, time.Hour)
	svc.SetEdge(edgetest.FakeResolver{}, &edgetest.FakeDialer{})
	if setup != nil {
		setup(svc)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("start service: %v", err)
	}

	// As in production, the request log wraps the API from outside.
	api := httpx.LogRequests(log, New(Deps{Service: svc, Auth: e.Auth, Store: st, Log: log, Version: "1.2.3-test"}))
	raw := httptest.NewServer(api)
	withToken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Webhook calls carry their own token, in the query or the header.
		if r.Header.Get("Authorization") == "" && !strings.HasPrefix(r.URL.Path, "/v1/webhooks/") {
			r.Header.Set("Authorization", "Bearer "+e.Token)
		}
		api.ServeHTTP(w, r)
	}))
	e.RawURL, e.URL = raw.URL, withToken.URL
	t.Cleanup(func() {
		raw.Close()
		withToken.Close()
		cancel()
		svc.Wait()
		st.Close()
	})
	return e
}

// newTestServer starts the API on a fresh store and returns its base URL, the
// store, and the captured log output.
func newTestServer(t *testing.T) (string, *store.Store, *syncBuffer) {
	t.Helper()
	e := newTestEnv(t, "")
	return e.URL, e.Store, e.Logs
}

// errorBody is the standard error response, decoded.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Field   string `json:"field"`
	} `json:"error"`
}

// readError checks that resp is a JSON error with the given status and
// returns its body.
func readError(t *testing.T, resp *http.Response, status int) errorBody {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, status, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body errorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body %q: %v", raw, err)
	}
	if body.Error.Code == "" || body.Error.Message == "" {
		t.Errorf("error body %s lacks code or message", raw)
	}
	return body
}

func TestHealth(t *testing.T) {
	url, st, _ := newTestServer(t)

	resp, err := http.Get(url + "/v1/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := strings.TrimSpace(string(raw)); got != `{"status":"ok","version":"1.2.3-test"}` {
		t.Errorf("body = %s, want status ok and the version", got)
	}
	checkSchema(t, loadContract(t), raw, "Health")

	st.Close()
	resp, err = http.Get(url + "/v1/health")
	if err != nil {
		t.Fatalf("GET after close: %v", err)
	}
	if body := readError(t, resp, http.StatusServiceUnavailable); body.Error.Code != "unavailable" {
		t.Errorf("code = %q, want unavailable", body.Error.Code)
	}
}

// noRedirects is a client that fails loudly instead of following a redirect.
var noRedirects = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func TestNotFound(t *testing.T) {
	url, _, _ := newTestServer(t)
	resp, err := http.Get(url + "/v1/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if body := readError(t, resp, http.StatusNotFound); body.Error.Code != codeNotFound {
		t.Errorf("code = %q, want %q", body.Error.Code, codeNotFound)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	url, _, _ := newTestServer(t)
	resp, err := http.Post(url+"/v1/health", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to include GET", allow)
	}
	if body := readError(t, resp, http.StatusMethodNotAllowed); body.Error.Code != codeMethodNotAllowed {
		t.Errorf("code = %q, want %q", body.Error.Code, codeMethodNotAllowed)
	}
}

func TestNoRedirect(t *testing.T) {
	url, _, _ := newTestServer(t)
	for _, path := range []string{"//v1/health", "/v1/health/", "/v1/../v1/health", "/v1/./health"} {
		t.Run(path, func(t *testing.T) {
			resp, err := noRedirects.Get(url + path)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			if loc := resp.Header.Get("Location"); loc != "" {
				t.Errorf("Location = %q, want no redirect", loc)
			}
			if body := readError(t, resp, http.StatusNotFound); body.Error.Code != codeNotFound {
				t.Errorf("code = %q, want %q", body.Error.Code, codeNotFound)
			}
		})
	}
}

func TestErrorMapping(t *testing.T) {
	logs := newSyncBuffer()
	s := &server{log: slog.New(slog.NewJSONHandler(logs, nil))}

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{"field error", &app.FieldError{Field: "services[0].domains[1].port", Message: `"nope" is not a port of this service`},
			http.StatusBadRequest, codeInvalidField, "services[0].domains[1].port"},
		{"not found", fmt.Errorf("get app %q: %w", "shop", store.ErrNotFound), http.StatusNotFound, codeNotFound, ""},
		{"api error", &Error{Status: http.StatusTeapot, Code: "teapot", Message: "short and stout"}, http.StatusTeapot, "teapot", ""},
		{"unexpected", errors.New("boom: secret internals"), http.StatusInternalServerError, codeInternal, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.handle(func(http.ResponseWriter, *http.Request) error { return tc.err })(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

			body := readError(t, rec.Result(), tc.wantStatus)
			if body.Error.Code != tc.wantCode || body.Error.Field != tc.wantField {
				t.Errorf("code %q field %q, want %q %q", body.Error.Code, body.Error.Field, tc.wantCode, tc.wantField)
			}
			if tc.wantStatus == http.StatusInternalServerError {
				if strings.Contains(rec.Body.String(), "boom") {
					t.Errorf("500 body leaks the internal error: %s", rec.Body.String())
				}
				if !strings.Contains(logs.String(), "boom") {
					t.Errorf("internal error not logged; log:\n%s", logs.String())
				}
			}
		})
	}
}

func TestPanic(t *testing.T) {
	logs := newSyncBuffer()
	s := &server{log: slog.New(slog.NewJSONHandler(logs, nil))}
	h := s.handle(func(http.ResponseWriter, *http.Request) error { panic("kaboom") })

	for range 2 { // the second call proves the handler keeps working
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if body := readError(t, rec.Result(), http.StatusInternalServerError); body.Error.Code != codeInternal {
			t.Errorf("code = %q, want %q", body.Error.Code, codeInternal)
		}
	}
	if !strings.Contains(logs.String(), "kaboom") {
		t.Errorf("panic not logged; log:\n%s", logs.String())
	}
}

func TestRequestLog(t *testing.T) {
	url, _, logs := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, url+"/v1/health?token=s3cr3t-q", nil)
	req.Header.Set("Authorization", "Bearer s3cr3t-a")
	req.Header.Set("X-Api-Token", "s3cr3t-h")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	resp, err = http.Post(url+"/v1/health", "application/json", strings.NewReader(`{"password":"s3cr3t-b"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	out := logs.String()
	if strings.Contains(out, "s3cr3t") {
		t.Errorf("a secret reached the log:\n%s", out)
	}

	var requests []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if entry["msg"] == "request" {
			requests = append(requests, entry)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("got %d request log lines, want 2; log:\n%s", len(requests), out)
	}
	for i, wantStatus := range []float64{200, 405} {
		e := requests[i]
		if e["path"] != "/v1/health" || e["status"] != wantStatus || e["method"] == nil || e["durationMs"] == nil {
			t.Errorf("request line %d = %v, want path /v1/health, status %v, method and durationMs", i, e, wantStatus)
		}
	}
}
