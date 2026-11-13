package ui

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

// This file is New app › From a template (feature 035): the grid of known apps, the page of one
// with its inputs, the catalog's footer, the logo and the notices the app's own pages draw
// for a template app. Installing is a Compose deploy of a new app (service.InstallTemplate); the
// console only asks the questions.
//
// Secrets rule: a generated secret never reaches this package (the service makes it and sends
// it to the secret store), and a typed secret input is read from the post, handed to the
// service and dropped: it is never put in a field, a page, an address or a log line.

// ---- the grid -------------------------------------------------------------------------

// templateCard is one template in the grid.
type templateCard struct {
	Name, Title, Version, Description string
	Tags                              []string
	Href                              string
	Logo                              string // the logo's address; "" draws the monogram tile
	Letter                            string // the monogram's letter
	HueClass                          string
}

// templateTag is one tag link above the grid.
type templateTag struct {
	Name, Href string
	Current    bool
}

// templateGrid is the "From a template" tab.
type templateGrid struct {
	Q, Tag string
	Place  placeChoice
	Tags   []templateTag
	AllHref,
	ClearHref string
	Cards []templateCard
	// Footer says which catalog is offered, and how its check went.
	Version  sentence
	Error    sentence // why the last check failed
	Checking bool     // the owner's setting
	// Notice is what a post just did (?checked=1, ?saved=1); Refusal why a check was refused.
	Notice  sentence
	Refusal sentence
	// HiddenPlace holds the place's query values as hidden inputs of the footer's forms.
	HiddenPlace []formHidden
}

// formHidden is a hidden input of a form.
type formHidden struct{ Name, Value string }

