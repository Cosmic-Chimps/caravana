package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/templates"
	"golang.org/x/crypto/bcrypt"
)

const (
	tplCatVersion = "2027.01.01"
	tplLogo       = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#336699"/></svg>`
	kumaDigest    = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// demoTemplate is a template the fake registry resolves (traefik/whoami): a generated password, a
// secret input, a logo and a backup.
func demoTemplate() templates.Template {
	secret := func(s string) *string { return &s }
	return templates.Template{
		Name: "demo", Title: "Demo", Version: "1.0-1", Description: "A demo app.", Tags: []string{"demo", "web"},
		Upstream: "https://example.com/demo", AppName: "demo", Logo: tplLogo,
		Notes: "Open https://{{domain}} now.\nThen sign in.",
		Inputs: []templates.Input{
			{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "demo.example.com", Help: "Its DNS must point here."},
			{Name: "api_key", Label: "API key", Type: templates.TypeSecret, Example: "example-key"},
		},
		Generated: []templates.Generated{{Name: "db_password", Length: 32, Alphabet: templates.AlphabetBase64URL}},
		Services: map[string]compose.Overlay{"web": {
			Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}},
			Health:  &compose.Health{Path: "/"},
			Secrets: []app.Secret{{Name: "DB_PASSWORD", Value: secret("{{db_password}}")}, {Name: "API_KEY", Value: secret("{{api_key}}")}},
			Backup:  &app.Backup{Engine: app.BackupVolume},
		}},
		Compose: "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n    volumes:\n      - data:/data\nvolumes:\n  data: {}\n",
	}
}

