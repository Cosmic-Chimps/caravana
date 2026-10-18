package ui

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const otherDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// postForm sends a same-origin form post as the signed-in admin.
func (e *consoleEnv) postForm(t *testing.T, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	return e.post(t, e.Cookie, path, form)
}

// duoApp is two services on one image each: web with an address and a health check, and a
// worker. Both carry fields a deploy from the console must leave alone.
func duoApp() app.App {
	two := 2
	return app.App{
		ID: "duo", Name: "Duo", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{Name: "web", Image: "traefik/whoami:latest", Replicas: &two,
				Env:     []app.EnvVar{{Name: "MODE", Value: "plain"}},
				Ports:   []app.Port{{Name: "http", ContainerPort: 80}},
				Domains: []app.Domain{{Host: "duo.example.com", Port: "http"}},
				Health:  &app.HealthCheck{Path: "/"}},
			{Name: "worker", Image: "traefik/whoami:latest", Args: []string{"--name", "worker"},
				Resources: app.Resources{MemoryMB: 128}},
		},
	}
}

func (e *consoleEnv) storedApp(t *testing.T, id string) app.App {
	t.Helper()
	rec, err := e.Store.GetApp(context.Background(), id)
	if err != nil {
		t.Fatalf("GetApp(%s): %v", id, err)
	}
	return rec.App
}

func (e *consoleEnv) deployments(t *testing.T, appID string) []store.DeploymentRecord {
	t.Helper()
	recs, err := e.Store.ListDeployments(context.Background(), appID, 0)
	if err != nil {
		t.Fatalf("ListDeployments(%s): %v", appID, err)
	}
	return recs
}

// text is a body's visible text, tags and entities gone, whitespace collapsed.
func text(body string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagPattern.ReplaceAllString(body, ""))), " ")
}

// historyEnv has an app, blog, with one deployment of each kind. The newest, v5, is still
// rolling out, and Nomad says two copies of it are wanted and one is healthy.
func historyEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seed(t, blogApp(),
		seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1},
		seedDeploy{ID: "d2", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 2},
		seedDeploy{ID: "d3", Trigger: app.TriggerWebhook, Status: store.StatusReverted, Version: 3,
			Error: "Failed due to unhealthy allocations - rolling back to job version 2",
			Warns: []app.Warning{{Field: "services[0].image", Message: `tag "1.2" is ignored: the digest pins the image`}}},
		seedDeploy{ID: "d4", Trigger: app.TriggerConsole, Status: store.StatusFailed,
			Error: "image not found, or private: add credentials for ghcr.io in Settings › Registries"},
		seedDeploy{ID: "d5", Trigger: app.TriggerManual, Status: store.StatusSuperseded, Version: 4,
			Error: "Cancelled due to newer version of job"},
		seedDeploy{ID: "d6", Trigger: app.TriggerConsole, Status: store.StatusRunning, Version: 5})
	for range 6 { // job versions 0 to 5
		e.Nomad.AddForeignJob(&api.Job{ID: new("blog")})
	}
	e.Nomad.Script("blog", 5, deploytest.Rolling("Deployment is running", map[string]api.DeploymentState{
		"web": {DesiredTotal: 2, PlacedAllocs: 1, HealthyAllocs: 1},
	}))
	return e
}

