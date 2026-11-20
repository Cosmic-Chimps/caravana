package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 047: /requests, the HTTP requests Traefik served, read from its access log in Nomad's files.
// The environment has blog (web with a domain, and a worker) and docs (web with a domain); Traefik
// writes the access log and one allocation of it runs.

const (
	rqTraefik     = "7a1c9e22-0000-4000-8000-0000000000aa"
	rqSentinel    = "s3cret-sentinel-047"
	rqSentinelURL = "/s3cret-sentinel-047-path"
)

// rqLine is one line of Traefik's access log as the lab writes it: start is when the request began
// (its own "time" is when it ended, in seconds). svc is Traefik's service name without "@nomad"; ""
// is a request no router took.
func rqLine(start time.Time, dur time.Duration, svc, method, path string, status int, client string) string {
	names := ""
	if svc != "" {
		names = fmt.Sprintf(`"RouterName":%q,"ServiceName":%q,`, svc+"@nomad", svc+"@nomad")
	}
	return fmt.Sprintf(`{"ClientHost":%q,"DownstreamContentSize":%d,"DownstreamStatus":%d,"Duration":%d,`+
		`"RequestHost":"x.example.com","RequestMethod":%q,"RequestPath":%q,"RetryAttempts":0,%s"StartUTC":%q,`+
		`"TLSVersion":"1.3","entryPointName":"websecure","level":"info","msg":"","time":%q}`,
		client, 398, status, dur.Nanoseconds(), method, path, names,
		start.UTC().Format(time.RFC3339Nano), start.Add(dur).UTC().Format(time.RFC3339))
}

type rqEnv struct {
	*consoleEnv
	now time.Time
}

// ago is the time d before the env's now.
func (e *rqEnv) ago(d time.Duration) time.Time { return e.now.Add(-d) }

// requestsPageEnv has the two apps and these requests, oldest first as the file holds them:
//
//	-9m  blog web   GET  /health            200  10.0.0.1  20 ms
//	-8m  blog web   POST /api/login         500  1.1.1.1   1.2 s
//	-7m  blog web   GET  /missing           404  2.2.2.2   5 ms
//	-6m  docs web   GET  /docs/intro        200  3.3.3.3   300 ms
//	-5m  console    GET  /console           200  (caravana@file: nobody's)
//	-4m  blog web   GET  /s3cret-sentinel-047-path 200 1.1.1.1
//	-3m  docs web   GET  /boom              502  3.3.3.3   2 s
//	-2m  blog web   GET  /<script>alert(1)</script> 200 1.1.1.1
func requestsPageEnv(t *testing.T) *rqEnv {
	t.Helper()
	e := &rqEnv{consoleEnv: newConsoleEnv(t), now: time.Now().UTC()}
	e.seed(t, blogApp(), seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 7})
	e.seed(t, domainsApp("docs", "docs.example.com"), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 2})
	e.Nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true", "--accesslog.format=json")
	stub := api.AllocationListStub{ID: rqTraefik, TaskGroup: "traefik", JobVersion: 1, ClientStatus: "running",
		CreateTime: e.now.Add(-48 * time.Hour).UnixNano(), TaskStates: map[string]*api.TaskState{"traefik": {StartedAt: e.now.Add(-time.Hour)}}}
	e.Nomad.SetCopies("traefik", stub)
	e.Nomad.AddLogFile(rqTraefik, "stdout", 0, []string{
		rqLine(e.ago(9*time.Minute), 20*time.Millisecond, "blog-web", "GET", "/health", 200, "10.0.0.1"),
		rqLine(e.ago(8*time.Minute), 1200*time.Millisecond, "blog-web", "POST", "/api/login", 500, "1.1.1.1"),
		rqLine(e.ago(7*time.Minute), 5*time.Millisecond, "blog-web", "GET", "/missing", 404, "2.2.2.2"),
		rqLine(e.ago(6*time.Minute), 300*time.Millisecond, "docs-web", "GET", "/docs/intro", 200, "3.3.3.3"),
		rqLine(e.ago(5*time.Minute), time.Millisecond, "caravana@file", "GET", "/console", 200, "9.9.9.9"),
		rqLine(e.ago(4*time.Minute), time.Millisecond, "blog-web", "GET", rqSentinelURL, 200, "1.1.1.1"),
		rqLine(e.ago(3*time.Minute), 2*time.Second, "docs-web", "GET", "/boom", 502, "3.3.3.3"),
		rqLine(e.ago(2*time.Minute), time.Millisecond, "blog-web", "GET", "/<script>alert(1)</script>", 200, "1.1.1.1"),
	}, e.now)
	return e
}

func (e *rqEnv) noAccessLog() { e.Nomad.SetTraefikJob("traefik:v3.7.13") }

