package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reclaiming space (feature 045): the owner's clean-up settings, the clean-ups of a server's unused
// images and the deletions of orphan volumes. A row holds counts, names and Caravana's own sentences;
// never an app's output or a secret.

// ErrCleanupOpen is returned when a server already has a clean-up that is requested or running.
var ErrCleanupOpen = errors.New("a clean-up is already open for this server")

// The states of an image clean-up. requested and running are the open ones.
const (
	CleanupRequested = "requested" // written, not handed to the agent yet
	CleanupRunning   = "running"   // handed to the agent; its outcome isn't reported yet
	CleanupDone      = "done"
	CleanupFailed    = "failed"
	CleanupLost      = "lost" // no outcome ever came
)

// CleanupOpen says a clean-up state is an open one.
func CleanupOpen(status string) bool { return status == CleanupRequested || status == CleanupRunning }

// The triggers of an image clean-up.
const (
	CleanupByOwner    = "owner"
	CleanupBySchedule = "schedule"
)

// The states of an orphan volume's deletion. archiving and deleting are the open ones.
const (
	DeletionArchiving = "archiving"
	DeletionDeleting  = "deleting"
	DeletionDone      = "done"
	DeletionFailed    = "failed"
)

// CleanupSettings is the owner's one row of settings.
type CleanupSettings struct {
	Automatic       bool
	KeepDeployments int // 1 to 10
	// LastAutomaticDay is the UTC date (YYYY-MM-DD) the daily run last made its requests on; "" before.
	LastAutomaticDay string
}

// CleanupSettings reads the settings. A store the migration made has the row, with the daily run on
// and three deployments kept.
func (s *Store) CleanupSettings(ctx context.Context) (CleanupSettings, error) {
	var c CleanupSettings
	var automatic int
	err := s.db.QueryRowContext(ctx, `SELECT automatic, keep_deployments, last_automatic_day FROM cleanup_settings WHERE id = 1`).
		Scan(&automatic, &c.KeepDeployments, &c.LastAutomaticDay)
	if err != nil {
		return CleanupSettings{}, fmt.Errorf("read cleanup settings: %w", err)
	}
	c.Automatic = automatic != 0
	return c, nil
}

// SaveCleanupSettings stores whether the daily run is on and how many deployments keep their images.
// It leaves the day of the last daily run alone. A number outside 1 to 10 is refused by the table.
func (s *Store) SaveCleanupSettings(ctx context.Context, automatic bool, keepDeployments int) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE cleanup_settings SET automatic = ?, keep_deployments = ? WHERE id = 1`,
		boolInt(automatic), keepDeployments); err != nil {
		return fmt.Errorf("save cleanup settings: %w", err)
	}
	return nil
}

// SetLastAutomaticDay records the UTC date (YYYY-MM-DD) the daily run made its requests on.
func (s *Store) SetLastAutomaticDay(ctx context.Context, day string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE cleanup_settings SET last_automatic_day = ? WHERE id = 1`, day); err != nil {
		return fmt.Errorf("set the last automatic clean-up day: %w", err)
	}
	return nil
}

// ImageCleanup is one stored clean-up of a server's unused images. A zero StartedAt or FinishedAt is
// NULL: not known yet.
type ImageCleanup struct {
	ID             string // 32 lowercase hex characters
	ServerIdx      int
	ServerName     string
	Trigger        string // CleanupByOwner or CleanupBySchedule
	DockerVolumes  bool
	RequestedAt    time.Time
	StartedAt      time.Time
	FinishedAt     time.Time
	Status         string
	ImagesRemoved  int
	BytesFreed     int64
	VolumesRemoved int
	Error          string
}

const imageCleanupColumns = `id, server_idx, server_name, trigger, docker_volumes, requested_at, started_at, finished_at,
	status, images_removed, bytes_freed, volumes_removed, error`

func scanImageCleanup(row interface{ Scan(...any) error }) (ImageCleanup, error) {
	var c ImageCleanup
	var volumes int
	var requested string
	var started, finished sql.NullString
	if err := row.Scan(&c.ID, &c.ServerIdx, &c.ServerName, &c.Trigger, &volumes, &requested, &started, &finished,
		&c.Status, &c.ImagesRemoved, &c.BytesFreed, &c.VolumesRemoved, &c.Error); err != nil {
		return ImageCleanup{}, err
	}
	c.DockerVolumes = volumes != 0
	var err error
	if c.RequestedAt, err = parseTime(requested); err != nil {
		return ImageCleanup{}, err
	}
	if started.Valid {
		if c.StartedAt, err = parseTime(started.String); err != nil {
			return ImageCleanup{}, err
		}
	}
	if finished.Valid {
		if c.FinishedAt, err = parseTime(finished.String); err != nil {
			return ImageCleanup{}, err
		}
	}
	return c, nil
}