// monogramOf is the first letter of a template's title, upper case.
func monogramOf(title string) string {
	for _, r := range title {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// templateHue is a template's colour class: the console's one identity hue, a stable hash over six.
func templateHue(name string) string { return "cv-hue-" + string(rune('0'+hueOf("template", name))) }

// templateLogoSrc is the address of a template's logo, or "".
func templateLogoSrc(t service.TemplateInfo) string {
	if t.Logo == "" {
		return ""
	}
	return "/templates/" + url.PathEscape(t.Name) + "/logo.svg"
}

// placeQuery is a place's query ("project=..&environment=.."), "" for Default's production.
func placeValues(c placeChoice) url.Values {
	q := url.Values{}
	if project, environment, ok := strings.Cut(c.Value, "/"); ok && c.Value != homeValue {
		q.Set("project", project)
		q.Set("environment", environment)
	}
	return q
}

// gridHref is a link to the grid with the search, the tag and the place.
func gridHref(q, tag string, place placeChoice) string {
	v := placeValues(place)
	v.Set("from", "template")
	if q != "" {
		v.Set("q", q)
	}
	if tag != "" {
		v.Set("tag", tag)
	}
	return "/apps/new?" + v.Encode()
}

// loadTemplateGrid reads the catalog for the grid: what ?q= and ?tag= ask for, and the tags of
// everything offered, so choosing a tag never hides the other tags.
func (s *server) loadTemplateGrid(r *http.Request, place string) (templateGrid, error) {
	ctx := r.Context()
	q, tag := strings.TrimSpace(r.URL.Query().Get("q")), strings.TrimSpace(r.URL.Query().Get("tag"))
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return templateGrid{}, err
	}
	choice := pl.choice(place)
	all, err := s.svc.ListTemplates(ctx, "", "")
	if err != nil {
		return templateGrid{}, err
	}
	shown, err := s.svc.ListTemplates(ctx, q, tag)
	if err != nil {
		return templateGrid{}, err
	}
	g := templateGrid{Q: q, Tag: tag, Place: choice, AllHref: gridHref(q, "", choice), ClearHref: gridHref("", "", choice)}
	var tags []string
	for _, t := range all.Templates {
		if !t.Available {
			continue
		}
		for _, name := range t.Tags {
			if !slices.Contains(tags, name) {
				tags = append(tags, name)
			}
		}
	}
	slices.Sort(tags)
	for _, name := range tags {
		g.Tags = append(g.Tags, templateTag{Name: name, Href: gridHref(q, name, choice), Current: name == tag})
	}
	for _, t := range shown.Templates {
		if !t.Available {
			continue // the API lists it with its reason; the console leaves it out
		}
		href := "/apps/new/template/" + url.PathEscape(t.Name)
		if pq := placeValues(choice).Encode(); pq != "" {
			href += "?" + pq
		}
		g.Cards = append(g.Cards, templateCard{Name: t.Name, Title: t.Title, Version: t.Version, Description: t.Description,
			Tags: t.Tags, Href: href, Logo: templateLogoSrc(t), Letter: monogramOf(t.Title), HueClass: templateHue(t.Name)})
	}
	for k, v := range placeValues(choice) {
		g.HiddenPlace = append(g.HiddenPlace, formHidden{Name: k, Value: v[0]})
	}
	slices.SortFunc(g.HiddenPlace, func(a, b formHidden) int { return strings.Compare(a.Name, b.Name) })

	c := shown.Catalog
	g.Checking = c.Checking
	if c.Source == service.CatalogFetched {
		g.Version = sentence{plain("Catalog "), mono(c.Version), plain(", fetched " + clockText(c.FetchedAt) + ".")}
	} else {
		g.Version = sentence{plain("Catalog "), mono(c.Version), plain(", built in.")}
	}
	if c.Error != "" {
		g.Error = sentence{plain("The last check failed: " + c.Error + ".")}
	}
	switch r.URL.Query().Get("checked") {
	case "1":
		g.Notice = sentence{plain("Checking for new templates. Refresh this page in a few seconds to see the result.")}
	}
	switch r.URL.Query().Get("saved") {
	case "1":
		if c.Checking {
			g.Notice = sentence{plain("caravana checks for new templates once a day.")}
		} else {
			g.Notice = sentence{plain("caravana won't check for new templates. It makes no request about them.")}
		}
	}
	return g, nil
}

// clockText is a time as the footer says it: "2026-11-12 09:30 UTC".
func clockText(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

// drawTemplateGrid draws New app with the template tab.
func (s *server) drawTemplateGrid(w http.ResponseWriter, r *http.Request, status int, g templateGrid) {
	head, err := s.shellPage(r, "New app", navHome, crumb{Label: "New app"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	s.render(w, status, "newapp", newAppPage{pageData: head, Templates: &g})
}

// showTemplateGrid answers GET /apps/new?from=template.
func (s *server) showTemplateGrid(w http.ResponseWriter, r *http.Request) {
	g, err := s.loadTemplateGrid(r, placeAsked(r.URL.Query()))
	if err != nil {
		s.serverError(w, r, "list templates", err)
		return
	}
	s.drawTemplateGrid(w, r, http.StatusOK, g)
}

// backToGrid is where the footer's posts go back to: the grid, with the notice's flag.
func backToGrid(r *http.Request, flag string) string {
	v := url.Values{"from": {"template"}, flag: {"1"}}
	if project, environment := r.PostForm.Get("project"), r.PostForm.Get("environment"); project != "" && environment != "" {
		v.Set("project", project)
		v.Set("environment", environment)
	}
	return "/apps/new?" + v.Encode()
}

// checkTemplates answers POST /templates/check: it starts a check and goes back to the grid. A
// check that is refused (too soon, or turned off) draws the grid again with the reason.
func (s *server) checkTemplates(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	err := s.svc.CheckTemplates(r.Context())
	if err == nil {
		http.Redirect(w, r, backToGrid(r, "checked"), http.StatusSeeOther)
		return
	}
	var words sentence
	status := http.StatusConflict
	var soon *service.CheckTooSoonError
	switch {
	case errors.As(err, &soon):
		words, status = sentence{plain(soon.Error())}, http.StatusTooManyRequests
	case errors.Is(err, service.ErrCheckDisabled):
		words = sentence{plain("Checking for new templates is turned off.")}
	default:
		s.serverError(w, r, "check templates", err)
		return
	}
	g, err := s.loadTemplateGrid(r, placeAsked(r.PostForm))
	if err != nil {
		s.serverError(w, r, "list templates", err)
		return
	}
	g.Refusal = words
	s.drawTemplateGrid(w, r, status, g)
}

// saveTemplateSettings answers POST /templates/settings: the one switch, checking on or off.
func (s *server) saveTemplateSettings(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	switch r.PostForm.Get("checking") {
	case "on", "off":
	default:
		s.message(w, r, http.StatusBadRequest, "Bad request", "That form couldn't be read.", "Go back and try again.")
		return
	}
	if err := s.svc.SetTemplateChecking(r.Context(), r.PostForm.Get("checking") == "on"); err != nil {
		s.serverError(w, r, "save template setting", err)
		return
	}
	http.Redirect(w, r, backToGrid(r, "saved"), http.StatusSeeOther)
}

// showTemplateLogo answers GET /templates/{name}/logo.svg: the template's SVG as an image that
// runs nothing (research R8).
func (s *server) showTemplateLogo(w http.ResponseWriter, r *http.Request, session string) {
	t, err := s.svc.Template(r.Context(), r.PathValue("name"))
	if errors.Is(err, service.ErrTemplateNotFound) || (err == nil && t.Logo == "") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, "read template", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", templates.LogoContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", templates.LogoCSP)
	_, _ = w.Write([]byte(t.Logo))
}

// ---- one template ---------------------------------------------------------------------

// templateInputView is one question of the form.
type templateInputView struct {
	Field                formField
	Label, Help, Example string
	Type                 string // the HTML input type
	Mode                 string // inputmode, or ""
	Secret               bool
	Required             bool
}

// templateServiceView is one line of "What it makes".
type templateServiceView struct {
	Name, Image string
	Domains     []string // "your domain"
	TeamOnly    bool
	Backup      string // "postgres", "volume", "sqlite", or ""
}

// templatePreviewView is what a Preview shows: the app the answers make.
type templatePreviewView struct {
	Services []templateServiceView
	Warnings []string
}

// templateFormView is the template's form with what was typed (never a secret).
type templateFormView struct {
	Action string
	Name   formField
	Place  placeChoice
	Inputs []templateInputView
	// WithoutBackups is the checkbox "Install without backups for now".
	WithoutBackups bool
	Notice         sentence
	SavedID        string
	Focus          string
	// Posted says the form is drawn again after a post, so a secret that was typed is gone.
	Posted bool
}

// templatePage is the page of one template.
type templatePage struct {
	pageData
	Name, Title, Version, Description, Upstream string
	Tags                                        []string
	Logo, Letter, HueClass                      string
	BackHref                                    string
	Services                                    []templateServiceView
	// Generated says how many passwords Caravana makes, and which secrets hold them.
	Generated      int
	GeneratedNames []string
	// Notes are the template's notes with each answer shown as "<your domain>"-style words.
	Notes string
	// NoDestination is set when the template has a backup and none is set to receive it.
	NoDestination  bool
	BackupServices []string
	Form           templateFormView
	Preview        *templatePreviewView
}

// engineWord says how a backup engine is called on the page.
func engineWord(b *app.Backup) string {
	if b == nil {
		return ""
	}
	return b.Engine
}

// servicesOf lists the services of the template as the page says them: from the preview of its example
// answers (which has the images), or, when that can't be made, from its overlay alone.
func (s *server) templateServices(r *http.Request, t service.TemplateInfo) []templateServiceView {
	hostWords := func(host string) string {
		probe := t.Template
		probe.Notes = host
		return strings.Trim(probe.RenderNotes(nil), "<>")
	}
	images := map[string]string{}
	if p, err := s.svc.PreviewTemplate(r.Context(), t.Name, t.AppName, t.ExampleInputs(), service.TemplateInstallOptions{}); err == nil {
		for _, svc := range p.App.Services {
			images[svc.Name] = svc.Image
		}
	}
	names := make([]string, 0, len(t.Services))
	for name := range t.Services {
		names = append(names, name)
	}
	for name := range images {
		if _, ok := t.Services[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var out []templateServiceView
	for _, name := range names {
		ov := t.Services[name]
		v := templateServiceView{Name: name, Image: images[name], Backup: engineWord(ov.Backup)}
		for _, d := range ov.Domains {
			v.Domains = append(v.Domains, hostWords(d.Host))
			v.TeamOnly = v.TeamOnly || d.TeamOnly
		}
		out = append(out, v)
	}
	return out
}

// generatedSecrets says how many passwords Caravana makes and the names of the secrets that
// hold them, in the order the overlay lists them.
func generatedSecrets(t service.TemplateInfo) (int, []string) {
	generated := map[string]bool{}
	for _, g := range t.Generated {
		generated[g.Name] = true
	}
	var names []string
	for _, svc := range t.Services {
		for _, sec := range svc.Secrets {
			if sec.Value == nil {
				continue
			}
			for _, m := range placeholderNames(*sec.Value) {
				if generated[m] && !slices.Contains(names, sec.Name) {
					names = append(names, sec.Name)
				}
			}
		}
	}
	slices.Sort(names)
	return len(t.Generated), names
}

// placeholderNames lists the names in the {{placeholders}} of text.
func placeholderNames(text string) []string {
	var out []string
	for {
		i := strings.Index(text, "{{")
		if i < 0 {
			return out
		}
		j := strings.Index(text[i:], "}}")
		if j < 0 {
			return out
		}
		out = append(out, strings.TrimSpace(text[i+2:i+j]))
		text = text[i+j+2:]
	}
}

// newTemplateForm is the form of t with nothing typed: its inputs in order, a default name.
func newTemplateForm(t service.TemplateInfo, place string) templateFormView {
	f := templateFormView{Action: "/apps/new/template/" + url.PathEscape(t.Name), Name: formField{ID: "name", Value: t.AppName}}
	f.Place.Value = place
	for _, in := range t.Inputs {
		v := templateInputView{Field: formField{ID: "in_" + in.Name}, Label: in.Label, Help: in.Help, Example: in.Example,
			Type: "text", Secret: in.Type == templates.TypeSecret, Required: in.Default == ""}
		switch in.Type {
		case templates.TypeDomain:
			v.Mode = "url"
		case templates.TypeEmail:
			v.Type, v.Mode = "email", "email"
		case templates.TypeSecret:
			v.Type = "password"
		}
		if in.Type != templates.TypeSecret {
			v.Field.Value = in.Default
		}
		f.Inputs = append(f.Inputs, v)
	}
	return f
}

// markFirstError chooses the error that is announced and the field that is focused.
func (f *templateFormView) markFirstError() {
	if len(f.Name.Error) > 0 {
		f.Name.Alert, f.Focus = true, "name"
		return
	}
	for i := range f.Inputs {
		if len(f.Inputs[i].Field.Error) > 0 {
			f.Inputs[i].Field.Alert, f.Focus = true, f.Inputs[i].Field.ID
			return
		}
	}
}

// showTemplate answers GET /apps/new/template/{name}.
func (s *server) showTemplate(w http.ResponseWriter, r *http.Request, session string) {
	t, ok := s.templateOrMessage(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	s.drawTemplate(w, r, http.StatusOK, t, newTemplateForm(t, placeAsked(r.URL.Query())), nil)
}

// templateOrMessage reads the template, or answers 404 with the words of why it isn't there.
func (s *server) templateOrMessage(w http.ResponseWriter, r *http.Request, name string) (service.TemplateInfo, bool) {
	t, err := s.svc.Template(r.Context(), name)
	switch {
	case errors.Is(err, service.ErrTemplateNotFound):
		s.message(w, r, http.StatusNotFound, "Not found", "There is no such template.", "Go back to New app and choose one from the list.")
		return t, false
	case err != nil:
		s.serverError(w, r, "read template", err)
		return t, false
	case !t.Available:
		s.message(w, r, http.StatusNotFound, "Not found", "This template isn't offered: "+t.Unavailable+".", "Go back to New app and choose another.")
		return t, false
	}
	return t, true
}

// drawTemplate draws the page of a template with the form as it is.
func (s *server) drawTemplate(w http.ResponseWriter, r *http.Request, status int, t service.TemplateInfo, form templateFormView, preview *templatePreviewView) {
	ctx := r.Context()
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		s.serverError(w, r, "list projects", err)
		return
	}
	refusal := form.Place.Error
	form.Place = pl.choice(form.Place.Value)
	form.Place.Error = refusal

	head, err := s.shellPage(r, t.Title, navHome, crumb{Label: "New app", Href: gridHref("", "", form.Place)}, crumb{Label: t.Title})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page := templatePage{pageData: head, Name: t.Name, Title: t.Title, Version: t.Version, Description: t.Description,
		Upstream: t.Upstream, Tags: t.Tags, Logo: templateLogoSrc(t), Letter: monogramOf(t.Title), HueClass: templateHue(t.Name),
		BackHref: gridHref("", "", form.Place), Services: s.templateServices(r, t), Notes: t.RenderNotes(nil),
		Form: form, Preview: preview}
	page.Generated, page.GeneratedNames = generatedSecrets(t)
	for _, svc := range page.Services {
		if svc.Backup != "" {
			page.BackupServices = append(page.BackupServices, svc.Name)
		}
	}
	if len(page.BackupServices) > 0 {
		if _, err := s.svc.GetBackupDestination(ctx); errors.Is(err, store.ErrNotFound) {
			page.NoDestination = true
		} else if err != nil {
			s.serverError(w, r, "read backup destination", err)
			return
		}
	}
	s.render(w, status, "template", page)
}

// createFromTemplate answers POST /apps/new/template/{name}: Create installs the template as a new
// app and goes to its Deployments tab; Preview (action=preview) draws the page again with the app
// the answers make. A refusal draws the form again with the message at the input the owner typed.
// Typed secrets are used and dropped: the form is drawn without them.
func (s *server) createFromTemplate(w http.ResponseWriter, r *http.Request, session string) {
	t, ok := s.templateOrMessage(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	if !s.readForm(w, r) {
		return
	}
	val := func(name string) string { return strings.TrimSpace(r.PostForm.Get(name)) }
	form := newTemplateForm(t, val("place"))
	form.Posted = true
	form.Name.Value = val("name")
	form.WithoutBackups = r.PostForm.Get("withoutBackups") != ""
	answers := map[string]string{}
	for i, in := range t.Inputs {
		answers[in.Name] = val("in_" + in.Name)
		if in.Type != templates.TypeSecret {
			form.Inputs[i].Field.Value = answers[in.Name]
		}
	}
	preview := r.PostForm.Get("action") == "preview"

	pl, err := s.loadPlaces(r.Context())
	if err != nil {
		s.serverError(w, r, "list projects", err)
		return
	}
	switch {
	case form.Name.Value == "":
		form.Name.Error = sentence{plain("Enter a name for the app.")}
	case len(form.Name.Value) > maxNewAppName:
		form.Name.Error = sentence{plain("The name can have at most 31 characters.")}
	}
	place, refusal, placed := pl.placeFor(form.Place.Value, form.Name.Value)
	if !placed {
		form.Place.Error = refusal
	}
	if len(form.Name.Error) > 0 || len(form.Place.Error) > 0 {
		form.markFirstError()
		s.drawTemplate(w, r, http.StatusUnprocessableEntity, t, form, nil)
		return
	}
	id := place.ID()
	opts := service.TemplateInstallOptions{WithoutBackups: form.WithoutBackups}

	if preview {
		p, err := s.svc.PreviewTemplate(r.Context(), t.Name, id, answers, opts)
		if err != nil {
			status := s.refuseTemplate(&form, t, id, err)
			if status == 0 {
				s.serverError(w, r, "preview template", err)
				return
			}
			form.markFirstError()
			s.drawTemplate(w, r, status, t, form, nil)
			return
		}
		view := &templatePreviewView{}
		for _, svc := range p.App.Services {
			sv := templateServiceView{Name: svc.Name, Image: svc.Image}
			for _, d := range svc.Domains {
				sv.Domains = append(sv.Domains, "your domain")
				sv.TeamOnly = sv.TeamOnly || d.TeamOnly
			}
			if svc.Backup != nil {
				sv.Backup = svc.Backup.Engine
			}
			view.Services = append(view.Services, sv)
		}
		for _, w := range p.Warnings {
			view.Warnings = append(view.Warnings, w.Message)
		}
		s.drawTemplate(w, r, http.StatusOK, t, form, view)
		return
	}

	_, err = s.svc.InstallTemplate(r.Context(), t.Name, id, answers, opts, app.TriggerConsole)
	var deployErr *service.DeployError
	if err == nil {
		http.Redirect(w, r, installedHref(t, id, answers), http.StatusSeeOther)
		return
	}
	if errors.As(err, &deployErr) {
		// The app was saved and its first deploy didn't start: the name is taken now.
		form.SavedID = id
	}
	status := s.refuseTemplate(&form, t, id, err)
	if status == 0 {
		s.serverError(w, r, "install template", err)
		return
	}
	form.markFirstError()
	s.drawTemplate(w, r, status, t, form, nil)
}

// installedHref is where a finished install goes: the app's Deployments tab, with the flag the
// created notice reads and the answers of the plain inputs, so the notes can say them. A secret
// input is never in it.
func installedHref(t service.TemplateInfo, id string, answers map[string]string) string {
	v := url.Values{"created": {"template"}}
	// The checked answers (trimmed, a domain in lower case), as the service used them.
	if checked, err := t.CheckPreviewInputs(answers); err == nil {
		answers = checked
	}
	for _, in := range t.Inputs {
		if in.Type != templates.TypeSecret && answers[in.Name] != "" {
			v.Set("v_"+in.Name, answers[in.Name])
		}
	}
	return "/apps/" + url.PathEscape(id) + "/deployments?" + v.Encode()
}

// refuseTemplate puts a refusal where it belongs on the form and returns the HTTP status, or 0 for
// an error the page has no words for.
func (s *server) refuseTemplate(f *templateFormView, t service.TemplateInfo, id string, err error) int {
	var fieldErr *app.FieldError
	var taken *service.DomainTakenError
	var placeErr *service.PlaceError
	onInput := func(name, message string) bool {
		for i := range f.Inputs {
			if f.Inputs[i].Field.ID == "in_"+name {
				f.Inputs[i].Field.Error = sentence{plain(message)}
				return true
			}
		}
		return false
	}
	switch {
	case errors.Is(err, service.ErrAppExists):
		f.Name.Error = takenSentence(id)
		return http.StatusUnprocessableEntity
	case errors.As(err, &taken):
		input, _ := strings.CutPrefix(taken.Field, "inputs.")
		words := sentence{plain("That domain is already used by "), mono(taken.App), plain(". Remove it there first, or use another domain.")}
		if !onInputSentence(f, input, words) {
			f.Notice = words
		}
		return http.StatusConflict
	case errors.As(err, &placeErr), errors.Is(err, service.ErrProjectNotFound), errors.Is(err, service.ErrEnvironmentNotFound):
		f.Place.Error = sentence{plain("That environment doesn't exist any more. Choose another.")}
		return http.StatusUnprocessableEntity
	case errors.As(err, &fieldErr):
		switch {
		case strings.HasPrefix(fieldErr.Field, "inputs."):
			if !onInput(strings.TrimPrefix(fieldErr.Field, "inputs."), inputMessage(fieldErr.Message)) {
				f.Notice = sentence{plain("This answer isn't accepted: " + fieldErr.Field + " " + fieldErr.Message)}
			}
		case fieldErr.Field == "id":
			f.Name.Error = sentence{plain(fieldMessage("id", fieldErr.Message))}
		case strings.HasSuffix(fieldErr.Field, ".backup") && strings.Contains(fieldErr.Message, "backup destination"):
			f.Notice = sentence{plain("No backup destination is set, so a backup has nowhere to go. Set one in "), plain("Settings › Backup storage"),
				plain(", or install without backups for now.")}
		default:
			f.Notice = sentence{plain("The " + t.Title + " template can't be installed: "), mono(fieldErr.Field), plain(" " + fieldErr.Message)}
		}
		return http.StatusUnprocessableEntity
	}
	// Image, registry, Nomad and secret store failures read as they do for any new app.
	probe := newForm()
	status, ok := refuseNewApp(&probe, id, err)
	if !ok {
		return 0
	}
	if f.SavedID == "" {
		f.SavedID = probe.SavedID
	}
	for _, words := range []sentence{probe.Notice, probe.Image.Error, probe.Name.Error} {
		if len(words) > 0 {
			f.Notice = words
			break
		}
	}
	return status
}

// onInputSentence puts words on the input called name; false when there is none.
func onInputSentence(f *templateFormView, name string, words sentence) bool {
	for i := range f.Inputs {
		if f.Inputs[i].Field.ID == "in_"+name {
			f.Inputs[i].Field.Error = words
			return true
		}
	}
	return false
}

// inputMessage is a refusal of an answer as a sentence: the service's predicate ("is required",
// "must be a host name like ...") gets its subject.
func inputMessage(message string) string {
	if message == "" {
		return "This answer isn't accepted."
	}
	return "This answer " + message + "."
}

// ---- what the app's own pages say ----------------------------------------------------------

// installedNotice is the created notice of a template app's Deployments tab: where it came from and
// its notes with the answers that were typed (the plain ones, from the address).
type installedNotice struct {
	Title sentence
	Notes string
}

// installedNoticeOf reads ?created=template for an app that does come from a template. The answers
// in the address are used only for the plain inputs the template asks for, and only when they
// pass the same checks as when they were typed.
func (s *server) installedNoticeOf(r *http.Request, rec store.AppRecord) *installedNotice {
	if r.URL.Query().Get("created") != "template" || !rec.Template.Set() {
		return nil
	}
	n := &installedNotice{Title: sentence{plain("Installed from the "), mono(rec.Template.Name), plain(" template.")}}
	info, err := s.svc.Template(r.Context(), rec.Template.Name)
	if err != nil {
		return n
	}
	n.Title = sentence{plain("Installed from the " + info.Title + " template.")}
	answers := map[string]string{}
	for _, in := range info.Inputs {
		if in.Type != templates.TypeSecret {
			answers[in.Name] = r.URL.Query().Get("v_" + in.Name)
		}
	}
	checked, err := info.CheckPreviewInputs(answers)
	if err != nil {
		checked = nil
	}
	// The address is anybody's to write: a crafted link must not make the console say "open
	// https://<somewhere else>". An answer is shown only when the app itself has it, in its kept
	// Compose file or as one of its domains; else the notes keep "<your …>".
	held := appTexts(rec)
	for name, v := range checked {
		if !slices.ContainsFunc(held, func(s string) bool { return strings.Contains(s, v) }) {
			delete(checked, name)
		}
	}
	n.Notes = info.RenderNotes(checked)
	return n
}

// appTexts is what an installed app holds that a template's plain answers end up in: the kept
// Compose file (with its overlay's domains) and the domains of every service.
func appTexts(rec store.AppRecord) []string {
	var out []string
	if rec.Compose != nil {
		out = append(out, rec.Compose.Compose)
		for _, ov := range rec.Compose.Services {
			for _, d := range ov.Domains {
				out = append(out, d.Host)
			}
		}
	}
	for _, svc := range rec.App.Services {
		for _, d := range svc.Domains {
			out = append(out, d.Host)
		}
	}
	return out
}

// templateFactOf is Configure's "Template · <title> <version>" fact of an app that came from a template.
func (s *server) templateFactOf(r *http.Request, rec store.AppRecord) sentence {
	if !rec.Template.Set() {
		return nil
	}
	title := rec.Template.Name
	if info, err := s.svc.Template(r.Context(), rec.Template.Name); err == nil {
		title = info.Title
	}
	return sentence{plain("Template · "), plain(title + " "), mono(rec.Template.Version)}
}
