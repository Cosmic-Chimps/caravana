package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Passkeys in the console (feature 033, specs/033-passkeys/contracts/console.md). Signing in with a
// passkey and adding one are ceremonies between the browser, the owner's authenticator and this
// console's own origin, so they are console routes only (plan, Complexity Tracking): /login/passkey*,
// /settings/sign-in/verify* and /settings/passkeys*. Renaming, removing and the password setting
// are the same service calls as the API's updatePasskey, deletePasskey and putSignIn.
//
// Nothing the browser posts for a ceremony (the credential, the ceremony's ID) is ever drawn into a
// page, put in a redirect address or written to the log. The hidden fields of the forms are empty
// until static/js/passkeys.js fills them, and a refused post draws them empty again.

// passkeyPlace is where a request stands as far as passkeys are concerned (research R2, R3).
type passkeyPlace struct {
	// RP is the domain and origin the browser addressed.
	RP service.RelyingParty
	// Available says passkeys can be used at this address. Why says where they can be when not.
	Available bool
	Why       string
	// Console is the console's own domain: the host of CARAVANA_PUBLIC_URL when it is set, else the
	// host the browser addressed. Password sign-in is judged against passkeys made there.
	Console string
}

// passkeyPlace decides the relying party from the request. The domain is the host the browser
// addressed, without port, lower case. Trusting Host is safe: an assertion carries the SHA-256 of
// the RP ID and the origin it was made at, so a forged Host only matches a passkey made at that
// domain. Passkeys are offered only at a domain name (browsers refuse an IP address as an RP ID),
// over HTTPS or at localhost (a secure context), and, when the public address is set, only at its
// host or at localhost.
func (s *server) passkeyPlace(r *http.Request) passkeyPlace {
	host := requestHostname(r)
	scheme := "http"
	if s.proxies.HTTPS(r) {
		scheme = "https"
	}
	p := passkeyPlace{
		RP:      service.RelyingParty{ID: host, Origin: scheme + "://" + strings.ToLower(r.Host)},
		Console: host,
	}
	publicHost := s.publicHost()
	if publicHost != "" {
		p.Console = publicHost
	}

	localhost := host == "localhost" || strings.HasSuffix(host, ".localhost")
	_, notADomain := netip.ParseAddr(host)
	switch {
	case notADomain == nil, !plainDomain(host):
		p.Why = p.whereTheyWork(s, r)
	case scheme != "https" && !localhost:
		p.Why = p.whereTheyWork(s, r)
	case publicHost != "" && host != publicHost && !localhost:
		p.Why = p.whereTheyWork(s, r)
	default:
		p.Available = true
	}
	return p
}

