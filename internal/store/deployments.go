package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Status is where a deployment stands.
type Status string

// A deployment starts pending, usually runs, and ends in one final status.
const (
	StatusPending    Status = "pending"    // created, not yet registered with Nomad
	StatusRunning    Status = "running"    // registered; Nomad is rolling it out
	StatusHealthy    Status = "healthy"    // final: the rollout succeeded
	StatusFailed     Status = "failed"     // final: it failed (before or during the rollout)
	StatusReverted   Status = "reverted"   // final: it failed and Nomad rolled back to the previous version
	StatusSuperseded Status = "superseded" // final: a newer deploy replaced it mid-rollout
)

// allowedNext lists the statuses each status may change to. Final statuses
// have no entry, so nothing can change them.
var allowedNext = map[Status][]Status{
	StatusPending: {StatusRunning, StatusFailed, StatusSuperseded},
	StatusRunning: {StatusRunning, StatusHealthy, StatusFailed, StatusReverted, StatusSuperseded},
}

// DeploymentRecord is a stored deployment.
type DeploymentRecord struct {
	Deployment app.Deployment
	AppID      string
	Status     Status
	JobVersion *uint64 // nil until Nomad registers the job
	Warnings   []app.Warning
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DeploymentUpdate changes a deployment. Zero values keep what's stored.
type DeploymentUpdate struct {
	Status     Status        // required
	JobVersion *uint64       // nil keeps the stored value
	Warnings   []app.Warning // nil keeps the stored value
	Error      string        // "" keeps the stored value
}

// CreateDeployment records a new, pending deployment of an existing app.
func (s *Store) CreateDeployment(ctx context.Context, appID string, d app.Deployment) (DeploymentRecord, error) {
	if d.ID == "" {
		return DeploymentRecord{}, errors.New("create deployment: ID is required")
	}
	digests, err := json.Marshal(d.Digests)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("create deployment %q: %w", d.ID, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("create deployment %q: %w", d.ID, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if ok, err := exists(ctx, tx, "SELECT 1 FROM apps WHERE id = ?", appID); err != nil {
		return DeploymentRecord{}, fmt.Errorf("create deployment %q: %w", d.ID, err)
	} else if !ok {
		return DeploymentRecord{}, fmt.Errorf("create deployment %q: app %q: %w", d.ID, appID, ErrNotFound)
	}
	if ok, err := exists(ctx, tx, "SELECT 1 FROM deployments WHERE id = ?", d.ID); err != nil {
		return DeploymentRecord{}, fmt.Errorf("create deployment %q: %w", d.ID, err)
	} else if ok {
		return DeploymentRecord{}, fmt.Errorf("deployment %q: %w", d.ID, ErrAlreadyExists)
	}

	now := formatTime(s.now())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deployments (id, app_id, trigger, digests, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, appID, string(d.Trigger), string(digests), string(StatusPending), now, now); err != nil {
		return DeploymentRecord{}, fmt.Errorf("create deployment %q: %w", d.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return DeploymentRecord{}, fmt.Errorf("create deployment %q: %w", d.ID, err)
	}
	return s.GetDeployment(ctx, d.ID)
}

// GetDeployment returns the deployment with the given ID, or ErrNotFound.
func (s *Store) GetDeployment(ctx context.Context, id string) (DeploymentRecord, error) {
	return getDeployment(ctx, s.db, id)
}

// UpdateDeployment changes a deployment's status and, optionally, its job
// version, warnings and error. A status change the lifecycle doesn't allow
// returns ErrInvalidTransition and changes nothing.
func (s *Store) UpdateDeployment(ctx context.Context, id string, u DeploymentUpdate) (DeploymentRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("update deployment %q: %w", id, err)
	}
	defer tx.Rollback()

	// Reading and writing in one transaction means nobody can change the
	// status between the check and the update.
	cur, err := getDeployment(ctx, tx, id)
	if err != nil {
		return DeploymentRecord{}, err
	}
	if !slices.Contains(allowedNext[cur.Status], u.Status) {
		return DeploymentRecord{}, fmt.Errorf("deployment %q: %s -> %q: %w", id, cur.Status, u.Status, ErrInvalidTransition)
	}

	jobVersion := cur.JobVersion
	if u.JobVersion != nil {
		jobVersion = u.JobVersion
	}
	warnings := cur.Warnings
	if u.Warnings != nil {
		warnings = u.Warnings
	}
	errText := cur.Error
	if u.Error != "" {
		errText = u.Error
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("update deployment %q: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE deployments
		SET status = ?, job_version = ?, warnings = ?, error = ?, updated_at = ?
		WHERE id = ?`,
		string(u.Status), jobVersion, string(warningsJSON), errText, formatTime(s.now()), id); err != nil {
		return DeploymentRecord{}, fmt.Errorf("update deployment %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return DeploymentRecord{}, fmt.Errorf("update deployment %q: %w", id, err)
	}
	return s.GetDeployment(ctx, id)
}

// ListDeployments returns an app's deployments, newest first. An unknown app
// is ErrNotFound; an app without deployments gives an empty list.
func (s *Store) ListDeployments(ctx context.Context, appID string) ([]DeploymentRecord, error) {
	if ok, err := exists(ctx, s.db, "SELECT 1 FROM apps WHERE id = ?", appID); err != nil {
		return nil, fmt.Errorf("list deployments of %q: %w", appID, err)
	} else if !ok {
		return nil, fmt.Errorf("list deployments: app %q: %w", appID, ErrNotFound)
	}
	// rowid breaks ties between deployments created in the same instant.
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+deploymentColumns+" FROM deployments WHERE app_id = ? ORDER BY created_at DESC, rowid DESC", appID)
	if err != nil {
		return nil, fmt.Errorf("list deployments of %q: %w", appID, err)
	}
	defer rows.Close()

	var out []DeploymentRecord
	for rows.Next() {
		rec, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("list deployments of %q: %w", appID, err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

const deploymentColumns = "id, app_id, trigger, digests, status, job_version, warnings, error, created_at, updated_at"

// querier is what *sql.DB and *sql.Tx have in common, so the same read works
// inside and outside a transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getDeployment(ctx context.Context, q querier, id string) (DeploymentRecord, error) {
	row := q.QueryRowContext(ctx, "SELECT "+deploymentColumns+" FROM deployments WHERE id = ?", id)
	rec, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentRecord{}, fmt.Errorf("deployment %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("get deployment %q: %w", id, err)
	}
	return rec, nil
}

// scanDeployment reads one row of deploymentColumns. *sql.Row and *sql.Rows
// both have Scan.
func scanDeployment(row interface{ Scan(...any) error }) (DeploymentRecord, error) {
	var (
		rec                                DeploymentRecord
		trigger, digests, status, warnings string
		created, updated                   string
		jobVersion                         sql.NullInt64
	)
	if err := row.Scan(&rec.Deployment.ID, &rec.AppID, &trigger, &digests, &status, &jobVersion,
		&warnings, &rec.Error, &created, &updated); err != nil {
		return DeploymentRecord{}, err
	}
	rec.Deployment.Trigger = app.Trigger(trigger)
	rec.Status = Status(status)
	if jobVersion.Valid {
		rec.JobVersion = new(uint64(jobVersion.Int64))
	}
	if err := json.Unmarshal([]byte(digests), &rec.Deployment.Digests); err != nil {
		return DeploymentRecord{}, fmt.Errorf("decode digests: %w", err)
	}
	if err := json.Unmarshal([]byte(warnings), &rec.Warnings); err != nil {
		return DeploymentRecord{}, fmt.Errorf("decode warnings: %w", err)
	}
	var err error
	if rec.CreatedAt, err = parseTime(created); err != nil {
		return DeploymentRecord{}, err
	}
	if rec.UpdatedAt, err = parseTime(updated); err != nil {
		return DeploymentRecord{}, err
	}
	return rec, nil
}

// exists reports whether query returns a row.
func exists(ctx context.Context, q querier, query string, args ...any) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
