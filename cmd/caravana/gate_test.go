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
)

// gateTarget is where the gate runs.
type gateTarget struct {
	API     string       // Caravana's API, e.g. http://127.0.0.1:17070
	Webhook string       // the webhook base URL: the webhook-only listener, or the public webhook host
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

// runGate runs every step, in order, stopping at the first failure.
func runGate(t *testing.T, target gateTarget) {
	g := &gate{target: target, run: strconv.FormatInt(time.Now().UnixNano(), 36)}
	steps := []struct {
		name       string
		publicOnly bool
		run        func(t *testing.T)
	}{
		{"clean", false, g.clean},
		{"deploy", false, g.deploy},
		{"webhook-redeploy", false, g.webhookRedeploy},
		{"webhook-unchanged", false, g.webhookUnchanged},
		{"https-trusted", true, g.httpsTrusted},
		{"http-redirects", true, g.httpRedirects},
		{"webhook-host-only-webhook", false, g.webhookOnly},
		{"same-payload", false, g.samePayload},
		{"broken-reverts", false, g.brokenReverts},
		{"missing-tag", false, g.missingTag},
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
	resp, err := http.Post(g.target.API+"/v1/apps/whoami/webhook", "", nil)
	if err != nil {
		t.Fatal(err)
	}
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

// webhookOnly: the webhook base serves nothing but the webhook.
func (g *gate) webhookOnly(t *testing.T) {
	for _, path := range []string{"/v1/health", "/v1/apps/whoami", "/"} {
		resp, err := http.Get(g.target.Webhook + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s on the webhook base = %d, want 404", path, resp.StatusCode)
		}
	}
	resp, err := http.Post(g.target.Webhook+"/v1/webhooks/no-such-app", "", nil)
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
	resp, err := http.Post(g.target.API+"/v1/apps/whoami/deployments", "application/json; charset=UTF-8", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var d gateDeployment
	_ = json.Unmarshal(raw, &d)
	return resp.StatusCode, d, raw
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
	resp, err := http.DefaultClient.Do(req)
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

// waitFinal polls a deployment until it has a final status.
func (g *gate) waitFinal(t *testing.T, id string, within time.Duration) gateDeployment {
	t.Helper()
	var d gateDeployment
	if !waitUntil(within, func() bool {
		resp, err := http.Get(g.target.API + "/v1/apps/whoami/deployments/" + id)
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
	if staging {
		// Staging certificates aren't publicly trusted, so the steps that only
		// need the app to answer skip the check; https-trusted still fails
		// and says why.
		app.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // staging CA only
	}
	runGate(t, gateTarget{
		API:     need("GATE_API"),
		Webhook: need("GATE_WEBHOOK"),
		AppHost: host,
		AppURL:  "https://" + host + "/",
		App:     app,
		Nomad:   nomad,
		Public:  &publicTarget{IP: need("GATE_PUBLIC_IP"), Staging: staging},
	})
}
