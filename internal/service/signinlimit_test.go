package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

const addrA, addrB = "203.0.113.9", "198.51.100.7"

// fail makes one wrong sign-in from address and returns its error.
func (e *authEnv) fail(address string) error {
	_, err := e.auth.SignIn(context.Background(), testEmail, "not the password", address)
	return err
}

// triesLeft returns the tries left a failed sign-in reported, failing the
// test if it was not a plain failure.
func triesLeft(t *testing.T, err error) int {
	t.Helper()
	var f *SigninFailure
	if !errors.As(err, &f) {
		t.Fatalf("error = %v, want a *SigninFailure", err)
	}
	return f.TriesLeft
}

func TestSigninLimit(t *testing.T) {
	ctx := context.Background()

	t.Run("tries left count down, the fifth failure locks", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for _, want := range []int{4, 3, 2, 1} {
			if got := triesLeft(t, e.fail(addrA)); got != want {
				t.Errorf("tries left = %d, want %d", got, want)
			}
		}
		err := e.fail(addrA)
		var limited *SigninLimitError
		if !errors.As(err, &limited) || limited.RetryAfter != 15*time.Minute {
			t.Fatalf("fifth failure = %v, want a *SigninLimitError with 15 minutes", err)
		}
		if !errors.Is(err, ErrSigninLimited) {
			t.Error("the limit error does not wrap ErrSigninLimited")
		}
	})

	t.Run("while locked, even the right password is refused and no hash runs", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for range 5 {
			e.fail(addrA)
		}
		e.hashes = 0

		e.clock.Advance(3 * time.Minute)
		_, err := e.auth.SignIn(ctx, testEmail, testPassword, addrA)
		var limited *SigninLimitError
		if !errors.As(err, &limited) || limited.RetryAfter != 12*time.Minute {
			t.Fatalf("right password while locked = %v, want a limit error with 12 minutes", err)
		}
		if err := e.fail(addrA); !errors.Is(err, ErrSigninLimited) {
			t.Errorf("wrong password while locked = %v, want ErrSigninLimited", err)
		}
		if e.hashes != 0 {
			t.Errorf("%d password checks ran while locked, want 0", e.hashes)
		}
		// Trying while locked doesn't extend the lock.
		e.clock.Advance(12 * time.Minute)
		if _, err := e.auth.SignIn(ctx, testEmail, testPassword, addrA); err != nil {
			t.Errorf("sign-in when the wait ended = %v, want success", err)
		}
	})

	t.Run("after the wait the count starts over", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for range 5 {
			e.fail(addrA)
		}
		e.clock.Advance(15 * time.Minute)
		if got := triesLeft(t, e.fail(addrA)); got != 4 {
			t.Errorf("tries left after the wait = %d, want 4", got)
		}
	})

	t.Run("a failure after 15 minutes of quiet starts a new window", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for range 3 {
			e.fail(addrA)
		}
		e.clock.Advance(15 * time.Minute)
		if got := triesLeft(t, e.fail(addrA)); got != 4 {
			t.Errorf("tries left = %d, want 4 (the old window is over)", got)
		}
	})

	t.Run("failures inside the window add up", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		e.fail(addrA)
		e.clock.Advance(14 * time.Minute)
		if got := triesLeft(t, e.fail(addrA)); got != 3 {
			t.Errorf("tries left = %d, want 3", got)
		}
	})

	t.Run("success deletes the row", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		e.fail(addrA)
		e.fail(addrA)
		if _, err := e.store.GetSigninLimit(ctx, addrA); err != nil {
			t.Fatalf("no row after failures: %v", err)
		}
		if _, err := e.auth.SignIn(ctx, testEmail, testPassword, addrA); err != nil {
			t.Fatal(err)
		}
		if _, err := e.store.GetSigninLimit(ctx, addrA); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("row after a successful sign-in = %v, want ErrNotFound", err)
		}
		if got := triesLeft(t, e.fail(addrA)); got != 4 {
			t.Errorf("tries left after success = %d, want 4", got)
		}
	})

	t.Run("a restart keeps the lock", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for range 5 {
			e.fail(addrA)
		}
		again := NewAuth(e.store, slog.New(slog.DiscardHandler), AuthOptions{Now: e.clock.Now, PasswordIterations: 1000})
		if _, err := again.SignIn(ctx, testEmail, testPassword, addrA); !errors.Is(err, ErrSigninLimited) {
			t.Errorf("sign-in through a new Auth on the same store = %v, want ErrSigninLimited", err)
		}
	})

	t.Run("another address is unaffected", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for range 5 {
			e.fail(addrA)
		}
		if _, err := e.auth.SignIn(ctx, testEmail, testPassword, addrB); err != nil {
			t.Errorf("sign-in from another address = %v, want success", err)
		}
		if got := triesLeft(t, e.fail(addrB)); got != 4 {
			t.Errorf("tries left for the other address = %d, want 4", got)
		}
	})

	t.Run("a wrong email counts like a wrong password", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		_, err := e.auth.SignIn(ctx, "nobody@example.com", testPassword, addrA)
		if got := triesLeft(t, err); got != 4 {
			t.Errorf("tries left after a wrong email = %d, want 4", got)
		}
	})

	t.Run("API tokens are never limited", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		created, err := e.auth.CreateAPIToken(ctx, "ci", nil)
		if err != nil {
			t.Fatal(err)
		}
		for range 6 {
			e.fail(addrA)
		}
		if err := e.auth.CheckAPIToken(ctx, created.Token); err != nil {
			t.Errorf("CheckAPIToken while the address is locked = %v", err)
		}
		if _, err := e.auth.CreateAPIToken(ctx, "ci-2", nil); err != nil {
			t.Errorf("CreateAPIToken while the address is locked = %v", err)
		}
	})

	t.Run("without an admin nothing is counted", func(t *testing.T) {
		e := newAuthEnv(t)
		for range 7 {
			if _, err := e.auth.SignIn(ctx, testEmail, testPassword, addrA); !errors.Is(err, ErrNoAdmin) {
				t.Fatalf("SignIn without admin = %v, want ErrNoAdmin", err)
			}
		}
		if _, err := e.store.GetSigninLimit(ctx, addrA); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a limit row exists without an admin: %v", err)
		}
	})

	t.Run("old rows are pruned", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		e.fail(addrA)
		e.clock.Advance(25 * time.Hour)
		e.fail(addrB) // any write prunes
		if _, err := e.store.GetSigninLimit(ctx, addrA); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a day-old row survived a later write: %v", err)
		}
	})

	t.Run("limiting is logged with the address", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for range 6 {
			e.fail(addrA)
		}
		out := e.logs.String()
		if !strings.Contains(out, `"event":"signin_limited"`) || !strings.Contains(out, `"address":"`+addrA+`"`) {
			t.Errorf("log lacks signin_limited with the address:\n%s", out)
		}
	})
}

// TestSigninLimitHoldsUnderParallelGuesses: a burst of simultaneous wrong
// guesses from one address can't test more than 5 passwords, even though none
// has been recorded yet when the others start.
func TestSigninLimitHoldsUnderParallelGuesses(t *testing.T) {
	e := newAuthEnvWithAdmin(t)
	var mu sync.Mutex
	e.auth.onHash = func() { mu.Lock(); e.hashes++; mu.Unlock() }

	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.fail(addrA)
		}()
	}
	wg.Wait()

	if e.hashes > 5 {
		t.Errorf("%d passwords were checked from one address in one burst, want at most 5", e.hashes)
	}
	if _, err := e.auth.SignIn(context.Background(), testEmail, testPassword, addrA); !errors.Is(err, ErrSigninLimited) {
		t.Errorf("the address is not locked after the burst: %v", err)
	}
}
