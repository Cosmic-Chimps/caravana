package ui

import (
	"context"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const sampleDigest = "sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"

// consoleEnv is the console's pages on a real Service, with a real store, a
// fake registry and a fake Nomad, and a signed-in admin. The clock stands
// still at the moment the test started, so "just now" is stable.
type consoleEnv struct {
	*uiEnv
	Svc    *service.Service
	Nomad  *deploytest.Nomad
	Reg    *registrytest.Server
	Server *server
	Cookie *http.Cookie
}

// newConsoleEnv serves the pages with a signed-in admin.
func newConsoleEnv(t *testing.T) *consoleEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	uie := &uiEnv{Store: st, Clock: &fakeClock{t: time.Now()}, Logs: &syncBuffer{}}
	log := slog.New(slog.NewJSONHandler(uie.Logs, nil))
	uie.Auth = service.NewAuth(st, log, service.AuthOptions{Now: uie.Clock.Now, PasswordIterations: 1000})
	if err := uie.Auth.InitAdmin(ctx, testEmail, testPassword); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	nomad := deploytest.New(t)
	nomad.Succeed()
	reg := registrytest.New(t)
	reg.AddIndex("traefik/whoami", "latest", sampleDigest)
	client, err := deploy.NewNomadClient(nomad.Addr(), "")
	if err != nil {
		t.Fatalf("nomad client: %v", err)
	}
	deployer := deploy.New(client, &registry.Client{Endpoints: reg.Endpoints()},
		nomadjob.Settings{Entrypoint: "websecure", DefaultCPUMHz: 100, DefaultMemoryMB: 64})
	deployer.Timing = deploy.Timing{
		ImageTimeout: 5 * time.Second, PollGap: time.Millisecond,
		BackoffStart: time.Millisecond, BackoffMax: 5 * time.Millisecond,
		VersionRetries: []time.Duration{time.Millisecond, time.Millisecond},
	}
	svc := service.New(st, deployer, deploy.NewVariables(client), log)
	svcCtx, stop := context.WithCancel(ctx)
	if err := svc.Start(svcCtx); err != nil {
		t.Fatalf("start service: %v", err)
	}
	t.Cleanup(func() { stop(); svc.Wait() })

	s, err := newServer(Deps{Auth: uie.Auth, Service: svc, Log: log, Host: "caravana.example.com", Now: uie.Clock.Now})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	srv := httptest.NewServer(s.routes())
	t.Cleanup(srv.Close)
	uie.URL = srv.URL

	return &consoleEnv{uiEnv: uie, Svc: svc, Nomad: nomad, Reg: reg, Server: s, Cookie: uie.signIn(t, nil)}
}

// get fetches a page as the signed-in admin.
func (e *consoleEnv) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	return e.do(t, request{path: path, cookies: []*http.Cookie{e.Cookie}})
}

// seedDeploy is one stored deployment of a seeded app.
type seedDeploy struct {
	ID      string
	Trigger app.Trigger
	Status  store.Status
	Version uint64 // the Nomad job version; 0 means it never got one
	Error   string
	Warns   []app.Warning
}

// seed stores the app and its deployments, oldest first, each ending in its status.
func (e *consoleEnv) seed(t *testing.T, a app.App, ds ...seedDeploy) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.Store.SaveApp(ctx, a); err != nil {
		t.Fatalf("SaveApp(%s): %v", a.ID, err)
	}
	digests := map[string]string{}
	for _, svc := range a.Services {
		digests[svc.Name] = "traefik/whoami@" + sampleDigest
	}
	for _, d := range ds {
		if _, err := e.Store.CreateDeployment(ctx, a.ID, app.Deployment{ID: d.ID, Trigger: d.Trigger, Digests: digests}); err != nil {
			t.Fatalf("CreateDeployment(%s): %v", d.ID, err)
		}
		if d.Version != 0 {
			if _, err := e.Store.UpdateDeployment(ctx, d.ID, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: new(d.Version), Warnings: d.Warns}); err != nil {
				t.Fatalf("running %s: %v", d.ID, err)
			}
		}
		if d.Status != store.StatusPending && d.Status != store.StatusRunning {
			if _, err := e.Store.UpdateDeployment(ctx, d.ID, store.DeploymentUpdate{Status: d.Status, Error: d.Error}); err != nil {
				t.Fatalf("%s %s: %v", d.ID, d.Status, err)
			}
		}
	}
}

