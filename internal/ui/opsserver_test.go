package ui

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 027, page 2: the server page at /ops/servers/{name}. The routes and the whole page are
// checked on an environment whose sampler has read the fake node once; the panels' numbers, the
// facts and the copies are also checked on builders given hand-made data.

// srvStub is a copy on the node with the service task's reservation (MHz, MB, MB ceiling).
func srvStub(id, group, status, desired string, cpu, mem, ceiling int) api.AllocationListStub {
	a := api.AllocationListStub{
		ID: id, TaskGroup: group, JobVersion: 7, NodeID: "node-1", NodeName: "lab",
		ClientStatus: status, DesiredStatus: desired,
		AllocatedResources: &api.AllocatedResources{Tasks: map[string]*api.AllocatedTaskResources{group: {
			Cpu:    api.AllocatedCpuResources{CpuShares: int64(cpu)},
			Memory: api.AllocatedMemoryResources{MemoryMB: int64(mem), MemoryMaxMB: int64(ceiling)},
		}}},
	}
	if status == api.AllocClientStatusRunning {
		a.TaskStates = map[string]*api.TaskState{group: {State: "running", StartedAt: time.Now().Add(-76*time.Hour - 30*time.Minute)}}
	}
	return a
}

// srvEnv is the standard apps, with the node read by the sampler: x86 facts, the host's files from the
// hostos fixture (a reboot is pending, Ubuntu's automatic updates ran), a Traefik job and these copies:
// whoami's two (one near its memory limit), blog's worker starting, one being stopped, and a failed copy
// of the staging app.
func srvEnv(t *testing.T) *consoleEnv { return srvEnvStats(t, nil) }

