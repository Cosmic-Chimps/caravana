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

// US3, FR-016: the panel lists names, and the form never holds a value.
func TestConfigureListsSecretNames(t *testing.T) {
	e := secretsEnv(t)
	resp, body := e.get(t, "/apps/duo/configure")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET configure = %d", resp.StatusCode)
	}
	got := text(body)
	for _, want := range []string{"Secrets", "DATABASE_URL", "No secrets for worker.", "Values can't be read back."} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	for _, want := range []string{
		`<form method="post" action="/apps/duo/secrets" class="flex flex-col gap-3" autocomplete="off">`,
		`<option value="web">web</option><option value="worker">worker</option>`,
		`<input type="hidden" name="name" value="DATABASE_URL">`,
		`name="value" rows="3" autocomplete="off" autocapitalize="off" spellcheck="false"></textarea>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the tab lacks %q", want)
		}
	}
	if strings.Contains(body, secretMarker) {
		t.Error("a secret value is in the page")
	}
}

func TestSetSecretFromTheConsole(t *testing.T) {
	e := secretsEnv(t)
	resp, body := e.postForm(t, "/apps/duo/secrets", url.Values{
		"service": {"web"}, "name": {" API_KEY "}, "value": {"line one\r\nline two"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duo/deployments" {
		t.Fatalf("POST = %d to %q, want 303 to deployments:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	got := e.Nomad.Variable("nomad/jobs/duo/web")
	if got["API_KEY"] != "line one\nline two" || got["DATABASE_URL"] != secretMarker {
		t.Errorf("web's secrets = %v, want API_KEY set (LF line ends) and DATABASE_URL kept", got)
	}
	if recs := e.deployments(t, "duo"); len(recs) != 2 || recs[0].Deployment.Trigger != app.TriggerConsole {
		t.Errorf("deployments = %d, want a new one from the console", len(recs))
	}
	if names := e.storedApp(t, "duo").Services[0].Secrets; len(names) != 2 || names[1].Name != "API_KEY" || names[1].Value != nil {
		t.Errorf("stored secrets = %+v, want DATABASE_URL and API_KEY, names only", names)
	}

	resp, _ = e.postForm(t, "/apps/duo/secrets/remove", url.Values{"service": {"web"}, "name": {"DATABASE_URL"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove = %d, want 303", resp.StatusCode)
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
		{name: "bad name", form: url.Values{"service": {"web"}, "name": {"1BAD"}, "value": {secretMarker}},
			wantStatus: http.StatusUnprocessableEntity, wantText: "not starting with a digit",
			wantInBody: `value="1BAD" required autocomplete="off" autocapitalize="off" spellcheck="false" aria-invalid="true"`},
		{name: "same name as env", form: url.Values{"service": {"web"}, "name": {"MODE"}, "value": {secretMarker}},
			wantStatus: http.StatusUnprocessableEntity, wantText: "also a plain env var"},
		{name: "unknown service", form: url.Values{"service": {"api"}, "name": {"A"}, "value": {secretMarker}},
			wantStatus: http.StatusUnprocessableEntity, wantText: `app "duo" has no service "api"`},
		{name: "store refuses", setup: func(e *consoleEnv) { e.Nomad.FailVariables(http.StatusForbidden) },
			form:       url.Values{"service": {"web"}, "name": {"API_KEY"}, "value": {secretMarker}},
			wantStatus: http.StatusBadGateway, wantText: "The secret store refused the change"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := secretsEnv(t)
			if tc.setup != nil {
				tc.setup(e)
			}
			resp, body := e.postForm(t, "/apps/duo/secrets", tc.form)
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
