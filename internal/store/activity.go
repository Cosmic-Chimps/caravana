package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// The activity feed (feature 027): what happened on the host and to the apps on
// it, read from the records Caravana already keeps. The store gives plain rows;
// the words are the service's job. No row holds command output, a command's
// arguments, an env value or a secret: an app action carries its program (the
// command's first word) and its exit code, and nothing else.

// The tables an activity row can come from.
const (
	ActivityDeployment = "deployments"    // two rows per deployment: Phase "started", then "ended" once it is over
	ActivityAction     = "app_actions"    // Label is the action: stop, start, restart, run or terminal
	ActivityBackup     = "backups"        // Label is the engine; At is its end, or its start while it runs
	ActivityRestore    = "restores"       // likewise
	ActivitySchedule   = "schedule_runs"  // only runs that failed or were stopped at their limit; Label is the schedule's name
	ActivityProblem    = "problem_events" // Label is the attention kind, Status the change, Detail the sentence sent
	ActivityHost       = "host_events"    // Label is the kind (boot, version, os_packages), Detail its JSON
)

// ServerProblemKinds are the needs-you kinds about the server itself: the host
// (disk, OS updates) and its copies (out of memory, crash loops). ListActivity's
// serverOnly keeps their problem events and drops the rest.
var ServerProblemKinds = []string{
	"disk_nearly_full", "os_reboot_required", "os_security_updates", // the host
	"out_of_memory", "restarting", // the copies on it
}

// ActivityRow is one row of the feed. Which fields are filled depends on
// Source; the others are empty.
type ActivityRow struct {
	Source string // one of the Activity constants: the table the row is from
	// RowID is the row's ID in that table (a backup's is "<app>/<id>", an event's
	// is its number). With Source and Phase it identifies the entry for good.
	RowID string
	// Phase tells a deployment's two entries apart: "started" or "ended".
	Phase string
	At    time.Time
	// AppID and Place name the app; both are empty for the host's own rows.
	AppID string
	Place app.Place

	Service string
	// Status is the row's status as stored: a deployment's, an action's, a
	// backup's, a schedule run's; for a problem event, its change (opened,
	// worsened, resolved).
	Status  string
	Trigger string
	Label   string // see the Activity constants
	// Program is a command's first word (an action's), or the shell a terminal
	// asked for. Never the arguments.
	Program  string
	ExitCode *int
	// Version is a deployment's Nomad job version, nil until it registered.
	Version *int64
	// Error is why a deployment, an action or a schedule run failed, in
	// Caravana's words.
	Error string
	// Detail is JSON or a sentence the row came with: a deployment's origin (the
	// CI details), a schedule run's stop reason, a problem event's sentence, a
	// host event's JSON.
	Detail string
}

