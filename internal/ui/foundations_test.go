package ui

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newTestServer is the server itself, on the env's Auth, for tests of what
// the routes don't reach yet (the shell, the parts, requirePart).
func newTestServer(t *testing.T, e *uiEnv) *server {
	t.Helper()
	s, err := newServer(Deps{Auth: e.Auth, Log: slog.New(slog.DiscardHandler), Host: "caravana.example.com", Version: "0.7.0", Now: e.Clock.Now})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

// The sidebar names Caravana's version: "v" and the release, "dev" for a
// build that isn't one, nothing when the server wasn't told.
func TestVersionLabel(t *testing.T) {
	for version, want := range map[string]string{"0.7.0": "v0.7.0", "0.7.0-rc.1": "v0.7.0-rc.1", "dev": "dev", "": ""} {
		if got := (&server{version: version}).versionLabel(); got != want {
			t.Errorf("versionLabel(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestStaticAssetsAreHashed(t *testing.T) {
	e := newUIEnv(t, true, "")
	s := newTestServer(t, e)

	// Every embedded file has a hash, and the scripts the base template links exist.
	for _, name := range []string{"app.css", "js/htmx.min.js", "js/sse.min.js", "js/console.js", "js/LICENSES.md"} {
		if _, err := s.asset(name); err != nil {
			t.Errorf("asset(%q): %v", name, err)
		}
	}
	if _, err := s.asset("js/nope.js"); err == nil {
		t.Error("asset of a file that isn't embedded did not fail")
	}
	count := 0
	_ = fs.WalkDir(staticFS, "static", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			count++
		}
		return nil
	})
	if len(s.assets) != count {
		t.Errorf("%d files embedded, %d hashed", count, len(s.assets))
	}

	// The page links each by its current hash, and that URL is cached for a year.
	_, body := e.do(t, request{path: "/login"})
	for _, name := range []string{"app.css", "js/htmx.min.js", "js/sse.min.js", "js/console.js"} {
		href, _ := s.asset(name)
		if !strings.Contains(body, `"`+href+`"`) {
			t.Errorf("the login page does not link %s", href)
		}
		resp, _ := e.do(t, request{path: href})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", href, resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=31536000") || !strings.Contains(cc, "immutable") {
			t.Errorf("GET %s: Cache-Control = %q, want a year, immutable", href, cc)
		}
		// An old or made-up hash is not trusted for a year.
		bare := href[:strings.Index(href, "?")]
		for _, p := range []string{bare, bare + "?v=0123456789ab"} {
			resp, _ = e.do(t, request{path: p})
			if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
				t.Errorf("GET %s: Cache-Control = %q, want no-cache", p, cc)
			}
		}
	}

	resp, js := e.do(t, request{path: "/static/js/console.js"})
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") || !strings.HasPrefix(js, `"use strict";`) {
		t.Errorf("console.js is served as %q and starts %.20q", ct, js)
	}
}

// The base document configures htmx for our policy (research R3) and loads the
// three scripts deferred, in the order that works: htmx, its extension, ours.
func TestBaseDocumentSetsUpHtmx(t *testing.T) {
	e := newUIEnv(t, true, "")
	resp, body := e.do(t, request{path: "/login"})

	const config = `<meta name="htmx-config" content='{"allowEval":false,"allowScriptTags":false,"includeIndicatorStyles":false,"historyCacheSize":0}'>`
	if !strings.Contains(body, config) {
		t.Errorf("the page lacks the htmx-config meta:\n%s", body)
	}
	srcs := regexp.MustCompile(`<script src="(/static/js/[^"?]+)\?v=[0-9a-f]{12}" defer></script>`).FindAllStringSubmatch(body, -1)
	var got []string
	for _, m := range srcs {
		got = append(got, m[1])
	}
	want := []string{"/static/js/htmx.min.js", "/static/js/sse.min.js", "/static/js/console.js"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("scripts = %v, want %v", got, want)
	}
	if strings.Contains(body, "data-live") {
		t.Error("a page with no stream opens one")
	}

	// The policy is the 006 one, word for word: htmx fits under it.
	const csp = "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'"
	if got := resp.Header.Get("Content-Security-Policy"); got != csp {
		t.Errorf("Content-Security-Policy = %q, want %q", got, csp)
	}
}

