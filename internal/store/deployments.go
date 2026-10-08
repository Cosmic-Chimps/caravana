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
	// Definition is the app as this deployment deployed it, secret values
	// left out. nil for deployments recorded before feature 011.
	Definition *app.App
	// Source is the deployment this one redeployed, or "".
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DeploymentUpdate changes a deployment. Zero values keep what's stored.
type DeploymentUpdate struct {
	Status     Status            // required
	JobVersion *uint64           // nil keeps the stored value
	Digests    map[string]string // nil keeps the stored value
	Warnings   []app.Warning     // nil keeps the stored value
	Error      string            // "" keeps the stored value
}

// CreateDeployment records a new, pending deployment of an existing app,
// with the definition it deploys (stored without secret values; nil stores
// none) and the deployment it redeploys (source, or ""). Neither changes
// afterwards.
func (s *Store) CreateDeployment(ctx context.Context, appID string, d app.Deployment, definition *app.App, source string) (DeploymentRecord, error) {
	if d.ID == "" {
		return DeploymentRecord{}, errors.New("create deployment: ID is required")
	}
	digests, err := json.Marshal(d.Digests)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("create deployment %q: %w", d.ID, err)
	}
	var def, src sql.NullString
	if definition != nil {
		doc, err := json.Marshal(definition.WithoutSecretValues())
		if err != nil {
			return DeploymentRecord{}, fmt.Errorf("create deployment %q: %w", d.ID, err)
		}
		def = sql.NullString{String: string(doc), Valid: true}
	}
	if source != "" {
		src = sql.NullString{String: source, Valid: true}
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
		INSERT INTO deployments (id, app_id, trigger, digests, status, definition, source_deployment, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, appID, string(d.Trigger), string(digests), string(StatusPending), def, src, now, now); err != nil {
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
// version, digests, warnings and error. A status change the lifecycle doesn't allow
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
	digests := cur.Deployment.Digests
	if u.Digests != nil {
		digests = u.Digests
	}
	warnings := cur.Warnings
	if u.Warnings != nil {
		warnings = u.Warnings
	}
	errText := cur.Error
	if u.Error != "" {
		errText = u.Error
	}
	digestsJSON, err := json.Marshal(digests)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("update deployment %q: %w", id, err)
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("update deployment %q: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE deployments
		SET status = ?, job_version = ?, digests = ?, warnings = ?, error = ?, updated_at = ?
		WHERE id = ?`,
		string(u.Status), jobVersion, string(digestsJSON), string(warningsJSON), errText, formatTime(s.now()), id); err != nil {
		return DeploymentRecord{}, fmt.Errorf("update deployment %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return DeploymentRecord{}, fmt.Errorf("update deployment %q: %w", id, err)
	}
	return s.GetDeployment(ctx, id)
}

// ListDeployments returns an app's newest deployments, newest first: at most
// limit of them, or all of them when limit is 0 or less. An unknown app is
// ErrNotFound; an app without deployments gives an empty list.
func (s *Store) ListDeployments(ctx context.Context, appID string, limit int) ([]DeploymentRecord, error) {
	if ok, err := exists(ctx, s.db, "SELECT 1 FROM apps WHERE id = ?", appID); err != nil {
		return nil, fmt.Errorf("list deployments of %q: %w", appID, err)
	} else if !ok {
		return nil, fmt.Errorf("list deployments: app %q: %w", appID, ErrNotFound)
	}
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	// rowid breaks ties between deployments created in the same instant.
	out, err := s.queryDeployments(ctx,
		"SELECT "+deploymentColumns+" FROM deployments WHERE app_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?",
		appID, limit)
	if err != nil {
		return nil, fmt.Errorf("list deployments of %q: %w", appID, err)
	}
	return out, nil
}

// ListDeploymentsBefore returns an app's deployments created before the given
// time, newest first, at most limit of them: the next page after one that
// ended at before. A limit below 1 returns none (the caller validates it, and
// "everything" would be a surprise). An unknown app is ErrNotFound.
func (s *Store) ListDeploymentsBefore(ctx context.Context, appID string, before time.Time, limit int) ([]DeploymentRecord, error) {
	if ok, err := exists(ctx, s.db, "SELECT 1 FROM apps WHERE id = ?", appID); err != nil {
		return nil, fmt.Errorf("list deployments of %q before %s: %w", appID, formatTime(before), err)
	} else if !ok {
		return nil, fmt.Errorf("list deployments: app %q: %w", appID, ErrNotFound)
	}
	if limit < 1 {
		return []DeploymentRecord{}, nil
	}
	out, err := s.queryDeployments(ctx,
		"SELECT "+deploymentColumns+" FROM deployments WHERE app_id = ? AND created_at < ? ORDER BY created_at DESC, rowid DESC LIMIT ?",
		appID, formatTime(before), limit)
	if err != nil {
		return nil, fmt.Errorf("list deployments of %q before %s: %w", appID, formatTime(before), err)
	}
	if out == nil {
		out = []DeploymentRecord{}
	}
	return out, nil
}

// LatestDeployments returns each app's newest deployment, keyed by app ID.
// Apps without deployments have no entry. Deployments created in the same
// instant are ordered by ID, so the answer never depends on row order. It is
// one query, so the Apps page costs one read however many apps there are.
func (s *Store) LatestDeployments(ctx context.Context) (map[string]DeploymentRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+deploymentColumns+` FROM (
			SELECT `+deploymentColumns+`,
				ROW_NUMBER() OVER (PARTITION BY app_id ORDER BY created_at DESC, id) AS n
			FROM deployments
		) WHERE n = 1`)
	if err != nil {
		return nil, fmt.Errorf("latest deployments: %w", err)
	}
	defer rows.Close()

	out := map[string]DeploymentRecord{}
	for rows.Next() {
		rec, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("latest deployments: %w", err)
		}
		out[rec.AppID] = rec
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("latest deployments: %w", err)
	}
	return out, nil
}

// ListRecentDeployments returns the newest deployments across all apps,
// newest first, at most limit of them. A limit below 1 returns none: callers
// validate it, and "no limit" across every app would be a surprise. The
// deployments_by_time index serves the ordering.
func (s *Store) ListRecentDeployments(ctx context.Context, limit int) ([]DeploymentRecord, error) {
	if limit < 1 {
		return []DeploymentRecord{}, nil
	}
	out, err := s.queryDeployments(ctx,
		"SELECT "+deploymentColumns+" FROM deployments ORDER BY created_at DESC, id LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("list recent deployments: %w", err)
	}
	if out == nil {
		out = []DeploymentRecord{}
	}
	return out, nil
}

// ListUnfinishedDeployments returns every deployment still pending or
// running, oldest first. On startup the deploy flow picks them up again.
func (s *Store) ListUnfinishedDeployments(ctx context.Context) ([]DeploymentRecord, error) {
	out, err := s.queryDeployments(ctx,
		"SELECT "+deploymentColumns+" FROM deployments WHERE status IN (?, ?) ORDER BY created_at, rowid",
		string(StatusPending), string(StatusRunning))
	if err != nil {
		return nil, fmt.Errorf("list unfinished deployments: %w", err)
	}
	return out, nil
}

// LatestHealthyDeployment returns appID's newest healthy deployment: what
// Nomad is running for it. ErrNotFound when it has none.
func (s *Store) LatestHealthyDeployment(ctx context.Context, appID string) (DeploymentRecord, error) {
	recs, err := s.queryDeployments(ctx,
		"SELECT "+deploymentColumns+" FROM deployments WHERE app_id = ? AND status = ? ORDER BY created_at DESC, rowid DESC LIMIT 1",
		appID, string(StatusHealthy))
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("latest healthy deployment of %q: %w", appID, err)
	}
	if len(recs) == 0 {
		return DeploymentRecord{}, fmt.Errorf("latest healthy deployment of %q: %w", appID, ErrNotFound)
	}
	return recs[0], nil
}

// queryDeployments runs a query returning deploymentColumns rows.
func (s *Store) queryDeployments(ctx context.Context, query string, args ...any) ([]DeploymentRecord, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DeploymentRecord
	for rows.Next() {
		rec, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

const deploymentColumns = "id, app_id, trigger, digests, status, job_version, warnings, error, definition, source_deployment, created_at, updated_at"

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
		definition, source                 sql.NullString
	)
	if err := row.Scan(&rec.Deployment.ID, &rec.AppID, &trigger, &digests, &status, &jobVersion,
		&warnings, &rec.Error, &definition, &source, &created, &updated); err != nil {
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
	if definition.Valid {
		rec.Definition = new(app.App)
		if err := json.Unmarshal([]byte(definition.String), rec.Definition); err != nil {
			return DeploymentRecord{}, fmt.Errorf("decode definition: %w", err)
		}
	}
	rec.Source = source.String
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