// copies puts running copies of one service at one job version into the fake Nomad.
func (e *consoleEnv) copies(appID, service string, version uint64, running, failed int) {
	var stubs []api.AllocationListStub
	for range running {
		stubs = append(stubs, api.AllocationListStub{TaskGroup: service, JobVersion: version, ClientStatus: "running", NodeName: "node-1"})
	}
	for range failed {
		stubs = append(stubs, api.AllocationListStub{TaskGroup: service, JobVersion: version, ClientStatus: "failed", NodeName: "node-1"})
	}
	e.Nomad.SetCopies(appID, stubs...)
}

// whoamiApp is the whoami fixture (two copies of one web service) on example.com.
func whoamiApp() app.App {
	a := apptest.Whoami().App
	a.Services[0].Domains[0].Host = "whoami.example.com"
	return a
}

// blogApp has two services: a web service with an address and a worker without one.
func blogApp() app.App {
	return app.App{
		ID: "blog", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{Name: "web", Image: "ghcr.io/example/blog:1.2", Ports: []app.Port{{Name: "http", ContainerPort: 8080}},
				Domains: []app.Domain{{Host: "blog.example.com", Port: "http"}}, Health: &app.HealthCheck{Path: "/"}},
			{Name: "worker", Image: "ghcr.io/example/blog-worker:1.2"},
		},
	}
}

// apiApp is whoami under another name and domain, two copies of it.
func apiApp() app.App {
	a := whoamiApp()
	a.ID, a.Name = "api", ""
	a.Services[0].Domains[0].Host = "api.example.com"
	return a
}

// seedStandard stores three apps: whoami healthy at v7, blog healthy at v7 whose newest
// deploy (v8) was reverted, and api with one of its two copies healthy.
func (e *consoleEnv) seedStandard(t *testing.T) {
	t.Helper()
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
	e.copies("whoami", "web", 7, 2, 0)
	e.seed(t, blogApp(),
		seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 7},
		seedDeploy{ID: "b2", Trigger: app.TriggerWebhook, Status: store.StatusReverted, Version: 8, Error: "health checks failed"})
	e.Nomad.SetCopies("blog",
		api.AllocationListStub{TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-1"},
		api.AllocationListStub{TaskGroup: "worker", JobVersion: 7, ClientStatus: "running", NodeName: "node-1"})
	e.seed(t, apiApp(), seedDeploy{ID: "a1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 3})
	e.copies("api", "web", 3, 1, 1)
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

// h1Text is the text of the page's <h1>, without tags or entities.
func h1Text(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no <h1> in:\n%s", body)
	}
	return strings.Join(strings.Fields(html.UnescapeString(tagPattern.ReplaceAllString(m[1], ""))), " ")
}

func TestAppsEmptyState(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/apps")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps = %d:\n%s", resp.StatusCode, body)
	}
	if got := h1Text(t, body); got != "No apps yet." {
		t.Errorf("headline = %q, want %q", got, "No apps yet.")
	}
	for _, want := range []string{
		"Deploy an image and it shows up here.",
		`<a href="/apps/new" class="cv-btn cv-btn-primary">New app</a>`,
		`aria-current="page">Apps</span>`, // the breadcrumb of this page
		`<a href="/apps" class="cv-nav-link" aria-current="page">Apps</a>`,
		`hx-ext="sse" sse-connect="/live" sse-close="ended" data-live`,
		"Nothing has been deployed yet.", // the activity rail
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the empty Apps page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `role="table"`) {
		t.Error("the empty page draws a table")
	}
	// One primary action in view: the page's own button, not a second one in the top bar.
	if got := strings.Count(body, "New app"); got != 1 {
		t.Errorf(`"New app" appears %d times, want 1`, got)
	}

	t.Run("the action stays hidden until the New app page exists", func(t *testing.T) {
		e.Server.newApp = false
		defer func() { e.Server.newApp = true }()
		_, body := e.get(t, "/apps")
		if strings.Contains(body, "/apps/new") || strings.Contains(body, "New app") {
			t.Errorf("the page links to /apps/new before it exists:\n%s", body)
		}
		if !strings.Contains(body, "Deploy an image and it shows up here.") {
			t.Errorf("the empty state is gone:\n%s", body)
		}
	})
}

