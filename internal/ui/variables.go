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
	// Info is a sentence that isn't a refusal: "Secrets unchanged".
	Info sentence
	// Providers are the secret providers this app's place may use, for the Add
	// form's "From a provider" choice (feature 037); none means the choice is
	// absent and the page links to Settings.
	Providers []providerChoice
	// Sources are the rows of "Where secrets come from": Caravana's own, then
	// each provider the app references or may use.
	Sources []sourceRow
	// Builtin is how many of the app's secrets are typed values, kept in Nomad Variables.
	Builtin int
	// BuiltinText is Builtin as the panel says it: "3 values".
	BuiltinText string
	// HasRefs says the app has a secret reference: Refresh secrets is offered.
	HasRefs bool
}

// providerChoice is a provider in the Add form's select.
type providerChoice struct {
	Name, Label string
	Selected    bool
}

// sourceRow is a provider's row in "Where secrets come from": its mark from the
// last test, name, kind and how many references of this app point at it.
type sourceRow struct {
	Name, Mark, Kind, State, Refs string
	StateFailed                   bool
}

// variableRow is one variable or secret of one service. A secret's value is not
// here, so no template can draw it.
type variableRow struct {
	Service, Name, Value string
	Secret               bool
	// From is where a secret reads its value at each deploy; nil for a typed
	// secret and for a variable. It is a name and a path, never a value.
	From *app.SecretRef
}

// addVariableForm is the "Add or change" form with what was typed, never a secret's value.
type addVariableForm struct {
	Service     string
	Services    []string
	Name, Value formField
	Secret      bool
	Notice      sentence
	// Source is "value" (the typed value) or "provider" (a reference, feature
	// 037); Provider and Path are the reference as typed. A secret's value is
	// never put back, a path is not secret.
	Source   string
	Provider formField
	Path     formField
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
			secretRows = append(secretRows, variableRow{Service: svc.Name, Name: sec.Name, Secret: true, From: sec.From})
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
	Info   sentence
	// Providers are every secret provider, with the apps that use them: the
	// page keeps those the app's place may use and those it references.
	Providers []service.SecretProviderInfo
}

// variablesTabOf builds the page's body from the app's summary. It is pure.
func variablesTabOf(sum service.AppSummary, extra variablesExtras) *variablesTab {
	a := sum.App
	tab := &variablesTab{ID: a.ID, Revision: sum.Revision, Defined: a.Defined(), Kept: sum.Compose != nil,
		Stopped: sum.StoppedAt != nil, Many: len(a.Services) > 1, Rows: variableRowsOf(a), Notice: extra.Notice, Info: extra.Info,
		HasRefs: service.HasSecretReferences(a)}
	tab.Add = addVariableForm{Name: formField{ID: "var-name"}, Value: formField{ID: "var-value"}, Secret: true, Source: "value"}
	if extra.Add != nil {
		tab.Add = *extra.Add
		tab.Add.Name.ID, tab.Add.Value.ID = "var-name", "var-value"
		if tab.Add.Source == "" {
			tab.Add.Source = "value"
		}
	}
	tab.Add.Provider.ID, tab.Add.Path.ID = "var-provider", "var-path"
	tab.Providers, tab.Sources, tab.Builtin = secretSourcesOf(a, extra.Providers, tab.Add.Provider.Value)
	tab.BuiltinText = plural(tab.Builtin, "value")
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

// secretSourcesOf works out what the Variables page says about providers: the
// select's choices (providers that may be used in the app's place), the rows of
// "Where secrets come from" (those, and any the app still references) and how
// many typed secrets the app keeps in Nomad Variables.
func secretSourcesOf(a app.App, providers []service.SecretProviderInfo, selected string) (choices []providerChoice, rows []sourceRow, builtin int) {
	refs := map[string]int{}
	for _, svc := range a.Services {
		for _, sec := range svc.Secrets {
			if sec.From != nil {
				refs[sec.From.Provider]++
			} else {
				builtin++
			}
		}
	}
	place := placeOfID(a.ID)
	for _, p := range providers {
		allowed := service.ProviderAllows(p.SecretProvider, place)
		if !allowed && refs[p.Name] == 0 {
			continue
		}
		kind := "Bella Baxter · " + p.Project + "/" + p.Environment
		if allowed {
			choices = append(choices, providerChoice{Name: p.Name, Label: p.Name + " · " + p.Project + "/" + p.Environment, Selected: p.Name == selected})
		}
		row := sourceRow{Name: p.Name, Mark: "idle", Kind: kind, State: "not tested yet", Refs: "not referenced"}
		switch t := p.LastTest; {
		case t != nil && t.OK:
			row.Mark, row.State = "healthy", t.Message
		case t != nil:
			row.Mark, row.State, row.StateFailed = "failed", "can't be read", true
		}
		if n := refs[p.Name]; n > 0 {
			row.Refs = plural(n, "reference")
		}
		rows = append(rows, row)
	}
	return choices, rows, builtin
}

// placeOfID is where an app ID puts it; an ID from before projects is in Default's production.
func placeOfID(id string) app.Place {
	if p, err := app.PlaceOf(id); err == nil {
		return p
	}
	return app.Place{Project: app.DefaultProject, Environment: app.DefaultEnvironment, Name: id}
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
		s.serverError(w, r, "read app", err)
		return
	}
	head, err := s.shellPage(r, id+" variables", navProjects, s.appCrumbs(ctx, id)...)
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id
	if extra.Providers, err = s.svc.SecretProviders(ctx); err != nil {
		s.serverError(w, r, "list secret providers", err)
		return
	}
	// The Configure tab stays current: this is one of its pages.
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(ctx, sum, "configure"), VariablesTab: variablesTabOf(sum, extra)})
}

