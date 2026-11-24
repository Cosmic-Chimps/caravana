package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/nodeagent"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// consoleHost is a fake host that records the units the agent restarts.
type consoleHost struct {
	fakeHost
	mu       sync.Mutex
	restarts []string
}

func (h *consoleHost) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" {
		h.mu.Lock()
		h.restarts = append(h.restarts, strings.Join(args, " "))
		h.mu.Unlock()
		return nil, nil
	}
	return h.fakeHost.Run(ctx, name, args...)
}

func (h *consoleHost) restartsOf(cmd string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.restarts {
		if r == cmd {
			n++
		}
	}
	return n
}

// A change of the console's address end to end, with nothing in the middle: the real agent of the first
// server, whose node.env points at the console's loopback listener, reports that it runs the console;
// the service hands it the switching change in the sync answer; the agent rewrites the two files on its
// temporary root and restarts Caravana; and its outcome, reported at the next sync, settles the change
// once Caravana runs under the new address. Both sides' JSON is checked against the contract.
func TestAConsoleAddressChangeThroughTheRealAgent(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t, "")
	caCert, caKey, err := clusterca.New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(caCert), "key": string(caKey)})
	// A Nomad server needs a real gossip key: 32 bytes.
	e.Nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": "c2VjcmV0LXNlbnRpbmVsLWdvc3NpcDMyYnl0ZXMhISE=\n"})
	priv, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	token, err := e.Service.AdoptFirst(ctx, "hel-1", pub, "203.0.113.4")
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.AddNode("node-1", "hel-1", "ready", "10.99.0.1")
	e.Service.SetPublicURL("https://console.example.com")

	// The first server's agent: node.env at the loopback listener, which the test routes to these handlers.
	host := &consoleHost{fakeHost: fakeHost{hostname: "hel-1.example.com"}}
	var stdout, stderr bytes.Buffer
	root := t.TempDir()
	target := strings.TrimPrefix(e.RawURL, "http://")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	agent := &nodeagent.Agent{
		Root: root, Runner: host, HTTP: &http.Client{Timeout: 10 * time.Second, Transport: tr},
		Version: "0.0.1-e2e", NomadAddr: localNomad(t).URL, Stdout: &stdout, Stderr: &stderr,
		Sleep: func(time.Duration) {},
	}
	if err := agent.SaveNodeEnv(nodeagent.NodeEnv{URL: "http://127.0.0.1:7070", Token: token}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveState(nodeagent.State{Index: 1, Name: "hel-1", Pool: "default", First: true, Role: nodeagent.RoleServer, Servers: []int{1}}); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("etc/caravana-node/mesh.key", priv+"\n")
	write("etc/caravana-node/gossip.key", "c2VjcmV0LXNlbnRpbmVsLWdvc3NpcDMyYnl0ZXMhISE=\n")
	write("etc/nomad.d/tls/ca.pem", string(caCert))
	write("etc/caravana/caravana.env", "NOMAD_ADDR=http://127.0.0.1:4646\nCARAVANA_PUBLIC_URL=https://console.example.com\n")
	write("var/lib/caravana-host/install.env", "CARAVANA_ROLE=first\nCARAVANA_DOMAIN=console.example.com\n")

	sync := func() {
		t.Helper()
		if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil {
			t.Fatalf("Sync: %v\n%s", err, stderr.String())
		}
	}
	sync() // the agent says it runs the console
	if view, err := e.Service.ConsoleAddress(ctx); err != nil || !view.Agent {
		t.Fatalf("the console doesn't know the agent: %+v, %v", view, err)
	}

	// A change that is switching, as SwitchConsoleAddress leaves it (its checks need Traefik and DNS,
	// which these handlers don't have; their own tests cover them).
	id := strings.Repeat("ab", 16)
	if _, err := e.Store.InsertConsoleAddressChange(ctx, store.ConsoleAddressChange{ID: id, Domain: "new.example.com",
		OldDomain: "console.example.com", RedirectDays: 30, Status: store.ConsoleSwitching, SwitchAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	// The answer, as the real handler writes it, is the contract's.
	rawReport, _ := json.Marshal(map[string]any{
		"agent": "0.0.1", "nomad": "2.0.7", "docker": "27.5.1", "dockerRunning": true, "diskFreeMb": 1, "diskTotalMb": 2,
		"memoryMb": 3, "kernel": "6", "role": "server", "nomadLeader": true, "serverRunning": true, "reach": []any{},
		"runsConsole": true, "consoleAddress": map[string]any{"id": strings.Repeat("0", 32), "state": "refused", "reason": "an old one"},
	})
	checkSchema(t, loadContract(t), rawReport, "NodeReport")
	status, _, raw := callWith(t, "Bearer "+token, http.MethodPost, e.RawURL+"/v1/node/sync", "application/json", string(rawReport))
	if status != http.StatusOK {
		t.Fatalf("sync = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "NodeSyncAnswer")
	var ans struct {
		ConsoleURL     string `json:"consoleURL"`
		ConsoleAddress *struct {
			ID     string `json:"id"`
			Domain string `json:"domain"`
		} `json:"consoleAddress"`
	}
	if err := json.Unmarshal(raw, &ans); err != nil {
		t.Fatal(err)
	}
	if ans.ConsoleURL != "https://console.example.com" || ans.ConsoleAddress == nil || ans.ConsoleAddress.ID != id || ans.ConsoleAddress.Domain != "new.example.com" {
		t.Fatalf("answer = %s, want the console URL and the request", raw)
	}

	// The real agent applies it.
	stdout.Reset()
	sync()
	envFile, _ := os.ReadFile(filepath.Join(root, "etc/caravana/caravana.env"))
	record, _ := os.ReadFile(filepath.Join(root, "var/lib/caravana-host/install.env"))
	if string(envFile) != "NOMAD_ADDR=http://127.0.0.1:4646\nCARAVANA_PUBLIC_URL=https://new.example.com\n" ||
		string(record) != "CARAVANA_ROLE=first\nCARAVANA_DOMAIN=new.example.com\n" {
		t.Errorf("files: %q, %q", envFile, record)
	}
	// Nomad was restarted once for the first issuer (a server takes the console's https URL at its first
	// sync), which has nothing to do with the change; Caravana was restarted once.
	if n := host.restartsOf("restart caravana"); n != 1 {
		t.Errorf("Caravana was restarted %d times, want 1: %v", n, host.restarts)
	}
	if n := host.restartsOf("restart nomad"); n != 1 {
		t.Errorf("Nomad was restarted %d times before the new address, want only the issuer's: %v", n, host.restarts)
	}
	// While it is switching, nothing has settled: the outcome travels with the next report.
	if got, _ := e.Store.ConsoleAddressChange(ctx, id); got.Status != store.ConsoleSwitching {
		t.Fatalf("status = %q before the outcome is reported", got.Status)
	}

	// Caravana restarts under the new address, and the agent reports it.
	e.Service.SetPublicURL("https://new.example.com")
	sync()
	got, err := e.Store.ConsoleAddressChange(ctx, id)
	if err != nil || got.Status != store.ConsoleApplied || got.RedirectUntil.IsZero() {
		t.Errorf("change = %+v, %v, want applied with a redirect window", got, err)
	}
	// The issuer follows the address by itself: this sync's answer carried the new one and the Nomad server
	// restarted to take it. And no request is handed out again: the change isn't open.
	if n := host.restartsOf("restart nomad"); n != 2 {
		t.Errorf("Nomad was restarted %d times, want a second for the new issuer: %v", n, host.restarts)
	}
	sync()
	if n := host.restartsOf("restart caravana"); n != 1 {
		t.Errorf("Caravana was restarted %d times in all, want 1: %v", n, host.restarts)
	}
}
