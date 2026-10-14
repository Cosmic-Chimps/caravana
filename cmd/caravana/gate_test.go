package main

// The Phase 1 gate, as one list of steps run against a running Caravana:
//
//   - TestLab (lab_test.go) starts Caravana in-process next to the local lab.
//   - TestGate runs against a real host (deploy/vm/host.sh prove) through an
//     SSH tunnel and its public names, and adds the checks only a public host
//     can answer: a publicly trusted certificate, the HTTP redirect, and that
//     nothing but 22, 80 and 443 answers from the internet.
//
// Both are opt-in, so CI never needs a lab or a VM. Each step is a subtest,
// and the first failed step stops the rest.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// gateTarget is where the gate runs.
type gateTarget struct {
	API      string // Caravana's API, e.g. http://127.0.0.1:17070
	APIToken string // an API token of the admin: every API route but health and the webhook needs one
	Webhook  string // the webhook base URL: the webhook-only listener, or the console domain
	// WebhookOnly says Webhook serves nothing but the webhook (the lab's
	// webhook-only listener); an installed host serves it on the console domain.
	WebhookOnly bool
	// HTTP calls the API and the webhook; nil means http.DefaultClient. A host
	// on Let's Encrypt's staging CA needs one that skips the certificate check.
	HTTP    *http.Client
	AppHost string       // the app's host name, e.g. whoami.localhost
	AppURL  string       // the app's URL, e.g. https://whoami.localhost:8443/
	App     *http.Client // how to call the app (the lab's skips certificate checks)
	Nomad   *api.Client
	Public  *publicTarget // nil in the lab: the public-only steps are skipped
}

// publicTarget is what only a real host has.
type publicTarget struct {
	IP      string // the host's public IP
	Staging bool   // certificates come from Let's Encrypt's staging CA
}

// gateApp is the whoami app the gate deploys. GATE_RUN makes each run's app
// different from the last one, so a store that survived an earlier run (on a
// real host) never treats this run's first webhook as "unchanged".
const gateApp = `{
  "source": {"kind": "image"},
  "services": [{
    "name": "web",
    "image": %q,
    "env": [{"name": "GATE_RUN", "value": %q}],
    "ports": [{"name": "http", "containerPort": %d}],
    "domains": [{"host": %q, "port": "http"}],
    "health": {"path": "/", "interval": "5s", "timeout": "2s"}
  }]
}`

type gateDeployment struct {
	ID         string            `json:"id"`
	Status     string            `json:"status"`
	JobVersion *uint64           `json:"jobVersion"`
	Digests    map[string]string `json:"digests"`
	Error      string            `json:"error"`
}

// gate holds what the steps share as they go.
type gate struct {
	target gateTarget
	run    string // this run's GATE_RUN value
	token  string // whoami's webhook token
	first  gateDeployment
	live   gateDeployment // the deployment Nomad runs
}

// client is how the gate calls the API and the webhook.
func (g *gate) client() *http.Client {
	if g.target.HTTP != nil {
		return g.target.HTTP
	}
	return http.DefaultClient
}

// runGate runs every step, in order, stopping at the first failure.
func runGate(t *testing.T, target gateTarget) {
	g := &gate{target: target, run: strconv.FormatInt(time.Now().UnixNano(), 36)}
	steps := []struct {
		name       string
		publicOnly bool
		run        func(t *testing.T)
	}{
		{"api-needs-token", false, g.apiNeedsToken},
		{"clean", false, g.clean},
		{"deploy", false, g.deploy},
		{"webhook-redeploy", false, g.webhookRedeploy},
		{"webhook-unchanged", false, g.webhookUnchanged},
		{"console-reads", false, g.consoleReads},
		{"console-v2-reads", false, g.consoleV2Reads},
		{"secrets-render", false, g.secretsRender},
		{"compose-app", false, g.composeAppStep},
		{"backup-restore", false, g.backupRestoreStep},
		{"https-trusted", true, g.httpsTrusted},
		{"http-redirects", true, g.httpRedirects},
		{"webhook-host-only-webhook", false, g.webhookOnly},
		{"same-payload", false, g.samePayload},
		{"broken-reverts", false, g.brokenReverts},
		{"missing-tag", false, g.missingTag},
		{"rollback-export", false, g.rollbackExport},
		{"ports-closed", true, g.portsClosed},
	}
	for _, s := range steps {
		if s.publicOnly && target.Public == nil {
			continue
		}
		if !t.Run(s.name, s.run) {
			return // later steps build on this one
		}
	}
}

