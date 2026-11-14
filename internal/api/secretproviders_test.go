package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Credentials and values no answer, record, Nomad job or request log may hold.
const (
	providerAPIKey     = "bax-s3cret-sentinel-037-api-key"
	providerPrivateKey = "s3cret-sentinel-037-api-private-key"
	providerValue      = "s3cret-sentinel-037-api"
)

// providersEnv is a test environment whose providers are all one in-memory
// source, which holds DATABASE_URL (the sentinel value) and API_KEY.
func providersEnv(t *testing.T) (*testEnv, *secretstest.Source) {
	t.Helper()
	src := secretstest.NewSource(map[string]string{"DATABASE_URL": providerValue, "API_KEY": "k1"})
	e := newTestEnvWith(t, "", func(svc *service.Service) {
		svc.SetSecretSources(func(p store.SecretProvider, creds map[string]string) (secrets.Source, error) {
			return src, nil
		})
	})
	return e, src
}

const providerBody = `{"kind": "bella", "bella": {"url": "https://api.bella-baxter.io", "project": "shop", "environment": "production"},
	"apiKey": "` + providerAPIKey + `", "privateKey": "` + providerPrivateKey + `"}`

func putProvider(t *testing.T, e *testEnv, name, body string) (int, []byte) {
	t.Helper()
	status, _, raw := call(t, http.MethodPut, e.URL+"/v1/secret-providers/"+name, "application/json", body)
	return status, raw
}