// useCatalog makes ts the catalog in use, as a fetched one stored in the row.
func useCatalog(t *testing.T, e *consoleEnv, ts ...templates.Template) {
	t.Helper()
	bundle, err := json.Marshal(templates.Catalog{Format: templates.Format, Version: tplCatVersion, Templates: ts})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	row := store.TemplateCatalogRow{Bundle: bundle, BundleSum: templates.SumOf(bundle), Version: tplCatVersion, FetchedAt: now, CheckedAt: now, CheckedOKAt: now}
	if err := e.Store.SaveTemplateCheck(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateGrid(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/apps/new?from=template")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/new?from=template = %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`href="/apps/new?from=template" class="cv-tab" aria-current="page">From a template</a>`,
		`href="/apps/new" class="cv-tab">Image</a>`,
		`href="/apps/new/template/uptime-kuma"`, `href="/apps/new/template/hollo"`, `href="/apps/new/template/codemagic-patch"`,
		`>Uptime Kuma</span>`, `>2.5.11-1</span>`, `class="cv-chip">monitoring</span>`,
		`placeholder="Search templates"`, `action="/templates/check"`, `action="/templates/settings"`,
		`Stop checking for new templates`, `, built in.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the grid lacks %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, `class="cv-panel cv-tplcard"`); n != 6 {
		t.Errorf("%d cards, want the six built in", n)
	}
	// No logo yet: each card is a monogram tile in its hue, never an image.
	if strings.Contains(body, `<img class="cv-tpltile`) || !strings.Contains(body, `class="cv-tpltile cv-hue-`) {
		t.Errorf("the monogram tiles are missing:\n%s", body)
	}
	if strings.Contains(body, "hx-") {
		t.Error("the grid needs no script: it is links and plain forms")
	}

	t.Run("a search and a tag narrow the grid, without script", func(t *testing.T) {
		_, body := e.get(t, "/apps/new?from=template&q=status")
		if strings.Count(body, `class="cv-panel cv-tplcard"`) != 1 || !strings.Contains(body, "Uptime Kuma") || strings.Contains(body, ">Hollo<") {
			t.Errorf("q=status:\n%s", body)
		}
		_, body = e.get(t, "/apps/new?from=template&tag=database")
		if strings.Count(body, `class="cv-panel cv-tplcard"`) != 2 || !strings.Contains(body, ">Adminer<") || !strings.Contains(body, ">PostgreSQL<") {
			t.Errorf("tag=database:\n%s", body)
		}
		// Every tag stays a link, the chosen one is marked, and All clears it.
		if !strings.Contains(body, `href="/apps/new?from=template&amp;tag=database" class="cv-tpltag" aria-current="true">database</a>`) ||
			!strings.Contains(body, `class="cv-tpltag">monitoring</a>`) {
			t.Errorf("the tag links:\n%s", body)
		}
		_, body = e.get(t, "/apps/new?from=template&q=zzzz")
		if !strings.Contains(body, "No template matches.") || !strings.Contains(body, `href="/apps/new?from=template"`) {
			t.Errorf("an empty search:\n%s", body)
		}
	})

	t.Run("a template that is broken isn't offered", func(t *testing.T) {
		e := newConsoleEnv(t)
		broken := demoTemplate()
		broken.Name, broken.Broken = "broken", "its image moved"
		fine := demoTemplate()
		fine.Name = "fine"
		useCatalog(t, e, broken, fine)
		_, body := e.get(t, "/apps/new?from=template")
		if strings.Contains(body, "/apps/new/template/broken") || !strings.Contains(body, "/apps/new/template/fine") ||
			!strings.Contains(body, "fetched ") || !strings.Contains(body, tplCatVersion) {
			t.Errorf("grid:\n%s", body)
		}
		resp, body := e.get(t, "/apps/new/template/broken")
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "This template isn't offered: its image moved.") {
			t.Errorf("GET a broken template = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("the place stays chosen from tab to tab", func(t *testing.T) {
		e := newConsoleEnv(t)
		if _, err := e.Svc.CreateProject(context.Background(), "shop", "Shop"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Svc.CreateEnvironment(context.Background(), "shop", "staging"); err != nil {
			t.Fatal(err)
		}
		_, body := e.get(t, "/apps/new?from=template&project=shop&environment=staging")
		for _, want := range []string{`href="/apps/new/template/hollo?environment=staging&amp;project=shop"`,
			`name="project" value="shop"`, `href="/apps/new?from=template&amp;environment=staging&amp;project=shop"`} {
			if !strings.Contains(body, want) {
				t.Errorf("the grid lacks %q:\n%s", want, body)
			}
		}
	})
}

func TestTemplateCatalogFooterPosts(t *testing.T) {
	e := newConsoleEnv(t)
	cs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	t.Cleanup(cs.Close)
	e.Svc.SetTemplatesURL(cs.URL)

	resp, _ := e.postForm(t, "/templates/check", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/new?checked=1&from=template" {
		t.Fatalf("Check now = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, body := e.get(t, "/apps/new?from=template&checked=1")
	if !strings.Contains(text(body), "Checking for new templates.") {
		t.Errorf("the notice:\n%s", text(body))
	}
	// A second check is too soon: the page is drawn again with the reason, and 429.
	resp, body = e.postForm(t, "/templates/check", url.Values{})
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(text(body), "ran less than 10 minutes ago") {
		t.Errorf("a second check = %d:\n%s", resp.StatusCode, text(body))
	}

	// The switch.
	resp, _ = e.postForm(t, "/templates/settings", url.Values{"checking": {"off"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/new?from=template&saved=1" {
		t.Fatalf("stop checking = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, body = e.get(t, "/apps/new?from=template&saved=1")
	if !strings.Contains(body, "Check for new templates daily") || strings.Contains(body, `action="/templates/check"`) ||
		!strings.Contains(text(body), "won't check for new templates") {
		t.Errorf("with checking off:\n%s", body)
	}
	resp, body = e.postForm(t, "/templates/check", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "turned off") {
		t.Errorf("a check with checking off = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, _ = e.postForm(t, "/templates/settings", url.Values{"checking": {"maybe"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad setting = %d", resp.StatusCode)
	}
	resp, _ = e.postForm(t, "/templates/settings", url.Values{"checking": {"on"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("checking on = %d", resp.StatusCode)
	}
	// Posts need a session and come from the console's own pages.
	if resp, _ := e.do(t, request{method: http.MethodPost, path: "/templates/check"}); resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther && strings.Contains(resp.Header.Get("Location"), "checked") {
		t.Errorf("a post with no session = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestTemplateLogoRoute(t *testing.T) {
	e := newConsoleEnv(t)
	useCatalog(t, e, demoTemplate())
	resp, body := e.get(t, "/templates/demo/logo.svg")
	if resp.StatusCode != http.StatusOK || body != tplLogo {
		t.Fatalf("logo = %d %q", resp.StatusCode, body)
	}
	for k, v := range map[string]string{"Content-Type": "image/svg+xml", "X-Content-Type-Options": "nosniff",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; sandbox"} {
		if resp.Header.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, resp.Header.Get(k), v)
		}
	}
	// The grid and the page draw it as an image.
	_, grid := e.get(t, "/apps/new?from=template")
	if !strings.Contains(grid, `<img class="cv-tpltile cv-tpltile-img" src="/templates/demo/logo.svg" alt="" width="40" height="40">`) {
		t.Errorf("the grid doesn't draw the logo as an image:\n%s", grid)
	}
	if resp, _ := e.get(t, "/templates/nope/logo.svg"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown template's logo = %d", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/templates/hollo/logo.svg"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a template without a logo = %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, request{path: "/templates/demo/logo.svg"}); resp.StatusCode == http.StatusOK {
		t.Error("the logo is served without a session")
	}
}

func TestTemplatePage(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/apps/new/template/hollo")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET the Hollo page = %d:\n%s", resp.StatusCode, body)
	}
	page := text(body)
	for _, want := range []string{
		"Hollo 0.9.25-1", "A single-user fediverse server", "What it makes",
		"hollo ghcr.io/fedify-dev/hollo:0.9.25 reached at your domain",
		"postgres postgres:17 backed up (postgres)",
		"caravana makes 2 passwords for it, kept in the secrets DATABASE_URL, POSTGRES_PASSWORD, SECRET_KEY.",
		"They go to the app's secret store and are never shown.",
		"Open https://<your domain>/setup first",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q:\n%s", want, page)
		}
	}
	for _, want := range []string{
		`href="/apps/new?from=template"`, `href="https://github.com/fedify-dev/hollo" rel="noopener noreferrer"`,
		`action="/apps/new/template/hollo"`, `name="name" value="hollo"`,
		`id="in_domain" type="text" name="in_domain"`, `inputmode="url"`, `placeholder="e.g. social.example.com"`, `autocomplete="off"`,
		`>Create</button>`, `name="action" value="preview"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "hx-") || strings.Contains(body, "target=") {
		t.Error("the page needs no script, and the upstream link opens in place")
	}

	t.Run("no backup destination: the notice and the checkbox", func(t *testing.T) {
		for _, want := range []string{
			"This template backs up postgres. No backup destination is set yet.",
			`href="/settings/backup-storage"`, `type="checkbox" id="withoutBackups" name="withoutBackups"`, "Install without backups for now",
		} {
			if !strings.Contains(page, want) && !strings.Contains(body, want) {
				t.Errorf("the page lacks %q:\n%s", want, page)
			}
		}
	})
	t.Run("a destination is set: no notice and no checkbox", func(t *testing.T) {
		e := newConsoleEnv(t)
		if _, err := e.Svc.SetBackupDestination(context.Background(), service.DestinationInput{Endpoint: "http://10.0.0.5:9000", Region: "r", Bucket: "backups", AccessKeyID: "AK", SecretAccessKey: new("sk")}); err != nil {
			t.Fatal(err)
		}
		_, body := e.get(t, "/apps/new/template/hollo")
		if strings.Contains(body, "No backup destination") || strings.Contains(body, "withoutBackups") {
			t.Errorf("the page still asks:\n%s", text(body))
		}
	})
	t.Run("a template with no backup never asks", func(t *testing.T) {
		_, body := e.get(t, "/apps/new/template/adminer")
		if strings.Contains(body, "withoutBackups") || strings.Contains(body, "No backup destination") {
			t.Errorf("adminer asks about backups:\n%s", text(body))
		}
	})
	t.Run("secret inputs are password fields, empty, with the new-password hint", func(t *testing.T) {
		_, body := e.get(t, "/apps/new/template/postgres")
		if !strings.Contains(body, `type="password" name="in_db_password" value=""`) || !strings.Contains(body, `autocomplete="new-password"`) {
			t.Errorf("the secret input:\n%s", body)
		}
		if strings.Contains(text(body), "secrets aren't kept in the form") {
			t.Error("the hint is for a form drawn again after a post")
		}
	})
	t.Run("an unknown template", func(t *testing.T) {
		if resp, _ := e.get(t, "/apps/new/template/nope"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET an unknown template = %d", resp.StatusCode)
		}
	})
}

// installForm is what a browser sends for a template page.
func installForm(name string, answers map[string]string, more url.Values) url.Values {
	v := url.Values{"name": {name}}
	for k, a := range answers {
		v.Set("in_"+k, a)
	}
	for k, a := range more {
		v[k] = a
	}
	return v
}

func TestInstallFromTemplatePage(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnvWith(t, nil)
	e.Reg.AddIndex("louislam/uptime-kuma", "2.5.11", kumaDigest)

	t.Run("a refused answer comes back at its input, nothing is made", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/new/template/uptime-kuma", installForm("status", map[string]string{"domain": "not a domain"}, url.Values{"withoutBackups": {"1"}}))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d:\n%s", resp.StatusCode, text(body))
		}
		if !strings.Contains(body, `id="in_domain-error"`) || !strings.Contains(text(body), "This answer must be a host name like status.example.com.") ||
			!strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, `value="not a domain"`) {
			t.Errorf("the error isn't at the input:\n%s", body)
		}
		if _, err := e.Store.GetApp(ctx, "status"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an app was made: %v", err)
		}
	})
	t.Run("no backup destination and no choice: the refusal says what to do", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/new/template/uptime-kuma", installForm("status", map[string]string{"domain": "status.example.com"}, nil))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "No backup destination is set, so a backup has nowhere to go.") ||
			!strings.Contains(text(body), "or install without backups for now") {
			t.Errorf("= %d:\n%s", resp.StatusCode, text(body))
		}
		if !strings.Contains(body, `name="withoutBackups"`) {
			t.Error("the checkbox isn't on the page")
		}
	})
	t.Run("a name that is bad", func(t *testing.T) {
		for name, want := range map[string]string{"": "Enter a name for the app.", strings.Repeat("a", 32): "at most 31 characters"} {
			resp, body := e.postForm(t, "/apps/new/template/uptime-kuma", installForm(name, map[string]string{"domain": "status.example.com"}, nil))
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), want) {
				t.Errorf("name %q = %d:\n%s", name, resp.StatusCode, text(body))
			}
		}
		resp, body := e.postForm(t, "/apps/new/template/uptime-kuma", installForm("Bad Name", map[string]string{"domain": "status.example.com"}, url.Values{"withoutBackups": {"1"}}))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="name-error"`) {
			t.Errorf("a bad name = %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("a preview shows what it would make and makes nothing", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/new/template/uptime-kuma", installForm("status", map[string]string{"domain": "status.example.com"},
			url.Values{"withoutBackups": {"1"}, "action": {"preview"}}))
		page := text(body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, "With these answers it makes:") ||
			!strings.Contains(page, "uptime-kuma louislam/uptime-kuma:2.5.11 reached at your domain") ||
			!strings.Contains(page, "Backups are off for uptime-kuma: set a backup destination in Settings › Backup storage") ||
			!strings.Contains(page, "Nothing was made") {
			t.Errorf("preview = %d:\n%s", resp.StatusCode, page)
		}
		if strings.Contains(body, `class="cv-chip">backed up (volume)`) && strings.Contains(page, "With these answers it makes: uptime-kuma louislam/uptime-kuma:2.5.11 reached at your domain backed up") {
			t.Error("the preview shows a backup that was turned off")
		}
		if !strings.Contains(body, `value="status.example.com"`) || !strings.Contains(body, `name="withoutBackups" value="1" checked`) {
			t.Errorf("the form lost what was typed:\n%s", body)
		}
		if _, err := e.Store.GetApp(ctx, "status"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a preview made an app: %v", err)
		}
	})

	t.Run("Create without backups installs, lands on Deployments with the notice and the notes", func(t *testing.T) {
		resp, _ := e.postForm(t, "/apps/new/template/uptime-kuma", installForm("status", map[string]string{"domain": "Status.Example.com"}, url.Values{"withoutBackups": {"1"}}))
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/status/deployments?created=template&v_domain=status.example.com" {
			t.Fatalf("status = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		rec, err := e.Store.GetApp(ctx, "status")
		if err != nil || rec.Template.Name != "uptime-kuma" || rec.App.Services[0].Backup != nil {
			t.Fatalf("the app = %+v, %v", rec, err)
		}
		_, body := e.get(t, location(t, resp))
		page := text(body)
		for _, want := range []string{"Installed from the Uptime Kuma template.",
			"Open https://status.example.com and create the admin account",
			"Backups are off for uptime-kuma",
			"Other apps in this environment reach it at status:3001 or status-uptime-kuma:3001."} {
			if !strings.Contains(page, want) {
				t.Errorf("the Deployments tab lacks %q:\n%s", want, page)
			}
		}
		// The notice is wrapped so the mark and the text share a line, and the address lines sit in its text, under the title.
		if !strings.Contains(body, `<div class="cv-main"><div role="status" class="cv-notice"><span class="cv-mark cv-mark-healthy" aria-hidden="true"></span><span>Installed from the Uptime Kuma template.<span class="cv-notice-addr"><span class="cv-addr">`) {
			t.Errorf("the installed notice isn't wrapped or lacks its address lines:\n%s", body)
		}
		if strings.Contains(body, "cv-main cv-notice") {
			t.Error("a notice still shares its element with cv-main")
		}
		// Configure says where the app came from.
		_, conf := e.get(t, "/apps/status/configure")
		if !strings.Contains(text(conf), "Template · Uptime Kuma 2.5.11-1") {
			t.Errorf("Configure lacks the template fact:\n%s", text(conf))
		}
		// The notice is for a template app only, and is made of what the address carries only when it passes the checks.
		_, other := e.get(t, "/apps/status/deployments?created=template&v_domain=bad%20value")
		if strings.Contains(text(other), "bad value") || !strings.Contains(text(other), "<your domain>") {
			t.Errorf("a bad answer in the address reached the notes:\n%s", text(other))
		}
		// A well-formed answer the app doesn't hold (a crafted link) isn't shown either.
		_, crafted := e.get(t, "/apps/status/deployments?created=template&v_domain=attacker.example")
		if strings.Contains(text(crafted), "attacker.example") || !strings.Contains(text(crafted), "<your domain>") {
			t.Errorf("an answer the app doesn't hold reached the notes:\n%s", text(crafted))
		}
		if _, plain := e.get(t, "/apps/status/deployments"); strings.Contains(text(plain), "Installed from the") {
			t.Error("the notice shows without ?created=template")
		}
	})
	t.Run("the name is taken now", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/new/template/uptime-kuma", installForm("status", map[string]string{"domain": "other.example.com"}, url.Values{"withoutBackups": {"1"}}))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "An app called status already exists.") {
			t.Errorf("= %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("the domain is another app's: refused at the domain input", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/new/template/uptime-kuma", installForm("status2", map[string]string{"domain": "status.example.com"}, url.Values{"withoutBackups": {"1"}}))
		if resp.StatusCode != http.StatusConflict || !strings.Contains(body, `id="in_domain-error"`) || !strings.Contains(text(body), "That domain is already used by status.") {
			t.Errorf("= %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("a post needs a session", func(t *testing.T) {
		resp, _ := e.do(t, request{method: http.MethodPost, path: "/apps/new/template/uptime-kuma", form: installForm("x", map[string]string{"domain": "x.example.com"}, nil)})
		if resp.StatusCode == http.StatusSeeOther && strings.Contains(resp.Header.Get("Location"), "created=template") {
			t.Error("installed without a session")
		}
	})
}

// sentinelReader yields the bytes that make the base64url alphabet's characters spell the sentinel.
type sentinelReader struct {
	mu   sync.Mutex
	data []byte
}

func newSentinelReader(sentinel string) *sentinelReader {
	const base64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	r := &sentinelReader{}
	for _, c := range sentinel {
		r.data = append(r.data, byte(strings.IndexRune(base64url, c)))
	}
	r.data = append(r.data, make([]byte, 64)...)
	return r
}

func (r *sentinelReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := copy(p, r.data)
	r.data = r.data[n:]
	if n == 0 {
		return 0, errors.New("the sentinel reader is empty")
	}
	return n, nil
}

// TestTemplatePagesKeepSecretsOut: a generated secret and a typed secret input are in no page the console
// draws, not the form that comes back after a refusal, the preview, the page after the install or an address
// (FR-003). The form is drawn again without what was typed in a secret field and says so.
func TestTemplatePagesKeepSecretsOut(t *testing.T) {
	const generated = "s3cret-sentinel-035-generated-xx" // 32 characters of the base64url alphabet
	const typed = "s3cret-sentinel-035-typed"
	e := newConsoleEnvWith(t, func(s *service.Service, _ *deploytest.Nomad) { s.SetTemplateRandom(newSentinelReader(generated)) })
	useCatalog(t, e, demoTemplate())

	var pages []string
	var locations []string
	record := func(what string, resp *http.Response, body string) {
		pages = append(pages, what+"\n"+body)
		locations = append(locations, resp.Header.Get("Location"))
	}
	post := func(what string, form url.Values) *http.Response {
		resp, body := e.postForm(t, "/apps/new/template/demo", form)
		record(what, resp, body)
		return resp
	}

	// A refusal draws the form again: the secret field is empty and says to type it again.
	resp, body := e.postForm(t, "/apps/new/template/demo", installForm("demo", map[string]string{"domain": "not a domain", "api_key": typed}, url.Values{"withoutBackups": {"1"}}))
	record("a refused post", resp, body)
	if !strings.Contains(body, `type="password" name="in_api_key" value=""`) || !strings.Contains(text(body), "Type it again: secrets aren't kept in the form.") {
		t.Errorf("the secret field after a refusal:\n%s", body)
	}
	post("a preview", installForm("demo", map[string]string{"domain": "demo.example.com", "api_key": typed}, url.Values{"action": {"preview"}, "withoutBackups": {"1"}}))
	post("a post with a newline in the secret", installForm("demo", map[string]string{"domain": "demo.example.com", "api_key": "a\n" + typed}, url.Values{"withoutBackups": {"1"}}))
	resp = post("the install", installForm("demo", map[string]string{"domain": "demo.example.com", "api_key": typed}, url.Values{"withoutBackups": {"1"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("install = %d", resp.StatusCode)
	}
	next := resp.Header.Get("Location")
	resp, body = e.get(t, next)
	record("the page after the install", resp, body)
	for _, path := range []string{"/apps/new?from=template", "/apps/new/template/demo", "/apps/demo", "/apps/demo/configure", "/apps/demo/deployments",
		"/apps/demo/variables", "/apps/demo/backups", "/apps", "/settings/backup-storage"} {
		resp, body = e.get(t, path)
		record(path, resp, body)
	}
	resp, body = e.get(t, "/apps/demo/deployments/"+deploymentIDOf(t, e, "demo")+"/compose")
	record("the kept Compose file", resp, body)

	for _, p := range pages {
		for _, secret := range []string{generated, typed} {
			if strings.Contains(p, secret) {
				t.Errorf("a page holds %q: %.300s", secret, p)
			}
		}
	}
	for _, l := range locations {
		for _, secret := range []string{generated, typed, url.QueryEscape(typed)} {
			if strings.Contains(l, secret) {
				t.Errorf("an address holds %q: %s", secret, l)
			}
		}
	}
	if strings.Contains(e.Logs.String(), generated) || strings.Contains(e.Logs.String(), typed) {
		t.Error("the log holds a secret")
	}
	files, _ := filepath.Glob(filepath.Join(e.Dir, "caravana.db*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, secret := range []string{generated, typed} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s holds %q", filepath.Base(f), secret)
			}
		}
	}
}

// deploymentIDOf is the ID of the app's newest deployment.
func deploymentIDOf(t *testing.T, e *consoleEnv, id string) string {
	t.Helper()
	recs, err := e.Svc.ListDeployments(context.Background(), id, 1)
	if err != nil || len(recs) == 0 {
		t.Fatalf("deployments of %s: %v, %v", id, recs, err)
	}
	return recs[0].Deployment.ID
}

// TestInstallShowsTeamOnlyPasswordOnce installs Adminer, whose domain is Team only: the answer to
// Create is the Deployments tab with the password once (never cached), and no later page has it.
func TestInstallShowsTeamOnlyPasswordOnce(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnvWith(t, nil)
	e.Reg.AddIndex("library/adminer", "5.5.1", kumaDigest)

	resp, body := e.postForm(t, "/apps/new/template/adminer", installForm("adminer", map[string]string{"domain": "Adminer.Example.com"}, nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Create = %d to %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), text(body))
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	m := regexp.MustCompile(`<code class="cv-once-value" id="team-password-0">([^<]+)</code>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the answer has no password:\n%s", body)
	}
	password := m[1]
	if n := strings.Count(body, password); n != 1 {
		t.Errorf("the password is on the answer %d times, want once", n)
	}
	page := text(body)
	for _, want := range []string{"Installed from the Adminer template.", "Copy the password for adminer.example.com now.",
		"User name team.", "If it's lost, New password on the Domains page makes another.", "Adminer is at https://adminer.example.com"} {
		if !strings.Contains(page, want) {
			t.Errorf("the answer lacks %q:\n%s", want, page)
		}
	}
	if !strings.Contains(body, `data-copy="team-password-0"`) || !strings.Contains(body, `href="/apps/adminer/domains">Domains page</a>`) {
		t.Error("the password has no copy button, or the sentence no link to the Domains page")
	}
	// It is the password Caravana keeps the hash of.
	rec, err := e.Store.TeamPassword(ctx, "adminer.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(rec.Hash), []byte(password)) != nil {
		t.Error("the password shown isn't the one whose hash is kept")
	}

	// No later page has it, the installed notice included.
	for _, path := range []string{
		"/apps/adminer/deployments?created=template",
		"/apps/adminer/deployments?created=template&v_domain=adminer.example.com",
		"/apps/adminer", "/apps/adminer/deployments", "/apps/adminer/domains", "/apps/adminer/configure", "/domains",
		"/parts/apps/adminer/deployments",
	} {
		resp, later := e.get(t, path)
		if strings.Contains(later, password) {
			t.Errorf("GET %s (%d) has the password", path, resp.StatusCode)
		}
	}
	_, notice := e.get(t, "/apps/adminer/deployments?created=template&v_domain=adminer.example.com")
	if !strings.Contains(text(notice), "Installed from the Adminer template.") || strings.Contains(notice, "cv-once-value") {
		t.Errorf("the installed notice after the answer:\n%s", text(notice))
	}
}

