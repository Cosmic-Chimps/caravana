package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var deployTokenPattern = regexp.MustCompile(`cvat_[A-Za-z0-9_-]{43}`)

// githubForm is what a browser sends for the GitHub tab's form.
func githubForm(name, image, port, domain, health string) url.Values {
	return postedForm(name, image, port, domain, health)
}

// tokenIn returns the one API token a page shows.
func tokenIn(t *testing.T, body string) string {
	t.Helper()
	found := deployTokenPattern.FindAllString(body, -1)
	if len(found) == 0 {
		t.Fatalf("the page shows no token:\n%s", body)
	}
	for _, f := range found[1:] {
		if f != found[0] {
			t.Fatalf("the page shows two different tokens: %q and %q", found[0], f)
		}
	}
	return found[0]
}

func TestNewAppGitHubTab(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/apps/new?from=github")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/new?from=github = %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`action="/apps/new/github"`,
		`>Create the app and its deploy token</button>`,
		`href="/apps/new?from=github" class="cv-tab" aria-current="page">From a GitHub repository</a>`,
		`href="/apps/new" class="cv-tab">Image</a>`,
		`href="/apps/new?from=compose" class="cv-tab">Compose file</a>`,
		`ghcr.io/you/app`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the GitHub tab lacks %q:\n%s", want, body)
		}
	}
	for _, not := range []string{`hx-post`, `Create and deploy`, `Create without deploying`} {
		if strings.Contains(body, not) {
			t.Errorf("the GitHub tab has %q", not)
		}
	}
	// The other tabs link to it, and the plain tab is current there.
	_, plain := e.get(t, "/apps/new")
	if !strings.Contains(plain, `href="/apps/new?from=github" class="cv-tab">From a GitHub repository</a>`) ||
		!strings.Contains(plain, `href="/apps/new" class="cv-tab" aria-current="page">Image</a>`) {
		t.Errorf("the Image tab doesn't link the GitHub tab, or isn't current:\n%s", plain)
	}
}

