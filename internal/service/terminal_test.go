package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// termSentinel is what the sentinel test types, and what the fake shell prints
// back as it echoes it.
const termSentinel = "s3cret-sentinel-0206"

// screen collects what a session prints, as a user would see it.
type screen struct {
	mu  sync.Mutex
	buf strings.Builder
	// read is closed once the watcher has taken everything the session printed.
	read chan struct{}
}

// watch reads the session's output into a screen until the session is done.
func watch(sess *TerminalSession) *screen {
	sc := &screen{read: make(chan struct{})}
	go func() {
		defer close(sc.read)
		for {
			select {
			case b := <-sess.Output():
				sc.mu.Lock()
				sc.buf.Write(b)
				sc.mu.Unlock()
			case <-sess.Done():
				for {
					select {
					case b := <-sess.Output():
						sc.mu.Lock()
						sc.buf.Write(b)
						sc.mu.Unlock()
					default:
						return
					}
				}
			}
		}
	}()
	return sc
}

func (sc *screen) String() string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.buf.String()
}

// final is the whole screen of a session that has ended. When Done closes, the
// watcher may still be copying the last pieces of output (the fake shell sends
// one per typed character), so final waits for it to finish first.
func (sc *screen) final(t *testing.T) string {
	t.Helper()
	select {
	case <-sc.read:
	case <-time.After(5 * time.Second):
		t.Fatal("the screen's watcher did not finish")
	}
	return sc.String()
}

func (sc *screen) waitFor(t *testing.T, want string) {
	t.Helper()
	waitFor(t, fmt.Sprintf("the screen to show %q", want), func() bool { return strings.Contains(sc.String(), want) })
}

