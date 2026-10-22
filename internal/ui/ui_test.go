package ui

import (
	"bytes"
	"context"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	testEmail    = "you@example.com"
	testPassword = "correct horse battery"
)

// fakeClock is the clock the test Auth uses; tests move it by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// syncBuffer is a bytes.Buffer safe to write from the server and read from
// the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// uiEnv is the pages on a real store, with a fake clock and fast hashing.
type uiEnv struct {
	URL   string
	Auth  *service.Auth
	Store *store.Store
	Clock *fakeClock
	Logs  *syncBuffer
}

// newUIEnv serves the pages. withAdmin creates the admin testEmail /
// testPassword first. proxies are the trusted proxies ("" for none).
func newUIEnv(t *testing.T, withAdmin bool, proxies string) *uiEnv {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	e := &uiEnv{Store: st, Clock: &fakeClock{t: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)}, Logs: &syncBuffer{}}
	log := slog.New(slog.NewJSONHandler(e.Logs, nil))
	e.Auth = service.NewAuth(st, log, service.AuthOptions{Now: e.Clock.Now, PasswordIterations: 1000})
	if withAdmin {
		if err := e.Auth.InitAdmin(context.Background(), testEmail, testPassword); err != nil {
			t.Fatalf("create admin: %v", err)
		}
	}
	trusted, err := httpx.ParseProxies(proxies)
	if err != nil {
		t.Fatalf("parse proxies: %v", err)
	}
	h, err := New(Deps{Auth: e.Auth, Proxies: trusted, Log: log, Now: e.Clock.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e.URL = srv.URL
	return e
}

// noRedirects is a client that shows redirects instead of following them.
var noRedirects = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// request describes one call to the pages.
type request struct {
	method  string
	path    string
	form    url.Values
	headers map[string]string
	cookies []*http.Cookie
}

// do sends r and returns the response, with its body read into body.
func (e *uiEnv) do(t *testing.T, r request) (resp *http.Response, body string) {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodGet
	}
	var reader io.Reader
	if r.form != nil {
		reader = strings.NewReader(r.form.Encode())
	}
	req, err := http.NewRequest(r.method, e.URL+r.path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if r.form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	for _, c := range r.cookies {
		req.AddCookie(c)
	}
	resp, err = noRedirects.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.method, r.path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// signIn signs in and returns the session cookie.
func (e *uiEnv) signIn(t *testing.T, headers map[string]string) *http.Cookie {
	t.Helper()
	resp, body := e.do(t, request{method: http.MethodPost, path: "/login", headers: headers,
		form: url.Values{"email": {testEmail}, "password": {testPassword}}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in = %d, want 303; body:\n%s", resp.StatusCode, body)
	}
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, "caravana_session") {
			return c
		}
	}
	t.Fatal("sign-in set no session cookie")
	return nil
}

func location(t *testing.T, resp *http.Response) string {
	t.Helper()
	return resp.Header.Get("Location")
}

func TestLoginPage(t *testing.T) {
	e := newUIEnv(t, true, "")
	resp, body := e.do(t, request{path: "/login"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	for _, want := range []string{
		"<form", `name="email"`, `name="password"`, ">Sign in<", "caravana admin reset-password",
		"caravana</span>", strings.TrimPrefix(e.URL, "http://"), // the host being signed in to
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the login page lacks %q:\n%s", want, body)
		}
	}
}

func TestLoginPageWithoutAdmin(t *testing.T) {
	e := newUIEnv(t, false, "")
	resp, body := e.do(t, request{path: "/login"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d", resp.StatusCode)
	}
	if strings.Contains(body, "<form") || strings.Contains(body, `name="password"`) {
		t.Errorf("a form is shown although there is no admin:\n%s", body)
	}
	for _, want := range []string{"no admin yet", "caravana admin init --email"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}

	// Posting anyway shows the same page, not a sign-in failure.
	resp, body = e.do(t, request{method: http.MethodPost, path: "/login",
		form: url.Values{"email": {testEmail}, "password": {testPassword}}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "no admin yet") || len(resp.Cookies()) != 0 {
		t.Errorf("POST /login without admin = %d, cookies %v", resp.StatusCode, resp.Cookies())
	}

	// And the admin appearing makes the form appear, without a restart.
	if err := e.Auth.InitAdmin(context.Background(), testEmail, testPassword); err != nil {
		t.Fatal(err)
	}
	if _, body = e.do(t, request{path: "/login"}); !strings.Contains(body, "<form") {
		t.Error("the form did not appear once the admin existed")
	}
}

func TestSignIn(t *testing.T) {
	e := newUIEnv(t, true, "")

	t.Run("sets the cookie and goes to /apps", func(t *testing.T) {
		resp, _ := e.do(t, request{method: http.MethodPost, path: "/login",
			form: url.Values{"email": {testEmail}, "password": {testPassword}}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps" {
			t.Fatalf("sign-in = %d to %q, want 303 to /apps", resp.StatusCode, location(t, resp))
		}
		cookies := resp.Cookies()
		if len(cookies) != 1 {
			t.Fatalf("got %d cookies, want 1", len(cookies))
		}
		c := cookies[0]
		wantExpiry := e.Clock.Now().Add(7 * 24 * time.Hour)
		if c.Name != "caravana_session" || c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
			c.Path != "/" || !c.Expires.Equal(wantExpiry) || len(c.Value) != 43 {
			t.Errorf("cookie = %+v, want caravana_session, HttpOnly, SameSite=Lax, Path=/, expiring %v", c, wantExpiry)
		}
	})

	t.Run("follows a safe next", func(t *testing.T) {
		resp, _ := e.do(t, request{method: http.MethodPost, path: "/login",
			form: url.Values{"email": {testEmail}, "password": {testPassword}, "next": {"/account?tab=tokens"}}})
		if got := location(t, resp); got != "/account?tab=tokens" {
			t.Errorf("redirect to %q, want /account?tab=tokens", got)
		}
	})

	t.Run("unsafe next values land on /apps", func(t *testing.T) {
		for _, next := range []string{"//evil.example", `/\evil`, "https://evil.example", "/v1/apps", "/v1", "javascript:alert(1)", "evil", "/login", "/x\r\nSet-Cookie: a=b"} {
			resp, _ := e.do(t, request{method: http.MethodPost, path: "/login",
				form: url.Values{"email": {testEmail}, "password": {testPassword}, "next": {next}}})
			if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps" {
				t.Errorf("next %q: redirect %d to %q, want 303 to /apps", next, resp.StatusCode, location(t, resp))
			}
		}
	})

	t.Run("behind a trusted proxy that says HTTPS the cookie is __Host- and Secure", func(t *testing.T) {
		proxied := newUIEnv(t, true, "127.0.0.1")
		c := proxied.signIn(t, map[string]string{"X-Forwarded-Proto": "https"})
		if c.Name != "__Host-caravana_session" || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" {
			t.Errorf("cookie = %+v, want __Host-caravana_session, Secure, HttpOnly, Path=/, no Domain", c)
		}
		// The same header from an untrusted peer is ignored.
		plain := newUIEnv(t, true, "")
		if c := plain.signIn(t, map[string]string{"X-Forwarded-Proto": "https"}); c.Secure || c.Name != "caravana_session" {
			t.Errorf("an untrusted X-Forwarded-Proto changed the cookie: %+v", c)
		}
	})
}

func TestSignInFailure(t *testing.T) {
	bodies := map[string]string{}
	for name, form := range map[string]url.Values{
		"wrong password": {"email": {testEmail}, "password": {"not the password"}},
		"wrong email":    {"email": {"nobody@example.com"}, "password": {testPassword}},
	} {
		e := newUIEnv(t, true, "") // one each, so both are a first failure
		resp, body := e.do(t, request{method: http.MethodPost, path: "/login", form: form})
		if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 0 {
			t.Errorf("%s: %d with %d cookies, want 200 and none", name, resp.StatusCode, len(resp.Cookies()))
		}
		if !strings.Contains(body, "That email and password don&#39;t match.") {
			t.Errorf("%s: the page lacks the sentence:\n%s", name, body)
		}
		if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `cv-mark-warning`) {
			t.Errorf("%s: the sentence is not an announced alert with its mark:\n%s", name, body)
		}
		if strings.Contains(body, "not the password") || strings.Contains(body, testPassword) {
			t.Errorf("%s: the password came back in the page", name)
		}
		if got := strings.Contains(body, `value="`+form.Get("email")+`"`); !got {
			t.Errorf("%s: the email was not kept", name)
		}
		// Each case ran on its own server: only the host and the email may differ.
		body = strings.ReplaceAll(body, strings.TrimPrefix(e.URL, "http://"), "HOST")
		bodies[name] = strings.ReplaceAll(body, form.Get("email"), "EMAIL")
	}
	if bodies["wrong password"] != bodies["wrong email"] {
		t.Error("a wrong email and a wrong password get different pages")
	}
}

func TestAccountNeedsASession(t *testing.T) {
	e := newUIEnv(t, true, "")

	resp, _ := e.do(t, request{path: "/settings"})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/login?next=%2Fsettings" {
		t.Errorf("GET /settings without a session = %d to %q, want 303 to /login?next=%%2Fsettings", resp.StatusCode, location(t, resp))
	}

	c := e.signIn(t, nil)
	resp, body := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Signed in as") || !strings.Contains(body, testEmail) {
		t.Errorf("GET /settings with a session = %d:\n%s", resp.StatusCode, body)
	}

	// Signed in already: /login goes on to /apps.
	resp, _ = e.do(t, request{path: "/login", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps" {
		t.Errorf("GET /login while signed in = %d to %q, want 303 to /apps", resp.StatusCode, location(t, resp))
	}

	// "/" goes to /apps for a signed-in admin, and to sign-in for anyone else.
	resp, _ = e.do(t, request{path: "/", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps" {
		t.Errorf("GET / while signed in = %d to %q, want 303 to /apps", resp.StatusCode, location(t, resp))
	}
	resp, _ = e.do(t, request{path: "/"})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/login" {
		t.Errorf("GET / without a session = %d to %q, want 303 to /login", resp.StatusCode, location(t, resp))
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)

	e.Clock.Advance(13 * time.Hour) // idle for more than 12 hours
	resp, _ := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login?next=%2Fsettings") {
		t.Fatalf("an idle session = %d to %q, want 303 to /login", resp.StatusCode, location(t, resp))
	}
	// The sign-in page says the session ended.
	_, body := e.do(t, request{path: location(t, resp)})
	if !strings.Contains(body, "Your session ended. Sign in again.") {
		t.Errorf("the login page does not say the session ended:\n%s", body)
	}

	// A form posted with an ended session doesn't come back to the form's URL,
	// which is no page to land on.
	resp, _ = e.do(t, request{method: http.MethodPost, path: "/settings/password", cookies: []*http.Cookie{c},
		form: url.Values{"current": {"x"}, "new": {"y"}}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/login?expired=1" {
		t.Errorf("a POST with an idle session = %d to %q, want 303 to /login?expired=1", resp.StatusCode, location(t, resp))
	}
}

func TestSignOut(t *testing.T) {
	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)

	resp, _ := e.do(t, request{method: http.MethodPost, path: "/logout", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/login" {
		t.Fatalf("POST /logout = %d to %q, want 303 to /login", resp.StatusCode, location(t, resp))
	}
	cleared := false
	for _, set := range resp.Cookies() {
		if set.Name == "caravana_session" && set.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the response does not clear the cookie")
	}

	// Replaying the old cookie gets nowhere: the session ended on the server.
	resp, _ = e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
		t.Errorf("the old cookie after sign-out = %d to %q, want a redirect to /login", resp.StatusCode, location(t, resp))
	}
}

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	cases := map[string]map[string]string{
		"Sec-Fetch-Site cross-site": {"Sec-Fetch-Site": "cross-site"},
		"Sec-Fetch-Site same-site":  {"Sec-Fetch-Site": "same-site"},
		"Origin of another host":    {"Origin": "https://evil.example"},
	}

	t.Run("sign-in", func(t *testing.T) {
		for name, headers := range cases {
			e := newUIEnv(t, true, "")
			resp, body := e.do(t, request{method: http.MethodPost, path: "/login", headers: headers,
				form: url.Values{"email": {testEmail}, "password": {testPassword}}})
			if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 {
				t.Errorf("%s: %d with %d cookies, want 403 and none", name, resp.StatusCode, len(resp.Cookies()))
			}
			if !strings.Contains(body, "refused") || strings.Contains(body, "<form") {
				t.Errorf("%s: not the 403 page:\n%s", name, body)
			}
			if strings.Contains(e.Logs.String(), `"event":"signin"`) {
				t.Errorf("%s: a sign-in was attempted", name)
			}
		}
	})

	t.Run("sign-out changes nothing", func(t *testing.T) {
		for name, headers := range cases {
			e := newUIEnv(t, true, "")
			c := e.signIn(t, nil)
			resp, _ := e.do(t, request{method: http.MethodPost, path: "/logout", headers: headers, cookies: []*http.Cookie{c}})
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: %d, want 403", name, resp.StatusCode)
			}
			if resp, _ := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}}); resp.StatusCode != http.StatusOK {
				t.Errorf("%s: the session ended anyway (GET /settings = %d)", name, resp.StatusCode)
			}
		}
	})

	t.Run("same-origin and direct requests work", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		host := strings.TrimPrefix(e.URL, "http://")
		for name, headers := range map[string]map[string]string{
			"Sec-Fetch-Site same-origin": {"Sec-Fetch-Site": "same-origin"},
			"Sec-Fetch-Site none":        {"Sec-Fetch-Site": "none"},
			"Origin of this host":        {"Origin": "http://" + host},
			"no browser headers":         nil,
		} {
			resp, _ := e.do(t, request{method: http.MethodPost, path: "/login", headers: headers,
				form: url.Values{"email": {testEmail}, "password": {testPassword}}})
			if resp.StatusCode != http.StatusSeeOther {
				t.Errorf("%s: %d, want 303", name, resp.StatusCode)
			}
		}
	})
}

