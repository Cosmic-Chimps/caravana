package main

// The console-build step of the lab test (feature 050, plan D12): New app › Build from Git through the
// console's own forms, against a Git server and a registry on the lab VM's loopback (lab_git_test.go,
// on ports of its own). It is in its own file only because console_lab_test.go is long; runConsole lists
// it after console-security.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// consoleBuild: an app built from a public repository, made through the form.
//
//  1. Settings › Builds renders the five limits (after lowering them for the 2 GiB VM through the API).
//  2. New app › Build from Git with the repository, the image, a port and a domain, "Build and deploy":
//     303 to the Deployments tab, which shows the Build stage and the Build log link while it builds.
//  3. The build log page renders the repository, commit, author and subject and, once it ends, the log.
//  4. The app is healthy, the domain answers the commit's page, the history says the commit it built.
//
// Budget: the first build pulls the BuildKit and Git images (two or three minutes); the step waits up to
// twenty.
func (c *console) consoleBuild(t *testing.T) {
	const appID = "con050"
	g := c.g
	if g.target.Public != nil {
		t.Skip("the build step needs the lab's own registry and Git server on the VM's loopback; it is lab only")
	}
	host := g.extraHost(appID)

	g.lowerBuildSettings(t)
	lab := newBuildLab(t, g, "gate-gitserver-console", "gate-regbuild-console", 2223, 8099, 5002)
	lab.start(t)
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+appID, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover of an aborted run, if any
	t.Cleanup(remove)

	// 1. Settings › Builds.
	resp, body := c.get(t, "/settings/builds")
	text := pageText(body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings/builds = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"Builds", "Memory", "CPU", "Timeout", "Builds at once", "Build cache"} {
		if !strings.Contains(text, want) {
			t.Errorf("Settings › Builds doesn't show %q:\n%s", want, text)
		}
	}

	for _, want := range []string{`value="768"`, `value="1000"`, `value="10"`, `value="2048"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Settings › Builds has no input with %s", want)
		}
	}

	// 2. New app › Build from Git.
	if resp, body := c.get(t, "/apps/new?from=git"); resp.StatusCode != http.StatusOK ||
		!strings.Contains(pageText(body), "Build and deploy") || !strings.Contains(body, `name="repository"`) {
		t.Fatalf("GET /apps/new?from=git = %d, want the form with a repository field and Build and deploy", resp.StatusCode)
	}
	sha := lab.commit(t, labDockerfile("con-build-v1"), "Console build: the first version")
	lab.push(t, "main")
	form := url.Values{
		"name": {appID}, "image": {lab.regHost() + "/" + appID}, "port": {"8080"}, "domain": {host}, "healthPath": {"/"},
		"repository": {lab.httpURL()}, "branch": {"main"}, "folder": {""}, "dockerfile": {""},
		"access": {"public"}, "action": {"deploy"},
	}
	resp, body = c.post(t, "/apps/new/git", form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+appID+"/deployments" {
		t.Fatalf("POST /apps/new/git = %d → %q, want 303 to /apps/%s/deployments: %s", resp.StatusCode, resp.Header.Get("Location"), appID, pageText(body))
	}
	var deploymentID string
	waitFor(t, "the app's first deployment to exist", time.Minute, func() bool {
		var list struct {
			Deployments []gateDeployment `json:"deployments"`
		}
		resp, err := g.apiRequest(http.MethodGet, "/v1/apps/"+appID+"/deployments", "", "")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list.Deployments) == 0 {
			return false
		}
		deploymentID = list.Deployments[0].ID
		return true
	})
	resp, body = c.get(t, "/apps/"+appID+"/deployments")
	text = pageText(body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(text, "Build") {
		t.Fatalf("GET the Deployments tab = %d, want a history entry with a Build stage:\n%s", resp.StatusCode, text)
	}
	logPath := "/apps/" + appID + "/deployments/" + deploymentID + "/build"

	// 3. The build log page, while it builds and after.
	resp, body = c.get(t, logPath)
	text = pageText(body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(text, "Build of deployment") || !strings.Contains(text, lab.httpURL()) {
		t.Fatalf("GET %s = %d, want the build page naming the repository %s:\n%s", logPath, resp.StatusCode, lab.httpURL(), text)
	}

	// 4. Healthy, and the commit is what answers.
	done := g.waitFinalOf(t, appID, deploymentID, 20*time.Minute)
	if done.Status != "healthy" {
		b := g.buildOf(t, appID, deploymentID)
		t.Fatalf("the console's build ended %s: %s\nbuild %s: %s\n%s", done.Status, done.Error, b.Status, b.Error, b.Log)
	}
	g.waitBody(t, host, "con-build-v1", time.Minute)

	resp, body = c.get(t, logPath)
	text = pageText(body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s after the build = %d", logPath, resp.StatusCode)
	}
	for _, want := range []string{"built", sha, "Gate Test", "Console build: the first version", "sha256:", "Log"} {
		if !strings.Contains(text, want) {
			t.Errorf("the build page doesn't show %q:\n%s", want, text)
		}
	}
	resp, body = c.get(t, "/apps/"+appID+"/deployments")
	text = pageText(body)
	for _, want := range []string{sha[:7], "Console build: the first version", "Build log"} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(text, want) {
			t.Errorf("the Deployments tab after the build doesn't show %q:\n%s", want, text)
		}
	}
	if resp, body := c.get(t, "/parts/apps/"+appID+"/deployments/"+deploymentID+"/build"); resp.StatusCode != http.StatusOK && resp.StatusCode != 286 {
		t.Errorf("GET the build's live part = %d, want 200 or 286: %s", resp.StatusCode, body)
	}

	// Delete it through the console, as a user would, so the cache volume and variables go too.
	resp, body = c.post(t, "/apps/"+appID+"/delete", url.Values{"confirm": {appID}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /apps/%s/delete = %d, want 303: %s", appID, resp.StatusCode, pageText(body))
	}
}
