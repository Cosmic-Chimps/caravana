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
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
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
		{"console-edit", c.edit},
		{"console-redeploy-version", c.redeployVersion},
		{"console-needs-you", c.needsYou},
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

// edit: the Configure form (feature 014) changes the environment and the
// memory of whoami in one save: one deployment that ends healthy, Nomad's job
// has the new env and memory, and the stored app kept what the form doesn't
// touch. A second post from the same form, now stale, is refused with 409 and
// registers nothing. (Volumes and backup are kept by the same merge; a volume
// needs a host volume plugin, which the unit tests cover.)
func (c *console) edit(t *testing.T) {
	value := "edit " + c.g.run
	_, page := c.get(t, "/apps/whoami/configure")
	form := editFormValues(t, page)
	if form.Get("revision") == "" || form.Get("svc.whoami.image") != "traefik/whoami:v1.10" {
		t.Fatalf("the Configure form = %v, want a revision and the image traefik/whoami:v1.10", form)
	}
	stale := url.Values{}
	for k, v := range form {
		stale[k] = v
	}

	form.Set("svc.whoami.env", "CONSOLE_EDIT="+value)
	form.Set("svc.whoami.memory", "200")
	form.Set("svc.whoami.memoryMax", "300")
	c.prev = c.summary(t).LastDeployment.ID
	resp, body := c.post(t, "/apps/whoami/configure", form)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/apps/whoami/deployments#deployment-") {
		t.Fatalf("POST /apps/whoami/configure = %d → %q, want 303 to the new deployment: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	c.waitDeployment(t, 3*time.Minute, c.prev)
	deployed := c.summary(t).LastDeployment.ID

	job, _, err := c.g.target.Nomad.Jobs().Info("whoami", nil)
	if err != nil {
		t.Fatalf("read Nomad's job: %v", err)
	}
	task := job.TaskGroups[0].Tasks[0]
	if task.Env["CONSOLE_EDIT"] != value || task.Resources == nil || task.Resources.MemoryMB == nil || *task.Resources.MemoryMB != 200 ||
		task.Resources.MemoryMaxMB == nil || *task.Resources.MemoryMaxMB != 300 {
		t.Errorf("Nomad's task env = %v, resources = %+v; want CONSOLE_EDIT, 200 MB and a 300 MB ceiling", task.Env, task.Resources)
	}

	var stored struct {
		App struct {
			Services []struct {
				Image   string                         `json:"image"`
				Ports   []struct{ ContainerPort int }  `json:"ports"`
				Domains []struct{ Host string }        `json:"domains"`
				Health  struct{ Path string }          `json:"health"`
				Env     []struct{ Name, Value string } `json:"env"`
			} `json:"services"`
		} `json:"app"`
	}
	c.g.getJSON(t, "/v1/apps/whoami", &stored)
	svc := stored.App.Services[0]
	if svc.Image != "traefik/whoami:v1.10" || len(svc.Ports) != 1 || svc.Ports[0].ContainerPort != 80 ||
		len(svc.Domains) != 1 || svc.Domains[0].Host != c.g.target.AppHost || svc.Health.Path != "/" ||
		len(svc.Env) != 1 || svc.Env[0].Name != "CONSOLE_EDIT" {
		t.Errorf("stored service = %+v, want the image, port, domain and health check kept and the one env var", svc)
	}
	waitFor(t, "whoami to answer over HTTPS", time.Minute, func() bool { return c.g.callApp() == nil })

	// The form opened before the save is stale now.
	stale.Set("svc.whoami.env", "SHOULD_NOT=land")
	resp, body = c.post(t, "/apps/whoami/configure", stale)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "This app changed since you opened the form.") {
		t.Errorf("POST from a stale form = %d, want 409 with the stale panel", resp.StatusCode)
	}
	if got := c.summary(t).LastDeployment.ID; got != deployed {
		t.Errorf("a stale save started deployment %s, want none after %s", got, deployed)
	}
	c.g.getJSON(t, "/v1/apps/whoami", &stored)
	if env := stored.App.Services[0].Env; len(env) != 1 || env[0].Name != "CONSOLE_EDIT" {
		t.Errorf("a stale save changed the stored env to %+v", env)
	}
}

