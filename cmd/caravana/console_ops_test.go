package main

// The console-ops-overview step of the lab test (feature 027 page 1, quickstart §2): the
// Developer/Operator toggle, the Operator Overview and its Live activity, through the signed-in
// console (links and plain GETs, no JavaScript) against real Nomad. It is in its own file only
// because console_lab_test.go is long; runConsole lists it after console-monitoring.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	// coSpecRe is the node panel's spec line: "4 vCPU · 7.8 GB · Ubuntu 24.04 · kernel 6.8.0-147-generic".
	coSpecRe = regexp.MustCompile(`^(\d+) vCPU · [\d.]+ GB · [A-Za-z]+ [\d.]+ · kernel (\S+)$`)
	// coStatsRe is the stats line's items, each its own span.
	coStatsRe = regexp.MustCompile(`<span class="cv-opsstat">([^<]*)</span>`)
	// coAppRe is the name cell of an apps table row, with its place prefix when it has one.
	coAppRe = regexp.MustCompile(`(?:<span class="cv-opswhere">([^<]*)</span> )?<a href="([^"]*)" class="cv-opsapp">([^<]*)</a>`)
	// coChipRe is a copy of the server panel: where it links.
	coChipRe = regexp.MustCompile(`<a href="(/apps/[^"]*)" class="cv-copychip cv-opschip`)
)

