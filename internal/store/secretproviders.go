package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SecretProviderKind is the kind of an outside secret provider. Bella Baxter
// is the only one today.
type SecretProviderKind string

// SecretProviderBella is Bella Baxter.
const SecretProviderBella SecretProviderKind = "bella"

// SecretProviderTest is the outcome of the last Test of a provider.
type SecretProviderTest struct {
	At time.Time
	// OK is true when Caravana could read the provider. False means it was
	// refused or couldn't be reached; Message says which, in Caravana's words.
	OK      bool
	Message string
	// Count is how many secrets a successful test could read.
	Count int
}

// SecretProvider is an outside place secrets are read from, without its
// credentials, which live in a Nomad Variable (constitution VII).
type SecretProvider struct {
	// Name is a lowercase DNS label, unique across providers.
	Name string
	Kind SecretProviderKind
	// URL, Project and Environment are Bella's settings: its address and the
	// slugs of the one environment Caravana reads.
	URL         string
	Project     string
	Environment string
	// Allowed limits the provider to some places: each entry is a project or
	// "project/environment". Empty means every place.
	Allowed   []string
	CreatedAt time.Time
	UpdatedAt time.Time
	// LastTest is nil until the provider was tested, and again after its
	// settings or credentials changed.
	LastTest *SecretProviderTest
}

// secretProviderSettings is the JSON of the settings column.
type secretProviderSettings struct {
	URL         string `json:"url"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

const secretProviderColumns = "name, kind, settings, allowed, created_at, updated_at, tested_at, test_ok, test_message, test_count"

// SaveSecretProvider adds a provider, or replaces the kind, settings and
// allowed places of the one with p.Name. The creation time and the last test
// of an existing one are kept: the service clears the test when something
// changed, as it does for registries.
func (s *Store) SaveSecretProvider(ctx context.Context, p SecretProvider) error {
	settings, err := json.Marshal(secretProviderSettings{URL: p.URL, Project: p.Project, Environment: p.Environment})
	if err != nil {
		return fmt.Errorf("save secret provider %q: %w", p.Name, err)
	}
	allowed, err := json.Marshal(nonNil(p.Allowed))
	if err != nil {
		return fmt.Errorf("save secret provider %q: %w", p.Name, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO secret_providers (name, kind, settings, allowed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, settings = excluded.settings,
			allowed = excluded.allowed, updated_at = excluded.updated_at`,
		p.Name, string(p.Kind), string(settings), string(allowed), formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save secret provider %q: %w", p.Name, err)
	}
	return nil
}

// ListSecretProviders returns every provider, ordered by name.
func (s *Store) ListSecretProviders(ctx context.Context) ([]SecretProvider, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+secretProviderColumns+" FROM secret_providers ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list secret providers: %w", err)
	}
	defer rows.Close()
	var out []SecretProvider
	for rows.Next() {
		p, err := scanSecretProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("list secret providers: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list secret providers: %w", err)
	}
	return out, nil
}

// GetSecretProvider returns the provider with this name, or ErrNotFound.
func (s *Store) GetSecretProvider(ctx context.Context, name string) (SecretProvider, error) {
	p, err := scanSecretProvider(s.db.QueryRowContext(ctx, "SELECT "+secretProviderColumns+" FROM secret_providers WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return SecretProvider{}, fmt.Errorf("secret provider %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return SecretProvider{}, fmt.Errorf("get secret provider %q: %w", name, err)
	}
	return p, nil
}

// DeleteSecretProvider removes the provider with this name. An unknown one is
// ErrNotFound. Whether an app still references it is the service's check.
func (s *Store) DeleteSecretProvider(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM secret_providers WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("delete secret provider %q: %w", name, err)
	}
	return affectedOrNotFound(res, "secret provider "+name)
}

// SaveSecretProviderTest records the outcome of testing a provider, replacing
// the one before it. An unknown provider is ErrNotFound.
func (s *Store) SaveSecretProviderTest(ctx context.Context, name string, t SecretProviderTest) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE secret_providers SET tested_at = ?, test_ok = ?, test_message = ?, test_count = ? WHERE name = ?",
		formatTime(t.At), boolInt(t.OK), t.Message, t.Count, name)
	if err != nil {
		return fmt.Errorf("save test of secret provider %q: %w", name, err)
	}
	return affectedOrNotFound(res, "secret provider "+name)
}

// ClearSecretProviderTest forgets the last test of a provider, because its
// settings or credentials changed. An unknown provider is ErrNotFound.
func (s *Store) ClearSecretProviderTest(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE secret_providers SET tested_at = NULL, test_ok = NULL, test_message = '', test_count = 0 WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("clear test of secret provider %q: %w", name, err)
	}
	return affectedOrNotFound(res, "secret provider "+name)
}

func scanSecretProvider(row scanner) (SecretProvider, error) {
	var (
		p                 SecretProvider
		kind              string
		settings, allowed string
		created, updated  string
		testAt            sql.NullString
		testOK            sql.NullInt64
		message           string
		count             int
	)
	if err := row.Scan(&p.Name, &kind, &settings, &allowed, &created, &updated, &testAt, &testOK, &message, &count); err != nil {
		return SecretProvider{}, err
	}
	p.Kind = SecretProviderKind(kind)
	var set secretProviderSettings
	if err := json.Unmarshal([]byte(settings), &set); err != nil {
		return SecretProvider{}, fmt.Errorf("decode settings: %w", err)
	}
	p.URL, p.Project, p.Environment = set.URL, set.Project, set.Environment
	if err := json.Unmarshal([]byte(allowed), &p.Allowed); err != nil {
		return SecretProvider{}, fmt.Errorf("decode allowed places: %w", err)
	}
	var err error
	if p.CreatedAt, err = parseTime(created); err != nil {
		return SecretProvider{}, err
	}
	if p.UpdatedAt, err = parseTime(updated); err != nil {
		return SecretProvider{}, err
	}
	if testAt.Valid {
		at, err := parseTime(testAt.String)
		if err != nil {
			return SecretProvider{}, err
		}
		p.LastTest = &SecretProviderTest{At: at, OK: testOK.Int64 == 1, Message: message, Count: count}
	}
	return p, nil
}
