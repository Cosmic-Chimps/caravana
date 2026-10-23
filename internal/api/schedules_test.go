package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// outputSentinel is what the schedules' fake runs print. No answer but
// getScheduleRunOutput's, and no log line, may hold it.
const outputSentinel = "s3cret-sentinel-022-output"

// scheduleEnv is the API with whoami deployed and healthy, and a run watcher
// that looks every few milliseconds.
func scheduleEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnvWith(t, "", func(svc *service.Service) { svc.SetScheduleWatchGap(5 * time.Millisecond) })
	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)
	return e
}

// createScheduleOK creates a schedule through the API and checks the answer.
func createScheduleOK(t *testing.T, e *testEnv, body string) service.Schedule {
	t.Helper()
	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/schedules", "application/json", body)
	if status != http.StatusCreated {
		t.Fatalf("createSchedule = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Schedule")
	sc := decodeAs[service.Schedule](t, raw)
	if loc := header.Get("Location"); loc != "/v1/schedules/"+sc.ID {
		t.Errorf("Location = %q, want /v1/schedules/%s", loc, sc.ID)
	}
	return sc
}

// runScheduleOK starts a run through the API and checks the answer.
func runScheduleOK(t *testing.T, e *testEnv, id string) service.ScheduleRun {
	t.Helper()
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/"+id+"/run", "", "")
	if status != http.StatusAccepted {
		t.Fatalf("runSchedule = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "ScheduleRun")
	return decodeAs[service.ScheduleRun](t, raw)
}

// getRun reads a run through the API and checks it against the contract.
func getRun(t *testing.T, e *testEnv, scheduleID, runID string) service.ScheduleRun {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+scheduleID+"/runs/"+runID, "", "")
	if status != http.StatusOK {
		t.Fatalf("getScheduleRun = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "ScheduleRun")
	return decodeAs[service.ScheduleRun](t, raw)
}

// waitRun waits for the watcher to settle a run, and returns it.
func waitRun(t *testing.T, e *testEnv, scheduleID, runID string) service.ScheduleRun {
	t.Helper()
	var run service.ScheduleRun
	waitUntil(t, func() bool {
		run = getRun(t, e, scheduleID, runID)
		return run.FinishedAt != nil
	})
	return run
}

// finishRun plays a run's end in the fake Nomad: it prints the sentinel on
// both streams, then exits with code.
func finishRun(e *testEnv, run service.ScheduleRun, code int) {
	job := deploy.ScheduleRunJobID("whoami", run.ID)
	e.Nomad.SetRunLogs(job, []byte("line one\n"+outputSentinel+"\n"), []byte("warn: "+outputSentinel+"\n"))
	e.Nomad.FinishRunExit(job, code)
}

// wantError checks an error answer's status, code and field.
func wantScheduleError(t *testing.T, what string, status int, raw []byte, wantStatus int, code, field string) {
	t.Helper()
	body := decodeAs[errorBody](t, raw)
	if status != wantStatus || body.Error.Code != code || body.Error.Field != field {
		t.Errorf("%s = %d %s, want %d %s (field %q)", what, status, raw, wantStatus, code, field)
	}
}

const nightly = `{"app": "whoami", "name": "rates-sync", "command": ["sh", "-c", "echo hi"], "cron": "0 3 * * *"}`

// T009: create, read, change and remove a schedule, and each refusal of the
// writes.
func TestScheduleEndpoints(t *testing.T) {
	e := scheduleEnv(t)
	doc := loadContract(t)
	sc := createScheduleOK(t, e, nightly)
	if sc.App != "whoami" || sc.Service != "web" || sc.Words != "Every day at 03:00 UTC" || sc.StopAfterSeconds != 1800 ||
		sc.MemoryMB != 256 || !sc.SkipIfRunning || sc.NextRunAt == nil || sc.LastRun != nil || sc.Builtin || sc.Paused ||
		sc.Project != "default" || sc.Environment != "production" || sc.AppName != "whoami" {
		t.Errorf("created %+v, want the defaults, the words and a next run", sc)
	}

	t.Run("create refusals", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps?deploy=false", "application/json", `{"id": "blank"}`); status != http.StatusCreated {
			t.Fatalf("create blank = %d %s", status, raw)
		}
		tests := []struct {
			name, body  string
			status      int
			code, field string
		}{
			{"unknown app", `{"app": "ghost", "name": "x", "command": ["true"], "cron": "0 3 * * *"}`, 404, codeAppNotFound, "app"},
			{"an app not defined yet", `{"app": "blank", "name": "x", "command": ["true"], "cron": "0 3 * * *"}`, 422, codeInvalidField, "app"},
			{"the name taken", nightly, 409, codeScheduleExists, "name"},
			{"a bad name", `{"app": "whoami", "name": "Rates_Sync", "command": ["true"], "cron": "0 3 * * *"}`, 422, codeInvalidField, "name"},
			{"no command", `{"app": "whoami", "name": "x", "command": [], "cron": "0 3 * * *"}`, 422, codeInvalidField, "command"},
			{"every second", `{"app": "whoami", "name": "x", "command": ["true"], "cron": "* * * * * *"}`, 422, codeInvalidField, "cron"},
			{"never", `{"app": "whoami", "name": "x", "command": ["true"], "cron": "0 0 31 2 *"}`, 422, codeInvalidField, "cron"},
			{"an unknown service", `{"app": "whoami", "service": "db", "name": "x", "command": ["true"], "cron": "0 3 * * *"}`, 422, codeInvalidField, "service"},
			{"a limit not offered", `{"app": "whoami", "name": "x", "command": ["true"], "cron": "0 3 * * *", "stopAfterSeconds": 42}`, 422, codeInvalidField, "stopAfterSeconds"},
			{"a memory not offered", `{"app": "whoami", "name": "x", "command": ["true"], "cron": "0 3 * * *", "memoryMB": 300}`, 422, codeInvalidField, "memoryMB"},
			{"an unknown field", `{"app": "whoami", "name": "x", "command": ["true"], "cron": "0 3 * * *", "tz": "Europe/Madrid"}`, 400, codeUnknownField, "tz"},
		}
		for _, tc := range tests {
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules", "application/json", tc.body)
			wantScheduleError(t, tc.name, status, raw, tc.status, tc.code, tc.field)
			checkSchema(t, doc, raw, "Error")
		}
	})

	t.Run("get, list and filters", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID, "", "")
		if status != http.StatusOK || decodeAs[service.Schedule](t, raw).ID != sc.ID {
			t.Errorf("getSchedule = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Schedule")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/schedules/0123456789ab", "", "")
		wantScheduleError(t, "getSchedule of nothing", status, raw, 404, codeScheduleNotFound, "")

		for query, want := range map[string]int{"": 1, "?app=whoami": 1, "?project=default": 1, "?app=ghost": 0, "?project=ghost": 0} {
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules"+query, "", "")
			if status != http.StatusOK {
				t.Fatalf("listSchedules%s = %d %s", query, status, raw)
			}
			checkSchema(t, doc, raw, "ScheduleList")
			if got := decodeAs[struct{ Schedules []service.Schedule }](t, raw).Schedules; len(got) != want {
				t.Errorf("listSchedules%s has %d, want %d: %s", query, len(got), want, raw)
			}
		}
	})

	t.Run("update", func(t *testing.T) {
		body := `{"app": "whoami", "name": "rates", "command": ["true"], "cron": "15 * * * *", "stopAfterSeconds": 300, "skipIfRunning": false}`
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/schedules/"+sc.ID, "application/json", body)
		if status != http.StatusOK {
			t.Fatalf("updateSchedule = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Schedule")
		got := decodeAs[service.Schedule](t, raw)
		if got.Name != "rates" || got.Cron != "15 * * * *" || got.Words == sc.Words || got.StopAfterSeconds != 300 || got.SkipIfRunning {
			t.Errorf("updated %+v", got)
		}
		status, _, raw = call(t, http.MethodPut, e.URL+"/v1/schedules/0123456789ab", "application/json", body)
		wantScheduleError(t, "updateSchedule of nothing", status, raw, 404, codeScheduleNotFound, "")
		status, _, raw = call(t, http.MethodPut, e.URL+"/v1/schedules/"+sc.ID, "application/json", strings.Replace(body, `"15 * * * *"`, `"nope"`, 1))
		wantScheduleError(t, "updateSchedule with a bad cron", status, raw, 422, codeInvalidField, "cron")
		// Put it back for the subtests that follow.
		if status, _, raw = call(t, http.MethodPut, e.URL+"/v1/schedules/"+sc.ID, "application/json", nightly); status != http.StatusOK {
			t.Fatalf("updateSchedule back = %d %s", status, raw)
		}
	})

	t.Run("pause and resume", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/"+sc.ID+"/pause", "", "")
		checkSchema(t, doc, raw, "Schedule")
		if got := decodeAs[service.Schedule](t, raw); status != http.StatusOK || !got.Paused || got.PausedAt == nil || got.NextRunAt != nil {
			t.Errorf("pauseSchedule = %d %s, want paused with no next run", status, raw)
		}
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/schedules/"+sc.ID+"/resume", "", "")
		checkSchema(t, doc, raw, "Schedule")
		if got := decodeAs[service.Schedule](t, raw); status != http.StatusOK || got.Paused || got.NextRunAt == nil {
			t.Errorf("resumeSchedule = %d %s, want a next run", status, raw)
		}
		for _, verb := range []string{"pause", "resume"} {
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/0123456789ab/"+verb, "", "")
			wantScheduleError(t, verb+" of nothing", status, raw, 404, codeScheduleNotFound, "")
		}
	})

	t.Run("delete", func(t *testing.T) {
		other := createScheduleOK(t, e, `{"app": "whoami", "name": "gone", "command": ["true"], "cron": "0 4 * * *"}`)
		status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/schedules/"+other.ID, "", "")
		if status != http.StatusNoContent || len(raw) != 0 {
			t.Errorf("deleteSchedule = %d %s, want 204 and no body", status, raw)
		}
		status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/schedules/"+other.ID, "", "")
		wantScheduleError(t, "deleteSchedule again", status, raw, 404, codeScheduleNotFound, "")
	})
}

// T009 and T012: a run from start to end through the API, its output read
// once, and the refusals of the run routes.
func TestScheduleRunEndpoints(t *testing.T) {
	e := scheduleEnv(t)
	doc := loadContract(t)
	sc := createScheduleOK(t, e, nightly)

	run := runScheduleOK(t, e, sc.ID)
	if run.Trigger != "owner" || (run.Status != "running" && run.Status != "starting") || run.DeploymentID == nil ||
		len(run.Command) != 3 || run.FinishedAt != nil {
		t.Errorf("runSchedule answered %+v, want a going owner run of the live version", run)
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/"+sc.ID+"/run", "", "")
	wantScheduleError(t, "runSchedule while one goes", status, raw, 409, codeRunGoing, "")

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID+"/runs/"+run.ID+"/output", "", "")
	wantScheduleError(t, "output of a run going", status, raw, 404, codeOutputNotKept, "")

	finishRun(e, run, 3)
	ended := waitRun(t, e, sc.ID, run.ID)
	if ended.Status != "failed" || ended.ExitCode == nil || *ended.ExitCode != 3 || ended.Error != "exit 3" || !ended.OutputKept ||
		ended.StdoutBytes == 0 || ended.StderrBytes == 0 || ended.DurationSeconds == nil {
		t.Errorf("settled %+v, want failed with exit 3 and its output kept", ended)
	}

	t.Run("output", func(t *testing.T) {
		for stream, want := range map[string]string{"": "line one\n" + outputSentinel + "\n", "stdout": "line one\n" + outputSentinel + "\n",
			"stderr": "warn: " + outputSentinel + "\n"} {
			url := e.URL + "/v1/schedules/" + sc.ID + "/runs/" + run.ID + "/output"
			if stream != "" {
				url += "?stream=" + stream
			}
			status, header, raw := call(t, http.MethodGet, url, "", "")
			if status != http.StatusOK || string(raw) != want {
				t.Errorf("output %q = %d %q, want %q", stream, status, raw, want)
			}
			if ct := header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Errorf("output Content-Type = %q", ct)
			}
			if cc := header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("output Cache-Control = %q, want no-store", cc)
			}
			if cut := header.Get("Caravana-Output-Cut"); cut != "false" {
				t.Errorf("Caravana-Output-Cut = %q, want false", cut)
			}
		}
		status, header, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID+"/runs/"+run.ID+"/output?stream=both", "", "")
		wantScheduleError(t, "output of stream both", status, raw, 422, codeInvalidField, "stream")
		if cc := header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("a refused output read's Cache-Control = %q, want no-store", cc)
		}
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID+"/runs/0123456789ab/output", "", "")
		wantScheduleError(t, "output of no run", status, raw, 404, codeRunNotFound, "")
	})

	t.Run("runs", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID+"/runs", "", "")
		if status != http.StatusOK {
			t.Fatalf("listScheduleRuns = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "ScheduleRunList")
		if runs := decodeAs[struct{ Runs []service.ScheduleRun }](t, raw).Runs; len(runs) != 1 || runs[0].ID != run.ID {
			t.Errorf("listScheduleRuns = %s, want the one run", raw)
		}
		for _, limit := range []string{"0", "101", "x"} {
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID+"/runs?limit="+limit, "", "")
			wantScheduleError(t, "listScheduleRuns limit="+limit, status, raw, 422, codeInvalidField, "limit")
		}
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/schedules/0123456789ab/runs", "", "")
		wantScheduleError(t, "runs of no schedule", status, raw, 404, codeScheduleNotFound, "")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID+"/runs/0123456789ab", "", "")
		wantScheduleError(t, "a run that isn't", status, raw, 404, codeRunNotFound, "")
	})

	t.Run("stop", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/"+sc.ID+"/runs/"+run.ID+"/stop", "", "")
		wantScheduleError(t, "stop an ended run", status, raw, 409, codeRunNotRunning, "")

		going := runScheduleOK(t, e, sc.ID)
		e.Nomad.StartRun(deploy.ScheduleRunJobID("whoami", going.ID))
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/schedules/"+sc.ID+"/runs/"+going.ID+"/stop", "", "")
		if status != http.StatusAccepted {
			t.Fatalf("stopScheduleRun = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "ScheduleRun")
		if got := waitRun(t, e, sc.ID, going.ID); got.Status != "stopped" || got.Error != "stopped by the owner" {
			t.Errorf("stopped run settled %s %q, want stopped by the owner", got.Status, got.Error)
		}
	})

	t.Run("no live version", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps?deploy=false", "application/json",
			`{"id": "later", "source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest"}]}`); status != http.StatusCreated {
			t.Fatalf("create later = %d %s", status, raw)
		}
		later := createScheduleOK(t, e, `{"app": "later", "name": "x", "command": ["true"], "cron": "0 3 * * *"}`)
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/"+later.ID+"/run", "", "")
		wantScheduleError(t, "runSchedule of an app never deployed", status, raw, 409, codeNoLiveDeployment, "")
	})

	t.Run("a stopped app", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/stop", "", ""); status != http.StatusOK {
			t.Fatalf("stop app = %d %s", status, raw)
		}
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/"+sc.ID+"/run", "", "")
		wantScheduleError(t, "runSchedule of a stopped app", status, raw, 409, codeAppStopped, "")
	})
}

