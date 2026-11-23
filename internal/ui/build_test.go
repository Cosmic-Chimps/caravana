package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 050, console: New app › Build from Git, Next steps, Configure's Build panel and Git access, the
// Build stage and the build log page, and Settings › Builds. A deploytest.BuildPlayer stands in for Git and
// BuildKit, as it does in the service's tests. The sentinels are a token and a private key that must be on
// no page.

const (
	uiBuildCommit = "4e0a77c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7"
	uiBuildDigest = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
	uiTokenSent   = "s3cret-sentinel-050-ui-token"
	uiShopImage   = "127.0.0.1:5001/acme/web" // a registry on this machine: no registry entry is needed
	uiShopRepo    = "http://127.0.0.1:8098/app.git"
)

// buildEnv is a console with a player of builds: every build job succeeds as play says (nil: a clean build).
func buildEnv(t *testing.T, play func(deploytest.PlayedBuild) deploytest.BuildPlay) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.Svc.SetBuildTimingForTests(5*time.Millisecond, 0)
	if play == nil {
		play = func(deploytest.PlayedBuild) deploytest.BuildPlay { return succeedingUIBuild() }
	}
	deploytest.PlayBuilds(t, e.Nomad, play)
	return e
}

func succeedingUIBuild() deploytest.BuildPlay {
	return deploytest.BuildPlay{Commit: uiBuildCommit, Author: "Ana Ñandú", Subject: "Retry supplier timeouts", Digest: uiBuildDigest,
		SourceOut: "caravana-build: cloned commit " + uiBuildCommit + "\n", BuildOut: "#1 [1/2] FROM busybox\n#2 pushing layers\n"}
}

// gitForm is what a browser sends for the Build from Git tab.
func gitFormValues(name, repo, image string) url.Values {
	return url.Values{"name": {name}, "repository": {repo}, "branch": {"main"}, "access": {"public"}, "image": {image},
		"port": {"8080"}, "domain": {name + ".example.com"}, "healthPath": {"/"}}
}

// seedBuilt stores a built app, shop, with the service web, and returns its definition.
func (e *consoleEnv) seedBuilt(t *testing.T, repo, access string) app.App {
	t.Helper()
	a := app.App{ID: "shop", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
		Name: "web", Image: uiShopImage, Ports: []app.Port{{Name: "http", ContainerPort: 8080}},
		Domains: []app.Domain{{Host: "shop.example.com", Port: "http"}}, Health: &app.HealthCheck{Path: "/"},
		Build: &app.Build{Repository: repo, Access: access}}}}
	if _, err := e.Svc.CreateAppOnly(context.Background(), a); err != nil {
		t.Fatalf("CreateAppOnly: %v", err)
	}
	return a
}

// seedBuild stores a deployment of shop with its build, ended as end says (nil: still queued), and the
// deployment's own end (nil: still pending).
func (e *consoleEnv) seedBuild(t *testing.T, depID string, end *store.BuildEnd, dep *store.DeploymentUpdate) {
	t.Helper()
	ctx := context.Background()
	rec, err := e.Store.GetApp(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Store.CreateDeploymentWithBuild(ctx, "shop", app.Deployment{ID: depID, Trigger: app.TriggerConsole}, &rec.App, nil, "",
		store.BuildRecord{ID: "b-" + depID, Service: "web", Repository: uiShopRepo, Branch: "main", Image: uiShopImage})
	if err != nil {
		t.Fatalf("CreateDeploymentWithBuild: %v", err)
	}
	if end != nil {
		if err := e.Store.EndBuild(ctx, "b-"+depID, *end); err != nil {
			t.Fatalf("EndBuild: %v", err)
		}
	}
	if dep != nil {
		if dep.Status == store.StatusHealthy {
			if _, err := e.Store.UpdateDeployment(ctx, depID, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: new(uint64(1))}); err != nil {
				t.Fatalf("running: %v", err)
			}
		}
		if _, err := e.Store.UpdateDeployment(ctx, depID, *dep); err != nil {
			t.Fatalf("UpdateDeployment: %v", err)
		}
	}
}

// secretsIn are the secrets a page of shop must never show: the deploy key's private half as the Nomad variable
// holds it, and a token.
func (e *consoleEnv) gitSecrets(t *testing.T, tokens ...string) []string {
	t.Helper()
	out := append([]string(nil), tokens...)
	for _, svc := range []string{"web", "shop"} { // the service of a seeded app, and the one New app names like the app
		v := e.Nomad.Variable(deploy.GitAccessPath("shop", svc))
		if k := v[deploy.GitAccessPrivateKey]; k != "" {
			out = append(out, k, strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(k), "-----END OPENSSH PRIVATE KEY-----"), "-----BEGIN OPENSSH PRIVATE KEY-----")), "OPENSSH PRIVATE KEY")
		}
		if tok := v[deploy.GitAccessToken]; tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// noSecretOnAnyPage fetches the pages of shop and fails when one holds a secret.
