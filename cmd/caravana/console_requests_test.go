package main

// The console-requests step of the lab test (feature 047, plan "Phase C"): the
// Requests page, the app's Requests tab and Monitoring's link, through the
// signed-in console (links and plain GETs, no JavaScript), against real Nomad
// and Traefik. The requests are made through the lab's edge (127.0.0.1:8443,
// the host as the TLS server name), so Traefik writes them to its access log
// and Caravana reads them back from Nomad. It is in its own file only because
// console_lab_test.go is long; runConsole lists it after console-logs.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requestsScript is the command of the apps of the requests steps: busybox's
// httpd with an index page and two CGI scripts, /cgi-bin/boom (500) and
// /cgi-bin/things (201). busybox's httpd answers a POST to anything that isn't
// a CGI script with 501 (verified in the lab), so the POST of the steps goes to
// a CGI script, which is the only place it can be answered with a 2xx.
const requestsScript = `mkdir -p /www/cgi-bin; echo ok > /www/index.html
printf '#!/bin/sh\necho "Status: 500 Boom"\necho\necho boom body\n' > /www/cgi-bin/boom
printf '#!/bin/sh\necho "Status: 201 Created"\necho\necho made\n' > /www/cgi-bin/things
chmod +x /www/cgi-bin/boom /www/cgi-bin/things
exec httpd -f -p 8080 -h /www`

