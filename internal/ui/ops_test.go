package ui

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 027, page 1: the Operator Overview at /ops. The page reads the apps, the needs-you
// list, the host and the activity through the service; most of these tests go through the real
// routes on a store, and the panel's numbers are checked on builders given a hand-made host.

const opsSentinel = "s3cret-sentinel-027ui"

// opsEnv is the standard three apps (whoami healthy with two copies, blog reverted, api with one of
// two copies), a staging app in a second project, an action and a command that printed a sentinel.
func opsEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seedStandard(t)
	ctx := context.Background()
	if _, err := e.Svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	staged := whoamiApp()
	staged.ID = "shop--staging--web"
	staged.Services[0].Domains[0].Host = "staging.example.com"
	e.seed(t, staged, seedDeploy{ID: "s1", Trigger: app.TriggerWebhook, Status: store.StatusHealthy, Version: 2})
	e.copies("shop--staging--web", "web", 2, 1, 0)
	// A minute ahead, so that it is the newest entry and the eight of the panel include it.
	if err := e.Store.InsertAction(ctx, store.ActionRecord{ID: "act_ops1", AppID: "whoami", Action: store.ActionRun, Trigger: "console",
		Status: store.ActionSucceeded, CopyID: "c1", Service: "web", Program: "printf", ExitCode: new(0), StartedAt: time.Now().UTC().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestOpsPage(t *testing.T) {
	e := opsEnv(t)
	resp, body := e.get(t, "/ops")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /ops = %d:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)
	for _, want := range []string{
		// The headline is the Developer home's sentence, and the stats line follows it.
		"apps need you", "1 server", "4 apps", "waiting for the first reading",
		"Server", "Apps", "4 running", "Needs you", "Live activity", "from caravana's records · times in UTC",
		"App State Copies CPU 1h Mem Reach Last change",
		"Waiting for the first reading of the server.",
		// The activity: Caravana's words.
		"web v2 is live, by the webhook.", "printf ran in whoami from the console, exit 0.",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	for _, want := range []string{
		`sse-connect="/live?watch=ops"`, `data-live`,
		`id="ops-server"`, `hx-get="/parts/ops/server"`, `id="ops-apps"`, `hx-get="/parts/ops/apps"`,
		`hx-get="/parts/ops/activity"`, `hx-get="/parts/attention"`,
		`id="ops-head"`, `id="ops-needs-meta"`, `id="ops-activity"`, `data-ev="`, `data-ops-time`,
		`<script src="/static/js/ops.js?v=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// The server panel and the apps table are separate live regions, each swapped by its own part: no
	// region is the other's child, and the old one part for both is gone.
	if strings.Contains(body, "/parts/ops/overview") {
		t.Error("the page still has the one overview part")
	}
	server := regionOf(t, body, `id="ops-server"`)
	apps := regionOf(t, body, `id="ops-apps"`)
	if strings.Contains(server, "cv-opstbl") || strings.Contains(apps, "cv-node-chips") || strings.Contains(apps, "cv-node-meters") {
		t.Error("a region draws the other's content")
	}
	// No Capacity section: on one host it would repeat the Server panel's meters and room sentence.
	if strings.Contains(body, "Capacity") || strings.Contains(body, "ops-capacity") || strings.Contains(body, "/parts/ops/capacity") {
		t.Error("the page still has a Capacity section")
	}
	// The sections read in the order they stack: Needs you, Server, Apps, Live activity.
	last := -1
	for _, id := range []string{`id="ops-needs-title"`, `id="ops-server-title"`, `id="ops-apps-title"`, `id="ops-activity-title"`} {
		i := strings.Index(body, id)
		if i < 0 || i < last {
			t.Errorf("%s is at %d, after %d: the sections aren't in the order Needs you, Server, Apps, Live activity", id, i, last)
		}
		last = i
	}
	// The activity's names are mono wherever they stand, and a deploy that rolls out has the rolling mark.
	for _, want := range []string{
		`<span class="cv-inline-mono text-fg-1">printf</span> ran in <span class="cv-inline-mono text-fg-1">whoami</span> from the console, exit 0.`,
		`<span class="cv-inline-mono text-fg-1">web</span> <span class="cv-inline-mono text-fg-1">v2</span> is live, by the webhook.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the activity lacks %q", want)
		}
	}
	rolling := body[strings.Index(body, `data-ev="deployments.s1.started"`):]
	rolling = rolling[:strings.Index(rolling, "</div>")]
	if !strings.Contains(rolling, `cv-mark cv-mark-rolling`) || strings.Contains(rolling, "cv-mark-idle") {
		t.Errorf("a deploy that is rolling out doesn't have the rolling mark:\n%s", rolling)
	}
	// The Operator sidebar, Overview marked, and the toggle with Operator current.
	if !strings.Contains(body, `<a href="/ops" class="cv-nav-item" aria-current="page">`) {
		t.Error("the sidebar doesn't mark Overview")
	}
	if !strings.Contains(body, `<a href="/ops" class="cv-view-link" aria-current="page">Operator</a>`) {
		t.Error("the toggle doesn't mark Operator")
	}
	// The page sets the mode cookie.
	found := false
	for _, c := range resp.Cookies() {
		found = found || (c.Name == "caravana_mode" && c.Value == "ops")
	}
	if !found {
		t.Error("/ops didn't set the mode cookie")
	}
	// The policy forbids both, and no text is fg-4.
	if regexp.MustCompile(monNoStyle).MatchString(body) {
		t.Errorf("the page has a style attribute: %s", regexp.MustCompile(monNoStyle).FindString(body))
	}
	if regexp.MustCompile(monNoScript).MatchString(body) {
		t.Error("the page has an inline script")
	}
	if strings.Contains(body, "text-fg-4") {
		t.Error("the page has text in fg-4")
	}
	if strings.Contains(strings.ToLower(words), "ship") && regexp.MustCompile(`(?i)\bship`).MatchString(words) {
		t.Error("the page says ship")
	}
}

// The Apps table lists every place, problems first and then by name, with the place before the
// name outside Default › production; each row links to its app.
func TestOpsAppsTable(t *testing.T) {
	e := opsEnv(t)
	_, body := e.get(t, "/ops")
	order := regexp.MustCompile(`class="cv-opsapp">([^<]+)</a>`).FindAllStringSubmatch(body, -1)
	var names []string
	for _, m := range order {
		names = append(names, m[1])
	}
	// blog's newest deploy was reverted (deploy failed), api is short a copy; whoami and web are healthy.
	if got := strings.Join(names, ","); got != "blog,api,web,whoami" {
		t.Errorf("rows = %s, want blog,api,web,whoami (problems first, then by name)", got)
	}
	for _, want := range []string{
		`<a href="/apps/blog" id="ops-app-blog" class="cv-opsapp">blog</a>`,
		`<a href="/apps/shop--staging--web" id="ops-app-shop--staging--web" class="cv-opsapp">web</a>`,
		`<span class="cv-opswhere">Shop › staging ›</span>`, ">staging.example.com<", ">whoami.example.com<",
		"deploy failed", "healthy", "v7 · ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the table lacks %q", want)
		}
	}
	// One mark per copy: whoami has two, api two (one healthy, one failed), staging one.
	row := func(name string) string {
		for _, r := range strings.Split(body, `<div class="cv-opsrow"`)[1:] {
			if strings.Contains(r, `class="cv-opsapp">`+name+`</a>`) {
				return r
			}
		}
		return ""
	}
	if n := strings.Count(row("whoami"), `cv-opsmark`); n != 2 {
		t.Errorf("whoami has %d copy marks, want 2", n)
	}
	if n := strings.Count(row("api"), `cv-opsmark cv-mark-failed`); n != 1 {
		t.Errorf("api has %d failed marks, want 1", n)
	}
	// No samples were written, so no sparkline and no memory: a dash, never a zero.
	if strings.Contains(body, "cv-opsspark-line") {
		t.Error("a sparkline without samples")
	}
	// The fold: the name and the state, then one wrapper of five facts. Each fact is a cell with a label
	// (drawn from data-label when folded) and exactly one value element, so the label sits over its value.
	cells := regexp.MustCompile(`<div role="cell" class="cv-opscell [^"]*" data-label="([^"]+)">`).FindAllStringSubmatch(row("whoami"), -1)
	var labels []string
	for _, c := range cells {
		labels = append(labels, c[1])
	}
	if got := strings.Join(labels, ","); got != "Copies,CPU 1h,Mem,Reach,Last change" {
		t.Errorf("whoami's facts = %s", got)
	}
	if n := strings.Count(row("whoami"), `class="cv-opsfacts"`); n != 1 {
		t.Errorf("a row has %d fact wrappers, want 1", n)
	}
	for _, cell := range strings.Split(row("whoami"), `<div role="cell" class="cv-opscell `)[1:] {
		cell = cell[:strings.Index(cell, "</div>")]
		if n := strings.Count(cell, `class="cv-opsval"`); n != 1 {
			t.Errorf("a folded cell has %d value elements, want 1:\n%s", n, cell)
		}
	}
}

