package main

// The app-actions gate step (feature 020.5, plan.md "Lab"), and the helpers it
// and the console step share. It is in its own file only because gate_test.go
// is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// actionsWeb is the service of the app-actions steps: busybox serving one page
// on 8080, so the app has a shell, echo and an address that answers 200. It
// reserves little (see labResources); replicas is how many copies it runs.
func actionsWeb(host string, replicas int) map[string]any {
	web := map[string]any{
		"name": "web", "image": "busybox:1.37", "command": "sh",
		"args":      []string{"-c", "mkdir -p /www; echo ok > /www/index.html; exec httpd -f -p 8080 -h /www"},
		"ports":     []map[string]any{{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": labResources,
	}
	if replicas > 1 {
		web["replicas"] = replicas
	}
	if host != "" {
		web["domains"] = []map[string]any{{"host": host, "port": "http"}}
	}
	return web
}

// gateAction is an entry of listAppActions.
type gateAction struct {
	ID       string `json:"id"`
	Action   string `json:"action"`
	Trigger  string `json:"trigger"`
	Status   string `json:"status"`
	CopyID   string `json:"copyId"`
	Program  string `json:"program"`
	ExitCode *int   `json:"exitCode"`
	Error    string `json:"error"`
}

// actionList reads the app's recent actions, newest first, and the raw answer.
func (g *gate) actionList(t *testing.T, appID string) ([]gateAction, string) {
	t.Helper()
	raw := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps/"+appID+"/actions", "")
	var acts []gateAction
	if err := json.Unmarshal(raw, &acts); err != nil {
		t.Fatalf("listAppActions: %v: %s", err, raw)
	}
	return acts, string(raw)
}

// waitActionEnds waits until the action is no longer running and returns it.
func (g *gate) waitActionEnds(t *testing.T, appID, actionID string, within time.Duration) gateAction {
	t.Helper()
	var found gateAction
	if !waitUntil(within, func() bool {
		resp, err := g.apiRequest(http.MethodGet, "/v1/apps/"+appID+"/actions", "", "")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var acts []gateAction
		if json.NewDecoder(resp.Body).Decode(&acts) != nil {
			return false
		}
		for _, a := range acts {
			if a.ID == actionID && a.Status != "running" {
				found = a
				return true
			}
		}
		return false
	}) {
		t.Fatalf("action %s of %s was still running after %v", actionID, appID, within)
	}
	return found
}

// appState is the API's state of the app: healthy, stopped, ...
func (g *gate) appState(t *testing.T, appID string) string {
	t.Helper()
	var sum struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	g.getJSON(t, "/v1/apps/"+appID, &sum)
	return sum.Status.State
}

// taskShape is what a task of the job Nomad holds is made of, apart from its
// version: the image (the digest) and the env.
type taskShape struct {
	Group, Task, Image string
	Env                map[string]string
}

// jobShape reads the job's tasks and the deployment that made it.
func (g *gate) jobShape(t *testing.T, jobID string) (shape []taskShape, deployment string) {
	t.Helper()
	job, _, err := g.target.Nomad.Jobs().Info(jobID, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", jobID, err)
	}
	for _, tg := range job.TaskGroups {
		for _, tk := range tg.Tasks {
			shape = append(shape, taskShape{Group: *tg.Name, Task: tk.Name, Image: fmt.Sprint(tk.Config["image"]), Env: tk.Env})
		}
	}
	sort.Slice(shape, func(i, j int) bool { return shape[i].Group+"/"+shape[i].Task < shape[j].Group+"/"+shape[j].Task })
	return shape, job.Meta["caravana.deployment"]
}

// gateRun is the answer of runCommand.
type gateRun struct {
	ExitCode        *int   `json:"exitCode"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	TimedOut        bool   `json:"timedOut"`
	FailedToStart   bool   `json:"failedToStart"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
}

// runIn runs a command in a copy through the API and returns the answer, the
// raw body and the Cache-Control header.
func (g *gate) runIn(t *testing.T, appID, copyID string, command ...string) (gateRun, string, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"command": command})
	resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/copies/"+copyID+"/run", "application/json", string(body))
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runCommand %v = %d, want 200: %s", command, resp.StatusCode, raw)
	}
	var run gateRun
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatalf("runCommand: %v: %s", err, raw)
	}
	return run, string(raw), resp.Header.Get("Cache-Control")
}

// hostWatcher calls https://host/ every 200 ms until stopped and keeps the
// answers that weren't 200.
type hostWatcher struct {
	mu     sync.Mutex
	calls  int
	failed []string
	stop   chan struct{}
	done   sync.WaitGroup
}

// watchHost starts calling the host.
func (g *gate) watchHost(host string) *hostWatcher {
	w := &hostWatcher{stop: make(chan struct{})}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		for {
			select {
			case <-w.stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			status, _, err := g.hostStatus(host)
			w.mu.Lock()
			w.calls++
			if err != nil || status != http.StatusOK {
				w.failed = append(w.failed, fmt.Sprintf("status %d, error %v", status, err))
			}
			w.mu.Unlock()
		}
	}()
	return w
}

