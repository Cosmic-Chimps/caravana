package storetest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/store"

	_ "modernc.org/sqlite"
)

func TestDirIsMigrated(t *testing.T) {
	dir := Dir(t)

	// One file, no WAL beside it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "caravana.db" {
		t.Fatalf("directory holds %v, want only caravana.db", entries)
	}

	s, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := s.MigrationsApplied(); n != 0 {
		t.Errorf("Open applied %d migrations on a copy, want 0", n)
	}
	s.Close()

	// Every migration file is recorded.
	files, err := filepath.Glob("../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("list migrations: %v (%d files)", err, len(files))
	}
	last := 0
	for _, f := range files {
		prefix, _, _ := strings.Cut(filepath.Base(f), "_")
		v, err := strconv.Atoi(prefix)
		if err != nil {
			t.Fatalf("migration %s has no number: %v", f, err)
		}
		last = max(last, v)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "caravana.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count, newest int
	if err := db.QueryRow("SELECT COUNT(*), MAX(version) FROM schema_migrations").Scan(&count, &newest); err != nil {
		t.Fatal(err)
	}
	if count != len(files) {
		t.Errorf("schema_migrations has %d rows, want %d (one per file)", count, len(files))
	}
	if newest != last {
		t.Errorf("newest recorded migration is %d, want %d", newest, last)
	}
}

func TestDirsAreIndependent(t *testing.T) {
	ctx := context.Background()
	a, b := Open(t), Open(t)

	if _, err := a.InsertApp(ctx, apptest.Whoami().App, nil); err != nil {
		t.Fatal(err)
	}
	recs, err := b.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("a write in one store shows %d apps in the other", len(recs))
	}
	// The same app can be inserted in the second: nothing is shared.
	if _, err := b.InsertApp(ctx, apptest.Whoami().App, nil); errors.Is(err, store.ErrAlreadyExists) {
		t.Fatal("the second store already had the app")
	}
}

func TestTemplateIsBuiltOnce(t *testing.T) {
	Dir(t) // the template exists after this
	start := time.Now()
	for range 20 {
		Dir(t)
	}
	if templateBuilt != 1 {
		t.Errorf("template built %d times, want once", templateBuilt)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("20 copies took %v: they should not migrate", d)
	}
}
