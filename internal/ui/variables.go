package ui

import (
	"cmp"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Variables & secrets page (feature 021, /apps/{id}/variables): one table of
// the app's environment variables and secret names, the form that sets one, and
// the .env editor. Every change is a deploy: a variable is a definition edit
// (DeployIf with the page's revision) and a secret goes through the secret
// store like it always did. A secret's value is never in a page, not even the
// length of it.

const (
	// changedSincePage is the refusal of a post made from a page the app has changed since.
	changedSincePage = "This app changed since you opened this page. What is shown now is current: make the change again."
	// fileEditedSentence is said where a page can't change what a kept Compose file makes.
	fileEditedSentence = "This app is edited through its Compose file."
)

// variablesTab is the body of the Variables & secrets page.
type variablesTab struct {
	ID       string
	Revision string
	Defined  bool
	// Kept says the app keeps its Compose file: the variables are read-only
	// (the file makes them), and only secrets can be set or removed.
	Kept bool
	// Stopped says the owner stopped the app: the forms have no submit.
	Stopped bool
	Many    bool // several services: a Used by column, a Service select
	Rows    []variableRow
	Add     addVariableForm
	Editors []envEditor
	Notice  sentence
}

// variableRow is one variable or secret of one service. A secret's value is not
// here, so no template can draw it.
type variableRow struct {
	Service, Name, Value string
	Secret               bool
}

// addVariableForm is the "Add or change" form with what was typed, never a secret's value.
type addVariableForm struct {
	Service     string
	Services    []string
	Name, Value formField
	Secret      bool
	Notice      sentence
}

// envEditor is one service's ".env" textarea. Field is what was typed, or what
// is stored.
type envEditor struct {
	Service string
	Field   formField
	// Refused says the edit was refused: the text is kept in the box, and the table above
	// shows what the app has now.
	Refused bool
}

// CanEdit says the page offers the forms that change variables.
func (v variablesTab) CanEdit() bool { return v.Defined && !v.Kept && !v.Stopped }

// CanSet says the Add form has a submit: a kept-file app only sets secrets, but
// it can.
func (v variablesTab) CanSet() bool { return v.Defined && !v.Stopped }

// CanRemove says a secret's row has Remove.
func (v variablesTab) CanRemove() bool { return v.Defined && !v.Stopped }

// variableRowsOf lists the app's secrets first and its variables after, each by
// name, then service.
func variableRowsOf(a app.App) []variableRow {
	var secretRows, varRows []variableRow
	for _, svc := range a.Services {
		for _, sec := range svc.Secrets {
			secretRows = append(secretRows, variableRow{Service: svc.Name, Name: sec.Name, Secret: true})
		}
		for _, v := range svc.Env {
			varRows = append(varRows, variableRow{Service: svc.Name, Name: v.Name, Value: v.Value})
		}
	}
	byName := func(a, b variableRow) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Service, b.Service))
	}
	slices.SortFunc(secretRows, byName)
	slices.SortFunc(varRows, byName)
	return append(secretRows, varRows...)
}

// variablesExtras is what an answer adds to the page: the Add form as typed with
// its errors, the .env text as typed with its error, or a sentence for the page.
type variablesExtras struct {
	Add    *addVariableForm
	Env    map[string]formField // by service
	Notice sentence
}

// variablesTabOf builds the page's body from the app's summary. It is pure.
func variablesTabOf(sum service.AppSummary, extra variablesExtras) *variablesTab {
	a := sum.App
	tab := &variablesTab{ID: a.ID, Revision: sum.Revision, Defined: a.Defined(), Kept: sum.Compose != nil,
		Stopped: sum.StoppedAt != nil, Many: len(a.Services) > 1, Rows: variableRowsOf(a), Notice: extra.Notice}
	tab.Add = addVariableForm{Name: formField{ID: "var-name"}, Value: formField{ID: "var-value"}, Secret: true}
	if extra.Add != nil {
		tab.Add = *extra.Add
		tab.Add.Name.ID, tab.Add.Value.ID = "var-name", "var-value"
	}
	for _, svc := range a.Services {
		tab.Add.Services = append(tab.Add.Services, svc.Name)
		ed := envEditor{Service: svc.Name, Field: formField{ID: "env-" + svc.Name, Value: envText(svc.Env)}}
		if typed, ok := extra.Env[svc.Name]; ok {
			ed.Field.Value, ed.Field.Error, ed.Field.Alert = typed.Value, typed.Error, typed.Alert
			ed.Refused = true
		}
		tab.Editors = append(tab.Editors, ed)
	}
	if tab.Add.Service == "" && len(tab.Add.Services) > 0 {
		tab.Add.Service = tab.Add.Services[0]
	}
	return tab
}

