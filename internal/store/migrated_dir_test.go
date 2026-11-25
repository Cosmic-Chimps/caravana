package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// This is the copy trick of internal/store/storetest, repeated here because
// storetest imports this package (an import cycle otherwise). Opening a new
// store applies every migration: about 230 ms, about 6 s under -race. Copying
// the file of a store migrated once costs about 3 ms (37 ms under -race).
// Tests of the migrations themselves keep calling Open on an empty directory.
var (
	migratedOnce  sync.Once
	migratedBytes []byte
	migratedErr   error
)

// migratedDir is a new temporary directory holding a migrated, empty database.
func migratedDir(t testing.TB) string {
	t.Helper()
	migratedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "caravana-store-template-")
		if err != nil {
			migratedErr = err
			return
		}
		defer os.RemoveAll(dir)
		s, err := Open(context.Background(), dir)
		if err != nil {
			migratedErr = err
			return
		}
		if migratedErr = s.Close(); migratedErr != nil {
			return
		}
		migratedBytes, migratedErr = os.ReadFile(filepath.Join(dir, dbFile))
	})
	if migratedErr != nil {
		t.Fatalf("build the migrated template store: %v", migratedErr)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, dbFile), migratedBytes, 0o600); err != nil {
		t.Fatalf("write the database copy: %v", err)
	}
	return dir
}
