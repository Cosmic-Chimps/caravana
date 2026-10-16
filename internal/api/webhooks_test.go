package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"
)

const movedDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// webhookResult is a WebhookResult response, decoded.
type webhookResult struct {
	Changed    bool       `json:"changed"`
	Deployment deployment `json:"deployment"`
}

// withWebhook deploys whoami, waits for it to be healthy, and creates a
// token. It returns the token.
func withWebhook(t *testing.T, e *testEnv) string {
	t.Helper()
	d := deployOK(t, e, "whoami", whoamiBody)
	waitUntil(t, func() bool {
		rec, err := e.Store.GetDeployment(context.Background(), d.ID)
		return err == nil && rec.Status == "healthy"
	})
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/webhook", "", "")
	if status != http.StatusCreated {
		t.Fatalf("create token: %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "WebhookToken")
	tok := decodeAs[struct {
		Token string `json:"token"`
		Path  string `json:"path"`
	}](t, raw)
	if tok.Path != "/v1/webhooks/whoami?token="+tok.Token {
		t.Errorf("path doesn't hold the webhook path and the token")
	}
	return tok.Token
}

// hook calls the webhook at base with the token in the query (q) and/or the
// Authorization header (h).
func hook(t *testing.T, base, appID, q, h, contentType, body string) (int, http.Header, []byte) {
	t.Helper()
	url := base + "/v1/webhooks/" + appID
	if q != "" {
		url += "?token=" + q
	}
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if h != "" {
		req.Header.Set("Authorization", h)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, resp.Header, buf.Bytes()
}

// US1
func TestWebhookEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	tok := withWebhook(t, e)
	doc := loadContract(t)

	for name, c := range map[string][2]string{
		"query":          {tok, ""},
		"header":         {"", "Bearer " + tok},
		"header, lower":  {"", "bearer " + tok},
		"both, the same": {tok, "Bearer " + tok},
	} {
		t.Run("unchanged via "+name, func(t *testing.T) {
			status, _, raw := hook(t, e.URL, "whoami", c[0], c[1], "", "")
			if status != http.StatusOK {
				t.Fatalf("status %d %s, want 200", status, raw)
			}
			checkSchema(t, doc, raw, "WebhookResult")
			if r := decodeAs[webhookResult](t, raw); r.Changed {
				t.Error("changed = true for an unchanged image")
			}
		})
	}

	for name, c := range map[string][2]string{
		"json body":  {"application/json", `{"push_data":{"tag":"latest"}}`},
		"plain body": {"text/plain", "hello"},
		"no body":    {"", ""},
	} {
		t.Run("body ignored: "+name, func(t *testing.T) {
			if status, _, raw := hook(t, e.URL, "whoami", tok, "", c[0], c[1]); status != http.StatusOK {
				t.Errorf("status %d %s, want 200", status, raw)
			}
		})
	}

	t.Run("body too large", func(t *testing.T) {
		e.Registry.AddIndex("traefik/whoami", "latest", movedDigest)
		defer e.Registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
		before := e.Nomad.Registrations()
		status, _, raw := hook(t, e.URL, "whoami", tok, "", "application/json", strings.Repeat("a", maxBodyBytes+1))
		if status != http.StatusRequestEntityTooLarge || decodeAs[errorBody](t, raw).Error.Code != codeBodyTooLarge {
			t.Errorf("status %d %s, want 413 body_too_large", status, raw)
		}
		if e.Nomad.Registrations() != before {
			t.Error("a too-large body still deployed")
		}
	})

	t.Run("moved tag redeploys", func(t *testing.T) {
		e.Registry.AddIndex("traefik/whoami", "latest", movedDigest)
		status, header, raw := hook(t, e.URL, "whoami", tok, "", "", "")
		if status != http.StatusAccepted {
			t.Fatalf("status %d %s, want 202", status, raw)
		}
		checkSchema(t, doc, raw, "WebhookResult")
		r := decodeAs[webhookResult](t, raw)
		if !r.Changed || r.Deployment.Trigger != "webhook" || header.Get("Location") != "/v1/apps/whoami/deployments/"+r.Deployment.ID {
			t.Errorf("result %+v, Location %q; want a webhook deployment and its Location", r, header.Get("Location"))
		}
	})
}

// SC-004: a planted token never shows up in logs or later responses.
func TestWebhookTokenNeverLogged(t *testing.T) {
	e := newTestEnv(t, "")
	tok := withWebhook(t, e)
	var bodies []string
	for _, c := range [][3]string{{tok, "", ""}, {"", "Bearer " + tok, ""}, {"", "", tok}, {"wrong", "Bearer " + tok, tok}} {
		_, _, raw := hook(t, e.URL, "whoami", c[0], c[1], "text/plain", c[2])
		bodies = append(bodies, string(raw))
	}
	_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/webhook", "", "")
	bodies = append(bodies, string(raw))
	for _, out := range append(bodies, e.Logs.String()) {
		if strings.Contains(out, tok) || strings.Contains(out, "cvwh_") {
			t.Errorf("token found in %.200s", out)
		}
	}
}

// US2: one 401 for every reason, and the webhook-only listener.
func TestWebhookUnauthorized(t *testing.T) {
	e := newTestEnv(t, "")
	tok := withWebhook(t, e)
	before := e.Nomad.Registrations()

	var first []byte
	for name, c := range map[string][3]string{
		"no token":                {"whoami", "", ""},
		"wrong token":             {"whoami", "cvwh_" + strings.Repeat("A", 43), ""},
		"query and header differ": {"whoami", tok, "Bearer cvwh_" + strings.Repeat("B", 43)},
		"another scheme":          {"whoami", "", "Basic " + tok},
		"unknown app":             {"ghost", tok, ""},
	} {
		t.Run(name, func(t *testing.T) {
			status, header, raw := hook(t, e.URL, c[0], c[1], c[2], "", "")
			if status != http.StatusUnauthorized || header.Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("status %d, WWW-Authenticate %q, body %s; want 401 Bearer", status, header.Get("WWW-Authenticate"), raw)
			}
			checkSchema(t, loadContract(t), raw, "Error")
			if first == nil {
				first = raw
			} else if !bytes.Equal(first, raw) {
				t.Errorf("body %s differs from %s: failures must be indistinguishable", raw, first)
			}
		})
	}
	if e.Nomad.Registrations() != before {
		t.Error("a refused call deployed")
	}

	t.Run("webhook-only listener", func(t *testing.T) {
		srv := httptest.NewServer(NewWebhook(e.Service, e.log))
		defer srv.Close()
		for _, c := range [][2]string{
			{http.MethodGet, "/v1/health"},
			{http.MethodPost, "/v1/apps/whoami/deployments"},
			{http.MethodGet, "/v1/apps/whoami"},
			{http.MethodPost, "/v1/apps/whoami/webhook"},
			{http.MethodGet, "/v1/apps/whoami/webhook"},
		} {
			status, _, raw := call(t, c[0], srv.URL+c[1], "", "")
			if status != http.StatusNotFound {
				t.Errorf("%s %s on the webhook listener = %d %s, want 404", c[0], c[1], status, raw)
			}
		}
		if status, _, _ := call(t, http.MethodGet, srv.URL+"/v1/webhooks/whoami", "", ""); status != http.StatusMethodNotAllowed {
			t.Errorf("GET /v1/webhooks/whoami = %d, want 405", status)
		}
		status, _, raw := hook(t, srv.URL, "whoami", tok, "", "", "")
		if status != http.StatusOK || decodeAs[webhookResult](t, raw).Changed {
			t.Errorf("webhook on its listener = %d %s, want 200 unchanged like the main API", status, raw)
		}
		if status, _, _ := hook(t, srv.URL, "whoami", "", "", "", ""); status != http.StatusUnauthorized {
			t.Errorf("no token on the webhook listener = %d, want 401", status)
		}
	})
}

// US3: status, delete, rotate.
func TestWebhookTokenEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	tok := withWebhook(t, e)
	doc := loadContract(t)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/webhook", "", "")
	checkSchema(t, doc, raw, "WebhookStatus")
	if st := decodeAs[struct{ Enabled bool }](t, raw); status != http.StatusOK || !st.Enabled {
		t.Errorf("status = %d %s, want enabled", status, raw)
	}

	_, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/webhook", "", "")
	rotated := decodeAs[struct{ Token string }](t, raw).Token
	if s, _, _ := hook(t, e.URL, "whoami", tok, "", "", ""); s != http.StatusUnauthorized {
		t.Errorf("old token after rotation = %d, want 401", s)
	}
	if s, _, _ := hook(t, e.URL, "whoami", rotated, "", "", ""); s != http.StatusOK {
		t.Errorf("new token = %d, want 200", s)
	}

	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/apps/whoami/webhook", "", "")
	if status != http.StatusNoContent || len(raw) != 0 {
		t.Errorf("delete = %d %q, want 204 with no body", status, raw)
	}
	if s, _, _ := hook(t, e.URL, "whoami", rotated, "", "", ""); s != http.StatusUnauthorized {
		t.Errorf("token after delete = %d, want 401", s)
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/webhook", "", "")
	checkSchema(t, doc, raw, "WebhookStatus")
	if strings.Contains(string(raw), "createdAt") || strings.Contains(string(raw), "true") {
		t.Errorf("status after delete = %s, want enabled false and no createdAt", raw)
	}

	for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if s, _, _ := call(t, m, e.URL+"/v1/apps/ghost/webhook", "", ""); s != http.StatusNotFound {
			t.Errorf("%s on an unknown app = %d, want 404", m, s)
		}
	}
}

