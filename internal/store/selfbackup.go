package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Caravana's own backup (feature 063): the owner's settings with the public half of the recovery
// key, one row per run, and the record of a restore. A row holds names, sizes and one sentence: never a
// key, a variable or a byte of a backup.

// The statuses and triggers of a backup run.
const (
	SelfBackupRunning   = "running"
	SelfBackupSucceeded = "succeeded"
	SelfBackupFailed    = "failed"

	SelfBackupBySchedule = "schedule"
	SelfBackupByOwner    = "owner"
	SelfBackupByKey      = "key"
)

// SelfBackupSettings is the owner's one row. PublicKey is empty until a recovery key was made; KeyCreatedAt
// is zero then.
type SelfBackupSettings struct {
	Enabled        bool
	Hour           int // UTC, 0 to 23
	Keep           int // 1 to 90
	PublicKey      []byte
	KeyFingerprint string
	KeyCreatedAt   time.Time
}

// HasKey says a recovery key was made.
func (s SelfBackupSettings) HasKey() bool { return len(s.PublicKey) > 0 }

// SelfBackupSettings reads the settings.
func (s *Store) SelfBackupSettings(ctx context.Context) (SelfBackupSettings, error) {
	var out SelfBackupSettings
	var enabled int
	var pub string
	var created sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT enabled, hour, keep, public_key, key_fingerprint, key_created_at
		FROM self_backup_settings WHERE id = 1`).Scan(&enabled, &out.Hour, &out.Keep, &pub, &out.KeyFingerprint, &created)
	if err != nil {
		return SelfBackupSettings{}, fmt.Errorf("read the self-backup settings: %w", err)
	}
	out.Enabled = enabled == 1
	if pub != "" {
		if out.PublicKey, err = base64.StdEncoding.DecodeString(pub); err != nil {
			return SelfBackupSettings{}, fmt.Errorf("read the self-backup settings: the public key: %w", err)
		}
	}
	if created.Valid {
		if out.KeyCreatedAt, err = parseTime(created.String); err != nil {
			return SelfBackupSettings{}, fmt.Errorf("read the self-backup settings: %w", err)
		}
	}
	return out, nil
}

// SetSelfBackupSchedule stores whether the daily backup is on, its UTC hour and how many to keep. A number
// outside the table's ranges is refused by it.
func (s *Store) SetSelfBackupSchedule(ctx context.Context, enabled bool, hour, keep int) error {
	on := 0
	if enabled {
		on = 1
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE self_backup_settings SET enabled = ?, hour = ?, keep = ? WHERE id = 1`, on, hour, keep); err != nil {
		return fmt.Errorf("save the self-backup settings: %w", err)
	}
	return nil
}