// T009: a run's output is in getScheduleRunOutput's answer and nowhere else:
// not in a schedule, a run, a list, the needs-you list, or the request log.
func TestScheduleOutputOnlyInItsRead(t *testing.T) {
	e := scheduleEnv(t)
	sc := createScheduleOK(t, e, nightly)
	run := runScheduleOK(t, e, sc.ID)
	finishRun(e, run, 3)
	waitRun(t, e, sc.ID, run.ID)

	_, _, out := call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID+"/runs/"+run.ID+"/output", "", "")
	if !strings.Contains(string(out), outputSentinel) {
		t.Fatalf("the output read = %q, want the sentinel (else this test proves nothing)", out)
	}
	for _, path := range []string{"/v1/schedules", "/v1/schedules?app=whoami", "/v1/schedules/" + sc.ID,
		"/v1/schedules/" + sc.ID + "/runs", "/v1/schedules/" + sc.ID + "/runs/" + run.ID, "/v1/attention",
		"/v1/apps/whoami", "/v1/apps", "/v1/apps/whoami/actions"} {
		status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, status, raw)
		}
		if strings.Contains(string(raw), outputSentinel) {
			t.Errorf("GET %s holds the run's output: %s", path, raw)
		}
	}
	// Refusals carry no output either.
	_, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/"+sc.ID+"/runs/"+run.ID+"/stop", "", "")
	if strings.Contains(string(raw), outputSentinel) {
		t.Errorf("a refusal holds the run's output: %s", raw)
	}
	if strings.Contains(e.Logs.String(), outputSentinel) {
		t.Errorf("the log holds the run's output:\n%s", e.Logs.String())
	}
}

