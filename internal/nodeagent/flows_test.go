package nodeagent

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
)

func ctx() context.Context { return context.Background() }

// ---- init-first --------------------------------------------------------------------------------

func TestInitFirst(t *testing.T) {
	a := newTestAgent(t, nil)
	a.runner.hostname = "Hel-1.example.com"
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4", CPUTotalCompute: 4000}); err != nil {
		t.Fatal(err)
	}
	modes := map[string]uint32{
		"etc/caravana-node":             0o700,
		"etc/caravana-node/mesh.key":    0o600,
		"etc/caravana-node/state.json":  0o600,
		"etc/caravana-node/gossip.key":  0o600,
		"etc/caravana-node/ca-key.pem":  0o600,
		"etc/nomad.d/tls/ca.pem":        0o644,
		"etc/nomad.d/tls/agent.pem":     0o644,
		"etc/nomad.d/tls/agent-key.pem": 0o600,
		"etc/nomad.d/nomad.hcl":         0o600,
	}
	for name, want := range modes {
		if got := a.mode(t, name); uint32(got) != want {
			t.Errorf("%s mode = %o, want %o", name, got, want)
		}
	}
	st, err := a.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Index != 1 || !st.First || st.Role != RoleServer || st.Name != "hel-1" || st.Pool != "default" ||
		!reflect.DeepEqual(st.Servers, []int{1}) || st.CPUTotalCompute != 4000 || len(st.Peers) != 0 {
		t.Errorf("state = %+v", st)
	}
	// The certificate is the cluster CA's, for a server, for the key on disk.
	ca, cert, key := a.read(t, "etc/nomad.d/tls/ca.pem"), a.read(t, "etc/nomad.d/tls/agent.pem"), a.read(t, "etc/nomad.d/tls/agent-key.pem")
	if err := clusterca.Check([]byte(ca), []byte(cert), []byte(key), clusterca.Server, t0); err != nil {
		t.Errorf("the server certificate: %v", err)
	}
	// The CA's own key is the key of ca.pem.
	if _, err := clusterca.Sign([]byte(ca), []byte(a.read(t, "etc/caravana-node/ca-key.pem")), mustCSR(t), clusterca.Client, mustAddr("10.98.0.2"), t0); err != nil {
		t.Errorf("ca-key.pem isn't ca.pem's key: %v", err)
	}
	gossip := strings.TrimSpace(a.read(t, "etc/caravana-node/gossip.key"))
	if raw, err := base64.StdEncoding.DecodeString(gossip); err != nil || len(raw) != 32 {
		t.Errorf("gossip.key isn't base64 of 32 bytes: %v", err)
	}
	if got, want := a.read(t, "etc/nomad.d/nomad.hcl"), RenderNomad(st, gossip); got != want {
		t.Errorf("nomad.hcl isn't RenderNomad's:\n%s", got)
	}
	// It ran Up for index 1.
	if cmds := a.runner.commands(); len(cmds) == 0 || cmds[2] != "ip address replace 10.99.0.1/32 dev caravana0" {
		t.Errorf("commands: %v", cmds)
	}
	if pub, err := a.Show("public-key"); err != nil || !mesh.ValidPublicKey(pub) {
		t.Errorf("show public-key = %q, %v", pub, err)
	}
}

func mustCSR(t *testing.T) []byte {
	t.Helper()
	_, csr, err := clusterca.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	return csr
}

// Run twice it is one CA and nothing changes; after the installer has shredded the CA's key it
// stays gone.
func TestInitFirstTwiceIsOneCA(t *testing.T) {
	a := newTestAgent(t, nil)
	opts := InitFirstOptions{Address: "203.0.113.4"}
	if err := a.InitFirst(ctx(), opts); err != nil {
		t.Fatal(err)
	}
	before := a.snapshot(t)
	if err := a.InitFirst(ctx(), opts); err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(before, a.snapshot(t)) {
		t.Error("the second run changed a file")
	}
	// The installer stores the CA key in Nomad and shreds the file.
	if err := removeFile(a.file("etc/caravana-node/ca-key.pem")); err != nil {
		t.Fatal(err)
	}
	if err := a.InitFirst(ctx(), opts); err != nil {
		t.Fatal(err)
	}
	if a.exists("etc/caravana-node/ca-key.pem") {
		t.Error("a rerun made a second CA key")
	}
}

