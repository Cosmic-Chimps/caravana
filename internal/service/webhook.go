package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// ErrUnauthorized is every webhook authorization failure: no token, a wrong
// or revoked one, another app's, an app without a token, or no such app. The
// reason is never told apart, so callers can't probe which apps exist.
var ErrUnauthorized = errors.New("missing or invalid webhook token")

// WebhookToken is a newly created token. Token is shown once: only its hash
// is stored.
type WebhookToken struct {
	Token     string
	Path      string // the webhook path with the token, e.g. /v1/webhooks/whoami?token=…
	CreatedAt time.Time
}

// WebhookResult is what a webhook call did. Changed is false when nothing
// changed and nothing was deployed; Deployment is then the live one.
type WebhookResult struct {
	Changed    bool
	Deployment store.DeploymentRecord
}

// WebhookPath is the webhook path of an app, without the token.
func WebhookPath(appID string) string { return "/v1/webhooks/" + appID }

// CreateWebhookToken creates (or replaces) appID's webhook token and returns
// it; any earlier token stops working at once. An unknown app is
// store.ErrNotFound.
func (s *Service) CreateWebhookToken(ctx context.Context, appID string) (WebhookToken, error) {
	token, err := newWebhookToken()
	if err != nil {
		return WebhookToken{}, err
	}
	rec, err := s.store.SetWebhookToken(ctx, appID, hashToken(token))
	if err != nil {
		return WebhookToken{}, err
	}
	return WebhookToken{Token: token, Path: WebhookPath(appID) + "?token=" + token, CreatedAt: rec.CreatedAt}, nil
}

// WebhookStatus tells whether an app has a webhook token, never the token.
type WebhookStatus struct {
	Enabled   bool
	CreatedAt time.Time // zero when !Enabled
}

// WebhookStatus returns appID's webhook status. An unknown app is
// store.ErrNotFound.
func (s *Service) WebhookStatus(ctx context.Context, appID string) (WebhookStatus, error) {
	if _, err := s.store.GetApp(ctx, appID); err != nil {
		return WebhookStatus{}, err
	}
	rec, err := s.store.GetWebhookToken(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return WebhookStatus{}, nil
	}
	if err != nil {
		return WebhookStatus{}, err
	}
	return WebhookStatus{Enabled: true, CreatedAt: rec.CreatedAt}, nil
}

// DeleteWebhookToken turns appID's webhook off: every call is refused from
// then on. Deleting when there is no token is fine; an unknown app is
// store.ErrNotFound.
func (s *Service) DeleteWebhookToken(ctx context.Context, appID string) error {
	return s.store.DeleteWebhookToken(ctx, appID)
}

// CheckWebhookToken returns nil when token is appID's webhook token, else
// ErrUnauthorized. Every failure does the same work (research R2).
func (s *Service) CheckWebhookToken(ctx context.Context, appID, token string) error {
	stored := ""
	rec, err := s.store.GetWebhookToken(ctx, appID)
	switch {
	case err == nil:
		stored = rec.Hash
	case !errors.Is(err, store.ErrNotFound):
		return err // the store failed: a 500, not a 401
	}
	if !tokenMatches(stored, token) {
		return ErrUnauthorized
	}
	return nil
}

// Webhook checks the token, then runs WebhookRedeploy.
func (s *Service) Webhook(ctx context.Context, appID, token string) (WebhookResult, error) {
	if err := s.CheckWebhookToken(ctx, appID, token); err != nil {
		return WebhookResult{}, err
	}
	return s.WebhookRedeploy(ctx, appID)
}

// WebhookRedeploy redeploys appID for a webhook, unless nothing changed
// (research R6): when every image still resolves to the digests of the app's
// latest healthy deployment, and the stored app hasn't been saved since that
// deployment, it deploys nothing and returns that deployment. Otherwise it is
// Redeploy with trigger webhook, errors included. Call it only after
// CheckWebhookToken.
func (s *Service) WebhookRedeploy(ctx context.Context, appID string) (WebhookResult, error) {
	if live, ok := s.unchanged(ctx, appID); ok {
		return WebhookResult{Changed: false, Deployment: live}, nil
	}
	rec, err := s.Redeploy(ctx, appID, app.TriggerWebhook)
	if err != nil {
		return WebhookResult{}, err
	}
	return WebhookResult{Changed: true, Deployment: rec}, nil
}

// unchanged reports whether a redeploy would run exactly what Nomad already
// runs, and returns that deployment. Any doubt (an error, no healthy
// deployment) means "changed": Redeploy then resolves again and records any
// failure the usual way.
func (s *Service) unchanged(ctx context.Context, appID string) (store.DeploymentRecord, bool) {
	stored, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, false
	}
	live, err := s.store.LatestHealthyDeployment(ctx, appID)
	if err != nil || stored.UpdatedAt.After(live.CreatedAt) {
		return store.DeploymentRecord{}, false
	}
	digests, _, err := s.deployer.Resolve(ctx, stored.App)
	if err != nil || !maps.Equal(digests, live.Deployment.Digests) {
		return store.DeploymentRecord{}, false
	}
	return live, true
}

// webhookTokenPrefix marks Caravana webhook tokens, so secret scanners (and
// people) recognize a leaked one.
const webhookTokenPrefix = "cvwh_"

// webhookTokenBytes is the token's randomness: 256 bits, far beyond guessing,
// which is why the webhook needs no rate limit.
const webhookTokenBytes = 32

// newWebhookToken returns a fresh token: the prefix, then 32 random bytes in
// URL-safe base64 without padding (48 characters in all), so it can go in a
// query string as it is.
func newWebhookToken() (string, error) {
	b := make([]byte, webhookTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook token: %w", err)
	}
	return webhookTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is what the store keeps: the hex SHA-256 of the token. The token
// is long and random, so a fast hash is as safe as a slow password hash.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// dummyHash stands in for a missing stored hash, so checking a token for an
// app without one does the same work as checking a wrong token.
var dummyHash = hashToken("caravana: no webhook token")

// tokenMatches reports whether presented is the token whose hash is stored.
// storedHash may be "" (no token), and then the dummy is compared instead,
// and the answer is false. The comparison of the two 32-byte hashes takes
// the same time wherever they differ (like CryptographicOperations.
// FixedTimeEquals in .NET).
func tokenMatches(storedHash, presented string) bool {
	want, err := hex.DecodeString(storedHash)
	known := err == nil && len(want) == sha256.Size
	if !known {
		want, _ = hex.DecodeString(dummyHash)
	}
	got := sha256.Sum256([]byte(presented))
	equal := subtle.ConstantTimeCompare(want, got[:]) == 1
	return known && equal && presented != ""
}
