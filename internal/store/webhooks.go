package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// WebhookTokenRecord is an app's webhook token, as stored: a hash only. The
// token itself exists only in the response that created it.
type WebhookTokenRecord struct {
	AppID     string
	Hash      string // hex SHA-256 of the token
	CreatedAt time.Time
}

// SetWebhookToken stores hash as appID's webhook token, replacing any earlier
// one in the same statement, so the old token stops working at once. An
// unknown app is ErrNotFound.
func (s *Store) SetWebhookToken(ctx context.Context, appID, hash string) (WebhookTokenRecord, error) {
	if ok, err := exists(ctx, s.db, "SELECT 1 FROM apps WHERE id = ?", appID); err != nil {
		return WebhookTokenRecord{}, fmt.Errorf("set webhook token of %q: %w", appID, err)
	} else if !ok {
		return WebhookTokenRecord{}, fmt.Errorf("set webhook token: app %q: %w", appID, ErrNotFound)
	}
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_tokens (app_id, token_hash, created_at) VALUES (?, ?, ?)
		ON CONFLICT (app_id) DO UPDATE SET
			token_hash = excluded.token_hash,
			created_at = excluded.created_at`,
		appID, hash, formatTime(now)); err != nil {
		return WebhookTokenRecord{}, fmt.Errorf("set webhook token of %q: %w", appID, err)
	}
	return s.GetWebhookToken(ctx, appID)
}

// GetWebhookToken returns appID's webhook token record, or ErrNotFound when
// it has none (or the app doesn't exist).
func (s *Store) GetWebhookToken(ctx context.Context, appID string) (WebhookTokenRecord, error) {
	rec := WebhookTokenRecord{AppID: appID}
	var created string
	err := s.db.QueryRowContext(ctx,
		"SELECT token_hash, created_at FROM webhook_tokens WHERE app_id = ?", appID,
	).Scan(&rec.Hash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookTokenRecord{}, fmt.Errorf("webhook token of %q: %w", appID, ErrNotFound)
	}
	if err != nil {
		return WebhookTokenRecord{}, fmt.Errorf("get webhook token of %q: %w", appID, err)
	}
	if rec.CreatedAt, err = parseTime(created); err != nil {
		return WebhookTokenRecord{}, fmt.Errorf("webhook token of %q: %w", appID, err)
	}
	return rec, nil
}

// DeleteWebhookToken removes appID's webhook token. Deleting when there is
// none is fine; an unknown app is ErrNotFound.
func (s *Store) DeleteWebhookToken(ctx context.Context, appID string) error {
	if ok, err := exists(ctx, s.db, "SELECT 1 FROM apps WHERE id = ?", appID); err != nil {
		return fmt.Errorf("delete webhook token of %q: %w", appID, err)
	} else if !ok {
		return fmt.Errorf("delete webhook token: app %q: %w", appID, ErrNotFound)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM webhook_tokens WHERE app_id = ?", appID); err != nil {
		return fmt.Errorf("delete webhook token of %q: %w", appID, err)
	}
	return nil
}
