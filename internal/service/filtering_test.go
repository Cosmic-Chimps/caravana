package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const blogID = "blog--production--web"

// twoProjects is an installation with project shop (production and staging, both deployed, production
// with a domain and a backup) and project blog (its production deployed): the places feature 053's
// filtering is told apart by.
func twoProjects(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	e := shopEnv(t)
	both(t, e)
	if _, err := e.svc.CreateProject(ctx, "blog", ""); err != nil {
		t.Fatal(err)
	}
	deployHealthy(t, e, blogApp())
	return e
}

// blogApp is blog's production web, with a domain and a backup.
func blogApp() app.App {
	b := whoamiAs(blogID)
	b.Services[0].Env = []app.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pg"}}
	b.Services[0].Domains = []app.Domain{{Host: "blog.example.com", Port: "http"}}
	b.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres, Keep: 3}
	return b
}

// seesAll is every vantage that is not filtered: Caravana itself, an owner and an admin.
func seesAll() map[string]context.Context {
	return map[string]context.Context{
		"no principal": context.Background(),
		"owner":        asPerson(Principal{PersonID: "o", OrgRole: OrgOwner}),
		"admin":        asPerson(Principal{PersonID: "a", OrgRole: OrgAdmin}),
	}
}

func TestListAppsFilters(t *testing.T) {
	e := twoProjects(t)
	all := []string{blogID, productionID, stagingID}

	for name, ctx := range seesAll() {
		sums, err := e.svc.ListApps(ctx)
		if err != nil || !slices.Equal(summaryIDs(sums), all) {
			t.Errorf("%s: ListApps = %v, %v; want all three", name, summaryIDs(sums), err)
		}
	}

	// A member with deploy on staging and view on production of shop sees exactly those two.
	sums, err := e.svc.ListApps(asPerson(shopMember()))
	if err != nil || !slices.Equal(summaryIDs(sums), []string{productionID, stagingID}) {
		t.Errorf("member: ListApps = %v, %v; want shop's two", summaryIDs(sums), err)
	}
	// An override of none hides one environment.
	hidden := shopMember()
	hidden.Roles[0].Environments["production"] = RoleNone
	if sums, _ := e.svc.ListApps(asPerson(hidden)); !slices.Equal(summaryIDs(sums), []string{stagingID}) {
		t.Errorf("production none: ListApps = %v, want staging only", summaryIDs(sums))
	}
	// A place that is asked for by name is filtered too, not an error that tells it exists.
	if sums, err := e.svc.ListAppsIn(asPerson(shopMember()), "blog", ""); err != nil || len(sums) != 0 {
		t.Errorf("member: ListAppsIn(blog) = %v, %v; want an empty list", summaryIDs(sums), err)
	}
	// Someone with no roles sees nothing, and the lists are empty, not nil-errors.
	nobody := Principal{PersonID: "n", OrgRole: OrgMember}
	if sums, err := e.svc.ListApps(asPerson(nobody)); err != nil || len(sums) != 0 {
		t.Errorf("no roles: ListApps = %v, %v", summaryIDs(sums), err)
	}
	// The other cross-app lists of apps.
	if recs, _ := e.svc.StoredApps(asPerson(shopMember())); len(recs) != 2 {
		t.Errorf("member: StoredApps = %d, want 2", len(recs))
	}
	if recs, _ := e.svc.StoredApps(context.Background()); len(recs) != 3 {
		t.Errorf("no principal: StoredApps = %d, want 3", len(recs))
	}
}

