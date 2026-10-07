package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// tabSpec is one tab of the app page. Path is appended to /apps/{id}; the
// Running tab's is "".
type tabSpec struct {
	Key   string // running, deployments, configure or logs
	Label string
	Path  string
}

// builtTabs are the tabs whose pages exist, in the order they show. Each
// story that builds a tab adds it here (Running, Deployments, Configure,
// Logs); the Running tab is the app's own page, so it comes first.
var builtTabs = []tabSpec{
	{Key: "running", Label: "Running", Path: ""},
	{Key: "deployments", Label: "Deployments", Path: "/deployments"},
	{Key: "configure", Label: "Configure", Path: "/configure"},
	{Key: "logs", Label: "Logs", Path: "/logs"},
}

// tabLink is a tab as the header draws it.
type tabLink struct {
	Label   string
	Href    string
	Count   string // a number or version after the label; "" for none
	Current bool
}

// appHeader is what the "app" region draws: the app's name, status,
// addresses and summary and its tabs. Gone and Missing replace all that with
// one sentence and a way back.
type appHeader struct {
	ID         string
	Status     statusView
	Domains    []domainLink
	Summary    sentence
	NomadError string // why the status is unknown, as Nomad said it; mono detail
	Tabs       []tabLink
	DeployHref string   // the top bar's Deploy action; "" until the Deployments tab exists, and on that tab
	HeaderHref string   // where the header region refreshes from; it keeps the tab, so the right one stays marked
	Gone       sentence // "blog was deleted." / "There's no app called x."
}

// appPage is the whole app page: the header, and the body of the current tab
// (each tab's data is a pointer that is nil on the other tabs).
type appPage struct {
	pageData
	appHeader
	Running     *copiesRegion
	Deployments *deploymentsTab
	Configure   *configureTab
	Logs        *logsTab
}

// hasTab says whether the named tab's page exists yet.
func (s *server) hasTab(key string) bool {
	return slices.ContainsFunc(s.tabs, func(t tabSpec) bool { return t.Key == key })
}

// deploymentsHref is where an entry about a deployment of the app leads: the
// Deployments tab, or the app's page while that tab isn't built.
func (s *server) deploymentsHref(appID string) string {
	if s.hasTab("deployments") {
		return "/apps/" + appID + "/deployments"
	}
	return "/apps/" + appID
}

// tabCount is the number or version a tab carries after its label.
func tabCount(key string, sum service.AppSummary) string {
	switch key {
	case "running":
		return strconv.Itoa(sum.Status.HealthyCopies)
	case "deployments":
		return versionLabel(sum.Status.LiveVersion)
	}
	return ""
}

// header builds the app's header from its summary. current is the key of the
// tab being shown.
func (s *server) header(sum service.AppSummary, current string) appHeader {
	h := appHeader{
		ID:         sum.App.ID,
		Status:     appStatusView(sum),
		Domains:    domainLinks(sum.App),
		Summary:    summaryOf(sum, s.now()),
		NomadError: sum.Status.NomadError,
	}
	for _, t := range s.tabs {
		h.Tabs = append(h.Tabs, tabLink{
			Label: t.Label, Href: "/apps/" + sum.App.ID + t.Path,
			Count: tabCount(t.Key, sum), Current: t.Key == current,
		})
	}
	// Deploy leads to the Deployments tab's form; on that tab the form is right there.
	if s.hasTab("deployments") && current != "deployments" {
		h.DeployHref = "/apps/" + sum.App.ID + "/deployments"
	}
	h.HeaderHref = "/parts/apps/" + sum.App.ID + "/header"
	if current != "running" {
		h.HeaderHref += "?tab=" + url.QueryEscape(current)
	}
	return h
}