// srvEnvStats is srvEnv with the host's numbers changed by edit (a fuller disk, for instance) before
// the sampler first reads them; nil leaves them as the lab's.
func srvEnvStats(t *testing.T, edit func(*api.HostStats)) *consoleEnv {
	t.Helper()
	e := newConsoleEnvWith(t, func(svc *service.Service, n *deploytest.Nomad) {
		svc.SetMetricsSampleGap(500 * time.Millisecond)
		svc.SetHostRoot(hostos.Dir("../hostos/testdata/host"))
		svc.SetDockerRoot("/var/lib/docker") // the installer's CARAVANA_DOCKER_ROOT
		n.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
		stats := deploytest.LabHostStats()
		stats.Uptime = uint64((8*24*time.Hour + 2*time.Hour) / time.Second)
		if edit != nil {
			edit(&stats)
		}
		n.SetHostStats(stats)
		n.SetNodeFacts(deploytest.X86NodeAttributes(), "default", []*api.NodeEvent{
			{Subsystem: "Cluster", Message: "Node registered", Timestamp: time.Now().Add(-9 * 24 * time.Hour)},
		})
		n.SetNodeNetworks(deploytest.Caravana0Networks("10.99.0.1"))
		n.SetTraefikJob("traefik:v3.7.13")
	})
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
	e.seed(t, staged, seedDeploy{ID: "s1", Trigger: app.TriggerWebhook, Status: store.StatusHealthy, Version: 7})

	run, pending, failed := api.AllocClientStatusRunning, api.AllocClientStatusPending, api.AllocClientStatusFailed
	wantRun, wantStop := api.AllocDesiredStatusRun, api.AllocDesiredStatusStop
	e.Nomad.SetCopies("whoami",
		srvStub("w1aaaaaa-0000", "web", run, wantRun, 100, 64, 512),
		srvStub("w2bbbbbb-0000", "web", run, wantStop, 100, 64, 512))
	e.Nomad.SetCopies("blog",
		srvStub("b1cccccc-0000", "web", run, wantRun, 100, 128, 256),
		srvStub("b2dddddd-0000", "worker", pending, wantRun, 50, 64, 64))
	e.Nomad.SetCopies("api")
	e.Nomad.SetCopies("shop--staging--web", srvStub("s1eeeeee-0000", "web", failed, wantRun, 100, 64, 256))
	// 190 MHz and 470 MB of a 512 MB limit; the others are small.
	e.Nomad.SetStats("w1aaaaaa-0000", deploytest.AllocUsage(map[string]deploytest.TaskUsage{"web": {CPUTicks: 190, UsageBytes: 470 << 20}}))
	e.Nomad.SetStats("w2bbbbbb-0000", deploytest.AllocUsage(map[string]deploytest.TaskUsage{"web": {CPUTicks: 5, UsageBytes: 30 << 20}}))
	e.Nomad.SetStats("b1cccccc-0000", deploytest.AllocUsage(map[string]deploytest.TaskUsage{"web": {CPUTicks: 60, UsageBytes: 100 << 20}}))

	deadline := time.Now().Add(20 * time.Second)
	for {
		h, err := e.Svc.Host(ctx, "")
		if err == nil && h.Node != nil && h.SampledAt != nil && h.Reboot != nil {
			return e
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sampler never read the node: %+v, %v", h, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestOpsServerPage(t *testing.T) {
	e := srvEnv(t)
	resp, body := e.get(t, "/ops/servers/lab")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /ops/servers/lab = %d:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)

	// The head: the back link, the name, the state, the reboot pill that links to Updates, the spec line.
	for _, want := range []string{
		`<a href="/ops/servers" class="cv-srvback">← Servers</a>`, `<h1 class="cv-srvname">lab</h1>`,
		`cv-pill cv-pill-healthy"><span class="cv-mark cv-mark-healthy" aria-hidden="true"></span>ready`,
		`<a href="/ops/updates" class="cv-pill cv-pill-warning cv-srvreboot">`,
		`id="srv-head"`, `id="srv-body"`, `hx-get="/parts/ops/servers/lab"`, `hx-trigger="sse:metrics, sse:host, sse:copies, sse:attention, sse:actions, sse:servers, sse:sync"`,
		`sse-connect="/live?watch=ops"`, `data-live`, `<script src="/static/js/ops.js?v=`,
		`<a href="/ops/servers" class="cv-nav-item" aria-current="page">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	head := body[strings.Index(body, `id="srv-head"`):strings.Index(body, `id="srv-body"`)]
	// The spec line keeps each item whole: a span per item, the separator at the end of the one before.
	if !strings.Contains(text(head), "4 vCPU · 1.9 GB · Ubuntu 24.04 · kernel 6.8.0-88-generic") ||
		!strings.Contains(head, `<span class="cv-srvitem">kernel 6.8.0-88-generic</span>`) ||
		!strings.Contains(head, `<span class="cv-srvitem">Ubuntu 24.04 ·</span> <span class="cv-srvitem">kernel`) {
		t.Errorf("the spec line:\n%s", head)
	}
	for _, action := range []string{"<button", "<form", "Open shell", "Move work off", "Take work again"} {
		if strings.Contains(head, action) {
			t.Errorf("the head has an action (%s):\n%s", action, head)
		}
	}

	// The three numbers, the room line, and the sections.
	for _, want := range []string{
		"CPU", "Memory", "Disk", "of 2 vCPU", "reserved", "of 1.9 GB", "of 18.3 GB on /",
		"Running here · 4 · 1 failed", "Server", "Recent on this server",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	if n := strings.Count(body, `class="cv-srvmeter"`); n != 3 {
		t.Errorf("the numbers have %d meters, want 3", n)
	}

	// Running here: problems first (failed, starting, stopping), then by place and app. The staging app
	// shows its place; blog has two services, so its copies say which; whoami has one, so it doesn't.
	ids := regexp.MustCompile(`id="srv-copy-([^"]+)"`).FindAllStringSubmatch(body, -1)
	var order []string
	for _, m := range ids {
		order = append(order, m[1][:2])
	}
	if got := strings.Join(order, ","); got != "s1,b2,w2,b1,w1" {
		t.Errorf("copies = %s, want s1,b2,w2,b1,w1 (failed, starting, stopping, then by app)", got)
	}
	for _, want := range []string{
		`<a href="/apps/whoami" id="srv-copy-w1aaaaaa-0000" class="cv-srvlink cv-copychip-name"><span class="cv-inline-mono text-fg-1">whoami</span></a>`,
		`<a href="/apps/blog" id="srv-copy-b2dddddd-0000" class="cv-srvlink cv-copychip-name"><span class="cv-inline-mono text-fg-1">blog/worker</span></a>`,
		`<span class="text-fg-3">Shop › </span>`, `href="/apps/shop--staging--web"`,
		`cv-copychip cv-srvchip cv-copychip-starting`, `cv-copychip cv-srvchip cv-copychip-draining`, `cv-copychip-failed`,
		`cv-mark cv-mark-rolling`, `cv-mark cv-mark-failed`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Running here lacks %q", want)
		}
	}
	// whoami's near-limit copy: 470 MB of a 512 MB limit in the warn hue, 190 MHz of the 100 reserved
	// (the host's 4000 MHz over 2 cores: 0.1 vCPU of 0.05), "3 d 4 h" up.
	for _, want := range []string{
		`<span class="cv-srvval">470 <span class="cv-srvunit">MB</span><span class="sr-only"> (near its limit)</span></span><span class="cv-srvsub">of 512 MB limit</span>`,
		`<span class="cv-srvval">0.10 <span class="cv-srvunit">vCPU</span></span><span class="cv-srvsub">of 0.05 reserved</span>`,
		">3 d 4 h<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Running here lacks %q", want)
		}
	}
	// A copy that hasn't started has no value, not 0, and says what it reserves; a failed one draws the
	// same two cells, since the reservation is a fact of the copy in every phase.
	pending := srvRowOf(t, body, "srv-copy-b2dddddd-0000")
	if strings.Contains(pending, "0 MB") || !strings.Contains(pending, `<span class="cv-srvval cv-srvval-none">—</span><span class="cv-srvsub">of 64 MB limit</span>`) {
		t.Errorf("a starting copy's row:\n%s", pending)
	}
	failed := srvRowOf(t, body, "srv-copy-s1eeeeee-0000")
	for _, want := range []string{
		`<span class="cv-srvval cv-srvval-none">—</span><span class="cv-srvsub">of 0.05 reserved</span>`,
		`<span class="cv-srvval cv-srvval-none">—</span><span class="cv-srvsub">of 256 MB limit</span>`,
	} {
		if !strings.Contains(failed, want) {
			t.Errorf("a failed copy's row lacks %q:\n%s", want, failed)
		}
	}
	// The table has a header row for screen readers (Copy, CPU, Memory, Up) and the columns of the ruling.
	header := regionBetween(t, body, `<div class="sr-only" role="row">`, "</div>")
	if n := strings.Count(header, `role="columnheader"`); n != 4 || text(header) != "Copy CPU Memory Up" {
		t.Errorf("the header row = %q (%d columnheaders)", text(header), n)
	}
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "grid-template-columns:minmax(10rem,200px) minmax(0,1fr) minmax(0,1fr) minmax(90px,auto)") {
		t.Error("the stylesheet's rows grid isn't minmax(10rem, 200px) minmax(0, 1fr) minmax(0, 1fr) minmax(90px, auto)")
	}

	// The facts: pool, both addresses, up since with the relative words, the agent line, os, cpu, last seen.
	facts := text(regionBetween(t, body, `class="cv-srvdl"`, "</dl>"))
	for _, want := range []string{
		"pool default", "agent nomad 2.0.1 · docker 29.1.3 · traefik 3.7.13 · caravana 0.7.0",
		"os Ubuntu 24.04 · kernel 6.8.0-88-generic", "cpu AMD EPYC 7763 64-Core Processor · 4 cores · 2,400 MHz",
	} {
		if !strings.Contains(facts, want) {
			t.Errorf("the facts lack %q:\n%s", want, facts)
		}
	}
	// Both addresses are named, the words in fg-3 (cv-srvafter); the private one is caravana0's.
	if !strings.Contains(regionBetween(t, body, `class="cv-srvdl"`, "</dl>"),
		`<dd>caravana.example.com<span class="cv-srvafter"> · public</span><span class="cv-srvsecond">10.99.0.1<span class="cv-srvafter"> · private</span></span></dd>`) {
		t.Errorf("the address lacks the public and the private lines, named:\n%s", facts)
	}
	if !strings.Contains(facts, "address caravana.example.com · public10.99.0.1 · private") {
		t.Errorf("the address reads:\n%s", facts)
	}
	// A fresh reading on a ready node: no "last seen" row (the sampler reads every 500 ms here).
	if strings.Contains(facts, "last seen") || strings.Contains(facts, "last reading") {
		t.Errorf("a ready node read lately has a last seen row:\n%s", facts)
	}
	if !regexp.MustCompile(`<dt>up since</dt> <dd><time class="cv-clock" datetime="[^"]+" title="[^"]+" data-local="date">\d{4}-\d\d-\d\d \d\d:\d\d UTC</time>&#8288;&nbsp;&#8288;<span class="cv-srvafter">\(8 d ago\)</span>`).MatchString(strings.Join(strings.Fields(regionBetween(t, body, `class="cv-srvdl"`, "</dl>")), " ")) {
		t.Errorf("the facts lack up since with its relative words:\n%s", facts)
	}
	// The agent's items are each whole.
	if !strings.Contains(body, `<span class="cv-srvitem">nomad 2.0.1 ·</span> <span class="cv-srvitem">docker 29.1.3 ·</span>`) {
		t.Errorf("the agent's items aren't one span each:\n%s", regionBetween(t, body, `class="cv-srvdl"`, "</dl>"))
	}
	if !strings.Contains(facts, "(8 d ago)") {
		t.Errorf("up since lacks its relative words:\n%s", facts)
	}

	// Recent: the boot, node events and the package runs, newest first, from the three sources.
	recent := text(regionBetween(t, body, `class="cv-panel cv-srvrecent"`, "</section>"))
	for _, want := range []string{"The server started (up since ", "OS packages updated by Ubuntu's automatic updates", "OS packages updated by hand"} {
		if !strings.Contains(recent, want) {
			t.Errorf("Recent lacks %q:\n%s", want, recent)
		}
	}
	if !strings.Contains(text(regionBetween(t, body, `id="srv-recent-title"`, "</div>")), "Recent on this server times in UTC") {
		t.Errorf("Recent's head doesn't say times in UTC:\n%s", regionBetween(t, body, `id="srv-recent-title"`, "</div>"))
	}
	if n := strings.Count(body, `class="cv-srvev"`); n > 8 {
		t.Errorf("Recent lists %d entries, want at most 8", n)
	}
	if !strings.Contains(body, `data-ops-time`) {
		t.Error("Recent's times don't carry data-ops-time (the local time in the title)")
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
	if regexp.MustCompile(`(?i)\bship`).MatchString(words) {
		t.Error("the page says ship")
	}
	// The Operator view's cookie is set.
	found := false
	for _, c := range resp.Cookies() {
		found = found || (c.Name == "caravana_mode" && c.Value == "ops")
	}
	if !found {
		t.Error("the page didn't set the mode cookie")
	}
}

