package ui

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Feature 028: Settings › This browser › Appearance keeps the theme in a cookie.

// radiosOf lists the theme radios of a page as "value" or "value*" for the checked one.
func radiosOf(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<input type="radio" name="theme" value="([a-z]+)"( checked)?>`).FindAllStringSubmatch(body, -1) {
		v := m[1]
		if m[2] != "" {
			v += "*"
		}
		out = append(out, v)
	}
	return out
}

func themeCookieOf(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "caravana_theme" {
			return c
		}
	}
	return nil
}

func TestAppearancePage(t *testing.T) {
	e := newUIEnv(t, true, "")
	session := e.signIn(t, nil)

	t.Run("without a cookie, Match the system is checked", func(t *testing.T) {
		resp, body := e.do(t, request{path: "/settings/appearance", cookies: []*http.Cookie{session}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d", resp.StatusCode)
		}
		if got := strings.Join(radiosOf(body), " "); got != "system* light dark" {
			t.Errorf("radios = %q, want system* light dark", got)
		}
		for _, want := range []string{"Appearance", "How the console looks in this browser.", "Theme", "Kept in this browser only.",
			"Match the system", `data-system-theme`, ">Light<", ">Dark<", `action="/settings/appearance"`, ">Save<"} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q", want)
			}
		}
		if strings.Contains(text(body), "Saved.") {
			t.Error("the page says Saved. without having saved")
		}
	})

	t.Run("with the cookie light, Light is checked", func(t *testing.T) {
		_, body := e.do(t, request{path: "/settings/appearance",
			cookies: []*http.Cookie{session, {Name: "caravana_theme", Value: "light"}}})
		if got := strings.Join(radiosOf(body), " "); got != "system light* dark" {
			t.Errorf("radios = %q, want system light* dark", got)
		}
	})

	t.Run("?saved=1 says Saved.", func(t *testing.T) {
		_, body := e.do(t, request{path: "/settings/appearance?saved=1", cookies: []*http.Cookie{session}})
		if !strings.Contains(text(body), "Saved.") || !strings.Contains(body, "cv-mark-healthy") {
			t.Errorf("no Saved. notice:\n%s", text(body))
		}
	})

	t.Run("a signed-out visitor is sent to sign in", func(t *testing.T) {
		resp, _ := e.do(t, request{path: "/settings/appearance"})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("GET signed out = %d to %q, want 303 to /login", resp.StatusCode, location(t, resp))
		}
	})
}

func TestAppearanceSave(t *testing.T) {
	e := newUIEnv(t, true, "")
	session := e.signIn(t, nil)

	for _, theme := range []string{"light", "dark"} {
		resp, _ := e.post(t, session, "/settings/appearance", url.Values{"theme": {theme}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/appearance?saved=1" {
			t.Fatalf("POST theme=%s = %d to %q, want 303 to /settings/appearance?saved=1", theme, resp.StatusCode, location(t, resp))
		}
		c := themeCookieOf(resp)
		if c == nil || c.Value != theme || c.MaxAge != 31536000 || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
			t.Errorf("theme=%s: cookie = %+v, want %s, a year, HttpOnly, Lax, Path /", theme, c, theme)
		}
	}

	t.Run("system deletes the cookie", func(t *testing.T) {
		resp, _ := e.post(t, session, "/settings/appearance", url.Values{"theme": {"system"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/appearance?saved=1" {
			t.Fatalf("POST theme=system = %d to %q", resp.StatusCode, location(t, resp))
		}
		c := themeCookieOf(resp)
		if c == nil || c.MaxAge > 0 || c.Value != "" {
			t.Errorf("cookie = %+v, want one that is deleted (Max-Age <= 0, empty)", c)
		}
	})

	t.Run("another value draws the page again and keeps the cookie", func(t *testing.T) {
		resp, body := e.do(t, request{method: http.MethodPost, path: "/settings/appearance", form: url.Values{"theme": {"blue"}},
			cookies: []*http.Cookie{session, {Name: "caravana_theme", Value: "dark"}},
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST theme=blue = %d, want 422", resp.StatusCode)
		}
		if c := themeCookieOf(resp); c != nil {
			t.Errorf("a refused post set the cookie: %+v", c)
		}
		if !strings.Contains(text(body), "Choose one of the three.") {
			t.Errorf("the sentence is missing:\n%s", text(body))
		}
		if got := strings.Join(radiosOf(body), " "); got != "system light dark*" {
			t.Errorf("radios = %q, want the cookie's choice (dark) still checked", got)
		}
	})

	t.Run("a post from another site is refused", func(t *testing.T) {
		resp, _ := e.do(t, request{method: http.MethodPost, path: "/settings/appearance", form: url.Values{"theme": {"light"}},
			cookies: []*http.Cookie{session}, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if resp.StatusCode != http.StatusForbidden || themeCookieOf(resp) != nil {
			t.Errorf("cross-site POST = %d, cookie %+v, want 403 and none", resp.StatusCode, themeCookieOf(resp))
		}
	})
}
