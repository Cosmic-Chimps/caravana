package main

// The schedule-in-copy gate step (feature 040, spec D9): a schedule that runs
// its command in the running copy of a service, on a service with a volume.
// It is in its own file only because gate_test.go is long; runGate lists it
// after schedules.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gateCopyRun is the part of a ScheduleRun (api/openapi.yaml) this step reads.
type gateCopyRun struct {
	ID         string     `json:"id"`
	Trigger    string     `json:"trigger"`
	SlotAt     *time.Time `json:"slotAt"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exitCode"`
	Error      string     `json:"error"`
	OutputKept bool       `json:"outputKept"`
	Target     string     `json:"target"`
	Copies     []string   `json:"copies"`
}

func (r gateCopyRun) open() bool { return r.Status == "starting" || r.Status == "running" }

// copyRunsOf lists a schedule's runs, newest first, with the fields of a run in a copy.
func (g *gate) copyRunsOf(t *testing.T, schedID string) []gateCopyRun {
	t.Helper()
	var list struct {
		Runs []gateCopyRun `json:"runs"`
	}
	g.getJSON(t, "/v1/schedules/"+schedID+"/runs", &list)
	return list.Runs
}

// linesIn is how many lines the file has in the copy, read with Run a command.
func (g *gate) linesIn(t *testing.T, appID, copyID, file string) int {
	t.Helper()
	run, raw, _ := g.runIn(t, appID, copyID, "sh", "-c", "wc -l < "+file+" || echo 0")
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(run.Stdout), "%d", &n); err != nil {
		t.Fatalf("count the lines of %s in %s: %s", file, copyID, raw)
	}
	return n
}

