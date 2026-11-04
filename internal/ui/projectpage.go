package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// This file is a project's page (feature 027.5, the handoff's ProjectScreen): the
// header with one tab per environment, the environment's apps as a table, and a
// rail with what needs the owner there, the Environment panel and its newest
// deploys. The apps and the rail are two live regions, drawn by the page and by
// /parts/projects/{project}/apps and /rail alike. The forms that used to be this
// page are on the settings page (projects.go).

// projectDeploysLimit is how many deployments "Deploys in <env>" lists.
const projectDeploysLimit = 10

// maxMarks is how many copy marks a row draws; more than that is the text "N copies".
const maxMarks = 12

// envTab is one environment's badge in the tabs row.
type envTab struct {
	Name    string
	Href    string
	Current bool // the current one reads in fg-1 on a ground; every other in fg-2 on none
}

// markGroup is one service's copies in a row: the status kind of each mark.
// Service is set only when the app has several services (it is the group's title).
type markGroup struct {
	Service string
	Kinds   []string
}

// markCell is the Copies cell of a row: a mark per wanted copy, the text "N copies"
// when there are too many to draw, or "—" when nothing was ever deployed. Words is
// what a screen reader hears instead of the marks.
type markCell struct {
	None   bool
	Groups []markGroup
	Count  string
	Words  string
}

// screenRow is one app of the environment's table: the Apps home's row plus the
// copy marks and the first address.
type screenRow struct {
	appRow
	Marks   markCell
	Address string // the first domain; "" when there is none
	More    int    // how many domains follow it
}

// projectApps is what the "project-apps" region draws: the count and the table, or
// the empty state. NewHref leads to New app with this environment chosen ("" when
// New app isn't offered).
type projectApps struct {
	Env     string // "" when the project has no environment at all
	Count   int
	Rows    []screenRow
	NewHref string
	// AddEnvHref is where a project without an environment adds one.
	AddEnvHref string
}

// promoteLink is one app that can be promoted, and where its preview is.
type promoteLink struct {
	Name string
	Href string
}

// promoteRow is "Promote to <env>": the apps of this environment that can go there.
type promoteRow struct {
	Env  string
	Apps []promoteLink
}

// projectRail is what the "project-rail" region draws.
type projectRail struct {
	Env string
	// Deploys says how the environment was last deployed ("Pushed to main by ana ·
	// 2 hours ago"); HasDeploy is false before its first deployment.
	HasDeploy  bool
	DeployWho  sentence
	DeployWhen when
	Addresses  string
	Promote    []promoteRow
	Attention  attentionRegion // only drawn with rows
	Activity   []activityRow
}

// projectScreen is a project's page.
type projectScreen struct {
	ID, Display string
	// Slot is the project's name in mono beside its display name, when they differ.
	Slot         string
	Notice       sentence
	SettingsHref string
	NewHref      string
	AddEnvHref   string
	Tabs         []envTab
	Env          string
	Apps         projectApps
	Rail         projectRail
	AppsHref     string // where the apps region reloads itself from
	RailHref     string
}

// projectData is what both regions are made of: the project, the environment the
// page is about and the apps, read once per request.
type projectData struct {
	Project service.Project
	Env     string
	Places  places
	InEnv   []service.AppSummary
	// ByEnv is every app of the project by environment, for the Promote rows.
	ByEnv map[string][]service.AppSummary
}

// chooseEnv is the environment a page is about: the one ?env= names when the
// project has it, else production when it has that, else its first. "" when the
// project has none.
func chooseEnv(p service.Project, asked string) string {
	for _, e := range p.Environments {
		if e.Name == asked {
			return asked
		}
	}
	for _, e := range p.Environments {
		if e.Name == "production" {
			return e.Name
		}
	}
	if len(p.Environments) > 0 {
		return p.Environments[0].Name
	}
	return ""
}

// loadProjectData reads the project and its apps. An unknown project is
// service.ErrProjectNotFound.
func (s *server) loadProjectData(ctx context.Context, id, asked string) (projectData, error) {
	p, err := s.svc.Project(ctx, id)
	if err != nil {
		return projectData{}, err
	}
	sums, err := s.svc.ListAppsIn(ctx, id, "")
	if err != nil {
		return projectData{}, err
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return projectData{}, err
	}
	d := projectData{Project: p, Env: chooseEnv(p, asked), Places: pl, ByEnv: map[string][]service.AppSummary{}}
	for _, sum := range sums {
		d.ByEnv[sum.Place.Environment] = append(d.ByEnv[sum.Place.Environment], sum)
	}
	d.InEnv = d.ByEnv[d.Env]
	return d, nil
}

