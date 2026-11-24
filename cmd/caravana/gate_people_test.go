package main

// The people-roles gate step (feature 053, plan.md "Lab"). It is in its own file only because
// gate_test.go is long; runGate lists it after previews.
//
// It is the API and `caravana mcp` as three people use them: an admin, a member who may deploy in
// one environment and look at another, and guests whose end date passes. The people get their API
// tokens the way a person does without a shell: they accept their invite (acceptInvite, public),
// sign in through the console's POST /login, and ask for a token with that session (POST /v1/tokens,
// which acts on the caller's own). Run it alone with
//
//	CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestLab/^(api-needs-token|people-roles)$' -v -timeout 20m

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// labPerson is a person of the step: who they are and how they call the API.
type labPerson struct {
	Email, Password string
	ID              string
	Token           string       // a full API token of the person
	Browser         *http.Client // their console session (a cookie jar)
}

// p053Web is the small whoami service of the people steps: no domain (nothing to collide with),
// FOO in its env so a change of more than the image is easy to make.
func p053Web(image, foo string) map[string]any {
	return map[string]any{
		"name":      "web",
		"image":     image,
		"env":       []map[string]string{{"name": "FOO", "value": foo}},
		"ports":     []map[string]any{{"name": "http", "containerPort": 80}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": labResources,
	}
}

// sendPublic calls a route that needs no credentials.
func (g *gate) sendPublic(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, g.target.API+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// browser is a client with a cookie jar that shows redirects instead of following them: a person's
// console session.
func (g *gate) browser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: g.client().Transport, Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// signIn signs the person in through the console's form. It returns the status of the answer.
func (g *gate) signInAs(t *testing.T, b *http.Client, email, password string) int {
	t.Helper()
	resp, err := b.PostForm(g.target.API+"/login", url.Values{"email": {email}, "password": {password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// sessionCall calls the API with a person's console session.
func (g *gate) sessionCall(t *testing.T, b *http.Client, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, g.target.API+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// errorOf is the code and field of an API error body.
func errorOf(raw []byte) (code, field string) {
	var e struct {
		Error struct{ Code, Field string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Error.Code, e.Error.Field
}

// wantRefusal fails the step unless a call with the token is refused with status and code (and field,
// when given).
func (g *gate) wantRefusal(t *testing.T, token string, status int, code, field, method, path, body string) {
	t.Helper()
	got, raw := g.sendAs(t, token, method, path, body)
	gotCode, gotField := errorOf(raw)
	if got != status || gotCode != code || (field != "" && gotField != field) {
		t.Errorf("%s %s = %d %s (field %q), want %d %s (field %q): %s", method, path, got, gotCode, gotField, status, code, field, raw)
	}
}

// invitePerson invites a person (as the owner), accepts the invite and gives them a console session and
// a full API token.
func (g *gate) joinPerson(t *testing.T, email string, body map[string]any) *labPerson {
	t.Helper()
	p := &labPerson{Email: email, Password: "lab053 password " + g.run}
	body["email"] = email
	raw, _ := json.Marshal(body)
	var invited struct {
		Person      struct{ ID string }
		InviteToken string
	}
	if err := json.Unmarshal(g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/people", string(raw)), &invited); err != nil || invited.InviteToken == "" {
		t.Fatalf("invitePerson %s: %v", email, err)
	}
	p.ID = invited.Person.ID
	accepted, _ := json.Marshal(map[string]string{"token": invited.InviteToken, "password": p.Password})
	if status, raw := g.sendPublic(t, http.MethodPost, "/v1/invites/accept", string(accepted)); status != http.StatusOK {
		t.Fatalf("acceptInvite %s = %d: %s", email, status, raw)
	}
	if status, raw := g.sendPublic(t, http.MethodPost, "/v1/invites/accept", string(accepted)); status != http.StatusNotFound {
		t.Errorf("acceptInvite of a used link = %d, want 404 invite_invalid: %s", status, raw)
	}
	p.Browser = g.browser(t)
	if status := g.signInAs(t, p.Browser, email, p.Password); status != http.StatusSeeOther {
		t.Fatalf("sign-in of %s = %d, want 303", email, status)
	}
	status, raw2 := g.sessionCall(t, p.Browser, http.MethodPost, "/v1/tokens", `{"name":"lab053-`+strings.SplitN(email, "-", 2)[0]+`-`+g.run+`"}`)
	var made struct{ Token string }
	if err := json.Unmarshal(raw2, &made); err != nil || status != http.StatusCreated || made.Token == "" {
		t.Fatalf("createToken for %s with their session = %d %s", email, status, raw2)
	}
	p.Token = made.Token
	return p
}

// appIDsOf lists the IDs of the apps listApps gives a token.
func (g *gate) appIDsOf(t *testing.T, token string) []string {
	t.Helper()
	status, raw := g.sendAs(t, token, http.MethodGet, "/v1/apps", "")
	var list struct {
		Apps []struct {
			App struct{ ID string } `json:"app"`
		} `json:"apps"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &list) != nil {
		t.Fatalf("listApps = %d: %s", status, raw)
	}
	var ids []string
	for _, a := range list.Apps {
		ids = append(ids, a.App.ID)
	}
	slices.Sort(ids)
	return ids
}

// peopleRolesStep (feature 053). In order:
//
//  1. two projects, lab053 (staging and production) and lab053b, and a small app in each place: lab053's
//     web in both environments and lab053b's hidden;
//  2. people: an admin and a member (deploy in lab053 › staging, view in lab053 › production) accept
//     their invites and get tokens through their own session; a used link is 404;
//  3. the member: getMe says member; listApps shows the two lab053 apps and not lab053b's; reads both
//     environments; deploys a new image to staging (healthy) and is 403 not_allowed on production, on
//     an env change in staging (field services[0].env), on registries, servers, creating a project,
//     changing lab053b and reading its app;
//  4. the member's deploy token for lab053/web deploys staging and is refused production with
//     not_allowed (the person's role, not the token's scope);
//  5. the admin lists people, reads registries and deploys production; the deployments say who made them;
//  6. MCP: `caravana mcp` with the member's token: get_me, list_apps with the two apps, deploy_image
//     refused on production and working on staging;
//  7. guests: a person whose end date passed can't sign in and their token is 401; a member whose role
//     ended is refused as one with none, and listApps is empty;
//  8. removal: the member's token and session are 401 on the very next call, and their console page
//     sends them to sign in.
func (g *gate) peopleRolesStep(t *testing.T) {
	const project, other = "lab053", "lab053b"
	stagingID, prodID, hiddenID := project+"--staging--web", project+"--production--web", other+"--production--hidden"
	suffix := "@lab053.test"

	removePeople := func() {
		resp, err := g.apiRequest(http.MethodGet, "/v1/people", "", "")
		if err != nil {
			return
		}
		var list struct {
			People []struct{ ID, Email string } `json:"people"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&list)
		resp.Body.Close()
		for _, p := range list.People {
			if strings.HasSuffix(p.Email, suffix) {
				if r, err := g.apiRequest(http.MethodDelete, "/v1/people/"+p.ID, "", ""); err == nil {
					r.Body.Close()
				}
			}
		}
	}
	remove := func() {
		for _, path := range []string{"/v1/apps/" + stagingID, "/v1/apps/" + prodID, "/v1/apps/" + hiddenID,
			"/v1/projects/" + project, "/v1/projects/" + other} {
			resp, err := g.apiRequest(http.MethodDelete, path, "", "")
			if err != nil {
				t.Logf("cleanup: DELETE %s: %v", path, err)
				continue
			}
			resp.Body.Close()
		}
		removePeople()
	}
	remove() // leftovers from an aborted run, if any
	t.Cleanup(remove)

	deploy := func(token, id, body string) string {
		t.Helper()
		status, raw := g.sendAs(t, token, http.MethodPost, "/v1/apps/"+id+"/deployments", body)
		var d struct{ ID string }
		if status != http.StatusAccepted || json.Unmarshal(raw, &d) != nil || d.ID == "" {
			t.Fatalf("deploy %s = %d: %s", id, status, raw)
		}
		if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
		}
		return d.ID
	}
	// withImage is the stored app of id with another image (and FOO, when given): the body of a deploy
	// that changes images only, or more.
	withImage := func(token, id, image string, foo string) string {
		t.Helper()
		status, raw := g.sendAs(t, token, http.MethodGet, "/v1/apps/"+id, "")
		var rec struct {
			App map[string]any `json:"app"`
		}
		if status != http.StatusOK || json.Unmarshal(raw, &rec) != nil {
			t.Fatalf("getApp %s = %d: %s", id, status, raw)
		}
		svc := rec.App["services"].([]any)[0].(map[string]any)
		svc["image"] = image
		if foo != "" {
			svc["env"] = []any{map[string]any{"name": "FOO", "value": foo}}
		}
		out, _ := json.Marshal(rec.App)
		return string(out)
	}
	actorOf := func(id, deployment string) string {
		t.Helper()
		var d struct {
			Actor string `json:"actor"`
		}
		g.getJSON(t, "/v1/apps/"+id+"/deployments/"+deployment, &d)
		return d.Actor
	}

	// 1. The projects and the apps.
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"`+project+`","displayName":"Lab 053"}`)
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"staging"}`)
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"`+other+`","displayName":"Lab 053 B"}`)
	owner := g.target.APIToken
	deploy(owner, stagingID, labApp(p053Web("traefik/whoami:v1.10", "staging")))
	deploy(owner, prodID, labApp(p053Web("traefik/whoami:v1.10", "production")))
	deploy(owner, hiddenID, labApp(p053Web("traefik/whoami:v1.10", "hidden")))

	// 2. The people. The guests are made now so their end dates pass while the step does the rest.
	end := time.Now().UTC().Add(60 * time.Second).Format(time.RFC3339)
	admin := g.joinPerson(t, "admin-"+g.run+suffix, map[string]any{"orgRole": "admin"})
	member := g.joinPerson(t, "member-"+g.run+suffix, map[string]any{"orgRole": "member", "projects": []map[string]any{
		{"project": project, "role": "view", "environments": map[string]string{"staging": "deploy"}}}})
	gone := g.joinPerson(t, "gone-"+g.run+suffix, map[string]any{"orgRole": "member", "endsAt": end, "projects": []map[string]any{
		{"project": project, "role": "view"}}})
	roleEnds := g.joinPerson(t, "roleends-"+g.run+suffix, map[string]any{"orgRole": "member", "projects": []map[string]any{
		{"project": project, "role": "view", "endsAt": end}}})
	for _, p := range []*labPerson{gone, roleEnds} { // before the end they read
		if status, raw := g.sendAs(t, p.Token, http.MethodGet, "/v1/apps/"+prodID, ""); status != http.StatusOK {
			t.Fatalf("%s reads production before their end = %d: %s", p.Email, status, raw)
		}
	}

	// 3. The member.
	var me struct {
		Email, OrgRole string
		Token          struct{ Name string }
	}
	status, raw := g.sendAs(t, member.Token, http.MethodGet, "/v1/me", "")
	if status != http.StatusOK || json.Unmarshal(raw, &me) != nil || me.OrgRole != "member" || me.Email != member.Email || me.Token.Name == "" {
		t.Fatalf("getMe of the member = %d: %s", status, raw)
	}
	if ids, want := g.appIDsOf(t, member.Token), []string{prodID, stagingID}; !slices.Equal(ids, want) {
		t.Errorf("the member's listApps = %v, want %v (and not %s)", ids, want, hiddenID)
	}
	for _, id := range []string{stagingID, prodID} {
		if status, raw := g.sendAs(t, member.Token, http.MethodGet, "/v1/apps/"+id, ""); status != http.StatusOK {
			t.Errorf("the member reads %s = %d: %s", id, status, raw)
		}
	}
	memberDeployment := deploy(member.Token, stagingID, withImage(member.Token, stagingID, "traefik/whoami:v1.11", ""))
	if a := actorOf(stagingID, memberDeployment); a != member.Email {
		t.Errorf("the member's deployment says actor %q, want %q", a, member.Email)
	}
	g.wantRefusal(t, member.Token, http.StatusForbidden, "not_allowed", "", http.MethodPost, "/v1/apps/"+prodID+"/deployments",
		withImage(member.Token, prodID, "traefik/whoami:v1.11", ""))
	g.wantRefusal(t, member.Token, http.StatusForbidden, "not_allowed", "services[0].env", http.MethodPost, "/v1/apps/"+stagingID+"/deployments",
		withImage(member.Token, stagingID, "traefik/whoami:v1.10", "changed"))
	for _, path := range []string{"/v1/registries", "/v1/servers", "/v1/host", "/v1/people", "/v1/backups/destinations"} {
		g.wantRefusal(t, member.Token, http.StatusForbidden, "not_allowed", "", http.MethodGet, path, "")
	}
	g.wantRefusal(t, member.Token, http.StatusForbidden, "not_allowed", "", http.MethodPost, "/v1/projects", `{"name":"lab053c"}`)
	g.wantRefusal(t, member.Token, http.StatusForbidden, "not_allowed", "", http.MethodPatch, "/v1/projects/"+other, `{"displayName":"mine"}`)
	g.wantRefusal(t, member.Token, http.StatusForbidden, "not_allowed", "", http.MethodGet, "/v1/apps/"+hiddenID, "")
	g.wantRefusal(t, member.Token, http.StatusNotFound, "project_not_found", "", http.MethodGet, "/v1/projects/"+other, "")

	// 4. The member's deploy token.
	status, raw = g.sessionCall(t, member.Browser, http.MethodPost, "/v1/tokens",
		`{"name":"lab053-deploy-`+g.run+`","scope":{"project":"`+project+`","app":"web"}}`)
	var made struct{ Token string }
	if err := json.Unmarshal(raw, &made); err != nil || status != http.StatusCreated || made.Token == "" {
		t.Fatalf("a deploy token for the member = %d: %s", status, raw)
	}
	tokenDeployment := deploy(made.Token, stagingID, withImage(member.Token, stagingID, "traefik/whoami:v1.10", ""))
	if a := actorOf(stagingID, tokenDeployment); a != member.Email {
		t.Errorf("the deploy token's deployment says actor %q, want its person %q", a, member.Email)
	}
	g.wantRefusal(t, made.Token, http.StatusForbidden, "not_allowed", "", http.MethodPost, "/v1/apps/"+prodID+"/deployments",
		withImage(member.Token, prodID, "traefik/whoami:v1.10", ""))

	// 5. The admin.
	status, raw = g.sendAs(t, admin.Token, http.MethodGet, "/v1/people", "")
	var people struct {
		People []struct{ Email string }
	}
	if status != http.StatusOK || json.Unmarshal(raw, &people) != nil || len(people.People) < 5 {
		t.Errorf("the admin's listPeople = %d: %s", status, raw)
	}
	if status, raw := g.sendAs(t, admin.Token, http.MethodGet, "/v1/registries", ""); status != http.StatusOK {
		t.Errorf("the admin's listRegistries = %d: %s", status, raw)
	}
	adminDeployment := deploy(admin.Token, prodID, withImage(admin.Token, prodID, "traefik/whoami:v1.11", ""))
	if a := actorOf(prodID, adminDeployment); a != admin.Email {
		t.Errorf("the admin's deployment says actor %q, want %q", a, admin.Email)
	}
	// An admin can't make or change an owner or an admin: the service holds them to members.
	g.wantRefusal(t, admin.Token, http.StatusForbidden, "not_allowed", "", http.MethodPost, "/v1/people", `{"email":"x-`+g.run+suffix+`","orgRole":"admin"}`)

	// 6. MCP, with the member's token.
	leaks := &mcpLeaks{}
	m := startMCP(t, leaks, g.target.API, member.Token, true)
	var mcpMe struct{ OrgRole, Email string }
	m.mustCall("get_me", nil, &mcpMe)
	if mcpMe.OrgRole != "member" || mcpMe.Email != member.Email {
		t.Errorf("get_me = %+v, want the member", mcpMe)
	}
	var list struct {
		Apps []struct {
			App struct{ ID string } `json:"app"`
		} `json:"apps"`
	}
	m.mustCall("list_apps", nil, &list)
	var mcpIDs []string
	for _, a := range list.Apps {
		mcpIDs = append(mcpIDs, a.App.ID)
	}
	slices.Sort(mcpIDs)
	if want := []string{prodID, stagingID}; !slices.Equal(mcpIDs, want) {
		t.Errorf("list_apps = %v, want %v", mcpIDs, want)
	}
	if got := m.mustFail("deploy_image", map[string]any{"app": prodID, "image": "traefik/whoami:v1.10"}); !strings.Contains(got, "not_allowed") {
		t.Errorf("deploy_image on production: %q, want not_allowed", got)
	}
	var dep struct{ ID string }
	m.mustCall("deploy_image", map[string]any{"app": stagingID, "image": "traefik/whoami:v1.11"}, &dep)
	if dep.ID == "" {
		t.Fatal("deploy_image on staging gave no deployment")
	}
	if final := g.waitFinalOf(t, stagingID, dep.ID, 4*time.Minute); final.Status != "healthy" {
		t.Errorf("the MCP deploy of staging ended %s: %s", final.Status, final.Error)
	}
	leaks.mu.Lock()
	for i, s := range leaks.stderrs {
		if strings.Contains(s.String(), member.Token) {
			t.Errorf("server %d's standard error holds the member's token", i)
		}
	}
	leaks.mu.Unlock()

	// 7. The guests, once their end date has passed.
	until, _ := time.Parse(time.RFC3339, end)
	time.Sleep(time.Until(until.Add(2 * time.Second)))
	if status, raw := g.sendAs(t, gone.Token, http.MethodGet, "/v1/me", ""); status != http.StatusUnauthorized {
		t.Errorf("the token of a person whose end date passed = %d, want 401: %s", status, raw)
	}
	if status := g.signInAs(t, g.browser(t), gone.Email, gone.Password); status == http.StatusSeeOther {
		t.Errorf("a person whose end date passed signed in (303)")
	}
	g.wantRefusal(t, roleEnds.Token, http.StatusForbidden, "not_allowed", "", http.MethodGet, "/v1/apps/"+prodID, "")
	if ids := g.appIDsOf(t, roleEnds.Token); len(ids) != 0 {
		t.Errorf("listApps of a member whose role ended = %v, want none", ids)
	}
	if status, raw := g.sendAs(t, roleEnds.Token, http.MethodGet, "/v1/me", ""); status != http.StatusOK {
		t.Errorf("getMe of a member whose role ended = %d, want 200: %s", status, raw)
	}

	// 8. Removal: the very next call is 401.
	if status, raw := g.sessionCall(t, member.Browser, http.MethodGet, "/v1/me", ""); status != http.StatusOK {
		t.Fatalf("the member's session before removal = %d: %s", status, raw)
	}
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/people/"+member.ID, "")
	for name, token := range map[string]string{"token": member.Token, "deploy token": made.Token} {
		if status, raw := g.sendAs(t, token, http.MethodGet, "/v1/me", ""); status != http.StatusUnauthorized {
			t.Errorf("the removed member's %s = %d, want 401: %s", name, status, raw)
		}
	}
	if status, raw := g.sessionCall(t, member.Browser, http.MethodGet, "/v1/me", ""); status != http.StatusUnauthorized {
		t.Errorf("the removed member's session = %d, want 401: %s", status, raw)
	}
	resp, err := member.Browser.Get(g.target.API + "/apps")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/login") {
		t.Errorf("the removed member's console page = %d to %q, want 303 to /login", resp.StatusCode, loc)
	}
}
