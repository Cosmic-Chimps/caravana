package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/logline"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// logsEnv has the app blog with three copies: two of the web service (w-late started after
// w-early) and one of the worker service, each writing to both streams.
func logsEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	if _, err := e.store.SaveApp(context.Background(), appWith("blog", 2, 1), nil); err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	start := func(minute int) map[string]*api.TaskState {
		return map[string]*api.TaskState{"main": {StartedAt: time.Date(2026, 10, 4, 9, minute, 0, 0, time.UTC)}}
	}
	e.nomad.SetCopies("blog",
		api.AllocationListStub{ID: "w-late", TaskGroup: "web", JobVersion: 3, ClientStatus: "running", TaskStates: start(9)},
		api.AllocationListStub{ID: "w-early", TaskGroup: "web", JobVersion: 3, ClientStatus: "running", TaskStates: start(5)},
		api.AllocationListStub{ID: "k-1", TaskGroup: "worker", JobVersion: 3, ClientStatus: "running", TaskStates: start(7)})
	e.nomad.WriteLog("w-early", "stdout", "early out 1\nearly out 2\n")
	e.nomad.WriteLog("w-early", "stderr", "early err 1\n")
	e.nomad.WriteLog("w-late", "stdout", "late out 1\n")
	e.nomad.WriteLog("k-1", "stderr", "worker err 1\n")
	return e
}

// readLogs runs Logs without follow and returns every line it sent.
func readLogs(t *testing.T, e *env, appID string, q LogQuery) ([]LogLine, error) {
	t.Helper()
	out := make(chan LogLine)
	done := make(chan error, 1)
	go func() { done <- e.svc.Logs(context.Background(), appID, q, out) }()
	var lines []LogLine
	for {
		select {
		case l := <-out:
			lines = append(lines, l)
		case err := <-done:
			return lines, err
		case <-time.After(5 * time.Second):
			t.Fatal("Logs didn't finish within 5s")
		}
	}
}

func texts(lines []LogLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

func TestLogsMergesCopiesAndStreams(t *testing.T) {
	e := logsEnv(t)
	lines, err := readLogs(t, e, "blog", LogQuery{Lines: 500})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}

	// Copies in start order (w-early 9:05, k-1 9:07, w-late 9:09); for each, stdout, then stderr.
	want := []string{"early out 1", "early out 2", "early err 1", "worker err 1", "late out 1"}
	if !slices.Equal(texts(lines), want) {
		t.Fatalf("lines = %q, want %q", texts(lines), want)
	}
	first, worker := lines[0], lines[3]
	if first.Copy != "w-early" || first.Service != "web" || first.Stream != deploy.LogStdout || first.Time != nil {
		t.Errorf("first line = %+v, want w-early's web stdout line with no time (the tail has none)", first)
	}
	if worker.Copy != "k-1" || worker.Service != "worker" || worker.Stream != deploy.LogStderr {
		t.Errorf("worker line = %+v", worker)
	}
	// Each request names the copy's service as the task.
	for _, r := range e.nomad.LogRequests() {
		wantTask := map[string]string{"w-early": "web", "w-late": "web", "k-1": "worker"}[r.Copy]
		if r.Task != wantTask || r.Follow {
			t.Errorf("request %+v, want task %q and no follow", r, wantTask)
		}
	}
}

func TestLogsFilters(t *testing.T) {
	e := logsEnv(t)
	tests := []struct {
		name string
		q    LogQuery
		want []string
	}{
		{"one stream", LogQuery{Stream: deploy.LogStdout, Lines: 500}, []string{"early out 1", "early out 2", "late out 1"}},
		{"the other stream", LogQuery{Stream: deploy.LogStderr, Lines: 500}, []string{"early err 1", "worker err 1"}},
		{"both, said out loud", LogQuery{Stream: "both", Lines: 500}, []string{"early out 1", "early out 2", "early err 1", "worker err 1", "late out 1"}},
		{"one copy", LogQuery{Copy: "w-early", Lines: 500}, []string{"early out 1", "early out 2", "early err 1"}},
		{"one copy and one stream", LogQuery{Copy: "w-early", Stream: deploy.LogStderr, Lines: 500}, []string{"early err 1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines, err := readLogs(t, e, "blog", tc.q)
			if err != nil || !slices.Equal(texts(lines), tc.want) {
				t.Errorf("lines = %q, err %v; want %q", texts(lines), err, tc.want)
			}
		})
	}
}

