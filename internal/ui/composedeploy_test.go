package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// storedSecret is the value shop's API_KEY has in the secret store; no page may draw it.
const storedSecret = "stored-api-key-marker"

// shopRequest is the Compose request shop is deployed from: web with a domain,
// a health check and a secret, worker, and db with a backup.
func shopRequest() compose.Request {
	return compose.Request{Compose: shopCompose, Services: map[string]compose.Overlay{
		"web": {Domains: []compose.Domain{{Host: "shop.example.com"}}, Health: &compose.Health{Path: "/health"},
			Secrets: []app.Secret{{Name: "API_KEY", Value: new(storedSecret)}}},
		"db": {Backup: &app.Backup{Engine: "postgres", Keep: 5}, Secrets: []app.Secret{{Name: "POSTGRES_PASSWORD", Value: new("pw")}}},
	}}
}

// keptShopEnv has shop deployed from shopRequest through the service, which
// keeps its Compose file (feature 015). The revision is the stored app's.
func keptShopEnv(t *testing.T) (*consoleEnv, string) {
	t.Helper()
	return deployShop(t, shopRequest())
}

// deployShop is keptShopEnv for another request.
func deployShop(t *testing.T, req compose.Request) (*consoleEnv, string) {
	t.Helper()
	e := composeEnv(t)
	rec, err := e.Svc.DeployCompose(context.Background(), "shop", req, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	stored, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	return e, stored.Revision
}

// shopEnv is keptShopEnv for an app made before feature 015: the same app
// with no kept file, so the Compose forms start empty as they did in 014.
func shopEnv(t *testing.T) (*consoleEnv, string) {
	t.Helper()
	e, _ := keptShopEnv(t)
	ctx := context.Background()
	stored, err := e.Store.GetApp(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := e.Store.SaveApp(ctx, stored.App, nil)
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	return e, legacy.Revision
}

// previewShop posts the Deploy from Compose preview with the revision a form drawn now has.
func (e *consoleEnv) previewShop(t *testing.T, revision, file string) (*http.Response, string) {
	t.Helper()
	return e.postForm(t, "/apps/shop/compose/preview", url.Values{"revision": {revision}, "compose": {file}})
}

// shopFormOf is the form of a Deploy from Compose preview, as a browser would send it from its second button.
func shopFormOf(t *testing.T, body string, set url.Values) url.Values {
	t.Helper()
	f := formWith(t, body, "/apps/shop/compose/preview")
	f.Values.Set("svc.db.backupEngine", "postgres") // a select, which formsIn doesn't read
	for k, v := range set {
		f.Values[k] = v
	}
	return f.Values
}

func TestConfigureOffersDeployFromCompose(t *testing.T) {
	e, _ := shopEnv(t)
	_, body := e.get(t, "/apps/shop/configure")
	if !strings.Contains(body, `<a href="/apps/shop/compose" class="cv-btn cv-btn-secondary cv-btn-sm">Deploy from Compose</a>`) {
		t.Errorf("Configure doesn't link to Deploy from Compose:\n%s", text(body))
	}
}

func TestComposeDeployForm(t *testing.T) {
	e, revision := shopEnv(t)
	resp, body := e.get(t, "/apps/shop/compose")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(body))
	}
	for _, want := range []string{
		// under the Configure tab, with the app's header
		`class="cv-tab" aria-current="page">Configure</a>`, `aria-current="page">Deploy from Compose</span>`,
		`<form id="compose-form" method="post" action="/apps/shop/compose/preview"`,
		`<input type="hidden" name="revision" value="` + revision + `">`,
		`<textarea class="cv-field cv-field-area cv-field-file" id="compose" name="compose"`,
		`>Preview</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="name"`) || strings.Contains(body, ">Deploy</button>") || strings.Contains(body, "svc.") {
		t.Error("the form asks for a name, or offers a deploy or overlay before a preview")
	}
	if resp, _ := e.get(t, "/apps/nope/compose"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /apps/nope/compose = %d, want 404", resp.StatusCode)
	}
}

