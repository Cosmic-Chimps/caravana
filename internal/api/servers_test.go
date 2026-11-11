package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/nodeagent"
)

const sentinelGossipKey = "c2VudGluZWwtZ29zc2lwLTAwMzEhISEhISEhISE="

// withCluster sets the cluster up as the installer leaves it: the CA and the gossip key in Nomad
// Variables, the first server adopted as hel-1 (index 1) and its Nomad node at 10.99.0.1. It returns
// the CA's PEM key, for the tests that look for it where it must not be.
func withCluster(t *testing.T, e *testEnv) (caCert, caKey []byte) {
	t.Helper()
	caCert, caKey, err := clusterca.New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(caCert), "key": string(caKey)})
	e.Nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": sentinelGossipKey + "\n"})
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Service.AdoptFirst(context.Background(), "hel-1", pub, "203.0.113.4"); err != nil {
		t.Fatal(err)
	}
	e.Nomad.AddNode("node-1", "hel-1", "ready", "10.99.0.1")
	return caCert, caKey
}

type serverAnswer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Index  int    `json:"index"`
	State  string `json:"state"`
	Checks []struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
	} `json:"checks"`
	ExpiresAt string `json:"expiresAt"`
}

type joinAnswer struct {
	Server      serverAnswer `json:"server"`
	Token       string       `json:"token"`
	Command     string       `json:"command"`
	CommandNote string       `json:"commandNote"`
	ExpiresAt   string       `json:"expiresAt"`
}

func addServer(t *testing.T, e *testEnv, body string) joinAnswer {
	t.Helper()
	status, hdr, raw := call(t, http.MethodPost, e.URL+"/v1/servers", "application/json", body)
	if status != http.StatusCreated {
		t.Fatalf("addServer = %d %s", status, raw)
	}
	if hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("addServer's Cache-Control = %q, want no-store", hdr.Get("Cache-Control"))
	}
	checkSchema(t, loadContract(t), raw, "ServerJoin")
	return decodeAs[joinAnswer](t, raw)
}

func TestAddServer(t *testing.T) {
	t.Run("a cluster that isn't set up", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/servers", "application/json", `{"name": ""}`)
		body := wantError(t, "addServer", status, raw, http.StatusConflict, codeClusterNotReady, "")
		if !strings.HasPrefix(body.Error.Message, "Run the installer again on this server first:") {
			t.Errorf("message = %q", body.Error.Message)
		}
	})
	t.Run("a join with the command and the waiting server", func(t *testing.T) {
		e := newTestEnv(t, "")
		withCluster(t, e)
		j := addServer(t, e, `{"name": "fsn-2", "pool": "default"}`)
		if !strings.HasPrefix(j.Token, "cvjn_") || j.Server.State != "waiting" || j.Server.Name != "fsn-2" || j.Server.Index != 2 || j.Server.ExpiresAt == "" {
			t.Errorf("answer = %+v", j)
		}
		if !strings.Contains(j.Command, "--join "+j.Token+" --url http://127.0.0.1:") || j.CommandNote == "" {
			t.Errorf("command = %q, note = %q (a test build is a development build)", j.Command, j.CommandNote)
		}
		// The body may be left out.
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/servers", "", ""); status != http.StatusCreated {
			t.Errorf("addServer with no body = %d %s", status, raw)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		e := newTestEnv(t, "")
		withCluster(t, e)
		for _, c := range []struct {
			body, code, field string
			status            int
		}{
			{`{"name": "Bad Name"}`, codeInvalidField, "name", 422},
			{`{"pool": "Bad Pool"}`, codeInvalidField, "pool", 422},
			{`{"name": "hel-1"}`, codeServerExists, "name", 409},
			{`{"nope": 1}`, codeUnknownField, "nope", 400},
		} {
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/servers", "application/json", c.body)
			wantError(t, "addServer "+c.body, status, raw, c.status, c.code, c.field)
		}
	})
	t.Run("a full cluster", func(t *testing.T) {
		e := newTestEnv(t, "")
		withCluster(t, e)
		for i := 2; i <= 63; i++ {
			addServer(t, e, `{}`)
		}
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/servers", "application/json", `{}`)
		wantError(t, "the 64th server", status, raw, http.StatusConflict, codeClusterFull, "")
	})
}

// checkPart checks one part of a JSON answer against a schema of the contract.
func checkPart(t *testing.T, part json.RawMessage, schema string) {
	t.Helper()
	checkSchema(t, loadContract(t), part, schema)
}

