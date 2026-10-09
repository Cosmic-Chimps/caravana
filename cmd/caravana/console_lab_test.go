package main

// The console steps of the lab test (feature 007, research R21): what the
// admin does in the web console, done over HTTP the way a browser does it
// (sign in, plain form posts, the live stream), with the API token used only
// to read what the console shows. They run in TestLab before the gate steps
// and alone in TestConsoleLab; both are opt-in (CARAVANA_LAB=1).
//
// A browser also runs JavaScript, which these steps don't. That part is
// checked by hand with specs/007-htmx-ui-v1/quickstart.md.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

// consoleApp is the part of the API's AppSummary these steps read.
type consoleApp struct {
	App struct {
		ID       string `json:"id"`
		Services []struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		} `json:"services"`
	} `json:"app"`
	Status struct {
		State         string `json:"state"`
		HealthyCopies int    `json:"healthyCopies"`
	} `json:"status"`
	LastDeployment *struct {
		ID      string `json:"id"`
		Trigger string `json:"trigger"`
		Status  string `json:"status"`
		Error   string `json:"error"`
	} `json:"lastDeployment"`
}

// console is one signed-in browser: a cookie jar, redirects not followed (so
// the steps can check where a post sends the browser), and an open live stream.
type console struct {
	g      *gate
	web    *http.Client
	stream *http.Client    // the same cookies, no timeout: a stream stays open
	events chan string     // names of the events on the live stream
	prev   string          // the deployment before the one a step is waiting for
	ctx    context.Context // lives as long as runConsole's test, not one step: the stream outlives the step that opens it
}

// runConsole runs the steps in order and stops at the first failure.
func runConsole(t *testing.T, target gateTarget, email, password string) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // t is runConsole's, so this runs after the last step
	c := &console{
		ctx: ctx,
		g:   &gate{target: target},
		web: &http.Client{
			Jar:           jar,
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		stream: &http.Client{Jar: jar},
	}
	steps := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"console-sign-in", func(t *testing.T) { c.signIn(t, email, password) }},
		{"console-clean", c.g.clean},
		{"console-create", c.create},
		{"console-healthy", c.healthy},
		{"console-reads", c.reads},
		{"console-deploy-changed", c.deployChanged},
		{"console-secrets", c.secrets},
		{"console-redeploy-version", c.redeployVersion},
		{"console-delete", c.deleteApp},
	}
	for _, s := range steps {
		if !t.Run(s.name, s.run) {
			return // later steps build on this one
		}
	}
}