// requestsWeb is the web service of the requests steps, with a domain.
func requestsWeb(host string) map[string]any {
	return map[string]any{
		"name": "web", "image": "busybox:1.37", "command": "sh",
		"args":      []string{"-c", requestsScript},
		"ports":     []map[string]any{{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"domains":   []map[string]any{{"host": host, "port": "http"}},
		"resources": labResources,
	}
}

// requestsRow is one row of the Requests table as the page draws it.
type requestsRow struct {
	Href, Method, Host, Path, Class, Status string
	Error                                   bool
}

// rqRowRe is one request's row (requests.html's requests-region).
var rqRowRe = regexp.MustCompile(`(?s)<a href="([^"]*)" class="cv-rqrow( cv-row-error)?" title="Logs around this request">\s*` +
	`<time class="cv-ltime"[^>]*>[^<]*</time>\s*` +
	`<span class="cv-lsvc[^"]*"><span class="cv-svcname">[^<]*</span></span>\s*` +
	`<span class="cv-rqmethod">([^<]*)</span>\s*` +
	`<span class="cv-rqwhat"><span class="cv-rqhost">([^<]*)</span><span class="cv-rqpath">([^<]*)</span>.*?</span>\s*` +
	`<span class="cv-rqstatus cv-rqs-(\w+)">([^<]*)</span>`)

// requestsRowsOf reads the request rows of a page, in page order (newest first).
func requestsRowsOf(page string) []requestsRow {
	var rows []requestsRow
	for _, m := range rqRowRe.FindAllStringSubmatch(page, -1) {
		rows = append(rows, requestsRow{
			Href: html.UnescapeString(m[1]), Error: m[2] != "", Method: m[3],
			Host: html.UnescapeString(m[4]), Path: html.UnescapeString(m[5]), Class: m[6], Status: m[7],
		})
	}
	return rows
}

// rowsWithPath is the rows whose path is exactly path.
func rowsWithPath(rows []requestsRow, path string) []requestsRow {
	var out []requestsRow
	for _, r := range rows {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// requestsEdge makes the three requests of the steps through the lab's edge
// (or the host's address on a real host, as hostStatus does): a GET of the
// index with the sentinel in its query string, a POST to /cgi-bin/things and a
// GET of /cgi-bin/boom, which answers 500. It fails the test when a status
// isn't the one the script gives.
func (g *gate) requestsEdge(t *testing.T, host, sentinel string) {
	t.Helper()
	for _, call := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/?token=" + sentinel, http.StatusOK},
		{http.MethodPost, "/cgi-bin/things", http.StatusCreated},
		{http.MethodGet, "/cgi-bin/boom", http.StatusInternalServerError},
	} {
		status, _, _, err := g.domainCall(host, call.method, call.path, "", "")
		if err != nil {
			t.Fatalf("%v", strings.ReplaceAll(err.Error(), sentinel, "<sentinel>"))
		}
		if status != call.want {
			t.Fatalf("%s https://%s%s = %d, want %d", call.method, host, strings.ReplaceAll(call.path, sentinel, "<sentinel>"), status, call.want)
		}
		// Each request ends before the next starts, so their start times are
		// in this order whatever Traefik's clock resolution.
		time.Sleep(50 * time.Millisecond)
	}
}

// consoleRequests: the Requests page against one busybox app, cl047<hex>, deployed
// through the API with a domain (New app's form has no command field):
//
//  1. the app (cl047 and a random suffix) is healthy and answers its domain; a GET of /?token=<sentinel>, a
//     POST to /cgi-bin/things (201) and a GET of /cgi-bin/boom (500) go
//     through Traefik;
//  2. within 20 s the app's Requests tab shows the three rows with the path
//     alone (no query string), the 500 as 5xx, each row a link to Logs around
//     its time;
//  3. the 5xx chip shows only the boom row, a path prefix only the POST, a
//     method and a minimum duration narrow the rows, and a status with no
//     match says so;
//  4. the page follows Traefik (Live), says "Requests" and its footer, draws
//     the histogram with its summary and no style attribute, and /parts/requests
//     draws the same rows;
//  5. the workspace page /requests lists the same rows with the app and the
//     rail, the row's link to Logs answers 200 and /apps/<id>/logs shows the
//     tab;
//  6. Monitoring has the "See the requests that failed" link, which opens
//     /requests with the 5xx chip on;
//  7. the sentinel is in no page read, no API answer and no line of Caravana's
//     log.
//
// A failure message never holds the sentinel.
func (c *console) consoleRequests(t *testing.T) {
	c.ensureSignedIn(t)

	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	// Traefik's log keeps the requests of earlier runs, and a request belongs
	// to the app ID it names (also an ID made again), so each run has an app of
	// its own and counts only its own three requests.
	id := "cl047" + hex.EncodeToString(raw[:2])
	sentinel := "s3cret-sentinel-047-console-" + hex.EncodeToString(raw[:])
	hide := func(s string) string { return strings.ReplaceAll(s, sentinel, "<sentinel>") }
	errorf := func(format string, args ...any) {
		t.Helper()
		t.Error(hide(fmt.Sprintf(format, args...)))
	}
	fatalf := func(format string, args ...any) {
		t.Helper()
		t.Fatal(hide(fmt.Sprintf(format, args...)))
	}

	// Every body read, for the sentinel search at the end.
	var bodies []string
	page := func(path string, want int) string {
		t.Helper()
		resp, body := c.get(t, path)
		bodies = append(bodies, path+"\n"+body)
		if resp.StatusCode != want {
			fatalf("GET %s = %d, want %d: %.600s", path, resp.StatusCode, want, pageText(body))
		}
		return body
	}

	t.Cleanup(func() { c.removeLabApp(id) })
	t.Cleanup(func() { logTail(t, c.g.target) })

	// 1. Deploy, and the three requests.
	host := c.g.extraHost(id)
	c.deployLabApp(t, id, labApp(requestsWeb(host)))
	c.g.waitDomainStatus(t, http.StatusOK, 90*time.Second, host, http.MethodGet, "/", "", "")
	c.g.requestsEdge(t, host, sentinel)

	// 2. The app's Requests tab.
	tab := "/apps/" + id + "/requests"
	var body string
	var rows []requestsRow
	defer func() {
		if t.Failed() {
			t.Logf("the Requests tab last read: %d rows %v: %.500s", len(rows), rows, hide(prose(body)))
		}
	}()
	waitFor(t, "the Requests tab to show the three requests", 20*time.Second, func() bool {
		body = page(tab, http.StatusOK)
		rows = requestsRowsOf(body)
		return len(rowsWithPath(rows, "/cgi-bin/boom")) == 1 && len(rowsWithPath(rows, "/cgi-bin/things")) == 1 && len(rowsWithPath(rows, "/")) >= 1
	})
	text := prose(body)
	for _, want := range []string{"Each HTTP request Traefik served, newest first.", "All requests", "Only the path is recorded, never the query string or headers."} {
		if !strings.Contains(text, want) {
			errorf("%s doesn't say %q: %.400s", tab, want, text)
		}
	}
	boom, things := rowsWithPath(rows, "/cgi-bin/boom")[0], rowsWithPath(rows, "/cgi-bin/things")[0]
	if boom.Method != "GET" || boom.Status != "500" || boom.Class != "5xx" || !boom.Error || boom.Host != host {
		errorf("the boom row is %+v, want GET %s 500 in the 5xx class, drawn as an error", boom, host)
	}
	if things.Method != "POST" || things.Status != "201" || things.Class != "2xx" || things.Error {
		errorf("the things row is %+v, want POST 201 in the 2xx class", things)
	}
	index := rowsWithPath(rows, "/")
	if index[0].Method != "GET" || index[0].Status != "200" {
		errorf("the index rows are %+v, want GET 200", index)
	}
	for _, r := range rows {
		if strings.Contains(r.Path, "?") || strings.Contains(r.Path, "token") {
			errorf("a row's path is %q: the query string must never be drawn", r.Path)
		}
		if !strings.HasPrefix(r.Href, "/logs?") || !strings.Contains(r.Href, id) {
			errorf("a row's link is %q, want Logs around its time for %s", r.Href, id)
		}
	}
	// Newest first: the GET of boom was the last request, the POST the one before.
	if i, j := slices.Index(rows, boom), slices.Index(rows, things); i > j {
		errorf("the rows aren't newest first: boom is row %d, things row %d", i, j)
	}
	if !strings.Contains(body, "following Traefik") {
		errorf("%s doesn't say it is following Traefik: %.300s", tab, text)
	}
	if !strings.Contains(body, `class="cv-hist-svg"`) || !regexp.MustCompile(`aria-label="\d+ requests? in the last [^"]*"`).MatchString(body) {
		errorf("%s has no histogram whose summary counts the requests: %.300s", tab, text)
	}
	if strings.Contains(body, " style=") || strings.Contains(body, "<script>") {
		errorf("%s has a style attribute or an inline script (the CSP forbids both)", tab)
	}
	if !strings.Contains(body, "Read from Traefik's access log") {
		errorf("%s has no footer saying where the requests come from", tab)
	}
	for _, class := range []string{"2xx", "5xx"} {
		if !strings.Contains(body, `rqchip-`+class) {
			errorf("%s has no %s chip", tab, class)
		}
	}

	// 3. Filters.
	filtered := func(query string) []requestsRow {
		t.Helper()
		return requestsRowsOf(page(tab+"?"+query, http.StatusOK))
	}
	if got := filtered("status=5xx"); len(got) != 1 || got[0].Path != "/cgi-bin/boom" || got[0].Class != "5xx" {
		errorf("?status=5xx = %+v, want only the boom row", got)
	}
	if got := filtered("path=/cgi-bin/t"); len(got) != 1 || got[0].Path != "/cgi-bin/things" || got[0].Method != "POST" {
		errorf("?path=/cgi-bin/t = %+v, want only the POST", got)
	}
	if got := filtered("method=POST"); len(got) != 1 || got[0].Path != "/cgi-bin/things" {
		errorf("?method=POST = %+v, want only the POST", got)
	}
	if got := filtered("status=2xx,5xx"); len(got) < 3 {
		errorf("?status=2xx,5xx = %d rows, want the index, the POST and the boom", len(got))
	}
	none := page(tab+"?status=4xx&path=/cgi-bin", http.StatusOK)
	if r := requestsRowsOf(none); len(r) != 0 || !strings.Contains(pageText(none), "No requests match.") {
		errorf("?status=4xx&path=/cgi-bin = %d rows, want none and a sentence saying so: %.300s", len(r), pageText(none))
	}
	// A filter keeps the others in its links: the 5xx chip's page links back to the page with every class.
	if filteredPage := page(tab+"?status=5xx", http.StatusOK); !strings.Contains(filteredPage, `href="`+tab+`?`) {
		errorf("?status=5xx draws no link back to %s", tab)
	}
	// A bad value is a page that says so, never a redirect or an error page of the server.
	page(tab+"?status=6xx", http.StatusBadRequest)

	// 4. The part the live page swaps.
	part := page("/parts/requests?app="+id, http.StatusOK)
	if got := requestsRowsOf(part); len(rowsWithPath(got, "/cgi-bin/boom")) != 1 || len(rowsWithPath(got, "/cgi-bin/things")) != 1 {
		errorf("/parts/requests?app=%s rows = %+v, want the boom and the POST", id, got)
	}
	if !strings.Contains(part, `class="cv-hist-svg"`) || strings.Contains(part, `class="cv-lhead"`) {
		errorf("/parts/requests should draw the histogram and the table alone")
	}

	// 5. The workspace page, and Logs around a row.
	work := page("/requests", http.StatusOK)
	wrows := requestsRowsOf(work)
	if len(rowsWithPath(wrows, "/cgi-bin/boom")) != 1 || len(rowsWithPath(wrows, "/cgi-bin/things")) != 1 {
		errorf("/requests rows = %+v, want the boom and the POST of %s", wrows, id)
	}
	rail := regexp.MustCompile(`(?s)<nav class="cv-lograil" aria-label="Services">(.*?)</nav>`).FindStringSubmatch(work)
	// The rail names the app ("cl047/ web") only when more than one app is listed.
	if rail == nil {
		fatalf("/requests has no services rail")
	}
	if m := regexp.MustCompile(`web (\d+)`).FindStringSubmatch(pageText(rail[1])); m == nil {
		errorf("the rail reads %q, want web with its requests", pageText(rail[1]))
	} else if n, _ := strconv.Atoi(m[1]); n < 3 {
		errorf("the rail counts %d requests for web, want at least 3", n)
	}
	if !strings.Contains(prose(work), "Each HTTP request Traefik served, newest first.") || strings.Contains(work, ">All requests</a>") {
		errorf("/requests is the workspace page: it has the head sentence and no \"All requests\" link")
	}
	page(boom.Href, http.StatusOK)

	// 6. Monitoring's link. Its Errors chart exists once the sampler has a
	// minute of the place's traffic, which the lab's sampler gap makes quick.
	var link string
	waitFor(t, "Monitoring to offer \"See the requests that failed\"", 90*time.Second, func() bool {
		link = clLinkTo(page("/monitoring", http.StatusOK), "See the requests that failed")
		return link != ""
	})
	if !strings.HasPrefix(link, "/requests?") || !strings.Contains(link, "status=5xx") {
		errorf("Monitoring's link is %q, want /requests with the 5xx chip on", link)
	}
	if got := requestsRowsOf(page(link, http.StatusOK)); len(got) == 0 {
		// The link's window is the chart's; the boom request is inside it unless
		// the sampler's first minute started after it.
		t.Logf("Monitoring's link %s lists no failed request (the chart's window may start after the request)", link)
	}

	// 7. The sentinel is nowhere.
	for _, b := range bodies {
		if strings.Contains(b, sentinel) {
			path, _, _ := strings.Cut(b, "\n")
			errorf("the sentinel (a query string) is in the page of %s", path)
		}
	}
	for _, p := range []string{"/v1/requests?app=" + id, "/v1/requests?app=" + id + "&status=5xx", "/v1/logs?app=" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id} {
		status, answer := c.g.send(t, http.MethodGet, p, "")
		if status != http.StatusOK {
			errorf("GET %s = %d, want 200", p, status)
		}
		if strings.Contains(string(answer), sentinel) {
			errorf("the sentinel is in the answer of %s", p)
		}
	}
	if c.g.target.ServerLog != nil && strings.Contains(c.g.target.ServerLog(), sentinel) {
		errorf("the sentinel is in Caravana's log")
	}
}