func TestSecurityHeaders(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)
	c := []*http.Cookie{e.Cookie}
	cross := map[string]string{"Sec-Fetch-Site": "cross-site"}
	reqs := []request{
		{path: "/login"}, {path: "/settings"}, {path: "/nope"}, {path: "/static/app.css"},
		{method: http.MethodPost, path: "/login", headers: cross},
		// Every console page and part, signed in and not, and the answers to refused posts.
		{path: "/"}, {path: "/apps"}, {path: "/apps/new"}, {path: "/apps/whoami"}, {path: "/apps/whoami/deployments"},
		{path: "/apps/whoami/configure"}, {path: "/apps/whoami/logs"}, {path: "/apps/nope"}, {path: "/account"},
		{path: "/parts/apps"}, {path: "/parts/activity"}, {path: "/parts/attention"}, {path: "/parts/apps/whoami/header"},
		{path: "/parts/apps/whoami/copies"}, {path: "/parts/apps/whoami/deployments"},
		{path: "/live"}, {path: "/live/apps/whoami"},
		{method: http.MethodPost, path: "/apps", headers: cross}, {method: http.MethodPost, path: "/apps/whoami/deploy", headers: cross},
		{method: http.MethodPost, path: "/apps/whoami/webhook", headers: cross},
		{method: http.MethodPost, path: "/apps/whoami/delete", headers: cross},
	}
	for _, p := range []string{"/apps", "/apps/new", "/apps/whoami", "/apps/whoami/logs", "/settings", "/parts/apps", "/live"} {
		reqs = append(reqs, request{path: p, cookies: c})
	}
	// The scripts and every other embedded file get the policy too.
	_ = fs.WalkDir(staticFS, "static", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			reqs = append(reqs, request{path: "/" + name})
		}
		return nil
	})
	for _, r := range reqs {
		var h http.Header
		if strings.HasPrefix(r.path, "/live") && r.cookies != nil {
			// A stream with a session stays open: read its headers and hang up.
			req, _ := http.NewRequest(http.MethodGet, e.URL+r.path, nil)
			req.AddCookie(r.cookies[0])
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET %s: %v", r.path, err)
			}
			resp.Body.Close()
			h = resp.Header
		} else {
			resp, _ := e.do(t, r)
			h = resp.Header
		}
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "form-action 'self'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s %s: Content-Security-Policy %q lacks %q", r.method, r.path, csp, want)
			}
		}
		// The policy is the 006 one, unchanged: nothing is loosened for htmx (research R3).
		if strings.Contains(csp, "unsafe") || strings.Contains(csp, "nonce") || strings.Contains(csp, "*") {
			t.Errorf("%s %s: Content-Security-Policy %q was loosened", r.method, r.path, csp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s %s: X-Content-Type-Options %q, Referrer-Policy %q", r.method, r.path,
				h.Get("X-Content-Type-Options"), h.Get("Referrer-Policy"))
		}
	}

	// Pages with a session in them are never cached.
	for _, path := range []string{"/settings", "/apps", "/apps/whoami", "/parts/apps"} {
		resp, _ := e.get(t, path)
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control on %s = %q, want no-store", path, got)
		}
	}
}

