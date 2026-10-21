package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// An app can't take the console's own domain (CARAVANA_PUBLIC_URL): every
// operation that saves domains answers 400 invalid_field naming the domain, and
// saves and deploys nothing. Without the setting nothing is compared.
func TestConsoleDomainRefused(t *testing.T) {
	const message = "caravana.example.com is the console's own address (CARAVANA_PUBLIC_URL): " +
		"an app can't use it, or the console would stop answering. Use another domain."
	clash := strings.Replace(whoamiBody, "whoami.localhost", "caravana.example.com", 1)
	composeClash := composeBody(t, whoamiCompose, map[string]any{
		"web": map[string]any{"domains": []any{map[string]any{"host": "caravana.example.com"}}},
	})

	tests := []struct {
		name, path, body, field string
	}{
		{"deployApp", "/v1/apps/whoami/deployments", clash, "services[0].domains[0].host"},
		{"createApp", "/v1/apps", strings.Replace(clash, "{", `{"id": "whoami",`, 1), "services[0].domains[0].host"},
		{"createApp without deploying", "/v1/apps?deploy=false", strings.Replace(clash, "{", `{"id": "whoami",`, 1), "services[0].domains[0].host"},
		{"deployCompose", "/v1/apps/whoami/compose", composeClash, "services.web.domains[0].host"},
		{"previewCompose", "/v1/apps/whoami/compose/preview", composeClash, "services.web.domains[0].host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, "")
			e.Service.SetPublicURL("https://Caravana.Example.com:8443/")
			status, _, raw := call(t, http.MethodPost, e.URL+tt.path, "application/json; charset=UTF-8", tt.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status %d body %s, want 400", status, raw)
			}
			checkSchema(t, loadContract(t), raw, "Error")
			got := decodeAs[errorBody](t, raw).Error
			if got.Code != codeInvalidField || got.Field != tt.field || got.Message != message {
				t.Errorf("error = %+v, want invalid_field on %s with %q", got, tt.field, message)
			}
			if _, err := e.Store.GetApp(context.Background(), "whoami"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("GetApp = %v, want store.ErrNotFound: nothing may be saved", err)
			}
			if n := e.Nomad.Registrations(); n != 0 {
				t.Errorf("Nomad registrations = %d, want none", n)
			}
		})
	}

	t.Run("an app saved before the setting is refused on its redeploy", func(t *testing.T) {
		e := newTestEnv(t, "")
		deployOK(t, e, "whoami", clash)
		e.Service.SetPublicURL("https://caravana.example.com")
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "", "")
		if status != http.StatusBadRequest {
			t.Fatalf("status %d body %s, want 400", status, raw)
		}
		if got := decodeAs[errorBody](t, raw).Error; got.Code != codeInvalidField || got.Field != "services[0].domains[0].host" || got.Message != message {
			t.Errorf("error = %+v, want invalid_field on the stored domain", got)
		}
	})

	t.Run("no setting, no check", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", clash)
		if status != http.StatusAccepted {
			t.Errorf("status %d body %s, want 202", status, raw)
		}
	})

	t.Run("a subdomain of the console's is fine", func(t *testing.T) {
		e := newTestEnv(t, "")
		e.Service.SetPublicURL("https://caravana.example.com")
		body := strings.Replace(whoamiBody, "whoami.localhost", "app.caravana.example.com", 1)
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", body)
		if status != http.StatusAccepted {
			t.Errorf("status %d body %s, want 202", status, raw)
		}
	})
}