// showApp answers GET /apps/{id}: the app's page. An unknown app is a 404
// page inside the shell.
func (s *server) showApp(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	sum, err := s.svc.AppSummary(r.Context(), id)
	missing := errors.Is(err, store.ErrNotFound)
	if err != nil && !missing {
		s.serverError(w, "read app", err)
		return
	}
	if missing {
		s.appNotFound(w, r, id)
		return
	}
	head, err := s.shellPage(r, id, "apps", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: id, Mono: true})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id
	copies, err := s.loadCopies(r, id)
	if err != nil {
		s.serverError(w, "list copies", err)
		return
	}
	s.render(w, http.StatusOK, "app", appPage{pageData: head, appHeader: s.header(sum, "running"), Running: &copies})
}

// partAppHeader answers GET /parts/apps/{id}/header. A page only asks for it
// while it is open, so an app that isn't there any more was deleted.
func (s *server) partAppHeader(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	sum, err := s.svc.AppSummary(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.renderPart(w, "app", "app-header", appHeader{ID: id, Gone: sentence{mono(id), plain(" was deleted.")}})
	case err != nil:
		s.log.Error("read app", "app", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		s.renderPart(w, "app", "app-header", s.header(sum, s.tabKey(r.URL.Query().Get("tab"))))
	}
}

// tabKey is the tab a header part is drawn for: the ?tab= value if it names a
// tab, else the Running tab, which is the app's own page.
func (s *server) tabKey(key string) string {
	if s.hasTab(key) {
		return key
	}
	return "running"
}

// liveApp answers GET /live/apps/{id}: the stream of one app's page.
// With ?logs=1 (the Logs tab) it also carries the app's log lines.
func (s *server) liveApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	route := liveRoute{AppID: id,
		Kinds: []service.ChangeKind{service.ChangeApp, service.ChangeDeployments, service.ChangeCopies, service.ChangeDeleted}}
	if r.URL.Query().Get("logs") == "1" {
		if choice, ok := logChoiceOf(r.URL.Query()); ok {
			route.Start = s.logStart(id, choice)
		}
	}
	s.stream(w, r, route)
}

// ---- Running tab (/apps/{id}) --------------------------------------------------------

// copyEntry is one copy in the Running table.
type copyEntry struct {
	ID       string // the whole Nomad allocation ID, for the title attribute
	Short    string // its first 8 characters, which the row shows
	Service  string
	Status   statusView
	Version  string // "v7"
	Restarts int
	UpFor    string // "1 min 30 s", "not started", or "not running" for a copy that ended
}

// copiesRegion is what the "copies" region draws: the copies, or one sentence
// on why there are none. Notice replaces everything when Nomad can't be read;
// Gone when the app was deleted. Failure, with the Deployments link, explains
// an empty list after a deploy that didn't work.
type copiesRegion struct {
	Rows            []copyEntry
	Failure         sentence // "v8 failed: see "; the template adds the link and the full stop
	DeploymentsHref string
	Notice          sentence
	RefreshHref     string
	Gone            sentence
}

// loadCopies reads an app's copies and the newest deployment, which says why
// there may be none. An unknown app is Gone, and a Nomad that can't be read is
// a Notice: both are what the region shows, not errors of the page.
func (s *server) loadCopies(r *http.Request, appID string) (copiesRegion, error) {
	ctx := r.Context()
	region := copiesRegion{RefreshHref: "/parts/apps/" + appID + "/copies", DeploymentsHref: s.deploymentsHref(appID)}

	copies, err := s.svc.Copies(ctx, appID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		region.Gone = sentence{mono(appID), plain(" was deleted.")}
		return region, nil
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't read copies from Nomad", "app", appID, "error", err)
		region.Notice = sentence{plain("Nomad can't be reached right now, so copies can't be shown.")}
		return region, nil
	case err != nil:
		return copiesRegion{}, err
	}

	now := s.now()
	for _, c := range copies {
		region.Rows = append(region.Rows, copyEntryOf(now, c))
	}
	if len(copies) == 0 {
		recs, err := s.svc.ListDeployments(ctx, appID, 1)
		if err != nil {
			return copiesRegion{}, err
		}
		if len(recs) > 0 {
			region.Failure = failedDeploySentence(recs[0])
		}
	}
	return region, nil
}

