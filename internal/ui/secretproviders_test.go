package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Sentinels no page, address, form or log line may hold (feature 037).
const (
	providerValueSentinel = "s3cret-sentinel-037-ui"
	providerAPIKey        = "bax-0123456789abcdef-fedcba9876543210"
	providerPrivateKey    = "-----BEGIN PRIVATE KEY-----s3cret-sentinel-037-ui-private-----END PRIVATE KEY-----"
)

// sourcesEnv is a console whose providers are in-memory sources: one
// secretstest.Source per provider name, made on first use.
type sourcesEnv struct {
	*consoleEnv
	mu      sync.Mutex
	sources map[string]*secretstest.Source
}

// source is the fake of the provider called name.
func (e *sourcesEnv) source(name string) *secretstest.Source {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sources[name] == nil {
		e.sources[name] = secretstest.NewSource(nil)
	}
	return e.sources[name]
}

func (e *sourcesEnv) factory(p store.SecretProvider, creds map[string]string) (secrets.Source, error) {
	if p.URL == "" || !strings.HasPrefix(p.URL, "https://") {
		return nil, app.NewFieldError("bella.url", "must be an https:// address (http:// only for localhost)")
	}
	if !strings.HasPrefix(creds[deploy.SecretProviderAPIKey], "bax-") {
		return nil, app.NewFieldError("apiKey", "must be a Bella API key, bax- followed by a key id and a secret in hex")
	}
	return e.source(p.Name), nil
}

// newSourcesEnv serves the console with the project "shop" and sources for providers.
func newSourcesEnv(t *testing.T) *sourcesEnv {
	t.Helper()
	e := &sourcesEnv{sources: map[string]*secretstest.Source{}}
	e.consoleEnv = newConsoleEnvWith(t, func(svc *service.Service, _ *deploytest.Nomad) { svc.SetSecretSources(e.factory) })
	if _, err := e.Svc.CreateProject(context.Background(), "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	return e
}

// bellaForm is the values of a valid provider form.
func bellaForm(name string) url.Values {
	return url.Values{"name": {name}, "url": {"https://api.bella-baxter.io"}, "project": {"shop"}, "environment": {"production"},
		"apiKey": {providerAPIKey}, "privateKey": {providerPrivateKey}, "allowed": {""}}
}

// providerAddForm is the page's add form: the one that posts without edit=1.
func providerAddForm(t *testing.T, page string) plainForm {
	t.Helper()
	for _, f := range formsIn(page) {
		if f.Action == "/settings/secret-providers" && f.Values.Get("edit") != "1" {
			return f
		}
	}
	t.Fatalf("no add form in:\n%s", page)
	return plainForm{}
}

// addProvider saves a provider through the page's own add form.
func (e *sourcesEnv) addProvider(t *testing.T, name string, set url.Values) {
	t.Helper()
	_, page := e.get(t, "/settings/secret-providers")
	resp, body := e.submit(t, providerAddForm(t, page), set)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add %s = %d, want 303:\n%s", name, resp.StatusCode, text(body))
	}
	e.source(name).Set("DATABASE_URL", providerValueSentinel)
}

func (e *sourcesEnv) providerForm(t *testing.T, page, action, name string) plainForm {
	t.Helper()
	for _, f := range formsIn(page) {
		if f.Action == action && f.Values.Get("name") == name {
			return f
		}
	}
	t.Fatalf("no form posts to %s for %q:\n%s", action, name, page)
	return plainForm{}
}

// noProviderSecrets fails the test when an output holds a credential or a value.
func noProviderSecrets(t *testing.T, what string, outputs ...string) {
	t.Helper()
	for i, out := range outputs {
		for _, s := range []string{providerValueSentinel, providerAPIKey, "sentinel-037-ui-private"} {
			if strings.Contains(out, s) {
				t.Errorf("%s: output %d holds %q", what, i, s)
			}
		}
	}
}