// rowsOf is the table's rows' paths in the order drawn.
func rqPaths(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<span class="cv-rqpath">([^<]*)</span>`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func sameList(got, want []string) bool { return strings.Join(got, "|") == strings.Join(want, "|") }

// ---- The page -----------------------------------------------------------------------------------

func TestRequestsPageShape(t *testing.T) {
	e := requestsPageEnv(t)
	body := e.pageOf(t, "/requests")
	got := text(body)

	lgWant(t, "/requests", got, "Requests", "Each HTTP request Traefik served, newest first. Only the path is recorded, never the query string or headers.",
		"times in UTC", "following Traefik", "7 requests")
	lgWant(t, "/requests", body, `<a href="/requests" class="cv-nav-item" aria-current="page">`, `class="cv-page cv-page-logs"`)
	if want := []string{"/<script>alert(1)</script>", "/boom", rqSentinelURL, "/docs/intro", "/missing", "/api/login", "/health"}; !sameList(rqPaths(body), []string{
		"/&lt;script&gt;alert(1)&lt;/script&gt;", "/boom", rqSentinelURL, "/docs/intro", "/missing", "/api/login", "/health"}) {
		t.Errorf("rows = %q, want %q newest first, with nobody's console request left out", rqPaths(body), want)
	}

	// Two apps in the read: rows name "<app>/<service>", each with its hue.
	lgWant(t, "/requests", body, `<span class="cv-lsvc cv-hue-`, `<span class="cv-svcname">blog/web</span>`, `<span class="cv-svcname">docs/web</span>`)
}

