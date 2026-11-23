package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// Builds (feature 050). A build is the first stage of a deployment of a built
// service: Caravana clones the repository, builds its Dockerfile and pushes the
// image, and only then deploys the digest. This file keeps one row per build,
// beside the deployment it belongs to. The row holds names, Caravana's own
// sentences and the build's masked log, never a deploy key, a token or the push
// credentials (constitution VII; they live in Nomad Variables).

// The statuses of a build.
const (
	BuildQueued    = "queued"
	BuildRunning   = "running"
	BuildSucceeded = "succeeded"
	BuildFailed    = "failed"
	BuildCancelled = "cancelled"
)

// BuildLogsKept is how many builds of an app keep their log. Older rows keep
// every other field.
const BuildLogsKept = 50

// BuildRecord is one build.
type BuildRecord struct {
	ID           string
	DeploymentID string
	AppID        string
	Service      string
	// Status is one of the Build* statuses.
	Status string
	// Repository, Branch: what was asked for. Commit, Author and Subject are
	// what was cloned (empty until the clone, and when it failed).
	Repository string
	Branch     string
	Commit     string
	Author     string
	Subject    string
	// Image is the image the build pushed, repo:git-<commit12>; Digest the
	// manifest digest the registry has, which the deployment pins.
	Image  string
	Digest string
	// Error is why a failed build failed: Caravana's sentence, then the last
	// lines of the log.
	Error string
	// Log is the last 64 KiB of the build's output, masked. It is empty on
	// the summary a DeploymentRecord carries, and once the build's log was
	// pruned. LogCut says the output was longer than what is kept.
	Log    string
	LogCut bool
	// Node is the Nomad node the build ran on, "" before it started.
	Node string
	// QueuedAt is when the deployment was made; StartedAt and EndedAt are zero
	// until the build starts and ends.
	QueuedAt, StartedAt, EndedAt time.Time
}

// Final says the build ended.
func (b BuildRecord) Final() bool {
	switch b.Status {
	case BuildSucceeded, BuildFailed, BuildCancelled:
		return true
	}
	return false
}

// BuildEnd is what settles a build.
type BuildEnd struct {
	// Status is succeeded, failed or cancelled.
	Status string
	// Commit, Author, Subject are what the clone read, when it got that far.
	Commit, Author, Subject string
	Image, Digest           string
	Error                   string
	Log                     string
	LogCut                  bool
	At                      time.Time
}

// CreateDeploymentWithBuild is CreateDeployment for a deployment that has to
// build first: the build's row (status queued, ID, Service, Repository, Branch
// and Image as given) is written in the same transaction, so the deployment is
// never without it. The deployment's record has the build.
func (s *Store) CreateDeploymentWithBuild(ctx context.Context, appID string, d app.Deployment, definition *app.App, file *compose.Request, source string, b BuildRecord) (DeploymentRecord, error) {
	return s.createDeployment(ctx, appID, d, definition, file, source, &b)
}

const buildColumns = `id, deployment_id, app_id, service, status, repository, branch, commit_sha, author, subject,
	image, digest, error, log, log_cut, node, queued_at, started_at, ended_at`

