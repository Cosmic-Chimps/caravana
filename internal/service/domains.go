package service

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Domains (feature 023): every address the apps answer on, with what
// Caravana's own checks last found about it. The list is built from the stored
// apps (a plain read of SQLite, so it draws when Nomad is down), each app's
// stored state (stopped, or with a healthy deployment) and the cache of checks
// in domain_checks, which the watcher (domainwatch.go) and CheckDomain write.
// The checks themselves are package edge's: the host's DNS against this host's
// own addresses, and the certificate Traefik's edge presents for it.

// ErrDomainNotFound means no stored app has the host as a domain, nor
// redirects it to one of its domains.
var ErrDomainNotFound = errors.New("no app has this domain")

const (
	// defaultEdgeAddress is where Traefik's HTTPS entrypoint answers on an
	// installed host (CARAVANA_EDGE_ADDRESS).
	defaultEdgeAddress = "127.0.0.1:443"
	// hostLookupTimeout bounds the lookup of this host's own addresses.
	hostLookupTimeout = 2 * time.Second
	// hostLookupRetry is how long a failed lookup of this host's addresses is
	// kept, so a name that doesn't resolve doesn't make every check wait.
	hostLookupRetry = time.Minute
	// certRenewBefore is how long before NotAfter Traefik renews a
	// certificate, as package edge counts it for a fresh check: RenewsAt of a
	// cached one is derived with it.
	certRenewBefore = 30 * 24 * time.Hour
)

// DomainFilter narrows Domains. Empty fields mean "any".
type DomainFilter struct {
	App     string // an app ID
	Project string // a project name
}

// DomainView is one row of the Domains page and one Domain of the API: a
// domain of a stored app (or the redirecting pair of one with WWWRedirect),
// where it goes, and its state from the last check.
type DomainView struct {
	Host string
	// App is the app's ID; Project, Environment and AppName are its place.
	App                           string
	Project, Environment, AppName string
	Service                       string
	// Port is the name of the service's port the domain goes to, as the app
	// definition has it; ContainerPort is that port's number (0 when the
	// service has no port of that name).
	Port          string
	ContainerPort int
	Path          string
	WWWRedirect   bool
	TeamOnly      bool
	// Router is the Traefik router that serves the host: "<app>-<service>",
	// or the domain's own router when it has options.
	Router string
	// RedirectOf is set on a redirect row: the host this one redirects to.
	RedirectOf string
	State      edge.State
	DNS        edge.DNS
	// Certificate is what the last check found; Status is edge.CertNone when
	// the host wasn't probed (its app isn't served, or it was never checked).
	Certificate edge.Certificate
	// CheckedAt is when the facts were measured; nil when the host has never
	// been checked, and then State is unknown (or not_served).
	CheckedAt *time.Time
	// Stopped says the app is stopped; Deployed that it has a healthy
	// deployment. Together they word a not_served row ("app stopped", "not
	// deployed").
	Stopped  bool
	Deployed bool
	// Taken lists the other apps whose definitions also have this host, by ID:
	// two apps claim the address and Traefik picks one of them.
	Taken []string
}

// DNSRecord is a record the owner creates where they manage a domain's DNS.
type DNSRecord struct {
	Type  string // "A" or "AAAA"
	Name  string // "@" for an apex, else the first label
	Value string // this host's address
}

// DomainCheck is the answer of a fresh check of one host.
type DomainCheck struct {
	Host string
	// Records are the records that point the host here: one per address of
	// this host. Empty when this host's addresses aren't known.
	Records     []DNSRecord
	DNS         edge.DNS
	Certificate edge.Certificate
	CheckedAt   time.Time
}

// domainSettings are the edge checks' seams and settings, guarded by the
// service's domainMu.
type domainSettings struct {
	resolver    edge.Resolver
	dialer      edge.Dialer
	edgeAddress string
	// certResolver is Traefik's certificate resolver ("" in the lab): with
	// one, Traefik's own certificate means "pending", without, "self".
	certResolver string
	acmeMode     string // CARAVANA_ACME: production, staging, none, or "" when not said
	acmeEmail    string
}

