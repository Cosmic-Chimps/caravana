package ui

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Previews page (feature 049, contracts/previews-screen.md): /previews, the
// handoff's PreviewsScreen. Its open tab lists the open previews of every project
// as one live list; its setup tab holds each project's preview settings (the form
// in previews.go). Everything it knows comes from Service.Projects and
// Service.Previews, so it is the same facts as the API's getPreviews.

// The tabs of the page.
const (
	tabOpen  = "open"
	tabSetup = "setup"
)

// previewsSeg is one link of a segmented bar.
type previewsSeg struct {
	Label, Href string
	On          bool
}

// previewsPage is the template set "previews": exactly one of Open and Setup is set.
type previewsPage struct {
	pageData
	Tabs   []previewsSeg
	Notice sentence
	Open   *previewsRegion
	Setup  *previewsSetup
}

// previewsRegion is the open tab's live region: the count line and the list. The part
// /parts/previews draws it alone.
type previewsRegion struct {
	// Count is "3 open · reserving 2.5 GB of memory"; "" (not drawn) when none is open.
	Count       string
	Rows        []previewView
	RefreshHref string
}

// previewView is one open preview as the list draws it.
type previewView struct {
	Project, Environment string
	// Number is "#142", the preview's name (Caravana never learns the pull request's
	// title, ruling 1). Branch is the newest deployment's branch, "" when it names none:
	// the number then stands alone as the row's name.
	Number string
	Branch string
	// Meta is "Shop Floor · pushed by ana · 4e0a77"; When and Run follow it.
	Meta      sentence
	MetaTitle string
	When      *when // nil when nothing was deployed yet
	Run       string
	Pill      statusView
	Apps      []projectApp
	// Address is the first address of the preview's apps; More counts the others.
	Address string
	More    int
	Live    bool // the row is healthy: its address is drawn in the first text colour
	Memory  string
	// Password says the project has a preview password, so the address asks for it.
	Password                      bool
	OpenHref, LogsHref, CloseHref string
	Line                          *previewLine

	// newest and memoryMB order the list and add up the count line.
	newest   time.Time
	memoryMB int
}

// previewLine is the one line under a row: a failed app, an app rolling out, or when the
// preview closes.
type previewLine struct {
	Kind string // previewLineFailed, previewLineRolling or previewLineExpiry
	Lead sentence
	// Error is the failed deployment's raw error and ErrorLead what it is ("Nomad said: "),
	// drawn as the history entry draws them.
	Error, ErrorLead string
	Href             string // "See the deploy"
	Started          *when  // a rollout's start
	// Closes is the preview's time of closing; Expired says it has passed (the reaper
	// closes it at its next look).
	Closes  *clockTime
	Expired bool
}

const (
	previewLineFailed  = "failed"
	previewLineRolling = "rolling"
	previewLineExpiry  = "expiry"
)

// previewsSetup is the setup tab: a bar of projects and the chosen one's settings.
type previewsSetup struct {
	Projects []previewsSeg
	Display  string
	Form     *previewsForm
	// State is "On · 2 open now" or "Off (at most 0)"; Budget is " · about 1.2 GB if each
	// reserves what open ones do", only while previews are open.
	State  string
	Budget string
}

// previewsQuery says which tab and, for the setup tab, which project.
type previewsQuery struct {
	Tab, Project string
}

// previewsExtras is what a response adds: a calm sentence, the preview that was just
// closed, and what was typed in a refused settings form.
type previewsExtras struct {
	Notice sentence
	Closed struct{ Environment, Project string }
	Setup  *previewsExtra
}

// projectPreviews is a project with its previews, as the page reads them.
type projectPreviews struct {
	Project service.Project
	List    service.PreviewList
}

// loadPreviewLists reads every project's previews, in the order Projects gives.
func (s *server) loadPreviewLists(ctx context.Context) ([]projectPreviews, error) {
	projects, err := s.svc.Projects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]projectPreviews, 0, len(projects))
	for _, p := range projects {
		list, err := s.svc.Previews(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, projectPreviews{Project: p, List: list})
	}
	return out, nil
}

