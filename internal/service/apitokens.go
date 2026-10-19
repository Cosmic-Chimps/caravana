package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	// apiTokenPrefix marks Caravana API tokens, so secret scanners (and
	// people) recognize a leaked one. Webhook tokens use cvwh_.
	apiTokenPrefix = "cvat_"
	apiTokenBytes  = 32 // 256 bits of randomness
	maxTokenName   = 64
	// touchEvery is how often a token's last-used time is written at most.
	touchEvery = time.Minute
)

// CreatedAPIToken is a new token. Token is shown once: only its hash is
// stored.
type CreatedAPIToken struct {
	Token string
	Info  APITokenInfo
}

// APITokenInfo is what a token listing shows. It never holds the token.
type APITokenInfo struct {
	ID, Name   string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	State      string // active | expired | revoked
	// Scope is set for a deploy token (feature 020); nil for a full token.
	Scope *TokenScope
}

// TokenScope is what a deploy token is limited to: the app named App in any
// environment of project Project. The app need not exist yet.
type TokenScope struct {
	Project string
	App     string
}

// Describe is what a deploy token may do, in words, for the refusal of
// anything else: "this token can only deploy app web of project shop".
func (sc TokenScope) Describe() string {
	return fmt.Sprintf("this token can only deploy app %s of project %s", sc.App, sc.Project)
}

// Token states.
const (
	TokenActive  = "active"
	TokenExpired = "expired"
	TokenRevoked = "revoked"
)

// tokenInfo converts a stored token, deriving its state at now.
func tokenInfo(r store.APITokenRecord, now time.Time) APITokenInfo {
	state := TokenActive
	switch {
	case r.RevokedAt != nil:
		state = TokenRevoked
	case r.ExpiresAt != nil && !now.Before(*r.ExpiresAt):
		state = TokenExpired
	}
	return APITokenInfo{
		ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt,
		LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt,
		State: state, Scope: scopeOf(r),
	}
}

// scopeOf is the scope a stored token carries, or nil for a full token.
func scopeOf(r store.APITokenRecord) *TokenScope {
	if r.ScopeProject == "" && r.ScopeApp == "" {
		return nil
	}
	return &TokenScope{Project: r.ScopeProject, App: r.ScopeApp}
}

// newAPIToken returns a fresh token: the prefix, then 32 random bytes in
// URL-safe base64 without padding (48 characters in all).
func newAPIToken() (string, error) {
	b := make([]byte, apiTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api token: %w", err)
	}
	return apiTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// newTokenID returns a token's ID: 26 lowercase base32 characters. It names
// the token in URLs and isn't secret.
func newTokenID() string { return strings.ToLower(rand.Text()) }

// checkTokenName enforces the name rule: 1 to 64 characters from letters,
// digits, ".", "_" and "-", starting with a letter or a digit.
func checkTokenName(name string) error {
	if name == "" || len(name) > maxTokenName {
		return app.NewFieldError("name", "must be 1 to %d characters", maxTokenName)
	}
	for i, c := range name {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
			return app.NewFieldError("name", "may only use letters, digits, '.', '_' and '-', and must start with a letter or digit")
		}
	}
	return nil
}

// CreateAPIToken creates a token named name that acts as the admin, expiring
// at expiresAt (nil: never). Without an admin it is ErrNoAdmin; a bad name or
// a past expiry is an *app.FieldError; a name an active token already has is
// ErrTokenNameTaken.
func (a *Auth) CreateAPIToken(ctx context.Context, name string, expiresAt *time.Time) (CreatedAPIToken, error) {
	return a.CreateScopedAPIToken(ctx, name, expiresAt, nil)
}

