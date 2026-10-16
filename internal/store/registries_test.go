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
		if _, err := s.GetRegistry(ctx, "ghcr.io"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetRegistry() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("save and read back", func(t *testing.T) {
		s := openTestStore(t)
		want := Registry{Host: "registry.example.com:5000", Username: "deploy", CreatedAt: t0, UpdatedAt: t0}
		if err := s.SaveRegistry(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRegistry(ctx, "registry.example.com:5000")
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
		got, err := s.GetRegistry(ctx, "ghcr.io")
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
		if err := s.DeleteRegistry(ctx, "ghcr.io"); err != nil {
			t.Fatalf("DeleteRegistry() = %v", err)
		}
		if _, err := s.GetRegistry(ctx, "ghcr.io"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetRegistry() after delete = %v, want ErrNotFound", err)
		}
		if err := s.DeleteRegistry(ctx, "ghcr.io"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteRegistry() of a missing host = %v, want ErrNotFound", err)
		}
	})

	t.Run("test outcomes round-trip", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveRegistry(ctx, Registry{Host: "ghcr.io", Username: "u", CreatedAt: t0, UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		refused := RegistryTest{At: t0.Add(time.Minute), OK: false, Message: "the registry refused the stored credentials"}
		if err := s.SaveRegistryTest(ctx, "ghcr.io", refused); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRegistry(ctx, "ghcr.io")
		if err != nil || got.LastTest == nil || *got.LastTest != refused {
			t.Fatalf("GetRegistry().LastTest = %+v, %v; want %+v", got.LastTest, err, refused)
		}

		ok := RegistryTest{At: t0.Add(2 * time.Minute), OK: true}
		if err := s.SaveRegistryTest(ctx, "ghcr.io", ok); err != nil {
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
		if got, _ := s.GetRegistry(ctx, "ghcr.io"); got.LastTest == nil || *got.LastTest != ok {
			t.Errorf("LastTest after a save = %+v, want it kept", got.LastTest)
		}

		if err := s.SaveRegistryTest(ctx, "quay.io", ok); !errors.Is(err, ErrNotFound) {
			t.Errorf("SaveRegistryTest() of a missing host = %v, want ErrNotFound", err)
		}
	})
}