func TestCreateFromGitHub(t *testing.T) {
	ctx := context.Background()

	t.Run("creates the app without deploying, makes the token, shows both once", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps/new/github", githubForm("web", "ghcr.io/acme/shop", "8080", "shop.example.com", "/healthz"))
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("POST /apps/new/github = %d (Cache-Control %q), want 200 no-store:\n%s", resp.StatusCode, resp.Header.Get("Cache-Control"), body)
		}
		token := tokenIn(t, body)

		// The app: defined, not deployed, nothing in Nomad.
		got := e.storedApp(t, "web")
		svc := got.Services[0]
		if !got.Defined() || svc.Image != "ghcr.io/acme/shop" || svc.Ports[0].ContainerPort != 8080 ||
			svc.Domains[0].Host != "shop.example.com" || svc.Health == nil || svc.Health.Path != "/healthz" {
			t.Errorf("stored app = %+v, want the typed definition", got)
		}
		if n := len(e.deployments(t, "web")); n != 0 || e.Nomad.Registrations() != 0 {
			t.Errorf("%d deployments, %d registrations; want none", n, e.Nomad.Registrations())
		}

		// The token: scoped to this app name in its project, named after the app.
		scope, err := e.Auth.CheckAPIToken(ctx, token)
		if err != nil || scope == nil || scope.Project != "default" || scope.App != "web" {
			t.Fatalf("the token's scope = %+v, %v; want default/web", scope, err)
		}
		infos, _ := e.Auth.ListAPITokens(ctx)
		if len(infos) != 1 || infos[0].Name != "web-ci" {
			t.Errorf("tokens = %+v, want one named web-ci", infos)
		}

		// The page: the secret to add, and the workflow with the app, the address and the image.
		page := text(body)
		for _, want := range []string{
			"Created web", "CARAVANA_TOKEN", "Copy it now. It won't be shown again.",
			".github/workflows/deploy.yml", "Nothing is deployed yet.",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("the page lacks %q:\n%s", want, page)
			}
		}
		for _, want := range []string{
			"uses: actions/checkout@v7",
			"uses: docker/login-action@v4",
			"password: ${{ secrets.GITHUB_TOKEN }}",
			"uses: docker/build-push-action@v7",
			"tags: ghcr.io/acme/shop:latest",
			"uses: Cosmic-Chimps/caravana-action@v1",
			"url: " + e.URL,
			"token: ${{ secrets.CARAVANA_TOKEN }}",
			"app: web",
			"image: ghcr.io/acme/shop@${{ steps.build.outputs.digest }}",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the workflow lacks %q:\n%s", want, body)
			}
		}
		if strings.Contains(strings.SplitN(body, "id=\"gh-workflow-text\"", 2)[1], token) {
			t.Error("the workflow holds the token itself: it belongs in the repository's secrets")
		}

		// Shown once: no other page has the token's value.
		for _, path := range []string{"/settings", "/apps/web", "/apps/web/configure", "/apps"} {
			if _, page := e.get(t, path); strings.Contains(page, token) {
				t.Errorf("GET %s shows the token again", path)
			}
		}
		if strings.Contains(e.Logs.String(), token) {
			t.Error("the token is in the log")
		}
	})

	t.Run("a place outside Default, a tag, and the configured public address", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.Svc.SetPublicURL("https://caravana.example.com/")
		if _, err := e.Svc.CreateProject(ctx, "shop", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatal(err)
		}
		form := githubForm("web", "ghcr.io/acme/shop:1.4", "", "", "")
		form.Set("place", "shop/staging")
		resp, body := e.postForm(t, "/apps/new/github", form)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, body)
		}
		scope, err := e.Auth.CheckAPIToken(ctx, tokenIn(t, body))
		if err != nil || scope == nil || scope.Project != "shop" || scope.App != "web" {
			t.Errorf("scope = %+v, %v; want shop/web", scope, err)
		}
		for _, want := range []string{"app: shop--staging--web", "url: https://caravana.example.com\n",
			"tags: ghcr.io/acme/shop:latest", "image: ghcr.io/acme/shop@${{"} {
			if !strings.Contains(body, want) {
				t.Errorf("the workflow lacks %q:\n%s", want, body)
			}
		}
		if got := e.storedApp(t, "shop--staging--web"); got.Services[0].Image != "ghcr.io/acme/shop:1.4" {
			t.Errorf("stored image = %q, want what was typed", got.Services[0].Image)
		}
	})

	t.Run("refusals draw the form again and make nothing", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp())
		tests := []struct {
			name string
			form url.Values
			want string
		}{
			{"no image", githubForm("web", "", "", "", ""), "Enter the image your workflow pushes"},
			{"not on ghcr.io", githubForm("web", "traefik/whoami", "", "", ""), "Use an image on"},
			{"no name", githubForm("", "ghcr.io/acme/shop", "", "", ""), "Enter a name for the app."},
			{"a name that is taken", githubForm("whoami", "ghcr.io/acme/shop", "", "", ""), "already exists"},
			{"a domain without a port", githubForm("web", "ghcr.io/acme/shop", "", "shop.example.com", ""), "needs a port"},
			{"a bad port", githubForm("web", "ghcr.io/acme/shop", "x", "", ""), "from 1 to 65535"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				resp, body := e.postForm(t, "/apps/new/github", tt.form)
				if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), tt.want) {
					t.Fatalf("POST = %d, want 422 saying %q:\n%s", resp.StatusCode, tt.want, text(body))
				}
				if !strings.Contains(body, `action="/apps/new/github"`) || deployTokenPattern.MatchString(body) {
					t.Errorf("a refusal must draw the GitHub form again, with no token:\n%s", body)
				}
			})
		}
		if infos, _ := e.Auth.ListAPITokens(ctx); len(infos) != 0 {
			t.Errorf("%d tokens after the refusals, want none", len(infos))
		}
		if _, err := e.Svc.GetApp(ctx, "web"); err == nil {
			t.Error("a refused form created the app")
		}
	})

	t.Run("a token name an active token already has gets a number", func(t *testing.T) {
		e := newConsoleEnv(t)
		if _, err := e.Auth.CreateAPIToken(ctx, "web-ci", nil); err != nil {
			t.Fatal(err)
		}
		resp, body := e.postForm(t, "/apps/new/github", githubForm("web", "ghcr.io/acme/shop", "", "", ""))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, body)
		}
		if !strings.Contains(text(body), "web-ci-2") {
			t.Errorf("the new token isn't web-ci-2: %s", text(body))
		}
	})

	t.Run("needs a session and a same-site post", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, _ := e.do(t, request{method: http.MethodPost, path: "/apps/new/github", form: githubForm("web", "ghcr.io/acme/shop", "", "", ""),
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/login" {
			t.Errorf("without a session = %d to %q, want a redirect to /login", resp.StatusCode, location(t, resp))
		}
		resp, _ = e.do(t, request{method: http.MethodPost, path: "/apps/new/github", form: githubForm("web", "ghcr.io/acme/shop", "", "", ""),
			cookies: []*http.Cookie{e.Cookie}, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("from another site = %d, want 403", resp.StatusCode)
		}
		if _, err := e.Svc.GetApp(ctx, "web"); err == nil {
			t.Error("a refused request created the app")
		}
	})
}

