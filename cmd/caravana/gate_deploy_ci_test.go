package main

// The deploy-from-ci gate step (feature 020, plan.md "Lab"), and what it needs.
// It is in its own file only because gate_test.go is long; runGate lists it with
// the other steps.
//
// The step runs the real caravana-action script (caravana-deploy.sh, a client of
// the public API with curl and jq) against this Caravana, with the environment
// GitHub gives a step and a deploy token for one app. The script lives in another
// repository, so the step finds it at $CARAVANA_ACTION_DIR (default: a
// caravana-action directory beside this repository) and skips with a note when
// it isn't there.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// actionScript is the path of the action's script, or "" with the reason it
// can't be found.
func actionScript() (path, why string) {
	dir := os.Getenv("CARAVANA_ACTION_DIR")
	if dir == "" {
		// The repository's root is two levels above this package.
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			return "", "can't find this repository's root: " + err.Error()
		}
		dir = filepath.Join(root, "..", "caravana-action")
	}
	path = filepath.Join(dir, "caravana-deploy.sh")
	if _, err := os.Stat(path); err != nil {
		return "", "the action's script isn't at " + path + ": clone Cosmic-Chimps/caravana-action there or set CARAVANA_ACTION_DIR"
	}
	return path, ""
}

// sendAs is send with another token than the gate's own.
func (g *gate) sendAs(t *testing.T, token, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, g.target.API+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// actionRun is one run of the action's script: how it ended, what it printed,
// the step's outputs and its job summary.
type actionRun struct {
	Code    int
	Log     string
	Outputs map[string]string
	Summary string
}

// runAction runs the script as a workflow step would: with the inputs as
// CARAVANA_* variables and the run's facts as the GITHUB_* ones. extra adds or
// replaces variables.
func (g *gate) runAction(t *testing.T, script, token string, extra map[string]string) actionRun {
	t.Helper()
	dir := t.TempDir()
	outputs, summary := filepath.Join(dir, "output"), filepath.Join(dir, "summary")
	for _, f := range []string{outputs, summary} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]string{
		"CARAVANA_URL":          g.target.API,
		"CARAVANA_TOKEN":        token,
		"CARAVANA_TIMEOUT":      "300",
		"CARAVANA_POLL_SECONDS": "1",
		"GITHUB_SHA":            "0123456789abcdef0123456789abcdef01234567",
		"GITHUB_REF":            "refs/heads/main",
		"GITHUB_ACTOR":          "lab-ana",
		"GITHUB_SERVER_URL":     "https://github.com",
		"GITHUB_REPOSITORY":     "acme/lab020",
		"GITHUB_RUN_ID":         "212",
		"GITHUB_OUTPUT":         outputs,
		"GITHUB_STEP_SUMMARY":   summary,
	}
	for k, v := range extra {
		vars[k] = v
	}
	// The script gets nothing of this process's CARAVANA_* or GITHUB_* variables.
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CARAVANA_") && !strings.HasPrefix(kv, "GITHUB_") {
			env = append(env, kv)
		}
	}
	for k, v := range vars {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command("bash", script)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	run := actionRun{Outputs: map[string]string{}}
	var exit *exec.ExitError
	switch err := cmd.Run(); {
	case err == nil:
	case errors.As(err, &exit):
		run.Code = exit.ExitCode()
	default:
		t.Fatalf("run the action's script: %v", err)
	}
	run.Log = out.String()
	if raw, err := os.ReadFile(outputs); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				run.Outputs[k] = v
			}
		}
	}
	if raw, err := os.ReadFile(summary); err == nil {
		run.Summary = string(raw)
	}
	return run
}

