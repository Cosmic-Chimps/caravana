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
	// RestartNomad restarts the Nomad agent and returns once it answers again;
	// only the lab sets it. The volume step uses it to back up a copy that
	// started before the agent's last restart (a host after an installer rerun).
	RestartNomad func(t *testing.T)
	// RestartCaravana stops Caravana and starts it again on the same data
	// directory, returning once it is healthy; only the lab sets it (the
	// schedules step restarts it during a run).
	RestartCaravana func(t *testing.T)
	// ServerLog is everything Caravana has logged so far, and DataDir its data
	// directory; only the lab has them, for the checks that nothing secret is
	// logged or stored.
	ServerLog func() string
	DataDir   string
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
		{"volume-backup-restore", false, g.volumeBackupRestoreStep},
		{"https-trusted", true, g.httpsTrusted},
		{"http-redirects", true, g.httpRedirects},
		{"webhook-host-only-webhook", false, g.webhookOnly},
		{"same-payload", false, g.samePayload},
		{"broken-reverts", false, g.brokenReverts},
		{"missing-tag", false, g.missingTag},
		{"rollback-export", false, g.rollbackExport},
		{"projects-promote", false, g.projectsPromoteStep},
		{"app-actions", false, g.appActionsStep},
		{"schedules", false, g.schedulesStep},
		{"terminal", false, g.terminalStep},
		{"deploy-from-ci", false, g.deployFromCIStep},
		{"private-registry", false, g.privateRegistryStep},
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
//
// Nomad's exec stream sometimes reports a command's exit before its output
// has arrived, so a run that exits 0 with no output is run again, up to three
// times in all, a second apart (seen in the lab: printenv answering "" for a
// variable that was set). Every command the gate runs this way may run twice:
// they read, or write the same thing again.
func (g *gate) exec(t *testing.T, appID, service, deploymentID string, cmd ...string) (string, error) {
	t.Helper()
	var out string
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(time.Second)
		}
		if out, err = g.execOnce(t, appID, service, deploymentID, cmd...); err != nil || out != "" {
			break
		}
	}
	return out, err
}

// execOnce is one run of exec's command.
func (g *gate) execOnce(t *testing.T, appID, service, deploymentID string, cmd ...string) (string, error) {
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

// startBucket runs gateS3Job, waits until it listens, and points the backup
// destination at it. The job is deregistered when the step ends, so each step
// that needs a bucket starts its own (its contents are gone with it).
func (g *gate) startBucket(t *testing.T) {
	t.Helper()
	nomad := g.target.Nomad
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
		_, err := g.bucketExec("nc", "-z", "127.0.0.1", "9000")
		return err == nil
	})

	dest, _ := json.Marshal(map[string]string{"endpoint": "http://" + nodeIP + ":9000", "region": "us-east-1", "bucket": "backups",
		"prefix": "gate", "accessKeyId": "GATEKEY", "secretAccessKey": secret})
	resp := g.api(t, http.MethodPut, "/v1/backups/destination", "application/json", string(dest))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), secret) {
		t.Fatalf("PUT destination: %d %s", resp.StatusCode, raw)
	}
}

// bucketExec runs a command inside the bucket job's task and answers its
// trimmed output. Only a copy Nomad still wants running counts: an earlier
// step's bucket may still be stopping when the next step starts its own.
func (g *gate) bucketExec(cmd ...string) (string, error) {
	return g.jobExec("gate-s3", "s3", cmd...)
}

// jobExec runs a command inside the task of a job the gate itself started (not
// an app), in a copy Nomad still wants running, and answers its trimmed
// output. An exit code other than 0 is an error holding the code and stderr.
func (g *gate) jobExec(jobID, task string, cmd ...string) (string, error) {
	nomad := g.target.Nomad
	allocs, _, err := nomad.Jobs().Allocations(jobID, false, nil)
	if err != nil {
		return "", err
	}
	for _, a := range allocs {
		if a.ClientStatus != "running" || a.DesiredStatus != "run" {
			continue
		}
		full, _, err := nomad.Allocations().Info(a.ID, nil)
		if err != nil {
			return "", err
		}
		var out, errOut strings.Builder
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		code, err := nomad.Allocations().Exec(ctx, full, task, false, cmd, strings.NewReader(""), &out, &errOut, nil, nil)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(errOut.String()))
		}
		return strings.TrimSpace(out.String()), err
	}
	return "", fmt.Errorf("no running copy of job %s", jobID)
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

	g.startBucket(t)

	// The database.
	body, _ := json.Marshal(map[string]any{"compose": gateDBCompose,
		"services": map[string]any{"db": map[string]any{"backup": map[string]any{"engine": "postgres", "keep": 2}}}})
	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/compose", "application/json", string(body))
	raw, _ := io.ReadAll(resp.Body)
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
	// Safe to run twice: psql is retried until the database answers, and an
	// exec whose output went missing is asked again, so this may have run once
	// already ("relation gate already exists", seen in the lab 2026-10-19).
	psql("create table if not exists gate(v text); delete from gate; insert into gate values ('A')")

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