func TestInitFirstRefusals(t *testing.T) {
	a := newTestAgent(t, nil)
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "caravana.example.com"}); err == nil {
		t.Error("a name as --address was accepted")
	}
	if err := a.InitFirst(ctx(), InitFirstOptions{}); err == nil {
		t.Error("no --address was accepted")
	}
	// ca.pem without agent.pem.
	b := newTestAgent(t, nil)
	b.writeFiles(t, map[string]string{"etc/nomad.d/tls/ca.pem": "x"})
	err := b.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4"})
	if err == nil || !strings.Contains(err.Error(), "ca.pem exists but agent.pem doesn't") {
		t.Errorf("err = %v", err)
	}
	// A server that joined another cluster.
	c := newTestAgent(t, newConsole(t))
	c.joined(t)
	if err := c.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4"}); err == nil {
		t.Error("a joined server became a first server")
	}
}

func TestInitFirstUpdatesTheCPUSetting(t *testing.T) {
	a := newTestAgent(t, nil)
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4"}); err != nil {
		t.Fatal(err)
	}
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4", CPUTotalCompute: 2000}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.read(t, "etc/nomad.d/nomad.hcl"), "cpu_total_compute     = 2000") {
		t.Error("nomad.hcl wasn't re-rendered with the CPU setting")
	}
}

// ---- join --------------------------------------------------------------------------------------

func TestJoinWritesTheFiles(t *testing.T) {
	c := newConsole(t)
	c.serveJoin()
	a := newTestAgent(t, c)
	if err := a.Join(ctx(), JoinOptions{URL: c.srv.URL + "/", Token: sentinelJoinToken, Address: "203.0.113.9", Insecure: true, CPUTotalCompute: 4000}); err != nil {
		t.Fatal(err)
	}
	modes := map[string]uint32{
		"etc/caravana-node":             0o700,
		"etc/caravana-node/mesh.key":    0o600,
		"etc/caravana-node/state.json":  0o600,
		"etc/caravana-node/node.env":    0o600,
		"etc/nomad.d/tls/ca.pem":        0o644,
		"etc/nomad.d/tls/agent.pem":     0o644,
		"etc/nomad.d/tls/agent-key.pem": 0o600,
		"etc/nomad.d/nomad.hcl":         0o600,
	}
	for name, want := range modes {
		if got := a.mode(t, name); uint32(got) != want {
			t.Errorf("%s mode = %o, want %o", name, got, want)
		}
	}
	if a.exists("etc/caravana-node/gossip.key") {
		t.Error("a client has a gossip key")
	}
	st, err := a.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Index != 3 || st.Name != "fsn-2" || st.Role != RoleClient || st.First || st.CPUTotalCompute != 4000 ||
		!reflect.DeepEqual(st.Servers, []int{1}) || len(st.Peers) != 1 || st.Peers[0].Index != 1 {
		t.Errorf("state = %+v", st)
	}
	env, ok, err := a.LoadNodeEnv()
	if err != nil || !ok || env.Token != sentinelNodeToken || env.URL != c.srv.URL || !env.Insecure {
		t.Errorf("node.env = %+v %v %v", env, ok, err)
	}
	if got, want := a.read(t, "etc/nomad.d/nomad.hcl"), RenderNomad(st, ""); got != want {
		t.Errorf("nomad.hcl isn't RenderNomad's:\n%s", got)
	}
	if err := clusterca.Check([]byte(a.read(t, "etc/nomad.d/tls/ca.pem")), []byte(a.read(t, "etc/nomad.d/tls/agent.pem")), []byte(a.read(t, "etc/nomad.d/tls/agent-key.pem")), clusterca.Client, t0); err != nil {
		t.Errorf("the client certificate: %v", err)
	}

	// The request: the token in the body, no credentials in the header, our public key and facts.
	reqs := c.calls("/v1/join")
	if len(reqs) != 1 || reqs[0].auth != "" {
		t.Fatalf("join calls = %+v", reqs)
	}
	pub, _ := a.Show("public-key")
	body := reqs[0].body
	facts, _ := body["facts"].(map[string]any)
	if body["token"] != sentinelJoinToken || body["publicKey"] != pub || body["address"] != "203.0.113.9" ||
		!strings.Contains(body["csr"].(string), "CERTIFICATE REQUEST") ||
		facts["hostname"] != "Fsn-2.example.com" || facts["docker"] != "27.5.1" || facts["nomad"] != "2.0.7" || facts["agent"] != "0.18.0" || facts["kernel"] != "6.8.0-134-generic" {
		t.Errorf("join request = %+v", body)
	}
	// It ran up for index 3, with the peer in the config.
	if wgs := a.runner.wgCalls(); len(wgs) != 1 || !strings.Contains(wgs[0].conf, "PublicKey = "+peerKey1) {
		t.Errorf("wg calls = %+v", wgs)
	}
}

