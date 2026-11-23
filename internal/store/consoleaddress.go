package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Console address changes (feature 051): one row per time the owner asked to move the console to
// another domain. A row holds a domain, the checks Caravana ran, a state and Caravana's own
// sentences; nothing secret.

// ErrConsoleChangeOpen is returned when a change is already checking, ready or switching.
var ErrConsoleChangeOpen = errors.New("a console address change is already open")

// The states of a console address change. checking, ready and switching are the open ones.
const (
	ConsoleChecking    = "checking"
	ConsoleReady       = "ready"
	ConsoleCheckFailed = "check_failed"
	ConsoleSwitching   = "switching"
	ConsoleApplied     = "applied"
	ConsoleFailed      = "failed"
	ConsoleRefused     = "refused"
	ConsoleLost        = "lost"
	ConsoleCancelled   = "cancelled"
)

// ConsoleChangeOpen says a change state is an open one.
func ConsoleChangeOpen(status string) bool {
	return status == ConsoleChecking || status == ConsoleReady || status == ConsoleSwitching
}

// ConsoleCheck is one check of a change, as the page shows it. Name is hostname, dns, route or
// certificate; State is ok, failed, waiting or skipped.
type ConsoleCheck struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
}

// ConsoleAddressChange is one stored change. A zero ReadyAt, SwitchAt, SettledAt or RedirectUntil
// is NULL: not reached yet.
type ConsoleAddressChange struct {
	ID               string // 32 lowercase hex characters
	Domain           string
	OldDomain        string
	RedirectDays     int // 0, 7, 30 or 90
	Status           string
	Checks           []ConsoleCheck
	Reason           string
	DNSRecordCreated bool
	CreatedAt        time.Time
	ReadyAt          time.Time
	SwitchAt         time.Time
	SettledAt        time.Time
	RedirectUntil    time.Time
}

// ConsoleOldDomain is a domain the console used to have, still routed and redirected until Until.
type ConsoleOldDomain struct {
	Domain string
	Until  time.Time
}

const consoleChangeColumns = `id, domain, old_domain, redirect_days, status, checks, reason, dns_record_created,
	created_at, ready_at, switch_at, settled_at, redirect_until`

