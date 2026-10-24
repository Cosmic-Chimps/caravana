package service

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The addresses the fake resolver gives the console's own name.
var (
	hostV4 = netip.MustParseAddr("203.0.113.1")
	hostV6 = netip.MustParseAddr("2001:db8::1")
)

// domainApp is an app with one service "web" on port 80 and the domains.
func domainApp(id string, domains ...app.Domain) app.App {
	for i := range domains {
		domains[i].Port = "http"
	}
	return app.App{ID: id, Name: id, Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
		Name: "web", Image: "traefik/whoami:latest", Ports: []app.Port{{Name: "http", ContainerPort: 80}},
		Domains: domains, Health: &app.HealthCheck{Path: "/"},
	}}}
}

// seedDomainApp stores a straight into the store and, when healthy, gives it a
// healthy deployment: the app is served.
func seedDomainApp(t *testing.T, e *env, a app.App, healthy bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.store.InsertApp(ctx, a, nil); err != nil {
		t.Fatalf("InsertApp(%s): %v", a.ID, err)
	}
	if !healthy {
		return
	}
	d := app.Deployment{ID: a.ID + "-d1", Trigger: app.TriggerManual}
	if _, err := e.store.CreateDeployment(ctx, a.ID, d, &a, nil, ""); err != nil {
		t.Fatalf("CreateDeployment(%s): %v", a.ID, err)
	}
	for _, st := range []store.Status{store.StatusRunning, store.StatusHealthy} {
		if _, err := e.store.UpdateDeployment(ctx, d.ID, store.DeploymentUpdate{Status: st, JobVersion: new(uint64(1))}); err != nil {
			t.Fatalf("UpdateDeployment(%s, %s): %v", d.ID, st, err)
		}
	}
}

// stopDomainApp marks an app stopped.
func stopDomainApp(t *testing.T, e *env, id string) {
	t.Helper()
	if err := e.store.SetAppStopped(context.Background(), id, new(time.Now().UTC())); err != nil {
		t.Fatalf("SetAppStopped(%s): %v", id, err)
	}
}

// domainEnv is a service whose console is console.example.com (203.0.113.1 and
// 2001:db8::1), with its clock at the real time so the test CA's certificates
// are valid, and dialer as the edge.
func domainEnv(t *testing.T, resolver edgetest.FakeResolver, dialer edge.Dialer) *env {
	t.Helper()
	e := newEnv(t)
	e.svc.clock.(*fakeClock).Set(time.Now().UTC())
	e.svc.SetPublicURL("https://console.example.com")
	if resolver.Hosts == nil {
		resolver.Hosts = map[string][]string{}
	}
	resolver.Hosts["console.example.com"] = []string{hostV4.String(), hostV6.String(), "127.0.0.1"}
	e.svc.SetEdge(resolver, dialer)
	e.svc.SetEdgeAddress("127.0.0.1:8443")
	return e
}

// edgeOf is a fake edge that presents a trusted certificate for each host and
// Traefik's own for any other.
func edgeOf(t *testing.T, hosts ...string) *edgetest.FakeDialer {
	t.Helper()
	ca := edgetest.NewCA(t, "Test CA")
	d := &edgetest.FakeDialer{Chains: map[string][]*x509.Certificate{}, RootCAs: ca.Pool(),
		Default: []*x509.Certificate{edgetest.TraefikDefault(t)}}
	for _, h := range hosts {
		d.Chains[h] = []*x509.Certificate{ca.Issue(t, []string{h}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))}
	}
	return d
}

