package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// waitUntil polls cond for up to 5 seconds.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// US5: an app reads back exactly as it was sent.
func TestGetApp(t *testing.T) {
	e := newTestEnv(t, "")
	deployOK(t, e, "whoami", whoamiBody)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET app = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")

	var got struct {
		App       map[string]any `json:"app"`
		CreatedAt time.Time      `json:"createdAt"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(whoamiBody), &sent); err != nil {
		t.Fatal(err)
	}
	sent["id"] = "whoami" // taken from the path
	if !reflect.DeepEqual(got.App, sent) {
		t.Errorf("app = %v\nwant %v (durations as the same strings)", got.App, sent)
	}
	if got.CreatedAt.IsZero() {
		t.Error("createdAt missing")
	}

	// The app can be sent back unchanged as a deploy body.
	back, _ := json.Marshal(got.App)
	deployOK(t, e, "whoami", string(back))

	if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/ghost", "", ""); status != http.StatusNotFound {
		t.Errorf("unknown app: %d %s, want 404", status, raw)
	}
}

// appSummary is the part of AppSummary the tests read.
type appSummary struct {
	App       app.App `json:"app"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
	Status    struct {
		State         string  `json:"state"`
		WantedCopies  int     `json:"wantedCopies"`
		HealthyCopies int     `json:"healthyCopies"`
		LiveVersion   *uint64 `json:"liveVersion"`
	} `json:"status"`
	LastDeployment *deployment `json:"lastDeployment"`
}

// seedHealthy stores an app (whoami's shape under id) with one healthy deployment at version.
func seedHealthy(t *testing.T, e *testEnv, id string, replicas int, version uint64) {
	t.Helper()
	ctx := context.Background()
	a := apptest.Whoami().App
	a.ID = id
	a.Services[0].Replicas = &replicas
	if _, err := e.Store.SaveApp(ctx, a); err != nil {
		t.Fatal(err)
	}
	d := apptest.Whoami().Deployment
	d.ID = id + "-d1"
	if _, err := e.Store.CreateDeployment(ctx, id, d, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.UpdateDeployment(ctx, d.ID, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: &version}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.UpdateDeployment(ctx, d.ID, store.DeploymentUpdate{Status: store.StatusHealthy}); err != nil {
		t.Fatal(err)
	}
}

// US1: the Apps page's twin.
func TestListApps(t *testing.T) {
	e := newTestEnv(t, "")
	list := func(t *testing.T) []appSummary {
		t.Helper()
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps", "", "")
		if status != http.StatusOK {
			t.Fatalf("GET /v1/apps = %d %s", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "AppSummaryList")
		return decodeAs[struct {
			Apps []appSummary `json:"apps"`
		}](t, raw).Apps
	}

	t.Run("empty", func(t *testing.T) {
		if got := list(t); got == nil || len(got) != 0 {
			t.Errorf("apps = %#v, want an empty list", got)
		}
	})

	seedHealthy(t, e, "whoami", 2, 4)
	seedHealthy(t, e, "blog", 1, 1)
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{TaskGroup: "web", JobVersion: 4, ClientStatus: "running"},
		api.AllocationListStub{TaskGroup: "web", JobVersion: 4, ClientStatus: "running"})
	// blog has a deployment but no copies in Nomad.

	t.Run("two apps with statuses, by ID", func(t *testing.T) {
		got := list(t)
		if len(got) != 2 || got[0].App.ID != "blog" || got[1].App.ID != "whoami" {
			t.Fatalf("apps = %+v, want blog then whoami", got)
		}
		blog, who := got[0], got[1]
		if blog.Status.State != "not_running" || blog.Status.WantedCopies != 1 || blog.Status.HealthyCopies != 0 || blog.Status.LiveVersion != nil {
			t.Errorf("blog status = %+v, want not_running 0 of 1, no live version", blog.Status)
		}
		if who.Status.State != "healthy" || who.Status.WantedCopies != 2 || who.Status.HealthyCopies != 2 ||
			who.Status.LiveVersion == nil || *who.Status.LiveVersion != 4 {
			t.Errorf("whoami status = %+v, want healthy 2 of 2 at v4", who.Status)
		}
		if who.LastDeployment == nil || who.LastDeployment.ID != "whoami-d1" || who.LastDeployment.Status != "healthy" {
			t.Errorf("whoami lastDeployment = %+v, want whoami-d1 healthy", who.LastDeployment)
		}
		if who.CreatedAt == "" || who.UpdatedAt == "" {
			t.Error("createdAt or updatedAt missing")
		}
	})

	t.Run("an app never deployed has no lastDeployment", func(t *testing.T) {
		a := apptest.Whoami().App
		a.ID = "fresh"
		if _, err := e.Store.SaveApp(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps", "", "")
		checkSchema(t, loadContract(t), raw, "AppSummaryList")
		var generic struct {
			Apps []map[string]any `json:"apps"`
		}
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatal(err)
		}
		for _, a := range generic.Apps {
			if _, has := a["lastDeployment"]; has != (a["app"].(map[string]any)["id"] != "fresh") {
				t.Errorf("app %v: lastDeployment present = %v", a["app"].(map[string]any)["id"], has)
			}
		}
	})

	t.Run("Nomad down: 200 with unknown statuses", func(t *testing.T) {
		e.Nomad.Down()
		defer e.Nomad.Up()
		for _, a := range list(t) {
			if a.Status.State != "unknown" {
				t.Errorf("%s state = %s, want unknown", a.App.ID, a.Status.State)
			}
		}
	})

	t.Run("401 without credentials", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.RawURL+"/v1/apps", "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("GET /v1/apps without credentials = %d %s, want 401", status, raw)
		}
	})
}

