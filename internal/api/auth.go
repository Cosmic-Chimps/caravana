package api

import (
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// authenticate wraps a handler of a protected route. The request must carry
// credentials, or the handler never runs and the client gets the one 401 every
// credential failure gets, whatever the reason (research R11).
func (s *server) authenticate(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if err := s.checkCredentials(r); err != nil {
			return err
		}
		return next(w, r)
	}
}

// checkCredentials returns nil when r may use the API.
//
// An Authorization header means an API token, and only that: it must be
// exactly one header with a valid token, and a session cookie sent along
// doesn't rescue it. Without the
// header, a valid session cookie (the web UI's calls) is enough, but then a
// state-changing request must not come from another site (research R7).
func (s *server) checkCredentials(r *http.Request) error {
	switch len(r.Header.Values("Authorization")) {
	case 0:
	case 1:
		return s.auth.CheckAPIToken(r.Context(), bearerToken(r))
	default:
		return service.ErrNotAuthenticated
	}

	value, ok := httpx.SessionCookie(r, s.proxies.HTTPS(r))
	if !ok {
		return service.ErrNotAuthenticated
	}
	if err := s.auth.CheckSession(r.Context(), value); err != nil {
		return err
	}
	// A browser tells whether the request came from another site, in headers a
	// page on another site can't forge; http.CrossOriginProtection reads them.
	// Requests without those headers (curl, scripts) aren't from a browser and
	// pass, and so do GET, HEAD and OPTIONS, which change nothing. A Bearer
	// token never reaches this check.
	if err := s.csrf.Check(r); err != nil {
		return &Error{Status: http.StatusForbidden, Code: codeCrossOrigin,
			Message: "this request came from another site, so it was refused; nothing was changed"}
	}
	return nil
}

// bearerToken returns the value after "Bearer " in the Authorization header,
// untouched, or "" for any other scheme. It is not trimmed: "Bearer  x" has a
// leading space, so it is not a token, and is refused like any other.
func bearerToken(r *http.Request) string {
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return value
}
