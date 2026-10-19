package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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
	"github.com/cosmic-chimps/caravana/internal/httpx"
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
	{Key: "backups", Label: "Backups", Path: "/backups"},
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
	ID string
	// Heading is the app's name in its environment, which the header's headline
	// shows (the ID at home).
	Heading string
	// Where is "<project> › <environment> › <app>", each step a link or not; empty
	// for an app at home while the installation has no other environment.
	Where []placeStep
	// Envs are the project's other environments with what the app can do there
	// (feature 019); empty while the project has only this one.
	Envs   []envLink
	Status statusView
	// Actions are the Restart, Stop and Start buttons (feature 020.5); nil
	// while nothing is deployed.
	Buttons    *headerActions
	Domains    []domainLink
	Summary    sentence
	NomadError string // why the status is unknown, as Nomad said it; mono detail
	Tabs       []tabLink
	HeaderHref string   // where the header region refreshes from; it keeps the tab, so the right one stays marked
	Gone       sentence // "blog was deleted." / "There's no app called x."
}

// appPage is the whole app page: the header, and the body of the current tab
// (each tab's data is a pointer that is nil on the other tabs).
type appPage struct {
	pageData
	appHeader
	// Created is the notice after New app's "Create without deploying" (feature
	// 020): the app exists and nothing is deployed yet. Empty otherwise.
	Created sentence
	// Notice is what an action that just succeeded says (?did=), Refusal why one
	// was refused (feature 020.5). Both show above the header.
	Notice  sentence
	Refusal sentence
	Running *copiesRegion
	// Run and Actions are the Running tab's "Run a command" part and its list
	// of recent actions.
	Run         *runSection
	Actions     *actionsRegion
	Deployments *deploymentsTab
	Configure   *configureTab
	Compose     *composeView // Deploy from Compose, under the Configure tab
	Logs        *logsTab
	Backups     *backupsTab
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

// placeStep is one step of an app's place line: a project, an environment, the
// app. The project links to its page.
type placeStep struct {
	Label string
	Href  string
	Mono  bool
}

// envLink is another environment of the app's project. Where the app exists,
// Href leads to it and PromoteHref to the promotion's preview; where it doesn't,
// CopyAction is what the Copy button posts to.
type envLink struct {
	Name        string
	Exists      bool
	Href        string
	PromoteHref string
	CopyAction  string
}

// placeParts fills the header's place line and Environments line (feature 019)
// from the projects. A failure to read them is logged and leaves both out: the
// rest of the header doesn't depend on them.
func (s *server) placeParts(ctx context.Context, h *appHeader, sum service.AppSummary) {
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		s.log.Error("read projects for the app header", "app", sum.App.ID, "error", err)
		return
	}
	place := sum.Place
	if place.Project == "" {
		return
	}
	h.Heading = cmp.Or(place.Name, sum.App.ID)
	if atHome(place) && pl.single() {
		return
	}
	h.Where = []placeStep{
		{Label: pl.display(place.Project), Href: "/projects/" + url.PathEscape(place.Project)},
		{Label: place.Environment, Mono: true},
		{Label: place.Name, Mono: true},
	}
	for _, pr := range pl.Projects {
		if pr.ID != place.Project || len(pr.Environments) < 2 {
			continue
		}
		for _, env := range pr.Environments {
			if env.Name == place.Environment {
				continue
			}
			there := app.Place{Project: place.Project, Environment: env.Name, Name: place.Name}
			if _, err := app.PlaceOf(there.ID()); err != nil {
				continue // an app from before projects whose name holds "--" has no counterpart
			}
			link := envLink{Name: env.Name, CopyAction: "/apps/" + url.PathEscape(sum.App.ID) + "/copy"}
			if rec, err := s.svc.GetApp(ctx, there.ID()); err == nil && rec.Place == there {
				link.Exists = true
				link.Href = "/apps/" + url.PathEscape(there.ID())
				link.PromoteHref = "/apps/" + url.PathEscape(sum.App.ID) + "/promote?to=" + url.QueryEscape(env.Name)
			}
			h.Envs = append(h.Envs, link)
		}
	}
}

