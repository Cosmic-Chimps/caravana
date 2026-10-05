package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	sessionBytes    = 32                 // 256 bits of randomness
	sessionIdle     = 12 * time.Hour     // a session unused this long ends
	sessionAbsolute = 7 * 24 * time.Hour // and no session outlives this
)

// Session is a new signed-in browser. Value is what goes in the cookie; only
// its SHA-256 is stored, so a copy of the store can't be used to sign in.
type Session struct {
	Value     string    // the cookie value; never logged
	ExpiresAt time.Time // absolute expiry, for the cookie
}

// newSessionValue returns 32 random bytes as URL-safe base64 (43 characters).
func newSessionValue() (string, error) {
	b := make([]byte, sessionBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SignIn checks the email and password and starts a session. address is the
// client address (httpx.Proxies.ClientAddress): the sign-in limit counts per
// address.
//
// A wrong email and a wrong password get the same error, and cost the same:
// one password check each, the unknown email against a stand-in hash. A
// failure is a *SigninFailure saying how many tries are left; from a locked
// address the password isn't checked at all and the error is a
// *SigninLimitError. Without an admin it is ErrNoAdmin.
func (a *Auth) SignIn(ctx context.Context, email, password, address string) (Session, error) {
	admin, err := a.store.GetAdmin(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return Session{}, ErrNoAdmin
	} else if err != nil {
		return Session{}, fmt.Errorf("sign in: %w", err)
	}

	// A locked address is refused before any password work: the slow hash must
	// not be a way to burn this server's CPU.
	at, err := a.beginAttempt(ctx, address)
	if err != nil {
		var limited *SigninLimitError
		if errors.As(err, &limited) {
			a.event("signin_limited", "address", address)
		}
		return Session{}, err
	}
	defer at.release() // a no-op once the result is recorded

	// The email is compared as typed, trimmed and lower-cased. A malformed one
	// simply doesn't match: it is wrong credentials, not a field error.
	hash := a.dummyHash()
	emailMatches := strings.ToLower(strings.TrimSpace(email)) == admin.Email
	if emailMatches {
		hash = admin.PasswordHash
	}
	if !a.check(password, hash) || !emailMatches {
		a.event("signin_failed", "address", address)
		triesLeft, err := a.recordFailure(ctx, at)
		var limited *SigninLimitError
		switch {
		case errors.As(err, &limited):
			a.event("signin_limited", "address", address)
			return Session{}, err
		case err != nil:
			return Session{}, err
		}
		return Session{}, &SigninFailure{TriesLeft: triesLeft}
	}

	if err := a.clearFailures(ctx, at); err != nil {
		return Session{}, err
	}
	value, err := newSessionValue()
	if err != nil {
		return Session{}, err
	}
	now := a.now().UTC()
	// Expired rows have no other use: clear them out while someone signs in.
	if err := a.store.DeleteExpiredSessions(ctx, now, sessionIdle); err != nil {
		a.log.Warn("could not delete expired sessions", "error", err)
	}
	s := Session{Value: value, ExpiresAt: now.Add(sessionAbsolute)}
	if err := a.store.CreateSession(ctx, store.SessionRecord{
		IDHash: hashToken(value), CreatedAt: now, LastSeenAt: now, ExpiresAt: s.ExpiresAt,
	}); err != nil {
		return Session{}, fmt.Errorf("sign in: %w", err)
	}
	a.event("signin", "address", address)
	return s, nil
}

// SignOut ends the session with that cookie value. A value that is empty or
// already gone is fine.
func (a *Auth) SignOut(ctx context.Context, sessionValue string) error {
	if sessionValue == "" {
		return nil
	}
	if err := a.store.DeleteSession(ctx, hashToken(sessionValue)); err != nil {
		return fmt.Errorf("sign out: %w", err)
	}
	a.event("signout")
	return nil
}

// CheckSession returns nil when sessionValue is a live session: not past its
// absolute expiry and not idle for 12 hours. Anything else is
// ErrNotAuthenticated. A use is recorded at most once a minute.
func (a *Auth) CheckSession(ctx context.Context, sessionValue string) error {
	id, rec, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return err
	}
	now := a.now().UTC()
	if now.Sub(rec.LastSeenAt) >= touchEvery {
		if err := a.store.TouchSession(ctx, id, now); err != nil {
			a.log.Warn("could not record session use", "error", err)
		}
	}
	return nil
}

// CheckSessionQuiet is CheckSession without renewing the session's idle time.
// Live streams use it: an open page that nobody touches must not keep a
// session alive forever (research R11).
func (a *Auth) CheckSessionQuiet(ctx context.Context, sessionValue string) error {
	_, _, err := a.liveSession(ctx, sessionValue)
	return err
}

// liveSession finds the session and checks both expiry rules. It records
// nothing. The returned id is the stored key (the SHA-256 of the value).
func (a *Auth) liveSession(ctx context.Context, sessionValue string) (string, store.SessionRecord, error) {
	if sessionValue == "" {
		return "", store.SessionRecord{}, ErrNotAuthenticated
	}
	id := hashToken(sessionValue)
	rec, err := a.store.GetSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", store.SessionRecord{}, ErrNotAuthenticated
	} else if err != nil {
		return "", store.SessionRecord{}, fmt.Errorf("check session: %w", err) // the store failed: a 500, not a 401
	}
	now := a.now().UTC()
	if !now.Before(rec.ExpiresAt) || !now.Before(rec.LastSeenAt.Add(sessionIdle)) {
		return "", store.SessionRecord{}, ErrNotAuthenticated
	}
	return id, rec, nil
}