func copyEntryOf(now time.Time, c service.Copy) copyEntry {
	e := copyEntry{ID: c.ID, Short: shortID(c.ID), Service: c.Service, Status: copyStatusView(c.Status),
		Version: versionLabel(&c.Version), Restarts: c.Restarts}
	switch {
	case c.Terminal:
		e.UpFor = "not running"
	case c.StartedAt == nil:
		e.UpFor = "not started"
	default:
		e.UpFor = durationText(now.Sub(*c.StartedAt))
	}
	return e
}

// partCopies answers GET /parts/apps/{id}/copies: the region alone. An app
// that isn't there any more was deleted.
func (s *server) partCopies(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	region, err := s.loadCopies(r, id)
	if err != nil {
		s.log.Error("list copies", "app", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "app", "copies-region", region)
}

// ---- Deployments tab (/apps/{id}/deployments) ----------------------------------------

// deployPageSize is how many deployments the tab shows before "Show older".
const deployPageSize = 50

// deployField is one service's image field in the deploy form.
type deployField struct {
	Service string // "web"
	Label   string // "Image" for a one-service app, "web image" otherwise
	Name    string // the form field: image.web
	ID      string // the element ID: image-web
	Value   string // the stored image, or what was typed when the post was refused
	Error   string // what is wrong with it; "" when nothing is
}

// deployForm is the deploy form: one image field per service. Notice is an
// error that isn't about one field (Nomad is down, say).
type deployForm struct {
	Action string
	Fields []deployField
	Notice sentence
}

// digestView is one service's pinned image in an entry: the short digest, and
// the whole reference for the title attribute. Service is set only when the
// deployment has several.
type digestView struct {
	Service string
	Short   string
	Full    string
}

// historyEntry is one deployment in the history.
type historyEntry struct {
	ID       string
	Version  string // "v7"; "" until Nomad registered the job
	Status   statusView
	Trigger  string // in words: "from the console"
	Started  when
	Timing   string // "took 42 s", "running for 40 s"
	Sentence sentence
	Progress string      // a running deployment's progress line; "" for the others
	Error    string      // the raw error, shown in mono under the sentence
	Action   *actionLink // the one fix a failed newest deployment offers; nil for the rest
	Warnings []app.Warning
	Digests  []digestView
}

// historyRegion is what the "deployments" region draws: the history, newest
// first. OlderHref leads to the next page; RefreshHref is where the region
// reloads itself from, which keeps its page. Gone replaces everything when
// the app was deleted.
type historyRegion struct {
	Entries     []historyEntry
	OlderHref   string
	RefreshHref string
	Empty       emptyView // shown when there are no entries
	Gone        sentence
}

// deploymentsTab is the body of the Deployments tab.
type deploymentsTab struct {
	Form    deployForm
	History historyRegion
}

// errBadCursor is a ?before= that isn't a time.
var errBadCursor = errors.New("bad cursor")

// loadHistory reads one page of an app's deployments: the first when the
// request has no ?before=, else the one after that time. It reads one more
// than it shows, to know whether there is an older page and to give the last
// entry its neighbour.
func (s *server) loadHistory(r *http.Request, appID string) (historyRegion, error) {
	ctx := r.Context()
	limit := s.deployPage
	first := true
	var before time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return historyRegion{}, errBadCursor
		}
		first, before = false, t
	}

	var recs []store.DeploymentRecord
	var err error
	if first {
		recs, err = s.svc.ListDeployments(ctx, appID, limit+1)
	} else {
		recs, err = s.svc.ListDeploymentsBefore(ctx, appID, before, limit+1)
	}
	if err != nil {
		return historyRegion{}, err
	}
	facts := deployFactsOf(recs, first)
	more := len(recs) > limit
	if more {
		recs = recs[:limit]
	}

	region := historyRegion{RefreshHref: "/parts/apps/" + appID + "/deployments",
		Empty: emptyView{Title: "No deployments yet.", Text: "Deploy the app above to start the first one."}}
	if !first {
		region.RefreshHref += "?before=" + url.QueryEscape(before.UTC().Format(time.RFC3339Nano))
	}
	now := s.now()
	for i, rec := range recs {
		region.Entries = append(region.Entries, s.historyEntry(ctx, now, rec, facts[i]))
	}
	if more {
		region.OlderHref = "/apps/" + appID + "/deployments?before=" +
			url.QueryEscape(recs[len(recs)-1].CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	return region, nil
}

func (s *server) historyEntry(ctx context.Context, now time.Time, rec store.DeploymentRecord, f deployFacts) historyEntry {
	e := historyEntry{
		ID:       rec.Deployment.ID,
		Version:  versionLabel(rec.JobVersion),
		Status:   deploymentStatusView(rec.Status),
		Trigger:  triggerWords(rec.Deployment.Trigger),
		Started:  whenOf(now, &rec.CreatedAt, ""),
		Timing:   deploymentTiming(rec, now),
		Sentence: deploymentSentence(rec, f),
		Error:    rec.Error,
		Action:   failedAction(rec, f),
		Warnings: rec.Warnings,
	}
	multi := len(rec.Deployment.Digests) > 1
	for service, ref := range rec.Deployment.Digests {
		d := digestView{Short: shortDigest(ref), Full: ref}
		if multi {
			d.Service = service
		}
		e.Digests = append(e.Digests, d)
	}
	sort.Slice(e.Digests, func(i, j int) bool { return e.Digests[i].Service < e.Digests[j].Service })

	if rec.Status == store.StatusRunning {
		switch p, err := s.svc.Progress(ctx, rec); {
		case err != nil:
			s.log.Warn("can't read rollout progress", "deployment", rec.Deployment.ID, "error", err)
			e.Progress = "Nomad can't be reached right now, so the rollout's progress isn't shown."
		case p == nil:
			e.Progress = "Nomad hasn't started this rollout yet."
		default:
			e.Progress = progressLine(*p)
		}
	}
	return e
}

// deployFormOf builds the form from the stored app: one field per service,
// holding its image.
func deployFormOf(a app.App) deployForm {
	f := deployForm{Action: "/apps/" + a.ID + "/deploy"}
	for _, svc := range a.Services {
		label := "Image"
		if len(a.Services) > 1 {
			label = svc.Name + " image"
		}
		f.Fields = append(f.Fields, deployField{Service: svc.Name, Label: label,
			Name: "image." + svc.Name, ID: "image-" + svc.Name, Value: svc.Image})
	}
	return f
}

// appNotFound is the shell with "There's no app called x." (404), the page
// every app route answers for an unknown app.
func (s *server) appNotFound(w http.ResponseWriter, r *http.Request, id string) {
	head, err := s.shellPage(r, id, "apps", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: id, Mono: true})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	page := appPage{pageData: head, appHeader: appHeader{ID: id,
		Gone: sentence{plain("There's no app called "), mono(id), plain(".")}}}
	s.render(w, http.StatusNotFound, "app", page)
}