// cacheRow stores a check of host with the two statuses.
func cacheRow(t *testing.T, e *env, host, appID, dns, cert string) {
	t.Helper()
	rec := store.DomainCheckRecord{Host: host, AppID: appID, Service: "web", State: string(edge.StateUnknown),
		DNSStatus: dns, CertStatus: cert, CheckedAt: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	if cert == edge.CertIssued {
		rec.CertIssuer, rec.CertNotAfter = "Let's Encrypt", new(time.Date(2026, 12, 24, 9, 0, 0, 0, time.UTC))
	}
	if err := e.store.UpsertDomainCheck(context.Background(), rec); err != nil {
		t.Fatalf("UpsertDomainCheck(%s): %v", host, err)
	}
}

// The list: every domain of every app, the redirecting pair as a row of its own, the state from the
// cache (not_served for a stopped or undeployed app whatever the cache says), the routers, the
// apps that claim one host, and the order (problems, then live, then idle, by host).
func TestDomainsList(t *testing.T) {
	ctx := context.Background()
	e := domainEnv(t, edgetest.FakeResolver{}, edgeOf(t))
	seedDomainApp(t, e, domainApp("shop",
		app.Domain{Host: "www.shop.example.com", WWWRedirect: true}, // shop.example.com redirects here
		app.Domain{Host: "api.shop.example.com", Path: "/api"}), true)
	seedDomainApp(t, e, domainApp("blog", app.Domain{Host: "blog.example.com"}), true)
	seedDomainApp(t, e, domainApp("twin", app.Domain{Host: "blog.example.com"}, app.Domain{Host: "twin.example.com"}), true)
	seedDomainApp(t, e, domainApp("old", app.Domain{Host: "old.example.com"}), true)
	stopDomainApp(t, e, "old")
	seedDomainApp(t, e, domainApp("fresh", app.Domain{Host: "fresh.example.com"}), false)
	seedDomainApp(t, e, domainApp("nodomain"), true)

	cacheRow(t, e, "shop.example.com", "shop", edge.DNSOK, edge.CertIssued)
	cacheRow(t, e, "www.shop.example.com", "shop", edge.DNSOK, edge.CertIssued)
	cacheRow(t, e, "api.shop.example.com", "shop", edge.DNSElsewhere, edge.CertIssued)
	cacheRow(t, e, "blog.example.com", "blog", edge.DNSNone, edge.CertPending)
	cacheRow(t, e, "old.example.com", "old", edge.DNSOK, edge.CertIssued) // from before it stopped

	rows, err := e.svc.Domains(ctx, DomainFilter{})
	if err != nil {
		t.Fatalf("Domains: %v", err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.Host+" "+r.App+" "+string(r.State))
	}
	want := []string{
		"api.shop.example.com shop dns_elsewhere",
		"blog.example.com blog cert_pending",
		"blog.example.com twin cert_pending",
		"www.shop.example.com shop live",
		"fresh.example.com fresh not_served",
		"old.example.com old not_served",
		"shop.example.com shop redirect",
		"twin.example.com twin unknown",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	byKey := map[string]DomainView{}
	for _, r := range rows {
		byKey[r.Host+" "+r.App] = r
	}
	shop := byKey["www.shop.example.com shop"]
	shopRouter := nomadjob.DomainRouterName("shop", "web", "www.shop.example.com")
	if shop.Router != shopRouter || !shop.WWWRedirect || shop.RedirectOf != "" || shop.Port != "http" || shop.ContainerPort != 80 ||
		shop.Project != app.DefaultProject || shop.Environment != app.DefaultEnvironment || shop.AppName != "shop" || shop.Service != "web" ||
		shop.CheckedAt == nil || shop.Certificate.Issuer != "Let's Encrypt" ||
		!shop.Certificate.RenewsAt.Equal(shop.Certificate.NotAfter.Add(-30*24*time.Hour)) || !shop.Deployed || shop.Stopped {
		t.Errorf("shop's row = %+v", shop)
	}
	redirect := byKey["shop.example.com shop"]
	if redirect.RedirectOf != "www.shop.example.com" || redirect.Router != shopRouter || redirect.Taken != nil {
		t.Errorf("the redirect row = %+v", redirect)
	}
	if api := byKey["api.shop.example.com shop"]; api.Path != "/api" || api.Router != nomadjob.DomainRouterName("shop", "web", "api.shop.example.com") {
		t.Errorf("the path row = %+v", api)
	}
	if blog := byKey["blog.example.com blog"]; blog.Router != "blog-web" || !slices.Equal(blog.Taken, []string{"twin"}) {
		t.Errorf("blog's row = %+v, want router blog-web, taken by twin", blog)
	}
	if twin := byKey["blog.example.com twin"]; !slices.Equal(twin.Taken, []string{"blog"}) {
		t.Errorf("twin's row of blog.example.com: taken = %v, want [blog]", twin.Taken)
	}
	if twin := byKey["twin.example.com twin"]; twin.Taken != nil || twin.CheckedAt != nil || twin.DNS.Status != edge.DNSUnknown || twin.Certificate.Status != edge.CertNone {
		t.Errorf("a host never checked = %+v", twin)
	}
	if old := byKey["old.example.com old"]; !old.Stopped || !old.Deployed || old.CheckedAt == nil {
		t.Errorf("the stopped app's row = %+v, want stopped, deployed, its last check kept", old)
	}
	if fresh := byKey["fresh.example.com fresh"]; fresh.Stopped || fresh.Deployed || fresh.CheckedAt != nil {
		t.Errorf("the undeployed app's row = %+v, want not stopped, not deployed", fresh)
	}

	t.Run("filters", func(t *testing.T) {
		rows, err := e.svc.Domains(ctx, DomainFilter{App: "shop"})
		if err != nil || len(rows) != 3 {
			t.Errorf("app shop: %d rows, %v; want 3", len(rows), err)
		}
		rows, err = e.svc.Domains(ctx, DomainFilter{Project: app.DefaultProject})
		if err != nil || len(rows) != len(want) {
			t.Errorf("project default: %d rows, %v; want %d", len(rows), err, len(want))
		}
		if _, err := e.svc.Domains(ctx, DomainFilter{App: "nope"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an app that doesn't exist: %v, want ErrNotFound", err)
		}
		if _, err := e.svc.Domains(ctx, DomainFilter{Project: "nope"}); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("a project that doesn't exist: %v, want ErrProjectNotFound", err)
		}
	})

	t.Run("one domain", func(t *testing.T) {
		d, err := e.svc.Domain(ctx, "Shop.example.com.")
		if err != nil || d.RedirectOf != "www.shop.example.com" {
			t.Errorf("Domain(www) = %+v, %v", d, err)
		}
		if _, err := e.svc.Domain(ctx, "nobody.example.com"); !errors.Is(err, ErrDomainNotFound) {
			t.Errorf("Domain(nobody) = %v, want ErrDomainNotFound", err)
		}
	})
}

// A redirect's pair that is a domain of an app is that app's row, not a redirect.
func TestDomainsRedirectPairTaken(t *testing.T) {
	e := domainEnv(t, edgetest.FakeResolver{}, edgeOf(t))
	seedDomainApp(t, e, domainApp("apex", app.Domain{Host: "example.com", WWWRedirect: true}), true)
	seedDomainApp(t, e, domainApp("other", app.Domain{Host: "www.example.com"}), true)
	rows, err := e.svc.Domains(context.Background(), DomainFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || slices.ContainsFunc(rows, func(r DomainView) bool { return r.RedirectOf != "" }) {
		t.Errorf("rows = %+v, want the two domains and no redirect row", rows)
	}
}

// A fresh check of a stored host: DNS and the certificate, saved as its row, the pages told. A
// host no app has gets DNS only and the records to create; a stopped app's host isn't dialed.
func TestCheckDomain(t *testing.T) {
	ctx := context.Background()
	resolver := edgetest.FakeResolver{Hosts: map[string][]string{
		"shop.example.com": {hostV4.String(), hostV6.String()},
		"new.example.com":  {"198.51.100.9"},
		"example.org":      {hostV4.String()},
		"old.example.com":  {hostV4.String()},
	}}
	dialer := edgeOf(t, "shop.example.com", "new.example.com")
	e := domainEnv(t, resolver, dialer)
	seedDomainApp(t, e, domainApp("shop", app.Domain{Host: "shop.example.com"}), true)
	seedDomainApp(t, e, domainApp("old", app.Domain{Host: "old.example.com"}), true)
	stopDomainApp(t, e, "old")

	changes, unsubscribe := e.svc.Subscribe("shop")
	defer unsubscribe()

	t.Run("a stored host", func(t *testing.T) {
		c, err := e.svc.CheckDomain(ctx, "shop.example.com", "shop")
		if err != nil {
			t.Fatalf("CheckDomain: %v", err)
		}
		if c.DNS.Status != edge.DNSOK || c.Certificate.Status != edge.CertIssued || c.Certificate.Issuer != "Test CA" || c.CheckedAt.IsZero() {
			t.Errorf("check = %+v", c)
		}
		wantRecords := []DNSRecord{{"A", "shop", "203.0.113.1"}, {"AAAA", "shop", "2001:db8::1"}}
		if !slices.Equal(c.Records, wantRecords) {
			t.Errorf("records = %+v, want %+v", c.Records, wantRecords)
		}
		row, err := e.store.GetDomainCheck(ctx, "shop.example.com")
		if err != nil || row.State != string(edge.StateLive) || row.AppID != "shop" || row.Service != "web" ||
			!slices.Equal(row.DNSExpected, []string{"203.0.113.1", "2001:db8::1"}) || row.CertNotAfter == nil {
			t.Errorf("the saved row = %+v, %v", row, err)
		}
		waitFor(t, "the domains notice", func() bool {
			for {
				select {
				case c := <-changes:
					if c.What == ChangeDomains && c.AppID == "shop" {
						return true
					}
				default:
					return false
				}
			}
		})
		if v, _ := e.svc.Domain(ctx, "shop.example.com"); v.State != edge.StateLive {
			t.Errorf("the list now says %s, want live", v.State)
		}
	})

	t.Run("a host no app has", func(t *testing.T) {
		dialed := len(dialer.Dialed())
		c, err := e.svc.CheckDomain(ctx, " New.example.com ", "")
		if err != nil {
			t.Fatalf("CheckDomain: %v", err)
		}
		if c.Host != "new.example.com" || c.DNS.Status != edge.DNSElsewhere || c.Certificate.Status != edge.CertNone ||
			!slices.Equal(c.DNS.Seen, []netip.Addr{netip.MustParseAddr("198.51.100.9")}) {
			t.Errorf("check = %+v", c)
		}
		if len(dialer.Dialed()) != dialed {
			t.Errorf("the edge was dialed for a host no app has: %v", dialer.Dialed())
		}
		if _, err := e.store.GetDomainCheck(ctx, "new.example.com"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a row was saved for a host no app has: %v", err)
		}
		c, err = e.svc.CheckDomain(ctx, "example.org", "")
		if err != nil || c.DNS.Status != edge.DNSOK || !slices.Equal(c.Records, []DNSRecord{{"A", "@", "203.0.113.1"}, {"AAAA", "@", "2001:db8::1"}}) {
			t.Errorf("an apex: %+v, %v", c, err)
		}
	})

	t.Run("a stopped app's host", func(t *testing.T) {
		dialed := len(dialer.Dialed())
		c, err := e.svc.CheckDomain(ctx, "old.example.com", "old")
		if err != nil || c.DNS.Status != edge.DNSOK || c.Certificate.Status != edge.CertNone {
			t.Errorf("check = %+v, %v", c, err)
		}
		if len(dialer.Dialed()) != dialed {
			t.Errorf("the edge was dialed for a stopped app: %v", dialer.Dialed())
		}
		if row, err := e.store.GetDomainCheck(ctx, "old.example.com"); err != nil || row.State != string(edge.StateNotServed) {
			t.Errorf("the saved row = %+v, %v", row, err)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		var fe *app.FieldError
		for _, h := range []string{"", "bad host", "https://x.example.com", "x.example.com/path", "*.example.com", "203.0.113.5"} {
			if _, err := e.svc.CheckDomain(ctx, h, ""); !errors.As(err, &fe) || fe.Field != "host" {
				t.Errorf("CheckDomain(%q) = %v, want a field error on host", h, err)
			}
		}
		if _, err := e.svc.CheckDomain(ctx, "shop.example.com", "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an app that doesn't exist: %v, want ErrNotFound", err)
		}
	})
}

// Traefik's own certificate is "self" without a certificate resolver (the lab) and "pending" with
// one (ACME isn't done yet).
func TestCheckDomainCertResolver(t *testing.T) {
	ctx := context.Background()
	e := domainEnv(t, edgetest.FakeResolver{Hosts: map[string][]string{"shop.example.com": {hostV4.String()}}}, edgeOf(t))
	seedDomainApp(t, e, domainApp("shop", app.Domain{Host: "shop.example.com"}), true)
	if c, err := e.svc.CheckDomain(ctx, "shop.example.com", ""); err != nil || c.Certificate.Status != edge.CertSelf {
		t.Errorf("no cert resolver: %+v, %v; want self", c.Certificate, err)
	}
	e.svc.SetCertResolver("letsencrypt")
	if c, err := e.svc.CheckDomain(ctx, "shop.example.com", ""); err != nil || c.Certificate.Status != edge.CertPending {
		t.Errorf("a cert resolver: %+v, %v; want pending", c.Certificate, err)
	}
	if row, _ := e.store.GetDomainCheck(ctx, "shop.example.com"); row.State != string(edge.StateCertPending) {
		t.Errorf("the row says %s, want cert_pending", row.State)
	}
}

// countingResolver counts lookups and answers from the fake it wraps.
type countingResolver struct {
	mu    sync.Mutex
	n     int
	inner edgetest.FakeResolver
}

func (c *countingResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.inner.LookupIPAddr(ctx, host)
}

func (c *countingResolver) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// This host's addresses: the public URL's host resolved (IPv4 and IPv6, loopback left out), kept
// once found, a failure kept a minute, nothing without a public URL.
func TestHostAddresses(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	clock := e.svc.clock.(*fakeClock)
	r := &countingResolver{inner: edgetest.FakeResolver{
		Hosts: map[string][]string{
			"console.example.com": {"2001:db8::1", "203.0.113.1", "127.0.0.1", "203.0.113.1"},
			"local.example.com":   {"127.0.0.1", "::1"},
		},
		Errors: map[string]error{"down.example.com": errors.New("timeout")},
	}}
	e.svc.SetEdge(r, nil)

	if got := e.svc.HostAddresses(ctx); got != nil || r.calls() != 0 {
		t.Errorf("no public URL: %v after %d lookups, want none", got, r.calls())
	}

	e.svc.SetPublicURL("https://console.example.com:8443/")
	want := []netip.Addr{hostV4, hostV6}
	if got := e.svc.HostAddresses(ctx); !slices.Equal(got, want) {
		t.Errorf("HostAddresses = %v, want %v", got, want)
	}
	clock.Advance(24 * time.Hour)
	e.svc.HostAddresses(ctx)
	if r.calls() != 1 {
		t.Errorf("%d lookups, want the answer kept", r.calls())
	}

	e.svc.SetPublicURL("https://down.example.com")
	if got := e.svc.HostAddresses(ctx); got != nil {
		t.Errorf("a name that doesn't resolve: %v", got)
	}
	clock.Advance(30 * time.Second)
	e.svc.HostAddresses(ctx)
	if r.calls() != 2 {
		t.Errorf("%d lookups within a minute of a failure, want 2", r.calls())
	}
	clock.Advance(time.Minute)
	e.svc.HostAddresses(ctx)
	if r.calls() != 3 {
		t.Errorf("%d lookups after the minute, want a retry", r.calls())
	}

	e.svc.SetPublicURL("http://local.example.com:7070")
	if got := e.svc.HostAddresses(ctx); got != nil {
		t.Errorf("only loopback: %v, want none", got)
	}

	n := r.calls()
	e.svc.SetPublicURL("https://198.51.100.4")
	if got := e.svc.HostAddresses(ctx); !slices.Equal(got, []netip.Addr{netip.MustParseAddr("198.51.100.4")}) || r.calls() != n {
		t.Errorf("an address in the URL: %v after %d more lookups", got, r.calls()-n)
	}

	// A new resolver drops what was kept.
	e.svc.SetPublicURL("https://console.example.com")
	e.svc.HostAddresses(ctx)
	e.svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{"console.example.com": {"192.0.2.8"}}}, nil)
	if got := e.svc.HostAddresses(ctx); !slices.Equal(got, []netip.Addr{netip.MustParseAddr("192.0.2.8")}) {
		t.Errorf("after SetEdge: %v", got)
	}
}

// Without this host's addresses, DNS can't be compared: unknown, except for a loopback name.
func TestCheckDomainWithoutPublicURL(t *testing.T) {
	ctx := context.Background()
	e := domainEnv(t, edgetest.FakeResolver{Hosts: map[string][]string{"app.example.com": {"198.51.100.9"}, "app.lab.test": {"127.0.0.1"}}}, edgeOf(t))
	e.svc.SetPublicURL("")
	c, err := e.svc.CheckDomain(ctx, "app.example.com", "")
	if err != nil || c.DNS.Status != edge.DNSUnknown || len(c.Records) != 0 {
		t.Errorf("a public name: %+v, %v; want unknown and no records", c, err)
	}
	if c, err := e.svc.CheckDomain(ctx, "app.lab.test", ""); err != nil || c.DNS.Status != edge.DNSOK {
		t.Errorf("a loopback name: %+v, %v; want ok", c.DNS, err)
	}
}

// The record's name is "@" for a host with one dot and the first label otherwise.
func TestDNSRecordName(t *testing.T) {
	for host, want := range map[string]string{
		"example.com":     "@",
		"Example.com.":    "@",
		"www.example.com": "www",
		"a.b.example.com": "a",
		"app.example.com": "app",
		"":                "",
		"  ":              "",
		"example.co.uk":   "example", // the registered domain needs the public suffix list; documented in DNSRecordName
	} {
		if got := DNSRecordName(host); got != want {
			t.Errorf("DNSRecordName(%q) = %q, want %q", host, got, want)
		}
	}
}