func TestJoinRefusalWritesOnlyTheMeshKey(t *testing.T) {
	c := newConsole(t)
	c.serveJoin()
	a := newTestAgent(t, c)
	badToken := "cvjn_" + strings.Repeat("x", 43)
	err := a.Join(ctx(), JoinOptions{URL: c.srv.URL, Token: badToken})
	var ce *ConsoleError
	if !errors.As(err, &ce) || ce.Status != 401 || ce.Code != "join_token_invalid" || !strings.Contains(err.Error(), "never existed") {
		t.Fatalf("err = %v", err)
	}
	snap := a.snapshot(t)
	if len(snap) != 1 {
		t.Errorf("files after a refusal: %v", keys(snap))
	}
	if _, ok := snap["/etc/caravana-node/mesh.key"]; !ok {
		t.Errorf("no mesh.key: %v", keys(snap))
	}
	if len(a.runner.wgCalls()) != 0 {
		t.Error("ran wg after a refusal")
	}
	// Retrying reuses the key.
	before := a.read(t, "etc/caravana-node/mesh.key")
	_ = a.Join(ctx(), JoinOptions{URL: c.srv.URL, Token: badToken})
	if a.read(t, "etc/caravana-node/mesh.key") != before {
		t.Error("a retry made a new mesh.key")
	}
	if strings.Contains(err.Error(), badToken) {
		t.Error("the error holds the token")
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestJoinBadAnswersChangeNothing(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"loopback endpoint": func(m map[string]any) { m["peers"] = []any{peerJSON(1, peerKey1, "127.0.0.1:51820")} },
		"index 99":          func(m map[string]any) { m["index"] = 99 },
		"no token":          func(m map[string]any) { delete(m, "nodeToken") },
		"servers unknown":   func(m map[string]any) { m["servers"] = []int{1, 7} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := newConsole(t)
			c.on("/v1/join", func(req map[string]any, _ string) (int, any) {
				m := map[string]any{
					"name": "fsn-2", "index": 3, "pool": "default", "role": "client", "servers": []int{1},
					"ca": string(c.caCert), "certificate": c.sign(req["csr"], clusterca.Client, 3), "nodeToken": sentinelNodeToken,
					"peers": []any{peerJSON(1, peerKey1, "203.0.113.4:51820")},
				}
				mutate(m)
				return 200, m
			})
			a := newTestAgent(t, c)
			err := a.Join(ctx(), JoinOptions{URL: c.srv.URL, Token: sentinelJoinToken})
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("err = %v", err)
			}
			if snap := a.snapshot(t); len(snap) != 1 {
				t.Errorf("files after a bad answer: %v", keys(snap))
			}
			if strings.Contains(err.Error(), sentinelNodeToken) {
				t.Error("the error holds the node token")
			}
		})
	}
}