// editFormValues reads the values of the Configure form from its page, as a
// browser without script would send them: every named input and textarea.
func editFormValues(t *testing.T, page string) url.Values {
	t.Helper()
	m := regexp.MustCompile(`(?s)<form id="edit-form"[^>]*>(.*?)</form>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no edit form on the Configure page:\n%s", page)
	}
	attr := func(tag, name string) string {
		v := regexp.MustCompile(`\s` + name + `="([^"]*)"`).FindStringSubmatch(tag)
		if v == nil {
			return ""
		}
		return html.UnescapeString(v[1])
	}
	values := url.Values{}
	for _, in := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(m[1], -1) {
		if name := attr(in, "name"); name != "" {
			values.Set(name, attr(in, "value"))
		}
	}
	for _, ta := range regexp.MustCompile(`(?s)<textarea\b([^>]*)>(.*?)</textarea>`).FindAllStringSubmatch(m[1], -1) {
		values.Set(attr(ta[1], "name"), strings.TrimPrefix(html.UnescapeString(ta[2]), "\n"))
	}
	return values
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

// attentionItem is the part of an AttentionItem of GET /v1/attention these
// steps read.
type attentionItem struct {
	Kind         string   `json:"kind"`
	AppID        string   `json:"appId"`
	Service      string   `json:"service"`
	Copies       []string `json:"copies"`
	DeploymentID string   `json:"deploymentId"`
	Detail       string   `json:"detail"`
}

// attention reads the needs-you list through the API, as the Apps page's twin.
func (c *console) attention(t *testing.T) (items []attentionItem, copiesUnknown bool) {
	t.Helper()
	var list struct {
		Items         []attentionItem `json:"items"`
		CopiesUnknown bool            `json:"copiesUnknown"`
	}
	c.g.getJSON(t, "/v1/attention", &list)
	return list.Items, list.CopiesUnknown
}

// attentionOf finds the item of that kind for that app.
func attentionOf(items []attentionItem, kind, appID string) (attentionItem, bool) {
	for _, it := range items {
		if it.Kind == kind && it.AppID == appID {
			return it, true
		}
	}
	return attentionItem{}, false
}

