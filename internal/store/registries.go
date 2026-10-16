package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RegistryTest is the outcome of the last "Test" of a registry's credentials.
type RegistryTest struct {
	At time.Time
	// OK is true when the registry accepted the credentials. False means it
	// refused them or couldn't be reached; Message says which.
	OK      bool
	Message string
}

// Registry is a container registry Caravana has credentials for, without the
// secret, which lives in a Nomad Variable (constitution VII), as the backup
// destination's does.
type Registry struct {
	// Host is lowercased, with ":port" when it has one; "docker.io" is Docker
	// Hub.
	Host      string
	Username  string
	CreatedAt time.Time
	UpdatedAt time.Time
	// LastTest is nil until the credentials were tested.
	LastTest *RegistryTest
}

// registryColumns is the column order scanRegistry reads.
const registryColumns = "host, username, created_at, updated_at, last_test_at, last_test_ok, last_test_message"

// SaveRegistry adds a registry, or updates the user name and update time of
// the one with this host. The creation time of an existing one is kept, and so
// is its last test: the store can't tell that the secret changed, so the
// service records a new test (or leaves the old one) as it sees fit.
func (s *Store) SaveRegistry(ctx context.Context, r Registry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO registries (host, username, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (host) DO UPDATE SET username = excluded.username, updated_at = excluded.updated_at`,
		r.Host, r.Username, formatTime(r.CreatedAt), formatTime(r.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save registry %q: %w", r.Host, err)
	}
	return nil
}

// ListRegistries returns every registry, ordered by host.
func (s *Store) ListRegistries(ctx context.Context) ([]Registry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+registryColumns+" FROM registries ORDER BY host")
	if err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	defer rows.Close()
	var out []Registry
	for rows.Next() {
		r, err := scanRegistry(rows)
		if err != nil {
			return nil, fmt.Errorf("list registries: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	return out, nil
}

// GetRegistry returns the registry with this host, or ErrNotFound.
func (s *Store) GetRegistry(ctx context.Context, host string) (Registry, error) {
	r, err := scanRegistry(s.db.QueryRowContext(ctx, "SELECT "+registryColumns+" FROM registries WHERE host = ?", host))
	if errors.Is(err, sql.ErrNoRows) {
		return Registry{}, fmt.Errorf("registry %q: %w", host, ErrNotFound)
	}
	if err != nil {
		return Registry{}, fmt.Errorf("get registry %q: %w", host, err)
	}
	return r, nil
}

// DeleteRegistry removes the registry with this host. An unknown host is
// ErrNotFound.
func (s *Store) DeleteRegistry(ctx context.Context, host string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM registries WHERE host = ?", host)
	if err != nil {
		return fmt.Errorf("delete registry %q: %w", host, err)
	}
	return affectedOrNotFound(res, "registry "+host)
}

// SaveRegistryTest records the outcome of testing a registry's credentials,
// replacing the one before it. An unknown host is ErrNotFound.
func (s *Store) SaveRegistryTest(ctx context.Context, host string, t RegistryTest) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE registries SET last_test_at = ?, last_test_ok = ?, last_test_message = ? WHERE host = ?",
		formatTime(t.At), boolInt(t.OK), t.Message, host)
	if err != nil {
		return fmt.Errorf("save test of registry %q: %w", host, err)
	}
	return affectedOrNotFound(res, "registry "+host)
}

// affectedOrNotFound is ErrNotFound, naming what, when the statement changed
// no row.
func affectedOrNotFound(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}

func scanRegistry(row scanner) (Registry, error) {
	var r Registry
	var created, updated string
	var testAt sql.NullString
	var testOK sql.NullInt64
	var message string
	if err := row.Scan(&r.Host, &r.Username, &created, &updated, &testAt, &testOK, &message); err != nil {
		return Registry{}, err
	}
	var err error
	if r.CreatedAt, err = parseTime(created); err != nil {
		return Registry{}, err
	}
	if r.UpdatedAt, err = parseTime(updated); err != nil {
		return Registry{}, err
	}
	if testAt.Valid {
		at, err := parseTime(testAt.String)
		if err != nil {
			return Registry{}, err
		}
		r.LastTest = &RegistryTest{At: at, OK: testOK.Int64 == 1, Message: message}
	}
	return r, nil
}
