package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

const (
	kumaDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	catVersion = "2027.01.01"
	tplLogo    = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#336699"/></svg>`
)

// demoTemplate is a template the fake registry can resolve (traefik/whoami),
// with a generated password and a secret input.
func demoTemplate() templates.Template {
	secret := func(s string) *string { return &s }
	return templates.Template{
		Name: "demo", Title: "Demo", Version: "1.0-1", Description: "A demo app.", Tags: []string{"demo", "web"},
		Upstream: "https://example.com", AppName: "demo", Notes: "Open https://{{domain}}.", Logo: tplLogo,
		Inputs: []templates.Input{
			{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "demo.example.com"},
			{Name: "api_key", Label: "API key", Type: templates.TypeSecret, Example: "example-key"},
		},
		Generated: []templates.Generated{{Name: "db_password", Length: 32, Alphabet: templates.AlphabetBase64URL}},
		Services: map[string]compose.Overlay{"web": {
			Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}},
			Health:  &compose.Health{Path: "/"},
			Secrets: []app.Secret{
				{Name: "DB_PASSWORD", Value: secret("{{db_password}}")},
				{Name: "API_KEY", Value: secret("{{api_key}}")},
			},
		}},
		Compose: "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n    environment:\n      PUBLIC_URL: \"https://{{domain}}\"\n",
	}
}

// useCatalog makes ts the catalog in use, as a fetched one stored in the row.
func useCatalog(t *testing.T, e *testEnv, ts ...templates.Template) {
	t.Helper()
	bundle, err := json.Marshal(templates.Catalog{Format: templates.Format, Version: catVersion, Templates: ts})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	row := store.TemplateCatalogRow{Bundle: bundle, BundleSum: templates.SumOf(bundle), Version: catVersion,
		FetchedAt: now, CheckedAt: now, CheckedOKAt: now}
	if err := e.Store.SaveTemplateCheck(t.Context(), row); err != nil {
		t.Fatal(err)
	}
}

func install(t *testing.T, e *testEnv, name, body string) (int, http.Header, []byte) {
	t.Helper()
	return call(t, http.MethodPost, e.URL+"/v1/templates/"+name+"/install", "application/json", body)
}

func TestTemplateReads(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)

	status, header, raw := call(t, http.MethodGet, e.URL+"/v1/templates", "", "")
	if status != http.StatusOK {
		t.Fatalf("listTemplates = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "TemplateList")
	list := decodeAs[struct {
		Catalog   map[string]any `json:"catalog"`
		Templates []struct {
			Name        string   `json:"name"`
			Tags        []string `json:"tags"`
			HasLogo     bool     `json:"hasLogo"`
			Available   bool     `json:"available"`
			Unavailable string   `json:"unavailable"`
		} `json:"templates"`
	}](t, raw)
	if len(list.Templates) != 6 || list.Catalog["source"] != "embedded" || list.Catalog["checking"] != true ||
		list.Catalog["checkedAt"] != nil || list.Catalog["fetchedAt"] != nil {
		t.Errorf("list = %s", raw)
	}
	_ = header

	for query, want := range map[string]string{
		"?q=status":             "uptime-kuma",
		"?tag=database":         "adminer,postgres",
		"?q=FEDIVERSE":          "hollo",
		"?q=status&tag=nothing": "",
		"?q=zzz":                "",
	} {
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/templates"+query, "", "")
		var names []string
		for _, tpl := range decodeAs[struct {
			Templates []struct {
				Name string `json:"name"`
			} `json:"templates"`
		}](t, raw).Templates {
			names = append(names, tpl.Name)
		}
		if strings.Join(names, ",") != want {
			t.Errorf("listTemplates%s = %v, want %q", query, names, want)
		}
		checkSchema(t, doc, raw, "TemplateList")
	}

	t.Run("one template", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/templates/hollo", "", "")
		if status != http.StatusOK {
			t.Fatalf("getTemplate = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Template")
		got := decodeAs[struct {
			Name      string                    `json:"name"`
			Compose   string                    `json:"compose"`
			Inputs    []map[string]any          `json:"inputs"`
			Generated []map[string]any          `json:"generated"`
			Services  map[string]map[string]any `json:"services"`
			Available bool                      `json:"available"`
		}](t, raw)
		if got.Name != "hollo" || !strings.Contains(got.Compose, "ghcr.io/fedify-dev/hollo") || !strings.Contains(got.Compose, "{{domain}}") ||
			len(got.Inputs) != 1 || len(got.Generated) != 2 || !got.Available {
			t.Errorf("template = %s", raw)
		}
		// The overlay is unrendered, and a generated secret has no value.
		if !strings.Contains(string(raw), `{{db_password}}`) || strings.Contains(string(raw), `"value":"`+strings.Repeat("0", 16)) {
			t.Errorf("the overlay: %s", raw)
		}
		if got.Generated[0]["alphabet"] == nil || got.Generated[0]["value"] != nil {
			t.Errorf("generated = %v", got.Generated)
		}
	})
	t.Run("an unknown template", func(t *testing.T) {
		for _, path := range []string{"/v1/templates/nope", "/v1/templates/nope/logo"} {
			status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
			wantError(t, "GET "+path, status, raw, http.StatusNotFound, codeTemplateNotFound, "name")
		}
	})
	t.Run("a template without a logo", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/templates/hollo/logo", "", "")
		wantError(t, "logo of hollo", status, raw, http.StatusNotFound, codeTemplateNotFound, "name")
	})
	t.Run("the logo is served so that it runs nothing", func(t *testing.T) {
		useCatalog(t, e, demoTemplate())
		status, header, raw := call(t, http.MethodGet, e.URL+"/v1/templates/demo/logo", "", "")
		if status != http.StatusOK || string(raw) != tplLogo {
			t.Fatalf("logo = %d %q", status, raw)
		}
		for k, v := range map[string]string{
			"Content-Type":            "image/svg+xml",
			"X-Content-Type-Options":  "nosniff",
			"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; sandbox",
		} {
			if header.Get(k) != v {
				t.Errorf("%s = %q, want %q", k, header.Get(k), v)
			}
		}
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/templates", "", "")
		if !strings.Contains(string(raw), `"hasLogo":true`) || strings.Contains(string(raw), "<svg") {
			t.Errorf("the list: %s", raw)
		}
	})
}

