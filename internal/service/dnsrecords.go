package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// DNS records that follow domains (feature 048). When a deploy adds a host
// under a zone that has a DNS provider, Caravana makes the record that points
// the host at this installation (an A per IPv4 address and an AAAA per IPv6
// one), marked "caravana:<app id>" in the record's comment, before Nomad gets
// the job; when a deploy drops the host or the app is deleted, it removes the
// record. Only records Caravana made, which dns_records lists, are ever
// changed or removed.
//
// A deploy first checks (checkDNSRecords, before the app is saved: a hand-made
// record pointing elsewhere refuses it, and nothing is changed), then makes the
// records (ensureDNSRecords, before Register), and after Register removes the
// records of hosts the app no longer has (pruneDNSRecords). Removals never fail
// a deploy or a delete: the row stays and the pass (dnspass.go) tries again.

var (
	// ErrDNSRecordExists is a deploy that adds a host with a record at the
	// provider that points somewhere else. The error is a *DNSRecordExistsError.
	ErrDNSRecordExists = errors.New("the host already has a DNS record that points elsewhere")
	// ErrHostAddressUnknown is a deploy that needs a DNS record while this
	// installation's public address is not known.
	ErrHostAddressUnknown = errors.New("this installation's public address is not known")
)

// DNSRecordExistsError is the refusal of a deploy that adds a host whose
// record at the provider points elsewhere. Field is the host's field in the
// request, Content what the record points at (an address, or a CNAME's target).
type DNSRecordExistsError struct {
	Field, Host, Zone, Content string
}

func (e *DNSRecordExistsError) Error() string {
	return fmt.Sprintf("%s: %s already has a record at %s pointing at %s; remove it there, or point it at this installation by hand",
		e.Field, e.Host, providerName, e.Content)
}

// Unwrap makes errors.Is(err, ErrDNSRecordExists) true.
func (e *DNSRecordExistsError) Unwrap() error { return ErrDNSRecordExists }

// providerName is the provider as words say it: Cloudflare is the only one.
const providerName = "Cloudflare"

// DNSProviderError is a deploy that needs the provider and can't have it: Err
// is dns.ErrRefused (the token is wrong or lacks Zone › DNS › Edit, or the
// provider has no such zone) or dns.ErrUnavailable. Its text is in Caravana's
// words, never the provider's.
type DNSProviderError struct {
	// Field is the field of the first domain that needed the provider; empty
	// when no field is at fault (a zone's own save or test).
	Field string
	Zone  string
	Err   error
	// Words is the sentence, without the field.
	Words string
}

func (e *DNSProviderError) Error() string {
	if e.Field == "" {
		return e.Words
	}
	return e.Field + ": " + e.Words
}

// Unwrap makes errors.Is find dns.ErrRefused or dns.ErrUnavailable.
func (e *DNSProviderError) Unwrap() error { return e.Err }

var (
	errDNSOff     = errors.New("DNS providers are not set up in this installation")
	errDNSNoToken = errors.New("the zone has no token stored")
)

// dnsProviderError turns anything a provider call failed with into a
// *DNSProviderError. Nothing of err is kept but its class.
func dnsProviderError(field, zone string, err error) *DNSProviderError {
	e := &DNSProviderError{Field: field, Zone: zone}
	switch {
	case errors.Is(err, errDNSOff):
		e.Err, e.Words = dns.ErrRefused, "DNS providers are not set up in this installation"
	case errors.Is(err, errDNSNoToken):
		e.Err, e.Words = dns.ErrRefused, fmt.Sprintf("Caravana has no token for zone %s: save it again in Settings › DNS providers", zone)
	case errors.Is(err, dns.ErrZoneNotFound):
		e.Err, e.Words = dns.ErrRefused, fmt.Sprintf("%s has no zone %s for this token", providerName, zone)
	case errors.Is(err, dns.ErrRefused):
		e.Err, e.Words = dns.ErrRefused, fmt.Sprintf("%s refused the token for zone %s: check that it has Zone › DNS › Edit for it", providerName, zone)
	default:
		e.Err, e.Words = dns.ErrUnavailable, fmt.Sprintf("%s could not be reached for zone %s", providerName, zone)
	}
	return e
}

// dnsHost is a host an app answers on and the field of the request it came from.
type dnsHost struct {
	Host, Field string
}

