package service

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Errors callers can check with errors.Is.
var (
	ErrNoAdmin        = errors.New("no admin yet: run caravana admin init on the host")
	ErrAdminExists    = errors.New("an admin already exists; use caravana admin reset-password")
	ErrBadCredentials = errors.New("that email and password don't match")
	ErrSigninLimited  = errors.New("too many failed sign-ins from this address")
	ErrWrongPassword  = errors.New("the current password is wrong")
	// ErrNotAuthenticated is every API-credential failure: a missing,
	// malformed, wrong, expired or revoked token or session, or no admin yet.
	// The API maps it to the same 401 as the webhook's ErrUnauthorized, and
	// never says which of these it was.
	ErrNotAuthenticated = errors.New("missing or invalid API token or session")
	ErrTokenNameTaken   = errors.New("an active token already has this name")
)

// SigninLimitError says how long to wait. It wraps ErrSigninLimited.
type SigninLimitError struct{ RetryAfter time.Duration }

func (e *SigninLimitError) Error() string { return ErrSigninLimited.Error() }
func (e *SigninLimitError) Unwrap() error { return ErrSigninLimited }

// SigninFailure says how many tries are left after a wrong email or
// password. It wraps ErrBadCredentials.
type SigninFailure struct{ TriesLeft int }

func (e *SigninFailure) Error() string { return ErrBadCredentials.Error() }
func (e *SigninFailure) Unwrap() error { return ErrBadCredentials }

// Defaults and limits.
const (
	// defaultPasswordIterations is OWASP's current figure for PBKDF2-SHA256.
	defaultPasswordIterations = 600_000
	saltBytes                 = 16
	keyBytes                  = 32
	minPasswordLength         = 12
	// maxStoredIterations stops a corrupted hash from turning one check into
	// hours of work.
	maxStoredIterations = 100_000_000
	hashScheme          = "pbkdf2-sha256"
)

// AuthOptions tune Auth. The zero value is production.
type AuthOptions struct {
	Now                func() time.Time // nil: time.Now
	PasswordIterations int              // 0: 600,000 (tests use a small count)
}

// Auth is the one admin, its sessions, API tokens and the sign-in limit. It
// needs only the store, so the host CLI can use it while Nomad is down. It
// is safe for concurrent use.
type Auth struct {
	store      *store.Store
	log        *slog.Logger
	now        func() time.Time
	iterations int

	// dummyOnce guards dummy, a password hash made with this Auth's
	// iteration count, which a sign-in with an unknown email checks against
	// so it costs what a known email costs.
	dummyOnce sync.Once
	dummy     string

	limit limiter // the sign-in limit's bookkeeping, signinlimit.go

	// onHash, when set (tests only), is called every time a password is
	// hashed or checked, so tests can count the slow work.
	onHash func()
}

// NewAuth returns an Auth on the store.
func NewAuth(st *store.Store, log *slog.Logger, opts AuthOptions) *Auth {
	a := &Auth{store: st, log: log, now: opts.Now, iterations: opts.PasswordIterations}
	if a.now == nil {
		a.now = time.Now
	}
	if a.iterations <= 0 {
		a.iterations = defaultPasswordIterations
	}
	return a
}

// event logs a security event: what happened, never a secret (research R15).
func (a *Auth) event(name string, attrs ...any) {
	a.log.Info("auth", append([]any{"event", name}, attrs...)...)
}

// hash and check are hashPassword and checkPassword plus the test hook.
func (a *Auth) hash(password string) (string, error) {
	if a.onHash != nil {
		a.onHash()
	}
	return hashPassword(password, a.iterations)
}

func (a *Auth) check(password, stored string) bool {
	if a.onHash != nil {
		a.onHash()
	}
	return checkPassword(password, stored)
}

// dummyHash returns the stand-in hash for unknown emails.
func (a *Auth) dummyHash() string {
	a.dummyOnce.Do(func() {
		a.dummy, _ = hashPassword("caravana: no such admin", a.iterations)
	})
	return a.dummy
}

