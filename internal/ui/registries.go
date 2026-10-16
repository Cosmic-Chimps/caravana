package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Settings › Registries (feature 017): the credentials Caravana uses to read
// private images. Every form is a plain post, so the section works without
// script. The password is write-only: no struct here has a field for it, so no
// page can show it.

// registryCheck is the outcome of the last "Test" of a registry's credentials.
type registryCheck struct {
	OK      bool
	Message string // why it was refused or couldn't be reached; "" when OK
	When    when
}

// registryForm is what a Registries form shows: the host and user name as saved
// or as typed, and the field a refusal names. Never the password.
type registryForm struct {
	Host, Username string
	Field          string // the field at fault, after a refusal
	Err            *fieldError
}

// registryRow is one registry in the table.
type registryRow struct {
	Host, Username string
	Check          *registryCheck // nil until the credentials were tested
	Edit           registryForm   // what the row's edit form shows
	Open           bool           // the edit form was refused, so it is shown open
}

// registryNotice is a sentence under the section's heading.
type registryNotice struct {
	Text string
	Bad  bool // a refusal: shown as an alert with the failed mark
}

// registriesData is the whole section.
type registriesData struct {
	Rows   []registryRow
	Notice *registryNotice
	Alert  *fieldError // a refusal that names no field, such as Nomad being down
	Add    registryForm
	// EditHost and Edit say which row's edit form was refused, and how.
	EditHost string
	Edit     registryForm
	// Saved and Tested come from the redirect after a save or a test: Tested is
	// the host whose result to announce.
	Saved  bool
	Tested string
}

// registryFieldView is one input of a Registries form.
type registryFieldView struct {
	ID, Name, Label, Type, Value, Placeholder, Hint, Err string
}

// regField builds an input of a Registries form for the template. idPrefix
// keeps the ids of the add form and of each row's edit form apart; name is the
// input's name, which is also the field a refusal names. A password input is
// never given a value.
func regField(f registryForm, idPrefix, name, label, placeholder, hint string) registryFieldView {
	v := registryFieldView{ID: idPrefix + "-" + name, Name: name, Label: label, Type: "text", Placeholder: placeholder, Hint: hint}
	switch name {
	case "host":
		v.Value = f.Host
	case "username":
		v.Value = f.Username
	case "password":
		v.Type = "password"
	}
	if f.Field == name && f.Err != nil {
		v.Err = f.Err.Text
	}
	return v
}

// saveRegistry answers POST /settings/registries, for the add form and for a
// row's edit form (which also posts the registry's host and edit=1). An empty
// password keeps the one stored. A refusal draws the page again with what was
// typed, except the password.
func (s *server) saveRegistry(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	host, user := strings.TrimSpace(r.PostFormValue("host")), strings.TrimSpace(r.PostFormValue("username"))
	var password *string
	if v := r.PostFormValue("password"); v != "" {
		password = &v
	}
	edit := r.PostFormValue("edit") == "1"

	reg, err := s.svc.SaveRegistry(r.Context(), host, user, password)
	if err == nil {
		http.Redirect(w, r, "/settings?registries=saved#registries", http.StatusSeeOther)
		return
	}

	form := registryForm{Host: host, Username: user}
	data := registriesData{Add: form}
	if edit {
		data = registriesData{EditHost: host, Edit: form}
	}
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr):
		form.Field, form.Err = fieldErr.Field, &fieldError{Text: registryFieldMessage(fieldErr)}
		if edit {
			data.Edit = form
		} else {
			data.Add = form
		}
	case reg.Host != "":
		// Saved, but the running apps' copies couldn't be told. The service
		// says so in its own words.
		s.log.Warn("registry saved, but its copies weren't updated", "registry", reg.Host, "error", err)
		data = registriesData{Alert: &fieldError{Text: err.Error()}}
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't save the registry credentials", "registry", host, "error", err)
		data = registriesData{Add: data.Add, EditHost: data.EditHost, Edit: data.Edit,
			Alert: &fieldError{Text: "Nomad couldn't keep the credentials, so nothing was saved: " + err.Error()}}
	default:
		s.serverError(w, "save registry", err)
		return
	}
	s.renderSettings(w, r, settingsData{Registries: data})
}

// registryFieldMessage is a field error as a sentence for the input it names: the
// service's messages are written to follow the field's name ("is required").
func registryFieldMessage(fe *app.FieldError) string {
	name := map[string]string{"host": "The registry", "username": "The user name", "password": "The password or token"}[fe.Field]
	// A message about the value itself already starts with it: "acme" isn't a
	// registry host.
	if name == "" || strings.HasPrefix(fe.Message, "\"") {
		return fe.Message
	}
	return name + " " + fe.Message
}

// testRegistry answers POST /settings/registries/{host}/test. The outcome is
// stored, and the page it goes back to announces it.
func (s *server) testRegistry(w http.ResponseWriter, r *http.Request, session string) {
	reg, err := s.svc.TestRegistry(r.Context(), r.PathValue("host"))
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings?tested="+url.QueryEscape(reg.Host)+"#registries", http.StatusSeeOther)
	case errors.Is(err, store.ErrNotFound):
		http.Redirect(w, r, "/settings#registries", http.StatusSeeOther)
	default:
		s.serverError(w, "test registry", err)
	}
}

// removeRegistry answers POST /settings/registries/{host}/remove. The page asks
// first (a <details>); this is the post it sends. Apps already running keep
// running.
func (s *server) removeRegistry(w http.ResponseWriter, r *http.Request, session string) {
	err := s.svc.DeleteRegistry(r.Context(), r.PathValue("host"))
	switch {
	case err == nil, errors.Is(err, store.ErrNotFound):
		http.Redirect(w, r, "/settings#registries", http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't remove the registry credentials", "registry", r.PathValue("host"), "error", err)
		s.renderSettings(w, r, settingsData{Registries: registriesData{Alert: &fieldError{Text: err.Error()}}})
	default:
		s.serverError(w, "remove registry", err)
	}
}

// registriesSection fills in the rows of the section and the notice the
// request carries, keeping what the handler set.
func (s *server) registriesSection(r *http.Request, data registriesData) (registriesData, error) {
	regs, err := s.svc.Registries(r.Context())
	if err != nil {
		return data, err
	}
	now := s.now()
	data.Rows = make([]registryRow, 0, len(regs))
	for _, reg := range regs {
		row := registryRow{Host: reg.Host, Username: reg.Username, Edit: registryForm{Host: reg.Host, Username: reg.Username}}
		if reg.LastTest != nil {
			row.Check = &registryCheck{OK: reg.LastTest.OK, Message: reg.LastTest.Message, When: whenOf(now, &reg.LastTest.At, "")}
		}
		if reg.Host == data.EditHost {
			row.Edit, row.Open = data.Edit, true
		}
		data.Rows = append(data.Rows, row)
	}

	switch {
	case data.Saved:
		data.Notice = &registryNotice{Text: "Saved. The next deploy uses them; running apps pick them up the next time they start."}
	case data.Tested != "":
		for _, row := range data.Rows {
			switch {
			case row.Host != data.Tested || row.Check == nil:
			case row.Check.OK:
				data.Notice = &registryNotice{Text: row.Host + " accepted the credentials."}
			default:
				data.Notice = &registryNotice{Text: row.Host + " refused the credentials: " + strings.TrimSuffix(row.Check.Message, ".") + ".", Bad: true}
			}
		}
	}
	return data, nil
}
