package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// templApp is whoami with one template secret and one typed secret.
func templApp(tpl, refs string) string {
	return withSecrets(`[{"name": "ConnectionStrings__postgres", "template": "` + tpl + `", "refs": ` + refs + `}, {"name": "TYPED", "value": "t"}]`)
}

// A template secret (feature 053) deploys with its refs resolved, is read back as
// written, and its failures name the ref's field; no answer holds a value.
func TestDeployWithTemplateEndpoint(t *testing.T) {
	e, src := providersEnv(t)
	if status, raw := putProvider(t, e, "bella-prod", providerBody); status != http.StatusCreated {
		t.Fatalf("putSecretProvider = %d %s", status, raw)
	}
	refs := `{"pw": {"provider": "bella-prod", "path": "DATABASE_URL"}}`
	const tpl = "Host=postgres;Password=${pw};Cost=$$5"

	d := deployOK(t, e, "whoami", templApp(tpl, refs))
	waitHealthy(t, e, "whoami", d.ID)
	want := "Host=postgres;Password=" + providerValue + ";Cost=$5"
	if got := e.Nomad.Variable("nomad/jobs/whoami/web"); got["ConnectionStrings__postgres"] != want || got["TYPED"] != "t" {
		t.Fatalf("store = %v, want the rendered and the typed value", got)
	}

	var seen []string
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET app: %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")
	if !strings.Contains(string(raw), `"template":"Host=postgres;Password=${pw};Cost=$$5","refs":{"pw":{"provider":"bella-prod","path":"DATABASE_URL"}}`) {
		t.Errorf("GET app = %s, want the template and refs as written", raw)
	}
	seen = append(seen, string(raw))
	for _, path := range []string{"/v1/apps", "/v1/apps/whoami/deployments", "/v1/apps/whoami/deployments/" + d.ID, "/v1/attention", "/v1/apps/whoami/jobspec"} {
		_, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		seen = append(seen, string(raw))
	}

	failures := []struct {
		name       string
		change     func()
		body       string
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{"unknown provider", func() {}, templApp(tpl, `{"pw": {"provider": "nope", "path": "X"}}`), 400, codeInvalidField, "services[0].secrets[0].refs.pw.provider"},
		{"missing path", func() {}, templApp(tpl, `{"pw": {"provider": "bella-prod", "path": "NOT_THERE"}}`), 422, codeSecretNotFound, "services[0].secrets[0].refs.pw.path"},
		{"lone dollar", func() {}, templApp("a$b", refs), 400, codeInvalidField, "services[0].secrets[0].template"},
		{"unused ref", func() {}, templApp("plain", refs), 400, codeInvalidField, "services[0].secrets[0].refs.pw"},
		{"refused", func() { src.SetRefused(true) }, templApp(tpl, refs), 422, codeSecretProviderRefused, "services[0].secrets[0].refs.pw"},
		{"down", func() { src.SetRefused(false); src.SetDown(true) }, templApp(tpl, refs), 502, codeSecretProviderUnavail, "services[0].secrets[0].refs.pw"},
	}
	for _, f := range failures {
		f.change()
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", f.body)
		wantError(t, f.name, status, raw, f.wantStatus, f.wantCode, f.wantField)
		seen = append(seen, string(raw))
	}
	src.SetDown(false)
	if got := e.Nomad.Variable("nomad/jobs/whoami/web"); got["ConnectionStrings__postgres"] != want {
		t.Errorf("store after the failed deploys = %v, want the running value", got)
	}

	// The value changes at the provider: Refresh re-renders it.
	src.Set("DATABASE_URL", providerValue+"-2")
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/secrets/refresh", "", "")
	if status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("refresh = %d %s", status, raw)
	}
	seen = append(seen, string(raw))

	rec, err := e.Store.GetApp(context.Background(), "whoami")
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, string(mustJSON(t, rec)), string(mustJSON(t, e.Nomad.Job("whoami"))), e.Logs.String())
	for _, out := range seen {
		for _, secret := range []string{providerValue, providerAPIKey, providerPrivateKey} {
			if strings.Contains(out, secret) {
				t.Errorf("%q leaked into: %.300s", secret, out)
			}
		}
	}
}
