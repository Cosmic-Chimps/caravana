package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The Operator view's records (feature 027): what happened to the host and to
// Caravana, the history of the needs-you list, the release check's one row and
// small marks that must survive a restart. None of it holds a secret value, an
// env value or command output.

// The kinds a host event can be of.
const (
	HostEventBoot       = "boot"        // the host started; Key is boot|<boot time>
	HostEventVersion    = "version"     // Caravana changed version; Key is version|<version>
	HostEventOSPackages = "os_packages" // a run of the OS's package manager; Key is os|<run start>
)

// keepEvents is how many host events and problem events are kept.
const keepEvents = 200

// HostEvent is one thing that happened to the host or to Caravana.
type HostEvent struct {
	ID   int64
	Kind string // HostEventBoot, HostEventVersion or HostEventOSPackages
	// At is the event's own time: the boot time, the start time, the start of
	// the package run.
	At time.Time
	// Detail is JSON the service writes and reads, for example
	// {"from":"0.14.0","to":"0.15.0"}.
	Detail string
	// Key stops the same event being recorded twice. Empty means no dedup.
	Key string
}

// AppendHostEvent records an event and prunes the table to the newest 200. It
// returns false, and changes nothing, when an event with the same key is
// already recorded.
func (s *Store) AppendHostEvent(ctx context.Context, e HostEvent) (bool, error) {
	var key any // NULL for no key: several rows may have none
	if e.Key != "" {
		key = e.Key
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("append host event %q: %w", e.Kind, err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	res, err := tx.ExecContext(ctx, `INSERT INTO host_events (kind, at, detail, key) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO NOTHING`, // not OR IGNORE: that would swallow a bad kind too
		e.Kind, formatTime(e.At), e.Detail, key)
	if err != nil {
		return false, fmt.Errorf("append host event %q: %w", e.Kind, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("append host event %q: %w", e.Kind, err)
	}
	if n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM host_events WHERE id NOT IN (
			SELECT id FROM host_events ORDER BY at DESC, id DESC LIMIT ?)`, keepEvents); err != nil {
		return false, fmt.Errorf("append host event %q: prune: %w", e.Kind, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("append host event %q: %w", e.Kind, err)
	}
	return true, nil
}

// HostEvents returns up to limit events, newest first. kind limits it to one
// kind; empty means every kind.
func (s *Store) HostEvents(ctx context.Context, kind string, limit int) ([]HostEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, at, detail, COALESCE(key, '') FROM host_events
		WHERE ? = '' OR kind = ? ORDER BY at DESC, id DESC LIMIT ?`, kind, kind, limit)
	if err != nil {
		return nil, fmt.Errorf("list host events: %w", err)
	}
	defer rows.Close()
	var out []HostEvent
	for rows.Next() {
		var e HostEvent
		var at string
		if err := rows.Scan(&e.ID, &e.Kind, &at, &e.Detail, &e.Key); err != nil {
			return nil, fmt.Errorf("list host events: %w", err)
		}
		if e.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("list host events: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list host events: %w", err)
	}
	return out, nil
}

// The changes a problem event records.
const (
	ProblemOpened   = "opened"
	ProblemWorsened = "worsened"
	ProblemResolved = "resolved"
)

// ProblemEvent is one time the needs-you list got a problem, made one worse or
// lost one. ApplyProblemCheck writes them with the table of sent problems.
type ProblemEvent struct {
	ID     int64
	Key    string // the sent problem's key
	Kind   string // the attention kind
	AppID  string // empty for an installation-wide problem
	Change string // ProblemOpened, ProblemWorsened or ProblemResolved
	At     time.Time
	// Summary is the title that was sent for the problem.
	Summary string
	// Sentence is the needs-you list's own sentence for it, as JSON parts
	// ([{"text","mono"}]); empty for an event written before the column.
	Sentence string
}

// appendProblemEvent writes one event inside the check's transaction.
func appendProblemEvent(ctx context.Context, tx *sql.Tx, e ProblemEvent) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO problem_events (key, kind, app_id, change, at, summary, sentence) VALUES (?, ?, ?, ?, ?, ?, ?)",
		e.Key, e.Kind, e.AppID, e.Change, formatTime(e.At), e.Summary, e.Sentence)
	return err
}

