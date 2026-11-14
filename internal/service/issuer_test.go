package service

import (
	"context"
	"testing"
	"time"
)

func TestClusterIssuer(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://console.example.com", "https://console.example.com"},
		{"https://console.example.com/", "https://console.example.com"}, // SetPublicURL trims it anyway
		{"https://console.example.com:8443", "https://console.example.com:8443"},
		{"", ""},
		{"http://console.example.com", ""},
		{"http://127.0.0.1:7070", ""},
		{"https://console.example.com/app", ""},
		{"https://console.example.com?x=1", ""},
		{"https://console.example.com#frag", ""},
		{"https://user@console.example.com", ""},
		{"https://user:pw@console.example.com", ""},
		{"https://", ""},
		{"https://console.example.com:0", ""},
		{"https://console.example.com:99999", ""},
		{"https://console.example.com:", ""},
		{"https://203.0.113.4", ""},
		{"https://[::1]", ""},
		{"console.example.com", ""},
		{"https:console.example.com", ""},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			e := newEnv(t)
			e.svc.SetPublicURL(c.url)
			if got := e.svc.ClusterIssuer(); got != c.want {
				t.Errorf("ClusterIssuer() for %q = %q, want %q", c.url, got, c.want)
			}
		})
	}
}

func TestIssuerFor(t *testing.T) {
	const cur = "https://console.example.com"
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		server     bool
		reported   string
		current    string
		inFlight   map[string]time.Time
		wantSend   string
		wantStart  bool
		serverName string
	}{
		{name: "a client stores the current one", server: false, reported: "", current: cur, wantSend: cur},
		{name: "a client is never held back by a server in flight", server: false, current: cur,
			inFlight: map[string]time.Time{"a": now}, wantSend: cur},
		{name: "a server already on it", server: true, reported: cur, current: cur, wantSend: cur},
		{name: "a server already on it is not held back", server: true, reported: cur, current: cur,
			inFlight: map[string]time.Time{"a": now}, wantSend: cur},
		{name: "a server that is in flight keeps getting it", server: true, reported: "", current: cur,
			inFlight: map[string]time.Time{"s": now.Add(-5 * time.Minute)}, wantSend: cur},
		{name: "another server restarting under 2 minutes holds this one", server: true, reported: "", current: cur,
			inFlight: map[string]time.Time{"other": now.Add(-time.Minute)}, wantSend: ""},
		{name: "the held server keeps what it reports", server: true, reported: "https://old.example.com", current: cur,
			inFlight: map[string]time.Time{"other": now.Add(-time.Minute)}, wantSend: "https://old.example.com"},
		{name: "another server in flight for over 2 minutes doesn't hold", server: true, reported: "", current: cur,
			inFlight: map[string]time.Time{"other": now.Add(-3 * time.Minute)}, wantSend: cur, wantStart: true},
		{name: "nobody in flight: this one starts", server: true, reported: "", current: cur, wantSend: cur, wantStart: true},
		{name: "the issuer is removed: a server starts on none", server: true, reported: cur, current: "", wantSend: "", wantStart: true},
		{name: "nothing to change", server: true, reported: "", current: "", wantSend: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			send, start := issuerFor("s", c.server, c.reported, c.current, c.inFlight, now)
			if send != c.wantSend || start != c.wantStart {
				t.Errorf("issuerFor = (%q, %v), want (%q, %v)", send, start, c.wantSend, c.wantStart)
			}
		})
	}
}

// Two Nomad servers learn a new issuer one after the other, and a server that is only a client just
// stores it.
func TestNodeSyncHandsTheIssuerOutOneAtATime(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.svc.SetPublicURL("https://console.example.com")
	const cur = "https://console.example.com"
	sync := func(token string, r NodeReport) NodeAnswer {
		t.Helper()
		out, err := e.svc.NodeSync(ctx, token, r)
		if err != nil {
			t.Fatalf("NodeSync: %v", err)
		}
		return out
	}
	first := healthyReport()
	first.Role, first.ServerRunning = "server", true

	_, two := e.join(t, "two", "")
	_, three := e.join(t, "three", "")
	e.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	e.nomad.AddNode("node-3", "three", "ready", "10.99.0.3")
	sync(e.firstToken, first)
	sync(two.NodeToken, healthyReport(1, 3))
	sync(three.NodeToken, healthyReport(1, 2))
	// Two is promoted (the two oldest others become Nomad servers once there are three).
	twoServer := healthyReport(1, 3)
	twoServer.Role, twoServer.ServerRunning = "server", true
	if out := sync(two.NodeToken, twoServer); out.Role != "server" {
		t.Fatalf("two should be a Nomad server: %+v", out)
	}
	sync(two.NodeToken, twoServer)

	// The first Nomad server to sync takes the issuer; the other waits.
	if out := sync(e.firstToken, first); out.Issuer != cur {
		t.Fatalf("the first server's answer issuer = %q, want %q", out.Issuer, cur)
	}
	if out := sync(two.NodeToken, twoServer); out.Issuer != "" {
		t.Errorf("the second server was handed the issuer while the first restarts: %q", out.Issuer)
	}
	// A client stores it at once.
	if out := sync(three.NodeToken, healthyReport(1, 2)); out.Role == "client" && out.Issuer != cur {
		t.Errorf("the client's answer issuer = %q", out.Issuer)
	}
	// The first reports it rendered the issuer: the second may take it.
	first.Issuer = cur
	if out := sync(e.firstToken, first); out.Issuer != cur {
		t.Errorf("a server on the issuer is told %q", out.Issuer)
	}
	if out := sync(two.NodeToken, twoServer); out.Issuer != cur {
		t.Errorf("the second server's answer issuer = %q, want %q", out.Issuer, cur)
	}
}

// An issuer in a report that this console wouldn't hand out is dropped, not echoed back.
func TestNodeSyncIgnoresAnOddReportedIssuer(t *testing.T) {
	e := newClusterEnv(t)
	r := healthyReport()
	r.Role, r.ServerRunning, r.Issuer = "server", true, "http://evil.example/x"
	out, err := e.svc.NodeSync(context.Background(), e.firstToken, r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Issuer != "" {
		t.Errorf("answer issuer = %q, want none (no public URL)", out.Issuer)
	}
}
