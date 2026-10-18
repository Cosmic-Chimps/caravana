package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 020: ?deploy=false creates an app without deploying it, and an app
// with no services refuses to be redeployed as stored.
func TestCreateAppWithoutDeploying(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	url := e.URL + "/v1/apps?deploy=false"

	noJob := func(t *testing.T, id string) {
		t.Helper()
		if recs, _ := e.Store.ListDeployments(context.Background(), id, 0); len(recs) != 0 {
			t.Errorf("%d deployments of %s, want none", len(recs), id)
		}
		if n := e.Nomad.Registrations(); n != 0 {
			t.Errorf("Nomad registrations = %d, want none", n)
		}
	}

	t.Run("a definition: 201 with the app, its ETag and Location", func(t *testing.T) {
		status, header, raw := call(t, http.MethodPost, url, "application/json; charset=UTF-8", withID("whoami"))
		if status != http.StatusCreated {
			t.Fatalf("POST /v1/apps?deploy=false = %d %s, want 201", status, raw)
		}
		checkSchema(t, doc, raw, "AppRecord")
		rec := decodeAs[struct {
			Revision string `json:"revision"`
			Defined  bool   `json:"defined"`
		}](t, raw)
		if !rec.Defined || header.Get("ETag") != `"`+rec.Revision+`"` || header.Get("Location") != "/v1/apps/whoami" {
			t.Errorf("created %s with ETag %q, Location %q; want defined, ETag from the revision, Location the app", raw, header.Get("ETag"), header.Get("Location"))
		}
		noJob(t, "whoami")
	})

	t.Run("a name only: no services, listed, not defined", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, url, "application/json", `{"id": "shop", "name": "Shop"}`)
		if status != http.StatusCreated {
			t.Fatalf("create = %d %s, want 201", status, raw)
		}
		checkSchema(t, doc, raw, "AppRecord")
		if rec := decodeAs[struct {
			Defined bool `json:"defined"`
			App     struct {
				Services []any `json:"services"`
			} `json:"app"`
		}](t, raw); rec.Defined || rec.App.Services == nil || len(rec.App.Services) != 0 {
			t.Errorf("created %s, want defined=false and \"services\": []", raw)
		}
		noJob(t, "shop")

		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop", "", "")
		if status != http.StatusOK || !strings.Contains(string(raw), `"defined":false`) {
			t.Errorf("GET /v1/apps/shop = %d %s, want defined false", status, raw)
		}
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps", "", "")
		checkSchema(t, doc, raw, "AppSummaryList")
		if status != http.StatusOK || !strings.Contains(string(raw), `"id":"shop"`) || !strings.Contains(string(raw), `"defined":false`) {
			t.Errorf("GET /v1/apps = %d %s, want shop listed as not defined", status, raw)
		}
	})

	t.Run("an empty-body deploy, a past-version redeploy and the webhook are 409 app_not_defined", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/deployments", "", "")
		wantError(t, "deployApp, empty body", status, raw, http.StatusConflict, codeAppNotDefined, "")
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/deployments/anything/redeploy", "", "")
		wantError(t, "redeployDeployment", status, raw, http.StatusConflict, codeAppNotDefined, "")

		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/webhook", "", "")
		if status != http.StatusCreated {
			t.Fatalf("create token = %d %s", status, raw)
		}
		tok := decodeAs[struct {
			Token string `json:"token"`
		}](t, raw).Token
		status, _, raw = hook(t, e.URL, "shop", tok, "", "", "")
		wantError(t, "webhook", status, raw, http.StatusConflict, codeAppNotDefined, "")
		noJob(t, "shop")
	})

	t.Run("a deploy with a body defines it and deploys", func(t *testing.T) {
		d := deployOK(t, e, "shop", whoamiBody)
		if d.AppID != "shop" {
			t.Errorf("deployment = %+v, want app shop", d)
		}
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop", "", "")
		if status != http.StatusOK || !strings.Contains(string(raw), `"defined":true`) {
			t.Errorf("GET /v1/apps/shop = %d %s, want defined true", status, raw)
		}
	})

	t.Run("an existing ID is 409 app_exists", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, url, "application/json", `{"id": "shop"}`)
		wantError(t, "taken", status, raw, http.StatusConflict, codeAppExists, "id")
	})

	t.Run("a secret value is 400 invalid_field naming it, and not stored", func(t *testing.T) {
		body := strings.Replace(withID("vault"), `"name": "web"`,
			`"name": "web", "secrets": [{"name": "TOKEN", "value": "s3cret-sentinel-020"}]`, 1)
		status, _, raw := call(t, http.MethodPost, url, "application/json", body)
		wantError(t, "secret value", status, raw, http.StatusBadRequest, codeInvalidField, "services[0].secrets[0].value")
		if strings.Contains(string(raw), "s3cret-sentinel-020") {
			t.Error("the error holds the secret value")
		}
		if _, err := e.Store.GetApp(context.Background(), "vault"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp(vault) = %v, want it not stored", err)
		}
	})

	t.Run("an invalid definition and an invalid ID", func(t *testing.T) {
		bad := strings.Replace(withID("ghost"), `"name": "web"`, `"name": "Web"`, 1)
		status, _, raw := call(t, http.MethodPost, url, "application/json", bad)
		wantError(t, "invalid service", status, raw, http.StatusBadRequest, codeInvalidField, "services[0].name")
		status, _, raw = call(t, http.MethodPost, url, "application/json", `{"id": "Bad_ID"}`)
		wantError(t, "invalid ID", status, raw, http.StatusBadRequest, codeInvalidField, "id")
		status, _, raw = call(t, http.MethodPost, url, "application/json", `{}`)
		wantError(t, "no ID", status, raw, http.StatusBadRequest, codeInvalidField, "id")
	})

	t.Run("deploy=true and no parameter deploy as before; anything else is 400", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps?deploy=true", "application/json", withID("eager"))
		if status != http.StatusAccepted {
			t.Errorf("deploy=true = %d %s, want 202", status, raw)
		}
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps?deploy=maybe", "application/json", withID("typo"))
		wantError(t, "deploy=maybe", status, raw, http.StatusBadRequest, codeInvalidField, "deploy")
		if _, err := e.Store.GetApp(context.Background(), "typo"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp(typo) = %v, want it not stored", err)
		}
		// With deploy the body still needs services.
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps", "application/json", `{"id": "empty", "source": {"kind": "image"}, "services": []}`)
		wantError(t, "no services, deploying", status, raw, http.StatusBadRequest, codeInvalidField, "services")
	})
}

