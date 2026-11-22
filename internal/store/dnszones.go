package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DNSZoneTest is the outcome of the last Test of a zone.
type DNSZoneTest struct {
	At time.Time
	// OK is true when the provider listed the zone. False means it refused or
	// couldn't be reached; Message says which, in Caravana's words.
	OK      bool
	Message string
	// Count is how many records a successful test listed.
	Count int
}

// DNSZone is a zone Caravana may make DNS records in (feature 048), without
// its token, which lives in a Nomad Variable (constitution VII).
type DNSZone struct {
	// Zone is a lowercase domain name, unique.
	Zone     string
	Provider string
	// Proxied is the proxy setting of the records Caravana makes.
	Proxied bool
	// ZoneID is the provider's ID of the zone, "" until learned.
	ZoneID    string
	CreatedAt time.Time
	UpdatedAt time.Time
	// LastTest is nil until the zone was tested, and again after its token or
	// proxy setting changed.
	LastTest *DNSZoneTest
}

const dnsZoneColumns = "zone, provider, proxied, zone_id, created_at, updated_at, tested_at, test_ok, test_message, test_count"

// SaveDNSZone adds a zone, or replaces the provider and proxy setting of the
// one with z.Zone. The creation time, the provider's ID and the last test of an
// existing one are kept: the service clears them when something changed.
func (s *Store) SaveDNSZone(ctx context.Context, z DNSZone) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dns_zones (zone, provider, proxied, zone_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (zone) DO UPDATE SET provider = excluded.provider, proxied = excluded.proxied,
			updated_at = excluded.updated_at`,
		z.Zone, z.Provider, boolInt(z.Proxied), z.ZoneID, formatTime(z.CreatedAt), formatTime(z.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save dns zone %q: %w", z.Zone, err)
	}
	return nil
}

// ListDNSZones returns every zone, ordered by name.
func (s *Store) ListDNSZones(ctx context.Context) ([]DNSZone, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+dnsZoneColumns+" FROM dns_zones ORDER BY zone")
	if err != nil {
		return nil, fmt.Errorf("list dns zones: %w", err)
	}
	defer rows.Close()
	var out []DNSZone
	for rows.Next() {
		z, err := scanDNSZone(rows)
		if err != nil {
			return nil, fmt.Errorf("list dns zones: %w", err)
		}
		out = append(out, z)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list dns zones: %w", err)
	}
	return out, nil
}

// GetDNSZone returns the zone with this name, or ErrNotFound.
func (s *Store) GetDNSZone(ctx context.Context, zone string) (DNSZone, error) {
	z, err := scanDNSZone(s.db.QueryRowContext(ctx, "SELECT "+dnsZoneColumns+" FROM dns_zones WHERE zone = ?", zone))
	if errors.Is(err, sql.ErrNoRows) {
		return DNSZone{}, fmt.Errorf("dns zone %q: %w", zone, ErrNotFound)
	}
	if err != nil {
		return DNSZone{}, fmt.Errorf("get dns zone %q: %w", zone, err)
	}
	return z, nil
}

// DeleteDNSZone removes the zone. Its records' rows are the service's to drop
// first (DeleteDNSRecordsInZone). An unknown zone is ErrNotFound.
func (s *Store) DeleteDNSZone(ctx context.Context, zone string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM dns_zones WHERE zone = ?", zone)
	if err != nil {
		return fmt.Errorf("delete dns zone %q: %w", zone, err)
	}
	return affectedOrNotFound(res, "dns zone "+zone)
}

// SaveDNSZoneTest records the outcome of testing a zone, replacing the one
// before it. An unknown zone is ErrNotFound.
func (s *Store) SaveDNSZoneTest(ctx context.Context, zone string, t DNSZoneTest) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE dns_zones SET tested_at = ?, test_ok = ?, test_message = ?, test_count = ? WHERE zone = ?",
		formatTime(t.At), boolInt(t.OK), t.Message, t.Count, zone)
	if err != nil {
		return fmt.Errorf("save test of dns zone %q: %w", zone, err)
	}
	return affectedOrNotFound(res, "dns zone "+zone)
}

// ClearDNSZoneTest forgets the last test of a zone, because its token or proxy
// setting changed. An unknown zone is ErrNotFound.
func (s *Store) ClearDNSZoneTest(ctx context.Context, zone string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE dns_zones SET tested_at = NULL, test_ok = NULL, test_message = '', test_count = 0 WHERE zone = ?", zone)
	if err != nil {
		return fmt.Errorf("clear test of dns zone %q: %w", zone, err)
	}
	return affectedOrNotFound(res, "dns zone "+zone)
}

// SetDNSZoneID stores the provider's ID of the zone ("" forgets it). An unknown
// zone is ErrNotFound.
func (s *Store) SetDNSZoneID(ctx context.Context, zone, id string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE dns_zones SET zone_id = ? WHERE zone = ?", id, zone)
	if err != nil {
		return fmt.Errorf("set id of dns zone %q: %w", zone, err)
	}
	return affectedOrNotFound(res, "dns zone "+zone)
}

func scanDNSZone(row scanner) (DNSZone, error) {
	var (
		z                DNSZone
		proxied          int
		created, updated string
		testAt           sql.NullString
		testOK           sql.NullInt64
		message          string
		count            int
	)
	if err := row.Scan(&z.Zone, &z.Provider, &proxied, &z.ZoneID, &created, &updated, &testAt, &testOK, &message, &count); err != nil {
		return DNSZone{}, err
	}
	z.Proxied = proxied == 1
	var err error
	if z.CreatedAt, err = parseTime(created); err != nil {
		return DNSZone{}, err
	}
	if z.UpdatedAt, err = parseTime(updated); err != nil {
		return DNSZone{}, err
	}
	if testAt.Valid {
		at, err := parseTime(testAt.String)
		if err != nil {
			return DNSZone{}, err
		}
		z.LastTest = &DNSZoneTest{At: at, OK: testOK.Int64 == 1, Message: message, Count: count}
	}
	return z, nil
}
