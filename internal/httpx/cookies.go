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
