package ui

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// This file is what the Configure tab shows: the definition as a form (one
// panel per service) with the app's facts above it, the secrets, the webhook,
// and the delete section. The handlers of the tab's other sections are in
// app.go; the form's own is here.

// defRow is one fact of the definition: a label and its values (each a mono
// line). Note is a line of small print under the values. A fact with no
// values isn't drawn as a row: its label goes into the panel's one "Using
// defaults" line (defPanelOf).
type defRow struct {
	Label  string
	Values []string
	Note   string
}

// defPanel is one panel of the definition: the app's own facts, or one
// service's. Defaults names the facts nobody set (no value, or the default
// one), which share one line instead of a row each.
type defPanel struct {
	Title    string
	Rows     []defRow
	Defaults []string
}

// defPanelOf makes a panel from its facts, folding those with no value into
// Defaults.
func defPanelOf(title string, rows []defRow) defPanel {
	p := defPanel{Title: title}
	for _, r := range rows {
		if len(r.Values) == 0 {
			p.Defaults = append(p.Defaults, defaultName(r.Label))
			continue
		}
		p.Rows = append(p.Rows, r)
	}
	return p
}

// defaultName is a label as it reads in the middle of a sentence: "Health
// check" becomes "health check", but "CPU" stays.
func defaultName(label string) string {
	if len(label) > 1 && label[1] >= 'A' && label[1] <= 'Z' {
		return label
	}
	return strings.ToLower(label[:1]) + label[1:]
}

// webhookView is the webhook section. URL is the whole webhook address with
// its token, set only in the response that created the token (it is shown
// once and never stored).
type webhookView struct {
	Enabled bool
	Created when
	URL     string
}

// deleteView is the delete section. Goes says what will be removed; Error is
// about the name field, Notice about the delete as a whole (Nomad could not
// do its part). Either one opens the confirmation, so the message is in view.
type deleteView struct {
	ID     string
	Goes   sentence
	Error  sentence
	Notice sentence
}

func (d deleteView) Open() bool { return len(d.Error) > 0 || len(d.Notice) > 0 }

// configureTab is the body of the Configure tab.
type configureTab struct {
	App     defPanel // the app's own facts
	Compose bool     // the app was made from a Compose file: a note says a file replaces edits
	// ComposeForm is the Compose form drawn first when the app keeps its file
	// (feature 015); the definition is then read-only (Edit.ReadOnly).
	ComposeForm *composeView
	// Notice is a refusal of the tab as a whole: a per-field save that an app
	// edited through its file doesn't take.
	Notice sentence
	Edit   editView
	Secrets secretsView
	Webhook webhookView
	Export  exportView
	Delete  deleteView
}

// editView is the form of the Configure tab: the inputs with what was typed
// (or what is stored), one section per service, and, after a refused stale
// save, what changed under the owner.
//
// An app that keeps its Compose file has no form to edit: ReadOnly is set, and
// Panels hold what its services are, one panel each (feature 015).
type editView struct {
	Action   string
	Form     editForm
	Services []editServiceView
	Stale    *staleView
	ReadOnly bool
	Panels   []defPanel
}

// editServiceView is a service's inputs with the facts of it the form keeps.
type editServiceView struct {
	editService
	Kept []defRow
}

// staleView is the panel after a save refused because the app changed since
// the form was opened: the fields where the app now differs from what was
// typed, with the app's current value.
type staleView struct {
	Diffs []staleDiff
}

// staleDiff is one such field.
type staleDiff struct {
	Service, Label, Current string
}

// editViewOf joins the form with the stored app it edits: the facts a service
// keeps come from the stored service of the same name.
func editViewOf(stored app.App, form editForm, stale *staleView) editView {
	v := editView{Action: "/apps/" + stored.ID + "/configure", Form: form, Stale: stale}
	for _, s := range form.Services {
		sv := editServiceView{editService: s}
		for _, svc := range stored.Services {
			if svc.Name == s.Name {
				sv.Kept = keptRows(svc, s)
			}
		}
		v.Services = append(v.Services, sv)
	}
	return v
}

