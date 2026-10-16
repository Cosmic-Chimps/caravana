package ui

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// maxNewAppName is the longest name the New app form accepts. The form makes
// one service named like the app, and Nomad's service name <app>-<service>
// may have 63 characters at most, so the name can have 31.
const maxNewAppName = 31

// formField is one input of a form: what it holds, and what is wrong with it.
type formField struct {
	ID    string // the input's element ID, which the error's ID derives from
	Value string
	Error sentence // empty when nothing is wrong
	Alert bool     // the first error of the form is announced; the others only show
}

// newAppForm is the New app form with what was typed. Notice is an error that
// isn't about one field (Nomad is down, say). SavedID is set when the app was
// saved but its first deploy didn't start: the name is taken now, so the form
// points at the app's own Deployments tab, which can try again. Focus names
// the input that gets the cursor: the first one with an error.
type newAppForm struct {
	Name, Image, Port, Domain, Health formField
	Notice                            sentence
	SavedID                           string
	Focus                             string
}

// newForm is an empty form: every input has its ID, and the health check
// path starts at "/".
func newForm() newAppForm {
	return newAppForm{Name: formField{ID: "name"}, Image: formField{ID: "image"}, Port: formField{ID: "port"},
		Domain: formField{ID: "domain"}, Health: formField{ID: "healthPath", Value: "/"}}
}

// fields lists the inputs in the order they show, with the names of their form values.
func (f *newAppForm) fields() []struct {
	Name  string
	Field *formField
} {
	return []struct {
		Name  string
		Field *formField
	}{{"name", &f.Name}, {"image", &f.Image}, {"port", &f.Port}, {"domain", &f.Domain}, {"healthPath", &f.Health}}
}

// markFirstError chooses which error is announced and which field is focused.
func (f *newAppForm) markFirstError() {
	for _, in := range f.fields() {
		if len(in.Field.Error) > 0 {
			in.Field.Alert, f.Focus = true, in.Name
			return
		}
	}
}

// fieldFor is the input an error about the app's field path belongs to, or
// nil for a path the form has no input for (data-model, "Validation rules
// added"). The form makes one service, services[0].
func (f *newAppForm) fieldFor(path string) *formField {
	switch {
	case path == "id", path == "services[0].name":
		return &f.Name
	case path == "services[0].image":
		return &f.Image
	case strings.HasPrefix(path, "services[0].ports["):
		return &f.Port
	case strings.HasPrefix(path, "services[0].domains["):
		return &f.Domain
	case strings.HasPrefix(path, "services[0].health"):
		return &f.Health
	}
	return nil
}

// newAppPage is the whole New app page.
type newAppPage struct {
	pageData
	Form    newAppForm
	Compose *composeView // set on the Compose file tab
}

// showNewApp answers GET /apps/new.
func (s *server) showNewApp(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.Query().Get("from") == "compose" {
		s.drawCompose(w, r, http.StatusOK, newComposeView(""))
		return
	}
	s.renderNewApp(w, r, http.StatusOK, newForm())
}

