package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	session := func(id string, created time.Time) SessionRecord {
		return SessionRecord{IDHash: id, CreatedAt: created, LastSeenAt: created, ExpiresAt: created.Add(7 * 24 * time.Hour)}
	}

	t.Run("create, get, touch", func(t *testing.T) {
		s := openTestStore(t)
		want := session("a", t0)
		if err := s.CreateSession(ctx, want); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		got, err := s.GetSession(ctx, "a")
		if err != nil || got != want {
			t.Fatalf("GetSession = %+v, %v; want %+v", got, err, want)
		}
		later := t0.Add(5 * time.Minute)
		if err := s.TouchSession(ctx, "a", later); err != nil {
			t.Fatalf("TouchSession: %v", err)
		}
		if got, _ := s.GetSession(ctx, "a"); !got.LastSeenAt.Equal(later) || !got.CreatedAt.Equal(t0) {
			t.Errorf("session after touch = %+v", got)
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSession(nope) = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete is fine when gone", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreateSession(ctx, session("a", t0)); err != nil {
			t.Fatal(err)
		}
		for i := range 2 {
			if err := s.DeleteSession(ctx, "a"); err != nil {
				t.Fatalf("DeleteSession #%d: %v", i+1, err)
			}
		}
		if _, err := s.GetSession(ctx, "a"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSession after delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("expired and idle sessions are deleted", func(t *testing.T) {
		s := openTestStore(t)
		now := t0.Add(8 * 24 * time.Hour)
		idle := 12 * time.Hour

		old := session("past-expiry", t0) // expired a day ago
		old.LastSeenAt = now.Add(-time.Minute)
		stale := session("idle", now.Add(-48*time.Hour)) // idle for 24 h
		fresh := session("fresh", now.Add(-time.Hour))
		for _, r := range []SessionRecord{old, stale, fresh} {
			if err := s.CreateSession(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteExpiredSessions(ctx, now, idle); err != nil {
			t.Fatalf("DeleteExpiredSessions: %v", err)
		}
		for id, wantFound := range map[string]bool{"past-expiry": false, "idle": false, "fresh": true} {
			_, err := s.GetSession(ctx, id)
			if (err == nil) != wantFound {
				t.Errorf("session %q found = %v, want %v", id, err == nil, wantFound)
			}
		}
	})
}
