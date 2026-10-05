package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// logRun is one Deployer.Logs call in the background: its lines arrive on
// out, and its result on done.
type logRun struct {
	t      *testing.T
	out    chan LogLine
	done   chan error
	cancel context.CancelFunc
}

func startLogs(t *testing.T, d *Deployer, copyID string, stream LogStream, tailBytes int64, follow bool) *logRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &logRun{t: t, out: make(chan LogLine), done: make(chan error, 1), cancel: cancel}
	go func() { r.done <- d.Logs(ctx, copyID, "web", stream, tailBytes, follow, r.out) }()
	return r
}

// next returns the next line; it fails the test if none comes in time.
func (r *logRun) next() LogLine {
	r.t.Helper()
	select {
	case l := <-r.out:
		return l
	case err := <-r.done:
		r.t.Fatalf("Logs returned (%v) before the next line", err)
	case <-time.After(3 * time.Second):
		r.t.Fatal("no line within 3s")
	}
	return LogLine{}
}

// expectNone fails if a line arrives within a short while.
func (r *logRun) expectNone() {
	r.t.Helper()
	select {
	case l := <-r.out:
		r.t.Fatalf("got line %q, want none yet", l.Text)
	case <-time.After(150 * time.Millisecond):
	}
}

// finish waits for Logs to return and gives back what it returned.
func (r *logRun) finish() error {
	r.t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(3 * time.Second):
		r.t.Fatal("Logs didn't return within 3s")
	}
	return nil
}

// all reads every line until Logs returns.
func (r *logRun) all() ([]string, error) {
	r.t.Helper()
	var texts []string
	for {
		select {
		case l := <-r.out:
			texts = append(texts, l.Text)
		case err := <-r.done:
			return texts, err
		case <-time.After(3 * time.Second):
			r.t.Fatal("Logs didn't finish within 3s")
		}
	}
}

