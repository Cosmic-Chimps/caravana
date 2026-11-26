package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// keptServiceAction is the form's action on Services (formsIn drops the anchor of the service's panel).
const keptServiceAction = "/apps/shop/services/web/settings"

// healthForm is the health check form of shop's web service as Services draws it, with set over its values.
func (e *consoleEnv) healthForm(t *testing.T, set url.Values) url.Values {
	t.Helper()
	page := e.pageOf(t, "/apps/shop/configure/services")
	form := formWith(t, page, keptServiceAction).Values
	for k, v := range set {
		form[k] = v
	}
	return form
}

// Feature 059: Services of an app that keeps its Compose file has the health check as a form that writes
// the check into the file's overlay, and "Save and deploy" beside the service's name. Saving deploys on
// the page's revision, keeps the file's text, and never touches a secret.
func TestServicesKeptFileHealthForm(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	page := e.pageOf(t, "/apps/shop/configure/services")
	got := text(page)
	for _, want := range []string{"Save and deploy web", "Health check", "Empty checks the TCP port.",
		"For this app, copies and memory are written into its Compose file and the health check into its Compose settings. The rest of the file is changed on Source."} {
		if !strings.Contains(got, want) {
			t.Errorf("Services lacks %q:\n%s", want, got)
		}
	}
	// worker has no port, so it has no check; its size is still a form.
	if strings.Contains(page, `name="svc.worker.healthPath"`) || !strings.Contains(page, "Save and deploy worker") {
		t.Error("a service with no port has a health form, or no size form")
	}
	if strings.Contains(page, sentinel015) {
		t.Error("a secret value is on the page")
	}

	before, _ := e.Store.GetApp(context.Background(), "shop")
	n := e.deployCount(t, "shop")
	resp, body := e.postForm(t, "/apps/shop/services/web/settings", e.healthForm(t, url.Values{
		"svc.web.healthPath": {"/ready"}, "svc.web.healthInterval": {"5"}, "svc.web.healthTimeout": {"1.5"}}))
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/shop/deployments#deployment-") {
		t.Fatalf("save = %d to %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), text(body))
	}
	if e.deployCount(t, "shop") != n+1 {
		t.Error("saving the health check didn't deploy")
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)
	after, _ := e.Store.GetApp(context.Background(), "shop")
	web := after.Compose.Services["web"]
	if after.Compose.Compose != before.Compose.Compose || web.Health == nil || web.Health.Path != "/ready" ||
		web.Health.Interval != 5*time.Second || web.Health.Timeout != 1500*time.Millisecond {
		t.Errorf("kept file / overlay health = %q / %+v, want the same text and /ready every 5 s", after.Compose.Compose, web.Health)
	}
	if len(web.Domains) != 1 || len(web.Secrets) != 1 || web.Secrets[0].Value != nil || after.Compose.Services["db"].Backup == nil {
		t.Errorf("the overlay lost what the form doesn't offer: %+v", after.Compose.Services)
	}
	if v := e.Nomad.Variable("nomad/jobs/shop/web")["API_KEY"]; v != sentinel015 {
		t.Errorf("API_KEY = %q, want the stored value", v)
	}

	t.Run("empty is the TCP check", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/shop/services/web/settings", e.healthForm(t, url.Values{
			"svc.web.healthPath": {""}, "svc.web.healthInterval": {""}, "svc.web.healthTimeout": {""}}))
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("save = %d:\n%s", resp.StatusCode, text(body))
		}
		e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)
		got, _ := e.Store.GetApp(context.Background(), "shop")
		if h := got.Compose.Services["web"].Health; h != nil && h.Path != "" {
			t.Errorf("overlay health = %+v, want no path", h)
		}
	})

	t.Run("refusals draw Services with the message at its input", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			set    url.Values
			status int
			id     string
		}{
			{"a path with no slash", url.Values{"svc.web.healthPath": {"ready"}}, http.StatusUnprocessableEntity, "svc-web-healthPath-error"},
			{"seconds that aren't a number", url.Values{"svc.web.healthInterval": {"often"}}, http.StatusUnprocessableEntity, "svc-web-healthInterval-error"},
		} {
			n := e.deployCount(t, "shop")
			resp, body := e.postForm(t, "/apps/shop/services/web/settings", e.healthForm(t, tc.set))
			if resp.StatusCode != tc.status || !strings.Contains(body, `id="`+tc.id+`"`) {
				t.Errorf("%s = %d, want the page with %q:\n%s", tc.name, resp.StatusCode, tc.id, text(body))
			}
			if !strings.Contains(text(body), "Save and deploy web") {
				t.Errorf("%s: the answer isn't the Services page", tc.name)
			}
			if e.deployCount(t, "shop") != n {
				t.Errorf("%s deployed", tc.name)
			}
		}
		// What was typed is drawn again.
		_, body := e.postForm(t, "/apps/shop/services/web/settings", e.healthForm(t, url.Values{"svc.web.healthPath": {"ready"}}))
		if !strings.Contains(body, `value="ready"`) {
			t.Error("the refused form lost what was typed")
		}
	})

	t.Run("a stale page and the wrong service", func(t *testing.T) {
		stale := e.healthForm(t, nil)
		stale.Set("revision", "0000000000000000")
		resp, body := e.postForm(t, "/apps/shop/services/web/settings", stale)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app changed since you opened the form.") {
			t.Errorf("a stale revision = %d, want 409 with the stale panel:\n%s", resp.StatusCode, text(body))
		}
		if resp, _ := e.postForm(t, "/apps/shop/services/nope/settings", e.healthForm(t, nil)); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("an unknown service = %d, want 422", resp.StatusCode)
		}
		if resp, _ := e.postForm(t, "/apps/shop/services/worker/settings", e.healthForm(t, nil)); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("a service with no port = %d, want 422", resp.StatusCode)
		}
		if resp, _ := e.postForm(t, "/apps/nope/services/web/settings", url.Values{"revision": {"x"}}); resp.StatusCode != http.StatusNotFound {
			t.Errorf("an unknown app = %d, want 404", resp.StatusCode)
		}
	})
}

