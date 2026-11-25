package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Reading the audit log, its retention and the prune pass (feature 057, plan D10, D11, D13). Owners
// and admins read and export; owners change the retention. No function here edits or deletes a single
// entry.

const (
	// DefaultAuditRetentionDays, MinAuditRetentionDays and MaxAuditRetentionDays are the retention's
	// default and bounds.
	DefaultAuditRetentionDays = 90
	MinAuditRetentionDays     = 30
	MaxAuditRetentionDays     = 3650

	// defaultAuditFirstGap and defaultAuditGap are when the prune pass first runs after Start and how
	// often it runs after that.
	defaultAuditFirstGap = time.Minute
	defaultAuditGap      = 24 * time.Hour
)

// AuditQuery is what the log is filtered by. Empty fields don't filter. Actor is an email or a token
// name, exactly. From and To are inclusive. Before is the cursor, an entry ID: only older entries.
// Limit is the page size, 1 to 500 (100 when zero).
type AuditQuery struct {
	Actor, Action, Kind, Resource string
	Project, Environment          string
	From, To                      time.Time
	Before                        int64
	Limit                         int
}

// AuditPage is one page of entries, newest first. Next is the Before of the next page, 0 on the last.
type AuditPage struct {
	Events []audit.Event
	Next   int64
}

// AuditSettings is the owner's setting of the audit log.
type AuditSettings struct {
	RetentionDays int `json:"retentionDays"`
}

func auditField(field, format string, args ...any) *app.FieldError {
	return &app.FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// validate refuses a filter value that can't match anything, naming the parameter.
func (q AuditQuery) validate() error {
	if q.Action != "" && !audit.ValidAction(audit.Action(q.Action)) {
		return auditField("action", "use one of the actions of the audit log, such as deployed or deleted")
	}
	if q.Kind != "" && !audit.ValidKind(audit.Kind(q.Kind)) {
		return auditField("kind", "use one of the resource kinds of the audit log, such as app or project")
	}
	if q.Environment != "" && q.Project == "" {
		return auditField("environment", "needs a project")
	}
	if q.Limit < 0 || q.Limit > store.MaxAuditLimit {
		return auditField("limit", "must be from 1 to %d", store.MaxAuditLimit)
	}
	if q.Before < 0 {
		return auditField("before", "must be an entry ID")
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.To.Before(q.From) {
		return auditField("to", "must not be before from")
	}
	return nil
}

func (q AuditQuery) filter() store.AuditFilter {
	return store.AuditFilter{Actor: q.Actor, Action: q.Action, Kind: q.Kind, Resource: q.Resource,
		Project: q.Project, Environment: q.Environment, From: q.From, To: q.To, Before: q.Before, Limit: q.Limit}
}

// AuditEvents is a page of the log. Owners and admins only.
func (s *Service) AuditEvents(ctx context.Context, q AuditQuery) (AuditPage, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return AuditPage{}, err
	}
	if err := q.validate(); err != nil {
		return AuditPage{}, err
	}
	events, more, err := s.store.ListAuditEvents(ctx, q.filter())
	if err != nil {
		return AuditPage{}, err
	}
	page := AuditPage{Events: events}
	if more && len(events) > 0 {
		page.Next = events[len(events)-1].ID
	}
	return page, nil
}

// ExportAuditEvents calls fn for every entry in the range, oldest first. The range (From and To) is
// required; more than store.MaxAuditExportRows entries is a field error on from. Owners and admins only.
func (s *Service) ExportAuditEvents(ctx context.Context, q AuditQuery, fn func(audit.Event) error) error {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return err
	}
	if q.From.IsZero() {
		return auditField("from", "is required")
	}
	if q.To.IsZero() {
		return auditField("to", "is required")
	}
	if q.Before != 0 || q.Limit != 0 {
		return auditField("limit", "an export has no page: leave before and limit out")
	}
	if err := q.validate(); err != nil {
		return err
	}
	err := s.store.ExportAuditEvents(ctx, q.filter(), fn)
	if errors.Is(err, store.ErrAuditExportTooLarge) {
		return auditField("from", "the range holds more than %d entries: narrow the range", store.MaxAuditExportRows)
	}
	return err
}

// AuditSettings reads the retention. Owners and admins only.
func (s *Service) AuditSettings(ctx context.Context) (AuditSettings, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return AuditSettings{}, err
	}
	return s.auditSettings(ctx)
}

func (s *Service) auditSettings(ctx context.Context) (AuditSettings, error) {
	st, err := s.store.AuditSettings(ctx)
	if err != nil {
		return AuditSettings{}, err
	}
	return AuditSettings{RetentionDays: st.RetentionDays}, nil
}

// SetAuditSettings changes the retention, 30 to 3650 days. Owners only. The change is itself an entry.
func (s *Service) SetAuditSettings(ctx context.Context, in AuditSettings) (AuditSettings, error) {
	if err := s.Allow(ctx, NeedOwner(), Target{}); err != nil {
		return AuditSettings{}, err
	}
	if in.RetentionDays < MinAuditRetentionDays || in.RetentionDays > MaxAuditRetentionDays {
		return AuditSettings{}, auditField("retentionDays", "must be from %d to %d days", MinAuditRetentionDays, MaxAuditRetentionDays)
	}
	before, err := s.auditSettings(ctx)
	if err != nil {
		return AuditSettings{}, err
	}
	if err := s.store.SetAuditSettings(ctx, store.AuditSettings{RetentionDays: in.RetentionDays}); err != nil {
		return AuditSettings{}, err
	}
	s.audit(ctx, audit.Entry{
		Action:   audit.ActionUpdated,
		Resource: audit.Resource{Kind: audit.KindSettings, ID: "audit", Name: "Audit log"},
		Changes:  audit.Diff(before, in),
	})
	return in, nil
}

// SetAuditPruneGap sets when the prune pass first runs after Start and how often after that (tests
// shorten them). Call it before Start.
func (s *Service) SetAuditPruneGap(first, every time.Duration) {
	s.auditFirstGap, s.auditGap = first, every
}

// runAuditPrune deletes the entries older than the retention: first after firstGap, then every gap.
// Pruning is Caravana's housekeeping, not a change anyone made, so it notes nothing.
func (s *Service) runAuditPrune(ctx context.Context, firstGap, gap time.Duration) {
	timer := time.NewTimer(firstGap)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if n, err := s.pruneAudit(ctx); err != nil {
			s.log.Error("audit: prune failed", "error", err)
		} else if n > 0 {
			s.log.Info("audit: pruned old entries", "entries", n)
		}
		timer.Reset(gap)
	}
}

// pruneAudit deletes the entries older than the retention and returns how many.
func (s *Service) pruneAudit(ctx context.Context) (int64, error) {
	st, err := s.auditSettings(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := s.clock.Now().Add(-time.Duration(st.RetentionDays) * 24 * time.Hour)
	return s.store.PruneAuditEvents(ctx, cutoff)
}
