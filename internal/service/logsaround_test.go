package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/logline"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// aroundFile is a file of timed lines with the offset each starts at.
type aroundFile struct {
	lines   []string
	offsets []int64
	times   []time.Time
	size    int64
}

// makeAroundFile builds n lines, one per step from start, each text unique
// and about pad bytes long.
func makeAroundFile(label string, start time.Time, n int, step time.Duration, pad int) aroundFile {
	var f aroundFile
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * step)
		l := fmt.Sprintf("%s %s %05d %s", ts.Format("2006-01-02T15:04:05.000Z"), label, i, strings.Repeat("z", pad))
		f.lines = append(f.lines, l)
		f.offsets = append(f.offsets, f.size)
		f.times = append(f.times, ts)
		f.size += int64(len(l)) + 1
	}
	return f
}

func (f aroundFile) last() time.Time { return f.times[len(f.times)-1] }

func readatCount(n *deploytest.Nomad) int {
	c := 0
	for _, r := range n.FileRequests() {
		if r.Op == "readat" {
			c++
		}
	}
	return c
}

func mustAround(t *testing.T, e *env, copyRef string, at logline.LineID, n int) LogAround {
	t.Helper()
	got, err := e.svc.LogsAround(context.Background(), copyRef, at, n)
	if err != nil {
		t.Fatalf("LogsAround: %v", err)
	}
	return got
}

func TestLogsAroundCutsNBeforeAndAfter(t *testing.T) {
	e := readEnv(t)
	f := makeAroundFile("tick", readTime(t, "2026-10-25T00:00:00Z"), 300, time.Second, 40)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, f.lines, f.last())

	at := logline.LineID{Stream: logline.Stdout, File: 0, Offset: f.offsets[150]}
	got := mustAround(t, e, "9a4d13ff", at, 5)
	if len(got.Lines) != 11 {
		t.Fatalf("%d lines %q, want 11", len(got.Lines), entryTexts(got.Lines))
	}
	for i, l := range got.Lines { // newest first: 155 down to 145
		w := 155 - i
		if l.Text != f.lines[w] || l.ID.Offset != f.offsets[w] || l.ID.File != 0 || l.ID.Copy != "9a4d13ff" || l.Copy != "9a4d13ff" || l.App != "blog" || l.Service != "web" {
			t.Fatalf("row %d = %v %q, want line %d", i, l.ID, l.Text, w)
		}
	}
	wantAnchor := logline.LineID{Copy: "9a4d13ff", Stream: logline.Stdout, File: 0, Offset: f.offsets[150]}
	if got.Anchor != wantAnchor {
		t.Errorf("anchor = %v, want %v", got.Anchor, wantAnchor)
	}
	if len(got.Copies) != 1 || got.Copies[0].Short != "9a4d13ff" || got.Copies[0].Lines != 11 || !got.Coverage.Complete {
		t.Errorf("copies %+v coverage %+v", got.Copies, got.Coverage)
	}
	if !got.From.Equal(f.times[145]) || !got.To.Equal(f.times[155]) {
		t.Errorf("span %s to %s, want lines 145 to 155", got.From, got.To)
	}

	// The full allocation ID finds the same copy, and the default is 50 each way.
	got = mustAround(t, e, copyBlogWeb, at, 0)
	if len(got.Lines) != 101 {
		t.Errorf("default n gave %d lines, want 101", len(got.Lines))
	}

	// Near the start of the file, fewer lines are before the anchor than asked.
	got = mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, Offset: f.offsets[2]}, 5)
	if len(got.Lines) != 8 || got.Lines[len(got.Lines)-1].Text != f.lines[0] || got.Anchor.Offset != f.offsets[2] {
		t.Errorf("start of file: %d lines, oldest %q, anchor %v", len(got.Lines), got.Lines[len(got.Lines)-1].Text, got.Anchor)
	}
	// And near the end, fewer after.
	got = mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, Offset: f.offsets[298]}, 5)
	if len(got.Lines) != 7 || got.Lines[0].Text != f.lines[299] {
		t.Errorf("end of file: %d lines, newest %q", len(got.Lines), got.Lines[0].Text)
	}
}

