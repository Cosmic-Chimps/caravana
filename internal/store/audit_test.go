package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/audit"
)

var auditT0 = time.Date(2026, 11, 25, 10, 0, 0, 123456789, time.UTC)

func auditEntry(actor, action, kind, id string) audit.Entry {
	return audit.Entry{
		Actor:     audit.Actor{Kind: audit.ActorPerson, Email: actor, PersonID: "p1"},
		Door:      audit.DoorAPI,
		Address:   "203.0.113.4",
		Action:    audit.Action(action),
		Operation: "POST /v1/x",
		Resource:  audit.Resource{Kind: audit.Kind(kind), ID: id, Name: id, Project: "shop", Environment: "staging"},
	}
}

func TestAuditInsertAndRead(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	want := audit.Entry{
		Actor:     audit.Actor{Kind: audit.ActorDeployToken, Email: "ana@example.com", Token: "ci", PersonID: "01J"},
		Door:      audit.DoorMCP,
		Address:   "203.0.113.4",
		Action:    audit.ActionDeployed,
		Operation: "POST /v1/apps/{id}/deployments",
		Resource:  audit.Resource{Kind: audit.KindApp, ID: "shop--staging--web", Name: "web", Project: "shop", Environment: "staging"},
		Changes: []audit.Change{
			{Path: "services[web].image", Op: audit.OpChanged, From: "a", To: "b"},
			{Path: "services[web].secrets[X].value", Op: audit.OpChanged},
		},
		Record:   &audit.Record{Kind: audit.RecordDeployment, ID: "01JD"},
		Related:  []audit.Related{{Kind: audit.KindDomain, ID: "new.example.com", Action: audit.ActionCreated}},
		Fallback: true,
	}
	id, err := s.InsertAuditEvent(ctx, auditT0, want)
	if err != nil || id < 1 {
		t.Fatalf("InsertAuditEvent = %d, %v", id, err)
	}
	got, more, err := s.ListAuditEvents(ctx, AuditFilter{})
	if err != nil || more || len(got) != 1 {
		t.Fatalf("ListAuditEvents = %v, %v, %v", got, more, err)
	}
	if got[0].ID != id || !got[0].At.Equal(auditT0) {
		t.Errorf("id/at = %d %v, want %d %v", got[0].ID, got[0].At, id, auditT0)
	}
	if !reflect.DeepEqual(got[0].Entry, want) {
		t.Errorf("entry =\n  %+v\nwant\n  %+v", got[0].Entry, want)
	}

	// A bare entry reads back with no nil changes (the API says [], not null), no record and no related.
	id2, err := s.InsertAuditEvent(ctx, time.Time{}, auditEntry("a@example.com", "created", "project", "shop"))
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.ListAuditEvents(ctx, AuditFilter{Before: id2 + 1, Limit: 1})
	if len(got) != 1 || got[0].ID != id2 || got[0].Changes == nil || len(got[0].Changes) != 0 || got[0].Record != nil || got[0].Related != nil || got[0].Fallback {
		t.Errorf("bare entry = %+v", got)
	}
}

