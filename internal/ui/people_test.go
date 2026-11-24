package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 054, T012: the People pages, the invite link, what each role sees and the who-words.

// personCookie makes an active person and signs them in: the cookie of their session.
func (e *uiEnv) personCookie(t *testing.T, email, org string, ends *time.Time, roles []store.ProjectRoleRecord) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	id := strings.NewReplacer(".", "", "-", "").Replace(strings.Split(email, "@")[0]) + strings.Repeat("0", 26)
	id = id[:26]
	err := e.Store.CreatePerson(ctx, store.PersonRecord{ID: id, Email: email, OrgRole: org, State: "active", PasswordHash: "x",
		PasswordSignIn: true, EndsAt: ends, CreatedAt: e.Clock.Now()}, roles)
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	sess, err := e.Auth.StartSessionFor(ctx, id)
	if err != nil {
		t.Fatalf("session of %s: %v", email, err)
	}
	return &http.Cookie{Name: "caravana_session", Value: sess.Value}
}

var inviteLink = regexp.MustCompile(`/invite\?token=([^"<\s&]+)`)

func TestPeoplePagesLifecycle(t *testing.T) {
	e := shopConsole(t)
	ctx := context.Background()

	resp, body := e.get(t, "/settings/people")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, testEmail) || !strings.Contains(text(body), "(you)") {
		t.Fatalf("GET /settings/people = %d; want the owner, marked as you:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.get(t, "/settings/people/new")
	for _, want := range []string{"Role in the installation", `name="role.shop"`, `name="env.shop/staging"`, `name="endsAt"`} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("the invite form lacks %s (status %d)", want, resp.StatusCode)
		}
	}
	if strings.Contains(body, `name="env.shop/pr-`) {
		t.Error("a preview environment has an override: it takes the project's role")
	}

	// Invite: 200, never cached, the link once.
	resp, body = e.post(t, e.Cookie, "/settings/people", url.Values{"email": {"Ana@Example.com"}, "orgRole": {"member"},
		"role.shop": {"view"}, "env.shop/staging": {"deploy"}})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("invite = %d, Cache-Control %q; want 200 no-store:\n%s", resp.StatusCode, resp.Header.Get("Cache-Control"), text(body))
	}
	m := inviteLink.FindStringSubmatch(body)
	if m == nil || !strings.Contains(text(body), "Send this link to ana@example.com") {
		t.Fatalf("the answer has no invite link for ana@example.com:\n%s", text(body))
	}
	token := m[1]
	_, body = e.get(t, "/settings/people")
	if strings.Contains(body, token) || !strings.Contains(body, "ana@example.com") || !strings.Contains(text(body), "Invited") {
		t.Errorf("the list holds the token, or lacks ana as invited:\n%s", text(body))
	}
	if !strings.Contains(text(body), "shop: view (staging deploy)") {
		t.Errorf("the list doesn't say ana's roles:\n%s", text(body))
	}
	// A second invite for the same email is refused at the field.
	resp, body = e.post(t, e.Cookie, "/settings/people", url.Values{"email": {"ana@example.com"}, "orgRole": {"member"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "already here") {
		t.Errorf("a second invite = %d, want 422 at the email:\n%s", resp.StatusCode, text(body))
	}
	// A date that isn't one is refused at its field and nothing is made.
	resp, body = e.post(t, e.Cookie, "/settings/people", url.Values{"email": {"bob@example.com"}, "orgRole": {"member"}, "endsAt": {"soon"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "end date must be a date") {
		t.Errorf("a bad date = %d:\n%s", resp.StatusCode, text(body))
	}

	// The invite page: no session, the email, a password and its confirmation.
	resp, body = e.do(t, request{path: "/invite?token=" + url.QueryEscape(token)})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "ana@example.com") || !strings.Contains(body, `name="confirm"`) ||
		!strings.Contains(body, `<input type="hidden" name="token" value="`+token+`">`) {
		t.Fatalf("GET /invite = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the invite page's Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	post := func(form url.Values) (*http.Response, string) {
		return e.do(t, request{method: http.MethodPost, path: "/invite", form: form})
	}
	resp, body = post(url.Values{"token": {token}, "password": {"a long enough password 1"}, "confirm": {"another password 22"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "passwords don't match") {
		t.Errorf("mismatched passwords = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = post(url.Values{"token": {token}, "password": {"short"}, "confirm": {"short"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "The password") {
		t.Errorf("a short password = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, _ = post(url.Values{"token": {token}, "password": {"a long enough password 1"}, "confirm": {"a long enough password 1"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps" {
		t.Fatalf("POST /invite = %d to %q, want 303 to /apps", resp.StatusCode, resp.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, "caravana_session") {
			session = c
		}
	}
	if session == nil {
		t.Fatal("accepting the invite started no session")
	}
	if resp, _ = e.do(t, request{path: "/apps", cookies: []*http.Cookie{session}}); resp.StatusCode != http.StatusOK {
		t.Errorf("the new person's /apps = %d, want 200", resp.StatusCode)
	}
	if resp, body = e.do(t, request{path: "/settings/people", cookies: []*http.Cookie{session}}); resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(text(body), "this needs an owner or an admin") {
		t.Errorf("a member's /settings/people = %d:\n%s", resp.StatusCode, text(body))
	}
	// The link works once.
	resp, body = e.do(t, request{path: "/invite?token=" + url.QueryEscape(token)})
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "This invite link isn't valid any more. Ask for a new one.") {
		t.Errorf("a used link = %d:\n%s", resp.StatusCode, text(body))
	}

	// A person's page: change the roles, see them again, then remove them.
	people, err := e.Svc.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ana, owner service.Person
	for _, p := range people {
		switch p.Email {
		case "ana@example.com":
			ana = p
		case testEmail:
			owner = p
		}
	}
	resp, body = e.get(t, "/settings/people/"+ana.ID)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<option value="view" selected>`) {
		t.Fatalf("ana's page = %d, want view selected for shop:\n%s", resp.StatusCode, text(body))
	}
	resp, _ = e.post(t, e.Cookie, "/settings/people/"+ana.ID, url.Values{"orgRole": {"member"}, "role.shop": {"deploy"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/people/"+ana.ID+"?saved=1" {
		t.Fatalf("saving ana = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := e.Svc.Person(ctx, ana.ID); len(got.Projects) != 1 || got.Projects[0].Role != service.RoleDeploy || len(got.Projects[0].Environments) != 0 {
		t.Errorf("ana after the save = %+v, want deploy in shop with no override", got.Projects)
	}
	// A new link for a person who accepted is refused.
	if resp, _ = e.post(t, e.Cookie, "/settings/people/"+ana.ID+"/invite", url.Values{}); resp.StatusCode != http.StatusConflict {
		t.Errorf("a new link for an active person = %d, want 409", resp.StatusCode)
	}
	// The only owner can't be removed.
	if resp, body = e.get(t, "/settings/people/"+owner.ID+"/remove"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Remove "+testEmail) {
		t.Errorf("the confirmation = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, body = e.post(t, e.Cookie, "/settings/people/"+owner.ID+"/remove", url.Values{}); resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "last owner") {
		t.Errorf("removing the only owner = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, _ = e.post(t, e.Cookie, "/settings/people/"+ana.ID+"/remove", url.Values{}); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/people?did=removed" {
		t.Errorf("removing ana = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ = e.do(t, request{path: "/apps", cookies: []*http.Cookie{session}}); resp.StatusCode == http.StatusOK {
		t.Error("a removed person still has a session")
	}
	// A new link for someone still invited: shown once, never cached.
	cy, err := e.Svc.InvitePerson(ctx, service.PersonInput{Email: "cy@example.com", OrgRole: service.OrgMember})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = e.post(t, e.Cookie, "/settings/people/"+cy.Person.ID+"/invite", url.Values{})
	m2 := inviteLink.FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || m2 == nil || m2[1] == cy.Token {
		t.Errorf("a new link = %d, Cache-Control %q, link %v: want 200 no-store and a new token", resp.StatusCode, resp.Header.Get("Cache-Control"), m2)
	}
}

func TestInviteLinkLocksTheAddress(t *testing.T) {
	e := newConsoleEnv(t)
	var resp *http.Response
	var body string
	for range 6 {
		resp, body = e.do(t, request{path: "/invite?token=cvin_guess"})
	}
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(text(body), "Too many failed sign-ins") {
		t.Errorf("the sixth wrong link = %d:\n%s", resp.StatusCode, text(body))
	}
}

// A member sees their own Settings and none of the installation's, and not the Operator view.
func TestMemberSeesOnlyTheirOwn(t *testing.T) {
	e := shopConsole(t)
	member := e.personCookie(t, "mia@example.com", "member", nil, []store.ProjectRoleRecord{{Project: "shop", Role: "view"}})
	get := func(c *http.Cookie, path string) (*http.Response, string) {
		return e.do(t, request{path: path, cookies: []*http.Cookie{c}})
	}

	resp, body := get(member, "/settings")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a member's /settings = %d", resp.StatusCode)
	}
	for _, want := range []string{"Sign-in", "API tokens", "Appearance"} {
		if !strings.Contains(body, want) {
			t.Errorf("a member's Settings lacks %q", want)
		}
	}
	for _, absent := range []string{"Registries", "People", "Backup storage", "Notifications", "Certificates", "DNS providers", "Secret providers", "Builds", `class="cv-view"`, ">Operator<"} {
		if strings.Contains(body, absent) {
			t.Errorf("a member's Settings shows %q", absent)
		}
	}
	for _, path := range []string{"/ops", "/ops/updates", "/settings/registries", "/settings/people", "/settings/backup-storage", "/projects/new", "/parts/platform"} {
		resp, body := get(member, path)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("a member's %s = %d, want 403", path, resp.StatusCode)
		}
		if !strings.HasPrefix(path, "/parts/") && !strings.Contains(text(body), "You can't do this here") {
			t.Errorf("a member's %s: the page doesn't say what is wrong:\n%s", path, text(body))
		}
	}
	// The owner sees the toggle, People and the installation's entries.
	_, body = get(e.Cookie, "/settings")
	for _, want := range []string{"People", "Registries", `class="cv-view"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the owner's Settings lacks %q", want)
		}
	}
	// New app is for a person who may make one.
	_, body = get(member, "/apps")
	if strings.Contains(body, `href="/apps/new"`) {
		t.Error("a viewer is offered New app")
	}
	if resp, _ = get(member, "/apps/new"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer's /apps/new = %d, want 403", resp.StatusCode)
	}
	maintainer := e.personCookie(t, "max@example.com", "member", nil, []store.ProjectRoleRecord{{Project: "shop", Role: "maintain"}})
	if _, body = get(maintainer, "/apps"); !strings.Contains(body, `href="/apps/new"`) {
		t.Error("a person who maintains a project isn't offered New app")
	}
	if resp, _ = get(maintainer, "/apps/new"); resp.StatusCode != http.StatusOK {
		t.Errorf("a maintainer's /apps/new = %d, want 200", resp.StatusCode)
	}
}

// What an app's page draws follows the role: view only reads, deploy deploys and restarts, maintain
// stops, runs a command and changes the setup.
func TestAppControlsFollowTheRole(t *testing.T) {
	e := shopConsole(t)
	stagingApp, productionApp := shopWeb("staging", "latest"), shopWeb("production", "latest")
	for _, a := range []app.App{stagingApp, productionApp} {
		a := a
		e.seed(t, a,
			seedDeploy{ID: "d1-" + a.ID, Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1, Definition: &a},
			seedDeploy{ID: "d2-" + a.ID, Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 2, Definition: &a})
		e.copies(a.ID, "web", 2, 1, 0)
	}
	member := e.personCookie(t, "dee@example.com", "member", nil, []store.ProjectRoleRecord{
		{Project: "shop", Role: "view", Environments: map[string]string{"staging": "deploy"}}})
	maintainer := e.personCookie(t, "max@example.com", "member", nil, []store.ProjectRoleRecord{{Project: "shop", Role: "maintain"}})
	const staging, production = "shop--staging--web", "shop--production--web"

	page := func(c *http.Cookie, id, tab string) string {
		t.Helper()
		resp, body := e.do(t, request{path: "/apps/" + id + tab, cookies: []*http.Cookie{c}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /apps/%s%s = %d", id, tab, resp.StatusCode)
		}
		return body
	}
	has := func(t *testing.T, who, where, body, what string, want bool) {
		t.Helper()
		if strings.Contains(body, what) != want {
			t.Errorf("%s on %s: %q present = %v, want %v", who, where, what, !want, want)
		}
	}

	t.Run("the header", func(t *testing.T) {
		for _, tc := range []struct {
			who  string
			c    *http.Cookie
			id   string
			want [3]bool // Restart, Stop, Deploy
		}{
			{"the owner", e.Cookie, staging, [3]bool{true, true, true}},
			{"a maintainer", maintainer, staging, [3]bool{true, true, true}},
			{"a deployer", member, staging, [3]bool{true, false, true}},
			{"a viewer", member, production, [3]bool{false, false, false}},
		} {
			header := appHeadRegion.FindString(page(tc.c, tc.id, ""))
			has(t, tc.who, tc.id, header, `id="app-restart"`, tc.want[0])
			has(t, tc.who, tc.id, header, `id="app-stop"`, tc.want[1])
			has(t, tc.who, tc.id, header, `id="app-deploy"`, tc.want[2])
		}
	})
	t.Run("the other environments' promote and copy", func(t *testing.T) {
		has(t, "a deployer", staging, appHeadRegion.FindString(page(member, staging, "")), `id="promote-production"`, false) // view on production
		has(t, "the owner", staging, appHeadRegion.FindString(page(e.Cookie, staging, "")), `id="promote-production"`, true)
		has(t, "a deployer", production, appHeadRegion.FindString(page(member, production, "")), `id="promote-staging"`, true) // deploy on staging
	})
	t.Run("run a command and the terminal", func(t *testing.T) {
		has(t, "the owner", staging, page(e.Cookie, staging, ""), "Run a command", true)
		has(t, "a deployer", staging, page(member, staging, ""), "Run a command", false)
		has(t, "a deployer", staging, page(member, staging, ""), "/terminal", false)
	})
	t.Run("the Deployments tab", func(t *testing.T) {
		has(t, "a deployer", staging, page(member, staging, "/deployments"), `name="image.web"`, true)
		has(t, "a deployer", staging, page(member, staging, "/deployments"), "Redeploy this version", true)
		body := page(member, production, "/deployments")
		has(t, "a viewer", production, body, `name="image.web"`, false)
		has(t, "a viewer", production, body, "Redeploy this version", false)
		has(t, "a viewer", production, text(body), "Your role here is to look", true)
		has(t, "a viewer", production, body, "Export jobspec", true) // reading stays
	})
	t.Run("Configure", func(t *testing.T) {
		body := page(member, staging, "/configure")
		has(t, "a deployer", staging, body, `action="/apps/`+staging+`/configure`, false)
		has(t, "a deployer", staging, body, `action="/apps/`+staging+`/delete"`, false)
		has(t, "a deployer", staging, body, `action="/apps/`+staging+`/deploy-token"`, false)
		has(t, "a deployer", staging, body, `action="/apps/`+staging+`/webhook"`, false)
		has(t, "a deployer", staging, text(body), "Your role here is to look", true)
		body = page(maintainer, staging, "/configure")
		has(t, "a maintainer", staging, body, `action="/apps/`+staging+`/configure`, true)
		has(t, "a maintainer", staging, body, `action="/apps/`+staging+`/delete"`, true)
	})
	t.Run("Variables, Domains and Backups", func(t *testing.T) {
		body := page(member, staging, "/variables")
		has(t, "a deployer", staging, body, `action="/apps/`+staging+`/variables"`, false)
		has(t, "a deployer", staging, body, `action="/apps/`+staging+`/variables/env"`, false)
		body = page(maintainer, staging, "/variables")
		has(t, "a maintainer", staging, body, `action="/apps/`+staging+`/variables"`, true)
		body = page(member, staging, "/domains")
		has(t, "a deployer", staging, body, "Add a domain", false)
		body = page(maintainer, staging, "/domains")
		has(t, "a maintainer", staging, body, "Add a domain", true)
	})
}

func TestBackupControlsFollowTheRole(t *testing.T) {
	e := shopConsole(t)
	if resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
		"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {"s3cr3t-key-marker"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /settings/backups = %d:\n%s", resp.StatusCode, text(body))
	}
	a := shopWeb("staging", "latest")
	a.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	a.Services[0].Backup = &app.Backup{Engine: app.BackupVolume}
	e.seed(t, a, seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1, Definition: &a})
	e.copies(a.ID, "web", 1, 1, 0)
	deployer := e.personCookie(t, "dee@example.com", "member", nil, []store.ProjectRoleRecord{{Project: "shop", Role: "deploy"}})
	get := func(c *http.Cookie) string {
		resp, body := e.do(t, request{path: "/apps/" + a.ID + "/backups", cookies: []*http.Cookie{c}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET backups = %d", resp.StatusCode)
		}
		return body
	}
	owner, member := get(e.Cookie), get(deployer)
	if !strings.Contains(owner, "Restore on a schedule") || !strings.Contains(owner, `action="/apps/`+a.ID+`/backups"`) {
		t.Errorf("the owner's Backups lacks Back up now or the schedule:\n%s", text(owner))
	}
	if !strings.Contains(member, `action="/apps/`+a.ID+`/backups"`) {
		t.Error("a deployer can't Back up now")
	}
	for _, absent := range []string{"Restore on a schedule", "Where backups go", `/backups/policy`, `/restore-schedule`} {
		if strings.Contains(member, absent) {
			t.Errorf("a deployer's Backups shows %q", absent)
		}
	}
}

// Deployments, actions, backups and restores say who did them: "by you" for the viewer, else the email;
// a token adds its name.
func TestWhoWordsNameThePerson(t *testing.T) {
	e := shopConsole(t)
	a := shopWeb("staging", "latest")
	e.seed(t, a)
	ctx := context.Background()
	create := func(id string, d app.Deployment) {
		t.Helper()
		d.ID, d.Digests = id, map[string]string{"web": "traefik/whoami@" + sampleDigest}
		if _, err := e.Store.CreateDeployment(ctx, a.ID, d, &a, nil, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: new(uint64(1))}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: store.StatusHealthy}); err != nil {
			t.Fatal(err)
		}
	}
	create("by-owner", app.Deployment{Trigger: app.TriggerConsole, Actor: testEmail})
	create("by-ana", app.Deployment{Trigger: app.TriggerConsole, Actor: "ana@example.com"})
	create("by-token", app.Deployment{Trigger: app.TriggerManual, TokenName: "ci", Actor: "ana@example.com"})
	create("by-mine", app.Deployment{Trigger: app.TriggerManual, TokenName: "mine", Actor: testEmail})
	create("old", app.Deployment{Trigger: app.TriggerConsole}) // before people had names

	_, body := e.get(t, "/apps/"+a.ID+"/deployments")
	got := text(body)
	for _, want := range []string{"by you", "by ana@example.com", "through the API by ci (ana@example.com)", "through the API by mine (you)", "from the console"} {
		if !strings.Contains(got, want) {
			t.Errorf("the owner's history lacks %q:\n%s", want, got)
		}
	}
	dee := e.personCookie(t, "dee@example.com", "member", nil, []store.ProjectRoleRecord{{Project: "shop", Role: "view"}})
	_, body = e.do(t, request{path: "/apps/" + a.ID + "/deployments", cookies: []*http.Cookie{dee}})
	got = text(body)
	if strings.Contains(got, "by you ") || strings.Contains(got, "(you)") || !strings.Contains(got, "by "+testEmail) {
		t.Errorf("dee's history says \"by you\" for the owner's deploy, or doesn't name the owner:\n%s", got)
	}

	// The words of an action and of a backup run follow the same rule.
	row := actionRowOf(time.Now(), store.ActionRecord{Action: store.ActionRestart, Trigger: string(app.TriggerConsole), Actor: testEmail}, testEmail)
	if row.Trigger != "by you" {
		t.Errorf("my action = %q, want \"by you\"", row.Trigger)
	}
	row = actionRowOf(time.Now(), store.ActionRecord{Action: store.ActionRestart, Trigger: string(app.TriggerManual), Actor: "ana@example.com"}, testEmail)
	if row.Trigger != "through the API by ana@example.com" {
		t.Errorf("ana's action over the API = %q", row.Trigger)
	}
	row = actionRowOf(time.Now(), store.ActionRecord{Action: store.ActionRestart, Trigger: string(app.TriggerConsole)}, testEmail)
	if row.Trigger != "from the console" {
		t.Errorf("an action with no actor = %q, want the old words", row.Trigger)
	}
}

// ---- the live streams ---------------------------------------------------------------------------

func (h *fakeHub) publishChange(cs ...service.Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range cs {
		for _, ch := range h.subs {
			ch <- c
		}
	}
}

// what the streams send follows the role: a notice that names an app only to a person who may see it,
// the installation's only to admins, the Operator stream and an app's stream only to those who may.
func TestLiveStreamsFollowRoles(t *testing.T) {
	e := newLiveEnv(t, liveSettings{}, nil)
	member := e.personCookie(t, "mia@example.com", "member", nil, []store.ProjectRoleRecord{{Project: "default", Role: "view"}})

	expectEvents := func(ls *liveStream, want ...string) {
		t.Helper()
		for _, w := range want {
			ls.expect(w)
		}
	}
	// The member's /live: the sync, then their apps' notices and the lists, never other apps' or the host's.
	ls := openStream(t, e.srv.URL, "/live", member.Value)
	ls.expect("sync")
	e.hub.publishChange(
		service.Change{AppID: "shop--production--web", What: service.ChangeDeployments}, // an app they can't see
		service.Change{What: service.ChangeServers},
		service.Change{What: service.ChangeMetrics},
		service.Change{What: service.ChangeApps}, // a list the service filters: sent, and the first thing that is
	)
	expectEvents(ls, "apps")
	e.hub.publishChange(service.Change{AppID: "whoami", What: service.ChangeDeployments}) // an app they can see
	expectEvents(ls, "deployments")
}

// visibleTo: a notice that names an app goes to a person who may see it; one that names none goes to
// everyone when it is about a list the service filters, to admins only when it is the installation's.
func TestNoticeVisibility(t *testing.T) {
	e := newConsoleEnv(t)
	s := e.Server
	member := service.Principal{PersonID: "m", OrgRole: service.OrgMember, Roles: []service.ProjectRole{{Project: "shop", Role: service.RoleView}}}
	admin := service.Principal{PersonID: "a", OrgRole: service.OrgAdmin}
	for _, tc := range []struct {
		c            service.Change
		member, adm  bool
		memberMetric bool // with the Monitoring page's own stream
	}{
		{service.Change{AppID: "shop--production--web", What: service.ChangeDeployments}, true, true, true},
		{service.Change{AppID: "other--production--web", What: service.ChangeDeployments}, false, true, false},
		{service.Change{AppID: "other--production--web", What: service.ChangeDeleted}, false, true, false},
		{service.Change{What: service.ChangeApps}, true, true, true},
		{service.Change{What: service.ChangeActivity}, true, true, true},
		{service.Change{What: service.ChangeAttention}, true, true, true},
		{service.Change{What: service.ChangeSchedules}, true, true, true},
		{service.Change{What: service.ChangeServers}, false, true, false},
		{service.Change{What: service.ChangeHost}, false, true, false},
		{service.Change{What: service.ChangeUpdates}, false, true, false},
		{service.Change{What: service.ChangeScans}, false, true, false},
		{service.Change{What: service.ChangeConsoleAddress}, false, true, false},
		{service.Change{What: service.ChangeMetrics}, false, true, true},
	} {
		if got := s.visibleTo(member, tc.c, false); got != tc.member {
			t.Errorf("%+v to a member = %v, want %v", tc.c, got, tc.member)
		}
		if got := s.visibleTo(member, tc.c, true); got != (tc.member || tc.memberMetric) {
			t.Errorf("%+v to a member on the Monitoring stream = %v, want %v", tc.c, got, tc.member || tc.memberMetric)
		}
		if got := s.visibleTo(admin, tc.c, false); got != tc.adm {
			t.Errorf("%+v to an admin = %v, want %v", tc.c, got, tc.adm)
		}
	}
}

func TestLiveOperatorStreamIsForAdmins(t *testing.T) {
	e := newLiveEnv(t, liveSettings{}, nil)
	member := e.personCookie(t, "mia@example.com", "member", nil, []store.ProjectRoleRecord{{Project: "default", Role: "view"}})
	ls := openStream(t, e.srv.URL, "/live?watch=ops", member.Value)
	ls.expect("ended")
	ls.expectClosed()
	ol := openStream(t, e.srv.URL, "/live?watch=ops", e.session(t).Value)
	ol.expect("sync")
}

func TestLiveAppStreamNeedsView(t *testing.T) {
	e := newLiveEnv(t, liveSettings{}, nil)
	member := e.personCookie(t, "mia@example.com", "member", nil, []store.ProjectRoleRecord{{Project: "default", Role: "view"}})
	ls := openStream(t, e.srv.URL, "/live/apps/other--production--web", member.Value)
	ls.expect("ended")
	ls.expectClosed()
	ok := openStream(t, e.srv.URL, "/live/apps/whoami", member.Value)
	ok.expect("sync")

	// A role taken away ends the stream at the next check.
	if err := e.Store.ReplacePerson(context.Background(), personID("mia@example.com"), "member", nil, nil); err != nil {
		t.Fatal(err)
	}
	ok.expect("ended")
}

// personID is the ID personCookie gave a person.
func personID(email string) string {
	id := strings.NewReplacer(".", "", "-", "").Replace(strings.Split(email, "@")[0]) + strings.Repeat("0", 26)
	return id[:26]
}
