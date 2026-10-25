package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/logline"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The environment of these tests is Default › production with two apps: blog
// (services web and worker) and docs (service web). Every copy started the
// evening before "now", so the research's example lines (all of 2026-10-24 and
// 25) fall inside their lives.
const (
	copyBlogWeb    = "9a4d13ff-0000-4000-8000-000000000001"
	copyBlogWorker = "b7c20e11-0000-4000-8000-000000000002"
	copyDocsWeb    = "c3e5a0d9-0000-4000-8000-000000000003"
	copyBlogOld    = "d4f6b1ea-0000-4000-8000-000000000004"

	sentinelRead = "s3cret-sentinel-024-read"
)

var (
	readNow   = time.Date(2026, 10, 25, 0, 40, 0, 0, time.UTC)
	readStart = time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)
)

func copyStub(id, group, status string, start time.Time) api.AllocationListStub {
	return api.AllocationListStub{ID: id, TaskGroup: group, JobVersion: 3, ClientStatus: status,
		TaskStates: map[string]*api.TaskState{group: {StartedAt: start}}}
}

// readEnv is the environment above, with the clock at readNow. Nothing has
// written a line yet.
func readEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.svc.clock.(*fakeClock).Set(readNow)
	ctx := context.Background()
	for _, a := range []app.App{appWith("blog", 1, 1), appWith("docs", 1)} {
		if _, err := e.store.SaveApp(ctx, a, nil); err != nil {
			t.Fatalf("SaveApp: %v", err)
		}
	}
	e.nomad.SetCopies("blog",
		copyStub(copyBlogWeb, "web", "running", readStart),
		copyStub(copyBlogWorker, "worker", "running", readStart))
	e.nomad.SetCopies("docs", copyStub(copyDocsWeb, "web", "running", readStart))
	return e
}

// wrote is when a file was last written, on 2026-10-25: a file's last write is
// within a minute of its last line, or its times aren't believed.
func wrote(clock string) time.Time {
	v, err := time.Parse(time.RFC3339, "2026-10-25T"+clock+"Z")
	if err != nil {
		panic(err)
	}
	return v
}

func entryTexts(rows []LogEntry) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out
}

func readTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return v.UTC()
}

func mustRead(t *testing.T, e *env, q EnvLogQuery) LogRead {
	t.Helper()
	read, err := e.svc.ReadLogs(context.Background(), q)
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	return read
}

// The research's example lines, one of each shape, with the level and text the
// readers should find.
func TestReadLogsExampleShapes(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{
		`2026-10-24 23:48:36.479 UTC [1] LOG:  database system is ready to accept connections`,
		`{"level":"error","ts":1792886809.1,"msg":"dial tcp: refused"}`,
		`    at com.example.Foo.bar(Foo.java:42)`,
		`2026/10/25 00:16:42 Starting up on port 80`,
		"\x1b[90m2026-10-25T00:26:49Z\x1b[0m \x1b[31mERR\x1b[0m error=\"middleware does not exist\"",
		`192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`,
	}, wrote("00:33:40"))

	read := mustRead(t, e, EnvLogQuery{Range: 24 * time.Hour, App: "blog"})
	type want struct {
		time  string
		src   logline.TimeSource
		level logline.Level
		text  string
	}
	// Newest first.
	wants := []want{
		{"2026-10-25T00:33:21Z", logline.FromLine, logline.Info, `192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`},
		{"2026-10-25T00:26:49Z", logline.FromLine, logline.Error, `2026-10-25T00:26:49Z ERR error="middleware does not exist"`},
		{"2026-10-25T00:16:42Z", logline.FromLine, logline.Info, `2026/10/25 00:16:42 Starting up on port 80`},
		{"2026-10-25T00:06:49.1Z", logline.Estimated, logline.Error, `    at com.example.Foo.bar(Foo.java:42)`},
		{"2026-10-25T00:06:49.1Z", logline.FromLine, logline.Error, `dial tcp: refused`},
		{"2026-10-24T23:48:36.479Z", logline.FromLine, logline.Info, `2026-10-24 23:48:36.479 UTC [1] LOG:  database system is ready to accept connections`},
	}
	if len(read.Lines) != len(wants) {
		t.Fatalf("%d lines (%q), want %d", len(read.Lines), entryTexts(read.Lines), len(wants))
	}
	for i, w := range wants {
		got := read.Lines[i]
		if !got.Time.Equal(readTime(t, w.time)) || got.TimeSource != w.src || got.Level != w.level || got.Text != w.text {
			t.Errorf("line %d = {%s %s %s %q}, want {%s %s %s %q}", i,
				got.Time.Format(time.RFC3339Nano), got.TimeSource, got.Level, got.Text, w.time, w.src, w.level, w.text)
		}
		if got.App != "blog" || got.Service != "web" || got.Copy != "9a4d13ff" || got.Stream != logline.Stdout {
			t.Errorf("line %d belongs to %s/%s copy %s stream %s", i, got.App, got.Service, got.Copy, got.Stream)
		}
	}
	if read.Lines[4].Raw == "" {
		t.Error("the JSON line has no Raw")
	}
	if !read.Coverage.Complete || !read.Coverage.Timed || !read.Coverage.From.Equal(read.From) {
		t.Errorf("coverage = %+v, want complete and timed from the range's start %s", read.Coverage, read.From)
	}
	if read.Dropped {
		t.Error("Dropped is set for a copy whose first file is file 0")
	}
}

