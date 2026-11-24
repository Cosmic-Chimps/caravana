package api

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 054, Phase C: the people routes end to end through the real handlers, who a caller is
// (getMe), the actor on records, what a deploy-level member may deploy, schedules judged by their app
// and followed streams ending when a role does. The walk (access_test.go) covers every route with
// stubbed handlers; these cover what the handlers answer.

// memberOf makes an active member with the given project roles and an API token for them.
func memberOf(t *testing.T, e *testEnv, email string, roles []store.ProjectRoleRecord) (id, token string) {
	t.Helper()
	ctx := context.Background()
	id = strings.TrimSuffix(email, "@example.com") + strings.Repeat("0", 26)
	id = id[:26]
	p := store.PersonRecord{ID: id, Email: email, OrgRole: "member", State: "active", PasswordHash: "x",
		PasswordSignIn: true, CreatedAt: e.Clock.Now()}
	if err := e.Store.CreatePerson(ctx, p, roles); err != nil {
		t.Fatal(err)
	}
	c, err := e.Auth.CreateAPIToken(ctx, id, strings.TrimSuffix(email, "@example.com")+"-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id, c.Token
}

func TestPeopleRoutesLifecycle(t *testing.T) {
	e := newTestEnv(t, "")
	ctx := context.Background()
	doc := loadContract(t)
	if _, err := e.Service.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}

	// Invite: 201, no-store, the token and the link in this answer only.
	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/people", "application/json",
		`{"email": "Ana@Example.com", "orgRole": "member", "projects": [{"project": "shop", "role": "view", "environments": {"production": "none"}}]}`)
	if status != http.StatusCreated {
		t.Fatalf("invitePerson = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Invited")
	if cc := header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var invited struct {
		Person struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			State string `json:"state"`
		} `json:"person"`
		InviteToken string `json:"inviteToken"`
		InviteURL   string `json:"inviteUrl"`
	}
	if err := json.Unmarshal(raw, &invited); err != nil {
		t.Fatal(err)
	}
	if invited.Person.Email != "ana@example.com" || invited.Person.State != "invited" || invited.InviteToken == "" {
		t.Fatalf("invited = %+v, want ana@example.com invited with a token", invited)
	}
	if !strings.HasPrefix(invited.InviteURL, "http://") || !strings.HasSuffix(invited.InviteURL, "/invite?token="+invited.InviteToken) {
		t.Errorf("inviteUrl = %q, want the request's scheme and host and the token", invited.InviteURL)
	}
	if loc := header.Get("Location"); loc != "/v1/people/"+invited.Person.ID {
		t.Errorf("Location = %q", loc)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/people", "application/json",
		`{"email": "ana@example.com", "orgRole": "member"}`); status != http.StatusConflict || !strings.Contains(string(raw), `"email_taken"`) {
		t.Errorf("a second invite for the same email = %d %s, want 409 email_taken", status, raw)
	}

	// The reads never hold the token.
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/people/"+invited.Person.ID, "", "")
	checkSchema(t, doc, raw, "Person")
	if strings.Contains(string(raw), invited.InviteToken) {
		t.Error("getPerson holds the invite token")
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/people", "", "")
	checkSchema(t, doc, raw, "PersonList")
	if strings.Contains(string(raw), invited.InviteToken) || !strings.Contains(string(raw), "ana@example.com") || !strings.Contains(string(raw), testAdminEmail) {
		t.Errorf("listPeople = %s, want both people and no token", raw)
	}

	// A wrong token is 404 invite_invalid and counts against the address; the right one makes Ana active.
	if status, _, raw := call(t, http.MethodPost, e.RawURL+"/v1/invites/accept", "application/json",
		`{"token": "cvin_wrong", "password": "a long enough password 1"}`); status != http.StatusNotFound || !strings.Contains(string(raw), `"invite_invalid"`) {
		t.Errorf("accept with a wrong token = %d %s, want 404 invite_invalid", status, raw)
	}
	status, header, raw = call(t, http.MethodPost, e.RawURL+"/v1/invites/accept", "application/json",
		`{"token": "`+invited.InviteToken+`", "password": "a long enough password 1"}`)
	if status != http.StatusOK || !strings.Contains(string(raw), "ana@example.com") {
		t.Fatalf("accept = %d %s, want 200 with the email", status, raw)
	}
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("accept Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}
	if status, _, raw := call(t, http.MethodPost, e.RawURL+"/v1/invites/accept", "application/json",
		`{"token": "`+invited.InviteToken+`", "password": "a long enough password 1"}`); status != http.StatusNotFound {
		t.Errorf("a used invite = %d %s, want 404 invite_invalid", status, raw)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/people/"+invited.Person.ID+"/invite", "", ""); status != http.StatusConflict || !strings.Contains(string(raw), `"not_invited"`) {
		t.Errorf("reinvite of an active person = %d %s, want 409 not_invited", status, raw)
	}

	// Update replaces the role; an unknown person is 404 person_not_found.
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/people/"+invited.Person.ID, "application/json",
		`{"orgRole": "member", "projects": [{"project": "shop", "role": "deploy"}]}`)
	if status != http.StatusOK {
		t.Fatalf("updatePerson = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Person")
	if !strings.Contains(string(raw), `"role":"deploy"`) || strings.Contains(string(raw), `"production"`) {
		t.Errorf("updatePerson = %s, want deploy and the old override gone", raw)
	}
	if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/people/nobody", "", ""); status != http.StatusNotFound || !strings.Contains(string(raw), `"person_not_found"`) {
		t.Errorf("getPerson of nobody = %d %s", status, raw)
	}

	// Remove: 204, then gone.
	if status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/people/"+invited.Person.ID, "", ""); status != http.StatusNoContent {
		t.Fatalf("removePerson = %d %s", status, raw)
	}
	if status, _, _ := call(t, http.MethodGet, e.URL+"/v1/people/"+invited.Person.ID, "", ""); status != http.StatusNotFound {
		t.Errorf("getPerson after removal = %d, want 404", status)
	}

	// The last owner can't be removed or made an admin.
	if status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/people/"+e.OwnerID, "", ""); status != http.StatusConflict || !strings.Contains(string(raw), `"last_owner"`) {
		t.Errorf("removing the only owner = %d %s, want 409 last_owner", status, raw)
	}
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/people/"+e.OwnerID, "application/json", `{"orgRole": "admin"}`); status != http.StatusConflict || !strings.Contains(string(raw), `"last_owner"`) {
		t.Errorf("demoting the only owner = %d %s, want 409 last_owner", status, raw)
	}
}