// withID returns whoamiBody with an id, as POST /v1/apps needs it.
func withID(id string) string {
	return strings.Replace(whoamiBody, `{`, `{"id": "`+id+`",`, 1)
}

// US3: New app's twin. It never replaces an app.
func TestCreateApp(t *testing.T) {
	e := newTestEnv(t, "")
	url := e.URL + "/v1/apps"

	t.Run("202 with the deployment and its Location", func(t *testing.T) {
		status, header, raw := call(t, http.MethodPost, url, "application/json; charset=UTF-8", withID("whoami"))
		if status != http.StatusAccepted {
			t.Fatalf("POST /v1/apps = %d %s, want 202", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Deployment")
		d := decodeAs[deployment](t, raw)
		if d.AppID != "whoami" || d.Trigger != "manual" || d.JobVersion == nil {
			t.Errorf("deployment = %+v, want whoami, trigger manual, with a job version", d)
		}
		if loc := header.Get("Location"); loc != "/v1/apps/whoami/deployments/"+d.ID {
			t.Errorf("Location = %q, want the deployment's URL", loc)
		}
		if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", ""); status != http.StatusOK {
			t.Errorf("GET the new app = %d %s", status, raw)
		}
	})

	t.Run("an existing ID is 409 app_exists on id, and nothing changes", func(t *testing.T) {
		before, _ := e.Store.GetApp(context.Background(), "whoami")
		deploysBefore := e.Nomad.Registrations()
		other := strings.Replace(withID("whoami"), `"name": "web"`, `"name": "web", "replicas": 3`, 1)
		status, header, raw := call(t, http.MethodPost, url, "application/json", other)
		if status != http.StatusConflict {
			t.Fatalf("POST /v1/apps = %d %s, want 409", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Error")
		if got := decodeAs[errorBody](t, raw).Error; got.Code != codeAppExists || got.Field != "id" || !strings.Contains(got.Message, "whoami") {
			t.Errorf("error = %+v, want app_exists on id naming whoami", got)
		}
		if header.Get("Location") != "" {
			t.Errorf("Location = %q, want none: nothing was recorded", header.Get("Location"))
		}
		after, _ := e.Store.GetApp(context.Background(), "whoami")
		if !reflect.DeepEqual(before, after) || e.Nomad.Registrations() != deploysBefore {
			t.Error("a refused create changed the stored app or registered a job")
		}
		if recs, _ := e.Store.ListDeployments(context.Background(), "whoami", 0); len(recs) != 1 {
			t.Errorf("deployments = %d, want 1", len(recs))
		}
	})

	t.Run("errors", func(t *testing.T) {
		tests := []struct {
			name        string
			body        string
			contentType string
			status      int
			code, field string
		}{
			{name: "no id", body: whoamiBody, status: 400, code: codeInvalidField, field: "id"},
			{name: "invalid app", body: strings.Replace(withID("ghost"), `"name": "web"`, `"name": "Web"`, 1), status: 400, code: codeInvalidField, field: "services[0].name"},
			{name: "unknown field", body: strings.Replace(withID("ghost"), `{`, `{"bogus": 1,`, 1), status: 400, code: codeUnknownField, field: "bogus"},
			{name: "image not found", body: strings.Replace(withID("ghost"), "traefik/whoami:latest", "traefik/whoami:nope", 1), status: 422, code: codeImageNotFound, field: "services[0].image"},
			{name: "text/plain", body: withID("ghost"), contentType: "text/plain", status: 415, code: codeUnsupportedMediaType},
			{name: "empty body", body: " ", status: 400, code: codeBadRequest},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ct := tc.contentType
				if ct == "" {
					ct = "application/json"
				}
				status, _, raw := call(t, http.MethodPost, url, ct, tc.body)
				if status != tc.status {
					t.Fatalf("status = %d, want %d (body %s)", status, tc.status, raw)
				}
				checkSchema(t, loadContract(t), raw, "Error")
				if got := decodeAs[errorBody](t, raw).Error; got.Code != tc.code || got.Field != tc.field {
					t.Errorf("error = %+v, want code %s field %q", got, tc.code, tc.field)
				}
			})
		}
	})

	t.Run("401 without credentials", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.RawURL+"/v1/apps", "application/json", withID("ghost"))
		if status != http.StatusUnauthorized {
			t.Errorf("status = %d %s, want 401", status, raw)
		}
	})

	t.Run("403 across sites with a cookie", func(t *testing.T) {
		c := sessionCookie(t, e)
		status, raw := cookieCall(t, c, http.MethodPost, e.RawURL+"/v1/apps", withID("forged"), map[string]string{"Sec-Fetch-Site": "cross-site"})
		if status != http.StatusForbidden || decodeAs[errorBody](t, raw).Error.Code != codeCrossOrigin {
			t.Errorf("status = %d %s, want 403 cross_origin", status, raw)
		}
		if _, err := e.Store.GetApp(context.Background(), "forged"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a cross-site request created the app (%v)", err)
		}
	})

	t.Run("a cookie request from the same origin creates the app", func(t *testing.T) {
		c := sessionCookie(t, e)
		status, raw := cookieCall(t, c, http.MethodPost, e.RawURL+"/v1/apps", withID("blog"), map[string]string{"Sec-Fetch-Site": "same-origin"})
		if status != http.StatusAccepted {
			t.Errorf("status = %d %s, want 202", status, raw)
		}
	})
}

