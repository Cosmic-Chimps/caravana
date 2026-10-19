package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

// A websocket upgrade needs the connection of the response writer, which the
// request log's wrapper must hand over, and the log line then says 101.
func TestLogRequestsAllowsUpgrade(t *testing.T) {
	var out syncBuffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	upgrader := websocket.Upgrader{}
	handled := make(chan struct{})
	srv := httptest.NewServer(LogRequests(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handled)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade through LogRequests: %v", err)
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("hello"))
	})))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/ws?token=s3cr3t-q", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, msg, err := conn.ReadMessage(); err != nil || string(msg) != "hello" {
		t.Fatalf("read = %q, %v; want hello", msg, err)
	}
	conn.Close()

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return")
	}
	// The log line comes right after the handler returns.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), `"msg":"request"`) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	var e map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &e); err != nil {
		t.Fatalf("log %q is not one JSON line: %v", out.String(), err)
	}
	if e["status"] != float64(101) || e["path"] != "/v1/ws" {
		t.Errorf("log line = %v, want status 101 on /v1/ws", e)
	}
	if strings.Contains(out.String(), "s3cr3t") {
		t.Errorf("the query reached the log: %s", out.String())
	}
}

// syncBuffer is a bytes.Buffer that can be written by the server's goroutine
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

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