func TestJoinOnlyTrustsAGoodAddressAndToken(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	for name, o := range map[string]JoinOptions{
		"no url":         {Token: sentinelJoinToken},
		"http remote":    {URL: "http://caravana.example.com", Token: sentinelJoinToken},
		"a path":         {URL: "https://caravana.example.com/x", Token: sentinelJoinToken},
		"short token":    {URL: "https://caravana.example.com", Token: "cvjn_abc"},
		"wrong prefix":   {URL: "https://caravana.example.com", Token: "cvnd_" + strings.Repeat("a", 43)},
		"address a name": {URL: "https://caravana.example.com", Token: sentinelJoinToken, Address: "fsn.example.com"},
	} {
		if err := a.Join(ctx(), o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if c.total() != 0 || len(a.snapshot(t)) != 0 {
		t.Error("a bad option made a call or a file")
	}
}

func TestJoinConsoleUnreachable(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	url := c.srv.URL
	c.srv.Close()
	if err := a.Join(ctx(), JoinOptions{URL: url, Token: sentinelJoinToken}); err == nil {
		t.Error("joined a console that isn't there")
	}
	if len(a.snapshot(t)) > 1 {
		t.Errorf("files: %v", keys(a.snapshot(t)))
	}
}

func TestSecondJoinMakesNoCall(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	calls := c.total()
	before := a.snapshot(t)
	if err := a.Join(ctx(), JoinOptions{URL: c.srv.URL, Token: sentinelJoinToken}); err != nil {
		t.Fatal(err)
	}
	if c.total() != calls {
		t.Error("a second join called the console")
	}
	if !sameSnapshot(before, a.snapshot(t)) {
		t.Error("a second join changed a file")
	}
	if len(a.runner.wgCalls()) != 1 {
		t.Error("a second join didn't run up")
	}
}

// ---- sync --------------------------------------------------------------------------------------

func (a *testAgent) syncAnswer(extra func(m map[string]any)) func(map[string]any, string) (int, any) {
	return func(req map[string]any, auth string) (int, any) {
		m := map[string]any{
			"name": "fsn-2", "index": 3, "pool": "default", "role": "client", "servers": []int{1},
			"peers": []any{peerJSON(1, peerKey1, "203.0.113.4:51820")}, "renew": false, "state": "joining",
			"checks": []any{map[string]any{"name": "connected", "ok": true, "detail": "Connected to the cluster"}},
		}
		if extra != nil {
			extra(m)
		}
		return 200, m
	}
}

func TestSyncNotRegistered(t *testing.T) {
	a := newTestAgent(t, newConsole(t))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.stdout.String(), "not registered yet") || len(a.runner.calls) != 0 || a.console.total() != 0 {
		t.Errorf("stdout %q, calls %v", a.stdout.String(), a.runner.calls)
	}
}

func TestSyncSendsTheReportWithTheNodeToken(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	c.on("/v1/node/sync", a.syncAnswer(nil))
	if err := a.Sync(ctx(), SyncOptions{PrintChecks: true}); err != nil {
		t.Fatal(err)
	}
	reqs := c.calls("/v1/node/sync")
	if len(reqs) != 1 || reqs[0].auth != "Bearer "+sentinelNodeToken {
		t.Fatalf("sync calls = %+v", reqs)
	}
	b := reqs[0].body
	if b["role"] != "client" || b["dockerRunning"] != true || b["agent"] != "0.18.0" || b["nomadLeader"] != true {
		t.Errorf("report = %+v", b)
	}
	if b["certificateNotAfter"] == nil {
		t.Error("the report has no certificate end")
	}
	// Nothing differed, so nothing was applied.
	if len(a.runner.wgCalls()) != 0 {
		t.Error("wg ran with nothing new")
	}
	if out := a.stdout.String(); !strings.Contains(out, "check connected ok Connected to the cluster") || !strings.Contains(out, "state joining") {
		t.Errorf("stdout = %q", out)
	}
}

func TestSyncAppliesNewPeersWithoutRestartingNomad(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	hcl := a.read(t, "etc/nomad.d/nomad.hcl")
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["peers"] = []any{peerJSON(1, peerKey1, "203.0.113.4:51820"), peerJSON(2, peerKey2, "198.51.100.7:51820")}
	}))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	wgs := a.runner.wgCalls()
	if len(wgs) != 1 || wgs[0].args[0] != "syncconf" || !strings.Contains(wgs[0].conf, "PublicKey = "+peerKey2) || wgs[0].confMode != 0o600 {
		t.Fatalf("wg calls = %+v", wgs)
	}
	for _, cmd := range a.runner.commands() {
		if strings.HasPrefix(cmd, "systemctl") {
			t.Errorf("ran %s", cmd)
		}
	}
	st, _ := a.LoadState()
	if len(st.Peers) != 2 {
		t.Errorf("state has %d peers", len(st.Peers))
	}
	if a.read(t, "etc/nomad.d/nomad.hcl") != hcl {
		t.Error("nomad.hcl changed for a peers-only change")
	}
	// The same answer again changes nothing.
	a.runner.reset()
	before := a.snapshot(t)
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(a.runner.wgCalls()) != 0 || !sameSnapshot(before, a.snapshot(t)) {
		t.Error("a second identical sync applied something")
	}
}

func TestSyncRerendersNomadWhenTheServersChange(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["peers"] = []any{peerJSON(1, peerKey1, "203.0.113.4:51820"), peerJSON(2, peerKey2, "198.51.100.7:51820")}
		m["servers"] = []int{1, 2}
	}))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.read(t, "etc/nomad.d/nomad.hcl"), `servers               = ["10.98.0.1:4647", "10.98.0.2:4647"]`) {
		t.Errorf("nomad.hcl wasn't re-rendered:\n%s", a.read(t, "etc/nomad.d/nomad.hcl"))
	}
	for _, cmd := range a.runner.commands() {
		if strings.HasPrefix(cmd, "systemctl") {
			t.Errorf("ran %s", cmd)
		}
	}
}

