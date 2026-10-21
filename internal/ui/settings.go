package ui

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
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
	// Scope says what a deploy token is limited to ("app web of project shop");
	// "" for a full token.
	Scope string
}

// newToken is the token just created, shown once. ExampleID is the app a deploy
// token's example reads, "" for a full token.
type newToken struct {
	Name      string
	Value     string
	ExampleID string
}

// scopeForm is the "Only for one app" part of the new-token form: what was
// typed, and what is wrong with it. Projects lists the projects to choose from.
type scopeForm struct {
	Open     bool // the <details> is open: something was typed, or something is wrong
	Project  string
	App      string
	Projects []scopeProject
	AppErr   *fieldError
	ProjErr  *fieldError
}

// scopeProject is one project in the select.
type scopeProject struct {
	ID, Label string
	Selected  bool
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
	Scope           scopeForm
	NewToken        *newToken
	Dest            destinationForm
	Registries      registriesData
	Notifications   notificationsData
}

// destinationForm is the backup destination section: the saved destination
// (or what was typed, after a refusal), never the secret key.
type destinationForm struct {
	Endpoint, Region, Bucket, Prefix, AccessKeyID string
	HasSecret                                     bool // a secret key is kept: the field may stay empty
	Saved                                         bool
	Field                                         string // the field at fault, after a refusal
	Err                                           *fieldError
}

// destFieldView is one text field of the destination form.
type destFieldView struct {
	Name, Label, Value, Placeholder, Err string
}

// destField is the template function that builds one: name is the input's
// name, which is also the field a refusal names.
func destField(f destinationForm, name, label, value, placeholder string) destFieldView {
	v := destFieldView{Name: name, Label: label, Value: value, Placeholder: placeholder}
	if f.Field == name && f.Err != nil {
		v.Err = f.Err.Text
	}
	return v
}

// showSettings answers GET /settings: the 006 account page, inside the console's shell.
func (s *server) showSettings(w http.ResponseWriter, r *http.Request, session string) {
	data := settingsData{PasswordChanged: r.URL.Query().Get("password") == "changed"}
	data.Dest.Saved = r.URL.Query().Get("backups") == "saved"
	data.Registries.Saved = r.URL.Query().Get("registries") == "saved"
	data.Registries.Tested = r.URL.Query().Get("tested")
	data.Notifications.Saved = r.URL.Query().Get("notifications") == "saved"
	data.Notifications.Notified = r.URL.Query().Get("notified")
	s.renderSettings(w, r, data)
}

