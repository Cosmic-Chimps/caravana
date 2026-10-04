package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var tokenFormat = regexp.MustCompile(`^cvat_[A-Za-z0-9_-]{43}$`)

func TestCreateAPIToken(t *testing.T) {
	ctx := context.Background()

	t.Run("format and storage", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, err := e.auth.CreateAPIToken(ctx, "ci-github", nil)
		if err != nil {
			t.Fatalf("CreateAPIToken: %v", err)
		}
		if !tokenFormat.MatchString(created.Token) {
			t.Errorf("token %q does not match %s", created.Token, tokenFormat)
		}
		if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(created.Info.ID) {
			t.Errorf("ID %q is not 26 lowercase base32 characters", created.Info.ID)
		}
		if created.Info.Name != "ci-github" || created.Info.State != TokenActive || created.Info.ExpiresAt != nil {
			t.Errorf("Info = %+v", created.Info)
		}

		// The store has a hash, never the token.
		stored, err := e.store.ListAPITokens(ctx)
		if err != nil || len(stored) != 1 {
			t.Fatalf("ListAPITokens = %v, %v", stored, err)
		}
		if stored[0].Hash == created.Token || strings.Contains(stored[0].Hash, created.Token[len("cvat_"):]) {
			t.Error("the store holds the token itself")
		}
		if stored[0].Hash != hashToken(created.Token) {
			t.Error("the store's hash is not the SHA-256 of the token")
		}
	})

	t.Run("two tokens differ", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		a, _ := e.auth.CreateAPIToken(ctx, "a", nil)
		b, _ := e.auth.CreateAPIToken(ctx, "b", nil)
		if a.Token == b.Token || a.Info.ID == b.Info.ID {
			t.Error("two tokens share a value or an ID")
		}
	})

	t.Run("needs an admin", func(t *testing.T) {
		e := newAuthEnv(t)
		if _, err := e.auth.CreateAPIToken(ctx, "ci", nil); !errors.Is(err, ErrNoAdmin) {
			t.Errorf("CreateAPIToken without admin = %v, want ErrNoAdmin", err)
		}
	})

	t.Run("name rules", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for _, name := range []string{"", "-lead", ".lead", "_lead", "has space", "slash/name", "ünï", strings.Repeat("a", 65)} {
			_, err := e.auth.CreateAPIToken(ctx, name, nil)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != "name" {
				t.Errorf("CreateAPIToken(%q) = %v, want a field error naming name", name, err)
			}
		}
		for _, name := range []string{"a", "0", "ci.github_1-prod", strings.Repeat("a", 64)} {
			if _, err := e.auth.CreateAPIToken(ctx, name, nil); err != nil {
				t.Errorf("CreateAPIToken(%q) = %v, want ok", name, err)
			}
		}
	})

	t.Run("expiry must be in the future", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for _, at := range []time.Time{e.clock.Now(), e.clock.Now().Add(-time.Hour)} {
			_, err := e.auth.CreateAPIToken(ctx, "ci", &at)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != "expiresAt" {
				t.Errorf("CreateAPIToken expiring at %v = %v, want a field error naming expiresAt", at, err)
			}
		}
		future := e.clock.Now().Add(time.Hour)
		created, err := e.auth.CreateAPIToken(ctx, "ci", &future)
		if err != nil || created.Info.ExpiresAt == nil || !created.Info.ExpiresAt.Equal(future) {
			t.Errorf("CreateAPIToken with a future expiry = %+v, %v", created.Info, err)
		}
	})

	t.Run("an active name is taken", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		if _, err := e.auth.CreateAPIToken(ctx, "ci", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.auth.CreateAPIToken(ctx, "ci", nil); !errors.Is(err, ErrTokenNameTaken) {
			t.Errorf("second CreateAPIToken(ci) = %v, want ErrTokenNameTaken", err)
		}
	})

	t.Run("the creation is logged with the name, not the token", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, _ := e.auth.CreateAPIToken(ctx, "ci-github", nil)
		out := e.logs.String()
		if !strings.Contains(out, `"event":"token_created"`) || !strings.Contains(out, `"tokenName":"ci-github"`) {
			t.Errorf("log lacks the token_created event with the name:\n%s", out)
		}
		if strings.Contains(out, created.Token) || strings.Contains(out, created.Token[len("cvat_"):]) {
			t.Errorf("the token reached the log:\n%s", out)
		}
	})
}

