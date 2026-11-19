package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// Image scanning (feature 044). This file keeps the add-on's record, the scan
// queue and history, one result per image digest with its findings, and the
// owner's settings. It holds names, counts, versions and Caravana's own
// sentences, never the scanner's output (constitution VII).

// ProblemImageVulnerable is the needs-you kind of a fixable vulnerability in a live
// image. The service package owns the kind (service.AttentionImageVulnerable, which
// a test keeps equal); the store needs the word to forget the notifier's rows.
const ProblemImageVulnerable = "image_vulnerable"

// AddonImageScanner is the name of the image scanner add-on.
const AddonImageScanner = "image-scanner"

// AddonState is where an add-on is in its life.
type AddonState string

// The states of an add-on. No row at all means it isn't installed.
const (
	AddonInstalling AddonState = "installing"
	AddonInstalled  AddonState = "installed"
	AddonFailed     AddonState = "failed"
	AddonRemoving   AddonState = "removing"
)

// Addon is one add-on's record.
type Addon struct {
	Name  string
	State AddonState
	// Version is the version Caravana pins for it.
	Version     string
	InstalledAt time.Time // zero until installed
	UpdatedAt   time.Time
	// Reason is why a failed install or remove failed, in Caravana's words.
	Reason string
}

// GetAddon returns the add-on's record, or ErrNotFound when it has none.
func (s *Store) GetAddon(ctx context.Context, name string) (Addon, error) {
	var a Addon
	var state, installed, updated string
	err := s.db.QueryRowContext(ctx, `SELECT name, state, version, installed_at, updated_at, reason
		FROM addons WHERE name = ?`, name).Scan(&a.Name, &state, &a.Version, &installed, &updated, &a.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return Addon{}, fmt.Errorf("add-on %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Addon{}, fmt.Errorf("add-on %q: %w", name, err)
	}
	a.State = AddonState(state)
	if a.InstalledAt, err = parseTimeOrZero(installed); err != nil {
		return Addon{}, fmt.Errorf("add-on %q: %w", name, err)
	}
	if a.UpdatedAt, err = parseTime(updated); err != nil {
		return Addon{}, fmt.Errorf("add-on %q: %w", name, err)
	}
	return a, nil
}

// SaveAddon writes the add-on's record, replacing the one with its name. A zero
// UpdatedAt is the store's clock.
func (s *Store) SaveAddon(ctx context.Context, a Addon) error {
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = s.now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO addons (name, state, version, installed_at, updated_at, reason)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET state = excluded.state, version = excluded.version,
			installed_at = excluded.installed_at, updated_at = excluded.updated_at, reason = excluded.reason`,
		a.Name, string(a.State), a.Version, zeroOrTime(a.InstalledAt), formatTime(a.UpdatedAt), a.Reason)
	if err != nil {
		return fmt.Errorf("save add-on %q: %w", a.Name, err)
	}
	return nil
}

// DeleteAddon removes the add-on's record, so it lists as available again. One
// that has none is done.
func (s *Store) DeleteAddon(ctx context.Context, name string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM addons WHERE name = ?`, name); err != nil {
		return fmt.Errorf("delete add-on %q: %w", name, err)
	}
	return nil
}