func (e *consoleEnv) noSecretOnAnyPage(t *testing.T, secrets ...string) {
	t.Helper()
	paths := []string{"/apps/shop", "/apps/shop/configure", "/apps/shop/deployments", "/apps/new?from=git", "/settings/builds", "/settings/registries"}
	for _, d := range e.deployments(t, "shop") {
		paths = append(paths, "/apps/shop/deployments/"+d.Deployment.ID+"/build", "/parts/apps/shop/deployments/"+d.Deployment.ID+"/build")
	}
	for _, path := range paths {
		_, body := e.get(t, path)
		for _, s := range e.gitSecrets(t, secrets...) {
			if s != "" && strings.Contains(body, s) {
				t.Errorf("%s holds a secret (%.20q…)", path, s)
			}
		}
	}
}

func TestNewAppGitTab(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/apps/new?from=git")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/new?from=git = %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`action="/apps/new/git"`,
		`href="/apps/new?from=git" class="cv-tab" aria-current="page">Build from Git</a>`,
		`href="/apps/new" class="cv-tab">Image</a>`,
		`name="repository"`, `name="branch" value="main"`, `name="folder"`, `name="dockerfile"`,
		`name="access" value="public" checked`, `name="access" value="deploy-key"`, `name="access" value="token"`,
		`name="image"`, `name="port"`, `name="domain"`, `name="healthPath"`,
		`name="action" value="deploy" class="cv-btn cv-btn-primary">Build and deploy</button>`,
		`name="action" value="create" class="cv-btn cv-btn-secondary">Create, then add access</button>`,
		`Settings › Registries`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Build from Git tab lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "hx-post") {
		t.Error("the Build from Git form posts through htmx: it is a plain post")
	}
	// The other tabs link to it, and none of them is current there.
	_, plain := e.get(t, "/apps/new")
	if !strings.Contains(plain, `href="/apps/new?from=git" class="cv-tab">Build from Git</a>`) || !strings.Contains(plain, `href="/apps/new" class="cv-tab" aria-current="page">Image</a>`) {
		t.Errorf("the Image tab doesn't link the Git tab, or isn't current:\n%s", plain)
	}
}

func TestBuildFromGitDeploysAndShowsItsBuild(t *testing.T) {
	e := buildEnv(t, nil)
	resp, body := e.postForm(t, "/apps/new/git", gitFormValues("shop", uiShopRepo, uiShopImage))
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop/deployments" {
		t.Fatalf("POST /apps/new/git = %d to %q, want 303 to the Deployments tab:\n%s", resp.StatusCode, location(t, resp), body)
	}
	got := e.storedApp(t, "shop").Services[0]
	if got.Build == nil || got.Build.Repository != uiShopRepo || got.Build.Branch != "main" || got.Build.Access != app.BuildAccessPublic || got.Image != uiShopImage {
		t.Fatalf("stored service = %+v, want the typed source with its defaults", got)
	}
	recs := e.deployments(t, "shop")
	if len(recs) != 1 || recs[0].Build == nil {
		t.Fatalf("deployments = %+v, want one with a build", recs)
	}
	dep := recs[0].Deployment.ID
	e.waitDeploy(t, dep)

	_, page := e.get(t, "/apps/shop/deployments")
	words := text(page)
	for _, want := range []string{
		"Build and deploy the newest commit of main from " + uiShopRepo + ", and push it to " + uiShopImage + ".",
		"Build · 4e0a77c Retry supplier timeouts · main · Build log",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the Deployments tab lacks %q:\n%s", want, words)
		}
	}
	if !strings.Contains(page, `>Build and deploy</button>`) || strings.Contains(page, `name="image.web"`) || strings.Contains(page, "Keep the image to deploy it again") {
		t.Error("the deploy form of a built service still asks for an image")
	}
	// The strip has four stages, Build first, and the build is done.
	if !regexpHas(page, `(?s)aria-label="Stages".*?Build.*?Queued.*?Roll out.*?Live`) || !strings.Contains(words, "built in") {
		t.Errorf("the stages strip lacks the Build stage:\n%s", words)
	}
	if !strings.Contains(page, `href="/apps/shop/deployments/`+dep+`/build"`) {
		t.Error("the entry doesn't link its build log")
	}

	// The log page.
	resp, log := e.get(t, "/apps/shop/deployments/"+dep+"/build")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET build page = %d (Cache-Control %q), want 200 no-store:\n%s", resp.StatusCode, resp.Header.Get("Cache-Control"), log)
	}
	lw := text(log)
	for _, want := range []string{"Build of deployment", "built", uiShopRepo, "main", uiBuildCommit, "Ana Ñandú", "Retry supplier timeouts",
		uiShopImage + ":git-" + uiBuildCommit[:12], uiBuildDigest, "#1 [1/2] FROM busybox", "#2 pushing layers"} {
		if !strings.Contains(lw, want) {
			t.Errorf("the build page lacks %q:\n%s", want, lw)
		}
	}
	if strings.Contains(log, `hx-get="/parts/apps/shop/deployments/`) {
		t.Error("a build that is over reads itself again")
	}
	e.noSecretOnAnyPage(t)
}

