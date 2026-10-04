package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestSignIn(t *testing.T) {
	ctx := context.Background()

	t.Run("success returns a 43-character value whose SHA-256 is the stored key", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		s, err := e.auth.SignIn(ctx, "  You@Example.com ", testPassword, "203.0.113.9")
		if err != nil {
			t.Fatalf("SignIn: %v", err)
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(s.Value) {
			t.Errorf("session value %q is not 43 base64url characters (256 bits)", s.Value)
		}
		if want := e.clock.Now().Add(7 * 24 * time.Hour); !s.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want %v", s.ExpiresAt, want)
		}

		rec, err := e.store.GetSession(ctx, hashToken(s.Value))
		if err != nil {
			t.Fatalf("the store has no session under SHA-256 of the value: %v", err)
		}
		if rec.IDHash == s.Value || !rec.ExpiresAt.Equal(s.ExpiresAt) {
			t.Errorf("stored session = %+v", rec)
		}
		if e.hashes != 1 {
			t.Errorf("sign-in ran %d password checks, want 1", e.hashes)
		}
	})

	t.Run("every sign-in gets a new value", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		a, _ := e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9")
		b, _ := e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9")
		if a.Value == b.Value {
			t.Error("two sign-ins share a session value")
		}
	})

	t.Run("a wrong email and a wrong password get the same error and cost one password check each", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)

		_, errEmail := e.auth.SignIn(ctx, "nobody@example.com", testPassword, "203.0.113.9")
		emailChecks := e.hashes
		e.hashes = 0
		_, errPassword := e.auth.SignIn(ctx, testEmail, "not the password", "203.0.113.9")
		passwordChecks := e.hashes

		for name, err := range map[string]error{"wrong email": errEmail, "wrong password": errPassword} {
			if !errors.Is(err, ErrBadCredentials) {
				t.Errorf("%s: SignIn = %v, want ErrBadCredentials", name, err)
			}
		}
		if errEmail.Error() != errPassword.Error() {
			t.Errorf("the two failures read differently: %q vs %q", errEmail, errPassword)
		}
		if emailChecks != 1 || passwordChecks != 1 {
			t.Errorf("password checks: wrong email %d, wrong password %d; want 1 each", emailChecks, passwordChecks)
		}
	})

	t.Run("a malformed email is just wrong credentials", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		if _, err := e.auth.SignIn(ctx, "not-an-email", testPassword, "203.0.113.9"); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("SignIn(malformed email) = %v, want ErrBadCredentials", err)
		}
	})

	t.Run("without an admin", func(t *testing.T) {
		e := newAuthEnv(t)
		if _, err := e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9"); !errors.Is(err, ErrNoAdmin) {
			t.Errorf("SignIn without admin = %v, want ErrNoAdmin", err)
		}
	})

	t.Run("expired rows are deleted on the next sign-in", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		old, _ := e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9")
		e.clock.Advance(8 * 24 * time.Hour)
		fresh, err := e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.store.GetSession(ctx, hashToken(old.Value)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("the expired session row is still there: %v", err)
		}
		if _, err := e.store.GetSession(ctx, hashToken(fresh.Value)); err != nil {
			t.Errorf("the new session row is missing: %v", err)
		}
	})

	t.Run("events are logged with the address and no secret", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		e.auth.SignIn(ctx, testEmail, "wrong password here", "203.0.113.9")
		s, _ := e.auth.SignIn(ctx, testEmail, testPassword, "198.51.100.7")
		e.auth.SignOut(ctx, s.Value)

		out := e.logs.String()
		for _, want := range []string{
			`"event":"signin_failed"`, `"address":"203.0.113.9"`,
			`"event":"signin"`, `"address":"198.51.100.7"`,
			`"event":"signout"`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("log lacks %s:\n%s", want, out)
			}
		}
		for _, secret := range []string{testPassword, "wrong password here", s.Value} {
			if strings.Contains(out, secret) {
				t.Errorf("a secret reached the log:\n%s", out)
			}
		}
	})
}

