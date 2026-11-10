package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
)

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// Sentinels: values that must never appear in the agent's output, errors or arguments.
const (
	sentinelJoinToken = "cvjn_s3cretSentinelJoinToken0123456789abcdefgh12"
	sentinelNodeToken = "cvnd_s3cretSentinelNodeToken0123456789abcdefgh12"
	sentinelGossip    = "c2VjcmV0LXNlbnRpbmVsLWdvc3NpcDMyYnl0ZXMhISE="
)

type call struct {
	name string
	args []string
	// conf is what the file named by a wg call held at the time of the call, with its mode.
	conf     string
	confMode os.FileMode
}

func (c call) String() string { return c.name + " " + strings.Join(c.args, " ") }

// fakeRunner stands in for the host's programs. It remembers links (so a second `up` finds them) and
// every call it got.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []call
	links    map[string]bool
	pingFail map[string]bool
	failOn   map[string]error // "systemctl restart" -> error
	hostname string
	inFlight int
	maxPings int
}

func newRunner() *fakeRunner {
	return &fakeRunner{links: map[string]bool{}, pingFail: map[string]bool{}, failOn: map[string]error{}, hostname: "Fsn-2.example.com"}
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := call{name: name, args: append([]string(nil), args...)}
	if name == "wg" && len(args) >= 3 {
		if b, err := os.ReadFile(args[2]); err == nil {
			c.conf = string(b)
		}
		if fi, err := os.Stat(args[2]); err == nil {
			c.confMode = fi.Mode().Perm()
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	if err, ok := f.failOn[name+" "+firstWord(args)]; ok {
		f.mu.Unlock()
		return nil, err
	}
	switch {
	case name == "ping":
		f.inFlight++
		if f.inFlight > f.maxPings {
			f.maxPings = f.inFlight
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		f.mu.Lock()
		f.inFlight--
		defer f.mu.Unlock()
		if f.pingFail[args[len(args)-1]] {
			return nil, errors.New("no answer")
		}
		return []byte("1 packets transmitted, 1 received"), nil
	}
	defer f.mu.Unlock()
	switch {
	case name == "ip" && len(args) >= 3 && args[0] == "link" && args[1] == "show":
		if !f.links[args[3]] {
			return nil, errors.New("Device does not exist")
		}
		return nil, nil
	case name == "ip" && len(args) >= 3 && args[0] == "link" && args[1] == "add":
		f.links[args[2]] = true
	case name == "ip" && len(args) >= 3 && args[0] == "link" && args[1] == "delete":
		if !f.links[args[2]] {
			return nil, errors.New("Cannot find device")
		}
		delete(f.links, args[2])
	case name == "uname" && args[0] == "-n":
		return []byte(f.hostname + "\n"), nil
	case name == "uname" && args[0] == "-r":
		return []byte("6.8.0-134-generic\n"), nil
	case name == "docker":
		return []byte("27.5.1\n"), nil
	case name == "nomad":
		return []byte("Nomad v2.0.7\nBuildDate 2026-10-01\n"), nil
	case name == "df":
		return []byte("Filesystem     1M-blocks  Used Available Capacity Mounted on\n/dev/vda1         190000 30000    160000      16% /\n"), nil
	}
	return nil, nil
}

// commands are the calls but wg's temporary file name (random) replaced by <conf>, ping left out.
func (f *fakeRunner) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		switch c.name {
		case "ping", "uname", "docker", "nomad", "df":
			continue
		}
		s := c.String()
		if c.name == "wg" {
			s = "wg " + c.args[0] + " " + c.args[1] + " <conf>"
		}
		out = append(out, s)
	}
	return out
}

func (f *fakeRunner) wgCalls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.name == "wg" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeRunner) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func (f *fakeRunner) allArgs() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, c := range f.calls {
		b.WriteString(c.String() + "\n")
	}
	return b.String()
}

// console plays the Caravana console: the three node calls.
type console struct {
	t      *testing.T
	srv    *httptest.Server
	caCert []byte
	caKey  []byte

	mu       sync.Mutex
	requests []consoleRequest
	handlers map[string]func(req map[string]any, auth string) (int, any)
}

type consoleRequest struct {
	path, auth string
	body       map[string]any
}

func newConsole(t *testing.T) *console {
	t.Helper()
	caCert, caKey, err := clusterca.New(t0)
	if err != nil {
		t.Fatal(err)
	}
	c := &console{t: t, caCert: caCert, caKey: caKey, handlers: map[string]func(map[string]any, string) (int, any){}}
	c.srv = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *console) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	c.mu.Lock()
	c.requests = append(c.requests, consoleRequest{r.URL.Path, r.Header.Get("Authorization"), body})
	h := c.handlers[r.URL.Path]
	c.mu.Unlock()
	if h == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	status, answer := h(body, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	switch a := answer.(type) {
	case string:
		_, _ = io.WriteString(w, a)
	case nil:
	default:
		_ = json.NewEncoder(w).Encode(a)
	}
}