// SetEdge sets the resolver the domain checks ask and the dialer they
// handshake with (edge.SystemAndPublicResolvers and edge.TLSDialer by
// default). The lab and tests give fakes. A nil argument keeps that one. The
// resolver also finds this host's own addresses, so their cache is dropped.
func (s *Service) SetEdge(resolver edge.Resolver, dialer edge.Dialer) {
	s.domainMu.Lock()
	if resolver != nil {
		s.edgeCfg.resolver = resolver
	}
	if dialer != nil {
		s.edgeCfg.dialer = dialer
	}
	s.domainMu.Unlock()
	s.hostAddrs.reset()
}

// SetEdgeAddress sets where Traefik's HTTPS entrypoint answers, host:port
// (CARAVANA_EDGE_ADDRESS; 127.0.0.1:443 by default, the lab's 127.0.0.1:8443).
// Empty keeps the default.
func (s *Service) SetEdgeAddress(addr string) {
	if addr == "" {
		return
	}
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	s.edgeCfg.edgeAddress = addr
}

// SetCertResolver says which certificate resolver Traefik's routers name
// (CARAVANA_CERT_RESOLVER, as the translator has it). With one, a host that
// gets Traefik's own certificate is waiting for its certificate; without
// (the lab), that certificate is the one it will keep.
func (s *Service) SetCertResolver(name string) {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	s.edgeCfg.certResolver = name
}

// SetACME records the certificate authority the installer chose
// (CARAVANA_ACME: production, staging, none, or "" when not said) and the
// contact email given to it (CARAVANA_ACME_EMAIL). Both are read-only facts
// for Settings › Certificates.
func (s *Service) SetACME(mode, email string) {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	s.edgeCfg.acmeMode, s.edgeCfg.acmeEmail = mode, email
}

// ACME returns what SetACME was given.
func (s *Service) ACME() (mode, email string) {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	return s.edgeCfg.acmeMode, s.edgeCfg.acmeEmail
}

// edgeSettings returns a copy of the checks' settings, defaults filled in.
func (s *Service) edgeSettings() domainSettings {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	es := s.edgeCfg
	if es.resolver == nil {
		es.resolver = edge.SystemAndPublicResolvers()
	}
	if es.dialer == nil {
		es.dialer = edge.TLSDialer{}
	}
	if es.edgeAddress == "" {
		es.edgeAddress = defaultEdgeAddress
	}
	return es
}

// ---- this host's own addresses -------------------------------------------------

// hostAddressCache keeps this host's addresses: what the public URL's host
// resolves to. A lookup that found addresses is kept for the life of the
// process (or until the URL or the resolver changes); one that found none is
// kept hostLookupRetry, so a name that doesn't resolve doesn't make every
// check wait. The zero value is ready to use.
type hostAddressCache struct {
	mu     sync.Mutex
	url    string // the public URL addrs were found for
	addrs  []netip.Addr
	failed time.Time // when the last lookup for url found nothing
}

func (c *hostAddressCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.url, c.addrs, c.failed = "", nil, time.Time{}
}

// HostAddresses returns this host's public addresses, IPv4 and IPv6: the host
// of the console's address (CARAVANA_PUBLIC_URL) resolved through the checks'
// resolver, two seconds at most, loopback and unspecified addresses left out
// (they are the console running on a laptop, not an address a domain can
// point at). Without a public URL, or when its host doesn't resolve, the list
// is empty: the DNS checks then say "unknown", except for a name that resolves
// to loopback (the lab's *.localhost), which is "ok".
func (s *Service) HostAddresses(ctx context.Context) []netip.Addr {
	return s.hostAddresses(ctx)
}

func (s *Service) hostAddresses(ctx context.Context) []netip.Addr {
	publicURL := s.publicURL
	c := &s.hostAddrs
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.url != publicURL {
		c.url, c.addrs, c.failed = publicURL, nil, time.Time{}
	}
	now := s.clock.Now()
	switch {
	case len(c.addrs) > 0:
		return slices.Clone(c.addrs)
	case publicURL == "":
		return nil
	case !c.failed.IsZero() && now.Sub(c.failed) < hostLookupRetry:
		return nil
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		c.failed = now
		return nil
	}
	c.addrs = s.lookupHost(ctx, u.Hostname())
	if len(c.addrs) == 0 {
		c.failed = now
		return nil
	}
	return slices.Clone(c.addrs)
}

