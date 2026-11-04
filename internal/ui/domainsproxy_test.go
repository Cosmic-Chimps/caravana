package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A domain whose DNS answers a proxy's addresses while Traefik presents a valid certificate is live:
// its DNS fact says what the addresses are as information, there is no banner, no "Change the record
// to" box and no "isn't this host" sentence, and the Open link stays. With no certificate yet the same
// addresses are still "DNS points elsewhere".
func TestDomainsBehindAProxyAreLive(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, domainsApp("shop", "cf.example.com", "cdn.example.com", "soon.example.com", "fresh.example.com"),
		seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	here := []string{"203.0.113.1"}
	cf := []string{"188.114.96.3", "2a06:98c1:3120::3"}
	for _, c := range []checkSeed{
		{host: "cf.example.com", dns: edge.DNSElsewhere, seen: cf, expected: here, cert: edge.CertIssued, issuer: "Let's Encrypt", notAfterFromNow: 50 * day},
		{host: "cdn.example.com", dns: edge.DNSElsewhere, seen: []string{"198.51.100.9"}, expected: here, cert: edge.CertIssued, issuer: "Let's Encrypt", notAfterFromNow: 50 * day},
		{host: "soon.example.com", dns: edge.DNSElsewhere, seen: cf, expected: here, cert: edge.CertExpiring, issuer: "Let's Encrypt", notAfterFromNow: 6*day + time.Hour},
		{host: "fresh.example.com", dns: edge.DNSElsewhere, seen: cf, expected: here, cert: edge.CertPending, issuer: edge.TraefikOwn},
	} {
		c.app = "shop"
		e.seedCheck(t, c)
	}
	// The stored state is not read: the page derives it from the stored facts.
	if row, err := e.Store.GetDomainCheck(context.Background(), "cf.example.com"); err != nil || row.State != string(edge.StateUnknown) {
		t.Fatalf("the seed = %+v, %v", row, err)
	}

	body := e.pageOf(t, "/domains")
	for host, wants := range map[string][]string{
		"cf.example.com":   {"live · HTTPS", "DNS A → 188.114.96.3, AAAA → 2a06:98c1:3120::3 · Cloudflare's proxy"},
		"cdn.example.com":  {"live · HTTPS", "DNS A → 198.51.100.9 · a proxy or CDN in front of this host"},
		"soon.example.com": {"certificate expiring", "· Cloudflare's proxy"},
	} {
		row := dmRowOf(body, host)
		for _, want := range wants {
			if !strings.Contains(text(row), want) {
				t.Errorf("%s's row lacks %q:\n%s", host, want, text(row))
			}
		}
		for _, never := range []string{"DNS points elsewhere", "Change the record to", "isn't this host", "should be"} {
			if strings.Contains(text(row), never) {
				t.Errorf("%s's row says %q:\n%s", host, never, text(row))
			}
		}
	}
	for _, host := range []string{"cf.example.com", "cdn.example.com"} {
		row := dmRowOf(body, host)
		if !strings.Contains(row, `class="cv-iconlink" aria-label="Open https://`+host+`"`) {
			t.Errorf("%s has no Open link", host)
		}
		if strings.Contains(row, "cv-mark-failed") {
			t.Errorf("%s is drawn in the failed colour", host)
		}
	}
	if row := dmRowOf(body, "fresh.example.com"); !strings.Contains(text(row), "DNS points elsewhere") {
		t.Errorf("fresh's row:\n%s", text(row))
	}
	// Only fresh (no certificate) and soon (expiring) need the owner, and soon's banner is the
	// certificate's, not DNS's.
	for _, host := range []string{"cf.example.com", "cdn.example.com", "soon.example.com"} {
		if strings.Contains(text(body), host+" points at") {
			t.Errorf("a banner says %s points elsewhere:\n%s", host, text(body))
		}
	}
	if !strings.Contains(text(body), "4 domains · 2 need you") {
		t.Errorf("the count isn't 2 needing the owner:\n%s", text(body))
	}
}

// A domain whose DNS answers this host's own address and another one is not behind a proxy, even
// with a valid certificate: a proxy never answers with the origin's address, so some visitors reach
// the other host. Its row is "DNS points elsewhere" with a banner and a box naming the record to
// remove. The same mix with a Cloudflare address is a proxy and stays live.
func TestDomainsPointingHereAndElsewhere(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, domainsApp("shop", "split.example.com", "cfsplit.example.com"),
		seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	here := []string{"203.0.113.1"}
	for _, c := range []checkSeed{
		{host: "split.example.com", dns: edge.DNSElsewhere, seen: []string{"203.0.113.1", "198.51.100.9"}, expected: here, cert: edge.CertIssued, issuer: "Let's Encrypt", notAfterFromNow: 50 * day},
		{host: "cfsplit.example.com", dns: edge.DNSElsewhere, seen: []string{"203.0.113.1", "188.114.96.3"}, expected: here, cert: edge.CertIssued, issuer: "Let's Encrypt", notAfterFromNow: 50 * day},
	} {
		c.app = "shop"
		e.seedCheck(t, c)
	}

	body := e.pageOf(t, "/domains")
	row := dmRowOf(body, "split.example.com")
	for _, want := range []string{
		"DNS points elsewhere",
		"Remove this record: Type A Name split Value 198.51.100.9",
		"keep A · 203.0.113.1",
	} {
		if !strings.Contains(text(row), want) {
			t.Errorf("the split row lacks %q:\n%s", want, text(row))
		}
	}
	if !strings.Contains(row, "cv-mark-failed") {
		t.Errorf("the split row isn't drawn in the failed colour")
	}
	if strings.Contains(row, "cv-iconlink") {
		t.Errorf("the split row has an Open link")
	}
	if !strings.Contains(text(body), "split.example.com points here and at 198.51.100.9: remove the record to 198.51.100.9.") {
		t.Errorf("no banner for the split domain:\n%s", text(body))
	}

	cf := dmRowOf(body, "cfsplit.example.com")
	for _, want := range []string{"live · HTTPS", "· Cloudflare's proxy"} {
		if !strings.Contains(text(cf), want) {
			t.Errorf("cfsplit's row lacks %q:\n%s", want, text(cf))
		}
	}
	if strings.Contains(text(body), "cfsplit.example.com points") {
		t.Errorf("a banner names cfsplit:\n%s", text(body))
	}
}