func TestAcceptInviteCountsFailuresAndLocks(t *testing.T) {
	e := newTestEnv(t, "")
	var last int
	for i := 0; i < 6; i++ {
		last, _, _ = call(t, http.MethodPost, e.RawURL+"/v1/invites/accept", "application/json",
			`{"token": "cvin_guess", "password": "a long enough password 1"}`)
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("the sixth wrong token = %d, want 429 too_many_attempts", last)
	}
}

func TestGetMe(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	ctx := context.Background()
	if _, err := e.Service.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	_, memberTok := memberOf(t, e, "ben@example.com", []store.ProjectRoleRecord{{Project: "shop", Role: "deploy"}})

	status, header, raw := call(t, http.MethodGet, e.URL+"/v1/me", "", "")
	if status != http.StatusOK {
		t.Fatalf("getMe = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Me")
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}
	if !strings.Contains(string(raw), `"orgRole":"owner"`) || !strings.Contains(string(raw), `"name":"tests"`) {
		t.Errorf("getMe of the owner's token = %s, want owner and the token's name", raw)
	}

	status, _, raw = callWith(t, "Bearer "+memberTok, http.MethodGet, e.RawURL+"/v1/me", "", "")
	if status != http.StatusOK {
		t.Fatalf("getMe of a member = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Me")
	if !strings.Contains(string(raw), `"orgRole":"member"`) || !strings.Contains(string(raw), `"project":"shop"`) || strings.Contains(string(raw), `"scope"`) {
		t.Errorf("getMe of a member = %s, want member, the shop role and no scope", raw)
	}

	// A deploy token is outside 020's allowlist, so getMe is 403 token_scope for it (default deny).
	c, err := e.Auth.CreateScopedAPIToken(ctx, e.OwnerID, "ci", nil, &service.TokenScope{Project: "shop", App: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, raw := callWith(t, "Bearer "+c.Token, http.MethodGet, e.RawURL+"/v1/me", "", ""); status != http.StatusForbidden || !strings.Contains(string(raw), `"token_scope"`) {
		t.Errorf("getMe of a deploy token = %d %s, want 403 token_scope", status, raw)
	}

	// No credentials: 401, as for every protected route.
	if status, _, _ := call(t, http.MethodGet, e.RawURL+"/v1/me", "", ""); status != http.StatusUnauthorized {
		t.Errorf("getMe with no credentials = %d, want 401", status)
	}
}

// A person who can deploy but not maintain may change images only: anything else in the body is
// 403 not_allowed naming the field.
func TestMemberDeploysImagesOnly(t *testing.T) {
	e := newTestEnv(t, "")
	ctx := context.Background()
	if _, err := e.Service.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Service.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	e.Registry.AddIndex("traefik/whoami", "v2", whoamiDigest)
	_, tok := memberOf(t, e, "dee@example.com", []store.ProjectRoleRecord{{Project: "shop", Role: "deploy"}})
	const id = "shop--staging--web"
	owned := strings.Replace(whoamiBody, "whoami.localhost", "web.staging.shop.localhost", 1)
	d := deployOK(t, e, id, owned)
	waitHealthy(t, e, id, d.ID)

	deploy := func(body string) (int, []byte) {
		status, _, raw := callWith(t, "Bearer "+tok, http.MethodPost, e.RawURL+"/v1/apps/"+id+"/deployments", "application/json", body)
		return status, raw
	}
	if status, raw := deploy(strings.Replace(owned, "traefik/whoami:latest", "traefik/whoami:v2", 1)); status != http.StatusAccepted {
		t.Errorf("a new image by a deploy-level member = %d %s, want 202", status, raw)
	} else {
		checkSchema(t, loadContract(t), raw, "Deployment")
		if !strings.Contains(string(raw), `"actor":"dee@example.com"`) {
			t.Errorf("deployment answer %s, want the actor dee@example.com", raw)
		}
	}
	status, raw := deploy(strings.Replace(owned, `"name": "web",`, `"name": "web", "env": [{"name": "A", "value": "1"}],`, 1))
	if status != http.StatusForbidden || !strings.Contains(string(raw), `"not_allowed"`) || !strings.Contains(string(raw), `"field":"services[0].env"`) {
		t.Errorf("an env change by a deploy-level member = %d %s, want 403 not_allowed at services[0].env", status, raw)
	}
	// An empty body redeploys what is stored: that is deploy level.
	if status, raw := deploy(""); status != http.StatusAccepted && status != http.StatusConflict {
		t.Errorf("a redeploy by a deploy-level member = %d %s, want it let through", status, raw)
	}
}

// A schedule's ID doesn't say which app it belongs to, so the service judges it by its app: an app the
// person can't see is "not found", one they can see but not change is not_allowed.
func TestScheduleRoutesAreJudgedByTheirApp(t *testing.T) {
	e := scheduleEnv(t)
	ctx := context.Background()
	if _, err := e.Service.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	sc := createScheduleOK(t, e, `{"app": "whoami", "name": "nightly", "command": ["true"], "cron": "0 4 * * *"}`)
	_, viewer := memberOf(t, e, "vic@example.com", []store.ProjectRoleRecord{{Project: "default", Role: "view"}})
	_, outsider := memberOf(t, e, "out@example.com", []store.ProjectRoleRecord{{Project: "shop", Role: "maintain"}})
	_, deployer := memberOf(t, e, "dev@example.com", []store.ProjectRoleRecord{{Project: "default", Role: "deploy"}})
	as := func(tok, method, path, body string) (int, string) {
		status, _, raw := callWith(t, "Bearer "+tok, method, e.RawURL+path, "application/json", body)
		return status, string(raw)
	}

	// The viewer reads, and may not change or run.
	if status, raw := as(viewer, http.MethodGet, "/v1/schedules/"+sc.ID, ""); status != http.StatusOK {
		t.Errorf("a viewer reading the schedule = %d %s, want 200", status, raw)
	}
	if status, raw := as(viewer, http.MethodDelete, "/v1/schedules/"+sc.ID, ""); status != http.StatusForbidden || !strings.Contains(raw, `"not_allowed"`) {
		t.Errorf("a viewer deleting it = %d %s, want 403 not_allowed", status, raw)
	}
	if status, raw := as(viewer, http.MethodPost, "/v1/schedules/"+sc.ID+"/run", ""); status != http.StatusForbidden {
		t.Errorf("a viewer running it = %d %s, want 403", status, raw)
	}
	// A deployer runs it, and may not delete it (that is maintain).
	if status, raw := as(deployer, http.MethodPost, "/v1/schedules/"+sc.ID+"/run", ""); status != http.StatusAccepted {
		t.Errorf("a deployer running it = %d %s, want 202", status, raw)
	}
	if status, raw := as(deployer, http.MethodDelete, "/v1/schedules/"+sc.ID, ""); status != http.StatusForbidden {
		t.Errorf("a deployer deleting it = %d %s, want 403", status, raw)
	}
	// Someone with no role in the app's project: not found, never "not allowed", and not in the list.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/schedules/" + sc.ID},
		{http.MethodDelete, "/v1/schedules/" + sc.ID},
		{http.MethodPost, "/v1/schedules/" + sc.ID + "/run"},
		{http.MethodGet, "/v1/schedules/" + sc.ID + "/runs"},
	} {
		if status, raw := as(outsider, c.method, c.path, ""); status != http.StatusNotFound || !strings.Contains(raw, `"schedule_not_found"`) {
			t.Errorf("%s %s by an outsider = %d %s, want 404 schedule_not_found", c.method, c.path, status, raw)
		}
	}
	if status, raw := as(outsider, http.MethodGet, "/v1/schedules", ""); status != http.StatusOK || strings.Contains(raw, "nightly") {
		t.Errorf("the outsider's schedule list = %d %s, want none of the app's schedules", status, raw)
	}
	if status, raw := as(viewer, http.MethodGet, "/v1/schedules", ""); status != http.StatusOK || !strings.Contains(raw, "nightly") {
		t.Errorf("the viewer's schedule list = %d %s, want the schedule", status, raw)
	}
	// A new schedule needs maintain on its app.
	if status, raw := as(deployer, http.MethodPost, "/v1/schedules", `{"app": "whoami", "name": "more", "command": ["true"], "cron": "0 5 * * *"}`); status != http.StatusForbidden {
		t.Errorf("a deployer creating a schedule = %d %s, want 403", status, raw)
	}
}

// A followed log stream ends when the person's role stops allowing it, not only when the credential does.
func TestFollowedLogsEndWhenTheRoleDoes(t *testing.T) {
	e := logsEnv(t)
	url := logServer(t, e, 50*time.Millisecond, time.Hour)
	id, tok := memberOf(t, e, "lee@example.com", []store.ProjectRoleRecord{{Project: "default", Role: "view"}})
	c := openSSE(t, url+"/v1/apps/whoami/logs?follow=true", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) })
	c.nextLog() // the stream is up
	if _, err := e.Service.UpdatePerson(context.Background(), id, service.PersonInput{OrgRole: service.OrgMember}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ev, ok := c.next()
		if ok && ev.name == "ended" {
			return
		}
		if !ok {
			t.Fatal("the stream closed without an ended event")
		}
		if time.Now().After(deadline) {
			t.Fatal("the stream was still open 3s after the person lost the role")
		}
	}
}

