package deploytest

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/nomad/api"
)

// Commands run in copies (feature 020.5). The fake answers
// GET /v1/client/allocation/{id}/exec the way a Nomad server does: it upgrades
// the request to a websocket, streams the scripted output as frames and ends
// with the exit code. OnExec scripts what a command does; without it every
// command exits 0 and prints nothing.

// ExecRequest is one command the fake was asked to run.
type ExecRequest struct {
	Copy    string
	Task    string
	Command []string
	TTY     bool
}

// ExecReply is what a scripted command does.
type ExecReply struct {
	Stdout, Stderr string
	ExitCode       int
	// Hold keeps the command running until the client goes away: a command
	// that outlives the caller's time limit.
	Hold bool
	// Fail closes the websocket with this text before any exit code, as Nomad
	// does when the program can't be started.
	Fail string
	// Shell runs the fake interactive shell instead of the scripted output
	// (see runShell). A terminal request (TTY) with no OnExec hook set gets it
	// too, so a test of the terminal needs no script.
	Shell bool
}

// OnExec sets what a command does: fn is called with the request, without the
// fake's lock, and its reply is played. nil removes the hook.
func (n *Nomad) OnExec(fn func(ExecRequest) ExecReply) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onExec = fn
}

// Execs returns the commands the fake was asked to run, oldest first.
func (n *Nomad) Execs() []ExecRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.execs)
}

// FailExec makes every exec answer status (0 undoes it) instead of upgrading,
// as Nomad does when the token's policy has no alloc-exec capability (403).
func (n *Nomad) FailExec(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.execStatus = status
}

// SetChecks sets what GET /v1/client/allocation/{id}/checks answers for a
// copy: its service checks by ID. A copy with none set answers no checks.
func (n *Nomad) SetChecks(copyID string, checks api.AllocCheckStatuses) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.checks[copyID] = checks
}

func (n *Nomad) serveChecks(w http.ResponseWriter, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.copyStub(id) == nil {
		http.Error(w, "alloc not found", http.StatusNotFound)
		return
	}
	checks := n.checks[id]
	if checks == nil {
		checks = api.AllocCheckStatuses{}
	}
	n.writeJSON(w, checks)
}

// execFrame is the most one output frame carries, so a long output arrives in
// several, as it does from a real client.
const execFrame = 8 << 10

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (n *Nomad) serveExec(w http.ResponseWriter, r *http.Request, id string) {
	n.mu.Lock()
	status := n.execStatus
	hasCopy := n.copyStub(id) != nil
	n.mu.Unlock()
	switch {
	case status != 0:
		http.Error(w, "Permission denied", status)
		return
	case !hasCopy:
		http.Error(w, "Unknown allocation", http.StatusNotFound)
		return
	}

	var command []string
	_ = json.Unmarshal([]byte(r.URL.Query().Get("command")), &command)
	req := ExecRequest{Copy: id, Task: r.URL.Query().Get("task"), Command: command, TTY: r.URL.Query().Get("tty") == "true"}

	n.mu.Lock()
	n.execs = append(n.execs, req)
	hook := n.onExec
	n.mu.Unlock()
	reply := ExecReply{}
	if hook != nil {
		reply = hook(req)
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	if reply.Fail == "" && (reply.Shell || (hook == nil && req.TTY)) {
		runShell(conn)
		return
	}

	// Read the client's frames (stdin, heartbeats) so its close is noticed.
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	if reply.Fail != "" {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, reply.Fail), time.Now().Add(time.Second))
		return
	}
	send := func(frame api.ExecStreamingOutput) bool { return conn.WriteJSON(frame) == nil }
	for _, c := range []struct {
		text string
		out  func([]byte) api.ExecStreamingOutput
	}{
		{reply.Stdout, func(b []byte) api.ExecStreamingOutput {
			return api.ExecStreamingOutput{Stdout: &api.ExecStreamingIOOperation{Data: b}}
		}},
		{reply.Stderr, func(b []byte) api.ExecStreamingOutput {
			return api.ExecStreamingOutput{Stderr: &api.ExecStreamingIOOperation{Data: b}}
		}},
	} {
		for data := []byte(c.text); len(data) > 0; {
			chunk := data[:min(execFrame, len(data))]
			data = data[len(chunk):]
			if !send(c.out(chunk)) {
				return
			}
		}
	}
	if reply.Hold {
		<-gone
		return
	}
	send(api.ExecStreamingOutput{Exited: true, Result: &api.ExecStreamingExitResult{ExitCode: reply.ExitCode}})
	// Give the client the frame before the connection closes.
	select {
	case <-gone:
	case <-time.After(time.Second):
	}
}
