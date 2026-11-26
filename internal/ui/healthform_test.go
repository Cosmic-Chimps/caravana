package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// healthAction is the form's action on Services (formsIn drops the anchor of the service's panel).
const healthAction = "/apps/shop/services/web/health"

// healthForm is the health check form of shop's web service as Services draws it, with set over its values.
func (e *consoleEnv) healthForm(t *testing.T, set url.Values) url.Values {
	t.Helper()
	page := e.pageOf(t, "/apps/shop/configure/services")
	form := formWith(t, page, healthAction).Values
	for k, v := range set {
		form[k] = v
	}
	return form
}

// Feature 059: Services of an app that keeps its Compose file has the health check as a form that writes
// the check into the file's overlay, with copies and size as read-only text, and "Save and deploy" beside
// the service's name. Saving deploys on the page's revision, keeps the file's text, and never touches a
// secret.
func TestServicesKeptFileHealthForm(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	page := e.pageOf(t, "/apps/shop/configure/services")
	got := text(page)
	for _, want := range []string{"Set in the Compose file.", "Save and deploy web", "Health check", "Empty checks the TCP port.",
		"For this app the health check is written into its Compose settings; the file itself is changed on Source."} {
		if !strings.Contains(got, want) {
			t.Errorf("Services lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(page, `name="svc.web.copies"`) || strings.Contains(page, `name="svc.web.memory"`) {
		t.Error("a kept-file app's copies and size are inputs: they are set in the file")
	}
	// worker has no port, so it has no check and no button.
	if strings.Contains(page, "Save and deploy worker") || strings.Contains(page, `name="svc.worker.healthPath"`) {
		t.Error("a service with no port has a health check form")
	}
	if strings.Contains(page, sentinel015) {
		t.Error("a secret value is on the page")
	}

	before, _ := e.Store.GetApp(context.Background(), "shop")
	n := e.deployCount(t, "shop")
	resp, body := e.postForm(t, "/apps/shop/services/web/health", e.healthForm(t, url.Values{
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
		resp, body := e.postForm(t, "/apps/shop/services/web/health", e.healthForm(t, url.Values{
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
			resp, body := e.postForm(t, "/apps/shop/services/web/health", e.healthForm(t, tc.set))
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
		_, body := e.postForm(t, "/apps/shop/services/web/health", e.healthForm(t, url.Values{"svc.web.healthPath": {"ready"}}))
		if !strings.Contains(body, `value="ready"`) {
			t.Error("the refused form lost what was typed")
		}
	})

	t.Run("a stale page and the wrong service", func(t *testing.T) {
		stale := e.healthForm(t, nil)
		stale.Set("revision", "0000000000000000")
		resp, body := e.postForm(t, "/apps/shop/services/web/health", stale)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app changed since you opened the form.") {
			t.Errorf("a stale revision = %d, want 409 with the stale panel:\n%s", resp.StatusCode, text(body))
		}
		if resp, _ := e.postForm(t, "/apps/shop/services/nope/health", e.healthForm(t, nil)); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("an unknown service = %d, want 422", resp.StatusCode)
		}
		if resp, _ := e.postForm(t, "/apps/shop/services/worker/health", e.healthForm(t, nil)); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("a service with no port = %d, want 422", resp.StatusCode)
		}
		if resp, _ := e.postForm(t, "/apps/nope/services/web/health", url.Values{"revision": {"x"}}); resp.StatusCode != http.StatusNotFound {
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
	resp, body := e.postForm(t, "/apps/duo/services/web/health", url.Values{"revision": {e.revisionOf(t, "duo")},
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
