package nodeagent

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
)

const issuerLine = `oidc_issuer = "https://console.example.com"`

// oidc_issuer is a setting of a Nomad server: it is inside server { } and a client never renders it,
// whatever its state says.
func TestIssuerIsRenderedInTheServerBlockOnly(t *testing.T) {
	for name, c := range renderCases() {
		got := RenderNomad(c.state, c.key)
		has := strings.Contains(got, "oidc_issuer")
		if want := c.state.Role == RoleServer && c.state.Issuer != ""; has != want {
			t.Errorf("%s: oidc_issuer present = %v, want %v", name, has, want)
		}
		if has {
			server := normBlock(got, "server {")
			if !strings.Contains(server, issuerLine) {
				t.Errorf("%s: the issuer line isn't inside server { }:\n%s", name, got)
			}
		}
	}
	client := State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleClient, Servers: []int{1}, Issuer: testIssuer}
	if got := RenderNomad(client, ""); strings.Contains(got, "oidc_issuer") {
		t.Errorf("a client rendered the issuer:\n%s", got)
	}
}

func TestParseSyncIssuer(t *testing.T) {
	tests := []struct {
		name   string
		issuer any // nil: the member is absent
		want   string
		field  string // "" means accepted
	}{
		{"absent", nil, "", ""},
		{"empty", "", "", ""},
		{"good", "https://console.example.com", "https://console.example.com", ""},
		{"a trailing slash is trimmed", "https://console.example.com/", "https://console.example.com", ""},
		{"a port", "https://console.example.com:8443", "https://console.example.com:8443", ""},
		{"localhost", "https://localhost", "https://localhost", ""},
		{"http", "http://console.example.com", "", "issuer"},
		{"a path", "https://console.example.com/x", "", "issuer"},
		{"a query", "https://console.example.com?a=b", "", "issuer"},
		{"a fragment", "https://console.example.com#x", "", "issuer"},
		{"user information", "https://u:p@console.example.com", "", "issuer"},
		{"an IPv4 address", "https://203.0.113.4", "", "issuer"},
		{"an IPv6 address", "https://[::1]", "", "issuer"},
		{"a bad host", "https://-bad.example.com", "", "issuer"},
		{"a bad port", "https://console.example.com:70000", "", "issuer"},
		{"a quote", `https://console.example.com"; evil = "1`, "", "issuer"},
		{"not a string", 5, "", "issuer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := syncAnswer()
			if tc.issuer != nil {
				m["issuer"] = tc.issuer
			}
			ans, err := ParseSync(asJSON(t, m), Expect{State: syncState(), CA: nil, Now: t0})
			if tc.field == "" {
				if err != nil {
					t.Fatalf("ParseSync: %v", err)
				}
				if ans.Issuer != tc.want {
					t.Errorf("Issuer = %q, want %q", ans.Issuer, tc.want)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Fatalf("ParseSync = %v, want a refusal of %q", err, tc.field)
			}
			if s, ok := tc.issuer.(string); ok && strings.Contains(err.Error(), s) {
				t.Errorf("the error quotes the value: %v", err)
			}
		})
	}
}

// A state.json written before the issuer existed loads, and one with a bad issuer doesn't.
func TestStateIssuerIsBackwardCompatible(t *testing.T) {
	a := newTestAgent(t, nil)
	old := `{"version":1,"index":3,"name":"fsn-2","pool":"default","first":false,"role":"client","servers":[1],"cpuTotalCompute":0,"peers":[]}`
	a.writeFiles(t, map[string]string{"etc/caravana-node/state.json": old})
	st, err := a.LoadState()
	if err != nil || st.Issuer != "" {
		t.Fatalf("an old state.json: %+v, %v", st, err)
	}
	st.Issuer = testIssuer
	if err := a.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if got, err := a.LoadState(); err != nil || got.Issuer != testIssuer {
		t.Errorf("round trip: %+v, %v", got, err)
	}
	st.Issuer = "http://nope"
	if err := a.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LoadState(); err == nil {
		t.Error("a state.json with a bad issuer was accepted")
	}
	// Without an issuer the member is left out, so an older agent's file and this one look the same.
	st.Issuer = ""
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "issuer") {
		t.Errorf("an empty issuer is written: %s", b)
	}
}