// staleOf lists the fields where the app as stored now differs from what the
// form holds: the current values a refused stale save shows. Services the
// form doesn't have (added since it was drawn) are not listed; their sections
// are in the form that comes back.
func staleOf(current app.App, typed editForm) *staleView {
	now := formOf(current, "")
	v := &staleView{}
	for _, ns := range now.Services {
		ts, ok := typed.service(ns.Name)
		if !ok {
			continue
		}
		typedFields := ts.fields()
		for i, in := range ns.fields() {
			if !ns.shows(in.Name) || sameText(in.Field.Value, typedFields[i].Field.Value) {
				continue
			}
			label := in.Name
			for _, spec := range inputSpecs {
				if spec.Name == in.Name {
					label = spec.Label
				}
			}
			v.Diffs = append(v.Diffs, staleDiff{Service: ns.Name, Label: label, Current: currentText(in.Field.Value)})
		}
	}
	return v
}

// sameText compares two input values as the form does: line breaks and the
// space around the value don't count.
func sameText(a, b string) bool {
	norm := func(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }
	return norm(a) == norm(b)
}

// currentText is an input's stored value on one line, for the stale panel.
func currentText(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "empty"
	}
	return strings.ReplaceAll(v, "\n", ", ")
}

// exportView is the Export section: a download of the live version's
// jobspec, or why there is none yet.
type exportView struct {
	Href string
	Note string
}

// exportOf finds the live version, the newest healthy deployment, among an
// app's deployments (newest first).
func exportOf(appID string, deployments []store.DeploymentRecord) exportView {
	for _, d := range deployments {
		if d.Status != store.StatusHealthy {
			continue
		}
		if d.Definition == nil {
			return exportView{Note: "The live version was deployed before caravana saved what each deployment deployed. Deploy the app again to export it."}
		}
		return exportView{Href: "/apps/" + appID + "/jobspec"}
	}
	return exportView{Note: "Nothing to export yet: the app has no healthy deployment."}
}

// secretsView is the Secrets section: each service's secret names, and the
// one form that sets a secret. No value is ever in it (FR-016).
type secretsView struct {
	Services []secretService
	Many     bool // more than one service: the form asks which
	Form     secretForm
}

// secretService is one service's secret names, in their stored order.
type secretService struct {
	Name  string
	Names []string
}

// secretForm is what the set form shows after a refusal: the service and
// name typed (never the value), and what was wrong.
type secretForm struct {
	Service, Name         string
	NameError, ValueError sentence
	Notice                sentence
}

// secretsOf lays out the stored app's secret names for the Secrets section.
func secretsOf(a app.App, form secretForm) secretsView {
	v := secretsView{Many: len(a.Services) > 1, Form: form}
	for _, svc := range a.Services {
		s := secretService{Name: svc.Name}
		for _, sec := range svc.Secrets {
			s.Names = append(s.Names, sec.Name)
		}
		v.Services = append(v.Services, s)
	}
	return v
}

// appFacts lays out the app's own facts, above the form: what the form
// doesn't edit and the owner may want to read.
func appFacts(a app.App) defPanel {
	rows := []defRow{{Label: "ID", Values: []string{a.ID}}}
	if a.Name != "" {
		rows = append(rows, defRow{Label: "Display name", Values: []string{a.Name}})
	}
	rows = append(rows, defRow{Label: "Source", Values: []string{string(a.Source.Kind)}})
	return defPanelOf("App", rows)
}

// keptRows are the facts of a service the form can't change and a save keeps:
// shown under its inputs so the owner sees what else the service is.
// Environment values are in the form; secrets have their own section.
func keptRows(svc app.Service, s editService) []defRow {
	var rows []defRow
	if svc.Command != "" {
		rows = append(rows, defRow{Label: "Command", Values: []string{svc.Command}})
	}
	if len(svc.Args) > 0 {
		rows = append(rows, defRow{Label: "Arguments", Values: svc.Args})
	}
	if len(svc.Ports) > 0 && !s.ShowPort {
		var ports []string
		for _, p := range svc.Ports {
			ports = append(ports, p.Name+" → "+strconv.Itoa(p.ContainerPort))
		}
		rows = append(rows, defRow{Label: "Ports", Values: ports, Note: "A service with several ports changes them through the API."})
	}
	if svc.Health != nil && svc.Health.Port != "" {
		rows = append(rows, defRow{Label: "Health check port", Values: []string{svc.Health.Port}})
	}
	if len(svc.Volumes) > 0 {
		var volumes []string
		for _, v := range svc.Volumes {
			text := v.Name + " → " + v.Path
			if v.ReadOnly {
				text += " (read-only)"
			}
			volumes = append(volumes, text)
		}
		rows = append(rows, defRow{Label: "Volumes", Values: volumes, Note: "Kept across deploys, erased with the app."})
	}
	if b := svc.Backup; b != nil {
		rows = append(rows, defRow{Label: "Backup", Values: []string{
			fmt.Sprintf("%s, %s (UTC), keeping the last %d", b.Engine, b.ScheduleOrDefault(), b.KeepOrDefault())}})
	}
	return rows
}

