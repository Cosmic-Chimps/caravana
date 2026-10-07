package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// secretMarker is a value that must never show up outside the secret store.
const secretMarker = "s3cr3t-marker"

// withSecrets returns whoamiBody with secrets on its web service. secretsJSON
// is the JSON array, e.g. `[{"name": "A", "value": "1"}]`.
func withSecrets(secretsJSON string) string {
	return strings.Replace(whoamiBody, `"image": "traefik/whoami:latest",`,
		`"image": "traefik/whoami:latest", "secrets": `+secretsJSON+`,`, 1)
}

// waitHealthy polls the deployment until it is healthy.
func waitHealthy(t *testing.T, e *testEnv, appID, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/"+appID+"/deployments/"+id, "", "")
		switch decodeAs[deployment](t, raw).Status {
		case "healthy":
			return
		case "failed", "reverted", "superseded":
			t.Fatalf("deployment ended %s", raw)
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment %s not healthy after 5s", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// US1, FR-005, SC-002: values reach the store and nothing else.
func TestDeploySecretsEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployOK(t, e, "whoami", withSecrets(`[{"name": "DATABASE_URL", "value": "`+secretMarker+`"}, {"name": "API_KEY", "value": "k1"}]`))
	var seen []string // every response body, to search for the value
	seen = append(seen, string(mustJSON(t, d)))
	waitHealthy(t, e, "whoami", d.ID)

	if got := e.Nomad.Variable("nomad/jobs/whoami/web"); got["DATABASE_URL"] != secretMarker || got["API_KEY"] != "k1" {
		t.Fatalf("store = %v, want both secrets", got)
	}

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET app: %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")
	if !strings.Contains(string(raw), `"secrets":[{"name":"DATABASE_URL"},{"name":"API_KEY"}]`) {
		t.Errorf("GET app = %s, want the two names", raw)
	}
	seen = append(seen, string(raw))
	for _, path := range []string{"/v1/apps", "/v1/apps/whoami/deployments", "/v1/apps/whoami/deployments/" + d.ID} {
		_, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		seen = append(seen, string(raw))
	}

	// Deploying the app as read back keeps every value.
	keepAll := withSecrets(`[{"name": "DATABASE_URL"}, {"name": "API_KEY"}]`)
	d2 := deployOK(t, e, "whoami", keepAll)
	waitHealthy(t, e, "whoami", d2.ID)
	if got := e.Nomad.Variable("nomad/jobs/whoami/web"); got["DATABASE_URL"] != secretMarker {
		t.Errorf("store after keep = %v, want the value kept", got)
	}

	// Leaving one out removes it.
	d3 := deployOK(t, e, "whoami", withSecrets(`[{"name": "API_KEY"}]`))
	waitHealthy(t, e, "whoami", d3.ID)
	if got := e.Nomad.Variable("nomad/jobs/whoami/web"); len(got) != 1 || got["API_KEY"] != "k1" {
		t.Errorf("store after removing DATABASE_URL = %v, want only API_KEY", got)
	}

	rec, err := e.Store.GetApp(context.Background(), "whoami")
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, string(mustJSON(t, rec)), string(mustJSON(t, e.Nomad.Job("whoami"))), e.Logs.String())
	for _, out := range seen {
		if strings.Contains(out, secretMarker) {
			t.Errorf("the secret value leaked into: %.300s", out)
		}
	}
}

func TestDeploySecretsErrors(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(e *testEnv)
		body         string
		wantStatus   int
		wantCode     string
		wantField    string
		wantLocation bool
	}{
		{name: "bad name", body: withSecrets(`[{"name": "1BAD", "value": "x"}]`),
			wantStatus: 400, wantCode: codeInvalidField, wantField: "services[0].secrets[0].name"},
		{name: "same name as env", body: strings.Replace(withSecrets(`[{"name": "PORT", "value": "x"}]`),
			`"ports":`, `"env": [{"name": "PORT", "value": "80"}], "ports":`, 1),
			wantStatus: 400, wantCode: codeInvalidField, wantField: "services[0].secrets[0].name"},
		{name: "keep without a value", body: withSecrets(`[{"name": "API_KEY"}]`),
			wantStatus: 400, wantCode: codeInvalidField, wantField: "services[0].secrets[0].value"},
		{name: "too large", body: withSecrets(`[{"name": "BIG", "value": "` + strings.Repeat("x", 61441) + `"}]`),
			wantStatus: 400, wantCode: codeInvalidField, wantField: "services[0].secrets"},
		{name: "store refuses", setup: func(e *testEnv) { e.Nomad.FailVariables(http.StatusForbidden) },
			body:       withSecrets(`[{"name": "API_KEY", "value": "` + secretMarker + `"}]`),
			wantStatus: 502, wantCode: codeSecretStoreUnavail},
		{name: "store refuses an app without secrets", setup: func(e *testEnv) { e.Nomad.FailVariables(http.StatusForbidden) },
			body:       whoamiBody,
			wantStatus: 502, wantCode: codeSecretStoreUnavail, wantLocation: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, "")
			if tc.setup != nil {
				tc.setup(e)
			}
			status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", tc.body)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %.300s)", status, tc.wantStatus, raw)
			}
			checkSchema(t, loadContract(t), raw, "Error")
			got := decodeAs[errorBody](t, raw).Error
			if got.Code != tc.wantCode || got.Field != tc.wantField {
				t.Errorf("error = %+v, want code %s field %q", got, tc.wantCode, tc.wantField)
			}
			if (header.Get("Location") != "") != tc.wantLocation {
				t.Errorf("Location = %q, want one: %v", header.Get("Location"), tc.wantLocation)
			}
			if strings.Contains(string(raw), secretMarker) || strings.Contains(e.Logs.String(), secretMarker) {
				t.Error("the secret value leaked into the error or the log")
			}
			if e.Nomad.Registrations() != 0 {
				t.Errorf("registrations = %d, want 0", e.Nomad.Registrations())
			}
		})
	}
}