func TestDeploymentsTabListsTheHistory(t *testing.T) {
	e := historyEnv(t)
	e.Clock.Advance(40 * time.Second)

	resp, body := e.get(t, "/apps/blog/deployments")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/blog/deployments = %d:\n%s", resp.StatusCode, body)
	}

	// The app's header with its tabs, the Deployments tab marked as the current one.
	for _, want := range []string{
		`<a href="/apps/blog/deployments" class="cv-tab" aria-current="page">Deployments`,
		`hx-get="/parts/apps/blog/header?tab=deployments"`,
		`aria-current="page">Deployments</span>`, // the breadcrumb
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the tab lacks %q:\n%s", want, body)
		}
	}

	// Entries, newest first.
	recs := e.deployments(t, "blog")
	var last int
	for i, rec := range recs { // recs is newest first, like the page
		at := strings.Index(body, `id="deployment-`+rec.Deployment.ID+`"`)
		if at < 0 || (i > 0 && at < last) {
			t.Fatalf("entry %s is missing or out of order (at %d, previous at %d)", rec.Deployment.ID, at, last)
		}
		last = at
	}
	if len(recs) != 6 || recs[0].Deployment.ID != "d6" {
		t.Fatalf("seeded %d deployments, newest %s", len(recs), recs[0].Deployment.ID)
	}

	entry := func(id string) string {
		i := strings.Index(body, `id="deployment-`+id+`"`)
		if i < 0 {
			t.Fatalf("no entry %s", id)
		}
		rest := body[i:]
		if j := strings.Index(rest[1:], `id="deployment-`); j >= 0 {
			rest = rest[:j+1]
		}
		return rest
	}

	t.Run("each entry says what started it, which version, and which digest", func(t *testing.T) {
		d2 := text(entry("d2"))
		for _, want := range []string{"v2", "healthy", "through the API", "sha256:c4717a8d1f01", "took "} {
			if !strings.Contains(d2, want) {
				t.Errorf("entry d2 lacks %q: %s", want, d2)
			}
		}
		if !strings.Contains(entry("d2"), `title="traefik/whoami@`+sampleDigest+`"`) {
			t.Errorf("the full digest isn't in a title:\n%s", entry("d2"))
		}
		for id, want := range map[string]string{"d1": "from the console", "d3": "by webhook"} {
			if got := text(entry(id)); !strings.Contains(got, want) {
				t.Errorf("entry %s lacks %q: %s", id, want, got)
			}
		}
		// A multi-service app names the service next to each digest.
		if !strings.Contains(text(entry("d2")), "web sha256:") || !strings.Contains(text(entry("d2")), "worker sha256:") {
			t.Errorf("digests don't name their services: %s", text(entry("d2")))
		}
	})

	t.Run("the explanation for each status, with the raw error under it", func(t *testing.T) {
		for id, want := range map[string][]string{
			"d1": {"v1 rolled out healthy."},
			"d2": {"v2 rolled out healthy."},
			"d3": {"v3 failed its health checks. Nomad rolled back to v2.",
				"Failed due to unhealthy allocations - rolling back to job version 2"},
			"d4": {"This deploy didn't start: the image wasn't found.",
				"image not found, or private: add credentials for ghcr.io in Settings › Registries"},
			"d5": {"v4 was replaced by v5 before it finished.", "Cancelled due to newer version of job"},
			"d6": {"Rolling out v5."},
		} {
			got := text(entry(id))
			for _, w := range want {
				if !strings.Contains(got, w) {
					t.Errorf("entry %s lacks %q: %s", id, w, got)
				}
			}
		}
		// The raw error is mono detail, not prose.
		if !regexp.MustCompile(`<[a-z]+ class="cv-data-muted[^"]*"[^>]*>\s*Failed due to unhealthy allocations`).MatchString(entry("d3")) {
			t.Errorf("the raw error isn't set in mono:\n%s", entry("d3"))
		}
	})

	t.Run("status marks come with their words", func(t *testing.T) {
		for id, want := range map[string]string{
			"d2": `cv-pill-healthy`, "d3": `cv-pill-failed`, "d4": `cv-pill-failed`, "d5": `cv-pill-idle`, "d6": `cv-pill-rolling`,
		} {
			if !strings.Contains(entry(id), want) {
				t.Errorf("entry %s lacks %s", id, want)
			}
		}
		for id, word := range map[string]string{"d3": "reverted", "d4": "failed", "d5": "superseded", "d6": "rolling out"} {
			if !strings.Contains(text(entry(id)), word) {
				t.Errorf("entry %s lacks the word %q", id, word)
			}
		}
	})

	t.Run("warnings are listed", func(t *testing.T) {
		got := text(entry("d3"))
		if !strings.Contains(got, `services[0].image`) || !strings.Contains(got, `tag "1.2" is ignored: the digest pins the image`) {
			t.Errorf("entry d3 lacks its warning: %s", got)
		}
		if strings.Contains(text(entry("d2")), "Warning") {
			t.Errorf("an entry without warnings shows a warnings block: %s", text(entry("d2")))
		}
	})

	t.Run("the running entry shows its duration and Nomad's progress", func(t *testing.T) {
		got := text(entry("d6"))
		for _, want := range []string{"running for ", "1 of 2 new copies started, 1 healthy. Old copies keep serving until the new ones pass their checks."} {
			if !strings.Contains(got, want) {
				t.Errorf("entry d6 lacks %q: %s", want, got)
			}
		}
		if strings.Contains(text(entry("d5")), "new copies started") {
			t.Error("a final entry shows progress")
		}
	})

	t.Run("the region is polite live and refreshes on deployments and copies", func(t *testing.T) {
		for _, want := range []string{
			`hx-get="/parts/apps/blog/deployments"`, `hx-trigger="sse:deployments, sse:copies, sse:sync"`, `aria-live="polite"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the history region lacks %q", want)
			}
		}
	})

	t.Run("an app without deployments says so", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp())
		_, body := e.get(t, "/apps/whoami/deployments")
		if got := text(body); !strings.Contains(got, "No deployments yet.") || strings.Contains(body, "Show older") {
			t.Errorf("the empty history is wrong: %s", got)
		}
		if !strings.Contains(body, `<form method="post" action="/apps/whoami/deploy"`) {
			t.Error("an app without deployments has no deploy form")
		}
	})
}

func TestDeploymentsTabIsTheDeployForm(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, duoApp(), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})

	_, body := e.get(t, "/apps/duo/deployments")
	for _, want := range []string{
		`<form method="post" action="/apps/duo/deploy"`,
		`name="image.web"`, `value="traefik/whoami:latest"`, `name="image.worker"`,
		`class="cv-btn cv-btn-primary"`,
		"Keep the image to deploy it again and pick up a new push to the same tag, or change the tag.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the deploy form lacks %q:\n%s", want, body)
		}
	}
	// Each field has a label naming its service.
	if !regexp.MustCompile(`(?s)<label[^>]*>.*?web.*?<input[^>]*name="image.web"`).MatchString(body) {
		t.Error("the web image field has no label")
	}
	// The form is a plain form: nothing in it needs script, and the page has one primary action.
	form := body[strings.Index(body, `<form method="post" action="/apps/duo/deploy"`):]
	form = form[:strings.Index(form, "</form>")]
	if strings.Contains(form, "hx-") || strings.Contains(form, "onsubmit") {
		t.Errorf("the deploy form depends on script:\n%s", form)
	}
	if n := strings.Count(body, "cv-btn-primary"); n != 1 {
		t.Errorf("the page has %d primary buttons, want 1 (DESIGN.md: at most one per view)", n)
	}
}

func TestDeploymentsTabPages(t *testing.T) {
	e := historyEnv(t)
	e.Server.deployPage = 2

	hrefs := func(body string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`href="(/apps/blog/deployments\?before=[^"]+)"`).FindAllStringSubmatch(body, -1) {
			out = append(out, html.UnescapeString(m[1]))
		}
		return out
	}
	ids := func(body string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`id="deployment-(d\d)"`).FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
		return out
	}

	_, body := e.get(t, "/apps/blog/deployments")
	if got := ids(body); !reflect.DeepEqual(got, []string{"d6", "d5"}) {
		t.Fatalf("the first page = %v, want d6, d5", got)
	}
	older := hrefs(body)
	if len(older) != 1 || !strings.Contains(body, "Show older") {
		t.Fatalf("the first page's links to older = %v", older)
	}
	recs := e.deployments(t, "blog")
	if want := "/apps/blog/deployments?before=" + url.QueryEscape(recs[1].CreatedAt.UTC().Format(time.RFC3339Nano)); older[0] != want {
		t.Errorf("Show older = %q, want %q (the oldest shown entry's start)", older[0], want)
	}

	_, body = e.get(t, older[0])
	if got := ids(body); !reflect.DeepEqual(got, []string{"d4", "d3"}) {
		t.Fatalf("the second page = %v, want d4, d3", got)
	}
	// An older page doesn't claim anything about what is serving now.
	if strings.Contains(text(body), "keeps serving") || strings.Contains(text(body), "is live") {
		t.Errorf("an older page says what is serving: %s", text(body))
	}
	older = hrefs(body)
	if len(older) != 1 {
		t.Fatalf("the second page's links to older = %v", older)
	}
	// Its region refreshes the same page, not the first one.
	if want := `hx-get="/parts/apps/blog/deployments?before=`; !strings.Contains(body, want) {
		t.Errorf("an older page's region doesn't refresh itself:\n%s", body)
	}

	_, body = e.get(t, older[0])
	if got := ids(body); !reflect.DeepEqual(got, []string{"d2", "d1"}) {
		t.Fatalf("the last page = %v, want d2, d1", got)
	}
	if len(hrefs(body)) != 0 || strings.Contains(body, "Show older") {
		t.Errorf("the last page offers older entries:\n%s", body)
	}

	t.Run("a bad cursor is a 400, never a redirect", func(t *testing.T) {
		for _, q := range []string{"yesterday", "", "2026-13-45T00:00:00Z"} {
			resp, body := e.get(t, "/apps/blog/deployments?before="+url.QueryEscape(q))
			if q == "" {
				if resp.StatusCode != http.StatusOK { // an empty cursor is no cursor
					t.Errorf("empty before = %d, want 200", resp.StatusCode)
				}
				continue
			}
			if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
				t.Errorf("before=%q = %d (Location %q), want 400:\n%s", q, resp.StatusCode, resp.Header.Get("Location"), body)
			}
		}
	})
}

