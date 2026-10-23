package ui

import (
	"cmp"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// recentLimit is how many deployments the activity rail lists.
const recentLimit = 20

// serviceImage is one service's image in the apps table. Service is set only
// when the app has several, so each line says which service it belongs to.
type serviceImage struct {
	Service string
	Image   string
}

// domainLink is an app's public address as a link.
type domainLink struct {
	Host string
	URL  string // https://<host>
}

// deployCell is the last-deploy column: when, and how it ended if not well.
type deployCell struct {
	When    when
	Outcome string // "v8 reverted"; "" when there is nothing to add to the status
	Href    string // the deployment's entry on the Deployments tab, for an Outcome
	Failed  bool   // the deployment failed: the outcome carries the failed mark
}

// appRow is one line of the apps table.
type appRow struct {
	ID      string
	Name    string // what the row shows: the app's name in its environment (its ID at home)
	Href    string
	Status  statusView
	Sort    string // the kind the table sorts by: the status, or failed when the newest deploy failed
	Version string // the live version, "v7"; "" when none is running
	Next    string // the version rolling out, "v8", while the app rolls out; else ""
	Images  []serviceImage
	Domains []domainLink
	Deploy  *deployCell // nil if never deployed
	// Kind is the word after the name (compose, database, volume; "" for an image
	// app) and State the filter's state of the app (stateNeeds, ...; "").
	Kind  string
	State string
}

// activityRow is one entry of the activity rail: a deployment of some app.
// Where is "<project display> › <environment>" for an app outside Default ›
// production, and then App is the app's name; at home App is its ID.
type activityRow struct {
	Where   string
	App     string
	Href    string
	Version string
	Outcome statusView
	Who     sentence // "from the console", "pushed to main by ana · 4e0a77", "through the API by ci-github"
	When    when
}

// appsRegion is what the "apps" region draws: the headline, the needs-you
// list and the table, or the empty state when there is no app. The needs-you
// list is a region of its own too (it refreshes by itself on "attention"),
// drawn here so that a load of the page and a refresh of this region agree.
//
// While the installation has one project with one environment the apps are
// Rows, as before projects. Otherwise they are Groups: a section per project
// and a table per environment (feature 019).
type appsRegion struct {
	Headline  sentence
	Attention attentionRegion
	// Deploying is the "Deploying now" cards, a live region of its own inside this one.
	Deploying deployingRegion
	// Filter is the filter row (drawn with more than 5 apps); NoMatch says it left
	// nothing. RefreshHref is where the region reloads itself from, with the filter.
	Filter      filterView
	NoMatch     bool
	RefreshHref string
	Rows        []appRow
	Groups      []projectGroup
	Empty       emptyView
}

// projectGroup is a project's section of the grouped Apps page.
type projectGroup struct {
	Display string
	Href    string // the project's page
	Envs    []envGroup
}

// envGroup is an environment's apps in the grouped Apps page. NewHref leads to
// New app with this environment chosen; it is empty when New app isn't offered.
type envGroup struct {
	Name    string
	Rows    []appRow
	NewHref string
}

// activityRegion is what the "activity" region draws.
type activityRegion struct {
	Activity []activityRow
}

// appsPage is the whole Apps page. It embeds both regions' data, so the same
// templates draw a region inside the page and, alone, as its refresh. (Go:
// embedding promotes the inner struct's fields, so a template reads
// .Headline whether it was given an appsRegion or an appsPage. C#: like
// inheriting from both, minus the inheritance.)
type appsPage struct {
	pageData
	appsRegion
	activityRegion
	Deleted string // an app ID to say was deleted, from ?deleted=
	// ProjectDeleted is a project name to say was deleted, from ?projectDeleted=.
	ProjectDeleted string
}

// deletedID matches what an app ID looks like; only that is echoed back from
// ?deleted=, so the notice can't be made to say anything else.
var deletedID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// showApps answers GET /apps.
func (s *server) showApps(w http.ResponseWriter, r *http.Request, session string) {
	region, err := s.loadApps(r)
	if err != nil {
		s.serverError(w, "list apps", err)
		return
	}
	activity, err := s.loadActivity(r)
	if err != nil {
		s.serverError(w, "list recent deployments", err)
		return
	}
	head, err := s.shellPage(r, "Apps", navHome, crumb{Label: "Home"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live"
	page := appsPage{pageData: head, appsRegion: region, activityRegion: activity}
	if id := r.URL.Query().Get("deleted"); deletedID.MatchString(id) {
		page.Deleted = id
	}
	if name := r.URL.Query().Get("projectDeleted"); labelName.MatchString(name) {
		page.ProjectDeleted = name
	}
	s.render(w, http.StatusOK, "apps", page)
}

// partApps answers GET /parts/apps: the headline, the needs-you list and the table.
func (s *server) partApps(w http.ResponseWriter, r *http.Request, session string) {
	region, err := s.loadApps(r)
	if err != nil {
		s.log.Error("list apps", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "apps", "apps-region", region)
}

// partActivity answers GET /parts/activity: the activity rail.
func (s *server) partActivity(w http.ResponseWriter, r *http.Request, session string) {
	region, err := s.loadActivity(r)
	if err != nil {
		s.log.Error("list recent deployments", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "apps", "activity-region", region)
}

// liveApps answers GET /live: the stream of the workspace pages (Apps, and
// Schedules, which listens for the schedules notice).
func (s *server) liveApps(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, liveRoute{Kinds: []service.ChangeKind{service.ChangeApps, service.ChangeActivity, service.ChangeAttention,
		service.ChangeDeployments, service.ChangeCopies, service.ChangeSchedules}})
}

// loadApps reads every app and builds the headline, the needs-you list and
// the rows, problems first. While there is one project with one environment
// the rows are one table; otherwise each environment has its own, under its
// project's heading.
func (s *server) loadApps(r *http.Request) (appsRegion, error) {
	ctx := r.Context()
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return appsRegion{}, err
	}
	sums, err := s.svc.ListApps(ctx)
	if err != nil {
		return appsRegion{}, err
	}
	attention, items, err := s.loadAttention(r, pl, sums)
	if err != nil {
		return appsRegion{}, err
	}
	now := s.now()
	// The apps the headline names as needing the user: the state filter's "Needs you".
	listed := map[string]bool{}
	for _, it := range items {
		listed[it.AppID] = true
	}
	needs := map[string]bool{}
	for _, sum := range sums {
		needs[sum.App.ID] = needsYouOf(sum, listed[sum.App.ID])
	}
	// The filter is drawn, and obeyed, only with more than a few apps.
	filter := filterOf(r)
	if len(sums) <= filterMinApps {
		filter = appsFilter{}
	}
	region := appsRegion{Headline: headlineOf(sums, items), Attention: attention,
		Deploying:   s.loadDeploying(ctx, sums, pl),
		Filter:      filterViewOf(filter, len(sums)),
		RefreshHref: "/parts/apps" + filter.Query(),
		Empty:       emptyView{Title: "Deploy an image and it shows up here."}}
	// The top bar's "New app" is the page's one primary action, so the empty
	// state doesn't repeat it.

	// The rows that pass the filter. The headline and the needs-you list above
	// are about the whole caravana and never filtered.
	if pl.single() {
		rows := make([]appRow, 0, len(sums))
		for _, sum := range sums {
			if row := s.appRow(now, sum, needs[sum.App.ID]); filter.matches(sum, row) {
				rows = append(rows, row)
			}
		}
		sortProblemsFirst(rows, func(row appRow) string { return row.Sort })
		region.Rows = rows
		region.NoMatch = filter.Active() && len(rows) == 0
		return region, nil
	}

	matched := 0
	byEnv := map[app.Place][]appRow{} // keyed by project and environment: the name is left empty
	for _, sum := range sums {
		row := s.appRow(now, sum, needs[sum.App.ID])
		if !filter.matches(sum, row) {
			continue
		}
		matched++
		key := app.Place{Project: sum.Place.Project, Environment: sum.Place.Environment}
		byEnv[key] = append(byEnv[key], row)
	}
	for _, pr := range pl.Projects {
		group := projectGroup{Display: pr.DisplayName, Href: "/projects/" + url.PathEscape(pr.ID)}
		for _, env := range pr.Environments {
			rows := byEnv[app.Place{Project: pr.ID, Environment: env.Name}]
			if filter.Active() && len(rows) == 0 {
				continue // the groups hold only what matches
			}
			sortProblemsFirst(rows, func(row appRow) string { return row.Sort })
			eg := envGroup{Name: env.Name, Rows: rows}
			if s.newApp {
				eg.NewHref = "/apps/new?project=" + url.QueryEscape(pr.ID) + "&environment=" + url.QueryEscape(env.Name)
			}
			group.Envs = append(group.Envs, eg)
		}
		if filter.Active() && len(group.Envs) == 0 {
			continue
		}
		region.Groups = append(region.Groups, group)
	}
	region.NoMatch = filter.Active() && matched == 0
	return region, nil
}

// appRow is an app's line of the table. needsYou says the app has an item in the
// needs-you list.
func (s *server) appRow(now time.Time, sum service.AppSummary, needsYou bool) appRow {
	row := appRow{
		ID:      sum.App.ID,
		Name:    cmp.Or(sum.Place.Name, sum.App.ID),
		Href:    "/apps/" + sum.App.ID,
		Status:  appStatusView(sum),
		Version: versionLabel(sum.Status.LiveVersion),
		Domains: domainLinks(sum.App),
		Kind:    kindOf(sum.App, sum.Compose != nil),
	}
	row.State = stateKey(sum, row.Status, needsYou)
	if isRollingOut(sum) {
		if next := versionLabel(sum.LastDeployment.JobVersion); next != row.Version {
			row.Next = next
		}
	}
	// Healthy copies don't hide a failed deploy: the row sorts with the problems.
	row.Sort = row.Status.Kind
	if lastDeployFailed(sum) {
		row.Sort = "failed"
	}
	multi := len(sum.App.Services) > 1
	for _, svc := range sum.App.Services {
		img := serviceImage{Image: svc.Image}
		if multi {
			img.Service = svc.Name
		}
		row.Images = append(row.Images, img)
	}
	if d := sum.LastDeployment; d != nil {
		row.Deploy = &deployCell{When: whenOf(now, &d.CreatedAt, ""), Outcome: deployOutcome(d), Failed: d.Status == store.StatusFailed}
		if row.Deploy.Outcome != "" {
			row.Deploy.Href = s.deploymentsHref(sum.App.ID)
			if s.hasTab("deployments") {
				row.Deploy.Href += "#deployment-" + d.Deployment.ID
			}
		}
	}
	return row
}

// domainLinks lists every service's domains as https links.
func domainLinks(a app.App) []domainLink {
	var out []domainLink
	for _, svc := range a.Services {
		for _, d := range svc.Domains {
			out = append(out, domainLink{Host: d.Host, URL: "https://" + d.Host})
		}
	}
	return out
}

// loadActivity reads the newest deployments across all apps.
func (s *server) loadActivity(r *http.Request) (activityRegion, error) {
	recs, err := s.svc.RecentDeployments(r.Context(), recentLimit)
	if err != nil {
		return activityRegion{}, err
	}
	pl, err := s.loadPlaces(r.Context())
	if err != nil {
		return activityRegion{}, err
	}
	now := s.now()
	rows := make([]activityRow, 0, len(recs))
	for _, rec := range recs {
		rows = append(rows, s.activityRow(now, rec, pl))
	}
	return activityRegion{Activity: rows}, nil
}

func (s *server) activityRow(now time.Time, rec store.DeploymentRecord, pl places) activityRow {
	row := s.activityRowOf(now, rec)
	if !atHome(rec.Place) {
		row.Where, row.App = pl.display(rec.Place.Project)+" › "+rec.Place.Environment, rec.Place.Name
	}
	return row
}

func (s *server) activityRowOf(now time.Time, rec store.DeploymentRecord) activityRow {
	return activityRow{
		App:     rec.AppID,
		Href:    s.deploymentsHref(rec.AppID),
		Version: versionLabel(rec.JobVersion),
		Outcome: deploymentStatusView(rec.Status),
		Who:     deployWho(rec, true),
		When:    whenOf(now, &rec.CreatedAt, ""),
	}
}
