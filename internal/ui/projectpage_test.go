package ui

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// pjSummary is an app summary with the given services (name and wanted copies), a
// state and the healthy copies per service, for the pure tests.
func pjSummary(state service.AppState, deployed bool, services ...service.ServiceStatus) service.AppSummary {
	a := app.App{ID: "web"}
	for _, svc := range services {
		a.Services = append(a.Services, app.Service{Name: svc.Name})
	}
	sum := service.AppSummary{AppRecord: store.AppRecord{App: a}, Status: service.AppStatus{State: state, Services: services}}
	if deployed {
		sum.LastDeployment = &store.DeploymentRecord{Status: store.StatusHealthy}
	}
	return sum
}

// copyStub is a running copy of a service at a job version.
func copyStub(service string, version uint64) api.AllocationListStub {
	return api.AllocationListStub{TaskGroup: service, JobVersion: version, ClientStatus: "running", NodeName: "node-1"}
}

func TestCopyMarksOf(t *testing.T) {
	web := func(wanted, healthy int) service.ServiceStatus {
		return service.ServiceStatus{Name: "web", Wanted: wanted, Healthy: healthy}
	}
	kinds := func(c markCell) [][]string {
		var out [][]string
		for _, g := range c.Groups {
			out = append(out, g.Kinds)
		}
		return out
	}
	tests := []struct {
		name  string
		sum   service.AppSummary
		kinds [][]string
		none  bool
		count string
		words string
	}{
		{"all healthy", pjSummary(service.StateHealthy, true, web(2, 2)), [][]string{{"healthy", "healthy"}}, false, "", "2 of 2 copies healthy"},
		{"short of copies: the healthy ones first, then a warning", pjSummary(service.StateDegraded, true, web(3, 1)), [][]string{{"healthy", "warning", "warning"}}, false, "", "1 of 3 copies healthy"},
		{"rolling out", pjSummary(service.StateRollingOut, true, web(2, 1)), [][]string{{"healthy", "rolling"}}, false, "", "1 of 2 copies healthy"},
		{"nothing runs", pjSummary(service.StateNotRunning, true, web(2, 0)), [][]string{{"failed", "failed"}}, false, "", "0 of 2 copies healthy"},
		{"stopped", pjSummary(service.StateStopped, true, web(2, 0)), [][]string{{"idle", "idle"}}, false, "", "0 of 2 copies healthy"},
		{"a surplus never draws more than wanted", pjSummary(service.StateHealthy, true, web(1, 3)), [][]string{{"healthy"}}, false, "", "1 of 1 copy healthy"},
		{"never deployed", pjSummary(service.StateNotRunning, false, web(1, 0)), nil, true, "", "none"},
		{"Nomad can't be read: no rings that would say stopped", pjSummary(service.StateUnknown, true, web(2, 0)), nil, true, "", "unknown"},
		{"not defined", pjSummary(service.StateNotRunning, false), nil, true, "", "none"},
		{"too many to draw", pjSummary(service.StateHealthy, true, web(14, 14)), nil, false, "14 copies", "14 of 14 copies healthy"},
		{"twelve are still drawn", pjSummary(service.StateHealthy, true, web(12, 12)), [][]string{slices.Repeat([]string{"healthy"}, 12)}, false, "", "12 of 12 copies healthy"},
		{"services are groups, in the app's order", pjSummary(service.StateDegraded, true,
			service.ServiceStatus{Name: "web", Wanted: 2, Healthy: 2}, service.ServiceStatus{Name: "worker", Wanted: 1, Healthy: 0}),
			[][]string{{"healthy", "healthy"}, {"warning"}}, false, "", "2 of 3 copies healthy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := copyMarksOf(tc.sum)
			if !slices.EqualFunc(kinds(got), tc.kinds, slices.Equal[[]string]) || got.None != tc.none || got.Count != tc.count || got.Words != tc.words {
				t.Errorf("copyMarksOf = %+v (kinds %v), want kinds %v none %v count %q words %q", got, kinds(got), tc.kinds, tc.none, tc.count, tc.words)
			}
		})
	}
	t.Run("a group is titled by its service only when there are several", func(t *testing.T) {
		one := copyMarksOf(pjSummary(service.StateHealthy, true, web(1, 1)))
		if one.Groups[0].Service != "" {
			t.Errorf("a single service's group is titled %q", one.Groups[0].Service)
		}
		two := copyMarksOf(pjSummary(service.StateHealthy, true, web(1, 1), service.ServiceStatus{Name: "worker", Wanted: 1, Healthy: 1}))
		if two.Groups[0].Service != "web" || two.Groups[1].Service != "worker" {
			t.Errorf("groups are titled %q, %q, want web, worker", two.Groups[0].Service, two.Groups[1].Service)
		}
	})
}