// apiNeedsToken: the API is closed (feature 006). A protected route without a
// token, or with a made-up one, is 401; the health check needs none; and the
// gate's own token works, so every later step can use it.
func (g *gate) apiNeedsToken(t *testing.T) {
	status := func(path, authorization string) int {
		req, _ := http.NewRequest(http.MethodGet, g.target.API+path, nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := g.client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := status("/v1/apps/whoami", ""); got != http.StatusUnauthorized {
		t.Errorf("GET /v1/apps/whoami without a token = %d, want 401", got)
	}
	if got := status("/v1/apps/whoami", "Bearer cvat_"+strings.Repeat("A", 43)); got != http.StatusUnauthorized {
		t.Errorf("GET /v1/apps/whoami with a made-up token = %d, want 401", got)
	}
	if got := status("/v1/health", ""); got != http.StatusOK {
		t.Errorf("GET /v1/health without a token = %d, want 200", got)
	}
	if got := status("/v1/tokens", "Bearer "+g.target.APIToken); got != http.StatusOK {
		t.Fatalf("GET /v1/tokens with the gate's token = %d, want 200 (is GATE_API_TOKEN a live token?)", got)
	}
}

// clean purges whoami from Nomad, including a hand-deployed one.
func (g *gate) clean(t *testing.T) {
	_, _, _ = g.target.Nomad.Jobs().Deregister("whoami", true, nil)
	waitFor(t, "whoami to be gone from Nomad", 2*time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info("whoami", nil)
		return err != nil
	})
}

// deploy: 202 within 10 s with a job version (feature 003, SC-001).
func (g *gate) deploy(t *testing.T) {
	start := time.Now()
	status, d, raw := g.deployApp(t, "traefik/whoami:latest", 80)
	if status != http.StatusAccepted || d.JobVersion == nil {
		t.Fatalf("deploy: %d %s", status, raw)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("deploy answered after %v, want under 10s", took)
	}
	g.first = d
}

// webhookRedeploy: while the first rollout still runs (no healthy deployment
// yet), the webhook redeploys, and its deployment wins (feature 004, FR-016).
func (g *gate) webhookRedeploy(t *testing.T) {
	resp := g.api(t, http.MethodPost, "/v1/apps/whoami/webhook", "", "")
	var created struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || created.Token == "" {
		t.Fatalf("create webhook token: %d", resp.StatusCode)
	}
	g.token = created.Token

	if code, _, _ := g.hook(t, ""); code != http.StatusUnauthorized {
		t.Errorf("webhook without a token = %d, want 401", code)
	}
	start := time.Now()
	code, changed, hooked := g.hook(t, g.token)
	if code != http.StatusAccepted || !changed {
		t.Fatalf("webhook during the first rollout = %d (changed %v), want 202 changed", code, changed)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("webhook answered after %v, want under 10s", took)
	}
	if final := g.waitFinal(t, hooked.ID, 3*time.Minute); final.Status != "healthy" {
		t.Fatalf("webhook deploy ended %s: %s", final.Status, final.Error)
	}
	if final := g.waitFinal(t, g.first.ID, time.Minute); final.Status != "superseded" {
		t.Errorf("the first API deploy ended %s, want superseded by the webhook's", final.Status)
	}
	g.live = hooked
	if err := g.callApp(); err != nil {
		t.Errorf("%s: %v", g.target.AppURL, err)
	}
	job, _, err := g.target.Nomad.Jobs().Info("whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	if image := job.TaskGroups[0].Tasks[0].Config["image"]; !strings.HasPrefix(fmt.Sprint(image), "traefik/whoami@sha256:") {
		t.Errorf("Nomad's image = %v, want a digest", image)
	}
}

// webhookUnchanged: nothing moved, so nothing is deployed.
func (g *gate) webhookUnchanged(t *testing.T) {
	before, _, _ := g.target.Nomad.Jobs().Info("whoami", nil)
	code, changed, live := g.hook(t, g.token)
	if code != http.StatusOK || changed || live.ID != g.live.ID {
		t.Errorf("second webhook = %d (changed %v, deployment %s), want 200 unchanged with %s", code, changed, live.ID, g.live.ID)
	}
	after, _, _ := g.target.Nomad.Jobs().Info("whoami", nil)
	if before != nil && after != nil && *after.Version != *before.Version {
		t.Errorf("an unchanged webhook moved the job version from %d to %d", *before.Version, *after.Version)
	}
}

// consoleReads: what the console reads from Nomad works with the token
// Caravana really runs with (feature 007): the app list has whoami, its copies
// include a healthy one, and its logs answer 200. The logs read needs the
// read-logs capability in deploy/install/caravana-policy.hcl, so a policy without
// it fails here and not in front of a user.
func (g *gate) consoleReads(t *testing.T) {
	var list struct {
		Apps []struct {
			App struct {
				ID string `json:"id"`
			} `json:"app"`
		} `json:"apps"`
	}
	g.getJSON(t, "/v1/apps", &list)
	found := false
	for _, a := range list.Apps {
		found = found || a.App.ID == "whoami"
	}
	if !found {
		t.Errorf("GET /v1/apps doesn't list whoami: %+v", list.Apps)
	}

	var copies struct {
		Copies []struct {
			Status string `json:"status"`
		} `json:"copies"`
	}
	g.getJSON(t, "/v1/apps/whoami/copies", &copies)
	healthy := false
	for _, c := range copies.Copies {
		healthy = healthy || c.Status == "healthy"
	}
	if !healthy {
		t.Errorf("GET /v1/apps/whoami/copies has no healthy copy: %+v", copies.Copies)
	}

	resp := g.api(t, http.MethodGet, "/v1/apps/whoami/logs?lines=10", "", "")
	defer resp.Body.Close()
	if raw, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/apps/whoami/logs?lines=10 = %d, want 200 (does Caravana's Nomad policy have read-logs?): %s", resp.StatusCode, raw)
	}
}

// consoleV2Reads: the reads Console v2 added, with the token Caravana's
// policy gives a client. A running copy has usage with no error, which fails
// when the policy lacks what Nomad's allocation stats need (a missing
// capability shows only on a real host), and the needs-you list answers 200.
func (g *gate) consoleV2Reads(t *testing.T) {
	var copies struct {
		Copies []struct {
			Status string `json:"status"`
			Usage  *struct {
				MemoryMB *int   `json:"memoryMB"`
				Error    string `json:"error"`
			} `json:"usage"`
		} `json:"copies"`
	}
	g.getJSON(t, "/v1/apps/whoami/copies?usage=true", &copies)
	checked := false
	for _, c := range copies.Copies {
		if c.Status != "healthy" {
			continue
		}
		checked = true
		if c.Usage == nil || c.Usage.Error != "" || c.Usage.MemoryMB == nil {
			t.Errorf("GET /v1/apps/whoami/copies?usage=true: a healthy copy has usage %+v, want numbers without error (does Caravana's Nomad policy allow reading allocation stats?)", c.Usage)
		}
	}
	if !checked {
		t.Errorf("GET /v1/apps/whoami/copies?usage=true has no healthy copy: %+v", copies.Copies)
	}

	resp := g.api(t, http.MethodGet, "/v1/attention", "", "")
	defer resp.Body.Close()
	if raw, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/attention = %d, want 200: %s", resp.StatusCode, raw)
	}
}

