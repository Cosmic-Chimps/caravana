package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogRequests(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	h := LogRequests(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusTeapot)
		}
	}))

	get := httptest.NewRequest(http.MethodGet, "/v1/health?token=s3cr3t-q", nil)
	get.Header.Set("Authorization", "Bearer s3cr3t-a")
	get.AddCookie(&http.Cookie{Name: "caravana_session", Value: "s3cr3t-c"})
	h.ServeHTTP(httptest.NewRecorder(), get)
	h.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=s3cr3t-b")))

	if strings.Contains(out.String(), "s3cr3t") {
		t.Errorf("a secret reached the log:\n%s", out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2:\n%s", len(lines), out.String())
	}
	for i, want := range []struct {
		method, path string
		status       float64
	}{{"GET", "/v1/health", 200}, {"POST", "/login", 418}} {
		var e map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &e); err != nil {
			t.Fatalf("line %q is not JSON: %v", lines[i], err)
		}
		if e["msg"] != "request" || e["method"] != want.method || e["path"] != want.path ||
			e["status"] != want.status || e["durationMs"] == nil {
			t.Errorf("line %d = %v, want %+v", i, e, want)
		}
	}
}