func TestReportCarriesTheRenderedIssuer(t *testing.T) {
	a := newTestAgent(t, nil)
	st := State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleServer, Servers: []int{1, 3}, Issuer: testIssuer}
	r := a.Report(ctx(), st)
	if r.Issuer != testIssuer {
		t.Errorf("Report.Issuer = %q", r.Issuer)
	}
	b, _ := json.Marshal(a.Report(ctx(), State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleClient}))
	if strings.Contains(string(b), "issuer") {
		t.Errorf("a report without an issuer names one: %s", b)
	}
}

// serverAgent is a joined agent that has been promoted to a Nomad server, with what it ran forgotten.
func serverAgent(t *testing.T, c *console) *testAgent {
	t.Helper()
	a := newTestAgent(t, c)
	a.joined(t)
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["role"] = "server"
		m["gossipKey"] = sentinelGossip
		m["servers"] = []int{1, 3}
	}))
	c.on("/v1/node/certificate", func(req map[string]any, _ string) (int, any) {
		return 200, map[string]any{"certificate": c.sign(req["csr"], clusterca.Server, 3), "ca": string(c.caCert)}
	})
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	a.runner.reset()
	a.stdout.Reset()
	return a
}

func restarts(a *testAgent) int {
	n := 0
	for _, cmd := range a.runner.commands() {
		if cmd == "systemctl restart nomad" {
			n++
		}
	}
	return n
}

func answerWithIssuer(a *testAgent, role, issuer string) func(map[string]any, string) (int, any) {
	return a.syncAnswer(func(m map[string]any) {
		m["role"] = role
		m["servers"] = []int{1, 3}
		if role == "server" {
			m["gossipKey"] = sentinelGossip
		}
		if issuer != "" {
			m["issuer"] = issuer
		}
	})
}

func TestSyncIssuerOnANomadServerRendersAndRestartsOnce(t *testing.T) {
	c := newConsole(t)
	a := serverAgent(t, c)
	c.on("/v1/node/sync", answerWithIssuer(a, "server", testIssuer))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.read(t, "etc/nomad.d/nomad.hcl"), issuerLine) {
		t.Errorf("nomad.hcl has no issuer:\n%s", a.read(t, "etc/nomad.d/nomad.hcl"))
	}
	if n := restarts(a); n != 1 {
		t.Errorf("Nomad was restarted %d times: %v", n, a.runner.commands())
	}
	if st, _ := a.LoadState(); st.Issuer != testIssuer {
		t.Errorf("state issuer = %q", st.Issuer)
	}
	// The next sync reports it, and the same answer again changes nothing.
	a.runner.reset()
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	reqs := c.calls("/v1/node/sync")
	if last := reqs[len(reqs)-1].body; last["issuer"] != testIssuer {
		t.Errorf("the report's issuer = %v", last["issuer"])
	}
	if n := restarts(a); n != 0 {
		t.Errorf("a second identical answer restarted Nomad %d times", n)
	}
}

func TestSyncIssuerChangeAndRemovalOnANomadServer(t *testing.T) {
	c := newConsole(t)
	a := serverAgent(t, c)
	c.on("/v1/node/sync", answerWithIssuer(a, "server", testIssuer))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	a.runner.reset()
	c.on("/v1/node/sync", answerWithIssuer(a, "server", "https://other.example.org"))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if hcl := a.read(t, "etc/nomad.d/nomad.hcl"); !strings.Contains(hcl, `oidc_issuer = "https://other.example.org"`) || strings.Contains(hcl, issuerLine) {
		t.Errorf("the changed issuer isn't what nomad.hcl says:\n%s", hcl)
	}
	if restarts(a) != 1 {
		t.Errorf("restarts after a change = %d", restarts(a))
	}
	a.runner.reset()
	c.on("/v1/node/sync", answerWithIssuer(a, "server", ""))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.read(t, "etc/nomad.d/nomad.hcl"), "oidc_issuer") || restarts(a) != 1 {
		t.Errorf("an emptied issuer: restarts %d\n%s", restarts(a), a.read(t, "etc/nomad.d/nomad.hcl"))
	}
}

