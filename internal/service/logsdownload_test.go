package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/logline"
)

// downloadFixture writes the lines a download test reads: the blog's web copy
// has a folded run, a line with no time, a tab and escape sequences in a line;
// the worker copy has an error on stderr.
func downloadFixture(e *env) {
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{
		`2026-10-25T00:10:00Z first`,
		`2026-10-25T00:12:00Z GET /health 200`,
		`2026-10-25T00:12:05Z GET /health 200`,
		`2026-10-25T00:12:10Z GET /health 200`,
		`no time here`,
		"2026-10-25T00:20:00Z a\tb",
		"2026-10-25T00:21:00Z \x1b[31mred\x1b[0m \x1b]0;title\x07done\x00",
	}, wrote("00:21:30"))
	e.nomad.AddLogFile(copyBlogWorker, "stderr", 0, []string{
		`2026-10-25T00:11:00Z ERROR boom`,
	}, wrote("00:11:30"))
}

func download(t *testing.T, e *env, q EnvLogQuery) string {
	t.Helper()
	var buf bytes.Buffer
	if err := e.svc.DownloadLogs(context.Background(), q, &buf); err != nil {
		t.Fatalf("DownloadLogs: %v", err)
	}
	return buf.String()
}

// The file: two header lines, then every row oldest first as tab-separated
// fields, an estimated time with a trailing "~", a folded row written out as
// many times as it stands for, tabs and control sequences gone from the text.
func TestDownloadLogsFormat(t *testing.T) {
	e := readEnv(t)
	downloadFixture(e)

	got := download(t, e, EnvLogQuery{App: "blog", Range: 24 * time.Hour})
	want := strings.Join([]string{
		"# newest last",
		"# caravana logs default/production 2026-10-24T00:40:00Z to 2026-10-25T00:40:00Z, read from 2026-10-24T00:40:00Z (app blog, levels error,warn,info)",
		"2026-10-25T00:10:00.000Z\tblog/web\t9a4d13ff\tstdout\tinfo\t2026-10-25T00:10:00Z first",
		"2026-10-25T00:11:00.000Z\tblog/worker\tb7c20e11\tstderr\terror\t2026-10-25T00:11:00Z ERROR boom",
		"2026-10-25T00:12:10.000Z\tblog/web\t9a4d13ff\tstdout\tinfo\t2026-10-25T00:12:10Z GET /health 200",
		"2026-10-25T00:12:10.000Z\tblog/web\t9a4d13ff\tstdout\tinfo\t2026-10-25T00:12:10Z GET /health 200",
		"2026-10-25T00:12:10.000Z\tblog/web\t9a4d13ff\tstdout\tinfo\t2026-10-25T00:12:10Z GET /health 200",
		"2026-10-25T00:12:10.000Z~\tblog/web\t9a4d13ff\tstdout\tinfo\tno time here",
		"2026-10-25T00:20:00.000Z\tblog/web\t9a4d13ff\tstdout\tinfo\t2026-10-25T00:20:00Z a b",
		"2026-10-25T00:21:00.000Z\tblog/web\t9a4d13ff\tstdout\tinfo\t2026-10-25T00:21:00Z red done",
		"",
	}, "\n")
	if got != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}

	// Whatever the app wrote, the file has no control byte but the newlines
	// that end its lines and the tabs that separate its fields.
	for _, b := range []byte(got) {
		if b < 0x20 && b != '\n' && b != '\t' || b == 0x7f {
			t.Fatalf("the file holds the control byte %#x", b)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") && strings.Count(line, "\t") != 5 {
			t.Errorf("row %q has %d tabs, want 5", line, strings.Count(line, "\t"))
		}
	}
}

// The header says what the query kept: the service, the copy, the stream, the levels (debug
// included when asked) and the text, which is quoted so it can't end the line.
func TestDownloadLogsHeaderFilters(t *testing.T) {
	e := readEnv(t)
	downloadFixture(e)

	got := download(t, e, EnvLogQuery{Service: "blog/web", Copy: "9a4d13ff", Stream: "stdout",
		Levels: []logline.Level{"debug", "error"}, Q: "a \x1b[1m\nb", From: wrote("00:00:00"), To: wrote("00:30:00")})
	lines := strings.Split(got, "\n")
	want := "# caravana logs default/production 2026-10-25T00:00:00Z to 2026-10-25T00:30:00Z, read from 2026-10-25T00:00:00Z " +
		`(app blog, service web, copy 9a4d13ff, stream stdout, levels error,debug, q "a \x1b[1m\nb")`
	if lines[1] != want {
		t.Errorf("header = %q\nwant     %q", lines[1], want)
	}
	if len(lines) != 3 || lines[2] != "" { // nothing matches that text
		t.Errorf("file = %q, want the two header lines only", got)
	}
}