func TestNewAppGitRefusals(t *testing.T) {
	e := newConsoleEnv(t)
	post := func(set url.Values) (int, string) {
		form := gitFormValues("shop", uiShopRepo, uiShopImage)
		for k, v := range set {
			form[k] = v
		}
		resp, body := e.postForm(t, "/apps/new/git", form)
		return resp.StatusCode, body
	}
	for _, c := range []struct {
		name   string
		set    url.Values
		status int
		at     string // the id of the input whose message says want
		want   string
	}{
		{"no repository", url.Values{"repository": {""}}, 422, "repository", "Enter the repository"},
		{"no image", url.Values{"image": {""}}, 422, "image", "Enter the image the build pushes to"},
		{"an image with a tag", url.Values{"image": {uiShopImage + ":1.2"}}, 422, "image", "Leave the tag out"},
		{"a key can't exist yet", url.Values{"access": {"deploy-key"}, "repository": {"git@github.com:acme/web.git"}}, 422, "access", "Choose Create, then add access"},
		{"a bad access", url.Values{"access": {"password"}}, 422, "access", "Choose Public, Deploy key or Token"},
		{"a bad repository", url.Values{"repository": {"ftp://example.com/app.git"}}, 422, "repository", "The scheme"},
		{"a bad branch", url.Values{"branch": {"-evil"}}, 422, "branch", "must not start with"},
		{"a bad folder", url.Values{"folder": {"../x"}}, 422, "folder", "must be a plain relative path"},
		{"a bad dockerfile", url.Values{"dockerfile": {"/etc/passwd"}}, 422, "dockerfile", "must be relative to the repository"},
		{"nowhere to push", url.Values{"image": {"ghcr.io/acme/web"}}, 422, "image", "No registry entry covers this image"},
		{"no name", url.Values{"name": {""}}, 422, "name", "Enter a name for the app."},
	} {
		status, body := post(c.set)
		if status != c.status {
			t.Errorf("%s: POST = %d, want %d:\n%s", c.name, status, c.status, body)
			continue
		}
		if !regexpHas(body, `(?s)id="`+c.at+`-error"[^>]*>.*?`+regexpQuote(c.want)) {
			t.Errorf("%s: no message %q at %s:\n%s", c.name, c.want, c.at, text(body))
		}
		// The form keeps what was typed.
		if !strings.Contains(body, `value="`+uiShopRepo+`"`) && c.set.Get("repository") == "" && c.at != "repository" {
			t.Errorf("%s: the repository was lost:\n%s", c.name, text(body))
		}
		if recs, _ := e.Store.ListApps(context.Background()); len(recs) != 0 {
			t.Errorf("%s: an app was stored", c.name)
		}
	}
}

