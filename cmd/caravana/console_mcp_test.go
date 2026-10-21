package main

// The console-mcp step of the lab test (feature 026.5, plan.md "Lab"): `caravana
// mcp` against the lab's real Caravana and real Nomad. The server runs in this
// process over pipes, as a coding agent's client would run it over standard input
// and output, and an MCP client from the SDK talks to it. It is in its own file
// only because console_lab_test.go is long; runConsole lists it before
// console-delete.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpLeaks collects everything the step must be able to search at the end for
// the tokens: every server's standard error and every tool's answer text.
type mcpLeaks struct {
	mu      sync.Mutex
	stderrs []*syncBuffer
	answers []string
}

func (l *mcpLeaks) answer(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.answers = append(l.answers, s)
}

// mcpLab is one `caravana mcp` process (in this one) and a client connected to
// it.
type mcpLab struct {
	t      *testing.T
	cs     *mcp.ClientSession
	stderr *syncBuffer
	leaks  *mcpLeaks
	exit   chan int // runMCP's exit code, once it returns
}

// startMCP runs `caravana mcp` over two pipes, with the settings in its
// environment as an agent's configuration would give them, and connects a
// client. Closing the client ends the server, which the cleanup checks.
func startMCP(t *testing.T, leaks *mcpLeaks, url, token string, allowActions bool) *mcpLab {
	t.Helper()
	env := map[string]string{"CARAVANA_URL": url, "CARAVANA_TOKEN": token}
	var args []string
	if allowActions {
		args = []string{"--allow-actions"}
	}
	stderr := &syncBuffer{}
	leaks.mu.Lock()
	leaks.stderrs = append(leaks.stderrs, stderr)
	leaks.mu.Unlock()

	// Two pipes: what the client writes is what the server reads, and back.
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	exit := make(chan int, 1)
	go func() {
		exit <- runMCP(ctx, args, func(k string) string { return env[k] }, serverIn, serverOut, stderr)
		serverOut.Close() // what the process ending does to standard output
	}()

	// Nothing is called while connecting: the server starts without the API.
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "lab", Version: "0"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		cancel()
		t.Fatalf("connect to caravana mcp: %v\nstderr: %s", err, stderr.String())
	}
	m := &mcpLab{t: t, cs: cs, stderr: stderr, leaks: leaks, exit: exit}
	t.Cleanup(func() {
		defer cancel()
		cs.Close() // the client closes the stream, which is how a server is stopped
		select {
		case code := <-exit:
			if code != exitOK {
				t.Errorf("caravana mcp exited %d after its client closed the stream, want 0\nstderr: %s", code, stderr.String())
			}
		case <-time.After(10 * time.Second):
			t.Errorf("caravana mcp did not stop after its client closed the stream")
		}
	})
	return m
}

