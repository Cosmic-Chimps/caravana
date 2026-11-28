package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// walkClasses is what every protected route needs, written out here on purpose, apart from
// accessRules, so that opening a route to more people takes two edits and a reviewer sees both. The
// classes are plan D4's table (specs/053-people-and-roles/plan.md):
//
//	signed            any signed-in person (their own account, and lists the service filters for them;
//	                  the schedule routes by ID, which the service judges by the schedule's app)
//	admin             an owner or an admin
//	owner             an owner only
//	app:view|deploy|maintain
//	                  that level in the place of the app named by {id}
//	appcfg            maintain in an environment, deploy in a preview: the level that changes an app
//	place:view        view in the project and environment of the path
//	qplace:view       view in the place the query names (Default's production when it names none)
//	project:maintain, project:deploy
//	                  that level in the role of the project of the path
//	envcreate, envdelete
//	                  project deploy for a preview (pr-<n>), project maintain for any other environment
//	createapp         the creating level (appcfg's) in the place of the body's app ID
//	createinplace     the same, in the place of the path
//	promote           deploy on the app of that name in the body's "to" environment
//	copy              maintain on it
//	domcheck          maintain on the body's app, an admin with none
//	schedcreate       maintain on the body's app
var walkClasses = map[string][]string{
	"signed": {
		"GET /v1/me", "PUT /v1/account/password", "GET /v1/account/passkeys", "PATCH /v1/account/passkeys/{passkeyId}",
		"DELETE /v1/account/passkeys/{passkeyId}", "GET /v1/account/sign-in", "PUT /v1/account/sign-in",
		"POST /v1/tokens", "GET /v1/tokens", "DELETE /v1/tokens/{tokenId}",
		"GET /v1/apps", "GET /v1/deployments", "GET /v1/attention", "GET /v1/schedules", "GET /v1/domains", "GET /v1/domains/{host}",
		"GET /v1/projects", "GET /v1/projects/{project}", "GET /v1/projects/{project}/previews", "GET /v1/logs/around",
		"GET /v1/templates", "GET /v1/templates/{name}", "GET /v1/templates/{name}/logo",
		"GET /v1/schedules/{scheduleId}", "PUT /v1/schedules/{scheduleId}", "DELETE /v1/schedules/{scheduleId}",
		"POST /v1/schedules/{scheduleId}/run", "POST /v1/schedules/{scheduleId}/pause", "POST /v1/schedules/{scheduleId}/resume",
		"GET /v1/schedules/{scheduleId}/runs", "GET /v1/schedules/{scheduleId}/runs/{runId}",
		"POST /v1/schedules/{scheduleId}/runs/{runId}/stop", "GET /v1/schedules/{scheduleId}/runs/{runId}/output",
	},
	"owner": {"PUT /v1/audit/settings", "PUT /v1/self-backups/settings", "POST /v1/self-backups/recovery-key"},
	"admin": {
		"GET /v1/audit", "GET /v1/audit/export", "GET /v1/audit/settings",
		"GET /v1/people", "POST /v1/people", "GET /v1/people/{personId}", "PUT /v1/people/{personId}", "DELETE /v1/people/{personId}",
		"POST /v1/people/{personId}/invite",
		"POST /v1/projects", "DELETE /v1/projects/{project}", "GET /v1/host", "GET /v1/activity",
		"GET /v1/addons", "PUT /v1/addons/{name}", "DELETE /v1/addons/{name}",
		"GET /v1/scans", "POST /v1/scans", "GET /v1/scans/runs", "GET /v1/scans/settings", "PUT /v1/scans/settings",
		"GET /v1/updates", "PUT /v1/updates/settings", "POST /v1/updates/check", "POST /v1/updates/runs", "GET /v1/updates/runs",
		"GET /v1/console-address", "POST /v1/console-address/changes", "GET /v1/console-address/changes/{id}",
		"DELETE /v1/console-address/changes/{id}", "POST /v1/console-address/changes/{id}/switch",
		"POST /v1/templates/check", "PUT /v1/templates/settings",
		"GET /v1/servers", "POST /v1/servers", "GET /v1/servers/joins/{id}", "GET /v1/servers/{name}", "DELETE /v1/servers/{name}",
		"PUT /v1/servers/{name}/labels", "POST /v1/servers/{name}/drain", "DELETE /v1/servers/{name}/drain",
		"GET /v1/servers/{name}/space", "GET /v1/servers/{name}/cleanups", "POST /v1/servers/{name}/cleanups",
		"GET /v1/servers/{name}/cleanups/{id}", "POST /v1/volumes/{name}/deletions", "GET /v1/volumes/{name}/deletions/{id}",
		"GET /v1/cleanup/settings", "PUT /v1/cleanup/settings",
		"GET /v1/backups/destination", "PUT /v1/backups/destination", "GET /v1/backups/destinations",
		"PUT /v1/backups/destinations/{name}", "DELETE /v1/backups/destinations/{name}", "POST /v1/backups/destinations/{name}/test",
		"GET /v1/registries", "PUT /v1/registries/{host}", "DELETE /v1/registries/{host}", "POST /v1/registries/{host}/test",
		"GET /v1/secret-providers", "PUT /v1/secret-providers/{name}", "DELETE /v1/secret-providers/{name}", "POST /v1/secret-providers/{name}/test",
		"GET /v1/dns/zones", "PUT /v1/dns/zones/{zone}", "DELETE /v1/dns/zones/{zone}", "POST /v1/dns/zones/{zone}/test", "GET /v1/dns/records",
		"GET /v1/notifications/channels", "POST /v1/notifications/channels", "PATCH /v1/notifications/channels/{id}",
		"DELETE /v1/notifications/channels/{id}", "POST /v1/notifications/channels/{id}/test",
		"GET /v1/notifications/channels/{id}/deliveries", "POST /v1/notifications/channels/{id}/secret",
		"GET /v1/settings/certificates", "PUT /v1/settings/certificates", "GET /v1/certificates",
		"PUT /v1/settings/certificates/issuer", "DELETE /v1/settings/certificates/issuer",
		"GET /v1/certificates/uploaded", "POST /v1/certificates/uploaded", "GET /v1/certificates/uploaded/{id}",
		"DELETE /v1/certificates/uploaded/{id}",
		"POST /v1/certificates/drops", "GET /v1/certificates/drops", "GET /v1/certificates/drops/{id}",
		"GET /v1/self-backups", "POST /v1/self-backups/runs", "GET /v1/self-backups/runs", "GET /v1/self-backups/runs/{id}",
		"GET /v1/builds/settings", "PUT /v1/builds/settings",
	},
	"app:view": {
		"GET /v1/apps/{id}", "GET /v1/apps/{id}/copies", "GET /v1/apps/{id}/volumes", "GET /v1/apps/{id}/actions", "GET /v1/apps/{id}/logs",
		"GET /v1/apps/{id}/metrics", "GET /v1/apps/{id}/compose", "GET /v1/apps/{id}/deployments",
		"GET /v1/apps/{id}/deployments/{deploymentId}", "GET /v1/apps/{id}/deployments/{deploymentId}/compose",
		"GET /v1/apps/{id}/deployments/{deploymentId}/jobspec", "GET /v1/apps/{id}/deployments/{deploymentId}/build",
		"GET /v1/apps/{id}/deployments/{deploymentId}/scan", "GET /v1/apps/{id}/jobspec", "GET /v1/apps/{id}/scan",
		"GET /v1/apps/{id}/backups", "GET /v1/apps/{id}/webhook", "GET /v1/apps/{id}/services/{service}/git-access",
		"POST /v1/apps/{id}/promote/preview",
	},
	"app:deploy": {
		"POST /v1/apps/{id}/deployments", "POST /v1/apps/{id}/deployments/{deploymentId}/redeploy", "POST /v1/apps/{id}/restart",
		"POST /v1/apps/{id}/start", "POST /v1/apps/{id}/secrets/refresh", "POST /v1/apps/{id}/backups",
	},
	"app:maintain": {
		"POST /v1/apps/{id}/stop", "POST /v1/apps/{id}/copies/{copyId}/run", "GET /v1/apps/{id}/copies/{copyId}/terminal",
		"GET /v1/apps/{id}/copies/{copyId}/files", "GET /v1/apps/{id}/copies/{copyId}/files/download",
		"POST /v1/apps/{id}/backups/{backupId}/restore", "PUT /v1/apps/{id}/backups/{backupId}/pin",
		"DELETE /v1/apps/{id}/backups/{backupId}/pin", "POST /v1/apps/{id}/webhook", "DELETE /v1/apps/{id}/webhook",
		"POST /v1/apps/{id}/domains/{host}/team-password", "POST /v1/apps/{id}/services/{service}/git-access/deploy-key",
		"PUT /v1/apps/{id}/services/{service}/git-access/token", "DELETE /v1/apps/{id}/services/{service}/git-access",
	},
	"appcfg": {"DELETE /v1/apps/{id}", "POST /v1/apps/{id}/compose", "POST /v1/apps/{id}/compose/preview"},
	"place:view": {
		"GET /v1/projects/{project}/environments/{environment}/apps",
		"GET /v1/projects/{project}/environments/{environment}/apps/{name}",
	},
	"qplace:view":      {"GET /v1/metrics", "GET /v1/logs", "GET /v1/logs/download", "GET /v1/requests"},
	"project:maintain": {"PATCH /v1/projects/{project}", "PUT /v1/projects/{project}/previews/settings"},
	"project:deploy":   {"DELETE /v1/projects/{project}/previews/{environment}"},
	"envcreate":        {"POST /v1/projects/{project}/environments"},
	"envdelete":        {"DELETE /v1/projects/{project}/environments/{environment}"},
	"createapp":        {"POST /v1/apps", "POST /v1/templates/{name}/preview", "POST /v1/templates/{name}/install"},
	"createinplace":    {"POST /v1/projects/{project}/environments/{environment}/apps"},
	"promote":          {"POST /v1/apps/{id}/promote"},
	"copy":             {"POST /v1/apps/{id}/copy"},
	"domcheck":         {"POST /v1/domains/check"},
	"schedcreate":      {"POST /v1/schedules"},
}

