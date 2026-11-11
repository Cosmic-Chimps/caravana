package ui

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The three parts of the frame, cut from a page, for the shell tests.
var (
	sidebarRegion = regexp.MustCompile(`(?s)<aside class="cv-side">.*?</aside>`)
	navItem       = regexp.MustCompile(`<a href="([^"]*)" class="cv-nav-item"( aria-current="page")?>.*?<span>([^<]*)</span>(?:<span class="cv-nav-mark".*?</span>)?</a>`)
	appHeadRegion = regexp.MustCompile(`(?s)<header class="cv-apphead">.*?</header>`)
)

// navOf is the sidebar's items in order, as "label=href", with the current one
// starred.
func navOf(t *testing.T, body string) []string {
	t.Helper()
	side := sidebarRegion.FindString(body)
	if side == "" {
		t.Fatalf("no sidebar in:\n%s", body)
	}
	var out []string
	for _, m := range navItem.FindAllStringSubmatch(side, -1) {
		item := m[3] + "=" + m[1]
		if m[2] != "" {
			item += "*"
		}
		out = append(out, item)
	}
	return out
}

// The sidebar has exactly the handoff's order on every page (feature 027.5):
// Workspace holds Home, Projects, Logs, Monitoring and Schedules, Configure holds
// Domains, then Settings. It links nothing that has no page yet, and marks the right
// one: Home on /apps, Projects on the projects and on every page of an app, Schedules
// on /schedules (feature 022), Settings on /settings. The top bar
// has New app, and the crumbs say where the page is (contracts/page1-shell.md).
func TestShellOnEveryPage(t *testing.T) {
	e := shopConsole(t)
	e.seedStandard(t)

	tests := []struct {
		path   string
		active string // the starred item
		crumbs string
	}{
		{"/apps", "Home", "Home"},
		{"/apps/new", "Home", "New app"},
		{"/projects", "Projects", "Projects"},
		{"/projects/default", "Projects", "Default"},
		{"/projects/shop", "Projects", "Shop Floor"},
		{"/projects/shop/settings", "Projects", "Shop Floor /Settings"},
		{"/projects/new", "Projects", "Projects /New project"},
		{"/apps/whoami", "Projects", "Default /production"},
		{"/apps/whoami/deployments", "Projects", "Default /production"},
		{"/apps/whoami/configure", "Projects", "Default /production"},
		{"/apps/whoami/logs", "Projects", "Default /production"},
		{"/apps/whoami/stop", "Projects", "Default /production /whoami /Stop"},
		{"/settings", "Settings", "Settings /Account"},
		{"/settings/registries", "Settings", "Settings /Registries"},
		{"/settings/backup-storage", "Settings", "Settings /Backup storage"},
		{"/settings/notifications", "Settings", "Settings /Notifications"},
		{"/settings/tokens", "Settings", "Settings /API tokens"},
		{"/schedules", "Schedules", "Schedules"},
		{"/schedules/new", "Schedules", "Schedules /New schedule"},
		{"/domains", "Domains", "Domains"},
		{"/logs", "Logs", "Logs"},
		{"/monitoring", "Monitoring", "Monitoring"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, body := e.get(t, tc.path)
			if resp.StatusCode != 200 {
				t.Fatalf("GET %s = %d:\n%s", tc.path, resp.StatusCode, text(body))
			}

			want := []string{"Home=/apps", "Projects=/projects", "Logs=/logs", "Monitoring=/monitoring", "Schedules=/schedules", "Domains=/domains", "Settings=/settings"}
			for i, item := range want {
				if strings.HasPrefix(item, tc.active+"=") {
					want[i] += "*"
				}
			}
			if got := navOf(t, body); !slices.Equal(got, want) {
				t.Errorf("sidebar = %v, want %v", got, want)
			}
			side := text(sidebarRegion.FindString(body))
			// Two group headings, in order: Workspace, then Configure with Domains under it.
			if w, c := strings.Index(side, "Workspace"), strings.Index(side, "Configure"); w < 0 || c < w ||
				strings.Index(side, "Domains") < c || strings.Index(side, "Schedules") > c {
				t.Errorf("the sidebar's groups are not Workspace, then Configure with Domains: %s", side)
			}
			for _, absent := range []string{"Deploys", "Previews", "Backups"} {
				if strings.Contains(side, absent) {
					t.Errorf("the sidebar links %q, which has no page yet: %s", absent, side)
				}
			}
			// The brand links to Home; the account row signs out with a POST.
			for _, want := range []string{`<a href="/apps" class="cv-brand">`, `<form method="post" action="/logout">`,
				`aria-label="Sign out" title="Sign out"`, "caravana v0.7.0"} {
				if !strings.Contains(sidebarRegion.FindString(body), want) {
					t.Errorf("the sidebar lacks %q", want)
				}
			}
			// The top bar: New app, and the crumbs.
			if !strings.Contains(body, `<a href="/apps/new" class="cv-btn cv-btn-primary cv-btn-sm cv-top-new">`) {
				t.Error("the top bar lacks New app")
			}
			if got := text(crumbLine.FindString(body)); got != tc.crumbs {
				t.Errorf("crumbs = %q, want %q", got, tc.crumbs)
			}
			if strings.Contains(body, "cv-top-action") {
				t.Error("the top bar still has a per-page action slot")
			}
		})
	}
}

