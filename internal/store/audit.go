package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/audit"
)

// The audit log (feature 057): audit_events, one row per change, and audit_settings, the owner's
// retention. Entries are only inserted and pruned by age: no function edits or deletes one.

const (
	// DefaultAuditLimit and MaxAuditLimit are the page sizes of ListAuditEvents.
	DefaultAuditLimit = 100
	MaxAuditLimit     = 500
	// MaxAuditExportRows is how many entries one export may hold.
	MaxAuditExportRows = 100000
)

// ErrAuditExportTooLarge means the range holds more than MaxAuditExportRows entries.
var ErrAuditExportTooLarge = errors.New("the range holds too many audit entries")

// InsertAuditEvent stores an entry written at at (the store's clock when at is zero) and returns
// its ID.
func (s *Store) InsertAuditEvent(ctx context.Context, at time.Time, e audit.Entry) (int64, error) {
	if at.IsZero() {
		at = s.now()
	}
	changes, err := json.Marshal(nonNilChanges(e.Changes))
	if err != nil {
		return 0, fmt.Errorf("encode audit changes: %w", err)
	}
	related, err := json.Marshal(nonNilRelated(e.Related))
	if err != nil {
		return 0, fmt.Errorf("encode audit related: %w", err)
	}
	var recKind, recID string
	if e.Record != nil {
		recKind, recID = string(e.Record.Kind), e.Record.ID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit_events
		(at, actor_kind, actor_email, actor_token, actor_person, door, address, action, operation,
		 resource_kind, resource_id, resource_name, project, environment, changes, record_kind, record_id, related, fallback)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTime(at), string(e.Actor.Kind), e.Actor.Email, e.Actor.Token, e.Actor.PersonID, string(e.Door), e.Address,
		string(e.Action), e.Operation, string(e.Resource.Kind), e.Resource.ID, e.Resource.Name, e.Resource.Project,
		e.Resource.Environment, string(changes), recKind, recID, string(related), boolInt(e.Fallback))
	if err != nil {
		return 0, fmt.Errorf("insert audit event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert audit event: %w", err)
	}
	return id, nil
}

func nonNilChanges(c []audit.Change) []audit.Change {
	if c == nil {
		return []audit.Change{}
	}
	return c
}

func nonNilRelated(r []audit.Related) []audit.Related {
	if r == nil {
		return []audit.Related{}
	}
	return r
}

// AuditFilter says which entries to read. Zero fields don't filter. Actor matches an email or a
// token name exactly. From and To are inclusive. Before is the cursor: only entries with a smaller
// ID. Limit is the page size (DefaultAuditLimit when zero, at most MaxAuditLimit).
type AuditFilter struct {
	Actor       string
	Action      string
	Kind        string
	Resource    string
	Project     string
	Environment string
	From, To    time.Time
	Before      int64
	Limit       int
}

// where is the filter's WHERE clause (with the leading " WHERE " when there is one) and its arguments.
func (f AuditFilter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, a ...any) {
		conds = append(conds, cond)
		args = append(args, a...)
	}
	if f.Actor != "" {
		add("(actor_email = ? OR actor_token = ?)", f.Actor, f.Actor)
	}
	if f.Action != "" {
		add("action = ?", f.Action)
	}
	if f.Kind != "" {
		add("resource_kind = ?", f.Kind)
	}
	if f.Resource != "" {
		add("resource_id = ?", f.Resource)
	}
	if f.Project != "" {
		add("project = ?", f.Project)
	}
	if f.Environment != "" {
		add("environment = ?", f.Environment)
	}
	if !f.From.IsZero() {
		add("at >= ?", formatTime(f.From))
	}
	if !f.To.IsZero() {
		add("at <= ?", formatTime(f.To))
	}
	if f.Before > 0 {
		add("id < ?", f.Before)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const auditColumns = `id, at, actor_kind, actor_email, actor_token, actor_person, door, address, action, operation,
	resource_kind, resource_id, resource_name, project, environment, changes, record_kind, record_id, related, fallback`

// ListAuditEvents returns a page of entries, newest first, and whether older ones match too.
func (s *Store) ListAuditEvents(ctx context.Context, f AuditFilter) ([]audit.Event, bool, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultAuditLimit
	}
	limit = min(limit, MaxAuditLimit)
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT `+auditColumns+` FROM audit_events`+where+` ORDER BY id DESC LIMIT ?`,
		append(args, limit+1)...)
	if err != nil {
		return nil, false, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	var out []audit.Event
	for rows.Next() {
		e, err := scanAuditEvent(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list audit events: %w", err)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// CountAuditEvents is how many entries match the filter's fields (Before and Limit are ignored).
func (s *Store) CountAuditEvents(ctx context.Context, f AuditFilter) (int, error) {
	f.Before = 0
	where, args := f.where()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count audit events: %w", err)
	}
	return n, nil
}

// ExportAuditEvents calls fn for each entry that matches, oldest first, and stops at the first error
// fn returns. A range of more than MaxAuditExportRows entries is ErrAuditExportTooLarge, before fn
// is called. Before and Limit are ignored.
func (s *Store) ExportAuditEvents(ctx context.Context, f AuditFilter, fn func(audit.Event) error) error {
	f.Before, f.Limit = 0, 0
	n, err := s.CountAuditEvents(ctx, f)
	if err != nil {
		return err
	}
	if n > MaxAuditExportRows {
		return ErrAuditExportTooLarge
	}
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT `+auditColumns+` FROM audit_events`+where+` ORDER BY id ASC LIMIT ?`,
		append(args, MaxAuditExportRows)...)
	if err != nil {
		return fmt.Errorf("export audit events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanAuditEvent(rows)
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("export audit events: %w", err)
	}
	return nil
}