// The member of the walk, written out: view on shop's production, deploy on shop's staging, and nothing in
// other (or anywhere else). A preview of shop takes the project role (view).
func memberLevel(project, environment string) int {
	switch {
	case project == "shop" && environment == "staging":
		return 2
	case project == "shop":
		return 1
	}
	return 0
}

// memberProjectLevel is the member's role in a project itself, whatever the overrides say.
func memberProjectLevel(project string) int {
	if project == "shop" {
		return 1
	}
	return 0
}

const (
	lvView = 1 + iota
	lvDeploy
	lvMaintain
)

// walkTarget is an app or a place a request names.
type walkTarget struct {
	label, id, project, env, name string
}

// The apps and places the walk aims at. The preview is shop's pr-3.
var walkTargets = []walkTarget{
	{"shop/staging", "shop--staging--web", "shop", "staging", "web"},
	{"shop/production", "shop--production--web", "shop", "production", "web"},
	{"other/production", "other--production--web", "other", "production", "web"},
	{"shop/preview", "shop--pr-3--web", "shop", "pr-3", "web"},
}

// walkRequest is one request of the walk and what it needs of a person.
type walkRequest struct {
	label, path, body string
	// kind says how a member is judged: "signed" (any), "admin", "place" (need at project and env) or
	// "project" (need in the project's own role).
	kind         string
	need         int
	project, env string
	scopeProject string // the project and app name a deploy token's scope reads from the request
	scopeName    string
	scopePreview bool // the request names a preview environment (the narrow routes of 049)
}

