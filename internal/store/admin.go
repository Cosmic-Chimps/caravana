package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AdminRecord is the one admin, as stored: the password only as a hash.
type AdminRecord struct {
	Email             string
	PasswordHash      string // pbkdf2-sha256$<iterations>$<salt>$<key>
	CreatedAt         time.Time
	PasswordChangedAt time.Time
	// PasswordSignIn is false when only passkeys may sign in (feature 033).
	PasswordSignIn bool
	// WebAuthnHandle is the admin's WebAuthn user ID: 32 random bytes made at the
	// first passkey registration, nil before that.
	WebAuthnHandle []byte
}

// CreateAdmin stores the admin. There is only ever one: a second call is
// ErrAlreadyExists (the table's id is always 1).
func (s *Store) CreateAdmin(ctx context.Context, a AdminRecord) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO admin (id, email, password_hash, created_at, password_changed_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		a.Email, a.PasswordHash, formatTime(a.CreatedAt), formatTime(a.PasswordChangedAt))
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	// DO NOTHING is not an error; it just writes no row.
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("create admin: %w", err)
	} else if n == 0 {
		return fmt.Errorf("admin: %w", ErrAlreadyExists)
	}
	return nil
}

// GetAdmin returns the admin, or ErrNotFound before one was created.
func (s *Store) GetAdmin(ctx context.Context) (AdminRecord, error) {
	var (
		a                AdminRecord
		created, changed string
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT email, password_hash, created_at, password_changed_at, password_signin, webauthn_handle FROM admin WHERE id = 1",
	).Scan(&a.Email, &a.PasswordHash, &created, &changed, &a.PasswordSignIn, &a.WebAuthnHandle)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminRecord{}, fmt.Errorf("admin: %w", ErrNotFound)
	}
	if err != nil {
		return AdminRecord{}, fmt.Errorf("get admin: %w", err)
	}
	if a.CreatedAt, err = parseTime(created); err != nil {
		return AdminRecord{}, fmt.Errorf("admin: %w", err)
	}
	if a.PasswordChangedAt, err = parseTime(changed); err != nil {
		return AdminRecord{}, fmt.Errorf("admin: %w", err)
	}
	return a, nil
}

// SetPassword replaces the admin's password hash and deletes every session
// but keepSession (an ID hash; "" deletes them all), in one transaction: a new
// password never leaves the old sessions alive. Without an admin it is
// ErrNotFound and nothing changes.
func (s *Store) SetPassword(ctx context.Context, hash string, at time.Time, keepSession string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	res, err := tx.ExecContext(ctx,
		"UPDATE admin SET password_hash = ?, password_changed_at = ? WHERE id = 1",
		hash, formatTime(at))
	if err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("set admin password: %w", err)
	} else if n == 0 {
		return fmt.Errorf("set admin password: admin: %w", ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE id_hash != ?", keepSession); err != nil {
		return fmt.Errorf("set admin password: delete sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	return nil
}

// SetPasswordSignIn turns password sign-in on or off. Without an admin it is
// ErrNotFound.
func (s *Store) SetPasswordSignIn(ctx context.Context, on bool) error {
	res, err := s.db.ExecContext(ctx, "UPDATE admin SET password_signin = ? WHERE id = 1", on)
	if err != nil {
		return fmt.Errorf("set password sign-in: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("set password sign-in: %w", err)
	} else if n == 0 {
		return fmt.Errorf("set password sign-in: admin: %w", ErrNotFound)
	}
	return nil
}

// SetWebAuthnHandle stores the admin's WebAuthn user handle, but only when none
// is stored yet, in one statement: two first registrations at once agree on the
// one that won. It returns the stored handle (the one given, or the earlier one).
// Without an admin it is ErrNotFound.
func (s *Store) SetWebAuthnHandle(ctx context.Context, handle []byte) ([]byte, error) {
	var stored []byte
	err := s.db.QueryRowContext(ctx,
		"UPDATE admin SET webauthn_handle = COALESCE(webauthn_handle, ?) WHERE id = 1 RETURNING webauthn_handle",
		handle).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("set webauthn handle: admin: %w", ErrNotFound)
	} else if err != nil {
		return nil, fmt.Errorf("set webauthn handle: %w", err)
	}
	return stored, nil
}