// secretsApp is a busybox worker that only sleeps, with one secret. busybox,
// because whoami has no shell to print its environment with.
const secretsApp = `{
  "source": {"kind": "image"},
  "services": [{
    "name": "worker",
    "image": "busybox:1.37",
    "command": "sh",
    "args": ["-c", "exec sleep 86400"],
    "secrets": [{"name": "GATE_SECRET", "value": %s}]
  }]
}`

// secretsRender: a secret reaches the container byte for byte, through the
// Nomad Variables store and the env template, with the token Caravana really
// runs with (feature 008). A redeploy without a body keeps it, and deleting
// the app removes it from Nomad. The store needs the variables rule in
// deploy/install/caravana-policy.hcl, so a policy without it fails here.
func (g *gate) secretsRender(t *testing.T) {
	const appID = "gate-secrets"
	value := "line one\nsay \"hi\" = $HOME {{ .x }} é \\ " + g.run
	quoted, _ := json.Marshal(value)
	resp := g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "") // a leftover from an aborted run, if any
	resp.Body.Close()

	deployAndRead := func(body string) {
		t.Helper()
		resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", "application/json", body)
		var d gateDeployment
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil {
			t.Fatalf("deploy %s: %d %s", appID, resp.StatusCode, raw)
		}
		final := g.waitFinalOf(t, appID, d.ID, 3*time.Minute)
		if final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
		}
		if got := g.execEnv(t, appID, "worker", "GATE_SECRET", d.ID); got != value {
			t.Fatalf("the container sees GATE_SECRET = %q, want %q", got, value)
		}
	}
	deployAndRead(fmt.Sprintf(secretsApp, quoted))
	deployAndRead("")                                    // a redeploy keeps the value
	g.exportMatchesLive(t, appID, value, string(quoted)) // feature 011: names it, never its value

	var rec struct {
		App struct {
			Services []struct {
				Secrets []map[string]any `json:"secrets"`
			} `json:"services"`
		} `json:"app"`
	}
	g.getJSON(t, "/v1/apps/"+appID, &rec)
	if s := rec.App.Services[0].Secrets; len(s) != 1 || s[0]["name"] != "GATE_SECRET" || s[0]["value"] != nil {
		t.Errorf("GET /v1/apps/%s secrets = %v, want the name only", appID, s)
	}

	resp = g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete %s: %d", appID, resp.StatusCode)
	}
	left, _, err := g.target.Nomad.Variables().PrefixList("nomad/jobs/"+appID+"/", nil)
	if err != nil || len(left) != 0 {
		t.Errorf("variables left after delete: %d (%v)", len(left), err)
	}
}

// execEnv prints one env var inside a running copy of appID's service that
// belongs to the deployment's job version, with Nomad's exec (the gate's
// token can; Caravana's can't).
func (g *gate) execEnv(t *testing.T, appID, task, name, deploymentID string) string {
	t.Helper()
	out, err := g.exec(t, appID, task, deploymentID, "printenv", name)
	if err != nil {
		t.Fatalf("exec printenv %s: %v", name, err)
	}
	return out
}