// A deploy of the environment is a "# deploy" line between the rows, by time.
func TestDownloadLogsDeployRows(t *testing.T) {
	e := readEnv(t)
	downloadFixture(e)
	d := apptest.Whoami().Deployment
	d.ID, d.Trigger = "dl-deploy-1", app.TriggerManual
	if _, err := e.store.CreateDeployment(context.Background(), "blog", d, nil, nil, ""); err != nil {
		t.Fatal(err)
	}

	// The store stamps a deployment with the real clock, so the range is wide.
	got := download(t, e, EnvLogQuery{App: "blog", From: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)})
	var rows []string
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "# deploy ") {
			rows = append(rows, l)
		}
	}
	if len(rows) != 1 || !strings.HasSuffix(rows[0], " blog v0 started rolling out · through the API") {
		t.Errorf("deploy rows = %q, want one for blog's started deployment", rows)
	}

	// Another app's deploy isn't in a download of one app, and a level or a search never removes one.
	got = download(t, e, EnvLogQuery{App: "docs", Levels: []logline.Level{"error"}, Q: "zzz",
		From: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)})
	if strings.Contains(got, "# deploy") {
		t.Errorf("a download of docs holds blog's deploy: %q", got)
	}
}

func TestDownloadDeployWords(t *testing.T) {
	tests := []struct {
		row  DeployRow
		want string
	}{
		{DeployRow{App: "shop", JobVersion: 24, Kind: DeployStarted, Who: "pushed to main by ana"}, "shop v24 started rolling out · pushed to main by ana"},
		{DeployRow{App: "shop", JobVersion: 24, Kind: DeployLive}, "shop v24 is live"},
		{DeployRow{App: "shop", JobVersion: 25, Kind: DeployReverted, RevertedTo: 24, Who: "by the webhook"}, "shop v25 failed its health checks · rolled back to v24 · by the webhook"},
		{DeployRow{App: "shop", JobVersion: 25, Kind: DeployFailed}, "shop v25 failed its health checks"},
		{DeployRow{App: "shop", JobVersion: 26, Kind: DeploySuperseded, Who: "x\ty\x1b[0m"}, "shop v26 was replaced by a newer deploy · x y"},
	}
	for _, tc := range tests {
		if got := downloadDeployWords(tc.row); got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.row, got, tc.want)
		}
	}
}

// A stream the budget stopped is cut at the older end: the file keeps the newest lines, the
// header's "read from" is where the read reached, and a last line names the copy.
func TestDownloadLogsCut(t *testing.T) {
	e := readEnv(t)
	var lines []string
	for i := range 3000 {
		lines = append(lines, fmt.Sprintf("%s line %04d with padding to make it longer ......", readStart.Add(time.Duration(i)*time.Second).Format(time.RFC3339), i))
	}
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, lines, readStart.Add(3000*time.Second))

	var buf bytes.Buffer
	b := downloadBudgets{stream: logChunk, total: 1 << 30, deadline: time.Minute}
	if err := e.svc.download(context.Background(), EnvLogQuery{App: "blog", From: readStart.Add(-time.Hour), To: readNow}, &buf, b); err != nil {
		t.Fatal(err)
	}
	out := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	rows := out[2 : len(out)-1]
	if len(rows) == 0 || len(rows) >= 3000 {
		t.Fatalf("%d rows, want some but not all 3000", len(rows))
	}
	if !strings.HasSuffix(rows[len(rows)-1], "line 2999 with padding to make it longer ......") {
		t.Errorf("the newest row = %q, want line 2999", rows[len(rows)-1])
	}
	firstAt, _, _ := strings.Cut(rows[0], "\t")
	edge := readTime(t, firstAt)
	cut := out[len(out)-1]
	wantCut := fmt.Sprintf("# cut: the budget ended at %s; older lines of copy 9a4d13ff weren't included", edge.Format(time.RFC3339))
	if cut != wantCut {
		t.Errorf("last line = %q, want %q", cut, wantCut)
	}
	if !strings.Contains(out[1], "read from "+edge.Format(time.RFC3339)+" (") {
		t.Errorf("header = %q, want it to say it read from %s", out[1], edge.Format(time.RFC3339))
	}
	if n := strings.Count(buf.String(), "# cut:"); n != 1 {
		t.Errorf("%d cut lines, want one (only the web copy's stdout was cut)", n)
	}
}

