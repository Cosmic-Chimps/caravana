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

// refsEnv is a console with the providers "bella-prod" (open to every place)
// and "bella-other" (limited to the project "elsewhere", so duo can't use it),
// both holding DATABASE_URL, and duo deployed with a typed secret and a
// reference.
func refsEnv(t *testing.T) *sourcesEnv {
	t.Helper()
	e := newSourcesEnv(t)
	if _, err := e.Svc.CreateProject(context.Background(), "elsewhere", "Elsewhere"); err != nil {
		t.Fatal(err)
	}
	e.addProvider(t, "bella-prod", bellaForm("bella-prod"))
	other := bellaForm("bella-other")
	other.Set("allowed", "elsewhere")
	e.addProvider(t, "bella-other", other)
	e.source("bella-prod").Set("API_KEY", "k1")
	return e
}

// duoWithRef deploys duo with TYPED (a value) and DATABASE_URL (from bella-prod).
func (e *sourcesEnv) duoWithRef(t *testing.T) {
	t.Helper()
	a := duoApp()
	typed := "typed-value"
	a.Services[0].Secrets = []app.Secret{
		{Name: "TYPED", Value: &typed},
		{Name: "DATABASE_URL", From: &app.SecretRef{Provider: "bella-prod", Path: "DATABASE_URL"}},
	}
	e.deploy(t, a)
}

func TestVariablesShowReferences(t *testing.T) {
	e := refsEnv(t)
	e.duoWithRef(t)

	resp, page := e.get(t, "/apps/duo/variables")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET variables = %d", resp.StatusCode)
	}
	got := text(page)
	for _, want := range []string{
		"from bella-prod · DATABASE_URL",
		"Where secrets come from",
		"Caravana Built in · stored in Nomad Variables 1 value",
		"bella-prod Bella Baxter · shop/production",
		"1 reference",
		"Connect a source",
		"To use a value from a source, pick it when you add a secret. Caravana shows the path, never the value.",
		"Refresh secrets",
		"Reads every reference again and rolls the app out if a value changed.",
		"A value", "From a provider",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	// Only providers this app's place may use are offered; the typed secret keeps its dots.
	if !strings.Contains(page, `<option value="bella-prod"`) || strings.Contains(page, `value="bella-other"`) {
		t.Error("the Provider select should offer bella-prod only")
	}
	if strings.Count(page, `class="cv-dots"`) != 1 {
		t.Errorf("want one dotted (typed) secret, got %d", strings.Count(page, `class="cv-dots"`))
	}
	form := formWith(t, page, "/apps/duo/secrets/refresh")
	if form.Method != "post" {
		t.Errorf("refresh form = %+v", form)
	}
	noProviderSecrets(t, "the Variables page", page, e.Logs.String())
}

func TestVariablesWithoutProviders(t *testing.T) {
	e := newConsoleEnv(t)
	e.deploy(t, duoApp())
	_, page := e.get(t, "/apps/duo/variables")
	if strings.Contains(page, `name="source"`) || strings.Contains(page, "Refresh secrets") {
		t.Error("with no provider the choice and Refresh secrets are absent")
	}
	if got := text(page); !strings.Contains(got, "connect one in Settings") || !strings.Contains(page, `href="/settings/secret-providers"`) {
		t.Errorf("the page doesn't link to Settings › Secret providers:\n%s", got)
	}
}

func TestAddSecretFromProvider(t *testing.T) {
	e := refsEnv(t)
	e.deploy(t, duoApp())
	_, page := e.get(t, "/apps/duo/variables")
	f := formWith(t, page, "/apps/duo/variables")

	// The path is typed with a value in the box next to it: the value is never read.
	resp, body := e.submit(t, f, url.Values{"service": {"web"}, "name": {"DATABASE_URL"}, "source": {"provider"},
		"provider": {"bella-prod"}, "path": {"DATABASE_URL"}, "value": {"must-be-ignored"}, "secret": {"1"}})
	recs := e.deployments(t, "duo")
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duo/deployments#deployment-"+recs[0].Deployment.ID {
		t.Fatalf("POST = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	if recs[0].Deployment.Trigger != app.TriggerConsole {
		t.Errorf("trigger = %s, want console", recs[0].Deployment.Trigger)
	}
	e.waitDeploy(t, recs[0].Deployment.ID)
	secs := e.storedApp(t, "duo").Services[0].Secrets
	if len(secs) != 1 || secs[0].From == nil || secs[0].From.Path != "DATABASE_URL" || secs[0].Value != nil {
		t.Errorf("stored secrets = %+v, want the reference only", secs)
	}
	if got := e.Nomad.Variable("nomad/jobs/duo/web"); got["DATABASE_URL"] != providerValueSentinel {
		t.Errorf("the value wasn't copied into the service's variable: %v", got)
	}

	_, page = e.get(t, "/apps/duo/variables")
	_, history := e.get(t, "/apps/duo/deployments")
	noProviderSecrets(t, "pages after a reference", body, page, history, location(t, resp), e.Logs.String())
	if strings.Contains(page, "must-be-ignored") {
		t.Error("the typed value was kept")
	}
}