// exec runs a command inside a running copy of appID's service (its task
// group and task share the service's name) that belongs to the deployment's
// job version, with Nomad's exec (the gate's token can; Caravana's can't).
// It answers the trimmed output, or an error with the exit code and stderr.
func (g *gate) exec(t *testing.T, appID, service, deploymentID string, cmd ...string) (string, error) {
	t.Helper()
	var alloc *api.Allocation
	waitFor(t, "a running copy of "+appID+"/"+service, time.Minute, func() bool {
		stubs, _, err := g.target.Nomad.Jobs().Allocations(appID, false, nil)
		if err != nil {
			return false
		}
		job, _, err := g.target.Nomad.Jobs().Info(appID, nil)
		if err != nil || job.Meta["caravana.deployment"] != deploymentID {
			return false
		}
		for _, s := range stubs {
			if s.ClientStatus == "running" && s.JobVersion == *job.Version && s.TaskGroup == service {
				alloc, _, err = g.target.Nomad.Allocations().Info(s.ID, nil)
				return err == nil
			}
		}
		return false
	})
	var out, errOut strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := g.target.Nomad.Allocations().Exec(ctx, alloc, service, false, cmd,
		strings.NewReader(""), &out, &errOut, nil, nil)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimSuffix(out.String(), "\n"), err
}

// gateS3Job is the S3-compatible bucket the backup step writes to: rclone
// serving a directory on the node, on static port 9000, run with the gate's
// own token (it isn't an app; Caravana never sees it as one). Only the
// node's address reaches it, and the step purges it at the end.
func gateS3Job(secret string) *api.Job {
	return &api.Job{
		ID: new("gate-s3"), Name: new("gate-s3"), Type: new("service"), Datacenters: []string{"*"},
		TaskGroups: []*api.TaskGroup{{
			Name:     new("s3"),
			Networks: []*api.NetworkResource{{Mode: "bridge", ReservedPorts: []api.Port{{Label: "s3", Value: 9000, To: 9000}}}},
			Tasks: []*api.Task{{
				Name: "s3", Driver: "docker",
				Config: map[string]any{
					// The image Caravana's backup jobs use, so the node pulls it once.
					"image":      nomadjob.DefaultBackupImage,
					"entrypoint": []string{"/bin/sh", "-c"},
					"args":       []string{"mkdir -p /data/backups && exec rclone serve s3 /data --addr :9000 --auth-key GATEKEY," + secret},
				},
				Resources: &api.Resources{CPU: new(100), MemoryMB: new(128)},
			}},
		}},
	}
}

// gateDBCompose is a Compose file with one backed-up PostgreSQL service on
// a volume. The password is plain env: it is a throwaway database.
const gateDBCompose = `services:
  db:
    image: postgres:17
    ports: ["5432"]
    environment:
      POSTGRES_PASSWORD: gate-password
    volumes: ["db-data:/var/lib/postgresql/data"]
volumes:
  db-data: {}
`

