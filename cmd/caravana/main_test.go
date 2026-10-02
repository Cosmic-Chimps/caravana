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
