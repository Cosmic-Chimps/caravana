package main

// The console-terminal step of the lab test (feature 020.6, plan.md "Lab"): the
// terminal page and the websocket it connects to, with a signed-in browser's
// cookie. It is in its own file only because console_lab_test.go is long;
// runConsole lists it before console-delete.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// terminalCSPWant is the policy of the terminal page alone (internal/ui/terminal.go):
// styles may be inline for xterm.js, scripts stay 'self'.
const terminalCSPWant = "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'"

// terminal: on a small busybox app (one copy, no domain),
//
//  1. the terminal page is 200 with its own Content-Security-Policy, the
//     websocket's address as data-socket, and every script and stylesheet it
//     names loads (200, the vendored xterm.js among them);
//  2. the websocket with the browser's cookie and the console's own Origin
//     opens (101) and runs a command (`echo $((6*7))` prints 42); the app's
//     actions say it came from the console;
//  3. the same request with a foreign Origin is 403 cross_origin, and so is
//     one with no Origin: nothing opens;
//  4. the app is deleted.
func (c *console) terminal(t *testing.T) {
	const id = "con0206"

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
	copyID := copies[0]

	// 1. The page and its assets.
	page := "/apps/" + id + "/copies/" + copyID + "/terminal"
	socket := "/v1/apps/" + id + "/copies/" + copyID + "/terminal"
	resp, body := c.get(t, page)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200:\n%s", page, resp.StatusCode, pageText(body))
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != terminalCSPWant {
		t.Errorf("the terminal page's Content-Security-Policy = %q, want %q", got, terminalCSPWant)
	}
	if !strings.Contains(body, `data-socket="`+socket+`"`) {
		t.Errorf("the terminal page doesn't name its socket %s", socket)
	}
	assets := regexp.MustCompile(`(?:href|src)="(/static/[^"]+)"`).FindAllStringSubmatch(body, -1)
	have := map[string]bool{}
	for _, m := range assets {
		have[m[1]] = true
		if r, _ := c.get(t, m[1]); r.StatusCode != http.StatusOK {
			t.Errorf("GET %s (named by the terminal page) = %d, want 200", m[1], r.StatusCode)
		}
	}
	for _, want := range []string{"/static/js/xterm.js", "/static/js/addon-fit.js", "/static/js/xterm.css", "/static/js/terminal.js"} {
		found := false
		for a := range have {
			if strings.HasPrefix(a, want+"?v=") || a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the terminal page doesn't load %s; it names %v", want, assets)
		}
	}

	// 2. The socket, as a browser on the console's own page would open it.
	api, err := url.Parse(c.g.target.API)
	if err != nil {
		t.Fatal(err)
	}
	origin := api.Scheme + "://" + api.Host
	dialer := c.g.labDialer(c.web.Jar)
	tc, wsResp, err := c.g.openTerminal(dialer, socket+"?shell=sh", http.Header{"Origin": {origin}})
	if err != nil {
		status, code := refused(t, wsResp, err)
		t.Fatalf("the websocket with the cookie and Origin %s = %d %s (%v), want 101", origin, status, code, err)
	}
	t.Cleanup(func() { tc.conn.Close() })
	tc.typeText(t, "echo $((6*7))\r")
	tc.waitOutput(t, "42", regexp.MustCompile(`\b42\r?\n`), 30*time.Second)
	tc.typeText(t, "exit\r")
	if exit, exitRaw, _ := tc.waitEnd(t, 30*time.Second); exit == nil || exit.Code == nil || *exit.Code != 0 {
		t.Errorf("the last text frame = %q, want {\"type\":\"exit\",\"code\":0}", exitRaw)
	}
	var rec gateAction
	waitFor(t, "the terminal in the app's actions", 30*time.Second, func() bool {
		acts, _ := c.g.actionList(t, id)
		for _, a := range acts {
			if a.Action == "terminal" && a.Status != "running" {
				rec = a
				return true
			}
		}
		return false
	})
	if rec.Trigger != "console" || rec.Program != "sh" || rec.CopyID != copyID {
		t.Errorf("the terminal action = %+v, want trigger console, program sh, copy %s", rec, copyID)
	}

	// 3. A cookie without the console's Origin opens nothing.
	for _, try := range []struct {
		what   string
		header http.Header
	}{
		{"a foreign Origin", http.Header{"Origin": {"https://evil.example"}}},
		{"no Origin", nil},
	} {
		_, wsResp, err := c.g.openTerminal(dialer, socket+"?shell=sh", try.header)
		if status, code := refused(t, wsResp, err); status != http.StatusForbidden || code != "cross_origin" {
			t.Errorf("the websocket with the cookie and %s = %d %s, want 403 cross_origin", try.what, status, code)
		}
	}
	// Nothing of those opened: the app has the one terminal action.
	terminals := 0
	acts, _ := c.g.actionList(t, id)
	for _, a := range acts {
		if a.Action == "terminal" {
			terminals++
		}
	}
	if terminals != 1 {
		t.Errorf("the app has %d terminal actions, want 1: a refused request opens no session", terminals)
	}

	// 4. Delete.
	c.g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
}