// srvRowOf is the copy row that has the link id.
func srvRowOf(t *testing.T, body, id string) string {
	t.Helper()
	for _, r := range strings.Split(body, `<div class="cv-srvrow"`)[1:] {
		if strings.Contains(r, `id="`+id+`"`) {
			return r[:strings.Index(r, `<div role="cell" class="cv-srvcell cv-srvup"`)]
		}
	}
	t.Fatalf("no row for %s", id)
	return ""
}

// regionBetween is the text of body from the first start to the next end after it.
func regionBetween(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("no %q in the page", start)
	}
	rest := body[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("no %q after %q", end, start)
	}
	return rest[:j]
}

// /ops/servers is the list, with the one server in it; a name that isn't its own is a 404 page that names
// the real one.
func TestOpsServersListAndMissing(t *testing.T) {
	e := srvEnv(t)
	resp, list := e.get(t, "/ops/servers")
	if resp.StatusCode != http.StatusOK || !strings.Contains(list, `<a href="/ops/servers/lab" id="srv-row-`) {
		t.Errorf("/ops/servers = %d, want the list with lab in it:\n%s", resp.StatusCode, text(list))
	}
	resp, body := e.get(t, "/ops/servers/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/ops/servers/nope = %d, want 404", resp.StatusCode)
	}
	for _, want := range []string{
		`No server called <span class="cv-inline-mono text-fg-1">nope</span>. This caravana has one: <span class="cv-inline-mono text-fg-1">lab</span>.`,
		`<a href="/ops/servers/lab" class="cv-btn cv-btn-secondary cv-btn-sm">Open lab</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the 404 page lacks %q:\n%s", want, text(body))
		}
	}
	if strings.Contains(body, "sse-connect") || strings.Contains(body, "srv-body") {
		t.Error("the 404 page draws the server or opens a stream")
	}
	// Signed out, the pages and the part go to sign-in.
	resp, _ = e.do(t, request{path: "/ops/servers/lab"})
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("/ops/servers/lab without a session = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = e.do(t, request{path: "/parts/ops/servers/lab"})
	if resp.StatusCode != 401 || resp.Header.Get("HX-Redirect") == "" {
		t.Errorf("the part without a session = %d, want 401 with HX-Redirect", resp.StatusCode)
	}
}

// Before the sampler's first reading the node's name isn't known: the server's page says so. The list
// has no server to show yet, and says that.
func TestOpsServersBeforeTheFirstReading(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/ops/servers/lab")
	if resp.StatusCode != 200 || !strings.Contains(text(body), "Waiting for the first reading of the server.") {
		t.Errorf("/ops/servers/lab = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.get(t, "/ops/servers")
	if resp.StatusCode != 200 || !strings.Contains(text(body), "No servers yet.") {
		t.Errorf("/ops/servers = %d:\n%s", resp.StatusCode, text(body))
	}
}

// The part is the body and, by id, the head: the same defines the page draws, a 404 for another name.
func TestOpsServerPart(t *testing.T) {
	e := srvEnv(t)
	resp, body := e.get(t, "/parts/ops/servers/lab")
	if resp.StatusCode != 200 {
		t.Fatalf("part = %d", resp.StatusCode)
	}
	for _, want := range []string{`id="srv-running-title"`, `class="cv-panel cv-srvnums"`, `id="srv-copy-w1aaaaaa-0000"`,
		`hx-swap-oob="innerHTML:#srv-head"`, `<h1 class="cv-srvname">lab</h1>`, `cv-srvreboot`} {
		if !strings.Contains(body, want) {
			t.Errorf("the part lacks %q", want)
		}
	}
	if strings.Contains(body, "<html") || strings.Contains(body, `class="cv-shell"`) {
		t.Error("the part draws a whole page")
	}
	if resp, _ = e.get(t, "/parts/ops/servers/nope"); resp.StatusCode != 404 {
		t.Errorf("the part for another name = %d, want 404", resp.StatusCode)
	}
	// The ids that carry focus are the same in the page and in its part.
	ids := func(b string) string {
		var out []string
		for _, m := range regexp.MustCompile(`id="(srv-copy-[^"]+)"`).FindAllStringSubmatch(b, -1) {
			out = append(out, m[1])
		}
		return strings.Join(out, ",")
	}
	_, page := e.get(t, "/ops/servers/lab")
	if a, b := ids(page), ids(body); a == "" || a != b {
		t.Errorf("the copies' ids differ between the page and its part:\n%s\n%s", a, b)
	}
	// Page 1's own server panel and its part are untouched.
	if resp, body = e.get(t, "/parts/ops/server"); resp.StatusCode != 200 || !strings.Contains(body, `class="cv-panel cv-node"`) {
		t.Errorf("page 1's /parts/ops/server = %d", resp.StatusCode)
	}
}

