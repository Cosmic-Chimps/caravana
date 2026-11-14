package main

// The secret-references gate step (feature 037, tasks.md C2). It is in its own
// file only because gate_test.go is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// secretRefsStep (feature 037): an app's secret read from Bella Baxter at deploy,
// against the lab's fake Bella (bellatest, which speaks the SDK's HMAC and
// encrypted answers), with the token Caravana really runs with:
//
//  1. a provider saved through the API answers no credential, and its Test reads
//     the fake's two secrets;
//  2. a busybox app with one reference and one typed secret deploys healthy, and
//     printenv in its copy shows both values;
//  3. the value changes in Bella: Refresh secrets answers 202 with a deployment
//     marked secretsRefreshed whose source is the live one, and the new copy has
//     the new value; a second refresh answers 200 changed false;
//  4. Bella down: a restarted copy still has the value; a deploy and a refresh are
//     502 secret_provider_unavailable naming the reference; Bella up again;
//  5. the provider limited to another project: a deploy is 400 invalid_field on
//     the reference's provider; removing it while the app uses it is 409
//     secret_provider_in_use;
//  6. a path Bella doesn't have is 422 secret_not_found on its path;
//  7. no value read from Bella, nor the API key or the private key, is in any
//     answer, the Nomad job, the jobspec export, Caravana's log or its SQLite
//     files (printenv's own answers are left out: they show the value on purpose);
//  8. the app, the provider and the project are removed.
//
// It needs the fake Bella on Caravana's loopback, so it runs only in the lab.
func (g *gate) secretRefsStep(t *testing.T) {
	bella := g.target.Bella
	if bella == nil {
		t.Skip("the fake Bella runs beside an in-process Caravana only: the lab")
	}
	const (
		id       = "lab037"
		provider = "lab037-bella"
		project  = "lab037p"
	)
	sentinel := "s3cret-sentinel-037-" + g.run
	rotated := sentinel + "-rotated"
	bella.Set("GATE_REF", sentinel)
	bella.Set("GATE_OTHER", "unused-"+g.run)

	remove := func() {
		for _, p := range []string{"/v1/apps/" + id, "/v1/secret-providers/" + provider, "/v1/projects/" + project} {
			if resp, err := g.apiRequest(http.MethodDelete, p, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // leftovers from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	t.Cleanup(func() {
		bella.SetDown(false)
		remove()
	})
	answers := map[string]string{} // everything read, searched for secrets at the end

	// 1. The provider, and its test.
	putProvider := func(allowed []string) []byte {
		body := map[string]any{"kind": "bella", "allowed": allowed,
			"bella":  map[string]string{"url": bella.URL(), "project": bella.Project(), "environment": bella.Environment()},
			"apiKey": bella.APIKey(), "privateKey": bella.PrivateKeyPEM()}
		raw, _ := json.Marshal(body)
		status, ans := g.send(t, http.MethodPut, "/v1/secret-providers/"+provider, string(raw))
		if status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("putSecretProvider = %d: %s", status, ans)
		}
		return ans
	}
	answers["putSecretProvider"] = string(putProvider([]string{}))
	raw := g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/secret-providers/"+provider+"/test", "")
	answers["testSecretProvider"] = string(raw)
	var tested struct {
		LastTest struct {
			OK    bool `json:"ok"`
			Count int  `json:"count"`
		} `json:"lastTest"`
	}
	if err := json.Unmarshal(raw, &tested); err != nil || !tested.LastTest.OK || tested.LastTest.Count != 2 {
		t.Fatalf("testSecretProvider = %s, want ok with 2 secrets readable", raw)
	}

	// 2. Deploy with a reference and a typed secret.
	web := actionsWeb("", 1)
	web["secrets"] = []map[string]any{
		{"name": "GATE_REF", "from": map[string]string{"provider": provider, "path": "GATE_REF"}},
		{"name": "GATE_TYPED", "value": "typed-" + g.run},
	}
	deployBody := labApp(web)
	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", deployBody)
	answers["deployApp"] = string(raw)
	var first gateDeployment
	if err := json.Unmarshal(raw, &first); err != nil || first.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, first.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	// envIs waits until printenv name in the app's one running copy prints want,
	// and returns the last value read. Right after a rollout the old copy may
	// still be listed, or an exec may come back without its output (task phpv),
	// so a single read can miss; the value has a minute and a half to show.
	envIs := func(name, want string) (string, bool) {
		t.Helper()
		last := ""
		ok := waitUntil(90*time.Second, func() bool {
			copies := g.runningCopies(t, id)
			if len(copies) != 1 {
				return false
			}
			body, _ := json.Marshal(map[string]any{"command": []string{"printenv", name}})
			status, raw := g.send(t, http.MethodPost, "/v1/apps/"+id+"/copies/"+copies[0]+"/run", string(body))
			var run gateRun
			if status != http.StatusOK || json.Unmarshal(raw, &run) != nil || run.ExitCode == nil || *run.ExitCode != 0 {
				return false
			}
			last = strings.TrimSpace(run.Stdout)
			return last == want
		})
		return last, ok
	}
	if got, ok := envIs("GATE_REF", sentinel); !ok {
		t.Fatalf("GATE_REF in the copy is not the value set in Bella (%d bytes)", len(got))
	}
	if got, ok := envIs("GATE_TYPED", "typed-"+g.run); !ok {
		t.Errorf("GATE_TYPED in the copy = %q, want the typed value", got)
	}

	// 3. Rotate in Bella, then Refresh secrets.
	bella.Set("GATE_REF", rotated)
	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/secrets/refresh", "")
	answers["refreshSecrets"] = string(raw)
	var refreshed struct {
		Changed    bool `json:"changed"`
		Deployment struct {
			ID               string `json:"id"`
			Source           string `json:"source"`
			SecretsRefreshed bool   `json:"secretsRefreshed"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(raw, &refreshed); err != nil || !refreshed.Changed || !refreshed.Deployment.SecretsRefreshed ||
		refreshed.Deployment.Source != first.ID {
		t.Fatalf("refreshSecrets = %s, want changed with a refresh deployment whose source is %s", raw, first.ID)
	}
	if final := g.waitFinalOf(t, id, refreshed.Deployment.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the refresh ended %s: %s", final.Status, final.Error)
	}
	if got, ok := envIs("GATE_REF", rotated); !ok {
		t.Fatalf("GATE_REF after the refresh is not the rotated value (%d bytes)", len(got))
	}
	raw = g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+id+"/secrets/refresh", "")
	answers["refreshSecrets unchanged"] = string(raw)
	if !strings.Contains(string(raw), `"changed":false`) {
		t.Errorf("a second refresh = %s, want changed false", raw)
	}

	// 4. Bella down: running and restarted copies keep the value; deploys fail.
	bella.SetDown(true)
	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/restart", "")
	var restart gateAction
	if err := json.Unmarshal(raw, &restart); err != nil || restart.ID == "" {
		t.Fatalf("restartApp = %s", raw)
	}
	if ended := g.waitActionEnds(t, id, restart.ID, 3*time.Minute); ended.Status != "succeeded" {
		t.Fatalf("the restart while Bella is down ended %s (%s)", ended.Status, ended.Error)
	}
	if got, ok := envIs("GATE_REF", rotated); !ok {
		t.Fatalf("a copy restarted while Bella is down lost GATE_REF's value (%d bytes)", len(got))
	}
	ref := "services[0].secrets[0].from"
	g.refusal(t, http.StatusBadGateway, "secret_provider_unavailable", ref, http.MethodPost, "/v1/apps/"+id+"/deployments", deployBody)
	g.refusal(t, http.StatusBadGateway, "secret_provider_unavailable", ref, http.MethodPost, "/v1/apps/"+id+"/secrets/refresh", "")
	bella.SetDown(false)
	if got, ok := envIs("GATE_REF", rotated); !ok {
		t.Errorf("GATE_REF changed after a failed deploy (%d bytes)", len(got))
	}

	// 5. A project not allowed; a removal while in use.
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"`+project+`"}`)
	answers["putSecretProvider limited"] = string(putProvider([]string{project}))
	g.refusal(t, http.StatusBadRequest, "invalid_field", ref+".provider", http.MethodPost, "/v1/apps/"+id+"/deployments", deployBody)
	answers["putSecretProvider all"] = string(putProvider([]string{}))
	msg := g.refusal(t, http.StatusConflict, "secret_provider_in_use", "", http.MethodDelete, "/v1/secret-providers/"+provider, "")
	if !strings.Contains(msg, id) {
		t.Errorf("the in-use refusal %q doesn't name the app %s", msg, id)
	}

	// 6. A path Bella doesn't have.
	missing := strings.Replace(deployBody, `"path":"GATE_REF"`, `"path":"GATE_MISSING"`, 1)
	if missing == deployBody {
		t.Fatalf("the deploy body has no GATE_REF path to replace: %s", deployBody)
	}
	g.refusal(t, http.StatusUnprocessableEntity, "secret_not_found", ref+".path", http.MethodPost, "/v1/apps/"+id+"/deployments", missing)

	// 7. Nothing secret anywhere.
	for _, p := range []string{
		"/v1/apps/" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id + "/deployments/" + first.ID,
		"/v1/apps/" + id + "/deployments/" + refreshed.Deployment.ID, "/v1/apps/" + id + "/jobspec",
		"/v1/apps", "/v1/attention", "/v1/secret-providers",
	} {
		status, body := g.send(t, http.MethodGet, p, "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d: %s", p, status, body)
		}
		answers[p] = string(body)
	}
	if job, _, err := g.target.Nomad.Jobs().Info(id, nil); err == nil {
		answers["the Nomad job"] = string(mustJSON(t, job))
	} else {
		t.Errorf("read the job %s: %v", id, err)
	}
	if g.target.ServerLog != nil {
		answers["Caravana's log"] = g.target.ServerLog()
	}
	if dir := g.target.DataDir; dir != "" {
		for _, f := range []string{"caravana.db", "caravana.db-wal"} {
			b, _ := os.ReadFile(filepath.Join(dir, f))
			answers["the SQLite file "+f] = string(b)
		}
	}
	secrets := map[string]string{
		"the value read from Bella":     sentinel,
		"the rotated value":             rotated,
		"the API key":                   bella.APIKey(),
		"the private key's base64 body": privateKeyBody(bella.PrivateKeyPEM()),
	}
	for where, text := range answers {
		for what, secret := range secrets {
			if secret != "" && strings.Contains(text, secret) {
				t.Errorf("%s holds %s", where, what)
			}
		}
	}

	// 8. Remove the app, then the provider (no longer used) and the project.
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/secret-providers/"+provider, "")
	if v, _, err := g.target.Nomad.Variables().Read("caravana/secret-providers/"+provider, nil); err == nil && v != nil {
		t.Errorf("the provider's credentials are still in Nomad after it was removed")
	}
}

// privateKeyBody is the longest line of a PEM key, the part that would show if
// the key leaked in another wrapping.
func privateKeyBody(pemText string) string {
	longest := ""
	for _, line := range strings.Split(pemText, "\n") {
		if line = strings.TrimSpace(line); !strings.HasPrefix(line, "-----") && len(line) > len(longest) {
			longest = line
		}
	}
	return longest
}
