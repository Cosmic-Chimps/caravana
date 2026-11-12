package service

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/webauthntest"
)

var (
	rpMain  = RelyingParty{ID: "console.example.com", Origin: "https://console.example.com"}
	rpOther = RelyingParty{ID: "old.example.com", Origin: "https://old.example.com"}
)

// signedIn signs in with the password and returns the session value. A session made this way is
// confirmed.
func signedIn(t *testing.T, e *authEnv) string {
	t.Helper()
	s, err := e.auth.SignIn(context.Background(), testEmail, testPassword, "", addrA)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	return s.Value
}

// unconfirmedSession is a live session from before verified_at existed: it has none.
func unconfirmedSession(t *testing.T, e *authEnv, value string) string {
	t.Helper()
	now := e.clock.Now()
	err := e.store.CreateSession(context.Background(), store.SessionRecord{
		IDHash: hashToken(value), CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(sessionAbsolute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// addPasskey adds a passkey for rp with a new software authenticator, from a confirmed session.
func addPasskey(t *testing.T, e *authEnv, session string, rp RelyingParty, name string) *webauthntest.Authenticator {
	t.Helper()
	ctx := context.Background()
	opts, err := e.auth.BeginPasskeyRegistration(ctx, session, rp, addrA)
	if err != nil {
		t.Fatalf("BeginPasskeyRegistration: %v", err)
	}
	a := webauthntest.New(rp.ID)
	posted, err := a.Create(opts.Options, rp.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rp, name, opts.Ceremony, posted); err != nil {
		t.Fatalf("FinishPasskeyRegistration: %v", err)
	}
	return a
}

// signInOptions begins a passkey sign-in and has a sign what it was sent, for origin.
func signInOptions(t *testing.T, e *authEnv, rp RelyingParty, a *webauthntest.Authenticator, origin string) (ceremonyID string, posted []byte) {
	t.Helper()
	opts, err := e.auth.BeginPasskeySignIn(context.Background(), rp, addrA)
	if err != nil {
		t.Fatalf("BeginPasskeySignIn: %v", err)
	}
	posted, err = a.Get(opts.Options, origin)
	if err != nil {
		t.Fatal(err)
	}
	return opts.Ceremony, posted
}

// passkeySignIn is a whole passkey sign-in at rp from address.
func passkeySignIn(t *testing.T, e *authEnv, rp RelyingParty, a *webauthntest.Authenticator, address string) (Session, error) {
	t.Helper()
	id, posted := signInOptions(t, e, rp, a, rp.Origin)
	return e.auth.FinishPasskeySignIn(context.Background(), rp, id, posted, address)
}

// refusedWith checks err is a counted passkey failure with that many tries left.
func refusedWith(t *testing.T, what string, err error, triesLeft int) {
	t.Helper()
	var f *PasskeyFailure
	if !errors.As(err, &f) || !errors.Is(err, ErrPasskeyRefused) {
		t.Fatalf("%s = %v, want a *PasskeyFailure", what, err)
	}
	if f.TriesLeft != triesLeft {
		t.Errorf("%s: tries left = %d, want %d", what, f.TriesLeft, triesLeft)
	}
}

// lastFailureReason is the reason word of the newest passkey_signin_failed or
// passkey_confirm_failed log line.
func (e *authEnv) lastFailureReason(t *testing.T) string {
	t.Helper()
	reason := ""
	for _, line := range strings.Split(e.logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if ev, _ := rec["event"].(string); ev == "passkey_signin_failed" || ev == "passkey_confirm_failed" {
			reason, _ = rec["reason"].(string)
		}
	}
	return reason
}

func TestAddAndSignInWithAPasskey(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	session := signedIn(t, e)

	a := addPasskey(t, e, session, rpMain, "  MacBook ")
	list, err := e.auth.ListPasskeys(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListPasskeys = %v, %v", list, err)
	}
	p := list[0]
	if p.Name != "MacBook" || p.Domain != "console.example.com" || p.LastUsedAt != nil || len(p.ID) != 26 {
		t.Errorf("passkey = %+v; want a trimmed name, the domain, never used, a 26-character ID", p)
	}
	if !p.CreatedAt.Equal(e.clock.Now()) {
		t.Errorf("CreatedAt = %v, want %v", p.CreatedAt, e.clock.Now())
	}

	// The admin has a handle now, and the stored passkey carries it.
	admin, _ := e.store.GetAdmin(ctx)
	rec, _ := e.store.GetPasskey(ctx, p.ID)
	if len(admin.WebAuthnHandle) != 32 || string(rec.UserHandle) != string(admin.WebAuthnHandle) {
		t.Errorf("handle = %x, stored with the passkey = %x", admin.WebAuthnHandle, rec.UserHandle)
	}
	if !rec.UserVerified || rec.SignCount != 0 || rec.AttestationFormat != "none" {
		t.Errorf("stored passkey = %+v; want UV, count 0, attestation none", rec)
	}

	e.clock.Advance(time.Hour)
	s, err := passkeySignIn(t, e, rpMain, a, addrB)
	if err != nil {
		t.Fatalf("passkey sign-in: %v", err)
	}
	if err := e.auth.CheckSession(ctx, s.Value); err != nil {
		t.Errorf("the session of a passkey sign-in is not live: %v", err)
	}
	if ok, _ := e.auth.SessionConfirmed(ctx, s.Value); !ok {
		t.Error("a session that signed in with a passkey is not confirmed")
	}
	rec, _ = e.store.GetPasskey(ctx, p.ID)
	if rec.SignCount != 1 || rec.LastUsedAt == nil || !rec.LastUsedAt.Equal(e.clock.Now()) {
		t.Errorf("after the sign-in: count = %d, last used = %v", rec.SignCount, rec.LastUsedAt)
	}

	// A second sign-in counts on.
	if _, err := passkeySignIn(t, e, rpMain, a, addrB); err != nil {
		t.Fatalf("second passkey sign-in: %v", err)
	}
	if rec, _ = e.store.GetPasskey(ctx, p.ID); rec.SignCount != 2 {
		t.Errorf("sign count = %d, want 2", rec.SignCount)
	}

	// The log names our passkey ID and the address, and nothing the authenticator holds.
	logs := e.logs.String()
	for _, want := range []string{`"event":"passkey_added"`, `"event":"passkey_signin"`, `"passkey":"` + p.ID + `"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("log has no %s", want)
		}
	}
}

func TestPasskeySignInRefusals(t *testing.T) {
	// Each case builds what a browser would post, wrongly; each is refused, counted, and no session
	// starts. reason is the word the log gets.
	cases := []struct {
		name   string
		reason string
		try    func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error
	}{
		{"made for another domain", "domain", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			// Stored at rpMain; the page is rpOther, and the device signs for it correctly.
			b := *a
			b.RPID = rpOther.ID
			id, posted := signInOptions(t, e, rpOther, &b, rpOther.Origin)
			_, err := e.auth.FinishPasskeySignIn(context.Background(), rpOther, id, posted, addrA)
			return err
		}},
		{"signed for another domain than the page's", "verification", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			id, posted := signInOptions(t, e, rpMain, a, "https://evil.example.net")
			_, err := e.auth.FinishPasskeySignIn(context.Background(), rpMain, id, posted, addrA)
			return err
		}},
		{"a replayed answer", "ceremony", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			id, posted := signInOptions(t, e, rpMain, a, rpMain.Origin)
			if _, err := e.auth.FinishPasskeySignIn(context.Background(), rpMain, id, posted, addrB); err != nil {
				t.Fatalf("the first use of the answer: %v", err)
			}
			_, err := e.auth.FinishPasskeySignIn(context.Background(), rpMain, id, posted, addrA)
			return err
		}},
		{"a sign count that went backwards", "counter", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			a.Counter = 5
			if _, err := passkeySignIn(t, e, rpMain, a, addrB); err != nil {
				t.Fatalf("sign-in at count 5: %v", err)
			}
			a.Counter = 3
			_, err := passkeySignIn(t, e, rpMain, a, addrA)
			return err
		}},
		{"the same sign count again", "counter", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			a.Counter = 4
			if _, err := passkeySignIn(t, e, rpMain, a, addrB); err != nil {
				t.Fatalf("sign-in at count 4: %v", err)
			}
			a.Counter = 4
			_, err := passkeySignIn(t, e, rpMain, a, addrA)
			return err
		}},
		{"no user verification", "verification", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			a.UserVerified = false
			_, err := passkeySignIn(t, e, rpMain, a, addrA)
			return err
		}},
		{"another user's handle", "credential", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			a.UserHandle = []byte("someone else entirely, 32 bytes!")
			_, err := passkeySignIn(t, e, rpMain, a, addrA)
			return err
		}},
		{"a passkey that was never added", "credential", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			stranger := webauthntest.New(rpMain.ID)
			stranger.UserHandle = a.UserHandle // knows the handle, not a stored credential
			_, err := passkeySignIn(t, e, rpMain, stranger, addrA)
			return err
		}},
		{"an expired ceremony", "ceremony", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			id, posted := signInOptions(t, e, rpMain, a, rpMain.Origin)
			e.clock.Advance(ceremonyLife)
			_, err := e.auth.FinishPasskeySignIn(context.Background(), rpMain, id, posted, addrA)
			return err
		}},
		{"an unknown ceremony", "ceremony", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			_, posted := signInOptions(t, e, rpMain, a, rpMain.Origin)
			_, err := e.auth.FinishPasskeySignIn(context.Background(), rpMain, "never-issued", posted, addrA)
			return err
		}},
		{"a ceremony begun at another domain", "ceremony", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			id, posted := signInOptions(t, e, rpMain, a, rpMain.Origin)
			_, err := e.auth.FinishPasskeySignIn(context.Background(), rpOther, id, posted, addrA)
			return err
		}},
		{"a confirm ceremony used to sign in", "ceremony", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			opts, err := e.auth.BeginPasskeyConfirm(context.Background(), signedIn(t, e), rpMain, addrA)
			if err != nil {
				t.Fatal(err)
			}
			posted, _ := a.Get(opts.Options, rpMain.Origin)
			_, err = e.auth.FinishPasskeySignIn(context.Background(), rpMain, opts.Ceremony, posted, addrA)
			return err
		}},
		{"something that isn't a credential", "credential", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			opts, _ := e.auth.BeginPasskeySignIn(context.Background(), rpMain, addrA)
			_, err := e.auth.FinishPasskeySignIn(context.Background(), rpMain, opts.Ceremony, []byte(`{"id":"x"}`), addrA)
			return err
		}},
		{"a credential far too large", "credential", func(t *testing.T, e *authEnv, a *webauthntest.Authenticator) error {
			opts, _ := e.auth.BeginPasskeySignIn(context.Background(), rpMain, addrA)
			_, err := e.auth.FinishPasskeySignIn(context.Background(), rpMain, opts.Ceremony, []byte(strings.Repeat("x", maxCredentialBytes+1)), addrA)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAuthEnvWithAdmin(t)
			a := addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
			// Count what the setup used up: signedIn's password sign-in cleared its address.
			err := tc.try(t, e, a)
			refusedWith(t, tc.name, err, 4)
			if got := e.lastFailureReason(t); got != tc.reason {
				t.Errorf("logged reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

func TestPasskeyAnotherSessionsCeremony(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	one := signedIn(t, e)
	a := addPasskey(t, e, one, rpMain, "MacBook")
	two := signedIn(t, e)

	t.Run("confirm", func(t *testing.T) {
		opts, err := e.auth.BeginPasskeyConfirm(ctx, one, rpMain, addrA)
		if err != nil {
			t.Fatal(err)
		}
		posted, _ := a.Get(opts.Options, rpMain.Origin)
		refusedWith(t, "confirm by another session", e.auth.FinishPasskeyConfirm(ctx, two, rpMain, opts.Ceremony, posted, addrA), 4)
		if e.lastFailureReason(t) != "ceremony" {
			t.Errorf("reason = %q, want ceremony", e.lastFailureReason(t))
		}
	})

	t.Run("register", func(t *testing.T) {
		opts, err := e.auth.BeginPasskeyRegistration(ctx, one, rpMain, addrA)
		if err != nil {
			t.Fatal(err)
		}
		posted, _ := webauthntest.New(rpMain.ID).Create(opts.Options, rpMain.Origin)
		if _, err := e.auth.FinishPasskeyRegistration(ctx, two, rpMain, "Phone", opts.Ceremony, posted); !errors.Is(err, ErrPasskeyRefused) {
			t.Errorf("registration finished by another session = %v, want ErrPasskeyRefused", err)
		}
		if n, _ := e.store.CountPasskeys(ctx, ""); n != 1 {
			t.Errorf("passkeys = %d, want 1: nothing was added", n)
		}
	})
}

func TestPasskeyLimit(t *testing.T) {
	ctx := context.Background()

	t.Run("the sixth try is limited before anything is verified", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		a := addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
		for _, want := range []int{4, 3, 2, 1} {
			_, err := e.auth.FinishPasskeySignIn(ctx, rpMain, "never-issued", []byte("{}"), addrB)
			refusedWith(t, "failed sign-in", err, want)
		}
		// The fifth failure locks the address.
		_, err := e.auth.FinishPasskeySignIn(ctx, rpMain, "never-issued", []byte("{}"), addrB)
		var limited *SigninLimitError
		if !errors.As(err, &limited) || limited.RetryAfter != signinLockout {
			t.Fatalf("fifth failure = %v, want a *SigninLimitError of %v", err, signinLockout)
		}

		// Locked: a good passkey is refused, and its ceremony isn't even taken out of the table.
		id, posted := signInOptions(t, e, rpMain, a, rpMain.Origin)
		pending := e.auth.ceremonies.pending()
		_, err = e.auth.FinishPasskeySignIn(ctx, rpMain, id, posted, addrB)
		if !errors.As(err, &limited) {
			t.Fatalf("a good passkey from a locked address = %v, want a *SigninLimitError", err)
		}
		if got := e.auth.ceremonies.pending(); got != pending {
			t.Errorf("pending ceremonies %d -> %d: a locked address's ceremony was used", pending, got)
		}
		// The confirm path is held to the same limit.
		if err := e.auth.FinishPasskeyConfirm(ctx, signedIn(t, e), rpMain, id, posted, addrB); !errors.As(err, &limited) {
			t.Errorf("confirm from a locked address = %v, want a *SigninLimitError", err)
		}
		// Another address is untouched, and so is the password's own path from this one: it is the
		// same count, so the password is refused too.
		if _, err := passkeySignIn(t, e, rpMain, a, addrA); err != nil {
			t.Errorf("a good passkey from another address: %v", err)
		}
		if _, err := e.auth.SignIn(ctx, testEmail, testPassword, "", addrB); !errors.Is(err, ErrSigninLimited) {
			t.Errorf("password from the locked address = %v, want ErrSigninLimited", err)
		}
		// After the wait it works again.
		e.clock.Advance(signinLockout + time.Second)
		if _, err := passkeySignIn(t, e, rpMain, a, addrB); err != nil {
			t.Errorf("passkey after the wait: %v", err)
		}
	})

	t.Run("passkey and password failures share the count", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
		e.fail(addrB)
		e.fail(addrB)
		_, err := e.auth.FinishPasskeySignIn(ctx, rpMain, "never-issued", []byte("{}"), addrB)
		refusedWith(t, "the third failure", err, 2)
	})

	t.Run("asking for options is not counted", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for range 20 {
			if _, err := e.auth.BeginPasskeySignIn(ctx, rpMain, addrB); err != nil {
				t.Fatal(err)
			}
		}
		_, err := e.auth.FinishPasskeySignIn(ctx, rpMain, "never-issued", []byte("{}"), addrB)
		refusedWith(t, "first failure after 20 begins", err, 4)
		if got := e.auth.ceremonies.pending(); got != ceremonyPerAddress {
			t.Errorf("pending = %d, want the cap of %d per address", got, ceremonyPerAddress)
		}
	})

	t.Run("a good passkey clears the failures", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		a := addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
		e.fail(addrB)
		e.fail(addrB)
		if _, err := passkeySignIn(t, e, rpMain, a, addrB); err != nil {
			t.Fatal(err)
		}
		refusedWithSignin(t, e.fail(addrB), 4)
	})

	t.Run("a failed confirm counts like a failed sign-in", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		a := addPasskey(t, e, session, rpMain, "MacBook")
		opts, _ := e.auth.BeginPasskeyConfirm(ctx, session, rpMain, addrB)
		a.UserVerified = false
		posted, _ := a.Get(opts.Options, rpMain.Origin)
		refusedWith(t, "confirm without UV", e.auth.FinishPasskeyConfirm(ctx, session, rpMain, opts.Ceremony, posted, addrB), 4)
		if got := e.lastFailureReason(t); got != "verification" {
			t.Errorf("reason = %q, want verification", got)
		}
	})
}

func TestConfirmWindow(t *testing.T) {
	ctx := context.Background()

	t.Run("a session from before has not confirmed", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		old := unconfirmedSession(t, e, "old-session")
		if ok, err := e.auth.SessionConfirmed(ctx, old); err != nil || ok {
			t.Errorf("SessionConfirmed = %v, %v; want false", ok, err)
		}
		if _, err := e.auth.BeginPasskeyRegistration(ctx, old, rpMain, addrA); !errors.Is(err, ErrNotConfirmed) {
			t.Errorf("BeginPasskeyRegistration = %v, want ErrNotConfirmed", err)
		}
	})

	t.Run("signing in confirms for ten minutes", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		e.clock.Advance(confirmWindow - time.Second)
		if ok, _ := e.auth.SessionConfirmed(ctx, session); !ok {
			t.Error("not confirmed just under ten minutes after signing in")
		}
		opts, err := e.auth.BeginPasskeyRegistration(ctx, session, rpMain, addrA)
		if err != nil {
			t.Fatal(err)
		}
		// It lapses between begin and finish: the finish asks again.
		e.clock.Advance(2 * time.Second)
		posted, _ := webauthntest.New(rpMain.ID).Create(opts.Options, rpMain.Origin)
		if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "Phone", opts.Ceremony, posted); !errors.Is(err, ErrNotConfirmed) {
			t.Errorf("finish after the window = %v, want ErrNotConfirmed", err)
		}
	})

	t.Run("the password confirms, a wrong one counts", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		old := unconfirmedSession(t, e, "old-session")
		err := e.auth.VerifyPassword(ctx, old, "not the password", "", addrB)
		var f *SigninFailure
		if !errors.As(err, &f) || f.TriesLeft != 4 {
			t.Fatalf("wrong password = %v, want a *SigninFailure with 4 tries left", err)
		}
		if ok, _ := e.auth.SessionConfirmed(ctx, old); ok {
			t.Error("a wrong password confirmed the session")
		}
		if err := e.auth.VerifyPassword(ctx, old, testPassword, "", addrB); err != nil {
			t.Fatalf("right password: %v", err)
		}
		if ok, _ := e.auth.SessionConfirmed(ctx, old); !ok {
			t.Error("the right password did not confirm the session")
		}
		refusedWithSignin(t, e.fail(addrB), 4) // and the failures were cleared
		if _, err := e.auth.BeginPasskeyRegistration(ctx, old, rpMain, addrA); err != nil {
			t.Errorf("BeginPasskeyRegistration after confirming: %v", err)
		}
		e.clock.Advance(confirmWindow)
		if ok, _ := e.auth.SessionConfirmed(ctx, old); ok {
			t.Error("still confirmed ten minutes later")
		}
	})

	t.Run("a locked address can't confirm with the password", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		old := unconfirmedSession(t, e, "old-session")
		for range 5 {
			e.fail(addrB)
		}
		e.hashes = 0
		err := e.auth.VerifyPassword(ctx, old, testPassword, "", addrB)
		if !errors.Is(err, ErrSigninLimited) || e.hashes != 0 {
			t.Errorf("VerifyPassword from a locked address = %v after %d hashes; want ErrSigninLimited and none", err, e.hashes)
		}
	})

	t.Run("a passkey confirms", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		a := addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
		old := unconfirmedSession(t, e, "old-session")
		opts, err := e.auth.BeginPasskeyConfirm(ctx, old, rpMain, addrA)
		if err != nil {
			t.Fatalf("BeginPasskeyConfirm: %v", err)
		}
		posted, err := a.Get(opts.Options, rpMain.Origin)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.auth.FinishPasskeyConfirm(ctx, old, rpMain, opts.Ceremony, posted, addrA); err != nil {
			t.Fatalf("FinishPasskeyConfirm: %v", err)
		}
		if ok, _ := e.auth.SessionConfirmed(ctx, old); !ok {
			t.Error("a passkey did not confirm the session")
		}
		// Single use.
		refusedWith(t, "the same answer again", e.auth.FinishPasskeyConfirm(ctx, old, rpMain, opts.Ceremony, posted, addrA), 4)
	})

	t.Run("confirming needs a passkey for this domain", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		addPasskey(t, e, signedIn(t, e), rpOther, "Old domain")
		_, err := e.auth.BeginPasskeyConfirm(ctx, signedIn(t, e), rpMain, addrA)
		if !errors.Is(err, ErrNoPasskey) {
			t.Errorf("BeginPasskeyConfirm with only another domain's passkey = %v, want ErrNoPasskey", err)
		}
	})

	t.Run("a dead session", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		for name, err := range map[string]error{
			"BeginPasskeyConfirm":      func() error { _, err := e.auth.BeginPasskeyConfirm(ctx, "nope", rpMain, addrA); return err }(),
			"BeginPasskeyRegistration": func() error { _, err := e.auth.BeginPasskeyRegistration(ctx, "nope", rpMain, addrA); return err }(),
			"VerifyPassword":           e.auth.VerifyPassword(ctx, "nope", testPassword, "", addrA),
		} {
			if !errors.Is(err, ErrNotAuthenticated) {
				t.Errorf("%s with a dead session = %v, want ErrNotAuthenticated", name, err)
			}
		}
		if _, err := e.auth.SessionConfirmed(ctx, "nope"); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("SessionConfirmed = %v, want ErrNotAuthenticated", err)
		}
	})
}

// refusedWithSignin checks err is a *SigninFailure with that many tries left.
func refusedWithSignin(t *testing.T, err error, triesLeft int) {
	t.Helper()
	var f *SigninFailure
	if !errors.As(err, &f) || f.TriesLeft != triesLeft {
		t.Fatalf("error = %v, want a *SigninFailure with %d tries left", err, triesLeft)
	}
}

func TestRegistration(t *testing.T) {
	ctx := context.Background()

	t.Run("one handle for every passkey, other passkeys are excluded", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		first := addPasskey(t, e, session, rpMain, "MacBook")

		opts, err := e.auth.BeginPasskeyRegistration(ctx, session, rpMain, addrA)
		if err != nil {
			t.Fatal(err)
		}
		var options struct {
			Exclude []struct {
				ID string `json:"id"`
			} `json:"excludeCredentials"`
			Selection struct {
				ResidentKey string `json:"residentKey"`
				UV          string `json:"userVerification"`
			} `json:"authenticatorSelection"`
			Attestation string `json:"attestation"`
			User        struct {
				ID, Name string
			} `json:"user"`
		}
		if err := json.Unmarshal(opts.Options, &options); err != nil {
			t.Fatal(err)
		}
		if len(options.Exclude) != 1 || options.Exclude[0].ID != base64.RawURLEncoding.EncodeToString(first.CredentialID()) {
			t.Errorf("excludeCredentials = %+v, want the first passkey", options.Exclude)
		}
		if options.Selection.ResidentKey != "required" || options.Selection.UV != "required" || options.Attestation != "none" {
			t.Errorf("options ask for %+v, attestation %q; want resident key and UV required, no attestation", options.Selection, options.Attestation)
		}
		admin, _ := e.store.GetAdmin(ctx)
		if options.User.ID != base64.RawURLEncoding.EncodeToString(admin.WebAuthnHandle) || options.User.Name != testEmail {
			t.Errorf("user = %+v, want the admin's handle and email", options.User)
		}

		// A second device registers; the handle is the same.
		second := webauthntest.New(rpMain.ID)
		posted, _ := second.Create(opts.Options, rpMain.Origin)
		if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "Phone", opts.Ceremony, posted); err != nil {
			t.Fatal(err)
		}
		after, _ := e.store.GetAdmin(ctx)
		if string(after.WebAuthnHandle) != string(admin.WebAuthnHandle) {
			t.Error("the admin's handle changed with the second passkey")
		}
	})

	t.Run("a passkey of another domain is not excluded, and is stored under its own", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		addPasskey(t, e, session, rpMain, "MacBook")
		addPasskey(t, e, session, rpOther, "Old domain")
		list, _ := e.auth.ListPasskeys(ctx)
		if len(list) != 2 || list[0].Domain != rpMain.ID || list[1].Domain != rpOther.ID {
			t.Errorf("list = %+v", list)
		}
	})

	t.Run("the same authenticator twice", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		a := addPasskey(t, e, session, rpMain, "MacBook")
		// A device that ignores excludeCredentials and registers the same credential again.
		opts, _ := e.auth.BeginPasskeyRegistration(ctx, session, rpMain, addrA)
		posted, _ := a.Create(opts.Options, rpMain.Origin)
		if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "Again", opts.Ceremony, posted); !errors.Is(err, ErrPasskeyRefused) {
			t.Errorf("registering one credential twice = %v, want ErrPasskeyRefused", err)
		}
		if n, _ := e.store.CountPasskeys(ctx, ""); n != 1 {
			t.Errorf("passkeys = %d, want 1", n)
		}
	})

	t.Run("refusals add nothing", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		try := func(name string, tweak func(a *webauthntest.Authenticator) (string, RelyingParty)) {
			t.Run(name, func(t *testing.T) {
				opts, err := e.auth.BeginPasskeyRegistration(ctx, session, rpMain, addrA)
				if err != nil {
					t.Fatal(err)
				}
				a := webauthntest.New(rpMain.ID)
				origin, rp := rpMain.Origin, rpMain
				if o, r := tweak(a); o != "" {
					origin, rp = o, r
				}
				posted, _ := a.Create(opts.Options, origin)
				if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rp, "Phone", opts.Ceremony, posted); !errors.Is(err, ErrPasskeyRefused) {
					t.Errorf("err = %v, want ErrPasskeyRefused", err)
				}
				if n, _ := e.store.CountPasskeys(ctx, ""); n != 0 {
					t.Errorf("passkeys = %d, want none", n)
				}
			})
		}
		try("without user verification", func(a *webauthntest.Authenticator) (string, RelyingParty) {
			a.UserVerified = false
			return "", RelyingParty{}
		})
		try("signed for another domain", func(a *webauthntest.Authenticator) (string, RelyingParty) {
			a.RPID = rpOther.ID
			return "", RelyingParty{}
		})
		try("made at another origin", func(a *webauthntest.Authenticator) (string, RelyingParty) {
			return "https://evil.example.net", rpMain
		})
		try("finished for another domain", func(a *webauthntest.Authenticator) (string, RelyingParty) {
			return rpOther.Origin, rpOther
		})
	})

	t.Run("a ceremony is single use and expires", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		opts, _ := e.auth.BeginPasskeyRegistration(ctx, session, rpMain, addrA)
		posted, _ := webauthntest.New(rpMain.ID).Create(opts.Options, rpMain.Origin)
		if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "Phone", opts.Ceremony, posted); err != nil {
			t.Fatal(err)
		}
		if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "Phone", opts.Ceremony, posted); !errors.Is(err, ErrPasskeyRefused) {
			t.Errorf("second finish = %v, want ErrPasskeyRefused", err)
		}

		opts, _ = e.auth.BeginPasskeyRegistration(ctx, session, rpMain, addrA)
		posted, _ = webauthntest.New(rpMain.ID).Create(opts.Options, rpMain.Origin)
		e.clock.Advance(ceremonyLife)
		// The session is still confirmed? Not after five minutes plus: stay inside the window.
		if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "Tablet", opts.Ceremony, posted); !errors.Is(err, ErrPasskeyRefused) {
			t.Errorf("finish of an expired ceremony = %v, want ErrPasskeyRefused", err)
		}
	})

	t.Run("names", func(t *testing.T) {
		for _, tc := range []struct {
			name, want string
			ok         bool
		}{
			{"MacBook", "MacBook", true},
			{"  Work laptop \t", "Work laptop", true},
			{strings.Repeat("é", 64), strings.Repeat("é", 64), true},
			{"", "", false},
			{"   ", "", false},
			{strings.Repeat("a", 65), "", false},
			{"bad\x00name", "", false},
			{"two\nlines", "", false},
		} {
			got, err := checkPasskeyName(tc.name)
			var fe *app.FieldError
			switch {
			case tc.ok && (err != nil || got != tc.want):
				t.Errorf("checkPasskeyName(%q) = %q, %v; want %q", tc.name, got, err, tc.want)
			case !tc.ok && (!errors.As(err, &fe) || fe.Field != "name"):
				t.Errorf("checkPasskeyName(%q) = %q, %v; want an *app.FieldError on name", tc.name, got, err)
			}
		}
		// A bad name is refused before the ceremony is used up.
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		opts, _ := e.auth.BeginPasskeyRegistration(ctx, session, rpMain, addrA)
		posted, _ := webauthntest.New(rpMain.ID).Create(opts.Options, rpMain.Origin)
		var fe *app.FieldError
		if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "", opts.Ceremony, posted); !errors.As(err, &fe) {
			t.Fatalf("empty name = %v, want a field error", err)
		}
		if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "Phone", opts.Ceremony, posted); err != nil {
			t.Errorf("finishing again with a good name: %v", err)
		}
	})
}

func TestRenamePasskey(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
	list, _ := e.auth.ListPasskeys(ctx)

	got, err := e.auth.RenamePasskey(ctx, list[0].ID, "  Work laptop ")
	if err != nil || got.Name != "Work laptop" || got.ID != list[0].ID || got.Domain != rpMain.ID {
		t.Fatalf("RenamePasskey = %+v, %v", got, err)
	}
	var fe *app.FieldError
	if _, err := e.auth.RenamePasskey(ctx, list[0].ID, ""); !errors.As(err, &fe) || fe.Field != "name" {
		t.Errorf("empty name = %v, want a field error on name", err)
	}
	if _, err := e.auth.RenamePasskey(ctx, "nope", "x"); !errors.Is(err, ErrPasskeyNotFound) {
		t.Errorf("unknown ID = %v, want ErrPasskeyNotFound", err)
	}
	if list, _ = e.auth.ListPasskeys(ctx); list[0].Name != "Work laptop" {
		t.Errorf("listed name = %q", list[0].Name)
	}
}

func TestPasswordSignInOff(t *testing.T) {
	ctx := context.Background()

	t.Run("turning it off needs a passkey for the console's domain", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		_, err := e.auth.SetPasswordSignIn(ctx, false, rpMain.ID)
		var de *PasskeyDomainError
		if !errors.Is(err, ErrNoPasskey) || !errors.As(err, &de) || de.Domain != rpMain.ID {
			t.Fatalf("with no passkey = %v, want ErrNoPasskey for %s", err, rpMain.ID)
		}
		if !strings.Contains(err.Error(), "add a passkey for console.example.com before turning password sign-in off") {
			t.Errorf("message = %q", err)
		}
		addPasskey(t, e, signedIn(t, e), rpOther, "Old domain")
		if _, err := e.auth.SetPasswordSignIn(ctx, false, rpMain.ID); !errors.Is(err, ErrNoPasskey) {
			t.Errorf("with a passkey for another domain only = %v, want ErrNoPasskey", err)
		}
		// With no console domain known, any passkey will do.
		got, err := e.auth.SetPasswordSignIn(ctx, false, "")
		if err != nil || got.PasswordSignIn || got.Passkeys != 1 {
			t.Errorf("SetPasswordSignIn(off, any) = %+v, %v", got, err)
		}
	})

	t.Run("sign-in is refused without a password check and without counting", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
		if _, err := e.auth.SetPasswordSignIn(ctx, false, rpMain.ID); err != nil {
			t.Fatal(err)
		}
		e.hashes = 0
		for range 8 { // more than the limit allows
			if _, err := e.auth.SignIn(ctx, testEmail, testPassword, rpMain.ID, addrB); !errors.Is(err, ErrPasswordSignInOff) {
				t.Fatalf("SignIn = %v, want ErrPasswordSignInOff", err)
			}
		}
		if e.hashes != 0 {
			t.Errorf("%d password hashes ran; none should", e.hashes)
		}
		// Nothing was counted: the address still has all its tries on a passkey.
		_, err := e.auth.FinishPasskeySignIn(ctx, rpMain, "never-issued", []byte("{}"), addrB)
		refusedWith(t, "first failure", err, 4)
		if ok, _ := e.auth.PasswordSignInAllowed(ctx, rpMain.ID); ok {
			t.Error("PasswordSignInAllowed = true with it off and a passkey here")
		}
	})

	t.Run("the password keeps working where no passkey exists", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		addPasskey(t, e, signedIn(t, e), rpOther, "Old domain")
		if _, err := e.auth.SetPasswordSignIn(ctx, false, rpOther.ID); err != nil {
			t.Fatal(err)
		}
		// The console moved to rpMain, where there is no passkey yet.
		if _, err := e.auth.SignIn(ctx, testEmail, testPassword, rpMain.ID, addrA); err != nil {
			t.Errorf("SignIn at a domain with no passkey = %v, want it to work", err)
		}
		if ok, _ := e.auth.PasswordSignInAllowed(ctx, rpMain.ID); !ok {
			t.Error("PasswordSignInAllowed = false where no passkey exists")
		}
		// And where a passkey does exist it is refused; "" counts any passkey.
		for _, domain := range []string{rpOther.ID, ""} {
			if _, err := e.auth.SignIn(ctx, testEmail, testPassword, domain, addrA); !errors.Is(err, ErrPasswordSignInOff) {
				t.Errorf("SignIn(consoleDomain=%q) = %v, want ErrPasswordSignInOff", domain, err)
			}
		}
	})

	t.Run("confirming with the password is refused too", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
		e.auth.SetPasswordSignIn(ctx, false, rpMain.ID)
		old := unconfirmedSession(t, e, "old-session")
		e.hashes = 0
		if err := e.auth.VerifyPassword(ctx, old, testPassword, rpMain.ID, addrA); !errors.Is(err, ErrPasswordSignInOff) || e.hashes != 0 {
			t.Errorf("VerifyPassword = %v after %d hashes; want ErrPasswordSignInOff and none", err, e.hashes)
		}
	})

	t.Run("the last passkey of a domain can't be removed while it is off", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		addPasskey(t, e, session, rpMain, "MacBook")
		addPasskey(t, e, session, rpOther, "Old domain")
		e.auth.SetPasswordSignIn(ctx, false, rpMain.ID)
		list, _ := e.auth.ListPasskeys(ctx)
		main, other := list[0], list[1]

		// Another domain's passkey doesn't count towards this one.
		err := e.auth.RemovePasskey(ctx, main.ID)
		var de *PasskeyDomainError
		if !errors.Is(err, ErrLastPasskey) || !errors.As(err, &de) || de.Domain != rpMain.ID {
			t.Fatalf("removing the only passkey of its domain = %v, want ErrLastPasskey for %s", err, rpMain.ID)
		}
		if !strings.Contains(err.Error(), "turn password sign-in on first, or add another passkey") {
			t.Errorf("message = %q", err)
		}
		// A second one makes the first removable.
		addPasskey(t, e, session, rpMain, "Phone")
		if err := e.auth.RemovePasskey(ctx, main.ID); err != nil {
			t.Errorf("removing one of two: %v", err)
		}
		// With it on, the last one goes.
		if _, err := e.auth.SetPasswordSignIn(ctx, true, rpMain.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.auth.RemovePasskey(ctx, other.ID); err != nil {
			t.Errorf("removing the last passkey with password sign-in on: %v", err)
		}
		if err := e.auth.RemovePasskey(ctx, "nope"); !errors.Is(err, ErrPasskeyNotFound) {
			t.Errorf("unknown ID = %v, want ErrPasskeyNotFound", err)
		}
	})

	t.Run("the host's removal skips the rule", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		addPasskey(t, e, signedIn(t, e), rpMain, "MacBook")
		e.auth.SetPasswordSignIn(ctx, false, rpMain.ID)
		list, _ := e.auth.ListPasskeys(ctx)
		if err := e.auth.RemovePasskeyUnchecked(ctx, list[0].ID); err != nil {
			t.Fatalf("RemovePasskeyUnchecked: %v", err)
		}
		if err := e.auth.RemovePasskeyUnchecked(ctx, list[0].ID); !errors.Is(err, ErrPasskeyNotFound) {
			t.Errorf("removing it twice = %v, want ErrPasskeyNotFound", err)
		}
		// Nothing for the domain is left, so the password works again by itself.
		if _, err := e.auth.SignIn(ctx, testEmail, testPassword, rpMain.ID, addrA); err != nil {
			t.Errorf("SignIn after the last passkey went: %v", err)
		}
	})

	t.Run("a reset turns it back on and keeps the passkeys", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		session := signedIn(t, e)
		addPasskey(t, e, session, rpMain, "MacBook")
		e.auth.SetPasswordSignIn(ctx, false, rpMain.ID)
		if err := e.auth.ResetPassword(ctx, "a brand new password"); err != nil {
			t.Fatal(err)
		}
		got, _ := e.auth.SignInSettings(ctx)
		if !got.PasswordSignIn || got.Passkeys != 1 {
			t.Errorf("after reset: %+v, want password on and 1 passkey", got)
		}
		if _, err := e.auth.SignIn(ctx, testEmail, "a brand new password", rpMain.ID, addrA); err != nil {
			t.Errorf("SignIn with the new password: %v", err)
		}
		if err := e.auth.CheckSession(ctx, session); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("the old session after a reset = %v, want it ended", err)
		}
		if !strings.Contains(e.logs.String(), `"event":"password_signin_on"`) {
			t.Error("turning it back on left no log line")
		}
	})

	t.Run("setting it to what it is changes nothing", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		got, err := e.auth.SetPasswordSignIn(ctx, true, "")
		if err != nil || !got.PasswordSignIn || got.Passkeys != 0 {
			t.Errorf("SetPasswordSignIn(on) = %+v, %v", got, err)
		}
		if strings.Contains(e.logs.String(), "password_signin_on") {
			t.Error("an unchanged setting was logged as a change")
		}
	})

	t.Run("without an admin", func(t *testing.T) {
		e := newAuthEnv(t)
		if _, err := e.auth.SignInSettings(ctx); !errors.Is(err, ErrNoAdmin) {
			t.Errorf("SignInSettings = %v, want ErrNoAdmin", err)
		}
		if _, err := e.auth.BeginPasskeySignIn(ctx, rpMain, addrA); !errors.Is(err, ErrNoAdmin) {
			t.Errorf("BeginPasskeySignIn = %v, want ErrNoAdmin", err)
		}
		if _, err := e.auth.FinishPasskeySignIn(ctx, rpMain, "x", []byte("{}"), addrA); !errors.Is(err, ErrNoAdmin) {
			t.Errorf("FinishPasskeySignIn = %v, want ErrNoAdmin", err)
		}
	})
}

// TestPasskeyLogsHoldNoSecret runs every path, good and bad, and searches the log for everything
// an authenticator or a browser keeps private or sends once.
func TestPasskeyLogsHoldNoSecret(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	session := signedIn(t, e)

	sentinels := map[string]bool{}
	note := func(a *webauthntest.Authenticator) {
		for _, s := range a.PrivateKeySentinel() {
			sentinels[s] = true
		}
		sentinels[base64.RawURLEncoding.EncodeToString(a.CredentialID())] = true
		sentinels[hex.EncodeToString(a.CredentialID())] = true
	}

	a := webauthntest.New(rpMain.ID)
	note(a)
	opts, err := e.auth.BeginPasskeyRegistration(ctx, session, rpMain, addrA)
	if err != nil {
		t.Fatal(err)
	}
	var reg struct{ Challenge string }
	json.Unmarshal(opts.Options, &reg)
	sentinels[reg.Challenge] = true
	posted, _ := a.Create(opts.Options, rpMain.Origin)
	if _, err := e.auth.FinishPasskeyRegistration(ctx, session, rpMain, "MacBook", opts.Ceremony, posted); err != nil {
		t.Fatal(err)
	}

	// A good sign-in, then each way to fail.
	signOpts, _ := e.auth.BeginPasskeySignIn(ctx, rpMain, addrA)
	var sign struct{ Challenge string }
	json.Unmarshal(signOpts.Options, &sign)
	sentinels[sign.Challenge] = true
	good, _ := a.Get(signOpts.Options, rpMain.Origin)
	var cred struct {
		Response struct{ ClientDataJSON, Signature, AuthenticatorData string }
	}
	json.Unmarshal(good, &cred)
	sentinels[cred.Response.ClientDataJSON] = true
	sentinels[cred.Response.Signature] = true
	if _, err := e.auth.FinishPasskeySignIn(ctx, rpMain, signOpts.Ceremony, good, addrA); err != nil {
		t.Fatal(err)
	}
	e.auth.FinishPasskeySignIn(ctx, rpMain, signOpts.Ceremony, good, addrB)              // replay
	e.auth.FinishPasskeySignIn(ctx, rpOther, "never-issued", good, addrB)                // unknown ceremony
	e.auth.FinishPasskeySignIn(ctx, rpMain, signOpts.Ceremony, []byte("garbage"), addrB) // not JSON
	b := *a
	b.UserVerified = false
	badID, bad := signInOptions(t, e, rpMain, &b, rpMain.Origin)
	e.auth.FinishPasskeySignIn(ctx, rpMain, badID, bad, addrB)
	a.Counter = 1 // backwards
	backID, back := signInOptions(t, e, rpMain, a, rpMain.Origin)
	e.auth.FinishPasskeySignIn(ctx, rpMain, backID, back, addrB)
	// Confirm, with a passkey that works and one that doesn't.
	cOpts, _ := e.auth.BeginPasskeyConfirm(ctx, session, rpMain, addrA)
	a.Counter = 9
	cGood, _ := a.Get(cOpts.Options, rpMain.Origin)
	e.auth.FinishPasskeyConfirm(ctx, session, rpMain, cOpts.Ceremony, cGood, addrA)
	e.auth.FinishPasskeyConfirm(ctx, session, rpMain, cOpts.Ceremony, cGood, addrA) // replay
	e.auth.SetPasswordSignIn(ctx, false, rpMain.ID)
	e.auth.SignIn(ctx, testEmail, testPassword, rpMain.ID, addrB)
	list, _ := e.auth.ListPasskeys(ctx)
	e.auth.RemovePasskey(ctx, list[0].ID) // refused: the last one
	e.auth.RenamePasskey(ctx, list[0].ID, "Work laptop")

	logs := e.logs.String()
	if !strings.Contains(logs, "passkey_signin_failed") || !strings.Contains(logs, "passkey_added") {
		t.Fatalf("the log has no passkey events to check: %s", logs)
	}
	for s, check := range sentinels {
		if check && len(s) >= 16 && strings.Contains(logs, s) {
			t.Errorf("the log holds %s...", s[:12])
		}
	}
	for _, banned := range []string{"clientDataJSON", "attestationObject", "authenticatorData", "challenge", "publicKey", "credentialId", "user handle"} {
		if strings.Contains(logs, banned) {
			t.Errorf("the log mentions %q", banned)
		}
	}
}
