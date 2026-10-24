package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// DomainCheckRecord is what the last check found for one host: the cache the
// Domains page and the needs-you list read. State and the statuses are the
// words of package edge; the store keeps them as text, and the table's CHECKs
// refuse any other.
type DomainCheckRecord struct {
	Host, AppID, Service string
	State                string
	DNSStatus            string
	DNSSeen              []string // addresses the resolvers answered
	DNSExpected          []string // this host's addresses at check time
	CertStatus           string
	CertIssuer           string
	CertNotAfter         *time.Time // nil when there is none
	CheckedAt            time.Time
}

const domainCheckColumns = `host, app_id, service, state, dns_status, dns_seen, dns_expected,
	cert_status, cert_issuer, cert_not_after, checked_at`

// UpsertDomainCheck saves the check of a host, replacing the one there is.
func (s *Store) UpsertDomainCheck(ctx context.Context, r DomainCheckRecord) error {
	seen, err := json.Marshal(nonNil(r.DNSSeen))
	if err != nil {
		return fmt.Errorf("save check of %q: %w", r.Host, err)
	}
	expected, err := json.Marshal(nonNil(r.DNSExpected))
	if err != nil {
		return fmt.Errorf("save check of %q: %w", r.Host, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO domain_checks (`+domainCheckColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (host) DO UPDATE SET app_id = excluded.app_id, service = excluded.service,
			state = excluded.state, dns_status = excluded.dns_status, dns_seen = excluded.dns_seen,
			dns_expected = excluded.dns_expected, cert_status = excluded.cert_status,
			cert_issuer = excluded.cert_issuer, cert_not_after = excluded.cert_not_after,
			checked_at = excluded.checked_at`,
		r.Host, r.AppID, r.Service, r.State, r.DNSStatus, string(seen), string(expected),
		r.CertStatus, r.CertIssuer, optionalTime(r.CertNotAfter), formatTime(r.CheckedAt))
	if err != nil {
		return fmt.Errorf("save check of %q: %w", r.Host, err)
	}
	return nil
}

// ListDomainChecks returns every host's check, by host.
func (s *Store) ListDomainChecks(ctx context.Context) ([]DomainCheckRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+domainCheckColumns+" FROM domain_checks ORDER BY host")
	if err != nil {
		return nil, fmt.Errorf("list domain checks: %w", err)
	}
	defer rows.Close()
	var out []DomainCheckRecord
	for rows.Next() {
		r, err := scanDomainCheck(rows)
		if err != nil {
			return nil, fmt.Errorf("list domain checks: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list domain checks: %w", err)
	}
	return out, nil
}

// GetDomainCheck returns the check of a host, or ErrNotFound.
func (s *Store) GetDomainCheck(ctx context.Context, host string) (DomainCheckRecord, error) {
	r, err := scanDomainCheck(s.db.QueryRowContext(ctx,
		"SELECT "+domainCheckColumns+" FROM domain_checks WHERE host = ?", host))
	if errors.Is(err, sql.ErrNoRows) {
		return DomainCheckRecord{}, fmt.Errorf("check of %q: %w", host, ErrNotFound)
	}
	if err != nil {
		return DomainCheckRecord{}, fmt.Errorf("get check of %q: %w", host, err)
	}
	return r, nil
}

// DeleteDomainCheck forgets a host's check. A host without one is not an error.
func (s *Store) DeleteDomainCheck(ctx context.Context, host string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM domain_checks WHERE host = ?", host); err != nil {
		return fmt.Errorf("delete check of %q: %w", host, err)
	}
	return nil
}

// DeleteDomainChecks forgets every check of an app's hosts.
func (s *Store) DeleteDomainChecks(ctx context.Context, appID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM domain_checks WHERE app_id = ?", appID); err != nil {
		return fmt.Errorf("delete checks of %q: %w", appID, err)
	}
	return nil
}

func scanDomainCheck(row scanner) (DomainCheckRecord, error) {
	var r DomainCheckRecord
	var seen, expected, checked string
	var notAfter sql.NullString
	err := row.Scan(&r.Host, &r.AppID, &r.Service, &r.State, &r.DNSStatus, &seen, &expected,
		&r.CertStatus, &r.CertIssuer, &notAfter, &checked)
	if err != nil {
		return DomainCheckRecord{}, err
	}
	if err := json.Unmarshal([]byte(seen), &r.DNSSeen); err != nil {
		return DomainCheckRecord{}, fmt.Errorf("bad stored addresses of %q: %w", r.Host, err)
	}
	if err := json.Unmarshal([]byte(expected), &r.DNSExpected); err != nil {
		return DomainCheckRecord{}, fmt.Errorf("bad stored addresses of %q: %w", r.Host, err)
	}
	if r.CertNotAfter, err = parseOptionalTime(notAfter); err != nil {
		return DomainCheckRecord{}, err
	}
	if r.CheckedAt, err = parseTime(checked); err != nil {
		return DomainCheckRecord{}, err
	}
	return r, nil
}