func TestLogsTailKeepsTheLastLines(t *testing.T) {
	e := logsEnv(t)
	var b strings.Builder
	for i := 1; i <= 700; i++ {
		fmt.Fprintf(&b, "w-late line %d\n", i)
	}
	e.nomad.WriteLog("w-late", "stdout", b.String())

	t.Run("the cap is the query's Lines, taken from the end of the merge", func(t *testing.T) {
		lines, err := readLogs(t, e, "blog", LogQuery{Lines: 3})
		if err != nil {
			t.Fatal(err)
		}
		// w-late started last, so its lines end the merge.
		if got := texts(lines); !slices.Equal(got, []string{"w-late line 698", "w-late line 699", "w-late line 700"}) {
			t.Errorf("lines = %q", got)
		}
	})
	t.Run("500 at most, whatever is asked", func(t *testing.T) {
		for _, n := range []int{0, 500, 5000} {
			lines, err := readLogs(t, e, "blog", LogQuery{Stream: deploy.LogStdout, Lines: n})
			if err != nil || len(lines) != 500 {
				t.Errorf("Lines %d: %d lines, err %v; want 500", n, len(lines), err)
				continue
			}
			if last := lines[len(lines)-1].Text; last != "w-late line 700" {
				t.Errorf("Lines %d: last line = %q", n, last)
			}
		}
	})
}

