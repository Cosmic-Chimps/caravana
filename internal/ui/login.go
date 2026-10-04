package ui

import (
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// maxFormBytes bounds a form post: a password and an email are tiny.
const maxFormBytes = 64 << 10

// notice is a sentence with its status mark first. Kind is the mark's shape:
// healthy, warning, failed or idle.
type notice struct {
	Kind string
	Text string
}

type loginData struct {
	pageData
	Host    string // the host being signed in to, as the browser addressed it
	Email   string // kept after a failed try; the password never is
	Next    string // where to go after signing in, already checked
	NoAdmin bool   // no admin yet: show the host command instead of the form
	Notice  *notice
}

func (s *server) loginPage(r *http.Request) loginData {
	return loginData{pageData: s.page("Sign in"), Host: r.Host}
}

// showLogin answers GET /login.
func (s *server) showLogin(w http.ResponseWriter, r *http.Request) {
	if _, state, err := s.current(r); err != nil {
		s.serverError(w, "check session", err)
		return
	} else if state == validSession {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}

	data := s.loginPage(r)
	data.Next = safeNext(r.URL.Query().Get("next"))
	if r.URL.Query().Get("expired") == "1" {
		data.Notice = &notice{Kind: "idle", Text: "Your session ended. Sign in again."}
	}
	s.renderLogin(w, r, data)
}

// renderLogin shows the form, or the host command when no admin exists yet.
func (s *server) renderLogin(w http.ResponseWriter, r *http.Request, data loginData) {
	if _, err := s.auth.AdminEmail(r.Context()); errors.Is(err, service.ErrNoAdmin) {
		data.NoAdmin = true
		data.Notice = nil
	} else if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, http.StatusOK, "login", data)
}

// signIn answers POST /login. A failure shows the form again (200) with the
// email kept and one sentence about what happened; success sets the session
// cookie and sends the browser on (post/redirect/get).
func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) {
		return
	}
	email, password := r.PostFormValue("email"), r.PostFormValue("password")
	next := safeNext(r.PostFormValue("next"))

	session, err := s.auth.SignIn(r.Context(), email, password, s.proxies.ClientAddress(r))
	if err == nil {
		httpx.SetSessionCookie(w, session.Value, session.ExpiresAt, s.proxies.HTTPS(r))
		if next == "" {
			next = "/account"
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}

	data := s.loginPage(r)
	data.Email, data.Next = email, next
	var failure *service.SigninFailure
	var limited *service.SigninLimitError
	switch {
	case errors.Is(err, service.ErrNoAdmin):
		// renderLogin shows the host command.
	case errors.As(err, &limited):
		data.Notice = &notice{Kind: "failed", Text: limitedSentence(limited.RetryAfter.Minutes())}
	case errors.As(err, &failure):
		data.Notice = &notice{Kind: "warning", Text: failureSentence(failure.TriesLeft)}
	default:
		s.serverError(w, "sign in", err)
		return
	}
	s.renderLogin(w, r, data)
}

// failureSentence is the answer to a wrong email or password. triesLeft is how
// many tries remain before the wait; 0 means the service didn't say.
func failureSentence(triesLeft int) string {
	const base = "That email and password don't match."
	switch {
	case triesLeft <= 0:
		return base
	case triesLeft == 1:
		return base + " 1 try left before a 15-minute wait."
	default:
		return fmt.Sprintf("%s %d tries left before a 15-minute wait.", base, triesLeft)
	}
}

// limitedSentence tells how long to wait, in whole minutes rounded up.
func limitedSentence(minutes float64) string {
	n := max(1, int(math.Ceil(minutes)))
	unit := "minutes"
	if n == 1 {
		unit = "minute"
	}
	return fmt.Sprintf("Too many failed sign-ins from this address. Try again in %d %s.", n, unit)
}

// signOut answers POST /logout: the session ends on the server, the cookie is
// cleared, and the browser goes to the sign-in page.
func (s *server) signOut(w http.ResponseWriter, r *http.Request) {
	https := s.proxies.HTTPS(r)
	if value, ok := httpx.SessionCookie(r, https); ok {
		if err := s.auth.SignOut(r.Context(), value); err != nil {
			s.serverError(w, "sign out", err)
			return
		}
	}
	httpx.ClearSessionCookie(w, https)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