func TestCheckAPIToken(t *testing.T) {
	ctx := context.Background()

	t.Run("accepts only an active token", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, _ := e.auth.CreateAPIToken(ctx, "ci", nil)

		if err := e.auth.CheckAPIToken(ctx, created.Token); err != nil {
			t.Errorf("CheckAPIToken(valid) = %v", err)
		}
		for name, token := range map[string]string{
			"empty":          "",
			"unknown":        "cvat_" + strings.Repeat("A", 43),
			"no prefix":      created.Token[len("cvat_"):],
			"truncated":      created.Token[:20],
			"webhook token":  "cvwh_" + strings.Repeat("A", 43),
			"wrong prefix":   "cvat-" + created.Token[len("cvat_"):],
			"extra trailing": created.Token + " ",
		} {
			if err := e.auth.CheckAPIToken(ctx, token); !errors.Is(err, ErrNotAuthenticated) {
				t.Errorf("CheckAPIToken(%s) = %v, want ErrNotAuthenticated", name, err)
			}
		}
	})

	t.Run("a token without the cvat_ prefix never reaches the store", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, _ := e.auth.CreateAPIToken(ctx, "ci", nil)
		e.store.Close() // any lookup would now fail with a store error, not a 401

		if err := e.auth.CheckAPIToken(ctx, "cvwh_abc"); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("CheckAPIToken(cvwh_) with a closed store = %v, want ErrNotAuthenticated", err)
		}
		err := e.auth.CheckAPIToken(ctx, created.Token)
		if err == nil || errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("CheckAPIToken(cvat_) with a closed store = %v, want a store error", err)
		}
	})

	t.Run("expiry follows the clock", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		exp := e.clock.Now().Add(time.Hour)
		created, _ := e.auth.CreateAPIToken(ctx, "ci", &exp)

		e.clock.Advance(59 * time.Minute)
		if err := e.auth.CheckAPIToken(ctx, created.Token); err != nil {
			t.Errorf("CheckAPIToken before expiry = %v", err)
		}
		e.clock.Advance(time.Minute)
		if err := e.auth.CheckAPIToken(ctx, created.Token); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("CheckAPIToken at expiry = %v, want ErrNotAuthenticated", err)
		}
	})

	t.Run("a revoked token is refused", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, _ := e.auth.CreateAPIToken(ctx, "ci", nil)
		if _, err := e.store.RevokeAPIToken(ctx, created.Info.ID, e.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := e.auth.CheckAPIToken(ctx, created.Token); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("CheckAPIToken(revoked) = %v, want ErrNotAuthenticated", err)
		}
	})

	t.Run("last use is written at most once a minute", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, _ := e.auth.CreateAPIToken(ctx, "ci", nil)
		lastUsed := func() *time.Time {
			rec, err := e.store.GetAPITokenByHash(ctx, hashToken(created.Token))
			if err != nil {
				t.Fatal(err)
			}
			return rec.LastUsedAt
		}
		if lastUsed() != nil {
			t.Fatal("a new token has a last-used time")
		}

		start := e.clock.Now()
		e.auth.CheckAPIToken(ctx, created.Token)
		if got := lastUsed(); got == nil || !got.Equal(start) {
			t.Fatalf("last used = %v after the first use, want %v", got, start)
		}
		e.clock.Advance(30 * time.Second)
		e.auth.CheckAPIToken(ctx, created.Token)
		if got := lastUsed(); !got.Equal(start) {
			t.Errorf("last used = %v after 30 s, want it unchanged (%v)", got, start)
		}
		e.clock.Advance(30 * time.Second)
		e.auth.CheckAPIToken(ctx, created.Token)
		if got := lastUsed(); !got.Equal(start.Add(time.Minute)) {
			t.Errorf("last used = %v after a minute, want %v", got, start.Add(time.Minute))
		}
	})
}