func TestLogsAroundAnchorInsideALine(t *testing.T) {
	e := readEnv(t)
	f := makeAroundFile("tick", readTime(t, "2026-10-25T00:00:00Z"), 100, time.Second, 40)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, f.lines, f.last())

	// Three bytes into line 60: the line that contains the offset is the anchor.
	got := mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, Offset: f.offsets[60] + 3}, 2)
	if got.Anchor.Offset != f.offsets[60] {
		t.Errorf("anchor offset = %d, want line 60's %d", got.Anchor.Offset, f.offsets[60])
	}
	if want := []string{f.lines[62], f.lines[61], f.lines[60], f.lines[59], f.lines[58]}; !equalTexts(entryTexts(got.Lines), want) {
		t.Errorf("lines = %q", entryTexts(got.Lines))
	}
}

func equalTexts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLogsAroundCrossesFileEdges(t *testing.T) {
	e := readEnv(t)
	f0 := makeAroundFile("zero", readTime(t, "2026-10-25T00:00:00Z"), 400, time.Second, 90)
	f1 := makeAroundFile("one", readTime(t, "2026-10-25T00:07:00Z"), 400, time.Second, 90)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, f0.lines, f0.last())
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 1, f1.lines, f1.last())

	// Line 3 of file 1: the window reaches back into file 0.
	got := mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, File: 1, Offset: f1.offsets[3]}, 50)
	if len(got.Lines) != 101 {
		t.Fatalf("%d lines, want 101", len(got.Lines))
	}
	// Newest first: file 1 lines 53 down to 0, then file 0's last 50.
	for i, l := range got.Lines {
		var want string
		var file int
		var off int64
		if k := 53 - i; k >= 0 {
			want, file, off = f1.lines[k], 1, f1.offsets[k]
		} else {
			j := 399 + k + 1
			want, file, off = f0.lines[j], 0, f0.offsets[j]
		}
		if l.Text != want || l.ID.File != file || l.ID.Offset != off {
			t.Fatalf("row %d = %v %q, want file %d offset %d %q", i, l.ID, l.Text, file, off, want)
		}
	}
	if got.Anchor.File != 1 || got.Anchor.Offset != f1.offsets[3] {
		t.Errorf("anchor = %v", got.Anchor)
	}

	// The second-to-last line of file 0: the window reaches on into file 1.
	got = mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, File: 0, Offset: f0.offsets[398]}, 50)
	// 50 before, the anchor, and 50 after: the last line of file 0, then file 1's first 49.
	if len(got.Lines) != 101 {
		t.Fatalf("%d lines, want 101", len(got.Lines))
	}
	if got.Lines[0].Text != f1.lines[48] || got.Lines[0].ID.File != 1 || got.Lines[48].Text != f1.lines[0] || got.Lines[49].Text != f0.lines[399] || got.Lines[50].Text != f0.lines[398] {
		t.Errorf("newest %q (%v), 49th %q, 50th %q, 51st %q", got.Lines[0].Text, got.Lines[0].ID, got.Lines[48].Text, got.Lines[49].Text, got.Lines[50].Text)
	}
}

