package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// whoamiBody is the whoami app as a client sends it, without an id.
const whoamiBody = `{
  "source": {"kind": "image"},
  "services": [{
    "name": "web",
    "image": "traefik/whoami:latest",
    "ports": [{"name": "http", "containerPort": 80}],
    "domains": [{"host": "whoami.localhost", "port": "http"}],
    "health": {"path": "/", "interval": "10s", "timeout": "2s"}
  }]
}`

// withImage returns whoamiBody with another image.
func withImage(image string) string {
	return strings.Replace(whoamiBody, "traefik/whoami:latest", image, 1)
}

// call sends a request and returns the status, headers and raw body.
func call(t *testing.T, method, url, contentType, body string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

// deployment is a Deployment response, decoded.
type deployment struct {
	ID         string            `json:"id"`
	AppID      string            `json:"appId"`
	Trigger    string            `json:"trigger"`
	Status     string            `json:"status"`
	JobVersion *uint64           `json:"jobVersion"`
	Digests    map[string]string `json:"digests"`
	Warnings   []app.Warning     `json:"warnings"`
	Error      string            `json:"error"`
}

func decodeAs[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// deployOK posts body for appID and checks the 202.
func deployOK(t *testing.T, e *testEnv, appID, body string) deployment {
	t.Helper()
	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/"+appID+"/deployments", "application/json; charset=UTF-8", body)
	if status != http.StatusAccepted {
		t.Fatalf("deploy %s: status %d, body %s", appID, status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Deployment")
	d := decodeAs[deployment](t, raw)
	if loc := header.Get("Location"); loc != "/v1/apps/"+appID+"/deployments/"+d.ID {
		t.Errorf("Location = %q, want the deployment's URL", loc)
	}
	return d
}

// US1
func TestDeployEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployOK(t, e, "whoami", whoamiBody)
	if d.AppID != "whoami" || d.Trigger != "manual" || d.JobVersion == nil || *d.JobVersion != 0 {
		t.Errorf("deployment = %+v, want app whoami, trigger manual, version 0", d)
	}
	if d.Digests["web"] != apptest.WhoamiDigest || d.Warnings == nil || len(d.Warnings) != 0 {
		t.Errorf("digests %v, warnings %#v; want the pinned image and no warnings", d.Digests, d.Warnings)
	}

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+d.ID, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET deployment: %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Deployment")

	for name, path := range map[string]string{
		"unknown deployment":       "/v1/apps/whoami/deployments/nope",
		"another app's deployment": "/v1/apps/other/deployments/" + d.ID,
	} {
		t.Run(name, func(t *testing.T) {
			status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
			if status != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (body %s)", status, raw)
			}
			checkSchema(t, loadContract(t), raw, "Error")
		})
	}

	t.Run("tag and digest warns", func(t *testing.T) {
		d := deployOK(t, e, "whoami", withImage("traefik/whoami:v1@"+whoamiDigest))
		if len(d.Warnings) != 1 || d.Warnings[0].Field != "services[0].image" {
			t.Errorf("warnings = %+v, want one on services[0].image", d.Warnings)
		}
	})
}

// US2
func TestRedeployEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	url := e.URL + "/v1/apps/whoami/deployments"

	t.Run("empty body on an unknown app", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, url, "", "")
		if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeNotFound {
			t.Errorf("status %d body %s, want 404 not_found", status, raw)
		}
	})

	first := deployOK(t, e, "whoami", whoamiBody)
	t.Run("empty body redeploys the stored app", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, url, "", "")
		if status != http.StatusAccepted {
			t.Fatalf("status %d body %s, want 202", status, raw)
		}
		d := decodeAs[deployment](t, raw)
		if d.ID == first.ID || d.Trigger != "manual" || *d.JobVersion != *first.JobVersion+1 {
			t.Errorf("redeploy = %+v, want a new manual deployment one version up", d)
		}
	})

	t.Run("an empty object is a body", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, url, "application/json", "{}")
		if status != http.StatusBadRequest || decodeAs[errorBody](t, raw).Error.Code != codeInvalidField {
			t.Errorf("status %d body %s, want 400 invalid_field", status, raw)
		}
	})
}

