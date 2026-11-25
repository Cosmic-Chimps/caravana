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
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Errors callers can check with errors.Is.
var (
	ErrNoAdmin        = errors.New("no admin yet: run caravana admin init on the host")
	ErrAdminExists    = errors.New("an owner already exists; use caravana admin reset-password")
	ErrNoSuchPerson   = errors.New("no active person with that email")
	ErrBadCredentials = errors.New("that email and password don't match")
	ErrSigninLimited  = errors.New("too many failed sign-ins from this address")
	ErrWrongPassword  = errors.New("the current password is wrong")
	// ErrNotAuthenticated is every API-credential failure: a missing,
	// malformed, wrong, expired or revoked token or session, or a person who was
	// removed, never accepted their invite or has passed their end date.
	// The API maps it to the same 401 as the webhook's ErrUnauthorized, and
	// never says which of these it was.
	ErrNotAuthenticated = errors.New("missing or invalid API token or session")
	ErrTokenNameTaken   = errors.New("an active token already has this name")

	// ErrPasswordSignInOff is SignIn refusing because password sign-in is turned
	// off and a passkey exists for the console's domain (feature 033).
	ErrPasswordSignInOff = errors.New("password sign-in is off: sign in with a passkey")
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

// Auth is the people, their sessions, API tokens, passkeys and the sign-in
// limit. It needs only the store, so the host CLI can use it while Nomad is
// down. It is safe for concurrent use.
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

	ceremonies ceremonies // pending passkey ceremonies, ceremonies.go
	// passkeyMu serializes the changes that check the passkeys before they change
	// them (removing one, turning password sign-in off), so two at once can't
	// each see a passkey the other is about to take.
	passkeyMu sync.Mutex

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
		a.dummy, _ = hashPassword("caravana: no such person", a.iterations)
	})
	return a.dummy
}

// InitAdmin creates the first person, an owner, for `caravana admin init`. Once anyone
// exists it is ErrAdminExists. A bad email or a password under 12 characters is an
// *app.FieldError naming email or password.
func (a *Auth) InitAdmin(ctx context.Context, email, password string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if err := checkPasswordRule("password", password); err != nil {
		return err
	}
	if _, err := a.store.FirstOwner(ctx); err == nil {
		return ErrAdminExists
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("init admin: %w", err)
	}
	hash, err := a.hash(password)
	if err != nil {
		return err
	}
	now := a.now().UTC()
	id := newPersonID()
	err = a.store.CreatePerson(ctx, store.PersonRecord{
		ID: id, Email: email, OrgRole: string(OrgOwner), State: personActive, PasswordHash: hash,
		PasswordChangedAt: &now, PasswordSignIn: true, CreatedAt: now,
	}, nil)
	if errors.Is(err, store.ErrAlreadyExists) {
		return ErrAdminExists
	} else if err != nil {
		return fmt.Errorf("init admin: %w", err)
	}
	a.event("admin_created")
	a.audit(ctx, audit.Entry{Action: audit.ActionCreated, Resource: personResource(id, email),
		Changes: audit.Diff(nil, map[string]any{"orgRole": string(OrgOwner)})})
	return nil
}

// newPersonID returns a person's ID: 26 lowercase base32 characters. It isn't secret.
func newPersonID() string { return strings.ToLower(rand.Text()) }

// Person states.
// Person states.
const (
	personInvited = "invited"
	personActive  = "active"
)

// PersonRef names a person for the host CLI.
type PersonRef struct {
	ID    string
	Email string
}

// FirstOwnerEmail returns the oldest owner's email, or ErrNoAdmin before anyone exists.
func (a *Auth) FirstOwnerEmail(ctx context.Context) (string, error) {
	owner, err := a.store.FirstOwner(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNoAdmin
	} else if err != nil {
		return "", fmt.Errorf("get first owner: %w", err)
	}
	return owner.Email, nil
}

// ResolvePerson finds the active person with that email for the host CLI; "" means the first
// owner. Before anyone exists it is ErrNoAdmin, and an email nobody active has is ErrNoSuchPerson.
func (a *Auth) ResolvePerson(ctx context.Context, email string) (PersonRef, error) {
	if strings.TrimSpace(email) == "" {
		owner, err := a.store.FirstOwner(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return PersonRef{}, ErrNoAdmin
		} else if err != nil {
			return PersonRef{}, fmt.Errorf("resolve person: %w", err)
		}
		return PersonRef{ID: owner.ID, Email: owner.Email}, nil
	}
	p, err := a.store.GetPersonByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, store.ErrNotFound) || err == nil && p.State != personActive {
		if err := a.anyone(ctx); err != nil {
			return PersonRef{}, err
		}
		return PersonRef{}, ErrNoSuchPerson
	} else if err != nil {
		return PersonRef{}, fmt.Errorf("resolve person: %w", err)
	}
	return PersonRef{ID: p.ID, Email: p.Email}, nil
}

// PersonSummary is a line of `caravana admin status`.
type PersonSummary struct {
	ID, Email, OrgRole, State string
	EndsAt                    *time.Time
	PasswordSignIn            bool
	Passkeys                  int
}

// ListPeople returns everyone, the oldest first, for the host CLI.
func (a *Auth) ListPeople(ctx context.Context) ([]PersonSummary, error) {
	recs, err := a.store.ListPeople(ctx)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	out := make([]PersonSummary, 0, len(recs))
	for _, r := range recs {
		n, err := a.store.CountPasskeys(ctx, r.ID, "")
		if err != nil {
			return nil, fmt.Errorf("list people: %w", err)
		}
		out = append(out, PersonSummary{ID: r.ID, Email: r.Email, OrgRole: r.OrgRole, State: r.State, EndsAt: r.EndsAt,
			PasswordSignIn: r.PasswordSignIn, Passkeys: n})
	}
	return out, nil
}

