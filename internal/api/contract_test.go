package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// loadContract reads api/openapi.yaml, the contract clients are generated
// from.
func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load api/openapi.yaml: %v", err)
	}
	return doc
}

func TestContractIsValid(t *testing.T) {
	doc := loadContract(t)
	if !doc.IsOpenAPI31OrLater() {
		t.Errorf("openapi version = %q, want 3.1", doc.OpenAPI)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Errorf("api/openapi.yaml is not valid: %v", err)
	}
}

// TestContractMatchesRoutes fails when an endpoint exists in the server but
// not in the contract, or the other way round.
func TestContractMatchesRoutes(t *testing.T) {
	doc := loadContract(t)
	var contract []string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			contract = append(contract, method+" "+path)
		}
	}
	var router []string
	for _, rt := range (&server{}).routes() {
		router = append(router, rt.method+" "+rt.path)
	}
	for _, problem := range diffRoutes(contract, router) {
		t.Error(problem)
	}
}

// diffRoutes lists every "METHOD path" that only one side has.
func diffRoutes(contract, router []string) []string {
	var problems []string
	for _, r := range router {
		if !slices.Contains(contract, r) {
			problems = append(problems, r+" is served but missing from api/openapi.yaml")
		}
	}
	for _, c := range contract {
		if !slices.Contains(router, c) {
			problems = append(problems, c+" is in api/openapi.yaml but not served")
		}
	}
	slices.Sort(problems)
	return problems
}

func TestDiffRoutes(t *testing.T) {
	tests := []struct {
		name             string
		contract, router []string
		want             int
	}{
		{"same", []string{"GET /v1/health"}, []string{"GET /v1/health"}, 0},
		{"missing from contract", []string{"GET /v1/health"}, []string{"GET /v1/health", "POST /v1/apps"}, 1},
		{"missing from router", []string{"GET /v1/health", "POST /v1/apps"}, []string{"GET /v1/health"}, 1},
		{"method differs", []string{"GET /v1/health"}, []string{"POST /v1/health"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diffRoutes(tc.contract, tc.router)
			if len(got) != tc.want {
				t.Errorf("diffRoutes = %v, want %d problems", got, tc.want)
			}
		})
	}
}

// checkSchema fails the test when raw, a JSON body, doesn't match the named
// schema in the contract. Every API test that reads a body can use it
// (FR-005).
func checkSchema(t *testing.T, doc *openapi3.T, raw []byte, schema string) {
	t.Helper()
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	ref := doc.Components.Schemas[schema]
	if ref == nil {
		t.Fatalf("schema %s not in the contract", schema)
	}
	if err := ref.Value.VisitJSON(body); err != nil {
		t.Errorf("body %s does not match schema %s: %v", raw, schema, err)
	}
}

// TestContractBodies checks that what the server really sends matches the
// schemas in the contract.
func TestContractBodies(t *testing.T) {
	doc := loadContract(t)
	url, st, _ := newTestServer(t)

	check := func(t *testing.T, resp *http.Response, schema string) {
		t.Helper()
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		checkSchema(t, doc, raw, schema)
	}
	get := func(t *testing.T, method, path string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, url+path, strings.NewReader(""))
		resp, err := noRedirects.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// Prove the schema check can fail, so the passing cases mean something.
	t.Run("schemas reject wrong bodies", func(t *testing.T) {
		deployment := func(change func(d map[string]any)) map[string]any {
			d := map[string]any{"id": "d1", "appId": "whoami", "trigger": "manual", "status": "running",
				"digests": map[string]any{"web": "traefik/whoami@" + whoamiDigest}, "warnings": []any{},
				"createdAt": "2026-10-02T10:00:00Z", "updatedAt": "2026-10-02T10:00:00Z"}
			change(d)
			return d
		}
		for schema, body := range map[string]any{
			"Health": map[string]any{"status": "fine"},
			"Error":  map[string]any{"error": map[string]any{"code": "x"}}, // no message
			"App": map[string]any{"source": map[string]any{"kind": "image"}, "services": []any{map[string]any{
				"name": "web", "image": "nginx", "health": map[string]any{"interval": 10.0}}}}, // numeric duration
			"Deployment":     deployment(func(d map[string]any) { d["status"] = "done" }),
			"DeploymentList": map[string]any{"deployments": []any{deployment(func(d map[string]any) { d["digests"] = map[string]any{"web": "nginx:latest"} })}},
			"AppRecord":      map[string]any{"app": map[string]any{}, "createdAt": "x"},
			"WebhookToken": map[string]any{"token": "ghp_" + strings.Repeat("a", 44), "path": "/v1/webhooks/whoami",
				"createdAt": "2026-10-02T10:00:00Z"}, // not a cvwh_ token
			"WebhookResult": map[string]any{"changed": true}, // no deployment
			"WebhookStatus": map[string]any{"enabled": true, "token": "cvwh_x"},
		} {
			if err := doc.Components.Schemas[schema].Value.VisitJSON(body); err == nil {
				t.Errorf("schema %s accepted %v", schema, body)
			}
		}
	})
	t.Run("health 200", func(t *testing.T) { check(t, get(t, "GET", "/v1/health"), "Health") })
	t.Run("404", func(t *testing.T) { check(t, get(t, "GET", "/v1/nope"), "Error") })
	t.Run("405", func(t *testing.T) { check(t, get(t, "DELETE", "/v1/health"), "Error") })
	t.Run("unclean path", func(t *testing.T) { check(t, get(t, "GET", "//v1/health"), "Error") })
	t.Run("deploy 202, get deployment, list, get app", func(t *testing.T) {
		e := newTestEnv(t, "")
		_, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", whoamiBody)
		checkSchema(t, doc, raw, "Deployment")
		_, _, raw = call(t, http.MethodGet, e.URL+header.Get("Location"), "", "")
		checkSchema(t, doc, raw, "Deployment")
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments", "", "")
		checkSchema(t, doc, raw, "DeploymentList")
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
		checkSchema(t, doc, raw, "AppRecord")
	})
	t.Run("webhook token, status, result and 401", func(t *testing.T) {
		e := newTestEnv(t, "")
		tok := withWebhook(t, e) // checks WebhookToken
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/webhook", "", "")
		checkSchema(t, doc, raw, "WebhookStatus")
		_, _, raw = hook(t, e.URL, "whoami", tok, "", "", "")
		checkSchema(t, doc, raw, "WebhookResult")
		_, _, raw = hook(t, e.URL, "whoami", "", "", "", "")
		checkSchema(t, doc, raw, "Error")
	})
	t.Run("deploy errors", func(t *testing.T) {
		e := newTestEnv(t, "")
		for _, body := range []string{withImage("traefik/whoami:nope"), withImage("quay.io/x/y:1"), "{}"} {
			_, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", body)
			checkSchema(t, doc, raw, "Error")
		}
	})
	t.Run("health 503", func(t *testing.T) {
		st.Close()
		check(t, get(t, "GET", "/v1/health"), "Error")
	})
}
