package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdmin(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	rec := AdminRecord{Email: "you@example.com", PasswordHash: "hash-one", CreatedAt: t0, PasswordChangedAt: t0}

	t.Run("create and get round trip", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.GetAdmin(ctx); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetAdmin before create = %v, want ErrNotFound", err)
		}
		if err := s.CreateAdmin(ctx, rec); err != nil {
			t.Fatalf("CreateAdmin: %v", err)
		}
		got, err := s.GetAdmin(ctx)
		if err != nil || got != rec {
			t.Errorf("GetAdmin = %+v, %v; want %+v", got, err, rec)
		}
	})

	t.Run("a second create is refused", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAdmin(ctx, rec); err != nil {
			t.Fatal(err)
		}
		other := rec
		other.Email = "other@example.com"
		if err := s.CreateAdmin(ctx, other); !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("second CreateAdmin = %v, want ErrAlreadyExists", err)
		}
		if got, _ := s.GetAdmin(ctx); got.Email != rec.Email {
			t.Errorf("admin email = %q after a refused create, want %q", got.Email, rec.Email)
		}
	})

	t.Run("set password changes hash and time", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAdmin(ctx, rec); err != nil {
			t.Fatal(err)
		}
		later := t0.Add(time.Hour)
		if err := s.SetPassword(ctx, "hash-two", later, ""); err != nil {
			t.Fatalf("SetPassword: %v", err)
		}
		got, _ := s.GetAdmin(ctx)
		if got.PasswordHash != "hash-two" || !got.PasswordChangedAt.Equal(later) || !got.CreatedAt.Equal(t0) {
			t.Errorf("admin after SetPassword = %+v", got)
		}
	})

	t.Run("set password without an admin", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SetPassword(ctx, "hash", t0, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPassword without admin = %v, want ErrNotFound", err)
		}
	})

	t.Run("set password ends every session but the kept one", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAdmin(ctx, rec); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"a", "b", "c"} {
			if err := s.CreateSession(ctx, SessionRecord{IDHash: id, CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.SetPassword(ctx, "hash-two", t0, "b"); err != nil {
			t.Fatalf("SetPassword keeping b: %v", err)
		}
		for id, wantFound := range map[string]bool{"a": false, "b": true, "c": false} {
			if _, err := s.GetSession(ctx, id); (err == nil) != wantFound {
				t.Errorf("session %q found = %v, want %v", id, err == nil, wantFound)
			}
		}
		if err := s.SetPassword(ctx, "hash-three", t0, ""); err != nil {
			t.Fatalf("SetPassword keeping none: %v", err)
		}
		if _, err := s.GetSession(ctx, "b"); !errors.Is(err, ErrNotFound) {
			t.Errorf("session b after a reset = %v, want ErrNotFound", err)
		}
	})
}
