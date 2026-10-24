package ui

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 023: /domains and the app's Domains page, with the states the stored checks give.

// domainsApp is one service on port 80 with the given domains.
func domainsApp(id string, hosts ...string) app.App {
	a := whoamiApp()
	a.ID = id
	a.Services[0].Domains = nil
	for _, h := range hosts {
		a.Services[0].Domains = append(a.Services[0].Domains, app.Domain{Host: h, Port: a.Services[0].Ports[0].Name})
	}
	return a
}

// checkSeed is a stored check of a host, as the watcher would write it.
type checkSeed struct {
	host, app       string
	dns             string
	seen, expected  []string
	cert, issuer    string
	notAfterFromNow time.Duration // 0: none
}

func (e *consoleEnv) seedCheck(t *testing.T, c checkSeed) {
	t.Helper()
	now := e.Clock.Now().UTC()
	rec := store.DomainCheckRecord{Host: c.host, AppID: c.app, Service: "web", State: string(edge.StateUnknown),
		DNSStatus: c.dns, DNSSeen: c.seen, DNSExpected: c.expected, CertStatus: c.cert, CertIssuer: c.issuer,
		CheckedAt: now.Add(-40 * time.Second)}
	if c.notAfterFromNow != 0 {
		rec.CertNotAfter = new(now.Add(c.notAfterFromNow))
	}
	if err := e.Store.UpsertDomainCheck(context.Background(), rec); err != nil {
		t.Fatalf("UpsertDomainCheck(%s): %v", c.host, err)
	}
}

const day = 24 * time.Hour

// statesEnv has one app per kind of row: shop (healthy, with a domain in every checked
// state and one never checked), idle (never deployed) and paused (stopped).
func statesEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	healthy := seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1}
	e.seed(t, domainsApp("shop", "live.example.com", "elsewhere.example.com", "expiring.example.com", "pending.example.com",
		"none.example.com", "self.example.com", "unknown.example.com"), healthy)
	e.seed(t, domainsApp("idle", "idle.example.com"))
	e.seed(t, domainsApp("paused", "paused.example.com"), seedDeploy{ID: "p1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	if err := e.Store.SetAppStopped(context.Background(), "paused", new(e.Clock.Now())); err != nil {
		t.Fatal(err)
	}
	here := []string{"203.0.113.1"}
	for _, c := range []checkSeed{
		{host: "live.example.com", dns: edge.DNSOK, seen: here, expected: here, cert: edge.CertIssued, issuer: "Let's Encrypt", notAfterFromNow: 91*day + time.Hour},
		{host: "elsewhere.example.com", dns: edge.DNSElsewhere, seen: []string{"185.199.108.153"}, expected: here, cert: edge.CertError},
		{host: "expiring.example.com", dns: edge.DNSOK, seen: here, expected: here, cert: edge.CertExpiring, issuer: "Let's Encrypt", notAfterFromNow: 6*day + time.Hour},
		{host: "pending.example.com", dns: edge.DNSOK, seen: here, expected: here, cert: edge.CertPending, issuer: edge.TraefikOwn},
		{host: "none.example.com", dns: edge.DNSNone, expected: here, cert: edge.CertError},
		{host: "self.example.com", dns: edge.DNSOK, seen: here, expected: here, cert: edge.CertSelf, issuer: edge.TraefikOwn},
	} {
		c.app = "shop"
		e.seedCheck(t, c)
	}
	return e
}

// dmRowOf is the <details> of a host on a page, or "" when there is none.
func dmRowOf(body, host string) string {
	m := regexp.MustCompile(`(?s)<details class="cv-dmdetails" id="domain-` + regexp.QuoteMeta(host) + `"[^>]*>.*?</details>\s*(?:<details class="cv-dmdetails"|</div>)`).FindString(body)
	return m
}

