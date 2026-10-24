package ui

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 021, page 2: Configure's grid, and the Variables, Domains and Backups pages, for
// every shape of app the contract names, and each form of them.

// duetApp is two services that both have a port and a domain, so the Domains form asks which
// one a new domain is for, and both have variables and a secret.
func duetApp() app.App {
	return app.App{
		ID: "duet", Name: "Duet", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{Name: "web", Image: "traefik/whoami:latest", Ports: []app.Port{{Name: "http", ContainerPort: 80}},
				Domains: []app.Domain{{Host: "web.duet.example.com", Port: "http"}}, Health: &app.HealthCheck{Path: "/"},
				Env: []app.EnvVar{{Name: "MODE", Value: "plain"}}},
			{Name: "api", Image: "traefik/whoami:latest", Ports: []app.Port{{Name: "http", ContainerPort: 8080}},
				Domains: []app.Domain{{Host: "api.duet.example.com", Port: "http"}}, Health: &app.HealthCheck{Path: "/"},
				Env:     []app.EnvVar{{Name: "LOG_LEVEL", Value: "info"}},
				Secrets: []app.Secret{{Name: "TOKEN", Value: new(secretMarker)}}},
		},
	}
}

// duetEnv has duet deployed through the service and healthy.
func duetEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.deploy(t, duetApp())
	return e
}

// revisionOf is the stored revision of the app.
func (e *consoleEnv) revisionOf(t *testing.T, id string) string {
	t.Helper()
	rec, err := e.Store.GetApp(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Revision
}

// deployCount is how many deployments the app has, for "nothing deployed".
func (e *consoleEnv) deployCount(t *testing.T, id string) int { return len(e.deployments(t, id)) }

// pageOf fetches a page that must be 200.
func (e *consoleEnv) pageOf(t *testing.T, path string) string {
	t.Helper()
	resp, body := e.get(t, path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d:\n%s", path, resp.StatusCode, text(body))
	}
	return body
}

// The four pages render for each shape of app, with the sentences the contract has, and the
// Configure tab is the current one on all of them.
func TestPage2Shapes(t *testing.T) {
	configureCurrent := `<a href="/apps/%s/configure" class="cv-tab" id="tab-configure" aria-current="page">Configure</a>`
	check := func(t *testing.T, e *consoleEnv, id, path string, want, gone []string) string {
		t.Helper()
		body := e.pageOf(t, "/apps/"+id+path)
		got := text(body)
		for _, w := range want {
			if !strings.Contains(got, w) && !strings.Contains(body, w) {
				t.Errorf("%s: lacks %q:\n%s", path, w, got)
			}
		}
		for _, g := range gone {
			if strings.Contains(got, g) || strings.Contains(body, g) {
				t.Errorf("%s: has %q:\n%s", path, g, got)
			}
		}
		if path != "/backups" && !strings.Contains(body, strings.ReplaceAll(configureCurrent, "%s", id)) {
			t.Errorf("%s: the Configure tab isn't the current one", path)
		}
		if strings.Contains(body, "hx-post") {
			t.Errorf("%s: a form needs script", path)
		}
		return body
	}

	t.Run("one service", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
		check(t, e, "whoami", "/variables", []string{"Variables & secrets", "One row per name. A change deploys the app.", "Edit as .env", "Add variable",
			"Add or change", "Keep as a secret (the value is never shown again)", "Set and deploy", "Secret values live in Nomad Variables"},
			[]string{"Used by", `id="var-service"`})
		check(t, e, "whoami", "/domains", []string{"Domains", "Every address people use to reach this app. HTTPS is set up for each.", "1 domain", "whoami.example.com",
			"whoami :80 · production", `aria-label="Open https://whoami.example.com"`, `target="_blank" rel="noopener"`, "Add a domain", "Point it at caravana", "Add domain and deploy", "All domains", "Sends traffic to whoami :80, set in Configure",
			"The root of a domain needs an A record. A subdomain can use one too."},
			[]string{`id="domain-service"`})
		check(t, e, "whoami", "/configure", []string{"Copies & size", "Deploy"}, []string{`id="svc-web-title"`})
	})

	t.Run("two services", func(t *testing.T) {
		e := duetEnv(t)
		vars := check(t, e, "duet", "/variables", []string{"Used by", `id="var-service"`, "Variables as .env · api", "Variables as .env · web", "TOKEN"}, nil)
		// Secrets first, then the variables, each by name.
		if at := func(s string) int { return strings.Index(vars, `<span class="cv-vname-text">`+s+"</span>") }; !(at("TOKEN") < at("LOG_LEVEL") && at("LOG_LEVEL") < at("MODE")) {
			t.Errorf("rows aren't secrets first, then variables by name: TOKEN %d, LOG_LEVEL %d, MODE %d", at("TOKEN"), at("LOG_LEVEL"), at("MODE"))
		}
		check(t, e, "duet", "/domains", []string{"2 domains", "Sends traffic to", "web · :80", "api · :8080", "api.duet.example.com", "web.duet.example.com"}, nil)
		check(t, e, "duet", "/configure", []string{`id="svc-web-title"`, `id="svc-api-title"`, "api.duet.example.com → api :8080"}, nil)
	})

	t.Run("a kept Compose file", func(t *testing.T) {
		e, _ := keptShopEnv(t)
		vars := check(t, e, "shop", "/variables", []string{"This app is edited through its Compose file.", `href="/apps/shop/configure"`, "Add secret", "The value is set as a secret and never shown again."},
			[]string{"Edit as .env", `id="env-editor"`, "Keep as a secret", "Add variable"})
		if !strings.Contains(vars, `<input type="hidden" name="secret" value="1">`) {
			t.Error("the kept-file Add form doesn't set a secret")
		}
		check(t, e, "shop", "/domains", []string{"This app is edited through its Compose file.", "shop.example.com"}, []string{"Add a domain", "Remove", "Add domain and deploy"})
		check(t, e, "shop", "/backups", []string{"1 backed up · 0 failing · 0 not"}, nil)
	})

	t.Run("no definition yet", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, app.App{ID: "empty", Source: app.Source{Kind: app.SourceImage}})
		check(t, e, "empty", "/variables", []string{"Define the app first.", `href="/apps/empty/configure"`}, []string{"Add or change", "Set and deploy"})
		check(t, e, "empty", "/domains", []string{"Define the app first.", `href="/apps/empty/configure"`}, []string{"Add a domain", "Add domain and deploy"})
		check(t, e, "empty", "/backups", []string{"Nothing to back up."}, []string{"Back up daily"})
	})

	t.Run("stopped", func(t *testing.T) {
		e := duetEnv(t)
		if resp, body := e.postAction(t, "/apps/duet/stop"); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("stop = %d:\n%s", resp.StatusCode, text(body))
		}
		const sentence = "The app is stopped: start it first."
		vars := check(t, e, "duet", "/variables", []string{sentence}, []string{"Set and deploy", "Remove and deploy", "Edit as .env", `id="env-editor"`})
		doms := check(t, e, "duet", "/domains", []string{sentence}, []string{"Add domain and deploy", "Remove and deploy"})
		conf := check(t, e, "duet", "/configure", []string{sentence}, []string{"Save and deploy"})
		for name, body := range map[string]string{"variables": vars, "domains": doms, "configure": conf} {
			// Forms without submits: nothing in them can post.
			for _, f := range formsIn(body) {
				if strings.HasPrefix(f.Action, "/apps/duet/variables") || f.Action == "/apps/duet/configure" || f.Action == "/apps/duet/domains" {
					if strings.Contains(body, `<button type="submit" class="cv-btn cv-btn-primary cv-btn-sm">Set and deploy`) {
						t.Errorf("%s: %s has a submit", name, f.Action)
					}
				}
			}
		}
		// A hand-made post is refused with the same words, and deploys nothing.
		n := e.deployCount(t, "duet")
		for path, form := range map[string]url.Values{
			"/apps/duet/variables":      {"revision": {e.revisionOf(t, "duet")}, "service": {"web"}, "name": {"A"}, "value": {"1"}},
			"/apps/duet/variables/env":  {"revision": {e.revisionOf(t, "duet")}, "service": {"web"}, "env": {"A=1"}},
			"/apps/duet/domains":        {"revision": {e.revisionOf(t, "duet")}, "service": {"web"}, "host": {"x.duet.example.com"}},
			"/apps/duet/domains/remove": {"revision": {e.revisionOf(t, "duet")}, "service": {"web"}, "host": {"web.duet.example.com"}},
			"/apps/duet/secrets/remove": {"revision": {e.revisionOf(t, "duet")}, "service": {"api"}, "name": {"TOKEN"}},
			"/apps/duet/backups/policy": {"revision": {e.revisionOf(t, "duet")}, "service": {"web"}},
		} {
			resp, body := e.postForm(t, path, form)
			if resp.StatusCode >= 500 || resp.StatusCode == http.StatusSeeOther || !strings.Contains(text(body), sentence) && path != "/apps/duet/backups/policy" {
				t.Errorf("POST %s on a stopped app = %d:\n%s", path, resp.StatusCode, text(body))
			}
		}
		if e.deployCount(t, "duet") != n {
			t.Error("a stopped app deployed")
		}
	})
}

