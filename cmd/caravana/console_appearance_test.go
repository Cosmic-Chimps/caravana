package main

// The console-appearance step of the lab test (feature 028, the light theme): Settings › This
// browser › Appearance keeps the theme in the cookie caravana_theme, and every page, the sign-in
// page included, says it in the first byte (data-cv-theme on <html> and the color-scheme meta).
// It is in its own file only because console_lab_test.go is long; runConsole lists it right
// before console-delete.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var (
	themeRadio     = regexp.MustCompile(`<input type="radio" name="theme" value="([a-z]+)"( checked)?>`)
	htmlTheme      = regexp.MustCompile(`<html [^>]*data-cv-theme="([^"]*)"`)
	colorSchemeTag = regexp.MustCompile(`<meta name="color-scheme" content="([^"]*)">`)
)

// checkedTheme is the value of the theme radio that is checked, "" when none is, after checking
// that the page has the three radios in their order.
func checkedTheme(t *testing.T, body string) string {
	t.Helper()
	var values []string
	checked := ""
	for _, m := range themeRadio.FindAllStringSubmatch(body, -1) {
		values = append(values, m[1])
		if m[2] != "" {
			checked = m[1]
		}
	}
	if strings.Join(values, " ") != "system light dark" {
		t.Fatalf("the Appearance form has the radios %v, want system, light, dark:\n%s", values, body)
	}
	return checked
}

// pageTheme is what a page says about its theme: data-cv-theme ("" when it has none) and the
// color-scheme meta.
func pageTheme(t *testing.T, path, body string) (attribute, meta string) {
	t.Helper()
	if m := htmlTheme.FindStringSubmatch(body); m != nil {
		attribute = m[1]
	}
	m := colorSchemeTag.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("GET %s has no color-scheme meta:\n%s", path, body)
	}
	return attribute, m[1]
}