func TestDomainsListStates(t *testing.T) {
	e := statesEnv(t)
	body := e.pageOf(t, "/domains")
	got := text(body)
	for _, want := range []string{"Domains", "Every address people use to reach your apps. Certificates are issued and renewed for you.",
		"9 domains · 3 need you · 1 getting a certificate", "Checks run every minute. Certificates come from Let's Encrypt and renew by themselves.",
		`href="#add"`} {
		if !strings.Contains(got, want) && !strings.Contains(body, want) {
			t.Errorf("/domains lacks %q:\n%s", want, got)
		}
	}
	// The sidebar's Domains is current; the live stream watches the domains.
	if !strings.Contains(body, `<a href="/domains" class="cv-nav-item" aria-current="page">`) || !strings.Contains(body, `/live?watch=domains`) {
		t.Error("the sidebar's Domains isn't current, or the page doesn't watch the domains")
	}

	// Problems first (failed, warning, rolling), then live, then idle, by host within.
	order := []string{"elsewhere", "expiring", "none", "pending", "live", "idle", "paused", "self", "unknown"}
	last := -1
	for _, h := range order {
		at := strings.Index(body, `id="domain-`+h+`.example.com"`)
		if at < 0 || at < last {
			t.Fatalf("rows aren't in the order %v: %s at %d after %d", order, h, at, last)
		}
		last = at
	}

	rows := map[string][]string{
		"elsewhere": {"DNS points elsewhere", "shop :80 · production", "—", "The domain points at 185.199.108.153, which isn't this host. Until it changes, caravana can't get a certificate and visitors won't reach shop.",
			"now A · 185.199.108.153", "should be A · 203.0.113.1", "last check 40 s ago · checks every minute", "Check again", "Remove…"},
		"expiring": {"certificate expiring", "expires in 6 d", "The certificate expires in 6 days and hasn't been renewed. Let's Encrypt renews 30 days before expiry, so renewal has been failing", "Check again"},
		"none":     {"no DNS record", "No DNS record yet. Add this record where you manage DNS:", "Type A Name none Value this host's public IP address"},
		"pending":  {"getting certificate", "Waiting for Let's Encrypt. DNS points here; the certificate usually arrives within a minute of the first visit."},
		"live":     {"live · HTTPS", "renews in 61 d", "certificate Let's Encrypt · renews in 61 d · expires", "DNS A → 203.0.113.1", "router shop-web", "checked 40 s ago"},
		"self":     {"Traefik's own certificate", "Traefik's own", "Traefik serves this domain with its own certificate: browsers will warn."},
		"unknown":  {"not checked yet", "Not checked yet. Checks run every minute while this page is open."},
		"idle":     {"not deployed", "idle :80 · production", "Nothing is deployed yet.", "Deploy idle"},
		"paused":   {"app stopped", "The app is stopped: start it to serve this domain.", "Open paused", "The app is stopped: start it first."},
	}
	for h, wants := range rows {
		row := dmRowOf(body, h+".example.com")
		if row == "" {
			t.Errorf("no row for %s", h)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(text(row), want) {
				t.Errorf("%s's row lacks %q:\n%s", h, want, text(row))
			}
		}
	}
	// The warn hue for an expiring certificate; the expiry as a clock time in the live detail.
	if !strings.Contains(dmRowOf(body, "expiring.example.com"), `cv-dmcert cv-dmcert-warn`) {
		t.Error("an expiring certificate isn't in the warn hue")
	}
	if !strings.Contains(dmRowOf(body, "live.example.com"), `<time class="cv-clock"`) {
		t.Error("the live row's expiry isn't a clock time")
	}
	// The first problem row opens by itself, and only it.
	if strings.Count(body, `class="cv-dmdetails" id="domain-`) != 9 || strings.Count(body, `" open>`) != 1 || !strings.Contains(body, `id="domain-elsewhere.example.com" open>`) {
		t.Error("the first problem row doesn't open alone")
	}
	// ?open= opens another.
	opened := e.pageOf(t, "/domains?open=self.example.com")
	if !strings.Contains(opened, `id="domain-self.example.com" open>`) || strings.Contains(opened, `id="domain-elsewhere.example.com" open>`) {
		t.Error("?open= doesn't open the row asked for, alone")
	}

	// Banners: one per DNS-elsewhere and expiring row, with Check again.
	banners := regexp.MustCompile(`(?s)<div class="cv-panel cv-dmbanner">.*?</form>`).FindAllString(body, -1)
	if len(banners) != 2 {
		t.Fatalf("banners = %d, want 2", len(banners))
	}
	if b := text(banners[0]); !strings.Contains(b, "elsewhere.example.com points at 185.199.108.153, which isn't this host.") || !strings.Contains(b, "Check again") {
		t.Errorf("the DNS banner = %q", b)
	}
	if b := text(banners[1]); !strings.Contains(b, "expiring.example.com expires in 6 days and hasn't been renewed.") {
		t.Errorf("the certificate banner = %q", b)
	}
	if !strings.Contains(banners[0], `action="/domains/elsewhere.example.com/check"`) {
		t.Error("the banner's Check again doesn't post to the host's check")
	}
}