func TestCheckSession(t *testing.T) {
	ctx := context.Background()
	signedIn := func(t *testing.T) (*authEnv, Session) {
		t.Helper()
		e := newAuthEnvWithAdmin(t)
		s, err := e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9")
		if err != nil {
			t.Fatal(err)
		}
		return e, s
	}

	t.Run("a valid session", func(t *testing.T) {
		e, s := signedIn(t)
		if err := e.auth.CheckSession(ctx, s.Value); err != nil {
			t.Errorf("CheckSession = %v", err)
		}
	})

	t.Run("unknown, empty and tampered values", func(t *testing.T) {
		e, s := signedIn(t)
		for name, v := range map[string]string{
			"empty": "", "unknown": strings.Repeat("A", 43), "tampered": s.Value[:42] + "A" + "x",
			"the hash instead of the value": hashToken(s.Value),
		} {
			if err := e.auth.CheckSession(ctx, v); !errors.Is(err, ErrNotAuthenticated) {
				t.Errorf("CheckSession(%s) = %v, want ErrNotAuthenticated", name, err)
			}
		}
	})

	t.Run("12 hours without use ends it", func(t *testing.T) {
		e, s := signedIn(t)
		e.clock.Advance(12*time.Hour - time.Second)
		if err := e.auth.CheckSession(ctx, s.Value); err != nil {
			t.Fatalf("just before the idle limit: %v", err)
		}
		// That check was a use: the idle clock restarted.
		e.clock.Advance(12*time.Hour - time.Second)
		if err := e.auth.CheckSession(ctx, s.Value); err != nil {
			t.Fatalf("a use restarts the idle clock: %v", err)
		}
		e.clock.Advance(12 * time.Hour)
		if err := e.auth.CheckSession(ctx, s.Value); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("after 12 h idle: %v, want ErrNotAuthenticated", err)
		}
	})

	t.Run("7 days after sign-in ends it, however busy", func(t *testing.T) {
		e, s := signedIn(t)
		for range 13 { // used every 13 hours: never idle for 12
			e.clock.Advance(13 * time.Hour)
			if e.clock.Now().Sub(e.clockStart()) >= 7*24*time.Hour {
				break
			}
			e.auth.CheckSession(ctx, s.Value)
		}
		e.clock.Advance(time.Hour)
		if err := e.auth.CheckSession(ctx, s.Value); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("past 7 days: %v, want ErrNotAuthenticated", err)
		}
	})

	t.Run("last seen is written at most once a minute", func(t *testing.T) {
		e, s := signedIn(t)
		start := e.clock.Now()
		lastSeen := func() time.Time {
			rec, err := e.store.GetSession(ctx, hashToken(s.Value))
			if err != nil {
				t.Fatal(err)
			}
			return rec.LastSeenAt
		}
		e.clock.Advance(30 * time.Second)
		e.auth.CheckSession(ctx, s.Value)
		if got := lastSeen(); !got.Equal(start) {
			t.Errorf("last seen = %v after 30 s, want it unchanged (%v)", got, start)
		}
		e.clock.Advance(30 * time.Second)
		e.auth.CheckSession(ctx, s.Value)
		if got := lastSeen(); !got.Equal(start.Add(time.Minute)) {
			t.Errorf("last seen = %v after a minute, want %v", got, start.Add(time.Minute))
		}
	})
}

func TestSignOut(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	s, _ := e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9")
	other, _ := e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9")

	if err := e.auth.SignOut(ctx, s.Value); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	if _, err := e.store.GetSession(ctx, hashToken(s.Value)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the row is still there after sign-out: %v", err)
	}
	if err := e.auth.CheckSession(ctx, s.Value); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("CheckSession after sign-out = %v, want ErrNotAuthenticated", err)
	}
	if err := e.auth.CheckSession(ctx, other.Value); err != nil {
		t.Errorf("another session ended too: %v", err)
	}
	if err := e.auth.SignOut(ctx, s.Value); err != nil {
		t.Errorf("signing out twice = %v, want nil", err)
	}
	if err := e.auth.SignOut(ctx, ""); err != nil {
		t.Errorf("signing out with no cookie = %v, want nil", err)
	}
}