func TestReadLogsRangeCut(t *testing.T) {
	e := readEnv(t)
	var lines []string
	for m := 0; m < 30; m++ {
		lines = append(lines, fmt.Sprintf("2026-10-25T00:%02d:00Z tick %02d", m, m))
	}
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, lines, wrote("00:29:30"))

	read := mustRead(t, e, EnvLogQuery{App: "blog", From: readTime(t, "2026-10-25T00:25:00Z"), To: readTime(t, "2026-10-25T00:27:30Z")})
	want := []string{"2026-10-25T00:27:00Z tick 27", "2026-10-25T00:26:00Z tick 26", "2026-10-25T00:25:00Z tick 25"}
	if got := entryTexts(read.Lines); !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	if !read.Coverage.Complete || !read.Coverage.From.Equal(readTime(t, "2026-10-25T00:25:00Z")) {
		t.Errorf("coverage = %+v, want complete from 00:25", read.Coverage)
	}
	if len(read.stamps) != 3 || read.Copies[0].Lines != 3 {
		t.Errorf("stamps %d, copy lines %d, want 3 and 3", len(read.stamps), read.Copies[0].Lines)
	}

	// The default range is 15 minutes ending now (00:40): nothing newer than 00:25 was written.
	read = mustRead(t, e, EnvLogQuery{App: "blog"})
	if !read.From.Equal(readNow.Add(-15*time.Minute)) || !read.To.Equal(readNow) {
		t.Errorf("range = %s to %s, want the 15 minutes before now", read.From, read.To)
	}
	if got := len(read.Lines); got != 5 { // 00:25 .. 00:29
		t.Errorf("%d lines in the default range, want 5 (00:25 to 00:29)", got)
	}

	// Lines is a cut of the rows, newest first.
	read = mustRead(t, e, EnvLogQuery{App: "blog", Lines: 2})
	if got := entryTexts(read.Lines); !slices.Equal(got, []string{"2026-10-25T00:29:00Z tick 29", "2026-10-25T00:28:00Z tick 28"}) {
		t.Errorf("Lines 2 = %q", got)
	}

	// Before is "Show older": the lines before it.
	read = mustRead(t, e, EnvLogQuery{App: "blog", Range: 15 * time.Minute, Before: readTime(t, "2026-10-25T00:27:00Z"), Lines: 1})
	if got := entryTexts(read.Lines); !slices.Equal(got, []string{"2026-10-25T00:26:00Z tick 26"}) {
		t.Errorf("Before = %q", got)
	}
}

// bigFile builds n lines of 98 bytes with a time 50 ms after the last one's,
// from start. Line text is unique.
func bigFile(start time.Time, n int, label string) (lines []string, last time.Time) {
	for i := 0; i < n; i++ {
		last = start.Add(time.Duration(i) * 50 * time.Millisecond)
		lines = append(lines, fmt.Sprintf("%s %s %06d %s", last.Format("2006-01-02T15:04:05.000Z"), label, i, strings.Repeat("x", 60)))
	}
	return lines, last
}

func TestReadLogsBudgetCut(t *testing.T) {
	e := readEnv(t)
	lines, last := bigFile(readTime(t, "2026-10-24T23:30:00Z"), 33000, "line")
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, lines, last)
	if size, _ := e.nomad.LogFileSize(copyBlogWeb, "stdout", 0); size < 3<<20 {
		t.Fatalf("the file is %d bytes, want more than 3 MiB", size)
	}

	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: 24 * time.Hour, Lines: 1000})
	if read.Coverage.Complete || !read.Coverage.Timed {
		t.Errorf("coverage = %+v, want incomplete (the budget) and timed", read.Coverage)
	}
	if read.Coverage.From.Before(read.From) || !read.Coverage.From.Before(last) {
		t.Errorf("coverage from %s is not between the range's start and the last line", read.Coverage.From)
	}
	if got := read.Lines[0].Text; !strings.Contains(got, " line 032999 ") {
		t.Errorf("the newest row is %q, want line 032999", got)
	}
	var oldest time.Time
	for i, st := range read.stamps {
		if i == 0 || st.Time.Before(oldest) {
			oldest = st.Time
		}
	}
	if !oldest.Equal(read.Coverage.From) {
		t.Errorf("coverage from %s, the oldest line read is %s", read.Coverage.From, oldest)
	}
	if n := len(read.stamps); n < 10000 || n > 11000 {
		t.Errorf("%d lines read, want about 1 MiB of 98-byte lines (10,700)", n)
	}
	if read.Copies[0].Lines != len(read.stamps) {
		t.Errorf("copy lines %d, stamps %d", read.Copies[0].Lines, len(read.stamps))
	}
	var bytesRead int64
	for _, r := range e.nomad.FileRequests() {
		if r.Op == "readat" {
			bytesRead += r.Limit
		}
	}
	if bytesRead != logStreamBudget {
		t.Errorf("read %d bytes, want the stream's budget %d", bytesRead, logStreamBudget)
	}
}

func TestStreamBudgetSplit(t *testing.T) {
	for _, tc := range []struct {
		streams int
		want    int64
	}{{1, logStreamBudget}, {16, logStreamBudget}, {32, 512 << 10}, {40, logPageBudget / 40}, {100000, logChunk}} {
		if got := streamBudget(tc.streams); got != tc.want {
			t.Errorf("streamBudget(%d) = %d, want %d", tc.streams, got, tc.want)
		}
	}
}

// Two files, the lines of each cut by the 64 KiB chunks: every line comes back
// whole, in order, with the offset of its first byte, across the files.
func TestReadLogsCrossesFiles(t *testing.T) {
	e := readEnv(t)
	type placed struct {
		file   int
		offset int64
		text   string
	}
	var all []placed
	build := func(file int, start time.Time, n int) (last time.Time) {
		var lines []string
		var off int64
		for i := 0; i < n; i++ {
			last = start.Add(time.Duration(i) * 100 * time.Millisecond)
			l := fmt.Sprintf("%s event %d-%05d %s", last.Format("2006-01-02T15:04:05.000Z"), file, i, strings.Repeat("y", 110))
			lines = append(lines, l)
			all = append(all, placed{file, off, l})
			off += int64(len(l)) + 1
		}
		e.nomad.AddLogFile(copyBlogWeb, "stdout", file, lines, last)
		return last
	}
	build(0, readTime(t, "2026-10-25T00:00:00Z"), 700)
	build(1, readTime(t, "2026-10-25T00:04:00Z"), 600)

	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: 24 * time.Hour, Lines: 1000})
	if n := len(read.stamps); n != 1300 {
		t.Fatalf("%d lines read, want all 1300", n)
	}
	if len(read.Lines) != 1000 {
		t.Fatalf("%d rows, want 1000", len(read.Lines))
	}
	for i, row := range read.Lines {
		w := all[len(all)-1-i]
		if row.Text != w.text || row.ID.File != w.file || row.ID.Offset != w.offset || row.ID.Copy != "9a4d13ff" || row.ID.Stream != logline.Stdout {
			t.Fatalf("row %d = %v %q, want file %d offset %d %q", i, row.ID, row.Text, w.file, w.offset, w.text)
		}
	}
	// Only the first 300 lines of file 0 are left out of the rows: the boundary line is the 601st from the end.
	if got := read.Lines[600].ID; got.File != 0 || got.Offset != all[699].offset {
		t.Errorf("the last line of file 0 is %v", got)
	}
}

