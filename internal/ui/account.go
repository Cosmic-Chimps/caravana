package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// fieldError is a sentence about one form field, shown under it.
type fieldError struct{ Text string }

// when is a time as the page shows it: a relative phrase, with the exact time
// for the title attribute (hover).
type when struct {
	Text  string
	Exact string
}

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

type accountData struct {
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

// showAccount answers GET /account: the signed-in landing page.
func (s *server) showAccount(w http.ResponseWriter, r *http.Request, session string) {
	data := accountData{PasswordChanged: r.URL.Query().Get("password") == "changed"}
	s.renderAccount(w, r, data)
}

// renderAccount fills in what every account page shows (who, and the token
// list) and writes it.
func (s *server) renderAccount(w http.ResponseWriter, r *http.Request, data accountData) {
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
	data.pageData = s.page("Account")
	data.Email = email
	data.Origin = "http://" + r.Host
	if s.proxies.HTTPS(r) {
		data.Origin = "https://" + r.Host
	}
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
	s.render(w, http.StatusOK, "account", data)
}

// whenOf describes t relative to now; a nil t is the given words.
func whenOf(now time.Time, t *time.Time, none string) when {
	if t == nil {
		return when{Text: none}
	}
	return when{Text: relative(now, *t), Exact: t.UTC().Format("2006-01-02 15:04:05 UTC")}
}

// relative is "5 minutes ago" or "in 3 days"; past two months, the date.
func relative(now, t time.Time) string {
	d := now.Sub(t)
	future := d < 0
	if future {
		d = -d
	}
	var text string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		text = plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		text = plural(int(d/time.Hour), "hour")
	case d < 60*24*time.Hour:
		text = plural(int(d/(24*time.Hour)), "day")
	default:
		return t.UTC().Format("2006-01-02")
	}
	if future {
		return "in " + text
	}
	return text + " ago"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// changePassword answers POST /account/password. Success goes back to the
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
		http.Redirect(w, r, "/account?password=changed", http.StatusSeeOther)
	case errors.Is(err, service.ErrWrongPassword):
		s.renderAccount(w, r, accountData{CurrentErr: &fieldError{Text: "That isn't the current password."}})
	case errors.As(err, &fieldErr) && fieldErr.Field == "newPassword":
		s.renderAccount(w, r, accountData{NewErr: &fieldError{Text: "The new password " + fieldErr.Message + "."}})
	default:
		s.serverError(w, "change password", err)
	}
}

// createToken answers POST /account/tokens. The new token is shown once, in
// this very response: a redirect would lose it (research R12).
func (s *server) createToken(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name, expires := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("expires")
	data := accountData{TokenName: name, TokenExpires: expires}

	var expiresAt *time.Time
	if expires != "" {
		// A date, taken as the end of that day in UTC.
		day, err := time.Parse("2006-01-02", expires)
		if err != nil {
			data.ExpiresErr = &fieldError{Text: "The expiry must be a date."}
			s.renderAccount(w, r, data)
			return
		}
		expiresAt = new(day.Add(24*time.Hour - time.Second))
	}

	created, err := s.auth.CreateAPIToken(r.Context(), name, expiresAt)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		s.renderAccount(w, r, accountData{NewToken: &newToken{Name: created.Info.Name, Value: created.Token}})
	case errors.Is(err, service.ErrTokenNameTaken):
		data.NameErr = &fieldError{Text: "An active token already has that name."}
		s.renderAccount(w, r, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "name":
		data.NameErr = &fieldError{Text: "The name " + fieldErr.Message + "."}
		s.renderAccount(w, r, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "expiresAt":
		data.ExpiresErr = &fieldError{Text: "The expiry " + fieldErr.Message + "."}
		s.renderAccount(w, r, data)
	default:
		s.serverError(w, "create api token", err)
	}
}

// revokeToken answers POST /account/tokens/{id}/revoke. It is reversible by
// creating a new token and can't touch deployed apps, so it needs no modal.
func (s *server) revokeToken(w http.ResponseWriter, r *http.Request, session string) {
	err := s.auth.RevokeAPIToken(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "revoke api token", err)
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
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