// backupRestoreStep: the Phase 2 gate's "a backup that restores" (feature
// 010), with the token Caravana really runs with. Row A is written, backed up
// to the bucket, changed to B, and the backup restored: A is read back.
func (g *gate) backupRestoreStep(t *testing.T) {
	const appID = "gate-db"
	nomad := g.target.Nomad
	resp := g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "") // a leftover from an aborted run, if any
	resp.Body.Close()

	// The bucket.
	secret := "gate-" + g.run
	if _, _, err := nomad.Jobs().Register(gateS3Job(secret), nil); err != nil {
		t.Fatalf("register the S3 bucket job: %v", err)
	}
	t.Cleanup(func() { _, _, _ = nomad.Jobs().Deregister("gate-s3", true, nil) })
	nodes, _, err := nomad.Nodes().List(nil)
	if err != nil || len(nodes) == 0 {
		t.Fatalf("list nodes: %v", err)
	}
	node, _, err := nomad.Nodes().Info(nodes[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodeIP := node.Attributes["unique.network.ip-address"]
	waitFor(t, "the S3 bucket to listen", 2*time.Minute, func() bool {
		allocs, _, err := nomad.Jobs().Allocations("gate-s3", false, nil)
		if err != nil {
			return false
		}
		for _, a := range allocs {
			if a.ClientStatus == "running" {
				full, _, err := nomad.Allocations().Info(a.ID, nil)
				if err != nil {
					return false
				}
				code, err := nomad.Allocations().Exec(context.Background(), full, "s3", false, []string{"nc", "-z", "127.0.0.1", "9000"},
					strings.NewReader(""), io.Discard, io.Discard, nil, nil)
				return err == nil && code == 0
			}
		}
		return false
	})

	dest, _ := json.Marshal(map[string]string{"endpoint": "http://" + nodeIP + ":9000", "region": "us-east-1", "bucket": "backups",
		"prefix": "gate", "accessKeyId": "GATEKEY", "secretAccessKey": secret})
	resp = g.api(t, http.MethodPut, "/v1/backups/destination", "application/json", string(dest))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), secret) {
		t.Fatalf("PUT destination: %d %s", resp.StatusCode, raw)
	}

	// The database.
	body, _ := json.Marshal(map[string]any{"compose": gateDBCompose,
		"services": map[string]any{"db": map[string]any{"backup": map[string]any{"engine": "postgres", "keep": 2}}}})
	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/compose", "application/json", string(body))
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var d gateDeployment
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil {
		t.Fatalf("deploy %s: %d %s", appID, resp.StatusCode, raw)
	}
	if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}
	psql := func(sql string) string {
		t.Helper()
		var out string
		var lastErr error
		if !waitUntil(time.Minute, func() bool {
			out, lastErr = g.exec(t, appID, "db", d.ID, "psql", "-U", "postgres", "-tAc", sql)
			return lastErr == nil
		}) {
			t.Fatalf("psql %q: %v", sql, lastErr)
		}
		return strings.TrimSpace(out)
	}
	psql("create table gate(v text); insert into gate values ('A')")

	// Back up, change, restore.
	type listing struct {
		Backups []struct {
			ID, Status, Message string
		} `json:"backups"`
		Restores []struct {
			Status, Message string
		} `json:"restores"`
	}
	list := func() listing {
		t.Helper()
		var l listing
		g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
		return l
	}
	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/backups", "application/json", `{"service": "db"}`)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("back up now: %d %s", resp.StatusCode, raw)
	}
	var backupID string
	waitFor(t, "the backup to succeed", 3*time.Minute, func() bool {
		for _, b := range list().Backups {
			switch b.Status {
			case "succeeded":
				backupID = b.ID
				return true
			case "failed":
				t.Fatalf("the backup failed: %s", b.Message)
			}
		}
		return false
	})

	psql("update gate set v = 'B'")
	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/backups/"+backupID+"/restore", "application/json", "")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("restore: %d %s", resp.StatusCode, raw)
	}
	waitFor(t, "the restore to succeed", 3*time.Minute, func() bool {
		r := list().Restores
		if len(r) > 0 && r[0].Status == "failed" {
			t.Fatalf("the restore failed: %s", r[0].Message)
		}
		return len(r) > 0 && r[0].Status == "succeeded"
	})
	if got := psql("select v from gate"); got != "A" {
		t.Fatalf("after the restore the row is %q, want A", got)
	}

	resp = g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete %s: %d", appID, resp.StatusCode)
	}
	if left, _, err := nomad.Jobs().List(&api.QueryOptions{Prefix: appID + "_"}); err != nil || len(left) != 0 {
		t.Errorf("backup jobs left after delete: %d (%v)", len(left), err)
	}
}

// composeApp is a two-service Compose file (feature 009). store writes a
// random value into its volume on its first start only and serves it on
// 8080; web only sleeps. "$$" is Compose's escape for a literal "$".
const composeApp = `services:
  store:
    image: busybox:1.37
    command: ["sh", "-c", "mkdir -p /data/www; [ -f /data/www/index.html ] || cat /proc/sys/kernel/random/uuid > /data/www/index.html; exec httpd -f -p 8080 -h /data/www"]
    ports: ["8080"]
    volumes: ["store-data:/data"]
  web:
    image: busybox:1.37
    command: ["sh", "-c", "trap 'exit 0' TERM; while :; do sleep 3600 & wait $$!; done"]
    depends_on: [store]
volumes:
  store-data: {}
`

// composeAppStep: a Compose app deploys (feature 009) with the token Caravana
// really runs with: web reaches store by its Compose name on its container
// port, a redeploy keeps store's volume data and web still reaches it by
// name, and deleting the app deletes the volume. The volume needs the
// host-volume rules in deploy/install/caravana-policy.hcl, so a policy without
// them fails here.
func (g *gate) composeAppStep(t *testing.T) {
	const appID = "gate-compose"
	resp := g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "") // a leftover from an aborted run, if any
	resp.Body.Close()

	body, _ := json.Marshal(map[string]string{"compose": composeApp})
	deploy := func(path, body string) gateDeployment {
		t.Helper()
		resp := g.api(t, http.MethodPost, path, "application/json", body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var d gateDeployment
		if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil {
			t.Fatalf("POST %s: %d %s", path, resp.StatusCode, raw)
		}
		if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
		}
		return d
	}
	// readByName fetches store's page from inside web, by name, retrying while
	// web's names catch up with a new copy of store (within seconds).
	readByName := func(d gateDeployment) string {
		t.Helper()
		var got string
		var lastErr error
		if !waitUntil(time.Minute, func() bool {
			got, lastErr = g.exec(t, appID, "web", d.ID, "wget", "-qO-", "-T", "5", "http://store:8080/")
			return lastErr == nil && got != ""
		}) {
			t.Fatalf("web can't reach http://store:8080/ by name: %v", lastErr)
		}
		return got
	}

	first := deploy("/v1/apps/"+appID+"/compose", string(body))
	before := readByName(first)

	again := deploy("/v1/apps/"+appID+"/deployments", "") // stops store, then starts it on the same volume
	if after := readByName(again); after != before {
		t.Errorf("after a redeploy store serves %q, want %q: its volume lost its data, or web reaches the old copy", after, before)
	}
	// Feature 011: a multi-service export (volume, names sidecar) is the job Nomad runs.
	g.exportMatchesLive(t, appID)

	resp = g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete %s: %d", appID, resp.StatusCode)
	}
	left, _, err := g.target.Nomad.HostVolumes().List(&api.HostVolumeListRequest{}, &api.QueryOptions{Prefix: appID + "_"})
	if err != nil || len(left) != 0 {
		t.Errorf("host volumes left after delete: %d (%v)", len(left), err)
	}
}