func TestSyncPromotesAClientToAServer(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	oldKey, oldCert := a.read(t, "etc/nomad.d/tls/agent-key.pem"), a.read(t, "etc/nomad.d/tls/agent.pem")
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["role"] = "server"
		m["gossipKey"] = sentinelGossip
		m["servers"] = []int{1, 2, 3}
		m["peers"] = []any{peerJSON(1, peerKey1, "203.0.113.4:51820"), peerJSON(2, peerKey2, "198.51.100.7:51820")}
	}))
	c.on("/v1/node/certificate", func(req map[string]any, auth string) (int, any) {
		return 200, map[string]any{"certificate": c.sign(req["csr"], clusterca.Server, 3), "ca": string(c.caCert)}
	})
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	certCalls := c.calls("/v1/node/certificate")
	if len(certCalls) != 1 || certCalls[0].auth != "Bearer "+sentinelNodeToken {
		t.Fatalf("certificate calls = %+v", certCalls)
	}
	if a.read(t, "etc/caravana-node/gossip.key") != sentinelGossip+"\n" || a.mode(t, "etc/caravana-node/gossip.key") != 0o600 {
		t.Error("gossip.key wasn't written with 0600")
	}
	newKey, newCert := a.read(t, "etc/nomad.d/tls/agent-key.pem"), a.read(t, "etc/nomad.d/tls/agent.pem")
	if newKey == oldKey || newCert == oldCert {
		t.Error("the certificate or key wasn't replaced")
	}
	if err := clusterca.Check(c.caCert, []byte(newCert), []byte(newKey), clusterca.Server, t0); err != nil {
		t.Errorf("the new certificate: %v", err)
	}
	st, _ := a.LoadState()
	if st.Role != RoleServer || !reflect.DeepEqual(st.Servers, []int{1, 2, 3}) {
		t.Errorf("state = %+v", st)
	}
	hcl := a.read(t, "etc/nomad.d/nomad.hcl")
	if !strings.Contains(hcl, "\nserver {") || !strings.Contains(hcl, sentinelGossip) || !strings.Contains(hcl, `retry_join = ["10.98.0.1:4648", "10.98.0.2:4648"]`) {
		t.Errorf("nomad.hcl isn't a server's:\n%s", hcl)
	}
	if a.mode(t, "etc/nomad.d/nomad.hcl") != 0o600 {
		t.Error("nomad.hcl isn't 0600")
	}
	restarts := 0
	for _, cmd := range a.runner.commands() {
		if cmd == "systemctl restart nomad" {
			restarts++
		}
	}
	if restarts != 1 {
		t.Errorf("Nomad was restarted %d times: %v", restarts, a.runner.commands())
	}
	assertNoSecrets(t, a, nil)
}

func TestSyncRenewal(t *testing.T) {
	for name, tt := range map[string]struct {
		answerRenew bool
		flag        bool
	}{"the console says renew": {true, false}, "--renew": {false, true}} {
		t.Run(name, func(t *testing.T) {
			c := newConsole(t)
			a := newTestAgent(t, c)
			a.joined(t)
			oldKey, oldCert := a.read(t, "etc/nomad.d/tls/agent-key.pem"), a.read(t, "etc/nomad.d/tls/agent.pem")
			c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) { m["renew"] = tt.answerRenew }))
			c.on("/v1/node/certificate", func(req map[string]any, _ string) (int, any) {
				return 200, map[string]any{"certificate": c.sign(req["csr"], clusterca.Client, 3)}
			})
			if err := a.Sync(ctx(), SyncOptions{Renew: tt.flag}); err != nil {
				t.Fatal(err)
			}
			newKey, newCert := a.read(t, "etc/nomad.d/tls/agent-key.pem"), a.read(t, "etc/nomad.d/tls/agent.pem")
			if newKey == oldKey || newCert == oldCert {
				t.Error("the key or certificate wasn't replaced")
			}
			if err := clusterca.Check(c.caCert, []byte(newCert), []byte(newKey), clusterca.Client, t0); err != nil {
				t.Errorf("the renewed certificate: %v", err)
			}
			if a.mode(t, "etc/nomad.d/tls/agent-key.pem") != 0o600 {
				t.Error("agent-key.pem isn't 0600")
			}
			var systemctl []string
			for _, cmd := range a.runner.commands() {
				if strings.HasPrefix(cmd, "systemctl") {
					systemctl = append(systemctl, cmd)
				}
			}
			if !reflect.DeepEqual(systemctl, []string{"systemctl reload nomad"}) {
				t.Errorf("systemctl calls = %v", systemctl)
			}
		})
	}
}

