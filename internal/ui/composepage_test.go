package ui

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// shopCompose is a web service with a port, a worker with none and a database
// on a volume, with two attributes Caravana honors with a warning.
const shopCompose = `services:
  web:
    image: traefik/whoami:latest
    expose: ["80"]
    restart: "no"
  worker:
    image: traefik/whoami:latest
  db:
    image: postgres:17
    expose: ["5432"]
    container_name: shop-db
    volumes:
      - data:/var/lib/postgresql/data
volumes:
  data: {}
`

// passwordInput matches the tag of a password input.
var passwordInput = regexp.MustCompile(`<input[^>]*type="password"[^>]*>`)

// aSecret is a value no page may ever contain.
const aSecret = "hunter2-do-not-print"

// composeEnv is a console whose registry also has postgres and that has a
// backup destination, so a database can ask for backups.
func composeEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.Reg.AddIndex("library/postgres", "17", otherDigest)
	resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
		"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {"dest-key"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /settings/backups = %d:\n%s", resp.StatusCode, text(body))
	}
	return e
}

// previewNew posts New app's Compose preview as a browser would.
func (e *consoleEnv) previewNew(t *testing.T, name, file string) (*http.Response, string) {
	t.Helper()
	return e.postForm(t, "/apps/new/compose/preview", url.Values{"name": {name}, "compose": {file}})
}

// composeFormOf is the Compose form of a page, with its values as the markup
// has them, plus overrides: what a browser sends for the page's second button.
func composeFormOf(t *testing.T, body string, set url.Values) url.Values {
	t.Helper()
	f := formWith(t, body, "/apps/new/compose/preview")
	for k, v := range set {
		f.Values[k] = v
	}
	return f.Values
}

