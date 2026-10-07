package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Backup run statuses. running is the only one that changes.
const (
	BackupRunning   = "running"
	BackupSucceeded = "succeeded"
	BackupFailed    = "failed"
)

// BackupDestination is the installation's backup destination without its
// secret key, which lives in Nomad Variables.
type BackupDestination struct {
	Endpoint, Region, Bucket, Prefix, AccessKeyID string
	UpdatedAt                                     time.Time
}

// GetBackupDestination returns the destination, or ErrNotFound when none is
// set.
func (s *Store) GetBackupDestination(ctx context.Context) (BackupDestination, error) {
	var d BackupDestination
	var updated string
	err := s.db.QueryRowContext(ctx,
		"SELECT endpoint, region, bucket, prefix, access_key_id, updated_at FROM backup_destination WHERE id = 1",
	).Scan(&d.Endpoint, &d.Region, &d.Bucket, &d.Prefix, &d.AccessKeyID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupDestination{}, fmt.Errorf("backup destination: %w", ErrNotFound)
	}
	if err != nil {
		return BackupDestination{}, fmt.Errorf("get backup destination: %w", err)
	}
	if d.UpdatedAt, err = parseTime(updated); err != nil {
		return BackupDestination{}, err
	}
	return d, nil
}

// SetBackupDestination saves the destination, replacing the one there is.
func (s *Store) SetBackupDestination(ctx context.Context, d BackupDestination) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_destination (id, endpoint, region, bucket, prefix, access_key_id, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET endpoint = excluded.endpoint, region = excluded.region,
			bucket = excluded.bucket, prefix = excluded.prefix, access_key_id = excluded.access_key_id,
			updated_at = excluded.updated_at`,
		d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKeyID, formatTime(d.UpdatedAt))
	if err != nil {
		return fmt.Errorf("set backup destination: %w", err)
	}
	return nil
}

// BackupRecord is one backup run of one service.
type BackupRecord struct {
	AppID, ID, Service, JobID string
	Status                    string
	Object                    string
	Bytes                     int64
	Message                   string
	Expired                   bool
	StartedAt                 time.Time
	FinishedAt                *time.Time
}

// SaveBackup creates or updates a backup record. A final record never goes
// back to running: a later write of running is ignored.
func (s *Store) SaveBackup(ctx context.Context, b BackupRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO backups (app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (app_id, id) DO UPDATE SET status = excluded.status, object = excluded.object,
			bytes = excluded.bytes, message = excluded.message, expired = excluded.expired,
			finished_at = excluded.finished_at
		WHERE backups.status = 'running' OR excluded.status != 'running'`,
		b.AppID, b.ID, b.Service, b.JobID, b.Status, b.Object, b.Bytes, b.Message, boolInt(b.Expired),
		formatTime(b.StartedAt), optionalTime(b.FinishedAt))
	if err != nil {
		return fmt.Errorf("save backup %s of %q: %w", b.ID, b.AppID, err)
	}
	return nil
}

// ExpireBackups marks the given backups of an app expired: retention deleted
// their objects.
func (s *Store) ExpireBackups(ctx context.Context, appID string, ids []string) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, "UPDATE backups SET expired = 1 WHERE app_id = ? AND id = ?", appID, id); err != nil {
			return fmt.Errorf("expire backup %s of %q: %w", id, appID, err)
		}
	}
	return nil
}

// ListBackups returns an app's backups, newest first, at most limit (all
// when limit <= 0). Expired ones are included; callers decide.
func (s *Store) ListBackups(ctx context.Context, appID string, limit int) ([]BackupRecord, error) {
	q := `SELECT app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at
		FROM backups WHERE app_id = ? ORDER BY started_at DESC, id DESC`
	args := []any{appID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list backups of %q: %w", appID, err)
	}
	defer rows.Close()
	var out []BackupRecord
	for rows.Next() {
		var b BackupRecord
		var expired int
		var started string
		var finished sql.NullString
		if err := rows.Scan(&b.AppID, &b.ID, &b.Service, &b.JobID, &b.Status, &b.Object, &b.Bytes, &b.Message,
			&expired, &started, &finished); err != nil {
			return nil, fmt.Errorf("list backups of %q: %w", appID, err)
		}
		b.Expired = expired == 1
		if b.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if b.FinishedAt, err = parseOptionalTime(finished); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBackup returns one backup of an app, or ErrNotFound.
func (s *Store) GetBackup(ctx context.Context, appID, id string) (BackupRecord, error) {
	all, err := s.ListBackups(ctx, appID, 0)
	if err != nil {
		return BackupRecord{}, err
	}
	for _, b := range all {
		if b.ID == id {
			return b, nil
		}
	}
	return BackupRecord{}, fmt.Errorf("backup %s of %q: %w", id, appID, ErrNotFound)
}

// RestoreRecord is one restore of one backup.
type RestoreRecord struct {
	ID, AppID, Service, BackupID string
	Status                       string
	Message                      string
	CreatedAt                    time.Time
	FinishedAt                   *time.Time
}

// SaveRestore creates or updates a restore record.
func (s *Store) SaveRestore(ctx context.Context, r RestoreRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO restores (id, app_id, service, backup_id, status, message, created_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, message = excluded.message,
			finished_at = excluded.finished_at
		WHERE restores.status = 'running'`,
		r.ID, r.AppID, r.Service, r.BackupID, r.Status, r.Message, formatTime(r.CreatedAt), optionalTime(r.FinishedAt))
	if err != nil {
		return fmt.Errorf("save restore %s of %q: %w", r.ID, r.AppID, err)
	}
	return nil
}

// ListRestores returns an app's restores, newest first, at most limit (all
// when limit <= 0).
func (s *Store) ListRestores(ctx context.Context, appID string, limit int) ([]RestoreRecord, error) {
	q := `SELECT id, app_id, service, backup_id, status, message, created_at, finished_at
		FROM restores WHERE app_id = ? ORDER BY created_at DESC, id DESC`
	args := []any{appID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list restores of %q: %w", appID, err)
	}
	defer rows.Close()
	var out []RestoreRecord
	for rows.Next() {
		var r RestoreRecord
		var created string
		var finished sql.NullString
		if err := rows.Scan(&r.ID, &r.AppID, &r.Service, &r.BackupID, &r.Status, &r.Message, &created, &finished); err != nil {
			return nil, fmt.Errorf("list restores of %q: %w", appID, err)
		}
		if r.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if r.FinishedAt, err = parseOptionalTime(finished); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunningRestores returns every restore still running, across apps, for the
// watcher.
func (s *Store) RunningRestores(ctx context.Context) ([]RestoreRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT app_id FROM restores WHERE status = 'running'")
	if err != nil {
		return nil, fmt.Errorf("running restores: %w", err)
	}
	var apps []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("running restores: %w", err)
		}
		apps = append(apps, id)
	}
	rows.Close()
	var out []RestoreRecord
	for _, id := range apps {
		list, err := s.ListRestores(ctx, id, 0)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			if r.Status == BackupRunning {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func optionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseOptionalTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