// CreateScopedAPIToken is CreateAPIToken for a token that may be limited to one
// app (a deploy token, feature 020); a nil scope makes a full token. The
// project must exist and the app must be a valid app name, but the app itself
// need not exist yet: the token is made before the app's first deploy. A bad
// scope is an *app.FieldError naming scope.project or scope.app.
func (a *Auth) CreateScopedAPIToken(ctx context.Context, name string, expiresAt *time.Time, scope *TokenScope) (CreatedAPIToken, error) {
	if _, err := a.store.GetAdmin(ctx); errors.Is(err, store.ErrNotFound) {
		return CreatedAPIToken{}, ErrNoAdmin
	} else if err != nil {
		return CreatedAPIToken{}, fmt.Errorf("create api token %q: %w", name, err)
	}
	if err := checkTokenName(name); err != nil {
		return CreatedAPIToken{}, err
	}
	now := a.now().UTC()
	if expiresAt != nil && !expiresAt.After(now) {
		return CreatedAPIToken{}, app.NewFieldError("expiresAt", "must be in the future")
	}
	if scope != nil {
		if err := a.checkScope(ctx, *scope); err != nil {
			return CreatedAPIToken{}, err
		}
	}

	token, err := newAPIToken()
	if err != nil {
		return CreatedAPIToken{}, err
	}
	rec := store.APITokenRecord{ID: newTokenID(), Name: name, Hash: hashToken(token), CreatedAt: now}
	if scope != nil {
		rec.ScopeProject, rec.ScopeApp = scope.Project, scope.App
	}
	if expiresAt != nil {
		exp := expiresAt.UTC()
		rec.ExpiresAt = &exp
	}
	if err := a.store.CreateAPIToken(ctx, rec, now); errors.Is(err, store.ErrAlreadyExists) {
		return CreatedAPIToken{}, fmt.Errorf("%w: %q", ErrTokenNameTaken, name)
	} else if err != nil {
		return CreatedAPIToken{}, fmt.Errorf("create api token %q: %w", name, err)
	}
	if scope != nil {
		a.event("token_created", "tokenName", name, "scopeProject", scope.Project, "scopeApp", scope.App)
	} else {
		a.event("token_created", "tokenName", name)
	}
	return CreatedAPIToken{Token: token, Info: tokenInfo(rec, now)}, nil
}

// checkScope validates a deploy token's scope: the project is a valid name that
// exists, and the app is a valid app name.
func (a *Auth) checkScope(ctx context.Context, sc TokenScope) error {
	if err := app.ValidName("scope.project", sc.Project); err != nil {
		return err
	}
	if err := app.ValidName("scope.app", sc.App); err != nil {
		return err
	}
	if _, err := a.store.GetProject(ctx, sc.Project); errors.Is(err, store.ErrNotFound) {
		return app.NewFieldError("scope.project", "project %q doesn't exist", sc.Project)
	} else if err != nil {
		return fmt.Errorf("check token scope: %w", err)
	}
	return nil
}

// CheckAPIToken returns the token's scope (nil for a full token) when token is
// an active API token and an admin exists, else ErrNotAuthenticated, whatever
// the reason. A value without the cvat_ prefix (a webhook token, a typo) is
// refused before any store lookup. It records the token's use at most once a
// minute. Holding a deploy token to its scope is the caller's job (the API's
// scope middleware); a session has no scope and never reaches this.
func (a *Auth) CheckAPIToken(ctx context.Context, token string) (*TokenScope, error) {
	if !strings.HasPrefix(token, apiTokenPrefix) {
		return nil, ErrNotAuthenticated
	}
	rec, err := a.store.GetAPITokenByHash(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotAuthenticated
	} else if err != nil {
		return nil, fmt.Errorf("check api token: %w", err) // the store failed: a 500, not a 401
	}
	now := a.now().UTC()
	if tokenInfo(rec, now).State != TokenActive {
		return nil, ErrNotAuthenticated
	}
	if _, err := a.store.GetAdmin(ctx); errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotAuthenticated
	} else if err != nil {
		return nil, fmt.Errorf("check api token: %w", err)
	}
	if rec.LastUsedAt == nil || now.Sub(*rec.LastUsedAt) >= touchEvery {
		// A failed touch must not fail the request that was authorized.
		if err := a.store.TouchAPIToken(ctx, rec.ID, now); err != nil {
			a.log.Warn("could not record api token use", "tokenName", rec.Name, "error", err)
		}
	}
	return scopeOf(rec), nil
}

// ListAPITokens returns every token, newest first, with its state as of now.
// It never returns a token's value: only its hash is stored.
func (a *Auth) ListAPITokens(ctx context.Context) ([]APITokenInfo, error) {
	recs, err := a.store.ListAPITokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	now := a.now().UTC()
	out := make([]APITokenInfo, 0, len(recs))
	for _, r := range recs {
		out = append(out, tokenInfo(r, now))
	}
	return out, nil
}

// RevokeAPIToken revokes the token with that ID: it is refused from now on
// but stays in the list. An unknown ID is store.ErrNotFound; revoking twice
// changes nothing.
func (a *Auth) RevokeAPIToken(ctx context.Context, id string) error {
	name, err := a.store.RevokeAPIToken(ctx, id, a.now().UTC())
	if err != nil {
		return err // already says which token; an unknown one is store.ErrNotFound
	}
	a.event("token_revoked", "tokenName", name)
	return nil
}

// RevokeAPITokenByName revokes the active token with that name (for the host
// CLI). With none, it is store.ErrNotFound.
func (a *Auth) RevokeAPITokenByName(ctx context.Context, name string) error {
	rec, err := a.store.ActiveAPITokenByName(ctx, name, a.now().UTC())
	if err != nil {
		return err
	}
	return a.RevokeAPIToken(ctx, rec.ID)
}