// Nomad unreachable: the notice, the numbers "as of" the last sample, and Running here says it can't read.
func TestOpsServerNomadUnreachable(t *testing.T) {
	e := srvEnv(t)
	e.Nomad.Down()
	resp, body := e.get(t, "/ops/servers/lab")
	if resp.StatusCode != 200 {
		t.Fatalf("GET with Nomad down = %d", resp.StatusCode)
	}
	words := text(body)
	for _, want := range []string{
		"Nomad can't be reached right now. The numbers and facts here are the last ones Caravana read.",
		"Can't read Nomad right now.", "Running here · 0", "as of ",
		"pool default", // the facts are the last known ones
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(body, "cv-srvrow") {
		t.Error("copies are drawn though Nomad can't be read")
	}
}

// ---- builders -----------------------------------------------------------------------------------

func TestServerState(t *testing.T) {
	for state, want := range map[string]statusView{
		"ready": {"healthy", "ready"}, "draining": {"rolling", "draining"}, "ineligible": {"warning", "not taking new work"},
		"down": {"failed", "down"}, "initializing": {"rolling", "starting"},
	} {
		if got := serverState(state); *got != want {
			t.Errorf("serverState(%s) = %+v, want %+v", state, *got, want)
		}
	}
}

func TestServerNumbers(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC)
	h := opsHost()
	h.Memory = service.HostResource{Used: i64(1700), Reserved: i64(1200), Total: i64(1949)} // 87 %
	h.CPU = service.HostCPU{HostResource: service.HostResource{Used: i64(1200), Reserved: i64(800), Total: i64(2000)}, Cores: i64(2)}
	n := serverNumbers(now, h, false, 1000)
	if len(n.Cells) != 3 || n.Line != "" {
		t.Fatalf("numbers = %+v", n)
	}
	cpu, mem, disk := n.Cells[0], n.Cells[1], n.Cells[2]
	if cpu.Value != "1.20" || cpu.Unit != "vCPU" || cpu.Sub.String() != "of 2 vCPU · 0.80 reserved" || cpu.Warn || cpu.Meters[0].Read != "60%" {
		t.Errorf("cpu = %+v / %s", cpu, cpu.Sub)
	}
	if mem.Value != "1.7" || mem.Unit != "GB" || mem.Sub.String() != "of 1.9 GB · 1.2 GB reserved" || !mem.Warn || mem.Meters[0].Read != "87%" || mem.Meters[0].Mark != "warning" {
		t.Errorf("memory = %+v / %s (87 %% is warn)", mem, mem.Sub)
	}
	// The reserved part takes the total's unit: under a GB it is still "0.7 GB", never "736 MB".
	h2 := h
	h2.Memory = service.HostResource{Used: i64(1000), Reserved: i64(736), Total: i64(1949)}
	if got := memoryCell(h2, nil).Sub.String(); got != "of 1.9 GB · 0.7 GB reserved" {
		t.Errorf("memory sub = %q, want the reserved part in GB", got)
	}
	h2.Memory = service.HostResource{Used: i64(300), Reserved: i64(200), Total: i64(900)}
	if got := memoryCell(h2, nil).Sub.String(); got != "of 900 MB · 200 MB reserved" {
		t.Errorf("a host under a GB: memory sub = %q", got)
	}
	if !mem.Meters[0].HasReserved || mem.Meters[0].Label != "" {
		t.Errorf("the meter has no hatch or has a label: %+v", mem.Meters[0])
	}
	if disk.Value != "5.8" || disk.Unit != "GB" || disk.Sub.String() != "of 18.3 GB on /" || disk.Warn || len(disk.Meters) != 1 {
		t.Errorf("disk = %+v / %s", disk, disk.Sub)
	}

	// Two filesystems: the fuller one is big and the other a second meter that reads its mount.
	h.Disks = []service.HostDisk{
		{Mount: "/", UsedMB: 5939, TotalMB: 18739, FreeMB: 12800, UsedPercent: 31.7, State: metrics.DiskOK},
		{Mount: "/var/lib/docker", UsedMB: 41 * 1024, TotalMB: 80 * 1024, FreeMB: 39 * 1024, UsedPercent: 51.25, State: metrics.DiskOK},
	}
	disk = serverNumbers(now, h, false, 1000).Cells[2]
	if disk.Value != "41" || disk.Sub.String() != "of 80 GB on /var/lib/docker" || len(disk.Meters) != 2 || disk.Meters[0].Read != "51%" ||
		disk.Meters[1].Read != "on / 5.8/18.3G" {
		t.Errorf("two disks = %+v / %s", disk, disk.Sub)
	}
	// An open disk item puts its state on the meter and the number.
	h.Disks[1].State = metrics.DiskWarning
	if d := serverNumbers(now, h, false, 1000).Cells[2]; !d.Warn || d.Meters[0].Mark != "warning" {
		t.Errorf("a disk in a state = %+v", d)
	}

	// Nomad can't be read: every sub says when the sample is from.
	for _, c := range serverNumbers(now, h, true, 1000).Cells {
		if !strings.HasSuffix(c.Sub.String(), " · as of 14:00 UTC") {
			t.Errorf("%s's sub = %q, want it to say as of 14:00 UTC", c.Label, c.Sub.String())
		}
	}
	// Without cores the CPU is in MHz.
	if c := serverNumbers(now, h, false, 0).Cells[0]; c.Unit != "MHz" || c.Value != "1200" {
		t.Errorf("cpu without cores = %s %s", c.Value, c.Unit)
	}
	// Never read.
	h.SampledAt = nil
	if n := serverNumbers(now, h, false, 1000); n.Line != "The host hasn't been read yet. Its numbers appear a minute after Caravana starts." || len(n.Cells) != 0 {
		t.Errorf("unread = %+v", n)
	}
}

