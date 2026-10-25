package main

// The logs gate step (feature 024, quickstart §6), through the API with the
// gate's token. It is in its own file only because gate_test.go is long;
// runGate lists it after domains.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// gateLogEntry is the part of a LogEntry (api/openapi.yaml) this step reads.
type gateLogEntry struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	TimeSource string    `json:"timeSource"`
	App        string    `json:"app"`
	Service    string    `json:"service"`
	Copy       string    `json:"copy"`
	Stream     string    `json:"stream"`
	Level      string    `json:"level"`
	Text       string    `json:"text"`
	Raw        string    `json:"raw"`
	Repeat     int       `json:"repeat"`
}

// gateLogRead is the part of a LogRead this step reads.
type gateLogRead struct {
	Lines   []gateLogEntry `json:"lines"`
	Deploys []struct {
		DeploymentID string `json:"deploymentId"`
		Kind         string `json:"kind"`
		JobVersion   int    `json:"jobVersion"`
		Who          string `json:"who"`
	} `json:"deploys"`
	Histogram struct {
		Buckets []struct {
			Count  int `json:"count"`
			Errors int `json:"errors"`
		} `json:"buckets"`
	} `json:"histogram"`
	Coverage struct {
		Complete bool `json:"complete"`
	} `json:"coverage"`
	Copies []struct {
		ID    string `json:"id"`
		Short string `json:"short"`
		App   string `json:"app"`
		Gone  bool   `json:"gone"`
		Lines int    `json:"lines"`
	} `json:"copies"`
	Dropped bool   `json:"dropped"`
	Anchor  string `json:"anchor"`
}

// logsScript is the service's command: the sample lines of the quickstart in a
// loop every 2 s, while busybox's httpd answers the health check. SENTINEL
// (the service's env) is printed once at the start.
const logsScript = `mkdir -p /www; echo ok > /www/index.html
echo "$SENTINEL"
( while true; do
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) ERROR boom"
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) tick" >&2
  echo '[warn] careful' >&2
  echo "{\"level\":\"error\",\"msg\":\"json boom\",\"time\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}"
  printf '\033[31mred\033[0m\n'
  printf '\033]8;;https://example.com\033\\link\033]8;;\033\\\n'
  printf 'progress 10%%\rprogress 100%%\n'
  echo 'same same same'; echo 'same same same'; echo 'same same same'
  echo 'no time here'
  sleep 2
done ) &
exec httpd -f -p 8080 -h /www`