func scanAuditEvent(rows *sql.Rows) (audit.Event, error) {
	var (
		e                       audit.Event
		at, changes, related    string
		actorKind, door, action string
		kind, recKind           string
		fallback                int
	)
	var recID string
	if err := rows.Scan(&e.ID, &at, &actorKind, &e.Actor.Email, &e.Actor.Token, &e.Actor.PersonID, &door, &e.Address,
		&action, &e.Operation, &kind, &e.Resource.ID, &e.Resource.Name, &e.Resource.Project, &e.Resource.Environment,
		&changes, &recKind, &recID, &related, &fallback); err != nil {
		return audit.Event{}, fmt.Errorf("read an audit event: %w", err)
	}
	var err error
	if e.At, err = parseTime(at); err != nil {
		return audit.Event{}, fmt.Errorf("audit event %d: %w", e.ID, err)
	}
	e.Actor.Kind, e.Door, e.Action, e.Resource.Kind = audit.ActorKind(actorKind), audit.Door(door), audit.Action(action), audit.Kind(kind)
	e.Changes = []audit.Change{}
	if err := json.Unmarshal([]byte(changes), &e.Changes); err != nil {
		return audit.Event{}, fmt.Errorf("audit event %d: bad changes: %w", e.ID, err)
	}
	if err := json.Unmarshal([]byte(related), &e.Related); err != nil {
		return audit.Event{}, fmt.Errorf("audit event %d: bad related: %w", e.ID, err)
	}
	if len(e.Related) == 0 {
		e.Related = nil
	}
	if recKind != "" {
		e.Record = &audit.Record{Kind: audit.RecordKind(recKind), ID: recID}
	}
	e.Fallback = fallback != 0
	return e, nil
}

// PruneAuditEvents deletes the entries written before the given time and returns how many.
func (s *Store) PruneAuditEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_events WHERE at < ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("prune audit events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune audit events: %w", err)
	}
	return n, nil
}

// AuditSettings is the owner's one row of audit settings.
type AuditSettings struct {
	RetentionDays int // 30 to 3650, 90 by default
}

// AuditSettings reads the settings.
func (s *Store) AuditSettings(ctx context.Context) (AuditSettings, error) {
	var a AuditSettings
	if err := s.db.QueryRowContext(ctx, `SELECT retention_days FROM audit_settings WHERE id = 1`).Scan(&a.RetentionDays); err != nil {
		return AuditSettings{}, fmt.Errorf("read audit settings: %w", err)
	}
	return a, nil
}

// SetAuditSettings stores the retention in days. A number outside 30 to 3650 is refused by the table.
func (s *Store) SetAuditSettings(ctx context.Context, a AuditSettings) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_settings SET retention_days = ? WHERE id = 1`, a.RetentionDays); err != nil {
		return fmt.Errorf("save audit settings: %w", err)
	}
	return nil
}