func TestChooseEnv(t *testing.T) {
	proj := func(names ...string) service.Project {
		p := service.Project{ID: "shop"}
		for _, n := range names {
			p.Environments = append(p.Environments, service.Environment{Project: "shop", Name: n})
		}
		return p
	}
	for _, tc := range []struct {
		name  string
		p     service.Project
		asked string
		want  string
	}{
		{"the one asked for", proj("production", "staging"), "staging", "staging"},
		{"none asked: production", proj("staging", "production"), "", "production"},
		{"a stale one: production", proj("production", "staging"), "gone", "production"},
		{"no production: the first", proj("staging", "qa"), "", "staging"},
		{"no production, a stale one: the first", proj("staging", "qa"), "gone", "staging"},
		{"no environment at all", proj(), "staging", ""},
	} {
		if got := chooseEnv(tc.p, tc.asked); got != tc.want {
			t.Errorf("%s: chooseEnv = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// region is the part of body between the first occurrence of start and the closing of the
// element it opens, for a few tags the page draws once.
func between(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", start, body)
	}
	j := strings.Index(body[i:], end)
	if j < 0 {
		t.Fatalf("no %q after %q in:\n%s", end, start, body[i:])
	}
	return body[i : i+j+len(end)]
}

// pjRow is the table row of an app, from the page.
func pjRow(t *testing.T, body, href string) string {
	t.Helper()
	return between(t, body, `<a href="`+href+`" class="cv-row cv-pj-row">`, "</a>")
}

func TestProjectPageDefaultProduction(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
	e.copies("whoami", "web", 7, 2, 0)

	resp, body := e.get(t, "/projects/default")
	if resp.StatusCode != http.StatusOK || h1Text(t, body) != "Default" {
		t.Fatalf("GET /projects/default = %d, h1 %q", resp.StatusCode, h1Text(t, body))
	}
	got := text(body)

	// The header: the buttons, and the tabs row even with one environment.
	for _, want := range []string{`<a href="/projects/default/settings" class="cv-btn cv-btn-secondary cv-btn-sm">`, `href="/apps/new?project=default&amp;environment=production"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the header lacks %s", want)
		}
	}
	tabs := between(t, body, `<nav aria-label="Environments"`, "</nav>")
	for _, want := range []string{`<a href="/projects/default?env=production" class="cv-envtab" aria-current="page">production</a>`,
		`href="/projects/default/settings#add-environment"`, "Environment"} {
		if !strings.Contains(tabs, want) {
			t.Errorf("the tabs lack %s:\n%s", want, tabs)
		}
	}
	if strings.Contains(body, `cv-pj-slot`) {
		t.Error("the project's name is drawn beside a display name that is the same")
	}

	// The row: one link, the pill, one mark per copy, the address, v7 and when.
	if !strings.Contains(got, "Apps · 1") {
		t.Errorf("the label doesn't say Apps · 1:\n%s", got)
	}
	row := pjRow(t, body, "/apps/whoami")
	if !strings.HasPrefix(text(row), "whoami healthy 2 of 2 copies healthy whoami.example.com v7 · ") {
		t.Errorf("the row reads %q", text(row))
	}
	if n := strings.Count(between(t, row, `<span class="cv-pj-groups"`, `<span class="sr-only">`), `<span class="cv-mark cv-mark-healthy" aria-hidden="true"></span>`); n != 2 {
		t.Errorf("the row has %d healthy marks, want 2:\n%s", n, row)
	}
	for _, want := range []string{`title="whoami.example.com"`, `<time class="cv-data" datetime=`, `cv-pill-healthy cv-pill-quiet cv-pill-sm`, `<span class="sr-only">2 of 2 copies healthy</span>`} {
		if !strings.Contains(row, want) {
			t.Errorf("the row lacks %s:\n%s", want, row)
		}
	}

	// The rail: the Environment panel and Deploys in production, with no Where prefix.
	panel := text(between(t, body, `<dl class="cv-panel cv-pj-env">`, "</dl>"))
	for _, want := range []string{"Deploys From the console · ", "Addresses 1"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the Environment panel lacks %q: %s", want, panel)
		}
	}
	if strings.Contains(panel, "Promote to") {
		t.Errorf("a project with one environment offers Promote: %s", panel)
	}
	if strings.Contains(body, "Needs you") {
		t.Errorf("an environment with nothing to fix draws the Needs you section:\n%s", got)
	}
	deploys := between(t, body, `id="pj-deploys-title"`, "</section>")
	if !strings.Contains(text(deploys), "Deploys in production") || !strings.Contains(deploys, `<span class="cv-app-name">whoami</span> <span class="cv-data">v7</span>`) ||
		strings.Contains(deploys, "›") {
		t.Errorf("Deploys in production = %s", text(deploys))
	}
}

// The header's two buttons are one group; the badges are plain links with no production class; a
// Deploys value keeps its "· when" on one line.
func TestProjectPageHeaderAndBadges(t *testing.T) {
	e := shopConsole(t)
	e.seed(t, shopWeb("staging", "latest"), seedDeploy{ID: "s1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1})
	_, body := e.get(t, "/projects/shop?env=staging")
	actions := between(t, body, `<span class="cv-pj-actions">`, "</span>\n  </div>")
	if !strings.Contains(actions, "Project settings") || !strings.Contains(actions, "New app here") {
		t.Errorf("the buttons aren't one group: %s", text(actions))
	}
	if strings.Contains(body, "cv-envtab-prod") || strings.Contains(body, "cv-spacer") {
		t.Error("the production badge class or the spacer is still drawn")
	}
	if !strings.Contains(body, `<span class="cv-nowrap"> · <time`) {
		t.Errorf("the Deploys value's time isn't kept on one line:\n%s", text(between(t, body, `<dl class="cv-panel cv-pj-env">`, "</dl>")))
	}
	if strings.Contains(text(between(t, body, `<dl class="cv-panel cv-pj-env">`, "</dl>")), "Apps ") {
		t.Error("the Environment panel still has an Apps row")
	}
}

func TestProjectPageEnvironmentsAndTabs(t *testing.T) {
	e := shopConsole(t)
	e.seed(t, shopWeb("staging", "latest"), seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.copies("shop--staging--web", "web", 1, 2, 0)

	current := func(body string) string {
		m := regexp.MustCompile(`<a href="[^"]*" class="cv-envtab[^"]*" aria-current="page">([^<]*)</a>`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no current tab in:\n%s", between(t, body, `<nav aria-label="Environments"`, "</nav>"))
		}
		return m[1]
	}
	order := func(body string) string {
		var names []string
		for _, m := range regexp.MustCompile(`class="cv-envtab[^"]*"[^>]*>([^<]*)</a>`).FindAllStringSubmatch(body, -1) {
			names = append(names, m[1])
		}
		return strings.Join(names, ",")
	}

	for _, tc := range []struct{ path, want string }{
		{"/projects/shop", "production"},
		{"/projects/shop?env=staging", "staging"},
		{"/projects/shop?env=production", "production"},
		{"/projects/shop?env=gone", "production"}, // a stale link falls back, with no error
		{"/projects/shop?env=%3Cb%3E", "production"},
	} {
		resp, body := e.get(t, tc.path)
		if resp.StatusCode != http.StatusOK || current(body) != tc.want {
			t.Errorf("GET %s = %d with %q current, want %q", tc.path, resp.StatusCode, current(body), tc.want)
		}
		if got := order(body); got != "production,staging" {
			t.Errorf("GET %s lists the tabs as %q, want the project's order (production, staging)", tc.path, got)
		}
	}

	t.Run("staging's table and its empty production", func(t *testing.T) {
		_, staging := e.get(t, "/projects/shop?env=staging")
		if !strings.Contains(text(staging), "Apps · 1") || !strings.Contains(staging, `href="/apps/shop--staging--web" class="cv-row cv-pj-row"`) {
			t.Errorf("staging doesn't list its app:\n%s", text(staging))
		}
		// The row says the app's name in its environment.
		if row := text(pjRow(t, staging, "/apps/shop--staging--web")); !strings.HasPrefix(row, "web healthy 2 of 2 copies healthy staging.shop.example.com v1 · ") {
			t.Errorf("the row reads %q", row)
		}
		_, prod := e.get(t, "/projects/shop")
		for _, want := range []string{"Apps · 0", "No apps in production yet.", `href="/apps/new?project=shop&amp;environment=production"`} {
			if !strings.Contains(strings.ReplaceAll(prod, "\n", " "), want) && !strings.Contains(text(prod), want) {
				t.Errorf("production's empty state lacks %q:\n%s", want, text(prod))
			}
		}
		if n := strings.Count(prod, "New app here"); n != 2 { // the header's and the empty state's
			t.Errorf("the empty production has %d New app here links, want 2", n)
		}
		panel := text(between(t, prod, `<dl class="cv-panel cv-pj-env">`, "</dl>"))
		for _, want := range []string{"Deploys None yet", "Addresses none"} {
			if !strings.Contains(panel, want) {
				t.Errorf("the empty environment's panel lacks %q: %s", want, panel)
			}
		}
		if !strings.Contains(prod, "No deploys in <span") {
			t.Errorf("the empty environment's Deploys doesn't say so:\n%s", text(prod))
		}
	})

	t.Run("the slot holds the name when it differs from the display name", func(t *testing.T) {
		_, body := e.get(t, "/projects/shop")
		if !strings.Contains(body, `<span class="cv-pj-slot cv-inline-mono">shop</span>`) {
			t.Errorf("the project's name isn't beside its display name:\n%s", between(t, body, `<header class="cv-pj-head">`, "</header>"))
		}
	})

	t.Run("a project whose production was deleted opens on its first environment", func(t *testing.T) {
		ctx := context.Background()
		if _, err := e.Svc.CreateProject(ctx, "solo", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Svc.CreateEnvironment(ctx, "solo", "qa"); err != nil {
			t.Fatal(err)
		}
		if err := e.Svc.DeleteEnvironment(ctx, "solo", "production"); err != nil {
			t.Fatal(err)
		}
		_, body := e.get(t, "/projects/solo")
		if current(body) != "qa" || strings.Contains(body, "cv-envtab-prod") {
			t.Errorf("the page opens on %q:\n%s", current(body), text(body))
		}
		if err := e.Svc.DeleteEnvironment(ctx, "solo", "qa"); err != nil {
			t.Fatal(err)
		}
		resp, body := e.get(t, "/projects/solo")
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "This project has no environment yet.") || strings.Contains(body, `class="cv-envtab`) {
			t.Errorf("a project with no environment = %d:\n%s", resp.StatusCode, text(body))
		}
		for _, path := range []string{"/parts/projects/solo/apps", "/parts/projects/solo/rail"} {
			if resp, _ := e.get(t, path); resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s with no environment = %d, want 200", path, resp.StatusCode)
			}
		}
	})
}