func TestServerReads(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	j := addServer(t, e, `{"name": "later"}`)
	e.Nomad.AddNode("node-lab", "lab", "ready", "")

	status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/servers", "", "")
	if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("listServers = %d %s", status, raw)
	}
	list := decodeAs[struct {
		Cluster json.RawMessage   `json:"cluster"`
		Servers []json.RawMessage `json:"servers"`
	}](t, raw)
	checkPart(t, list.Cluster, "Cluster")
	byName := map[string]map[string]any{}
	for _, part := range list.Servers {
		checkPart(t, part, "Server")
		m := decodeAs[map[string]any](t, part)
		byName[m["name"].(string)] = m
	}
	if len(byName) != 3 {
		t.Fatalf("servers = %v, want hel-1, later and lab", byName)
	}
	first := byName["hel-1"]
	if first["state"] != "ready" || first["first"] != true || first["ingress"] != true || first["address"] != "203.0.113.4" ||
		first["privateAddress"] != "10.98.0.1" || first["memory"] != nil || first["cpu"] != nil || first["disks"] != nil {
		t.Errorf("hel-1 = %v (numbers are null with no sample)", first)
	}
	if byName["lab"]["managed"] != false || byName["lab"]["state"] != "ready" || byName["later"]["state"] != "waiting" {
		t.Errorf("lab = %v, later = %v", byName["lab"], byName["later"])
	}

	// One server, by name and by the join's ID.
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/hel-1", "", "")
	if status != http.StatusOK {
		t.Fatalf("getServer = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Server")
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/joins/"+j.Server.ID, "", "")
	if status != http.StatusOK || decodeAs[serverAnswer](t, raw).State != "waiting" {
		t.Errorf("getServerJoin = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Server")
	for _, path := range []string{"/v1/servers/nobody", "/v1/servers/joins/nobody"} {
		status, _, raw = call(t, http.MethodGet, e.URL+path, "", "")
		wantError(t, "GET "+path, status, raw, http.StatusNotFound, codeServerNotFound, "")
	}
}

func TestRemoveServerEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	j := addServer(t, e, `{"name": "two"}`)

	status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/servers/hel-1", "", "")
	wantError(t, "remove the first server", status, raw, http.StatusConflict, codeProtected, "")
	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/servers/nobody", "", "")
	wantError(t, "remove nobody", status, raw, http.StatusNotFound, codeServerNotFound, "")
	// A waiting join is cancelled by its ID.
	if status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/servers/"+j.Server.ID, "", ""); status != http.StatusNoContent {
		t.Errorf("cancel a waiting join = %d %s", status, raw)
	}
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/joins/"+j.Server.ID, "", "")
	wantError(t, "the cancelled join", status, raw, http.StatusNotFound, codeServerNotFound, "")
}

// consoleOps are the five operations of the console, which a deploy token is denied.
var consoleOps = []struct{ method, path, body string }{
	{http.MethodGet, "/v1/servers", ""},
	{http.MethodPost, "/v1/servers", `{}`},
	{http.MethodGet, "/v1/servers/hel-1", ""},
	{http.MethodGet, "/v1/servers/joins/abc", ""},
	{http.MethodDelete, "/v1/servers/hel-1", ""},
}

// nodeOps are the three calls of the agent. None answers to an API token, a session or a deploy
// token: the 401 is the node token's own.
var nodeOps = []struct{ method, path, body string }{
	{http.MethodPost, "/v1/node/sync", `{}`},
	{http.MethodPost, "/v1/node/certificate", `{"csr": "x"}`},
}

