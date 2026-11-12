package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Settings › Sign-in (feature 033, contracts/console.md): the passkeys, adding one, and whether the
// password may still sign in. The page holds no secret: a passkey is listed by our own ID, name and
// domain, never by the authenticator's credential ID, and the hidden fields the script fills (the
// ceremony and the credential) are empty in every response.

// passkeyRow is one passkey in the list.
type passkeyRow struct {
	ID, Name, Domain string
	Added, LastUsed  when
	Synced           bool
	// Elsewhere says it was made for another domain than this address's: it doesn't work here.
	Elsewhere bool
	// Last says it is the only passkey of its domain while password sign-in is off: it can't be
	// removed, and the row says why.
	Last bool
}

// signInView is the Sign-in page.
type signInView struct {
	// Place says whether passkeys work at this address, and the console's domain.
	Place passkeyPlace
	Rows  []passkeyRow
	// Confirmed says the session proved it is the owner in the last 10 minutes: only then may a
	// passkey be added.
	Confirmed bool
	// PasswordOn is the setting; PasswordWorks says the password form may confirm (it is on, or no
	// passkey exists for the console's domain).
	PasswordOn    bool
	PasswordWorks bool
	// HerePasskey says a passkey exists for this address's domain: "Use a passkey" can confirm.
	HerePasskey bool
	// CanTurnOff says a passkey exists for the console's domain, so password sign-in may be turned off.
	CanTurnOff bool
	// Name is what was typed in the Add form, kept after a refusal.
	Name string
	// NameErr is a sentence under the name field.
	NameErr *fieldError
	// Notice is the one sentence at the top: a success from a query flag, or why a post was refused.
	Notice *notice
}

// signInNotices are the sentences the success flags of the page's address draw.
var signInNotices = map[string]string{
	"added":    "Passkey added.",
	"renamed":  "Passkey renamed.",
	"removed":  "Passkey removed.",
	"saved":    "Saved.",
	"verified": "Confirmed. You can add a passkey now.",
}

// showSignIn answers GET /settings/sign-in.
func (s *server) showSignIn(w http.ResponseWriter, r *http.Request, session string) {
	var v signInView
	for flag, text := range signInNotices {
		if r.URL.Query().Get(flag) == "1" {
			v.Notice = &notice{Kind: "healthy", Text: text}
		}
	}
	s.renderSignIn(w, r, session, http.StatusOK, v)
}

// renderSignIn fills in what the page shows from the service (the passkeys, whether the session is
// confirmed, the setting) around what the handler set (a notice, the typed name) and draws it.
func (s *server) renderSignIn(w http.ResponseWriter, r *http.Request, session string, status int, v signInView) {
	ctx := r.Context()
	v.Place = s.passkeyPlace(r)
	list, err := s.auth.ListPasskeys(ctx)
	if err != nil {
		s.serverError(w, r, "list passkeys", err)
		return
	}
	settings, err := s.auth.SignInSettings(ctx)
	if err != nil {
		s.serverError(w, r, "read sign-in settings", err)
		return
	}
	if v.Confirmed, err = s.auth.SessionConfirmed(ctx, session); err != nil {
		s.serverError(w, r, "check the session", err)
		return
	}
	if v.PasswordWorks, err = s.auth.PasswordSignInAllowed(ctx, v.Place.Console); err != nil {
		s.serverError(w, r, "read sign-in settings", err)
		return
	}
	v.PasswordOn = settings.PasswordSignIn

	now := s.now()
	perDomain := map[string]int{}
	for _, p := range list {
		perDomain[p.Domain]++
	}
	for _, p := range list {
		row := passkeyRow{ID: p.ID, Name: p.Name, Domain: p.Domain, Synced: p.BackedUp,
			Added:     whenOf(now, &p.CreatedAt, ""),
			LastUsed:  whenOf(now, p.LastUsedAt, "never"),
			Elsewhere: p.Domain != v.Place.RP.ID,
			Last:      !settings.PasswordSignIn && perDomain[p.Domain] == 1,
		}
		row.Added.Text = p.CreatedAt.UTC().Format("Jan 2")
		v.Rows = append(v.Rows, row)
		if p.Domain == v.Place.RP.ID {
			v.HerePasskey = true
		}
		if p.Domain == v.Place.Console {
			v.CanTurnOff = true
		}
	}
	data := settingsData{SignIn: v, Status: status}
	s.renderSettings(w, r, entrySignIn, data)
}

