package service

import (
	"context"
	"crypto/x509"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/dns/dnstest"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// consoleEnv is a cluster whose console is https://console.example.com (203.0.113.1, 2001:db8::1), with
// Traefik's job reading the console's hosts from its variable, fake DNS that knows new.example.com
// (here) and elsewhere.example.com (another address), a CA the edge trusts for the hosts the test
// issues, and a route probe the test switches.
type consoleEnv struct {
	*clusterEnv
	resolver edgetest.FakeResolver
	dialer   *edgetest.FakeDialer
	ca       *edgetest.CA
	routeOK  atomic.Bool
	probes   atomic.Int32
}

func newConsoleEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := &consoleEnv{clusterEnv: newClusterEnv(t)}
	e.svc.clock.(*fakeClock).Set(time.Now().UTC()) // the test CA's certificates are valid now
	e.svc.SetPublicURL("https://console.example.com")
	here := []string{hostV4.String(), hostV6.String()}
	e.resolver = edgetest.FakeResolver{Hosts: map[string][]string{
		"console.example.com":   here,
		"new.example.com":       here,
		"elsewhere.example.com": {"198.51.100.7"},
	}}
	e.ca = edgetest.NewCA(t, "Test CA")
	e.dialer = &edgetest.FakeDialer{Chains: map[string][]*x509.Certificate{}, RootCAs: e.ca.Pool(),
		Default: []*x509.Certificate{edgetest.TraefikDefault(t)}}
	e.issue(t, "console.example.com", "new.example.com")
	e.svc.SetEdge(e.resolver, e.dialer)
	e.svc.SetEdgeAddress("127.0.0.1:8443")
	e.svc.SetCertResolver("letsencrypt")
	e.nomad.SetTraefikConsoleRoute("traefik:v3.7.13")
	e.routeOK.Store(true)
	e.svc.SetConsoleRouteProbe(func(_ context.Context, edgeAddress, host string) error {
		e.probes.Add(1)
		if edgeAddress != "127.0.0.1:8443" {
			return errors.New("probed the wrong address " + edgeAddress)
		}
		if !e.routeOK.Load() {
			return errors.New("not routed")
		}
		return nil
	})
	e.svc.consoleAddr.poll = time.Millisecond
	e.svc.consoleAddr.limit = 400 * time.Millisecond
	return e
}

// issue makes the edge present a trusted certificate for each host.
func (e *consoleEnv) issue(t *testing.T, hosts ...string) {
	t.Helper()
	for _, h := range hosts {
		e.dialer.Chains[h] = []*x509.Certificate{e.ca.Issue(t, []string{h}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))}
	}
}

// agent makes the first server report that its agent runs the console.
func (e *consoleEnv) agent(t *testing.T) {
	t.Helper()
	r := healthyReport()
	r.RunsConsole = true
	e.firstSync(t, r)
}

func (e *consoleEnv) start(t *testing.T, domain string) store.ConsoleAddressChange {
	t.Helper()
	c, err := e.svc.StartConsoleAddressChange(context.Background(), domain, 30, false)
	if err != nil {
		t.Fatalf("StartConsoleAddressChange(%q): %v", domain, err)
	}
	return c
}

// wait polls until the change has one of the statuses.
func (e *consoleEnv) wait(t *testing.T, id string, statuses ...string) store.ConsoleAddressChange {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := e.store.ConsoleAddressChange(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(statuses, c.Status) {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("change %s is %q, want one of %v after 5s; checks %+v", id, c.Status, statuses, c.Checks)
		}
		time.Sleep(time.Millisecond)
	}
}

// ready starts a change to new.example.com and waits until it is ready, with the agent reporting.
func (e *consoleEnv) ready(t *testing.T) store.ConsoleAddressChange {
	t.Helper()
	e.agent(t)
	c := e.start(t, "new.example.com")
	return e.wait(t, c.ID, store.ConsoleReady)
}

// hosts is what Traefik's console route variable says.
func (e *consoleEnv) hosts() string { return e.nomad.Variable(deploy.ConsoleHostsPath)["hosts"] }

func checkOf(c store.ConsoleAddressChange, name string) store.ConsoleCheck {
	for _, ck := range c.Checks {
		if ck.Name == name {
			return ck
		}
	}
	return store.ConsoleCheck{}
}

// noChange says the store has no change at all and Traefik's variable was never written.
func (e *consoleEnv) noChange(t *testing.T) {
	t.Helper()
	if _, ok, err := e.store.LatestConsoleAddressChange(context.Background()); err != nil || ok {
		t.Errorf("a change was recorded (%v, %v); a refusal records nothing", ok, err)
	}
	if v := e.nomad.Variable(deploy.ConsoleHostsPath); v != nil {
		t.Errorf("Traefik's console variable = %v; a refusal changes no route", v)
	}
}