// servicePanels shows each service of the app as a panel of facts, for the
// Configure tab of an app that is edited through its Compose file: what the
// file and its overlay made, read-only. Environment values are shown, as the
// edit form shows them; secrets are not here (their names are in the Secrets
// section, their values nowhere).
func servicePanels(a app.App) []defPanel {
	var out []defPanel
	for _, svc := range a.Services {
		rows := []defRow{{Label: "Image", Values: []string{svc.Image}}}
		if len(svc.Env) > 0 {
			var env []string
			for _, v := range svc.Env {
				env = append(env, v.Name+"="+v.Value)
			}
			rows = append(rows, defRow{Label: "Environment", Values: env})
		}
		if svc.Command != "" {
			rows = append(rows, defRow{Label: "Command", Values: []string{svc.Command}})
		}
		if len(svc.Args) > 0 {
			rows = append(rows, defRow{Label: "Arguments", Values: svc.Args})
		}
		var ports []string
		for _, p := range svc.Ports {
			ports = append(ports, p.Name+" → "+strconv.Itoa(p.ContainerPort))
		}
		rows = append(rows, defRow{Label: "Ports", Values: ports})
		var domains []string
		for _, d := range svc.Domains {
			domains = append(domains, d.Host)
		}
		rows = append(rows, defRow{Label: "Domains", Values: domains})
		if svc.Replicas != nil {
			rows = append(rows, defRow{Label: "Copies", Values: []string{strconv.Itoa(*svc.Replicas)}})
		}
		if h := svc.Health; h != nil {
			check := "TCP"
			if h.Path != "" {
				check = h.Path
			}
			if h.Interval > 0 {
				check += ", every " + secondsText(h.Interval) + " s"
			}
			if h.Timeout > 0 {
				check += ", timeout " + secondsText(h.Timeout) + " s"
			}
			rows = append(rows, defRow{Label: "Health check", Values: []string{check}})
		}
		if r := svc.Resources; r.CPUMHz > 0 {
			rows = append(rows, defRow{Label: "CPU", Values: []string{strconv.Itoa(r.CPUMHz) + " MHz"}})
		}
		var memory []string
		if r := svc.Resources; r.MemoryMB > 0 {
			memory = append(memory, strconv.Itoa(r.MemoryMB)+" MB reserved")
		}
		if r := svc.Resources; r.MemoryMaxMB > 0 {
			memory = append(memory, "up to "+strconv.Itoa(r.MemoryMaxMB)+" MB")
		}
		rows = append(rows, defRow{Label: "Memory", Values: memory})
		var volumes []string
		for _, v := range svc.Volumes {
			text := v.Name + " → " + v.Path
			if v.ReadOnly {
				text += " (read-only)"
			}
			volumes = append(volumes, text)
		}
		rows = append(rows, defRow{Label: "Volumes", Values: volumes})
		if b := svc.Backup; b != nil {
			rows = append(rows, defRow{Label: "Backup", Values: []string{
				fmt.Sprintf("%s, %s (UTC), keeping the last %d", b.Engine, b.ScheduleOrDefault(), b.KeepOrDefault())}})
		}
		out = append(out, defPanelOf(svc.Name, rows))
	}
	return out
}