// lookupHost resolves name to its usable addresses, sorted, without repeats.
// An IP literal is its own address.
func (s *Service) lookupHost(ctx context.Context, name string) []netip.Addr {
	var found []netip.Addr
	if a, err := netip.ParseAddr(name); err == nil {
		found = []netip.Addr{a}
	} else {
		ctx, cancel := context.WithTimeout(ctx, hostLookupTimeout)
		defer cancel()
		ips, _ := s.edgeSettings().resolver.LookupIPAddr(ctx, name)
		for _, ip := range ips {
			if a, ok := netip.AddrFromSlice(ip.IP); ok {
				found = append(found, a)
			}
		}
	}
	var out []netip.Addr
	for _, a := range found {
		a = a.Unmap().WithZone("")
		if a.IsLoopback() || a.IsUnspecified() {
			continue
		}
		out = append(out, a)
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out)
}

// DNSRecordName is the Name of the record that points host here: "@" for an
// apex, the first label otherwise. An apex is a host with exactly one dot
// (app.IsApex): telling the registered domain apart in general
// (example.co.uk, example.com.br) needs the public suffix list, which
// Caravana doesn't carry, so example.co.uk reads as a subdomain "example". An
// empty host has no name.
func DNSRecordName(host string) string {
	host = normalizeHost(host)
	switch {
	case host == "":
		return ""
	case app.IsApex(host):
		return "@"
	}
	label, _, _ := strings.Cut(host, ".")
	return label
}

