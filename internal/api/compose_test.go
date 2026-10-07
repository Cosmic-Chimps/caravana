package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// composeBody is a deployCompose request for whoami as a Compose file.
func composeBody(t *testing.T, file string, services map[string]any) string {
	t.Helper()
	body := map[string]any{"compose": file}
	if services != nil {
		body["services"] = services
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const whoamiCompose = "services:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"8080:80\"]\n"

// US1: deployCompose answers like deployApp, with the file's warnings.
func TestDeployComposeEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	body := composeBody(t, whoamiCompose, map[string]any{
		"web": map[string]any{"domains": []any{map[string]any{"host": "whoami.localhost"}}, "health": map[string]any{"path": "/", "interval": "10s"}},
	})
	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/compose", "application/json; charset=UTF-8", body)
	if status != http.StatusAccepted {
		t.Fatalf("status %d: %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Deployment")
	d := decodeAs[deployment](t, raw)
	if header.Get("Location") != "/v1/apps/whoami/deployments/"+d.ID || d.JobVersion == nil {
		t.Errorf("deployment = %+v, Location %q", d, header.Get("Location"))
	}
	if len(d.Warnings) != 1 || d.Warnings[0].Field != "services.web.ports[0]" {
		t.Errorf("warnings = %+v, want the published port", d.Warnings)
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"kind":"compose"`) {
		t.Errorf("GET app = %d %s, want source kind compose", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")
}

// FR-002: preview answers the app and warnings, saves nothing, and never
// echoes a secret value.
func TestPreviewComposeEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	body := composeBody(t, whoamiCompose, map[string]any{
		"web": map[string]any{"secrets": []any{map[string]any{"name": "API_KEY", "value": secretMarker}}},
	})
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/compose/preview", "application/json", body)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "ComposePreview")
	if strings.Contains(string(raw), secretMarker) || !strings.Contains(string(raw), `"name":"API_KEY"`) {
		t.Errorf("preview = %s, want the secret's name and never its value", raw)
	}
	if _, err := e.Store.GetApp(context.Background(), "whoami"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetApp = %v, want nothing saved", err)
	}
	if e.Nomad.Registrations() != 0 {
		t.Error("a preview registered a job")
	}
}

// FR-004, FR-006: refusals are 400 naming the path in the file or request.
func TestComposeErrors(t *testing.T) {
	tests := []struct {
		name, path, body, wantField string
	}{
		{"build", "/compose", composeBody(t, "services:\n  web:\n    build: .\n", nil), "services.web.build"},
		{"build in a preview", "/compose/preview", composeBody(t, "services:\n  web:\n    build: .\n", nil), "services.web.build"},
		{"bind mount", "/compose", composeBody(t, "services:\n  web:\n    image: x\n    volumes: [\"./d:/d\"]\n", nil), "services.web.volumes[0]"},
		{"unset variable", "/compose", composeBody(t, "services:\n  web:\n    image: ${IMAGE}\n", nil), "compose"},
		{"overlay for an unknown service", "/compose", composeBody(t, whoamiCompose, map[string]any{"api": map[string]any{}}), "services.api"},
		{"overlay field unknown", "/compose", composeBody(t, whoamiCompose, map[string]any{"web": map[string]any{"bogus": 1}}), "services.web.bogus"},
		{"missing file", "/compose", `{}`, "compose"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, "")
			status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami"+tc.path, "application/json", tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", status, raw)
			}
			checkSchema(t, loadContract(t), raw, "Error")
			if got := decodeAs[errorBody](t, raw).Error; got.Field != tc.wantField {
				t.Errorf("error = %+v, want field %s", got, tc.wantField)
			}
			if header.Get("Location") != "" || e.Nomad.Registrations() != 0 {
				t.Error("a refused file left a deployment or a job")
			}
			if _, err := e.Store.GetApp(context.Background(), "whoami"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("GetApp = %v, want nothing saved", err)
			}
		})
	}
}