// anyone is ErrNoAdmin until a person exists (an owner always exists once anyone does).
func (a *Auth) anyone(ctx context.Context) error {
	if _, err := a.store.FirstOwner(ctx); errors.Is(err, store.ErrNotFound) {
		return ErrNoAdmin
	} else if err != nil {
		return fmt.Errorf("get first owner: %w", err)
	}
	return nil
}

// ResetPassword sets a new password for the active person with that email ("" is the first
// owner) without asking for the old one (it is for the owner at the host's shell). It ends
// every session of theirs and turns their password sign-in on if it was off; API tokens and
// passkeys are kept.
func (a *Auth) ResetPassword(ctx context.Context, email, password string) error {
	if err := checkPasswordRule("password", password); err != nil {
		return err
	}
	who, err := a.ResolvePerson(ctx, email)
	if err != nil {
		return err
	}
	hash, err := a.hash(password)
	if err != nil {
		return err
	}
	if err := a.store.SetPassword(ctx, who.ID, hash, a.now().UTC(), ""); errors.Is(err, store.ErrNotFound) {
		return ErrNoSuchPerson
	} else if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	a.event("password_reset")
	a.audit(ctx, audit.Entry{Action: audit.ActionUpdated, Resource: accountResource(who.ID, who.Email),
		Changes: []audit.Change{{Path: "password", Op: audit.OpChanged}}})
	// The owner at the host's shell is the way back in when every passkey is
	// lost, so a reset turns password sign-in on again (passkeys are kept).
	if err := a.passwordSignInOn(ctx, who.ID); err != nil {
		return err
	}
	return nil
}

// ChangePassword sets a new password for the person who knows the current
// one. It ends every session of theirs except keepSession, the cookie value of the one
// making the change ("" when the change came by API token: then every session
// ends). API tokens keep working. A wrong current password is
// ErrWrongPassword; a new one under 12 characters is an *app.FieldError
// naming newPassword.
func (a *Auth) ChangePassword(ctx context.Context, personID, current, next, keepSession string) error {
	person, err := a.settingsPerson(ctx, personID)
	if err != nil {
		return err
	}
	if err := checkPasswordRule("newPassword", next); err != nil {
		return err
	}
	if !a.check(current, person.PasswordHash) {
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
	if err := a.store.SetPassword(ctx, personID, hash, a.now().UTC(), keep); err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	a.event("password_changed")
	a.audit(ctx, audit.Entry{Action: audit.ActionUpdated, Resource: accountResource(person.ID, person.Email),
		Actor:   a.actorUnlessKnown(ctx, person.ID, person.Email),
		Changes: []audit.Change{{Path: "password", Op: audit.OpChanged}}})
	return nil
}

// person returns the person with that ID. One that doesn't exist is ErrNotAuthenticated: the
// callers act for the person a credential belongs to, so a gone person is a gone credential.
func (a *Auth) person(ctx context.Context, id string) (store.PersonRecord, error) {
	p, err := a.store.GetPerson(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.PersonRecord{}, ErrNotAuthenticated
	} else if err != nil {
		return store.PersonRecord{}, fmt.Errorf("get person: %w", err)
	}
	return p, nil
}

// settingsPerson is person for the calls that change or read a person's own sign-in settings:
// before anyone exists the answer is ErrNoAdmin, so the host's hint reaches the page, and
// afterwards a person who is gone is ErrNotAuthenticated.
func (a *Auth) settingsPerson(ctx context.Context, id string) (store.PersonRecord, error) {
	p, err := a.person(ctx, id)
	if errors.Is(err, ErrNotAuthenticated) {
		if err := a.anyone(ctx); err != nil {
			return store.PersonRecord{}, err
		}
		return store.PersonRecord{}, ErrNotAuthenticated
	}
	return p, err
}

// livePerson returns the person if they may act now: they exist, accepted their invite and
// haven't passed their end date. Anything else is ErrNotAuthenticated, and a person past their
// end date has their sessions deleted here, so no later check needs the date.
func (a *Auth) livePerson(ctx context.Context, id string) (store.PersonRecord, error) {
	p, err := a.person(ctx, id)
	if err != nil {
		return store.PersonRecord{}, err
	}
	if p.State != personActive {
		return store.PersonRecord{}, ErrNotAuthenticated
	}
	if p.EndsAt != nil && !a.now().UTC().Before(*p.EndsAt) {
		if err := a.store.DeleteSessionsOf(ctx, id); err != nil {
			a.log.Warn("could not delete the sessions of a person whose access ended", "error", err)
		}
		return store.PersonRecord{}, ErrNotAuthenticated
	}
	return p, nil
}

// principalOf loads who a request acts as for a person known to be live: their roles, fresh
// from the store. scope and tokenName come from the API token, nil and "" for a session.
func (a *Auth) principalOf(ctx context.Context, p store.PersonRecord, scope *TokenScope, tokenName string) (Principal, error) {
	out := Principal{PersonID: p.ID, Email: p.Email, OrgRole: OrgRole(p.OrgRole), Scope: scope, TokenName: tokenName}
	if out.OrgRole == OrgMember {
		recs, err := a.store.PersonRoles(ctx, p.ID)
		if err != nil {
			return Principal{}, fmt.Errorf("load roles: %w", err)
		}
		for _, r := range recs {
			pr := ProjectRole{Project: r.Project, Role: Role(r.Role), EndsAt: r.EndsAt}
			if len(r.Environments) > 0 {
				pr.Environments = make(map[string]Role, len(r.Environments))
				for env, role := range r.Environments {
					pr.Environments[env] = Role(role)
				}
			}
			out.Roles = append(out.Roles, pr)
		}
	}
	return out, nil
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