// copyMarksOf is the Copies cell of an app: the healthy copies of each service
// first, then the others with the app's kind (rolling while it rolls out, warning
// when it is short of copies, failed when nothing runs, idle when it is stopped),
// from the counts the app's status already holds (no call to Nomad for a list).
func copyMarksOf(sum service.AppSummary) markCell {
	st := sum.Status
	total := 0
	for _, svc := range st.Services {
		total += svc.Wanted
	}
	if !sum.App.Defined() || total == 0 || (sum.LastDeployment == nil && st.State == service.StateNotRunning) {
		return markCell{None: true, Words: "none"}
	}
	if st.State == service.StateUnknown {
		// Nomad can't be read: rings would say "stopped", which isn't known.
		return markCell{None: true, Words: "unknown"}
	}
	rest := "idle"
	switch st.State {
	case service.StateRollingOut:
		rest = "rolling"
	case service.StateHealthy, service.StateDegraded:
		rest = "warning"
	case service.StateNotRunning:
		rest = "failed"
	}
	cell := markCell{}
	healthy := 0
	multi := len(st.Services) > 1
	for _, svc := range st.Services {
		h := min(svc.Healthy, svc.Wanted)
		healthy += h
		g := markGroup{}
		if multi {
			g.Service = svc.Name
		}
		for range h {
			g.Kinds = append(g.Kinds, "healthy")
		}
		for range svc.Wanted - h {
			g.Kinds = append(g.Kinds, rest)
		}
		cell.Groups = append(cell.Groups, g)
	}
	cell.Words = fmt.Sprintf("%d of %s healthy", healthy, copyCount(total))
	if total > maxMarks {
		cell.Groups, cell.Count = nil, copyCount(total)
	}
	return cell
}

// screenRowOf is an app's row of the project's table.
func (s *server) screenRowOf(now time.Time, sum service.AppSummary) screenRow {
	row := screenRow{appRow: s.appRow(now, sum, false), Marks: copyMarksOf(sum)}
	if len(row.Domains) > 0 {
		row.Address, row.More = row.Domains[0].Host, len(row.Domains)-1
	}
	return row
}

// projectAppsOf builds the apps region: the environment's rows, problems first.
func (s *server) projectAppsOf(d projectData) projectApps {
	now := s.now()
	region := projectApps{Env: d.Env, AddEnvHref: "/projects/" + url.PathEscape(d.Project.ID) + "/settings#add-environment"}
	for _, sum := range d.InEnv {
		region.Rows = append(region.Rows, s.screenRowOf(now, sum))
	}
	sortProblemsFirst(region.Rows, func(row screenRow) string { return row.Sort })
	region.Count = len(region.Rows)
	if s.newApp && d.Env != "" {
		region.NewHref = newAppHere(d.Project.ID, d.Env)
	}
	return region
}

// newAppHere is New app with a project's environment chosen.
func newAppHere(project, environment string) string {
	return "/apps/new?project=" + url.QueryEscape(project) + "&environment=" + url.QueryEscape(environment)
}

// addressCount is how many distinct domains the apps have.
func addressCount(sums []service.AppSummary) int {
	seen := map[string]bool{}
	for _, sum := range sums {
		for _, dl := range domainLinks(sum.App) {
			seen[dl.Host] = true
		}
	}
	return len(seen)
}

