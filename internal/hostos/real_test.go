package hostos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePro puts a program called pro first on PATH that runs script.
func fakePro(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pro"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunProMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := RunPro(context.Background(), t.TempDir()); !errors.Is(err, ErrProUnavailable) {
		t.Errorf("err = %v, want ErrProUnavailable", err)
	}
}

func TestRunPro(t *testing.T) {
	t.Run("its output, with HOME set", func(t *testing.T) {
		home := t.TempDir()
		fakePro(t, `[ "$1 $2 $3" = "security-status --format json" ] || exit 9
printf '{"home":"%s"}' "$HOME"`)
		out, err := RunPro(context.Background(), home)
		if err != nil || string(out) != `{"home":"`+home+`"}` {
			t.Errorf("out = %q, err = %v", out, err)
		}
	})
	t.Run("a failure never carries its words", func(t *testing.T) {
		fakePro(t, "echo SENTINEL-OUT; echo SENTINEL-ERR >&2; exit 3")
		_, err := RunPro(context.Background(), t.TempDir())
		if err == nil || strings.Contains(err.Error(), "SENTINEL") || !strings.Contains(err.Error(), "code 3") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a cancelled run", func(t *testing.T) {
		fakePro(t, "sleep 30")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := RunPro(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want Canceled", err)
		}
	})
	t.Run("too much output is cut off", func(t *testing.T) {
		fakePro(t, "yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		_, err := RunPro(context.Background(), t.TempDir())
		if err == nil {
			t.Error("an endless answer succeeded")
		}
	})
}
