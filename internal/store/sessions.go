package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SessionRecord is a signed-in browser. IDHash is the SHA-256 of the cookie
// value; the value itself is never stored.
type SessionRecord struct {
	IDHash     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, r SessionRecord) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (id_hash, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?)",
		r.IDHash, formatTime(r.CreatedAt), formatTime(r.LastSeenAt), formatTime(r.ExpiresAt))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession returns the session with that hash, or ErrNotFound.
func (s *Store) GetSession(ctx context.Context, idHash string) (SessionRecord, error) {
	r := SessionRecord{IDHash: idHash}
	var created, seen, expires string
	err := s.db.QueryRowContext(ctx,
		"SELECT created_at, last_seen_at, expires_at FROM sessions WHERE id_hash = ?", idHash,
	).Scan(&created, &seen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRecord{}, fmt.Errorf("session: %w", ErrNotFound)
	}
	if err != nil {
		return SessionRecord{}, fmt.Errorf("get session: %w", err)
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return SessionRecord{}, fmt.Errorf("session: %w", err)
	}
	if r.LastSeenAt, err = parseTime(seen); err != nil {
		return SessionRecord{}, fmt.Errorf("session: %w", err)
	}
	if r.ExpiresAt, err = parseTime(expires); err != nil {
		return SessionRecord{}, fmt.Errorf("session: %w", err)
	}
	return r, nil
}

// TouchSession records that the session was used at the given time.
func (s *Store) TouchSession(ctx context.Context, idHash string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?", formatTime(at), idHash); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// DeleteSession removes one session. A session that is already gone is fine.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id_hash = ?", idHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions past their absolute expiry, or idle
// for longer than idle, as of now.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time, idle time.Duration) error {
	// Times are RFC 3339 text in UTC; compare them as times, not as text,
	// because the fractional seconds have no fixed width.
	rows, err := s.db.QueryContext(ctx, "SELECT id_hash, last_seen_at, expires_at FROM sessions")
	if err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	var dead []string
	for rows.Next() {
		var id, seen, expires string
		if err := rows.Scan(&id, &seen, &expires); err != nil {
			rows.Close()
			return fmt.Errorf("delete expired sessions: %w", err)
		}
		lastSeen, err1 := parseTime(seen)
		expiresAt, err2 := parseTime(expires)
		if err := errors.Join(err1, err2); err != nil {
			rows.Close()
			return fmt.Errorf("delete expired sessions: %w", err)
		}
		if !now.Before(expiresAt) || !now.Before(lastSeen.Add(idle)) {
			dead = append(dead, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	rows.Close() // the single connection must be free before the deletes
	for _, id := range dead {
		if err := s.DeleteSession(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