// The toggle (feature 027): a nav "View" of two plain links, the current view marked, in the
// brand block of the sidebar and of the Menu panel; the Developer sidebar is what it was.
func TestViewToggle(t *testing.T) {
	e := shopConsole(t)
	e.seedStandard(t)
	toggle := regexp.MustCompile(`(?s)<nav aria-label="View" class="cv-view">(.*?)</nav>`)
	link := regexp.MustCompile(`<a href="([^"]*)" class="cv-view-link"( aria-current="page")?>([^<]*)</a>`)
	viewOf := func(t *testing.T, region string) []string {
		t.Helper()
		m := toggle.FindStringSubmatch(region)
		if m == nil {
			t.Fatalf("no View nav in:\n%s", region)
		}
		var out []string
		for _, l := range link.FindAllStringSubmatch(m[1], -1) {
			item := l[3] + "=" + l[1]
			if l[2] != "" {
				item += "*"
			}
			out = append(out, item)
		}
		return out
	}

	_, body := e.get(t, "/apps")
	if got, want := viewOf(t, sidebarRegion.FindString(body)), []string{"Developer=/apps*", "Operator=/ops"}; !slices.Equal(got, want) {
		t.Errorf("toggle on /apps = %v, want %v", got, want)
	}
	if n := strings.Count(body, `<nav aria-label="View"`); n != 2 {
		t.Errorf("the page has %d toggles, want 2 (the sidebar's and the Menu panel's)", n)
	}
	menu := regexp.MustCompile(`(?s)<div class="cv-menu-panel">.*?</div>\s*</details>`).FindString(body)
	if got, want := viewOf(t, menu), []string{"Developer=/apps*", "Operator=/ops"}; !slices.Equal(got, want) {
		t.Errorf("toggle in the Menu panel = %v, want %v", got, want)
	}

	// An Operator page shows the other side current (the pages themselves come with T011; the
	// shell is drawn by whatever page asks for it, here the brand block on its own).
	req := httptest.NewRequest(http.MethodGet, "/ops", nil)
	d, err := e.Server.shellPage(req, "Overview", navOpsOverview, crumb{Label: "Overview"})
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := e.Server.pages["monitoring"].ExecuteTemplate(&buf, "shell-brand", d.Shell); err != nil {
		t.Fatal(err)
	}
	if got, want := viewOf(t, buf.String()), []string{"Developer=/apps", "Operator=/ops*"}; !slices.Equal(got, want) {
		t.Errorf("toggle on /ops = %v, want %v", got, want)
	}
}

