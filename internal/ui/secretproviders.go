package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Settings › Secret providers (feature 037): the outside places an app's
// secrets may be read from, one connection row each (today only Bella Baxter).
// Every form is a plain post, so the page works without script. The API key and
// the private key are write-only, as the registries' password is: no struct
// here has a field for them, so no page can show them and a refused save never
// puts them back in a form.

// providerForm is what a provider form shows: the settings as saved or as
// typed, and the field a refusal names. Never the API key or the private key.
type providerForm struct {
	Name, URL, Project, Environment string
	Allowed                         string // "project, project/environment" as typed
	Editing                         bool   // the name is fixed, and the keys may stay empty
	Field                           string // the field at fault, after a refusal
	Err                             *fieldError
}

// providerFieldView is one input of a provider form.
type providerFieldView struct {
	ID, Name, Label, Kind, Value, Placeholder, Hint, Err string // Kind: text, password or textarea
	ReadOnly, Required                                   bool
}

// providerField builds an input of a provider form for the template. idPrefix
// keeps the ids of the add form and of each row's edit form apart; name is the
// input's name, which is also the field a refusal names. A key input is never
// given a value.
func providerField(f providerForm, idPrefix, name string) providerFieldView {
	v := providerFieldView{ID: idPrefix + "-" + name, Name: name, Kind: "text"}
	switch name {
	case "name":
		v.Label, v.Value, v.Placeholder, v.ReadOnly, v.Required = "Name", f.Name, "bella-prod", f.Editing, true
		v.Hint = "A lowercase name apps use to refer to it."
	case "url":
		v.Label, v.Value, v.Placeholder, v.Required = "Bella URL", f.URL, "https://api.bella-baxter.io", true
	case "project":
		v.Label, v.Value, v.Placeholder, v.Required = "Bella project", f.Project, "shop", true
		v.Hint = "The project's slug in Bella."
	case "environment":
		v.Label, v.Value, v.Placeholder, v.Required = "Bella environment", f.Environment, "production", true
		v.Hint = "The environment's slug. The API key must belong to it."
	case "apiKey":
		v.Label, v.Kind, v.Placeholder, v.Required = "API key", "password", "bax-…", !f.Editing
		if f.Editing {
			v.Hint = "Leave empty to keep the one stored."
		}
	case "privateKey":
		v.Label, v.Kind, v.Placeholder, v.Required = "Private key", "textarea", "-----BEGIN PRIVATE KEY-----", !f.Editing
		if f.Editing {
			v.Hint = "Leave empty to keep the one stored."
		} else {
			v.Hint = "The key Bella showed when it made the API key."
		}
	case "allowed":
		v.Label, v.Value, v.Placeholder = "Allowed in", f.Allowed, "shop, shop/production"
		v.Hint = "Comma separated: a project, or project/environment. Empty: every project."
	}
	if f.Field == name && f.Err != nil {
		v.Err = f.Err.Text
	}
	return v
}

// providerFormView is the inputs of a provider form, in order.
type providerFormView struct{ Fields []providerFieldView }

// providerForms is the template function that builds a form's inputs. The
// name comes first, and is read-only when editing.
func providerForms(f providerForm, idPrefix string) providerFormView {
	var v providerFormView
	for _, name := range []string{"name", "url", "project", "environment", "apiKey", "privateKey", "allowed"} {
		v.Fields = append(v.Fields, providerField(f, idPrefix, name))
	}
	return v
}

// providerRow is one provider, one connection row.
type providerRow struct {
	Name string
	Mark string // secretProvidersState's: failed, healthy or idle
	Kind string // "Bella Baxter · shop/production · api.bella-baxter.io"
	// Allowed is "All projects" or "Allowed in shop, shop/production".
	Allowed string
	UsedBy  string
	// State and Event are the row's two lines: the last test's outcome in
	// Caravana's words, and when it happened.
	State, Event string
	StateFailed  bool
	Detail       string // the failed test's reason, under the row
	Test         *when
	Edit         providerForm
	Open         bool // the edit form is shown open: it was refused, or asked for with ?edit=
}

// providersNotice is a sentence above the rows.
type providersNotice struct {
	Text string
	Bad  bool
}

// providersData is the whole section.
type providersData struct {
	Rows   []providerRow
	Notice *providersNotice
	Alert  *fieldError // a refusal that names no field: Nomad being down, a provider in use
	Add    providerForm
	// EditName says which row's edit form is open; Edit is what it was refused
	// with (its Name is empty when it wasn't refused).
	EditName string
	Edit     providerForm
	Saved    bool
	Tested   string // the provider whose test result to announce
	Removed  bool
}

