package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// filledPath turns "/v1/apps/{id}/deployments" into a concrete path.
func filledPath(path string) string { return pathParam.ReplaceAllString(path, "x") }

// protectedRoutes are every route of the API that needs credentials.
func protectedRoutes(t *testing.T) []route {
	t.Helper()
	var out []route
	for _, rt := range (&server{}).routes() {
		if !rt.public {
			out = append(out, rt)
		}
	}
	if len(out) == 0 {
		t.Fatal("no protected routes: the route walk found nothing to check")
	}
	return out
}

// noAdminServer serves the API with an Auth on a fresh store that has no admin.
func noAdminServer(t *testing.T, e *testEnv) string {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	auth := service.NewAuth(st, slog.New(slog.DiscardHandler), service.AuthOptions{PasswordIterations: 1000})
	srv := httptest.NewServer(New(Deps{Service: e.Service, Auth: auth, Store: st, Log: e.log}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// requireUnauthorized checks resp is the one 401 every credential failure gets
// and returns its body.
func requireUnauthorized(t *testing.T, resp *http.Response) string {
	t.Helper()
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Errorf("Location = %q, want no redirect", loc)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := readErrorFrom(t, resp, raw, http.StatusUnauthorized)
	if body.Error.Code != codeUnauthorized {
		t.Errorf("code = %q, want %q", body.Error.Code, codeUnauthorized)
	}
	return string(raw)
}

// readErrorFrom is readError for a body that was already read.
func readErrorFrom(t *testing.T, resp *http.Response, raw []byte, status int) errorBody {
	t.Helper()
	resp.Body = io.NopCloser(strings.NewReader(string(raw)))
	return readError(t, resp, status)
}

func TestEveryProtectedRouteNeedsCredentials(t *testing.T) {
	e := newTestEnv(t, "")
	servers := map[string]string{
		"admin exists, no token": e.RawURL,
		"no admin yet":           noAdminServer(t, e),
	}
	for name, base := range servers {
		for _, rt := range protectedRoutes(t) {
			t.Run(name+"/"+rt.method+" "+rt.path, func(t *testing.T) {
				req, _ := http.NewRequest(rt.method, base+filledPath(rt.path), strings.NewReader(`{}`))
				req.Header.Set("Content-Type", "application/json")
				resp, err := noRedirects.Do(req)
				if err != nil {
					t.Fatalf("request: %v", err)
				}
				requireUnauthorized(t, resp)
			})
		}
	}
}

func TestPublicRoutesNeedNoCredentials(t *testing.T) {
	e := newTestEnv(t, "")

	resp, err := http.Get(e.RawURL + "/v1/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health without credentials = %d, want 200", resp.StatusCode)
	}

	// The webhook answers by its own token, not the API's: without one it is
	// the webhook's own 401, and an API token doesn't stand in for it.
	status, header, raw := hook(t, e.RawURL, "whoami", "", "", "", "")
	if status != http.StatusUnauthorized || !strings.Contains(string(raw), "webhook token") || header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("webhook without a token = %d %s", status, raw)
	}
	status, _, raw = hook(t, e.RawURL, "whoami", "", "Bearer "+e.Token, "", "")
	if status != http.StatusUnauthorized {
		t.Errorf("webhook with an API token = %d %s, want 401", status, raw)
	}

	tok := withWebhook(t, e)
	if status, _, raw := hook(t, e.RawURL, "whoami", tok, "", "", ""); status != http.StatusOK {
		t.Errorf("webhook with its own token = %d %s, want 200", status, raw)
	}
}

// TestUniformUnauthorized checks every credential failure gets the same body.
func TestUniformUnauthorized(t *testing.T) {
	e := newTestEnv(t, "")
	revoked, err := e.Auth.CreateAPIToken(context.Background(), "old", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Auth.RevokeAPIToken(context.Background(), revoked.Info.ID); err != nil {
		t.Fatal(err)
	}

	headers := map[string][]string{
		"none":                nil,
		"basic":               {"Basic YWRtaW46cGFzc3dvcmQ="},
		"bearer, no value":    {"Bearer"},
		"bearer, two spaces":  {"Bearer  " + e.Token},
		"bearer, unknown":     {"Bearer cvat_" + strings.Repeat("A", 43)},
		"webhook token":       {"Bearer cvwh_" + strings.Repeat("A", 43)},
		"revoked token":       {"Bearer " + revoked.Token},
		"token, wrong scheme": {"Token " + e.Token},
		"just the token":      {e.Token},
		"two headers":         {"Bearer " + e.Token, "Bearer " + e.Token},
	}
	var want string
	for name, hs := range headers {
		req, _ := http.NewRequest(http.MethodGet, e.RawURL+"/v1/apps/whoami", nil)
		for _, h := range hs {
			req.Header.Add("Authorization", h)
		}
		resp, err := noRedirects.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := requireUnauthorized(t, resp)
		if want == "" {
			want = got
		} else if got != want {
			t.Errorf("%s: body %s differs from the others, %s", name, got, want)
		}
	}
}

func TestValidTokenWorksOnEveryRoute(t *testing.T) {
	e := newTestEnv(t, "")
	for _, rt := range protectedRoutes(t) {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req, _ := http.NewRequest(rt.method, e.RawURL+filledPath(rt.path), strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+e.Token)
			resp, err := noRedirects.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				t.Errorf("a valid token got %d", resp.StatusCode)
			}
		})
	}
}

