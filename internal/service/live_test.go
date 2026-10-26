package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/logline"
)

const sentinelLive = "s3cret-sentinel-024-live"

// notice is a ChangeLogs notice and when it arrived.
type notice struct {
	appID string
	at    time.Time
}

// collectNotices records every ChangeLogs notice, and every other kind it sees as "other:<kind>".
type noticeLog struct {
	mu   sync.Mutex
	logs []notice
	more []string
}

func collectNotices(t *testing.T, svc *Service) *noticeLog {
	t.Helper()
	ch, unsubscribe := svc.Subscribe("")
	t.Cleanup(unsubscribe)
	n := &noticeLog{}
	go func() {
		for c := range ch {
			n.mu.Lock()
			if c.What == ChangeLogs {
				n.logs = append(n.logs, notice{c.AppID, time.Now()})
			} else {
				n.more = append(n.more, string(c.What))
			}
			n.mu.Unlock()
		}
	}()
	return n
}

func (n *noticeLog) snapshot() []notice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notice(nil), n.logs...)
}

func (n *noticeLog) wait(t *testing.T, count int) []notice {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := n.snapshot(); len(got) >= count {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d logs notices within 5s, want %d", len(n.snapshot()), count)
	return nil
}

func waitStreams(t *testing.T, e *env, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.nomad.OpenLogStreams() != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := e.nomad.OpenLogStreams(); got != want {
		t.Fatalf("%d log streams open, want %d", got, want)
	}
}

// liveEnv is readEnv with a service that logs into the returned buffer.
func liveEnv(t *testing.T) (*env, *lockedBuffer) {
	t.Helper()
	e := readEnv(t)
	logs := &lockedBuffer{}
	e.svc = startServiceLogging(t, e.store, e.svc.deployer, e.secrets, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e.svc.clock.(*fakeClock).Set(readNow)
	return e, logs
}

// A watched place follows both streams of every running copy and tells the pages which app wrote;
// nothing of the line is in the notice or the log, and the followers end with the watch.
func TestWatchLogsNoticeOnNewLine(t *testing.T) {
	e, logs := liveEnv(t)
	e.svc.SetLogWatchGaps(50*time.Millisecond, time.Hour)
	notices := collectNotices(t, e.svc)

	done := e.svc.WatchLogs("", "")
	waitStreams(t, e, 6) // three copies, two streams each
	if got := notices.snapshot(); len(got) != 0 {
		t.Errorf("notices before any line: %v", got)
	}

	e.nomad.WriteLog(copyBlogWeb, "stdout", "ERROR "+sentinelLive+"\n")
	got := notices.wait(t, 1)
	if got[0].appID != "blog" {
		t.Errorf("notice for %q, want blog", got[0].appID)
	}
	e.nomad.WriteLog(copyDocsWeb, "stderr", sentinelLive+" again\n")
	got = notices.wait(t, 2)
	if got[1].appID != "docs" {
		t.Errorf("second notice for %q, want docs", got[1].appID)
	}

	done()
	done() // safe to call again
	waitStreams(t, e, 0)
	e.nomad.WriteLog(copyBlogWeb, "stdout", "after the watch\n")
	time.Sleep(200 * time.Millisecond)
	if n := len(notices.snapshot()); n != 2 {
		t.Errorf("%d notices after the watch ended, want none more (2 in all)", n)
	}
	if strings.Contains(logs.String(), sentinelLive) {
		t.Errorf("the server's log holds a line: %s", logs.String())
	}
	notices.mu.Lock()
	defer notices.mu.Unlock()
	if len(notices.more) != 0 {
		t.Errorf("other notices: %v", notices.more)
	}
}

// A burst of lines is one notice per gap: the first goes at once, the rest wait and go together.
func TestWatchLogsDebounce(t *testing.T) {
	e, _ := liveEnv(t)
	const gap = 300 * time.Millisecond
	e.svc.SetLogWatchGaps(gap, time.Hour)
	notices := collectNotices(t, e.svc)
	done := e.svc.WatchLogs("", "")
	defer done()
	waitStreams(t, e, 6)

	start := time.Now()
	for i := 0; time.Since(start) < 1100*time.Millisecond; i++ {
		e.nomad.WriteLog(copyBlogWeb, "stdout", fmt.Sprintf("line %d\n", i))
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(gap + 100*time.Millisecond) // the one waiting at the end
	got := notices.snapshot()
	if len(got) < 3 || len(got) > 5 {
		t.Fatalf("%d notices for a burst of about 1.1 s at a %v gap, want 3 to 5: %v", len(got), gap, got)
	}
	if first := got[0].at.Sub(start); first > 150*time.Millisecond {
		t.Errorf("the first notice came after %v, want at once", first)
	}
	for i := 1; i < len(got); i++ {
		if d := got[i].at.Sub(got[i-1].at); d < gap-40*time.Millisecond {
			t.Errorf("notices %d and %d are %v apart, want at least about %v", i-1, i, d, gap)
		}
	}
}

// Pages of one place share the followers, which end when the last page leaves; the copies are
// listed again and a copy that started after the watch is followed, one that ended is let go.
func TestWatchLogsSharedAndRelisted(t *testing.T) {
	e, _ := liveEnv(t)
	e.svc.SetLogWatchGaps(20*time.Millisecond, 100*time.Millisecond)
	notices := collectNotices(t, e.svc)

	first := e.svc.WatchLogs("default", "production")
	second := e.svc.WatchLogs("", "") // the same place by its default names
	waitStreams(t, e, 6)
	first()
	time.Sleep(100 * time.Millisecond)
	if n := e.nomad.OpenLogStreams(); n != 6 {
		t.Fatalf("%d streams open with one page left, want 6", n)
	}

	// A new copy of docs is followed from the next listing on.
	const newCopy = "e5a7c3b1-0000-4000-8000-000000000005"
	e.nomad.SetCopies("docs",
		copyStub(copyDocsWeb, "web", "running", readStart),
		api.AllocationListStub{ID: newCopy, TaskGroup: "web", JobVersion: 3, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {StartedAt: readNow}}})
	waitStreams(t, e, 8)
	e.nomad.WriteLog(newCopy, "stdout", "hello from the new copy\n")
	got := notices.wait(t, 1)
	if got[0].appID != "docs" {
		t.Errorf("notice for %q, want docs", got[0].appID)
	}

	// A copy that ended is let go.
	e.nomad.SetCopies("blog", copyStub(copyBlogWeb, "web", "running", readStart),
		api.AllocationListStub{ID: copyBlogWorker, TaskGroup: "worker", JobVersion: 3, ClientStatus: "complete",
			TaskStates: map[string]*api.TaskState{"worker": {StartedAt: readStart, FinishedAt: readNow}}})
	waitStreams(t, e, 6)

	second()
	waitStreams(t, e, 0)

	// Watching again starts again.
	again := e.svc.WatchLogs("", "")
	waitStreams(t, e, 6)
	again()
	waitStreams(t, e, 0)
}

// A place that doesn't exist has nothing to follow, and its pages leave cleanly.
func TestWatchLogsUnknownPlace(t *testing.T) {
	e, logs := liveEnv(t)
	e.svc.SetLogWatchGaps(20*time.Millisecond, 50*time.Millisecond)
	done := e.svc.WatchLogs("nope", "nowhere")
	time.Sleep(150 * time.Millisecond)
	done()
	if n := e.nomad.OpenLogStreams(); n != 0 {
		t.Errorf("%d streams open", n)
	}
	if !strings.Contains(logs.String(), "list copies to follow logs") {
		t.Errorf("the failure to list wasn't logged: %q", logs.String())
	}
}

// Following the per-app tail keeps a line out of the log too.
func TestLogsKeepLinesOutOfTheLog(t *testing.T) {
	e, logs := liveEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{"2026-10-25T00:30:00Z ERROR " + sentinelLive}, wrote("00:30:10"))
	out := make(chan LogLine, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.svc.Logs(ctx, "blog", LogQuery{Copy: copyBlogWeb, Lines: 10}, out); err != nil {
		t.Fatal(err)
	}
	if l := <-out; !strings.Contains(l.Text, sentinelLive) || l.Level != logline.Error {
		t.Errorf("tail line = %+v", l)
	}
	e.nomad.FailFS(403)
	err := e.svc.Logs(ctx, "blog", LogQuery{Copy: copyBlogWeb, Lines: 10}, out)
	e.nomad.FailFS(0)
	if err == nil || !errors.Is(err, deploy.ErrNomadUnavailable) || strings.Contains(err.Error(), sentinelLive) {
		t.Errorf("a refused listing = %v, want ErrNomadUnavailable without a line", err)
	}
	if strings.Contains(logs.String(), sentinelLive) {
		t.Errorf("the server's log holds a line: %s", logs.String())
	}
}
