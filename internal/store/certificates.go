package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ACMEIssuer is the custom ACME issuer of the installation (feature 034): the one the owner set in
// Settings › Certificates instead of the installer's. Everything here is public. The EAB HMAC key
// is in the Nomad Variable caravana/acme and never in SQLite; EABKeySet only says there is one.
type ACMEIssuer struct {
	Server    string // the directory URL
	Email     string
	EABKeyID  string // the key ID, not the key
	EABKeySet bool   // an HMAC key is stored in the variable
	CA        string // PEM of the CA that signs the issuer's own HTTPS certificate; empty for none
	UpdatedAt time.Time
}

// ACMEIssuer returns the custom issuer. ok is false when there is none (the installer's issuer).
func (s *Store) ACMEIssuer(ctx context.Context) (issuer ACMEIssuer, ok bool, err error) {
	var set int
	var updated sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT acme_server, acme_email, acme_eab_kid, acme_eab_set, acme_ca, acme_updated_at
		FROM certificate_settings WHERE id = 1`).
		Scan(&issuer.Server, &issuer.Email, &issuer.EABKeyID, &set, &issuer.CA, &updated)
	if err != nil {
		return ACMEIssuer{}, false, fmt.Errorf("get the ACME issuer: %w", err)
	}
	if issuer.Server == "" {
		return ACMEIssuer{}, false, nil
	}
	issuer.EABKeySet = set != 0
	if updated.Valid {
		if issuer.UpdatedAt, err = parseTime(updated.String); err != nil {
			return ACMEIssuer{}, false, err
		}
	}
	return issuer, true, nil
}

// SetACMEIssuer saves the custom issuer, replacing the one there is. The server must not be empty:
// ClearACMEIssuer is how the installer's issuer comes back. UpdatedAt is set by the store.
func (s *Store) SetACMEIssuer(ctx context.Context, i ACMEIssuer) error {
	if i.Server == "" {
		return errors.New("set the ACME issuer: the server is empty")
	}
	set := 0
	if i.EABKeySet {
		set = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE certificate_settings
		SET acme_server = ?, acme_email = ?, acme_eab_kid = ?, acme_eab_set = ?, acme_ca = ?, acme_updated_at = ?
		WHERE id = 1`,
		i.Server, i.Email, i.EABKeyID, set, i.CA, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("set the ACME issuer: %w", err)
	}
	return nil
}

// ClearACMEIssuer forgets the custom issuer: the installer's applies again. Clearing none is not an
// error.
func (s *Store) ClearACMEIssuer(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE certificate_settings
		SET acme_server = '', acme_email = '', acme_eab_kid = '', acme_eab_set = 0, acme_ca = '', acme_updated_at = NULL
		WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("clear the ACME issuer: %w", err)
	}
	return nil
}

// UploadedCertificate is what is public about a certificate the owner uploaded. The chain and the
// private key are only in the Nomad Variable caravana/certificates/<ID>.
type UploadedCertificate struct {
	ID                  string // crt_ and 12 lowercase hex
	Names               []string
	Subject, Issuer     string
	NotBefore, NotAfter time.Time
	Fingerprint         string // SHA-256 of the leaf, lower-case hex
	KeyType             string // "RSA 2048", "ECDSA P-256"
	Trusted             bool   // chains to a root the server trusts
	UploadedAt          time.Time
}

const uploadedCertificateColumns = `id, names, subject, issuer, not_before, not_after, fingerprint, key_type, trusted, uploaded_at`

// InsertUploadedCertificate saves a certificate. An ID that exists is an error.
func (s *Store) InsertUploadedCertificate(ctx context.Context, c UploadedCertificate) error {
	names, err := json.Marshal(nonNil(c.Names))
	if err != nil {
		return fmt.Errorf("save certificate %q: %w", c.ID, err)
	}
	trusted := 0
	if c.Trusted {
		trusted = 1
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO uploaded_certificates (`+uploadedCertificateColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, string(names), c.Subject, c.Issuer, formatTime(c.NotBefore), formatTime(c.NotAfter),
		c.Fingerprint, c.KeyType, trusted, formatTime(c.UploadedAt))
	if err != nil {
		return fmt.Errorf("save certificate %q: %w", c.ID, err)
	}
	return nil
}

// ListUploadedCertificates returns every uploaded certificate, oldest upload first (then by ID).
func (s *Store) ListUploadedCertificates(ctx context.Context) ([]UploadedCertificate, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+uploadedCertificateColumns+" FROM uploaded_certificates ORDER BY uploaded_at, id")
	if err != nil {
		return nil, fmt.Errorf("list uploaded certificates: %w", err)
	}
	defer rows.Close()
	var out []UploadedCertificate
	for rows.Next() {
		c, err := scanUploadedCertificate(rows)
		if err != nil {
			return nil, fmt.Errorf("list uploaded certificates: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list uploaded certificates: %w", err)
	}
	return out, nil
}

// GetUploadedCertificate returns one certificate, or ErrNotFound.
func (s *Store) GetUploadedCertificate(ctx context.Context, id string) (UploadedCertificate, error) {
	c, err := scanUploadedCertificate(s.db.QueryRowContext(ctx,
		"SELECT "+uploadedCertificateColumns+" FROM uploaded_certificates WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return UploadedCertificate{}, fmt.Errorf("certificate %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return UploadedCertificate{}, fmt.Errorf("get certificate %q: %w", id, err)
	}
	return c, nil
}

// DeleteUploadedCertificate forgets a certificate. One that isn't there is not an error.
func (s *Store) DeleteUploadedCertificate(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM uploaded_certificates WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete certificate %q: %w", id, err)
	}
	return nil
}

// CountUploadedCertificates says how many certificates are uploaded.
func (s *Store) CountUploadedCertificates(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM uploaded_certificates").Scan(&n); err != nil {
		return 0, fmt.Errorf("count uploaded certificates: %w", err)
	}
	return n, nil
}

func scanUploadedCertificate(row scanner) (UploadedCertificate, error) {
	var c UploadedCertificate
	var names, notBefore, notAfter, uploaded string
	var trusted int
	if err := row.Scan(&c.ID, &names, &c.Subject, &c.Issuer, &notBefore, &notAfter, &c.Fingerprint, &c.KeyType, &trusted, &uploaded); err != nil {
		return UploadedCertificate{}, err
	}
	if err := json.Unmarshal([]byte(names), &c.Names); err != nil {
		return UploadedCertificate{}, fmt.Errorf("bad stored names of %q: %w", c.ID, err)
	}
	var err error
	if c.NotBefore, err = parseTime(notBefore); err != nil {
		return UploadedCertificate{}, err
	}
	if c.NotAfter, err = parseTime(notAfter); err != nil {
		return UploadedCertificate{}, err
	}
	if c.UploadedAt, err = parseTime(uploaded); err != nil {
		return UploadedCertificate{}, err
	}
	c.Trusted = trusted != 0
	return c, nil
}
