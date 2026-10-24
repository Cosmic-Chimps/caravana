package main

// The schedules gate step (feature 022, plan.md "Lab"), and the helpers it and
// the console step share. It is in its own file only because gate_test.go is
// long; runGate lists it after app-actions.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// gateSchedule is the part of a Schedule (api/openapi.yaml) these steps read.
type gateSchedule struct {
	ID               string           `json:"id"`
	App              string           `json:"app"`
	Service          string           `json:"service"`
	Name             string           `json:"name"`
	Command          []string         `json:"command"`
	Cron             string           `json:"cron"`
	Words            string           `json:"words"`
	StopAfterSeconds int              `json:"stopAfterSeconds"`
	Paused           bool             `json:"paused"`
	Builtin          bool             `json:"builtin"`
	NextRunAt        *time.Time       `json:"nextRunAt"`
	LastRun          *gateScheduleRun `json:"lastRun"`
	RunningNow       int              `json:"runningNow"`
}

// gateScheduleRun is the part of a ScheduleRun these steps read.
type gateScheduleRun struct {
	ID            string     `json:"id"`
	ScheduleID    string     `json:"scheduleId"`
	Trigger       string     `json:"trigger"`
	Status        string     `json:"status"`
	ExitCode      *int       `json:"exitCode"`
	Error         string     `json:"error"`
	TaskStartedAt *time.Time `json:"taskStartedAt"`
	OutputKept    bool       `json:"outputKept"`
	StdoutBytes   int        `json:"stdoutBytes"`
}

// ended says the run has settled.
func (r gateScheduleRun) ended() bool { return r.Status != "starting" && r.Status != "running" }

// schedule reads one schedule through the API, and its raw answer.
func (g *gate) schedule(t *testing.T, id string) (gateSchedule, string) {
	t.Helper()
	raw := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/schedules/"+id, "")
	var sc gateSchedule
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("getSchedule %s: %v: %s", id, err, raw)
	}
	return sc, string(raw)
}