// CreateImageCleanup stores a new clean-up. A zero RequestedAt is the store's clock and an empty Status
// is "requested". An ID in use is ErrAlreadyExists; a server that has an open clean-up is
// ErrCleanupOpen; a server that doesn't exist is ErrNotFound.
func (s *Store) CreateImageCleanup(ctx context.Context, c ImageCleanup) (ImageCleanup, error) {
	if c.RequestedAt.IsZero() {
		c.RequestedAt = s.now()
	}
	if c.Status == "" {
		c.Status = CleanupRequested
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO image_cleanups (`+imageCleanupColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ServerIdx, c.ServerName, c.Trigger, boolInt(c.DockerVolumes), formatTime(c.RequestedAt),
		nullIfZero(c.StartedAt), nullIfZero(c.FinishedAt), c.Status, c.ImagesRemoved, c.BytesFreed, c.VolumesRemoved, c.Error)
	switch {
	case isUnique(err) && strings.Contains(err.Error(), "server_idx"):
		return ImageCleanup{}, fmt.Errorf("clean-up of server %d: %w", c.ServerIdx, ErrCleanupOpen)
	case isUnique(err):
		return ImageCleanup{}, fmt.Errorf("clean-up %q: %w", c.ID, ErrAlreadyExists)
	case isForeignKey(err):
		return ImageCleanup{}, fmt.Errorf("create clean-up: server %d: %w", c.ServerIdx, ErrNotFound)
	case err != nil:
		return ImageCleanup{}, fmt.Errorf("create clean-up %q: %w", c.ID, err)
	}
	return c, nil
}

// OpenImageCleanup returns the server's clean-up that is requested or running, and false when there is
// none. There is at most one.
func (s *Store) OpenImageCleanup(ctx context.Context, serverIdx int) (ImageCleanup, bool, error) {
	c, err := scanImageCleanup(s.db.QueryRowContext(ctx, `SELECT `+imageCleanupColumns+` FROM image_cleanups
		WHERE server_idx = ? AND status IN ('requested', 'running') LIMIT 1`, serverIdx))
	if errors.Is(err, sql.ErrNoRows) {
		return ImageCleanup{}, false, nil
	}
	if err != nil {
		return ImageCleanup{}, false, fmt.Errorf("read the open clean-up of server %d: %w", serverIdx, err)
	}
	return c, true, nil
}

// OpenImageCleanups returns every clean-up that is requested or running, oldest first: the pass looks
// for the ones that were never picked up or never reported.
func (s *Store) OpenImageCleanups(ctx context.Context) ([]ImageCleanup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+imageCleanupColumns+` FROM image_cleanups
		WHERE status IN ('requested', 'running') ORDER BY requested_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list open clean-ups: %w", err)
	}
	defer rows.Close()
	var out []ImageCleanup
	for rows.Next() {
		c, err := scanImageCleanup(rows)
		if err != nil {
			return nil, fmt.Errorf("list open clean-ups: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list open clean-ups: %w", err)
	}
	return out, nil
}

// ListImageCleanups returns up to limit of a server's clean-ups, newest first.
func (s *Store) ListImageCleanups(ctx context.Context, serverIdx, limit int) ([]ImageCleanup, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list clean-ups: limit %d, want at least 1", limit)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+imageCleanupColumns+` FROM image_cleanups
		WHERE server_idx = ? ORDER BY requested_at DESC, id DESC LIMIT ?`, serverIdx, limit)
	if err != nil {
		return nil, fmt.Errorf("list clean-ups of server %d: %w", serverIdx, err)
	}
	defer rows.Close()
	var out []ImageCleanup
	for rows.Next() {
		c, err := scanImageCleanup(rows)
		if err != nil {
			return nil, fmt.Errorf("list clean-ups of server %d: %w", serverIdx, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list clean-ups of server %d: %w", serverIdx, err)
	}
	return out, nil
}

// GetImageCleanup returns one clean-up, or ErrNotFound.
func (s *Store) GetImageCleanup(ctx context.Context, id string) (ImageCleanup, error) {
	c, err := scanImageCleanup(s.db.QueryRowContext(ctx, `SELECT `+imageCleanupColumns+` FROM image_cleanups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ImageCleanup{}, fmt.Errorf("clean-up %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return ImageCleanup{}, fmt.Errorf("read clean-up %q: %w", id, err)
	}
	return c, nil
}

// StartImageCleanup marks a requested clean-up as running from at: it was handed to the agent. A
// clean-up that isn't requested (already running, or settled) is left as it is, so a second sync
// doesn't move the start; an unknown ID is ErrNotFound.
func (s *Store) StartImageCleanup(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE image_cleanups SET status = 'running', started_at = ? WHERE id = ? AND status = 'requested'`,
		formatTime(at), id)
	if err != nil {
		return fmt.Errorf("start clean-up %q: %w", id, err)
	}
	return s.requireCleanup(ctx, res, id)
}

// SettleImageCleanup ends an open clean-up: its final status (done, failed or lost), when it ended and
// what it did. A clean-up that is already settled is left as it is, so the agent's report (repeated by
// every sync until a newer clean-up replaces it) never changes it again; an unknown ID is ErrNotFound.
func (s *Store) SettleImageCleanup(ctx context.Context, id, status string, at time.Time, imagesRemoved int, bytesFreed int64, volumesRemoved int, errText string) error {
	if CleanupOpen(status) {
		return fmt.Errorf("settle clean-up %q: %q is not a final status", id, status)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE image_cleanups SET status = ?, finished_at = ?, images_removed = ?, bytes_freed = ?,
		volumes_removed = ?, error = ? WHERE id = ? AND status IN ('requested', 'running')`,
		status, formatTime(at), imagesRemoved, bytesFreed, volumesRemoved, errText, id)
	if err != nil {
		return fmt.Errorf("settle clean-up %q: %w", id, err)
	}
	return s.requireCleanup(ctx, res, id)
}

// requireCleanup says an update touched nothing: a clean-up that doesn't exist is ErrNotFound, one
// that exists but wasn't in the state the update needed is no error.
func (s *Store) requireCleanup(ctx context.Context, res sql.Result, id string) error {
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM image_cleanups WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("clean-up %q: %w", id, ErrNotFound)
	}
	return err
}