// InsertConsoleAddressChange stores a new change. A zero CreatedAt is the store's clock, an empty
// Status is "checking" and no Checks is none. An ID in use is ErrAlreadyExists; a change that is
// already open is ErrConsoleChangeOpen.
func (s *Store) InsertConsoleAddressChange(ctx context.Context, c ConsoleAddressChange) (ConsoleAddressChange, error) {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.now()
	}
	if c.Status == "" {
		c.Status = ConsoleChecking
	}
	checks, err := encodeConsoleChecks(c.Checks)
	if err != nil {
		return ConsoleAddressChange{}, fmt.Errorf("insert console address change %q: %w", c.ID, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO console_address_changes (`+consoleChangeColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Domain, c.OldDomain, c.RedirectDays, c.Status, checks, c.Reason, boolInt(c.DNSRecordCreated),
		formatTime(c.CreatedAt), nullIfZero(c.ReadyAt), nullIfZero(c.SwitchAt), nullIfZero(c.SettledAt), nullIfZero(c.RedirectUntil))
	switch {
	case isUnique(err) && strings.Contains(err.Error(), "one_open"):
		return ConsoleAddressChange{}, fmt.Errorf("console address change %q: %w", c.ID, ErrConsoleChangeOpen)
	case isUnique(err):
		return ConsoleAddressChange{}, fmt.Errorf("console address change %q: %w", c.ID, ErrAlreadyExists)
	case err != nil:
		return ConsoleAddressChange{}, fmt.Errorf("insert console address change %q: %w", c.ID, err)
	}
	return c, nil
}

// ConsoleAddressChange returns one change by ID, or ErrNotFound.
func (s *Store) ConsoleAddressChange(ctx context.Context, id string) (ConsoleAddressChange, error) {
	c, err := scanConsoleChange(s.db.QueryRowContext(ctx, `SELECT `+consoleChangeColumns+` FROM console_address_changes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ConsoleAddressChange{}, fmt.Errorf("console address change %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return ConsoleAddressChange{}, fmt.Errorf("read console address change %q: %w", id, err)
	}
	return c, nil
}

// OpenConsoleAddressChange returns the change that is checking, ready or switching, and false when
// there is none. There is at most one.
func (s *Store) OpenConsoleAddressChange(ctx context.Context) (ConsoleAddressChange, bool, error) {
	c, err := scanConsoleChange(s.db.QueryRowContext(ctx, `SELECT `+consoleChangeColumns+` FROM console_address_changes
		WHERE status IN ('checking', 'ready', 'switching') LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return ConsoleAddressChange{}, false, nil
	}
	if err != nil {
		return ConsoleAddressChange{}, false, fmt.Errorf("read the open console address change: %w", err)
	}
	return c, true, nil
}

// LatestConsoleAddressChange returns the newest change in any state, and false when none was made.
func (s *Store) LatestConsoleAddressChange(ctx context.Context) (ConsoleAddressChange, bool, error) {
	c, err := scanConsoleChange(s.db.QueryRowContext(ctx, `SELECT `+consoleChangeColumns+` FROM console_address_changes
		ORDER BY created_at DESC, id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return ConsoleAddressChange{}, false, nil
	}
	if err != nil {
		return ConsoleAddressChange{}, false, fmt.Errorf("read the newest console address change: %w", err)
	}
	return c, true, nil
}

// UpdateConsoleAddressChange writes c's state, checks, reason, DNS flag and times over the stored
// row, but only while the stored status is one of from (a compare-and-set: the check goroutine,
// the owner's cancel and the pass all move a change, and the last one to arrive must not undo
// another). A stored status outside from is ErrInvalidTransition; an unknown ID is ErrNotFound.
// The domain, old domain, redirect days and creation time never change.
func (s *Store) UpdateConsoleAddressChange(ctx context.Context, c ConsoleAddressChange, from ...string) error {
	if len(from) == 0 {
		return fmt.Errorf("update console address change %q: no status to change from", c.ID)
	}
	checks, err := encodeConsoleChecks(c.Checks)
	if err != nil {
		return fmt.Errorf("update console address change %q: %w", c.ID, err)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(from)), ", ")
	args := []any{c.Status, checks, c.Reason, boolInt(c.DNSRecordCreated),
		nullIfZero(c.ReadyAt), nullIfZero(c.SwitchAt), nullIfZero(c.SettledAt), nullIfZero(c.RedirectUntil), c.ID}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE console_address_changes SET status = ?, checks = ?, reason = ?,
		dns_record_created = ?, ready_at = ?, switch_at = ?, settled_at = ?, redirect_until = ?
		WHERE id = ? AND status IN (`+marks+`)`, args...)
	if err != nil {
		return fmt.Errorf("update console address change %q: %w", c.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update console address change %q: %w", c.ID, err)
	}
	if n > 0 {
		return nil
	}
	// Nothing changed: either there is no such change or it is in another state.
	if _, err := s.ConsoleAddressChange(ctx, c.ID); err != nil {
		return err
	}
	return fmt.Errorf("update console address change %q: %w", c.ID, ErrInvalidTransition)
}

// ConsoleOldDomains returns the domains of applied changes whose redirect window ends after now,
// each once with its latest end, sorted by domain.
func (s *Store) ConsoleOldDomains(ctx context.Context, now time.Time) ([]ConsoleOldDomain, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT old_domain, redirect_until FROM console_address_changes
		WHERE status = 'applied' AND redirect_until > ?`, formatTime(now))
	if err != nil {
		return nil, fmt.Errorf("read the old console domains: %w", err)
	}
	defer rows.Close()
	latest := map[string]time.Time{}
	for rows.Next() {
		var domain, until string
		if err := rows.Scan(&domain, &until); err != nil {
			return nil, fmt.Errorf("read the old console domains: %w", err)
		}
		t, err := parseTime(until)
		if err != nil {
			return nil, fmt.Errorf("read the old console domains: %w", err)
		}
		if t.After(latest[domain]) {
			latest[domain] = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the old console domains: %w", err)
	}
	out := make([]ConsoleOldDomain, 0, len(latest))
	for d, t := range latest {
		out = append(out, ConsoleOldDomain{Domain: d, Until: t})
	}
	slices.SortFunc(out, func(a, b ConsoleOldDomain) int { return strings.Compare(a.Domain, b.Domain) })
	return out, nil
}

// ConsoleAddressApplied says some change was applied: the console has moved at least once, so the
// route's variable (not the Traefik job's own domain) is what names its address.
func (s *Store) ConsoleAddressApplied(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM console_address_changes WHERE status = 'applied'`).Scan(&n); err != nil {
		return false, fmt.Errorf("read the applied console address changes: %w", err)
	}
	return n > 0, nil
}

func encodeConsoleChecks(checks []ConsoleCheck) (string, error) {
	if checks == nil {
		checks = []ConsoleCheck{}
	}
	b, err := json.Marshal(checks)
	if err != nil {
		return "", fmt.Errorf("encode the checks: %w", err)
	}
	return string(b), nil
}

// scanConsoleChange reads one row of consoleChangeColumns.
func scanConsoleChange(row interface{ Scan(...any) error }) (ConsoleAddressChange, error) {
	var c ConsoleAddressChange
	var checks, created string
	var dns int
	var ready, switched, settled, until sql.NullString
	if err := row.Scan(&c.ID, &c.Domain, &c.OldDomain, &c.RedirectDays, &c.Status, &checks, &c.Reason, &dns,
		&created, &ready, &switched, &settled, &until); err != nil {
		return ConsoleAddressChange{}, err
	}
	c.DNSRecordCreated = dns != 0
	if err := json.Unmarshal([]byte(checks), &c.Checks); err != nil {
		return ConsoleAddressChange{}, fmt.Errorf("decode the checks: %w", err)
	}
	var err error
	if c.CreatedAt, err = parseTime(created); err != nil {
		return ConsoleAddressChange{}, err
	}
	for _, f := range []struct {
		from sql.NullString
		to   *time.Time
	}{{ready, &c.ReadyAt}, {switched, &c.SwitchAt}, {settled, &c.SettledAt}, {until, &c.RedirectUntil}} {
		if f.from.Valid {
			if *f.to, err = parseTime(f.from.String); err != nil {
				return ConsoleAddressChange{}, err
			}
		}
	}
	return c, nil
}