// The Operator sidebar: Server (Overview, Server) and Platform (Updates), then Settings; each
// item marked as page 1's contract says, and an app's pages in Operator mode mark Overview.
func TestOperatorSidebar(t *testing.T) {
	e := shopConsole(t)
	e.seedStandard(t)
	ops := &http.Cookie{Name: "caravana_mode", Value: "ops"}
	page := func(t *testing.T, path string, cookies ...*http.Cookie) string {
		t.Helper()
		resp, body := e.do(t, request{path: path, cookies: append([]*http.Cookie{e.Cookie}, cookies...)})
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		return body
	}
	want := func(active string) []string {
		items := []string{"Overview=/ops", "Servers=/ops/servers", "Updates=/ops/updates", "Settings=/settings"}
		for i, it := range items {
			if strings.HasPrefix(it, active+"=") {
				items[i] += "*"
			}
		}
		return items
	}

	tests := []struct {
		path   string
		active string
	}{
		{"/settings", "Settings"},
		{"/settings/tokens", "Settings"},
		{"/apps/whoami", "Overview"},
		{"/apps/whoami/deployments", "Overview"},
		{"/apps/new", "Overview"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			body := page(t, tc.path, ops)
			if got := navOf(t, body); !slices.Equal(got, want(tc.active)) {
				t.Errorf("Operator sidebar on %s = %v, want %v", tc.path, got, want(tc.active))
			}
			sidebar := text(sidebarRegion.FindString(body))
			for _, group := range []string{"Server", "Platform"} {
				if !strings.Contains(sidebar, group) {
					t.Errorf("the Operator sidebar lacks the group %q", group)
				}
			}
			for _, absent := range []string{"Workspace", "Projects", "Schedules", "Monitoring"} {
				if strings.Contains(sidebar, absent) {
					t.Errorf("the Operator sidebar has %q", absent)
				}
			}
		})
	}

	// The same pages without the cookie are the Developer ones.
	if got := navOf(t, page(t, "/settings")); !hasItem(got, "Projects=/projects") || hasItem(got, "Overview=/ops") {
		t.Errorf("Settings without the cookie = %v, want the Developer sidebar", got)
	}
	// Anything but "ops" in the cookie is Developer.
	if got := navOf(t, page(t, "/settings", &http.Cookie{Name: "caravana_mode", Value: "admin"})); !hasItem(got, "Projects=/projects") {
		t.Errorf("Settings with a bad cookie = %v, want the Developer sidebar", got)
	}
	// The Developer-only pages ignore the cookie: their path says the view.
	for _, path := range []string{"/apps", "/projects", "/schedules", "/domains", "/logs", "/monitoring"} {
		if got := navOf(t, page(t, path, ops)); !hasItem(got, "Projects=/projects") {
			t.Errorf("%s with the Operator cookie = %v, want the Developer sidebar", path, got)
		}
	}
}