func TestDeploymentsPartIsTheRegionOfThePage(t *testing.T) {
	e := historyEnv(t)
	e.Server.deployPage = 4
	_, page := e.get(t, "/apps/blog/deployments")

	for _, path := range []string{"/parts/apps/blog/deployments", "/parts/apps/blog/deployments?before=" + url.QueryEscape(
		e.deployments(t, "blog")[3].CreatedAt.UTC().Format(time.RFC3339Nano))} {
		resp, part := e.get(t, path)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s = %d %q", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if strings.Contains(part, "<html") || strings.Contains(part, "cv-shell") || strings.Contains(part, "<form") {
			t.Errorf("GET %s answers more than the history:\n%s", path, part)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, got)
		}
		if !strings.Contains(part, `id="deployment-`) {
			t.Errorf("GET %s has no entries:\n%s", path, part)
		}
		if strings.HasSuffix(path, "/deployments") { // the first page is what the page shows
			if part = strings.TrimSpace(part); !strings.Contains(page, part) {
				t.Errorf("the page doesn't contain what GET %s answers:\n%s", path, part)
			}
		}
	}

	t.Run("an app deleted meanwhile", func(t *testing.T) {
		resp, part := e.get(t, "/parts/apps/nope/deployments")
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(part), "nope was deleted.") {
			t.Errorf("GET /parts/apps/nope/deployments = %d:\n%s", resp.StatusCode, part)
		}
	})

	t.Run("needs a session, and says so the htmx way", func(t *testing.T) {
		resp, _ := e.do(t, request{path: "/parts/apps/blog/deployments", headers: map[string]string{"HX-Current-URL": e.URL + "/apps/blog/deployments"}})
		if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("HX-Redirect"), "/login") {
			t.Errorf("without a session = %d, HX-Redirect %q; want 401 and a redirect to /login", resp.StatusCode, resp.Header.Get("HX-Redirect"))
		}
	})
}

func TestDeploymentsTabOfAnUnknownApp(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/apps/nope/deployments")
	if resp.StatusCode != http.StatusNotFound || h1Text(t, body) != "There's no app called nope." {
		t.Errorf("GET /apps/nope/deployments = %d, headline %q", resp.StatusCode, h1Text(t, body))
	}
	resp, _ = e.postForm(t, "/apps/nope/deploy", url.Values{})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /apps/nope/deploy = %d, want 404", resp.StatusCode)
	}
	// Both need a session.
	for _, r := range []request{
		{path: "/apps/nope/deployments"},
		{method: http.MethodPost, path: "/apps/nope/deploy", form: url.Values{}, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}},
	} {
		resp, _ := e.do(t, r)
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("%s %s without a session = %d to %q, want a redirect to /login", orGet(r.method), r.path, resp.StatusCode, location(t, resp))
		}
	}
}