// tools is the names of the tools the server offers.
func (m *mcpLab) tools() []string {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := m.cs.ListTools(ctx, nil)
	if err != nil {
		m.t.Fatalf("tools/list: %v", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// call calls a tool and returns its text and whether it was a tool error. A
// protocol error fails the step: tools report their failures as results.
func (m *mcpLab) call(name string, args map[string]any) (string, bool) {
	m.t.Helper()
	// The longest call is wait_for_deployment, which the step asks to wait 300 s.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	res, err := m.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		m.t.Fatalf("%s: protocol error: %v\nstderr: %s", name, err, m.stderr.String())
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	m.leaks.answer(b.String())
	return b.String(), res.IsError
}

// mustCall calls a tool that must succeed and returns its text as JSON in into
// (when given).
func (m *mcpLab) mustCall(name string, args map[string]any, into any) string {
	m.t.Helper()
	text, isErr := m.call(name, args)
	if isErr {
		m.t.Fatalf("%s %v gave a tool error: %s\nstderr: %s", name, args, text, m.stderr.String())
	}
	if into != nil {
		if err := json.Unmarshal([]byte(text), into); err != nil {
			m.t.Fatalf("%s: the answer is not the JSON expected (%v): %s", name, err, text)
		}
	}
	return text
}

// mustFail calls a tool that must give a tool error and returns its text.
func (m *mcpLab) mustFail(name string, args map[string]any) string {
	m.t.Helper()
	text, isErr := m.call(name, args)
	if !isErr {
		m.t.Fatalf("%s %v succeeded with %s, want a tool error", name, args, text)
	}
	return text
}

// mcpServer: `caravana mcp`, as an agent runs it, against whoami (feature
// 026.5):
//
//  1. read-only, with the admin's token: tools/list holds the read tools and
//     none that changes anything; get_version, list_apps, get_app, list_copies,
//     get_logs and get_needs_you answer with real data;
//  2. with --allow-actions: the action tools are listed; deploy_image deploys
//     whoami's own image again (a new deployment), wait_for_deployment ends
//     healthy, and restart_app starts a restart that ends well;
//  3. with a deploy token for whoami: get_app works, list_apps and get_logs
//     are refused with the token's scope in the words;
//  4. with a token Caravana doesn't know: the answer says the token was
//     refused;
//  5. neither token is in any server's standard error or in any answer.
func (c *console) mcpServer(t *testing.T) {
	const id = "whoami"
	readTools := []string{"get_version", "list_apps", "get_app", "list_copies", "get_logs", "get_needs_you"}
	actionTools := []string{"redeploy_app", "deploy_image", "redeploy_version", "promote_app",
		"restart_app", "stop_app", "start_app", "back_up_now"}

	url, adminToken := c.g.target.API, c.g.target.APIToken
	leaks := &mcpLeaks{}

	// 1. The read tools.
	ro := startMCP(t, leaks, url, adminToken, false)
	listed := ro.tools()
	for _, name := range readTools {
		if !slices.Contains(listed, name) {
			t.Errorf("tools/list without --allow-actions lacks %s; it has %v", name, listed)
		}
	}
	for _, name := range actionTools {
		if slices.Contains(listed, name) {
			t.Errorf("tools/list without --allow-actions offers %s", name)
		}
	}

	var ver map[string]any
	ro.mustCall("get_version", nil, &ver)
	if ver["mcp"] != version || ver["version"] == nil {
		t.Errorf("get_version = %v, want Caravana's version and \"mcp\": %q", ver, version)
	}

	var list struct {
		Apps []consoleApp `json:"apps"`
	}
	ro.mustCall("list_apps", nil, &list)
	found := false
	for _, a := range list.Apps {
		found = found || a.App.ID == id
	}
	if !found {
		t.Errorf("list_apps = %+v, want it to include %s", list.Apps, id)
	}

	var rec struct {
		App      struct{ ID string } `json:"app"`
		Revision string              `json:"revision"`
	}
	ro.mustCall("get_app", map[string]any{"app": id}, &rec)
	if rec.App.ID != id || rec.Revision == "" {
		t.Errorf("get_app = %+v, want the app %s with a revision", rec, id)
	}

	var copies struct {
		Copies []struct{ Status string } `json:"copies"`
	}
	ro.mustCall("list_copies", map[string]any{"app": id}, &copies)
	running := 0
	for _, cp := range copies.Copies {
		if cp.Status == "healthy" || cp.Status == "running" {
			running++
		}
	}
	if running == 0 {
		t.Errorf("list_copies = %+v, want at least one running copy", copies.Copies)
	}

	logs := ro.mustCall("get_logs", map[string]any{"app": id, "lines": 20}, nil)
	if !json.Valid([]byte(logs)) || !strings.HasPrefix(strings.TrimSpace(logs), "{") {
		t.Errorf("get_logs = %q, want the API's JSON answer", logs)
	}
	ro.mustCall("get_needs_you", nil, nil)

	// 2. The action tools: a deploy of the image whoami runs now, then a restart.
	image := c.summary(t).App.Services[0].Image
	rw := startMCP(t, leaks, url, adminToken, true)
	listed = rw.tools()
	for _, name := range append(slices.Clone(readTools), actionTools...) {
		if !slices.Contains(listed, name) {
			t.Errorf("tools/list with --allow-actions lacks %s; it has %v", name, listed)
		}
	}

	var dep gateDeployment
	rw.mustCall("deploy_image", map[string]any{"app": id, "image": image}, &dep)
	if dep.ID == "" {
		t.Fatalf("deploy_image gave no deployment ID")
	}
	var waited struct {
		gateDeployment
		WaitEnded string `json:"waitEnded"`
	}
	rw.mustCall("wait_for_deployment", map[string]any{"app": id, "deployment": dep.ID, "timeoutSeconds": 300}, &waited)
	if waited.Status != "healthy" || waited.WaitEnded != "final" {
		t.Fatalf("wait_for_deployment = status %q, waitEnded %q (%s), want healthy and final", waited.Status, waited.WaitEnded, waited.Error)
	}
	c.prev = dep.ID

	var action gateAction
	rw.mustCall("restart_app", map[string]any{"app": id}, &action)
	if action.ID == "" || action.Action != "restart" {
		t.Fatalf("restart_app = %+v, want a restart action", action)
	}
	// It answers at once: wait for the restart to end, so the next step finds
	// the app as it was.
	if done := c.g.waitActionEnds(t, id, action.ID, 4*time.Minute); done.Status != "succeeded" {
		t.Errorf("the restart ended %s (%s), want succeeded", done.Status, done.Error)
	}
	var actions []gateAction
	rw.mustCall("list_app_actions", map[string]any{"app": id}, &actions)
	if !slices.ContainsFunc(actions, func(a gateAction) bool { return a.ID == action.ID }) {
		t.Errorf("list_app_actions = %+v, want it to include the restart %s", actions, action.ID)
	}
	waitFor(t, "a running copy of "+id+" after the restart", time.Minute, func() bool {
		return len(c.g.runningCopies(t, id)) > 0
	})

	// 3. A deploy token for whoami.
	raw := c.g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"lab0265-mcp-`+c.g.run+`","scope":{"project":"default","app":"`+id+`"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(raw, &made); err != nil || made.Token == "" || made.ID == "" {
		t.Fatalf("createToken with a scope: %v (the answer holds the token, so it isn't shown)", err)
	}
	t.Cleanup(func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/tokens/"+made.ID, "", ""); err == nil {
			resp.Body.Close()
		}
	})
	scoped := startMCP(t, leaks, url, made.Token, false)
	scoped.mustCall("get_app", map[string]any{"app": id}, &rec)
	if rec.App.ID != id {
		t.Errorf("get_app with the deploy token = %+v, want %s", rec, id)
	}
	for name, args := range map[string]map[string]any{"list_apps": nil, "get_logs": {"app": id, "lines": 20}} {
		got := scoped.mustFail(name, args)
		if !strings.Contains(got, "token_scope") && !strings.Contains(got, "can only deploy app "+id) {
			t.Errorf("%s with the deploy token: %q, want the token's scope refusal", name, got)
		}
	}

	// 4. A token that looks right and isn't Caravana's.
	wrong := startMCP(t, leaks, url, "cvat_wrongwrongwrong", false)
	got := wrong.mustFail("get_app", map[string]any{"app": id})
	if !strings.Contains(got, "refused the token") || !strings.Contains(got, "401") {
		t.Errorf("get_app with an unknown token: %q, want it to say the token was refused (401)", got)
	}

	// 5. No token anywhere it shouldn't be. The wrong one is searched for too.
	leaks.mu.Lock()
	defer leaks.mu.Unlock()
	for _, token := range []string{adminToken, made.Token, "cvat_wrongwrongwrong"} {
		for i, s := range leaks.stderrs {
			if strings.Contains(s.String(), token) {
				t.Errorf("server %d's standard error holds a token (%d characters)", i, len(token))
			}
		}
		for i, s := range leaks.answers {
			if strings.Contains(s, token) {
				t.Errorf("answer %d holds a token (%d characters)", i, len(token))
			}
		}
	}
	// The log is not empty either: the search above would pass on a server that
	// logged nothing. Each server says it started and logs each tool call.
	for i, s := range leaks.stderrs {
		if log := s.String(); !strings.Contains(log, "mcp server started") || !strings.Contains(log, `"tool call"`) {
			t.Errorf("server %d's standard error lacks its start line or a tool-call line: %q", i, log)
		}
	}
}