// TestInstallShowsEachTeamOnlyPassword installs a template with two Team-only domains: each password
// is drawn once, with its own element ID and its own Copy button.
func TestInstallShowsEachTeamOnlyPassword(t *testing.T) {
	e := newConsoleEnvWith(t, nil)
	two := demoTemplate()
	two.Name, two.Title, two.AppName, two.Notes, two.Generated = "two", "Two", "two", "", nil
	two.Inputs = []templates.Input{
		{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "one.example.com"},
		{Name: "admin", Label: "Admin domain", Type: templates.TypeDomain, Example: "admin.example.com"},
	}
	two.Services = map[string]compose.Overlay{"web": {
		Domains: []compose.Domain{{Host: "{{domain}}", Port: 80, TeamOnly: true}, {Host: "{{admin}}", Port: 80, TeamOnly: true}},
		Health:  &compose.Health{Path: "/"},
	}}
	two.Compose = "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n"
	useCatalog(t, e, two)

	resp, body := e.postForm(t, "/apps/new/template/two", installForm("two", map[string]string{"domain": "one.example.com", "admin": "admin.example.com"}, nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Create = %d to %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), text(body))
	}
	// Sorted by host: admin first.
	for i, host := range []string{"admin.example.com", "one.example.com"} {
		id := "team-password-" + strconv.Itoa(i)
		m := regexp.MustCompile(`<code class="cv-once-value" id="` + id + `">([^<]+)</code>`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no password with ID %s:\n%s", id, body)
		}
		if n := strings.Count(body, m[1]); n != 1 {
			t.Errorf("the password of %s is on the answer %d times, want once", host, n)
		}
		if !strings.Contains(body, `data-copy="`+id+`" hidden>Copy password</button>`) {
			t.Errorf("no Copy button of its own for %s", id)
		}
		if !strings.Contains(text(body), "Copy the password for "+host+" now.") {
			t.Errorf("no panel names %s:\n%s", host, text(body))
		}
	}
	if strings.Contains(body, "team-password-value") || strings.Count(body, `class="cv-panel cv-once"`) != 2 {
		t.Errorf("want exactly the two indexed panels:\n%s", body)
	}
}