func TestStartConsoleAddressChangeRefusals(t *testing.T) {
	ctx := context.Background()
	fieldOf := func(t *testing.T, err error) string {
		t.Helper()
		var fe *app.FieldError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v, want an *app.FieldError", err)
		}
		return fe.Field
	}

	t.Run("a name that isn't a host name, an address, the current domain", func(t *testing.T) {
		e := newConsoleEnv(t)
		for _, domain := range []string{"", "a b.test", "1.2.3.4", "[::1]", "localhost", "console", "a.test/x", "a.test:8443", "https://a.test",
			"a$(x).test", "a\nb.test", "console.example.com", "Console.Example.COM.", strings.Repeat("a", 64) + ".test"} {
			_, err := e.svc.StartConsoleAddressChange(ctx, domain, 30, false)
			if got := fieldOf(t, err); got != "domain" {
				t.Errorf("%q: field = %q, want domain", domain, got)
			}
		}
		e.noChange(t)
	})

	t.Run("redirect days", func(t *testing.T) {
		e := newConsoleEnv(t)
		for _, days := range []int{-1, 1, 5, 31, 91} {
			_, err := e.svc.StartConsoleAddressChange(ctx, "new.example.com", days, false)
			if got := fieldOf(t, err); got != "redirectDays" {
				t.Errorf("%d days: field = %q, want redirectDays", days, got)
			}
		}
		for _, days := range []int{0, 7, 30, 90} {
			e2 := newConsoleEnv(t)
			if _, err := e2.svc.StartConsoleAddressChange(ctx, "new.example.com", days, false); err != nil {
				t.Errorf("%d days refused: %v", days, err)
			}
		}
		e.noChange(t)
	})

	t.Run("an installation that can't change", func(t *testing.T) {
		tests := []struct {
			name string
			set  func(e *consoleEnv)
			want string
		}{
			{"no public address", func(e *consoleEnv) { e.svc.SetPublicURL("") }, "no public https address"},
			{"an address that isn't https", func(e *consoleEnv) { e.svc.SetPublicURL("http://127.0.0.1:7070") }, "no public https address"},
			{"a Traefik job from before the feature", func(e *consoleEnv) { e.nomad.SetTraefikJob("traefik:v3.7.13") }, "run the installer again"},
		}
		for _, tt := range tests {
			e := newConsoleEnv(t)
			tt.set(e)
			_, err := e.svc.StartConsoleAddressChange(ctx, "new.example.com", 30, false)
			var ue *ConsoleUnsupportedError
			if !errors.Is(err, ErrConsoleAddressUnsupported) || !errors.As(err, &ue) || !strings.Contains(ue.Words, tt.want) {
				t.Errorf("%s: err = %v, want ErrConsoleAddressUnsupported saying %q", tt.name, err, tt.want)
			}
			e.noChange(t)
			if v, _ := e.svc.ConsoleAddress(ctx); v.CanChange || v.CannotChange == "" {
				t.Errorf("%s: the view says CanChange=%v %q", tt.name, v.CanChange, v.CannotChange)
			}
		}
	})

	t.Run("an app has the domain", func(t *testing.T) {
		e := newConsoleEnv(t)
		seedDomainApp(t, e.env, domainApp("shop", app.Domain{Host: "new.example.com"}), true)
		_, err := e.svc.StartConsoleAddressChange(ctx, "new.example.com", 30, false)
		var taken *DomainTakenError
		if !errors.Is(err, ErrDomainTaken) || !errors.As(err, &taken) || taken.Field != "domain" || taken.App != "shop" {
			t.Fatalf("err = %v, want a *DomainTakenError on domain naming shop", err)
		}
		// The pair a wwwRedirect redirects counts too.
		seedDomainApp(t, e.env, domainApp("blog", app.Domain{Host: "www.pair.example.com", WWWRedirect: true}), true)
		if _, err := e.svc.StartConsoleAddressChange(ctx, "pair.example.com", 30, false); !errors.Is(err, ErrDomainTaken) {
			t.Errorf("a redirect pair: err = %v, want ErrDomainTaken", err)
		}
		e.noChange(t)
	})

	t.Run("DNS that doesn't point here", func(t *testing.T) {
		e := newConsoleEnv(t)
		for domain, want := range map[string]string{
			"nowhere.example.com":   "has no DNS record",
			"elsewhere.example.com": "198.51.100.7, not at this server",
		} {
			_, err := e.svc.StartConsoleAddressChange(ctx, domain, 30, false)
			var nh *DomainNotHereError
			if !errors.Is(err, ErrDomainNotHere) || !errors.As(err, &nh) || nh.Field != "domain" || !strings.Contains(nh.Words, want) || nh.Zone != "" {
				t.Errorf("%s: err = %v (%+v), want ErrDomainNotHere saying %q and no zone", domain, err, nh, want)
			}
		}
		// This server's own address isn't known: nothing can be compared.
		e.svc.SetEdge(edgetest.FakeResolver{}, nil)
		_, err := e.svc.StartConsoleAddressChange(ctx, "new.example.com", 30, false)
		var nh *DomainNotHereError
		if !errors.As(err, &nh) || !strings.Contains(nh.Words, "can't tell this server's own public address") {
			t.Errorf("unknown own address: err = %v", err)
		}
		e.noChange(t)
	})

	t.Run("a second change while one is open", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.routeOK.Store(false)
		e.svc.consoleAddr.limit = time.Minute
		c := e.start(t, "new.example.com")
		if _, err := e.svc.StartConsoleAddressChange(ctx, "new.example.com", 30, false); !errors.Is(err, ErrConsoleChangeOpen) {
			t.Errorf("second start: err = %v, want ErrConsoleChangeOpen", err)
		}
		if got, _, _ := e.store.OpenConsoleAddressChange(ctx); got.ID != c.ID {
			t.Errorf("the open change is %q, want the first", got.ID)
		}
		if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestConsoleAddressChangeGetsReady(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	e.agent(t)
	c := e.start(t, "new.example.com")
	if c.Status != store.ConsoleChecking || c.OldDomain != "console.example.com" || c.RedirectDays != 30 || len(c.ID) != 32 ||
		checkOf(c, "hostname").State != "ok" || checkOf(c, "dns").State != "ok" ||
		checkOf(c, "route").State != "waiting" || checkOf(c, "certificate").State != "waiting" {
		t.Fatalf("started change = %+v", c)
	}

	got := e.wait(t, c.ID, store.ConsoleReady, store.ConsoleCheckFailed)
	if got.Status != store.ConsoleReady || got.ReadyAt.IsZero() || got.Reason != "" {
		t.Fatalf("change = %+v, want ready", got)
	}
	for _, name := range []string{"hostname", "dns", "route", "certificate"} {
		if ck := checkOf(got, name); ck.State != "ok" || ck.Message == "" {
			t.Errorf("check %s = %+v, want ok with a message", name, ck)
		}
	}
	if !strings.Contains(checkOf(got, "certificate").Message, "Test CA") {
		t.Errorf("certificate message = %q, want the issuer", checkOf(got, "certificate").Message)
	}
	// The domain is in Traefik's route beside the current one, which stays first.
	if e.hosts() != "console.example.com new.example.com" {
		t.Errorf("route hosts = %q, want the current domain then the new one", e.hosts())
	}
	// Nothing about the address changed yet.
	if e.svc.PublicURL() != "https://console.example.com" {
		t.Errorf("public URL = %q", e.svc.PublicURL())
	}
	// A ready change is read back by ID, and an unknown ID is a not-found.
	if again, err := e.svc.ConsoleAddressChange(ctx, c.ID); err != nil || again.Status != store.ConsoleReady {
		t.Errorf("ConsoleAddressChange = %+v, %v", again, err)
	}
	if _, err := e.svc.ConsoleAddressChange(ctx, strings.Repeat("0", 32)); !errors.Is(err, ErrConsoleChangeNotFound) {
		t.Errorf("unknown ID: err = %v, want ErrConsoleChangeNotFound", err)
	}
}

func TestConsoleAddressChecksWait(t *testing.T) {
	ctx := context.Background()

	t.Run("the route comes after a few looks", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.svc.consoleAddr.limit = 5 * time.Second
		e.routeOK.Store(false)
		c := e.start(t, "new.example.com")
		for e.probes.Load() < 3 {
			time.Sleep(time.Millisecond)
		}
		if got, _ := e.store.ConsoleAddressChange(ctx, c.ID); got.Status != store.ConsoleChecking || checkOf(got, "route").State != "waiting" {
			t.Errorf("while the route is missing: %+v", got)
		}
		e.routeOK.Store(true)
		e.wait(t, c.ID, store.ConsoleReady)
	})

	t.Run("no route in time fails the change and takes the domain back", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.routeOK.Store(false)
		c := e.start(t, "new.example.com")
		got := e.wait(t, c.ID, store.ConsoleCheckFailed)
		if !strings.Contains(got.Reason, "didn't route new.example.com to Caravana within") || checkOf(got, "route").State != "failed" || got.SettledAt.IsZero() {
			t.Errorf("failed change = %+v", got)
		}
		// The route is taken back right after the state is written.
		deadline := time.Now().Add(5 * time.Second)
		for e.nomad.Variable(deploy.ConsoleHostsPath) != nil && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if v := e.nomad.Variable(deploy.ConsoleHostsPath); v != nil {
			t.Errorf("after a failed check the route variable = %v; the job's own domain should rule", v)
		}
		if _, ok, _ := e.store.OpenConsoleAddressChange(ctx); ok {
			t.Error("a failed change is still open")
		}
		// It can be cancelled, which only changes its state, and another can start.
		if got, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); err != nil || got.Status != store.ConsoleCancelled {
			t.Errorf("cancel a failed change = %+v, %v", got, err)
		}
		e.routeOK.Store(true)
		e.start(t, "new.example.com")
	})

	t.Run("a certificate Traefik hasn't got yet", func(t *testing.T) {
		e := newConsoleEnv(t)
		delete(e.dialer.Chains, "new.example.com") // Traefik's own certificate while it asks Let's Encrypt
		c := e.start(t, "new.example.com")
		got := e.wait(t, c.ID, store.ConsoleCheckFailed)
		if !strings.Contains(got.Reason, "got no certificate within") || checkOf(got, "certificate").State != "failed" || checkOf(got, "route").State != "ok" {
			t.Errorf("change = %+v", got)
		}
	})

	t.Run("Traefik's own certificate is the one an installation without Let's Encrypt keeps", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.svc.SetCertResolver("") // --acme none
		delete(e.dialer.Chains, "new.example.com")
		c := e.start(t, "new.example.com")
		got := e.wait(t, c.ID, store.ConsoleReady, store.ConsoleCheckFailed)
		if got.Status != store.ConsoleReady || !strings.Contains(checkOf(got, "certificate").Message, "Traefik's own certificate") {
			t.Errorf("change = %+v, want ready with Traefik's own certificate", got)
		}
	})

	t.Run("an untrusted certificate is waited for, then fails", func(t *testing.T) {
		e := newConsoleEnv(t)
		other := edgetest.NewCA(t, "Staging CA")
		e.dialer.Chains["new.example.com"] = []*x509.Certificate{other.Issue(t, []string{"new.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))}
		c := e.start(t, "new.example.com")
		got := e.wait(t, c.ID, store.ConsoleCheckFailed)
		if !strings.Contains(checkOf(got, "certificate").Message, "isn't one a browser trusts") {
			t.Errorf("certificate check = %+v", checkOf(got, "certificate"))
		}
	})

	t.Run("Nomad refusing the route fails the change", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.nomad.FailVariables(500)
		c := e.start(t, "new.example.com")
		got := e.wait(t, c.ID, store.ConsoleCheckFailed)
		if !strings.Contains(got.Reason, "couldn't put the domain in Traefik's route") {
			t.Errorf("reason = %q", got.Reason)
		}
	})
}

func TestConsoleAddressCreatesTheDNSRecord(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	p := dnstest.NewProvider()
	p.AddZone("example.com")
	p.WantToken(dnsSentinel)
	e.svc.SetDNSProviders(p.Factory())
	if _, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Token: new(dnsSentinel)}); err != nil {
		t.Fatal(err)
	}
	e.issue(t, "fresh.example.com")

	// Without the owner's word the refusal offers the zone and changes nothing at the provider.
	_, err := e.svc.StartConsoleAddressChange(ctx, "fresh.example.com", 30, false)
	var nh *DomainNotHereError
	if !errors.As(err, &nh) || nh.Zone != "example.com" || !strings.Contains(nh.Words, "let Caravana create the record in zone example.com") {
		t.Fatalf("err = %v, want ErrDomainNotHere offering zone example.com", err)
	}
	if recs := p.Records("example.com"); len(recs) != 0 {
		t.Fatalf("records after a refusal = %+v, want none", recs)
	}
	e.noChange(t)

	c, err := e.svc.StartConsoleAddressChange(ctx, "fresh.example.com", 30, true)
	if err != nil {
		t.Fatalf("with createRecord: %v", err)
	}
	if !c.DNSRecordCreated || !strings.Contains(checkOf(c, "dns").Message, "Caravana made the DNS record") {
		t.Errorf("change = %+v, want dnsRecordCreated and the DNS check saying so", c)
	}
	recs := p.Records("example.com")
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want an A and an AAAA", recs)
	}
	for _, r := range recs {
		if r.Name != "fresh.example.com" || r.Comment != "caravana:console" ||
			(r.Type != "A" || r.Content != hostV4.String()) && (r.Type != "AAAA" || r.Content != hostV6.String()) {
			t.Errorf("record = %+v", r)
		}
	}
	// No row: the pass that removes the records of apps never removes these.
	if rows, _ := e.store.ListDNSRecords(ctx); len(rows) != 0 {
		t.Errorf("dns_records = %+v, want none", rows)
	}
	e.wait(t, c.ID, store.ConsoleReady)

	// Cancelling and asking again makes no second set of records.
	if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartConsoleAddressChange(ctx, "fresh.example.com", 30, true); err != nil {
		t.Fatal(err)
	}
	if recs := p.Records("example.com"); len(recs) != 2 {
		t.Errorf("records after a second ask = %d, want the same 2", len(recs))
	}
}

