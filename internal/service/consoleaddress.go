package service

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Changing the console's address from the console (feature 051).
//
// The owner asks for a new domain. Caravana checks the name and its DNS before it records anything
// (StartConsoleAddressChange), then puts the domain beside the current one in Traefik's console
// route and waits until Traefik routes it to Caravana and presents a certificate for it (the check
// goroutine, consolechecks.go). SwitchConsoleAddress hands the change to the server agent that runs
// beside Caravana as root: it rewrites the public URL in Caravana's env file and the installer's
// record and restarts Caravana (the agent's side is in internal/nodeagent). The console never gets
// root. The old domain stays routed for the redirect window and Caravana redirects its pages
// (ConsoleRedirect); the pass (consolesettle.go) settles what a restart left and keeps the route
// right.

// The redirect windows the owner may choose, in days. 0 takes the old domain out of the route at
// the switch.
const (
	DefaultConsoleRedirectDays = 30
	consoleReasonMax           = 300 // the longest reason a change keeps, in bytes
)

// The refusals of the change calls. The typed ones below carry their words and field; a caller tests
// all of them with errors.Is.
var (
	// ErrConsoleChangeOpen: a change is already checking, ready or switching (409
	// console_address_change_open).
	ErrConsoleChangeOpen = errors.New("a change of the console's address is already open: cancel it or wait for it to end")
	// ErrConsoleChangeNotFound: no change has that ID (404 console_address_change_not_found).
	ErrConsoleChangeNotFound = errors.New("no such change of the console's address")
	// ErrConsoleChangeNotCancellable: the change is switching or already ended (409
	// console_address_not_cancellable).
	ErrConsoleChangeNotCancellable = errors.New("the change can't be cancelled any more: it is switching or has ended")
	// ErrConsoleChangeNotReady: only a ready change can be switched (409 console_address_not_ready).
	ErrConsoleChangeNotReady = errors.New("the change isn't ready to switch: its checks haven't all passed")
	// ErrConsoleAddressUnsupported: this installation can't change the address; a
	// *ConsoleUnsupportedError says why (409 console_address_unsupported).
	ErrConsoleAddressUnsupported = errors.New("this installation can't change the console's address")
	// ErrDomainNotHere: the domain's DNS doesn't point at this server; a *DomainNotHereError says
	// what it found (422 domain_not_here).
	ErrDomainNotHere = errors.New("the domain doesn't point at this server")
	// ErrConsoleCheckFailed: a fresh check at the switch failed; a *ConsoleCheckError says which
	// (422 console_address_check_failed).
	ErrConsoleCheckFailed = errors.New("a check of the new address failed")
)

// ConsoleUnsupportedError is ErrConsoleAddressUnsupported with the sentence the owner reads.
type ConsoleUnsupportedError struct{ Words string }

func (e *ConsoleUnsupportedError) Error() string { return e.Words }

// Unwrap makes errors.Is(err, ErrConsoleAddressUnsupported) true.
func (e *ConsoleUnsupportedError) Unwrap() error { return ErrConsoleAddressUnsupported }

// DomainNotHereError is ErrDomainNotHere. Field is "domain"; Words says what DNS showed; Zone is set
// when a DNS provider is connected for the domain's zone and the domain has no record, so Caravana
// could make it if asked (createDNSRecord).
type DomainNotHereError struct {
	Field, Words, Zone string
}

func (e *DomainNotHereError) Error() string { return e.Field + ": " + e.Words }

// Unwrap makes errors.Is(err, ErrDomainNotHere) true.
func (e *DomainNotHereError) Unwrap() error { return ErrDomainNotHere }

// ConsoleCheckError is ErrConsoleCheckFailed: the route or the certificate wasn't there at the
// switch. Field is "domain".
type ConsoleCheckError struct{ Field, Words string }

func (e *ConsoleCheckError) Error() string { return e.Field + ": " + e.Words }

// Unwrap makes errors.Is(err, ErrConsoleCheckFailed) true.
func (e *ConsoleCheckError) Unwrap() error { return ErrConsoleCheckFailed }

// ConsoleNoAgentError is ErrNoAgent for the switch: no agent that runs beside Caravana reported
// lately.
type ConsoleNoAgentError struct{}

func (ConsoleNoAgentError) Error() string {
	return "No server agent has reported beside Caravana in the last 10 minutes, so it can't switch the address: " +
		"run the installer on this server again (the agent is part of it) or check that caravana-node.timer is running."
}