// sessionCookie signs the test admin in and returns a cookie for a request.
func sessionCookie(t *testing.T, e *testEnv) *http.Cookie {
	t.Helper()
	s, err := e.Auth.SignIn(context.Background(), testAdminEmail, testAdminPassword, "203.0.113.9")
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	return &http.Cookie{Name: "caravana_session", Value: s.Value}
}

// cookieCall sends a request with a session cookie and optional headers.
func cookieCall(t *testing.T, c *http.Cookie, method, url, body string, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestSessionCookieAuthenticates(t *testing.T) {
	e := newTestEnv(t, "")
	c := sessionCookie(t, e)

	// GETs work with the cookie alone.
	if status, raw := cookieCall(t, c, http.MethodGet, e.RawURL+"/v1/tokens", "", nil); status != http.StatusOK {
		t.Errorf("GET /v1/tokens with a session = %d %s, want 200", status, raw)
	}
	// So does a state-changing request with no browser headers (curl) ...
	if status, raw := cookieCall(t, c, http.MethodPost, e.RawURL+"/v1/tokens", `{"name":"via-cookie"}`, nil); status != http.StatusCreated {
		t.Errorf("POST /v1/tokens with a session = %d %s, want 201", status, raw)
	}
	// ... and one the browser says is same-origin.
	status, raw := cookieCall(t, c, http.MethodPost, e.RawURL+"/v1/tokens", `{"name":"same-origin"}`,
		map[string]string{"Sec-Fetch-Site": "same-origin"})
	if status != http.StatusCreated {
		t.Errorf("same-origin POST with a session = %d %s, want 201", status, raw)
	}

	// No cookie, or a made-up one: 401, like any other credential failure.
	if status, _ := cookieCall(t, &http.Cookie{Name: "caravana_session", Value: strings.Repeat("A", 43)}, http.MethodGet, e.RawURL+"/v1/tokens", "", nil); status != http.StatusUnauthorized {
		t.Errorf("GET with an unknown session = %d, want 401", status)
	}
}

func TestCrossSiteCookieRequestsAreRefused(t *testing.T) {
	e := newTestEnv(t, "")
	c := sessionCookie(t, e)

	for name, headers := range map[string]map[string]string{
		"Sec-Fetch-Site cross-site": {"Sec-Fetch-Site": "cross-site"},
		"Sec-Fetch-Site same-site":  {"Sec-Fetch-Site": "same-site"},
		"Origin of another host":    {"Origin": "https://evil.example"},
	} {
		status, raw := cookieCall(t, c, http.MethodPost, e.RawURL+"/v1/tokens", `{"name":"forged"}`, headers)
		if status != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, status, raw)
			continue
		}
		body := decodeAs[errorBody](t, raw)
		if body.Error.Code != "cross_origin" {
			t.Errorf("%s: code %q, want cross_origin", name, body.Error.Code)
		}
		checkSchema(t, loadContract(t), raw, "Error")
	}

	// Nothing was changed.
	infos, err := e.Auth.ListAPITokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range infos {
		if i.Name == "forged" {
			t.Error("a cross-site request created a token")
		}
	}

	// Reads are never refused for coming from elsewhere: they change nothing.
	if status, _ := cookieCall(t, c, http.MethodGet, e.RawURL+"/v1/tokens", "", map[string]string{"Sec-Fetch-Site": "cross-site"}); status != http.StatusOK {
		t.Errorf("a cross-site GET = %d, want 200", status)
	}
}

func TestBearerBeatsCookie(t *testing.T) {
	e := newTestEnv(t, "")
	c := sessionCookie(t, e)

	// A bad Bearer token is refused even with a valid session cookie.
	status, raw := cookieCall(t, c, http.MethodGet, e.RawURL+"/v1/tokens", "", map[string]string{"Authorization": "Bearer cvat_" + strings.Repeat("A", 43)})
	if status != http.StatusUnauthorized {
		t.Errorf("a bad token with a good cookie = %d %s, want 401", status, raw)
	}

	// A Bearer request never gets 403, whatever browser headers it carries.
	for _, headers := range []map[string]string{
		{"Sec-Fetch-Site": "cross-site"},
		{"Origin": "https://evil.example"},
		{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"},
	} {
		headers["Authorization"] = "Bearer " + e.Token
		name := "bearer-" + strings.ReplaceAll(headers["Sec-Fetch-Site"]+headers["Origin"], "/", "")
		status, raw := cookieCall(t, nil, http.MethodPost, e.RawURL+"/v1/tokens", `{"name":"`+strings.ReplaceAll(name, ":", "")+`"}`, headers)
		if status != http.StatusCreated {
			t.Errorf("a Bearer POST with %v = %d %s, want 201", headers, status, raw)
		}
	}
}
