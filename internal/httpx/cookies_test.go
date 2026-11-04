package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionCookie(t *testing.T) {
	expires := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

	t.Run("over HTTPS", func(t *testing.T) {
		w := httptest.NewRecorder()
		SetSessionCookie(w, "value-1", expires, true)
		c := onlyCookie(t, w)
		if c.Name != "__Host-caravana_session" || !c.Secure || !c.HttpOnly || c.Path != "/" ||
			c.SameSite != http.SameSiteLaxMode || c.Domain != "" || !c.Expires.Equal(expires) {
			t.Errorf("cookie = %+v", c)
		}
	})

	t.Run("over plain HTTP", func(t *testing.T) {
		w := httptest.NewRecorder()
		SetSessionCookie(w, "value-1", expires, false)
		c := onlyCookie(t, w)
		if c.Name != "caravana_session" || c.Secure || !c.HttpOnly || c.Path != "/" ||
			c.SameSite != http.SameSiteLaxMode || !c.Expires.Equal(expires) {
			t.Errorf("cookie = %+v", c)
		}
	})

	t.Run("read back, only the cookie of the right kind", func(t *testing.T) {
		for _, https := range []bool{true, false} {
			w := httptest.NewRecorder()
			SetSessionCookie(w, "value-1", expires, https)
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(onlyCookie(t, w))

			if v, ok := SessionCookie(r, https); !ok || v != "value-1" {
				t.Errorf("SessionCookie(https=%v) = %q, %v; want value-1", https, v, ok)
			}
			if _, ok := SessionCookie(r, !https); ok {
				t.Errorf("SessionCookie(https=%v) found a cookie of the other kind", !https)
			}
		}
	})

	t.Run("none, or empty", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, ok := SessionCookie(r, false); ok {
			t.Error("found a cookie on a request with none")
		}
		r.AddCookie(&http.Cookie{Name: "caravana_session", Value: ""})
		if _, ok := SessionCookie(r, false); ok {
			t.Error("an empty cookie counts as a session")
		}
	})

	t.Run("clearing", func(t *testing.T) {
		for _, https := range []bool{true, false} {
			w := httptest.NewRecorder()
			ClearSessionCookie(w, https)
			header := w.Header().Get("Set-Cookie")
			if !strings.Contains(header, "Max-Age=0") || !strings.Contains(header, "Path=/") ||
				!strings.Contains(header, "HttpOnly") || strings.Contains(header, "value") {
				t.Errorf("clearing header (https=%v) = %q", https, header)
			}
			if https != strings.Contains(header, "Secure") {
				t.Errorf("clearing header (https=%v) = %q: Secure mismatch", https, header)
			}
		}
	})
}

func onlyCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	return cookies[0]
}

func TestThemeCookie(t *testing.T) {
	t.Run("set over HTTPS and over HTTP", func(t *testing.T) {
		for _, https := range []bool{true, false} {
			for _, theme := range []string{"light", "dark"} {
				w := httptest.NewRecorder()
				SetThemeCookie(w, theme, https)
				c := onlyCookie(t, w)
				if c.Name != "caravana_theme" || c.Value != theme || c.Path != "/" || c.MaxAge != 365*24*60*60 ||
					!c.HttpOnly || c.Secure != https || c.SameSite != http.SameSiteLaxMode || c.Domain != "" {
					t.Errorf("cookie (%s, https=%v) = %+v", theme, https, c)
				}
			}
		}
	})

	t.Run("read back", func(t *testing.T) {
		for _, theme := range []string{"light", "dark"} {
			w := httptest.NewRecorder()
			SetThemeCookie(w, theme, false)
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(onlyCookie(t, w))
			if got := ThemeCookie(r); got != theme {
				t.Errorf("ThemeCookie after setting %q = %q", theme, got)
			}
		}
	})

	t.Run("none, or another value, is no choice", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if got := ThemeCookie(r); got != "" {
			t.Errorf("no cookie: ThemeCookie = %q, want empty", got)
		}
		for _, value := range []string{"blue", "Light", "", "light "} {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(&http.Cookie{Name: "caravana_theme", Value: value})
			if got := ThemeCookie(r); got != "" {
				t.Errorf("cookie %q: ThemeCookie = %q, want empty", value, got)
			}
		}
	})

	t.Run("an empty or unknown theme deletes the cookie", func(t *testing.T) {
		for _, theme := range []string{"", "blue"} {
			for _, https := range []bool{true, false} {
				w := httptest.NewRecorder()
				SetThemeCookie(w, theme, https)
				header := w.Header().Get("Set-Cookie")
				c := onlyCookie(t, w)
				if c.Name != "caravana_theme" || c.Value != "" || c.Path != "/" || !strings.Contains(header, "Max-Age=0") ||
					!c.HttpOnly || c.Secure != https || c.SameSite != http.SameSiteLaxMode {
					t.Errorf("deleting cookie (%q, https=%v) = %q", theme, https, header)
				}
			}
		}
	})
}
