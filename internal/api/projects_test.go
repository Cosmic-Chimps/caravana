package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// shopBody is an app body for the project routes: no domain, so the same app
// can sit in two environments.
func shopBody(appName string) string {
	return `{
  "appName": "` + appName + `",
  "source": {"kind": "image"},
  "services": [{"name": "web", "image": "traefik/whoami:latest"}]
}`
}

// checkEach checks every element of a JSON array against a schema.
func checkEach(t *testing.T, doc *openapi3.T, raw []byte, schema string) {
	t.Helper()
	for _, item := range decodeAs[[]json.RawMessage](t, raw) {
		checkSchema(t, doc, item, schema)
	}
}

// projectView is a Project answer, decoded.
type projectView struct {
	Name         string `json:"name"`
	DisplayName  string `json:"displayName"`
	Environments []struct {
		Project  string `json:"project"`
		Name     string `json:"name"`
		Position int    `json:"position"`
		Apps     int    `json:"apps"`
	} `json:"environments"`
}

func (p projectView) envNames() []string {
	var out []string
	for _, e := range p.Environments {
		out = append(out, e.Name)
	}
	return out
}

// wantError checks an error answer's status, code and field, and its schema.
func wantError(t *testing.T, what string, status int, raw []byte, wantStatus int, wantCode, wantField string) errorBody {
	t.Helper()
	got := decodeAs[errorBody](t, raw)
	if status != wantStatus || got.Error.Code != wantCode || got.Error.Field != wantField {
		t.Errorf("%s = %d %s, want %d %s on field %q", what, status, raw, wantStatus, wantCode, wantField)
	}
	checkSchema(t, loadContract(t), raw, "Error")
	return got
}

func TestProjectEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	url := e.URL

	t.Run("a new installation has Default with production", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, url+"/v1/projects", "", "")
		if status != http.StatusOK {
			t.Fatalf("GET /v1/projects = %d %s", status, raw)
		}
		list := decodeAs[[]projectView](t, raw)
		if len(list) != 1 || list[0].Name != "default" || list[0].DisplayName != "Default" || !slices.Equal(list[0].envNames(), []string{"production"}) {
			t.Fatalf("projects = %s, want Default with production", raw)
		}
		checkEach(t, doc, raw, "Project")
	})

	t.Run("create answers 201 with Location and production", func(t *testing.T) {
		status, header, raw := call(t, http.MethodPost, url+"/v1/projects", "application/json", `{"name": "shop", "displayName": "The Shop"}`)
		if status != http.StatusCreated {
			t.Fatalf("POST /v1/projects = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Project")
		p := decodeAs[projectView](t, raw)
		if p.Name != "shop" || p.DisplayName != "The Shop" || !slices.Equal(p.envNames(), []string{"production"}) || p.Environments[0].Project != "shop" {
			t.Errorf("created = %s", raw)
		}
		if loc := header.Get("Location"); loc != "/v1/projects/shop" {
			t.Errorf("Location = %q, want /v1/projects/shop", loc)
		}
		// The display name defaults to the name.
		status, _, raw = call(t, http.MethodPost, url+"/v1/projects", "application/json", `{"name": "blog"}`)
		if status != http.StatusCreated || decodeAs[projectView](t, raw).DisplayName != "blog" {
			t.Errorf("POST without a display name = %d %s", status, raw)
		}
	})

	t.Run("create refusals", func(t *testing.T) {
		for _, tc := range []struct {
			name, body, field string
		}{
			{"uppercase", `{"name": "Shop"}`, "name"},
			{"double dash", `{"name": "a--b"}`, "name"},
			{"underscore", `{"name": "my_shop"}`, "name"},
			{"empty", `{"name": ""}`, "name"},
			{"too long", `{"name": "` + strings.Repeat("a", 64) + `"}`, "name"},
			{"blank display name", `{"name": "ok", "displayName": "   "}`, "displayName"},
			{"long display name", `{"name": "ok", "displayName": "` + strings.Repeat("x", 101) + `"}`, "displayName"},
		} {
			status, _, raw := call(t, http.MethodPost, url+"/v1/projects", "application/json", tc.body)
			wantError(t, tc.name, status, raw, http.StatusUnprocessableEntity, codeInvalidField, tc.field)
		}
		status, _, raw := call(t, http.MethodPost, url+"/v1/projects", "application/json", `{"name": "shop"}`)
		wantError(t, "duplicate", status, raw, http.StatusConflict, codeProjectExists, "name")
		status, _, raw = call(t, http.MethodPost, url+"/v1/projects", "application/json", `{"name": "default"}`)
		wantError(t, "default again", status, raw, http.StatusConflict, codeProjectExists, "name")
		status, _, raw = call(t, http.MethodPost, url+"/v1/projects", "application/json", `{"name": "x", "colour": "red"}`)
		wantError(t, "unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "colour")
		status, _, raw = call(t, http.MethodPost, url+"/v1/projects", "text/plain", `{"name": "x"}`)
		wantError(t, "wrong media type", status, raw, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "")
	})

	t.Run("get, rename", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, url+"/v1/projects/shop", "", "")
		if status != http.StatusOK || decodeAs[projectView](t, raw).Name != "shop" {
			t.Fatalf("GET = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Project")
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/nope", "", "")
		wantError(t, "GET unknown", status, raw, http.StatusNotFound, codeProjectNotFound, "project")

		status, _, raw = call(t, http.MethodPatch, url+"/v1/projects/shop", "application/json", `{"displayName": "Shop (EU)"}`)
		if status != http.StatusOK || decodeAs[projectView](t, raw).DisplayName != "Shop (EU)" {
			t.Errorf("PATCH = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Project")
		status, _, raw = call(t, http.MethodPatch, url+"/v1/projects/default", "application/json", `{"displayName": "Everything"}`)
		if status != http.StatusOK {
			t.Errorf("renaming Default = %d %s, want 200", status, raw)
		}
		status, _, raw = call(t, http.MethodPatch, url+"/v1/projects/shop", "application/json", `{}`)
		wantError(t, "PATCH without displayName", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "displayName")
		status, _, raw = call(t, http.MethodPatch, url+"/v1/projects/shop", "application/json", `{"displayName": ""}`)
		wantError(t, "PATCH with an empty displayName", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "displayName")
		status, _, raw = call(t, http.MethodPatch, url+"/v1/projects/nope", "application/json", `{"displayName": "x"}`)
		wantError(t, "PATCH unknown", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
		status, _, raw = call(t, http.MethodPatch, url+"/v1/projects/shop", "application/json", `{"name": "other"}`)
		wantError(t, "PATCH the name", status, raw, http.StatusBadRequest, codeUnknownField, "name")
	})

	t.Run("environments", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, url+"/v1/projects/shop/environments", "application/json", `{"name": "staging"}`)
		if status != http.StatusCreated {
			t.Fatalf("POST environment = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Environment")
		env := decodeAs[struct {
			Project  string `json:"project"`
			Name     string `json:"name"`
			Position int    `json:"position"`
			Apps     int    `json:"apps"`
		}](t, raw)
		if env.Project != "shop" || env.Name != "staging" || env.Position != 1 || env.Apps != 0 {
			t.Errorf("created environment = %s", raw)
		}
		status, _, raw = call(t, http.MethodPost, url+"/v1/projects/shop/environments", "application/json", `{"name": "staging"}`)
		wantError(t, "duplicate", status, raw, http.StatusConflict, codeEnvironmentExists, "name")
		status, _, raw = call(t, http.MethodPost, url+"/v1/projects/shop/environments", "application/json", `{"name": "pre--prod"}`)
		wantError(t, "double dash", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "name")
		status, _, raw = call(t, http.MethodPost, url+"/v1/projects/nope/environments", "application/json", `{"name": "staging"}`)
		wantError(t, "unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "project")

		_, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop", "", "")
		if p := decodeAs[projectView](t, raw); !slices.Equal(p.envNames(), []string{"production", "staging"}) {
			t.Errorf("environments = %v, want production then staging", p.envNames())
		}

		status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/shop/environments/staging", "", "")
		if status != http.StatusNoContent || len(raw) != 0 {
			t.Errorf("DELETE environment = %d %s, want 204", status, raw)
		}
		status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/shop/environments/staging", "", "")
		wantError(t, "DELETE again", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "environment")
		status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/nope/environments/staging", "", "")
		wantError(t, "DELETE in an unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
	})

	t.Run("protected", func(t *testing.T) {
		status, _, raw := call(t, http.MethodDelete, url+"/v1/projects/default", "", "")
		wantError(t, "DELETE default", status, raw, http.StatusConflict, codeProtected, "")
		status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/default/environments/production", "", "")
		wantError(t, "DELETE default/production", status, raw, http.StatusConflict, codeProtected, "")
	})

	t.Run("delete a project", func(t *testing.T) {
		status, _, raw := call(t, http.MethodDelete, url+"/v1/projects/blog", "", "")
		if status != http.StatusNoContent {
			t.Errorf("DELETE = %d %s, want 204", status, raw)
		}
		status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/blog", "", "")
		wantError(t, "DELETE again", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
	})
}

// appView is the place part of an AppRecord or AppSummary answer.
type appView struct {
	App struct {
		ID string `json:"id"`
	} `json:"app"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	AppName     string `json:"appName"`
	Revision    string `json:"revision"`
}

func TestEnvironmentApps(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	url := e.URL
	for _, body := range []string{`{"name": "shop"}`} {
		if status, _, raw := call(t, http.MethodPost, url+"/v1/projects", "application/json", body); status != http.StatusCreated {
			t.Fatalf("create project = %d %s", status, raw)
		}
	}
	if status, _, raw := call(t, http.MethodPost, url+"/v1/projects/shop/environments", "application/json", `{"name": "staging"}`); status != http.StatusCreated {
		t.Fatalf("create environment = %d %s", status, raw)
	}

	create := func(project, env, body string) (int, http.Header, []byte) {
		return call(t, http.MethodPost, url+"/v1/projects/"+project+"/environments/"+env+"/apps", "application/json", body)
	}

	t.Run("create in staging and production: the same name side by side", func(t *testing.T) {
		for _, env := range []string{"staging", "production"} {
			id := "shop--" + env + "--web"
			status, header, raw := create("shop", env, shopBody("web"))
			if status != http.StatusAccepted {
				t.Fatalf("create in %s = %d %s", env, status, raw)
			}
			checkSchema(t, doc, raw, "Deployment")
			d := decodeAs[deployment](t, raw)
			if d.AppID != id || header.Get("Location") != "/v1/apps/"+id+"/deployments/"+d.ID {
				t.Errorf("created %+v with Location %q, want app %s", d, header.Get("Location"), id)
			}
		}
		// Default's production takes the bare name.
		status, _, raw := create("default", "production", shopBody("blog"))
		if status != http.StatusAccepted || decodeAs[deployment](t, raw).AppID != "blog" {
			t.Errorf("create in default/production = %d %s, want app blog", status, raw)
		}
	})

	t.Run("create refusals", func(t *testing.T) {
		status, _, raw := create("shop", "staging", shopBody("web"))
		wantError(t, "taken", status, raw, http.StatusConflict, codeAppExists, "id")
		status, _, raw = create("shop", "qa", shopBody("web"))
		wantError(t, "unknown environment", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "environment")
		status, _, raw = create("nope", "staging", shopBody("web"))
		wantError(t, "unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
		status, _, raw = create("shop", "staging", shopBody("a--b"))
		wantError(t, "double dash in the name", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "appName")
		status, _, raw = create("shop", "staging", shopBody("Web"))
		wantError(t, "uppercase name", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "appName")
		status, _, raw = create("shop", "staging", `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest"}]}`)
		wantError(t, "no appName", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "appName")

		// id is optional; given, it must be the computed one.
		status, _, raw = create("shop", "staging", strings.Replace(shopBody("api"), `"appName"`, `"id": "shop--production--api", "appName"`, 1))
		wantError(t, "id of another place", status, raw, http.StatusBadRequest, codeInvalidField, "id")
		status, _, raw = create("shop", "staging", strings.Replace(shopBody("api"), `"appName"`, `"id": "shop--staging--api", "appName"`, 1))
		if status != http.StatusAccepted {
			t.Errorf("create with the matching id = %d %s", status, raw)
		}
		status, _, raw = create("shop", "staging", strings.Replace(shopBody("api2"), `"appName"`, `"colour": 1, "appName"`, 1))
		wantError(t, "unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "colour")
		// A body error of the app itself stays a 400, as for createApp.
		status, _, raw = create("shop", "staging", strings.Replace(shopBody("api3"), "traefik/whoami:latest", "", 1))
		wantError(t, "app field error", status, raw, http.StatusBadRequest, codeInvalidField, "services[0].image")
	})

	t.Run("reads carry the place", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, url+"/v1/apps/shop--staging--web", "", "")
		if status != http.StatusOK {
			t.Fatalf("GET app = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "AppRecord")
		if v := decodeAs[appView](t, raw); v.Project != "shop" || v.Environment != "staging" || v.AppName != "web" || v.App.ID != "shop--staging--web" {
			t.Errorf("AppRecord = %s", raw)
		}
		_, _, raw = call(t, http.MethodGet, url+"/v1/apps/blog", "", "")
		if v := decodeAs[appView](t, raw); v.Project != "default" || v.Environment != "production" || v.AppName != "blog" {
			t.Errorf("blog's place = %s", raw)
		}

		// By names, with the ETag.
		status, header, raw := call(t, http.MethodGet, url+"/v1/projects/shop/environments/staging/apps/web", "", "")
		if status != http.StatusOK {
			t.Fatalf("GET by names = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "AppRecord")
		v := decodeAs[appView](t, raw)
		if v.App.ID != "shop--staging--web" || header.Get("ETag") != `"`+v.Revision+`"` {
			t.Errorf("by names = %s with ETag %q", raw, header.Get("ETag"))
		}
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/default/environments/production/apps/blog", "", "")
		if status != http.StatusOK || decodeAs[appView](t, raw).App.ID != "blog" {
			t.Errorf("GET default/production/blog = %d %s", status, raw)
		}
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop/environments/staging/apps/nope", "", "")
		wantError(t, "unknown app", status, raw, http.StatusNotFound, codeNotFound, "")
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop/environments/qa/apps/web", "", "")
		wantError(t, "unknown environment", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "environment")
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/nope/environments/staging/apps/web", "", "")
		wantError(t, "unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop/environments/staging/apps/a--b", "", "")
		wantError(t, "bad name", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "name")
	})

	t.Run("lists", func(t *testing.T) {
		ids := func(raw []byte) []string {
			var list []appView
			if raw[0] == '[' {
				list = decodeAs[[]appView](t, raw)
			} else {
				list = decodeAs[struct {
					Apps []appView `json:"apps"`
				}](t, raw).Apps
			}
			out := []string{}
			for _, a := range list {
				out = append(out, a.App.ID)
			}
			return out
		}

		status, _, raw := call(t, http.MethodGet, url+"/v1/projects/shop/environments/staging/apps", "", "")
		if status != http.StatusOK {
			t.Fatalf("listEnvironmentApps = %d %s", status, raw)
		}
		if got := ids(raw); !slices.Equal(got, []string{"shop--staging--api", "shop--staging--web"}) {
			t.Errorf("shop/staging = %v", got)
		}
		checkEach(t, doc, raw, "AppSummary")
		for _, item := range decodeAs[[]map[string]any](t, raw) {
			if item["project"] != "shop" || item["environment"] != "staging" || item["appName"] == nil {
				t.Errorf("item lacks the place: %v", item)
			}
		}
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop/environments/production/apps", "", "")
		if got := ids(raw); status != http.StatusOK || !slices.Equal(got, []string{"shop--production--web"}) {
			t.Errorf("shop/production = %d %v", status, got)
		}
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/default/environments/production/apps", "", "")
		if got := ids(raw); status != http.StatusOK || !slices.Equal(got, []string{"blog"}) {
			t.Errorf("default/production = %d %v", status, got)
		}
		status, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop/environments/qa/apps", "", "")
		wantError(t, "unknown environment", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "environment")

		// listApps and its filters.
		for _, tc := range []struct {
			query string
			want  []string
		}{
			{"", []string{"blog", "shop--production--web", "shop--staging--api", "shop--staging--web"}},
			{"?project=shop", []string{"shop--production--web", "shop--staging--api", "shop--staging--web"}},
			{"?project=shop&environment=staging", []string{"shop--staging--api", "shop--staging--web"}},
			{"?environment=production", []string{"blog", "shop--production--web"}},
			{"?project=default", []string{"blog"}},
			{"?project=&environment=", []string{"blog", "shop--production--web", "shop--staging--api", "shop--staging--web"}},
		} {
			status, _, raw := call(t, http.MethodGet, url+"/v1/apps"+tc.query, "", "")
			if status != http.StatusOK {
				t.Errorf("GET /v1/apps%s = %d %s", tc.query, status, raw)
				continue
			}
			checkSchema(t, doc, raw, "AppSummaryList")
			if got := ids(raw); !slices.Equal(got, tc.want) {
				t.Errorf("GET /v1/apps%s = %v, want %v", tc.query, got, tc.want)
			}
		}
		status, _, raw = call(t, http.MethodGet, url+"/v1/apps?project=nope", "", "")
		wantError(t, "listApps unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
		status, _, raw = call(t, http.MethodGet, url+"/v1/apps?project=shop&environment=qa", "", "")
		wantError(t, "listApps unknown environment", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "environment")
	})

	t.Run("delete is refused while apps remain, naming them", func(t *testing.T) {
		status, _, raw := call(t, http.MethodDelete, url+"/v1/projects/shop/environments/staging", "", "")
		got := wantError(t, "DELETE environment", status, raw, http.StatusConflict, codeNotEmpty, "")
		for _, id := range []string{"shop--staging--web", "shop--staging--api"} {
			if !strings.Contains(got.Error.Message, id) {
				t.Errorf("message %q does not name %s", got.Error.Message, id)
			}
		}
		status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/shop", "", "")
		got = wantError(t, "DELETE project", status, raw, http.StatusConflict, codeNotEmpty, "")
		if !strings.Contains(got.Error.Message, "shop--production--web") {
			t.Errorf("message %q does not name the production app", got.Error.Message)
		}
		// Nothing was deleted.
		_, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop", "", "")
		if p := decodeAs[projectView](t, raw); !slices.Equal(p.envNames(), []string{"production", "staging"}) || p.Environments[1].Apps != 2 {
			t.Errorf("project after the refusals = %s", raw)
		}
	})
}

// Research R7: the old routes place an app by its ID too.
func TestOldRoutesPlaceByID(t *testing.T) {
	e := newTestEnv(t, "")
	url := e.URL
	if status, _, raw := call(t, http.MethodPost, url+"/v1/projects", "application/json", `{"name": "shop"}`); status != http.StatusCreated {
		t.Fatalf("create project = %d %s", status, raw)
	}
	body := strings.Replace(shopBody("x"), `"appName": "x",`, "", 1)

	t.Run("deployApp of an unknown environment names the id", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, url+"/v1/apps/shop--staging--web/deployments", "application/json", body)
		wantError(t, "deployApp", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "id")
		status, _, raw = call(t, http.MethodPost, url+"/v1/apps/nope--production--web/deployments", "application/json", body)
		wantError(t, "deployApp in an unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "id")
		if status, _, raw := call(t, http.MethodGet, url+"/v1/apps/shop--staging--web", "", ""); status != http.StatusNotFound {
			t.Errorf("the refused app exists: %d %s", status, raw)
		}
		if recent := e.Nomad.Registrations(); recent != 0 {
			t.Errorf("Nomad registered %d job(s) for a refused deploy", recent)
		}
	})

	t.Run("createApp and deployCompose too", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, url+"/v1/apps", "application/json",
			strings.Replace(body, `"source"`, `"id": "shop--qa--web", "source"`, 1))
		wantError(t, "createApp", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "id")
		status, _, raw = call(t, http.MethodPost, url+"/v1/apps/shop--qa--web/compose", "application/json",
			`{"compose": "services:\n  web:\n    image: traefik/whoami:latest\n"}`)
		wantError(t, "deployCompose", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "id")
	})

	t.Run("an ID that isn't a place is a field error on id", func(t *testing.T) {
		for _, id := range []string{"a--b", "a--b--c--d", "default--production--web"} {
			status, _, raw := call(t, http.MethodPost, url+"/v1/apps/"+id+"/deployments", "application/json", body)
			wantError(t, "deployApp "+id, status, raw, http.StatusBadRequest, codeInvalidField, "id")
		}
	})

	t.Run("an environment that exists takes the first deploy", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodPost, url+"/v1/projects/shop/environments", "application/json", `{"name": "staging"}`); status != http.StatusCreated {
			t.Fatalf("create environment = %d %s", status, raw)
		}
		d := deployOK(t, e, "shop--staging--web", body)
		if d.AppID != "shop--staging--web" {
			t.Errorf("deployment of %q", d.AppID)
		}
		_, _, raw := call(t, http.MethodGet, url+"/v1/apps/shop--staging--web", "", "")
		if v := decodeAs[appView](t, raw); v.Project != "shop" || v.Environment != "staging" || v.AppName != "web" {
			t.Errorf("place = %s", raw)
		}
	})
}

func TestProjectRoutesNeedAuthentication(t *testing.T) {
	e := newTestEnv(t, "")
	for _, rt := range (&server{}).routes() {
		if !strings.HasPrefix(rt.path, "/v1/projects") {
			continue
		}
		path := rt.path
		for _, p := range []string{"{project}", "{environment}", "{name}"} {
			path = strings.ReplaceAll(path, p, "x")
		}
		status, _, raw := call(t, rt.method, e.RawURL+path, "application/json", `{}`)
		wantError(t, rt.method+" "+path+" without a token", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
	}
}