// A variable is set, or replaced, in that service's env; everything else of the stored app stays;
// the deploy is a console one on the page's revision.
func TestSetVariable(t *testing.T) {
	post := func(e *consoleEnv, set url.Values) (*http.Response, string) {
		return e.submit(t, e.variablesForm(t, "duet"), set)
	}
	t.Run("add and replace", func(t *testing.T) {
		e := duetEnv(t)
		before := e.storedApp(t, "duet")
		n := e.deployCount(t, "duet")
		resp, body := post(e, url.Values{"service": {"api"}, "name": {"NEW_ONE"}, "value": {"a=b c"}, "secret": {""}})
		recs := e.deployments(t, "duet")
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duet/deployments#deployment-"+recs[0].Deployment.ID {
			t.Fatalf("POST = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		if len(recs) != n+1 || recs[0].Deployment.Trigger != app.TriggerConsole {
			t.Errorf("deployments = %d (newest %s), want one more, from the console", len(recs), recs[0].Deployment.Trigger)
		}
		want := before
		want.Services = append([]app.Service(nil), before.Services...)
		want.Services[1].Env = []app.EnvVar{{Name: "LOG_LEVEL", Value: "info"}, {Name: "NEW_ONE", Value: "a=b c"}}
		if got := e.storedApp(t, "duet"); !reflect.DeepEqual(got, want) {
			t.Errorf("the stored app\n got: %+v\nwant: %+v", got, want)
		}
		if v := e.Nomad.Variable("nomad/jobs/duet/api"); v["TOKEN"] != secretMarker {
			t.Errorf("the secret's value didn't survive: %v", v)
		}

		// The same name again replaces the value, in place.
		resp, _ = post(e, url.Values{"service": {"api"}, "name": {"LOG_LEVEL"}, "value": {"debug"}, "secret": {""}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("replace = %d", resp.StatusCode)
		}
		want.Services[1].Env = []app.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}, {Name: "NEW_ONE", Value: "a=b c"}}
		if got := e.storedApp(t, "duet"); !reflect.DeepEqual(got, want) {
			t.Errorf("after the replace\n got: %+v\nwant: %+v", got, want)
		}
	})

	t.Run("a name that is a secret or a variable already", func(t *testing.T) {
		e := duetEnv(t)
		n := e.deployCount(t, "duet")
		resp, body := post(e, url.Values{"service": {"api"}, "name": {"TOKEN"}, "value": {"x"}, "secret": {""}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "TOKEN is already a secret of this service.") || !strings.Contains(body, `aria-describedby="var-name-error"`) {
			t.Errorf("a variable named like a secret = %d:\n%s", resp.StatusCode, text(body))
		}
		resp, body = post(e, url.Values{"service": {"api"}, "name": {"LOG_LEVEL"}, "value": {"x"}, "secret": {"1"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "LOG_LEVEL is already a variable of this service.") {
			t.Errorf("a secret named like a variable = %d:\n%s", resp.StatusCode, text(body))
		}
		if e.deployCount(t, "duet") != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("refusals keep what was typed, a secret's value never", func(t *testing.T) {
		e := duetEnv(t)
		resp, body := post(e, url.Values{"service": {"api"}, "name": {"BAD=NAME"}, "value": {"keep me"}, "secret": {""}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `value="BAD=NAME"`) || !strings.Contains(body, "keep me") || !strings.Contains(body, `<option value="api" selected>`) {
			t.Errorf("a bad variable name = %d, want the form back with what was typed:\n%s", resp.StatusCode, text(body))
		}
		resp, body = post(e, url.Values{"service": {"api"}, "name": {"1BAD"}, "value": {"s3cret-021p2-typed"}, "secret": {"1"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || strings.Contains(body, "s3cret-021p2-typed") || !strings.Contains(body, `value="1BAD"`) {
			t.Errorf("a bad secret name = %d, want the form back without the value:\n%s", resp.StatusCode, text(body))
		}
		resp, body = post(e, url.Values{"service": {"api"}, "name": {"MULTI"}, "value": {"one\r\ntwo"}, "secret": {""}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "A variable's value is one line.") {
			t.Errorf("a multi-line variable = %d:\n%s", resp.StatusCode, text(body))
		}
		resp, body = post(e, url.Values{"service": {"nope"}, "name": {"A"}, "value": {"1"}, "secret": {""}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "This app has no service nope.") {
			t.Errorf("an unknown service = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("a stale page is refused and nothing deploys", func(t *testing.T) {
		e := duetEnv(t)
		f := e.variablesForm(t, "duet")
		other := e.storedApp(t, "duet")
		other.Services[0].Resources.MemoryMB = 300
		e.deploy(t, other)
		n, stored := e.deployCount(t, "duet"), e.storedApp(t, "duet")
		resp, body := e.submit(t, f, url.Values{"service": {"web"}, "name": {"LATE"}, "value": {"1"}, "secret": {""}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), changedSincePage) || !strings.Contains(body, `value="LATE"`) {
			t.Errorf("a stale post = %d:\n%s", resp.StatusCode, text(body))
		}
		if e.deployCount(t, "duet") != n || !reflect.DeepEqual(e.storedApp(t, "duet"), stored) {
			t.Error("a stale post changed the app")
		}
		// The same for a secret and for Remove.
		resp, _ = e.submit(t, f, url.Values{"service": {"web"}, "name": {"LATE_SECRET"}, "value": {"v"}, "secret": {"1"}})
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("a stale secret post = %d, want 409", resp.StatusCode)
		}
		resp, _ = e.postForm(t, "/apps/duet/secrets/remove", url.Values{"revision": {f.Values.Get("revision")}, "service": {"api"}, "name": {"TOKEN"}})
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("a stale Remove = %d, want 409", resp.StatusCode)
		}
		if e.deployCount(t, "duet") != n {
			t.Error("a stale post deployed")
		}
	})

	t.Run("a kept Compose file takes secrets and refuses variables", func(t *testing.T) {
		e, _ := keptShopEnv(t)
		before := e.storedApp(t, "shop")
		f := e.variablesForm(t, "shop")
		resp, body := e.submit(t, f, url.Values{"service": {"web"}, "name": {"MODE"}, "value": {"1"}, "secret": {}, "secretChoice": {"1"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app is edited through its Compose file.") {
			t.Errorf("a variable on a kept-file app = %d:\n%s", resp.StatusCode, text(body))
		}
		if !reflect.DeepEqual(e.storedApp(t, "shop"), before) {
			t.Error("the refusal changed the app")
		}
		resp, body = e.submit(t, f, url.Values{"service": {"web"}, "name": {"EXTRA_KEY"}, "value": {"v"}, "secret": {"1"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("a secret on a kept-file app = %d:\n%s", resp.StatusCode, text(body))
		}
		rec, _ := e.Store.GetApp(context.Background(), "shop")
		names := []string{}
		for _, s := range rec.Compose.Services["web"].Secrets {
			names = append(names, s.Name)
		}
		if rec.Compose == nil || !reflect.DeepEqual(names, []string{"API_KEY", "EXTRA_KEY"}) {
			t.Errorf("the kept overlay's secrets = %v, want API_KEY and EXTRA_KEY", names)
		}
	})
}

// The .env editor replaces one service's variables as a whole, and nothing else.
func TestSaveEnvText(t *testing.T) {
	e := duetEnv(t)
	_, page := e.get(t, "/apps/duet/variables")
	var web plainForm
	for _, f := range formsIn(page) {
		if f.Action == "/apps/duet/variables/env" && f.Values.Get("service") == "web" {
			web = f
		}
	}
	if web.Action == "" || web.Values.Get("env") != "MODE=plain" {
		t.Fatalf("the web editor = %+v", web)
	}
	before := e.storedApp(t, "duet")
	resp, body := e.submit(t, web, url.Values{"env": {"# the new list\r\nNEW=1\r\nOTHER=a=b\r\n"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/duet/deployments#deployment-") {
		t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
	}
	want := before
	want.Services = append([]app.Service(nil), before.Services...)
	want.Services[0].Env = []app.EnvVar{{Name: "NEW", Value: "1"}, {Name: "OTHER", Value: "a=b"}}
	if got := e.storedApp(t, "duet"); !reflect.DeepEqual(got, want) {
		t.Errorf("the stored app\n got: %+v\nwant: %+v", got, want)
	}
	if v := e.Nomad.Variable("nomad/jobs/duet/api"); v["TOKEN"] != secretMarker {
		t.Errorf("the secret's value didn't survive: %v", v)
	}

	// A line without "=" and a secret's name are refused at the textarea, with what was typed.
	_, page = e.get(t, "/apps/duet/variables")
	for _, f := range formsIn(page) {
		if f.Action == "/apps/duet/variables/env" && f.Values.Get("service") == "api" {
			web = f
		}
	}
	n := e.deployCount(t, "duet")
	for text0, wantText := range map[string]string{"A=1\nB": "Line 2 has no", "TOKEN=1": "TOKEN is a secret of this service."} {
		resp, body = e.submit(t, web, url.Values{"env": {text0}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), wantText) || !strings.Contains(body, `aria-describedby="env-api-hint env-api-error"`) {
			t.Errorf("env %q = %d, want 422 with %q at the textarea:\n%s", text0, resp.StatusCode, wantText, text(body))
		}
	}
	if e.deployCount(t, "duet") != n {
		t.Error("a refused .env deployed")
	}
	// A stale page.
	other := e.storedApp(t, "duet")
	other.Services[0].Resources.MemoryMB = 222
	e.deploy(t, other)
	n = e.deployCount(t, "duet")
	resp, body = e.submit(t, web, url.Values{"env": {"LATE=1"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), changedSincePage) {
		t.Errorf("a stale save = %d:\n%s", resp.StatusCode, text(body))
	}
	if e.deployCount(t, "duet") != n {
		t.Error("a refused save deployed")
	}
}

// A domain is added to the service it is sent to, on the port its domains already use, and removed
// again; both are deploys on the page's revision.
func TestAddAndRemoveDomain(t *testing.T) {
	e := duetEnv(t)
	before := e.storedApp(t, "duet")
	_, page := e.get(t, "/apps/duet/domains")
	add := formWith(t, page, "/apps/duet/domains")
	resp, body := e.submit(t, add, url.Values{"service": {"api"}, "host": {"v2.duet.example.com"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/duet/deployments#deployment-") {
		t.Fatalf("add = %d:\n%s", resp.StatusCode, text(body))
	}
	want := before
	want.Services = append([]app.Service(nil), before.Services...)
	want.Services[1].Domains = []app.Domain{{Host: "api.duet.example.com", Port: "http"}, {Host: "v2.duet.example.com", Port: "http"}}
	if got := e.storedApp(t, "duet"); !reflect.DeepEqual(got, want) {
		t.Errorf("the stored app\n got: %+v\nwant: %+v", got, want)
	}
	// The table is sorted by host.
	body = e.pageOf(t, "/apps/duet/domains")
	if a, b, c := strings.Index(body, `title="api.duet`), strings.Index(body, `title="v2.duet`), strings.Index(body, `title="web.duet`); !(a < b && b < c) {
		t.Errorf("the rows aren't sorted by host: %d %d %d", a, b, c)
	}

	_, page = e.get(t, "/apps/duet/domains")
	var remove plainForm
	for _, f := range formsIn(page) {
		if f.Action == "/apps/duet/domains/remove" && f.Values.Get("host") == "v2.duet.example.com" {
			remove = f
		}
	}
	if remove.Action == "" || remove.Values.Get("service") != "api" {
		t.Fatalf("no remove form for the new domain")
	}
	if resp, body = e.submit(t, remove, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove = %d:\n%s", resp.StatusCode, text(body))
	}
	got := e.storedApp(t, "duet")
	want.Services[1].Domains = []app.Domain{{Host: "api.duet.example.com", Port: "http"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after the remove\n got: %+v\nwant: %+v", got, want)
	}

	t.Run("refusals", func(t *testing.T) {
		n := e.deployCount(t, "duet")
		_, page := e.get(t, "/apps/duet/domains")
		add := formWith(t, page, "/apps/duet/domains")
		var remove plainForm
		for _, f := range formsIn(page) {
			if f.Action == "/apps/duet/domains/remove" && f.Values.Get("host") == "web.duet.example.com" {
				remove = f
			}
		}
		for host, wantText := range map[string]string{
			"web.duet.example.com": "is already used in this app", // a duplicate
			"Not A Host":           "is not a lowercase hostname",
			"":                     "Enter a domain.",
		} {
			resp, body := e.submit(t, add, url.Values{"service": {"api"}, "host": {host}})
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), wantText) || !strings.Contains(body, `aria-describedby="domain-host-error"`) {
				t.Errorf("host %q = %d, want 422 with %q at the input:\n%s", host, resp.StatusCode, wantText, text(body))
			}
		}
		if e.deployCount(t, "duet") != n {
			t.Error("a refused domain deployed")
		}
		// A stale page.
		other := e.storedApp(t, "duet")
		other.Services[0].Resources.MemoryMB = 123
		e.deploy(t, other)
		n = e.deployCount(t, "duet")
		resp, body := e.submit(t, add, url.Values{"service": {"api"}, "host": {"late.duet.example.com"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), changedSincePage) {
			t.Errorf("a stale add = %d:\n%s", resp.StatusCode, text(body))
		}
		if resp, _ = e.submit(t, remove, nil); resp.StatusCode != http.StatusConflict {
			t.Errorf("a stale remove = %d, want 409", resp.StatusCode)
		}
		if e.deployCount(t, "duet") != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("a kept Compose file is read-only", func(t *testing.T) {
		k, _ := keptShopEnv(t)
		n := k.deployCount(t, "shop")
		resp, body := k.postForm(t, "/apps/shop/domains", url.Values{"revision": {k.revisionOf(t, "shop")}, "service": {"web"}, "host": {"x.shop.example.com"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app is edited through its Compose file.") || k.deployCount(t, "shop") != n {
			t.Errorf("add on a kept-file app = %d:\n%s", resp.StatusCode, text(body))
		}
	})
}

// The add form's record box: the A record's name from the typed host, and the address when the
// resolver gives one, else the words for it.
func TestDomainRecordBox(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	box := func(body string) string {
		i := strings.Index(body, `class="cv-dnsbox"`)
		j := strings.Index(body[i:], "</dl>")
		return text(body[i : i+j])
	}

	t.Run("no public URL: the words", func(t *testing.T) {
		e.Svc.SetPublicURL("")
		if got := box(e.pageOf(t, "/apps/whoami/domains")); !strings.Contains(got, "Type A") || !strings.Contains(got, "Value this host's public IP address") {
			t.Errorf("the box = %q", got)
		}
	})

	t.Run("the address the host resolves to", func(t *testing.T) {
		e.Svc.SetPublicURL("https://console.example.com")
		e.Svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{"console.example.com": {"2001:db8::7", "203.0.113.7"}}}, nil)
		if got := box(e.pageOf(t, "/apps/whoami/domains")); !strings.Contains(got, "Value 203.0.113.7") || strings.Contains(got, "public IP address") {
			t.Errorf("the box = %q", got)
		}
		// A refused add draws the box for the host that was typed: an apex is "@", a subdomain its first label.
		add := formWith(t, e.pageOf(t, "/apps/whoami/domains"), "/apps/whoami/domains")
		_, body := e.submit(t, add, url.Values{"host": {"Example.com"}}) // uppercase: refused, and one dot
		if got := box(body); !strings.Contains(got, "Name @") || !strings.Contains(got, "Value 203.0.113.7") {
			t.Errorf("the box for an apex = %q", got)
		}
		_, body = e.submit(t, add, url.Values{"host": {"Sub.example.com"}})
		if got := box(body); !strings.Contains(got, "Name sub") {
			t.Errorf("the box for a subdomain = %q", got)
		}
	})

	t.Run("a host that doesn't resolve: the words", func(t *testing.T) {
		e.Svc.SetEdge(edgetest.FakeResolver{}, nil) // a new resolver drops the kept address; this one finds nothing
		if got := box(e.pageOf(t, "/apps/whoami/domains")); !strings.Contains(got, "Value this host's public IP address") {
			t.Errorf("the box = %q", got)
		}
	})
}

// "Back up daily": which services get the offer, and the policy it sets.
func TestBackupGapsAndPolicy(t *testing.T) {
	// Three services: a database on a volume, a plain volume, and a worker with neither.
	mk := func() app.App {
		return app.App{ID: "gaps", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
			{Name: "db", Image: "postgres:17", Env: []app.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pw"}}, Ports: []app.Port{{Name: "pg", ContainerPort: 5432}}, Health: &app.HealthCheck{},
				Volumes: []app.Volume{{Name: "pgdata", Path: "/var/lib/postgresql/data"}}},
			{Name: "files", Image: "traefik/whoami:latest", Volumes: []app.Volume{{Name: "files", Path: "/files"}, {Name: "cache", Path: "/cache"}}},
			{Name: "worker", Image: "traefik/whoami:latest"},
		}}
	}
	e := backupsEnv(t, true)
	e.Reg.AddIndex("library/postgres", "17", otherDigest)
	e.seed(t, mk(), seedDeploy{ID: "g1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})

	body := e.pageOf(t, "/apps/gaps/backups")
	got := text(body)
	for _, want := range []string{"db has no backup. PostgreSQL · 0 copies kept", "files · /files, /cache has no backup. Volume", "0 backed up · 0 failing · 2 not · stored in backups", "Back up daily"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "worker has no backup") || strings.Count(body, `action="/apps/gaps/backups/policy"`) != 2 {
		t.Errorf("a service with neither a database nor a volume got an offer, or a gap has no form:\n%s", got)
	}

	// Each posts its service with the page's revision, and sets the engine its kind calls for.
	_, page := e.get(t, "/apps/gaps/backups")
	for _, tc := range []struct{ service, engine string }{{"db", app.BackupPostgres}, {"files", app.BackupVolume}} {
		var f plainForm
		for _, c := range formsIn(page) {
			if c.Action == "/apps/gaps/backups/policy" && c.Values.Get("service") == tc.service {
				f = c
			}
		}
		if f.Action == "" || f.Values.Get("revision") == "" {
			t.Fatalf("no policy form for %s", tc.service)
		}
		before := e.storedApp(t, "gaps")
		// The first post is on the page's revision; the second is stale (the first changed the app).
		f.Values.Set("revision", e.revisionOf(t, "gaps"))
		resp, body := e.submit(t, f, nil)
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/gaps/deployments#deployment-") {
			t.Fatalf("Back up daily on %s = %d:\n%s", tc.service, resp.StatusCode, text(body))
		}
		after := e.storedApp(t, "gaps")
		for i, svc := range after.Services {
			want := before.Services[i]
			if svc.Name == tc.service {
				want.Backup = &app.Backup{Engine: tc.engine, Schedule: "0 3 * * *", Keep: 7}
			}
			if !reflect.DeepEqual(svc, want) {
				t.Errorf("%s after %s's policy:\n got: %+v\nwant: %+v", svc.Name, tc.service, svc, want)
			}
		}
	}
	got = text(e.pageOf(t, "/apps/gaps/backups"))
	if !strings.Contains(got, "2 backed up · 0 failing · 0 not") || strings.Contains(got, "has no backup.") {
		t.Errorf("after both policies:\n%s", got)
	}

	t.Run("a stale page is refused", func(t *testing.T) {
		e := backupsEnv(t, true)
		a := mk()
		a.Services = a.Services[1:2]
		e.seed(t, a, seedDeploy{ID: "g1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
		page := e.pageOf(t, "/apps/gaps/backups")
		f := formWith(t, page, "/apps/gaps/backups/policy")
		e.seed(t, func() app.App {
			b := a
			b.Services = append([]app.Service(nil), a.Services...)
			b.Services[0].Resources.MemoryMB = 222
			return b
		}())
		before := e.storedApp(t, "gaps")
		resp, body := e.submit(t, f, nil)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), changedSincePage) || !reflect.DeepEqual(e.storedApp(t, "gaps"), before) {
			t.Errorf("a stale post = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("bad posts", func(t *testing.T) {
		resp, _ := e.postForm(t, "/apps/gaps/backups/policy", url.Values{"service": {"db"}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("without a revision = %d, want 400", resp.StatusCode)
		}
		resp, body := e.postForm(t, "/apps/gaps/backups/policy", url.Values{"revision": {e.revisionOf(t, "gaps")}, "service": {"worker"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "has no database or volume to back up") {
			t.Errorf("a worker = %d:\n%s", resp.StatusCode, text(body))
		}
	})
}

// "Back up daily" on an app that keeps its Compose file rewrites the file's overlay, as secrets do,
// and keeps the file as typed.
func TestBackupPolicyOnAKeptFile(t *testing.T) {
	req := shopRequest()
	ov := req.Services["db"]
	ov.Backup = nil
	req.Services["db"] = ov
	e, _ := deployShop(t, req)
	if !strings.Contains(text(e.pageOf(t, "/apps/shop/backups")), "db has no backup.") {
		t.Fatalf("no gap row for db:\n%s", text(e.pageOf(t, "/apps/shop/backups")))
	}
	f := formWith(t, e.pageOf(t, "/apps/shop/backups"), "/apps/shop/backups/policy")
	resp, body := e.submit(t, f, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Back up daily = %d:\n%s", resp.StatusCode, text(body))
	}
	rec, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil || rec.Compose == nil || rec.Compose.Compose != shopCompose {
		t.Fatalf("the kept file = %+v, %v; want the file as it was", rec.Compose, err)
	}
	want := &app.Backup{Engine: app.BackupPostgres, Schedule: "0 3 * * *", Keep: 7}
	if got := rec.Compose.Services["db"].Backup; !reflect.DeepEqual(got, want) {
		t.Errorf("the overlay's backup = %+v, want %+v", got, want)
	}
	if got := rec.Compose.Services["db"].Secrets; len(got) != 1 || got[0].Name != "POSTGRES_PASSWORD" || got[0].Value != nil {
		t.Errorf("the overlay's secrets = %+v, want the name only", got)
	}
	for _, svc := range rec.App.Services {
		if svc.Name == "db" && !reflect.DeepEqual(svc.Backup, want) {
			t.Errorf("db's backup = %+v", svc.Backup)
		}
	}
	recs := e.deployments(t, "shop")
	for _, w := range recs[0].Warnings {
		if strings.Contains(w.Message, "dropped") {
			t.Errorf("the deploy dropped the file: %+v", recs[0].Warnings)
		}
	}
}

// restoreStrip: 30 cells, today last; a healthy run is a bar linking to its row, a failed one and a
// running one their own; a day with no run is a gap.
func TestRestoreStrip(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	run := func(id string, ago time.Duration, status string, bytes int64) store.BackupRecord {
		return store.BackupRecord{ID: id, Service: "db", Status: status, Bytes: bytes, StartedAt: now.Add(-ago)}
	}
	runs := []store.BackupRecord{ // newest first
		run("r-now", time.Hour, store.BackupRunning, 0),              // today, running
		run("r-fail", 25*time.Hour, store.BackupFailed, 0),           // yesterday: the newest run failed, a good one ran earlier
		run("r-ok-yday", 26*time.Hour, store.BackupSucceeded, 3<<20), // the same day, earlier
		run("r-3", 3*24*time.Hour+3*time.Hour, store.BackupSucceeded, 1900<<20),
		run("r-old", 40*24*time.Hour, store.BackupSucceeded, 1), // beyond 30 days: not drawn
	}
	cells := restoreStrip(now, runs)
	if len(cells) != 30 {
		t.Fatalf("%d cells, want 30", len(cells))
	}
	kinds := map[int]string{29: "running", 28: "healthy", 26: "healthy"}
	links := 0
	for i, c := range cells {
		want := kinds[i]
		if want == "" {
			want = "none"
		}
		if c.Kind != want {
			t.Errorf("cell %d = %q, want %q", i, c.Kind, want)
		}
		if c.Href != "" {
			links++
		}
	}
	if links != 3 {
		t.Errorf("%d cells link to a run, want 3", links)
	}
	// A day with a healthy run is restorable: its bar is healthy and links to that run, with a failed
	// cap because the day's newest run failed.
	if c := cells[28]; c.Href != "#backup-r-ok-yday" || !c.Capped || c.Title != "1 d ago · 2 runs, the last failed" {
		t.Errorf("yesterday's cell = %+v, want the healthy run, capped, titled for two runs", c)
	}
	if cells[26].Capped || cells[29].Capped {
		t.Error("a day whose newest run didn't fail has a failed cap")
	}
	// The other way round: a healthy run after a failed one is a healthy bar without a cap.
	back := restoreStrip(now, []store.BackupRecord{run("ok", time.Hour, store.BackupSucceeded, 2<<20), run("bad", 2*time.Hour, store.BackupFailed, 0)})
	if back[29].Kind != "healthy" || back[29].Href != "#backup-ok" || back[29].Capped || back[29].Title != "today · 2 runs, the last healthy" {
		t.Errorf("a healthy run after a failed one = %+v, want healthy, uncapped", back[29])
	}
	lateFail := restoreStrip(now, []store.BackupRecord{run("bad", time.Hour, store.BackupFailed, 0), run("ok", 2*time.Hour, store.BackupSucceeded, 2<<20)})
	if lateFail[29].Kind != "healthy" || !lateFail[29].Capped || lateFail[29].Title != "today · 2 runs, the last failed" {
		t.Errorf("a failed run after a healthy one = %+v", lateFail[29])
	}
	if cells[26].Title != "3 d ago · 12:00 · 1.9 GB" || cells[26].Href != "#backup-r-3" {
		t.Errorf("the cell of three days ago = %+v", cells[26])
	}
	if cells[29].Title != "today · 14:00 · running" {
		t.Errorf("today's cell = %+v", cells[29])
	}
	only := restoreStrip(now, []store.BackupRecord{run("f", 2*time.Hour, store.BackupFailed, 0)})
	if only[29].Kind != "failed" || only[29].Title != "today · 13:00 · failed" {
		t.Errorf("a failed day = %+v", only[29])
	}
}

// The Backups tab's rows: the first one open, the strip drawn, the counts, and Restore… on a
// healthy run; no destination, no buttons.
func TestBackupsRows(t *testing.T) {
	e := backupsEnv(t, true)
	a := e.storedApp(t, "duo")
	// A second backed-up service.
	a.Services[1].Image = "postgres:17"
	a.Services[1].Ports, a.Services[1].Health = []app.Port{{Name: "pg", ContainerPort: 5432}}, &app.HealthCheck{}
	a.Services[1].Env = []app.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pw"}}
	a.Services[1].Backup = &app.Backup{Engine: app.BackupPostgres, Schedule: "30 14 * * *", Keep: 2}
	e.Reg.AddIndex("library/postgres", "17", otherDigest)
	e.deploy(t, a)
	for _, b := range []store.BackupRecord{
		{AppID: "duo", ID: "web-100", Service: "web", Engine: "postgres", Status: store.BackupSucceeded, Bytes: 5 << 20, StartedAt: e.Clock.Now().Add(-3 * time.Hour)},
		{AppID: "duo", ID: "worker-100", Service: "worker", Engine: "postgres", Status: store.BackupFailed, Message: "pg_dump: connection refused", StartedAt: e.Clock.Now().Add(-5 * time.Hour)},
	} {
		fin := b.StartedAt.Add(time.Minute)
		b.FinishedAt = &fin
		if err := e.Store.SaveBackup(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	body := e.pageOf(t, "/apps/duo/backups")
	got := text(body)
	for _, want := range []string{"2 backed up · 1 failing · 0 not · stored in backups", "Every day at 14:30 UTC 2 backups", "The last backup failed 5 hours ago.",
		"took 1 min", "5 MB 5 MB all", "30 d ago today"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	if n := strings.Count(body, `<details class="cv-bdetails"`); n != 2 || strings.Count(body, `<details class="cv-bdetails" id="svc-web" open>`) != 1 || strings.Contains(body, `id="svc-worker" open`) {
		t.Errorf("the rows: %d, the first should be open and the second closed:\n%s", n, got)
	}
	for _, id := range []string{"web-100"} {
		if !strings.Contains(body, `href="#backup-`+id+`"`) || !strings.Contains(body, `id="backup-`+id+`"`) {
			t.Errorf("the strip's bar doesn't link to its run %s", id)
		}
	}
	if n := strings.Count(body, `class="cv-bar `); n != 60 {
		t.Errorf("%d cells in two strips, want 60", n)
	}
	// The failed run has no Restore…
	if strings.Contains(body, "/backups/worker-100/restore") || !strings.Contains(body, "/backups/web-100/restore") {
		t.Error("Restore… is on the wrong runs")
	}

	t.Run("no destination", func(t *testing.T) {
		n := backupsEnv(t, false)
		got := text(n.pageOf(t, "/apps/duo/backups"))
		if strings.Contains(got, "Back up now") || strings.Contains(got, "Restore…") || strings.Contains(got, "Restore from") || !strings.Contains(got, "Set the destination") {
			t.Errorf("without a destination:\n%s", got)
		}
	})

	t.Run("show older", func(t *testing.T) {
		for i := range 12 {
			b := store.BackupRecord{AppID: "duo", ID: "web-" + string(rune('a'+i)), Service: "web", Engine: "postgres", Status: store.BackupSucceeded, Bytes: 1 << 20,
				StartedAt: e.Clock.Now().Add(-time.Duration(10+i) * time.Hour)}
			if err := e.Store.SaveBackup(context.Background(), b); err != nil {
				t.Fatal(err)
			}
		}
		body := e.pageOf(t, "/apps/duo/backups")
		if n := len(regexp.MustCompile(`<div class="cv-brun cv-confirm-row" id="backup-web-`).FindAllString(body, -1)); n != backupRunsShown || !strings.Contains(body, `href="/apps/duo/backups?all=web#svc-web"`) {
			t.Errorf("%d runs listed, want %d and a Show older link", n, backupRunsShown)
		}
		body = e.pageOf(t, "/apps/duo/backups?all=web")
		if n := len(regexp.MustCompile(`<div class="cv-brun cv-confirm-row" id="backup-web-`).FindAllString(body, -1)); n != 13 || strings.Contains(body, "Show older") {
			t.Errorf("%d runs listed with ?all=web, want all 13 and no link", n)
		}
	})
}

// A secret set through the Variables form is in no page afterwards, nor in any refusal or log.
func TestNoSecretValueOnPage2Pages(t *testing.T) {
	const sentinel = "s3cret-sentinel-021p2"
	e := duetEnv(t)
	f := e.variablesForm(t, "duet")
	resp, body := e.submit(t, f, url.Values{"service": {"web"}, "name": {"SENTINEL"}, "value": {sentinel}, "secret": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
	}
	if v := e.Nomad.Variable("nomad/jobs/duet/web"); v["SENTINEL"] != sentinel {
		t.Fatalf("the secret didn't reach the store: %v", v)
	}
	e.waitDeploy(t, e.deployments(t, "duet")[0].Deployment.ID)

	pages := map[string]string{}
	for _, p := range []string{"", "/configure", "/variables", "/domains", "/backups", "/deployments", "/logs"} {
		pages[p] = e.pageOf(t, "/apps/duet"+p)
	}
	for _, p := range []string{"/header", "/copies", "/deployments", "/actions"} {
		resp, body := e.get(t, "/parts/apps/duet"+p)
		pages["part"+p] = body
		_ = resp
	}
	// Refused posts that carry the value back: a bad name, a stale page, the store refusing.
	_, pages["bad name"] = e.submit(t, e.variablesForm(t, "duet"), url.Values{"service": {"web"}, "name": {"1BAD"}, "value": {sentinel}, "secret": {"1"}})
	_, pages["clash"] = e.submit(t, e.variablesForm(t, "duet"), url.Values{"service": {"web"}, "name": {"MODE"}, "value": {sentinel}, "secret": {"1"}})
	stale := e.variablesForm(t, "duet")
	other := e.storedApp(t, "duet")
	other.Services[0].Resources.MemoryMB = 77
	e.deploy(t, other)
	_, pages["stale"] = e.submit(t, stale, url.Values{"service": {"web"}, "name": {"AGAIN"}, "value": {sentinel}, "secret": {"1"}})
	e.Nomad.FailVariables(http.StatusForbidden)
	_, pages["store refuses"] = e.submit(t, e.variablesForm(t, "duet"), url.Values{"service": {"web"}, "name": {"AGAIN"}, "value": {sentinel}, "secret": {"1"}})
	for name, body := range pages {
		if strings.Contains(body, sentinel) {
			t.Errorf("page %q holds the secret's value", name)
		}
	}
	if strings.Contains(e.Logs.String(), sentinel) {
		t.Error("the log holds the secret's value")
	}
}

// The registry notice shows for an image whose host has no credentials when the last deploy failed
// on it, and not otherwise.
func TestRegistryPrereq(t *testing.T) {
	missing := "image not found, or private: add credentials for ghcr.io in Settings › Registries"
	refused := "the registry ghcr.io refused the stored credentials"
	for _, tc := range []struct {
		name, image string
		have        map[string]bool
		err         string
		want        bool
	}{
		{"missing image, no credentials", "ghcr.io/acme/app:1", nil, missing, true},
		{"refused, credentials removed since", "ghcr.io/acme/app:1", nil, refused, true},
		{"credentials are there", "ghcr.io/acme/app:1", map[string]bool{"ghcr.io": true}, missing, false},
		{"another host's image failed", "quay.io/acme/app:1", nil, missing, false},
		{"another kind of failure", "ghcr.io/acme/app:1", nil, "nomad rejected the job", false},
		{"not an image", "Not A Ref!!", nil, missing, false},
		{"Docker Hub is a host too", "acme/app:1", nil, "image not found, or private: add credentials for docker.io in Settings › Registries", true},
	} {
		got := registryPrereqFor(tc.image, tc.have, tc.err)
		if (got != nil) != tc.want {
			t.Errorf("%s: prereq = %+v, want shown %v", tc.name, got, tc.want)
		}
		if got != nil && got.Href != "/settings/registries" {
			t.Errorf("%s: href = %q", tc.name, got.Href)
		}
	}

	// On the page: Configure shows it for the failed image, with one action.
	e := newConsoleEnv(t)
	a := shopApp()
	a.Services = a.Services[:1]
	e.seed(t, a,
		seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1},
		seedDeploy{ID: "s2", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: missing})
	body := e.pageOf(t, "/apps/shop/configure")
	got := text(body)
	if !strings.Contains(got, "Needs a container registry") || !strings.Contains(got, "add credentials for ghcr.io and deploy again") ||
		!strings.Contains(body, `<a href="/settings/registries" class="cv-btn cv-btn-secondary cv-btn-sm">Add credentials in Settings</a>`) {
		t.Errorf("the notice is missing:\n%s", got)
	}
	// A healthy newest deployment shows none.
	e.seed(t, a, seedDeploy{ID: "s3", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 3})
	if got := text(e.pageOf(t, "/apps/shop/configure")); strings.Contains(got, "Needs a container registry") {
		t.Error("the notice shows after a healthy deploy")
	}
}

// The one field rule is 32px; the sign-in's stays 42px; the one select is 32px too, with the field's 13px mono.
func TestFieldsAre32px(t *testing.T) {
	e := newUIEnv(t, true, "")
	_, css := e.do(t, request{path: "/static/app.css"})
	flat := strings.ReplaceAll(strings.ReplaceAll(css, " ", ""), "\n", "")
	for _, want := range []string{".cv-field{", "height:32px", ".cv-field-lg{", "height:42px", ".cv-select{"} {
		if !strings.Contains(flat, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	if m := regexp.MustCompile(`\.cv-select\{[^}]*\}`).FindString(flat); !strings.Contains(m, "height:32px") || !strings.Contains(m, "font:40013px/1var(--font-mono)") || strings.Contains(m, "38px") {
		t.Errorf(".cv-select = %s, want 32px with 13px mono", m)
	}
	// The filter's Apply and Clear and the Logs toolbar's buttons are as tall as the selects beside them.
	for _, want := range []string{".cv-filter.cv-btn{height:32px", ".cv-logbar.cv-btn{height:32px"} {
		if !strings.Contains(flat, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	if m := regexp.MustCompile(`\.cv-field\{[^}]*\}`).FindString(flat); !strings.Contains(m, "height:32px") || strings.Contains(m, "36px") {
		t.Errorf(".cv-field = %s, want 32px", m)
	}
	// A field with a prefix or a suffix is 32px too: the box has the height and the edge, its input neither.
	if m := regexp.MustCompile(`\.cv-field-box\{[^}]*\}`).FindString(flat); !strings.Contains(m, "height:32px") || !strings.Contains(m, "box-sizing:border-box") {
		t.Errorf(".cv-field-box = %s, want a 32px border box", m)
	}
	if all := strings.Join(regexp.MustCompile(`\.cv-field-input\{[^}]*\}`).FindAllString(flat, -1), ""); !strings.Contains(all, "border:0") {
		t.Errorf(".cv-field-input = %s, want no border of its own", all)
	}
	for _, name := range []string{"lock", "external-link", "file-text", "history", "play", "chevron-right", "chevron-down"} {
		if _, ok := icons[name]; !ok {
			t.Errorf("no icon %q", name)
		}
	}
}

// backupEngineFor: a database image with a port gets the dump, else a volume gets the archive; the
// repository is read as Docker reads it, so a registry host or a namespace doesn't hide it.
func TestBackupEngineFor(t *testing.T) {
	port := []app.Port{{Name: "pg", ContainerPort: 5432}}
	vol := []app.Volume{{Name: "d", Path: "/d"}}
	for _, tc := range []struct {
		name string
		svc  app.Service
		want string // "" for nothing to offer
	}{
		{"postgres", app.Service{Image: "postgres:17", Ports: port}, app.BackupPostgres},
		{"postgres by digest", app.Service{Image: "postgres@sha256:" + strings.Repeat("a", 64), Ports: port}, app.BackupPostgres},
		{"a registry's postgres", app.Service{Image: "ghcr.io/acme/postgres:17", Ports: port}, app.BackupPostgres},
		{"docker.io/library/postgres", app.Service{Image: "docker.io/library/postgres:17", Ports: port}, app.BackupPostgres},
		{"postgres on a volume: the dump, which doesn't stop it", app.Service{Image: "postgres:17", Ports: port, Volumes: vol}, app.BackupPostgres},
		{"postgres with no port can't be dumped: its volume is archived", app.Service{Image: "postgres:17", Volumes: vol}, app.BackupVolume},
		{"postgres with no port and no volume", app.Service{Image: "postgres:17"}, ""},
		{"a volume", app.Service{Image: "traefik/whoami", Volumes: vol}, app.BackupVolume},
		{"timescale isn't postgres", app.Service{Image: "timescale/timescaledb:2", Ports: port}, ""},
		{"neither", app.Service{Image: "traefik/whoami", Ports: port}, ""},
		{"not an image", app.Service{Image: "Not A Ref!!", Volumes: vol}, app.BackupVolume},
	} {
		got, ok := backupEngineFor(tc.svc)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: backupEngineFor = %q, %v; want %q", tc.name, got, ok, tc.want)
		}
	}
}

// A failed run's error is drawn once, in its run row; the sentence under the strip says only when.
func TestFailedRunErrorIsDrawnOnce(t *testing.T) {
	e := volumeBackupsEnv(t)
	const msg = "rclone: exit 1: the destination refused the upload of the archive"
	started := e.Clock.Now().Add(-12 * time.Minute).UTC()
	for _, b := range []store.BackupRecord{
		{AppID: "kuma", ID: "uptime-kuma-1000", Service: "uptime-kuma", Engine: app.BackupVolume, Status: store.BackupFailed, Message: msg, StartedAt: started},
	} {
		if err := e.Store.SaveBackup(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	body := e.pageOf(t, "/apps/kuma/backups")
	if n := strings.Count(body, msg); n != 1 {
		t.Errorf("the error is in the page %d times, want once:\n%s", n, text(body))
	}
	if !strings.Contains(text(body), "The last backup failed 12 minutes ago.") {
		t.Errorf("the sentence under the strip:\n%s", text(body))
	}
	if !strings.Contains(body, `<span class="cv-brun-detail">`+msg+`</span>`) {
		t.Error("the run's row lacks the error")
	}
}

// Stopped Configure says it once, at the top, with the idle mark; the forms are disabled.
func TestStoppedConfigureSaysItOnce(t *testing.T) {
	e := duetEnv(t)
	if resp, body := e.postAction(t, "/apps/duet/stop"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("stop = %d:\n%s", resp.StatusCode, text(body))
	}
	body := e.pageOf(t, "/apps/duet/configure")
	if n := strings.Count(body, "The app is stopped: start it first."); n != 1 {
		t.Errorf("the sentence is drawn %d times, want once", n)
	}
	if !strings.Contains(body, `<span class="cv-mark cv-mark-idle" aria-hidden="true"></span><span>The app is stopped: start it first.</span>`) {
		t.Error("the sentence lacks the idle mark")
	}
	if n := strings.Count(body, "<fieldset class=\"cv-fieldset\" disabled>"); n != 2 || strings.Contains(body, "Save and deploy") {
		t.Errorf("%d disabled fieldsets, and a submit: %v", n, strings.Contains(body, "Save and deploy"))
	}
	// A refusal of a post on a stopped app is one failed notice, not that and the idle one.
	resp, refused := e.postForm(t, "/apps/duet/configure", url.Values{"revision": {e.revisionOf(t, "duet")}, "svc.web.image": {"traefik/whoami:latest"}})
	if resp.StatusCode >= 500 || strings.Count(refused, "The app is stopped: start it first.") != 1 {
		t.Errorf("a post on a stopped app = %d, the sentence %d times", resp.StatusCode, strings.Count(refused, "The app is stopped: start it first."))
	}
}

// "Also set": the health check's port only for a service with several ports; the key column doesn't wrap.
func TestAlsoSetHealthPort(t *testing.T) {
	svc := app.Service{Name: "web", Ports: []app.Port{{Name: "http", ContainerPort: 80}}, Command: "/run", Health: &app.HealthCheck{Port: "http"}}
	for _, r := range alsoSet(svc, newEditService(svc)) {
		if r.Label == "Check port" {
			t.Errorf("a one-port service shows the health check's port: %+v", r)
		}
	}
	svc.Ports = append(svc.Ports, app.Port{Name: "metrics", ContainerPort: 9100})
	svc.Health.Port = "metrics"
	found := false
	for _, r := range alsoSet(svc, newEditService(svc)) {
		found = found || r.Label == "Check port" && r.Values[0] == "metrics"
	}
	if !found {
		t.Error("a two-port service doesn't show the health check's port")
	}
	e := newUIEnv(t, true, "")
	_, css := e.do(t, request{path: "/static/app.css"})
	flat := strings.ReplaceAll(strings.ReplaceAll(css, " ", ""), "\n", "")
	if !strings.Contains(flat, ".cv-also-row{display:grid;grid-template-columns:120pxminmax(0,1fr)") && !strings.Contains(flat, "grid-template-columns:120px") || !strings.Contains(flat, ".cv-also-rowdt{white-space:nowrap") {
		t.Error("the key column isn't a fixed 120px that doesn't wrap")
	}
}

// Domains with none: no table head, the empty block alone.
func TestDomainsEmptyHasNoHead(t *testing.T) {
	e := newConsoleEnv(t)
	a := whoamiApp()
	a.ID = "bare"
	a.Services[0].Domains = nil
	e.seed(t, a, seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	body := e.pageOf(t, "/apps/bare/domains")
	if strings.Contains(body, `class="cv-dhead`) || !strings.Contains(text(body), "No domains. Copies are reachable only inside the app.") {
		t.Errorf("the empty Domains page:\n%s", text(body))
	}
}

// A refused per-service Configure form draws the page scrolled to that service: the form's action
// names the service's section, and the first field in error is autofocused.
func TestConfigureRefusalScrollsToTheService(t *testing.T) {
	e := duetEnv(t)
	_, page := e.get(t, "/apps/duet/configure")
	var api plainForm
	for _, f := range formsIn(page) {
		if f.Action == "/apps/duet/configure" && f.Values.Has("svc.api.image") {
			api = f
		}
	}
	if api.Fragment != "service-api" || !strings.Contains(page, `<section id="service-api"`) {
		t.Fatalf("the api form's fragment = %q, want service-api, with a section of that id", api.Fragment)
	}
	resp, body := e.submit(t, api, url.Values{"svc.api.copies": {"zero"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || strings.Count(body, " autofocus") != 1 || !strings.Contains(body, `id="svc-api-copies" type="text" inputmode="numeric" name="svc.api.copies" value="zero"`) && !strings.Contains(body, `id="svc-api-copies"`) {
		t.Errorf("refusal = %d, want 422 with one autofocused field:\n%s", resp.StatusCode, text(body))
	}
	// The refused page's forms still name their sections, so the next refusal scrolls too.
	for _, f := range formsIn(body) {
		if f.Action == "/apps/duet/configure" && f.Fragment == "" {
			t.Errorf("a form of the refused page has no fragment: %+v", f)
		}
	}
}

// The strip as one phrase for a screen reader.
func TestStripLabel(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	run := func(ago time.Duration, status string) store.BackupRecord {
		return store.BackupRecord{Status: status, StartedAt: now.Add(-ago)}
	}
	for name, tc := range map[string]struct {
		runs []store.BackupRecord
		want string
	}{
		"none":            {nil, "30 days: no backups"},
		"one":             {[]store.BackupRecord{run(26*time.Minute, store.BackupSucceeded)}, "30 days: 1 backup, the last 26 minutes ago"},
		"two, one failed": {[]store.BackupRecord{run(26*time.Minute, store.BackupFailed), run(3*time.Hour, store.BackupSucceeded)}, "30 days: 2 backups, 1 failed, the last 26 minutes ago"},
		"old ones aren't": {[]store.BackupRecord{run(time.Hour, store.BackupSucceeded), run(40*24*time.Hour, store.BackupFailed)}, "30 days: 1 backup, the last 1 hour ago"},
	} {
		if got := stripLabel(now, tc.runs); got != tc.want {
			t.Errorf("%s: stripLabel = %q, want %q", name, got, tc.want)
		}
	}
}

// causeOf: a plain reason from a fixed table for what rclone and the S3 SDK say, in the order of the
// table (a specific cause beats the retries around it), and nothing for anything else.
func TestCauseOf(t *testing.T) {
	const (
		refused = "The destination refused the connection."
		host    = "The destination's host name wasn't found."
		denied  = "The destination refused the access key."
		bucket  = "The bucket wasn't found at the destination."
		late    = "The destination didn't answer in time."
	)
	for _, tc := range []struct{ name, msg, want string }{
		{"rclone: connection refused", "Failed to copyto: RequestError: send request failed\ncaused by: Put \"http://10.0.0.5:9000/backups/x\": dial tcp 10.0.0.5:9000: connect: connection refused", refused},
		{"SDK: refused inside the retries", "failed upload: NOTICE: Failed to copyto: operation error S3: PutObject, exceeded maximum number of attempts, 3, https response error StatusCode: 0, RequestID: , HostID: , request send failed, Put \"https://s3.example.com/b\": dial tcp 192.0.2.1:443: connect: connection refused", refused},
		{"no such host", "Failed to create file system for \"dest:backups\": dial tcp: lookup s3.nowhere.example: no such host", host},
		{"AccessDenied", "operation error S3: PutObject, https response error StatusCode: 403, RequestID: 1, api error AccessDenied: Access Denied", denied},
		{"InvalidAccessKeyId", "api error InvalidAccessKeyId: The AWS Access Key Id you provided does not exist in our records.", denied},
		{"SignatureDoesNotMatch", "api error SignatureDoesNotMatch: The request signature we calculated does not match", denied},
		{"NoSuchBucket", "operation error S3: HeadBucket, https response error StatusCode: 404, api error NoSuchBucket: The specified bucket does not exist", bucket},
		{"i/o timeout", "Failed to copyto: Put \"https://s3.example.com/b/x\": dial tcp 192.0.2.1:443: i/o timeout", late},
		{"context deadline", "Failed to copyto: context deadline exceeded", late},
		// The review's db: the SDK gave up after its retries, and the stored message stops there. It is
		// the timed-out entry: the destination never answered well within the retries. A cause named in
		// the message (the cases above) wins over it.
		{"the review's db", "failed upload: ... NOTICE: Failed to copyto: operation error S3: HeadObject, exceeded maximum number of attempt", late},
		{"anything else", "pg_dump: error: connection to server on socket failed: FATAL: role \"x\" does not exist", ""},
		{"empty", "", ""},
		{"a 403 that is only a number", "bytes=1403 written", ""},
	} {
		if got := causeOf(tc.msg); got != tc.want {
			t.Errorf("%s: causeOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A failed run's row leads with the plain cause, then the detail in mono; a message with no known cause
// has the detail alone.
func TestFailedRunRowLeadsWithTheCause(t *testing.T) {
	e := volumeBackupsEnv(t)
	started := e.Clock.Now().Add(-time.Hour).UTC()
	for id, msg := range map[string]string{
		"uptime-kuma-1": "Put \"https://s3.example.com\": dial tcp 192.0.2.1:443: connect: connection refused",
		"uptime-kuma-2": "some other failure",
	} {
		at := started
		if id == "uptime-kuma-2" {
			at = started.Add(-time.Hour)
		}
		if err := e.Store.SaveBackup(context.Background(), store.BackupRecord{AppID: "kuma", ID: id, Service: "uptime-kuma", Engine: app.BackupVolume,
			Status: store.BackupFailed, Message: msg, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	body := e.pageOf(t, "/apps/kuma/backups")
	if !strings.Contains(body, `<p class="cv-brun-msg"><span class="cv-brun-cause">The destination refused the connection.</span><span class="cv-brun-detail">Put &#34;https://s3.example.com&#34;: dial tcp 192.0.2.1:443: connect: connection refused</span></p>`) {
		t.Errorf("the cause isn't before the detail:\n%s", text(body))
	}
	if !strings.Contains(body, `<p class="cv-brun-msg"><span class="cv-brun-detail">some other failure</span></p>`) {
		t.Errorf("a message with no known cause isn't the detail alone:\n%s", text(body))
	}
}

// The summary row, accessibly and with its facts from the newest run: no row or cell roles, a leading
// sr-only state, "· failed" in the failed hue, and a size only from a run that has one. After a failed
// run "Restore from …" is secondary and "Back up now" primary; the head counts the failing.
func TestBackupsFailedRow(t *testing.T) {
	e := volumeBackupsEnv(t)
	// Both runs fall on one UTC day, so the strip has one cell for them: in the first minutes of a
	// day the test clock is moved an hour on, so the test never depends on when it runs.
	if now := e.Clock.Now().UTC(); now.Hour() == 0 && now.Minute() < 40 {
		e.Clock.Advance(time.Hour)
	}
	now := e.Clock.Now().UTC()
	good, bad := now.Add(-35*time.Minute), now.Add(-25*time.Minute)
	fin := good.Add(time.Minute)
	for _, b := range []store.BackupRecord{
		{AppID: "kuma", ID: "uptime-kuma-1", Service: "uptime-kuma", Engine: app.BackupVolume, Status: store.BackupSucceeded, Bytes: 5 << 20, StartedAt: good, FinishedAt: &fin},
		{AppID: "kuma", ID: "uptime-kuma-2", Service: "uptime-kuma", Engine: app.BackupVolume, Status: store.BackupFailed, Message: "boom", StartedAt: bad},
	} {
		if err := e.Store.SaveBackup(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	body := e.pageOf(t, "/apps/kuma/backups")
	got := text(body)
	// The run times are clock times (feature 022): "14:05 UTC" today.
	for _, want := range []string{"1 backed up · 1 failing · 0 not · stored in backups", "uptime-kuma, last backup failed.", bad.Format("15:04") + " UTC · failed",
		"Restore from " + good.Format("15:04") + " UTC", "The last backup failed 25 minutes ago."} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	sum := body[strings.Index(body, `<summary class="cv-bsum"`):]
	sum = sum[:strings.Index(sum, "</summary>")]
	for _, gone := range []string{`role="row"`, `role="cell"`, "aria-label"} {
		if strings.Contains(sum, gone) {
			t.Errorf("the summary has %s", gone)
		}
	}
	if !strings.Contains(sum, `<span class="sr-only">uptime-kuma, last backup failed. </span>`) || !strings.Contains(sum, `<span class="cv-blast-failed">· failed</span>`) {
		t.Errorf("the summary's leading state or its failed word:\n%s", sum)
	}
	// The newest run failed, so the summary shows no size: that of an older run would be of another time.
	if strings.Contains(sum, `<span class="cv-blast-when">5 MB</span>`) {
		t.Errorf("the summary's size is of an older run:\n%s", sum)
	}
	if !strings.Contains(body, `<summary class="cv-btn cv-btn-secondary cv-btn-sm">`+"<svg") || !strings.Contains(body, `cv-btn-primary cv-btn-sm"><svg class="cv-icon" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon`) {
		t.Errorf("after a failed run Restore from… should be secondary and Back up now primary:\n%s", body)
	}
	// A visually hidden sentence says the strip in one phrase; the strip has no role, so its link
	// bars stay reachable with their titles as names, and its empty cells are hidden from readers.
	if !strings.Contains(body, `<p class="sr-only">30 days: 2 backups, 1 failed, the last 25 minutes ago.</p>`) ||
		strings.Contains(body, `role="img"`) || strings.Contains(body, `role="listitem" aria-label="no backup that day"`) {
		t.Errorf("the strip's sentence:\n%s", body)
	}
	if !strings.Contains(body, `class="cv-bar cv-bar-none" aria-hidden="true"`) || !strings.Contains(body, `class="cv-bar cv-bar-healthy cv-bar-capped" title="today · 2 runs, the last failed"`) {
		t.Errorf("the strip's cells:\n%s", body)
	}
}

// "Back up daily" is a confirmation that says what it will do; with no destination its summary is secondary
// and "Set the destination" is the one primary action.
func TestBackUpDailyConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() app.App
		want  string
	}{
		{"volume", func() app.App { return kumaApp() }, "Backs up /app/data every day at 03:00 UTC and keeps 7. The service stops for a few seconds each time. This deploys the app."},
		{"postgres", nil, "Backs up the database every day at 03:00 UTC and keeps 7. This deploys the app."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := backupsEnv(t, true)
			a := duoApp()
			if tc.build != nil {
				a = tc.build()
				a.Services[0].Backup = nil
			} else {
				a.Services = a.Services[:1]
				a.Services[0].Image = "postgres:17"
				a.Services[0].Ports = []app.Port{{Name: "pg", ContainerPort: 5432}}
				a.Services[0].Health = &app.HealthCheck{}
				a.Services[0].Domains = nil
			}
			e.seed(t, a)
			body := e.pageOf(t, "/apps/"+a.ID+"/backups")
			if !strings.Contains(text(body), tc.want) || !strings.Contains(text(body), "Back up daily and deploy") || !strings.Contains(body, `cv-btn cv-btn-primary cv-btn-sm">Back up daily</summary>`) {
				t.Errorf("the confirmation:\n%s", text(body))
			}
			f := formWith(t, body, "/apps/"+a.ID+"/backups/policy")
			if f.Values.Get("revision") == "" || f.Values.Get("service") == "" {
				t.Errorf("the form = %v", f.Values)
			}
		})
	}
	// No destination: secondary summary, a primary "Set the destination".
	e := newConsoleEnv(t)
	k := kumaApp()
	k.Services[0].Backup = nil
	e.seed(t, k)
	body := e.pageOf(t, "/apps/kuma/backups")
	if !strings.Contains(body, `cv-btn cv-btn-secondary cv-btn-sm">Back up daily</summary>`) || !strings.Contains(body, `href="/settings/backup-storage" class="cv-btn cv-btn-primary cv-btn-sm">Set the destination`) {
		t.Errorf("without a destination:\n%s", text(body))
	}
}

// "Keep as a secret" is ticked when the form is drawn. An unticked box sends nothing, so the form also
// sends secretChoice: with it, the box says what is meant; without it the value is a secret, the side
// that shows nothing. A refused submit clears the value and keeps the choice.
func TestKeepAsASecretIsTheDefault(t *testing.T) {
	e := duetEnv(t)
	body := e.pageOf(t, "/apps/duet/variables")
	if !strings.Contains(body, `<input type="checkbox" id="var-secret" name="secret" value="1" checked>`) || !strings.Contains(body, `<input type="hidden" name="secretChoice" value="1">`) {
		t.Errorf("the box isn't ticked by default, or the form doesn't send its choice:\n%s", body)
	}
	if !strings.Contains(text(body), "Untick only for a value anyone signed in may read: it is stored in the app's definition, its deploy history and the export.") {
		t.Errorf("the note is missing:\n%s", text(body))
	}
	f := formWith(t, body, "/apps/duet/variables")
	stored := func() app.Service { return e.storedApp(t, "duet").Services[0] }

	// Ticked: the box is in the post. A secret, whatever the name.
	resp, _ := e.submit(t, f, url.Values{"service": {"web"}, "name": {"PLAIN_LOOKING"}, "value": {"v1"}})
	if resp.StatusCode != http.StatusSeeOther || len(stored().Secrets) != 1 || stored().Secrets[0].Name != "PLAIN_LOOKING" {
		t.Fatalf("a ticked box = %d, secrets %+v, want a secret", resp.StatusCode, stored().Secrets)
	}
	// Unticked: the browser sends the marker and no box.
	f = formWith(t, e.pageOf(t, "/apps/duet/variables"), "/apps/duet/variables")
	resp, _ = e.submit(t, f, url.Values{"service": {"web"}, "name": {"VISIBLE"}, "value": {"v2"}, "secret": {}})
	found := false
	for _, v := range stored().Env {
		found = found || v.Name == "VISIBLE" && v.Value == "v2"
	}
	if resp.StatusCode != http.StatusSeeOther || !found || slices.ContainsFunc(stored().Secrets, func(s app.Secret) bool { return s.Name == "VISIBLE" }) {
		t.Fatalf("an unticked box = %d, env %+v, want a variable", resp.StatusCode, stored().Env)
	}
	// No marker and no box (a post that isn't this form): the value is a secret.
	rev := e.revisionOf(t, "duet")
	resp, _ = e.postForm(t, "/apps/duet/variables", url.Values{"revision": {rev}, "service": {"web"}, "name": {"BARE_POST"}, "value": {"v3"}})
	if resp.StatusCode != http.StatusSeeOther || !slices.ContainsFunc(stored().Secrets, func(s app.Secret) bool { return s.Name == "BARE_POST" }) ||
		slices.ContainsFunc(stored().Env, func(v app.EnvVar) bool { return v.Name == "BARE_POST" }) {
		t.Errorf("a post with neither the marker nor the box = %d, want a secret", resp.StatusCode)
	}

	// A refusal: the value is cleared when it's a secret, kept (it's shown anyway) when unticked, and the box stays as chosen.
	f = formWith(t, e.pageOf(t, "/apps/duet/variables"), "/apps/duet/variables")
	_, refused := e.submit(t, f, url.Values{"service": {"web"}, "name": {"1BAD"}, "value": {"s3cret-021p2-typed"}})
	if strings.Contains(refused, "s3cret-021p2-typed") || !strings.Contains(refused, `id="var-secret" name="secret" value="1" checked>`) {
		t.Errorf("a refused secret: the value must be cleared and the box still ticked:\n%s", text(refused))
	}
	_, refused = e.submit(t, f, url.Values{"service": {"web"}, "name": {"BAD=NAME"}, "value": {"typed-variable"}, "secret": {}})
	if !strings.Contains(refused, "typed-variable") || strings.Contains(refused, `id="var-secret" name="secret" value="1" checked>`) {
		t.Errorf("a refused variable: the value is kept and the box stays unticked:\n%s", text(refused))
	}
}

// Configure's panel is "Variables & secrets" and counts both; a stopped Variables page says so once at the
// top with the idle mark; no "Add domain" where the Domains page can't add; a refused .env says to compare.
func TestCritiqueRulings(t *testing.T) {
	e := duetEnv(t)
	got := text(e.pageOf(t, "/apps/duet/configure"))
	if !strings.Contains(got, "Variables & secrets") || !strings.Contains(got, "2 variables · 1 secret. Values of secrets are never shown here.") || !strings.Contains(got, "Add domain") {
		t.Errorf("Configure's panel:\n%s", got)
	}
	// aria-labels on every Remove control.
	vars := e.pageOf(t, "/apps/duet/variables")
	doms := e.pageOf(t, "/apps/duet/domains")
	if !strings.Contains(vars, `aria-label="Remove TOKEN"`) || !strings.Contains(doms, `aria-label="Remove web.duet.example.com"`) || !strings.Contains(doms, `aria-label="Remove api.duet.example.com"`) {
		t.Error("a Remove control has no name of its own")
	}
	// A refused .env edit.
	_, page := e.get(t, "/apps/duet/variables")
	var web plainForm
	for _, f := range formsIn(page) {
		if f.Action == "/apps/duet/variables/env" && f.Values.Get("service") == "web" {
			web = f
		}
	}
	_, refused := e.submit(t, web, url.Values{"env": {"BROKEN"}})
	if !strings.Contains(text(refused), "Your text is kept below; the table shows what the app has now. Compare before saving.") {
		t.Errorf("a refused .env doesn't say to compare:\n%s", text(refused))
	}
	if strings.Contains(vars, "Your text is kept below") {
		t.Error("the note is on a page nothing was refused on")
	}
	// Hints are not inside labels, and name themselves in aria-describedby.
	conf := e.pageOf(t, "/apps/duet/configure")
	if !strings.Contains(conf, `aria-describedby="svc-web-healthPath-hint"`) || !strings.Contains(conf, `<span class="cv-hint" id="svc-web-healthPath-hint">Empty checks the TCP port.</span>`) {
		t.Error("the health check's hint isn't described by id")
	}
	for _, m := range regexp.MustCompile(`(?s)<label[^>]*>.*?</label>`).FindAllString(conf, -1) {
		if strings.Contains(m, `class="cv-hint"`) {
			t.Errorf("a hint is inside a label: %s", m)
		}
	}
	// A kept-file app and a stopped one have no Add domain link in Configure.
	k, _ := keptShopEnv(t)
	if strings.Contains(text(k.pageOf(t, "/apps/shop/configure")), "Add domain") {
		t.Error("Configure of a kept-file app links to a form that isn't there")
	}
	if resp, _ := e.postAction(t, "/apps/duet/stop"); resp.StatusCode != http.StatusSeeOther {
		t.Fatal("stop")
	}
	if strings.Contains(text(e.pageOf(t, "/apps/duet/configure")), "Add domain") {
		t.Error("Configure of a stopped app links to a form that isn't there")
	}
	stopped := e.pageOf(t, "/apps/duet/variables")
	if n := strings.Count(stopped, "The app is stopped: start it first."); n != 1 || !strings.Contains(stopped, `<span class="cv-mark cv-mark-idle" aria-hidden="true"></span><span>The app is stopped: start it first.</span>`) {
		t.Errorf("Variables of a stopped app says it %d times", n)
	}
}

// One-service Configure has no skipped heading level: an h2 stands before the panels' h3.
func TestOneServiceConfigureHeadings(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	body := e.pageOf(t, "/apps/whoami/configure")
	first3, first2 := strings.Index(body, "<h3"), strings.Index(body, `<h2 class="sr-only">Settings</h2>`)
	if first2 < 0 || first3 < first2 {
		t.Errorf("the first h3 is at %d, the h2 before it at %d", first3, first2)
	}
}