// header builds the app's header from its summary. current is the key of the
// tab being shown.
func (s *server) header(ctx context.Context, sum service.AppSummary, current string) appHeader {
	h := appHeader{
		ID:         sum.App.ID,
		Heading:    sum.App.ID,
		Status:     appStatusView(sum),
		Buttons:    actionsOf(sum),
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
	h.HeaderHref = "/parts/apps/" + sum.App.ID + "/header"
	if current != "running" {
		h.HeaderHref += "?tab=" + url.QueryEscape(current)
	}
	s.placeParts(ctx, &h, sum)
	return h
}

// showApp answers GET /apps/{id}: the app's page. An unknown app is a 404
// page inside the shell.
func (s *server) showApp(w http.ResponseWriter, r *http.Request, session string) {
	s.runningPage(w, r, http.StatusOK, r.PathValue("id"), runningExtras{})
}

// runningExtras is what an answer adds to the Running tab: why an action was
// refused, the result of a command, or the line a refused post typed.
type runningExtras struct {
	Refusal sentence
	Result  *runResult
	Typed   *typedCommand
}

// runningPage draws the Running tab (the app's own page) with the given status.
func (s *server) runningPage(w http.ResponseWriter, r *http.Request, status int, id string, extra runningExtras) {
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
	// ?tab=running is what makes the stream send "usage" events: nothing reads
	// CPU and memory from Nomad unless this tab is open (FR-015).
	head.LiveURL = "/live/apps/" + id + "?tab=running"
	copies, err := s.loadCopies(r, id)
	if err != nil {
		s.serverError(w, "list copies", err)
		return
	}
	actions, err := s.loadActions(r, id)
	if err != nil {
		s.serverError(w, "list actions", err)
		return
	}
	page := appPage{pageData: head, appHeader: s.header(r.Context(), sum, "running"), Running: &copies, Actions: &actions,
		Run: runSectionOf(id, copies, extra), Refusal: extra.Refusal}
	if r.URL.Query().Get("created") == "1" && sum.LastDeployment == nil {
		// Only said while it is still true: a reload after the first deploy says nothing.
		page.Created = sentence{plain("Created "), mono(cmp.Or(sum.Place.Name, sum.App.ID)), plain(". Nothing is deployed yet.")}
	}
	page.Notice = didNotice(r.URL.Query().Get("did"), sum)
	s.render(w, status, "app", page)
}

// runSectionOf builds the "Run a command" part from the copies the tab shows:
// a form for each copy that hasn't ended. A refused post's line goes back in
// its copy's form, and a refusal that isn't about one form shows above them.
func runSectionOf(id string, copies copiesRegion, extra runningExtras) *runSection {
	sec := &runSection{Result: extra.Result}
	for _, c := range copies.Rows {
		if !c.Live {
			continue
		}
		f := runForm{Action: "/apps/" + url.PathEscape(id) + "/copies/" + url.PathEscape(c.ID) + "/run",
			ID: "run-" + c.Short, CopyID: c.ID, Short: c.Short, Service: c.Service}
		if extra.Typed != nil && extra.Typed.CopyID == c.ID {
			f.Value, f.Error = extra.Typed.Line, extra.Typed.Error
		}
		sec.Forms = append(sec.Forms, f)
	}
	if extra.Typed != nil {
		// The error is on its form; a copy that is gone has none, so the
		// sentence goes on top.
		shown := false
		for _, f := range sec.Forms {
			shown = shown || f.CopyID == extra.Typed.CopyID
		}
		if !shown {
			sec.Refusal = extra.Typed.Error
		}
	}
	return sec
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
		s.renderPart(w, "app", "app-header", s.header(r.Context(), sum, s.tabKey(r.URL.Query().Get("tab"))))
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
// With ?logs=1 (the Logs tab) it also carries the app's log lines; with
// ?tab=running (the Running tab) a "usage" event every few seconds, which is
// what makes the copies region read CPU and memory again.
func (s *server) liveApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	route := liveRoute{AppID: id,
		Kinds: []service.ChangeKind{service.ChangeApp, service.ChangeDeployments, service.ChangeCopies, service.ChangeActions, service.ChangeDeleted}}
	switch {
	case r.URL.Query().Get("logs") == "1":
		if choice, ok := logChoiceOf(r.URL.Query()); ok {
			route.Start = s.logStart(id, choice)
		}
	case r.URL.Query().Get("tab") == "running":
		route.Start = s.usageStart()
	}
	s.stream(w, r, route)
}

// usageStart is the Start of the Running tab's stream: a "usage" event every
// usageEvery until the stream ends. The event carries nothing; the page
// fetches the copies region again, and that is what asks Nomad.
func (s *server) usageStart() func(ctx context.Context, ev *httpx.EventWriter) {
	return func(ctx context.Context, ev *httpx.EventWriter) {
		tick := time.NewTicker(s.live.usageInterval())
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if ev.Event("usage", nil) != nil {
					return // the client is gone
				}
			case <-ctx.Done():
				return
			}
		}
	}
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
	// OutOfMemory marks a copy whose last stop was the kernel killing it for
	// passing its memory ceiling; the status column says so.
	OutOfMemory bool
	Usage       usageView
	// Live says the copy hasn't ended, so a command can be run in it.
	Live bool
}