// Is makes errors.Is(err, ErrNoAgent) true.
func (ConsoleNoAgentError) Is(target error) bool { return target == ErrNoAgent }

// The names and states of a change's checks.
const (
	checkHostname    = "hostname"
	checkDNS         = "dns"
	checkRoute       = "route"
	checkCertificate = "certificate"

	checkOK      = "ok"
	checkFailed  = "failed"
	checkWaiting = "waiting"
)

// consoleAgentFresh is how recently an agent that runs the console must have reported for a switch.
const consoleAgentFresh = 10 * time.Minute

// consoleAddressState is the service's state for the console's address, see consoleaddress.go.
type consoleAddressState struct {
	// mu serializes the calls that read a change and then move it: a start, a cancel, a switch, the
	// check goroutine's steps, the pass and the settling from an agent's report. It is taken after
	// serverMu, never before.
	mu sync.Mutex
	// running holds the IDs of the changes whose check goroutine is going in this process; a change
	// that is checking and not in it was left by a process that ended. Guarded by mu.
	running map[string]bool
	// written is what the route's variable was last made to say: nil when this process hasn't
	// written or read it yet. Guarded by mu.
	written *string
	// routed is what the route carries besides the current host, read without a lock by the
	// redirect and by the app checks.
	routed atomic.Pointer[consoleRouted]
	// probe asks whether Traefik routes a host to Caravana; the real one dials the edge.
	probe atomic.Pointer[ConsoleRouteProbe]

	passGap   time.Duration // between two passes
	poll      time.Duration // between two looks of the check goroutine
	limit     time.Duration // how long the check goroutine waits for the route and the certificate
	lostAfter time.Duration // how long a switch waits for the agent's outcome
}

// consoleRouted is the route's hosts other than the current one, as the last reconcile found them.
type consoleRouted struct {
	// pending is the open change's domain ("" for none); reserved is every host the route carries
	// besides the current one, which no app may use.
	pending  string
	reserved map[string]bool
	// old are the old domains whose redirect window hasn't ended, and when it ends.
	old map[string]time.Time
}

func newConsoleAddressState() consoleAddressState {
	return consoleAddressState{
		running:   map[string]bool{},
		passGap:   defaultConsoleAddressPassGap,
		poll:      10 * time.Second,
		limit:     5 * time.Minute,
		lostAfter: 5 * time.Minute,
	}
}

// defaultConsoleAddressPassGap is how often the pass settles changes and keeps the route right.
const defaultConsoleAddressPassGap = time.Minute

// SetConsoleAddressPassGap sets how often the console address pass runs (a minute by default). Call
// it before Start; a value that isn't positive keeps the default.
func (s *Service) SetConsoleAddressPassGap(d time.Duration) {
	if d > 0 {
		s.consoleAddr.passGap = d
	}
}

// ConsoleDependents counts what holds the current address, for the page that asks before a change.
type ConsoleDependents struct {
	Passkeys     int // passkeys registered on the current domain
	Webhooks     int // apps with a webhook token
	DeployTokens int // active deploy tokens
	APITokens    int // active full API tokens
	Servers      int // joined servers: they follow by themselves
}

// ConsoleAddressView is the console's address as the owner sees it.
type ConsoleAddressView struct {
	Domain    string // the host of the public URL; "" when Caravana has none
	PublicURL string
	Issuer    string // the cluster's workload identity issuer; "" for none
	SSLIPHint string // "<a-b-c-d>.sslip.io" of this server's IPv4 address; "" when unknown
	CanChange bool
	// CannotChange is one sentence saying why not when CanChange is false.
	CannotChange string
	// Agent says an agent that runs beside Caravana reported in the last 10 minutes: a switch needs it.
	Agent      bool
	OldDomains []store.ConsoleOldDomain
	// Change is the open change, else the newest one that ended; nil before any was made.
	Change     *store.ConsoleAddressChange
	Dependents ConsoleDependents
}

// The reasons an installation can't change its address, in the owner's words.
const (
	reasonNoPublicURL = "This console has no public https address (CARAVANA_PUBLIC_URL), so there is no address to change: " +
		"it is reached by a local address. Install Caravana with a domain first."
	reasonNoTraefik     = "Traefik isn't running on this installation, so the console's route can't be changed."
	reasonOldTraefik    = "This installation's Traefik job is from before the console's address could change: run the installer again, once (Traefik restarts for a few seconds), then change the address here."
	reasonTraefikUnread = "Caravana couldn't read Traefik's job from Nomad just now, so it can't tell whether the address can change."
)