func previewEnv(env string) bool { return strings.HasPrefix(env, "pr-") }

// configureNeed is the level that changes an app in an environment.
func configureNeed(env string) int {
	if previewEnv(env) {
		return lvDeploy
	}
	return lvMaintain
}

// walkRequests are the requests the walk sends for one route of the class (variants: see the doc of
// TestRolesWalkEveryRoute).
func walkRequests(class, key string, pathPattern string) []walkRequest {
	fill := func(tg walkTarget) string {
		r := strings.NewReplacer("{id}", tg.id, "{project}", tg.project, "{environment}", tg.env, "{name}", tg.name)
		return pathParam.ReplaceAllString(r.Replace(pathPattern), "x")
	}
	var out []walkRequest
	switch class {
	case "signed":
		out = append(out, walkRequest{label: "any", path: fill(walkTargets[0]), kind: "signed"})
	case "admin":
		out = append(out, walkRequest{label: "any", path: fill(walkTargets[0]), kind: "admin"})
	case "owner":
		out = append(out, walkRequest{label: "any", path: fill(walkTargets[0]), kind: "owner"})
	case "app:view", "app:deploy", "app:maintain", "appcfg":
		for _, tg := range walkTargets {
			need := map[string]int{"app:view": lvView, "app:deploy": lvDeploy, "app:maintain": lvMaintain, "appcfg": configureNeed(tg.env)}[class]
			out = append(out, walkRequest{label: tg.label, path: fill(tg), kind: "place", need: need, project: tg.project, env: tg.env,
				scopeProject: tg.project, scopeName: tg.name, scopePreview: previewEnv(tg.env)})
		}
	case "place:view":
		for _, tg := range walkTargets {
			out = append(out, walkRequest{label: tg.label, path: fill(tg), kind: "place", need: lvView, project: tg.project, env: tg.env,
				scopeProject: tg.project, scopeName: tg.name, scopePreview: previewEnv(tg.env)})
		}
	case "qplace:view":
		for _, tg := range walkTargets {
			out = append(out, walkRequest{label: tg.label, path: fill(tg) + "?project=" + tg.project + "&environment=" + tg.env,
				kind: "place", need: lvView, project: tg.project, env: tg.env})
		}
		out = append(out, walkRequest{label: "default place", path: fill(walkTargets[0]), kind: "place", need: lvView, project: "default", env: "production"})
	case "project:maintain", "project:deploy":
		need := map[string]int{"project:maintain": lvMaintain, "project:deploy": lvDeploy}[class]
		for _, p := range []string{"shop", "other"} {
			tg := walkTarget{label: p, project: p, env: "pr-3", name: "web"}
			out = append(out, walkRequest{label: p, path: fill(tg), kind: "project", need: need, project: p,
				scopeProject: p, scopePreview: true})
		}
	case "envcreate":
		for _, p := range []string{"shop", "other"} {
			for _, name := range []string{"qa", "pr-3"} {
				tg := walkTarget{project: p, env: name, name: "web"}
				need := lvMaintain
				if previewEnv(name) {
					need = lvDeploy
				}
				out = append(out, walkRequest{label: p + "/" + name, path: fill(tg), body: `{"name":"` + name + `"}`, kind: "project", need: need,
					project: p, scopeProject: p, scopePreview: previewEnv(name)})
			}
		}
	case "envdelete":
		for _, p := range []string{"shop", "other"} {
			for _, name := range []string{"qa", "pr-3"} {
				tg := walkTarget{project: p, env: name, name: "web"}
				need := lvMaintain
				if previewEnv(name) {
					need = lvDeploy
				}
				out = append(out, walkRequest{label: p + "/" + name, path: fill(tg), kind: "project", need: need,
					project: p, scopeProject: p, scopePreview: previewEnv(name)})
			}
		}
	case "createapp":
		for _, tg := range walkTargets {
			out = append(out, walkRequest{label: tg.label, path: fill(tg), body: `{"id":"` + tg.id + `"}`, kind: "place",
				need: configureNeed(tg.env), project: tg.project, env: tg.env, scopeProject: tg.project, scopeName: tg.name})
		}
		out = append(out, walkRequest{label: "an unreadable body", path: fill(walkTargets[0]), body: `nope`, kind: "admin"})
	case "createinplace":
		for _, tg := range walkTargets {
			out = append(out, walkRequest{label: tg.label, path: fill(tg), body: `{"appName":"` + tg.name + `"}`, kind: "place",
				need: configureNeed(tg.env), project: tg.project, env: tg.env, scopeProject: tg.project, scopeName: tg.name})
		}
	case "promote", "copy":
		need := map[string]int{"promote": lvDeploy, "copy": lvMaintain}[class]
		for _, c := range []struct {
			from walkTarget
			to   string
		}{
			{walkTargets[0], "production"}, // into shop's production
			{walkTargets[1], "staging"},    // into shop's staging
			{walkTargets[2], "qa"},         // into other's qa
		} {
			out = append(out, walkRequest{label: c.from.label + " to " + c.to, path: fill(c.from), body: `{"to":"` + c.to + `"}`, kind: "place",
				need: need, project: c.from.project, env: c.to, scopeProject: c.from.project, scopeName: c.from.name})
		}
		// A "to" that is no name: judged on the source, and the handler's refusal follows.
		out = append(out, walkRequest{label: "an invalid to", path: fill(walkTargets[0]), body: `{"to":"No Way"}`, kind: "place",
			need: need, project: "shop", env: "staging", scopeProject: "shop", scopeName: "web"})
	case "domcheck":
		for _, tg := range walkTargets {
			out = append(out, walkRequest{label: tg.label, path: fill(tg), body: `{"host":"x.example.com","app":"` + tg.id + `"}`, kind: "place",
				need: lvMaintain, project: tg.project, env: tg.env})
		}
		out = append(out, walkRequest{label: "no app", path: fill(walkTargets[0]), body: `{"host":"x.example.com"}`, kind: "admin"})
	case "schedcreate":
		for _, tg := range walkTargets {
			out = append(out, walkRequest{label: tg.label, path: fill(tg), body: `{"app":"` + tg.id + `"}`, kind: "place",
				need: lvMaintain, project: tg.project, env: tg.env})
		}
	default:
		panic("unknown class " + class + " for " + key)
	}
	return out
}

