package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Backup destinations (feature 052). A destination is an S3-compatible bucket
// with a name. Exactly one is the default while any exists. The secret key of a
// destination isn't here: SecretPath names the Nomad Variable that holds it.

// ErrDefaultDestination means the destination is the default, which can't be
// removed: another must be made the default first.
var ErrDefaultDestination = errors.New("is the default backup destination")

// DestinationTest is the outcome of the last check of a destination.
type DestinationTest struct {
	OK       bool
	Message  string
	TestedAt time.Time
}

// BackupDestination is a named backup destination without its secret key.
type BackupDestination struct {
	Name                                          string
	Endpoint, Region, Bucket, Prefix, AccessKeyID string
	// SecretPath is the Nomad Variable that holds the secret key.
	SecretPath string
	IsDefault  bool
	UpdatedAt  time.Time
	// Test is the last check, nil until one ran.
	Test *DestinationTest
}

// backupDestinationColumns is the column order scanBackupDestination reads.
const backupDestinationColumns = `name, endpoint, region, bucket, prefix, access_key_id, secret_path, is_default,
	updated_at, tested_at, test_ok, test_message`

func scanBackupDestination(row interface{ Scan(...any) error }) (BackupDestination, error) {
	var d BackupDestination
	var isDefault int
	var updated string
	var tested sql.NullString
	var ok sql.NullInt64
	var message string
	if err := row.Scan(&d.Name, &d.Endpoint, &d.Region, &d.Bucket, &d.Prefix, &d.AccessKeyID, &d.SecretPath, &isDefault,
		&updated, &tested, &ok, &message); err != nil {
		return BackupDestination{}, err
	}
	d.IsDefault = isDefault == 1
	var err error
	if d.UpdatedAt, err = parseTime(updated); err != nil {
		return BackupDestination{}, fmt.Errorf("backup destination %q: %w", d.Name, err)
	}
	if tested.Valid {
		at, err := parseTime(tested.String)
		if err != nil {
			return BackupDestination{}, fmt.Errorf("backup destination %q: %w", d.Name, err)
		}
		d.Test = &DestinationTest{OK: ok.Int64 == 1, Message: message, TestedAt: at}
	}
	return d, nil
}

// ListBackupDestinations returns every destination, ordered by name.
func (s *Store) ListBackupDestinations(ctx context.Context) ([]BackupDestination, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+backupDestinationColumns+" FROM backup_destinations ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list backup destinations: %w", err)
	}
	defer rows.Close()
	var out []BackupDestination
	for rows.Next() {
		d, err := scanBackupDestination(rows)
		if err != nil {
			return nil, fmt.Errorf("list backup destinations: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list backup destinations: %w", err)
	}
	return out, nil
}

