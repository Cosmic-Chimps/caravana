package ui

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// fieldError is a sentence about one form field, shown under it.
type fieldError struct{ Text string }

// tokenRow is one API token in the list. Never the token's value.
type tokenRow struct {
	ID       string
	Name     string
	State    string // active | expired | revoked: picks the mark and pill
	Created  when
	LastUsed when
	Expires  when
	Revoked  when
}

// newToken is the token just created, shown once.
type newToken struct {
	Name  string
	Value string
}

type settingsData struct {
	pageData
	Email           string
	Origin          string // scheme://host, for the curl example
	PasswordChanged bool
	CurrentErr      *fieldError
	NewErr          *fieldError
	Tokens          []tokenRow
	TokenName       string // kept after a refused create
	TokenExpires    string
	NameErr         *fieldError
	ExpiresErr      *fieldError
	NewToken        *newToken
}

// showSettings answers GET /settings: the 006 account page, inside the console's shell.
func (s *server) showSettings(w http.ResponseWriter, r *http.Request, session string) {
	data := settingsData{PasswordChanged: r.URL.Query().Get("password") == "changed"}
	s.renderSettings(w, r, data)
}

// renderSettings fills in what every Settings response shows (who, and the
// token list) and writes it inside the shell, with Settings marked current.
func (s *server) renderSettings(w http.ResponseWriter, r *http.Request, data settingsData) {
	email, err := s.auth.AdminEmail(r.Context())
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	infos, err := s.auth.ListAPITokens(r.Context())
	if err != nil {
		s.serverError(w, "list api tokens", err)
		return
	}
	now := s.now()
	head, err := s.shellPage(r, "Settings", "settings", crumb{Label: "Settings"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	data.pageData = head
	data.Email = email
	data.Origin = s.origin(r)
	data.Tokens = make([]tokenRow, 0, len(infos))
	for _, i := range infos {
		data.Tokens = append(data.Tokens, tokenRow{
			ID: i.ID, Name: i.Name, State: i.State,
			Created:  whenOf(now, &i.CreatedAt, "never"),
			LastUsed: whenOf(now, i.LastUsedAt, "never"),
			Expires:  whenOf(now, i.ExpiresAt, "never"),
			Revoked:  whenOf(now, i.RevokedAt, ""),
		})
	}
	s.render(w, http.StatusOK, "settings", data)
}

// changePassword answers POST /settings/password. Success goes back to the
// page with a confirmation (post/redirect/get); a refusal shows the page with
// a sentence under the field at fault.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	err := s.auth.ChangePassword(r.Context(), r.PostFormValue("currentPassword"), r.PostFormValue("newPassword"), session)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings?password=changed", http.StatusSeeOther)
	case errors.Is(err, service.ErrWrongPassword):
		s.renderSettings(w, r, settingsData{CurrentErr: &fieldError{Text: "That isn't the current password."}})
	case errors.As(err, &fieldErr) && fieldErr.Field == "newPassword":
		s.renderSettings(w, r, settingsData{NewErr: &fieldError{Text: "The new password " + fieldErr.Message + "."}})
	default:
		s.serverError(w, "change password", err)
	}
}

// createToken answers POST /settings/tokens. The new token is shown once, in
// this very response: a redirect would lose it (research R12).
func (s *server) createToken(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name, expires := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("expires")
	data := settingsData{TokenName: name, TokenExpires: expires}

	var expiresAt *time.Time
	if expires != "" {
		// A date, taken as the end of that day in UTC.
		day, err := time.Parse("2006-01-02", expires)
		if err != nil {
			data.ExpiresErr = &fieldError{Text: "The expiry must be a date."}
			s.renderSettings(w, r, data)
			return
		}
		expiresAt = new(day.Add(24*time.Hour - time.Second))
	}

	created, err := s.auth.CreateAPIToken(r.Context(), name, expiresAt)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		s.renderSettings(w, r, settingsData{NewToken: &newToken{Name: created.Info.Name, Value: created.Token}})
	case errors.Is(err, service.ErrTokenNameTaken):
		data.NameErr = &fieldError{Text: "An active token already has that name."}
		s.renderSettings(w, r, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "name":
		data.NameErr = &fieldError{Text: "The name " + fieldErr.Message + "."}
		s.renderSettings(w, r, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "expiresAt":
		data.ExpiresErr = &fieldError{Text: "The expiry " + fieldErr.Message + "."}
		s.renderSettings(w, r, data)
	default:
		s.serverError(w, "create api token", err)
	}
}

// revokeToken answers POST /settings/tokens/{tokenId}/revoke. The page asks
// for a confirmation first (a <details>, research R16); this is the post it
// sends. Revoking can't touch deployed apps, and a new token replaces it.
func (s *server) revokeToken(w http.ResponseWriter, r *http.Request, session string) {
	err := s.auth.RevokeAPIToken(r.Context(), r.PathValue("tokenId"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "revoke api token", err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// readForm parses a posted form with a size limit. It answers 400 itself and
// returns false when the form can't be read.
func (s *server) readForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.message(w, http.StatusBadRequest, "Bad request", "That form couldn't be read.", "Go back and try again.")
		return false
	}
	return true
}

// showAccount answers GET /account, the address 006 had: it is Settings now. A
// permanent redirect, so bookmarks and old links move over. It needs no
// session; /settings asks for one.
func (s *server) showAccount(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings", http.StatusMovedPermanently)
}
