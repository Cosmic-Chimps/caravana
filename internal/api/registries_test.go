package api

import (
	"net/http"
	"strings"
	"testing"
)

// registrySecret is a password no answer, log line or error may contain.
const registrySecret = "s3cr3t-SENTINEL-registry-pw"

func registryBody(user, password string) string {
	if password == "" {
		return `{"username": "` + user + `"}`
	}
	return `{"username": "` + user + `", "password": "` + password + `"}`
}

// US2: the four operations, the 422 field errors, the 404s, and the password
// absent from every answer and from the request log.
func TestRegistryEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	var seen []string // every body the API answered, to search for the secret
	record := func(raw []byte) { seen = append(seen, string(raw)) }

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/registries", "", "")
	if status != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("GET /v1/registries with none = %d %s, want 200 []", status, raw)
	}

	t.Run("422 field errors", func(t *testing.T) {
		for _, tc := range []struct {
			name, host, body, field string
		}{
			{"a Docker Hub user is not a host", "acme", registryBody("u", "p"), "host"},
			{"no password the first time", "ghcr.io", registryBody("u", ""), "password"},
			{"an empty password", "ghcr.io", `{"username": "u", "password": ""}`, "password"},
			{"no user name", "ghcr.io", `{"username": "", "password": "p"}`, "username"},
		} {
			status, _, raw := call(t, http.MethodPut, e.URL+"/v1/registries/"+tc.host, "application/json", tc.body)
			record(raw)
			got := decodeAs[errorBody](t, raw).Error
			if status != http.StatusUnprocessableEntity || got.Code != codeInvalidField || got.Field != tc.field {
				t.Errorf("%s: PUT = %d %s, want 422 invalid_field on %q", tc.name, status, raw, tc.field)
			}
			checkSchema(t, doc, raw, "Error")
		}
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/registries/ghcr.io", "application/json", `{"username": "u", "extra": 1}`)
		if status != http.StatusBadRequest || decodeAs[errorBody](t, raw).Error.Code != codeUnknownField {
			t.Errorf("PUT with an unknown field = %d %s, want 400 unknown_field", status, raw)
		}
		if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/registries", "", ""); strings.TrimSpace(string(raw)) != "[]" {
			t.Errorf("after the refusals GET = %d %s, want none stored", status, raw)
		}
	})

	t.Run("put with a port in the host, then update", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/registries/Registry.Example.COM:5000", "application/json", registryBody("deploy", registrySecret))
		record(raw)
		if status != http.StatusOK {
			t.Fatalf("PUT = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Registry")
		reg := decodeAs[registryJSON](t, raw)
		if reg.Host != "registry.example.com:5000" || reg.Username != "deploy" || reg.LastTest != nil || reg.CreatedAt.IsZero() {
			t.Errorf("PUT answered %+v", reg)
		}

		// A second PUT without a password keeps the secret and changes the user.
		status, _, raw = call(t, http.MethodPut, e.URL+"/v1/registries/registry.example.com:5000", "application/json", registryBody("other", ""))
		record(raw)
		if status != http.StatusOK || decodeAs[registryJSON](t, raw).Username != "other" {
			t.Fatalf("PUT without a password = %d %s", status, raw)
		}
		stored := e.Nomad.Variable("caravana/registries/registry~example~com_5000")
		if stored["username"] != "other" || stored["password"] != registrySecret {
			t.Error("a PUT without a password didn't keep the secret, or didn't change the user")
		}

		// The Docker Hub alias, in any case, is the one entry for docker.io, and
		// the stored credentials are where deploys look for docker.io.
		status, _, raw = call(t, http.MethodPut, e.URL+"/v1/registries/Index.Docker.IO", "application/json", registryBody("me", "tok"))
		record(raw)
		if status != http.StatusOK || decodeAs[registryJSON](t, raw).Host != "docker.io" {
			t.Errorf("PUT Index.Docker.IO = %d %s, want host docker.io", status, raw)
		}
		if stored := e.Nomad.Variable("caravana/registries/docker~io"); stored["username"] != "me" || stored["password"] != "tok" {
			t.Error("the alias didn't store its credentials under docker.io")
		}

		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/registries", "", "")
		record(raw)
		list := decodeAs[[]registryJSON](t, raw)
		if status != http.StatusOK || len(list) != 2 || list[0].Host != "docker.io" || list[1].Host != "registry.example.com:5000" {
			t.Errorf("GET = %d %s, want docker.io then the self-hosted one", status, raw)
		}
	})

	t.Run("test records a refusal and a success", func(t *testing.T) {
		e.Registry.RequireToken(true)
		e.Registry.RequireLogin("deploy", registrySecret)
		host := e.Registry.Host()

		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/registries/"+host, "application/json", registryBody("deploy", "wrong-"+registrySecret))
		record(raw)
		if status != http.StatusOK {
			t.Fatalf("PUT = %d %s", status, raw)
		}
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/registries/"+host+"/test", "", "")
		record(raw)
		reg := decodeAs[registryJSON](t, raw)
		if status != http.StatusOK || reg.LastTest == nil || reg.LastTest.OK || reg.LastTest.Message == "" {
			t.Fatalf("test with a wrong secret = %d %s, want 200 with lastTest.ok false and a message", status, raw)
		}
		checkSchema(t, doc, raw, "Registry")

		call(t, http.MethodPut, e.URL+"/v1/registries/"+host, "application/json", registryBody("deploy", registrySecret))
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/registries/"+host+"/test", "", "")
		record(raw)
		reg = decodeAs[registryJSON](t, raw)
		if status != http.StatusOK || reg.LastTest == nil || !reg.LastTest.OK || reg.LastTest.Message != "" {
			t.Fatalf("test with the right secret = %d %s, want lastTest.ok", status, raw)
		}
		checkSchema(t, doc, raw, "Registry")
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/registries", "", "")
		record(raw)
		if !strings.Contains(string(raw), `"lastTest"`) {
			t.Errorf("GET = %s, want the stored test", raw)
		}
	})

	t.Run("delete and 404", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/registries/docker.io", "", ""); status != http.StatusNoContent || len(raw) != 0 {
			t.Fatalf("DELETE = %d %s, want 204", status, raw)
		}
		for _, tc := range []struct{ method, path string }{
			{http.MethodDelete, "/v1/registries/docker.io"},
			{http.MethodPost, "/v1/registries/docker.io/test"},
			{http.MethodDelete, "/v1/registries/acme"},
			{http.MethodPost, "/v1/registries/ghcr.io/test"},
		} {
			status, _, raw := call(t, tc.method, e.URL+tc.path, "", "")
			record(raw)
			if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeNotFound {
				t.Errorf("%s %s = %d %s, want 404 not_found", tc.method, tc.path, status, raw)
			}
		}
	})

	t.Run("they need a token", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/v1/registries"},
			{http.MethodPut, "/v1/registries/ghcr.io"},
			{http.MethodDelete, "/v1/registries/ghcr.io"},
			{http.MethodPost, "/v1/registries/ghcr.io/test"},
		} {
			status, _, raw := call(t, tc.method, e.RawURL+tc.path, "application/json", registryBody("u", "p"))
			if status != http.StatusUnauthorized {
				t.Errorf("%s %s without a token = %d %s, want 401", tc.method, tc.path, status, raw)
			}
		}
	})

	// FR-002: the secret is in no answer, no log line, and no stored job.
	seen = append(seen, e.Logs.String())
	for i, out := range seen {
		for _, s := range []string{registrySecret, "wrong-" + registrySecret} {
			if strings.Contains(out, s) {
				t.Errorf("output %d (%.80s...) contains %q", i, out, s)
			}
		}
	}
}

// The Registry schema is closed, so a password could not be added to an answer
// without the contract test noticing.
func TestRegistrySchemaHasNoPassword(t *testing.T) {
	doc := loadContract(t)
	body := map[string]any{"host": "ghcr.io", "username": "u", "createdAt": "2026-10-16T10:00:00Z",
		"updatedAt": "2026-10-16T10:00:00Z", "password": "x"}
	if err := doc.Components.Schemas["Registry"].Value.VisitJSON(body); err == nil {
		t.Error("the Registry schema accepted a password property")
	}
	delete(body, "password")
	if err := doc.Components.Schemas["Registry"].Value.VisitJSON(body); err != nil {
		t.Errorf("the Registry schema refused a plain registry: %v", err)
	}
}
