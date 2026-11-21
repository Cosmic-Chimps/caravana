package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// DNS provider zones (feature 048): the zones Caravana may make records in.
// SQLite keeps what the console shows; the zone's token lives in a Nomad
// Variable (package deploy), as the registries' and the secret providers'
// credentials do. Saving or removing a zone deploys nothing: records follow
// domains at deploy time (dnsrecords.go).

// DNSProviderCloudflare is the one provider kind there is.
const DNSProviderCloudflare = "cloudflare"

// SetDNSProviders sets how a zone's provider is built from its token
// (dns.CloudflareFactory in production, dnstest's in tests). Until it is
// called no zone can be saved or tested, and a deploy under a zone is refused.
func (s *Service) SetDNSProviders(f dns.Factory) { s.dnsFactory.Store(&f) }

// DNSZoneInput is what SaveDNSZone takes. Token is nil to keep the stored one;
// it is required the first time and may not be empty.
type DNSZoneInput struct {
	Zone     string
	Provider string
	// Proxied is the proxy setting of the records Caravana makes.
	Proxied bool
	Token   *string
}

// DNSZoneInfo is a zone with how many records Caravana made in it.
type DNSZoneInfo struct {
	store.DNSZone
	Records int
}

// DNSZoneRecord is a record as the provider has it now.
type DNSZoneRecord struct {
	dns.Record
	// Managed is true for a record Caravana made, and App says for which app.
	Managed bool
	App     string
}

// DNSZoneTestResult is the answer of TestDNSZone: the zone with its new last
// test, and the zone's records as the provider has them now (never stored).
type DNSZoneTestResult struct {
	Zone    DNSZoneInfo
	Records []DNSZoneRecord
}

// DNSZones returns every zone with the records Caravana made in it, ordered by
// name, never with a token.
func (s *Service) DNSZones(ctx context.Context) ([]DNSZoneInfo, error) {
	zones, err := s.store.ListDNSZones(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.dnsCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DNSZoneInfo, len(zones))
	for i, z := range zones {
		out[i] = DNSZoneInfo{DNSZone: z, Records: counts[z.Zone]}
	}
	return out, nil
}

// DNSZone returns one zone, or store.ErrNotFound.
func (s *Service) DNSZone(ctx context.Context, zone string) (DNSZoneInfo, error) {
	z, err := s.store.GetDNSZone(ctx, normalizeHost(zone))
	if err != nil {
		return DNSZoneInfo{}, err
	}
	counts, err := s.dnsCounts(ctx)
	if err != nil {
		return DNSZoneInfo{}, err
	}
	return DNSZoneInfo{DNSZone: z, Records: counts[z.Zone]}, nil
}

func (s *Service) dnsCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.store.ListDNSRecords(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Zone]++
	}
	return counts, nil
}

// DNSRecords lists the records Caravana made, ordered by host.
func (s *Service) DNSRecords(ctx context.Context) ([]store.DNSRecord, error) {
	return s.store.ListDNSRecords(ctx)
}

// SaveDNSZone creates or replaces a zone. The token goes to the Nomad Variable
// first, then the settings to SQLite, so a zone listed in the console always has
// one. Errors are *app.FieldError on zone, provider or token. created is true
// for a new zone. The last test is forgotten when the token or the proxy
// setting changed.
//
// The records Caravana made in the zone are brought to the proxy setting, on
// every save that finds one that differs; when the provider can't take some,
// the saved zone comes back together with a *DNSProviderError that says
// "saved, but the old setting stays on N records; the next save tries again".
func (s *Service) SaveDNSZone(ctx context.Context, in DNSZoneInput) (info DNSZoneInfo, created bool, err error) {
	zone := normalizeHost(in.Zone)
	if !validCheckHost(zone) || !strings.Contains(zone, ".") {
		return DNSZoneInfo{}, false, app.NewFieldError("zone", "%q is not a domain name: write it in lower case with its dot, like example.com", in.Zone)
	}
	if in.Provider != DNSProviderCloudflare {
		return DNSZoneInfo{}, false, app.NewFieldError("provider", "must be %q", DNSProviderCloudflare)
	}
	if f := s.dnsFactory.Load(); f == nil || *f == nil {
		return DNSZoneInfo{}, false, app.NewFieldError("provider", "%s", errDNSOff.Error())
	}
	existing, err := s.store.GetDNSZone(ctx, zone)
	switch {
	case errors.Is(err, store.ErrNotFound):
		created = true
	case err != nil:
		return DNSZoneInfo{}, false, err
	}
	_, stored, err := s.deployer.DNSZoneTokenFor(ctx, zone)
	if err != nil {
		return DNSZoneInfo{}, false, err
	}
	switch {
	case in.Token != nil && *in.Token == "":
		return DNSZoneInfo{}, false, app.NewFieldError("token", "is empty; leave it out to keep the one set before")
	case in.Token == nil && !stored:
		return DNSZoneInfo{}, false, app.NewFieldError("token", "is required the first time")
	}

	if in.Token != nil {
		if err := s.deployer.SaveDNSZoneToken(ctx, zone, *in.Token); err != nil {
			return DNSZoneInfo{}, false, err
		}
	}
	now := time.Now().UTC()
	if err := s.store.SaveDNSZone(ctx, store.DNSZone{Zone: zone, Provider: in.Provider, Proxied: in.Proxied, CreatedAt: now, UpdatedAt: now}); err != nil {
		return DNSZoneInfo{}, false, err
	}
	if in.Token != nil {
		// The ID belongs to the account the token reads: find it again.
		if err := s.store.SetDNSZoneID(ctx, zone, ""); err != nil {
			return DNSZoneInfo{}, false, err
		}
	}
	if created || in.Token != nil || existing.Proxied != in.Proxied || existing.Provider != in.Provider {
		if err := s.store.ClearDNSZoneTest(ctx, zone); err != nil {
			return DNSZoneInfo{}, false, err
		}
	}
	info, err = s.DNSZone(ctx, zone)
	if err != nil {
		return DNSZoneInfo{}, false, err
	}
	return info, created, s.applyProxySetting(ctx, info.DNSZone)
}

