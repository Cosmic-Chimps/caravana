package ui

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Download (feature 024, T016): /logs/download is the page's filter as a text file, oldest first.

// lgBlockingWriter holds a download open: its first write says so and then waits to be let go.
type lgBlockingWriter struct {
	wrote   chan struct{}
	release chan struct{}
	once    bool
}

func (b *lgBlockingWriter) Write(p []byte) (int, error) {
	if !b.once {
		b.once = true
		close(b.wrote)
		<-b.release
	}
	return len(p), nil
}

func TestLogsDownload(t *testing.T) {
	e := logsPageEnv(t)
	e.Nomad.AddLogFile(lgDocs, "stdout", 0, []string{e.ago(150*time.Second) + " INFO docs ready", e.ago(90*time.Second) + " ERROR docs broke",
		e.ago(80*time.Second) + " INFO \x1b]8;;https://evil.example\x07" + lgSecret + "\x1b]8;;\x07 \x1b[1mbold\x1b[0m"}, e.wrote(80*time.Second))

	// The footer's link is the page's filter, without the row, the paging or Live.
	page := e.pageOf(t, "/logs?service=blog%2Fweb&q=retry&live=0&open="+e.lineID(5))
	lgWant(t, "the footer", page, `href="/logs/download?q=retry&amp;service=blog%2Fweb" class="cv-btn cv-btn-secondary cv-btn-sm"`)

	resp, body := e.get(t, "/logs/download?levels=error%2Cwarn%2Cinfo%2Cdebug")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /logs/download = %d:\n%s", resp.StatusCode, body)
	}
	h := resp.Header
	if got := h.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := h.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	disp := regexp.MustCompile(`^attachment; filename=caravana-logs-default-production-\d{8}T\d{6}Z-\d{8}T\d{6}Z\.txt$`)
	if got := h.Get("Content-Disposition"); !disp.MatchString(got) {
		t.Errorf("Content-Disposition = %q, want an attachment named by place and range", got)
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 3 || lines[0] != "# newest last" || !strings.HasPrefix(lines[1], "# caravana logs default/production ") ||
		!strings.Contains(lines[1], "levels error,warn,info,debug") {
		t.Fatalf("the file starts:\n%s", strings.Join(lines[:min(len(lines), 3)], "\n"))
	}
	// Oldest first, every app of the place, the deploys as comment lines, and no escape byte.
	for _, row := range []string{`\tblog/web\taaaa1111\tstdout\terror\t[^\n]*connection refused to db\n`, `\tdocs/web\tdddd4444\tstdout\terror\t[^\n]*docs broke\n`,
		`\n# deploy [^\n]* blog v8 failed its health checks · rolled back to v7`} {
		if !regexp.MustCompile(row).MatchString(body) {
			t.Errorf("the file has no row like %s:\n%s", row, body)
		}
	}
	lgWant(t, "the file", body, lgSecret, " bold")
	if i, j := strings.Index(body, "listening on :8080"), strings.Index(body, "web b up"); i < 0 || j < i {
		t.Error("the file isn't oldest first")
	}
	lgNot(t, "the file", body, "\x1b", "\x07", "[31m", "evil.example")
	if strings.Contains(e.Logs.String(), lgSecret) {
		t.Error("a downloaded line reached the server's log")
	}

	// The filter narrows the file; the page's paging, open row, around view and Live are ignored.
	resp, narrow := e.get(t, "/logs/download?service=docs%2Fweb&live=0&open=nope&at=nope&before=yesterday")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a download with the page's other values = %d:\n%s", resp.StatusCode, narrow)
	}
	lgWant(t, "the narrowed file", narrow, "docs broke", "service web")
	lgNot(t, "the narrowed file", narrow, "blog/web", "listening on")

	// What the page refuses, the download refuses as a page, not a file.
	for path, want := range map[string]int{"/logs/download?range=2d": 400, "/logs/download?project=nope": 404, "/logs/download?app=nope": 404,
		"/logs/download?copy=ffff9999": 404} {
		resp, body := e.get(t, path)
		if resp.StatusCode != want || resp.Header.Get("Content-Disposition") != "" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s = %d (disposition %q, cache %q), want a %d page", path, resp.StatusCode,
				resp.Header.Get("Content-Disposition"), resp.Header.Get("Cache-Control"), want)
		}
		lgNot(t, path, body, "# newest last")
	}
	// It needs a session.
	if anon, _ := e.do(t, request{path: "/logs/download"}); anon.StatusCode != http.StatusSeeOther {
		t.Errorf("a download without a session = %d, want 303 to sign in", anon.StatusCode)
	}
}

func TestLogsDownloadOneAtATime(t *testing.T) {
	e := logsPageEnv(t)
	bw := &lgBlockingWriter{wrote: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- e.Svc.DownloadLogs(t.Context(), service.EnvLogQuery{}, bw) }()
	<-bw.wrote // the first download has read its streams and is writing

	resp, body := e.get(t, "/logs/download")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("a second download = %d, want 409", resp.StatusCode)
	}
	lgWant(t, "the busy page", text(body), "A download is already running; try again in a moment.")
	if resp.Header.Get("Content-Disposition") != "" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the busy page's headers = %v", resp.Header)
	}
	close(bw.release)
	if err := <-done; err != nil {
		t.Errorf("the first download: %v", err)
	}
	if resp, _ := e.get(t, "/logs/download"); resp.StatusCode != http.StatusOK {
		t.Errorf("a download after it ended = %d", resp.StatusCode)
	}
}

func TestLogsDownloadNomadUnreachable(t *testing.T) {
	e := logsPageEnv(t)
	e.Nomad.Down()
	resp, body := e.get(t, "/logs/download")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("a download with Nomad down = %d, want 502", resp.StatusCode)
	}
	lgWant(t, "the page", text(body), "Nomad can't be reached right now, so logs can't be read.")
	if resp.Header.Get("Content-Disposition") != "" {
		t.Error("a refused download still has a file name")
	}
}