// nonNil makes a nil list store as [] and not as null.
func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// TeamPasswordRecord is the stored side of a Team-only domain's password: its
// hash, never the password.
type TeamPasswordRecord struct {
	Host, AppID string
	Hash        string
	CreatedAt   time.Time
}

// SetTeamPassword saves the hash for a host, replacing the one there is. at is
// the moment of this rotation.
func (s *Store) SetTeamPassword(ctx context.Context, host, appID, hash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO team_passwords (host, app_id, hash, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (host) DO UPDATE SET app_id = excluded.app_id, hash = excluded.hash,
			created_at = excluded.created_at`,
		host, appID, hash, formatTime(at))
	if err != nil {
		return fmt.Errorf("save team password of %q: %w", host, err)
	}
	return nil
}

// TeamPassword returns a host's record, or ErrNotFound.
func (s *Store) TeamPassword(ctx context.Context, host string) (TeamPasswordRecord, error) {
	r, err := scanTeamPassword(s.db.QueryRowContext(ctx,
		"SELECT host, app_id, hash, created_at FROM team_passwords WHERE host = ?", host))
	if errors.Is(err, sql.ErrNoRows) {
		return TeamPasswordRecord{}, fmt.Errorf("team password of %q: %w", host, ErrNotFound)
	}
	if err != nil {
		return TeamPasswordRecord{}, fmt.Errorf("get team password of %q: %w", host, err)
	}
	return r, nil
}

// TeamPasswords returns every record, by host.
func (s *Store) TeamPasswords(ctx context.Context) ([]TeamPasswordRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT host, app_id, hash, created_at FROM team_passwords ORDER BY host")
	if err != nil {
		return nil, fmt.Errorf("list team passwords: %w", err)
	}
	defer rows.Close()
	var out []TeamPasswordRecord
	for rows.Next() {
		r, err := scanTeamPassword(rows)
		if err != nil {
			return nil, fmt.Errorf("list team passwords: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list team passwords: %w", err)
	}
	return out, nil
}

// DeleteTeamPassword removes a host's record. A host without one is not an
// error.
func (s *Store) DeleteTeamPassword(ctx context.Context, host string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM team_passwords WHERE host = ?", host); err != nil {
		return fmt.Errorf("delete team password of %q: %w", host, err)
	}
	return nil
}

// DeleteTeamPasswords removes every record of an app's hosts.
func (s *Store) DeleteTeamPasswords(ctx context.Context, appID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM team_passwords WHERE app_id = ?", appID); err != nil {
		return fmt.Errorf("delete team passwords of %q: %w", appID, err)
	}
	return nil
}

func scanTeamPassword(row scanner) (TeamPasswordRecord, error) {
	var r TeamPasswordRecord
	var created string
	if err := row.Scan(&r.Host, &r.AppID, &r.Hash, &created); err != nil {
		return TeamPasswordRecord{}, err
	}
	var err error
	if r.CreatedAt, err = parseTime(created); err != nil {
		return TeamPasswordRecord{}, err
	}
	return r, nil
}

// WarnBeforeDays are the values the certificate warning window may take.
var WarnBeforeDays = []int{7, 14, 30}

// CertificateSettings returns how many days before a certificate's expiry
// Caravana warns (14 until changed) and when that was last set.
func (s *Store) CertificateSettings(ctx context.Context) (warnBeforeDays int, updatedAt time.Time, err error) {
	var updated string
	err = s.db.QueryRowContext(ctx,
		"SELECT warn_before_days, updated_at FROM certificate_settings WHERE id = 1").Scan(&warnBeforeDays, &updated)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("get certificate settings: %w", err)
	}
	if updatedAt, err = parseTime(updated); err != nil {
		return 0, time.Time{}, err
	}
	return warnBeforeDays, updatedAt, nil
}

// SetWarnBeforeDays sets the warning window: 7, 14 or 30 days.
func (s *Store) SetWarnBeforeDays(ctx context.Context, days int) error {
	if !slices.Contains(WarnBeforeDays, days) {
		return fmt.Errorf("warn before %d days: must be 7, 14 or 30", days)
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE certificate_settings SET warn_before_days = ?, updated_at = ? WHERE id = 1",
		days, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("set certificate settings: %w", err)
	}
	return nil
}