func orGet(method string) string {
	if method == "" {
		return http.MethodGet
	}
	return method
}

func TestDeployFromTheConsole(t *testing.T) {
	t.Run("unchanged images deploy the stored app again", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, duoApp(), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
		before := e.storedApp(t, "duo")
		regs := e.Nomad.Registrations()

		resp, body := e.postForm(t, "/apps/duo/deploy", url.Values{"image.web": {"traefik/whoami:latest"}, "image.worker": {"traefik/whoami:latest"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duo/deployments" {
			t.Fatalf("POST = %d to %q, want 303 to /apps/duo/deployments:\n%s", resp.StatusCode, location(t, resp), body)
		}
		recs := e.deployments(t, "duo")
		if len(recs) != 2 || recs[0].Deployment.Trigger != app.TriggerConsole {
			t.Fatalf("deployments = %d, newest trigger %q; want a new one with trigger console", len(recs), recs[0].Deployment.Trigger)
		}
		if e.Nomad.Registrations() != regs+1 {
			t.Errorf("Nomad got %d registrations, want 1 more", e.Nomad.Registrations()-regs)
		}
		if after := e.storedApp(t, "duo"); !reflect.DeepEqual(after, before) {
			t.Errorf("the stored app changed:\n got %+v\nwant %+v", after, before)
		}
		// The next page load shows the new entry.
		_, page := e.get(t, "/apps/duo/deployments")
		if !strings.Contains(page, `id="deployment-`+recs[0].Deployment.ID+`"`) || !strings.Contains(text(page), "from the console") {
			t.Errorf("the new deployment isn't on the tab:\n%s", text(page))
		}
	})

	t.Run("a form without any image field is a redeploy too", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, duoApp())
		resp, _ := e.postForm(t, "/apps/duo/deploy", url.Values{})
		if resp.StatusCode != http.StatusSeeOther || len(e.deployments(t, "duo")) != 1 {
			t.Errorf("POST with no fields = %d, %d deployments; want 303 and 1", resp.StatusCode, len(e.deployments(t, "duo")))
		}
	})

	t.Run("a changed image deploys the stored app with only that image replaced", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.Reg.AddIndex("traefik/whoami", "v1.10", otherDigest)
		e.seed(t, duoApp(), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
		before := e.storedApp(t, "duo")

		// Whitespace around a value is not part of it.
		resp, body := e.postForm(t, "/apps/duo/deploy", url.Values{"image.web": {"  traefik/whoami:v1.10 "}, "image.worker": {"traefik/whoami:latest"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duo/deployments" {
			t.Fatalf("POST = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
		}
		want := before
		want.Services = append([]app.Service(nil), before.Services...)
		want.Services[0].Image = "traefik/whoami:v1.10"
		if after := e.storedApp(t, "duo"); !reflect.DeepEqual(after, want) {
			t.Errorf("the stored app isn't the old one with web's image replaced:\n got %+v\nwant %+v", after, want)
		}
		recs := e.deployments(t, "duo")
		if len(recs) != 2 || recs[0].Deployment.Trigger != app.TriggerConsole {
			t.Fatalf("deployments = %d, newest trigger %q", len(recs), recs[0].Deployment.Trigger)
		}
		// The new digest is what shows.
		deadline := time.Now().Add(5 * time.Second)
		for {
			rec, _ := e.Store.GetDeployment(context.Background(), recs[0].Deployment.ID)
			if rec.Deployment.Digests["web"] == "traefik/whoami@"+otherDigest {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("web's digest = %q, want the new one", rec.Deployment.Digests["web"])
			}
			time.Sleep(2 * time.Millisecond)
		}
		_, page := e.get(t, "/apps/duo/deployments")
		if !strings.Contains(page, "sha256:0123456789ab") {
			t.Errorf("the new digest isn't on the tab:\n%s", text(page))
		}
		// The form shows the stored image, now the new one.
		if !strings.Contains(page, `name="image.web" value="traefik/whoami:v1.10"`) {
			t.Errorf("the form doesn't show the new image:\n%s", page)
		}
	})

	t.Run("an image that doesn't exist is an error on its field, with what was typed kept", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, duoApp(), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
		before := e.storedApp(t, "duo")

		resp, body := e.postForm(t, "/apps/duo/deploy", url.Values{"image.web": {"traefik/whoami:latest"}, "image.worker": {"traefik/nope:1"}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, body)
		}
		for _, want := range []string{
			`name="image.worker" value="traefik/nope:1"`, `name="image.web" value="traefik/whoami:latest"`,
			`aria-invalid="true"`, `role="alert"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q:\n%s", want, body)
			}
		}
		// The error sits with worker's field, and web's has none.
		worker := body[strings.Index(body, `name="image.worker"`):]
		if !strings.Contains(text(worker[:strings.Index(worker, "</label>")]), "image not found") {
			t.Errorf("the worker field has no error:\n%s", worker)
		}
		web := body[strings.Index(body, `name="image.web"`):strings.Index(body, `name="image.worker"`)]
		if strings.Contains(web, `role="alert"`) || strings.Contains(web, "aria-invalid") {
			t.Errorf("the web field is marked as wrong:\n%s", web)
		}
		// Deploy stores the app before it resolves the images (feature 003), so the typed
		// image is now the app's own: the declarative model. Nothing else of it changed.
		want := before
		want.Services = append([]app.Service(nil), before.Services...)
		want.Services[1].Image = "traefik/nope:1"
		if after := e.storedApp(t, "duo"); !reflect.DeepEqual(after, want) {
			t.Errorf("the stored app isn't the old one with the typed image:\n got %+v\nwant %+v", after, want)
		}
		if recs := e.deployments(t, "duo"); len(recs) != 2 || recs[0].Status != store.StatusFailed {
			t.Errorf("deployments = %+v, want the failed attempt recorded", recs)
		}
	})

	t.Run("an image that isn't a reference is an error on its field", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, duoApp())
		resp, body := e.postForm(t, "/apps/duo/deploy", url.Values{"image.web": {"Not A Ref!!"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `value="Not A Ref!!"`) || !strings.Contains(body, `role="alert"`) {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, body)
		}
		if len(e.deployments(t, "duo")) != 0 {
			t.Error("a deploy that failed validation left a deployment")
		}
	})

	t.Run("an empty image is an error on its field", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, duoApp())
		resp, body := e.postForm(t, "/apps/duo/deploy", url.Values{"image.web": {"  "}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `role="alert"`) {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, body)
		}
	})

	t.Run("fields that aren't images are ignored", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, duoApp())
		before := e.storedApp(t, "duo")
		resp, _ := e.postForm(t, "/apps/duo/deploy", url.Values{
			"image.web": {"traefik/whoami:latest"}, "image.ghost": {"evil/image:1"}, "id": {"other"}, "services[0].replicas": {"9"}, "env.MODE": {"x"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST = %d, want 303", resp.StatusCode)
		}
		if after := e.storedApp(t, "duo"); !reflect.DeepEqual(after, before) {
			t.Errorf("extra form fields changed the app:\n got %+v\nwant %+v", after, before)
		}
	})

	t.Run("Nomad down: the error is a sentence, the attempt is in the history", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, duoApp())
		e.Nomad.Down()
		resp, body := e.postForm(t, "/apps/duo/deploy", url.Values{"image.web": {"traefik/whoami:latest"}})
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("POST = %d, want 502:\n%s", resp.StatusCode, body)
		}
		if got := text(body); !strings.Contains(got, "Nomad couldn't be reached") || !strings.Contains(body, `role="alert"`) {
			t.Errorf("the page doesn't explain it: %s", got)
		}
		recs := e.deployments(t, "duo")
		if len(recs) != 1 || recs[0].Status != store.StatusFailed {
			t.Errorf("deployments = %+v, want one failed", recs)
		}
		if !strings.Contains(text(body), "This deploy didn't start: Nomad couldn't be reached.") {
			t.Errorf("the failed attempt isn't in the history: %s", text(body))
		}
	})

	t.Run("a cross-site post is refused and changes nothing", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, duoApp())
		resp, body := e.do(t, request{method: http.MethodPost, path: "/apps/duo/deploy", form: url.Values{"image.web": {"traefik/whoami:latest"}},
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, cookies: []*http.Cookie{e.Cookie}})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "refused") {
			t.Errorf("cross-site POST = %d, want the 403 page", resp.StatusCode)
		}
		if n := len(e.deployments(t, "duo")); n != 0 {
			t.Errorf("a cross-site post started %d deployments", n)
		}
	})
}

// A deploy from the console is a plain form post: no script is needed, and nothing in
// the request is something only script can send.
func TestDeployWorksAsAPlainFormPost(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})

	_, page := e.get(t, "/apps/whoami/deployments")
	m := regexp.MustCompile(`<form method="post" action="(/apps/whoami/deploy)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no deploy form:\n%s", page)
	}
	// What a browser sends for that form: its fields, urlencoded, and nothing from htmx.
	resp, _ := e.postForm(t, m[1], url.Values{"image.web": {"traefik/whoami:latest"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami/deployments" {
		t.Errorf("POST %s = %d to %q, want 303 to the tab", m[1], resp.StatusCode, location(t, resp))
	}
	if resp.Header.Get("HX-Redirect") != "" {
		t.Error("the plain post got an htmx-only answer")
	}
}

func TestDeploymentsTabObeysThePolicy(t *testing.T) {
	e := historyEnv(t)
	for _, path := range []string{"/apps/blog/deployments", "/parts/apps/blog/deployments"} {
		_, body := e.get(t, path)
		for _, banned := range []string{" style=", "<style", "hx-on", "javascript:", " onclick=", " onsubmit="} {
			if strings.Contains(body, banned) {
				t.Errorf("GET %s contains %q", path, banned)
			}
		}
		if regexp.MustCompile(`hx-trigger="[^"]*[\[{]`).MatchString(body) {
			t.Errorf("GET %s has a trigger filter, which needs eval", path)
		}
	}
	// Env values never reach a page (FR-015).
	e2 := newConsoleEnv(t)
	a := duoApp()
	a.Services[0].Env = []app.EnvVar{{Name: "DB_PASSWORD", Value: "hunter2-not-for-pages"}}
	e2.seed(t, a, seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	_, body := e2.get(t, "/apps/duo/deployments")
	if strings.Contains(body, "hunter2-not-for-pages") {
		t.Error("an env value is on the page")
	}
}

func TestLiveStreamOfAnAppSendsDeploymentsAndCopies(t *testing.T) {
	e := newConsoleEnv(t)
	ls := openStream(t, e.URL, "/live/apps/whoami", e.Cookie.Value)
	ls.expect("sync")
	if _, err := e.Svc.Deploy(context.Background(), whoamiApp(), app.TriggerManual); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	seen := map[string]bool{}
	for !(seen["app"] && seen["deployments"]) {
		name := ls.next()
		if name != "app" && name != "deployments" && name != "copies" {
			t.Fatalf("event %q, want app, deployments or copies", name)
		}
		seen[name] = true
	}
	e.Nomad.SetCopies("whoami", api.AllocationListStub{TaskGroup: "web", ClientStatus: "running"})
	for !seen["copies"] {
		if name := ls.next(); name == "copies" {
			seen[name] = true
		}
	}
}

// ---- Running tab (/apps/{id}) ------------------------------------------------------------

// runningEnv has whoami at v7 with copies of every kind: two healthy (one restarted twice), one
// still starting, one failing its checks, and one that ended. All of them are v7, the newest.
func runningEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
	started := e.Clock.Now().Add(-90 * time.Second)
	healthy, unhealthy := true, false
	run := func(id string, restarts uint64, health *bool) api.AllocationListStub {
		a := api.AllocationListStub{ID: id, TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-1",
			TaskStates: map[string]*api.TaskState{"web": {Restarts: restarts, StartedAt: started}}}
		if health != nil {
			a.DeploymentStatus = &api.AllocDeploymentStatus{Healthy: health}
		}
		return a
	}
	e.Nomad.SetCopies("whoami",
		run("1111aaaa-0000-4000-8000-000000000001", 0, nil),
		run("2222bbbb-0000-4000-8000-000000000002", 2, &healthy),
		api.AllocationListStub{ID: "3333cccc-0000-4000-8000-000000000003", TaskGroup: "web", JobVersion: 7, ClientStatus: "pending", NodeName: "node-2"},
		run("4444dddd-0000-4000-8000-000000000004", 0, &unhealthy),
		api.AllocationListStub{ID: "5555eeee-0000-4000-8000-000000000005", TaskGroup: "web", JobVersion: 7, ClientStatus: "complete", NodeName: "node-1"})
	return e
}

// copyRow is the markup of one copy's row, found by its full ID in the title attribute.
func copyRow(t *testing.T, body, fullID string) string {
	t.Helper()
	i := strings.Index(body, `title="`+fullID+`"`)
	if i < 0 {
		t.Fatalf("no row for copy %s in:\n%s", fullID, body)
	}
	start := strings.LastIndex(body[:i], `<div class="cv-row`)
	end := strings.Index(body[i:], `<div class="cv-row`)
	if end < 0 {
		end = len(body) - i
	}
	return body[start : i+end]
}

func TestRunningTabListsTheCopies(t *testing.T) {
	e := runningEnv(t)
	resp, body := e.get(t, "/apps/whoami")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/whoami = %d:\n%s", resp.StatusCode, body)
	}

	t.Run("the tabs: Running first and current, with the count of healthy copies", func(t *testing.T) {
		running := `<a href="/apps/whoami" class="cv-tab" aria-current="page">Running<span class="cv-tab-count">2</span></a>`
		deployments := `<a href="/apps/whoami/deployments" class="cv-tab">Deployments<span class="cv-tab-count">v7</span></a>`
		r, d := strings.Index(body, running), strings.Index(body, deployments)
		if r < 0 || d < 0 || r > d {
			t.Errorf("tabs: Running at %d, Deployments at %d; want both, Running first:\n%s", r, d, body)
		}
		if strings.Contains(body, `hx-get="/parts/apps/whoami/header?tab=`) {
			t.Error("the header region of the Running tab asks for another tab's header")
		}
		if !strings.Contains(body, `hx-get="/parts/apps/whoami/header"`) {
			t.Errorf("the header region doesn't refresh from the Running tab's part:\n%s", body)
		}
	})

	t.Run("every copy is a row: short ID with the full one in title, service, status, version, restarts, up for", func(t *testing.T) {
		row := copyRow(t, body, "1111aaaa-0000-4000-8000-000000000001")
		for _, want := range []string{
			`<span class="cv-data text-fg-1" title="1111aaaa-0000-4000-8000-000000000001">1111aaaa</span>`,
			`web`, `cv-pill-healthy`, `>healthy<`, `v7`, `1 min 30 s`,
		} {
			if !strings.Contains(row, want) {
				t.Errorf("the row lacks %q:\n%s", want, row)
			}
		}
		if got := text(row); !strings.Contains(got, "1111aaaa web healthy v7 0 1 min 30 s") {
			t.Errorf("the row reads %q, want ID, service, status, version, restarts, up for in that order", got)
		}
		if strings.Contains(row, "cv-restarts-warn") {
			t.Error("a copy that never restarted is marked")
		}
		// A copy that restarted is marked, with the number in the same cell.
		restarted := copyRow(t, body, "2222bbbb-0000-4000-8000-000000000002")
		if !strings.Contains(restarted, `class="cv-data cv-restarts-warn">2<`) {
			t.Errorf("restarts of 2 aren't marked:\n%s", restarted)
		}
	})

	t.Run("each status has its word and its shape", func(t *testing.T) {
		for id, want := range map[string][]string{
			"3333cccc-0000-4000-8000-000000000003": {"cv-pill-rolling", ">starting<", "not started"},
			"4444dddd-0000-4000-8000-000000000004": {"cv-pill-warning", ">unhealthy<", "1 min 30 s"},
			"5555eeee-0000-4000-8000-000000000005": {"cv-pill-idle", ">stopped<", "not running"},
		} {
			row := copyRow(t, body, id)
			for _, w := range want {
				if !strings.Contains(row, w) {
					t.Errorf("row %s lacks %q:\n%s", id[:8], w, row)
				}
			}
		}
		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "6666ffff-0000-4000-8000-000000000006", TaskGroup: "web", JobVersion: 7, ClientStatus: "failed", NodeName: "node-1"})
		_, failedBody := e.get(t, "/apps/whoami")
		if row := copyRow(t, failedBody, "6666ffff-0000-4000-8000-000000000006"); !strings.Contains(row, "cv-pill-failed") || !strings.Contains(row, ">failed<") {
			t.Errorf("a failed copy isn't marked failed:\n%s", row)
		}
	})

	t.Run("a column header names each cell", func(t *testing.T) {
		for _, want := range []string{`role="table"`, ">Copy<", ">Service<", ">Status<", ">Version<", ">Restarts<", ">Up for<", ">CPU<", ">Memory<"} {
			if !strings.Contains(body, want) {
				t.Errorf("the table lacks %q", want)
			}
		}
	})

	t.Run("the region is live: it refreshes on copies and on a (re)connect", func(t *testing.T) {
		if !strings.Contains(body, `hx-get="/parts/apps/whoami/copies" hx-trigger="sse:copies, sse:usage, sse:sync" hx-swap="innerHTML"`) {
			t.Errorf("the copies region doesn't listen for its notices:\n%s", body)
		}
	})

	t.Run("the part is the region of the page", func(t *testing.T) {
		resp, part := e.get(t, "/parts/apps/whoami/copies")
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if strings.Contains(part, "<html") || strings.Contains(part, "cv-shell") || strings.Contains(part, "cv-tabs") {
			t.Errorf("the part answers more than the table:\n%s", part)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		// The page above was loaded before the failed copy replaced the list; read it again to compare.
		_, page := e.get(t, "/apps/whoami")
		if part = strings.TrimSpace(part); part == "" || !strings.Contains(page, part) {
			t.Errorf("the page doesn't contain what the part answers:\n%s", part)
		}
	})

	t.Run("a rollout shows old and new copies, each with its version", func(t *testing.T) {
		e.Nomad.SetCopies("whoami",
			api.AllocationListStub{ID: "7777aaaa-0000-4000-8000-000000000007", TaskGroup: "web", JobVersion: 8, ClientStatus: "pending", NodeName: "node-2"},
			api.AllocationListStub{ID: "8888bbbb-0000-4000-8000-000000000008", TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-1"})
		_, part := e.get(t, "/parts/apps/whoami/copies")
		newer, older := copyRow(t, part, "7777aaaa-0000-4000-8000-000000000007"), copyRow(t, part, "8888bbbb-0000-4000-8000-000000000008")
		if !strings.Contains(newer, "v8") || !strings.Contains(older, "v7") || strings.Index(part, "7777aaaa") > strings.Index(part, "8888bbbb") {
			t.Errorf("old and new copies aren't both shown, newest version first:\n%s", part)
		}
	})
}

func TestRunningTabSaysWhyThereAreNoCopies(t *testing.T) {
	t.Run("an app that was never deployed", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp())
		_, body := e.get(t, "/apps/whoami")
		got := text(body)
		if !strings.Contains(got, "Waiting for its first deploy.") || strings.Contains(got, "No copies are running.") {
			t.Errorf("the tab doesn't say the app is waiting for its first deploy: %s", got)
		}
		if strings.Contains(got, "see Deployments") || strings.Contains(body, `role="table"`) {
			t.Errorf("the tab blames a deploy that never happened, or draws an empty table: %s", got)
		}
	})

	t.Run("the last deploy failed: the sentence names its version and points at Deployments", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(),
			seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 7},
			seedDeploy{ID: "w2", Trigger: app.TriggerManual, Status: store.StatusFailed, Version: 8, Error: "deadline exceeded"})
		_, part := e.get(t, "/parts/apps/whoami/copies")
		if got := text(part); !strings.Contains(got, "No copies are running. v8 failed: see Deployments.") {
			t.Errorf("part = %s", got)
		}
		if !strings.Contains(part, `<a href="/apps/whoami/deployments">Deployments</a>`) {
			t.Errorf("the sentence doesn't link to the Deployments tab:\n%s", part)
		}
	})

	t.Run("a failed deploy that never got a version, and a reverted one", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusFailed, Error: "image not found"})
		_, part := e.get(t, "/parts/apps/whoami/copies")
		if got := text(part); !strings.Contains(got, "The last deploy failed: see Deployments.") {
			t.Errorf("part = %s", got)
		}
		e2 := newConsoleEnv(t)
		e2.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusReverted, Version: 3, Error: "unhealthy"})
		_, part = e2.get(t, "/parts/apps/whoami/copies")
		if got := text(part); !strings.Contains(got, "v3 was reverted: see Deployments.") {
			t.Errorf("part = %s", got)
		}
	})

	t.Run("a rollout that hasn't placed a copy yet is not blamed", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusRunning, Version: 1})
		_, part := e.get(t, "/parts/apps/whoami/copies")
		if got := text(part); !strings.Contains(got, "No copies are running.") || strings.Contains(got, "failed") {
			t.Errorf("part = %s", got)
		}
	})

	t.Run("Nomad down: copies can't be shown, and the page still opens", func(t *testing.T) {
		e := runningEnv(t)
		e.Nomad.Down()
		resp, body := e.get(t, "/apps/whoami")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /apps/whoami = %d, want the page", resp.StatusCode)
		}
		for _, p := range []string{body, func() string { _, part := e.get(t, "/parts/apps/whoami/copies"); return part }()} {
			if got := text(p); !strings.Contains(got, "Nomad can't be reached right now, so copies can't be shown.") {
				t.Errorf("Nomad being down isn't explained: %s", got)
			}
			if strings.Contains(p, "No copies are running.") {
				t.Error("an unreadable Nomad is reported as no copies")
			}
		}
	})

	t.Run("an app deleted while its page is open", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, part := e.get(t, "/parts/apps/nope/copies")
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(part), "nope was deleted.") {
			t.Errorf("GET /parts/apps/nope/copies = %d:\n%s", resp.StatusCode, part)
		}
	})

	t.Run("an unknown app's page is the 404 page", func(t *testing.T) {
		e := newConsoleEnv(t)
		if resp, _ := e.get(t, "/apps/nope"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /apps/nope = %d, want 404", resp.StatusCode)
		}
	})
}