// showVariables answers GET /apps/{id}/variables.
func (s *server) showVariables(w http.ResponseWriter, r *http.Request, session string) {
	extra := variablesExtras{}
	if r.URL.Query().Get("refresh") == "unchanged" {
		extra.Info = secretsUnchanged
	}
	s.variablesPage(w, r, http.StatusOK, r.PathValue("id"), extra)
}

// secretsUnchanged is the answer to Refresh secrets when every value is as it was.
var secretsUnchanged = sentence{plain("Secrets unchanged: nothing to roll out.")}

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
		s.message(w, r, http.StatusBadRequest, "Bad request", "That form has no revision.", "Open the page again and "+what+" from there.")
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
		s.serverError(w, r, "read app", err)
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
	// "From a provider" (feature 037): the secret is a reference, so a typed value is never read.
	fromProvider := r.PostForm.Get("source") == "provider"
	form.Source = "value"
	if fromProvider {
		form.Source, form.Secret, value = "provider", true, ""
		form.Provider.Value = strings.TrimSpace(r.PostForm.Get("provider"))
		form.Path.Value = strings.TrimSpace(r.PostForm.Get("path"))
	}
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
	if fromProvider {
		switch {
		case form.Provider.Value == "":
			form.Provider.Error = sentence{plain("Choose a provider.")}
		case form.Path.Value == "":
			form.Path.Error = sentence{plain("Enter the secret's path in the provider.")}
		}
		if form.Provider.Error != nil || form.Path.Error != nil {
			refuse(http.StatusUnprocessableEntity, &form)
			return
		}
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
	if fromProvider {
		ref := app.SecretRef{Provider: form.Provider.Value, Path: form.Path.Value}
		deployment, err = s.svc.SetSecretRefIf(ctx, id, svc.Name, form.Name.Value, ref, cond, app.TriggerConsole)
		edited = rec.App
	} else if form.Secret {
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
	var refErr *service.ReferenceError
	status := http.StatusUnprocessableEntity
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case errors.Is(err, service.ErrAppChanged):
		status, form.Notice = http.StatusConflict, sentence{plain(changedSincePage)}
	case errors.As(err, &fieldErr) && strings.HasSuffix(fieldErr.Field, ".from.provider"):
		form.Provider.Error = sentence{plain(fieldErr.Message)}
	case errors.As(err, &fieldErr) && strings.HasSuffix(fieldErr.Field, ".from.path"):
		form.Path.Error = sentence{plain(fieldErr.Message)}
	case errors.As(err, &refErr) && errors.Is(refErr.Err, service.ErrSecretNotFound) && form.Source == "provider" &&
		refErr.Provider == form.Provider.Value && refErr.Path == form.Path.Value:
		// The path just typed isn't in the provider: say so at the Path input.
		form.Path.Error = sentence{plain("Provider "), mono(refErr.Provider), plain(" has no secret "), mono(refErr.Path), plain(".")}
		var deployErr *service.DeployError
		if errors.As(err, &deployErr) { // the definition was saved, and the attempt is in the history
			form.Notice = sentence{plain(savedNote)}
		}
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
			s.serverError(w, r, "change a variable", err)
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
		s.serverError(w, r, "read app", err)
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
			s.serverError(w, r, "save variables", err)
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
			s.serverError(w, r, "remove a secret", err)
			return
		}
	}
	s.variablesPage(w, r, status, id, variablesExtras{Notice: notice})
}

// refreshSecrets answers POST /apps/{id}/secrets/refresh (feature 037): every
// reference of the running version is read again. A value that changed rolls the
// app out again (303 to the Deployments tab); nothing changed draws the page
// with a notice (303 to ?refresh=unchanged, so a reload doesn't post again); a
// refusal draws the page again with the reason and its status. No revision is
// needed: it deploys what already runs.
func (s *server) refreshSecrets(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	res, err := s.svc.RefreshSecrets(r.Context(), id, app.TriggerConsole)
	if err == nil {
		if res.Changed && res.Deployment != nil {
			http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+res.Deployment.Deployment.ID, http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/apps/"+id+"/variables?refresh=unchanged", http.StatusSeeOther)
		return
	}
	status := http.StatusConflict
	var notice sentence
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case errors.Is(err, service.ErrNoSecretReferences):
		notice = sentence{plain("This app has no secrets from a provider, so there is nothing to refresh.")}
	case errors.Is(err, service.ErrNoLiveDeployment), errors.Is(err, service.ErrDefinitionNotRecorded):
		notice = sentence{plain("This app has no running version to refresh yet. Deploy it first.")}
	default:
		var ok bool
		if status, notice, ok = refusalOf(app.App{}, err); !ok {
			s.serverError(w, r, "refresh secrets", err)
			return
		}
	}
	s.variablesPage(w, r, status, id, variablesExtras{Notice: notice})
}
