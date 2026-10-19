package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// APITokenRecord is an API token, as stored: a hash only. The token itself
// exists only in the response that created it.
type APITokenRecord struct {
	ID         string
	Name       string
	Hash       string // hex SHA-256 of the token
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	// ScopeProject and ScopeApp, both set or both empty, make this a deploy
	// token: it may only deploy the app named ScopeApp in project ScopeProject
	// (in any of its environments). Empty: a full token.
	ScopeProject string
	ScopeApp     string
}

// active reports whether the token can authenticate at now.
func (r APITokenRecord) active(now time.Time) bool {
	if r.RevokedAt != nil {
		return false
	}
	return r.ExpiresAt == nil || now.Before(*r.ExpiresAt)
}

const apiTokenColumns = "id, name, token_hash, created_at, last_used_at, expires_at, revoked_at, scope_project, scope_app"

// CreateAPIToken inserts r unless an active token (not revoked, not expired
// at now) already has the same name: then it is ErrAlreadyExists. The check
// and the insert are one transaction.
func (s *Store) CreateAPIToken(ctx context.Context, r APITokenRecord, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create api token %q: %w", r.Name, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	rows, err := tx.QueryContext(ctx,
		"SELECT "+apiTokenColumns+" FROM api_tokens WHERE name = ?", r.Name)
	if err != nil {
		return fmt.Errorf("create api token %q: %w", r.Name, err)
	}
	taken := false
	for rows.Next() {
		rec, err := scanAPIToken(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("create api token %q: %w", r.Name, err)
		}
		if rec.active(now) {
			taken = true
		}
	}
	err = rows.Err()
	rows.Close() // the transaction's connection must be free for the insert
	if err != nil {
		return fmt.Errorf("create api token %q: %w", r.Name, err)
	}
	if taken {
		return fmt.Errorf("api token name %q: %w", r.Name, ErrAlreadyExists)
	}

	if _, err := tx.ExecContext(ctx,
		"INSERT INTO api_tokens ("+apiTokenColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		r.ID, r.Name, r.Hash, formatTime(r.CreatedAt),
		nullTime(r.LastUsedAt), nullTime(r.ExpiresAt), nullTime(r.RevokedAt),
		nullString(r.ScopeProject), nullString(r.ScopeApp)); err != nil {
		return fmt.Errorf("create api token %q: %w", r.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create api token %q: %w", r.Name, err)
	}
	return nil
}

// GetAPITokenByHash returns the token whose SHA-256 is hash, or ErrNotFound.
func (s *Store) GetAPITokenByHash(ctx context.Context, hash string) (APITokenRecord, error) {
	rec, err := scanAPIToken(s.db.QueryRowContext(ctx,
		"SELECT "+apiTokenColumns+" FROM api_tokens WHERE token_hash = ?", hash))
	if errors.Is(err, sql.ErrNoRows) {
		return APITokenRecord{}, fmt.Errorf("api token: %w", ErrNotFound)
	}
	if err != nil {
		return APITokenRecord{}, fmt.Errorf("get api token: %w", err)
	}
	return rec, nil
}

// ListAPITokens returns every token, newest first.
func (s *Store) ListAPITokens(ctx context.Context) ([]APITokenRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+apiTokenColumns+" FROM api_tokens ORDER BY created_at DESC, rowid DESC")
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()
	var out []APITokenRecord
	for rows.Next() {
		rec, err := scanAPIToken(rows)
		if err != nil {
			return nil, fmt.Errorf("list api tokens: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	return out, nil
}

// TouchAPIToken records that the token was used at the given time.
func (s *Store) TouchAPIToken(ctx context.Context, id string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		"UPDATE api_tokens SET last_used_at = ? WHERE id = ?", formatTime(at), id); err != nil {
		return fmt.Errorf("touch api token: %w", err)
	}
	return nil
}

// RevokeAPIToken revokes the token with that ID and returns its name. It
// stays in the list, refused from now on. Revoking again changes nothing (the
// first time is kept); an unknown ID is ErrNotFound.
func (s *Store) RevokeAPIToken(ctx context.Context, id string, at time.Time) (name string, err error) {
	err = s.db.QueryRowContext(ctx,
		"UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ? RETURNING name",
		formatTime(at), id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("revoke api token: token %q: %w", id, ErrNotFound)
	} else if err != nil {
		return "", fmt.Errorf("revoke api token %q: %w", id, err)
	}
	return name, nil
}

// ActiveAPITokenByName returns the active token (not revoked, not expired at
// now) with that name, or ErrNotFound.
func (s *Store) ActiveAPITokenByName(ctx context.Context, name string, now time.Time) (APITokenRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+apiTokenColumns+" FROM api_tokens WHERE name = ?", name)
	if err != nil {
		return APITokenRecord{}, fmt.Errorf("find api token %q: %w", name, err)
	}
	defer rows.Close()
	for rows.Next() {
		rec, err := scanAPIToken(rows)
		if err != nil {
			return APITokenRecord{}, fmt.Errorf("find api token %q: %w", name, err)
		}
		if rec.active(now) {
			return rec, nil
		}
	}
	if err := rows.Err(); err != nil {
		return APITokenRecord{}, fmt.Errorf("find api token %q: %w", name, err)
	}
	return APITokenRecord{}, fmt.Errorf("active api token %q: %w", name, ErrNotFound)
}

// scanner is what *sql.Row and *sql.Rows have in common.
type scanner interface {
	Scan(dest ...any) error
}

func scanAPIToken(sc scanner) (APITokenRecord, error) {
	var (
		r                          APITokenRecord
		created                    string
		lastUsed, expires, revoked sql.NullString
		scopeProject, scopeApp     sql.NullString
	)
	if err := sc.Scan(&r.ID, &r.Name, &r.Hash, &created, &lastUsed, &expires, &revoked, &scopeProject, &scopeApp); err != nil {
		return APITokenRecord{}, err
	}
	r.ScopeProject, r.ScopeApp = scopeProject.String, scopeApp.String
	var err error
	if r.CreatedAt, err = parseTime(created); err != nil {
		return APITokenRecord{}, err
	}
	if r.LastUsedAt, err = parseNullTime(lastUsed); err != nil {
		return APITokenRecord{}, err
	}
	if r.ExpiresAt, err = parseNullTime(expires); err != nil {
		return APITokenRecord{}, err
	}
	if r.RevokedAt, err = parseNullTime(revoked); err != nil {
		return APITokenRecord{}, err
	}
	return r, nil
}

// nullTime is the value to store for an optional time: NULL when nil.
func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// nullString is the value to store for an optional text: NULL when empty.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseNullTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
