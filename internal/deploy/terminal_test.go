package deploy

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// lockedBuffer collects what a terminal prints; Nomad's receiving goroutine
// writes while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls until the buffer holds want.
func (b *lockedBuffer) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(b.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("output %q never had %q", b.String(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOpenTerminal(t *testing.T) {
	ctx := context.Background()

	t.Run("types, resizes and exits with a code", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		in, inW := io.Pipe()
		out := &lockedBuffer{}
		sizes := make(chan TerminalSize, 4)
		sizes <- TerminalSize{Cols: 100, Rows: 30}

		type result struct {
			code int
			err  error
		}
		done := make(chan result, 1)
		go func() {
			code, err := d.OpenTerminal(ctx, id, "web", []string{"/bin/sh"}, in, out, sizes)
			done <- result{code, err}
		}()

		write := func(s string) {
			t.Helper()
			if _, err := inW.Write([]byte(s)); err != nil {
				t.Fatal(err)
			}
		}
		write("echo $((6*7))\r")
		out.waitFor(t, "42\r\n")
		write("stty size\r")
		out.waitFor(t, "30 100\r\n")
		sizes <- TerminalSize{Cols: 132, Rows: 50}
		// Nomad's client sends the resize and the typing from different
		// goroutines, so a line typed at once may still see the old size: ask
		// until the new one shows.
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(out.String(), "50 132\r\n") {
			if time.Now().After(deadline) {
				t.Fatalf("output %q never had the new size", out.String())
			}
			write("stty size\r")
			time.Sleep(20 * time.Millisecond)
		}
		write("exit 3\r")

		select {
		case r := <-done:
			if r.err != nil || r.code != 3 {
				t.Errorf("OpenTerminal = %d, %v; want 3, nil", r.code, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the terminal did not end")
		}
		got := nomad.Execs()
		if len(got) != 1 || !got[0].TTY || got[0].Task != "web" || strings.Join(got[0].Command, "|") != "/bin/sh" {
			t.Errorf("exec requests = %+v; want one with a terminal in the service's task", got)
		}
	})

	t.Run("end of input ends the shell", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		in, inW := io.Pipe()
		time.AfterFunc(50*time.Millisecond, func() { inW.Close() })
		code, err := d.OpenTerminal(ctx, id, "web", []string{"/bin/sh"}, in, &lockedBuffer{}, nil)
		if err != nil || code != 0 {
			t.Errorf("OpenTerminal = %d, %v; want 0, nil", code, err)
		}
	})

	t.Run("a cancelled context is its error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		in, inW := io.Pipe()
		defer inW.Close()
		cctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(100*time.Millisecond, cancel)
		if _, err := d.OpenTerminal(cctx, id, "web", []string{"/bin/sh"}, in, &lockedBuffer{}, nil); !errors.Is(err, context.Canceled) {
			t.Errorf("OpenTerminal = %v, want context.Canceled", err)
		}
	})

	t.Run("a copy that isn't running", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "complete")
		if _, err := d.OpenTerminal(ctx, id, "web", []string{"/bin/sh"}, strings.NewReader(""), &lockedBuffer{}, nil); !errors.Is(err, ErrCopyNotRunning) {
			t.Errorf("OpenTerminal = %v, want ErrCopyNotRunning", err)
		}
		if len(nomad.Execs()) != 0 {
			t.Error("a terminal was opened in a copy that isn't running")
		}
	})

	t.Run("an unknown copy", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if _, err := d.OpenTerminal(ctx, "nope", "web", []string{"/bin/sh"}, strings.NewReader(""), &lockedBuffer{}, nil); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("OpenTerminal = %v, want ErrCopyNotFound", err)
		}
	})

	t.Run("a program Nomad can't start", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Fail: `exec: "/bin/bash": no such file`}
		})
		_, err := d.OpenTerminal(ctx, id, "web", []string{"/bin/bash"}, strings.NewReader(""), &lockedBuffer{}, nil)
		if !errors.Is(err, ErrExecFailed) {
			t.Errorf("OpenTerminal = %v, want ErrExecFailed", err)
		}
	})

	t.Run("no permission to exec is the capability error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.FailExec(403)
		_, err := d.OpenTerminal(ctx, id, "web", []string{"/bin/sh"}, strings.NewReader(""), &lockedBuffer{}, nil)
		if err == nil || errors.Is(err, ErrExecFailed) || !strings.Contains(err.Error(), "alloc-exec") {
			t.Errorf("OpenTerminal = %v, want the alloc-exec capability error", err)
		}
	})
}
