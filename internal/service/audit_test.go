package service

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func auditRows(t *testing.T, st *store.Store) []audit.Event {
	t.Helper()
	got, _, err := st.ListAuditEvents(context.Background(), store.AuditFilter{Limit: store.MaxAuditLimit})
	if err != nil {
		t.Fatal(err)
	}
	// Oldest first reads best in a test.
	for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
		got[i], got[j] = got[j], got[i]
	}
	return got
}

func appNote(id string, action audit.Action, changes ...audit.Change) audit.Entry {
	return audit.Entry{Action: action, Resource: audit.Resource{Kind: audit.KindApp, ID: id, Name: "web", Project: "shop", Environment: "staging"}, Changes: changes}
}

func TestAuditOutsideAScopeWritesAtOnce(t *testing.T) {
	e := newEnv(t)
	ana := Principal{PersonID: "p1", Email: "ana@example.com", OrgRole: OrgAdmin}
	ci := ana
	ci.TokenName = "ci"
	deploy := ci
	deploy.Scope = &TokenScope{Project: "shop", App: "web"}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want audit.Actor
		door audit.Door
	}{
		{"a person", WithPrincipal(context.Background(), ana), audit.Actor{Kind: audit.ActorPerson, Email: "ana@example.com", PersonID: "p1"}, audit.DoorAPI},
		{"a token", WithPrincipal(context.Background(), ci), audit.Actor{Kind: audit.ActorToken, Email: "ana@example.com", Token: "ci", PersonID: "p1"}, audit.DoorAPI},
		{"a deploy token", WithPrincipal(context.Background(), deploy), audit.Actor{Kind: audit.ActorDeployToken, Email: "ana@example.com", Token: "ci", PersonID: "p1"}, audit.DoorAPI},
		{"no one is Caravana", context.Background(), audit.Actor{Kind: audit.ActorCaravana}, audit.DoorCaravana},
		{"the host's CLI", WithAuditDoor(context.Background(), audit.DoorHost), audit.Actor{Kind: audit.ActorHost}, audit.DoorHost},
		{"a webhook is its app's", WithAuditDoor(context.Background(), audit.DoorWebhook), audit.Actor{Kind: audit.ActorWebhook, Token: "shop--staging--web"}, audit.DoorWebhook},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(auditRows(t, e.store))
			e.svc.audit(tc.ctx, appNote("shop--staging--web", audit.ActionDeployed))
			rows := auditRows(t, e.store)
			if len(rows) != before+1 {
				t.Fatalf("%d entries, want %d", len(rows), before+1)
			}
			got := rows[len(rows)-1]
			if got.Actor != tc.want || got.Door != tc.door || got.Action != audit.ActionDeployed || got.Resource.ID != "shop--staging--web" {
				t.Errorf("entry = %+v; want actor %+v door %s", got, tc.want, tc.door)
			}
			if !got.At.Equal(e.svc.clock.Now()) {
				t.Errorf("at = %v, want the service's clock %v", got.At, e.svc.clock.Now())
			}
		})
	}
}