func TestLogsUnknowns(t *testing.T) {
	e := logsEnv(t)
	t.Run("a copy that isn't one of the app's", func(t *testing.T) {
		e.nomad.SetCopies("other", api.AllocationListStub{ID: "o-1", TaskGroup: "web", ClientStatus: "running"})
		_, err := readLogs(t, e, "blog", LogQuery{Copy: "o-1", Lines: 500})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("error = %v, want store.ErrNotFound", err)
		}
		if _, err := readLogs(t, e, "blog", LogQuery{Copy: "nope", Lines: 500}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("error = %v, want store.ErrNotFound", err)
		}
	})
	t.Run("an unknown app", func(t *testing.T) {
		if _, err := readLogs(t, e, "ghost", LogQuery{Lines: 500}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("error = %v, want store.ErrNotFound", err)
		}
	})
	t.Run("an app without copies has no lines", func(t *testing.T) {
		if _, err := e.store.SaveApp(context.Background(), appWith("quiet", 1), nil); err != nil {
			t.Fatal(err)
		}
		lines, err := readLogs(t, e, "quiet", LogQuery{Lines: 500})
		if err != nil || len(lines) != 0 {
			t.Errorf("lines %v, err %v; want none", lines, err)
		}
	})
	t.Run("Nomad down", func(t *testing.T) {
		e.nomad.Down()
		defer e.nomad.Up()
		if _, err := readLogs(t, e, "blog", LogQuery{Lines: 500}); !errors.Is(err, deploy.ErrNomadUnavailable) {
			t.Errorf("error = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestLogCopies(t *testing.T) {
	e := logsEnv(t)
	ctx := context.Background()
	all, err := e.svc.LogCopies(ctx, "blog", "")
	if err != nil || len(all) != 3 || all[0].ID != "w-early" || all[1].ID != "k-1" || all[2].ID != "w-late" {
		t.Errorf("all = %v, err %v; want the three copies in start order", all, err)
	}
	one, err := e.svc.LogCopies(ctx, "blog", "k-1")
	if err != nil || len(one) != 1 || one[0].Service != "worker" {
		t.Errorf("one = %v, err %v", one, err)
	}
	if _, err := e.svc.LogCopies(ctx, "blog", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown copy: %v, want store.ErrNotFound", err)
	}
	if _, err := e.svc.LogCopies(ctx, "ghost", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app: %v, want store.ErrNotFound", err)
	}
}

func TestLogsFollow(t *testing.T) {
	e := logsEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan LogLine)
	done := make(chan error, 1)
	go func() {
		done <- e.svc.Logs(ctx, "blog", LogQuery{Stream: deploy.LogStdout, Lines: 500, Follow: true}, out)
	}()
	next := func() LogLine {
		t.Helper()
		select {
		case l := <-out:
			return l
		case err := <-done:
			t.Fatalf("Logs returned (%v) while following", err)
		case <-time.After(5 * time.Second):
			t.Fatal("no line within 5s")
		}
		return LogLine{}
	}

	// The tail first, as without follow.
	for _, want := range []string{"early out 1", "early out 2", "late out 1"} {
		if l := next(); l.Text != want || l.Time != nil {
			t.Fatalf("tail line = %+v, want %q with no time", l, want)
		}
	}

	// Then new lines from any copy, each stamped with when Caravana got it.
	deadline := time.Now().Add(5 * time.Second)
	for e.nomad.OpenLogStreams() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := e.nomad.OpenLogStreams(); n != 3 { // one per (copy, stdout)
		t.Fatalf("%d follow streams open, want 3: one per copy for the chosen stream", n)
	}
	before := time.Now()
	e.nomad.WriteLog("w-late", "stdout", "late out 2\n")
	l := next()
	if l.Text != "late out 2" || l.Copy != "w-late" || l.Service != "web" || l.Stream != deploy.LogStdout {
		t.Errorf("followed line = %+v", l)
	}
	if l.Time == nil || l.Time.Before(before.Add(-time.Second)) || l.Time.After(time.Now().Add(time.Second)) {
		t.Errorf("followed line time = %v, want about now", l.Time)
	}
	e.nomad.WriteLog("k-1", "stdout", "worker out 1\n")
	if l := next(); l.Text != "worker out 1" || l.Copy != "k-1" || l.Service != "worker" {
		t.Errorf("followed line = %+v", l)
	}

	// Ending: the context ends it, with no error, and the connections go.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Logs after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Logs didn't return after the context ended")
	}
	deadline = time.Now().Add(5 * time.Second)
	for e.nomad.OpenLogStreams() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := e.nomad.OpenLogStreams(); n != 0 {
		t.Errorf("%d follow streams still open after cancel", n)
	}
}

// A stream with files is tailed through them: each line has its address, its level, the stripped
// text and a time (read from the line, or estimated from the one before). A stream without files
// is tailed from Nomad's frames, as before: no address, and a time only when the line carries one.
func TestLogsTailFromFiles(t *testing.T) {
	e := logsEnv(t)
	e.nomad.AddLogFile("w-early", "stdout", 0, []string{
		`2026-10-04T09:10:00Z first`,
		`no time here`,
		`2026-10-04T09:11:00Z ERROR bad`,
		"2026-10-04T09:12:00Z \x1b[31mcolored\x1b[0m",
		`{"level":"warn","ts":"2026-10-04T09:13:00Z","msg":"json says"}`,
	}, time.Date(2026, 10, 4, 9, 13, 30, 0, time.UTC))

	lines, err := readLogs(t, e, "blog", LogQuery{Copy: "w-early", Lines: 500})
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) *time.Time { v := time.Date(2026, 10, 4, h, m, 0, 0, time.UTC); return &v }
	type want struct {
		text   string
		id     logline.LineID
		level  logline.Level
		source logline.TimeSource
		time   *time.Time
	}
	out := func(off int64) logline.LineID {
		return logline.LineID{Copy: "w-early", Stream: logline.Stdout, File: 0, Offset: off}
	}
	// Each file line's offset is the length of the lines before it, newlines included.
	wants := []want{
		{`2026-10-04T09:10:00Z first`, out(0), logline.Info, logline.FromLine, at(9, 10)},
		{`no time here`, out(27), logline.Info, logline.Estimated, at(9, 10)},
		{`2026-10-04T09:11:00Z ERROR bad`, out(40), logline.Error, logline.FromLine, at(9, 11)},
		{`2026-10-04T09:12:00Z colored`, out(71), logline.Info, logline.FromLine, at(9, 12)},
		{`{"level":"warn","ts":"2026-10-04T09:13:00Z","msg":"json says"}`, out(109), logline.Warn, logline.FromLine, at(9, 13)},
		// The stderr stream has no file: its frames, with no address or time.
		{`early err 1`, logline.LineID{}, logline.Error, "", nil}, // the word err is a level
	}
	if len(lines) != len(wants) {
		t.Fatalf("%d lines (%q), want %d", len(lines), texts(lines), len(wants))
	}
	for i, w := range wants {
		l := lines[i]
		if l.Text != w.text || l.ID != w.id || l.Level != w.level || l.TimeSource != w.source || (l.Time == nil) != (w.time == nil) ||
			(l.Time != nil && !l.Time.Equal(*w.time)) {
			t.Errorf("line %d = {%q %v %s %q %v}, want {%q %v %s %q %v}", i, l.Text, l.ID, l.Level, l.TimeSource, l.Time, w.text, w.id, w.level, w.source, w.time)
		}
		if l.Copy != "w-early" || l.Service != "web" {
			t.Errorf("line %d belongs to %s/%s", i, l.Service, l.Copy)
		}
	}
}