// refuseSignIn draws the page again with one sentence, at the status the refusal has.
func (s *server) refuseSignIn(w http.ResponseWriter, r *http.Request, session string, status int, kind, text string, v signInView) {
	v.Notice = &notice{Kind: kind, Text: text}
	s.renderSignIn(w, r, session, status, v)
}

// verifyPasskeyOptions answers POST /settings/sign-in/verify/options: a confirm ceremony with a
// passkey made at this address's domain.
func (s *server) verifyPasskeyOptions(w http.ResponseWriter, r *http.Request, session string) {
	place := s.passkeyPlace(r)
	if !place.Available {
		passkeyOptionsUnavailable(w, place)
		return
	}
	opts, err := s.auth.BeginPasskeyConfirm(r.Context(), session, place.RP, s.proxies.ClientAddress(r))
	switch {
	case errors.Is(err, service.ErrNoPasskey):
		writeJSONError(w, http.StatusConflict, "no_passkey", "No passkey was made for "+place.RP.ID+" yet.")
	case errors.Is(err, service.ErrNotAuthenticated):
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "Your session ended. Sign in again.")
	case err != nil:
		s.log.Error("begin passkey confirm", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal", "internal error")
	default:
		writeOptions(w, opts)
	}
}

// verifyYou answers POST /settings/sign-in/verify: "Confirm it's you", with the password, or with
// the ceremony and credential a passkey made. Success answers 303 to the Add panel; a failure draws
// the page with one sentence, counted against the address like a failed sign-in.
func (s *server) verifyYou(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	place := s.passkeyPlace(r)
	var err error
	byPasskey := r.PostFormValue("ceremony") != ""
	if byPasskey {
		if !place.Available {
			s.refuseSignIn(w, r, session, http.StatusConflict, "warning", place.Why, signInView{})
			return
		}
		err = s.auth.FinishPasskeyConfirm(r.Context(), session, place.RP, r.PostFormValue("ceremony"),
			[]byte(r.PostFormValue("credential")), s.proxies.ClientAddress(r))
	} else {
		err = s.auth.VerifyPassword(r.Context(), session, r.PostFormValue("password"), place.Console, s.proxies.ClientAddress(r))
	}
	var passwordFailure *service.SigninFailure
	var passkeyFailure *service.PasskeyFailure
	var limited *service.SigninLimitError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/sign-in?verified=1#add", http.StatusSeeOther)
	case errors.As(err, &limited):
		s.refuseSignIn(w, r, session, http.StatusOK, "failed", limitedSentence(limited.RetryAfter.Minutes()), signInView{})
	case errors.As(err, &passwordFailure):
		s.refuseSignIn(w, r, session, http.StatusOK, "warning", triesSentence("That isn't your password.", passwordFailure.TriesLeft), signInView{})
	case errors.As(err, &passkeyFailure):
		s.refuseSignIn(w, r, session, http.StatusOK, "warning", passkeyFailureSentence(passkeyFailure.TriesLeft), signInView{})
	case errors.Is(err, service.ErrPasswordSignInOff):
		s.refuseSignIn(w, r, session, http.StatusConflict, "warning", "Password sign-in is off. Confirm with a passkey.", signInView{})
	case errors.Is(err, service.ErrNotAuthenticated):
		http.Redirect(w, r, "/login?expired=1", http.StatusSeeOther)
	default:
		s.serverError(w, r, "confirm the session", err)
	}
}

// addPasskeyOptions answers POST /settings/passkeys/options: a registration ceremony. The session
// must be confirmed.
func (s *server) addPasskeyOptions(w http.ResponseWriter, r *http.Request, session string) {
	place := s.passkeyPlace(r)
	if !place.Available {
		passkeyOptionsUnavailable(w, place)
		return
	}
	opts, err := s.auth.BeginPasskeyRegistration(r.Context(), session, place.RP, s.proxies.ClientAddress(r))
	switch {
	case errors.Is(err, service.ErrNotConfirmed):
		writeJSONError(w, http.StatusForbidden, "not_confirmed", service.ErrNotConfirmed.Error())
	case errors.Is(err, service.ErrNotAuthenticated):
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "Your session ended. Sign in again.")
	case err != nil:
		s.log.Error("begin passkey registration", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal", "internal error")
	default:
		writeOptions(w, opts)
	}
}

