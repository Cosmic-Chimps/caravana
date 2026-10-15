package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// appSchemaVersion is the format of the app documents this binary writes.
// Bump it when a change to app.App's json tags can't be read by older code,
// and teach GetApp to upgrade older documents.
const appSchemaVersion = 1

// ErrRevisionMismatch means SaveAppIf found a different stored app than the
// caller read: someone saved in between, so nothing was written.
var ErrRevisionMismatch = errors.New("app changed since it was read")

// AppRecord is a stored app.
type AppRecord struct {
	App app.App
	// Compose is the Compose file the app was made from, as last deployed,
	// secret values left out. nil when the app wasn't made from a file, or
	// the file was dropped.
	Compose   *compose.Request
	CreatedAt time.Time
	UpdatedAt time.Time
	// Revision names this exact stored app: the first 16 hex digits of the
	// SHA-256 of its JSON document and, when it keeps a Compose file, of that
	// file's JSON too. The same app and file always have the same revision, so
	// a save that changes nothing doesn't move it, and any change (even a
	// comment in the file) does.
	Revision string
}

// revisionOf returns the revision of a stored app: the hash of the document,
// then, only when the app keeps a file, a zero byte and the file's JSON. The
// zero byte keeps the two parts from running together. An app without a file
// hashes the document alone, so its revision is what it was before files were
// kept and open console pages don't turn stale.
func revisionOf(doc, composeJSON string, kept bool) string {
	h := sha256.New()
	h.Write([]byte(doc))
	if kept {
		h.Write([]byte{0})
		h.Write([]byte(composeJSON))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// composeColumn is the value for the compose column: the file's JSON without
// secret values, or NULL for no file. The service strips the values too; this
// is the second line, so no caller can store one by mistake.
func composeColumn(file *compose.Request) (sql.NullString, error) {
	if file == nil {
		return sql.NullString{}, nil
	}
	doc, err := json.Marshal(file.WithoutSecretValues())
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(doc), Valid: true}, nil
}

// SaveApp inserts the app, or replaces the stored one with the same ID. file
// is the Compose file the app was made from, or nil. Saving an identical app
// and file changes nothing, not even UpdatedAt, so repeating the same deploy
// payload only adds a deployment record.
func (s *Store) SaveApp(ctx context.Context, a app.App, file *compose.Request) (AppRecord, error) {
	if a.ID == "" {
		return AppRecord{}, errors.New("save app: ID is required")
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	kept, err := composeColumn(file)
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	now := formatTime(s.now())
	// IS NOT, not !=, for compose: it is NULL for an app without a file, and
	// NULL != 'x' is NULL (not true), which would skip a real change.
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO apps (id, schema_version, document, compose, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			schema_version = excluded.schema_version,
			document       = excluded.document,
			compose        = excluded.compose,
			updated_at     = excluded.updated_at
		WHERE apps.document != excluded.document OR apps.compose IS NOT excluded.compose`,
		a.ID, appSchemaVersion, string(doc), kept, now, now)
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	return s.GetApp(ctx, a.ID)
}

// SaveAppIf is SaveApp for an app read earlier: it saves only when the stored
// app still has the given revision, else it writes nothing and returns
// ErrRevisionMismatch. The read and the write share one transaction (which
// takes the write lock when it begins), so two saves from the same revision
// can't both pass. An unknown ID is ErrNotFound. Like SaveApp, an identical
// document and file leave UpdatedAt alone. The revision covers the stored
// file, so a stale revision is refused even when only the file changed.
func (s *Store) SaveAppIf(ctx context.Context, a app.App, file *compose.Request, revision string) (AppRecord, error) {
	if a.ID == "" {
		return AppRecord{}, errors.New("save app: ID is required")
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	kept, err := composeColumn(file)
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	// The connection pool has one connection and this transaction holds it,
	// so every read and write below goes through tx, never through s.db.
	var stored string
	var storedCompose sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT document, compose FROM apps WHERE id = ?", a.ID).Scan(&stored, &storedCompose)
	if errors.Is(err, sql.ErrNoRows) {
		return AppRecord{}, fmt.Errorf("app %q: %w", a.ID, ErrNotFound)
	}
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	if current := revisionOf(stored, storedCompose.String, storedCompose.Valid); current != revision {
		return AppRecord{}, fmt.Errorf("app %q is at revision %s, not %s: %w",
			a.ID, current, revision, ErrRevisionMismatch)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE apps SET schema_version = ?, document = ?, compose = ?, updated_at = ?
		WHERE id = ? AND (document != ? OR compose IS NOT ?)`,
		appSchemaVersion, string(doc), kept, formatTime(s.now()), a.ID, string(doc), kept); err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	rec, err := scanApp(tx.QueryRowContext(ctx, "SELECT "+appColumns+" FROM apps WHERE id = ?", a.ID))
	if err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return AppRecord{}, fmt.Errorf("save app %q: %w", a.ID, err)
	}
	return rec, nil
}

// InsertApp stores a new app. An app with the same ID is ErrAlreadyExists and
// nothing changes: unlike SaveApp it never replaces, so two concurrent
// creates of one ID can't both win (the database decides, not a check
// before the write).
func (s *Store) InsertApp(ctx context.Context, a app.App, file *compose.Request) (AppRecord, error) {
	if a.ID == "" {
		return AppRecord{}, errors.New("insert app: ID is required")
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return AppRecord{}, fmt.Errorf("insert app %q: %w", a.ID, err)
	}
	kept, err := composeColumn(file)
	if err != nil {
		return AppRecord{}, fmt.Errorf("insert app %q: %w", a.ID, err)
	}
	now := formatTime(s.now())
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO apps (id, schema_version, document, compose, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		a.ID, appSchemaVersion, string(doc), kept, now, now)
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

const appColumns = "id, schema_version, document, compose, created_at, updated_at"

// scanApp reads one row of appColumns and decodes the app document. It
// returns sql.ErrNoRows unwrapped (for GetApp); other errors name the app.
func scanApp(row interface{ Scan(...any) error }) (AppRecord, error) {
	var (
		id, doc, created, upd string
		version               int
		kept                  sql.NullString
	)
	if err := row.Scan(&id, &version, &doc, &kept, &created, &upd); err != nil {
		return AppRecord{}, err
	}
	if version > appSchemaVersion {
		return AppRecord{}, fmt.Errorf("app %q has document version %d, this binary reads up to %d: %w",
			id, version, appSchemaVersion, ErrSchemaTooNew)
	}

	rec := AppRecord{Revision: revisionOf(doc, kept.String, kept.Valid)}
	if err := json.Unmarshal([]byte(doc), &rec.App); err != nil {
		return AppRecord{}, fmt.Errorf("decode app %q: %w", id, err)
	}
	if kept.Valid {
		rec.Compose = new(compose.Request)
		if err := json.Unmarshal([]byte(kept.String), rec.Compose); err != nil {
			return AppRecord{}, fmt.Errorf("decode compose file of app %q: %w", id, err)
		}
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

// Times are stored as RFC 3339 text in UTC with all nine fraction digits, so
// they sort correctly as text. (RFC3339Nano drops trailing zeros, which made
// ".7612Z" sort after ".76123Z"; rows written that way still parse.)
func formatTime(t time.Time) string { return t.UTC().Format(storedTime) }

const storedTime = "2006-01-02T15:04:05.000000000Z07:00"

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
		"DELETE FROM volume_sizes WHERE app_id = ?",
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