// The tail's cut still takes the last lines, over lines that now carry times.
func TestLogsTailFromFilesKeepsTheLastLines(t *testing.T) {
	e := logsEnv(t)
	var in []string
	for i := range 700 {
		in = append(in, fmt.Sprintf("2026-10-04T09:%02d:%02dZ w-late line %d", 10+i/60, i%60, i+1))
	}
	e.nomad.AddLogFile("w-late", "stdout", 0, in, time.Date(2026, 10, 4, 9, 22, 0, 0, time.UTC))
	lines, err := readLogs(t, e, "blog", LogQuery{Copy: "w-late", Stream: deploy.LogStdout, Lines: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || !strings.HasSuffix(lines[0].Text, "line 698") || !strings.HasSuffix(lines[2].Text, "line 700") {
		t.Fatalf("lines = %q, want the last three", texts(lines))
	}
	if lines[2].Time == nil || lines[2].ID.Stream != logline.Stdout || lines[2].ID.Offset == 0 {
		t.Errorf("last line = %+v, want a time and an address", lines[2])
	}
}

// A followed line has its arrival as its time, marked received, and a level, and no address.
func TestLogsFollowedLineHasLevelAndSource(t *testing.T) {
	e := logsEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan LogLine)
	go func() {
		_ = e.svc.Logs(ctx, "blog", LogQuery{Copy: "w-late", Stream: deploy.LogStdout, Lines: 1, Follow: true}, out)
	}()
	next := func() LogLine {
		select {
		case l := <-out:
			return l
		case <-time.After(5 * time.Second):
			t.Fatal("no line within 5s")
		}
		return LogLine{}
	}
	next() // the tail
	deadline := time.Now().Add(5 * time.Second)
	for e.nomad.OpenLogStreams() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	e.nomad.WriteLog("w-late", "stdout", "\x1b[31mERROR boom\x1b[0m\n")
	l := next()
	if l.Text != "ERROR boom" || l.Level != logline.Error || l.TimeSource != logline.Received || l.Time == nil || l.ID != (logline.LineID{}) {
		t.Errorf("followed line = %+v, want stripped text, level error, received, a time and no address", l)
	}
}
