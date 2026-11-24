package api

import (
	"net/http"
	"strings"
	"testing"
)

// previewBodyWith is a service with no domain and the given secrets, as CI sends it.
func previewBodyWith(secretsJSON string) string {
	return `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest",
		"ports": [{"name": "http", "containerPort": 80}], "secrets": ` + secretsJSON + `,
		"health": {"path": "/", "interval": "10s", "timeout": "2s"}}]}`
}

// A preview is defined by CI's deploy token (feature 054, plan D12): it may only use the secret
// references the base app already has, so a pull request can't read a maintainer's secrets.
func TestScopedTokenPreviewSecretReferences(t *testing.T) {
	e, _ := providersEnv(t)
	shopProject(t, e)
	if status, raw := putProvider(t, e, "bella-prod", providerBody); status != http.StatusCreated {
		t.Fatalf("putSecretProvider = %d %s", status, raw)
	}
	base := `[{"name": "DB", "from": {"provider": "bella-prod", "path": "DATABASE_URL"}}]`
	d := deployOK(t, e, "shop--production--web", previewBodyWith(base))
	waitHealthy(t, e, "shop--production--web", d.ID)
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects/shop/environments", "application/json", `{"name": "pr-1"}`); status != http.StatusCreated {
		t.Fatalf("create pr-1 = %d %s", status, raw)
	}
	auth := scopedToken(t, e, "shop", "web")
	deployAs := func(body string) (int, []byte) {
		status, _, raw := callWith(t, auth, http.MethodPost, e.RawURL+"/v1/apps/shop--pr-1--web/deployments", "application/json", body)
		return status, raw
	}

	// A new from, a new template ref and the same path of another provider are refused.
	for _, c := range []struct{ name, secrets, field string }{
		{"a new from", `[{"name": "DB", "from": {"provider": "bella-prod", "path": "API_KEY"}}]`, "services[0].secrets[0].from"},
		{"another provider", `[{"name": "DB", "from": {"provider": "bella-other", "path": "DATABASE_URL"}}]`, "services[0].secrets[0].from"},
		{"a template ref to a new path", `[{"name": "CONN", "template": "k=${k}", "refs": {"k": {"provider": "bella-prod", "path": "API_KEY"}}}]`,
			"services[0].secrets[0].refs.k"},
	} {
		status, raw := deployAs(previewBodyWith(c.secrets))
		got := wantError(t, c.name, status, raw, http.StatusForbidden, codeNotAllowed, c.field)
		if !strings.Contains(got.Error.Message, "secret references its base app already has") {
			t.Errorf("%s: message = %q, want the preview sentence", c.name, got.Error.Message)
		}
	}
	if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop--pr-1--web", "", ""); status != http.StatusNotFound {
		t.Errorf("the app after the refusals = %d %s, want 404 (nothing was saved)", status, raw)
	}

	// CI's normal deploy: a copy of the base app, and none, are allowed.
	for _, secrets := range []string{base, `[]`} {
		status, raw := deployAs(previewBodyWith(secrets))
		if status != http.StatusAccepted {
			t.Fatalf("deploy of a preview with %s = %d %s, want 202", secrets, status, raw)
		}
		waitHealthy(t, e, "shop--pr-1--web", decodeAs[deployment](t, raw).ID)
	}
}