func TestAddSecretFromProviderRefused(t *testing.T) {
	e := refsEnv(t)
	e.deploy(t, duoApp())

	tests := []struct {
		name       string
		set        url.Values
		down       bool
		wantStatus int
		wantText   string
		wantBody   string // markup that proves the message sits at its input
	}{
		{"no path", url.Values{"provider": {"bella-prod"}, "path": {""}}, false, 422, "Enter the secret's path in the provider.", `aria-describedby="var-path-hint var-path-error"`},
		{"missing path in the provider", url.Values{"provider": {"bella-prod"}, "path": {"NOPE"}}, false, 422, "Provider bella-prod has no secret NOPE.", `value="NOPE"`},
		{"a provider this place may not use", url.Values{"provider": {"bella-other"}, "path": {"API_KEY"}}, false, 422, "bella-other", `id="var-provider-error"`},
		{"provider down", url.Values{"provider": {"bella-prod"}, "path": {"API_KEY"}}, true, 502, "bella-prod", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e.source("bella-prod").SetDown(tc.down)
			defer e.source("bella-prod").SetDown(false)
			// The page is read again each time: a refused deploy may have saved the definition.
			_, page := e.get(t, "/apps/duo/variables")
			f := formWith(t, page, "/apps/duo/variables")
			set := url.Values{"service": {"web"}, "name": {"API_KEY"}, "source": {"provider"}, "secret": {"1"}}
			for k, v := range tc.set {
				set[k] = v
			}
			resp, body := e.submit(t, f, set)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("POST = %d, want %d:\n%s", resp.StatusCode, tc.wantStatus, text(body))
			}
			if !strings.Contains(text(body), tc.wantText) {
				t.Errorf("the page lacks %q:\n%s", tc.wantText, text(body))
			}
			if tc.wantBody != "" && !strings.Contains(body, tc.wantBody) {
				t.Errorf("the page lacks %s", tc.wantBody)
			}
			noProviderSecrets(t, tc.name, body, e.Logs.String())
			// Nothing reached the service's Nomad Variable, whatever the refusal.
			if got := e.Nomad.Variable("nomad/jobs/duo/web"); got["API_KEY"] != "" {
				t.Errorf("a refused reference put a value in Nomad: %v", got)
			}
		})
	}
}