func TestCreateThenNextStepsWithDeployKey(t *testing.T) {
	e := buildEnv(t, nil)
	form := gitFormValues("shop", "git@github.com:acme/web.git", uiShopImage)
	form["access"] = []string{"deploy-key"}
	form.Set("action", "create")
	resp, body := e.postForm(t, "/apps/new/git", form)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop?created=1#next-steps" {
		t.Fatalf("POST = %d to %q, want 303 to the app's Next steps:\n%s", resp.StatusCode, location(t, resp), body)
	}
	if n := len(e.deployments(t, "shop")); n != 0 || e.Nomad.Registrations() != 0 {
		t.Errorf("%d deployments, %d registrations; want none", n, e.Nomad.Registrations())
	}

	_, page := e.get(t, "/apps/shop?created=1")
	words := text(page)
	for _, want := range []string{"Next steps", "Builds the newest commit of main from git@github.com:acme/web.git", "reading it with a deploy key",
		"Git access", "The build needs a deploy key", "Make a deploy key", "Push webhook", "Create a webhook token", "application/json",
		"First build", "Build and deploy", "Waiting for its first build."} {
		if !strings.Contains(words, want) {
			t.Errorf("Next steps lack %q:\n%s", want, words)
		}
	}
	if strings.Contains(page, `class="cv-cmd" id="git-key-shop"`) {
		t.Error("a public key is drawn before one was made")
	}

	// Make the key: the public half is shown with its fingerprint, the private half never.
	f := formWith(t, page, "/apps/shop/services/shop/git-access/deploy-key")
	if f.Values.Get("from") != "next" {
		t.Errorf("the form's from = %q, want next", f.Values.Get("from"))
	}
	resp, body = e.submit(t, f, nil)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop#next-steps" {
		t.Fatalf("make key = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
	}
	_, page = e.get(t, "/apps/shop")
	words = text(page)
	for _, want := range []string{"A deploy key, made", "read-only access", "ssh-ed25519 AAAA", "caravana shop/shop", "Fingerprint SHA256:", "New key…"} {
		if !strings.Contains(words, want) {
			t.Errorf("after making a key the page lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "The build needs a deploy key") {
		t.Error("the page still says the key is missing")
	}
	if !strings.Contains(page, `data-copy="git-key-shop"`) {
		t.Error("the public key has no Copy button")
	}
	if len(e.gitSecrets(t)) == 0 {
		t.Fatal("the fake Nomad holds no private key")
	}
	e.noSecretOnAnyPage(t)

	// A new key replaces it; Remove access deletes it.
	before := e.Nomad.Variable(deploy.GitAccessPath("shop", "shop"))[deploy.GitAccessPublicKey]
	f = formWith(t, page, "/apps/shop/services/shop/git-access/deploy-key")
	if resp, _ := e.submit(t, f, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("new key = %d", resp.StatusCode)
	}
	if after := e.Nomad.Variable(deploy.GitAccessPath("shop", "shop"))[deploy.GitAccessPublicKey]; after == before || after == "" {
		t.Errorf("the key wasn't replaced: %q → %q", before, after)
	}
	_, page = e.get(t, "/apps/shop/configure")
	resp, _ = e.submit(t, formWith(t, page, "/apps/shop/services/shop/git-access/remove"), nil)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop/configure#git-access" {
		t.Errorf("remove = %d to %q, want 303 to Configure's Git access", resp.StatusCode, location(t, resp))
	}
	if e.Nomad.Variable(deploy.GitAccessPath("shop", "shop")) != nil {
		t.Error("the key is still stored")
	}
	// Removing again is the state asked for.
	if resp, _ := e.submit(t, formWith(t, page, "/apps/shop/services/shop/git-access/remove"), nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("removing nothing = %d, want 303", resp.StatusCode)
	}
}