// A stream with no timed line: each line is placed by its byte offset between
// the file's write bounds (the copy's start and the file's last write), not
// all at the copy's start (the gate step's finding 1).
func TestReadLogsUntimedStreamIsPlacedByItsFileBounds(t *testing.T) {
	e := readEnv(t)
	mod := wrote("00:30:10")
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"booting", "still booting", "ready"}, mod)

	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: 24 * time.Hour})
	if got := entryTexts(read.Lines); !slices.Equal(got, []string{"ready", "still booting", "booting"}) {
		t.Fatalf("order = %q, want the file's order, newest first", got)
	}
	// Offsets 0, 8 and 22 of 28 bytes, between 23:00:00 and 00:30:10.
	for i, off := range []int64{22, 8, 0} {
		l := read.Lines[i]
		want := readStart.Add(time.Duration(float64(mod.Sub(readStart)) * float64(off) / 28))
		if l.TimeSource != logline.Estimated || !l.Time.Equal(want) {
			t.Errorf("%q: %s at %s, want estimated at %s (offset %d of 28)", l.Text, l.TimeSource, l.Time, want, off)
		}
	}
}

// 600 untimed lines over a file written from 00:20 to 00:40 (now): the last
// 15 minutes hold the newest 450, not none (all at the copy's start) and not
// all of them.
func TestReadLogsUntimedStreamSpreadsAcrossTheRange(t *testing.T) {
	e := readEnv(t)
	e.nomad.SetCopies("blog", copyStub(copyBlogWeb, "web", "running", wrote("00:20:00")))
	var lines []string
	for i := 0; i < 600; i++ {
		lines = append(lines, fmt.Sprintf("untimed line %04d", i)) // 18 bytes with the newline
	}
	e.nomad.AddLogFile(copyBlogWeb, "stderr", 0, lines, readNow)

	read := mustRead(t, e, EnvLogQuery{App: "blog"})
	if n := len(read.Lines); n != 450 {
		t.Fatalf("%d rows in the last 15 minutes, want 450 (lines 150 to 599)", n)
	}
	if got := read.Lines[0].Text; got != "untimed line 0599" {
		t.Errorf("the newest row is %q", got)
	}
	if got := read.Lines[449]; got.Text != "untimed line 0150" || !got.Time.Equal(wrote("00:25:00")) {
		t.Errorf("the oldest row is %q at %s, want line 0150 at 00:25:00", got.Text, got.Time)
	}
	for i, l := range read.Lines {
		if l.TimeSource != logline.Estimated || l.Time.Before(read.From) || l.Time.After(readNow) {
			t.Fatalf("row %d %q: %s at %s, want estimated inside the range", i, l.Text, l.TimeSource, l.Time)
		}
		if i > 0 && !l.Time.Before(read.Lines[i-1].Time) {
			t.Fatalf("row %d %q at %s is not older than the row before it", i, l.Text, l.Time)
		}
	}
	if !read.Coverage.Complete {
		t.Errorf("coverage = %+v, want complete", read.Coverage)
	}
}

// The walk of an untimed stream stops at the range's start by the lines'
// placed times, not at its budget.
func TestReadLogsUntimedWalkStopsAtTheRange(t *testing.T) {
	e := readEnv(t)
	e.nomad.SetCopies("blog", copyStub(copyBlogWeb, "web", "running", wrote("00:00:00")))
	var lines []string
	for i := 0; i < 100000; i++ {
		lines = append(lines, fmt.Sprintf("untimed %06d %s", i, strings.Repeat("u", 24))) // 40 bytes with the newline
	}
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, lines, readNow)

	// The last 2 of the file's 40 minutes: its last 5% of 4,000,000 bytes.
	read := mustRead(t, e, EnvLogQuery{App: "blog", From: wrote("00:38:00"), To: readNow, Lines: 1000})
	if n := len(read.stamps); n < 4999 || n > 5001 {
		t.Errorf("%d lines in the range, want 5000", n)
	}
	if !read.Coverage.Complete {
		t.Errorf("coverage = %+v, want complete: the walk reached the range's start", read.Coverage)
	}
	var bytesRead int64
	for _, r := range e.nomad.FileRequests() {
		if r.Op == "readat" {
			bytesRead += r.Limit
		}
	}
	if bytesRead > 5*logChunk {
		t.Errorf("read %d bytes, want the 200,000 the range needs, in 64 KiB chunks (the budget is %d)", bytesRead, logStreamBudget)
	}
}

// A first timed line a second before the copy's start (the container's clock
// against Nomad's start event, the gate step's finding 2) keeps its time, the
// untimed line before it is placed earlier still, and a search for its time
// finds it, not the untimed line.
func TestReadLogsTimedLineBeforeTheCopyStart(t *testing.T) {
	e := readEnv(t)
	started := readTime(t, "2026-10-25T00:20:00.039Z")
	e.nomad.SetCopies("blog", copyStub(copyBlogWeb, "web", "running", started))
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{
		"banner",
		"2026-10-25T00:19:59Z ERROR boom",
		"no time",
		"2026-10-25T00:20:01Z tick",
	}, wrote("00:20:02"))

	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: time.Hour})
	if got := entryTexts(read.Lines); !slices.Equal(got, []string{"2026-10-25T00:20:01Z tick", "no time", "2026-10-25T00:19:59Z ERROR boom", "banner"}) {
		t.Fatalf("order = %q", got)
	}
	boom, banner := read.Lines[2], read.Lines[3]
	t1 := wrote("00:19:59")
	if boom.TimeSource != logline.FromLine || !boom.Time.Equal(t1) {
		t.Errorf("the ERROR line is %s at %s, want its own time %s", boom.TimeSource, boom.Time, t1)
	}
	if banner.TimeSource != logline.Estimated || !banner.Time.Before(t1) || banner.Time.Before(started.Add(-logline.Slack)) {
		t.Errorf("the untimed first line is %s at %s, want estimated before %s and not more than the slack before the start", banner.TimeSource, banner.Time, t1)
	}
	if l := read.Lines[1]; l.TimeSource != logline.Estimated || !l.Time.Equal(t1) {
		t.Errorf("the untimed line after it is %s at %s, want estimated at %s", l.TimeSource, l.Time, t1)
	}

	id, err := e.svc.LogsAroundTime(context.Background(), "9a4d13ff", logline.Stdout, t1)
	if err != nil {
		t.Fatal(err)
	}
	if id != boom.ID {
		t.Errorf("around %s = %v, want the ERROR line %v", t1, id, boom.ID)
	}
}

