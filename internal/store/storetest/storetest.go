// Package storetest gives tests a store that is already migrated.
//
// store.Open applies every migration to a new database. That costs about 230 ms,
// and about 6 s under "go test -race", where the race detector instruments the
// whole of SQLite (modernc.org/sqlite is SQLite translated to Go). Thousands of
// tests each build a fresh store, so the migrations were most of the suite's
// time. Copying the file of a store that was migrated once, and opening the
// copy, costs about 3 ms (37 ms with -race); Open then finds nothing to apply.
//
// Tests whose point is the migrations themselves (upgrades from an older schema,
// MigrationsApplied) must keep calling store.Open on an empty directory.
package storetest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// dbFile is the store's database file name inside a data directory.
const dbFile = "caravana.db"

var (
	templateOnce  sync.Once
	templateBytes []byte // the migrated, empty database
	templateErr   error
	templateBuilt int // how many times the template was built (this package's test reads it)
)

// template migrates a store once per test process and keeps the bytes of its
// database file. The temporary directory is gone before it returns.
func template() ([]byte, error) {
	templateOnce.Do(func() {
		templateBuilt++
		dir, err := os.MkdirTemp("", "caravana-storetest-")
		if err != nil {
			templateErr = err
			return
		}
		defer os.RemoveAll(dir)

		s, err := store.Open(context.Background(), dir)
		if err != nil {
			templateErr = err
			return
		}
		// Closing the last connection checkpoints the WAL and removes it, so the
		// main file alone holds the whole database (this package's test checks it).
		if err := s.Close(); err != nil {
			templateErr = err
			return
		}
		templateBytes, templateErr = os.ReadFile(filepath.Join(dir, dbFile))
	})
	return templateBytes, templateErr
}

// Dir returns a new temporary data directory that already holds a migrated,
// empty caravana.db. Open it with store.Open (a store that is reopened later
// on the same directory works as usual).
func Dir(t testing.TB) string {
	t.Helper()
	b, err := template()
	if err != nil {
		t.Fatalf("storetest: build the migrated template store: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, dbFile), b, 0o600); err != nil {
		t.Fatalf("storetest: write the database copy: %v", err)
	}
	return dir
}

// Open opens a store on a new migrated directory and closes it when the test
// ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), Dir(t))
	if err != nil {
		t.Fatalf("storetest: open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