func TestSecretProviderEndpoints(t *testing.T) {
	e, _ := providersEnv(t)
	doc := loadContract(t)

	status, raw := putProvider(t, e, "bella-prod", providerBody)
	if status != http.StatusCreated {
		t.Fatalf("putSecretProvider = %d %s, want 201", status, raw)
	}
	checkSchema(t, doc, raw, "SecretProvider")
	got := decodeAs[struct {
		Name    string   `json:"name"`
		Allowed []string `json:"allowed"`
		UsedBy  []string `json:"usedBy"`
		Bella   struct {
			URL, Project, Environment string
		} `json:"bella"`
		LastTest any `json:"lastTest"`
	}](t, raw)
	if got.Name != "bella-prod" || got.Bella.Project != "shop" || got.Allowed == nil || got.UsedBy == nil || got.LastTest != nil {
		t.Errorf("answer = %s, want the provider with empty lists and no test", raw)
	}

	// A save without the credentials keeps them: 200.
	status, raw = putProvider(t, e, "bella-prod", `{"kind": "bella", "bella": {"url": "https://api.bella-baxter.io", "project": "shop", "environment": "staging"}}`)
	if status != http.StatusOK {
		t.Fatalf("second putSecretProvider = %d %s, want 200", status, raw)
	}
	checkSchema(t, doc, raw, "SecretProvider")
	if creds := e.Nomad.Variable("caravana/secret-providers/bella-prod"); creds["api_key"] != providerAPIKey {
		t.Errorf("credentials after a save without them = %v, want the old ones", creds)
	}

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/secret-providers/bella-prod/test", "", "")
	if status != http.StatusOK {
		t.Fatalf("testSecretProvider = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "SecretProvider")
	if !strings.Contains(string(raw), `"message":"2 secrets readable"`) || !strings.Contains(string(raw), `"ok":true`) {
		t.Errorf("test answer = %s, want 2 secrets readable", raw)
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/secret-providers", "", "")
	if status != http.StatusOK {
		t.Fatalf("listSecretProviders = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "SecretProviderList")

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/secret-providers/nope/test", "", "")
	wantError(t, "test unknown", status, raw, http.StatusNotFound, codeSecretProviderNotFound, "")
	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/secret-providers/nope", "", "")
	wantError(t, "delete unknown", status, raw, http.StatusNotFound, codeSecretProviderNotFound, "")

	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/secret-providers/bella-prod", "", "")
	if status != http.StatusNoContent {
		t.Fatalf("deleteSecretProvider = %d %s, want 204", status, raw)
	}
	if got := e.Nomad.VariablePaths(); len(got) != 0 {
		t.Errorf("variables after the delete = %v, want none", got)
	}
}

func TestPutSecretProviderFieldErrors(t *testing.T) {
	e, _ := providersEnv(t)
	tests := []struct {
		name, body, field string
	}{
		{"unknown kind", `{"kind": "vault", "bella": {"url": "https://x.io", "project": "p", "environment": "e"}, "apiKey": "k", "privateKey": "p"}`, "kind"},
		{"no API key the first time", `{"kind": "bella", "bella": {"url": "https://x.io", "project": "p", "environment": "e"}, "privateKey": "p"}`, "apiKey"},
		{"empty private key", `{"kind": "bella", "bella": {"url": "https://x.io", "project": "p", "environment": "e"}, "apiKey": "k", "privateKey": ""}`, "privateKey"},
		{"unknown project", `{"kind": "bella", "bella": {"url": "https://x.io", "project": "p", "environment": "e"}, "allowed": ["nope"], "apiKey": "k", "privateKey": "p"}`, "allowed[0]"},
	}
	for _, tt := range tests {
		status, raw := putProvider(t, e, "bella-x", tt.body)
		wantError(t, tt.name, status, raw, http.StatusBadRequest, codeInvalidField, tt.field)
	}
	status, raw := putProvider(t, e, "Bad_Name", providerBody)
	wantError(t, "bad name", status, raw, http.StatusBadRequest, codeInvalidField, "name")
	// Credentials are never a property of an answer.
	if strings.Contains(string(raw), providerAPIKey) {
		t.Errorf("an error holds a credential: %s", raw)
	}
}

// refApp is whoami with one reference and one typed secret.
func refApp(path string) string {
	return withSecrets(`[{"name": "DATABASE_URL", "from": {"provider": "bella-prod", "path": "` + path + `"}}, {"name": "TYPED", "value": "t"}]`)
}

func TestDeployWithReferenceEndpoint(t *testing.T) {
	e, src := providersEnv(t)
	if status, raw := putProvider(t, e, "bella-prod", providerBody); status != http.StatusCreated {
		t.Fatalf("putSecretProvider = %d %s", status, raw)
	}
	seen := []string{}

	d := deployOK(t, e, "whoami", refApp("DATABASE_URL"))
	waitHealthy(t, e, "whoami", d.ID)
	if got := e.Nomad.Variable("nomad/jobs/whoami/web"); got["DATABASE_URL"] != providerValue || got["TYPED"] != "t" {
		t.Fatalf("store = %v, want the resolved and the typed value", got)
	}

	// The reference is read back as written; the answers hold no value.
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET app: %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")
	if !strings.Contains(string(raw), `{"name":"DATABASE_URL","from":{"provider":"bella-prod","path":"DATABASE_URL"}}`) {
		t.Errorf("GET app = %s, want the reference as written", raw)
	}
	seen = append(seen, string(raw))
	for _, path := range []string{"/v1/apps", "/v1/apps/whoami/deployments", "/v1/apps/whoami/deployments/" + d.ID, "/v1/secret-providers", "/v1/attention"} {
		_, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		seen = append(seen, string(raw))
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/jobspec", "", "")
	seen = append(seen, string(raw))

	// The failures of a deploy, each with its code, status and field.
	failures := []struct {
		name       string
		change     func()
		body       string
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{"unknown provider", func() {}, strings.ReplaceAll(refApp("X"), "bella-prod", "nope"), 400, codeInvalidField, "services[0].secrets[0].from.provider"},
		{"missing path", func() {}, refApp("NOT_THERE"), 422, codeSecretNotFound, "services[0].secrets[0].from.path"},
		{"refused", func() { src.SetRefused(true) }, refApp("DATABASE_URL"), 422, codeSecretProviderRefused, "services[0].secrets[0].from"},
		{"down", func() { src.SetRefused(false); src.SetDown(true) }, refApp("DATABASE_URL"), 502, codeSecretProviderUnavail, "services[0].secrets[0].from"},
	}
	for _, f := range failures {
		f.change()
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", f.body)
		wantError(t, f.name, status, raw, f.wantStatus, f.wantCode, f.wantField)
		seen = append(seen, string(raw))
	}
	src.SetDown(false)
	// The running copies' values were never touched.
	if got := e.Nomad.Variable("nomad/jobs/whoami/web"); got["DATABASE_URL"] != providerValue {
		t.Errorf("store after the failed deploys = %v, want the running value", got)
	}

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

func TestRefreshSecretsEndpoint(t *testing.T) {
	e, src := providersEnv(t)
	doc := loadContract(t)
	if status, raw := putProvider(t, e, "bella-prod", providerBody); status != http.StatusCreated {
		t.Fatalf("putSecretProvider = %d %s", status, raw)
	}

	// No version yet.
	deploy := func(body string) deployment {
		d := deployOK(t, e, "whoami", body)
		waitHealthy(t, e, "whoami", d.ID)
		return d
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/secrets/refresh", "", "")
	wantError(t, "unknown app", status, raw, http.StatusNotFound, codeNotFound, "")

	// A live version without references.
	live := deploy(withSecrets(`[{"name": "TYPED", "value": "t"}]`))
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/secrets/refresh", "", "")
	wantError(t, "no references", status, raw, http.StatusConflict, codeNoSecretReferences, "")

	// With a reference and nothing changed: 200.
	live = deploy(refApp("DATABASE_URL"))
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/secrets/refresh", "", "")
	if status != http.StatusOK || strings.TrimSpace(string(raw)) != `{"changed":false}` {
		t.Fatalf("refresh with nothing changed = %d %s, want 200 {\"changed\":false}", status, raw)
	}
	checkSchema(t, doc, raw, "SecretsUnchanged")

	// A changed value: 202 with the deployment.
	src.Set("DATABASE_URL", "rotated")
	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/secrets/refresh", "", "")
	if status != http.StatusAccepted {
		t.Fatalf("refresh with a changed value = %d %s, want 202", status, raw)
	}
	checkSchema(t, doc, raw, "SecretsRefreshed")
	got := decodeAs[struct {
		Changed    bool `json:"changed"`
		Deployment struct {
			ID               string `json:"id"`
			Source           string `json:"source"`
			SecretsRefreshed bool   `json:"secretsRefreshed"`
		} `json:"deployment"`
	}](t, raw)
	if !got.Changed || !got.Deployment.SecretsRefreshed || got.Deployment.Source != live.ID {
		t.Errorf("answer = %s, want a refresh of %s", raw, live.ID)
	}
	if loc := header.Get("Location"); loc != "/v1/apps/whoami/deployments/"+got.Deployment.ID {
		t.Errorf("Location = %q", loc)
	}
	waitHealthy(t, e, "whoami", got.Deployment.ID)
	if v := e.Nomad.Variable("nomad/jobs/whoami/web"); v["DATABASE_URL"] != "rotated" || v["TYPED"] != "t" {
		t.Errorf("store after the refresh = %v, want the new value", v)
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+got.Deployment.ID, "", "")
	checkSchema(t, doc, raw, "Deployment")
	if !strings.Contains(string(raw), `"secretsRefreshed":true`) {
		t.Errorf("the deployment read back = %s, want secretsRefreshed", raw)
	}

	// Provider failures deploy nothing and use the deploy codes.
	src.Set("DATABASE_URL", "again")
	src.SetDown(true)
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/secrets/refresh", "", "")
	wantError(t, "provider down", status, raw, http.StatusBadGateway, codeSecretProviderUnavail, "services[0].secrets[0].from")
	src.SetDown(false)
	src.SetRefused(true)
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/secrets/refresh", "", "")
	wantError(t, "provider refuses", status, raw, http.StatusUnprocessableEntity, codeSecretProviderRefused, "services[0].secrets[0].from")
	src.SetRefused(false)

	// Stopped.
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/stop", "", ""); status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("stop = %d %s", status, raw)
	}
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/secrets/refresh", "", "")
	wantError(t, "stopped", status, raw, http.StatusConflict, "app_stopped", "")
}

func TestDeleteSecretProviderInUse(t *testing.T) {
	e, _ := providersEnv(t)
	if status, raw := putProvider(t, e, "bella-prod", providerBody); status != http.StatusCreated {
		t.Fatalf("putSecretProvider = %d %s", status, raw)
	}
	d := deployOK(t, e, "whoami", refApp("DATABASE_URL"))
	waitHealthy(t, e, "whoami", d.ID)

	status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/secret-providers/bella-prod", "", "")
	body := wantError(t, "delete in use", status, raw, http.StatusConflict, codeSecretProviderInUse, "")
	if !strings.Contains(body.Error.Message, "whoami") {
		t.Errorf("message = %q, want it to name the app", body.Error.Message)
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/secret-providers", "", "")
	if !strings.Contains(string(raw), `"usedBy":["whoami"]`) {
		t.Errorf("list = %s, want usedBy whoami", raw)
	}
}