// An app that isn't edited through a file posts the same form to the same route: the check is set on the
// service, as the Configure form does.
func TestHealthRouteOfADefinitionApp(t *testing.T) {
	e := newConsoleEnv(t)
	e.deploy(t, duoApp())
	n := e.deployCount(t, "duo")
	resp, body := e.postForm(t, "/apps/duo/services/web/settings", url.Values{"revision": {e.revisionOf(t, "duo")},
		"svc.web.healthPath": {"/up"}, "svc.web.healthInterval": {"7"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save = %d:\n%s", resp.StatusCode, text(body))
	}
	if e.deployCount(t, "duo") != n+1 {
		t.Error("saving the health check didn't deploy")
	}
	if h := e.storedApp(t, "duo").Services[0].Health; h == nil || h.Path != "/up" || h.Interval != 7*time.Second {
		t.Errorf("health = %+v, want /up every 7 s", h)
	}

}

// Task gqbf: Services of an app that keeps its Compose file has Copies, Memory and Ceiling as inputs, and
// saving writes them into the file's text, keeping its comments and layout, in one deploy with the health check.
func TestServicesKeptFileSize(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	page := e.pageOf(t, "/apps/shop/configure/services")
	for _, want := range []string{`name="svc.web.copies"`, `name="svc.web.memory"`, `name="svc.web.memoryMax"`, `name="svc.worker.memory"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Services lacks the input %s", want)
		}
	}
	// db has a volume: one copy, so no Copies input; CPU is the file's.
	if strings.Contains(page, `name="svc.db.copies"`) || strings.Contains(page, `name="svc.web.cpu"`) {
		t.Error("Services offers copies for a service with a volume, or CPU for a kept file")
	}

	n := e.deployCount(t, "shop")
	resp, body := e.postForm(t, keptServiceAction, e.healthForm(t, url.Values{
		"svc.web.copies": {"2"}, "svc.web.memory": {"300"}, "svc.web.memoryMax": {"600"}, "svc.web.healthPath": {"/ready"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save = %d:\n%s", resp.StatusCode, text(body))
	}
	if e.deployCount(t, "shop") != n+1 {
		t.Error("saving copies, size and health wasn't one deploy")
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)
	after, _ := e.Store.GetApp(context.Background(), "shop")
	if !strings.HasPrefix(after.Compose.Compose, "# shop stack <kept> & commented\nservices:\n  web:\n    mem_limit: 600m\n    mem_reservation: 300m\n    deploy:\n      replicas: 2\n    image: traefik/whoami:latest\n") {
		t.Errorf("kept file =\n%s\nwant the three keys first in web, the comment kept", after.Compose.Compose)
	}
	i, _ := serviceNamed(after.App, "web")
	web := after.App.Services[i]
	if web.Replicas == nil || *web.Replicas != 2 || web.Resources.MemoryMB != 300 || web.Resources.MemoryMaxMB != 600 {
		t.Errorf("web = replicas %v, %+v; want 2, 300 MB up to 600 MB", web.Replicas, web.Resources)
	}
	if h := after.Compose.Services["web"].Health; h == nil || h.Path != "/ready" {
		t.Errorf("overlay health = %+v, want /ready", h)
	}

	t.Run("memory emptied is refused at its input", func(t *testing.T) {
		n := e.deployCount(t, "shop")
		resp, body := e.postForm(t, keptServiceAction, e.healthForm(t, url.Values{"svc.web.memoryMax": {""}}))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="svc-web-memoryMax-error"`) ||
			!strings.Contains(text(body), "Ceiling can't go back to the default from here: that means taking the key out of the file. Change it on Source.") {
			t.Errorf("emptied memory = %d, want the message at Ceiling:\n%s", resp.StatusCode, text(body))
		}
		if e.deployCount(t, "shop") != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("a reservation over the ceiling is refused at its input", func(t *testing.T) {
		resp, body := e.postForm(t, keptServiceAction, e.healthForm(t, url.Values{"svc.web.memory": {"900"}}))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="svc-web-memory-error"`) {
			t.Errorf("reservation over ceiling = %d, want the message at Memory:\n%s", resp.StatusCode, text(body))
		}
		if got, _ := e.Store.GetApp(context.Background(), "shop"); got.Compose.Compose != after.Compose.Compose {
			t.Error("a refused change rewrote the file")
		}
	})
}

// A kept file whose size can't be written without guessing (here an alias) keeps copies and size as
// read-only text, saying why; the health check is still a form.
func TestServicesKeptFileSizeLocked(t *testing.T) {
	req := shopRequest()
	req.Compose = "x-mem: &mem 512m\n" + strings.Replace(shopCompose, "    expose: [\"80\"]\n", "    expose: [\"80\"]\n    mem_limit: *mem\n", 1)
	e, _ := deployShop(t, req)
	page := e.pageOf(t, "/apps/shop/configure/services")
	got := text(page)
	if strings.Contains(page, `name="svc.web.memory"`) || !strings.Contains(page, `at <span class="font-mono">services.web.mem_limit</span>, the value is an alias`) || !strings.Contains(got, "Set in the Compose file. It can't be written from here: at services.web.mem_limit, the value is an alias. Change it on Source.") {
		t.Errorf("web's size should be read-only, saying why:\n%s", got)
	}
	if !strings.Contains(page, `name="svc.web.healthPath"`) || !strings.Contains(page, `name="svc.worker.memory"`) {
		t.Error("the health check of web, or the size of worker, isn't a form")
	}
}
