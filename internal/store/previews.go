package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Previews (feature 049). A preview is an environment named pr-<n>
// (app.PreviewNumber); nothing in the schema says so. What the store keeps is
// the project's settings (columns of projects) and the questions the service
// asks about previews: how many are open, when each was last active, and the
// Team-only rows their domains share.

// ErrPreviewLimit means a project has as many open previews as its limit
// allows. The error returned is a *PreviewLimitError, which names them.
var ErrPreviewLimit = errors.New("preview limit reached")

// PreviewLimitError is the error behind ErrPreviewLimit.
type PreviewLimitError struct {
	Project string
	Limit   int
	Open    []string // the open preview environments, by pull request number
}

func (e *PreviewLimitError) Error() string {
	if e.Limit == 0 {
		return fmt.Sprintf("previews are off for project %s (its limit is 0)", e.Project)
	}
	return fmt.Sprintf("project %s has %d open previews (%s), its limit; close one or raise the limit",
		e.Project, len(e.Open), strings.Join(e.Open, ", "))
}

// Is lets errors.Is(err, ErrPreviewLimit) match a *PreviewLimitError.
func (e *PreviewLimitError) Is(target error) bool { return target == ErrPreviewLimit }

// previewNames lists the preview environments among names, by pull request
// number.
func previewNames(names []string) []string {
	var out []string
	for _, n := range names {
		if app.IsPreviewEnvironment(n) {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		na, _ := app.PreviewNumber(a)
		nb, _ := app.PreviewNumber(b)
		return na - nb
	})
	return out
}

// InsertPreviewEnvironment is InsertEnvironment for a preview name, with the
// project's limit: it counts the project's preview environments and inserts in
// one transaction, so two creates at once can't both take the last place. An
// unknown project is ErrNotFound, a name already there ErrAlreadyExists, and a
// project at its limit ErrPreviewLimit (a *PreviewLimitError). The caller has
// checked the name.
func (s *Store) InsertPreviewEnvironment(ctx context.Context, project, name string) (EnvironmentRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	var limit int
	switch err := tx.QueryRowContext(ctx, "SELECT preview_limit FROM projects WHERE id = ?", project).Scan(&limit); {
	case errors.Is(err, sql.ErrNoRows):
		return EnvironmentRecord{}, fmt.Errorf("insert environment: project %q: %w", project, ErrNotFound)
	case err != nil:
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name, err)
	}
	names, err := environmentNames(ctx, tx, project)
	if err != nil {
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name, err)
	}
	if slices.Contains(names, name) {
		return EnvironmentRecord{}, fmt.Errorf("environment %s/%s: %w", project, name, ErrAlreadyExists)
	}
	if open := previewNames(names); len(open) >= limit {
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name,
			&PreviewLimitError{Project: project, Limit: limit, Open: open})
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO environments (project_id, name, position, created_at)
		VALUES (?, ?, (SELECT COALESCE(MAX(position), -1) + 1 FROM environments WHERE project_id = ?), ?)`,
		project, name, project, formatTime(s.now())); err != nil {
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name, err)
	}
	if err := tx.Commit(); err != nil {
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name, err)
	}
	return s.getEnvironment(ctx, project, name)
}

// environmentNames lists a project's environment names.
func environmentNames(ctx context.Context, tx *sql.Tx, project string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM environments WHERE project_id = ? ORDER BY position, name", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// PreviewSettings is what SetPreviewSettings changes. A nil field keeps what
// there is; ClearPassword removes the password (PasswordHash must then be nil).
type PreviewSettings struct {
	Limit         *int
	ExpireDays    *int
	PasswordHash  *string
	ClearPassword bool
}

// SetPreviewSettings changes a project's preview settings in one statement and
// returns the project. An unknown project is ErrNotFound. The caller has
// validated the values.
func (s *Store) SetPreviewSettings(ctx context.Context, project string, set PreviewSettings) (ProjectRecord, error) {
	if set.ClearPassword && set.PasswordHash != nil {
		return ProjectRecord{}, errors.New("set preview settings: a password can't be both set and cleared")
	}
	var hash any // the column keeps its value unless asked
	changeHash := 0
	switch {
	case set.PasswordHash != nil:
		hash, changeHash = *set.PasswordHash, 1
	case set.ClearPassword:
		hash, changeHash = nil, 1
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE projects SET
			preview_limit = COALESCE(?, preview_limit),
			preview_expire_days = COALESCE(?, preview_expire_days),
			preview_password_hash = CASE WHEN ? = 1 THEN ? ELSE preview_password_hash END,
			updated_at = ?
		WHERE id = ?`,
		set.Limit, set.ExpireDays, changeHash, hash, formatTime(s.now()), project)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("set preview settings of %q: %w", project, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return ProjectRecord{}, fmt.Errorf("set preview settings of %q: %w", project, err)
	} else if n == 0 {
		return ProjectRecord{}, fmt.Errorf("project %q: %w", project, ErrNotFound)
	}
	return s.GetProject(ctx, project)
}