// volumeApp is the app of the volume backup step (feature 016): busybox with
// one volume, data, at /data. Its command writes "A" into /data/v only when
// the file is missing, then serves /data over HTTP on 8080, so the health
// check (GET /v) passes once the file is there and the step reads the file
// from inside the copy.
const volumeApp = `{
  "source": {"kind": "image"},
  "services": [{
    "name": "web",
    "image": "busybox:1.37",
    "command": "sh",
    "args": ["-c", "[ -f /data/v ] || printf A > /data/v; exec httpd -f -p 8080 -h /data"],
    "ports": [{"name": "http", "containerPort": 8080}],
    "health": {"path": "/v", "interval": "5s", "timeout": "2s"},
    "volumes": [{"name": "data", "path": "/data"}],
    "backup": {"engine": "volume", "keep": 2}
  }]
}`

// gateVolumeRun is the part of a Backup (schema Backup) the volume step reads.
type gateVolumeRun struct {
	ID             string   `json:"id"`
	Engine         string   `json:"engine"`
	Status         string   `json:"status"`
	Message        string   `json:"message"`
	Bytes          int64    `json:"bytes"`
	StoppedSeconds *float64 `json:"stoppedSeconds"`
	Volumes        []struct {
		Name      string `json:"name"`
		Bytes     int64  `json:"bytes"`
		SizeBytes int64  `json:"sizeBytes"`
	} `json:"volumes"`
}

// gateVolumeRestore is the part of a Restore (schema Restore) the volume step reads.
type gateVolumeRestore struct {
	ID             string   `json:"id"`
	BackupID       string   `json:"backupId"`
	Engine         string   `json:"engine"`
	Status         string   `json:"status"`
	Message        string   `json:"message"`
	StoppedSeconds *float64 `json:"stoppedSeconds"`
}