// The templates follow the rules of research R3, so the policy never needs
// loosening: no inline handlers or scripts, no style attributes or blocks.
func TestTemplatesFollowThePolicy(t *testing.T) {
	banned := []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`(?i)\bhx-on\b|\bhx-on[:-]`), "an hx-on attribute"},
		{regexp.MustCompile(`(?i)\sstyle\s*=`), "a style attribute"},
		{regexp.MustCompile(`(?i)<style\b`), "a <style> block"},
		{regexp.MustCompile(`(?i)<script\b[^>]*>`), "an inline script"}, // the ones with src are allowed below
		{regexp.MustCompile(`(?i)\son[a-z]+\s*=`), "an inline event handler"},
		{regexp.MustCompile(`(?i)javascript:`), "a javascript: URL"},
		{regexp.MustCompile(`hx-trigger="[^"]*[\[{]`), "an hx-trigger filter, which needs eval"},
		{regexp.MustCompile(`hx-vals="[^"]*js:`), "a js: value in hx-vals"},
	}
	files, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil || len(files) < 8 {
		t.Fatalf("found %d templates (%v)", len(files), err)
	}
	for _, f := range files {
		raw, err := fs.ReadFile(templatesFS, f)
		if err != nil {
			t.Fatal(err)
		}
		// Template comments may mention the rules; they never reach a page.
		src := regexp.MustCompile(`(?s)\{\{/\*.*?\*/\}\}`).ReplaceAllString(string(raw), "")
		for _, b := range banned {
			for _, m := range b.re.FindAllString(src, -1) {
				if b.what == "an inline script" && strings.Contains(m, ` src="{{asset "js/`) && strings.Contains(m, " defer") {
					continue
				}
				t.Errorf("%s has %s: %s", f, b.what, m)
			}
		}
	}

	// The scan can fail: it catches each rule in a sample.
	for _, bad := range []string{`<a hx-on:click="x()">`, `<p style="color:red">`, `<style>a{}</style>`, `<script>x()</script>`,
		`<a onclick="x()">`, `<a href="javascript:x()">`, `<div hx-trigger="click[ctrlKey]">`} {
		hit := false
		for _, b := range banned {
			hit = hit || b.re.MatchString(bad)
		}
		if !hit {
			t.Errorf("the scan doesn't catch %s", bad)
		}
	}
}

func TestStaticFiles(t *testing.T) {
	e := newUIEnv(t, true, "")

	// The page links the stylesheet by its content hash.
	_, body := e.do(t, request{path: "/login"})
	i := strings.Index(body, `href="/static/app.css?v=`)
	if i < 0 {
		t.Fatalf("the page does not link /static/app.css?v=…:\n%s", body)
	}
	href := body[i+len(`href="`):]
	href = href[:strings.Index(href, `"`)]

	resp, css := e.do(t, request{path: href})
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("GET %s = %d %q", href, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=31536000") || !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want a year, immutable", cc)
	}
	if !strings.Contains(css, "font-face") {
		t.Error("app.css has no @font-face rules")
	}

	// Without the content hash it must be revalidated.
	resp, _ = e.do(t, request{path: "/static/app.css"})
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control without ?v = %q, want no-cache", cc)
	}

	resp, font := e.do(t, request{path: "/static/fonts/IBMPlexMono-Regular-Latin1.woff2"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "font/woff2" || len(font) < 1000 {
		t.Errorf("font = %d %q (%d bytes)", resp.StatusCode, resp.Header.Get("Content-Type"), len(font))
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("font Cache-Control = %q, want a year", cc)
	}

	// Directories are not listed.
	for _, p := range []string{"/static/", "/static/fonts/", "/static/fonts"} {
		if resp, body := e.do(t, request{path: p}); resp.StatusCode == http.StatusOK || strings.Contains(body, "woff2") {
			t.Errorf("GET %s listed the directory (%d)", p, resp.StatusCode)
		}
	}
}

func TestNotFound(t *testing.T) {
	e := newUIEnv(t, true, "")
	resp, body := e.do(t, request{path: "/nope"})
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "doesn&#39;t exist") {
		t.Errorf("GET /nope = %d:\n%s", resp.StatusCode, body)
	}
}

func TestSafeNext(t *testing.T) {
	for next, want := range map[string]string{
		"":                       "",
		"/account":               "/account",
		"/account?x=1#frag":      "/account?x=1#frag",
		"/apps/whoami":           "/apps/whoami",
		"//evil.example":         "",
		`/\evil.example`:         "",
		"https://evil.example/":  "",
		"account":                "",
		"/v1/apps":               "",
		"/v1":                    "",
		"/v1?x":                  "",
		"/login":                 "",
		"/logout":                "",
		"/a\nb":                  "",
		"/%2F%2Fevil.example":    "/%2F%2Fevil.example", // still a path on this host
		"/path with a space":     "/path with a space",
		"/\x00":                  "", // a control character is refused
		"/..;/evil":              "/..;/evil",
		"/?next=https://x.test/": "/?next=https://x.test/",
	} {
		if got := safeNext(next); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", next, got, want)
		}
	}
}

// failSignIn posts a wrong password and returns the page body.
func (e *uiEnv) failSignIn(t *testing.T, headers map[string]string) (*http.Response, string) {
	t.Helper()
	return e.do(t, request{method: http.MethodPost, path: "/login", headers: headers,
		form: url.Values{"email": {testEmail}, "password": {"not the password"}}})
}

