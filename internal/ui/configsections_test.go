package ui

import (
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Feature 059: Configure is five pages, each with its nav, its own form and its own submit.

// cfgNavItem is the nav entry with the given label as the page draws it: its markup, so a test can read
// whether it is current and what it counts.
func cfgNavItem(t *testing.T, body, key string) string {
	t.Helper()
	m := regexp.MustCompile(`<a href="[^"]*" class="cv-snav-item" id="cfg-nav-` + key + `"[^>]*>.*?</a>`).FindString(body)
	if m == "" {
		t.Fatalf("no nav entry %q in:\n%s", key, body)
	}
	return m
}

func TestConfigureSectionsDrawWithTheirNav(t *testing.T) {
	e := configureEnv(t)
	for _, tc := range []struct{ path, key string }{
		{"/apps/shop/configure", "overview"},
		{"/apps/shop/configure/source", "source"},
		{"/apps/shop/configure/services", "services"},
		{"/apps/shop/variables", "variables"},
		{"/apps/shop/domains", "domains"},
		{"/apps/shop/configure/integrations", "integrations"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			resp, body := e.get(t, tc.path)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d:\n%s", tc.path, resp.StatusCode, text(body))
			}
			if !strings.Contains(body, `<nav aria-label="Configure" class="cv-snav cv-snav-inpage">`) || !strings.Contains(body, `class="cv-cfg"`) {
				t.Errorf("%s has no section nav in its frame", tc.path)
			}
			// The Configure tab stays current, and exactly one nav entry is.
			if !strings.Contains(body, `id="tab-configure" aria-current="page"`) {
				t.Errorf("%s doesn't mark the Configure tab", tc.path)
			}
			if got := strings.Count(body, `class="cv-snav-item"`); got != 6 {
				t.Errorf("%d nav entries, want 6", got)
			}
			if !strings.Contains(cfgNavItem(t, body, tc.key), `aria-current="page"`) {
				t.Errorf("%s: the %s entry isn't the current one", tc.path, tc.key)
			}
			if got := len(regexp.MustCompile(`cv-snav-item"[^>]*aria-current="page"`).FindAllString(body, -1)); got != 1 {
				t.Errorf("%d current nav entries, want 1", got)
			}
			// The counts: two services, two variables, one domain.
			if n := cfgNavItem(t, body, "services"); !strings.Contains(n, `cv-snav-count">2<`) {
				t.Errorf("Services count: %s", n)
			}
			if n := cfgNavItem(t, body, "variables"); !strings.Contains(n, `cv-snav-count">2<`) {
				t.Errorf("Variables count: %s", n)
			}
			if n := cfgNavItem(t, body, "domains"); !strings.Contains(n, `cv-snav-count">1<`) {
				t.Errorf("Domains count: %s", n)
			}
			for _, key := range []string{"overview", "source", "integrations"} {
				if strings.Contains(cfgNavItem(t, body, key), "cv-snav-count") {
					t.Errorf("%s has a count", key)
				}
			}
		})
	}
}

func TestConfigureNavLinks(t *testing.T) {
	e := configureEnv(t)
	_, body := e.get(t, "/apps/shop/configure")
	for key, href := range map[string]string{
		"overview": "/apps/shop/configure", "source": "/apps/shop/configure/source", "services": "/apps/shop/configure/services",
		"variables": "/apps/shop/variables", "domains": "/apps/shop/domains", "integrations": "/apps/shop/configure/integrations",
	} {
		if n := cfgNavItem(t, body, key); !strings.Contains(n, `href="`+href+`"`) {
			t.Errorf("%s links to %s, want %s", key, n, href)
		}
	}
}

