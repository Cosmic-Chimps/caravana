package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPUsageErrors: a start-up mistake is exit 2 with a message that says
// what to fix, nothing on standard output (it carries the protocol) and never
// the values the owner typed.
func TestMCPUsageErrors(t *testing.T) {
	const token = "cvat_secretvalue0265"
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no address", []string{"mcp"}, nil, "CARAVANA_URL is required"},
		{"no token", []string{"mcp"}, map[string]string{"CARAVANA_URL": "https://caravana.example.com"}, "CARAVANA_TOKEN or CARAVANA_TOKEN_FILE is required"},
		{"both token settings", []string{"mcp"}, map[string]string{"CARAVANA_URL": "https://caravana.example.com",
			"CARAVANA_TOKEN": token, "CARAVANA_TOKEN_FILE": "/run/token"}, "only one of CARAVANA_TOKEN and CARAVANA_TOKEN_FILE"},
		{"http to a remote host", []string{"mcp"}, map[string]string{"CARAVANA_URL": "http://caravana.example.com",
			"CARAVANA_TOKEN": token}, "token would travel in clear"},
		{"a token flag", []string{"mcp", "--token", token}, map[string]string{"CARAVANA_URL": "https://caravana.example.com"}, "flag provided but not defined"},
		{"token file that is missing", []string{"mcp"}, map[string]string{"CARAVANA_URL": "https://caravana.example.com",
			"CARAVANA_TOKEN_FILE": "/definitely/not/here"}, "CARAVANA_TOKEN_FILE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			getenv := func(k string) string { return tt.env[k] }
			code := run(t.Context(), tt.args, getenv, strings.NewReader(""), &stdout, &stderr)

			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr = %q, want it to say %q", stderr.String(), tt.want)
			}
			if !strings.Contains(stderr.String(), "usage: caravana mcp") {
				t.Errorf("stderr has no usage text: %q", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing: it carries the protocol", stdout.String())
			}
			if strings.Contains(stderr.String(), token) {
				t.Errorf("stderr holds the token: %q", stderr.String())
			}
		})
	}
}

func TestMCPHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"mcp", "-h"}, noEnv, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "usage: caravana mcp") || stderr.Len() != 0 {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

// TestMCPServesAndStops: `caravana mcp` talks the protocol over its standard
// input and output, calls the API with the token, exits 0 when the client closes
// the stream, and logs to standard error without the token.
func TestMCPServesAndStops(t *testing.T) {
	const token = "cvat_s3cretSentinel0265"
	var sawBearer atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawBearer.Store(r.Header.Get("Authorization") == "Bearer "+token)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok","version":"0.9.1"}`)
	}))
	t.Cleanup(api.Close)

	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	var stderr syncBuffer
	getenv := func(k string) string {
		return map[string]string{"CARAVANA_URL": api.URL + "/", "CARAVANA_TOKEN": token}[k]
	}
	done := make(chan int, 1)
	go func() { done <- run(t.Context(), []string{"mcp"}, getenv, serverIn, serverOut, &stderr) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(t.Context(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := cs.InitializeResult().ServerInfo; got.Name != "caravana" || got.Version != version {
		t.Errorf("server = %+v, want caravana %s", got, version)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_version"})
	if err != nil || res.IsError {
		t.Fatalf("get_version: %v %+v", err, res)
	}
	if !sawBearer.Load() {
		t.Error("the API did not see the token as a Bearer")
	}

	cs.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0 when the client closes the stream; stderr: %s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("`caravana mcp` did not exit after the client closed the stream")
	}

	logged := stderr.String()
	if !strings.Contains(logged, `"tool":"get_version"`) || !strings.Contains(logged, `"outcome":"ok"`) {
		t.Errorf("stderr has no line for the tool call: %s", logged)
	}
	if strings.Contains(logged, token) {
		t.Errorf("stderr holds the token: %s", logged)
	}
	if strings.Contains(logged, `"version":"0.9.1"`) {
		t.Errorf("stderr holds a tool's answer: %s", logged)
	}
}
