package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
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
	Progress   *progress         `json:"progress"`
}

// progress is the Progress schema, decoded.
type progress struct {
	Wanted         int    `json:"wanted"`
	Placed         int    `json:"placed"`
	Healthy        int    `json:"healthy"`
	Unhealthy      int    `json:"unhealthy"`
	CanariesWanted int    `json:"canariesWanted"`
	Promoted       bool   `json:"promoted"`
	Description    string `json:"description"`
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

// callHeaders is call with extra request headers. A key may be given more
// than once, as a client sending two header lines would.
func callHeaders(t *testing.T, method, url, body string, headers [][2]string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		req.Header.Add(h[0], h[1])
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

// appRevision reads an app and returns its revision, checking that the ETag
// header and the body field agree.
func appRevision(t *testing.T, e *testEnv, appID string) string {
	t.Helper()
	status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/"+appID, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET app %s = %d %s", appID, status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")
	rev := decodeAs[struct {
		Revision string `json:"revision"`
	}](t, raw).Revision
	if rev == "" || header.Get("ETag") != `"`+rev+`"` {
		t.Fatalf("ETag %q, revision %q; want the revision quoted", header.Get("ETag"), rev)
	}
	return rev
}

// Feature 014: getApp names the app's revision, and a deploy with If-Match
// saves only if it is still current.
func TestDeployIfMatch(t *testing.T) {
	post := func(e *testEnv, body string, headers ...[2]string) (int, http.Header, []byte) {
		return callHeaders(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", body, headers)
	}
	ifMatch := func(v string) [2]string { return [2]string{"If-Match", v} }
	// edited is whoamiBody with a longer health timeout: a real change the fake registry can deploy.
	edited := strings.Replace(whoamiBody, `"2s"`, `"3s"`, 1)

	t.Run("the current revision deploys the edit", func(t *testing.T) {
		e := newTestEnv(t, "")
		deployOK(t, e, "whoami", whoamiBody)
		rev := appRevision(t, e, "whoami")

		status, _, raw := post(e, edited, ifMatch(`"`+rev+`"`))
		if status != http.StatusAccepted {
			t.Fatalf("status %d: %s", status, raw)
		}
		if got := appRevision(t, e, "whoami"); got == rev {
			t.Error("the revision didn't change after an edit")
		}
	})

	t.Run("a weak tag is the same revision", func(t *testing.T) {
		e := newTestEnv(t, "")
		deployOK(t, e, "whoami", whoamiBody)
		rev := appRevision(t, e, "whoami")
		if status, _, raw := post(e, whoamiBody, ifMatch(`W/"`+rev+`"`)); status != http.StatusAccepted {
			t.Errorf("status %d: %s", status, raw)
		}
	})

	t.Run("a redeploy of the same payload keeps the revision", func(t *testing.T) {
		e := newTestEnv(t, "")
		deployOK(t, e, "whoami", whoamiBody)
		rev := appRevision(t, e, "whoami")
		deployOK(t, e, "whoami", whoamiBody)
		if got := appRevision(t, e, "whoami"); got != rev {
			t.Errorf("revision %s after an identical deploy, want %s", got, rev)
		}
	})

	t.Run("a stale revision is 412 and deploys nothing", func(t *testing.T) {
		e := newTestEnv(t, "")
		deployOK(t, e, "whoami", whoamiBody)
		rev := appRevision(t, e, "whoami")
		registrations := e.Nomad.Registrations()

		status, header, raw := post(e, edited, ifMatch(`"0000000000000000"`))
		if status != http.StatusPreconditionFailed {
			t.Fatalf("status = %d, want 412 (body %s)", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Error")
		got := decodeAs[errorBody](t, raw).Error
		if got.Code != "app_changed" || got.Field != "" || !strings.Contains(got.Message, rev) {
			t.Errorf("error = %+v, want app_changed, no field, naming revision %s", got, rev)
		}
		if header.Get("Location") != "" || e.Nomad.Registrations() != registrations {
			t.Error("a refused deploy left a deployment or a job")
		}
		if appRevision(t, e, "whoami") != rev {
			t.Error("a refused deploy changed the stored app")
		}
	})

	t.Run("an unknown app is 404", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, _, raw := post(e, whoamiBody, ifMatch(`"0000000000000000"`))
		if status != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (body %s)", status, raw)
		}
		if e.Nomad.Registrations() != 0 {
			t.Error("a refused deploy registered a job")
		}
	})

	t.Run("without a body the header is ignored", func(t *testing.T) {
		e := newTestEnv(t, "")
		deployOK(t, e, "whoami", whoamiBody)
		if status, _, raw := post(e, "", ifMatch(`"0000000000000000"`)); status != http.StatusAccepted {
			t.Errorf("redeploy with a stale If-Match = %d %s, want 202", status, raw)
		}
	})

	t.Run("malformed values are 400 naming the header", func(t *testing.T) {
		for name, headers := range map[string][][2]string{
			"unquoted":  {ifMatch("abcdef0123456789")},
			"star":      {ifMatch("*")},
			"empty tag": {ifMatch(`""`)},
			"a list":    {ifMatch(`"a", "b"`)},
			"two lines": {ifMatch(`"a"`), ifMatch(`"b"`)},
		} {
			t.Run(name, func(t *testing.T) {
				e := newTestEnv(t, "")
				status, _, raw := post(e, whoamiBody, headers...)
				if status != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body %s)", status, raw)
				}
				checkSchema(t, loadContract(t), raw, "Error")
				if got := decodeAs[errorBody](t, raw).Error; got.Code != "invalid_field" || got.Field != "If-Match" {
					t.Errorf("error = %+v, want invalid_field on If-Match", got)
				}
				if e.Nomad.Registrations() != 0 {
					t.Error("a refused deploy registered a job")
				}
			})
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
		if _, err := e.Store.CreateDeployment(ctx, "whoami", d, nil, ""); err != nil {
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

// US1: the Apps page's activity feed.
func TestListRecentDeployments(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t, "")
	for _, id := range []string{"whoami", "blog"} {
		a := apptest.Whoami().App
		a.ID = id
		if _, err := e.Store.SaveApp(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 25 {
		d := apptest.Whoami().Deployment
		d.ID = fmt.Sprintf("d%02d", i)
		appID := []string{"whoami", "blog"}[i%2]
		if _, err := e.Store.CreateDeployment(ctx, appID, d, nil, ""); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct creation times keep "newest" unambiguous
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

	if got := list(t, "/v1/deployments"); len(got) != 20 || got[0].ID != "d24" || got[1].ID != "d23" || got[19].ID != "d05" {
		t.Errorf("default list = %d items from %s, want the newest 20 from d24 (across apps)", len(got), got[0].ID)
	}
	if got := list(t, "/v1/deployments"); got[0].AppID != "whoami" || got[1].AppID != "blog" {
		t.Errorf("app IDs = %s, %s; want whoami, blog (both apps are mixed in)", got[0].AppID, got[1].AppID)
	}
	if got := list(t, "/v1/deployments?limit=3"); len(got) != 3 || got[2].ID != "d22" {
		t.Errorf("limit=3 = %d items, want d24..d22", len(got))
	}
	if got := list(t, "/v1/deployments?limit=100"); len(got) != 25 {
		t.Errorf("limit=100 = %d items, want 25", len(got))
	}

	for _, q := range []string{"0", "101", "abc", "-1"} {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/deployments?limit="+q, "", "")
		got := decodeAs[errorBody](t, raw).Error
		if status != http.StatusBadRequest || got.Code != codeInvalidField || got.Field != "limit" {
			t.Errorf("limit=%s: %d %+v, want 400 invalid_field on limit", q, status, got)
		}
	}

	t.Run("401 without credentials", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.RawURL+"/v1/deployments", "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("GET /v1/deployments without credentials = %d %s, want 401", status, raw)
		}
	})

	t.Run("empty store gives an empty list", func(t *testing.T) {
		other := newTestEnv(t, "")
		_, _, raw := call(t, http.MethodGet, other.URL+"/v1/deployments", "", "")
		checkSchema(t, loadContract(t), raw, "DeploymentList")
		if got := decodeAs[struct {
			Deployments []deployment `json:"deployments"`
		}](t, raw).Deployments; got == nil || len(got) != 0 {
			t.Errorf("deployments = %#v, want an empty list", got)
		}
	})
}

// US2: rollout progress on the Deployment schema.
func TestDeploymentProgress(t *testing.T) {
	ctx := context.Background()
	rolling := deploytest.Rolling("Deployment is running", map[string]api.DeploymentState{
		"web": {DesiredTotal: 2, DesiredCanaries: 2, PlacedAllocs: 2, HealthyAllocs: 1},
	})

	t.Run("a deployment started through the API is manual, and its running state shows progress", func(t *testing.T) {
		e := newTestEnv(t, "")
		e.Nomad.Script("whoami", 0, rolling)
		d := deployOK(t, e, "whoami", whoamiBody)
		if d.Trigger != "manual" {
			t.Errorf("trigger = %q, want manual", d.Trigger)
		}

		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+d.ID, "", "")
		if status != http.StatusOK {
			t.Fatalf("GET deployment = %d %s", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Deployment")
		got := decodeAs[deployment](t, raw)
		want := progress{Wanted: 2, Placed: 2, Healthy: 1, CanariesWanted: 2, Description: "Deployment is running"}
		if got.Status != "running" || got.Progress == nil || *got.Progress != want {
			t.Errorf("deployment = %s with progress %+v, want running with %+v", got.Status, got.Progress, want)
		}
	})

	t.Run("never on a final deployment", func(t *testing.T) {
		e := newTestEnv(t, "")
		d := deployOK(t, e, "whoami", whoamiBody)
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+d.ID, "", "")
			got := decodeAs[deployment](t, raw)
			if got.Status == "healthy" {
				if strings.Contains(string(raw), `"progress"`) {
					t.Errorf("a healthy deployment has progress: %s", raw)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("deployment still %s after 5s", got.Status)
			}
			time.Sleep(2 * time.Millisecond)
		}
	})

	t.Run("listDeployments adds it to the newest running deployment only", func(t *testing.T) {
		e := newTestEnv(t, "")
		if _, err := e.Store.SaveApp(ctx, apptest.Whoami().App); err != nil {
			t.Fatal(err)
		}
		job := func() *api.Job { return &api.Job{ID: new("whoami")} }
		e.Nomad.AddForeignJob(job())
		e.Nomad.AddForeignJob(job())
		e.Nomad.Script("whoami", 0, rolling)
		e.Nomad.Script("whoami", 1, rolling)

		for _, d := range []struct {
			id      string
			version uint64
			status  store.Status
		}{{"d1", 0, store.StatusRunning}, {"d2", 0, store.StatusHealthy}, {"d3", 1, store.StatusRunning}, {"d4", 1, store.StatusRunning}} {
			dep := apptest.Whoami().Deployment
			dep.ID = d.id
			if _, err := e.Store.CreateDeployment(ctx, "whoami", dep, nil, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Store.UpdateDeployment(ctx, d.id, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: new(d.version)}); err != nil {
				t.Fatal(err)
			}
			if d.status != store.StatusRunning {
				if _, err := e.Store.UpdateDeployment(ctx, d.id, store.DeploymentUpdate{Status: d.status}); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(time.Millisecond)
		}

		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments", "", "")
		if status != http.StatusOK {
			t.Fatalf("list = %d %s", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "DeploymentList")
		list := decodeAs[struct {
			Deployments []deployment `json:"deployments"`
		}](t, raw).Deployments
		has := map[string]bool{}
		for _, d := range list {
			has[d.ID] = d.Progress != nil
		}
		if want := map[string]bool{"d4": true, "d3": false, "d2": false, "d1": false}; !reflect.DeepEqual(has, want) {
			t.Errorf("progress by deployment = %v, want only the newest running one (d4): %v", has, want)
		}

		// The cross-app feed never carries it.
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/deployments", "", "")
		if strings.Contains(string(raw), `"progress"`) {
			t.Errorf("GET /v1/deployments has progress: %s", raw)
		}
	})

	t.Run("Nomad down: the deployment is still answered, without progress", func(t *testing.T) {
		e := newTestEnv(t, "")
		e.Nomad.Script("whoami", 0, rolling)
		d := deployOK(t, e, "whoami", whoamiBody)
		e.Nomad.Down()
		for _, path := range []string{"/v1/apps/whoami/deployments/" + d.ID, "/v1/apps/whoami/deployments"} {
			status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
			if status != http.StatusOK || strings.Contains(string(raw), `"progress"`) {
				t.Errorf("GET %s with Nomad down = %d %s; want 200 without progress", path, status, raw)
			}
		}
	})
}

// Feature 013: a service's memory reservation and ceiling round-trip, and a
// reservation above the ceiling is refused on the reservation.
func TestDeployMemory(t *testing.T) {
	e := newTestEnv(t, "")
	withMemory := func(res string) string {
		return strings.Replace(whoamiBody, `"name": "web",`, `"name": "web", "resources": `+res+`,`, 1)
	}

	d := deployOK(t, e, "whoami", withMemory(`{"memoryMB": 300, "memoryMaxMB": 600}`))
	waitHealthy(t, e, "whoami", d.ID)
	_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if !strings.Contains(string(raw), `"memoryMB":300`) || !strings.Contains(string(raw), `"memoryMaxMB":600`) {
		t.Errorf("the stored app lost its memory settings: %s", raw)
	}
	r := e.Nomad.Job("whoami").TaskGroups[0].Tasks[0].Resources
	if *r.MemoryMB != 300 || *r.MemoryMaxMB != 600 {
		t.Errorf("Nomad's task reserves %d, may use %d; want 300 and 600", *r.MemoryMB, *r.MemoryMaxMB)
	}

	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json",
		withMemory(`{"memoryMB": 600, "memoryMaxMB": 300}`))
	body := decodeAs[errorBody](t, raw)
	if status != http.StatusBadRequest || body.Error.Field != "services[0].resources.memoryMB" {
		t.Errorf("reservation above the ceiling = %d %s, want 400 on services[0].resources.memoryMB", status, raw)
	}
}