// T015: the failed run is a schedule_failed item of the needs-you list, with
// the schedule's name and ID and Caravana's sentence; a later ok run clears it.
func TestScheduleAttention(t *testing.T) {
	e := scheduleEnv(t)
	doc := loadContract(t)
	sc := createScheduleOK(t, e, nightly)
	type item struct {
		Kind, AppID, Service, Schedule, ScheduleID, Detail string
	}
	items := func() []item {
		t.Helper()
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/attention", "", "")
		if status != http.StatusOK {
			t.Fatalf("listAttention = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Attention")
		return decodeAs[struct{ Items []item }](t, raw).Items
	}

	run := runScheduleOK(t, e, sc.ID)
	finishRun(e, run, 3)
	waitRun(t, e, sc.ID, run.ID)
	want := item{Kind: "schedule_failed", AppID: "whoami", Service: "web", Schedule: "rates-sync", ScheduleID: sc.ID,
		Detail: "Exited with code 3."}
	if got := items(); len(got) != 1 || got[0] != want {
		t.Errorf("items = %+v, want %+v", got, want)
	}

	run = runScheduleOK(t, e, sc.ID)
	finishRun(e, run, 0)
	waitRun(t, e, sc.ID, run.ID)
	if got := items(); len(got) != 0 {
		t.Errorf("after an ok run, items = %+v, want none", got)
	}
}

// T012: deploy tokens can't use the schedules (default deny, research R7),
// even for their own app.
func TestScheduleRoutesDenyDeployTokens(t *testing.T) {
	e := scheduleEnv(t)
	sc := createScheduleOK(t, e, nightly)
	auth := scopedToken(t, e, "default", "whoami")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/schedules", ""},
		{http.MethodPost, "/v1/schedules", strings.Replace(nightly, "rates-sync", "other", 1)},
		{http.MethodGet, "/v1/schedules/" + sc.ID, ""},
		{http.MethodPost, "/v1/schedules/" + sc.ID + "/run", ""},
		{http.MethodPost, "/v1/schedules/" + sc.ID + "/pause", ""},
		{http.MethodDelete, "/v1/schedules/" + sc.ID, ""},
	} {
		status, _, raw := callWith(t, auth, c.method, e.RawURL+c.path, "application/json", c.body)
		wantScheduleError(t, c.method+" "+c.path+" with a deploy token", status, raw, 403, codeTokenScope, "")
	}
	if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID, "", ""); status != http.StatusOK ||
		decodeAs[service.Schedule](t, raw).Paused {
		t.Errorf("after the refusals the schedule is %d %s, want it unchanged", status, raw)
	}
}

