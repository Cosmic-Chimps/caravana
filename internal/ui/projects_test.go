package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// shopConsole is a console with project shop (display name "Shop Floor") and its
// environments production and staging.
func shopConsole(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	ctx := context.Background()
	if _, err := e.Svc.CreateProject(ctx, "shop", "Shop Floor"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	return e
}

// shopWeb is the app web of the shop's environment, running traefik/whoami at tag.
func shopWeb(environment, tag string) app.App {
	a := whoamiApp()
	a.ID = app.Place{Project: "shop", Environment: environment, Name: "web"}.ID()
	a.Services[0].Image = "traefik/whoami:" + tag
	a.Services[0].Domains[0].Host = environment + ".shop.example.com"
	return a
}

// two says how many times s occurs in body, for lists of rows.
func count(body, s string) int { return strings.Count(body, s) }

func TestAppsStayFlatWhileThereIsOneEnvironment(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)
	for _, path := range []string{"/apps", "/parts/apps"} {
		_, body := e.get(t, path)
		for _, banned := range []string{"cv-project", "cv-env", "No apps in"} {
			if strings.Contains(body, banned) {
				t.Errorf("GET %s shows %q while there is one project with one environment", path, banned)
			}
		}
		if count(body, `class="cv-panel cv-apps"`) != 1 {
			t.Errorf("GET %s should have one table", path)
		}
	}
	// New project is on the Projects page, not in the Apps page's top bar.
	_, page := e.get(t, "/projects")
	if !strings.Contains(page, `<a href="/projects/new" class="cv-btn cv-btn-secondary cv-btn-sm">New project</a>`) {
		t.Errorf("the Projects page doesn't offer New project:\n%s", page)
	}
}