// variablesPage draws the Variables & secrets page with the given status. An
// unknown app is the not-found page.
func (s *server) variablesPage(w http.ResponseWriter, r *http.Request, status int, id string, extra variablesExtras) {
	ctx := r.Context()
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	head, err := s.shellPage(r, id+" variables", navProjects, s.appCrumbs(ctx, id)...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id
	// The Configure tab stays current: this is one of its pages.
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(ctx, sum, "configure"), VariablesTab: variablesTabOf(sum, extra)})
}

// showVariables answers GET /apps/{id}/variables.
func (s *server) showVariables(w http.ResponseWriter, r *http.Request, session string) {
	s.variablesPage(w, r, http.StatusOK, r.PathValue("id"), variablesExtras{})
}

// serviceNamed finds a service of the app by name.
func serviceNamed(a app.App, name string) (int, bool) {
	i := slices.IndexFunc(a.Services, func(svc app.Service) bool { return svc.Name == name })
	return i, i >= 0
}

// formRevision reads the revision the page was drawn from. A form without one
// would save over whatever is there, so it is refused.
func (s *server) formRevision(w http.ResponseWriter, r *http.Request, what string) (string, bool) {
	revision := strings.TrimSpace(r.PostForm.Get("revision"))
	if revision == "" {
		s.message(w, http.StatusBadRequest, "Bad request", "That form has no revision.", "Open the page again and "+what+" from there.")
		return "", false
	}
	return revision, true
}

// setVariable answers POST /apps/{id}/variables: the "Add or change" form. A
// variable is set or replaced in the service's environment; with "secret" it is
// the secret's value that is set. Either one is a deploy on the condition that
// the app still has the revision the page was drawn from.
func (s *server) setVariable(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "set it")
	if !ok {
		return
	}
	rec, err := s.svc.GetApp(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}

	// "Keep as a secret" is ticked when the form is drawn, and an unticked box sends nothing, so the
	// form also sends secretChoice: with it, the box says what is meant; without it (a form that
	// isn't this one) the value is kept as a secret, the side that shows nothing.
	form := addVariableForm{Service: strings.TrimSpace(r.PostForm.Get("service")),
		Secret: !r.PostForm.Has("secretChoice") || r.PostForm.Get("secret") != ""}
	form.Name.Value = strings.TrimSpace(r.PostForm.Get("name"))
	// A textarea sends its line breaks as CRLF; values (keys, certificates) expect LF.
	value := strings.ReplaceAll(r.PostForm.Get("value"), "\r\n", "\n")
	if !form.Secret {
		form.Value.Value = value // a variable's value is shown in the table anyway
	}
	if form.Service == "" && len(rec.App.Services) == 1 {
		form.Service = rec.App.Services[0].Name
	}
	refuse := func(status int, f *addVariableForm) {
		s.variablesPage(w, r, status, id, variablesExtras{Add: f})
	}

	i, found := serviceNamed(rec.App, form.Service)
	if !found {
		form.Notice = sentence{plain("This app has no service "), mono(form.Service), plain(".")}
		refuse(http.StatusUnprocessableEntity, &form)
		return
	}
	svc := rec.App.Services[i]
	// An app that keeps its Compose file has its variables from the file; its secrets are
	// still the console's to set (editSecrets rewrites the overlay).
	if rec.Compose != nil && !form.Secret {
		form.Notice = sentence{plain(fileEditedSentence + " Change the file in Configure and deploy it.")}
		refuse(http.StatusConflict, &form)
		return
	}
	if form.Name.Value == "" {
		form.Name.Error = sentence{plain("Enter a name.")}
		refuse(http.StatusUnprocessableEntity, &form)
		return
	}
	isSecret := slices.ContainsFunc(svc.Secrets, func(sec app.Secret) bool { return sec.Name == form.Name.Value })
	isVariable := slices.ContainsFunc(svc.Env, func(v app.EnvVar) bool { return v.Name == form.Name.Value })
	switch {
	case !form.Secret && isSecret:
		form.Name.Error = sentence{mono(form.Name.Value), plain(" is already a secret of this service. Keep this one as a secret, or remove the secret first.")}
		refuse(http.StatusUnprocessableEntity, &form)
		return
	case form.Secret && isVariable:
		form.Name.Error = sentence{mono(form.Name.Value), plain(" is already a variable of this service. Edit it as a variable, or choose another name.")}
		refuse(http.StatusUnprocessableEntity, &form)
		return
	case !form.Secret && strings.Contains(value, "\n"):
		form.Value.Error = sentence{plain("A variable's value is one line. Keep it as a secret for a value with line breaks.")}
		refuse(http.StatusUnprocessableEntity, &form)
		return
	}

	cond := service.Condition{IfRevision: revision}
	var deployment store.DeploymentRecord
	var edited app.App
	if form.Secret {
		deployment, err = s.svc.SetSecretIf(ctx, id, svc.Name, form.Name.Value, value, cond, app.TriggerConsole)
		edited = rec.App
	} else {
		edited = withEnv(rec.App, i, setEnvVar(svc.Env, form.Name.Value, value))
		deployment, err = s.svc.DeployIf(ctx, edited, cond, app.TriggerConsole)
	}
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}
	s.refuseVariable(w, r, id, edited, &form, err)
}