// A project-deploy member opens and closes previews (pr-<n>) and nothing else in the project.
func TestPreviewRoutesAtDeployLevel(t *testing.T) {
	e := newTestEnv(t, "")
	ctx := context.Background()
	if _, err := e.Service.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	_, tok := memberOf(t, e, "pat@example.com", []store.ProjectRoleRecord{{Project: "shop", Role: "deploy"}})
	as := func(method, path, body string) (int, string) {
		status, _, raw := callWith(t, "Bearer "+tok, method, e.RawURL+path, "application/json", body)
		return status, string(raw)
	}
	if status, raw := as(http.MethodPost, "/v1/projects/shop/environments", `{"name": "pr-7"}`); status != http.StatusCreated {
		t.Errorf("a deploy-level member opening pr-7 = %d %s, want 201", status, raw)
	}
	if status, raw := as(http.MethodPost, "/v1/projects/shop/environments", `{"name": "qa"}`); status != http.StatusForbidden || !strings.Contains(raw, `"not_allowed"`) {
		t.Errorf("a deploy-level member opening qa = %d %s, want 403 not_allowed", status, raw)
	}
	if status, raw := as(http.MethodDelete, "/v1/projects/shop/environments/pr-7", ""); status != http.StatusNoContent {
		t.Errorf("a deploy-level member closing pr-7 = %d %s, want 204", status, raw)
	}
}