// A missing value is drawn quiet: "—" in 400 and fg-3, never as the bold value; so is the Up of a copy with none.
func TestMissingValuesAreQuiet(t *testing.T) {
	e := opsEnv(t)
	var buf strings.Builder
	if err := e.Server.pages["opsserver"].ExecuteTemplate(&buf, "srv-metric", srvMetric{Value: "—", None: true, Sub: "of 64 MB limit"}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, `class="cv-srvval cv-srvval-none">—<`) {
		t.Errorf("a missing value:\n%s", got)
	}
	buf.Reset()
	if err := e.Server.pages["opsserver"].ExecuteTemplate(&buf, "srv-metric", srvMetric{Value: "412", Unit: "MB"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "cv-srvval-none") {
		t.Errorf("a value drawn as missing:\n%s", buf.String())
	}
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`\.cv-srvval-none\{([^}]*)\}`).FindSubmatch(css); m == nil ||
		!strings.Contains(string(m[1]), "font-weight:400") || !strings.Contains(string(m[1]), "color:var(--color-fg-3)") {
		t.Error("the stylesheet doesn't make .cv-srvval-none 400 and fg-3")
	}
}

// When the aside stacks under the main column (container under 1200px) and the container is 900px or
// more, Server and Recent sit side by side in two equal columns with the main column's 32px gap; under
// 900 they stack (the rule has a floor of 900, and nothing else changes the aside).
func TestAsideSideBySideRule(t *testing.T) {
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`@container cvsrv \(min-width:\s*900px\) and \(max-width:\s*1199\.98px\)\s*\{\s*\.cv-srvaside\s*\{([^}]*)\}`)
	m := re.FindSubmatch(css)
	if m == nil {
		t.Fatal("the stylesheet has no rule for the aside between 900 and 1199px of container")
	}
	for _, want := range []string{"display:grid", "grid-template-columns:repeat(2,minmax(0,1fr))", "column-gap:32px"} {
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(string(m[1]), ": ", ":")), ""), want) {
			t.Errorf("the aside's rule lacks %s: %s", want, m[1])
		}
	}
}