func TestLogsAroundPurgedFileAndGoneCopy(t *testing.T) {
	e := readEnv(t)
	f := makeAroundFile("tick", readTime(t, "2026-10-25T00:00:00Z"), 50, time.Second, 40)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 1, f.lines, f.last()) // file 0 was purged
	ctx := context.Background()

	_, err := e.svc.LogsAround(ctx, "9a4d13ff", logline.LineID{Stream: logline.Stdout, File: 0, Offset: 10}, 5)
	if !errors.Is(err, ErrLogRotated) {
		t.Errorf("purged file = %v, want ErrLogRotated", err)
	}
	// A file past the newest never existed: the address is wrong, not rotated away.
	_, err = e.svc.LogsAround(ctx, "9a4d13ff", logline.LineID{Stream: logline.Stdout, File: 7, Offset: 0}, 5)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "at" {
		t.Errorf("file 7 = %v, want a field error on at", err)
	}
	_, err = e.svc.LogsAround(ctx, "9a4d13ff", logline.LineID{Stream: logline.Stdout, File: 1, Offset: f.size + 10}, 5)
	if !errors.As(err, &fe) || fe.Field != "at" {
		t.Errorf("offset past the end = %v, want a field error on at", err)
	}
	// A stream with no file at all.
	_, err = e.svc.LogsAround(ctx, "9a4d13ff", logline.LineID{Stream: logline.Stderr, File: 0, Offset: 0}, 5)
	if !errors.Is(err, ErrLogRotated) {
		t.Errorf("stream with no output = %v, want ErrLogRotated", err)
	}

	// A copy Nomad doesn't list.
	_, err = e.svc.LogsAround(ctx, "deadbeef", logline.LineID{Stream: logline.Stdout, File: 1, Offset: 0}, 5)
	if !errors.Is(err, ErrLogCopyNotFound) || !errors.Is(err, store.ErrNotFound) {
		t.Errorf("gone copy = %v, want ErrLogCopyNotFound", err)
	}
	// A copy Nomad listed a moment ago and then lost: the file listing says so.
	e.nomad.SetCopies("blog", copyStub(copyBlogWorker, "worker", "running", readStart))
	_, err = e.svc.LogsAround(ctx, "9a4d13ff", logline.LineID{Stream: logline.Stdout, File: 1, Offset: 0}, 5)
	if !errors.Is(err, ErrLogCopyNotFound) {
		t.Errorf("copy removed = %v, want ErrLogCopyNotFound", err)
	}

	// Bad parameters.
	for _, tc := range []struct {
		at    logline.LineID
		n     int
		field string
	}{
		{logline.LineID{Stream: "tty", File: 1}, 5, "at"},
		{logline.LineID{Stream: logline.Stdout, File: -1}, 5, "at"},
		{logline.LineID{Stream: logline.Stdout, File: 1, Offset: -1}, 5, "at"},
		{logline.LineID{Stream: logline.Stdout, File: 1}, 201, "lines"},
		{logline.LineID{Stream: logline.Stdout, File: 1}, -1, "lines"},
	} {
		_, err := e.svc.LogsAround(ctx, "b7c20e11", tc.at, tc.n)
		if !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("%+v n=%d = %v, want a field error on %s", tc.at, tc.n, err, tc.field)
		}
	}
}

func TestLogsAroundMergesTheOtherStream(t *testing.T) {
	e := readEnv(t)
	out := makeAroundFile("out", readTime(t, "2026-10-25T00:10:00Z"), 100, 10*time.Second, 20) // 00:10:00 .. 00:26:30
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, out.lines, out.last())
	// stderr: lines at 00:05, 00:14:30 (inside), 00:15:30 (inside), 00:20:05 (inside), 00:25 (outside, n = 3 stops at 00:20).
	e.nomad.AddLogFile(copyBlogWeb, "stderr", 0, []string{
		"2026-10-25T00:05:00Z too early",
		"2026-10-25T00:14:30Z ERROR first in the span",
		"2026-10-25T00:15:30Z second in the span",
		"2026-10-25T00:20:05Z third in the span",
		"2026-10-25T00:25:30Z too late",
	}, wrote("00:26:00"))

	// Anchor: line 50 (00:18:20), n = 3: lines 47 to 53, 00:17:50 to 00:18:50.
	got := mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, Offset: out.offsets[50]}, 3)
	// No stderr line is in that span.
	if !got.From.Equal(out.times[47]) || !got.To.Equal(out.times[53]) {
		t.Fatalf("span %s to %s", got.From, got.To)
	}
	for _, l := range got.Lines {
		if l.Stream == logline.Stderr {
			t.Errorf("stderr line %q inside a span with none", l.Text)
		}
	}

	// A wider read: n = 40 covers lines 10..90 = 00:11:40 .. 00:25:00 -> the three in the middle and the last.
	got = mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, Offset: out.offsets[50]}, 40)
	var errTexts []string
	var prev time.Time
	for i, l := range got.Lines {
		if i > 0 && l.Time.After(prev) {
			t.Fatalf("row %d (%s) is newer than the row before it (%s): not newest first", i, l.Time, prev)
		}
		prev = l.Time
		if l.Stream == logline.Stderr {
			errTexts = append(errTexts, l.Text)
			if l.Copy != "9a4d13ff" || l.Service != "web" || l.App != "blog" || l.ID.Stream != logline.Stderr {
				t.Errorf("stderr row %+v", l)
			}
		}
	}
	want := []string{"2026-10-25T00:20:05Z third in the span", "2026-10-25T00:15:30Z second in the span", "2026-10-25T00:14:30Z ERROR first in the span"}
	if !equalTexts(errTexts, want) {
		t.Errorf("stderr rows = %q, want %q", errTexts, want)
	}
	if len(got.Lines) != 81+3 || got.Copies[0].Lines != 84 {
		t.Errorf("%d rows (copy says %d), want 81 stdout and 3 stderr", len(got.Lines), got.Copies[0].Lines)
	}
	// Every level: the error line is there beside the info ones.
	for _, l := range got.Lines {
		if strings.Contains(l.Text, "ERROR first") && l.Level != logline.Error {
			t.Errorf("level of the error line = %s", l.Level)
		}
	}
}