// In a file with timed lines, the untimed lines before its first timed line
// are placed between the file's lower bound (the previous file's last write)
// and that line, and the ones after it take its time.
func TestReadLogsUntimedLinesBeforeTheFirstTimedLine(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"2026-10-25T00:05:00Z a", "2026-10-25T00:10:00Z b"}, wrote("00:10:00"))
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 1, []string{"cont one", "cont two", "2026-10-25T00:20:00Z x", "after x"}, wrote("00:20:05"))

	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: time.Hour})
	got := map[string]LogEntry{}
	for _, l := range read.Lines {
		got[l.Text] = l
	}
	lo, t1 := wrote("00:10:00"), wrote("00:20:00")
	one, two := got["cont one"], got["cont two"]
	if one.TimeSource != logline.Estimated || !one.Time.Equal(lo) {
		t.Errorf("cont one (offset 0) is %s at %s, want estimated at the lower bound %s", one.TimeSource, one.Time, lo)
	}
	if two.TimeSource != logline.Estimated || !two.Time.After(lo) || !two.Time.Before(t1) {
		t.Errorf("cont two is %s at %s, want estimated between %s and %s", two.TimeSource, two.Time, lo, t1)
	}
	if l := got["after x"]; l.TimeSource != logline.Estimated || !l.Time.Equal(t1) {
		t.Errorf("after x is %s at %s, want estimated at %s", l.TimeSource, l.Time, t1)
	}
}

func TestReadLogsSkewedFileIsEstimated(t *testing.T) {
	e := readEnv(t)
	started := readTime(t, "2026-10-24T20:00:00Z")
	e.nomad.SetCopies("blog",
		copyStub(copyBlogWeb, "web", "running", started),
		copyStub(copyBlogWorker, "worker", "running", started))

	// web: times two and a half hours behind the file's last write, inside the
	// bounds, as a container in a zone west of UTC writes them.
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{
		"2026-10-24T22:00:00Z first", "2026-10-24T22:00:05Z second"}, wrote("00:30:10"))
	// web's stderr is the control: its times match its file.
	e.nomad.AddLogFile(copyBlogWeb, "stderr", 0, []string{"2026-10-25T00:20:00Z steady"}, readTime(t, "2026-10-25T00:20:30Z"))
	// worker: times two hours after the file's last write, past the upper bound.
	e.nomad.AddLogFile(copyBlogWorker, "stdout", 0, []string{"2026-10-25T02:30:00Z ahead"}, readTime(t, "2026-10-25T00:30:00Z"))

	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: 24 * time.Hour, Lines: 10})
	got := map[string]LogEntry{}
	for _, l := range read.Lines {
		got[l.Text] = l
	}
	// Each skewed line is placed like an untimed one: by its offset between
	// the copy's start and its file's last write. "first" and "ahead" are at
	// offset 0, "second" at byte 27 of web's 55.
	webMod := wrote("00:30:10")
	for text, want := range map[string]time.Time{
		"2026-10-24T22:00:00Z first":  started,
		"2026-10-24T22:00:05Z second": started.Add(time.Duration(float64(webMod.Sub(started)) * 27 / 55)),
		"2026-10-25T02:30:00Z ahead":  started,
	} {
		l, ok := got[text]
		if !ok {
			t.Fatalf("no row %q in %q", text, entryTexts(read.Lines))
		}
		if l.TimeSource != logline.Estimated || !l.Time.Equal(want) {
			t.Errorf("%q: %s at %s, want estimated at %s", text, l.TimeSource, l.Time, want)
		}
	}
	if l := got["2026-10-25T00:20:00Z steady"]; l.TimeSource != logline.FromLine || !l.Time.Equal(readTime(t, "2026-10-25T00:20:00Z")) {
		t.Errorf("the control line is %s at %s", l.TimeSource, l.Time)
	}
}

func TestReadLogsMergesStreamsWithNonDecreasingSortTimes(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{
		"2026-10-25T00:10:05.000Z a", "2026-10-25T00:10:03.000Z b", "2026-10-25T00:10:01.000Z c"}, wrote("00:10:10"))
	e.nomad.AddLogFile(copyBlogWeb, "stderr", 0, []string{"2026-10-25T00:10:04.000Z err"}, wrote("00:10:10"))

	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: time.Hour})
	// b and c are older than a but sort at a's time: they stay with their stream, ahead of err (00:10:04).
	if got := entryTexts(read.Lines); !slices.Equal(got, []string{"2026-10-25T00:10:01.000Z c", "2026-10-25T00:10:03.000Z b", "2026-10-25T00:10:05.000Z a", "2026-10-25T00:10:04.000Z err"}) {
		t.Errorf("order = %q", got)
	}
	if !read.Lines[0].Time.Equal(readTime(t, "2026-10-25T00:10:01Z")) {
		t.Errorf("c keeps its own time for display, has %s", read.Lines[0].Time)
	}

	read = mustRead(t, e, EnvLogQuery{App: "blog", Range: time.Hour, Stream: logline.Stderr})
	if got := entryTexts(read.Lines); !slices.Equal(got, []string{"2026-10-25T00:10:04.000Z err"}) {
		t.Errorf("stderr only = %q", got)
	}
}

func TestReadLogsStreamFilterReadsOnlyThatStream(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"2026-10-25T00:10:05Z out"}, wrote("00:10:10"))
	e.nomad.AddLogFile(copyBlogWeb, "stderr", 0, []string{"2026-10-25T00:10:06Z err"}, wrote("00:10:10"))
	mustRead(t, e, EnvLogQuery{App: "blog", Stream: logline.Stderr})
	for _, r := range e.nomad.FileRequests() {
		if r.Op != "ls" && strings.Contains(r.Path, ".stdout.") {
			t.Errorf("a request read stdout: %+v", r)
		}
	}
}