// addPasskey answers POST /settings/passkeys: the name, and the ceremony and credential the
// script posts. Success answers 303; a name or a passkey the service refuses draws the page (422)
// with the typed name kept.
func (s *server) addPasskey(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name := cleanName(r.PostFormValue("name"))
	kept := signInView{Name: name}
	place := s.passkeyPlace(r)
	if !place.Available {
		s.refuseSignIn(w, r, session, http.StatusConflict, "warning", place.Why, kept)
		return
	}
	_, err := s.auth.FinishPasskeyRegistration(r.Context(), session, place.RP, name, r.PostFormValue("ceremony"),
		[]byte(r.PostFormValue("credential")))
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/sign-in?added=1", http.StatusSeeOther)
	case errors.Is(err, service.ErrNotConfirmed):
		s.refuseSignIn(w, r, session, http.StatusForbidden, "warning", "Confirm it's you first.", kept)
	case errors.Is(err, service.ErrPasskeyRefused):
		s.refuseSignIn(w, r, session, http.StatusUnprocessableEntity, "failed", "That passkey couldn't be added. Nothing was saved. Try again.", kept)
	case errors.Is(err, service.ErrNotAuthenticated):
		http.Redirect(w, r, "/login?expired=1", http.StatusSeeOther)
	default:
		if sentence, ok := nameSentence(err); ok {
			kept.NameErr = &fieldError{Text: sentence}
			s.renderSignIn(w, r, session, http.StatusUnprocessableEntity, kept)
			return
		}
		s.serverError(w, r, "add a passkey", err)
	}
}

// renamePasskey answers POST /settings/passkeys/{id}.
func (s *server) renamePasskey(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	_, err := s.auth.RenamePasskey(r.Context(), r.PathValue("id"), r.PostFormValue("name"))
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/sign-in?renamed=1", http.StatusSeeOther)
	case errors.Is(err, service.ErrPasskeyNotFound):
		s.refuseSignIn(w, r, session, http.StatusNotFound, "warning", "That passkey is already gone.", signInView{})
	default:
		if sentence, ok := nameSentence(err); ok {
			s.refuseSignIn(w, r, session, http.StatusUnprocessableEntity, "failed", sentence, signInView{})
			return
		}
		s.serverError(w, r, "rename a passkey", err)
	}
}

// removePasskey answers POST /settings/passkeys/{id}/remove. The page asks first (a <details>).
func (s *server) removePasskey(w http.ResponseWriter, r *http.Request, session string) {
	err := s.auth.RemovePasskey(r.Context(), r.PathValue("id"))
	var de *service.PasskeyDomainError
	switch {
	case err == nil, errors.Is(err, service.ErrPasskeyNotFound):
		http.Redirect(w, r, "/settings/sign-in?removed=1", http.StatusSeeOther)
	case errors.As(err, &de) && errors.Is(err, service.ErrLastPasskey):
		s.refuseSignIn(w, r, session, http.StatusConflict, "warning", lastPasskeySentence(de.Domain), signInView{})
	default:
		s.serverError(w, r, "remove a passkey", err)
	}
}

// lastPasskeySentence says why the last passkey of a domain can't go.
func lastPasskeySentence(domain string) string {
	return fmt.Sprintf("That is your only passkey for %s, and password sign-in is off. Turn password sign-in on first, or add another passkey.", domain)
}

// setPasswordSignIn answers POST /settings/sign-in: password=on or off.
func (s *server) setPasswordSignIn(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	on := true
	switch strings.TrimSpace(r.PostFormValue("password")) {
	case "on":
	case "off":
		on = false
	default:
		s.refuseSignIn(w, r, session, http.StatusUnprocessableEntity, "failed", "Choose on or off.", signInView{})
		return
	}
	domain := s.consoleDomain(r)
	_, err := s.auth.SetPasswordSignIn(r.Context(), on, domain)
	var de *service.PasskeyDomainError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/sign-in?saved=1", http.StatusSeeOther)
	case errors.As(err, &de) && errors.Is(err, service.ErrNoPasskey):
		s.refuseSignIn(w, r, session, http.StatusConflict, "warning",
			"Add a passkey for "+domain+" before turning password sign-in off.", signInView{})
	default:
		s.serverError(w, r, "set password sign-in", err)
	}
}