// The gate step's shape: stdout timed, stderr with no time at all, both a
// line every 2 s for 20 minutes. Around an ERROR on stdout, the stderr lines
// of the same seconds are found by their places between their file's write
// bounds (the gate step's finding 1: they were all at the copy's start).
func TestLogsAroundUntimedOtherStream(t *testing.T) {
	e := readEnv(t)
	start := wrote("00:00:00")
	e.nomad.SetCopies("blog", copyStub(copyBlogWeb, "web", "running", start))
	var out, errs []string
	for i := 0; i < 600; i++ {
		at := start.Add(time.Duration(2*i+1) * time.Second)
		out = append(out, fmt.Sprintf("%s ERROR boom %03d", at.Format(time.RFC3339), i))
		errs = append(errs, "[warn] careful")
	}
	last := start.Add(1200 * time.Second)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, out, last)
	e.nomad.AddLogFile(copyBlogWeb, "stderr", 0, errs, last)
	lineLen := int64(len(out[0]) + 1)

	got := mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, Offset: 300 * lineLen}, 3)
	var n int
	for _, l := range got.Lines {
		if l.Stream != logline.Stderr {
			continue
		}
		n++
		if l.Text != "[warn] careful" || l.TimeSource != logline.Estimated || l.Level != logline.Warn {
			t.Errorf("stderr row %q: %s %s", l.Text, l.TimeSource, l.Level)
		}
		if l.Time.Before(got.From) || l.Time.After(got.To) {
			t.Errorf("stderr row at %s is outside the span %s to %s", l.Time, got.From, got.To)
		}
	}
	// The span is stdout's lines 297 to 303, 12 seconds: about 6 stderr lines.
	if n < 5 || n > 7 {
		t.Errorf("%d stderr rows in the span %s to %s, want about 6", n, got.From, got.To)
	}
}

func TestLogsAroundTimeBinarySearch(t *testing.T) {
	e := readEnv(t)
	f := makeAroundFile("line", readTime(t, "2026-10-25T00:00:00Z"), 11000, 50*time.Millisecond, 60)
	if f.size < 1<<20 {
		t.Fatalf("the fixture is %d bytes, want at least 1 MiB", f.size)
	}
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, f.lines, f.last())
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		t    time.Time
		want int // the line
	}{
		{"exactly a line", f.times[7000], 7000},
		{"between two lines", f.times[7000].Add(10 * time.Millisecond), 7001},
		{"a hair before a line", f.times[3333].Add(-time.Millisecond), 3333},
		{"before everything", f.times[0].Add(-time.Hour), 0},
		{"the first line", f.times[0], 0},
		{"the last line", f.times[10999], 10999},
		{"after everything", f.times[10999].Add(time.Hour), 10999},
		{"near the start", f.times[41], 41},
	} {
		before := readatCount(e.nomad)
		id, err := e.svc.LogsAroundTime(ctx, "9a4d13ff", logline.Stdout, tc.t)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want := logline.LineID{Copy: "9a4d13ff", Stream: logline.Stdout, File: 0, Offset: f.offsets[tc.want]}
		if id != want {
			t.Errorf("%s: id %v, want line %d %v", tc.name, id, tc.want, want)
		}
		if probes := readatCount(e.nomad) - before; probes > 12 {
			t.Errorf("%s: %d reads, want at most 12 for 1 MiB", tc.name, probes)
		}
	}
}