func TestAppsListsEveryApp(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)
	resp, body := e.get(t, "/apps")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps = %d:\n%s", resp.StatusCode, body)
	}
	if got, want := h1Text(t, body), "1 app needs you: api has 1 of 2 copies healthy. 2 apps healthy."; got != want {
		t.Errorf("headline = %q, want %q", got, want)
	}

	for _, want := range []string{
		`role="table"`,
		// the app names are links to their pages
		`<a href="/apps/whoami" class="cv-app-name">whoami</a>`, `<a href="/apps/blog" class="cv-app-name">blog</a>`,
		// status mark and word; the degraded app says how many copies
		`cv-mark cv-mark-healthy`, `cv-mark cv-mark-warning`, `1 of 2 healthy`,
		// the live version, and the images, one line per service
		`>v7<`, `>v3<`, `traefik/whoami:latest`, `ghcr.io/example/blog:1.2`, `ghcr.io/example/blog-worker:1.2`,
		// the newest deploy of blog failed: its outcome shows beside the time
		`v8 reverted`,
		// the addresses are https links, with the host as text
		`<a href="https://whoami.example.com" class="cv-data">whoami.example.com</a>`,
		`<a href="https://blog.example.com" class="cv-data">blog.example.com</a>`,
		// the apps' own refresh, and the activity rail's
		`hx-get="/parts/apps" hx-trigger="sse:apps, sse:sync"`, `hx-get="/parts/activity" hx-trigger="sse:activity, sse:sync"`,
		`<a href="/apps/new" class="cv-btn cv-btn-primary cv-btn-sm">New app</a>`, // the top bar action, with apps listed
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Apps page lacks %q:\n%s", want, body)
		}
	}
	// A multi-service app names each service before its image.
	if !regexp.MustCompile(`>worker\s*</span>ghcr\.io/example/blog-worker:1\.2`).MatchString(body) {
		t.Errorf("a service's image isn't labelled with its name:\n%s", body)
	}

	// The Problems-First Rule: the app that needs a human is on top, then the rest by name.
	at := func(id string) int { return strings.Index(body, `href="/apps/`+id+`" class="cv-app-name"`) }
	if at("api") < 0 || !(at("api") < at("blog") && at("blog") < at("whoami")) {
		t.Errorf("rows are not sorted problems first (api %d, blog %d, whoami %d)", at("api"), at("blog"), at("whoami"))
	}
}