// deploymentsPage draws the Deployments tab with the given status and form
// (a refused post draws it again with the typed values and the errors).
func (s *server) deploymentsPage(w http.ResponseWriter, r *http.Request, status int, sum service.AppSummary, form deployForm) {
	id := sum.App.ID
	history, err := s.loadHistory(r, id)
	switch {
	case errors.Is(err, errBadCursor):
		s.message(w, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.",
			"The \"before\" value must be a time. Go back to the Deployments tab and use Show older.")
		return
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case err != nil:
		s.serverError(w, "list deployments", err)
		return
	}
	head, err := s.shellPage(r, id+" deployments", "apps",
		crumb{Label: "Apps", Href: "/apps"}, crumb{Label: id, Href: "/apps/" + id, Mono: true}, crumb{Label: "Deployments"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(sum, "deployments"),
		Deployments: &deploymentsTab{Form: form, History: history}})
}

// showDeployments answers GET /apps/{id}/deployments.
func (s *server) showDeployments(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	sum, err := s.svc.AppSummary(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case err != nil:
		s.serverError(w, "read app", err)
	default:
		s.deploymentsPage(w, r, http.StatusOK, sum, deployFormOf(sum.App))
	}
}

// partDeployments answers GET /parts/apps/{id}/deployments: the history
// region alone. An app that isn't there any more was deleted.
func (s *server) partDeployments(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	region, err := s.loadHistory(r, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		region = historyRegion{Gone: sentence{mono(id), plain(" was deleted.")}}
	case errors.Is(err, errBadCursor):
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	case err != nil:
		s.log.Error("list deployments", "app", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "app", "deployments-region", region)
}

// imageField matches the path of a service's image in a validation error.
var imageField = regexp.MustCompile(`^services\[(\d+)\]\.image$`)

// deployApp answers POST /apps/{id}/deploy (research R15). The form carries
// one image.<service> field per service. The handler reads the stored app,
// copies the posted images onto it, and deploys: the stored app again when
// no image changed, else the stored app with those images replaced. Nothing
// else of the app can change here, whatever else the form says.
func (s *server) deployApp(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.message(w, http.StatusBadRequest, "Bad request", "That form couldn't be read.", "Go back and try again.")
		return
	}

	a := sum.App
	a.Services = slices.Clone(a.Services) // the posted images go on a copy
	form := deployFormOf(a)
	changed := false
	for i := range a.Services {
		v, posted := r.PostForm["image."+a.Services[i].Name]
		if !posted || len(v) == 0 {
			continue
		}
		typed := strings.TrimSpace(v[0])
		form.Fields[i].Value = typed
		if typed != a.Services[i].Image {
			a.Services[i].Image = typed
			changed = true
		}
	}

	if changed {
		_, err = s.svc.Deploy(ctx, a, app.TriggerConsole)
	} else {
		_, err = s.svc.Redeploy(ctx, id, app.TriggerConsole)
	}
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments", http.StatusSeeOther)
		return
	}

	status, notice, ok := refuseDeploy(&form, err)
	if !ok {
		if errors.Is(err, store.ErrNotFound) {
			s.appNotFound(w, r, id)
			return
		}
		s.serverError(w, "deploy from the console", err)
		return
	}
	form.Notice = notice
	// The failed attempt, if one was recorded, is part of the history now.
	if fresh, err := s.svc.AppSummary(ctx, id); err == nil {
		sum = fresh
	}
	s.deploymentsPage(w, r, status, sum, form)
}