// showSecretProviders answers GET /settings/secret-providers. The notice comes
// from the redirect after a save, a test or a removal; ?edit=<name> opens that
// provider's edit form.
func (s *server) showSecretProviders(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	s.renderSettings(w, r, entrySecretProviders, settingsData{Providers: providersData{
		Saved: q.Get("providers") == "saved", Removed: q.Get("providers") == "removed", Tested: q.Get("tested"), EditName: q.Get("edit"),
	}})
}

// saveSecretProvider answers POST /settings/secret-providers, for the add form
// and for a row's edit form (which also posts edit=1). An empty API key or
// private key keeps the one stored. A refusal draws the page again with what
// was typed, except both keys.
func (s *server) saveSecretProvider(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	edit := r.PostFormValue("edit") == "1"
	form := providerForm{
		Name: strings.TrimSpace(r.PostFormValue("name")), URL: strings.TrimSpace(r.PostFormValue("url")),
		Project: strings.TrimSpace(r.PostFormValue("project")), Environment: strings.TrimSpace(r.PostFormValue("environment")),
		Allowed: strings.TrimSpace(r.PostFormValue("allowed")), Editing: edit,
	}
	in := service.SecretProviderInput{
		Name: form.Name, Kind: store.SecretProviderBella, URL: form.URL, Project: form.Project, Environment: form.Environment,
		Allowed: splitPlaces(form.Allowed),
	}
	// An empty key field keeps the stored key (editing) or is refused as missing
	// (adding): the service says which, so an empty form value is not sent.
	if v := r.PostFormValue("apiKey"); v != "" {
		in.APIKey = &v
	}
	if v := r.PostFormValue("privateKey"); v != "" {
		v = strings.ReplaceAll(v, "\r\n", "\n") // a textarea sends CRLF
		in.PrivateKey = &v
	}

	_, _, err := s.svc.SaveSecretProvider(r.Context(), in)
	if err == nil {
		http.Redirect(w, r, "/settings/secret-providers?providers=saved", http.StatusSeeOther)
		return
	}

	data := providersData{Add: form}
	if edit {
		data = providersData{EditName: form.Name, Edit: form}
	}
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr):
		form.Field, form.Err = formFieldOf(fieldErr.Field), &fieldError{Text: providerFieldMessage(fieldErr)}
		if edit {
			data.Edit = form
		} else {
			data.Add = form
		}
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't save the secret provider's credentials", "provider", form.Name, "error", err)
		data.Alert = &fieldError{Text: "Nomad couldn't keep the credentials, so nothing was saved: " + err.Error()}
	default:
		s.serverError(w, r, "save secret provider", err)
		return
	}
	status := http.StatusUnprocessableEntity
	if data.Alert != nil {
		status = http.StatusBadGateway
	}
	s.renderSettings(w, r, entrySecretProviders, settingsData{Providers: data, Status: status})
}