// certsDemoTemplate is demoTemplate with a certificate authority, a certificate kept in secret files and a
// password hash (spec 060).
func certsDemoTemplate() templates.Template {
	secret := func(s string) *string { return &s }
	t := demoTemplate()
	t.Requires = "0.24.0"
	t.Generated = nil
	t.Services = map[string]compose.Overlay{"web": {
		Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}},
		Health:  &compose.Health{Path: "/"},
		Secrets: []app.Secret{
			{Name: "API_KEY", Value: secret("{{api_key}}")},
			{Name: "CA_CERT", Value: secret("{{ca_cert}}")},
			{Name: "NODE_CERT", Value: secret("{{node_cert}}")},
			{Name: "NODE_KEY", Value: secret("{{node_key}}")},
			{Name: "USERS", Value: secret("admin:\n  hash: \"{{api_key|bcrypt}}\"\n")},
		},
		Files: []app.File{
			{Path: "/certs/ca.pem", Secret: "CA_CERT", Mode: "0444"},
			{Path: "/certs/node.pem", Secret: "NODE_CERT", Mode: "0444"},
			{Path: "/certs/node.key", Secret: "NODE_KEY", Mode: "0400"},
			{Path: "/etc/users.yml", Secret: "USERS", Mode: "0400"},
		},
	}}
	t.Compose = "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n"
	subject := func(cn string) []templates.DNPart {
		return []templates.DNPart{{Type: "CN", Value: cn}, {Type: "O", Value: "Demo"}}
	}
	t.Certificates = &templates.Certificates{
		CA:     templates.CertAuthority{Subject: subject("Demo CA")},
		Leaves: []templates.CertLeaf{{Name: "node", Subject: subject("node"), DNSNames: []string{"web"}}},
	}
	return t
}