// sectionBody is the page's section: what follows the nav.
func sectionBody(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<div class="cv-cfg-body">`)
	if i < 0 {
		t.Fatalf("no section body in:\n%s", body)
	}
	return body[i:]
}

func TestOverviewIsReadOnlyAndHasNoDangerEdge(t *testing.T) {
	e := configureEnv(t)
	_, body := e.get(t, "/apps/shop/configure")
	body = sectionBody(t, body)
	// No input of its own but the delete confirmation's name.
	for _, form := range formsIn(body) {
		if form.Action != "/apps/shop/delete" {
			t.Errorf("Overview has a form posting to %s", form.Action)
		}
	}
	if strings.Contains(body, "cv-danger-panel") {
		t.Error("the Delete panel has the danger edge: colour is status only")
	}
	if !strings.Contains(body, `cv-btn cv-btn-danger`) {
		t.Error("the Delete button isn't a danger button")
	}
	// The panels, in the contract's order.
	got := text(body)
	last := -1
	for _, panel := range []string{"Source Image", "Variables & secrets", "Services", "Domains", "Backups", "Schedules", "Integrations", "Delete app"} {
		i := strings.Index(got, panel)
		if i < 0 || i < last {
			t.Fatalf("panel %q is missing or out of order (%d after %d):\n%s", panel, i, last, got)
		}
		last = i
	}
	for _, want := range []string{
		"Each service runs the image it names.", "Change the images", "2 variables", "Values of secrets are never shown.",
		"shop.example.com", "No backups.", "No webhook token.", "Open integrations",
		"web ghcr.io/example/shop:2.1 2 copies · 256 MB, up to 1 GB GET /healthz",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Overview lacks %q:\n%s", want, got)
		}
	}
	// The definition's old read-only panels are gone.
	if strings.Contains(got, "Definition") {
		t.Error("Overview still has the Definition panels")
	}
}

func TestSourceAndServicesHoldTheirOwnForms(t *testing.T) {
	e := configureEnv(t)

	_, source := e.get(t, "/apps/shop/configure/source")
	for _, name := range []string{"svc.web.image", "svc.web.port", "svc.worker.image", "svc.web.form", "svc.worker.form", "section"} {
		if !strings.Contains(source, `name="`+name+`"`) {
			t.Errorf("Source lacks the input %s", name)
		}
	}
	for _, name := range []string{"copies", "cpu", "memory", "memoryMax", "healthPath", "healthInterval", "healthTimeout"} {
		if strings.Contains(source, `name="svc.web.`+name+`"`) {
			t.Errorf("Source has the Services input %s", name)
		}
	}
	for _, f := range formsIn(source) {
		if f.Action == "/apps/shop/configure" && f.Values.Get("section") != "source" {
			t.Errorf("a Source form posts section %q", f.Values.Get("section"))
		}
	}
	if !strings.Contains(text(source), "Save and deploy web") || !strings.Contains(text(source), "worker is left as it is.") {
		t.Errorf("Source's foot: %s", text(source))
	}
	if !strings.Contains(source, `/apps/shop/compose`) {
		t.Error("Source lacks Deploy from Compose")
	}

	_, services := e.get(t, "/apps/shop/configure/services")
	for _, name := range []string{"svc.web.copies", "svc.web.cpu", "svc.web.memory", "svc.web.memoryMax", "svc.web.healthPath", "svc.web.healthInterval", "svc.web.healthTimeout", "svc.web.form"} {
		if !strings.Contains(services, `name="`+name+`"`) {
			t.Errorf("Services lacks the input %s", name)
		}
	}
	for _, name := range []string{"image", "port", "domainPort"} {
		if strings.Contains(services, `name="svc.web.`+name+`"`) {
			t.Errorf("Services has the Source input %s", name)
		}
	}
	// The foot's button sits outside the form and names it: forms can't nest with placement and scaling.
	if !strings.Contains(services, `type="submit" form="svcform-web"`) {
		t.Error("Services' submit doesn't name its form")
	}
	for _, want := range []string{"Placement", "Scaling", "Save and deploy web", "Copies & size", "Health check"} {
		if !strings.Contains(text(services), want) {
			t.Errorf("Services lacks %q", want)
		}
	}
}

// Each section's form posts to the one route, and a post draws back the section that holds its form.
func TestConfigureRefusalsDrawTheirSection(t *testing.T) {
	e := configureEnv(t)

	// A Services post with a bad number answers Services, with the message at the input.
	f := formWith(t, e.pageOf(t, "/apps/shop/configure/services"), "/apps/shop/configure")
	resp, body := e.submit(t, f, url.Values{"svc.web.memory": {"lots"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
	}
	if !strings.Contains(body, `id="cfg-nav-services" aria-current="page"`) || !strings.Contains(text(body), "Enter a whole number") {
		t.Errorf("a refused Services post doesn't draw Services with the message:\n%s", text(body))
	}

	// A Source post with an empty image answers Source.
	f = formWith(t, e.pageOf(t, "/apps/shop/configure/source"), "/apps/shop/configure")
	resp, body = e.submit(t, f, url.Values{"svc.web.image": {""}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
	}
	if !strings.Contains(body, `id="cfg-nav-source" aria-current="page"`) || !strings.Contains(text(body), "Enter an image.") {
		t.Errorf("a refused Source post doesn't draw Source with the message:\n%s", text(body))
	}

	// An error on an input the section doesn't draw is said at the top, never lost: a post made from
	// Services that carries a bad image names the service.
	f = formWith(t, e.pageOf(t, "/apps/shop/configure/services"), "/apps/shop/configure")
	resp, body = e.submit(t, f, url.Values{"svc.web.image": {""}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="cfg-nav-services" aria-current="page"`) || !strings.Contains(text(body), "web: Enter an image.") {
		t.Errorf("an error the section has no input for isn't said in its notice (%d):\n%s", resp.StatusCode, text(body))
	}
}