// VolumeDeletion is one stored deletion of an orphan volume. A zero FinishedAt is NULL.
type VolumeDeletion struct {
	ID            string // "vd_" and 26 lowercase base32 characters
	Volume        string // Nomad's name, <app>_<volume>
	AppID         string // the app the name reads as, "" when it doesn't read as one; it may not exist any more
	ServerName    string
	BackUpFirst   bool
	Trigger       string // "owner" when empty
	RequestedAt   time.Time
	FinishedAt    time.Time
	Status        string
	ArchiveObject string // where the archive is in the backup destination, "" for none
	Bytes         int64  // the size last measured, 0 when unknown
	Error         string
}

const volumeDeletionColumns = `id, volume, app_id, server_name, back_up_first, trigger, requested_at, finished_at,
	status, archive_object, bytes, error`

func scanVolumeDeletion(row interface{ Scan(...any) error }) (VolumeDeletion, error) {
	var d VolumeDeletion
	var backUp int
	var requested string
	var finished sql.NullString
	if err := row.Scan(&d.ID, &d.Volume, &d.AppID, &d.ServerName, &backUp, &d.Trigger, &requested, &finished,
		&d.Status, &d.ArchiveObject, &d.Bytes, &d.Error); err != nil {
		return VolumeDeletion{}, err
	}
	d.BackUpFirst = backUp != 0
	var err error
	if d.RequestedAt, err = parseTime(requested); err != nil {
		return VolumeDeletion{}, err
	}
	if finished.Valid {
		if d.FinishedAt, err = parseTime(finished.String); err != nil {
			return VolumeDeletion{}, err
		}
	}
	return d, nil
}