func TestCopyMetrics(t *testing.T) {
	u := &service.CopyUsage{CPUMHz: 190, MemoryMB: 412, CPUReservedMHz: 50, MemoryReservedMB: 256, MemoryCeilingMB: 512, NearCeiling: true}
	if m := copyCPU(u, 1000); m.Value != "0.19" || m.Unit != "vCPU" || m.Sub != "of 0.05 reserved" {
		t.Errorf("cpu = %+v", m)
	}
	if m := copyCPU(u, 0); m.Value != "190" || m.Unit != "MHz" || m.Sub != "of 50 reserved" {
		t.Errorf("cpu in MHz = %+v", m)
	}
	if m := copyMemory(u); m.Value != "412" || m.Unit != "MB" || m.Sub != "of 512 MB limit" || !m.Warn {
		t.Errorf("memory = %+v", m)
	}
	big := &service.CopyUsage{MemoryMB: 1536, MemoryCeilingMB: 2048}
	if m := copyMemory(big); m.Value != "1.5" || m.Unit != "GB" || m.Sub != "of 2 GB limit" || m.Warn {
		t.Errorf("memory in GB = %+v", m)
	}
	// No measurement is "—" (not 0), an error says why in the title, nothing at all is "—".
	if m := copyMemory(&service.CopyUsage{None: true, MemoryCeilingMB: 256}); m.Value != "—" || m.Sub != "of 256 MB limit" || m.Warn {
		t.Errorf("no value = %+v", m)
	}
	if m := copyCPU(&service.CopyUsage{Error: "Nomad didn't answer for this copy.", CPUReservedMHz: 100}, 1000); m.Value != "—" || m.Title != "Nomad didn't answer for this copy." {
		t.Errorf("an error = %+v", m)
	}
	if m := copyCPU(nil, 1000); m.Value != "—" || m.Sub != "" {
		t.Errorf("nil = %+v", m)
	}
}

func TestServerTimeWords(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 5, 7, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		30 * time.Second: "under 1 min", 12 * time.Minute: "12 min", 5*time.Hour + 12*time.Minute: "5 h 12 min", 5 * time.Hour: "5 h",
		76 * time.Hour: "3 d 4 h", 72 * time.Hour: "3 d",
	} {
		if got := upText(d); got != want {
			t.Errorf("upText(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		0: "just now", 2 * time.Second: "2 s ago", 3 * time.Minute: "3 min ago", 5 * time.Hour: "5 h ago", 8 * 24 * time.Hour: "8 d ago",
	} {
		if got := agoWords(now, now.Add(-d)); got != want {
			t.Errorf("agoWords(%v) = %q, want %q", d, got, want)
		}
	}
	// Sentences use full words; the dense columns the short ones.
	for d, want := range map[time.Duration]string{
		0: "under a minute ago", 30 * time.Second: "under a minute ago", time.Minute: "1 minute ago", 3 * time.Minute: "3 minutes ago",
		5 * time.Hour: "5 hours ago", 8 * 24 * time.Hour: "8 days ago", 90 * 24 * time.Hour: "over 60 days ago",
	} {
		if got := agoFull(now, now.Add(-d)); got != want {
			t.Errorf("agoFull(%v) = %q, want %q", d, got, want)
		}
	}
	if got := shortTime(now, now.Add(-time.Hour)); got != "13:05" {
		t.Errorf("today's time = %q", got)
	}
	if got := shortTime(now, now.Add(-8*24*time.Hour)); got != "Oct 22" {
		t.Errorf("an older time = %q", got)
	}
	for n, want := range map[int]string{5: "5", 999: "999", 4890: "4,890", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q", n, got)
		}
	}
}