// refuseDeploy says what a failed deploy looks like on the page: an error on
// the image field it is about, or a sentence for the form, and the HTTP
// status. ok is false for errors the page has no words for.
func refuseDeploy(form *deployForm, err error) (status int, notice sentence, ok bool) {
	var fieldErr *app.FieldError
	var imageErr *deploy.ImageError
	// onField puts the message on a service's image field when the path names one.
	onField := func(path, message string) bool {
		m := imageField.FindStringSubmatch(path)
		if m == nil {
			return false
		}
		i, convErr := strconv.Atoi(m[1])
		if convErr != nil || i >= len(form.Fields) {
			return false
		}
		form.Fields[i].Error = message
		return true
	}

	switch {
	case errors.As(err, &fieldErr):
		if onField(fieldErr.Field, fieldErr.Message) {
			return http.StatusUnprocessableEntity, nil, true
		}
		return http.StatusUnprocessableEntity, sentence{plain("caravana can't deploy this app: "), mono(fieldErr.Field), plain(" " + fieldErr.Message)}, true
	case errors.As(err, &imageErr):
		status := http.StatusUnprocessableEntity
		if !errors.Is(err, deploy.ErrImageNotFound) {
			status = http.StatusBadGateway // the registry couldn't answer: not the typed value's fault
		}
		if onField(imageErr.Field, imageErr.Error()) {
			return status, nil, true
		}
		return status, sentence{plain(imageErr.Error())}, true
	case errors.Is(err, deploy.ErrJobConflict):
		if foreignJob(err.Error()) {
			return http.StatusConflict, sentence{plain("A job with this name already exists in Nomad and caravana didn't create it, so nothing was deployed. The running version, if any, was left alone. Stop that job in Nomad, then deploy again.")}, true
		}
		return http.StatusConflict, sentence{plain("The Nomad job changed while deploying. Nothing was lost; try again.")}, true
	case errors.Is(err, deploy.ErrNomadRejected):
		return http.StatusBadGateway, sentence{plain("Nomad rejected the job: " + strings.TrimPrefix(err.Error(), "nomad rejected the job: "))}, true
	case errors.Is(err, deploy.ErrNomadUnavailable):
		return http.StatusBadGateway, sentence{plain("Nomad couldn't be reached right now, so nothing was deployed. The attempt is in the history below.")}, true
	}
	return 0, nil, false
}

