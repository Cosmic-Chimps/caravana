package main

// The console-ops-server step of the lab test (feature 027 page 2, quickstart §3): the Operator
// server page against real Nomad. It is in its own file because console_ops_test.go (page 1) is
// long; runConsole lists it after console-ops-overview.

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	// coSrvSpecRe is the head's spec line.
	coSrvSpecRe = regexp.MustCompile(`<span class="cv-srvspec">([^<]*)</span>`)
	// coSrvFactRe is a row of the facts panel: its key and its value.
	coSrvFactRe = regexp.MustCompile(`(?s)<dt>([^<]*)</dt>\s*<dd[^>]*>(.*?)</dd>`)
	// coSrvCopyRe is a copy's link in Running here: the copy ID and the app it goes to.
	coSrvCopyRe = regexp.MustCompile(`<a href="(/apps/[^"]*)" id="srv-copy-([^"]*)" class="cv-srvlink`)
	// coSrvUpSinceRe is the facts' "up since" time: UTC, with the date.
	coSrvUpSinceRe = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d UTC`)
)

// labHostRoot makes the directory the lab's in-process Caravana reads as the host's root
// (CARAVANA_HOST_ROOT): in the lab Caravana runs on the Mac, so /var/log/apt and /run would be the
// Mac's own, which has neither. It starts empty, as a host with no history: the console-ops steps
// write apt's history and the reboot marker into it when they need them, and the sampler reads the
// files on every tick.
func labHostRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"var/log/apt", "run"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeAptHistory writes two runs into the host root's apt history, a minute apart and ending just
// now: Ubuntu's automatic updates upgrading three packages, one of which is libc6, and a person
// installing htop. Also the reboot marker, naming libc6, as the real one does. It returns a function
// that takes them away again, so that the steps after this one see a host with no history.
func writeAptHistory(t *testing.T, root string) (remove func()) {
	t.Helper()
	at := func(ago time.Duration) string { return time.Now().UTC().Add(-ago).Format("2006-01-02  15:04:05") }
	history := "\nStart-Date: " + at(3*time.Minute) + "\n" +
		"Commandline: /usr/bin/unattended-upgrade\n" +
		"Upgrade: libc6:amd64 (2.39-0ubuntu8.5, 2.39-0ubuntu8.6), libc-bin:amd64 (2.39-0ubuntu8.5, 2.39-0ubuntu8.6), openssl:amd64 (3.0.13-0ubuntu3.5, 3.0.13-0ubuntu3.6)\n" +
		"End-Date: " + at(3*time.Minute-20*time.Second) + "\n" +
		"\nStart-Date: " + at(2*time.Minute) + "\n" +
		"Commandline: apt install htop\n" +
		"Requested-By: ubuntu (1000)\n" +
		"Install: htop:amd64 (3.3.0-4build1)\n" +
		"End-Date: " + at(2*time.Minute-5*time.Second) + "\n"
	files := map[string]string{
		"var/log/apt/history.log":  history,
		"run/reboot-required":      "*** System restart required ***\n",
		"run/reboot-required.pkgs": "libc6\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		for name := range files {
			_ = os.Remove(filepath.Join(root, name))
		}
	}
}

// consoleOpsServer: the Operator view's second page.
//
//  1. Two apps in two places (con0272a in Default › production, web in the project con0272's
//     staging), deployed through the API, and apt's history and the reboot marker written into the
//     host root the in-process Caravana reads (the lab's Caravana runs on the Mac, not on the VM).
//  2. /ops/servers redirects (302) to the node's own page, /ops/servers/<name>; another name is a 404
//     page that names the real one.
//  3. The head: the node's name, ready, "reboot needed" linking to Updates, the spec line (cores
//     and kernel checked against Nomad's own record), and no action (no button, no form).
//  4. The facts are the lab's real ones: pool, the addresses (the private one only when the node has caravana0), the versions of Nomad, Docker,
//     Traefik (its job's image tag) and Caravana, the OS and kernel, the cores, up since, last seen.
//  5. Running here has a row for every running copy of both apps, linking to its app, the second
//     with its place; the copy's memory is a value (Nomad collects it) and its Up a time.
//  6. Recent on this server has the two apt runs as Caravana says them: Ubuntu's automatic updates
//     with the reboot, and the one by hand.
//  7. The part answers with the body and the head by id, and the page renders in under a second.
//
// Budget: two deploys, then the sampler's tick (10 s in the lab) to read the files.
func (c *console) consoleOpsServer(t *testing.T) {
	const (
		project = "con0272"
		prodID  = "con0272a"
		stageID = project + "--staging--web"
	)
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

	// 1. The apps and the host's files.
	c.g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"`+project+`","displayName":"Console 0272"}`)
	c.g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"staging"}`)
	c.deployLabApp(t, prodID, labApp(actionsWeb("", 1)))
	c.deployLabApp(t, stageID, labApp(actionsWeb("", 1)))
	copiesOf := map[string][]string{}
	for _, id := range []string{prodID, stageID} {
		waitFor(t, "a running copy of "+id, time.Minute, func() bool {
			copiesOf[id] = c.g.runningCopies(t, id)
			return len(copiesOf[id]) == 1
		})
	}
	if root := c.g.target.HostRoot; root != "" {
		t.Cleanup(writeAptHistory(t, root))
	} else {
		t.Log("this target reads the machine's own files; the apt history and reboot checks are skipped")
	}

	// Nomad's own record of the node: the facts below are checked against it.
	nodes, _, err := c.g.target.Nomad.Nodes().List(nil)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("Nomad's nodes = %v, %v; want the lab's one", nodes, err)
	}
	node, _, err := c.g.target.Nomad.Nodes().Info(nodes[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	pagePath := "/ops/servers/" + node.Name

	// 2. The redirect and the 404 page.
	waitFor(t, "the sampler's first reading of the node", 2*time.Minute, func() bool {
		resp, _ := c.get(t, "/ops/servers")
		return resp.StatusCode == http.StatusFound
	})
	if resp, _ := c.get(t, "/ops/servers"); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != pagePath {
		t.Errorf("GET /ops/servers = %d to %q, want 302 to %s", resp.StatusCode, resp.Header.Get("Location"), pagePath)
	}
	if resp, body := c.get(t, "/ops/servers/nope"); resp.StatusCode != http.StatusNotFound ||
		!strings.Contains(pageText(body), "No server called nope") ||
		!strings.Contains(pageText(body), "This caravana has one: "+node.Name) {
		t.Errorf("GET /ops/servers/nope = %d, want a 404 page that names %s:\n%.400s", resp.StatusCode, node.Name, pageText(body))
	}

	// Wait for what the sampler reads from the files: the two runs are in the server's own history.
	if c.g.target.HostRoot != "" {
		auto := regexp.MustCompile(`^OS packages updated by Ubuntu's automatic updates: 3 packages, a reboot is needed\.`)
		waitFor(t, "the sampler to record the two apt runs", 2*time.Minute, func() bool {
			return c.opsHasEntry(t, auto)
		})
	}

	// 3. The page and its head.
	var page string
	waitFor(t, "the server page to show every copy with its memory", 2*time.Minute, func() bool {
		resp, body := c.get(t, pagePath)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d:\n%.600s", pagePath, resp.StatusCode, pageText(body))
		}
		page = body
		for _, id := range []string{prodID, stageID} {
			row := srvRowOf(page, copiesOf[id][0])
			if row == "" || !strings.Contains(row, `cv-srvunit">M`) && !strings.Contains(row, `cv-srvunit">G`) {
				return false
			}
		}
		return true
	})
	head := page[strings.Index(page, `id="srv-head"`):strings.Index(page, `id="srv-body"`)]
	for _, want := range []string{`<a href="/ops" class="cv-srvback">← Overview</a>`, `<h1 class="cv-srvname">` + node.Name + `</h1>`,
		`cv-mark-healthy" aria-hidden="true"></span>ready`} {
		if !strings.Contains(head, want) {
			t.Errorf("the head lacks %q:\n%s", want, pageText(head))
		}
	}
	for _, action := range []string{"<button", "<form", "Open shell", "Move work off", "Take work again"} {
		if strings.Contains(head, action) {
			t.Errorf("the head has an action (%s)", action)
		}
	}
	if c.g.target.HostRoot != "" && !strings.Contains(head, `<a href="/ops/updates" class="cv-pill cv-pill-warning cv-srvreboot">`) {
		t.Errorf("the head lacks the \"reboot needed\" pill that links to Updates:\n%s", pageText(head))
	}
	spec := coSrvSpecRe.FindStringSubmatch(head)
	if spec == nil {
		t.Fatalf("the head has no spec line:\n%s", pageText(head))
	}
	if m := coSpecRe.FindStringSubmatch(spec[1]); m == nil || m[1] != node.Attributes["cpu.numcores"] || m[2] != node.Attributes["kernel.version"] {
		t.Errorf("the spec line is %q, want cores %s and kernel %s of Nomad's record", spec[1], node.Attributes["cpu.numcores"], node.Attributes["kernel.version"])
	}

	// 4. The facts: the lab's real ones.
	facts := map[string]string{}
	for _, m := range coSrvFactRe.FindAllStringSubmatch(page, -1) {
		facts[m[1]] = pageText(m[2])
	}
	var health struct {
		Version string `json:"version"`
	}
	c.g.getJSON(t, "/v1/health", &health)
	agent := "nomad " + node.Attributes["nomad.version"] + " · docker " + node.Attributes["driver.docker.version"] + " · traefik "
	if image := traefikImage(c); image != "" {
		agent += strings.TrimPrefix(image[strings.LastIndex(image, ":")+1:], "v")
	}
	if got := facts["agent"]; !strings.HasPrefix(got, agent) || !strings.HasSuffix(got, " · caravana "+health.Version) {
		t.Errorf("agent = %q, want it to start with %q and end with caravana %s", got, agent, health.Version)
	}
	osWant := strings.ToUpper(node.Attributes["os.name"][:1]) + node.Attributes["os.name"][1:] + " " + node.Attributes["os.version"] + " · kernel " + node.Attributes["kernel.version"]
	if facts["os"] != osWant {
		t.Errorf("os = %q, want %q", facts["os"], osWant)
	}
	if got := facts["cpu"]; !strings.Contains(got, node.Attributes["cpu.numcores"]+" cores") {
		t.Errorf("cpu = %q, want %s cores", got, node.Attributes["cpu.numcores"])
	}
	if got := facts["pool"]; got != node.NodePool {
		t.Errorf("pool = %q, want %q", got, node.NodePool)
	}
	// The addresses are named. The private one is the caravana0 interface's, and only that: the lab's node
	// runs on Lima's eth0 (192.168.5.15), which must not be shown as private; a host the installer made
	// lists caravana0, and its address is.
	private := ""
	if node.NodeResources != nil {
		for _, n := range node.NodeResources.Networks {
			if n != nil && n.Device == "caravana0" {
				private = n.IP
			}
		}
	}
	got := facts["address"]
	if !strings.Contains(got, " · public") {
		t.Errorf("address = %q, want the public address named", got)
	}
	switch {
	case private != "" && !strings.Contains(got, private+" · private"):
		t.Errorf("address = %q, want the caravana0 address %s named private", got, private)
	case private == "" && (strings.Contains(got, "private") || strings.Contains(got, node.Attributes["unique.network.ip-address"])):
		t.Errorf("address = %q: this node has no caravana0, so no private line (Nomad's ip-address %s is another interface)", got, node.Attributes["unique.network.ip-address"])
	}
	if got := facts["up since"]; !coSrvUpSinceRe.MatchString(got) || !strings.Contains(got, " ago)") && !strings.Contains(got, "just now)") {
		t.Errorf("up since = %q, want a UTC time with how long ago", got)
	}
	if got := facts["last seen"]; got == "" || strings.Contains(got, "lost") {
		t.Errorf("last seen = %q, want a heartbeat of a node that is up", got)
	}

	// 5. Running here: a row for every running copy of both apps.
	links := map[string]string{} // copy ID -> the app it goes to
	for _, m := range coSrvCopyRe.FindAllStringSubmatch(page, -1) {
		links[m[2]] = m[1]
	}
	for id, copies := range copiesOf {
		if got := links[copies[0]]; got != "/apps/"+id {
			t.Errorf("Running here: copy %s of %s links to %q, want /apps/%s (rows: %v)", copies[0], id, got, id, links)
		}
	}
	text := pageText(page)
	if m := regexp.MustCompile(`Running here · (\d+)`).FindStringSubmatch(text); m == nil || len(links) < 2 {
		t.Errorf("Running here has no count or fewer than the two apps' copies:\n%.600s", text)
	}
	if !strings.Contains(text, "Console 0272 › staging › web") {
		t.Errorf("the staging copy's row doesn't name its place:\n%.800s", text)
	}
	if up := upOf(page, copiesOf[prodID][0]); !regexp.MustCompile(`^(under 1 min|\d+ min|\d+ h|\d+ d)`).MatchString(up) {
		t.Errorf("the copy's Up is %q, want a time", up)
	}

	// 6. Recent on this server.
	if c.g.target.HostRoot != "" {
		region := page[strings.Index(page, `class="cv-panel cv-srvrecent"`):]
		recent := pageText(region[:strings.Index(region, "</section>")])
		for _, want := range []string{
			"OS packages updated by Ubuntu's automatic updates: 3 packages, a reboot is needed.",
			"OS packages updated by hand: 1 package.",
		} {
			if !strings.Contains(recent, want) {
				t.Errorf("Recent on this server lacks %q:\n%s", want, recent)
			}
		}
	}

	// 7. The part and the clock.
	resp, part := c.get(t, "/parts/ops/servers/"+node.Name)
	if resp.StatusCode != http.StatusOK || !strings.Contains(part, `hx-swap-oob="innerHTML:#srv-head"`) ||
		!strings.Contains(part, `id="srv-copy-`+copiesOf[prodID][0]+`"`) || strings.Contains(part, "<html") {
		t.Errorf("the part = %d, want the body and the head by id:\n%.400s", resp.StatusCode, pageText(part))
	}
	if strings.Contains(page, `text-fg-4`) || regexp.MustCompile(`\sstyle\s*=`).MatchString(page) {
		t.Error("the page has text in fg-4 or a style attribute")
	}
	best := time.Hour
	for range 3 {
		start := time.Now()
		if resp, _ := c.get(t, pagePath); resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", pagePath, resp.StatusCode)
		}
		best = min(best, time.Since(start))
	}
	if best > time.Second {
		t.Errorf("GET %s took %v at best, want under 1 s", pagePath, best)
	}
}