func TestAppsHeadlines(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, e *consoleEnv)
		want  string
	}{
		{"all healthy", func(t *testing.T, e *consoleEnv) {
			e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
			e.copies("whoami", "web", 7, 2, 0)
			e.seed(t, blogApp(), seedDeploy{ID: "b1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 2})
			e.Nomad.SetCopies("blog",
				api.AllocationListStub{TaskGroup: "web", JobVersion: 2, ClientStatus: "running"},
				api.AllocationListStub{TaskGroup: "worker", JobVersion: 2, ClientStatus: "running"})
		}, "2 apps healthy."},
		{"one rolling out", func(t *testing.T, e *consoleEnv) {
			e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
			e.copies("whoami", "web", 7, 2, 0)
			e.seed(t, blogApp(),
				seedDeploy{ID: "b1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 2},
				seedDeploy{ID: "b2", Trigger: app.TriggerConsole, Status: store.StatusRunning, Version: 3})
		}, "1 app healthy, 1 rollout in progress."},
		{"one degraded", func(t *testing.T, e *consoleEnv) {
			e.seed(t, apiApp(), seedDeploy{ID: "a1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 3})
			e.copies("api", "web", 3, 1, 1)
		}, "1 app needs you: api has 1 of 2 copies healthy."},
		{"Nomad down", func(t *testing.T, e *consoleEnv) {
			e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
			e.Nomad.Down()
		}, "Nomad can't be reached; showing what caravana last recorded."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newConsoleEnv(t)
			tc.setup(t, e)
			resp, body := e.get(t, "/apps")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /apps = %d:\n%s", resp.StatusCode, body)
			}
			if got := h1Text(t, body); got != tc.want {
				t.Errorf("headline = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("an app that is rolling out says so in its row", func(t *testing.T) {
		e := newConsoleEnv(t)
		tests[1].setup(t, e)
		_, body := e.get(t, "/apps")
		if !strings.Contains(body, "cv-mark cv-mark-rolling") || !strings.Contains(body, "rolling out") {
			t.Errorf("the rollout isn't shown with its mark and word:\n%s", body)
		}
	})
	t.Run("with Nomad down the rows say unknown, with the mark of an idle app", func(t *testing.T) {
		e := newConsoleEnv(t)
		tests[3].setup(t, e)
		_, body := e.get(t, "/apps")
		if !strings.Contains(body, "unknown") || !strings.Contains(body, "cv-mark cv-mark-idle") {
			t.Errorf("an unreadable app isn't shown as unknown:\n%s", body)
		}
	})
}

func TestAppsActivity(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)
	_, body := e.get(t, "/apps")

	// The triggers, in words, and every outcome with its mark.
	for _, want := range []string{"from the console", "through the API", "by webhook", "reverted", "just now"} {
		if !strings.Contains(body, want) {
			t.Errorf("the activity lacks %q:\n%s", want, body)
		}
	}
	// Newest first: api (a1) was deployed last, whoami (w1) first.
	rail := body[strings.Index(body, `hx-get="/parts/activity"`):]
	a1, b2, w1 := strings.Index(rail, `href="/apps/api/deployments"`), strings.Index(rail, `href="/apps/blog/deployments"`), strings.Index(rail, `href="/apps/whoami/deployments"`)
	if a1 < 0 || !(a1 < b2 && b2 < w1) {
		t.Errorf("activity is not newest first (api %d, blog %d, whoami %d)", a1, b2, w1)
	}

	t.Run("an entry opens the Deployments tab, and the app while the tab doesn't exist", func(t *testing.T) {
		if !strings.Contains(rail, `href="/apps/whoami/deployments"`) {
			t.Errorf("entries don't link to the Deployments tab:\n%s", rail)
		}
		saved := e.Server.tabs
		e.Server.tabs = nil
		defer func() { e.Server.tabs = saved }()
		_, body := e.get(t, "/parts/activity")
		if !strings.Contains(body, `href="/apps/whoami"`) || strings.Contains(body, "/deployments") {
			t.Errorf("without the tab, entries link to the app page:\n%s", body)
		}
	})

	t.Run("a deployment that never got a version shows none", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusFailed, Error: "image not found"})
		_, body := e.get(t, "/parts/activity")
		if !strings.Contains(body, "cv-mark cv-mark-failed") || !strings.Contains(body, "failed") || strings.Contains(body, ">v0<") {
			t.Errorf("a deploy that failed before registration is shown wrongly:\n%s", body)
		}
	})
}

// A region's fragment is the same markup the page puts in the region.
func TestPartsRenderTheSameFragmentsAsThePage(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)
	_, page := e.get(t, "/apps")

	for _, path := range []string{"/parts/apps", "/parts/activity"} {
		resp, part := e.get(t, path)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s = %d %q", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if strings.Contains(part, "<html") || strings.Contains(part, "cv-shell") {
			t.Errorf("GET %s answers a whole page:\n%s", path, part)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, got)
		}
		if part = strings.TrimSpace(part); part == "" || !strings.Contains(page, part) {
			t.Errorf("the page doesn't contain what GET %s answers:\n%s", path, part)
		}
	}
	// The activity part holds the entries only; the apps part holds the headline and the table.
	_, apps := e.get(t, "/parts/apps")
	if !strings.Contains(apps, "<h1") || !strings.Contains(apps, `role="table"`) {
		t.Errorf("/parts/apps lacks the headline or the table:\n%s", apps)
	}
}

