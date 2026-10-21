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
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify"
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
		// run names this lab run in the secrets the steps make up (the volumes
		// step's bucket key, for one), as the gate's own does.
		g: &gate{target: target, run: strconv.FormatInt(time.Now().UnixNano(), 36)},
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
		{"console-usage", c.usage},
		{"console-deploy-changed", c.deployChanged},
		{"console-secrets", c.secrets},
		{"console-edit", c.edit},
		{"console-redeploy-version", c.redeployVersion},
		{"console-needs-you", c.needsYou},
		{"console-compose", c.compose},
		{"console-compose-file-edit", c.composeFileEdit},
		{"console-volumes", c.volumes},
		{"console-notifications", c.notifications},
		{"console-projects", c.projects},
		{"console-create-only", c.createOnly},
		{"console-app-actions", c.appActions},
		{"console-terminal", c.terminal},
		{"console-mcp", c.mcpServer},
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

// usage: CPU and memory per copy (feature 014, US4) against real Nomad, with
// the token of the scoped policy, so a missing capability for the allocation's
// stats fails here. A running copy of whoami has non-zero memory (its service
// task's, not the sidecar's), and the reservation is the app's (the defaults,
// whoami sets none). The Running tab's copies part shows it, and the first
// answer says which of Nomad's memory measures was used (cgroups v2 reports
// Usage, v1 RSS) in the test log.
func (c *console) usage(t *testing.T) {
	var copies struct {
		Copies []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Usage  *struct {
				CPUMHz           *int   `json:"cpuMHz"`
				MemoryMB         *int   `json:"memoryMB"`
				CPUReservedMHz   *int   `json:"cpuReservedMHz"`
				MemoryReservedMB *int   `json:"memoryReservedMB"`
				MemoryCeilingMB  *int   `json:"memoryCeilingMB"`
				Error            string `json:"error"`
			} `json:"usage"`
		} `json:"copies"`
	}
	waitFor(t, "a running copy of whoami with usage", time.Minute, func() bool {
		copies.Copies = nil
		c.g.getJSON(t, "/v1/apps/whoami/copies?usage=true", &copies)
		for _, cp := range copies.Copies {
			if cp.Status == "healthy" && cp.Usage != nil && cp.Usage.Error == "" && cp.Usage.MemoryMB != nil && *cp.Usage.MemoryMB > 0 {
				return true
			}
		}
		return false
	})
	for _, cp := range copies.Copies {
		if cp.Status != "healthy" {
			continue
		}
		u := cp.Usage
		if u == nil || u.Error != "" || u.CPUMHz == nil || u.MemoryMB == nil || *u.MemoryMB <= 0 ||
			u.CPUReservedMHz == nil || *u.CPUReservedMHz <= 0 || u.MemoryReservedMB == nil || *u.MemoryReservedMB <= 0 ||
			u.MemoryCeilingMB == nil || *u.MemoryCeilingMB < *u.MemoryReservedMB {
			t.Errorf("healthy copy %s usage = %+v, want numbers with the app's reservation", cp.ID, u)
			continue
		}
		t.Logf("copy %s: %d MHz, %d MB used; reserved %d MHz, %d MB; ceiling %d MB",
			cp.ID[:8], *u.CPUMHz, *u.MemoryMB, *u.CPUReservedMHz, *u.MemoryReservedMB, *u.MemoryCeilingMB)
	}

	// Without the parameter Nomad isn't asked for usage and the copy has none.
	var plain struct {
		Copies []struct {
			Usage *struct{} `json:"usage"`
		} `json:"copies"`
	}
	c.g.getJSON(t, "/v1/apps/whoami/copies", &plain)
	for _, cp := range plain.Copies {
		if cp.Usage != nil {
			t.Errorf("listCopies without usage=true has usage: %+v", cp)
		}
	}

	// The console's part says it in words.
	resp, body := c.get(t, "/parts/apps/whoami/copies")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, " MHz") || !strings.Contains(body, " MB") ||
		!strings.Contains(body, "ceiling") || strings.Contains(body, "Nomad didn't answer for this copy.") {
		t.Errorf("GET /parts/apps/whoami/copies = %d, want CPU and memory in the table:\n%s", resp.StatusCode, body)
	}
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
	return formValuesOf(t, page, "edit-form")
}