func TestIntegrationsMakeAndShowTokensOnce(t *testing.T) {
	e := configureEnv(t)
	resp, body := e.postForm(t, "/apps/shop/webhook", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="cfg-nav-integrations" aria-current="page"`) {
		t.Fatalf("POST webhook = %d, want the Integrations page:\n%s", resp.StatusCode, text(body))
	}
	m := webhookURLPattern.FindStringSubmatch(body)
	if m == nil || strings.Count(body, m[1]) != 1 {
		t.Fatalf("the webhook URL isn't shown once:\n%s", text(body))
	}
	if !strings.Contains(body, `curl -X POST `+e.URL+`/v1/webhooks/shop -H &#34;Authorization: Bearer $CARAVANA_WEBHOOK_TOKEN&#34;`) {
		t.Errorf("no curl block with the call:\n%s", text(body))
	}
	_, body = e.get(t, "/apps/shop/configure/integrations")
	if strings.Contains(body, m[1]) {
		t.Error("the token is on the page again after a reload")
	}
	// Revoking goes back to Integrations.
	resp, _ = e.postForm(t, "/apps/shop/webhook/revoke", nil)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop/configure/integrations" {
		t.Errorf("revoke = %d to %q, want 303 to Integrations", resp.StatusCode, location(t, resp))
	}
	// The deploy token is shown on the same page.
	resp, body = e.postForm(t, "/apps/shop/deploy-token", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="deploy-token-value"`) || !strings.Contains(body, `id="cfg-nav-integrations" aria-current="page"`) {
		t.Errorf("deploy token = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestOverviewOfAnAppWithNoDefinition(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, app.App{ID: "blank", Name: "blank", Source: app.Source{Kind: app.SourceImage}})
	_, body := e.get(t, "/apps/blank/configure")
	got := text(sectionBody(t, body))
	for _, want := range []string{"Not defined yet.", "Define the app", "Integrations", "Delete app"} {
		if !strings.Contains(got, want) {
			t.Errorf("Overview of an undefined app lacks %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"Variables & secrets 0", "Backups", "Schedules", "Open the Backups tab"} {
		if strings.Contains(got, not+" ") && not != "Variables & secrets 0" {
			t.Errorf("Overview of an undefined app draws %q", not)
		}
	}
	_, source := e.get(t, "/apps/blank/configure/source")
	if !strings.Contains(source, `action="/apps/blank/define"`) {
		t.Errorf("Source of an undefined app lacks the define form:\n%s", text(source))
	}
}

// ---- D2: each input is read only when the post has it -----------------------------------------

func TestServicesPostKeepsWhatItDoesNotCarry(t *testing.T) {
	stored := richApp()
	// A Services form: the marker and the copies, size and health fields; no image, no ports.
	post := url.Values{"revision": {"r"}, "svc.db.form": {"1"}, "svc.db.memory": {"384"}, "svc.db.healthPath": {"/ready"}}
	f, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	got := applyEdit(stored, f)
	want := richApp()
	want.Services[1].Resources.MemoryMB = 384
	want.Services[1].Health = &app.HealthCheck{Path: "/ready"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a Services post changed more than it carried\n got: %+v\nwant: %+v", got.Services[1], want.Services[1])
	}
	if got.Services[1].Image != "postgres:17" || got.Services[1].Ports[0].ContainerPort != 5432 {
		t.Errorf("a Services post emptied the image or the port: %+v", got.Services[1])
	}
}

func TestSourcePostKeepsCopiesSizeAndHealth(t *testing.T) {
	stored := richApp()
	post := url.Values{"revision": {"r"}, "svc.web.form": {"1"}, "svc.web.image": {"ghcr.io/acme/shop:2"}, "svc.web.domainPort": {"metrics"}}
	f, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	got := applyEdit(stored, f)
	want := richApp()
	want.Services[0].Image = "ghcr.io/acme/shop:2"
	want.Services[0].Domains[0].Port, want.Services[0].Domains[1].Port = "metrics", "metrics"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a Source post changed more than it carried\n got: %+v\nwant: %+v", got.Services[0], want.Services[0])
	}
	if got.Services[0].Health == nil || got.Services[0].Health.Interval != 15*time.Second || got.Services[0].Resources.MemoryMaxMB != 1024 {
		t.Errorf("a Source post reset the health check or the size: %+v", got.Services[0])
	}
}

func TestAnOldConfigureFormStillWorks(t *testing.T) {
	// A form drawn before the split: every input, no marker, no section.
	stored := richApp()
	post := postOf(formOf(stored, "r"))
	post.Set("svc.web.image", "ghcr.io/acme/shop:3")
	post.Set("svc.worker.copies", "2")
	post.Set("svc.web.memory", "300")
	post.Set("svc.web.healthTimeout", "4")
	f, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	edited := applyEdit(stored, f)
	got := edited.Services[0]
	if got.Image != "ghcr.io/acme/shop:3" || got.Resources.MemoryMB != 300 || got.Health.Timeout != 4*time.Second {
		t.Errorf("an old form's post wasn't applied: %+v", got)
	}
	if w := edited.Services[2]; w.Replicas == nil || *w.Replicas != 2 {
		t.Errorf("worker copies = %v, want 2", w.Replicas)
	}
	if sectionOfPost(post, f) != sectionServices {
		t.Error("a post with no section and no error should draw Services")
	}
}

func TestOneCopyServiceKeepsItsReplicas(t *testing.T) {
	stored := richApp() // web has a volume: one copy; its stored replicas are 1
	f, ok := parseEditForm(url.Values{"revision": {"r"}, "svc.web.form": {"1"}, "svc.web.copies": {"5"}}, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	if got := applyEdit(stored, f).Services[0].Replicas; got == nil || *got != 1 {
		t.Errorf("replicas of a volume service = %v, want the stored 1", got)
	}
}

func TestAnAbsentServiceMarkerAndImageIsLeftAlone(t *testing.T) {
	stored := richApp()
	f, ok := parseEditForm(url.Values{"revision": {"r"}, "svc.web.form": {"1"}, "svc.web.memory": {"300"}}, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	got := applyEdit(stored, f)
	for _, name := range []int{1, 2} {
		if !reflect.DeepEqual(got.Services[name], stored.Services[name]) {
			t.Errorf("service %d changed though the post named only web", name)
		}
	}
}

// The Source form deploys the new image and leaves copies, size and health as they were; the Services
// form does the opposite, and each is a plain post that redirects to the deployment.
func TestSourceAndServicesFormsEachChangeOnlyTheirOwn(t *testing.T) {
	e := editEnv(t)
	e.Reg.AddIndex("traefik/whoami", "v1.10", otherDigest)
	before := e.storedApp(t, "duo")

	resp, body := e.submit(t, e.sourceForm(t, "duo"), url.Values{"svc.web.image": {"traefik/whoami:v1.10"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Source POST = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "duo")[0].Deployment.ID)
	after := e.storedApp(t, "duo")
	if !reflect.DeepEqual(after.Services[0].Resources, before.Services[0].Resources) || !reflect.DeepEqual(after.Services[0].Health, before.Services[0].Health) ||
		!reflect.DeepEqual(after.Services[0].Replicas, before.Services[0].Replicas) || !strings.HasPrefix(after.Services[0].Image, "traefik/whoami") {
		t.Errorf("a Source post changed more than the image: %+v", after.Services[0])
	}

	resp, body = e.submit(t, e.configureForm(t, "duo"), url.Values{"svc.web.healthPath": {"/ready"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Services POST = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "duo")[0].Deployment.ID)
	last := e.storedApp(t, "duo").Services[0]
	if last.Health == nil || last.Health.Path != "/ready" || last.Image != after.Services[0].Image {
		t.Errorf("a Services post changed the image or missed the health path: %+v", last)
	}
}