func TestSignInLimitMessages(t *testing.T) {
	const dont = "That email and password don&#39;t match."

	t.Run("tries left, then the wait", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		for i, want := range []string{
			dont + " 4 tries left before a 15-minute wait.",
			dont + " 3 tries left before a 15-minute wait.",
			dont + " 2 tries left before a 15-minute wait.",
			dont + " 1 try left before a 15-minute wait.",
		} {
			resp, body := e.failSignIn(t, nil)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, want) {
				t.Errorf("failure %d: %d, want the sentence %q:\n%s", i+1, resp.StatusCode, want, body)
			}
			if !strings.Contains(body, "cv-mark-warning") || !strings.Contains(body, `role="alert"`) {
				t.Errorf("failure %d: the sentence lacks its warning mark or role=alert:\n%s", i+1, body)
			}
		}

		// The fifth locks: a failed mark and the wait.
		resp, body := e.failSignIn(t, nil)
		want := "Too many failed sign-ins from this address. Try again in 15 minutes."
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, want) || !strings.Contains(body, "cv-mark-failed") ||
			strings.Contains(body, "cv-mark-warning") {
			t.Errorf("the fifth failure: %d, want %q with a failed mark:\n%s", resp.StatusCode, want, body)
		}

		// Even the right password is refused, and the minutes round up.
		e.Clock.Advance(2*time.Minute + 30*time.Second)
		resp, body = e.do(t, request{method: http.MethodPost, path: "/login",
			form: url.Values{"email": {testEmail}, "password": {testPassword}}})
		if len(resp.Cookies()) != 0 || !strings.Contains(body, "Try again in 13 minutes.") {
			t.Errorf("the right password while locked: cookies %v, body:\n%s", resp.Cookies(), body)
		}
		e.Clock.Advance(11*time.Minute + 45*time.Second) // 45 seconds left
		if _, body = e.do(t, request{method: http.MethodPost, path: "/login",
			form: url.Values{"email": {testEmail}, "password": {testPassword}}}); !strings.Contains(body, "Try again in 1 minute.") {
			t.Errorf("with under a minute left:\n%s", body)
		}

		// After the wait, sign-in works again.
		e.Clock.Advance(time.Minute)
		c := e.signIn(t, nil)
		if c == nil {
			t.Fatal("no cookie after the wait")
		}
	})

	t.Run("a locked page keeps the email and still offers the form", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		var body string
		for range 5 {
			_, body = e.failSignIn(t, nil)
		}
		if !strings.Contains(body, `value="`+testEmail+`"`) || !strings.Contains(body, "<form") {
			t.Errorf("the locked page lost the email or the form:\n%s", body)
		}
	})
}

func TestSignInLimitIsPerClientAddress(t *testing.T) {
	t.Run("behind a trusted proxy, forwarded addresses are limited separately", func(t *testing.T) {
		e := newUIEnv(t, true, "127.0.0.1")
		from := func(addr string) map[string]string { return map[string]string{"X-Forwarded-For": addr} }
		for range 5 {
			e.failSignIn(t, from("198.51.100.1"))
		}
		if _, body := e.failSignIn(t, from("198.51.100.1")); !strings.Contains(body, "Too many failed sign-ins") {
			t.Errorf("the first client is not locked:\n%s", body)
		}
		// A second client behind the same proxy is unaffected.
		if _, body := e.failSignIn(t, from("198.51.100.2")); !strings.Contains(body, "4 tries left") {
			t.Errorf("the second client was affected by the first:\n%s", body)
		}
		if c := e.signIn(t, from("198.51.100.3")); c == nil {
			t.Error("a third client could not sign in")
		}
	})

	t.Run("from an untrusted peer a spoofed X-Forwarded-For doesn't change the address", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		for i := range 5 {
			e.failSignIn(t, map[string]string{"X-Forwarded-For": "198.51.100." + string(rune('1'+i))})
		}
		// A fresh forwarded address doesn't escape the lock.
		resp, body := e.do(t, request{method: http.MethodPost, path: "/login",
			headers: map[string]string{"X-Forwarded-For": "203.0.113.200"},
			form:    url.Values{"email": {testEmail}, "password": {testPassword}}})
		if len(resp.Cookies()) != 0 || !strings.Contains(body, "Too many failed sign-ins") {
			t.Errorf("a spoofed X-Forwarded-For escaped the lock: cookies %v\n%s", resp.Cookies(), body)
		}
	})
}

var tokenInPage = regexp.MustCompile(`cvat_[A-Za-z0-9_-]{43}`)

// post sends a same-origin form post with the session cookie.
func (e *uiEnv) post(t *testing.T, c *http.Cookie, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	return e.do(t, request{method: http.MethodPost, path: path, form: form, cookies: []*http.Cookie{c},
		headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
}

func TestChangePasswordPage(t *testing.T) {
	ctx := context.Background()
	const newPassword = "a brand new password"

	t.Run("success: back to the page with a confirmation, other browsers signed out", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c, other := e.signIn(t, nil), e.signIn(t, nil)

		resp, _ := e.post(t, c, "/settings/password", url.Values{"currentPassword": {testPassword}, "newPassword": {newPassword}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings?password=changed" {
			t.Fatalf("POST = %d to %q, want 303 to /settings?password=changed", resp.StatusCode, location(t, resp))
		}
		resp, body := e.do(t, request{path: location(t, resp), cookies: []*http.Cookie{c}})
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Password changed. Other browsers were signed out.") ||
			!strings.Contains(body, "cv-mark-healthy") {
			t.Errorf("the confirmation is missing:\n%s", body)
		}
		if resp, _ := e.do(t, request{path: "/settings", cookies: []*http.Cookie{other}}); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("another browser's session survived: %d", resp.StatusCode)
		}
		if _, err := e.Auth.SignIn(ctx, testEmail, newPassword, "203.0.113.9"); err != nil {
			t.Errorf("the new password does not sign in: %v", err)
		}
	})

	t.Run("a wrong current password and a short new one name their field", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		for name, tc := range map[string]struct {
			form url.Values
			want string
		}{
			"wrong current": {url.Values{"currentPassword": {"not the password"}, "newPassword": {newPassword}}, "That isn&#39;t the current password."},
			"short new":     {url.Values{"currentPassword": {testPassword}, "newPassword": {"short"}}, "The new password must be at least 12 characters."},
		} {
			resp, body := e.post(t, c, "/settings/password", tc.form)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, tc.want) || !strings.Contains(body, `role="alert"`) ||
				!strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, "cv-mark-failed") {
				t.Errorf("%s: %d, want %q as an announced error on the field:\n%s", name, resp.StatusCode, tc.want, body)
			}
			for _, secret := range []string{testPassword, newPassword, "not the password"} {
				if strings.Contains(body, secret) {
					t.Errorf("%s: a password came back in the page", name)
				}
			}
		}
		// Nothing changed.
		if _, err := e.Auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9"); err != nil {
			t.Errorf("the password changed on refused posts: %v", err)
		}
	})
}