// Every page of a view sets the cookie; the shared pages, the parts and the static files don't
// touch it; the cookie is HttpOnly, Lax, for the whole site, and not Secure over plain HTTP.
func TestModeCookieIsSetByPagesOfAView(t *testing.T) {
	e := shopConsole(t)
	e.seedStandard(t)
	cookieOf := func(resp *http.Response) *http.Cookie {
		for _, c := range resp.Cookies() {
			if c.Name == "caravana_mode" {
				return c
			}
		}
		return nil
	}
	tests := []struct {
		path string
		want string // "" means no cookie is set
	}{
		{"/apps", "dev"},
		{"/projects", "dev"},
		{"/projects/shop", "dev"},
		{"/schedules", "dev"},
		{"/domains", "dev"},
		{"/logs", "dev"},
		{"/monitoring", "dev"},
		{"/settings", ""},
		{"/apps/whoami", ""},
		{"/apps/new", ""},
		{"/parts/apps", ""},
		{"/static/app.css", ""},
		{"/login", ""},
		{"/nope", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, _ := e.get(t, tc.path)
			c := cookieOf(resp)
			if tc.want == "" {
				if c != nil {
					t.Errorf("GET %s set the mode cookie %+v", tc.path, c)
				}
				return
			}
			if c == nil || c.Value != tc.want {
				t.Fatalf("GET %s set %+v, want caravana_mode=%s", tc.path, c, tc.want)
			}
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure {
				t.Errorf("cookie = %+v, want HttpOnly, Lax, Path=/ and not Secure over plain HTTP", c)
			}
		})
	}

	// A form's answer changes nothing.
	resp, _ := e.do(t, request{method: http.MethodPost, path: "/schedules/none/delete", cookies: []*http.Cookie{e.Cookie}, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
	if c := cookieOf(resp); c != nil {
		t.Errorf("a POST set the mode cookie %+v", c)
	}
}

// hasItem says whether the sidebar items (navOf) have item, starred or not.
func hasItem(items []string, item string) bool {
	return slices.ContainsFunc(items, func(i string) bool { return strings.TrimSuffix(i, "*") == item })
}

func TestPathMode(t *testing.T) {
	tests := map[string]string{
		"/ops": "ops", "/ops/": "ops", "/ops/servers": "ops", "/ops/servers/lab": "ops", "/ops/updates/check": "ops",
		"/opsy": "", "/operator": "",
		"/": "dev", "/apps": "dev", "/projects": "dev", "/projects/new": "dev", "/schedules/abc/edit": "dev",
		"/domains": "dev", "/logs": "dev", "/monitoring": "dev",
		"/logs/download": "", "/parts/logs": "", "/parts/ops/server": "", "/live": "", "/static/app.css": "",
		"/apps/new": "", "/apps/blog": "", "/apps/blog/logs": "", "/settings": "", "/settings/tokens": "", "/login": "",
	}
	for path, want := range tests {
		if got := pathMode(path); got != want {
			t.Errorf("pathMode(%q) = %q, want %q", path, got, want)
		}
	}
}

// The cookie is Secure when the client reached the console over HTTPS, the way the session
// cookie decides it.
func TestModeCookieSecureOverHTTPS(t *testing.T) {
	e := shopConsole(t)
	h := e.Server.rememberMode(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, tc := range []struct {
		url    string
		secure bool
	}{{"http://console.example.com/ops", false}, {"https://console.example.com/ops", true}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
		cs := w.Result().Cookies()
		if len(cs) != 1 || cs[0].Name != "caravana_mode" || cs[0].Value != "ops" || cs[0].Secure != tc.secure {
			t.Errorf("GET %s set %+v, want caravana_mode=ops with Secure=%v", tc.url, cs, tc.secure)
		}
	}
}

func TestInitials(t *testing.T) {
	for email, want := range map[string]string{"ana.diaz@example.com": "AN", "j@example.com": "J", "": "?", "@example.com": "?", "élan@example.com": "ÉL"} {
		if got := initialsOf(email); got != want {
			t.Errorf("initialsOf(%q) = %q, want %q", email, got, want)
		}
	}
	// A host that doesn't start with a letter (an IP) gets "C", for caravana.
	for host, want := range map[string]string{"caravana.example.com": "C", "lacalabaza.xyz": "L", "127.0.0.1:7070": "C", "[::1]:7070": "C", "": "C"} {
		if got := initialOf(host); got != want {
			t.Errorf("initialOf(%q) = %q, want %q", host, got, want)
		}
	}
}

// /projects lists every project as a row that links to its page: the name, the
// environments as pills and the number of apps.
func TestProjectsPage(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, shopWeb("staging", "latest"))
	e.deploy(t, whoamiApp())

	resp, body := e.get(t, "/projects")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /projects = %d:\n%s", resp.StatusCode, text(body))
	}
	if got := h1Text(t, body); got != "Projects" {
		t.Errorf("headline = %q", got)
	}
	rows := regexp.MustCompile(`(?s)<a href="(/projects/[^"]*)" class="cv-project-row">(.*?)</a>`).FindAllStringSubmatch(body, -1)
	var got []string
	for _, m := range rows {
		got = append(got, m[1]+" "+text(m[2]))
	}
	want := []string{"/projects/default Default production 1 app", "/projects/shop Shop Floor production staging 1 app"}
	if !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if !strings.Contains(body, `<span class="cv-chip">staging</span>`) {
		t.Error("the environments aren't pills")
	}
	if !strings.Contains(body, `<a href="/projects/new" class="cv-btn cv-btn-secondary cv-btn-sm">New project</a>`) {
		t.Error("the page's header lacks New project")
	}
	if strings.Contains(body, "Back to Apps") {
		t.Error("the page is the not-found one")
	}
}

func TestProjectsNeedsASession(t *testing.T) {
	e := newUIEnv(t, true, "")
	resp, _ := e.do(t, request{path: "/projects"})
	if resp.StatusCode != 303 || location(t, resp) != "/login?next=%2Fprojects" {
		t.Errorf("GET /projects without a session = %d to %q", resp.StatusCode, location(t, resp))
	}
}