// appearance: through the console's own pages,
//
//  1. Appearance answers 200 with the three radios, "Match the system" checked, and the Settings
//     nav's last group is "This browser" with Appearance.
//  2. Saving Light answers 303 to /settings/appearance?saved=1 and sets the cookie; Apps then says
//     data-cv-theme="light" and the meta "light"; the saved page says "Saved." with Light checked.
//  3. Dark does the same.
//  4. Match the system deletes the cookie (Max-Age=0 in the answer, gone from the jar); Apps has no
//     attribute and the meta says "dark light".
//  5. A value that is none of the three is 422 with its sentence and sets no cookie.
//  6. The sign-in page, fetched by a browser that has no session and only the cookie, holds the
//     theme too.
//
// It leaves the browser as it found it (Match the system), so later steps see no theme attribute.
func (c *console) appearance(t *testing.T) {
	const path = "/settings/appearance"
	saved := path + "?saved=1"
	base, err := url.Parse(c.g.target.API)
	if err != nil {
		t.Fatal(err)
	}
	// The browser's own copy of the cookie, from the jar.
	jarTheme := func() string {
		for _, ck := range c.web.Jar.Cookies(base) {
			if ck.Name == "caravana_theme" {
				return ck.Value
			}
		}
		return ""
	}
	setCookie := func(resp *http.Response) *http.Cookie {
		for _, ck := range resp.Cookies() {
			if ck.Name == "caravana_theme" {
				return ck
			}
		}
		return nil
	}
	t.Cleanup(func() { c.post(t, path, url.Values{"theme": {"system"}}) }) // back to the system, whatever happened

	// 1. The form.
	resp, body := c.get(t, path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200:\n%s", path, resp.StatusCode, body)
	}
	if got := checkedTheme(t, body); got != "system" {
		t.Fatalf("with no cookie the checked radio is %q, want system", got)
	}
	nav := between(body, `<nav aria-label="Settings" class="cv-snav">`, `</nav>`)
	groups := regexp.MustCompile(`cv-snav-label">([^<]*)<`).FindAllStringSubmatch(nav, -1)
	if len(groups) == 0 || groups[len(groups)-1][1] != "This browser" {
		t.Fatalf("the Settings nav's last group is not \"This browser\": %v", groups)
	}
	last := nav[strings.LastIndex(nav, `cv-snav-label">`):]
	if !strings.Contains(last, `href="/settings/appearance"`) || !strings.Contains(last, `cv-snav-text">Appearance<`) ||
		!strings.Contains(last, `aria-current="page"`) {
		t.Fatalf("the last group of the nav lacks the current Appearance entry:\n%s", last)
	}

	// 2 and 3. Light, then Dark.
	for _, theme := range []string{"light", "dark"} {
		resp, body = c.post(t, path, url.Values{"theme": {theme}})
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != saved {
			t.Fatalf("POST theme=%s = %d → %q, want 303 to %s: %s", theme, resp.StatusCode, resp.Header.Get("Location"), saved, body)
		}
		if ck := setCookie(resp); ck == nil || ck.Value != theme || ck.MaxAge <= 0 {
			t.Fatalf("POST theme=%s set the cookie %+v, want caravana_theme=%s with a Max-Age", theme, ck, theme)
		}
		if got := jarTheme(); got != theme {
			t.Fatalf("after theme=%s the browser's cookie is %q", theme, got)
		}
		resp, body = c.get(t, "/apps")
		if attribute, meta := pageTheme(t, "/apps", body); attribute != theme || meta != theme {
			t.Fatalf("GET /apps after theme=%s: data-cv-theme %q and meta %q, want both %q", theme, attribute, meta, theme)
		}
		resp, body = c.get(t, saved)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Saved.") {
			t.Fatalf("GET %s = %d, want 200 saying Saved.:\n%s", saved, resp.StatusCode, body)
		}
		if got := checkedTheme(t, body); got != theme {
			t.Fatalf("after theme=%s the checked radio is %q", theme, got)
		}
	}

	// 6 (before the cookie goes). The sign-in page of a browser with no session.
	req, err := http.NewRequest(http.MethodGet, c.g.target.API+"/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "caravana_theme", Value: "dark"})
	anon, err := (&http.Client{Timeout: c.web.Timeout}).Do(req)
	resp, body = c.read(t, anon, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login with only the theme cookie = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if attribute, meta := pageTheme(t, "/login", body); attribute != "dark" || meta != "dark" {
		t.Fatalf("GET /login: data-cv-theme %q and meta %q, want both dark", attribute, meta)
	}

	// 5. Something else: 422, the sentence, no cookie (the dark one still holds).
	resp, body = c.post(t, path, url.Values{"theme": {"blue"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Choose one of the three.") {
		t.Fatalf("POST theme=blue = %d, want 422 saying \"Choose one of the three.\":\n%s", resp.StatusCode, body)
	}
	if ck := setCookie(resp); ck != nil {
		t.Fatalf("a refused post set the cookie %+v", ck)
	}
	if got := jarTheme(); got != "dark" {
		t.Fatalf("a refused post changed the browser's cookie to %q, want dark", got)
	}

	// 4. Match the system.
	resp, body = c.post(t, path, url.Values{"theme": {"system"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != saved {
		t.Fatalf("POST theme=system = %d → %q, want 303 to %s: %s", resp.StatusCode, resp.Header.Get("Location"), saved, body)
	}
	if ck := setCookie(resp); ck == nil || ck.MaxAge > 0 || ck.Value != "" {
		t.Fatalf("POST theme=system set the cookie %+v, want it deleted (Max-Age 0, empty)", ck)
	}
	if got := jarTheme(); got != "" {
		t.Fatalf("after theme=system the browser still has the cookie %q", got)
	}
	resp, body = c.get(t, "/apps")
	if attribute, meta := pageTheme(t, "/apps", body); attribute != "" || meta != "dark light" {
		t.Fatalf("GET /apps after theme=system: data-cv-theme %q and meta %q, want none and \"dark light\"", attribute, meta)
	}
}
