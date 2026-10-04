package ui

import (
	"bytes"
	"context"
	"io"
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

	t.Run("sets the cookie and goes to /account", func(t *testing.T) {
		resp, _ := e.do(t, request{method: http.MethodPost, path: "/login",
			form: url.Values{"email": {testEmail}, "password": {testPassword}}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/account" {
			t.Fatalf("sign-in = %d to %q, want 303 to /account", resp.StatusCode, location(t, resp))
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

	t.Run("unsafe next values land on /account", func(t *testing.T) {
		for _, next := range []string{"//evil.example", `/\evil`, "https://evil.example", "/v1/apps", "/v1", "javascript:alert(1)", "evil", "/login", "/x\r\nSet-Cookie: a=b"} {
			resp, _ := e.do(t, request{method: http.MethodPost, path: "/login",
				form: url.Values{"email": {testEmail}, "password": {testPassword}, "next": {next}}})
			if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/account" {
				t.Errorf("next %q: redirect %d to %q, want 303 to /account", next, resp.StatusCode, location(t, resp))
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

	resp, _ := e.do(t, request{path: "/account"})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/login?next=%2Faccount" {
		t.Errorf("GET /account without a session = %d to %q, want 303 to /login?next=%%2Faccount", resp.StatusCode, location(t, resp))
	}

	c := e.signIn(t, nil)
	resp, body := e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Signed in as") || !strings.Contains(body, testEmail) {
		t.Errorf("GET /account with a session = %d:\n%s", resp.StatusCode, body)
	}

	// Signed in already: /login goes on to /account.
	resp, _ = e.do(t, request{path: "/login", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/account" {
		t.Errorf("GET /login while signed in = %d to %q, want 303 to /account", resp.StatusCode, location(t, resp))
	}

	// "/" goes to /account.
	resp, _ = e.do(t, request{path: "/"})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/account" {
		t.Errorf("GET / = %d to %q, want 303 to /account", resp.StatusCode, location(t, resp))
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)

	e.Clock.Advance(13 * time.Hour) // idle for more than 12 hours
	resp, _ := e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login?next=%2Faccount") {
		t.Fatalf("an idle session = %d to %q, want 303 to /login", resp.StatusCode, location(t, resp))
	}
	// The sign-in page says the session ended.
	_, body := e.do(t, request{path: location(t, resp)})
	if !strings.Contains(body, "Your session ended. Sign in again.") {
		t.Errorf("the login page does not say the session ended:\n%s", body)
	}

	// A form posted with an ended session doesn't come back to the form's URL,
	// which is no page to land on.
	resp, _ = e.do(t, request{method: http.MethodPost, path: "/account/password", cookies: []*http.Cookie{c},
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
	resp, _ = e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
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
			if resp, _ := e.do(t, request{path: "/account", cookies: []*http.Cookie{c}}); resp.StatusCode != http.StatusOK {
				t.Errorf("%s: the session ended anyway (GET /account = %d)", name, resp.StatusCode)
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
	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)
	for _, r := range []request{
		{path: "/login"}, {path: "/account", cookies: []*http.Cookie{c}}, {path: "/account"},
		{path: "/nope"}, {path: "/static/app.css"},
		{method: http.MethodPost, path: "/login", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}},
	} {
		resp, _ := e.do(t, r)
		h := resp.Header
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "form-action 'self'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s %s: Content-Security-Policy %q lacks %q", r.method, r.path, csp, want)
			}
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s %s: X-Content-Type-Options %q, Referrer-Policy %q", r.method, r.path,
				h.Get("X-Content-Type-Options"), h.Get("Referrer-Policy"))
		}
	}

	// Pages with a session in them are never cached.
	resp, _ := e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on /account = %q, want no-store", got)
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

		resp, _ := e.post(t, c, "/account/password", url.Values{"currentPassword": {testPassword}, "newPassword": {newPassword}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/account?password=changed" {
			t.Fatalf("POST = %d to %q, want 303 to /account?password=changed", resp.StatusCode, location(t, resp))
		}
		resp, body := e.do(t, request{path: location(t, resp), cookies: []*http.Cookie{c}})
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Password changed. Other browsers were signed out.") ||
			!strings.Contains(body, "cv-mark-healthy") {
			t.Errorf("the confirmation is missing:\n%s", body)
		}
		if resp, _ := e.do(t, request{path: "/account", cookies: []*http.Cookie{other}}); resp.StatusCode != http.StatusSeeOther {
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
			resp, body := e.post(t, c, "/account/password", tc.form)
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

		resp, body := e.post(t, c, "/account/tokens", url.Values{"name": {"ci-github"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /account/tokens = %d, want 200 (no redirect)", resp.StatusCode)
		}
		token := tokenInPage.FindString(body)
		if token == "" || !strings.Contains(body, "It won't be shown again.") {
			t.Fatalf("the new token is not shown with its warning:\n%s", body)
		}
		if !strings.Contains(body, `-H "Authorization: Bearer `+token+`"`) || !strings.Contains(body, e.URL+"/v1/tokens") {
			t.Errorf("the curl example is missing:\n%s", body)
		}
		if err := e.Auth.CheckAPIToken(ctx, token); err != nil {
			t.Errorf("the token shown does not work: %v", err)
		}

		_, body = e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
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

		_, body := e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
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

		resp, _ := e.post(t, c, "/account/tokens/"+created.Info.ID+"/revoke", url.Values{})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/account" {
			t.Fatalf("revoke = %d to %q, want 303 to /account", resp.StatusCode, location(t, resp))
		}
		if err := e.Auth.CheckAPIToken(ctx, created.Token); err == nil {
			t.Error("the revoked token still works")
		}
		_, body := e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
		if !strings.Contains(body, "cv-pill-failed") || strings.Contains(body, ">Revoke<") {
			t.Errorf("the row does not show revoked:\n%s", body)
		}
		// An unknown ID is not an error page: the list is simply unchanged.
		resp, _ = e.post(t, c, "/account/tokens/p4nzr2t6v3x7z5bqd7f4h6j3kq/revoke", url.Values{})
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
			resp, body := e.post(t, c, "/account/tokens", tc.form)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, tc.want) || !strings.Contains(body, `role="alert"`) {
				t.Errorf("%s: %d, want %q:\n%s", name, resp.StatusCode, tc.want, body)
			}
			if tokenInPage.MatchString(body) {
				t.Errorf("%s: a token was created and shown", name)
			}
		}
		// The name stays in the field after a refusal.
		_, body := e.post(t, c, "/account/tokens", url.Values{"name": {"taken"}})
		if !strings.Contains(body, `value="taken"`) {
			t.Errorf("the refused name was not kept:\n%s", body)
		}
	})

	t.Run("an expiry date becomes the end of that day, UTC", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		c := e.signIn(t, nil)
		day := e.Clock.Now().Add(48 * time.Hour).Format("2006-01-02")
		if resp, body := e.post(t, c, "/account/tokens", url.Values{"name": {"dated"}, "expires": {day}}); resp.StatusCode != http.StatusOK || !tokenInPage.MatchString(body) {
			t.Fatalf("create with an expiry = %d:\n%s", resp.StatusCode, body)
		}
		infos, _ := e.Auth.ListAPITokens(ctx)
		want, _ := time.Parse("2006-01-02 15:04:05", day+" 23:59:59")
		if len(infos) != 1 || infos[0].ExpiresAt == nil || !infos[0].ExpiresAt.Equal(want) {
			t.Errorf("tokens = %+v, want one expiring %v", infos, want)
		}
	})
}

