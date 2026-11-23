package main

// The build-from-git gate step (feature 050, plan D12). Lab only: it needs a registry and a Git server on
// the VM's loopback (lab_git_test.go), which a real host doesn't have for a test to use. It builds an app
// from a repository in four ways and checks what a user would see and what must never leak:
//
//  1. a public repository over HTTP: 202 with a queued build, healthy, answered through Traefik, the
//     build's commit, author, subject, digest and log, the origin;
//  2. the push webhook with GitHub-shaped bodies: v2 builds and deploys, another branch is skipped, the
//     same commit is unchanged;
//  3. a broken Dockerfile: the deployment fails with "build failed" and the build's last lines, and v2
//     still answers;
//  4. the same repository over SSH with a deploy key and the server's host key: v3 builds and deploys;
//
// then the deploy key's private half is searched for in every answer, the log, the app's Nomad jobs, the
// export and the SQLite file, and the app is deleted: its Git access, build cache and build jobs go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// gateBuildDeployment is a deployment as the build steps read it.
type gateBuildDeployment struct {
	gateDeployment
	Build  *gateBuildSummary `json:"build"`
	Origin *struct {
		Commit, Ref, Actor, Repository string
	} `json:"origin"`
}

// gateBuildSummary is the API's Build (BuildSummary plus the log).
type gateBuildSummary struct {
	ID, Service, Status, Repository, Branch       string
	Commit, Author, Subject, Image, Digest, Error string
	Log                                           string
	LogCut                                        bool `json:"logCut"`
}

// buildSettings is the installation's build limits as the API has them.
type buildSettingsBody struct {
	MemoryMB       int `json:"memoryMB"`
	CPUMHz         int `json:"cpuMHz"`
	TimeoutMinutes int `json:"timeoutMinutes"`
	Concurrency    int `json:"concurrency"`
	CacheMB        int `json:"cacheMB"`
}

// lowerBuildSettings gives builds a size the 2 GiB lab VM can run and puts the old limits back when the
// test ends.
func (g *gate) lowerBuildSettings(t *testing.T) {
	t.Helper()
	var old buildSettingsBody
	g.getJSON(t, "/v1/builds/settings", &old)
	small, _ := json.Marshal(buildSettingsBody{MemoryMB: 768, CPUMHz: 1000, TimeoutMinutes: 10, Concurrency: 1, CacheMB: 2048})
	g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/builds/settings", string(small))
	t.Cleanup(func() {
		raw, _ := json.Marshal(old)
		if resp, err := g.apiRequest(http.MethodPut, "/v1/builds/settings", "application/json", string(raw)); err == nil {
			resp.Body.Close()
		}
	})
}

// pushHook calls the app's webhook the way GitHub does for a push of commit to ref and returns the
// status and the body of the answer.
func (g *gate) pushHook(t *testing.T, appID, token, ref, commit string) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"ref": ref, "after": commit, "deleted": false})
	req, err := http.NewRequest(http.MethodPost, g.target.Webhook+"/v1/webhooks/"+appID, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Event", "push")
	resp, err := g.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, raw
}

// waitBody waits for https://host/ to answer with a body that has want in it.
func (g *gate) waitBody(t *testing.T, host, want string, within time.Duration) {
	t.Helper()
	var last string
	if !waitUntil(within, func() bool {
		status, _, body, err := g.domainCall(host, http.MethodGet, "/", "", "")
		if err != nil {
			last = err.Error()
			return false
		}
		last = fmt.Sprintf("%d %q", status, body)
		return status == http.StatusOK && strings.Contains(body, want)
	}) {
		t.Fatalf("https://%s didn't answer %q within %v (last: %s)", host, want, within, last)
	}
}

// buildOf reads a deployment's build.
func (g *gate) buildOf(t *testing.T, appID, deploymentID string) gateBuildSummary {
	t.Helper()
	var b gateBuildSummary
	g.getJSON(t, "/v1/apps/"+appID+"/deployments/"+deploymentID+"/build", &b)
	return b
}

// deployBuildOf reads a deployment with its build and origin.
func (g *gate) deployBuildOf(t *testing.T, appID, deploymentID string) gateBuildDeployment {
	t.Helper()
	var d gateBuildDeployment
	g.getJSON(t, "/v1/apps/"+appID+"/deployments/"+deploymentID, &d)
	return d
}