// With 29 days left the agent renews by itself, whatever the console says.
func TestSyncRenewsByTheCertificatesOwnDate(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	oldCert := a.read(t, "etc/nomad.d/tls/agent.pem")
	end, err := clusterca.NotAfter([]byte(oldCert))
	if err != nil {
		t.Fatal(err)
	}
	later := end.Add(-29 * 24 * time.Hour)
	a.Now = func() time.Time { return later }
	c.on("/v1/node/sync", a.syncAnswer(nil))
	c.on("/v1/node/certificate", func(req map[string]any, _ string) (int, any) {
		return 200, map[string]any{"certificate": c.signAt(req["csr"], clusterca.Client, 3, later)}
	})
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if a.read(t, "etc/nomad.d/tls/agent.pem") == oldCert {
		t.Error("the certificate wasn't renewed with 29 days left")
	}
}

func TestSyncWithoutRenewalAsksForNoCertificate(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	c.on("/v1/node/sync", a.syncAnswer(nil))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(c.calls("/v1/node/certificate")) != 0 {
		t.Error("asked for a certificate nobody wanted")
	}
}

// Every rule of §5 on a sync answer: nothing changes.
func TestSyncBadAnswerChangesNothing(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"index":              func(m map[string]any) { m["index"] = 4 },
		"name":               func(m map[string]any) { m["name"] = "other" },
		"pool":               func(m map[string]any) { m["pool"] = "BAD" },
		"role":               func(m map[string]any) { m["role"] = "boss" },
		"peer index":         func(m map[string]any) { m["peers"] = []any{peerJSON(0, peerKey1, "203.0.113.4:51820")} },
		"peer is itself":     func(m map[string]any) { m["peers"] = []any{peerJSON(3, peerKey1, "203.0.113.4:51820")} },
		"peer key":           func(m map[string]any) { m["peers"] = []any{peerJSON(1, "nope", "203.0.113.4:51820")} },
		"peer endpoint":      func(m map[string]any) { m["peers"] = []any{peerJSON(1, peerKey1, "evil.example.com:51820")} },
		"servers empty":      func(m map[string]any) { m["servers"] = []int{} },
		"servers unknown":    func(m map[string]any) { m["servers"] = []int{1, 9} },
		"gossip key missing": func(m map[string]any) { m["role"] = "server"; m["servers"] = []int{1, 3} },
		"foreign CA": func(m map[string]any) {
			other, _, _ := clusterca.New(t0)
			m["ca"] = string(other)
		},
		"two peers one new, one bad": func(m map[string]any) {
			m["peers"] = []any{peerJSON(1, peerKey1, "203.0.113.4:51820"), peerJSON(2, peerKey2, "127.0.0.1:51820")}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := newConsole(t)
			a := newTestAgent(t, c)
			a.joined(t)
			c.on("/v1/node/sync", a.syncAnswer(mutate))
			before := a.snapshot(t)
			err := a.Sync(ctx(), SyncOptions{})
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("err = %v, want a FieldError", err)
			}
			if !sameSnapshot(before, a.snapshot(t)) {
				t.Error("a file changed")
			}
			if len(a.runner.commands()) != 0 {
				t.Errorf("ran %v", a.runner.commands())
			}
		})
	}
	// A server going back to client.
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	st, _ := a.LoadState()
	st.Role = RoleServer
	st.Servers = []int{1, 3}
	_ = a.SaveState(st)
	_ = a.writeGossip()
	c.on("/v1/node/sync", a.syncAnswer(nil))
	if err := a.Sync(ctx(), SyncOptions{}); err == nil {
		t.Error("a server went back to client")
	}
}

func (a *testAgent) writeGossip() error {
	return writeFile(a.file("etc/caravana-node/gossip.key"), []byte(sentinelGossip+"\n"), 0o600)
}

