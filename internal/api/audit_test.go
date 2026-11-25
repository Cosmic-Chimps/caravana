package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// auditToken makes a person with the organization role and a token of theirs, and returns the
// Authorization header value.
func auditToken(t *testing.T, e *testEnv, email, role string, scope *service.TokenScope) string {
	t.Helper()
	ctx := context.Background()
	id := (strings.ReplaceAll(email, "@example.com", "") + "00000000000000000000000")[:26]
	if err := e.Store.CreatePerson(ctx, store.PersonRecord{ID: id, Email: email, OrgRole: role, State: "active", PasswordHash: "x",
		PasswordSignIn: true, CreatedAt: e.Clock.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	c, err := e.Auth.CreateScopedAPIToken(ctx, id, strings.ReplaceAll(email, "@example.com", "")+"-token", nil, scope)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + c.Token
}

type auditEventsBody struct {
	Events []map[string]any `json:"events"`
	Next   string           `json:"next"`
}

func getAudit(t *testing.T, e *testEnv, query string) (int, http.Header, []byte) {
	t.Helper()
	return call(t, http.MethodGet, e.URL+"/v1/audit"+query, "", "")
}

func decodeAudit(t *testing.T, raw []byte) auditEventsBody {
	t.Helper()
	var out auditEventsBody
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

func putRetention(t *testing.T, e *testEnv, days int) {
	t.Helper()
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/audit/settings", "application/json", fmt.Sprintf(`{"retentionDays":%d}`, days)); status != http.StatusOK {
		t.Fatalf("PUT /v1/audit/settings %d = %d %s", days, status, raw)
	}
}

func TestListAuditEvents(t *testing.T) {
	e := newTestEnv(t, "")
	putRetention(t, e, 365)

	status, hdr, raw := getAudit(t, e, "")
	if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /v1/audit = %d %v %s", status, hdr, raw)
	}
	body := decodeAudit(t, raw)
	if len(body.Events) != 1 || body.Next != "" {
		t.Fatalf("events = %s", raw)
	}
	ev := body.Events[0]
	for _, k := range []string{"id", "at", "actor", "door", "action", "operation", "resource", "changes", "fallback"} {
		if _, ok := ev[k]; !ok {
			t.Errorf("event has no %q: %v", k, ev)
		}
	}
	actor := ev["actor"].(map[string]any)
	if actor["kind"] != "token" || actor["email"] != testAdminEmail || actor["token"] != "tests" || actor["personId"] != e.OwnerID {
		t.Errorf("actor = %v", actor)
	}
	if ev["door"] != "api" || ev["action"] != "updated" || ev["operation"] != "PUT /v1/audit/settings" || ev["fallback"] != false {
		t.Errorf("event = %v", ev)
	}
	if res := ev["resource"].(map[string]any); res["kind"] != "settings" || res["id"] != "audit" {
		t.Errorf("resource = %v", res)
	}
	changes := ev["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["path"] != "retentionDays" || changes[0].(map[string]any)["from"] != "90" || changes[0].(map[string]any)["to"] != "365" {
		t.Errorf("changes = %v", changes)
	}
	if _, err := time.Parse(time.RFC3339Nano, ev["at"].(string)); err != nil {
		t.Errorf("at = %v: %v", ev["at"], err)
	}
	// No token ever reaches an entry or an answer.
	if strings.Contains(string(raw), e.Token) || strings.Contains(string(raw), "cvat_") {
		t.Errorf("the answer holds a token: %s", raw)
	}
}

func TestListAuditEventsPagesAndFilters(t *testing.T) {
	e := newTestEnv(t, "")
	for _, days := range []int{31, 32, 33, 34, 35} {
		putRetention(t, e, days)
	}
	// A different actor and door: the MCP server's user agent.
	req, _ := http.NewRequest(http.MethodPut, e.URL+"/v1/audit/settings", strings.NewReader(`{"retentionDays":40}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "caravana-mcp/9.9.9")
	resp, err := noRedirects.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT as mcp: %v %v", resp, err)
	}
	resp.Body.Close()

	// Paging: newest first, limited, by the cursor.
	_, _, raw := getAudit(t, e, "?limit=4")
	page := decodeAudit(t, raw)
	if len(page.Events) != 4 || page.Next == "" || fmt.Sprintf("%.0f", page.Events[3]["id"]) != page.Next {
		t.Fatalf("page 1 = %s", raw)
	}
	if page.Events[0]["door"] != "mcp" {
		t.Errorf("the newest entry's door = %v, want mcp (the MCP server's User-Agent)", page.Events[0]["door"])
	}
	_, _, raw = getAudit(t, e, "?limit=4&before="+page.Next)
	rest := decodeAudit(t, raw)
	if len(rest.Events) != 2 || rest.Next != "" {
		t.Fatalf("page 2 = %s", raw)
	}

	// Filters.
	for query, want := range map[string]int{
		"?actor=" + url.QueryEscape(testAdminEmail): 6,
		"?actor=tests":    6,
		"?actor=nobody":   0,
		"?action=updated": 6,
		"?action=deleted": 0,
		"?kind=settings":  6,
		"?kind=app":       0,
		"?resource=audit": 6,
		"?resource=other": 0,
		"?project=shop":   0,
		"?from=2000-01-01T00:00:00Z&to=2001-01-01T00:00:00Z": 0,
		"?from=2000-01-01T00:00:00Z":                         6,
	} {
		status, _, raw := getAudit(t, e, query)
		if got := len(decodeAudit(t, raw).Events); status != http.StatusOK || got != want {
			t.Errorf("GET /v1/audit%s = %d with %d events, want %d", query, status, got, want)
		}
	}
}

func TestListAuditEventsRefusals(t *testing.T) {
	e := newTestEnv(t, "")
	for query, field := range map[string]string{
		"?action=made":         "action",
		"?kind=thing":          "kind",
		"?environment=staging": "environment",
		"?limit=0":             "limit",
		"?limit=501":           "limit",
		"?limit=many":          "limit",
		"?before=0":            "before",
		"?before=abc":          "before",
		"?from=yesterday":      "from",
		"?to=2026-13-01":       "to",
		"?from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z": "to",
	} {
		status, _, raw := getAudit(t, e, query)
		var body struct {
			Error struct{ Code, Field string }
		}
		_ = json.Unmarshal(raw, &body)
		if status != http.StatusUnprocessableEntity || body.Error.Code != "invalid_field" || body.Error.Field != field {
			t.Errorf("GET /v1/audit%s = %d %s, want 422 invalid_field on %s", query, status, raw, field)
		}
	}
}

func TestExportAuditEvents(t *testing.T) {
	e := newTestEnv(t, "")
	putRetention(t, e, 100)
	putRetention(t, e, 200)
	from, to := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/audit/export?from="+from+"&to="+to, "", "")
	if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") {
		t.Fatalf("export = %d %v %s", status, hdr, raw)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="caravana-audit-`) || !strings.HasSuffix(cd, `.csv"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatalf("export is not CSV with a header and two rows: %v %q", err, raw)
	}
	if rows[0][0] != "id" || rows[0][16] != "record_id" || len(rows[0]) != 17 {
		t.Errorf("header = %v", rows[0])
	}
	// Oldest first.
	if rows[1][0] >= rows[2][0] || !strings.Contains(rows[1][14], "retentionDays changed 90 → 100") || !strings.Contains(rows[2][14], "retentionDays changed 100 → 200") {
		t.Errorf("rows = %v", rows[1:])
	}
	if rows[1][3] != testAdminEmail || rows[1][4] != "tests" || rows[1][5] != "api" || rows[1][7] != "updated" || rows[1][9] != "settings" || rows[1][10] != "audit" {
		t.Errorf("row 1 = %v", rows[1])
	}

	// An empty range is the header alone.
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/audit/export?from=2000-01-01T00:00:00Z&to=2000-01-02T00:00:00Z", "", "")
	if status != http.StatusOK || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 1 {
		t.Errorf("empty export = %d %q", status, raw)
	}

	// Refusals are JSON with no attachment header, whatever was set first.
	for query, field := range map[string]string{
		"":              "from",
		"?from=" + from: "to",
		"?to=" + to:     "from",
		"?from=" + from + "&to=" + to + "&limit=5":    "limit",
		"?from=" + from + "&to=" + to + "&before=3":   "limit",
		"?from=" + from + "&to=" + to + "&kind=thing": "kind",
		"?from=" + to + "&to=" + from:                 "to",
		"?from=nope&to=" + to:                         "from",
	} {
		status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/audit/export"+query, "", "")
		var body struct {
			Error struct{ Code, Field string }
		}
		_ = json.Unmarshal(raw, &body)
		if status != http.StatusUnprocessableEntity || body.Error.Field != field || hdr.Get("Content-Disposition") != "" ||
			!strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
			t.Errorf("GET /v1/audit/export%s = %d %v %s, want JSON 422 on %s", query, status, hdr, raw, field)
		}
	}
}

func TestAuditSettingsEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/audit/settings", "", "")
	if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" || strings.TrimSpace(string(raw)) != `{"retentionDays":90}` {
		t.Fatalf("GET settings = %d %v %s", status, hdr, raw)
	}
	for name, tc := range map[string]struct {
		body   string
		status int
		code   string
		field  string
	}{
		"too short":     {`{"retentionDays":29}`, 422, "invalid_field", "retentionDays"},
		"too long":      {`{"retentionDays":3651}`, 422, "invalid_field", "retentionDays"},
		"missing":       {`{}`, 422, "invalid_field", "retentionDays"},
		"a string":      {`{"retentionDays":"90"}`, 400, "invalid_field", "retentionDays"},
		"unknown field": {`{"retentionDays":90,"keep":1}`, 400, "unknown_field", "keep"},
	} {
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/audit/settings", "application/json", tc.body)
		var body struct {
			Error struct{ Code, Field string }
		}
		_ = json.Unmarshal(raw, &body)
		if status != tc.status || (tc.code != "" && body.Error.Code != tc.code) || body.Error.Field != tc.field {
			t.Errorf("%s: PUT %s = %d %s", name, tc.body, status, raw)
		}
	}
	if n := auditRowCount(t, e.Store); n != 0 {
		t.Errorf("refused changes left %d entries", n)
	}
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/audit/settings", "application/json", `{"retentionDays":3650}`)
	if status != http.StatusOK || strings.TrimSpace(string(raw)) != `{"retentionDays":3650}` {
		t.Fatalf("PUT = %d %s", status, raw)
	}
	if _, _, raw = call(t, http.MethodGet, e.URL+"/v1/audit/settings", "", ""); strings.TrimSpace(string(raw)) != `{"retentionDays":3650}` {
		t.Errorf("GET after = %s", raw)
	}
}

// Owners and admins read, owners change the retention; members and deploy tokens are refused.
func TestAuditLogIsForOwnersAndAdmins(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)
	admin := "Bearer " + func() string {
		return strings.TrimPrefix(auditToken(t, e, "admin2@example.com", "admin", nil), "Bearer ")
	}()
	member := auditToken(t, e, "member@example.com", "member", nil)
	deployTok := auditToken(t, e, "other@example.com", "admin", &service.TokenScope{Project: "shop", App: "web"})

	range1 := "?from=2000-01-01T00:00:00Z&to=2100-01-01T00:00:00Z"
	for name, tc := range map[string]struct {
		auth         string
		method, path string
		body         string
		want         int
		code         string
	}{
		"an admin reads":           {admin, "GET", "/v1/audit", "", 200, ""},
		"an admin exports":         {admin, "GET", "/v1/audit/export" + range1, "", 200, ""},
		"an admin reads settings":  {admin, "GET", "/v1/audit/settings", "", 200, ""},
		"an admin can't change":    {admin, "PUT", "/v1/audit/settings", `{"retentionDays":60}`, 403, "not_allowed"},
		"a member can't read":      {member, "GET", "/v1/audit", "", 403, "not_allowed"},
		"a member can't export":    {member, "GET", "/v1/audit/export" + range1, "", 403, "not_allowed"},
		"a member can't read set.": {member, "GET", "/v1/audit/settings", "", 403, "not_allowed"},
		"a member can't change":    {member, "PUT", "/v1/audit/settings", `{"retentionDays":60}`, 403, "not_allowed"},
		"a deploy token reads":     {deployTok, "GET", "/v1/audit", "", 403, "token_scope"},
		"a deploy token exports":   {deployTok, "GET", "/v1/audit/export" + range1, "", 403, "token_scope"},
		"a deploy token changes":   {deployTok, "PUT", "/v1/audit/settings", `{"retentionDays":60}`, 403, "token_scope"},
		"no credentials":           {"", "GET", "/v1/audit", "", 401, ""},
	} {
		status, _, raw := callWith(t, tc.auth, tc.method, e.RawURL+tc.path, "application/json", tc.body)
		if status != tc.want || (tc.code != "" && !strings.Contains(string(raw), `"`+tc.code+`"`)) {
			t.Errorf("%s: %s %s = %d %s", name, tc.method, tc.path, status, raw)
		}
	}
	// None of the refusals left an entry; the project made through the API did (a fallback today).
	got, _, _ := e.Store.ListAuditEvents(context.Background(), store.AuditFilter{Kind: "settings"})
	if len(got) != 0 {
		t.Errorf("refused requests left audit entries: %+v", got)
	}
}