// signIn: the pages need a session, the sign-in form gives one (303 to the
// Apps page with the cookie), and an empty Apps page says so.
func (c *console) signIn(t *testing.T, email, password string) {
	if resp, _ := c.get(t, "/apps"); resp.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("GET /apps without a session = %d → %q, want 303 to /login", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := c.post(t, "/login", url.Values{"email": {email}, "password": {password}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps" {
		t.Fatalf("POST /login = %d → %q, want 303 to /apps: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	resp, body = c.get(t, "/apps")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "No apps yet.") {
		t.Fatalf("GET /apps after signing in = %d, want 200 saying \"No apps yet.\":\n%s", resp.StatusCode, body)
	}
}

// create: the New app form deploys whoami and sends the browser to its
// Deployments tab; a second try is refused on the Name field; and the live
// stream of the app page, opened first, says "sync" and then "deployments"
// within 5 s (the page would refresh that region).
func (c *console) create(t *testing.T) {
	c.openLive(t, "/live/apps/whoami")
	c.expectEvent(t, "sync", 5*time.Second)

	form := url.Values{"name": {"whoami"}, "image": {"traefik/whoami:latest"}, "port": {"80"},
		"domain": {c.g.target.AppHost}, "healthPath": {"/"}}
	resp, body := c.post(t, "/apps", form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/whoami/deployments" {
		t.Fatalf("POST /apps = %d → %q, want 303 to /apps/whoami/deployments: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	c.expectEvent(t, "deployments", 5*time.Second)

	resp, body = c.post(t, "/apps", form)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "already exists") {
		t.Errorf("POST /apps for the same name = %d, want 422 saying it already exists", resp.StatusCode)
	}
	a := c.summary(t)
	if a.LastDeployment == nil || a.LastDeployment.Trigger != "console" {
		t.Fatalf("whoami's deployment after the form = %+v, want one with trigger console", a.LastDeployment)
	}
	c.prev = a.LastDeployment.ID
}

// healthy: the deployment ends healthy, the app has a healthy copy and answers.
func (c *console) healthy(t *testing.T) {
	c.waitDeployment(t, 3*time.Minute, "")
	if a := c.summary(t); a.Status.State != "healthy" || a.Status.HealthyCopies < 1 {
		t.Errorf("app status = %+v, want healthy with a healthy copy", a.Status)
	}
	waitFor(t, "whoami to answer over HTTPS", time.Minute, func() bool { return c.g.callApp() == nil })
}

// reads: the tabs render for a signed-in admin, /account lands on /settings,
// and the console's reads of copies and logs work (a log line shows up after
// calling the app).
func (c *console) reads(t *testing.T) {
	for _, path := range []string{"/apps", "/apps/whoami", "/apps/whoami/deployments", "/apps/whoami/configure",
		"/apps/whoami/logs", "/apps/new", "/settings"} {
		if resp, _ := c.get(t, path); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	if resp, _ := c.get(t, "/account"); resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/settings" {
		t.Errorf("GET /account = %d → %q, want 301 to /settings", resp.StatusCode, resp.Header.Get("Location"))
	}

	var copies struct {
		Copies []struct{ Status string } `json:"copies"`
	}
	c.g.getJSON(t, "/v1/apps/whoami/copies", &copies)
	if len(copies.Copies) == 0 || copies.Copies[0].Status != "healthy" {
		t.Errorf("copies = %+v, want a healthy one", copies.Copies)
	}

	var logs struct {
		Lines []struct{ Copy, Stream, Text string } `json:"lines"`
	}
	waitFor(t, "a log line of whoami", time.Minute, func() bool {
		_ = c.g.callApp() // whoami logs what it receives
		logs.Lines = nil
		resp, err := c.g.apiRequest(http.MethodGet, "/v1/apps/whoami/logs?lines=10", "", "")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&logs) == nil && len(logs.Lines) > 0
	})
}

// deployChanged: the Deploy form with a new tag deploys it (303 to the
// Deployments tab), the new deployment ends healthy, and the stored app has
// the new image.
func (c *console) deployChanged(t *testing.T) {
	a := c.summary(t)
	c.prev = a.LastDeployment.ID
	resp, body := c.post(t, "/apps/whoami/deploy", url.Values{"image.whoami": {"traefik/whoami:v1.10"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/whoami/deployments" {
		t.Fatalf("POST /apps/whoami/deploy = %d → %q, want 303 to /apps/whoami/deployments: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	c.waitDeployment(t, 3*time.Minute, c.prev)

	var stored struct {
		App struct {
			Services []struct{ Image string } `json:"services"`
		} `json:"app"`
	}
	c.g.getJSON(t, "/v1/apps/whoami", &stored)
	if len(stored.App.Services) != 1 || stored.App.Services[0].Image != "traefik/whoami:v1.10" {
		t.Errorf("stored app = %+v, want the image traefik/whoami:v1.10", stored.App.Services)
	}
	waitFor(t, "whoami to answer over HTTPS", time.Minute, func() bool { return c.g.callApp() == nil })
}

// secrets: setting a secret from Configure is a deploy that ends healthy with
// the value in Nomad Variables and only the name on the page; removing it is
// another deploy, and the variable is gone (feature 008).
func (c *console) secrets(t *testing.T) {
	value := "console " + c.g.run
	path := "nomad/jobs/whoami/whoami"
	change := func(route string, form url.Values) {
		t.Helper()
		c.prev = c.summary(t).LastDeployment.ID
		resp, body := c.post(t, route, form)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/whoami/deployments" {
			t.Fatalf("POST %s = %d → %q: %s", route, resp.StatusCode, resp.Header.Get("Location"), body)
		}
		c.waitDeployment(t, 3*time.Minute, c.prev)
	}

	change("/apps/whoami/secrets", url.Values{"service": {"whoami"}, "name": {"CONSOLE_SECRET"}, "value": {value}})
	v, _, err := c.g.target.Nomad.Variables().Read(path, nil)
	if err != nil || v.Items["CONSOLE_SECRET"] != value {
		t.Fatalf("Nomad variable %s = %v, %v; want CONSOLE_SECRET set", path, v, err)
	}
	if _, body := c.get(t, "/apps/whoami/configure"); !strings.Contains(body, "CONSOLE_SECRET") || strings.Contains(body, value) {
		t.Errorf("Configure should list CONSOLE_SECRET and never its value")
	}

	change("/apps/whoami/secrets/remove", url.Values{"service": {"whoami"}, "name": {"CONSOLE_SECRET"}})
	if _, _, err := c.g.target.Nomad.Variables().Read(path, nil); err == nil {
		t.Errorf("Nomad variable %s is still there after the secret was removed", path)
	}
}

// redeployVersion: the history's "Redeploy this version" on the first healthy
// deployment (before the image changed) deploys it again from the console,
// names it as the source, and the stored app is that version once more
// (feature 011).
func (c *console) redeployVersion(t *testing.T) {
	var list struct {
		Deployments []struct {
			ID                 string `json:"id"`
			Status             string `json:"status"`
			Source             string `json:"source"`
			Trigger            string `json:"trigger"`
			DefinitionRecorded bool   `json:"definitionRecorded"`
		} `json:"deployments"`
	}
	c.g.getJSON(t, "/v1/apps/whoami/deployments?limit=100", &list)
	past := ""
	for _, d := range list.Deployments { // newest first: the last match is the oldest
		if d.Status == "healthy" && d.DefinitionRecorded {
			past = d.ID
		}
	}
	if past == "" {
		t.Fatal("no healthy deployment to go back to")
	}
	if _, body := c.get(t, "/apps/whoami/deployments"); !strings.Contains(body, "/apps/whoami/deployments/"+past+"/redeploy") {
		t.Fatalf("the history doesn't offer to redeploy %s", past)
	}

	c.prev = c.summary(t).LastDeployment.ID
	resp, body := c.post(t, "/apps/whoami/deployments/"+past+"/redeploy", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/whoami/deployments" {
		t.Fatalf("POST redeploy = %d → %q: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	c.waitDeployment(t, 3*time.Minute, c.prev)

	c.g.getJSON(t, "/v1/apps/whoami/deployments?limit=1", &list)
	if d := list.Deployments[0]; d.Source != past || d.Trigger != "console" || d.Status != "healthy" {
		t.Errorf("newest deployment = %+v, want a healthy console redeploy of %s", d, past)
	}
	var stored struct {
		App struct {
			Services []struct{ Image string } `json:"services"`
		} `json:"app"`
	}
	c.g.getJSON(t, "/v1/apps/whoami", &stored)
	if stored.App.Services[0].Image == "traefik/whoami:v1.10" {
		t.Errorf("the stored app still has the later image %s", stored.App.Services[0].Image)
	}
	waitFor(t, "whoami to answer over HTTPS", time.Minute, func() bool { return c.g.callApp() == nil })
}

// deleteApp: a wrong confirmation changes nothing (422); the right one sends
// the browser to /apps with the notice, stops and purges the Nomad job, and
// erases the app (the API says 404); the open stream says "deleted".
func (c *console) deleteApp(t *testing.T) {
	resp, _ := c.post(t, "/apps/whoami/delete", url.Values{"confirm": {"whoam"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("POST /apps/whoami/delete with the wrong name = %d, want 422", resp.StatusCode)
	}
	if _, _, err := c.g.target.Nomad.Jobs().Info("whoami", nil); err != nil {
		t.Fatalf("the wrong confirmation touched the Nomad job: %v", err)
	}

	resp, body := c.post(t, "/apps/whoami/delete", url.Values{"confirm": {"whoami"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps?deleted=whoami" {
		t.Fatalf("POST /apps/whoami/delete = %d → %q, want 303 to /apps?deleted=whoami: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	c.expectEvent(t, "deleted", 5*time.Second)
	if resp, body := c.get(t, "/apps?deleted=whoami"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "was deleted") {
		t.Errorf("GET /apps?deleted=whoami = %d, want 200 saying the app was deleted", resp.StatusCode)
	}

	waitFor(t, "whoami's Nomad job to be gone", time.Minute, func() bool {
		_, _, err := c.g.target.Nomad.Jobs().Info("whoami", nil)
		return err != nil && strings.Contains(err.Error(), "404")
	})
	api, err := c.g.apiRequest(http.MethodGet, "/v1/apps/whoami", "", "")
	if err != nil {
		t.Fatal(err)
	}
	api.Body.Close()
	if api.StatusCode != http.StatusNotFound {
		t.Errorf("GET /v1/apps/whoami after the delete = %d, want 404", api.StatusCode)
	}
}

// get and post call the console's pages with the browser's cookies. Posts are
// plain form posts, with no Origin or Sec-Fetch-Site header, like a command
// line client's (the cross-origin check lets those through).
func (c *console) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	resp, err := c.web.Get(c.g.target.API + path)
	return c.read(t, resp, err)
}

func (c *console) post(t *testing.T, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := c.web.PostForm(c.g.target.API+path, form)
	return c.read(t, resp, err)
}

func (c *console) read(t *testing.T, resp *http.Response, err error) (*http.Response, string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, string(body)
}

// summary is whoami's entry of GET /v1/apps, the call the Apps page twins.
func (c *console) summary(t *testing.T) consoleApp {
	t.Helper()
	var list struct {
		Apps []consoleApp `json:"apps"`
	}
	c.g.getJSON(t, "/v1/apps", &list)
	for _, a := range list.Apps {
		if a.App.ID == "whoami" {
			return a
		}
	}
	t.Fatalf("GET /v1/apps doesn't list whoami: %+v", list.Apps)
	return consoleApp{}
}

// waitDeployment waits until whoami's newest deployment is another one than
// notID and is healthy. It fails early when that deployment ends any other way.
func (c *console) waitDeployment(t *testing.T, within time.Duration, notID string) {
	t.Helper()
	var last consoleApp
	ok := waitUntil(within, func() bool {
		resp, err := c.g.apiRequest(http.MethodGet, "/v1/apps", "", "")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var list struct {
			Apps []consoleApp `json:"apps"`
		}
		if json.NewDecoder(resp.Body).Decode(&list) != nil {
			return false
		}
		for _, a := range list.Apps {
			if a.App.ID == "whoami" {
				last = a
			}
		}
		d := last.LastDeployment
		if d == nil || d.ID == notID {
			return false
		}
		switch d.Status {
		case "healthy":
			return true
		case "failed", "reverted", "superseded":
			t.Fatalf("deployment %s ended %s: %s", d.ID, d.Status, d.Error)
		}
		return false
	})
	if !ok {
		t.Fatalf("no new healthy deployment of whoami within %v (last: %+v)", within, last.LastDeployment)
	}
}

// openLive opens a live stream of the console as the signed-in browser and
// collects the names of its events (a "event: name" line each).
func (c *console) openLive(t *testing.T, path string) {
	t.Helper()
	ctx := c.ctx
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.g.target.API+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body.Close()
		t.Fatalf("GET %s = %d %q, want 200 text/event-stream", path, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := make(chan string, 64)
	c.events = events
	go func() {
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if name, ok := strings.CutPrefix(scanner.Text(), "event: "); ok {
				select {
				case events <- name:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

// expectEvent waits for an event of that name on the open stream; events of
// other names that come first are skipped.
func (c *console) expectEvent(t *testing.T, name string, within time.Duration) {
	t.Helper()
	timeout := time.After(within)
	var seen []string
	for {
		select {
		case got := <-c.events:
			if got == name {
				return
			}
			seen = append(seen, got)
		case <-timeout:
			t.Errorf("no %q event on the live stream within %v (saw %s)", name, within, fmt.Sprint(seen))
			return
		}
	}
}
