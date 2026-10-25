package main

// The console-logs step of the lab test (feature 024, quickstart §6): the Logs
// page, the lines around one, Download and the app's Logs tab through the
// signed-in console (links and plain GETs, no JavaScript), against real Nomad.
// It is in its own file only because console_lab_test.go is long; runConsole
// lists it after console-domains-states.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// consoleLogsScript is cl024a's command: the sample lines every 2 s while
// busybox's httpd answers the health check. SENTINEL (the service's env) is
// printed once at the start. stderr has a timed line (tick) and an untimed one
// ([warn] careful).
const consoleLogsScript = `mkdir -p /www; echo ok > /www/index.html
echo "$SENTINEL"
( while true; do
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) ERROR boom"
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) INFO served the page"
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) tick" >&2
  echo '[warn] careful' >&2
  echo "{\"level\":\"error\",\"msg\":\"json boom\",\"time\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}"
  printf '\033[31mred text\033[0m\n'
  echo 'same same same'; echo 'same same same'; echo 'same same same'
  echo 'no time here'
  sleep 2
done ) &
exec httpd -f -p 8080 -h /www`

// consoleLogsQuiet is cl024b's command: one timed INFO line every 5 s.
const consoleLogsQuiet = `mkdir -p /www; echo ok > /www/index.html
( while true; do echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) INFO quiet"; sleep 5; done ) &
exec httpd -f -p 8080 -h /www`

var (
	// clRowRe is one line's row of the stream (logs.html's logs-region).
	clRowRe = regexp.MustCompile(`(?s)<a href="([^"]*)" id="(line-[^"]*)" class="cv-lrow([^"]*)"(?: aria-current="true")?>\s*` +
		`<time class="cv-ltime( cv-time-est)?" datetime="([^"]*)" title="([^"]*)">([^<]*)</time>\s*` +
		`<span class="cv-llvl cv-llvl-(\w+)">\w+</span>\s*` +
		`<span class="cv-lsvc cv-hue-\d"><span class="cv-svcname">([^<]*)</span><span class="cv-lcopy">([^<]*)</span></span>\s*` +
		`<span class="cv-ltext">([^<]*)</span>\s*(?:<span class="cv-repeat">×(\d+)</span>)?`)
	// clDeployRe is a deploy row of the stream.
	clDeployRe = regexp.MustCompile(`(?s)<a href="([^"]*)" class="cv-row-deploy( cv-row-deploy-failed)?">(.*?)</a>`)
	// clChipRe is a level chip.
	clChipRe = regexp.MustCompile(`<a href="([^"]*)" class="cv-lvlchip cv-lvl-(\w+)( cv-on)?" title="[^"]*">\w+(?: <span class="cv-lvlchip-n">(\d+)</span>)?`)
	// clRailCopyRe is a copy listed under the chosen service of the rail.
	clRailCopyRe = regexp.MustCompile(`<a href="([^"]*)" class="cv-lrailcopy( cv-on)?"(?: aria-current="true")?>(\w+)( <span class="cv-lgone">\(gone\)</span>)?</a>`)
	// clRailOnRe is the chosen item of the rail.
	clRailOnRe = regexp.MustCompile(`<a href="([^"]*)" class="cv-lrailitem cv-hue-\d cv-on" aria-current="true">`)
	// clBarRe is one of the histogram's 30 bars (not an error rect on top of one).
	clBarRe = regexp.MustCompile(`<rect class="cv-hbar(?:-empty|-unread)?" `)
	// clMarkRe is a deploy marker of the histogram.
	clMarkRe = regexp.MustCompile(`<line class="cv-hmark[^"]*"[^>]*><title>([^<]*)</title></line>`)
	// clRegionRe is the live region's opening tag.
	clRegionRe = regexp.MustCompile(`<div class="cv-lmain" id="logs-live"[^>]*>`)
	// clLinkRe is any link and its inner markup.
	clLinkRe = regexp.MustCompile(`(?s)<a href="([^"]*)"[^>]*>(.*?)</a>`)
	// clClockRe is a row's time: hours to milliseconds, "~" first when estimated.
	clClockRe = regexp.MustCompile(`^~?\d\d:\d\d:\d\d\.\d{3}$`)
)

// clRow is one line's row as the page draws it.
type clRow struct {
	Href, Anchor, Class string
	Estimated           bool
	ISO, Title, Clock   string
	Level               string
	Service, Copy, Text string
	Times               int
}

// clRowsOf reads the line rows of a page, in page order (newest first).
func clRowsOf(page string) []clRow {
	var rows []clRow
	for _, m := range clRowRe.FindAllStringSubmatch(page, -1) {
		r := clRow{
			Href: html.UnescapeString(m[1]), Anchor: m[2], Class: m[3], Estimated: m[4] != "",
			ISO: m[5], Title: html.UnescapeString(m[6]), Clock: html.UnescapeString(m[7]), Level: m[8],
			Service: html.UnescapeString(m[9]), Copy: m[10], Text: html.UnescapeString(m[11]),
		}
		r.Times, _ = strconv.Atoi(m[12])
		rows = append(rows, r)
	}
	return rows
}

