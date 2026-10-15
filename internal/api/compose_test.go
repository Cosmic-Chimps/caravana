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

// Feature 014: deployCompose with If-None-Match: * creates only, with
// If-Match edits only.
func TestDeployComposeConditions(t *testing.T) {
	post := func(e *testEnv, headers ...[2]string) (int, http.Header, []byte) {
		return callHeaders(t, http.MethodPost, e.URL+"/v1/apps/whoami/compose", composeBody(t, whoamiCompose, nil), headers)
	}
	header := func(k, v string) [2]string { return [2]string{k, v} }
	errorOf := func(t *testing.T, raw []byte) errorBody {
		t.Helper()
		checkSchema(t, loadContract(t), raw, "Error")
		return decodeAs[errorBody](t, raw)
	}

	t.Run("If-None-Match creates, then refuses a taken ID", func(t *testing.T) {
		e := newTestEnv(t, "")
		if status, _, raw := post(e, header("If-None-Match", "*")); status != http.StatusAccepted {
			t.Fatalf("create: status %d: %s", status, raw)
		}
		registrations := e.Nomad.Registrations()
		rev := appRevision(t, e, "whoami")

		status, header, raw := post(e, header("If-None-Match", "*"))
		if status != http.StatusConflict {
			t.Fatalf("second create: status = %d, want 409 (body %s)", status, raw)
		}
		if got := errorOf(t, raw).Error; got.Code != "app_exists" || got.Field != "id" {
			t.Errorf("error = %+v, want app_exists on id", got)
		}
		if header.Get("Location") != "" || e.Nomad.Registrations() != registrations || appRevision(t, e, "whoami") != rev {
			t.Error("a refused create changed something")
		}
	})

	t.Run("If-Match", func(t *testing.T) {
		e := newTestEnv(t, "")
		if status, _, raw := post(e); status != http.StatusAccepted {
			t.Fatalf("deploy: status %d: %s", status, raw)
		}
		rev := appRevision(t, e, "whoami")
		registrations := e.Nomad.Registrations()

		status, _, raw := post(e, header("If-Match", `"0000000000000000"`))
		if status != http.StatusPreconditionFailed {
			t.Fatalf("stale: status = %d, want 412 (body %s)", status, raw)
		}
		if got := errorOf(t, raw).Error; got.Code != "app_changed" || !strings.Contains(got.Message, rev) {
			t.Errorf("error = %+v, want app_changed naming %s", got, rev)
		}
		if e.Nomad.Registrations() != registrations {
			t.Error("a refused deploy registered a job")
		}
		if status, _, raw := post(e, header("If-Match", `"`+rev+`"`)); status != http.StatusAccepted {
			t.Errorf("current: status %d: %s", status, raw)
		}
	})

	t.Run("If-Match on an unknown app is 404", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, _, raw := post(e, header("If-Match", `"0000000000000000"`))
		if status != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (body %s)", status, raw)
		}
	})

	t.Run("bad headers are 400 naming the header", func(t *testing.T) {
		for name, tc := range map[string]struct {
			headers   [][2]string
			wantField string
		}{
			"both":                {[][2]string{header("If-Match", `"a"`), header("If-None-Match", "*")}, "If-None-Match"},
			"If-None-Match value": {[][2]string{header("If-None-Match", `"a"`)}, "If-None-Match"},
			"If-None-Match list":  {[][2]string{header("If-None-Match", "*, *")}, "If-None-Match"},
			"If-Match unquoted":   {[][2]string{header("If-Match", "a")}, "If-Match"},
		} {
			t.Run(name, func(t *testing.T) {
				e := newTestEnv(t, "")
				status, _, raw := post(e, tc.headers...)
				if status != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body %s)", status, raw)
				}
				if got := errorOf(t, raw).Error; got.Code != "invalid_field" || got.Field != tc.wantField {
					t.Errorf("error = %+v, want invalid_field on %s", got, tc.wantField)
				}
				if e.Nomad.Registrations() != 0 {
					t.Error("a refused deploy registered a job")
				}
			})
		}
	})
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

// keptSentinel is a secret value that must never come back from any read.
const keptSentinel = "s3cret-sentinel-015"

// keptFile is a Compose file with a comment and a trailing newline: what the
// reads must give back byte for byte.
const keptFile = "# the whoami app\n" + whoamiCompose

// keptRequest is a getCompose answer, decoded as far as the tests look.
type keptRequest struct {
	Compose  string `json:"compose"`
	Services map[string]struct {
		Secrets []map[string]any `json:"secrets"`
	} `json:"services"`
}