// The app header as the handoff draws it: a back link to Home, the title row with its
// actions (Restart, Stop…, Deploy as a link to the deploy form; Start and no Deploy when
// stopped), and neither the old place line nor the old actions row.
func TestAppHeaderActions(t *testing.T) {
	e := shopConsole(t)
	e.seedStandard(t)

	header := func(e *consoleEnv, path string) string {
		t.Helper()
		_, body := e.get(t, path)
		h := appHeadRegion.FindString(body)
		if h == "" {
			t.Fatalf("no app header in:\n%s", body)
		}
		for _, gone := range []string{"cv-where", "cv-actions"} {
			if strings.Contains(h, gone) {
				t.Errorf("GET %s: the header still has %s", path, gone)
			}
		}
		if !strings.Contains(h, `<a href="/apps" class="cv-backlink" id="app-home">← Home</a>`) {
			t.Errorf("GET %s: no back link to Home:\n%s", path, h)
		}
		return h
	}

	t.Run("a running app", func(t *testing.T) {
		h := header(e, "/apps/whoami")
		for _, want := range []string{
			`<form method="post" action="/apps/whoami/restart"><button type="submit" class="cv-btn cv-btn-secondary cv-btn-sm" id="app-restart">`,
			`<a href="/apps/whoami/stop" class="cv-btn cv-btn-secondary cv-btn-sm" id="app-stop">Stop…</a>`,
			`<a href="/apps/whoami/deployments#deploy" class="cv-btn cv-btn-primary cv-btn-sm" id="app-deploy">`,
			`class="cv-domain"`,
		} {
			if !strings.Contains(h, want) {
				t.Errorf("the header lacks %q:\n%s", want, h)
			}
		}
		if strings.Contains(h, `/start"`) {
			t.Error("a running app offers Start")
		}
		// Deploy is a link, never a form that deploys from the header.
		if strings.Contains(h, `action="/apps/whoami/deploy"`) {
			t.Error("the header deploys in one click")
		}
		// Title row order: the name, the pill, the address, then the actions.
		order := []string{`cv-apptitle`, `cv-pill`, `cv-domain`, `cv-apphead-actions`, `class="cv-lede"`, `cv-tabs`}
		last := -1
		for _, s := range order {
			i := strings.Index(h, s)
			if i <= last {
				t.Errorf("%s is out of order in the header", s)
			}
			last = i
		}
	})

	t.Run("a stopped app", func(t *testing.T) {
		s := actionsEnv(t)
		if resp, body := s.postAction(t, "/apps/whoami/stop"); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("stop = %d:\n%s", resp.StatusCode, text(body))
		}
		s.Nomad.SetCopies("whoami")
		h := header(s, "/apps/whoami")
		if !strings.Contains(h, `<form method="post" action="/apps/whoami/start"><button type="submit" class="cv-btn cv-btn-primary cv-btn-sm" id="app-start">Start</button></form>`) {
			t.Errorf("a stopped app has no primary Start:\n%s", h)
		}
		for _, absent := range []string{"</svg>Deploy</a>", "Restart", "Stop…", "/deployments#deploy"} {
			if strings.Contains(h, absent) {
				t.Errorf("a stopped app's header has %q:\n%s", absent, h)
			}
		}
	})
}

// The Running tab carries the copies count and, when the app isn't well, a 6px status mark;
// Deployments carries the live version.
func TestAppTabsCountsAndMark(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)

	_, healthy := e.get(t, "/apps/whoami")
	if !strings.Contains(healthy, `<a href="/apps/whoami" class="cv-tab" id="tab-running" aria-current="page">Running<span class="cv-tab-count">2</span></a>`) ||
		!strings.Contains(healthy, `>Deployments<span class="cv-tab-count">v7</span>`) {
		t.Errorf("tab counts, healthy app:\n%s", appHeadRegion.FindString(healthy))
	}
	if strings.Contains(healthy, "cv-tab-mark") {
		t.Error("a healthy app's Running tab has a status mark")
	}

	// api has one of its two copies healthy.
	_, degraded := e.get(t, "/apps/api")
	if !strings.Contains(degraded, `<span class="cv-mark cv-mark-warning cv-tab-mark" aria-hidden="true"></span><span class="sr-only">1 of 2 healthy: </span>Running`) {
		t.Errorf("a degraded app's Running tab has no warning mark:\n%s", appHeadRegion.FindString(degraded))
	}
}

