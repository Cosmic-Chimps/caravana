package store

import (
	"context"
	"fmt"
	"time"
)

// DNSRecord is a DNS record Caravana made at a provider (feature 048). Only
// these are ever changed or removed. A host with an A and an AAAA record has
// two rows.
type DNSRecord struct {
	Host string
	// Type is "A" or "AAAA".
	Type string
	Zone string
	// AppID is the app whose domain the record serves.
	AppID string
	// RecordID is the provider's ID of the record.
	RecordID  string
	Content   string
	Proxied   bool
	CreatedAt time.Time
}

const dnsRecordColumns = "record_id, host, type, zone, app_id, content, proxied, created_at"

// InsertDNSRecord saves a record, replacing the row of the same provider ID (a
// record adopted from another app).
func (s *Store) InsertDNSRecord(ctx context.Context, r DNSRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dns_records (`+dnsRecordColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (record_id) DO UPDATE SET host = excluded.host, type = excluded.type, zone = excluded.zone,
			app_id = excluded.app_id, content = excluded.content, proxied = excluded.proxied,
			created_at = excluded.created_at`,
		r.RecordID, r.Host, r.Type, r.Zone, r.AppID, r.Content, boolInt(r.Proxied), formatTime(r.CreatedAt))
	if err != nil {
		return fmt.Errorf("save dns record %s %s: %w", r.Type, r.Host, err)
	}
	return nil
}

// ListDNSRecords returns every record, ordered by host and type.
func (s *Store) ListDNSRecords(ctx context.Context) ([]DNSRecord, error) {
	return s.queryDNSRecords(ctx, "")
}

// ListDNSRecordsInZone returns the records of one zone.
func (s *Store) ListDNSRecordsInZone(ctx context.Context, zone string) ([]DNSRecord, error) {
	return s.queryDNSRecords(ctx, "WHERE zone = ?", zone)
}

// ListDNSRecordsOfApp returns the records of one app.
func (s *Store) ListDNSRecordsOfApp(ctx context.Context, appID string) ([]DNSRecord, error) {
	return s.queryDNSRecords(ctx, "WHERE app_id = ?", appID)
}

func (s *Store) queryDNSRecords(ctx context.Context, where string, args ...any) ([]DNSRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+dnsRecordColumns+" FROM dns_records "+where+" ORDER BY host, type, content", args...)
	if err != nil {
		return nil, fmt.Errorf("list dns records: %w", err)
	}
	defer rows.Close()
	var out []DNSRecord
	for rows.Next() {
		var (
			r       DNSRecord
			proxied int
			created string
		)
		if err := rows.Scan(&r.RecordID, &r.Host, &r.Type, &r.Zone, &r.AppID, &r.Content, &proxied, &created); err != nil {
			return nil, fmt.Errorf("list dns records: %w", err)
		}
		r.Proxied = proxied == 1
		if r.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("list dns records: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list dns records: %w", err)
	}
	return out, nil
}

// DeleteDNSRecord drops the row of a provider's record ID; one that isn't
// there is not an error.
func (s *Store) DeleteDNSRecord(ctx context.Context, recordID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM dns_records WHERE record_id = ?", recordID); err != nil {
		return fmt.Errorf("delete dns record row: %w", err)
	}
	return nil
}

// DeleteDNSRecordsInZone drops every row of a zone (the records stay at the
// provider: the zone is being forgotten).
func (s *Store) DeleteDNSRecordsInZone(ctx context.Context, zone string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM dns_records WHERE zone = ?", zone); err != nil {
		return fmt.Errorf("delete dns records of zone %q: %w", zone, err)
	}
	return nil
}

// SetDNSRecordProxied records the proxy setting of one record's row, after the
// provider took it (a zone's records change one by one, so one failure leaves
// the others as they are).
func (s *Store) SetDNSRecordProxied(ctx context.Context, recordID string, proxied bool) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE dns_records SET proxied = ? WHERE record_id = ?", boolInt(proxied), recordID); err != nil {
		return fmt.Errorf("update dns record row: %w", err)
	}
	return nil
}
