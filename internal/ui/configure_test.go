package ui

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// shopApp has every field of the definition set on its web service, two env vars among them,
// and a worker with almost none, so the tab's "Using defaults" line shows too. The env
// values are made to be found if they are ever drawn.
func shopApp() app.App {
	two := 2
	return app.App{
		ID: "shop", Name: "The Shop", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{Name: "web", Image: "ghcr.io/example/shop:2.1", Command: "/bin/server", Args: []string{"--port", "8080"},
				Env:       []app.EnvVar{{Name: "LOG_LEVEL", Value: "debug-value-x1"}, {Name: "API_KEY", Value: "s3cr3t-value-9f2"}},
				Ports:     []app.Port{{Name: "http", ContainerPort: 8080}},
				Domains:   []app.Domain{{Host: "shop.example.com", Port: "http"}},
				Replicas:  &two,
				Resources: app.Resources{CPUMHz: 250, MemoryMB: 256, MemoryMaxMB: 1024},
				Health:    &app.HealthCheck{Path: "/healthz", Interval: 15 * time.Second, Timeout: 3 * time.Second}},
			{Name: "worker", Image: "ghcr.io/example/shop-worker:2.1"},
		},
	}
}

// configureEnv has shop with two deployments, healthy copies, and its job in Nomad.
func configureEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seed(t, shopApp(),
		seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1},
		seedDeploy{ID: "s2", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 2})
	// Caravana's own job (the meta of its deployment s2): Purge only removes those.
	e.Nomad.AddForeignJob(&api.Job{ID: new("shop"), Meta: map[string]string{"caravana.deployment": "s2"}})
	e.copies("shop", "web", 2, 2, 0)
	return e
}

var webhookURLPattern = regexp.MustCompile(`https?://[^\s<"]+/v1/webhooks/shop\?token=(cvwh_[A-Za-z0-9_-]+)`)

