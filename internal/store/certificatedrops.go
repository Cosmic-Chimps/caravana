package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Dropping the earlier certificates an upload covers (feature 056): one row per request to the
// ingress server's agent. A row holds the names of what was dropped and one sentence, never a
// certificate, a key or an account.

// ErrCertificateDropOpen is returned when a drop is already requested, running or restarting.
var ErrCertificateDropOpen = errors.New("a certificate drop is already open")

// The states of a certificate drop. requested, running and restarting are the open ones.
const (
	DropRequested  = "requested"  // written, not handed to the agent yet
	DropRunning    = "running"    // handed to the agent; its outcome isn't reported yet
	DropRestarting = "restarting" // the store is rewritten; Traefik must start again
	DropDone       = "done"
	DropFailed     = "failed"
	DropLost       = "lost" // no outcome ever came
)

// CertificateDropOpen says a drop state is an open one.
func CertificateDropOpen(status string) bool {
	return status == DropRequested || status == DropRunning || status == DropRestarting
}

// CertificateDrop is one stored drop. A zero StartedAt, RewrittenAt or FinishedAt is NULL: not known yet.
type CertificateDrop struct {
	ID          string // 32 lowercase hex characters
	ServerIdx   int
	ServerName  string
	Status      string
	Dropped     []string // never nil
	Error       string
	Actor       string // the person's email, "" for Caravana itself
	RequestedAt time.Time
	StartedAt   time.Time
	RewrittenAt time.Time
	FinishedAt  time.Time
}

const certificateDropColumns = `id, server_idx, server_name, status, dropped, error, actor, requested_at, started_at,
	rewritten_at, finished_at`

func scanCertificateDrop(row interface{ Scan(...any) error }) (CertificateDrop, error) {
	var d CertificateDrop
	var dropped, requested string
	var started, rewritten, finished sql.NullString
	if err := row.Scan(&d.ID, &d.ServerIdx, &d.ServerName, &d.Status, &dropped, &d.Error, &d.Actor, &requested,
		&started, &rewritten, &finished); err != nil {
		return CertificateDrop{}, err
	}
	d.Dropped = []string{}
	if err := json.Unmarshal([]byte(dropped), &d.Dropped); err != nil {
		return CertificateDrop{}, fmt.Errorf("the dropped names of %s: %w", d.ID, err)
	}
	var err error
	if d.RequestedAt, err = parseTime(requested); err != nil {
		return CertificateDrop{}, err
	}
	for _, f := range []struct {
		from sql.NullString
		to   *time.Time
	}{{started, &d.StartedAt}, {rewritten, &d.RewrittenAt}, {finished, &d.FinishedAt}} {
		if f.from.Valid {
			if *f.to, err = parseTime(f.from.String); err != nil {
				return CertificateDrop{}, err
			}
		}
	}
	return d, nil
}