func TestAllCopiesFilters(t *testing.T) {
	e := twoProjects(t)
	got, err := e.svc.AllCopies(asPerson(shopMember()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[blogID]; ok || len(got) != 2 {
		t.Errorf("member: AllCopies has %d apps (blog: %v), want shop's two", len(got), ok)
	}
	if all, _ := e.svc.AllCopies(context.Background()); len(all) != 3 {
		t.Errorf("no principal: AllCopies has %d apps, want 3", len(all))
	}
}

func TestProjectsFilter(t *testing.T) {
	e := twoProjects(t)
	names := func(ps []Project) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}

	for name, ctx := range seesAll() {
		ps, err := e.svc.Projects(ctx)
		if err != nil || !slices.Equal(names(ps), []string{"default", "shop", "blog"}) {
			t.Errorf("%s: Projects = %v, %v; want default, shop and blog (the order they were made)", name, names(ps), err)
		}
	}

	ps, err := e.svc.Projects(asPerson(shopMember()))
	if err != nil || !slices.Equal(names(ps), []string{"shop"}) || !slices.Equal(envNames(ps[0]), []string{"production", "staging"}) {
		t.Errorf("member: Projects = %v, %v; want shop with both environments", names(ps), err)
	}
	// Only the environments they can see.
	staging := shopMember()
	staging.Roles[0].Environments["production"] = RoleNone
	ps, _ = e.svc.Projects(asPerson(staging))
	if len(ps) != 1 || !slices.Equal(envNames(ps[0]), []string{"staging"}) {
		t.Errorf("production none: Projects = %v / %v; want shop with staging only", names(ps), ps)
	}
	// A project none of whose environments they see is gone: not in the list, and not found.
	none := Principal{PersonID: "n", OrgRole: OrgMember, Roles: []ProjectRole{{Project: "shop", Role: RoleView,
		Environments: map[string]Role{"production": RoleNone, "staging": RoleNone}}}}
	if ps, _ := e.svc.Projects(asPerson(none)); len(ps) != 0 {
		t.Errorf("every environment none: Projects = %v, want none", names(ps))
	}
	if _, err := e.svc.Project(asPerson(none), "shop"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Project(shop) with every environment none = %v, want ErrProjectNotFound", err)
	}
	if _, err := e.svc.Project(asPerson(shopMember()), "blog"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Project(blog) as a member of shop = %v, want ErrProjectNotFound (the same as a project that doesn't exist)", err)
	}
	if _, err := e.svc.Project(asPerson(shopMember()), "nothing"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Project(nothing) = %v, want ErrProjectNotFound", err)
	}
	if p, err := e.svc.Project(asPerson(staging), "shop"); err != nil || !slices.Equal(envNames(p), []string{"staging"}) {
		t.Errorf("Project(shop) = %v, %v; want staging only", envNames(p), err)
	}
	if p, err := e.svc.Project(context.Background(), "blog"); err != nil || p.ID != "blog" {
		t.Errorf("Project(blog) with no principal = %v, %v", p.ID, err)
	}
}

func TestRecentDeploymentsFilter(t *testing.T) {
	ctx := context.Background()
	e := twoProjects(t) // blog's deployment is the newest
	all, err := e.svc.RecentDeployments(ctx, 50)
	if err != nil || len(all) != 3 || all[0].AppID != blogID {
		t.Fatalf("RecentDeployments = %d rows, first %v, %v; want three, blog's newest", len(all), all[0].AppID, err)
	}
	for name, ctx := range seesAll() {
		if got, _ := e.svc.RecentDeployments(ctx, 50); len(got) != 3 {
			t.Errorf("%s: %d deployments, want 3", name, len(got))
		}
	}

	member := asPerson(shopMember())
	got, err := e.svc.RecentDeployments(member, 50)
	if err != nil || len(got) != 2 {
		t.Fatalf("member: %d deployments, %v; want shop's two", len(got), err)
	}
	for _, r := range got {
		if r.Place.Project != "shop" {
			t.Errorf("member sees a deployment of %s", r.AppID)
		}
	}
	// A page of one is still filled with the newest the member may see, though the newest overall is blog's.
	one, err := e.svc.RecentDeployments(member, 1)
	if err != nil || len(one) != 1 || one[0].Place.Project != "shop" || one[0].AppID != got[0].AppID {
		t.Errorf("member: page of 1 = %v, %v; want shop's newest", one, err)
	}
	if rows, _ := e.svc.RecentDeploymentsIn(member, "blog", "production", 10); len(rows) != 0 {
		t.Errorf("member: RecentDeploymentsIn(blog) = %d rows, want none", len(rows))
	}
	if rows, _ := e.svc.RecentDeploymentsIn(member, "shop", "staging", 10); len(rows) != 1 {
		t.Errorf("member: RecentDeploymentsIn(shop, staging) = %d rows, want 1", len(rows))
	}
	// Many invisible rows newer than the one visible row don't hide it.
	for range 12 {
		e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
		deployHealthy(t, e, blogApp())
	}
	if got, _ := e.svc.RecentDeployments(member, 2); len(got) != 2 {
		t.Errorf("member: page of 2 behind twelve of blog's = %d rows, want 2", len(got))
	}
}

func TestAttentionFilters(t *testing.T) {
	ctx := context.Background()
	e := twoProjects(t)
	// A failed deployment in shop's staging and in blog: Nomad refuses to register the job.
	failing := func(a app.App) {
		t.Helper()
		e.nomad.RejectNext(500, "no room")
		if _, err := e.svc.Deploy(ctx, a, app.TriggerManual); err == nil {
			t.Fatalf("a deploy of %s that Nomad refused succeeded", a.ID)
		}
	}
	failing(staging())
	failing(blogApp())

	byApp := func(att Attention) map[string]bool {
		out := map[string]bool{}
		for _, it := range att.Items {
			out[it.AppID] = true
		}
		return out
	}
	full, err := e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := byApp(full); !got[stagingID] || !got[blogID] {
		t.Fatalf("no principal: items %v; want shop's staging and blog", got)
	}
	for name, c := range seesAll() {
		if a, _ := e.svc.Attention(c); len(a.Items) != len(full.Items) {
			t.Errorf("%s: %d items, want all %d", name, len(a.Items), len(full.Items))
		}
	}
	mine, err := e.svc.Attention(asPerson(shopMember()))
	if err != nil {
		t.Fatal(err)
	}
	got := byApp(mine)
	if !got[stagingID] || got[blogID] {
		t.Errorf("member: items %v; want shop's staging and not blog", got)
	}
	for _, it := range mine.Items {
		if it.AppID == "" {
			t.Errorf("member sees an item of the installation: %s", it.Kind)
		}
	}
}

func TestPreviewsFilter(t *testing.T) {
	ctx := context.Background()
	e := twoProjects(t)
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "pr-7"); err != nil {
		t.Fatal(err)
	}
	deployHealthy(t, e, whoamiAs("shop--pr-7--web"))

	list, err := e.svc.Previews(asPerson(shopMember()), "shop")
	if err != nil || len(list.Previews) != 1 || list.Previews[0].Environment != "pr-7" || len(list.Previews[0].Apps) != 1 {
		t.Fatalf("member: Previews(shop) = %+v, %v; want pr-7 with its app (a preview takes the project role)", list.Previews, err)
	}
	if _, err := e.svc.Previews(asPerson(shopMember()), "blog"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("member: Previews(blog) = %v, want ErrProjectNotFound", err)
	}
	// With an override of none on every environment, the project is gone, even for its previews.
	none := Principal{PersonID: "n", OrgRole: OrgMember, Roles: []ProjectRole{{Project: "shop", Role: RoleView,
		Environments: map[string]Role{"production": RoleNone, "staging": RoleNone}}}}
	if list, err := e.svc.Previews(asPerson(none), "shop"); err != nil || len(list.Previews) != 1 {
		// A preview takes the project role, which is view: the project is visible through it.
		t.Errorf("member with every named environment none: Previews = %+v, %v; want the preview (project role view)", list.Previews, err)
	}
	if list, err := e.svc.Previews(context.Background(), "shop"); err != nil || len(list.Previews) != 1 {
		t.Errorf("no principal: Previews = %+v, %v", list.Previews, err)
	}
}

func TestSchedulesAndDomainsFilter(t *testing.T) {
	e := twoProjects(t)
	member := asPerson(shopMember())

	scheds, err := e.svc.ListSchedules(member, ScheduleFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scheds {
		if p, _ := app.PlaceOf(s.App); p.Project != "shop" {
			t.Errorf("member sees a schedule of %s", s.App)
		}
	}
	if len(scheds) != 1 { // shop's production has the one backup; staging none
		t.Errorf("member: %d schedules, want shop's one built-in backup", len(scheds))
	}
	if all, _ := e.svc.ListSchedules(context.Background(), ScheduleFilter{}); len(all) != 2 {
		t.Errorf("no principal: %d schedules, want shop's and blog's", len(all))
	}
	blogOnly := asPerson(Principal{PersonID: "b", OrgRole: OrgMember, Roles: []ProjectRole{{Project: "blog", Role: RoleView}}})
	if got, _ := e.svc.ListSchedules(blogOnly, ScheduleFilter{Project: "shop"}); len(got) != 0 {
		t.Errorf("a blog member asking for shop's schedules gets %d", len(got))
	}

	hosts := func(c context.Context) []string {
		t.Helper()
		views, err := e.svc.Domains(c, DomainFilter{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range views {
			out = append(out, v.Host)
		}
		slices.Sort(out)
		return out
	}
	if got := hosts(member); !slices.Equal(got, []string{"shop.example.com", "staging.example.com"}) {
		t.Errorf("member: domains = %v, want shop's two", got)
	}
	if got := hosts(context.Background()); !slices.Equal(got, []string{"blog.example.com", "shop.example.com", "staging.example.com"}) {
		t.Errorf("no principal: domains = %v, want all three", got)
	}
	// Certificates are drawn from the domains, so they follow.
	if certs, err := e.svc.Certificates(member); err != nil {
		t.Fatal(err)
	} else {
		for _, c := range certs {
			if c.Host == "blog.example.com" {
				t.Error("member sees blog's certificate")
			}
		}
	}
}

func TestPlaceReadsNeedView(t *testing.T) {
	e := twoProjects(t)
	member := asPerson(shopMember())
	now := e.svc.clock.Now()
	_ = now

	var na *NotAllowedError
	if _, err := e.svc.Metrics(member, MetricsQuery{Project: "blog", Environment: "production"}); !errors.As(err, &na) {
		t.Errorf("Metrics of blog = %v, want a *NotAllowedError", err)
	}
	if _, err := e.svc.ReadLogs(member, EnvLogQuery{Project: "blog", Environment: "production"}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("ReadLogs of blog = %v, want ErrNotAllowed", err)
	}
	if _, err := e.svc.LogDeploys(member, "blog", "production", now.Add(-time.Hour), now, ""); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("LogDeploys of blog = %v, want ErrNotAllowed", err)
	}
	if _, err := e.svc.ReadRequests(member, RequestQuery{Project: "blog", Environment: "production"}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("ReadRequests of blog = %v, want ErrNotAllowed", err)
	}
	if _, err := e.svc.Metrics(member, MetricsQuery{Project: "shop", Environment: "staging"}); err != nil {
		t.Errorf("Metrics of shop's staging = %v, want allowed", err)
	}
	if _, err := e.svc.LogDeploys(member, "shop", "production", now.Add(-time.Hour), now, ""); err != nil {
		t.Errorf("LogDeploys of shop's production = %v, want allowed", err)
	}
	// Nothing of blog was read for the refused calls; no principal reads anywhere.
	if _, err := e.svc.Metrics(context.Background(), MetricsQuery{Project: "blog", Environment: "production"}); err != nil {
		t.Errorf("Metrics of blog with no principal = %v", err)
	}
	_ = store.ErrNotFound
}

func TestLogCopyOfAnotherProjectIsNotFound(t *testing.T) {
	ctx := context.Background()
	e := twoProjects(t)
	e.nomad.SetCopies(blogID, copyStub(copyBlogWeb, "web", "running", readStart))
	e.nomad.SetCopies(stagingID, copyStub(copyDocsWeb, "web", "running", readStart))

	if c, err := e.svc.resolveLogCopy(ctx, copyBlogWeb); err != nil || c.app != blogID {
		t.Fatalf("resolveLogCopy with no principal = %+v, %v; want blog's copy", c, err)
	}
	if _, err := e.svc.resolveLogCopy(asPerson(shopMember()), copyBlogWeb); !errors.Is(err, ErrLogCopyNotFound) {
		t.Errorf("a shop member asking for a copy of blog = %v, want ErrLogCopyNotFound", err)
	}
	if c, err := e.svc.resolveLogCopy(asPerson(shopMember()), copyDocsWeb); err != nil || c.app != stagingID {
		t.Errorf("a shop member asking for a copy of staging = %+v, %v", c, err)
	}
}