// consoleSupport says why this installation can't change the console's address, in words; "" means
// it can. An error is Nomad failing to answer, which is no verdict.
func (s *Service) consoleSupport(ctx context.Context) (string, error) {
	public := s.PublicURL()
	if public == "" {
		return reasonNoPublicURL, nil
	}
	if clean, ok := mesh.CleanIssuer(public); !ok || clean == "" || s.consoleHost() == "" {
		return reasonNoPublicURL, nil
	}
	ok, err := s.deployer.ConsoleRouteSupported(ctx)
	switch {
	case errors.Is(err, deploy.ErrNoProxy):
		return reasonNoTraefik, nil
	case err != nil:
		return "", err
	case !ok:
		return reasonOldTraefik, nil
	}
	return "", nil
}

// ConsoleAddress reads the current address, what depends on it and the open or newest change.
func (s *Service) ConsoleAddress(ctx context.Context) (ConsoleAddressView, error) {
	now := s.clock.Now().UTC()
	v := ConsoleAddressView{Domain: s.consoleHost(), PublicURL: s.PublicURL(), Issuer: s.ClusterIssuer()}

	reason, err := s.consoleSupport(ctx)
	switch {
	case err != nil:
		v.CannotChange = reasonTraefikUnread
	case reason != "":
		v.CannotChange = reason
	default:
		v.CanChange = true
	}
	if v.Agent, err = s.consoleAgentSeen(ctx, now); err != nil {
		return ConsoleAddressView{}, err
	}
	olds, err := s.store.ConsoleOldDomains(ctx, now)
	if err != nil {
		return ConsoleAddressView{}, err
	}
	v.OldDomains = slices.DeleteFunc(olds, func(o store.ConsoleOldDomain) bool { return o.Domain == v.Domain })
	if c, ok, err := s.store.OpenConsoleAddressChange(ctx); err != nil {
		return ConsoleAddressView{}, err
	} else if ok {
		v.Change = &c
	} else if c, ok, err := s.store.LatestConsoleAddressChange(ctx); err != nil {
		return ConsoleAddressView{}, err
	} else if ok {
		v.Change = &c
	}
	if v.Dependents, err = s.consoleDependents(ctx, v.Domain, now); err != nil {
		return ConsoleAddressView{}, err
	}
	for _, a := range s.hostAddresses(ctx) {
		if a.Is4() {
			v.SSLIPHint = strings.ReplaceAll(a.String(), ".", "-") + ".sslip.io"
			break
		}
	}
	return v, nil
}

func (s *Service) consoleDependents(ctx context.Context, domain string, now time.Time) (ConsoleDependents, error) {
	var d ConsoleDependents
	var err error
	if domain != "" {
		if d.Passkeys, err = s.store.CountAllPasskeys(ctx, domain); err != nil {
			return d, err
		}
	}
	if d.Webhooks, err = s.store.CountWebhookTokens(ctx); err != nil {
		return d, err
	}
	tokens, err := s.store.ListAllAPITokens(ctx)
	if err != nil {
		return d, err
	}
	for _, t := range tokens {
		if t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
			continue
		}
		if t.ScopeApp != "" {
			d.DeployTokens++
		} else {
			d.APITokens++
		}
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return d, err
	}
	for _, r := range servers {
		if !r.First {
			d.Servers++
		}
	}
	return d, nil
}

// consoleAgentSeen says an agent that runs the console (it reported runsConsole) was heard from in
// the last 10 minutes. It reads the stored reports, so a restart of Caravana doesn't forget it.
func (s *Service) consoleAgentSeen(ctx context.Context, now time.Time) (bool, error) {
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if r.SeenAt.IsZero() || now.Sub(r.SeenAt) > consoleAgentFresh {
			continue
		}
		if rep, ok := parseReport(r.Report); ok && rep.RunsConsole {
			return true, nil
		}
	}
	return false, nil
}