// needsYou: the needs-you list (feature 014, US5) against real Nomad.
//
// A deploy of an image that doesn't exist fails before anything is
// registered, which is quick; a health check that can't pass takes Nomad's
// 10 minute progress deadline to end "reverted", which the unit tests cover
// (the console says "was reverted" for those). The failed deploy puts a
// deploy_failed item on the API's list and on /parts/attention, with a link
// to the deployment, and the live stream of the Apps page says "attention";
// a working deploy after it takes the item away.
//
// Then a service that eats memory past its ceiling is killed by the kernel:
// within 60 s its copy says outOfMemory (the oom_killed event comes in the
// allocation list, research R8) and the list has an out_of_memory item.
func (c *console) needsYou(t *testing.T) {
	events := c.events
	defer func() { c.events = events }() // the app page's stream, which deleteApp reads
	c.openLive(t, "/live")
	c.expectEvent(t, "sync", 5*time.Second)

	c.prev = c.summary(t).LastDeployment.ID
	resp, body := c.post(t, "/apps/whoami/deploy", url.Values{"image.whoami": {"traefik/whoami:does-not-exist"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /apps/whoami/deploy with a missing tag = %d, want 422: %s", resp.StatusCode, body)
	}
	c.expectEvent(t, "attention", 30*time.Second)
	failed := c.summary(t).LastDeployment
	if failed == nil || failed.ID == c.prev || failed.Status != "failed" {
		t.Fatalf("whoami's newest deployment = %+v, want a new failed one", failed)
	}

	var item attentionItem
	waitFor(t, "a deploy_failed item for whoami", 30*time.Second, func() bool {
		items, _ := c.attention(t)
		var ok bool
		item, ok = attentionOf(items, "deploy_failed", "whoami")
		return ok
	})
	if item.DeploymentID != failed.ID || !strings.Contains(item.Detail, "does-not-exist") {
		t.Errorf("deploy_failed item = %+v, want the failed deployment %s and its error", item, failed.ID)
	}
	_, part := c.get(t, "/parts/attention")
	for _, want := range []string{"deploy failed", `href="/apps/whoami/deployments#deployment-` + failed.ID + `"`} {
		if !strings.Contains(part, want) {
			t.Errorf("/parts/attention lacks %q:\n%s", want, part)
		}
	}
	if _, apps := c.get(t, "/apps"); !strings.Contains(apps, "whoami") || !strings.Contains(apps, "needs you") {
		t.Errorf("the Apps page doesn't say whoami needs you")
	}

	// A working deploy takes it away, with nothing done to the item.
	c.prev = failed.ID
	resp, body = c.post(t, "/apps/whoami/deploy", url.Values{"image.whoami": {"traefik/whoami:latest"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /apps/whoami/deploy = %d, want 303: %s", resp.StatusCode, body)
	}
	c.waitDeployment(t, 3*time.Minute, failed.ID)
	waitFor(t, "the deploy_failed item to be gone", 30*time.Second, func() bool {
		items, _ := c.attention(t)
		_, still := attentionOf(items, "deploy_failed", "whoami")
		return !still
	})
	if _, part := c.get(t, "/parts/attention"); strings.Contains(part, failed.ID) {
		t.Errorf("/parts/attention still links to the failed deployment %s:\n%s", failed.ID, part)
	}
	waitFor(t, "whoami to answer over HTTPS", time.Minute, func() bool { return c.g.callApp() == nil })

	// Out of memory. With no restart policy in the job, Nomad's default is
	// two restarts, then the copy fails and is replaced, so the copy is there
	// as running (not ended) only part of the time: the check reads the copy
	// and the list in one go, and tries again until both agree.
	oomer := `{"source": {"kind": "image"}, "services": [{
	  "name": "hog", "image": "busybox:1.37", "command": "sh", "args": ["-c", "head -c 64m /dev/zero | tail"],
	  "resources": {"memoryMB": 16, "memoryMaxMB": 24}}]}`
	resp = c.g.api(t, http.MethodPost, "/v1/apps/oomer/deployments", "application/json", oomer)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deploy the memory hog = %d: %s", resp.StatusCode, raw)
	}
	t.Cleanup(func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/oomer", "", ""); err == nil {
			resp.Body.Close()
		}
	})

	var killed struct {
		ID, Status    string
		OutOfMemory   bool   `json:"outOfMemory"`
		LastRestartAt string `json:"lastRestartAt"`
	}
	var oom attentionItem
	var attentionPart string
	found := waitUntil(60*time.Second, func() bool {
		var copies struct {
			Copies []struct {
				ID, Status    string
				OutOfMemory   bool   `json:"outOfMemory"`
				LastRestartAt string `json:"lastRestartAt"`
			} `json:"copies"`
		}
		resp, err := c.g.apiRequest(http.MethodGet, "/v1/apps/oomer/copies", "", "")
		if err != nil {
			return false
		}
		err = json.NewDecoder(resp.Body).Decode(&copies)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			return false
		}
		for _, cp := range copies.Copies {
			if cp.OutOfMemory && cp.Status != "failed" && cp.Status != "stopped" {
				killed = cp
			}
		}
		if !killed.OutOfMemory {
			return false
		}
		items, _ := c.attention(t)
		var ok bool
		oom, ok = attentionOf(items, "out_of_memory", "oomer")
		if ok {
			_, attentionPart = c.get(t, "/parts/attention")
		}
		return ok
	})
	if !found {
		t.Fatalf("within 60 s the memory hog's copy never showed outOfMemory with an out_of_memory item (last copy: %+v)", killed)
	}
	if oom.Service != "hog" || len(oom.Copies) == 0 || killed.LastRestartAt == "" {
		t.Errorf("out_of_memory item = %+v, copy = %+v; want service hog, its copies, and a last restart time", oom, killed)
	}
	for _, want := range []string{"oomer/hog", "ran out of memory.", `href="/apps/oomer/configure#svc-hog-memory"`} {
		if !strings.Contains(attentionPart, want) {
			t.Errorf("/parts/attention lacks %q:\n%s", want, attentionPart)
		}
	}
	if _, running := c.get(t, "/parts/apps/oomer/copies"); !strings.Contains(running, "ran out of memory") {
		t.Errorf("the Running tab's copies don't say a copy ran out of memory:\n%s", running)
	}
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