func (c *console) on(path string, h func(req map[string]any, auth string) (int, any)) {
	c.mu.Lock()
	c.handlers[path] = h
	c.mu.Unlock()
}

func (c *console) calls(path string) []consoleRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []consoleRequest
	for _, r := range c.requests {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func (c *console) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// sign signs the CSR of a request as the console would, for the mesh address of index.
func (c *console) sign(csr any, kind clusterca.Kind, index int) string {
	c.t.Helper()
	return c.signAt(csr, kind, index, t0)
}

// signAt is sign at another moment, for a renewal that happens later.
func (c *console) signAt(csr any, kind clusterca.Kind, index int, now time.Time) string {
	c.t.Helper()
	s, _ := csr.(string)
	addrs, _ := mesh.Of(index)
	cert, err := clusterca.Sign(c.caCert, c.caKey, []byte(s), kind, addrs.Mesh, now)
	if err != nil {
		c.t.Fatalf("the fake console can't sign: %v", err)
	}
	return string(cert)
}

const (
	peerKey1 = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo="
	peerKey2 = "3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08="
)

func peerJSON(index int, key, endpoint string) map[string]any {
	return map[string]any{"index": index, "name": "peer", "publicKey": key, "endpoint": endpoint}
}

// serveJoin makes the console answer joins like a real one: a client certificate for index 3, the
// first server as the only peer, a node token.
func (c *console) serveJoin() {
	c.on("/v1/join", func(req map[string]any, _ string) (int, any) {
		if req["token"] != sentinelJoinToken {
			return 401, map[string]any{"error": map[string]any{"code": "join_token_invalid", "message": "This join token isn't valid: it was used, expired, or never existed."}}
		}
		return 200, map[string]any{
			"name": "fsn-2", "index": 3, "pool": "default", "role": "client", "servers": []int{1},
			"ca": string(c.caCert), "certificate": c.sign(req["csr"], clusterca.Client, 3), "nodeToken": sentinelNodeToken,
			"peers": []any{peerJSON(1, peerKey1, "203.0.113.4:51820")},
		}
	})
}

type testAgent struct {
	*Agent
	root    string
	runner  *fakeRunner
	console *console
	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
	nomad   *nomadFake
}

type nomadFake struct {
	leader string
	peers  []string
	srv    *httptest.Server
}

func newTestAgent(t *testing.T, c *console) *testAgent {
	t.Helper()
	root := t.TempDir()
	nf := &nomadFake{leader: "10.98.0.1:4647", peers: []string{}}
	nf.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status/leader":
			_ = json.NewEncoder(w).Encode(nf.leader)
		case "/v1/status/peers":
			_ = json.NewEncoder(w).Encode(nf.peers)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(nf.srv.Close)
	runner := newRunner()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	a := &Agent{
		Root: root, Runner: runner, HTTP: NewHTTPClient(false), Now: func() time.Time { return t0 },
		Version: "0.18.0", NomadAddr: nf.srv.URL, Stdout: stdout, Stderr: stderr,
	}
	return &testAgent{Agent: a, root: root, runner: runner, console: c, stdout: stdout, stderr: stderr, nomad: nf}
}

func (a *testAgent) file(name string) string { return filepath.Join(a.root, name) }

func (a *testAgent) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(a.file(name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func (a *testAgent) exists(name string) bool {
	_, err := os.Stat(a.file(name))
	return err == nil
}

func (a *testAgent) mode(t *testing.T, name string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(a.file(name))
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	return fi.Mode().Perm()
}

// snapshot is every file under the root with its mode and content: to say "nothing changed".
func (a *testAgent) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(a.root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		out[strings.TrimPrefix(p, a.root)] = fi.Mode().Perm().String() + string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// joined makes the agent join against a console that serves joins, and clears what was recorded.
func (a *testAgent) joined(t *testing.T) {
	t.Helper()
	a.console.serveJoin()
	if err := a.Join(context.Background(), JoinOptions{URL: a.console.srv.URL, Token: sentinelJoinToken, Address: "203.0.113.9"}); err != nil {
		t.Fatalf("join: %v", err)
	}
	a.runner.reset()
	a.stdout.Reset()
	a.stderr.Reset()
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func (a *testAgent) writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := a.file(name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func removeFile(p string) error { return os.Remove(p) }

func readIfExistsFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}