func TestRequestsPageRows(t *testing.T) {
	e := requestsPageEnv(t)
	body := e.pageOf(t, "/requests")

	lgWant(t, "rows", body, `<span class="cv-rqstatus cv-rqs-5xx">500</span>`, `<span class="cv-rqstatus cv-rqs-4xx">404</span>`,
		`<span class="cv-rqstatus cv-rqs-2xx">200</span>`, `<span class="cv-rqstatus cv-rqs-5xx">502</span>`)
	lgWant(t, "rows", body, `class="cv-rqrow cv-row-error"`, `<span class="cv-rqmethod">POST</span>`, `<span class="cv-rqhost">x.example.com</span>`,
		`<span class="cv-rqdur cv-rqr cv-rqdur-slow">1.20 s</span>`, `<span class="cv-rqdur cv-rqr">20 ms</span>`, `<span class="cv-rqdur cv-rqr">5.0 ms</span>`,
		`<span class="cv-rqbytes cv-rqr">398 B</span>`, `<span class="cv-rqclient">10.0.0.1</span>`)
	// The header row of the table.
	lgWant(t, "rows", body, `<span>Time</span><span>Service</span><span>Method</span><span>Host and path</span><span>Status</span><span class="cv-rqr">Took</span><span class="cv-rqr">Size</span><span>Client</span>`)

	// A row links to the Logs around its time for its service: a minute either side, in RFC 3339.
	failed := regexp.MustCompile(`href="([^"]*)" class="cv-rqrow cv-row-error" title="Logs around this request">\s*<time class="cv-ltime" datetime="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(failed) != 2 {
		t.Fatalf("%d failed rows, want 2:\n%s", len(failed), text(body))
	}
	u, err := url.Parse(strings.ReplaceAll(failed[0][1], "&amp;", "&")) // /boom is the newer: drawn first
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, failed[0][2])
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/logs" || u.Query().Get("service") != "docs/web" {
		t.Errorf("row link = %s, want /logs for docs/web", failed[0][1])
	}
	from, to := mustTime(t, u.Query().Get("from")), mustTime(t, u.Query().Get("to"))
	if d := at.Sub(from); d < time.Minute || d > time.Minute+time.Second || to.Sub(at) < time.Minute || to.Sub(at) > time.Minute+2*time.Second {
		t.Errorf("window %s to %s around %s, want a minute either side", from, to, at)
	}
	// ...and that address is a Logs page that reads that window, paused, with no Live button.
	logs := e.pageOf(t, strings.ReplaceAll(failed[0][1], "&amp;", "&"))
	lgWant(t, "logs around a request", logs, "in this range")
	lgNot(t, "logs around a request", logs, "cv-livebtn")
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time %q: %v", s, err)
	}
	return v
}

func TestRequestsPageHistogramIsStackedByClass(t *testing.T) {
	e := requestsPageEnv(t)
	body := e.pageOf(t, "/requests")
	hist := regexp.MustCompile(`(?s)<section class="cv-panel cv-hist".*?</section>`).FindString(body)
	lgWant(t, "the histogram", hist, `viewBox="0 0 300 48"`, `aria-label="7 requests in the last 15 minutes, 2 5xx, 1 4xx"`,
		`<rect class="cv-hbar" `, `<rect class="cv-hbar-warn" `, `<rect class="cv-hbar-err" `, `<th scope="col">5xx</th>`, `now</span>`)
	if n := strings.Count(hist, `<rect class="cv-hbar-err" `); n != 2 {
		t.Errorf("%d 5xx segments, want 2 (the two bins that have one)", n)
	}
	lgNot(t, "the histogram", hist, "cv-hbar-unread")
	if m := regexp.MustCompile(lgNoStyle).FindString(body); m != "" {
		t.Errorf("an inline style attribute: %q", m)
	}
}

func TestRequestsPageFilters(t *testing.T) {
	e := requestsPageEnv(t)

	// Status chips are links: from no filter the first click turns that chip off.
	body := e.pageOf(t, "/requests")
	lgWant(t, "chips", body, `href="/requests?status=3xx%2C4xx%2C5xx" class="cv-lvlchip cv-rqchip-2xx cv-on"`,
		`href="/requests?status=2xx%2C3xx%2C5xx" class="cv-lvlchip cv-rqchip-4xx cv-on"`,
		`class="cv-lvlchip cv-rqchip-5xx cv-on" title="Hide 5xx requests">5xx <span class="cv-lvlchip-n">2</span>`,
		`class="cv-lvlchip cv-rqchip-4xx cv-on" title="Hide 4xx requests">4xx <span class="cv-lvlchip-n">1</span>`)

	// 5xx only: the Monitoring link's address.
	body = e.pageOf(t, "/requests?status=5xx")
	if !sameList(rqPaths(body), []string{"/boom", "/api/login"}) {
		t.Errorf("status=5xx rows = %q", rqPaths(body))
	}
	// The only chip on can't be turned off: its link leaves the filter as it is.
	lgWant(t, "chips", body, `href="/requests?status=5xx" class="cv-lvlchip cv-rqchip-5xx cv-on"`,
		`href="/requests?status=2xx%2C5xx" class="cv-lvlchip cv-rqchip-2xx"`)

	// 4xx and 5xx together, then the one that is last on can't be turned off.
	body = e.pageOf(t, "/requests?status=4xx,5xx")
	if !sameList(rqPaths(body), []string{"/boom", "/missing", "/api/login"}) {
		t.Errorf("status=4xx,5xx rows = %q", rqPaths(body))
	}

	// Method, path prefix, minimum duration and client are the form's fields, carrying the rest.
	form := regexp.MustCompile(`(?s)<form class="cv-logfilter cv-rqfilter".*?</form>`).FindString(e.pageOf(t, "/requests?status=5xx&range=1h"))
	lgWant(t, "the form", form, `action="/requests"`, `<input type="hidden" name="status" value="5xx">`, `<input type="hidden" name="range" value="1h">`,
		`<select class="cv-field cv-select cv-rqselect" name="method">`, `<option value="" selected>any method</option>`, `<option value="POST">POST</option>`,
		`<select class="cv-field cv-select cv-rqselect" name="minDuration">`, `<option value="1000">1 s or more</option>`,
		`<input class="cv-logsearch-input" type="text" name="path"`, `<input class="cv-logsearch-input" type="text" name="client"`, `>Filter</button>`)
	lgNot(t, "the form", form, `name="method" value`, `>Clear</a>`)

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"method=POST", []string{"/api/login"}},
		{"method=post", []string{"/api/login"}},
		{"path=/docs", []string{"/docs/intro"}},
		{"path=/s3cret", []string{rqSentinelURL}},
		{"minDuration=1000", []string{"/boom", "/api/login"}},
		{"minDuration=250", []string{"/boom", "/docs/intro", "/api/login"}},
		{"client=3.3.3.3", []string{"/boom", "/docs/intro"}},
		{"status=4xx&client=3.3.3.3", nil},
		{"service=blog%2Fweb&status=5xx", []string{"/api/login"}},
		{"app=docs", []string{"/boom", "/docs/intro"}},
	} {
		body := e.pageOf(t, "/requests?"+tc.query)
		if !sameList(rqPaths(body), tc.want) {
			t.Errorf("?%s rows = %q, want %q", tc.query, rqPaths(body), tc.want)
		}
	}
	// The fields are shown as set, with a way to clear them that keeps the rest.
	body = e.pageOf(t, "/requests?method=POST&path=/api&minDuration=500&client=1.1.1.1&status=5xx")
	lgWant(t, "the form", body, `<option value="POST" selected>POST</option>`, `<option value="500" selected>500 ms or more</option>`,
		`name="path" value="/api"`, `name="client" value="1.1.1.1"`, `<a href="/requests?status=5xx" class="cv-rqclear">Clear</a>`)
	// A duration the page doesn't offer is still shown as chosen.
	lgWant(t, "the form", e.pageOf(t, "/requests?minDuration=250"), `<option value="250" selected>250 ms or more</option>`)

	// Nothing matches: the empty stream says so.
	lgWant(t, "no match", text(e.pageOf(t, "/requests?path=/nowhere")), "No requests match. Try a wider time range or fewer filters.")
}

func TestRequestsPageRail(t *testing.T) {
	e := requestsPageEnv(t)
	rail := regexp.MustCompile(`(?s)<nav class="cv-lograil".*?</nav>`).FindString(e.pageOf(t, "/requests"))
	if r := text(rail); !strings.Contains(r, "Services all services blog blog/web 5 docs docs/web 2") || strings.Contains(r, "worker") {
		t.Errorf("rail reads %q, want blog's web (5) and docs' web (2), no worker", r)
	}
	lgWant(t, "the rail", rail, `href="/requests?service=blog%2Fweb" class="cv-lrailitem cv-hue-`, `href="/requests" class="cv-lrailitem cv-on"`)
	// The rail's counts are after the filters but the service one: choosing a service keeps the other's count.
	rail = regexp.MustCompile(`(?s)<nav class="cv-lograil".*?</nav>`).FindString(e.pageOf(t, "/requests?service=blog%2Fweb&status=5xx"))
	if r := text(rail); !strings.Contains(r, "blog blog/web 1 docs docs/web 1") {
		t.Errorf("rail with 5xx reads %q", r)
	}
	lgWant(t, "the rail", rail, `class="cv-lrailitem cv-hue-`, `aria-current="true"`)
}

func TestRequestsPageRangeAndLive(t *testing.T) {
	e := requestsPageEnv(t)
	body := e.pageOf(t, "/requests")
	lgWant(t, "range bar", body, `href="/requests?range=1h" class="cv-seg-item"`, `href="/requests" class="cv-seg-item cv-on" aria-current="true">15 min`)
	lgWant(t, "live", body, `name="live" value="0"`, `aria-pressed="true"`,
		`id="requests-live" hx-get="/parts/requests" hx-trigger="sse:requests" hx-swap="innerHTML"`, `sse-connect="/live?environment=production&amp;live=1&amp;project=default&amp;watch=requests"`)

	// Paused: no stream, no refresh, a button that follows again.
	body = e.pageOf(t, "/requests?live=0&status=5xx")
	lgWant(t, "paused", body, `name="live" value="1"`, `aria-pressed="false"`, "paused")
	lgNot(t, "paused", body, `hx-trigger="sse:requests"`, "sse-connect")

	// A window the address gives: read, not followed, no Live, its end labelled.
	from, to := e.ago(10*time.Minute).Truncate(time.Second), e.ago(5*time.Minute).Truncate(time.Second)
	body = e.pageOf(t, "/requests?from="+from.Format(time.RFC3339)+"&to="+to.Format(time.RFC3339))
	lgWant(t, "window", body, `in this range`, to.Format("15:04")+" UTC")
	lgNot(t, "window", body, "cv-livebtn", "sse-connect", `cv-seg-item cv-on`)
	if !sameList(rqPaths(body), []string{"/docs/intro", "/missing", "/api/login", "/health"}) {
		t.Errorf("window rows = %q", rqPaths(body))
	}
	// A range's link leaves the window.
	lgWant(t, "window", body, `href="/requests?range=1h" class="cv-seg-item"`)
}

func TestRequestsBadAddresses(t *testing.T) {
	e := requestsPageEnv(t)
	for _, q := range []string{"range=2d", "status=9xx", "method=get%20it", "path=nope", "minDuration=-3", "minDuration=x", "before=yesterday",
		"from=2026-10-25T00:00:00Z", "from=2026-10-25T01:00:00Z&to=2026-10-25T00:00:00Z", "client=" + strings.Repeat("a", 65)} {
		if resp, body := e.get(t, "/requests?"+q); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET ?%s = %d, want 400:\n%s", q, resp.StatusCode, text(body))
		}
		if resp, _ := e.get(t, "/parts/requests?"+q); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /parts/requests?%s = %d, want 400", q, resp.StatusCode)
		}
	}
	if resp, _ := e.get(t, "/requests?project=nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown project = %d, want 404", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/requests?app=nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown app = %d, want 404", resp.StatusCode)
	}
}

