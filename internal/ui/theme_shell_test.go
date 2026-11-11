package ui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// Feature 028: the theme is in the first byte of every page. With the cookie
// light or dark, <html> carries data-cv-theme and the meta says that one word;
// without it (or with any other value) there is no attribute and the meta says
// "dark light", so the stylesheet follows the system.

var (
	themeAttribute  = regexp.MustCompile(`<html [^>]*data-cv-theme="([^"]*)"`)
	colorSchemeMeta = regexp.MustCompile(`<meta name="color-scheme" content="([^"]*)">`)
)

// themeOf reads what a page says about its theme: the attribute ("" for none) and the meta.
func themeOf(t *testing.T, path, body string) (attribute, meta string) {
	t.Helper()
	if m := themeAttribute.FindStringSubmatch(body); m != nil {
		attribute = m[1]
	}
	m := colorSchemeMeta.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("GET %s: no color-scheme meta", path)
	}
	return attribute, m[1]
}

func TestThemeInEveryPage(t *testing.T) {
	e := newConsoleEnv(t)
	if _, err := e.Svc.CreateAppOnly(context.Background(), duoApp()); err != nil {
		t.Fatal(err)
	}
	// The pages TestNoShipWords walks, then a 404 page (a message page).
	paths := []string{
		"/apps", "/projects", "/apps/new",
		"/apps/duo", "/apps/duo/deployments", "/apps/duo/configure", "/apps/duo/variables",
		"/apps/duo/domains", "/apps/duo/backups", "/apps/duo/logs",
		"/settings", "/settings/registries", "/settings/backup-storage", "/settings/notifications", "/settings/tokens", "/settings/appearance",
		"/schedules", "/schedules/new", "/logs", "/monitoring",
		"/ops/servers", "/ops/servers/new",
		"/no/such/page",
	}
	cases := []struct {
		name, cookie, attribute, meta string
	}{
		{"no cookie", "", "", "dark light"},
		{"light", "light", "light", "light"},
		{"dark", "dark", "dark", "dark"},
		{"another value", "blue", "", "dark light"},
	}
	for _, tc := range cases {
		cookies := []*http.Cookie{e.Cookie}
		if tc.cookie != "" {
			cookies = append(cookies, &http.Cookie{Name: "caravana_theme", Value: tc.cookie})
		}
		for _, path := range paths {
			resp, body := e.do(t, request{path: path, cookies: cookies})
			if path == "/no/such/page" {
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
				}
			} else if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: GET %s = %d", tc.name, path, resp.StatusCode)
				continue
			}
			if attribute, meta := themeOf(t, path, body); attribute != tc.attribute || meta != tc.meta {
				t.Errorf("%s: GET %s: data-cv-theme %q and meta %q, want %q and %q", tc.name, path, attribute, meta, tc.attribute, tc.meta)
			}
			if !strings.Contains(body, `data-theme="caravana"`) {
				t.Errorf("%s: GET %s lost daisyUI's data-theme", tc.name, path)
			}
		}
	}
}

// The sign-in page reads the cookie too, so a forced theme holds after sign-out.
func TestThemeOnTheSignInPage(t *testing.T) {
	e := newUIEnv(t, true, "")
	for cookie, want := range map[string][2]string{"": {"", "dark light"}, "light": {"light", "light"}, "dark": {"dark", "dark"}, "blue": {"", "dark light"}} {
		var cookies []*http.Cookie
		if cookie != "" {
			cookies = append(cookies, &http.Cookie{Name: "caravana_theme", Value: cookie})
		}
		resp, body := e.do(t, request{path: "/login", cookies: cookies})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /login = %d", resp.StatusCode)
		}
		if attribute, meta := themeOf(t, "/login", body); attribute != want[0] || meta != want[1] {
			t.Errorf("cookie %q: /login has data-cv-theme %q and meta %q, want %q and %q", cookie, attribute, meta, want[0], want[1])
		}
	}
}
