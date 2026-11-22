package ui

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Previews in the console (feature 049, plan D11): the Previews section of a
// project's settings page, the "preview · expires" line and Close preview on a
// preview environment's row, and the page that asks before a preview is closed.
// The service decides everything; these pages say it in words. The preview
// password is typed into a password input, handed to the service and dropped:
// no page, redirect or log line carries it back.

// previewsForm is the Previews section: the settings as they are (or as they
// were typed in a refused post) and what is wrong with each. The password field
// never has a value.
type previewsForm struct {
	Action        string
	Limit, Expire formField
	Password      formField
	// Remove is the "Remove the password" checkbox: Checked is what was posted,
	// Error is the refusal (previews are open).
	Remove      formField
	RemoveOn    bool
	PasswordSet bool
	Open        int
}

// previewRow is what a preview environment's row adds: when it expires and where
// Close preview leads.
type previewRow struct {
	Expires   clockTime
	CloseHref string
}

// previewClose is the page that asks before a preview is closed.
type previewClose struct {
	Project, Environment string
	Apps                 []projectApp
	Action, Back         string
	// Refusal replaces the button, in words.
	Refusal sentence
}

// previewsExtra is what a response adds to the Previews section: what was typed
// and the reasons.
type previewsExtra struct {
	Limit, Expire, Password *formField
	Remove                  *formField
	RemoveOn                bool
}

// previewsFormOf builds the section from the service's settings and the extras.
func previewsFormOf(project string, st service.PreviewSettings, extra *previewsExtra) *previewsForm {
	f := &previewsForm{
		Action:      "/projects/" + url.PathEscape(project) + "/previews/settings",
		Limit:       formField{ID: "preview-limit", Value: strconv.Itoa(st.Limit)},
		Expire:      formField{ID: "preview-expire", Value: strconv.Itoa(st.ExpireAfterDays)},
		Password:    formField{ID: "preview-password"},
		Remove:      formField{ID: "preview-remove"},
		PasswordSet: st.PasswordSet,
		Open:        st.Open,
	}
	if extra == nil {
		return f
	}
	if extra.Limit != nil {
		f.Limit = *extra.Limit
	}
	if extra.Expire != nil {
		f.Expire = *extra.Expire
	}
	if extra.Password != nil {
		f.Password = *extra.Password
		f.Password.Value = "" // never drawn back
	}
	if extra.Remove != nil {
		f.Remove = *extra.Remove
	}
	f.RemoveOn = extra.RemoveOn
	return f
}

// savePreviewSettings answers POST /projects/{project}/previews/settings: the
// limit, the days, a new password and removing it. An empty number or password
// keeps what is there. Success redirects to the section (303) with the calm
// sentence; a success with something to say (open previews that get the password
// at their next deploy) is the page itself, 200. A refusal draws the section
// again with the reason at its field, never a redirect.
func (s *server) savePreviewSettings(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("project")
	if !s.readForm(w, r) {
		return
	}
	limitText := strings.TrimSpace(r.PostForm.Get("limit"))
	expireText := strings.TrimSpace(r.PostForm.Get("expireAfterDays"))
	password := r.PostForm.Get("password")
	remove := r.PostForm.Get("removePassword") != ""

	// A number that wasn't posted keeps the current value on the page (nil); what was typed stays as typed.
	typed := &previewsExtra{
		Password: &formField{ID: "preview-password"},
		Remove:   &formField{ID: "preview-remove"},
		RemoveOn: remove,
	}
	if r.PostForm.Has("limit") {
		typed.Limit = &formField{ID: "preview-limit", Value: limitText}
	}
	if r.PostForm.Has("expireAfterDays") {
		typed.Expire = &formField{ID: "preview-expire", Value: expireText}
	}
	refuse := func(status int, field *formField, text sentence) {
		field.Error, field.Alert = text, true
		s.settingsPage(w, r, status, id, projectExtras{Previews: typed})
	}

	in := service.PreviewSettingsInput{RemovePassword: remove}
	if limitText != "" {
		n, err := strconv.Atoi(limitText)
		if err != nil {
			refuse(http.StatusUnprocessableEntity, typed.Limit, sentence{plain("Enter a whole number from 0 to 20.")})
			return
		}
		in.Limit = &n
	}
	if expireText != "" {
		n, err := strconv.Atoi(expireText)
		if err != nil {
			refuse(http.StatusUnprocessableEntity, typed.Expire, sentence{plain("Enter a whole number of days from 1 to 30.")})
			return
		}
		in.ExpireAfterDays = &n
	}
	if password != "" {
		in.Password = &password
	}

	res, err := s.svc.SetPreviewSettings(r.Context(), id, in)
	var fieldErr *app.FieldError
	var openErr *service.PreviewsOpenError
	switch {
	case err == nil:
		notice := sentence{plain("Preview settings saved.")}
		if len(res.Warnings) > 0 {
			// The warnings name environments, never the password.
			for _, w := range res.Warnings {
				notice = append(notice, plain(" "+upperFirst(w.Message)+"."))
			}
			s.settingsPage(w, r, http.StatusOK, id, projectExtras{Notice: notice})
			return
		}
		http.Redirect(w, r, "/projects/"+url.PathEscape(id)+"/settings?saved=previews#previews", http.StatusSeeOther)
	case errors.Is(err, service.ErrProjectNotFound):
		s.projectGone(w, r, id)
	case errors.As(err, &openErr):
		refuse(http.StatusConflict, typed.Remove, sentence{plain("Can't remove the password while previews are open ("),
			mono(strings.Join(openErr.Open, ", ")), plain("): their domains use it. Close them first.")})
	case errors.As(err, &fieldErr):
		field := map[string]*formField{"limit": typed.Limit, "expireAfterDays": typed.Expire,
			"password": typed.Password, "removePassword": typed.Remove}[fieldErr.Field]
		if field == nil {
			s.serverError(w, r, "save the preview settings", err)
			return
		}
		refuse(http.StatusUnprocessableEntity, field, sentence{plain(upperFirst(fieldAbout(fieldErr)) + ".")})
	default:
		s.serverError(w, r, "save the preview settings", err)
	}
}