func (g *gate) buildFromGitStep(t *testing.T) {
	if g.target.Public != nil {
		t.Skip("the build step needs the lab's own registry and Git server on the VM's loopback; it is lab only")
	}
	const appID, service = "gate-build", "web"
	host := g.extraHost(appID)

	g.lowerBuildSettings(t)
	lab := newBuildLab(t, g, "gate-gitserver", "gate-regbuild", 2222, 8098, 5001)
	lab.start(t)
	image := lab.regHost() + "/" + appID
	t.Cleanup(func() { // before lab.stop: the app's own jobs and variable first
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+appID, "", ""); err == nil {
			resp.Body.Close()
		}
	})
	if status, _ := g.send(t, http.MethodDelete, "/v1/apps/"+appID, ""); status == http.StatusNoContent {
		t.Log("an app left by an earlier run was deleted")
	}

	// What the step saw, to search for the key at the end.
	var seen []string
	note := func(raw []byte) []byte { seen = append(seen, string(raw)); return raw }

	// 1. A public repository over dumb HTTP.
	sha1 := lab.commit(t, labDockerfile("gate-build-v1"), "v1: the first version")
	lab.push(t, "main")
	publicBuild := map[string]any{"repository": lab.httpURL(), "access": "public"}
	status, raw := g.send(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", buildAppBody(image, host, publicBuild))
	note(raw)
	if status != http.StatusAccepted {
		t.Fatalf("deploy of a built app = %d, want 202: %s", status, raw)
	}
	var first gateBuildDeployment
	_ = json.Unmarshal(raw, &first)
	if first.Status != "pending" || first.Build == nil || first.Build.Status != "queued" {
		t.Fatalf("the 202 answer is %+v (build %+v), want a pending deployment with its build queued", first.gateDeployment, first.Build)
	}
	done := g.waitFinalOf(t, appID, first.ID, 20*time.Minute)
	if done.Status != "healthy" {
		var b gateBuildSummary
		g.getJSON(t, "/v1/apps/"+appID+"/deployments/"+first.ID+"/build", &b)
		t.Fatalf("the first build ended %s: %s\nbuild %s: %s\n%s", done.Status, done.Error, b.Status, b.Error, b.Log)
	}
	g.waitBody(t, host, "gate-build-v1", time.Minute)

	dep := g.deployBuildOf(t, appID, first.ID)
	b := g.buildOf(t, appID, first.ID)
	note([]byte(fmt.Sprintf("%+v %+v", dep, b)))
	if b.Status != "succeeded" || b.Commit != sha1 || b.Author != "Gate Test" || b.Subject != "v1: the first version" ||
		!strings.HasPrefix(b.Digest, "sha256:") || b.Branch != "main" || !strings.HasPrefix(b.Image, image+":git-") {
		t.Errorf("the build row is %+v, want succeeded at %s by Gate Test, with a digest and image %s:git-…", b, sha1, image)
	}
	if b.Log == "" {
		t.Errorf("the build kept no log")
	}
	if dep.Origin == nil || !strings.HasPrefix(sha1, dep.Origin.Commit) || dep.Origin.Commit == "" || dep.Origin.Ref != "refs/heads/main" || dep.Origin.Actor != "Gate Test" {
		t.Errorf("the deployment's origin is %+v, want the commit %s on refs/heads/main by Gate Test", dep.Origin, sha1)
	}

	// 2. The push webhook.
	token := g.webhookTokenOf(t, appID)
	sha2 := lab.commit(t, labDockerfile("gate-build-v2"), "v2: the second version")
	lab.push(t, "main")
	status, raw = g.pushHook(t, appID, token, "refs/heads/main", sha2)
	note(raw)
	var hooked struct {
		Changed    bool
		Skipped    string
		Deployment gateBuildDeployment
	}
	_ = json.Unmarshal(raw, &hooked)
	if status != http.StatusAccepted || !hooked.Changed {
		t.Fatalf("the push webhook for v2 = %d: %s, want 202 changed", status, raw)
	}
	if d := g.waitFinalOf(t, appID, hooked.Deployment.ID, 15*time.Minute); d.Status != "healthy" {
		t.Fatalf("v2's deployment ended %s: %s", d.Status, d.Error)
	}
	g.waitBody(t, host, "gate-build-v2", time.Minute)
	if b2 := g.buildOf(t, appID, hooked.Deployment.ID); b2.Commit != sha2 || b2.Status != "succeeded" {
		t.Errorf("v2's build is %+v, want succeeded at %s", b2, sha2)
	}

	status, raw = g.pushHook(t, appID, token, "refs/heads/dev", sha2)
	note(raw)
	hooked.Skipped, hooked.Changed = "", true
	_ = json.Unmarshal(raw, &hooked)
	if status != http.StatusOK || hooked.Changed || hooked.Skipped == "" {
		t.Errorf("a push to refs/heads/dev = %d: %s, want 200 unchanged with a skipped sentence", status, raw)
	}
	status, raw = g.pushHook(t, appID, token, "refs/heads/main", sha2)
	note(raw)
	hooked.Changed = true
	_ = json.Unmarshal(raw, &hooked)
	if status != http.StatusOK || hooked.Changed {
		t.Errorf("the same v2 commit again = %d: %s, want 200 unchanged", status, raw)
	}
	if n := g.deploymentsOf(t, appID); n != 2 {
		t.Errorf("the app has %d deployments after the skipped and unchanged pushes, want 2", n)
	}

	// 3. A broken Dockerfile fails with its last lines and v2 still answers.
	shaBad := lab.commit(t, labBrokenDockerfile, "v3: broken on purpose")
	lab.push(t, "main")
	status, raw = g.pushHook(t, appID, token, "refs/heads/main", shaBad)
	note(raw)
	hooked.Deployment = gateBuildDeployment{}
	_ = json.Unmarshal(raw, &hooked)
	if status != http.StatusAccepted {
		t.Fatalf("the push webhook for the broken commit = %d: %s, want 202", status, raw)
	}
	bad := g.waitFinalOf(t, appID, hooked.Deployment.ID, 15*time.Minute)
	if bad.Status != "failed" || !strings.Contains(bad.Error, "build failed") || !strings.Contains(bad.Error, "gate-build-broken-line") {
		t.Errorf("the broken build's deployment is %s with error %q, want failed, \"build failed\" and the build's lines", bad.Status, bad.Error)
	}
	if bb := g.buildOf(t, appID, hooked.Deployment.ID); bb.Status != "failed" || bb.Commit != shaBad {
		t.Errorf("the broken build's row is %+v, want failed at %s", bb, shaBad)
	}
	g.waitBody(t, host, "gate-build-v2", 30*time.Second)

	// 4. The deploy key over SSH.
	raw = g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/apps/"+appID+"/services/"+service+"/git-access/deploy-key", "")
	note(raw)
	var key struct{ Kind, PublicKey, Fingerprint string }
	if err := json.Unmarshal(raw, &key); err != nil || key.Kind != "deploy-key" || !strings.HasPrefix(key.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("createDeployKey answered %s", raw)
	}
	lab.setKeys(t, key.PublicKey)
	if v, _, err := g.target.Nomad.Variables().Read("caravana/builds/"+appID+"/"+service, nil); err != nil || v == nil {
		t.Fatalf("read the deploy key's variable: %v", err)
	} else {
		lab.waitKeyAccepted(t, v.Items["private_key"])
	}
	hostKey := lab.hostKey(t)
	sha3 := lab.commit(t, labDockerfile("gate-build-v4"), "v4: fixed, over SSH")
	lab.push(t, "main")
	sshBuild := map[string]any{"repository": lab.sshURL(), "access": "deploy-key", "hostKey": hostKey}
	// (The new key was accepted by the Git server above: the build job's clone retries nothing.)
	status, raw = g.send(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", buildAppBody(image, host, sshBuild))
	note(raw)
	if status != http.StatusAccepted {
		t.Fatalf("deploy over SSH = %d, want 202: %s", status, raw)
	}
	var ssh gateBuildDeployment
	_ = json.Unmarshal(raw, &ssh)
	sshDone := g.waitFinalOf(t, appID, ssh.ID, 20*time.Minute)
	if sshDone.Status != "healthy" {
		bs := g.buildOf(t, appID, ssh.ID)
		t.Fatalf("the SSH build ended %s: %s\nbuild %s: %s\n%s", sshDone.Status, sshDone.Error, bs.Status, bs.Error, bs.Log)
	}
	g.waitBody(t, host, "gate-build-v4", time.Minute)
	if bs := g.buildOf(t, appID, ssh.ID); bs.Commit != sha3 || bs.Status != "succeeded" {
		t.Errorf("the SSH build is %+v, want succeeded at %s", bs, sha3)
	}

	// 5. The private key is nowhere but its Nomad Variable.
	nomad := g.target.Nomad
	v, _, err := nomad.Variables().Read("caravana/builds/"+appID+"/"+service, nil)
	if err != nil || v == nil || v.Items["private_key"] == "" {
		t.Fatalf("the deploy key's variable caravana/builds/%s/%s has no private_key: %v", appID, service, err)
	}
	var needles []string
	for _, line := range strings.Split(strings.TrimSpace(v.Items["private_key"]), "\n") {
		if len(line) >= 40 {
			needles = append(needles, line)
		}
	}
	needles = append(needles, "BEGIN OPENSSH PRIVATE KEY")
	if len(needles) < 3 {
		t.Fatalf("the stored private key has too few lines to search for: %d", len(needles))
	}
	var list struct {
		Deployments []struct{ ID string } `json:"deployments"`
	}
	g.getJSON(t, "/v1/apps/"+appID+"/deployments", &list)
	note(g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps/"+appID, ""))
	note(g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps/"+appID+"/deployments", ""))
	note(g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps/"+appID+"/services/"+service+"/git-access", ""))
	note(g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps/"+appID+"/jobspec", ""))
	for _, d := range list.Deployments {
		note(g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps/"+appID+"/deployments/"+d.ID, ""))
		if status, raw := g.send(t, http.MethodGet, "/v1/apps/"+appID+"/deployments/"+d.ID+"/build", ""); status == http.StatusOK {
			note(raw)
		}
	}
	places := map[string]string{"the answers the step got": strings.Join(seen, "\n")}
	stubs, _, err := nomad.Jobs().List(&api.QueryOptions{Prefix: appID})
	if err != nil {
		t.Fatalf("list the app's Nomad jobs: %v", err)
	}
	for _, s := range stubs {
		job, _, err := nomad.Jobs().Info(s.ID, nil)
		if err != nil {
			continue
		}
		raw, _ := json.Marshal(job)
		places["the Nomad job "+s.ID] = string(raw)
	}
	if g.target.ServerLog != nil {
		places["Caravana's log"] = g.target.ServerLog()
	}
	if dir := g.target.DataDir; dir != "" {
		for _, f := range []string{"caravana.db", "caravana.db-wal"} {
			b, _ := os.ReadFile(filepath.Join(dir, f))
			places["the SQLite file "+f] = string(b)
		}
	}
	for where, text := range places {
		for _, needle := range needles {
			if strings.Contains(text, needle) {
				t.Errorf("%s holds the deploy key's private half (%.12s…)", where, needle)
				break
			}
		}
	}

	// 6. Deleting the app takes the access, the cache and the build jobs.
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+appID, "")
	waitFor(t, appID+"'s Git access, build cache and build jobs to be gone", 2*time.Minute, func() bool {
		if v, _, err := nomad.Variables().Read("caravana/builds/"+appID+"/"+service, nil); err == nil && v != nil {
			return false
		}
		vols, _, err := nomad.HostVolumes().List(&api.HostVolumeListRequest{}, &api.QueryOptions{Prefix: appID + "_"})
		if err != nil || len(vols) != 0 {
			return false
		}
		jobs, _, err := nomad.Jobs().List(&api.QueryOptions{Prefix: appID})
		return err == nil && len(jobs) == 0
	})
	vars, _, err := nomad.Variables().List(&api.QueryOptions{Prefix: "caravana/builds/" + appID})
	if err != nil || len(vars) != 0 {
		t.Errorf("variables under caravana/builds/%s after the delete: %d (%v)", appID, len(vars), err)
	}
}
