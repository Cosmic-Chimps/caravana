package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SigninLimitRecord counts failed sign-ins from one source address (an IPv4
// address, or an IPv6 /64).
type SigninLimitRecord struct {
	Address     string
	Failures    int
	WindowStart time.Time
	LockedUntil *time.Time // nil: not locked
}

// GetSigninLimit returns the record for address, or ErrNotFound.
func (s *Store) GetSigninLimit(ctx context.Context, address string) (SigninLimitRecord, error) {
	r := SigninLimitRecord{Address: address}
	var window string
	var locked sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT failures, window_start, locked_until FROM signin_limits WHERE address = ?", address,
	).Scan(&r.Failures, &window, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return SigninLimitRecord{}, fmt.Errorf("sign-in limit of %q: %w", address, ErrNotFound)
	}
	if err != nil {
		return SigninLimitRecord{}, fmt.Errorf("get sign-in limit of %q: %w", address, err)
	}
	if r.WindowStart, err = parseTime(window); err != nil {
		return SigninLimitRecord{}, fmt.Errorf("sign-in limit of %q: %w", address, err)
	}
	if r.LockedUntil, err = parseNullTime(locked); err != nil {
		return SigninLimitRecord{}, fmt.Errorf("sign-in limit of %q: %w", address, err)
	}
	return r, nil
}

// PutSigninLimit inserts or replaces the record, then deletes the records
// whose window started before pruneBefore, so the table doesn't grow without
// bound.
func (s *Store) PutSigninLimit(ctx context.Context, r SigninLimitRecord, pruneBefore time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("put sign-in limit of %q: %w", r.Address, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO signin_limits (address, failures, window_start, locked_until)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (address) DO UPDATE SET
			failures     = excluded.failures,
			window_start = excluded.window_start,
			locked_until = excluded.locked_until`,
		r.Address, r.Failures, formatTime(r.WindowStart), nullTime(r.LockedUntil)); err != nil {
		return fmt.Errorf("put sign-in limit of %q: %w", r.Address, err)
	}

	// Prune in Go-parsed time order: the stored text has no fixed width.
	rows, err := tx.QueryContext(ctx, "SELECT address, window_start FROM signin_limits")
	if err != nil {
		return fmt.Errorf("prune sign-in limits: %w", err)
	}
	var stale []string
	for rows.Next() {
		var addr, window string
		if err := rows.Scan(&addr, &window); err != nil {
			rows.Close()
			return fmt.Errorf("prune sign-in limits: %w", err)
		}
		t, err := parseTime(window)
		if err != nil {
			rows.Close()
			return fmt.Errorf("prune sign-in limits: %w", err)
		}
		if t.Before(pruneBefore) {
			stale = append(stale, addr)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("prune sign-in limits: %w", err)
	}
	for _, addr := range stale {
		if _, err := tx.ExecContext(ctx, "DELETE FROM signin_limits WHERE address = ?", addr); err != nil {
			return fmt.Errorf("prune sign-in limits: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("put sign-in limit of %q: %w", r.Address, err)
	}
	return nil
}

// DeleteSigninLimit removes address's record. Deleting when there is none is
// fine.
func (s *Store) DeleteSigninLimit(ctx context.Context, address string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM signin_limits WHERE address = ?", address); err != nil {
		return fmt.Errorf("delete sign-in limit of %q: %w", address, err)
	}
	return nil
}
