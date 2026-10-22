package main

// The console-projects step of the lab test (feature 019, quickstart.md "Lab"
// 2): the console's projects, environments, copy and promote, done through its
// forms the way console_lab_test.go's other steps are. It is in its own file
// only because console_lab_test.go is long; runConsole lists it with the others.

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// pageText is a page as text: tags dropped, entities decoded, runs of white
// space one space. The console's sentences are set in spans, so a sentence is
// read as the owner reads it.
func pageText(body string) string {
	plain := regexp.MustCompile(`(?s)<[^>]*>`).ReplaceAllString(body, " ")
	return strings.Join(strings.Fields(html.UnescapeString(plain)), " ")
}

// latestDeployment is the newest deployment of an app, as the API lists it.
func (c *console) latestDeployment(t *testing.T, appID string) gatePlaceDeployment {
	t.Helper()
	var list struct {
		Deployments []gatePlaceDeployment `json:"deployments"`
	}
	c.g.getJSON(t, "/v1/apps/"+appID+"/deployments?limit=1", &list)
	if len(list.Deployments) == 0 {
		t.Fatalf("%s has no deployment", appID)
	}
	return list.Deployments[0]
}

// shortDeploymentID is a deployment ID as the console shortens it: its first
// eight characters.
func shortDeploymentID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// projects: the console's projects and environments, through its forms. In
// order: New project (a taken or bad name is refused on the form), an
// environment added, New app placed in it (the select's choice is the app's
// ID, con019--staging--web), the app page's breadcrumb and its Copy to
// production, the copy's Configure notice, Promote (the preview page; a stale
// revision is 412; the right one deploys the never-deployed copy with
// staging's digest, and its history says "Promoted from staging"), the grouped
// Apps page, and then the deletes in the order the console allows them: the
// project and the environment refuse while apps are in them, the apps go, then
// the environments and the project. The apps' domains aren't set here (that is
// Configure's, and the gate step projects-promote covers a domain over HTTPS).
// Budget: two deploys, each waited for up to 4 minutes, normally under one.
func (c *console) projects(t *testing.T) {
	const (
		project = "con019"
		display = "Console Nineteen"
	)
	stagingID, prodID := project+"--staging--web", project+"--production--web"
	remove := func() {
		for _, path := range []string{"/v1/apps/" + stagingID, "/v1/apps/" + prodID, "/v1/projects/" + project} {
			if resp, err := c.g.apiRequest(http.MethodDelete, path, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(remove)

	// New project.
	if resp, body := c.get(t, "/projects/new"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `name="displayName"`) {
		t.Fatalf("GET /projects/new = %d, want the form", resp.StatusCode)
	}
	resp, body := c.post(t, "/projects", url.Values{"name": {project}, "displayName": {display}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/projects/"+project+"?created=1" {
		t.Fatalf("POST /projects = %d → %q, want 303 to the project's page: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	resp, body = c.get(t, "/projects/"+project+"?created=1")
	if got := pageText(body); resp.StatusCode != http.StatusOK || !strings.Contains(got, "Project "+display+" created with a production environment.") {
		t.Fatalf("GET the project's page = %d, want the created notice:\n%s", resp.StatusCode, got)
	}
	if resp, body = c.post(t, "/projects", url.Values{"name": {project}}); resp.StatusCode != http.StatusConflict || !strings.Contains(pageText(body), "already exists") {
		t.Errorf("POST /projects with a taken name = %d, want 409 saying it exists", resp.StatusCode)
	}
	if resp, _ = c.post(t, "/projects", url.Values{"name": {"con--019"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("POST /projects with a name that holds -- = %d, want 422", resp.StatusCode)
	}

	// Add an environment.
	resp, body = c.post(t, "/projects/"+project+"/environments", url.Values{"environment": {"staging"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/projects/"+project {
		t.Fatalf("POST the environment = %d → %q, want 303 to the project's page: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	if _, body = c.get(t, "/projects/"+project); !strings.Contains(body, "production") || !strings.Contains(body, "staging") {
		t.Errorf("the project's page lacks its two environments:\n%s", pageText(body))
	}
	if resp, _ = c.post(t, "/projects/"+project+"/environments", url.Values{"environment": {"staging"}}); resp.StatusCode != http.StatusConflict {
		t.Errorf("POST the same environment again = %d, want 409", resp.StatusCode)
	}

	// New app, placed by the select.
	resp, body = c.get(t, "/apps/new?project="+project+"&environment=staging")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<option value="`+project+`/staging" selected>`) {
		t.Fatalf("GET /apps/new?project=…&environment=staging = %d, want the place select on con019 › staging:\n%s", resp.StatusCode, body)
	}
	form := url.Values{"name": {"web"}, "image": {"traefik/whoami:v1.10"}, "port": {"80"}, "healthPath": {"/"},
		"place": {project + "/staging"}}
	resp, body = c.post(t, "/apps", form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+stagingID+"/deployments" {
		t.Fatalf("POST /apps with a place = %d → %q, want 303 to the Deployments tab of %s: %s", resp.StatusCode, resp.Header.Get("Location"), stagingID, body)
	}
	stagingDep := c.latestDeployment(t, stagingID)
	if final := c.g.waitFinalOf(t, stagingID, stagingDep.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("staging's deployment ended %s: %s", final.Status, final.Error)
	}
	stagingDep = c.latestDeployment(t, stagingID) // now with its digests
	if resp, body = c.post(t, "/apps", form); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(pageText(body), "already exists in "+project+"/staging") {
		t.Errorf("POST /apps for the same name and place = %d, want 422 saying it exists there", resp.StatusCode)
	}

	// The app page: where it is (the top bar's crumbs, project / environment; the h1 carries
	// the app's name), and the other environment.
	resp, body = c.get(t, "/apps/"+stagingID)
	where := regexp.MustCompile(`(?s)<nav aria-label="Breadcrumb".*?</nav>`).FindString(body)
	// The "/" between crumbs is its own element, so the spaces around it are the template's, not
	// the page's meaning: any amount is fine.
	crumbText := regexp.MustCompile(regexp.QuoteMeta(display) + `\s*/\s*staging`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(where, `<a href="/projects/`+project+`" class="cv-crumb">`+display+`</a>`) ||
		!crumbText.MatchString(pageText(where)) {
		t.Fatalf("GET /apps/%s = %d; its crumbs are %q, want %q linked to the project", stagingID, resp.StatusCode, pageText(where), display+" / staging")
	}
	if !strings.Contains(pageText(body), "Copy to production") {
		t.Errorf("the app page of %s has no Copy to production", stagingID)
	}

	// Copy to production: the copy's Configure says what it still needs.
	resp, body = c.post(t, "/apps/"+stagingID+"/copy", url.Values{"to": {"production"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+prodID+"/configure?copied=production" {
		t.Fatalf("POST the copy = %d → %q, want 303 to the copy's Configure: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	resp, body = c.get(t, resp.Header.Get("Location"))
	// pageText puts a space around each element, and the environment is a <code>
	// element in the notice, so its two halves are checked apart.
	if got := pageText(body); resp.StatusCode != http.StatusOK || !strings.Contains(got, "Copied to production") ||
		!strings.Contains(got, "Set its domains before deploying.") {
		t.Fatalf("the copy's Configure = %d, want the copied notice:\n%s", resp.StatusCode, got)
	}
	if _, _, err := c.g.target.Nomad.Jobs().Info(prodID, nil); err == nil {
		t.Errorf("the copy registered a Nomad job for %s", prodID)
	}
	if resp, _ = c.post(t, "/apps/"+stagingID+"/copy", url.Values{"to": {"production"}}); resp.StatusCode != http.StatusConflict {
		t.Errorf("POST the copy again = %d, want 409", resp.StatusCode)
	}
	if _, body = c.get(t, "/apps/"+stagingID); !strings.Contains(pageText(body), "Promote to production") {
		t.Errorf("after the copy the app page of %s has no Promote to production", stagingID)
	}

	// Promote: the preview, a stale revision, the real one.
	resp, body = c.get(t, "/apps/"+stagingID+"/promote?to=production")
	revision := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(body)
	if got := pageText(body); resp.StatusCode != http.StatusOK || revision == nil || !strings.Contains(got, "Promote to production") ||
		!strings.Contains(got, "not deployed yet") || !strings.Contains(got, "keeps its env, secrets, domains, memory, copies and backups") {
		t.Fatalf("GET the promote page = %d, want the preview of a copy not deployed yet, with a revision:\n%s", resp.StatusCode, got)
	}
	resp, body = c.post(t, "/apps/"+stagingID+"/promote", url.Values{"to": {"production"}, "revision": {"0000000000000000"}})
	if got := pageText(body); resp.StatusCode != http.StatusPreconditionFailed || !strings.Contains(got, "changed since this preview; review it again.") {
		t.Fatalf("POST the promotion with a stale revision = %d, want 412 asking for a new look:\n%s", resp.StatusCode, got)
	}
	if _, _, err := c.g.target.Nomad.Jobs().Info(prodID, nil); err == nil {
		t.Fatalf("a refused promotion registered the job %s", prodID)
	}
	resp, body = c.post(t, "/apps/"+stagingID+"/promote", url.Values{"to": {"production"}, "revision": {revision[1]}})
	promotedID := c.deployedFrom(t, prodID, resp, body)
	if got := c.latestDeployment(t, prodID); got.ID != promotedID || got.PromotedFrom == nil || got.PromotedFrom.App != stagingID ||
		got.PromotedFrom.Environment != "staging" || got.PromotedFrom.Deployment != stagingDep.ID || got.Digests["web"] != stagingDep.Digests["web"] {
		t.Errorf("production's newest deployment = %+v, want the promotion of staging's %s with its digest %s", got, stagingDep.ID, stagingDep.Digests["web"])
	}
	_, body = c.get(t, "/apps/"+prodID+"/deployments")
	if !strings.Contains(pageText(body), "Promoted from staging · deployment "+shortDeploymentID(stagingDep.ID)) {
		t.Errorf("production's Deployments tab doesn't say where the promotion came from:\n%s", pageText(body))
	}

	// The Apps page groups by project, with this one's apps under their environments.
	for _, path := range []string{"/apps", "/parts/apps"} {
		resp, body = c.get(t, path)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<a href="/projects/`+project+`">`+display+`</a>`) ||
			!strings.Contains(body, `href="/apps/`+stagingID+`"`) || !strings.Contains(body, `href="/apps/`+prodID+`"`) {
			t.Errorf("GET %s = %d, want the apps of %s grouped under it:\n%s", path, resp.StatusCode, display, pageText(body))
		}
	}

	// Deletes, in the order the console allows them.
	resp, body = c.post(t, "/projects/"+project+"/delete", url.Values{"confirm": {project}})
	if got := pageText(body); resp.StatusCode != http.StatusConflict || !strings.Contains(got, stagingID) || !strings.Contains(got, prodID) {
		t.Errorf("POST the project's delete with apps in it = %d, want 409 naming them:\n%s", resp.StatusCode, got)
	}
	if resp, _ = c.post(t, "/projects/"+project+"/environments/staging/delete", nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("POST staging's delete with an app in it = %d, want 409", resp.StatusCode)
	}
	for _, id := range []string{stagingID, prodID} {
		resp, body = c.post(t, "/apps/"+id+"/delete", url.Values{"confirm": {id}})
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps?deleted="+id {
			t.Fatalf("POST the delete of %s = %d → %q: %s", id, resp.StatusCode, resp.Header.Get("Location"), body)
		}
		waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
			_, _, err := c.g.target.Nomad.Jobs().Info(id, nil)
			return err != nil && strings.Contains(err.Error(), "404")
		})
	}
	for _, environment := range []string{"staging", "production"} {
		resp, body = c.post(t, "/projects/"+project+"/environments/"+environment+"/delete", nil)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/projects/"+project {
			t.Fatalf("POST the delete of %s = %d → %q: %s", environment, resp.StatusCode, resp.Header.Get("Location"), body)
		}
	}
	if resp, _ = c.post(t, "/projects/"+project+"/delete", url.Values{"confirm": {"wrong"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("POST the project's delete with the wrong name = %d, want 422", resp.StatusCode)
	}
	resp, body = c.post(t, "/projects/"+project+"/delete", url.Values{"confirm": {project}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps?projectDeleted="+project {
		t.Fatalf("POST the project's delete = %d → %q: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	if resp, _ = c.get(t, "/projects/"+project); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET the deleted project's page = %d, want 404", resp.StatusCode)
	}
	if _, body = c.get(t, "/apps"); strings.Contains(body, "/projects/"+project) {
		t.Errorf("the Apps page still links the deleted project")
	}
}
