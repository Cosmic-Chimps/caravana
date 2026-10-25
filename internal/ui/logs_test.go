package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/logline"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 024: /logs, the environment's output read from Nomad, with the sample lines of the
// contract: a timed error, a [warn], a JSON error, an ANSI line, a repeated line, an untimed line.

const (
	lgBlogA   = "aaaa1111-0000-4000-8000-0000000000a1" // blog's web, copy one
	lgBlogB   = "bbbb2222-0000-4000-8000-0000000000b2" // blog's web, copy two
	lgWorker  = "cccc3333-0000-4000-8000-0000000000c3" // blog's worker
	lgDocs    = "dddd4444-0000-4000-8000-0000000000d4" // docs' web
	lgSecret  = "s3cret-sentinel-024-page"
	lgLayout  = "2006-01-02T15:04:05.000Z"
	lgNoStyle = `\sstyle\s*=`
)

// lgEnv is what the page tests read: where each line sits in its file, for the ids.
type lgEnv struct {
	*consoleEnv
	now     time.Time
	blogOff []int64 // the byte offset of each line of blog's first copy
}

// ago is the time d before the env's now, as a log line writes it.
func (e *lgEnv) ago(d time.Duration) string { return e.now.Add(-d).Format(lgLayout) }

// wrote is when a file whose last line is d old was last written: a few seconds after it. A file whose last
// write is more than a minute from its last timed line has its times disbelieved, as Nomad's would.
func (e *lgEnv) wrote(d time.Duration) time.Time { return e.now.Add(-d + 5*time.Second) }

func lgStarted(group string, now time.Time) map[string]*api.TaskState {
	return map[string]*api.TaskState{group: {StartedAt: now.Add(-time.Hour)}}
}

