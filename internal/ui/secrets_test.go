package ui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// secretMarker is a stored value the console must never draw.
const secretMarker = "s3cr3t-marker"

// secretsEnv has duo with one secret on web, its value in the fake Nomad.
func secretsEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	a := duoApp()
	a.Services[0].Secrets = []app.Secret{{Name: "DATABASE_URL"}}
	e.seed(t, a, seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.Nomad.SetVariable("nomad/jobs/duo/web", map[string]string{"DATABASE_URL": secretMarker})
	return e
}

// variablesForm is the "Add or change" form of the app's Variables page as the page draws it.
func (e *consoleEnv) variablesForm(t *testing.T, id string) plainForm {
	t.Helper()
	resp, body := e.get(t, "/apps/"+id+"/variables")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/%s/variables = %d:\n%s", id, resp.StatusCode, text(body))
	}
	return formWith(t, body, "/apps/"+id+"/variables")
}

// FR-016: Configure's panel lists names, the Variables page shows a secret as dots with a lock,
// and no form holds a value.
func TestSecretsAreNamesOnly(t *testing.T) {
	e := secretsEnv(t)
	resp, body := e.get(t, "/apps/duo/configure")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET configure = %d", resp.StatusCode)
	}
	got := text(body)
	for _, want := range []string{"Secrets", "DATABASE_URL", "1 secret in Nomad Variables. Values are never shown here.", "Manage variables and secrets"} {
		if !strings.Contains(got, want) {
			t.Errorf("Configure lacks %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{`action="/apps/duo/secrets`, `name="value"`} {
		if strings.Contains(body, gone) {
			t.Errorf("Configure still has %q: the set and remove forms live on the Variables page", gone)
		}
	}

	resp, body = e.get(t, "/apps/duo/variables")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET variables = %d", resp.StatusCode)
	}
	for _, want := range []string{
		`<form method="post" action="/apps/duo/variables" class="cv-formcol cv-formcol-narrow" autocomplete="off">`,
		`<option value="web" selected>web</option><option value="worker">worker</option>`,
		`<input type="hidden" name="name" value="DATABASE_URL">`,
		`<input type="hidden" name="revision"`,
		`name="value" rows="3" autocomplete="off" autocapitalize="off" spellcheck="false"></textarea>`,
		`<span class="cv-dots" aria-hidden="true">••••••••••</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Variables page lacks %q", want)
		}
	}
	if strings.Contains(body, secretMarker) {
		t.Error("a secret value is in the page")
	}
}

func TestSetSecretFromTheConsole(t *testing.T) {
	e := secretsEnv(t)
	f := e.variablesForm(t, "duo")
	resp, body := e.submit(t, f, url.Values{"service": {"web"}, "name": {" API_KEY "}, "value": {"line one\r\nline two"}, "secret": {"1"}})
	recs := e.deployments(t, "duo")
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duo/deployments#deployment-"+recs[0].Deployment.ID {
		t.Fatalf("POST = %d to %q, want 303 to the new deployment:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	got := e.Nomad.Variable("nomad/jobs/duo/web")
	if got["API_KEY"] != "line one\nline two" || got["DATABASE_URL"] != secretMarker {
		t.Errorf("web's secrets = %v, want API_KEY set (LF line ends) and DATABASE_URL kept", got)
	}
	if len(recs) != 2 || recs[0].Deployment.Trigger != app.TriggerConsole {
		t.Errorf("deployments = %d, want a new one from the console", len(recs))
	}
	if names := e.storedApp(t, "duo").Services[0].Secrets; len(names) != 2 || names[1].Name != "API_KEY" || names[1].Value != nil {
		t.Errorf("stored secrets = %+v, want DATABASE_URL and API_KEY, names only", names)
	}

	// Remove is a form in the secret's row, on the revision the page was drawn from.
	_, page := e.get(t, "/apps/duo/variables")
	remove := formWith(t, page, "/apps/duo/secrets/remove")
	if remove.Values.Get("revision") == "" || remove.Values.Get("name") != "API_KEY" && remove.Values.Get("name") != "DATABASE_URL" {
		t.Fatalf("the remove form = %v", remove.Values)
	}
	resp, _ = e.submit(t, remove, url.Values{"service": {"web"}, "name": {"DATABASE_URL"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/duo/deployments#deployment-") {
		t.Fatalf("remove = %d, want 303 to the deployment", resp.StatusCode)
	}
	if got := e.Nomad.Variable("nomad/jobs/duo/web"); len(got) != 1 || got["API_KEY"] == "" {
		t.Errorf("web's secrets after remove = %v, want only API_KEY", got)
	}
}

func TestSetSecretRefused(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(e *consoleEnv)
		form       url.Values
		wantStatus int
		wantText   string
		wantInBody string
	}{
		{name: "bad name", form: url.Values{"service": {"web"}, "name": {"1BAD"}, "value": {secretMarker}, "secret": {"1"}},
			wantStatus: http.StatusUnprocessableEntity, wantText: "not starting with a digit",
			wantInBody: `value="1BAD" required autocomplete="off" autocapitalize="off" spellcheck="false" aria-invalid="true"`},
		{name: "same name as a variable", form: url.Values{"service": {"web"}, "name": {"MODE"}, "value": {secretMarker}, "secret": {"1"}},
			wantStatus: http.StatusUnprocessableEntity, wantText: "MODE is already a variable of this service"},
		{name: "unknown service", form: url.Values{"service": {"api"}, "name": {"A"}, "value": {secretMarker}, "secret": {"1"}},
			wantStatus: http.StatusUnprocessableEntity, wantText: "This app has no service api."},
		{name: "store refuses", setup: func(e *consoleEnv) { e.Nomad.FailVariables(http.StatusForbidden) },
			form:       url.Values{"service": {"web"}, "name": {"API_KEY"}, "value": {secretMarker}, "secret": {"1"}},
			wantStatus: http.StatusBadGateway, wantText: "The secret store refused the change"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := secretsEnv(t)
			f := e.variablesForm(t, "duo")
			if tc.setup != nil {
				tc.setup(e)
			}
			resp, body := e.submit(t, f, tc.form)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("POST = %d, want %d:\n%s", resp.StatusCode, tc.wantStatus, text(body))
			}
			if !strings.Contains(text(body), tc.wantText) {
				t.Errorf("the page lacks %q:\n%s", tc.wantText, text(body))
			}
			if tc.wantInBody != "" && !strings.Contains(body, tc.wantInBody) {
				t.Errorf("the page lacks %q", tc.wantInBody)
			}
			if strings.Contains(body, secretMarker) {
				t.Error("the refused value is in the page")
			}
			if n := len(e.deployments(t, "duo")); n != 1 {
				t.Errorf("deployments = %d, want none added", n)
			}
		})
	}

	t.Run("removing a secret the service doesn't have", func(t *testing.T) {
		e := secretsEnv(t)
		resp, body := e.postForm(t, "/apps/duo/secrets/remove", url.Values{"service": {"web"}, "name": {"NOPE"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), `has no secret "NOPE"`) {
			t.Errorf("remove = %d:\n%s", resp.StatusCode, text(body))
		}
	})
}