func TestReadLogsTwoCopiesEqualLinesAreNotFolded(t *testing.T) {
	e := readEnv(t)
	other := "e5a7c2fb-0000-4000-8000-000000000005"
	e.nomad.SetCopies("blog",
		copyStub(copyBlogWeb, "web", "running", readStart),
		copyStub(other, "web", "running", readStart),
		copyStub(copyBlogWorker, "worker", "running", readStart))
	check := []string{
		`192.168.5.15 - - [25/Oct/2026:00:30:01 +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`,
		`192.168.5.15 - - [25/Oct/2026:00:30:11 +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`,
		`192.168.5.15 - - [25/Oct/2026:00:30:21 +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`,
	}
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, check, wrote("00:30:30"))
	e.nomad.AddLogFile(other, "stdout", 0, check, wrote("00:30:30"))

	read := mustRead(t, e, EnvLogQuery{Service: "blog/web"})
	if len(read.Lines) != 2 {
		t.Fatalf("%d rows, want one folded row per copy", len(read.Lines))
	}
	copies := map[string]bool{}
	for _, l := range read.Lines {
		if l.Repeat != 2 {
			t.Errorf("row of copy %s repeats %d, want 2 (3 lines)", l.Copy, l.Repeat)
		}
		if !l.Time.Equal(readTime(t, "2026-10-25T00:30:21Z")) {
			t.Errorf("the folded row is at %s, want its newest line's time", l.Time)
		}
		copies[l.Copy] = true
	}
	if len(copies) != 2 {
		t.Errorf("rows are from copies %v, want both", copies)
	}
	// The rail counts the unfolded lines.
	for _, c := range read.Copies {
		if c.Lines != 3 {
			t.Errorf("copy %s has %d lines, want 3", c.Short, c.Lines)
		}
	}
}

// A search for 502 over nginx's access lines keeps only the 502s, each where
// the copy wrote it: the 200s between them keep them apart, so nothing folds
// (the fold comes before the filter, so "×N" is always consecutive lines).
func TestReadLogsFoldComesBeforeFilter(t *testing.T) {
	e := readEnv(t)
	access := func(sec int, status int) string {
		return fmt.Sprintf(`192.168.5.15 - - [25/Oct/2026:00:30:%02d +0000] "GET / HTTP/1.1" %d 896 "-" "Nomad/2.0.7"`, sec, status)
	}
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{
		access(1, 200), access(2, 502), access(3, 200), access(4, 502), access(5, 200)}, wrote("00:30:10"))

	read := mustRead(t, e, EnvLogQuery{App: "blog", Q: "502"})
	if len(read.Lines) != 2 || read.Lines[0].Repeat != 0 || read.Lines[1].Repeat != 0 || !strings.Contains(read.Lines[0].Text, " 502 ") {
		t.Fatalf("rows = %+v, want two unfolded 502 rows", entryTexts(read.Lines))
	}
	if !read.Lines[0].Time.Equal(readTime(t, "2026-10-25T00:30:04Z")) || !read.Lines[1].Time.Equal(readTime(t, "2026-10-25T00:30:02Z")) {
		t.Errorf("the rows are at %s and %s, want each line's own time", read.Lines[0].Time, read.Lines[1].Time)
	}
	if read.Copies[0].Lines != 2 {
		t.Errorf("the copy has %d lines after the filter, want 2", read.Copies[0].Lines)
	}

	// Without the search the 200s and 502s alternate: nothing folds.
	read = mustRead(t, e, EnvLogQuery{App: "blog"})
	if len(read.Lines) != 5 {
		t.Errorf("%d rows without a filter, want 5", len(read.Lines))
	}
	// The search covers the app and service too, case-insensitively.
	if read = mustRead(t, e, EnvLogQuery{App: "blog", Q: "BLOG/WEB"}); len(read.Lines) != 5 {
		t.Errorf("%d rows for q=BLOG/WEB, want all 5", len(read.Lines))
	}
	if read = mustRead(t, e, EnvLogQuery{App: "blog", Q: "blog/worker"}); len(read.Lines) != 0 {
		t.Errorf("%d rows for q=blog/worker, want none", len(read.Lines))
	}
}

// A JSON error that repeats every ten lines, with other output between: a
// search for it shows one row per occurrence, each at its own time, none folded
// (a fold would have put all of them at the newest one's time). The histogram
// still counts each line at its own time.
func TestReadLogsSearchShowsEachOccurrenceWhereItWasWritten(t *testing.T) {
	e := readEnv(t)
	var lines []string
	for i := 0; i < 5; i++ {
		for j := 0; j < 9; j++ {
			lines = append(lines, fmt.Sprintf("2026-10-25T00:%02d:%02dZ tick %d-%d", 10+i, j, i, j))
		}
		lines = append(lines, fmt.Sprintf(`{"time":"2026-10-25T00:%02d:30Z","level":"error","msg":"boom"}`, 10+i))
	}
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, lines, wrote("00:14:40"))

	read := mustRead(t, e, EnvLogQuery{App: "blog", Q: "boom", Range: time.Hour})
	if len(read.Lines) != 5 {
		t.Fatalf("%d rows, want one per occurrence: %q", len(read.Lines), entryTexts(read.Lines))
	}
	seen := map[time.Time]bool{}
	for _, l := range read.Lines {
		if l.Repeat != 0 {
			t.Errorf("row %q is folded (repeat %d)", l.Text, l.Repeat)
		}
		seen[l.Time] = true
	}
	if len(seen) != 5 {
		t.Errorf("the rows are at %d distinct times, want 5", len(seen))
	}
	stamps := 0
	for _, st := range read.stamps {
		stamps++
		if st.Level != logline.Error {
			t.Errorf("a stamp of level %s under q=boom", st.Level)
		}
	}
	if stamps != 5 {
		t.Errorf("%d stamps, want 5", stamps)
	}
}

// Equal lines that are adjacent in the stream fold under a search or a levels
// filter that matches them; a line of another level between them keeps them
// apart even when the levels filter drops that line.
func TestReadLogsAdjacentEqualLinesFoldUnderAFilter(t *testing.T) {
	e := readEnv(t)
	const bad = `time=2026-10-25T00:30:0%dZ level=ERROR msg="upstream down"`
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{
		fmt.Sprintf(bad, 1), fmt.Sprintf(bad, 2), fmt.Sprintf(bad, 3),
		"time=2026-10-25T00:30:04Z level=INFO msg=ok",
		fmt.Sprintf(bad, 5), fmt.Sprintf(bad, 6),
	}, wrote("00:30:10"))

	for name, q := range map[string]EnvLogQuery{
		"a search":     {App: "blog", Q: "upstream"},
		"the levels":   {App: "blog", Levels: []logline.Level{logline.Error}},
		"the unfilter": {App: "blog", Levels: []logline.Level{logline.Error, logline.Info}},
	} {
		read := mustRead(t, e, q)
		var bad []LogEntry
		for _, l := range read.Lines {
			if l.Level == logline.Error {
				bad = append(bad, l)
			}
		}
		// Newest first: the run of two (×2), then the run of three (×3).
		if len(bad) != 2 || bad[0].Repeat != 1 || bad[1].Repeat != 2 {
			t.Errorf("%s: rows = %q repeats %d, %d, want ×2 then ×3", name, entryTexts(bad), repeatOf(bad, 0), repeatOf(bad, 1))
		}
	}
}