// showPreviews answers GET /previews. An unknown tab is the open one.
func (s *server) showPreviews(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	pq := previewsQuery{Tab: tabOpen, Project: q.Get("project")}
	var x previewsExtras
	if q.Get("tab") == tabSetup {
		pq.Tab = tabSetup
		if q.Get("saved") == "1" {
			x.Notice = plainS("Preview settings saved.")
		}
	} else if labelName.MatchString(q.Get("closed")) && labelName.MatchString(q.Get("project")) {
		x.Closed.Environment, x.Closed.Project = q.Get("closed"), q.Get("project")
	}
	s.renderPreviews(w, r, http.StatusOK, pq, x)
}

// renderPreviews draws the page with the given status.
func (s *server) renderPreviews(w http.ResponseWriter, r *http.Request, status int, pq previewsQuery, x previewsExtras) {
	lists, err := s.loadPreviewLists(r.Context())
	if err != nil {
		s.serverError(w, r, "read the previews", err)
		return
	}
	head, err := s.shellPage(r, "Previews", navPreviews, crumb{Label: "Previews"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	if x.Closed.Environment != "" {
		display := x.Closed.Project
		for _, l := range lists {
			if l.Project.ID == x.Closed.Project {
				display = l.Project.DisplayName
			}
		}
		x.Notice = sentence{plain("Closed "), mono(x.Closed.Environment), plain(" of " + display + ".")}
	}
	page := previewsPage{Notice: x.Notice, Tabs: []previewsSeg{
		{Label: "open previews", Href: "/previews", On: pq.Tab == tabOpen},
		{Label: "setup per project", Href: "/previews?tab=setup", On: pq.Tab == tabSetup},
	}}
	if pq.Tab == tabSetup {
		page.Setup = s.previewsSetupOf(lists, pq.Project, x.Setup)
	} else {
		// The list refreshes by region, so the page opens the workspace stream; the setup
		// tab is a form and has none.
		head.LiveURL = "/live"
		page.Open = previewsRegionOf(s.now(), lists)
	}
	page.pageData = head
	s.render(w, status, "previews", page)
}

// partPreviews answers GET /parts/previews: the open tab's region alone, from the same
// reads as the page.
func (s *server) partPreviews(w http.ResponseWriter, r *http.Request, session string) {
	lists, err := s.loadPreviewLists(r.Context())
	if err != nil {
		s.log.Error("read the previews", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "previews", "previews-region", previewsRegionOf(s.now(), lists))
}

// ---- the open tab -------------------------------------------------------------------------

// previewsRegionOf is the open tab: every open preview of every project, problems
// first, then the newest deployment first, then project and number.
func previewsRegionOf(now time.Time, lists []projectPreviews) *previewsRegion {
	region := &previewsRegion{RefreshHref: "/parts/previews"}
	for _, l := range lists {
		for _, p := range l.List.Previews {
			region.Rows = append(region.Rows, previewViewOf(now, l.Project, l.List.Settings, p))
		}
	}
	slices.SortFunc(region.Rows, func(a, b previewView) int {
		return cmp.Or(b.newest.Compare(a.newest), cmp.Compare(a.Project, b.Project), cmp.Compare(a.Number, b.Number))
	})
	sortProblemsFirst(region.Rows, func(v previewView) string { return v.Pill.Kind })
	if n := len(region.Rows); n > 0 {
		total := 0
		for _, v := range region.Rows {
			total += v.memoryMB
		}
		region.Count = strconv.Itoa(n) + " open · reserving " + reservedText(total) + " of memory"
	}
	return region
}

// reservedText is an amount of memory as the Previews page says it: "512 MB", "2.5 GB".
func reservedText(mb int) string {
	if mb >= 1024 {
		return gbText(float64(mb)) + " GB"
	}
	return sizeText(int64(mb))
}

// pullRef matches the refs GitHub gives a pull request's own run. They are no branch:
// the preview's "#142" already says them (ruling 1).
var pullRef = regexp.MustCompile(`^refs/pull/[0-9]+/(merge|head)$`)

// previewBranch is the branch (or tag) an origin names, "" for none or for a pull
// request's own ref.
func previewBranch(o *app.Origin) string {
	if o == nil || o.Ref == "" || pullRef.MatchString(o.Ref) {
		return ""
	}
	return o.BranchOrTag()
}

// previewWho is deployWho for this page: the ref is never in the words, since the
// branch is the row's own name (and a pull request's own ref is no branch at all, ruling
// 1), so they read "pushed by ana · 4e0a77". rec is a copy; the stored origin is not
// changed.
func previewWho(rec store.DeploymentRecord) sentence {
	if o := rec.Deployment.Origin; o != nil && o.Ref != "" {
		withoutRef := *o
		withoutRef.Ref = ""
		rec.Deployment.Origin = &withoutRef
	}
	return deployWho(rec, true)
}

// previewViewOf is one open preview as the list draws it.
func previewViewOf(now time.Time, project service.Project, settings service.PreviewSettings, p service.Preview) previewView {
	base := "/projects/" + url.PathEscape(project.ID)
	v := previewView{
		Project: project.ID, Environment: p.Environment, Number: "#" + strconv.Itoa(p.Number),
		Password:  settings.PasswordSet,
		LogsHref:  "/logs?" + url.Values{"project": {project.ID}, "environment": {p.Environment}}.Encode(),
		CloseHref: base + "/previews/" + url.PathEscape(p.Environment) + "/close?from=previews",
		Pill:      previewState(p.Apps),
	}

	// The newest deployment of any app of the preview speaks for it; the address is the first
	// one of the first app that has one.
	var newest *store.DeploymentRecord
	addresses := 0
	for _, a := range p.Apps {
		if d := a.LastDeployment; d != nil && (newest == nil || d.CreatedAt.After(newest.CreatedAt)) {
			newest = d
		}
		v.Apps = append(v.Apps, projectApp{Name: a.Name, Href: "/apps/" + url.PathEscape(a.ID)})
		v.memoryMB += a.MemoryMB
		if v.Address == "" && len(a.Addresses) > 0 {
			v.Address = a.Addresses[0]
		}
		addresses += len(a.Addresses)
	}
	if addresses > 1 {
		v.More = addresses - 1
	}
	v.Live = v.Pill.Kind == "healthy"
	if v.Live && v.Address != "" {
		v.OpenHref = "https://" + v.Address + "/"
	}
	if v.memoryMB > 0 {
		v.Memory = reservedText(v.memoryMB) + " reserved"
	} else {
		v.Memory = "nothing reserved"
	}

	display := project.DisplayName
	if newest == nil {
		v.Meta = plainS(display + " · no deploy yet")
		v.MetaTitle = display + " · no deploy yet"
	} else {
		w := whenOf(now, &newest.CreatedAt, "")
		who := previewWho(*newest)
		v.newest, v.When = newest.CreatedAt, &w
		v.Meta = join(plainS(display+" · "), who)
		v.MetaTitle = display + " · " + who.String() + " · " + w.Exact
		if o := newest.Deployment.Origin; o != nil {
			v.Run = o.Run
			if b := previewBranch(o); b != "" {
				v.Branch = b
			}
		}
	}
	v.Line = previewLineOf(now, p)
	return v
}

// previewState is the preview's word: empty with no app, else the worst app's status in
// the console's own words (appStatusView), healthy when every app is.
func previewState(apps []service.PreviewApp) statusView {
	if len(apps) == 0 {
		return statusView{"idle", "empty"}
	}
	worst := appStatusView(apps[0].Summary)
	for _, a := range apps[1:] {
		if v := appStatusView(a.Summary); statusRank[v.Kind] > statusRank[worst.Kind] {
			worst = v
		}
	}
	return worst
}

// previewLineOf is the one line under a row, the first that applies: an app whose newest
// deployment failed, an app rolling out, else when the preview closes.
func previewLineOf(now time.Time, p service.Preview) *previewLine {
	for _, a := range p.Apps {
		if !deployEnded(a.Summary) {
			continue
		}
		rec := *a.LastDeployment
		lead := sentence{mono(a.Name), plain("'s newest deployment ")}
		if label := versionLabel(rec.JobVersion); label != "" {
			lead = append(lead, mono(label), plain(" "))
		}
		if rec.Status == store.StatusReverted {
			lead = append(lead, plain("was reverted"))
		} else {
			lead = append(lead, plain("failed"))
		}
		if rec.Error != "" {
			lead = append(lead, plain(":"))
		}
		return &previewLine{Kind: previewLineFailed, Lead: lead, Error: rec.Error, ErrorLead: deployErrorLead(rec),
			Href: "/apps/" + url.PathEscape(a.ID) + "/deployments"}
	}
	for _, a := range p.Apps {
		if a.Summary.Status.State != service.StateRollingOut {
			continue
		}
		lead := sentence{plain("Rolling out "), mono(a.Name)}
		line := &previewLine{Kind: previewLineRolling}
		if rec := a.LastDeployment; rec != nil {
			if label := versionLabel(rec.JobVersion); label != "" {
				lead = append(lead, plain(" "), mono(label))
			}
			started := whenOf(now, &rec.CreatedAt, "")
			line.Started = &started
		}
		line.Lead = lead
		return line
	}
	closes := clockOf(now, p.ExpiresAt)
	return &previewLine{Kind: previewLineExpiry, Closes: &closes, Expired: p.ExpiresAt.Before(now)}
}

// ---- the setup tab ------------------------------------------------------------------------

// previewsSetupOf is the setup tab: the project is the one asked for, else the first with
// an open preview, else the first.
func (s *server) previewsSetupOf(lists []projectPreviews, wanted string, extra *previewsExtra) *previewsSetup {
	if len(lists) == 0 {
		return &previewsSetup{}
	}
	chosen := slices.IndexFunc(lists, func(l projectPreviews) bool { return l.Project.ID == wanted })
	if chosen < 0 {
		chosen = slices.IndexFunc(lists, func(l projectPreviews) bool { return len(l.List.Previews) > 0 })
	}
	if chosen < 0 {
		chosen = 0
	}
	cur := lists[chosen]
	setup := &previewsSetup{Display: cur.Project.DisplayName, Form: previewsFormOf(cur.Project.ID, cur.List.Settings, extra)}
	for i, l := range lists {
		// A project with previews open says how many: "Shop · 3".
		label := l.Project.DisplayName
		if n := len(l.List.Previews); n > 0 {
			label += " · " + strconv.Itoa(n)
		}
		setup.Projects = append(setup.Projects, previewsSeg{Label: label, On: i == chosen,
			Href: "/previews?" + url.Values{"tab": {tabSetup}, "project": {l.Project.ID}}.Encode()})
	}

	st := cur.List.Settings
	if st.Limit > 0 {
		setup.State = "On · " + strconv.Itoa(st.Open) + " open now"
	} else {
		setup.State = "Off (at most 0)"
	}
	// What the limit could reserve if every preview took what the open ones do on average.
	if n := len(cur.List.Previews); n > 0 && st.Limit > 0 {
		total := 0
		for _, p := range cur.List.Previews {
			for _, a := range p.Apps {
				total += a.MemoryMB
			}
		}
		if mean := total / n; mean > 0 {
			setup.Budget = " · about " + reservedText(mean*st.Limit) + " if each reserves what open ones do"
		}
	}
	return setup
}

// previewsSummary is the one sentence of a project's settings page about its previews,
// with the link to where they change.
type previewsSummary struct {
	Sentence sentence
	Href     string
}

func previewsSummaryOf(project string, st service.PreviewSettings) *previewsSummary {
	sum := &previewsSummary{Href: "/previews?" + url.Values{"tab": {tabSetup}, "project": {project}}.Encode()}
	if st.Limit == 0 {
		sum.Sentence = sentence{plain("Previews are off: at most "), mono("0"), plain(".")}
		return sum
	}
	sum.Sentence = sentence{plain("At most "), mono(strconv.Itoa(st.Limit)), plain(" open previews, closed after "),
		mono(strconv.Itoa(st.ExpireAfterDays)), plain(" days without a deploy, ")}
	if st.PasswordSet {
		sum.Sentence = append(sum.Sentence, plain("with a password."))
	} else {
		sum.Sentence = append(sum.Sentence, plain("public."))
	}
	return sum
}

// previewCloseCrumbs are the crumbs of the Close page when it was opened from the Previews page.
func previewCloseCrumbs(environment string) []crumb {
	return []crumb{{Label: "Previews", Href: "/previews"}, {Label: "Close " + environment}}
}
