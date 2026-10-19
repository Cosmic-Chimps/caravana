package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// allowedForDeployTokens is the contract's table (specs/020-deploy-from-ci/
// contracts/api.md): the only routes a deploy token may use. It is written out
// here on purpose, apart from scopeRules, so that allowing one more route takes
// two edits and a reviewer sees both.
var allowedForDeployTokens = []string{
	"GET /v1/apps/{id}",                                                 // getApp
	"GET /v1/apps/{id}/compose",                                         // getCompose
	"GET /v1/apps/{id}/deployments",                                     // listDeployments
	"GET /v1/apps/{id}/deployments/{deploymentId}",                      // getDeployment
	"POST /v1/apps/{id}/deployments",                                    // deployApp
	"POST /v1/apps/{id}/compose",                                        // deployCompose
	"POST /v1/apps/{id}/promote/preview",                                // previewPromotion
	"POST /v1/apps/{id}/promote",                                        // promoteApp
	"POST /v1/apps",                                                     // createApp
	"POST /v1/projects/{project}/environments/{environment}/apps",       // createEnvironmentApp
	"GET /v1/projects/{project}/environments/{environment}/apps/{name}", // getEnvironmentApp
}

// scopedToken makes a deploy token for app name in project and returns the
// Authorization header value.
func scopedToken(t *testing.T, e *testEnv, project, name string) string {
	t.Helper()
	created, err := e.Auth.CreateScopedAPIToken(context.Background(), project+"-"+name+"-ci", nil,
		&service.TokenScope{Project: project, App: name})
	if err != nil {
		t.Fatalf("CreateScopedAPIToken(%s, %s): %v", project, name, err)
	}
	return "Bearer " + created.Token
}

// shopProject makes project shop with a staging environment, through the API.
func shopProject(t *testing.T, e *testEnv) {
	t.Helper()
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects", "application/json", `{"name": "shop"}`); status != http.StatusCreated {
		t.Fatalf("create project = %d %s", status, raw)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects/shop/environments", "application/json", `{"name": "staging"}`); status != http.StatusCreated {
		t.Fatalf("create environment = %d %s", status, raw)
	}
}

func TestScopeRulesAreTheContract(t *testing.T) {
	var ruled []string
	for key := range scopeRules {
		ruled = append(ruled, key)
	}
	slices.Sort(ruled)
	want := slices.Clone(allowedForDeployTokens)
	slices.Sort(want)
	if !slices.Equal(ruled, want) {
		t.Errorf("scopeRules allows\n  %s\nbut the contract's table is\n  %s", strings.Join(ruled, "\n  "), strings.Join(want, "\n  "))
	}

	routes := map[string]route{}
	for _, rt := range (&server{}).routes() {
		routes[rt.method+" "+rt.path] = rt
	}
	for _, key := range ruled {
		if rt, ok := routes[key]; !ok || rt.public {
			t.Errorf("scopeRules has %q, which is not a protected route", key)
		}
	}
}

// TestScopedTokenWalksEveryRoute tries every protected route with a deploy
// token for shop/web: the ones outside the contract's table answer 403
// token_scope whatever the request says, the ones in it answer 403 for another
// app, and don't for the token's own.
func TestScopedTokenWalksEveryRoute(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")

	// What the token may name: its own app, in a place that is in scope, and
	// others that are not (another name, another project, the default project).
	type target struct {
		id, project, environment, name string
	}
	own := target{"shop--staging--web", "shop", "staging", "web"}
	others := map[string]target{
		"another name":    {"shop--staging--other", "shop", "staging", "other"},
		"another project": {"other--staging--web", "other", "staging", "web"},
		"default project": {"web", "default", "production", "web"},
		"not an app ID":   {"a--b", "shop", "staging", "a--b"},
	}
	fill := func(path string, tg target) string {
		r := strings.NewReplacer("{id}", tg.id, "{project}", tg.project, "{environment}", tg.environment,
			"{name}", tg.name)
		return pathParam.ReplaceAllString(r.Replace(path), "x") // any other parameter: a filler
	}
	// bodyFor is a body that names the target the way each call reads it.
	bodyFor := func(rt route, tg target) string {
		switch rt.path {
		case "/v1/apps":
			return `{"id": "` + tg.id + `"}`
		case "/v1/projects/{project}/environments/{environment}/apps":
			return `{"appName": "` + tg.name + `"}`
		}
		return `{}`
	}
	send := func(rt route, tg target) (int, string) {
		status, _, raw := callWith(t, auth, rt.method, e.RawURL+fill(rt.path, tg), "application/json", bodyFor(rt, tg))
		return status, string(raw)
	}
	isScopeError := func(status int, raw string) bool {
		return status == http.StatusForbidden && strings.Contains(raw, `"token_scope"`) &&
			strings.Contains(raw, "this token can only deploy app web of project shop")
	}

	for _, rt := range protectedRoutes(t) {
		key := rt.method + " " + rt.path
		t.Run(key, func(t *testing.T) {
			if !slices.Contains(allowedForDeployTokens, key) {
				if status, raw := send(rt, own); !isScopeError(status, raw) {
					t.Errorf("%s with a deploy token for its own app = %d %s, want 403 token_scope", key, status, raw)
				}
				return
			}
			if status, raw := send(rt, own); status == http.StatusForbidden {
				t.Errorf("%s for the token's own app = %d %s, want it allowed", key, status, raw)
			}
			for name, tg := range others {
				if status, raw := send(rt, tg); !isScopeError(status, raw) {
					t.Errorf("%s for %s = %d %s, want 403 token_scope", key, name, status, raw)
				}
			}
		})
	}
}