func TestServersAndTheScopeAndTheNodeCredentials(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	shopProject(t, e)
	deployToken := scopedToken(t, e, "shop", "web")

	// T017: the console's five operations are not in a deploy token's table.
	for _, op := range consoleOps {
		if key := op.method + " " + strings.NewReplacer("/hel-1", "/{name}", "/joins/abc", "/joins/{id}").Replace(op.path); slicesContain(allowedForDeployTokens, key) {
			t.Errorf("%s is allowed for deploy tokens", key)
		}
		status, _, raw := callWith(t, deployToken, op.method, e.RawURL+op.path, "application/json", op.body)
		if status != http.StatusForbidden || !strings.Contains(string(raw), codeTokenScope) {
			t.Errorf("%s %s with a deploy token = %d %s, want 403 token_scope", op.method, op.path, status, raw)
		}
		status, _, raw = callWith(t, "", op.method, e.RawURL+op.path, "application/json", op.body)
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s with no credentials = %d %s, want 401", op.method, op.path, status, raw)
		}
	}
	// The node calls: any other credential is the same 401 node_token_invalid.
	nodeToken := "cvnd_" + strings.Repeat("x", 43)
	for _, op := range nodeOps {
		for name, auth := range map[string]string{
			"an API token": "Bearer " + e.Token, "a deploy token": deployToken, "a wrong node token": "Bearer " + nodeToken,
			"no credentials": "", "a join token": "Bearer cvjn_" + strings.Repeat("x", 43), "another scheme": "Basic " + nodeToken,
		} {
			status, _, raw := callWith(t, auth, op.method, e.RawURL+op.path, "application/json", op.body)
			wantError(t, fmt.Sprintf("%s with %s", op.path, name), status, raw, http.StatusUnauthorized, codeNodeTokenInvalid, "")
		}
	}
	// A node token isn't an API token either: the console's calls don't take it.
	for _, op := range consoleOps {
		if status, _, raw := callWith(t, "Bearer "+nodeToken, op.method, e.RawURL+op.path, "application/json", op.body); status != http.StatusUnauthorized {
			t.Errorf("%s %s with a node token = %d %s, want 401", op.method, op.path, status, raw)
		}
	}
	// The node calls are not on the webhook-only listener.
	hook := httptest.NewServer(NewWebhook(e.Service, e.log))
	t.Cleanup(hook.Close)
	for _, path := range []string{"/v1/join", "/v1/node/sync", "/v1/node/certificate", "/v1/servers"} {
		if status, _, _ := call(t, http.MethodPost, hook.URL+path, "application/json", `{}`); status != http.StatusNotFound {
			t.Errorf("POST %s on the webhook listener = %d, want 404", path, status)
		}
	}
}

func slicesContain(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestJoinCallRefusals(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	joinBody := func(token string) string {
		_, pub, _ := mesh.NewKey()
		_, csr, _ := clusterca.NewKeyAndCSR()
		b, _ := json.Marshal(map[string]any{"token": token, "publicKey": pub, "csr": string(csr), "address": "203.0.113.9"})
		return string(b)
	}
	// A token that isn't one: 401, five times, then 429 from that address.
	for i := 0; i < 5; i++ {
		status, hdr, raw := call(t, http.MethodPost, e.URL+"/v1/join", "application/json", joinBody("cvjn_"+strings.Repeat("x", 43)))
		body := wantError(t, "join with a wrong token", status, raw, http.StatusUnauthorized, codeJoinTokenInvalid, "")
		if !strings.HasPrefix(body.Error.Message, "This join token isn't valid: it was used, expired, or never existed.") || hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("message %q, cache %q", body.Error.Message, hdr.Get("Cache-Control"))
		}
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/join", "application/json", joinBody("cvjn_"+strings.Repeat("x", 43)))
	wantError(t, "the sixth try", status, raw, http.StatusTooManyRequests, codeTooManyAttempts, "")
	// The address is locked, whatever the token: even a real one is refused until the lockout ends.
	j := addServer(t, e, `{}`)
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/join", "application/json", joinBody(j.Token))
	wantError(t, "a real token while locked", status, raw, http.StatusTooManyRequests, codeTooManyAttempts, "")
	// The sign-in limit is another count: signing in isn't affected.
	if status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers", "", ""); status != http.StatusOK {
		t.Errorf("the console while join is limited = %d %s", status, raw)
	}
}

func TestJoinCallRequestRules(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	j := addServer(t, e, `{}`)
	_, pub, _ := mesh.NewKey()
	_, csr, _ := clusterca.NewKeyAndCSR()
	body := func(m map[string]any) string {
		base := map[string]any{"token": j.Token, "publicKey": pub, "csr": string(csr), "address": "203.0.113.9"}
		for k, v := range m {
			base[k] = v
		}
		b, _ := json.Marshal(base)
		return string(b)
	}
	cases := []struct {
		name   string
		body   string
		status int
		code   string
		field  string
	}{
		{"a bad key", body(map[string]any{"publicKey": "nope"}), 422, codeInvalidField, "publicKey"},
		{"a bad CSR", body(map[string]any{"csr": "nope"}), 422, codeInvalidField, "csr"},
		{"a name for an address", body(map[string]any{"address": "evil.example.com"}), 422, codeInvalidField, "address"},
		{"loopback", body(map[string]any{"address": "127.0.0.1"}), 422, codeInvalidField, "address"},
		{"a long fact", body(map[string]any{"facts": map[string]any{"hostname": strings.Repeat("h", 300)}}), 422, codeInvalidField, "facts.hostname"},
		{"an unknown field", body(map[string]any{"nope": 1}), 400, codeUnknownField, "nope"},
	}
	for _, c := range cases {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/join", "application/json", c.body)
		wantError(t, c.name, status, raw, c.status, c.code, c.field)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/join", "text/plain", "x"); status != http.StatusUnsupportedMediaType {
		t.Errorf("join with text/plain = %d %s", status, raw)
	}
	// Over 64 KiB.
	big := body(map[string]any{"csr": strings.Repeat("A", 70<<10)})
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/join", "application/json", big)
	wantError(t, "a body over 64 KiB", status, raw, http.StatusRequestEntityTooLarge, codeBodyTooLarge, "")
	// None of these spent the token.
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/join", "application/json", body(nil)); status != http.StatusOK {
		t.Errorf("the token after the mistakes = %d %s", status, raw)
	}
}

