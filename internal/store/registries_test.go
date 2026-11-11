package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRegistries(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 123456789, time.UTC)

	t.Run("a new store has none", func(t *testing.T) {
		s := openTestStore(t)
		got, err := s.ListRegistries(ctx)
		if err != nil || len(got) != 0 {
			t.Errorf("ListRegistries() = %v, %v; want none", got, err)
		}
		if _, err := s.GetRegistry(ctx, "ghcr.io", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetRegistry() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("save and read back", func(t *testing.T) {
		s := openTestStore(t)
		want := Registry{Host: "registry.example.com:5000", Username: "deploy", CreatedAt: t0, UpdatedAt: t0}
		if err := s.SaveRegistry(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRegistry(ctx, "registry.example.com:5000", "")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("GetRegistry() = %+v, %v; want %+v", got, err, want)
		}
		if got.LastTest != nil {
			t.Errorf("LastTest = %+v on an untested registry, want nil", got.LastTest)
		}
	})

	t.Run("saving again keeps created_at and changes the rest", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Username: "old", CreatedAt: t0, UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		t1 := t0.Add(time.Hour)
		// The second save's CreatedAt is what a service that doesn't know the
		// first one would send: it must not win.
		if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Username: "new", CreatedAt: t1, UpdatedAt: t1}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRegistry(ctx, "ghcr.io", "")
		want := Registry{Host: "ghcr.io", Username: "new", CreatedAt: t0, UpdatedAt: t1}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("GetRegistry() = %+v, %v; want %+v", got, err, want)
		}
	})

	t.Run("list is ordered by host", func(t *testing.T) {
		s := openTestStore(t)
		for _, host := range []string{"quay.io", "ghcr.io", "docker.io", "registry.example.com:5000", "registry.example.com"} {
			if err := s.SaveRegistry(ctx, Registry{Host: host, Username: "u", CreatedAt: t0, UpdatedAt: t0}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListRegistries(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var hosts []string
		for _, r := range got {
			hosts = append(hosts, r.Host)
		}
		want := []string{"docker.io", "ghcr.io", "quay.io", "registry.example.com", "registry.example.com:5000"}
		if !reflect.DeepEqual(hosts, want) {
			t.Errorf("hosts = %v, want %v", hosts, want)
		}
	})

	t.Run("delete removes it, and a missing host is ErrNotFound", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Username: "u", CreatedAt: t0, UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteRegistry(ctx, "ghcr.io", ""); err != nil {
			t.Fatalf("DeleteRegistry() = %v", err)
		}
		if _, err := s.GetRegistry(ctx, "ghcr.io", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetRegistry() after delete = %v, want ErrNotFound", err)
		}
		if err := s.DeleteRegistry(ctx, "ghcr.io", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteRegistry() of a missing host = %v, want ErrNotFound", err)
		}
	})

	t.Run("test outcomes round-trip", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Username: "u", CreatedAt: t0, UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		refused := RegistryTest{At: t0.Add(time.Minute), OK: false, Message: "the registry refused the stored credentials"}
		if err := s.SaveRegistryTest(ctx, "ghcr.io", "", refused); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRegistry(ctx, "ghcr.io", "")
		if err != nil || got.LastTest == nil || *got.LastTest != refused {
			t.Fatalf("GetRegistry().LastTest = %+v, %v; want %+v", got.LastTest, err, refused)
		}

		ok := RegistryTest{At: t0.Add(2 * time.Minute), OK: true}
		if err := s.SaveRegistryTest(ctx, "ghcr.io", "", ok); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListRegistries(ctx)
		if err != nil || len(list) != 1 || list[0].LastTest == nil || *list[0].LastTest != ok {
			t.Errorf("ListRegistries() = %+v, %v; want the newer test, ok", list, err)
		}

		// Saving the registry again leaves the test alone.
		if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Username: "u2", CreatedAt: t0, UpdatedAt: t0.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetRegistry(ctx, "ghcr.io", ""); got.LastTest == nil || *got.LastTest != ok {
			t.Errorf("LastTest after a save = %+v, want it kept", got.LastTest)
		}

		if err := s.SaveRegistryTest(ctx, "quay.io", "", ok); !errors.Is(err, ErrNotFound) {
			t.Errorf("SaveRegistryTest() of a missing host = %v, want ErrNotFound", err)
		}
	})

	t.Run("clearing a test leaves the registry untested", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Username: "u", CreatedAt: t0, UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveRegistryTest(ctx, "ghcr.io", "", RegistryTest{At: t0, OK: true}); err != nil {
			t.Fatal(err)
		}
		if err := s.ClearRegistryTest(ctx, "ghcr.io", ""); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetRegistry(ctx, "ghcr.io", ""); err != nil || got.LastTest != nil {
			t.Errorf("GetRegistry().LastTest = %+v, %v; want nil", got.LastTest, err)
		}
		if err := s.ClearRegistryTest(ctx, "quay.io", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("ClearRegistryTest() of a missing host = %v, want ErrNotFound", err)
		}
	})
}