func TestSyncIssuerOnAClientIsKeptAndRestartsNothing(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	hcl := a.read(t, "etc/nomad.d/nomad.hcl")
	c.on("/v1/node/sync", answerWithIssuer(a, "client", testIssuer))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.LoadState(); st.Issuer != testIssuer {
		t.Errorf("a client didn't keep the issuer: %q", st.Issuer)
	}
	if got := a.read(t, "etc/nomad.d/nomad.hcl"); got != hcl || strings.Contains(got, "oidc_issuer") {
		t.Errorf("a client's nomad.hcl changed:\n%s", got)
	}
	for _, cmd := range a.runner.commands() {
		if strings.HasPrefix(cmd, "systemctl") {
			t.Errorf("ran %s", cmd)
		}
	}
}

// A client that becomes a Nomad server in the same sync that brings the issuer renders it with the
// promotion's one restart.
func TestSyncPromotionTakesTheIssuerWithOneRestart(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	c.on("/v1/node/sync", answerWithIssuer(a, "server", testIssuer))
	c.on("/v1/node/certificate", func(req map[string]any, _ string) (int, any) {
		return 200, map[string]any{"certificate": c.sign(req["csr"], clusterca.Server, 3), "ca": string(c.caCert)}
	})
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.read(t, "etc/nomad.d/nomad.hcl"), issuerLine) {
		t.Error("the promoted server's nomad.hcl has no issuer")
	}
	if n := restarts(a); n != 1 {
		t.Errorf("restarts = %d, want 1: %v", n, a.runner.commands())
	}
	if st, _ := a.LoadState(); st.Role != RoleServer || st.Issuer != testIssuer {
		t.Errorf("state = %+v", st)
	}
}

// A restart that fails leaves the saved issuer as it was, so the report still says what Nomad runs
// with and the next sync tries again.
func TestSyncIssuerRestartFailureRetries(t *testing.T) {
	c := newConsole(t)
	a := serverAgent(t, c)
	c.on("/v1/node/sync", answerWithIssuer(a, "server", testIssuer))
	a.runner.failOn["systemctl restart"] = errors.New("unit not found")
	if err := a.Sync(ctx(), SyncOptions{}); err == nil {
		t.Fatal("a failed restart was swallowed")
	}
	if st, _ := a.LoadState(); st.Issuer != "" {
		t.Errorf("the issuer was saved before Nomad took it: %q", st.Issuer)
	}
	delete(a.runner.failOn, "systemctl restart")
	a.runner.reset()
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if restarts(a) != 1 {
		t.Errorf("the retry restarted Nomad %d times", restarts(a))
	}
	if st, _ := a.LoadState(); st.Issuer != testIssuer {
		t.Errorf("state issuer = %q", st.Issuer)
	}
}

// A refused issuer refuses the whole answer: nothing else in it is applied.
func TestSyncRefusedIssuerChangesNothing(t *testing.T) {
	c := newConsole(t)
	a := serverAgent(t, c)
	before := a.snapshot(t)
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["role"] = "server"
		m["gossipKey"] = sentinelGossip
		m["servers"] = []int{1, 3}
		m["issuer"] = "http://console.example.com"
		m["peers"] = []any{peerJSON(1, peerKey1, "203.0.113.4:51820"), peerJSON(2, peerKey2, "198.51.100.7:51820")}
	}))
	err := a.Sync(ctx(), SyncOptions{})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "issuer" {
		t.Fatalf("Sync = %v", err)
	}
	if !sameSnapshot(before, a.snapshot(t)) || len(a.runner.wgCalls()) != 0 {
		t.Error("a refused answer changed something")
	}
}

func TestInitFirstWithAnIssuer(t *testing.T) {
	a := newTestAgent(t, nil)
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4", Issuer: testIssuer}); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.LoadState(); st.Issuer != testIssuer {
		t.Errorf("state issuer = %q", st.Issuer)
	}
	if !strings.Contains(a.read(t, "etc/nomad.d/nomad.hcl"), issuerLine) {
		t.Error("the first server's nomad.hcl has no issuer from the start")
	}
	// A second run keeps what is there: the console's sync is what changes it afterwards.
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4", Issuer: "https://other.example.org"}); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.LoadState(); st.Issuer != testIssuer {
		t.Errorf("a second init-first changed the issuer to %q", st.Issuer)
	}
}

func TestInitFirstRefusesABadIssuerBeforeChangingAnything(t *testing.T) {
	a := newTestAgent(t, nil)
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4", Issuer: "http://console.example.com"}); err == nil {
		t.Fatal("a bad issuer was accepted")
	}
	if _, err := os.Stat(a.file("etc/caravana-node")); err == nil {
		t.Error("the agent's folder was made before the issuer was checked")
	}
}