// getJSON reads a JSON answer of the API; anything but 200 fails the step.
func (g *gate) getJSON(t *testing.T, path string, into any) {
	t.Helper()
	resp := g.api(t, http.MethodGet, path, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s = %d, want 200: %s", path, resp.StatusCode, raw)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

// httpsTrusted: the app's certificate is publicly trusted (SC-002).
func (g *gate) httpsTrusted(t *testing.T) {
	client := &http.Client{Timeout: 10 * time.Second} // the system's roots, nothing else
	var lastErr error
	ok := waitUntil(2*time.Minute, func() bool { // the first certificate may still be on its way
		resp, err := client.Get("https://" + g.target.AppHost + "/")
		if err != nil {
			lastErr = err
			return false
		}
		resp.Body.Close()
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		return resp.StatusCode == http.StatusOK
	})
	if !ok {
		if g.target.Public.Staging {
			t.Fatalf("staging certificate (CARAVANA_ACME_STAGING=1) isn't publicly trusted; set it to 0 to prove the gate: %v", lastErr)
		}
		t.Fatalf("https://%s with a verified certificate: %v", g.target.AppHost, lastErr)
	}
}

// httpRedirects: plain HTTP goes to HTTPS.
func (g *gate) httpRedirects(t *testing.T) {
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get("http://" + g.target.AppHost + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || !strings.HasPrefix(loc, "https://"+g.target.AppHost) {
		t.Errorf("http://%s = %d → %q, want a redirect to https", g.target.AppHost, resp.StatusCode, loc)
	}
}

// webhookOnly: the webhook base serves nothing but the webhook. Only a
// webhook-only listener does; an installed host serves the webhook on the
// console domain, next to everything else (feature 012).
func (g *gate) webhookOnly(t *testing.T) {
	if !g.target.WebhookOnly {
		t.Skip("the webhook is served on the console domain, not by a webhook-only listener")
	}
	for _, path := range []string{"/v1/health", "/v1/apps/whoami", "/"} {
		resp, err := g.client().Get(g.target.Webhook + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s on the webhook base = %d, want 404", path, resp.StatusCode)
		}
	}
	resp, err := g.client().Post(g.target.Webhook+"/v1/webhooks/no-such-app", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /v1/webhooks/no-such-app = %d, want 401", resp.StatusCode)
	}
}

// samePayload: the same app again is a new record and job version (SC-003).
func (g *gate) samePayload(t *testing.T) {
	status, d, raw := g.deployApp(t, "traefik/whoami:latest", 80)
	if status != http.StatusAccepted || d.ID == g.live.ID || *d.JobVersion <= *g.live.JobVersion {
		t.Fatalf("same payload again: %d %s", status, raw)
	}
	if final := g.waitFinal(t, d.ID, 3*time.Minute); final.Status != "healthy" {
		t.Fatalf("it ended %s: %s", final.Status, final.Error)
	}
	g.live = d
}

// brokenReverts: a health check that can't pass ends reverted, the app keeps
// answering every request, and Nomad runs the last healthy deployment again
// (SC-004 of feature 003; SC-003 here).
func (g *gate) brokenReverts(t *testing.T) {
	var calls, failures atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(500 * time.Millisecond):
			}
			calls.Add(1)
			if g.callApp() != nil {
				failures.Add(1)
			}
		}
	}()
	status, broken, raw := g.deployApp(t, "traefik/whoami:latest", 81)
	if status != http.StatusAccepted {
		close(stop)
		t.Fatalf("broken deploy: %d %s", status, raw)
	}
	// Nomad fails it at the progress deadline (10 min, set by the translator).
	final := g.waitFinal(t, broken.ID, 12*time.Minute)
	time.Sleep(5 * time.Second) // keep calling a little after the revert
	close(stop)
	<-done
	if final.Status != "reverted" || !strings.Contains(final.Error, "rolling back to job version") {
		t.Errorf("broken deploy ended %s (%q), want reverted with Nomad's rollback description", final.Status, final.Error)
	}
	if failures.Load() != 0 {
		t.Errorf("%d of %d calls failed during the broken rollout", failures.Load(), calls.Load())
	}
	job, _, err := g.target.Nomad.Jobs().Info("whoami", nil)
	if err != nil || job.Meta["caravana.deployment"] != g.live.ID {
		t.Errorf("after the revert Nomad runs deployment %q (%v), want the last healthy one %q", job.Meta["caravana.deployment"], err, g.live.ID)
	}
}