func TestProjectPageRowStates(t *testing.T) {
	e := shopConsole(t)
	ctx := context.Background()

	// A never-deployed app with a definition, and a name-only one.
	defined := shopWeb("staging", "latest")
	defined.ID = app.Place{Project: "shop", Environment: "staging", Name: "later"}.ID()
	if _, err := e.Svc.CreateAppOnly(ctx, defined); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateAppOnly(ctx, app.App{ID: app.Place{Project: "shop", Environment: "staging", Name: "bare"}.ID()}); err != nil {
		t.Fatal(err)
	}
	// An app that rolls out: v1 serves while v2 starts.
	roll := shopWeb("staging", "latest")
	roll.ID = app.Place{Project: "shop", Environment: "staging", Name: "roll"}.ID()
	roll.Services[0].Domains[0].Host = "roll.shop.example.com"
	roll.Services[0].Domains = append(roll.Services[0].Domains, app.Domain{Host: "roll2.shop.example.com", Port: roll.Services[0].Domains[0].Port}, app.Domain{Host: "roll3.shop.example.com", Port: roll.Services[0].Domains[0].Port})
	e.seed(t, roll,
		seedDeploy{ID: "r1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1},
		seedDeploy{ID: "r2", Trigger: app.TriggerManual, Status: store.StatusRunning, Version: 2})
	e.copies(roll.ID, "web", 1, 1, 0)
	// A failed deploy beside a healthy one: the failed one sorts first.
	fine := shopWeb("staging", "latest")
	fine.ID = app.Place{Project: "shop", Environment: "staging", Name: "aaa"}.ID()
	e.seed(t, fine, seedDeploy{ID: "f1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.copies(fine.ID, "web", 1, 2, 0)
	bad := shopWeb("staging", "latest")
	bad.ID = app.Place{Project: "shop", Environment: "staging", Name: "zzz"}.ID()
	e.seed(t, bad, seedDeploy{ID: "z1", Trigger: app.TriggerManual, Status: store.StatusFailed, Error: "image not found"})

	_, body := e.get(t, "/projects/shop?env=staging")

	later := text(pjRow(t, body, "/apps/shop--staging--later"))
	if !strings.HasPrefix(later, "later never deployed —none ") || !strings.HasSuffix(later, " —") {
		t.Errorf("the never-deployed row reads %q", later)
	}
	laterHTML := pjRow(t, body, "/apps/shop--staging--later")
	if !strings.Contains(laterHTML, `<span class="cv-pj-none" aria-hidden="true">—</span><span class="sr-only">none</span>`) ||
		!strings.Contains(laterHTML, `<span class="cv-pj-ver">—</span>`) {
		t.Errorf("a never-deployed app draws marks and version as — with words for a screen reader:\n%s", laterHTML)
	}
	if bare := text(pjRow(t, body, "/apps/shop--staging--bare")); !strings.Contains(bare, "not defined yet") || !strings.Contains(bare, "no address") {
		t.Errorf("the name-only row reads %q", bare)
	}

	rollHTML := pjRow(t, body, "/apps/shop--staging--roll")
	for _, want := range []string{`cv-mark cv-mark-healthy" aria-hidden="true"></span>`, `cv-mark cv-mark-rolling" aria-hidden="true"></span>`, `<span aria-hidden="true"> → </span><span class="sr-only"> rolling out </span>v2`,
		"roll.shop.example.com", `<span class="cv-pj-more"> +2</span>`, "rolling out"} {
		if !strings.Contains(rollHTML, want) {
			t.Errorf("the rolling row lacks %s:\n%s", want, rollHTML)
		}
	}
	if !strings.Contains(text(rollHTML), "v1 → rolling out v2 · ") {
		t.Errorf("the rolling row's version reads %q", text(rollHTML))
	}

	// Problems first: failed, then rolling, then healthy (the Apps home's rule).
	iBad, iRoll, iFine := strings.Index(body, `href="/apps/shop--staging--zzz"`), strings.Index(body, `href="/apps/shop--staging--roll"`), strings.Index(body, `href="/apps/shop--staging--aaa"`)
	if !(iBad >= 0 && iBad < iRoll && iRoll < iFine) {
		t.Errorf("rows aren't problems first: failed at %d, rolling at %d, healthy at %d", iBad, iRoll, iFine)
	}
	if !strings.Contains(text(body), "Apps · 5") {
		t.Errorf("the label doesn't count five apps:\n%s", text(body))
	}

	// The rail names the environment's trouble, and its Environment panel counts it.
	rail := between(t, body, `id="project-rail"`, "</aside>")
	need := text(between(t, rail, `id="pj-need-title"`, "</section>"))
	if !strings.Contains(need, "Needs you") || !strings.Contains(need, "zzz") || !strings.Contains(need, "View deployment") {
		t.Errorf("the rail's Needs you = %s", need)
	}
	// The label says where: the row's sentence starts with the app's name, not "Shop Floor › staging ›".
	if strings.Contains(need, "Shop Floor") || strings.Contains(need, "›") {
		t.Errorf("the rail's Needs you repeats the place before the app's name: %s", need)
	}
	// Later, aaa and zzz share staging's one host and roll has three of its own: four addresses.
	if panel := text(between(t, rail, `<dl class="cv-panel cv-pj-env">`, "</dl>")); !strings.Contains(panel, "Addresses 4") || strings.Contains(panel, "Apps ") {
		t.Errorf("the Environment panel = %s", panel)
	}

	// The rail's order: what needs the owner first, then the Environment panel, then the deploys.
	iNeed, iEnv, iDeploys := strings.Index(rail, `id="pj-need-title"`), strings.Index(rail, `id="pj-env-title"`), strings.Index(rail, `id="pj-deploys-title"`)
	if !(iNeed >= 0 && iNeed < iEnv && iEnv < iDeploys) {
		t.Errorf("the rail isn't Needs you, Environment, Deploys: at %d, %d, %d", iNeed, iEnv, iDeploys)
	}

	// Production, in the same project, has none of it.
	_, prod := e.get(t, "/projects/shop?env=production")
	if strings.Contains(prod, "Needs you") {
		t.Errorf("another environment's trouble is on production's rail:\n%s", text(prod))
	}
}

func TestProjectPageServicesAreGroupsAndTheRailIsOfThePlace(t *testing.T) {
	e := shopConsole(t)
	blog := blogApp()
	blog.ID = app.Place{Project: "shop", Environment: "staging", Name: "blog"}.ID()
	e.seed(t, blog, seedDeploy{ID: "b1", Trigger: app.TriggerWebhook, Status: store.StatusHealthy, Version: 3})
	e.Nomad.SetCopies(blog.ID, copyStub("web", 3), copyStub("worker", 3))
	// Many deployments elsewhere, newer than staging's: staging's rail still lists its own.
	for i, id := range []string{"w1", "w2", "w3", "w4"} {
		e.seed(t, whoamiApp(), seedDeploy{ID: id, Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: uint64(i + 1)})
	}

	_, body := e.get(t, "/projects/shop?env=staging")
	row := pjRow(t, body, "/apps/shop--staging--blog")
	for _, want := range []string{`<span class="cv-pj-group" title="web">`, `<span class="cv-pj-group" title="worker">`, "blog.example.com"} {
		if !strings.Contains(row, want) {
			t.Errorf("the two-service row lacks %s:\n%s", want, row)
		}
	}
	if strings.Contains(row, "cv-pj-more") || strings.Contains(row, "cv-kind") {
		t.Errorf("one domain has a +N, or an image app has a kind word:\n%s", row)
	}
	if !strings.Contains(row, "2 of 2 copies healthy") {
		t.Errorf("the screen-reader words are missing:\n%s", row)
	}

	rail := between(t, body, `id="project-rail"`, "</aside>")
	deploys := between(t, rail, `id="pj-deploys-title"`, "</section>")
	if !strings.Contains(deploys, `<span class="cv-app-name">blog</span> <span class="cv-data">v3</span>`) || strings.Contains(deploys, "whoami") {
		t.Errorf("staging's Deploys = %s", text(deploys))
	}
	if !strings.Contains(text(rail), "Deploys By the webhook · ") {
		t.Errorf("the Environment panel doesn't say how it was last deployed: %s", text(rail))
	}
}

func TestProjectPagePromoteRows(t *testing.T) {
	e := shopConsole(t)
	// staging: web (promotable, with a counterpart), solo (promotable, no counterpart),
	// broken (a counterpart, but its newest deploy failed), stopped-ish is covered by promotable's own test.
	healthy := func(name, env string) app.App {
		a := shopWeb(env, "latest")
		a.ID = app.Place{Project: "shop", Environment: env, Name: name}.ID()
		a.Services[0].Domains[0].Host = name + "." + env + ".shop.example.com"
		return a
	}
	for _, name := range []string{"web", "solo"} {
		a := healthy(name, "staging")
		e.seed(t, a, seedDeploy{ID: "s-" + name, Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1, Definition: &a})
	}
	failed := healthy("broken", "staging")
	e.seed(t, failed, seedDeploy{ID: "s-broken", Trigger: app.TriggerManual, Status: store.StatusFailed, Error: "image not found", Definition: &failed})
	for _, name := range []string{"web", "broken"} {
		e.seed(t, healthy(name, "production"), seedDeploy{ID: "p-" + name, Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	}

	_, staging := e.get(t, "/projects/shop?env=staging")
	panel := between(t, staging, `<dl class="cv-panel cv-pj-env">`, "</dl>")
	if !strings.Contains(text(panel), "Promote to production") {
		t.Errorf("staging's panel has no Promote to production row: %s", text(panel))
	}
	if want := `<a href="/apps/shop--staging--web/promote?to=production" class="cv-btn cv-btn-secondary cv-btn-sm">web</a>`; !strings.Contains(panel, want) {
		t.Errorf("the panel lacks the link %s:\n%s", want, panel)
	}
	for _, not := range []string{"solo", "broken"} {
		if strings.Contains(panel, "promote?to=production") && strings.Contains(panel, ">"+not+"</a>") {
			t.Errorf("%s is offered for promotion:\n%s", not, panel)
		}
	}
	if n := strings.Count(panel, "promote?to="); n != 1 {
		t.Errorf("%d promote links, want only web's:\n%s", n, panel)
	}

	// Production's own panel: its apps aren't promotable (no recorded definition), so no row.
	_, prod := e.get(t, "/projects/shop?env=production")
	if strings.Contains(text(between(t, prod, `<dl class="cv-panel cv-pj-env">`, "</dl>")), "Promote to") {
		t.Errorf("production offers a promotion nothing can take:\n%s", text(prod))
	}
}

func TestProjectPageDeploysRowWords(t *testing.T) {
	e := shopConsole(t)
	a := shopWeb("staging", "latest")
	for _, tc := range []struct {
		id   string
		d    seedDeploy
		want string
	}{
		{"o1", seedDeploy{Trigger: app.TriggerWebhook, Origin: &app.Origin{Commit: "abcdef1234567", Ref: "refs/heads/main", Actor: "ana"}}, "Deploys Pushed to main by ana · "},
		{"t1", seedDeploy{Trigger: app.TriggerManual, TokenName: "ci-github"}, "Deploys Through the API by ci-github · "},
		{"c1", seedDeploy{Trigger: app.TriggerConsole}, "Deploys From the console · "},
	} {
		tc.d.ID, tc.d.Status, tc.d.Version = tc.id, store.StatusHealthy, 1
		e.seed(t, a, tc.d)
		_, body := e.get(t, "/projects/shop?env=staging")
		if got := text(between(t, body, `<dl class="cv-panel cv-pj-env">`, "</dl>")); !strings.Contains(got, tc.want) {
			t.Errorf("after %s the panel reads %q, want %q", tc.id, got, tc.want)
		}
	}
}

// The parts are the page's regions: the page draws the same markup, 404 for an unknown
// project, and a stale ?env= falls back as the page does.
func TestProjectParts(t *testing.T) {
	e := shopConsole(t)
	e.seed(t, shopWeb("staging", "latest"), seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.copies("shop--staging--web", "web", 1, 2, 0)

	_, page := e.get(t, "/projects/shop?env=staging")
	same := func(part, region string) {
		t.Helper()
		if text(part) != text(region) {
			t.Errorf("a part and its region differ:\npart:   %s\nregion: %s", text(part), text(region))
		}
	}

	resp, apps := e.get(t, "/parts/projects/shop/apps?env=staging")
	if resp.StatusCode != http.StatusOK || strings.Contains(apps, "<html") || strings.Contains(apps, "cv-shell") {
		t.Fatalf("GET the apps part = %d, a fragment is wanted:\n%s", resp.StatusCode, apps)
	}
	inner := func(region string) string { return strings.SplitN(region, `innerHTML">`, 2)[1] }
	same(apps, inner(between(t, page, `id="project-apps"`, "</section>")))
	if !strings.Contains(apps, "Apps · 1") || !strings.Contains(apps, `class="cv-row cv-pj-row"`) {
		t.Errorf("the apps part lacks the table:\n%s", apps)
	}

	resp, rail := e.get(t, "/parts/projects/shop/rail?env=staging")
	if resp.StatusCode != http.StatusOK || strings.Contains(rail, "<html") {
		t.Fatalf("GET the rail part = %d:\n%s", resp.StatusCode, rail)
	}
	same(rail, inner(between(t, page, `id="project-rail"`, "</aside>")))

	// The regions refresh themselves from these parts, on the notices the contract names.
	for _, want := range []string{
		`hx-get="/parts/projects/shop/apps?env=staging" hx-trigger="sse:apps, sse:deployments, sse:copies, sse:sync"`,
		`hx-get="/parts/projects/shop/rail?env=staging" hx-trigger="sse:apps, sse:attention, sse:activity, sse:deployments, sse:sync"`,
	} {
		if !strings.Contains(strings.ReplaceAll(page, "&amp;", "&"), want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	// The header and the tabs are outside both regions.
	if strings.Contains(apps, "cv-envtab") || strings.Contains(rail, "cv-envtab") || strings.Contains(apps, "Project settings") {
		t.Error("a part draws the header")
	}
	if !strings.Contains(page, `data-live-indicator`) {
		t.Error("the page doesn't open the workspace stream")
	}

	// A stale environment falls back to production, as the page does.
	_, stale := e.get(t, "/parts/projects/shop/apps?env=gone")
	if !strings.Contains(stale, "No apps in") || !strings.Contains(text(stale), "production") {
		t.Errorf("a stale ?env= doesn't fall back to production:\n%s", text(stale))
	}
	for _, path := range []string{"/parts/projects/nope/apps", "/parts/projects/nope/rail"} {
		if resp, _ := e.get(t, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
	// Both need the session, as every part does.
	for _, path := range []string{"/parts/projects/shop/apps", "/parts/projects/shop/rail"} {
		resp, _ := e.do(t, request{path: path})
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s without a session = 200", path)
		}
	}
}

func TestProjectPageWithoutNewApp(t *testing.T) {
	e := shopConsole(t)
	s := e.Server
	s.newApp = false
	_, body := e.get(t, "/projects/shop")
	if strings.Contains(body, "New app here") || strings.Contains(body, "/apps/new") {
		t.Errorf("New app is offered while it is off:\n%s", text(body))
	}
	if !strings.Contains(text(body), "No apps in production yet.") {
		t.Errorf("the empty state is gone:\n%s", text(body))
	}
}