// renderNewApp draws the page, or, for an htmx request, just the form: htmx
// swaps it in place of the old one, so errors appear where the form was.
func (s *server) renderNewApp(w http.ResponseWriter, r *http.Request, status int, form newAppForm) {
	if r.Header.Get("HX-Request") == "true" {
		var buf bytes.Buffer
		if err := s.pages["newapp"].ExecuteTemplate(&buf, "newapp-form", form); err != nil {
			s.serverError(w, "render new app form", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write(buf.Bytes())
		return
	}
	head, err := s.shellPage(r, "New app", "apps", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: "New app"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, status, "newapp", newAppPage{pageData: head, Form: form})
}

// createApp answers POST /apps: it builds the app of data-model.md's
// "Validation rules added" from the form and creates it, with trigger
// console. Success goes to the new app's Deployments tab; any problem draws
// the form again with what was typed and the errors under their fields.
func (s *server) createApp(w http.ResponseWriter, r *http.Request, session string) {
	if err := r.ParseForm(); err != nil {
		s.message(w, http.StatusBadRequest, "Bad request", "That form couldn't be read.", "Go back and try again.")
		return
	}
	val := func(name string) string { return strings.TrimSpace(r.PostForm.Get(name)) }
	form := newForm()
	form.Name.Value, form.Image.Value = val("name"), val("image")
	form.Port.Value, form.Domain.Value, form.Health.Value = val("port"), val("domain"), val("healthPath")

	a, ok := buildNewApp(&form)
	if !ok {
		form.markFirstError()
		s.renderNewApp(w, r, http.StatusUnprocessableEntity, form)
		return
	}

	_, err := s.svc.CreateApp(r.Context(), a, app.TriggerConsole)
	if err == nil {
		to := "/apps/" + a.ID + "/deployments"
		if r.Header.Get("HX-Request") == "true" {
			// htmx would follow a redirect inside the form's place; this makes the browser go there.
			w.Header().Set("HX-Redirect", to)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}

	status, ok := refuseNewApp(&form, a.ID, err)
	if !ok {
		s.serverError(w, "create app from the console", err)
		return
	}
	form.markFirstError()
	s.renderNewApp(w, r, status, form)
}

// buildNewApp turns what was typed into the app: one service named like the
// app. Without a port it is a worker, and the domain and health path are not
// part of it. It reports the problems only the form can see (empty or
// misspelled values, a domain with no port) on their fields, and ok is false
// then; the app's own rules are checked by the service.
func buildNewApp(f *newAppForm) (a app.App, ok bool) {
	ok = true
	refuse := func(field *formField, text string) {
		field.Error, ok = sentence{plain(text)}, false
	}

	switch name := f.Name.Value; {
	case name == "":
		refuse(&f.Name, "Enter a name for the app.")
	case len(name) > maxNewAppName:
		refuse(&f.Name, "Use at most "+strconv.Itoa(maxNewAppName)+" characters: the service takes the same name, and Nomad allows 63 for the two together.")
	}
	if f.Image.Value == "" {
		refuse(&f.Image, "Enter an image.")
	}

	port := 0
	if f.Port.Value != "" {
		n, err := strconv.Atoi(f.Port.Value)
		if err != nil || n < 1 || n > 65535 {
			refuse(&f.Port, "Enter a number from 1 to 65535.")
		} else {
			port = n
		}
	}
	if f.Domain.Value != "" && f.Port.Value == "" {
		refuse(&f.Domain, "A domain needs a port to send traffic to.")
	}
	if !ok {
		return app.App{}, false
	}

	svc := app.Service{Name: f.Name.Value, Image: f.Image.Value}
	if port != 0 {
		svc.Ports = []app.Port{{Name: "http", ContainerPort: port}}
		if f.Domain.Value != "" {
			svc.Domains = []app.Domain{{Host: f.Domain.Value, Port: "http"}}
		}
		// A service with a port needs a health check; an empty path is a TCP check.
		svc.Health = &app.HealthCheck{Path: f.Health.Value}
	}
	return app.App{ID: f.Name.Value, Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{svc}}, true
}

// refuseNewApp says what a failed create looks like on the form: errors on
// the fields they are about, or a sentence for the form, and the HTTP status.
// ok is false for errors the form has no words for.
func refuseNewApp(f *newAppForm, id string, err error) (status int, ok bool) {
	var fieldErr *app.FieldError
	var imageErr *deploy.ImageError
	var deployErr *service.DeployError

	// Once the app is saved, the name is taken: the form points at the
	// Deployments tab, where the failed attempt is recorded and a new try is a Deploy.
	if errors.As(err, &deployErr) {
		f.SavedID = id
	}
	onField := func(path, message string) bool {
		field := f.fieldFor(path)
		if field == nil {
			return false
		}
		field.Error = sentence{plain(message)}
		return true
	}

	switch {
	case errors.Is(err, service.ErrAppExists):
		f.Name.Error = sentence{plain("An app called "), mono(id), plain(" already exists.")}
		return http.StatusUnprocessableEntity, true
	case errors.As(err, &fieldErr):
		if !onField(fieldErr.Field, fieldMessage(fieldErr.Field, fieldErr.Message)) {
			f.Notice = sentence{plain("caravana can't deploy this app: "), mono(fieldErr.Field), plain(" " + fieldErr.Message)}
		}
		return http.StatusUnprocessableEntity, true
	case errors.As(err, &imageErr):
		status = http.StatusUnprocessableEntity
		if !errors.Is(err, deploy.ErrImageNotFound) && !errors.Is(err, deploy.ErrRegistryAuth) {
			status = http.StatusBadGateway // the registry couldn't answer: not the typed value's fault
		}
		if !onField(imageErr.Field, imageErr.Error()) {
			f.Notice = sentence{plain(imageErr.Error())}
		}
		return status, true
	case errors.Is(err, deploy.ErrJobConflict):
		if foreignJob(err.Error()) {
			f.Notice = sentence{plain("A job with this name already exists in Nomad and caravana didn't create it, so nothing was deployed. Stop that job in Nomad, or use another name.")}
		} else {
			f.Notice = sentence{plain("The Nomad job changed while deploying. Nothing was lost; try again.")}
		}
		return http.StatusConflict, true
	case errors.Is(err, deploy.ErrNomadRejected):
		f.Notice = sentence{plain("Nomad rejected the job: " + strings.TrimPrefix(err.Error(), "nomad rejected the job: "))}
		return http.StatusBadGateway, true
	case errors.Is(err, deploy.ErrNomadUnavailable):
		f.Notice = sentence{plain("Nomad couldn't be reached right now, so nothing was deployed.")}
		return http.StatusBadGateway, true
	}
	return 0, false
}

// fieldMessage is a validation message as a sentence about its field. Most of
// the app's messages are a predicate ("is required", "must not contain
// spaces") that needs its subject; the ones that start with the quoted value
// already have one.
func fieldMessage(path, message string) string {
	if strings.HasPrefix(message, `"`) {
		return message
	}
	subject := "This value"
	switch {
	case strings.HasSuffix(path, "image"):
		subject = "The image"
	case path == "id" || strings.HasSuffix(path, ".name"):
		subject = "The name"
	case strings.Contains(path, ".ports["):
		subject = "The port"
	case strings.Contains(path, ".domains["):
		subject = "The domain"
	case strings.HasSuffix(path, ".health.interval"):
		subject = "The check interval"
	case strings.HasSuffix(path, ".health.timeout"):
		subject = "The check timeout"
	case strings.Contains(path, ".health"):
		subject = "The health check path"
	case strings.Contains(path, ".env["):
		subject = "The variable name"
	case strings.HasSuffix(path, ".replicas"):
		subject = "The number of copies"
	case strings.HasSuffix(path, ".resources.cpuMHz"):
		subject = "The CPU"
	case strings.HasSuffix(path, ".resources.memoryMB"):
		subject = "The memory reservation"
	case strings.HasSuffix(path, ".resources.memoryMaxMB"):
		subject = "The memory ceiling"
	}
	return subject + " " + message
}
