package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/service"
)

const (
	cmdSentinel    = "s3cret-sentinel-0205"
	cmdArgSentinel = "s3cret-arg-0205"
)

// deployedWithCopies deploys whoami, waits until it is healthy and gives it two
// running copies, c1 and c2.
func deployedWithCopies(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, "")
	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 0},
		api.AllocationListStub{ID: "c2", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
	e.Nomad.SetRestartStop(0)
	return e
}

// US1: runCommand answers with the command's output and exit code, says it
// must not be cached, and the output is in no later answer or log line.
func TestRunCommand(t *testing.T) {
	e := deployedWithCopies(t)
	doc := loadContract(t)
	e.Nomad.OnExec(func(r deploytest.ExecRequest) deploytest.ExecReply {
		switch r.Command[0] {
		case "nope":
			return deploytest.ExecReply{Fail: "exec: nope: not found"}
		case "big":
			return deploytest.ExecReply{Stdout: strings.Repeat("x", 70<<10)}
		}
		return deploytest.ExecReply{Stdout: cmdSentinel + "\n", Stderr: "warn\n", ExitCode: 3}
	})
	run := func(copyID, body string) (int, http.Header, []byte) {
		return call(t, http.MethodPost, e.URL+"/v1/apps/whoami/copies/"+copyID+"/run", "application/json", body)
	}

	t.Run("output, exit code, no-store", func(t *testing.T) {
		status, header, raw := run("c1", `{"command": ["echo", "--password=`+cmdArgSentinel+`"]}`)
		if status != http.StatusOK {
			t.Fatalf("run = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "CommandResult")
		if cc := header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", cc)
		}
		got := decodeAs[struct {
			ExitCode *int   `json:"exitCode"`
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
		}](t, raw)
		if got.ExitCode == nil || *got.ExitCode != 3 || got.Stdout != cmdSentinel+"\n" || got.Stderr != "warn\n" {
			t.Errorf("answer = %s", raw)
		}
	})

	t.Run("a long output is cut and says so", func(t *testing.T) {
		_, _, raw := run("c2", `{"command": ["big"]}`)
		checkSchema(t, doc, raw, "CommandResult")
		got := decodeAs[struct {
			Stdout          string `json:"stdout"`
			StdoutTruncated bool   `json:"stdoutTruncated"`
		}](t, raw)
		if len(got.Stdout) != 64<<10 || !got.StdoutTruncated {
			t.Errorf("stdout = %d bytes, truncated %v; want 65536, true", len(got.Stdout), got.StdoutTruncated)
		}
	})

	t.Run("a program the image doesn't have is a 200 saying so", func(t *testing.T) {
		status, _, raw := run("c1", `{"command": ["nope"]}`)
		checkSchema(t, doc, raw, "CommandResult")
		got := decodeAs[struct {
			ExitCode      *int   `json:"exitCode"`
			Stderr        string `json:"stderr"`
			FailedToStart bool   `json:"failedToStart"`
		}](t, raw)
		if status != http.StatusOK || !got.FailedToStart || got.ExitCode != nil || !strings.Contains(got.Stderr, "not found") {
			t.Errorf("answer = %d %s", status, raw)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		tests := []struct {
			name, copyID, contentType, body string
			status                          int
			code, field                     string
		}{
			{"no command", "c1", "application/json", `{"command": []}`, 400, "invalid_field", "command"},
			{"an empty program", "c1", "application/json", `{"command": [""]}`, 400, "invalid_field", "command"},
			{"too many items", "c1", "application/json", `{"command": [` + strings.TrimSuffix(strings.Repeat(`"x",`, 65), ",") + `]}`, 400, "invalid_field", "command"},
			{"a NUL byte", "c1", "application/json", `{"command": ["a\u0000b"]}`, 400, "invalid_field", "command"},
			{"another field", "c1", "application/json", `{"command": ["true"], "tty": true}`, 400, "unknown_field", "tty"},
			{"no body", "c1", "application/json", ``, 400, "bad_request", ""},
			{"not JSON", "c1", "text/plain", `true`, 415, "unsupported_media_type", ""},
			{"not the app's copy", "other", "application/json", `{"command": ["true"]}`, 404, "not_found", ""},
		}
		for _, tt := range tests {
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/copies/"+tt.copyID+"/run", tt.contentType, tt.body)
			got := decodeAs[errorBody](t, raw)
			if status != tt.status || got.Error.Code != tt.code || got.Error.Field != tt.field {
				t.Errorf("%s: %d %s, want %d %s field %q", tt.name, status, raw, tt.status, tt.code, tt.field)
			}
			checkSchema(t, doc, raw, "Error")
		}
		if status, _, _ := call(t, http.MethodPost, e.URL+"/v1/apps/ghost/copies/c1/run", "application/json", `{"command": ["true"]}`); status != http.StatusNotFound {
			t.Errorf("run in an unknown app = %d, want 404", status)
		}

		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "complete", JobVersion: 0})
		status, _, raw := run("c1", `{"command": ["true"]}`)
		if got := decodeAs[errorBody](t, raw); status != http.StatusConflict || got.Error.Code != "copy_not_running" {
			t.Errorf("run in an ended copy = %d %s, want 409 copy_not_running", status, raw)
		}
	})

	t.Run("the output and arguments are in no later answer and no log line", func(t *testing.T) {
		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		e.Nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: cmdSentinel, Stderr: cmdSentinel}
		})
		run("c1", `{"command": ["echo", "`+cmdArgSentinel+`"]}`)

		var seen []string
		for _, p := range []string{"/v1/apps/whoami/actions", "/v1/apps/whoami", "/v1/apps", "/v1/apps/whoami/copies",
			"/v1/apps/whoami/deployments", "/v1/attention", "/v1/deployments", "/v1/apps/whoami/volumes", "/v1/apps/whoami/backups"} {
			_, _, raw := call(t, http.MethodGet, e.URL+p, "", "")
			seen = append(seen, p+" "+string(raw))
		}
		seen = append(seen, "the log: "+e.Logs.String())
		for _, s := range seen {
			for _, sentinel := range []string{cmdSentinel, cmdArgSentinel} {
				if strings.Contains(s, sentinel) {
					head, _, _ := strings.Cut(s, " ")
					t.Errorf("%s holds %q", head, sentinel)
				}
			}
		}

		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/actions", "", "")
		acts := decodeAs[[]struct {
			Action   string `json:"action"`
			Program  string `json:"program"`
			Status   string `json:"status"`
			Trigger  string `json:"trigger"`
			ExitCode *int   `json:"exitCode"`
			CopyID   string `json:"copyId"`
		}](t, raw)
		if len(acts) == 0 || acts[0].Action != "run" || acts[0].Program != "echo" || acts[0].Trigger != "manual" ||
			acts[0].ExitCode == nil || *acts[0].ExitCode != 0 || acts[0].CopyID != "c1" {
			t.Errorf("newest action = %+v, want echo run in c1 through the API, exit 0", acts)
		}
	})
}