// ---- The app's tab ------------------------------------------------------------------------------

func TestAppRequestsTab(t *testing.T) {
	e := requestsPageEnv(t)
	if _, err := e.Svc.CreateProject(context.Background(), "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	body := e.pageOf(t, "/apps/blog/requests")

	// Inside the app's shell: the header with the Requests tab current, after Logs.
	lgWant(t, "the tab", body, `class="cv-apphead"`, `id="tab-requests" aria-current="page"`, `class="cv-page cv-page-app"`, `<div class="cv-applogs">`)
	lgNot(t, "the tab", body, "cv-page-logs", `<h1 class="cv-headline">Requests</h1>`)
	tabs := regexp.MustCompile(`id="tab-([a-z]+)"`).FindAllStringSubmatch(body, -1)
	var keys []string
	for _, m := range tabs {
		keys = append(keys, m[1])
	}
	if got := strings.Join(keys, " "); !strings.Contains(got, "logs requests") {
		t.Errorf("tabs = %q, want Requests right after Logs", got)
	}
	// The head is a section title, with "All requests" at the end of the sentence; no place bars.
	lgWant(t, "the tab", body, `<h2 class="cv-pgtitle">Requests</h2>`,
		`never the query string or headers. <a href="/requests?project=default&amp;environment=production">All requests</a></p>`)
	lgNot(t, "the tab", body, `aria-label="Project"`, `aria-label="Environment"`)
	// Only the app's requests, naming the service alone.
	if !sameList(rqPaths(body), []string{"/&lt;script&gt;alert(1)&lt;/script&gt;", rqSentinelURL, "/missing", "/api/login", "/health"}) {
		t.Errorf("tab rows = %q", rqPaths(body))
	}
	lgWant(t, "the tab", body, `<span class="cv-svcname">web</span>`)
	lgNot(t, "the tab", body, "docs/web", "blog/web")
	// Every link and form stays on the tab; the app and its place are the path's.
	lgWant(t, "the tab", body, `<form method="get" action="/apps/blog/requests">`, `<form class="cv-logfilter cv-rqfilter" method="get" action="/apps/blog/requests"`,
		`href="/apps/blog/requests?range=1h" class="cv-seg-item"`, `href="/apps/blog/requests?status=3xx%2C4xx%2C5xx" class="cv-lvlchip cv-rqchip-2xx cv-on"`)
	lgNot(t, "the tab", body, `name="app"`, `name="project"`)
	if n := strings.Count(body, `href="/requests?`); n != 1 {
		t.Errorf("the tab has %d links to /requests, want only All requests", n)
	}
	// The region is the environment's route narrowed to the app; the app's own stream carries the notice.
	lgWant(t, "the tab", body, `id="requests-live" hx-get="/parts/requests?app=blog" hx-trigger="sse:requests" hx-swap="innerHTML"`,
		`sse-connect="/live/apps/blog?watch=requests&amp;live=1"`)
	// The tab loads no script of its own.
	lgNot(t, "the tab", body, "js/logs.js")
	// The rail lists the app's web service (the worker has no domain and no traffic).
	rail := regexp.MustCompile(`(?s)<nav class="cv-lograil".*?</nav>`).FindString(body)
	if r := text(rail); !strings.Contains(r, "Services all services web 5") || strings.Contains(r, "docs") || strings.Contains(r, "worker") {
		t.Errorf("the tab's rail reads %q", r)
	}

	// Filters narrow it; the tab of an app that isn't there is 404.
	if got := rqPaths(e.pageOf(t, "/apps/blog/requests?status=5xx")); !sameList(got, []string{"/api/login"}) {
		t.Errorf("tab 5xx rows = %q", got)
	}
	if resp, _ := e.get(t, "/apps/nope/requests"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an app that isn't there = %d, want 404", resp.StatusCode)
	}
	// The other tabs are not Requests.
	lgNot(t, "the Running tab", e.pageOf(t, "/apps/blog"), `id="tab-requests" aria-current`)
	lgWant(t, "the Running tab", e.pageOf(t, "/apps/blog"), `id="tab-requests"`)
}

// ---- The states ---------------------------------------------------------------------------------

func TestRequestsPageStates(t *testing.T) {
	t.Run("the access log isn't on yet", func(t *testing.T) {
		e := requestsPageEnv(t)
		e.noAccessLog()
		body := e.pageOf(t, "/requests")
		lgWant(t, "page", text(body), "Traefik isn't writing its access log yet: rerun the installer, which restarts Traefik once.")
		lgNot(t, "page", body, "cv-rqrow", "cv-hist-svg", "No requests")
		resp, part := e.get(t, "/parts/requests")
		if resp.StatusCode != 200 || !strings.Contains(text(part), "Traefik isn't writing its access log yet") {
			t.Errorf("part = %d: %s", resp.StatusCode, text(part))
		}
	})
	t.Run("Traefik isn't running", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.get(t, "/requests")
		if resp.StatusCode != 200 {
			t.Fatalf("page = %d, want a calm 200", resp.StatusCode)
		}
		lgWant(t, "page", text(body), "Traefik isn't running, so there are no requests to show.")
		resp, part := e.get(t, "/parts/requests")
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(text(part), "Traefik isn't running") {
			t.Errorf("part = %d: %s", resp.StatusCode, text(part))
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("part cache-control = %q", resp.Header.Get("Cache-Control"))
		}
		// The app's tab says the same.
		e.seed(t, blogApp())
		lgWant(t, "tab", text(e.pageOf(t, "/apps/blog/requests")), "Traefik isn't running, so there are no requests to show.")
	})
	t.Run("nothing in the range", func(t *testing.T) {
		e := requestsPageEnv(t)
		e.Nomad.AddLogFile(rqTraefik, "stdout", 0, nil, e.now)
		lgWant(t, "empty", text(e.pageOf(t, "/requests")), "No requests yet. New requests appear here.", "no requests in this range")
		from, to := e.ago(3*time.Hour), e.ago(2*time.Hour)
		lgWant(t, "empty window", text(e.pageOf(t, "/requests?from="+from.Format(time.RFC3339)+"&to="+to.Format(time.RFC3339))), "No requests in this range.")
	})
	t.Run("read cut short by the allocation's start", func(t *testing.T) {
		e := requestsPageEnv(t)
		stub := api.AllocationListStub{ID: rqTraefik, TaskGroup: "traefik", JobVersion: 1, ClientStatus: "running",
			CreateTime: e.ago(12 * time.Minute).UnixNano(), TaskStates: map[string]*api.TaskState{"traefik": {StartedAt: e.ago(12 * time.Minute)}}}
		e.Nomad.SetCopies("traefik", stub)
		body := e.pageOf(t, "/requests")
		hist := regexp.MustCompile(`(?s)<section class="cv-panel cv-hist".*?</section>`).FindString(body)
		lgWant(t, "the histogram", hist, `<span class="cv-hlabel-read">read from `+e.ago(12*time.Minute).Format("15:04")+` UTC</span>`, "cv-hbar-unread")
		lgNot(t, "the histogram", hist, "not all read in time")
	})
	t.Run("the oldest Nomad keeps", func(t *testing.T) {
		e := requestsPageEnv(t)
		e.Nomad.AddLogFile(rqTraefik, "stdout", 3, []string{rqLine(e.ago(time.Minute), time.Millisecond, "blog-web", "GET", "/kept", 200, "1.1.1.1")}, e.now)
		e.Nomad.PurgeLogFile(rqTraefik, "stdout", 0)
		body := e.pageOf(t, "/requests?range=24h")
		lgWant(t, "dropped", text(body), "This is the oldest Nomad keeps: older requests were dropped.")
		if !sameList(rqPaths(body), []string{"/kept"}) {
			t.Errorf("rows = %q", rqPaths(body))
		}
	})
	t.Run("more than a page", func(t *testing.T) {
		e := requestsPageEnv(t)
		var lines []string
		for i := range 230 {
			lines = append(lines, rqLine(e.ago(14*time.Minute-time.Duration(i)*time.Second), time.Millisecond, "blog-web", "GET", fmt.Sprintf("/n/%03d", i), 200, "1.1.1.1"))
		}
		e.Nomad.AddLogFile(rqTraefik, "stdout", 0, lines, e.now)
		body := e.pageOf(t, "/requests")
		if n := len(rqPaths(body)); n != 200 {
			t.Errorf("%d rows, want the newest 200", n)
		}
		lgWant(t, "head", text(body), "230 requests", "newest 200 · Show older")
		older := regexp.MustCompile(`<a href="(/requests\?[^"]*before=[^"]*)"`).FindStringSubmatch(body)
		if older == nil {
			t.Fatalf("no Show older link:\n%s", text(body))
		}
		page := e.pageOf(t, strings.ReplaceAll(older[1], "&amp;", "&"))
		lgWant(t, "older", page, "Requests before", "Back to the newest")
		lgWant(t, "older", page, `name="live" value="1"`) // paused: its button follows again
		if rows := rqPaths(page); len(rows) != 30 || rows[len(rows)-1] != "/n/000" || rows[0] != "/n/029" {
			t.Errorf("older rows = %d, first %q last %q; want the 30 older ones, /n/029 down to /n/000", len(rows), rows[0], rows[len(rows)-1])
		}
	})
}