// setEnvVar is env with name set to value: replaced where it is, else added last.
func setEnvVar(env []app.EnvVar, name, value string) []app.EnvVar {
	out := slices.Clone(env)
	if i := slices.IndexFunc(out, func(v app.EnvVar) bool { return v.Name == name }); i >= 0 {
		out[i].Value = value
		return out
	}
	return append(out, app.EnvVar{Name: name, Value: value})
}

// withEnv is the app with service i's environment replaced; a is not changed.
func withEnv(a app.App, i int, env []app.EnvVar) app.App {
	a.Services = slices.Clone(a.Services)
	a.Services[i].Env = env
	return a
}

// secretValueField matches an error about a secret's value, or about a
// service's secrets as a whole (their size).
var secretValueField = regexp.MustCompile(`^services\[\d+\]\.secrets(\[\d+\]\.value)?$`)

// refuseVariable draws the page again after the Add form's change was refused:
// the typed name and service stay (a variable's value too), the message sits at
// the input it is about.
func (s *server) refuseVariable(w http.ResponseWriter, r *http.Request, id string, edited app.App, form *addVariableForm, err error) {
	var fieldErr *app.FieldError
	status := http.StatusUnprocessableEntity
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case errors.Is(err, service.ErrAppChanged):
		status, form.Notice = http.StatusConflict, sentence{plain(changedSincePage)}
	case errors.As(err, &fieldErr) && (fieldErr.Field == "name" || strings.HasSuffix(fieldErr.Field, ".name")):
		form.Name.Error = sentence{plain(fieldErr.Message)}
	case errors.As(err, &fieldErr) && secretValueField.MatchString(fieldErr.Field):
		form.Value.Error = sentence{plain(fieldErr.Message)}
	case errors.Is(err, secrets.ErrUnavailable) && !errors.Is(err, deploy.ErrNomadUnavailable):
		status = http.StatusBadGateway
		form.Notice = sentence{plain("The secret store refused the change, so nothing was deployed. The running copies keep their secrets.")}
		s.log.Warn("can't change a secret: the secret store refused", "app", id, "error", err)
	default:
		var notice sentence
		var ok bool
		if status, notice, ok = refusalOf(edited, err); !ok {
			s.serverError(w, "change a variable", err)
			return
		}
		form.Notice = notice
	}
	s.variablesPage(w, r, status, id, variablesExtras{Add: form})
}

// refusalOf says what a refused deploy of an edited app looks like on a page
// that edits one thing: the status and the sentence, with the words of the
// Configure form (a field error that no input is for names its path; a deploy
// that failed after the app was saved says so). ok is false for an error the
// pages have no words for.
func refusalOf(edited app.App, err error) (status int, notice sentence, ok bool) {
	var f editForm
	status, ok = refuseEdit(&f, edited, err)
	return status, f.Notice, ok
}

