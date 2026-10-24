package main

// The console-schedules step of the lab test (feature 022, plan.md "Lab"):
// Schedules through the console's forms, without JavaScript, against real
// Nomad. It is in its own file only because console_lab_test.go is long;
// runConsole lists it after console-backup-daily.

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// schedMessage is the part of a webhook message about a schedule's problem
// that this step reads.
type schedMessage struct {
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	App      string `json:"app"`
	Schedule string `json:"schedule"`
	ExitCode *int   `json:"exitCode"`
	Title    string `json:"title"`
}

// liveTap reads a live stream whole, every line, so a step can check what the
// notices carry and not only their names.
type liveTap struct {
	mu    sync.Mutex
	lines []string
}

func (l *liveTap) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// tapLive opens a live stream of the console as the signed-in browser and
// keeps every line it sends until the console's context ends.
func (c *console) tapLive(t *testing.T, path string) *liveTap {
	t.Helper()
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, c.g.target.API+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
	}
	tap := &liveTap{}
	go func() {
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			tap.mu.Lock()
			tap.lines = append(tap.lines, scanner.Text())
			tap.mu.Unlock()
		}
	}()
	return tap
}

// scheduleRow is the markup of one schedule's row on /schedules: from its
// <details> to the next row's (a row holds <details> of its own, for Stop and
// Delete, so a pattern can't find its end).
func scheduleRow(page, schedID string) string {
	start := strings.Index(page, `<details class="cv-sdetails" id="schedule-`+schedID+`"`)
	if start < 0 {
		return ""
	}
	rest := page[start+1:]
	end := len(rest)
	for _, next := range []string{`<details class="cv-sdetails"`, `<div class="cv-sfoot">`} {
		if i := strings.Index(rest, next); i >= 0 && i < end {
			end = i
		}
	}
	return page[start : start+1+end]
}

// newSchedule makes a schedule through the New schedule form: the page for the
// app, Check (the preview shows wantCron in UTC), then Create, which lands on
// the schedule's row. It returns the schedule's ID.
func (c *console) newSchedule(t *testing.T, appID, name, command, when, wantCron string) string {
	t.Helper()
	resp, page := c.get(t, "/schedules/new?app="+appID)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `<option value="`+appID+`" selected>`) ||
		!strings.Contains(page, `<form method="post" action="/schedules"`) {
		t.Fatalf("GET /schedules/new?app=%s = %d, want the form with the app picked:\n%s", appID, resp.StatusCode, pageText(page))
	}
	form := url.Values{"name": {name}, "app": {appID}, "command": {command}, "when": {when},
		"stopAfter": {"300"}, "memory": {"256"}, "skipChoice": {"1"}, "skip": {"1"}, "action": {"check"}}
	resp, page = c.post(t, "/schedules", form)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `<dd class="cv-spreview-cron">`+wantCron+`</dd>`) ||
		!strings.Contains(pageText(page), "timezone UTC") {
		t.Fatalf("Check %q = %d, want the preview with %s in UTC:\n%s", when, resp.StatusCode, wantCron, pageText(page))
	}
	form.Set("action", "create")
	resp, page = c.post(t, "/schedules", form)
	loc := resp.Header.Get("Location")
	m := regexp.MustCompile(`^/schedules\?created=([0-9a-f]+)#schedule-([0-9a-f]+)$`).FindStringSubmatch(loc)
	if resp.StatusCode != http.StatusSeeOther || m == nil || m[1] != m[2] {
		t.Fatalf("Create %s = %d → %q, want 303 to its row: %s", name, resp.StatusCode, loc, pageText(page))
	}
	id := m[1]
	resp, page = c.get(t, loc)
	if row := scheduleRow(page, id); resp.StatusCode != http.StatusOK || row == "" || !strings.Contains(pageText(page), name+" was created.") {
		t.Errorf("GET %s = %d, want the notice and %s's row:\n%s", loc, resp.StatusCode, name, pageText(page))
	}
	return id
}

// runScheduleNow presses Run now on the schedule's row and returns the run it
// started, read through the API.
func (c *console) runScheduleNow(t *testing.T, schedID string) gateScheduleRun {
	t.Helper()
	before := c.g.scheduleRuns(t, schedID)
	resp, page := c.post(t, "/schedules/"+schedID+"/run", url.Values{})
	if want := "/schedules?open=" + schedID + "#schedule-" + schedID; resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
		t.Fatalf("POST Run now = %d → %q, want 303 to %s: %s", resp.StatusCode, resp.Header.Get("Location"), want, pageText(page))
	}
	runs := c.g.scheduleRuns(t, schedID)
	if len(runs) != len(before)+1 || runs[0].Trigger != "owner" {
		t.Fatalf("the runs after Run now = %+v, want one more, by the owner", runs)
	}
	return runs[0]
}