// SetSelfBackupKey stores the public half of a new recovery key, its fingerprint and when it was made. It
// replaces the one before.
func (s *Store) SetSelfBackupKey(ctx context.Context, public []byte, fingerprint string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE self_backup_settings SET public_key = ?, key_fingerprint = ?, key_created_at = ? WHERE id = 1`,
		base64.StdEncoding.EncodeToString(public), fingerprint, formatTime(at)); err != nil {
		return fmt.Errorf("save the recovery key's public half: %w", err)
	}
	return nil
}

// SelfBackup is one run.
type SelfBackup struct {
	ID             string
	Status         string
	Trigger        string
	Actor          string
	StartedAt      time.Time
	EndedAt        time.Time // zero while running
	Destination    string
	Object         string
	Bytes          int64
	KeyFingerprint string
	Version        string
	Schema         int
	Warning        string
	Error          string
}

// SelfBackupResult is what a finished run records.
type SelfBackupResult struct {
	Destination, Object, KeyFingerprint, Version, Warning, Error string
	Bytes                                                        int64
	Schema                                                       int
}

// ErrSelfBackupOpen is returned when a backup run is already running.
var ErrSelfBackupOpen = errors.New("a backup of Caravana is already running")

const selfBackupColumns = `id, status, trigger, actor, started_at, ended_at, destination, object, bytes, key_fingerprint,
	version, schema_version, warning, error`

func scanSelfBackup(row interface{ Scan(...any) error }) (SelfBackup, error) {
	var b SelfBackup
	var started string
	var ended sql.NullString
	if err := row.Scan(&b.ID, &b.Status, &b.Trigger, &b.Actor, &started, &ended, &b.Destination, &b.Object, &b.Bytes,
		&b.KeyFingerprint, &b.Version, &b.Schema, &b.Warning, &b.Error); err != nil {
		return SelfBackup{}, err
	}
	var err error
	if b.StartedAt, err = parseTime(started); err != nil {
		return SelfBackup{}, err
	}
	if ended.Valid {
		if b.EndedAt, err = parseTime(ended.String); err != nil {
			return SelfBackup{}, err
		}
	}
	return b, nil
}

// InsertSelfBackup stores a run that is starting (status running). A zero StartedAt is the store's clock.
// Another run that is running is ErrSelfBackupOpen; an ID in use is ErrAlreadyExists.
func (s *Store) InsertSelfBackup(ctx context.Context, b SelfBackup) (SelfBackup, error) {
	if b.StartedAt.IsZero() {
		b.StartedAt = s.now()
	}
	b.Status = SelfBackupRunning
	_, err := s.db.ExecContext(ctx, `INSERT INTO self_backups (id, status, trigger, actor, started_at, destination, key_fingerprint)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, b.ID, b.Status, b.Trigger, b.Actor, formatTime(b.StartedAt), b.Destination, b.KeyFingerprint)
	switch {
	case isUnique(err):
		// Either the ID is taken or another run is running; SQLite names only one of the two.
		if _, getErr := s.GetSelfBackup(ctx, b.ID); getErr == nil {
			return SelfBackup{}, fmt.Errorf("self backup %q: %w", b.ID, ErrAlreadyExists)
		}
		return SelfBackup{}, ErrSelfBackupOpen
	case err != nil:
		return SelfBackup{}, fmt.Errorf("insert a self backup %q: %w", b.ID, err)
	}
	return b, nil
}