// usageView is a copy's CPU and memory cells. A copy that isn't running has
// neither (the cells show a dash); one Nomad didn't answer for has Error.
type usageView struct {
	Has      bool   // there are numbers
	Error    string // the sentence, when Nomad didn't answer for this copy
	CPU      string // "42 MHz"
	CPUOf    string // "of 100 MHz reserved"
	Memory   string // "120 MB"
	MemoryOf string // "of 256 MB ceiling, 64 reserved"
	MeterPct int    // memory in use against the ceiling, 0 to 100
	NearCeil bool   // 90 % of the ceiling or more: the warning mark and words
}

// copiesRegion is what the "copies" region draws: the copies, or one sentence
// on why there are none. Notice replaces everything when Nomad can't be read;
// Gone when the app was deleted. Failure, with the Deployments link, explains
// an empty list after a deploy that didn't work.
type copiesRegion struct {
	Rows []copyEntry
	// First is set when there are no copies because the app was never deployed
	// (feature 019.5): what to do next instead of "No copies are running".
	First   *firstDeploy
	Failure sentence // "v8 failed: see "; the template adds the link and the full stop
	// StoppedNote says why an app with no copies has none: the owner stopped it.
	StoppedNote     sentence
	DeploymentsHref string
	Notice          sentence
	RefreshHref     string
	Gone            sentence
}

// firstDeploy is the guidance of an app that was created and never deployed:
// how to give it its first deploy (or, with no definition yet, a definition).
type firstDeploy struct {
	Defined       bool   // it has services: it deploys as stored
	DeployAction  string // where "Deploy now" posts
	ConfigureHref string // the Configure tab, where a definition is made
	WebhookHref   string // Configure's webhook section
	ComposeHref   string // Deploy from Compose
	Call          string // the API call, as a curl line
	CallNote      string // what goes in the call's body, for an app with no definition
}

// firstDeployOf builds the guidance for appID, which has no deployment.
func (s *server) firstDeployOf(r *http.Request, rec store.AppRecord) *firstDeploy {
	id := rec.App.ID
	f := &firstDeploy{
		Defined:       rec.App.Defined(),
		DeployAction:  "/apps/" + id + "/deploy",
		ConfigureHref: "/apps/" + id + "/configure",
		WebhookHref:   "/apps/" + id + "/configure#webhook",
		ComposeHref:   "/apps/" + id + "/compose",
	}
	call := `curl -X POST -H "Authorization: Bearer $CARAVANA_TOKEN" `
	if f.Defined {
		f.Call = call + s.origin(r) + "/v1/apps/" + id + "/deployments"
	} else {
		f.Call = call + `-H "Content-Type: application/json" -d @app.json ` + s.origin(r) + "/v1/apps/" + id + "/deployments"
		f.CallNote = "app.json holds the app: its source and its services."
	}
	return f
}

