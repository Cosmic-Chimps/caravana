package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// Building from a Git repository (feature 050, specs/050-build-from-git/contracts/api.md): the routes
// run the real flow against the fake Nomad, with a deploytest.BuildPlayer standing in for Git and BuildKit.

const (
	apiBuildCommit = "4e0a77c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7"
	apiBuildDigest = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
	apiBuildImage  = "127.0.0.1:5001/acme/web" // a loopback registry: no registry entry is needed
	apiTokenS      = "s3cret-sentinel-050-api-token"
)

// builtBody is the app of a built service as a client sends it. build is the object of "build".
func builtBody(image, build string) string {
	return `{
  "source": {"kind": "image"},
  "services": [{
    "name": "web",
    "image": "` + image + `",
    "build": ` + build + `,
    "ports": [{"name": "http", "containerPort": 80}],
    "domains": [{"host": "shop.localhost", "port": "http"}],
    "health": {"path": "/", "interval": "10s", "timeout": "2s"}
  }]
}`
}

const publicBuild = `{"repository": "http://127.0.0.1:8098/app.git"}`

// buildEnv is a test environment whose builds succeed with a fixed commit.
func buildEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, "")
	e.Nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	deploytest.PlayBuilds(t, e.Nomad, func(deploytest.PlayedBuild) deploytest.BuildPlay {
		return deploytest.BuildPlay{Commit: apiBuildCommit, Author: "Ana", Subject: "Retry supplier timeouts", Digest: apiBuildDigest,
			SourceOut: "caravana-build: cloned commit " + apiBuildCommit + "\n", BuildOut: "#1 [1/2] FROM busybox\n",
			BuildErr: "caravana-build: pushed " + apiBuildImage + "\n"}
	})
	return e
}

// createBuilt stores a built app without deploying it.
func createBuilt(t *testing.T, e *testEnv, id, build string) {
	t.Helper()
	body := strings.Replace(builtBody(apiBuildImage, build), "{\n", `{"id": "`+id+`",`+"\n", 1)
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps?deploy=false", "application/json", body); status != http.StatusCreated {
		t.Fatalf("create %s: %d %s", id, status, raw)
	}
}