func TestServerFactLines(t *testing.T) {
	n := &service.HostNode{NodeFacts: deploy.NodeFacts{
		Name: "lab", OS: "ubuntu", OSVersion: "24.04", Kernel: "6.8.0-147-generic", NomadVersion: "1.9.4", DockerVersion: "27.3",
		TraefikVersion: "v3.7.1", Cores: 2, TotalMHz: 4890,
	}}
	if got := agentLine(n, "0.13.0"); got != "nomad 1.9.4 · docker 27.3 · traefik 3.7.1 · caravana 0.13.0" {
		t.Errorf("agent = %q", got)
	}
	// Only what is known.
	if got := agentLine(&service.HostNode{}, ""); got != "" {
		t.Errorf("an empty agent line = %q", got)
	}
	if got := osLine(n); got != "Ubuntu 24.04 · kernel 6.8.0-147-generic" {
		t.Errorf("os = %q", got)
	}
	// On arm Nomad gives no model or per-core speed: the row says the total.
	if got := cpuLine(n); got != "2 cores · 4,890 MHz in total" {
		t.Errorf("arm cpu = %q", got)
	}
	n.CPUModel, n.CoreMHz = "AMD EPYC-Rome", 2445
	if got := cpuLine(n); got != "AMD EPYC-Rome · 2 cores · 2,445 MHz" {
		t.Errorf("x86 cpu = %q", got)
	}
}

// A node that is down: the state pill, a banner that says when the heartbeat was lost and has no action,
// and last seen as "lost ...". A disk item's banner has the needs-you list's own sentence and action.
func TestServerDownAndBanners(t *testing.T) {
	e := opsEnv(t)
	now := time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC)
	n := &service.HostNode{NodeFacts: deploy.NodeFacts{Name: "lab", State: "down", LastHeartbeat: now.Add(-3 * time.Minute)}}
	banners := e.Server.nodeBanners(now, n)
	if len(banners) != 1 || banners[0].Mark != "failed" || banners[0].Line.String() != "Nomad lost this server's heartbeat 3 minutes ago. Apps on it may be down." || banners[0].Action != "" {
		t.Errorf("banners = %+v", banners)
	}
	if got := e.Server.nodeBanners(now, &service.HostNode{NodeFacts: deploy.NodeFacts{State: "ready"}}); len(got) != 0 {
		t.Errorf("a ready node has banners: %+v", got)
	}
	var buf strings.Builder
	if err := e.Server.pages["opsserver"].ExecuteTemplate(&buf, "srv-banner", banners[0]); err != nil {
		t.Fatal(err)
	}
	if html := buf.String(); strings.Contains(html, "<a ") || !strings.Contains(html, "cv-mark-failed") {
		t.Errorf("the down banner:\n%s", html)
	}

	facts := e.Server.serverFacts(&http.Request{Host: "caravana.example.com"}, now, n, now.Add(-time.Minute), time.Minute)
	last := facts[len(facts)-1]
	if last.Key != "last seen" || last.Value != "lost 3 minutes ago" || last.Mark != "failed" || !last.Strong {
		t.Errorf("last seen of a down node = %+v", last)
	}
	// The time is the one Nomad marked the node down, not the sampler's last reading or the heartbeat.
	n.StatusAt = now.Add(-5 * time.Minute)
	banners = e.Server.nodeBanners(now, n)
	if got := banners[0].Line.String(); got != "Nomad lost this server's heartbeat 5 minutes ago. Apps on it may be down." {
		t.Errorf("the banner with Nomad's status time = %q", got)
	}
	if f, ok := lastSeenFact(now, n, now, time.Minute); !ok || f.Value != "lost 5 minutes ago" {
		t.Errorf("last seen with Nomad's status time = %+v", f)
	}

	if got := diskExplain([]string{"nomad", "docker"}); got.String() != "It holds Nomad's data and Docker's images." {
		t.Errorf("explain = %q", got)
	}
	// Holding only one of the two, or nothing: no clause, the banner names its own mount.
	for _, holds := range [][]string{{"nomad"}, {"docker"}, nil} {
		if got := diskExplain(holds); got != nil {
			t.Errorf("explain of %v = %q, want none", holds, got)
		}
	}
	if got := diskExplain(nil); got != nil {
		t.Errorf("explain of nothing = %q", got)
	}
	for _, kind := range []service.AttentionKind{service.AttentionDiskNearlyFull, "os_reboot_required", "os_security_updates"} {
		if !isHostItem(kind) {
			t.Errorf("%s isn't a host item", kind)
		}
	}
	if isHostItem(service.AttentionDeployFailed) || isHostItem(service.AttentionOutOfMemory) {
		t.Error("an app's item is a host item")
	}
}