func TestImageRepository(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/you/app":                "ghcr.io/you/app",
		"ghcr.io/you/app:1.4":            "ghcr.io/you/app",
		"ghcr.io/you/app@sha256:abc":     "ghcr.io/you/app",
		"ghcr.io/you/app:1.4@sha256:abc": "ghcr.io/you/app",
		"localhost:5000/you/app":         "localhost:5000/you/app",
		"localhost:5000/you/app:2":       "localhost:5000/you/app",
	} {
		if got := imageRepository(in); got != want {
			t.Errorf("imageRepository(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigureDeployToken(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp())

	_, page := e.get(t, "/apps/whoami/configure")
	for _, want := range []string{`action="/apps/whoami/deploy-token"`, "Create a deploy token for this app"} {
		if !strings.Contains(page, want) {
			t.Errorf("Configure lacks %q", want)
		}
	}
	if deployTokenPattern.MatchString(page) {
		t.Error("Configure shows a token before one is made")
	}

	resp, body := e.postForm(t, "/apps/whoami/deploy-token", url.Values{})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("POST = %d (Cache-Control %q), want 200 no-store:\n%s", resp.StatusCode, resp.Header.Get("Cache-Control"), body)
	}
	token := tokenIn(t, body)
	scope, err := e.Auth.CheckAPIToken(ctx, token)
	if err != nil || scope == nil || scope.Project != "default" || scope.App != "whoami" {
		t.Fatalf("scope = %+v, %v; want default/whoami", scope, err)
	}
	for _, want := range []string{"Copy it now. It won't be shown again.", "whoami-ci", "CARAVANA_TOKEN", "Cosmic-Chimps/caravana-action@v1"} {
		if !strings.Contains(text(body), want) {
			t.Errorf("the answer lacks %q:\n%s", want, text(body))
		}
	}
	if _, again := e.get(t, "/apps/whoami/configure"); strings.Contains(again, token) {
		t.Error("Configure shows the token again")
	}
	if strings.Contains(e.Logs.String(), token) {
		t.Error("the token is in the log")
	}

	// Another click makes another token, and an unknown app is the not-found page.
	resp, body = e.postForm(t, "/apps/whoami/deploy-token", url.Values{})
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "whoami-ci-2") {
		t.Errorf("a second token = %d: %s", resp.StatusCode, text(body))
	}
	if resp, _ = e.postForm(t, "/apps/ghost/deploy-token", url.Values{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown app = %d, want 404", resp.StatusCode)
	}
}

func TestSettingsDeployToken(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	if _, err := e.Svc.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}

	_, page := e.get(t, "/settings")
	for _, want := range []string{"Only for one app", `name="scopeApp"`, `<option value="shop">`} {
		if !strings.Contains(page, want) {
			t.Errorf("Settings lacks %q", want)
		}
	}

	resp, body := e.postForm(t, "/settings/tokens", url.Values{"name": {"shop-web-ci"}, "scopeProject": {"shop"}, "scopeApp": {"web"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /settings/tokens = %d:\n%s", resp.StatusCode, body)
	}
	token := tokenIn(t, body)
	scope, err := e.Auth.CheckAPIToken(ctx, token)
	if err != nil || scope == nil || scope.Project != "shop" || scope.App != "web" {
		t.Errorf("scope = %+v, %v; want shop/web", scope, err)
	}
	if !strings.Contains(body, "/v1/apps/shop--production--web") {
		t.Errorf("the example doesn't read the app:\n%s", text(body))
	}
	_, list := e.get(t, "/settings")
	if !strings.Contains(text(list), "deploys app web of project shop") || strings.Contains(list, token) {
		t.Errorf("the token's row doesn't show its scope, or shows the token:\n%s", text(list))
	}

	// No app name: a full token, as before.
	resp, body = e.postForm(t, "/settings/tokens", url.Values{"name": {"everything"}, "scopeProject": {"shop"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a full token = %d", resp.StatusCode)
	}
	if scope, err := e.Auth.CheckAPIToken(ctx, tokenIn(t, body)); err != nil || scope != nil {
		t.Errorf("a token made without an app name has scope %+v, %v; want none", scope, err)
	}

	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"a project that doesn't exist": {url.Values{"name": {"a"}, "scopeProject": {"nope"}, "scopeApp": {"web"}}, `The project "nope" doesn't exist.`},
		"an app name that isn't one":   {url.Values{"name": {"b"}, "scopeProject": {"shop"}, "scopeApp": {"Web App"}}, "The app name is not a lowercase DNS label"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := e.postForm(t, "/settings/tokens", tc.form)
			if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), tc.want) {
				t.Errorf("POST = %d, want the page saying %q:\n%s", resp.StatusCode, tc.want, text(body))
			}
			if !strings.Contains(body, "<details class=\"cv-confirm\" open>") || deployTokenPattern.MatchString(body) {
				t.Errorf("the scope section must stay open, with no token:\n%s", body)
			}
		})
	}
}