// activitySQL is the feed: one UNION ALL over the sources, each cut to
// ?1 (a stored time; empty means no cut) and, with ?2 = 1, to what concerns the
// server. ?3 is the limit. Every branch has the same twelve columns.
const activitySQL = `
SELECT u.source, u.row_id, u.phase, u.at, u.app_id, COALESCE(a.project, ''), COALESCE(a.environment, ''), COALESCE(a.app_name, ''),
       u.service, u.status, u.trigger, u.label, u.program, u.exit_code, u.version, u.error, u.detail
FROM (
  SELECT 'deployments' AS source, id AS row_id, 'started' AS phase, created_at AS at, app_id,
         '' AS service, status, trigger, '' AS label, '' AS program, NULL AS exit_code, job_version AS version,
         error, COALESCE(origin, '') AS detail
  FROM deployments WHERE ?2 = 0 AND (?1 = '' OR created_at < ?1)
  UNION ALL
  SELECT 'deployments', id, 'ended', updated_at, app_id,
         '', status, trigger, '', '', NULL, job_version,
         error, COALESCE(origin, '')
  FROM deployments WHERE ?2 = 0 AND status IN ('healthy', 'failed', 'reverted', 'superseded') AND (?1 = '' OR updated_at < ?1)
  UNION ALL
  SELECT 'app_actions', id, '', started_at, app_id,
         service, status, trigger, action, program, exit_code, NULL,
         error, ''
  FROM app_actions WHERE (?2 = 0 OR action = 'restart') AND (?1 = '' OR started_at < ?1)
  UNION ALL
  SELECT 'backups', app_id || '/' || id, '', COALESCE(finished_at, started_at), app_id,
         service, status, '', engine, '', NULL, NULL,
         '', ''
  FROM backups WHERE ?2 = 0 AND (?1 = '' OR COALESCE(finished_at, started_at) < ?1)
  UNION ALL
  SELECT 'restores', id, '', COALESCE(finished_at, created_at), app_id,
         service, status, '', engine, '', NULL, NULL,
         '', ''
  FROM restores WHERE ?2 = 0 AND (?1 = '' OR COALESCE(finished_at, created_at) < ?1)
  UNION ALL
  SELECT 'schedule_runs', r.id, '', COALESCE(r.finished_at, r.started_at), r.app_id,
         r.service, r.status, r.trigger, s.name, '', r.exit_code, NULL,
         COALESCE(r.error, ''), COALESCE(r.stop_reason, '')
  FROM schedule_runs r JOIN schedules s ON s.id = r.schedule_id
  WHERE ?2 = 0 AND r.status IN ('failed', 'stopped_at_limit') AND (?1 = '' OR COALESCE(r.finished_at, r.started_at) < ?1)
  UNION ALL
  SELECT 'problem_events', CAST(id AS TEXT), '', at, app_id,
         '', change, '', kind, '', NULL, NULL,
         '', summary
  FROM problem_events WHERE (?2 = 0 OR kind IN (%s)) AND (?1 = '' OR at < ?1)
  UNION ALL
  SELECT 'host_events', CAST(id AS TEXT), '', at, '',
         '', '', '', kind, '', NULL, NULL,
         '', detail
  FROM host_events WHERE (?1 = '' OR at < ?1)
) u
LEFT JOIN apps a ON a.id = u.app_id
ORDER BY u.at DESC, u.source, u.row_id DESC, u.phase
LIMIT ?3`

// ListActivity returns up to limit rows of the feed, newest first. A zero
// before means from the newest; otherwise only rows older than it, which is how
// the next page is read. Rows at exactly the same instant as the last of a page
// are not repeated on the next one, nor guaranteed to be on it, since times
// have nine fraction digits and a clash is not expected.
//
// serverOnly keeps what concerns the server: host events, problem events of
// ServerProblemKinds and restarts of an app. Everything else is left out.
func (s *Store) ListActivity(ctx context.Context, limit int, before time.Time, serverOnly bool) ([]ActivityRow, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list activity: limit %d, want at least 1", limit)
	}
	cut := ""
	if !before.IsZero() {
		cut = formatTime(before)
	}
	server := 0
	if serverOnly {
		server = 1
	}
	list := ""
	for i, k := range ServerProblemKinds {
		if i > 0 {
			list += ", "
		}
		list += "'" + k + "'" // fixed words of this package, never input
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(activitySQL, list), cut, server, limit)
	if err != nil {
		return nil, fmt.Errorf("list activity: %w", err)
	}
	defer rows.Close()
	var out []ActivityRow
	for rows.Next() {
		var r ActivityRow
		var at string
		var exit, version sql.NullInt64
		if err := rows.Scan(&r.Source, &r.RowID, &r.Phase, &at, &r.AppID, &r.Place.Project, &r.Place.Environment, &r.Place.Name,
			&r.Service, &r.Status, &r.Trigger, &r.Label, &r.Program, &exit, &version, &r.Error, &r.Detail); err != nil {
			return nil, fmt.Errorf("list activity: %w", err)
		}
		if r.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("list activity: %w", err)
		}
		if exit.Valid {
			n := int(exit.Int64)
			r.ExitCode = &n
		}
		if version.Valid {
			r.Version = &version.Int64
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list activity: %w", err)
	}
	return out, nil
}