func openTerm(t *testing.T, l *actionLab, copyID, shell string) *TerminalSession {
	t.Helper()
	sess, err := l.svc.OpenTerminal(context.Background(), "whoami", copyID, shell, TerminalSize{Cols: 80, Rows: 24}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	t.Cleanup(func() { sess.Close(EndClosed) })
	return sess
}

func waitDone(t *testing.T, sess *TerminalSession) TerminalEnd {
	t.Helper()
	select {
	case <-sess.Done():
		return sess.End()
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
		return TerminalEnd{}
	}
}

func terminalRecords(t *testing.T, l *actionLab) []store.ActionRecord {
	t.Helper()
	acts, err := l.svc.ListActions(context.Background(), "whoami", 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.ActionRecord
	for _, a := range acts {
		if a.Action == store.ActionTerminal {
			out = append(out, a)
		}
	}
	return out
}

func TestShellCommand(t *testing.T) {
	tests := []struct {
		shell string
		first string
		ok    bool
	}{
		{ShellAuto, "/bin/sh", true},
		{ShellBash, "/bin/bash", true},
		{ShellSh, "/bin/sh", true},
		{"zsh", "", false},
		{"", "", false},
		{"/bin/bash", "", false},
	}
	for _, tt := range tests {
		got, err := shellCommand(tt.shell)
		var fe *app.FieldError
		switch {
		case tt.ok && (err != nil || got[0] != tt.first):
			t.Errorf("shellCommand(%q) = %q, %v; want %s", tt.shell, got, err, tt.first)
		case !tt.ok && (!errors.As(err, &fe) || fe.Field != "shell"):
			t.Errorf("shellCommand(%q) = %v, want a field error on shell", tt.shell, err)
		}
	}
	auto, _ := shellCommand(ShellAuto)
	if len(auto) != 3 || auto[1] != "-c" || !strings.Contains(auto[2], "exec bash") || !strings.Contains(auto[2], "exec sh") {
		t.Errorf("auto = %q", auto)
	}
}

func TestOpenTerminal(t *testing.T) {
	ctx := context.Background()

	t.Run("a session, its output, its exit code and its record", func(t *testing.T) {
		l := newActionLab(t)
		changes, stop := l.svc.Subscribe("whoami")
		defer stop()
		sess := openTerm(t, l, "c1", ShellAuto)
		sc := watch(sess)

		recs := terminalRecords(t, l)
		if len(recs) != 1 || recs[0].Status != store.ActionRunning || recs[0].Program != "auto" ||
			recs[0].CopyID != "c1" || recs[0].Service != "web" || recs[0].Trigger != "console" {
			t.Fatalf("record while open = %+v", recs)
		}
		if _, err := sess.Write([]byte("echo $((6*7))\r")); err != nil {
			t.Fatal(err)
		}
		sc.waitFor(t, "42\r\n")
		if got := l.nomad.Execs(); len(got) != 1 || !got[0].TTY || got[0].Task != "web" || got[0].Command[0] != "/bin/sh" {
			t.Errorf("exec = %+v; want a terminal in the service's task", got)
		}
		sess.Resize(120, 40)
		waitFor(t, "the new size", func() bool {
			_, _ = sess.Write([]byte("stty size\r"))
			time.Sleep(10 * time.Millisecond)
			return strings.Contains(sc.String(), "40 120\r\n")
		})
		if _, err := sess.Write([]byte("exit 3\r")); err != nil {
			t.Fatal(err)
		}
		end := waitDone(t, sess)
		if end.Code == nil || *end.Code != 3 || end.Reason != "" {
			t.Errorf("end = %+v, want exit code 3", end)
		}

		recs = terminalRecords(t, l)
		if len(recs) != 1 || recs[0].Status != store.ActionSucceeded || recs[0].ExitCode == nil || *recs[0].ExitCode != 3 ||
			recs[0].Error != "" || recs[0].FinishedAt == nil {
			t.Errorf("record after = %+v", recs)
		}
		if n := drainNotices(changes); !strings.Contains(n, "actions") && !strings.Contains(n, "Actions") {
			t.Errorf("notices = %q, want an actions change", n)
		}
		if _, err := sess.Write([]byte("x")); err == nil {
			t.Error("a write after the end succeeded")
		}
	})

	t.Run("refusals", func(t *testing.T) {
		l := newActionLab(t)
		open := func(appID, copyID, shell string, size TerminalSize) error {
			sess, err := l.svc.OpenTerminal(ctx, appID, copyID, shell, size, app.TriggerManual)
			if err == nil {
				t.Cleanup(func() { sess.Close(EndClosed) })
			}
			return err
		}
		ok := TerminalSize{Cols: 80, Rows: 24}
		var fe *app.FieldError
		if err := open("whoami", "c1", "zsh", ok); !errors.As(err, &fe) || fe.Field != "shell" {
			t.Errorf("a bad shell = %v, want a field error on shell", err)
		}
		for _, size := range []TerminalSize{{Cols: 0, Rows: 24}, {Cols: 80, Rows: 0}, {Cols: 1001, Rows: 24}, {Cols: 80, Rows: 1001}, {Cols: -1, Rows: 24}} {
			if err := open("whoami", "c1", "auto", size); !errors.As(err, &fe) || (fe.Field != "cols" && fe.Field != "rows") {
				t.Errorf("size %+v = %v, want a field error on cols or rows", size, err)
			}
		}
		if err := open("whoami", "c1", "auto", TerminalSize{Cols: 1000, Rows: 1}); err != nil {
			t.Errorf("the largest and smallest sizes = %v, want ok", err)
		}
		// The session dials Nomad from its own goroutine: wait for that one exec
		// now, before the steps below (a Stop) could end the copy under it.
		waitFor(t, "the largest-size terminal's exec", func() bool { return len(l.nomad.Execs()) >= 1 })
		if err := open("ghost", "c1", "auto", ok); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an unknown app = %v, want store.ErrNotFound", err)
		}
		if err := open("whoami", "other", "auto", ok); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a copy that isn't the app's = %v, want store.ErrNotFound", err)
		}
		l.nomad.SetCopies("whoami",
			api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "complete", JobVersion: 0},
			api.AllocationListStub{ID: "c2", TaskGroup: "web", ClientStatus: "running", DesiredStatus: "stop", JobVersion: 0})
		for _, id := range []string{"c1", "c2"} {
			if err := open("whoami", id, "auto", ok); !errors.Is(err, ErrCopyNotRunning) {
				t.Errorf("copy %s = %v, want ErrCopyNotRunning", id, err)
			}
		}
		l.copies()
		if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		if err := open("whoami", "c1", "auto", ok); !errors.Is(err, ErrAppStopped) {
			t.Errorf("a stopped app = %v, want ErrAppStopped", err)
		}
		// No refusal added an exec to the one waited for above.
		if got := len(l.nomad.Execs()); got != 1 {
			t.Errorf("exec requests = %d; only the one of the largest-size check should have opened a terminal", got)
		}
	})

	t.Run("at most the limit open at once, a freed place is reusable", func(t *testing.T) {
		l := newActionLab(t)
		l.svc.SetTerminalLimits(2, 0, 0)
		a := openTerm(t, l, "c1", ShellAuto)
		openTerm(t, l, "c2", ShellSh)
		_, err := l.svc.OpenTerminal(ctx, "whoami", "c1", ShellAuto, TerminalSize{Cols: 80, Rows: 24}, app.TriggerManual)
		if !errors.Is(err, ErrTooManyTerminals) {
			t.Fatalf("the third = %v, want ErrTooManyTerminals", err)
		}
		if n := len(terminalRecords(t, l)); n != 2 {
			t.Errorf("records = %d, want 2: a refused session leaves none", n)
		}
		a.Close(EndClosed)
		waitDone(t, a)
		waitFor(t, "a free place", func() bool {
			sess, err := l.svc.OpenTerminal(ctx, "whoami", "c1", ShellAuto, TerminalSize{Cols: 80, Rows: 24}, app.TriggerManual)
			if err == nil {
				t.Cleanup(func() { sess.Close(EndClosed) })
			}
			return err == nil
		})
	})

	t.Run("idle and the time limit", func(t *testing.T) {
		l := newActionLab(t)
		l.svc.SetTerminalLimits(0, 150*time.Millisecond, 0)
		sess := openTerm(t, l, "c1", ShellAuto)
		if end := waitDone(t, sess); end.Reason != EndIdle || end.Code != nil || end.Message != "idle for 150ms" {
			t.Errorf("idle end = %+v", end)
		}
		if recs := terminalRecords(t, l); len(recs) != 1 || recs[0].Status != store.ActionFailed ||
			recs[0].Error != "idle for 150ms" || recs[0].ExitCode != nil {
			t.Errorf("idle record = %+v", recs)
		}

		// Typing keeps a session from going idle; the limit still ends it.
		l2 := newActionLab(t)
		l2.svc.SetTerminalLimits(0, 400*time.Millisecond, 600*time.Millisecond)
		sess = openTerm(t, l2, "c1", ShellAuto)
		stopTyping := make(chan struct{})
		go func() {
			for {
				select {
				case <-stopTyping:
					return
				case <-time.After(100 * time.Millisecond):
					_, _ = sess.Write([]byte("x"))
				}
			}
		}()
		end := waitDone(t, sess)
		close(stopTyping)
		if end.Reason != EndLimit || end.Message != "open for 600ms" {
			t.Errorf("limit end = %+v", end)
		}
		if recs := terminalRecords(t, l2); len(recs) != 1 || recs[0].Status != store.ActionFailed || recs[0].Error != "open for 600ms" {
			t.Errorf("limit record = %+v", recs)
		}
	})

	t.Run("the words of a default time", func(t *testing.T) {
		for d, want := range map[time.Duration]string{
			15 * time.Minute: "15 minutes", 60 * time.Minute: "60 minutes", time.Minute: "1 minute", 90 * time.Second: "1m30s",
		} {
			if got := describeDuration(d); got != want {
				t.Errorf("describeDuration(%s) = %q, want %q", d, got, want)
			}
		}
	})

	t.Run("the caller closes it or the connection is lost", func(t *testing.T) {
		l := newActionLab(t)
		closed := openTerm(t, l, "c1", ShellBash)
		gone := openTerm(t, l, "c2", ShellSh)
		closed.Close(EndClosed)
		gone.Close(EndGone)
		if end := waitDone(t, closed); end.Reason != EndClosed {
			t.Errorf("closed end = %+v", end)
		}
		if end := waitDone(t, gone); end.Reason != EndGone {
			t.Errorf("gone end = %+v", end)
		}
		byProgram := map[string]store.ActionRecord{}
		for _, r := range terminalRecords(t, l) {
			byProgram[r.Program] = r
		}
		if r := byProgram["bash"]; r.Status != store.ActionSucceeded || r.Error != "closed" {
			t.Errorf("closed record = %+v", r)
		}
		if r := byProgram["sh"]; r.Status != store.ActionFailed || r.Error != "connection lost" {
			t.Errorf("gone record = %+v", r)
		}
	})

	t.Run("a shell the image doesn't have", func(t *testing.T) {
		l := newActionLab(t)
		l.nomad.OnExec(func(r deploytest.ExecRequest) deploytest.ExecReply {
			if r.Command[0] == "/bin/bash" {
				return deploytest.ExecReply{Fail: `exec: "/bin/bash": stat /bin/bash: no such file`}
			}
			return deploytest.ExecReply{ExitCode: 127}
		})
		bash := openTerm(t, l, "c1", ShellBash)
		if end := waitDone(t, bash); end.Reason != EndError || end.Message != "no shell found in this image" {
			t.Errorf("a missing program = %+v", end)
		}
		sh := openTerm(t, l, "c2", ShellSh)
		end := waitDone(t, sh)
		if end.Code == nil || *end.Code != 127 || end.Message != "no shell found in this image" {
			t.Errorf("exit 127 before typing = %+v", end)
		}
		for _, r := range terminalRecords(t, l) {
			if r.Status != store.ActionFailed || r.Error != "no shell found in this image" {
				t.Errorf("record = %+v", r)
			}
		}
	})

	t.Run("a copy that is no longer there ends the session with a fixed message", func(t *testing.T) {
		l := newActionLab(t)
		l.nomad.FailExec(403)
		sess := openTerm(t, l, "c1", ShellAuto)
		end := waitDone(t, sess)
		if end.Reason != EndError || end.Message == "" || strings.Contains(end.Message, "Permission") {
			t.Errorf("end = %+v, want a fixed message of ours", end)
		}
	})
}