// formValuesOf is editFormValues for the form with that ID: the Compose
// forms' pages use it too. A select isn't read; none of these steps has one
// that matters.
func formValuesOf(t *testing.T, page, id string) url.Values {
	t.Helper()
	m := regexp.MustCompile(`(?s)<form id="` + id + `"[^>]*>(.*?)</form>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no form %q on the page:\n%s", id, page)
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
	// two restarts, then the copy fails and is replaced after a delay. A
	// failed copy still counts for an hour, so the item holds through the
	// replacement's wait; the check reads the copy and the list in one go.
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
			if cp.OutOfMemory && cp.Status != "stopped" {
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

// consoleCompose is a small Compose file for the console's Compose forms:
// busybox serving a page on 8080. A second version adds an environment
// variable, to see what a deploy from Compose changes.
const consoleCompose = `services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "mkdir -p /www; echo ok > /www/index.html; exec httpd -f -p 8080 -h /www"]
    ports: ["8080"]
`

const consoleComposeV2 = consoleCompose + `    environment:
      FLAG: v2
`

// compose: New app › Compose file (feature 014). The preview creates nothing;
// the second button creates the app with the health check and the secret
// typed into the page, the deployment ends healthy and the secret reaches the
// service; the stored app is what the API makes of the same file and overlay;
// a taken name is refused. Then Deploy from Compose: the overlay starts from
// the app's (the secret as a name, empty), a new file deploys with the
// secret's value kept, and the form opened before is refused as stale.
func (c *console) compose(t *testing.T) {
	const id = "console-compose"
	remove := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(remove)
	secret := "compose-secret-" + c.g.run
	appStatus := func() int {
		resp, err := c.g.apiRequest(http.MethodGet, "/v1/apps/"+id, "", "")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// envIs checks a variable inside the deployment's copy of web. Nomad's exec
	// now and then answers an empty string right after the copy starts, so it
	// asks again for a few seconds before it says the value is wrong.
	envIs := func(deployment, name, want, why string) {
		t.Helper()
		var got string
		if !waitUntil(30*time.Second, func() bool {
			got = c.g.execEnv(t, id, "web", name, deployment)
			return got == want
		}) {
			t.Errorf("%s inside the service = %q, want %s", name, got, why)
		}
	}
	// deployed reads the deployment a 303 to the app's Deployments tab names and waits for it to end healthy.
	deployed := func(resp *http.Response, body string) string {
		t.Helper()
		const prefix = "/apps/" + id + "/deployments#deployment-"
		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, prefix) {
			t.Fatalf("the post = %d → %q, want 303 to the new deployment: %s", resp.StatusCode, loc, body)
		}
		deployment := strings.TrimPrefix(loc, prefix)
		if final := c.g.waitFinalOf(t, id, deployment, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deployment %s ended %s: %s", deployment, final.Status, final.Error)
		}
		return deployment
	}

	resp, body := c.get(t, "/apps/new?from=compose")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `action="/apps/new/compose/preview"`) {
		t.Fatalf("GET /apps/new?from=compose = %d, want 200 with the Compose form: %s", resp.StatusCode, body)
	}

	// The preview shows the service and creates nothing.
	resp, preview := c.post(t, "/apps/new/compose/preview", url.Values{"name": {id}, "compose": {consoleCompose}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(preview, "busybox:1.37") || !strings.Contains(preview, "Create and deploy") {
		t.Fatalf("POST the preview = %d, want 200 with the service and the create button: %s", resp.StatusCode, preview)
	}
	if got := appStatus(); got != http.StatusNotFound {
		t.Fatalf("GET /v1/apps/%s after a preview = %d, want 404", id, got)
	}

	// Create it with the overlay typed into the page.
	form := formValuesOf(t, preview, "compose-form")
	if form.Get("svc.web.overlay") != "1" {
		t.Fatalf("the preview's form = %v, want web's overlay", form)
	}
	form.Set("svc.web.healthPath", "/")
	form.Set("svc.web.newSecret.0", "API_KEY")
	form.Set("svc.web.newSecretValue.0", secret)
	resp, body = c.post(t, "/apps/new/compose", form)
	first := deployed(resp, body)
	envIs(first, "API_KEY", secret, "the value typed on the page")

	// The stored app is what the API makes of the same file and overlay.
	request, _ := json.Marshal(map[string]any{"compose": consoleCompose, "services": map[string]any{"web": map[string]any{
		"health":  map[string]any{"path": "/"},
		"secrets": []map[string]any{{"name": "API_KEY", "value": secret}}}}})
	var stored, previewed struct {
		App any `json:"app"`
	}
	c.g.getJSON(t, "/v1/apps/"+id, &stored)
	api := c.g.api(t, http.MethodPost, "/v1/apps/"+id+"/compose/preview", "application/json", string(request))
	err := json.NewDecoder(api.Body).Decode(&previewed)
	api.Body.Close()
	if err != nil || api.StatusCode != http.StatusOK {
		t.Fatalf("the API's preview = %d: %v", api.StatusCode, err)
	}
	if !reflect.DeepEqual(stored.App, previewed.App) {
		t.Errorf("the stored app differs from the API's preview of the same request:\n stored: %v\n   API: %v", stored.App, previewed.App)
	}

	// The name is taken now, and nothing replaces the app.
	resp, body = c.post(t, "/apps/new/compose", form)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "already exists") {
		t.Errorf("POST /apps/new/compose for a taken name = %d, want 409 saying it already exists", resp.StatusCode)
	}

	// Deploy from Compose: the form carries the revision; the preview starts from the app's overlay.
	resp, body = c.get(t, "/apps/"+id+"/compose")
	if resp.StatusCode != http.StatusOK || formValuesOf(t, body, "compose-form").Get("revision") == "" {
		t.Fatalf("GET /apps/%s/compose = %d, want 200 with a revision: %s", id, resp.StatusCode, body)
	}
	revision := formValuesOf(t, body, "compose-form").Get("revision")
	resp, preview = c.post(t, "/apps/"+id+"/compose/preview", url.Values{"revision": {revision}, "compose": {consoleComposeV2}})
	again := formValuesOf(t, preview, "compose-form")
	if resp.StatusCode != http.StatusOK || again.Get("svc.web.healthPath") != "/" || !strings.Contains(preview, "Empty keeps the current value.") {
		t.Fatalf("the preview from Deploy from Compose = %d, want the app's health check and the secret kept: %s", resp.StatusCode, preview)
	}
	if v, ok := again["svc.web.secret.API_KEY"]; !ok || v[0] != "" || strings.Contains(preview, secret) {
		t.Errorf("the secret on the page = %v (present: %v), want its name with an empty input and no value anywhere", v, ok)
	}
	staleForm := url.Values{}
	for k, v := range again {
		staleForm[k] = v
	}

	resp, body = c.post(t, "/apps/"+id+"/compose", again)
	second := deployed(resp, body)
	envIs(second, "FLAG", "v2", "v2 from the new file")
	envIs(second, "API_KEY", secret, "the value kept by the empty secret input")

	// The form opened before that deploy is stale now.
	var before, after struct {
		Deployments []struct{ ID string } `json:"deployments"`
	}
	c.g.getJSON(t, "/v1/apps/"+id+"/deployments?limit=100", &before)
	resp, body = c.post(t, "/apps/"+id+"/compose", staleForm)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "This app changed since you opened the form.") {
		t.Errorf("POST from a stale form = %d, want 409 with the stale panel", resp.StatusCode)
	}
	c.g.getJSON(t, "/v1/apps/"+id+"/deployments?limit=100", &after)
	if len(after.Deployments) != len(before.Deployments) {
		t.Errorf("a stale deploy made %d deployments, want none", len(after.Deployments)-len(before.Deployments))
	}
}

// fileEditCompose is the one-service file of the compose-file-edit step, with a
// comment Caravana must keep as typed. %s is the image tag.
const fileEditCompose = `# cfe: edited here, kept as typed
services:
  web:
    image: traefik/whoami:%s   # the tag changes below
    ports: ["80"]
`

// deployedFrom reads the deployment that a 303 from an app's Compose form
// names and waits for it to end healthy.
func (c *console) deployedFrom(t *testing.T, id string, resp *http.Response, body string) string {
	t.Helper()
	const prefix = "/deployments#deployment-"
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/apps/"+id+prefix) {
		t.Fatalf("the post = %d → %q, want 303 to the new deployment of %s: %s", resp.StatusCode, loc, id, body)
	}
	deployment := strings.TrimPrefix(loc, "/apps/"+id+prefix)
	if final := c.g.waitFinalOf(t, id, deployment, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deployment %s ended %s: %s", deployment, final.Status, final.Error)
	}
	return deployment
}

// composeFileEdit: an app made from a Compose file keeps the file (feature
// 015). It is created through the console form from a file with a comment;
// Configure shows the file as typed (the comment is in the page); editing the
// tag, previewing and deploying with the revision from the page ends healthy,
// the API's app has the new image, and Configure shows the edited file. Then the
// first deployment is redeployed from the history: Configure shows v1.10 and
// its comment again, and the history downloads that deployment's file byte for
// byte.
func (c *console) composeFileEdit(t *testing.T) {
	const id = "cfe"
	remove := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(remove)
	v10, v11 := fmt.Sprintf(fileEditCompose, "v1.10"), fmt.Sprintf(fileEditCompose, "v1.11")
	imageOf := func() string {
		t.Helper()
		var stored consoleApp
		c.g.getJSON(t, "/v1/apps/"+id, &stored)
		if len(stored.App.Services) != 1 {
			t.Fatalf("GET /v1/apps/%s has %d services, want 1", id, len(stored.App.Services))
		}
		return stored.App.Services[0].Image
	}

	// Create it through the form, with its domain and health check typed on the page.
	resp, preview := c.post(t, "/apps/new/compose/preview", url.Values{"name": {id}, "compose": {v10}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(preview, "Create and deploy") {
		t.Fatalf("POST the preview = %d, want 200 with the create button: %s", resp.StatusCode, preview)
	}
	form := formValuesOf(t, preview, "compose-form")
	form.Set("svc.web.domains", id+".localhost")
	form.Set("svc.web.healthPath", "/")
	resp, body := c.post(t, "/apps/new/compose", form)
	first := c.deployedFrom(t, id, resp, body)

	// Configure shows the file as typed, the comment in it, and the overlay.
	resp, page := c.get(t, "/apps/"+id+"/configure")
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "# cfe: edited here, kept as typed") {
		t.Fatalf("GET /apps/%s/configure = %d, want 200 with the file's comment in the page: %s", id, resp.StatusCode, page)
	}
	kept := formValuesOf(t, page, "compose-form")
	if kept.Get("compose") != v10 || kept.Get("revision") == "" || kept.Get("svc.web.domains") != id+".localhost" || kept.Get("svc.web.healthPath") != "/" {
		t.Fatalf("Configure's Compose form = %v, want the file as typed, a revision, and the overlay", kept)
	}
	if !strings.Contains(page, "This app is edited through its Compose file.") || strings.Contains(page, `id="edit-form"`) {
		t.Errorf("Configure draws the per-field form of an app edited through its file")
	}

	// Edit the tag from that page: preview, then deploy with its revision.
	kept.Set("compose", v11)
	resp, preview = c.post(t, "/apps/"+id+"/compose/preview", kept)
	if resp.StatusCode != http.StatusOK || !strings.Contains(preview, "traefik/whoami:v1.11") {
		t.Fatalf("POST the preview of the edited file = %d, want 200 with the new image: %s", resp.StatusCode, preview)
	}
	resp, body = c.post(t, "/apps/"+id+"/compose", formValuesOf(t, preview, "compose-form"))
	second := c.deployedFrom(t, id, resp, body)
	if second == first {
		t.Fatalf("the edit made no new deployment (still %s)", first)
	}

	// The new image runs, and Configure shows the edited file, comment kept.
	if got := imageOf(); got != "traefik/whoami:v1.11" {
		t.Errorf("the app's image = %q, want traefik/whoami:v1.11", got)
	}
	_, page = c.get(t, "/apps/"+id+"/configure")
	if got := formValuesOf(t, page, "compose-form").Get("compose"); got != v11 {
		t.Errorf("Configure's file after the edit = %q, want %q", got, v11)
	}

	// Rollback: redeploying the first deployment from the history brings back
	// its file, so Configure shows v1.10 and its comment again.
	if _, history := c.get(t, "/apps/"+id+"/deployments"); !strings.Contains(history, "/apps/"+id+"/deployments/"+first+"/redeploy") {
		t.Fatalf("the history doesn't offer to redeploy %s", first)
	}
	resp, body = c.post(t, "/apps/"+id+"/deployments/"+first+"/redeploy", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+id+"/deployments" {
		t.Fatalf("POST redeploy = %d → %q, want 303 to the history: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	var newest struct {
		Deployments []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"deployments"`
	}
	c.g.getJSON(t, "/v1/apps/"+id+"/deployments?limit=1", &newest)
	if len(newest.Deployments) != 1 || newest.Deployments[0].Source != first {
		t.Fatalf("newest deployment = %+v, want the redeploy of %s", newest.Deployments, first)
	}
	if final := c.g.waitFinalOf(t, id, newest.Deployments[0].ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the redeploy %s ended %s: %s", newest.Deployments[0].ID, final.Status, final.Error)
	}
	if got := imageOf(); got != "traefik/whoami:v1.10" {
		t.Errorf("the app's image after the rollback = %q, want traefik/whoami:v1.10", got)
	}
	_, page = c.get(t, "/apps/"+id+"/configure")
	if got := formValuesOf(t, page, "compose-form").Get("compose"); got != v10 {
		t.Errorf("Configure's file after the rollback = %q, want the first file %q", got, v10)
	}
	if !strings.Contains(page, "# cfe: edited here, kept as typed") {
		t.Errorf("Configure after the rollback lacks the original comment")
	}

	// The history downloads each deployment's own file, byte for byte.
	resp, download := c.get(t, "/apps/"+id+"/deployments/"+first+"/compose")
	if resp.StatusCode != http.StatusOK || download != v10 {
		t.Errorf("GET the first deployment's Compose file = %d %q, want 200 with %q", resp.StatusCode, download, v10)
	}
	if want := `attachment; filename="` + id + "-" + first + `.compose.yaml"`; resp.Header.Get("Content-Disposition") != want {
		t.Errorf("Content-Disposition = %q, want %q", resp.Header.Get("Content-Disposition"), want)
	}
}