// deleteSchedule deletes a schedule through the form in its row's Delete…
// confirmation.
func (c *console) deleteSchedule(t *testing.T, schedID, name string) {
	t.Helper()
	_, page := c.get(t, "/schedules?open="+schedID)
	if !strings.Contains(scheduleRow(page, schedID), `action="/schedules/`+schedID+`/delete"`) {
		t.Fatalf("%s's row has no Delete form:\n%s", name, pageText(scheduleRow(page, schedID)))
	}
	resp, page := c.post(t, "/schedules/"+schedID+"/delete", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/schedules?deleted="+name {
		t.Fatalf("POST delete %s = %d → %q: %s", name, resp.StatusCode, resp.Header.Get("Location"), pageText(page))
	}
	if _, page = c.get(t, "/schedules?deleted="+name); !strings.Contains(pageText(page), name+" was deleted, with its runs.") {
		t.Errorf("the page after deleting %s lacks the notice: %s", name, pageText(page))
	}
	c.g.refusal(t, http.StatusNotFound, "schedule_not_found", "", http.MethodGet, "/v1/schedules/"+schedID, "")
}

// schedules: Schedules through the console's forms (no JavaScript), on a busybox
// app with the secret TOKEN and a second app whose volume is backed up daily.
// A webhook channel subscribed to problems and a tap on the live stream are set
// up first.
//
//  1. hello (every day at 03:00, Check shows the cron in UTC) is created and
//     Run now: the run ends ok, its row says so, and its page shows what it
//     printed (a sentinel its command doesn't hold).
//  2. boom exits 3: failed, "Exited with code 3." on its row and its page, a
//     needs-you item on /apps and in the API, one problem.opened
//     (schedule_failed, exit code 3) at the receiver.
//  3. tick ("every minute") runs on its own, by the timer, within about 70 s
//     (the lab's clock looks every 5 s); it is deleted at once after.
//  4. boom is fixed through Edit and Run now: ok, the item goes, and
//     problem.resolved arrives after the resolve delay.
//  5. slow (sleep 180) is stopped at the lab's limit (stopped_at_limit,
//     "Stopped at the … limit." in its row), then run again and stopped with
//     Stop this run (stopped, and no item for it).
//  6. Pause and Resume of hello: "paused" and no next run, then a next run.
//  7. show-token echoes $TOKEN: its page shows [secret TOKEN], never the value.
//  8. The app's Configure panel lists hello.
//  9. Neither the printed sentinel nor the secret value is in any list, item,
//     delivery, log line or notice; the secret value isn't in the SQLite files.
//  10. Every schedule is deleted through its confirmation, then the app.
//  11. A volume app's "Back up daily" (Backups tab) puts a built-in row on
//     /schedules: "Change in Backups", no Run now, Pause, Edit or Delete. It
//     is deployed only now, after the first app is gone (the lab's 2 GB hold
//     few copies at once), and deleted.
//
// Budget: about 5 minutes, most of it the notifier's 30 s checks.
func (c *console) schedules(t *testing.T) {
	const (
		id, volID = "con022", "con022vol"
		oneGap    = 30 * time.Second // the notifier's check interval
	)
	printed := "hello-sentinel-022-" + c.g.run
	secretValue := "s3cret-sentinel-022-value-" + c.g.run
	for _, a := range []string{id, volID} {
		c.removeLabApp(a) // a leftover from an aborted run, if any
	}
	waitFor(t, "the step's Nomad jobs to be gone", time.Minute, func() bool { return c.jobAbsent(id) && c.jobAbsent(volID) })
	t.Cleanup(func() { c.removeLabApp(id); c.removeLabApp(volID) })
	t.Cleanup(func() { logTail(t, c.g.target) })

	// A webhook channel for problems, through the form, as console-notifications adds one.
	rx := &notifyReceiver{}
	srv := httptest.NewServer(rx)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		if t.Failed() {
			for _, r := range rx.all() {
				t.Logf("the receiver got: %s", r.Body)
			}
		}
	})
	resp, body := c.post(t, "/settings/notifications", url.Values{"kind": {"webhook"}, "name": {"schedules receiver"},
		"url": {srv.URL}, "events": {"problems"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /settings/notifications = %d: %s", resp.StatusCode, pageText(body))
	}
	var channels []struct{ ID, Name string }
	c.g.getJSON(t, "/v1/notifications/channels", &channels)
	channelID := ""
	for _, ch := range channels {
		if ch.Name == "schedules receiver" {
			channelID = ch.ID
		}
	}
	if channelID == "" {
		t.Fatalf("the channel isn't listed: %+v", channels)
	}
	t.Cleanup(func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/notifications/channels/"+channelID, "", ""); err == nil {
			resp.Body.Close()
		}
	})
	scheduleMessages := func(typ, name string) int {
		n := 0
		for _, r := range rx.all() {
			var m schedMessage
			if json.Unmarshal(r.Body, &m) != nil || m.Type != typ || m.Kind != "schedule_failed" || m.App != id {
				continue
			}
			// problem.resolved doesn't carry the schedule's name, only the
			// title that was sent ("Resolved: con022: boom failed (exit 3)").
			if m.Schedule == name || (typ == "problem.resolved" && strings.Contains(m.Title, ": "+name+" failed")) {
				n++
			}
		}
		return n
	}
	live := c.tapLive(t, "/live")

	// The app, through the API, with the secret. The volume app comes last,
	// once this one is gone: the lab's 2 GB hold few copies at once.
	web := actionsWeb("", 1)
	web["secrets"] = []map[string]any{{"name": "TOKEN", "value": secretValue}}
	c.deployLabApp(t, id, labApp(web))

	// 1. hello: created from words, Run now, ok.
	hello := c.newSchedule(t, id, "hello", "sh -c 'echo hello-sentinel-0$((20+2))-"+c.g.run+"; exit 0'", "every day at 03:00", "0 3 * * *")
	run := c.runScheduleNow(t, hello)
	if ended := c.g.waitRunStatus(t, hello, run.ID, 2*time.Minute, "ok"); ended.ExitCode == nil || *ended.ExitCode != 0 {
		t.Errorf("hello's run = %+v, want ok with exit code 0", ended)
	}
	_, page := c.get(t, "/schedules")
	if row := pageText(scheduleRow(page, hello)); !strings.Contains(row, "The last run: finished in") {
		t.Errorf("hello's row after an ok run = %s, want it to say the last run finished", row)
	}
	runPage := "/schedules/" + hello + "/runs/" + run.ID
	resp, page = c.get(t, runPage)
	if got := pageText(page); resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" ||
		!strings.Contains(got, printed) || !strings.Contains(got, "Finished in") {
		t.Errorf("GET %s = %d (Cache-Control %q), want no-store with the output and \"Finished in\":\n%s", runPage, resp.StatusCode, resp.Header.Get("Cache-Control"), got)
	}

	// 2. boom: failed, an item, and one problem.opened.
	boom := c.newSchedule(t, id, "boom", "sh -c 'echo boom; exit 3'", "every day at 04:00", "0 4 * * *")
	run = c.runScheduleNow(t, boom)
	if ended := c.g.waitRunStatus(t, boom, run.ID, 2*time.Minute, "failed"); ended.ExitCode == nil || *ended.ExitCode != 3 || ended.Error != "exit 3" {
		t.Errorf("boom's run = %+v, want failed, exit code 3, error \"exit 3\"", ended)
	}
	tickCreated := time.Now()
	tick := c.newSchedule(t, id, "tick", "sh -c 'echo tick'", "every minute", "* * * * *")
	_, page = c.get(t, "/schedules")
	if row := pageText(scheduleRow(page, boom)); !strings.Contains(row, "Exited with code 3.") {
		t.Errorf("boom's row = %s, want \"Exited with code 3.\"", row)
	}
	if _, page = c.get(t, "/schedules/"+boom+"/runs/"+run.ID); !strings.Contains(pageText(page), "Exited with code 3.") || !strings.Contains(pageText(page), "boom") {
		t.Errorf("boom's run page = %s, want \"Exited with code 3.\" and its output", pageText(page))
	}
	items, _ := c.attention(t)
	if it, ok := attentionOf(items, "schedule_failed", id); !ok || !strings.Contains(it.Detail, "Exited with code 3.") {
		t.Errorf("GET /v1/attention = %+v, want a schedule_failed item for %s saying it exited with code 3", items, id)
	}
	if _, page = c.get(t, "/apps"); !strings.Contains(pageText(page), "the last run of boom failed. Exited with code 3.") {
		t.Errorf("/apps doesn't name boom in the needs-you list:\n%s", pageText(page))
	}
	waitFor(t, "problem.opened for boom", 2*oneGap+15*time.Second, func() bool { return scheduleMessages("problem.opened", "boom") > 0 })
	for _, r := range rx.all() {
		var m schedMessage
		if json.Unmarshal(r.Body, &m) == nil && m.Type == "problem.opened" && m.Schedule == "boom" && (m.ExitCode == nil || *m.ExitCode != 3) {
			t.Errorf("problem.opened for boom = %s, want exitCode 3", r.Body)
		}
	}

	// 3. tick runs on its own, then goes.
	waitFor(t, "tick to run by the timer", 100*time.Second-time.Since(tickCreated), func() bool {
		for _, r := range c.g.scheduleRuns(t, tick) {
			if r.Trigger == "timer" && r.Status == "ok" {
				return true
			}
		}
		return false
	})
	t.Logf("tick ran on its own and ended ok %s after it was created", time.Since(tickCreated).Round(time.Second))
	c.deleteSchedule(t, tick, "tick")

	// 4. boom fixed through Edit: ok, the item goes, problem.resolved follows.
	resp, page = c.get(t, "/schedules/"+boom+"/edit")
	edit := formValuesAction(t, page, "/schedules/"+boom)
	if resp.StatusCode != http.StatusOK || edit.Get("command") != "sh -c 'echo boom; exit 3'" || edit.Get("name") != "boom" {
		t.Fatalf("GET boom's Edit = %d with %v, want its name and command:\n%s", resp.StatusCode, edit, pageText(page))
	}
	edit.Set("command", "sh -c 'echo boom; exit 0'")
	edit.Set("stopAfter", "300")
	edit.Set("memory", "256")
	edit.Set("action", "save")
	resp, page = c.post(t, "/schedules/"+boom, edit)
	if want := "/schedules?saved=" + boom + "#schedule-" + boom; resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
		t.Fatalf("POST boom's Edit = %d → %q, want 303 to %s: %s", resp.StatusCode, resp.Header.Get("Location"), want, pageText(page))
	}
	if sc, _ := c.g.schedule(t, boom); strings.Join(sc.Command, "|") != "sh|-c|echo boom; exit 0" || sc.Cron != "0 4 * * *" {
		t.Errorf("boom after Edit = %+v, want the new command and the same cron", sc)
	}
	run = c.runScheduleNow(t, boom)
	c.g.waitRunStatus(t, boom, run.ID, 2*time.Minute, "ok")
	if items, _ := c.attention(t); func() bool { _, ok := attentionOf(items, "schedule_failed", id); return ok }() {
		t.Errorf("GET /v1/attention after boom's ok run = %+v, want no schedule_failed item", items)
	}

	// 5. slow: stopped at the lab's limit, then by Stop this run. Its first
	// run goes while problem.resolved for boom is awaited.
	slow := c.newSchedule(t, id, "slow", "sleep 180", "every day at 05:00", "0 5 * * *")
	slowRun := c.runScheduleNow(t, slow)
	waitFor(t, "problem.resolved for boom", 2*oneGap+30*time.Second, func() bool { return scheduleMessages("problem.resolved", "boom") > 0 })
	if ended := c.g.waitRunStatus(t, slow, slowRun.ID, labRunLimit+time.Minute, "stopped_at_limit"); ended.Error == "" {
		t.Errorf("slow's run = %+v, want stopped at the limit with words for it", ended)
	}
	_, page = c.get(t, "/schedules")
	if row := pageText(scheduleRow(page, slow)); !regexp.MustCompile(`Stopped at the \S+ min limit\.`).MatchString(row) {
		t.Errorf("slow's row = %s, want \"Stopped at the … limit.\"", row)
	}
	slowRun = c.runScheduleNow(t, slow)
	c.g.waitRunStatus(t, slow, slowRun.ID, 2*time.Minute, "running")
	_, page = c.get(t, "/schedules?open="+slow)
	stopPath := "/schedules/" + slow + "/runs/" + slowRun.ID + "/stop"
	if !strings.Contains(scheduleRow(page, slow), `action="`+stopPath+`"`) {
		t.Fatalf("slow's row has no Stop this run form for %s:\n%s", slowRun.ID, pageText(scheduleRow(page, slow)))
	}
	resp, page = c.post(t, stopPath, url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST Stop this run = %d: %s", resp.StatusCode, pageText(page))
	}
	if ended := c.g.waitRunStatus(t, slow, slowRun.ID, time.Minute, "stopped"); ended.Error != "stopped by the owner" {
		t.Errorf("slow's stopped run = %+v, want stopped by the owner", ended)
	}
	// A run the owner stopped is not a failure, and it doesn't clear one either
	// (research R5): the limit's failure stays on the list until a run succeeds.
	if items, _ := c.attention(t); !func() bool {
		for _, it := range items {
			if it.Kind == "schedule_failed" && it.AppID == id && it.Detail == "Stopped at the 5 min limit." {
				return true
			}
		}
		return false
	}() {
		t.Errorf("GET /v1/attention after slow was stopped by the owner = %+v, want the limit's schedule_failed item still there", items)
	}

	// 6. Pause and Resume.
	if resp, page = c.post(t, "/schedules/"+hello+"/pause", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST pause = %d: %s", resp.StatusCode, pageText(page))
	}
	if sc, raw := c.g.schedule(t, hello); !sc.Paused || sc.NextRunAt != nil || !strings.Contains(raw, `"nextRunAt":null`) {
		t.Errorf("hello after Pause = %s, want paused with nextRunAt null", raw)
	}
	_, page = c.get(t, "/schedules")
	if row := scheduleRow(page, hello); !strings.Contains(pageText(row), "paused") || !strings.Contains(row, `action="/schedules/`+hello+`/resume"`) {
		t.Errorf("hello's row after Pause = %s, want it paused with a Resume form", pageText(row))
	}
	if resp, page = c.post(t, "/schedules/"+hello+"/resume", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST resume = %d: %s", resp.StatusCode, pageText(page))
	}
	if sc, raw := c.g.schedule(t, hello); sc.Paused || sc.NextRunAt == nil {
		t.Errorf("hello after Resume = %s, want a next run", raw)
	}

	// 7. A secret echoed is masked.
	show := c.newSchedule(t, id, "show-token", "sh -c 'echo $TOKEN'", "every day at 06:00", "0 6 * * *")
	run = c.runScheduleNow(t, show)
	c.g.waitRunStatus(t, show, run.ID, 2*time.Minute, "ok")
	if _, page = c.get(t, "/schedules/"+show+"/runs/"+run.ID); !strings.Contains(pageText(page), "[secret TOKEN]") || strings.Contains(page, secretValue) {
		t.Errorf("show-token's run page = %s, want [secret TOKEN] and never the value", pageText(page))
	}

	// 8. Configure lists the app's schedules.
	if _, page = c.get(t, "/apps/"+id+"/configure"); !strings.Contains(pageText(page), "hello · every day at 03:00 UTC") {
		t.Errorf("Configure of %s doesn't list hello:\n%s", id, pageText(page))
	}

	// 9. The printed sentinel only ever on its run's page and output; the secret value nowhere.
	ids := []string{hello, boom, slow, show}
	var answers []string
	for _, p := range []string{"/schedules", "/parts/schedules", "/apps", "/parts/attention", "/apps/" + id, "/apps/" + id + "/configure",
		"/schedules?app=" + id, "/schedules?open=" + hello} {
		_, page := c.get(t, p)
		answers = append(answers, "GET "+p+"\n"+page)
	}
	for _, p := range append([]string{"/v1/schedules", "/v1/attention", "/v1/apps/" + id,
		"/v1/notifications/channels/" + channelID + "/deliveries"}, func() []string {
		var ps []string
		for _, s := range ids {
			ps = append(ps, "/v1/schedules/"+s, "/v1/schedules/"+s+"/runs")
		}
		return ps
	}()...) {
		answers = append(answers, "GET "+p+"\n"+string(c.g.sendWant(t, http.StatusOK, http.MethodGet, p, "")))
	}
	for _, r := range rx.all() {
		answers = append(answers, "a delivery\n"+string(r.Body))
	}
	answers = append(answers, "the live stream\n"+live.text())
	if c.g.target.ServerLog != nil {
		answers = append(answers, "Caravana's log\n"+c.g.target.ServerLog())
	}
	for _, a := range answers {
		what, _, _ := strings.Cut(a, "\n")
		if strings.Contains(a, printed) {
			t.Errorf("%s holds hello's output %q", what, printed)
		}
		if strings.Contains(a, secretValue) {
			t.Errorf("%s holds the secret's value", what)
		}
	}
	if dir := c.g.target.DataDir; dir != "" {
		files := 0
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			files++
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), secretValue) {
				t.Errorf("%s holds the secret's value", path)
			}
			return nil
		})
		if err != nil || files == 0 {
			t.Errorf("read the data directory %s: %d files, %v", dir, files, err)
		}
	} else {
		t.Log("this target has no data directory to search")
	}
	if n := scheduleMessages("problem.opened", "boom"); n != 1 {
		t.Errorf("problem.opened for boom was sent %d times, want once", n)
	}

	// 10. Delete the schedules through their confirmation, then the app.
	for _, s := range []struct{ id, name string }{{hello, "hello"}, {boom, "boom"}, {slow, "slow"}, {show, "show-token"}} {
		c.deleteSchedule(t, s.id, s.name)
	}
	var left struct {
		Schedules []gateSchedule `json:"schedules"`
	}
	c.g.getJSON(t, "/v1/schedules?app="+id, &left)
	if len(left.Schedules) != 0 {
		t.Errorf("listSchedules?app=%s after the deletes = %+v, want none", id, left.Schedules)
	}
	if resp, page = c.post(t, "/apps/"+id+"/delete", url.Values{"confirm": {id}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST the delete of %s = %d: %s", id, resp.StatusCode, pageText(page))
	}
	waitFor(t, id+"'s jobs, runs included, to be gone from Nomad", time.Minute, func() bool {
		stubs, _, err := c.g.target.Nomad.Jobs().PrefixList(id + "_")
		return err == nil && len(stubs) == 0 && c.jobAbsent(id)
	})

	// 11. The built-in row of a volume app's daily backup, set on its Backups tab.
	// A policy needs a backup destination: the backup steps set one in the full
	// run; on its own, this step starts the lab's bucket as console-volumes does.
	if resp := c.g.api(t, http.MethodGet, "/v1/backups/destination", "", ""); resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		c.g.startBucket(t)
	} else {
		resp.Body.Close()
	}
	vol := page2Service("web", "")
	vol["args"] = []string{"-c", "[ -f /data/v ] || printf A > /data/v; exec httpd -f -p 8080 -h /data"}
	vol["health"] = map[string]any{"path": "/v", "interval": "5s", "timeout": "2s"}
	vol["volumes"] = []map[string]any{{"name": "data", "path": "/data"}}
	c.deployLabApp(t, volID, labApp(vol))
	_, page = c.get(t, "/apps/"+volID+"/backups")
	policy, ok := formWhere(formsPosting(page, "/apps/"+volID+"/backups/policy"), "service", "web")
	if !ok {
		t.Fatalf("%s's Backups tab has no Back up daily form for web:\n%s", volID, pageText(page))
	}
	resp, body = c.post(t, "/apps/"+volID+"/backups/policy", policy)
	c.deployedFrom(t, volID, resp, body)
	builtin := "backup:" + volID + ":web"
	if sc, raw := c.g.schedule(t, builtin); !sc.Builtin || sc.Name != "web-backup" || sc.Words != "Every day at 03:00 UTC" {
		t.Errorf("GET /v1/schedules/%s = %s, want the built-in web-backup, every day at 03:00 UTC", builtin, raw)
	}
	_, page = c.get(t, "/schedules")
	row := scheduleRow(page, builtin)
	if !strings.Contains(pageText(row), "web backup") || !strings.Contains(row, "Change in Backups") {
		t.Errorf("the built-in row = %s, want web backup with Change in Backups", pageText(row))
	}
	for _, not := range []string{"/run\"", "/pause\"", "/resume\"", "/edit\"", "/delete\"", "Run now"} {
		if strings.Contains(row, not) {
			t.Errorf("the built-in row has %s; it is changed in Backups only", not)
		}
	}
	if resp, page = c.post(t, "/apps/"+volID+"/delete", url.Values{"confirm": {volID}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST the delete of %s = %d: %s", volID, resp.StatusCode, pageText(page))
	}
	waitFor(t, volID+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(volID) })
}

// logTail logs the end of Caravana's log when the test failed, for the
// reason a request hung or a run ended as it did.
func logTail(t *testing.T, target gateTarget) {
	if !t.Failed() || target.ServerLog == nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(target.ServerLog()), "\n")
	t.Logf("the last lines of Caravana's log:\n%s", strings.Join(lines[max(0, len(lines)-80):], "\n"))
}
