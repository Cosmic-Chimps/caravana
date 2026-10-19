package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Actions (feature 020.5): what the owner asked of an app besides deploying
// it. A record says what, when, how it was started and how it ended. It never
// holds command output: for a run, Program is the command's first word and
// nothing else, ExitCode is its status, and Error is a short reason Caravana
// wrote ("timed out"), never text the command printed.

// The actions a record can be of.
const (
	ActionStop    = "stop"
	ActionStart   = "start"
	ActionRestart = "restart"
	ActionRun     = "run"
	// ActionTerminal is an interactive shell in a copy (feature 020.6). Its
	// record holds the shell asked for, its exit code and a fixed end reason,
	// never what was typed or printed.
	ActionTerminal = "terminal"
)

// The statuses of an action. running is the only one that changes.
const (
	ActionRunning   = "running"
	ActionSucceeded = "succeeded"
	ActionFailed    = "failed"
)

// ActionRecord is one action on an app.
type ActionRecord struct {
	ID      string // "act_" and 12 random hex digits
	AppID   string
	Action  string // ActionStop, ActionStart, ActionRestart, ActionRun or ActionTerminal
	Trigger string // "console" or "manual", as deployments name them
	Status  string
	// CopyID and Service say where a run happened; empty for the others.
	CopyID, Service string
	// Program is a run's first word, nothing else.
	Program string
	// ExitCode is a run's exit status; nil when it timed out or didn't start.
	ExitCode *int
	// Error is why the action failed, in Caravana's words.
	Error      string
	StartedAt  time.Time
	FinishedAt *time.Time // nil while running
}

const actionColumns = "id, app_id, action, trigger, status, copy_id, service, program, exit_code, error, started_at, finished_at"

// SetAppStopped marks the app stopped at the given time, or not stopped (nil).
// It does not touch the app's document, so the revision stays. An unknown ID
// is ErrNotFound.
func (s *Store) SetAppStopped(ctx context.Context, appID string, at *time.Time) error {
	res, err := s.db.ExecContext(ctx, "UPDATE apps SET stopped_at = ? WHERE id = ?", optionalTime(at), appID)
	if err != nil {
		return fmt.Errorf("set app %q stopped: %w", appID, err)
	}
	return affectedOrNotFound(res, "app "+appID)
}

// InsertAction stores an action as given: running when it starts, or with its
// final status when it is recorded after the fact. The service only records
// actions of apps it has.
func (s *Store) InsertAction(ctx context.Context, a ActionRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_actions (`+actionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.AppID, a.Action, a.Trigger, a.Status, a.CopyID, a.Service, a.Program,
		optionalInt(a.ExitCode), a.Error, formatTime(a.StartedAt), optionalTime(a.FinishedAt))
	if err != nil {
		return fmt.Errorf("insert action %q: %w", a.ID, err)
	}
	return nil
}

// FinishAction ends an action: status is ActionSucceeded or ActionFailed,
// exitCode is a run's status (nil for none) and errMsg says why it failed
// (empty on success). An unknown ID is ErrNotFound.
func (s *Store) FinishAction(ctx context.Context, id, status string, exitCode *int, errMsg string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE app_actions SET status = ?, exit_code = ?, error = ?, finished_at = ? WHERE id = ?",
		status, optionalInt(exitCode), errMsg, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("finish action %q: %w", id, err)
	}
	return affectedOrNotFound(res, "action "+id)
}

// ListActions returns the newest limit actions of the app, newest first.
func (s *Store) ListActions(ctx context.Context, appID string, limit int) ([]ActionRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+actionColumns+
		" FROM app_actions WHERE app_id = ? ORDER BY started_at DESC, id DESC LIMIT ?", appID, limit)
	if err != nil {
		return nil, fmt.Errorf("list actions of app %q: %w", appID, err)
	}
	defer rows.Close()

	out := []ActionRecord{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, fmt.Errorf("list actions of app %q: %w", appID, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list actions of app %q: %w", appID, err)
	}
	return out, nil
}

// FailRunningActions marks every action still running as failed with msg, and
// returns how many it changed. The service calls it at start: a restart that
// was running when Caravana stopped has no one left to finish it.
func (s *Store) FailRunningActions(ctx context.Context, msg string, at time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		"UPDATE app_actions SET status = 'failed', error = ?, finished_at = ? WHERE status = 'running'",
		msg, formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("fail running actions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("fail running actions: %w", err)
	}
	return int(n), nil
}

func scanAction(row scanner) (ActionRecord, error) {
	var (
		a        ActionRecord
		exit     sql.NullInt64
		started  string
		finished sql.NullString
	)
	if err := row.Scan(&a.ID, &a.AppID, &a.Action, &a.Trigger, &a.Status, &a.CopyID, &a.Service,
		&a.Program, &exit, &a.Error, &started, &finished); err != nil {
		return ActionRecord{}, err
	}
	if exit.Valid {
		a.ExitCode = new(int(exit.Int64))
	}
	var err error
	if a.StartedAt, err = parseTime(started); err != nil {
		return ActionRecord{}, fmt.Errorf("action %q: %w", a.ID, err)
	}
	if a.FinishedAt, err = parseOptionalTime(finished); err != nil {
		return ActionRecord{}, fmt.Errorf("action %q: %w", a.ID, err)
	}
	return a, nil
}

// optionalInt is an int as stored: NULL for nil.
func optionalInt(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}
