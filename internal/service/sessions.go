package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/audit"
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
// address. consoleDomain is the domain the console is served at; when the person's
// password sign-in is off and they have a passkey for that domain ("" means any
// passkey), SignIn answers ErrPasswordSignInOff without checking the password and
// without counting a failure.
//
// Only an active person who hasn't passed their end date signs in. An unknown email, an
// invited one and an ended one get the same error as a wrong password, and cost the
// same: one password check each, against a stand-in hash when there is no password to
// check. A failure is a *SigninFailure saying how many tries are left; from a locked
// address the password isn't checked at all and the error is a *SigninLimitError.
// Before anyone exists it is ErrNoAdmin.
func (a *Auth) SignIn(ctx context.Context, email, password, consoleDomain, address string) (Session, error) {
	// The email is compared as typed, trimmed and lower-cased. A malformed one
	// simply doesn't match: it is wrong credentials, not a field error.
	person, err := a.store.GetPersonByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	known := err == nil
	switch {
	case errors.Is(err, store.ErrNotFound):
		if err := a.anyone(ctx); err != nil {
			return Session{}, err
		}
	case err != nil:
		return Session{}, fmt.Errorf("sign in: %w", err)
	}
	// An invited person has no password, and one past their end date can't sign in.
	live := known && person.State == personActive && (person.EndsAt == nil || a.now().UTC().Before(*person.EndsAt))

	// Password sign-in turned off: nothing to guess, so nothing is counted.
	if live && !person.PasswordSignIn {
		if n, err := a.store.CountPasskeys(ctx, person.ID, consoleDomain); err != nil {
			return Session{}, fmt.Errorf("sign in: %w", err)
		} else if n > 0 {
			return Session{}, ErrPasswordSignInOff
		}
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

	hash := a.dummyHash()
	if live {
		hash = person.PasswordHash
	}
	if !a.check(password, hash) || !live {
		a.event("signin_failed", "address", address)
		a.noteSignInFailed(ctx, email, address)
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
	s, err := a.startSession(ctx, person.ID)
	if err != nil {
		return Session{}, err
	}
	a.event("signin", "address", address)
	a.noteSignedIn(ctx, person, address, "password")
	return s, nil
}

// startSession makes a signed-in session for a person, confirmed now (signing in is
// proof of who is at the keyboard), and clears out expired ones.
func (a *Auth) startSession(ctx context.Context, personID string) (Session, error) {
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
		IDHash: hashToken(value), PersonID: personID, CreatedAt: now, LastSeenAt: now, ExpiresAt: s.ExpiresAt, VerifiedAt: &now,
	}); err != nil {
		return Session{}, fmt.Errorf("start session: %w", err)
	}
	return s, nil
}

// StartSessionFor makes a signed-in session for a person who has just proved who they are in another
// way: the console calls it, once, for the person Service.AcceptInvite returned (the invite link
// is the proof, and accepting it set their password). Nothing else may call it.
func (a *Auth) StartSessionFor(ctx context.Context, personID string) (Session, error) {
	s, err := a.startSession(ctx, personID)
	if err != nil {
		return Session{}, err
	}
	a.event("signin", "person", personID, "how", "invite")
	return s, nil
}

// SignOut ends the session with that cookie value. A value that is empty or
// already gone is fine.
func (a *Auth) SignOut(ctx context.Context, sessionValue string) error {
	if sessionValue == "" {
		return nil
	}
	rec, recErr := a.store.GetSession(ctx, hashToken(sessionValue))
	if err := a.store.DeleteSession(ctx, hashToken(sessionValue)); err != nil {
		return fmt.Errorf("sign out: %w", err)
	}
	a.event("signout")
	if recErr != nil {
		a.noChange(ctx) // no such session: nothing ended
		return nil
	}
	if person, err := a.store.GetPerson(ctx, rec.PersonID); err == nil {
		a.audit(ctx, audit.Entry{Action: audit.ActionSignedOut, Resource: accountResource(person.ID, person.Email),
			Actor: audit.Actor{Kind: audit.ActorPerson, Email: person.Email, PersonID: person.ID}})
	}
	return nil
}

// CheckSession returns who the session is when sessionValue is a live session: not past its
// absolute expiry, not idle for 12 hours, and its person still active and not past their end
// date. Anything else is ErrNotAuthenticated. The Principal is loaded fresh, so a changed role
// applies at once. A use is recorded at most once a minute.
func (a *Auth) CheckSession(ctx context.Context, sessionValue string) (Principal, error) {
	id, rec, person, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return Principal{}, err
	}
	now := a.now().UTC()
	if now.Sub(rec.LastSeenAt) >= touchEvery {
		if err := a.store.TouchSession(ctx, id, now); err != nil {
			a.log.Warn("could not record session use", "error", err)
		}
	}
	return a.principalOf(ctx, person, nil, "")
}

// CheckSessionQuiet is CheckSession without renewing the session's idle time.
// Live streams use it: an open page that nobody touches must not keep a
// session alive forever (research R11).
func (a *Auth) CheckSessionQuiet(ctx context.Context, sessionValue string) (Principal, error) {
	_, _, person, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return Principal{}, err
	}
	return a.principalOf(ctx, person, nil, "")
}

// liveSession finds the session, checks both expiry rules and that its person may still act.
// It records nothing. The returned id is the stored key (the SHA-256 of the value).
func (a *Auth) liveSession(ctx context.Context, sessionValue string) (string, store.SessionRecord, store.PersonRecord, error) {
	if sessionValue == "" {
		return "", store.SessionRecord{}, store.PersonRecord{}, ErrNotAuthenticated
	}
	id := hashToken(sessionValue)
	rec, err := a.store.GetSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", store.SessionRecord{}, store.PersonRecord{}, ErrNotAuthenticated
	} else if err != nil {
		return "", store.SessionRecord{}, store.PersonRecord{}, fmt.Errorf("check session: %w", err) // the store failed: a 500, not a 401
	}
	now := a.now().UTC()
	if !now.Before(rec.ExpiresAt) || !now.Before(rec.LastSeenAt.Add(sessionIdle)) {
		return "", store.SessionRecord{}, store.PersonRecord{}, ErrNotAuthenticated
	}
	person, err := a.livePerson(ctx, rec.PersonID)
	if err != nil {
		return "", store.SessionRecord{}, store.PersonRecord{}, err
	}
	return id, rec, person, nil
}