// What was typed and what was printed are in no database row or file, no log
// line, no record, no error and no notice (feature 020.6).
func TestTerminalKeepsNoInputOrOutput(t *testing.T) {
	ctx := context.Background()
	l, logs := newLoggedActionLab(t)
	changes, stop := l.svc.Subscribe("")
	defer stop()

	// One session typed into and left idle, one that is typed into and ends
	// with the shell's exit, one the caller closes: every way a session ends.
	l.svc.SetTerminalLimits(0, 400*time.Millisecond, 0)
	idle := openTerm(t, l, "c1", ShellAuto)
	idleScreen := watch(idle)
	if _, err := idle.Write([]byte("echo " + termSentinel + "\r")); err != nil {
		t.Fatal(err)
	}
	idleScreen.waitFor(t, termSentinel+"\r\n")
	waitDone(t, idle)

	l.svc.SetTerminalLimits(0, time.Hour, 0)
	exits := openTerm(t, l, "c1", ShellBash)
	exitScreen := watch(exits)
	if _, err := exits.Write([]byte(termSentinel + "\rexit 4\r")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, exits)
	if got := exitScreen.final(t); !strings.Contains(got, termSentinel) {
		t.Fatalf("the screen = %q; the fake shell should have echoed the typing", got)
	}

	closer := openTerm(t, l, "c2", ShellSh)
	if _, err := closer.Write([]byte(termSentinel)); err != nil {
		t.Fatal(err)
	}
	closer.Close(EndGone)
	waitDone(t, closer)

	// A failure to open, with the sentinel in nothing it could report.
	l.nomad.FailExec(403)
	failing := openTerm(t, l, "c1", ShellAuto)
	_, _ = failing.Write([]byte(termSentinel))
	waitDone(t, failing)
	l.nomad.FailExec(0)

	acts, err := l.svc.ListActions(ctx, "whoami", 50)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(terminalRecords(t, l)); n != 4 {
		t.Errorf("terminal records = %d, want 4", n)
	}
	for what, text := range map[string]string{
		"the actions as the service lists them": fmt.Sprintf("%+v", acts),
		"the SQLite file and every row in it":   rawStore(t, l.dir),
		"the log":                               logs.String(),
		"the live notices":                      drainNotices(changes),
	} {
		if strings.Contains(text, termSentinel) {
			t.Errorf("%s holds the typed text", what)
		}
	}
}