// applyProxySetting brings the rows of a zone, and their records at the
// provider, to the zone's proxy setting. It makes no call when none differs.
func (s *Service) applyProxySetting(ctx context.Context, z store.DNSZone) error {
	rows, err := s.store.ListDNSRecordsInZone(ctx, z.Zone)
	if err != nil {
		return err
	}
	var differ []store.DNSRecord
	for _, r := range rows {
		if r.Proxied != z.Proxied {
			differ = append(differ, r)
		}
	}
	if len(differ) == 0 {
		return nil
	}
	partial := func(cause error, n int) error {
		e := dnsProviderError("", z.Zone, cause)
		e.Words = fmt.Sprintf("saved, but the old setting stays on %s; the next save tries again", plural(n, "record"))
		return e
	}
	zc, err := s.dnsClient(ctx, z)
	if err != nil {
		return partial(err, len(differ))
	}
	var cause error
	failed := 0
	for _, r := range differ {
		if err := zc.p.Update(ctx, zc.id, dns.Record{ID: r.RecordID, Proxied: z.Proxied}); err != nil && !errors.Is(err, dns.ErrRecordNotFound) {
			cause, failed = err, failed+1
			continue
		}
		// A record that is gone has nothing to change: the row follows the zone.
		if err := s.store.SetDNSRecordProxied(ctx, r.RecordID, z.Proxied); err != nil {
			cause, failed = err, failed+1
		}
	}
	for _, r := range differ {
		s.domainsChanged(r.AppID)
	}
	if failed > 0 {
		return partial(cause, failed)
	}
	return nil
}

// DeleteDNSZone forgets a zone: the rows of the records Caravana made in it
// (the records stay at the provider), then the token, then the zone. An
// unknown zone is store.ErrNotFound. Domains under it stop getting records.
func (s *Service) DeleteDNSZone(ctx context.Context, zone string) error {
	zone = normalizeHost(zone)
	if _, err := s.store.GetDNSZone(ctx, zone); err != nil {
		return err
	}
	rows, err := s.store.ListDNSRecordsInZone(ctx, zone)
	if err != nil {
		return err
	}
	if err := s.store.DeleteDNSRecordsInZone(ctx, zone); err != nil {
		return err
	}
	if err := s.deployer.DeleteDNSZoneToken(ctx, zone); err != nil {
		return err
	}
	if err := s.store.DeleteDNSZone(ctx, zone); err != nil {
		return err
	}
	for _, r := range rows {
		s.domainsChanged(r.AppID)
	}
	return nil
}

// TestDNSZone asks the provider for the zone and its records, stores the
// outcome with the time and returns the records as they are now. A refusal or
// an unreachable provider is an outcome, not an error: the zone comes back with
// LastTest.OK false and the reason in Caravana's words. An unknown zone is
// store.ErrNotFound.
func (s *Service) TestDNSZone(ctx context.Context, zone string) (DNSZoneTestResult, error) {
	zone = normalizeHost(zone)
	z, err := s.store.GetDNSZone(ctx, zone)
	if err != nil {
		return DNSZoneTestResult{}, err
	}
	rows, err := s.store.ListDNSRecordsInZone(ctx, zone)
	if err != nil {
		return DNSZoneTestResult{}, err
	}
	made := map[string]store.DNSRecord{}
	for _, r := range rows {
		made[r.RecordID] = r
	}

	outcome := store.DNSZoneTest{At: time.Now().UTC()}
	var listed []DNSZoneRecord
	zc, err := s.dnsClient(ctx, z)
	var recs []dns.Record
	if err == nil {
		recs, err = zc.p.Records(ctx, zc.id, "")
	}
	if err != nil {
		outcome.Message = dnsProviderError("", zone, err).Words
	} else {
		mine := 0
		for _, r := range recs {
			row, ok := made[r.ID]
			if ok {
				mine++
			}
			listed = append(listed, DNSZoneRecord{Record: r, Managed: ok, App: row.AppID})
		}
		outcome.OK, outcome.Count = true, len(recs)
		outcome.Message = fmt.Sprintf("%s, %d made by Caravana", plural(len(recs), "record"), mine)
	}
	if err := s.store.SaveDNSZoneTest(ctx, zone, outcome); err != nil {
		return DNSZoneTestResult{}, err
	}
	info, err := s.DNSZone(ctx, zone)
	return DNSZoneTestResult{Zone: info, Records: listed}, err
}