// scheduleInCopyStep: schedules that run in the running copy, through the
// public API with the gate's token, on a busybox service with a volume.
//
//  1. Two schedules every minute on the volume service: purge appends to a
//     file on the volume, prints a sentinel on both streams and exits 3;
//     slow runs sleep 100 with a 120 s limit. A schedule that runs in a new
//     copy on the same service is still 422 on service.
//  2. purge's slot runs in the live copy: the run is target copy, in that
//     copy, failed with exit code 3, its output not kept, and reading it is
//     404 output_not_kept. Run a command by hand shows the file grew, and
//     again after a later slot.
//  3. /v1/attention has schedule_failed for purge, and listAppActions has a
//     run with the trigger schedule, its program sh and exit code 3 in that
//     copy.
//  4. slow's slot after its first run started is a skipped row, since its run
//     is still going. slow is deleted (its exec is cancelled).
//  5. The app is stopped: purge's next slot is a skipped row, "the app is
//     stopped", and nothing runs in the copy.
//  6. The sentinel is in no answer the step read, in Caravana's log, or in any
//     file of its data directory. The command never holds the sentinel whole
//     (printf joins its pieces), so only its output could put it there.
//
// Both schedules are budgeted for about four minutes: the clock's granularity
// is a minute.
func (g *gate) scheduleInCopyStep(t *testing.T) {
	const id = "lab040"
	printed := "s3cret-sentinel-040-" + g.run
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	t.Cleanup(remove)

	files := map[string]any{"name": "files", "image": "busybox:1.37", "command": "sh", "args": []string{"-c", "exec sleep 86400"},
		"resources": labResources, "volumes": []map[string]any{{"name": "data", "path": "/data"}}}
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(files))
	var d gateDeployment
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	// The lab stops every run at 40 s; slow's run must outlast a slot (60 s), so its own
	// limit (120 s) rules while this step runs.
	if g.target.SetScheduleLimit != nil {
		g.target.SetScheduleLimit(0)
		t.Cleanup(func() { g.target.SetScheduleLimit(labRunLimit) })
	}
	var copies []string
	waitFor(t, "a running copy of "+id, time.Minute, func() bool {
		copies = g.runningCopies(t, id)
		return len(copies) == 1
	})
	copyID := copies[0]
	var later []string // every answer after the sentinel could have been printed

	// 1. The schedules.
	// printf joins the sentinel from pieces, so the command never holds it whole.
	purgeCmd := "echo grew >> /data/purge.txt; printf 's3cret-sentinel-0%s-%s\\n' 40 " + g.run + "; printf 's3cret-sentinel-0%s-%s\\n' 40 " + g.run + " >&2; exit 3"
	create := func(name, command, extra string) string {
		t.Helper()
		body := fmt.Sprintf(`{"app":%q,"service":"files","name":%q,"command":["sh","-c",%s],"cron":"* * * * *","target":"copy"%s}`,
			id, name, mustJSON(t, command), extra)
		status, raw := g.send(t, http.MethodPost, "/v1/schedules", body)
		var sc struct {
			ID, Target string
			AllCopies  bool
			Memory     int `json:"memoryMB"`
			Stop       int `json:"stopAfterSeconds"`
		}
		if err := json.Unmarshal(raw, &sc); status != http.StatusCreated || err != nil || sc.ID == "" || sc.Target != "copy" || sc.Memory != 0 {
			t.Fatalf("createSchedule %s = %d: %s, want 201 with target copy and no memory", name, status, raw)
		}
		return sc.ID
	}
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "service", http.MethodPost, "/v1/schedules",
		`{"app":"`+id+`","service":"files","name":"fresh","command":["true"],"cron":"0 3 * * *"}`)
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "stopAfterSeconds", http.MethodPost, "/v1/schedules",
		`{"app":"`+id+`","service":"files","name":"badlimit","command":["true"],"cron":"0 3 * * *","target":"copy","stopAfterSeconds":1800}`)
	purge := create("purge", purgeCmd, "")
	slow := create("slow", "sleep 100", `,"stopAfterSeconds":120`)

	// 2. purge's first slot.
	var first gateCopyRun
	waitFor(t, "purge's first run to end", 3*time.Minute, func() bool {
		for _, r := range g.copyRunsOf(t, purge) {
			if !r.open() && r.Status != "skipped" {
				first = r
				return true
			}
		}
		return false
	})
	if first.Status != "failed" || first.ExitCode == nil || *first.ExitCode != 3 || first.Error != "exit 3" || first.Target != "copy" ||
		len(first.Copies) != 1 || first.Copies[0] != copyID || first.OutputKept || first.Trigger != "timer" || first.SlotAt == nil {
		t.Fatalf("purge's first run = %+v, want failed, exit 3, target copy in copy %s, no output kept, started by the timer", first, copyID)
	}
	for _, stream := range []string{"stdout", "stderr"} {
		code, out, _ := g.runOutput(t, purge, first.ID, stream)
		if code != http.StatusNotFound || strings.Contains(out, printed) {
			t.Errorf("the %s of a run in the copy = %d %.200s, want 404 output_not_kept", stream, code, out)
		}
		later = append(later, out)
	}
	g.refusal(t, http.StatusNotFound, "output_not_kept", "", http.MethodGet, "/v1/schedules/"+purge+"/runs/"+first.ID+"/output", "")
	grown := g.linesIn(t, id, copyID, "/data/purge.txt")
	if grown < 1 {
		t.Errorf("/data/purge.txt has %d lines after purge's run, want at least 1: the command ran on the volume", grown)
	}

	// 3. The needs-you list and the action record.
	var att struct {
		Items []struct {
			Kind       string `json:"kind"`
			AppID      string `json:"appId"`
			ScheduleID string `json:"scheduleId"`
			Detail     string `json:"detail"`
		} `json:"items"`
	}
	g.getJSON(t, "/v1/attention", &att)
	seen := false
	for _, it := range att.Items {
		if it.Kind == "schedule_failed" && it.ScheduleID == purge && it.AppID == id {
			seen = true
			if it.Detail != "Exited with code 3." {
				t.Errorf("purge's needs-you item = %+v, want the detail \"Exited with code 3.\"", it)
			}
		}
	}
	if !seen {
		t.Errorf("/v1/attention has no schedule_failed for purge: %+v", att.Items)
	}
	acts, rawActs := g.actionList(t, id)
	later = append(later, rawActs)
	found := false
	for _, a := range acts {
		// slow's run (sh -c "sleep 100") may be going: look for purge's, which ended with exit 3.
		if a.Action == "run" && a.Trigger == "schedule" && a.ExitCode != nil && *a.ExitCode == 3 {
			found = true
			if a.Program != "sh" || a.CopyID != copyID || a.Status != "succeeded" {
				t.Errorf("the schedule's action = %+v, want sh, exit 3, succeeded, in %s", a, copyID)
			}
		}
	}
	if !found {
		t.Errorf("listAppActions = %s, want a run with the trigger schedule and exit code 3", rawActs)
	}

	// 4. slow: the slot after its first run started is skipped.
	if !waitUntil(4*time.Minute, func() bool {
		for _, r := range g.copyRunsOf(t, slow) {
			if r.Status == "skipped" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("slow skipped no slot in 4 minutes while its run went; its runs: %+v", g.copyRunsOf(t, slow))
	}
	var skipped, started *gateCopyRun
	runs := g.copyRunsOf(t, slow)
	for i := range runs {
		if runs[i].Status == "skipped" && skipped == nil {
			skipped = &runs[i]
		}
		if runs[i].Status != "skipped" {
			started = &runs[i]
		}
	}
	if skipped == nil || started == nil || skipped.Error != "the run before was still going" || skipped.Target != "copy" || len(skipped.Copies) != 0 {
		t.Errorf("slow's runs = %+v, want one that started and a skipped slot saying the run before was still going", runs)
	}
	if started != nil && started.Target == "copy" && len(started.Copies) != 1 {
		t.Errorf("slow's run = %+v, want one copy", *started)
	}
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/schedules/"+slow, "")
	g.refusal(t, http.StatusNotFound, "schedule_not_found", "", http.MethodGet, "/v1/schedules/"+slow, "")

	// The file grew with the later slots too.
	waitFor(t, "purge's file to grow past "+fmt.Sprint(grown)+" lines", 3*time.Minute, func() bool {
		return g.linesIn(t, id, copyID, "/data/purge.txt") > grown
	})

	// 5. The app is stopped: the next slot is a skipped row.
	g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+id+"/stop", "")
	waitFor(t, "no copy of "+id+" to run", time.Minute, func() bool { return len(g.runningCopies(t, id)) == 0 })
	before := len(g.copyRunsOf(t, purge))
	waitFor(t, "purge's slot to be skipped, the app being stopped", 3*time.Minute, func() bool {
		runs := g.copyRunsOf(t, purge)
		return len(runs) > before && runs[0].Status == "skipped"
	})
	newest := g.copyRunsOf(t, purge)[0]
	if newest.Error != "the app is stopped" || newest.Target != "copy" || len(newest.Copies) != 0 || newest.Trigger != "timer" {
		t.Errorf("purge's slot while the app is stopped = %+v, want skipped with \"the app is stopped\"", newest)
	}
	g.getJSON(t, "/v1/attention", &att)
	for _, it := range att.Items {
		if it.Kind == "schedule_skipping" && it.ScheduleID == purge {
			t.Errorf("a stopped app raised schedule_skipping: %+v", it)
		}
	}

	// 6. The sentinel is nowhere.
	for _, p := range []string{"/v1/schedules", "/v1/schedules?app=" + id, "/v1/schedules/" + purge, "/v1/schedules/" + purge + "/runs",
		"/v1/schedules/" + purge + "/runs/" + first.ID, "/v1/attention", "/v1/apps/" + id, "/v1/apps", "/v1/apps/" + id + "/actions",
		"/v1/apps/" + id + "/logs", "/v1/activity"} {
		later = append(later, string(g.sendWant(t, http.StatusOK, http.MethodGet, p, "")))
	}
	if g.target.ServerLog != nil {
		later = append(later, g.target.ServerLog())
	}
	for i, text := range later {
		if strings.Contains(text, printed) {
			t.Errorf("answer #%d holds the command's output: %.300s", i, text)
		}
	}
	if dir := g.target.DataDir; dir != "" {
		n := 0
		matches, _ := filepath.Glob(filepath.Join(dir, "*"))
		for _, f := range matches {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			n++
			if strings.Contains(string(b), printed) {
				t.Errorf("%s holds the command's output", f)
			}
		}
		if n == 0 {
			t.Errorf("no file read in the data directory %s", dir)
		}
	} else {
		t.Log("this target has no data directory to search")
	}

	g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+id+"/start", "")
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/schedules/"+purge, "")
}
