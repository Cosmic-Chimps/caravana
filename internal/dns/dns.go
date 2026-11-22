// Package dns is the seam to a DNS provider (feature 048): it knows zones
// and records and nothing of apps, deployments or Caravana's store. The one
// implementation is Cloudflare's (cloudflare.go); package dnstest has the fakes.
//
// No error of this package holds a response body, a token or a URL: they are
// the sentinels below, wrapped with the operation that failed.
package dns

import (
	"context"
	"errors"
	"strings"
)

// Record is one DNS record as the provider has it.
type Record struct {
	// ID is the provider's own ID for the record; empty before Create.
	ID string
	// Type is "A", "AAAA", "CNAME", "TXT"...
	Type string
	// Name is the full host: "web.example.com".
	Name    string
	Content string
	// Proxied is Cloudflare's orange cloud.
	Proxied bool
	Comment string
	// TTL is in seconds; 1 means automatic.
	TTL int
}

// Provider is what Caravana asks of a DNS provider, for one token. zoneID is
// the provider's ID for a zone, found by FindZone.
type Provider interface {
	// FindZone returns the provider's ID of the zone with this name, or an
	// error that is ErrZoneNotFound, ErrRefused or ErrUnavailable.
	FindZone(ctx context.Context, zone string) (zoneID string, err error)
	// Records lists the zone's records with this name; an empty name lists
	// every record of the zone.
	Records(ctx context.Context, zoneID, name string) ([]Record, error)
	// Create makes a record and returns it with its ID.
	Create(ctx context.Context, zoneID string, r Record) (Record, error)
	// Update changes the proxy setting of a record Caravana made.
	Update(ctx context.Context, zoneID string, r Record) error
	// Delete removes a record. A record already gone is not an error.
	Delete(ctx context.Context, zoneID, recordID string) error
}

// Factory builds the Provider of a kind ("cloudflare") for a token.
type Factory func(kind, token string) (Provider, error)

// The classes of failure.
var (
	// ErrRefused: the token is wrong or lacks Zone > DNS > Edit, or the
	// provider turned the request down.
	ErrRefused = errors.New("the provider refused the request")
	// ErrZoneNotFound: the token has no zone of that name.
	ErrZoneNotFound = errors.New("the provider has no such zone")
	// ErrUnavailable: a network error, a timeout, rate limiting or a 5xx.
	ErrUnavailable = errors.New("the provider could not be reached")
	// ErrRecordNotFound: Update of a record that is gone.
	ErrRecordNotFound = errors.New("the provider has no such record")
)

// ProviderError says which operation failed with which class. Its text never
// holds a response body, a token or a URL.
type ProviderError struct {
	Op  string
	Err error
}

func (e *ProviderError) Error() string { return e.Op + ": " + e.Err.Error() }

// Unwrap makes errors.Is find the class.
func (e *ProviderError) Unwrap() error { return e.Err }

// ZoneOf returns the longest zone that is host or a parent of it (after a
// dot), or false when none is.
func ZoneOf(host string, zones []string) (string, bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	best := ""
	for _, z := range zones {
		z = strings.ToLower(z)
		if (host == z || strings.HasSuffix(host, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best, best != ""
}