// walkCaller is one way of calling, with what it is.
type walkCaller struct {
	name string
	call func(method, url, body string) (int, string)
}

// expectation is whether the caller passes the access check for the request (it then reaches the
// handler, which the walk replaces with one that says 204).
func (wr walkRequest) allowedFor(caller, key string) bool {
	member := func() bool {
		switch wr.kind {
		case "signed":
			return true
		case "admin", "owner":
			return false
		case "project":
			return memberProjectLevel(wr.project) >= wr.need
		}
		return memberLevel(wr.project, wr.env) >= wr.need
	}
	switch caller {
	case "owner":
		return true
	case "admin token":
		return wr.kind != "owner" // an owner's work, such as the audit retention, is not an admin's
	case "member token":
		return member()
	case "deploy token":
		// The scope first (the contract of 020 and 049), then the person behind it.
		return wr.inScope(key) && member()
	}
	return false // an ended guest is refused before anything else
}

// inScope is whether a deploy token for shop/web may make the request at all.
func (wr walkRequest) inScope(key string) bool {
	if !slices.Contains(allowedForDeployTokens, key) || wr.scopeProject != "shop" {
		return false
	}
	if slices.Contains(previewScoped, key) {
		return wr.scopePreview && (wr.scopeName == "" || wr.scopeName == "web")
	}
	return wr.scopeName == "web"
}

