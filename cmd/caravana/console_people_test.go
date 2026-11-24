package main

// The console-people step of the lab test (feature 053, plan.md "Lab"): the people pages through the
// forms, as the owner and then as a member who joins with an invite link. It is in its own file only
// because console_lab_test.go is long; runConsole lists it before console-delete. Run it alone with
//
//	CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestConsoleLab/^console-(sign-in|people)$' -v -timeout 20m

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var consoleInviteLink = regexp.MustCompile(`/invite\?token=([^"<\s&]+)`)

// consolePeople: the owner invites a member to lab053c (deploy in staging, view in production) from
// Settings › People; the link is drawn once; the member opens it, sets a password and is signed in;
// their Apps page lists only lab053c, the production page has no Deploy form and a POST to deploy it is
// 403; they have no Operator toggle and no installation Settings; and when the owner removes them their
// next page is the sign-in redirect.
func (c *console) consolePeople(t *testing.T) {
	const project, hidden = "lab053c", "lab053chidden"
	stagingID, prodID := project+"--staging--web", project+"--production--web"
	g := c.g

	var memberID string
	cleanup := func() {
		for _, path := range []string{"/v1/apps/" + stagingID, "/v1/apps/" + prodID, "/v1/apps/" + hidden, "/v1/projects/" + project} {
			if resp, err := g.apiRequest(http.MethodDelete, path, "", ""); err == nil {
				resp.Body.Close()
			}
		}
		if memberID != "" {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/people/"+memberID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// The places and their apps, made with the owner's API token.
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"`+project+`","displayName":"Lab 053 C"}`)
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"staging"}`)
	for _, id := range []string{stagingID, prodID, hidden} {
		raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(p053Web("traefik/whoami:v1.10", id)))
		var d struct{ ID string }
		if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
			t.Fatalf("deploy %s: %s", id, raw)
		}
		if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
		}
	}

	// The owner's pages.
	for _, path := range []string{"/settings/people", "/settings/people/new"} {
		if resp, body := c.get(t, path); resp.StatusCode != http.StatusOK || !strings.Contains(body, "People") {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	email := "member-" + g.run + "@lab053.test"
	resp, body := c.post(t, "/settings/people", url.Values{"email": {email}, "orgRole": {"member"},
		"role." + project: {"view"}, "env." + project + "/staging": {"deploy"}})
	m := consoleInviteLink.FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || m == nil {
		t.Fatalf("POST /settings/people = %d (Cache-Control %q), want 200, never cached, with the link:\n%s", resp.StatusCode, resp.Header.Get("Cache-Control"), body)
	}
	token := m[1]
	if _, list := c.get(t, "/settings/people"); strings.Contains(list, token) || !strings.Contains(list, email) {
		t.Errorf("the people list holds the link's token, or lacks the new person")
	}
	var people struct {
		People []struct{ ID, Email string }
	}
	g.getJSON(t, "/v1/people", &people)
	for _, p := range people.People {
		if p.Email == email {
			memberID = p.ID
		}
	}
	if memberID == "" {
		t.Fatalf("listPeople doesn't have %s", email)
	}

	// The member's browser: the link, a password, a session.
	b := g.browser(t)
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := b.Get(g.target.API + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp, string(raw)
	}
	post := func(path string, form url.Values) (*http.Response, string) {
		t.Helper()
		resp, err := b.PostForm(g.target.API+path, form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp, string(raw)
	}
	if resp, body := get("/invite?token=" + url.QueryEscape(token)); resp.StatusCode != http.StatusOK || !strings.Contains(body, email) {
		t.Fatalf("GET /invite = %d, want 200 naming %s", resp.StatusCode, email)
	}
	password := "lab053 console " + g.run
	if resp, body := post("/invite", url.Values{"token": {token}, "password": {password}, "confirm": {"not the same"}}); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(body, "don't match") && !strings.Contains(body, "don&#39;t match") {
		t.Errorf("POST /invite with two passwords = %d, want 422 saying they don't match", resp.StatusCode)
	}
	if resp, _ := post("/invite", url.Values{"token": {token}, "password": {password}, "confirm": {password}}); resp.StatusCode != http.StatusSeeOther ||
		resp.Header.Get("Location") != "/apps" {
		t.Fatalf("POST /invite = %d to %q, want 303 to /apps", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := get("/invite?token=" + url.QueryEscape(token)); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "isn't valid any more") && !strings.Contains(body, "isn&#39;t valid any more") {
		t.Errorf("the used link = %d, want 404 saying it isn't valid any more", resp.StatusCode)
	}

	// What the member sees.
	resp, body = get("/apps")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, stagingID) || !strings.Contains(body, prodID) || strings.Contains(body, hidden) {
		t.Errorf("the member's Apps page = %d: it must list %s and %s and not %s", resp.StatusCode, stagingID, prodID, hidden)
	}
	for _, absent := range []string{`class="cv-view"`, ">Operator<", `href="/apps/new"`} {
		if strings.Contains(body, absent) {
			t.Errorf("the member's Apps page shows %q", absent)
		}
	}
	if resp, body := get("/apps/" + stagingID + "/deployments"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `name="image.web"`) {
		t.Errorf("the member's staging Deployments = %d, want 200 with the deploy form", resp.StatusCode)
	}
	resp, body = get("/apps/" + prodID + "/deployments")
	if resp.StatusCode != http.StatusOK || strings.Contains(body, `name="image.web"`) || !strings.Contains(body, "Your role here is to look") {
		t.Errorf("the member's production Deployments = %d: want 200 with no Deploy form and a note saying their role is to look", resp.StatusCode)
	}
	if resp, body := post("/apps/"+prodID+"/deploy", url.Values{"image.web": {"traefik/whoami:v1.11"}}); resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(body, "You can&#39;t do this here") && !strings.Contains(body, "You can't do this here") {
		t.Errorf("POST /apps/%s/deploy by the member = %d, want 403", prodID, resp.StatusCode)
	}
	for _, path := range []string{"/ops", "/ops/updates", "/settings/registries", "/settings/people", "/settings/backup-storage"} {
		if resp, _ := get(path); resp.StatusCode != http.StatusForbidden {
			t.Errorf("the member's %s = %d, want 403", path, resp.StatusCode)
		}
	}
	if resp, body := get("/settings"); resp.StatusCode != http.StatusOK || strings.Contains(body, "Registries") || strings.Contains(body, "People") || !strings.Contains(body, "API tokens") {
		t.Errorf("the member's Settings = %d: want their own entries only", resp.StatusCode)
	}

	// The owner removes them; their next page is the sign-in redirect.
	if resp, body := c.post(t, "/settings/people/"+memberID+"/remove", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("removing the member = %d, want 303:\n%s", resp.StatusCode, body)
	}
	memberID = "" // gone: the cleanup has nothing to remove
	resp, _ = get("/apps")
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/login") {
		t.Errorf("the removed member's /apps = %d to %q, want 303 to /login", resp.StatusCode, loc)
	}
}