// At most three banners, then "and N more" linking to the first row without one.
func TestDomainsBannersCap(t *testing.T) {
	e := newConsoleEnv(t)
	hosts := []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com"}
	e.seed(t, domainsApp("many", hosts...), seedDeploy{ID: "m1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	for _, h := range hosts {
		e.seedCheck(t, checkSeed{host: h, app: "many", dns: edge.DNSElsewhere, seen: []string{"198.51.100.4"}, expected: []string{"203.0.113.1"}, cert: edge.CertError})
	}
	body := e.pageOf(t, "/domains")
	if n := strings.Count(body, `class="cv-panel cv-dmbanner"`); n != 3 {
		t.Errorf("banners = %d, want 3", n)
	}
	if !strings.Contains(body, `<a href="#domain-d.example.com">and 2 more</a>`) {
		t.Errorf("no \"and 2 more\":\n%s", text(body))
	}
	if !strings.Contains(text(body), "5 domains · 5 need you") {
		t.Errorf("the count line:\n%s", text(body))
	}
}

// The filter: by app, by project, and a filter that names nothing.
func TestDomainsFilter(t *testing.T) {
	e := statesEnv(t)
	body := e.pageOf(t, "/domains?app=idle")
	if got := text(body); !strings.Contains(got, "1 domain") || strings.Contains(body, `id="domain-live.example.com"`) || !strings.Contains(body, `<option value="idle" selected>`) {
		t.Errorf("?app=idle:\n%s", got)
	}
	if !strings.Contains(body, `hx-get="/parts/domains?app=idle"`) {
		t.Error("the region's refresh doesn't keep the filter")
	}
	// A project select only with more than one project.
	if strings.Contains(body, `name="project"`) {
		t.Error("a project select with one project")
	}
	ctx := context.Background()
	if _, err := e.Svc.CreateProject(ctx, "shopfront", ""); err != nil {
		t.Fatal(err)
	}
	body = e.pageOf(t, "/domains?project=shopfront")
	if !strings.Contains(body, `name="project"`) || !strings.Contains(text(body), "No domains yet.") && !strings.Contains(text(body), "No domain matches.") {
		t.Errorf("?project=shopfront:\n%s", text(body))
	}
	for _, path := range []string{"/domains?app=nope", "/domains?project=nope"} {
		if got := text(e.pageOf(t, path)); !strings.Contains(got, "No domain matches.") || !strings.Contains(got, "0 domains") {
			t.Errorf("%s:\n%s", path, got)
		}
	}
	// The part draws the region alone, with the filter.
	resp, part := e.get(t, "/parts/domains?app=idle")
	if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") || !strings.Contains(part, `id="domain-idle.example.com"`) || strings.Contains(part, "live.example.com") {
		t.Errorf("/parts/domains?app=idle = %d:\n%s", resp.StatusCode, part)
	}
}

// The empty state.
func TestDomainsEmpty(t *testing.T) {
	e := newConsoleEnv(t)
	got := text(e.pageOf(t, "/domains"))
	if !strings.Contains(got, "No domains yet. Give an app a domain and HTTPS is set up for it.") || !strings.Contains(got, "0 domains") {
		t.Errorf("the empty /domains:\n%s", got)
	}
}

