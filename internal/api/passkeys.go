package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Passkeys (feature 033): the five management operations of specs/033-passkeys/contracts/api.md.
// They are authenticated like every other route (a session or a full API token; a deploy token is
// refused by the scope middleware's default deny). The ceremonies, adding a passkey and signing in
// with one, are console routes only: no operation here starts one. No answer holds a credential
// ID, a public key or anything an authenticator keeps private: passkeyJSON has no field for them.

const (
	codePasskeyNotFound = "passkey_not_found"
	codeLastPasskey     = "last_passkey"
	codeNoPasskey       = "no_passkey"
)

// passkeysError maps the errors of the passkey calls. It returns nil for any other error.
func passkeysError(err error) *Error {
	switch {
	case errors.Is(err, service.ErrPasskeyNotFound):
		return &Error{Status: http.StatusNotFound, Code: codePasskeyNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrLastPasskey):
		return &Error{Status: http.StatusConflict, Code: codeLastPasskey, Message: err.Error()}
	case errors.Is(err, service.ErrNoPasskey):
		return &Error{Status: http.StatusConflict, Code: codeNoPasskey, Message: err.Error()}
	}
	return nil
}

// passkeyJSON is schema Passkey.
type passkeyJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Domain     string     `json:"domain"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"` // null until first used
	Transports []string   `json:"transports"`
	BackedUp   bool       `json:"backedUp"`
}

func toPasskeyJSON(p service.Passkey) passkeyJSON {
	transports := p.Transports
	if transports == nil {
		transports = []string{}
	}
	return passkeyJSON{ID: p.ID, Name: p.Name, Domain: p.Domain, CreatedAt: p.CreatedAt,
		LastUsedAt: p.LastUsedAt, Transports: transports, BackedUp: p.BackedUp}
}

// signInJSON is schema SignIn.
type signInJSON struct {
	PasswordSignIn bool `json:"passwordSignIn"`
	Passkeys       int  `json:"passkeys"`
}

// listPasskeys answers GET /v1/account/passkeys.
func (s *server) listPasskeys(w http.ResponseWriter, r *http.Request) error {
	list, err := s.auth.ListPasskeys(r.Context())
	if err != nil {
		return err
	}
	out := struct {
		Passkeys []passkeyJSON `json:"passkeys"`
	}{Passkeys: make([]passkeyJSON, 0, len(list))}
	for _, p := range list {
		out.Passkeys = append(out.Passkeys, toPasskeyJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// updatePasskey answers PATCH /v1/account/passkeys/{passkeyId}: a new name.
func (s *server) updatePasskey(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	p, err := s.auth.RenamePasskey(r.Context(), r.PathValue("passkeyId"), req.Name)
	if err != nil {
		return unprocessable(err)
	}
	writeJSON(w, http.StatusOK, toPasskeyJSON(p))
	return nil
}

// deletePasskey answers DELETE /v1/account/passkeys/{passkeyId}.
func (s *server) deletePasskey(w http.ResponseWriter, r *http.Request) error {
	if err := s.auth.RemovePasskey(r.Context(), r.PathValue("passkeyId")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// getSignIn answers GET /v1/account/sign-in.
func (s *server) getSignIn(w http.ResponseWriter, r *http.Request) error {
	st, err := s.auth.SignInSettings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, signInJSON{PasswordSignIn: st.PasswordSignIn, Passkeys: st.Passkeys})
	return nil
}

// putSignIn answers PUT /v1/account/sign-in. Turning password sign-in off needs a passkey made at
// the console's domain: CARAVANA_PUBLIC_URL's host, or any passkey when none is set.
func (s *server) putSignIn(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		PasswordSignIn *bool `json:"passwordSignIn"` // a pointer: leaving it out must not mean "off"
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.PasswordSignIn == nil {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: "passwordSignIn",
			Message: "passwordSignIn is required"}
	}
	st, err := s.auth.SetPasswordSignIn(r.Context(), *req.PasswordSignIn, s.svc.ConsoleHost())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, signInJSON{PasswordSignIn: st.PasswordSignIn, Passkeys: st.Passkeys})
	return nil
}
