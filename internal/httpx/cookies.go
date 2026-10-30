package httpx

import (
	"net/http"
	"time"
)

// The session cookie's name depends on HTTPS. Over HTTPS it carries the
// __Host- prefix, which makes browsers refuse it unless it is Secure, has
// Path=/ and names no Domain, so another subdomain can't plant one. Plain HTTP
// (loopback during development) can't have the prefix.
const (
	sessionCookieHTTPS = "__Host-caravana_session"
	sessionCookieHTTP  = "caravana_session"
)

func sessionCookieName(https bool) string {
	if https {
		return sessionCookieHTTPS
	}
	return sessionCookieHTTP
}

// SessionCookie returns the session cookie's value from the request. https is
// whether the client used HTTPS (Proxies.HTTPS); only the cookie of that kind
// is read.
func SessionCookie(r *http.Request, https bool) (string, bool) {
	c, err := r.Cookie(sessionCookieName(https))
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// SetSessionCookie sends the session cookie. expires is the session's
// absolute expiry.
func SetSessionCookie(w http.ResponseWriter, value string, expires time.Time, https bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(https),
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   https,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie tells the browser to drop the session cookie.
func ClearSessionCookie(w http.ResponseWriter, https bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(https),
		Value:    "",
		Path:     "/",
		MaxAge:   -1, // sent as Max-Age=0
		HttpOnly: true,
		Secure:   https,
		SameSite: http.SameSiteLaxMode,
	})
}

// The console's view, Developer or Operator, is remembered in a plain cookie so
// the pages both views share (Settings, an app's pages, New app) draw the
// sidebar of the view the owner was last in. It is a preference, not a
// security value: nothing trusts it, and any other value reads as Developer.
const (
	modeCookieName = "caravana_mode"
	// ModeDeveloper and ModeOperator are the two values the cookie carries.
	ModeDeveloper = "dev"
	ModeOperator  = "ops"
	// modeCookieAge is how long the preference is kept: a year.
	modeCookieAge = 365 * 24 * time.Hour
)

// ModeCookie returns the view the request's cookie remembers: ModeOperator or
// ModeDeveloper, which is also what no cookie, or one with another value, means.
func ModeCookie(r *http.Request) string {
	if c, err := r.Cookie(modeCookieName); err == nil && c.Value == ModeOperator {
		return ModeOperator
	}
	return ModeDeveloper
}

// SetModeCookie remembers the view (ModeDeveloper or ModeOperator). It has no
// __Host- prefix, since it holds nothing to protect, but it is HttpOnly, Lax,
// for the whole site and Secure when the client used HTTPS (Proxies.HTTPS).
func SetModeCookie(w http.ResponseWriter, mode string, https bool) {
	if mode != ModeOperator {
		mode = ModeDeveloper
	}
	http.SetCookie(w, &http.Cookie{
		Name:     modeCookieName,
		Value:    mode,
		Path:     "/",
		MaxAge:   int(modeCookieAge / time.Second),
		HttpOnly: true,
		Secure:   https,
		SameSite: http.SameSiteLaxMode,
	})
}