// Under 900px the top bar's crumbs are hidden, so the back-link row says where an app is:
// "← Home · Shop / staging", the project a link and the environment plain text. An app in
// Default › production reads "← Home" alone. The markup is always there; the stylesheet shows it
// only on narrow screens.
func TestAppHeaderBackRowForNarrowScreens(t *testing.T) {
	e := shopConsole(t)
	e.seed(t, shopWeb("staging", "latest"), seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})

	backRow := regexp.MustCompile(`(?s)<div class="cv-backrow">.*?</div>`)
	_, body := e.get(t, "/apps/shop--staging--web")
	row := backRow.FindString(body)
	if got := text(row); got != "← Home · Shop Floor / staging" {
		t.Errorf("the back-link row reads %q, want the home link and the place", got)
	}
	for _, want := range []string{`<a href="/apps" class="cv-backlink" id="app-home">← Home</a>`,
		`<span class="cv-backplace">`, `<a href="/projects/shop?env=staging" class="cv-backlink">Shop Floor</a>`, `<span>staging</span>`} {
		if !strings.Contains(row, want) {
			t.Errorf("the back-link row lacks %q:\n%s", want, row)
		}
	}
	// The environment is plain text, not a link.
	if strings.Contains(row, `>staging</a>`) {
		t.Error("the environment is a link")
	}

	_, home := e.get(t, "/apps/whoami")
	if got := text(backRow.FindString(home)); got != "← Home" || strings.Contains(home, "cv-backplace") {
		t.Errorf("an app in Default › production has the row %q, want ← Home alone", got)
	}

	// The class hides the place everywhere but under 900px.
	src, err := os.ReadFile("style/input.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(src)
	narrow := strings.Index(css, "@media (max-width: 899.98px)")
	if narrow < 0 || !strings.Contains(css[:narrow], ".cv-backplace { display: none;") || !strings.Contains(css[narrow:], ".cv-backplace { display: inline-flex; }") {
		t.Error("the stylesheet doesn't hide .cv-backplace by default and show it under 900px")
	}
	if built, err := fs.ReadFile(staticFS, "static/app.css"); err != nil || !strings.Contains(string(built), ".cv-backplace") {
		t.Errorf("the built stylesheet lacks .cv-backplace (run tools/ui-css.sh build): %v", err)
	}
}

// The line about the app's other environments is labeled "Other environments".
func TestAppHeaderOtherEnvironmentsLabel(t *testing.T) {
	e := shopConsole(t)
	e.seed(t, shopWeb("staging", "latest"), seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	_, body := e.get(t, "/apps/shop--staging--web")
	if !strings.Contains(body, `<span class="cv-label">Other environments</span>`) || strings.Contains(body, `>Environments</span>`) {
		t.Errorf("the line isn't labeled Other environments:\n%s", appHeadRegion.FindString(body))
	}
}

// "Promote to <env>" is offered only when the cheap facts a page holds say it could work: the
// app is defined and not stopped, its newest deployment is healthy with a recorded definition,
// and the app exists there. Otherwise the environment's name is a plain link. The preview, which
// asks the registry, is never run to draw the header.
func TestHeaderPromoteNeedsItsPreconditions(t *testing.T) {
	staging := shopWeb("staging", "latest")
	deployed := func(id string, status store.Status, def *app.App) seedDeploy {
		return seedDeploy{ID: id, Trigger: app.TriggerManual, Status: status, Version: 1, Definition: def}
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T, e *consoleEnv)
		promote bool
	}{
		{"healthy with a recorded definition", func(t *testing.T, e *consoleEnv) {
			e.seed(t, staging, deployed("s1", store.StatusHealthy, &staging))
		}, true},
		{"no definition recorded", func(t *testing.T, e *consoleEnv) {
			e.seed(t, staging, deployed("s1", store.StatusHealthy, nil))
		}, false},
		{"newest deployment failed", func(t *testing.T, e *consoleEnv) {
			e.seed(t, staging, deployed("s1", store.StatusHealthy, &staging), deployed("s2", store.StatusFailed, &staging))
		}, false},
		{"newest deployment reverted", func(t *testing.T, e *consoleEnv) {
			e.seed(t, staging, deployed("s1", store.StatusHealthy, &staging), deployed("s2", store.StatusReverted, &staging))
		}, false},
		{"still rolling out", func(t *testing.T, e *consoleEnv) {
			e.seed(t, staging, deployed("s1", store.StatusRunning, &staging))
		}, false},
		{"never deployed", func(t *testing.T, e *consoleEnv) {
			e.seed(t, staging)
		}, false},
		{"stopped", func(t *testing.T, e *consoleEnv) {
			e.seed(t, staging, deployed("s1", store.StatusHealthy, &staging))
			now := time.Now()
			if err := e.Store.SetAppStopped(context.Background(), staging.ID, &now); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := shopConsole(t)
			production := shopWeb("production", "latest")
			e.seed(t, production, deployed("p1", store.StatusHealthy, &production))
			tc.setup(t, e)

			regs := e.Nomad.Registrations()
			_, body := e.get(t, "/apps/"+staging.ID)
			header := appHeadRegion.FindString(body)
			if !strings.Contains(header, `<a href="/apps/shop--production--web" class="cv-data">production</a>`) {
				t.Errorf("the other environment isn't a link:\n%s", header)
			}
			if got := strings.Contains(header, `id="promote-production"`); got != tc.promote {
				t.Errorf("Promote offered = %v, want %v", got, tc.promote)
			}
			if strings.Contains(header, "Promote to production") != tc.promote {
				t.Errorf("Promote's label is shown = %v, want %v", !tc.promote, tc.promote)
			}
			if e.Nomad.Registrations() != regs {
				t.Error("drawing the header registered a job")
			}
		})
	}
}

