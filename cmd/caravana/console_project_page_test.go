package main

// The console-project-page step of the lab test (feature 027.5, the handoff's ProjectScreen): a
// project's page with its environment tabs, the apps as a table of links and the rail, its two live
// parts, its settings page and the sidebar's order, done through the console's own pages. It is in
// its own file only because console_lab_test.go is long; runConsole lists it right after
// console-projects.

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	sidebarAside = regexp.MustCompile(`(?s)<aside class="cv-side">.*?</aside>`)
	navHrefs     = regexp.MustCompile(`<a href="([^"]*)" class="cv-nav-item"`)
	currentTab   = regexp.MustCompile(`<a href="[^"]*" class="cv-envtab[^"]*" aria-current="page">([^<]*)</a>`)
)

// between is the part of body from the first start to the first end after it, or "" when either is
// missing (the caller's check then fails with the page in the message).
func between(body, start, end string) string {
	i := strings.Index(body, start)
	if i < 0 {
		return ""
	}
	j := strings.Index(body[i:], end)
	if j < 0 {
		return ""
	}
	return body[i : i+j+len(end)]
}

// projectPage: what the project's page says of real apps.
//
//  1. Default › production holds whoami (healthy, with its domain): its row is one link with the
//     healthy pill, one healthy mark for each healthy copy, the address, v<n> and a time; the
//     Environment panel says how it was last deployed and the addresses; Deploys in
//     production lists whoami; the Needs you section is not there while nothing needs the owner.
//  2. A project of its own (the form) with a staging environment: the tabs, ?env= switching, the
//     fallback for an unknown environment, and the empty state.
//  3. A never-deployed app created in staging (New app's Create without deploying): its row says
//     "never deployed" and draws — for its copies and its version, and the panel has no deploy yet.
//  4. The two live parts answer the same markup, 404 for an unknown project.
//  5. The settings page: its forms, the display name's redirect, Add environment's redirect to the
//     new tab, the environment delete's redirect.
//  6. The sidebar's order, in the aside of three pages: Workspace (Home, Projects, Logs, Monitoring,
//     Schedules), Configure (Domains), then Settings.
//
// Nothing is deployed here: it costs no memory and no waiting. It leaves nothing behind.
func (c *console) projectPage(t *testing.T) {
	const (
		project = "con0275"
		display = "Console Page"
	)
	laterID := project + "--staging--later"
	remove := func() {
		for _, path := range []string{"/v1/apps/" + laterID, "/v1/projects/" + project} {
			if resp, err := c.g.apiRequest(http.MethodDelete, path, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(remove)

	// 1. Default › production, with whoami.
	a := c.summary(t)
	// The live version is in the list's summaries (getApp answers the record, which has no status).
	var list struct {
		Apps []struct {
			App struct {
				ID string `json:"id"`
			} `json:"app"`
			Status struct {
				LiveVersion *uint64 `json:"liveVersion"`
			} `json:"status"`
		} `json:"apps"`
	}
	c.g.getJSON(t, "/v1/apps", &list)
	var liveVersion *uint64
	for _, x := range list.Apps {
		if x.App.ID == "whoami" {
			liveVersion = x.Status.LiveVersion
		}
	}
	resp, body := c.get(t, "/projects/default")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<h1 class="cv-pj-h1">Default</h1>`) {
		t.Fatalf("GET /projects/default = %d, want the project's page:\n%s", resp.StatusCode, pageText(body))
	}
	row := between(body, `<a href="/apps/whoami" class="cv-row cv-pj-row">`, "</a>")
	if row == "" {
		t.Fatalf("/projects/default has no row for whoami:\n%s", pageText(body))
	}
	if a.Status.State != "healthy" || !strings.Contains(row, `cv-pill-healthy cv-pill-quiet cv-pill-sm`) || !strings.Contains(pageText(row), "healthy") {
		t.Errorf("whoami's row doesn't have the healthy pill (the app is %s): %s", a.Status.State, pageText(row))
	}
	marks := between(row, `<span class="cv-pj-groups"`, `<span class="sr-only">`)
	if n := strings.Count(marks, `cv-mark cv-mark-healthy`); n != a.Status.HealthyCopies || n < 1 {
		t.Errorf("whoami's row has %d healthy marks, want one for each of its %d healthy copies: %s", n, a.Status.HealthyCopies, marks)
	}
	if !strings.Contains(row, `title="`+c.g.target.AppHost+`"`) {
		t.Errorf("whoami's row doesn't show its address %s: %s", c.g.target.AppHost, pageText(row))
	}
	if liveVersion == nil || !strings.Contains(pageText(row), "v"+strconv.FormatUint(*liveVersion, 10)+" · ") || !strings.Contains(row, "<time ") {
		t.Errorf("whoami's row doesn't say its live version %v and when: %s", liveVersion, pageText(row))
	}
	panel := pageText(between(body, `<dl class="cv-panel cv-pj-env">`, "</dl>"))
	for _, want := range []string{"Deploys ", " · ", "Addresses 1"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the Environment panel of Default › production lacks %q: %s", want, panel)
		}
	}
	if !strings.Contains(pageText(between(body, `id="pj-deploys-title"`, "</section>")), "Deploys in production whoami") {
		t.Errorf("Deploys in production doesn't list whoami:\n%s", pageText(between(body, `id="pj-deploys-title"`, "</section>")))
	}
	if strings.Contains(panel, "Promote to") {
		t.Errorf("a project with one environment offers a promotion: %s", panel)
	}

	// 2. A project of its own, with staging.
	if resp, body = c.post(t, "/projects", url.Values{"name": {project}, "displayName": {display}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /projects = %d: %s", resp.StatusCode, pageText(body))
	}
	resp, body = c.post(t, "/projects/"+project+"/environments", url.Values{"environment": {"staging"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/projects/"+project+"?env=staging" {
		t.Fatalf("POST the environment = %d → %q, want 303 to the new tab: %s", resp.StatusCode, resp.Header.Get("Location"), pageText(body))
	}
	tab := func(path string) string {
		t.Helper()
		resp, body := c.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d:\n%s", path, resp.StatusCode, pageText(body))
		}
		if !strings.Contains(between(body, `<nav aria-label="Environments"`, "</nav>"), `>production</a>`) {
			t.Errorf("GET %s: the tabs row lacks production", path)
		}
		m := currentTab.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("GET %s has no current tab:\n%s", path, between(body, `<nav aria-label="Environments"`, "</nav>"))
		}
		return m[1]
	}
	for _, tc := range []struct{ path, want string }{
		{"/projects/" + project, "production"},
		{"/projects/" + project + "?env=staging", "staging"},
		{"/projects/" + project + "?env=production", "production"},
		{"/projects/" + project + "?env=gone", "production"}, // a stale link falls back, with no error
	} {
		if got := tab(tc.path); got != tc.want {
			t.Errorf("GET %s has %q current, want %q", tc.path, got, tc.want)
		}
	}
	_, body = c.get(t, "/projects/"+project+"?env=staging")
	for _, want := range []string{"Apps · 0", "No apps in staging yet.", "Deploys None yet", "Addresses none", "No deploys in staging yet."} {
		if !strings.Contains(pageText(body), want) {
			t.Errorf("the empty staging page lacks %q:\n%s", want, pageText(body))
		}
	}
	if !strings.Contains(body, `href="/apps/new?project=`+project+`&amp;environment=staging"`) {
		t.Errorf("the empty staging page doesn't lead to New app with staging chosen:\n%s", body)
	}

	// 3. A never-deployed app in staging.
	form := url.Values{"name": {"later"}, "image": {""}, "port": {""}, "domain": {""}, "healthPath": {"/"}, "place": {project + "/staging"}, "action": {"create"}}
	if resp, body = c.post(t, "/apps", form); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+laterID+"?created=1" {
		t.Fatalf("POST /apps with Create without deploying = %d → %q: %s", resp.StatusCode, resp.Header.Get("Location"), pageText(body))
	}
	_, body = c.get(t, "/projects/"+project+"?env=staging")
	later := between(body, `<a href="/apps/`+laterID+`" class="cv-row cv-pj-row">`, "</a>")
	if later == "" || !strings.Contains(pageText(later), "later") || !strings.Contains(pageText(later), "not defined yet") {
		t.Fatalf("staging has no row for the name-only app, with its status:\n%s", pageText(body))
	}
	if !strings.Contains(later, `<span class="cv-pj-none" aria-hidden="true">—</span>`) || !strings.Contains(later, `<span class="cv-pj-ver">—</span>`) ||
		!strings.Contains(later, "no address") {
		t.Errorf("a never-deployed row doesn't draw — for its copies and its version, and no address: %s", pageText(later))
	}
	for _, want := range []string{"Apps · 1", "Deploys None yet", "Addresses none"} {
		if !strings.Contains(pageText(body), want) {
			t.Errorf("staging with one app lacks %q:\n%s", want, pageText(body))
		}
	}
	_, prod := c.get(t, "/projects/"+project)
	if !strings.Contains(pageText(prod), "No apps in production yet.") {
		t.Errorf("production, which has no app, doesn't say so:\n%s", pageText(prod))
	}

	// 4. The live parts.
	for _, path := range []string{"/parts/projects/" + project + "/apps?env=staging", "/parts/projects/" + project + "/rail?env=staging"} {
		resp, part := c.get(t, path)
		if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") {
			t.Errorf("GET %s = %d, want a fragment:\n%s", path, resp.StatusCode, pageText(part))
		}
		if strings.Contains(path, "/apps?") && !strings.Contains(part, `href="/apps/`+laterID+`"`) {
			t.Errorf("the apps part lacks the app's row:\n%s", pageText(part))
		}
		if strings.Contains(path, "/rail?") && !strings.Contains(pageText(part), "Deploys in staging") {
			t.Errorf("the rail part lacks Deploys in staging:\n%s", pageText(part))
		}
	}
	for _, path := range []string{"/parts/projects/nope-0275/apps", "/parts/projects/nope-0275/rail"} {
		if resp, _ := c.get(t, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
	if resp, _ := c.get(t, "/projects/nope-0275"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET an unknown project's page = %d, want 404", resp.StatusCode)
	}

	// 5. The settings page.
	resp, body = c.get(t, "/projects/"+project+"/settings")
	if got := pageText(body); resp.StatusCode != http.StatusOK || !strings.Contains(got, display+" settings") || !strings.Contains(body, `<section id="add-environment"`) {
		t.Fatalf("GET the settings page = %d, want its heading and the Add environment section:\n%s", resp.StatusCode, got)
	}
	for _, action := range []string{"/projects/" + project, "/projects/" + project + "/environments", "/projects/" + project + "/delete"} {
		if !strings.Contains(body, `action="`+action+`"`) {
			t.Errorf("the settings page has no form posting to %s", action)
		}
	}
	if strings.Contains(prod, `name="displayName"`) {
		t.Errorf("the project's page still holds the display name form")
	}
	resp, body = c.post(t, "/projects/"+project, url.Values{"displayName": {display + " Two"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/projects/"+project+"/settings?saved=1" {
		t.Fatalf("POST the display name = %d → %q, want 303 to the settings page: %s", resp.StatusCode, resp.Header.Get("Location"), pageText(body))
	}
	if _, body = c.get(t, "/projects/"+project+"/settings?saved=1"); !strings.Contains(pageText(body), "Display name saved.") {
		t.Errorf("the settings page doesn't say the display name was saved:\n%s", pageText(body))
	}
	resp, body = c.post(t, "/projects/"+project+"/environments", url.Values{"environment": {"qa"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/projects/"+project+"?env=qa" {
		t.Fatalf("POST Add environment = %d → %q, want 303 to the new tab: %s", resp.StatusCode, resp.Header.Get("Location"), pageText(body))
	}
	if got := tab("/projects/" + project + "?env=qa"); got != "qa" {
		t.Errorf("the new environment's tab isn't current (%q)", got)
	}
	resp, body = c.post(t, "/projects/"+project+"/environments", url.Values{"environment": {"qa"}})
	if got := pageText(body); resp.StatusCode != http.StatusConflict || !strings.Contains(got, "This project already has an environment called") || !strings.Contains(got, display+" Two settings") {
		t.Errorf("POST Add environment again = %d, want the settings page again with the reason:\n%s", resp.StatusCode, got)
	}
	resp, body = c.post(t, "/projects/"+project+"/environments/qa/delete", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/projects/"+project+"/settings" {
		t.Errorf("POST the environment's delete = %d → %q, want 303 to the settings page: %s", resp.StatusCode, resp.Header.Get("Location"), pageText(body))
	}

	// 6. The sidebar's order.
	want := []string{"/apps", "/projects", "/logs", "/monitoring", "/schedules", "/domains", "/settings"}
	for _, path := range []string{"/apps", "/projects/default", "/projects/" + project + "/settings"} {
		_, body := c.get(t, path)
		aside := sidebarAside.FindString(body)
		var got []string
		for _, m := range navHrefs.FindAllStringSubmatch(aside, -1) {
			got = append(got, m[1])
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s's sidebar lists %v, want %v", path, got, want)
		}
		text := pageText(aside)
		if w, g := strings.Index(text, "Workspace"), strings.Index(text, "Configure"); w < 0 || g < w || strings.Index(text, "Domains") < g || strings.Index(text, "Schedules") > g {
			t.Errorf("%s's sidebar groups aren't Workspace, then Configure with Domains: %s", path, text)
		}
	}
}