// saveEnvText answers POST /apps/{id}/variables/env: one service's .env text
// replaces its variables, as a deploy on the page's revision. The text is a list
// of NAME=value lines; secrets are not in it, and they are left as they are.
func (s *server) saveEnvText(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "save it")
	if !ok {
		return
	}
	rec, err := s.svc.GetApp(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("service"))
	i, found := serviceNamed(rec.App, name)
	if !found {
		s.variablesPage(w, r, http.StatusUnprocessableEntity, id, variablesExtras{Notice: sentence{plain("This app has no service "), mono(name), plain(".")}})
		return
	}
	if rec.Compose != nil {
		s.variablesPage(w, r, http.StatusConflict, id, variablesExtras{Notice: sentence{plain(fileEditedSentence + " Change the file in Configure and deploy it.")}})
		return
	}

	text := strings.ReplaceAll(r.PostForm.Get("env"), "\r\n", "\n")
	typed := map[string]formField{name: {Value: text}}
	refuse := func(status int, message sentence) {
		field := typed[name]
		field.Error, field.Alert = message, true
		typed[name] = field
		s.variablesPage(w, r, status, id, variablesExtras{Env: typed})
	}
	env, problem := parseEnv(text)
	if problem != "" {
		refuse(http.StatusUnprocessableEntity, sentence{plain(problem)})
		return
	}
	for _, v := range env {
		if slices.ContainsFunc(rec.App.Services[i].Secrets, func(sec app.Secret) bool { return sec.Name == v.Name }) {
			refuse(http.StatusUnprocessableEntity, sentence{mono(v.Name), plain(" is a secret of this service. Remove the secret first, or leave this line out.")})
			return
		}
	}

	edited := withEnv(rec.App, i, env)
	deployment, err := s.svc.DeployIf(ctx, edited, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}
	var fieldErr *app.FieldError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case errors.Is(err, service.ErrAppChanged):
		s.variablesPage(w, r, http.StatusConflict, id, variablesExtras{Env: typed, Notice: sentence{plain(changedSincePage)}})
	case errors.As(err, &fieldErr) && strings.Contains(fieldErr.Field, ".env["):
		refuse(http.StatusUnprocessableEntity, sentence{plain(fieldErr.Message)})
	default:
		status, notice, ok := refusalOf(edited, err)
		if !ok {
			s.serverError(w, "save variables", err)
			return
		}
		s.variablesPage(w, r, status, id, variablesExtras{Env: typed, Notice: notice})
	}
}

// removeSecret answers POST /apps/{id}/secrets/remove: it deploys without one
// secret, keeping the others (service.RemoveSecretIf). A form that carries the
// revision it was drawn from makes the deploy conditional on it.
func (s *server) removeSecret(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	cond := service.Condition{IfRevision: strings.TrimSpace(r.PostForm.Get("revision"))}
	deployment, err := s.svc.RemoveSecretIf(r.Context(), id, strings.TrimSpace(r.PostForm.Get("service")), r.PostForm.Get("name"), cond, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}
	var fieldErr *app.FieldError
	status := http.StatusUnprocessableEntity
	var notice sentence
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case errors.Is(err, service.ErrAppChanged):
		status, notice = http.StatusConflict, sentence{plain(changedSincePage)}
	case errors.As(err, &fieldErr):
		notice = sentence{plain("caravana can't remove this secret: " + fieldErr.Message)}
	case errors.Is(err, secrets.ErrUnavailable) && !errors.Is(err, deploy.ErrNomadUnavailable):
		status = http.StatusBadGateway
		notice = sentence{plain("The secret store refused the change, so nothing was deployed. The running copies keep their secrets.")}
		s.log.Warn("can't change a secret: the secret store refused", "app", id, "error", err)
	default:
		var ok bool
		if status, notice, ok = refuseDeploy(&deployForm{}, err); !ok {
			s.serverError(w, "remove a secret", err)
			return
		}
	}
	s.variablesPage(w, r, status, id, variablesExtras{Notice: notice})
}