// end stops the calls and returns how many were made and the failed ones.
func (w *hostWatcher) end() (calls int, failed []string) {
	close(w.stop)
	w.done.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls, w.failed
}

// waitHostStatus waits for https://host/ to answer the status.
func (g *gate) waitHostStatus(t *testing.T, host string, want int, within time.Duration) {
	t.Helper()
	var status int
	var lastErr error
	if !waitUntil(within, func() bool {
		status, _, lastErr = g.hostStatus(host)
		return lastErr == nil && status == want
	}) {
		t.Fatalf("https://%s didn't answer %d within %v (status %d, error %v)", host, want, within, status, lastErr)
	}
}

// appActionsStep (feature 020.5): run a command in a copy, restart, stop and
// start an app, with the token Caravana really runs with (so a policy without
// alloc-exec fails here). A busybox app with two copies and a domain, then:
//
//  1. runCommand echoes a sentinel in a copy: exit 0, the sentinel in stdout,
//     Cache-Control no-store; sh -c 'exit 3' answers 3; a program that isn't
//     there answers failedToStart; listAppActions has the program (echo) and
//     the exit code and nothing of the output or the arguments, and the
//     sentinel is in no later answer;
//  2. restartApp while the domain is called every 200 ms: no call fails, the
//     action ends succeeded within 3 minutes, and the needs-you list has
//     nothing for the app (a requested restart is no crash loop);
//  3. stopApp: the domain answers 404 within 30 s, no copy runs, the app's
//     state is stopped, and the webhook, deployApp and a second stop are
//     409 app_stopped;
//  4. startApp: the domain answers 200 within a minute, the job's images (the
//     digests) and env are what they were before the stop, and the app is
//     healthy again;
//  5. the app is deleted.
func (g *gate) appActionsStep(t *testing.T) {
	const id = "lab0205"
	host := g.extraHost(id)
	sentinel := "s3cret-sentinel-0205-" + g.run

	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // leftovers from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	t.Cleanup(remove)

	// Deploy: two copies, answering.
	body := labApp(actionsWeb(host, 2))
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", body)
	var first gateDeployment
	if err := json.Unmarshal(raw, &first); err != nil || first.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, first.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	g.waitHostAnswers(t, host)
	var copies []string
	waitFor(t, "two running copies of "+id, time.Minute, func() bool {
		copies = g.runningCopies(t, id)
		return len(copies) == 2
	})
	var later []string // every answer after the sentinel was printed

	// 1. Run a command.
	run, rawRun, cache := g.runIn(t, id, copies[0], "echo", sentinel)
	if run.ExitCode == nil || *run.ExitCode != 0 || strings.TrimSpace(run.Stdout) != sentinel || run.TimedOut || run.FailedToStart {
		t.Fatalf("runCommand echo = %s, want exit 0 with the sentinel on stdout", rawRun)
	}
	if cache != "no-store" {
		t.Errorf("runCommand's Cache-Control = %q, want no-store", cache)
	}
	acts, rawActs := g.actionList(t, id)
	if len(acts) != 1 || acts[0].Action != "run" || acts[0].Program != "echo" || acts[0].ExitCode == nil || *acts[0].ExitCode != 0 ||
		acts[0].CopyID != copies[0] || acts[0].Trigger != "manual" || acts[0].Status != "succeeded" {
		t.Fatalf("listAppActions = %s, want one succeeded run of echo with exit 0 in %s", rawActs, copies[0])
	}
	later = append(later, rawActs)

	if run, rawRun, _ = g.runIn(t, id, copies[1], "sh", "-c", "exit 3"); run.ExitCode == nil || *run.ExitCode != 3 {
		t.Errorf("runCommand sh -c 'exit 3' = %s, want exit 3", rawRun)
	}
	if run, rawRun, _ = g.runIn(t, id, copies[0], "caravana-no-such-program"); !run.FailedToStart || run.ExitCode != nil {
		t.Errorf("runCommand of a program that isn't there = %s, want failedToStart", rawRun)
	}
	g.refusal(t, http.StatusNotFound, "not_found", "", http.MethodPost, "/v1/apps/"+id+"/copies/00000000-0000-0000-0000-000000000000/run", `{"command":["true"]}`)
	g.refusal(t, http.StatusBadRequest, "invalid_field", "command", http.MethodPost, "/v1/apps/"+id+"/copies/"+copies[0]+"/run", `{"command":[]}`)
	for _, p := range []string{"/v1/apps/" + id, "/v1/apps/" + id + "/copies", "/v1/apps", "/v1/attention", "/v1/apps/" + id + "/deployments"} {
		later = append(later, string(g.sendWant(t, http.StatusOK, http.MethodGet, p, "")))
	}

	// 2. Restart with the domain under watch.
	g.waitHostAnswers(t, host)
	watcher := g.watchHost(host)
	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/restart", "")
	var restart gateAction
	if err := json.Unmarshal(raw, &restart); err != nil || restart.ID == "" || restart.Action != "restart" {
		t.Fatalf("restartApp = %s, want a restart action", raw)
	}
	ended := g.waitActionEnds(t, id, restart.ID, 3*time.Minute)
	calls, failed := watcher.end()
	if ended.Status != "succeeded" {
		t.Errorf("the restart ended %s (%s), want succeeded", ended.Status, ended.Error)
	}
	if calls < 5 {
		t.Errorf("the domain was called %d times during the restart, want it watched throughout", calls)
	}
	if len(failed) > 0 {
		t.Errorf("%d of %d calls to %s failed during the restart (the first: %s): copies must restart one at a time", len(failed), calls, host, failed[0])
	}
	var list struct {
		Items []attentionItem `json:"items"`
	}
	g.getJSON(t, "/v1/attention", &list)
	for _, it := range list.Items {
		if it.AppID == id {
			t.Errorf("the needs-you list has an item for %s after a requested restart: %+v", id, it)
		}
	}
	g.waitHostAnswers(t, host)

	// 3. Stop.
	token := g.webhookTokenOf(t, id)
	before, deployment := g.jobShape(t, id)
	raw = g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+id+"/stop", "")
	var stopped struct {
		StoppedAt *string `json:"stoppedAt"`
		Status    struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &stopped); err != nil || stopped.StoppedAt == nil || stopped.Status.State != "stopped" {
		t.Fatalf("stopApp = %s, want the app stopped with stoppedAt", raw)
	}
	g.waitHostStatus(t, host, http.StatusNotFound, 30*time.Second)
	waitFor(t, "no copy of "+id+" to run", 30*time.Second, func() bool { return len(g.runningCopies(t, id)) == 0 })
	if state := g.appState(t, id); state != "stopped" {
		t.Errorf("GET /v1/apps/%s says %q after the stop, want stopped", id, state)
	}
	if code, hookCode := g.hookOf(t, id, token); code != http.StatusConflict || hookCode != "app_stopped" {
		t.Errorf("the webhook of a stopped app = %d %s, want 409 app_stopped", code, hookCode)
	}
	g.refusal(t, http.StatusConflict, "app_stopped", "", http.MethodPost, "/v1/apps/"+id+"/deployments", body)
	g.refusal(t, http.StatusConflict, "app_stopped", "", http.MethodPost, "/v1/apps/"+id+"/stop", "")
	g.refusal(t, http.StatusConflict, "app_stopped", "", http.MethodPost, "/v1/apps/"+id+"/restart", "")
	if n := g.deploymentsOf(t, id); n != 1 {
		t.Errorf("%s has %d deployments after refused deploys, want the first only", id, n)
	}
	later = append(later, string(g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps", "")))

	// 4. Start.
	g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+id+"/start", "")
	g.waitHostStatus(t, host, http.StatusOK, time.Minute)
	after, deploymentAfter := g.jobShape(t, id)
	if !reflect.DeepEqual(before, after) || deployment != deploymentAfter {
		t.Errorf("the job after Start (deployment %s) = %+v, want what it was before the stop (deployment %s) = %+v", deploymentAfter, after, deployment, before)
	}
	waitFor(t, id+" to be healthy again", 2*time.Minute, func() bool { return g.appState(t, id) == "healthy" })
	g.refusal(t, http.StatusConflict, "app_not_stopped", "", http.MethodPost, "/v1/apps/"+id+"/start", "")
	if n := g.deploymentsOf(t, id); n != 1 {
		t.Errorf("%s has %d deployments after stop and start, want 1: neither is a deploy", id, n)
	}

	acts, rawActs = g.actionList(t, id)
	later = append(later, rawActs)
	kinds := map[string]int{}
	for _, a := range acts {
		kinds[a.Action]++
	}
	if kinds["run"] != 3 || kinds["restart"] != 1 || kinds["stop"] != 1 || kinds["start"] != 1 {
		t.Errorf("listAppActions = %s, want three runs, a restart, a stop and a start", rawActs)
	}
	for _, a := range later {
		if strings.Contains(a, sentinel) {
			t.Errorf("an API answer after the command holds its output: %.300s", a)
		}
	}

	// 5. Delete.
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
}

// webhookTokenOf makes the app's webhook token.
func (g *gate) webhookTokenOf(t *testing.T, appID string) string {
	t.Helper()
	raw := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/apps/"+appID+"/webhook", "")
	var created struct{ Token string }
	if err := json.Unmarshal(raw, &created); err != nil || created.Token == "" {
		t.Fatalf("create the webhook token of %s: %s", appID, raw)
	}
	return created.Token
}

// hookOf calls the app's webhook with the token and returns the status and the
// error code of the answer.
func (g *gate) hookOf(t *testing.T, appID, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, g.target.Webhook+"/v1/webhooks/"+appID, strings.NewReader(`{"push_data":{"tag":"main"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Error.Code
}

// deploymentsOf is how many deployments the API lists for the app.
func (g *gate) deploymentsOf(t *testing.T, appID string) int {
	t.Helper()
	var list struct {
		Deployments []gateDeployment `json:"deployments"`
	}
	g.getJSON(t, "/v1/apps/"+appID+"/deployments", &list)
	return len(list.Deployments)
}