func TestRequirePart(t *testing.T) {
	e := newUIEnv(t, true, "")
	s := newTestServer(t, e)
	c := e.signIn(t, nil)
	part := s.requirePart(func(w http.ResponseWriter, r *http.Request, session string) {
		_, _ = w.Write([]byte("fragment"))
	})
	call := func(cookie *http.Cookie, currentURL string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/parts/apps", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if currentURL != "" {
			req.Header.Set("HX-Current-URL", currentURL)
		}
		rec := httptest.NewRecorder()
		part(rec, req)
		return rec
	}

	t.Run("a signed-in admin gets the fragment", func(t *testing.T) {
		rec := call(c, "http://example.com/apps")
		if rec.Code != http.StatusOK || rec.Body.String() != "fragment" || rec.Header().Get("HX-Redirect") != "" {
			t.Errorf("= %d %q, HX-Redirect %q", rec.Code, rec.Body, rec.Header().Get("HX-Redirect"))
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
	})

	t.Run("without a session: 401, an empty body, and the page to come back to", func(t *testing.T) {
		for name, tc := range map[string]struct{ current, want string }{
			"a page":               {"https://caravana.example.com/apps/whoami/deployments", "/login?next=%2Fapps%2Fwhoami%2Fdeployments"},
			"its query is dropped": {"http://127.0.0.1:7070/apps/blog?x=1", "/login?next=%2Fapps%2Fblog"},
			"no header":            {"", "/login"},
			"not a URL":            {"%%", "/login"},
			"the API":              {"http://example.com/v1/apps", "/login"},
			"the sign-in page":     {"http://example.com/login", "/login"},
		} {
			rec := call(nil, tc.current)
			if rec.Code != http.StatusUnauthorized || rec.Body.Len() != 0 || rec.Header().Get("HX-Redirect") != tc.want {
				t.Errorf("%s: %d %q, HX-Redirect %q, want 401, empty, %q", name, rec.Code, rec.Body, rec.Header().Get("HX-Redirect"), tc.want)
			}
		}
	})

	t.Run("an ended session says so on the sign-in page", func(t *testing.T) {
		e.Clock.Advance(13 * time.Hour) // idle for more than 12 hours
		rec := call(c, "http://example.com/apps")
		if want := "/login?next=%2Fapps&expired=1"; rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != want {
			t.Errorf("= %d, HX-Redirect %q, want 401 and %q", rec.Code, rec.Header().Get("HX-Redirect"), want)
		}
	})
}

// shellTestPage is a console page as the later screens will write them: it
// fills "content" and "action" and starts from shellPage.
const shellTestPage = `{{define "content"}}<h1>Test page</h1>
{{template "status-pill" .Pill}} {{template "status-mark" "rolling"}} {{template "time" .When}} {{template "time" .Never}}
{{template "empty" .Empty}}{{template "empty" .EmptyBare}}{{end}}
{{define "action"}}<a href="/apps/new" class="cv-btn cv-btn-primary">New app</a>{{end}}`

type shellTestData struct {
	pageData
	Pill      statusView
	When      when
	Never     when
	Empty     emptyView
	EmptyBare emptyView
}

func TestShellAndParts(t *testing.T) {
	e := newUIEnv(t, true, "")
	s := newTestServer(t, e)
	tmpl := template.Must(template.New("base.html").Funcs(template.FuncMap{"asset": s.asset}).ParseFS(templatesFS,
		"templates/base.html", "templates/shell.html", "templates/parts.html"))
	template.Must(tmpl.Parse(shellTestPage))
	s.pages["shelltest"] = tmpl

	render := func(t *testing.T, live string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		page, err := s.shellPage(req, "Test", "settings", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: "whoami", Mono: true})
		if err != nil {
			t.Fatal(err)
		}
		page.LiveURL = live
		at := e.Clock.Now().Add(-5 * time.Minute)
		rec := httptest.NewRecorder()
		s.render(rec, http.StatusOK, "shelltest", shellTestData{
			pageData:  page,
			Pill:      statusView{Kind: "healthy", Word: "healthy"},
			When:      whenOf(e.Clock.Now(), &at, "never"),
			Never:     whenOf(e.Clock.Now(), nil, "never"),
			Empty:     emptyView{Title: "No apps yet.", Text: "Deploy an image and it shows up here.", ActionLabel: "New app", ActionHref: "/apps/new"},
			EmptyBare: emptyView{Title: "No copies are running."},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("render = %d: %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}

	body := render(t, "")
	for _, want := range []string{
		// the sidebar: wordmark, host in mono, the two links with the current one marked, the account row
		`class="cv-wordmark">caravana</a>`, `caravana.example.com`, `title="Caravana's version">v0.7.0</span>`,
		`<a href="/apps" class="cv-nav-link">Apps</a>`, `<a href="/settings" class="cv-nav-link" aria-current="page">Settings</a>`,
		testEmail, `<form method="post" action="/logout">`, `>Sign out</button>`,
		// the narrow-screen menu, the skip link, the page and its action slot
		`<details class="cv-menu">`, `>Menu</summary>`, `href="#main"`, `<main id="main" class="cv-page">`, `<h1>Test page</h1>`,
		`<div class="cv-top-action"><a href="/apps/new" class="cv-btn cv-btn-primary">New app</a></div>`,
		// breadcrumbs: links, and the page itself as current
		`<a href="/apps">Apps</a>`, `aria-current="page">whoami</span>`,
		// the parts
		`cv-pill cv-pill-healthy"><span class="cv-mark cv-mark-healthy" aria-hidden="true"></span>healthy`,
		`cv-mark cv-mark-rolling`, `title="2026-10-04 08:55:00 UTC">5 minutes ago</time>`, `datetime="2026-10-04T08:55:00Z"`,
		`<time class="cv-data">never</time>`,
		`No apps yet.`, `Deploy an image and it shows up here.`, `>New app</a>`, `No copies are running.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the shell page lacks %q:\n%s", want, body)
		}
	}
	// Both nav lists (sidebar and menu) mark the current link; the bare empty state has no button.
	if got := strings.Count(body, `aria-current="page">Settings</a>`); got != 2 {
		t.Errorf("Settings is marked current %d times, want 2 (sidebar and menu)", got)
	}
	if strings.Contains(body, `id="live"`) {
		t.Error("the live indicator is shown on a page with no stream")
	}

	body = render(t, "/live")
	for _, want := range []string{
		`hx-ext="sse" sse-connect="/live" sse-close="ended" data-live`,
		`<span id="live" role="status" aria-live="polite" data-live-indicator class="cv-live">`, `data-live-text`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the live page lacks %q:\n%s", want, body)
		}
	}
}
