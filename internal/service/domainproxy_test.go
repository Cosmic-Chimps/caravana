package service

import (
	"context"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

// A domain behind Cloudflare's proxy: DNS answers Cloudflare's addresses, Traefik presents a trusted
// certificate. The pass stores it as live (from edge.StateOf), the page reads the proxy back from the
// stored addresses, and the needs-you list has no item. The same addresses with no certificate yet
// are dns_elsewhere and an item.
func TestDomainBehindCloudflareIsNotAnItem(t *testing.T) {
	ctx := context.Background()
	resolver := edgetest.FakeResolver{Hosts: map[string][]string{
		"proxied.example.com": {"188.114.96.3", "2a06:98c1:3120::3"},
		"other.example.com":   {"198.51.100.9"},
		"fresh.example.com":   {"188.114.96.3"},
	}}
	// edgeOf gives a trusted certificate to the two named hosts and Traefik's own to fresh.
	dialer := edgeOf(t, "proxied.example.com", "other.example.com")
	e := domainEnv(t, resolver, dialer)
	seedDomainApp(t, e, domainApp("shop", app.Domain{Host: "proxied.example.com"}, app.Domain{Host: "other.example.com"}), true)
	seedDomainApp(t, e, domainApp("fresh", app.Domain{Host: "fresh.example.com"}), true)

	if err := e.svc.checkDomainTargets(ctx, DomainFilter{}, true); err != nil {
		t.Fatalf("pass: %v", err)
	}
	for host, want := range map[string]edge.State{
		"proxied.example.com": edge.StateLive,
		"other.example.com":   edge.StateLive,
		"fresh.example.com":   edge.StateDNSElsewhere,
	} {
		row, err := e.store.GetDomainCheck(ctx, host)
		if err != nil {
			t.Fatal(err)
		}
		if row.State != string(want) || row.DNSStatus != edge.DNSElsewhere {
			t.Errorf("%s: stored state %q with DNS %q, want %q with DNS elsewhere", host, row.State, row.DNSStatus, want)
		}
	}
	views, err := e.svc.Domains(ctx, DomainFilter{})
	if err != nil {
		t.Fatal(err)
	}
	wantProxy := map[string]string{"proxied.example.com": edge.ProxyCloudflare, "other.example.com": "", "fresh.example.com": edge.ProxyCloudflare}
	for _, v := range views {
		if v.DNS.Proxy != wantProxy[v.Host] {
			t.Errorf("%s: proxy %q, want %q", v.Host, v.DNS.Proxy, wantProxy[v.Host])
		}
	}
	got, err := e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var elsewhere []string
	for _, it := range got.Items {
		if it.Kind == AttentionDomainDNSElsewhere || it.Kind == AttentionDomainCertExpiring {
			elsewhere = append(elsewhere, it.Domain)
		}
	}
	if len(elsewhere) != 1 || elsewhere[0] != "fresh.example.com" {
		t.Errorf("domain items for %v, want only fresh.example.com (no certificate yet)", elsewhere)
	}
}
