package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

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

	var stderr bytes.Buffer
	exit := make(chan int, 1)
	go func() { exit <- run(ctx, []string{"-listen", addr, "-data-dir", dir}, noEnv, &stderr) }()

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
	code := run(context.Background(), []string{"-data-dir", t.TempDir()}, getenv, &stderr)
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
		exit <- run(ctx, []string{"-listen", addr, "-webhook-listen", hookAddr, "-data-dir", t.TempDir()}, noEnv, &stderr)
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
	code := run(context.Background(), []string{"-listen", addr, "-webhook-listen", addr, "-data-dir", t.TempDir()}, noEnv, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "CARAVANA_WEBHOOK_LISTEN") {
		t.Errorf("exit %d, stderr %s; want 1 naming CARAVANA_WEBHOOK_LISTEN", code, stderr.String())
	}
}
