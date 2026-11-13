package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

// RestoreSkipped is a restore status only a scheduled run gets: its slot came
// while a backup or restore of the service was running, so it did nothing. It
// is final, like failed, but never counts as a failure.
const RestoreSkipped = "skipped"

// Who started a restore: the owner (Restore) or the service's restore schedule.
const (
	RestoreTriggerOwner    = "owner"
	RestoreTriggerSchedule = "schedule"
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

// enginePostgres is what a record without an engine means: every run made
// before feature 016. It is the value of app.BackupPostgres; the store doesn't
// import the app package for one word.
const enginePostgres = "postgres"

// VolumeArchive is one volume's part of a volume backup.
type VolumeArchive struct {
	Name string `json:"name"`
	// Bytes is the size of the archive in the destination.
	Bytes int64 `json:"bytes"`
	// SizeKB is the size of the volume's data when it was archived, in
	// kilobytes (the unit du reports).
	SizeKB int64 `json:"sizeKB"`
}

// Who started a backup run: its schedule, or the owner (Back up now).
const (
	BackupTriggerTimer = "timer"
	BackupTriggerOwner = "owner"
)

// BackupRecord is one backup run of one service.
type BackupRecord struct {
	AppID, ID, Service, JobID string
	// Engine is "postgres" or "volume". Empty means "postgres" when saved.
	Engine     string
	Status     string
	Object     string
	Bytes      int64
	Message    string
	Expired    bool
	StartedAt  time.Time
	FinishedAt *time.Time
	// Stopped is how long the service was stopped. Only a volume run has it,
	// and only once it was measured; nil otherwise.
	Stopped *time.Duration
	// Volumes lists the archives of a volume backup, one per volume.
	Volumes []VolumeArchive
	// Trigger is BackupTriggerTimer or BackupTriggerOwner. Empty means timer
	// when saved. Once a run is recorded as the owner's it stays so: the
	// watcher, which can't tell a forced run from a timed one, writes timer.
	Trigger string
	// Pin is the label the backup is pinned under, "" when it isn't. Retention
	// never deletes a pinned backup. SaveBackup never writes it: PinBackup and
	// UnpinBackup do.
	Pin string
	// PinnedAt is when the label was put on the backup; nil when not pinned.
	PinnedAt *time.Time
}

// backupColumns is the column order scanBackups reads.
const backupColumns = `app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at,
	engine, stopped_ms, volumes, trigger, pin, pinned_at`

// SaveBackup creates or updates a backup record. A final record never goes
// back to running: a later write of running is ignored.
func (s *Store) SaveBackup(ctx context.Context, b BackupRecord) error {
	if b.Engine == "" {
		b.Engine = enginePostgres
	}
	if b.Trigger == "" {
		b.Trigger = BackupTriggerTimer
	}
	var volumes any // NULL, not "null", when there are none
	if len(b.Volumes) > 0 {
		doc, err := json.Marshal(b.Volumes)
		if err != nil {
			return fmt.Errorf("save backup %s of %q: %w", b.ID, b.AppID, err)
		}
		volumes = string(doc)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO backups (app_id, id, service, job_id, status, object, bytes, message, expired, started_at, finished_at,
			engine, stopped_ms, volumes, trigger)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (app_id, id) DO UPDATE SET status = excluded.status, object = excluded.object,
			bytes = excluded.bytes, message = excluded.message, expired = excluded.expired,
			finished_at = excluded.finished_at, stopped_ms = excluded.stopped_ms, volumes = excluded.volumes
		WHERE backups.status = 'running' OR excluded.status != 'running'`,
		b.AppID, b.ID, b.Service, b.JobID, b.Status, b.Object, b.Bytes, b.Message, boolInt(b.Expired),
		formatTime(b.StartedAt), optionalTime(b.FinishedAt), b.Engine, optionalMillis(b.Stopped), volumes, b.Trigger)
	if err != nil {
		return fmt.Errorf("save backup %s of %q: %w", b.ID, b.AppID, err)
	}
	// The owner's word is kept whatever the upsert above did: the watcher may
	// have recorded a forced run (as timer, even as finished) before Back up
	// now wrote its own record.
	if b.Trigger == BackupTriggerOwner {
		if _, err := s.db.ExecContext(ctx, "UPDATE backups SET trigger = 'owner' WHERE app_id = ? AND id = ?", b.AppID, b.ID); err != nil {
			return fmt.Errorf("save backup %s of %q: %w", b.ID, b.AppID, err)
		}
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
	q := `SELECT ` + backupColumns + `
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
	out, err := scanBackups(rows)
	if err != nil {
		return nil, fmt.Errorf("list backups of %q: %w", appID, err)
	}
	return out, nil
}

// scanBackups reads backup rows in the order of backupColumns.
func scanBackups(rows *sql.Rows) ([]BackupRecord, error) {
	var out []BackupRecord
	for rows.Next() {
		var b BackupRecord
		var expired int
		var started string
		var finished, volumes, pin, pinnedAt sql.NullString
		var stopped sql.NullInt64
		if err := rows.Scan(&b.AppID, &b.ID, &b.Service, &b.JobID, &b.Status, &b.Object, &b.Bytes, &b.Message,
			&expired, &started, &finished, &b.Engine, &stopped, &volumes, &b.Trigger, &pin, &pinnedAt); err != nil {
			return nil, err
		}
		var err error
		b.Expired = expired == 1
		b.Pin = pin.String
		if b.PinnedAt, err = parseOptionalTime(pinnedAt); err != nil {
			return nil, err
		}
		if b.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if b.FinishedAt, err = parseOptionalTime(finished); err != nil {
			return nil, err
		}
		b.Stopped = millisToDuration(stopped)
		if volumes.Valid {
			if err := json.Unmarshal([]byte(volumes.String), &b.Volumes); err != nil {
				return nil, fmt.Errorf("backup %s of %q: bad stored volumes: %w", b.ID, b.AppID, err)
			}
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
	// Engine is "postgres" or "volume". Empty means "postgres" when saved.
	Engine     string
	Status     string
	Message    string
	CreatedAt  time.Time
	FinishedAt *time.Time
	// Stopped is how long the service was stopped, for a volume restore once
	// measured; nil otherwise.
	Stopped *time.Duration
	// Trigger is RestoreTriggerOwner or RestoreTriggerSchedule. Empty means
	// owner when saved: every restore recorded before feature 036 was the owner's.
	Trigger string
	// Pin is the label a scheduled run asked for ("" for the owner's). It is
	// kept when the run was refused, so the record says which label was missing.
	Pin string
}

// restoreColumns is the column order scanRestores reads.
const restoreColumns = "id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms, trigger, pin"

// SaveRestore creates or updates a restore record.
func (s *Store) SaveRestore(ctx context.Context, r RestoreRecord) error {
	if r.Engine == "" {
		r.Engine = enginePostgres
	}
	if r.Trigger == "" {
		r.Trigger = RestoreTriggerOwner
	}
	var pin any // NULL, not "", for the owner's restores
	if r.Pin != "" {
		pin = r.Pin
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO restores (id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms,
			trigger, pin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, message = excluded.message,
			finished_at = excluded.finished_at, stopped_ms = excluded.stopped_ms
		WHERE restores.status = 'running'`,
		r.ID, r.AppID, r.Service, r.BackupID, r.Status, r.Message, formatTime(r.CreatedAt), optionalTime(r.FinishedAt),
		r.Engine, optionalMillis(r.Stopped), r.Trigger, pin)
	if err != nil {
		return fmt.Errorf("save restore %s of %q: %w", r.ID, r.AppID, err)
	}
	return nil
}

// ListRestores returns an app's restores, newest first, at most limit (all
// when limit <= 0).
func (s *Store) ListRestores(ctx context.Context, appID string, limit int) ([]RestoreRecord, error) {
	q := `SELECT ` + restoreColumns + `
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
	out, err := scanRestores(rows)
	if err != nil {
		return nil, fmt.Errorf("list restores of %q: %w", appID, err)
	}
	return out, nil
}

// scanRestores reads restore rows in the order of restoreColumns.
func scanRestores(rows *sql.Rows) ([]RestoreRecord, error) {
	var out []RestoreRecord
	for rows.Next() {
		var r RestoreRecord
		var created string
		var finished, pin sql.NullString
		var stopped sql.NullInt64
		if err := rows.Scan(&r.ID, &r.AppID, &r.Service, &r.BackupID, &r.Status, &r.Message, &created, &finished,
			&r.Engine, &stopped, &r.Trigger, &pin); err != nil {
			return nil, err
		}
		r.Pin = pin.String
		var err error
		if r.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if r.FinishedAt, err = parseOptionalTime(finished); err != nil {
			return nil, err
		}
		r.Stopped = millisToDuration(stopped)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestBackupRuns returns, for every service of every app, its newest
// backup and its newest restore (by start time, ties by ID), for the
// needs-you list. Expired backups count: the question is whether the last
// run worked, not whether its file is still kept. A skipped restore doesn't
// count: it did nothing, so it must never hide the failure before it.
func (s *Store) LatestBackupRuns(ctx context.Context) ([]BackupRecord, []RestoreRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+backupColumns+` FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY app_id, service ORDER BY started_at DESC, id DESC) AS n
			FROM backups)
		WHERE n = 1 ORDER BY app_id, service`)
	if err != nil {
		return nil, nil, fmt.Errorf("latest backups: %w", err)
	}
	backups, err := scanBackups(rows)
	rows.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("latest backups: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT `+restoreColumns+` FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY app_id, service ORDER BY created_at DESC, id DESC) AS n
			FROM restores WHERE status != 'skipped')
		WHERE n = 1 ORDER BY app_id, service`)
	if err != nil {
		return nil, nil, fmt.Errorf("latest restores: %w", err)
	}
	restores, err := scanRestores(rows)
	rows.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("latest restores: %w", err)
	}
	return backups, restores, nil
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

// optionalMillis is a duration as stored: whole milliseconds, NULL for nil.
func optionalMillis(d *time.Duration) any {
	if d == nil {
		return nil
	}
	return d.Milliseconds()
}

// millisToDuration is optionalMillis read back.
func millisToDuration(ms sql.NullInt64) *time.Duration {
	if !ms.Valid {
		return nil
	}
	d := time.Duration(ms.Int64) * time.Millisecond
	return &d
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

// PinBackup puts the label on a backup. A label is unique per service, so a
// backup that held it loses it (prev is that backup's ID, "" for none, so a
// caller can put it back); a backup that had another label gets this one
// instead. It changes nothing but the pin columns, in one transaction.
func (s *Store) PinBackup(ctx context.Context, appID, backupID, label string, at time.Time) (prev string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("pin backup %s of %q: %w", backupID, appID, err)
	}
	defer tx.Rollback()

	var service string
	if err := tx.QueryRowContext(ctx, "SELECT service FROM backups WHERE app_id = ? AND id = ?", appID, backupID).Scan(&service); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("backup %s of %q: %w", backupID, appID, ErrNotFound)
		}
		return "", fmt.Errorf("pin backup %s of %q: %w", backupID, appID, err)
	}
	err = tx.QueryRowContext(ctx, "SELECT id FROM backups WHERE app_id = ? AND service = ? AND pin = ?", appID, service, label).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("pin backup %s of %q: %w", backupID, appID, err)
	}
	if prev == backupID {
		prev = "" // already the holder: nothing to put back
	}
	// The label leaves its old holder first: the unique index allows one.
	if _, err := tx.ExecContext(ctx, "UPDATE backups SET pin = NULL, pinned_at = NULL WHERE app_id = ? AND service = ? AND pin = ?",
		appID, service, label); err != nil {
		return "", fmt.Errorf("pin backup %s of %q: %w", backupID, appID, err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE backups SET pin = ?, pinned_at = ? WHERE app_id = ? AND id = ?",
		label, formatTime(at), appID, backupID); err != nil {
		return "", fmt.Errorf("pin backup %s of %q: %w", backupID, appID, err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("pin backup %s of %q: %w", backupID, appID, err)
	}
	return prev, nil
}

// UnpinBackup takes the label off a backup. A backup with no label, or one
// that doesn't exist, is not an error: the result is the same.
func (s *Store) UnpinBackup(ctx context.Context, appID, backupID string) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE backups SET pin = NULL, pinned_at = NULL WHERE app_id = ? AND id = ?", appID, backupID); err != nil {
		return fmt.Errorf("unpin backup %s of %q: %w", backupID, appID, err)
	}
	return nil
}

// PinnedBackup returns the backup of a service that is pinned under the label,
// or ErrNotFound. An expired one is returned too: the caller says why it can't
// be restored.
func (s *Store) PinnedBackup(ctx context.Context, appID, service, label string) (BackupRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+backupColumns+`
		FROM backups WHERE app_id = ? AND service = ? AND pin = ?`, appID, service, label)
	if err != nil {
		return BackupRecord{}, fmt.Errorf("pinned backup of %q/%s: %w", appID, service, err)
	}
	defer rows.Close()
	list, err := scanBackups(rows)
	if err != nil {
		return BackupRecord{}, fmt.Errorf("pinned backup of %q/%s: %w", appID, service, err)
	}
	if len(list) == 0 {
		return BackupRecord{}, fmt.Errorf("backup of %q/%s pinned as %q: %w", appID, service, label, ErrNotFound)
	}
	return list[0], nil
}

// PinnedBackups returns the pinned backups of an app that retention may not
// delete (not expired), for the pin lists the engines' retention reads.
func (s *Store) PinnedBackups(ctx context.Context, appID string) ([]BackupRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+backupColumns+`
		FROM backups WHERE app_id = ? AND pin IS NOT NULL AND expired = 0 ORDER BY service, id`, appID)
	if err != nil {
		return nil, fmt.Errorf("pinned backups of %q: %w", appID, err)
	}
	defer rows.Close()
	out, err := scanBackups(rows)
	if err != nil {
		return nil, fmt.Errorf("pinned backups of %q: %w", appID, err)
	}
	return out, nil
}

// LastScheduledRestore returns when the service's newest scheduled restore was
// made, whatever its outcome (a refused or skipped slot counts: the clock must
// not run the same slot again). ok is false when it never had one.
func (s *Store) LastScheduledRestore(ctx context.Context, appID, service string) (at time.Time, ok bool, err error) {
	var created sql.NullString
	err = s.db.QueryRowContext(ctx, "SELECT MAX(created_at) FROM restores WHERE app_id = ? AND service = ? AND trigger = 'schedule'",
		appID, service).Scan(&created)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("last scheduled restore of %q/%s: %w", appID, service, err)
	}
	if !created.Valid {
		return time.Time{}, false, nil
	}
	if at, err = parseTime(created.String); err != nil {
		return time.Time{}, false, err
	}
	return at, true, nil
}
