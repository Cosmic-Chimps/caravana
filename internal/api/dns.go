package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// DNS providers (feature 048): the zones Caravana may make records in, the
// records it made, and the errors a deploy gets when a record can't be made.
// No answer here has a property for a token.

const (
	codeDNSRecordExists        = "dns_record_exists"
	codeDNSAddressUnknown      = "dns_address_unknown"
	codeDNSProviderRefused     = "dns_provider_refused"
	codeDNSProviderUnavailable = "dns_provider_unavailable"
	codeDNSZoneNotFound        = "dns_zone_not_found"
)

// dnsError maps the refusals of DNS records and zones to their codes; mapError
// calls it.
func dnsError(err error) *Error {
	var exists *service.DNSRecordExistsError
	var prov *service.DNSProviderError
	switch {
	case errors.As(err, &exists):
		return &Error{Status: http.StatusConflict, Code: codeDNSRecordExists, Field: exists.Field, Message: err.Error()}
	case errors.Is(err, service.ErrHostAddressUnknown):
		return &Error{Status: http.StatusConflict, Code: codeDNSAddressUnknown, Message: err.Error()}
	case errors.As(err, &prov) && errors.Is(err, dns.ErrRefused):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeDNSProviderRefused, Field: prov.Field, Message: err.Error()}
	case errors.As(err, &prov):
		return &Error{Status: http.StatusBadGateway, Code: codeDNSProviderUnavailable, Field: prov.Field, Message: err.Error()}
	}
	return nil
}

// dnsZoneNotFound is the 404 of the zone routes for a zone that isn't one;
// other errors pass through for mapError.
func dnsZoneNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return &Error{Status: http.StatusNotFound, Code: codeDNSZoneNotFound, Message: err.Error()}
	}
	return err
}

// dnsZoneTestJSON is the outcome of the last test (schema DNSZoneTestOutcome).
type dnsZoneTestJSON struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	Count   int       `json:"count"`
}

// dnsZoneJSON is a zone (schema DNSZone).
type dnsZoneJSON struct {
	Zone      string           `json:"zone"`
	Provider  string           `json:"provider"`
	Proxied   bool             `json:"proxied"`
	Records   int              `json:"records"`
	LastTest  *dnsZoneTestJSON `json:"lastTest,omitempty"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

func toDNSTestJSON(t *store.DNSZoneTest) *dnsZoneTestJSON {
	if t == nil {
		return nil
	}
	return &dnsZoneTestJSON{At: t.At, OK: t.OK, Message: t.Message, Count: t.Count}
}

func toDNSZoneJSON(z service.DNSZoneInfo) dnsZoneJSON {
	return dnsZoneJSON{Zone: z.Zone, Provider: z.Provider, Proxied: z.Proxied, Records: z.Records,
		LastTest: toDNSTestJSON(z.LastTest), CreatedAt: z.CreatedAt, UpdatedAt: z.UpdatedAt}
}

// listDNSZones answers GET /v1/dns/zones.
func (s *server) listDNSZones(w http.ResponseWriter, r *http.Request) error {
	zones, err := s.svc.DNSZones(r.Context())
	if err != nil {
		return err
	}
	out := make([]dnsZoneJSON, 0, len(zones))
	for _, z := range zones {
		out = append(out, toDNSZoneJSON(z))
	}
	writeJSON(w, http.StatusOK, struct {
		Zones []dnsZoneJSON `json:"zones"`
	}{out})
	return nil
}

// putDNSZone answers PUT /v1/dns/zones/{zone}: 201 for a new zone, 200 for one
// replaced. A body without a token keeps the one stored before. When the
// zone's records can't all be brought to the new proxy setting the zone is
// saved and the answer is the provider's error, "saved, but ...".
func (s *server) putDNSZone(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Provider string  `json:"provider"`
		Proxied  bool    `json:"proxied"`
		Token    *string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	info, created, err := s.svc.SaveDNSZone(r.Context(), service.DNSZoneInput{
		Zone: r.PathValue("zone"), Provider: in.Provider, Proxied: in.Proxied, Token: in.Token,
	})
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toDNSZoneJSON(info))
	return nil
}

// deleteDNSZone answers DELETE /v1/dns/zones/{zone}. The records Caravana made
// in the zone stay at the provider.
func (s *server) deleteDNSZone(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteDNSZone(r.Context(), r.PathValue("zone")); err != nil {
		return dnsZoneNotFound(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// dnsProviderRecordJSON is a record as the provider has it now (schema
// DNSProviderRecord).
type dnsProviderRecordJSON struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
	TTL     int    `json:"ttl"`
	Managed bool   `json:"managed"`
	// App is the app a record Caravana made is for; empty otherwise.
	App string `json:"app"`
}

// testDNSZone answers POST /v1/dns/zones/{zone}/test. A refusal is an outcome
// in lastTest, so the answer is 200 either way; the records are the provider's
// now and are in this answer only. Sent with no-store.
func (s *server) testDNSZone(w http.ResponseWriter, r *http.Request) error {
	res, err := s.svc.TestDNSZone(r.Context(), r.PathValue("zone"))
	if err != nil {
		return dnsZoneNotFound(err)
	}
	out := struct {
		Zone     string                  `json:"zone"`
		LastTest *dnsZoneTestJSON        `json:"lastTest"`
		Records  []dnsProviderRecordJSON `json:"records"`
	}{Zone: res.Zone.Zone, LastTest: toDNSTestJSON(res.Zone.LastTest), Records: make([]dnsProviderRecordJSON, 0, len(res.Records))}
	for _, rec := range res.Records {
		out.Records = append(out.Records, dnsProviderRecordJSON{ID: rec.ID, Type: rec.Type, Name: rec.Name, Content: rec.Content,
			Proxied: rec.Proxied, Comment: rec.Comment, TTL: rec.TTL, Managed: rec.Managed, App: rec.App})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
	return nil
}

// dnsRecordMadeJSON is a record Caravana made (schema DNSRecordMade).
type dnsRecordMadeJSON struct {
	Host      string    `json:"host"`
	Type      string    `json:"type"`
	Zone      string    `json:"zone"`
	App       string    `json:"app"`
	Content   string    `json:"content"`
	Proxied   bool      `json:"proxied"`
	CreatedAt time.Time `json:"createdAt"`
}

// listDNSRecords answers GET /v1/dns/records: the records Caravana made, from
// its own table (no provider call).
func (s *server) listDNSRecords(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.svc.DNSRecords(r.Context())
	if err != nil {
		return err
	}
	out := make([]dnsRecordMadeJSON, 0, len(rows))
	for _, rec := range rows {
		out = append(out, dnsRecordMadeJSON{Host: rec.Host, Type: rec.Type, Zone: rec.Zone, App: rec.AppID,
			Content: rec.Content, Proxied: rec.Proxied, CreatedAt: rec.CreatedAt})
	}
	writeJSON(w, http.StatusOK, struct {
		Records []dnsRecordMadeJSON `json:"records"`
	}{out})
	return nil
}

// dnsManagedRecordJSON is the fact that Caravana made a domain's record
// (schema DNSManagedRecord).
type dnsManagedRecordJSON struct {
	Zone      string    `json:"zone"`
	Provider  string    `json:"provider"`
	Proxied   bool      `json:"proxied"`
	CreatedAt time.Time `json:"createdAt"`
}

func toManagedRecordJSON(m *service.ManagedRecord) *dnsManagedRecordJSON {
	if m == nil {
		return nil
	}
	return &dnsManagedRecordJSON{Zone: m.Zone, Provider: m.Provider, Proxied: m.Proxied, CreatedAt: m.CreatedAt}
}