// stubbed is the routes with every handler replaced by one that answers 204: the walk is about who
// gets through authenticate, not about what a handler does.
func stubbed(s *server) []route {
	routes := s.routes()
	for i := range routes {
		routes[i].handle = func(w http.ResponseWriter, r *http.Request) error { w.WriteHeader(http.StatusNoContent); return nil }
	}
	return routes
}

// walkEnv makes the people of the walk on a service with projects shop (production, staging) and other.
func walkEnv(t *testing.T) (*testEnv, []walkCaller, *server, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	e := newTestEnv(t, "")
	for _, p := range []string{"shop", "other"} {
		if _, err := e.Service.CreateProject(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Service.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	now := e.Clock.Now()
	person := func(email, role string, ends *time.Time, roles []store.ProjectRoleRecord) store.PersonRecord {
		t.Helper()
		p := store.PersonRecord{ID: strings.ReplaceAll(strings.ToLower(email), "@example.com", "") + "00000000000000000000000", Email: email, OrgRole: role,
			State: "active", PasswordHash: "x", PasswordSignIn: true, EndsAt: ends, CreatedAt: now}
		p.ID = p.ID[:26]
		if err := e.Store.CreatePerson(ctx, p, roles); err != nil {
			t.Fatal(err)
		}
		return p
	}
	token := func(id, name string, scope *service.TokenScope) string {
		t.Helper()
		c, err := e.Auth.CreateScopedAPIToken(ctx, id, name, nil, scope)
		if err != nil {
			t.Fatalf("token %s: %v", name, err)
		}
		return c.Token
	}
	admin := person("admin2@example.com", "admin", nil, nil)
	member := person("member@example.com", "member", nil, []store.ProjectRoleRecord{
		{Project: "shop", Role: "view", Environments: map[string]string{"staging": "deploy"}}})
	end := now.Add(time.Hour)
	guest := person("guest@example.com", "member", &end, []store.ProjectRoleRecord{{Project: "shop", Role: "maintain"}})

	adminTok := token(admin.ID, "admin-walk", nil)
	memberTok := token(member.ID, "member-walk", nil)
	deployTok := token(member.ID, "deploy-walk", &service.TokenScope{Project: "shop", App: "web"})
	guestTok := token(guest.ID, "guest-walk", nil)
	cookie := sessionCookie(t, e)
	e.Clock.Advance(2 * time.Hour) // the guest's end has passed; the owner's session, idle for two hours, is alive

	s := newServer(Deps{Service: e.Service, Auth: e.Auth, Store: e.Store, Log: e.log})
	stub := httptest.NewServer(s.handler(stubbed(s)))
	t.Cleanup(stub.Close)

	bearer := func(tok string) func(method, url, body string) (int, string) {
		return func(method, url, body string) (int, string) {
			status, _, raw := callWith(t, "Bearer "+tok, method, stub.URL+url, "application/json", body)
			return status, string(raw)
		}
	}
	callers := []walkCaller{
		{"owner", func(method, url, body string) (int, string) {
			status, raw := cookieCall(t, cookie, method, stub.URL+url, body, nil)
			return status, string(raw)
		}},
		{"admin token", bearer(adminTok)},
		{"member token", bearer(memberTok)},
		{"deploy token", bearer(deployTok)},
		{"ended guest", bearer(guestTok)},
	}
	return e, callers, s, cookie
}

// TestRolesWalkEveryRoute tries every protected route with five callers and checks who gets through the
// access check against the table above (walkClasses and the member's levels), not against accessRules:
//
//	owner         a signed-in session of the owner
//	admin token   a token of an admin
//	member token  a token of a member with deploy on shop/staging, view on shop/production, nothing in other
//	deploy token  that member's deploy token for app web of shop (the scope, then the person)
//	ended guest   a token of a member whose end date has passed
//
// Each route is tried with the variants its class says: apps shop--staging--web, shop--production--web,
// other--production--web and the preview shop--pr-3--web (app routes, place routes, the places the query
// names, the body's app for creates, schedules, domain checks), the projects shop and other (project
// routes), environment names qa and pr-3 (create and delete an environment), the sources and targets
// staging→production, production→staging, other→qa and an invalid "to" (promote, copy), a body with no
// app (domain check) and an unreadable body (create an app). The handlers are stubbed to 204, so a
// request that is let through shows as anything but 401 and 403. The schedule routes by ID are
// "signed" here: their judgement is in the service (TestScheduleRoutesAreJudgedByTheirApp).
func TestRolesWalkEveryRoute(t *testing.T) {
	_, callers, s, ownerCookie := walkEnv(t)

	classOf := map[string]string{}
	for class, keys := range walkClasses {
		for _, key := range keys {
			if prev, dup := classOf[key]; dup {
				t.Fatalf("%s is in two classes: %s and %s", key, prev, class)
			}
			classOf[key] = class
		}
	}

	sent := 0
	for _, rt := range protectedRoutes(t) {
		key := rt.method + " " + rt.path
		class, ok := classOf[key]
		if !ok {
			t.Errorf("%s has no class in walkClasses", key)
			continue
		}
		t.Run(key, func(t *testing.T) {
			for _, wr := range walkRequests(class, key, rt.path) {
				for _, c := range callers {
					status, raw := c.call(rt.method, wr.path, wr.body)
					sent++
					want := wr.allowedFor(c.name, key)
					denied := status == http.StatusUnauthorized || status == http.StatusForbidden
					if denied == want {
						t.Errorf("%s as the %s (%s): %d %s; want it %s", key, c.name, wr.label, status, strings.TrimSpace(raw), map[bool]string{true: "let through", false: "refused"}[want])
						continue
					}
					if want {
						continue
					}
					// What the refusal says: the guest's credentials are over; anyone else is refused for what they may do.
					switch {
					case c.name == "ended guest" && status != http.StatusUnauthorized:
						t.Errorf("%s as the ended guest: %d, want 401", key, status)
					case c.name == "deploy token" && !wr.inScope(key) && !strings.Contains(raw, `"token_scope"`):
						t.Errorf("%s as the deploy token (%s) out of scope: %s, want token_scope", key, wr.label, raw)
					case c.name != "ended guest" && !(c.name == "deploy token" && !wr.inScope(key)) && !strings.Contains(raw, `"not_allowed"`):
						t.Errorf("%s as the %s (%s): %s, want not_allowed", key, c.name, wr.label, raw)
					}
				}
			}
		})
	}
	if sent < 1500 {
		t.Errorf("the walk sent %d requests; it should be well over a thousand", sent)
	}

	t.Run("a route no rule lists is refused for everyone, owners included", func(t *testing.T) {
		routes := stubbed(s)
		okHandler := routes[0].handle
		routes = append(routes,
			route{method: http.MethodGet, path: "/v1/zzz-no-rule", handle: okHandler},
			route{method: http.MethodGet, path: "/v1/zzz-bad-request", handle: okHandler})
		accessRules["GET /v1/zzz-bad-request"] = func(*http.Request) (service.Need, service.Target, bool) {
			return service.NeedSignedIn(), service.Target{}, false // the request says nothing
		}
		t.Cleanup(func() { delete(accessRules, "GET /v1/zzz-bad-request") })
		srv := httptest.NewServer(s.handler(routes))
		defer srv.Close()
		for _, path := range []string{"/v1/zzz-no-rule", "/v1/zzz-bad-request"} {
			status, raw := cookieCall(t, ownerCookie, http.MethodGet, srv.URL+path, "", nil)
			if status != http.StatusForbidden || !strings.Contains(string(raw), `"not_allowed"`) || !strings.Contains(string(raw), "this isn't open to your role") {
				t.Errorf("GET %s for the owner = %d %s; want 403 not_allowed, this isn't open to your role", path, status, raw)
			}
		}
	})
}

// TestAccessRulesCoverEveryRoute makes a new route get a rule, and the table of the test the same class.
func TestAccessRulesCoverEveryRoute(t *testing.T) {
	protected := map[string]bool{}
	var missing []string
	for _, rt := range protectedRoutes(t) {
		key := rt.method + " " + rt.path
		protected[key] = true
		if _, ok := accessRules[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("protected routes with no access rule (they are refused for everyone until one is written):\n  %s", strings.Join(missing, "\n  "))
	}
	for key := range accessRules {
		if !protected[key] {
			t.Errorf("accessRules has %q, which is not a protected route", key)
		}
	}
	inClass := map[string]bool{}
	for _, keys := range walkClasses {
		for _, key := range keys {
			inClass[key] = true
			if !protected[key] {
				t.Errorf("walkClasses has %q, which is not a protected route", key)
			}
		}
	}
	for key := range protected {
		if !inClass[key] {
			t.Errorf("walkClasses has no class for %q", key)
		}
	}
	// The public routes are the ones the contract says, and none has a rule.
	var public []string
	for _, rt := range (&server{}).routes() {
		if rt.public {
			public = append(public, rt.method+" "+rt.path)
			if _, ok := accessRules[rt.method+" "+rt.path]; ok {
				t.Errorf("public route %s has an access rule", rt.method+" "+rt.path)
			}
		}
	}
	slices.Sort(public)
	want := []string{"GET /.well-known/jwks.json", "GET /.well-known/openid-configuration", "GET /v1/health", "POST /v1/invites/accept",
		"POST /v1/join", "POST /v1/node/certificate", "POST /v1/node/sync", "POST /v1/webhooks/{id}"}
	if !slices.Equal(public, want) {
		t.Errorf("public routes = %v, want %v", public, want)
	}
}