// PreviewActivity is when a preview environment was made and when an app in it
// was last deployed, the two times its expiry counts from.
type PreviewActivity struct {
	Environment string
	Number      int
	CreatedAt   time.Time
	// LastDeployment is the newest deployment of any app in the environment, or
	// the zero time when none was ever made.
	LastDeployment time.Time
}

// Idle is the moment the preview's clock starts: the later of its creation and
// its newest deployment.
func (a PreviewActivity) Idle() time.Time {
	if a.LastDeployment.After(a.CreatedAt) {
		return a.LastDeployment
	}
	return a.CreatedAt
}

// PreviewActivity returns, for each preview environment of the project, by
// pull request number, its creation time and the time of the newest deployment
// of an app in it. An unknown project is ErrNotFound.
func (s *Store) PreviewActivity(ctx context.Context, project string) ([]PreviewActivity, error) {
	if ok, err := exists(ctx, s.db, "SELECT 1 FROM projects WHERE id = ?", project); err != nil {
		return nil, fmt.Errorf("preview activity of %q: %w", project, err)
	} else if !ok {
		return nil, fmt.Errorf("preview activity: project %q: %w", project, ErrNotFound)
	}
	// created_at keeps nine fraction digits, so the text sorts as time and MAX
	// is the newest.
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.name, e.created_at,
			COALESCE((SELECT MAX(d.created_at) FROM deployments d JOIN apps a ON a.id = d.app_id
				WHERE a.project = e.project_id AND a.environment = e.name), '')
		FROM environments e WHERE e.project_id = ? AND e.name LIKE 'pr-%'`, project)
	if err != nil {
		return nil, fmt.Errorf("preview activity of %q: %w", project, err)
	}
	defer rows.Close()

	var out []PreviewActivity
	for rows.Next() {
		var name, created, last string
		if err := rows.Scan(&name, &created, &last); err != nil {
			return nil, fmt.Errorf("preview activity of %q: %w", project, err)
		}
		n, ok := app.PreviewNumber(name) // the LIKE only narrows: the name rule decides
		if !ok {
			continue
		}
		a := PreviewActivity{Environment: name, Number: n}
		if a.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("preview %s/%s: %w", project, name, err)
		}
		if last != "" {
			if a.LastDeployment, err = parseTime(last); err != nil {
				return nil, fmt.Errorf("preview %s/%s: %w", project, name, err)
			}
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("preview activity of %q: %w", project, err)
	}
	slices.SortFunc(out, func(a, b PreviewActivity) int { return a.Number - b.Number })
	return out, nil
}

// PreviewTeamPasswords returns the Team-only rows of the apps in the project's
// preview environments (every row of such an app, by host).
func (s *Store) PreviewTeamPasswords(ctx context.Context, project string) ([]TeamPasswordRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.host, t.app_id, t.hash, t.created_at, a.environment
		FROM team_passwords t JOIN apps a ON a.id = t.app_id
		WHERE a.project = ? AND a.environment LIKE 'pr-%' ORDER BY t.host`, project)
	if err != nil {
		return nil, fmt.Errorf("list preview team passwords of %q: %w", project, err)
	}
	defer rows.Close()
	var out []TeamPasswordRecord
	for rows.Next() {
		var r TeamPasswordRecord
		var created, env string
		if err := rows.Scan(&r.Host, &r.AppID, &r.Hash, &created, &env); err != nil {
			return nil, fmt.Errorf("list preview team passwords of %q: %w", project, err)
		}
		if !app.IsPreviewEnvironment(env) {
			continue
		}
		if r.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("list preview team passwords of %q: %w", project, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list preview team passwords of %q: %w", project, err)
	}
	return out, nil
}

// SetPreviewTeamHash gives every Team-only row of the project's preview
// environments the same hash, in one transaction, and returns the rows as they
// were, so a caller that can't publish the change can put them back with
// SetTeamPassword.
func (s *Store) SetPreviewTeamHash(ctx context.Context, project, hash string, at time.Time) ([]TeamPasswordRecord, error) {
	old, err := s.PreviewTeamPasswords(ctx, project)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("set preview team hash of %q: %w", project, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds
	for _, r := range old {
		if _, err := tx.ExecContext(ctx, "UPDATE team_passwords SET hash = ?, created_at = ? WHERE host = ?",
			hash, formatTime(at), r.Host); err != nil {
			return nil, fmt.Errorf("set preview team hash of %q: %w", project, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("set preview team hash of %q: %w", project, err)
	}
	return old, nil
}
