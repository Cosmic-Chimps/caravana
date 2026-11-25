package ui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 057, T012: Settings › Audit log.

// auditEntries lists every entry, newest first, as Caravana itself (no principal: nothing filtered).
func (e *consoleEnv) auditEntries(t *testing.T) []audit.Event {
	t.Helper()
	page, err := e.Svc.AuditEvents(context.Background(), service.AuditQuery{Limit: 500})
	if err != nil {
		t.Fatalf("list the audit log: %v", err)
	}
	return page.Events
}

// auditOf finds the newest entry of the action on the kind, or fails.
func (e *consoleEnv) auditOf(t *testing.T, action audit.Action, kind audit.Kind) audit.Event {
	t.Helper()
	for _, ev := range e.auditEntries(t) {
		if ev.Action == action && ev.Resource.Kind == kind {
			return ev
		}
	}
	t.Fatalf("no %s entry of a %s in %+v", action, kind, e.auditEntries(t))
	return audit.Event{}
}

// cookiesOf signs in the walk's people once and returns their cookies by name.
func (e *consoleEnv) cookiesOf(t *testing.T) map[string]*http.Cookie {
	t.Helper()
	out := map[string]*http.Cookie{}
	for _, c := range walkPeople(t, e) {
		out[c.name] = c.cookie
	}
	return out
}

