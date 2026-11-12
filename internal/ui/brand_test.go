package ui

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestBrandMark(t *testing.T) {
	const (
		bigBox   = `viewBox="4.375 2.25 23.25 27.5"`
		smallBox = `viewBox="2 1.5 11 13"`
	)
	for _, tc := range []struct {
		name       string
		height     int
		wantBox    string
		wantWidth  string
		wantHeight string
		wantShape  string
	}{
		{"console size uses the brand geometry", 32, bigBox, `width="27.05"`, `height="32"`, `<circle cx="9.875" cy="7.5" r="5.25"/>`},
		{"24 is the first brand-geometry size", 24, bigBox, `width="20.29"`, `height="24"`, `<circle cx="9.875" cy="7.5" r="5.25"/>`},
		{"top bar size uses the hinted geometry", 20, smallBox, `width="16.92"`, `height="20"`, `<circle cx="5" cy="4" r="2.5"/>`},
		{"23 is the last hinted size", 23, smallBox, `width="19.46"`, `height="23"`, `<circle cx="5" cy="4" r="2.5"/>`},
		{"16 is the minimum", 16, smallBox, `width="13.54"`, `height="16"`, `<rect x="8" y="9.5" width="5" height="5"/>`},
		{"below the minimum is raised to 16", 8, smallBox, `width="13.54"`, `height="16"`, `<rect x="8" y="9.5" width="5" height="5"/>`},
		{"zero is raised to 16", 0, smallBox, `width="13.54"`, `height="16"`, `<path d="M8 6.5V1.5A5 5 0 0 1 13 6.5Z"/>`},
		{"a whole width has no decimals", 55, bigBox, `width="46.5"`, `height="55"`, `<rect x="17.125" y="19.25" width="10.5" height="10.5"/>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(brandMark(tc.height))
			for _, want := range []string{tc.wantBox, tc.wantWidth, tc.wantHeight, tc.wantShape,
				`<svg class="cv-logo-mark"`, `fill="currentColor"`, `aria-hidden="true"`, `focusable="false"`} {
				if !strings.Contains(got, want) {
					t.Errorf("brandMark(%d) lacks %s:\n%s", tc.height, want, got)
				}
			}
			other := smallBox
			if tc.wantBox == smallBox {
				other = bigBox
			}
			if strings.Contains(got, other) {
				t.Errorf("brandMark(%d) has the other geometry's %s:\n%s", tc.height, other, got)
			}
			if strings.Contains(got, "style=") {
				t.Errorf("brandMark(%d) has a style attribute:\n%s", tc.height, got)
			}
			if regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).MatchString(got) {
				t.Errorf("brandMark(%d) writes a hex color:\n%s", tc.height, got)
			}
			for _, bad := range []string{"role=", "aria-label"} {
				if strings.Contains(got, bad) {
					t.Errorf("brandMark(%d) has %s: the word beside it is the name:\n%s", tc.height, bad, got)
				}
			}
		})
	}
}

// logoMarkSVG returns the first inline logo mark in a page, or fails.
func logoMarkSVG(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `<svg class="cv-logo-mark"`)
	if i < 0 {
		t.Fatalf("the page has no logo mark:\n%s", page)
	}
	svg := page[i:]
	return svg[:strings.Index(svg, "</svg>")+len("</svg>")]
}

func checkLogoMark(t *testing.T, where, page string) {
	t.Helper()
	svg := logoMarkSVG(t, page)
	if !strings.Contains(svg, `fill="currentColor"`) || !strings.Contains(svg, `aria-hidden="true"`) {
		t.Errorf("%s: the mark isn't currentColor and aria-hidden: %s", where, svg)
	}
	if strings.Contains(svg, "style=") {
		t.Errorf("%s: the mark has a style attribute (the CSP forbids it): %s", where, svg)
	}
}

func TestBrandOnPages(t *testing.T) {
	e := newConsoleEnv(t)

	// The sign-in page and a plain message carry the 32 px mark.
	_, login := e.do(t, request{path: "/login"})
	checkLogoMark(t, "sign-in", login)
	if !strings.Contains(login, `height="32"`) {
		t.Errorf("the sign-in mark isn't 32 px tall")
	}
	resp, notFound := e.do(t, request{path: "/nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /nope = %d", resp.StatusCode)
	}
	checkLogoMark(t, "message page", notFound)

	// The shell's top bar (shown under 900 px) has the 20 px mark and the wordmark in one link.
	resp, apps := e.do(t, request{path: "/apps", cookies: []*http.Cookie{e.Cookie}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps = %d", resp.StatusCode)
	}
	checkLogoMark(t, "shell", apps)
	re := regexp.MustCompile(`(?s)<a href="/apps" class="cv-wordmark cv-top-brand"><svg class="cv-logo-mark".*?height="20".*?</svg>caravana</a>`)
	if !re.MatchString(apps) {
		t.Errorf("the top bar's link isn't the 20 px mark and the wordmark:\n%s", apps)
	}

	// Every page links the three icons with their content hash.
	for _, link := range []string{
		`<link rel="icon" href="/static/brand/favicon.ico?v=`,
		`<link rel="icon" href="/static/brand/favicon.svg?v=`,
		`<link rel="apple-touch-icon" href="/static/brand/apple-touch-icon.png?v=`,
	} {
		for name, page := range map[string]string{"sign-in": login, "shell": apps, "message": notFound} {
			if !strings.Contains(page, link) {
				t.Errorf("%s page lacks %s", name, link)
			}
		}
	}
}

func TestBrandFiles(t *testing.T) {
	e := newUIEnv(t, true, "")

	// /favicon.ico is public: browsers ask for it at the root, with no hash.
	resp, body := e.do(t, request{path: "/favicon.ico"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/x-icon" || len(body) < 100 {
		t.Errorf("GET /favicon.ico = %d %q (%d bytes)", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Errorf("/favicon.ico Cache-Control = %q", cc)
	}
	if body != string(mustRead(t, "brand/favicon.ico")) {
		t.Error("/favicon.ico isn't the embedded brand/favicon.ico")
	}

	for path, typ := range map[string]string{
		"/static/brand/favicon.svg":          "image/svg+xml",
		"/static/brand/mark.svg":             "image/svg+xml",
		"/static/brand/lockup.svg":           "image/svg+xml",
		"/static/brand/favicon.ico":          "",
		"/static/brand/apple-touch-icon.png": "image/png",
	} {
		resp, _ := e.do(t, request{path: path})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		} else if typ != "" && resp.Header.Get("Content-Type") != typ {
			t.Errorf("GET %s Content-Type = %q, want %q", path, resp.Header.Get("Content-Type"), typ)
		}
	}

	// The brand SVGs were stripped of the generator's provenance block.
	for _, name := range []string{"favicon", "mark", "lockup"} {
		svg := string(mustRead(t, "brand/"+name+".svg"))
		if strings.Contains(svg, "<metadata") || strings.Contains(svg, "c2pa") {
			t.Errorf("brand/%s.svg still has its metadata", name)
		}
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read static/%s: %v", name, err)
	}
	return b
}