func TestConfigureShowsTheStoredDefinition(t *testing.T) {
	e := configureEnv(t)
	resp, body := e.get(t, "/apps/shop/configure")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/shop/configure = %d:\n%s", resp.StatusCode, body)
	}

	// The tab is in the header, marked as the current one, and the breadcrumbs end here.
	for _, want := range []string{
		`<a href="/apps/shop/configure" class="cv-tab" aria-current="page">Configure</a>`,
		`hx-get="/parts/apps/shop/header?tab=configure"`,
		`aria-current="page">Configure</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the tab lacks %q:\n%s", want, body)
		}
	}

	// The app's own facts, and what the web service keeps that the form can't change.
	got := text(body)
	for _, want := range []string{"Display name The Shop", "Source image", "Command /bin/server", "Arguments --port 8080"} {
		if !strings.Contains(got, want) {
			t.Errorf("the definition lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Made from a Compose file") {
		t.Error("an image app says it was made from a Compose file")
	}

	// The form holds every editable value, one panel per service, in order, drawn from the stored app.
	f := formWith(t, body, "/apps/shop/configure")
	rec, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"revision":               rec.Revision,
		"svc.web.image":          "ghcr.io/example/shop:2.1",
		"svc.web.env":            "LOG_LEVEL=debug-value-x1\nAPI_KEY=s3cr3t-value-9f2",
		"svc.web.port":           "8080",
		"svc.web.domains":        "shop.example.com",
		"svc.web.copies":         "2",
		"svc.web.healthPath":     "/healthz",
		"svc.web.healthInterval": "15",
		"svc.web.healthTimeout":  "3",
		"svc.web.cpu":            "250",
		"svc.web.memory":         "256",
		"svc.web.memoryMax":      "1024",
		"svc.worker.image":       "ghcr.io/example/shop-worker:2.1",
		"svc.worker.env":         "",
		"svc.worker.copies":      "",
		"svc.worker.memory":      "",
	} {
		if got := f.Values.Get(name); got != want {
			t.Errorf("input %s = %q, want %q", name, got, want)
		}
	}
	// A worker has no port, domains or health check to edit.
	for _, name := range []string{"svc.worker.port", "svc.worker.domains", "svc.worker.healthPath"} {
		if _, has := f.Values[name]; has {
			t.Errorf("the worker has an input %s", name)
		}
	}
	for _, want := range []string{"Image", "Environment", "Container port", "Domains", "Copies", "Health check", "CPU (MHz)",
		"Memory reserved (MB)", "Memory ceiling (MB)", "Save and deploy"} {
		if !strings.Contains(got, want) {
			t.Errorf("the form lacks %q:\n%s", want, got)
		}
	}
	web, worker := strings.Index(body, `<h3 id="svc-web-title"`), strings.Index(body, `<h3 id="svc-worker-title"`)
	if web < 0 || worker < web {
		t.Errorf("service panels: web at %d, worker at %d", web, worker)
	}
	// The memory input has the anchor the needs-you list links to.
	if !strings.Contains(body, `id="svc-web-memory"`) {
		t.Error("the memory input has no id svc-web-memory")
	}
}

// A Compose-made app says that a file replaces edits.
func TestConfigureNotesComposeApps(t *testing.T) {
	e := newConsoleEnv(t)
	a := shopApp()
	a.Source.Kind = app.SourceCompose
	e.seed(t, a)
	_, body := e.get(t, "/apps/shop/configure")
	if got := text(body); !strings.Contains(got, "Made from a Compose file. Deploying a file again replaces these edits.") {
		t.Errorf("the note is missing:\n%s", got)
	}
}

// sentinel015 is a secret value that no page may ever draw.
const sentinel015 = "s3cret-sentinel-015"

// keptShopFile is shopCompose with a comment the kept file must show back
// as typed, with characters that need escaping.
const keptShopFile = "# shop stack <kept> & commented\n" + shopCompose

// keptSentinelEnv has shop deployed from keptShopFile, with a secret whose
// value is sentinel015, so every page that draws the kept file can be searched.
func keptSentinelEnv(t *testing.T) (*consoleEnv, string) {
	t.Helper()
	req := shopRequest()
	req.Compose = keptShopFile
	ov := req.Services["web"]
	ov.Secrets = []app.Secret{{Name: "API_KEY", Value: new(sentinel015)}}
	req.Services["web"] = ov
	return deployShop(t, req)
}

// Feature 015, US1: Configure of an app that keeps its Compose file shows the
// form first, filled from the file, and the definition as facts, not inputs.
func TestConfigureKeptFile(t *testing.T) {
	e, revision := keptSentinelEnv(t)
	resp, body := e.get(t, "/apps/shop/configure")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/shop/configure = %d:\n%s", resp.StatusCode, text(body))
	}

	// The file as it was typed, escaped, in the textarea of the Compose form.
	if !strings.Contains(body, html.EscapeString("# shop stack <kept> & commented")) {
		t.Errorf("the page lacks the file's comment, escaped:\n%s", body)
	}
	f := formWith(t, body, "/apps/shop/compose/preview")
	if got := f.Values.Get("compose"); got != keptShopFile {
		t.Errorf("the textarea = %q, want the kept file byte for byte", got)
	}
	// The overlay, the secret names with empty inputs, and the revision.
	for name, want := range map[string]string{
		"revision":                        revision,
		"svc.web.domains":                 "shop.example.com",
		"svc.web.healthPath":              "/health",
		"svc.web.secret.API_KEY":          "",
		"svc.db.secret.POSTGRES_PASSWORD": "",
		"svc.db.backupKeep":               "5",
	} {
		if got, ok := f.Values[name]; !ok || got[0] != want {
			t.Errorf("input %s = %v, want %q", name, got, want)
		}
	}
	// Preview first, as on Deploy from Compose: the deploy button comes with the preview.
	if !strings.Contains(body, `hx-post="/apps/shop/compose/preview">Preview</button>`) || strings.Contains(body, ">Deploy</button>") {
		t.Errorf("the form should offer Preview and no Deploy yet:\n%s", text(body))
	}
	for _, want := range []string{`wrap="off"`, `spellcheck="false"`, `autocapitalize="off"`, "Put passwords and keys in secrets below, not in the file."} {
		if !strings.Contains(body, want) {
			t.Errorf("the file's textarea lacks %q", want)
		}
	}

	// The definition is read-only: facts, no inputs, no save.
	got := text(body)
	for _, want := range []string{"This app is edited through its Compose file.", "Image postgres:17", "Domains shop.example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{`name="svc.web.image"`, `name="svc.web.env"`, `id="edit-form"`, "Save and deploy", "Made from a Compose file", "Deploy from Compose"} {
		if strings.Contains(body, gone) {
			t.Errorf("the page still has %q", gone)
		}
	}
	// The sections below are as before, and no value is anywhere.
	for _, want := range []string{"Secrets", "Webhook", "Export", "Delete app"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks the %s section", want)
		}
	}
	if strings.Contains(body, sentinel015) {
		t.Error("a secret value is on the Configure page")
	}
}

// Feature 015: an image app is drawn as it was: the edit form, no Compose
// form, no read-only line, no "wasn't kept" notice.
func TestConfigureImageAppIsAsBefore(t *testing.T) {
	e := configureEnv(t)
	_, body := e.get(t, "/apps/shop/configure")
	for _, gone := range []string{`id="compose-form"`, "This app is edited through its Compose file.", "wasn't kept"} {
		if strings.Contains(body, gone) {
			t.Errorf("the page has %q", gone)
		}
	}
	for _, want := range []string{`id="edit-form"`, "Save and deploy"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}

// Feature 015, US2: a Compose app made before the file was kept asks for it
// once: the notice, an empty file, the overlay from the app, the revision, and
// the per-field form still editable below.
func TestConfigureNotKeptFile(t *testing.T) {
	e, revision := shopEnv(t)
	resp, body := e.get(t, "/apps/shop/configure")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/shop/configure = %d:\n%s", resp.StatusCode, text(body))
	}
	got := text(body)
	if !strings.Contains(got, "This app's Compose file wasn't kept. Paste it once to edit it here from now on.") {
		t.Errorf("the page lacks the notice:\n%s", got)
	}

	// The Compose form: an empty file, the overlay the app has, secret names with empty inputs.
	f := formWith(t, body, "/apps/shop/compose/preview")
	if v, ok := f.Values["compose"]; !ok || v[0] != "" {
		t.Errorf("the textarea = %v, want it present and empty", v)
	}
	for name, want := range map[string]string{
		"revision":                        revision,
		"svc.web.domains":                 "shop.example.com",
		"svc.web.healthPath":              "/health",
		"svc.web.secret.API_KEY":          "",
		"svc.db.secret.POSTGRES_PASSWORD": "",
		"svc.db.backupKeep":               "5",
	} {
		if got, ok := f.Values[name]; !ok || got[0] != want {
			t.Errorf("input %s = %v, want %q", name, got, want)
		}
	}
	if !strings.Contains(body, `hx-post="/apps/shop/compose/preview">Preview</button>`) || strings.Contains(body, ">Deploy</button>") {
		t.Errorf("the form should offer Preview and no Deploy yet:\n%s", got)
	}
	if strings.Contains(body, sentinel015) || strings.Contains(body, storedSecret) {
		t.Error("a secret value is on the Configure page")
	}

	// The per-field form is editable as in 014, with its note and the link.
	for _, want := range []string{`id="edit-form"`, "Save and deploy", `name="svc.web.image"`,
		"Made from a Compose file. Deploying a file again replaces these edits.", ">Deploy from Compose</a>"} {
		if !strings.Contains(body, want) && !strings.Contains(got, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(got, "This app is edited through its Compose file.") {
		t.Error("the definition is read-only for an app that kept no file")
	}
}

// Feature 015, US2: paste the file once on Configure, preview and deploy, and
// the app keeps it: Configure then shows the file and the read-only definition.
func TestConfigureNotKeptFilePasteOnce(t *testing.T) {
	e, _ := shopEnv(t)
	_, page := e.get(t, "/apps/shop/configure")
	const pasted = "# pasted once\n" + shopCompose

	resp, body := e.postForm(t, "/apps/shop/compose/preview", shopFormOf(t, page, url.Values{"compose": {pasted}}))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, ">Deploy</button>") {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/apps/shop/compose", shopFormOf(t, body, nil))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)

	_, body = e.get(t, "/apps/shop/configure")
	if got := formWith(t, body, "/apps/shop/compose/preview").Values.Get("compose"); got != pasted {
		t.Errorf("Configure shows %q, want the pasted file", got)
	}
	got := text(body)
	if strings.Contains(got, "wasn't kept") || !strings.Contains(got, "This app is edited through its Compose file.") {
		t.Errorf("after the paste Configure should show the kept view:\n%s", got)
	}
}

// US4, US5 (feature 009): a Compose app's volumes are listed, and the delete
// section says they go, with their data.
func TestConfigureShowsVolumes(t *testing.T) {
	e := newConsoleEnv(t)
	a := shopApp()
	a.Source.Kind = app.SourceCompose
	a.Services[1].Volumes = []app.Volume{{Name: "db-data", Path: "/var/lib/postgresql/data"}, {Name: "seed", Path: "/seed", ReadOnly: true}}
	e.seed(t, a)
	_, body := e.get(t, "/apps/shop/configure")
	got := text(body)
	for _, want := range []string{
		"Source compose",
		"Volumes db-data → /var/lib/postgresql/data seed → /seed (read-only) Kept across deploys, erased with the app.",
		"Its volumes db-data and seed and the data in them are erased.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
}

func TestConfigureWebhook(t *testing.T) {
	ctx := context.Background()
	e := configureEnv(t)
	post := func(path string) (*http.Response, string) { return e.postForm(t, path, nil) }

	t.Run("no token: a way to create one, and nothing to replace or revoke", func(t *testing.T) {
		_, body := e.get(t, "/apps/shop/configure")
		got := text(body)
		if !strings.Contains(got, "No webhook token.") || !strings.Contains(got, "Create token") {
			t.Errorf("the webhook section lacks the empty state:\n%s", got)
		}
		if !strings.Contains(body, `<form method="post" action="/apps/shop/webhook"`) {
			t.Errorf("no form posting to /apps/shop/webhook:\n%s", body)
		}
		if strings.Contains(got, "Replace token") || strings.Contains(got, "Revoke token") {
			t.Errorf("replace or revoke is offered without a token:\n%s", got)
		}
	})

	var first string
	t.Run("create: 200 with the whole URL once, with a copy button", func(t *testing.T) {
		resp, body := post("/apps/shop/webhook")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST webhook = %d, want 200 (never a redirect: the URL would be in history):\n%s", resp.StatusCode, body)
		}
		m := webhookURLPattern.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("the response has no webhook URL:\n%s", body)
		}
		first = m[1]
		if !strings.HasPrefix(m[0], e.URL+"/v1/webhooks/shop?token=") {
			t.Errorf("URL = %s, want it on this server's own address", m[0])
		}
		for _, want := range []string{
			"Copy it now. It won't be shown again.",
			`id="webhook-url"`,
			`data-copy="webhook-url"`,
			"Token created just now.",
		} {
			if !strings.Contains(body, want) && !strings.Contains(text(body), want) {
				t.Errorf("the response lacks %q:\n%s", want, body)
			}
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
		}
		if err := e.Svc.CheckWebhookToken(ctx, "shop", first); err != nil {
			t.Errorf("the shown token doesn't work: %v", err)
		}
	})

	t.Run("a reload doesn't show it again, and replace and revoke wait behind confirmations", func(t *testing.T) {
		_, body := e.get(t, "/apps/shop/configure")
		if strings.Contains(body, first) || strings.Contains(body, "cvwh_") || strings.Contains(body, `id="webhook-url"`) {
			t.Errorf("the token is shown again:\n%s", body)
		}
		got := text(body)
		if !strings.Contains(got, "Token created") || strings.Contains(got, "No webhook token.") {
			t.Errorf("the section doesn't say a token exists:\n%s", got)
		}
		// Each is a <details>: the summary opens a sentence and the second button submits.
		for _, action := range []string{"/apps/shop/webhook", "/apps/shop/webhook/revoke"} {
			form := `<form method="post" action="` + action + `"`
			i := strings.Index(body, form)
			if i < 0 {
				t.Fatalf("no form posting to %s:\n%s", action, body)
			}
			before := body[:i]
			if open, closed := strings.LastIndex(before, "<details"), strings.LastIndex(before, "</details>"); open < 0 || open < closed {
				t.Errorf("the form for %s isn't inside a <details>", action)
			}
		}
		if n := strings.Count(got, "The current token stops working at once."); n != 2 {
			t.Errorf("the consequence is said %d times, want once per confirmation: %s", n, got)
		}
		if regexp.MustCompile(`<details[^>]* open`).MatchString(body) {
			t.Error("a confirmation starts open")
		}
	})

	t.Run("replace: a new URL, and the old token stops working at once", func(t *testing.T) {
		resp, body := post("/apps/shop/webhook")
		m := webhookURLPattern.FindStringSubmatch(body)
		if resp.StatusCode != http.StatusOK || m == nil || m[1] == first {
			t.Fatalf("replace = %d with %v, want 200 and a different token:\n%s", resp.StatusCode, m, body)
		}
		if err := e.Svc.CheckWebhookToken(ctx, "shop", first); !errors.Is(err, service.ErrUnauthorized) {
			t.Errorf("the replaced token = %v, want it refused", err)
		}
		if err := e.Svc.CheckWebhookToken(ctx, "shop", m[1]); err != nil {
			t.Errorf("the new token = %v", err)
		}
	})

	t.Run("revoke: back to Configure, the token refused", func(t *testing.T) {
		resp, _ := post("/apps/shop/webhook/revoke")
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop/configure" {
			t.Fatalf("revoke = %d to %q, want 303 to /apps/shop/configure", resp.StatusCode, location(t, resp))
		}
		if st, err := e.Svc.WebhookStatus(ctx, "shop"); err != nil || st.Enabled {
			t.Errorf("webhook status = %+v, %v; want none", st, err)
		}
		_, body := e.get(t, "/apps/shop/configure")
		if !strings.Contains(text(body), "No webhook token.") {
			t.Errorf("the page still shows a token:\n%s", text(body))
		}
		// Revoking again is fine: the end state is the one asked for.
		if resp, _ := post("/apps/shop/webhook/revoke"); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("second revoke = %d, want 303", resp.StatusCode)
		}
	})

	t.Run("an unknown app is the not-found page", func(t *testing.T) {
		for _, path := range []string{"/apps/ghost/webhook", "/apps/ghost/webhook/revoke"} {
			resp, body := post(path)
			if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "There's no app called ghost.") {
				t.Errorf("POST %s = %d:\n%s", path, resp.StatusCode, text(body))
			}
		}
	})

	t.Run("a cross-site post is refused and changes nothing", func(t *testing.T) {
		resp, _ := e.do(t, request{method: http.MethodPost, path: "/apps/shop/webhook", cookies: []*http.Cookie{e.Cookie},
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
		if st, _ := e.Svc.WebhookStatus(ctx, "shop"); st.Enabled {
			t.Error("a cross-site post created a token")
		}
	})
}

func TestConfigureDelete(t *testing.T) {
	ctx := context.Background()
	const confirmWrong = "Type shop to confirm."

	t.Run("the section sits last, behind a confirmation, and says what goes", func(t *testing.T) {
		e := configureEnv(t)
		_, body := e.get(t, "/apps/shop/configure")
		got := text(body)
		for _, want := range []string{
			"Delete app", "Its 3 copies stop and shop.example.com stops answering.",
			"Its 2 deployments are erased.", "This can't be undone.",
			"Type shop to confirm", "Delete shop",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the delete section lacks %q:\n%s", want, got)
			}
		}
		for _, want := range []string{
			`<form method="post" action="/apps/shop/delete"`,
			`name="confirm"`,
			`data-confirm-name="shop"`, // console.js keeps the button off until the name matches
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the delete form lacks %q", want)
			}
		}
		// The name in the label is set in mono inside the uppercase label; the stylesheet keeps its
		// letters as they are (TestStylesheetKeepsTheNameInALabel), since the match is case-sensitive.
		if !strings.Contains(body, `<span class="cv-label">Type <span class="cv-inline-mono">shop</span> to confirm</span>`) {
			t.Errorf("the confirmation label isn't the label with the name inside it:\n%s", body)
		}
		// The button is enabled in the markup, so the form works without script.
		if m := regexp.MustCompile(`<button[^>]*>Delete shop</button>`).FindString(body); m == "" || strings.Contains(m, "disabled") {
			t.Errorf("the delete button = %q, want it present and not disabled", m)
		}
		// It is the last thing on the page, after the webhook, inside a <details>.
		del := strings.Index(body, `action="/apps/shop/delete"`)
		hook := strings.Index(body, `action="/apps/shop/webhook"`)
		if hook < 0 || del < hook {
			t.Errorf("delete form at %d, webhook form at %d: delete must come last", del, hook)
		}
		if open, closed := strings.LastIndex(body[:del], "<details"), strings.LastIndex(body[:del], "</details>"); open < 0 || open < closed {
			t.Error("the delete form isn't inside a <details>")
		}
		if strings.Contains(body[:del], "<details open") {
			t.Error("the delete confirmation starts open")
		}

		// A webhook token is part of what goes.
		if _, err := e.Svc.CreateWebhookToken(ctx, "shop"); err != nil {
			t.Fatal(err)
		}
		_, body = e.get(t, "/apps/shop/configure")
		if got := text(body); !strings.Contains(got, "Its 2 deployments and its webhook token are erased.") {
			t.Errorf("the token isn't named among what goes:\n%s", got)
		}
	})

	t.Run("a wrong or empty name: 422, the sentence, and nothing deleted", func(t *testing.T) {
		e := configureEnv(t)
		for _, typed := range []string{"", "shops", "Shop", " shop", "blog"} {
			resp, body := e.postForm(t, "/apps/shop/delete", map[string][]string{"confirm": {typed}})
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("typed %q: status %d, want 422", typed, resp.StatusCode)
			}
			if got := text(body); !strings.Contains(got, confirmWrong) {
				t.Errorf("typed %q: lacks %q:\n%s", typed, confirmWrong, got)
			}
			if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `aria-invalid="true"`) {
				t.Errorf("typed %q: the error isn't announced on the field", typed)
			}
			// The confirmation is open, so the message and the field are in view.
			if !regexp.MustCompile(`<details[^>]* open[^>]*>`).MatchString(body[strings.Index(body, `id="delete-title"`):]) {
				t.Errorf("typed %q: the delete confirmation is closed", typed)
			}
			if _, err := e.Store.GetApp(ctx, "shop"); err != nil {
				t.Fatalf("typed %q: the app is gone: %v", typed, err)
			}
		}
		if e.Nomad.Purged("shop") {
			t.Error("Nomad was asked to purge the job on a wrong name")
		}
		if resp, _ := e.postForm(t, "/apps/shop/delete", nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("a post with no confirm field = %d, want 422", resp.StatusCode)
		}
	})

	t.Run("the right name: 303 to the Apps page with its notice, the job gone", func(t *testing.T) {
		e := configureEnv(t)
		if _, err := e.Svc.CreateWebhookToken(ctx, "shop"); err != nil {
			t.Fatal(err)
		}
		resp, body := e.postForm(t, "/apps/shop/delete", map[string][]string{"confirm": {"shop"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps?deleted=shop" {
			t.Fatalf("delete = %d to %q, want 303 to /apps?deleted=shop:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if _, err := e.Store.GetApp(ctx, "shop"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want the app gone", err)
		}
		if !e.Nomad.Purged("shop") || e.Nomad.Job("shop") != nil {
			t.Error("the job is still in Nomad")
		}
		_, apps := e.get(t, location(t, resp))
		if got := text(apps); !strings.Contains(got, "shop was deleted.") || strings.Contains(apps, `href="/apps/shop"`) {
			t.Errorf("the Apps page after the delete:\n%s", got)
		}
		// The name is free again.
		if resp, _ := e.postForm(t, "/apps", postedForm("shop", "traefik/whoami:latest", "", "", "")); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("creating shop again = %d, want 303", resp.StatusCode)
		}
	})

	t.Run("submitted twice, the second lands on the same Apps page", func(t *testing.T) {
		e := configureEnv(t)
		form := map[string][]string{"confirm": {"shop"}}
		e.postForm(t, "/apps/shop/delete", form)
		resp, _ := e.postForm(t, "/apps/shop/delete", form)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps?deleted=shop" {
			t.Errorf("second delete = %d to %q, want 303 to /apps?deleted=shop", resp.StatusCode, location(t, resp))
		}
		// An app that never existed, or a wrong name for one, is a plain not-found.
		if resp, _ := e.postForm(t, "/apps/ghost/delete", map[string][]string{"confirm": {"other"}}); resp.StatusCode != http.StatusNotFound {
			t.Errorf("delete of a stranger = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("Nomad down: 502 with the sentence, and the app is kept", func(t *testing.T) {
		e := configureEnv(t)
		e.Nomad.Down()
		resp, body := e.postForm(t, "/apps/shop/delete", map[string][]string{"confirm": {"shop"}})
		e.Nomad.Up()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("delete with Nomad down = %d, want 502:\n%s", resp.StatusCode, text(body))
		}
		if got := text(body); !strings.Contains(got, "Nomad couldn't be reached right now, so nothing was deleted. The app is still here.") {
			t.Errorf("the sentence is missing:\n%s", got)
		}
		if _, err := e.Store.GetApp(ctx, "shop"); err != nil {
			t.Errorf("the app is gone: %v", err)
		}
		if n := len(e.deployments(t, "shop")); n != 2 {
			t.Errorf("deployments = %d, want both kept", n)
		}
		// The page still has its delete form, open, for another try.
		if !strings.Contains(body, `action="/apps/shop/delete"`) {
			t.Error("no delete form to try again with")
		}
		// And once Nomad is back, the same form works.
		if resp, _ := e.postForm(t, "/apps/shop/delete", map[string][]string{"confirm": {"shop"}}); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("delete after Nomad is back = %d, want 303", resp.StatusCode)
		}
	})

	t.Run("another tab on the app hears it was deleted", func(t *testing.T) {
		e := configureEnv(t)
		ls := openStream(t, e.URL, "/live/apps/shop", e.Cookie.Value)
		ls.expect("sync")
		if resp, _ := e.postForm(t, "/apps/shop/delete", map[string][]string{"confirm": {"shop"}}); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("delete = %d, want 303", resp.StatusCode)
		}
		for deleted := false; !deleted; {
			switch name := ls.next(); name {
			case "deleted":
				deleted = true
			case "app", "deployments", "copies":
			default:
				t.Fatalf("event %q, want deleted among app, deployments and copies", name)
			}
		}
		// That tab refreshes its header and finds the app gone.
		_, part := e.get(t, "/parts/apps/shop/header?tab=configure")
		if !strings.Contains(text(part), "shop was deleted.") {
			t.Errorf("the header part after the delete: %s", text(part))
		}
	})
}

func TestConfigureNeedsASessionAndAnApp(t *testing.T) {
	e := configureEnv(t)

	resp, _ := e.do(t, request{path: "/apps/shop/configure"})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login?next=") {
		t.Errorf("GET without a session = %d to %q, want 303 to /login", resp.StatusCode, location(t, resp))
	}
	for _, path := range []string{"/apps/shop/webhook", "/apps/shop/webhook/revoke", "/apps/shop/delete", "/apps/shop/configure"} {
		resp, _ := e.do(t, request{method: http.MethodPost, path: path, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("POST %s without a session = %d to %q, want 303 to /login", path, resp.StatusCode, location(t, resp))
		}
	}
	if _, err := e.Store.GetApp(context.Background(), "shop"); err != nil {
		t.Errorf("an unauthenticated request deleted the app: %v", err)
	}

	resp, body := e.get(t, "/apps/ghost/configure")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "There's no app called ghost.") {
		t.Errorf("GET unknown app = %d:\n%s", resp.StatusCode, text(body))
	}
}
