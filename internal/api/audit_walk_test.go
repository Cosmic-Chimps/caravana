package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// auditExceptions are the write routes that leave no audit entry, written out here on purpose, apart from
// auditExempt in the code, so that exempting a route takes two edits and a reviewer sees both. Each has
// the reason (feature 057 plan D12).
var auditExceptions = map[string]string{
	"POST /v1/node/sync":                 "the agent's routine call, every 30 seconds",
	"POST /v1/node/certificate":          "the agent's routine certificate renewal",
	"POST /v1/apps/{id}/compose/preview": "a read sent as POST: it shows the app a Compose file becomes",
	"POST /v1/templates/{name}/preview":  "a read sent as POST: it shows the app a template becomes",
	"POST /v1/apps/{id}/promote/preview": "a read sent as POST: it shows what a promotion would do",
}

func auditRowCount(t *testing.T, st *store.Store) int {
	t.Helper()
	got, _, err := st.ListAuditEvents(context.Background(), store.AuditFilter{Limit: store.MaxAuditLimit})
	if err != nil {
		t.Fatal(err)
	}
	return len(got)
}

func lastAuditRow(t *testing.T, st *store.Store) audit.Event {
	t.Helper()
	got, _, err := st.ListAuditEvents(context.Background(), store.AuditFilter{Limit: 1})
	if err != nil || len(got) != 1 {
		t.Fatalf("no audit row: %v, %v", got, err)
	}
	return got[0]
}

// The exception table of the code and the one of the test are the same.
func TestAuditExemptionsMatch(t *testing.T) {
	for key, why := range auditExceptions {
		if why == "" {
			t.Errorf("%s has no reason", key)
		}
		if _, ok := auditExempt[key]; !ok {
			t.Errorf("%s is an exception here but not in auditExempt", key)
		}
	}
	for key, why := range auditExempt {
		if why == "" {
			t.Errorf("auditExempt %s has no reason", key)
		}
		if _, ok := auditExceptions[key]; !ok {
			t.Errorf("auditExempt has %s, which the test doesn't list", key)
		}
	}
}

// TestEveryWriteRouteLeavesOneEntry walks every non-GET route of the API as the owner, with the handlers
// stubbed to 204 (so nothing noted anything), and checks that the request left exactly one entry that names
// its actor and its resource kind: the fallback, which the scope writes when a write route noted nothing.
// The routes in auditExceptions leave none. Then it checks that a request that is refused, unauthenticated or
// that fails before changing anything leaves none either, and that one that changed something and then failed
// keeps its entry.
func TestEveryWriteRouteLeavesOneEntry(t *testing.T) {
	e, callers, s, _ := walkEnv(t)
	owner := callers[0]
	member := callers[2]
	deploy := callers[3]

	classOf := map[string]string{}
	for class, keys := range walkClasses {
		for _, key := range keys {
			classOf[key] = class
		}
	}

	walked := 0
	for _, rt := range s.routes() {
		if rt.method == http.MethodGet {
			continue
		}
		key := rt.method + " " + rt.path
		t.Run(key, func(t *testing.T) {
			path, body := filledPath(rt.path), ""
			if !rt.public {
				class, ok := classOf[key]
				if !ok {
					t.Fatalf("%s has no class in walkClasses", key)
				}
				// The owner passes every route, so the first variant of the class will do.
				wr := walkRequests(class, key, rt.path)[0]
				path, body = wr.path, wr.body
			}
			before := auditRowCount(t, e.Store)
			status, raw := owner.call(rt.method, path, body)
			if status != http.StatusNoContent && !rt.public {
				t.Fatalf("%s as the owner = %d %s, want the stub's 204", key, status, raw)
			}
			after := auditRowCount(t, e.Store)
			if _, exempt := auditExceptions[key]; exempt {
				if after != before {
					t.Errorf("%s is an exception but left %d entries", key, after-before)
				}
				return
			}
			walked++
			if after != before+1 {
				t.Fatalf("%s left %d entries, want exactly 1", key, after-before)
			}
			row := lastAuditRow(t, e.Store)
			if row.Operation != key || row.Resource.Kind == "" || !audit.ValidKind(row.Resource.Kind) || !audit.ValidAction(row.Action) {
				t.Errorf("%s: entry = %+v", key, row)
			}
			if rt.path == webhookPath {
				if row.Door != audit.DoorWebhook || row.Actor.Kind != audit.ActorWebhook || row.Actor.Token != "x" || row.Resource.Kind != audit.KindApp {
					t.Errorf("%s: the webhook's entry = %+v", key, row)
				}
			} else {
				if row.Door != audit.DoorAPI {
					t.Errorf("%s: door = %s, want api", key, row.Door)
				}
				if !rt.public && (row.Actor.Kind != audit.ActorPerson || row.Actor.Email != testAdminEmail || row.Actor.PersonID == "") {
					t.Errorf("%s: actor = %+v, want the owner %s", key, row.Actor, testAdminEmail)
				}
			}
			if !row.Fallback || row.Action != audit.ActionChanged {
				t.Errorf("%s: stubbed handlers note nothing, so this should be the fallback: %+v", key, row)
			}
		})
	}
	if walked < 80 {
		t.Errorf("the walk covered %d write routes; it should be well over eighty", walked)
	}

	t.Run("a refused request leaves no entry", func(t *testing.T) {
		before := auditRowCount(t, e.Store)
		if status, raw := member.call(http.MethodPost, "/v1/people", `{}`); status != http.StatusForbidden || !strings.Contains(raw, "not_allowed") {
			t.Fatalf("a member inviting = %d %s", status, raw)
		}
		if status, raw := member.call(http.MethodPut, "/v1/audit/settings", `{"retentionDays":365}`); status != http.StatusForbidden {
			t.Fatalf("a member setting the retention = %d %s", status, raw)
		}
		if status, raw := deploy.call(http.MethodPost, "/v1/people", `{}`); status != http.StatusForbidden || !strings.Contains(raw, "token_scope") {
			t.Fatalf("a deploy token inviting = %d %s", status, raw)
		}
		if status, _ := (walkCaller{call: func(m, u, b string) (int, string) {
			st, _, raw := callWith(t, "", m, httptestURL(t, s)+u, "application/json", b)
			return st, string(raw)
		}}).call(http.MethodPost, "/v1/people", `{}`); status != http.StatusUnauthorized {
			t.Fatalf("no credentials = %d, want 401", status)
		}
		if got := auditRowCount(t, e.Store); got != before {
			t.Errorf("refused requests left %d entries", got-before)
		}
	})
}