// A 401, a 503, a 404 (an older Caravana) and a console that isn't there: exit 0, a line, no change.
func TestSyncSoftFailures(t *testing.T) {
	for name, status := range map[string]int{"401": 401, "503": 503, "404": 404, "500": 500} {
		t.Run(name, func(t *testing.T) {
			c := newConsole(t)
			a := newTestAgent(t, c)
			a.joined(t)
			c.on("/v1/node/sync", func(map[string]any, string) (int, any) {
				return status, map[string]any{"error": map[string]any{"code": "x", "message": "y"}}
			})
			before := a.snapshot(t)
			if err := a.Sync(ctx(), SyncOptions{}); err != nil {
				t.Fatalf("err = %v", err)
			}
			if !sameSnapshot(before, a.snapshot(t)) || len(a.runner.commands()) != 0 {
				t.Error("something changed")
			}
			if a.stderr.Len() == 0 {
				t.Error("no line on stderr")
			}
			if status == 401 && !strings.Contains(a.stderr.String(), "removed from the cluster") {
				t.Errorf("stderr = %q", a.stderr.String())
			}
			assertNoSecrets(t, a, nil)
		})
	}
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	c.srv.Close()
	if err := a.Sync(ctx(), SyncOptions{}); err != nil || a.stderr.Len() == 0 {
		t.Errorf("unreachable console: err %v, stderr %q", err, a.stderr.String())
	}
}

func TestSyncCertificateFailures(t *testing.T) {
	// A console that answers 503 to the certificate request: soft, the role stays client.
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["role"] = "server"
		m["gossipKey"] = sentinelGossip
		m["servers"] = []int{1, 3}
	}))
	c.on("/v1/node/certificate", func(map[string]any, string) (int, any) { return 503, nil })
	oldCert := a.read(t, "etc/nomad.d/tls/agent.pem")
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatalf("a 503 on the certificate is soft: %v", err)
	}
	if st, _ := a.LoadState(); st.Role != RoleClient || a.exists("etc/caravana-node/gossip.key") || a.read(t, "etc/nomad.d/tls/agent.pem") != oldCert {
		t.Error("a failed promotion changed the server")
	}
	if len(c.calls("/v1/node/certificate")) != 1 {
		t.Error("no certificate call")
	}

	// A certificate signed by another CA: refused, error, nothing written.
	other, otherKey, _ := clusterca.New(t0)
	c.on("/v1/node/certificate", func(req map[string]any, _ string) (int, any) {
		cert, err := clusterca.Sign(other, otherKey, []byte(req["csr"].(string)), clusterca.Server, mustAddr("10.98.0.3"), t0)
		if err != nil {
			t.Fatal(err)
		}
		return 200, map[string]any{"certificate": string(cert)}
	})
	before := a.snapshot(t)
	err := a.Sync(ctx(), SyncOptions{})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "certificate" {
		t.Fatalf("err = %v", err)
	}
	if !sameSnapshot(before, a.snapshot(t)) {
		t.Error("a bad certificate changed a file")
	}
	for _, cmd := range a.runner.commands() {
		if strings.HasPrefix(cmd, "systemctl") {
			t.Errorf("ran %s", cmd)
		}
	}
}

func TestSyncRestartFailureIsAnError(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	a.runner.failOn["systemctl restart"] = errors.New("unit not found")
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["role"] = "server"
		m["gossipKey"] = sentinelGossip
		m["servers"] = []int{1, 3}
	}))
	c.on("/v1/node/certificate", func(req map[string]any, _ string) (int, any) {
		return 200, map[string]any{"certificate": c.sign(req["csr"], clusterca.Server, 3)}
	})
	if err := a.Sync(ctx(), SyncOptions{}); err == nil {
		t.Error("a failed restart was swallowed")
	}
}

// ---- leave and show ----------------------------------------------------------------------------

func TestLeave(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	a.runner.links["caravana-mesh"] = true
	a.runner.links["caravana0"] = true
	a.runner.nomadActive = true
	if err := a.Leave(ctx()); err != nil {
		t.Fatal(err)
	}
	// Nomad is asked to leave the cluster (SIGTERM) and seen to exit before anything is taken away.
	want := []string{
		"systemctl is-active --quiet nomad", "systemctl kill --signal=SIGTERM nomad", "systemctl is-active --quiet nomad",
		"systemctl disable --now nomad", "ip link delete caravana-mesh", "ip link delete caravana0",
	}
	if got := a.runner.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v", got)
	}
	if a.exists("etc/caravana-node") || a.exists("etc/nomad.d/tls") {
		t.Error("the agent's folder or the certificates remain")
	}
	// Leaving again finds nothing and still succeeds.
	if err := a.Leave(ctx()); err != nil {
		t.Errorf("second leave: %v", err)
	}
}