// recordsFor are the records that point host at addrs: an A per IPv4 and an
// AAAA per IPv6 address.
func recordsFor(host string, addrs []netip.Addr) []DNSRecord {
	name := DNSRecordName(host)
	var out []DNSRecord
	for _, a := range addrs {
		typ := "A"
		if a.Is6() {
			typ = "AAAA"
		}
		out = append(out, DNSRecord{Type: typ, Name: name, Value: a.String()})
	}
	return out
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// ---- the list ------------------------------------------------------------------

// domainTarget is one host a stored app answers on: a domain of a service, or
// the redirecting pair of a domain with WWWRedirect.
type domainTarget struct {
	host    string
	rec     *store.AppRecord
	service app.Service
	domain  app.Domain // for a redirect row, the domain it redirects to
	// redirectOf is the host a redirect row redirects to; "" for a domain.
	redirectOf string
	stopped    bool
	deployed   bool
	taken      []string
}

// served says Traefik should be serving the host: the app isn't stopped and
// has a healthy deployment.
func (t domainTarget) served() bool { return !t.stopped && t.deployed }

// domainTargets lists every host the stored apps that pass f answer on, in
// app ID order, then the services' and domains' own order, each redirecting
// pair right after its domain. A pair that is itself a domain of some app is
// that app's row, not a redirect. Errors: ErrProjectNotFound for a project
// that doesn't exist, store.ErrNotFound for an app that doesn't.
func (s *Service) domainTargets(ctx context.Context, f DomainFilter) ([]domainTarget, error) {
	if f.Project != "" {
		if err := s.requireProject(ctx, f.Project); err != nil {
			return nil, err
		}
	}
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	if f.App != "" && !slices.ContainsFunc(recs, func(r store.AppRecord) bool { return r.App.ID == f.App }) {
		return nil, fmt.Errorf("app %q: %w", f.App, store.ErrNotFound)
	}

	// Who claims each host, across every app (not just the filtered ones).
	claims := map[string][]string{}
	for _, rec := range recs {
		for _, svc := range rec.App.Services {
			for _, d := range svc.Domains {
				if !slices.Contains(claims[d.Host], rec.App.ID) {
					claims[d.Host] = append(claims[d.Host], rec.App.ID)
				}
			}
		}
	}
	others := func(host, appID string) []string {
		var out []string
		for _, id := range claims[host] {
			if id != appID {
				out = append(out, id)
			}
		}
		return out
	}

	var out []domainTarget
	for i := range recs {
		rec := &recs[i]
		if (f.App != "" && rec.App.ID != f.App) || (f.Project != "" && rec.Place.Project != f.Project) {
			continue
		}
		if !slices.ContainsFunc(rec.App.Services, func(svc app.Service) bool { return len(svc.Domains) > 0 }) {
			continue
		}
		deployed, err := s.hasHealthyDeployment(ctx, rec.App.ID)
		if err != nil {
			return nil, fmt.Errorf("list domains: %w", err)
		}
		for _, svc := range rec.App.Services {
			for _, d := range svc.Domains {
				t := domainTarget{host: d.Host, rec: rec, service: svc, domain: d,
					stopped: rec.StoppedAt != nil, deployed: deployed, taken: others(d.Host, rec.App.ID)}
				out = append(out, t)
				if pair := d.RedirectPair(); d.WWWRedirect && pair != "" && len(claims[pair]) == 0 {
					t.host, t.redirectOf, t.taken = pair, d.Host, nil
					out = append(out, t)
				}
			}
		}
	}
	return out, nil
}

// hasHealthyDeployment says whether an app has a healthy deployment: a version
// Nomad runs for it.
func (s *Service) hasHealthyDeployment(ctx context.Context, appID string) (bool, error) {
	_, err := s.store.LatestHealthyDeployment(ctx, appID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// Domains returns every domain of the stored apps that pass f, with the
// redirecting pair of each WWWRedirect domain as a row of its own, and the
// state the last check gives each. A host never checked is unknown; a host
// whose app is stopped or has no healthy deployment is not_served, whatever
// was last found. Rows come problems first (failed, then warning, then
// rolling), then live, then the idle ones, by host within each. It reads only
// SQLite; nothing is checked here.
//
// Errors: ErrProjectNotFound for a project that doesn't exist, store.ErrNotFound
// for an app that doesn't.
func (s *Service) Domains(ctx context.Context, f DomainFilter) ([]DomainView, error) {
	targets, err := s.domainTargets(ctx, f)
	if err != nil {
		return nil, err
	}
	checks, err := s.store.ListDomainChecks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	byHost := make(map[string]store.DomainCheckRecord, len(checks))
	for _, c := range checks {
		byHost[c.Host] = c
	}

	out := make([]DomainView, 0, len(targets))
	for _, t := range targets {
		var cached *store.DomainCheckRecord
		if c, ok := byHost[t.host]; ok {
			cached = &c
		}
		out = append(out, viewOf(t, cached))
	}
	sortDomains(out)
	return out, nil
}

// Domain returns one host's row, as Domains has it, or ErrDomainNotFound.
func (s *Service) Domain(ctx context.Context, host string) (DomainView, error) {
	host = normalizeHost(host)
	all, err := s.Domains(ctx, DomainFilter{})
	if err != nil {
		return DomainView{}, err
	}
	for _, v := range all {
		if v.Host == host {
			return v, nil
		}
	}
	return DomainView{}, fmt.Errorf("domain %q: %w", host, ErrDomainNotFound)
}

// viewOf is a target's row with its cached check (nil when there is none).
func viewOf(t domainTarget, cached *store.DomainCheckRecord) DomainView {
	a := t.rec.App
	v := DomainView{
		Host: t.host, App: a.ID,
		Project: t.rec.Place.Project, Environment: t.rec.Place.Environment, AppName: t.rec.Place.Name,
		Service: t.service.Name, Port: t.domain.Port,
		Path: t.domain.Path, WWWRedirect: t.domain.WWWRedirect, TeamOnly: t.domain.TeamOnly,
		Router:      routerOf(a.ID, t.service.Name, t.domain),
		RedirectOf:  t.redirectOf,
		State:       edge.StateUnknown,
		DNS:         edge.DNS{Status: edge.DNSUnknown},
		Certificate: edge.Certificate{Status: edge.CertNone},
		Stopped:     t.stopped, Deployed: t.deployed,
		Taken: t.taken,
	}
	for _, p := range t.service.Ports {
		if p.Name == t.domain.Port {
			v.ContainerPort = p.ContainerPort
		}
	}
	if cached != nil {
		f := factsOf(*cached)
		v.DNS, v.Certificate = f.DNS, f.Certificate
		v.CheckedAt = new(cached.CheckedAt)
		v.State = edge.StateOf(f, true, t.redirectOf != "")
	}
	if !t.served() {
		v.State = edge.StateNotServed
	}
	return v
}

// routerOf is the Traefik router that serves a domain: the service's own,
// "<app>-<service>", or the domain's when it has options.
func routerOf(appID, service string, d app.Domain) string {
	if d.HasOptions() {
		return nomadjob.DomainRouterName(appID, service, d.Host)
	}
	return appID + "-" + service
}

// factsOf turns a stored check back into edge's facts. An address that doesn't
// parse (none should) is left out.
func factsOf(c store.DomainCheckRecord) edge.Facts {
	f := edge.Facts{
		DNS:         edge.DNS{Status: c.DNSStatus, Seen: parseAddrs(c.DNSSeen), Expected: parseAddrs(c.DNSExpected)},
		Certificate: edge.Certificate{Status: c.CertStatus, Issuer: c.CertIssuer},
		CheckedAt:   c.CheckedAt,
	}
	if c.CertNotAfter != nil {
		f.Certificate.NotAfter = *c.CertNotAfter
		if c.CertStatus == edge.CertIssued || c.CertStatus == edge.CertExpiring {
			f.Certificate.RenewsAt = c.CertNotAfter.Add(-certRenewBefore)
		}
	}
	return f
}

func parseAddrs(in []string) []netip.Addr {
	var out []netip.Addr
	for _, s := range in {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, a)
		}
	}
	return out
}

func addrStrings(in []netip.Addr) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = a.String()
	}
	return out
}