// fakeHost stands in for the machine's programs, as the agent's own tests do.
type fakeHost struct {
	hostname string
}

func (f *fakeHost) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch {
	case name == "uname" && len(args) > 0 && args[0] == "-n":
		return []byte(f.hostname + "\n"), nil
	case name == "uname":
		return []byte("6.8.0-134-generic\n"), nil
	case name == "docker":
		return []byte("27.5.1\n"), nil
	case name == "nomad":
		return []byte("Nomad v2.0.7\n"), nil
	case name == "df":
		return []byte("Filesystem 1M-blocks Used Available Capacity Mounted on\n/dev/vda1 190000 30000 160000 16% /\n"), nil
	case name == "ip" && len(args) >= 2 && args[0] == "link" && args[1] == "show":
		return nil, errors.New("Device does not exist")
	}
	return nil, nil
}

// localNomad answers what the agent reads from its own Nomad: a leader, and no raft peers.
func localNomad(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status/leader":
			fmt.Fprint(w, `"10.98.0.1:4647"`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestAServerJoinsThroughTheRealAgent is the end-to-end proof of the protocol: a real
// nodeagent.Agent on a temporary root, with a fake runner, joins through these handlers with a token
// from addServer, then syncs until its four checks pass (the Nomad node it would have registered is
// added to the fake Nomad), renews its certificate and is removed. Both sides are checked against
// each other, with nothing in the middle.
func TestAServerJoinsThroughTheRealAgent(t *testing.T) {
	e := newTestEnv(t, "")
	caCert, caKey := withCluster(t, e)
	j := addServer(t, e, `{"name": "fsn-e2e"}`)

	var stdout, stderr bytes.Buffer
	root := t.TempDir()
	agent := &nodeagent.Agent{
		Root: root, Runner: &fakeHost{hostname: "fsn-e2e.example.com"}, HTTP: nodeagent.NewHTTPClient(false),
		Version: "0.0.1-e2e", NomadAddr: localNomad(t).URL, Stdout: &stdout, Stderr: &stderr,
	}
	ctx := context.Background()

	if err := agent.Join(ctx, nodeagent.JoinOptions{URL: e.RawURL, Token: j.Token, Address: "203.0.113.9"}); err != nil {
		t.Fatalf("Join: %v\nstderr: %s", err, stderr.String())
	}
	for _, f := range []string{"etc/caravana-node/state.json", "etc/caravana-node/node.env", "etc/caravana-node/mesh.key",
		"etc/nomad.d/tls/ca.pem", "etc/nomad.d/tls/agent.pem", "etc/nomad.d/tls/agent-key.pem", "etc/nomad.d/nomad.hcl"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("after the join: %v", err)
		}
	}
	st, err := agent.LoadState()
	if err != nil || st.Index != 2 || st.Name != "fsn-e2e" || st.Role != "client" || len(st.Peers) != 1 || st.Peers[0].Index != 1 {
		t.Fatalf("state = %+v, %v", st, err)
	}

	// The console sees it joining, and the token no longer works.
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/servers/joins/"+j.Server.ID, "", "")
	if got := decodeAs[serverAnswer](t, raw); status != http.StatusOK || got.State != "joining" || got.Name != "fsn-e2e" || len(got.Checks) != 4 {
		t.Errorf("getServerJoin after the join = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Server")
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/join", "application/json", `{"token": "`+j.Token+`", "publicKey": "x", "csr": "x"}`)
	wantError(t, "the join token a second time", status, raw, http.StatusUnauthorized, codeJoinTokenInvalid, "")

	// Syncs: Docker answers and the first server pings, but Nomad has no node yet; then it does.
	stdout.Reset()
	if err := agent.Sync(ctx, nodeagent.SyncOptions{PrintChecks: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !strings.Contains(stdout.String(), "check ready open") || !strings.Contains(stdout.String(), "state joining") {
		t.Errorf("first sync printed %q, want the ready check open and state joining", stdout.String())
	}
	e.Nomad.AddNode("node-2", "fsn-e2e", "ready", "10.99.0.2")
	stdout.Reset()
	if err := agent.Sync(ctx, nodeagent.SyncOptions{PrintChecks: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for _, want := range []string{"check connected ok", "check docker ok", "check network ok", "check ready ok", "state ready"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("second sync printed %q, want it to contain %q", stdout.String(), want)
		}
	}
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/fsn-e2e", "", "")
	if got := decodeAs[serverAnswer](t, raw); status != http.StatusOK || got.State != "ready" {
		t.Errorf("getServer after the sync = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Server")
	var cluster struct {
		Cluster struct {
			Servers, Ready int
		} `json:"cluster"`
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers", "", "")
	if json.Unmarshal(raw, &cluster); cluster.Cluster.Servers != 2 || cluster.Cluster.Ready != 2 {
		t.Errorf("cluster = %+v", cluster)
	}

	// A renewal through the real certificate call: a new key and certificate, and Nomad reloaded.
	before, _ := os.ReadFile(filepath.Join(root, "etc/nomad.d/tls/agent.pem"))
	if err := agent.Sync(ctx, nodeagent.SyncOptions{Renew: true}); err != nil {
		t.Fatalf("Sync --renew: %v\n%s", err, stderr.String())
	}
	after, _ := os.ReadFile(filepath.Join(root, "etc/nomad.d/tls/agent.pem"))
	key, _ := os.ReadFile(filepath.Join(root, "etc/nomad.d/tls/agent-key.pem"))
	if bytes.Equal(before, after) {
		t.Error("the certificate wasn't renewed")
	}
	if err := clusterca.Check(caCert, after, key, clusterca.Client, time.Now()); err != nil {
		t.Errorf("the renewed certificate: %v", err)
	}

	// T018: the join token, the node token, the agent's private key, the CA's key and the gossip key
	// are in no log line, no list answer and no stored row.
	env, _ := os.ReadFile(filepath.Join(root, "etc/caravana-node/node.env"))
	nodeTok := regexp.MustCompile(`cvnd_[A-Za-z0-9_-]{43}`).FindString(string(env))
	if nodeTok == "" {
		t.Fatal("no node token in node.env")
	}
	secrets := map[string]string{
		"the join token": j.Token, "the node token": nodeTok, "the agent's private key": pemBody(string(key)),
		"the CA's private key": pemBody(string(caKey)), "the gossip key": sentinelGossipKey,
	}
	for _, path := range []string{"/v1/servers", "/v1/servers/fsn-e2e", "/v1/servers/hel-1", "/v1/attention", "/v1/activity"} {
		_, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		for what, secret := range secrets {
			if strings.Contains(string(raw), secret) {
				t.Errorf("GET %s holds %s", path, what)
			}
		}
	}
	for what, secret := range secrets {
		if strings.Contains(e.Logs.String(), secret) {
			t.Errorf("the log holds %s", what)
		}
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Errorf("the agent's output holds %s", what)
		}
	}
	files, _ := filepath.Glob(filepath.Join(e.Dir, "caravana.db*"))
	if len(files) == 0 {
		t.Fatal("no database file")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for what, secret := range secrets {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("%s holds %s", filepath.Base(f), what)
			}
		}
	}

	// Removal: only once Nomad says the node is down; then the agent's token stops working.
	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/servers/fsn-e2e", "", "")
	body := wantError(t, "remove a running server", status, raw, http.StatusConflict, codeServerRunning, "")
	if body.Error.Message != "fsn-e2e is running. Stop it, or run the installer with --leave on it, then remove it." {
		t.Errorf("message = %q", body.Error.Message)
	}
	e.Nomad.SetNodeStatusOf("node-2", "down")
	if status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/servers/fsn-e2e", "", ""); status != http.StatusNoContent {
		t.Fatalf("remove a down server = %d %s", status, raw)
	}
	stderr.Reset()
	if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil {
		t.Fatalf("Sync after removal: %v", err)
	}
	if !strings.Contains(stderr.String(), "removed from the cluster") {
		t.Errorf("sync after removal said %q, want it to say the server was removed", stderr.String())
	}
}

// pemBody is the base64 of a PEM block's first line of data: a string that is in the PEM and in no
// answer that doesn't carry the key.
func pemBody(pem string) string {
	for _, line := range strings.Split(pem, "\n") {
		if line != "" && !strings.HasPrefix(line, "-----") {
			return line
		}
	}
	return pem
}