// whatGoes is the sentence that says what deleting the app removes (research
// R16): its copies stop, its addresses stop answering, its history and its
// webhook token are erased.
func whatGoes(a app.App, sum service.AppSummary, deployments int, hasToken bool) sentence {
	copies := sum.Status.WantedCopies
	verb := "stop"
	if copies == 1 {
		verb = "stops"
	}
	s := sentence{plain("Its " + copyCount(copies) + " " + verb)}
	var hosts []string
	for _, svc := range a.Services {
		for _, d := range svc.Domains {
			hosts = append(hosts, d.Host)
		}
	}
	if len(hosts) > 0 {
		s = append(s, plain(" and "))
		for i, h := range hosts {
			switch {
			case i == 0:
			case i == len(hosts)-1:
				s = append(s, plain(" and "))
			default:
				s = append(s, plain(", "))
			}
			s = append(s, mono(h))
		}
		if len(hosts) == 1 {
			s = append(s, plain(" stops answering."))
		} else {
			s = append(s, plain(" stop answering."))
		}
	} else {
		s = append(s, plain("."))
	}

	var erased []string
	if deployments > 0 {
		erased = append(erased, plural(deployments, "deployment"))
	}
	if hasToken {
		erased = append(erased, "webhook token")
	}
	if len(erased) > 0 {
		be := "are"
		if len(erased) == 1 && (hasToken || deployments == 1) {
			be = "is"
		}
		s = append(s, plain(" Its "+strings.Join(erased, " and its ")+" "+be+" erased."))
	}

	var volumes []string
	for _, svc := range a.Services {
		for _, v := range svc.Volumes {
			volumes = append(volumes, v.Name)
		}
	}
	if len(volumes) > 0 {
		word, them := "volume", "it"
		if len(volumes) > 1 {
			word, them = "volumes", "them"
		}
		s = append(s, plain(" Its "+word+" "))
		for i, v := range volumes {
			switch {
			case i == 0:
			case i == len(volumes)-1:
				s = append(s, plain(" and "))
			default:
				s = append(s, plain(", "))
			}
			s = append(s, mono(v))
		}
		s = append(s, plain(" and the data in "+them+" are erased."))
	}
	if slices.ContainsFunc(a.Services, func(svc app.Service) bool { return svc.Backup != nil }) {
		s = append(s, plain(" Its backups stay in the backup destination."))
	}
	return append(s, plain(" This can't be undone."))
}

// maxEditBytes bounds the edit form: environment lists are long, but not this long.
const maxEditBytes = 256 << 10

// savedNote is what a refused deploy adds when the edit was already saved:
// the stored app has the new values, so the next save starts from them.
const savedNote = "The changes are saved; the failed attempt is on the Deployments tab."

// editedThroughFileNotice is the refusal of a per-field save on an app that
// keeps its Compose file (feature 015).
const editedThroughFileNotice = "This app is edited through its Compose file. Change the file above and deploy it."