// loadCopies reads an app's copies and the newest deployment, which says why
// there may be none. An unknown app is Gone, and a Nomad that can't be read is
// a Notice: both are what the region shows, not errors of the page.
func (s *server) loadCopies(r *http.Request, appID string) (copiesRegion, error) {
	ctx := r.Context()
	region := copiesRegion{RefreshHref: "/parts/apps/" + appID + "/copies", DeploymentsHref: s.deploymentsHref(appID)}

	copies, usage, err := s.svc.CopiesWithUsage(ctx, appID)
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
		e := copyEntryOf(now, c)
		if u, ok := usage[c.ID]; ok {
			e.Usage = usageViewOf(u)
		}
		region.Rows = append(region.Rows, e)
	}
	if len(copies) == 0 {
		recs, err := s.svc.ListDeployments(ctx, appID, 1)
		if err != nil {
			return copiesRegion{}, err
		}
		rec, recErr := s.svc.GetApp(ctx, appID)
		if recErr != nil && !errors.Is(recErr, store.ErrNotFound) {
			return copiesRegion{}, recErr
		}
		switch {
		case recErr == nil && rec.StoppedAt != nil:
			region.StoppedNote = sentence{plain("The app is stopped. Start it to run its copies again.")}
		case len(recs) > 0:
			region.Failure = failedDeploySentence(recs[0])
		case recErr == nil:
			region.First = s.firstDeployOf(r, rec)
		}
	}
	return region, nil
}