// dnsHostsOf lists the hosts of a's domains, each domain's www pair after it
// when it redirects, without repeats (the first field names a host).
func dnsHostsOf(a app.App) []dnsHost {
	var out []dnsHost
	seen := map[string]bool{}
	add := func(host, field string) {
		if host != "" && !seen[host] {
			seen[host] = true
			out = append(out, dnsHost{Host: host, Field: field})
		}
	}
	for i, svc := range a.Services {
		for j, d := range svc.Domains {
			base := fmt.Sprintf("services[%d].domains[%d]", i, j)
			add(d.Host, base+".host")
			if d.WWWRedirect {
				add(d.RedirectPair(), base+".wwwRedirect")
			}
		}
	}
	return out
}

// dnsPlan is what a deploy has to do at the providers: for each host under a
// zone that has no record row of the app yet, the records that exist already
// and the ones to create.
type dnsPlan struct {
	hosts []dnsHostPlan
}

type dnsHostPlan struct {
	host, zone, field string
	// adopt are records with the app's comment and an address of this
	// installation that no row of the app has: they become rows.
	adopt []dns.Record
	// create are the addresses with no record yet.
	create []netip.Addr
}

func recordType(a netip.Addr) string {
	if a.Is6() {
		return "AAAA"
	}
	return "A"
}

func dnsComment(appID string) string { return "caravana:" + appID }

// zoneClient is a provider for one zone and the zone's ID there.
type zoneClient struct {
	p  dns.Provider
	id string
}

// dnsClient builds the provider of a stored zone from its token, and finds the
// zone at the provider when its ID isn't known yet. Errors are classified by
// dnsProviderError.
func (s *Service) dnsClient(ctx context.Context, z store.DNSZone) (zoneClient, error) {
	f := s.dnsFactory.Load()
	if f == nil || *f == nil {
		return zoneClient{}, errDNSOff
	}
	token, ok, err := s.deployer.DNSZoneTokenFor(ctx, z.Zone)
	if err != nil {
		return zoneClient{}, err
	}
	if !ok {
		return zoneClient{}, errDNSNoToken
	}
	p, err := (*f)(z.Provider, token)
	if err != nil {
		return zoneClient{}, err
	}
	id := z.ZoneID
	if id == "" {
		if id, err = p.FindZone(ctx, z.Zone); err != nil {
			return zoneClient{}, err
		}
		if err := s.store.SetDNSZoneID(ctx, z.Zone, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return zoneClient{}, err
		}
	}
	return zoneClient{p: p, id: id}, nil
}

// zonesOf lists the stored zones by name and their names.
func (s *Service) zonesOf(ctx context.Context) (map[string]store.DNSZone, []string, error) {
	zones, err := s.store.ListDNSZones(ctx)
	if err != nil {
		return nil, nil, err
	}
	by := make(map[string]store.DNSZone, len(zones))
	names := make([]string, len(zones))
	for i, z := range zones {
		by[z.Zone], names[i] = z, z.Zone
	}
	return by, names, nil
}

