package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Autoscaling (feature 043): the counts a scaled service has now, and the
// versions Nomad's scale steps made.

// SetServiceCount stores the count a scaled service has now.
func (s *Store) SetServiceCount(ctx context.Context, appID, service string, count int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO service_counts (app_id, service, count, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (app_id, service) DO UPDATE SET count = excluded.count, updated_at = excluded.updated_at`,
		appID, service, count, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("set count of %s/%s: %w", appID, service, err)
	}
	return nil
}

// ReplaceServiceCounts makes counts the app's stored counts: the services in it
// are set and any other service's count is removed, in one transaction. A
// deploy calls it with the counts it registered, so a service that is no longer
// scaled leaves nothing behind.
func (s *Store) ReplaceServiceCounts(ctx context.Context, appID string, counts map[string]int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace counts of %s: %w", appID, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds
	if _, err := tx.ExecContext(ctx, "DELETE FROM service_counts WHERE app_id = ?", appID); err != nil {
		return fmt.Errorf("replace counts of %s: %w", appID, err)
	}
	now := formatTime(s.now())
	for service, count := range counts {
		if _, err := tx.ExecContext(ctx, "INSERT INTO service_counts (app_id, service, count, updated_at) VALUES (?, ?, ?, ?)",
			appID, service, count, now); err != nil {
			return fmt.Errorf("replace counts of %s: %w", appID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace counts of %s: %w", appID, err)
	}
	return nil
}

// ServiceCounts returns every stored count: app ID, then service name, to
// count.
func (s *Store) ServiceCounts(ctx context.Context) (map[string]map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT app_id, service, count FROM service_counts")
	if err != nil {
		return nil, fmt.Errorf("list service counts: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var appID, service string
		var count int
		if err := rows.Scan(&appID, &service, &count); err != nil {
			return nil, fmt.Errorf("list service counts: %w", err)
		}
		if out[appID] == nil {
			out[appID] = map[string]int{}
		}
		out[appID][service] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list service counts: %w", err)
	}
	return out, nil
}

// LastScale returns the newest scale action of a service, of any outcome. The
// bool is false when it has none.
func (s *Store) LastScale(ctx context.Context, appID, service string) (ActionRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+actionColumns+
		" FROM app_actions WHERE app_id = ? AND service = ? AND action = 'scale' ORDER BY started_at DESC, id DESC LIMIT 1",
		appID, service)
	a, err := scanAction(row)
	if err == sql.ErrNoRows {
		return ActionRecord{}, false, nil
	}
	if err != nil {
		return ActionRecord{}, false, fmt.Errorf("last scale of %s/%s: %w", appID, service, err)
	}
	return a, true, nil
}

// ScaleVersions returns, per app, the job versions that scale steps made and
// the version of the deployment each one scaled: job version to base version.
// Nomad makes a new job version for a scale; the console and the API show the
// deployment's.
func (s *Store) ScaleVersions(ctx context.Context) (map[string]map[uint64]uint64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT app_id, job_version, base_version FROM app_actions
		WHERE action = 'scale' AND job_version IS NOT NULL AND base_version IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("list scale versions: %w", err)
	}
	defer rows.Close()
	out := map[string]map[uint64]uint64{}
	for rows.Next() {
		var appID string
		var job, base int64
		if err := rows.Scan(&appID, &job, &base); err != nil {
			return nil, fmt.Errorf("list scale versions: %w", err)
		}
		if out[appID] == nil {
			out[appID] = map[uint64]uint64{}
		}
		out[appID][uint64(job)] = uint64(base)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list scale versions: %w", err)
	}
	return out, nil
}

// RecentScales returns the newest limit scale actions of the given apps, newest
// first (Monitoring's list of steps). No apps give none.
func (s *Store) RecentScales(ctx context.Context, appIDs []string, limit int) ([]ActionRecord, error) {
	if len(appIDs) == 0 {
		return []ActionRecord{}, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(appIDs)), ", ")
	args := make([]any, 0, len(appIDs)+1)
	for _, id := range appIDs {
		args = append(args, id)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, "SELECT "+actionColumns+
		" FROM app_actions WHERE action = 'scale' AND app_id IN ("+marks+") ORDER BY started_at DESC, id DESC LIMIT ?", args...)
	if err != nil {
		return nil, fmt.Errorf("list recent scales: %w", err)
	}
	defer rows.Close()
	out := []ActionRecord{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, fmt.Errorf("list recent scales: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recent scales: %w", err)
	}
	return out, nil
}