// srvRowOf is the row of Running here that has the copy's link, from its start up to its Up cell
// (excluded); "" when the copy has no row.
func srvRowOf(page, copyID string) string {
	for _, r := range strings.Split(page, `<div class="cv-srvrow"`)[1:] {
		if strings.Contains(r, `id="srv-copy-`+copyID+`"`) {
			if i := strings.Index(r, `<div role="cell" class="cv-srvcell cv-srvup"`); i >= 0 {
				return r[:i]
			}
			return r
		}
	}
	return ""
}

// upOf is the text of the Up cell of the copy's row.
func upOf(page, copyID string) string {
	for _, r := range strings.Split(page, `<div class="cv-srvrow"`)[1:] {
		if strings.Contains(r, `id="srv-copy-`+copyID+`"`) {
			m := regexp.MustCompile(`class="cv-srvcell cv-srvup" data-label="Up">([^<]*)<`).FindStringSubmatch(r)
			if m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// traefikImage is the image of Traefik's job in the lab ("traefik:v3.7.13"), "" when it can't be read.
func traefikImage(c *console) string {
	job, _, err := c.g.target.Nomad.Jobs().Info("traefik", nil)
	if err != nil || len(job.TaskGroups) == 0 || len(job.TaskGroups[0].Tasks) == 0 {
		return ""
	}
	image, _ := job.TaskGroups[0].Tasks[0].Config["image"].(string)
	return image
}
