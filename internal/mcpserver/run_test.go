package mcpserver

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRun serves the protocol over plain pipes, as `caravana mcp` does over
// standard input and output, and returns nil when the client closes the
// stream. It also checks that starting calls nothing.
func TestRun(t *testing.T) {
	f := newFakeAPI(t)
	f.reply("GET /v1/health", 200, healthJSON)
	cfg := Config{URL: f.srv.URL, Token: sentinelToken, Version: "9.9.9"}
	log, logs := newTestLog()

	// Two pipes: what the client writes is what the server reads, and back.
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	done := make(chan error, 1)
	go func() { done <- Run(t.Context(), cfg, serverIn, serverOut, log) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(t.Context(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := cs.InitializeResult().ServerInfo; got.Name != "caravana" || got.Version != "9.9.9" {
		t.Errorf("server = %+v, want caravana 9.9.9", got)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("starting made %d API calls, want none", n)
	}
	if !strings.Contains(mustCall(t, cs, "get_version", nil), `"mcp":"9.9.9"`) {
		t.Error("get_version over the pipes did not answer")
	}

	cs.Close() // the client closes the stream
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil when the client closes the stream", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the client closed the stream")
	}

	if !strings.Contains(logs.String(), "mcp server started") {
		t.Errorf("no start line in the log: %s", logs.String())
	}
	if strings.Contains(logs.String(), sentinelToken) {
		t.Errorf("the log holds the token: %s", logs.String())
	}
}

// TestRunStopsWhenTheContextEnds: stopping the process (Ctrl-C, SIGTERM) is a
// clean end, not an error.
func TestRunStopsWhenTheContextEnds(t *testing.T) {
	f := newFakeAPI(t)
	cfg := Config{URL: f.srv.URL, Token: sentinelToken}
	log, _ := newTestLog()
	serverIn, clientOut := io.Pipe()
	_, serverOut := io.Pipe()
	t.Cleanup(func() { clientOut.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, serverIn, serverOut, log) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context ended")
	}
}