func TestGitTokenIsNeverDrawn(t *testing.T) {
	e := buildEnv(t, nil)
	e.seedBuilt(t, "https://git.example.com/acme/web.git", app.BuildAccessToken)
	_, page := e.get(t, "/apps/shop")
	if !strings.Contains(text(page), "The build needs a token") {
		t.Errorf("Next steps don't say the token is missing:\n%s", text(page))
	}
	f := formWith(t, page, "/apps/shop/services/web/git-access/token")
	resp, body := e.submit(t, f, url.Values{"token": {uiTokenSent}, "username": {"ana"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop#next-steps" {
		t.Fatalf("set token = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
	}
	if strings.Contains(body, uiTokenSent) || strings.Contains(resp.Header.Get("Location"), uiTokenSent) {
		t.Error("the answer holds the token")
	}
	_, page = e.get(t, "/apps/shop")
	for _, want := range []string{"A token for the user ana", "It is never shown again.", "Replace token…"} {
		if !strings.Contains(text(page), want) {
			t.Errorf("after the token the page lacks %q:\n%s", want, text(page))
		}
	}
	e.noSecretOnAnyPage(t, uiTokenSent)

	// A token the service refuses draws the page again with the message at the token and no value.
	f = formWith(t, page, "/apps/shop/services/web/git-access/token")
	resp, body = e.submit(t, f, url.Values{"token": {"has a space " + uiTokenSent}, "username": {"bob"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a bad token = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, uiTokenSent) || !regexpHas(body, `(?s)id="git-token-web-error"`) || !strings.Contains(body, `value="bob"`) {
		t.Errorf("the refusal leaks the token, lacks its message, or lost the user name:\n%s", text(body))
	}
	if !regexpHas(body, `<input[^>]*name="token"[^>]*autocomplete="new-password"`) || regexpHas(body, `name="token"[^>]*value=`) {
		t.Error("the token input holds a value")
	}
	e.noSecretOnAnyPage(t, uiTokenSent)

	// An unknown service is a not-found page.
	resp, _ = e.postForm(t, "/apps/shop/services/nope/git-access/token", url.Values{"token": {"x"}})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a service that isn't there = %d, want 404", resp.StatusCode)
	}
}

func TestConfigureBuildPanel(t *testing.T) {
	e := buildEnv(t, nil)
	e.seedBuilt(t, uiShopRepo, "")
	_, page := e.get(t, "/apps/shop/configure")
	words := text(page)
	for _, want := range []string{"Build", "Builds the newest commit of main from " + uiShopRepo + ", reading it without credentials.",
		"Repository", "Branch", "Folder", "Dockerfile", "How it is read", "Public", "Deploy key", "Token", "SSH host key",
		"Save build source", "Saving builds the newest commit and deploys it.", "Git access", "No access is stored: the repository is read without credentials.",
		"Make a deploy key", "Add a token…"} {
		if !strings.Contains(words, want) {
			t.Errorf("Configure lacks %q:\n%s", want, words)
		}
	}
	f := formWith(t, page, "/apps/shop/services/web/build")
	f.Values.Set("svc.web.build-access", "public") // formsIn takes the last radio's value: a browser sends the checked one
	if f.Values.Get("revision") == "" || f.Values.Get("svc.web.build-repository") != uiShopRepo || f.Values.Get("svc.web.build-branch") != "main" ||
		f.Values.Get("svc.web.build-folder") != "." || f.Values.Get("svc.web.build-dockerfile") != "Dockerfile" {
		t.Fatalf("the Build form = %v, want the stored source and the revision", f.Values)
	}
	if !strings.Contains(page, `name="svc.web.build-access" value="public" checked`) {
		t.Error("the Public radio isn't checked")
	}

	t.Run("a refusal puts the message at its input and keeps what was typed", func(t *testing.T) {
		for _, c := range []struct {
			name, field, value, at, want string
		}{
			{"branch", "svc.web.build-branch", "-evil", "svc-web-build-branch", "must not start with"},
			{"folder", "svc.web.build-folder", "/abs", "svc-web-build-folder", "must be relative to the repository"},
			{"repository", "svc.web.build-repository", "gopher://x/y", "svc-web-build-repository", "The scheme"},
			{"a key with an https address", "svc.web.build-access", "deploy-key", "svc-web-build-access", "needs an SSH repository"},
		} {
			resp, body := e.submit(t, f, url.Values{c.field: {c.value}})
			if resp.StatusCode != http.StatusUnprocessableEntity || !regexpHas(body, `(?s)id="`+c.at+`-error"[^>]*>.*?`+regexpQuote(c.want)) {
				t.Errorf("%s: POST = %d, no %q at %s:\n%s", c.name, resp.StatusCode, c.want, c.at, text(body))
			}
			if c.field != "svc.web.build-access" && !strings.Contains(body, `value="`+c.value+`"`) {
				t.Errorf("%s: the typed value was lost", c.name)
			}
		}
		if n := len(e.deployments(t, "shop")); n != 0 {
			t.Errorf("%d deployments after refusals, want none", n)
		}
	})

	t.Run("a token the build needs and doesn't have is refused at How it is read", func(t *testing.T) {
		e2 := buildEnv(t, nil)
		e2.seedBuilt(t, "https://git.example.com/acme/web.git", "")
		_, p := e2.get(t, "/apps/shop/configure")
		resp, body := e2.submit(t, formWith(t, p, "/apps/shop/services/web/build"), url.Values{"svc.web.build-access": {"token"}})
		if resp.StatusCode != http.StatusConflict || !regexpHas(body, `(?s)id="svc-web-build-access-error"[^>]*>.*?make the deploy key or add the token it needs`) {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("an SSH host nobody knows needs its key", func(t *testing.T) {
		e2 := buildEnv(t, nil)
		e2.seedBuilt(t, "git@git.example.com:acme/web.git", app.BuildAccessDeployKey)
		_, p := e2.get(t, "/apps/shop/configure")
		resp, _ := e2.submit(t, formWith(t, p, "/apps/shop/services/web/git-access/deploy-key"), nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("make key = %d", resp.StatusCode)
		}
		_, p = e2.get(t, "/apps/shop/configure")
		resp, body := e2.submit(t, formWith(t, p, "/apps/shop/services/web/build"), url.Values{"svc.web.build-access": {"deploy-key"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !regexpHas(body, `(?s)caravana doesn&#39;t know this SSH host&#39;s key`) {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("a stale page is refused", func(t *testing.T) {
		g := f
		g.Values = url.Values{}
		for k, v := range f.Values {
			g.Values[k] = v
		}
		g.Values.Set("revision", "0000000000000000")
		resp, body := e.submit(t, g, nil)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app changed since you opened this page") {
			t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("saving the service's own form keeps the build source and builds", func(t *testing.T) {
		e2 := buildEnv(t, nil)
		e2.seedBuilt(t, uiShopRepo, "")
		resp, body := e2.submit(t, e2.configureForm(t, "shop"), url.Values{"svc.web.memory": {"192"}})
		recs := e2.deployments(t, "shop")
		if resp.StatusCode != http.StatusSeeOther || len(recs) != 1 || recs[0].Build == nil {
			t.Fatalf("POST = %d, deployments %+v:\n%s", resp.StatusCode, recs, text(body))
		}
		if b := e2.storedApp(t, "shop").Services[0].Build; b == nil || b.Repository != uiShopRepo {
			t.Errorf("the build source was lost: %+v", b)
		}
		e2.waitDeploy(t, recs[0].Deployment.ID)
	})

	t.Run("a good save is a deploy that builds", func(t *testing.T) {
		resp, body := e.submit(t, f, url.Values{"svc.web.build-branch": {"release"}, "svc.web.build-folder": {"web"}, "svc.web.build-dockerfile": {"deploy/Dockerfile"}})
		recs := e.deployments(t, "shop")
		if resp.StatusCode != http.StatusSeeOther || len(recs) != 1 || location(t, resp) != "/apps/shop/deployments#deployment-"+recs[0].Deployment.ID {
			t.Fatalf("POST = %d to %q, %d deployments:\n%s", resp.StatusCode, location(t, resp), len(recs), body)
		}
		if recs[0].Build == nil || recs[0].Build.Branch != "release" {
			t.Errorf("deployment's build = %+v, want one of branch release", recs[0].Build)
		}
		b := e.storedApp(t, "shop").Services[0].Build
		if b.Branch != "release" || b.Path != "web" || b.Dockerfile != "deploy/Dockerfile" || b.Repository != uiShopRepo {
			t.Errorf("stored build = %+v", b)
		}
		e.waitDeploy(t, recs[0].Deployment.ID)
	})
}

func TestBuildStageWords(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return now.Add(time.Duration(sec) * time.Second) }
	three := stagesOf(store.DeploymentRecord{Status: store.StatusPending}, nil)
	for _, c := range []struct {
		name       string
		build      store.BuildRecord
		status     store.Status
		registered *time.Time
		want       []string // name:mark:value, Build first
		active     string
	}{
		{"queued", store.BuildRecord{Status: store.BuildQueued, QueuedAt: at(-5)}, store.StatusPending, nil,
			[]string{"Build:rolling:queued", "Queued:idle:—", "Roll out:idle:—", "Live:idle:—"}, "Build"},
		{"running shows the time it has taken", store.BuildRecord{Status: store.BuildRunning, StartedAt: at(-12)}, store.StatusPending, nil,
			[]string{"Build:rolling:building 12 s", "Queued:idle:—", "Roll out:idle:—", "Live:idle:—"}, "Build"},
		{"built in", store.BuildRecord{Status: store.BuildSucceeded, StartedAt: at(-60), EndedAt: at(-19)}, store.StatusPending, nil,
			[]string{"Build:healthy:built in 41 s", "Queued:rolling:waiting", "Roll out:idle:—", "Live:idle:—"}, "Queued"},
		{"failed", store.BuildRecord{Status: store.BuildFailed, StartedAt: at(-60), EndedAt: at(-30)}, store.StatusFailed, nil,
			[]string{"Build:failed:failed", "Queued:idle:—", "Roll out:idle:—", "Live:idle:—"}, ""},
		{"cancelled", store.BuildRecord{Status: store.BuildCancelled}, store.StatusSuperseded, nil,
			[]string{"Build:idle:cancelled", "Queued:idle:—", "Roll out:idle:—", "Live:idle:—"}, ""},
	} {
		rec := store.DeploymentRecord{Status: c.status, Build: &c.build, CreatedAt: at(-100), RegisteredAt: c.registered}
		got := withBuildStage(stagesOf(rec, nil), rec, now)
		var words []string
		active := ""
		for _, st := range got {
			words = append(words, st.Name+":"+st.Mark+":"+st.Value)
			if st.Active {
				active = st.Name
			}
		}
		if strings.Join(words, " | ") != strings.Join(c.want, " | ") || active != c.active {
			t.Errorf("%s: stages = %v (active %q), want %v (active %q)", c.name, words, active, c.want, c.active)
		}
	}
	if got := withBuildStage(three, store.DeploymentRecord{}, now); len(got) != 3 {
		t.Errorf("a deployment that didn't build has %d stages, want 3", len(got))
	}
}

func TestBuildLogPage(t *testing.T) {
	e := buildEnv(t, nil)
	e.seedBuilt(t, uiShopRepo, "")
	end := func(s store.BuildEnd) *store.BuildEnd { s.At = time.Now(); return &s }
	e.seedBuild(t, "queued1", nil, nil)
	e.seedBuild(t, "failed1", end(store.BuildEnd{Status: store.BuildFailed, Commit: uiBuildCommit, Author: "Ana", Subject: "Break it",
		Error: "build failed: the build failed\nstep 3 exploded", Log: "step 1\nstep 3 exploded\n", LogCut: true}),
		&store.DeploymentUpdate{Status: store.StatusFailed, Error: "build failed: the build failed\nstep 3 exploded"})
	e.seedBuild(t, "pruned1", end(store.BuildEnd{Status: store.BuildSucceeded, Commit: uiBuildCommit, Image: uiShopImage + ":git-4e0a77c1b2d3", Digest: uiBuildDigest}),
		&store.DeploymentUpdate{Status: store.StatusHealthy})
	if err := e.Store.StartBuild(context.Background(), "b-queued1", time.Now()); err != nil {
		t.Fatal(err)
	}

	t.Run("a build that is going reads itself again and reloads without script", func(t *testing.T) {
		resp, body := e.get(t, "/apps/shop/deployments/queued1/build")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d", resp.StatusCode)
		}
		for _, want := range []string{`hx-get="/parts/apps/shop/deployments/queued1/build"`, `hx-trigger="sse:deployments, sse:sync, every 5s"`,
			`<noscript><meta http-equiv="refresh" content="10">`, `hx-ext="sse" sse-connect="/live/apps/shop"`, "building", "The build has started and has printed nothing yet."} {
			if !strings.Contains(body, want) {
				t.Errorf("the page of a running build lacks %q:\n%s", want, body)
			}
		}
		resp, part := e.get(t, "/parts/apps/shop/deployments/queued1/build")
		if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") || !strings.Contains(text(part), "building") {
			t.Errorf("part = %d: %s", resp.StatusCode, text(part))
		}
	})

	t.Run("a failed build shows why, its log and that the log was cut", func(t *testing.T) {
		_, body := e.get(t, "/apps/shop/deployments/failed1/build")
		words := text(body)
		for _, want := range []string{"build failed", "Why it failed", "build failed: the build failed step 3 exploded", "step 1 step 3 exploded",
			"only the last 64 KiB were kept", "Ana", "Break it"} {
			if !strings.Contains(words, want) {
				t.Errorf("the page of a failed build lacks %q:\n%s", want, words)
			}
		}
		if strings.Contains(body, `hx-get="/parts/apps/shop/deployments/`) {
			t.Error("a failed build reads itself again")
		}
		resp, part := e.get(t, "/parts/apps/shop/deployments/failed1/build")
		if resp.StatusCode != 286 || !strings.Contains(text(part), "Why it failed") {
			t.Errorf("part of a final build = %d (%s), want 286 with the region", resp.StatusCode, text(part))
		}
	})

	t.Run("a build whose log was pruned says so", func(t *testing.T) {
		_, body := e.get(t, "/apps/shop/deployments/pruned1/build")
		if !strings.Contains(text(body), "There is no log: it was kept for the newest 50 builds of the app") {
			t.Errorf("no word about the missing log:\n%s", text(body))
		}
	})

	t.Run("the history entry of a failed build says it and links the log", func(t *testing.T) {
		_, body := e.get(t, "/apps/shop/deployments")
		words := text(body)
		if !strings.Contains(words, "Build · 4e0a77c Break it · main · Build log") || !strings.Contains(body, `href="/apps/shop/deployments/failed1/build"`) {
			t.Errorf("the entry lacks its build line:\n%s", words)
		}
		if !strings.Contains(words, "build failed: the build failed") || strings.Contains(words, "step 3 exploded Build") {
			t.Errorf("the entry's error isn't the build's reason alone:\n%s", words)
		}
	})

	t.Run("a deployment that didn't build has no build page", func(t *testing.T) {
		e2 := newConsoleEnv(t)
		e2.deploy(t, whoamiApp())
		dep := e2.deployments(t, "whoami")[0].Deployment.ID
		if resp, _ := e2.get(t, "/apps/whoami/deployments/"+dep+"/build"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET = %d, want 404", resp.StatusCode)
		}
		if resp, part := e2.get(t, "/parts/apps/whoami/deployments/"+dep+"/build"); resp.StatusCode != 286 || !strings.Contains(text(part), "This build is gone") {
			t.Errorf("part = %d: %s", resp.StatusCode, text(part))
		}
		if resp, _ := e.get(t, "/apps/nope/deployments/x/build"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("an app that isn't there = %d, want 404", resp.StatusCode)
		}
	})
}

func TestBuildFailureReachesTheHistory(t *testing.T) {
	e := buildEnv(t, func(deploytest.PlayedBuild) deploytest.BuildPlay {
		p := succeedingUIBuild()
		p.Fail, p.BuildErr = "the build failed", "error: step 4 exploded\n"
		return p
	})
	resp, _ := e.postForm(t, "/apps/new/git", gitFormValues("shop", uiShopRepo, uiShopImage))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST = %d", resp.StatusCode)
	}
	dep := e.deployments(t, "shop")[0].Deployment.ID
	e.waitDeploy(t, dep)
	_, body := e.get(t, "/apps/shop/deployments")
	words := text(body)
	for _, want := range []string{"The build failed, so nothing was deployed.", "Build failed", "build failed: the build failed"} {
		if !strings.Contains(words, want) && !strings.Contains(strings.ToLower(words), strings.ToLower(want)) {
			t.Errorf("the history lacks %q:\n%s", want, words)
		}
	}
	_, log := e.get(t, "/apps/shop/deployments/"+dep+"/build")
	if !strings.Contains(text(log), "error: step 4 exploded") {
		t.Errorf("the log page lacks the build's output:\n%s", text(log))
	}
}

func TestDeployNowOfABuiltAppBuilds(t *testing.T) {
	e := buildEnv(t, nil)
	e.seedBuilt(t, uiShopRepo, "")
	_, page := e.get(t, "/apps/shop")
	f := formWith(t, page, "/apps/shop/deploy")
	resp, _ := e.submit(t, f, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Build and deploy = %d", resp.StatusCode)
	}
	recs := e.deployments(t, "shop")
	if len(recs) != 1 || recs[0].Build == nil {
		t.Fatalf("deployments = %+v, want one that builds", recs)
	}
	e.waitDeploy(t, recs[0].Deployment.ID)

	// The Next steps are gone once something is deployed.
	_, page = e.get(t, "/apps/shop")
	if strings.Contains(page, `id="next-steps"`) {
		t.Error("Next steps are still drawn after the first deploy")
	}

	// A built service that needs a key it doesn't have is refused with a sentence, and the access is theirs to make.
	e2 := buildEnv(t, nil)
	e2.seedBuilt(t, "https://git.example.com/acme/web.git", app.BuildAccessToken)
	_, p := e2.get(t, "/apps/shop")
	resp, body := e2.submit(t, formWith(t, p, "/apps/shop/deploy"), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The build can't read the repository yet") {
		t.Errorf("deploy without access = %d:\n%s", resp.StatusCode, text(body))
	}
	if n := len(e2.deployments(t, "shop")); n != 0 {
		t.Errorf("%d deployments recorded for a refused build, want none", n)
	}
}

func TestSettingsBuilds(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/settings/builds")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{`href="/settings/builds" class="cv-snav-item" aria-current="page"`, "Builds",
		`name="memoryMB" value="2048"`, `name="cpuMHz" value="2000"`, `name="timeoutMinutes" value="30"`, `name="concurrency" value="1"`, `name="cacheMB" value="5120"`,
		"512 to 16384", "500 to 32000", "5 to 180", "1 to 4", "0 to 102400", "0 means no cache", "Save limits"} {
		if !strings.Contains(body, want) {
			t.Errorf("Settings › Builds lacks %q:\n%s", want, text(body))
		}
	}
	// It is in the Connections group, after Registries.
	if !regexpHas(body, `(?s)Connections.*?>Registries<.*?>Builds<`) {
		t.Error("Builds isn't in the Connections group after Registries")
	}

	f := formWith(t, body, "/settings/builds")
	resp, _ = e.submit(t, f, url.Values{"memoryMB": {"4096"}, "concurrency": {"2"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/builds?saved=1" {
		t.Fatalf("save = %d to %q", resp.StatusCode, location(t, resp))
	}
	set, _ := e.Svc.BuildSettings(context.Background())
	if set.MemoryMB != 4096 || set.Concurrency != 2 || set.CPUMHz != 2000 {
		t.Errorf("settings = %+v", set)
	}
	_, body = e.get(t, "/settings/builds?saved=1")
	if !strings.Contains(text(body), "Saved. The next build uses these limits") || !strings.Contains(body, `name="memoryMB" value="4096"`) {
		t.Errorf("the saved page lacks its notice or the new value:\n%s", text(body))
	}

	t.Run("refusals sit at their inputs and keep what was typed", func(t *testing.T) {
		resp, body := e.submit(t, f, url.Values{"memoryMB": {"100"}, "cpuMHz": {"lots"}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422", resp.StatusCode)
		}
		if !regexpHas(body, `(?s)id="builds-cpuMHz-error"[^>]*>.*?Enter a whole number`) || !strings.Contains(body, `name="cpuMHz" value="lots"`) {
			t.Errorf("a non-number isn't refused at its input:\n%s", text(body))
		}
		// With every number a number, the service's range is the judge.
		resp, body = e.submit(t, f, url.Values{"memoryMB": {"100"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !regexpHas(body, `(?s)id="builds-memoryMB-error"[^>]*>.*?Must be from 512 to 16384 MB`) || !strings.Contains(body, `name="memoryMB" value="100"`) {
			t.Errorf("an out-of-range value isn't refused at its input (%d):\n%s", resp.StatusCode, text(body))
		}
		if got, _ := e.Svc.BuildSettings(context.Background()); got.MemoryMB != 4096 {
			t.Errorf("a refused save changed the settings: %+v", got)
		}
	})
}

// regexpHas reports whether the pattern matches somewhere in body.
func regexpHas(body, pattern string) bool { return regexp.MustCompile(pattern).MatchString(body) }

// regexpQuote is text as a pattern that matches it literally.
func regexpQuote(text string) string { return regexp.QuoteMeta(text) }