func TestConsoleAddressRefusesAnExistingRecordElsewhere(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	p := dnstest.NewProvider()
	p.AddZone("example.com")
	p.WantToken(dnsSentinel)
	e.svc.SetDNSProviders(p.Factory())
	if _, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Token: new(dnsSentinel)}); err != nil {
		t.Fatal(err)
	}
	// DNS shows nothing yet (a record just made elsewhere), but the provider has a CNAME: never overwritten.
	p.Seed("example.com", dns.Record{Type: "CNAME", Name: "fresh.example.com", Content: "other.example.net", TTL: 1})
	_, err := e.svc.StartConsoleAddressChange(ctx, "fresh.example.com", 30, true)
	var nh *DomainNotHereError
	if !errors.As(err, &nh) || !strings.Contains(nh.Words, "already has a record at Cloudflare pointing at other.example.net") {
		t.Fatalf("err = %v, want the existing record refused", err)
	}
	if recs := p.Records("example.com"); len(recs) != 1 {
		t.Errorf("records = %+v; the CNAME must be alone", recs)
	}
	e.noChange(t)

	// A provider that refuses the token is the 422 of deploys.
	p.SetRefuse(true)
	_, err = e.svc.StartConsoleAddressChange(ctx, "fresh2.example.com", 30, true)
	var pe *DNSProviderError
	if !errors.As(err, &pe) || pe.Field != "domain" || !errors.Is(err, dns.ErrRefused) {
		t.Errorf("a refusing provider: err = %v, want a *DNSProviderError on domain", err)
	}
	e.noChange(t)
}

