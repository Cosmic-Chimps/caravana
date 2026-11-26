package main

// The opensearch gate step (spec 060): a service with ulimits, a namespaced sysctl, a config file
// whose content is in the app and a secret file. It is in its own file only because gate_test.go
// is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openSearchImage is a 2.x release with an arm64 build too: the lab VM is aarch64.
const openSearchImage = "opensearchproject/opensearch:2.19.1"

// openSearchSentinel is the value of the secret a file holds. Only that file, the secret store and the
// run that reads the file may ever have it.
const openSearchSentinel = "s3cret-sentinel-060-gate"

// openSearchYML is the config the service mounts. The image appends to opensearch.yml when
// DISABLE_SECURITY_PLUGIN is set and can't on a read-only file, so the setting is in the file; GET / names
// the cluster, which shows this file is the one in use.
const openSearchYML = "cluster.name: gate060\nnetwork.host: 0.0.0.0\nplugins.security.disabled: true\n"

// openSearchService is the app's one service, with the secret's name and the file's path as arguments
// so the second app can reuse it.
func openSearchService(host string) map[string]any {
	svc := map[string]any{
		"name": "search", "image": openSearchImage,
		"env": []map[string]string{
			{"name": "discovery.type", "value": "single-node"},
			{"name": "DISABLE_INSTALL_DEMO_CONFIG", "value": "true"},
			{"name": "OPENSEARCH_JAVA_OPTS", "value": "-Xms512m -Xmx512m"},
		},
		"secrets": []map[string]any{
			{"name": "GATE_KEY", "value": "-----BEGIN KEY-----\n" + openSearchSentinel + "\n-----END KEY-----\n"},
			{"name": "GATE_PLAIN", "value": "plain-secret-060"},
		},
		"files": []map[string]any{
			{"path": "/usr/share/opensearch/config/opensearch.yml", "content": openSearchYML},
			{"path": "/etc/gate060/key.pem", "secret": "GATE_KEY", "mode": "0400", "uid": 1000, "gid": 1000},
		},
		"ulimits": map[string]any{
			"memlock": map[string]int{"soft": -1, "hard": -1},
			"nofile":  map[string]int{"soft": 65536, "hard": 65536},
		},
		"sysctls":   map[string]string{"net.ipv4.tcp_keepalive_time": "600"},
		"ports":     []map[string]any{{"name": "http", "containerPort": 9200}},
		"health":    map[string]any{"path": "/_cluster/health", "interval": "10s", "timeout": "5s"},
		"resources": map[string]any{"memoryMB": 1024, "memoryMaxMB": 1536},
	}
	if host != "" {
		svc["domains"] = []map[string]any{{"host": host, "port": "http"}}
	}
	return svc
}

