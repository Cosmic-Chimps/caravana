package main

// The console-servers step of the lab test (feature 031, the console's Servers pages): the lab's one
// node is listed, not managed (the lab has no private network and no cluster CA, so no server row),
// Add a server says why no server can be added instead of showing its form, and GET /v1/servers
// agrees with the page. No join is made: that needs a second machine with a mesh, which the lab
// doesn't have (deploy/dev/cluster-test.sh does). It is in its own file only because
// console_lab_test.go is long; runConsole lists it after console-ops-updates.

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// htmlTagRe is a tag: dropping them leaves the page's words.
var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// htmlText is a page's words: tags dropped, entities read, white space made one space.
func htmlText(page string) string {
	return strings.Join(strings.Fields(html.UnescapeString(htmlTagRe.ReplaceAllString(page, " "))), " ")
}

var (
	// coServerRowRe is a row of the Servers list: its link and the server's name.
	coServerRowRe = regexp.MustCompile(`<a href="(/ops/servers/[^"]*)" id="srv-row-[^"]*" class="cv-sllink[^"]*">([^<]*)</a>`)
	// coServersLineRe is the head's line of sums: "1 server · 3.8 GB · 2 vCPU".
	coServersLineRe = regexp.MustCompile(`<span class="cv-slline">([^<]*)</span>`)
)

// consoleServers: through the console's pages and the API, against the lab's real Nomad.
//
//  1. GET /ops/servers lists the lab's one node, with the name Nomad gives it (the API's listServers
//     says the same name), "Not set up for a cluster yet." (it is not managed: no server row), the line of
//     sums "1 server · … GB · … vCPU", and no sentence about the cluster. Add server is a link to
//     /ops/servers/new, drawn as unavailable, with the notice that says why.
//  2. GET /v1/servers agrees: one server, managed false, the cluster says it can't add one
//     (canAdd false, with the sentence).
//  3. GET /ops/servers/new shows the service's cluster_not_ready sentence instead of the form, and
//     POST /ops/servers answers 409 with the same sentence and makes nothing: the API still lists one
//     server.
//  4. The lab's node has a server page (links and plain GETs), which says Updates and restarts are
//     read on the server Caravana runs on only when there is more than one server: here it doesn't.
//  5. GET /parts/ops/servers is the region the page draws, and the Operator sidebar marks Servers.
func (c *console) consoleServers(t *testing.T) {
	const notReady = "Run the installer again on this server first: it sets up the private network and Nomad's certificates."

	// The API first: the truth the page must agree with.
	var cluster struct {
		Cluster struct {
			Servers         int    `json:"servers"`
			CanAdd          bool   `json:"canAdd"`
			CannotAddReason string `json:"cannotAddReason"`
		} `json:"cluster"`
		Servers []struct {
			Name    string `json:"name"`
			State   string `json:"state"`
			Managed bool   `json:"managed"`
		} `json:"servers"`
	}
	c.g.getJSON(t, "/v1/servers", &cluster)
	if len(cluster.Servers) != 1 || cluster.Servers[0].Managed || cluster.Servers[0].Name == "" || cluster.Servers[0].State != "ready" {
		t.Fatalf("GET /v1/servers = %+v, want the lab's one node, ready and not managed", cluster)
	}
	if cluster.Cluster.CanAdd || cluster.Cluster.CannotAddReason != notReady || cluster.Cluster.Servers != 1 {
		t.Errorf("the cluster = %+v, want one server and canAdd false with the sentence %q", cluster.Cluster, notReady)
	}
	name := cluster.Servers[0].Name

	// 1. The list.
	resp, list := c.get(t, "/ops/servers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops/servers = %d:\n%s", resp.StatusCode, list)
	}
	rows := coServerRowRe.FindAllStringSubmatch(list, -1)
	if len(rows) != 1 || rows[0][2] != name || rows[0][1] != "/ops/servers/"+name {
		t.Errorf("the list's rows = %v, want the node %q alone", rows, name)
	}
	if line := coServersLineRe.FindStringSubmatch(list); line == nil || !regexp.MustCompile(`^1 server · [\d.]+ GB · \d+ vCPU$`).MatchString(line[1]) {
		t.Errorf("the line of sums = %v, want \"1 server · … GB · … vCPU\"", line)
	}
	words := htmlText(list)
	for _, want := range []string{"Not set up for a cluster yet.", notReady} {
		if !strings.Contains(words, want) {
			t.Errorf("the list lacks %q:\n%s", want, words)
		}
	}
	for _, want := range []string{`href="/ops/servers/new" class="cv-btn cv-btn-primary cv-btn-sm" aria-disabled="true"`,
		`<a href="/ops/servers" class="cv-nav-item" aria-current="page">`, `hx-get="/parts/ops/servers"`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	if strings.Contains(list, "cv-slcluster") {
		t.Error("a cluster of one has a sentence")
	}

	// 3. Add a server says why it can't, and a post makes nothing.
	resp, add := c.get(t, "/ops/servers/new")
	if resp.StatusCode != http.StatusOK || !strings.Contains(htmlText(add), notReady) || strings.Contains(add, `action="/ops/servers"`) {
		t.Errorf("GET /ops/servers/new = %d, want the sentence and no form:\n%s", resp.StatusCode, htmlText(add))
	}
	resp, refused := c.post(t, "/ops/servers", url.Values{"name": {"fsn-2"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(htmlText(refused), notReady) || strings.Contains(refused, "cvjn_") {
		t.Errorf("POST /ops/servers = %d, want 409 with the sentence and no token:\n%s", resp.StatusCode, htmlText(refused))
	}
	c.g.getJSON(t, "/v1/servers", &cluster)
	if len(cluster.Servers) != 1 {
		t.Errorf("a refused post left %d servers", len(cluster.Servers))
	}

	// 4. The node's own page.
	resp, page := c.get(t, "/ops/servers/"+name)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `<a href="/ops/servers" class="cv-srvback">← Servers</a>`) {
		t.Errorf("GET /ops/servers/%s = %d, want the page with a back link to the list", name, resp.StatusCode)
	}
	if strings.Contains(page, "are read on") {
		t.Error("a cluster of one says where updates are read")
	}

	// 5. The part.
	resp, part := c.get(t, "/parts/ops/servers")
	if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") || !strings.Contains(part, ">"+name+"</a>") {
		t.Errorf("GET /parts/ops/servers = %d, want the region with %q", resp.StatusCode, name)
	}
}
