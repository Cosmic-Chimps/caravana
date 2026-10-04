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
