package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// copySentinel is what the commands of a schedule that runs in the running
// copy print. They are read and dropped: no answer, page or log line holds it.
const copySentinel = "s3cret-sentinel-040-output"

const copyNightly = `{"app": "whoami", "name": "purge", "command": ["sh", "-c", "echo hi"], "cron": "0 3 * * *", "target": "copy"}`

// Feature 040: a schedule that runs in the running copy, through the API.
func TestCopyScheduleEndpoints(t *testing.T) {
	e := scheduleEnv(t)
	doc := loadContract(t)
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "c2", TaskGroup: "web", ClientStatus: "running", CreateTime: 2000},
		api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", CreateTime: 1000})
	e.Nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
		return deploytest.ExecReply{Stdout: copySentinel, Stderr: copySentinel, ExitCode: 3}
	})

	sc := createScheduleOK(t, e, strings.Replace(copyNightly, `"target": "copy"`, `"target": "copy", "allCopies": true`, 1))
	if sc.Target != "copy" || !sc.AllCopies || sc.StopAfterSeconds != 60 || sc.MemoryMB != 0 || !sc.SkipIfRunning {
		t.Errorf("created %+v, want a copy schedule in every copy with a 60 s limit and no memory", sc)
	}
	if plain := createScheduleOK(t, e, nightly); plain.Target != "fresh" || plain.AllCopies {
		t.Errorf("a schedule with no target = %+v, want fresh", plain)
	}

	t.Run("refusals name their field", func(t *testing.T) {
		for _, tc := range []struct{ name, body, field string }{
			{"every copy of a new copy", `"target": "fresh", "allCopies": true`, "allCopies"},
			{"a target that isn't one", `"target": "both"`, "target"},
			{"a fresh limit", `"target": "copy", "stopAfterSeconds": 1800`, "stopAfterSeconds"},
			{"memory", `"target": "copy", "memoryMB": 256`, "memoryMB"},
			{"overlap", `"target": "copy", "skipIfRunning": false`, "skipIfRunning"},
		} {
			body := strings.Replace(copyNightly, `"target": "copy"`, tc.body, 1)
			body = strings.Replace(body, `"name": "purge"`, `"name": "x"`, 1)
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules", "application/json", body)
			wantScheduleError(t, tc.name, status, raw, 422, codeInvalidField, tc.field)
			checkSchema(t, doc, raw, "Error")
		}
	})

	t.Run("a run in the copies keeps no output", func(t *testing.T) {
		run := runScheduleOK(t, e, sc.ID)
		if run.Target != "copy" || len(run.Copies) != 2 || run.Copies[0] != "c1" || run.Copies[1] != "c2" {
			t.Errorf("started %+v, want a copy run in c1 then c2", run)
		}
		done := waitRun(t, e, sc.ID, run.ID)
		if done.Status != "failed" || done.Error != "exit 3 in copy c1" || done.ExitCode == nil || *done.ExitCode != 3 ||
			done.OutputKept || done.DeploymentID != nil {
			t.Errorf("run = %+v", done)
		}

		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+sc.ID+"/runs/"+run.ID+"/output", "", "")
		wantScheduleError(t, "the output of a copy run", status, raw, 404, codeOutputNotKept, "")

		for _, path := range []string{"/v1/schedules", "/v1/schedules/" + sc.ID, "/v1/schedules/" + sc.ID + "/runs",
			"/v1/schedules/" + sc.ID + "/runs/" + run.ID, "/v1/attention", "/v1/apps/whoami", "/v1/apps/whoami/actions", "/v1/activity"} {
			status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
			if status != http.StatusOK {
				t.Errorf("GET %s = %d %s", path, status, raw)
			}
			if strings.Contains(string(raw), copySentinel) {
				t.Errorf("GET %s holds the command's output: %s", path, raw)
			}
		}
		if strings.Contains(e.Logs.String(), copySentinel) {
			t.Errorf("the log holds the command's output:\n%s", e.Logs.String())
		}

		// The run's action rows, one per copy, say a schedule asked for them.
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/actions", "", "")
		for _, item := range decodeAs[[]json.RawMessage](t, raw) {
			checkSchema(t, doc, item, "AppAction")
		}
		acts := decodeAs[[]struct {
			Action, Program, Trigger string
			ExitCode                 *int
		}](t, raw)
		n := 0
		for _, a := range acts {
			if a.Trigger == "schedule" {
				n++
				if a.Action != "run" || a.Program != "sh" || a.ExitCode == nil || *a.ExitCode != 3 {
					t.Errorf("action = %+v", a)
				}
			}
		}
		if n != 2 {
			t.Errorf("%d actions with trigger schedule, want 2: %s", n, raw)
		}

		// The failure is on the needs-you list, with the code and no output.
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/attention", "", "")
		items := decodeAs[struct {
			Items []struct{ Kind, Detail string }
		}](t, raw).Items
		if len(items) != 1 || items[0].Kind != "schedule_failed" || items[0].Detail != "Exited with code 3." {
			t.Errorf("items = %+v", items)
		}
	})

	t.Run("no healthy copy: Run now is 409 no_running_copy", func(t *testing.T) {
		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "pending"})
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/schedules/"+sc.ID+"/run", "", "")
		wantScheduleError(t, "runSchedule", status, raw, 409, codeNoRunningCopy, "")
	})

	t.Run("a schedule can move between targets", func(t *testing.T) {
		body := strings.Replace(copyNightly, `"target": "copy"`, `"target": "fresh"`, 1)
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/schedules/"+sc.ID, "application/json", body)
		if got := decodeAs[service.Schedule](t, raw); status != http.StatusOK || got.Target != "fresh" || got.StopAfterSeconds != 1800 {
			t.Errorf("updateSchedule = %d %s", status, raw)
		}
	})
}
