package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotEmpty means a project or environment still has apps, so it can't be
// deleted. The error returned is a *NotEmptyError, which names them:
//
//	var ne *store.NotEmptyError
//	if errors.As(err, &ne) { ... ne.AppIDs ... }
var ErrNotEmpty = errors.New("not empty")

// NotEmptyError is the error behind ErrNotEmpty. errors.Is(err, ErrNotEmpty)
// is true for it (it says so in its Is method), so callers that only need to
// know "refused because of apps" don't have to unpack it.
type NotEmptyError struct {
	What   string   // "project shop" or "environment shop/staging"
	AppIDs []string // the apps left, by ID
}

func (e *NotEmptyError) Error() string {
	return fmt.Sprintf("%s still has %d app(s): %s", e.What, len(e.AppIDs), strings.Join(e.AppIDs, ", "))
}

// Is lets errors.Is(err, ErrNotEmpty) match a *NotEmptyError.
func (e *NotEmptyError) Is(target error) bool { return target == ErrNotEmpty }

// ProjectRecord is a stored project.
type ProjectRecord struct {
	ID          string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// EnvironmentRecord is a stored environment of a project.
type EnvironmentRecord struct {
	Project   string
	Name      string
	Position  int
	Apps      int // how many apps are in it now
	CreatedAt time.Time
}

// ListProjects returns every project: Default first, then by creation.
func (s *Store) ListProjects(ctx context.Context) ([]ProjectRecord, error) {
	// "id = 'default'" is 1 for Default and 0 for the rest, so it sorts first
	// whatever the clock said when the others were made.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, display_name, created_at, updated_at FROM projects
		ORDER BY id = 'default' DESC, created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	out := []ProjectRecord{}
	for rows.Next() {
		rec, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return out, nil
}

// GetProject returns the project with the given ID, or ErrNotFound.
func (s *Store) GetProject(ctx context.Context, id string) (ProjectRecord, error) {
	rec, err := scanProject(s.db.QueryRowContext(ctx,
		"SELECT id, display_name, created_at, updated_at FROM projects WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectRecord{}, fmt.Errorf("project %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("get project %q: %w", id, err)
	}
	return rec, nil
}

func scanProject(row interface{ Scan(...any) error }) (ProjectRecord, error) {
	var rec ProjectRecord
	var created, updated string
	if err := row.Scan(&rec.ID, &rec.DisplayName, &created, &updated); err != nil {
		return ProjectRecord{}, err
	}
	var err error
	if rec.CreatedAt, err = parseTime(created); err != nil {
		return ProjectRecord{}, fmt.Errorf("project %q: %w", rec.ID, err)
	}
	if rec.UpdatedAt, err = parseTime(updated); err != nil {
		return ProjectRecord{}, fmt.Errorf("project %q: %w", rec.ID, err)
	}
	return rec, nil
}

// InsertProject creates a project and its "production" environment (position
// 0) in one transaction. A project with that ID is ErrAlreadyExists. The
// caller has checked the names (app.ValidName).
func (s *Store) InsertProject(ctx context.Context, id, displayName string) (ProjectRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("insert project %q: %w", id, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if ok, err := exists(ctx, tx, "SELECT 1 FROM projects WHERE id = ?", id); err != nil {
		return ProjectRecord{}, fmt.Errorf("insert project %q: %w", id, err)
	} else if ok {
		return ProjectRecord{}, fmt.Errorf("project %q: %w", id, ErrAlreadyExists)
	}
	now := formatTime(s.now())
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO projects (id, display_name, created_at, updated_at) VALUES (?, ?, ?, ?)",
		id, displayName, now, now); err != nil {
		return ProjectRecord{}, fmt.Errorf("insert project %q: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO environments (project_id, name, position, created_at) VALUES (?, 'production', 0, ?)",
		id, now); err != nil {
		return ProjectRecord{}, fmt.Errorf("insert project %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return ProjectRecord{}, fmt.Errorf("insert project %q: %w", id, err)
	}
	return s.GetProject(ctx, id)
}

// SetProjectDisplayName changes a project's display name. An unknown project
// is ErrNotFound.
func (s *Store) SetProjectDisplayName(ctx context.Context, id, displayName string) (ProjectRecord, error) {
	res, err := s.db.ExecContext(ctx,
		"UPDATE projects SET display_name = ?, updated_at = ? WHERE id = ?",
		displayName, formatTime(s.now()), id)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("rename project %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return ProjectRecord{}, fmt.Errorf("rename project %q: %w", id, err)
	} else if n == 0 {
		return ProjectRecord{}, fmt.Errorf("project %q: %w", id, ErrNotFound)
	}
	return s.GetProject(ctx, id)
}

// DeleteProject removes a project and, with it, its environments. It is
// refused with ErrNotEmpty (a *NotEmptyError naming the apps) while any app is
// in the project, and an unknown project is ErrNotFound. Whether a project may
// be deleted at all (Default may not) is the service's rule.
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete project %q: %w", id, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if ok, err := exists(ctx, tx, "SELECT 1 FROM projects WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete project %q: %w", id, err)
	} else if !ok {
		return fmt.Errorf("project %q: %w", id, ErrNotFound)
	}
	ids, err := appIDsIn(ctx, tx, id, "")
	if err != nil {
		return fmt.Errorf("delete project %q: %w", id, err)
	}
	if len(ids) > 0 {
		return fmt.Errorf("delete project %q: %w", id, &NotEmptyError{What: "project " + id, AppIDs: ids})
	}
	// The environments go with it: their foreign key says ON DELETE CASCADE.
	if _, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete project %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete project %q: %w", id, err)
	}
	return nil
}

// ListEnvironments returns a project's environments in the order shown. An
// unknown project is ErrNotFound.
func (s *Store) ListEnvironments(ctx context.Context, project string) ([]EnvironmentRecord, error) {
	if ok, err := exists(ctx, s.db, "SELECT 1 FROM projects WHERE id = ?", project); err != nil {
		return nil, fmt.Errorf("list environments of %q: %w", project, err)
	} else if !ok {
		return nil, fmt.Errorf("list environments: project %q: %w", project, ErrNotFound)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.project_id, e.name, e.position, e.created_at,
			(SELECT count(*) FROM apps a WHERE a.project = e.project_id AND a.environment = e.name)
		FROM environments e WHERE e.project_id = ? ORDER BY e.position, e.name`, project)
	if err != nil {
		return nil, fmt.Errorf("list environments of %q: %w", project, err)
	}
	defer rows.Close()

	out := []EnvironmentRecord{}
	for rows.Next() {
		var rec EnvironmentRecord
		var created string
		if err := rows.Scan(&rec.Project, &rec.Name, &rec.Position, &created, &rec.Apps); err != nil {
			return nil, fmt.Errorf("list environments of %q: %w", project, err)
		}
		if rec.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("environment %s/%s: %w", project, rec.Name, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list environments of %q: %w", project, err)
	}
	return out, nil
}

// InsertEnvironment adds an environment after the project's others. An unknown
// project is ErrNotFound; a name already in the project is ErrAlreadyExists.
func (s *Store) InsertEnvironment(ctx context.Context, project, name string) (EnvironmentRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if ok, err := exists(ctx, tx, "SELECT 1 FROM projects WHERE id = ?", project); err != nil {
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name, err)
	} else if !ok {
		return EnvironmentRecord{}, fmt.Errorf("insert environment: project %q: %w", project, ErrNotFound)
	}
	if ok, err := exists(ctx, tx, "SELECT 1 FROM environments WHERE project_id = ? AND name = ?", project, name); err != nil {
		return EnvironmentRecord{}, fmt.Errorf("insert environment %s/%s: %w", project, name, err)
	} else if ok {
		return EnvironmentRecord{}, fmt.Errorf("environment %s/%s: %w", project, name, ErrAlreadyExists)
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

// getEnvironment reads one environment of a project, ErrNotFound when absent.
func (s *Store) getEnvironment(ctx context.Context, project, name string) (EnvironmentRecord, error) {
	envs, err := s.ListEnvironments(ctx, project)
	if err != nil {
		return EnvironmentRecord{}, err
	}
	for _, e := range envs {
		if e.Name == name {
			return e, nil
		}
	}
	return EnvironmentRecord{}, fmt.Errorf("environment %s/%s: %w", project, name, ErrNotFound)
}

// DeleteEnvironment removes an environment. It is refused with ErrNotEmpty (a
// *NotEmptyError naming the apps) while any app is in it, and one that doesn't
// exist is ErrNotFound. Whether it may be deleted at all (a production
// environment may not) is the service's rule.
func (s *Store) DeleteEnvironment(ctx context.Context, project, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete environment %s/%s: %w", project, name, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if ok, err := exists(ctx, tx, "SELECT 1 FROM environments WHERE project_id = ? AND name = ?", project, name); err != nil {
		return fmt.Errorf("delete environment %s/%s: %w", project, name, err)
	} else if !ok {
		return fmt.Errorf("environment %s/%s: %w", project, name, ErrNotFound)
	}
	ids, err := appIDsIn(ctx, tx, project, name)
	if err != nil {
		return fmt.Errorf("delete environment %s/%s: %w", project, name, err)
	}
	if len(ids) > 0 {
		return fmt.Errorf("delete environment %s/%s: %w", project, name,
			&NotEmptyError{What: "environment " + project + "/" + name, AppIDs: ids})
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM environments WHERE project_id = ? AND name = ?", project, name); err != nil {
		return fmt.Errorf("delete environment %s/%s: %w", project, name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete environment %s/%s: %w", project, name, err)
	}
	return nil
}

// EnvironmentExists reports whether the project has an environment of that
// name. An unknown project simply has none.
func (s *Store) EnvironmentExists(ctx context.Context, project, name string) (bool, error) {
	ok, err := exists(ctx, s.db, "SELECT 1 FROM environments WHERE project_id = ? AND name = ?", project, name)
	if err != nil {
		return false, fmt.Errorf("environment %s/%s exists: %w", project, name, err)
	}
	return ok, nil
}

// appIDsIn lists the IDs of the apps in a project, and, when environment is
// not "", in that environment of it.
func appIDsIn(ctx context.Context, tx *sql.Tx, project, environment string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT id FROM apps WHERE project = ? AND (? = '' OR environment = ?) ORDER BY id",
		project, environment, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