// consoleVolumeCompose is the file of the volumes step: busybox with one named
// volume, which its command writes "A" into once and then serves over HTTP, so
// the health check (GET /v) passes only once the file is there.
const consoleVolumeCompose = `services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "[ -f /data/v ] || printf A > /data/v; exec httpd -f -p 8080 -h /data"]
    ports: ["8080"]
    volumes: ["data:/data"]
volumes:
  data: {}
`

// volumes: the Volumes section and a volume backup, through the console
// (feature 016, US3). The app is made from the Compose form with the overlay's
// backup engine set to "Volume (stops the service while it runs)". Once it is
// healthy, Backups lists the volume with its mount, host path and a size (the
// caravana-volumes task measures at the copy's start, so the page is asked again
// for up to 30 s), and says there is no backup yet. "Back up now", in its
// <details>, starts a run; when it has succeeded the backup's row says how long
// the service was stopped and the Volumes row shows the last backup. Then the
// app is deleted through the console.
//
// The backup needs a destination. The console steps run first, on a Caravana
// whose data directory is new, so none is set: the step starts the gate's S3
// bucket job (gate.startBucket) and points Caravana at it, as the gate steps
// do. A destination that is already set is left alone (it is not ours to
// overwrite), and then only the backup half is skipped, with a log line.
func (c *console) volumes(t *testing.T) {
	const id = "cvol"
	remove := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(remove)

	canBackUp := false
	resp := c.g.api(t, http.MethodGet, "/v1/backups/destination", "", "")
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		c.g.startBucket(t)
		canBackUp = true
	case http.StatusOK:
		t.Log("a backup destination is already set; leaving it, and skipping the back-up half of this step")
	default:
		t.Fatalf("GET /v1/backups/destination = %d, want 404 (none yet) or 200", resp.StatusCode)
	}

	// Create the app from the Compose form, with the volume engine picked on the page.
	resp, preview := c.post(t, "/apps/new/compose/preview", url.Values{"name": {id}, "compose": {consoleVolumeCompose}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(preview, `name="svc.web.backupEngine"`) ||
		!strings.Contains(preview, `<option value="volume">Volume (stops the service while it runs)</option>`) {
		t.Fatalf("POST the preview = %d, want 200 with the backup engine choice for a service with a volume: %s", resp.StatusCode, preview)
	}
	form := formValuesOf(t, preview, "compose-form")
	form.Set("svc.web.healthPath", "/v")
	form.Set("svc.web.backupEngine", "volume") // a select, which formValuesOf doesn't read
	form.Set("svc.web.backupKeep", "2")
	resp, body := c.post(t, "/apps/new/compose", form)
	c.deployedFrom(t, id, resp, body)

	// The Volumes row: the volume, its mount, its host path and, once the task has
	// measured it, a size ("4 KB", with when it was measured).
	size := regexp.MustCompile(`>(\d+(?:\.\d)? (?:B|KB|MB|GB))</span>\s*<span class="cv-usage-of">measured `)
	var page string
	waitFor(t, "the Volumes row to show a size", 30*time.Second, func() bool {
		resp, page = c.get(t, "/apps/"+id+"/backups")
		return resp.StatusCode == http.StatusOK && size.MatchString(page)
	})
	var volumes []struct {
		Name     string `json:"name"`
		Mounted  bool   `json:"mounted"`
		HostPath string `json:"hostPath"`
		BackedUp bool   `json:"backedUp"`
		Size     *struct {
			Bytes int64 `json:"bytes"`
		} `json:"size"`
	}
	c.g.getJSON(t, "/v1/apps/"+id+"/volumes", &volumes)
	if len(volumes) != 1 || volumes[0].Name != "data" || !volumes[0].Mounted || !volumes[0].BackedUp ||
		volumes[0].HostPath == "" || volumes[0].Size == nil || volumes[0].Size.Bytes <= 0 {
		t.Fatalf("GET /v1/apps/%s/volumes = %+v, want data, mounted, backed up, with a host path and a size", id, volumes)
	}
	for _, want := range []string{`id="volumes-title"`, `<span class="cv-data text-fg-1">data</span>`, "web:/data",
		volumes[0].HostPath, "every day at 03:00 UTC", "no backup yet"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Volumes section lacks %q:\n%s", want, page)
		}
	}
	if !canBackUp {
		return
	}

	// "Back up now" is a form inside a <details>; it posts the service's name.
	m := regexp.MustCompile(`(?s)<details class="cv-confirm">\s*<summary[^>]*>Back up now…</summary>\s*<form method="post" action="(/apps/` + id + `/backups)"[^>]*>(.*?)</form>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the Backups tab has no Back up now confirmation for %s:\n%s", id, page)
	}
	attr := func(tag, name string) string {
		if v := regexp.MustCompile(`\s` + name + `="([^"]*)"`).FindStringSubmatch(tag); v != nil {
			return html.UnescapeString(v[1])
		}
		return ""
	}
	backUp := url.Values{}
	for _, in := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(m[2], -1) {
		if name := attr(in, "name"); name != "" {
			backUp.Set(name, attr(in, "value"))
		}
	}
	if backUp.Get("service") != "web" {
		t.Fatalf("the Back up now form = %v, want service web", backUp)
	}
	resp, body = c.post(t, m[1], backUp)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+id+"/backups" {
		t.Fatalf("POST Back up now = %d → %q, want 303 to the Backups tab: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}

	// The run restarts the app's own copy, archives the volume and starts it
	// again; it ends within the minute or so the restart takes, and the limit is
	// 30 minutes, so wait a few.
	var run gateVolumeRun
	waitFor(t, "the volume backup to end", 4*time.Minute, func() bool {
		var list struct {
			Backups []gateVolumeRun `json:"backups"`
		}
		c.g.getJSON(t, "/v1/apps/"+id+"/backups", &list)
		if len(list.Backups) == 0 {
			return false
		}
		run = list.Backups[0]
		return run.Status != "running"
	})
	if run.Status != "succeeded" || run.Engine != "volume" {
		t.Fatalf("the volume backup ended %s (%s) as engine %s, want succeeded as volume", run.Status, run.Message, run.Engine)
	}

	// The backup's row says the service was stopped, and the Volumes row its last backup.
	_, page = c.get(t, "/apps/"+id+"/backups")
	if !regexp.MustCompile(`stopped (?:under 1|\d+) s`).MatchString(page) {
		t.Errorf("the backup's row doesn't say how long the service was stopped:\n%s", page)
	}
	if !regexp.MustCompile(`succeeded</span>, last <time`).MatchString(page) || strings.Contains(page, "no backup yet") {
		t.Errorf("the Volumes row doesn't show the last backup:\n%s", page)
	}

	// Delete it through the console: the app and its Nomad job go.
	resp, body = c.post(t, "/apps/"+id+"/delete", url.Values{"confirm": {id}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps?deleted="+id {
		t.Fatalf("POST /apps/%s/delete = %d → %q, want 303 to /apps?deleted=%s: %s", id, resp.StatusCode, resp.Header.Get("Location"), id, body)
	}
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := c.g.target.Nomad.Jobs().Info(id, nil)
		return err != nil && strings.Contains(err.Error(), "404")
	})
	gone := c.g.api(t, http.MethodGet, "/v1/apps/"+id, "", "")
	gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Errorf("GET /v1/apps/%s after the delete = %d, want 404", id, gone.StatusCode)
	}
}

