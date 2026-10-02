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

	"github.com/cosmic-chimps/caravana/internal/app"
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

// newTestServer starts the API on a fresh store and returns its base URL, the
// store, and the captured log output.
func newTestServer(t *testing.T) (string, *store.Store, *syncBuffer) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	logs := newSyncBuffer()
	log := slog.New(slog.NewJSONHandler(logs, nil))
	srv := httptest.NewServer(New(st, log))
	t.Cleanup(func() {
		srv.Close()
		st.Close()
	})
	return srv.URL, st, logs
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
	if got := strings.TrimSpace(string(raw)); got != `{"status":"ok"}` {
		t.Errorf("body = %s, want {\"status\":\"ok\"}", got)
	}

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