// checkDNSRecords reads, for each host of a under a managed zone that has no
// record row of a, the provider's records of the host, and says what a deploy
// would have to do. A record that isn't Caravana's and points somewhere else
// is a *DNSRecordExistsError; one that already points here is left alone. An
// app whose managed hosts all have rows makes no provider call. It changes
// nothing.
func (s *Service) checkDNSRecords(ctx context.Context, a app.App) (*dnsPlan, error) {
	plan := &dnsPlan{}
	zones, names, err := s.zonesOf(ctx)
	if err != nil || len(zones) == 0 {
		return plan, err
	}
	mine, err := s.store.ListDNSRecordsOfApp(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	hasRow := map[string]bool{}
	for _, r := range mine {
		hasRow[r.Host] = true
	}
	var pending []dnsHost
	for _, h := range dnsHostsOf(a) {
		if _, ok := dns.ZoneOf(h.Host, names); ok && !hasRow[h.Host] {
			pending = append(pending, h)
		}
	}
	if len(pending) == 0 {
		return plan, nil
	}
	addrs := s.hostAddresses(ctx)
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s: Caravana can't tell its own public address, so it can't make the DNS record for %s: "+
			"set CARAVANA_PUBLIC_URL to an address that resolves: %w", pending[0].Field, pending[0].Host, ErrHostAddressUnknown)
	}
	here := map[string]bool{}
	for _, a := range addrs {
		here[a.String()] = true
	}
	known, err := s.store.ListDNSRecords(ctx)
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{} // provider IDs of rows
	for _, r := range known {
		owned[r.RecordID] = true
	}

	clients := map[string]zoneClient{}
	for _, h := range pending {
		zone, _ := dns.ZoneOf(h.Host, names)
		zc, ok := clients[zone]
		if !ok {
			if zc, err = s.dnsClient(ctx, zones[zone]); err != nil {
				return nil, dnsProviderError(h.Field, zone, err)
			}
			clients[zone] = zc
		}
		recs, err := zc.p.Records(ctx, zc.id, h.Host)
		if err != nil {
			return nil, dnsProviderError(h.Field, zone, err)
		}
		hp := dnsHostPlan{host: h.Host, zone: zone, field: h.Field}
		satisfied := map[string]bool{} // type|content
		for _, r := range recs {
			if r.Type != "A" && r.Type != "AAAA" && r.Type != "CNAME" {
				continue
			}
			addrRecord := r.Type != "CNAME"
			switch {
			case addrRecord && owned[r.ID], addrRecord && r.Comment == dnsComment(a.ID) && here[r.Content]:
				// Made by Caravana (a row of another app, left behind, or one
				// whose row was lost): it becomes this app's.
				hp.adopt = append(hp.adopt, r)
				satisfied[r.Type+"|"+r.Content] = true
			case addrRecord && here[r.Content]:
				satisfied[r.Type+"|"+r.Content] = true // hand-made, and already right
			default:
				return nil, &DNSRecordExistsError{Field: h.Field, Host: h.Host, Zone: zone, Content: r.Content}
			}
		}
		for _, addr := range addrs {
			if !satisfied[recordType(addr)+"|"+addr.String()] {
				hp.create = append(hp.create, addr)
			}
		}
		plan.hosts = append(plan.hosts, hp)
	}
	return plan, nil
}

// ensureDNSRecords makes the records plan says (computing the plan when the
// deploy didn't come through the check), one row written right after each
// create, so a failure half-way leaves what was made owned. Rows the app has
// already are skipped, so two deploys of one app never make a record twice.
func (s *Service) ensureDNSRecords(ctx context.Context, a app.App, plan *dnsPlan) error {
	if plan == nil {
		var err error
		if plan, err = s.checkDNSRecords(ctx, a); err != nil {
			return err
		}
	}
	if len(plan.hosts) == 0 {
		return nil
	}
	mine, err := s.store.ListDNSRecordsOfApp(ctx, a.ID)
	if err != nil {
		return err
	}
	have := map[string]bool{} // host|type|content
	for _, r := range mine {
		have[r.Host+"|"+r.Type+"|"+r.Content] = true
	}
	zones, _, err := s.zonesOf(ctx)
	if err != nil {
		return err
	}
	clients := map[string]zoneClient{}
	for _, hp := range plan.hosts {
		z, ok := zones[hp.zone]
		if !ok {
			continue // the zone was removed since the check: nothing to make
		}
		for _, r := range hp.adopt {
			if have[hp.host+"|"+r.Type+"|"+r.Content] {
				continue
			}
			if err := s.store.InsertDNSRecord(ctx, store.DNSRecord{Host: hp.host, Type: r.Type, Zone: hp.zone, AppID: a.ID,
				RecordID: r.ID, Content: r.Content, Proxied: r.Proxied, CreatedAt: s.clock.Now().UTC()}); err != nil {
				return err
			}
		}
		for _, addr := range hp.create {
			typ := recordType(addr)
			if have[hp.host+"|"+typ+"|"+addr.String()] {
				continue
			}
			zc, ok := clients[hp.zone]
			if !ok {
				if zc, err = s.dnsClient(ctx, z); err != nil {
					return dnsProviderError(hp.field, hp.zone, err)
				}
				clients[hp.zone] = zc
			}
			made, err := zc.p.Create(ctx, zc.id, dns.Record{Type: typ, Name: hp.host, Content: addr.String(),
				Proxied: z.Proxied, Comment: dnsComment(a.ID), TTL: 1})
			if err != nil {
				return dnsProviderError(hp.field, hp.zone, err)
			}
			if err := s.store.InsertDNSRecord(ctx, store.DNSRecord{Host: hp.host, Type: typ, Zone: hp.zone, AppID: a.ID,
				RecordID: made.ID, Content: addr.String(), Proxied: z.Proxied, CreatedAt: s.clock.Now().UTC()}); err != nil {
				return err
			}
		}
	}
	s.domainsChanged(a.ID)
	return nil
}