// TestTemplatePagesKeepCertificatesOut: the key and certificates a template makes, and the hash it writes,
// are in no page the console draws, no address, no log line and no byte of the SQLite file; the secret store
// has them.
func TestTemplatePagesKeepCertificatesOut(t *testing.T) {
	const typed = "s3cret-sentinel-060-typed"
	e := newConsoleEnvWith(t, nil)
	useCatalog(t, e, certsDemoTemplate())

	var pages, locations []string
	record := func(what string, resp *http.Response, body string) {
		pages = append(pages, what+"\n"+body)
		locations = append(locations, resp.Header.Get("Location"))
	}
	answers := map[string]string{"domain": "demo.example.com", "api_key": typed}
	resp, body := e.postForm(t, "/apps/new/template/demo", installForm("demo", answers, url.Values{"action": {"preview"}}))
	record("a preview", resp, body)
	resp, body = e.postForm(t, "/apps/new/template/demo", installForm("demo", answers, nil))
	record("the install", resp, body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("install = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.get(t, resp.Header.Get("Location"))
	record("the page after the install", resp, body)
	waitFor(t, "the key to be stored", func() bool { return e.Nomad.Variable("nomad/jobs/demo/web")["NODE_KEY"] != "" })
	live := e.Nomad.Variable("nomad/jobs/demo/web")
	key := live["NODE_KEY"]
	if !strings.HasPrefix(key, "-----BEGIN PRIVATE KEY-----\n") || !strings.HasPrefix(live["CA_CERT"], "-----BEGIN CERTIFICATE-----\n") || !strings.Contains(live["USERS"], `hash: "$2y$12$`) {
		t.Fatalf("the variable has %d items, the key starts %.30q", len(live), key)
	}
	needles := []string{strings.Split(key, "\n")[1], strings.Split(live["NODE_CERT"], "\n")[1], strings.Split(live["CA_CERT"], "\n")[1], typed,
		strings.Trim(strings.TrimSpace(strings.Split(live["USERS"], "\n")[1][len("  hash:"):]), `"`)}

	for _, path := range []string{"/apps/new/template/demo", "/apps/demo", "/apps/demo/configure", "/apps/demo/configure/source", "/apps/demo/configure/services",
		"/apps/demo/deployments", "/apps/demo/variables", "/apps"} {
		resp, body = e.get(t, path)
		record(path, resp, body)
	}
	resp, body = e.get(t, "/apps/demo/deployments/"+deploymentIDOf(t, e, "demo")+"/compose")
	record("the kept Compose file", resp, body)

	for _, n := range needles {
		for _, p := range pages {
			if strings.Contains(p, n) {
				t.Errorf("a page holds %.40q: %.300s", n, p)
			}
		}
		for _, l := range locations {
			if strings.Contains(l, n) || strings.Contains(l, url.QueryEscape(n)) {
				t.Errorf("an address holds %.40q: %s", n, l)
			}
		}
		if strings.Contains(e.Logs.String(), n) {
			t.Errorf("the log holds %.40q", n)
		}
	}
	files, _ := filepath.Glob(filepath.Join(e.Dir, "caravana.db*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, n := range needles {
			if strings.Contains(string(b), n) {
				t.Errorf("%s holds %.40q", filepath.Base(f), n)
			}
		}
	}
}