// missingTag: fails with 422 before anything is registered (SC-005 of 003).
func (g *gate) missingTag(t *testing.T) {
	before, _, _ := g.target.Nomad.Jobs().Info("whoami", nil)
	if status, _, raw := g.deployApp(t, "traefik/whoami:does-not-exist", 80); status != http.StatusUnprocessableEntity {
		t.Errorf("missing tag: %d %s, want 422", status, raw)
	}
	after, _, _ := g.target.Nomad.Jobs().Info("whoami", nil)
	if before != nil && after != nil && *before.Version != *after.Version {
		t.Errorf("job version moved from %d to %d", *before.Version, *after.Version)
	}
}

// rollbackExport (feature 011): an earlier healthy deployment of whoami is
// deployed again with its pinned digest and named as the source; one that
// never ran properly is refused; and the live version's export is the job
// Nomad runs.
func (g *gate) rollbackExport(t *testing.T) {
	var list struct {
		Deployments []struct {
			gateDeployment
			Source             string `json:"source"`
			DefinitionRecorded bool   `json:"definitionRecorded"`
		} `json:"deployments"`
	}
	g.getJSON(t, "/v1/apps/whoami/deployments?limit=100", &list)
	var past, notHealthy string
	var pastDigest string
	for _, d := range list.Deployments {
		switch {
		case d.Status == "healthy" && d.ID != g.live.ID && d.DefinitionRecorded && past == "":
			past, pastDigest = d.ID, d.Digests["web"]
		case d.Status == "reverted" && notHealthy == "":
			notHealthy = d.ID
		}
	}
	if past == "" || notHealthy == "" {
		t.Fatalf("no earlier healthy deployment (%q) or reverted one (%q) to work with", past, notHealthy)
	}

	resp := g.api(t, http.MethodPost, "/v1/apps/whoami/deployments/"+notHealthy+"/redeploy", "", "")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "deployment_not_healthy") {
		t.Errorf("redeploy of the reverted deployment = %d %s, want 409 deployment_not_healthy", resp.StatusCode, raw)
	}

	resp = g.api(t, http.MethodPost, "/v1/apps/whoami/deployments/"+past+"/redeploy", "", "")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var d struct {
		gateDeployment
		Source string `json:"source"`
	}
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil || d.Source != past || d.Digests["web"] != pastDigest {
		t.Fatalf("redeploy of %s = %d %s, want 202 naming it with its digest", past, resp.StatusCode, raw)
	}
	if final := g.waitFinal(t, d.ID, 3*time.Minute); final.Status != "healthy" {
		t.Fatalf("the redeploy ended %s: %s", final.Status, final.Error)
	}
	g.live = d.gateDeployment
	job, _, err := g.target.Nomad.Jobs().Info("whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	if job.Meta["caravana.deployment"] != d.ID || job.TaskGroups[0].Tasks[0].Config["image"] != pastDigest {
		t.Errorf("Nomad runs deployment %s with %v, want %s with %s",
			job.Meta["caravana.deployment"], job.TaskGroups[0].Tasks[0].Config["image"], d.ID, pastDigest)
	}

	g.exportMatchesLive(t, "whoami")
}

// exportMatchesLive exports appID's live version, has Nomad parse the HCL,
// and plans it against the running job: the plan must change nothing
// (FR-013). The file must hold neither the gate's API token nor any of
// secrets, values the gate set as secrets.
func (g *gate) exportMatchesLive(t *testing.T, appID string, secrets ...string) {
	t.Helper()
	resp := g.api(t, http.MethodGet, "/v1/apps/"+appID+"/jobspec", "", "")
	hcl, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("export %s = %d %s %s", appID, resp.StatusCode, resp.Header.Get("Content-Type"), hcl)
	}
	for _, never := range append(secrets, g.target.APIToken) {
		if strings.Contains(string(hcl), never) {
			t.Fatalf("the export of %s holds a token or a secret value", appID)
		}
	}
	job, err := g.target.Nomad.Jobs().ParseHCLOpts(&api.JobsParseRequest{JobHCL: string(hcl), Canonicalize: true})
	if err != nil {
		t.Fatalf("Nomad can't parse the export of %s: %v\n%s", appID, err, hcl)
	}
	plan, _, err := g.target.Nomad.Jobs().Plan(job, true, nil)
	if err != nil {
		t.Fatalf("plan the export of %s: %v", appID, err)
	}
	if plan.Diff == nil || plan.Diff.Type != "None" {
		diff, _ := json.MarshalIndent(plan.Diff, "", "  ")
		t.Errorf("planning the export of %s against the live job changes it:\n%s", appID, diff)
	}
}