// regionOf is the inside of the section that has marker, up to its end.
func regionOf(t *testing.T, body, marker string) string {
	t.Helper()
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no element with %s", marker)
	}
	rest := body[i:]
	return rest[:strings.Index(rest, "</section>")]
}

// With samples, an app's CPU line and memory are the sum of its services'. Unusual is no longer
// the table's to say (025's issues are), and an app whose services are all stopped has no memory.
func TestOpsUseFoldsServices(t *testing.T) {
	cpu := func(vals ...float64) []*float64 {
		out := make([]*float64, len(vals))
		for i, v := range vals {
			if v >= 0 {
				out[i] = fl(v)
			}
		}
		return out
	}
	svs := []service.ServiceMetrics{
		{AppID: "blog", Service: "web", CPU: cpu(10, -1, 30), CPUReservedMHz: i64(100), MemoryMB: i64(500)},
		{AppID: "blog", Service: "worker", CPU: cpu(5, -1, 5), CPUReservedMHz: i64(50), MemoryMB: i64(900), Unusual: true},
	}
	u := opsUseOf(svs, 2000)
	if u.Mem != "1.4 GB" || u.Stopped || u.Issue != "" {
		t.Errorf("use = %+v, want 1.4 GB, running, and no issue of its own", u)
	}
	if u.Spark == nil || len(u.Spark.Segments) != 2 {
		t.Fatalf("spark = %+v, want two segments around the gap", u.Spark)
	}
	if !strings.Contains(u.Spark.Title, "0.02 vCPU now") {
		t.Errorf("spark title = %q", u.Spark.Title)
	}
	if got := opsUseOf([]service.ServiceMetrics{{MemoryMB: nil}}, 0); got.Mem != "" || got.Spark != nil {
		t.Errorf("no numbers = %+v", got)
	}
	stopped := opsUseOf([]service.ServiceMetrics{{State: service.ServiceStopped, CPU: cpu(10, 20), MemoryMB: i64(500)}}, 0)
	if !stopped.Stopped || stopped.Mem != "" {
		t.Errorf("a stopped service = %+v, want stopped and no memory", stopped)
	}
	if got := issueLabel("web is using 98% of its 1 GB memory limit. It started at 13:58 UTC. ", 0); got != "web is using 98% of its 1 GB memory limit. It started at 13:58 UTC, open monitoring" {
		t.Errorf("issueLabel = %q", got)
	}
	if got := issueLabel("web restarted 3 times in the last hour.", 2); got != "web restarted 3 times in the last hour (and 2 more issues), open monitoring" {
		t.Errorf("issueLabel = %q", got)
	}
}