type buildSummary struct {
	ID         string `json:"id"`
	Service    string `json:"service"`
	Status     string `json:"status"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Author     string `json:"author"`
	Subject    string `json:"subject"`
	Image      string `json:"image"`
	Digest     string `json:"digest"`
	Error      string `json:"error"`
	Log        string `json:"log"`
	LogCut     bool   `json:"logCut"`
}

type builtDeployment struct {
	ID     string        `json:"id"`
	Status string        `json:"status"`
	Build  *buildSummary `json:"build"`
}

func TestBuiltDeployAnswersWithItsBuild(t *testing.T) {
	e := buildEnv(t)
	doc := loadContract(t)

	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/deployments", "application/json", builtBody(apiBuildImage, publicBuild))
	if status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Deployment")
	d := decodeAs[builtDeployment](t, raw)
	if d.Status != "pending" || d.Build == nil || d.Build.Status != "queued" || d.Build.Service != "web" || d.Build.Branch != "main" {
		t.Fatalf("deployment = %s, want pending with a queued build of web on main", raw)
	}
	if header.Get("Location") != "/v1/apps/shop/deployments/"+d.ID {
		t.Errorf("Location = %q", header.Get("Location"))
	}

	waitUntil(t, func() bool {
		rec, err := e.Store.GetDeployment(context.Background(), d.ID)
		return err == nil && rec.Status == "healthy"
	})
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop/deployments/"+d.ID, "", "")
	checkSchema(t, doc, raw, "Deployment")
	got := decodeAs[builtDeployment](t, raw)
	if got.Build == nil || got.Build.Status != "succeeded" || got.Build.Commit != apiBuildCommit || got.Build.Digest != apiBuildDigest ||
		got.Build.Image != apiBuildImage+":git-"+apiBuildCommit[:12] || got.Build.Author != "Ana" {
		t.Errorf("build = %+v, want a succeeded build of the commit with its image and digest", got.Build)
	}
	if got.Build != nil && got.Build.Log != "" {
		t.Errorf("the deployment's summary holds the build's log")
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop/deployments", "", "")
	checkSchema(t, doc, raw, "DeploymentList")

	// The app reads back with its build, defaults written in.
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop", "", "")
	checkSchema(t, doc, raw, "AppRecord")
	for _, want := range []string{`"branch":"main"`, `"path":"."`, `"dockerfile":"Dockerfile"`, `"access":"public"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("getApp = %s, want the default %s written in", raw, want)
		}
	}

	t.Run("getDeploymentBuild", func(t *testing.T) {
		status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop/deployments/"+d.ID+"/build", "", "")
		if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
			t.Fatalf("status %d, Cache-Control %q; want 200 no-store: %s", status, header.Get("Cache-Control"), raw)
		}
		checkSchema(t, doc, raw, "Build")
		b := decodeAs[buildSummary](t, raw)
		if b.Status != "succeeded" || b.Commit != apiBuildCommit || !strings.Contains(b.Log, "FROM busybox") || b.LogCut {
			t.Errorf("build = %+v, want the succeeded build with its output", b)
		}
	})
	t.Run("a deployment that didn't build", func(t *testing.T) {
		plain := deployOK(t, e, "whoami", whoamiBody)
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+plain.ID+"/build", "", "")
		if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeBuildNotFound {
			t.Errorf("status %d %s, want 404 build_not_found", status, raw)
		}
		checkSchema(t, doc, raw, "Error")
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+plain.ID, "", "")
		if strings.Contains(string(raw), `"build"`) {
			t.Errorf("a deployment that didn't build has a build: %s", raw)
		}
	})
	t.Run("unknown app or deployment", func(t *testing.T) {
		for _, path := range []string{"/v1/apps/nope/deployments/x/build", "/v1/apps/shop/deployments/nope/build"} {
			status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
			if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeNotFound {
				t.Errorf("GET %s = %d %s, want 404 not_found", path, status, raw)
			}
		}
	})
}

func TestBuiltDeployRefusals(t *testing.T) {
	e := buildEnv(t)
	doc := loadContract(t)
	for name, c := range map[string]struct {
		image, build string
		status       int
		code, field  string
	}{
		"deploy key not made": {apiBuildImage, `{"repository": "git@github.com:acme/web.git", "access": "deploy-key"}`,
			http.StatusConflict, codeGitAccessMissing, "services[0].build.access"},
		"token not stored": {apiBuildImage, `{"repository": "https://github.com/acme/web.git", "access": "token"}`,
			http.StatusConflict, codeGitAccessMissing, "services[0].build.access"},
		"no registry entry for the image": {"registry.example.com/acme/web", publicBuild,
			http.StatusUnprocessableEntity, codeBuildRegistryMissing, "services[0].image"},
		"a bad repository": {apiBuildImage, `{"repository": "https://user:pw@github.com/acme/web.git"}`,
			http.StatusBadRequest, codeInvalidField, "services[0].build.repository"},
	} {
		t.Run(name, func(t *testing.T) {
			before := e.Nomad.Registrations()
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/refused/deployments", "application/json", builtBody(c.image, c.build))
			got := decodeAs[errorBody](t, raw)
			if status != c.status || got.Error.Code != c.code || got.Error.Field != c.field {
				t.Fatalf("%d %s, want %d %s at %s", status, raw, c.status, c.code, c.field)
			}
			checkSchema(t, doc, raw, "Error")
			if e.Nomad.Registrations() != before {
				t.Error("a refused deploy registered a job")
			}
			if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/refused", "", ""); status != http.StatusNotFound {
				t.Errorf("a refused deploy saved the app: %d %s", status, raw)
			}
		})
	}
}

