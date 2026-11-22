package main

// The previews gate step (feature 049, plan.md "Lab"). It is in its own file only because
// gate_test.go is long; runGate lists it after deploy-from-ci.
//
// A preview is an environment named pr-<n>. The step walks what CI does with a deploy token
// (open it, deploy into it, close it), what the project's limit, password and expiry do, and, when
// the caravana-action beside this repository has the preview mode, runs that script with a fake
// pull request event. Run it alone with
//
//	CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestLab/^(api-needs-token|previews)$' -v -timeout 20m

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// previewsStep (feature 049). In order:
//
//  1. a project, lab049, with staging (deployed with a domain and a secret) and a deploy token for
//     lab049/web;
//  2. the token opens pr-7 and is refused qa, pr-07 and another project (403 token_scope); limit 1
//     makes pr-8 a 409 preview_limit naming pr-7;
//  3. a preview password is set while pr-7 is open (the answer warns and holds no password); the token
//     deploys web into pr-7 with a domain: healthy, Team only with a warning, no password in the
//     answer; the domain answers 401 without the password and 200 with it;
//  4. the password changed: the old one is 401, the new one 200, with no deploy (the job's version
//     doesn't move); getPreviews lists pr-7 and no password; removing the password with a preview
//     open is 409 previews_open;
//  5. the token can't delete its staging app (403), deletes its pr-7 app and then the environment
//     (204 each), and the job is gone;
//  6. with caravana-action's preview mode: an opened event makes pr-21 with the pull request's image,
//     the preview domain, no staging domain and the secret from the input, healthy; a closed event
//     removes the app and the environment, and the token is nowhere in the logs;
//  7. the reaper: pr-9 is left alone with the expiry shortened, and is closed (app, job and
//     environment) without anyone asking; then the password can be removed.
func (g *gate) previewsStep(t *testing.T) {
	const project = "lab049"
	stagingID := project + "--staging--web"
	previewID := func(n string) string { return project + "--pr-" + n + "--web" }
	stagingHost, host7 := "staging-web."+project+".test", "pr-7-web."+project+".test"
	stagingSecret := "p049-staging-secret-" + g.run
	pw1, pw2 := "preview-one-"+g.run, "preview-two-"+g.run
	tokenName := "lab049-ci-" + g.run

	var tokenID string
	remove := func() {
		for _, path := range []string{"/v1/apps/" + previewID("7"), "/v1/apps/" + previewID("9"), "/v1/apps/" + previewID("21"),
			"/v1/apps/" + stagingID, "/v1/projects/" + project} {
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
		if g.target.SetPreviewExpiry != nil {
			g.target.SetPreviewExpiry(0)
		}
	}
	remove() // leftovers from an aborted run, if any
	t.Cleanup(remove)

	jobGone := func(id string) bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	}
	envNames := func() []string {
		var p struct{ Environments []struct{ Name string } }
		g.getJSON(t, "/v1/projects/"+project, &p)
		var names []string
		for _, e := range p.Environments {
			names = append(names, e.Name)
		}
		return names
	}
	hasEnv := func(name string) bool {
		for _, n := range envNames() {
			if n == name {
				return true
			}
		}
		return false
	}
	// noSecret fails the step when a text holds a password or a secret value.
	noSecret := func(what, text string) {
		t.Helper()
		for _, secret := range []string{pw1, pw2, stagingSecret} {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds a secret or a preview password", what)
			}
		}
	}
	// 1. The project, staging and the token.
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"`+project+`","displayName":"Lab 049"}`)
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"staging"}`)
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+stagingID+"/deployments",
		labApp(labWeb("traefik/whoami:v1.10", "staging", stagingSecret, stagingHost)))
	var staging struct{ ID string }
	if err := json.Unmarshal(raw, &staging); err != nil || staging.ID == "" {
		t.Fatalf("deploy staging: %s", raw)
	}
	if final := g.waitFinalOf(t, stagingID, staging.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("staging ended %s: %s", final.Status, final.Error)
	}
	raw = g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"`+tokenName+`","scope":{"project":"`+project+`","app":"web"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(raw, &made); err != nil || made.Token == "" {
		t.Fatalf("createToken = %s (%v)", raw, err)
	}
	token := made.Token
	tokenID = made.ID

	// asToken calls the API with the deploy token and checks its answer.
	asToken := func(want int, method, path, body string) []byte {
		t.Helper()
		status, raw := g.sendAs(t, token, method, path, body)
		if status != want {
			t.Fatalf("%s %s with the deploy token = %d, want %d: %s", method, path, status, want, raw)
		}
		return raw
	}
	scopeRefused := func(method, path, body string) {
		t.Helper()
		status, raw := g.sendAs(t, token, method, path, body)
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if status != http.StatusForbidden || e.Error.Code != "token_scope" {
			t.Errorf("%s %s with the deploy token = %d %s, want 403 token_scope", method, path, status, raw)
		}
	}
	// deploy deploys the preview app of pull request n (a plain whoami with domain host, or none when
	// empty) with the deploy token and waits until it is healthy. It returns the final deployment.
	type previewDeployment struct {
		gatePlaceDeployment
		Warnings []struct{ Field, Message string } `json:"warnings"`
		Teams    map[string]string                 `json:"teamPasswords"`
	}
	deploy := func(n, host string) previewDeployment {
		t.Helper()
		id := previewID(n)
		svc := map[string]any{"name": "web", "image": "traefik/whoami:v1.10",
			"ports":     []map[string]any{{"name": "http", "containerPort": 80}},
			"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
			"resources": labResources}
		if host != "" {
			svc["domains"] = []map[string]any{{"host": host, "port": "http"}}
		}
		raw := asToken(http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(svc))
		noSecret("the deploy's answer", string(raw))
		var d struct{ ID string }
		if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
			t.Fatalf("deploy %s: %s", id, raw)
		}
		if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
		}
		var out previewDeployment
		raw = g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps/"+id+"/deployments/"+d.ID, "")
		noSecret("the deployment", string(raw))
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	jobVersion := func(id string) uint64 {
		t.Helper()
		job, _, err := g.target.Nomad.Jobs().Info(id, nil)
		if err != nil {
			t.Fatalf("read the job %s: %v", id, err)
		}
		return *job.Version
	}

	// 2. The token opens pr-7 and nothing else; the limit counts it.
	asToken(http.StatusCreated, http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"pr-7"}`)
	for _, name := range []string{"qa", "pr-07", "pr-"} {
		scopeRefused(http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"`+name+`"}`)
	}
	scopeRefused(http.MethodPost, "/v1/projects/default/environments", `{"name":"pr-7"}`) // another project
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "name", http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"pr-07"}`)
	g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/projects/"+project+"/previews/settings", `{"limit":1}`)
	msg := g.refusal(t, http.StatusConflict, "preview_limit", "name", http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"pr-8"}`)
	if !strings.Contains(msg, "pr-7") || !strings.Contains(msg, project) {
		t.Errorf("preview_limit says %q, want it to name the project and the open preview pr-7", msg)
	}
	if status, _ := g.sendAs(t, token, http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"pr-8"}`); status != http.StatusConflict {
		t.Errorf("the deploy token's pr-8 = %d, want 409 (the limit holds for any token)", status)
	}
	if hasEnv("pr-8") {
		t.Errorf("pr-8 exists after the limit refused it")
	}
	g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/projects/"+project+"/previews/settings", `{"limit":3}`)

	// 3. The password, set while pr-7 is open, then pr-7 deployed with a domain.
	raw = g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/projects/"+project+"/previews/settings", `{"password":"`+pw1+`"}`)
	noSecret("putPreviewSettings", string(raw))
	var settings struct {
		Limit, ExpireAfterDays int
		PasswordSet            bool
		Warnings               []struct{ Field, Message string }
	}
	if err := json.Unmarshal(raw, &settings); err != nil || !settings.PasswordSet || settings.Limit != 3 || len(settings.Warnings) != 1 ||
		!strings.Contains(settings.Warnings[0].Message, "pr-7") {
		t.Fatalf("putPreviewSettings = %s (%v), want passwordSet, limit 3 and one warning naming pr-7", raw, err)
	}
	pr7 := deploy("7", host7)
	teamWarning := false
	for _, w := range pr7.Warnings {
		teamWarning = teamWarning || (strings.HasSuffix(w.Field, ".teamOnly") && strings.Contains(w.Message, "preview password"))
	}
	if !teamWarning || len(pr7.Teams) != 0 {
		t.Errorf("the deployment has warnings %+v and teamPasswords %v, want the Team-only warning and no password made", pr7.Warnings, pr7.Teams)
	}
	var stored struct {
		App struct {
			Services []struct {
				Domains []struct {
					Host     string
					TeamOnly bool
				}
			}
		}
	}
	g.getJSON(t, "/v1/apps/"+previewID("7"), &stored)
	if len(stored.App.Services) != 1 || len(stored.App.Services[0].Domains) != 1 || !stored.App.Services[0].Domains[0].TeamOnly {
		t.Errorf("the stored preview app = %+v, want its domain Team only", stored.App)
	}
	skipDomain := g.target.Public != nil // domainCall reaches the lab's Traefik only
	if skipDomain {
		t.Log("on a real host the preview domains aren't called (domainCall is the lab's); the password checks of the API still run")
	} else {
		g.waitDomainStatus(t, http.StatusUnauthorized, 2*time.Minute, host7, http.MethodGet, "/", "", "")
		g.waitDomainStatus(t, http.StatusOK, 2*time.Minute, host7, http.MethodGet, "/", "team", pw1)
	}

	// 4. A new password applies to the open preview at once; nothing is deployed.
	before := jobVersion(previewID("7"))
	raw = g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/projects/"+project+"/previews/settings", `{"password":"`+pw2+`"}`)
	noSecret("putPreviewSettings (change)", string(raw))
	if !skipDomain {
		g.waitDomainStatus(t, http.StatusUnauthorized, 2*time.Minute, host7, http.MethodGet, "/", "team", pw1)
		g.waitDomainStatus(t, http.StatusOK, 2*time.Minute, host7, http.MethodGet, "/", "team", pw2)
	}
	if after := jobVersion(previewID("7")); after != before {
		t.Errorf("changing the password moved the job from version %d to %d: it needs no deploy", before, after)
	}
	raw = g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/projects/"+project+"/previews", "")
	noSecret("getPreviews", string(raw))
	var list struct {
		Settings struct {
			Limit, ExpireAfterDays int
			PasswordSet            bool
		}
		Previews []struct {
			Environment string
			Number      int
			ExpiresAt   time.Time
			Apps        []struct {
				ID        string
				State     string
				Addresses []string
				MemoryMB  int
			}
		}
	}
	if err := json.Unmarshal(raw, &list); err != nil || !list.Settings.PasswordSet || len(list.Previews) != 1 ||
		list.Previews[0].Environment != "pr-7" || list.Previews[0].Number != 7 || list.Previews[0].ExpiresAt.IsZero() ||
		len(list.Previews[0].Apps) != 1 || list.Previews[0].Apps[0].ID != previewID("7") ||
		len(list.Previews[0].Apps[0].Addresses) != 1 || list.Previews[0].Apps[0].Addresses[0] != host7 || list.Previews[0].Apps[0].MemoryMB <= 0 {
		t.Errorf("getPreviews = %s (%v), want pr-7 with its app, address and memory", raw, err)
	}
	g.refusal(t, http.StatusConflict, "previews_open", "removePassword", http.MethodPut, "/v1/projects/"+project+"/previews/settings", `{"removePassword":true}`)
	// The preview's domain can't take a password of its own.
	g.refusal(t, http.StatusConflict, "preview_password", "host", http.MethodPost, "/v1/apps/"+previewID("7")+"/domains/"+host7+"/team-password", "")
	// Deploy tokens can't read or change the settings, or close a preview whole.
	scopeRefused(http.MethodGet, "/v1/projects/"+project+"/previews", "")
	scopeRefused(http.MethodPut, "/v1/projects/"+project+"/previews/settings", `{"limit":20}`)
	scopeRefused(http.MethodDelete, "/v1/projects/"+project+"/previews/pr-7", "")

	// 5. The token closes its own preview, and only that.
	scopeRefused(http.MethodDelete, "/v1/apps/"+stagingID, "")
	scopeRefused(http.MethodDelete, "/v1/projects/"+project+"/environments/staging", "")
	asToken(http.StatusNoContent, http.MethodDelete, "/v1/apps/"+previewID("7"), "")
	waitFor(t, "the pr-7 job to be gone from Nomad", time.Minute, func() bool { return jobGone(previewID("7")) })
	asToken(http.StatusNoContent, http.MethodDelete, "/v1/projects/"+project+"/environments/pr-7", "")
	if hasEnv("pr-7") {
		t.Errorf("pr-7 is still an environment after the token deleted it")
	}
	if _, raw := g.send(t, http.MethodGet, "/v1/apps/"+stagingID, ""); !strings.Contains(string(raw), `"id":"`+stagingID+`"`) {
		t.Errorf("staging's app is gone after the token's close of pr-7: %s", raw)
	}

	// 6. caravana-action's preview mode, when the script beside this repository has it.
	g.previewsAction(t, project, stagingID, previewID("21"), pw2, stagingSecret, skipDomain, jobGone, hasEnv, noSecret)

	// 7. The reaper closes a preview nobody closed.
	if g.target.SetPreviewExpiry == nil {
		t.Log("the reaper check is skipped: only the lab can shorten the expiry")
	} else {
		asToken(http.StatusCreated, http.MethodPost, "/v1/projects/"+project+"/environments", `{"name":"pr-9"}`)
		deploy("9", "")
		g.target.SetPreviewExpiry(30 * time.Second)
		waitFor(t, "the reaper to close pr-9 (its app, its job and its environment)", 3*time.Minute, func() bool {
			return !hasEnv("pr-9") && jobGone(previewID("9"))
		})
		g.target.SetPreviewExpiry(0)
		if status, _ := g.send(t, http.MethodGet, "/v1/apps/"+previewID("9"), ""); status != http.StatusNotFound {
			t.Errorf("the reaper left the app of pr-9: GET = %d, want 404", status)
		}
	}

	// With no preview open the password can go, and the answer says so.
	raw = g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/projects/"+project+"/previews/settings", `{"removePassword":true}`)
	if err := json.Unmarshal(raw, &settings); err != nil || settings.PasswordSet {
		t.Errorf("removing the password = %s (%v), want passwordSet false", raw, err)
	}
	if g.target.ServerLog != nil {
		noSecret("the server log", g.target.ServerLog())
		if strings.Contains(g.target.ServerLog(), token) {
			t.Errorf("the server log holds the deploy token")
		}
	}
	if g.target.DataDir != "" {
		// SQLite holds the password's bcrypt hash and never the password.
		err := filepath.Walk(g.target.DataDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Size() > 64<<20 {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr == nil {
				noSecret("the data file "+filepath.Base(path), string(content))
			}
			return nil
		})
		if err != nil {
			t.Logf("scan the data directory: %v", err)
		}
	}
}

// previewsAction is the part of the previews step that runs caravana-action's script with a fake
// pull request event: opened makes pr-21 from staging, closed removes it. It skips with a note
// when the script isn't beside this repository, has no preview mode or needs a tool the machine
// lacks.
func (g *gate) previewsAction(t *testing.T, project, stagingID, previewApp, password, stagingSecret string, skipDomain bool,
	jobGone func(string) bool, hasEnv func(string) bool, noSecret func(what, text string)) {
	t.Helper()
	script, why := actionScript()
	if script == "" {
		t.Logf("caravana-action's preview mode isn't run: %s", why)
		return
	}
	if body, err := os.ReadFile(script); err != nil || !strings.Contains(string(body), "CARAVANA_PREVIEW") {
		t.Logf("caravana-action's preview mode isn't run: %s has no CARAVANA_PREVIEW (update the action)", script)
		return
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Log("caravana-action's preview mode isn't run: jq isn't installed")
		return
	}
	if g.target.Public != nil && g.target.Public.Staging {
		t.Log("caravana-action's preview mode isn't run: the host's certificate comes from Let's Encrypt's staging CA, which curl rejects")
		return
	}

	// A token for the action: a deploy token of its own, so the log check below has a value to search for.
	raw := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"lab049-action-`+g.run+`","scope":{"project":"`+project+`","app":"web"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(raw, &made); err != nil || made.Token == "" {
		t.Fatalf("createToken for the action = %s (%v)", raw, err)
	}
	t.Cleanup(func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/tokens/"+made.ID, "", ""); err == nil {
			resp.Body.Close()
		}
	})

	// event writes the file GitHub gives a pull_request step.
	event := func(action string) string {
		path := filepath.Join(t.TempDir(), "event.json")
		body, _ := json.Marshal(map[string]any{"action": action, "number": 21,
			"pull_request": map[string]any{"number": 21, "head": map[string]any{"repo": map[string]any{"fork": false}}}})
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	host := "pr-{pr}-web." + project + ".test"
	opened := func(action string) actionRun {
		return g.runAction(t, script, made.Token, map[string]string{
			"CARAVANA_PREVIEW": "true", "CARAVANA_PROJECT": project, "CARAVANA_NAME": "web",
			"CARAVANA_IMAGE":           "traefik/whoami:v1.11",
			"CARAVANA_PREVIEW_DOMAIN":  host,
			"CARAVANA_PREVIEW_SECRETS": "LAB_SECRET=preview-secret-" + g.run,
			"GITHUB_EVENT_NAME":        "pull_request", "GITHUB_EVENT_PATH": event(action),
		})
	}
	noToken := func(what string, run actionRun) {
		t.Helper()
		// The lines that must hold a value are its "::add-mask::<value>": that is
		// how the action tells GitHub to hide it, and GitHub never shows them.
		secret := "preview-secret-" + g.run
		var log []string
		for _, line := range strings.Split(run.Log, "\n") {
			if line != "::add-mask::"+made.Token && line != "::add-mask::"+secret {
				log = append(log, line)
			}
		}
		text := strings.Join(log, "\n")
		if strings.Contains(text, made.Token) || strings.Contains(run.Summary, made.Token) {
			t.Errorf("%s: the token is in the script's log or summary", what)
		}
		noSecret(what+": the script's log", text+run.Summary)
		if strings.Contains(text+run.Summary, secret) {
			t.Errorf("%s: a preview secret value is in the script's log or summary", what)
		}
	}

	run := opened("opened")
	noToken("opened", run)
	if run.Code != 0 || run.Outputs["status"] != "healthy" || run.Outputs["preview-environment"] != "pr-21" || run.Outputs["app-id"] != previewApp {
		t.Fatalf("opened exited %d with outputs %v:\n%s", run.Code, run.Outputs, run.Log)
	}
	wantURL := "https://pr-21-web." + project + ".test"
	if run.Outputs["preview-url"] != wantURL {
		t.Errorf("preview-url = %q, want %q", run.Outputs["preview-url"], wantURL)
	}
	var stored struct {
		App struct {
			Services []struct {
				Image   string
				Domains []struct {
					Host     string
					TeamOnly bool
				}
				Backup  any
				Secrets []struct{ Name string }
			}
		}
	}
	g.getJSON(t, "/v1/apps/"+previewApp, &stored)
	if len(stored.App.Services) != 1 || len(stored.App.Services[0].Domains) != 1 || stored.App.Services[0].Domains[0].Host != "pr-21-web."+project+".test" ||
		!stored.App.Services[0].Domains[0].TeamOnly || stored.App.Services[0].Backup != nil ||
		len(stored.App.Services[0].Secrets) != 1 || stored.App.Services[0].Secrets[0].Name != "LAB_SECRET" {
		t.Errorf("the preview app = %+v, want the preview domain (Team only), no backup and LAB_SECRET", stored.App)
	}
	if !skipDomain {
		g.waitDomainStatus(t, http.StatusUnauthorized, 2*time.Minute, "pr-21-web."+project+".test", http.MethodGet, "/", "", "")
		g.waitDomainStatus(t, http.StatusOK, 2*time.Minute, "pr-21-web."+project+".test", http.MethodGet, "/", "team", password)
	}
	// Staging was only read.
	if _, _, err := g.target.Nomad.Jobs().Info(stagingID, nil); err != nil {
		t.Errorf("staging's job is gone after a preview was made from it: %v", err)
	}

	// A later push deploys the new image into the same preview.
	again := opened("synchronize")
	noToken("synchronize", again)
	if again.Code != 0 || again.Outputs["status"] != "healthy" || again.Outputs["deployment-id"] == run.Outputs["deployment-id"] {
		t.Errorf("synchronize exited %d with outputs %v:\n%s", again.Code, again.Outputs, again.Log)
	}

	closed := opened("closed")
	noToken("closed", closed)
	if closed.Code != 0 || closed.Outputs["mode"] != "preview-closed" {
		t.Fatalf("closed exited %d with outputs %v:\n%s", closed.Code, closed.Outputs, closed.Log)
	}
	waitFor(t, "pr-21's job to be gone from Nomad", time.Minute, func() bool { return jobGone(previewApp) })
	if hasEnv("pr-21") {
		t.Errorf("pr-21 is still an environment after the closed event")
	}
}