func TestComposeDeployPreviewPrefills(t *testing.T) {
	e, revision := shopEnv(t)
	regs := e.Nomad.Registrations()
	resp, body := e.previewShop(t, revision, shopCompose)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
	f := formWith(t, body, "/apps/shop/compose/preview")
	if f.Values.Get("svc.web.domains") != "shop.example.com" || f.Values.Get("svc.web.healthPath") != "/health" ||
		f.Values.Get("svc.db.backupKeep") != "5" || f.Values.Get("revision") != revision {
		t.Errorf("the overlay isn't the app's: %v", f.Values)
	}
	// The secrets are names, as inputs that start empty, and the stored values are nowhere.
	for _, name := range []string{"svc.web.secret.API_KEY", "svc.db.secret.POSTGRES_PASSWORD"} {
		if v, ok := f.Values[name]; !ok || v[0] != "" {
			t.Errorf("input %s = %v, want it there and empty", name, v)
		}
	}
	for _, secret := range []string{storedSecret, "pw"} {
		if strings.Contains(body, `"`+secret+`"`) || strings.Contains(body, ">"+secret+"<") {
			t.Errorf("the page holds a stored secret value %q", secret)
		}
	}
	if strings.Contains(body, storedSecret) {
		t.Error("the page holds a stored secret value")
	}
	for _, want := range []string{"Empty keeps the current value.", `<option value="postgres" selected>`, ">Deploy</button>", `formaction="/apps/shop/compose"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the preview lacks %q", want)
		}
	}
	if strings.Contains(text(body), "Adds service") || strings.Contains(text(body), "Removes service") {
		t.Errorf("the preview of the same services names a change:\n%s", text(body))
	}
	if e.Nomad.Registrations() != regs {
		t.Errorf("a preview registered %d jobs", e.Nomad.Registrations()-regs)
	}
}

func TestComposeDeployPreviewNamesAddedAndRemoved(t *testing.T) {
	e, revision := shopEnv(t)
	file := "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n  cache:\n    image: traefik/whoami:latest\n"
	_, body := e.previewShop(t, revision, file)
	page := text(body)
	for _, want := range []string{"Adds service cache.", "Removes service worker: its copies stop.", "Removes service db: its copies stop."} {
		if !strings.Contains(page, want) {
			t.Errorf("the preview lacks %q:\n%s", want, page)
		}
	}
	// The overlay of a service the file dropped isn't asked for; the new one's starts empty.
	if strings.Contains(body, `name="svc.db.`) || !strings.Contains(body, `name="svc.cache.newSecret.0"`) {
		t.Errorf("the overlay isn't the file's services':\n%s", page)
	}
	if f := formWith(t, body, "/apps/shop/compose/preview"); f.Values.Get("svc.web.domains") != "shop.example.com" {
		t.Errorf("web's domain = %q, want the app's", f.Values.Get("svc.web.domains"))
	}
}

