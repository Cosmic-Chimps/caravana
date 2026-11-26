package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 054, T011: the console's access table.

// TestConsoleAccessCoversEveryRoute fails for a console route that is neither open on purpose nor
// in the table, and for a table entry that names no route.
func TestConsoleAccessCoversEveryRoute(t *testing.T) {
	served := map[string]bool{}
	for _, rt := range Routes() {
		key := rt.Method + " " + rt.Path
		served[key] = true
		_, ruled := consoleAccess[key]
		switch {
		case openRoutes[key] && ruled:
			t.Errorf("%s is open and also has a rule: pick one", key)
		case !openRoutes[key] && !ruled:
			t.Errorf("the console serves %s, which has no rule in consoleAccess: decide what it needs of a person, or say it is open on purpose", key)
		}
	}
	for key := range consoleAccess {
		if !served[key] {
			t.Errorf("consoleAccess has a rule for %s, which the console doesn't serve", key)
		}
	}
	for key := range openRoutes {
		if !served[key] {
			t.Errorf("openRoutes names %s, which the console doesn't serve", key)
		}
	}
}

// ---- the walk -------------------------------------------------------------------------------

// walkCaller is one person of the walk and how they come.
type walkCaller struct {
	name   string
	cookie *http.Cookie
}

// walkPeople makes the people of the walk: the owner (the environment's own sign-in), an admin, a member
// with view on shop › production and deploy on shop › staging and nothing in other, and a guest whose
// end date has passed (their session is over).
func walkPeople(t *testing.T, e *consoleEnv) []walkCaller {
	t.Helper()
	ctx := context.Background()
	for _, p := range []string{"shop", "other"} {
		if _, err := e.Svc.CreateProject(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	join := func(in service.PersonInput) *http.Cookie {
		t.Helper()
		inv, err := e.Svc.InvitePerson(ctx, in)
		if err != nil {
			t.Fatalf("invite %s: %v", in.Email, err)
		}
		p, err := e.Svc.AcceptInvite(ctx, inv.Token, "a long enough password 1", "203.0.113.5")
		if err != nil {
			t.Fatalf("accept %s: %v", in.Email, err)
		}
		sess, err := e.Auth.StartSessionFor(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "caravana_session", Value: sess.Value}
	}
	end := time.Now().Add(time.Hour)
	admin := join(service.PersonInput{Email: "admin2@example.com", OrgRole: service.OrgAdmin})
	member := join(service.PersonInput{Email: "member@example.com", OrgRole: service.OrgMember, Projects: []service.ProjectRole{
		{Project: "shop", Role: service.RoleView, Environments: map[string]service.Role{"staging": service.RoleDeploy}}}})
	guest := join(service.PersonInput{Email: "guest@example.com", OrgRole: service.OrgMember, EndsAt: &end, Projects: []service.ProjectRole{
		{Project: "shop", Role: service.RoleMaintain}}})
	e.Clock.Advance(2 * time.Hour) // the guest's end has passed; the others' sessions are idle for two hours, well inside twelve
	return []walkCaller{{"owner", e.Cookie}, {"admin", admin}, {"member", member}, {"ended guest", guest}}
}

// memberLevel is what the walk's member may do in a place: view in shop › production, deploy in shop ›
// staging, the project's view in a preview, nothing elsewhere. 0 none, 1 view, 2 deploy, 3 maintain.
func memberLevel(project, env string) int {
	switch {
	case project != "shop":
		return 0
	case env == "staging":
		return 2
	}
	return 1
}

// walkRoute is one request of the walk: the route's key, the request, and who it is open to.
type walkRequest struct {
	label string
	path  string
	form  url.Values
	// who says whether the owner and the admin (every signed-in person, any rule), the member (only
	// when open) get through.
	member bool
	admin  bool
	// ownerOnly says an admin is refused too: the route is the owner's (the audit log's retention).
	ownerOnly bool
	// redirects says a member who is refused is sent to their first visible place instead (a page
	// that defaults to a place they can't see).
	redirects bool
}

var appIDs = []struct {
	id, project, env string
}{
	{"shop--staging--web", "shop", "staging"},
	{"shop--production--web", "shop", "production"},
	{"other--production--web", "other", "production"},
	{"shop--pr-3--web", "shop", "pr-3"},
}

func walkKind(key string) string {
	_, path, _ := strings.Cut(key, " ")
	switch {
	case strings.HasPrefix(path, "/parts/"):
		return "part"
	case key == "POST /settings/sign-in/verify/options", key == "POST /settings/passkeys/options":
		return "json"
	}
	return "page"
}

// need is what a person must have in an app's place for a route, by the route's own key.
func appNeed(key string, env string) int {
	configure := 3
	if strings.HasPrefix(env, "pr-") {
		configure = 2
	}
	switch key {
	case "POST /apps/{id}/deploy", "POST /apps/{id}/deployments/{deploymentId}/redeploy", "POST /apps/{id}/restart",
		"POST /apps/{id}/start", "POST /apps/{id}/secrets/refresh", "POST /apps/{id}/backups":
		return 2
	case "POST /apps/{id}/configure", "POST /apps/{id}/define", "GET /apps/{id}/compose", "POST /apps/{id}/compose/preview",
		"POST /apps/{id}/compose", "POST /apps/{id}/variables", "POST /apps/{id}/variables/env", "POST /apps/{id}/secrets/remove",
		"POST /apps/{id}/domains", "POST /apps/{id}/backups/policy", "POST /apps/{id}/backups/restore-schedule",
		"POST /apps/{id}/backups/places", "POST /apps/{id}/services/{service}/placement", "POST /apps/{id}/services/{service}/scale",
		"POST /apps/{id}/services/{service}/health", "POST /apps/{id}/backups/policy/change",
		"POST /apps/{id}/services/{service}/build", "POST /apps/{id}/delete":
		return configure
	case "GET /apps/{id}/stop", "POST /apps/{id}/stop", "POST /apps/{id}/copies/{copyId}/run", "GET /apps/{id}/copies/{copyId}/terminal",
		"POST /apps/{id}/backups/{backupId}/restore", "POST /apps/{id}/backups/{backupId}/pin", "POST /apps/{id}/backups/{backupId}/unpin",
		"POST /apps/{id}/webhook", "POST /apps/{id}/webhook/revoke", "POST /apps/{id}/deploy-token",
		"POST /apps/{id}/services/{service}/git-access/deploy-key", "POST /apps/{id}/services/{service}/git-access/token",
		"POST /apps/{id}/services/{service}/git-access/remove":
		return 3
	}
	return 1 // the reads of an app
}

// fill puts walk values in a pattern's wildcards.
func fill(path string, values map[string]string) string {
	for name, v := range values {
		path = strings.ReplaceAll(path, "{"+name+"}", v)
	}
	// What a route names that the walk doesn't care about.
	for _, other := range []string{"deploymentId", "backupId", "copyId", "service", "runId", "tokenId", "kind", "rest", "volume", "name", "id", "host", "environment", "project"} {
		path = strings.ReplaceAll(path, "{"+other+"}", "x")
	}
	return path
}

// requestsFor lists what the walk tries for one route and who may do it, written out here apart from
// consoleAccess: by the route's own words, with the member's levels above.
func requestsFor(key string) []walkRequest {
	_, path, _ := strings.Cut(key, " ")
	only := func(admin bool) []walkRequest {
		return []walkRequest{{label: "plain", path: fill(path, nil), admin: true, member: !admin}}
	}
	if key == "POST /settings/audit" {
		return []walkRequest{{label: "plain", path: fill(path, nil), admin: true, ownerOnly: true}}
	}
	switch {
	// The installation: owners and admins.
	case strings.HasPrefix(path, "/ops"), strings.HasPrefix(path, "/parts/ops/"), strings.HasPrefix(path, "/settings/people"), strings.HasPrefix(path, "/settings/audit"),
		strings.HasPrefix(path, "/settings/registries"), strings.HasPrefix(path, "/settings/builds"),
		strings.HasPrefix(path, "/settings/secret-providers"), strings.HasPrefix(path, "/settings/backup"),
		strings.HasPrefix(path, "/settings/notifications"), strings.HasPrefix(path, "/settings/certificates"),
		strings.HasPrefix(path, "/settings/dns"), strings.HasPrefix(path, "/templates/check"), strings.HasPrefix(path, "/templates/settings"),
		path == "/parts/platform", path == "/parts/monitoring/host", path == "/parts/settings/certificates/drop", path == "/projects/new", key == "POST /projects",
		key == "POST /projects/{project}/delete", key == "POST /apps/{id}/security/scan":
		return only(true)
	// The person's own: signed in.
	case strings.HasPrefix(path, "/settings"), key == "GET /apps", path == "/parts/apps", path == "/parts/activity", path == "/parts/deploying",
		path == "/parts/attention", key == "GET /projects", key == "GET /projects/{project}", strings.HasPrefix(path, "/parts/projects/"),
		path == "/previews", path == "/parts/previews", key == "GET /schedules", path == "/parts/schedules", key == "GET /domains",
		path == "/parts/domains", path == "/parts/domains/add-service", path == "/apps/new", key == "GET /apps/new/template/{name}",
		path == "/templates/{name}/logo.svg", strings.HasPrefix(path, "/schedules/{id}"):
		return only(false)
	}

	switch {
	// Places the query names.
	case path == "/logs", path == "/parts/logs", path == "/logs/download", path == "/requests", path == "/parts/requests",
		path == "/monitoring", path == "/parts/monitoring":
		page := path == "/logs" || path == "/requests" || path == "/monitoring"
		out := []walkRequest{
			{label: "shop/staging", path: path + "?project=shop&environment=staging", admin: true, member: memberLevel("shop", "staging") >= 1},
			{label: "shop/production", path: path + "?project=shop&environment=production", admin: true, member: memberLevel("shop", "production") >= 1},
			{label: "shop, no environment", path: path + "?project=shop", admin: true, member: memberLevel("shop", "production") >= 1},
			{label: "other/production", path: path + "?project=other&environment=production", admin: true, member: memberLevel("other", "production") >= 1},
			{label: "no place", path: path, admin: true, member: false, redirects: page},
		}
		return out
	// An app's own routes.
	case strings.Contains(path, "{id}") && !strings.HasPrefix(path, "/schedules") && !strings.HasPrefix(path, "/settings") &&
		path != "/apps/{id}/promote" && path != "/apps/{id}/copy":
		var out []walkRequest
		for _, a := range appIDs {
			out = append(out, walkRequest{label: a.id, path: fill(path, map[string]string{"id": a.id}), admin: true,
				member: memberLevel(a.project, a.env) >= appNeed(key, a.env)})
		}
		return out
	case path == "/apps/{id}/promote" || path == "/apps/{id}/copy":
		need := 2
		if path == "/apps/{id}/copy" {
			need = 3
		}
		if key == "GET /apps/{id}/promote" {
			return []walkRequest{
				{label: "view source", path: "/apps/shop--production--web/promote?to=staging", admin: true, member: true},
				{label: "hidden source", path: "/apps/other--production--web/promote?to=staging", admin: true, member: false},
			}
		}
		var out []walkRequest
		for _, c := range []struct{ to, id, project, env string }{
			{"staging", "shop--production--web", "shop", "staging"},
			{"qa", "shop--production--web", "shop", "qa"},
			{"production", "shop--staging--web", "shop", "production"},
			{"BAD NAME", "shop--staging--web", "shop", "staging"}, // not a name: judged on the source itself
		} {
			out = append(out, walkRequest{label: c.id + " to " + c.to, path: fill(path, map[string]string{"id": c.id}),
				form: url.Values{"to": {c.to}}, admin: true, member: memberLevel(c.project, c.env) >= need})
		}
		return out
	// Making an app: the place's level, which a member of the walk never has.
	case key == "POST /apps", key == "POST /apps/new/github", key == "POST /apps/new/git", key == "POST /apps/new/template/{name}",
		key == "POST /apps/new/compose/preview", key == "POST /apps/new/compose":
		var out []walkRequest
		for _, place := range []string{"", "shop/staging", "shop/production", "other/production", "shop/pr-3", "not a place"} {
			out = append(out, walkRequest{label: "place " + place, path: fill(path, nil), form: url.Values{"place": {place}}, admin: true, member: false})
		}
		return out
	// A project's own pages.
	case strings.HasPrefix(path, "/projects/{project}"):
		var out []walkRequest
		for _, p := range []string{"shop", "other"} {
			level := 0
			if p == "shop" {
				level = 1 // the project role: view
			}
			switch key {
			case "GET /projects/{project}/settings":
				out = append(out, walkRequest{label: p, path: fill(path, map[string]string{"project": p}), admin: true, member: level >= 1})
			case "POST /projects/{project}/environments":
				for _, name := range []string{"qa", "pr-4"} {
					out = append(out, walkRequest{label: p + " " + name, path: fill(path, map[string]string{"project": p}), form: url.Values{"environment": {name}}, admin: true, member: false})
				}
			case "POST /projects/{project}/environments/{environment}/delete":
				for _, name := range []string{"qa", "pr-4"} {
					out = append(out, walkRequest{label: p + " " + name, path: fill(path, map[string]string{"project": p, "environment": name}), admin: true, member: false})
				}
			default: // settings posts, previews: maintain or deploy in the project, which the member's view isn't
				out = append(out, walkRequest{label: p, path: fill(path, map[string]string{"project": p, "environment": "pr-4"}), admin: true, member: false})
			}
		}
		return out
	// Schedules: the form of a new one names its app.
	case path == "/schedules/new" || key == "POST /schedules":
		return []walkRequest{
			{label: "no app", path: fill(path, nil), admin: true, member: true},
			{label: "app shop--staging--web", path: fill(path, nil) + queryIf(key, "app=shop--staging--web"), form: formIf(key, url.Values{"app": {"shop--staging--web"}}), admin: true, member: false},
		}
	// Domains: each row's routes are judged by the app that owns the host, which the walk has none of.
	case strings.HasPrefix(path, "/domains/{host}"), key == "POST /domains":
		if key == "POST /domains/{host}/check" {
			return []walkRequest{
				{label: "no app", path: fill(path, map[string]string{"host": "nowhere.test"}), admin: true, member: false},
				{label: "an app", path: fill(path, map[string]string{"host": "nowhere.test"}), form: url.Values{"app": {"shop--staging--web"}}, admin: true, member: false},
			}
		}
		if key == "POST /domains" {
			return []walkRequest{{label: "an app", path: path, form: url.Values{"app": {"shop--staging--web"}}, admin: true, member: false}}
		}
		return []walkRequest{{label: "a host nobody has", path: fill(path, map[string]string{"host": "nowhere.test"}), admin: true, member: false}}
	}
	return nil
}

func queryIf(key, q string) string {
	if strings.HasPrefix(key, "GET") {
		return "?" + q
	}
	return ""
}

func formIf(key string, v url.Values) url.Values {
	if strings.HasPrefix(key, "POST") {
		return v
	}
	return nil
}

// TestConsoleRolesWalkEveryRoute tries every console route that needs a session with four people and
// checks who gets through the guards against what is written above (requestsFor and the member's levels),
// not against consoleAccess:
//
//	owner        the environment's own sign-in
//	admin        another person with the admin role
//	member       view on shop › production, deploy on shop › staging, nothing in other
//	ended guest  a member whose end date has passed: their session is over
//
// The pages' own handlers are replaced by a 204 (afterGuard), so a request let through shows as 204 and
// one refused as what the guard answers: a 403 page "You can't do this here" (a page), a 403 fragment (a part),
// a 403 JSON error (a script's call), a redirect to /login or 401 for the guest, and for a member who
// asked for a page that defaults to a place they can't see, a redirect to their first visible place.
// The streams (/live, /live/apps/{id}) judge inside the stream, TestLiveStreamsFollowRoles.
func TestConsoleRolesWalkEveryRoute(t *testing.T) {
	e := newConsoleEnv(t)
	people := walkPeople(t, e)
	e.Server.afterGuard = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }

	tried := 0
	for _, rt := range Routes() {
		key := rt.Method + " " + rt.Path
		if openRoutes[key] || rt.Path == "/live" || rt.Path == "/live/apps/{id}" {
			continue
		}
		reqs := requestsFor(key)
		if len(reqs) == 0 {
			t.Errorf("the walk has no request for %s: say who may do it", key)
			continue
		}
		kind := walkKind(key)
		for _, wr := range reqs {
			for _, c := range people {
				method, _, _ := strings.Cut(key, " ")
				resp, body := e.do(t, request{method: method, path: wr.path, form: wr.form, cookies: []*http.Cookie{c.cookie}})
				tried++
				where := key + " [" + wr.label + "] as " + c.name
				switch c.name {
				case "ended guest":
					wantStatus := http.StatusSeeOther
					if kind != "page" {
						wantStatus = http.StatusUnauthorized
					}
					if resp.StatusCode != wantStatus {
						t.Errorf("%s = %d, want %d (the session is over)", where, resp.StatusCode, wantStatus)
					}
				case "owner", "admin":
					if c.name == "admin" && wr.ownerOnly {
						if resp.StatusCode != http.StatusForbidden {
							t.Errorf("%s = %d, want 403: only an owner may", where, resp.StatusCode)
						}
						break
					}
					if resp.StatusCode != http.StatusNoContent {
						t.Errorf("%s = %d %q, want it let through", where, resp.StatusCode, shortLine(body))
					}
				case "member":
					switch {
					case wr.member:
						if resp.StatusCode != http.StatusNoContent {
							t.Errorf("%s = %d %q, want it let through", where, resp.StatusCode, shortLine(body))
						}
					case wr.redirects:
						loc := resp.Header.Get("Location")
						if resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "project=shop") {
							t.Errorf("%s = %d to %q, want a redirect to the member's first place (shop)", where, resp.StatusCode, loc)
						}
					default:
						if resp.StatusCode != http.StatusForbidden {
							t.Errorf("%s = %d, want 403", where, resp.StatusCode)
							break
						}
						switch kind {
						case "page":
							if !strings.Contains(body, "You can&#39;t do this here") && !strings.Contains(body, "You can't do this here") {
								t.Errorf("%s: the 403 page doesn't say %q:\n%s", where, "You can't do this here", shortLine(body))
							}
						case "part":
							if !strings.Contains(body, `<p class="cv-hint">`) {
								t.Errorf("%s: the 403 part isn't a fragment: %q", where, body)
							}
						case "json":
							if !strings.Contains(body, `"not_allowed"`) {
								t.Errorf("%s: the 403 JSON isn't not_allowed: %q", where, body)
							}
						}
					}
				}
			}
		}
	}
	if tried < 1500 {
		t.Errorf("the walk made only %d requests: it isn't walking the table", tried)
	}
}

func shortLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// A route nobody listed is refused for everyone, an owner included.
func TestConsoleRouteWithNoRuleIsRefused(t *testing.T) {
	e := newConsoleEnv(t)
	saved := consoleAccess["GET /parts/attention"]
	delete(consoleAccess, "GET /parts/attention")
	t.Cleanup(func() { consoleAccess["GET /parts/attention"] = saved })
	resp, body := e.get(t, "/parts/attention")
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "isn&#39;t open to your role") && !strings.Contains(body, "isn't open to your role") {
		t.Errorf("a part with no rule = %d %q, want 403 for the owner too", resp.StatusCode, body)
	}
}