func TestAccountFormsWorkWithoutJavaScript(t *testing.T) {
	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)
	_, body := e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
	if strings.Contains(strings.ToLower(body), "<script") || strings.Contains(strings.ToLower(body), " onclick=") {
		t.Errorf("the account page needs JavaScript:\n%s", body)
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

func TestAccountPostsAreProtected(t *testing.T) {
	ctx := context.Background()
	e := newUIEnv(t, true, "")
	c := e.signIn(t, nil)
	victim, _ := e.Auth.CreateAPIToken(ctx, "victim", nil)

	cross := map[string]string{"Sec-Fetch-Site": "cross-site"}
	for path, form := range map[string]url.Values{
		"/account/password": {"currentPassword": {testPassword}, "newPassword": {"a brand new password"}},
		"/account/tokens":   {"name": {"forged"}},
		"/account/tokens/" + victim.Info.ID + "/revoke": {},
	} {
		resp, body := e.do(t, request{method: http.MethodPost, path: path, form: form, headers: cross, cookies: []*http.Cookie{c}})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "refused") {
			t.Errorf("cross-site POST %s = %d, want the 403 page", path, resp.StatusCode)
		}
	}
	// Nothing changed.
	if err := e.Auth.CheckAPIToken(ctx, victim.Token); err != nil {
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
	for _, path := range []string{"/account/password", "/account/tokens", "/account/tokens/x/revoke"} {
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
	_, pages["account"] = e.do(t, request{path: "/account", cookies: []*http.Cookie{c}})
	_, pages["account, new token"] = e.post(t, c, "/account/tokens", url.Values{"name": {"ci"}})
	_, pages["account, errors"] = e.post(t, c, "/account/password", url.Values{"currentPassword": {"x"}, "newPassword": {"y"}})
	_, pages["not found"] = e.do(t, request{path: "/nope"})
	_, pages["refused"] = e.do(t, request{method: http.MethodPost, path: "/login", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})

	noAdmin := newUIEnv(t, false, "")
	_, pages["login, no admin"] = noAdmin.do(t, request{path: "/login"})
	for name, body := range pages {
		if !strings.Contains(body, "<html") {
			t.Fatalf("page %q did not render:\n%s", name, body)
		}
	}
	return pages
}

func TestPagesAskNothingOfOtherHosts(t *testing.T) {
	remote := regexp.MustCompile(`(?i)\b(?:href|src|action|srcset|poster|data)\s*=\s*["']?\s*(?:https?:)?//`)
	elements := regexp.MustCompile(`(?i)<(script|iframe|img|object|embed|video|audio|source|form[^>]*\saction="https?:)`)
	for name, body := range pagesToCheck(t) {
		if remote.MatchString(body) {
			t.Errorf("page %q references another host:\n%s", name, remote.FindString(body))
		}
		if m := elements.FindString(body); m != "" {
			t.Errorf("page %q has %q: pages ship no scripts, frames or images", name, m)
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
			if !strings.Contains(tag, `role="alert"`) && !strings.Contains(tag, `role="status"`) {
				t.Errorf("page %q has a message that isn't announced: %s", name, tag)
			}
		}
		for _, m := range regexp.MustCompile(`<span class="cv-mark[^"]*"[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(m, `aria-hidden="true"`) {
				t.Errorf("page %q has a status mark readers would hear: %s", name, m)
			}
		}
	}

	// Errors are role=alert; an expired-session note is a status.
	e := newUIEnv(t, true, "")
	_, body := e.failSignIn(t, nil)
	if !strings.Contains(body, `role="alert"`) {
		t.Errorf("a failed sign-in is not announced:\n%s", body)
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