// opsStatsOf reads the items of the stats line.
func opsStatsOf(page string) []string {
	var out []string
	for _, m := range coStatsRe.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

// opsEntries is GET /v1/activity: Caravana's sentences, newest first.
func (c *console) opsEntries(t *testing.T) []struct{ ID, Kind, Message string } {
	t.Helper()
	var list struct {
		Entries []struct{ ID, Kind, Message string } `json:"entries"`
	}
	c.g.getJSON(t, "/v1/activity?limit=50", &list)
	return list.Entries
}

// opsHasEntry says the API's feed has an entry whose message matches re.
func (c *console) opsHasEntry(t *testing.T, re *regexp.Regexp) bool {
	t.Helper()
	for _, e := range c.opsEntries(t) {
		if re.MatchString(e.Message) {
			return true
		}
	}
	return false
}

// consoleOpsOverview: the Operator view's first page.
//
//  1. The toggle: the Developer pages link to /ops; /ops sets the mode cookie and marks Operator
//     and Overview; Settings read after it carries the Operator sidebar, and after /apps the
//     Developer one again.
//  2. Two apps in two places (con0271 in Default › production, web in the project con027's
//     staging), deployed through the API: the stats line (1 server, the apps, copies, and cpu and
//     mem once the host has been read), the server panel with the lab's spec line (cores and
//     kernel checked against Nomad's own record), a chip for each running copy linking to its
//     app, the apps table with both rows (the second with its place). There is no Capacity
//     section: on one host it would repeat the panel's meters (split readouts: used, reserved, of).
//  3. Live activity: the deploys of both apps, then a command run through the console's form
//     (its output is a sentinel that must not appear in the page, its parts or the API's feed,
//     which name the program and the exit code) and a restart; the panel and listActivity say
//     so in Caravana's words. The Overview's live stream sends an "actions" notice for the restart.
//  4. After Caravana restarts, the deploys, the command and the restart are still in the panel:
//     they are records, not memory.
//  5. The page renders in under a second.
//
// Budget: two deploys and a restart, each waited for up to 4 minutes, normally under one, and
// a Caravana restart.
func (c *console) consoleOpsOverview(t *testing.T) {
	// This step opens its own stream and restarts Caravana, which ends every stream the earlier
	// steps opened. console-delete reads the app page's stream, so open it again on the way out.
	defer func() {
		c.openLive(t, "/live/apps/whoami")
		c.expectEvent(t, "sync", 5*time.Second)
	}()
	const (
		project = "con027"
		prodID  = "con0271"
		stageID = project + "--staging--web"
	)
	sentinel := "s3cret-sentinel-027-" + c.g.run
	remove := func() {
		for _, path := range []string{"/v1/apps/" + prodID, "/v1/apps/" + stageID, "/v1/projects/" + project} {
			if resp, err := c.g.apiRequest(http.MethodDelete, path, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // a leftover from an aborted run, if any
	for _, id := range []string{prodID, stageID} {
		waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	}
	t.Cleanup(remove)
	t.Cleanup(func() { logTail(t, c.g.target) })

	// 1. The toggle.
	_, body := c.get(t, "/apps")
	if !strings.Contains(body, `<a href="/ops" class="cv-view-link">Operator</a>`) ||
		!strings.Contains(body, `<a href="/apps" class="cv-view-link" aria-current="page">Developer</a>`) {
		t.Errorf("the Developer home has no toggle with Developer current and a link to /ops:\n%.600s", pageText(body))
	}
	resp, body := c.get(t, "/ops")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops = %d:\n%s", resp.StatusCode, pageText(body))
	}
	mode := ""
	for _, ck := range resp.Cookies() {
		if ck.Name == "caravana_mode" || ck.Name == "__Host-caravana_mode" {
			mode = ck.Value
		}
	}
	if mode != "ops" {
		t.Errorf("GET /ops set the mode cookie to %q, want ops", mode)
	}
	for _, want := range []string{`<a href="/ops" class="cv-view-link" aria-current="page">Operator</a>`,
		`<a href="/ops" class="cv-nav-item" aria-current="page">`, `class="cv-label cv-nav-title">Platform</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("/ops lacks %q", want)
		}
	}
	if _, body = c.get(t, "/settings"); !strings.Contains(body, `class="cv-label cv-nav-title">Platform</span>`) ||
		strings.Contains(body, `class="cv-label cv-nav-title">Workspace</span>`) {
		t.Errorf("Settings after /ops doesn't carry the Operator sidebar")
	}
	c.get(t, "/apps")
	if _, body = c.get(t, "/settings"); !strings.Contains(body, `class="cv-label cv-nav-title">Workspace</span>`) {
		t.Errorf("Settings after /apps doesn't carry the Developer sidebar")
	}

	// 2. Two apps in two places.
	c.g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"`+project+`","displayName":"Console 027"}`)
	c.g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"staging"}`)
	c.deployLabApp(t, prodID, labApp(actionsWeb("", 1)))
	c.deployLabApp(t, stageID, labApp(actionsWeb("", 1)))
	var copies []string
	waitFor(t, "a running copy of "+prodID, time.Minute, func() bool {
		copies = c.g.runningCopies(t, prodID)
		return len(copies) == 1
	})

	c.openLive(t, "/live?watch=ops")
	c.expectEvent(t, "sync", 5*time.Second)

	var page string
	waitFor(t, "the Overview's stats to have the host's use", 2*time.Minute, func() bool {
		_, page = c.get(t, "/ops")
		stats := strings.Join(opsStatsOf(page), " · ")
		return strings.Contains(stats, "cpu ") && strings.Contains(stats, "mem ")
	})
	stats := opsStatsOf(page)
	if len(stats) < 6 || stats[0] != "1 server" || !regexp.MustCompile(`^\d+ apps?$`).MatchString(stats[1]) ||
		!regexp.MustCompile(`^\d+ copies$|^1 copy$`).MatchString(stats[2]) || !regexp.MustCompile(`^cpu \d+%$`).MatchString(stats[3]) ||
		!regexp.MustCompile(`^mem \d+%$`).MatchString(stats[4]) || !regexp.MustCompile(`^\d+ open items?$`).MatchString(stats[5]) {
		t.Errorf("the stats line = %q, want server, apps, copies, cpu, mem and open items", stats)
	}

	// The node panel: its facts are Nomad's own.
	nodes, _, err := c.g.target.Nomad.Nodes().List(nil)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("Nomad's nodes = %v, %v; want the lab's one", nodes, err)
	}
	node, _, err := c.g.target.Nomad.Nodes().Info(nodes[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	panel := regexp.MustCompile(`(?s)<div class="cv-panel cv-node">.*?<p class="cv-node-spec">([^<]*)</p>`).FindStringSubmatch(page)
	if panel == nil {
		t.Fatalf("/ops has no node panel with a spec line:\n%.800s", pageText(page))
	}
	spec := coSpecRe.FindStringSubmatch(pageText(panel[1]))
	if spec == nil || spec[1] != node.Attributes["cpu.numcores"] || spec[2] != node.Attributes["kernel.version"] {
		t.Errorf("the spec line is %q, want cores %s and kernel %s of Nomad's record", panel[1], node.Attributes["cpu.numcores"], node.Attributes["kernel.version"])
	}
	if !strings.Contains(page, `class="cv-node-name">`+node.Name+`</a>`) {
		t.Errorf("the node panel doesn't name Nomad's node %q", node.Name)
	}
	chips := map[string]bool{}
	for _, m := range coChipRe.FindAllStringSubmatch(page, -1) {
		chips[m[1]] = true
	}
	for _, id := range []string{prodID, stageID} {
		if !chips["/apps/"+id] {
			t.Errorf("the node panel has no copy chip linking to /apps/%s (chips: %v)", id, chips)
		}
	}
	if n := strings.Count(page, `cv-mnmeter-label`); n < 3 {
		t.Errorf("the page has %d meter labels, want mem, cpu and disk in the panel", n)
	}
	if n := strings.Count(page, `cv-mnmeter-split`); n != 2 {
		t.Errorf("the page has %d split readouts, want the memory and the CPU of the panel", n)
	}

	// The apps table: both rows, the second with its place before its name.
	rows := map[string][2]string{} // href -> prefix, name
	for _, m := range coAppRe.FindAllStringSubmatch(page, -1) {
		rows[m[2]] = [2]string{m[1], m[3]}
	}
	if r, ok := rows["/apps/"+prodID]; !ok || r[0] != "" || r[1] != prodID {
		t.Errorf("the table's row of %s = %v, want no place prefix", prodID, r)
	}
	if r, ok := rows["/apps/"+stageID]; !ok || r[0] != "Console 027 › staging ›" || r[1] != "web" {
		t.Errorf("the table's row of %s = %v, want \"Console 027 › staging ›\" and web", stageID, r)
	}
	if got := pageText(page); !strings.Contains(got, "Live activity from caravana's records") || strings.Contains(got, "Capacity") {
		t.Errorf("/ops lacks Live activity or still has Capacity:\n%.600s", got)
	}

	// 3. Live activity: the deploys, a command, a restart.
	if !c.opsHasEntry(t, regexp.MustCompile(`^`+prodID+` v\d+ is live, through the API`)) ||
		!c.opsHasEntry(t, regexp.MustCompile(`^web v\d+ is live, through the API`)) {
		t.Errorf("listActivity doesn't have the two deploys: %v", c.opsEntries(t))
	}
	runPath := "/apps/" + prodID + "/copies/" + copies[0] + "/run"
	if resp, out := c.post(t, runPath, url.Values{"command": {"echo " + sentinel}}); resp.StatusCode != http.StatusOK || !strings.Contains(out, sentinel) {
		t.Fatalf("POST %s = %d, want 200 with the output shown once", runPath, resp.StatusCode)
	}
	restart := func() {
		for range 100 { // the deploy that just ended may still hold the app's lock a moment: 409 is "wait"
			resp, out := c.post(t, "/apps/"+prodID+"/restart", url.Values{})
			if resp.StatusCode == http.StatusSeeOther {
				return
			}
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("POST restart = %d: %s", resp.StatusCode, pageText(out))
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("POST restart stayed busy for 5 s")
	}
	restart()
	c.expectEvent(t, "actions", 10*time.Second)
	restarted := regexp.MustCompile(`^` + prodID + ` was restarted from the console\.`)
	waitFor(t, "the restart to end and be in the feed", 4*time.Minute, func() bool { return c.opsHasEntry(t, restarted) })
	echoed := regexp.MustCompile(`^echo ran in ` + prodID + ` from the console, exit 0\.`)
	if !c.opsHasEntry(t, echoed) {
		t.Errorf("listActivity doesn't say the command ran: %v", c.opsEntries(t))
	}
	panelWords := func() string {
		_, part := c.get(t, "/parts/ops/activity")
		return pageText(part)
	}
	got := panelWords()
	for _, want := range []string{prodID + " was restarted from the console.", "echo ran in " + prodID + " from the console, exit 0."} {
		if !strings.Contains(got, want) {
			t.Errorf("Live activity lacks %q:\n%s", want, got)
		}
	}
	noSentinel := func(when string) {
		for _, p := range []string{"/ops", "/parts/ops/overview", "/parts/ops/activity", "/v1/activity?limit=50"} {
			if _, out := c.get(t, p); strings.Contains(out, sentinel) {
				t.Errorf("GET %s %s has the command's output", p, when)
			}
		}
		if status, raw := c.g.send(t, http.MethodGet, "/v1/activity?limit=50", ""); status != http.StatusOK || strings.Contains(string(raw), sentinel) {
			t.Errorf("listActivity %s = %d, has the command's output: %v", when, status, strings.Contains(string(raw), sentinel))
		}
	}
	noSentinel("after the run")

	// 4. Still there after Caravana restarts.
	if c.g.target.RestartCaravana != nil {
		before := c.opsEntries(t)
		c.g.target.RestartCaravana(t)
		waitFor(t, "the console to answer after the restart", time.Minute, func() bool {
			resp, _ := c.get(t, "/ops")
			return resp.StatusCode == http.StatusOK
		})
		got := panelWords()
		for _, want := range []string{prodID + " was restarted from the console.", "echo ran in " + prodID + " from the console, exit 0."} {
			if !strings.Contains(got, want) {
				t.Errorf("after Caravana restarted, Live activity lacks %q:\n%s", want, got)
			}
		}
		after := c.opsEntries(t)
		ids := map[string]bool{}
		for _, e := range after {
			ids[e.ID] = true
		}
		for _, e := range before {
			if e.Kind != "server" && !ids[e.ID] {
				t.Errorf("entry %s (%s) is gone after Caravana restarted", e.ID, e.Message)
			}
		}
		noSentinel("after Caravana restarted")
	} else {
		t.Log("this target can't restart Caravana; records surviving a restart isn't checked")
	}

	// 5. The page renders in under a second (the best of three, past the first read's warm-up).
	best := time.Hour
	for range 3 {
		start := time.Now()
		if resp, _ := c.get(t, "/ops"); resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /ops = %d", resp.StatusCode)
		}
		best = min(best, time.Since(start))
	}
	if best > time.Second {
		t.Errorf("GET /ops took %v at best, want under 1 s", best)
	}

	// The API's twin answers the same facts. After the restart above, the new process holds no
	// node facts until its sampler's first tick (the spec allows the block to be empty for one
	// sampling period after a start), so wait for it rather than read once.
	var host struct {
		Node *struct {
			Name string `json:"name"`
		} `json:"node"`
	}
	var raw []byte
	deadline := time.Now().Add(90 * time.Second)
	for {
		raw = c.g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/host", "")
		if err := json.Unmarshal(raw, &host); err == nil && host.Node != nil {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if host.Node == nil || host.Node.Name != node.Name {
		t.Errorf("getHost's node within 90 s of the restart = %.300s, want the lab's node %s", raw, node.Name)
	}
}
