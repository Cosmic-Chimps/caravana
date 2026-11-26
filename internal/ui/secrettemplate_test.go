package ui

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// tplRefs is the two references of the template secret these tests use.
func tplSecret() app.Secret {
	return app.Secret{Name: "ConnectionStrings__postgres", Template: "Host=postgres;Password=${pw};Key=${k}", Refs: map[string]app.SecretRef{
		"pw": {Provider: "bella-prod", Path: "DATABASE_URL"},
		"k":  {Provider: "bella-prod", Path: "API_KEY"},
	}}
}

// A template secret (feature 053) is listed read-only on the Variables page, by the
// references it reads, with no value and no input; the pages hold no value.
func TestVariablesShowTemplates(t *testing.T) {
	e := refsEnv(t)
	a := duoApp()
	a.Services[0].Secrets = []app.Secret{tplSecret()}
	e.deploy(t, a)

	resp, page := e.get(t, "/apps/duo/variables")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET variables = %d", resp.StatusCode)
	}
	got := text(page)
	for _, want := range []string{
		"ConnectionStrings__postgres",
		"template with 2 references: bella-prod/API_KEY, bella-prod/DATABASE_URL",
		"2 references", // the provider's row counts each reference
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(page, `class="cv-dots"`) {
		t.Error("a template secret is drawn as a typed one")
	}
	for _, path := range []string{"/apps/duo", "/apps/duo/variables", "/apps/duo/configure", "/apps/duo/compose", "/apps/duo/deployments", "/apps/duo/jobspec", "/settings/secret-providers"} {
		resp, body := e.get(t, path)
		if resp.StatusCode >= 500 {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
		noProviderSecrets(t, "GET "+path, body)
	}
	noProviderSecrets(t, "the log", e.Logs.String())
}

// The Compose form of an app that exists has no row for a template secret, and a
// preview and a deploy from the form keep it, in the app and in the kept overlay.
func TestComposeFormKeepsTemplates(t *testing.T) {
	e := refsEnv(t)
	req := compose.Request{Compose: "services:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"8080:80\"]\n", Services: map[string]compose.Overlay{
		"web": {Secrets: []app.Secret{tplSecret()}},
	}}
	rec, err := e.Svc.DeployCompose(context.Background(), "duo", req, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)

	// The form of an app that exists has only the file (feature 059): no row for the template secret.
	_, page := e.get(t, "/apps/duo/compose")
	if strings.Contains(page, `name="svc.web.secret.ConnectionStrings__postgres"`) || strings.Contains(page, `type="password"`) {
		t.Error("the form has an input for the template secret: a value could be typed over it")
	}
	noProviderSecrets(t, "compose form", page)

	form := formWith(t, page, "/apps/duo/compose/preview")
	resp, body := e.submit(t, form, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
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
	want := tplSecret()
	if secs := stored.App.Services[0].Secrets; len(secs) != 1 || secs[0].Template != want.Template || len(secs[0].Refs) != 2 || secs[0].Refs["pw"] != want.Refs["pw"] {
		t.Errorf("stored secrets = %+v, want the template kept", secs)
	}
	if secs := stored.Compose.Services["web"].Secrets; len(secs) != 1 || secs[0].Template != want.Template || len(secs[0].Refs) != 2 {
		t.Errorf("kept overlay secrets = %+v, want the template kept", secs)
	}
	noProviderSecrets(t, "compose deploy", body, e.Logs.String())
}

// A save from the Configure form keeps a template secret: it is not a field of the form.
func TestConfigureKeepsTemplates(t *testing.T) {
	stored := duoApp()
	stored.Services[0].Secrets = []app.Secret{tplSecret()}
	form := formOf(stored, "r")
	posted, ok := parseEditForm(postOf(form), stored)
	if !ok {
		t.Fatal("the form doesn't parse")
	}
	got := applyEdit(stored, posted)
	if !reflect.DeepEqual(got.Services[0].Secrets, stored.Services[0].Secrets) {
		t.Errorf("secrets = %+v, want the template kept", got.Services[0].Secrets)
	}
}