func TestCancelConsoleAddressChange(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	c := e.ready(t)
	if e.hosts() == "" {
		t.Fatal("the domain isn't in the route before the cancel")
	}
	got, err := e.svc.CancelConsoleAddressChange(ctx, c.ID)
	if err != nil || got.Status != store.ConsoleCancelled || got.SettledAt.IsZero() {
		t.Fatalf("cancel = %+v, %v", got, err)
	}
	if v := e.nomad.Variable(deploy.ConsoleHostsPath); v != nil {
		t.Errorf("after the cancel the route variable = %v; the pending domain must leave the route", v)
	}
	if _, ok, _ := e.store.OpenConsoleAddressChange(ctx); ok {
		t.Error("a cancelled change is still open")
	}
	// Cancelling again, and an unknown ID.
	if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); !errors.Is(err, ErrConsoleChangeNotCancellable) {
		t.Errorf("second cancel: err = %v, want ErrConsoleChangeNotCancellable", err)
	}
	if _, err := e.svc.CancelConsoleAddressChange(ctx, strings.Repeat("0", 32)); !errors.Is(err, ErrConsoleChangeNotFound) {
		t.Errorf("unknown ID: err = %v, want ErrConsoleChangeNotFound", err)
	}

	t.Run("a change that is checking can be cancelled, and the check goroutine ends", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.routeOK.Store(false)
		e.svc.consoleAddr.limit = time.Minute
		c := e.start(t, "new.example.com")
		if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			e.svc.consoleAddr.mu.Lock()
			going := e.svc.consoleAddr.running[c.ID]
			e.svc.consoleAddr.mu.Unlock()
			if !going {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the check goroutine of a cancelled change is still going")
			}
			time.Sleep(time.Millisecond)
		}
		if got, _ := e.store.ConsoleAddressChange(ctx, c.ID); got.Status != store.ConsoleCancelled {
			t.Errorf("status = %q; the goroutine must not undo the cancel", got.Status)
		}
	})

	t.Run("a switching change can't be cancelled", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); !errors.Is(err, ErrConsoleChangeNotCancellable) {
			t.Errorf("err = %v, want ErrConsoleChangeNotCancellable", err)
		}
	})
}