// waitOpen waits until a followed stream is connected, so a write after it is
// surely read as new output.
func waitOpen(t *testing.T, nomad *deploytest.Nomad, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for nomad.OpenLogStreams() != want {
		if time.Now().After(deadline) {
			t.Fatalf("%d log streams open, want %d", nomad.OpenLogStreams(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func logsEnv(t *testing.T) (*Deployer, *deploytest.Nomad) {
	t.Helper()
	d, _, nomad := newTestDeployer(t)
	nomad.SetCopies("blog", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running"})
	return d, nomad
}

func TestLogsReadsLinesFromFrames(t *testing.T) {
	const big = 1 << 16
	tests := []struct {
		name      string
		frameSize int
		written   []string
		tail      int64
		want      []string
	}{
		{"one frame", 0, []string{"one\ntwo\n"}, big, []string{"one", "two"}},
		{"a line split across frames is joined", 3, []string{"hello world\nsecond\n"}, big, []string{"hello world", "second"}},
		{"a character split across frames stays whole", 1, []string{"café über\n"}, big, []string{"café über"}},
		{"invalid UTF-8 is replaced", 0, []string{"bad \xff\xfe bytes\n"}, big, []string{"bad \uFFFD bytes"}},
		{"a carriage return before the newline is dropped", 0, []string{"windows\r\nline\r\n"}, big, []string{"windows", "line"}},
		{"an empty line is a line", 0, []string{"a\n\nb\n"}, big, []string{"a", "", "b"}},
		{"a last line without a newline is still a line at the end of the output", 0, []string{"a\nb"}, big, []string{"a", "b"}},
		{"no output", 0, nil, big, nil},
		{"a 64 KiB line without a newline is cut", 4096, []string{strings.Repeat("x", 64*1024+10) + "\n"}, 1 << 20,
			[]string{strings.Repeat("x", 64*1024), strings.Repeat("x", 10)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, nomad := logsEnv(t)
			nomad.SetLogFrameSize(tc.frameSize)
			for _, w := range tc.written {
				nomad.WriteLog("c1", "stdout", w)
			}
			got, err := startLogs(t, d, "c1", LogStdout, tc.tail, false).all()
			if err != nil {
				t.Fatalf("Logs: %v", err)
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") || len(got) != len(tc.want) {
				t.Errorf("lines = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogsLinesCarryTheirCopyAndStream(t *testing.T) {
	d, nomad := logsEnv(t)
	nomad.WriteLog("c1", "stdout", "out\n")
	nomad.WriteLog("c1", "stderr", "err\n")

	r := startLogs(t, d, "c1", LogStderr, 1000, false)
	if l := r.next(); l != (LogLine{Copy: "c1", Stream: LogStderr, Text: "err"}) {
		t.Errorf("line = %+v, want the stderr line of c1", l)
	}
	if err := r.finish(); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	reqs := nomad.LogRequests()
	if len(reqs) != 1 || reqs[0].Task != "web" || reqs[0].Stream != "stderr" || reqs[0].Origin != "end" || reqs[0].Follow {
		t.Errorf("requests = %+v, want one: task web, stderr, from the end, not following", reqs)
	}
}

func TestLogsTailRespectsTailBytes(t *testing.T) {
	const written = "aaaa\nbbbb\ncccc\n" // three 5-byte lines
	tests := []struct {
		name string
		tail int64
		want []string
	}{
		{"the tail starts on a line start: that line is kept", 10, []string{"bbbb", "cccc"}},
		{"the tail starts inside a line: the cut line is dropped", 8, []string{"cccc"}},
		{"the whole output fits", 15, []string{"aaaa", "bbbb", "cccc"}},
		{"more than the whole output", 1000, []string{"aaaa", "bbbb", "cccc"}},
		{"one byte of the last line", 1, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, nomad := logsEnv(t)
			nomad.WriteLog("c1", "stdout", written)
			got, err := startLogs(t, d, "c1", LogStdout, tc.tail, false).all()
			if err != nil {
				t.Fatalf("Logs: %v", err)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("lines = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogsFollow(t *testing.T) {
	t.Run("delivers new lines after the tail", func(t *testing.T) {
		d, nomad := logsEnv(t)
		nomad.WriteLog("c1", "stdout", "old\n")
		r := startLogs(t, d, "c1", LogStdout, 1000, true)
		if l := r.next(); l.Text != "old" {
			t.Fatalf("first line = %q, want the tail", l.Text)
		}
		r.expectNone()
		nomad.WriteLog("c1", "stdout", "new one\nnew two\n")
		if a, b := r.next(), r.next(); a.Text != "new one" || b.Text != "new two" {
			t.Errorf("new lines = %q, %q", a.Text, b.Text)
		}
	})

	t.Run("a partial last line waits for its newline", func(t *testing.T) {
		d, nomad := logsEnv(t)
		r := startLogs(t, d, "c1", LogStdout, 1000, true)
		nomad.WriteLog("c1", "stdout", "hel")
		r.expectNone()
		nomad.WriteLog("c1", "stdout", "lo\nwor")
		if l := r.next(); l.Text != "hello" {
			t.Fatalf("line = %q, want hello", l.Text)
		}
		r.expectNone() // "wor" is still waiting
		nomad.WriteLog("c1", "stdout", "ld\n")
		if l := r.next(); l.Text != "world" {
			t.Errorf("line = %q, want world", l.Text)
		}
	})

	t.Run("a 64 KiB line without a newline is cut while following", func(t *testing.T) {
		d, nomad := logsEnv(t)
		r := startLogs(t, d, "c1", LogStdout, 0, true)
		waitOpen(t, nomad, 1)
		nomad.WriteLog("c1", "stdout", strings.Repeat("y", 64*1024))
		if l := r.next(); len(l.Text) != 64*1024 {
			t.Errorf("line has %d bytes, want 65536", len(l.Text))
		}
	})

	t.Run("without a tail the first line is whole, not cut at the first newline", func(t *testing.T) {
		d, nomad := logsEnv(t)
		nomad.WriteLog("c1", "stdout", "before\n")
		r := startLogs(t, d, "c1", LogStdout, 0, true)
		waitOpen(t, nomad, 1)
		nomad.WriteLog("c1", "stdout", "after\n")
		if l := r.next(); l.Text != "after" {
			t.Errorf("line = %q, want after (nothing from before the call)", l.Text)
		}
	})

	t.Run("cancelling the context returns, and lets go of the connection", func(t *testing.T) {
		d, nomad := logsEnv(t)
		r := startLogs(t, d, "c1", LogStdout, 1000, true)
		waitOpen(t, nomad, 1)
		r.cancel()
		if err := r.finish(); err != nil {
			t.Errorf("Logs after cancel = %v, want nil: ending is not a failure", err)
		}
		waitOpen(t, nomad, 0) // the connection is let go
	})

	t.Run("a reader that stopped reading doesn't block the cancel", func(t *testing.T) {
		d, nomad := logsEnv(t)
		nomad.SetLogFrameSize(2)
		nomad.WriteLog("c1", "stdout", strings.Repeat("line\n", 200))
		r := startLogs(t, d, "c1", LogStdout, 1<<16, true)
		r.next() // Logs is now blocked sending the second line, with frames piling up behind it
		time.Sleep(50 * time.Millisecond)
		r.cancel()
		if err := r.finish(); err != nil {
			t.Errorf("Logs after cancel = %v, want nil", err)
		}
	})
}

func TestLogsErrors(t *testing.T) {
	t.Run("a copy Nomad doesn't have has no output", func(t *testing.T) {
		d, _ := logsEnv(t)
		got, err := startLogs(t, d, "ghost", LogStdout, 1000, false).all()
		if err != nil || len(got) != 0 {
			t.Errorf("lines %q, error %v; want none and no error", got, err)
		}
	})

	t.Run("Nomad down", func(t *testing.T) {
		d, nomad := logsEnv(t)
		nomad.Down()
		_, err := startLogs(t, d, "c1", LogStdout, 1000, false).all()
		if !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("error = %v, want ErrNomadUnavailable", err)
		}
	})

	t.Run("a token without read-logs", func(t *testing.T) {
		d, nomad := logsEnv(t)
		nomad.RequireToken("only-this-one")
		_, err := startLogs(t, d, "c1", LogStdout, 1000, false).all()
		if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "refused the token") {
			t.Errorf("error = %v, want ErrNomadUnavailable saying the token was refused", err)
		}
	})
}
