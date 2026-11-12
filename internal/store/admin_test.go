package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAdmin(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	rec := AdminRecord{Email: "you@example.com", PasswordHash: "hash-one", CreatedAt: t0, PasswordChangedAt: t0, PasswordSignIn: true}

	t.Run("create and get round trip", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.GetAdmin(ctx); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetAdmin before create = %v, want ErrNotFound", err)
		}
		if err := s.CreateAdmin(ctx, rec); err != nil {
			t.Fatalf("CreateAdmin: %v", err)
		}
		got, err := s.GetAdmin(ctx)
		if err != nil || !reflect.DeepEqual(got, rec) {
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

func TestAdminSignInSettings(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	t.Run("without an admin", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SetPasswordSignIn(ctx, false); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPasswordSignIn without admin = %v, want ErrNotFound", err)
		}
		if _, err := s.SetWebAuthnHandle(ctx, []byte("handle")); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetWebAuthnHandle without admin = %v, want ErrNotFound", err)
		}
	})

	t.Run("password sign-in is on by default and can be turned off and on", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAdmin(ctx, AdminRecord{Email: "a@example.com", PasswordHash: "h", CreatedAt: t0, PasswordChangedAt: t0}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []bool{true, false, true} {
			if got, _ := s.GetAdmin(ctx); got.PasswordSignIn != want {
				t.Fatalf("PasswordSignIn = %v, want %v", got.PasswordSignIn, want)
			}
			if err := s.SetPasswordSignIn(ctx, !want); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("the handle is set once", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAdmin(ctx, AdminRecord{Email: "a@example.com", PasswordHash: "h", CreatedAt: t0, PasswordChangedAt: t0}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetAdmin(ctx); got.WebAuthnHandle != nil {
			t.Fatalf("handle before any registration = %x, want none", got.WebAuthnHandle)
		}
		first, err := s.SetWebAuthnHandle(ctx, []byte("first-handle"))
		if err != nil || string(first) != "first-handle" {
			t.Fatalf("first SetWebAuthnHandle = %q, %v", first, err)
		}
		second, err := s.SetWebAuthnHandle(ctx, []byte("second-handle"))
		if err != nil || string(second) != "first-handle" {
			t.Errorf("second SetWebAuthnHandle = %q, %v; want the first one back", second, err)
		}
		if got, _ := s.GetAdmin(ctx); string(got.WebAuthnHandle) != "first-handle" {
			t.Errorf("stored handle = %q, want first-handle", got.WebAuthnHandle)
		}
	})

	t.Run("a new password leaves the sign-in setting alone", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAdmin(ctx, AdminRecord{Email: "a@example.com", PasswordHash: "h", CreatedAt: t0, PasswordChangedAt: t0}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetPasswordSignIn(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := s.SetPassword(ctx, "h2", t0, ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetAdmin(ctx); got.PasswordSignIn {
			t.Error("SetPassword turned password sign-in on; the service does that, not the store")
		}
	})
}