func TestConsolePagesNeedASession(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)

	for _, path := range []string{"/apps", "/apps/whoami", "/apps/nope"} {
		resp, _ := e.do(t, request{path: path})
		if want := "/login?next=" + strings.ReplaceAll(path, "/", "%2F"); resp.StatusCode != http.StatusSeeOther || location(t, resp) != want {
			t.Errorf("GET %s without a session = %d to %q, want 303 to %q", path, resp.StatusCode, location(t, resp), want)
		}
	}
	for path, from := range map[string]string{
		"/parts/apps":               "/apps",
		"/parts/activity":           "/apps",
		"/parts/apps/whoami/header": "/apps/whoami",
		"/parts/apps/nope/header":   "/apps/nope",
	} {
		resp, body := e.do(t, request{path: path, headers: map[string]string{"HX-Current-URL": e.URL + from}})
		want := "/login?next=" + strings.ReplaceAll(from, "/", "%2F")
		if resp.StatusCode != http.StatusUnauthorized || body != "" || resp.Header.Get("HX-Redirect") != want {
			t.Errorf("GET %s without a session = %d %q, HX-Redirect %q; want 401, empty, %q", path, resp.StatusCode, body, resp.Header.Get("HX-Redirect"), want)
		}
	}

	// A signed-in admin lands on the apps from "/".
	resp, _ := e.do(t, request{path: "/", cookies: []*http.Cookie{e.Cookie}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps" {
		t.Errorf("GET / = %d to %q, want 303 to /apps", resp.StatusCode, location(t, resp))
	}
}

func TestAppPage(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)
	// This test is about the frame: it shows the tabs the test names, and none to begin with.
	e.Server.tabs = nil
	e.Clock.Advance(3*time.Minute + time.Second)

	resp, body := e.get(t, "/apps/whoami")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/whoami = %d:\n%s", resp.StatusCode, body)
	}
	if got := h1Text(t, body); got != "whoami" {
		t.Errorf("headline = %q, want the app's name", got)
	}
	for _, want := range []string{
		// the status, the address, the summary sentence
		`cv-pill cv-pill-healthy`, `<a href="https://whoami.example.com" class="cv-data">whoami.example.com</a>`,
		`2 copies running <span class="cv-inline-mono text-fg-1">v7</span>. Last deployed 3 minutes ago from the console.`,
		// the breadcrumbs, and the stream and the region of this app
		`<a href="/apps">Apps</a>`, `aria-current="page">whoami</span>`,
		`hx-ext="sse" sse-connect="/live/apps/whoami" sse-close="ended" data-live`,
		`hx-get="/parts/apps/whoami/header" hx-trigger="sse:app, sse:deleted, sse:sync"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the app page lacks %q:\n%s", want, body)
		}
	}
	// Only the tabs built so far appear: none yet, and no Deploy action that would lead nowhere.
	for _, absent := range []string{`class="cv-tabs"`, ">Deploy<"} {
		if strings.Contains(body, absent) {
			t.Errorf("the app page shows %q before its story exists:\n%s", absent, body)
		}
	}

	t.Run("summaries by state", func(t *testing.T) {
		for path, want := range map[string]string{
			"/parts/apps/api/header":  "1 of 2 copies healthy, running v3.",
			"/parts/apps/blog/header": "2 copies running v7. Last deployed 3 minutes ago by webhook.",
		} {
			_, part := e.get(t, path)
			if text := strings.Join(strings.Fields(html.UnescapeString(tagPattern.ReplaceAllString(part, ""))), " "); !strings.Contains(text, want) {
				t.Errorf("GET %s says %q, want it to contain %q", path, text, want)
			}
		}
		e.Nomad.Down()
		_, part := e.get(t, "/parts/apps/whoami/header")
		if !strings.Contains(part, "Nomad can&#39;t be reached right now, so caravana can&#39;t say what&#39;s running.") ||
			!strings.Contains(part, "cv-pill cv-pill-idle") {
			t.Errorf("an unreadable app isn't explained:\n%s", part)
		}
	})

	t.Run("tabs built so far are links, the current one marked, and Deploy leads to its form", func(t *testing.T) {
		e.Server.tabs = []tabSpec{
			{Key: "running", Label: "Running", Path: ""},
			{Key: "deployments", Label: "Deployments", Path: "/deployments"},
		}
		defer func() { e.Server.tabs = nil }()
		e.Nomad.Up()
		_, body := e.get(t, "/apps/whoami")
		for _, want := range []string{
			`<nav aria-label="App sections" class="cv-tabs">`,
			`<a href="/apps/whoami" class="cv-tab" aria-current="page">Running<span class="cv-tab-count">2</span></a>`,
			`<a href="/apps/whoami/deployments" class="cv-tab">Deployments<span class="cv-tab-count">v7</span></a>`,
			`<a href="/apps/whoami/deployments" class="cv-btn cv-btn-primary cv-btn-sm">Deploy</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the app page lacks %q:\n%s", want, body)
			}
		}
	})
}

