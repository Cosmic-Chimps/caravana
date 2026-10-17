package ui

import (
	"bytes"
	"context"
	"errors"
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

// This file is the Compose forms' pages and handlers (feature 014, research
// R11): New app › Compose file and Deploy from Compose are the same two
// steps. Preview shows what the file becomes and asks for what Compose has no
// words for; the second button creates or deploys. Both steps are plain
// posts; htmx posts them in place.

// maxComposeFile is the largest file the form takes, the API's body limit.
// The form also carries the overlay, so its body may be a little larger.
const (
	maxComposeFile = 1 << 20
	maxComposeForm = maxComposeFile + 64<<10
)

// composeNote is a warning or a refusal: the Compose path it is about and
// what is wrong.
type composeNote struct {
	Path, Message string
}

// composeView is a Compose form page: New app › Compose file, or Deploy from
// Compose of an existing app. Draft holds the inputs as typed, never with
// secret values.
type composeView struct {
	Existing                    bool
	AppID                       string // Existing only
	PreviewAction, DeployAction string
	// Place is the project and environment of a new app (feature 019); drawCompose
	// lists its options.
	Place placeChoice
	Draft composeDraft
	// Kept says the draft was filled from the Compose file the app keeps
	// (feature 015), not typed: its services are drawn before any preview.
	Kept bool
	// NotKept says the app was made from Compose before the file was kept: the
	// file is empty and the overlay is filled from the app, so Configure can ask
	// for the file once (feature 015, US2).
	NotKept bool

	// Notice is an error that isn't about one input; SavedID is set when the
	// app was saved but its deploy didn't start; Stale says the app changed
	// since the form was drawn; SecretsNote says typed values were dropped.
	Notice      sentence
	SavedID     string
	Stale       bool
	SecretsNote bool

	// Previewed is set when the file translated: Services then has the file's
	// services with their facts, Warnings what changed in meaning and, for an
	// existing app, Changes the services added and removed.
	Previewed bool
	Services  []composeServiceView
	Changes   []sentence
	Warnings  []composeNote
	Refusals  []composeNote
}

// CanDeploy is whether the page offers the second button: the file
// translated and nothing is refused, or the form was filled from the app
// (Kept, NotKept), so its services are already drawn and the owner edits them
// in place. A deploy previews the file first anyway and comes back with the
// refusals instead of deploying, so skipping Preview is safe; without the
// button, the nearest "Deploy" was the top bar's link, which leaves the page.
func (v composeView) CanDeploy() bool {
	return (v.Previewed || v.Kept || v.NotKept) && len(v.Refusals) == 0
}

// FileRows is the height of the file's textarea: a row for each line and one
// to spare, from 12 rows (an empty form) to 40 (a long file scrolls).
func (v composeView) FileRows() int {
	return min(max(strings.Count(v.Draft.File.Value, "\n")+2, 12), 40)
}

// composeServiceView is one service's panel: the facts the preview found
// (none when the file didn't translate) and the overlay inputs. The Show
// flags say which inputs the service has.
type composeServiceView struct {
	draftService
	Facts                                               []defRow
	ShowDomains, ShowHealth, ShowHealthPort, ShowBackup bool
}

// composeSpecs are how the overlay's text inputs are drawn, by their posted
// field name.
var composeSpecs = map[string]inputSpec{
	"domains": {Name: "domains", Label: "Domains", Area: true, Wide: true,
		Hint: "One host per line. Add :port when the service has several ports. HTTPS is set up for each."},
	"healthPath":     {Name: "healthPath", Label: "Health check", Placeholder: "TCP", Hint: "A path such as /healthz. Empty checks the TCP port."},
	"healthInterval": {Name: "healthInterval", Numeric: true, Label: "Check every (seconds)", Placeholder: "10"},
	"healthTimeout":  {Name: "healthTimeout", Numeric: true, Label: "Check timeout (seconds)", Placeholder: "2"},
	"healthPort":     {Name: "healthPort", Numeric: true, Label: "Check port", Hint: "The container port to check."},
	"backupSchedule": {Name: "backupSchedule", Label: "Backup schedule (UTC)", Placeholder: "0 3 * * *", Hint: "A cron expression with five fields."},
	"backupKeep":     {Name: "backupKeep", Numeric: true, Label: "Backups to keep", Placeholder: "7"},
}

// Inputs lists the plain inputs the service has, in the order they show.
func (s composeServiceView) Inputs() []editInput {
	byName := map[string]*formField{}
	for _, in := range s.inputs() {
		byName[in.name] = in.field
	}
	var names []string
	if s.ShowDomains {
		names = append(names, "domains")
	}
	if s.ShowHealth {
		names = append(names, "healthPath", "healthInterval", "healthTimeout")
	}
	if s.ShowHealthPort {
		names = append(names, "healthPort")
	}
	var out []editInput
	for _, name := range names {
		out = append(out, editInput{inputSpec: composeSpecs[name], Field: byName[name]})
	}
	return out
}

// BackupInputs are the schedule and keep inputs, which follow the engine's.
func (s composeServiceView) BackupInputs() []editInput {
	return []editInput{
		{inputSpec: composeSpecs["backupSchedule"], Field: &s.BackupSchedule},
		{inputSpec: composeSpecs["backupKeep"], Field: &s.BackupKeep},
	}
}

// serviceViews lays out the draft's services for the page, with the facts of
// the previewed app a when there is one. A service of a that the draft lacks
// is not shown; ensureServices adds it first.
func serviceViews(d composeDraft, a *app.App) []composeServiceView {
	var out []composeServiceView
	for _, ds := range d.Services {
		v := composeServiceView{draftService: ds, ShowDomains: true, ShowHealth: true,
			ShowHealthPort: ds.HealthPort.Value != "", ShowBackup: ds.hasBackupInput()}
		if a != nil {
			for _, svc := range a.Services {
				if svc.Name != ds.Name {
					continue
				}
				v.Facts = composeFacts(svc)
				v.ShowDomains, v.ShowHealth = len(svc.Ports) > 0, len(svc.Ports) > 0
				v.ShowHealthPort = len(svc.Ports) > 1
				// A database image, or a service with a volume (the volume engine's
				// only requirement), can be given a backup.
				v.ShowBackup = ds.hasBackupInput() || strings.Contains(strings.ToLower(svc.Image), "postgres") || len(svc.Volumes) > 0
			}
		}
		out = append(out, v)
	}
	return out
}

// hasBackupInput says the service has something in its backup inputs, or an
// error on them, so they must be drawn whatever the image is.
func (s draftService) hasBackupInput() bool {
	for _, f := range []formField{s.BackupEngine, s.BackupSchedule, s.BackupKeep} {
		if f.Value != "" || len(f.Error) > 0 {
			return true
		}
	}
	return false
}

// composeFacts are what the preview says of a service: its image, ports,
// volumes, memory and copies.
func composeFacts(svc app.Service) []defRow {
	rows := []defRow{{Label: "Image", Values: []string{svc.Image}}}
	var ports []string
	for _, p := range svc.Ports {
		ports = append(ports, p.Name+" → "+strconv.Itoa(p.ContainerPort))
	}
	rows = append(rows, defRow{Label: "Ports", Values: ports})
	var volumes []string
	for _, v := range svc.Volumes {
		text := v.Name + " → " + v.Path
		if v.ReadOnly {
			text += " (read-only)"
		}
		volumes = append(volumes, text)
	}
	rows = append(rows, defRow{Label: "Volumes", Values: volumes})
	var memory []string
	if r := svc.Resources; r.MemoryMB > 0 {
		memory = append(memory, strconv.Itoa(r.MemoryMB)+" MB reserved")
		if r.MemoryMaxMB > 0 {
			memory = append(memory, "up to "+strconv.Itoa(r.MemoryMaxMB)+" MB")
		}
	} else if r.MemoryMaxMB > 0 {
		memory = append(memory, "up to "+strconv.Itoa(r.MemoryMaxMB)+" MB")
	}
	rows = append(rows, defRow{Label: "Memory", Values: memory})
	var copies []string
	if svc.Replicas != nil {
		copies = append(copies, strconv.Itoa(*svc.Replicas))
	}
	rows = append(rows, defRow{Label: "Copies", Values: copies})
	return rows
}

// ensureServices adds an empty overlay for each service of the previewed app
// that the draft has none for: a service the file just introduced. Sorting
// keeps them in the file's order.
func (d *composeDraft) ensureServices(a app.App) {
	for _, svc := range a.Services {
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
}

// dropService removes the overlay of a service the file doesn't have.
func (d *composeDraft) dropService(name string) bool {
	i := slices.IndexFunc(d.Services, func(s draftService) bool { return s.Name == name })
	if i < 0 {
		return false
	}
	d.Services = slices.Delete(d.Services, i, i+1)
	return true
}

// composeChanges says which services the file adds to the stored app and
// which it removes.
func composeChanges(stored, previewed app.App) []sentence {
	has := func(a app.App, name string) bool {
		return slices.ContainsFunc(a.Services, func(s app.Service) bool { return s.Name == name })
	}
	var out []sentence
	for _, svc := range previewed.Services {
		if !has(stored, svc.Name) {
			out = append(out, sentence{plain("Adds service "), mono(svc.Name), plain(".")})
		}
	}
	for _, svc := range stored.Services {
		if !has(previewed, svc.Name) {
			out = append(out, sentence{plain("Removes service "), mono(svc.Name), plain(": its copies stop.")})
		}
	}
	return out
}

// fieldFor is the input an error about a Compose path belongs to (the
// request's overlay, as "services.web.domains[0].port" in the file's terms),
// with the words that start its sentence. It is nil for a path the form has no
// input for: the file's own attributes, which go to the list of refusals.
func (d *composeDraft) fieldFor(path string) (field *formField, subject string) {
	switch {
	case path == "id":
		return &d.Name, "The name"
	case path == "compose":
		return &d.File, "The file"
	}
	rest, ok := strings.CutPrefix(path, "services.")
	if !ok {
		return nil, ""
	}
	name, tail, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, ""
	}
	s, ok := d.service(name)
	if !ok {
		return nil, ""
	}
	switch {
	case hasPathPrefix(tail, "domains"):
		return &s.Domains, "The domain"
	case tail == "health.port":
		return &s.HealthPort, "The check port"
	case tail == "health.interval":
		return &s.HealthInterval, "The check interval"
	case tail == "health.timeout":
		return &s.HealthTimeout, "The check timeout"
	case hasPathPrefix(tail, "health"):
		return &s.HealthPath, "The health check"
	case tail == "backup.schedule":
		return &s.BackupSchedule, "The schedule"
	case tail == "backup.keep":
		return &s.BackupKeep, "The number to keep"
	case hasPathPrefix(tail, "backup"):
		return &s.BackupEngine, "The backup"
	case strings.HasPrefix(tail, "secrets["):
		num, _, _ := strings.Cut(strings.TrimPrefix(tail, "secrets["), "]")
		if j, err := strconv.Atoi(num); err == nil && j >= 0 && j < len(s.Secrets) {
			return &s.Secrets[j].Field, "The secret"
		}
	}
	return nil, ""
}

// onField is a model message as a sentence about its input. Messages that are
// a predicate ("is required") take the subject; the others already say it.
func onField(subject, message string) sentence {
	first, _, _ := strings.Cut(message, " ")
	switch first {
	case "is", "isn't", "must", "may", "has", "needs", "should":
		return sentence{plain(subject + " " + message)}
	}
	text := strings.ToUpper(message[:1]) + message[1:]
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return sentence{plain(text)}
}

// refuse puts a field error on its input, or, for a path of the file itself
// ("services.web.build"), in the list of refusals.
func (v *composeView) refuse(d *composeDraft, fe *app.FieldError) {
	if f, subject := d.fieldFor(fe.Field); f != nil {
		f.Error = onField(subject, fe.Message)
		if strings.HasSuffix(fe.Field, ".backup.engine") && fe.Message == app.NoVolumeToBackUp {
			// The model's message names no subject; the form says which service.
			f.Error = sentence{plain("This service has no volume to back up.")}
		}
		return
	}
	v.Refusals = append(v.Refusals, composeNote{Path: fe.Field, Message: fe.Message})
}

// ---- pages ---------------------------------------------------------------

// newComposeView is a view with its actions set.
func newComposeView(appID string) composeView {
	v := composeView{Existing: appID != "", AppID: appID,
		PreviewAction: "/apps/new/compose/preview", DeployAction: "/apps/new/compose"}
	if v.Existing {
		v.PreviewAction, v.DeployAction = "/apps/"+appID+"/compose/preview", "/apps/"+appID+"/compose"
	}
	v.Draft = composeDraft{Name: formField{ID: "name"}, File: formField{ID: "compose"}}
	return v
}

// keptComposeView is the Compose form of an app that keeps its file (feature
// 015, research R6, R7): the file as it was deployed, the overlay it had, and
// the stored app's secrets as names with empty inputs, with the revision of the
// app the form was drawn from. Its services are drawn at once, with the facts of
// the stored app, so the owner can edit them before a preview. rec.Compose must
// not be nil.
func keptComposeView(rec store.AppRecord) composeView {
	v := newComposeView(rec.App.ID)
	v.Kept = true
	v.Draft = draftOf(*rec.Compose)
	v.Draft.Revision = rec.Revision
	v.Draft.keepStored(rec.App)
	v.Draft.ensureServices(rec.App)
	v.Services = serviceViews(v.Draft, &rec.App)
	return v
}

// notKeptComposeView is the Compose form of an app made from Compose before
// the file was kept (feature 015, US2, research R6): an empty file to paste
// once, the overlay as the stored app has it (draftFrom), and the app's
// revision. Its services are drawn at once, as the kept view's are, so the
// overlay can be seen and edited with the file.
func notKeptComposeView(rec store.AppRecord) composeView {
	v := newComposeView(rec.App.ID)
	v.NotKept = true
	v.Draft = draftFrom(rec.App)
	v.Draft.File = formField{ID: "compose"}
	v.Draft.Revision = rec.Revision
	v.Services = serviceViews(v.Draft, &rec.App)
	return v
}

// drawCompose answers with the Compose form: the whole page, or, for an htmx
// post, the form alone to swap in place. Whatever the draft held, the page
// gets no secret value.
func (s *server) drawCompose(w http.ResponseWriter, r *http.Request, status int, v composeView) {
	v.Draft = v.Draft.forPage()
	if !v.Existing {
		pl, err := s.loadPlaces(r.Context())
		if err != nil {
			s.serverError(w, "list projects", err)
			return
		}
		refusal := v.Place.Error
		v.Place = pl.choice(v.Place.Value)
		v.Place.Error = refusal
	}
	if r.Header.Get("HX-Request") == "true" {
		page := "newapp"
		if v.Existing {
			page = "app"
		}
		var buf bytes.Buffer
		if err := s.pages[page].ExecuteTemplate(&buf, "compose-form", v); err != nil {
			s.serverError(w, "render compose form", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write(buf.Bytes())
		return
	}
	if v.Existing {
		s.composeAppPage(w, r, status, v)
		return
	}
	head, err := s.shellPage(r, "New app", "apps", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: "New app"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, status, "newapp", newAppPage{pageData: head, Compose: &v})
}

// composeAppPage draws Deploy from Compose under the app's Configure tab. An
// unknown app is the not-found page.
func (s *server) composeAppPage(w http.ResponseWriter, r *http.Request, status int, v composeView) {
	id := v.AppID
	sum, err := s.svc.AppSummary(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	head, err := s.shellPage(r, id+" compose", "apps", crumb{Label: "Apps", Href: "/apps"},
		crumb{Label: id, Href: "/apps/" + id, Mono: true}, crumb{Label: "Configure", Href: "/apps/" + id + "/configure"},
		crumb{Label: "Deploy from Compose"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(r.Context(), sum, "configure"), Compose: &v})
}

// ---- steps ---------------------------------------------------------------

// previewNewCompose answers POST /apps/new/compose/preview.
func (s *server) previewNewCompose(w http.ResponseWriter, r *http.Request, session string) {
	s.composeStep(w, r, "", false)
}

// createNewCompose answers POST /apps/new/compose.
func (s *server) createNewCompose(w http.ResponseWriter, r *http.Request, session string) {
	s.composeStep(w, r, "", true)
}

// composeStep is the one handler behind the four posts: appID is the existing
// app, or "" for a new one (the name is in the form); deploy says the second
// button was pressed. The service translates the file, so the preview and the
// deploy refuse the same way; a deploy previews first only to have the app
// whose services the errors' paths refer to.
func (s *server) composeStep(w http.ResponseWriter, r *http.Request, appID string, deploy bool) {
	ctx := r.Context()
	v := newComposeView(appID)

	var stored *store.AppRecord
	if v.Existing {
		rec, err := s.svc.GetApp(ctx, appID)
		if errors.Is(err, store.ErrNotFound) {
			s.appNotFound(w, r, appID)
			return
		}
		if err != nil {
			s.serverError(w, "read app", err)
			return
		}
		stored = &rec
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxComposeForm)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if !errors.As(err, &tooBig) {
			s.message(w, http.StatusBadRequest, "Bad request", "That form couldn't be read.", "Go back and try again.")
			return
		}
		// Nothing of it was read: the form comes back empty, with the app's revision as it is now.
		v.Draft.File.Error, v.Draft.File.Alert = sentence{plain("The file is larger than 1 MiB.")}, true
		if stored != nil {
			v.Draft.Revision = stored.Revision
		}
		s.drawCompose(w, r, http.StatusRequestEntityTooLarge, v)
		return
	}

	draft, ok := parseComposeDraft(r.PostForm)
	status := http.StatusUnprocessableEntity
	if stored != nil {
		if draft.Revision == "" {
			if deploy {
				s.message(w, http.StatusBadRequest, "Bad request", "That form has no revision.", "Open Deploy from Compose again and deploy from there.")
				return
			}
			draft.Revision = stored.Revision
		}
		// A post with no overlay is the first look at a file: start from what the app has.
		if len(draft.Services) == 0 {
			draft.Services = draftFrom(stored.App).Services
		}
		draft.keepStored(stored.App)
	}

	id := appID
	if !v.Existing {
		v.Place.Value = strings.TrimSpace(r.PostForm.Get("place"))
		pl, err := s.loadPlaces(ctx)
		if err != nil {
			s.serverError(w, "list projects", err)
			return
		}
		place, refusal, placed := pl.placeFor(v.Place.Value, draft.Name.Value)
		if !placed {
			v.Place.Error, ok = refusal, false
		}
		// The name as typed, not yet checked: an empty one has no ID to look up.
		id = draft.Name.Value
		if id != "" {
			id = place.ID()
		}
		switch _, err := s.svc.GetApp(ctx, id); {
		case id == "":
			draft.Name.Error, ok = sentence{plain("Enter a name for the app.")}, false
		case err == nil:
			draft.Name.Error, ok, status = takenSentence(id), false, http.StatusConflict
		case !errors.Is(err, store.ErrNotFound):
			s.serverError(w, "read app", err)
			return
		}
	}
	if strings.TrimSpace(draft.File.Value) == "" {
		draft.File.Error, ok = sentence{plain("Paste the Compose file.")}, false
	}
	v.SecretsNote = len(draft.values) > 0
	if !ok {
		s.drawComposeRefused(w, r, status, v, draft)
		return
	}

	displayName := ""
	if stored != nil {
		displayName = stored.App.Name
	}
	prev, warnings, err := s.previewDraft(ctx, id, &draft, displayName)
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr):
		v.refuse(&draft, fieldErr)
		s.drawComposeRefused(w, r, http.StatusUnprocessableEntity, v, draft)
		return
	case err != nil:
		s.serverError(w, "preview compose", err)
		return
	}
	draft.ensureServices(prev)
	v.Previewed = true
	for _, warning := range warnings {
		v.Warnings = append(v.Warnings, composeNote{Path: warning.Field, Message: warning.Message})
	}
	if stored != nil {
		v.Changes = composeChanges(stored.App, prev)
	}
	v.Services = serviceViews(draft, &prev)
	if !deploy {
		v.Draft = draft
		s.drawCompose(w, r, http.StatusOK, v)
		return
	}

	cond := service.Condition{IfNew: true}
	if v.Existing {
		cond = service.Condition{IfRevision: draft.Revision}
	}
	req := draft.request()
	req.Name = displayName
	rec, err := s.svc.DeployComposeIf(ctx, id, req, cond, app.TriggerConsole)
	if err == nil {
		to := "/apps/" + id + "/deployments#deployment-" + rec.Deployment.ID
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", to)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}

	// Whatever happened, draw the form from the app as it is stored now: a
	// deploy that failed after saving, or a stale deploy, both left a newer one.
	if v.Existing {
		fresh, getErr := s.svc.GetApp(ctx, id)
		switch {
		case errors.Is(getErr, store.ErrNotFound):
			s.appNotFound(w, r, id)
			return
		case getErr != nil:
			s.serverError(w, "read app", getErr)
			return
		}
		draft.Revision = fresh.Revision
		draft.keepStored(fresh.App)
		v.Changes = composeChanges(fresh.App, prev)
	}
	v.SecretsNote = slices.ContainsFunc(draft.Services, func(s draftService) bool { return len(s.Secrets) > 0 })
	status, ok = v.refuseFailure(&draft, prev, id, err)
	if !ok {
		s.serverError(w, "deploy compose from the console", err)
		return
	}
	draft.markFirstError()
	v.Services = serviceViews(draft, &prev)
	v.Draft = draft
	s.drawCompose(w, r, status, v)
}

// drawComposeRefused draws the form after something in it was refused: the
// first error is announced and no second button is offered.
func (s *server) drawComposeRefused(w http.ResponseWriter, r *http.Request, status int, v composeView, draft composeDraft) {
	draft.markFirstError()
	v.Services = serviceViews(draft, nil)
	v.Draft = draft
	s.drawCompose(w, r, status, v)
}

// previewDraft translates the draft's file. An overlay for a service the file
// doesn't have (it was removed from the file, or the app had it before) is
// dropped, one at a time, so the file is judged by its own services.
func (s *server) previewDraft(ctx context.Context, id string, d *composeDraft, displayName string) (app.App, []app.Warning, error) {
	for {
		req := d.request()
		req.Name = displayName
		a, warnings, err := s.svc.PreviewCompose(ctx, id, req)
		var fe *app.FieldError
		if errors.As(err, &fe) && strings.HasPrefix(fe.Message, "the file has no service") {
			if name, ok := strings.CutPrefix(fe.Field, "services."); ok && d.dropService(name) {
				continue
			}
		}
		return a, warnings, err
	}
}

// refuseFailure says what a failed deploy looks like on the form: errors on
// the inputs they are about, a list entry for the file's own attributes, or a
// sentence for the form, and the HTTP status. prev is the previewed app, whose
// service order the model's paths refer to. ok is false for errors the form
// has no words for.
func (v *composeView) refuseFailure(d *composeDraft, prev app.App, id string, err error) (status int, ok bool) {
	var fieldErr *app.FieldError
	var imageErr *deploy.ImageError
	var deployErr *service.DeployError
	_ = errors.As(err, &deployErr)
	if deployErr != nil {
		v.SavedID = id
	}
	notice := func(parts ...part) { v.Notice = sentence(parts) }

	switch {
	case errors.Is(err, service.ErrAppExists):
		d.Name.Error = takenSentence(id)
		return http.StatusConflict, true
	case errors.Is(err, service.ErrProjectNotFound), errors.Is(err, service.ErrEnvironmentNotFound):
		// The environment was deleted after the form was drawn.
		v.Place.Error = sentence{plain("That environment doesn't exist any more. Choose another.")}
		return http.StatusUnprocessableEntity, true
	case errors.Is(err, service.ErrAppChanged):
		v.Stale = true
		return http.StatusConflict, true
	case errors.As(err, &fieldErr):
		v.refuse(d, fieldErr)
		return http.StatusUnprocessableEntity, true
	case errors.As(err, &imageErr):
		status = http.StatusUnprocessableEntity
		if !errors.Is(err, deploy.ErrImageNotFound) && !errors.Is(err, deploy.ErrRegistryAuth) {
			status = http.StatusBadGateway // the registry couldn't answer: not the file's fault
		}
		path := imageErr.Field
		if i := strings.Index(path, "]"); strings.HasPrefix(path, "services[") {
			if n, convErr := strconv.Atoi(path[len("services["):i]); convErr == nil && n < len(prev.Services) {
				path = "services." + prev.Services[n].Name + path[i+1:]
			}
		}
		v.Refusals = append(v.Refusals, composeNote{Path: path, Message: imageErr.Error()})
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
	v.Notice = sentenceOf
	return status, true
}
