package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PasskeyRecord is a passkey as stored (feature 033). Everything in it is
// public: the credential's public key and counter are verifiers, like the
// password hash, so nothing here grants access if the file is read.
type PasskeyRecord struct {
	ID                string // ours: 26 lowercase base32 characters, shown in lists and logs
	PersonID          string // whose passkey it is
	UserHandle        []byte // the WebAuthn user.id of that person
	CredentialID      []byte // the authenticator's ID for it; never logged
	RPID              string // the domain it was made at
	PublicKey         []byte // COSE_Key as the authenticator gave it
	AttestationType   string
	AttestationFormat string
	AAGUID            []byte
	SignCount         uint32
	Transports        []string
	UserPresent       bool
	UserVerified      bool
	BackupEligible    bool
	BackupState       bool
	Name              string
	CreatedAt         time.Time
	LastUsedAt        *time.Time // nil: never used
}

const passkeyColumns = "id, person_id, user_handle, credential_id, rp_id, public_key, attestation_type, attestation_format, " +
	"aaguid, sign_count, transports, user_present, user_verified, backup_eligible, backup_state, name, created_at, last_used_at"

// InsertPasskey stores a new passkey. A credential ID that is already stored is
// ErrAlreadyExists.
func (s *Store) InsertPasskey(ctx context.Context, p PasskeyRecord) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO passkeys ("+passkeyColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		p.ID, p.PersonID, p.UserHandle, p.CredentialID, p.RPID, p.PublicKey, p.AttestationType, p.AttestationFormat,
		nonNilBytes(p.AAGUID), int64(p.SignCount), strings.Join(p.Transports, ","),
		p.UserPresent, p.UserVerified, p.BackupEligible, p.BackupState,
		p.Name, formatTime(p.CreatedAt), nullTime(p.LastUsedAt))
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf("passkey credential: %w", ErrAlreadyExists)
		}
		return fmt.Errorf("insert passkey: %w", err)
	}
	return nil
}

// ListPasskeys returns one person's passkeys, oldest first.
func (s *Store) ListPasskeys(ctx context.Context, personID string) ([]PasskeyRecord, error) {
	return s.queryPasskeys(ctx, "SELECT "+passkeyColumns+" FROM passkeys WHERE person_id = ? ORDER BY created_at, rowid", personID)
}

// ListAllPasskeys returns every person's passkeys, oldest first (for the host CLI).
func (s *Store) ListAllPasskeys(ctx context.Context) ([]PasskeyRecord, error) {
	return s.queryPasskeys(ctx, "SELECT "+passkeyColumns+" FROM passkeys ORDER BY created_at, rowid")
}