func TestUnknownApp(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)

	resp, body := e.get(t, "/apps/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /apps/nope = %d, want 404", resp.StatusCode)
	}
	if got := h1Text(t, body); got != "There's no app called nope." {
		t.Errorf("headline = %q", got)
	}
	for _, want := range []string{`<span class="cv-inline-mono text-fg-1">nope</span>`, `<a href="/apps">Back to Apps</a>`, "cv-side"} {
		if !strings.Contains(body, want) {
			t.Errorf("the not-found page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "sse-connect") {
		t.Error("the not-found page opens a live stream")
	}

	// An app deleted while its page is open: the header says so, as a normal part.
	resp, part := e.get(t, "/parts/apps/nope/header")
	if resp.StatusCode != http.StatusOK || !strings.Contains(part, `nope</span> was deleted.`) || !strings.Contains(part, `<a href="/apps">Back to Apps</a>`) {
		t.Errorf("GET /parts/apps/nope/header = %d:\n%s", resp.StatusCode, part)
	}
}

func TestAppsNoticeAfterDelete(t *testing.T) {
	e := newConsoleEnv(t)
	_, body := e.get(t, "/apps?deleted=blog")
	if want := `<span class="cv-data text-fg-1">blog</span> was deleted.`; !strings.Contains(body, want) {
		t.Errorf("the page lacks the notice %q:\n%s", want, body)
	}
	if !strings.Contains(body, `role="status"`) {
		t.Error("the notice isn't announced")
	}
	// Anything that isn't an app ID is not echoed.
	_, body = e.get(t, "/apps?deleted="+`%3Cb%3Ehi%3C%2Fb%3E`)
	if strings.Contains(body, "was deleted") || strings.Contains(body, "<b>hi") {
		t.Errorf("a made-up value is shown:\n%s", body)
	}
}

func TestLiveStreamsOfTheConsole(t *testing.T) {
	e := newConsoleEnv(t)

	t.Run("/live sends apps and activity, and nothing about one app", func(t *testing.T) {
		ls := openStream(t, e.URL, "/live", e.Cookie.Value)
		ls.expect("sync")
		if _, err := e.Svc.Deploy(context.Background(), whoamiApp(), app.TriggerManual); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		seen := map[string]bool{}
		for !(seen["apps"] && seen["activity"]) {
			name := ls.next()
			if name != "apps" && name != "activity" {
				t.Fatalf("event %q on /live, want only apps and activity", name)
			}
			seen[name] = true
		}
	})

	t.Run("/live/apps/{id} sends that app's notices", func(t *testing.T) {
		ls := openStream(t, e.URL, "/live/apps/whoami", e.Cookie.Value)
		ls.expect("sync")
		if _, err := e.Svc.Deploy(context.Background(), whoamiApp(), app.TriggerManual); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		// The route sends app, deployments, copies and deleted; a deploy changes the first two.
		for seen := false; !seen; {
			switch name := ls.next(); name {
			case "app":
				seen = true
			case "deployments", "copies":
			default:
				t.Fatalf("event %q, want app, deployments or copies", name)
			}
		}
	})

	t.Run("without a session the stream says ended at once", func(t *testing.T) {
		for _, path := range []string{"/live", "/live/apps/whoami"} {
			ls := openStream(t, e.URL, path, "")
			ls.expect("ended")
			ls.expectClosed()
		}
	})
}

// Every console page follows the CSP rules (research R3) and works as plain HTML:
// the data is in the markup, not fetched by script.
func TestConsolePagesObeyThePolicyAndNeedNoScript(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)
	e.Server.tabs = []tabSpec{{Key: "running", Label: "Running"}, {Key: "deployments", Label: "Deployments", Path: "/deployments"}}

	for _, path := range []string{"/apps", "/parts/apps", "/parts/activity", "/apps/whoami", "/parts/apps/whoami/header", "/apps/nope"} {
		_, body := e.get(t, path)
		for _, banned := range []string{" style=", "<style", "hx-on", "javascript:", " onclick="} {
			if strings.Contains(body, banned) {
				t.Errorf("GET %s contains %q", path, banned)
			}
		}
		if regexp.MustCompile(`hx-trigger="[^"]*[\[{]`).MatchString(body) {
			t.Errorf("GET %s has a trigger filter, which needs eval", path)
		}
		for _, tag := range regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(tag, ` src="/static/js/`) {
				t.Errorf("GET %s has an inline script: %s", path, tag)
			}
		}
	}

	// Without script the page still lists the apps and links to them: no region starts empty.
	_, body := e.get(t, "/apps")
	for _, region := range []string{`hx-get="/parts/apps"`, `hx-get="/parts/activity"`} {
		i := strings.Index(body, region)
		if i < 0 {
			t.Fatalf("no region %s", region)
		}
		if after := body[i:]; !strings.Contains(after[:min(len(after), 600)], "cv-") {
			t.Errorf("the region %s starts empty and waits for script", region)
		}
	}
	if !strings.Contains(body, `href="/apps/whoami"`) {
		t.Error("the table isn't in the markup")
	}
}