// deployFromCIStep (feature 020): the action's script, with a deploy token for
// one app, deploys and promotes that app in a project. In order:
//
//  1. a project with staging and production is made, and a deploy token for the
//     app name web in it (answer and list show the scope);
//  2. the script, given a definition file, creates lab020--staging--web (which
//     doesn't exist) and deploys it: healthy, outputs set, the token nowhere in
//     its log or summary, and the deployment's origin is the run it said;
//  3. the script with a new image deploys it into the stored app, keeping the
//     rest of its settings;
//  4. the deploy token creates production's web without deploying, then the script
//     promotes staging to production: production runs staging's image by digest,
//     with promotedFrom, and the origin is on the promotion;
//  5. a deploy of an image that doesn't exist ends the script with Caravana's code
//     and changes nothing that runs;
//  6. the token is refused everywhere else: the lists, tokens, another app, the
//     default project's app with the same name, and stopping or deleting its own.
func (g *gate) deployFromCIStep(t *testing.T) {
	script, why := actionScript()
	if script == "" {
		t.Skip(why)
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq isn't installed: the action's script needs curl and jq")
	}
	if g.target.Public != nil && g.target.Public.Staging {
		t.Skip("the host's certificate comes from Let's Encrypt's staging CA, which curl rejects")
	}

	const project = "lab020"
	stagingID, prodID := project+"--staging--web", project+"--production--web"
	tokenName := "lab020-ci-" + g.run

	var token, tokenID string
	remove := func() {
		for _, path := range []string{"/v1/apps/" + stagingID, "/v1/apps/" + prodID, "/v1/projects/" + project} {
			resp, err := g.apiRequest(http.MethodDelete, path, "", "")
			if err != nil {
				t.Logf("cleanup: DELETE %s: %v", path, err)
				continue
			}
			resp.Body.Close()
		}
		if tokenID != "" {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/tokens/"+tokenID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // leftovers from an aborted run, if any
	t.Cleanup(remove)

	service := func(image string) map[string]any {
		return map[string]any{
			"name": "web", "image": image,
			"ports":     []map[string]any{{"name": "http", "containerPort": 80}},
			"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
			"resources": labResources,
		}
	}

	// 1. The project, and a token for the app name web in it.
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"lab020","displayName":"Lab 020"}`)
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects/lab020/environments", `{"name":"staging"}`)
	g.refusal(t, http.StatusBadRequest, "invalid_field", "scope.project", http.MethodPost, "/v1/tokens",
		`{"name":"`+tokenName+`-x","scope":{"project":"nope020","app":"web"}}`)
	raw := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"`+tokenName+`","scope":{"project":"lab020","app":"web"}}`)
	var made struct {
		ID, Token string
		Scope     struct{ Project, App string }
	}
	if err := json.Unmarshal(raw, &made); err != nil || made.Token == "" || made.Scope.Project != project || made.Scope.App != "web" {
		t.Fatalf("createToken with a scope = %s (%v), want a token scoped to lab020/web", raw, err)
	}
	token, tokenID = made.Token, made.ID
	if raw := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/tokens", ""); !strings.Contains(string(raw), `"app":"web"`) || strings.Contains(string(raw), token) {
		t.Errorf("listTokens = %s, want the scope shown and never the token", raw)
	}

	noToken := func(what string, run actionRun) {
		t.Helper()
		if strings.Contains(run.Log, token) || strings.Contains(run.Summary, token) {
			t.Errorf("%s: the token is in the script's log or summary", what)
		}
	}
	originOf := func(appID, deploymentID string) (commit, ref, actor, run string) {
		t.Helper()
		var d struct {
			Trigger string
			Origin  struct{ Commit, Ref, Actor, Run, Repository string }
		}
		g.getJSON(t, "/v1/apps/"+appID+"/deployments/"+deploymentID, &d)
		if d.Trigger != "manual" || d.Origin.Repository != "acme/lab020" {
			t.Errorf("deployment %s = %+v, want trigger manual and repository acme/lab020", deploymentID, d)
		}
		return d.Origin.Commit, d.Origin.Ref, d.Origin.Actor, d.Origin.Run
	}
	wantOrigin := func(appID, deploymentID string) {
		t.Helper()
		commit, ref, actor, run := originOf(appID, deploymentID)
		if commit != "0123456789abcdef0123456789abcdef01234567" || ref != "refs/heads/main" || actor != "lab-ana" ||
			run != "https://github.com/acme/lab020/actions/runs/212" {
			t.Errorf("origin of %s = %s %s %s %s, want the run the script described", deploymentID, commit, ref, actor, run)
		}
	}

	// 2. The first run creates the app from a definition file and deploys it.
	def := filepath.Join(t.TempDir(), "app.json")
	body, _ := json.Marshal(map[string]any{"source": map[string]string{"kind": "image"}, "services": []map[string]any{service("traefik/whoami:v1.10")}})
	if err := os.WriteFile(def, body, 0o600); err != nil {
		t.Fatal(err)
	}
	first := g.runAction(t, script, token, map[string]string{"CARAVANA_APP": stagingID, "CARAVANA_DEFINITION": def})
	noToken("first run", first)
	if first.Code != 0 || first.Outputs["status"] != "healthy" || first.Outputs["deployment-id"] == "" || first.Outputs["app-id"] != stagingID {
		t.Fatalf("first run exited %d with outputs %v:\n%s", first.Code, first.Outputs, first.Log)
	}
	if !strings.Contains(first.Summary, "healthy") {
		t.Errorf("the job summary doesn't say healthy:\n%s", first.Summary)
	}
	wantOrigin(stagingID, first.Outputs["deployment-id"])
	v1 := g.deploymentOf(t, stagingID, first.Outputs["deployment-id"])

	// 3. The second run deploys a new image into the stored app.
	second := g.runAction(t, script, token, map[string]string{"CARAVANA_APP": stagingID, "CARAVANA_IMAGE": "traefik/whoami:v1.11"})
	noToken("second run", second)
	if second.Code != 0 || second.Outputs["status"] != "healthy" || second.Outputs["deployment-id"] == first.Outputs["deployment-id"] {
		t.Fatalf("second run exited %d with outputs %v:\n%s", second.Code, second.Outputs, second.Log)
	}
	wantOrigin(stagingID, second.Outputs["deployment-id"])
	v2 := g.deploymentOf(t, stagingID, second.Outputs["deployment-id"])
	if v2.Digests["web"] == v1.Digests["web"] {
		t.Errorf("the second run deployed %s again, want another image", v2.Digests["web"])
	}
	var stored struct {
		App struct {
			Services []struct {
				Image string
				Ports []struct{ ContainerPort int }
			}
		}
	}
	g.getJSON(t, "/v1/apps/"+stagingID, &stored)
	if len(stored.App.Services) != 1 || stored.App.Services[0].Image != "traefik/whoami:v1.11" ||
		len(stored.App.Services[0].Ports) != 1 || stored.App.Services[0].Ports[0].ContainerPort != 80 {
		t.Errorf("the stored app = %+v, want the new image and the rest as it was", stored.App)
	}

	// 4. The token makes production's web without deploying; the script promotes.
	status, raw := g.sendAs(t, token, http.MethodPost, "/v1/projects/lab020/environments/production/apps?deploy=false",
		`{"appName":"web","source":{"kind":"image"},"services":[`+string(mustJSON(t, service("traefik/whoami:v1.10")))+`]}`)
	if status != http.StatusCreated {
		t.Fatalf("createEnvironmentApp with the deploy token = %d %s, want 201", status, raw)
	}
	promo := g.runAction(t, script, token, map[string]string{"CARAVANA_APP": stagingID, "CARAVANA_PROMOTE": "production"})
	noToken("promotion", promo)
	if promo.Code != 0 || promo.Outputs["promotion-status"] != "healthy" || promo.Outputs["promotion-id"] == "" {
		t.Fatalf("the promoting run exited %d with outputs %v:\n%s", promo.Code, promo.Outputs, promo.Log)
	}
	wantOrigin(prodID, promo.Outputs["promotion-id"])
	var promoted gatePlaceDeployment
	g.getJSON(t, "/v1/apps/"+prodID+"/deployments/"+promo.Outputs["promotion-id"], &promoted)
	if promoted.PromotedFrom == nil || promoted.PromotedFrom.App != stagingID || promoted.PromotedFrom.Environment != "staging" ||
		promoted.Digests["web"] != v2.Digests["web"] {
		t.Errorf("the promotion = %+v, want staging's %s by digest %s", promoted, stagingID, v2.Digests["web"])
	}
	job, _, err := g.target.Nomad.Jobs().Info(prodID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if image := job.TaskGroups[0].Tasks[0].Config["image"]; image != v2.Digests["web"] {
		t.Errorf("Nomad runs %v in production, want staging's %s", image, v2.Digests["web"])
	}

	// 5. An image that doesn't exist ends the script with Caravana's reason, and
	// staging keeps running what it ran.
	bad := g.runAction(t, script, token, map[string]string{"CARAVANA_APP": stagingID, "CARAVANA_IMAGE": "traefik/whoami:no-such-tag-020"})
	noToken("bad image", bad)
	if bad.Code == 0 || !strings.Contains(bad.Log, "::error::") || !strings.Contains(bad.Log, "image_not_found") {
		t.Errorf("a missing image: the script exited %d, want a failure naming image_not_found:\n%s", bad.Code, bad.Log)
	}
	job, _, err = g.target.Nomad.Jobs().Info(stagingID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if image := job.TaskGroups[0].Tasks[0].Config["image"]; image != v2.Digests["web"] {
		t.Errorf("staging runs %v after the failed run, want %s", image, v2.Digests["web"])
	}

	// 6. Anything else is refused, naming the scope.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/apps", ""},
		{http.MethodGet, "/v1/tokens", ""},
		{http.MethodPost, "/v1/tokens", `{"name":"` + tokenName + `-more"}`},
		{http.MethodGet, "/v1/apps/" + project + "--staging--other", ""},
		{http.MethodGet, "/v1/apps/web", ""},
		{http.MethodGet, "/v1/projects", ""},
		{http.MethodPost, "/v1/apps/" + stagingID + "/stop", ""},
		{http.MethodDelete, "/v1/apps/" + stagingID, ""},
		{http.MethodPost, "/v1/apps/" + stagingID + "/webhook", ""},
	} {
		status, raw := g.sendAs(t, token, c.method, c.path, c.body)
		var e struct {
			Error struct{ Code, Message string }
		}
		_ = json.Unmarshal(raw, &e)
		if status != http.StatusForbidden || e.Error.Code != "token_scope" || !strings.Contains(e.Error.Message, "app web of project lab020") {
			t.Errorf("%s %s with the deploy token = %d %s, want 403 token_scope naming app web of project lab020", c.method, c.path, status, raw)
		}
	}
	if status, raw := g.sendAs(t, token, http.MethodGet, "/v1/apps/"+stagingID, ""); status != http.StatusOK {
		t.Errorf("GET its own app with the deploy token = %d %s, want 200", status, raw)
	}
	// Revoked, it stops working at once.
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/tokens/"+tokenID, "")
	if status, _ := g.sendAs(t, token, http.MethodGet, "/v1/apps/"+stagingID, ""); status != http.StatusUnauthorized {
		t.Errorf("the revoked deploy token = %d, want 401", status)
	}
	tokenID = "" // already revoked
}

// mustJSON marshals v or fails the test.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