func repeatOf(rows []LogEntry, i int) int {
	if i >= len(rows) {
		return -1
	}
	return rows[i].Repeat
}

func TestReadLogsLevels(t *testing.T) {
	e := readEnv(t)
	const (
		lvlD = "time=2026-10-25T00:30:01Z level=DEBUG msg=d"
		lvlI = "time=2026-10-25T00:30:02Z level=INFO msg=i"
		lvlW = "time=2026-10-25T00:30:03Z level=WARN msg=w"
		lvlE = "time=2026-10-25T00:30:04Z level=ERROR msg=e"
	)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{lvlD, lvlI, lvlW, lvlE}, wrote("00:30:10"))
	for _, tc := range []struct {
		name   string
		levels []logline.Level
		want   []string
	}{
		{"default is error, warn and info", nil, []string{lvlE, lvlW, lvlI}},
		{"errors", []logline.Level{logline.Error}, []string{lvlE}},
		{"debug", []logline.Level{logline.Debug}, []string{lvlD}},
		{"all four", []logline.Level{logline.Debug, logline.Info, logline.Warn, logline.Error}, []string{lvlE, lvlW, lvlI, lvlD}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := mustRead(t, e, EnvLogQuery{App: "blog", Levels: tc.levels})
			if got := entryTexts(read.Lines); !slices.Equal(got, tc.want) {
				t.Errorf("rows = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadLogsCutsTheCopiesBeforeReading(t *testing.T) {
	setup := func() *env {
		e := readEnv(t)
		for _, id := range []string{copyBlogWeb, copyBlogWorker, copyDocsWeb} {
			e.nomad.AddLogFile(id, "stdout", 0, []string{"2026-10-25T00:30:00Z from " + id[:4]}, wrote("00:30:10"))
			e.nomad.AddLogFile(id, "stderr", 0, []string{"2026-10-25T00:31:00Z err " + id[:4]}, wrote("00:31:10"))
		}
		return e
	}
	readCopies := func(e *env) []string {
		seen := map[string]bool{}
		for _, r := range e.nomad.FileRequests() {
			seen[r.Copy] = true
		}
		return slices.Sorted(func(yield func(string) bool) {
			for c := range seen {
				if !yield(c[:4]) {
					return
				}
			}
		})
	}
	for _, tc := range []struct {
		name  string
		q     EnvLogQuery
		reads []string // copies whose files were asked for
		rows  int
	}{
		{"everything", EnvLogQuery{}, []string{"9a4d", "b7c2", "c3e5"}, 6},
		{"an app", EnvLogQuery{App: "docs"}, []string{"c3e5"}, 2},
		{"a service", EnvLogQuery{Service: "blog/worker"}, []string{"b7c2"}, 2},
		{"a service with its app", EnvLogQuery{App: "blog", Service: "blog/web"}, []string{"9a4d"}, 2},
		{"a copy by its first 8", EnvLogQuery{Copy: "b7c20e11"}, []string{"b7c2"}, 2},
		{"a copy by its id", EnvLogQuery{Copy: copyDocsWeb}, []string{"c3e5"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup()
			tc.q.Range = time.Hour
			read := mustRead(t, e, tc.q)
			if got := readCopies(e); !slices.Equal(got, tc.reads) {
				t.Errorf("files of copies %v were asked for, want %v", got, tc.reads)
			}
			if len(read.Lines) != tc.rows {
				t.Errorf("%d rows, want %d", len(read.Lines), tc.rows)
			}
			if len(read.Copies) != len(tc.reads) {
				t.Errorf("%d copies in the rail, want %d", len(read.Copies), len(tc.reads))
			}
		})
	}
}

func TestReadLogsUnknownThings(t *testing.T) {
	e := readEnv(t)
	ctx := context.Background()

	_, err := e.svc.ReadLogs(ctx, EnvLogQuery{App: "nope"})
	if !errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrLogCopyNotFound) {
		t.Errorf("unknown app: %v, want store.ErrNotFound (and not the copy's)", err)
	}
	_, err = e.svc.ReadLogs(ctx, EnvLogQuery{Copy: "ffffffff"})
	if !errors.Is(err, ErrLogCopyNotFound) || !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown copy: %v, want ErrLogCopyNotFound", err)
	}
	// A copy of another app than the one asked for isn't found either.
	_, err = e.svc.ReadLogs(ctx, EnvLogQuery{App: "docs", Copy: copyBlogWeb})
	if !errors.Is(err, ErrLogCopyNotFound) {
		t.Errorf("copy of another app: %v, want ErrLogCopyNotFound", err)
	}

	for _, tc := range []struct {
		name string
		q    EnvLogQuery
	}{
		{"service the app lacks", EnvLogQuery{Service: "blog/cache"}},
		{"service of an app that isn't there", EnvLogQuery{Service: "nope/web"}},
		{"service of another app than app", EnvLogQuery{App: "docs", Service: "blog/web"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.ReadLogs(ctx, tc.q)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != "service" {
				t.Errorf("error = %v, want a field error on service", err)
			}
		})
	}
}

func TestReadLogsPlaceErrors(t *testing.T) {
	e := readEnv(t)
	_, err := e.svc.ReadLogs(context.Background(), EnvLogQuery{Project: "nope", Environment: "production"})
	var pe *PlaceError
	if !errors.As(err, &pe) || pe.Field != "project" || !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("unknown project: %v, want a *PlaceError on project", err)
	}
	_, err = e.svc.ReadLogs(context.Background(), EnvLogQuery{Project: "default", Environment: "qa"})
	if !errors.As(err, &pe) || pe.Field != "environment" || !errors.Is(err, ErrEnvironmentNotFound) {
		t.Errorf("unknown environment: %v, want a *PlaceError on environment", err)
	}
	// Both empty is Default › production.
	if _, err := e.svc.ReadLogs(context.Background(), EnvLogQuery{}); err != nil {
		t.Errorf("the default place: %v", err)
	}
}