func TestAuditScopeMakesOneEntry(t *testing.T) {
	e := newEnv(t)
	ctx := WithPrincipal(context.Background(), Principal{PersonID: "p1", Email: "ana@example.com", OrgRole: OrgAdmin, TokenName: "ci"})
	ctx, sc := e.svc.BeginAudit(ctx, audit.DoorMCP, "203.0.113.4", "POST /v1/apps/{id}/deployments")

	e.svc.audit(ctx, appNote("shop--staging--web", audit.ActionDeployed, audit.Change{Path: "a", Op: audit.OpChanged, From: "1", To: "2"}))
	e.svc.audit(ctx, appNote("shop--staging--web", audit.ActionUpdated, audit.Change{Path: "b", Op: audit.OpAdded, To: "x"}))
	e.svc.audit(ctx, audit.Entry{Action: audit.ActionCreated, Resource: audit.Resource{Kind: audit.KindDomain, ID: "new.example.com"}})
	e.svc.audit(ctx, audit.Entry{Action: audit.ActionCreated, Resource: audit.Resource{Kind: audit.KindDomain, ID: "new.example.com"}})
	e.svc.audit(ctx, audit.Entry{Action: audit.ActionDeleted, Resource: audit.Resource{Kind: audit.KindDomain, ID: "old.example.com"}})
	e.svc.audit(ctx, audit.Entry{Action: audit.ActionUpdated, Resource: audit.Resource{Kind: audit.KindApp, ID: "shop--staging--web"},
		Record: &audit.Record{Kind: audit.RecordDeployment, ID: "dep1"}})
	if n := len(auditRows(t, e.store)); n != 0 {
		t.Fatalf("%d entries before Finish; a scope writes when it finishes", n)
	}
	sc.Finish(200, audit.Resource{})
	sc.Finish(200, audit.Resource{}) // again: nothing

	rows := auditRows(t, e.store)
	if len(rows) != 1 {
		t.Fatalf("%d entries, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	want := audit.Entry{
		Actor:     audit.Actor{Kind: audit.ActorToken, Email: "ana@example.com", Token: "ci", PersonID: "p1"},
		Door:      audit.DoorMCP,
		Address:   "203.0.113.4",
		Action:    audit.ActionDeployed,
		Operation: "POST /v1/apps/{id}/deployments",
		Resource:  audit.Resource{Kind: audit.KindApp, ID: "shop--staging--web", Name: "web", Project: "shop", Environment: "staging"},
		Changes: []audit.Change{
			{Path: "a", Op: audit.OpChanged, From: "1", To: "2"},
			{Path: "b", Op: audit.OpAdded, To: "x"},
		},
		Record: &audit.Record{Kind: audit.RecordDeployment, ID: "dep1"},
		Related: []audit.Related{
			{Kind: audit.KindDomain, ID: "new.example.com", Action: audit.ActionCreated},
			{Kind: audit.KindDomain, ID: "old.example.com", Action: audit.ActionDeleted},
		},
	}
	if !reflect.DeepEqual(got.Entry, want) {
		t.Errorf("entry =\n  %+v\nwant\n  %+v", got.Entry, want)
	}

	// A note after Finish (a goroutine that outlived the request) is written at once, with the same door and actor.
	e.svc.audit(ctx, appNote("shop--staging--web", audit.ActionRestarted))
	rows = auditRows(t, e.store)
	if len(rows) != 2 || rows[1].Action != audit.ActionRestarted || rows[1].Door != audit.DoorMCP || rows[1].Actor.Token != "ci" {
		t.Errorf("after Finish: %+v", rows)
	}
}

func TestAuditScopeActorFromAuthenticate(t *testing.T) {
	e := newEnv(t)
	// The scope was told by authenticate; the note's context carries no principal of its own.
	ctx, sc := e.svc.BeginAudit(context.Background(), audit.DoorAPI, "203.0.113.4", "DELETE /v1/apps/{id}")
	sc.SetActor(Principal{PersonID: "p9", Email: "bo@example.com", OrgRole: OrgMember})
	e.svc.audit(ctx, appNote("shop--staging--web", audit.ActionDeleted))
	sc.Finish(204, audit.Resource{})
	rows := auditRows(t, e.store)
	if len(rows) != 1 || rows[0].Actor.Email != "bo@example.com" || rows[0].Actor.Kind != audit.ActorPerson {
		t.Errorf("entry = %+v", rows)
	}
}

func TestAuditScopeFinish(t *testing.T) {
	someone := Principal{PersonID: "p1", Email: "ana@example.com", OrgRole: OrgOwner}
	tests := []struct {
		name     string
		noted    bool
		status   int
		fallback audit.Resource
		stored   audit.Resource // SetFallback
		want     *audit.Entry   // nil: no entry
	}{
		{name: "a write that noted nothing leaves the fallback entry", status: 204,
			fallback: audit.Resource{Kind: audit.KindApp, ID: "shop--staging--web", Name: "web", Project: "shop", Environment: "staging"},
			want: &audit.Entry{Action: audit.ActionChanged, Fallback: true,
				Resource: audit.Resource{Kind: audit.KindApp, ID: "shop--staging--web", Name: "web", Project: "shop", Environment: "staging"}}},
		{name: "the resource SetFallback gave", status: 200, stored: audit.Resource{Kind: audit.KindProject, ID: "shop"},
			want: &audit.Entry{Action: audit.ActionChanged, Fallback: true, Resource: audit.Resource{Kind: audit.KindProject, ID: "shop"}}},
		{name: "the argument beats SetFallback", status: 200, stored: audit.Resource{Kind: audit.KindProject, ID: "shop"},
			fallback: audit.Resource{Kind: audit.KindApp, ID: "x"},
			want:     &audit.Entry{Action: audit.ActionChanged, Fallback: true, Resource: audit.Resource{Kind: audit.KindApp, ID: "x"}}},
		{name: "no resource at all is the installation", status: 201,
			want: &audit.Entry{Action: audit.ActionChanged, Fallback: true, Resource: audit.Resource{Kind: audit.KindInstallation}}},
		{name: "a refused request leaves nothing", status: 403},
		{name: "a failed request that noted nothing leaves nothing", status: 500},
		{name: "a request that changed something and then failed keeps its entry", noted: true, status: 502,
			want: &audit.Entry{Action: audit.ActionDeployed, Resource: audit.Resource{Kind: audit.KindApp, ID: "noted"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx, sc := e.svc.BeginAudit(context.Background(), audit.DoorAPI, "203.0.113.4", "POST /v1/x")
			sc.SetActor(someone)
			if tc.stored.Kind != "" {
				sc.SetFallback(tc.stored)
			}
			if tc.noted {
				e.svc.audit(ctx, audit.Entry{Action: audit.ActionDeployed, Resource: audit.Resource{Kind: audit.KindApp, ID: "noted"}})
			}
			sc.Finish(tc.status, tc.fallback)
			rows := auditRows(t, e.store)
			if tc.want == nil {
				if len(rows) != 0 {
					t.Fatalf("entries = %+v, want none", rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("%d entries, want 1", len(rows))
			}
			got := rows[0].Entry
			tc.want.Actor = audit.Actor{Kind: audit.ActorPerson, Email: "ana@example.com", PersonID: "p1"}
			tc.want.Door, tc.want.Address, tc.want.Operation = audit.DoorAPI, "203.0.113.4", "POST /v1/x"
			tc.want.Changes = []audit.Change{}
			if !reflect.DeepEqual(got, *tc.want) {
				t.Errorf("entry =\n  %+v\nwant\n  %+v", got, *tc.want)
			}
		})
	}
}

// A fallback entry is a warning in the log: a write route that notes nothing is a gap.
func TestAuditFallbackIsLogged(t *testing.T) {
	logs := &syncBuffer{}
	e := newEnv(t)
	e.svc.log = slog.New(slog.NewTextHandler(logs, nil))
	_, sc := e.svc.BeginAudit(context.Background(), audit.DoorAPI, "", "POST /v1/zzz")
	sc.Finish(204, audit.Resource{})
	if out := logs.String(); !strings.Contains(out, "fallback entry") || !strings.Contains(out, "POST /v1/zzz") {
		t.Errorf("log = %q", out)
	}
}

// A write that fails is logged, never returned, and never the reason a change fails.
func TestAuditWriteFailureIsLogged(t *testing.T) {
	logs := &syncBuffer{}
	e := newEnv(t)
	e.svc.log = slog.New(slog.NewTextHandler(logs, nil))
	e.store.Close()                                                                        // every write fails now
	e.svc.audit(context.Background(), appNote("shop--staging--web", audit.ActionDeployed)) // no panic, no return value
	if out := logs.String(); !strings.Contains(out, "an entry was not written") || !strings.Contains(out, "deployed") {
		t.Errorf("log = %q", out)
	}
}

// An action outside the closed list is a programming error: logged, but the entry isn't lost.
func TestAuditUnknownActionIsLoggedAndKept(t *testing.T) {
	logs := &syncBuffer{}
	e := newEnv(t)
	e.svc.log = slog.New(slog.NewTextHandler(logs, nil))
	e.svc.audit(context.Background(), appNote("x", audit.Action("made")))
	if !strings.Contains(logs.String(), "closed lists") {
		t.Errorf("log = %q", logs.String())
	}
	if rows := auditRows(t, e.store); len(rows) != 1 {
		t.Errorf("entries = %d, want the entry kept", len(rows))
	}
}

func TestAuditClipsWhatARequestCarried(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("é", 400)
	e.svc.audit(context.Background(), audit.Entry{
		Action: audit.ActionCreated, Address: strings.Repeat("9", 100), Operation: strings.Repeat("o", 300),
		Resource: audit.Resource{Kind: audit.KindApp, ID: long, Name: long},
		Actor:    audit.Actor{Kind: audit.ActorAnonymous, Email: long},
	})
	got := auditRows(t, e.store)[0]
	if len(got.Address) != 64 || len(got.Operation) != 200 || len(got.Resource.ID) > 500 || len(got.Resource.Name) > 200 || len(got.Actor.Email) > 254 {
		t.Errorf("entry not clipped: %d %d %d %d %d", len(got.Address), len(got.Operation), len(got.Resource.ID), len(got.Resource.Name), len(got.Actor.Email))
	}
	if strings.ToValidUTF8(got.Resource.ID, "?") != got.Resource.ID {
		t.Error("a character was cut in half")
	}
}

func TestAnonymousActor(t *testing.T) {
	for typed, want := range map[string]string{
		"ana@example.com":                  "ana@example.com",
		"Ana <ana@example.com>":            "", // a display name: not a plain address
		"hunter2":                          "", // a password typed in the email box
		"":                                 "",
		"a b@example.com":                  "",
		strings.Repeat("a", 250) + "@x.co": "", // over 254 bytes
	} {
		got := AnonymousActor(typed)
		if got.Kind != audit.ActorAnonymous || got.Email != want {
			t.Errorf("AnonymousActor(%q) = %+v, want email %q", typed, got, want)
		}
	}
}

// Auth records the same way, on its own store.
func TestAuthNotesLikeService(t *testing.T) {
	e := newAuthEnvWithAdmin(t)
	ctx := WithAuditDoor(context.Background(), audit.DoorHost)
	setup := len(auditRows(t, e.store)) // the admin's creation is noted too
	e.auth.audit(ctx, audit.Entry{Action: audit.ActionSignInFailed, Actor: AnonymousActor("ana@example.com"), Address: "203.0.113.4",
		Resource: audit.Resource{Kind: audit.KindAccount}})
	e.auth.audit(ctx, audit.Entry{Action: audit.ActionUpdated, Resource: audit.Resource{Kind: audit.KindAccount, ID: "p1"}})
	rows := auditRows(t, e.store)[setup:]
	if len(rows) != 2 {
		t.Fatalf("%d entries", len(rows))
	}
	if rows[0].Actor != (audit.Actor{Kind: audit.ActorAnonymous, Email: "ana@example.com"}) || rows[0].Door != audit.DoorHost {
		t.Errorf("sign-in failure = %+v", rows[0])
	}
	if rows[1].Actor.Kind != audit.ActorHost {
		t.Errorf("CLI entry actor = %+v", rows[1].Actor)
	}
}

func TestAuditReadsAreForOwnersAndAdmins(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.svc.audit(context.Background(), appNote("shop--staging--web", audit.ActionUpdated))
	}
	member := WithPrincipal(context.Background(), shopMember())
	admin := WithPrincipal(context.Background(), Principal{OrgRole: OrgAdmin, Email: "a@example.com"})
	owner := WithPrincipal(context.Background(), Principal{OrgRole: OrgOwner, Email: "o@example.com"})
	from, to := e.svc.clock.Now().Add(-24*time.Hour), e.svc.clock.Now().Add(24*time.Hour)

	if _, err := e.svc.AuditEvents(member, AuditQuery{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("AuditEvents as a member = %v", err)
	}
	if err := e.svc.ExportAuditEvents(member, AuditQuery{From: from, To: to}, func(audit.Event) error { return nil }); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("ExportAuditEvents as a member = %v", err)
	}
	if _, err := e.svc.AuditSettings(member); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("AuditSettings as a member = %v", err)
	}
	for name, ctx := range map[string]context.Context{"admin": admin, "owner": owner, "caravana": context.Background()} {
		page, err := e.svc.AuditEvents(ctx, AuditQuery{Limit: 2})
		if err != nil || len(page.Events) != 2 || page.Next != page.Events[1].ID {
			t.Errorf("%s: AuditEvents = %+v, %v", name, page, err)
		}
		last, err := e.svc.AuditEvents(ctx, AuditQuery{Before: page.Next, Limit: 10})
		if err != nil || len(last.Events) != 3 || last.Next != 0 {
			t.Errorf("%s: the last page = %+v, %v", name, last, err)
		}
		var n int
		if err := e.svc.ExportAuditEvents(ctx, AuditQuery{From: from, To: to}, func(audit.Event) error { n++; return nil }); err != nil || n != 5 {
			t.Errorf("%s: export = %d entries, %v", name, n, err)
		}
		if st, err := e.svc.AuditSettings(ctx); err != nil || st.RetentionDays != 90 {
			t.Errorf("%s: AuditSettings = %+v, %v", name, st, err)
		}
	}
}

func TestAuditQueryRefusalsNameTheField(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	for name, tc := range map[string]struct {
		q     AuditQuery
		field string
		fn    string // "events" or "export"
	}{
		"a bad action":                   {AuditQuery{Action: "made"}, "action", "events"},
		"a bad kind":                     {AuditQuery{Kind: "thing"}, "kind", "events"},
		"an environment needs a project": {AuditQuery{Environment: "staging"}, "environment", "events"},
		"a limit of zero is the default": {AuditQuery{Limit: 501}, "limit", "events"},
		"a negative cursor":              {AuditQuery{Before: -1}, "before", "events"},
		"to before from":                 {AuditQuery{From: now, To: now.Add(-time.Hour)}, "to", "events"},
		"an export needs from":           {AuditQuery{To: now}, "from", "export"},
		"an export needs to":             {AuditQuery{From: now}, "to", "export"},
		"an export has no page":          {AuditQuery{From: now, To: now, Limit: 5}, "limit", "export"},
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if tc.fn == "events" {
				_, err = e.svc.AuditEvents(context.Background(), tc.q)
			} else {
				err = e.svc.ExportAuditEvents(context.Background(), tc.q, func(audit.Event) error { return nil })
			}
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Errorf("err = %v, want a field error on %s", err, tc.field)
			}
		})
	}
	// A valid action and kind pass.
	if _, err := e.svc.AuditEvents(context.Background(), AuditQuery{Action: "deployed", Kind: "app", Project: "shop", Environment: "staging", Limit: 500}); err != nil {
		t.Errorf("a valid query = %v", err)
	}
}

