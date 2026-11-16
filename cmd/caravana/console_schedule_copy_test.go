package main

// The console-schedule-in-copy step of the lab test (feature 040, spec D9): a
// schedule that runs in the running copy, made and read through the console's
// forms (no JavaScript), against real Nomad. It is in its own file only because
// console_lab_test.go is long; runConsole lists it after console-schedules.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// scheduleInCopy:
//
//  1. New schedule shows "Run in" with the running copy and "In every copy".
//  2. A schedule on a service with a volume is created with Run in = the
//     running copy and the Usual limit (Stop after 0, which is 1 minute for
//     the running copy): the row says "in the running copy" and "stops after
//     1 min".
//  3. Run now: the command appends to a file on the volume, prints a sentinel
//     it doesn't hold whole and exits 3. The run ends failed with exit code 3
//     and the copy it ran in; its page says "This run was in the running copy:
//     its output isn't kept.", "Exited with code 3." and "exit code 3", and
//     holds no sentinel; neither does any other page, nor the log.
//  4. The app's Recent actions say "on a schedule".
//  5. The schedule is deleted through its confirmation, then the app.
func (c *console) scheduleInCopy(t *testing.T) {
	const id = "con040"
	printed := "s3cret-sentinel-040-" + c.g.run
	c.removeLabApp(id) // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	t.Cleanup(func() { c.removeLabApp(id) })
	t.Cleanup(func() { logTail(t, c.g.target) })

	files := map[string]any{"name": "files", "image": "busybox:1.37", "command": "sh", "args": []string{"-c", "exec sleep 86400"},
		"resources": labResources, "volumes": []map[string]any{{"name": "data", "path": "/data"}}}
	c.deployLabApp(t, id, labApp(files))
	var copies []string
	waitFor(t, "a running copy of "+id, time.Minute, func() bool {
		copies = c.g.runningCopies(t, id)
		return len(copies) == 1
	})

	// 1. The form.
	resp, page := c.get(t, "/schedules/new?app="+id)
	for _, want := range []string{`<input type="radio" name="target" value="fresh" checked>`, `<input type="radio" name="target" value="copy">`,
		`<input type="checkbox" name="allCopies" value="1">`, `<option value="0" selected>Usual (30 min; 1 min in the running copy)</option>`} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, want) {
			t.Fatalf("GET /schedules/new?app=%s = %d, want the form with %s:\n%s", id, resp.StatusCode, want, pageText(page))
		}
	}

	// 2. Create it.
	// The command is split like a shell's words: the sentinel is joined by printf from pieces.
	command := `sh -c 'echo grew >> /data/con040.txt; printf "s3cret-sentinel-0%s-%s" 40 ` + c.g.run + `; exit 3'`
	form := url.Values{"name": {"purge"}, "app": {id}, "command": {command}, "when": {"every day at 03:00"},
		"target": {"copy"}, "stopAfter": {"0"}, "memory": {"256"}, "skipChoice": {"1"}, "skip": {"1"}, "action": {"create"}}
	resp, page = c.post(t, "/schedules", form)
	loc := resp.Header.Get("Location")
	m := regexp.MustCompile(`^/schedules\?created=([0-9a-f]+)#schedule-([0-9a-f]+)$`).FindStringSubmatch(loc)
	if resp.StatusCode != http.StatusSeeOther || m == nil || m[1] != m[2] {
		t.Fatalf("Create purge = %d → %q, want 303 to its row: %s", resp.StatusCode, loc, pageText(page))
	}
	schedID := m[1]
	sc, _ := c.g.schedule(t, schedID)
	if sc.StopAfterSeconds != 60 {
		t.Errorf("the Usual limit of a schedule in the running copy = %d s, want 60", sc.StopAfterSeconds)
	}
	_, page = c.get(t, loc)
	if row := pageText(scheduleRow(page, schedID)); !strings.Contains(row, "in the running copy") || !strings.Contains(row, "stops after1 min") &&
		!strings.Contains(row, "stops after 1 min") {
		t.Errorf("purge's row = %s, want it to say it runs in the running copy and stops after 1 min", row)
	}

	// 3. Run now.
	run := c.runScheduleNow(t, schedID)
	var ended gateCopyRun
	waitFor(t, "purge's run to end", 2*time.Minute, func() bool {
		for _, r := range c.g.copyRunsOf(t, schedID) {
			if r.ID == run.ID && !r.open() {
				ended = r
				return true
			}
		}
		return false
	})
	if ended.Status != "failed" || ended.ExitCode == nil || *ended.ExitCode != 3 || ended.Target != "copy" || len(ended.Copies) != 1 ||
		ended.Copies[0] != copies[0] || ended.OutputKept {
		t.Fatalf("purge's run = %+v, want failed, exit 3, in copy %s, no output kept", ended, copies[0])
	}
	resp, runPage := c.get(t, "/schedules/"+schedID+"/runs/"+run.ID)
	got := pageText(runPage)
	for _, want := range []string{"This run was in the running copy: its output isn't kept.", "Exited with code 3.", "exit code 3",
		"copy " + copies[0][:8]} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(got, want) {
			t.Errorf("the run page = %d, lacks %q:\n%s", resp.StatusCode, want, got)
		}
	}
	if n := c.g.linesIn(t, id, copies[0], "/data/con040.txt"); n != 1 {
		t.Errorf("/data/con040.txt has %d lines after one run, want 1", n)
	}

	// 4. Recent actions.
	_, part := c.get(t, "/parts/apps/"+id+"/actions")
	if !strings.Contains(pageText(part), "on a schedule") {
		t.Errorf("Recent actions lack \"on a schedule\": %s", pageText(part))
	}

	// No page, answer or log line holds the output.
	pages := []string{runPage, part}
	for _, p := range []string{"/schedules", "/parts/schedules", "/schedules/" + schedID + "/edit", "/apps/" + id + "/configure", "/apps", "/parts/attention"} {
		_, body := c.get(t, p)
		pages = append(pages, body)
	}
	if c.g.target.ServerLog != nil {
		pages = append(pages, c.g.target.ServerLog())
	}
	for i, p := range pages {
		if strings.Contains(p, printed) {
			t.Errorf("page or log #%d holds the command's output", i)
		}
	}

	// 5. Delete.
	c.deleteSchedule(t, schedID, "purge")
}
