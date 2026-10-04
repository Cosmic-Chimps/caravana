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
		State: state,
	}
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

	token, err := newAPIToken()
	if err != nil {
		return CreatedAPIToken{}, err
	}
	rec := store.APITokenRecord{ID: newTokenID(), Name: name, Hash: hashToken(token), CreatedAt: now}
	if expiresAt != nil {
		exp := expiresAt.UTC()
		rec.ExpiresAt = &exp
	}
	if err := a.store.CreateAPIToken(ctx, rec, now); errors.Is(err, store.ErrAlreadyExists) {
		return CreatedAPIToken{}, fmt.Errorf("%w: %q", ErrTokenNameTaken, name)
	} else if err != nil {
		return CreatedAPIToken{}, fmt.Errorf("create api token %q: %w", name, err)
	}
	a.event("token_created", "tokenName", name)
	return CreatedAPIToken{Token: token, Info: tokenInfo(rec, now)}, nil
}

// CheckAPIToken returns nil when token is an active API token and an admin
// exists, else ErrNotAuthenticated, whatever the reason. A value without the
// cvat_ prefix (a webhook token, a typo) is refused before any store lookup.
// It records the token's use at most once a minute.
func (a *Auth) CheckAPIToken(ctx context.Context, token string) error {
	if !strings.HasPrefix(token, apiTokenPrefix) {
		return ErrNotAuthenticated
	}
	rec, err := a.store.GetAPITokenByHash(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotAuthenticated
	} else if err != nil {
		return fmt.Errorf("check api token: %w", err) // the store failed: a 500, not a 401
	}
	now := a.now().UTC()
	if tokenInfo(rec, now).State != TokenActive {
		return ErrNotAuthenticated
	}
	if _, err := a.store.GetAdmin(ctx); errors.Is(err, store.ErrNotFound) {
		return ErrNotAuthenticated
	} else if err != nil {
		return fmt.Errorf("check api token: %w", err)
	}
	if rec.LastUsedAt == nil || now.Sub(*rec.LastUsedAt) >= touchEvery {
		// A failed touch must not fail the request that was authorized.
		if err := a.store.TouchAPIToken(ctx, rec.ID, now); err != nil {
			a.log.Warn("could not record api token use", "tokenName", rec.Name, "error", err)
		}
	}
	return nil
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