func TestLogsAroundTimePicksTheFileFromTheListing(t *testing.T) {
	e := readEnv(t)
	f0 := makeAroundFile("zero", readTime(t, "2026-10-25T00:00:00Z"), 200, time.Second, 40)
	f1 := makeAroundFile("one", readTime(t, "2026-10-25T00:10:00Z"), 200, time.Second, 40)
	f2 := makeAroundFile("two", readTime(t, "2026-10-25T00:20:00Z"), 200, time.Second, 40)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, f0.lines, f0.last())
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 1, f1.lines, f1.last())
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 2, f2.lines, f2.last())

	for _, tc := range []struct {
		t    time.Time
		file int
		line int
		f    aroundFile
	}{
		{f0.times[100], 0, 100, f0},
		{f1.times[100], 1, 100, f1},
		{f2.times[199], 2, 199, f2},
		{f1.times[0], 1, 0, f1},
	} {
		before := readatCount(e.nomad)
		id, err := e.svc.LogsAroundTime(context.Background(), "9a4d13ff", logline.Stdout, tc.t)
		if err != nil {
			t.Fatal(err)
		}
		if id.File != tc.file || id.Offset != tc.f.offsets[tc.line] {
			t.Errorf("t=%s: %v, want file %d offset %d", tc.t, id, tc.file, tc.f.offsets[tc.line])
		}
		if probes := readatCount(e.nomad) - before; probes > 6 {
			t.Errorf("t=%s: %d reads for a 10 KB file", tc.t, probes)
		}
	}

	// No output on the stream, and a copy that isn't there.
	if _, err := e.svc.LogsAroundTime(context.Background(), "9a4d13ff", logline.Stderr, f0.times[0]); !errors.Is(err, ErrLogRotated) {
		t.Errorf("no stderr = %v, want ErrLogRotated", err)
	}
	if _, err := e.svc.LogsAroundTime(context.Background(), "deadbeef", logline.Stdout, f0.times[0]); !errors.Is(err, ErrLogCopyNotFound) {
		t.Errorf("gone copy = %v", err)
	}
	var fe *app.FieldError
	if _, err := e.svc.LogsAroundTime(context.Background(), "9a4d13ff", "tty", f0.times[0]); !errors.As(err, &fe) || fe.Field != "stream" {
		t.Errorf("bad stream = %v", err)
	}
}

func TestLogsAroundTimeUntimedFallsBackToBytes(t *testing.T) {
	e := readEnv(t)
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, fmt.Sprintf("untimed line %04d", i)) // 18 bytes with the newline
	}
	// The copy started at 23:00 on 10-24; the file's last write was at 00:30.
	mod := readTime(t, "2026-10-25T00:30:00Z")
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, lines, mod)

	before := readatCount(e.nomad)
	mid := readStart.Add(mod.Sub(readStart) / 2) // 23:45
	id, err := e.svc.LogsAroundTime(context.Background(), "9a4d13ff", logline.Stdout, mid)
	if err != nil {
		t.Fatal(err)
	}
	if id.File != 0 || id.Offset%18 != 0 || id.Offset < 18*498 || id.Offset > 18*502 {
		t.Errorf("midway = %v, want a line start near byte %d", id, 18*500)
	}
	if probes := readatCount(e.nomad) - before; probes > 7 {
		t.Errorf("%d reads, want the first probe's steps and one alignment read", probes)
	}
	// Before the copy started: the start; after the last write: the end.
	if id, _ := e.svc.LogsAroundTime(context.Background(), "9a4d13ff", logline.Stdout, readStart.Add(-time.Hour)); id.Offset != 0 {
		t.Errorf("before the start = %v", id)
	}
	id, _ = e.svc.LogsAroundTime(context.Background(), "9a4d13ff", logline.Stdout, mod.Add(time.Hour))
	if id.Offset%18 != 0 || id.Offset < 18*990 {
		t.Errorf("after the end = %v, want one of the last lines", id)
	}
}