// fieldAbout is a field error as one sentence about the field ("The limit is 30;
// it must be 0 to 20"), for the field's own message.
func fieldAbout(e *app.FieldError) string {
	switch e.Field {
	case "limit":
		return "the limit " + e.Message
	case "expireAfterDays":
		return "the number of days " + e.Message
	case "password":
		return "the password " + e.Message
	}
	return e.Message
}

// previewCloseOf reads a preview for its confirmation page. ok is false after
// the answer was written (an unknown project, 404).
func (s *server) previewCloseOf(w http.ResponseWriter, r *http.Request) (previewClose, bool) {
	id, env := r.PathValue("project"), r.PathValue("environment")
	base := "/projects/" + url.PathEscape(id)
	v := previewClose{Project: id, Environment: env, Back: base + "/settings",
		Action: base + "/previews/" + url.PathEscape(env) + "/close"}
	p, err := s.svc.Project(r.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrProjectNotFound) {
			s.projectGone(w, r, id)
		} else {
			s.serverError(w, r, "read project", err)
		}
		return v, false
	}
	if !slices.ContainsFunc(p.Environments, func(e service.Environment) bool { return e.Name == env }) {
		v.Refusal = sentence{plain("There's no environment called "), mono(env), plain(" in this project.")}
		s.renderPreviewClose(w, r, http.StatusNotFound, v)
		return v, false
	}
	sums, err := s.svc.ListAppsIn(r.Context(), id, env)
	if err != nil {
		s.serverError(w, r, "list apps of a preview", err)
		return v, false
	}
	for _, sum := range sums {
		v.Apps = append(v.Apps, projectApp{Name: cmpName(sum), Href: "/apps/" + url.PathEscape(sum.App.ID)})
	}
	return v, true
}

// renderPreviewClose draws the confirmation page.
func (s *server) renderPreviewClose(w http.ResponseWriter, r *http.Request, status int, v previewClose) {
	head, err := s.shellPage(r, "Close "+v.Environment, navProjects,
		crumb{Label: v.Project, Href: "/projects/" + url.PathEscape(v.Project)},
		crumb{Label: "Settings", Href: v.Back}, crumb{Label: "Close " + v.Environment})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	s.render(w, status, "projects", projectsPage{pageData: head, Close: &v})
}

// showPreviewClose answers GET /projects/{project}/previews/{environment}/close:
// what closing deletes, and the button. An environment that isn't a preview is
// 409 with the reason in place of the button.
func (s *server) showPreviewClose(w http.ResponseWriter, r *http.Request, session string) {
	v, ok := s.previewCloseOf(w, r)
	if !ok {
		return
	}
	status := http.StatusOK
	if !app.IsPreviewEnvironment(v.Environment) {
		status = http.StatusConflict
		v.Refusal = sentence{mono(v.Environment), plain(" isn't a preview: only environments named pr-<pull request number> are.")}
	}
	s.renderPreviewClose(w, r, status, v)
}

// closePreview answers POST /projects/{project}/previews/{environment}/close:
// every app of the preview is deleted, then the environment, and the browser
// goes back to the settings. A refusal draws the confirmation again, with the
// reason in place of the button.
func (s *server) closePreview(w http.ResponseWriter, r *http.Request, session string) {
	v, ok := s.previewCloseOf(w, r)
	if !ok {
		return
	}
	err := s.svc.ClosePreview(r.Context(), v.Project, v.Environment, service.CloseByOwner)
	switch {
	case err == nil, errors.Is(err, service.ErrEnvironmentNotFound):
		http.Redirect(w, r, v.Back+"?closed="+url.QueryEscape(v.Environment), http.StatusSeeOther)
	case errors.Is(err, service.ErrNotAPreview):
		v.Refusal = sentence{mono(v.Environment), plain(" isn't a preview: only environments named pr-<pull request number> are.")}
		s.renderPreviewClose(w, r, http.StatusConflict, v)
	case errors.Is(err, service.ErrNotEmpty):
		v.Refusal = sentence{plain("An app was added to " + v.Environment + " while it was closing, so the environment stays. Close it again.")}
		s.renderPreviewClose(w, r, http.StatusConflict, v)
	default:
		s.log.Warn("close a preview", "project", v.Project, "environment", v.Environment, "error", err)
		v.Refusal = sentence{plain("Couldn't close " + v.Environment + ": an app couldn't be deleted. What is left stays; closing it again finishes the job.")}
		s.renderPreviewClose(w, r, http.StatusBadGateway, v)
	}
}