// InitAdmin creates the one admin. A second call is ErrAdminExists. A bad
// email or a password under 12 characters is an *app.FieldError naming email
// or password.
func (a *Auth) InitAdmin(ctx context.Context, email, password string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if err := checkPasswordRule("password", password); err != nil {
		return err
	}
	hash, err := a.hash(password)
	if err != nil {
		return err
	}
	now := a.now().UTC()
	err = a.store.CreateAdmin(ctx, store.AdminRecord{
		Email: email, PasswordHash: hash, CreatedAt: now, PasswordChangedAt: now,
	})
	if errors.Is(err, store.ErrAlreadyExists) {
		return ErrAdminExists
	} else if err != nil {
		return fmt.Errorf("init admin: %w", err)
	}
	a.event("admin_created")
	return nil
}

// AdminEmail returns the admin's email, or ErrNoAdmin before one exists.
func (a *Auth) AdminEmail(ctx context.Context) (string, error) {
	admin, err := a.store.GetAdmin(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNoAdmin
	} else if err != nil {
		return "", fmt.Errorf("get admin: %w", err)
	}
	return admin.Email, nil
}

// ResetPassword sets a new password without asking for the old one (it is
// for the owner at the host's shell). It ends every session; API tokens keep
// working.
func (a *Auth) ResetPassword(ctx context.Context, password string) error {
	if err := checkPasswordRule("password", password); err != nil {
		return err
	}
	hash, err := a.hash(password)
	if err != nil {
		return err
	}
	if err := a.store.SetPassword(ctx, hash, a.now().UTC(), ""); errors.Is(err, store.ErrNotFound) {
		return ErrNoAdmin
	} else if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	a.event("password_reset")
	return nil
}

// ChangePassword sets a new password for the admin who knows the current
// one. It ends every session except keepSession, the cookie value of the one
// making the change ("" when the change came by API token: then every session
// ends). API tokens keep working. A wrong current password is
// ErrWrongPassword; a new one under 12 characters is an *app.FieldError
// naming newPassword.
func (a *Auth) ChangePassword(ctx context.Context, current, next, keepSession string) error {
	admin, err := a.store.GetAdmin(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNoAdmin
	} else if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	if err := checkPasswordRule("newPassword", next); err != nil {
		return err
	}
	if !a.check(current, admin.PasswordHash) {
		return ErrWrongPassword
	}
	hash, err := a.hash(next)
	if err != nil {
		return err
	}
	keep := "" // an empty keep ends every session
	if keepSession != "" {
		keep = hashToken(keepSession)
	}
	if err := a.store.SetPassword(ctx, hash, a.now().UTC(), keep); err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	a.event("password_changed")
	return nil
}

// hashPassword derives a key from the password with PBKDF2-HMAC-SHA256 and a
// random salt, and returns "pbkdf2-sha256$<iterations>$<salt>$<key>" with the
// salt and key in base64 without padding. (C#: Rfc2898DeriveBytes.Pbkdf2.)
func hashPassword(password string, iterations int) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyBytes)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	enc := base64.RawStdEncoding
	return strings.Join([]string{hashScheme, strconv.Itoa(iterations), enc.EncodeToString(salt), enc.EncodeToString(key)}, "$"), nil
}

// checkPassword reports whether password is the one stored as the hash. The
// iteration count comes from the stored string, so it can be raised later
// without breaking old hashes. A malformed hash never matches.
func checkPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > maxStoredIterations {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	// Compares in the same time wherever the keys differ (C#:
	// CryptographicOperations.FixedTimeEquals).
	return subtle.ConstantTimeCompare(got, want) == 1
}

// checkPasswordRule enforces the only password rule: at least 12 characters.
// field names the request field for the error ("password", "newPassword").
func checkPasswordRule(field, password string) error {
	if utf8.RuneCountInString(password) < minPasswordLength {
		return app.NewFieldError(field, "must be at least %d characters", minPasswordLength)
	}
	return nil
}

// normalizeEmail trims and lower-cases an email, and checks it has exactly
// one "@" with text on both sides. Caravana never sends mail to it.
func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", app.NewFieldError("email", "must look like name@example.com")
	}
	return email, nil
}