// portsClosed: from the internet only 22, 80 and 443 answer; Nomad, Caravana,
// the dashboard and whoami's own mapped port don't (SC-004, FR-005a).
func (g *gate) portsClosed(t *testing.T) {
	ip := g.target.Public.IP
	for _, port := range []int{22, 80, 443} {
		if !portOpen(ip, port) {
			t.Errorf("port %d is closed, want open", port)
		}
	}
	closed := []int{4646, 4647, 4648, 7070, 7071, 8081}
	allocs, _, err := g.target.Nomad.Jobs().Allocations("whoami", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, stub := range allocs {
		if stub.ClientStatus != api.AllocClientStatusRunning {
			continue
		}
		alloc, _, err := g.target.Nomad.Allocations().Info(stub.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if alloc.AllocatedResources != nil {
			for _, p := range alloc.AllocatedResources.Shared.Ports {
				closed = append(closed, p.Value)
			}
		}
	}
	if len(closed) == 6 {
		t.Error("found no port for whoami's running allocations")
	}
	for _, port := range closed {
		if portOpen(ip, port) {
			t.Errorf("port %d answers from the internet, want closed", port)
		}
	}
}

// deployApp posts the gate app to the API.
func (g *gate) deployApp(t *testing.T, image string, port int) (int, gateDeployment, []byte) {
	t.Helper()
	body := fmt.Sprintf(gateApp, image, g.run, port, g.target.AppHost)
	resp := g.api(t, http.MethodPost, "/v1/apps/whoami/deployments", "application/json; charset=UTF-8", body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var d gateDeployment
	_ = json.Unmarshal(raw, &d)
	return resp.StatusCode, d, raw
}

// apiRequest calls Caravana's API with the gate's API token. Every API call
// goes through here; the webhook calls (hook, webhookOnly) carry their own
// token instead and don't.
func (g *gate) apiRequest(method, path, contentType, body string) (*http.Response, error) {
	req, err := http.NewRequest(method, g.target.API+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+g.target.APIToken)
	return g.client().Do(req)
}

// api is apiRequest that fails the test when the request can't be sent.
func (g *gate) api(t *testing.T, method, path, contentType, body string) *http.Response {
	t.Helper()
	resp, err := g.apiRequest(method, path, contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// hook calls the webhook for whoami with token in the Authorization header
// (none when empty).
func (g *gate) hook(t *testing.T, token string) (int, bool, gateDeployment) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, g.target.Webhook+"/v1/webhooks/whoami",
		strings.NewReader(`{"push_data":{"tag":"latest"}}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Changed    bool
		Deployment gateDeployment
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Changed, out.Deployment
}

// waitFinal polls a deployment of whoami until it has a final status.
func (g *gate) waitFinal(t *testing.T, id string, within time.Duration) gateDeployment {
	t.Helper()
	return g.waitFinalOf(t, "whoami", id, within)
}

// waitFinalOf polls a deployment of appID until it has a final status.
func (g *gate) waitFinalOf(t *testing.T, appID, id string, within time.Duration) gateDeployment {
	t.Helper()
	var d gateDeployment
	if !waitUntil(within, func() bool {
		resp, err := g.apiRequest(http.MethodGet, "/v1/apps/"+appID+"/deployments/"+id, "", "")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		d = gateDeployment{}
		_ = json.NewDecoder(resp.Body).Decode(&d)
		switch d.Status {
		case "healthy", "failed", "reverted", "superseded":
			return true
		}
		return false
	}) {
		t.Fatalf("deployment %s not final after %v (last status %q)", id, within, d.Status)
	}
	return d
}

// callApp calls the app once; nil means 200.
func (g *gate) callApp() error {
	resp, err := g.target.App.Get(g.target.AppURL)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// waitUntil polls cond every second until it's true or within passes.
func waitUntil(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	if !waitUntil(within, cond) {
		t.Fatalf("timed out after %v waiting for %s", within, what)
	}
}

// portOpen reports whether a TCP connection to ip:port succeeds within 3 s.
// A refusal or a timeout (a firewall dropping it) both count as closed.
func portOpen(ip string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// TestGate proves the Phase 1 gate on a real host. deploy/vm/host.sh prove
// sets everything up; see specs/005-vm-https-proof/contracts/host-sh.md.
func TestGate(t *testing.T) {
	if os.Getenv("CARAVANA_GATE") != "1" {
		t.Skip("set CARAVANA_GATE=1 (deploy/vm/host.sh prove does) to prove the gate on a real host")
	}
	need := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			t.Fatalf("%s is empty", name)
		}
		return v
	}
	nomad, err := deploy.NewNomadClient(need("NOMAD_ADDR"), need("NOMAD_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	host := need("GATE_APP_HOST")
	staging := os.Getenv("GATE_ACME_STAGING") == "1"
	app := &http.Client{Timeout: 5 * time.Second} // the system's roots
	var console *http.Client                      // the API and the webhook, on the console domain
	if staging {
		// Staging certificates aren't publicly trusted, so the steps that only
		// need the app to answer skip the check; https-trusted still fails
		// and says why.
		app.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // staging CA only
		console = &http.Client{Transport: app.Transport}
	}
	runGate(t, gateTarget{
		API:      need("GATE_API"),
		APIToken: need("GATE_API_TOKEN"),
		Webhook:  need("GATE_WEBHOOK"),
		AppHost:  host,
		AppURL:   "https://" + host + "/",
		App:      app,
		HTTP:     console,
		Nomad:    nomad,
		Public:   &publicTarget{IP: need("GATE_PUBLIC_IP"), Staging: staging},
	})
}