// clLinkTo is the address of the first link whose text contains label, or "".
func clLinkTo(page, label string) string {
	for _, m := range clLinkRe.FindAllStringSubmatch(page, -1) {
		if strings.Contains(pageText(m[2]), label) {
			return html.UnescapeString(m[1])
		}
	}
	return ""
}

// clPath is a link's address without its fragment, for the client to follow.
func clPath(href string) string {
	path, _, _ := strings.Cut(href, "#")
	return path
}

// clLines is "1 line" or "12 lines", as the search field counts.
func clLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}

// clUnq is an address with its encoded commas written plainly, as the levels
// value reads.
func clUnq(href string) string { return strings.ReplaceAll(href, "%2C", ",") }

// consoleLogs: the Logs page against two busybox apps, cl024a (the sample lines
// every 2 s, a sentinel once) and cl024b (one INFO line every 5 s), deployed
// through the API (New app's form has no command field):
//
//  1. both healthy, and cl024a's lines in the API's read;
//  2. /logs with every level: the head, the range bar, the rail, the rows (level,
//     time with milliseconds, the JSON text, no escape, the fold, the estimated
//     time, the stderr warning), the chips' counts, the histogram's 30 bars and
//     labels, the footer, no style attribute and no inline script;
//  3. the search, a level alone, a service, a copy and a range, each keeping
//     the other filters in its links;
//  4. an open row: the strip, Raw, the first-seen note, no refresh;
//  5. the lines around one: the notice, the anchor, both streams, the way
//     back; a wrong at is a 400 page, a copy that isn't there a 404 page;
//  6. Live and Paused, the part, the live stream's headers;
//  7. Download: headers, first lines, oldest first, the sentinel only in the
//     whole file;
//  8. the app's Logs tab and the sidebar; the empty sentence;
//  9. a deploy during the step: its rows, links and markers, the old copy
//     "(gone)", and Show older;
//  10. the sentinel in no other app's page, no error page, not in the needs-you
//     list and not in Caravana's log; no escape byte in any page.
//
// Nomad unreachable isn't done in the lab (the lab's Nomad isn't stopped): the
// UI's tests cover its sentence with the service's fake.
func (c *console) consoleLogs(t *testing.T) {
	const (
		idA = "cl024a"
		idB = "cl024b"
		all = "levels=error,warn,info,debug"
	)
	c.ensureSignedIn(t)

	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	sentinel := "s3cret-sentinel-024-console-" + hex.EncodeToString(raw[:])
	hide := func(s string) string { return strings.ReplaceAll(s, sentinel, "<sentinel>") }
	errorf := func(format string, args ...any) {
		t.Helper()
		t.Error(hide(fmt.Sprintf(format, args...)))
	}
	fatalf := func(format string, args ...any) {
		t.Helper()
		t.Fatal(hide(fmt.Sprintf(format, args...)))
	}

	// Every page read, for the escape-byte and sentinel searches at the end.
	type seen struct {
		path   string
		status int
		body   string
	}
	var pages []seen
	page := func(path string, want int) string {
		t.Helper()
		resp, body := c.get(t, path)
		pages = append(pages, seen{path, resp.StatusCode, body})
		if resp.StatusCode != want {
			fatalf("GET %s = %d, want %d: %.600s", path, resp.StatusCode, want, pageText(body))
		}
		return body
	}

	for _, id := range []string{idA, idB} {
		c.removeLabApp(id) // a leftover from an aborted run, if any
	}
	for _, id := range []string{idA, idB} {
		waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	}
	t.Cleanup(func() {
		for _, id := range []string{idA, idB} {
			c.removeLabApp(id)
		}
	})
	t.Cleanup(func() { logTail(t, c.g.target) })

	// 1. The two apps.
	webA := map[string]any{
		"name": "web", "image": "busybox:1.37", "command": "sh",
		"args":      []string{"-c", consoleLogsScript},
		"env":       []map[string]string{{"name": "SENTINEL", "value": sentinel}},
		"ports":     []map[string]any{{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": labResources,
	}
	webB := map[string]any{
		"name": "web", "image": "busybox:1.37", "command": "sh",
		"args":      []string{"-c", consoleLogsQuiet},
		"ports":     []map[string]any{{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": labResources,
	}
	c.deployLabApp(t, idA, labApp(webA))
	c.deployLabApp(t, idB, labApp(webB))
	waitFor(t, "cl024a's sample lines in the logs", time.Minute, func() bool {
		status, body := c.g.send(t, http.MethodGet, "/v1/logs?app="+idA+"&"+all, "")
		return status == http.StatusOK && strings.Contains(string(body), "no time here") && strings.Contains(string(body), "[warn] careful") &&
			strings.Contains(string(body), "same same same")
	})
	waitFor(t, "cl024b's line in the logs", time.Minute, func() bool {
		status, body := c.g.send(t, http.MethodGet, "/v1/logs?app="+idB, "")
		return status == http.StatusOK && strings.Contains(string(body), "INFO quiet")
	})

	// 2. The page with every level.
	main := page("/logs?"+all, http.StatusOK)
	text := prose(main)
	if !strings.Contains(text, "Logs Output from every copy of every service, newest first. Deploys show up in the stream.") {
		errorf("/logs has no head sentence: %.300s", text)
	}
	if !strings.Contains(main, `class="cv-seg-item cv-on" aria-current="true">15 min</a>`) {
		errorf("/logs: \"15 min\" isn't the current range")
	}
	railM := regexp.MustCompile(`(?s)<nav class="cv-lograil" aria-label="Services">(.*?)</nav>`).FindStringSubmatch(main)
	if railM == nil {
		fatalf("/logs has no services rail")
	}
	rail := pageText(railM[1])
	if !strings.HasPrefix(rail, "Services all services ") || !strings.Contains(rail, "cl024a/ web 1") || !strings.Contains(rail, "cl024b/ web 1") {
		errorf("the rail reads %q, want \"all services\" first and cl024a and cl024b each with web and 1 copy", rail)
	}
	if !strings.Contains(main, "times in UTC") {
		errorf("the stream head doesn't say \"times in UTC\"")
	}
	rows := clRowsOf(main)
	if len(rows) == 0 {
		fatalf("/logs has no line rows: %.600s", text)
	}
	prev := ""
	for i, r := range rows {
		if !clClockRe.MatchString(r.Clock) {
			errorf("row %d's time is %q, want hours to milliseconds", i, r.Clock)
		}
		if r.Estimated {
			continue // placed by the read's rules, not ordered by value (see the gate step)
		}
		if prev != "" && r.ISO > prev {
			errorf("/logs isn't newest first: row %d (%s) is after the row before it (%s)", i, r.ISO, prev)
			break
		}
		prev = r.ISO
	}
	rowsWith := func(rows []clRow, match func(clRow) bool) []clRow {
		var out []clRow
		for _, r := range rows {
			if match(r) {
				out = append(out, r)
			}
		}
		return out
	}
	ofA := func(r clRow) bool { return r.Service == "cl024a/web" }
	need := func(what string, got []clRow) []clRow {
		t.Helper()
		if len(got) == 0 {
			fatalf("/logs has no row of %s", what)
		}
		return got
	}
	errRows := need("cl024a's ERROR boom", rowsWith(rows, func(r clRow) bool { return ofA(r) && strings.Contains(r.Text, "ERROR boom") }))
	for _, r := range errRows {
		if r.Level != "error" || !strings.Contains(r.Class, "cv-row-error") || r.Estimated {
			errorf("the ERROR row is level %s, class %q, estimated %v; want error, cv-row-error, its own time", r.Level, r.Class, r.Estimated)
		}
	}
	jsonRows := need("the JSON line", rowsWith(rows, func(r clRow) bool { return ofA(r) && strings.Contains(r.Text, "json boom") }))
	for _, r := range jsonRows {
		if r.Text != "json boom" || r.Level != "error" {
			errorf("the JSON row reads %q at level %s, want \"json boom\" at error", r.Text, r.Level)
		}
	}
	for _, r := range need("the colored line", rowsWith(rows, func(r clRow) bool { return ofA(r) && strings.Contains(r.Text, "red text") })) {
		if r.Text != "red text" || r.Level != "info" {
			errorf("the colored row reads %q at level %s, want \"red text\" at info", r.Text, r.Level)
		}
	}
	for _, r := range need("the repeated line", rowsWith(rows, func(r clRow) bool { return ofA(r) && strings.Contains(r.Text, "same same same") })) {
		if r.Times != 3 {
			errorf("the repeated row has ×%d, want ×3", r.Times)
		}
	}
	if !strings.Contains(main, `<span class="cv-repeat">×3</span>`) {
		errorf("/logs draws no \"×3\" chip")
	}
	for _, r := range need("the untimed line", rowsWith(rows, func(r clRow) bool { return ofA(r) && r.Text == "no time here" })) {
		if !r.Estimated || !strings.HasPrefix(r.Clock, "~") || r.Title != "estimated: this line carried no time; taken from the line before it" {
			errorf("the untimed row's time is %q (estimated %v, title %q), want a leading ~ and the estimated title", r.Clock, r.Estimated, r.Title)
		}
	}
	warnRows := need("the [warn] line", rowsWith(rows, func(r clRow) bool { return ofA(r) && strings.Contains(r.Text, "[warn] careful") }))
	for _, r := range warnRows {
		if r.Level != "warn" {
			errorf("the [warn] row is level %s, want warn", r.Level)
		}
	}
	need("the stderr tick", rowsWith(rows, func(r clRow) bool { return ofA(r) && strings.HasSuffix(r.Text, "tick") }))
	need("cl024b's line", rowsWith(rows, func(r clRow) bool { return r.Service == "cl024b/web" && strings.Contains(r.Text, "INFO quiet") }))
	chips := map[string][]string{}
	for _, m := range clChipRe.FindAllStringSubmatch(main, -1) {
		chips[m[2]] = m
	}
	if len(chips) != 4 {
		errorf("/logs has %d level chips, want 4", len(chips))
	}
	for _, level := range []string{"error", "warn"} {
		want := 0
		for _, r := range rows {
			if r.Level == level {
				want += max(1, r.Times)
			}
		}
		if m := chips[level]; m == nil || m[3] == "" || m[4] != strconv.Itoa(want) {
			errorf("the %s chip is %q, want on with the count %d", level, m, want)
		}
	}
	if bars := len(clBarRe.FindAllString(main, -1)); bars != 30 || !strings.Contains(main, `<svg class="cv-hist-svg"`) {
		errorf("the histogram has %d bars, want an SVG of 30", bars)
	}
	if !strings.Contains(main, `<span class="cv-hlabel-end">now</span>`) || !regexp.MustCompile(`<div class="cv-hlabels">\s*<span>\d\d:\d\d UTC</span>`).MatchString(main) {
		errorf("the histogram's label row isn't the range's start in UTC then \"now\"")
	}
	if !strings.Contains(text, "Kept as long as Nomad keeps them. Send them somewhere longer from Settings › Connections, later.") {
		errorf("/logs has no retention sentence")
	}
	if dl := clLinkTo(main, "Download"); clUnq(dl) != "/logs/download?"+all {
		errorf("Download links to %q, want /logs/download?%s", dl, all)
	}
	for _, path := range []string{"/logs?" + all, "/logs"} {
		_, body := c.get(t, path)
		if m := regexp.MustCompile(`\sstyle\s*=`).FindString(body); m != "" {
			errorf("%s has a style attribute (%q)", path, m)
		}
		for _, tag := range regexp.MustCompile(`<script(?:\s[^>]*)?>`).FindAllString(body, -1) {
			if !strings.Contains(tag, ` src="/static/js/`) {
				errorf("%s has an inline script: %s", path, tag)
			}
		}
	}

	// 3. Filters.
	boom := page("/logs?q=boom", http.StatusOK)
	boomRows := clRowsOf(boom)
	if len(boomRows) == 0 {
		errorf("q=boom found no row")
	}
	n := 0
	for _, r := range boomRows {
		n += max(1, r.Times)
		if r.Level != "error" || !strings.Contains(r.Text, "boom") {
			errorf("q=boom kept %q at %s", r.Text, r.Level)
		}
	}
	if !strings.Contains(boom, `<span class="cv-logsearch-n">`+clLines(n)+`</span>`) {
		errorf("q=boom doesn't say %q beside the search", clLines(n))
	}
	warn := page("/logs?levels=warn&app="+idA, http.StatusOK)
	warnOnly := clRowsOf(warn)
	if len(warnOnly) == 0 {
		errorf("levels=warn found no row")
	}
	for _, r := range warnOnly {
		if r.Level != "warn" || !strings.Contains(r.Text, "[warn] careful") {
			errorf("levels=warn kept %q at %s", r.Text, r.Level)
		}
	}
	for _, m := range clChipRe.FindAllStringSubmatch(warn, -1) {
		if on := m[3] != ""; on != (m[2] == "warn") {
			errorf("with levels=warn the %s chip is on=%v", m[2], on)
		}
		if m[2] == "error" && !strings.Contains(clUnq(html.UnescapeString(m[1])), "levels=error,warn") {
			errorf("the error chip's link %q doesn't turn error on beside warn", html.UnescapeString(m[1]))
		}
	}
	svcPath := "/logs?service=" + url.QueryEscape(idA+"/web") + "&" + all
	svc := page(svcPath, http.StatusOK)
	if m := clRailOnRe.FindStringSubmatch(svc); m == nil || !strings.Contains(html.UnescapeString(m[1]), "service=cl024a%2Fweb") {
		errorf("with service=cl024a/web the rail's chosen item is %q", m)
	}
	railCopies := clRailCopyRe.FindAllStringSubmatch(svc, -1)
	if len(railCopies) != 1 || railCopies[0][4] != "" {
		fatalf("the copies under cl024a/web are %q, want its one copy", railCopies)
	}
	short := railCopies[0][3]
	for _, r := range clRowsOf(svc) {
		if r.Service != "web" || r.Copy != short {
			errorf("with service=cl024a/web a row is %s copy %s", r.Service, r.Copy)
		}
	}
	cp := page(clPath(html.UnescapeString(railCopies[0][1])), http.StatusOK)
	if m := clRailCopyRe.FindStringSubmatch(cp); m == nil || m[2] == "" || m[3] != short {
		errorf("after choosing copy %s the rail's copy is %q, want it chosen", short, m)
	}
	if rs := clRowsOf(cp); len(rs) == 0 {
		errorf("copy=%s shows no row", short)
	}
	hour := page("/logs?range=1h", http.StatusOK)
	if !strings.Contains(hour, `class="cv-seg-item cv-on" aria-current="true">1 h</a>`) {
		errorf("range=1h: \"1 h\" isn't the current range")
	}
	both := page("/logs?q=boom&service="+url.QueryEscape(idA+"/web"), http.StatusOK)
	if m := regexp.MustCompile(`<a href="([^"]*)" class="cv-seg-item">1 h</a>`).FindStringSubmatch(both); m == nil {
		errorf("no 1 h range link")
	} else if link := html.UnescapeString(m[1]); !strings.Contains(link, "q=boom") || !strings.Contains(link, "service=cl024a%2Fweb") || !strings.Contains(link, "range=1h") {
		errorf("the 1 h link %q doesn't keep q and service", link)
	}

	// 4. The open row: the JSON line's, then an ERROR line's.
	opened := func(r clRow) (string, string) {
		t.Helper()
		if !strings.Contains(r.Href, "open=") || !strings.Contains(r.Href, "live=0") || !strings.HasSuffix(r.Href, "#"+r.Anchor) {
			errorf("a row links to %q, want ?…open=…&live=0#%s", r.Href, r.Anchor)
		}
		body := page(clPath(r.Href), http.StatusOK)
		strip := regexp.MustCompile(`(?s)<section class="cv-rowstrip" id="open-line" aria-label="The open line">(.*?)</section>`).FindStringSubmatch(body)
		if strip == nil {
			fatalf("the open row %s has no strip", r.Anchor)
		}
		openRow := false
		for _, o := range clRowsOf(body) {
			if o.Anchor == r.Anchor && strings.Contains(o.Class, "cv-row-open") {
				openRow = true
			}
		}
		if !openRow {
			errorf("row %s isn't drawn open", r.Anchor)
		}
		if m := clRegionRe.FindString(body); m == "" || strings.Contains(m, "hx-trigger") {
			errorf("with a row open the live region is %q, want no hx-trigger", m)
		}
		return body, strip[1]
	}
	jsonPage, jsonStrip := opened(jsonRows[0])
	stripText := prose(jsonStrip)
	for _, want := range []string{"On stdout of cl024a / web, copy " + short + ".", "Lines around this", "Only cl024a/web", "Close", "Raw"} {
		if !strings.Contains(stripText, want) {
			errorf("the JSON row's strip doesn't say %q: %s", want, stripText)
		}
	}
	if u := regexp.MustCompile(`<span class="cv-rowstrip-url">([^<]*)</span>`).FindStringSubmatch(jsonStrip); u == nil ||
		!strings.HasPrefix(html.UnescapeString(u[1]), c.g.target.API+"/logs?") || !strings.HasSuffix(u[1], "#"+jsonRows[0].Anchor) {
		errorf("the strip's address is %q, want this page's URL with #%s", u, jsonRows[0].Anchor)
	}
	if m := regexp.MustCompile(`(?s)<details class="cv-rowraw"><summary>Raw</summary><pre>(.*?)</pre></details>`).FindStringSubmatch(jsonStrip); m == nil ||
		!strings.Contains(html.UnescapeString(m[1]), `"msg":"json boom"`) || !strings.Contains(html.UnescapeString(m[1]), `"level":"error"`) {
		errorf("Raw doesn't hold the JSON as received: %q", m)
	}
	if only := clLinkTo(jsonPage, "Only cl024a/web"); !strings.Contains(only, "service=cl024a%2Fweb") {
		errorf("Only cl024a/web links to %q", only)
	}
	_, errStrip := opened(errRows[0])
	if s := prose(errStrip); !regexp.MustCompile(`Lines like this: first seen \d\d:\d\d UTC( today)?, \d+ times since\.`).MatchString(s) {
		errorf("the ERROR row's strip has no first-seen note: %s", s)
	}
	if strings.Contains(errStrip, "cv-rowraw") {
		errorf("the ERROR row's strip has Raw, but its line isn't JSON")
	}
	_, warnStrip := opened(warnRows[0])
	if s := prose(warnStrip); !strings.Contains(s, "On stderr of cl024a / web") {
		errorf("the [warn] row's strip doesn't name stderr: %s", s)
	}

	// 5. The lines around the ERROR line.
	aroundHref := regexp.MustCompile(`<a href="([^"]*)" class="cv-btn cv-btn-secondary cv-btn-sm">`).FindStringSubmatch(errStrip)
	if aroundHref == nil {
		fatalf("the strip has no Lines around this link")
	}
	aroundPath := clPath(html.UnescapeString(aroundHref[1]))
	if !strings.Contains(aroundPath, "at=") || !strings.Contains(aroundPath, "copy="+short) {
		errorf("Lines around this links to %q", aroundPath)
	}
	around := page(aroundPath, http.StatusOK)
	aText := prose(around)
	if !regexp.MustCompile(`Around \d\d:\d\d:\d\d UTC in copy ` + short + ` of cl024a/web\. Every level, this copy only\. Back to the stream`).MatchString(aText) {
		errorf("the around view's notice isn't the contract's sentence: %.400s", aText)
	}
	aRows := clRowsOf(around)
	anchors, ticks, warns := 0, 0, 0
	levels := map[string]bool{}
	for _, r := range aRows {
		if strings.Contains(r.Class, "cv-row-anchor") {
			anchors++
			if !strings.Contains(r.Text, "ERROR boom") {
				errorf("the anchor row reads %q, want the ERROR line", r.Text)
			}
		}
		if r.Copy != short {
			errorf("the around view has a row of copy %s", r.Copy)
		}
		if strings.HasSuffix(r.Text, "tick") {
			ticks++
		}
		if strings.Contains(r.Text, "[warn] careful") {
			warns++
		}
		levels[r.Level] = true
	}
	if anchors != 1 {
		errorf("the around view has %d anchor rows, want 1", anchors)
	}
	if ticks == 0 {
		errorf("the around view has no stderr tick line among its %d rows", len(aRows))
	}
	if warns == 0 {
		// The untimed stderr line is placed by its file's write bounds; it should sit in the window.
		errorf("FINDING: the around view has no untimed stderr \"[warn] careful\" line among its %d rows (%d ticks): %.1500s", len(aRows), ticks, aText)
	}
	if !levels["error"] || !levels["info"] || !levels["warn"] {
		errorf("the around view has levels %v, want every level", slices.Sorted(mapKeys(levels)))
	}
	if back := clLinkTo(around, "Back to the stream"); back == "" || strings.Contains(back, "at=") || strings.Contains(back, "copy=") {
		errorf("Back to the stream links to %q, want the stream without the copy", back)
	}
	if body := page("/logs?copy="+short+"&at=nonsense", http.StatusBadRequest); !strings.Contains(pageText(body), "That address isn't one caravana made.") {
		errorf("a wrong at isn't the bad-request page: %.300s", pageText(body))
	}
	if body := page("/logs?copy=deadbeef&at=deadbeef.stdout.0.0", http.StatusNotFound); !strings.Contains(pageText(body), "This copy's lines are gone.") {
		errorf("a copy that isn't there doesn't say its lines are gone: %.300s", pageText(body))
	}

	// 6. Live and Paused, the part and the stream.
	live := page("/logs", http.StatusOK)
	if m := regexp.MustCompile(`streaming from (\d+) cop(?:y|ies)`).FindStringSubmatch(live); m == nil {
		errorf("the live page's head doesn't say \"streaming from N copies\"")
	} else if k, _ := strconv.Atoi(m[1]); k < 2 {
		errorf("the live page streams from %d copies, want cl024a's and cl024b's at least", k)
	}
	if m := clRegionRe.FindString(live); !strings.Contains(m, `hx-trigger="sse:logs"`) || !strings.Contains(m, `hx-get="/parts/logs`) {
		errorf("the live page's region is %q, want hx-get /parts/logs on sse:logs", m)
	}
	if !strings.Contains(live, "<noscript><span>Following needs JavaScript. Reload the page to see newer lines.</span></noscript>") {
		errorf("the live page has no no-script sentence")
	}
	if !strings.Contains(html.UnescapeString(live), "watch=logs") {
		errorf("the live page opens no logs stream")
	}
	pausedPage := page("/logs?live=0", http.StatusOK)
	if m := clRegionRe.FindString(pausedPage); strings.Contains(m, "hx-trigger") {
		errorf("the paused page's region is %q, want no trigger", m)
	}
	if !strings.Contains(pausedPage, `<span>paused</span>`) || !strings.Contains(pausedPage, `aria-pressed="false">`) {
		errorf("the paused page doesn't say paused")
	}
	part := page("/parts/logs?app="+idA, http.StatusOK)
	if !strings.Contains(part, `class="cv-panel cv-hist"`) || len(clRowsOf(part)) == 0 || strings.Contains(part, "cv-rowstrip") ||
		strings.Contains(part, "cv-logfilter") || strings.Contains(part, "<html") {
		errorf("/parts/logs isn't the histogram and the rows alone: %.400s", pageText(part))
	}
	func() {
		ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.g.target.API+"/live?watch=logs&live=1&project=default&environment=production", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.stream.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			errorf("the logs stream = %d %q, want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}()

	// 7. Download (before the deploy below, whose new copy prints the sentinel again).
	download := func(path string) (string, [][]string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(c.ctx, 3*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.g.target.API+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.stream.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body := string(b)
		if resp.StatusCode != http.StatusOK {
			fatalf("GET %s = %d: %.300s", path, resp.StatusCode, pageText(body))
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			errorf("%s Content-Type = %q", path, ct)
		}
		if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=caravana-logs-default-production-") {
			errorf("%s Content-Disposition = %q", path, cd)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			errorf("%s Cache-Control = %q", path, cc)
		}
		for _, bad := range []string{"\x1b", "\x07", "\x00", "\r"} {
			if strings.Contains(body, bad) {
				errorf("%s holds %q", path, bad)
			}
		}
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		if len(lines) < 2 || lines[0] != "# newest last" || !strings.HasPrefix(lines[1], "# caravana logs ") {
			fatalf("%s starts %q, want \"# newest last\" then \"# caravana logs …\"", path, lines[:min(2, len(lines))])
		}
		var out [][]string
		last := ""
		for _, l := range lines[2:] {
			if strings.HasPrefix(l, "#") {
				continue
			}
			f := strings.Split(l, "\t")
			out = append(out, f)
			if len(f) != 6 {
				errorf("%s has a line of %d fields, want 6 tab-separated", path, len(f))
				continue
			}
			if strings.HasSuffix(f[0], "~") {
				continue // an estimated time
			}
			if f[0] < last {
				errorf("%s isn't oldest first: %s after %s", path, f[0], last)
				break
			}
			last = f[0]
		}
		return body, out
	}
	boomFile, boomLines := download("/logs/download?app=" + idA + "&q=boom")
	if len(boomLines) < 2 {
		errorf("the boom download has %d lines, want both error lines at least", len(boomLines))
	}
	for _, f := range boomLines {
		if len(f) == 6 && (f[4] != "error" || !strings.Contains(f[5], "boom") || f[2] != short) {
			errorf("the boom download has %q", f)
			break
		}
	}
	if strings.Contains(boomFile, sentinel) {
		errorf("the boom download holds the sentinel, which isn't a boom line")
	}
	wholeFile, _ := download("/logs/download?app=" + idA)
	if k := strings.Count(wholeFile, sentinel); k != 1 {
		errorf("the whole download holds the sentinel %d times, want once", k)
	}

	// 8. The app's tab, the sidebar and the empty sentence.
	tab := page("/apps/"+idA+"/logs", http.StatusOK)
	if allLogs := clLinkTo(tab, "All logs"); allLogs != "/logs?project=default&environment=production" {
		errorf("All logs links to %q", allLogs)
	}
	if m := regexp.MustCompile(`(?s)<nav class="cv-lograil" aria-label="Services">(.*?)</nav>`).FindStringSubmatch(tab); m == nil || pageText(m[1]) != "Services all services web 1" {
		errorf("the tab's rail reads %q, want web only", m)
	}
	if strings.Contains(tab, `name="stream"`) {
		errorf("the tab still has the Stream select")
	}
	if !strings.Contains(html.UnescapeString(tab), `hx-get="/parts/logs?app=cl024a"`) {
		errorf("the tab's region doesn't refresh from /parts/logs?app=cl024a")
	}
	for _, path := range []string{"/apps", "/logs", "/apps/" + idA + "/logs", "/domains"} {
		body := page(path, http.StatusOK)
		d, l := strings.Index(body, `<a href="/domains" class="cv-nav-item"`), strings.Index(body, `<a href="/logs" class="cv-nav-item"`)
		if d < 0 || l < d {
			errorf("%s's sidebar doesn't list Logs after Domains (at %d and %d)", path, d, l)
		}
	}
	empty := page("/logs?app="+idB+"&q=nothing-matches-this", http.StatusOK)
	if !strings.Contains(empty, `<p class="cv-lempty">No lines match. Try a wider time range or turn on more levels.</p>`) {
		errorf("a search that matches nothing doesn't say so: %.300s", pageText(empty))
	}

	// 9. A deploy during the step.
	webA["args"] = []string{"-c", ": v2; " + consoleLogsScript}
	rawD := c.g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+idA+"/deployments", labApp(webA))
	var second gateDeployment
	if err := json.Unmarshal(rawD, &second); err != nil || second.ID == "" || second.JobVersion == nil {
		fatalf("deploy %s: %s", idA, rawD)
	}
	version := "v" + strconv.FormatUint(*second.JobVersion, 10)
	deployHref := "/apps/" + idA + "/deployments#deployment-" + second.ID
	// The stream of the app with errors and warnings: the deploy's rows are drawn above the
	// oldest row shown, so a page of every level (500 rows in about two minutes) could leave
	// them out; these two levels reach back several minutes.
	deployRows := func() (started, isLive bool, marks []string) {
		body := page("/logs?app="+idA+"&levels=error,warn", http.StatusOK)
		for _, m := range clDeployRe.FindAllStringSubmatch(body, -1) {
			if html.UnescapeString(m[1]) != deployHref {
				continue
			}
			s := prose(m[3])
			started = started || strings.Contains(s, "cl024a "+version+" started rolling out")
			isLive = isLive || strings.Contains(s, "cl024a "+version+" is live · rollout finished")
		}
		for _, m := range clMarkRe.FindAllStringSubmatch(body, -1) {
			marks = append(marks, html.UnescapeString(m[1]))
		}
		return started, isLive, marks
	}
	waitFor(t, "the deploy's started row", time.Minute, func() bool { s, _, _ := deployRows(); return s })
	if final := c.g.waitFinalOf(t, idA, second.ID, 4*time.Minute); final.Status != "healthy" {
		fatalf("deploy %s ended %s: %s", idA, final.Status, final.Error)
	}
	var marks []string
	if !waitUntil(30*time.Second, func() bool {
		s, l, m := deployRows()
		marks = m
		return s && l && slices.Contains(m, version+" rolling out") && slices.Contains(m, version+" live")
	}) {
		errorf("after the deploy of %s the stream lacks its started or live row linking to %s, or the markers (%q)", version, deployHref, marks)
	}
	var gone []string
	if !waitUntil(30*time.Second, func() bool {
		body := page(svcPath, http.StatusOK)
		gone = nil
		for _, m := range clRailCopyRe.FindAllStringSubmatch(body, -1) {
			if m[4] != "" {
				gone = append(gone, m[3])
			}
		}
		return slices.Contains(gone, short)
	}) {
		errorf("the first copy %s isn't listed \"(gone)\" under cl024a/web 30 s after the deploy (gone: %q)", short, gone)
	}
	full := page("/logs?app="+idA+"&"+all, http.StatusOK)
	if older := clLinkTo(full, "Show older"); older == "" {
		if k := len(clRowsOf(full)); k >= 500 {
			errorf("a page of %d rows has no Show older", k)
		} else {
			t.Logf("Show older isn't drawn: the read holds %d rows, all of the range", k)
		}
	} else {
		if !strings.Contains(older, "before=") || !strings.Contains(older, "live=0") {
			errorf("Show older links to %q", older)
		}
		ol := page(clPath(older), http.StatusOK)
		if !regexp.MustCompile(`Lines before \d\d:\d\d UTC\. Back to the newest`).MatchString(prose(ol)) || len(clRowsOf(ol)) == 0 {
			errorf("Show older's page doesn't say which lines it shows, or has none: %.300s", prose(ol))
		}
	}

	// 10. The sentinel and the escape bytes.
	for _, path := range []string{"/apps/" + idB + "/logs", "/apps/whoami/logs", "/logs?app=" + idB + "&" + all, "/apps"} {
		_, body := c.get(t, path)
		pages = append(pages, seen{path, 0, body})
		if strings.Contains(body, sentinel) {
			errorf("%s holds cl024a's sentinel", path)
		}
	}
	if status, body := c.g.send(t, http.MethodGet, "/v1/attention", ""); status != http.StatusOK || strings.Contains(string(body), sentinel) {
		errorf("GET /v1/attention = %d, or it holds the sentinel", status)
	}
	for _, p := range pages {
		if p.status >= 400 && strings.Contains(p.body, sentinel) {
			errorf("the error page of %s holds the sentinel", p.path)
		}
		for _, bad := range []string{"\x1b", "\x07", "\x00"} {
			if strings.Contains(p.body, bad) {
				errorf("%s holds %q", p.path, bad)
			}
		}
	}
	if c.g.target.ServerLog != nil && strings.Contains(c.g.target.ServerLog(), sentinel) {
		errorf("Caravana's log holds the sentinel")
	}
	if found := page("/logs?app="+idA+"&q="+url.QueryEscape(sentinel), http.StatusOK); !strings.Contains(found, sentinel) {
		errorf("cl024a's own page doesn't find the sentinel it printed")
	}

	// cl024b leaves through its delete form; cl024a through the clean-up.
	if resp, body := c.post(t, "/apps/"+idB+"/delete", url.Values{"confirm": {idB}}); resp.StatusCode != http.StatusSeeOther {
		errorf("POST the delete of %s = %d: %s", idB, resp.StatusCode, pageText(body))
	}
	waitFor(t, idB+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(idB) })
}