// ListAddons returns the records that exist, by name.
func (s *Store) ListAddons(ctx context.Context) ([]Addon, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM addons ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list add-ons: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list add-ons: %w", err)
		}
		names = append(names, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("list add-ons: %w", err)
	}
	var out []Addon
	for _, n := range names {
		a, err := s.GetAddon(ctx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// The triggers, sources and states of a scan run.
const (
	ScanTriggerInstall = "install"
	ScanTriggerDeploy  = "deploy"
	ScanTriggerDaily   = "daily"
	ScanTriggerOwner   = "owner"
	ScanTriggerGate    = "gate"

	ScanSourceRegistry = "registry"
	ScanSourceSBOM     = "sbom"
	ScanSourceDatabase = "database"

	ScanQueued  = "queued"
	ScanRunning = "running"
	ScanOK      = "ok"
	ScanFailed  = "failed"
	ScanSkipped = "skipped"
)

// scanRunsKept is how many finished runs the history keeps.
const scanRunsKept = 200

// ScanRun is one scan: a row of the queue and of the history. Image is empty
// for the install's database run.
type ScanRun struct {
	ID      string
	Image   string
	Trigger string
	Source  string
	Status  string
	// Reason is Caravana's sentence for a failed or skipped run.
	Reason string
	// Scanner and DBBuilt are what an ok run reports: the scanner's version and
	// the build time of the database it used.
	Scanner                       string
	DBBuilt                       time.Time
	CreatedAt, StartedAt, EndedAt time.Time
}

const scanRunColumns = `id, image, trigger, source, status, reason, scanner, db_built, created_at, started_at, ended_at`

func scanScanRun(r interface{ Scan(...any) error }) (ScanRun, error) {
	var run ScanRun
	var built, created, started, ended string
	if err := r.Scan(&run.ID, &run.Image, &run.Trigger, &run.Source, &run.Status, &run.Reason, &run.Scanner,
		&built, &created, &started, &ended); err != nil {
		return ScanRun{}, err
	}
	var err error
	for _, f := range []struct {
		text string
		into *time.Time
	}{{built, &run.DBBuilt}, {created, &run.CreatedAt}, {started, &run.StartedAt}, {ended, &run.EndedAt}} {
		if *f.into, err = parseTimeOrZero(f.text); err != nil {
			return ScanRun{}, fmt.Errorf("scan run %s: %w", run.ID, err)
		}
	}
	return run, nil
}

// QueueScanRun inserts run as queued, unless a run of the same image is already
// queued or running: then it returns that run and false. A zero CreatedAt is the
// store's clock. The ID, trigger and source must be set.
func (s *Store) QueueScanRun(ctx context.Context, run ScanRun) (ScanRun, bool, error) {
	if run.CreatedAt.IsZero() {
		run.CreatedAt = s.now()
	}
	run.Status = ScanQueued
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScanRun{}, false, fmt.Errorf("queue scan of %q: %w", run.Image, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds
	open, err := scanScanRun(tx.QueryRowContext(ctx, `SELECT `+scanRunColumns+` FROM scan_runs
		WHERE image = ? AND status IN ('queued', 'running') ORDER BY created_at LIMIT 1`, run.Image))
	switch {
	case err == nil:
		return open, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return ScanRun{}, false, fmt.Errorf("queue scan of %q: %w", run.Image, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scan_runs (`+scanRunColumns+`)
		VALUES (?, ?, ?, ?, ?, '', '', '', ?, '', '')`,
		run.ID, run.Image, run.Trigger, run.Source, run.Status, formatTime(run.CreatedAt)); err != nil {
		if isUnique(err) {
			return ScanRun{}, false, fmt.Errorf("scan run %q: %w", run.ID, ErrAlreadyExists)
		}
		return ScanRun{}, false, fmt.Errorf("queue scan of %q: %w", run.Image, err)
	}
	if err := tx.Commit(); err != nil {
		return ScanRun{}, false, fmt.Errorf("queue scan of %q: %w", run.Image, err)
	}
	return run, true, nil
}

// NextQueuedScanRun returns the run to start next: a gate run first, then the
// oldest. ErrNotFound when the queue is empty.
func (s *Store) NextQueuedScanRun(ctx context.Context) (ScanRun, error) {
	run, err := scanScanRun(s.db.QueryRowContext(ctx, `SELECT `+scanRunColumns+` FROM scan_runs
		WHERE status = 'queued' ORDER BY (trigger = 'gate') DESC, created_at, id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return ScanRun{}, fmt.Errorf("next scan: %w", ErrNotFound)
	}
	if err != nil {
		return ScanRun{}, fmt.Errorf("next scan: %w", err)
	}
	return run, nil
}

// GetScanRun returns one run, or ErrNotFound.
func (s *Store) GetScanRun(ctx context.Context, id string) (ScanRun, error) {
	run, err := scanScanRun(s.db.QueryRowContext(ctx, `SELECT `+scanRunColumns+` FROM scan_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ScanRun{}, fmt.Errorf("scan run %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return ScanRun{}, fmt.Errorf("scan run %q: %w", id, err)
	}
	return run, nil
}

// ListScanRuns returns the newest runs first, at most limit of them (all when
// limit <= 0).
func (s *Store) ListScanRuns(ctx context.Context, limit int) ([]ScanRun, error) {
	q := `SELECT ` + scanRunColumns + ` FROM scan_runs ORDER BY created_at DESC, id DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.queryScanRuns(ctx, q, args...)
}

// OpenScanRuns returns the runs that are queued or running, oldest first.
func (s *Store) OpenScanRuns(ctx context.Context) ([]ScanRun, error) {
	return s.queryScanRuns(ctx, `SELECT `+scanRunColumns+` FROM scan_runs
		WHERE status IN ('queued', 'running') ORDER BY created_at, id`)
}

func (s *Store) queryScanRuns(ctx context.Context, q string, args ...any) ([]ScanRun, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list scan runs: %w", err)
	}
	defer rows.Close()
	var out []ScanRun
	for rows.Next() {
		run, err := scanScanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list scan runs: %w", err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list scan runs: %w", err)
	}
	return out, nil
}

// StartScanRun marks a queued run running, and records what it reads (a run is
// queued before that is known: a daily rescan reads the kept SBOM when there is
// one). A run that isn't queued (it was failed by a remove, say) is ErrNotFound.
func (s *Store) StartScanRun(ctx context.Context, id, source string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE scan_runs SET status = 'running', source = ?, started_at = ?
		WHERE id = ? AND status = 'queued'`, source, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("start scan run %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("start scan run %q: %w", id, err)
	} else if n == 0 {
		return fmt.Errorf("start scan run %q: %w", id, ErrNotFound)
	}
	return nil
}

// EndScanRun settles an open run (queued or running) with a final status. A
// run that is already final is ErrNotFound, so a settled run never changes
// again. scanner and dbBuilt are for an ok run.
func (s *Store) EndScanRun(ctx context.Context, id, status, reason, scanner string, dbBuilt, at time.Time) error {
	if status != ScanOK && status != ScanFailed && status != ScanSkipped {
		return fmt.Errorf("end scan run %q: %q is not a final status", id, status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("end scan run %q: %w", id, err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE scan_runs SET status = ?, reason = ?, scanner = ?, db_built = ?, ended_at = ?
		WHERE id = ? AND status IN ('queued', 'running')`,
		status, reason, scanner, zeroOrTime(dbBuilt), formatTime(at), id)
	if err != nil {
		return fmt.Errorf("end scan run %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("end scan run %q: %w", id, err)
	} else if n == 0 {
		return fmt.Errorf("end scan run %q: %w", id, ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM scan_runs WHERE status NOT IN ('queued', 'running')
		AND id NOT IN (SELECT id FROM scan_runs ORDER BY created_at DESC, id DESC LIMIT ?)`, scanRunsKept); err != nil {
		return fmt.Errorf("end scan run %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("end scan run %q: %w", id, err)
	}
	return nil
}

// FailOpenScanRuns settles the queued runs (with queued set) and the running
// ones (with running set) as failed with reason, and returns the runs it settled.
func (s *Store) FailOpenScanRuns(ctx context.Context, queued, running bool, reason string, at time.Time) ([]ScanRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("fail scan runs: %w", err)
	}
	defer tx.Rollback()
	var states []string
	if queued {
		states = append(states, `'queued'`)
	}
	if running {
		states = append(states, `'running'`)
	}
	if len(states) == 0 {
		return nil, nil
	}
	where := `status IN (` + strings.Join(states, ", ") + `)`
	rows, err := tx.QueryContext(ctx, `SELECT `+scanRunColumns+` FROM scan_runs WHERE `+where+` ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("fail scan runs: %w", err)
	}
	var runs []ScanRun
	for rows.Next() {
		run, err := scanScanRun(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("fail scan runs: %w", err)
		}
		runs = append(runs, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("fail scan runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scan_runs SET status = 'failed', reason = ?, ended_at = ? WHERE `+where,
		reason, formatTime(at)); err != nil {
		return nil, fmt.Errorf("fail scan runs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("fail scan runs: %w", err)
	}
	for i := range runs {
		runs[i].Status, runs[i].Reason, runs[i].EndedAt = ScanFailed, reason, at
	}
	return runs, nil
}

// ImageScan is the stored state of one image digest: the counts of its newest
// successful result and the outcome of the newest try.
type ImageScan struct {
	Image string
	// ScannedAt is when the newest successful scan ended; zero when none did.
	ScannedAt time.Time
	// DBBuilt, DBSchema and Scanner are the database and scanner of that scan.
	DBBuilt  time.Time
	DBSchema string
	Scanner  string
	// Cut says that scan had more findings than are kept.
	Cut bool
	// SBOMKept says the scanner's volume has the image's SBOM, so a rescan needs
	// no pull.
	SBOMKept bool
	Counts   vulns.Counts
	// TriedAt, Status and Reason are the newest try's: status is ok, failed or
	// skipped, and for the last two Reason is Caravana's sentence.
	TriedAt time.Time
	Status  string
	Reason  string
}

// HasResult says a scan of the image ever succeeded, so its findings are stored.
func (i ImageScan) HasResult() bool { return !i.ScannedAt.IsZero() }

// StoredFinding is a finding with when scans first and last saw it.
type StoredFinding struct {
	vulns.Finding
	FirstSeen, LastSeen time.Time
}

const imageScanColumns = `image, scanned_at, db_built, db_schema, scanner, cut, sbom_kept,
	critical, high, medium, low, info, fixable_critical, fixable_high, kev, tried_at, status, reason`

func scanImageScan(r interface{ Scan(...any) error }) (ImageScan, error) {
	var i ImageScan
	var scanned, built, tried string
	var cut, kept int
	c := &i.Counts
	if err := r.Scan(&i.Image, &scanned, &built, &i.DBSchema, &i.Scanner, &cut, &kept,
		&c.Critical, &c.High, &c.Medium, &c.Low, &c.Info, &c.FixableCritical, &c.FixableHigh, &c.KEV,
		&tried, &i.Status, &i.Reason); err != nil {
		return ImageScan{}, err
	}
	i.Cut, i.SBOMKept = cut != 0, kept != 0
	var err error
	if i.ScannedAt, err = parseTimeOrZero(scanned); err != nil {
		return ImageScan{}, fmt.Errorf("scan of %q: %w", i.Image, err)
	}
	if i.DBBuilt, err = parseTimeOrZero(built); err != nil {
		return ImageScan{}, fmt.Errorf("scan of %q: %w", i.Image, err)
	}
	if i.TriedAt, err = parseTime(tried); err != nil {
		return ImageScan{}, fmt.Errorf("scan of %q: %w", i.Image, err)
	}
	return i, nil
}

// SaveScanResult stores a successful scan of image, replacing its result in one
// transaction: each finding is written by its ID, keeping the first_seen of one
// an earlier scan saw and moving its last_seen to at, and the findings this
// result doesn't have are deleted. The counts, the scanner and database it used
// and the try's outcome (ok) are written too.
func (s *Store) SaveScanResult(ctx context.Context, image string, r vulns.Result, sbomKept bool, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save scan of %q: %w", image, err)
	}
	defer tx.Rollback()
	c := r.Counts()
	when := formatTime(at)
	if _, err := tx.ExecContext(ctx, `INSERT INTO image_scans (`+imageScanColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ok', '')
		ON CONFLICT (image) DO UPDATE SET scanned_at = excluded.scanned_at, db_built = excluded.db_built,
			db_schema = excluded.db_schema, scanner = excluded.scanner, cut = excluded.cut,
			sbom_kept = excluded.sbom_kept, critical = excluded.critical, high = excluded.high,
			medium = excluded.medium, low = excluded.low, info = excluded.info,
			fixable_critical = excluded.fixable_critical, fixable_high = excluded.fixable_high,
			kev = excluded.kev, tried_at = excluded.tried_at, status = 'ok', reason = ''`,
		image, when, zeroOrTime(r.DBBuilt), r.DBSchema, r.Scanner, boolInt(r.Cut), boolInt(sbomKept),
		c.Critical, c.High, c.Medium, c.Low, c.Info, c.FixableCritical, c.FixableHigh, c.KEV, when); err != nil {
		return fmt.Errorf("save scan of %q: %w", image, err)
	}

	have := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM image_findings WHERE image = ?`, image)
	if err != nil {
		return fmt.Errorf("save scan of %q: %w", image, err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("save scan of %q: %w", image, err)
		}
		have[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("save scan of %q: %w", image, err)
	}

	upsert, err := tx.PrepareContext(ctx, `INSERT INTO image_findings
		(id, image, advisory, cve, package, version, type, severity, fix_state, fixed_in, url,
		 epss, epss_percentile, kev, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (image, id) DO UPDATE SET advisory = excluded.advisory, cve = excluded.cve,
			package = excluded.package, version = excluded.version, type = excluded.type,
			severity = excluded.severity, fix_state = excluded.fix_state, fixed_in = excluded.fixed_in,
			url = excluded.url, epss = excluded.epss, epss_percentile = excluded.epss_percentile,
			kev = excluded.kev, last_seen = excluded.last_seen`)
	if err != nil {
		return fmt.Errorf("save scan of %q: %w", image, err)
	}
	defer upsert.Close()
	for _, f := range r.Findings {
		var epss, pct any
		if f.EPSS != nil {
			epss, pct = f.EPSS.Score, f.EPSS.Percentile
		}
		if _, err := upsert.ExecContext(ctx, f.ID, image, f.Advisory, f.CVE, f.Package, f.Version, f.Type,
			f.Severity.String(), f.FixState, strings.Join(f.FixedIn, "\n"), f.URL, epss, pct, boolInt(f.KEV), when, when); err != nil {
			return fmt.Errorf("save scan of %q: %w", image, err)
		}
		delete(have, f.ID)
	}
	for id := range have { // what this result no longer has
		if _, err := tx.ExecContext(ctx, `DELETE FROM image_findings WHERE image = ? AND id = ?`, image, id); err != nil {
			return fmt.Errorf("save scan of %q: %w", image, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save scan of %q: %w", image, err)
	}
	return nil
}

// RecordScanTry records a try that gave no result (status failed or skipped,
// with Caravana's sentence): the findings of the newest successful scan stay.
// An image never scanned gets a row with no result.
func (s *Store) RecordScanTry(ctx context.Context, image, status, reason string, at time.Time) error {
	if status != ScanFailed && status != ScanSkipped {
		return fmt.Errorf("record scan try of %q: %q is not failed or skipped", image, status)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO image_scans (image, tried_at, status, reason) VALUES (?, ?, ?, ?)
		ON CONFLICT (image) DO UPDATE SET tried_at = excluded.tried_at, status = excluded.status, reason = excluded.reason`,
		image, formatTime(at), status, reason)
	if err != nil {
		return fmt.Errorf("record scan try of %q: %w", image, err)
	}
	return nil
}

// ClearSBOMKept says the image's SBOM isn't on the volume after all, so the next
// scan reads the registry.
func (s *Store) ClearSBOMKept(ctx context.Context, image string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE image_scans SET sbom_kept = 0 WHERE image = ?`, image); err != nil {
		return fmt.Errorf("clear kept SBOM of %q: %w", image, err)
	}
	return nil
}

// GetImageScan returns the stored scan of one image, or ErrNotFound.
func (s *Store) GetImageScan(ctx context.Context, image string) (ImageScan, error) {
	i, err := scanImageScan(s.db.QueryRowContext(ctx, `SELECT `+imageScanColumns+` FROM image_scans WHERE image = ?`, image))
	if errors.Is(err, sql.ErrNoRows) {
		return ImageScan{}, fmt.Errorf("scan of %q: %w", image, ErrNotFound)
	}
	if err != nil {
		return ImageScan{}, fmt.Errorf("scan of %q: %w", image, err)
	}
	return i, nil
}

// ListImageScans returns every stored scan, by image.
func (s *Store) ListImageScans(ctx context.Context) ([]ImageScan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+imageScanColumns+` FROM image_scans ORDER BY image`)
	if err != nil {
		return nil, fmt.Errorf("list scans: %w", err)
	}
	defer rows.Close()
	var out []ImageScan
	for rows.Next() {
		i, err := scanImageScan(rows)
		if err != nil {
			return nil, fmt.Errorf("list scans: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list scans: %w", err)
	}
	return out, nil
}

// severityRank orders the stored severity names, worst first.
const severityOrder = `CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END`

const findingColumns = `image, id, advisory, cve, package, version, type, severity, fix_state, fixed_in, url,
	epss, epss_percentile, kev, first_seen, last_seen`

func scanFinding(r interface{ Scan(...any) error }) (string, StoredFinding, error) {
	var image, severity, fixed, first, last string
	var epss, pct sql.Null[float64]
	var kev int
	var f StoredFinding
	if err := r.Scan(&image, &f.ID, &f.Advisory, &f.CVE, &f.Package, &f.Version, &f.Type, &severity, &f.FixState,
		&fixed, &f.URL, &epss, &pct, &kev, &first, &last); err != nil {
		return "", StoredFinding{}, err
	}
	f.Severity = vulns.FromGrype(severity)
	if fixed != "" {
		f.FixedIn = strings.Split(fixed, "\n")
	}
	if epss.Valid && pct.Valid {
		f.EPSS = &vulns.EPSS{Score: epss.V, Percentile: pct.V}
	}
	f.KEV = kev != 0
	var err error
	if f.FirstSeen, err = parseTime(first); err != nil {
		return "", StoredFinding{}, err
	}
	if f.LastSeen, err = parseTime(last); err != nil {
		return "", StoredFinding{}, err
	}
	return image, f, nil
}

// ImageFindings returns the stored findings of one image: the most severe
// first, then the fixable ones, then the exploited ones, then by package,
// advisory and version (the order vulns.Parse gives).
func (s *Store) ImageFindings(ctx context.Context, image string) ([]StoredFinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+findingColumns+` FROM image_findings WHERE image = ?
		ORDER BY `+severityOrder+`, (fix_state = 'fixed' AND fixed_in <> '') DESC, kev DESC, package, advisory, version`, image)
	if err != nil {
		return nil, fmt.Errorf("findings of %q: %w", image, err)
	}
	defer rows.Close()
	var out []StoredFinding
	for rows.Next() {
		_, f, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("findings of %q: %w", image, err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("findings of %q: %w", image, err)
	}
	return out, nil
}

// FixableFindings returns, by image, the stored findings of images that are at or
// above min and have a known fix, in the order of ImageFindings. It is what the
// needs-you list derives image_vulnerable from. Images with none are absent.
func (s *Store) FixableFindings(ctx context.Context, images []string, min vulns.Severity) (map[string][]StoredFinding, error) {
	out := map[string][]StoredFinding{}
	if len(images) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(images)), ", ")
	args := make([]any, len(images))
	for i, img := range images {
		args[i] = img
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+findingColumns+` FROM image_findings
		WHERE fix_state = 'fixed' AND fixed_in <> '' AND image IN (`+marks+`)
		ORDER BY image, `+severityOrder+`, kev DESC, package, advisory, version`, args...)
	if err != nil {
		return nil, fmt.Errorf("fixable findings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		image, f, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("fixable findings: %w", err)
		}
		if f.Severity >= min {
			out[image] = append(out[image], f)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fixable findings: %w", err)
	}
	return out, nil
}

// PruneImageScans deletes the stored scans (and their findings) of every image
// not in keep, and returns how many. Runs aren't touched.
func (s *Store) PruneImageScans(ctx context.Context, keep []string) (int, error) {
	all, err := s.ListImageScans(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, i := range all {
		if slices.Contains(keep, i.Image) {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM image_scans WHERE image = ?`, i.Image); err != nil {
			return n, fmt.Errorf("prune scan of %q: %w", i.Image, err)
		}
		n++
	}
	return n, nil
}

// ForgetScans deletes every stored scan, finding and run, the day of the last
// daily pass and the notifier's memory of image_vulnerable items: what the
// removal of the scanner leaves nothing of. Settings stay.
func (s *Store) ForgetScans(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("forget scans: %w", err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM image_scans`, // the findings go by ON DELETE CASCADE
		`DELETE FROM scan_runs`,
		`UPDATE scan_settings SET last_daily = ''`,
		// The needs-you items of this kind end with the scanner, without a "resolved".
		`DELETE FROM sent_problems WHERE kind = '` + ProblemImageVulnerable + `'`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("forget scans: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("forget scans: %w", err)
	}
	return nil
}

// ScanSettings are the owner's settings for scanning.
type ScanSettings struct {
	// NotifyLevel is the lowest severity that opens a needs-you item: "critical"
	// or "high".
	NotifyLevel string
	// RefuseFixableCritical makes a deploy of an image with a fixable critical
	// finding fail.
	RefuseFixableCritical bool
	// DailyHour is the UTC hour the daily rescan starts at or after, 0 to 23.
	DailyHour int
	// DatabaseURL is the scanner's database update URL; empty is the scanner's own.
	DatabaseURL string
	// LastDaily is when the daily pass last ran; zero before the first.
	LastDaily time.Time
}

// GetScanSettings returns the settings (the defaults before the owner saved any).
func (s *Store) GetScanSettings(ctx context.Context) (ScanSettings, error) {
	var set ScanSettings
	var refuse int
	var last string
	err := s.db.QueryRowContext(ctx, `SELECT notify_level, refuse_fixable_critical, daily_hour, database_url, last_daily
		FROM scan_settings WHERE id = 1`).Scan(&set.NotifyLevel, &refuse, &set.DailyHour, &set.DatabaseURL, &last)
	if err != nil {
		return ScanSettings{}, fmt.Errorf("scan settings: %w", err)
	}
	set.RefuseFixableCritical = refuse != 0
	if set.LastDaily, err = parseTimeOrZero(last); err != nil {
		return ScanSettings{}, fmt.Errorf("scan settings: %w", err)
	}
	return set, nil
}

// SaveScanSettings writes the settings the owner chooses. LastDaily is left
// alone: SetLastDaily writes it.
func (s *Store) SaveScanSettings(ctx context.Context, set ScanSettings) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE scan_settings SET notify_level = ?, refuse_fixable_critical = ?,
		daily_hour = ?, database_url = ? WHERE id = 1`,
		set.NotifyLevel, boolInt(set.RefuseFixableCritical), set.DailyHour, set.DatabaseURL); err != nil {
		return fmt.Errorf("save scan settings: %w", err)
	}
	return nil
}

// SetLastDaily records when the daily pass ran.
func (s *Store) SetLastDaily(ctx context.Context, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE scan_settings SET last_daily = ? WHERE id = 1`, formatTime(at)); err != nil {
		return fmt.Errorf("set last daily scan: %w", err)
	}
	return nil
}
