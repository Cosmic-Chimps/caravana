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

// Schedules (feature 022): a command on a timer in a fresh copy of an app,
// and the record of each run. The built-in schedules (a service's backup
// policy) are not rows here: the service derives them.

// The statuses of a run. starting and running are the open ones; every other
// status is final.
const (
	RunStarting       = "starting"
	RunRunning        = "running"
	RunOK             = "ok"
	RunFailed         = "failed"
	RunStoppedAtLimit = "stopped_at_limit"
	RunStopped        = "stopped"
	RunSkipped        = "skipped"
	RunCouldNotStart  = "could_not_start"
)

// ScheduleRecord is one stored schedule.
type ScheduleRecord struct {
	ID, AppID, Service, Name string
	Command                  []string
	// Cron is five fields, read as UTC.
	Cron          string
	StopAfter     time.Duration
	MemoryMB      int
	SkipIfRunning bool
	// PausedAt is nil when the schedule is not paused.
	PausedAt *time.Time
	// ArmedAt is the clock's base: when the schedule was created, resumed or
	// its cron changed.
	ArmedAt   time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

const scheduleColumns = `id, app_id, service, name, command, cron, stop_after_s, memory_mb, skip_if_running,
	paused_at, armed_at, created_at, updated_at`

// InsertSchedule stores a new schedule. The caller sets the ID; a zero
// CreatedAt or ArmedAt is the store's clock. A name the app already uses (or
// an ID in use) is ErrAlreadyExists; an unknown app fails the foreign key.
func (s *Store) InsertSchedule(ctx context.Context, r ScheduleRecord) (ScheduleRecord, error) {
	command, err := json.Marshal(r.Command)
	if err != nil {
		return ScheduleRecord{}, fmt.Errorf("insert schedule %q: %w", r.Name, err)
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.now()
	}
	if r.ArmedAt.IsZero() {
		r.ArmedAt = r.CreatedAt
	}
	r.UpdatedAt = r.CreatedAt
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO schedules (`+scheduleColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		r.ID, r.AppID, r.Service, r.Name, string(command), r.Cron, int(r.StopAfter/time.Second), r.MemoryMB,
		boolInt(r.SkipIfRunning), optionalTime(r.PausedAt), formatTime(r.ArmedAt),
		formatTime(r.CreatedAt), formatTime(r.UpdatedAt))
	if err != nil {
		return ScheduleRecord{}, fmt.Errorf("insert schedule %q of %q: %w", r.Name, r.AppID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return ScheduleRecord{}, fmt.Errorf("insert schedule %q of %q: %w", r.Name, r.AppID, err)
	} else if n == 0 {
		return ScheduleRecord{}, fmt.Errorf("schedule %q of %q: %w", r.Name, r.AppID, ErrAlreadyExists)
	}
	return s.GetSchedule(ctx, r.ID)
}

// UpdateSchedule rewrites what an owner can edit: name, service, command,
// cron, stop-after, memory and the overlap rule. It leaves the pause and the
// clock's base alone (SetSchedulePaused and ArmSchedule change those) and
// sets UpdatedAt. An unknown ID is ErrNotFound; a name another schedule of the
// app has is ErrAlreadyExists.
func (s *Store) UpdateSchedule(ctx context.Context, r ScheduleRecord) (ScheduleRecord, error) {
	command, err := json.Marshal(r.Command)
	if err != nil {
		return ScheduleRecord{}, fmt.Errorf("update schedule %q: %w", r.ID, err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE schedules SET service = ?, name = ?, command = ?, cron = ?, stop_after_s = ?, memory_mb = ?,
			skip_if_running = ?, updated_at = ?
		WHERE id = ?`,
		r.Service, r.Name, string(command), r.Cron, int(r.StopAfter/time.Second), r.MemoryMB,
		boolInt(r.SkipIfRunning), formatTime(s.now()), r.ID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ScheduleRecord{}, fmt.Errorf("schedule %q: %w", r.Name, ErrAlreadyExists)
		}
		return ScheduleRecord{}, fmt.Errorf("update schedule %q: %w", r.ID, err)
	}
	if err := affectedOrNotFound(res, "schedule "+r.ID); err != nil {
		return ScheduleRecord{}, err
	}
	return s.GetSchedule(ctx, r.ID)
}