// splitPlaces reads the "Allowed in" text: comma separated places, blanks
// dropped. The service checks each one.
func splitPlaces(text string) []string {
	var out []string
	for _, p := range strings.Split(text, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// formFieldOf is the input a service field error is about: the API names the
// Bella settings bella.url, bella.project and bella.environment, the form's
// inputs are url, project and environment; allowed[i] is the one text box.
func formFieldOf(field string) string {
	switch {
	case strings.HasPrefix(field, "bella."):
		return strings.TrimPrefix(field, "bella.")
	case strings.HasPrefix(field, "allowed"):
		return "allowed"
	case field == "kind":
		return "name"
	}
	return field
}

// providerFieldMessage is a field error as a sentence for the input it names.
// The service's messages about the setting itself ("must be…", "is required…")
// follow its name; the others stand alone.
func providerFieldMessage(fe *app.FieldError) string {
	label := map[string]string{
		"name": "The name", "bella.url": "The Bella URL", "bella.project": "The Bella project",
		"bella.environment": "The Bella environment", "apiKey": "The API key", "privateKey": "The private key",
	}[fe.Field]
	if label != "" && (strings.HasPrefix(fe.Message, "must ") || strings.HasPrefix(fe.Message, "is ")) {
		return label + " " + fe.Message
	}
	return fe.Message
}

// testSecretProvider answers POST /settings/secret-providers/test, with the
// provider's name in the form's "name" field. The outcome is stored, and the
// page it goes back to announces it.
func (s *server) testSecretProvider(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name := r.PostFormValue("name")
	_, err := s.svc.TestSecretProvider(r.Context(), name)
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/secret-providers?tested="+url.QueryEscape(name), http.StatusSeeOther)
	case errors.Is(err, store.ErrNotFound):
		http.Redirect(w, r, "/settings/secret-providers", http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't read the secret provider's credentials", "provider", name, "error", err)
		s.renderSettings(w, r, entrySecretProviders, settingsData{Providers: providersData{Alert: &fieldError{Text: err.Error()}}, Status: http.StatusBadGateway})
	default:
		s.serverError(w, r, "test secret provider", err)
	}
}

// removeSecretProvider answers POST /settings/secret-providers/remove, with the
// name in the form's "name" field. The page asks first (a <details>); this is
// the post it sends. A provider some app still references is refused with the
// apps named. Apps already running keep running.
func (s *server) removeSecretProvider(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name := r.PostFormValue("name")
	err := s.svc.DeleteSecretProvider(r.Context(), name)
	var inUse *service.ProviderInUseError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/secret-providers?providers=removed", http.StatusSeeOther)
	case errors.Is(err, store.ErrNotFound):
		http.Redirect(w, r, "/settings/secret-providers", http.StatusSeeOther)
	case errors.As(err, &inUse):
		s.renderSettings(w, r, entrySecretProviders, settingsData{Providers: providersData{Alert: &fieldError{Text: inUse.Error()}}, Status: http.StatusConflict})
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't remove the secret provider's credentials", "provider", name, "error", err)
		s.renderSettings(w, r, entrySecretProviders, settingsData{Providers: providersData{Alert: &fieldError{Text: err.Error()}}, Status: http.StatusBadGateway})
	default:
		s.serverError(w, r, "remove secret provider", err)
	}
}

// providerKind is the line under a provider's name.
func providerKind(p store.SecretProvider) string {
	host := p.URL
	if u, err := url.Parse(p.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	return "Bella Baxter · " + p.Project + "/" + p.Environment + " · " + host
}

// providersSection fills in the rows of the page and the notice the request
// carries, keeping what the handler set. The rows' marks are
// secretProvidersState's, the same function the nav counts with; refused
// providers come first, then the others by name.
func providersSection(now time.Time, facts settingsFacts, st entryState, data providersData) providersData {
	data.Rows = make([]providerRow, 0, len(facts.SecretProviders))
	for i, p := range facts.SecretProviders {
		row := providerRow{Name: p.Name, Mark: st.Rows[i], Kind: providerKind(p.SecretProvider), UsedBy: usedBy(len(p.UsedBy), "app"),
			Allowed: "All projects", Edit: providerForm{Name: p.Name, URL: p.URL, Project: p.Project, Environment: p.Environment,
				Allowed: strings.Join(p.Allowed, ", "), Editing: true}}
		if len(p.Allowed) > 0 {
			row.Allowed = "Allowed in " + strings.Join(p.Allowed, ", ")
		}
		switch t := p.LastTest; {
		case t != nil && t.OK:
			w := whenOf(now, &t.At, "")
			row.State, row.Event, row.Test = t.Message, "tested "+w.Text, &w
		case t != nil:
			w := whenOf(now, &t.At, "")
			row.State, row.StateFailed, row.Event, row.Test, row.Detail = "can't be read", true, "tested "+w.Text, &w, t.Message
		case p.UpdatedAt.After(p.CreatedAt):
			row.State, row.Event = "changed · not tested since", "saved "+whenOf(now, &p.UpdatedAt, "").Text
		default:
			row.State, row.Event = "not tested yet", "added "+whenOf(now, &p.CreatedAt, "").Text
		}
		if p.Name == data.EditName {
			row.Open = true
			if data.Edit.Name != "" {
				row.Edit = data.Edit
			}
		}
		data.Rows = append(data.Rows, row)
	}
	sortStable(data.Rows, func(r providerRow) int { return markRank(r.Mark) })

	switch {
	case data.Saved:
		data.Notice = &providersNotice{Text: "Saved. Test it to see that Caravana can read it."}
	case data.Removed:
		data.Notice = &providersNotice{Text: "Removed. Apps already running keep their secrets."}
	case data.Tested != "":
		for _, row := range data.Rows {
			switch {
			case row.Name != data.Tested || row.Test == nil:
			case !row.StateFailed:
				data.Notice = &providersNotice{Text: row.Name + ": " + row.State + "."}
			default:
				data.Notice = &providersNotice{Text: row.Name + " can't be read: " + strings.TrimSuffix(row.Detail, ".") + ".", Bad: true}
			}
		}
	}
	return data
}