// deployKept deploys whoami from keptFile with one secret that has a value,
// and waits for the rollout. It returns the deployment.
func deployKept(t *testing.T, e *testEnv) deployment {
	t.Helper()
	body := composeBody(t, keptFile, map[string]any{
		"web": map[string]any{"secrets": []any{map[string]any{"name": "TOKEN", "value": keptSentinel}}},
	})
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/compose", "application/json", body)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: status %d: %s", status, raw)
	}
	d := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "whoami", d.ID)
	return d
}

// checkKept checks that raw is a ComposeRequest holding keptFile, with the
// one secret by name only.
func checkKept(t *testing.T, raw []byte) {
	t.Helper()
	checkSchema(t, loadContract(t), raw, "ComposeRequest")
	got := decodeAs[keptRequest](t, raw)
	if got.Compose != keptFile {
		t.Errorf("compose = %q, want the file byte for byte", got.Compose)
	}
	secrets := got.Services["web"].Secrets
	if len(secrets) != 1 || secrets[0]["name"] != "TOKEN" {
		t.Errorf("secrets = %v, want TOKEN by name", secrets)
	}
	if _, has := secrets[0]["value"]; has {
		t.Errorf("secret = %v, want no value", secrets[0])
	}
	if strings.Contains(string(raw), keptSentinel) {
		t.Errorf("a secret value came back: %s", raw)
	}
}

// Feature 015: getCompose and getDeploymentCompose answer the kept file and
// overlay, secrets by name, with the app's revision as the ETag.
func TestGetCompose(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployKept(t, e)

	status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/compose", "", "")
	if status != http.StatusOK {
		t.Fatalf("getCompose: status %d: %s", status, raw)
	}
	checkKept(t, raw)
	if want := `"` + appRevision(t, e, "whoami") + `"`; header.Get("ETag") != want {
		t.Errorf("ETag = %q, want the app's revision %s", header.Get("ETag"), want)
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+d.ID+"/compose", "", "")
	if status != http.StatusOK {
		t.Fatalf("getDeploymentCompose: status %d: %s", status, raw)
	}
	checkKept(t, raw)
}

// Feature 015: a read that has no file to answer is 404 compose_not_kept with
// its own message; an unknown app or deployment is the plain not_found.
func TestGetComposeNotKept(t *testing.T) {
	e := newTestEnv(t, "")
	image := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", image.ID)

	tests := []struct {
		name, path, wantCode, wantMessage string
	}{
		{"app from an image", "/v1/apps/whoami/compose", "compose_not_kept", "This app's Compose file wasn't kept."},
		{"deployment from an image", "/v1/apps/whoami/deployments/" + image.ID + "/compose", "compose_not_kept", "This deployment wasn't made from a Compose file."},
		{"unknown app", "/v1/apps/ghost/compose", "not_found", ""},
		{"unknown app, deployment", "/v1/apps/ghost/deployments/" + image.ID + "/compose", "not_found", ""},
		{"unknown deployment", "/v1/apps/whoami/deployments/nope/compose", "not_found", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _, raw := call(t, http.MethodGet, e.URL+tt.path, "", "")
			if status != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", status, raw)
			}
			checkSchema(t, loadContract(t), raw, "Error")
			got := decodeAs[errorBody](t, raw).Error
			if got.Code != tt.wantCode || (tt.wantMessage != "" && got.Message != tt.wantMessage) {
				t.Errorf("error = %+v, want %s %q", got, tt.wantCode, tt.wantMessage)
			}
		})
	}
}