// A Nomad that doesn't exit after SIGTERM is waited for, then stopped: the server leaves either way.
func TestLeaveStopsANomadThatWontLeave(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	a.runner.nomadActive = true
	a.runner.nomadIgnoresTerm = true
	var slept time.Duration
	a.Sleep = func(d time.Duration) { slept += d }
	if err := a.Leave(ctx()); err != nil {
		t.Fatal(err)
	}
	if slept != leaveWait {
		t.Errorf("waited %s for Nomad to leave, want %s", slept, leaveWait)
	}
	if a.runner.nomadActive {
		t.Error("Nomad still runs after leave")
	}
	if !strings.Contains(a.stderr.String(), "didn't exit") {
		t.Errorf("stderr should say Nomad didn't exit: %q", a.stderr.String())
	}
}

func TestLeaveRefusesOnTheFirstServer(t *testing.T) {
	a := newTestAgent(t, nil)
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4"}); err != nil {
		t.Fatal(err)
	}
	a.runner.reset()
	before := a.snapshot(t)
	if err := a.Leave(ctx()); err == nil {
		t.Fatal("the first server left")
	}
	if len(a.runner.calls) != 0 || !sameSnapshot(before, a.snapshot(t)) {
		t.Error("a refused leave changed something")
	}
}

func TestShow(t *testing.T) {
	a := newTestAgent(t, newConsole(t))
	a.joined(t)
	want := map[string]string{"name": "fsn-2", "index": "3", "role": "client", "first": "false"}
	for field, v := range want {
		if got, err := a.Show(field); err != nil || got != v {
			t.Errorf("show %s = %q, %v; want %q", field, got, err, v)
		}
	}
	pub, err := a.Show("public-key")
	if err != nil || !mesh.ValidPublicKey(pub) {
		t.Errorf("public-key = %q, %v", pub, err)
	}
	if _, err := a.Show("nope"); err == nil {
		t.Error("an unknown field was accepted")
	}
	for _, f := range ShowFields {
		if _, err := a.Show(f); err != nil {
			t.Errorf("ShowFields has %s, which fails: %v", f, err)
		}
	}
}

// ---- secrets -----------------------------------------------------------------------------------

// assertNoSecrets: the join token, node token, the private keys on disk and the gossip key appear in
// no line the agent printed and no argument it ran, and not in err.
func assertNoSecrets(t *testing.T, a *testAgent, err error) {
	t.Helper()
	secrets := map[string]string{"join token": sentinelJoinToken, "node token": sentinelNodeToken, "gossip key": sentinelGossip}
	for _, f := range []string{"etc/caravana-node/mesh.key", "etc/nomad.d/tls/agent-key.pem"} {
		if b, e := readIfExistsFile(a.file(f)); e == nil && len(strings.TrimSpace(b)) > 20 {
			secrets[f] = strings.TrimSpace(b)
		}
	}
	haystacks := map[string]string{"stdout": a.stdout.String(), "stderr": a.stderr.String(), "arguments": a.runner.allArgs()}
	if err != nil {
		haystacks["error"] = err.Error()
	}
	for hn, h := range haystacks {
		for sn, s := range secrets {
			if strings.Contains(h, s) {
				t.Errorf("the %s is in the %s", sn, hn)
			}
		}
	}
}

// The whole life of a server, with every output checked for the sentinels.
func TestNoSecretInAnyOutput(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	c.serveJoin()

	// A refused join first.
	err := a.Join(ctx(), JoinOptions{URL: c.srv.URL, Token: "cvjn_" + strings.Repeat("y", 43)})
	assertNoSecrets(t, a, err)

	err = a.Join(ctx(), JoinOptions{URL: c.srv.URL, Token: sentinelJoinToken})
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, a, nil)

	// A bad answer, a refusal, a promotion and a renewal.
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["gossipKey"] = sentinelGossip
		m["role"] = "server"
		m["servers"] = []int{1, 9}
	}))
	err = a.Sync(ctx(), SyncOptions{})
	assertNoSecrets(t, a, err)
	if err == nil || strings.Contains(err.Error(), sentinelGossip) {
		t.Errorf("err = %v", err)
	}
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["role"] = "server"
		m["gossipKey"] = sentinelGossip
		m["servers"] = []int{1, 3}
		m["renew"] = true
	}))
	c.on("/v1/node/certificate", func(req map[string]any, _ string) (int, any) {
		return 200, map[string]any{"certificate": c.sign(req["csr"], clusterca.Server, 3)}
	})
	err = a.Sync(ctx(), SyncOptions{PrintChecks: true})
	assertNoSecrets(t, a, err)
	if err != nil {
		t.Fatal(err)
	}
	_ = a.Leave(ctx())
	assertNoSecrets(t, a, nil)
}
