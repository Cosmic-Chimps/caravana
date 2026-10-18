package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// createOnly is a posted New app form with the "Create without deploying" button.
func createOnly(form url.Values) url.Values {
	form.Set("action", "create")
	return form
}

func TestNewAppOffersTwoButtons(t *testing.T) {
	e := newConsoleEnv(t)
	_, body := e.get(t, "/apps/new")
	for _, want := range []string{
		`<button type="submit" name="action" value="deploy" class="cv-btn cv-btn-primary">Create and deploy</button>`,
		`<button type="submit" name="action" value="create" class="cv-btn cv-btn-secondary">Create without deploying</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q:\n%s", want, body)
		}
	}
	// The image may be left out when the app is only created, so the browser must not insist.
	if strings.Contains(fieldBlock(t, body, "image"), " required") {
		t.Error("the image input is required by the browser")
	}
}

func TestCreateWithoutDeployingFromTheConsole(t *testing.T) {
	t.Run("a definition is saved, nothing deploys, the page says how to start it", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps", createOnly(whoamiForm()))
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami?created=1" {
			t.Fatalf("POST /apps = %d to %q, want 303 to /apps/whoami?created=1:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if got := e.storedApp(t, "whoami"); !got.Defined() || got.Services[0].Image != "traefik/whoami:latest" ||
			got.Services[0].Ports[0].ContainerPort != 80 || got.Services[0].Domains[0].Host != "whoami.example.com" {
			t.Errorf("stored app = %+v, want the typed definition", got)
		}
		if n := len(e.deployments(t, "whoami")); n != 0 || e.Nomad.Registrations() != 0 {
			t.Errorf("%d deployments, %d registrations; want none", n, e.Nomad.Registrations())
		}

		_, page := e.get(t, location(t, resp))
		got := text(page)
		for _, want := range []string{
			"Created whoami. Nothing is deployed yet.",
			"Waiting for its first deploy.",
			"Deploy now",
			"Create a webhook token",
			`/v1/apps/whoami/deployments`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q: %s", want, got)
			}
		}
		for _, want := range []string{`action="/apps/whoami/deploy"`, `href="/apps/whoami/configure#webhook"`} {
			if !strings.Contains(page, want) {
				t.Errorf("the page lacks %q:\n%s", want, page)
			}
		}
		if strings.Contains(got, "No copies are running") {
			t.Errorf("the page says copies aren't running instead of waiting: %s", got)
		}

		_, plainPage := e.get(t, "/apps/whoami")
		if strings.Contains(text(plainPage), "Created whoami.") {
			t.Error("the created notice shows without ?created=1")
		}
	})

	t.Run("a name only is saved with no services, and the page asks for a definition", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps", createOnly(postedForm("shop", "", "", "", "/")))
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop?created=1" {
			t.Fatalf("POST /apps = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if got := e.storedApp(t, "shop"); got.Defined() || got.Services == nil {
			t.Errorf("stored app = %+v, want an app with an empty service list", got)
		}
		if n := len(e.deployments(t, "shop")); n != 0 || e.Nomad.Registrations() != 0 {
			t.Errorf("%d deployments, %d registrations; want none", n, e.Nomad.Registrations())
		}

		_, page := e.get(t, location(t, resp))
		got := text(page)
		for _, want := range []string{"Created shop. Nothing is deployed yet.", "No definition yet.", "Define it in Configure", "Deploy from Compose", "app.json"} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q: %s", want, got)
			}
		}
		if strings.Contains(got, "Deploy now") || strings.Contains(page, `action="/apps/shop/deploy"`) {
			t.Errorf("the page offers Deploy now for an app with nothing to deploy: %s", got)
		}
	})

	t.Run("a port or a domain without an image is refused, not dropped", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps", createOnly(postedForm("shop", "", "80", "", "/")))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, body)
		}
		if got := text(fieldBlock(t, body, "image")); !strings.Contains(got, "Add the image the port and domain belong to") {
			t.Errorf("the image field says: %s", got)
		}
		if _, err := e.Store.GetApp(context.Background(), "shop"); err == nil {
			t.Error("the app was saved")
		}
	})

	t.Run("a name is still needed, a taken one is refused", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps", createOnly(postedForm("", "", "", "", "/")))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(fieldBlock(t, body, "name")), "Enter a name") {
			t.Errorf("no name = %d:\n%s", resp.StatusCode, text(body))
		}
		if _, err := e.Svc.CreateAppOnly(context.Background(), app.App{ID: "shop"}); err != nil {
			t.Fatal(err)
		}
		resp, body = e.postForm(t, "/apps", createOnly(postedForm("shop", "", "", "", "/")))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(fieldBlock(t, body, "name")), "shop") {
			t.Errorf("taken name = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("create and deploy without an image is refused with the way out", func(t *testing.T) {
		e := newConsoleEnv(t)
		form := postedForm("shop", "", "", "", "/")
		form.Set("action", "deploy")
		resp, body := e.postForm(t, "/apps", form)
		if resp.StatusCode != http.StatusUnprocessableEntity ||
			!strings.Contains(text(fieldBlock(t, body, "image")), "Add an image to deploy now, or create the app without deploying.") {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("Deploy now deploys a defined app as stored, and an undefined one is refused", func(t *testing.T) {
		e := newConsoleEnv(t)
		if resp, body := e.postForm(t, "/apps", createOnly(whoamiForm())); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("create = %d:\n%s", resp.StatusCode, body)
		}
		resp, body := e.postForm(t, "/apps/whoami/deploy", url.Values{})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("Deploy now = %d:\n%s", resp.StatusCode, body)
		}
		if recs := e.deployments(t, "whoami"); len(recs) != 1 || recs[0].Deployment.Trigger != app.TriggerConsole {
			t.Errorf("deployments = %+v, want one from the console", recs)
		}

		if _, err := e.Svc.CreateAppOnly(context.Background(), app.App{ID: "shop"}); err != nil {
			t.Fatal(err)
		}
		resp, body = e.postForm(t, "/apps/shop/deploy", url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app has no definition yet") {
			t.Errorf("Deploy now on an undefined app = %d:\n%s", resp.StatusCode, text(body))
		}
		if n := len(e.deployments(t, "shop")); n != 0 {
			t.Errorf("%d deployments of shop, want none", n)
		}
	})
}

func TestAppListShowsAppsThatWereNeverDeployed(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp())
	if _, err := e.Svc.CreateAppOnly(context.Background(), app.App{ID: "shop"}); err != nil {
		t.Fatal(err)
	}
	_, body := e.get(t, "/apps")
	got := text(body)
	for _, want := range []string{"never deployed", "not defined yet", "2 apps waiting for a first deploy."} {
		if !strings.Contains(got, want) {
			t.Errorf("the list lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "needs you:") || strings.Contains(got, "need you:") {
		t.Errorf("the list calls a created app trouble: %s", got)
	}
}

func TestConfigureDefinesAnAppWithNoDefinition(t *testing.T) {
	ctx := context.Background()
	// shop is the app made by name only; its Configure tab holds the form that defines it.
	setup := func(t *testing.T) (*consoleEnv, string) {
		t.Helper()
		e := newConsoleEnv(t)
		rec, err := e.Svc.CreateAppOnly(ctx, app.App{ID: "shop"})
		if err != nil {
			t.Fatal(err)
		}
		return e, rec.Revision
	}
	define := func(revision, image, port string) url.Values {
		return url.Values{"revision": {revision}, "image": {image}, "port": {port}, "domain": {""}, "healthPath": {"/"}}
	}

	t.Run("the tab holds the Define form, not the editor", func(t *testing.T) {
		e, revision := setup(t)
		resp, body := e.get(t, "/apps/shop/configure")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d:\n%s", resp.StatusCode, body)
		}
		for _, want := range []string{
			`action="/apps/shop/define"`, `name="revision" value="` + revision + `"`, `name="image"`, `name="port"`, `name="domain"`, `name="healthPath"`,
			"Define and deploy", `id="webhook"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the tab lacks %q:\n%s", want, body)
			}
		}
		got := text(body)
		for _, want := range []string{"Define the app", "Deploy from Compose", "A secret belongs to a service", "refused until the app has a definition"} {
			if !strings.Contains(got, want) {
				t.Errorf("the tab lacks %q: %s", want, got)
			}
		}
		if strings.Contains(body, `action="/apps/shop/secrets"`) || strings.Contains(body, `action="/apps/shop/configure"`) {
			t.Error("the tab offers the secret or the per-service edit form for an app with no services")
		}
		if !strings.Contains(got, "It was never deployed, so nothing is running.") {
			t.Errorf("the delete section talks of stopping copies: %s", got)
		}
	})

	t.Run("defining saves and deploys, with the revision", func(t *testing.T) {
		e, revision := setup(t)
		resp, body := e.postForm(t, "/apps/shop/define", define(revision, "traefik/whoami:latest", "80"))
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/shop/deployments#deployment-") {
			t.Fatalf("POST = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
		}
		a := e.storedApp(t, "shop")
		if !a.Defined() || a.Services[0].Name != "shop" || a.Services[0].Image != "traefik/whoami:latest" ||
			a.Services[0].Ports[0].ContainerPort != 80 || a.Source.Kind != app.SourceImage {
			t.Errorf("stored app = %+v, want one service named shop on whoami:80", a)
		}
		if recs := e.deployments(t, "shop"); len(recs) != 1 || recs[0].Deployment.Trigger != app.TriggerConsole {
			t.Errorf("deployments = %+v, want one from the console", recs)
		}
		_, page := e.get(t, "/apps/shop/configure")
		if strings.Contains(text(page), "Define the app") {
			t.Error("the tab still offers the Define form for an app that has a definition")
		}
	})

	t.Run("an empty image is refused on its field, nothing is saved or deployed", func(t *testing.T) {
		e, revision := setup(t)
		resp, body := e.postForm(t, "/apps/shop/define", define(revision, "", "80"))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
		if got := text(fieldBlock(t, body, "image")); !strings.Contains(got, "Add an image to deploy now") || !strings.Contains(fieldBlock(t, body, "image"), `aria-invalid="true"`) {
			t.Errorf("the image field says: %s", got)
		}
		if !strings.Contains(fieldBlock(t, body, "port"), `value="80"`) {
			t.Error("what was typed is not kept")
		}
		if e.storedApp(t, "shop").Defined() || len(e.deployments(t, "shop")) != 0 {
			t.Error("a refused define saved or deployed something")
		}
	})

	t.Run("a stale revision, a missing one and an app that is defined already are refused", func(t *testing.T) {
		e, revision := setup(t)
		resp, body := e.postForm(t, "/apps/shop/define", define("0000000000000000", "traefik/whoami:latest", ""))
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The app changed since this page was drawn") {
			t.Errorf("stale = %d: %s", resp.StatusCode, text(body))
		}
		resp, body = e.postForm(t, "/apps/shop/define", define("", "traefik/whoami:latest", ""))
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(text(body), "no revision") {
			t.Errorf("no revision = %d: %s", resp.StatusCode, text(body))
		}
		if e.storedApp(t, "shop").Defined() || len(e.deployments(t, "shop")) != 0 {
			t.Error("a refused define saved or deployed something")
		}

		if resp, body := e.postForm(t, "/apps/shop/define", define(revision, "traefik/whoami:latest", "")); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("define = %d:\n%s", resp.StatusCode, text(body))
		}
		rec, _ := e.Store.GetApp(ctx, "shop")
		resp, body = e.postForm(t, "/apps/shop/define", define(rec.Revision, "traefik/whoami:latest", ""))
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app has a definition now") {
			t.Errorf("defined already = %d: %s", resp.StatusCode, text(body))
		}
	})

	t.Run("an app that doesn't exist is the not-found page", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, _ := e.postForm(t, "/apps/ghost/define", define("0123456789abcdef", "traefik/whoami:latest", ""))
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST = %d, want 404", resp.StatusCode)
		}
	})
}

func TestDeploymentsTabOfAnAppWithNoDefinition(t *testing.T) {
	e := newConsoleEnv(t)
	if _, err := e.Svc.CreateAppOnly(context.Background(), app.App{ID: "shop"}); err != nil {
		t.Fatal(err)
	}
	resp, body := e.get(t, "/apps/shop/deployments")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, body)
	}
	got := text(body)
	if !strings.Contains(got, "This app has no definition yet, so there is nothing to deploy.") || strings.Contains(body, `action="/apps/shop/deploy"`) {
		t.Errorf("the tab offers a deploy for an app with no definition: %s", got)
	}
}

func TestWebhookNoteOfANeverDeployedApp(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp())
	_, body := e.get(t, "/apps/whoami/configure")
	if got := text(body); !strings.Contains(got, "The app was never deployed: the webhook's first call deploys it as stored.") {
		t.Errorf("the webhook section doesn't say the first call deploys: %s", got)
	}
}