// markOrder ranks a state's mark for the list: problems first (failed,
// warning, rolling), then healthy, then idle.
var markOrder = map[string]int{"failed": 0, "warning": 1, "rolling": 2, "healthy": 3, "idle": 4}

func sortDomains(rows []DomainView) {
	slices.SortStableFunc(rows, func(x, y DomainView) int {
		return cmp.Or(
			cmp.Compare(markOrder[x.State.Mark()], markOrder[y.State.Mark()]),
			strings.Compare(x.Host, y.Host),
			strings.Compare(x.App, y.App),
		)
	})
}

// ---- checks --------------------------------------------------------------------

// probe runs a fresh check of one target: DNS and, when its app is served,
// the certificate. A target that isn't served gets DNS only and the
// certificate status none.
func (s *Service) probe(ctx context.Context, es domainSettings, host string, served bool, expected []netip.Addr, warn time.Duration) edge.Facts {
	dialer := es.dialer
	if !served {
		dialer = noDial{}
	}
	f := edge.Check(ctx, es.resolver, dialer, es.edgeAddress, host, expected, es.certResolver != "", warn, s.clock.Now().UTC())
	if !served {
		f.Certificate = edge.Certificate{Status: edge.CertNone}
	}
	return f
}

// noDial is a Dialer that never dials: the check of a host whose app isn't
// served asks DNS only.
type noDial struct{}

func (noDial) DialTLS(context.Context, string, string) (*tls.ConnectionState, error) {
	return nil, errNotProbed
}

var errNotProbed = errors.New("not probed")