func TestListAPITokens(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)

	// Created a minute apart: "old" first, then "gone" (revoked later),
	// "short" (expires), "fresh".
	old, _ := e.auth.CreateAPIToken(ctx, "old", nil)
	e.clock.Advance(time.Minute)
	gone, _ := e.auth.CreateAPIToken(ctx, "gone", nil)
	e.clock.Advance(time.Minute)
	soon := e.clock.Now().Add(time.Hour)
	short, _ := e.auth.CreateAPIToken(ctx, "short", &soon)
	e.clock.Advance(time.Minute)
	fresh, _ := e.auth.CreateAPIToken(ctx, "fresh", nil)

	if err := e.auth.RevokeAPIToken(ctx, gone.Info.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Hour) // "short" has now expired

	got, err := e.auth.ListAPITokens(ctx)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	wantOrder := []struct{ id, state string }{
		{fresh.Info.ID, TokenActive},
		{short.Info.ID, TokenExpired},
		{gone.Info.ID, TokenRevoked},
		{old.Info.ID, TokenActive},
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("listed %d tokens, want %d: %+v", len(got), len(wantOrder), got)
	}
	for i, w := range wantOrder {
		if got[i].ID != w.id || got[i].State != w.state {
			t.Errorf("entry %d = %s (%s), want %s (%s)", i, got[i].Name, got[i].State, w.id, w.state)
		}
	}
	if got[2].RevokedAt == nil || got[1].ExpiresAt == nil {
		t.Errorf("revokedAt / expiresAt missing: %+v", got)
	}

	// A listing never carries a token value or its hash.
	for _, c := range []CreatedAPIToken{old, gone, short, fresh} {
		if strings.Contains(fmt.Sprintf("%+v", got), c.Token[len("cvat_"):]) || strings.Contains(fmt.Sprintf("%+v", got), hashToken(c.Token)) {
			t.Errorf("the listing holds a token value or hash: %+v", got)
		}
	}
}

func TestRevokeAPIToken(t *testing.T) {
	ctx := context.Background()

	t.Run("then the check fails; twice is fine; unknown ID is not found", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, _ := e.auth.CreateAPIToken(ctx, "ci", nil)
		if err := e.auth.CheckAPIToken(ctx, created.Token); err != nil {
			t.Fatal(err)
		}
		if err := e.auth.RevokeAPIToken(ctx, created.Info.ID); err != nil {
			t.Fatalf("RevokeAPIToken: %v", err)
		}
		if err := e.auth.CheckAPIToken(ctx, created.Token); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("CheckAPIToken after revoke = %v, want ErrNotAuthenticated", err)
		}
		if err := e.auth.RevokeAPIToken(ctx, created.Info.ID); err != nil {
			t.Errorf("second RevokeAPIToken = %v, want nil", err)
		}
		if err := e.auth.RevokeAPIToken(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RevokeAPIToken(nope) = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("by name", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, _ := e.auth.CreateAPIToken(ctx, "ci", nil)
		if err := e.auth.RevokeAPITokenByName(ctx, "ci"); err != nil {
			t.Fatalf("RevokeAPITokenByName: %v", err)
		}
		if err := e.auth.CheckAPIToken(ctx, created.Token); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("CheckAPIToken after revoke by name = %v, want ErrNotAuthenticated", err)
		}
		if err := e.auth.RevokeAPITokenByName(ctx, "ci"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RevokeAPITokenByName of a revoked name = %v, want store.ErrNotFound", err)
		}
		if err := e.auth.RevokeAPITokenByName(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RevokeAPITokenByName(nope) = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("a name is reusable once its token expired or was revoked", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		exp := e.clock.Now().Add(time.Hour)
		if _, err := e.auth.CreateAPIToken(ctx, "ci", &exp); err != nil {
			t.Fatal(err)
		}
		if _, err := e.auth.CreateAPIToken(ctx, "ci", nil); !errors.Is(err, ErrTokenNameTaken) {
			t.Fatalf("while active: %v, want ErrTokenNameTaken", err)
		}
		e.clock.Advance(2 * time.Hour)
		second, err := e.auth.CreateAPIToken(ctx, "ci", nil)
		if err != nil {
			t.Fatalf("after expiry: %v", err)
		}
		if _, err := e.auth.CreateAPIToken(ctx, "ci", nil); !errors.Is(err, ErrTokenNameTaken) {
			t.Fatalf("while the second is active: %v, want ErrTokenNameTaken", err)
		}
		if err := e.auth.RevokeAPIToken(ctx, second.Info.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.auth.CreateAPIToken(ctx, "ci", nil); err != nil {
			t.Errorf("after revoke: %v", err)
		}
	})

	t.Run("the revocation is logged with the name", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, _ := e.auth.CreateAPIToken(ctx, "ci-github", nil)
		if err := e.auth.RevokeAPIToken(ctx, created.Info.ID); err != nil {
			t.Fatal(err)
		}
		out := e.logs.String()
		if !strings.Contains(out, `"event":"token_revoked"`) || !strings.Contains(out, `"tokenName":"ci-github"`) {
			t.Errorf("log lacks token_revoked with the name:\n%s", out)
		}
	})
}
