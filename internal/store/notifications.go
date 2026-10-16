package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Notifications (feature 018): the channels Caravana tells things to, which
// needs-you items it already told them about, and the queue of messages. The
// secret parts of a channel (a Slack URL, an ntfy token, a webhook's signing
// secret) are not here: they live in a Nomad Variable (constitution VII).

// NotificationChannel is one place messages go, without its secrets.
type NotificationChannel struct {
	ID   string // "nch_" and 12 random hex digits
	Kind string // webhook, slack, discord or ntfy
	Name string
	// Address is what the console shows. For Slack and Discord the path is
	// masked, because the whole URL is the secret.
	Address string
	// Events lists what the channel wants: "problems", "deploy_failed" and
	// "deploy_succeeded", in any combination.
	Events    []string
	Paused    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

const channelColumns = "id, kind, name, address, events, paused, created_at, updated_at"

// SaveNotificationChannel adds a channel, or updates the one with this ID. The
// creation time of an existing channel is kept.
func (s *Store) SaveNotificationChannel(ctx context.Context, c NotificationChannel) error {
	events, err := json.Marshal(c.Events)
	if err != nil {
		return fmt.Errorf("save notification channel %q: %w", c.ID, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO notification_channels (`+channelColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, name = excluded.name, address = excluded.address,
			events = excluded.events, paused = excluded.paused, updated_at = excluded.updated_at`,
		c.ID, c.Kind, c.Name, c.Address, string(events), boolInt(c.Paused), formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save notification channel %q: %w", c.ID, err)
	}
	return nil
}

// ListNotificationChannels returns every channel, oldest first.
func (s *Store) ListNotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+channelColumns+" FROM notification_channels ORDER BY created_at, id")
	if err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	defer rows.Close()
	var out []NotificationChannel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("list notification channels: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	return out, nil
}

// GetNotificationChannel returns the channel with this ID, or ErrNotFound.
func (s *Store) GetNotificationChannel(ctx context.Context, id string) (NotificationChannel, error) {
	c, err := scanChannel(s.db.QueryRowContext(ctx, "SELECT "+channelColumns+" FROM notification_channels WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationChannel{}, fmt.Errorf("notification channel %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return NotificationChannel{}, fmt.Errorf("get notification channel %q: %w", id, err)
	}
	return c, nil
}

// DeleteNotificationChannel removes a channel and, with it, its deliveries
// (the foreign key cascades). An unknown ID is ErrNotFound.
func (s *Store) DeleteNotificationChannel(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM notification_channels WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete notification channel %q: %w", id, err)
	}
	return affectedOrNotFound(res, "notification channel "+id)
}

func scanChannel(row scanner) (NotificationChannel, error) {
	var c NotificationChannel
	var events, created, updated string
	var paused int
	if err := row.Scan(&c.ID, &c.Kind, &c.Name, &c.Address, &events, &paused, &created, &updated); err != nil {
		return NotificationChannel{}, err
	}
	if err := json.Unmarshal([]byte(events), &c.Events); err != nil {
		return NotificationChannel{}, fmt.Errorf("bad stored events %q: %w", events, err)
	}
	c.Paused = paused == 1
	var err error
	if c.CreatedAt, err = parseTime(created); err != nil {
		return NotificationChannel{}, err
	}
	if c.UpdatedAt, err = parseTime(updated); err != nil {
		return NotificationChannel{}, err
	}
	return c, nil
}

// SentProblem is a needs-you item that was already sent as "problem opened"
// and has not been sent as resolved yet. The notifier compares what the
// needs-you list says now with these rows, so a problem that lasts a day is
// sent twice, not every minute, and a restart does not send it again.
type SentProblem struct {
	// Key is the item's identity: kind, app, service and deployment joined.
	Key      string
	Kind     string
	AppID    string // empty for an installation-wide problem
	Service  string
	OpenedAt time.Time
	// LastSeenAt is the last check that still found the item. A problem is
	// resolved once it has been gone for a while since then.
	LastSeenAt time.Time
	// Summary is the title that was sent, reused for the "resolved" message.
	Summary string
}

const problemColumns = "key, kind, app_id, service, opened_at, last_seen_at, summary"

// ListSentProblems returns every sent problem, oldest first.
func (s *Store) ListSentProblems(ctx context.Context) ([]SentProblem, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+problemColumns+" FROM sent_problems ORDER BY opened_at, key")
	if err != nil {
		return nil, fmt.Errorf("list sent problems: %w", err)
	}
	defer rows.Close()
	var out []SentProblem
	for rows.Next() {
		var p SentProblem
		var opened, seen string
		if err := rows.Scan(&p.Key, &p.Kind, &p.AppID, &p.Service, &opened, &seen, &p.Summary); err != nil {
			return nil, fmt.Errorf("list sent problems: %w", err)
		}
		if p.OpenedAt, err = parseTime(opened); err != nil {
			return nil, fmt.Errorf("list sent problems: %w", err)
		}
		if p.LastSeenAt, err = parseTime(seen); err != nil {
			return nil, fmt.Errorf("list sent problems: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sent problems: %w", err)
	}
	return out, nil
}

// UpsertSentProblem records a problem as sent, or replaces the row with the
// same key.
func (s *Store) UpsertSentProblem(ctx context.Context, p SentProblem) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sent_problems (`+problemColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET kind = excluded.kind, app_id = excluded.app_id, service = excluded.service,
			opened_at = excluded.opened_at, last_seen_at = excluded.last_seen_at, summary = excluded.summary`,
		p.Key, p.Kind, p.AppID, p.Service, formatTime(p.OpenedAt), formatTime(p.LastSeenAt), p.Summary)
	if err != nil {
		return fmt.Errorf("save sent problem %q: %w", p.Key, err)
	}
	return nil
}

// TouchSentProblems sets the last-seen time of the problems with these keys.
// A key with no row is skipped.
func (s *Store) TouchSentProblems(ctx context.Context, keys []string, at time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("touch sent problems: %w", err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, "UPDATE sent_problems SET last_seen_at = ? WHERE key = ?", formatTime(at), key); err != nil {
			return fmt.Errorf("touch sent problem %q: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("touch sent problems: %w", err)
	}
	return nil
}

// DeleteSentProblem forgets a problem, because it was resolved or because its
// app is gone. A key with no row is not an error.
func (s *Store) DeleteSentProblem(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sent_problems WHERE key = ?", key); err != nil {
		return fmt.Errorf("delete sent problem %q: %w", key, err)
	}
	return nil
}

// DeleteSentProblemsOfApp forgets every problem of an app. An app with none is
// not an error.
func (s *Store) DeleteSentProblemsOfApp(ctx context.Context, appID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sent_problems WHERE app_id = ?", appID); err != nil {
		return fmt.Errorf("delete sent problems of app %q: %w", appID, err)
	}
	return nil
}