// foreignJobError is what the engine records when Nomad already runs a job of the app's name that
// caravana didn't create.
const foreignJobError = `job conflict: Nomad already runs a job "whoami" that Caravana didn't create; stop it, or deploy under another app ID`

// whoami's copies are healthy (they belong to the foreign job), yet its newest deploy never started.
func failedDeployEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: foreignJobError})
	e.copies("whoami", "web", 7, 2, 0)
	e.seed(t, apiApp(), seedDeploy{ID: "a1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 3})
	e.copies("api", "web", 3, 2, 0)
	return e
}

func TestAppsFailedDeployNeedsYouEvenWithHealthyCopies(t *testing.T) {
	e := failedDeployEnv(t)
	_, body := e.get(t, "/apps")

	if got, want := h1Text(t, body), "1 app needs you: whoami has a failed deploy. 1 app healthy."; got != want {
		t.Errorf("headline = %q, want %q", got, want)
	}
	// Problems first: whoami sorts above api, which by name it would follow.
	at := func(id string) int { return strings.Index(body, `href="/apps/`+id+`" class="cv-app-name"`) }
	if at("whoami") < 0 || at("api") < 0 || at("whoami") > at("api") {
		t.Errorf("the app with the failed deploy isn't first (whoami %d, api %d)", at("whoami"), at("api"))
	}
	// Its "deploy failed" is a link to that deployment on the Deployments tab, with the failed mark.
	row := body[at("whoami"):at("api")]
	want := `<a href="/apps/whoami/deployments#deployment-w1" class="cv-outcome"><span class="cv-mark cv-mark-failed" aria-hidden="true"></span>deploy failed</a>`
	if !strings.Contains(row, want) {
		t.Errorf("the Last deploy cell lacks %q:\n%s", want, row)
	}
	// The deployment entry the link points at exists.
	_, tab := e.get(t, "/apps/whoami/deployments")
	if !strings.Contains(tab, `id="deployment-w1"`) {
		t.Error("the link's target isn't on the Deployments tab")
	}
	// A healthy last deploy has no link: nothing to say.
	if strings.Contains(body[at("api"):], `class="cv-outcome"`) {
		t.Error("an app whose deploy went well has an outcome link")
	}
}

func TestAppsCellsKeepTheirColumnNamesWhenStacked(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t)
	_, body := e.get(t, "/apps")
	row := regexp.MustCompile(`(?s)<div class="cv-row cv-apps-row".*?</div>\s*</div>`).FindString(body)
	if row == "" {
		t.Fatalf("no table row in:\n%s", body)
	}
	// Each cell after the name and the status has the name of its column: the stacked layout shows
	// it (data-label) where the column heads are gone.
	heads := regexp.MustCompile(`role="columnheader" class="(cv-c-[a-z]+)">([^<]+)<`).FindAllStringSubmatch(body, -1)
	if len(heads) != 6 {
		t.Fatalf("%d column heads, want 6", len(heads))
	}
	for _, h := range heads {
		class, label := h[1], h[2]
		if class == "cv-c-status" || class == "cv-c-name" {
			continue
		}
		if !strings.Contains(row, `class="`+class+` `) && !strings.Contains(row, `class="`+class+`" `) {
			t.Errorf("row has no %s cell:\n%s", class, row)
			continue
		}
		if !regexp.MustCompile(`class="` + class + `[^"]*" data-label="` + label + `"`).MatchString(row) {
			t.Errorf("the %s cell isn't labelled %q:\n%s", class, label, row)
		}
	}
}