func TestBuiltDeployWithAnUnknownSSHHost(t *testing.T) {
	e := buildEnv(t)
	createBuilt(t, e, "sshapp", `{"repository": "git@git.example.com:acme/web.git", "access": "deploy-key"}`)
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/sshapp/services/web/git-access/deploy-key", "", ""); status != http.StatusCreated {
		t.Fatalf("deploy key: %d %s", status, raw)
	}
	body := builtBody(apiBuildImage, `{"repository": "git@git.example.com:acme/web.git", "access": "deploy-key"}`)
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/sshapp/deployments", "application/json", body)
	got := decodeAs[errorBody](t, raw)
	if status != http.StatusUnprocessableEntity || got.Error.Code != codeGitHostKeyUnknown || got.Error.Field != "services[0].build.hostKey" {
		t.Fatalf("%d %s, want 422 git_host_key_unknown at services[0].build.hostKey", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Error")
	// Given its key, the same deploy is accepted.
	body = builtBody(apiBuildImage, `{"repository": "git@git.example.com:acme/web.git", "access": "deploy-key", "hostKey": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}`)
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/sshapp/deployments", "application/json", body); status != http.StatusAccepted {
		t.Errorf("with its host key: %d %s, want 202", status, raw)
	}
}

func TestGitAccessRoutes(t *testing.T) {
	e := buildEnv(t)
	doc := loadContract(t)
	createBuilt(t, e, "shop", `{"repository": "https://github.com/acme/web.git", "access": "token"}`)
	createBuilt(t, e, "plain", publicBuild)
	base := e.URL + "/v1/apps/shop/services/web/git-access"

	read := func() (int, http.Header, []byte) { return call(t, http.MethodGet, base, "", "") }

	t.Run("none at first", func(t *testing.T) {
		status, header, raw := read()
		if status != http.StatusOK || decodeAs[struct{ Kind string }](t, raw).Kind != "none" || header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %s, want 200 none", status, raw)
		}
		checkSchema(t, doc, raw, "GitAccess")
	})
	t.Run("a deploy key", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, base+"/deploy-key", "", "")
		if status != http.StatusCreated {
			t.Fatalf("%d %s, want 201", status, raw)
		}
		checkSchema(t, doc, raw, "GitAccess")
		k := decodeAs[struct{ Kind, PublicKey, Fingerprint string }](t, raw)
		if k.Kind != "deploy-key" || !strings.HasPrefix(k.PublicKey, "ssh-ed25519 ") || !strings.HasSuffix(k.PublicKey, " caravana shop/web") ||
			!strings.HasPrefix(k.Fingerprint, "SHA256:") {
			t.Errorf("deploy key = %s", raw)
		}
		for _, leak := range []string{"PRIVATE KEY", "OPENSSH"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("the answer holds %q", leak)
			}
		}
		_, _, again := read()
		if strings.Contains(string(again), "PRIVATE KEY") || decodeAs[struct{ Kind string }](t, again).Kind != "deploy-key" {
			t.Errorf("getGitAccess after = %s", again)
		}
	})
	t.Run("a token replaces it and is never answered", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPut, base+"/token", "application/json; charset=UTF-8", `{"token": "`+apiTokenS+`"}`)
		if status != http.StatusOK {
			t.Fatalf("%d %s, want 200", status, raw)
		}
		checkSchema(t, doc, raw, "GitAccess")
		g := decodeAs[struct{ Kind, Username, PublicKey string }](t, raw)
		if g.Kind != "token" || g.Username != "x-access-token" || g.PublicKey != "" {
			t.Errorf("token access = %s", raw)
		}
		_, _, again := read()
		for _, answer := range [][]byte{raw, again} {
			if strings.Contains(string(answer), apiTokenS) {
				t.Errorf("an answer holds the token: %s", answer)
			}
		}
		if strings.Contains(e.Logs.String(), apiTokenS) {
			t.Error("the log holds the token")
		}
		// The deploy that needed it now goes through.
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/deployments", "", "")
		if status != http.StatusAccepted {
			t.Errorf("deploy with the token stored: %d %s, want 202", status, raw)
		}
	})
	t.Run("a bad token or user name", func(t *testing.T) {
		for body, field := range map[string]string{
			`{"token": ""}`:                                  "token",
			`{"token": "has a space"}`:                       "token",
			`{"token": "x", "username": "a b"}`:              "username",
			`{"token": "` + strings.Repeat("a", 4097) + `"}`: "token",
		} {
			status, _, raw := call(t, http.MethodPut, base+"/token", "application/json", body)
			if status != http.StatusBadRequest || decodeAs[errorBody](t, raw).Error.Field != field {
				t.Errorf("PUT %.30s… = %d %s, want 400 at %s", body, status, raw, field)
			}
			if strings.Contains(string(raw), "has a space") {
				t.Errorf("the refusal echoes the token: %s", raw)
			}
		}
	})
	t.Run("a service that is not built", func(t *testing.T) {
		deployOK(t, e, "whoami", whoamiBody)
		for _, c := range []struct{ method, path string }{{http.MethodPost, "/deploy-key"}, {http.MethodPut, "/token"}} {
			status, _, raw := call(t, c.method, e.URL+"/v1/apps/whoami/services/web/git-access"+c.path, "application/json", `{"token": "x"}`)
			if status != http.StatusBadRequest || decodeAs[errorBody](t, raw).Error.Field != "service" {
				t.Errorf("%s %s = %d %s, want 400 at service", c.method, c.path, status, raw)
			}
		}
		if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/services/web/git-access", "", ""); status != http.StatusOK ||
			decodeAs[struct{ Kind string }](t, raw).Kind != "none" {
			t.Errorf("getGitAccess of an unbuilt service = %d %s, want none", status, raw)
		}
	})
	t.Run("unknown app or service", func(t *testing.T) {
		for _, c := range []struct{ method, path string }{
			{http.MethodGet, "/v1/apps/nope/services/web/git-access"},
			{http.MethodGet, "/v1/apps/shop/services/nope/git-access"},
			{http.MethodPost, "/v1/apps/shop/services/nope/git-access/deploy-key"},
			{http.MethodDelete, "/v1/apps/shop/services/nope/git-access"},
		} {
			status, _, raw := call(t, c.method, e.URL+c.path, "", "")
			if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeNotFound {
				t.Errorf("%s %s = %d %s, want 404 not_found", c.method, c.path, status, raw)
			}
		}
	})
	t.Run("delete", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodDelete, base, "", ""); status != http.StatusNoContent {
			t.Fatalf("DELETE = %d %s, want 204", status, raw)
		}
		status, _, raw := call(t, http.MethodDelete, base, "", "")
		if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeGitAccessNotFound {
			t.Errorf("second DELETE = %d %s, want 404 git_access_not_found", status, raw)
		}
		checkSchema(t, doc, raw, "Error")
		if _, _, raw := read(); decodeAs[struct{ Kind string }](t, raw).Kind != "none" {
			t.Errorf("after DELETE: %s", raw)
		}
	})
}