// The states of a delivery.
const (
	DeliveryPending = "pending" // queued, or waiting for its next attempt
	DeliverySent    = "sent"
	DeliveryFailed  = "failed" // gave up, or the channel went away
)

// Delivery is one message to one channel: the queue the sender works through
// and the history the console lists.
type Delivery struct {
	ID        string // "ndl_" and 12 random hex digits
	ChannelID string
	EventType string // problem.opened, problem.resolved, deploy.succeeded, deploy.failed or test
	Title     string
	// Payload is the event as JSON. It holds no secret.
	Payload  string
	Status   string
	Attempts int
	// NextAt is when a pending delivery may be tried next.
	NextAt     time.Time
	Error      string
	CreatedAt  time.Time
	FinishedAt *time.Time // nil while pending
}

const deliveryColumns = "id, channel_id, event_type, title, payload, status, attempts, next_at, error, created_at, finished_at"

// EnqueueDelivery stores a delivery as given. The notifier and the deploy hook
// store it pending; recording a test that was sent at once stores it with its
// final status. The channel must exist.
func (s *Store) EnqueueDelivery(ctx context.Context, d Delivery) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_deliveries (`+deliveryColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.ChannelID, d.EventType, d.Title, d.Payload, d.Status, d.Attempts,
		formatTime(d.NextAt), d.Error, formatTime(d.CreatedAt), optionalTime(d.FinishedAt))
	if err != nil {
		return fmt.Errorf("enqueue delivery %q: %w", d.ID, err)
	}
	return nil
}

// DueDeliveries returns up to limit pending deliveries whose next attempt is
// due at now, the longest waiting first.
func (s *Store) DueDeliveries(ctx context.Context, now time.Time, limit int) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+deliveryColumns+
		" FROM notification_deliveries WHERE status = 'pending' AND next_at <= ? ORDER BY next_at, id LIMIT ?",
		formatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("list due deliveries: %w", err)
	}
	return scanDeliveries(rows, "list due deliveries")
}

// FinishDelivery ends a delivery: status is DeliverySent or DeliveryFailed,
// attempts is how many were made, and errMsg says why it failed (empty when it
// was sent). An unknown ID is ErrNotFound.
func (s *Store) FinishDelivery(ctx context.Context, id, status string, attempts int, errMsg string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE notification_deliveries SET status = ?, attempts = ?, error = ?, finished_at = ? WHERE id = ?",
		status, attempts, errMsg, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("finish delivery %q: %w", id, err)
	}
	return affectedOrNotFound(res, "delivery "+id)
}

// RetryDelivery keeps a delivery pending after a failed attempt: attempts is
// how many were made, nextAt is when to try again and errMsg is what the last
// attempt said. An unknown ID is ErrNotFound.
func (s *Store) RetryDelivery(ctx context.Context, id string, attempts int, nextAt time.Time, errMsg string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE notification_deliveries SET attempts = ?, next_at = ?, error = ? WHERE id = ? AND status = 'pending'",
		attempts, formatTime(nextAt), errMsg, id)
	if err != nil {
		return fmt.Errorf("retry delivery %q: %w", id, err)
	}
	return affectedOrNotFound(res, "pending delivery "+id)
}

// ListDeliveries returns the newest limit deliveries of a channel, newest
// first, pending ones included.
func (s *Store) ListDeliveries(ctx context.Context, channelID string, limit int) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+deliveryColumns+
		" FROM notification_deliveries WHERE channel_id = ? ORDER BY created_at DESC, id DESC LIMIT ?", channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("list deliveries of channel %q: %w", channelID, err)
	}
	return scanDeliveries(rows, "list deliveries of channel "+channelID)
}

// PruneDeliveries deletes the sent and failed deliveries of a channel beyond
// the newest keep. Pending ones are never deleted, since they are messages not
// yet sent.
func (s *Store) PruneDeliveries(ctx context.Context, channelID string, keep int) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM notification_deliveries
		WHERE channel_id = ? AND status <> 'pending' AND id NOT IN (
			SELECT id FROM notification_deliveries WHERE channel_id = ? AND status <> 'pending'
			ORDER BY created_at DESC, id DESC LIMIT ?)`, channelID, channelID, keep)
	if err != nil {
		return fmt.Errorf("prune deliveries of channel %q: %w", channelID, err)
	}
	return nil
}

// scanDeliveries reads every row and closes them; what names the query for
// errors.
func scanDeliveries(rows *sql.Rows, what string) ([]Delivery, error) {
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		var next, created string
		var finished sql.NullString
		if err := rows.Scan(&d.ID, &d.ChannelID, &d.EventType, &d.Title, &d.Payload, &d.Status, &d.Attempts,
			&next, &d.Error, &created, &finished); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		var err error
		if d.NextAt, err = parseTime(next); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		if d.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		if finished.Valid {
			at, err := parseTime(finished.String)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", what, err)
			}
			d.FinishedAt = &at
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}