// notifyMessage is the part of a webhook message (api/openapi.yaml,
// NotificationMessage) these steps read.
type notifyMessage struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Kind       string `json:"kind"`
	App        string `json:"app"`
	Title      string `json:"title"`
	Deployment string `json:"deployment"`
	Outcome    string `json:"outcome"`
	Trigger    string `json:"trigger"`
}

// notifyRequest is one request the receiver got: what was sent, as it came.
type notifyRequest struct {
	At      time.Time
	Header  http.Header
	Body    []byte
	Message notifyMessage
}

// notifyReceiver is a webhook receiver that answers 200 to everything and
// records each request. Caravana runs in this process, so it reaches it.
type notifyReceiver struct {
	mu  sync.Mutex
	got []notifyRequest
}

func (rx *notifyReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	req := notifyRequest{At: time.Now(), Header: r.Header.Clone(), Body: body}
	_ = json.Unmarshal(body, &req.Message)
	rx.mu.Lock()
	rx.got = append(rx.got, req)
	rx.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// count is how many requests so far match.
func (rx *notifyReceiver) count(match func(notifyMessage) bool) int {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	n := 0
	for _, r := range rx.got {
		if match(r.Message) {
			n++
		}
	}
	return n
}

// first is the first request that matches, if any.
func (rx *notifyReceiver) first(match func(notifyMessage) bool) (notifyRequest, bool) {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	for _, r := range rx.got {
		if match(r.Message) {
			return r, true
		}
	}
	return notifyRequest{}, false
}

// all is a copy of every request so far.
func (rx *notifyReceiver) all() []notifyRequest {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	return append([]notifyRequest(nil), rx.got...)
}

// notifications: Settings › Notifications and the messages it sends (feature
// 018), against real Nomad. A receiver in this process stands in for the
// owner's endpoint; the channel is added through the console form with all
// three events on, and the signing secret is read from the page it is shown on
// (once). "Send a test" must arrive with a signature notify.Verify accepts.
//
// Then an app is deployed with an image tag that doesn't exist, which fails
// before anything is registered and so takes seconds, where a health check that
// can't pass takes Nomad's 10 minute progress deadline (needsYou's reasoning).
// The failed deployment must send deploy.failed once, and the notifier (which
// looks at the needs-you list every 30 s) problem.opened for it once. The
// problem has to be seen before it is fixed, or it would never have been sent.
// A working deploy then sends deploy.succeeded, and once the item has been off
// the list for the resolve delay (the lab sets CARAVANA_NOTIFY_RESOLVE_AFTER to
// 10 s) problem.resolved. A last wait of one more check shows nothing is sent
// twice. The channel is removed through the console, with its secret variable.
func (c *console) notifications(t *testing.T) {
	const (
		id      = "cnotify"
		name    = "lab receiver"
		oneGap  = 30 * time.Second // the notifier's check interval
		options = `"command": "sh", "args": ["-c", "mkdir -p /www; echo ok > /www/index.html; exec httpd -f -p 8080 -h /www"],
    "ports": [{"name": "http", "containerPort": 8080}], "health": {"path": "/", "interval": "5s", "timeout": "2s"}`
	)
	removeApp := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	removeApp() // a leftover from an aborted run, if any
	t.Cleanup(removeApp)

	rx := &notifyReceiver{}
	srv := httptest.NewServer(rx)
	t.Cleanup(srv.Close)

	// Add the channel through the form: a webhook with every event on. The
	// signing secret is in the page this answers with, and nowhere else.
	resp, body := c.post(t, "/settings/notifications", url.Values{
		"kind": {"webhook"}, "name": {name}, "url": {srv.URL},
		"events": {"problems", "deploy_failed", "deploy_succeeded"},
	})
	m := regexp.MustCompile(`id="new-signing-secret">([^<]+)<`).FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || m == nil || !strings.Contains(body, "Signing secret for") ||
		!strings.Contains(body, "Copy it now: it isn't shown again.") {
		t.Fatalf("POST /settings/notifications = %d, want 200 showing the signing secret once: %s", resp.StatusCode, body)
	}
	secret := html.UnescapeString(m[1])

	var channels []struct {
		ID, Name, Kind, Address string
	}
	c.g.getJSON(t, "/v1/notifications/channels", &channels)
	channelID := ""
	for _, ch := range channels {
		if ch.Name == name {
			channelID = ch.ID
			if ch.Kind != "webhook" || ch.Address != srv.URL {
				t.Errorf("channel = %+v, want a webhook at %s", ch, srv.URL)
			}
		}
	}
	if channelID == "" {
		t.Fatalf("GET /v1/notifications/channels = %+v, want the channel %q", channels, name)
	}
	removeChannel := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/notifications/channels/"+channelID, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	t.Cleanup(removeChannel)
	if raw, err := c.g.apiRequest(http.MethodGet, "/v1/notifications/channels", "", ""); err == nil {
		list, _ := io.ReadAll(raw.Body)
		raw.Body.Close()
		if strings.Contains(string(list), secret) {
			t.Errorf("the API's channel list shows the signing secret")
		}
	}
	if _, page := c.get(t, "/settings"); strings.Contains(page, secret) || !strings.Contains(page, name) {
		t.Errorf("Settings after adding the channel should list %q and never show the signing secret again", name)
	}

	// Send a test: the page says it was accepted, and the receiver got a message
	// whose signature verifies with the secret from the page.
	resp, body = c.post(t, "/settings/notifications/"+channelID+"/test", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST send a test = %d, want 303: %s", resp.StatusCode, body)
	}
	if _, page := c.get(t, resp.Header.Get("Location")); !strings.Contains(page, name+" accepted the test message.") {
		t.Errorf("Settings after the test doesn't say %q accepted it:\n%s", name, page)
	}
	isType := func(typ string) func(notifyMessage) bool {
		return func(msg notifyMessage) bool { return msg.Type == typ }
	}
	waitFor(t, "the test message to reach the receiver", 10*time.Second, func() bool { return rx.count(isType("test")) > 0 })
	testReq, _ := rx.first(isType("test"))
	if err := notify.Verify(secret, testReq.Header.Get(notify.SignatureHeader), testReq.Body, time.Now(), 5*time.Minute); err != nil {
		t.Errorf("the test message's signature %q doesn't verify: %v", testReq.Header.Get(notify.SignatureHeader), err)
	}
	if testReq.Header.Get("Caravana-Event") != "test" || testReq.Header.Get("Caravana-Delivery") != testReq.Message.ID || testReq.Message.ID == "" {
		t.Errorf("the test message's headers = %v, want Caravana-Event test and a Caravana-Delivery equal to its id %q", testReq.Header, testReq.Message.ID)
	}

	// A deploy that fails: one deploy.failed, then one problem.opened.
	definition := func(image string) string {
		return fmt.Sprintf(`{"source": {"kind": "image"}, "services": [{"name": "web", "image": %q, %s}]}`, image, options)
	}
	resp = c.g.api(t, http.MethodPost, "/v1/apps/"+id+"/deployments", "application/json", definition("busybox:does-not-exist"))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		t.Fatalf("deploy of a missing tag = 202, want it refused: %s", raw)
	}
	var newest struct {
		Deployments []gateDeployment `json:"deployments"`
	}
	c.g.getJSON(t, "/v1/apps/"+id+"/deployments?limit=1", &newest)
	if len(newest.Deployments) != 1 || newest.Deployments[0].Status != "failed" {
		t.Fatalf("%s's newest deployment = %+v, want one that failed", id, newest.Deployments)
	}
	failed := newest.Deployments[0].ID

	ofApp := func(typ, kind string) func(notifyMessage) bool {
		return func(msg notifyMessage) bool {
			return msg.App == id && msg.Type == typ && (kind == "" || msg.Kind == kind)
		}
	}
	waitFor(t, "deploy.failed to reach the receiver", 15*time.Second, func() bool { return rx.count(ofApp("deploy.failed", "")) > 0 })
	if got, _ := rx.first(ofApp("deploy.failed", "")); got.Message.Deployment != failed || got.Message.Outcome != "failed" || got.Message.Trigger != "manual" {
		t.Errorf("deploy.failed = %+v, want deployment %s, outcome failed, trigger manual", got.Message, failed)
	}
	// The notifier looks every 30 s, and its first look comes one gap after
	// Caravana started (long ago by now), so one more gap and a sender turn is the most it takes.
	waitFor(t, "problem.opened for the failed deploy", 2*oneGap+15*time.Second, func() bool {
		return rx.count(ofApp("problem.opened", "deploy_failed")) > 0
	})

	// Fix it. The working deploy sends deploy.succeeded; the item leaves the
	// list, and after the resolve delay problem.resolved follows.
	resp = c.g.api(t, http.MethodPost, "/v1/apps/"+id+"/deployments", "application/json", definition("busybox:1.37"))
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var fixed gateDeployment
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &fixed) != nil || fixed.ID == "" {
		t.Fatalf("deploy the fixed app = %d, want 202 with a deployment: %s", resp.StatusCode, raw)
	}
	if final := c.g.waitFinalOf(t, id, fixed.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deployment %s ended %s: %s", fixed.ID, final.Status, final.Error)
	}
	waitFor(t, "deploy.succeeded to reach the receiver", 15*time.Second, func() bool { return rx.count(ofApp("deploy.succeeded", "")) > 0 })
	if got, _ := rx.first(ofApp("deploy.succeeded", "")); got.Message.Deployment != fixed.ID || got.Message.Outcome != "healthy" {
		t.Errorf("deploy.succeeded = %+v, want deployment %s, outcome healthy", got.Message, fixed.ID)
	}
	waitFor(t, "problem.resolved for the failed deploy", 2*time.Minute, func() bool {
		return rx.count(ofApp("problem.resolved", "deploy_failed")) > 0
	})

	// One more look of the notifier: nothing is sent a second time.
	time.Sleep(oneGap + 5*time.Second)
	for _, want := range []struct {
		typ, kind string
	}{{"deploy.failed", ""}, {"problem.opened", "deploy_failed"}, {"deploy.succeeded", ""}, {"problem.resolved", "deploy_failed"}} {
		if n := rx.count(ofApp(want.typ, want.kind)); n != 1 {
			t.Errorf("%s (%s) for %s was sent %d times, want exactly once", want.typ, want.kind, id, n)
		}
	}
	// Every message is signed and named the way the contract says, and no id repeats.
	ids := map[string]bool{}
	for _, r := range rx.all() {
		if err := notify.Verify(secret, r.Header.Get(notify.SignatureHeader), r.Body, r.At, 5*time.Minute); err != nil {
			t.Errorf("the %s message's signature doesn't verify: %v", r.Message.Type, err)
		}
		if r.Header.Get("Caravana-Event") != r.Message.Type || r.Header.Get("Caravana-Delivery") != r.Message.ID {
			t.Errorf("the %s message's headers = %v, want Caravana-Event and Caravana-Delivery to match its body", r.Message.Type, r.Header)
		}
		if ids[r.Message.ID] {
			t.Errorf("delivery %s reached the receiver twice", r.Message.ID)
		}
		ids[r.Message.ID] = true
	}

	// Remove the channel through the console: its row and its secret variable go.
	resp, body = c.post(t, "/settings/notifications/"+channelID+"/remove", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST remove the channel = %d, want 303: %s", resp.StatusCode, body)
	}
	c.g.getJSON(t, "/v1/notifications/channels", &channels)
	for _, ch := range channels {
		if ch.ID == channelID {
			t.Errorf("channel %s is still listed after it was removed", channelID)
		}
	}
	if _, _, err := c.g.target.Nomad.Variables().Read("caravana/notifications/"+channelID, nil); err == nil {
		t.Errorf("Nomad variable caravana/notifications/%s is still there after the channel was removed", channelID)
	}

	// And the app, through the API (the console's delete is the next step's).
	resp = c.g.api(t, http.MethodDelete, "/v1/apps/"+id, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE /v1/apps/%s = %d, want 204", id, resp.StatusCode)
	}
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := c.g.target.Nomad.Jobs().Info(id, nil)
		return err != nil && strings.Contains(err.Error(), "404")
	})
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
