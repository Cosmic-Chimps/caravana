package ui

import (
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/httpx"
)

// The console's two views (feature 027): Developer, which is everything built
// before, and Operator, the pages about the host under /ops. The mode is the
// URL: being on an /ops page is Operator mode. The pages both views share
// (Settings, an app's pages, New app) draw the sidebar of the view the owner
// was last in, which a plain cookie remembers (httpx.ModeCookie). Sign-in and
// the plain messages are in neither view.

// pathMode says which view a page belongs to by its path: httpx.ModeOperator
// for /ops and everything under it, httpx.ModeDeveloper for the pages only the
// Developer view has, and "" for a page both share (Settings, an app's pages,
// New app) or one that isn't a page (sign-in, parts, live streams, static
// files, downloads), which never change the remembered view.
func pathMode(path string) string {
	switch {
	case path == "/ops" || strings.HasPrefix(path, "/ops/"):
		return httpx.ModeOperator
	case path == "/" || path == "/apps":
		return httpx.ModeDeveloper
	}
	for _, p := range []string{"/projects", "/schedules", "/domains", "/logs", "/monitoring"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			// The downloads under /logs are files, not pages.
			if path == "/logs/download" {
				return ""
			}
			return httpx.ModeDeveloper
		}
	}
	return ""
}

// modeOf is the view a page is drawn in: the one its path belongs to, else the
// one the cookie remembers, else Developer.
func (s *server) modeOf(r *http.Request) string {
	if m := pathMode(r.URL.Path); m != "" {
		return m
	}
	return httpx.ModeCookie(r)
}

// rememberMode sets the mode cookie on every page of a view, so the pages both
// views share know which sidebar to draw. Only GET and HEAD pages set it: a
// form's answer is drawn in the view the form was in and changes nothing.
func (s *server) rememberMode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if mode := pathMode(r.URL.Path); mode != "" {
				httpx.SetModeCookie(w, mode, s.proxies.HTTPS(r))
			}
		}
		next.ServeHTTP(w, r)
	})
}