// opsSampledEnv is opsEnv with an hour and a half of samples written the way the sampler writes them:
// whoami uses 98 % of its memory limit (025's issue), blog's web 10 % (none), and api is stopped.
func opsSampledEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := opsEnv(t)
	ctx := context.Background()
	if err := e.Store.SetAppStopped(ctx, "api", new(time.Now().UTC())); err != nil {
		t.Fatalf("SetAppStopped: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	var rows []store.MetricsSample
	for _, w := range []struct {
		app, service string
		mem          int64
	}{{"whoami", "web", 1000}, {"blog", "web", 100}, {"api", "web", 100}} {
		series, err := e.Store.EnsureSeries(ctx, w.app, w.service)
		if err != nil {
			t.Fatal(err)
		}
		for i := 90; i >= 1; i-- {
			rows = append(rows, store.MetricsSample{Series: series, TS: now.Add(-time.Duration(i) * time.Minute), Minutes: 1,
				MemoryMB: i64(w.mem), MemoryLimitMB: i64(1024), CPUMHz: i64(int64(40 + i%7)), CPUReservedMHz: i64(100), Copies: i64(1)})
		}
	}
	if err := e.Store.WriteMetricsTick(ctx, store.MetricsTick{At: now.Add(-3 * time.Hour), Minute: store.MetricsRows{Samples: rows}}); err != nil {
		t.Fatal(err)
	}
	return e
}

// The CPU cell says "unusual" by more than the colour: only an app 025's rules call unusual has the warn
// diamond, the warn line and the link to its row on /monitoring named by 025's sentence; a healthy app has
// the plain fg-2 line and no link; a stopped app has "—", the title that says it ran, and no memory.
func TestOpsUnusualIsAMarkAndALink(t *testing.T) {
	e := opsSampledEnv(t)
	_, body := e.get(t, "/ops")
	cpu := func(name string) string {
		for _, r := range strings.Split(body, `<div class="cv-opsrow"`)[1:] {
			if strings.Contains(r, `class="cv-opsapp">`+name+`</a>`) {
				r = r[strings.Index(r, `class="cv-opscell cv-opscpu"`):]
				return r[:strings.Index(r, "</span></div>")]
			}
		}
		t.Fatalf("no row for %s", name)
		return ""
	}
	issue := "whoami is using 98% of its 1 GB memory limit, open monitoring"
	who := cpu("whoami")
	for _, want := range []string{`<a href="/monitoring#mon-row-whoami-web" id="ops-cpu-whoami" class="cv-opsunusual"`,
		`aria-label="` + issue + `"`, `title="` + issue + `"`, `cv-mark cv-mark-warning cv-opsflag`, `cv-opsspark-line cv-opsspark-warn`} {
		if !strings.Contains(who, want) {
			t.Errorf("whoami's CPU cell lacks %q:\n%s", want, who)
		}
	}
	// The mark and the line are in the one link.
	if a := who[strings.Index(who, "<a "):strings.Index(who, "</a>")]; !strings.Contains(a, "cv-opsflag") || !strings.Contains(a, "<svg") {
		t.Errorf("the mark and the line aren't one link:\n%s", who)
	}
	if blog := cpu("blog"); strings.Contains(blog, "<a ") || strings.Contains(blog, "cv-opsflag") || strings.Contains(blog, "cv-opsspark-warn") || !strings.Contains(blog, `cv-opsspark-line"`) {
		t.Errorf("a healthy app's CPU cell has a mark, a link or an amber line:\n%s", blog)
	}
	stopped := cpu("api")
	if strings.Contains(stopped, "<a ") || strings.Contains(stopped, "cv-opsflag") || !strings.Contains(stopped, "CPU while the app ran; it is stopped now") || !strings.Contains(stopped, ">—<") {
		t.Errorf("a stopped app's CPU cell:\n%s", stopped)
	}
	for _, r := range strings.Split(body, `<div class="cv-opsrow"`)[1:] {
		if strings.Contains(r, `class="cv-opsapp">api</a>`) {
			mem := r[strings.Index(r, `cv-opsmem`):]
			mem = mem[:strings.Index(mem, "</div>")]
			if !strings.Contains(mem, ">—<") || strings.Contains(mem, "MB") || strings.Contains(mem, "GB") {
				t.Errorf("a stopped app's memory = %s, want a dash", mem)
			}
			if strings.Contains(r, "vCPU now") || strings.Contains(r, "MHz now") {
				t.Errorf("a stopped app's row has a \"now\" value:\n%s", r)
			}
		}
	}
}

func TestOpsParts(t *testing.T) {
	e := opsEnv(t)
	// The server panel and the apps table are two regions with a part each; each part draws its own
	// region and the headline, and the apps part the Needs-you count too.
	resp, body := e.get(t, "/parts/ops/server")
	if resp.StatusCode != 200 {
		t.Fatalf("part server = %d", resp.StatusCode)
	}
	for _, want := range []string{`id="ops-server-title"`, `class="cv-panel cv-node"`, `hx-swap-oob="innerHTML:#ops-head"`, `<h1 class="cv-headline">`} {
		if !strings.Contains(body, want) {
			t.Errorf("server part lacks %q:\n%s", want, text(body))
		}
	}
	if strings.Contains(body, "cv-opstbl") || strings.Contains(body, "ops-needs-meta") || strings.Contains(body, "<html") {
		t.Error("the server part draws the apps, the count or a whole page")
	}
	resp, body = e.get(t, "/parts/ops/apps")
	if resp.StatusCode != 200 {
		t.Fatalf("part apps = %d", resp.StatusCode)
	}
	for _, want := range []string{`id="ops-apps-title"`, `class="cv-panel cv-opstbl"`, `id="ops-app-whoami"`, `hx-swap-oob="innerHTML:#ops-head"`,
		`hx-swap-oob="innerHTML:#ops-needs-meta"`} {
		if !strings.Contains(body, want) {
			t.Errorf("apps part lacks %q:\n%s", want, text(body))
		}
	}
	if strings.Contains(body, "cv-node") || strings.Contains(body, "<html") || strings.Contains(body, `class="cv-shell"`) {
		t.Error("the apps part draws the server or a whole page")
	}
	resp, body = e.get(t, "/parts/ops/activity")
	if resp.StatusCode != 200 || !strings.Contains(body, `data-ev="deployments.s1.ended"`) || strings.Contains(body, "<html") {
		t.Errorf("activity part = %d:\n%s", resp.StatusCode, text(body))
	}
	for _, gone := range []string{"/parts/ops/capacity", "/parts/ops/overview"} {
		if resp, _ = e.get(t, gone); resp.StatusCode != 404 {
			t.Errorf("%s answers %d, want it gone", gone, resp.StatusCode)
		}
	}
	for _, path := range []string{"/parts/ops/server", "/parts/ops/apps", "/parts/ops/activity"} {
		resp, _ := e.do(t, request{path: path})
		if resp.StatusCode != 401 || resp.Header.Get("HX-Redirect") == "" {
			t.Errorf("%s without a session = %d, want 401 with HX-Redirect", path, resp.StatusCode)
		}
	}
	resp, _ = e.do(t, request{path: "/ops"})
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("/ops without a session = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// The ids that carry focus across a swap are the same in the page and in its parts: the table's rows by
// app, the chips by copy, the unusual link by app.
func TestOpsIdsAreStableAcrossSwaps(t *testing.T) {
	e := opsSampledEnv(t)
	ids := func(body string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`id="(ops-(?:app|copy|cpu)-[^"]+)"`).FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
		return out
	}
	_, page := e.get(t, "/ops")
	_, apps := e.get(t, "/parts/ops/apps")
	if a, b := strings.Join(ids(regionOf(t, page, `id="ops-apps"`)), ","), strings.Join(ids(apps), ","); a != b || !strings.Contains(a, "ops-app-whoami") || !strings.Contains(a, "ops-cpu-whoami") {
		t.Errorf("the table's ids differ between the page and its part:\n%s\n%s", a, b)
	}
	// The fake host has no node, so there are no chips here; the panel's chips are checked on the builder.
	var buf strings.Builder
	n := e.Server.opsNode(opsHost(), []opsAppRow{{appRow: appRow{ID: "whoami", Name: "whoami"}}}, map[string][]service.Copy{"whoami": {{ID: "aaaaaaaa-1", Status: deploy.CopyHealthy}}}, false)
	if err := e.Server.pages["ops"].ExecuteTemplate(&buf, "ops-node", n); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `<a href="/apps/whoami" id="ops-copy-aaaaaaaa-1" class="cv-copychip`) {
		t.Errorf("the chip has no stable id:\n%s", buf.String())
	}
}

// Nomad unreachable: the notice, the panel replaced by one sentence, the activity's line; the
// table and Needs you still draw from Caravana's records.
func TestOpsNomadUnreachable(t *testing.T) {
	e := opsEnv(t)
	e.Nomad.Down()
	resp, body := e.get(t, "/ops")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /ops with Nomad down = %d", resp.StatusCode)
	}
	words := text(body)
	for _, want := range []string{
		"Nomad can't be reached right now, so the server's state isn't shown.", "Can't read the server's own events right now.",
		"whoami", "blog", "Live activity", "Nomad can't be reached; showing what caravana last recorded.",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(body, "cv-node-chips") || strings.Contains(body, "cv-opsmark") {
		t.Error("copies are drawn though Nomad can't be read")
	}
}

func TestOpsNoApps(t *testing.T) {
	e := newConsoleEnv(t)
	_, body := e.get(t, "/ops")
	words := text(body)
	for _, want := range []string{"No apps yet.", "New app", "Server", "Nothing needs you.", "Nothing has happened yet."} {
		if !strings.Contains(words, want) {
			t.Errorf("the empty page lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(body, "cv-opstbl") {
		t.Error("a table without apps")
	}
}

// A command's output never reaches the page: the activity names the program and the exit code.
func TestOpsNeverShowsCommandOutput(t *testing.T) {
	e := actionsEnv(t)
	rec, err := e.Svc.RunCommand(context.Background(), "whoami", actCopy1, []string{"echo", opsSentinel}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	_ = rec
	for _, path := range []string{"/ops", "/parts/ops/overview", "/parts/ops/activity"} {
		_, body := e.get(t, path)
		if strings.Contains(body, opsSentinel) || strings.Contains(body, "echo "+opsSentinel) {
			t.Errorf("%s holds the command's text", path)
		}
	}
	_, body := e.get(t, "/parts/ops/activity")
	if !strings.Contains(text(body), "echo ran in whoami") {
		t.Errorf("the run isn't in the activity:\n%s", text(body))
	}
}

// ---- the server panel -----------------------------------------------------------------------

func opsHost() service.HostView {
	at := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	return service.HostView{
		SampledAt: &at,
		Memory:    service.HostResource{Used: i64(1740), Reserved: i64(1344), Total: i64(1949)},
		CPU:       service.HostCPU{HostResource: service.HostResource{Used: i64(1200), Reserved: i64(1780), Total: i64(4000)}, Cores: i64(2)},
		Disks:     []service.HostDisk{{Mount: "/", UsedMB: 5939, TotalMB: 18739, FreeMB: 12800, UsedPercent: 31.7, State: metrics.DiskOK}},
		Node: &service.HostNode{NodeFacts: deploy.NodeFacts{
			Name: "lab", State: "ready", Pool: "default", OS: "ubuntu", OSVersion: "24.04", Kernel: "6.8.0-147-generic",
			Cores: 4, MemoryMB: 7953, TotalMHz: 9780,
		}},
	}
}

func TestOpsNodePanel(t *testing.T) {
	e := opsEnv(t)
	h := opsHost()
	rows := []opsAppRow{{appRow: appRow{ID: "whoami", Name: "whoami"}}, {appRow: appRow{ID: "shop--staging--web", Name: "web"}}}
	copies := map[string][]service.Copy{
		"whoami":             {{ID: "aaaaaaaa-1", Status: deploy.CopyHealthy, Service: "web"}, {ID: "bbbbbbbb-2", Status: deploy.CopyStarting, Service: "web"}},
		"shop--staging--web": {{ID: "cccccccc-3", Status: deploy.CopyFailed, Terminal: true, Service: "web"}, {ID: "dddddddd-4", Status: deploy.CopyHealthy, Stopping: true, Service: "web"}},
	}
	n := e.Server.opsNode(h, rows, copies, false)
	if n.Name != "lab" || n.Pool != "default" || n.Mark != "healthy" || n.State != nil || n.Href != "/ops/servers/lab" {
		t.Errorf("node = %+v", n)
	}
	if n.Copies != "3 copies" {
		t.Errorf("copies = %q, want 3 copies (a failed copy isn't running)", n.Copies)
	}
	if n.Spec != "4 vCPU · 7.8 GB · Ubuntu 24.04 · kernel 6.8.0-147-generic" {
		t.Errorf("spec = %q", n.Spec)
	}
	var reads []string
	for _, m := range n.Meters {
		reads = append(reads, m.Label+" "+m.Read)
	}
	// The split readouts of Monitoring's host meters: what is used, what Nomad reserved, of the total.
	if got, want := strings.Join(reads, "; "), "mem 1.7G used · 1.3G reserved of 1.9G; cpu 0.6 used · 0.89 reserved of 2 vCPU; disk 5.8G used of 18.3G · /"; got != want {
		t.Errorf("meters = %s, want %s", got, want)
	}
	for _, m := range n.Meters {
		if !m.Split {
			t.Errorf("meter %s isn't a split readout", m.Label)
		}
	}
	if len(n.Chips) != 4 || n.More != 0 {
		t.Fatalf("chips = %+v, more %d", n.Chips, n.More)
	}
	if c := n.Chips[0]; c.Name != "whoami" || c.Short != "aaaaaaaa" || c.Mark != "healthy" || c.State != "" || c.Href != "/apps/whoami" {
		t.Errorf("chip 0 = %+v", c)
	}
	if c := n.Chips[1]; c.State != "starting" || c.Mark != "rolling" {
		t.Errorf("chip 1 = %+v", c)
	}
	if c := n.Chips[2]; c.Mark != "failed" || c.Name != "web" {
		t.Errorf("chip 2 = %+v", c)
	}
	if c := n.Chips[3]; c.State != "draining" || c.Mark != "idle" {
		t.Errorf("chip 3 = %+v", c)
	}

	// Drawn: the chips are links to the apps, the failed one has the failed ground, and the
	// panel's name links to the server's page.
	var buf strings.Builder
	if err := e.Server.pages["ops"].ExecuteTemplate(&buf, "ops-node", n); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{`<a href="/ops/servers/lab" class="cv-node-name">lab</a>`, `cv-node-pool" title="Node pool">default<`,
		`cv-node-copies">3 copies<`, `cv-copychip cv-opschip cv-copychip-starting"`, `cv-copychip-failed`, `cv-copychip-draining`,
		`<a href="/apps/whoami" id="ops-copy-aaaaaaaa-1" class="cv-copychip`} {
		if !strings.Contains(html, want) {
			t.Errorf("the panel lacks %q:\n%s", want, html)
		}
	}
}

func TestOpsNodePanelStatesAndLimits(t *testing.T) {
	e := opsEnv(t)
	h := opsHost()
	h.Node.State = "draining"
	rows := []opsAppRow{{appRow: appRow{ID: "big", Name: "big"}}}
	var many []service.Copy
	for range 52 {
		many = append(many, service.Copy{ID: "eeeeeeee-0", Status: deploy.CopyHealthy})
	}
	n := e.Server.opsNode(h, rows, map[string][]service.Copy{"big": many}, false)
	if len(n.Chips) != 40 || n.More != 12 {
		t.Errorf("chips = %d, more = %d; want 40 and 12", len(n.Chips), n.More)
	}
	if n.State == nil || n.State.Word != "draining" || n.Mark != "rolling" {
		t.Errorf("a draining node = %+v %+v", n.State, n.Mark)
	}
	var buf strings.Builder
	if err := e.Server.pages["ops"].ExecuteTemplate(&buf, "ops-node", n); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "and 12 more copies") || !strings.Contains(buf.String(), "cv-pill-rolling") {
		t.Errorf("the panel lacks the overflow or the state pill:\n%s", text(buf.String()))
	}
	for state, mark := range map[string]string{"ready": "healthy", "draining": "rolling", "ineligible": "warning", "down": "failed", "initializing": "idle"} {
		if got := nodeMark(state); got != mark {
			t.Errorf("nodeMark(%s) = %s, want %s", state, got, mark)
		}
	}
	// Nomad can't be read: one sentence; nothing read yet: another; both with the idle mark.
	if n := e.Server.opsNode(h, nil, nil, true); n.Message != "Nomad can't be reached right now, so the server's state isn't shown." || n.Mark != "idle" {
		t.Errorf("down = %+v", n)
	}
	h.Node = nil
	if n := e.Server.opsNode(h, nil, nil, false); n.Message != "Waiting for the first reading of the server." {
		t.Errorf("waiting = %+v", n)
	}
	// Only what Nomad reported is in the spec line.
	if got := nodeSpec(&service.HostNode{NodeFacts: deploy.NodeFacts{Cores: 2, Kernel: "7.0"}}); got != "2 vCPU · kernel 7.0" {
		t.Errorf("sparse spec = %q", got)
	}
}

func TestOpsStatsLine(t *testing.T) {
	h := opsHost()
	copies := map[string][]service.Copy{"a": {{Status: deploy.CopyHealthy}, {Status: deploy.CopyStopped, Terminal: true}}, "b": {{Status: deploy.CopyStarting}}}
	// The use is the newest sample's, the one the meters show: 1200 of 4000 MHz and 1740 of 1949 MB.
	if got := strings.Join(opsStats(h, 13, copies, nil, 3), " · "); got != "1 server · 13 apps · 2 copies · cpu 30% · mem 89% · 3 open items" {
		t.Errorf("stats = %q", got)
	}
	if got := strings.Join(opsStats(h, 1, copies, nil, 1), " · "); !strings.HasSuffix(got, "1 open item") {
		t.Errorf("stats = %q", got)
	}
	h.SampledAt = nil
	if got := strings.Join(opsStats(h, 13, copies, nil, 3), " · "); got != "1 server · 13 apps · 2 copies · waiting for the first reading" {
		t.Errorf("stats without a reading = %q", got)
	}
	if got := strings.Join(opsStats(h, 13, nil, deploy.ErrNomadUnavailable, 0), " · "); strings.Contains(got, "copies") {
		t.Errorf("stats with Nomad down say copies: %q", got)
	}
}

func TestOpsEventRows(t *testing.T) {
	at := time.Date(2026, 10, 30, 14, 2, 14, 0, time.UTC)
	ev := opsEventOf(service.ActivityEntry{ID: "deployments.d1.ended", Time: at, Kind: "deploy", Status: "healthy",
		Parts:    []service.TextPart{{Text: "whoami", Mono: true}, {Text: " "}, {Text: "v24", Mono: true}, {Text: " is live, from the console."}},
		Sentence: "whoami v24 is live, from the console.", AppID: "whoami", AppName: "whoami"})
	if ev.Time != "14:02:14" || ev.Title != "2026-10-30 14:02:14 UTC" || ev.Kind != "deploy" || ev.Mark != "healthy" || ev.Href != "/apps/whoami" {
		t.Errorf("event = %+v", ev)
	}
	if len(ev.Sentence) != 4 || !ev.Sentence[0].Mono || !ev.Sentence[2].Mono || ev.Sentence[3].Mono || ev.Sentence.String() != "whoami v24 is live, from the console." {
		t.Errorf("the names aren't in mono: %+v", ev.Sentence)
	}
	// A name that doesn't begin the sentence is mono too: the service says which parts are names.
	mid := opsEventOf(service.ActivityEntry{ID: "schedule_runs.r.", Time: at, Kind: "schedule", Status: "warning", AppID: "clock", AppName: "clock",
		Parts: []service.TextPart{{Text: "Schedule "}, {Text: "slow", Mono: true}, {Text: " was stopped at its time limit in "}, {Text: "clock", Mono: true}, {Text: "."}}})
	if !mid.Sentence[1].Mono || mid.Sentence[0].Mono || !mid.Sentence[3].Mono {
		t.Errorf("schedule sentence = %+v", mid.Sentence)
	}
	// The place is the full label, its parts muted and the name mono, drawn by the shared sentence template.
	placed := opsEventOf(service.ActivityEntry{ID: "deployments.d2.ended", Time: at, Kind: "deploy", Status: "healthy", AppID: "shop--staging--web", AppName: "web",
		Parts: []service.TextPart{{Text: "Shop › ", Muted: true}, {Text: "staging", Mono: true, Muted: true}, {Text: " › ", Muted: true}, {Text: "web", Mono: true}, {Text: " is live."}}})
	if !placed.Sentence[0].Muted || !placed.Sentence[1].Muted || !placed.Sentence[1].Mono || placed.Sentence[3].Muted || !placed.Sentence[3].Mono || placed.Sentence[4].Muted {
		t.Errorf("a placed entry = %+v", placed.Sentence)
	}
	srv := opsEventOf(service.ActivityEntry{ID: "node.1.cluster", Time: at, Kind: "server", Status: "idle",
		Parts: []service.TextPart{{Text: "The server started."}}, Sentence: "The server started."})
	if srv.Href != "" || len(srv.Sentence) != 1 || srv.Sentence[0].Mono {
		t.Errorf("a server entry = %+v", srv)
	}
}

// opsHost's meters are Monitoring's own (the split readouts, one function): a memory meter at 85 %
// is warn, so the number and the colour agree with the "This host" panel's.
func TestOpsMetersAreMonitoringOnes(t *testing.T) {
	h := opsHost()
	h.Memory.Used = i64(1700) // 87 % of 1949
	short := opsMeters(h)
	long := hostMeters(h)
	if len(short) != len(long) {
		t.Fatalf("meters = %d, Monitoring has %d", len(short), len(long))
	}
	for i := range short {
		// The disk's readout goes under its bar here ("5.8G used of 18.3G · /"); the rest is Monitoring's.
		if short[i].Label == "disk" {
			short[i].Read, short[i].Split = long[i].Read, long[i].Split
		}
		if short[i] != long[i] {
			t.Errorf("meter %s differs: %+v vs %+v", short[i].Label, short[i], long[i])
		}
	}
	if !short[0].Warn || short[0].Mark != "warning" {
		t.Errorf("memory at 87%% = %+v, want warn", short[0])
	}
	h.SampledAt = nil
	if got := opsMeters(h); got != nil {
		t.Errorf("meters of a host never read = %v", got)
	}
}

// The Overview's stream forwards the notices its regions draw, the host's among them, and none of
// the others (a logs notice, a domains notice).
func TestOpsLiveForwardsItsNotices(t *testing.T) {
	e := opsEnv(t)
	feed := make(chan service.Change, 32)
	e.Server.live.subscribe = func(string) (<-chan service.Change, func()) { return feed, func() {} }
	e.Server.live.coalesce = time.Millisecond
	ls := openStream(t, e.URL, "/live?watch=ops", e.Cookie.Value)
	ls.expect("sync")
	for _, k := range []service.ChangeKind{service.ChangeDomains, service.ChangeHost, service.ChangeActions,
		service.ChangeSchedules, service.ChangeMetrics, service.ChangeAttention, service.ChangeActivity, service.ChangeApps} {
		feed <- service.Change{What: k}
		time.Sleep(5 * time.Millisecond) // past the coalesce gap, so each is its own event
	}
	var got []string
	for range 7 {
		got = append(got, ls.next())
	}
	if want := "host actions schedules metrics attention activity apps"; strings.Join(got, " ") != want {
		t.Errorf("events = %v, want %s", got, want)
	}
}

var _ = api.AllocationListStub{} // the fake Nomad's allocation type, used by the helpers above

// The table's widths and its fold are in the stylesheet's source, so a change that moves one fails here:
// State has 136px and Last change 160px, neither wraps, and the table folds under 920px of its own width
// into label-over-value cells that fill the width with cells of 9rem; the activity rows change under 420px
// of their panel; the Needs-you action cell has a minimum width.
func TestOpsTableWidthsAndFold(t *testing.T) {
	raw, err := os.ReadFile("style/input.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	rule := func(selector string) string {
		for _, line := range strings.Split(css, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), selector+" {") {
				return line
			}
		}
		t.Fatalf("no rule for %s", selector)
		return ""
	}
	if r := rule(".cv-opstbl"); !strings.Contains(r, "--cols: minmax(140px, 1.3fr) 136px 80px 92px 64px minmax(140px, 1fr) 160px") {
		t.Errorf("the table's columns: %s", r)
	}
	if r := rule(".cv-opsstate"); !strings.Contains(r, "nowrap") {
		t.Errorf("State wraps: %s", r)
	}
	if r := rule(".cv-opschange .cv-opsval"); !strings.Contains(r, "nowrap") {
		t.Errorf("Last change wraps: %s", r)
	}
	if r := rule(".cv-opsfacts"); !strings.Contains(r, "display: contents") {
		t.Errorf("the facts have a box when the table is wide: %s", r)
	}
	fold := css[strings.Index(css, "@container cvopstbl (max-width: 919.98px)"):]
	fold = fold[:strings.Index(fold, "\n}\n")]
	for _, want := range []string{"repeat(auto-fill, minmax(9rem, 1fr))", "grid-column: 1 / -1", "content: attr(data-label)"} {
		if !strings.Contains(fold, want) {
			t.Errorf("the fold lacks %q:\n%s", want, fold)
		}
	}
	if strings.Contains(css, "cvopstbl (max-width: 859.98px)") {
		t.Error("the old 860px fold is still there")
	}
	ev := css[strings.Index(css, "@container cvev (max-width: 419.98px)"):]
	ev = ev[:strings.Index(ev, "\n}\n")]
	if !strings.Contains(ev, ".cv-evtext { grid-column: 1 / -1; }") && !strings.Contains(ev, ".cv-ev > .cv-evtext { grid-column: 1 / -1; }") {
		t.Errorf("the activity row doesn't put the sentence on its own line under 420px:\n%s", ev)
	}
	if r := rule(".cv-evpanel"); !strings.Contains(r, "container-name: cvev") {
		t.Errorf("the activity panel isn't the container: %s", r)
	}
	if r := rule(".cv-need-act"); !strings.Contains(r, "width: 8.75rem") || strings.Contains(r, "min-width") || !strings.Contains(r, "justify-content: flex-end") {
		t.Errorf("the needs-you action cell: %s", r)
	}
	e := opsEnv(t)
	_, body := e.get(t, "/ops")
	for _, class := range []string{`class="cv-opsstate"`, `class="cv-opsname"`, `class="cv-need-act"`} {
		if !strings.Contains(body, class) {
			t.Errorf("the page lacks %s", class)
		}
	}
	// The place label of the feed reaches the page muted: "Shop › staging › web", the name in mono.
	if want := `<span class="text-fg-3">Shop › </span><span class="cv-inline-mono text-fg-3">staging</span><span class="text-fg-3"> › </span><span class="cv-inline-mono text-fg-1">web</span>`; !strings.Contains(regionOf(t, body, `id="ops-activity-title"`), want) {
		t.Errorf("the feed doesn't say where web is, in full")
	}
}