// pruneProblemEvents keeps the newest 200 problem events.
func pruneProblemEvents(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM problem_events WHERE id NOT IN (
			SELECT id FROM problem_events ORDER BY at DESC, id DESC LIMIT ?)`, keepEvents)
	return err
}

// ProblemEvents returns up to limit problem events, newest first.
func (s *Store) ProblemEvents(ctx context.Context, limit int) ([]ProblemEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, key, kind, app_id, change, at, summary, sentence FROM problem_events
		ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list problem events: %w", err)
	}
	defer rows.Close()
	var out []ProblemEvent
	for rows.Next() {
		var e ProblemEvent
		var at string
		if err := rows.Scan(&e.ID, &e.Key, &e.Kind, &e.AppID, &e.Change, &at, &e.Summary, &e.Sentence); err != nil {
			return nil, fmt.Errorf("list problem events: %w", err)
		}
		if e.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("list problem events: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list problem events: %w", err)
	}
	return out, nil
}

// ReleaseCheck is the one row about Caravana's own releases: the owner's
// setting and what the last check found. A field the check hasn't filled is
// empty (a zero time, an empty string).
type ReleaseCheck struct {
	// Enabled is the owner's setting; false means no request is made at all.
	// A new store has it on.
	Enabled     bool
	LatestTag   string // v0.16.0
	PublishedAt time.Time
	URL         string // the release page
	Body        string // the release notes, capped by the writer at 16 KiB
	NomadPin    string
	TraefikPin  string
	ETag        string
	CheckedAt   time.Time // the last attempt, good or not
	// CheckedOKAt is the last attempt GitHub answered well: it stays when a later one fails, so the
	// page can say how long ago the release list was last known to be current.
	CheckedOKAt time.Time
	// Error is the last failure in Caravana's words; empty when it worked.
	Error string
	// NotifiedTag is the last tag a caravana.release message was queued for.
	NotifiedTag string
	// AutoUpdate is the owner's setting for automatic patch updates (feature 030) and
	// AutoUpdateHour the UTC hour (0-23) they are requested at. A new store has it off, at 3.
	AutoUpdate     bool
	AutoUpdateHour int
}

// ReleaseCheck returns the release row. It always exists: the migration makes
// it, with Enabled on.
func (s *Store) ReleaseCheck(ctx context.Context) (ReleaseCheck, error) {
	var rc ReleaseCheck
	var enabled, auto int
	var published, checked, checkedOK string
	err := s.db.QueryRowContext(ctx, `
		SELECT enabled, latest_tag, published_at, url, body, nomad_pin, traefik_pin, etag, checked_at, checked_ok_at, error, notified_tag,
		       auto_update, auto_update_hour
		FROM release_check WHERE id = 1`).Scan(&enabled, &rc.LatestTag, &published, &rc.URL, &rc.Body,
		&rc.NomadPin, &rc.TraefikPin, &rc.ETag, &checked, &checkedOK, &rc.Error, &rc.NotifiedTag,
		&auto, &rc.AutoUpdateHour)
	if err != nil {
		return ReleaseCheck{}, fmt.Errorf("read release check: %w", err)
	}
	rc.Enabled = enabled == 1
	rc.AutoUpdate = auto == 1
	if rc.PublishedAt, err = parseTimeOrZero(published); err != nil {
		return ReleaseCheck{}, fmt.Errorf("read release check: %w", err)
	}
	if rc.CheckedAt, err = parseTimeOrZero(checked); err != nil {
		return ReleaseCheck{}, fmt.Errorf("read release check: %w", err)
	}
	if rc.CheckedOKAt, err = parseTimeOrZero(checkedOK); err != nil {
		return ReleaseCheck{}, fmt.Errorf("read release check: %w", err)
	}
	return rc, nil
}

// SaveReleaseCheck replaces the release row with rc.
func (s *Store) SaveReleaseCheck(ctx context.Context, rc ReleaseCheck) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE release_check SET enabled = ?, latest_tag = ?, published_at = ?, url = ?, body = ?,
			nomad_pin = ?, traefik_pin = ?, etag = ?, checked_at = ?, checked_ok_at = ?, error = ?, notified_tag = ?,
			auto_update = ?, auto_update_hour = ?
		WHERE id = 1`,
		boolInt(rc.Enabled), rc.LatestTag, zeroOrTime(rc.PublishedAt), rc.URL, rc.Body,
		rc.NomadPin, rc.TraefikPin, rc.ETag, zeroOrTime(rc.CheckedAt), zeroOrTime(rc.CheckedOKAt), rc.Error, rc.NotifiedTag,
		boolInt(rc.AutoUpdate), rc.AutoUpdateHour)
	if err != nil {
		return fmt.Errorf("save release check: %w", err)
	}
	return nil
}