func TestTokensOnThePage(t *testing.T) {
	ctx := context.Background()

	t.Run("create shows the token once, and a reload doesn't", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)

		resp, body := e.post(t, c, "/settings/tokens", url.Values{"name": {"ci-github"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /settings/tokens = %d, want 200 (no redirect)", resp.StatusCode)
		}
		token := tokenInPage.FindString(body)
		if token == "" || !strings.Contains(body, "It won't be shown again.") {
			t.Fatalf("the new token is not shown with its warning:\n%s", body)
		}
		if !strings.Contains(body, `-H "Authorization: Bearer `+token+`"`) || !strings.Contains(body, e.URL+"/v1/tokens") {
			t.Errorf("the curl example is missing:\n%s", body)
		}
		if _, err := e.Auth.CheckAPIToken(ctx, token); err != nil {
			t.Errorf("the token shown does not work: %v", err)
		}

		_, body = e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
		if tokenInPage.MatchString(body) || strings.Contains(body, "be shown again") {
			t.Errorf("a reload shows the token again:\n%s", body)
		}
		if !strings.Contains(body, "ci-github") || !strings.Contains(body, "cv-pill-healthy") {
			t.Errorf("the token is not listed as active:\n%s", body)
		}
	})

	t.Run("the list shows states with their marks", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		soon := e.Clock.Now().Add(time.Hour)
		if _, err := e.Auth.CreateAPIToken(ctx, "will-expire", &soon); err != nil {
			t.Fatal(err)
		}
		gone, _ := e.Auth.CreateAPIToken(ctx, "gone", nil)
		if err := e.Auth.RevokeAPIToken(ctx, gone.Info.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Auth.CreateAPIToken(ctx, "live", nil); err != nil {
			t.Fatal(err)
		}
		e.Clock.Advance(2 * time.Hour)

		_, body := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
		for _, want := range []string{
			"cv-pill-healthy", "cv-mark-healthy", ">active<",
			"cv-pill-idle", "cv-mark-idle", ">expired<",
			"cv-pill-failed", "cv-mark-failed", ">revoked<",
			"2 hours ago", // created, relative
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the token list lacks %q:\n%s", want, body)
			}
		}
		// Only active rows offer Revoke.
		if got := strings.Count(body, ">Revoke<"); got != 1 {
			t.Errorf("%d Revoke buttons, want 1 (the active token)", got)
		}
	})

	t.Run("revoke redirects and the row shows revoked", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		created, _ := e.Auth.CreateAPIToken(ctx, "victim", nil)

		resp, _ := e.post(t, c, "/settings/tokens/"+created.Info.ID+"/revoke", url.Values{})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings" {
			t.Fatalf("revoke = %d to %q, want 303 to /settings", resp.StatusCode, location(t, resp))
		}
		if _, err := e.Auth.CheckAPIToken(ctx, created.Token); err == nil {
			t.Error("the revoked token still works")
		}
		_, body := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
		if !strings.Contains(body, "cv-pill-failed") || strings.Contains(body, ">Revoke<") {
			t.Errorf("the row does not show revoked:\n%s", body)
		}
		// An unknown ID is not an error page: the list is simply unchanged.
		resp, _ = e.post(t, c, "/settings/tokens/p4nzr2t6v3x7z5bqd7f4h6j3kq/revoke", url.Values{})
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("revoking an unknown ID = %d, want 303", resp.StatusCode)
		}
	})

	t.Run("refused creates say why, under the field", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		if _, err := e.Auth.CreateAPIToken(ctx, "taken", nil); err != nil {
			t.Fatal(err)
		}
		yesterday := e.Clock.Now().Add(-24 * time.Hour).Format("2006-01-02")
		for name, tc := range map[string]struct {
			form url.Values
			want string
		}{
			"name taken":  {url.Values{"name": {"taken"}}, "An active token already has that name."},
			"bad name":    {url.Values{"name": {"has space"}}, "The name may only use letters"},
			"empty name":  {url.Values{"name": {"  "}}, "The name must be 1 to 64 characters."},
			"past expiry": {url.Values{"name": {"x"}, "expires": {yesterday}}, "The expiry must be in the future."},
			"not a date":  {url.Values{"name": {"x"}, "expires": {"tomorrow"}}, "The expiry must be a date."},
		} {
			resp, body := e.post(t, c, "/settings/tokens", tc.form)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, tc.want) || !strings.Contains(body, `role="alert"`) {
				t.Errorf("%s: %d, want %q:\n%s", name, resp.StatusCode, tc.want, body)
			}
			if tokenInPage.MatchString(body) {
				t.Errorf("%s: a token was created and shown", name)
			}
		}
		// The name stays in the field after a refusal.
		_, body := e.post(t, c, "/settings/tokens", url.Values{"name": {"taken"}})
		if !strings.Contains(body, `value="taken"`) {
			t.Errorf("the refused name was not kept:\n%s", body)
		}
	})

	t.Run("an expiry date becomes the end of that day, UTC", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		day := e.Clock.Now().Add(48 * time.Hour).Format("2006-01-02")
		if resp, body := e.post(t, c, "/settings/tokens", url.Values{"name": {"dated"}, "expires": {day}}); resp.StatusCode != http.StatusOK || !tokenInPage.MatchString(body) {
			t.Fatalf("create with an expiry = %d:\n%s", resp.StatusCode, body)
		}
		infos, _ := e.Auth.ListAPITokens(ctx)
		want, _ := time.Parse("2006-01-02 15:04:05", day+" 23:59:59")
		if len(infos) != 1 || infos[0].ExpiresAt == nil || !infos[0].ExpiresAt.Equal(want) {
			t.Errorf("tokens = %+v, want one expiring %v", infos, want)
		}
	})
}

// US7: the 006 account page now lives in the shell as Settings.
func TestSettingsPage(t *testing.T) {
	ctx := context.Background()

	t.Run("the old /account address is a permanent redirect to /settings", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		for name, cookies := range map[string][]*http.Cookie{"signed in": {c}, "signed out": nil} {
			resp, _ := e.do(t, request{path: "/account", cookies: cookies})
			if resp.StatusCode != http.StatusMovedPermanently || location(t, resp) != "/settings" {
				t.Errorf("%s: GET /account = %d to %q, want 301 to /settings", name, resp.StatusCode, location(t, resp))
			}
		}
	})

	t.Run("it sits in the shell with Settings marked current", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		_, body := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
		for _, want := range []string{
			`<a href="/settings" class="cv-nav-item" aria-current="page">`,
			`<a href="/apps" class="cv-nav-item">`, // not current
			`class="cv-shell"`,
			`aria-current="page">Settings</span>`, // the breadcrumb
			`action="/logout"`,                    // Sign out stays in the sidebar's account row
			"Signed in as",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, "sse-connect") {
			t.Error("Settings opens a live stream")
		}
	})

	t.Run("nothing still points at /account", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		for name, body := range map[string]string{
			"settings":  func() string { _, b := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}}); return b }(),
			"not found": func() string { _, b := e.do(t, request{path: "/nope"}); return b }(),
		} {
			if strings.Contains(body, `"/account`) {
				t.Errorf("%s page still links to /account", name)
			}
		}
	})

	t.Run("revoke sits behind a confirmation", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		created, err := e.Auth.CreateAPIToken(ctx, "victim", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, body := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
		form := `<form method="post" action="/settings/tokens/` + created.Info.ID + `/revoke"`
		i := strings.Index(body, form)
		if i < 0 {
			t.Fatalf("no revoke form:\n%s", body)
		}
		if open, closed := strings.LastIndex(body[:i], "<details"), strings.LastIndex(body[:i], "</details>"); open < 0 || open < closed {
			t.Error("the revoke form isn't inside a <details>")
		}
		if regexp.MustCompile(`<details[^>]* open`).MatchString(body) {
			t.Error("a confirmation starts open")
		}
		if got := text(body); !strings.Contains(got, "can't be used again") {
			t.Errorf("the confirmation doesn't say what happens:\n%s", got)
		}
		// The summary is the one "Revoke" button, with the token's name for screen readers.
		if !strings.Contains(body, `aria-label="Revoke token victim"`) {
			t.Errorf("the revoke control doesn't name its token:\n%s", body)
		}
	})

	t.Run("the new token has a copy button, hidden until script reveals it", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		_, body := e.post(t, c, "/settings/tokens", url.Values{"name": {"ci"}})
		m := regexp.MustCompile(`<button[^>]*data-copy="([^"]+)"[^>]*>`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no copy button on the new token panel:\n%s", body)
		}
		if !strings.Contains(m[0], " hidden") {
			t.Errorf("the copy button %q works only with script, so it must start hidden", m[0])
		}
		// It copies the token itself, which sits in the element it names.
		if !regexp.MustCompile(`<code[^>]*id="` + m[1] + `"[^>]*>cvat_`).MatchString(body) {
			t.Errorf("the copy button names #%s, which isn't the token:\n%s", m[1], body)
		}
	})
}