func (s *Store) queryPasskeys(ctx context.Context, query string, args ...any) ([]PasskeyRecord, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	var out []PasskeyRecord
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, fmt.Errorf("list passkeys: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	return out, nil
}

// GetPasskey returns the person's passkey with our ID, or ErrNotFound (also for
// another person's passkey).
func (s *Store) GetPasskey(ctx context.Context, personID, id string) (PasskeyRecord, error) {
	p, err := scanPasskey(s.db.QueryRowContext(ctx,
		"SELECT "+passkeyColumns+" FROM passkeys WHERE person_id = ? AND id = ?", personID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PasskeyRecord{}, fmt.Errorf("passkey %q: %w", id, ErrNotFound)
	} else if err != nil {
		return PasskeyRecord{}, fmt.Errorf("get passkey: %w", err)
	}
	return p, nil
}

// GetPasskeyByCredential returns the passkey with that credential ID, or
// ErrNotFound. The error never holds the credential ID.
func (s *Store) GetPasskeyByCredential(ctx context.Context, credentialID []byte) (PasskeyRecord, error) {
	p, err := scanPasskey(s.db.QueryRowContext(ctx,
		"SELECT "+passkeyColumns+" FROM passkeys WHERE credential_id = ?", credentialID))
	if errors.Is(err, sql.ErrNoRows) {
		return PasskeyRecord{}, fmt.Errorf("passkey credential: %w", ErrNotFound)
	} else if err != nil {
		return PasskeyRecord{}, fmt.Errorf("get passkey: %w", err)
	}
	return p, nil
}

// RenamePasskey changes the name of a person's passkey. An unknown ID, or another
// person's passkey, is ErrNotFound.
func (s *Store) RenamePasskey(ctx context.Context, personID, id, name string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE passkeys SET name = ? WHERE id = ? AND person_id = ?", name, id, personID)
	if err != nil {
		return fmt.Errorf("rename passkey: %w", err)
	}
	return passkeyChanged(res, "rename passkey", id)
}

// DeletePasskey removes a person's passkey. An unknown ID, or another person's
// passkey, is ErrNotFound.
func (s *Store) DeletePasskey(ctx context.Context, personID, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM passkeys WHERE id = ? AND person_id = ?", id, personID)
	if err != nil {
		return fmt.Errorf("delete passkey: %w", err)
	}
	return passkeyChanged(res, "delete passkey", id)
}

// UpdatePasskeyUse records a sign-in with the passkey: its new counter, the
// flags the authenticator reported and the time. An unknown ID is ErrNotFound.
func (s *Store) UpdatePasskeyUse(ctx context.Context, id string, signCount uint32, userVerified, backupEligible, backupState bool, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE passkeys SET sign_count = ?, user_verified = ?, backup_eligible = ?, backup_state = ?, last_used_at = ?
		WHERE id = ?`,
		int64(signCount), userVerified, backupEligible, backupState, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("update passkey use: %w", err)
	}
	return passkeyChanged(res, "update passkey use", id)
}

// CountPasskeys counts a person's passkeys made at rpID, or all of theirs when rpID is "".
func (s *Store) CountPasskeys(ctx context.Context, personID, rpID string) (int, error) {
	query, args := "SELECT count(*) FROM passkeys WHERE person_id = ?", []any{personID}
	if rpID != "" {
		query, args = query+" AND rp_id = ?", append(args, rpID)
	}
	return s.countPasskeys(ctx, query, args)
}

// CountAllPasskeys counts everyone's passkeys made at rpID, or all of them when rpID is "".
func (s *Store) CountAllPasskeys(ctx context.Context, rpID string) (int, error) {
	query, args := "SELECT count(*) FROM passkeys", []any(nil)
	if rpID != "" {
		query, args = query+" WHERE rp_id = ?", []any{rpID}
	}
	return s.countPasskeys(ctx, query, args)
}

func (s *Store) countPasskeys(ctx context.Context, query string, args []any) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count passkeys: %w", err)
	}
	return n, nil
}

// passkeyChanged turns "no row changed" into ErrNotFound.
func passkeyChanged(res sql.Result, what, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: passkey %q: %w", what, id, ErrNotFound)
	}
	return nil
}

func scanPasskey(r scanner) (PasskeyRecord, error) {
	var (
		p                     PasskeyRecord
		signCount             int64
		transports, created   string
		lastUsed              sql.NullString
		present, verified     bool
		eligible, backedUpNow bool
	)
	if err := r.Scan(&p.ID, &p.PersonID, &p.UserHandle, &p.CredentialID, &p.RPID, &p.PublicKey, &p.AttestationType,
		&p.AttestationFormat, &p.AAGUID, &signCount, &transports, &present, &verified, &eligible, &backedUpNow,
		&p.Name, &created, &lastUsed); err != nil {
		return PasskeyRecord{}, err
	}
	p.SignCount = uint32(signCount)
	p.UserPresent, p.UserVerified, p.BackupEligible, p.BackupState = present, verified, eligible, backedUpNow
	if transports != "" {
		p.Transports = strings.Split(transports, ",")
	}
	var err error
	if p.CreatedAt, err = parseTime(created); err != nil {
		return PasskeyRecord{}, err
	}
	if p.LastUsedAt, err = parseNullTime(lastUsed); err != nil {
		return PasskeyRecord{}, err
	}
	return p, nil
}

// nonNilBytes makes a nil slice an empty one, since the column is NOT NULL and
// an authenticator may report no AAGUID.
func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