// US4: failed webhook deploys answer like failed deploys.
func TestWebhookDeployErrors(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(e *testEnv)
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{"missing tag", func(e *testEnv) {
			deployOKBody := withImage("traefik/whoami:nope")
			call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", deployOKBody)
		}, 422, codeImageNotFound, "services[0].image"},
		{"registry refuses the stored credentials", func(e *testEnv) { refuseStoredCredentials(t, e) }, 422, codeRegistryAuthFailed, "services[0].image"},
		{"registry down", func(e *testEnv) { e.Registry.Fail("traefik/whoami", "latest", 429) }, 502, codeRegistryUnavailable, "services[0].image"},
		{"foreign job", func(e *testEnv) {
			e.Registry.AddIndex("traefik/whoami", "latest", movedDigest)
			e.Nomad.AddForeignJob(&api.Job{ID: new("whoami"), Name: new("whoami"), Meta: map[string]string{"caravana.deployment": "lab-manual"}})
		}, 409, codeJobConflict, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, "")
			tok := withWebhook(t, e)
			tc.setup(e)
			status, header, raw := hook(t, e.URL, "whoami", tok, "", "", "")
			if status != tc.wantStatus {
				t.Fatalf("status %d %s, want %d", status, raw, tc.wantStatus)
			}
			checkSchema(t, loadContract(t), raw, "Error")
			got := decodeAs[errorBody](t, raw).Error
			if strings.Contains(string(raw), registrySecret) {
				t.Errorf("the answer holds a stored secret: %s", raw)
			}
			if got.Code != tc.wantCode || got.Field != tc.wantField || header.Get("Location") == "" {
				t.Errorf("error %+v, Location %q; want %s on %q with a Location", got, header.Get("Location"), tc.wantCode, tc.wantField)
			}
			_, _, raw = call(t, http.MethodGet, e.URL+header.Get("Location"), "", "")
			if d := decodeAs[deployment](t, raw); d.Status != "failed" || d.Trigger != "webhook" {
				t.Errorf("record = %s, trigger %s; want failed, webhook", d.Status, d.Trigger)
			}
		})
	}
}