// CreateVolumeDeletion stores a new deletion. A zero RequestedAt is the store's clock and an empty
// Trigger is "owner". An ID in use is ErrAlreadyExists.
func (s *Store) CreateVolumeDeletion(ctx context.Context, d VolumeDeletion) (VolumeDeletion, error) {
	if d.RequestedAt.IsZero() {
		d.RequestedAt = s.now()
	}
	if d.Trigger == "" {
		d.Trigger = CleanupByOwner
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO volume_deletions (`+volumeDeletionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Volume, d.AppID, d.ServerName, boolInt(d.BackUpFirst), d.Trigger, formatTime(d.RequestedAt),
		nullIfZero(d.FinishedAt), d.Status, d.ArchiveObject, d.Bytes, d.Error)
	switch {
	case isUnique(err):
		return VolumeDeletion{}, fmt.Errorf("volume deletion %q: %w", d.ID, ErrAlreadyExists)
	case err != nil:
		return VolumeDeletion{}, fmt.Errorf("create volume deletion %q: %w", d.ID, err)
	}
	return d, nil
}

// GetVolumeDeletion returns one deletion, or ErrNotFound.
func (s *Store) GetVolumeDeletion(ctx context.Context, id string) (VolumeDeletion, error) {
	d, err := scanVolumeDeletion(s.db.QueryRowContext(ctx, `SELECT `+volumeDeletionColumns+` FROM volume_deletions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return VolumeDeletion{}, fmt.Errorf("volume deletion %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return VolumeDeletion{}, fmt.Errorf("read volume deletion %q: %w", id, err)
	}
	return d, nil
}

// OpenVolumeDeletions returns every deletion that is archiving or deleting, oldest first: Caravana
// settles them as failed at start, since nobody is left to finish them.
func (s *Store) OpenVolumeDeletions(ctx context.Context) ([]VolumeDeletion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+volumeDeletionColumns+` FROM volume_deletions
		WHERE status IN ('archiving', 'deleting') ORDER BY requested_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list open volume deletions: %w", err)
	}
	defer rows.Close()
	var out []VolumeDeletion
	for rows.Next() {
		d, err := scanVolumeDeletion(rows)
		if err != nil {
			return nil, fmt.Errorf("list open volume deletions: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list open volume deletions: %w", err)
	}
	return out, nil
}

// SetVolumeDeletionDeleting moves an archiving deletion to deleting (the archive is made, the volume is
// next) and records where the archive is. A deletion that isn't archiving is ErrNotFound.
func (s *Store) SetVolumeDeletionDeleting(ctx context.Context, id, archiveObject string, bytes int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE volume_deletions SET status = 'deleting', archive_object = ?, bytes = ?
		WHERE id = ? AND status = 'archiving'`, archiveObject, bytes, id)
	return oneDeletion(res, err, "advance volume deletion", id)
}

// SettleVolumeDeletion ends an open deletion: done or failed, when, and the sentence for a failure.
// A deletion that isn't open is ErrNotFound, so a settled one never changes again.
func (s *Store) SettleVolumeDeletion(ctx context.Context, id, status string, at time.Time, errText string) error {
	if status != DeletionDone && status != DeletionFailed {
		return fmt.Errorf("settle volume deletion %q: %q is not a final status", id, status)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE volume_deletions SET status = ?, finished_at = ?, error = ?
		WHERE id = ? AND status IN ('archiving', 'deleting')`, status, formatTime(at), errText, id)
	return oneDeletion(res, err, "settle volume deletion", id)
}

func oneDeletion(res sql.Result, err error, what, id string) error {
	if err != nil {
		return fmt.Errorf("%s %q: %w", what, id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("%s %q: %w", what, id, err)
	} else if n == 0 {
		return fmt.Errorf("%s %q: %w", what, id, ErrNotFound)
	}
	return nil
}

// ListHealthyDeployments returns an app's newest healthy deployments that recorded their definition (the
// ones a rollback can deploy again), newest first, at most limit of them. An unknown app gives an empty
// list. The clean-up keeps these deployments' images.
func (s *Store) ListHealthyDeployments(ctx context.Context, appID string, limit int) ([]DeploymentRecord, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list healthy deployments of %q: limit %d, want at least 1", appID, limit)
	}
	out, err := s.queryDeployments(ctx,
		"SELECT "+deploymentColumns+" FROM "+deploymentSource+
			" WHERE app_id = ? AND status = ? AND definition IS NOT NULL ORDER BY created_at DESC, seq DESC LIMIT ?",
		appID, string(StatusHealthy), limit)
	if err != nil {
		return nil, fmt.Errorf("list healthy deployments of %q: %w", appID, err)
	}
	return out, nil
}