func TestBuildSettingsRoutes(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	url := e.URL + "/v1/builds/settings"

	status, _, raw := call(t, http.MethodGet, url, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "BuildSettings")
	if got := string(raw); !strings.Contains(got, `"memoryMB":2048`) || !strings.Contains(got, `"cacheMB":5120`) || !strings.Contains(got, `"concurrency":1`) {
		t.Errorf("defaults = %s", raw)
	}

	status, _, raw = call(t, http.MethodPut, url, "application/json; charset=UTF-8",
		`{"memoryMB": 4096, "cpuMHz": 3000, "timeoutMinutes": 45, "concurrency": 2, "cacheMB": 0}`)
	if status != http.StatusOK {
		t.Fatalf("PUT = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "BuildSettings")
	_, _, raw = call(t, http.MethodGet, url, "", "")
	if got := string(raw); !strings.Contains(got, `"memoryMB":4096`) || !strings.Contains(got, `"cacheMB":0`) || !strings.Contains(got, `"concurrency":2`) {
		t.Errorf("saved = %s", raw)
	}

	const ok = `"memoryMB": 2048, "cpuMHz": 2000, "timeoutMinutes": 30, "concurrency": 1, "cacheMB": 5120`
	for name, c := range map[string]struct{ body, field string }{
		"memory low":      {strings.Replace(`{`+ok+`}`, `"memoryMB": 2048`, `"memoryMB": 100`, 1), "memoryMB"},
		"cpu high":        {strings.Replace(`{`+ok+`}`, `"cpuMHz": 2000`, `"cpuMHz": 99999`, 1), "cpuMHz"},
		"timeout low":     {strings.Replace(`{`+ok+`}`, `"timeoutMinutes": 30`, `"timeoutMinutes": 1`, 1), "timeoutMinutes"},
		"concurrency":     {strings.Replace(`{`+ok+`}`, `"concurrency": 1`, `"concurrency": 9`, 1), "concurrency"},
		"cache negative":  {strings.Replace(`{`+ok+`}`, `"cacheMB": 5120`, `"cacheMB": -1`, 1), "cacheMB"},
		"a missing field": {`{"memoryMB": 2048, "cpuMHz": 2000, "timeoutMinutes": 30, "concurrency": 1}`, "cacheMB"},
	} {
		t.Run(name, func(t *testing.T) {
			status, _, raw := call(t, http.MethodPut, url, "application/json", c.body)
			if status != http.StatusBadRequest || decodeAs[errorBody](t, raw).Error.Field != c.field {
				t.Errorf("%d %s, want 400 at %s", status, raw, c.field)
			}
		})
	}
}

// push posts a push event to an app's webhook as the provider named by header would.
func push(t *testing.T, e *testEnv, tok string, headers map[string]string, body string) (int, http.Header, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.URL+"/v1/webhooks/shop", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

func TestPushWebhookOfABuiltApp(t *testing.T) {
	e := buildEnv(t)
	doc := loadContract(t)
	createBuilt(t, e, "shop", publicBuild)
	_, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/webhook", "", "")
	tok := decodeAs[struct{ Token string }](t, raw).Token

	body := func(ref, after string) string {
		return `{"ref": "` + ref + `", "after": "` + after + `", "deleted": false}`
	}
	gh := map[string]string{"X-GitHub-Event": "push", "Authorization": "Bearer " + tok}

	t.Run("a push to another branch, before any deployment, is 204", func(t *testing.T) {
		status, _, raw := push(t, e, tok, gh, body("refs/heads/dev", apiBuildCommit))
		if status != http.StatusNoContent || len(raw) != 0 {
			t.Errorf("%d %s, want 204 with no body", status, raw)
		}
	})
	t.Run("a push to the branch builds", func(t *testing.T) {
		status, header, raw := push(t, e, tok, gh, body("refs/heads/main", apiBuildCommit))
		if status != http.StatusAccepted {
			t.Fatalf("%d %s, want 202", status, raw)
		}
		checkSchema(t, doc, raw, "WebhookResult")
		r := decodeAs[struct {
			Changed    bool
			Skipped    string
			Deployment builtDeployment
		}](t, raw)
		if !r.Changed || r.Skipped != "" || r.Deployment.Build == nil || r.Deployment.Build.Status != "queued" ||
			header.Get("Location") != "/v1/apps/shop/deployments/"+r.Deployment.ID {
			t.Errorf("result = %s, Location %q; want a queued build and its Location", raw, header.Get("Location"))
		}
		waitUntil(t, func() bool {
			rec, err := e.Store.GetDeployment(context.Background(), r.Deployment.ID)
			return err == nil && rec.Status == "healthy"
		})
	})
	t.Run("another branch, a ping and a deleted branch build nothing and say why", func(t *testing.T) {
		before := e.Nomad.Registrations()
		for name, c := range map[string]struct {
			headers map[string]string
			body    string
			want    string
		}{
			"other branch": {gh, body("refs/heads/dev", apiBuildCommit), "refs/heads/dev"},
			"ping":         {map[string]string{"X-GitHub-Event": "ping", "Authorization": "Bearer " + tok}, `{"zen": "x"}`, "ping"},
			"deleted":      {gh, `{"ref": "refs/heads/main", "after": "0000000000000000000000000000000000000000", "deleted": true}`, "deleted"},
		} {
			status, _, raw := push(t, e, tok, c.headers, c.body)
			if status != http.StatusOK {
				t.Errorf("%s: %d %s, want 200", name, status, raw)
				continue
			}
			checkSchema(t, doc, raw, "WebhookResult")
			r := decodeAs[struct {
				Changed bool
				Skipped string
			}](t, raw)
			if r.Changed || !strings.Contains(r.Skipped, c.want) {
				t.Errorf("%s: %s, want changed false and a reason naming %q", name, raw, c.want)
			}
		}
		if e.Nomad.Registrations() != before {
			t.Error("a skipped push registered a job")
		}
	})
	t.Run("the commit that is live builds nothing", func(t *testing.T) {
		status, _, raw := push(t, e, tok, gh, body("refs/heads/main", apiBuildCommit))
		r := decodeAs[struct {
			Changed bool
			Skipped string
		}](t, raw)
		if status != http.StatusOK || r.Changed || r.Skipped != "" {
			t.Errorf("%d %s, want 200, changed false, no skipped", status, raw)
		}
	})
	t.Run("GitLab's token header", func(t *testing.T) {
		lab := map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": tok}
		status, _, raw := push(t, e, tok, lab, `{"ref": "refs/heads/dev", "checkout_sha": "`+apiBuildCommit+`", "after": "`+apiBuildCommit+`"}`)
		if status != http.StatusOK || !strings.Contains(string(raw), "refs/heads/dev") {
			t.Errorf("%d %s, want 200 skipped for dev", status, raw)
		}
		lab["X-Gitlab-Token"] = "cvwh_wrong"
		if status, _, raw := push(t, e, tok, lab, `{}`); status != http.StatusUnauthorized {
			t.Errorf("a wrong X-Gitlab-Token: %d %s, want 401", status, raw)
		}
		// The query or Authorization wins when sent: a different one in the header isn't tried.
		both := map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": tok, "Authorization": "Bearer cvwh_wrong"}
		if status, _, _ := push(t, e, tok, both, `{}`); status != http.StatusUnauthorized {
			t.Errorf("a wrong Authorization with a right X-Gitlab-Token: %d, want 401", status)
		}
	})
	t.Run("the token is never in the log", func(t *testing.T) {
		if strings.Contains(e.Logs.String(), tok) {
			t.Error("the log holds the webhook token")
		}
	})
}

// A deploy token deploys its own built app, which builds; everything else about builds (the output, the
// Git access, the settings) is the owner's: the default deny answers 403.
func TestDeployTokenAndBuilds(t *testing.T) {
	e := buildEnv(t)
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	id := "shop--staging--web"

	req, _ := http.NewRequest(http.MethodPost, e.RawURL+"/v1/apps/"+id+"/deployments", strings.NewReader(builtBody(apiBuildImage, publicBuild)))
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deploy with a deploy token: %d %s, want 202", resp.StatusCode, raw)
	}
	d := decodeAs[builtDeployment](t, raw)
	if d.Build == nil || d.Build.Status != "queued" {
		t.Errorf("deployment = %s, want a queued build", raw)
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/apps/" + id + "/deployments/" + d.ID + "/build"},
		{http.MethodGet, "/v1/apps/" + id + "/services/web/git-access"},
		{http.MethodPost, "/v1/apps/" + id + "/services/web/git-access/deploy-key"},
		{http.MethodPut, "/v1/apps/" + id + "/services/web/git-access/token"},
		{http.MethodDelete, "/v1/apps/" + id + "/services/web/git-access"},
		{http.MethodGet, "/v1/builds/settings"},
		{http.MethodPut, "/v1/builds/settings"},
	} {
		req, _ := http.NewRequest(c.method, e.RawURL+c.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", auth)
		req.Header.Set("Content-Type", "application/json")
		resp, err := noRedirects.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || decodeAs[errorBody](t, raw).Error.Code != codeTokenScope {
			t.Errorf("%s %s with a deploy token = %d %s, want 403 token_scope", c.method, c.path, resp.StatusCode, raw)
		}
	}
}