func TestSwitchConsoleAddress(t *testing.T) {
	ctx := context.Background()

	t.Run("needs an agent that runs the console", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.start(t, "new.example.com")
		e.wait(t, c.ID, store.ConsoleReady)
		_, err := e.svc.SwitchConsoleAddress(ctx, c.ID)
		var na ConsoleNoAgentError
		if !errors.Is(err, ErrNoAgent) || !errors.As(err, &na) {
			t.Fatalf("no agent: err = %v, want ErrNoAgent", err)
		}
		// An agent that doesn't run the console isn't enough.
		e.firstSync(t, healthyReport())
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); !errors.Is(err, ErrNoAgent) {
			t.Errorf("an agent that doesn't run the console: err = %v, want ErrNoAgent", err)
		}
		// A report that is too old isn't enough either.
		e.agent(t)
		e.svc.clock.(*fakeClock).Set(time.Now().UTC().Add(11 * time.Minute))
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); !errors.Is(err, ErrNoAgent) {
			t.Errorf("a report from 11 minutes ago: err = %v, want ErrNoAgent", err)
		}
		if got, _ := e.store.ConsoleAddressChange(ctx, c.ID); got.Status != store.ConsoleReady {
			t.Errorf("a refused switch left the change %q", got.Status)
		}
	})

	t.Run("a ready change switches once", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		got, err := e.svc.SwitchConsoleAddress(ctx, c.ID)
		if err != nil || got.Status != store.ConsoleSwitching || got.SwitchAt.IsZero() {
			t.Fatalf("switch = %+v, %v", got, err)
		}
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); !errors.Is(err, ErrConsoleChangeNotReady) {
			t.Errorf("second switch: err = %v, want ErrConsoleChangeNotReady", err)
		}
		if _, err := e.svc.SwitchConsoleAddress(ctx, strings.Repeat("0", 32)); !errors.Is(err, ErrConsoleChangeNotFound) {
			t.Errorf("unknown ID: err = %v, want ErrConsoleChangeNotFound", err)
		}
		// Nothing changed yet: the agent does that, and Caravana restarts under the new address.
		if e.svc.PublicURL() != "https://console.example.com" {
			t.Errorf("public URL = %q", e.svc.PublicURL())
		}
	})

	t.Run("a change that isn't ready", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.agent(t)
		e.routeOK.Store(false)
		e.svc.consoleAddr.limit = time.Minute
		c := e.start(t, "new.example.com")
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); !errors.Is(err, ErrConsoleChangeNotReady) {
			t.Errorf("a checking change: err = %v, want ErrConsoleChangeNotReady", err)
		}
	})

	t.Run("a route that went away since the checks passed stops the switch", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		e.routeOK.Store(false)
		_, err := e.svc.SwitchConsoleAddress(ctx, c.ID)
		var ce *ConsoleCheckError
		if !errors.Is(err, ErrConsoleCheckFailed) || !errors.As(err, &ce) || ce.Field != "domain" {
			t.Fatalf("err = %v, want a *ConsoleCheckError on domain", err)
		}
		got, _ := e.store.ConsoleAddressChange(ctx, c.ID)
		if got.Status != store.ConsoleReady || checkOf(got, "route").State != "failed" {
			t.Errorf("change = %+v, want ready with the route check failed", got)
		}
		// Once it is back the same change switches.
		e.routeOK.Store(true)
		if got, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil || got.Status != store.ConsoleSwitching {
			t.Errorf("switch after the route came back = %+v, %v", got, err)
		}
	})

	t.Run("DNS that moved away since the checks passed stops the switch", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		e.resolver.Hosts["new.example.com"] = []string{"198.51.100.7"}
		_, err := e.svc.SwitchConsoleAddress(ctx, c.ID)
		var nh *DomainNotHereError
		if !errors.As(err, &nh) || nh.Field != "domain" {
			t.Fatalf("err = %v, want a *DomainNotHereError on domain", err)
		}
		if got, _ := e.store.ConsoleAddressChange(ctx, c.ID); got.Status != store.ConsoleReady || checkOf(got, "dns").State != "failed" {
			t.Errorf("change = %+v, want ready with the dns check failed", got)
		}
	})

	t.Run("a certificate that went away stops the switch", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		delete(e.dialer.Chains, "new.example.com")
		_, err := e.svc.SwitchConsoleAddress(ctx, c.ID)
		var ce *ConsoleCheckError
		if !errors.As(err, &ce) || !strings.Contains(ce.Words, "no certificate") {
			t.Fatalf("err = %v, want a *ConsoleCheckError about the certificate", err)
		}
	})
}

// consoleReport is the first server's report with an outcome for the change.
func consoleReport(id, state, reason string) NodeReport {
	r := healthyReport()
	r.RunsConsole = true
	r.ConsoleAddress = &NodeConsoleOutcome{ID: id, State: state, Reason: reason}
	return r
}

