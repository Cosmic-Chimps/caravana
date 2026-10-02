package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// appSchemaVersion is the format of the app documents this binary writes.
// Bump it when a change to app.App's json tags can't be read by older code,
// and teach GetApp to upgrade older documents.
const appSchemaVersion = 1

// AppRecord is a stored app.
type AppRecord struct {
	App       app.App
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SaveApp inserts the app, or replaces the stored one with the same ID.
// Saving an identical app changes nothing, not even UpdatedAt, so repeating
// the same deploy payload only adds a deployment record.
func (s *Store) SaveApp(ctx context.Context, a app.App) (AppRecord, error) {
	if a.ID == "" {
		return AppRecord{}, errors.New("save app: ID is required")
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	now := formatTime(s.now())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO apps (id, schema_version, document, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			schema_version = excluded.schema_version,
			document       = excluded.document,
			updated_at     = excluded.updated_at
		WHERE apps.document != excluded.document`,
		a.ID, appSchemaVersion, string(doc), now, now)
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	return s.GetApp(ctx, a.ID)
}

// GetApp returns the app with the given ID, or ErrNotFound.
func (s *Store) GetApp(ctx context.Context, id string) (AppRecord, error) {
	var (
		version           int
		doc, created, upd string
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT schema_version, document, created_at, updated_at FROM apps WHERE id = ?", id,
	).Scan(&version, &doc, &created, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return AppRecord{}, fmt.Errorf("app %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return AppRecord{}, fmt.Errorf("get app %q: %w", id, err)
	}
	if version > appSchemaVersion {
		return AppRecord{}, fmt.Errorf("app %q has document version %d, this binary reads up to %d: %w",
			id, version, appSchemaVersion, ErrSchemaTooNew)
	}

	rec := AppRecord{}
	if err := json.Unmarshal([]byte(doc), &rec.App); err != nil {
		return AppRecord{}, fmt.Errorf("decode app %q: %w", id, err)
	}
	if rec.CreatedAt, err = parseTime(created); err != nil {
		return AppRecord{}, fmt.Errorf("app %q: %w", id, err)
	}
	if rec.UpdatedAt, err = parseTime(upd); err != nil {
		return AppRecord{}, fmt.Errorf("app %q: %w", id, err)
	}
	return rec, nil
}

// Times are stored as RFC 3339 text in UTC, which sorts correctly as text.
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad stored time %q: %w", s, err)
	}
	return t, nil
}
