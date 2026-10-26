package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 023 T017: the domain kinds of the needs-you list, and their
// notifications.

// domainRec is a stored app "shop" whose service web has the domains.
func domainRec(id string, domains ...app.Domain) store.AppRecord {
	return store.AppRecord{App: domainApp(id, domains...), UpdatedAt: at(0)}
}

// checkRow is a cached check of host for shop/web in state.
func checkRow(host, state string, m int) store.DomainCheckRecord {
	return store.DomainCheckRecord{Host: host, AppID: "shop", Service: "web", State: state,
		DNSStatus: edge.DNSOK, DNSSeen: []string{"203.0.113.1"}, DNSExpected: []string{"203.0.113.1"},
		CertStatus: edge.CertIssued, CheckedAt: at(m)}
}

func TestDomainItems(t *testing.T) {
	elsewhere := checkRow("shop.example.com", string(edge.StateDNSElsewhere), 30)
	elsewhere.DNSStatus, elsewhere.DNSSeen = edge.DNSElsewhere, []string{"203.0.113.1", "198.51.100.9"}
	expiring := checkRow("bi.example.com", string(edge.StateCertExpiring), 31)
	expiring.CertStatus, expiring.CertNotAfter = edge.CertExpiring, tp(attNow.Add(6*24*time.Hour+time.Hour))
	pair := checkRow("www.example.com", string(edge.StateDNSElsewhere), 32)
	pair.DNSStatus, pair.DNSSeen = edge.DNSElsewhere, []string{"198.51.100.7"}

	shop := domainRec("shop", app.Domain{Host: "shop.example.com"}, app.Domain{Host: "bi.example.com"},
		app.Domain{Host: "example.com", WWWRedirect: true})
	stopped := shop
	stopped.StoppedAt = tp(at(1))

	tests := []struct {
		name   string
		apps   []store.AppRecord
		checks []store.DomainCheckRecord
		want   []AttentionItem
	}{
		{"elsewhere, expiring and a redirected pair", []store.AppRecord{shop},
			[]store.DomainCheckRecord{elsewhere, expiring, pair, checkRow("ok.example.com", string(edge.StateLive), 5)},
			[]AttentionItem{
				{Kind: AttentionDomainDNSElsewhere, AppID: "shop", Service: "web", Domain: "www.example.com", AddressSeen: "198.51.100.7",
					When: at(32), Detail: "www.example.com points at 198.51.100.7, which isn't this host."},
				{Kind: AttentionDomainCertExpiring, AppID: "shop", Service: "web", Domain: "bi.example.com", ExpiresAt: expiring.CertNotAfter,
					When: at(31), Detail: "The certificate for bi.example.com expires in 6 days and hasn't been renewed."},
				{Kind: AttentionDomainDNSElsewhere, AppID: "shop", Service: "web", Domain: "shop.example.com", AddressSeen: "198.51.100.9",
					When: at(30), Detail: "shop.example.com points at 198.51.100.9, which isn't this host."},
			}},
		{"the other states are no item", []store.AppRecord{shop},
			[]store.DomainCheckRecord{
				checkRow("shop.example.com", string(edge.StateCertPending), 1), checkRow("shop.example.com", string(edge.StateDNSNone), 1),
				checkRow("shop.example.com", string(edge.StateUnknown), 1), checkRow("shop.example.com", string(edge.StateNotServed), 1),
			}, nil},
		{"a stopped app has none", []store.AppRecord{stopped}, []store.DomainCheckRecord{elsewhere}, nil},
		{"an app that is gone has none", nil, []store.DomainCheckRecord{elsewhere}, nil},
		{"a domain the app no longer has is none", []store.AppRecord{domainRec("shop", app.Domain{Host: "other.example.com"})},
			[]store.DomainCheckRecord{elsewhere}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := attentionOf(attNow, tc.apps, nil, nil, nil, nil, nil, true, scheduleFacts{}, tc.checks).Items
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("items:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestExpiringSentence(t *testing.T) {
	now := attNow
	for _, tc := range []struct {
		left time.Duration
		want string
	}{
		{14 * 24 * time.Hour, "The certificate for x.example.com expires in 14 days and hasn't been renewed."},
		{36 * time.Hour, "The certificate for x.example.com expires in 1 day and hasn't been renewed."},
		{3 * time.Hour, "The certificate for x.example.com expires today and hasn't been renewed."},
		{-time.Hour, "The certificate for x.example.com has expired and hasn't been renewed."},
	} {
		if got := expiringSentence(now, "x.example.com", tp(now.Add(tc.left))); got != tc.want {
			t.Errorf("%v left: %q, want %q", tc.left, got, tc.want)
		}
	}
}

func TestDomainProblemKeyAndEvent(t *testing.T) {
	elsewhere := AttentionItem{Kind: AttentionDomainDNSElsewhere, AppID: "shop", Service: "web", Domain: "shop.example.com",
		AddressSeen: "198.51.100.9", When: at(5), Detail: "shop.example.com points at 198.51.100.9, which isn't this host."}
	expiring := AttentionItem{Kind: AttentionDomainCertExpiring, AppID: "shop", Service: "web", Domain: "bi.example.com",
		ExpiresAt: tp(at(6 * 24 * 60)), When: at(6), Detail: "The certificate for bi.example.com expires in 6 days and hasn't been renewed."}

	if got := problemKey(elsewhere); got != "domain_dns_elsewhere|shop|web|shop.example.com" {
		t.Errorf("problemKey = %q", got)
	}
	if notJudged(AttentionDomainDNSElsewhere, judged{}) || notJudged(AttentionDomainCertExpiring, judged{}) {
		t.Error("a domain kind is judged only when Nomad or the host is readable")
	}
	const base = "https://caravana.example.com"
	for _, tc := range []struct {
		it    AttentionItem
		title string
		url   string
	}{
		{elsewhere, "shop: shop.example.com points elsewhere", base + "/domains?open=shop.example.com#domain-shop.example.com"},
		{expiring, "shop: the certificate for bi.example.com is expiring", base + "/domains?open=bi.example.com#domain-bi.example.com"},
	} {
		ev := problemEvent(tc.it, base, attNow)
		if ev.Type != notify.EventProblemOpened || ev.Kind != string(tc.it.Kind) || ev.Title != tc.title || ev.Text != tc.it.Detail ||
			ev.Detail != "" || ev.URL != tc.url || ev.Domain != tc.it.Domain || ev.AddressSeen != tc.it.AddressSeen ||
			!reflect.DeepEqual(ev.ExpiresAt, tc.it.ExpiresAt) || !ev.At.Equal(tc.it.When) {
			t.Errorf("problemEvent(%s) = %+v", tc.it.Kind, ev)
		}
		resolved := resolvedEvent(sentRow(tc.it, attNow), base, attNow)
		if resolved.URL != tc.url || resolved.Kind != string(tc.it.Kind) {
			t.Errorf("resolvedEvent = %+v", resolved)
		}
	}
}

func TestDiffProblemsDomains(t *testing.T) {
	now := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	const delay = 5 * time.Minute
	exists := map[string]bool{"shop": true}
	shop := AttentionItem{Kind: AttentionDomainDNSElsewhere, AppID: "shop", Service: "web", Domain: "shop.example.com", AddressSeen: "198.51.100.9"}
	admin := AttentionItem{Kind: AttentionDomainDNSElsewhere, AppID: "shop", Service: "web", Domain: "admin.example.com", AddressSeen: "198.51.100.9"}

	// Two domains of one service pointing elsewhere: one problem each.
	ch := diffProblems(nil, []AttentionItem{shop, admin}, allJudged, exists, now, delay)
	if len(ch.Opened) != 2 {
		t.Fatalf("opened %+v, want one per domain", ch.Opened)
	}
	// Still elsewhere, another address seen: the same problem, seen.
	moved := shop
	moved.AddressSeen = "198.51.100.10"
	prev := []store.SentProblem{sentRow(shop, now.Add(-time.Minute)), sentRow(admin, now.Add(-time.Minute))}
	ch = diffProblems(prev, []AttentionItem{moved, admin}, allJudged, exists, now, delay)
	if len(ch.Opened) != 0 || len(ch.Seen) != 2 {
		t.Errorf("a domain still pointing elsewhere: %+v", ch)
	}
	// The state cleared (or the domain was removed): resolved after the delay;
	// a younger absence is kept. Nomad unreadable changes nothing for them.
	prev = []store.SentProblem{sentRow(shop, now.Add(-delay)), sentRow(admin, now.Add(-time.Minute))}
	ch = diffProblems(prev, nil, judged{Disks: true}, exists, now, delay)
	if len(ch.Resolved) != 1 || ch.Resolved[0].Key != problemKey(shop) {
		t.Errorf("cleared: %+v, want shop's resolved only", ch)
	}
}

// The notifier end to end: a check that finds a domain elsewhere is one
// problem.opened with the domain's facts; the domain removed, one
// problem.resolved.
func TestCheckProblemsDomains(t *testing.T) {
	ctx := context.Background()
	e := newNotifyEnv(t)
	e.svc.publicURL = "https://caravana.example.com"
	e.svc.resolveAfter = 5 * time.Minute
	t0 := time.Now().UTC()
	e.clockAt(t0)
	deployHealthy(t, e.env, domainApp("shop", app.Domain{Host: "shop.example.com"}))

	row := store.DomainCheckRecord{Host: "shop.example.com", AppID: "shop", Service: "web", State: string(edge.StateDNSElsewhere),
		DNSStatus: edge.DNSElsewhere, DNSSeen: []string{"198.51.100.9"}, DNSExpected: []string{"203.0.113.1"},
		CertStatus: edge.CertIssued, CheckedAt: t0}
	if err := e.store.UpsertDomainCheck(ctx, row); err != nil {
		t.Fatal(err)
	}
	e.check(t)
	e.check(t)
	got := e.deliveriesOf(t, e.problems)
	if len(got) != 1 {
		t.Fatalf("deliveries = %+v, want one", got)
	}
	opened := payloadOf(t, got[0])
	if opened.Kind != "domain_dns_elsewhere" || opened.Domain != "shop.example.com" || opened.AddressSeen != "198.51.100.9" ||
		opened.URL != "https://caravana.example.com/domains?open=shop.example.com#domain-shop.example.com" {
		t.Errorf("opened = %+v", opened)
	}

	// The domain removed from the app: resolved once gone for the delay.
	deployHealthy(t, e.env, domainApp("shop", app.Domain{Host: "other.example.com"}))
	e.check(t)
	e.clockAt(t0.Add(6 * time.Minute))
	e.check(t)
	got = e.deliveriesOf(t, e.problems)
	if len(got) != 2 || payloadOf(t, got[1]).Type != notify.EventProblemResolved {
		t.Fatalf("deliveries = %+v, want opened then resolved", got)
	}
}
