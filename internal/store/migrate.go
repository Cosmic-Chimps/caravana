package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The schema migrations, compiled into the binary.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration is one NNNN_name.sql file.
type migration struct {
	version int
	file    string
}

// migrate applies, in order, every migration in fsys the database doesn't
// have yet, each in its own transaction, and returns how many it applied.
// Migrations only go forward; there is no "down".
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) (int, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	) STRICT`); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	known, err := readMigrations(fsys)
	if err != nil {
		return 0, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return 0, err
	}

	// A version we don't have means a newer Caravana migrated this store.
	// Running against a schema we don't understand could corrupt it.
	for v := range applied {
		if !slices.ContainsFunc(known, func(m migration) bool { return m.version == v }) {
			return 0, fmt.Errorf("%w: it has migration %d, this binary knows up to %d", ErrStoreTooNew, v, known[len(known)-1].version)
		}
	}

	n := 0
	for _, m := range known {
		if applied[m.version] {
			continue
		}
		if err := apply(ctx, db, fsys, m); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// readMigrations lists the NNNN_name.sql files in version order.
func readMigrations(fsys fs.FS) ([]migration, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	var out []migration
	for _, f := range files {
		num, _, ok := strings.Cut(f, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v < 1 {
			return nil, fmt.Errorf("migration %q: name must be NNNN_name.sql", f)
		}
		out = append(out, migration{version: v, file: f})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no migrations found")
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	return out, nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("read schema_migrations: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// apply runs one migration and records it, both in one transaction, so a
// failure leaves the store at the previous version.
func apply(ctx context.Context, db *sql.DB, fsys fs.FS, m migration) error {
	body, err := fs.ReadFile(fsys, m.file)
	if err != nil {
		return fmt.Errorf("migration %s: %w", m.file, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: %w", m.file, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("migration %s: %w", m.file, err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
		m.version, m.file, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("migration %s: record: %w", m.file, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.file, err)
	}
	return nil
}