// setBackupDestination answers POST /settings/backups (feature 010). An
// empty secret key keeps the one saved before. A refusal draws the page
// again with what was typed, except the secret key.
func (s *server) setBackupDestination(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	in := service.DestinationInput{
		Endpoint: strings.TrimSpace(r.PostFormValue("endpoint")), Region: strings.TrimSpace(r.PostFormValue("region")),
		Bucket: strings.TrimSpace(r.PostFormValue("bucket")), Prefix: strings.TrimSpace(r.PostFormValue("prefix")),
		AccessKeyID: strings.TrimSpace(r.PostFormValue("accessKeyId")),
	}
	if v := r.PostFormValue("secretAccessKey"); v != "" {
		in.SecretAccessKey = &v
	}
	_, err := s.svc.SetBackupDestination(r.Context(), in)
	if err == nil {
		http.Redirect(w, r, "/settings?backups=saved#backups", http.StatusSeeOther)
		return
	}
	form := destinationForm{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix, AccessKeyID: in.AccessKeyID}
	if _, err := s.svc.GetBackupDestination(r.Context()); err == nil {
		form.HasSecret = true
	}
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr):
		form.Field, form.Err = fieldErr.Field, &fieldError{Text: fieldErr.Message}
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't save the backup destination", "error", err)
		form.Err = &fieldError{Text: "Nomad couldn't keep the secret key, so nothing was saved: " + err.Error()}
	default:
		s.serverError(w, "set backup destination", err)
		return
	}
	s.renderSettings(w, r, settingsData{Dest: form})
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
	head, err := s.shellPage(r, "Settings", navSettings, crumb{Label: "Settings"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	data.pageData = head
	data.Email = email
	data.Origin = s.origin(r)
	// Some tests serve the account pages without a deploy service.
	if data.Dest.Err == nil && s.svc != nil {
		d, err := s.svc.GetBackupDestination(r.Context())
		switch {
		case err == nil:
			data.Dest = destinationForm{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: d.Prefix,
				AccessKeyID: d.AccessKeyID, HasSecret: true, Saved: data.Dest.Saved}
		case !errors.Is(err, store.ErrNotFound):
			s.serverError(w, "read backup destination", err)
			return
		}
	}
	if s.svc != nil {
		if data.Registries, err = s.registriesSection(r, data.Registries); err != nil {
			s.serverError(w, "list registries", err)
			return
		}
		if data.Notifications, err = s.notificationsSection(r, data.Notifications); err != nil {
			s.serverError(w, "list notification channels", err)
			return
		}
	}
	if s.svc != nil {
		pl, err := s.loadPlaces(r.Context())
		if err != nil {
			s.serverError(w, "list projects", err)
			return
		}
		data.Scope.Projects = nil
		for _, pr := range pl.Projects {
			data.Scope.Projects = append(data.Scope.Projects, scopeProject{ID: pr.ID, Label: pr.DisplayName, Selected: pr.ID == data.Scope.Project})
		}
	}
	data.Tokens = make([]tokenRow, 0, len(infos))
	for _, i := range infos {
		row := tokenRow{
			ID: i.ID, Name: i.Name, State: i.State,
			Created:  whenOf(now, &i.CreatedAt, "never"),
			LastUsed: whenOf(now, i.LastUsedAt, "never"),
			Expires:  whenOf(now, i.ExpiresAt, "never"),
			Revoked:  whenOf(now, i.RevokedAt, ""),
		}
		if i.Scope != nil {
			row.Scope = "deploys app " + i.Scope.App + " of project " + i.Scope.Project
		}
		data.Tokens = append(data.Tokens, row)
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
	// "Only for one app" (a deploy token): an app name makes it one, in the chosen
	// project (Default's when the form had no select).
	var scope *service.TokenScope
	data.Scope = scopeForm{Project: r.PostFormValue("scopeProject"), App: strings.TrimSpace(r.PostFormValue("scopeApp"))}
	data.Scope.Open = data.Scope.App != ""
	if data.Scope.App != "" {
		project := data.Scope.Project
		if project == "" {
			project = app.DefaultProject
		}
		scope = &service.TokenScope{Project: project, App: data.Scope.App}
	}

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

	created, err := s.auth.CreateScopedAPIToken(r.Context(), name, expiresAt, scope)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		nt := &newToken{Name: created.Info.Name, Value: created.Token}
		if scope != nil {
			nt.ExampleID = app.Place{Project: scope.Project, Environment: app.DefaultEnvironment, Name: scope.App}.ID()
		}
		s.renderSettings(w, r, settingsData{NewToken: nt})
	case errors.Is(err, service.ErrTokenNameTaken):
		data.NameErr = &fieldError{Text: "An active token already has that name."}
		s.renderSettings(w, r, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "name":
		data.NameErr = &fieldError{Text: "The name " + fieldErr.Message + "."}
		s.renderSettings(w, r, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "expiresAt":
		data.ExpiresErr = &fieldError{Text: "The expiry " + fieldErr.Message + "."}
		s.renderSettings(w, r, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "scope.project":
		data.Scope.Open, data.Scope.ProjErr = true, &fieldError{Text: "The project " + strings.TrimPrefix(fieldErr.Message, "project ") + "."}
		s.renderSettings(w, r, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "scope.app":
		data.Scope.Open, data.Scope.AppErr = true, &fieldError{Text: "The app name " + strings.TrimPrefix(fieldErr.Message, `"`+data.Scope.App+`" `) + "."}
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