// US2: a deploy replaced before it reached Nomad answers 202 superseded.
func TestSupersededBeforeRegistration(t *testing.T) {
	e := newTestEnv(t, "")
	release := e.Registry.Hold("traefik/whoami", "latest")
	url := e.URL + "/v1/apps/whoami/deployments"

	first := make(chan []byte, 1)
	go func() {
		_, _, raw := call(t, http.MethodPost, url, "application/json", whoamiBody)
		first <- raw
	}()
	waitUntil(t, func() bool { return e.Registry.Requests() >= 1 })
	second := make(chan []byte, 1)
	go func() {
		_, _, raw := call(t, http.MethodPost, url, "application/json", whoamiBody)
		second <- raw
	}()
	waitUntil(t, func() bool {
		list, _ := e.Store.ListDeployments(context.Background(), "whoami", 0)
		return len(list) == 2
	})
	// The second request marks itself the app's latest just after creating
	// its record; the API can't observe that, so give it a moment. (The
	// service tests check the same rule without the wait.)
	time.Sleep(100 * time.Millisecond)
	release()

	a, b := decodeAs[deployment](t, <-first), decodeAs[deployment](t, <-second)
	if a.Status != "superseded" || a.JobVersion != nil {
		t.Errorf("first = %s v%v, want superseded without a job version", a.Status, a.JobVersion)
	}
	if b.Status != "running" || b.JobVersion == nil {
		t.Errorf("second = %s v%v, want running with a job version", b.Status, b.JobVersion)
	}
}

