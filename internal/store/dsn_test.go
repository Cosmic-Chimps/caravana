package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A data directory whose name holds '#', '?', '%' or a space is a path like any other: the DSN
// must not let SQLite read the rest as a fragment or a query and open another file.
func TestOpenInADirectoryWithDSNCharacters(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"has#hash", "has?query", "has space", "has%41percent", "a#b?c d%e"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, name)

			st, err := Open(ctx, dir)
			if err != nil {
				t.Fatalf("Open(%q) = %v", dir, err)
			}
			if _, err := st.InsertProject(ctx, "dsn", "Written before closing"); err != nil {
				st.Close()
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(dir, dbFile))
			if err != nil || info.Size() == 0 {
				t.Fatalf("the database file in %q: %v (size %v), want it created there", dir, err, info)
			}
			// Nothing else was made beside the directory: SQLite didn't open a shorter name.
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 || entries[0].Name() != name {
				t.Errorf("the parent holds %v (%v), want only %q", entries, err, name)
			}

			again, err := Open(ctx, dir)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer again.Close()
			if _, err := again.GetProject(ctx, "dsn"); err != nil {
				t.Errorf("the data written before closing = %v, want it back", err)
			}
		})
	}
}