// SetReleaseCheckEnabled stores the owner's setting and touches nothing else, so
// it can't undo what a check that is running writes (SaveReleaseResult).
func (s *Store) SetReleaseCheckEnabled(ctx context.Context, enabled bool) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE release_check SET enabled = ? WHERE id = 1", boolInt(enabled)); err != nil {
		return fmt.Errorf("set release check: %w", err)
	}
	return nil
}

// SaveReleaseResult stores what a check found: every column of the row but the
// owner's setting, which is only ever changed by SetReleaseCheckEnabled, so a
// setting saved while a check is running isn't undone by it. deliveries are
// queued in the same transaction (the message about a new release): the row
// that says "notified" and the messages are written together or not at all.
func (s *Store) SaveReleaseResult(ctx context.Context, rc ReleaseCheck, deliveries []Delivery) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save release check: %w", err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if _, err := tx.ExecContext(ctx, `
		UPDATE release_check SET latest_tag = ?, published_at = ?, url = ?, body = ?,
			nomad_pin = ?, traefik_pin = ?, etag = ?, checked_at = ?, checked_ok_at = ?, error = ?, notified_tag = ?
		WHERE id = 1`,
		rc.LatestTag, zeroOrTime(rc.PublishedAt), rc.URL, rc.Body,
		rc.NomadPin, rc.TraefikPin, rc.ETag, zeroOrTime(rc.CheckedAt), zeroOrTime(rc.CheckedOKAt), rc.Error, rc.NotifiedTag); err != nil {
		return fmt.Errorf("save release check: %w", err)
	}
	for _, d := range deliveries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries (`+deliveryColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.ChannelID, d.EventType, d.Title, d.Payload, d.Status, d.Attempts,
			formatTime(d.NextAt), d.Error, formatTime(d.CreatedAt), optionalTime(d.FinishedAt)); err != nil {
			return fmt.Errorf("save release check: queue delivery %q: %w", d.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save release check: %w", err)
	}
	return nil
}

// parseTimeOrZero reads a stored time, where empty text is the zero time.
func parseTimeOrZero(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return parseTime(s)
}

// zeroOrTime is the stored text of t: empty for the zero time.
func zeroOrTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatTime(t)
}

// The keys of the host marks.
const (
	// MarkSecuritySince is when the count of pending security updates first
	// went above zero.
	MarkSecuritySince = "security_since"
)

// HostMark returns the time stored under key, and false when there is none.
func (s *Store) HostMark(ctx context.Context, key string) (time.Time, bool, error) {
	var at string
	err := s.db.QueryRowContext(ctx, "SELECT at FROM host_marks WHERE key = ?", key).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read host mark %q: %w", key, err)
	}
	t, err := parseTime(at)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read host mark %q: %w", key, err)
	}
	return t, true, nil
}

// SetHostMark stores a time under key, replacing an earlier one.
func (s *Store) SetHostMark(ctx context.Context, key string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO host_marks (key, at) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET at = excluded.at`, key, formatTime(at))
	if err != nil {
		return fmt.Errorf("set host mark %q: %w", key, err)
	}
	return nil
}

// DeleteHostMark removes a mark. A key with none is not an error.
func (s *Store) DeleteHostMark(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM host_marks WHERE key = ?", key); err != nil {
		return fmt.Errorf("delete host mark %q: %w", key, err)
	}
	return nil
}