func TestAppsAreGroupedByProjectAndEnvironment(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, whoamiApp())
	e.deploy(t, shopWeb("staging", "latest"))

	for _, path := range []string{"/apps", "/parts/apps"} {
		resp, body := e.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		got := text(body)
		for _, want := range []string{"Default", "Shop Floor", "No apps in production yet.", "New app here"} {
			if !strings.Contains(got, want) {
				t.Errorf("GET %s lacks %q:\n%s", path, want, got)
			}
		}
		for _, want := range []string{
			`<a href="/projects/default">Default</a>`, `<a href="/projects/shop">Shop Floor</a>`,
			`href="/apps/new?project=shop&amp;environment=production"`,
			// the row shows the app's name in its environment, and links to its ID
			`<a href="/apps/shop--staging--web" class="cv-app-name">web</a>`, `<a href="/apps/whoami" class="cv-app-name">whoami</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s lacks %s:\n%s", path, want, body)
			}
		}
		// Default has one environment (a table), shop's production has none, shop's staging has one.
		if n := count(body, `class="cv-panel cv-apps"`); n != 2 {
			t.Errorf("GET %s has %d tables, want 2", path, n)
		}
		// The environments are in order: production before staging.
		if strings.Index(body, ">production<") > strings.Index(body, ">staging<") {
			t.Errorf("GET %s lists staging before production", path)
		}
	}
}

func TestNeedsYouAndActivityNameWhereAnAppIs(t *testing.T) {
	e := shopConsole(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusFailed, Error: "image not found"})
	e.seed(t, shopWeb("staging", "latest"), seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusFailed, Error: "image not found"})

	_, body := e.get(t, "/apps")
	got := text(body)
	for _, want := range []string{
		"whoami's deploy failed", // at home: the ID, as before projects
		"Shop Floor › staging › web's deploy failed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the needs-you list lacks %q:\n%s", want, got)
		}
	}
	_, part := e.get(t, "/parts/activity")
	for _, want := range []string{
		`<span class="cv-data-muted">Shop Floor › staging ›</span><span class="cv-app-name">web</span>`,
		`<span class="cv-app-name">whoami</span>`,
	} {
		if !strings.Contains(part, want) {
			t.Errorf("the activity rail lacks %s:\n%s", want, part)
		}
	}
	if strings.Contains(text(part), "shop--staging--web") {
		t.Error("the activity rail shows the app's ID, not where it is")
	}
}

func TestNewProject(t *testing.T) {
	e := newConsoleEnv(t)

	t.Run("the form", func(t *testing.T) {
		resp, body := e.get(t, "/projects/new")
		if resp.StatusCode != http.StatusOK || h1Text(t, body) != "New project" {
			t.Fatalf("GET /projects/new = %d, h1 %q", resp.StatusCode, h1Text(t, body))
		}
		f := formWith(t, body, "/projects")
		if f.Method != "post" || f.HTMX {
			t.Errorf("form = %+v, want a plain post", f)
		}
		for _, want := range []string{`name="name"`, `name="displayName"`, "Lowercase letters, digits and single dashes; part of each app's address in Caravana."} {
			if !strings.Contains(body, want) {
				t.Errorf("the form lacks %q", want)
			}
		}
	})

	t.Run("a valid form creates the project and opens its page", func(t *testing.T) {
		resp, body := e.postForm(t, "/projects", url.Values{"name": {"blog"}, "displayName": {"The Blog"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/projects/blog?created=1" {
			t.Fatalf("POST /projects = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		_, page := e.get(t, location(t, resp))
		if !strings.Contains(text(page), "Project The Blog created with a production environment.") {
			t.Errorf("the page doesn't say the project was created:\n%s", text(page))
		}
		p, err := e.Svc.Project(context.Background(), "blog")
		if err != nil || p.DisplayName != "The Blog" || len(p.Environments) != 1 || p.Environments[0].Name != "production" {
			t.Errorf("project = %+v, %v", p, err)
		}
	})

	tests := []struct {
		name   string
		form   url.Values
		status int
		want   string
	}{
		{"no name", url.Values{"name": {""}}, http.StatusUnprocessableEntity, "Enter a name for the project."},
		{"a name that isn't a label", url.Values{"name": {"Not A Name"}}, http.StatusUnprocessableEntity, "The name is not a lowercase DNS label"},
		{"a name with --", url.Values{"name": {"a--b"}}, http.StatusUnprocessableEntity, "must not contain"},
		{"the form's own address", url.Values{"name": {"new"}}, http.StatusUnprocessableEntity, "/projects/new"},
		{"a taken name", url.Values{"name": {"blog"}}, http.StatusConflict, "A project called blog already exists."},
		{"a display name of only spaces", url.Values{"name": {"docs"}, "displayName": {"   "}}, http.StatusOK, ""},
		{"a display name with a control character", url.Values{"name": {"docs"}, "displayName": {"a\x07b"}}, http.StatusUnprocessableEntity, "The display name must be printable text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.postForm(t, "/projects", tc.form)
			if tc.want == "" {
				// An empty display name falls back to the name: the project is made.
				if resp.StatusCode != http.StatusSeeOther {
					t.Fatalf("POST = %d, want 303:\n%s", resp.StatusCode, text(body))
				}
				return
			}
			if resp.StatusCode != tc.status || !strings.Contains(text(body), tc.want) {
				t.Errorf("POST = %d, want %d with %q:\n%s", resp.StatusCode, tc.status, tc.want, text(body))
			}
			if resp.StatusCode >= 300 && location(t, resp) != "" {
				t.Errorf("a refusal redirected to %q", location(t, resp))
			}
			// What was typed comes back, in the field.
			if v := tc.form.Get("name"); v != "" && !strings.Contains(body, `value="`+v+`"`) {
				t.Errorf("the form lost the name %q", v)
			}
		})
	}
}

func TestProjectPage(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, shopWeb("staging", "latest"))

	resp, body := e.get(t, "/projects/shop")
	if resp.StatusCode != http.StatusOK || h1Text(t, body) != "Shop Floor" {
		t.Fatalf("GET /projects/shop = %d, h1 %q", resp.StatusCode, h1Text(t, body))
	}
	got := text(body)
	for _, want := range []string{"production", "staging", "No apps in production yet.", "Move or delete its 1 app first."} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(body, `<a href="/apps/shop--staging--web" class="cv-app-name">web</a>`) {
		t.Errorf("staging doesn't list its app:\n%s", body)
	}
	// An environment with apps has its Delete off and says why; the empty one can be deleted.
	if f := formWith(t, body, "/projects/shop/environments/staging/delete"); len(f.Disabled) != 1 {
		t.Errorf("staging's Delete should be disabled: %+v", f)
	}
	if f := formWith(t, body, "/projects/shop/environments/production/delete"); len(f.Disabled) != 0 {
		t.Errorf("production's Delete should work: %+v", f)
	}
	for _, action := range []string{"/projects/shop", "/projects/shop/environments", "/projects/shop/delete"} {
		if f := formWith(t, body, action); f.Method != "post" || f.HTMX {
			t.Errorf("form %s = %+v, want a plain post", action, f)
		}
	}

	t.Run("Default offers neither delete", func(t *testing.T) {
		_, page := e.get(t, "/projects/default")
		if strings.Contains(page, "/delete") || strings.Contains(text(page), "Delete project") {
			t.Errorf("Default offers a delete:\n%s", text(page))
		}
		if !strings.Contains(text(page), "can't be deleted") {
			t.Errorf("Default's production doesn't say why it has no Delete:\n%s", text(page))
		}
	})

	t.Run("an unknown project", func(t *testing.T) {
		resp, body := e.get(t, "/projects/nope")
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "There's no project called nope.") {
			t.Errorf("GET = %d:\n%s", resp.StatusCode, text(body))
		}
	})
}

func TestProjectDisplayName(t *testing.T) {
	e := shopConsole(t)

	resp, _ := e.postForm(t, "/projects/shop", url.Values{"displayName": {"Shop & Co"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/projects/shop?saved=1" {
		t.Fatalf("POST = %d to %q", resp.StatusCode, location(t, resp))
	}
	_, page := e.get(t, "/projects/shop?saved=1")
	if h1Text(t, page) != "Shop & Co" || !strings.Contains(text(page), "Display name saved.") {
		t.Errorf("the page after saving:\n%s", text(page))
	}

	resp, body := e.postForm(t, "/projects/shop", url.Values{"displayName": {"  "}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "The display name must not be empty.") {
		t.Errorf("an empty display name = %d:\n%s", resp.StatusCode, text(body))
	}
	if p, _ := e.Svc.Project(context.Background(), "shop"); p.DisplayName != "Shop & Co" {
		t.Errorf("display name = %q after a refusal, want it kept", p.DisplayName)
	}
	if resp, _ := e.postForm(t, "/projects/nope", url.Values{"displayName": {"x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST to an unknown project = %d, want 404", resp.StatusCode)
	}
}

func TestAddAndDeleteEnvironments(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, shopWeb("staging", "latest"))

	resp, _ := e.postForm(t, "/projects/shop/environments", url.Values{"environment": {"qa"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/projects/shop" {
		t.Fatalf("add = %d to %q", resp.StatusCode, location(t, resp))
	}
	_, page := e.get(t, "/projects/shop")
	if !strings.Contains(text(page), "No apps in qa yet.") {
		t.Errorf("qa isn't listed:\n%s", text(page))
	}

	for _, tc := range []struct {
		name, value string
		status      int
		want        string
	}{
		{"empty", "", http.StatusUnprocessableEntity, "Enter a name for the environment."},
		{"taken", "qa", http.StatusConflict, "This project already has an environment called qa."},
		{"not a label", "Q A", http.StatusUnprocessableEntity, "The name is not a lowercase DNS label"},
	} {
		t.Run("add refused: "+tc.name, func(t *testing.T) {
			resp, body := e.postForm(t, "/projects/shop/environments", url.Values{"environment": {tc.value}})
			if resp.StatusCode != tc.status || !strings.Contains(text(body), tc.want) || location(t, resp) != "" {
				t.Errorf("add = %d (%q) %q:\n%s", resp.StatusCode, location(t, resp), tc.want, text(body))
			}
		})
	}

	t.Run("delete an empty environment", func(t *testing.T) {
		resp, _ := e.postForm(t, "/projects/shop/environments/qa/delete", nil)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/projects/shop" {
			t.Fatalf("delete = %d to %q", resp.StatusCode, location(t, resp))
		}
		if p, _ := e.Svc.Project(context.Background(), "shop"); len(p.Environments) != 2 {
			t.Errorf("environments = %+v, want qa gone", p.Environments)
		}
		// Again: it is gone, which is what was asked for.
		if resp, _ := e.postForm(t, "/projects/shop/environments/qa/delete", nil); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("delete of a gone environment = %d, want 303", resp.StatusCode)
		}
	})

	t.Run("an environment with apps is refused with the list", func(t *testing.T) {
		resp, body := e.postForm(t, "/projects/shop/environments/staging/delete", nil)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Can't delete staging: it still has 1 app: shop--staging--web.") {
			t.Errorf("delete = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("Default's production can't be deleted", func(t *testing.T) {
		resp, body := e.postForm(t, "/projects/default/environments/production/delete", nil)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Can't delete production") {
			t.Errorf("delete = %d:\n%s", resp.StatusCode, text(body))
		}
	})
}

func TestDeleteProject(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, shopWeb("staging", "latest"))

	t.Run("the name must be typed", func(t *testing.T) {
		resp, body := e.postForm(t, "/projects/shop/delete", url.Values{"confirm": {"other"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Type shop to confirm.") ||
			!strings.Contains(body, `<details class="cv-confirm" open>`) {
			t.Errorf("delete = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("a project with apps is refused, naming them", func(t *testing.T) {
		resp, body := e.postForm(t, "/projects/shop/delete", url.Values{"confirm": {"shop"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This project still has apps") ||
			!strings.Contains(body, `href="/apps/shop--staging--web"`) {
			t.Errorf("delete = %d:\n%s", resp.StatusCode, text(body))
		}
		if _, err := e.Svc.Project(context.Background(), "shop"); err != nil {
			t.Errorf("the project was deleted: %v", err)
		}
	})

	t.Run("an empty project is deleted", func(t *testing.T) {
		if _, err := e.Svc.CreateProject(context.Background(), "scratch", ""); err != nil {
			t.Fatal(err)
		}
		resp, _ := e.postForm(t, "/projects/scratch/delete", url.Values{"confirm": {"scratch"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps?projectDeleted=scratch" {
			t.Fatalf("delete = %d to %q", resp.StatusCode, location(t, resp))
		}
		_, page := e.get(t, location(t, resp))
		if !strings.Contains(text(page), "Project scratch was deleted.") {
			t.Errorf("Apps doesn't say so:\n%s", text(page))
		}
		// Only a name is echoed.
		_, spoof := e.get(t, "/apps?projectDeleted="+url.QueryEscape("<b>x</b>"))
		if strings.Contains(spoof, "<b>x</b>") || strings.Contains(text(spoof), "was deleted") {
			t.Error("Apps echoed something that isn't a project name")
		}
	})

	t.Run("Default is never deleted", func(t *testing.T) {
		resp, body := e.postForm(t, "/projects/default/delete", url.Values{"confirm": {"default"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "can't be deleted") {
			t.Errorf("delete = %d:\n%s", resp.StatusCode, text(body))
		}
	})
}

func TestNewAppPlace(t *testing.T) {
	t.Run("hidden while only Default's production exists", func(t *testing.T) {
		e := newConsoleEnv(t)
		for _, path := range []string{"/apps/new", "/apps/new?from=compose"} {
			_, body := e.get(t, path)
			if strings.Contains(body, `name="place"`) {
				t.Errorf("GET %s offers a place select with one environment", path)
			}
		}
	})

	e := shopConsole(t)

	t.Run("both tabs offer the environments, Default's production by default", func(t *testing.T) {
		for _, path := range []string{"/apps/new", "/apps/new?from=compose"} {
			_, body := e.get(t, path)
			for _, want := range []string{
				`<option value="default/production" selected>Default › production</option>`,
				`<option value="shop/production">Shop Floor › production</option>`,
				`<option value="shop/staging">Shop Floor › staging</option>`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("GET %s lacks %s:\n%s", path, want, body)
				}
			}
		}
	})

	t.Run("the query chooses, and the tabs keep the choice", func(t *testing.T) {
		_, body := e.get(t, "/apps/new?project=shop&environment=staging")
		if !strings.Contains(body, `<option value="shop/staging" selected>`) || strings.Contains(body, `<option value="default/production" selected>`) {
			t.Errorf("the staging option isn't the chosen one:\n%s", body)
		}
		if !strings.Contains(body, `href="/apps/new?from=compose&amp;project=shop&amp;environment=staging"`) {
			t.Errorf("the Compose tab dropped the choice:\n%s", body)
		}
		_, compose := e.get(t, "/apps/new?from=compose&project=shop&environment=staging")
		if !strings.Contains(compose, `<option value="shop/staging" selected>`) || !strings.Contains(compose, `href="/apps/new?project=shop&amp;environment=staging"`) {
			t.Errorf("the Compose tab lost the choice:\n%s", compose)
		}
		// A place that doesn't exist falls back to Default's production.
		_, nowhere := e.get(t, "/apps/new?project=shop&environment=nowhere")
		if !strings.Contains(nowhere, `<option value="default/production" selected>`) {
			t.Error("an unknown place isn't ignored")
		}
	})

	t.Run("the app is created in the chosen environment under the computed ID", func(t *testing.T) {
		form := postedForm("web", "traefik/whoami:latest", "80", "", "/")
		form.Set("place", "shop/staging")
		resp, body := e.postForm(t, "/apps", form)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop--staging--web/deployments" {
			t.Fatalf("POST /apps = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		rec, err := e.Svc.GetApp(context.Background(), "shop--staging--web")
		if err != nil || rec.Place != (app.Place{Project: "shop", Environment: "staging", Name: "web"}) {
			t.Errorf("stored app = %+v, %v", rec, err)
		}
	})

	t.Run("no select posted means Default's production", func(t *testing.T) {
		resp, _ := e.postForm(t, "/apps", postedForm("plain", "traefik/whoami:latest", "80", "", "/"))
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/plain/deployments" {
			t.Errorf("POST = %d to %q", resp.StatusCode, location(t, resp))
		}
	})

	t.Run("a name that is taken there is refused on Name, naming where", func(t *testing.T) {
		form := postedForm("web", "traefik/whoami:latest", "80", "", "/")
		form.Set("place", "shop/staging")
		resp, body := e.postForm(t, "/apps", form)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "An app called web already exists in shop/staging.") {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
		// The same name in another environment is free.
		form.Set("place", "shop/production")
		if resp, _ := e.postForm(t, "/apps", form); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("the same name in production = %d, want 303", resp.StatusCode)
		}
	})

	t.Run("a place that isn't listed is refused on the select", func(t *testing.T) {
		form := postedForm("other", "traefik/whoami:latest", "80", "", "/")
		form.Set("place", "shop/nowhere")
		resp, body := e.postForm(t, "/apps", form)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Choose one of the environments in the list.") ||
			!strings.Contains(body, `id="place-error"`) {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
		if _, err := e.Svc.GetApp(context.Background(), "other"); err == nil {
			t.Error("an app was created")
		}
	})

	t.Run("a name too long once placed gets the name's message", func(t *testing.T) {
		form := postedForm(strings.Repeat("a", 31), "traefik/whoami:latest", "80", "", "/")
		form.Set("place", "shop/staging")
		resp, body := e.postForm(t, "/apps", form)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="name-error"`) {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
	})
}