// GetBackupDestinationNamed returns the destination called name, or ErrNotFound.
func (s *Store) GetBackupDestinationNamed(ctx context.Context, name string) (BackupDestination, error) {
	d, err := scanBackupDestination(s.db.QueryRowContext(ctx,
		"SELECT "+backupDestinationColumns+" FROM backup_destinations WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return BackupDestination{}, fmt.Errorf("backup destination %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return BackupDestination{}, fmt.Errorf("get backup destination %q: %w", name, err)
	}
	return d, nil
}

// GetBackupDestination returns the default destination, or ErrNotFound when
// none is set.
func (s *Store) GetBackupDestination(ctx context.Context) (BackupDestination, error) {
	d, err := scanBackupDestination(s.db.QueryRowContext(ctx,
		"SELECT "+backupDestinationColumns+" FROM backup_destinations WHERE is_default = 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return BackupDestination{}, fmt.Errorf("backup destination: %w", ErrNotFound)
	}
	if err != nil {
		return BackupDestination{}, fmt.Errorf("get backup destination: %w", err)
	}
	return d, nil
}

// SaveBackupDestination creates the destination or updates the one with the
// same name. The first destination there is becomes the default. An update
// changes the settings and forgets the last test; it never changes the default
// (SetDefaultBackupDestination does) or where the secret key is kept (the path
// is fixed when the destination is made).
func (s *Store) SaveBackupDestination(ctx context.Context, d BackupDestination) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save backup destination %q: %w", d.Name, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	// The first destination is the default: nothing is a default yet.
	hasDefault, err := exists(ctx, tx, "SELECT 1 FROM backup_destinations WHERE is_default = 1")
	if err != nil {
		return fmt.Errorf("save backup destination %q: %w", d.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO backup_destinations (name, endpoint, region, bucket, prefix, access_key_id, secret_path, is_default, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET endpoint = excluded.endpoint, region = excluded.region,
			bucket = excluded.bucket, prefix = excluded.prefix, access_key_id = excluded.access_key_id,
			updated_at = excluded.updated_at, tested_at = NULL, test_ok = NULL, test_message = ''`,
		d.Name, d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKeyID, d.SecretPath, boolInt(!hasDefault), formatTime(d.UpdatedAt)); err != nil {
		return fmt.Errorf("save backup destination %q: %w", d.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save backup destination %q: %w", d.Name, err)
	}
	return nil
}

// SetDefaultBackupDestination makes the destination called name the default,
// in one transaction. An unknown name is ErrNotFound.
func (s *Store) SetDefaultBackupDestination(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set the default backup destination %q: %w", name, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if ok, err := exists(ctx, tx, "SELECT 1 FROM backup_destinations WHERE name = ?", name); err != nil {
		return fmt.Errorf("set the default backup destination %q: %w", name, err)
	} else if !ok {
		return fmt.Errorf("backup destination %q: %w", name, ErrNotFound)
	}
	// The old default goes first: the unique index allows one at a time.
	if _, err := tx.ExecContext(ctx, "UPDATE backup_destinations SET is_default = 0 WHERE is_default = 1"); err != nil {
		return fmt.Errorf("set the default backup destination %q: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE backup_destinations SET is_default = 1 WHERE name = ?", name); err != nil {
		return fmt.Errorf("set the default backup destination %q: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set the default backup destination %q: %w", name, err)
	}
	return nil
}

// DeleteBackupDestination removes the destination called name. An unknown name
// is ErrNotFound and the default is ErrDefaultDestination. Whether anything
// still uses it is the service's question.
func (s *Store) DeleteBackupDestination(ctx context.Context, name string) error {
	d, err := s.GetBackupDestinationNamed(ctx, name)
	if err != nil {
		return err
	}
	if d.IsDefault {
		return fmt.Errorf("backup destination %q: %w", name, ErrDefaultDestination)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM backup_destinations WHERE name = ?", name); err != nil {
		return fmt.Errorf("delete backup destination %q: %w", name, err)
	}
	return nil
}

// SaveBackupDestinationTest keeps the outcome of a check of the destination. An
// unknown name is ErrNotFound.
func (s *Store) SaveBackupDestinationTest(ctx context.Context, name string, t DestinationTest) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE backup_destinations SET tested_at = ?, test_ok = ?, test_message = ? WHERE name = ?",
		formatTime(t.TestedAt), boolInt(t.OK), t.Message, name)
	if err != nil {
		return fmt.Errorf("save the test of backup destination %q: %w", name, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("save the test of backup destination %q: %w", name, err)
	} else if n == 0 {
		return fmt.Errorf("backup destination %q: %w", name, ErrNotFound)
	}
	return nil
}

// SetProjectBackupDestination sets the destination the project's backups go to
// when a policy names none. "" returns the project to the installation's
// default. An unknown project is ErrNotFound. Whether the destination exists is
// the service's question.
func (s *Store) SetProjectBackupDestination(ctx context.Context, project, name string) error {
	var value any // NULL, not "", for the installation's default
	if name != "" {
		value = name
	}
	res, err := s.db.ExecContext(ctx, "UPDATE projects SET backup_destination = ?, updated_at = ? WHERE id = ?",
		value, formatTime(s.now()), project)
	if err != nil {
		return fmt.Errorf("set the backup destination of project %q: %w", project, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("set the backup destination of project %q: %w", project, err)
	} else if n == 0 {
		return fmt.Errorf("project %q: %w", project, ErrNotFound)
	}
	return nil
}