func TestConsoleHandoffThroughNodeSync(t *testing.T) {
	ctx := context.Background()

	t.Run("the URL goes to every agent and the request only to the one that runs the console", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		r := healthyReport()
		r.RunsConsole = true
		if ans := e.firstSync(t, r); ans.ConsoleURL != "https://console.example.com" || ans.ConsoleAddress != nil {
			t.Errorf("answer before the switch = %q, %+v; want the URL and no request", ans.ConsoleURL, ans.ConsoleAddress)
		}
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		if ans := e.firstSync(t, healthyReport()); ans.ConsoleURL == "" || ans.ConsoleAddress != nil {
			t.Errorf("an agent that doesn't run the console: URL %q, request %+v; want the URL and no request", ans.ConsoleURL, ans.ConsoleAddress)
		}
		ans := e.firstSync(t, r)
		if ans.ConsoleAddress == nil || ans.ConsoleAddress.ID != c.ID || ans.ConsoleAddress.Domain != "new.example.com" {
			t.Fatalf("answer = %+v, want the request for new.example.com", ans.ConsoleAddress)
		}
		// While it is running the answer keeps carrying it: the agent doesn't apply an ID twice.
		if ans := e.firstSync(t, consoleReport(c.ID, "running", "")); ans.ConsoleAddress == nil {
			t.Error("the request left the answer while the agent was running it")
		}
	})

	t.Run("no URL when the public address isn't https", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.svc.SetPublicURL("http://127.0.0.1:7070")
		if ans := e.firstSync(t, healthyReport()); ans.ConsoleURL != "" {
			t.Errorf("ConsoleURL = %q, want none", ans.ConsoleURL)
		}
	})

	for _, state := range []string{"failed", "refused"} {
		t.Run("an outcome "+state+" ends the change and takes its domain out of the route", func(t *testing.T) {
			e := newConsoleEnv(t)
			c := e.ready(t)
			if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
			ans := e.firstSync(t, consoleReport(c.ID, state, "Caravana didn't answer at\x1b[31m https://new.example.com within 60 s;\n the old address is back "+strings.Repeat("x", 400)))
			if ans.ConsoleAddress != nil {
				t.Errorf("the request is still in the answer: %+v", ans.ConsoleAddress)
			}
			got, _ := e.store.ConsoleAddressChange(ctx, c.ID)
			if got.Status != state || got.SettledAt.IsZero() {
				t.Fatalf("change = %+v, want %s", got, state)
			}
			if len(got.Reason) > consoleReasonMax || strings.ContainsAny(got.Reason, "\x1b\n") || !strings.HasPrefix(got.Reason, "Caravana didn't answer at [31m https://new.example.com within 60 s; the old address is back") {
				t.Errorf("reason = %q, want one printable line of at most %d bytes", got.Reason, consoleReasonMax)
			}
			if v := e.nomad.Variable(deploy.ConsoleHostsPath); v != nil {
				t.Errorf("route variable = %v after a %s switch; the new domain should be gone", v, state)
			}
		})
	}

	t.Run("an outcome for another change is ignored", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		ans := e.firstSync(t, consoleReport(strings.Repeat("0", 32), "failed", "old news"))
		if got, _ := e.store.ConsoleAddressChange(ctx, c.ID); got.Status != store.ConsoleSwitching {
			t.Errorf("status = %q, want switching", got.Status)
		}
		if ans.ConsoleAddress == nil {
			t.Error("the request was dropped by another change's outcome")
		}
	})

	t.Run("applied when Caravana runs under the new address", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		e.svc.SetPublicURL("https://new.example.com") // the restart under the agent's new env file
		e.firstSync(t, consoleReport(c.ID, "applied", ""))
		got, _ := e.store.ConsoleAddressChange(ctx, c.ID)
		if got.Status != store.ConsoleApplied || got.Reason != "" {
			t.Fatalf("change = %+v, want applied", got)
		}
		if want := got.SettledAt.AddDate(0, 0, 30); !got.RedirectUntil.Equal(want) {
			t.Errorf("redirect until %v, want 30 days after %v", got.RedirectUntil, got.SettledAt)
		}
	})

	t.Run("applied while Caravana still runs under the old address is a failure", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		e.firstSync(t, consoleReport(c.ID, "applied", ""))
		got, _ := e.store.ConsoleAddressChange(ctx, c.ID)
		if got.Status != store.ConsoleFailed || got.Reason != "Caravana restarted, but its public URL isn't https://new.example.com." {
			t.Errorf("change = %+v", got)
		}
	})

	t.Run("a crafted report is refused whole", func(t *testing.T) {
		e := newConsoleEnv(t)
		for name, o := range map[string]*NodeConsoleOutcome{
			"an ID that isn't 32 hex digits": {ID: "abc", State: "failed"},
			"an ID in capitals":              {ID: strings.Repeat("A", 32), State: "failed"},
			"an unknown state":               {ID: strings.Repeat("a", 32), State: "done"},
		} {
			r := healthyReport()
			r.RunsConsole, r.ConsoleAddress = true, o
			if _, err := e.svc.NodeSync(ctx, e.firstToken, r); err == nil {
				t.Errorf("%s: the sync was accepted", name)
			}
		}
	})
}