// ---- The part -----------------------------------------------------------------------------------

func TestRequestsPartAnswersTheRegion(t *testing.T) {
	e := requestsPageEnv(t)
	resp, part := e.get(t, "/parts/requests?status=5xx")
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("part = %d, cache-control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	lgWant(t, "the part", part, "cv-hist", "cv-logstream", "/api/login")
	lgNot(t, "the part", part, "<html", "<form", "cv-lhead", "cv-lograil", "cv-lfoot", "/health")
	if anon, _ := e.do(t, request{path: "/parts/requests"}); anon.StatusCode != http.StatusUnauthorized {
		t.Errorf("part without a session = %d, want 401", anon.StatusCode)
	}
	// The app tab's region is the same part narrowed to the app.
	_, part = e.get(t, "/parts/requests?app=docs")
	lgWant(t, "the part", part, "/boom")
	lgNot(t, "the part", part, "/api/login")
}

// ---- Secrets and markup -------------------------------------------------------------------------

// A path is whatever a person typed: it is drawn as escaped text, and it reaches nothing else.
func TestRequestsPageHoldsNoPathInTheLogOrAnError(t *testing.T) {
	e := requestsPageEnv(t)
	body := e.pageOf(t, "/requests")
	lgWant(t, "/requests", body, rqSentinelURL)
	lgNot(t, "/requests", body, "<script>alert")
	lgWant(t, "/requests", body, "&lt;script&gt;alert(1)&lt;/script&gt;")

	e.pageOf(t, "/requests?path="+url.QueryEscape(rqSentinelURL))
	e.get(t, "/parts/requests?path="+url.QueryEscape(rqSentinelURL))
	e.pageOf(t, "/apps/blog/requests?path="+url.QueryEscape(rqSentinelURL))
	// A refused address names the value and never repeats it.
	for _, q := range []string{"path=" + rqSentinel, "method=" + rqSentinel, "status=" + rqSentinel, "client=" + strings.Repeat(rqSentinel, 4), "minDuration=" + rqSentinel} {
		resp, page := e.get(t, "/requests?"+q)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET ?%s = %d, want 400", q, resp.StatusCode)
		}
		if strings.Contains(page, rqSentinel) {
			t.Errorf("the 400 page for ?%s repeats the value:\n%s", q, text(page))
		}
		if loc := resp.Header.Get("Location"); strings.Contains(loc, rqSentinel) {
			t.Errorf("the redirect holds the value: %s", loc)
		}
	}
	if strings.Contains(e.Logs.String(), rqSentinel) {
		t.Errorf("a request's path reached the server log:\n%s", e.Logs.String())
	}
	// The store holds none of it either.
	apps, err := e.Svc.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(apps), rqSentinel) {
		t.Error("a request's path reached the stored apps")
	}
}

