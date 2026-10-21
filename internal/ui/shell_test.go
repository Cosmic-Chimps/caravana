package ui

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The three parts of the frame, cut from a page, for the shell tests.
var (
	sidebarRegion = regexp.MustCompile(`(?s)<aside class="cv-side">.*?</aside>`)
	navItem       = regexp.MustCompile(`<a href="([^"]*)" class="cv-nav-item"( aria-current="page")?>.*?<span>([^<]*)</span></a>`)
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

// The sidebar has exactly Home, Projects and Settings on every page, nothing
// that has no page yet, and the right one marked: Home on /apps, Projects on
// the projects and on every page of an app, Settings on /settings. The top bar
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
		{"/projects/new", "Projects", "Projects /New project"},
		{"/apps/whoami", "Projects", "Default /production"},
		{"/apps/whoami/deployments", "Projects", "Default /production"},
		{"/apps/whoami/configure", "Projects", "Default /production"},
		{"/apps/whoami/logs", "Projects", "Default /production"},
		{"/apps/whoami/stop", "Projects", "Default /production /whoami /Stop"},
		{"/settings", "Settings", "Settings"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, body := e.get(t, tc.path)
			if resp.StatusCode != 200 {
				t.Fatalf("GET %s = %d:\n%s", tc.path, resp.StatusCode, text(body))
			}

			want := []string{"Home=/apps", "Projects=/projects", "Settings=/settings"}
			for i, item := range want {
				if strings.HasPrefix(item, tc.active+"=") {
					want[i] += "*"
				}
			}
			if got := navOf(t, body); !slices.Equal(got, want) {
				t.Errorf("sidebar = %v, want %v", got, want)
			}
			side := text(sidebarRegion.FindString(body))
			for _, absent := range []string{"Deploys", "Previews", "Logs", "Monitoring", "Schedules", "Domains", "Backups", "Developer", "Operator"} {
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
		if !strings.Contains(h, `<a href="/apps" class="cv-backlink">← Home</a>`) {
			t.Errorf("GET %s: no back link to Home:\n%s", path, h)
		}
		return h
	}

	t.Run("a running app", func(t *testing.T) {
		h := header(e, "/apps/whoami")
		for _, want := range []string{
			`<form method="post" action="/apps/whoami/restart"><button type="submit" class="cv-btn cv-btn-secondary cv-btn-sm">`,
			`<a href="/apps/whoami/stop" class="cv-btn cv-btn-secondary cv-btn-sm">Stop…</a>`,
			`<a href="/apps/whoami/deployments#deploy" class="cv-btn cv-btn-primary cv-btn-sm">`,
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
		if !strings.Contains(h, `<form method="post" action="/apps/whoami/start"><button type="submit" class="cv-btn cv-btn-primary cv-btn-sm">Start</button></form>`) {
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
	if !strings.Contains(healthy, `<a href="/apps/whoami" class="cv-tab" aria-current="page">Running<span class="cv-tab-count">2</span></a>`) ||
		!strings.Contains(healthy, `>Deployments<span class="cv-tab-count">v7</span>`) {
		t.Errorf("tab counts, healthy app:\n%s", appHeadRegion.FindString(healthy))
	}
	if strings.Contains(healthy, "cv-tab-mark") {
		t.Error("a healthy app's Running tab has a status mark")
	}

	// api has one of its two copies healthy.
	_, degraded := e.get(t, "/apps/api")
	if !strings.Contains(degraded, `<span class="cv-mark cv-mark-warning cv-tab-mark" aria-hidden="true"></span>Running`) {
		t.Errorf("a degraded app's Running tab has no warning mark:\n%s", appHeadRegion.FindString(degraded))
	}
}
