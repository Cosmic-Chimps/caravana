package main

// The terminal gate step (feature 020.6, plan.md "Lab"), and the websocket
// client it and the console step share. It is in its own file only because
// gate_test.go is long; runGate lists it with the other steps.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// termExit is the last text frame of a terminal.
type termExit struct {
	Type    string `json:"type"`
	Code    *int   `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// termClient is one open terminal, seen from the browser's side. A goroutine
// reads the connection for as long as it lives (gorilla can't read on after a
// read timeout, so the test never sets one) and keeps the output and the end.
type termClient struct {
	conn *websocket.Conn

	mu      sync.Mutex
	out     bytes.Buffer
	exit    *termExit
	exitRaw string
	endErr  error
	done    chan struct{} // closed when the connection has ended
}

// wsURL is the websocket address of an API path: ws:// for http, wss:// for https.
func wsURL(api, path string) string {
	switch {
	case strings.HasPrefix(api, "https://"):
		return "wss://" + strings.TrimPrefix(api, "https://") + path
	case strings.HasPrefix(api, "http://"):
		return "ws://" + strings.TrimPrefix(api, "http://") + path
	}
	return api + path
}

// labDialer is a websocket client that trusts what the gate's HTTP client does
// (a host on a staging CA) and sends the jar's cookies, if any.
func (g *gate) labDialer(jar http.CookieJar) *websocket.Dialer {
	d := &websocket.Dialer{HandshakeTimeout: 15 * time.Second, Jar: jar}
	if tr, ok := g.client().Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
		d.TLSClientConfig = tr.TLSClientConfig.Clone()
	}
	return d
}

// openTerminal dials a copy's terminal. On a refusal it returns the answer
// (its status and error body) and a nil client.
func (g *gate) openTerminal(d *websocket.Dialer, path string, header http.Header) (*termClient, *http.Response, error) {
	conn, resp, err := d.Dial(wsURL(g.target.API, path), header)
	if err != nil {
		return nil, resp, err
	}
	tc := &termClient{conn: conn, done: make(chan struct{})}
	go tc.read()
	return tc, resp, nil
}

func (c *termClient) read() {
	defer close(c.done)
	for {
		kind, data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.endErr = err
			c.mu.Unlock()
			return
		}
		c.mu.Lock()
		if kind == websocket.BinaryMessage {
			c.out.Write(data)
		} else {
			var e termExit
			if json.Unmarshal(data, &e) == nil && e.Type == "exit" {
				c.exit, c.exitRaw = &e, string(data)
			}
		}
		c.mu.Unlock()
	}
}

// output is everything the shell has printed so far.
func (c *termClient) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

// send writes one frame of the protocol. Only the test's own goroutine writes.
func (c *termClient) send(t *testing.T, v any) {
	t.Helper()
	if err := c.conn.WriteJSON(v); err != nil {
		t.Fatalf("write to the terminal: %v", err)
	}
}

// typeText types text, as a keyboard would (\r is Enter).
func (c *termClient) typeText(t *testing.T, text string) {
	t.Helper()
	c.send(t, map[string]string{"type": "input", "data": text})
}

// waitOutput waits until the output matches the pattern.
func (c *termClient) waitOutput(t *testing.T, what string, re *regexp.Regexp, within time.Duration) {
	t.Helper()
	if !waitUntil(within, func() bool { return re.MatchString(c.output()) }) {
		t.Fatalf("the terminal didn't print %s within %v; output so far: %q", what, within, c.output())
	}
}

// waitEnd waits for the connection to end and returns the exit frame and the
// error that ended the read (a *websocket.CloseError for a close frame).
func (c *termClient) waitEnd(t *testing.T, within time.Duration) (*termExit, string, error) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(within):
		t.Fatalf("the terminal was still open %v after it should have ended; output so far: %q", within, c.output())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit, c.exitRaw, c.endErr
}

// refused reads why a dial failed: the status and the error code of the answer.
func refused(t *testing.T, resp *http.Response, err error) (int, string) {
	t.Helper()
	if err == nil {
		t.Fatal("the terminal opened, want it refused")
	}
	if resp == nil {
		t.Fatalf("the dial failed without an answer: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return resp.StatusCode, e.Error.Code
}

// terminalStep (feature 020.6): an interactive shell in a copy through the
// websocket, with the token Caravana really runs with (so a policy without
// alloc-exec fails here). A small busybox app, then:
//
//  1. refusals before any session: no credentials is 401, a deploy token is 403
//     token_scope, shell=zsh is 422 invalid_field;
//  2. with the gate's API token (no Origin, as a client that isn't a browser):
//     `echo $((6*7))` prints 42; after a resize to 100x40 `stty size` says
//     "40 100"; a sentinel typed is echoed; `exit 3` ends with the frame
//     {"type":"exit","code":3} and a normal close;
//  3. listAppActions has the terminal: the shell asked for (sh), exit code 3,
//     trigger manual, the copy; and no later API answer holds the sentinel;
//  4. the app is deleted.
//
// The cookie and Origin rules need a browser's session, so the console step
// (console-terminal) checks those.
func (g *gate) terminalStep(t *testing.T) {
	const id = "lab0206"
	sentinel := "s3cret-sentinel-0206-lab-" + g.run

	var tokenID string
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
	remove() // leftovers from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	t.Cleanup(remove)

	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(actionsWeb("", 1)))
	var first gateDeployment
	if err := json.Unmarshal(raw, &first); err != nil || first.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, first.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	var copies []string
	waitFor(t, "a running copy of "+id, time.Minute, func() bool {
		copies = g.runningCopies(t, id)
		return len(copies) == 1
	})
	copyID := copies[0]
	path := "/v1/apps/" + id + "/copies/" + copyID + "/terminal"
	bearer := func(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }
	dialer := g.labDialer(nil)

	// 1. Refusals.
	_, resp, err := g.openTerminal(dialer, path, nil)
	if status, _ := refused(t, resp, err); status != http.StatusUnauthorized {
		t.Errorf("openTerminal without credentials = %d, want 401", status)
	}
	raw = g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"lab0206-`+g.run+`","scope":{"project":"default","app":"`+id+`"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(raw, &made); err != nil || made.Token == "" {
		t.Fatalf("createToken with a scope = %s (%v)", raw, err)
	}
	tokenID = made.ID
	_, resp, err = g.openTerminal(dialer, path+"?shell=sh", bearer(made.Token))
	if status, code := refused(t, resp, err); status != http.StatusForbidden || code != "token_scope" {
		t.Errorf("openTerminal with a deploy token = %d %s, want 403 token_scope", status, code)
	}
	_, resp, err = g.openTerminal(dialer, path+"?shell=zsh", bearer(g.target.APIToken))
	if status, code := refused(t, resp, err); status != http.StatusUnprocessableEntity || code != "invalid_field" {
		t.Errorf("openTerminal with shell=zsh = %d %s, want 422 invalid_field", status, code)
	}

	// 2. A session.
	tc, resp, err := g.openTerminal(dialer, path+"?shell=sh&cols=80&rows=24", bearer(g.target.APIToken))
	if err != nil {
		status, code := refused(t, resp, err)
		t.Fatalf("openTerminal with the API token = %d %s (%v), want 101", status, code, err)
	}
	t.Cleanup(func() { tc.conn.Close() })
	tc.typeText(t, "echo $((6*7))\r")
	tc.waitOutput(t, "42", regexp.MustCompile(`\b42\r?\n`), 30*time.Second)

	// Resizes and input travel separately, so ask for the size until it is the
	// new one instead of once.
	tc.send(t, map[string]any{"type": "resize", "cols": 100, "rows": 40})
	sized := regexp.MustCompile(`\b40 100\r?\n`)
	if !waitUntil(20*time.Second, func() bool {
		if sized.MatchString(tc.output()) {
			return true
		}
		tc.typeText(t, "stty size\r")
		time.Sleep(time.Second)
		return sized.MatchString(tc.output())
	}) {
		t.Fatalf("stty size never said \"40 100\" after the resize; output: %q", tc.output())
	}

	tc.typeText(t, "echo "+sentinel+"\r")
	tc.waitOutput(t, "the sentinel", regexp.MustCompile(regexp.QuoteMeta(sentinel)), 15*time.Second)

	tc.typeText(t, "exit 3\r")
	exit, exitRaw, endErr := tc.waitEnd(t, 30*time.Second)
	if exit == nil || exit.Code == nil || *exit.Code != 3 {
		t.Errorf("the last text frame = %q, want {\"type\":\"exit\",\"code\":3}", exitRaw)
	}
	var closeErr *websocket.CloseError
	if !errors.As(endErr, &closeErr) || closeErr.Code != websocket.CloseNormalClosure {
		t.Errorf("the connection ended with %v, want a normal close (1000)", endErr)
	}

	// 3. The record.
	var rec gateAction
	var rawActs string
	waitFor(t, "the terminal in the app's actions", 30*time.Second, func() bool {
		var acts []gateAction
		acts, rawActs = g.actionList(t, id)
		for _, a := range acts {
			if a.Action == "terminal" && a.Status != "running" {
				rec = a
				return true
			}
		}
		return false
	})
	if rec.Program != "sh" || rec.ExitCode == nil || *rec.ExitCode != 3 || rec.Trigger != "manual" || rec.CopyID != copyID {
		t.Errorf("the terminal action = %+v, want program sh, exit code 3, trigger manual, copy %s", rec, copyID)
	}
	later := []string{rawActs}
	for _, p := range []string{"/v1/apps/" + id, "/v1/apps/" + id + "/copies", "/v1/apps", "/v1/apps/" + id + "/actions", "/v1/attention"} {
		later = append(later, string(g.sendWant(t, http.StatusOK, http.MethodGet, p, "")))
	}
	for _, a := range later {
		if strings.Contains(a, sentinel) {
			t.Errorf("an API answer after the session holds what was typed: %.300s", a)
		}
	}

	// 4. Delete.
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
}