func TestDownloadBudgetsSplit(t *testing.T) {
	b := defaultDownloadBudgets
	for _, tc := range []struct {
		n    int
		want int64
	}{{1, 8 << 20}, {8, 8 << 20}, {16, 4 << 20}, {64, 1 << 20}, {2000, logChunk}} {
		if got := b.each(tc.n); got != tc.want {
			t.Errorf("each(%d) = %d, want %d", tc.n, got, tc.want)
		}
	}
}

// gateWriter holds its first Write until release is closed.
type gateWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	buf     bytes.Buffer
}

func (g *gateWriter) Write(p []byte) (int, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.buf.Write(p)
}

// One download at a time: a second is refused at once, and the slot is free again after the first.
func TestDownloadLogsOneAtATime(t *testing.T) {
	e := readEnv(t)
	downloadFixture(e)
	q := EnvLogQuery{App: "blog", Range: 24 * time.Hour}

	g := &gateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- e.svc.DownloadLogs(context.Background(), q, g) }()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first download never wrote")
	}

	var second bytes.Buffer
	if err := e.svc.DownloadLogs(context.Background(), q, &second); !errors.Is(err, ErrDownloadBusy) {
		t.Errorf("second download = %v, want ErrDownloadBusy", err)
	}
	if second.Len() != 0 {
		t.Errorf("the refused download wrote %q", second.String())
	}
	close(g.release)
	if err := <-done; err != nil {
		t.Fatalf("first download: %v", err)
	}
	if !strings.HasPrefix(g.buf.String(), "# newest last\n") {
		t.Errorf("first download = %q", g.buf.String())
	}
	if got := download(t, e, q); !strings.HasPrefix(got, "# newest last\n") {
		t.Errorf("a download after the first = %q", got)
	}

	// A query that is refused doesn't keep the slot.
	if err := e.svc.DownloadLogs(context.Background(), EnvLogQuery{Stream: "nope"}, &second); err == nil {
		t.Error("a bad stream was accepted")
	}
	download(t, e, q)
}

// The query's errors are ReadLogs'.
func TestDownloadLogsRefusals(t *testing.T) {
	e := readEnv(t)
	var buf bytes.Buffer
	var fe *app.FieldError
	if err := e.svc.DownloadLogs(context.Background(), EnvLogQuery{Stream: "nope"}, &buf); !errors.As(err, &fe) || fe.Field != "stream" {
		t.Errorf("bad stream = %v, want a field error on stream", err)
	}
	var pe *PlaceError
	if err := e.svc.DownloadLogs(context.Background(), EnvLogQuery{Project: "nope"}, &buf); !errors.As(err, &pe) || pe.Field != "project" {
		t.Errorf("unknown project = %v, want a PlaceError on project", err)
	}
	if err := e.svc.DownloadLogs(context.Background(), EnvLogQuery{Copy: "ffffffff"}, &buf); !errors.Is(err, ErrLogCopyNotFound) {
		t.Errorf("unknown copy = %v, want ErrLogCopyNotFound", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a refused download wrote %q", buf.String())
	}
}

// A writer that fails ends the download with its error.
type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }

func TestDownloadLogsWriteError(t *testing.T) {
	e := readEnv(t)
	downloadFixture(e)
	boom := errors.New("client went away")
	if err := e.svc.DownloadLogs(context.Background(), EnvLogQuery{App: "blog", Range: 24 * time.Hour}, failWriter{boom}); !errors.Is(err, boom) {
		t.Errorf("DownloadLogs = %v, want the writer's error", err)
	}
}

func TestLogDownloadName(t *testing.T) {
	from := time.Date(2026, 10, 25, 0, 18, 21, 0, time.UTC)
	to := time.Date(2026, 10, 25, 0, 33, 21, 0, time.FixedZone("x", 3600))
	got := LogDownloadName("shop", "production", from, to)
	if want := "caravana-logs-shop-production-20261025T001821Z-20261024T233321Z.txt"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}