func TestAuditListFiltersAndPages(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for i := 0; i < 7; i++ {
		e := auditEntry("ana@example.com", "updated", "app", fmt.Sprintf("app%d", i%2))
		if i%3 == 0 {
			e.Actor = audit.Actor{Kind: audit.ActorToken, Email: "bo@example.com", Token: "ci"}
			e.Action = audit.ActionDeployed
		}
		if i == 6 {
			e.Resource.Project, e.Resource.Environment = "other", "production"
			e.Resource.Kind = audit.KindDomain
		}
		if _, err := s.InsertAuditEvent(ctx, auditT0.Add(time.Duration(i)*time.Hour), e); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(f AuditFilter) []int64 {
		t.Helper()
		got, _, err := s.ListAuditEvents(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, e := range got {
			out = append(out, e.ID)
		}
		return out
	}
	for name, tc := range map[string]struct {
		f    AuditFilter
		want []int64
	}{
		"all, newest first":   {AuditFilter{}, []int64{7, 6, 5, 4, 3, 2, 1}},
		"actor by email":      {AuditFilter{Actor: "ana@example.com"}, []int64{6, 5, 3, 2}},
		"actor by token name": {AuditFilter{Actor: "ci"}, []int64{7, 4, 1}},
		"action":              {AuditFilter{Action: "deployed"}, []int64{7, 4, 1}},
		"kind":                {AuditFilter{Kind: "domain"}, []int64{7}},
		"resource":            {AuditFilter{Resource: "app1"}, []int64{6, 4, 2}},
		"project":             {AuditFilter{Project: "other"}, []int64{7}},
		"project and env":     {AuditFilter{Project: "shop", Environment: "staging"}, []int64{6, 5, 4, 3, 2, 1}},
		"from":                {AuditFilter{From: auditT0.Add(5 * time.Hour)}, []int64{7, 6}},
		"to":                  {AuditFilter{To: auditT0.Add(1 * time.Hour)}, []int64{2, 1}},
		"range, inclusive":    {AuditFilter{From: auditT0.Add(1 * time.Hour), To: auditT0.Add(2 * time.Hour)}, []int64{3, 2}},
		"before":              {AuditFilter{Before: 4}, []int64{3, 2, 1}},
		"two filters":         {AuditFilter{Actor: "ci", Kind: "app"}, []int64{4, 1}},
		"nothing matches":     {AuditFilter{Actor: "nobody"}, nil},
	} {
		if got := ids(tc.f); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: ids = %v, want %v", name, got, tc.want)
		}
	}

	// Paging by the cursor.
	page, more, err := s.ListAuditEvents(ctx, AuditFilter{Limit: 3})
	if err != nil || !more || len(page) != 3 || page[2].ID != 5 {
		t.Fatalf("page 1 = %v, %v, %v", page, more, err)
	}
	page, more, _ = s.ListAuditEvents(ctx, AuditFilter{Limit: 3, Before: page[2].ID})
	if !more || len(page) != 3 || page[0].ID != 4 {
		t.Fatalf("page 2 = %v, %v", page, more)
	}
	page, more, _ = s.ListAuditEvents(ctx, AuditFilter{Limit: 3, Before: page[2].ID})
	if more || len(page) != 1 || page[0].ID != 1 {
		t.Fatalf("page 3 = %v, %v", page, more)
	}
	// The limit is capped.
	if got, _, _ := s.ListAuditEvents(ctx, AuditFilter{Limit: 100000}); len(got) != 7 {
		t.Errorf("a huge limit returned %d", len(got))
	}
	if n, err := s.CountAuditEvents(ctx, AuditFilter{Actor: "ci", Before: 2}); err != nil || n != 3 {
		t.Errorf("CountAuditEvents = %d, %v; want 3 (Before is ignored)", n, err)
	}
}

func TestAuditExportIsOldestFirstAndBounded(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for i := 0; i < 5; i++ {
		if _, err := s.InsertAuditEvent(ctx, auditT0.Add(time.Duration(i)*time.Minute), auditEntry("a@example.com", "updated", "app", "x")); err != nil {
			t.Fatal(err)
		}
	}
	var got []int64
	err := s.ExportAuditEvents(ctx, AuditFilter{From: auditT0.Add(time.Minute), To: auditT0.Add(3 * time.Minute), Limit: 1, Before: 2}, func(e audit.Event) error {
		got = append(got, e.ID)
		return nil
	})
	if err != nil || !reflect.DeepEqual(got, []int64{2, 3, 4}) {
		t.Errorf("export = %v, %v; want 2 3 4 (Limit and Before are ignored)", got, err)
	}
	stop := errors.New("stop")
	if err := s.ExportAuditEvents(ctx, AuditFilter{}, func(audit.Event) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("an error from the callback = %v, want it returned", err)
	}
}

func TestAuditExportTooLarge(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	// Fill the table with one statement, a recursive CTE.
	if _, err := s.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i <= ?)
		INSERT INTO audit_events (at, actor_kind, door, action, resource_kind) SELECT '2026-11-25T10:00:00.000000000Z', 'person', 'api', 'updated', 'app' FROM n`,
		MaxAuditExportRows); err != nil {
		t.Fatal(err)
	}
	called := false
	err := s.ExportAuditEvents(ctx, AuditFilter{}, func(audit.Event) error { called = true; return nil })
	if !errors.Is(err, ErrAuditExportTooLarge) || called {
		t.Errorf("export of %d+1 rows = %v (called %v), want ErrAuditExportTooLarge before any row", MaxAuditExportRows, err, called)
	}
	if err := s.ExportAuditEvents(ctx, AuditFilter{From: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}, func(audit.Event) error { return nil }); err != nil {
		t.Errorf("a narrower range = %v", err)
	}
}

func TestAuditPrune(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for i := 0; i < 4; i++ {
		if _, err := s.InsertAuditEvent(ctx, auditT0.Add(time.Duration(i)*24*time.Hour), auditEntry("a@example.com", "updated", "app", "x")); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneAuditEvents(ctx, auditT0.Add(2*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("PruneAuditEvents = %d, %v; want 2", n, err)
	}
	got, _, _ := s.ListAuditEvents(ctx, AuditFilter{})
	if len(got) != 2 || got[0].ID != 4 || got[1].ID != 3 {
		t.Errorf("left = %+v", got)
	}
	// An ID is never reused after a prune: the cursor stays right.
	if _, err := s.PruneAuditEvents(ctx, auditT0.Add(100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	id, err := s.InsertAuditEvent(ctx, auditT0.Add(101*24*time.Hour), auditEntry("a@example.com", "updated", "app", "x"))
	if err != nil || id != 5 {
		t.Errorf("the ID after a full prune = %d, %v; want 5", id, err)
	}
}

func TestAuditSettings(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if got, err := s.AuditSettings(ctx); err != nil || got.RetentionDays != 90 {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	for _, days := range []int{30, 3650, 365} {
		if err := s.SetAuditSettings(ctx, AuditSettings{RetentionDays: days}); err != nil {
			t.Errorf("%d days: %v", days, err)
		}
		if got, _ := s.AuditSettings(ctx); got.RetentionDays != days {
			t.Errorf("after saving %d: %+v", days, got)
		}
	}
	for _, days := range []int{0, 29, 3651, -1} {
		if err := s.SetAuditSettings(ctx, AuditSettings{RetentionDays: days}); err == nil {
			t.Errorf("%d days was stored", days)
		}
	}
	if got, _ := s.AuditSettings(ctx); got.RetentionDays != 365 {
		t.Errorf("a refused save changed the settings: %+v", got)
	}
}

// An entry outlives what it is about: the table has no foreign key.
func TestAuditHasNoForeignKeys(t *testing.T) {
	s := openTestStore(t)
	rows, err := s.db.QueryContext(context.Background(), `PRAGMA foreign_key_list(audit_events)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("audit_events has a foreign key")
	}
	var sqlText string
	if err := s.db.QueryRowContext(context.Background(), `SELECT sql FROM sqlite_master WHERE name = 'audit_events'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "STRICT") {
		t.Errorf("audit_events isn't STRICT: %s", sqlText)
	}
}