func TestEnvLogQueryValidate(t *testing.T) {
	long := strings.Repeat("a", logMaxQuery+1)
	t0 := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		q     EnvLogQuery
		field string // "" is valid
	}{
		{"empty", EnvLogQuery{}, ""},
		{"everything right", EnvLogQuery{Stream: "both", Levels: []logline.Level{logline.Debug}, Q: "x", Range: time.Hour, Lines: 1000, Service: "a/b"}, ""},
		{"from and to", EnvLogQuery{From: t0, To: t0.Add(time.Hour)}, ""},
		{"stream", EnvLogQuery{Stream: "stdin"}, "stream"},
		{"level", EnvLogQuery{Levels: []logline.Level{"fatal"}}, "levels"},
		{"q of 200 bytes", EnvLogQuery{Q: long[:logMaxQuery]}, ""},
		{"q too long", EnvLogQuery{Q: long}, "q"},
		{"service without a slash", EnvLogQuery{Service: "web"}, "service"},
		{"service with an empty part", EnvLogQuery{Service: "web/"}, "service"},
		{"service with two slashes", EnvLogQuery{Service: "a/b/c"}, "service"},
		{"range not a preset", EnvLogQuery{Range: 30 * time.Minute}, "range"},
		{"range and from", EnvLogQuery{Range: time.Hour, From: t0}, "range"},
		{"range and to", EnvLogQuery{Range: time.Hour, To: t0}, "range"},
		{"from after to", EnvLogQuery{From: t0.Add(time.Hour), To: t0}, "from"},
		{"lines negative", EnvLogQuery{Lines: -1}, "lines"},
		{"lines too many", EnvLogQuery{Lines: 1001}, "lines"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.q.Validate()
			if tc.field == "" {
				if err != nil {
					t.Errorf("Validate = %v, want nil", err)
				}
				return
			}
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Errorf("Validate = %v, want a field error on %s", err, tc.field)
			}
		})
	}
	// ReadLogs applies it, and the window's own checks.
	e := readEnv(t)
	for name, q := range map[string]EnvLogQuery{
		"before at the start": {Range: time.Hour, Before: readNow.Add(-time.Hour)},
		"before the start":    {Range: time.Hour, Before: readNow.Add(-2 * time.Hour)},
		"service":             {Service: "web"},
	} {
		if _, err := e.svc.ReadLogs(context.Background(), q); err == nil {
			t.Errorf("%s: ReadLogs accepted the query", name)
		} else if fe := new(app.FieldError); !errors.As(err, &fe) {
			t.Errorf("%s: %v is not a field error", name, err)
		}
	}
}

func TestReadLogsListsAnEndedCopyAsGone(t *testing.T) {
	e := readEnv(t)
	e.nomad.SetCopies("blog",
		copyStub(copyBlogWeb, "web", "running", readStart),
		copyStub(copyBlogOld, "web", "complete", readStart))
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"2026-10-25T00:30:00Z new"}, wrote("00:30:10"))
	e.nomad.AddLogFile(copyBlogOld, "stdout", 0, []string{"2026-10-25T00:20:00Z old"}, wrote("00:20:30"))

	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: time.Hour})
	if got := entryTexts(read.Lines); !slices.Equal(got, []string{"2026-10-25T00:30:00Z new", "2026-10-25T00:20:00Z old"}) {
		t.Errorf("rows = %q, want the ended copy's line too", got)
	}
	gone := map[string]bool{}
	for _, c := range read.Copies {
		gone[c.Short] = c.Gone
		if c.Lines != 1 || c.App != "blog" || c.Service != "web" || len(c.Short) != 8 || !strings.HasPrefix(c.ID, c.Short) {
			t.Errorf("rail entry %+v", c)
		}
	}
	if len(gone) != 2 || gone["d4f6b1ea"] != true || gone["9a4d13ff"] != false {
		t.Errorf("gone = %v, want only d4f6b1ea", gone)
	}
}

func TestReadLogsDropped(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 3, []string{"2026-10-25T00:30:00Z only the fourth file is left"}, wrote("00:30:10"))
	read := mustRead(t, e, EnvLogQuery{App: "blog"})
	if !read.Dropped || !read.Coverage.Complete {
		t.Errorf("dropped %v, coverage %+v: files 0 to 2 were purged, so older lines are gone, but the read is complete", read.Dropped, read.Coverage)
	}
	if len(read.Lines) != 1 || read.Lines[0].ID.File != 3 {
		t.Errorf("rows = %v", read.Lines)
	}
}

// A file purged between the listing and its read ends the stream with what it
// has: it isn't an error.
func TestWalkLogStreamPurgedFile(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"2026-10-25T00:10:00Z zero"}, readTime(t, "2026-10-25T00:11:00Z"))
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 1, []string{"2026-10-25T00:20:00Z one"}, wrote("00:20:10"))
	ctx := context.Background()
	files, err := e.svc.deployer.LogFiles(ctx, copyBlogWeb, "web")
	if err != nil || len(files) != 2 {
		t.Fatalf("LogFiles = %v, %v", files, err)
	}
	e.nomad.PurgeLogFile(copyBlogWeb, "stdout", 0)

	res, err := e.svc.walkLogStream(ctx, deploy.Copy{ID: copyBlogWeb, Service: "web"}, files, walkLimits{budget: logStreamBudget})
	if err != nil {
		t.Fatalf("walkLogStream: %v", err)
	}
	if !res.dropped || len(res.raws) != 1 || res.raws[0].text != "2026-10-25T00:20:00Z one" {
		t.Errorf("result = %+v, want file 1's line and dropped", res)
	}
}

func TestReadLogsDeadlineStopsTheRead(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"2026-10-25T00:30:00Z x"}, wrote("00:30:10"))
	e.svc.logDeadline = time.Nanosecond

	read, err := e.svc.ReadLogs(context.Background(), EnvLogQuery{App: "blog"})
	if err != nil {
		t.Fatalf("ReadLogs: %v, want the read it got to", err)
	}
	if read.Coverage.Timed || read.Coverage.Complete {
		t.Errorf("coverage = %+v, want not timed and not complete", read.Coverage)
	}
}

func TestReadLogsWaitsForItsTurn(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"2026-10-25T00:30:00Z x"}, wrote("00:30:10"))
	// Two reads are going: take both places.
	for i := 0; i < logReadsAtOnce; i++ {
		e.svc.logSem <- struct{}{}
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.ReadLogs(context.Background(), EnvLogQuery{App: "blog"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the third read ran while two were going (err %v)", err)
	case <-time.After(100 * time.Millisecond):
	}

	// One finishes: the waiting read goes.
	<-e.svc.logSem
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ReadLogs: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read didn't start when a place was free")
	}

	// A read that waits gives up with its context.
	ctx, cancel := context.WithCancel(context.Background())
	e.svc.logSem <- struct{}{}
	go func() {
		_, err := e.svc.ReadLogs(ctx, EnvLogQuery{App: "blog"})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a waiting read ended with %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a waiting read ignored its context")
	}
	// Both places are free again at the end.
	<-e.svc.logSem
	<-e.svc.logSem
}

