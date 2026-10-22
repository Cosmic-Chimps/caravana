package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func deploymentAt(status string) string {
	return `{"id":"d-1","appId":"web","status":"` + status + `","progress":{"wanted":2,"placed":2,"healthy":1}}`
}

// TestWaitEndsOnAFinalStatus: every final status ends the wait, with the
// deployment as it then is and waitEnded "final".
func TestWaitEndsOnAFinalStatus(t *testing.T) {
	for _, final := range []string{"healthy", "failed", "reverted", "superseded"} {
		t.Run(final, func(t *testing.T) {
			f := newFakeAPI(t)
			f.on("GET /v1/apps/web/deployments/d-1", sequence(deploymentAt("pending"), deploymentAt("running"), deploymentAt(final)))
			cs := f.session(t, false, nil, fastTuning, nil)

			var got map[string]any
			text := mustCall(t, cs, "wait_for_deployment", map[string]any{"app": "web", "deployment": "d-1"})
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatalf("answer is not JSON: %v: %s", err, text)
			}
			if got["status"] != final || got["waitEnded"] != "final" || got["appId"] != "web" {
				t.Errorf("answer = %v", got)
			}
			if n := len(f.requests()); n != 3 {
				t.Errorf("%d reads, want 3 (pending, running, %s)", n, final)
			}
		})
	}
}

// TestWaitTimesOut: a deployment that doesn't end is answered as it was last
// read, with waitEnded "timeout".
func TestWaitTimesOut(t *testing.T) {
	f := newFakeAPI(t)
	f.reply("GET /v1/apps/web/deployments/d-1", 200, deploymentAt("running"))
	cs := f.session(t, false, nil, fastTuning, nil)

	var got map[string]any
	text := mustCall(t, cs, "wait_for_deployment", map[string]any{"app": "web", "deployment": "d-1", "timeoutSeconds": 10})
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("answer is not JSON: %v: %s", err, text)
	}
	if got["status"] != "running" || got["waitEnded"] != "timeout" {
		t.Errorf("answer = %v", got)
	}
}

// TestWaitSendsProgressWhenAsked: with a progress token the client hears about
// each read; without one nothing is sent.
func TestWaitSendsProgressWhenAsked(t *testing.T) {
	run := func(t *testing.T, withToken bool) []*mcp.ProgressNotificationParams {
		f := newFakeAPI(t)
		f.on("GET /v1/apps/web/deployments/d-1", sequence(deploymentAt("running"), deploymentAt("running"), deploymentAt("healthy")))

		var mu sync.Mutex
		var seen []*mcp.ProgressNotificationParams
		opts := &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, req.Params)
		}}
		cs := f.session(t, false, opts, fastTuning, nil)

		params := &mcp.CallToolParams{Name: "wait_for_deployment", Arguments: map[string]any{"app": "web", "deployment": "d-1"}}
		if withToken {
			params.SetProgressToken("tok-1")
		}
		res, err := cs.CallTool(t.Context(), params)
		if err != nil || res.IsError {
			t.Fatalf("call: %v %v", err, res)
		}
		// Notifications reach the handler on their own goroutine, so the last
		// one can land just after the answer: give it a moment when one is due.
		for deadline := time.Now().Add(2 * time.Second); withToken && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			n := len(seen)
			mu.Unlock()
			if n >= 3 {
				break
			}
		}
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}

	t.Run("with a token", func(t *testing.T) {
		seen := run(t, true)
		if len(seen) != 3 {
			t.Fatalf("%d notifications, want one per read (3): %+v", len(seen), seen)
		}
		for i, n := range seen {
			if n.ProgressToken != "tok-1" || n.Progress != float64(i+1) {
				t.Errorf("notification %d = %+v, want token tok-1 and progress %d", i, n, i+1)
			}
		}
		if !strings.Contains(seen[0].Message, "running") || !strings.Contains(seen[0].Message, "1 of 2 copies healthy") {
			t.Errorf("message = %q", seen[0].Message)
		}
		if !strings.Contains(seen[2].Message, "healthy") {
			t.Errorf("last message = %q", seen[2].Message)
		}
	})
	t.Run("without a token", func(t *testing.T) {
		if seen := run(t, false); len(seen) != 0 {
			t.Errorf("%d notifications without a token", len(seen))
		}
	})
}

// TestWaitStopsOnAnError: a failing read ends the wait with that error.
func TestWaitStopsOnAnError(t *testing.T) {
	f := newFakeAPI(t)
	f.reply("GET /v1/apps/web/deployments/d-1", 404, `{"error":{"code":"not_found","message":"no such deployment"}}`)
	cs := f.session(t, false, nil, fastTuning, nil)

	got := mustFail(t, cs, "wait_for_deployment", map[string]any{"app": "web", "deployment": "d-1"})
	if !strings.Contains(got, "no such deployment") {
		t.Errorf("text = %q", got)
	}
}

// TestWaitStopsWhenTheClientCancels: cancelling the call ends the wait instead
// of leaving it polling.
func TestWaitStopsWhenTheClientCancels(t *testing.T) {
	f := newFakeAPI(t)
	f.reply("GET /v1/apps/web/deployments/d-1", 200, deploymentAt("running"))
	// An hour between reads and a ten-hour limit: only the cancel can end it.
	cs := f.session(t, false, nil, tuning{poll: time.Hour, waitUnit: time.Hour}, nil)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "wait_for_deployment",
			Arguments: map[string]any{"app": "web", "deployment": "d-1"}})
		done <- err
	}()
	for len(f.requests()) == 0 { // the first read has happened: it is waiting now
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the call returned no error after the cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not stop when the call was cancelled")
	}
}
