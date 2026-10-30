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
		"Server", "Apps", "4 running", "Needs you", "Live activity", "from caravana's records",
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
		`hx-get="/parts/ops/overview"`, `hx-get="/parts/ops/activity"`, `hx-get="/parts/attention"`,
		`id="ops-head"`, `id="ops-needs-meta"`, `id="ops-activity"`, `data-ev="`, `data-ops-time`,
		`<script src="/static/js/ops.js?v=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
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
		`<a href="/apps/blog" class="cv-opsapp">blog</a>`, `<a href="/apps/shop--staging--web" class="cv-opsapp">web</a>`,
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
}

// With samples, an app's CPU line and memory are the sum of its services', and the line is warn
// when the app is unusual.
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
	if u.Mem != "1.4 GB" || !u.Unusual {
		t.Errorf("use = %+v, want 1.4 GB and unusual", u)
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
}

func TestOpsParts(t *testing.T) {
	e := opsEnv(t)
	resp, body := e.get(t, "/parts/ops/overview")
	if resp.StatusCode != 200 {
		t.Fatalf("part overview = %d", resp.StatusCode)
	}
	for _, want := range []string{`class="cv-panel cv-opstbl"`, `hx-swap-oob="innerHTML:#ops-head"`, `<h1 class="cv-headline">`,
		`hx-swap-oob="innerHTML:#ops-needs-meta"`, `class="cv-panel cv-node"`} {
		if !strings.Contains(body, want) {
			t.Errorf("overview part lacks %q:\n%s", want, text(body))
		}
	}
	if strings.Contains(body, "<html") || strings.Contains(body, `class="cv-shell"`) {
		t.Error("overview part draws a whole page")
	}
	resp, body = e.get(t, "/parts/ops/activity")
	if resp.StatusCode != 200 || !strings.Contains(body, `data-ev="deployments.s1.ended"`) || strings.Contains(body, "<html") {
		t.Errorf("activity part = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, _ = e.get(t, "/parts/ops/capacity"); resp.StatusCode != 404 {
		t.Errorf("the Capacity part answers %d, want it gone", resp.StatusCode)
	}
	for _, path := range []string{"/parts/ops/overview", "/parts/ops/activity"} {
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
	if got, want := strings.Join(reads, "; "), "mem 1.7G used · 1.3G reserved of 1.9G; cpu 0.6 used · 0.89 reserved of 2 vCPU; disk 5.8/18.3G"; got != want {
		t.Errorf("meters = %s, want %s", got, want)
	}
	for _, m := range n.Meters[:2] {
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
		`<a href="/apps/whoami" class="cv-copychip`} {
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
	h.Recent = &service.HostRecent{CPUPercent: fl(38.4), MemoryPercent: fl(62.6)}
	if got := strings.Join(opsStats(h, 13, copies, nil, 3), " · "); got != "1 server · 13 apps · 2 copies · cpu 38% · mem 63% · 3 open items" {
		t.Errorf("stats = %q", got)
	}
	if got := strings.Join(opsStats(h, 1, copies, nil, 1), " · "); !strings.HasSuffix(got, "1 open item") {
		t.Errorf("stats = %q", got)
	}
	h.Recent = nil
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

// The apps table's text cells wrap inside their column (the State pill and the Last change cell used to
// run into their neighbours at 1440px): the cells are the grid's items with a minimum width of 0, the
// last change isn't nowrap and the pill may break. The rules are in the stylesheet's source, so a
// change that removes one fails here, and the markup has the classes the rules are on.
func TestOpsTableCellsWrapInTheirColumn(t *testing.T) {
	css, err := os.ReadFile("style/input.css")
	if err != nil {
		t.Fatal(err)
	}
	rule := func(selector string) string {
		for _, line := range strings.Split(string(css), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), selector+" {") {
				return line
			}
		}
		t.Fatalf("no rule for %s", selector)
		return ""
	}
	if r := rule(".cv-opshead > *, .cv-opsrow > *"); !strings.Contains(r, "min-width: 0") {
		t.Errorf("the table's cells have no min-width 0: %s", r)
	}
	if r := rule(".cv-opsstate, .cv-opschange"); !strings.Contains(r, "min-width: 0") || !strings.Contains(r, "overflow-wrap: anywhere") {
		t.Errorf("the state and last change cells don't wrap in their column: %s", r)
	}
	if r := rule(".cv-opschange"); strings.Contains(r, "nowrap") {
		t.Errorf("the last change doesn't wrap: %s", r)
	}
	if r := rule(".cv-opsstate .cv-pill"); !strings.Contains(r, "white-space: normal") {
		t.Errorf("the state pill doesn't wrap: %s", r)
	}
	e := opsEnv(t)
	_, body := e.get(t, "/ops")
	for _, class := range []string{`class="cv-opsstate"`, `class="cv-opschange"`, `class="cv-opsname"`} {
		if !strings.Contains(body, class) {
			t.Errorf("the table lacks a cell with %s", class)
		}
	}
}
