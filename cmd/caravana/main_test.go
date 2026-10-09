package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/httpx"
)

func noEnv(string) string { return "" }

// syncBuffer is a bytes.Buffer safe for the server's goroutines to write while
// the test reads.
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

// runServer is run with no input and its output thrown away: the server
// tests only look at stderr (its log) and the exit code.
func runServer(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	return run(ctx, args, getenv, strings.NewReader(""), io.Discard, stderr)
}

// freeAddr returns a loopback address with a port nothing is using right now.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestRun(t *testing.T) {
	addr := freeAddr(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stderr syncBuffer
	exit := make(chan int, 1)
	go func() { exit <- runServer(ctx, []string{"-listen", addr, "-data-dir", dir}, noEnv, &stderr) }()

	// SC-001: healthy within 2 seconds.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("not healthy after 2s (last error %v); stderr:\n%s", err, stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The API is closed: health is public, everything else needs a token.
	resp, err := http.Get("http://" + addr + "/v1/apps/x")
	if err != nil {
		t.Fatalf("GET /v1/apps/x: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /v1/apps/x without a token = %d, want 401", resp.StatusCode)
	}
	// The pages are on the same listener: /login answers (with no admin yet,
	// it shows the host command), and "/" goes to Apps.
	resp, err = http.Get("http://" + addr + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	loginPage, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(loginPage), "caravana admin init") {
		t.Errorf("GET /login = %d, want 200 with the host command; body:\n%s", resp.StatusCode, loginPage)
	}
	// The API still never redirects, even for paths a mux would clean.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"//v1/health", "/v1/../v1/health", "/v1/health/"} {
		resp, err := noRedirect.Get("http://" + addr + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("GET %s = %d %q, want a JSON 404 (no redirect)", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	if !strings.Contains(stderr.String(), "caravana admin init") {
		t.Errorf("the log does not tell the owner to run caravana admin init:\n%s", stderr.String())
	}

	cancel()
	select {
	case code := <-exit:
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	if _, err := os.Stat(filepath.Join(dir, "caravana.db")); err != nil {
		t.Errorf("store file: %v", err)
	}
}

func TestRunBadConfig(t *testing.T) {
	var stderr bytes.Buffer
	getenv := func(k string) string {
		if k == "CARAVANA_DEFAULT_MEMORY_MB" {
			return "-5"
		}
		return ""
	}
	code := runServer(context.Background(), []string{"-data-dir", t.TempDir()}, getenv, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "CARAVANA_DEFAULT_MEMORY_MB") {
		t.Errorf("stderr does not name the setting:\n%s", stderr.String())
	}
}

// TestRunWebhookListener starts both listeners: the main one answers health,
// the webhook-only one answers 404 for it. Both stop on cancel.
func TestRunWebhookListener(t *testing.T) {
	addr, hookAddr := freeAddr(t), freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stderr bytes.Buffer
	exit := make(chan int, 1)
	go func() {
		exit <- runServer(ctx, []string{"-listen", addr, "-webhook-listen", hookAddr, "-data-dir", t.TempDir()}, noEnv, &stderr)
	}()

	status := func(url string) int {
		resp, err := http.Get(url)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	deadline := time.Now().Add(2 * time.Second)
	for status("http://"+addr+"/v1/health") != http.StatusOK || status("http://"+hookAddr+"/v1/health") == 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("listeners not up after 2s; stderr:\n%s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := status("http://" + hookAddr + "/v1/health"); got != http.StatusNotFound {
		t.Errorf("health on the webhook listener = %d, want 404", got)
	}
	// The webhook-only listener serves no pages and no styles (FR-026): JSON 404s.
	for _, p := range []string{"/login", "/settings", "/static/app.css", "/"} {
		resp, err := http.Get("http://" + hookAddr + p)
		if err != nil {
			t.Fatalf("GET %s on the webhook listener: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("GET %s on the webhook listener = %d %q, want a JSON 404", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	if got := status("http://" + addr + "/login"); got != http.StatusOK {
		t.Errorf("/login on the main listener = %d, want 200", got)
	}

	cancel()
	select {
	case code := <-exit:
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	if status("http://"+hookAddr+"/v1/health") != 0 {
		t.Error("the webhook listener still answers after shutdown")
	}
}

func TestRunWebhookListenerSameAsMain(t *testing.T) {
	var stderr bytes.Buffer
	addr := freeAddr(t)
	code := runServer(context.Background(), []string{"-listen", addr, "-webhook-listen", addr, "-data-dir", t.TempDir()}, noEnv, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "CARAVANA_WEBHOOK_LISTEN") {
		t.Errorf("exit %d, stderr %s; want 1 naming CARAVANA_WEBHOOK_LISTEN", code, stderr.String())
	}
}

// An open event stream must not make shutdown wait for the full timeout:
// http.Server.Shutdown doesn't interrupt active requests, so the servers'
// request contexts come from one context that shutdownAll cancels first
// (research R12). The handler here is what every /live route does: stream
// until the request's context ends.
func TestShutdownEndsOpenStreams(t *testing.T) {
	streamCtx, endStreams := context.WithCancel(context.Background())
	defer endStreams()
	addr := freeAddr(t)
	srv := newServer(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := httpx.Stream(w); err != nil {
			return
		}
		<-r.Context().Done()
	}), streamCtx)
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()

	var resp *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for {
		var err error
		if resp, err = http.Get("http://" + addr + "/live"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /live: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("GET /live = %d %q, want an open event stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	start := time.Now()
	if err := shutdownAll(endStreams, []*http.Server{srv}); err != nil {
		t.Fatalf("shutdownAll: %v (the stream held it for %v)", err, time.Since(start))
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("shutdown took %v with a stream open, want well under the %v timeout", took, shutdownTimeout)
	}
	if err := <-served; err != http.ErrServerClosed {
		t.Errorf("ListenAndServe = %v, want ErrServerClosed", err)
	}
	// The client sees the stream end rather than hang.
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Logf("reading the ended stream: %v", err) // a reset is as good as a clean close
	}
}

// Feature 012: `caravana version` prints the build's version, which the
// installer compares before an upgrade.
func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"version"}, func(string) string { return "" }, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || strings.TrimSpace(stdout.String()) != version {
		t.Errorf("version = %d %q, want 0 and %q", code, stdout.String(), version)
	}
}