// US4: every failure has the right status, code, field and Location.
func TestDeployErrors(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(e *testEnv)
		body         string
		contentType  string
		wantStatus   int
		wantCode     string
		wantField    string
		wantMsg      string
		wantLocation bool
	}{
		{name: "image not found", body: withImage("traefik/whoami:nope"),
			wantStatus: 422, wantCode: codeImageNotFound, wantField: "services[0].image", wantMsg: "or private", wantLocation: true},
		{name: "foreign job", setup: func(e *testEnv) {
			e.Nomad.AddForeignJob(&api.Job{ID: new("whoami"), Name: new("whoami"),
				Meta: map[string]string{"caravana.deployment": "lab-manual"}})
		}, wantStatus: 409, wantCode: codeJobConflict, wantMsg: `"whoami"`, wantLocation: true},
		{name: "registry unavailable", setup: func(e *testEnv) { e.Registry.Fail("traefik/whoami", "latest", 429) },
			wantStatus: 502, wantCode: codeRegistryUnavailable, wantField: "services[0].image", wantMsg: "rate-limiting", wantLocation: true},
		{name: "nomad unavailable", setup: func(e *testEnv) { e.Nomad.Down() },
			wantStatus: 502, wantCode: codeNomadUnavailable, wantMsg: "unreachable", wantLocation: true},
		{name: "nomad rejects", setup: func(e *testEnv) { e.Nomad.RejectNext(400, "group web: bad driver") },
			wantStatus: 502, wantCode: codeNomadRejected, wantMsg: "group web: bad driver", wantLocation: true},
		{name: "unsupported registry", body: withImage("quay.io/x/y:1"),
			wantStatus: 400, wantCode: codeInvalidField, wantField: "services[0].image", wantMsg: "Docker Hub and GHCR only"},
		{name: "numeric duration", body: strings.Replace(whoamiBody, `"interval": "10s"`, `"interval": 10`, 1),
			wantStatus: 400, wantCode: codeInvalidField, wantField: "services[0].health.interval", wantMsg: "duration string"},
		{name: "id differs from the path", body: strings.Replace(whoamiBody, `{`, `{"id": "other",`, 1),
			wantStatus: 400, wantCode: codeInvalidField, wantField: "id"},
		{name: "invalid app", body: strings.Replace(whoamiBody, `"name": "web"`, `"name": "Web"`, 1),
			wantStatus: 400, wantCode: codeInvalidField, wantField: "services[0].name"},
		{name: "unknown field", body: strings.Replace(whoamiBody, `{`, `{"bogus": 1,`, 1),
			wantStatus: 400, wantCode: codeUnknownField, wantField: "bogus"},
		{name: "text/plain", contentType: "text/plain", body: whoamiBody, wantStatus: 415, wantCode: codeUnsupportedMediaType},
		{name: "too large", body: `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, wantStatus: 413, wantCode: codeBodyTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, "")
			if tc.setup != nil {
				tc.setup(e)
			}
			body, ct := tc.body, tc.contentType
			if body == "" {
				body = whoamiBody
			}
			if ct == "" {
				ct = "application/json"
			}
			status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", ct, body)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", status, tc.wantStatus, raw)
			}
			checkSchema(t, loadContract(t), raw, "Error")
			got := decodeAs[errorBody](t, raw).Error
			if got.Code != tc.wantCode || got.Field != tc.wantField || !strings.Contains(got.Message, tc.wantMsg) {
				t.Errorf("error = %+v, want code %s field %q message containing %q", got, tc.wantCode, tc.wantField, tc.wantMsg)
			}

			loc := header.Get("Location")
			if !tc.wantLocation {
				if loc != "" {
					t.Errorf("Location = %q, want none: nothing was recorded", loc)
				}
				return
			}
			status, _, raw = call(t, http.MethodGet, e.URL+loc, "", "")
			if status != http.StatusOK || decodeAs[deployment](t, raw).Status != "failed" {
				t.Errorf("GET %s = %d %s, want the failed deployment", loc, status, raw)
			}
			if e.Nomad.Registrations() != 0 {
				t.Errorf("registrations = %d, want 0", e.Nomad.Registrations())
			}
		})
	}
}

// SC-009: the Nomad token never shows up in logs or responses.
func TestNomadTokenNeverLeaks(t *testing.T) {
	const token = "s3cr3t-token"

	right := newTestEnv(t, token)
	right.Nomad.RequireToken(token)
	deployOK(t, right, "whoami", whoamiBody)

	wrong := newTestEnv(t, token)
	wrong.Nomad.RequireToken("another-token")
	status, _, raw := call(t, http.MethodPost, wrong.URL+"/v1/apps/whoami/deployments", "application/json", whoamiBody)
	if status != http.StatusBadGateway || !strings.Contains(string(raw), "refused the token") {
		t.Errorf("status %d body %s, want 502 saying the token was refused", status, raw)
	}

	for _, out := range []string{string(raw), right.Logs.String(), wrong.Logs.String()} {
		if strings.Contains(out, token) {
			t.Errorf("token found in: %s", out)
		}
	}
}

// US5
func TestListDeployments(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t, "")
	if _, err := e.Store.SaveApp(ctx, apptest.Whoami().App); err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		d := apptest.Whoami().Deployment
		d.ID = fmt.Sprintf("d%02d", i)
		if _, err := e.Store.CreateDeployment(ctx, "whoami", d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Store.SaveApp(ctx, app.App{ID: "empty", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{Name: "worker", Image: "nginx"}}}); err != nil {
		t.Fatal(err)
	}

	list := func(t *testing.T, path string) []deployment {
		t.Helper()
		status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, status, raw)
		}
		checkSchema(t, loadContract(t), raw, "DeploymentList")
		return decodeAs[struct {
			Deployments []deployment `json:"deployments"`
		}](t, raw).Deployments
	}

	if got := list(t, "/v1/apps/whoami/deployments"); len(got) != 20 || got[0].ID != "d24" || got[19].ID != "d05" {
		t.Errorf("default list = %d items from %s, want the newest 20 from d24", len(got), got[0].ID)
	}
	if got := list(t, "/v1/apps/whoami/deployments?limit=5"); len(got) != 5 || got[4].ID != "d20" {
		t.Errorf("limit=5 = %d items, want d24..d20", len(got))
	}
	if got := list(t, "/v1/apps/whoami/deployments?limit=100"); len(got) != 25 {
		t.Errorf("limit=100 = %d items, want 25", len(got))
	}
	if got := list(t, "/v1/apps/empty/deployments"); got == nil || len(got) != 0 {
		t.Errorf("an app without deployments = %#v, want an empty list", got)
	}

	for _, q := range []string{"0", "101", "abc", "-1"} {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments?limit="+q, "", "")
		got := decodeAs[errorBody](t, raw).Error
		if status != http.StatusBadRequest || got.Code != codeInvalidField || got.Field != "limit" {
			t.Errorf("limit=%s: %d %+v, want 400 invalid_field on limit", q, status, got)
		}
	}
	if status, _, _ := call(t, http.MethodGet, e.URL+"/v1/apps/ghost/deployments", "", ""); status != http.StatusNotFound {
		t.Errorf("unknown app: status %d, want 404", status)
	}
}