func TestHistoryShowsOrigin(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	e.seed(t, blogApp(), seedDeploy{ID: "plain", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1})
	origin := app.Origin{Commit: "abc1234def5678", Ref: "refs/heads/main", Actor: "ana",
		Run: "https://github.com/acme/shop/actions/runs/212", Repository: "acme/shop"}
	for id, o := range map[string]*app.Origin{"ci": &origin, "tag": {Commit: "9999999", Ref: "refs/tags/v1.2"}} {
		d := app.Deployment{ID: id, Trigger: app.TriggerManual, Digests: map[string]string{"web": "traefik/whoami@" + sampleDigest}, Origin: o}
		if _, err := e.Store.CreateDeployment(ctx, "blog", d, nil, nil, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: new(uint64(2))}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: store.StatusHealthy}); err != nil {
			t.Fatal(err)
		}
	}

	_, body := e.get(t, "/apps/blog/deployments")
	entry := func(id string) string {
		_, rest, _ := strings.Cut(body, `id="deployment-`+id+`"`)
		entry, _, _ := strings.Cut(rest, "</article>")
		return entry
	}
	ci := entry("ci")
	for _, want := range []string{
		`from CI: <span class="cv-inline-mono">abc1234</span> on <span class="cv-inline-mono">main</span> by ana`,
		`<a href="https://github.com/acme/shop/actions/runs/212" rel="noopener noreferrer" target="_blank">run</a>`,
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("the CI deployment lacks %q:\n%s", want, ci)
		}
	}
	if tag := entry("tag"); !strings.Contains(tag, `from CI: <span class="cv-inline-mono">9999999</span> on <span class="cv-inline-mono">v1.2</span></p>`) {
		t.Errorf("a tag with no actor or run reads wrongly:\n%s", tag)
	}
	if plain := entry("plain"); strings.Contains(plain, "from CI") {
		t.Errorf("a deploy without an origin says it came from CI:\n%s", plain)
	}
}
