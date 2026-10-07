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

// InsertApp stores a new app. An app with the same ID is ErrAlreadyExists and
// nothing changes: unlike SaveApp it never replaces, so two concurrent
// creates of one ID can't both win (the database decides, not a check
// before the write).
func (s *Store) InsertApp(ctx context.Context, a app.App) (AppRecord, error) {
	if a.ID == "" {
		return AppRecord{}, errors.New("insert app: ID is required")
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return AppRecord{}, fmt.Errorf("insert app %q: %w", a.ID, err)
	}
	now := formatTime(s.now())
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO apps (id, schema_version, document, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		a.ID, appSchemaVersion, string(doc), now, now)
	if err != nil {
		return AppRecord{}, fmt.Errorf("insert app %q: %w", a.ID, err)
	}
	// DO NOTHING is not an error; it just writes no row.
	if n, err := res.RowsAffected(); err != nil {
		return AppRecord{}, fmt.Errorf("insert app %q: %w", a.ID, err)
	} else if n == 0 {
		return AppRecord{}, fmt.Errorf("app %q: %w", a.ID, ErrAlreadyExists)
	}
	return s.GetApp(ctx, a.ID)
}

// GetApp returns the app with the given ID, or ErrNotFound.
func (s *Store) GetApp(ctx context.Context, id string) (AppRecord, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+appColumns+" FROM apps WHERE id = ?", id)
	rec, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AppRecord{}, fmt.Errorf("app %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return AppRecord{}, fmt.Errorf("get app %q: %w", id, err)
	}
	return rec, nil
}

// ListApps returns every stored app, ordered by ID. An empty store gives an
// empty (not nil) slice. One app whose document can't be read fails the whole
// call, naming the app: the Apps page can't show a partial truth silently.
func (s *Store) ListApps(ctx context.Context) ([]AppRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+appColumns+" FROM apps ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()

	out := []AppRecord{}
	for rows.Next() {
		rec, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("list apps: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	return out, nil
}

const appColumns = "id, schema_version, document, created_at, updated_at"

// scanApp reads one row of appColumns and decodes the app document. It
// returns sql.ErrNoRows unwrapped (for GetApp); other errors name the app.
func scanApp(row interface{ Scan(...any) error }) (AppRecord, error) {
	var (
		id, doc, created, upd string
		version               int
	)
	if err := row.Scan(&id, &version, &doc, &created, &upd); err != nil {
		return AppRecord{}, err
	}
	if version > appSchemaVersion {
		return AppRecord{}, fmt.Errorf("app %q has document version %d, this binary reads up to %d: %w",
			id, version, appSchemaVersion, ErrSchemaTooNew)
	}

	rec := AppRecord{}
	if err := json.Unmarshal([]byte(doc), &rec.App); err != nil {
		return AppRecord{}, fmt.Errorf("decode app %q: %w", id, err)
	}
	var err error
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

// DeleteApp erases an app with everything that belongs to it: its webhook
// token, its deployments and the app itself, in one transaction, children
// first because they point at the app. Either all of it goes or none. An
// unknown ID is ErrNotFound.
func (s *Store) DeleteApp(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete app %q: %w", id, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	for _, query := range []string{
		"DELETE FROM webhook_tokens WHERE app_id = ?",
		"DELETE FROM deployments WHERE app_id = ?",
		"DELETE FROM backups WHERE app_id = ?",
		"DELETE FROM restores WHERE app_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return fmt.Errorf("delete app %q: %w", id, err)
		}
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM apps WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete app %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("delete app %q: %w", id, err)
	} else if n == 0 {
		return fmt.Errorf("delete app: app %q: %w", id, ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete app %q: %w", id, err)
	}
	return nil
}