// warnWindow is how long before expiry a certificate counts as expiring
// (Settings › Certificates, 14 days until changed).
func (s *Service) warnWindow(ctx context.Context) (time.Duration, error) {
	days, _, err := s.store.CertificateSettings(ctx)
	if err != nil {
		return 0, err
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// recordOf is the row a target's check is stored as.
func recordOf(t domainTarget, f edge.Facts, state edge.State) store.DomainCheckRecord {
	r := store.DomainCheckRecord{
		Host: t.host, AppID: t.rec.App.ID, Service: t.service.Name,
		State:       string(state),
		DNSStatus:   f.DNS.Status,
		DNSSeen:     addrStrings(f.DNS.Seen),
		DNSExpected: addrStrings(f.DNS.Expected),
		CertStatus:  f.Certificate.Status,
		CertIssuer:  f.Certificate.Issuer,
		CheckedAt:   f.CheckedAt,
	}
	if !f.Certificate.NotAfter.IsZero() {
		r.CertNotAfter = new(f.Certificate.NotAfter.UTC())
	}
	return r
}

// stateFor is a target's state from fresh facts.
func stateFor(t domainTarget, f edge.Facts) edge.State {
	return edge.StateOf(f, t.served(), t.redirectOf != "")
}

// CheckDomain checks host now, not from the cache: its DNS against this
// host's addresses and, when a served app has the host, the certificate
// Traefik presents for it. When a stored app has the host (as a domain or a
// redirecting pair), the result is saved as its row and the Domains pages are
// told when its state changed. A host no app has yet (the Add form) gets DNS
// only, with the certificate status none: it is issued after the domain is
// added. Records are what points the host here either way.
//
// appID, when set, must name a stored app (store.ErrNotFound otherwise); it
// doesn't change what is checked. A host that isn't a hostname, or is the
// console's own, is an *app.FieldError on "host".
func (s *Service) CheckDomain(ctx context.Context, host, appID string) (DomainCheck, error) {
	host = normalizeHost(host)
	if !validCheckHost(host) {
		return DomainCheck{}, app.NewFieldError("host", "%q is not a hostname", host)
	}
	if console := s.consoleHost(); console != "" && host == console {
		return DomainCheck{}, app.NewFieldError("host", "%s is the console's own address (CARAVANA_PUBLIC_URL): an app can't use it", host)
	}
	if appID != "" {
		if _, err := s.store.GetApp(ctx, appID); err != nil {
			return DomainCheck{}, err
		}
	}
	warn, err := s.warnWindow(ctx)
	if err != nil {
		return DomainCheck{}, fmt.Errorf("check %q: %w", host, err)
	}
	targets, err := s.domainTargets(ctx, DomainFilter{})
	if err != nil {
		return DomainCheck{}, fmt.Errorf("check %q: %w", host, err)
	}
	var target *domainTarget
	for i := range targets {
		if targets[i].host == host {
			target = &targets[i]
			break
		}
	}

	expected := s.hostAddresses(ctx)
	es := s.edgeSettings()
	served := target != nil && target.served()
	f := s.probe(ctx, es, host, served, expected, warn)
	if target != nil {
		if _, err := s.saveCheck(ctx, *target, f); err != nil {
			return DomainCheck{}, err
		}
	}
	return DomainCheck{Host: host, Records: recordsFor(host, expected), DNS: f.DNS, Certificate: f.Certificate, CheckedAt: f.CheckedAt}, nil
}

// saveCheck upserts a target's row from fresh facts and, when its state is new,
// tells the Domains pages. It reports whether the state changed.
func (s *Service) saveCheck(ctx context.Context, t domainTarget, f edge.Facts) (bool, error) {
	before, err := s.store.GetDomainCheck(ctx, t.host)
	had := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	rec := recordOf(t, f, stateFor(t, f))
	if err := s.store.UpsertDomainCheck(ctx, rec); err != nil {
		return false, err
	}
	changed := !had || before.State != rec.State || before.AppID != rec.AppID
	if changed {
		s.domainsChanged(rec.AppID)
		if had && before.AppID != rec.AppID {
			s.domainsChanged(before.AppID)
		}
		s.publish(Change{What: ChangeAttention})
	}
	return changed, nil
}

// domainsChanged tells the Domains pages that a domain of appID changed state:
// the app's own page and the page of every domain.
func (s *Service) domainsChanged(appID string) {
	s.publish(Change{AppID: appID, What: ChangeDomains})
}

// validCheckHost reports whether h is a lowercase hostname, as an app's
// domain must be: labels of letters, digits and inner hyphens, at most 253
// bytes in all.
func validCheckHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for label := range strings.SplitSeq(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return net.ParseIP(h) == nil // an address is not a domain
}