// logsStep (feature 024): what the three log reads answer for a real copy.
// A busybox app gate024 whose service prints the sample lines (a timed ERROR
// line, a warning on stderr, a JSON error line, ANSI colour, an OSC 8 link, a
// carriage-return progress line, a line three times, an untimed line, and a
// sentinel once), then:
//
//  1. deploy, healthy, until listLogs shows a cycle;
//  2. listLogs: each sample line's level, time source, stream and stripped
//     text, the JSON line's raw, the fold, the copies, the histogram and the
//     coverage; q, levels, range and lines;
//  3. getLogsAround by id and by time; a wrong at is 422, a missing copy 404;
//  4. downloadLogs: headers, header lines, the lines oldest first;
//  5. the app's own getLogs: ids, levels, time sources, stripped text;
//  6. a deploy token is 403 token_scope on all three routes;
//  7. a deploy with a changed argument: the old copy is listed gone and the
//     deploys hold started then live for the new version;
//  8. a file the copy hasn't reached is 422 (a purged one, 404 log_rotated,
//     is out of the lab's reach: see the note in the code);
//  9. the sentinel appears only in gate024's own lines and download: in no
//     error answer, in no answer about another app, and not in Caravana's log.
//
// A failure message never holds the sentinel.
func (g *gate) logsStep(t *testing.T) {
	const id = "gate024"
	const place = "project=default&environment=production"

	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	sentinel := "s3cret-sentinel-024-gate-" + hex.EncodeToString(raw[:])
	hide := func(s string) string { return strings.ReplaceAll(s, sentinel, "<sentinel>") }
	fatalf := func(format string, args ...any) {
		t.Helper()
		t.Fatal(hide(fmt.Sprintf(format, args...)))
	}
	errorf := func(format string, args ...any) {
		t.Helper()
		t.Error(hide(fmt.Sprintf(format, args...)))
	}

	// Every answer with an error status, for the sentinel search at the end.
	var errorBodies []string
	get := func(path string) (int, []byte) {
		t.Helper()
		status, body := g.send(t, http.MethodGet, path, "")
		if status >= 400 {
			errorBodies = append(errorBodies, string(body))
		}
		return status, body
	}
	read := func(path string) (gateLogRead, []byte) {
		t.Helper()
		status, body := get(path)
		if status != http.StatusOK {
			fatalf("GET %s = %d, want 200: %.300s", path, status, body)
		}
		var r gateLogRead
		if err := json.Unmarshal(body, &r); err != nil {
			fatalf("GET %s: %v: %.300s", path, err, body)
		}
		checkClean(t, hide, path, body)
		return r, body
	}
	refused := func(status int, code, field, path string) {
		t.Helper()
		got, body := get(path)
		var e struct {
			Error struct{ Code, Field string } `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if got != status || e.Error.Code != code || (field != "" && e.Error.Field != field) {
			errorf("GET %s = %d %s (field %q), want %d %s (field %q)", path, got, e.Error.Code, e.Error.Field, status, code, field)
		}
	}

	tokenID := ""
	remove := func() {
		for _, app := range []string{id} {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+app, "", ""); err == nil {
				resp.Body.Close()
			}
		}
		if tokenID != "" {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/tokens/"+tokenID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // a leftover from an aborted run, if any
	for _, app := range []string{id} {
		waitFor(t, app+"'s Nomad job to be gone", time.Minute, func() bool {
			_, _, err := g.target.Nomad.Jobs().Info(app, nil)
			return err != nil && strings.Contains(err.Error(), "404")
		})
	}
	t.Cleanup(remove)

	// 1. Deploy; wait for the first cycle in the logs.
	web := map[string]any{
		"name": "web", "image": "busybox:1.37", "command": "sh",
		"args":      []string{"-c", logsScript},
		"env":       []map[string]string{{"name": "SENTINEL", "value": sentinel}},
		"ports":     []map[string]any{{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": labResources,
	}
	deploy := func() gateDeployment {
		t.Helper()
		status, body := g.send(t, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(web))
		var d gateDeployment
		_ = json.Unmarshal(body, &d)
		if status != http.StatusAccepted || d.ID == "" {
			fatalf("deployApp %s = %d: %.300s", id, status, body)
		}
		if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
			fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
		}
		return d
	}
	deploy()
	plain := "/v1/logs?" + place + "&app=" + id
	base := plain + "&levels=error,warn,info,debug"
	waitFor(t, "listLogs to show a cycle of the sample lines", time.Minute, func() bool {
		status, body := g.send(t, http.MethodGet, base, "")
		return status == http.StatusOK && strings.Contains(string(body), "no time here") && strings.Contains(string(body), "[warn] careful")
	})

	// 2. listLogs.
	list, _ := read(base)
	byText := func(r gateLogRead, part string) []gateLogEntry {
		var out []gateLogEntry
		for _, l := range r.Lines {
			if strings.Contains(l.Text, part) {
				out = append(out, l)
			}
		}
		return out
	}
	one := func(r gateLogRead, part string) []gateLogEntry {
		t.Helper()
		got := byText(r, part)
		if len(got) == 0 {
			fatalf("listLogs has no line containing %q (%d lines)", part, len(r.Lines))
		}
		return got
	}
	// Newest first among the lines that carry their own time. The estimated
	// ones are placed by the rules of research R2 (a stream with no timed line,
	// like this app's stderr, puts every line at the copy's start), which can
	// sit after a timed line's second: the report says so.
	var prevTime time.Time
	for i := 0; i < len(list.Lines); i++ {
		l := list.Lines[i]
		if l.TimeSource != "line" {
			continue
		}
		if !prevTime.IsZero() && l.Time.After(prevTime) {
			errorf("listLogs isn't newest first: timed line %d (%s) is after the timed line before it (%s)", i, l.Time, prevTime)
			break
		}
		prevTime = l.Time
	}
	expect := func(what string, got []gateLogEntry, level, stream, source string) {
		t.Helper()
		for _, l := range got {
			if l.Level != level || l.Stream != stream || (source != "" && l.TimeSource != source) || l.App != id || l.Service != "web" {
				errorf("%s: level %s, stream %s, time source %s, app %s/%s; want %s, %s, %s", what, l.Level, l.Stream, l.TimeSource, l.App, l.Service, level, stream, source)
			}
		}
	}
	var errorLine gateLogEntry
	var plainErrors []gateLogEntry
	for _, l := range one(list, "ERROR boom") {
		if l.Raw == "" {
			plainErrors = append(plainErrors, l)
		}
	}
	expect("the ERROR lines", plainErrors, "error", "stdout", "line")
	if len(plainErrors) > 0 {
		errorLine = plainErrors[len(plainErrors)/2] // not the first line of the copy: see around=<time> below
	}
	if errorLine.ID == "" {
		fatalf("listLogs has no ERROR line without raw")
	}
	if age := time.Since(errorLine.Time); age < -time.Minute || age > 5*time.Minute {
		errorf("the ERROR line's time %s isn't from the line's own timestamp (now %s)", errorLine.Time, time.Now().UTC())
	}
	expect("the warning", one(list, "[warn] careful"), "warn", "stderr", "")
	jsonLines := one(list, "json boom")
	expect("the JSON line", jsonLines, "error", "stdout", "line")
	for _, l := range jsonLines {
		if l.Text != "json boom" || !strings.Contains(l.Raw, `"msg":"json boom"`) || !strings.Contains(l.Raw, `"level":"error"`) {
			errorf("the JSON line has text %q and raw %q, want text \"json boom\" and the whole object as raw", l.Text, l.Raw)
		}
	}
	expect("the colored line", one(list, "red"), "info", "stdout", "")
	for _, l := range one(list, "red") {
		if l.Text == "red" {
			continue
		}
		if strings.Contains(l.Text, "[31m") {
			errorf("the colored line's text is %q, want the sequences removed", l.Text)
		}
	}
	progress := one(list, "progress 100%")
	expect("the progress line", progress, "info", "stdout", "")
	if !slices.ContainsFunc(one(list, "link"), func(l gateLogEntry) bool { return l.Text == "link" }) {
		errorf("no line has the OSC 8 link's text as just \"link\"")
	}
	same := one(list, "same same same")
	expect("the repeated line", same, "info", "stdout", "")
	for _, l := range same {
		if l.Text != "same same same" || l.Repeat != 2 {
			errorf("the repeated line is %q with repeat %d, want one entry, text \"same same same\", repeat 2", l.Text, l.Repeat)
		}
	}
	expect("the untimed line", one(list, "no time here"), "info", "stdout", "estimated")
	if sent := byText(list, sentinel); len(sent) > 0 {
		expect("the sentinel", sent, "info", "stdout", "")
	}
	if len(list.Copies) != 1 || list.Copies[0].Gone || list.Copies[0].App != id || list.Copies[0].Lines == 0 || len(list.Copies[0].Short) != 8 {
		errorf("copies = %+v, want the app's one copy, not gone, with lines", list.Copies)
	}
	if len(list.Histogram.Buckets) != 30 {
		errorf("the histogram has %d buckets, want 30", len(list.Histogram.Buckets))
	}
	count, errs := 0, 0
	for _, b := range list.Histogram.Buckets {
		count += b.Count
		errs += b.Errors
	}
	if count == 0 || errs == 0 {
		errorf("the histogram counts %d lines and %d errors, want both above 0", count, errs)
	}
	if !list.Coverage.Complete || list.Dropped {
		errorf("coverage.complete %v, dropped %v, want true and false for a fresh app", list.Coverage.Complete, list.Dropped)
	}
	if len(list.Copies) != 1 {
		t.FailNow()
	}
	short := list.Copies[0].Short

	if r, _ := read(base + "&q=boom"); len(r.Lines) == 0 {
		errorf("q=boom found nothing")
	} else {
		kinds := map[bool]bool{}
		for _, l := range r.Lines {
			if !strings.Contains(strings.ToLower(l.Text+l.Raw), "boom") || l.Level != "error" {
				errorf("q=boom kept %q (%s)", l.Text, l.Level)
			}
			kinds[l.Raw != ""] = true
		}
		if len(kinds) != 2 {
			errorf("q=boom kept lines with raw %v, want the plain ERROR line and the JSON line", kinds)
		}
	}
	if r, _ := read(plain + "&levels=warn"); len(r.Lines) == 0 {
		errorf("levels=warn found nothing")
	} else {
		for _, l := range r.Lines {
			if l.Level != "warn" || !strings.Contains(l.Text, "[warn] careful") {
				errorf("levels=warn kept %q (%s)", l.Text, l.Level)
			}
		}
	}
	if r, _ := read(base + "&range=1h"); len(r.Lines) == 0 {
		errorf("range=1h found nothing")
	}
	if r, _ := read(base + "&lines=2"); len(r.Lines) != 2 {
		errorf("lines=2 gave %d entries", len(r.Lines))
	}
	refused(http.StatusUnprocessableEntity, "invalid_field", "levels", plain+"&levels=loud")
	refused(http.StatusUnprocessableEntity, "invalid_field", "range", plain+"&range=2h")

	// 3. getLogsAround.
	around := "/v1/logs/around?copy=" + short
	r, _ := read(around + "&at=" + errorLine.ID + "&lines=3")
	if r.Anchor != errorLine.ID {
		errorf("anchor = %q, want %q", r.Anchor, errorLine.ID)
	}
	streams := map[string]bool{}
	anchorSeen := false
	for _, l := range r.Lines {
		streams[l.Stream] = true
		anchorSeen = anchorSeen || l.ID == errorLine.ID
		if l.Copy != short || l.App != id {
			errorf("getLogsAround has a line of copy %s app %s, want copy %s only", l.Copy, l.App, short)
		}
	}
	if !anchorSeen {
		errorf("getLogsAround doesn't hold the anchor line %s", errorLine.ID)
	}
	if !streams["stdout"] || !streams["stderr"] {
		errorf("getLogsAround has streams %v, want both", slices.Sorted(mapKeys(streams)))
	}
	levelsSeen := map[string]bool{}
	for _, l := range r.Lines {
		levelsSeen[l.Level] = true
	}
	if !levelsSeen["info"] || !levelsSeen["error"] {
		errorf("getLogsAround has levels %v, want every level (info and error among them)", slices.Sorted(mapKeys(levelsSeen)))
	}
	r, _ = read(around + "&around=" + url.QueryEscape(errorLine.Time.UTC().Format(time.RFC3339)) + "&stream=stdout&lines=3")
	if r.Anchor == "" {
		errorf("around=<time> gave no anchor")
	} else if i := slices.IndexFunc(r.Lines, func(l gateLogEntry) bool { return l.ID == r.Anchor }); i < 0 {
		errorf("around=<time>: the anchor %s isn't among the lines", r.Anchor)
	} else if l := r.Lines[i]; l.TimeSource != "line" || l.Time.Before(errorLine.Time.Truncate(time.Second)) {
		errorf("around=<time>: the anchor line is %s at %s, want a timed line at or after %s", l.TimeSource, l.Time, errorLine.Time.Truncate(time.Second))
	}
	refused(http.StatusUnprocessableEntity, "invalid_field", "at", around+"&at=x.stdout.0.0")
	refused(http.StatusUnprocessableEntity, "invalid_field", "at", around+"&at="+short+".stdout.0.999999999")
	// A file index the copy hasn't reached yet is the caller's mistake, not a
	// purged file: Nomad only purges older ones (step 8 makes a real one).
	refused(http.StatusUnprocessableEntity, "invalid_field", "at", around+"&at="+short+".stdout.9.0")
	refused(http.StatusNotFound, "not_found", "", "/v1/logs/around?copy=deadbeef&at=deadbeef.stdout.0.0")

	// 4. downloadLogs.
	dl := g.logsDownload(t, hide, "/v1/logs/download?"+place+"&app="+id+"&levels=error,warn,info,debug&q=boom")
	if len(dl.lines) < 2 {
		errorf("the download has %d lines, want both error lines at least", len(dl.lines))
	}
	for _, f := range dl.lines {
		if len(f) != 6 || f[2] != short || !strings.Contains(strings.ToLower(f[5]), "boom") {
			errorf("a download line has %d fields (%q), want time, app/service, copy, stream, level, text of copy %s with boom", len(f), f, short)
			break
		}
	}
	whole := g.logsDownload(t, hide, "/v1/logs/download?"+place+"&app="+id+"&levels=error,warn,info,debug")
	if !strings.Contains(whole.body, sentinel) {
		errorf("the whole download doesn't hold the sentinel the copy printed at its start")
	}

	// 5. The app's own getLogs.
	status, body := get("/v1/apps/" + id + "/logs?lines=100")
	if status != http.StatusOK {
		fatalf("getLogs = %d: %.300s", status, body)
	}
	checkClean(t, hide, "getLogs", body)
	var appLogs struct {
		Lines []struct {
			ID         string     `json:"id"`
			Level      string     `json:"level"`
			TimeSource string     `json:"timeSource"`
			Text       string     `json:"text"`
			Time       *time.Time `json:"time"`
			Stream     string     `json:"stream"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(body, &appLogs); err != nil || len(appLogs.Lines) == 0 {
		fatalf("getLogs: %v, %d lines", err, len(appLogs.Lines))
	}
	// Oldest first within each stream (getLogs reads the streams one after the other).
	prev := map[string]time.Time{}
	for i, l := range appLogs.Lines {
		if l.ID == "" || l.Level == "" || l.TimeSource == "" {
			errorf("getLogs line %d (%q) has id %q, level %q, timeSource %q, want all three", i, l.Text, l.ID, l.Level, l.TimeSource)
			break
		}
		if l.Time != nil && l.TimeSource == "line" { // estimated times: see the note at listLogs
			if l.Time.Before(prev[l.Stream]) {
				errorf("getLogs isn't oldest first in %s at timed line %d (%s after %s)", l.Stream, i, l.Time, prev[l.Stream])
				break
			}
			prev[l.Stream] = *l.Time
		}
	}

	// 6. A deploy token: 403 token_scope on the three routes.
	raw2 := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"gate024-ci","scope":{"project":"default","app":"`+id+`"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(raw2, &made); err != nil || made.Token == "" {
		fatalf("createToken with a scope: %v (token made: %v)", err, made.Token != "")
	}
	tokenID = made.ID
	for _, path := range []string{base, around + "&at=" + errorLine.ID, "/v1/logs/download?" + place + "&app=" + id} {
		status, body := g.sendAs(t, made.Token, http.MethodGet, path, "")
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if status != http.StatusForbidden || e.Error.Code != "token_scope" {
			errorf("a deploy token on GET %s = %d %s, want 403 token_scope", strings.SplitN(path, "?", 2)[0], status, e.Error.Code)
		}
		errorBodies = append(errorBodies, string(body))
	}

	// 7. A deploy with a changed argument; the old copy is listed gone.
	web["args"] = []string{"-c", ": v2; " + logsScript}
	second := deploy()
	if second.JobVersion == nil {
		fatalf("the second deploy answered no job version")
	}
	var after gateLogRead
	if !waitUntil(30*time.Second, func() bool {
		status, body := g.send(t, http.MethodGet, base, "")
		if status != http.StatusOK {
			return false
		}
		after = gateLogRead{}
		_ = json.Unmarshal(body, &after)
		for _, c := range after.Copies {
			if c.Short == short && c.Gone {
				return true
			}
		}
		return false
	}) {
		fatalf("the first copy %s isn't listed as gone 30 s after the redeploy (copies %+v)", short, after.Copies)
	}
	var kinds []string
	for _, d := range after.Deploys {
		if d.JobVersion == int(*second.JobVersion) && (d.Kind == "started" || d.Kind == "live") {
			kinds = append(kinds, d.Kind)
			if d.Who == "" {
				errorf("the %s row of v%d has no who", d.Kind, d.JobVersion)
			}
		}
	}
	if !slices.Equal(kinds, []string{"started", "live"}) {
		errorf("deploys of v%d are %v (all: %+v), want started then live", *second.JobVersion, kinds, after.Deploys)
	}

	// 8. A purged file (404 log_rotated) isn't asserted here: Nomad purges the
	// oldest of its 10 files of 10 MiB as the eleventh is made, and the lab
	// moved only 0.03 to 0.3 MiB/s of output through them (three attempts with a
	// 125 MB writer, 2026-10-24: 94, 36 and 29 MiB in 10 to 20 minutes). The
	// neighbouring case above (a file the copy hasn't reached is 422, not a
	// purge) is asserted, and the purge is the service tests' (ErrLogRotated).

	// 9. The sentinel only where it was printed.
	for _, other := range []string{"whoami"} { // another app of the lab, if the earlier steps left it
		status, body := get("/v1/logs?" + place + "&app=" + other + "&levels=error,warn,info,debug&lines=1000")
		if status == http.StatusOK && strings.Contains(string(body), sentinel) {
			errorf("listLogs of %s holds gate024's sentinel", other)
		}
	}
	for i, b := range errorBodies {
		if strings.Contains(b, sentinel) {
			errorf("error answer %d holds the sentinel", i)
		}
	}
	if g.target.ServerLog != nil && strings.Contains(g.target.ServerLog(), sentinel) {
		errorf("Caravana's log holds the sentinel")
	}
	if sent, _ := read(base + "&lines=1000&q=" + sentinel); len(sent.Lines) == 0 {
		errorf("gate024's own listLogs doesn't find the sentinel it printed")
	}
}

// logsDownload is a download answered 200 with the headers the contract
// names, split into its comment lines and its tab-separated lines.
type logsDownloaded struct {
	body  string
	lines [][]string
}

// logsDownload reads a download and checks what every one must have: the
// headers, the first two lines, no escape byte, and the lines oldest first.
func (g *gate) logsDownload(t *testing.T, hide func(string) string, path string) logsDownloaded {
	t.Helper()
	resp := g.api(t, http.MethodGet, path, "", "")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(hide(fmt.Sprintf("downloadLogs = %d: %.300s", resp.StatusCode, body)))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("download Content-Type = %q", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment; filename=") || !strings.Contains(cd, "caravana-logs-") {
		t.Errorf("download Content-Disposition = %q, want attachment; filename=caravana-logs-…", cd)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("download Cache-Control = %q", cc)
	}
	for _, bad := range []string{"\x1b", "\x07", "\x00", "\r"} {
		if strings.Contains(body, bad) {
			t.Errorf("the download holds %q", bad)
		}
	}
	rows := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(rows) < 2 || rows[0] != "# newest last" || !strings.HasPrefix(rows[1], "# caravana logs ") {
		t.Fatal(hide(fmt.Sprintf("the download starts %q, want \"# newest last\" then \"# caravana logs …\"", rows[:min(2, len(rows))])))
	}
	out := logsDownloaded{body: body}
	last := ""
	for _, row := range rows[2:] {
		if strings.HasPrefix(row, "#") {
			continue
		}
		f := strings.Split(row, "\t")
		out.lines = append(out.lines, f)
		if strings.HasSuffix(f[0], "~") {
			continue // an estimated time is placed by the read's rules, not ordered by value
		}
		stamp := f[0]
		if stamp < last {
			t.Error(hide(fmt.Sprintf("the download isn't oldest first: %s after %s", stamp, last)))
			break
		}
		last = stamp
	}
	return out
}

// checkClean fails the step when a JSON answer holds a control byte, written
// the way JSON writes it: escapes are removed from the text of every line.
func checkClean(t *testing.T, hide func(string) string, path string, body []byte) {
	t.Helper()
	for _, bad := range []string{`\u001b`, `\u0007`, `\u0000`, `\r`} {
		if strings.Contains(string(body), bad) {
			t.Error(hide(fmt.Sprintf("GET %s holds %s", strings.SplitN(path, "?", 2)[0], bad)))
		}
	}
}
