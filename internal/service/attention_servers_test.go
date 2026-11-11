package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestServerItemsOf(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ready := func(idx int, name string, cert time.Duration) store.Server {
		r := store.Server{Index: idx, Name: name, State: store.ServerReady, SeenAt: now.Add(-time.Minute)}
		if cert != 0 {
			r.CertNotAfter = now.Add(cert)
		}
		return r
	}
	up := func(idx int) deploy.Node {
		return deploy.Node{ID: "n", Status: "ready", Eligible: true, AppAddress: "10.99.0." + string(rune('0'+idx))}
	}
	cases := []struct {
		name   string
		rows   []store.Server
		nodes  []deploy.Node
		nomad  bool
		want   []string // kind|server
		detail string
	}{
		{"all up", []store.Server{ready(1, "hel-1", 0), ready(2, "fsn-2", 60*24*time.Hour)}, []deploy.Node{up(1), up(2)}, true, nil, ""},
		{"a down node", []store.Server{ready(1, "hel-1", 0), ready(2, "fsn-2", 0)},
			[]deploy.Node{up(1), {ID: "n2", Status: "down", AppAddress: "10.99.0.2"}}, true, []string{"server_down|fsn-2"},
			"fsn-2 is down. Its copies are being replaced on other servers; what has a volume there waits for it."},
		{"a disconnected node", []store.Server{ready(2, "fsn-2", 0)},
			[]deploy.Node{{ID: "n2", Status: "disconnected", AppAddress: "10.99.0.2"}}, true, []string{"server_down|fsn-2"}, ""},
		{"a node Nomad lost", []store.Server{ready(1, "hel-1", 0), ready(2, "fsn-2", 0)}, []deploy.Node{up(1)}, true, []string{"server_down|fsn-2"}, ""},
		{"Nomad can't be read: nobody is down", []store.Server{ready(1, "hel-1", 0), ready(2, "fsn-2", 0)}, nil, false, nil, ""},
		{"a joining server with no node is not down", []store.Server{{Index: 2, Name: "fsn-2", State: store.ServerJoining}}, nil, true, nil, ""},
		{"a certificate ending in 13 days", []store.Server{ready(2, "fsn-2", 13*24*time.Hour)}, []deploy.Node{up(2)}, true,
			[]string{"server_certificate_expiring|fsn-2"}, "fsn-2's Nomad certificate ends Nov 23. Its agent couldn't renew it; run the installer on it again."},
		{"a certificate ending in 15 days is fine", []store.Server{ready(2, "fsn-2", 15*24*time.Hour)}, []deploy.Node{up(2)}, true, nil, ""},
		{"the certificate item doesn't need Nomad", []store.Server{ready(2, "fsn-2", 2*24*time.Hour)}, nil, false, []string{"server_certificate_expiring|fsn-2"}, ""},
		{"a waiting join has neither", []store.Server{{Index: 3, State: store.ServerWaiting, CertNotAfter: now}}, nil, true, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			var detail string
			for _, it := range serverItemsOf(now, c.rows, c.nodes, c.nomad) {
				got = append(got, string(it.Kind)+"|"+it.Server)
				detail = it.Detail
				if key := problemKey(it); key != string(it.Kind)+"|||"+it.Server {
					t.Errorf("key = %q, want %s|||%s", key, it.Kind, it.Server)
				}
				if got := problemPath(it.Kind, it.AppID, problemSubject(it)); got != "/ops/servers/"+it.Server {
					t.Errorf("path = %q", got)
				}
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("items = %v, want %v", got, c.want)
			}
			if c.detail != "" && detail != c.detail {
				t.Errorf("detail = %q, want %q", detail, c.detail)
			}
		})
	}
}

func TestAttentionHasTheServerKinds(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	_, two := e.join(t, "two", "")
	e.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	if _, err := e.svc.NodeSync(ctx, two.NodeToken, healthyReport(1)); err != nil {
		t.Fatal(err)
	}
	att, err := e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range att.Items {
		if it.Server != "" {
			t.Errorf("an item for a healthy cluster: %+v", it)
		}
	}
	if att.ServersUnknown {
		t.Error("servers unknown with Nomad up")
	}

	// Down: an item, its event and its link.
	e.nomad.SetNodeStatusOf("node-2", "down")
	att, _ = e.svc.Attention(ctx)
	var down *AttentionItem
	for i := range att.Items {
		if att.Items[i].Kind == AttentionServerDown {
			down = &att.Items[i]
		}
	}
	if down == nil || down.Server != "two" || down.AppID != "" {
		t.Fatalf("items = %+v, want server_down for two", att.Items)
	}
	ev := problemEvent(*down, "https://c.test", e.svc.clock.Now())
	if ev.Server != "two" || ev.Title != "Server down: two" || ev.URL != "https://c.test/ops/servers/two" || ev.Kind != "server_down" || ev.AppID != "" {
		t.Errorf("event = %+v", ev)
	}
	if ev.Text != down.Detail {
		t.Errorf("event text = %q, want the item's sentence", ev.Text)
	}

	// Nomad unreadable: not judged, so no "resolved" would be sent for it.
	e.nomad.Down()
	att, _ = e.svc.Attention(ctx)
	if !att.ServersUnknown {
		t.Error("ServersUnknown is false with Nomad down")
	}
	if !notJudged(AttentionServerDown, judged{Copies: true, Disks: true, OS: true}) || notJudged(AttentionServerCertExpiring, judged{}) {
		t.Error("notJudged: server_down is judged only with Servers, the certificate kind always")
	}
	e.nomad.Up()

	// A certificate close to its end.
	soon := e.svc.clock.Now().UTC().Add(5 * 24 * time.Hour)
	r := healthyReport(1)
	r.CertificateNotAfter = soon.Format(time.RFC3339)
	if _, err := e.svc.NodeSync(ctx, two.NodeToken, r); err != nil {
		t.Fatal(err)
	}
	e.nomad.SetNodeStatusOf("node-2", "ready")
	att, _ = e.svc.Attention(ctx)
	found := false
	for _, it := range att.Items {
		if it.Kind == AttentionServerCertExpiring && it.Server == "two" && it.ExpiresAt != nil && it.ExpiresAt.Equal(soon.Truncate(time.Second)) {
			found = true
		}
	}
	if !found {
		t.Errorf("no server_certificate_expiring for two in %+v", att.Items)
	}
}