func TestRequestsPagesHaveNoInlineStyleOrScript(t *testing.T) {
	e := requestsPageEnv(t)
	style := regexp.MustCompile(lgNoStyle)
	for _, path := range []string{"/requests", "/requests?status=5xx", "/requests?live=0", "/apps/blog/requests", "/parts/requests"} {
		_, body := e.get(t, path)
		if m := style.FindString(body); m != "" {
			t.Errorf("GET %s has an inline style attribute (%q): the page's policy forbids it", path, m)
		}
		for _, m := range regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(m, "src=") {
				t.Errorf("GET %s has an inline script: %s", path, m)
			}
		}
		if strings.Contains(body, "js/requests.js") {
			t.Errorf("GET %s loads a script of its own: the page needs none", path)
		}
	}
}

// ---- The query round trip -----------------------------------------------------------------------

func TestRequestsQueryRoundTrip(t *testing.T) {
	for _, raw := range []string{"", "project=shop&environment=staging", "service=blog%2Fweb&status=4xx%2C5xx&method=POST&path=%2Fapi&minDuration=500&client=1.1.1.1&range=24h&live=0",
		"before=2026-10-25T00%3A30%3A00Z", "from=2026-10-25T00%3A00%3A00Z&to=2026-10-25T00%3A05%3A00Z"} {
		v, _ := url.ParseQuery(raw)
		q, bad := requestsReqOf(v)
		if bad != "" {
			t.Fatalf("%q: %s", raw, bad)
		}
		again, bad := requestsReqOf(q.values())
		if bad != "" || again != q {
			t.Errorf("%q: %+v then %+v (%s)", raw, q, again, bad)
		}
	}
	if q, _ := requestsReqOf(url.Values{}); q.href() != "/requests" {
		t.Errorf("the default query's href = %q", q.href())
	}
	// Logs' windows round-trip too (the address a request's row leads to).
	v, _ := url.ParseQuery("from=2026-10-25T00%3A00%3A00Z&to=2026-10-25T00%3A05%3A00Z&service=blog%2Fweb")
	l, bad := logsReqOf(v)
	if bad != "" || l.Live || !l.custom() {
		t.Fatalf("logs window = %+v, %q", l, bad)
	}
	if again, bad := logsReqOf(l.values()); bad != "" || again != l {
		t.Errorf("logs window: %+v then %+v (%s)", l, again, bad)
	}
}