// StartConsoleAddressChange begins a change to domain: it checks the name and its DNS before anything
// is recorded or changed, then records the change as checking and starts the checks that wait for
// Traefik's route and certificate (see ConsoleAddressChange for how it goes on). redirectDays is how
// long the old domain keeps redirecting after the switch: 0, 7, 30 or 90 (callers send
// DefaultConsoleRedirectDays when the owner didn't say). With createRecord, a domain that has no
// record under a zone connected to a DNS provider gets its A and AAAA records made.
//
// Refusals: *app.FieldError on "domain" (not a host name, the current domain) or "redirectDays";
// *ConsoleUnsupportedError; ErrConsoleChangeOpen; *DomainTakenError on "domain" (an app has it);
// *DomainNotHereError; *DNSProviderError on "domain" when making the record failed. Nothing is
// recorded for any of them.
func (s *Service) StartConsoleAddressChange(ctx context.Context, domain string, redirectDays int, createRecord bool) (store.ConsoleAddressChange, error) {
	domain = normalizeHost(domain)
	if !mesh.ValidConsoleDomain(domain) {
		return store.ConsoleAddressChange{}, app.NewFieldError("domain", "%q is not a host name: use letters, digits, dashes and dots, like console.example.com", domain)
	}
	if !slices.Contains([]int{0, 7, 30, 90}, redirectDays) {
		return store.ConsoleAddressChange{}, app.NewFieldError("redirectDays", "must be 0, 7, 30 or 90 days, not %d", redirectDays)
	}
	current := s.consoleHost()
	if domain == current {
		return store.ConsoleAddressChange{}, app.NewFieldError("domain", "%s is the console's address already", domain)
	}

	s.consoleAddr.mu.Lock()
	defer s.consoleAddr.mu.Unlock()
	if reason, err := s.consoleSupport(ctx); err != nil {
		return store.ConsoleAddressChange{}, err
	} else if reason != "" {
		return store.ConsoleAddressChange{}, &ConsoleUnsupportedError{Words: reason}
	}
	if _, open, err := s.store.OpenConsoleAddressChange(ctx); err != nil {
		return store.ConsoleAddressChange{}, err
	} else if open {
		return store.ConsoleAddressChange{}, ErrConsoleChangeOpen
	}
	if owner, taken, err := s.appWithHost(ctx, domain); err != nil {
		return store.ConsoleAddressChange{}, err
	} else if taken {
		return store.ConsoleAddressChange{}, &DomainTakenError{Field: "domain", Host: domain, App: owner}
	}
	dnsMessage, created, err := s.checkConsoleDNS(ctx, domain, createRecord)
	if err != nil {
		return store.ConsoleAddressChange{}, err
	}

	var raw [16]byte
	if _, err := crand.Read(raw[:]); err != nil {
		return store.ConsoleAddressChange{}, fmt.Errorf("make a change ID: %w", err)
	}
	c, err := s.store.InsertConsoleAddressChange(ctx, store.ConsoleAddressChange{
		ID: hex.EncodeToString(raw[:]), Domain: domain, OldDomain: current, RedirectDays: redirectDays,
		Status: store.ConsoleChecking, DNSRecordCreated: created, CreatedAt: s.clock.Now().UTC(),
		Checks: []store.ConsoleCheck{
			{Name: checkHostname, State: checkOK, Message: domain + " is a host name"},
			{Name: checkDNS, State: checkOK, Message: dnsMessage},
			{Name: checkRoute, State: checkWaiting, Message: "Waiting for Traefik to route " + domain + " to Caravana"},
			{Name: checkCertificate, State: checkWaiting, Message: "Waiting for a certificate for " + domain},
		},
	})
	if errors.Is(err, store.ErrConsoleChangeOpen) {
		return store.ConsoleAddressChange{}, ErrConsoleChangeOpen
	} else if err != nil {
		return store.ConsoleAddressChange{}, err
	}
	s.noteConsoleChange(ctx, audit.ActionRequested, c,
		audit.Change{Path: "domain", Op: audit.OpChanged, From: current, To: domain},
		audit.Change{Path: "redirectDays", Op: audit.OpAdded, To: fmt.Sprint(redirectDays)})
	// An app deploy that follows must already see the domain as the console's.
	if _, _, err := s.refreshConsoleRoutedLocked(ctx); err != nil {
		s.log.Warn("console address: refresh what the route carries", "error", err)
	}
	s.consoleAddr.running[c.ID] = true
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.runConsoleChecks(s.background(), c.ID)
	}()
	s.consoleChanged()
	return c, nil
}

// appWithHost finds the stored app that has host as a domain or as the pair its wwwRedirect redirects.
func (s *Service) appWithHost(ctx context.Context, host string) (appID string, found bool, err error) {
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return "", false, err
	}
	for _, rec := range recs {
		for _, h := range dnsHostsOf(rec.App) {
			if h.Host == host {
				return rec.App.ID, true, nil
			}
		}
	}
	return "", false, nil
}