// FinishSelfBackup ends a running run as succeeded or failed with what it recorded. A run that isn't
// running is left as it is; an unknown ID is ErrNotFound.
func (s *Store) FinishSelfBackup(ctx context.Context, id, status string, at time.Time, r SelfBackupResult) error {
	if status != SelfBackupSucceeded && status != SelfBackupFailed {
		return fmt.Errorf("finish self backup %q: %q is not a final status", id, status)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE self_backups SET status = ?, ended_at = ?, destination = ?, object = ?, bytes = ?,
		key_fingerprint = ?, version = ?, schema_version = ?, warning = ?, error = ? WHERE id = ? AND status = 'running'`,
		status, formatTime(at), r.Destination, r.Object, r.Bytes, r.KeyFingerprint, r.Version, r.Schema, r.Warning, r.Error, id)
	if err != nil {
		return fmt.Errorf("finish self backup %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	if _, err := s.GetSelfBackup(ctx, id); err != nil {
		return err
	}
	return nil
}

// FailRunningSelfBackups settles the runs a process left running: failed, with reason. It returns how many.
func (s *Store) FailRunningSelfBackups(ctx context.Context, at time.Time, reason string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE self_backups SET status = 'failed', ended_at = ?, error = ? WHERE status = 'running'`,
		formatTime(at), reason)
	if err != nil {
		return 0, fmt.Errorf("fail the self backups left running: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// GetSelfBackup returns one run, or ErrNotFound.
func (s *Store) GetSelfBackup(ctx context.Context, id string) (SelfBackup, error) {
	b, err := scanSelfBackup(s.db.QueryRowContext(ctx, `SELECT `+selfBackupColumns+` FROM self_backups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SelfBackup{}, fmt.Errorf("self backup %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return SelfBackup{}, fmt.Errorf("read self backup %q: %w", id, err)
	}
	return b, nil
}

// ListSelfBackups returns up to limit runs, newest first.
func (s *Store) ListSelfBackups(ctx context.Context, limit int) ([]SelfBackup, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list self backups: limit %d, want at least 1", limit)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+selfBackupColumns+` FROM self_backups ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list self backups: %w", err)
	}
	defer rows.Close()
	var out []SelfBackup
	for rows.Next() {
		b, err := scanSelfBackup(rows)
		if err != nil {
			return nil, fmt.Errorf("list self backups: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LatestSelfBackup returns the newest run in any state, and false when there is none.
func (s *Store) LatestSelfBackup(ctx context.Context) (SelfBackup, bool, error) {
	return s.oneSelfBackup(ctx, "", "latest")
}

// LatestFinishedSelfBackup returns the newest run that has ended (succeeded or failed).
func (s *Store) LatestFinishedSelfBackup(ctx context.Context) (SelfBackup, bool, error) {
	return s.oneSelfBackup(ctx, "WHERE status <> 'running'", "latest finished")
}

// LatestSucceededSelfBackup returns the newest run that succeeded.
func (s *Store) LatestSucceededSelfBackup(ctx context.Context) (SelfBackup, bool, error) {
	return s.oneSelfBackup(ctx, "WHERE status = 'succeeded'", "latest succeeded")
}

// OpenSelfBackup returns the run that is running, and false when there is none.
func (s *Store) OpenSelfBackup(ctx context.Context) (SelfBackup, bool, error) {
	return s.oneSelfBackup(ctx, "WHERE status = 'running'", "open")
}

func (s *Store) oneSelfBackup(ctx context.Context, where, what string) (SelfBackup, bool, error) {
	b, err := scanSelfBackup(s.db.QueryRowContext(ctx, `SELECT `+selfBackupColumns+` FROM self_backups `+where+
		` ORDER BY started_at DESC, id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return SelfBackup{}, false, nil
	}
	if err != nil {
		return SelfBackup{}, false, fmt.Errorf("read the %s self backup: %w", what, err)
	}
	return b, true, nil
}

// ScheduledSelfBackupSince says a run the daily clock started at or after since has ended, succeeded or
// failed: the slot is spent. A run that is still running isn't counted (it is the pass's own).
func (s *Store) ScheduledSelfBackupSince(ctx context.Context, since time.Time) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM self_backups WHERE trigger = 'schedule' AND status <> 'running'
		AND started_at >= ?`, formatTime(since)).Scan(&n); err != nil {
		return false, fmt.Errorf("read the scheduled self backups: %w", err)
	}
	return n > 0, nil
}

// SelfRestore is the record of a restore that `caravana admin restore` made.
type SelfRestore struct {
	ID              string
	Status          string // pending, running or done
	Object          string
	BackupCreatedAt time.Time
	BackupVersion   string
	RestoredAt      time.Time
	EndedAt         time.Time
}

// The statuses of a restore and of its items.
const (
	SelfRestorePending = "pending"
	SelfRestoreRunning = "running"
	SelfRestoreDone    = "done"

	RestoreItemRedeploy = "redeploy"
	RestoreItemData     = "data"

	RestoreItemWaiting = "waiting"
	RestoreItemRunning = "running"
	RestoreItemDone    = "done"
	RestoreItemFailed  = "failed"
	RestoreItemSkipped = "skipped"
)

// SelfRestoreItem is one thing Caravana does after a restore: redeploy an app, or restore a service's data.
type SelfRestoreItem struct {
	RestoreID string
	Seq       int
	Kind      string
	App       string
	Service   string
	Status    string
	Ref       string
	Reason    string
	StartedAt time.Time
	EndedAt   time.Time
}

const selfRestoreColumns = `id, status, object, backup_created_at, backup_version, restored_at, ended_at`

func scanSelfRestore(row interface{ Scan(...any) error }) (SelfRestore, error) {
	var r SelfRestore
	var created, restored string
	var ended sql.NullString
	if err := row.Scan(&r.ID, &r.Status, &r.Object, &created, &r.BackupVersion, &restored, &ended); err != nil {
		return SelfRestore{}, err
	}
	var err error
	if r.BackupCreatedAt, err = parseTime(created); err != nil {
		return SelfRestore{}, err
	}
	if r.RestoredAt, err = parseTime(restored); err != nil {
		return SelfRestore{}, err
	}
	if ended.Valid {
		if r.EndedAt, err = parseTime(ended.String); err != nil {
			return SelfRestore{}, err
		}
	}
	return r, nil
}

// InsertSelfRestore stores a restore (status pending unless it says otherwise).
func (s *Store) InsertSelfRestore(ctx context.Context, r SelfRestore) (SelfRestore, error) {
	if r.Status == "" {
		r.Status = SelfRestorePending
	}
	if r.RestoredAt.IsZero() {
		r.RestoredAt = s.now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO self_restores (`+selfRestoreColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Status, r.Object, formatTime(r.BackupCreatedAt), r.BackupVersion, formatTime(r.RestoredAt), nullIfZero(r.EndedAt))
	if isUnique(err) {
		return SelfRestore{}, fmt.Errorf("self restore %q: %w", r.ID, ErrAlreadyExists)
	}
	if err != nil {
		return SelfRestore{}, fmt.Errorf("insert self restore %q: %w", r.ID, err)
	}
	return r, nil
}

// LatestSelfRestore returns the newest restore, and false when this store was never restored.
func (s *Store) LatestSelfRestore(ctx context.Context) (SelfRestore, bool, error) {
	r, err := scanSelfRestore(s.db.QueryRowContext(ctx, `SELECT `+selfRestoreColumns+` FROM self_restores ORDER BY restored_at DESC, id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return SelfRestore{}, false, nil
	}
	if err != nil {
		return SelfRestore{}, false, fmt.Errorf("read the latest self restore: %w", err)
	}
	return r, true, nil
}

// SetSelfRestoreStatus moves a restore to status; a final one sets its end to at. An unknown ID is ErrNotFound.
func (s *Store) SetSelfRestoreStatus(ctx context.Context, id, status string, at time.Time) error {
	var ended any
	if status == SelfRestoreDone {
		ended = formatTime(at)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE self_restores SET status = ?, ended_at = COALESCE(?, ended_at) WHERE id = ?`, status, ended, id)
	if err != nil {
		return fmt.Errorf("set self restore %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("self restore %q: %w", id, ErrNotFound)
	}
	return nil
}

// AddSelfRestoreItems appends items to a restore, numbering them after the ones it has (the Seq the caller
// gives is replaced). An unknown restore is ErrNotFound.
func (s *Store) AddSelfRestoreItems(ctx context.Context, restoreID string, items []SelfRestoreItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("add self restore items: %w", err)
	}
	defer tx.Rollback()
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) + 1 FROM self_restore_items WHERE restore_id = ?`, restoreID).Scan(&next); err != nil {
		return fmt.Errorf("add self restore items: %w", err)
	}
	for i, it := range items {
		status := it.Status
		if status == "" {
			status = RestoreItemWaiting
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO self_restore_items (restore_id, seq, kind, app, service, status, ref, reason, started_at, ended_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, restoreID, next+i, it.Kind, it.App, it.Service, status, it.Ref, it.Reason,
			nullIfZero(it.StartedAt), nullIfZero(it.EndedAt)); err != nil {
			if isForeignKey(err) {
				return fmt.Errorf("self restore %q: %w", restoreID, ErrNotFound)
			}
			return fmt.Errorf("add self restore items: %w", err)
		}
	}
	return tx.Commit()
}

// UpdateSelfRestoreItem sets an item's status, the record it made and its reason; a zero startedAt or endedAt
// leaves the stored time. An unknown item is ErrNotFound.
func (s *Store) UpdateSelfRestoreItem(ctx context.Context, restoreID string, seq int, status, ref, reason string, startedAt, endedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE self_restore_items SET status = ?, ref = ?, reason = ?,
		started_at = COALESCE(?, started_at), ended_at = COALESCE(?, ended_at) WHERE restore_id = ? AND seq = ?`,
		status, ref, reason, nullIfZero(startedAt), nullIfZero(endedAt), restoreID, seq)
	if err != nil {
		return fmt.Errorf("update self restore item %s/%d: %w", restoreID, seq, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("self restore item %s/%d: %w", restoreID, seq, ErrNotFound)
	}
	return nil
}

// ListSelfRestoreItems returns a restore's items in order.
func (s *Store) ListSelfRestoreItems(ctx context.Context, restoreID string) ([]SelfRestoreItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT restore_id, seq, kind, app, service, status, ref, reason, started_at, ended_at
		FROM self_restore_items WHERE restore_id = ? ORDER BY seq`, restoreID)
	if err != nil {
		return nil, fmt.Errorf("list self restore items: %w", err)
	}
	defer rows.Close()
	var out []SelfRestoreItem
	for rows.Next() {
		var it SelfRestoreItem
		var started, ended sql.NullString
		if err := rows.Scan(&it.RestoreID, &it.Seq, &it.Kind, &it.App, &it.Service, &it.Status, &it.Ref, &it.Reason, &started, &ended); err != nil {
			return nil, fmt.Errorf("list self restore items: %w", err)
		}
		if started.Valid {
			if it.StartedAt, err = parseTime(started.String); err != nil {
				return nil, err
			}
		}
		if ended.Valid {
			if it.EndedAt, err = parseTime(ended.String); err != nil {
				return nil, err
			}
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// DataDir is the folder the store lives in.
func (s *Store) DataDir() string { return s.dir }

// SchemaVersion is the newest migration the database has.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read the schema version: %w", err)
	}
	return int(v.Int64), nil
}

// Counts is how many apps and people the store holds, for a backup's manifest.
func (s *Store) Counts(ctx context.Context) (apps, people int, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM apps), (SELECT count(*) FROM people)`).Scan(&apps, &people); err != nil {
		return 0, 0, fmt.Errorf("count apps and people: %w", err)
	}
	return apps, people, nil
}

// SpaceError is CopyTo refusing for want of room: the copy needs about as much as the database, and Caravana
// wants twice that free so a backup never fills the disk the database lives on.
type SpaceError struct {
	Free, Need int64
}

func (e *SpaceError) Error() string {
	return fmt.Sprintf("the data folder has %d MB free and a backup of the database needs %d MB", e.Free>>20, e.Need>>20)
}

// CopyTo writes a consistent copy of the database to path with VACUUM INTO (the store keeps running)
// and checks it with PRAGMA quick_check. path must not exist; it is removed again when the copy or the check
// fails. It is refused with a *SpaceError when the folder holding path has less than twice the database's size
// free.
func (s *Store) CopyTo(ctx context.Context, path string) error {
	info, err := os.Stat(filepath.Join(s.dir, dbFile))
	if err != nil {
		return fmt.Errorf("copy the database: %w", err)
	}
	if free, ok := freeBytes(filepath.Dir(path)); ok && free < 2*info.Size() {
		return &SpaceError{Free: free, Need: 2 * info.Size()}
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("copy the database: %q already exists", path)
	}
	// A SQL string literal: quotes doubled. The path is Caravana's own (a temporary folder in the data folder).
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(path, "'", "''")+`'`); err != nil {
		os.Remove(path)
		return fmt.Errorf("copy the database: %w", err)
	}
	if err := quickCheck(ctx, path); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// quickCheck opens the database file at path read-only and runs PRAGMA quick_check.
func quickCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+dsnPath(path)+"?mode=ro&immutable=1")
	if err != nil {
		return fmt.Errorf("check the copy of the database: %w", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("check the copy of the database: %w", err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("check the copy of the database: %w", err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check the copy of the database: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("the copy of the database failed its check: %s", problems[0])
	}
	return nil
}

// QuickCheckFile runs PRAGMA quick_check on the SQLite file at path, for the restore: nil when it is sound.
func QuickCheckFile(ctx context.Context, path string) error { return quickCheck(ctx, path) }

// freeBytes is the space free to an ordinary user in the folder dir is on; ok is false when it can't be told.
func freeBytes(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