func TestNewComposeAppPlace(t *testing.T) {
	e := shopConsole(t)
	file := "services:\n  db:\n    image: traefik/whoami:latest\n"

	resp, body := e.postForm(t, "/apps/new/compose/preview", url.Values{"name": {"stack"}, "place": {"shop/staging"}, "compose": {file}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<option value="shop/staging" selected>`) {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/apps/new/compose", url.Values{"name": {"stack"}, "place": {"shop/staging"}, "compose": {file}, "svc.db.overlay": {"1"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/shop--staging--stack/deployments") {
		t.Fatalf("create = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	if rec, err := e.Svc.GetApp(context.Background(), "shop--staging--stack"); err != nil || rec.Place.Environment != "staging" {
		t.Errorf("stored app = %+v, %v", rec, err)
	}

	resp, body = e.postForm(t, "/apps/new/compose/preview", url.Values{"name": {"stack"}, "place": {"shop/staging"}, "compose": {file}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "An app called stack already exists in shop/staging.") {
		t.Errorf("a taken name = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/apps/new/compose/preview", url.Values{"name": {"stack"}, "place": {"shop/nowhere"}, "compose": {file}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Choose one of the environments in the list.") {
		t.Errorf("a place that isn't listed = %d:\n%s", resp.StatusCode, text(body))
	}
}

// crumbLine is the top bar's breadcrumbs of a page, as the text a reader sees.
var crumbLine = regexp.MustCompile(`(?s)<nav aria-label="Breadcrumb".*?</nav>`)

func TestAppHeaderNamesPlaceAndOtherEnvironments(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, shopWeb("staging", "latest"))

	// The place is in the top bar: project / environment. The app's name is the h1, not a crumb.
	_, body := e.get(t, "/apps/shop--staging--web")
	where := text(crumbLine.FindString(body))
	if where != "Shop Floor /staging" {
		t.Errorf("the crumbs = %q, want the project and the environment", where)
	}
	if !strings.Contains(body, `<a href="/projects/shop" class="cv-crumb">Shop Floor</a>`) {
		t.Error("the project doesn't link to its page")
	}
	if strings.Contains(body, "cv-where") {
		t.Error("the header still has its place line")
	}
	if h1Text(t, body) != "web" {
		t.Errorf("headline = %q, want the app's name", h1Text(t, body))
	}
	got := text(body)
	for _, want := range []string{"Environments", "production", "Copy to production"} {
		if !strings.Contains(got, want) {
			t.Errorf("the header lacks %q:\n%s", want, got)
		}
	}
	f := formWith(t, body, "/apps/shop--staging--web/copy")
	if f.Method != "post" || f.Values.Get("to") != "production" {
		t.Errorf("the Copy form = %+v", f)
	}
	// The header region refreshes with the same line.
	_, part := e.get(t, "/parts/apps/shop--staging--web/header")
	if !strings.Contains(part, "Copy to production") || strings.Contains(part, "cv-where") {
		t.Errorf("the header part differs:\n%s", text(part))
	}

	t.Run("where the app exists there is a link and Promote", func(t *testing.T) {
		e.deploy(t, shopWeb("production", "latest"))
		_, body := e.get(t, "/apps/shop--staging--web")
		for _, want := range []string{`<a href="/apps/shop--production--web" class="cv-data">production</a>`,
			`<a href="/apps/shop--staging--web/promote?to=production" class="cv-btn cv-btn-secondary cv-btn-sm">Promote to production</a>`} {
			if !strings.Contains(body, want) {
				t.Errorf("the header lacks %s:\n%s", want, body)
			}
		}
		if strings.Contains(body, "Copy to production") {
			t.Error("the header offers a copy to where the app is")
		}
	})

	t.Run("an app at home has its crumbs always, its Environments line only when there is another environment", func(t *testing.T) {
		single := newConsoleEnv(t)
		single.deploy(t, whoamiApp())
		_, body := single.get(t, "/apps/whoami")
		if text(crumbLine.FindString(body)) != "Default /production" || strings.Contains(body, "cv-envs") {
			t.Errorf("a flat installation's app: crumbs %q, want Default /production and no Environments line", text(crumbLine.FindString(body)))
		}
		e.deploy(t, whoamiApp())
		_, body = e.get(t, "/apps/whoami")
		if text(crumbLine.FindString(body)) != "Default /production" {
			t.Errorf("the crumbs = %q", text(crumbLine.FindString(body)))
		}
		if strings.Contains(body, "cv-envs") {
			t.Error("Default has one environment: nothing to promote or copy to")
		}
	})
}

func TestCopyApp(t *testing.T) {
	e := shopConsole(t)
	const sentinel = "s3cret-sentinel-019"
	a := shopWeb("staging", "latest")
	a.Services[0].Secrets = []app.Secret{{Name: "DB_URL", Value: new(sentinel)}, {Name: "API_KEY", Value: new(sentinel)}}
	e.deploy(t, a)

	resp, body := e.postForm(t, "/apps/shop--staging--web/copy", url.Values{"to": {"production"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("copy = %d:\n%s", resp.StatusCode, text(body))
	}
	loc := location(t, resp)
	if !strings.HasPrefix(loc, "/apps/shop--production--web/configure?") {
		t.Fatalf("copy went to %q, want the copy's Configure", loc)
	}
	_, page := e.get(t, loc)
	got := text(page)
	if !strings.Contains(got, "Copied to production. Set its domains and these secrets before deploying: web/API_KEY, web/DB_URL.") &&
		!strings.Contains(got, "Copied to production. Set its domains and these secrets before deploying: web/DB_URL, web/API_KEY.") {
		t.Errorf("the notice is missing or wrong:\n%s", got)
	}
	rec, err := e.Svc.GetApp(context.Background(), "shop--production--web")
	if err != nil || len(rec.App.Services[0].Domains) != 0 {
		t.Errorf("copy = %+v, %v: want it stored without domains", rec, err)
	}
	if recs := e.deployments(t, "shop--production--web"); len(recs) != 0 {
		t.Errorf("the copy was deployed: %+v", recs)
	}

	t.Run("a notice only says what the stored app confirms", func(t *testing.T) {
		_, spoof := e.get(t, "/apps/shop--production--web/configure?copied=staging&missing=web/NOPE")
		if strings.Contains(text(spoof), "Copied to") {
			t.Error("a copied= that isn't the app's environment shows a notice")
		}
		_, spoof = e.get(t, "/apps/shop--production--web/configure?copied=production&missing=web/NOPE&missing=db/DB_URL")
		if strings.Contains(text(spoof), "NOPE") || strings.Contains(text(spoof), "db/DB_URL") {
			t.Errorf("the notice names secrets the app doesn't have:\n%s", text(spoof))
		}
		if !strings.Contains(text(spoof), "Copied to production. Set its domains before deploying.") {
			t.Errorf("the notice without secrets:\n%s", text(spoof))
		}
		_, plain := e.get(t, "/apps/shop--production--web/configure")
		if strings.Contains(text(plain), "Copied to") {
			t.Error("Configure says the app was copied without being asked")
		}
	})

	t.Run("a second copy is refused", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/shop--staging--web/copy", url.Values{"to": {"production"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Promote it from staging instead of copying.") {
			t.Errorf("copy = %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("the same environment, an unknown one, an unknown app", func(t *testing.T) {
		for _, tc := range []struct {
			id, to string
			status int
		}{
			{"shop--staging--web", "staging", http.StatusUnprocessableEntity},
			{"shop--staging--web", "nowhere", http.StatusNotFound},
			{"shop--staging--web", "", http.StatusUnprocessableEntity},
			{"nope", "production", http.StatusNotFound},
		} {
			resp, _ := e.postForm(t, "/apps/"+tc.id+"/copy", url.Values{"to": {tc.to}})
			if resp.StatusCode != tc.status {
				t.Errorf("copy %s to %q = %d, want %d", tc.id, tc.to, resp.StatusCode, tc.status)
			}
		}
	})

	t.Run("no page shows a secret value", func(t *testing.T) {
		for _, path := range []string{loc, "/apps/shop--production--web", "/apps/shop--staging--web/configure", "/apps/shop--staging--web/deployments",
			"/apps/shop--staging--web/promote?to=production", "/projects/shop", "/apps", "/parts/apps"} {
			if _, body := e.get(t, path); strings.Contains(body, sentinel) {
				t.Errorf("GET %s shows a secret value", path)
			}
		}
	})
}

func TestPromotePage(t *testing.T) {
	e := shopConsole(t)
	e.Reg.AddIndex("traefik/whoami", "1.0", otherDigest)
	e.deploy(t, shopWeb("production", "1.0"))
	e.deploy(t, shopWeb("staging", "latest"))

	resp, body := e.get(t, "/apps/shop--staging--web/promote?to=production")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET promote = %d:\n%s", resp.StatusCode, text(body))
	}
	got := text(body)
	for _, want := range []string{
		"Promote web from staging (deployment v0) to production",
		"traefik/whoami:1.0 → traefik/whoami:latest",
		"sha256:0123456789ab → sha256:c4717a8d1f01",
		"production keeps its env, secrets, domains, memory, copies and backups.",
		"Promote to production",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the preview lacks %q:\n%s", want, got)
		}
	}
	f := formWith(t, body, "/apps/shop--staging--web/promote")
	prod, _ := e.Svc.GetApp(context.Background(), "shop--production--web")
	if f.Method != "post" || f.Values.Get("to") != "production" || f.Values.Get("revision") != prod.Revision || f.HTMX {
		t.Errorf("the form = %+v, want a plain post of to and the target's revision %s", f, prod.Revision)
	}

	t.Run("the form deploys and lands on the target's Deployments tab", func(t *testing.T) {
		resp, body := e.submit(t, f, nil)
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/shop--production--web/deployments#deployment-") {
			t.Fatalf("POST promote = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		recs := e.deployments(t, "shop--production--web")
		if len(recs) != 2 || recs[0].SourceApp != "shop--staging--web" || recs[0].Deployment.Trigger != app.TriggerConsole {
			t.Fatalf("deployments = %+v, want a promotion first", recs)
		}
		if got := e.storedApp(t, "shop--production--web").Services[0].Image; got != "traefik/whoami:latest" {
			t.Errorf("production runs %q after the promotion", got)
		}

		_, tab := e.get(t, "/apps/shop--production--web/deployments")
		srcDep := e.deployments(t, "shop--staging--web")[0].Deployment.ID
		want := `<a href="/apps/shop--staging--web/deployments#deployment-` + srcDep + `">Promoted from <span class="cv-inline-mono">staging</span> · deployment <span class="cv-inline-mono">` + shortID(srcDep) + `</span></a>`
		if !strings.Contains(tab, want) {
			t.Errorf("the history entry lacks %s:\n%s", want, tab)
		}
		if strings.Contains(text(tab), "Redeploy of") {
			t.Error("a promotion is described as a redeploy")
		}
	})

	t.Run("a target that changed since the preview is the page again", func(t *testing.T) {
		// The form was drawn before the promotion above: its revision is stale now.
		resp, body := e.submit(t, f, nil)
		if resp.StatusCode != http.StatusPreconditionFailed || !strings.Contains(text(body), "production changed since this preview; review it again.") {
			t.Fatalf("stale post = %d:\n%s", resp.StatusCode, text(body))
		}
		fresh := formWith(t, body, "/apps/shop--staging--web/promote")
		now, _ := e.Svc.GetApp(context.Background(), "shop--production--web")
		if fresh.Values.Get("revision") != now.Revision || fresh.Values.Get("revision") == f.Values.Get("revision") {
			t.Errorf("the new form carries revision %q, want the current %q", fresh.Values.Get("revision"), now.Revision)
		}
	})
}

func TestPromoteRefusals(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, shopWeb("staging", "latest"))
	a := shopWeb("staging", "latest")
	a.ID = "shop--staging--sick"
	e.seed(t, a, seedDeploy{ID: "bad1", Trigger: app.TriggerManual, Status: store.StatusFailed, Error: "boom"})
	never := shopWeb("staging", "latest")
	never.ID = "shop--staging--never"
	e.seed(t, never)

	tests := []struct {
		name, path string
		status     int
		want       string
		copy       bool
	}{
		{"the target has no such app", "/apps/shop--staging--web/promote?to=production", http.StatusNotFound,
			"production has no app called web. Copy it there first.", true},
		{"no target at all", "/apps/shop--staging--web/promote", http.StatusUnprocessableEntity, "Choose an environment to promote to", false},
		{"the same environment", "/apps/shop--staging--web/promote?to=staging", http.StatusUnprocessableEntity,
			"web is in staging already. Choose another environment.", false},
		{"an unknown environment", "/apps/shop--staging--web/promote?to=nowhere", http.StatusNotFound, "There's no environment nowhere in this project.", false},
		{"not healthy", "/apps/shop--staging--sick/promote?to=production", http.StatusConflict, "isn't healthy, so it can't be promoted", false},
		{"never deployed", "/apps/shop--staging--never/promote?to=production", http.StatusConflict, "has no healthy deployment in staging yet", false},
		{"an app from before projects has no counterpart", "/apps/whoami/promote?to=staging", http.StatusNotFound, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.get(t, tc.path)
			if resp.StatusCode != tc.status {
				t.Fatalf("GET %s = %d, want %d:\n%s", tc.path, resp.StatusCode, tc.status, text(body))
			}
			if tc.want == "" {
				return
			}
			got := text(body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("the page lacks %q:\n%s", tc.want, got)
			}
			// The refusal takes the button's place.
			if strings.Contains(got, "Promote to ") {
				t.Errorf("a refused preview still offers the button:\n%s", got)
			}
			if has := strings.Contains(body, `action="/apps/shop--staging--web/copy"`); has != tc.copy {
				t.Errorf("Copy offered = %v, want %v", has, tc.copy)
			}
		})
	}

	t.Run("a refused post says why, with the preview kept when it still holds", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/shop--staging--sick/promote", url.Values{"to": {"production"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "isn't healthy") {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
		resp, _ = e.postForm(t, "/apps/nope/promote", url.Values{"to": {"production"}})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown app = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("the copy offered works, and then the preview shows", func(t *testing.T) {
		resp, _ := e.postForm(t, "/apps/shop--staging--web/copy", url.Values{"to": {"production"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("copy = %d", resp.StatusCode)
		}
		resp, body := e.get(t, "/apps/shop--staging--web/promote?to=production")
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "Promote to production") {
			t.Errorf("preview after the copy = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("a promotion that fails a rule shows the field", func(t *testing.T) {
		// Production runs two copies; staging's shape has a volume, which one copy only may mount.
		e := shopConsole(t)
		prod := shopWeb("production", "latest")
		prod.Services[0].Replicas = new(2)
		e.deploy(t, prod)
		st := shopWeb("staging", "latest")
		st.Services[0].Volumes, st.Services[0].Replicas = []app.Volume{{Name: "data", Path: "/data"}}, new(1)
		e.deploy(t, st)
		resp, body := e.get(t, "/apps/shop--staging--web/promote?to=production")
		got := text(body)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(got, "caravana can't deploy web in production: services[0]") {
			t.Errorf("GET = %d:\n%s", resp.StatusCode, got)
		}
	})
}

// projectPages are the pages of projects and environments for the checks every
// page passes (accessibility, policy, no outside hosts): the grouped Apps page,
// the project forms and their refusals, an app's place and Environments line, and
// the promote preview with its refusals.
func projectPages(t *testing.T) map[string]string {
	t.Helper()
	e := shopConsole(t)
	e.Reg.AddIndex("traefik/whoami", "1.0", otherDigest)
	e.deploy(t, shopWeb("production", "1.0"))
	e.deploy(t, shopWeb("staging", "latest"))
	st := shopWeb("staging", "latest")
	st.ID = "shop--staging--solo"
	e.deploy(t, st)

	pages := map[string]string{}
	get := func(path string) {
		t.Helper()
		resp, body := e.get(t, path)
		if resp.StatusCode >= 500 || !strings.Contains(body, "<") {
			t.Fatalf("GET %s = %d:\n%s", path, resp.StatusCode, body)
		}
		pages["projects: "+path] = body
	}
	post := func(name, path string, form url.Values) {
		t.Helper()
		resp, body := e.postForm(t, path, form)
		if resp.StatusCode >= 500 || !strings.Contains(body, "<html") {
			t.Fatalf("POST %s = %d:\n%s", path, resp.StatusCode, body)
		}
		pages["projects: "+name] = body
	}
	for _, path := range []string{"/apps", "/parts/apps", "/projects/new", "/projects/shop", "/projects/default", "/projects/nope",
		"/apps/new", "/apps/new?from=compose", "/apps/new?project=shop&environment=staging",
		"/apps/shop--staging--web", "/apps/shop--staging--web/deployments", "/parts/apps/shop--staging--web/header",
		"/apps/shop--staging--web/promote?to=production", "/apps/shop--staging--web/promote?to=staging",
		"/apps/shop--staging--solo/promote?to=production", "/apps/shop--staging--web/promote"} {
		get(path)
	}
	post("new project, errors", "/projects", url.Values{"name": {"Bad Name"}})
	post("display name, errors", "/projects/shop", url.Values{"displayName": {" "}})
	post("environment, errors", "/projects/shop/environments", url.Values{"environment": {"staging"}})
	post("delete environment, refused", "/projects/shop/environments/staging/delete", nil)
	post("delete project, refused", "/projects/shop/delete", url.Values{"confirm": {"shop"}})
	post("delete project, mismatch", "/projects/shop/delete", url.Values{"confirm": {"x"}})
	post("new app, bad place", "/apps", url.Values{"name": {"x"}, "image": {"y"}, "place": {"shop/none"}})
	return pages
}

func TestProjectPagesAreRoutedLikeTheRest(t *testing.T) {
	e := shopConsole(t)
	for _, path := range []string{"/projects/new", "/projects/shop"} {
		resp, _ := e.do(t, request{path: path})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login?next=") {
			t.Errorf("GET %s without a session = %d to %q, want the sign-in page", path, resp.StatusCode, location(t, resp))
		}
	}
	// Posts are refused when the browser says they come from another site, before anything changes.
	for _, path := range []string{"/projects", "/projects/shop", "/projects/shop/delete", "/projects/shop/environments",
		"/projects/shop/environments/staging/delete", "/apps/web/promote", "/apps/web/copy"} {
		resp, _ := e.do(t, request{method: http.MethodPost, path: path, form: url.Values{"name": {"x"}, "confirm": {"shop"}}, cookies: []*http.Cookie{e.Cookie},
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("cross-site POST %s = %d, want 403", path, resp.StatusCode)
		}
	}
	if p, err := e.Svc.Project(context.Background(), "shop"); err != nil || len(p.Environments) != 2 {
		t.Errorf("a refused post changed the project: %+v, %v", p, err)
	}
}