// CreateCertificateDrop stores a new drop. A zero RequestedAt is the store's clock and an empty Status is
// "requested". An ID in use is ErrAlreadyExists; any open drop is ErrCertificateDropOpen; a server that
// doesn't exist is ErrNotFound.
func (s *Store) CreateCertificateDrop(ctx context.Context, d CertificateDrop) (CertificateDrop, error) {
	if d.RequestedAt.IsZero() {
		d.RequestedAt = s.now()
	}
	if d.Status == "" {
		d.Status = DropRequested
	}
	if d.Dropped == nil {
		d.Dropped = []string{}
	}
	dropped, err := json.Marshal(d.Dropped)
	if err != nil {
		return CertificateDrop{}, fmt.Errorf("create certificate drop: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO certificate_drops (`+certificateDropColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.ServerIdx, d.ServerName, d.Status, string(dropped), d.Error, d.Actor, formatTime(d.RequestedAt),
		nullIfZero(d.StartedAt), nullIfZero(d.RewrittenAt), nullIfZero(d.FinishedAt))
	switch {
	case isUnique(err) && strings.Contains(err.Error(), "certificate_drops.id"):
		return CertificateDrop{}, fmt.Errorf("certificate drop %q: %w", d.ID, ErrAlreadyExists)
	case isUnique(err):
		return CertificateDrop{}, fmt.Errorf("certificate drop: %w", ErrCertificateDropOpen)
	case isForeignKey(err):
		return CertificateDrop{}, fmt.Errorf("create certificate drop: server %d: %w", d.ServerIdx, ErrNotFound)
	case err != nil:
		return CertificateDrop{}, fmt.Errorf("create certificate drop %q: %w", d.ID, err)
	}
	return d, nil
}

// OpenCertificateDrop returns the drop that is requested, running or restarting, and false when there is
// none. There is at most one.
func (s *Store) OpenCertificateDrop(ctx context.Context) (CertificateDrop, bool, error) {
	d, err := scanCertificateDrop(s.db.QueryRowContext(ctx, `SELECT `+certificateDropColumns+` FROM certificate_drops
		WHERE status IN ('requested', 'running', 'restarting') LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return CertificateDrop{}, false, nil
	}
	if err != nil {
		return CertificateDrop{}, false, fmt.Errorf("read the open certificate drop: %w", err)
	}
	return d, true, nil
}

// ListCertificateDrops returns up to limit drops, newest first.
func (s *Store) ListCertificateDrops(ctx context.Context, limit int) ([]CertificateDrop, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list certificate drops: limit %d, want at least 1", limit)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+certificateDropColumns+` FROM certificate_drops
		ORDER BY requested_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list certificate drops: %w", err)
	}
	defer rows.Close()
	var out []CertificateDrop
	for rows.Next() {
		d, err := scanCertificateDrop(rows)
		if err != nil {
			return nil, fmt.Errorf("list certificate drops: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list certificate drops: %w", err)
	}
	return out, nil
}

// GetCertificateDrop returns one drop, or ErrNotFound.
func (s *Store) GetCertificateDrop(ctx context.Context, id string) (CertificateDrop, error) {
	d, err := scanCertificateDrop(s.db.QueryRowContext(ctx, `SELECT `+certificateDropColumns+` FROM certificate_drops WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CertificateDrop{}, fmt.Errorf("certificate drop %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return CertificateDrop{}, fmt.Errorf("read certificate drop %q: %w", id, err)
	}
	return d, nil
}

// StartCertificateDrop marks a requested drop as running from at: it was handed to the agent. A drop that
// isn't requested is left as it is, so a second sync doesn't move the start; an unknown ID is ErrNotFound.
func (s *Store) StartCertificateDrop(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE certificate_drops SET status = 'running', started_at = ? WHERE id = ? AND status = 'requested'`,
		formatTime(at), id)
	if err != nil {
		return fmt.Errorf("start certificate drop %q: %w", id, err)
	}
	return s.requireCertificateDrop(ctx, res, id)
}

// RestartCertificateDrop moves a requested or running drop to restarting: the agent rewrote the store
// (dropped lists what it removed, possibly nothing) at at. Any other state is left as it is.
func (s *Store) RestartCertificateDrop(ctx context.Context, id string, at time.Time, dropped []string) error {
	if dropped == nil {
		dropped = []string{}
	}
	names, err := json.Marshal(dropped)
	if err != nil {
		return fmt.Errorf("restart certificate drop %q: %w", id, err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE certificate_drops SET status = 'restarting', rewritten_at = ?, dropped = ?
		WHERE id = ? AND status IN ('requested', 'running')`, formatTime(at), string(names), id)
	if err != nil {
		return fmt.Errorf("restart certificate drop %q: %w", id, err)
	}
	return s.requireCertificateDrop(ctx, res, id)
}

// SettleCertificateDrop ends an open drop: its final status (done, failed or lost), when it ended and one
// sentence. A drop that is already settled is left as it is, so the agent's report (repeated by every
// sync until a newer outcome replaces it) never changes it again; an unknown ID is ErrNotFound.
func (s *Store) SettleCertificateDrop(ctx context.Context, id, status string, at time.Time, errText string) error {
	if CertificateDropOpen(status) {
		return fmt.Errorf("settle certificate drop %q: %q is not a final status", id, status)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE certificate_drops SET status = ?, finished_at = ?, error = ?
		WHERE id = ? AND status IN ('requested', 'running', 'restarting')`, status, formatTime(at), errText, id)
	if err != nil {
		return fmt.Errorf("settle certificate drop %q: %w", id, err)
	}
	return s.requireCertificateDrop(ctx, res, id)
}

// requireCertificateDrop says an update touched nothing: a drop that doesn't exist is ErrNotFound, one
// that exists but wasn't in the state the update needed is no error.
func (s *Store) requireCertificateDrop(ctx context.Context, res sql.Result, id string) error {
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM certificate_drops WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("certificate drop %q: %w", id, ErrNotFound)
	}
	return err
}