func TestClassSetToggled(t *testing.T) {
	none := classSet{}
	if got := none.toggled("2xx").param(); got != "3xx,4xx,5xx" {
		t.Errorf("from none, 2xx off = %q", got)
	}
	only5, _ := parseClasses("5xx")
	if only5.toggled("5xx") != only5 {
		t.Error("turning off the only class changed the set")
	}
	if got := only5.toggled("4xx").param(); got != "4xx,5xx" {
		t.Errorf("5xx, 4xx on = %q", got)
	}
	three, _ := parseClasses("3xx,4xx,5xx")
	if three.toggled("2xx") != none {
		t.Error("the four chips on again is no filter")
	}
	for _, c := range []string{"2xx", "1xx", "other"} {
		if !none.has(c) {
			t.Errorf("no filter hides %s", c)
		}
	}
	if only5.has("2xx") || !only5.has("5xx") {
		t.Error("has is wrong for a filtered set")
	}
	if _, ok := parseClasses("2xx,six"); ok {
		t.Error("parseClasses took a word that isn't a class")
	}
}

func TestRqDurationText(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "<1 ms", 500 * time.Microsecond: "<1 ms", 3600 * time.Microsecond: "3.6 ms", 20 * time.Millisecond: "20 ms",
		999 * time.Millisecond: "999 ms", 1240 * time.Millisecond: "1.24 s", 12 * time.Second: "12 s"} {
		if got := rqDurationText(d); got != want {
			t.Errorf("rqDurationText(%v) = %q, want %q", d, got, want)
		}
	}
}