func copyEntryOf(now time.Time, c service.Copy) copyEntry {
	e := copyEntry{ID: c.ID, Short: shortID(c.ID), Service: c.Service, Status: copyStatusView(c.Status),
		Version: versionLabel(&c.Version), Restarts: c.Restarts, OutOfMemory: c.OutOfMemory, Live: !c.Terminal}
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

func usageViewOf(u service.CopyUsage) usageView {
	if u.Error != "" {
		return usageView{Error: u.Error}
	}
	v := usageView{Has: true, NearCeil: u.NearCeiling,
		CPU: fmt.Sprintf("%d MHz", u.CPUMHz), CPUOf: fmt.Sprintf("of %d MHz reserved", u.CPUReservedMHz),
		Memory: fmt.Sprintf("%d MB", u.MemoryMB), MemoryOf: fmt.Sprintf("of %d MB ceiling, %d reserved", u.MemoryCeilingMB, u.MemoryReservedMB)}
	if u.MemoryCeilingMB > 0 {
		v.MeterPct = min(u.MemoryMB*100/u.MemoryCeilingMB, 100)
	}
	return v
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
	// ConfigureHref and ComposeHref say where an app with no definition (so no
	// image field to deploy) is given one.
	ConfigureHref, ComposeHref string
	Action                     string
	Fields                     []deployField
	Notice                     sentence
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
	// Source is the deployment this one redeployed; nil for the others.
	Source *sourceLink
	// Promoted is the deployment of another environment's app this one
	// promoted (feature 019); nil for the others.
	Promoted *promotedLink
	// Redeploy offers this version again: healthy deployments with a
	// recorded definition only. ExportHref downloads its jobspec.
	Redeploy   *redeployView
	ExportHref string
	// ComposeHref downloads the Compose file this deployment was made from
	// (feature 015); "" when it wasn't made from one.
	ComposeHref string
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
	// The stored app says which secrets a redeploy of a past version removes.
	current, err := s.svc.GetApp(ctx, appID)
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
		e := s.historyEntry(ctx, now, rec, facts[i])
		addVersionActions(&e, rec, current.App)
		region.Entries = append(region.Entries, e)
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
	f := deployForm{Action: "/apps/" + a.ID + "/deploy",
		ConfigureHref: "/apps/" + a.ID + "/configure", ComposeHref: "/apps/" + a.ID + "/compose"}
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
// refused, when set, is a refused redeploy of a past version, shown in its
// entry.
func (s *server) deploymentsPage(w http.ResponseWriter, r *http.Request, status int, sum service.AppSummary, form deployForm, refused *refusedRedeploy) {
	id := sum.App.ID
	if sum.StoppedAt != nil && len(form.Notice) == 0 {
		// Said before anyone tries: a stopped app refuses every deploy, and the
		// header has the Start button.
		form.Notice = sentence{plain(appStoppedText)}
	}
	history, err := s.loadHistory(r, id)
	if err == nil && refused != nil {
		shown := false
		for i, e := range history.Entries {
			if e.ID == refused.ID && e.Redeploy != nil {
				history.Entries[i].Redeploy.Error, shown = refused.Why, true
			}
		}
		if !shown { // that entry offers no redeploy, or is on another page
			form.Notice = refused.Why
		}
	}
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
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(r.Context(), sum, "deployments"),
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
		s.deploymentsPage(w, r, http.StatusOK, sum, deployFormOf(sum.App), nil)
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
	s.deploymentsPage(w, r, status, sum, form, nil)
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
	case errors.Is(err, service.ErrAppStopped):
		return http.StatusConflict, sentence{plain(appStoppedText)}, true
	case errors.Is(err, service.ErrAppNotDefined):
		return http.StatusConflict, sentence{plain("This app has no definition yet, so there is nothing to deploy. Define it in Configure, or deploy a Compose file.")}, true
	case errors.As(err, &fieldErr):
		if onField(fieldErr.Field, fieldErr.Message) {
			return http.StatusUnprocessableEntity, nil, true
		}
		return http.StatusUnprocessableEntity, sentence{plain("caravana can't deploy this app: "), mono(fieldErr.Field), plain(" " + fieldErr.Message)}, true
	case errors.As(err, &imageErr):
		status := http.StatusUnprocessableEntity
		if !errors.Is(err, deploy.ErrImageNotFound) && !errors.Is(err, deploy.ErrRegistryAuth) {
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
// webhook URL just made (shown once), the refusals of a delete, and the form
// when a refused save draws it again.
type configureExtras struct {
	WebhookURL   string
	DeleteError  sentence
	DeleteNotice sentence
	Secret       secretForm
	// Form, when set, is the edit form to draw (what was typed, with its
	// errors) instead of one made from the stored app. Stale is the panel of
	// a save refused because the app changed.
	Form  *editForm
	Stale *staleView
	// Notice is a refusal of the tab as a whole (a per-field save on an app
	// edited through its Compose file).
	Notice sentence
	// Define, when set, is the "Define the app" form to draw (what was typed,
	// with its errors) for an app that has no definition yet.
	Define *newAppForm
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

	form := formOf(sum.App, sum.Revision)
	if extra.Form != nil {
		form = *extra.Form
	}
	if sum.StoppedAt != nil && len(extra.Notice) == 0 {
		extra.Notice = sentence{plain(appStoppedText)}
	}
	tab := &configureTab{
		App:     appFacts(sum.App),
		Compose: sum.App.Source.Kind == app.SourceCompose,
		Notice:  extra.Notice,
		Copied:  copiedNotice(r, sum),
		Edit:    editViewOf(sum.App, form, extra.Stale),
		Secrets: secretsOf(sum.App, extra.Secret),
		Webhook: webhookView{Enabled: hook.Enabled, URL: extra.WebhookURL, Never: len(deployments) == 0},
		Export:  exportOf(id, deployments),
		Delete: deleteView{ID: id, Goes: whatGoes(sum.App, sum, len(deployments), hook.Enabled),
			Error: extra.DeleteError, Notice: extra.DeleteNotice},
	}
	if sum.Compose != nil {
		// The file is the app's source of truth: its form comes first, and the
		// definition it makes is shown, not edited (feature 015, research R6).
		v := keptComposeView(sum.AppRecord)
		tab.ComposeForm = &v
		tab.Edit.ReadOnly = true
		tab.Edit.Panels = servicePanels(sum.App)
	} else if sum.App.Source.Kind == app.SourceCompose {
		// Made from Compose before the file was kept: ask for it once. The
		// per-field form stays editable until the file is pasted (US2).
		v := notKeptComposeView(sum.AppRecord)
		tab.ComposeForm = &v
	}
	if !sum.App.Defined() {
		// Nothing to edit yet: the form that gives it a definition replaces the per-service one.
		form := newForm()
		if extra.Define != nil {
			form = *extra.Define
		}
		tab.Define = &defineView{Action: "/apps/" + id + "/define", Revision: sum.Revision, Form: form}
	}
	if hook.Enabled {
		tab.Webhook.Created = whenOf(s.now(), &hook.CreatedAt, "")
	}
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(r.Context(), sum, "configure"), Configure: tab})
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