func TestComposeDeploy(t *testing.T) {
	t.Run("an empty secret keeps its value, a removed service is removed", func(t *testing.T) {
		e, revision := shopEnv(t)
		file := "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n"
		_, page := e.previewShop(t, revision, file)
		values := shopFormOf(t, page, nil)
		regs, deployments := e.Nomad.Registrations(), len(e.deployments(t, "shop"))

		resp, body := e.postForm(t, "/apps/shop/compose", values)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("deploy = %d, want 303:\n%s", resp.StatusCode, text(body))
		}
		recs := e.deployments(t, "shop")
		if len(recs) != deployments+1 || e.Nomad.Registrations() != regs+1 ||
			location(t, resp) != "/apps/shop/deployments#deployment-"+recs[0].Deployment.ID {
			t.Fatalf("went to %q with %d deployments, %d registrations; want one more of each", location(t, resp), len(recs), e.Nomad.Registrations()-regs)
		}
		if recs[0].Deployment.Trigger != app.TriggerConsole {
			t.Errorf("trigger = %q, want console", recs[0].Deployment.Trigger)
		}
		e.waitDeploy(t, recs[0].Deployment.ID)

		got := e.storedApp(t, "shop")
		if len(got.Services) != 1 || got.Services[0].Name != "web" || got.Services[0].Domains[0].Host != "shop.example.com" ||
			got.Services[0].Health.Path != "/health" || got.Services[0].Secrets[0].Name != "API_KEY" || got.Services[0].Secrets[0].Value != nil {
			t.Errorf("stored app = %+v; want web alone, with its domain, health check and secret name", got)
		}
		if v := e.Nomad.Variable("nomad/jobs/shop/web")["API_KEY"]; v != storedSecret {
			t.Errorf("API_KEY = %q, want the value kept", v)
		}
	})

	t.Run("a typed value replaces the secret", func(t *testing.T) {
		e, revision := shopEnv(t)
		_, page := e.previewShop(t, revision, shopCompose)
		resp, body := e.postForm(t, "/apps/shop/compose", shopFormOf(t, page, url.Values{"svc.web.secret.API_KEY": {"new-value"}}))
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
		}
		if v := e.Nomad.Variable("nomad/jobs/shop/web")["API_KEY"]; v != "new-value" {
			t.Errorf("API_KEY = %q, want the typed value", v)
		}
	})

	t.Run("an app that changed since the form was drawn is refused", func(t *testing.T) {
		e, revision := shopEnv(t)
		_, page := e.previewShop(t, revision, shopCompose)
		values := shopFormOf(t, page, url.Values{"svc.web.secret.API_KEY": {aSecret}})

		// Someone else saves the app: its revision moves on.
		changed := e.storedApp(t, "shop")
		changed.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: "other"}}
		e.deploy(t, changed)
		fresh, err := e.Store.GetApp(context.Background(), "shop")
		if err != nil || fresh.Revision == revision {
			t.Fatalf("the app's revision didn't move: %v", err)
		}
		regs, deployments := e.Nomad.Registrations(), len(e.deployments(t, "shop"))

		resp, body := e.postForm(t, "/apps/shop/compose", values)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app changed since you opened the form.") {
			t.Fatalf("deploy = %d; want 409 with the stale panel:\n%s", resp.StatusCode, text(body))
		}
		if f := formWith(t, body, "/apps/shop/compose/preview"); f.Values.Get("revision") != fresh.Revision {
			t.Errorf("the form's revision = %q, want the fresh %q", f.Values.Get("revision"), fresh.Revision)
		}
		if e.Nomad.Registrations() != regs || len(e.deployments(t, "shop")) != deployments {
			t.Error("a stale deploy deployed")
		}
		if strings.Contains(body, aSecret) || strings.Contains(body, storedSecret) {
			t.Error("the stale page holds a secret value")
		}
		if !strings.Contains(text(body), "Secret values aren't kept; type them again.") {
			t.Error("the stale page doesn't say the values were dropped")
		}
		if got := e.storedApp(t, "shop"); got.Services[0].Env[0].Value != "other" {
			t.Error("the other save was overwritten")
		}

		// Deploying again from the page that came back works.
		resp, body = e.postForm(t, "/apps/shop/compose", shopFormOf(t, body, nil))
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("deploying again = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("a form without its revision is refused", func(t *testing.T) {
		e, _ := shopEnv(t)
		regs := e.Nomad.Registrations()
		resp, _ := e.postForm(t, "/apps/shop/compose", url.Values{"compose": {shopCompose}})
		if resp.StatusCode != http.StatusBadRequest || e.Nomad.Registrations() != regs {
			t.Errorf("deploy = %d, want 400 and nothing deployed", resp.StatusCode)
		}
	})

	t.Run("a refused file names its path", func(t *testing.T) {
		e, revision := shopEnv(t)
		file := "services:\n  web:\n    image: traefik/whoami:latest\n    build: .\n"
		resp, body := e.previewShop(t, revision, file)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `<span class="cv-inline-mono text-fg-1">services.web.build</span>`) ||
			strings.Contains(body, ">Deploy</button>") {
			t.Errorf("preview = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("an unknown app is the not-found page", func(t *testing.T) {
		e, _ := shopEnv(t)
		for _, path := range []string{"/apps/nope/compose/preview", "/apps/nope/compose"} {
			if resp, _ := e.postForm(t, path, url.Values{"revision": {"x"}, "compose": {shopCompose}}); resp.StatusCode != http.StatusNotFound {
				t.Errorf("POST %s = %d, want 404", path, resp.StatusCode)
			}
		}
	})
}