// ---- Configure tab (/apps/{id}/configure) --------------------------------------------

// configureExtras is what a Configure response adds to the stored facts: the
// webhook URL just made (shown once), and the refusals of a delete.
type configureExtras struct {
	WebhookURL   string
	DeleteError  sentence
	DeleteNotice sentence
	Secret       secretForm
}

// configurePage draws the Configure tab with the given status. An unknown app
// is the not-found page. Everything on it comes from the service: the stored
// app, its webhook status and its deployments; nothing the Nomad job holds.
func (s *server) configurePage(w http.ResponseWriter, r *http.Request, status int, id string, extra configureExtras) {
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
	hook, err := s.svc.WebhookStatus(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "read webhook", err)
		return
	}
	deployments, err := s.svc.ListDeployments(ctx, id, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "list deployments", err)
		return
	}
	head, err := s.shellPage(r, id+" configure", "apps",
		crumb{Label: "Apps", Href: "/apps"}, crumb{Label: id, Href: "/apps/" + id, Mono: true}, crumb{Label: "Configure"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id

	tab := &configureTab{
		Panels:  definitionOf(sum.App),
		APIHint: sentence{plain("Editing here comes later. Until then, change these through the API: "), mono("POST /v1/apps/" + id + "/deployments"), plain(".")},
		Secrets: secretsOf(sum.App, extra.Secret),
		Webhook: webhookView{Enabled: hook.Enabled, URL: extra.WebhookURL},
		Delete: deleteView{ID: id, Goes: whatGoes(sum.App, sum, len(deployments), hook.Enabled),
			Error: extra.DeleteError, Notice: extra.DeleteNotice},
	}
	if hook.Enabled {
		tab.Webhook.Created = whenOf(s.now(), &hook.CreatedAt, "")
	}
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(sum, "configure"), Configure: tab})
}

// showConfigure answers GET /apps/{id}/configure.
func (s *server) showConfigure(w http.ResponseWriter, r *http.Request, session string) {
	s.configurePage(w, r, http.StatusOK, r.PathValue("id"), configureExtras{})
}

// createWebhook answers POST /apps/{id}/webhook: it makes the app's webhook
// token, or replaces it (the old one stops working at once). The answer is the
// Configure tab itself, status 200, with the whole URL: a redirect would put
// the token in the browser's history, and it is shown only here.
func (s *server) createWebhook(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	tok, err := s.svc.CreateWebhookToken(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case err != nil:
		s.serverError(w, "create webhook token", err)
	default:
		s.configurePage(w, r, http.StatusOK, id, configureExtras{WebhookURL: s.origin(r) + tok.Path})
	}
}

// revokeWebhook answers POST /apps/{id}/webhook/revoke: the webhook refuses
// every call from now on. Revoking when there is no token is fine.
func (s *server) revokeWebhook(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	switch err := s.svc.DeleteWebhookToken(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case err != nil:
		s.serverError(w, "revoke webhook token", err)
	default:
		http.Redirect(w, r, "/apps/"+id+"/configure", http.StatusSeeOther)
	}
}