func TestReadLogsNomadDown(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"2026-10-25T00:30:00Z " + sentinelRead}, wrote("00:30:10"))

	e.nomad.FailFS(403)
	_, err := e.svc.ReadLogs(context.Background(), EnvLogQuery{App: "blog"})
	if !errors.Is(err, deploy.ErrNomadUnavailable) {
		t.Errorf("a token without read-fs: %v, want ErrNomadUnavailable", err)
	}
	e.nomad.FailFS(0)

	e.nomad.Down()
	_, err = e.svc.ReadLogs(context.Background(), EnvLogQuery{App: "blog"})
	if !errors.Is(err, deploy.ErrNomadUnavailable) {
		t.Errorf("Nomad down: %v, want ErrNomadUnavailable", err)
	}
}

// Nothing the app printed reaches an error or the server's log.
func TestReadLogsKeepsLinesOutOfErrorsAndLogs(t *testing.T) {
	e := readEnv(t)
	logs := &lockedBuffer{}
	svc := startServiceLogging(t, e.store, e.svc.deployer, e.secrets, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	svc.clock.(*fakeClock).Set(readNow)
	lines := []string{
		"2026-10-25T00:30:00Z ERROR " + sentinelRead,
		sentinelRead + " with no time",
		`{"level":"error","msg":"` + sentinelRead + `"}`,
	}
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, lines, wrote("00:30:10"))
	e.nomad.AddLogFile(copyBlogWeb, "stderr", 0, lines, wrote("00:30:10"))

	var errs []error
	_, err := svc.ReadLogs(context.Background(), EnvLogQuery{App: "blog", Q: sentinelRead})
	errs = append(errs, err)
	e.nomad.FailFS(403)
	_, err = svc.ReadLogs(context.Background(), EnvLogQuery{App: "blog"})
	errs = append(errs, err)
	e.nomad.FailFS(0)
	_, err = svc.ReadLogs(context.Background(), EnvLogQuery{App: "blog", Q: strings.Repeat(sentinelRead, 10)})
	errs = append(errs, err)
	for i, err := range errs {
		if err != nil && strings.Contains(err.Error(), sentinelRead) {
			t.Errorf("error %d holds a line: %v", i, err)
		}
	}
	if errs[0] != nil {
		t.Errorf("the first read failed: %v", errs[0])
	}
	if strings.Contains(logs.String(), sentinelRead) {
		t.Errorf("the server's log holds a line: %s", logs.String())
	}

	// Nomad's message about a body of the wrong length may be file content:
	// the service keeps it out.
	msg := fmt.Errorf("%w: read output of copy x: Nomad answered: %s", deploy.ErrNomadUnavailable, sentinelRead)
	if got := scrubReadError(msg); strings.Contains(got.Error(), sentinelRead) || !errors.Is(got, deploy.ErrNomadUnavailable) {
		t.Errorf("scrubReadError = %v", got)
	}
	plain := fmt.Errorf("%w: Nomad refused the token (read output of copy x); its policy needs the read-fs capability", deploy.ErrNomadUnavailable)
	if got := scrubReadError(plain); got.Error() != plain.Error() {
		t.Errorf("scrubReadError changed a message that holds no content: %v", got)
	}
}

// A runaway line with no newline is cut at 64 KiB into pieces that each know
// their offset; a CR is dropped; invalid UTF-8 is replaced.
func TestSplitRawLines(t *testing.T) {
	long := strings.Repeat("a", logMaxLine+10)
	got := splitRawLines([]byte("one\r\ntwo\n\nbad \xff byte\n"+long+"\nlast"), 100, 2, false)
	type want struct {
		text   string
		offset int64
	}
	wants := []want{{"one", 100}, {"two", 105}, {"", 109}, {"bad � byte", 110}, {long[:logMaxLine], 121}, {long[logMaxLine:], 121 + logMaxLine}, {"last", 121 + int64(len(long)) + 1}}
	if len(got) != len(wants) {
		t.Fatalf("%d lines, want %d", len(got), len(wants))
	}
	for i, w := range wants {
		if got[i].text != w.text || got[i].offset != w.offset || got[i].file != 2 {
			t.Errorf("line %d = %q at %d, want %q at %d", i, truncate(got[i].text), got[i].offset, truncate(w.text), w.offset)
		}
	}
	// The tail after the last newline: nothing when the segment ended on a
	// newline, an empty line when its end is known to be a line's end.
	if n := len(splitRawLines([]byte("a\n"), 0, 0, false)); n != 1 {
		t.Errorf("'a\\n' = %d lines, want 1", n)
	}
	if n := len(splitRawLines([]byte("a\n"), 0, 0, true)); n != 2 {
		t.Errorf("'a\\n' with a known end = %d lines, want 2", n)
	}
	if n := len(splitRawLines(nil, 0, 0, false)); n != 0 {
		t.Errorf("nothing = %d lines, want 0", n)
	}
}

func truncate(s string) string {
	if len(s) > 20 {
		return s[:20] + "..."
	}
	return s
}

func TestMergeNewestFirst(t *testing.T) {
	at := func(s string) time.Time { return readTime(t, "2026-10-25T00:00:"+s+"Z") }
	row := func(text, sec string) LogEntry {
		e := LogEntry{sort: at(sec)}
		e.Text = text
		return e
	}
	a := []LogEntry{row("a1", "01"), row("a2", "03"), row("a3", "03")}
	b := []LogEntry{row("b1", "02"), row("b2", "03")}
	got := entryTexts(mergeNewestFirst([][]LogEntry{a, b}, 0))
	// Equal times: the earlier stream first; a stream's own order is kept, newest first.
	if want := []string{"a3", "a2", "b2", "b1", "a1"}; !slices.Equal(got, want) {
		t.Errorf("merge = %q, want %q", got, want)
	}
	if got := entryTexts(mergeNewestFirst([][]LogEntry{a, b}, 2)); !slices.Equal(got, []string{"a3", "a2"}) {
		t.Errorf("merge of 2 = %q", got)
	}
	if got := mergeNewestFirst(nil, 5); len(got) != 0 {
		t.Errorf("merge of nothing = %v", got)
	}
}