func TestScopedTokenErrorBody(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")

	status, _, raw := callWith(t, auth, http.MethodGet, e.RawURL+"/v1/apps", "", "")
	body := wantError(t, "listApps with a deploy token", status, raw, http.StatusForbidden, codeTokenScope, "")
	if body.Error.Message != "this token can only deploy app web of project shop" {
		t.Errorf("message = %q", body.Error.Message)
	}
	// A request without credentials is still the one 401, before any scope.
	status, _, raw = callWith(t, "", http.MethodGet, e.RawURL+"/v1/apps", "", "")
	if status != http.StatusUnauthorized {
		t.Errorf("no credentials = %d %s, want 401", status, raw)
	}
	// A full token is not limited.
	status, _, raw = callWith(t, "Bearer "+e.Token, http.MethodGet, e.RawURL+"/v1/apps", "", "")
	if status != http.StatusOK {
		t.Errorf("full token on listApps = %d %s, want 200", status, raw)
	}
}

func TestDeployTokenWorkflow(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)

	// Created through the API, with the scope in the answer and the list.
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json",
		`{"name": "shop-web-ci", "scope": {"project": "shop", "app": "web"}}`)
	if status != http.StatusCreated {
		t.Fatalf("createToken with a scope = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "CreatedToken")
	created := decodeAs[struct {
		Token string `json:"token"`
		Scope *struct {
			Project string `json:"project"`
			App     string `json:"app"`
		} `json:"scope"`
	}](t, raw)
	if created.Scope == nil || created.Scope.Project != "shop" || created.Scope.App != "web" {
		t.Fatalf("scope in the answer = %+v", created.Scope)
	}
	auth := "Bearer " + created.Token
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/tokens", "", "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"scope":{"app":"web","project":"shop"}`) && !strings.Contains(string(raw), `"scope":{"project":"shop","app":"web"}`) {
		t.Errorf("listTokens = %d %s, want the scope shown", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "TokenList")

	with := func(method, path, body string) (int, []byte) {
		status, _, raw := callWith(t, auth, method, e.RawURL+path, "application/json", body)
		return status, raw
	}
	mustBe := func(what string, status int, raw []byte, want int) {
		t.Helper()
		if status != want {
			t.Errorf("%s = %d %s, want %d", what, status, raw, want)
		}
	}

	// It creates its app without deploying, in an environment of its project,
	// reads it, and deploys it (staging, then production by promotion).
	status, raw = with(http.MethodPost, "/v1/projects/shop/environments/staging/apps?deploy=false", shopBody("web"))
	mustBe("createEnvironmentApp", status, raw, http.StatusCreated)
	status, raw = with(http.MethodGet, "/v1/projects/shop/environments/staging/apps/web", "")
	mustBe("getEnvironmentApp", status, raw, http.StatusOK)
	status, raw = with(http.MethodGet, "/v1/apps/shop--staging--web", "")
	mustBe("getApp", status, raw, http.StatusOK)
	status, raw = with(http.MethodPost, "/v1/apps/shop--staging--web/deployments", webApp("staging"))
	mustBe("deployApp", status, raw, http.StatusAccepted)
	d := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "shop--staging--web", d.ID)
	status, raw = with(http.MethodGet, "/v1/apps/shop--staging--web/deployments", "")
	mustBe("listDeployments", status, raw, http.StatusOK)
	status, raw = with(http.MethodGet, "/v1/apps/shop--staging--web/deployments/"+d.ID, "")
	mustBe("getDeployment", status, raw, http.StatusOK)

	// Production: created by the full token (the promotion needs the target to
	// exist), then promoted with the deploy token.
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects/shop/environments/production/apps?deploy=false", "application/json", shopBody("web")); status != http.StatusCreated {
		t.Fatalf("create production web = %d %s", status, raw)
	}
	status, raw = with(http.MethodPost, "/v1/apps/shop--staging--web/promote/preview", `{"to": "production"}`)
	mustBe("previewPromotion", status, raw, http.StatusOK)
	status, raw = with(http.MethodPost, "/v1/apps/shop--staging--web/promote", `{"to": "production"}`)
	mustBe("promoteApp", status, raw, http.StatusAccepted)
	p := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "shop--production--web", p.ID)

	// Never anything else.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/apps"},
		{http.MethodGet, "/v1/tokens"},
		{http.MethodPost, "/v1/tokens"},
		{http.MethodGet, "/v1/apps/shop--staging--other"},
		{http.MethodGet, "/v1/apps/web"},
		{http.MethodDelete, "/v1/apps/shop--staging--web"},
		{http.MethodPost, "/v1/apps/shop--staging--web/stop"},
		{http.MethodPost, "/v1/apps/shop--staging--web/webhook"},
		{http.MethodGet, "/v1/projects"},
		{http.MethodPut, "/v1/registries/ghcr.io"},
	} {
		status, raw = with(c.method, c.path, `{"name": "x"}`)
		mustBe(c.method+" "+c.path, status, raw, http.StatusForbidden)
	}
}

// TestDeployTokenCreateAppReadsTheBody checks createApp is held to the body's
// id, and that the handler still gets the whole body after the middleware
// looked at it.
func TestDeployTokenCreateAppReadsTheBody(t *testing.T) {
	e := newTestEnv(t, "")
	auth := scopedToken(t, e, "default", "whoami")
	send := func(body string) (int, []byte) {
		status, _, raw := callWith(t, auth, http.MethodPost, e.RawURL+"/v1/apps", "application/json", body)
		return status, raw
	}

	status, raw := send(strings.Replace(whoamiBody, "{", `{"id": "whoami",`, 1))
	if status != http.StatusAccepted {
		t.Fatalf("createApp for the token's app = %d %s, want 202 (the handler must see the body)", status, raw)
	}
	for name, body := range map[string]string{
		"another app":       strings.Replace(whoamiBody, "{", `{"id": "other",`, 1),
		"no id":             whoamiBody,
		"not JSON":          `not json`,
		"a duplicated id":   `{"id": "whoami", "id": "other"}`,
		"id of another env": strings.Replace(whoamiBody, "{", `{"id": "shop--staging--whoami",`, 1),
	} {
		status, raw := send(body)
		if status != http.StatusForbidden || !strings.Contains(string(raw), codeTokenScope) {
			t.Errorf("createApp (%s) = %d %s, want 403 token_scope", name, status, raw)
		}
	}
}

func TestCreateTokenScopeErrors(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)
	tests := []struct {
		name, body, code, field string
		status                  int
	}{
		{"unknown project", `{"name": "a", "scope": {"project": "nope", "app": "web"}}`, codeInvalidField, "scope.project", 400},
		{"project not a name", `{"name": "a", "scope": {"project": "Shop", "app": "web"}}`, codeInvalidField, "scope.project", 400},
		{"app not a name", `{"name": "a", "scope": {"project": "shop", "app": "web--x"}}`, codeInvalidField, "scope.app", 400},
		{"no app", `{"name": "a", "scope": {"project": "shop"}}`, codeInvalidField, "scope.app", 400},
		{"unknown scope field", `{"name": "a", "scope": {"project": "shop", "app": "web", "x": 1}}`, codeUnknownField, "scope.x", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json", tt.body)
			wantError(t, tt.name, status, raw, tt.status, tt.code, tt.field)
		})
	}
	// The app need not exist, and a null scope is a full token.
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json", `{"name": "ok", "scope": {"project": "shop", "app": "later"}}`); status != http.StatusCreated {
		t.Errorf("a scope for an app that doesn't exist yet = %d %s, want 201", status, raw)
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json", `{"name": "full", "scope": null}`)
	if status != http.StatusCreated || strings.Contains(string(raw), `"scope"`) {
		t.Errorf("a null scope = %d %s, want a full token without a scope", status, raw)
	}
}