func TestConsoleAddressPass(t *testing.T) {
	ctx := context.Background()
	clock := func(e *consoleEnv) *fakeClock { return e.svc.clock.(*fakeClock) }

	t.Run("a host that never used the feature is left alone", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.svc.consoleAddressPass(ctx)
		if paths := e.nomad.VariablePaths(); slices.Contains(paths, deploy.ConsoleHostsPath) {
			t.Errorf("variables = %v; the console's route must stay the job's own", paths)
		}
		if _, ok := e.svc.ConsoleRedirect("anything.example.com", "/"); ok {
			t.Error("a redirect with no old domain")
		}
	})

	t.Run("a change that was checking when the process ended fails", func(t *testing.T) {
		e := newConsoleEnv(t)
		now := clock(e).Now()
		if _, err := e.store.InsertConsoleAddressChange(ctx, store.ConsoleAddressChange{ID: strings.Repeat("a", 32), Domain: "new.example.com",
			OldDomain: "console.example.com", RedirectDays: 30, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		e.svc.consoleAddressPass(ctx)
		got, _ := e.store.ConsoleAddressChange(ctx, strings.Repeat("a", 32))
		if got.Status != store.ConsoleCheckFailed || got.Reason != "Caravana restarted during the check." {
			t.Errorf("change = %+v", got)
		}
		if v := e.nomad.Variable(deploy.ConsoleHostsPath); v != nil {
			t.Errorf("route variable = %v after the failure", v)
		}
	})

	t.Run("a change whose goroutine is going isn't failed by the pass", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.routeOK.Store(false)
		e.svc.consoleAddr.limit = time.Minute
		c := e.start(t, "new.example.com")
		e.svc.consoleAddressPass(ctx)
		if got, _ := e.store.ConsoleAddressChange(ctx, c.ID); got.Status != store.ConsoleChecking {
			t.Errorf("status = %q, want checking", got.Status)
		}
		if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("switching: applied when the address changed, lost when nothing came", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		e.svc.consoleAddressPass(ctx)
		if got, _ := e.store.ConsoleAddressChange(ctx, c.ID); got.Status != store.ConsoleSwitching {
			t.Fatalf("before the restart and before the limit: %q", got.Status)
		}
		// While switching, both domains are in the route.
		if e.hosts() != "console.example.com new.example.com" {
			t.Errorf("hosts while switching = %q", e.hosts())
		}

		clock(e).Set(clock(e).Now().Add(6 * time.Minute))
		e.svc.consoleAddressPass(ctx)
		got, _ := e.store.ConsoleAddressChange(ctx, c.ID)
		if got.Status != store.ConsoleLost || !strings.Contains(got.Reason, "journalctl -u caravana-node") {
			t.Errorf("after 6 minutes: %+v, want lost pointing at the agent's log", got)
		}
		if v := e.nomad.Variable(deploy.ConsoleHostsPath); v != nil {
			t.Errorf("route variable = %v for a lost change", v)
		}
	})

	t.Run("applied: the old domain redirects for its window and then leaves the route", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		e.svc.SetPublicURL("https://new.example.com") // Caravana restarted under the new address
		e.svc.consoleAddressPass(ctx)
		got, _ := e.store.ConsoleAddressChange(ctx, c.ID)
		if got.Status != store.ConsoleApplied || got.RedirectUntil.IsZero() {
			t.Fatalf("change = %+v, want applied with a window", got)
		}
		if e.hosts() != "new.example.com console.example.com" {
			t.Errorf("hosts = %q, want the new domain first and the old one still routed", e.hosts())
		}
		if loc, ok := e.svc.ConsoleRedirect("console.example.com", "/apps?x=1"); !ok || loc != "https://new.example.com/apps?x=1" {
			t.Errorf("ConsoleRedirect = %q, %v", loc, ok)
		}
		// The app check refuses the old domain while it is routed.
		if err := e.svc.checkConsoleDomain(domainApp("shop", app.Domain{Host: "console.example.com"})); err == nil {
			t.Error("an app may use an old console domain that still redirects")
		}

		clock(e).Set(got.RedirectUntil.Add(time.Second))
		e.svc.consoleAddressPass(ctx)
		if e.hosts() != "new.example.com" {
			t.Errorf("after the window hosts = %q, want only the new domain: the job's own domain is the old one", e.hosts())
		}
		if _, ok := e.svc.ConsoleRedirect("console.example.com", "/"); ok {
			t.Error("the old domain still redirects after its window")
		}
		if err := e.svc.checkConsoleDomain(domainApp("shop", app.Domain{Host: "console.example.com"})); err != nil {
			t.Errorf("an app may have the old domain once its window ended: %v", err)
		}
	})

	t.Run("0 days: the old domain leaves the route at once", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.agent(t)
		c, err := e.svc.StartConsoleAddressChange(ctx, "new.example.com", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		e.wait(t, c.ID, store.ConsoleReady)
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		e.svc.SetPublicURL("https://new.example.com")
		e.svc.consoleAddressPass(ctx)
		got, _ := e.store.ConsoleAddressChange(ctx, c.ID)
		if got.Status != store.ConsoleApplied || !got.RedirectUntil.IsZero() {
			t.Fatalf("change = %+v, want applied with no window", got)
		}
		if e.hosts() != "new.example.com" {
			t.Errorf("hosts = %q, want only the new domain", e.hosts())
		}
		if _, ok := e.svc.ConsoleRedirect("console.example.com", "/"); ok {
			t.Error("the old domain redirects with a window of 0 days")
		}
	})

	t.Run("a process that starts after an applied change writes the route again", func(t *testing.T) {
		e := newConsoleEnv(t)
		c := e.ready(t)
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		e.svc.SetPublicURL("https://new.example.com")
		e.svc.consoleAddressPass(ctx)
		e.nomad.SetVariable(deploy.ConsoleHostsPath, nil) // someone deleted it
		e.svc.consoleAddr.mu.Lock()
		e.svc.consoleAddr.written = nil // a new process knows nothing about what was written
		e.svc.consoleAddr.mu.Unlock()
		e.svc.consoleAddressPass(ctx)
		if e.hosts() != "new.example.com console.example.com" {
			t.Errorf("hosts = %q, want the route written again", e.hosts())
		}
	})
}

