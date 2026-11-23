package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Copies of a backup (feature 052): after a backup succeeds Caravana copies it
// to the other destinations its policy names, one row each.

// Statuses of a copy. pending and running are the open ones.
const (
	CopyPending   = "pending"
	CopyRunning   = "running"
	CopySucceeded = "succeeded"
	CopyFailed    = "failed"
)

// BackupCopy is one copy of one backup to one destination.
type BackupCopy struct {
	AppID, BackupID string
	// Service is the backup's service, read from the backup.
	Service     string
	Destination string
	Status      string
	// Object is the copy's path in the destination, on success.
	Object  string
	Bytes   int64
	Message string
	// Expired means retention deleted the copy from its destination.
	Expired bool
	// JobID is the copy's Nomad job while it runs.
	JobID      string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// copyColumns is the column order scanCopies reads. The copies join their
// backup for its service.
const copyColumns = `c.app_id, c.backup_id, b.service, c.destination, c.status, c.object, c.bytes, c.message, c.expired,
	c.job_id, c.created_at, c.started_at, c.finished_at`

const copyFrom = ` FROM backup_copies c JOIN backups b ON b.app_id = c.app_id AND b.id = c.backup_id`

func scanCopies(rows *sql.Rows) ([]BackupCopy, error) {
	var out []BackupCopy
	for rows.Next() {
		var c BackupCopy
		var expired int
		var created string
		var started, finished sql.NullString
		if err := rows.Scan(&c.AppID, &c.BackupID, &c.Service, &c.Destination, &c.Status, &c.Object, &c.Bytes, &c.Message,
			&expired, &c.JobID, &created, &started, &finished); err != nil {
			return nil, err
		}
		var err error
		c.Expired = expired == 1
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if c.StartedAt, err = parseOptionalTime(started); err != nil {
			return nil, err
		}
		if c.FinishedAt, err = parseOptionalTime(finished); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// QueueBackupCopies makes a pending copy of the backup to each destination in
// names. A copy that already has a row is left as it is.
func (s *Store) QueueBackupCopies(ctx context.Context, appID, backupID string, names []string) error {
	now := formatTime(s.now())
	for _, name := range names {
		if _, err := s.db.ExecContext(ctx, `
			INSERT OR IGNORE INTO backup_copies (app_id, backup_id, destination, status, created_at)
			VALUES (?, ?, ?, 'pending', ?)`, appID, backupID, name, now); err != nil {
			return fmt.Errorf("queue the copy of backup %s of %q to %q: %w", backupID, appID, name, err)
		}
	}
	return nil
}

// ListBackupCopies returns an app's copies by the ID of their backup, each
// backup's copies oldest first (then by destination).
func (s *Store) ListBackupCopies(ctx context.Context, appID string) (map[string][]BackupCopy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+copyColumns+copyFrom+`
		WHERE c.app_id = ? ORDER BY c.created_at, c.destination`, appID)
	if err != nil {
		return nil, fmt.Errorf("list the backup copies of %q: %w", appID, err)
	}
	defer rows.Close()
	list, err := scanCopies(rows)
	if err != nil {
		return nil, fmt.Errorf("list the backup copies of %q: %w", appID, err)
	}
	out := map[string][]BackupCopy{}
	for _, c := range list {
		out[c.BackupID] = append(out[c.BackupID], c)
	}
	return out, nil
}

// OpenBackupCopies returns every copy that is pending or running, across apps,
// oldest first.
func (s *Store) OpenBackupCopies(ctx context.Context) ([]BackupCopy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+copyColumns+copyFrom+`
		WHERE c.status IN ('pending', 'running') ORDER BY c.created_at, c.app_id, c.backup_id, c.destination`)
	if err != nil {
		return nil, fmt.Errorf("open backup copies: %w", err)
	}
	defer rows.Close()
	out, err := scanCopies(rows)
	if err != nil {
		return nil, fmt.Errorf("open backup copies: %w", err)
	}
	return out, nil
}

// SaveBackupCopy writes a copy's progress: its status, object, size, message,
// job and times. The copy must have been queued; an unknown copy is
// ErrNotFound. A copy that is final stays so: a late write of an open status
// is ignored.
func (s *Store) SaveBackupCopy(ctx context.Context, c BackupCopy) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE backup_copies SET status = ?, object = ?, bytes = ?, message = ?, job_id = ?, started_at = ?, finished_at = ?
		WHERE app_id = ? AND backup_id = ? AND destination = ?
		  AND (status IN ('pending', 'running') OR ? IN ('succeeded', 'failed'))`,
		c.Status, c.Object, c.Bytes, c.Message, c.JobID, optionalTime(c.StartedAt), optionalTime(c.FinishedAt),
		c.AppID, c.BackupID, c.Destination, c.Status)
	if err != nil {
		return fmt.Errorf("save the copy of backup %s of %q to %q: %w", c.BackupID, c.AppID, c.Destination, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("save the copy of backup %s of %q to %q: %w", c.BackupID, c.AppID, c.Destination, err)
	} else if n == 0 {
		var found bool
		if found, err = exists(ctx, s.db, "SELECT 1 FROM backup_copies WHERE app_id = ? AND backup_id = ? AND destination = ?",
			c.AppID, c.BackupID, c.Destination); err != nil {
			return fmt.Errorf("save the copy of backup %s of %q to %q: %w", c.BackupID, c.AppID, c.Destination, err)
		}
		if !found {
			return fmt.Errorf("copy of backup %s of %q to %q: %w", c.BackupID, c.AppID, c.Destination, ErrNotFound)
		}
	}
	return nil
}

// ExpireBackupCopies marks the copies of the given backups in one destination
// expired: retention deleted their objects there.
func (s *Store) ExpireBackupCopies(ctx context.Context, appID, destination string, backupIDs []string) error {
	for _, id := range backupIDs {
		if _, err := s.db.ExecContext(ctx, "UPDATE backup_copies SET expired = 1 WHERE app_id = ? AND backup_id = ? AND destination = ?",
			appID, id, destination); err != nil {
			return fmt.Errorf("expire the copy of backup %s of %q in %q: %w", id, appID, destination, err)
		}
	}
	return nil
}

// LatestBackupCopies returns, for every app, service and destination, the
// newest copy that finished, succeeded or failed (by when it was queued, ties by
// backup ID), for the needs-you list: a copy still waiting or running doesn't
// hide the failure before it, and a later success is what resolves it.
func (s *Store) LatestBackupCopies(ctx context.Context) ([]BackupCopy, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+copyColumns+` FROM (
			SELECT c.*, ROW_NUMBER() OVER (PARTITION BY c.app_id, b.service, c.destination
				ORDER BY c.created_at DESC, c.backup_id DESC) AS n
			`+copyFrom+` WHERE c.status IN ('succeeded', 'failed')) c
		JOIN backups b ON b.app_id = c.app_id AND b.id = c.backup_id
		WHERE c.n = 1 ORDER BY c.app_id, b.service, c.destination`)
	if err != nil {
		return nil, fmt.Errorf("latest backup copies: %w", err)
	}
	defer rows.Close()
	out, err := scanCopies(rows)
	if err != nil {
		return nil, fmt.Errorf("latest backup copies: %w", err)
	}
	return out, nil
}