func TestSetAuditSettings(t *testing.T) {
	e := newEnv(t)
	owner := WithPrincipal(context.Background(), Principal{PersonID: "o", OrgRole: OrgOwner, Email: "o@example.com"})
	admin := WithPrincipal(context.Background(), Principal{PersonID: "a", OrgRole: OrgAdmin, Email: "a@example.com"})

	// An admin and a refused value leave nothing.
	if _, err := e.svc.SetAuditSettings(admin, AuditSettings{RetentionDays: 365}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("as an admin = %v, want not allowed", err)
	}
	for _, days := range []int{0, 29, 3651, -5} {
		_, err := e.svc.SetAuditSettings(owner, AuditSettings{RetentionDays: days})
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "retentionDays" {
			t.Errorf("%d days = %v, want a field error on retentionDays", days, err)
		}
	}
	if n := len(auditRows(t, e.store)); n != 0 {
		t.Fatalf("refused changes left %d entries", n)
	}

	got, err := e.svc.SetAuditSettings(owner, AuditSettings{RetentionDays: 365})
	if err != nil || got.RetentionDays != 365 {
		t.Fatalf("SetAuditSettings = %+v, %v", got, err)
	}
	rows := auditRows(t, e.store)
	if len(rows) != 1 {
		t.Fatalf("%d entries, want 1", len(rows))
	}
	want := audit.Entry{
		Actor: audit.Actor{Kind: audit.ActorPerson, Email: "o@example.com", PersonID: "o"}, Door: audit.DoorAPI, Action: audit.ActionUpdated,
		Resource: audit.Resource{Kind: audit.KindSettings, ID: "audit", Name: "Audit log"},
		Changes:  []audit.Change{{Path: "retentionDays", Op: audit.OpChanged, From: "90", To: "365"}},
	}
	if !reflect.DeepEqual(rows[0].Entry, want) {
		t.Errorf("entry =\n  %+v\nwant\n  %+v", rows[0].Entry, want)
	}
	if st, _ := e.svc.AuditSettings(owner); st.RetentionDays != 365 {
		t.Errorf("stored = %+v", st)
	}
}