// scheduleRuns lists a schedule's runs, newest first.
func (g *gate) scheduleRuns(t *testing.T, id string) []gateScheduleRun {
	t.Helper()
	raw := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/schedules/"+id+"/runs", "")
	var list struct {
		Runs []gateScheduleRun `json:"runs"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("listScheduleRuns %s: %v: %s", id, err, raw)
	}
	return list.Runs
}

// scheduleRun reads one run.
func (g *gate) scheduleRun(t *testing.T, schedID, runID string) gateScheduleRun {
	t.Helper()
	raw := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/schedules/"+schedID+"/runs/"+runID, "")
	var r gateScheduleRun
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("getScheduleRun %s: %v: %s", runID, err, raw)
	}
	return r
}

// waitRunStatus waits until the run's status is one of want (or, when want is
// empty, until it has ended) and returns it. It fails early when the run ends
// some other way.
func (g *gate) waitRunStatus(t *testing.T, schedID, runID string, within time.Duration, want ...string) gateScheduleRun {
	t.Helper()
	var r gateScheduleRun
	ok := waitUntil(within, func() bool {
		r = g.scheduleRun(t, schedID, runID)
		for _, w := range want {
			if r.Status == w {
				return true
			}
		}
		if r.ended() && len(want) > 0 {
			t.Fatalf("run %s of %s ended %s (%s), want %v", runID, schedID, r.Status, r.Error, want)
		}
		return r.ended()
	})
	if !ok {
		t.Fatalf("run %s of %s is still %s after %v, want %v", runID, schedID, r.Status, within, want)
	}
	return r
}

// runOutput reads a run's output on one stream: the status, the body and the
// headers.
func (g *gate) runOutput(t *testing.T, schedID, runID, stream string) (int, string, http.Header) {
	t.Helper()
	resp := g.api(t, http.MethodGet, "/v1/schedules/"+schedID+"/runs/"+runID+"/output?stream="+stream, "", "")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

// schedulesStep: schedules through the public API with the gate's token, on a
// busybox app (web, which serves a page, and files, which holds a volume
// with a daily volume backup, so the app has a built-in schedule).
//
//  1. createSchedule makes hello (201, Location, words in UTC, a next run); the
//     same name again is 409 schedule_exists, a cron that never fires is 422
//     on cron, and a schedule on the volume service is 422 on service.
//  2. runSchedule is 202; the run ends ok with exit code 0; its output is
//     text/plain, no-store, Caravana-Output-Cut false, and holds what the
//     command printed, which no other answer (the schedule, its runs, the
//     list) holds.
//  3. pause: nextRunAt null; resume: nextRunAt set again.
//  4. A run of sleep 20 is going: Run now again is 409 run_going. Caravana is
//     restarted during it (in the lab), and the same run ends ok, not lost.
//  5. The built-in backup:<app>:files is listed with builtin true and the
//     words of the policy; update, delete, run, pause and resume are 409
//     builtin_schedule.
//  6. A deploy token for the app (Default › production) gets 403 token_scope on
//     every schedule route, the built-in's and a run's included.
//  7. deleteSchedule is 204 and the schedule is gone (404); no run job of the
//     app is left in Nomad. The app goes in the clean-up.
func (g *gate) schedulesStep(t *testing.T) {
	const id = "gate022"
	printed := "gate-hello-022-" + g.run // what hello prints; its command never holds it
	tokenID := ""
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
		if tokenID != "" {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/tokens/"+tokenID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil && strings.Contains(err.Error(), "404")
	})
	t.Cleanup(remove)

	// files' backup policy needs a backup destination: the backup steps set one
	// in the full gate; on its own, this step starts the lab's bucket.
	if resp := g.api(t, http.MethodGet, "/v1/backups/destination", "", ""); resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		g.startBucket(t)
	} else {
		resp.Body.Close()
	}
	files := map[string]any{"name": "files", "image": "busybox:1.37", "command": "sh", "args": []string{"-c", "exec sleep 86400"},
		"resources": labResources, "volumes": []map[string]any{{"name": "data", "path": "/data"}},
		"backup": map[string]any{"engine": "volume"}}
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(actionsWeb("", 1), files))
	var d gateDeployment
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}

	create := func(name string, command []string, cron string) gateSchedule {
		t.Helper()
		body := string(mustJSON(t, map[string]any{"app": id, "name": name, "command": command, "cron": cron,
			"stopAfterSeconds": 300, "memoryMB": 256}))
		status, raw := g.send(t, http.MethodPost, "/v1/schedules", body)
		var sc gateSchedule
		if err := json.Unmarshal(raw, &sc); status != http.StatusCreated || err != nil || sc.ID == "" {
			t.Fatalf("createSchedule %s = %d: %s", name, status, raw)
		}
		return sc
	}
	runNow := func(schedID string) gateScheduleRun {
		t.Helper()
		raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/schedules/"+schedID+"/run", "")
		var r gateScheduleRun
		if err := json.Unmarshal(raw, &r); err != nil || r.ID == "" || (r.Status != "running" && r.Status != "starting") || r.Trigger != "owner" {
			t.Fatalf("runSchedule %s = %s, want a starting or running run triggered by the owner", schedID, raw)
		}
		return r
	}

	// 1. Create, and three refusals.
	helloCmd := []string{"sh", "-c", "echo gate-hello-0$((20+2))-" + g.run}
	status, rawHello := g.send(t, http.MethodPost, "/v1/schedules", string(mustJSON(t, map[string]any{"app": id, "name": "hello",
		"command": helloCmd, "cron": "0 3 * * *", "stopAfterSeconds": 300, "memoryMB": 256})))
	var hello gateSchedule
	_ = json.Unmarshal(rawHello, &hello)
	if status != http.StatusCreated || hello.ID == "" || hello.Words != "Every day at 03:00 UTC" || hello.NextRunAt == nil ||
		hello.Builtin || hello.Paused || hello.Service != "web" {
		t.Fatalf("createSchedule hello = %d %s, want 201 every day at 03:00 UTC on web with a next run", status, rawHello)
	}
	if resp := g.api(t, http.MethodGet, "/v1/schedules/"+hello.ID, "", ""); resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("GET the new schedule = %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	g.refusal(t, http.StatusConflict, "schedule_exists", "", http.MethodPost, "/v1/schedules",
		string(mustJSON(t, map[string]any{"app": id, "name": "hello", "command": []string{"true"}, "cron": "0 3 * * *"})))
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "cron", http.MethodPost, "/v1/schedules",
		string(mustJSON(t, map[string]any{"app": id, "name": "never", "command": []string{"true"}, "cron": "0 0 31 2 *"})))
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "service", http.MethodPost, "/v1/schedules",
		string(mustJSON(t, map[string]any{"app": id, "service": "files", "name": "onvolume", "command": []string{"true"}, "cron": "0 3 * * *"})))

	// 2. Run now: ok, and its output only where it belongs.
	run := runNow(hello.ID)
	if ended := g.waitRunStatus(t, hello.ID, run.ID, 2*time.Minute, "ok"); ended.ExitCode == nil || *ended.ExitCode != 0 || !ended.OutputKept {
		t.Errorf("hello's run = %+v, want ok with exit code 0 and its output kept", ended)
	}
	code, out, header := g.runOutput(t, hello.ID, run.ID, "stdout")
	if code != http.StatusOK || strings.TrimSpace(out) != printed || header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		header.Get("Cache-Control") != "no-store" || header.Get("Caravana-Output-Cut") != "false" {
		t.Errorf("the run's stdout = %d %q (Content-Type %q, Cache-Control %q, Cut %q), want 200 %q as text/plain, no-store, not cut",
			code, out, header.Get("Content-Type"), header.Get("Cache-Control"), header.Get("Caravana-Output-Cut"), printed)
	}
	if code, out, _ := g.runOutput(t, hello.ID, run.ID, "stderr"); code != http.StatusOK || out != "" {
		t.Errorf("the run's stderr = %d %q, want 200 and empty", code, out)
	}
	for _, path := range []string{"/v1/schedules", "/v1/schedules/" + hello.ID, "/v1/schedules/" + hello.ID + "/runs",
		"/v1/schedules/" + hello.ID + "/runs/" + run.ID, "/v1/attention"} {
		if raw := g.sendWant(t, http.StatusOK, http.MethodGet, path, ""); strings.Contains(string(raw), printed) {
			t.Errorf("GET %s holds the run's output: %s", path, raw)
		}
	}

	// 3. Pause and resume.
	raw = g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/schedules/"+hello.ID+"/pause", "")
	var sc gateSchedule
	if err := json.Unmarshal(raw, &sc); err != nil || !sc.Paused || sc.NextRunAt != nil {
		t.Errorf("pauseSchedule = %s, want paused with no next run", raw)
	}
	raw = g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/schedules/"+hello.ID+"/resume", "")
	sc = gateSchedule{}
	if err := json.Unmarshal(raw, &sc); err != nil || sc.Paused || sc.NextRunAt == nil {
		t.Errorf("resumeSchedule = %s, want not paused with a next run", raw)
	}

	// 4. A run going: a second is refused; Caravana restarts under it, and it
	// ends ok all the same.
	sleeper := create("sleep20", []string{"sleep", "20"}, "0 4 * * *")
	run = runNow(sleeper.ID)
	g.refusal(t, http.StatusConflict, "run_going", "", http.MethodPost, "/v1/schedules/"+sleeper.ID+"/run", "")
	g.waitRunStatus(t, sleeper.ID, run.ID, 2*time.Minute, "running")
	if g.target.RestartCaravana != nil {
		g.target.RestartCaravana(t)
	} else {
		t.Log("this target can't restart Caravana; the run is only followed to its end")
	}
	if ended := g.waitRunStatus(t, sleeper.ID, run.ID, 2*time.Minute, "ok"); ended.ExitCode == nil || *ended.ExitCode != 0 {
		t.Errorf("the run of sleep 20 across the restart = %+v, want ok with exit code 0", ended)
	}

	// 5. The built-in schedule of files' backup is read-only.
	builtin := "backup:" + id + ":files"
	var list struct {
		Schedules []gateSchedule `json:"schedules"`
	}
	g.getJSON(t, "/v1/schedules?app="+id, &list)
	found := false
	for _, s := range list.Schedules {
		if s.ID == builtin {
			found = true
			if !s.Builtin || s.Name != "files-backup" || s.Words != "Every day at 03:00 UTC" || s.Service != "files" {
				t.Errorf("the built-in schedule = %+v, want files-backup, builtin, every day at 03:00 UTC", s)
			}
		}
	}
	if !found || len(list.Schedules) != 3 {
		t.Errorf("listSchedules?app=%s = %+v, want hello, sleep20 and %s", id, list.Schedules, builtin)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPut, "/v1/schedules/" + builtin, string(mustJSON(t, map[string]any{"app": id, "name": "x", "command": []string{"true"}, "cron": "0 3 * * *"}))},
		{http.MethodDelete, "/v1/schedules/" + builtin, ""},
		{http.MethodPost, "/v1/schedules/" + builtin + "/run", ""},
		{http.MethodPost, "/v1/schedules/" + builtin + "/pause", ""},
		{http.MethodPost, "/v1/schedules/" + builtin + "/resume", ""},
	} {
		g.refusal(t, http.StatusConflict, "builtin_schedule", "", c.method, c.path, c.body)
	}

	// 6. A deploy token for this app reaches no schedule route.
	raw = g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"gate022-ci-`+g.run+`","scope":{"project":"default","app":"`+id+`"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(raw, &made); err != nil || made.Token == "" {
		t.Fatalf("createToken scoped to %s: %s", id, raw)
	}
	tokenID = made.ID
	newBody := string(mustJSON(t, map[string]any{"app": id, "name": "ci", "command": []string{"true"}, "cron": "0 3 * * *"}))
	runPath := "/v1/schedules/" + hello.ID + "/runs/" + run.ID
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/schedules", ""},
		{http.MethodGet, "/v1/schedules?app=" + id, ""},
		{http.MethodPost, "/v1/schedules", newBody},
		{http.MethodGet, "/v1/schedules/" + hello.ID, ""},
		{http.MethodPut, "/v1/schedules/" + hello.ID, newBody},
		{http.MethodDelete, "/v1/schedules/" + hello.ID, ""},
		{http.MethodPost, "/v1/schedules/" + hello.ID + "/run", ""},
		{http.MethodPost, "/v1/schedules/" + hello.ID + "/pause", ""},
		{http.MethodPost, "/v1/schedules/" + hello.ID + "/resume", ""},
		{http.MethodGet, "/v1/schedules/" + hello.ID + "/runs", ""},
		{http.MethodGet, runPath, ""},
		{http.MethodPost, runPath + "/stop", ""},
		{http.MethodGet, runPath + "/output", ""},
		{http.MethodGet, "/v1/schedules/" + builtin, ""},
		{http.MethodGet, "/v1/schedules/" + builtin + "/runs", ""},
	} {
		status, raw := g.sendAs(t, made.Token, c.method, c.path, c.body)
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if status != http.StatusForbidden || e.Error.Code != "token_scope" {
			t.Errorf("%s %s with the deploy token = %d %s, want 403 token_scope", c.method, c.path, status, raw)
		}
	}
	if sc, _ := g.schedule(t, hello.ID); sc.Paused || sc.Name != "hello" {
		t.Errorf("hello after the deploy token's tries = %+v, want it unchanged", sc)
	}

	// 7. Delete; nothing of the runs is left in Nomad.
	for _, schedID := range []string{hello.ID, sleeper.ID} {
		g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/schedules/"+schedID, "")
		g.refusal(t, http.StatusNotFound, "schedule_not_found", "", http.MethodGet, "/v1/schedules/"+schedID, "")
	}
	waitFor(t, "no run job of "+id+" to be left in Nomad", time.Minute, func() bool {
		stubs, _, err := g.target.Nomad.Jobs().PrefixList(id + "_run-")
		return err == nil && len(stubs) == 0
	})
}
