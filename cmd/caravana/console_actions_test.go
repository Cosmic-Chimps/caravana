package main

// The console-app-actions step of the lab test (feature 020.5, plan.md "Lab"):
// Run a command, Stop, Start and Restart through the console's forms. It is in
// its own file only because console_lab_test.go is long; runConsole lists it
// before console-delete.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// appActions: on a small busybox app (one copy, no domain),
//
//  1. the Running tab has a form for the copy; its post answers 200, no-store,
//     with the output (a sentinel) and exit code 0; a reload of the tab, the
//     recent actions and the API's list don't have the sentinel, and the list
//     says "Run echo";
//  2. Stop's page says what stops; the post is 303 to the app with the notice;
//     the app shows as stopped (the page, the list and the API); a deploy form
//     post is 409 saying to start the app first;
//  3. Start is 303 and the app is healthy again;
//  4. Restart is 303 with its notice and the action ends succeeded;
//  5. the app is deleted through the console.
//
// Budget: one deploy, a start and a restart, each waited for within minutes.
func (c *console) appActions(t *testing.T) {
	const id = "con0205"
	sentinel := "s3cret-sentinel-0205-" + c.g.run

	remove := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	t.Cleanup(remove)

	raw := c.g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(actionsWeb("", 1)))
	d := gateDeployment{}
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := c.g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	var copies []string
	waitFor(t, "a running copy of "+id, time.Minute, func() bool {
		copies = c.g.runningCopies(t, id)
		return len(copies) == 1
	})

	// 1. Run a command.
	_, body := c.get(t, "/apps/"+id)
	runPath := "/apps/" + id + "/copies/" + copies[0] + "/run"
	if !strings.Contains(body, `action="`+runPath+`"`) {
		t.Fatalf("GET /apps/%s has no run form for copy %s:\n%s", id, copies[0], pageText(body))
	}
	resp, body := c.post(t, runPath, url.Values{"command": {"echo " + sentinel}})
	got := pageText(body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" ||
		!strings.Contains(got, sentinel) || !strings.Contains(got, "exit code 0") || !strings.Contains(got, "This output is shown once; it isn't kept.") {
		t.Fatalf("POST %s = %d (Cache-Control %q), want 200 no-store showing the output and exit code 0 once:\n%s",
			runPath, resp.StatusCode, resp.Header.Get("Cache-Control"), got)
	}
	for _, p := range []string{"/apps/" + id, "/parts/apps/" + id + "/actions", "/parts/apps/" + id + "/copies", "/apps"} {
		if _, body = c.get(t, p); strings.Contains(body, sentinel) {
			t.Errorf("GET %s has the command's output after the answer that showed it", p)
		}
	}
	if _, body = c.get(t, "/parts/apps/"+id+"/actions"); !strings.Contains(pageText(body), "Run echo") || !strings.Contains(pageText(body), "exit 0") {
		t.Errorf("the recent actions = %s, want the run of echo with exit 0", pageText(body))
	}
	if _, rawActs := c.g.actionList(t, id); strings.Contains(rawActs, sentinel) {
		t.Errorf("listAppActions has the output: %s", rawActs)
	}
	resp, body = c.post(t, runPath, url.Values{"command": {`echo "unclosed`}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(pageText(body), "quote isn't closed") {
		t.Errorf("POST a line with an unclosed quote = %d, want 422 saying so: %s", resp.StatusCode, pageText(body))
	}

	// 2. Stop.
	resp, body = c.get(t, "/apps/"+id+"/stop")
	if got := pageText(body); resp.StatusCode != http.StatusOK || !strings.Contains(got, "Nothing is deleted") || !strings.Contains(got, "the app stops answering") {
		t.Fatalf("GET the Stop page = %d, want what stops and what stays:\n%s", resp.StatusCode, got)
	}
	resp, body = c.post(t, "/apps/"+id+"/stop", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+id+"?did=stop" {
		t.Fatalf("POST stop = %d → %q, want 303 to /apps/%s?did=stop: %s", resp.StatusCode, resp.Header.Get("Location"), id, pageText(body))
	}
	resp, body = c.get(t, "/apps/"+id+"?did=stop")
	got = pageText(body)
	for _, want := range []string{"Stopped. Nothing was deleted", "stopped", "Start"} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(got, want) {
			t.Errorf("the page after Stop lacks %q: %s", want, got)
		}
	}
	if state := c.g.appState(t, id); state != "stopped" {
		t.Errorf("GET /v1/apps/%s says %q after the console's Stop, want stopped", id, state)
	}
	waitFor(t, "no copy of "+id+" to run", 30*time.Second, func() bool { return len(c.g.runningCopies(t, id)) == 0 })
	if _, body = c.get(t, "/apps"); !strings.Contains(pageText(body), "stopped") {
		t.Errorf("the Apps page doesn't show %s as stopped: %s", id, pageText(body))
	}
	resp, body = c.post(t, "/apps/"+id+"/deploy", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(pageText(body), "The app is stopped: start it first.") {
		t.Errorf("POST deploy of a stopped app = %d, want 409 saying to start it first: %s", resp.StatusCode, pageText(body))
	}

	// 3. Start.
	resp, body = c.post(t, "/apps/"+id+"/start", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+id+"?did=start" {
		t.Fatalf("POST start = %d → %q, want 303 to /apps/%s?did=start: %s", resp.StatusCode, resp.Header.Get("Location"), id, pageText(body))
	}
	waitFor(t, id+" to be healthy again", 3*time.Minute, func() bool { return c.g.appState(t, id) == "healthy" })
	if _, body = c.get(t, "/apps/"+id+"?did=start"); !strings.Contains(pageText(body), "Started. It runs the same images and settings") {
		t.Errorf("the page after Start lacks the notice: %s", pageText(body))
	}

	// 4. Restart.
	resp, body = c.post(t, "/apps/"+id+"/restart", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+id+"?did=restart" {
		t.Fatalf("POST restart = %d → %q, want 303 to /apps/%s?did=restart: %s", resp.StatusCode, resp.Header.Get("Location"), id, pageText(body))
	}
	if _, body = c.get(t, "/apps/"+id+"?did=restart"); !strings.Contains(pageText(body), "Restarting: its copies restart one at a time") {
		t.Errorf("the page after Restart lacks the notice: %s", pageText(body))
	}
	acts, rawActs := c.g.actionList(t, id)
	if len(acts) == 0 || acts[0].Action != "restart" {
		t.Fatalf("listAppActions = %s, want the restart newest", rawActs)
	}
	if ended := c.g.waitActionEnds(t, id, acts[0].ID, 3*time.Minute); ended.Status != "succeeded" || ended.Trigger != "console" {
		t.Errorf("the restart = %+v, want succeeded from the console", ended)
	}
	waitFor(t, id+" to be healthy after the restart", 2*time.Minute, func() bool { return c.g.appState(t, id) == "healthy" })

	// 5. Delete through the console.
	resp, body = c.post(t, "/apps/"+id+"/delete", url.Values{"confirm": {id}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST the delete of %s = %d: %s", id, resp.StatusCode, body)
	}
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
}