// The contract's schedule schemas have no place for output, and their
// nullable times are checked: a body with either wrong is refused, so the
// schema checks above mean something.
func TestScheduleSchemasRefuse(t *testing.T) {
	doc := loadContract(t)
	run := map[string]any{"id": "r", "scheduleId": "s", "trigger": "owner", "slotAt": nil, "status": "ok", "exitCode": 0,
		"error": "", "startedAt": "2026-10-23T03:00:00Z", "taskStartedAt": nil, "finishedAt": "2026-10-23T03:00:05Z",
		"durationSeconds": 5, "command": []any{"true"}, "deploymentId": nil, "jobVersion": nil, "outputKept": true,
		"stdoutBytes": 3, "stderrBytes": 0, "stdoutCut": false, "stderrCut": false}
	if err := doc.Components.Schemas["ScheduleRun"].Value.VisitJSON(run); err != nil {
		t.Fatalf("a good run is refused: %v", err)
	}
	for name, change := range map[string]func(m map[string]any){
		"with its output":     func(m map[string]any) { m["stdout"] = "hi" },
		"a time not a string": func(m map[string]any) { m["finishedAt"] = 5 },
		"an unknown status":   func(m map[string]any) { m["status"] = "succeeded" },
	} {
		bad := map[string]any{}
		for k, v := range run {
			bad[k] = v
		}
		change(bad)
		if err := doc.Components.Schemas["ScheduleRun"].Value.VisitJSON(bad); err == nil {
			t.Errorf("a run %s is accepted", name)
		}
	}
}