func TestAccountFormsWorkWithoutJavaScript(t *testing.T) {
	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)
	_, body := e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
	// Scripts only enhance (the shared ones, from /static/js); no inline script, no onclick.
	for _, tag := range regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(body, -1) {
		if !strings.Contains(tag, ` src="/static/js/`) {
			t.Errorf("the account page has an inline script: %s", tag)
		}
	}
	if strings.Contains(strings.ToLower(body), " onclick=") {
		t.Errorf("the account page has an onclick handler:\n%s", body)
	}
	// Every form is a plain post to a path this server serves.
	forms := regexp.MustCompile(`<form method="post" action="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(forms) < 3 {
		t.Fatalf("found %d forms, want sign out, password and create token at least", len(forms))
	}
	for _, f := range forms {
		if !strings.HasPrefix(f[1], "/") {
			t.Errorf("form action %q is not a path on this server", f[1])
		}
	}
}

// plainForm is a <form> as a browser without script would send it: the method,
// the action and the value of every named field, as it stands in the markup.
type plainForm struct {
	Method, Action string
	Fragment       string // what follows "#" in the action: where the answer scrolls to
	Values         url.Values
	Disabled       []string // fields or buttons the markup itself disables
	HTMX           bool     // has hx-post, which only enhances the plain post
}

// formsIn reads the forms of a page like a browser would fill them in: each
// named input with its value, a submit button's own name and value excluded.
func formsIn(body string) []plainForm {
	var forms []plainForm
	for _, m := range regexp.MustCompile(`(?s)<form\b([^>]*)>(.*?)</form>`).FindAllStringSubmatch(body, -1) {
		attr := func(tag, name string) string {
			v := regexp.MustCompile(`\s` + name + `="([^"]*)"`).FindStringSubmatch(tag)
			if v == nil {
				return ""
			}
			return html.UnescapeString(v[1])
		}
		action, fragment, _ := strings.Cut(attr(m[1], "action"), "#")
		f := plainForm{Method: strings.ToLower(attr(m[1], "method")), Action: action, Fragment: fragment, Values: url.Values{}, HTMX: strings.Contains(m[1], "hx-post")}
		for _, in := range regexp.MustCompile(`(?i)<(?:input|button)\b[^>]*>`).FindAllString(m[2], -1) {
			if regexp.MustCompile(`\sdisabled\b`).MatchString(in) {
				f.Disabled = append(f.Disabled, in)
			}
			if name := attr(in, "name"); name != "" && attr(in, "type") != "submit" {
				f.Values.Set(name, attr(in, "value"))
			}
		}
		// A textarea's value is its text; the first line break is not part of it.
		for _, ta := range regexp.MustCompile(`(?s)<textarea\b([^>]*)>(.*?)</textarea>`).FindAllStringSubmatch(m[2], -1) {
			if name := attr(ta[1], "name"); name != "" {
				f.Values.Set(name, strings.TrimPrefix(html.UnescapeString(ta[2]), "\n"))
			}
		}
		forms = append(forms, f)
	}
	return forms
}

// formWith finds the form on a page whose action is action.
func formWith(t *testing.T, body, action string) plainForm {
	t.Helper()
	for _, f := range formsIn(body) {
		if f.Action == action {
			return f
		}
	}
	t.Fatalf("no form posts to %s in:\n%s", action, body)
	return plainForm{}
}

