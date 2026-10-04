package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAPITokens(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	token := func(id, name, hash string, created time.Time) APITokenRecord {
		return APITokenRecord{ID: id, Name: name, Hash: hash, CreatedAt: created}
	}

	t.Run("create and get by hash", func(t *testing.T) {
		s := openTestStore(t)
		exp := t0.Add(24 * time.Hour)
		want := token("id1", "ci", "h1", t0)
		want.ExpiresAt = &exp
		if err := s.CreateAPIToken(ctx, want, t0); err != nil {
			t.Fatalf("CreateAPIToken: %v", err)
		}
		got, err := s.GetAPITokenByHash(ctx, "h1")
		if err != nil || got.ID != "id1" || got.Name != "ci" || got.LastUsedAt != nil || got.RevokedAt != nil ||
			got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
			t.Errorf("GetAPITokenByHash = %+v, %v", got, err)
		}
		if _, err := s.GetAPITokenByHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetAPITokenByHash(nope) = %v, want ErrNotFound", err)
		}
	})

	t.Run("the name is unique among active tokens only", func(t *testing.T) {
		s := openTestStore(t)
		exp := t0.Add(time.Hour)
		expiring := token("id1", "ci", "h1", t0)
		expiring.ExpiresAt = &exp
		if err := s.CreateAPIToken(ctx, expiring, t0); err != nil {
			t.Fatal(err)
		}
		// Active: refused.
		if err := s.CreateAPIToken(ctx, token("id2", "ci", "h2", t0), t0); !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("same name while active = %v, want ErrAlreadyExists", err)
		}
		// Expired by then: allowed.
		after := t0.Add(2 * time.Hour)
		if err := s.CreateAPIToken(ctx, token("id2", "ci", "h2", after), after); err != nil {
			t.Fatalf("same name after expiry: %v", err)
		}
		// The new one is active again; revoke it, and the name is free.
		if err := s.CreateAPIToken(ctx, token("id3", "ci", "h3", after), after); !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("third active = %v, want ErrAlreadyExists", err)
		}
		if _, err := s.RevokeAPIToken(ctx, "id2", after); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateAPIToken(ctx, token("id3", "ci", "h3", after), after); err != nil {
			t.Fatalf("same name after revoke: %v", err)
		}
		// A different name was never in the way.
		if err := s.CreateAPIToken(ctx, token("id4", "other", "h4", after), after); err != nil {
			t.Fatalf("different name: %v", err)
		}
	})

	t.Run("the hash is unique", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAPIToken(ctx, token("id1", "a", "same", t0), t0); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateAPIToken(ctx, token("id2", "b", "same", t0), t0); err == nil {
			t.Error("a second token with the same hash was accepted")
		}
	})

	t.Run("list is newest first", func(t *testing.T) {
		s := openTestStore(t)
		// Same created_at for the last two: rowid breaks the tie.
		for i, r := range []APITokenRecord{
			token("id1", "a", "h1", t0),
			token("id2", "b", "h2", t0.Add(time.Minute)),
			token("id3", "c", "h3", t0.Add(time.Minute)),
		} {
			if err := s.CreateAPIToken(ctx, r, t0); err != nil {
				t.Fatalf("create #%d: %v", i, err)
			}
		}
		got, err := s.ListAPITokens(ctx)
		if err != nil {
			t.Fatalf("ListAPITokens: %v", err)
		}
		var ids []string
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		if len(ids) != 3 || ids[0] != "id3" || ids[1] != "id2" || ids[2] != "id1" {
			t.Errorf("list order = %v, want [id3 id2 id1]", ids)
		}
	})

	t.Run("revoke twice, and an unknown ID", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAPIToken(ctx, token("id1", "a", "h1", t0), t0); err != nil {
			t.Fatal(err)
		}
		first := t0.Add(time.Minute)
		if name, err := s.RevokeAPIToken(ctx, "id1", first); err != nil || name != "a" {
			t.Fatalf("revoke = %q, %v; want the name \"a\"", name, err)
		}
		if _, err := s.RevokeAPIToken(ctx, "id1", first.Add(time.Hour)); err != nil {
			t.Fatalf("second revoke: %v", err)
		}
		got, _ := s.GetAPITokenByHash(ctx, "h1")
		if got.RevokedAt == nil || !got.RevokedAt.Equal(first) {
			t.Errorf("RevokedAt = %v after two revokes, want the first time %v", got.RevokedAt, first)
		}
		if _, err := s.RevokeAPIToken(ctx, "nope", first); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoke unknown = %v, want ErrNotFound", err)
		}
	})

	t.Run("touch and find the active token by name", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateAPIToken(ctx, token("id1", "a", "h1", t0), t0); err != nil {
			t.Fatal(err)
		}
		used := t0.Add(time.Minute)
		if err := s.TouchAPIToken(ctx, "id1", used); err != nil {
			t.Fatalf("touch: %v", err)
		}
		got, err := s.ActiveAPITokenByName(ctx, "a", used)
		if err != nil || got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
			t.Fatalf("ActiveAPITokenByName = %+v, %v", got, err)
		}
		if _, err := s.RevokeAPIToken(ctx, "id1", used); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ActiveAPITokenByName(ctx, "a", used); !errors.Is(err, ErrNotFound) {
			t.Errorf("ActiveAPITokenByName after revoke = %v, want ErrNotFound", err)
		}
	})
}
