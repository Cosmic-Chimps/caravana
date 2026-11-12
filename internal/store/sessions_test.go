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

func TestSessionVerifiedAt(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	s := openTestStore(t)

	// A session from before the column, or made without one, has none.
	if err := s.CreateSession(ctx, SessionRecord{IDHash: "old", CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(ctx, "old"); got.VerifiedAt != nil {
		t.Errorf("VerifiedAt of a session made without it = %v, want nil", got.VerifiedAt)
	}

	// CreateSession writes it, and SetSessionVerified moves it.
	if err := s.CreateSession(ctx, SessionRecord{IDHash: "new", CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour), VerifiedAt: &t0}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(ctx, "new"); got.VerifiedAt == nil || !got.VerifiedAt.Equal(t0) {
		t.Errorf("VerifiedAt after create = %v, want %v", got.VerifiedAt, t0)
	}
	later := t0.Add(30 * time.Minute)
	for _, id := range []string{"old", "new"} {
		if err := s.SetSessionVerified(ctx, id, later); err != nil {
			t.Fatalf("SetSessionVerified(%s): %v", id, err)
		}
		if got, _ := s.GetSession(ctx, id); got.VerifiedAt == nil || !got.VerifiedAt.Equal(later) {
			t.Errorf("VerifiedAt of %s after SetSessionVerified = %v, want %v", id, got.VerifiedAt, later)
		}
	}
	if err := s.SetSessionVerified(ctx, "nope", later); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetSessionVerified of an unknown session = %v, want ErrNotFound", err)
	}
}