func TestNewAppTabs(t *testing.T) {
	e := newConsoleEnv(t)
	for _, tc := range []struct{ path, current, other string }{
		{"/apps/new", "Image", "Compose file"},
		{"/apps/new?from=compose", "Compose file", "Image"},
	} {
		resp, body := e.get(t, tc.path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", tc.path, resp.StatusCode)
		}
		if !strings.Contains(body, `class="cv-tab" aria-current="page">`+tc.current+`</a>`) ||
			!strings.Contains(body, `class="cv-tab">`+tc.other+`</a>`) {
			t.Errorf("GET %s: the tab %q isn't the current one:\n%s", tc.path, tc.current, body)
		}
	}
	_, body := e.get(t, "/apps/new?from=compose")
	for _, want := range []string{
		`<form id="compose-form" method="post" action="/apps/new/compose/preview"`,
		`name="name"`, `<textarea class="cv-field cv-field-area cv-field-file" id="compose" name="compose"`,
		`>Preview</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Compose tab lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Create and deploy") || strings.Contains(body, `type="password"`) {
		t.Error("the Compose tab offers a deploy before a preview")
	}
	// The Image tab is as it was.
	_, image := e.get(t, "/apps/new")
	if !strings.Contains(image, `<form method="post" action="/apps" hx-post="/apps"`) || strings.Contains(image, "compose-form") {
		t.Errorf("the Image tab changed:\n%s", image)
	}
}

func TestComposePreview(t *testing.T) {
	e := composeEnv(t)
	resp, body := e.previewNew(t, "shop", shopCompose)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
	page := text(body)
	for _, want := range []string{
		"traefik/whoami:latest", "postgres:17", "data → /var/lib/postgresql/data",
		// each warning names its Compose path
		"services.web.restart", "services.db.container_name",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the preview lacks %q:\n%s", want, page)
		}
	}
	for _, want := range []string{
		`<input type="hidden" name="svc.web.overlay" value="1">`, `<input type="hidden" name="svc.db.overlay" value="1">`, `<input type="hidden" name="svc.worker.overlay" value="1">`,
		`name="svc.web.domains"`, `name="svc.web.healthPath"`, `name="svc.db.backupEngine"`, `name="svc.web.newSecret.0"`,
		`formaction="/apps/new/compose"`, ">Create and deploy</button>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the preview lacks %q:\n%s", want, body)
		}
	}
	// worker has no port: no domains or health inputs; web isn't a database: no backup inputs.
	if strings.Contains(body, `name="svc.worker.domains"`) || strings.Contains(body, `name="svc.worker.healthPath"`) || strings.Contains(body, `name="svc.web.backupEngine"`) {
		t.Error("the preview offers inputs a service doesn't have")
	}
	// The file is still in the textarea, and nothing was saved or deployed.
	if f := formWith(t, body, "/apps/new/compose/preview"); strings.TrimSpace(f.Values.Get("compose")) != strings.TrimSpace(shopCompose) {
		t.Errorf("the textarea = %q, want the file back", f.Values.Get("compose"))
	}
	if _, err := e.Store.GetApp(context.Background(), "shop"); err == nil {
		t.Error("a preview saved the app")
	}
	if e.Nomad.Registrations() != 0 {
		t.Errorf("a preview registered %d jobs", e.Nomad.Registrations())
	}
}

func TestComposePreviewRefusals(t *testing.T) {
	tests := []struct {
		name, file string
		want       string // in the page's markup
	}{
		{"an attribute Caravana can't honor", "services:\n  web:\n    image: traefik/whoami:latest\n    build: .\n", "services.web.build"},
		{"not YAML", "services: [", `aria-describedby="compose-error"`}, // the file's own error, at the file field
		{"no services", "services: {}\n", "the file has no services"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := composeEnv(t)
			resp, body := e.previewNew(t, "shop", tc.file)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, text(body))
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("the page lacks %q:\n%s", tc.want, text(body))
			}
			if strings.Contains(body, "Create and deploy") {
				t.Error("a refused file can be created")
			}
			if f := formWith(t, body, "/apps/new/compose/preview"); strings.TrimSpace(f.Values.Get("compose")) != strings.TrimSpace(tc.file) {
				t.Errorf("the textarea = %q, want the file back", f.Values.Get("compose"))
			}
		})
	}

	t.Run("the refusal names its path in the list", func(t *testing.T) {
		e := composeEnv(t)
		_, body := e.previewNew(t, "shop", "services:\n  web:\n    image: traefik/whoami:latest\n    build: .\n")
		if !strings.Contains(body, `<span class="cv-inline-mono text-fg-1">services.web.build</span>`) {
			t.Errorf("the refusal isn't in the list with its path:\n%s", text(body))
		}
	})
}

func TestComposeNameChecks(t *testing.T) {
	e := composeEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1})

	for _, tc := range []struct {
		name, post, want string
		status           int
	}{
		{"empty at preview", "/apps/new/compose/preview", "Enter a name", http.StatusUnprocessableEntity},
		{"taken at preview", "/apps/new/compose/preview", "already exists", http.StatusConflict},
		{"taken at create", "/apps/new/compose", "already exists", http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "whoami"
			if strings.HasPrefix(tc.name, "empty") {
				name = ""
			}
			regs := e.Nomad.Registrations()
			resp, body := e.postForm(t, tc.post, url.Values{"name": {name}, "compose": {shopCompose}})
			if resp.StatusCode != tc.status || !strings.Contains(text(body), tc.want) {
				t.Errorf("%s = %d, want %d with %q:\n%s", tc.post, resp.StatusCode, tc.status, tc.want, text(body))
			}
			if !strings.Contains(body, `aria-describedby="name-error"`) {
				t.Error("the error isn't on the name field")
			}
			if e.Nomad.Registrations() != regs {
				t.Error("a refused name deployed something")
			}
		})
	}
	if a := e.storedApp(t, "whoami"); len(a.Services) != 1 || a.Source.Kind == app.SourceCompose {
		t.Errorf("an app was replaced: %+v", a)
	}
}

// Creating from the preview's own page, with its overlay filled in, makes the
// app the service makes from the same request.
func TestComposeCreate(t *testing.T) {
	e := composeEnv(t)
	_, page := e.previewNew(t, "shop", shopCompose)
	values := composeFormOf(t, page, url.Values{
		"svc.web.domains":          {"shop.example.com"},
		"svc.web.healthPath":       {"/"},
		"svc.web.newSecret.0":      {"API_KEY"},
		"svc.web.newSecretValue.0": {aSecret},
		"svc.db.backupEngine":      {"postgres"},
		"svc.db.backupKeep":        {"5"},
		"svc.db.newSecret.0":       {"POSTGRES_PASSWORD"},
		"svc.db.newSecretValue.0":  {aSecret + "-db"},
	})

	resp, body := e.postForm(t, "/apps/new/compose", values)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create = %d, want 303:\n%s", resp.StatusCode, text(body))
	}
	recs := e.deployments(t, "shop")
	if len(recs) != 1 || location(t, resp) != "/apps/shop/deployments#deployment-"+recs[0].Deployment.ID {
		t.Fatalf("create went to %q with %d deployments, want the deployment", location(t, resp), len(recs))
	}
	if recs[0].Deployment.Trigger != app.TriggerConsole {
		t.Errorf("trigger = %q, want console", recs[0].Deployment.Trigger)
	}

	// The stored app is the app the service previews from the same request.
	req := compose.Request{Compose: shopCompose, Services: map[string]compose.Overlay{
		"web": {Domains: []compose.Domain{{Host: "shop.example.com"}}, Health: &compose.Health{Path: "/"},
			Secrets: []app.Secret{{Name: "API_KEY"}}},
		"db": {Backup: &app.Backup{Engine: "postgres", Keep: 5}, Secrets: []app.Secret{{Name: "POSTGRES_PASSWORD"}}},
	}}
	want, _, err := e.Svc.PreviewCompose(context.Background(), "shop", req)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.storedApp(t, "shop"); !reflect.DeepEqual(got, want) {
		t.Errorf("stored app\n got %+v\nwant %+v", got, want)
	}
	// The values typed went to the secret store, not into the stored app or any page.
	e.waitDeploy(t, recs[0].Deployment.ID)
	if got := e.Nomad.Variable("nomad/jobs/shop/web")["API_KEY"]; got != aSecret {
		t.Errorf("web's API_KEY in the secret store = %q, want the value typed", got)
	}
	if got := e.Nomad.Variable("nomad/jobs/shop/db")["POSTGRES_PASSWORD"]; got != aSecret+"-db" {
		t.Errorf("db's POSTGRES_PASSWORD in the secret store = %q, want the value typed", got)
	}
}

// A secret value typed on a Compose form is in no response, whatever refused the post.
func TestComposeNeverEchoesSecrets(t *testing.T) {
	const missingImage = "services:\n  web:\n    image: ghcr.io/acme/missing:1\n    expose: [\"80\"]\n"
	typed := func(file string) url.Values {
		return url.Values{
			"name": {"shop"}, "compose": {file},
			"svc.web.overlay":          {"1"},
			"svc.web.secret.API_KEY":   {aSecret},
			"svc.web.newSecret.0":      {"TOKEN"},
			"svc.web.newSecretValue.0": {aSecret + "-2"},
			"svc.web.domains":          {"bad domain:xx"}, // used by the overlay case
		}
	}
	notice := "Secret values aren't kept; type them again."

	tests := []struct {
		name, path string
		form       func() url.Values
		status     int
		note       bool // the page says the values were dropped
	}{
		{"a preview", "/apps/new/compose/preview", func() url.Values {
			f := typed(shopCompose)
			f.Set("svc.web.domains", "shop.example.com")
			return f
		}, http.StatusOK, true},
		{"a refused overlay", "/apps/new/compose", func() url.Values { return typed(shopCompose) }, http.StatusUnprocessableEntity, true},
		{"an image the registry doesn't have", "/apps/new/compose", func() url.Values {
			f := typed(missingImage)
			f.Set("svc.web.domains", "")
			return f
		}, http.StatusUnprocessableEntity, true},
		{"a name that is taken", "/apps/new/compose", func() url.Values {
			f := typed(shopCompose)
			f.Set("name", "whoami")
			f.Set("svc.web.domains", "")
			return f
		}, http.StatusConflict, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := composeEnv(t)
			e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1})
			resp, body := e.postForm(t, tc.path, tc.form())
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d:\n%s", resp.StatusCode, tc.status, text(body))
			}
			if strings.Contains(body, aSecret) {
				t.Errorf("the response holds a secret value:\n%s", body)
			}
			if got := strings.Contains(text(body), notice); got != tc.note {
				t.Errorf("says the values were dropped = %v, want %v", got, tc.note)
			}
			// The names stay, as inputs that start empty.
			for _, in := range passwordInput.FindAllString(body, -1) {
				if strings.Contains(in, ` value=`) {
					t.Errorf("a password input has a value: %s", in)
				}
			}
		})
	}

	t.Run("and the refused create saved nothing", func(t *testing.T) {
		e := composeEnv(t)
		e.postForm(t, "/apps/new/compose", typed(shopCompose)) // its domain is bad
		if _, err := e.Store.GetApp(context.Background(), "shop"); err == nil {
			t.Error("a refused create saved the app")
		}
	})
}

func TestComposeCreateRefusals(t *testing.T) {
	t.Run("an overlay the model refuses lands on its input", func(t *testing.T) {
		e := composeEnv(t)
		_, page := e.previewNew(t, "shop", shopCompose)
		values := composeFormOf(t, page, url.Values{"svc.web.healthInterval": {"5"}, "svc.web.healthTimeout": {"9"}})
		resp, body := e.postForm(t, "/apps/new/compose", values)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `aria-describedby="svc-web-healthTimeout-error"`) {
			t.Errorf("create = %d; want 422 with the error on the timeout:\n%s", resp.StatusCode, text(body))
		}
		if _, err := e.Store.GetApp(context.Background(), "shop"); err == nil {
			t.Error("a refused create saved the app")
		}
	})

	t.Run("a backup setting without an engine shows its error", func(t *testing.T) {
		e := composeEnv(t)
		_, page := e.previewNew(t, "shop", shopCompose)
		values := composeFormOf(t, page, url.Values{"svc.db.backupEngine": {""}, "svc.db.backupKeep": {"3"}})
		resp, body := e.postForm(t, "/apps/new/compose", values)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `aria-describedby="svc-db-backupEngine-error"`) ||
			!strings.Contains(text(body), "Choose postgres") {
			t.Errorf("create = %d; want 422 with the error on the engine:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("a secret with no value yet is refused naming it", func(t *testing.T) {
		e := composeEnv(t)
		_, page := e.previewNew(t, "shop", shopCompose)
		values := composeFormOf(t, page, url.Values{"svc.web.newSecret.0": {"API_KEY"}})
		resp, body := e.postForm(t, "/apps/new/compose", values)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "API_KEY") ||
			!strings.Contains(body, `id="svc-web-secret-API_KEY"`) || !strings.Contains(body, `aria-describedby="svc-web-secret-API_KEY-error"`) {
			t.Errorf("create = %d; want 422 with the error on the API_KEY input:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("an image that doesn't exist is a refusal with its path", func(t *testing.T) {
		e := composeEnv(t)
		file := "services:\n  web:\n    image: ghcr.io/acme/missing:1\n    expose: [\"80\"]\n"
		resp, body := e.postForm(t, "/apps/new/compose", url.Values{"name": {"shop"}, "compose": {file}})
		if resp.StatusCode != http.StatusUnprocessableEntity ||
			!strings.Contains(body, `<span class="cv-inline-mono text-fg-1">services.web.image</span>`) {
			t.Errorf("create = %d; want 422 naming services.web.image:\n%s", resp.StatusCode, text(body))
		}
	})
}

func TestComposeOversizeFile(t *testing.T) {
	e := composeEnv(t)
	big := "services:\n  web:\n    image: traefik/whoami:latest\n# " + strings.Repeat("x", maxComposeForm) + "\n"
	for _, path := range []string{"/apps/new/compose/preview", "/apps/new/compose"} {
		resp, body := e.postForm(t, path, url.Values{"name": {"shop"}, "compose": {big}})
		if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(text(body), "The file is larger than 1 MiB.") ||
			!strings.Contains(body, `aria-describedby="compose-error"`) {
			t.Errorf("POST %s = %d; want 413 with the error at the file:\n%s", path, resp.StatusCode, text(body))
		}
	}
	// A file over what the translator reads but within the form's limit is refused at the file too.
	mid := "services:\n  web:\n    image: traefik/whoami:latest\n# " + strings.Repeat("x", compose.MaxComposeBytes) + "\n"
	resp, body := e.postForm(t, "/apps/new/compose/preview", url.Values{"name": {"shop"}, "compose": {mid}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `aria-describedby="compose-error"`) {
		t.Errorf("a %d byte file = %d; want 422 at the file:\n%s", len(mid), resp.StatusCode, text(body))
	}
}

// htmx posts the form in place: a preview answers the form alone, a good
// create asks the browser to go to the deployment.
func TestComposeHTMX(t *testing.T) {
	e := composeEnv(t)
	hx := map[string]string{"Sec-Fetch-Site": "same-origin", "HX-Request": "true"}
	post := func(path string, form url.Values) (*http.Response, string) {
		return e.do(t, request{method: http.MethodPost, path: path, form: form, cookies: []*http.Cookie{e.Cookie}, headers: hx})
	}

	resp, body := post("/apps/new/compose/preview", url.Values{"name": {"shop"}, "compose": {shopCompose}})
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "<html") || !strings.HasPrefix(strings.TrimSpace(body), `<form id="compose-form"`) {
		t.Fatalf("an htmx preview = %d, want the form alone:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `hx-post="/apps/new/compose"`) {
		t.Error("the second button doesn't post in place")
	}

	values := composeFormOf(t, body, nil)
	resp, body = post("/apps/new/compose", values)
	recs := e.deployments(t, "shop")
	if resp.StatusCode != http.StatusNoContent || len(recs) != 1 ||
		resp.Header.Get("HX-Redirect") != "/apps/shop/deployments#deployment-"+recs[0].Deployment.ID {
		t.Fatalf("an htmx create = %d with HX-Redirect %q:\n%s", resp.StatusCode, resp.Header.Get("HX-Redirect"), body)
	}

	// A refusal swaps the form with its errors, status 422, no page around it.
	resp, body = post("/apps/new/compose/preview", url.Values{"name": {"shop"}, "compose": {shopCompose}})
	if resp.StatusCode != http.StatusConflict || strings.Contains(body, "<html") || !strings.Contains(text(body), "already exists") {
		t.Errorf("an htmx preview of a taken name = %d:\n%s", resp.StatusCode, body)
	}
}

// Feature 015: Deploy from Compose of an app that keeps its file opens filled
// from it, as Configure does, and never draws a secret value.
func TestComposeDeployFormKeptFile(t *testing.T) {
	e, revision := keptSentinelEnv(t)
	resp, body := e.get(t, "/apps/shop/compose")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(body))
	}
	f := formWith(t, body, "/apps/shop/compose/preview")
	if got := f.Values.Get("compose"); got != keptShopFile {
		t.Errorf("the textarea = %q, want the kept file", got)
	}
	for name, want := range map[string]string{
		"revision":               revision,
		"svc.web.domains":        "shop.example.com",
		"svc.web.healthPath":     "/health",
		"svc.web.secret.API_KEY": "",
		"svc.db.backupKeep":      "5",
	} {
		if got, ok := f.Values[name]; !ok || got[0] != want {
			t.Errorf("input %s = %v, want %q", name, got, want)
		}
	}
	for _, want := range []string{`wrap="off"`, "Put passwords and keys in secrets below, not in the file."} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, sentinel015) {
		t.Error("the page holds a secret value")
	}

	// An app with no kept file opens empty, as in 014.
	legacy, _ := shopEnv(t)
	_, body = legacy.get(t, "/apps/shop/compose")
	if f := formWith(t, body, "/apps/shop/compose/preview"); f.Values.Get("compose") != "" {
		t.Errorf("the textarea of an app with no kept file = %q, want it empty", f.Values.Get("compose"))
	}
}

// Feature 015, US4: a refused deploy of an app that keeps its file redraws what
// was typed, the file included, but never a secret value: not the one typed
// and not the one the app has.
func TestComposeDeployRefusedKeptFileEchoesNoSecret(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	_, page := e.get(t, "/apps/shop/configure")
	typed := shopFormOf(t, page, url.Values{
		"svc.web.secret.API_KEY":   {sentinel015 + "-typed"},
		"svc.web.newSecret.0":      {"TOKEN"},
		"svc.web.newSecretValue.0": {sentinel015 + "-new"},
		"svc.web.domains":          {"bad domain:xx"},
	})

	resp, body := e.postForm(t, "/apps/shop/compose", typed)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a refused deploy = %d, want 422:\n%s", resp.StatusCode, text(body))
	}
	if got := formWith(t, body, "/apps/shop/compose/preview").Values.Get("compose"); got != keptShopFile {
		t.Errorf("the redrawn file = %q, want what was typed", got)
	}
	if strings.Contains(body, sentinel015) || strings.Contains(body, storedSecret) {
		t.Errorf("the refused page holds a secret value:\n%s", text(body))
	}
	if stored, _ := e.Store.GetApp(context.Background(), "shop"); stored.Compose == nil || stored.Compose.Compose != keptShopFile {
		t.Errorf("a refused deploy changed the kept file: %+v", stored.Compose)
	}
}

// Feature 015: Configure's form is drawn with the services filled in, so the
// owner can change one (a domain) and deploy at once, without a preview. The
// deploy checks the file itself; the change is kept and deployed.
func TestConfigureKeptFileDeployWithoutPreview(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	_, page := e.get(t, "/apps/shop/configure")
	resp, body := e.postForm(t, "/apps/shop/compose", shopFormOf(t, page, url.Values{"svc.web.domains": {"edited.example.com"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	recs := e.deployments(t, "shop")
	e.waitDeploy(t, recs[0].Deployment.ID)

	rec, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	web := rec.Compose.Services["web"]
	if len(web.Domains) != 1 || web.Domains[0].Host != "edited.example.com" {
		t.Errorf("kept overlay domains = %+v, want edited.example.com", web.Domains)
	}
	for _, svc := range rec.App.Services {
		if svc.Name == "web" && (len(svc.Domains) != 1 || svc.Domains[0].Host != "edited.example.com") {
			t.Errorf("app domains = %+v, want edited.example.com", svc.Domains)
		}
	}
}

// Feature 015, US1: edit the kept file on Configure, preview, deploy. The edit
// is kept; a second tab that still holds the old file is refused as stale.
func TestConfigureKeptFileEditFlow(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	_, page := e.get(t, "/apps/shop/configure")
	const edited = "# shop stack, edited\n" + shopCompose
	first := shopFormOf(t, page, url.Values{"compose": {edited}})
	secondTab := shopFormOf(t, page, url.Values{"compose": {"# a second tab\n" + shopCompose}})

	resp, body := e.postForm(t, "/apps/shop/compose/preview", first)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, ">Deploy</button>") {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/apps/shop/compose", shopFormOf(t, body, nil))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	recs := e.deployments(t, "shop")
	e.waitDeploy(t, recs[0].Deployment.ID)

	rec, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil || rec.Compose == nil || rec.Compose.Compose != edited {
		t.Fatalf("kept file = %+v, %v; want the edited one", rec.Compose, err)
	}
	if recs[0].Compose == nil || recs[0].Compose.Compose != edited {
		t.Errorf("the deployment's file = %+v, want the edited one", recs[0].Compose)
	}
	_, body = e.get(t, "/apps/shop/configure")
	if got := formWith(t, body, "/apps/shop/compose/preview").Values.Get("compose"); got != edited {
		t.Errorf("Configure shows %q, want the edited file", got)
	}
	if strings.Contains(body, sentinel015) {
		t.Error("Configure holds a secret value")
	}

	// The other tab's deploy is refused, and its typed file comes back.
	resp, body = e.postForm(t, "/apps/shop/compose", secondTab)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app changed since you opened the form.") {
		t.Fatalf("a stale deploy = %d; want 409 with the stale panel:\n%s", resp.StatusCode, text(body))
	}
	if got := formWith(t, body, "/apps/shop/compose/preview").Values.Get("compose"); got != "# a second tab\n"+shopCompose {
		t.Errorf("the stale form's file = %q, want what was typed", got)
	}
	if strings.Contains(body, sentinel015) {
		t.Error("the stale page holds a secret value")
	}
}