func TestRegistryKey(t *testing.T) {
	for r, want := range map[Registry]string{
		{Host: "ghcr.io"}:                            "ghcr.io",
		{Host: "ghcr.io", Path: "acme"}:              "ghcr.io/acme",
		{Host: "127.0.0.1:15000", Path: "teama/sub"}: "127.0.0.1:15000/teama/sub",
	} {
		if got := r.Key(); got != want {
			t.Errorf("%+v Key() = %q, want %q", r, got, want)
		}
	}
}

// Entries with a path are separate rows from the host's own, in every call.
func TestRegistryPaths(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	s := openTestStore(t)
	for _, r := range []Registry{
		{Host: "ghcr.io", Path: "b", Username: "ub"},
		{Host: "ghcr.io", Username: "host"},
		{Host: "ghcr.io", Path: "a", Username: "ua"},
		{Host: "docker.io", Username: "hub"},
	} {
		r.CreatedAt, r.UpdatedAt = t0, t0
		if err := s.SaveRegistry(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListRegistries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range list {
		keys = append(keys, r.Key())
	}
	if want := []string{"docker.io", "ghcr.io", "ghcr.io/a", "ghcr.io/b"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v (host, then path)", keys, want)
	}

	// Saving one entry again changes only it.
	t1 := t0.Add(time.Hour)
	if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Path: "a", Username: "ua2", CreatedAt: t1, UpdatedAt: t1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRegistry(ctx, "ghcr.io", "a"); got.Username != "ua2" || !got.CreatedAt.Equal(t0) {
		t.Errorf("ghcr.io/a = %+v, want ua2 with created_at kept", got)
	}
	if got, _ := s.GetRegistry(ctx, "ghcr.io", ""); got.Username != "host" {
		t.Errorf("ghcr.io = %+v, want it untouched", got)
	}

	// A test outcome belongs to one entry.
	ok := RegistryTest{At: t1, OK: true, Message: "fine"}
	if err := s.SaveRegistryTest(ctx, "ghcr.io", "b", ok); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRegistry(ctx, "ghcr.io", "b"); got.LastTest == nil || *got.LastTest != ok {
		t.Errorf("ghcr.io/b LastTest = %+v, want %+v", got.LastTest, ok)
	}
	if got, _ := s.GetRegistry(ctx, "ghcr.io", ""); got.LastTest != nil {
		t.Errorf("the host entry got a test it never had: %+v", got.LastTest)
	}
	if err := s.ClearRegistryTest(ctx, "ghcr.io", "b"); err != nil {
		t.Fatal(err)
	}

	// Delete removes one, and a missing path is ErrNotFound.
	if err := s.DeleteRegistry(ctx, "ghcr.io", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRegistry(ctx, "ghcr.io", "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
	if _, err := s.GetRegistry(ctx, "ghcr.io", ""); err != nil {
		t.Errorf("the host entry went with a path entry: %v", err)
	}
}

// TestUpgradeTo0025 migrates a store holding registries as feature 017 left
// them: every row becomes a host-only entry, with its test kept.
func TestUpgradeTo0025(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, t.TempDir())
	if n, err := migrate(ctx, db, migrationsUpTo(t, 24)); err != nil || n != 24 {
		t.Fatalf("migrate to 0024 = %d, %v", n, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO registries (host, username, created_at, updated_at, last_test_at, last_test_ok, last_test_message) VALUES
		('ghcr.io', 'bot', '2026-10-16T09:00:00.000000000Z', '2026-10-17T09:00:00.000000000Z', '2026-10-17T10:00:00.000000000Z', 1, 'ok'),
		('registry.example.com:5000', 'deploy', '2026-10-16T09:00:00.000000000Z', '2026-10-16T09:00:00.000000000Z', NULL, NULL, '')`); err != nil {
		t.Fatal(err)
	}
	if n, err := migrate(ctx, db, testMigrations(t)); err != nil || n < 1 {
		t.Fatalf("migrate to the latest = %d, %v", n, err)
	}
	s := &Store{db: db}
	got, err := s.GetRegistry(ctx, "ghcr.io", "")
	if err != nil || got.Username != "bot" || got.Path != "" || got.LastTest == nil || !got.LastTest.OK || got.LastTest.Message != "ok" {
		t.Errorf("ghcr.io after the migration = %+v, %v; want the old row as a host-only entry with its test", got, err)
	}
	if got, err := s.GetRegistry(ctx, "registry.example.com:5000", ""); err != nil || got.Username != "deploy" || got.LastTest != nil {
		t.Errorf("registry.example.com:5000 after the migration = %+v, %v", got, err)
	}
	// A path entry can now sit beside the host's.
	if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Path: "acme", Username: "x", CreatedAt: got.CreatedAt, UpdatedAt: got.CreatedAt}); err != nil {
		t.Errorf("SaveRegistry(path) after the migration = %v", err)
	}
	if list, _ := s.ListRegistries(ctx); len(list) != 3 {
		t.Errorf("registries = %d, want 3", len(list))
	}
}