func TestTemplateInstall(t *testing.T) {
	e := newTestEnv(t, "")
	e.Registry.AddIndex("louislam/uptime-kuma", "2.5.11", kumaDigest)
	doc := loadContract(t)

	t.Run("a template with a backup needs the destination first, as any deploy does", func(t *testing.T) {
		status, _, raw := install(t, e, "uptime-kuma", `{"id": "status", "inputs": {"domain": "status.example.com"}}`)
		wantError(t, "install without a destination", status, raw, http.StatusBadRequest, codeInvalidField, "services.uptime-kuma.backup")
		if status, _, _ := call(t, http.MethodGet, e.URL+"/v1/apps/status", "", ""); status != http.StatusNotFound {
			t.Errorf("an app was made: %d", status)
		}
		if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
			t.Fatalf("PUT destination = %d %s", status, raw)
		}
	})

	t.Run("202 with the deployment, and the app names its template", func(t *testing.T) {
		status, header, raw := install(t, e, "uptime-kuma", `{"id": "status", "inputs": {"domain": "Status.Example.com"}}`)
		if status != http.StatusAccepted {
			t.Fatalf("installTemplate = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Deployment")
		d := decodeAs[deployment](t, raw)
		if header.Get("Location") != "/v1/apps/status/deployments/"+d.ID || header.Get("Cache-Control") != "no-store" {
			t.Errorf("headers = %v", header)
		}
		waitHealthy(t, e, "status", d.ID)

		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/status", "", "")
		checkSchema(t, doc, raw, "AppRecord")
		rec := decodeAs[struct {
			App struct {
				Services []struct {
					Domains []struct{ Host string }  `json:"domains"`
					Backup  *struct{ Engine string } `json:"backup"`
				} `json:"services"`
			} `json:"app"`
			Template    *struct{ Name, Version string } `json:"template"`
			ComposeKept bool                            `json:"composeKept"`
		}](t, raw)
		if rec.Template == nil || rec.Template.Name != "uptime-kuma" || rec.Template.Version != "2.5.11-1" || !rec.ComposeKept ||
			rec.App.Services[0].Domains[0].Host != "status.example.com" || rec.App.Services[0].Backup.Engine != "volume" {
			t.Errorf("app = %s", raw)
		}
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps", "", "")
		checkSchema(t, doc, raw, "AppSummaryList")
		if !strings.Contains(string(raw), `"template":{"name":"uptime-kuma","version":"2.5.11-1"}`) {
			t.Errorf("listApps has no template: %s", raw)
		}
		// An app that wasn't installed from a template has none.
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", whoamiBody); status != http.StatusAccepted {
			t.Fatalf("deploy whoami = %d %s", status, raw)
		}
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
		if strings.Contains(string(raw), `"template"`) {
			t.Errorf("a plain app has a template: %s", raw)
		}
	})

	errorsWanted := []struct {
		name   string
		path   string
		body   string
		status int
		code   string
		field  string
	}{
		{"the ID is taken", "uptime-kuma", `{"id": "status", "inputs": {"domain": "other.example.com"}}`, 409, codeAppExists, "id"},
		{"the domain is another app's", "uptime-kuma", `{"id": "status2", "inputs": {"domain": "status.example.com"}}`, 409, codeDomainTaken, "inputs.domain"},
		{"a bad domain", "uptime-kuma", `{"id": "k2", "inputs": {"domain": "not a domain"}}`, 400, codeInvalidField, "inputs.domain"},
		{"no domain", "uptime-kuma", `{"id": "k2"}`, 400, codeInvalidField, "inputs.domain"},
		{"an input the template doesn't ask for", "uptime-kuma", `{"id": "k2", "inputs": {"domain": "k.example.com", "x": "y"}}`, 400, codeInvalidField, "inputs.x"},
		{"no ID", "uptime-kuma", `{"inputs": {"domain": "k.example.com"}}`, 400, codeInvalidField, "id"},
		{"a bad ID", "uptime-kuma", `{"id": "Not A Name", "inputs": {"domain": "k.example.com"}}`, 400, codeInvalidField, "id"},
		{"an unknown body field", "uptime-kuma", `{"id": "k2", "surprise": 1}`, 400, codeUnknownField, "surprise"},
		{"a project that doesn't exist", "uptime-kuma", `{"id": "nope--production--k", "inputs": {"domain": "k.example.com"}}`, 404, codeProjectNotFound, "id"},
		{"an unknown template", "nope", `{"id": "k2", "inputs": {}}`, 404, codeTemplateNotFound, "name"},
	}
	for _, tt := range errorsWanted {
		t.Run(tt.name, func(t *testing.T) {
			status, _, raw := install(t, e, tt.path, tt.body)
			wantError(t, tt.name, status, raw, tt.status, tt.code, tt.field)
		})
	}

	t.Run("a template that isn't offered", func(t *testing.T) {
		broken := demoTemplate()
		broken.Name, broken.Broken = "broken", "its image moved"
		useCatalog(t, e, demoTemplate(), broken)
		status, _, raw := install(t, e, "broken", `{"id": "b", "inputs": {"domain": "b.example.com", "api_key": "k"}}`)
		got := wantError(t, "install of a broken template", status, raw, http.StatusConflict, codeTemplateUnavailable, "name")
		if !strings.Contains(got.Error.Message, "its image moved") {
			t.Errorf("message = %q", got.Error.Message)
		}
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/templates", "", "")
		if !strings.Contains(string(raw), `"available":false`) || !strings.Contains(string(raw), `"unavailable":"its image moved"`) {
			t.Errorf("the list: %s", raw)
		}
	})
}