// An app's Domains page draws the same rows and detail, filtered to the app, and links All domains.
func TestAppDomainsPageRows(t *testing.T) {
	e := statesEnv(t)
	body := e.pageOf(t, "/apps/shop/domains")
	got := text(body)
	for _, want := range []string{"All domains", "7 domains · 3 need you · 1 getting a certificate", "The domain points at 185.199.108.153"} {
		if !strings.Contains(got, want) {
			t.Errorf("the app's page lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(body, "idle.example.com") || !strings.Contains(body, `<a href="/domains">All domains</a>`) {
		t.Error("the app's page isn't filtered to the app, or has no All domains link")
	}
	if !strings.Contains(body, `/live/apps/shop?tab=domains`) || !strings.Contains(body, `hx-get="/parts/domains?app=shop&amp;page=app"`) {
		t.Error("the app's page doesn't watch the domains, or its region doesn't refresh as the app's")
	}
	// Check again on the app's page comes back to it.
	row := dmRowOf(body, "elsewhere.example.com")
	if !strings.Contains(row, `<input type="hidden" name="app" value="shop">`) {
		t.Error("the app page's Check again doesn't say where to come back")
	}
	// The app's part keeps its own empty state.
	bare := domainsApp("bare")
	e.seed(t, bare, seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	if _, part := e.get(t, "/parts/domains?app=bare&page=app"); !strings.Contains(text(part), "No domains. Copies are reachable only inside the app.") {
		t.Errorf("the app's part, empty:\n%s", text(part))
	}

	// With several services, Goes to names the service after the app.
	d := duetEnv(t)
	if got := text(dmRowOf(d.pageOf(t, "/domains"), "api.duet.example.com")); !strings.Contains(got, "duet › api :8080 · production") {
		t.Errorf("a service of several:\n%s", got)
	}
}

// Check again runs a fresh check and comes back to the row, open.
func TestDomainCheckAgain(t *testing.T) {
	e := statesEnv(t)
	dialer := &edgetest.FakeDialer{Default: []*x509.Certificate{edgetest.TraefikDefault(t)}}
	e.Svc.SetPublicURL("https://console.example.com")
	e.Svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{"console.example.com": {"203.0.113.1"}, "elsewhere.example.com": {"203.0.113.1"}}}, dialer)

	resp, body := e.postForm(t, "/domains/elsewhere.example.com/check", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/domains?open=elsewhere.example.com#domain-elsewhere.example.com" {
		t.Fatalf("check = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	if len(dialer.Dialed()) == 0 {
		t.Error("Check again didn't dial the edge")
	}
	page := e.pageOf(t, "/domains?open=elsewhere.example.com")
	if row := text(dmRowOf(page, "elsewhere.example.com")); !strings.Contains(row, "Traefik's own certificate") {
		t.Errorf("after the check the row is:\n%s", row)
	}
	resp, _ = e.postForm(t, "/domains/elsewhere.example.com/check", url.Values{"app": {"shop"}})
	if location(t, resp) != "/apps/shop/domains?open=elsewhere.example.com#domain-elsewhere.example.com" {
		t.Errorf("from the app page = %q", location(t, resp))
	}
	if resp, _ = e.postForm(t, "/domains/Not%20A%20Host/check", url.Values{}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a check of no host = %d, want 422", resp.StatusCode)
	}
}

// A domains page's stream marks the domains watched while it is open, and forwards the domains notice.
func TestDomainsStreamWatches(t *testing.T) {
	e := statesEnv(t)
	var open, done atomic.Int32
	e.Server.live.watchDomains = func() func() {
		open.Add(1)
		return func() { done.Add(1) }
	}
	for _, path := range []string{"/live?watch=domains", "/live/apps/shop?tab=domains"} {
		open.Store(0)
		done.Store(0)
		ls := openStream(t, e.URL, path, e.Cookie.Value)
		ls.expect("sync")
		if open.Load() != 1 {
			t.Fatalf("%s: watched %d times, want 1", path, open.Load())
		}
		// A check that changes a row's state is a domains notice.
		e.Svc.SetEdge(nil, &edgetest.FakeDialer{Default: []*x509.Certificate{edgetest.TraefikDefault(t)}})
		if _, err := e.Svc.CheckDomain(context.Background(), "unknown.example.com", ""); err != nil {
			t.Fatal(err)
		}
		ls.expect("domains")
		ls.close()
		waitFor(t, path+" done", func() bool { return done.Load() == 1 })
		if err := e.Store.DeleteDomainCheck(context.Background(), "unknown.example.com"); err != nil {
			t.Fatal(err)
		}
	}
	// The other streams don't watch.
	open.Store(0)
	ls := openStream(t, e.URL, "/live", e.Cookie.Value)
	ls.expect("sync")
	ls.close()
	if open.Load() != 0 {
		t.Error("/live without ?watch=domains marked the domains watched")
	}
}

// No page or part holds a Team-only domain's hash, nor anything shaped like one; the row shows a lock.
func TestDomainsHoldNoSecret(t *testing.T) {
	e := newConsoleEnv(t)
	a := domainsApp("vault", "admin.example.com")
	a.Services[0].Domains[0].TeamOnly = true
	e.seed(t, a, seedDeploy{ID: "v1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	const sentinel = "$2a$10$s3cret-sentinel-023-hash-0000000000000000000000000"
	if err := e.Store.SetTeamPassword(context.Background(), "admin.example.com", "vault", sentinel, e.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	hashLike := regexp.MustCompile(`\$2[aby]\$|s3cret-sentinel`)
	for _, path := range []string{"/domains", "/parts/domains", "/apps/vault/domains", "/parts/domains?app=vault&page=app", "/domains?open=admin.example.com"} {
		_, body := e.get(t, path)
		if m := hashLike.FindString(body); m != "" {
			t.Errorf("%s holds %q", path, m)
		}
	}
	row := dmRowOf(e.pageOf(t, "/domains"), "admin.example.com")
	if !strings.Contains(row, `class="cv-dmlock" title="Team only"`) {
		t.Errorf("a Team-only row has no lock:\n%s", row)
	}
	// Checked and live: the router fact names the domain's own router, still no hash.
	here := []string{"203.0.113.1"}
	e.seedCheck(t, checkSeed{host: "admin.example.com", app: "vault", dns: edge.DNSOK, seen: here, expected: here,
		cert: edge.CertIssued, issuer: "Let's Encrypt", notAfterFromNow: 80 * day})
	body := e.pageOf(t, "/domains")
	if got := text(dmRowOf(body, "admin.example.com")); !strings.Contains(got, "router vault-web-") || !strings.Contains(got, "(its own, for its options)") {
		t.Errorf("a domain with options names its own router:\n%s", got)
	}
	if m := hashLike.FindString(body); m != "" {
		t.Errorf("/domains holds %q", m)
	}
}