func insertBuild(ctx context.Context, tx *sql.Tx, b BuildRecord, deploymentID, appID string, now time.Time) error {
	if b.ID == "" || b.Service == "" {
		return errors.New("a build needs an ID and a service")
	}
	if b.QueuedAt.IsZero() {
		b.QueuedAt = now
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO builds (id, deployment_id, app_id, service, status, repository, branch, image, queued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, deploymentID, appID, b.Service, BuildQueued, b.Repository, b.Branch, b.Image, formatTime(b.QueuedAt)); err != nil {
		if isUnique(err) {
			return fmt.Errorf("build %q: %w", b.ID, ErrAlreadyExists)
		}
		return fmt.Errorf("insert build %q: %w", b.ID, err)
	}
	return nil
}

func scanBuild(r interface{ Scan(...any) error }) (BuildRecord, error) {
	var b BuildRecord
	var log sql.NullString
	var cut int
	var queued, started, ended string
	if err := r.Scan(&b.ID, &b.DeploymentID, &b.AppID, &b.Service, &b.Status, &b.Repository, &b.Branch, &b.Commit,
		&b.Author, &b.Subject, &b.Image, &b.Digest, &b.Error, &log, &cut, &b.Node, &queued, &started, &ended); err != nil {
		return BuildRecord{}, err
	}
	b.Log, b.LogCut = log.String, cut == 1
	var err error
	for _, f := range []struct {
		text string
		into *time.Time
	}{{queued, &b.QueuedAt}, {started, &b.StartedAt}, {ended, &b.EndedAt}} {
		if *f.into, err = parseTimeOrZero(f.text); err != nil {
			return BuildRecord{}, fmt.Errorf("build %s: %w", b.ID, err)
		}
	}
	return b, nil
}

// GetBuildOfDeployment returns the build of a deployment, log included, or
// ErrNotFound when the deployment didn't build.
func (s *Store) GetBuildOfDeployment(ctx context.Context, deploymentID string) (BuildRecord, error) {
	b, err := scanBuild(s.db.QueryRowContext(ctx, `SELECT `+buildColumns+` FROM builds WHERE deployment_id = ?`, deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return BuildRecord{}, fmt.Errorf("build of deployment %q: %w", deploymentID, ErrNotFound)
	}
	if err != nil {
		return BuildRecord{}, fmt.Errorf("build of deployment %q: %w", deploymentID, err)
	}
	return b, nil
}

// GetBuild returns one build by its ID, log included, or ErrNotFound.
func (s *Store) GetBuild(ctx context.Context, id string) (BuildRecord, error) {
	b, err := scanBuild(s.db.QueryRowContext(ctx, `SELECT `+buildColumns+` FROM builds WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return BuildRecord{}, fmt.Errorf("build %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return BuildRecord{}, fmt.Errorf("build %q: %w", id, err)
	}
	return b, nil
}

// StartBuild marks a queued build running. A build that isn't queued (it was
// cancelled while it waited) is ErrNotFound.
func (s *Store) StartBuild(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE builds SET status = 'running', started_at = ? WHERE id = ? AND status = 'queued'`,
		formatTime(at), id)
	return oneBuildRow(res, err, "start build", id)
}

// SetBuildNode records the node a running build was placed on.
func (s *Store) SetBuildNode(ctx context.Context, id, node string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE builds SET node = ? WHERE id = ?`, node, id)
	return oneBuildRow(res, err, "set build node", id)
}

func oneBuildRow(res sql.Result, err error, what, id string) error {
	if err != nil {
		return fmt.Errorf("%s %q: %w", what, id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("%s %q: %w", what, id, err)
	} else if n == 0 {
		return fmt.Errorf("%s %q: %w", what, id, ErrNotFound)
	}
	return nil
}

// EndBuild settles an open build (queued or running) with a final status and
// what it produced, and prunes the logs of the app's builds beyond the newest
// BuildLogsKept. A build that is already final is ErrNotFound, so a settled
// build never changes again.
func (s *Store) EndBuild(ctx context.Context, id string, e BuildEnd) error {
	if e.Status != BuildSucceeded && e.Status != BuildFailed && e.Status != BuildCancelled {
		return fmt.Errorf("end build %q: %q is not a final status", id, e.Status)
	}
	if e.At.IsZero() {
		e.At = s.now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("end build %q: %w", id, err)
	}
	defer tx.Rollback()
	var appID string
	if err := tx.QueryRowContext(ctx, `SELECT app_id FROM builds WHERE id = ?`, id).Scan(&appID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("end build %q: %w", id, ErrNotFound)
		}
		return fmt.Errorf("end build %q: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE builds SET status = ?, commit_sha = ?, author = ?, subject = ?, image = CASE WHEN ? != '' THEN ? ELSE image END,
		digest = ?, error = ?, log = ?, log_cut = ?, ended_at = ?
		WHERE id = ? AND status IN ('queued', 'running')`,
		e.Status, e.Commit, e.Author, e.Subject, e.Image, e.Image, e.Digest, e.Error, nullIfEmpty(e.Log), boolInt(e.LogCut), formatTime(e.At), id)
	if err := oneBuildRow(res, err, "end build", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE builds SET log = NULL WHERE app_id = ? AND log IS NOT NULL
		AND id NOT IN (SELECT id FROM builds WHERE app_id = ? ORDER BY queued_at DESC, id DESC LIMIT ?)`,
		appID, appID, BuildLogsKept); err != nil {
		return fmt.Errorf("end build %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("end build %q: %w", id, err)
	}
	return nil
}

func nullIfEmpty(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// OpenBuilds returns the builds that are queued or running, oldest first. At
// start the service settles them: a build doesn't outlive the process that ran it.
func (s *Store) OpenBuilds(ctx context.Context) ([]BuildRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+buildColumns+` FROM builds
		WHERE status IN ('queued', 'running') ORDER BY queued_at, id`)
	if err != nil {
		return nil, fmt.Errorf("open builds: %w", err)
	}
	defer rows.Close()
	var out []BuildRecord
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, fmt.Errorf("open builds: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("open builds: %w", err)
	}
	return out, nil
}

// buildSummaryJSON is the SQL that makes a build row into the JSON object
// decodeBuildSummary reads: every column but the log, so a list of deployments
// doesn't carry up to 64 KiB per row.
const buildSummaryJSON = `json_object('id', b.id, 'deploymentId', b.deployment_id, 'appId', b.app_id, 'service', b.service,
	'status', b.status, 'repository', b.repository, 'branch', b.branch, 'commit', b.commit_sha, 'author', b.author,
	'subject', b.subject, 'image', b.image, 'digest', b.digest, 'error', b.error, 'logCut', b.log_cut, 'node', b.node,
	'queuedAt', b.queued_at, 'startedAt', b.started_at, 'endedAt', b.ended_at)`

func decodeBuildSummary(doc string) (*BuildRecord, error) {
	var j struct {
		ID, DeploymentID, AppID, Service, Status, Repository, Branch, Commit, Author, Subject string
		Image, Digest, Error, Node, QueuedAt, StartedAt, EndedAt                              string
		LogCut                                                                                int
	}
	if err := json.Unmarshal([]byte(doc), &j); err != nil {
		return nil, fmt.Errorf("decode build: %w", err)
	}
	b := &BuildRecord{ID: j.ID, DeploymentID: j.DeploymentID, AppID: j.AppID, Service: j.Service, Status: j.Status,
		Repository: j.Repository, Branch: j.Branch, Commit: j.Commit, Author: j.Author, Subject: j.Subject,
		Image: j.Image, Digest: j.Digest, Error: j.Error, Node: j.Node, LogCut: j.LogCut == 1}
	var err error
	for _, f := range []struct {
		text string
		into *time.Time
	}{{j.QueuedAt, &b.QueuedAt}, {j.StartedAt, &b.StartedAt}, {j.EndedAt, &b.EndedAt}} {
		if *f.into, err = parseTimeOrZero(f.text); err != nil {
			return nil, fmt.Errorf("decode build %s: %w", j.ID, err)
		}
	}
	return b, nil
}