// httptestURL serves the stubbed routes of s without credentials and returns the address.
func httptestURL(t *testing.T, s *server) string {
	t.Helper()
	srv := httptest.NewServer(s.handler(stubbed(s)))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A write that fails after it changed something keeps its entry, and one that fails before leaves none,
// whatever the status; the entry names who asked through which door.
func TestAuditScopeFollowsTheStatus(t *testing.T) {
	e := newTestEnv(t, "")
	s := newServer(Deps{Service: e.Service, Auth: e.Auth, Store: e.Store, Log: e.log})
	failAfterChange := errors.New("then it failed")
	routes := []route{
		{http.MethodPut, "/v1/zzz/changed-then-failed", func(w http.ResponseWriter, r *http.Request) error {
			// A real service call that notes, then the handler fails.
			if _, err := s.svc.SetAuditSettings(r.Context(), service.AuditSettings{RetentionDays: 200}); err != nil {
				return err
			}
			return failAfterChange
		}, false, false},
		{http.MethodPut, "/v1/zzz/failed-before", func(w http.ResponseWriter, r *http.Request) error {
			return &Error{Status: http.StatusConflict, Code: "conflict", Message: "no"}
		}, false, false},
		{http.MethodPut, "/v1/zzz/internal", func(w http.ResponseWriter, r *http.Request) error { return failAfterChange }, false, false},
	}
	for _, rt := range routes {
		accessRules[rt.method+" "+rt.path] = admin()
		t.Cleanup(func() { delete(accessRules, rt.method+" "+rt.path) })
	}
	srv := httptest.NewServer(s.handler(routes))
	t.Cleanup(srv.Close)

	call := func(path string, headers map[string]string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, srv.URL+path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+e.Token)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := noRedirects.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	before := auditRowCount(t, e.Store)
	if status := call("/v1/zzz/failed-before", nil); status != http.StatusConflict {
		t.Fatalf("status = %d", status)
	}
	if status := call("/v1/zzz/internal", nil); status != http.StatusInternalServerError {
		t.Fatalf("status = %d", status)
	}
	if got := auditRowCount(t, e.Store); got != before {
		t.Fatalf("failed requests that changed nothing left %d entries", got-before)
	}

	if status := call("/v1/zzz/changed-then-failed", map[string]string{"User-Agent": "caravana-mcp/1.2.3"}); status != http.StatusInternalServerError {
		t.Fatalf("status = %d", status)
	}
	if got := auditRowCount(t, e.Store); got != before+1 {
		t.Fatalf("a change that then failed left %d entries, want 1", got-before)
	}
	row := lastAuditRow(t, e.Store)
	if row.Door != audit.DoorMCP || row.Actor.Kind != audit.ActorToken || row.Actor.Token != "tests" || row.Actor.Email != testAdminEmail ||
		row.Operation != "PUT /v1/zzz/changed-then-failed" || row.Fallback || row.Action != audit.ActionUpdated || row.Resource.ID != "audit" {
		t.Errorf("entry = %+v", row)
	}
	if !slices.Contains([]string{"127.0.0.1", "::1"}, row.Address) {
		t.Errorf("address = %q, want the client's loopback address", row.Address)
	}
}