// logsPageEnv has two apps, blog (web with two copies, and a worker) and docs (web), with their
// lines and deploys.
func logsPageEnv(t *testing.T) *lgEnv {
	t.Helper()
	e := &lgEnv{consoleEnv: newConsoleEnv(t), now: time.Now().UTC()}
	e.seed(t, blogApp(),
		seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 7},
		seedDeploy{ID: "b2", Trigger: app.TriggerWebhook, Status: store.StatusReverted, Version: 8, Error: "health checks failed - rolling back to job version 7"})
	e.seed(t, domainsApp("docs", "docs.example.com"), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 2})
	e.Nomad.SetCopies("blog",
		api.AllocationListStub{ID: lgBlogA, TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-1", TaskStates: lgStarted("web", e.now)},
		api.AllocationListStub{ID: lgBlogB, TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-2", TaskStates: lgStarted("web", e.now)},
		api.AllocationListStub{ID: lgWorker, TaskGroup: "worker", JobVersion: 7, ClientStatus: "running", NodeName: "node-1", TaskStates: lgStarted("worker", e.now)})
	e.Nomad.SetCopies("docs",
		api.AllocationListStub{ID: lgDocs, TaskGroup: "web", JobVersion: 2, ClientStatus: "running", NodeName: "node-1", TaskStates: lgStarted("web", e.now)})

	lines := []string{
		e.ago(9*time.Minute) + " INFO listening on :8080",
		e.ago(8*time.Minute) + " ERROR connection refused to db",
		e.ago(7*time.Minute) + " [warn] slow query took 2s",
		`{"time":"` + e.ago(6*time.Minute) + `","level":"error","msg":"json boom"}`,
		e.ago(5*time.Minute) + " \x1b[31mERROR\x1b[0m red alert",
		e.ago(4*time.Minute+10*time.Second) + " ERROR retry failed",
		e.ago(4*time.Minute) + " GET /health 200",
		e.ago(3*time.Minute+55*time.Second) + " GET /health 200",
		e.ago(3*time.Minute+50*time.Second) + " GET /health 200",
		e.ago(3*time.Minute) + " ERROR retry failed",
		"plain untimed line",
	}
	var off int64
	for _, l := range lines {
		e.blogOff = append(e.blogOff, off)
		off += int64(len(l)) + 1
	}
	e.Nomad.AddLogFile(lgBlogA, "stdout", 0, lines, e.wrote(3*time.Minute))
	e.Nomad.AddLogFile(lgBlogB, "stdout", 0, []string{e.ago(time.Minute) + " INFO web b up"}, e.wrote(time.Minute))
	e.Nomad.AddLogFile(lgWorker, "stdout", 0, []string{e.ago(2*time.Minute) + " INFO worker tick"}, e.wrote(2*time.Minute))
	e.Nomad.AddLogFile(lgDocs, "stdout", 0, []string{e.ago(150*time.Second) + " INFO docs ready", e.ago(90*time.Second) + " ERROR docs broke"}, e.wrote(90*time.Second))
	return e
}

// lineID is the id of line i of blog's first copy.
func (e *lgEnv) lineID(i int) string { return fmt.Sprintf("aaaa1111.stdout.0.%d", e.blogOff[i]) }

func lgAnchor(id string) string {
	return "line-" + strings.ReplaceAll(id, ".", "-")
}

// classOf is the class attribute of the element with the id.
func classOf(t *testing.T, body, id string) string {
	t.Helper()
	m := regexp.MustCompile(`id="` + regexp.QuoteMeta(id) + `"[^>]*? class="([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no element with id %q in:\n%s", id, text(body))
	}
	return m[1]
}

func lgWant(t *testing.T, what, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, text(got))
		}
	}
}

func lgNot(t *testing.T, what, got string, not ...string) {
	t.Helper()
	for _, w := range not {
		if strings.Contains(got, w) {
			t.Errorf("%s has %q, which it must not:\n%s", what, w, text(got))
		}
	}
}

// ---- The page -------------------------------------------------------------------------

func TestLogsPageStream(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs")
	got := text(body)

	lgWant(t, "/logs", got, "Logs", "Output from every copy of every service, newest first. Deploys show up in the stream.",
		"times in UTC", "streaming from 4 copies")
	lgWant(t, "/logs", body, `<a href="/logs" class="cv-nav-item" aria-current="page">`, `class="cv-page cv-page-logs"`)

	// Every sample line, newest first, with its level; the ANSI line shows stripped.
	order := []string{"web b up", "docs broke", "docs ready", "worker tick", "plain untimed line", "retry failed", "GET /health 200",
		"red alert", "json boom", "slow query took 2s", "connection refused to db", "listening on :8080"}
	last := -1
	for _, want := range order {
		i := strings.Index(got, want)
		if i < 0 {
			t.Errorf("the stream lacks %q:\n%s", want, got)
			continue
		}
		if i < last && want != "docs ready" && want != "worker tick" {
			t.Errorf("%q is out of order (newest first)", want)
		}
		last = max(last, i)
	}
	lgNot(t, "/logs", body, "\x1b", "[31m", "[0m")

	// The row's anatomy: the level in its word, the service and the copy's short id, the repeat chip.
	lgWant(t, "/logs", body, `<span class="cv-llvl cv-llvl-error">error</span>`, `<span class="cv-llvl cv-llvl-warn">warn</span>`,
		`<span class="cv-svcname">blog/web</span><span class="cv-lcopy">aaaa1111</span>`, `<span class="cv-repeat">×3</span>`)
	if c := classOf(t, body, lgAnchor(e.lineID(5))); !strings.Contains(c, "cv-row-error") {
		t.Errorf("an error row's class = %q, want cv-row-error", c)
	}
	// The untimed line has an estimated time: a leading ~, its own class and the words.
	lgWant(t, "/logs", body, `cv-time-est`, `>~`, `title="estimated: this line carried no time; taken from the line before it"`)
	// An ordinary time has its zone in the title and no ~.
	if m := regexp.MustCompile(`<time class="cv-ltime" datetime="[^"]*" title="[^"]* UTC">(\d\d:\d\d:\d\d\.\d{3})</time>`).FindStringSubmatch(body); m == nil {
		t.Errorf("no ordinary time as HH:MM:SS.mmm in a <time>:\n%s", text(body))
	}
	// Debug is off by default.
	lgNot(t, "/logs", body, "cv-llvl-debug", "cv-row-debug")
}

func TestLogsPageRail(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs")
	rail := regexp.MustCompile(`(?s)<nav class="cv-lograil".*?</nav>`).FindString(body)
	if rail == "" {
		t.Fatal("no rail")
	}
	got := text(rail)
	// all services, then each app (a group head, since there are two) with its services and copies.
	for _, want := range []string{"Services all services blog blog/web 2 blog/worker 1 docs docs/web 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("the rail reads %q, want it to contain %q", got, want)
		}
	}
	lgWant(t, "the rail", rail, `<span class="cv-label cv-lrailgroup">blog</span>`, `<span class="cv-label cv-lrailgroup">docs</span>`,
		`<span class="cv-svcring"`, `<span class="cv-svchue"`, `class="cv-lrailitem cv-on" aria-current="true"`)
	// Each service's hue is the stable hash of app/service.
	for _, ref := range [][2]string{{"blog", "web"}, {"blog", "worker"}, {"docs", "web"}} {
		want := fmt.Sprintf(`href="/logs?service=%s" class="cv-lrailitem cv-hue-%d"`, url.QueryEscape(ref[0]+"/"+ref[1]), hueOf(ref[0], ref[1]))
		if !strings.Contains(rail, want) {
			t.Errorf("the rail lacks %s:\n%s", want, rail)
		}
	}
	// With more than one app, an item carries its app's name before the service (the narrow rail has
	// no group head, so it is what tells "web" of blog from "web" of docs); the CSS shows it only there.
	lgWant(t, "the rail", rail, `<span class="cv-lrailitem-name"><span class="cv-lrailapp">blog/</span>web</span>`,
		`<span class="cv-lrailapp">docs/</span>web</span>`, `<span class="cv-lrailapp">blog/</span>worker</span>`)
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	lgWant(t, "app.css", string(css), ".cv-lrailapp")
	// Rows wear the same hue.
	lgWant(t, "/logs", body, fmt.Sprintf(`<span class="cv-lsvc cv-hue-%d"><span class="cv-svcname">docs/web`, hueOf("docs", "web")))
	// With one app there is no group head and the rows name the service alone.
	one := e.pageOf(t, "/logs?app=docs")
	lgNot(t, "/logs?app=docs", one, "cv-lrailgroup", "cv-lrailapp", "blog", "docs/web")
	lgWant(t, "/logs?app=docs", one, `<span class="cv-svcname">web</span>`)
}

func TestLogsPageChoosingAService(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs?service=blog%2Fweb")
	// Only that service's copies' lines, the item on, its copies under it.
	lgWant(t, "?service=blog/web", body, "web b up", "retry failed")
	lgNot(t, "?service=blog/web", body, "docs broke", "worker tick")
	rail := regexp.MustCompile(`(?s)<nav class="cv-lograil".*?</nav>`).FindString(body)
	lgWant(t, "the rail", rail, `class="cv-lrailitem cv-hue-`+strconv.Itoa(hueOf("blog", "web"))+` cv-on" aria-current="true"`,
		`<div class="cv-lrailcopies">`, `>aaaa1111</a>`, `>bbbb2222</a>`)

	// A copy: only its lines, and its entry on.
	body = e.pageOf(t, "/logs?service=blog%2Fweb&copy=bbbb2222")
	lgWant(t, "?copy", body, "web b up")
	lgNot(t, "?copy", body, "retry failed", "listening on")
	lgWant(t, "?copy", body, `class="cv-lrailcopy cv-on" aria-current="true"`)
}

func TestLogsPageChips(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs")
	chips := regexp.MustCompile(`(?s)<div class="cv-lvlchips".*?</div>`).FindString(body)
	got := text(chips)
	// Six errors (the folded rows count all their lines) and one warn; debug is off and has no count.
	lgWant(t, "the chips", got, "error 6", "warn 1", "info", "debug")
	lgWant(t, "the chips", chips,
		`href="/logs?levels=warn%2Cinfo" class="cv-lvlchip cv-lvl-error cv-on"`,
		`href="/logs?levels=error%2Cinfo" class="cv-lvlchip cv-lvl-warn cv-on"`,
		`href="/logs?levels=error%2Cwarn" class="cv-lvlchip cv-lvl-info cv-on"`,
		`href="/logs?levels=error%2Cwarn%2Cinfo%2Cdebug" class="cv-lvlchip cv-lvl-debug"`,
		`title="Hide error lines"`, `title="Show debug lines"`)

	// Turning error off hides the rows and its count; the chip is off by its class and its words.
	off := e.pageOf(t, "/logs?levels=warn%2Cinfo")
	lgNot(t, "levels without error", off, "connection refused", "docs broke")
	lgWant(t, "levels without error", off, "slow query", `cv-lvlchip cv-lvl-error"`, `, hidden`)
	if strings.Contains(text(regexp.MustCompile(`(?s)<div class="cv-lvlchips".*?</div>`).FindString(off)), "error 6") {
		t.Error("an off chip still has its count")
	}
	// The only level that is on links to the page as it is, so the set is never empty.
	only := e.pageOf(t, "/logs?levels=warn")
	lgWant(t, "levels=warn", only, `href="/logs?levels=warn" class="cv-lvlchip cv-lvl-warn cv-on"`)
}

// The chips count the range, as the histogram does, not the 500 rows the stream holds; and a stream
// that holds the cap says so, with Show older beside it.
func TestLogsPageChipsCountTheRangeAndTheCapIsSaid(t *testing.T) {
	e := newConsoleEnv(t)
	now := time.Now().UTC()
	e.seed(t, domainsApp("docs", "docs.example.com"))
	e.Nomad.SetCopies("docs", api.AllocationListStub{ID: lgDocs, TaskGroup: "web", JobVersion: 2, ClientStatus: "running", NodeName: "n", TaskStates: lgStarted("web", now)})
	var lines []string
	start := now.Add(-10 * time.Minute)
	for i := range 600 {
		lines = append(lines, fmt.Sprintf("%s ERROR failure %04d", start.Add(time.Duration(i)*time.Second).Format(lgLayout), i))
	}
	lines = append(lines, now.Add(-time.Second).Format(lgLayout)+" [warn] one warning")
	e.Nomad.AddLogFile(lgDocs, "stdout", 0, lines, now)

	body := e.pageOf(t, "/logs")
	chips := text(regexp.MustCompile(`(?s)<div class="cv-lvlchips".*?</div>`).FindString(body))
	lgWant(t, "the chips", chips, "error 600", "warn 1")
	// The histogram's hidden table says the same total.
	total := 0
	for _, m := range regexp.MustCompile(`(?s)<th scope="row">[^<]*</th><td>\d+</td><td>(\d+)</td>`).FindAllStringSubmatch(body, -1) {
		n, _ := strconv.Atoi(m[1])
		total += n
	}
	if total != 600 {
		t.Errorf("the histogram counts %d errors, want 600", total)
	}
	head := regexp.MustCompile(`(?s)<div class="cv-lstreamhead">.*?</div>`).FindString(body)
	lgWant(t, "the stream head", text(head), "newest 500 lines · Show older")
	lgWant(t, "the stream head", head, `<a href="/logs?`, `before=`)

	// A stale open link on a capped stream offers Show older beside the lines around.
	stale := regexp.MustCompile(`(?s)<p class="cv-lstale".*?</p>`).FindString(e.pageOf(t, "/logs?open=dddd4444.stdout.0.7"))
	lgWant(t, "a stale open link on a capped stream", text(stale), "That line isn't in what was read. Show older, or open the lines around it. Lines around this")
	lgWant(t, "a stale open link on a capped stream", stale, `before=`, `at=dddd4444.stdout.0.7`)

	// A stream below the cap says nothing of it.
	few := e.pageOf(t, "/logs?q=one+warning")
	lgNot(t, "an uncapped head", text(few), "newest 500 lines")
}

func TestLogsPageSearch(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs?q=retry")
	lgWant(t, "?q=retry", text(body), "2 lines")
	lgWant(t, "?q=retry", body, `name="q" value="retry"`, `class="cv-logsearch-n">2 lines<`, "retry failed")
	lgNot(t, "?q=retry", body, "red alert", "docs broke")
	// The search matches the service too (the contract's text + service).
	lgWant(t, "?q=docs/web", e.pageOf(t, "/logs?q=docs%2Fweb"), "docs ready")
	// Nothing typed: no count; the form carries the rest of the filter along.
	plain := e.pageOf(t, "/logs?service=blog%2Fweb&range=1h")
	lgNot(t, "/logs", plain, "cv-logsearch-n")
	form := regexp.MustCompile(`(?s)<form class="cv-logfilter".*?</form>`).FindString(plain)
	lgWant(t, "the search form", form, `<input type="hidden" name="service" value="blog/web">`, `<input type="hidden" name="range" value="1h">`,
		`class="cv-logsearch-submit"`, `role="search"`)
	lgNot(t, "the search form", form, `name="q" type="hidden"`, `name="before"`)
}

func TestLogsPageRangeAndLive(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs?q=retry&service=blog%2Fweb&range=1h&levels=error%2Cwarn")
	bar := regexp.MustCompile(`(?s)<nav class="cv-seg" aria-label="Time range">.*?</nav>`).FindString(body)
	// Each item keeps the other filters; the default range is just left out; the chosen one is on.
	lgWant(t, "the range bar", bar,
		`href="/logs?levels=error%2Cwarn&amp;q=retry&amp;service=blog%2Fweb" class="cv-seg-item"`,
		`href="/logs?levels=error%2Cwarn&amp;q=retry&amp;range=1h&amp;service=blog%2Fweb" class="cv-seg-item cv-on" aria-current="true"`,
		`range=24h`, `range=7d`, ">15 min<", ">1 h<", ">24 h<", ">7 d<")
	// Live is a form button flipping ?live and carrying the filter.
	head := regexp.MustCompile(`(?s)<div class="cv-lhead-tools">.*?</div>`).FindString(body)
	lgWant(t, "the head", head, `<form method="get" action="/logs">`, `<input type="hidden" name="q" value="retry">`,
		`name="live" value="0"`, `aria-pressed="true"`, ">Live<")
	paused := e.pageOf(t, "/logs?live=0")
	lgWant(t, "?live=0", paused, `name="live" value="1"`, `aria-pressed="false"`, ">Paused<", "paused")
	lgNot(t, "?live=0", paused, "streaming from", `id="logs-live" hx-get`)

	// While live, the region is drawn again from the part on the logs notice, and the page opens the
	// stream; the no-script sentence is in the stream's head.
	live := e.pageOf(t, "/logs?service=blog%2Fweb")
	lgWant(t, "/logs", live, `id="logs-live" hx-get="/parts/logs?service=blog%2Fweb" hx-trigger="sse:logs" hx-swap="innerHTML"`,
		`sse-connect="/live?environment=production&amp;live=1&amp;project=default&amp;watch=logs"`,
		"<noscript><span>Following needs JavaScript. Reload the page to see newer lines.</span></noscript>")
}

func TestLogsPagePlaceBars(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs")
	lgNot(t, "/logs with one place", body, `aria-label="Project"`, `aria-label="Environment"`, "cv-logfilter-div")

	ctx := context.Background()
	if _, err := e.Svc.CreateProject(ctx, "shop", "Shop Floor"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	body = e.pageOf(t, "/logs?q=x")
	projects := regexp.MustCompile(`(?s)<nav class="cv-seg" aria-label="Project">.*?</nav>`).FindString(body)
	lgWant(t, "the project bar", projects, ">Default<", ">Shop Floor<", `href="/logs?project=shop&amp;q=x"`, `cv-on" aria-current="true">Default`)
	lgNot(t, "default's page", body, `aria-label="Environment"`) // Default has one environment
	lgWant(t, "default's page", body, "cv-logfilter-div")

	body = e.pageOf(t, "/logs?project=shop&environment=staging")
	envs := regexp.MustCompile(`(?s)<nav class="cv-seg" aria-label="Environment">.*?</nav>`).FindString(body)
	lgWant(t, "the environment bar", envs, ">production<", ">staging<", `cv-on" aria-current="true">staging`,
		`href="/logs?project=shop"`)
	// A place with no apps reads nothing: a sentence, not an error.
	lgWant(t, "an empty place", text(body), "No output yet. New lines appear here.")

	for path, want := range map[string]int{"/logs?project=nope": 404, "/logs?project=shop&environment=nope": 404,
		"/logs?range=2d": 400, "/logs?levels=loud": 400, "/logs?open=nope": 400, "/logs?before=yesterday": 400, "/logs?at=nope": 400} {
		if resp, _ := e.get(t, path); resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestLogsPageHistogram(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs")
	hist := regexp.MustCompile(`(?s)<section class="cv-panel cv-hist".*?</section>`).FindString(body)
	if hist == "" {
		t.Fatal("no histogram")
	}
	lgWant(t, "the histogram", hist, `viewBox="0 0 300 48" preserveAspectRatio="none" role="img"`, `<table class="sr-only">`)
	// 30 bins: one base rect each (a bar, a stub for nothing), the errors stacked on some.
	bars := regexp.MustCompile(`<rect class="(cv-hbar|cv-hbar-empty|cv-hbar-unread)" `).FindAllStringSubmatch(hist, -1)
	if len(bars) != 30 {
		t.Errorf("the histogram has %d base rects, want 30", len(bars))
	}
	if n := strings.Count(hist, `<rect class="cv-hbar-err"`); n == 0 {
		t.Error("no rect for the errors")
	}
	if n := strings.Count(hist, `<tr><th scope="row">`); n != 30 {
		t.Errorf("the hidden table has %d rows, want 30", n)
	}
	// A bin's title: its lines, errors and estimated times.
	lgWant(t, "the histogram", hist, " at an estimated time</title>", " errors, ", "1 line</title>")
	// The summary says what the chart shows in a sentence.
	m := regexp.MustCompile(`aria-label="(\d+ lines) in the last 15 minutes, (\d+ errors), 6 deploys"`).FindStringSubmatch(hist)
	if m == nil {
		t.Errorf("the summary is unexpected:\n%s", hist[:min(len(hist), 600)])
	}
	// The deploys of the range are markers with a title, and the deploy's name is under them.
	lgWant(t, "the histogram", hist, `<line class="cv-hmark" `, `<line class="cv-hmark cv-hmark-failed" `, `<title>blog v7 rolling out</title>`,
		`<title>blog v8 failed, rolled back</title>`, `<span class="cv-hlabel-deploy">┆ docs v2 and 5 more</span>`, `<span class="cv-hlabel-end">now</span>`)
	// Everything was read: no "read from".
	lgNot(t, "the histogram", hist, "cv-hbar-unread", "cv-hlabel-read", "no lines in this range")
	// The start of the range.
	if !regexp.MustCompile(`<div class="cv-hlabels">\s*<span>\d\d:\d\d UTC</span>`).MatchString(hist) {
		t.Errorf("the label row doesn't start with the range's start:\n%s", hist)
	}
}

func TestHistOfGeometry(t *testing.T) {
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	h := service.LogHistogram{From: from, To: from.Add(15 * time.Minute), BucketSeconds: 30}
	for i := range h.Bins {
		h.Bins[i] = service.LogBin{From: from.Add(time.Duration(i) * 30 * time.Second), Read: true}
	}
	h.Bins[12].Count, h.Bins[12].Errors = 100, 25
	h.Bins[13].Count = 1
	h.Bins[14].Count, h.Bins[14].Estimated = 50, 2
	for i := range 10 { // the oldest ten bins weren't read
		h.Bins[i].Read = h.Bins[i].Read && i >= 6
	}
	cov := service.LogCoverage{From: from.Add(5 * time.Minute), Complete: false, Timed: false}
	deploys := []service.DeployRow{{DeploymentID: "d1", App: "web", At: from.Add(7*time.Minute + 30*time.Second), Kind: service.DeployFailed, JobVersion: 3}}
	v := histOf(h, cov, deploys, logsRanges[0])

	if len(v.Bars) != 30 {
		t.Fatalf("%d bars, want 30", len(v.Bars))
	}
	// The busiest bin is 44 high, standing on the floor; a quarter of it is errors; one line is 2 high.
	b3, b4, b5 := v.Bars[12], v.Bars[13], v.Bars[14]
	if b3.Class != "cv-hbar" || b3.H != "44" || b3.Y != "4" || !b3.Err || b3.ErrY != "4" || b3.ErrH != "11" {
		t.Errorf("bin 3 = %+v, want a bar of 44 at y 4 with 11 of errors on top", b3)
	}
	if b4.H != "2" || b4.Y != "46" || b4.Err {
		t.Errorf("bin 4 = %+v, want the 2-high minimum with no errors", b4)
	}
	if b5.H != "22" || b5.Title != "50 lines, 2 at an estimated time" {
		t.Errorf("bin 5 = %+v, want 22 high and an estimated count in its title", b5)
	}
	// Bins before the coverage are hatched, never zero; read ones with nothing are stubs.
	if u := v.Bars[0]; u.Class != "cv-hbar-unread" || u.Title != "not read: older output wasn't loaded" {
		t.Errorf("an unread bin = %+v", u)
	}
	if z := v.Bars[7]; z.Class != "cv-hbar-empty" || z.H != "2" {
		t.Errorf("an empty bin = %+v", z)
	}
	if v.Table[0].Lines != "not read" || v.Table[12].Lines != "100" || v.Table[12].Errors != "25" {
		t.Errorf("the table = %+v, %+v", v.Table[0], v.Table[12])
	}
	// The marker sits at its time: 7.5 of 15 minutes is the middle of 300.
	if len(v.Marks) != 1 || v.Marks[0].X != "150" || !v.Marks[0].Failed || v.Marks[0].Title != "v3 failed" {
		t.Errorf("marks = %+v", v.Marks)
	}
	if v.Deploy != "┆ web v3" || v.Read != "read from 00:05 UTC · not all read in time" || v.Start != "00:00 UTC" || v.Empty {
		t.Errorf("labels = %q, %q, %q, empty %v", v.Deploy, v.Read, v.Start, v.Empty)
	}
	if want := "151 lines in the last 15 minutes, 25 errors, 1 deploy"; v.Summary != want {
		t.Errorf("summary = %q, want %q", v.Summary, want)
	}
	// Nothing read: stubs, and the words for it.
	none := histOf(service.LogRead{From: from, To: from.Add(time.Hour), Coverage: service.LogCoverage{Complete: true, Timed: true}}.Histogram(), service.LogCoverage{Complete: true, Timed: true}, nil, logsRanges[1])
	if !none.Empty || none.Summary != "No lines in the last hour" || none.Bars[0].Class != "cv-hbar-empty" {
		t.Errorf("an empty histogram = %+v", none)
	}
}

func TestLogsPageDeployRows(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs")
	got := text(body)
	lgWant(t, "/logs", got, "blog v7 started rolling out · through the API", "blog v7 is live · rollout finished",
		"blog v8 failed its health checks · rolled back to v7", "docs v2 started rolling out · through the API")
	// Each is a link to the deployment's entry; a failed one has the failed look.
	lgWant(t, "/logs", body, `<a href="/apps/blog/deployments#deployment-b1" class="cv-row-deploy">`,
		`<a href="/apps/blog/deployments#deployment-b2" class="cv-row-deploy cv-row-deploy-failed">`)
	// The deploys of one app only, when the stream is narrowed to it.
	one := text(e.pageOf(t, "/logs?service=docs%2Fweb"))
	lgWant(t, "?service=docs/web", one, "docs v2")
	lgNot(t, "?service=docs/web", one, "blog v7")
	// A deploy is never filtered by level or search.
	q := text(e.pageOf(t, "/logs?q=zzzz&levels=debug"))
	lgWant(t, "a search with no match", q, "blog v7 started rolling out", logsNoMatch)
}

func TestDeployRowWords(t *testing.T) {
	at := time.Date(2026, 10, 25, 14, 3, 41, 0, time.UTC)
	for _, tc := range []struct {
		row  service.DeployRow
		want string
	}{
		{service.DeployRow{App: "web", Kind: service.DeployStarted, JobVersion: 24, Who: "pushed to main by ana"}, "web v24 started rolling out · pushed to main by ana"},
		{service.DeployRow{App: "web", Kind: service.DeployStarted, JobVersion: 0, Who: "from the console"}, "web started rolling out · from the console"},
		{service.DeployRow{App: "web", Kind: service.DeployLive, JobVersion: 24}, "web v24 is live · rollout finished"},
		{service.DeployRow{App: "web", Kind: service.DeployFailed, JobVersion: 24}, "web v24 failed its health checks"},
		{service.DeployRow{App: "web", Kind: service.DeployReverted, JobVersion: 24, RevertedTo: 23}, "web v24 failed its health checks · rolled back to v23"},
		{service.DeployRow{App: "web", Kind: service.DeployReverted, JobVersion: 24}, "web v24 failed its health checks · rolled back"},
		{service.DeployRow{App: "web", Kind: service.DeploySuperseded, JobVersion: 24}, "web v24 was replaced by a newer deploy"},
		{service.DeployRow{App: "shop--staging--web", Kind: service.DeployLive, JobVersion: 2}, "web v2 is live · rollout finished"},
	} {
		tc.row.At, tc.row.DeploymentID = at, "dep1"
		got := deployRowOf(tc.row)
		if s := got.Sentence.String(); s != tc.want {
			t.Errorf("%s: %q, want %q", tc.row.Kind, s, tc.want)
		}
		if want := tc.row.Kind == service.DeployFailed || tc.row.Kind == service.DeployReverted; got.Failed != want {
			t.Errorf("%s: failed = %v", tc.row.Kind, got.Failed)
		}
		if want := "/apps/" + url.PathEscape(tc.row.App) + "/deployments#deployment-dep1"; got.Href != want {
			t.Errorf("%s: href = %q, want %q", tc.row.Kind, got.Href, want)
		}
	}
}

func TestLogsPageOpenRow(t *testing.T) {
	e := logsPageEnv(t)
	repeat := e.lineID(9) // the second "ERROR retry failed"
	anchor := lgAnchor(repeat)
	body := e.pageOf(t, "/logs?open="+repeat)

	// The row is open and links back to the page without it (paused: an open row isn't followed); the
	// others open themselves, paused.
	lgWant(t, "the open page", body, `href="/logs?live=0#`+anchor+`"`, `href="/logs?live=0&amp;open=`+e.lineID(1)+`#`+lgAnchor(e.lineID(1))+`"`)

	// A page with a row open is not followed: the head says paused and nothing swaps the region, so
	// the strip under the row stays as it is.
	lgNot(t, "the open page", body, "hx-get", "streaming from")
	lgWant(t, "the open page", body, `name="live" value="1"`, `aria-pressed="false"`, ">Paused<")

	// The strip is under its row, inside the stream, and nowhere else.
	strip := regexp.MustCompile(`(?s)<section class="cv-rowstrip" id="open-line".*?</section>`).FindString(body)
	if strip == "" {
		t.Fatal("no strip for the open row")
	}
	if n := strings.Count(body, `id="open-line"`); n != 1 {
		t.Errorf("%d strips, want 1", n)
	}
	underRow := regexp.MustCompile(`(?s)<a href="[^"]*" id="` + regexp.QuoteMeta(anchor) + `".*?</a>\s*<section class="cv-rowstrip" id="open-line"`)
	if !underRow.MatchString(body) {
		t.Errorf("the strip doesn't follow the open row:\n%s", text(body))
	}
	if i, j, k := strings.Index(body, `<section class="cv-logstream"`), strings.Index(body, `id="open-line"`), strings.Index(body, `<footer class="cv-lfoot"`); i < 0 || j < i || k < j {
		t.Error("the strip isn't inside the stream")
	}
	got := text(strip)
	lgWant(t, "the strip", got, "On stdout of blog/web, copy aaaa1111.", "Lines around this", "Only blog/web", "Close",
		"First seen", "2 times since")
	lgWant(t, "the strip", strip, `href="/logs?at=`+repeat+`&amp;copy=aaaa1111&amp;live=0"`)
	lgWant(t, "the strip", strip, `href="/logs?live=0&amp;service=blog%2Fweb"`, `href="/logs?live=0"`)
	// The row's own address as selectable text.
	lgWant(t, "the strip", strip, `class="cv-rowstrip-url">http://127.0.0.1:`, `/logs?live=0&amp;open=`+repeat+`#`+anchor)
	lgNot(t, "the strip", strip, "cv-rowraw") // not a JSON line
	lgWant(t, "the strip", strip, ">Lines around this<", `<span class="cv-inline-mono">blog/web</span>`)

	// A JSON line has Raw, the line as received.
	json := e.pageOf(t, "/logs?open="+e.lineID(3))
	raw := regexp.MustCompile(`(?s)<details class="cv-rowraw">.*?</details>`).FindString(json)
	lgWant(t, "Raw", raw, "<summary>Raw</summary>", `&#34;level&#34;:&#34;error&#34;`, "json boom")
	// An info row has no first-seen note; an error seen once says so.
	if info := e.pageOf(t, "/logs?open="+e.lineID(0)); strings.Contains(info, "First seen") || strings.Contains(info, "Seen once") {
		t.Error("an info row has the first-seen note")
	}
	lgWant(t, "an error seen once", e.pageOf(t, "/logs?open="+e.lineID(1)), "Seen once in this range.")
	// A row that isn't in the read draws no strip and no error.
	gone := e.pageOf(t, "/logs?open=aaaa1111.stdout.0.999999")
	lgNot(t, "a row that isn't read", gone, "cv-rowstrip", "cv-row-open")
	// ... but the stream head says so, and offers the lines around it from the id alone.
	stale := regexp.MustCompile(`(?s)<p class="cv-lstale".*?</p>`).FindString(gone)
	lgWant(t, "the stale notice", text(stale), "That line isn't in what was read.", "Lines around this")
	lgWant(t, "the stale notice", stale, `href="/logs?at=aaaa1111.stdout.0.999999&amp;copy=aaaa1111&amp;live=0"`, `role="status"`)
	// Nothing older to show here (the whole range was read), so no "Show older".
	lgNot(t, "the stale notice", text(stale), "Show older")
	// An open row that is there has no notice; no open row has none either.
	lgNot(t, "an open row that is read", e.pageOf(t, "/logs?open="+repeat), "cv-lstale")
	lgNot(t, "the plain page", e.pageOf(t, "/logs"), "cv-lstale")
	// An id that doesn't parse stays a 400.
	if resp, _ := e.get(t, "/logs?open=nonsense"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("open=nonsense = %d, want 400", resp.StatusCode)
	}

	// The part is the region alone: the open row keeps its id and class, with no strip, filter or rail.
	resp, part := e.get(t, "/parts/logs?open="+repeat)
	if resp.StatusCode != 200 {
		t.Fatalf("part = %d", resp.StatusCode)
	}
	if c := classOf(t, part, anchor); !strings.Contains(c, "cv-row-open") {
		t.Errorf("the part's open row = %q", c)
	}
	lgNot(t, "the part", part, "cv-rowstrip", "Lines around this", "cv-logfilter", "cv-lograil", "<html", "cv-lhead", "cv-lfoot")
	lgWant(t, "the part", part, "cv-logstream", "cv-hist")
}

func TestLogsAroundView(t *testing.T) {
	e := logsPageEnv(t)
	at := e.lineID(3)
	body := e.pageOf(t, "/logs?copy=aaaa1111&at="+at)
	got := text(body)
	lgWant(t, "the around view", got, "Around ", " UTC in copy aaaa1111 of blog/web. Every level, this copy only. Back to the stream",
		"listening on :8080", "json boom", "plain untimed line", "times in UTC")
	if c := classOf(t, body, lgAnchor(at)); !strings.Contains(c, "cv-row-anchor") {
		t.Errorf("the anchor row's class = %q", c)
	}
	lgNot(t, "the around view", body, "cv-hist", "streaming from", "web b up")
	lgWant(t, "the around view", body, `<a href="/logs">Back to the stream</a>`)
	// The copy defaults to the line's.
	lgWant(t, "?at alone", text(e.pageOf(t, "/logs?at="+at)), "in copy aaaa1111")
	// A line of a purged file is a sentence; one past the files is a refused address.
	e.Nomad.AddLogFile(lgBlogB, "stdout", 3, []string{e.ago(time.Minute) + " INFO newer"}, e.wrote(time.Minute))
	e.Nomad.PurgeLogFile(lgBlogB, "stdout", 0)
	resp, rotated := e.get(t, "/logs?copy=bbbb2222&at=bbbb2222.stdout.1.0")
	if resp.StatusCode != 404 || !strings.Contains(text(rotated), logsRotated) {
		t.Errorf("a line of a purged file = %d: %s", resp.StatusCode, text(rotated))
	}
	if resp, _ := e.get(t, "/logs?copy=aaaa1111&at=aaaa1111.stdout.5.0"); resp.StatusCode != 400 {
		t.Errorf("a line past the files = %d, want 400", resp.StatusCode)
	}
	// The rotated-away sentence has its way back, keeping the filters, and no "Around" notice.
	lgWant(t, "the rotated view", text(rotated), logsRotated)
	lgWant(t, "the rotated view", rotated, `<a href="/logs">Back to the stream</a>`)
	_, rotated = e.get(t, "/logs?q=x&copy=bbbb2222&at=bbbb2222.stdout.1.0")
	lgWant(t, "the rotated view", text(rotated), logsRotated)
	lgWant(t, "the rotated view", rotated, `<a href="/logs?q=x">Back to the stream</a>`)
	lgNot(t, "the rotated view", text(rotated), "Around ", "Every level")

	// A copy that is gone has its own sentence, and the way back.
	resp, gone := e.get(t, "/logs?at=ffff9999.stdout.0.0")
	if resp.StatusCode != 404 {
		t.Errorf("the lines around a copy that isn't there = %d, want 404", resp.StatusCode)
	}
	lgWant(t, "the gone view", text(gone), "This copy's lines are gone.")
	lgWant(t, "the gone view", gone, `<a href="/logs">Back to the stream</a>`)
	lgNot(t, "the gone view", text(gone), "Around ", "Pick another")
}

func TestLogsPageEmptySentences(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, domainsApp("docs", "docs.example.com"), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 2})
	e.Nomad.SetCopies("docs", api.AllocationListStub{ID: lgDocs, TaskGroup: "web", JobVersion: 2, ClientStatus: "running", NodeName: "n", TaskStates: lgStarted("web", time.Now())})
	// No output, no filter.
	body := e.pageOf(t, "/logs")
	lgWant(t, "/logs", text(body), logsNoOutput, "no lines in this range", "docs v2 started rolling out")
	lgNot(t, "/logs", text(body), logsNoMatch)
	// With a filter.
	for _, path := range []string{"/logs?q=zzz", "/logs?levels=debug", "/logs?service=docs%2Fweb"} {
		body = e.pageOf(t, path)
		lgWant(t, path, text(body), logsNoMatch)
		lgNot(t, path, text(body), logsNoOutput)
	}
	// The footer is always there: the retention sentence and a Download link carrying the filter.
	lgWant(t, "the footer", e.pageOf(t, "/logs?q=zzz&service=docs%2Fweb&range=1h&live=0&levels=error%2Cwarn"),
		"Kept as long as Nomad keeps them. Send them somewhere longer from Settings › Connections, later.",
		`href="/logs/download?levels=error%2Cwarn&amp;q=zzz&amp;range=1h&amp;service=docs%2Fweb" class="cv-btn cv-btn-secondary cv-btn-sm"`)
}

func TestLogsPageOlderAndDropped(t *testing.T) {
	e := newConsoleEnv(t)
	now := time.Now().UTC()
	e.seed(t, domainsApp("docs", "docs.example.com"))
	e.Nomad.SetCopies("docs", api.AllocationListStub{ID: lgDocs, TaskGroup: "web", JobVersion: 2, ClientStatus: "running", NodeName: "n", TaskStates: lgStarted("web", now)})

	// A file of 3 MiB read for 11 minutes: the page reads one MiB, so not all of the range, and shows
	// 500 rows of what it read.
	var lines []string
	start := now.Add(-11 * time.Minute)
	for i := range 33000 {
		lines = append(lines, fmt.Sprintf("%s INFO line %06d %s", start.Add(time.Duration(i)*20*time.Millisecond).Format(lgLayout), i, strings.Repeat("x", 60)))
	}
	e.Nomad.AddLogFile(lgDocs, "stdout", 0, lines, now)
	body := e.pageOf(t, "/logs")
	hist := regexp.MustCompile(`(?s)<section class="cv-panel cv-hist".*?</section>`).FindString(body)
	lgWant(t, "the histogram", hist, `<rect class="cv-hbar-unread"`, `title`, `<span class="cv-hlabel-read">read from `, ` UTC</span>`)
	lgWant(t, "the histogram", hist, `<title>not read: older output wasn&#39;t loaded</title>`)
	older := regexp.MustCompile(`<a href="(/logs\?[^"]*before=[^"]*)" class="cv-btn cv-btn-secondary cv-btn-sm">Show older</a>`).FindStringSubmatch(body)
	if older == nil {
		t.Fatalf("no Show older link:\n%s", text(body)[:600])
	}
	href := strings.ReplaceAll(older[1], "&amp;", "&")
	lgWant(t, "Show older", href, "live=0")
	// Following it shows the lines before, with the way back, and no row repeated from the newest page.
	next := e.pageOf(t, href)
	lgWant(t, "the older page", text(next), "Lines before ", "Back to the newest")
	lgWant(t, "the older page", next, `<a href="/logs">Back to the newest</a>`)
	if m := regexp.MustCompile(`line (\d{6})`).FindStringSubmatch(text(body)); m != nil {
		if strings.Contains(text(next), "line "+m[1]) && m[1] != "" {
			// The newest line of the first page must not be on the older one.
			newest, _ := strconv.Atoi(m[1])
			if newest > 30000 {
				t.Errorf("the older page repeats the newest line %s", m[1])
			}
		}
	}

	// A copy whose first files are gone: "Older lines were dropped."
	e.Nomad.AddLogFile(lgDocs, "stdout", 3, []string{now.Add(-time.Minute).Format(lgLayout) + " INFO only the fourth file is left"}, now.Add(-55*time.Second))
	e.Nomad.PurgeLogFile(lgDocs, "stdout", 0)
	body = e.pageOf(t, "/logs")
	lgWant(t, "a dropped read", text(body), "Older lines were dropped.", "only the fourth file is left")
	lgNot(t, "a dropped read", text(body), "Show older")
}

func TestLogsPageNomadUnreachable(t *testing.T) {
	e := logsPageEnv(t)
	e.Nomad.Down()
	e.Clock.Advance(time.Minute) // the console's clock stands still; the deploys were stored after it
	resp, body := e.get(t, "/logs")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /logs with Nomad down = %d", resp.StatusCode)
	}
	lgWant(t, "/logs", body, `<div class="cv-lerror" role="status"><span class="cv-mark cv-mark-idle" aria-hidden="true"></span>`,
		"Nomad can&#39;t be reached right now, so logs can&#39;t be read.")
	lgNot(t, "/logs", text(body), "streaming from", logsNoOutput)
	// The histogram has its empty bars, and the deploys (from the store) are still rows.
	if n := strings.Count(body, `<rect class="cv-hbar-empty"`); n != 30 {
		t.Errorf("%d empty bars, want 30", n)
	}
	lgWant(t, "/logs", text(body), "no lines in this range", "blog v7 is live · rollout finished")
	// The part is a 200 with the same sentence, so a refresh never empties the page with an error.
	if resp, part := e.get(t, "/parts/logs"); resp.StatusCode != 200 || !strings.Contains(part, "so logs can&#39;t be read") {
		t.Errorf("part = %d: %s", resp.StatusCode, text(part))
	}
}

func TestLogsPageStaleCopy(t *testing.T) {
	e := logsPageEnv(t)
	resp, body := e.get(t, "/logs?copy=ffff9999")
	if resp.StatusCode != 404 {
		t.Errorf("a copy that isn't there = %d, want 404", resp.StatusCode)
	}
	lgWant(t, "a stale copy", text(body), "That copy isn't one of this environment's copies any more.")
	if resp, _ := e.get(t, "/logs?app=nope"); resp.StatusCode != 404 {
		t.Errorf("an app that isn't there = %d, want 404", resp.StatusCode)
	}
}

func TestLogsPartAnswersTheRegion(t *testing.T) {
	e := logsPageEnv(t)
	resp, part := e.get(t, "/parts/logs?q=retry")
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("part = %d, cache-control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	lgWant(t, "the part", part, "cv-hist", "cv-logstream", "retry failed")
	lgNot(t, "the part", part, "red alert", "<html", "<form", "cv-lhead", "cv-lograil", "cv-lfoot", "cv-rowstrip")
	if resp, _ := e.get(t, "/parts/logs?range=2d"); resp.StatusCode != 400 {
		t.Errorf("a bad range = %d, want 400", resp.StatusCode)
	}
	// It needs a session, like every part: no cookie is a redirect for htmx.
	if anon, _ := e.do(t, request{path: "/parts/logs"}); anon.StatusCode != http.StatusUnauthorized {
		t.Errorf("part without a session = %d, want 401", anon.StatusCode)
	}
}

func TestLogsPageHasNoInlineStyle(t *testing.T) {
	e := logsPageEnv(t)
	style := regexp.MustCompile(lgNoStyle)
	for _, path := range []string{"/logs", "/logs?live=0&open=" + e.lineID(3), "/logs?open=" + e.lineID(9), "/logs?copy=aaaa1111&at=" + e.lineID(3),
		"/logs?q=retry&levels=debug", "/parts/logs"} {
		_, body := e.get(t, path)
		if m := style.FindString(body); m != "" {
			t.Errorf("GET %s has an inline style attribute (%q): the page's policy forbids it", path, m)
		}
		if strings.Contains(body, "<script") && !strings.Contains(body, `src=`) {
			t.Errorf("GET %s has an inline script", path)
		}
	}
}

func TestLogsPageHoldsNoLineInTheLog(t *testing.T) {
	e := logsPageEnv(t)
	e.Nomad.AddLogFile(lgDocs, "stdout", 0, []string{e.ago(time.Minute) + " INFO " + lgSecret}, e.wrote(time.Minute))
	body := e.pageOf(t, "/logs")
	lgWant(t, "/logs", body, lgSecret)
	e.pageOf(t, "/logs?open=dddd4444.stdout.0.0")
	e.pageOf(t, "/logs?q="+url.QueryEscape(lgSecret))
	e.get(t, "/parts/logs?q="+lgSecret)
	if strings.Contains(e.Logs.String(), lgSecret) {
		t.Errorf("a line's text reached the server log:\n%s", e.Logs.String())
	}
	// A line's text is escaped, never markup.
	e.Nomad.AddLogFile(lgDocs, "stdout", 0, []string{e.ago(time.Minute) + ` INFO <script>alert(1)</script> <img src=x onerror=y>`}, e.wrote(time.Minute))
	body = e.pageOf(t, "/logs")
	lgNot(t, "/logs", body, "<script>alert", "<img src=x")
	lgWant(t, "/logs", body, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestLogsStreamWatches(t *testing.T) {
	e := newConsoleEnv(t)
	var placeSeen atomic.Value
	var open, done atomic.Int32
	e.Server.live.watchLogs = func(project, environment string) func() {
		placeSeen.Store(project + "/" + environment)
		open.Add(1)
		return func() { done.Add(1) }
	}
	ls := openStream(t, e.URL, "/live?watch=logs&project=default&environment=production&live=1", e.Cookie.Value)
	ls.expect("sync")
	if open.Load() != 1 || placeSeen.Load() != "default/production" {
		t.Fatalf("watched %d times, for %v", open.Load(), placeSeen.Load())
	}
	ls.close()
	waitFor(t, "done", func() bool { return done.Load() == 1 })

	// A page that isn't live, or for a place that isn't there, follows nothing.
	for _, path := range []string{"/live?watch=logs", "/live?watch=logs&project=nope&live=1"} {
		open.Store(0)
		ls := openStream(t, e.URL, path, e.Cookie.Value)
		ls.expect("sync")
		ls.close()
		if open.Load() != 0 {
			t.Errorf("%s followed the place", path)
		}
	}
	// Without a session it ends at once, and follows nothing.
	open.Store(0)
	anon := openStream(t, e.URL, "/live?watch=logs&live=1", "")
	anon.expect("ended")
	if open.Load() != 0 {
		t.Error("a stream with no session followed the place")
	}
	// The other streams don't follow.
	ls = openStream(t, e.URL, "/live", e.Cookie.Value)
	ls.expect("sync")
	ls.close()
	if open.Load() != 0 {
		t.Error("/live followed a place")
	}
}

// The stream's notice reaches a page: a line written by a copy of the place is the logs notice, which
// carries nothing of the line.
func TestLogsStreamForwardsTheNotice(t *testing.T) {
	e := logsPageEnv(t)
	e.Svc.SetLogWatchGaps(50*time.Millisecond, time.Hour)
	ls := openStream(t, e.URL, "/live?watch=logs&live=1", e.Cookie.Value)
	ls.expect("sync")
	waitFor(t, "the copies' streams", func() bool { return e.Nomad.OpenLogStreams() == 8 }) // four copies, two streams each
	e.Nomad.WriteLog(lgBlogA, "stdout", "ERROR "+lgSecret+"\n")
	ls.expect("logs")
	ls.close()
	waitFor(t, "the followers to end", func() bool { return e.Nomad.OpenLogStreams() == 0 })
	if strings.Contains(e.Logs.String(), lgSecret) {
		t.Error("a line reached the server's log")
	}
}

func TestHueIsStable(t *testing.T) {
	seen := map[int]bool{}
	for _, svc := range []string{"web", "worker", "db", "cache", "queue", "mail", "api", "search", "cron", "proxy"} {
		h := hueOf("blog", svc)
		if h < 0 || h > 5 || h != hueOf("blog", svc) {
			t.Errorf("hueOf(blog, %s) = %d", svc, h)
		}
		seen[h] = true
	}
	if len(seen) < 4 {
		t.Errorf("ten services use only %d of the six hues", len(seen))
	}
	if hueOf("blog", "web") == hueOf("docs", "web") && hueOf("blog", "web") == hueOf("shop--staging--web", "web") && hueOf("a", "b") == hueOf("c", "d") {
		t.Error("the hash ignores the app")
	}
}

func TestLevelSet(t *testing.T) {
	l := defaultLevels
	if l.param() != "error,warn,info" || l.has(logline.Debug) || !l.has(logline.Error) {
		t.Errorf("default = %q", l.param())
	}
	if got := l.toggled(logline.Debug).param(); got != "error,warn,info,debug" {
		t.Errorf("debug on = %q", got)
	}
	if got := l.toggled(logline.Error).param(); got != "warn,info" {
		t.Errorf("error off = %q", got)
	}
	only := levelSet{false, true, false, false}
	if got := only.toggled(logline.Warn); got != only {
		t.Errorf("the last level turned off = %v, want it left on", got)
	}
	if _, ok := parseLevels("error,loud"); ok {
		t.Error("an unknown level was accepted")
	}
	got, ok := parseLevels("debug, ERROR")
	if !ok || got != (levelSet{true, false, false, true}) {
		t.Errorf("parseLevels = %v, %v", got, ok)
	}
}

func TestLogsQueryRoundTrip(t *testing.T) {
	for _, raw := range []string{"", "project=shop&environment=staging", "service=blog%2Fweb&copy=aaaa1111&q=a+b&range=24h&levels=error%2Cdebug&live=0",
		"before=2026-10-25T00%3A30%3A00Z&open=aaaa1111.stdout.0.12"} {
		v, _ := url.ParseQuery(raw)
		q, bad := logsReqOf(v)
		if bad != "" {
			t.Fatalf("%q: %s", raw, bad)
		}
		again, bad := logsReqOf(q.values())
		if bad != "" || again != q {
			t.Errorf("%q: %+v then %+v (%s)", raw, q, again, bad)
		}
	}
	// A bare /logs is the default address.
	if q, _ := logsReqOf(url.Values{}); q.href() != "/logs" {
		t.Errorf("the default query's href = %q", q.href())
	}
}

func TestLogsFirstSeenNote(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	mk := func(text string, at time.Time, repeat int) service.LogEntry {
		return service.LogEntry{Line: logline.Line{Text: text, Time: at, Level: logline.Error, Repeat: repeat}}
	}
	lines := []service.LogEntry{mk("boom", now.Add(-time.Minute), 2), mk("other", now.Add(-2*time.Minute), 0), mk("boom", now.Add(-time.Hour), 0)}
	n := firstSeenOf(lines, lines[0], now, true)
	if n.Lead != "First seen " || n.Clock == nil || n.Clock.Text != "11:00 UTC" || n.Tail != " today, 4 times since." {
		t.Errorf("note = %+v %v", n, n.Clock)
	}
	if n := firstSeenOf(lines, lines[1], now, false); n.Lead != "Seen once in what was read." || n.Clock != nil {
		t.Errorf("note = %+v", n)
	}
	old := []service.LogEntry{mk("boom", now.Add(-30*time.Hour), 0), mk("boom", now.Add(-time.Hour), 0)}
	if n := firstSeenOf([]service.LogEntry{old[1], old[0]}, old[1], now, false); n.Tail != ", 2 times since (in what was read)." || n.Clock.Text != "yesterday 06:00 UTC" {
		t.Errorf("note = %+v %v", n, n.Clock)
	}
}

// Live and Paused (T015): the head's markup for both states, the script that adds the fresh veil and
// the count of new lines, and a page that draws no script of its own.
func TestLogsPageLiveAndPausedHeads(t *testing.T) {
	e := logsPageEnv(t)

	// Live: the stream's head says so and has no count; the region refreshes itself.
	live := e.pageOf(t, "/logs")
	lgWant(t, "/logs", live, "streaming from 4 copies", `hx-trigger="sse:logs"`)
	lgNot(t, "/logs", live, "data-logs-feed", "logs-paused-n")

	// Paused: "paused" for everyone, and for script a hidden count that Show answers. The page keeps
	// the stream open (live=1: the service follows) so the notice can reach the script, but nothing
	// swaps: the region has no refresh.
	paused := e.pageOf(t, "/logs?live=0&service=blog%2Fweb")
	head := regexp.MustCompile(`(?s)<div class="cv-lstreamhead">.*?</div>`).FindString(paused)
	lgWant(t, "the paused head", head, "<span>paused</span>",
		`<span class="cv-lpaused" data-logs-feed data-count-href="/parts/logs?live=0&amp;service=blog%2Fweb" hx-trigger="sse:logs" hidden>`,
		`<span id="logs-paused-n"></span>`, `<span id="logs-paused-unit">lines</span>`, `<a href="/logs?service=blog%2Fweb">Show</a>`)
	lgNot(t, "the paused head", text(head), "streaming from")
	lgWant(t, "the paused page", paused, `sse-connect="/live?environment=production&amp;live=1&amp;project=default&amp;watch=logs"`)
	lgNot(t, "the paused page", paused, `id="logs-live" hx-get`)

	// An open row is paused too, and Show drops it. A page of older lines, and the around view, count nothing.
	open := e.pageOf(t, "/logs?open="+e.lineID(3))
	lgWant(t, "the open page", open, `<a href="/logs">Show</a>`, `data-count-href="/parts/logs?live=0"`)
	around := e.pageOf(t, "/logs?copy=aaaa1111&at="+e.lineID(3))
	lgNot(t, "the around view", around, "data-logs-feed", "sse-connect")
	older := e.pageOf(t, "/logs?live=0&before="+url.QueryEscape(e.now.Add(-4*time.Minute).Format(time.RFC3339)))
	lgNot(t, "an older page", older, "data-logs-feed", "sse-connect")
	lgWant(t, "an older page", text(older), "Lines before ")
}

func TestLogsScriptIsOursAndServed(t *testing.T) {
	e := logsPageEnv(t)
	body := e.pageOf(t, "/logs")
	m := regexp.MustCompile(`<script src="(/static/js/logs\.js\?v=[0-9a-f]{12})" defer></script>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("/logs doesn't load logs.js:\n%s", body[:min(len(body), 900)])
	}
	resp, js := e.get(t, m[1])
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("logs.js = %d as %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("logs.js's policy = %q", csp)
	}
	// What the script does, and doesn't: a line's text is never made into markup, and the veil and
	// the count are the classes and ids the page and stylesheet use.
	lgWant(t, "logs.js", js, `"use strict";`, "cv-row-fresh", "logs-paused-n", "data-logs-feed", "sse:logs", "htmx:afterSwap")
	lgNot(t, "logs.js", js, "innerHTML", "outerHTML", "insertAdjacentHTML", "eval(", "document.write")
	if !strings.Contains(js, "textContent") {
		t.Error("logs.js doesn't write its numbers with textContent")
	}
	// It is a file of ours the vendor check pins.
	versions, err := os.ReadFile("../../tools/ui-vendor.versions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(versions), "logs.js ours - - ") {
		t.Error("logs.js isn't listed as ours in tools/ui-vendor.versions")
	}
	// The stylesheet has the veil the script adds, and drops it under reduced motion.
	_, css := e.get(t, "/static/app.css")
	lgWant(t, "app.css", css, ".cv-row-fresh")
}

// The page has no script of its own: every <script> loads a file under /static, so the console's
// policy (script-src 'self') holds without a nonce or a hash.
func TestLogsPagesHaveNoInlineScript(t *testing.T) {
	e := logsPageEnv(t)
	inline := regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>`)
	for _, path := range []string{"/logs", "/logs?live=0", "/logs?open=" + e.lineID(3), "/logs?copy=aaaa1111&at=" + e.lineID(3), "/parts/logs"} {
		_, body := e.get(t, path)
		for _, tag := range inline.FindAllString(body, -1) {
			if !strings.Contains(tag, ` src="/static/js/`) {
				t.Errorf("GET %s has a script that isn't a /static file: %s", path, tag)
			}
		}
		if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(body) {
			t.Errorf("GET %s has an inline event handler", path)
		}
		if strings.Contains(body, "javascript:") {
			t.Errorf("GET %s has a javascript: URL", path)
		}
	}
}
