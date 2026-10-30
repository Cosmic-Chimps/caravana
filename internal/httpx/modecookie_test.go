package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModeCookie(t *testing.T) {
	for _, https := range []bool{false, true} {
		for _, mode := range []string{ModeDeveloper, ModeOperator} {
			w := httptest.NewRecorder()
			SetModeCookie(w, mode, https)
			c := onlyCookie(t, w)
			// A preference: the same name over HTTP and HTTPS (no __Host- prefix), kept for a
			// year, Lax, for the whole site, never readable by script, Secure only over HTTPS.
			if c.Name != "caravana_mode" || c.Value != mode || c.Path != "/" || c.Domain != "" ||
				!c.HttpOnly || c.Secure != https || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 365*24*3600 {
				t.Errorf("SetModeCookie(%q, https=%v) = %+v", mode, https, c)
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(c)
			if got := ModeCookie(r); got != mode {
				t.Errorf("ModeCookie after setting %q = %q", mode, got)
			}
		}
	}
}

func TestModeCookieReadsOnlyOps(t *testing.T) {
	tests := []struct {
		name   string
		cookie *http.Cookie
		want   string
	}{
		{"none", nil, ModeDeveloper},
		{"dev", &http.Cookie{Name: "caravana_mode", Value: "dev"}, ModeDeveloper},
		{"ops", &http.Cookie{Name: "caravana_mode", Value: "ops"}, ModeOperator},
		{"anything else", &http.Cookie{Name: "caravana_mode", Value: "root"}, ModeDeveloper},
		{"another cookie's name", &http.Cookie{Name: "mode", Value: "ops"}, ModeDeveloper},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tt.cookie != nil {
			r.AddCookie(tt.cookie)
		}
		if got := ModeCookie(r); got != tt.want {
			t.Errorf("%s: ModeCookie = %q, want %q", tt.name, got, tt.want)
		}
	}

	// Setting anything but ops stores dev.
	w := httptest.NewRecorder()
	SetModeCookie(w, "whatever", false)
	if c := onlyCookie(t, w); c.Value != ModeDeveloper {
		t.Errorf("SetModeCookie(whatever) stored %q, want dev", c.Value)
	}
}
