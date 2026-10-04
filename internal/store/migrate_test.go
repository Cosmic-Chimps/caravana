package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestOpen(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "nested", "dir")

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, name := range []string{"caravana.db", "caravana.lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	want := embeddedVersions(t)
	if got := versions(t, s); !slices.Equal(got, want) {
		t.Errorf("applied versions = %v, want %v", got, want)
	}
	if n := s.MigrationsApplied(); n != len(want) {
		t.Errorf("MigrationsApplied() = %d on a new store, want %d", n, len(want))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if n := s.MigrationsApplied(); n != 0 {
		t.Errorf("MigrationsApplied() = %d on an up-to-date store, want 0", n)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestMigrate(t *testing.T) {
	ctx := context.Background()

	t.Run("embedded migrations apply once", func(t *testing.T) {
		db := testDB(t, t.TempDir())
		fsys := testMigrations(t)
		if n, err := migrate(ctx, db, fsys); err != nil || n != len(embeddedVersions(t)) {
			t.Fatalf("first migrate = %d, %v; want %d, nil", n, err, len(embeddedVersions(t)))
		}
		if n, err := migrate(ctx, db, fsys); err != nil || n != 0 {
			t.Fatalf("second migrate = %d, %v; want 0, nil", n, err)
		}
	})

	t.Run("a failing migration is rolled back", func(t *testing.T) {
		db := testDB(t, t.TempDir())
		fsys := fstest.MapFS{
			"0001_a.sql": {Data: []byte("CREATE TABLE a (id INTEGER PRIMARY KEY) STRICT;")},
			"0002_b.sql": {Data: []byte("CREATE TABLE b (id INTEGER PRIMARY KEY) STRICT; THIS IS NOT SQL;")},
		}
		_, err := migrate(ctx, db, fsys)
		if err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
			t.Fatalf("migrate error = %v, want one naming 0002_b.sql", err)
		}
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != 1 {
			t.Fatalf("recorded migrations = %d, %v; want 1", n, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'b'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("table b exists after a failed migration (count %d, %v)", n, err)
		}
	})
}

func TestOpenStoreTooNew(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, applied_at) VALUES (999, 'future', 'x')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	s.Close()

	if _, err := Open(ctx, dir); !errors.Is(err, ErrStoreTooNew) {
		t.Fatalf("Open = %v, want ErrStoreTooNew", err)
	}
}

func TestLock(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := Open(ctx, dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open = %v, want ErrLocked", err)
	}
	first.Close()

	again, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	again.Close()
}

func TestPing(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Errorf("Ping on an open store: %v", err)
	}
	s.Close()
	if err := s.Ping(ctx); err == nil {
		t.Error("Ping after Close succeeded, want an error")
	}
}

// testDB opens a database in dir without migrating it.
func testDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(dir, "caravana.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// testMigrations returns the embedded migrations, as Open uses them.
func testMigrations(t *testing.T) fs.FS {
	t.Helper()
	fsys, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return fsys
}

// embeddedVersions returns the versions of the migrations compiled into the
// binary, in order: 1, 2, … with no gaps.
func embeddedVersions(t *testing.T) []int {
	t.Helper()
	known, err := readMigrations(testMigrations(t))
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var out []int
	for i, m := range known {
		if m.version != i+1 {
			t.Fatalf("migration %s has version %d, want %d (no gaps)", m.file, m.version, i+1)
		}
		out = append(out, m.version)
	}
	return out
}

// versions returns the applied migration versions in order.
func versions(t *testing.T, s *Store) []int {
	t.Helper()
	rows, err := s.db.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("query versions: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func TestOpenShared(t *testing.T) {
	ctx := context.Background()

	t.Run("works while another Store holds the lock", func(t *testing.T) {
		dir := t.TempDir()
		server, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer server.Close()

		cli, err := OpenShared(ctx, dir)
		if err != nil {
			t.Fatalf("OpenShared while locked: %v", err)
		}
		defer cli.Close()

		now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
		if err := cli.CreateAdmin(ctx, AdminRecord{Email: "a@example.com", PasswordHash: "h", CreatedAt: now, PasswordChangedAt: now}); err != nil {
			t.Fatalf("CreateAdmin through the shared store: %v", err)
		}
		if got, err := server.GetAdmin(ctx); err != nil || got.Email != "a@example.com" {
			t.Errorf("server sees admin %+v, %v; want the CLI's", got, err)
		}
		if err := server.CreateAPIToken(ctx, APITokenRecord{ID: "id1", Name: "n", Hash: "h", CreatedAt: now}, now); err != nil {
			t.Fatal(err)
		}
		if got, err := cli.ListAPITokens(ctx); err != nil || len(got) != 1 {
			t.Errorf("shared store lists %d tokens, %v; want the server's 1", len(got), err)
		}
	})

	t.Run("a database one migration behind", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		last := embeddedVersions(t)[len(embeddedVersions(t))-1]
		if _, err := s.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", last); err != nil {
			t.Fatal(err)
		}
		s.Close()

		_, err = OpenShared(ctx, dir)
		if !errors.Is(err, ErrSchemaMismatch) {
			t.Fatalf("OpenShared = %v, want ErrSchemaMismatch", err)
		}
		want := fmt.Sprintf("migration %d", last-1)
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), fmt.Sprintf("%d", last)) {
			t.Errorf("error %q should name both versions (%d and %d)", err, last-1, last)
		}
	})

	t.Run("a database from a newer caravana", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, applied_at) VALUES (999, 'future', 'x')"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		if _, err := OpenShared(ctx, dir); !errors.Is(err, ErrSchemaMismatch) {
			t.Fatalf("OpenShared = %v, want ErrSchemaMismatch", err)
		}
	})

	t.Run("no database", func(t *testing.T) {
		if _, err := OpenShared(ctx, t.TempDir()); err == nil {
			t.Fatal("OpenShared on an empty directory succeeded, want an error")
		}
	})
}
