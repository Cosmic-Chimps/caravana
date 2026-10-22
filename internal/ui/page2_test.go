package ui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
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
			"web :80 · production", `aria-label="Open https://whoami.example.com"`, `target="_blank" rel="noopener"`, "Add a domain", "Point it at Caravana", "Add domain and deploy",
			"The root of a domain needs an A record. A subdomain can use one too."},
			[]string{`id="domain-service"`, "Sends traffic to"})
		check(t, e, "whoami", "/configure", []string{"Copies & size", "Shipping"}, []string{`id="svc-web-title"`})
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
		check(t, e, "shop", "/backups", []string{"1 backed up · 0 not"}, nil)
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
		resp, body := e.submit(t, f, url.Values{"service": {"web"}, "name": {"MODE"}, "value": {"1"}, "secret": {""}})
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
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), wantText) || !strings.Contains(body, `aria-describedby="env-api-error"`) {
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

// recordName is "@" for a host with one dot and the first label otherwise.
func TestRecordName(t *testing.T) {
	for host, want := range map[string]string{
		"example.com":     "@",
		"Example.com.":    "@",
		"www.example.com": "www",
		"a.b.example.com": "a",
		"app.example.com": "app",
		"":                "",
		"  ":              "",
		"example.co.uk":   "example", // the registered domain needs the public suffix list; documented in recordName
	} {
		if got := recordName(host); got != want {
			t.Errorf("recordName(%q) = %q, want %q", host, got, want)
		}
	}
}

// The address a domain's A record points at is what the console's own host name resolves to,
// found lazily with a two-second limit, kept once found, and not asked for again for a minute
// after a failure.
func TestHostIP(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	calls := 0
	h := &hostIP{timeout: time.Second, retry: time.Minute, lookup: func(_ context.Context, network, host string) ([]net.IP, error) {
		calls++
		if network != "ip4" || host != "console.example.com" {
			t.Errorf("lookup(%q, %q)", network, host)
		}
		return []net.IP{net.ParseIP("203.0.113.7")}, nil
	}}
	if got := h.find(context.Background(), "", now); got != "" || calls != 0 {
		t.Errorf("with no public URL: %q after %d lookups, want none", got, calls)
	}
	if got := h.find(context.Background(), "https://console.example.com", now); got != "203.0.113.7" {
		t.Errorf("find = %q", got)
	}
	h.find(context.Background(), "https://console.example.com", now.Add(time.Hour))
	if calls != 1 {
		t.Errorf("%d lookups, want the answer kept for the process", calls)
	}

	calls = 0
	h = &hostIP{timeout: 20 * time.Millisecond, retry: time.Minute, lookup: func(ctx context.Context, _, _ string) ([]net.IP, error) {
		calls++
		<-ctx.Done() // a resolver that never answers
		return nil, ctx.Err()
	}}
	start := time.Now()
	if got := h.find(context.Background(), "https://console.example.com", now); got != "" || time.Since(start) > 2*time.Second {
		t.Errorf("a stuck resolver: %q after %v", got, time.Since(start))
	}
	h.find(context.Background(), "https://console.example.com", now.Add(30*time.Second))
	if calls != 1 {
		t.Errorf("%d lookups within a minute of a failure, want 1", calls)
	}
	h.find(context.Background(), "https://console.example.com", now.Add(2*time.Minute))
	if calls != 2 {
		t.Errorf("%d lookups after the minute, want a retry", calls)
	}

	// A loopback address is the console on a laptop, not a host a domain can point at.
	h = &hostIP{timeout: time.Second, retry: time.Minute, lookup: func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}}
	if got := h.find(context.Background(), "http://127.0.0.1:7070", now); got != "" {
		t.Errorf("a loopback address was offered: %q", got)
	}
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
		e.Server.publicURL = func() string { return "" }
		if got := box(e.pageOf(t, "/apps/whoami/domains")); !strings.Contains(got, "Type A") || !strings.Contains(got, "Value this host's public IP address") {
			t.Errorf("the box = %q", got)
		}
	})

	t.Run("the address the host resolves to", func(t *testing.T) {
		e.Server.publicURL = func() string { return "https://console.example.com" }
		e.Server.hostIP.lookup = func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("203.0.113.7")}, nil
		}
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
		e.Server.hostIP = newHostIP()
		e.Server.hostIP.lookup = func(context.Context, string, string) ([]net.IP, error) { return nil, errors.New("no such host") }
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
	for _, want := range []string{"db has no backup. PostgreSQL · 0 copies kept", "files · /files, /cache has no backup. Volume", "0 backed up · 2 not · stored in backups", "Back up daily"} {
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
	if !strings.Contains(got, "2 backed up · 0 not") || strings.Contains(got, "has no backup.") {
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
		run("r-fail", 25*time.Hour, store.BackupFailed, 0),           // yesterday: failed, and a good one earlier that day
		run("r-ok-yday", 26*time.Hour, store.BackupSucceeded, 3<<20), // the same day: the good one is the restore point
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
	if cells[28].Href != "#backup-r-ok-yday" || cells[28].Title != "1 d ago · 13:00 · 3 MB" {
		t.Errorf("yesterday's cell = %+v, want the good run, not the failed one", cells[28])
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
	for _, want := range []string{"2 backed up · 0 not · stored in backups", "Every day at 14:30 UTC 2 backups", "The last backup failed 5 hours ago: pg_dump: connection refused.",
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
		if got != nil && got.Href != "/settings#registries" {
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
		!strings.Contains(body, `<a href="/settings#registries" class="cv-btn cv-btn-secondary cv-btn-sm">Add credentials in Settings</a>`) {
		t.Errorf("the notice is missing:\n%s", got)
	}
	// A healthy newest deployment shows none.
	e.seed(t, a, seedDeploy{ID: "s3", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 3})
	if got := text(e.pageOf(t, "/apps/shop/configure")); strings.Contains(got, "Needs a container registry") {
		t.Error("the notice shows after a healthy deploy")
	}
}

// The one field rule is 32px; the sign-in's stays 42px; a select is 38px.
func TestFieldsAre32px(t *testing.T) {
	e := newUIEnv(t, true, "")
	_, css := e.do(t, request{path: "/static/app.css"})
	flat := strings.ReplaceAll(strings.ReplaceAll(css, " ", ""), "\n", "")
	for _, want := range []string{".cv-field{", "height:32px", ".cv-field-lg{", "height:42px", ".cv-select{", "height:38px"} {
		if !strings.Contains(flat, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	if m := regexp.MustCompile(`\.cv-field\{[^}]*\}`).FindString(flat); !strings.Contains(m, "height:32px") || strings.Contains(m, "36px") {
		t.Errorf(".cv-field = %s, want 32px", m)
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