// Restart and Stop… only with a live version: without one they could only be refused.
func TestHeaderRestartAndStopNeedALiveVersion(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 7})

	// Deployed, but Nomad has no healthy copy: nothing to restart or stop; Deploy is still there.
	_, body := e.get(t, "/apps/whoami")
	header := appHeadRegion.FindString(body)
	for _, absent := range []string{`id="app-restart"`, `id="app-stop"`, "Stop…"} {
		if strings.Contains(header, absent) {
			t.Errorf("an app with no live version offers %q:\n%s", absent, header)
		}
	}
	if !strings.Contains(header, `id="app-deploy"`) {
		t.Error("an app with no live version lost Deploy")
	}

	e.copies("whoami", "web", 7, 2, 0)
	_, body = e.get(t, "/apps/whoami")
	header = appHeadRegion.FindString(body)
	for _, want := range []string{`id="app-restart"`, `id="app-stop"`, `id="app-deploy"`} {
		if !strings.Contains(header, want) {
			t.Errorf("an app with a live version lacks %q", want)
		}
	}
}

// The header's buttons and the tabs have stable element IDs, so htmx puts focus back on the same
// one when the live region is swapped. IDs are unique in the header.
func TestAppHeaderStableIDs(t *testing.T) {
	e := shopConsole(t)
	e.seedStandard(t)
	e.seed(t, shopWeb("staging", "latest"), seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})

	_, body := e.get(t, "/apps/whoami")
	header := appHeadRegion.FindString(body)
	for _, want := range []string{`id="app-restart"`, `id="app-stop"`, `id="app-deploy"`, `id="app-home"`,
		`id="tab-running"`, `id="tab-deployments"`, `id="tab-configure"`, `id="tab-logs"`, `id="tab-backups"`} {
		if !strings.Contains(header, want) {
			t.Errorf("the header lacks %s", want)
		}
	}
	// The part htmx fetches draws the same IDs.
	_, part := e.get(t, "/parts/apps/whoami/header")
	for _, id := range []string{"app-restart", "app-stop", "app-deploy", "tab-running", "tab-logs"} {
		if !strings.Contains(part, `id="`+id+`"`) {
			t.Errorf("the header part lacks id %q", id)
		}
	}
	ids := regexp.MustCompile(` id="([^"]+)"`).FindAllStringSubmatch(header, -1)
	seen := map[string]bool{}
	for _, m := range ids {
		if seen[m[1]] {
			t.Errorf("the id %q is used twice in the header", m[1])
		}
		seen[m[1]] = true
	}

	// The environment buttons too.
	_, staging := e.get(t, "/apps/shop--staging--web")
	if !strings.Contains(staging, `id="copy-production"`) {
		t.Errorf("the Copy button has no id:\n%s", appHeadRegion.FindString(staging))
	}
}