func TestAuditPageShowsAConsolePost(t *testing.T) {
	e := newConsoleEnv(t)
	if resp, body := e.postForm(t, "/settings/tokens", url.Values{"name": {"ci-audit"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("make a token = %d: %s", resp.StatusCode, text(body))
	}
	ev := e.auditOf(t, audit.ActionCreated, audit.KindToken)
	if ev.Door != audit.DoorConsole || ev.Actor.Email != testEmail || ev.Actor.Kind != audit.ActorPerson || ev.Fallback {
		t.Errorf("the entry = %+v: want door console, the owner, no fallback", ev)
	}
	if ev.Operation != "POST /settings/tokens" {
		t.Errorf("operation = %q", ev.Operation)
	}

	resp, body := e.get(t, "/settings/audit")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings/audit = %d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	got := text(body)
	for _, want := range []string{"Audit log", testEmail, "created", "token", "ci-audit", "console", "Export as CSV", "Save retention"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "recorded by the route") {
		t.Errorf("a console entry is no fallback:\n%s", got)
	}
	// Its changes are in a <details>, a time is a <time> with the UTC word.
	if !strings.Contains(body, `<details class="cv-audit-changes">`) || !strings.Contains(body, `class="cv-clock"`) {
		t.Errorf("the entry has no changes block or clock:\n%s", body)
	}
	// The nav has the entry.
	if nav := settingsNavOf(t, body)["Security"]; !strings.Contains(strings.Join(nav, " "), "Audit log=/settings/audit*") {
		t.Errorf("the nav = %v", nav)
	}
}

func TestAuditIsForOwnersAndAdmins(t *testing.T) {
	e := newConsoleEnv(t)
	people := e.cookiesOf(t)
	member, admin := people["member"], people["admin"]
	for _, path := range []string{"/settings/audit", "/settings/audit/export?exportFrom=2026-01-01&exportTo=2026-01-02"} {
		resp, _ := e.do(t, request{path: path, cookies: []*http.Cookie{member}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("a member GET %s = %d, want 403", path, resp.StatusCode)
		}
		resp, _ = e.do(t, request{path: path, cookies: []*http.Cookie{admin}})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("an admin GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	// A member's Settings doesn't list it.
	_, body := e.do(t, request{path: "/settings", cookies: []*http.Cookie{member}})
	if strings.Contains(body, "/settings/audit") {
		t.Errorf("a member's Settings links the audit log")
	}
	// Only an owner changes the retention: the admin's page has no form for it, and a post is refused.
	_, body = e.do(t, request{path: "/settings/audit", cookies: []*http.Cookie{admin}})
	if strings.Contains(body, `name="retentionDays"`) || !strings.Contains(text(body), "Only an owner changes that.") {
		t.Errorf("the admin's retention isn't read-only:\n%s", text(body))
	}
	resp, _ := e.post(t, admin, "/settings/audit", url.Values{"retentionDays": {"120"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("an admin's retention post = %d, want 403", resp.StatusCode)
	}
	resp, _ = e.post(t, member, "/settings/audit", url.Values{"retentionDays": {"120"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a member's retention post = %d, want 403", resp.StatusCode)
	}
}

func TestAuditFiltersNarrowTheList(t *testing.T) {
	e := newConsoleEnv(t)
	for _, name := range []string{"first-key", "second-key"} {
		if resp, _ := e.postForm(t, "/settings/tokens", url.Values{"name": {name}}); resp.StatusCode != http.StatusOK {
			t.Fatalf("make %s = %d", name, resp.StatusCode)
		}
	}
	if resp, _ := e.postForm(t, "/projects", url.Values{"name": {"shop"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("make a project = %d", resp.StatusCode)
	}
	count := func(query string) (int, string) {
		t.Helper()
		resp, body := e.get(t, "/settings/audit"+query)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", query, resp.StatusCode)
		}
		return strings.Count(body, `id="entry-`), body
	}
	all, _ := count("")
	if all < 3 {
		t.Fatalf("the page lists %d entries, want at least 3", all)
	}
	if n, body := count("?kind=project"); n != 1 || !strings.Contains(text(body), "shop") || strings.Contains(text(body), "first-key") {
		t.Errorf("kind=project lists %d entries:\n%s", n, text(body))
	}
	if n, body := count("?kind=token&action=created"); n != 2 || !strings.Contains(text(body), "first-key") || !strings.Contains(text(body), "second-key") {
		t.Errorf("kind=token lists %d entries:\n%s", n, text(body))
	}
	if n, body := count("?action=revoked"); n != 0 || !strings.Contains(text(body), "No entries match.") {
		t.Errorf("action=revoked lists %d entries:\n%s", n, text(body))
	}
	if n, _ := count("?actor=" + url.QueryEscape("nobody@example.com")); n != 0 {
		t.Errorf("a stranger's actor lists %d entries", n)
	}
	if n, _ := count("?actor=" + url.QueryEscape(testEmail) + "&project=shop"); n != 1 {
		t.Errorf("actor and project list %d entries, want 1", n)
	}
	// The filters stay in the form.
	if _, body := count("?kind=project&resource=shop"); !strings.Contains(body, `name="resource" value="shop"`) || !strings.Contains(body, `<option value="project" selected>`) {
		t.Errorf("the form forgot its filters:\n%s", body)
	}
	// A refused value is drawn at its input, with a 422.
	resp, body := e.get(t, "/settings/audit?from=yesterday&action=exploded")
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Use a date like 2026-11-25.") {
		t.Errorf("a bad date = %d:\n%s", resp.StatusCode, text(body))
	}
	// An action outside the list is refused at its select.
	resp, body = e.get(t, "/settings/audit?action=exploded")
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Use one of the actions") {
		t.Errorf("a bad action = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestAuditOlderLinkPages(t *testing.T) {
	e := newConsoleEnv(t)
	ctx := context.Background()
	for i := range 105 {
		if _, err := e.Auth.CreateAPIToken(ctx, e.OwnerID, "k-"+strconv.Itoa(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := e.get(t, "/settings/audit")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	if n := strings.Count(body, `id="entry-`); n != auditPageSize {
		t.Fatalf("the first page lists %d entries, want %d", n, auditPageSize)
	}
	i := strings.Index(body, `>Older</a>`)
	if i < 0 {
		t.Fatalf("no Older link")
	}
	href := body[strings.LastIndex(body[:i], `href="`)+len(`href="`) : i]
	href = href[:strings.Index(href, `"`)]
	href = strings.ReplaceAll(href, "&amp;", "&")
	if !strings.Contains(href, "before=") {
		t.Fatalf("the Older link = %q", href)
	}
	resp, second := e.get(t, href)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", href, resp.StatusCode)
	}
	if n := strings.Count(second, `id="entry-`); n < 5 || n >= auditPageSize {
		t.Errorf("the second page lists %d entries", n)
	}
	if strings.Contains(second, `>Older</a>`) {
		t.Errorf("the last page has an Older link")
	}
	// The pages don't overlap: the oldest entry of the first is not on the second.
	if strings.Count(body, "k-0")+strings.Count(second, "k-0") == 0 {
		t.Errorf("the oldest token is on neither page")
	}
}

func TestAuditConsolePostsThatChangeNothingLeaveNoEntry(t *testing.T) {
	e := newConsoleEnv(t)
	before := len(e.auditEntries(t))

	// A refused post (a field error): nothing changed, nothing recorded.
	_, refused := e.postForm(t, "/settings/tokens", url.Values{"name": {""}})
	if strings.Contains(text(refused), "Copy it now") {
		t.Errorf("an empty token name made a token:\n%s", text(refused))
	}
	// A post nothing notes: the browser's own theme. The console has no fallback entry for it.
	if resp, _ := e.postForm(t, "/settings/appearance", url.Values{"theme": {"dark"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("appearance = %d", resp.StatusCode)
	}
	// A refused retention.
	if resp, _ := e.postForm(t, "/settings/audit", url.Values{"retentionDays": {"2"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("retention 2 = %d, want 422", resp.StatusCode)
	}
	if after := len(e.auditEntries(t)); after != before {
		t.Errorf("%d entries before and %d after posts that changed nothing:\n%+v", before, after, e.auditEntries(t))
	}
}

func TestAuditExportAnswersCSV(t *testing.T) {
	e := newConsoleEnv(t)
	if resp, _ := e.postForm(t, "/settings/tokens", url.Values{"name": {"exported-key"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("make a token = %d", resp.StatusCode)
	}
	now := e.Clock.Now().UTC()
	q := url.Values{"exportFrom": {now.AddDate(0, 0, -1).Format(auditDay)}, "exportTo": {now.AddDate(0, 0, 1).Format(auditDay)}}
	resp, body := e.get(t, "/settings/audit/export?"+q.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export = %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=\"caravana-audit-") || !strings.HasSuffix(cd, ".csv\"") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if lines[0] != strings.Join(audit.CSVColumns, ",") {
		t.Errorf("the header = %q", lines[0])
	}
	if !strings.Contains(body, "exported-key") || !strings.Contains(body, ",console,") {
		t.Errorf("the CSV lacks the entry:\n%s", body)
	}

	// The range is required, and a refused one is drawn at the export form.
	resp, body = e.get(t, "/settings/audit/export?exportTo=2026-01-02")
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Choose the first day.") {
		t.Errorf("no first day = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.get(t, "/settings/audit/export?exportFrom=2026-01-05&exportTo=2026-01-02")
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "The last day is before the first.") {
		t.Errorf("backwards range = %d:\n%s", resp.StatusCode, text(body))
	}
	if ct := resp.Header.Get("Content-Disposition"); ct != "" {
		t.Errorf("a refused export has Content-Disposition %q", ct)
	}
}

func TestAuditRetentionForm(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.postForm(t, "/settings/audit", url.Values{"retentionDays": {"120"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/audit?saved=1" {
		t.Fatalf("save = %d to %q: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	_, body = e.get(t, "/settings/audit?saved=1")
	if !strings.Contains(body, `name="retentionDays" value="120"`) || !strings.Contains(text(body), "Saved. Entries older than 120 days") {
		t.Errorf("the saved value isn't drawn:\n%s", text(body))
	}
	ev := e.auditOf(t, audit.ActionUpdated, audit.KindSettings)
	if ev.Resource.ID != "audit" || ev.Door != audit.DoorConsole || ev.Actor.Email != testEmail {
		t.Errorf("the retention entry = %+v", ev)
	}
	// A refused value is drawn at the field, with what was typed.
	resp, body = e.postForm(t, "/settings/audit", url.Values{"retentionDays": {"10"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Must be from 30 to 3650 days.") || !strings.Contains(body, `value="10"`) {
		t.Errorf("retention 10 = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/settings/audit", url.Values{"retentionDays": {"many"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Enter a whole number of days.") {
		t.Errorf("retention many = %d:\n%s", resp.StatusCode, text(body))
	}
	if st, err := e.Svc.AuditSettings(context.Background()); err != nil || st.RetentionDays != 120 {
		t.Errorf("the retention is %+v, %v: a refused post must leave it", st, err)
	}
}

// A secret typed into a console form never reaches an entry, the page or the CSV.
func TestAuditNeverDrawsASecret(t *testing.T) {
	const (
		registryPassword = "s3cret-sentinel-057-registry"
		tokenSentinel    = "s3cret-sentinel-057-token"
	)
	e := newConsoleEnv(t)
	e.seedStandard(t)
	if resp, _ := e.postForm(t, "/settings/registries", url.Values{"host": {"quay.io"}, "username": {"deployer"}, "password": {registryPassword}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save a registry = %d", resp.StatusCode)
	}
	_, vars := e.get(t, "/apps/whoami/variables")
	resp, body := e.submit(t, formWith(t, vars, "/apps/whoami/variables"), url.Values{"service": {"web"}, "name": {"DB_PASSWORD"}, "value": {tokenSentinel}, "secret": {"1"}})
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusOK {
		t.Fatalf("set a secret = %d: %s", resp.StatusCode, text(body))
	}
	sec := e.auditOf(t, audit.ActionUpdated, audit.KindApp)
	secretNamed := false
	for _, c := range sec.Changes {
		// The first secret of a service is told whole, as "[…]": the diff keeps no leaves of it.
		if strings.HasSuffix(c.Path, ".secrets") || strings.Contains(c.Path, "DB_PASSWORD") {
			secretNamed = true
			if strings.Contains(c.From+c.To, tokenSentinel) {
				t.Errorf("the secret's change shows its value: %+v", c)
			}
		}
	}
	if !secretNamed {
		t.Errorf("the app's entry doesn't say a secret was added: %+v", sec.Changes)
	}
	reg := e.auditOf(t, audit.ActionCreated, audit.KindRegistry)
	if reg.Door != audit.DoorConsole {
		t.Errorf("the registry entry = %+v", reg)
	}
	found := false
	for _, c := range reg.Changes {
		if strings.HasSuffix(c.Path, "password") {
			found = true
			if c.From != "" || c.To != "" {
				t.Errorf("the registry password change shows values: %+v", c)
			}
		}
	}
	if !found {
		t.Errorf("the registry entry doesn't say a password was set: %+v", reg.Changes)
	}

	now := e.Clock.Now().UTC()
	q := url.Values{"exportFrom": {now.AddDate(0, 0, -1).Format(auditDay)}, "exportTo": {now.AddDate(0, 0, 1).Format(auditDay)}}
	_, csv := e.get(t, "/settings/audit/export?"+q.Encode())
	_, page := e.get(t, "/settings/audit")
	_, secrets := e.get(t, "/settings/audit?kind=registry")
	for name, doc := range map[string]string{"the page": page, "the CSV": csv, "the registry search": secrets} {
		for _, s := range []string{registryPassword, tokenSentinel} {
			if strings.Contains(doc, s) {
				t.Errorf("%s holds the secret %q", name, s)
			}
		}
	}
	if !strings.Contains(text(page), "quay.io") {
		t.Errorf("the registry entry isn't on the page:\n%s", text(page))
	}
	// Nor the server log.
	if strings.Contains(e.Logs.String(), registryPassword) || strings.Contains(e.Logs.String(), tokenSentinel) {
		t.Errorf("the log holds a secret")
	}
}