// submit sends the form as a browser does without script: urlencoded, the
// markup's own values with these overrides, and the headers of a same-site
// click (Sec-Fetch-Site, no HX-Request).
func (e *consoleEnv) submit(t *testing.T, f plainForm, set url.Values) (*http.Response, string) {
	t.Helper()
	if f.Method != "post" {
		t.Fatalf("form %s is a %q, want post", f.Action, f.Method)
	}
	if len(f.Disabled) > 0 {
		t.Errorf("form %s has controls that are disabled without script: %v", f.Action, f.Disabled)
	}
	form := url.Values{}
	for k, v := range f.Values {
		form[k] = v
	}
	for k, v := range set {
		form[k] = v
	}
	return e.do(t, request{method: http.MethodPost, path: f.Action, form: form, cookies: []*http.Cookie{e.Cookie},
		headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
}

// Every form of the console is a plain post: the page's own markup is enough to
// create an app, deploy, edit its definition, set a variable, a secret and a domain, make and revoke a webhook token, delete an app and
// change the Settings, with no script and with nothing but same-site headers.
func TestConsoleFormsWorkWithoutJavaScript(t *testing.T) {
	e := newConsoleEnv(t)
	ctx := context.Background()

	t.Run("New app", func(t *testing.T) {
		_, page := e.get(t, "/apps/new")
		f := formWith(t, page, "/apps")
		if !f.HTMX {
			t.Error("the New app form doesn't post in place when script runs")
		}
		resp, body := e.submit(t, f, url.Values{"name": {"plain"}, "image": {"traefik/whoami:latest"}, "port": {"80"}, "domain": {"plain.example.com"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/plain/deployments" {
			t.Fatalf("POST /apps = %d to %q, want 303 to /apps/plain/deployments:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if _, err := e.Store.GetApp(ctx, "plain"); err != nil {
			t.Errorf("the app wasn't stored: %v", err)
		}
		// A refusal is the page again, with the error where a reader finds it.
		resp, body = e.submit(t, f, url.Values{"name": {"plain"}, "image": {"traefik/whoami:latest"}})
		if resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `role="alert"`) {
			t.Errorf("a second app named plain = %d, want the form again with an alert:\n%s", resp.StatusCode, body)
		}
	})

	t.Run("Deploy", func(t *testing.T) {
		_, page := e.get(t, "/apps/plain/deployments")
		f := formWith(t, page, "/apps/plain/deploy")
		resp, body := e.submit(t, f, nil)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/plain/deployments" {
			t.Fatalf("POST deploy = %d to %q, want 303 to the Deployments tab:\n%s", resp.StatusCode, location(t, resp), body)
		}
	})

	t.Run("Configure", func(t *testing.T) {
		_, page := e.get(t, "/apps/plain/configure")
		f := formWith(t, page, "/apps/plain/configure")
		if f.HTMX || f.Values.Get("revision") == "" {
			t.Errorf("the Configure form is a plain post with a revision: htmx %v, revision %q", f.HTMX, f.Values.Get("revision"))
		}
		resp, body := e.submit(t, f, url.Values{"svc.plain.memory": {"100"}})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/plain/deployments#deployment-") {
			t.Fatalf("POST configure = %d to %q, want 303 to the new deployment's entry:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if got := e.storedApp(t, "plain").Services[0].Resources.MemoryMB; got != 100 {
			t.Errorf("memory = %d, want 100", got)
		}
		// The same form again is stale now: refused with the page, not lost.
		resp, body = e.submit(t, f, nil)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "This app changed since you opened the form.") {
			t.Errorf("a stale POST = %d, want 409 with the page:\n%s", resp.StatusCode, text(body))
		}
	})

	// Configure's other pages, as plain posts too: a variable, then a secret, then a domain added and removed.
	t.Run("Variables", func(t *testing.T) {
		_, page := e.get(t, "/apps/plain/variables")
		f := formWith(t, page, "/apps/plain/variables")
		if f.HTMX || f.Values.Get("revision") == "" {
			t.Errorf("the Variables form is a plain post with a revision: htmx %v, revision %q", f.HTMX, f.Values.Get("revision"))
		}
		resp, body := e.submit(t, f, url.Values{"name": {"MODE"}, "value": {"plain"}, "secret": {""}}) // formsIn sends a checkbox whether or not it is checked
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/plain/deployments#deployment-") {
			t.Fatalf("POST variables = %d to %q, want 303 to the new deployment's entry:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if got := e.storedApp(t, "plain").Services[0].Env; len(got) != 1 || got[0].Name != "MODE" || got[0].Value != "plain" {
			t.Errorf("env = %+v, want MODE=plain", got)
		}
		_, page = e.get(t, "/apps/plain/variables")
		editor := formWith(t, page, "/apps/plain/variables/env")
		resp, body = e.submit(t, editor, url.Values{"env": {"MODE=plain\nLEVEL=info"}})
		if resp.StatusCode != http.StatusSeeOther || len(e.storedApp(t, "plain").Services[0].Env) != 2 {
			t.Errorf("POST the .env editor = %d, env %+v:\n%s", resp.StatusCode, e.storedApp(t, "plain").Services[0].Env, text(body))
		}
	})

	t.Run("Domains", func(t *testing.T) {
		_, page := e.get(t, "/apps/plain/domains")
		resp, body := e.submit(t, formWith(t, page, "/apps/plain/domains"), url.Values{"host": {"www.plain.example.com"}})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/plain/deployments#deployment-") {
			t.Fatalf("POST domains = %d to %q, want 303 to the new deployment's entry:\n%s", resp.StatusCode, location(t, resp), body)
		}
		_, page = e.get(t, "/apps/plain/domains")
		remove := formWith(t, page, "/apps/plain/domains/remove")
		resp, body = e.submit(t, remove, url.Values{"host": {"www.plain.example.com"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST domains/remove = %d:\n%s", resp.StatusCode, text(body))
		}
		for _, d := range e.storedApp(t, "plain").Services[0].Domains {
			if d.Host == "www.plain.example.com" {
				t.Error("the domain wasn't removed")
			}
		}
	})

	t.Run("Webhook", func(t *testing.T) {
		_, page := e.get(t, "/apps/plain/configure")
		resp, body := e.submit(t, formWith(t, page, "/apps/plain/webhook"), nil)
		if resp.StatusCode != http.StatusOK || !regexp.MustCompile(`cvwh_[A-Za-z0-9_-]+`).MatchString(body) {
			t.Fatalf("POST webhook = %d, want 200 with the token shown once:\n%s", resp.StatusCode, body)
		}
		_, page = e.get(t, "/apps/plain/configure")
		if regexp.MustCompile(`cvwh_`).MatchString(page) {
			t.Error("the token is shown again on a later visit")
		}
		resp, body = e.submit(t, formWith(t, page, "/apps/plain/webhook/revoke"), nil)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/plain/configure" {
			t.Fatalf("POST revoke = %d to %q, want 303 to the Configure tab:\n%s", resp.StatusCode, location(t, resp), body)
		}
	})

	t.Run("Settings", func(t *testing.T) {
		_, page := e.get(t, "/settings")
		resp, body := e.submit(t, formWith(t, page, "/settings/tokens"), url.Values{"name": {"plain-ci"}})
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "cvat_") {
			t.Fatalf("POST /settings/tokens = %d, want 200 with the token:\n%s", resp.StatusCode, body)
		}
		_, page = e.get(t, "/settings")
		var revoke plainForm
		for _, f := range formsIn(page) {
			if strings.HasSuffix(f.Action, "/revoke") {
				revoke = f
			}
		}
		if revoke.Action == "" {
			t.Fatalf("no revoke form for the new token:\n%s", page)
		}
		if resp, body := e.submit(t, revoke, nil); resp.StatusCode >= 400 {
			t.Errorf("POST %s = %d:\n%s", revoke.Action, resp.StatusCode, body)
		}
		resp, body = e.submit(t, formWith(t, page, "/settings/password"), url.Values{"currentPassword": {testPassword}, "newPassword": {"another long password"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings?password=changed" {
			t.Errorf("POST /settings/password = %d to %q, want 303 to /settings?password=changed:\n%s", resp.StatusCode, location(t, resp), body)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		_, page := e.get(t, "/apps/plain/configure")
		f := formWith(t, page, "/apps/plain/delete")
		resp, body := e.submit(t, f, url.Values{"confirm": {"nope"}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("POST delete with the wrong name = %d, want 422:\n%s", resp.StatusCode, body)
		}
		resp, body = e.submit(t, f, url.Values{"confirm": {"plain"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps?deleted=plain" {
			t.Fatalf("POST delete = %d to %q, want 303 to /apps?deleted=plain:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if _, err := e.Store.GetApp(ctx, "plain"); err == nil {
			t.Error("the app is still stored")
		}
	})
}

func TestAccountPostsAreProtected(t *testing.T) {
	ctx := context.Background()
	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)
	victim, _ := e.Auth.CreateAPIToken(ctx, "victim", nil)

	cross := map[string]string{"Sec-Fetch-Site": "cross-site"}
	for path, form := range map[string]url.Values{
		"/settings/password": {"currentPassword": {testPassword}, "newPassword": {"a brand new password"}},
		"/settings/tokens":   {"name": {"forged"}},
		"/settings/tokens/" + victim.Info.ID + "/revoke": {},
	} {
		resp, body := e.do(t, request{method: http.MethodPost, path: path, form: form, headers: cross, cookies: []*http.Cookie{c}})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "refused") {
			t.Errorf("cross-site POST %s = %d, want the 403 page", path, resp.StatusCode)
		}
	}
	// Nothing changed.
	if _, err := e.Auth.CheckAPIToken(ctx, victim.Token); err != nil {
		t.Errorf("a cross-site post revoked the token: %v", err)
	}
	infos, _ := e.Auth.ListAPITokens(ctx)
	if len(infos) != 1 {
		t.Errorf("a cross-site post created a token: %+v", infos)
	}
	if _, err := e.Auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9"); err != nil {
		t.Errorf("a cross-site post changed the password: %v", err)
	}

	// Without a session the posts go to /login, whoever sends them.
	for _, path := range []string{"/settings/password", "/settings/tokens", "/settings/tokens/x/revoke"} {
		resp, _ := e.do(t, request{method: http.MethodPost, path: path, form: url.Values{},
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("POST %s without a session = %d to %q, want a redirect to /login", path, resp.StatusCode, location(t, resp))
		}
	}
}

func TestRelative(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-10 * time.Second), "just now"},
		{now.Add(-time.Minute), "1 minute ago"},
		{now.Add(-5 * time.Minute), "5 minutes ago"},
		{now.Add(-3 * time.Hour), "3 hours ago"},
		{now.Add(-47 * time.Hour), "47 hours ago"},
		{now.Add(-72 * time.Hour), "3 days ago"},
		{now.Add(30 * time.Minute), "in 30 minutes"},
		{now.Add(24 * time.Hour), "in 24 hours"},
		{now.Add(10 * 24 * time.Hour), "in 10 days"},
		{now.Add(-90 * 24 * time.Hour), "2026-07-06"},
		{now.Add(120 * 24 * time.Hour), "2027-02-01"},
	} {
		if got := relative(now, tc.t); got != tc.want {
			t.Errorf("relative(%v) = %q, want %q", tc.t.Sub(now), got, tc.want)
		}
	}
}

// pagesToCheck renders every page the UI has, in the states it shows.
func pagesToCheck(t *testing.T) map[string]string {
	t.Helper()
	pages := map[string]string{}

	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)
	_, pages["login"] = e.do(t, request{path: "/login"})
	_, pages["login, failed"] = e.failSignIn(t, nil)
	_, pages["account"] = e.do(t, request{path: "/settings", cookies: []*http.Cookie{c}})
	_, pages["account, new token"] = e.post(t, c, "/settings/tokens", url.Values{"name": {"ci"}})
	_, pages["account, errors"] = e.post(t, c, "/settings/password", url.Values{"currentPassword": {"x"}, "newPassword": {"y"}})
	_, pages["not found"] = e.do(t, request{path: "/nope"})
	_, pages["refused"] = e.do(t, request{method: http.MethodPost, path: "/login", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})

	noAdmin := newUIEnv(t, false, "")
	_, pages["login, no admin"] = noAdmin.do(t, request{path: "/login"})

	// Every console page and part, with no app and with four, and the pages the forms answer with.
	empty := newConsoleEnv(t)
	for name, body := range consolePages(t, empty) {
		pages["empty: "+name] = body
	}
	console, _ := populatedConsole(t)
	for name, body := range consolePages(t, console, "whoami", "blog", "api", "shop") {
		pages[name] = body
	}
	for name, body := range formPages(t, console, "shop") {
		pages[name] = body
	}
	for name, body := range projectPages(t) {
		pages[name] = body
	}
	for name, body := range pages {
		if !isPart(name) && !strings.Contains(body, "<html") {
			t.Fatalf("page %q did not render:\n%s", name, body)
		}
	}
	return pages
}

// isPart says whether a page of pagesToCheck is a fragment of /parts/…, which has
// no document around it.
func isPart(name string) bool {
	return strings.Contains(name, "/parts/")
}

func TestPagesAskNothingOfOtherHosts(t *testing.T) {
	remote := regexp.MustCompile(`(?i)\b(?:href|src|action|srcset|poster|data)\s*=\s*["']?\s*(?:https?:)?//`)
	elements := regexp.MustCompile(`(?i)<(iframe|img|object|embed|video|audio|source|form[^>]*\saction="https?:)`)
	scripts := regexp.MustCompile(`(?i)<script\b[^>]*>`)
	anchors := regexp.MustCompile(`(?i)<a\b[^>]*>`)
	for name, body := range pagesToCheck(t) {
		// An app's address is a link the admin may follow; it loads nothing.
		body = anchors.ReplaceAllString(body, "<a>")
		if remote.MatchString(body) {
			t.Errorf("page %q references another host:\n%s", name, remote.FindString(body))
		}
		if m := elements.FindString(body); m != "" {
			t.Errorf("page %q has %q: pages ship no frames or images", name, m)
		}
		// The only scripts are the console's own, from this server, deferred.
		for _, tag := range scripts.FindAllString(body, -1) {
			if !strings.Contains(tag, `src="/static/js/`) || !strings.Contains(tag, " defer") {
				t.Errorf("page %q has an unexpected script: %s", name, tag)
			}
		}
		if strings.Contains(strings.ToLower(body), "<link") {
			// The one stylesheet, from this server.
			for _, link := range regexp.MustCompile(`(?i)<link[^>]*>`).FindAllString(body, -1) {
				if !strings.Contains(link, `href="/static/app.css?v=`) {
					t.Errorf("page %q has an unexpected <link>: %s", name, link)
				}
			}
		}
	}

	// The stylesheet loads only its own fonts: no @import, every url() local.
	e := newUIEnv(t, true, "")
	_, css := e.do(t, request{path: "/static/app.css"})
	css = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "") // the license banner names tailwindcss.com
	if strings.Contains(css, "@import") {
		t.Error("app.css has an @import")
	}
	for _, m := range regexp.MustCompile(`url\(([^)]*)\)`).FindAllStringSubmatch(css, -1) {
		target := strings.Trim(m[1], `"'`)
		if !strings.HasPrefix(target, "fonts/") && !strings.HasPrefix(target, "data:") && !strings.HasPrefix(target, "#") {
			t.Errorf("app.css loads %q: only its own fonts and data URIs are allowed", target)
		}
	}
}

func TestPagesAreAccessible(t *testing.T) {
	label := regexp.MustCompile(`(?s)<label[^>]*>.*?</label>`)
	input := regexp.MustCompile(`(?i)<input\b[^>]*>`)
	for name, body := range pagesToCheck(t) {
		if isPart(name) {
			continue
		}
		// Every visible input sits inside a <label> (hidden inputs have none).
		labelled := strings.Join(label.FindAllString(body, -1), "\n")
		for _, in := range input.FindAllString(body, -1) {
			if strings.Contains(in, `type="hidden"`) {
				continue
			}
			if !strings.Contains(labelled, in) {
				t.Errorf("page %q has an input without a label: %s", name, in)
			}
		}
		// Page structure: a language, a title, one h1, a main landmark.
		for _, want := range []string{`<html lang="en"`, "<title>", "<main"} {
			if !strings.Contains(body, want) {
				t.Errorf("page %q lacks %s", name, want)
			}
		}
		if got := strings.Count(body, "<h1"); got != 1 {
			t.Errorf("page %q has %d <h1>, want 1", name, got)
		}
		// Every error block is announced, and its mark is hidden from readers.
		for _, m := range regexp.MustCompile(`class="cv-(?:error|notice)[^"]*"`).FindAllStringIndex(body, -1) {
			start := strings.LastIndex(body[:m[0]], "<")
			tag := body[start:m[1]]
			if strings.Contains(tag, `role="alert"`) || strings.Contains(tag, `role="status"`) {
				continue
			}
			// A form announces its first error and links the others to their fields, so a
			// reader hears them when the field is focused.
			if id := regexp.MustCompile(`\bid="([^"]+)"`).FindStringSubmatch(tag); id == nil || !strings.Contains(body, `aria-describedby="`+id[1]+`"`) {
				t.Errorf("page %q has a message that isn't announced: %s", name, tag)
			}
		}
		for _, m := range regexp.MustCompile(`<span class="cv-mark[^"]*"[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(m, `aria-hidden="true"`) {
				t.Errorf("page %q has a status mark readers would hear: %s", name, m)
			}
		}
	}

	// The console: where you are is marked, status has words, live regions are announced (R19, FR-029).
	pill := regexp.MustCompile(`<span class="cv-pill[^"]*"><span class="cv-mark[^"]*"[^>]*></span>([^<]*)</span>`)
	for name, body := range pagesToCheck(t) {
		for _, m := range pill.FindAllStringSubmatch(body, -1) {
			if strings.TrimSpace(m[1]) == "" {
				t.Errorf("page %q has a status with a mark and no word: %s", name, m[0])
			}
		}
		if n := strings.Count(body, `class="cv-pill`) - len(pill.FindAllString(body, -1)); n != 0 {
			t.Errorf("page %q has %d status pills that aren't a mark followed by a word", name, n)
		}
		if strings.Contains(body, "data-live-indicator") && !strings.Contains(body, `id="live" role="status" aria-live="polite"`) {
			t.Errorf("page %q has a live indicator that isn't announced politely", name)
		}
		if strings.Contains(body, `class="cv-tabs"`) {
			nav := body[strings.Index(body, `class="cv-tabs"`):]
			nav = nav[:strings.Index(nav, "</nav>")]
			if got := strings.Count(nav, `aria-current="page"`); got != 1 {
				t.Errorf("page %q marks %d tabs as the current one, want 1", name, got)
			}
		}
		if strings.Contains(body, `class="cv-nav"`) && !isPart(name) {
			nav := body[strings.Index(body, `class="cv-nav"`):]
			nav = nav[:strings.Index(nav, "</nav>")]
			if got := strings.Count(nav, `aria-current="page"`); got > 1 {
				t.Errorf("page %q marks %d main links as the current one", name, got)
			}
		}
		// The newest deployment and the copies are announced when they change.
		if strings.Contains(body, `hx-get="/parts/apps/shop/deployments`) && !regexp.MustCompile(`hx-get="/parts/apps/shop/deployments[^>]*aria-live="polite"`).MatchString(body) {
			t.Errorf("page %q: the deployment history isn't an aria-live region", name)
		}
	}

	// Errors are role=alert; an expired-session note is a status.
	e := newUIEnv(t, true, "")
	_, body := e.failSignIn(t, nil)
	if !strings.Contains(body, `role="alert"`) {
		t.Errorf("a failed sign-in is not announced:\n%s", body)
	}
}

// An app's name set in mono inside a .cv-label (the delete confirmation: "Type blog to confirm")
// must not be uppercased: the form compares what is typed with the name, exactly.
func TestStylesheetKeepsTheNameInALabel(t *testing.T) {
	e := newUIEnv(t, true, "")
	_, css := e.do(t, request{path: "/static/app.css"})
	rule := regexp.MustCompile(`\.cv-label \.cv-inline-mono\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if rule == nil {
		t.Fatal("app.css has no rule for a name inside a label (.cv-label .cv-inline-mono)")
	}
	if !strings.Contains(strings.ReplaceAll(rule[1], " ", ""), "text-transform:none") {
		t.Errorf(".cv-label .cv-inline-mono = {%s}, want text-transform: none", rule[1])
	}
	// The label itself still uppercases everything else in it.
	if label := regexp.MustCompile(`\.cv-label\s*\{([^}]*)\}`).FindStringSubmatch(css); label == nil || !strings.Contains(strings.ReplaceAll(label[1], " ", ""), "text-transform:uppercase") {
		t.Error("the label no longer uppercases; this test would be checking nothing")
	}
}

func TestStylesheetKeepsTheDesignRules(t *testing.T) {
	e := newUIEnv(t, true, "")
	_, css := e.do(t, request{path: "/static/app.css"})
	for _, want := range []string{
		"prefers-reduced-motion", // DESIGN.md, Live updates: reduced motion turns it all off
		"tabular-nums",           // DESIGN.md, The Tabular Rule
		"font-display:swap",
		"IBM Plex Sans", "IBM Plex Mono",
		// Narrow screens (R19): the tables stack by their own width, the sidebar becomes the
		// menu under 900px, and standing-alone links are 44px targets on touch screens.
		"@container (max-width:719.98px)", "container-type:inline-size", "(max-width:899.98px)", "(pointer:coarse)",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	// The One Tick Rule: nothing loops.
	if strings.Contains(css, "infinite") {
		t.Error("app.css has an infinite animation")
	}
}
