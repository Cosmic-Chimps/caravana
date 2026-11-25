package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// authenticate wraps a handler of a protected route. The request must carry
// credentials, or the handler never runs and the client gets the one 401 every
// credential failure gets, whatever the reason (research R11).
//
// A quiet route (see route) checks a session without renewing it.
//
// A deploy token (an API token with a scope, feature 020) is held to scopeRules
// right here, after it is known to be valid and before any handler runs: out of
// scope is 403 token_scope. Then the person behind the request, whatever the
// credential, is held to the route's access rule (feature 053): 403 not_allowed.
func (s *server) authenticate(rt route) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		who, err := s.checkCredentials(r, rt.quiet)
		if err != nil {
			return err
		}
		// Who is asking travels with the request: the person, their roles and, for a token, its
		// name and scope. A deploy made with a token records its name; a session has none.
		ctx := service.WithPrincipal(r.Context(), who)
		if who.TokenName != "" {
			ctx = service.WithTokenName(ctx, who.TokenName)
		}
		r = r.WithContext(ctx)
		// A write route's audit scope (feature 057) learns who is asking before anything can refuse the
		// request, so the entry of a change made and then failed names its actor.
		sc := service.AuditScopeFrom(ctx)
		if sc != nil {
			sc.SetActor(who)
		}
		if who.Scope != nil {
			if err := scopeAllows(r, rt, *who.Scope); err != nil {
				return err
			}
		}
		target, err := s.accessAllows(r, rt)
		if err != nil {
			return err
		}
		if sc != nil {
			sc.SetFallback(fallbackResource(target))
		}
		return rt.handle(w, r)
	}
}

// checkCredentials returns who r acts as when it may use the API: the person, with their
// roles, and for a token its scope (nil for a session and for a full token) and name ("" for a
// session).
//
// An Authorization header means an API token, and only that: it must be
// exactly one header with a valid token, and a session cookie sent along
// doesn't rescue it. Without the
// header, a valid session cookie (the web UI's calls) is enough, but then a
// state-changing request must not come from another site (research R7).
func (s *server) checkCredentials(r *http.Request, quiet bool) (service.Principal, error) {
	switch len(r.Header.Values("Authorization")) {
	case 0:
	case 1:
		return s.auth.CheckAPIToken(r.Context(), bearerToken(r))
	default:
		return service.Principal{}, service.ErrNotAuthenticated
	}

	value, ok := httpx.SessionCookie(r, s.proxies.HTTPS(r))
	if !ok {
		return service.Principal{}, service.ErrNotAuthenticated
	}
	check := s.auth.CheckSession
	if quiet {
		check = s.auth.CheckSessionQuiet
	}
	who, err := check(r.Context(), value)
	if err != nil {
		return service.Principal{}, err
	}
	// A browser tells whether the request came from another site, in headers a
	// page on another site can't forge; http.CrossOriginProtection reads them.
	// Requests without those headers (curl, scripts) aren't from a browser and
	// pass, and so do GET, HEAD and OPTIONS, which change nothing. A Bearer
	// token never reaches this check.
	if err := s.csrf.Check(r); err != nil {
		return service.Principal{}, &Error{Status: http.StatusForbidden, Code: codeCrossOrigin,
			Message: "this request came from another site, so it was refused; nothing was changed"}
	}
	return who, nil
}

// callerID is the ID of the person the request acts as, for the handlers of their own account
// (password, passkeys, tokens). authenticate puts the principal there before any handler runs;
// a request without one is a bug, answered 500.
func callerID(r *http.Request) (string, error) {
	who, ok := service.PrincipalFrom(r.Context())
	if !ok {
		return "", errors.New("no principal in the request: the route isn't behind authenticate")
	}
	return who.PersonID, nil
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

// credentialCheck returns a function that asks again, later, whether the credentials
// r came with are still good, without renewing a session. An open event
// stream calls it now and then, to end when they stop being.
//
// The person's rights are asked again too (feature 053): a stream ends when the person no
// longer may do what opened it (their role was taken away, or lowered below the route's
// need), which counts as their credentials ending (service.ErrNotAuthenticated).
func (s *server) credentialCheck(r *http.Request) func(context.Context) error {
	var check func(context.Context) (service.Principal, error)
	if len(r.Header.Values("Authorization")) == 1 {
		token := bearerToken(r)
		check = func(ctx context.Context) (service.Principal, error) { return s.auth.CheckAPIToken(ctx, token) }
	} else {
		value, _ := httpx.SessionCookie(r, s.proxies.HTTPS(r))
		check = func(ctx context.Context) (service.Principal, error) { return s.auth.CheckSessionQuiet(ctx, value) }
	}
	rule, listed := accessRules[r.Pattern]
	return func(ctx context.Context) error {
		who, err := check(ctx)
		if err != nil {
			return err
		}
		if !listed {
			return nil
		}
		need, target, ok := rule(r)
		if !ok {
			return nil
		}
		if err := s.svc.AllowPrincipal(who, need, target); err != nil {
			return fmt.Errorf("the person's role no longer allows this (%v): %w", err, service.ErrNotAuthenticated)
		}
		return nil
	}
}