func TestAuditPrune(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := e.svc.clock.Now()
	for _, age := range []time.Duration{100 * 24 * time.Hour, 91 * 24 * time.Hour, 89 * 24 * time.Hour, time.Hour} {
		if _, err := e.store.InsertAuditEvent(ctx, now.Add(-age), appNote("x", audit.ActionUpdated)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := e.svc.pruneAudit(ctx)
	if err != nil || n != 2 {
		t.Fatalf("pruneAudit = %d, %v; want 2 (the default is 90 days)", n, err)
	}
	if _, err := e.svc.SetAuditSettings(context.Background(), AuditSettings{RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	// The change is an entry too, and it is new.
	if n, err = e.svc.pruneAudit(ctx); err != nil || n != 1 {
		t.Errorf("pruneAudit at 30 days = %d, %v; want the 89-day-old entry only", n, err)
	}
	if rows := auditRows(t, e.store); len(rows) != 2 {
		t.Errorf("%d entries left, want the new one and the settings change", len(rows))
	}
}

// The pass runs by itself: once after the first gap, and again after the next.
func TestAuditPruneLoop(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.runAuditPrune(ctx, 5*time.Millisecond, 5*time.Millisecond); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	insertOld := func() {
		t.Helper()
		if _, err := e.store.InsertAuditEvent(context.Background(), e.svc.clock.Now().Add(-200*24*time.Hour), appNote("x", audit.ActionUpdated)); err != nil {
			t.Fatal(err)
		}
	}
	waitEmpty := func(round string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for len(auditRows(t, e.store)) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the pass never pruned", round)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	insertOld()
	waitEmpty("first")
	insertOld()
	waitEmpty("second")
}

// Pruning is housekeeping: it notes nothing.
func TestAuditPruneNotesNothing(t *testing.T) {
	e := newEnv(t)
	if _, err := e.store.InsertAuditEvent(context.Background(), e.svc.clock.Now().Add(-200*24*time.Hour), appNote("x", audit.ActionUpdated)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.pruneAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := auditRows(t, e.store); len(rows) != 0 {
		t.Errorf("entries after a prune: %+v", rows)
	}
}

func TestSetAuditPruneGap(t *testing.T) {
	s := New(nil, nil, nil, discardLogger())
	if s.auditFirstGap != time.Minute || s.auditGap != 24*time.Hour {
		t.Errorf("defaults = %v %v, want a minute after Start and then every 24 hours", s.auditFirstGap, s.auditGap)
	}
	s.SetAuditPruneGap(time.Second, time.Hour)
	if s.auditFirstGap != time.Second || s.auditGap != time.Hour {
		t.Errorf("gaps = %v %v", s.auditFirstGap, s.auditGap)
	}
}