// deleteApp answers POST /apps/{id}/delete (research R16). The form must carry
// the app's own ID as "confirm", exactly: console.js only keeps the button off
// until it matches, so this is the real guard. A wrong name draws the tab
// again (422) with the confirmation open; Nomad failing to stop the job
// (502) leaves everything in place and says so. Success goes to the Apps page,
// which says the app was deleted.
func (s *server) deleteApp(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	if r.PostForm.Get("confirm") != id {
		s.configurePage(w, r, http.StatusUnprocessableEntity, id,
			configureExtras{DeleteError: sentence{plain("Type "), mono(id), plain(" to confirm.")}})
		return
	}

	err := s.svc.DeleteApp(r.Context(), id)
	switch {
	case err == nil, errors.Is(err, store.ErrNotFound):
		// Not found after the right name typed: it is already gone (a second
		// click, another tab), which is what was asked for.
		http.Redirect(w, r, "/apps?deleted="+url.QueryEscape(id), http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't delete app: Nomad didn't stop its job", "app", id, "error", err)
		s.configurePage(w, r, http.StatusBadGateway, id, configureExtras{
			DeleteNotice: sentence{plain("Nomad couldn't be reached right now, so nothing was deleted. The app is still here.")}})
	case errors.Is(err, secrets.ErrUnavailable):
		s.log.Warn("can't delete app: the secret store refused", "app", id, "error", err)
		s.configurePage(w, r, http.StatusBadGateway, id, configureExtras{
			DeleteNotice: sentence{plain("The app's job is stopped, but its secrets couldn't be deleted, so the app is still here. Try again.")}})
	default:
		s.serverError(w, "delete app", err)
	}
}

// setSecret answers POST /apps/{id}/secrets: it sets one secret of one
// service and deploys, keeping the others (service.SetSecret). Success goes
// to Deployments, where the rollout shows. A refusal draws Configure again
// with the service and name filled back in, never the value.
func (s *server) setSecret(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	form := secretForm{Service: r.PostForm.Get("service"), Name: strings.TrimSpace(r.PostForm.Get("name"))}
	// A textarea sends its line breaks as CRLF; secrets (keys, certificates)
	// expect LF.
	value := strings.ReplaceAll(r.PostForm.Get("value"), "\r\n", "\n")
	_, err := s.svc.SetSecret(r.Context(), id, form.Service, form.Name, value, app.TriggerConsole)
	s.afterSecretChange(w, r, id, form, err)
}

// removeSecret answers POST /apps/{id}/secrets/remove: it deploys without
// one secret, keeping the others (service.RemoveSecret).
func (s *server) removeSecret(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	_, err := s.svc.RemoveSecret(r.Context(), id, r.PostForm.Get("service"), r.PostForm.Get("name"), app.TriggerConsole)
	s.afterSecretChange(w, r, id, secretForm{}, err)
}

// secretValueField matches an error about a secret's value, or about a
// service's secrets as a whole (their size).
var secretValueField = regexp.MustCompile(`^services\[\d+\]\.secrets(\[\d+\]\.value)?$`)

// afterSecretChange answers a set or remove: Deployments on success, else
// Configure with what went wrong.
func (s *server) afterSecretChange(w http.ResponseWriter, r *http.Request, id string, form secretForm, err error) {
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments", http.StatusSeeOther)
		return
	}
	var fieldErr *app.FieldError
	status := http.StatusUnprocessableEntity
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case errors.As(err, &fieldErr):
		switch {
		case fieldErr.Field == "name" || strings.HasSuffix(fieldErr.Field, ".name"):
			form.NameError = sentence{plain(fieldErr.Message)}
		case secretValueField.MatchString(fieldErr.Field):
			form.ValueError = sentence{plain(fieldErr.Message)}
		default:
			form.Notice = sentence{plain("caravana can't deploy this app: "), mono(fieldErr.Field), plain(" " + fieldErr.Message)}
		}
	case errors.Is(err, secrets.ErrUnavailable) && !errors.Is(err, deploy.ErrNomadUnavailable):
		status = http.StatusBadGateway
		form.Notice = sentence{plain("The secret store refused the change, so nothing was deployed. The running copies keep their secrets.")}
		s.log.Warn("can't change a secret: the secret store refused", "app", id, "error", err)
	default:
		var notice sentence
		var ok bool
		if status, notice, ok = refuseDeploy(&deployForm{}, err); !ok {
			s.serverError(w, "change a secret", err)
			return
		}
		form.Notice = notice
	}
	s.configurePage(w, r, status, id, configureExtras{Secret: form})
}