func TestRefreshSecretsFromTheConsole(t *testing.T) {
	e := refsEnv(t)
	e.duoWithRef(t)
	_, page := e.get(t, "/apps/duo/variables")
	f := formWith(t, page, "/apps/duo/secrets/refresh")
	before := len(e.deployments(t, "duo"))

	// Nothing changed: back to the page with the notice, and nothing deployed.
	resp, body := e.submit(t, f, nil)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duo/variables?refresh=unchanged" {
		t.Fatalf("unchanged refresh = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	_, page = e.get(t, "/apps/duo/variables?refresh=unchanged")
	if !strings.Contains(text(page), "Secrets unchanged: nothing to roll out.") {
		t.Errorf("the page lacks the notice:\n%s", text(page))
	}
	if n := len(e.deployments(t, "duo")); n != before {
		t.Errorf("an unchanged refresh deployed: %d deployments, want %d", n, before)
	}

	// A value changed in the provider: a new deployment, and its history entry says so.
	e.source("bella-prod").Set("DATABASE_URL", providerValueSentinel+"-changed")
	resp, body = e.submit(t, f, nil)
	recs := e.deployments(t, "duo")
	if resp.StatusCode != http.StatusSeeOther || len(recs) != before+1 || location(t, resp) != "/apps/duo/deployments#deployment-"+recs[0].Deployment.ID {
		t.Fatalf("changed refresh = %d to %q (%d deployments):\n%s", resp.StatusCode, location(t, resp), len(recs), text(body))
	}
	e.waitDeploy(t, recs[0].Deployment.ID)
	if !recs[0].Deployment.Refresh || recs[0].Deployment.Trigger != app.TriggerConsole {
		t.Errorf("record = refresh %v trigger %s", recs[0].Deployment.Refresh, recs[0].Deployment.Trigger)
	}
	_, history := e.get(t, "/apps/duo/deployments")
	got := text(history)
	if !strings.Contains(got, "Refreshed secrets (v") || !strings.Contains(got, "'s version)") {
		t.Errorf("the history lacks the kind words:\n%s", got)
	}
	// Redeploying a version that has a reference says it reads providers now.
	if !strings.Contains(got, "Reads secrets from providers again now, not as they were.") {
		t.Errorf("the redeploy confirmation lacks the note:\n%s", got)
	}
	noProviderSecrets(t, "refresh pages", body, history, e.Logs.String())

	t.Run("a refusal draws the page with the reason", func(t *testing.T) {
		e.source("bella-prod").Set("DATABASE_URL", providerValueSentinel+"-again")
		e.source("bella-prod").SetDown(true)
		defer e.source("bella-prod").SetDown(false)
		resp, body := e.submit(t, f, nil)
		if resp.StatusCode != http.StatusBadGateway || !strings.Contains(text(body), "bella-prod") || !strings.Contains(text(body), "Nothing was deployed") {
			t.Errorf("refresh with the provider down = %d:\n%s", resp.StatusCode, text(body))
		}
		noProviderSecrets(t, "refused refresh", body, e.Logs.String())
	})

	t.Run("an app with no reference", func(t *testing.T) {
		e2 := newConsoleEnv(t)
		e2.deploy(t, duoApp())
		resp, body := e2.postForm(t, "/apps/duo/secrets/refresh", url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "nothing to refresh") {
			t.Errorf("refresh without references = %d:\n%s", resp.StatusCode, text(body))
		}
	})
}

// Feature 037: a reference survives the Compose form. It is listed read-only,
// has no input, and a deploy from the form keeps it.
func TestComposeFormKeepsReferences(t *testing.T) {
	e := refsEnv(t)
	req := compose.Request{Compose: "services:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"8080:80\"]\n", Services: map[string]compose.Overlay{
		"web": {Secrets: []app.Secret{{Name: "DATABASE_URL", From: &app.SecretRef{Provider: "bella-prod", Path: "DATABASE_URL"}}}},
	}}
	rec, err := e.Svc.DeployCompose(context.Background(), "duo", req, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)

	_, page := e.get(t, "/apps/duo/compose")
	got := text(page)
	if !strings.Contains(got, "DATABASE_URL from bella-prod · DATABASE_URL") {
		t.Errorf("the form doesn't list the reference:\n%s", got)
	}
	if strings.Contains(page, `name="svc.web.secret.DATABASE_URL"`) {
		t.Error("the reference has an input: a value could be typed over it")
	}
	noProviderSecrets(t, "compose form", page)

	// Preview, then deploy from the form: the reference is still there.
	form := formWith(t, page, "/apps/duo/compose/preview")
	resp, body := e.submit(t, form, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
	// The preview's form has a second button that posts to /apps/duo/compose.
	preview := formWith(t, body, "/apps/duo/compose/preview")
	resp, body = e.postForm(t, "/apps/duo/compose", preview.Values)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	recs := e.deployments(t, "duo")
	e.waitDeploy(t, recs[0].Deployment.ID)
	stored, err := e.Store.GetApp(context.Background(), "duo")
	if err != nil {
		t.Fatal(err)
	}
	if secs := stored.App.Services[0].Secrets; len(secs) != 1 || secs[0].From == nil || secs[0].From.Provider != "bella-prod" {
		t.Errorf("stored secrets = %+v, want the reference kept", secs)
	}
	if secs := stored.Compose.Services["web"].Secrets; len(secs) != 1 || secs[0].From == nil {
		t.Errorf("kept overlay secrets = %+v, want the reference kept", secs)
	}
	noProviderSecrets(t, "compose deploy", body, e.Logs.String())
}

// B4 / FR-007: the resolved value, the API key and the private key are on no
// page the feature touches, in no export, address or log line, however the
// provider and the app are doing.
func TestProviderPagesHoldNoSecret(t *testing.T) {
	e := refsEnv(t)
	e.duoWithRef(t)
	// A refresh that rolled out, a failed test and a provider that is down: every state a page draws.
	e.source("bella-prod").Set("DATABASE_URL", providerValueSentinel+"-v2")
	_, page := e.get(t, "/apps/duo/variables")
	if resp, body := e.submit(t, formWith(t, page, "/apps/duo/secrets/refresh"), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("refresh = %d:\n%s", resp.StatusCode, text(body))
	}
	recs := e.deployments(t, "duo")
	e.waitDeploy(t, recs[0].Deployment.ID)
	e.source("bella-prod").SetRefused(true)
	_, spage := e.get(t, "/settings/secret-providers")
	e.submit(t, e.providerForm(t, spage, "/settings/secret-providers/test", "bella-prod"), nil)
	e.source("bella-prod").SetRefused(false)

	paths := []string{"/", "/apps/duo", "/apps/duo/variables", "/apps/duo/variables?refresh=unchanged", "/apps/duo/deployments", "/apps/duo/configure",
		"/apps/duo/compose", "/apps/duo/jobspec", "/parts/apps/duo/deployments", "/apps/duo/backups", "/projects", "/ops"}
	for _, tc := range settingsPages {
		paths = append(paths, tc.path)
	}
	for _, rec := range recs {
		paths = append(paths, "/apps/duo/deployments/"+rec.Deployment.ID+"/jobspec")
	}
	paths = append(paths, "/settings/secret-providers?tested=bella-prod", "/settings/secret-providers?edit=bella-prod")
	for _, path := range paths {
		resp, body := e.get(t, path)
		if resp.StatusCode >= 500 {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
		noProviderSecrets(t, "GET "+path, body, resp.Header.Get("Location"))
	}
	noProviderSecrets(t, "the log", e.Logs.String())
}