// pruneDNSRecords removes the records of a's rows whose host is no longer one
// of a's hosts. A failure is logged and the row kept: the pass tries again.
func (s *Service) pruneDNSRecords(ctx context.Context, a app.App) {
	rows, err := s.store.ListDNSRecordsOfApp(ctx, a.ID)
	if err != nil {
		s.log.Warn("list the DNS records of an app; the pass tries again", "app", a.ID, "error", err)
		return
	}
	keep := map[string]bool{}
	for _, h := range dnsHostsOf(a) {
		keep[h.Host] = true
	}
	stale := slices.DeleteFunc(rows, func(r store.DNSRecord) bool { return keep[r.Host] })
	if s.removeDNSRows(ctx, stale) > 0 || len(stale) > 0 {
		s.domainsChanged(a.ID)
	}
}

// removeDNSRecords removes every record of an app (it is being deleted).
// Failures are logged and the rows kept for the pass.
func (s *Service) removeDNSRecords(ctx context.Context, appID string) {
	rows, err := s.store.ListDNSRecordsOfApp(ctx, appID)
	if err != nil {
		s.log.Warn("list the DNS records of a deleted app; the pass tries again", "app", appID, "error", err)
		return
	}
	s.removeDNSRows(ctx, rows)
}

// removeDNSRows deletes each row's record at the provider and then the row. A
// row whose zone is no longer stored loses only the row. A failure is logged
// once per zone or record, never fails anything, and keeps the row. It
// returns how many rows it couldn't remove.
func (s *Service) removeDNSRows(ctx context.Context, rows []store.DNSRecord) int {
	if len(rows) == 0 {
		return 0
	}
	zones, _, err := s.zonesOf(ctx)
	if err != nil {
		s.log.Warn("read the DNS zones; the pass tries again", "error", err)
		return len(rows)
	}
	clients := map[string]*zoneClient{} // nil: this zone's client could not be built
	failed := 0
	for _, r := range rows {
		z, ok := zones[r.Zone]
		if !ok {
			if err := s.store.DeleteDNSRecord(ctx, r.RecordID); err != nil {
				s.log.Warn("drop the row of a record of a removed zone", "zone", r.Zone, "error", err)
				failed++
			}
			continue
		}
		zc, tried := clients[r.Zone]
		if !tried {
			c, err := s.dnsClient(ctx, z)
			if err != nil {
				s.log.Warn("remove DNS records: the provider isn't usable; the pass tries again", "zone", r.Zone, "error", dnsProviderError("", r.Zone, err).Words)
			} else {
				zc = &c
			}
			clients[r.Zone] = zc
		}
		if zc == nil {
			failed++
			continue
		}
		if err := zc.p.Delete(ctx, zc.id, r.RecordID); err != nil {
			s.log.Warn("remove a DNS record; the pass tries again", "zone", r.Zone, "host", r.Host, "error", dnsProviderError("", r.Zone, err).Words)
			failed++
			continue
		}
		if err := s.store.DeleteDNSRecord(ctx, r.RecordID); err != nil {
			s.log.Warn("drop the row of a removed DNS record", "zone", r.Zone, "host", r.Host, "error", err)
			failed++
		}
	}
	return failed
}

// ManagedRecord is the fact that Caravana made the record of a domain.
type ManagedRecord struct {
	Zone     string
	Provider string
	// Proxied is the proxy setting the record has.
	Proxied   bool
	CreatedAt time.Time
}

// ManagedZone returns the stored zone that covers host, if any.
func (s *Service) ManagedZone(ctx context.Context, host string) (store.DNSZone, bool, error) {
	zones, names, err := s.zonesOf(ctx)
	if err != nil {
		return store.DNSZone{}, false, err
	}
	zone, ok := dns.ZoneOf(normalizeHost(host), names)
	if !ok {
		return store.DNSZone{}, false, nil
	}
	return zones[zone], true, nil
}

// managedRecords maps each host to the record Caravana made for it (the first
// row by type), for the Domains views.
func (s *Service) managedRecords(ctx context.Context) (map[string]*ManagedRecord, error) {
	rows, err := s.store.ListDNSRecords(ctx)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	zones, _, err := s.zonesOf(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]*ManagedRecord{}
	for _, r := range rows {
		if _, ok := out[r.Host]; ok {
			continue
		}
		out[r.Host] = &ManagedRecord{Zone: r.Zone, Provider: zones[r.Zone].Provider, Proxied: r.Proxied, CreatedAt: r.CreatedAt}
	}
	return out, nil
}