// T011a: install without backups, on an installation with no backup destination.
func TestTemplateWithoutBackups(t *testing.T) {
	e := newTestEnv(t, "")
	e.Registry.AddIndex("louislam/uptime-kuma", "2.5.11", kumaDigest)
	doc := loadContract(t)
	const warning = "Backups are off for uptime-kuma: set a backup destination in Settings › Backup storage, then turn its backup on in the Backups tab."

	status, _, raw := install(t, e, "uptime-kuma", `{"id": "status", "inputs": {"domain": "status.example.com"}, "withoutBackups": false}`)
	wantError(t, "install with backups and no destination", status, raw, http.StatusBadRequest, codeInvalidField, "services.uptime-kuma.backup")

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/templates/uptime-kuma/preview", "application/json",
		`{"id": "status", "inputs": {"domain": "status.example.com"}, "withoutBackups": true}`)
	if status != http.StatusOK {
		t.Fatalf("preview = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "TemplatePreview")
	prev := decodeAs[struct {
		App      struct{ Services []struct{ Backup *struct{} } } `json:"app"`
		Warnings []app.Warning                                    `json:"warnings"`
	}](t, raw)
	if prev.App.Services[0].Backup != nil || len(prev.Warnings) != 1 || prev.Warnings[0].Field != "services.uptime-kuma.backup" || prev.Warnings[0].Message != warning {
		t.Errorf("preview = %s", raw)
	}

	status, header, raw := install(t, e, "uptime-kuma", `{"id": "status", "inputs": {"domain": "status.example.com"}, "withoutBackups": true}`)
	if status != http.StatusAccepted {
		t.Fatalf("install = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Deployment")
	d := decodeAs[deployment](t, raw)
	if len(d.Warnings) != 1 || d.Warnings[0].Field != "services.uptime-kuma.backup" || d.Warnings[0].Message != warning || header.Get("Cache-Control") != "no-store" {
		t.Errorf("deployment = %s", raw)
	}
	waitHealthy(t, e, "status", d.ID)
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/status", "", "")
	if strings.Contains(string(raw), `"backup"`) || !strings.Contains(string(raw), `"template":{"name":"uptime-kuma"`) {
		t.Errorf("app = %s", raw)
	}
	status, _, raw = install(t, e, "uptime-kuma", `{"id": "x", "inputs": {"domain": "x.example.com"}, "withoutBackups": "yes"}`)
	wantError(t, "a non-boolean option", status, raw, http.StatusBadRequest, codeInvalidField, "withoutBackups")
}

func TestTemplatePreview(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	useCatalog(t, e, demoTemplate())

	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/templates/demo/preview", "application/json",
		`{"id": "demo", "inputs": {"domain": "demo.example.com"}}`) // no secret input: a preview doesn't need it
	if status != http.StatusOK {
		t.Fatalf("previewTemplate = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "TemplatePreview")
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", header.Get("Cache-Control"))
	}
	got := decodeAs[struct {
		App      struct{ Services []struct{ Name string } } `json:"app"`
		Warnings []any                                      `json:"warnings"`
		Compose  string                                     `json:"compose"`
	}](t, raw)
	if len(got.App.Services) != 1 || got.Warnings == nil || !strings.Contains(got.Compose, `PUBLIC_URL: "https://demo.example.com"`) {
		t.Errorf("preview = %s", raw)
	}
	if status, _, _ := call(t, http.MethodGet, e.URL+"/v1/apps/demo", "", ""); status != http.StatusNotFound {
		t.Error("a preview made an app")
	}
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/templates/demo/preview", "application/json", `{"id": "demo", "inputs": {"domain": "bad"}}`)
	wantError(t, "preview with a bad domain", status, raw, http.StatusBadRequest, codeInvalidField, "inputs.domain")
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/templates/nope/preview", "application/json", `{"id": "demo"}`)
	wantError(t, "preview of an unknown template", status, raw, http.StatusNotFound, codeTemplateNotFound, "name")
}

// catalogRelease stands in for the catalog repository's release.
func catalogRelease(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	requests := 0
	bundle, err := json.Marshal(templates.Catalog{Format: templates.Format, Version: catVersion, Templates: []templates.Template{{
		Name: "whoami", Title: "Whoami", Version: "1-1", Description: "Shows who answered.", Upstream: "https://example.com", AppName: "whoami",
		Inputs:   []templates.Input{{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "who.example.com"}},
		Services: map[string]compose.Overlay{"web": {Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}}}},
		Compose:  "services:\n  web:\n    image: traefik/whoami:1.10\n    expose: [\"80\"]\n",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		switch r.URL.Path {
		case "/SHA256SUMS":
			_, _ = w.Write(templates.Sums(bundle))
		case "/catalog.json":
			_, _ = w.Write(bundle)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func TestTemplateCatalogEndpoints(t *testing.T) {
	var srv *httptest.Server
	var requests *int
	srv, requests = catalogRelease(t)
	e := newTestEnvWith(t, "", func(s *service.Service) { s.SetTemplatesURL(srv.URL) })
	doc := loadContract(t)

	t.Run("Check now starts a check, a second one is too soon", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/templates/check", "", "")
		if status != http.StatusAccepted {
			t.Fatalf("checkTemplates = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "TemplateCheckStarted")
		status, header, raw := call(t, http.MethodPost, e.URL+"/v1/templates/check", "", "")
		wantError(t, "a second check", status, raw, http.StatusTooManyRequests, codeCheckTooSoon, "")
		if header.Get("Retry-After") == "" {
			t.Error("no Retry-After")
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, _, raw := call(t, http.MethodGet, e.URL+"/v1/templates", "", "")
			if strings.Contains(string(raw), `"source":"fetched"`) {
				checkSchema(t, doc, raw, "TemplateList")
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the fetched catalog never arrived: %s", raw)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("checking off: 409, and no request is made", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/templates/settings", "application/json", `{"checking": false}`)
		if status != http.StatusOK {
			t.Fatalf("putTemplateSettings = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "TemplateCatalog")
		if decodeAs[map[string]any](t, raw)["checking"] != false {
			t.Errorf("catalog = %s", raw)
		}
		before := *requests
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/templates/check", "", "")
		wantError(t, "check with checking off", status, raw, http.StatusConflict, codeCheckDisabled, "")
		if *requests != before {
			t.Errorf("%d requests were made with checking off", *requests-before)
		}
		if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/templates/settings", "application/json", `{"checking": true}`); status != http.StatusOK ||
			decodeAs[map[string]any](t, raw)["checking"] != true {
			t.Errorf("turning it on = %d %s", status, raw)
		}
	})
	t.Run("the setting is required", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/templates/settings", "application/json", `{}`)
		wantError(t, "settings without checking", status, raw, http.StatusBadRequest, codeInvalidField, "checking")
	})
}

func TestTemplatesAreDeniedToDeployTokens(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/v1/templates", ""},
		{"GET", "/v1/templates/hollo", ""},
		{"GET", "/v1/templates/hollo/logo", ""},
		{"POST", "/v1/templates/hollo/preview", `{"id": "shop--staging--web"}`},
		{"POST", "/v1/templates/hollo/install", `{"id": "shop--staging--web", "inputs": {"domain": "a.example.com"}}`},
		{"POST", "/v1/templates/check", ""},
		{"PUT", "/v1/templates/settings", `{"checking": false}`},
	} {
		status, _, raw := callWith(t, auth, rt.method, e.RawURL+rt.path, "application/json", rt.body)
		if status != http.StatusForbidden || !strings.Contains(string(raw), `"token_scope"`) {
			t.Errorf("%s %s with a deploy token = %d %s, want 403 token_scope", rt.method, rt.path, status, raw)
		}
	}
	if status, _, _ := call(t, http.MethodGet, e.RawURL+"/v1/templates", "", ""); status != http.StatusUnauthorized {
		t.Errorf("without credentials = %d, want 401", status)
	}
}

// sentinelReader yields the bytes that make the base64url alphabet's
// characters spell the sentinel.
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

// TestTemplateAnswersKeepSecretsOut: a generated secret and a secret input
// appear in no answer of the API, no log line and no byte of the SQLite file
// (FR-003); the Nomad job has names only.
func TestTemplateAnswersKeepSecretsOut(t *testing.T) {
	const generated = "s3cret-sentinel-035-generated-xx" // 32 characters of the base64url alphabet
	const typed = "s3cret-sentinel-035-typed"
	e := newTestEnvWith(t, "", func(s *service.Service) { s.SetTemplateRandom(newSentinelReader(generated)) })
	useCatalog(t, e, demoTemplate())

	var answers []string
	record := func(status int, raw []byte) { answers = append(answers, fmt.Sprintf("%d %s", status, raw)) }
	record(callStatus(call(t, http.MethodPost, e.URL+"/v1/templates/demo/preview", "application/json",
		`{"id": "demo", "inputs": {"domain": "demo.example.com", "api_key": "`+typed+`"}}`)))
	status, _, raw := install(t, e, "demo", `{"id": "demo", "inputs": {"domain": "demo.example.com", "api_key": "`+typed+`"}}`)
	if status != http.StatusAccepted {
		t.Fatalf("install = %d %s", status, raw)
	}
	record(status, raw)
	waitHealthy(t, e, "demo", decodeAs[deployment](t, raw).ID)
	record(callStatus(install(t, e, "demo", `{"id": "demo2", "inputs": {"domain": "not a domain", "api_key": "`+typed+`"}}`)))
	record(callStatus(install(t, e, "demo", `{"id": "demo", "inputs": {"domain": "x.example.com", "api_key": "`+typed+`"}}`)))
	for _, path := range []string{"/v1/templates", "/v1/templates/demo", "/v1/apps", "/v1/apps/demo", "/v1/apps/demo/compose",
		"/v1/apps/demo/deployments", "/v1/apps/demo/jobspec", "/v1/deployments", "/v1/attention", "/v1/domains"} {
		record(callStatus(call(t, http.MethodGet, e.URL+path, "", "")))
	}
	for _, a := range answers {
		for _, secret := range []string{generated, typed} {
			if strings.Contains(a, secret) {
				t.Errorf("an answer holds %q: %.200s", secret, a)
			}
		}
	}
	job, _ := json.Marshal(e.Nomad.Job("demo"))
	for _, secret := range []string{generated, typed} {
		if bytes.Contains(job, []byte(secret)) {
			t.Errorf("the Nomad job holds %q", secret)
		}
		if strings.Contains(e.Logs.String(), secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
	files, _ := filepath.Glob(filepath.Join(e.Dir, "caravana.db*"))
	if len(files) == 0 {
		t.Fatal("no database file")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, secret := range []string{generated, typed} {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("%s holds %q", filepath.Base(f), secret)
			}
		}
	}
}

// callStatus is call's answer as the status and the body, for record.
func callStatus(status int, _ http.Header, raw []byte) (int, []byte) { return status, raw }