func TestCreateEnvironmentAppWithoutDeploying(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	for _, c := range []struct{ path, body string }{
		{"/v1/projects", `{"name": "shop"}`},
		{"/v1/projects/shop/environments", `{"name": "staging"}`},
	} {
		if status, _, raw := call(t, http.MethodPost, e.URL+c.path, "application/json", c.body); status != http.StatusCreated {
			t.Fatalf("POST %s = %d %s", c.path, status, raw)
		}
	}
	url := e.URL + "/v1/projects/shop/environments/staging/apps?deploy=false"

	status, header, raw := call(t, http.MethodPost, url, "application/json", `{"appName": "web"}`)
	if status != http.StatusCreated {
		t.Fatalf("name only = %d %s, want 201", status, raw)
	}
	checkSchema(t, doc, raw, "AppRecord")
	if header.Get("Location") != "/v1/apps/shop--staging--web" || header.Get("ETag") == "" {
		t.Errorf("Location %q, ETag %q; want the app's URL and a revision", header.Get("Location"), header.Get("ETag"))
	}
	if !strings.Contains(string(raw), `"appName":"web"`) || !strings.Contains(string(raw), `"defined":false`) {
		t.Errorf("created %s, want appName web and defined false", raw)
	}

	status, _, raw = call(t, http.MethodPost, url, "application/json", shopBody("api"))
	if status != http.StatusCreated || !strings.Contains(string(raw), `"defined":true`) {
		t.Errorf("with a definition = %d %s, want 201 defined", status, raw)
	}
	if n := e.Nomad.Registrations(); n != 0 {
		t.Errorf("Nomad registrations = %d, want none", n)
	}

	status, _, raw = call(t, http.MethodPost, url, "application/json", `{"appName": "web"}`)
	wantError(t, "taken", status, raw, http.StatusConflict, codeAppExists, "id")
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/projects/shop/environments/qa/apps?deploy=false", "application/json", `{"appName": "web"}`)
	wantError(t, "unknown environment", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "environment")
	status, _, raw = call(t, http.MethodPost, url, "application/json", `{"appName": "A--b"}`)
	wantError(t, "bad appName", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "appName")
}