// checkConsoleDNS refuses a domain whose DNS doesn't point at this server, and says in a sentence what
// it found when it does. With createRecord, and a connected zone covering a domain that has no record,
// it makes the A and AAAA records first: the sentence then says so, and no wait for public resolvers
// follows (Let's Encrypt asks the zone's own servers). created says Caravana made records.
func (s *Service) checkConsoleDNS(ctx context.Context, domain string, createRecord bool) (message string, created bool, err error) {
	expected := s.hostAddresses(ctx)
	warn, err := s.warnWindow(ctx)
	if err != nil {
		return "", false, err
	}
	f := s.probe(ctx, s.edgeSettings(), domain, false, expected, warn)
	zone, managed, err := s.ManagedZone(ctx, domain)
	if err != nil {
		return "", false, err
	}
	switch {
	case f.DNS.Status == edge.DNSOK:
		return fmt.Sprintf("%s points at this server (%s)", domain, addrList(f.DNS.Seen)), false, nil
	case len(expected) == 0:
		return "", false, &DomainNotHereError{Field: "domain",
			Words: "Caravana can't tell this server's own public address (the host of CARAVANA_PUBLIC_URL doesn't resolve), so it can't tell whether " + domain + " points at it."}
	case f.DNS.Status == edge.DNSNone && managed && createRecord:
		made, err := s.createConsoleRecords(ctx, domain, zone, expected)
		if err != nil {
			return "", false, err
		}
		return fmt.Sprintf("Caravana made the DNS record of %s in zone %s (%s): it points at %s. Public resolvers may take a few minutes to show it.",
			domain, zone.Zone, providerName, addrList(expected)), made, nil
	case f.DNS.Status == edge.DNSNone:
		words := fmt.Sprintf("%s has no DNS record. Point it at this server (%s), then check again.", domain, addrList(expected))
		e := &DomainNotHereError{Field: "domain", Words: words}
		if managed {
			e.Zone = zone.Zone
			e.Words = fmt.Sprintf("%s has no DNS record. Point it at this server (%s), or let Caravana create the record in zone %s (%s) if you ask it to.",
				domain, addrList(expected), zone.Zone, providerName)
		}
		return "", false, e
	case f.DNS.Status == edge.DNSElsewhere:
		return "", false, &DomainNotHereError{Field: "domain",
			Words: fmt.Sprintf("%s points at %s, not at this server (%s). Change the record, then check again.", domain, addrList(f.DNS.Seen), addrList(expected))}
	}
	return "", false, &DomainNotHereError{Field: "domain",
		Words: "No DNS server answered for " + domain + ". Try again in a moment."}
}

// createConsoleRecords makes the A and AAAA records of host in zone, one per address of this server,
// with the comment "caravana:console". A record it finds already pointing here is kept; any other record
// of the host (another address, a CNAME) is never touched and refuses the call. No dns_records row is
// kept: the pass removes the records of app domains only, and these must stay with the console.
func (s *Service) createConsoleRecords(ctx context.Context, host string, zone store.DNSZone, addrs []netip.Addr) (made bool, err error) {
	zc, err := s.dnsClient(ctx, zone)
	if err != nil {
		return false, dnsProviderError("domain", zone.Zone, err)
	}
	existing, err := zc.p.Records(ctx, zc.id, host)
	if err != nil {
		return false, dnsProviderError("domain", zone.Zone, err)
	}
	here := map[string]bool{}
	for _, a := range addrs {
		here[a.String()] = true
	}
	have := map[string]bool{}
	for _, r := range existing {
		if r.Type != "A" && r.Type != "AAAA" && r.Type != "CNAME" {
			continue
		}
		if r.Type == "CNAME" || !here[r.Content] {
			return false, &DomainNotHereError{Field: "domain", Zone: zone.Zone,
				Words: fmt.Sprintf("%s already has a record at %s pointing at %s: change or remove it there, or point it at this server by hand.", host, providerName, r.Content)}
		}
		have[r.Type+"|"+r.Content] = true
	}
	for _, a := range addrs {
		typ := recordType(a)
		if have[typ+"|"+a.String()] {
			continue
		}
		if _, err := zc.p.Create(ctx, zc.id, dns.Record{Type: typ, Name: host, Content: a.String(),
			Proxied: zone.Proxied, Comment: "caravana:console", TTL: 1}); err != nil {
			return made, dnsProviderError("domain", zone.Zone, err)
		}
		made = true
	}
	return made, nil
}

// addrList writes addresses as a comma-separated list.
func addrList(addrs []netip.Addr) string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return strings.Join(out, ", ")
}