func TestRunningPartNeedsASession(t *testing.T) {
	e := newConsoleEnv(t)
	resp, _ := e.do(t, request{path: "/parts/apps/whoami/copies", headers: map[string]string{"HX-Current-URL": e.URL + "/apps/whoami"}})
	if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("HX-Redirect"), "/login") {
		t.Errorf("without a session = %d, HX-Redirect %q; want 401 and a redirect to /login", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
}

func TestRunningTabObeysThePolicy(t *testing.T) {
	e := runningEnv(t)
	for _, path := range []string{"/apps/whoami", "/parts/apps/whoami/copies"} {
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
}

func TestFailedNewestDeploymentOffersOneAction(t *testing.T) {
	e := failedDeployEnv(t)
	_, body := e.get(t, "/apps/whoami/deployments")
	got := text(body)

	// The cause has its own sentence, in lowercase caravana, which doesn't blame a change
	// made while deploying; the engine's own words stay under it.
	for _, want := range []string{
		"This deploy didn't start: a job named whoami already exists in Nomad and caravana didn't create it. The running version, if any, was left alone.",
		foreignJobError,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "changed while deploying") {
		t.Errorf("the entry contradicts itself:\n%s", got)
	}
	// One action, a verb that says the outcome, leading to the deploy form on this tab.
	if n := strings.Count(body, `class="cv-deploy-action"`); n != 1 {
		t.Errorf("%d actions on the entry, want 1", n)
	}
	if !strings.Contains(body, `<a href="#deploy" class="cv-btn cv-btn-secondary cv-btn-sm">Deploy again</a>`) {
		t.Errorf("the failed entry has no Deploy again action:\n%s", body)
	}
	if !strings.Contains(body, `<section id="deploy" aria-labelledby="deploy-title"`) {
		t.Error("the action's target (#deploy) isn't the deploy form")
	}

	t.Run("an older failure and a healthy entry have none", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(),
			seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: "image not found"},
			seedDeploy{ID: "w2", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 2})
		_, body := e.get(t, "/apps/whoami/deployments")
		if strings.Contains(body, "cv-deploy-action") {
			t.Errorf("an entry that isn't the newest failure has an action:\n%s", body)
		}
	})

	t.Run("a failure of the image asks for the image", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: "image not found, or private"})
		_, body := e.get(t, "/apps/whoami/deployments")
		if !strings.Contains(body, `class="cv-btn cv-btn-secondary cv-btn-sm">Change the image</a>`) {
			t.Errorf("no Change the image action:\n%s", body)
		}
	})
}