// Running here: the place leads outside Default › production, the service follows only for an app with
// several, a failed copy has no Up, and Nomad unreachable replaces the rows with one sentence.
func TestServerCopiesRows(t *testing.T) {
	e := opsEnv(t)
	ctx := context.Background()
	sums, err := e.Svc.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := e.Server.loadPlaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	started := e.Server.now().Add(-26*time.Hour - 30*time.Minute)
	copies := []service.HostCopy{
		{Copy: deploy.Copy{ID: "ffffffff-1", Service: "web", Status: deploy.CopyFailed, Terminal: true}, AppID: "shop--staging--web",
			Place: app.Place{Project: "shop", Environment: "staging", Name: "web"}, Phase: service.CopyPhaseFailed},
		{Copy: deploy.Copy{ID: "aaaaaaaa-2", Service: "worker", Status: deploy.CopyHealthy, StartedAt: &started}, AppID: "blog",
			Place: app.Place{Project: "default", Environment: "production", Name: "blog"}, Phase: service.CopyPhaseRunning},
		{Copy: deploy.Copy{ID: "bbbbbbbb-3", Service: "web", Status: deploy.CopyHealthy, StartedAt: &started}, AppID: "whoami",
			Place: app.Place{Project: "default", Environment: "production", Name: "whoami"}, Phase: service.CopyPhaseRunning},
	}
	r := e.Server.serverCopies(pl, sums, copies, nil, false, 1000, nil)
	if r.Count != 2 || r.Failed != 1 || len(r.Rows) != 3 {
		t.Fatalf("running = %+v", r)
	}
	if got := r.Rows[0].Label.String(); got != "Shop › staging › web" || r.Rows[0].Mark != "failed" || r.Rows[0].Up != (srvUp{Label: "Failed", Word: "failed"}) || r.Rows[0].CPU.Value != "—" ||
		!r.Rows[0].CPU.None || !r.Rows[0].Mem.None {
		t.Errorf("failed row = %q %+v", got, r.Rows[0])
	}
	// A failed copy Nomad told to stop still has the failed mark (copyMark would call it idle), and its
	// Up cell says when it failed.
	endedAt := e.Server.now().Add(-3 * time.Minute)
	stopped := service.HostCopy{Copy: deploy.Copy{ID: "eeeeeeee-9", Service: "web", Status: deploy.CopyFailed, Terminal: true, Stopping: true, FinishedAt: &endedAt},
		AppID: "whoami", Place: app.Place{Project: "default", Environment: "production", Name: "whoami"}, Phase: service.CopyPhaseFailed}
	if row := e.Server.serverCopies(pl, sums, []service.HostCopy{stopped}, nil, false, 1000, nil).Rows[0]; row.Mark != "failed" || row.Up.Text != "3 min ago" || row.Up.Word != "failed" || row.Role != "failed" {
		t.Errorf("a failed copy told to stop = mark %q, up %+v, role %q", row.Mark, row.Up, row.Role)
	}
	if got := r.Rows[1].Label.String(); got != "blog/worker" || r.Rows[1].Up != (srvUp{Label: "Up", Text: "1 d 2 h"}) || r.Rows[1].Mark != "healthy" {
		t.Errorf("blog row = %q %+v", got, r.Rows[1])
	}
	if got := r.Rows[2].Label.String(); got != "whoami" || r.Rows[2].ID != "srv-copy-bbbbbbbb-3" || r.Rows[2].Href != "/apps/whoami" || r.Rows[2].Short != "bbbbbbbb" {
		t.Errorf("whoami row = %q %+v", got, r.Rows[2])
	}
	if got := e.Server.serverCopies(pl, sums, nil, deploy.ErrNomadUnavailable, true, 1000, nil); got.Message != "Can't read Nomad right now." || len(got.Rows) != 0 {
		t.Errorf("Nomad down = %+v", got)
	}
	// None at all: the empty state, with New app.
	var buf strings.Builder
	if err := e.Server.pages["opsserver"].ExecuteTemplate(&buf, "srv-body", srvBody{Running: srvRunning{}, Facts: nil}); err != nil {
		t.Fatal(err)
	}
	if html := buf.String(); !strings.Contains(html, "Nothing runs here yet.") || !strings.Contains(html, `<a href="/apps/new"`) || !strings.Contains(html, "Nothing recent.") {
		t.Errorf("the empty body:\n%s", text(html))
	}
}

// No command output, secret or token is on the page: the Recent list names programs, never output.
func TestOpsServerNeverShowsCommandOutput(t *testing.T) {
	e := srvEnv(t)
	for _, path := range []string{"/ops/servers/lab", "/parts/ops/servers/lab"} {
		_, body := e.get(t, path)
		if strings.Contains(body, opsSentinel) {
			t.Errorf("%s holds a sentinel", path)
		}
	}
}

// Running here puts problems first, and a crash loop is one even between its restarts (designer's
// last look): failed, crash signal, starting, stopping, then running.
func TestRowRank(t *testing.T) {
	for _, tc := range []struct {
		phase    string
		crashing bool
		want     int
	}{
		{service.CopyPhaseFailed, true, 0},
		{service.CopyPhaseFailed, false, 0},
		{service.CopyPhaseRunning, true, 1},
		{service.CopyPhaseStarting, true, 1},
		{service.CopyPhaseStarting, false, 2},
		{service.CopyPhaseStopping, false, 3},
		{service.CopyPhaseRunning, false, 4},
	} {
		if got := rowRank(tc.phase, tc.crashing); got != tc.want {
			t.Errorf("rowRank(%s, crashing %v) = %d, want %d", tc.phase, tc.crashing, got, tc.want)
		}
	}
}

// A capacity is a count when it is whole: "of 2 vCPU", never "of 2.00 vCPU"; measured and reserved
// values keep vcpuText's two decimals (designer's last look).
func TestVCPUTotalText(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		want string
	}{{2, "2"}, {1.999, "2"}, {12, "12"}, {1.5, "1.50"}, {0.5, "0.50"}} {
		if got := vcpuTotalText(tc.v); got != tc.want {
			t.Errorf("vcpuTotalText(%v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}
