package ui

import (
	"net/http"
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
	Href    string
	Status  statusView
	Sort    string // the kind the table sorts by: the status, or failed when the newest deploy failed
	Version string // the live version, "v7"; "" when none is running
	Images  []serviceImage
	Domains []domainLink
	Deploy  *deployCell // nil if never deployed
}

// activityRow is one entry of the activity rail: a deployment of some app.
type activityRow struct {
	App     string
	Href    string
	Version string
	Outcome statusView
	Trigger string // in words: "from the console"
	When    when
}

// appsRegion is what the "apps" region draws: the headline, the needs-you
// list and the table, or the empty state when there is no app. The needs-you
// list is a region of its own too (it refreshes by itself on "attention"),
// drawn here so that a load of the page and a refresh of this region agree.
type appsRegion struct {
	Headline  sentence
	Attention attentionRegion
	Rows      []appRow
	Empty     emptyView
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
	NewApp  bool   // the top bar offers "New app"
	Deleted string // an app ID to say was deleted, from ?deleted=
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
	head, err := s.shellPage(r, "Apps", "apps", crumb{Label: "Apps"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live"
	page := appsPage{pageData: head, appsRegion: region, activityRegion: activity, NewApp: s.newApp}
	if id := r.URL.Query().Get("deleted"); deletedID.MatchString(id) {
		page.Deleted = id
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

// liveApps answers GET /live: the stream of the Apps page.
func (s *server) liveApps(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, liveRoute{Kinds: []service.ChangeKind{service.ChangeApps, service.ChangeActivity, service.ChangeAttention}})
}

// loadApps reads every app and builds the headline, the needs-you list and
// the rows, problems first.
func (s *server) loadApps(r *http.Request) (appsRegion, error) {
	sums, err := s.svc.ListApps(r.Context())
	if err != nil {
		return appsRegion{}, err
	}
	attention, items, err := s.loadAttention(r)
	if err != nil {
		return appsRegion{}, err
	}
	now := s.now()
	rows := make([]appRow, 0, len(sums))
	for _, sum := range sums {
		rows = append(rows, s.appRow(now, sum))
	}
	sortProblemsFirst(rows, func(row appRow) string { return row.Sort })

	region := appsRegion{Headline: headlineOf(sums, items), Attention: attention, Rows: rows,
		Empty: emptyView{Title: "Deploy an image and it shows up here."}}
	if s.newApp {
		region.Empty.ActionLabel, region.Empty.ActionHref = "New app", "/apps/new"
	}
	return region, nil
}

func (s *server) appRow(now time.Time, sum service.AppSummary) appRow {
	row := appRow{
		ID:      sum.App.ID,
		Href:    "/apps/" + sum.App.ID,
		Status:  appStatusView(sum),
		Version: versionLabel(sum.Status.LiveVersion),
		Domains: domainLinks(sum.App),
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
	now := s.now()
	rows := make([]activityRow, 0, len(recs))
	for _, rec := range recs {
		rows = append(rows, s.activityRow(now, rec))
	}
	return activityRegion{Activity: rows}, nil
}

func (s *server) activityRow(now time.Time, rec store.DeploymentRecord) activityRow {
	return activityRow{
		App:     rec.AppID,
		Href:    s.deploymentsHref(rec.AppID),
		Version: versionLabel(rec.JobVersion),
		Outcome: deploymentStatusView(rec.Status),
		Trigger: triggerWords(rec.Deployment.Trigger),
		When:    whenOf(now, &rec.CreatedAt, ""),
	}
}