// US6: Configure > Delete app's twin.
func TestDeleteApp(t *testing.T) {
	e := newTestEnv(t, "")
	ctx := context.Background()
	url := e.URL + "/v1/apps/blog"

	t.Run("204, then the app is gone from Caravana and Nomad", func(t *testing.T) {
		deployOK(t, e, "blog", whoamiBody)
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/blog/webhook", "", ""); status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("create webhook = %d %s", status, raw)
		}

		status, header, raw := call(t, http.MethodDelete, url, "", "")
		if status != http.StatusNoContent || len(raw) != 0 {
			t.Fatalf("DELETE = %d %q, want 204 with no body", status, raw)
		}
		if header.Get("Content-Type") != "" {
			t.Errorf("Content-Type = %q, want none on a 204", header.Get("Content-Type"))
		}
		if status, _, raw := call(t, http.MethodGet, url, "", ""); status != http.StatusNotFound {
			t.Errorf("GET after delete = %d %s, want 404", status, raw)
		}
		if status, _, raw := call(t, http.MethodGet, url+"/deployments", "", ""); status != http.StatusNotFound {
			t.Errorf("GET deployments after delete = %d %s, want 404", status, raw)
		}
		if status, _, raw := call(t, http.MethodGet, url+"/webhook", "", ""); status != http.StatusNotFound {
			t.Errorf("GET webhook after delete = %d %s, want 404", status, raw)
		}
		if !e.Nomad.Purged("blog") || e.Nomad.Job("blog") != nil {
			t.Error("the job is still in Nomad")
		}
	})

	t.Run("an unknown app is 404", func(t *testing.T) {
		status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/apps/ghost", "", "")
		if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeNotFound {
			t.Errorf("DELETE ghost = %d %s, want 404 not_found", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Error")
	})

	t.Run("Nomad down is 502 nomad_unavailable and the app stays", func(t *testing.T) {
		deployOK(t, e, "blog", whoamiBody)
		e.Nomad.Down()
		status, header, raw := call(t, http.MethodDelete, url, "", "")
		e.Nomad.Up()
		if status != http.StatusBadGateway || decodeAs[errorBody](t, raw).Error.Code != codeNomadUnavailable {
			t.Fatalf("DELETE with Nomad down = %d %s, want 502 nomad_unavailable", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Error")
		if loc := header.Get("Location"); loc != "" {
			t.Errorf("Location = %q, want none: no deployment record is involved", loc)
		}
		if _, err := e.Store.GetApp(ctx, "blog"); err != nil {
			t.Errorf("the app is gone after a failed delete: %v", err)
		}
	})

	t.Run("401 without credentials", func(t *testing.T) {
		status, _, _ := call(t, http.MethodDelete, e.RawURL+"/v1/apps/blog", "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", status)
		}
		if _, err := e.Store.GetApp(ctx, "blog"); err != nil {
			t.Errorf("an unauthenticated request deleted the app: %v", err)
		}
	})

	t.Run("403 across sites with a cookie", func(t *testing.T) {
		c := sessionCookie(t, e)
		status, raw := cookieCall(t, c, http.MethodDelete, e.RawURL+"/v1/apps/blog", "", map[string]string{"Sec-Fetch-Site": "cross-site"})
		if status != http.StatusForbidden || decodeAs[errorBody](t, raw).Error.Code != codeCrossOrigin {
			t.Errorf("status = %d %s, want 403 cross_origin", status, raw)
		}
		if _, err := e.Store.GetApp(ctx, "blog"); err != nil {
			t.Errorf("a cross-site request deleted the app: %v", err)
		}
	})

	t.Run("a cookie request from the same origin deletes it", func(t *testing.T) {
		c := sessionCookie(t, e)
		status, raw := cookieCall(t, c, http.MethodDelete, e.RawURL+"/v1/apps/blog", "", map[string]string{"Sec-Fetch-Site": "same-origin"})
		if status != http.StatusNoContent {
			t.Errorf("status = %d %s, want 204", status, raw)
		}
	})
}