func TestConsoleRedirect(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	c := e.ready(t)
	if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.SetPublicURL("https://new.example.com:8443")
	e.svc.consoleAddressPass(ctx)

	tests := []struct {
		host, uri string
		want      string
		ok        bool
	}{
		{"console.example.com", "/apps", "https://new.example.com:8443/apps", true},
		{"console.example.com:443", "/apps", "https://new.example.com:8443/apps", true},
		{"CONSOLE.example.com.", "/logs?app=web", "https://new.example.com:8443/logs?app=web", true},
		{"console.example.com", "/", "https://new.example.com:8443/", true},
		{"console.example.com", "*", "https://new.example.com:8443/", true},
		{"console.example.com", "//evil.test/x", "https://new.example.com:8443//evil.test/x", true},
		{"new.example.com", "/apps", "", false}, // the current host
		{"other.example.com", "/apps", "", false},
		{"", "/apps", "", false},
		{"127.0.0.1:7070", "/apps", "", false},
	}
	for _, tt := range tests {
		got, ok := e.svc.ConsoleRedirect(tt.host, tt.uri)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ConsoleRedirect(%q, %q) = %q, %v; want %q, %v", tt.host, tt.uri, got, ok, tt.want, tt.ok)
		}
	}
}

func TestConsoleAddressView(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	e.agent(t)
	seedDomainApp(t, e.env, domainApp("shop"), true)
	if _, err := e.store.SetWebhookToken(ctx, "shop", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := e.store.InsertPasskey(ctx, store.PasskeyRecord{ID: "pk1", UserHandle: []byte("h"), CredentialID: []byte("c1"), RPID: "console.example.com",
		PublicKey: []byte("k"), Name: "mac", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.InsertPasskey(ctx, store.PasskeyRecord{ID: "pk2", UserHandle: []byte("h"), CredentialID: []byte("c2"), RPID: "old.example.org",
		PublicKey: []byte("k"), Name: "phone", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	v, err := e.svc.ConsoleAddress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.Domain != "console.example.com" || v.PublicURL != "https://console.example.com" || v.Issuer != "https://console.example.com" ||
		v.SSLIPHint != "203-0-113-1.sslip.io" || !v.CanChange || v.CannotChange != "" || !v.Agent || v.Change != nil || len(v.OldDomains) != 0 {
		t.Errorf("view = %+v", v)
	}
	if v.Dependents.Passkeys != 1 || v.Dependents.Webhooks != 1 || v.Dependents.Servers != 0 || v.Dependents.APITokens != 0 || v.Dependents.DeployTokens != 0 {
		t.Errorf("dependents = %+v, want the one passkey of this domain and the one webhook", v.Dependents)
	}

	c := e.start(t, "new.example.com")
	e.wait(t, c.ID, store.ConsoleReady)
	if v, _ = e.svc.ConsoleAddress(ctx); v.Change == nil || v.Change.ID != c.ID || v.Change.Status != store.ConsoleReady {
		t.Errorf("view.Change = %+v, want the open change", v.Change)
	}
	// After it ends the newest change is still shown.
	if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ = e.svc.ConsoleAddress(ctx); v.Change == nil || v.Change.Status != store.ConsoleCancelled {
		t.Errorf("view.Change = %+v, want the cancelled change", v.Change)
	}
}

func TestConsoleAddressWithoutAnyAgent(t *testing.T) {
	ctx := context.Background()
	// The lab: no cluster, no agent, and a Traefik job with no console route.
	e := newEnv(t)
	e.nomad.SetTraefikJob("traefik:v3.7.13")
	e.svc.SetPublicURL("https://console.example.com")
	v, err := e.svc.ConsoleAddress(ctx)
	if err != nil || v.CanChange || v.Agent || !strings.Contains(v.CannotChange, "run the installer again") {
		t.Errorf("view = %+v, %v", v, err)
	}
	// With no Traefik at all.
	e2 := newEnv(t)
	e2.svc.SetPublicURL("https://console.example.com")
	if v, _ := e2.svc.ConsoleAddress(ctx); v.CanChange || !strings.Contains(v.CannotChange, "Traefik isn't running") {
		t.Errorf("no Traefik: view = %+v", v)
	}
	// With Nomad down the view says it can't tell instead of failing.
	e2.nomad.Down()
	defer e2.nomad.Up()
	if v, err := e2.svc.ConsoleAddress(ctx); err != nil || v.CanChange || !strings.Contains(v.CannotChange, "couldn't read Traefik's job") {
		t.Errorf("Nomad down: view = %+v, %v", v, err)
	}
}

// An app can't use a console domain that is pending or old (spec 051, D12); the current one already
// was refused.
func TestAppsRefuseConsoleDomains(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	e.routeOK.Store(false)
	e.svc.consoleAddr.limit = time.Minute
	c := e.start(t, "new.example.com")

	for _, host := range []string{"new.example.com"} {
		a := domainApp("shop", app.Domain{Host: host})
		err := e.svc.checkConsoleDomain(a)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].domains[0].host" || !strings.Contains(fe.Message, "is a console address") {
			t.Errorf("%s: err = %v, want a console-address field error", host, err)
		}
		pair := domainApp("shop", app.Domain{Host: "www.new.example.com", WWWRedirect: true})
		if err := e.svc.checkConsoleDomain(pair); err == nil {
			t.Errorf("an app redirecting %s was accepted", host)
		}
	}
	if err := e.svc.checkConsoleDomain(domainApp("shop", app.Domain{Host: "other.example.com"})); err != nil {
		t.Errorf("another domain: %v", err)
	}
	// Through a real deploy too.
	if _, err := e.svc.Deploy(ctx, domainApp("shop2", app.Domain{Host: "new.example.com"}), app.TriggerManual); err == nil {
		t.Error("a deploy with the pending console domain was accepted")
	}
	if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.checkConsoleDomain(domainApp("shop", app.Domain{Host: "new.example.com"})); err != nil {
		t.Errorf("after the cancel the domain is free again: %v", err)
	}
}