// Feature 015: composeKept says whether the file read answers 200, on the
// app, in the list of apps and on every deployment answer.
func TestComposeKeptFields(t *testing.T) {
	e := newTestEnv(t, "")
	kept := deployKept(t, e)
	image := deployOK(t, e, "plain", whoamiBody)
	waitHealthy(t, e, "plain", image.ID)

	appKept := func(id string) bool {
		t.Helper()
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/"+id, "", "")
		return decodeAs[struct {
			ComposeKept bool `json:"composeKept"`
		}](t, raw).ComposeKept
	}
	if !appKept("whoami") || appKept("plain") {
		t.Errorf("getApp composeKept: whoami %v, plain %v; want true, false", appKept("whoami"), appKept("plain"))
	}

	_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps", "", "")
	checkSchema(t, loadContract(t), raw, "AppSummaryList")
	listed := map[string]bool{}
	for _, s := range decodeAs[struct {
		Apps []struct {
			App         struct{ ID string } `json:"app"`
			ComposeKept bool                `json:"composeKept"`
		} `json:"apps"`
	}](t, raw).Apps {
		listed[s.App.ID] = s.ComposeKept
	}
	if !listed["whoami"] || listed["plain"] || len(listed) != 2 {
		t.Errorf("listApps composeKept = %v, want whoami true, plain false", listed)
	}

	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+kept.ID, "", "")
	if !decodeAs[deployment](t, raw).ComposeKept {
		t.Errorf("getDeployment of the Compose deploy: composeKept false, want true (%s)", raw)
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/plain/deployments", "", "")
	for _, d := range decodeAs[struct {
		Deployments []deployment `json:"deployments"`
	}](t, raw).Deployments {
		if d.ComposeKept {
			t.Errorf("listDeployments of an image app: composeKept true on %s", d.ID)
		}
	}
	if !strings.Contains(string(raw), `"composeKept":false`) {
		t.Errorf("listDeployments = %s, want composeKept false written out", raw)
	}
}

// Feature 015, SC-003: no read of an app deployed from a Compose file with a
// secret value holds that value, and neither does the request log.
func TestComposeReadsHoldNoSecretValue(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployKept(t, e)
	// The value is in the secret store, so the check below can't pass for want of a deploy.
	if v := e.Nomad.Variable("nomad/jobs/whoami/web"); v["TOKEN"] != keptSentinel {
		t.Fatalf("secret store = %v, want the value deployed", v)
	}

	for _, path := range []string{
		"/v1/apps/whoami/compose",
		"/v1/apps/whoami/deployments/" + d.ID + "/compose",
		"/v1/apps/whoami",
		"/v1/apps",
		"/v1/apps/whoami/deployments",
		"/v1/apps/whoami/deployments/" + d.ID,
		"/v1/deployments",
	} {
		t.Run(path, func(t *testing.T) {
			status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
			if status != http.StatusOK || len(raw) == 0 {
				t.Fatalf("status %d, body %q; want 200 with a body", status, raw)
			}
			if strings.Contains(string(raw), keptSentinel) {
				t.Errorf("the answer holds the secret value: %s", raw)
			}
		})
	}
	if strings.Contains(e.Logs.String(), keptSentinel) {
		t.Error("the request log holds the secret value")
	}
}

// Feature 015: what getCompose answers goes back to deployCompose unchanged:
// with its ETag as If-Match it is a safe edit, and every secret keeps its value.
func TestGetComposeRoundTrip(t *testing.T) {
	e := newTestEnv(t, "")
	deployKept(t, e)
	status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/compose", "", "")
	if status != http.StatusOK {
		t.Fatalf("getCompose: status %d: %s", status, raw)
	}

	status, _, got := callHeaders(t, http.MethodPost, e.URL+"/v1/apps/whoami/compose", string(raw),
		[][2]string{{"If-Match", header.Get("ETag")}})
	if status != http.StatusAccepted {
		t.Fatalf("deployCompose with the answer: status %d: %s", status, got)
	}
	waitHealthy(t, e, "whoami", decodeAs[deployment](t, got).ID)
	if v := e.Nomad.Variable("nomad/jobs/whoami/web"); v["TOKEN"] != keptSentinel {
		t.Errorf("secret store = %v, want the value kept", v)
	}

	// The same ETag is now stale only if the file changed: it didn't.
	if _, h2, _ := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/compose", "", ""); h2.Get("ETag") != header.Get("ETag") {
		t.Errorf("ETag after an unchanged deploy = %q, was %q", h2.Get("ETag"), header.Get("ETag"))
	}

	// A comment-only edit moves it, so the old ETag is refused.
	edited := strings.Replace(string(raw), "# the whoami app", "# the whoami app, edited", 1)
	status, _, got = callHeaders(t, http.MethodPost, e.URL+"/v1/apps/whoami/compose", edited,
		[][2]string{{"If-Match", header.Get("ETag")}})
	if status != http.StatusAccepted {
		t.Fatalf("comment edit: status %d: %s", status, got)
	}
	waitHealthy(t, e, "whoami", decodeAs[deployment](t, got).ID)
	status, _, got = callHeaders(t, http.MethodPost, e.URL+"/v1/apps/whoami/compose", edited,
		[][2]string{{"If-Match", header.Get("ETag")}})
	if status != http.StatusPreconditionFailed {
		t.Errorf("stale ETag after a comment edit: status %d, want 412 (%s)", status, got)
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