// listRaw is the body of a GET that must answer 200.
func listRaw(t *testing.T, url string) []byte {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, url, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d %s", url, status, raw)
	}
	return raw
}

// US2: stop and start through the API.
func TestStopStartApp(t *testing.T) {
	e := deployedWithCopies(t)
	doc := loadContract(t)
	_, _, tokRaw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/webhook", "", "")
	token := decodeAs[struct {
		Token string `json:"token"`
	}](t, tokRaw).Token

	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/stop", "", "")
	if status != http.StatusOK {
		t.Fatalf("stop = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "AppSummary")
	got := decodeAs[struct {
		StoppedAt *time.Time `json:"stoppedAt"`
		Status    struct {
			State string `json:"state"`
		} `json:"status"`
	}](t, raw)
	if got.Status.State != "stopped" || got.StoppedAt == nil {
		t.Errorf("stop answered %s, want state stopped with stoppedAt", raw)
	}
	if job := e.Nomad.Job("whoami"); job == nil || !*job.Stop {
		t.Errorf("Nomad's job after stop = %+v, want it stopped, not purged", job)
	}

	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	checkSchema(t, doc, raw, "AppRecord")
	if rec := decodeAs[struct {
		StoppedAt *time.Time `json:"stoppedAt"`
	}](t, raw); rec.StoppedAt == nil {
		t.Errorf("getApp = %s, want stoppedAt", raw)
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps", "", "")
	checkSchema(t, doc, raw, "AppSummaryList")

	refused := func(what, method, path, body string, wantCode string) {
		t.Helper()
		ct := ""
		if body != "" {
			ct = "application/json"
		}
		status, header, raw := call(t, method, e.URL+path, ct, body)
		if got := decodeAs[errorBody](t, raw); status != http.StatusConflict || got.Error.Code != wantCode {
			t.Errorf("%s = %d %s, want 409 %s", what, status, raw, wantCode)
		}
		if loc := header.Get("Location"); loc != "" {
			t.Errorf("%s has Location %q: nothing was recorded", what, loc)
		}
		checkSchema(t, doc, raw, "Error")
	}
	regs := e.Nomad.Registrations()
	refused("deployApp", http.MethodPost, "/v1/apps/whoami/deployments", whoamiBody, "app_stopped")
	refused("redeploy as stored", http.MethodPost, "/v1/apps/whoami/deployments", "{}", "app_stopped")
	status, _, raw = hook(t, e.URL, "whoami", token, "", "", "")
	if got := decodeAs[errorBody](t, raw); status != http.StatusConflict || got.Error.Code != "app_stopped" {
		t.Errorf("webhook = %d %s, want 409 app_stopped", status, raw)
	}
	refused("stop again", http.MethodPost, "/v1/apps/whoami/stop", "", "app_stopped")
	refused("restart", http.MethodPost, "/v1/apps/whoami/restart", "", "app_stopped")
	if got := e.Nomad.Registrations(); got != regs {
		t.Errorf("registrations = %d, want %d: a refused deploy reached Nomad", got, regs)
	}
	deps := decodeAs[struct {
		Deployments []struct{ ID string } `json:"deployments"`
	}](t, listRaw(t, e.URL+"/v1/apps/whoami/deployments"))
	if len(deps.Deployments) != 1 {
		t.Errorf("deployments = %d, want the one: refusals record nothing", len(deps.Deployments))
	}

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/start", "", "")
	if status != http.StatusOK {
		t.Fatalf("start = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "AppSummary")
	if sum := decodeAs[struct {
		StoppedAt *time.Time `json:"stoppedAt"`
	}](t, raw); sum.StoppedAt != nil {
		t.Errorf("start answered %s, want no stoppedAt", raw)
	}
	refused("start again", http.MethodPost, "/v1/apps/whoami/start", "", "app_not_stopped")

	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/actions", "", "")
	acts := decodeAs[[]struct {
		Action string `json:"action"`
		Status string `json:"status"`
	}](t, raw)
	if len(acts) != 2 || acts[0].Action != "start" || acts[1].Action != "stop" || acts[0].Status != "succeeded" {
		t.Errorf("actions = %s, want start, then stop", raw)
	}
	for _, item := range decodeAs[[]json.RawMessage](t, raw) {
		checkSchema(t, doc, item, "AppAction")
	}

	for _, path := range []string{"/v1/apps/ghost/stop", "/v1/apps/ghost/start", "/v1/apps/ghost/restart", "/v1/apps/ghost/actions"} {
		method := http.MethodPost
		if strings.HasSuffix(path, "actions") {
			method = http.MethodGet
		}
		if status, _, _ := call(t, method, e.URL+path, "", ""); status != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", method, path, status)
		}
	}
}

// US3: restart answers 202 with the action, which ends succeeded.
func TestRestartApp(t *testing.T) {
	e := deployedWithCopies(t)
	doc := loadContract(t)

	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/restart", "", "")
	if status != http.StatusAccepted {
		t.Fatalf("restart = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "AppAction")
	act := decodeAs[struct {
		ID, Action, Status, Trigger string
	}](t, raw)
	if act.Action != "restart" || act.Status != "running" || act.Trigger != "manual" {
		t.Errorf("answer = %s", raw)
	}
	waitUntil(t, func() bool {
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/actions", "", "")
		a := decodeAs[[]struct{ ID, Status string }](t, raw)
		return len(a) == 1 && a[0].ID == act.ID && a[0].Status == "succeeded"
	})
	if rs := e.Nomad.Restarts(); len(rs) != 2 {
		t.Errorf("restarts = %+v, want both copies", rs)
	}

	e.Nomad.SetCopies("whoami")
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/restart", "", "")
	if got := decodeAs[errorBody](t, raw); status != http.StatusConflict || got.Error.Code != "no_running_copy" {
		t.Errorf("restart without copies = %d %s, want 409 no_running_copy", status, raw)
	}
}

// An app that was never deployed has nothing to stop, start or restart.
func TestActionsOfAnUndeployedApp(t *testing.T) {
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps?deploy=false", "application/json", strings.Replace(whoamiBody, "{", `{"id": "whoami",`, 1)); status != http.StatusCreated {
		t.Fatalf("create = %d %s", status, raw)
	}
	for _, op := range []string{"stop", "start", "restart"} {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/"+op, "", "")
		if got := decodeAs[errorBody](t, raw); status != http.StatusConflict || got.Error.Code != "no_live_deployment" {
			t.Errorf("%s = %d %s, want 409 no_live_deployment", op, status, raw)
		}
	}
}

func TestActionErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrAppStopped, 409, "app_stopped"},
		{service.ErrAppNotStopped, 409, "app_not_stopped"},
		{service.ErrAppBusy, 409, "app_busy"},
		{service.ErrCopyNotRunning, 409, "copy_not_running"},
		{errors.New("other"), 0, ""},
	}
	for _, tt := range tests {
		e := actionError(tt.err)
		if tt.code == "" {
			if e != nil {
				t.Errorf("actionError(%v) = %+v, want nil", tt.err, e)
			}
			continue
		}
		if e == nil || e.Status != tt.status || e.Code != tt.code {
			t.Errorf("actionError(%v) = %+v, want %d %s", tt.err, e, tt.status, tt.code)
		}
	}
}
