package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSigninLimits(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	prune := t0.Add(-24 * time.Hour)

	t.Run("put, get, replace, delete", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.GetSigninLimit(ctx, "1.2.3.4"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetSigninLimit before put = %v, want ErrNotFound", err)
		}
		if err := s.PutSigninLimit(ctx, SigninLimitRecord{Address: "1.2.3.4", Failures: 1, WindowStart: t0}, prune); err != nil {
			t.Fatalf("PutSigninLimit: %v", err)
		}
		locked := t0.Add(15 * time.Minute)
		want := SigninLimitRecord{Address: "1.2.3.4", Failures: 5, WindowStart: t0, LockedUntil: &locked}
		if err := s.PutSigninLimit(ctx, want, prune); err != nil {
			t.Fatalf("PutSigninLimit (replace): %v", err)
		}
		got, err := s.GetSigninLimit(ctx, "1.2.3.4")
		if err != nil || got.Failures != 5 || !got.WindowStart.Equal(t0) || got.LockedUntil == nil || !got.LockedUntil.Equal(locked) {
			t.Fatalf("GetSigninLimit = %+v, %v; want %+v", got, err, want)
		}
		for i := range 2 {
			if err := s.DeleteSigninLimit(ctx, "1.2.3.4"); err != nil {
				t.Fatalf("DeleteSigninLimit #%d: %v", i+1, err)
			}
		}
		if _, err := s.GetSigninLimit(ctx, "1.2.3.4"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSigninLimit after delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("put prunes old windows", func(t *testing.T) {
		s := openTestStore(t)
		old := SigninLimitRecord{Address: "old", Failures: 2, WindowStart: t0.Add(-48 * time.Hour)}
		recent := SigninLimitRecord{Address: "recent", Failures: 2, WindowStart: t0.Add(-time.Hour)}
		// Written with an early pruneBefore, so both stay.
		for _, r := range []SigninLimitRecord{old, recent} {
			if err := s.PutSigninLimit(ctx, r, t0.Add(-72*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutSigninLimit(ctx, SigninLimitRecord{Address: "new", Failures: 1, WindowStart: t0}, prune); err != nil {
			t.Fatal(err)
		}
		for addr, wantFound := range map[string]bool{"old": false, "recent": true, "new": true} {
			_, err := s.GetSigninLimit(ctx, addr)
			if (err == nil) != wantFound {
				t.Errorf("record %q found = %v, want %v", addr, err == nil, wantFound)
			}
		}
	})
}