// saveConfigure answers POST /apps/{id}/configure: it lays the posted form
// over the stored app (applyEdit) and saves and deploys it in one step, on
// the condition that the app still has the revision the form was drawn from.
// Success goes to the deployment's entry on the Deployments tab (a refusal
// draws the form again with everything typed, the message at its input).
func (s *server) saveConfigure(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, maxEditBytes)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.message(w, http.StatusRequestEntityTooLarge, "Too large", "That form is larger than 256 KiB.", "Shorten the environment lists and try again.")
			return
		}
		s.message(w, http.StatusBadRequest, "Bad request", "That form couldn't be read.", "Go back and try again.")
		return
	}
	// A form without its revision would save over whatever is there.
	revision := strings.TrimSpace(r.PostForm.Get("revision"))
	if revision == "" {
		s.message(w, http.StatusBadRequest, "Bad request", "That form has no revision.", "Open the Configure tab again and save from there.")
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
	// An app that keeps its Compose file is edited through the file. The page
	// has no form to post here, so this is a hand-made post: saving fields
	// would deploy a definition the file doesn't say, and drop the file.
	if rec.Compose != nil {
		s.configurePage(w, r, http.StatusConflict, id, configureExtras{Notice: sentence{plain(editedThroughFileNotice)}})
		return
	}

	form, ok := parseEditForm(r.PostForm, rec.App)
	if !ok {
		form.markFirstError()
		s.drawEdit(w, r, http.StatusUnprocessableEntity, rec.App, form, nil)
		return
	}
	edited := applyEdit(rec.App, form)
	deployment, err := s.svc.DeployIf(ctx, edited, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		to := "/apps/" + id + "/deployments#deployment-" + deployment.Deployment.ID
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", to)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}

	// Whatever happened, draw the form from the app as it is stored now: a
	// deploy that failed after saving, or a stale save, both left a newer one.
	fresh, getErr := s.svc.GetApp(ctx, id)
	switch {
	case errors.Is(getErr, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case getErr != nil:
		s.serverError(w, "read app", getErr)
		return
	}
	if errors.Is(err, service.ErrAppChanged) {
		form, _ = parseEditForm(r.PostForm, fresh.App)
		form.Revision = fresh.Revision
		s.drawEdit(w, r, http.StatusConflict, fresh.App, form, staleOf(fresh.App, form))
		return
	}
	form, _ = parseEditForm(r.PostForm, fresh.App)
	form.Revision = fresh.Revision
	status, ok := refuseEdit(&form, edited, err)
	if !ok {
		s.serverError(w, "save and deploy from the console", err)
		return
	}
	form.markFirstError()
	s.drawEdit(w, r, status, fresh.App, form, nil)
}

// drawEdit answers a refused save: the whole Configure tab, or, for an htmx
// post, the form alone to swap in place.
func (s *server) drawEdit(w http.ResponseWriter, r *http.Request, status int, stored app.App, form editForm, stale *staleView) {
	if r.Header.Get("HX-Request") != "true" {
		s.configurePage(w, r, status, stored.ID, configureExtras{Form: &form, Stale: stale})
		return
	}
	var buf bytes.Buffer
	if err := s.pages["app"].ExecuteTemplate(&buf, "configure-form", editViewOf(stored, form, stale)); err != nil {
		s.serverError(w, "render edit form", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// refuseEdit says what a failed save looks like on the form: the message on
// the input it is about, or a sentence for the form as a whole, and the HTTP
// status. edited is the app that was sent, whose service order its field paths
// refer to. ok is false for errors the form has no words for.
func refuseEdit(f *editForm, edited app.App, err error) (status int, ok bool) {
	var fieldErr *app.FieldError
	var imageErr *deploy.ImageError
	var deployErr *service.DeployError

	// onInput puts a message on the input a model path belongs to.
	onInput := func(path, message string) bool {
		name, found := fieldFor(path, edited)
		if !found {
			return false
		}
		field, found := f.field(name)
		if !found {
			return false
		}
		field.Error = sentence{plain(message)}
		return true
	}
	// notice is a sentence for the form; a deploy that failed after the app
	// was saved says so, since the stored app has the changes.
	notice := func(parts ...part) {
		f.Notice = sentence(parts)
		switch {
		case deployErr != nil && len(parts) > 0:
			f.Notice = append(f.Notice, plain(" "+savedNote))
		case deployErr != nil:
			f.Notice = sentence{plain(savedNote)}
		}
	}
	_ = errors.As(err, &deployErr)

	switch {
	case errors.As(err, &fieldErr):
		if !onInput(fieldErr.Field, fieldMessage(fieldErr.Field, fieldErr.Message)) {
			notice(plain("caravana can't deploy this app: "), mono(fieldErr.Field), plain(" "+fieldErr.Message))
		} else if deployErr != nil {
			notice()
		}
		return http.StatusUnprocessableEntity, true
	case errors.As(err, &imageErr):
		status = http.StatusUnprocessableEntity
		if !errors.Is(err, deploy.ErrImageNotFound) {
			status = http.StatusBadGateway // the registry couldn't answer: not the typed value's fault
		}
		if onInput(imageErr.Field, imageErr.Error()) {
			notice()
		} else {
			notice(plain(imageErr.Error()))
		}
		return status, true
	case errors.Is(err, deploy.ErrNomadUnavailable):
		notice(plain("Nomad can't be reached, so nothing was deployed."))
		return http.StatusBadGateway, true
	case errors.Is(err, secrets.ErrUnavailable):
		notice(plain("The secret store refused the change, so nothing was deployed. The running copies keep their secrets."))
		return http.StatusBadGateway, true
	}
	// A job Nomad rejected, a job Caravana didn't create: the sentences of the Deploy form.
	status, sentenceOf, ok := refuseDeploy(&deployForm{}, err)
	if !ok {
		return 0, false
	}
	notice(sentenceOf...)
	return status, true
}
