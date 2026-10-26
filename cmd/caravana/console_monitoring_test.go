package main

// The console-monitoring step of the lab test (feature 025, quickstart §6): the
// Monitoring page, its parts, the Apps home's Platform panel and the sidebar,
// through the signed-in console (links and plain GETs, no JavaScript), against
// real Nomad and Traefik. It is in its own file only because
// console_lab_test.go is long; runConsole lists it after console-logs.

import (
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

var (
	// cmIssueRe is a row of the issues panel: its mark, sentence, detail and action.
	cmIssueRe = regexp.MustCompile(`(?s)<div class="cv-mnissue">\s*<span class="cv-mark cv-mark-lg cv-mark-(\w+)"[^>]*></span>\s*` +
		`<div class="cv-mnissue-body">\s*<p class="cv-mnissue-text">(.*?)</p>\s*(?:<p class="cv-mnissue-detail">(.*?)</p>)?\s*</div>\s*` +
		`<div class="cv-mnissue-act"><a href="([^"]*)"[^>]*>(.*?)</a></div>`)
	// cmRowNameRe is the name cell of an apps table row.
	cmRowNameRe = regexp.MustCompile(`<a href="([^"]*)" id="[^"]*" class="cv-mnname-text">([^<]*)</a><span class="cv-mncopies">([^<]*)</span>`)
	// cmCellRe is a number cell of an apps table row, by its label.
	cmCellRe = regexp.MustCompile(`(?s)<div role="cell" class="cv-mnnumber([^"]*)" data-label="([^"]*)"[^>]*>(.*?)</div>`)
	// cmMeterRe is a Meter: its label and readout. The readout may be led by a mark (◆ from 85 % memory
	// in use, ■ from 95 %) and followed by a word for a screen reader ("mostly in use"), neither of which
	// is part of the number; the lab host's memory is often past 85 %.
	cmMeterRe = regexp.MustCompile(`(?s)<span class="cv-mnmeter-label">(\w+)</span>.*?<span class="cv-mnmeter-read[^"]*">` +
		`(?:<span class="cv-mark[^"]*"[^>]*></span>\s*)?(.*?)(?:<span class="sr-only">[^<]*</span>)?</span>\s*</div>`)
	// cmRowMarkRe is the mark cell of an apps table row: its kind and what it says.
	cmRowMarkRe = regexp.MustCompile(`<span class="cv-mark cv-mark-lg cv-mark-(\w+)" role="cell" aria-label="([^"]*)"`)
	// cmRowLogsRe is the logs link of a row, in a cell of its own.
	cmRowLogsRe = regexp.MustCompile(`<div role="cell" class="cv-mnlogscell"><a href="([^"]*)" id="[^"]*" class="cv-mnlogs"`)
	// cmRowCPURe is the figure beside a row's CPU sparkline.
	cmRowCPURe = regexp.MustCompile(`<span class="cv-mncpu-read" title="[^"]*">([^<]*)</span>`)
	// cmRoomRe is the room sentence and its title.
	cmRoomRe = regexp.MustCompile(`(?s)<p class="cv-mnroom( cv-mnroom-warn)?" title="([^"]*)">(.*?)</p>`)
	// cmSegRe is an item of a bar (range, sort): its address, whether it is on, its label.
	cmSegRe = regexp.MustCompile(`<a href="([^"]*)" class="cv-seg-item( cv-on)?"[^>]*>([^<]*)</a>`)
	// cmChartRe is a chart panel's opening tag.
	cmChartRe = regexp.MustCompile(`<div class="cv-panel cv-mnchart" id="mon-chart-(\w+)">`)
)

// cmRow is a row of the apps table as the page draws it.
type cmRow struct {
	Href, Name, Copies string
	Mark, MarkWord     string // the app's status first: failed/"not running", rolling, warning/"unusual", healthy
	LogsHref, CPU      string
	Cells              map[string]string // label -> text
	None               map[string]bool   // label -> drawn as "—" in the none style
}

// cmRowsOf reads the apps table's rows in page order.
func cmRowsOf(page string) []cmRow {
	var rows []cmRow
	parts := strings.Split(page, `<div class="cv-mnrow" role="row"`)
	for _, part := range parts[1:] {
		r := cmRow{Cells: map[string]string{}, None: map[string]bool{}}
		if m := cmRowNameRe.FindStringSubmatch(part); m != nil {
			r.Href, r.Name, r.Copies = html.UnescapeString(m[1]), html.UnescapeString(m[2]), html.UnescapeString(m[3])
		}
		if m := cmRowMarkRe.FindStringSubmatch(part); m != nil {
			r.Mark, r.MarkWord = m[1], html.UnescapeString(m[2])
		}
		if m := cmRowLogsRe.FindStringSubmatch(part); m != nil {
			r.LogsHref = html.UnescapeString(m[1])
		}
		if m := cmRowCPURe.FindStringSubmatch(part); m != nil {
			r.CPU = html.UnescapeString(m[1])
		}
		for _, m := range cmCellRe.FindAllStringSubmatch(part, -1) {
			r.Cells[m[2]] = pageText(m[3])
			r.None[m[2]] = strings.Contains(m[1], "cv-mnnumber-none")
		}
		rows = append(rows, r)
	}
	return rows
}

// cmWhoami is a service of this step: traefik/whoami (its ?wait= and its
// settable /health make a slow and a failing path) with a domain.
func cmWhoami(host, foo string) map[string]any {
	return map[string]any{
		"name": "web", "image": "traefik/whoami:latest",
		"env":       []map[string]string{{"name": "FOO", "value": foo}},
		"ports":     []map[string]any{{"name": "http", "containerPort": 80}},
		"domains":   []map[string]any{{"host": host, "port": "http"}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": labResources,
	}
}

// consoleMonitoring: the Monitoring page against three apps in Default ›
// production, deployed through the API (New app's form takes no env or
// resources): mon025slow and mon025calm (traefik/whoami with a domain) and
// mon025quiet (busybox sleep, no domain).
//
//  1. Two earlier days of history for slow and calm, written through the store
//     at the same UTC slots as now (slow: 60 requests a minute, all under
//     10 ms, no 5xx; calm: 2 a minute), and the baseline's start moved two
//     days back, so there is a band and the HTTP rules judge.
//  2. calm deployed again (a deploy marker, and the deploy the issue names).
//  3. The quickstart's load for 2 minutes through 127.0.0.1:8443: slow's
//     /health set to answer 500, then nine ?wait=50ms, one ?wait=400ms and one
//     /health on slow and one call to calm, over and over (it keeps going
//     while the page is read).
//  4. The API's issues for slow: slower and more errors, both with the deploy
//     clause "after mon025calm vN"; nothing about calm or quiet.
//  5. /monitoring: the issues panel's sentences, details and actions; the apps
//     table with slow first (marked by its numbers: unusual) and "—" and "· no domain" for quiet,
//     each row's mark the app's status first, its logs link and its CPU figure in vCPU; four charts
//     with a band polygon, the deploy marker and its label, "usually", and "last 10 min"
//     in each head (no script); the host section's three meters, the room
//     sentence and its title; the footer; the sidebar item; the range and
//     sort bars as links that answer; no style attribute or inline script.
//  6. /parts/monitoring, /parts/monitoring/host and /parts/platform draw their
//     regions alone (the first as out-of-band fragments by id, each row alone while the key of the
//     rows is the page's, the whole table body when it is not); the live stream says "metrics"; the Apps home has the
//     Platform panel linking to /monitoring#host.
//  7. Traefik unreachable (the service's setter pointed at a closed port): its
//     sentence with ○ under the HTTP charts, the history still drawn, and the
//     API's source "unreachable"; then back.
//
// Nomad unreachable isn't done in the lab (its Nomad isn't stopped): the UI's
// tests cover its sentence with the service's fake.
func (c *console) consoleMonitoring(t *testing.T) {
	const (
		slow  = "mon025slow"
		calm  = "mon025calm"
		quiet = "mon025quiet"
		place = "project=default&environment=production"
	)
	c.ensureSignedIn(t)
	if c.g.target.DataDir == "" || c.g.target.SetTraefikMetricsURL == nil {
		t.Skip("needs the lab's data directory and the service's setter")
	}
	ids := []string{slow, calm, quiet}
	for _, id := range ids {
		c.removeLabApp(id) // a leftover from an aborted run, if any
	}
	for _, id := range ids {
		waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	}
	t.Cleanup(func() {
		c.g.target.SetTraefikMetricsURL(labTraefikMetricsURL)
		for _, id := range ids {
			c.removeLabApp(id)
		}
	})
	t.Cleanup(func() { logTail(t, c.g.target) })

	slowHost, calmHost := c.g.extraHost(slow), c.g.extraHost(calm)
	c.deployLabApp(t, slow, labApp(cmWhoami(slowHost, "1")))
	c.deployLabApp(t, calm, labApp(cmWhoami(calmHost, "1")))
	c.deployLabApp(t, quiet, labApp(labWorker("web")))

	// 1. History: two earlier days at the slots around now.
	st := openSharedStore(t, c.g.target.DataDir)
	layout := seedLayout(t, st)
	now := time.Now().UTC()
	shape := func(perMinute int64, buckets func(int64) string) seedShape {
		return func(_ time.Time, mins int) store.MetricsSample {
			reqs, secs := perMinute*int64(mins), int64(mins*60)
			b := buckets(reqs)
			return store.MetricsSample{
				HTTPSecs: &secs, Requests: &reqs, Errors: new(int64(0)), Layout: &layout, Buckets: &b,
				CPUMHz: new(int64(5)), CPUReservedMHz: new(int64(100)), MemoryMB: new(int64(4)),
				MemoryLimitMB: new(int64(32)), MemoryReservedMB: new(int64(16)), Copies: new(int64(1)), Restarts: new(int64(0)),
			}
		}
	}
	for app, sh := range map[string]seedShape{
		slow: shape(60, func(n int64) string { return spread(n, 0, 1) }),            // all in 5-10 ms
		calm: shape(2, func(n int64) string { return spread(n, 0, 0, 0, 0, 0, 1) }), // all in 100-250 ms
	} {
		series, err := st.EnsureSeries(t.Context(), app, "web")
		if err != nil {
			t.Fatal(err)
		}
		for d := 1; d <= 2; d++ {
			day := now.Add(-time.Duration(d) * 24 * time.Hour)
			seedRows(t, st, series, store.SlotRes, day.Add(-7*time.Hour), day.Add(time.Hour), sh)
		}
	}
	setMetricsStart(t, c.g.target.DataDir, now.Add(-49*time.Hour))

	// 2. A deploy before the load. The issue's start is the first 10 s row
	// with the load in it, which may begin up to a tick before the load: 20 s
	// keep the deploy's end before it.
	c.deployLabApp(t, calm, labApp(cmWhoami(calmHost, "2")))
	calmDeploy := c.latestDeployment(t, calm)
	if calmDeploy.JobVersion == nil {
		t.Fatalf("calm's second deployment has no job version: %+v", calmDeploy)
	}
	calmV := "v" + strconv.FormatUint(*calmDeploy.JobVersion, 10)
	time.Sleep(20 * time.Second)

	// 3. The load, until the step ends.
	slowClient, slowBase := c.g.edgeClient(slowHost)
	calmClient, calmBase := c.g.edgeClient(calmHost)
	var sent edgeCount
	if s := sent.call(slowClient, http.MethodPost, slowBase+"/health", "500"); s != http.StatusOK {
		t.Fatalf("POST 500 to https://%s/health = %d, want 200 (whoami sets its /health status)", slowHost, s)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			for range 9 {
				sent.call(slowClient, http.MethodGet, slowBase+"/?wait=50ms", "")
			}
			sent.call(slowClient, http.MethodGet, slowBase+"/?wait=400ms", "")
			sent.call(slowClient, http.MethodGet, slowBase+"/health", "")
			sent.call(calmClient, http.MethodGet, calmBase+"/", "")
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	stopLoad := func() {
		select {
		case <-stop:
		default:
			close(stop)
			<-done
			t.Logf("the load's answers by status: %v", sent.snapshot())
		}
	}
	t.Cleanup(stopLoad)
	loadStart := time.Now()
	events := c.events
	defer func() { c.events = events }() // the app page's stream, which deleteApp reads
	c.openLive(t, "/live?watch=monitoring")
	c.expectEvent(t, "metrics", 30*time.Second)
	time.Sleep(time.Until(loadStart.Add(2 * time.Minute)))

	// 4. The API's issues.
	var m gateMetrics
	var slower, moreErrors *gateMetricIssue
	if !waitUntil(3*time.Minute, func() bool {
		m, _ = c.g.metricsOf(t, "/v1/metrics?"+place)
		slower, moreErrors = m.issue(slow, "slower"), m.issue(slow, "more_errors")
		return slower != nil && moreErrors != nil
	}) {
		t.Fatalf("no slower and more_errors issues for %s within 5 minutes of load: issues %+v, baseline %+v, load %v",
			slow, m.Issues, m.Baseline, sent.snapshot())
	}
	for _, is := range m.Issues {
		if is.AppID == calm || is.AppID == quiet {
			t.Errorf("an issue about %s: %+v; want none (only %s is unusual)", is.AppID, is, slow)
		}
	}
	for _, is := range []*gateMetricIssue{slower, moreErrors} {
		if is.DeployNear == nil || is.DeployNear.AppName != calm || "v"+strconv.FormatUint(is.DeployNear.Version, 10) != calmV || is.DeployNear.Relation != "after" {
			t.Errorf("the %s issue's deploy = %+v, want %s %s, after", is.Kind, is.DeployNear, calm, calmV)
		}
	}
	if m.Baseline.State != "partial" || m.Baseline.FullDays != 2 {
		t.Errorf("baseline = %+v, want partial over 2 full days (the seeded start)", m.Baseline)
	}

	// 5. The page.
	resp, page := c.get(t, "/monitoring")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /monitoring = %d: %.400s", resp.StatusCode, pageText(page))
	}
	text := prose(page)
	if !strings.Contains(text, "Monitoring How your apps are doing compared with their usual week. The shaded band is the same hour over the last 7 days.") {
		t.Errorf("/monitoring has no head and lede: %.300s", text)
	}
	if !strings.Contains(page, `<a href="/monitoring" class="cv-nav-item" aria-current="page">`) {
		t.Errorf("the sidebar's Monitoring item isn't the current one")
	}

	issues := cmIssueRe.FindAllStringSubmatch(page, -1)
	if len(issues) < 2 {
		t.Fatalf("/monitoring has %d issue rows, want at least 2 (slower and more errors): %.600s", len(issues), text)
	}
	clause := `It started (?:less than a minute|\d+ minutes?) after ` + calm + ` ` + calmV + ` rolled out\.`
	slowerRe := regexp.MustCompile(`^` + slow + ` is slower than usual\. The slowest 5% of requests take [\d.]+ m?s, usually [\d.]+ m?s\. ` +
		`It started at \d\d:\d\d UTC\. ` + clause + `$`)
	errorsRe := regexp.MustCompile(`^` + slow + ` returns more errors than usual: [\d.]+% of requests, usually [\d.]+%\. ` +
		`It started at \d\d:\d\d UTC\. ` + clause + `$`)
	var sawSlower, sawErrors bool
	for i, is := range issues {
		mark, sentence, detail, href, action := is[1], prose(is[2]), prose(is[3]), html.UnescapeString(is[4]), prose(is[5])
		if !strings.HasPrefix(sentence, slow+" ") {
			t.Errorf("issue row %d is about something else: %q", i, sentence)
			continue
		}
		if mark != "warning" && mark != "failed" {
			t.Errorf("issue row %d's mark is %q, want warning or failed", i, mark)
		}
		if action != "Show "+slow+" errors" || !strings.HasPrefix(href, "/logs?") || !strings.Contains(href, "app="+slow) ||
			!strings.Contains(href, "service="+slow+"%2Fweb") ||
			!strings.Contains(href, "levels=error") || strings.Contains(href, "levels=error,") || strings.Contains(href, "levels=error%2C") {
			t.Errorf("issue row %d's action is %q to %q, want \"Show %s errors\" to the Logs page with its service and its errors only", i, action, href, slow)
		}
		t.Logf("issue row %d: %s %q, detail %q", i, mark, sentence, detail)
		switch {
		case slowerRe.MatchString(sentence):
			sawSlower = true
			if !regexp.MustCompile(`^[\d.]+% of requests return 5xx · [\d.]+ req/s$`).MatchString(detail) {
				t.Errorf("the slower row's detail is %q, want \"N%% of requests return 5xx · N req/s\"", detail)
			}
		case errorsRe.MatchString(sentence):
			sawErrors = true
			if !regexp.MustCompile(`^\d+ errors? in the last 10 minutes$`).MatchString(detail) {
				t.Errorf("the more errors row's detail is %q, want \"N errors in the last 10 minutes\"", detail)
			}
			if wantMark := map[bool]string{true: "failed", false: "warning"}[moreErrors.Window.ErrorPercent > 10]; mark != wantMark {
				t.Errorf("the more errors row's mark is %q at %.1f%% errors, want %q", mark, moreErrors.Window.ErrorPercent, wantMark)
			}
		default:
			t.Errorf("issue row %d reads %q, want the contract's slower or more errors sentence with the deploy clause", i, sentence)
		}
	}
	if !sawSlower || !sawErrors {
		t.Errorf("the issues panel: slower %v, more errors %v; want both", sawSlower, sawErrors)
	}

	rows := cmRowsOf(page)
	if len(rows) < 3 || rows[0].Name != slow {
		names := []string{}
		for _, r := range rows {
			names = append(names, r.Name)
		}
		t.Errorf("the apps table's order is %v, want %s first (unusual first)", names, slow)
	}
	for _, r := range rows {
		switch r.Name {
		case slow:
			if r.Href != "/apps/"+slow || r.None["Requests"] || !strings.HasSuffix(r.Cells["Requests"], "/s") {
				t.Errorf("%s's row: link %q, requests %q, want its Running tab and a rate", slow, r.Href, r.Cells["Requests"])
			}
			if !strings.Contains(r.Cells["Slowest 5%"], "· usually") {
				t.Errorf("%s's Slowest 5%% reads %q, want the usual value beside it (it is unusual)", slow, r.Cells["Slowest 5%"])
			}
			if !regexp.MustCompile(`^\d+\.\d/s$`).MatchString(r.Cells["Requests"]) {
				t.Errorf("%s's Requests read %q, want a rate with one decimal (\"96.0/s\")", slow, r.Cells["Requests"])
			}
			// The app serves, so its mark is its numbers': ◆ unusual (■ when the errors are past 10 %).
			if (r.Mark != "warning" || r.MarkWord != "unusual") && (r.Mark != "failed" || r.MarkWord != "failing") {
				t.Errorf("%s's mark is %s %q, want warning \"unusual\" or failed \"failing\"", slow, r.Mark, r.MarkWord)
			}
			// The logs link carries the app and the service; the CPU figure is in vCPU beside the line.
			if r.LogsHref != "/logs?app="+slow+"&service="+slow+"%2Fweb" {
				t.Errorf("%s's logs link is %q, want the app's and the service's", slow, r.LogsHref)
			}
			if !regexp.MustCompile(`^[\d.]+(?:/[\d.]+)? vCPU$`).MatchString(r.CPU) {
				t.Errorf("%s's CPU figure reads %q, want \"0.4/0.5 vCPU\"", slow, r.CPU)
			}
		case calm:
			if r.Mark != "healthy" {
				t.Errorf("%s's mark is %s %q, want healthy (its numbers are in their usual range)", calm, r.Mark, r.MarkWord)
			}
		case quiet:
			if r.Mark != "healthy" {
				t.Errorf("%s's mark is %s %q, want healthy", quiet, r.Mark, r.MarkWord)
			}
			for _, label := range []string{"Requests", "Errors", "Slowest 5%"} {
				if r.Cells[label] != "—" || !r.None[label] {
					t.Errorf("%s's %s reads %q, want \"—\" (no domain)", quiet, label, r.Cells[label])
				}
			}
			if !strings.Contains(r.Copies, "· no domain") {
				t.Errorf("%s's copies line is %q, want \"· no domain\"", quiet, r.Copies)
			}
		}
	}

	charts := cmChartRe.FindAllStringSubmatch(page, -1)
	if len(charts) != 4 {
		t.Errorf("/monitoring has %d chart panels, want 4", len(charts))
	}
	if n := strings.Count(page, `<span class="cv-mnnow" data-mon-now>last 10 min</span>`); n != 4 {
		t.Errorf("%d chart heads say \"last 10 min\" without script, want 4", n)
	}
	if !strings.Contains(page, `<polygon class="cv-mnband" points="`) {
		t.Errorf("no chart draws a band polygon (two earlier days were seeded)")
	}
	if !strings.Contains(page, `<span class="cv-mnusually" data-mon-usually>usually `) {
		t.Errorf("no chart head says \"usually\"")
	}
	// The lab's deploys (three apps and calm's second one) end within minutes of each other, so the
	// markers may share one: its title then lists each deploy ("3 deploys rolled out:" and a line "calm v1
	// at 14:02 UTC"), its label says "3 deploys". A lone deploy reads "calm v1 rolled out at 14:02 UTC".
	if !regexp.MustCompile(`<line class="cv-mndeploy" [^>]*><title>[^<]*` + calm + ` ` + calmV + ` (?:rolled out )?at \d\d:\d\d UTC[^<]*</title></line>`).MatchString(page) {
		t.Errorf("no deploy marker for %s %s", calm, calmV)
	}
	if !regexp.MustCompile(`<span class="cv-mndeploy-label">(?:` + calm + ` ` + calmV + `|\d+ deploys)</span>`).MatchString(page) {
		t.Errorf("no deploy label \"%s %s\" or \"N deploys\" over the charts", calm, calmV)
	}
	// Only the deploys of apps with a domain mark the HTTP charts; the Memory chart marks them all.
	if req := regexp.MustCompile(`(?s)id="mon-chart-requests">(.*?)id="mon-chart-errors"`).FindStringSubmatch(page); req != nil && strings.Contains(req[1], quiet+" v") {
		t.Errorf("the Requests chart marks a deploy of %s, which has no domain", quiet)
	}

	hostStart := strings.Index(page, `<section class="cv-mnblock" id="host"`)
	if hostStart < 0 {
		t.Fatalf("/monitoring has no host section")
	}
	hostPart := page[hostStart:]
	if !strings.Contains(prose(hostPart), "This host 1 server") {
		t.Errorf("the host section's head doesn't say \"This host\" and \"1 server\"")
	}
	meters := map[string]string{}
	for _, mm := range cmMeterRe.FindAllStringSubmatch(hostPart, -1) {
		meters[mm[1]] = pageText(mm[2])
	}
	if !regexp.MustCompile(`^[\d.]+ used(?: · [\d.]+ reserved)? of \d+ vCPU$`).MatchString(meters["cpu"]) ||
		!regexp.MustCompile(`^[\d.]+G used(?: · [\d.]+G reserved)? of [\d.]+G$`).MatchString(meters["mem"]) ||
		!regexp.MustCompile(`^[\d.]+/[\d.]+G$`).MatchString(meters["disk"]) {
		t.Errorf("the host's meters read %v, want cpu \"1.2 used · 0.8 reserved of 2 vCPU\", mem \"1.7G used · 1.3G reserved of 1.9G\" and disk \"x/yG\"", meters)
	}
	if room := cmRoomRe.FindStringSubmatch(hostPart); room == nil {
		t.Errorf("the host section has no room sentence")
	} else {
		sentence := prose(room[3])
		t.Logf("the host: meters %v; %q", meters, sentence)
		if html.UnescapeString(room[2]) != "Nomad's count of what fits. Caravana's and the host's own processes aren't reserved." {
			t.Errorf("the room sentence's title is %q", html.UnescapeString(room[2]))
		}
		// vCPU is the page's one CPU unit, and memory in use at 85 % or more is said after the count.
		okRoom := regexp.MustCompile(`^Room for about \d+ more apps like \S+ \(\d+(\.\d)? [MG]B, [\d.]+ vCPU each\)(?:\.|, but memory in use is already at \d+%\.)$`).MatchString(sentence) && room[1] == ""
		noRoom := regexp.MustCompile(`^No room for another app like \S+: (memory|CPU) is fully reserved\.$`).MatchString(sentence) && room[1] != ""
		if !okRoom && !noRoom {
			t.Errorf("the room sentence reads %q (warn %v), want \"Room for about N more apps like …\" or the warn \"No room …\"", sentence, room[1] != "")
		}
	}
	if !strings.Contains(hostPart, "Memory, last 7 days") || !strings.Contains(hostPart, `class="cv-mnline-ref"`) {
		t.Errorf("the host section has no 7-day memory chart with the reserved line")
	}
	if !strings.Contains(text, "Sampled every minute and kept for 8 days. Requests come from Traefik, memory and CPU from Nomad.") {
		t.Errorf("/monitoring has no footer")
	}
	if mm := regexp.MustCompile(`\sstyle\s*=`).FindString(page); mm != "" {
		t.Errorf("/monitoring has a style attribute (%q)", mm)
	}
	for _, tag := range regexp.MustCompile(`<script(?:\s[^>]*)?>`).FindAllString(page, -1) {
		if !strings.Contains(tag, ` src="/static/js/`) {
			t.Errorf("/monitoring has an inline script: %s", tag)
		}
	}

	// The bars are links, and each answers with its item on.
	bars := map[string]string{} // label -> href
	for _, mm := range cmSegRe.FindAllStringSubmatch(page, -1) {
		bars[mm[3]] = html.UnescapeString(mm[1])
	}
	for _, label := range []string{"1 h", "6 h", "24 h", "7 d", "unusual first", "most traffic", "most memory"} {
		href := bars[label]
		if !strings.HasPrefix(href, "/monitoring") {
			t.Errorf("the bar item %q links to %q, want a /monitoring address", label, href)
			continue
		}
		resp, body := c.get(t, href)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s (%s) = %d", href, label, resp.StatusCode)
			continue
		}
		on := false
		for _, mm := range cmSegRe.FindAllStringSubmatch(body, -1) {
			on = on || (mm[3] == label && mm[2] != "")
		}
		if !on {
			t.Errorf("GET %s doesn't draw %q as the current item", href, label)
		}
		if label == "7 d" && !strings.Contains(prose(body), "The band thins to the left: older hours have fewer earlier days kept.") {
			t.Errorf("the 7 d range has no caption about the band thinning")
		}
		if label == "most traffic" {
			if r := cmRowsOf(body); len(r) == 0 || r[0].Name != slow {
				t.Errorf("most traffic doesn't put %s first", slow)
			}
		}
	}
	if resp, _ := c.get(t, "/monitoring?range=2h"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /monitoring?range=2h = %d, want 400", resp.StatusCode)
	}

	// 6. The parts, the stream and the Apps home.
	for path, want := range map[string]string{
		"/parts/monitoring":      `hx-swap-oob="innerHTML:#mon-chart-requests"`,
		"/parts/monitoring/host": `<span class="cv-mnmeter-label">mem</span>`,
		"/parts/platform":        `<a href="/monitoring#host" class="cv-panel cv-mnplat"`,
	} {
		resp, body := c.get(t, path)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, want) || strings.Contains(body, "<html") {
			t.Errorf("GET %s = %d (has %q: %v, a whole page: %v), want the region alone", path, resp.StatusCode, want,
				strings.Contains(body, want), strings.Contains(body, "<html"))
		}
		if path == "/parts/monitoring" && !strings.Contains(body, `<div class="cv-mnissue">`) {
			t.Errorf("GET /parts/monitoring draws no issue")
		}
	}
	// The live region is never swapped as a whole: the page says hx-swap="none" and carries the key of its
	// rows; the part with that key answers each row alone (and not the table body or a bar), a stale key the
	// whole table body.
	if !strings.Contains(page, `id="mon-live" hx-get="/parts/monitoring" hx-trigger="sse:metrics" hx-swap="none"`) {
		t.Errorf("/monitoring's live region isn't swapped by id (hx-swap=\"none\")")
	}
	if key := regexp.MustCompile(`id="mon-known" name="known" value="([0-9a-f]+)"`).FindStringSubmatch(page); key == nil {
		t.Errorf("/monitoring has no key of its rows")
	} else {
		_, same := c.get(t, "/parts/monitoring?known="+key[1])
		if !strings.Contains(same, `hx-swap-oob="innerHTML:#mon-row-`+slow+`-web"`) || strings.Contains(same, `innerHTML:#mon-table-slot`) ||
			strings.Contains(same, `aria-label="Sort"`) || strings.Contains(same, `aria-label="Time range"`) {
			t.Errorf("/parts/monitoring with the page's key isn't each row alone by id: %.400s", pageText(same))
		}
		_, whole := c.get(t, "/parts/monitoring?known=stale")
		if !strings.Contains(whole, `hx-swap-oob="innerHTML:#mon-table-slot"`) {
			t.Errorf("/parts/monitoring with a stale key isn't the whole table body")
		}
	}
	c.expectEvent(t, "metrics", 30*time.Second)
	_, home := c.get(t, "/apps")
	plat := regexp.MustCompile(`(?s)<a href="/monitoring#host" class="cv-panel cv-mnplat"[^>]*>(.*?)</a>`).FindStringSubmatch(home)
	if plat == nil {
		t.Errorf("the Apps home has no Platform panel linking to /monitoring#host")
	} else {
		pt := prose(plat[1])
		if !strings.HasPrefix(pt, "1 server up mem ") || !strings.Contains(plat[1], `cv-mark-healthy`) || !strings.Contains(pt, " disk ") {
			t.Errorf("the Platform panel reads %q, want \"1 server up\" with ●, then the mem and disk meters", pt)
		}
		if !strings.Contains(home, `<h2 class="cv-label" id="platform-title">This host</h2>`) {
			t.Errorf("the Platform panel has no \"This host\" head")
		}
	}
	stopLoad()

	// 7. Traefik unreachable: the setter between two ticks, then the next tick.
	c.expectEvent(t, "metrics", 30*time.Second)
	c.g.target.SetTraefikMetricsURL("http://" + freeAddr(t) + "/metrics")
	var unreachable gateMetrics
	if !waitUntil(time.Minute, func() bool {
		unreachable, _ = c.g.metricsOf(t, "/v1/metrics?"+place)
		return unreachable.Sources.Traefik.State == "unreachable"
	}) {
		t.Errorf("the API's Traefik source = %q a minute after the address went dead, want unreachable", unreachable.Sources.Traefik.State)
	}
	_, page = c.get(t, "/monitoring")
	n := 0
	for _, cap := range regexp.MustCompile(`(?s)<p class="cv-mncap">(.*?)</p>`).FindAllStringSubmatch(page, -1) {
		if strings.HasPrefix(cap[1], `<span class="cv-mark cv-mark-idle" aria-hidden="true"></span>`) &&
			prose(cap[1]) == "Can't read Traefik's numbers right now." {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d charts say \"Can't read Traefik's numbers right now.\" with ○, want the three HTTP charts: %.600s", n, prose(page))
	}
	if !strings.Contains(page, `<polygon class="cv-mnband" points="`) || !strings.Contains(page, `<polyline class="cv-mnline"`) {
		t.Errorf("with Traefik unreachable the stored history isn't drawn")
	}
	c.g.target.SetTraefikMetricsURL(labTraefikMetricsURL)
	waitFor(t, "the Traefik source to be ok again", time.Minute, func() bool {
		back, _ := c.g.metricsOf(t, "/v1/metrics?"+place)
		return back.Sources.Traefik.State == "ok"
	})
}