func TestSettingsSecretProviders(t *testing.T) {
	e := newSourcesEnv(t)

	_, page := e.get(t, "/settings/secret-providers")
	got := text(page)
	for _, want := range []string{
		"Secret providers",
		"Caravana reads these values at each deploy and copies them into Nomad Variables, so it holds this API key and, with zero-knowledge encryption, its private key. Make a key for Caravana alone, in the Bella environment the apps use.",
		"No secret providers. Connect Bella Baxter to use its secrets in apps without typing them here.",
		"Leave empty", // not on the add form: checked below
	} {
		if want == "Leave empty" {
			if strings.Contains(got, want) {
				t.Errorf("the add form says %q", want)
			}
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	for _, want := range []string{`placeholder="https://api.bella-baxter.io"`, `type="password"`, `<textarea`, `name="allowed"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the add form lacks %s", want)
		}
	}
	if !strings.Contains(page, `href="/settings/secret-providers"`) {
		t.Error("the Settings nav has no Secret providers entry")
	}

	t.Run("add", func(t *testing.T) {
		e.addProvider(t, "bella-prod", bellaForm("bella-prod"))
		resp, page := e.get(t, "/settings/secret-providers?providers=saved")
		got := text(page)
		for _, want := range []string{"Saved. Test it", "bella-prod", "Bella Baxter · shop/production · api.bella-baxter.io", "All projects", "not used", "not tested yet"} {
			if !strings.Contains(got, want) {
				t.Errorf("after the save the page lacks %q:\n%s", want, got)
			}
		}
		noProviderSecrets(t, "after add", page, e.Logs.String(), resp.Header.Get("Location"))
		items := e.Nomad.Variable(deploy.SecretProviderVariablePath("bella-prod"))
		if items[deploy.SecretProviderAPIKey] != providerAPIKey {
			t.Error("the API key wasn't kept in the Nomad Variable")
		}
	})

	t.Run("test", func(t *testing.T) {
		_, page := e.get(t, "/settings/secret-providers")
		f := e.providerForm(t, page, "/settings/secret-providers/test", "bella-prod")
		resp, body := e.submit(t, f, nil)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/secret-providers?tested=bella-prod" {
			t.Fatalf("POST test = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
		}
		_, page = e.get(t, "/settings/secret-providers?tested=bella-prod")
		if got := text(page); !strings.Contains(got, "bella-prod: 1 secret readable.") || !strings.Contains(got, "1 secret readable") {
			t.Errorf("the notice lacks the count:\n%s", got)
		}

		e.source("bella-prod").SetRefused(true)
		e.submit(t, f, nil)
		_, page = e.get(t, "/settings/secret-providers?tested=bella-prod")
		got := text(page)
		if !strings.Contains(got, "bella-prod can't be read") || !strings.Contains(got, "Deploys of apps that read from this provider fail until Caravana can read it again.") {
			t.Errorf("a refused test isn't said:\n%s", got)
		}
		if !strings.Contains(page, "cv-mark-failed") {
			t.Error("a failed provider has no failed mark")
		}
		e.source("bella-prod").SetRefused(false)
	})

	t.Run("a refused save keeps what was typed, never a key", func(t *testing.T) {
		_, page := e.get(t, "/settings/secret-providers")
		f := providerAddForm(t, page)
		bad := bellaForm("bella-two")
		bad.Set("url", "http://not-https.example.com")
		resp, body := e.submit(t, f, bad)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422", resp.StatusCode)
		}
		got := text(body)
		if !strings.Contains(got, "The Bella URL must be an https:// address") {
			t.Errorf("the error is not at the URL field:\n%s", got)
		}
		if !strings.Contains(body, `value="http://not-https.example.com"`) || !strings.Contains(body, `aria-describedby="provider-add-url-error"`) {
			t.Error("the typed URL or its error link is missing")
		}
		noProviderSecrets(t, "refused save", body, e.Logs.String())
		if _, err := e.Svc.SecretProvider(context.Background(), "bella-two"); err == nil {
			t.Error("a refused save stored the provider")
		}

		// A bad API key: the field's error, and the key isn't drawn back.
		bad = bellaForm("bella-two")
		bad.Set("apiKey", "nope-"+providerValueSentinel)
		_, body = e.submit(t, f, bad)
		if !strings.Contains(text(body), "The API key must be a Bella API key") || strings.Contains(body, providerValueSentinel) {
			t.Errorf("a bad key: error missing or key echoed:\n%s", text(body))
		}
		// An unknown allowed place is named at its input.
		bad = bellaForm("bella-two")
		bad.Set("allowed", "shop, nowhere")
		_, body = e.submit(t, f, bad)
		if !strings.Contains(text(body), `there is no project "nowhere"`) || !strings.Contains(body, `value="shop, nowhere"`) {
			t.Errorf("a bad place isn't named at Allowed in:\n%s", text(body))
		}
	})

	t.Run("edit keeps the stored keys", func(t *testing.T) {
		_, page := e.get(t, "/settings/secret-providers")
		f := e.providerForm(t, page, "/settings/secret-providers", "bella-prod")
		if f.Values.Get("edit") != "1" || f.Values.Get("project") != "shop" || f.Values.Get("apiKey") != "" || f.Values.Get("privateKey") != "" {
			t.Errorf("the edit form's values = %v", f.Values)
		}
		if !strings.Contains(text(page), "Leave empty to keep the one stored.") {
			t.Error("the edit form lacks the keep hint")
		}
		resp, body := e.submit(t, f, url.Values{"environment": {"staging"}, "allowed": {"shop, shop/production"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("edit = %d:\n%s", resp.StatusCode, text(body))
		}
		info, err := e.Svc.SecretProvider(context.Background(), "bella-prod")
		if err != nil || info.Environment != "staging" || len(info.Allowed) != 2 {
			t.Errorf("provider = %+v, %v", info, err)
		}
		if e.Nomad.Variable(deploy.SecretProviderVariablePath("bella-prod"))[deploy.SecretProviderAPIKey] != providerAPIKey {
			t.Error("an empty key field replaced the stored key")
		}
		_, page = e.get(t, "/settings/secret-providers")
		if got := text(page); !strings.Contains(got, "Allowed in shop, shop/production") || !strings.Contains(got, "shop/staging") {
			t.Errorf("the row lacks the places or the environment:\n%s", got)
		}
	})

	t.Run("a refused edit opens its row with the error and no key", func(t *testing.T) {
		_, page := e.get(t, "/settings/secret-providers")
		f := e.providerForm(t, page, "/settings/secret-providers", "bella-prod")
		resp, body := e.submit(t, f, url.Values{"url": {"http://plain.example.com"}, "apiKey": {providerValueSentinel}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422", resp.StatusCode)
		}
		if !strings.Contains(body, `id="provider-edit-0" name="conn-0" open`) || !strings.Contains(body, `id="provider-edit-0-url-error"`) || !strings.Contains(body, `value="http://plain.example.com"`) {
			t.Errorf("the row's edit form isn't open with the typed URL and its error:\n%s", text(body))
		}
		noProviderSecrets(t, "refused edit", body, e.Logs.String())
		info, _ := e.Svc.SecretProvider(context.Background(), "bella-prod")
		if info.URL != "https://api.bella-baxter.io" {
			t.Errorf("a refused edit changed the URL to %q", info.URL)
		}
	})

	t.Run("remove, refused while an app uses it", func(t *testing.T) {
		a := duoApp()
		a.Services[0].Secrets = []app.Secret{{Name: "DATABASE_URL", From: &app.SecretRef{Provider: "bella-prod", Path: "DATABASE_URL"}}}
		e.seed(t, a, seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
		_, page := e.get(t, "/settings/secret-providers")
		if got := text(page); !strings.Contains(got, "used by 1 app") {
			t.Errorf("the row doesn't say it is used:\n%s", got)
		}
		f := e.providerForm(t, page, "/settings/secret-providers/remove", "bella-prod")
		resp, body := e.submit(t, f, nil)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "duo") || !strings.Contains(text(body), "remove those references first") {
			t.Errorf("remove in use = %d, want 409 naming the app:\n%s", resp.StatusCode, text(body))
		}
		if err := e.Store.DeleteApp(context.Background(), "duo"); err != nil {
			t.Fatal(err)
		}
		resp, _ = e.submit(t, f, nil)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/secret-providers?providers=removed" {
			t.Errorf("remove = %d to %q", resp.StatusCode, location(t, resp))
		}
		if e.Nomad.Variable(deploy.SecretProviderVariablePath("bella-prod")) != nil {
			t.Error("the credentials stayed in Nomad")
		}
	})
	noProviderSecrets(t, "the log", e.Logs.String())
}