// volumeBackupRestoreStep: a volume backup and its restore (feature 016), with
// the token Caravana really runs with. The app's own copy is restarted to run
// each, so the step checks that the job is untouched (the same version, the
// same copy), that the service was stopped for a measured time, and that the
// archive is in the bucket. Then it changes the data, restores the backup and
// checks that the old data is back and what was added since is gone.
func (g *gate) volumeBackupRestoreStep(t *testing.T) {
	const appID = "vol"
	nomad := g.target.Nomad
	resp := g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "") // a leftover from an aborted run, if any
	resp.Body.Close()

	g.startBucket(t)

	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", "application/json", volumeApp)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var d gateDeployment
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil {
		t.Fatalf("deploy %s: %d %s", appID, resp.StatusCode, raw)
	}
	if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}
	// read runs a command that only reads, inside the copy of this deployment,
	// and returns its output. Nomad's exec stream can report the exit before the
	// output has arrived, so an empty answer is asked again (a few times, over a
	// few seconds); every read here prints something when it works. It waits
	// while the service is still starting.
	read := func(cmd ...string) string {
		t.Helper()
		var got string
		var lastErr error
		if !waitUntil(time.Minute, func() bool {
			got, lastErr = g.exec(t, appID, "web", d.ID, cmd...)
			return lastErr == nil && got != ""
		}) {
			t.Fatalf("%s: %q (%v)", strings.Join(cmd, " "), got, lastErr)
		}
		return got
	}
	// readFile reads /data/v.
	readFile := func() string {
		t.Helper()
		return read("cat", "/data/v")
	}
	if got := readFile(); got != "A" {
		t.Fatalf("/data/v = %q at the start, want A", got)
	}

	// The job and its copy, as they are before the backup.
	versionOf := func() uint64 {
		t.Helper()
		job, _, err := nomad.Jobs().Info(appID, nil)
		if err != nil || job.Version == nil {
			t.Fatalf("read job %s: %v", appID, err)
		}
		return *job.Version
	}
	copyOf := func() string {
		t.Helper()
		stubs, _, err := nomad.Jobs().Allocations(appID, false, nil)
		if err != nil {
			t.Fatalf("list the copies of %s: %v", appID, err)
		}
		var running []string
		for _, s := range stubs {
			// Running, not just wanted: a copy that failed and was replaced
			// (after an agent restart) is still wanted until Nomad tidies up.
			if s.DesiredStatus == "run" && s.ClientStatus == "running" {
				running = append(running, s.ID)
			}
		}
		if len(running) != 1 {
			t.Fatalf("%s has %d copies Nomad wants running, want 1", appID, len(running))
		}
		return running[0]
	}
	versionBefore, copyBefore := versionOf(), copyOf()

	// In the lab, restart the Nomad agent first: the copy then started before
	// the agent's last restart, as on a host after an installer rerun, when
	// Nomad no longer hands the request to the finished volume task until
	// the copy restarts (seen on the maintainer's host with 0.8.0).
	// Restoring a copy after an agent restart can itself fail: the bridge
	// plugin may refuse the copy's address as already given ("duplicate
	// allocation is not allowed", lab 2026-10-19), and Nomad then places a new
	// copy, which started after the restart and so isn't the case this tests.
	// So when the copy doesn't come through, wait for its replacement and
	// restart the agent again, up to three times.
	if g.target.RestartNomad != nil {
		for attempt := 1; ; attempt++ {
			g.target.RestartNomad(t)
			if g.copyStillRuns(t, copyBefore) {
				break
			}
			if attempt == 3 {
				t.Fatalf("copy %s of %s didn't come through 3 restarts of the Nomad agent", copyBefore, appID)
			}
			t.Logf("copy %s didn't come through the agent restart (attempt %d); waiting for its replacement", copyBefore, attempt)
			copyBefore = g.waitReplacementCopy(t, appID, copyBefore)
			versionBefore = versionOf()
		}
	}

	// Back up now, and wait for the run.
	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/backups", "application/json", `{"service": "web"}`)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var started struct{ ID string }
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &started) != nil || started.ID == "" {
		t.Fatalf("back up now: %d %s", resp.StatusCode, raw)
	}
	var run gateVolumeRun
	waitFor(t, "the volume backup to succeed", 5*time.Minute, func() bool {
		var l struct {
			Backups []gateVolumeRun `json:"backups"`
		}
		g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
		for _, b := range l.Backups {
			if b.ID != started.ID {
				continue
			}
			switch b.Status {
			case "failed":
				t.Fatalf("the volume backup failed: %s", b.Message)
			case "succeeded":
				run = b
				return true
			}
		}
		return false
	})

	settled := time.Now()
	if run.Engine != "volume" {
		t.Errorf("the backup's engine = %q, want volume", run.Engine)
	}
	if run.StoppedSeconds == nil || *run.StoppedSeconds <= 0 {
		t.Errorf("stoppedSeconds = %v, want more than 0: the service was stopped while its volume was archived", run.StoppedSeconds)
	}
	if len(run.Volumes) != 1 || run.Volumes[0].Name != "data" || run.Volumes[0].Bytes <= 0 || run.Volumes[0].SizeBytes <= 0 {
		t.Errorf("volumes = %+v, want the one volume data with its archive and data sizes", run.Volumes)
	} else if run.Bytes != run.Volumes[0].Bytes {
		t.Errorf("bytes = %d, want the archive's %d", run.Bytes, run.Volumes[0].Bytes)
	}
	// The record doesn't name the object, but the run's ID ends in the
	// timestamp its directory in the bucket is named by: <prefix>/vol/web/<ts>/.
	ts := strings.TrimPrefix(run.ID, "web-")
	if listed, err := g.bucketExec("ls", "/data/backups/gate/vol/web/"+ts+"/"); err != nil || !strings.Contains(listed, "data.tar.gz") {
		t.Errorf("the bucket's backups/gate/vol/web/%s/ lists %q (%v), want data.tar.gz", ts, listed, err)
	}

	// The backup restarted the same copy and left the job alone.
	if got := versionOf(); got != versionBefore {
		t.Errorf("the job is at version %d after the backup, was %d: a volume backup must not change it", got, versionBefore)
	}
	if got := copyOf(); got != copyBefore {
		t.Errorf("the copy is %s after the backup, was %s: it should be the same copy, restarted", got, copyBefore)
	}
	// Healthy again: serving its data, with every check of the copy passing.
	if got := readFile(); got != "A" {
		t.Errorf("/data/v = %q after the backup, want A", got)
	}
	// A check result older than the run is the one from before the restart, so
	// only results from after it count.
	waitFor(t, "the copy's checks to pass again", 2*time.Minute, func() bool {
		checks, err := nomad.Allocations().Checks(copyBefore, nil)
		if err != nil || len(checks) == 0 {
			return false
		}
		for _, c := range checks {
			if c.Status != "success" || c.Timestamp < settled.Unix() {
				return false
			}
		}
		return true
	})

	// The restore half. The data changes after the backup: /data/v becomes "B"
	// and /data/new appears, written through Nomad's exec (the gate's token can;
	// Caravana's can't). Restoring the backup must bring "A" back and delete
	// /data/new: a restore replaces the volume, it doesn't merge into it.
	if out, err := g.exec(t, appID, "web", d.ID, "sh", "-c", "printf B > /data/v && printf x > /data/new"); err != nil {
		t.Fatalf("change the volume's files: %v (%q)", err, out)
	}
	if got := readFile(); got != "B" {
		t.Fatalf("/data/v = %q after the change, want B", got)
	}

	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/backups/"+run.ID+"/restore", "", "")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var restoring struct{ ID string }
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &restoring) != nil || restoring.ID == "" {
		t.Fatalf("restore %s: %d %s", run.ID, resp.StatusCode, raw)
	}
	var restore gateVolumeRestore
	waitFor(t, "the volume restore to succeed", 5*time.Minute, func() bool {
		var l struct {
			Restores []gateVolumeRestore `json:"restores"`
		}
		g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
		for _, r := range l.Restores {
			if r.ID != restoring.ID {
				continue
			}
			switch r.Status {
			case "failed":
				t.Fatalf("the volume restore failed: %s", r.Message)
			case "succeeded":
				restore = r
				return true
			}
		}
		return false
	})
	restored := time.Now()
	if restore.Engine != "volume" || restore.BackupID != run.ID {
		t.Errorf("restore = %+v, want engine volume and the backup %s", restore, run.ID)
	}
	if restore.StoppedSeconds == nil || *restore.StoppedSeconds <= 0 {
		t.Errorf("restore stoppedSeconds = %v, want more than 0: the service was stopped while its volume was replaced", restore.StoppedSeconds)
	}

	// The old data is back and what was added since is gone.
	if got := readFile(); got != "A" {
		t.Errorf("/data/v = %q after the restore, want A", got)
	}
	if out := read("sh", "-c", "[ -e /data/new ] && echo present || echo gone"); out != "gone" {
		t.Errorf("/data/new after the restore: %q, want it deleted: a restore replaces the volume's files", out)
	}
	if out := read("ls", "-A", "/data"); out != "v" {
		t.Errorf("/data holds %q after the restore, want only v (no staging directory left)", out)
	}
	// Like the backup, the restore restarted the same copy and left the job alone.
	if got := versionOf(); got != versionBefore {
		t.Errorf("the job is at version %d after the restore, was %d: a volume restore must not change it", got, versionBefore)
	}
	if got := copyOf(); got != copyBefore {
		t.Errorf("the copy is %s after the restore, was %s: it should be the same copy, restarted", got, copyBefore)
	}
	waitFor(t, "the copy's checks to pass after the restore", 2*time.Minute, func() bool {
		checks, err := nomad.Allocations().Checks(copyBefore, nil)
		if err != nil || len(checks) == 0 {
			return false
		}
		for _, c := range checks {
			if c.Status != "success" || c.Timestamp < restored.Unix() {
				return false
			}
		}
		return true
	})

	resp = g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete %s: %d", appID, resp.StatusCode)
	}
	left, _, err := nomad.HostVolumes().List(&api.HostVolumeListRequest{}, &api.QueryOptions{Prefix: appID + "_"})
	if err != nil || len(left) != 0 {
		t.Errorf("host volumes left after delete: %d (%v)", len(left), err)
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

// gateRegistryHost is where the private-registry step's registry listens, on
// the node's loopback. The Docker daemon treats loopback registries as
// insecure by default, so it pulls from this one over plain HTTP, and Caravana
// reads digests from it the same way (lab finding P6: Lima forwards the VM's
// loopback to the Mac, where the lab's Caravana runs). Not port 5000: macOS's
// AirPlay Receiver listens there on the Mac and answers 403, so the lab's
// Caravana would talk to it instead of the forwarded registry.
const gateRegistryHost = "127.0.0.1:15000"

// gateRegistryJob is a registry (registry:2) with htpasswd auth on
// gateRegistryHost, run with the gate's own token like the S3 bucket. A
// prestart task writes the htpasswd file from the per-run user and password
// into the allocation's shared directory; the registry task then reads it. Both
// tasks use the host's network, so the registry binds the node's loopback. The
// password is in this job's env, which is fine for a throwaway one: the job is
// the gate's, not an app, and it lives only for the step.
func gateRegistryJob(user, pass string) *api.Job {
	return &api.Job{
		ID: new("gate-registry"), Name: new("gate-registry"), Type: new("service"), Datacenters: []string{"*"},
		TaskGroups: []*api.TaskGroup{{
			Name:     new("registry"),
			Networks: []*api.NetworkResource{{Mode: "host"}},
			Tasks: []*api.Task{
				{
					Name: "htpasswd", Driver: "docker",
					Lifecycle: &api.TaskLifecycle{Hook: "prestart"},
					Env:       map[string]string{"U": user, "P": pass},
					Config: map[string]any{
						"image":   "httpd:2-alpine",
						"command": "sh",
						"args":    []string{"-c", `htpasswd -Bbn "$U" "$P" > /alloc/data/htpasswd`},
					},
					Resources: &api.Resources{CPU: new(50), MemoryMB: new(64)},
				},
				{
					Name: "registry", Driver: "docker",
					Env: map[string]string{
						"REGISTRY_AUTH":                "htpasswd",
						"REGISTRY_AUTH_HTPASSWD_REALM": "gate",
						"REGISTRY_AUTH_HTPASSWD_PATH":  "/alloc/data/htpasswd",
						"REGISTRY_HTTP_ADDR":           gateRegistryHost,
					},
					Config:    map[string]any{"image": "registry:2", "network_mode": "host"},
					Resources: &api.Resources{CPU: new(100), MemoryMB: new(128)},
				},
			},
		}},
	}
}

// gateSkopeoJob is a batch job that copies busybox into the gate's registry as
// gate/busybox:v1, with the per-run credentials from its env (so they are not
// in the command line). It runs through a shell, whatever the image's own
// entrypoint is.
func gateSkopeoJob(user, pass string) *api.Job {
	return &api.Job{
		ID: new("gate-skopeo"), Name: new("gate-skopeo"), Type: new("batch"), Datacenters: []string{"*"},
		TaskGroups: []*api.TaskGroup{{
			Name:     new("copy"),
			Networks: []*api.NetworkResource{{Mode: "host"}},
			// A failed copy ends the job after two more tries, so the step sees
			// "failed" and says why, instead of waiting out its time.
			RestartPolicy: &api.RestartPolicy{Attempts: new(2), Interval: new(10 * time.Minute), Delay: new(5 * time.Second), Mode: new("fail")},
			Tasks: []*api.Task{{
				Name: "skopeo", Driver: "docker",
				Env: map[string]string{"U": user, "P": pass},
				Config: map[string]any{
					"image":        "quay.io/skopeo/stable",
					"network_mode": "host",
					"entrypoint":   []string{"/bin/sh", "-c"},
					"args": []string{`exec skopeo copy --dest-creds "$U:$P" --dest-tls-verify=false ` +
						`docker://busybox:1.37 docker://` + gateRegistryHost + `/gate/busybox:v1`},
				},
				Resources: &api.Resources{CPU: new(200), MemoryMB: new(256)},
			}},
		}},
	}
}

// gatePrivateApp is the app of the private-registry step: busybox's httpd on
// 8080, with a TCP health check, from the gate's private registry.
const gatePrivateApp = `{
  "source": {"kind": "image"},
  "services": [{
    "name": "web",
    "image": "` + gateRegistryHost + `/gate/busybox:v1",
    "command": "httpd",
    "args": ["-f", "-p", "8080", "-h", "/tmp"],
    "ports": [{"name": "http", "containerPort": 8080}],
    "health": {"interval": "5s", "timeout": "2s"}
  }]
}`

// registryAnswers401 says whether the gate's registry is up and asking for a
// login. It asks from this process, which reaches the node's loopback in the
// lab (Lima forwards it), and from inside the registry's own task, which also
// works when the gate runs far from the node (host.sh prove): busybox's wget
// exits non-zero on a 401 and names the status.
//
// When this process gets any answer, only a registry's 401 counts: something
// else answering on the port (in the lab, before Lima has forwarded it) means
// Caravana would reach that too. The task's own check is only for when this
// process can't connect at all.
func (g *gate) registryAnswers401() bool {
	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Get("http://" + gateRegistryHost + "/v2/"); err == nil {
		resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("Docker-Distribution-Api-Version") != ""
	}
	_, err := g.jobExec("gate-registry", "registry", "wget", "-q", "-O", "/dev/null", "http://"+gateRegistryHost+"/v2/")
	return err != nil && strings.Contains(err.Error(), "401")
}

// copyStillRuns reports whether the copy came through a restart of the Nomad
// agent: within half a minute it is running, Nomad still wants it to, and its
// tasks are running (the client restores them one by one).
func (g *gate) copyStillRuns(t *testing.T, copyID string) bool {
	t.Helper()
	return waitUntil(30*time.Second, func() bool {
		a, _, err := g.target.Nomad.Allocations().Info(copyID, nil)
		if err != nil || a.ClientStatus != "running" || a.DesiredStatus != "run" {
			return false
		}
		for _, ts := range a.TaskStates {
			if ts.State == "dead" && ts.Failed {
				return false
			}
		}
		return true
	})
}

// waitReplacementCopy waits, up to 3 minutes, for appID to run one copy other
// than old, and returns it.
func (g *gate) waitReplacementCopy(t *testing.T, appID, old string) string {
	t.Helper()
	var copyID string
	if !waitUntil(3*time.Minute, func() bool {
		for _, id := range g.runningCopies(t, appID) {
			if id != old {
				copyID = id
				return true
			}
		}
		return false
	}) {
		t.Fatalf("%s has no running copy other than %s after 3 minutes", appID, old)
	}
	return copyID
}

// runningCopies lists the copies of appID that Nomad wants running and runs.
func (g *gate) runningCopies(t *testing.T, appID string) []string {
	t.Helper()
	stubs, _, err := g.target.Nomad.Jobs().Allocations(appID, false, nil)
	if err != nil {
		t.Fatalf("list the copies of %s: %v", appID, err)
	}
	var ids []string
	for _, s := range stubs {
		if s.DesiredStatus == "run" && s.ClientStatus == "running" {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// privateRegistryStep (feature 017): an app whose image is in a registry that
// wants a login deploys with the credentials Caravana stores, and with the
// token Caravana really runs with. The step starts its own registry with
// htpasswd auth and puts an image in it, then checks, in order:
//
//  1. without credentials the deploy is 422 image_not_found, saying to add them;
//  2. with a wrong secret the test is not ok and the deploy is 422
//     registry_auth_failed;
//  3. with the right one the test is ok and the deploy becomes healthy;
//  4. the registered job holds placeholders and no secret, and no answer of
//     the API does;
//  5. removing the credentials leaves the running app running (its copy keeps
//     its pull credentials until it starts again, so the variable is only
//     emptied);
//  6. deleting the app leaves no variable of it, and the step's jobs and
//     credentials are gone.
//
// The user and password are made for each run.
func (g *gate) privateRegistryStep(t *testing.T) {
	const appID = "priv"
	nomad := g.target.Nomad
	user, pass := "gate"+g.run, "pw-"+g.run
	wrong := "wrong-" + pass
	registryPath := "/v1/registries/" + gateRegistryHost
	copyPath := nomadjob.PullCredentialsPath(appID, "web")

	// clean removes everything the step makes, whatever state it is in: it
	// runs first (an aborted earlier run may have left some) and when the step
	// ends.
	clean := func() {
		for _, req := range [][2]string{{http.MethodDelete, "/v1/apps/" + appID}, {http.MethodDelete, registryPath}} {
			if resp, err := g.apiRequest(req[0], req[1], "", ""); err == nil {
				resp.Body.Close()
			}
		}
		for _, id := range []string{"gate-skopeo", "gate-registry"} {
			_, _, _ = nomad.Jobs().Deregister(id, true, nil)
		}
		_, _ = nomad.Variables().Delete(deploy.RegistryVariablePath(gateRegistryHost), nil)
		if metas, _, err := nomad.Variables().PrefixList("nomad/jobs/"+appID+"/", nil); err == nil {
			for _, m := range metas {
				_, _ = nomad.Variables().Delete(m.Path, nil)
			}
		}
	}
	clean()
	t.Cleanup(clean)

	// The registry, and an image in it.
	if _, _, err := nomad.Jobs().Register(gateRegistryJob(user, pass), nil); err != nil {
		t.Fatalf("register the registry job: %v", err)
	}
	waitFor(t, "the registry to answer 401 on "+gateRegistryHost, 3*time.Minute, g.registryAnswers401)
	if _, _, err := nomad.Jobs().Register(gateSkopeoJob(user, pass), nil); err != nil {
		t.Fatalf("register the skopeo job: %v", err)
	}
	var copyFailure string
	waitFor(t, "skopeo to copy the image into the registry", 6*time.Minute, func() bool {
		stubs, _, err := nomad.Jobs().Allocations("gate-skopeo", false, nil)
		if err != nil {
			return false
		}
		for _, s := range stubs {
			switch s.ClientStatus {
			case "complete":
				return true
			case "failed":
				copyFailure = "the allocation failed"
				if state := s.TaskStates["skopeo"]; state != nil {
					for _, ev := range state.Events {
						copyFailure += "; " + ev.DisplayMessage
					}
				}
				return true
			}
		}
		return false
	})
	if copyFailure != "" {
		t.Fatalf("skopeo could not copy the image: %s", copyFailure)
	}

	// body reads and closes a response.
	body := func(resp *http.Response) []byte {
		t.Helper()
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// deployPrivate deploys the app and answers the status and body.
	deployPrivate := func() (int, []byte) {
		t.Helper()
		resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", "application/json", gatePrivateApp)
		status := resp.StatusCode
		return status, body(resp)
	}
	// refusal is the code, field and message of an error answer.
	refusal := func(raw []byte) (code, field, message string) {
		var e struct {
			Error struct{ Code, Field, Message string } `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return e.Error.Code, e.Error.Field, e.Error.Message
	}
	// saveCredentials stores the credentials and checks the answer holds no secret.
	saveCredentials := func(password string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"username": user, "password": password})
		resp := g.api(t, http.MethodPut, registryPath, "application/json", string(payload))
		status := resp.StatusCode
		raw := body(resp)
		if status != http.StatusOK || strings.Contains(string(raw), password) {
			t.Fatalf("PUT %s = %d %s, want 200 without the secret", registryPath, status, raw)
		}
	}
	// testCredentials runs the registry test and answers whether it was ok.
	testCredentials := func() bool {
		t.Helper()
		resp := g.api(t, http.MethodPost, registryPath+"/test", "", "")
		status := resp.StatusCode
		raw := body(resp)
		var r struct {
			LastTest *struct{ OK bool } `json:"lastTest"`
		}
		if status != http.StatusOK || json.Unmarshal(raw, &r) != nil || r.LastTest == nil {
			t.Fatalf("POST %s/test = %d %s, want 200 with lastTest", registryPath, status, raw)
		}
		return r.LastTest.OK
	}
	// noSecret fails when an answer holds a password the step used.
	noSecret := func(what string, raw []byte) {
		t.Helper()
		for _, secret := range []string{pass, wrong} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s holds a password", what)
			}
		}
	}

	// 1. No credentials: the image is "not found", and the message says what to do.
	status, raw := deployPrivate()
	code, field, message := refusal(raw)
	if status != http.StatusUnprocessableEntity || code != "image_not_found" || field != "services[0].image" ||
		!strings.Contains(message, "add credentials for "+gateRegistryHost) {
		t.Fatalf("deploy without credentials = %d %s, want 422 image_not_found on services[0].image saying to add credentials for %s",
			status, raw, gateRegistryHost)
	}

	// 2. A wrong secret: the test and the deploy both say it was refused.
	saveCredentials(wrong)
	if testCredentials() {
		t.Fatal("the registry test with a wrong secret is ok, want refused")
	}
	status, raw = deployPrivate()
	code, field, _ = refusal(raw)
	if status != http.StatusUnprocessableEntity || code != "registry_auth_failed" || field != "services[0].image" {
		t.Fatalf("deploy with a wrong secret = %d %s, want 422 registry_auth_failed on services[0].image", status, raw)
	}
	noSecret("the refused deploy's answer", raw)

	// 3. The right secret: the test is ok, and the node pulls the image.
	saveCredentials(pass)
	if !testCredentials() {
		t.Fatal("the registry test with the right secret is not ok")
	}
	status, raw = deployPrivate()
	var d gateDeployment
	if status != http.StatusAccepted || json.Unmarshal(raw, &d) != nil {
		t.Fatalf("deploy with the right secret = %d %s, want 202", status, raw)
	}
	if final := g.waitFinalOf(t, appID, d.ID, 5*time.Minute); final.Status != "healthy" {
		t.Fatalf("the private app ended %s: %s", final.Status, final.Error)
	}

	// 4. No secret anywhere a reader can look. The job holds the placeholders
	// the Docker driver fills in from the task's own variable, and the pinned
	// image keeps the registry's host and port.
	job, _, err := nomad.Jobs().Info(appID, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobJSON, _ := json.Marshal(job)
	noSecret("the registered job", jobJSON)
	config := job.TaskGroups[0].Tasks[0].Config
	if image, _ := config["image"].(string); !strings.HasPrefix(image, gateRegistryHost+"/gate/busybox@sha256:") {
		t.Errorf("the job's image = %q, want it pinned to a digest on %s", config["image"], gateRegistryHost)
	}
	auth, _ := config["auth"].([]any)
	var block map[string]any
	if len(auth) == 1 {
		block, _ = auth[0].(map[string]any)
	}
	if block["username"] != "${"+nomadjob.RegistryUsernameEnv+"}" || block["password"] != "${"+nomadjob.RegistryPasswordEnv+"}" ||
		block["server_address"] != gateRegistryHost {
		t.Errorf("the job's auth = %v, want placeholders for %s", auth, gateRegistryHost)
	}
	for _, path := range []string{"/v1/registries", "/v1/apps/" + appID, "/v1/apps/" + appID + "/deployments/" + d.ID,
		"/v1/apps/" + appID + "/jobspec"} {
		resp := g.api(t, http.MethodGet, path, "", "")
		status := resp.StatusCode
		answer := body(resp)
		if status != http.StatusOK {
			t.Errorf("GET %s = %d", path, status)
		}
		noSecret("GET "+path, answer)
		if path == "/v1/registries" && !strings.Contains(string(answer), gateRegistryHost) {
			t.Errorf("GET /v1/registries = %s, want %s listed", answer, gateRegistryHost)
		}
	}

	// 5. Remove the credentials: the app that runs keeps running, and its
	// variable is emptied (the registry stays named in it).
	before := g.runningCopies(t, appID)
	if len(before) != 1 {
		t.Fatalf("%s has %d running copies, want 1", appID, len(before))
	}
	resp := g.api(t, http.MethodDelete, registryPath, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE %s = %d, want 204", registryPath, resp.StatusCode)
	}
	if v, _, err := nomad.Variables().Peek(copyPath, nil); err != nil || v == nil || v.Items[nomadjob.RegistryPasswordEnv] != "" {
		t.Errorf("after the removal the copy's variable is %+v (%v), want it there with the password emptied", v, err)
	}
	time.Sleep(5 * time.Second) // time enough for Nomad to stop a copy, if anything asked it to
	if after := g.runningCopies(t, appID); len(after) != 1 || after[0] != before[0] {
		t.Errorf("after the removal the running copies are %v, want the same one, %v", after, before)
	}

	// 6. Deleting the app leaves nothing of it in Nomad Variables, and the
	// step's own jobs and credentials go.
	resp = g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete %s: %d", appID, resp.StatusCode)
	}
	if metas, _, err := nomad.Variables().PrefixList("nomad/jobs/"+appID+"/", nil); err != nil || len(metas) != 0 {
		t.Errorf("variables left after deleting %s: %d (%v)", appID, len(metas), err)
	}
	clean()
	if v, _, err := nomad.Variables().Peek(deploy.RegistryVariablePath(gateRegistryHost), nil); err != nil || v != nil {
		t.Errorf("the registry's credentials are still stored: %+v (%v)", v, err)
	}
	for _, id := range []string{"gate-skopeo", "gate-registry"} {
		if j, _, err := nomad.Jobs().Info(id, nil); err == nil && j != nil && j.Status != nil && *j.Status != "dead" {
			t.Errorf("job %s is %s after the step, want gone", id, *j.Status)
		}
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