// GetSchedule returns the schedule with this ID, or ErrNotFound.
func (s *Store) GetSchedule(ctx context.Context, id string) (ScheduleRecord, error) {
	r, err := scanSchedule(s.db.QueryRowContext(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return ScheduleRecord{}, fmt.Errorf("schedule %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return ScheduleRecord{}, fmt.Errorf("get schedule %q: %w", id, err)
	}
	return r, nil
}

// ListSchedules returns the schedules of one app, or of every app when appID
// is empty, ordered by app and name.
func (s *Store) ListSchedules(ctx context.Context, appID string) ([]ScheduleRecord, error) {
	q := "SELECT " + scheduleColumns + " FROM schedules"
	var args []any
	if appID != "" {
		q += " WHERE app_id = ?"
		args = append(args, appID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY app_id, name", args...)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	var out []ScheduleRecord
	for rows.Next() {
		r, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("list schedules: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	return out, nil
}

// DeleteSchedule removes a schedule and, with it, its runs. An unknown ID is
// ErrNotFound.
func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete schedule %q: %w", id, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	// The foreign key cascades, but saying it keeps the delete right even
	// where foreign keys are off.
	if _, err := tx.ExecContext(ctx, "DELETE FROM schedule_runs WHERE schedule_id = ?", id); err != nil {
		return fmt.Errorf("delete schedule %q: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM schedules WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete schedule %q: %w", id, err)
	}
	if err := affectedOrNotFound(res, "schedule "+id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete schedule %q: %w", id, err)
	}
	return nil
}

// SetSchedulePaused pauses the schedule at the given time, or resumes it
// (nil). An unknown ID is ErrNotFound.
func (s *Store) SetSchedulePaused(ctx context.Context, id string, at *time.Time) error {
	res, err := s.db.ExecContext(ctx, "UPDATE schedules SET paused_at = ?, updated_at = ? WHERE id = ?",
		optionalTime(at), formatTime(s.now()), id)
	if err != nil {
		return fmt.Errorf("set schedule %q paused: %w", id, err)
	}
	return affectedOrNotFound(res, "schedule "+id)
}

// ArmSchedule sets the clock's base: slots before it are not owed. An unknown
// ID is ErrNotFound.
func (s *Store) ArmSchedule(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, "UPDATE schedules SET armed_at = ?, updated_at = ? WHERE id = ?",
		formatTime(at), formatTime(s.now()), id)
	if err != nil {
		return fmt.Errorf("arm schedule %q: %w", id, err)
	}
	return affectedOrNotFound(res, "schedule "+id)
}

func scanSchedule(row scanner) (ScheduleRecord, error) {
	var (
		r                       ScheduleRecord
		command                 string
		stopAfter, skip         int
		paused                  sql.NullString
		armed, created, updated string
	)
	if err := row.Scan(&r.ID, &r.AppID, &r.Service, &r.Name, &command, &r.Cron, &stopAfter, &r.MemoryMB, &skip,
		&paused, &armed, &created, &updated); err != nil {
		return ScheduleRecord{}, err
	}
	if err := json.Unmarshal([]byte(command), &r.Command); err != nil {
		return ScheduleRecord{}, fmt.Errorf("schedule %q: bad stored command: %w", r.ID, err)
	}
	r.StopAfter = time.Duration(stopAfter) * time.Second
	r.SkipIfRunning = skip == 1
	var err error
	if r.PausedAt, err = parseOptionalTime(paused); err != nil {
		return ScheduleRecord{}, fmt.Errorf("schedule %q: %w", r.ID, err)
	}
	if r.ArmedAt, err = parseTime(armed); err != nil {
		return ScheduleRecord{}, fmt.Errorf("schedule %q: %w", r.ID, err)
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return ScheduleRecord{}, fmt.Errorf("schedule %q: %w", r.ID, err)
	}
	if r.UpdatedAt, err = parseTime(updated); err != nil {
		return ScheduleRecord{}, fmt.Errorf("schedule %q: %w", r.ID, err)
	}
	return r, nil
}

// ScheduleRunRecord is one run of a schedule, or one slot that was skipped.
// The lists (ListRuns, OpenRuns, AllOpenRuns, LatestScheduleRuns) leave
// Stdout and Stderr empty, since each can be 64 KiB; GetRun returns them.
// StdoutBytes and StderrBytes are always read, so a list can say how much
// output a run kept without carrying it.
type ScheduleRunRecord struct {
	ID, ScheduleID, AppID, Service string
	Trigger                        string // "timer" or "owner"
	// SlotAt is the cron instant the run answers; nil for an owner run.
	SlotAt  *time.Time
	Command []string
	// DeploymentID is the healthy deployment whose digest ran, with its Nomad
	// job version; empty and nil when the run never got that far.
	DeploymentID string
	JobVersion   *int
	Status       string
	ExitCode     *int
	// Error is why a run failed or was skipped, in Caravana's words.
	Error string
	// StopReason is "owner", "limit", "app_stopped" or "schedule_removed",
	// written before the job is deregistered.
	StopReason      string
	StopRequestedAt *time.Time
	StartedAt       time.Time
	TaskStartedAt   *time.Time
	FinishedAt      *time.Time
	Stdout, Stderr  string
	StdoutCut       bool
	StderrCut       bool
	// StdoutBytes and StderrBytes are the sizes of the kept output; 0 when
	// none was kept. They are read, never written.
	StdoutBytes, StderrBytes int
	// OutputKept is false when the output read failed or the run never ran.
	OutputKept bool
}

// Open reports whether the run is starting or running.
func (r ScheduleRunRecord) Open() bool { return r.Status == RunStarting || r.Status == RunRunning }

// RunSettlement is how an open run ended.
type RunSettlement struct {
	// Status is a final status other than skipped.
	Status   string
	ExitCode *int
	// Error is Caravana's words for the failed kinds; empty for none.
	Error string
	// TaskStartedAt is when the task itself started; nil keeps what is stored.
	TaskStartedAt *time.Time
	FinishedAt    time.Time
	// Stdout and Stderr are kept only when OutputKept is true.
	Stdout, Stderr       []byte
	StdoutCut, StderrCut bool
	OutputKept           bool
}

// runColumns is the column order InsertRun writes.
const runColumns = `id, schedule_id, app_id, service, trigger, slot_at, command, deployment_id, job_version, status,
	exit_code, error, stop_reason, stop_requested_at, started_at, task_started_at, finished_at,
	stdout_cut, stderr_cut, output_kept`

// runSelect is the column order scanRun reads: runColumns and the sizes of
// the output; GetRun adds the output itself.
const runSelect = runColumns + `, COALESCE(LENGTH(CAST(stdout AS BLOB)), 0), COALESCE(LENGTH(CAST(stderr AS BLOB)), 0)`

// InsertRun stores a run as given: starting, before its job is registered, or
// skipped with its reason in Error. An ID in use is ErrAlreadyExists.
func (s *Store) InsertRun(ctx context.Context, r ScheduleRunRecord) error {
	command, err := json.Marshal(r.Command)
	if err != nil {
		return fmt.Errorf("insert run %q: %w", r.ID, err)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO schedule_runs (`+runColumns+`, stdout, stderr)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		r.ID, r.ScheduleID, r.AppID, r.Service, r.Trigger, optionalTime(r.SlotAt), string(command),
		optionalString(r.DeploymentID), optionalInt(r.JobVersion), r.Status, optionalInt(r.ExitCode),
		optionalString(r.Error), optionalString(r.StopReason), optionalTime(r.StopRequestedAt),
		formatTime(r.StartedAt), optionalTime(r.TaskStartedAt), optionalTime(r.FinishedAt),
		boolInt(r.StdoutCut), boolInt(r.StderrCut), boolInt(r.OutputKept),
		optionalOutput(r.OutputKept, r.Stdout), optionalOutput(r.OutputKept, r.Stderr))
	if err != nil {
		return fmt.Errorf("insert run %q of schedule %q: %w", r.ID, r.ScheduleID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("insert run %q: %w", r.ID, err)
	} else if n == 0 {
		return fmt.Errorf("run %q: %w", r.ID, ErrAlreadyExists)
	}
	return nil
}

// SetRunRunning marks a starting run running: Nomad accepted its job. A run
// already running stays so; a run that has ended is ErrInvalidTransition and
// an unknown ID is ErrNotFound.
func (s *Store) SetRunRunning(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE schedule_runs SET status = 'running' WHERE id = ? AND status IN ('starting', 'running')", id)
	if err != nil {
		return fmt.Errorf("set run %q running: %w", id, err)
	}
	return s.openRunUpdated(ctx, res, id)
}

// SetRunStopIntent records why the run is about to be stopped (reason: owner,
// limit, app_stopped or schedule_removed), before the job is deregistered, so
// settling can tell the end reasons apart. The first intent stays: a second
// one changes nothing. A run that has ended is ErrInvalidTransition and an
// unknown ID is ErrNotFound.
func (s *Store) SetRunStopIntent(ctx context.Context, id, reason string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE schedule_runs SET stop_reason = COALESCE(stop_reason, ?), stop_requested_at = COALESCE(stop_requested_at, ?)
		WHERE id = ? AND status IN ('starting', 'running')`, reason, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("set stop intent of run %q: %w", id, err)
	}
	return s.openRunUpdated(ctx, res, id)
}

// SettleRun ends an open run. A run that already ended is ErrInvalidTransition
// (the first settlement stands) and an unknown ID is ErrNotFound.
func (s *Store) SettleRun(ctx context.Context, id string, st RunSettlement) error {
	switch st.Status {
	case RunOK, RunFailed, RunStoppedAtLimit, RunStopped, RunCouldNotStart:
	default:
		return fmt.Errorf("settle run %q: %q is not a status a run ends with", id, st.Status)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE schedule_runs SET status = ?, exit_code = ?, error = ?,
			task_started_at = COALESCE(?, task_started_at), finished_at = ?, stdout = ?, stderr = ?,
			stdout_cut = ?, stderr_cut = ?, output_kept = ?
		WHERE id = ? AND status IN ('starting', 'running')`,
		st.Status, optionalInt(st.ExitCode), optionalString(st.Error), optionalTime(st.TaskStartedAt),
		formatTime(st.FinishedAt), optionalOutput(st.OutputKept, string(st.Stdout)),
		optionalOutput(st.OutputKept, string(st.Stderr)),
		boolInt(st.StdoutCut && st.OutputKept), boolInt(st.StderrCut && st.OutputKept), boolInt(st.OutputKept), id)
	if err != nil {
		return fmt.Errorf("settle run %q: %w", id, err)
	}
	return s.openRunUpdated(ctx, res, id)
}

// openRunUpdated is nil when the update changed the run, else says why not: no
// such run (ErrNotFound) or one that had already ended (ErrInvalidTransition).
func (s *Store) openRunUpdated(ctx context.Context, res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("run %q: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	var status string
	err = s.db.QueryRowContext(ctx, "SELECT status FROM schedule_runs WHERE id = ?", id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("run %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("run %q: %w", id, err)
	}
	return fmt.Errorf("run %q is already %s: %w", id, status, ErrInvalidTransition)
}

// GetRun returns one run with its output, or ErrNotFound.
func (s *Store) GetRun(ctx context.Context, id string) (ScheduleRunRecord, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+runSelect+", stdout, stderr FROM schedule_runs WHERE id = ?", id)
	r, err := scanRun(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return ScheduleRunRecord{}, fmt.Errorf("run %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return ScheduleRunRecord{}, fmt.Errorf("get run %q: %w", id, err)
	}
	return r, nil
}

// ListRuns returns a schedule's runs, newest first, at most limit (all when
// limit <= 0), without their output.
func (s *Store) ListRuns(ctx context.Context, scheduleID string, limit int) ([]ScheduleRunRecord, error) {
	q := "SELECT " + runSelect + " FROM schedule_runs WHERE schedule_id = ? ORDER BY started_at DESC, id DESC"
	args := []any{scheduleID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	return s.queryRuns(ctx, "list runs of schedule "+scheduleID, q, args...)
}

// OpenRuns returns a schedule's runs that are starting or running, newest
// first.
func (s *Store) OpenRuns(ctx context.Context, scheduleID string) ([]ScheduleRunRecord, error) {
	return s.queryRuns(ctx, "open runs of schedule "+scheduleID,
		"SELECT "+runSelect+" FROM schedule_runs WHERE schedule_id = ? AND status IN ('starting', 'running') ORDER BY started_at DESC, id DESC",
		scheduleID)
}

// AllOpenRuns returns every run that is starting or running, across apps,
// oldest first, for the watcher.
func (s *Store) AllOpenRuns(ctx context.Context) ([]ScheduleRunRecord, error) {
	return s.queryRuns(ctx, "open runs",
		"SELECT "+runSelect+" FROM schedule_runs WHERE status IN ('starting', 'running') ORDER BY started_at, id")
}

// LatestScheduleRuns returns, for every schedule, its newest run that has
// ended and was neither skipped nor stopped by the owner, for the needs-you
// list: the question is whether the last real run worked. Newer skipped or
// stopped rows don't hide an older failure.
func (s *Store) LatestScheduleRuns(ctx context.Context) ([]ScheduleRunRecord, error) {
	return s.queryRuns(ctx, "latest runs", `
		SELECT `+runSelect+` FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY schedule_id ORDER BY started_at DESC, id DESC) AS n
			FROM schedule_runs
			WHERE status NOT IN ('starting', 'running', 'skipped', 'stopped'))
		WHERE n = 1 ORDER BY app_id, schedule_id`)
}

// maxSkipStreak is where SkipStreak stops counting.
const maxSkipStreak = 3

// SkipStreak says how many of the schedule's newest runs, in order, are
// skipped slots, counting no further than 3.
func (s *Store) SkipStreak(ctx context.Context, scheduleID string) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT status FROM schedule_runs WHERE schedule_id = ? ORDER BY started_at DESC, id DESC LIMIT ?",
		scheduleID, maxSkipStreak)
	if err != nil {
		return 0, fmt.Errorf("skip streak of schedule %q: %w", scheduleID, err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return 0, fmt.Errorf("skip streak of schedule %q: %w", scheduleID, err)
		}
		if status != RunSkipped {
			break
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("skip streak of schedule %q: %w", scheduleID, err)
	}
	return n, nil
}

// PruneRuns deletes a schedule's runs beyond the newest keep, never one that
// is starting or running, and returns how many it deleted.
func (s *Store) PruneRuns(ctx context.Context, scheduleID string, keep int) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM schedule_runs WHERE schedule_id = ? AND status NOT IN ('starting', 'running') AND id NOT IN (
			SELECT id FROM schedule_runs WHERE schedule_id = ? ORDER BY started_at DESC, id DESC LIMIT ?)`,
		scheduleID, scheduleID, keep)
	if err != nil {
		return 0, fmt.Errorf("prune runs of schedule %q: %w", scheduleID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune runs of schedule %q: %w", scheduleID, err)
	}
	return int(n), nil
}

func (s *Store) queryRuns(ctx context.Context, what, query string, args ...any) ([]ScheduleRunRecord, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()
	var out []ScheduleRunRecord
	for rows.Next() {
		r, err := scanRun(rows, false)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// scanRun reads a row in the order of runSelect, then stdout and stderr when
// withOutput is true.
func scanRun(row scanner, withOutput bool) (ScheduleRunRecord, error) {
	var (
		r                           ScheduleRunRecord
		slot, command               sql.NullString
		deployment, errText, reason sql.NullString
		stopAt, taskStart, finished sql.NullString
		started                     string
		version, exit               sql.NullInt64
		stdoutCut, stderrCut, kept  int
		stdout, stderr              sql.NullString
	)
	dest := []any{&r.ID, &r.ScheduleID, &r.AppID, &r.Service, &r.Trigger, &slot, &command, &deployment, &version,
		&r.Status, &exit, &errText, &reason, &stopAt, &started, &taskStart, &finished,
		&stdoutCut, &stderrCut, &kept, &r.StdoutBytes, &r.StderrBytes}
	if withOutput {
		dest = append(dest, &stdout, &stderr)
	}
	if err := row.Scan(dest...); err != nil {
		return ScheduleRunRecord{}, err
	}
	if err := json.Unmarshal([]byte(command.String), &r.Command); err != nil {
		return ScheduleRunRecord{}, fmt.Errorf("run %q: bad stored command: %w", r.ID, err)
	}
	var err error
	if r.SlotAt, err = parseOptionalTime(slot); err != nil {
		return ScheduleRunRecord{}, fmt.Errorf("run %q: %w", r.ID, err)
	}
	if r.StopRequestedAt, err = parseOptionalTime(stopAt); err != nil {
		return ScheduleRunRecord{}, fmt.Errorf("run %q: %w", r.ID, err)
	}
	if r.StartedAt, err = parseTime(started); err != nil {
		return ScheduleRunRecord{}, fmt.Errorf("run %q: %w", r.ID, err)
	}
	if r.TaskStartedAt, err = parseOptionalTime(taskStart); err != nil {
		return ScheduleRunRecord{}, fmt.Errorf("run %q: %w", r.ID, err)
	}
	if r.FinishedAt, err = parseOptionalTime(finished); err != nil {
		return ScheduleRunRecord{}, fmt.Errorf("run %q: %w", r.ID, err)
	}
	r.DeploymentID, r.Error, r.StopReason = deployment.String, errText.String, reason.String
	if version.Valid {
		r.JobVersion = new(int(version.Int64))
	}
	if exit.Valid {
		r.ExitCode = new(int(exit.Int64))
	}
	r.StdoutCut, r.StderrCut, r.OutputKept = stdoutCut == 1, stderrCut == 1, kept == 1
	r.Stdout, r.Stderr = stdout.String, stderr.String
	return r, nil
}

// optionalString is a string as stored: NULL for empty.
func optionalString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// optionalOutput is a run's output as stored: NULL when it wasn't kept.
func optionalOutput(kept bool, s string) any {
	if !kept {
		return nil
	}
	return s
}