// An app page's last crumb is its environment, which isn't "the current page": no aria-current.
// The page itself (Stop, a project) still is.
func TestCrumbsMarkOnlyThePageAsCurrent(t *testing.T) {
	e := shopConsole(t)
	e.seedStandard(t)

	for _, tc := range []struct {
		path    string
		current bool
	}{{"/apps/whoami", false}, {"/apps/whoami/logs", false}, {"/apps/whoami/stop", true}, {"/projects/shop", true}, {"/settings", true}} {
		_, body := e.get(t, tc.path)
		crumbs := crumbLine.FindString(body)
		if got := strings.Contains(crumbs, `aria-current="page"`); got != tc.current {
			t.Errorf("GET %s: a crumb is aria-current = %v, want %v:\n%s", tc.path, got, tc.current, crumbs)
		}
	}
}

// The "App actions" group is drawn only when it holds a button.
func TestNoEmptyAppActionsGroup(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	if _, err := e.Svc.CreateAppOnly(ctx, app.App{ID: "bare"}); err != nil { // no definition: no Deploy, nothing to restart
		t.Fatal(err)
	}
	_, body := e.get(t, "/apps/bare")
	if strings.Contains(body, `aria-label="App actions"`) {
		t.Errorf("an app with no actions has an empty group:\n%s", appHeadRegion.FindString(body))
	}
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 7})
	if _, body = e.get(t, "/apps/whoami"); !strings.Contains(body, `aria-label="App actions"`) {
		t.Error("an app with Deploy has no group")
	}
}

// The Running tab's status mark has hidden text in the pill's words, so a screen reader hears
// what the mark means.
func TestTabMarkHasHiddenText(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t) // blog: healthy copies, newest deploy reverted; api: 1 of 2 healthy
	e.seed(t, blogApp2(), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusFailed, Version: 1})

	for path, word := range map[string]string{
		"/apps/blog":  "deploy failed",
		"/apps/api":   "1 of 2 healthy",
		"/apps/gone2": "not running",
	} {
		_, body := e.get(t, path)
		want := `aria-hidden="true"></span><span class="sr-only">` + word + `: </span>Running`
		if !strings.Contains(body, want) {
			t.Errorf("GET %s: the Running tab lacks %q:\n%s", path, want, appHeadRegion.FindString(body))
		}
	}
	// A healthy app has no mark and no hidden text.
	_, body := e.get(t, "/apps/whoami")
	if strings.Contains(body, "sr-only") {
		t.Error("a healthy app's tab has hidden text")
	}
}

// blogApp2 is a one-service app named gone2, whose only deployment failed after it registered.
func blogApp2() app.App {
	a := whoamiApp()
	a.ID = "gone2"
	a.Services[0].Domains[0].Host = "gone2.example.com"
	return a
}

// A deploy-failed app is failed in the Apps list too: the same pill, sorted first.
func TestAppsListDeployFailedRow(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t) // blog is healthy and its newest deploy (v8) was reverted; api is degraded
	pill := `cv-pill cv-pill-failed cv-pill-quiet"><span class="cv-mark cv-mark-failed" aria-hidden="true"></span>deploy failed`
	for _, path := range []string{"/apps", "/parts/apps"} {
		_, body := e.get(t, path)
		if !strings.Contains(body, pill) {
			t.Errorf("GET %s: no deploy failed pill:\n%s", path, body)
		}
		at := func(id string) int { return strings.Index(body, `href="/apps/`+id+`" class="cv-app-name"`) }
		if !(at("blog") < at("api") && at("api") < at("whoami")) {
			t.Errorf("GET %s: rows aren't sorted failed, warning, healthy (blog %d, api %d, whoami %d)", path, at("blog"), at("api"), at("whoami"))
		}
	}
	// The app's own header says the same.
	_, body := e.get(t, "/apps/blog")
	if !strings.Contains(body, `cv-pill cv-pill-failed"><span class="cv-mark cv-mark-failed" aria-hidden="true"></span>deploy failed`) ||
		!strings.Contains(text(appHeadRegion.FindString(body)), "v8 failed its health checks. v7 keeps running.") {
		t.Errorf("the header of blog:\n%s", text(appHeadRegion.FindString(body)))
	}
}