// A deploy refused because Nomad already runs a job of that name says so, not "changed while deploying".
func TestDeployRefusedForAJobCaravanaDidNotCreate(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp())
	e.Nomad.AddForeignJob(&api.Job{ID: new("whoami")})
	resp, body := e.postForm(t, "/apps/whoami/deploy", url.Values{"image.web": {"traefik/whoami:latest"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST = %d, want 409:\n%s", resp.StatusCode, text(body))
	}
	got := text(body)
	if !strings.Contains(got, "A job with this name already exists in Nomad and caravana didn't create it, so nothing was deployed.") ||
		strings.Contains(got, "changed while deploying. Nothing was lost") {
		t.Errorf("the refusal is %s", got)
	}
}

// DESIGN.md: an app's top bar has no Deploy; a tab's Deploy is its own form's
// button, so pressing it deploys what the page holds. Deployments has exactly
// one; the other tabs have none in the top bar and at most one of their own.
func TestAppTabsDeployOnlyFromTheirForm(t *testing.T) {
	e := runningEnv(t)
	primaryDeploy := regexp.MustCompile(`class="cv-btn cv-btn-primary[^"]*"[^>]*>Deploy</`)
	// whoami is an image app: its Configure form saves (and so deploys) with
	// its own Save button, not one called Deploy.
	want := map[string]int{"": 0, "/deployments": 1, "/configure": 0, "/logs": 0}
	for tab, n := range want {
		_, body := e.get(t, "/apps/whoami"+tab)
		if !strings.Contains(body, `<div class="cv-top-action"></div>`) {
			t.Errorf("tab %q has an action in the top bar", tab)
		}
		if got := len(primaryDeploy.FindAllString(body, -1)); got != n {
			t.Errorf("tab %q has %d primary Deploy buttons, want %d", tab, got, n)
		}
	}
}