// promoteRowsOf is, for each other environment of the project, the apps of this one
// that can be promoted there: the header's cheap test (promotable) and an app of the
// same name in the target (promotion needs a target; Copy to is the app header's
// job). The promotion plan is never run to draw a page.
func promoteRowsOf(d projectData) []promoteRow {
	var rows []promoteRow
	for _, env := range d.Project.Environments {
		if env.Name == d.Env {
			continue
		}
		there := map[string]bool{}
		for _, sum := range d.ByEnv[env.Name] {
			there[cmpName(sum)] = true
		}
		row := promoteRow{Env: env.Name}
		for _, sum := range d.InEnv {
			if promotable(sum) && there[cmpName(sum)] {
				row.Apps = append(row.Apps, promoteLink{Name: cmpName(sum),
					Href: "/apps/" + url.PathEscape(sum.App.ID) + "/promote?to=" + url.QueryEscape(env.Name)})
			}
		}
		if len(row.Apps) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// projectRailOf builds the rail: the environment's needs-you items, the Environment
// panel and its newest deployments.
func (s *server) projectRailOf(r *http.Request, d projectData) (projectRail, error) {
	rail := projectRail{Env: d.Env}
	if d.Env == "" {
		return rail, nil
	}
	ctx := r.Context()
	project, env := d.Project.ID, d.Env
	// Items with no app (a missing backup destination, a full disk) are the host's.
	region, _, err := s.loadAttentionWhere(r, d.Places, d.InEnv, func(it service.AttentionItem) bool {
		return it.AppID != "" && it.Project == project && it.Environment == env
	})
	if err != nil {
		return projectRail{}, fmt.Errorf("read what needs you in %s/%s: %w", project, env, err)
	}
	for i := range region.Rows {
		region.Rows[i].Text = withoutPlace(region.Rows[i].Text)
	}
	if len(region.Rows) > 0 {
		rail.Attention = region
	}

	if n := addressCount(d.InEnv); n > 0 {
		rail.Addresses = fmt.Sprintf("%d", n)
	} else {
		rail.Addresses = "none"
	}
	rail.Promote = promoteRowsOf(d)

	recs, err := s.svc.RecentDeploymentsIn(ctx, project, env, projectDeploysLimit)
	if err != nil {
		return projectRail{}, fmt.Errorf("list recent deployments of %s/%s: %w", project, env, err)
	}
	now := s.now()
	if len(recs) > 0 {
		rail.HasDeploy = true
		rail.DeployWho = capitalized(deployWho(recs[0], false))
		rail.DeployWhen = whenOf(now, &recs[0].CreatedAt, "")
	}
	for _, rec := range recs {
		row := s.activityRowOf(now, rec)
		row.App = cmp.Or(rec.Place.Name, rec.AppID) // the label says where: no Where prefix
		rail.Activity = append(rail.Activity, row)
	}
	return rail, nil
}

// withoutPlace is a needs-you sentence without the place before the app's name
// ("Shop › staging › "): the rail's label already says where. The place is the
// sentence's muted parts and nothing else is muted (service.TextPart.Muted).
func withoutPlace(s sentence) sentence {
	out := make(sentence, 0, len(s))
	for _, p := range s {
		if !p.Muted {
			out = append(out, p)
		}
	}
	return out
}

// projectScreenOf is the whole page.
func (s *server) projectScreenOf(r *http.Request, d projectData) (*projectScreen, error) {
	rail, err := s.projectRailOf(r, d)
	if err != nil {
		return nil, err
	}
	id := d.Project.ID
	base := "/projects/" + url.PathEscape(id)
	v := &projectScreen{ID: id, Display: d.Project.DisplayName, Env: d.Env,
		SettingsHref: base + "/settings", AddEnvHref: base + "/settings#add-environment",
		Apps: s.projectAppsOf(d), Rail: rail}
	// The slot holds the project's name when the display name says something else (Default
	// is "default" in other letters: nothing to add).
	if !strings.EqualFold(d.Project.DisplayName, id) {
		v.Slot = id
	}
	if s.newApp && d.Env != "" {
		v.NewHref = newAppHere(id, d.Env)
	}
	for _, e := range d.Project.Environments {
		v.Tabs = append(v.Tabs, envTab{Name: e.Name, Href: base + "?env=" + url.QueryEscape(e.Name), Current: e.Name == d.Env})
	}
	query := "?env=" + url.QueryEscape(d.Env)
	v.AppsHref = "/parts/projects/" + url.PathEscape(id) + "/apps" + query
	v.RailHref = "/parts/projects/" + url.PathEscape(id) + "/rail" + query
	return v, nil
}

// showProject answers GET /projects/{project} (?env=<environment>). ?created=1 is
// the calm sentence after New project redirects here.
func (s *server) showProject(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("project")
	d, err := s.loadProjectData(r.Context(), id, r.URL.Query().Get("env"))
	if errors.Is(err, service.ErrProjectNotFound) {
		s.projectGone(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read project", err)
		return
	}
	screen, err := s.projectScreenOf(r, d)
	if err != nil {
		s.serverError(w, r, "build project page", err)
		return
	}
	if r.URL.Query().Get("created") == "1" {
		screen.Notice = sentence{plain("Project "), mono(d.Project.DisplayName), plain(" created with a production environment.")}
	}
	head, err := s.shellPage(r, d.Project.DisplayName, navProjects, crumb{Label: d.Project.DisplayName})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	head.LiveURL = "/live"
	s.render(w, http.StatusOK, "projects", projectsPage{pageData: head, Screen: screen})
}

// partProjectApps answers GET /parts/projects/{project}/apps?env=: the apps region.
func (s *server) partProjectApps(w http.ResponseWriter, r *http.Request, session string) {
	d, ok := s.partProjectData(w, r)
	if !ok {
		return
	}
	s.renderPart(w, "projects", "project-apps-region", s.projectAppsOf(d))
}

// partProjectRail answers GET /parts/projects/{project}/rail?env=: the rail region.
func (s *server) partProjectRail(w http.ResponseWriter, r *http.Request, session string) {
	d, ok := s.partProjectData(w, r)
	if !ok {
		return
	}
	rail, err := s.projectRailOf(r, d)
	if err != nil {
		s.log.Error("build the project rail", "project", d.Project.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "projects", "project-rail-region", rail)
}

// partProjectData reads what a project's parts are made of; an unknown project is
// 404. It reports false after answering.
func (s *server) partProjectData(w http.ResponseWriter, r *http.Request) (projectData, bool) {
	d, err := s.loadProjectData(r.Context(), r.PathValue("project"), r.URL.Query().Get("env"))
	switch {
	case errors.Is(err, service.ErrProjectNotFound):
		http.NotFound(w, r)
		return projectData{}, false
	case err != nil:
		s.log.Error("read project", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return projectData{}, false
	}
	return d, true
}
