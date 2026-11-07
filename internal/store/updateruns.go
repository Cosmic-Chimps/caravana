package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Update runs (feature 030): one row per update of Caravana the owner, the API
// or the automatic setting asked for. The row says who asked, which version and
// how it went in the updater's one line; it never holds the installer's log.

// The states of an update run. requested and running are the open ones; every
// other state is final.
const (
	UpdateRequested  = "requested"
	UpdateRunning    = "running"
	UpdateUpdated    = "updated"
	UpdateRolledBack = "rolled_back"
	UpdateFailed     = "failed"
	UpdateRefused    = "refused"
	UpdateLost       = "lost"
)

// The triggers of an update run.
const (
	UpdateByConsole   = "console"
	UpdateByAPI       = "api"
	UpdateByAutomatic = "automatic"
)

// UpdateRunFinal says a state is a final one.
func UpdateRunFinal(state string) bool {
	return state != UpdateRequested && state != UpdateRunning
}

// UpdateRun is one stored update run. A zero StartedAt or FinishedAt is NULL:
// not known yet.
type UpdateRun struct {
	ID          string // 32 lowercase hex characters
	Version     string // 0.17.0
	FromVersion string
	Trigger     string // UpdateByConsole, UpdateByAPI or UpdateByAutomatic
	TokenName   string // the API token's name, for UpdateByAPI
	RequestedAt time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
	State       string
	Reason      string
	// Log is where the updater keeps the installer's output on the server (root only); empty
	// until the updater started the run.
	Log string
}

const updateRunColumns = `id, version, from_version, trigger, token_name, requested_at, started_at, finished_at, state, reason, log`

// InsertUpdateRun stores a new run. A zero RequestedAt is the store's clock and
// an empty State is "requested". An ID in use is ErrAlreadyExists.
func (s *Store) InsertUpdateRun(ctx context.Context, r UpdateRun) (UpdateRun, error) {
	if r.RequestedAt.IsZero() {
		r.RequestedAt = s.now()
	}
	if r.State == "" {
		r.State = UpdateRequested
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO update_runs (`+updateRunColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		r.ID, r.Version, r.FromVersion, r.Trigger, r.TokenName, formatTime(r.RequestedAt),
		nullIfZero(r.StartedAt), nullIfZero(r.FinishedAt), r.State, r.Reason, r.Log)
	if err != nil {
		return UpdateRun{}, fmt.Errorf("insert update run %q: %w", r.ID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return UpdateRun{}, fmt.Errorf("insert update run %q: %w", r.ID, err)
	} else if n == 0 {
		return UpdateRun{}, fmt.Errorf("update run %q: %w", r.ID, ErrAlreadyExists)
	}
	return r, nil
}

// OpenUpdateRun returns the run that is requested or running, and false when
// there is none. There is at most one: the service's mutex sees to it.
func (s *Store) OpenUpdateRun(ctx context.Context) (UpdateRun, bool, error) {
	r, err := scanUpdateRun(s.db.QueryRowContext(ctx, `SELECT `+updateRunColumns+` FROM update_runs
		WHERE state IN ('requested', 'running') ORDER BY requested_at DESC, id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return UpdateRun{}, false, nil
	}
	if err != nil {
		return UpdateRun{}, false, fmt.Errorf("read open update run: %w", err)
	}
	return r, true, nil
}

// UpdateRuns returns up to limit runs, newest requested first.
func (s *Store) UpdateRuns(ctx context.Context, limit int) ([]UpdateRun, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list update runs: limit %d, want at least 1", limit)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+updateRunColumns+` FROM update_runs
		ORDER BY requested_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list update runs: %w", err)
	}
	defer rows.Close()
	var out []UpdateRun
	for rows.Next() {
		r, err := scanUpdateRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list update runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list update runs: %w", err)
	}
	return out, nil
}

// LatestFinishedUpdateRun returns the run that finished last, and false when
// none has.
func (s *Store) LatestFinishedUpdateRun(ctx context.Context) (UpdateRun, bool, error) {
	r, err := scanUpdateRun(s.db.QueryRowContext(ctx, `SELECT `+updateRunColumns+` FROM update_runs
		WHERE state NOT IN ('requested', 'running') ORDER BY COALESCE(finished_at, requested_at) DESC, id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return UpdateRun{}, false, nil
	}
	if err != nil {
		return UpdateRun{}, false, fmt.Errorf("read latest update run: %w", err)
	}
	return r, true, nil
}

// UpdateRunExists says some run, in any state, was ever made for the version.
func (s *Store) UpdateRunExists(ctx context.Context, version string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM update_runs WHERE version = ?", version).Scan(&n); err != nil {
		return false, fmt.Errorf("read update runs of %q: %w", version, err)
	}
	return n > 0, nil
}

// SetUpdateRunState moves a run to state and sets its reason and log path. A
// zero startedAt or finishedAt leaves the stored time as it is. An unknown ID is
// ErrNotFound.
func (s *Store) SetUpdateRunState(ctx context.Context, id, state string, startedAt, finishedAt time.Time, reason, log string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE update_runs SET state = ?, reason = ?, log = ?,
		started_at = COALESCE(?, started_at), finished_at = COALESCE(?, finished_at) WHERE id = ?`,
		state, reason, log, nullIfZero(startedAt), nullIfZero(finishedAt), id)
	if err != nil {
		return fmt.Errorf("set update run %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("set update run %q: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteUpdateRun removes a run: the one whose request file couldn't be
// written. An unknown ID is not an error.
func (s *Store) DeleteUpdateRun(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM update_runs WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete update run %q: %w", id, err)
	}
	return nil
}

// SetAutoUpdate stores the owner's automatic-update setting: on or off, and the
// UTC hour (0-23) the request is made at. It touches nothing else of the release
// row. An hour outside 0-23 is refused by the table.
func (s *Store) SetAutoUpdate(ctx context.Context, enabled bool, hour int) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE release_check SET auto_update = ?, auto_update_hour = ? WHERE id = 1",
		boolInt(enabled), hour); err != nil {
		return fmt.Errorf("set automatic update: %w", err)
	}
	return nil
}

// scanUpdateRun reads one row of updateRunColumns.
func scanUpdateRun(row interface{ Scan(...any) error }) (UpdateRun, error) {
	var r UpdateRun
	var requested string
	var started, finished sql.NullString
	if err := row.Scan(&r.ID, &r.Version, &r.FromVersion, &r.Trigger, &r.TokenName, &requested, &started, &finished,
		&r.State, &r.Reason, &r.Log); err != nil {
		return UpdateRun{}, err
	}
	var err error
	if r.RequestedAt, err = parseTime(requested); err != nil {
		return UpdateRun{}, err
	}
	if started.Valid {
		if r.StartedAt, err = parseTime(started.String); err != nil {
			return UpdateRun{}, err
		}
	}
	if finished.Valid {
		if r.FinishedAt, err = parseTime(finished.String); err != nil {
			return UpdateRun{}, err
		}
	}
	return r, nil
}

// nullIfZero is the stored text of t, or NULL for the zero time.
func nullIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}