// openSearchStep (spec 060, D11):
//
//  1. a sysctl that isn't namespaced (kernel.shmmax) is refused before anything deploys, naming its field;
//  2. the app deploys healthy, and GET / through its domain names the cluster the mounted config sets;
//  3. in the copy, ulimit -n is 65536, ulimit -l unlimited, the sysctl is 600, the secret file holds the
//     secret and the environment doesn't have it (another secret is in the environment);
//  4. the job holds names and no value, and the export plans against the live job with no diff;
//  5. the secret's value is in no answer, no log line, no byte of the data directory and no export.
func (g *gate) openSearchStep(t *testing.T) {
	const id, otherID = "gate-os", "gate-os2"
	host := g.extraHost("gate060-os")
	remove := func() {
		for _, a := range []string{id, otherID} {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+a, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // leftovers from an aborted run, if any
	for _, a := range []string{id, otherID} {
		waitFor(t, a+"'s Nomad job to be gone", time.Minute, func() bool {
			_, _, err := g.target.Nomad.Jobs().Info(a, nil)
			return err != nil
		})
	}
	t.Cleanup(remove)

	// 1. A sysctl of the server's is refused, and nothing is registered.
	other := openSearchService("")
	other["sysctls"] = map[string]string{"kernel.shmmax": "1"}
	msg := g.refusal(t, http.StatusBadRequest, "invalid_field", "services[0].sysctls.kernel.shmmax", http.MethodPost,
		"/v1/apps/"+otherID+"/deployments", labApp(other))
	if !strings.Contains(msg, "isn't namespaced") {
		t.Errorf("the refusal says %q, want it to say the sysctl isn't namespaced", msg)
	}
	if _, _, err := g.target.Nomad.Jobs().Info(otherID, nil); err == nil {
		t.Fatalf("a refused deploy registered %s", otherID)
	}

	// 2. Deploy.
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(openSearchService(host)))
	var d gateDeployment
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, d.ID, 6*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	var status int
	var body string
	var lastErr error
	if !waitUntil(time.Minute, func() bool {
		status, body, lastErr = g.hostStatus(host)
		return lastErr == nil && status == http.StatusOK && strings.Contains(body, `"cluster_name"`)
	}) {
		t.Fatalf("https://%s didn't answer OpenSearch's banner (status %d, error %v): %.300s", host, status, lastErr, body)
	}
	if !strings.Contains(body, "gate060") {
		t.Errorf("GET / names another cluster: the mounted opensearch.yml isn't the one in use:\n%.400s", body)
	}

	// 3. Inside the copy.
	var copies []string
	waitFor(t, "a running copy of "+id, time.Minute, func() bool {
		copies = g.runningCopies(t, id)
		return len(copies) == 1
	})
	// Docker can report the exit of an exec before its output has been copied (lab, observed for Run a
	// command), so an empty answer is asked again.
	var run gateRun
	var cache string
	for try := 0; try < 3 && run.Stdout == ""; try++ {
		run, _, cache = g.runIn(t, id, copies[0], "sh", "-c", "ulimit -n; ulimit -l; cat /proc/sys/net/ipv4/tcp_keepalive_time; cat /etc/gate060/key.pem; env")
	}
	if run.ExitCode == nil || *run.ExitCode != 0 {
		t.Fatalf("the command exited %v: %q", run.ExitCode, run.Stderr)
	}
	if cache != "no-store" {
		t.Errorf("Cache-Control of a run = %q, want no-store", cache)
	}
	lines := strings.Split(strings.TrimSpace(run.Stdout), "\n")
	if len(lines) < 6 || lines[0] != "65536" || lines[1] != "unlimited" || lines[2] != "600" {
		t.Fatalf("ulimit -n, ulimit -l and the sysctl = %q, want 65536, unlimited, 600:\nstdout %q\nstderr %q", lines[:min(len(lines), 3)], run.Stdout, run.Stderr)
	}
	if lines[3] != "-----BEGIN KEY-----" || lines[4] != openSearchSentinel || lines[5] != "-----END KEY-----" {
		t.Errorf("the secret file holds %q, want the secret", lines[3:6])
	}
	env := strings.Join(lines[6:], "\n")
	if strings.Contains(env, "GATE_KEY") || strings.Contains(env, openSearchSentinel) {
		t.Errorf("the secret a file holds is in the environment")
	}
	if !strings.Contains(env, "GATE_PLAIN=plain-secret-060") {
		t.Errorf("the other secret isn't in the environment:\n%s", env)
	}

	// 4. The job has names, and the export plans with no diff.
	job, _, err := g.target.Nomad.Jobs().Info(id, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", id, err)
	}
	jobJSON, _ := json.Marshal(job)
	if strings.Contains(string(jobJSON), openSearchSentinel) {
		t.Error("the Nomad job holds the secret's value")
	}
	if !strings.Contains(string(jobJSON), "secrets/caravana-files/1") {
		t.Error("the job has no secret file template")
	}
	g.exportMatchesLive(t, id, openSearchSentinel)

	// 5. No answer, log or data holds the secret (the run that read the file did, once).
	var later []string
	for _, p := range []string{"/v1/apps/" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id + "/copies",
		"/v1/apps/" + id + "/actions", "/v1/apps/" + id + "/logs", "/v1/apps/" + id + "/jobspec", "/v1/apps", "/v1/attention"} {
		later = append(later, string(g.sendWant(t, http.StatusOK, http.MethodGet, p, "")))
	}
	if g.target.ServerLog != nil {
		later = append(later, g.target.ServerLog())
	}
	if g.target.DataDir != "" {
		files, _ := filepath.Glob(filepath.Join(g.target.DataDir, "*"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil {
				later = append(later, string(b))
			}
		}
	}
	for i, text := range later {
		if strings.Contains(text, openSearchSentinel) {
			t.Fatalf("the secret file's value is in Caravana's answer, log or data (#%d)", i)
		}
	}
}