// ConsoleAddressChange reads one change by ID; an unknown ID is ErrConsoleChangeNotFound.
func (s *Service) ConsoleAddressChange(ctx context.Context, id string) (store.ConsoleAddressChange, error) {
	c, err := s.store.ConsoleAddressChange(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.ConsoleAddressChange{}, fmt.Errorf("change %q: %w", id, ErrConsoleChangeNotFound)
	}
	return c, err
}

// CancelConsoleAddressChange ends a change that is checking, ready or has failed its checks, and takes
// its pending domain out of Traefik's route. A change that is switching or already ended is
// ErrConsoleChangeNotCancellable; an unknown ID is ErrConsoleChangeNotFound.
func (s *Service) CancelConsoleAddressChange(ctx context.Context, id string) (store.ConsoleAddressChange, error) {
	s.consoleAddr.mu.Lock()
	defer s.consoleAddr.mu.Unlock()
	c, err := s.ConsoleAddressChange(ctx, id)
	if err != nil {
		return store.ConsoleAddressChange{}, err
	}
	c.Status, c.SettledAt, c.Reason = store.ConsoleCancelled, s.clock.Now().UTC(), ""
	err = s.store.UpdateConsoleAddressChange(ctx, c, store.ConsoleChecking, store.ConsoleReady, store.ConsoleCheckFailed)
	switch {
	case errors.Is(err, store.ErrInvalidTransition):
		return store.ConsoleAddressChange{}, ErrConsoleChangeNotCancellable
	case err != nil:
		return store.ConsoleAddressChange{}, err
	}
	s.noteConsoleChange(ctx, audit.ActionCancelled, c)
	s.consoleChanged()
	if err := s.reconcileConsoleRouteLocked(ctx); err != nil {
		// The change is cancelled; the pass takes the domain out of the route when Nomad answers.
		s.log.Warn("console address: take the cancelled domain out of the route; the pass tries again", "error", err)
	}
	return c, nil
}

// SwitchConsoleAddress hands a ready change to the server agent: it checks the name, the route and the
// certificate once more, without waiting, and marks the change switching. The agent gets it in its next
// sync answer (within about a minute), rewrites the address and restarts Caravana. A change that isn't
// ready is ErrConsoleChangeNotReady; no agent that runs beside Caravana is a ConsoleNoAgentError
// (ErrNoAgent); a check that fails is a *DomainNotHereError or *ConsoleCheckError and the change stays
// ready with its checks updated.
func (s *Service) SwitchConsoleAddress(ctx context.Context, id string) (store.ConsoleAddressChange, error) {
	s.consoleAddr.mu.Lock()
	defer s.consoleAddr.mu.Unlock()
	c, err := s.ConsoleAddressChange(ctx, id)
	if err != nil {
		return store.ConsoleAddressChange{}, err
	}
	if c.Status != store.ConsoleReady {
		return store.ConsoleAddressChange{}, ErrConsoleChangeNotReady
	}
	now := s.clock.Now().UTC()
	if seen, err := s.consoleAgentSeen(ctx, now); err != nil {
		return store.ConsoleAddressChange{}, err
	} else if !seen {
		return store.ConsoleAddressChange{}, ConsoleNoAgentError{}
	}
	if err := s.freshConsoleCheck(ctx, &c); err != nil {
		return store.ConsoleAddressChange{}, err
	}
	c.Status, c.SwitchAt, c.Reason = store.ConsoleSwitching, now, ""
	if err := s.store.UpdateConsoleAddressChange(ctx, c, store.ConsoleReady); errors.Is(err, store.ErrInvalidTransition) {
		return store.ConsoleAddressChange{}, ErrConsoleChangeNotReady
	} else if err != nil {
		return store.ConsoleAddressChange{}, err
	}
	s.noteConsoleChange(ctx, audit.ActionUpdated, c,
		audit.Change{Path: "domain", Op: audit.OpChanged, From: c.OldDomain, To: c.Domain})
	s.consoleChanged()
	return c, nil
}

// noteConsoleChange notes a step of a change of the console's address: the change is the record.
func (s *Service) noteConsoleChange(ctx context.Context, action audit.Action, c store.ConsoleAddressChange, changes ...audit.Change) {
	s.noteSetting(ctx, action, namedResource(audit.KindConsoleAddress, c.ID, c.Domain),
		&audit.Record{Kind: audit.RecordConsoleAddressChange, ID: c.ID}, changes...)
}

// consoleChanged tells the pages the change moved.
func (s *Service) consoleChanged() { s.publish(Change{What: ChangeConsoleAddress}) }