// ---- The streams --------------------------------------------------------------------------------

func TestRequestsStreamWatches(t *testing.T) {
	e := newConsoleEnv(t)
	var placeSeen atomic.Value
	var open, done atomic.Int32
	e.Server.live.watchRequests = func(project, environment string) func() {
		placeSeen.Store(project + "/" + environment)
		open.Add(1)
		return func() { done.Add(1) }
	}
	ls := openStream(t, e.URL, "/live?watch=requests&project=default&environment=production&live=1", e.Cookie.Value)
	ls.expect("sync")
	if open.Load() != 1 || placeSeen.Load() != "default/production" {
		t.Fatalf("watched %d times, for %v", open.Load(), placeSeen.Load())
	}
	ls.close()
	waitFor(t, "done", func() bool { return done.Load() == 1 })

	// A page that isn't live, or for a place that isn't there, follows nothing.
	for _, path := range []string{"/live?watch=requests", "/live?watch=requests&project=nope&live=1"} {
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
	anon := openStream(t, e.URL, "/live?watch=requests&live=1", "")
	anon.expect("ended")
	if open.Load() != 0 {
		t.Error("a stream with no session followed the place")
	}

	// An app's stream follows its place with ?watch=requests&live=1 and only then.
	e.seed(t, blogApp())
	for path, want := range map[string]int32{"/live/apps/blog?watch=requests&live=1": 1, "/live/apps/blog?watch=requests": 0, "/live/apps/blog?watch=logs&live=1": 0} {
		open.Store(0)
		ls := openStream(t, e.URL, path, e.Cookie.Value)
		ls.expect("sync")
		ls.close()
		if open.Load() != want {
			t.Errorf("%s watched %d times, want %d", path, open.Load(), want)
		}
	}
	if placeSeen.Load() != "default/production" {
		t.Errorf("the app's place = %v", placeSeen.Load())
	}
}

// The service's notice reaches a page: a request to an app of the place is the requests notice, which
// carries nothing of the request.
func TestRequestsStreamForwardsTheNotice(t *testing.T) {
	e := requestsPageEnv(t)
	e.Svc.SetLogWatchGaps(50*time.Millisecond, time.Hour)
	ls := openStream(t, e.URL, "/live?watch=requests&live=1", e.Cookie.Value)
	ls.expect("sync")
	waitFor(t, "Traefik's stream", func() bool { return e.Nomad.OpenLogStreams() == 1 })
	e.Nomad.WriteLog(rqTraefik, "stdout", rqLine(time.Now(), time.Millisecond, "blog-web", "GET", rqSentinelURL, 200, "1.1.1.1")+"\n")
	ls.expect("requests")
	ls.close()
	waitFor(t, "the follower to end", func() bool { return e.Nomad.OpenLogStreams() == 0 })
	if strings.Contains(e.Logs.String(), rqSentinel) {
		t.Error("a request's path reached the server's log")
	}
}

// The sidebar's Requests item and the app tab's key exist, and the route table lists the page.
func TestRequestsRoutesAreInTheTable(t *testing.T) {
	e := requestsPageEnv(t)
	have := map[string]bool{}
	for _, r := range e.Server.routeTable() {
		have[r.method+" "+r.path] = true
	}
	for _, want := range []string{"GET /requests", "GET /parts/requests", "GET /apps/{id}/requests"} {
		if !have[want] {
			t.Errorf("the route table lacks %s", want)
		}
	}
}