// plainDomain says host is a domain name in its plain ASCII form: letters, digits, hyphens and dots.
// A passkey's domain is hashed as it is written, so anything else (a name in Unicode, an odd
// character) could never match what the browser signs.
func plainDomain(host string) bool {
	if host == "" {
		return false
	}
	for _, c := range host {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

// whereTheyWork is the sentence for an address that can't use passkeys: the public address when
// there is one, else what to open instead.
func (p passkeyPlace) whereTheyWork(s *server, r *http.Request) string {
	if u := strings.TrimSpace(s.publicURL()); u != "" {
		return "Passkeys work at " + strings.TrimSuffix(u, "/") + ", this caravana's own address."
	}
	port := "7070"
	if _, got, err := net.SplitHostPort(r.Host); err == nil && got != "" {
		port = got
	}
	return "Passkeys work at the console's https domain or at http://localhost:" + port + ". Browsers don't accept an IP address or a plain http address for them."
}

// publicHost is the host of CARAVANA_PUBLIC_URL, lower case and without a trailing dot; "" when
// none is set.
func (s *server) publicHost() string {
	u, err := url.Parse(strings.TrimSpace(s.publicURL()))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// consoleDomain is the console's domain for the request: what SignIn, VerifyPassword,
// SetPasswordSignIn and PasswordSignInAllowed judge password sign-in against.
func (s *server) consoleDomain(r *http.Request) string { return s.passkeyPlace(r).Console }

// writeJSONError answers a script's call with {"error": {"code", "message"}} and no caching.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	writeConsoleJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// writeConsoleJSON answers a script's call: JSON that is never cached.
func writeConsoleJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the status is sent; our values always encode
}

// writeOptions answers an options call: the ceremony to send back and the publicKey object for
// navigator.credentials.
func writeOptions(w http.ResponseWriter, o service.PasskeyOptions) {
	writeConsoleJSON(w, http.StatusOK, struct {
		Ceremony  string          `json:"ceremony"`
		PublicKey json.RawMessage `json:"publicKey"`
	}{o.Ceremony, o.Options})
}

// requireSessionJSON is requireSession for a script's call: a signed-out caller gets 401 JSON, not
// a redirect to a page the script would try to read as JSON.
func (s *server) requireSessionJSON(next sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		value, state, err := s.current(r)
		if err != nil {
			s.log.Error("check session", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if state != validSession {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "Your session ended. Sign in again.")
			return
		}
		next(w, r, value)
	}
}

// ---- Signing in ----------------------------------------------------------------------------

// passkeyOptionsUnavailable answers an options call from an address that can't use passkeys.
func passkeyOptionsUnavailable(w http.ResponseWriter, p passkeyPlace) {
	writeJSONError(w, http.StatusConflict, "passkeys_unavailable", p.Why)
}

// loginPasskeyOptions answers POST /login/passkey/options: a sign-in ceremony for the script. It
// needs no session. Asking is not counted against the address.
func (s *server) loginPasskeyOptions(w http.ResponseWriter, r *http.Request) {
	place := s.passkeyPlace(r)
	if !place.Available {
		passkeyOptionsUnavailable(w, place)
		return
	}
	opts, err := s.auth.BeginPasskeySignIn(r.Context(), place.RP, s.proxies.ClientAddress(r))
	switch {
	case errors.Is(err, service.ErrNoAdmin):
		writeJSONError(w, http.StatusConflict, "no_admin", service.ErrNoAdmin.Error())
	case err != nil:
		s.log.Error("begin passkey sign-in", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal", "internal error")
	default:
		writeOptions(w, opts)
	}
}

// passkeySignIn answers POST /login/passkey: the script's form, with the ceremony and the
// credential the authenticator made. Success sets the session cookie and answers 303; a failure
// draws /login again with one sentence (the tries left, or the wait), as a wrong password does.
func (s *server) passkeySignIn(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) {
		return
	}
	next := safeNext(r.PostFormValue("next"))
	place := s.passkeyPlace(r)
	data := s.loginPage(r)
	data.Next = next
	if !place.Available {
		data.Notice = &notice{Kind: "warning", Text: place.Why}
		s.renderLogin(w, r, data)
		return
	}

	session, err := s.auth.FinishPasskeySignIn(r.Context(), place.RP, r.PostFormValue("ceremony"),
		[]byte(r.PostFormValue("credential")), s.proxies.ClientAddress(r))
	if err == nil {
		httpx.SetSessionCookie(w, session.Value, session.ExpiresAt, s.proxies.HTTPS(r))
		if next == "" {
			next = "/apps"
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	var failure *service.PasskeyFailure
	var limited *service.SigninLimitError
	switch {
	case errors.Is(err, service.ErrNoAdmin):
		// renderLogin shows the host command.
	case errors.As(err, &limited):
		data.Notice = &notice{Kind: "failed", Text: limitedSentence(limited.RetryAfter.Minutes())}
	case errors.As(err, &failure):
		data.Notice = &notice{Kind: "warning", Text: passkeyFailureSentence(failure.TriesLeft)}
	default:
		s.serverError(w, r, "sign in with a passkey", err)
		return
	}
	s.renderLogin(w, r, data)
}

// passkeyFailureSentence is the answer to a passkey that didn't work: the password sentence's
// shape, with the tries left.
func passkeyFailureSentence(triesLeft int) string {
	return triesSentence("That passkey didn't work here.", triesLeft)
}

// triesSentence puts how many tries remain after base; 0 means the service didn't say.
func triesSentence(base string, triesLeft int) string {
	switch {
	case triesLeft <= 0:
		return base
	case triesLeft == 1:
		return base + " 1 try left before a 15-minute wait."
	default:
		return fmt.Sprintf("%s %d tries left before a 15-minute wait.", base, triesLeft)
	}
}

// ---- Names ---------------------------------------------------------------------------------

// nameSentence is a field error of a passkey's name as a sentence.
func nameSentence(err error) (string, bool) {
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "name" {
		return "", false
	}
	return "The name " + fe.Message + ".", true
}

// cleanName is what a name field shows again after a refusal: as typed, cut to a size a page
// can't be flooded with.
func cleanName(name string) string {
	if utf8.RuneCountInString(name) > 200 {
		return string([]rune(name)[:200])
	}
	return name
}
