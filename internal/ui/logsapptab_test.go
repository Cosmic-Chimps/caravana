package ui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The app's Logs tab (feature 024, T017): the Logs page filtered to the app, inside the app's shell.

func TestAppLogsTabIsTheLogsPage(t *testing.T) {
	e := logsPageEnv(t)
	// A second project, so /logs would draw the place bars: the tab never does.
	if _, err := e.Svc.CreateProject(context.Background(), "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	body := e.pageOf(t, "/apps/blog/logs")
	got := text(body)

	// Inside the app's shell: the app's header with the Logs tab current, the app's crumbs.
	lgWant(t, "the tab", body, `class="cv-apphead"`, `id="tab-logs" aria-current="page"`, `class="cv-page cv-page-app"`,
		`<div class="cv-applogs">`)
	lgNot(t, "the tab", body, "cv-page-logs", `<h1 class="cv-headline">Logs</h1>`)
	// The head is a section title, and its sentence ends with "All logs", the app's place on /logs.
	lgWant(t, "the tab", body, `<h2 class="cv-pgtitle">Logs</h2>`,
		`Deploys show up in the stream. <a href="/logs?project=default&amp;environment=production">All logs</a></p>`)
	// No place bars, whatever the places.
	lgNot(t, "the tab", body, `aria-label="Project"`, `aria-label="Environment"`, "cv-logfilter-div")

	// The rail lists the app's services only, with no group head; the rows name the service alone.
	rail := regexp.MustCompile(`(?s)<nav class="cv-lograil".*?</nav>`).FindString(body)
	if r := text(rail); !strings.Contains(r, "Services all services web 2 worker 1") || strings.Contains(r, "docs") {
		t.Errorf("the tab's rail reads %q, want blog's web and worker only", r)
	}
	lgNot(t, "the rail", rail, "cv-lrailgroup")
	lgWant(t, "the rail", rail, `href="/apps/blog/logs?service=blog%2Fweb" class="cv-lrailitem cv-hue-`, `href="/apps/blog/logs" class="cv-lrailitem cv-on"`)
	lgWant(t, "the tab", got, "retry failed", "worker tick", "web b up")
	lgNot(t, "the tab", got, "docs ready", "docs broke")
	lgWant(t, "the tab", body, `<span class="cv-svcname">web</span>`)

	// The deploy rows are the app's.
	lgWant(t, "the tab", got, "blog v7 started rolling out", "blog v8 failed its health checks · rolled back to v7")
	lgNot(t, "the tab", got, "docs v2")

	// Every link and form stays on the tab; the app and its place are the path's, never the query's.
	lgWant(t, "the tab", body, `<form method="get" action="/apps/blog/logs">`, `<form class="cv-logfilter" method="get" action="/apps/blog/logs"`,
		`href="/apps/blog/logs?range=1h" class="cv-seg-item"`, `href="/apps/blog/logs?levels=warn%2Cinfo" class="cv-lvlchip cv-lvl-error cv-on"`,
		`href="/apps/blog/logs?live=0&amp;open=`+e.lineID(5))
	lgNot(t, "the tab", body, `name="app"`, `name="project"`)
	if n := strings.Count(body, `href="/logs?`); n != 1 {
		t.Errorf("the tab has %d links to /logs, want only All logs", n)
	}
	// Download and the region are the environment's routes, narrowed to the app.
	lgWant(t, "the tab", body, `href="/logs/download?app=blog" class="cv-btn cv-btn-secondary cv-btn-sm"`,
		`id="logs-live" hx-get="/parts/logs?app=blog" hx-trigger="sse:logs" hx-swap="innerHTML"`)
	// The app's own stream (for the header), with the logs notice and the place followed while live.
	lgWant(t, "the tab", body, `sse-connect="/live/apps/blog?watch=logs&amp;live=1"`, `hx-trigger="sse:app, sse:deleted, sse:sync"`)
	// Only this tab of the app loads logs.js.
	lgWant(t, "the tab", body, `<script src="/static/js/logs.js?v=`)
	lgNot(t, "the Running tab", e.pageOf(t, "/apps/blog"), "js/logs.js")

	// The old tab's Copy and Stream selects and its script-drawn panel are gone.
	lgNot(t, "the tab", body, `name="copy"`, `name="stream"`, "cv-logbar", "cv-logpick", "data-log", `sse-swap="log`, "All copies")
}

func TestAppLogsTabFilters(t *testing.T) {
	e := logsPageEnv(t)
	// A service of the app, a copy, a search: as on /logs, on the tab's path.
	body := e.pageOf(t, "/apps/blog/logs?service=blog%2Fweb&copy=bbbb2222")
	lgWant(t, "?copy", body, "web b up", `class="cv-lrailcopy cv-on" aria-current="true"`, `href="/apps/blog/logs?service=blog%2Fweb" class="cv-lrailcopy cv-on"`)
	lgNot(t, "?copy", body, "retry failed", "worker tick")
	lgWant(t, "?q", text(e.pageOf(t, "/apps/blog/logs?q=retry")), "2 lines", "retry failed")

	// The address can't take the tab out of its app or its place.
	other := e.pageOf(t, "/apps/blog/logs?app=docs&project=shop")
	lgWant(t, "?app=docs", text(other), "retry failed")
	lgNot(t, "?app=docs", text(other), "docs broke")
	if resp, _ := e.get(t, "/apps/blog/logs?service=docs%2Fweb"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("another app's service on the tab = %d, want 400", resp.StatusCode)
	}

	// Paused, an open row, the lines around one: the tab's own addresses, with the app's stream.
	paused := e.pageOf(t, "/apps/blog/logs?live=0")
	lgWant(t, "?live=0", paused, `data-count-href="/parts/logs?app=blog&amp;live=0"`, `<a href="/apps/blog/logs">Show</a>`,
		`sse-connect="/live/apps/blog?watch=logs&amp;live=1"`)
	open := e.pageOf(t, "/apps/blog/logs?open="+e.lineID(9))
	strip := regexp.MustCompile(`(?s)<section class="cv-rowstrip" id="open-line".*?</section>`).FindString(open)
	lgWant(t, "the strip", strip, `href="/apps/blog/logs?at=`+e.lineID(9)+`&amp;copy=aaaa1111&amp;live=0"`, "/apps/blog/logs?live=0&amp;open=")
	around := e.pageOf(t, "/apps/blog/logs?copy=aaaa1111&at="+e.lineID(3))
	lgWant(t, "the around view", around, `<a href="/apps/blog/logs">Back to the stream</a>`, `sse-connect="/live/apps/blog?watch=logs"`)
	lgNot(t, "the around view", around, "live=1")

	// Refusals: a bad address is 400, an app that isn't there the app's 404 page.
	for path, want := range map[string]int{"/apps/blog/logs?range=2d": 400, "/apps/blog/logs?copy=ffff9999": 404, "/apps/nope/logs": 404} {
		if resp, _ := e.get(t, path); resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestAppLogsTabInAnotherPlace(t *testing.T) {
	e := newConsoleEnv(t)
	ctx := context.Background()
	if _, err := e.Svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	id := "shop--staging--web"
	e.seed(t, domainsApp(id), seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 3})
	e.Nomad.SetCopies(id, api.AllocationListStub{ID: lgDocs, TaskGroup: "web", JobVersion: 3, ClientStatus: "running", NodeName: "n", TaskStates: lgStarted("web", time.Now())})
	at := time.Now().UTC().Add(-time.Minute)
	e.Nomad.AddLogFile(lgDocs, "stdout", 0, []string{at.Format(lgLayout) + " INFO staging up"}, at.Add(5*time.Second))

	body := e.pageOf(t, "/apps/"+id+"/logs")
	lgWant(t, "the tab", body, `<a href="/logs?project=shop&amp;environment=staging">All logs</a>`,
		`hx-get="/parts/logs?app=shop--staging--web&amp;environment=staging&amp;project=shop"`,
		`href="/logs/download?app=shop--staging--web&amp;environment=staging&amp;project=shop"`,
		`href="/apps/shop--staging--web/logs?range=1h"`, `sse-connect="/live/apps/shop--staging--web?watch=logs&amp;live=1"`)
	lgWant(t, "the tab", text(body), "staging up", "web v3 started rolling out")

	// The part the tab refreshes from draws the app's lines.
	resp, part := e.get(t, "/parts/logs?app="+id+"&project=shop&environment=staging")
	if resp.StatusCode != http.StatusOK || !strings.Contains(part, "staging up") {
		t.Errorf("the tab's part = %d:\n%s", resp.StatusCode, text(part))
	}
}

func TestAppLogsTabPartIsTheApps(t *testing.T) {
	e := logsPageEnv(t)
	resp, part := e.get(t, "/parts/logs?app=blog")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("part = %d", resp.StatusCode)
	}
	lgWant(t, "the part", part, "retry failed", "worker tick", "cv-hist", "cv-logstream")
	lgNot(t, "the part", part, "docs broke", "docs v2", "cv-lograil", "cv-apphead", "<html")
}

// The tab's stream is the app's (/live/apps/{id}): with watch=logs it forwards the logs notice, and with
// live=1 besides it follows the app's place while open.
func TestAppLogsTabStream(t *testing.T) {
	e := logsPageEnv(t)
	var placeSeen atomic.Value
	var open, done atomic.Int32
	e.Server.live.watchLogs = func(project, environment string) func() {
		placeSeen.Store(project + "/" + environment)
		open.Add(1)
		return func() { done.Add(1) }
	}
	ls := openStream(t, e.URL, "/live/apps/blog?watch=logs&live=1", e.Cookie.Value)
	ls.expect("sync")
	if open.Load() != 1 || placeSeen.Load() != "default/production" {
		t.Fatalf("watched %d times, for %v", open.Load(), placeSeen.Load())
	}
	ls.close()
	waitFor(t, "done", func() bool { return done.Load() == 1 })

	for _, path := range []string{"/live/apps/blog?watch=logs", "/live/apps/blog", "/live/apps/blog?tab=running"} {
		open.Store(0)
		ls := openStream(t, e.URL, path, e.Cookie.Value)
		ls.expect("sync")
		ls.close()
		if open.Load() != 0 {
			t.Errorf("%s followed the place", path)
		}
	}

	// The notice itself, from a line a copy of the app wrote.
	e.Server.live.watchLogs = nil
	e.Svc.SetLogWatchGaps(50*time.Millisecond, time.Hour)
	ls = openStream(t, e.URL, "/live/apps/blog?watch=logs&live=1", e.Cookie.Value)
	ls.expect("sync")
	waitFor(t, "the copies' streams", func() bool { return e.Nomad.OpenLogStreams() == 8 })
	e.Nomad.WriteLog(lgBlogA, "stdout", "ERROR "+lgSecret+"\n")
	for {
		ev := ls.next()
		if ev == "logs" {
			break
		}
		if ev == "" {
			t.Fatal("the stream ended before the logs notice")
		}
	}
	ls.close()
	waitFor(t, "the followers to end", func() bool { return e.Nomad.OpenLogStreams() == 0 })
	if strings.Contains(e.Logs.String(), lgSecret) {
		t.Error("a line reached the server's log")
	}
}

func TestAppLogsTabHasNoInlineStyleOrScript(t *testing.T) {
	e := logsPageEnv(t)
	style := regexp.MustCompile(lgNoStyle)
	inline := regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>`)
	for _, path := range []string{"/apps/blog/logs", "/apps/blog/logs?live=0&open=" + e.lineID(3), "/apps/blog/logs?copy=aaaa1111&at=" + e.lineID(3)} {
		_, body := e.get(t, path)
		if m := style.FindString(body); m != "" {
			t.Errorf("GET %s has an inline style attribute (%q)", path, m)
		}
		for _, tag := range inline.FindAllString(body, -1) {
			if !strings.Contains(tag, ` src="/static/js/`) {
				t.Errorf("GET %s has a script that isn't a /static file: %s", path, tag)
			}
		}
	}
}
