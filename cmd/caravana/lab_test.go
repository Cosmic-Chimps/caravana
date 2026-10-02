package main

// The lab test runs Caravana in-process against the local Nomad lab. It is
// opt-in, so CI never needs the lab:
//
//	deploy/dev/lab.sh up
//	eval "$(deploy/dev/lab.sh env)"
//	CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m
//
// It takes about 12 minutes: the broken deploy only fails at Nomad's
// progress deadline (10 minutes), because Nomad keeps rescheduling the
// unhealthy allocation until then.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
)

const labApp = `{
  "source": {"kind": "image"},
  "services": [{
    "name": "web",
    "image": "%s",
    "ports": [{"name": "http", "containerPort": %d}],
    "domains": [{"host": "whoami.localhost", "port": "http"}],
    "health": {"path": "/", "interval": "5s", "timeout": "2s"}
  }]
}`

type labDeployment struct {
	ID         string            `json:"id"`
	Status     string            `json:"status"`
	JobVersion *uint64           `json:"jobVersion"`
	Digests    map[string]string `json:"digests"`
	Error      string            `json:"error"`
}

func TestLab(t *testing.T) {
	if os.Getenv("CARAVANA_LAB") != "1" {
		t.Skip("set CARAVANA_LAB=1 (and NOMAD_ADDR/NOMAD_TOKEN from deploy/dev/lab.sh env) to run against the lab")
	}
	nomadAddr, token := os.Getenv("NOMAD_ADDR"), os.Getenv("NOMAD_TOKEN")
	if nomadAddr == "" {
		t.Fatal("NOMAD_ADDR is empty; run: eval \"$(deploy/dev/lab.sh env)\"")
	}
	nomad, err := deploy.NewNomadClient(nomadAddr, token)
	if err != nil {
		t.Fatal(err)
	}
	// Start clean: purge any whoami job, including the hand-deployed one.
	_, _, _ = nomad.Jobs().Deregister("whoami", true, nil)

	// Caravana itself, in-process.
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	exit := make(chan int, 1)
	go func() { exit <- run(ctx, []string{"-listen", addr, "-data-dir", t.TempDir()}, os.Getenv, &stderr) }()
	t.Cleanup(func() {
		cancel()
		if code := <-exit; code != 0 {
			t.Errorf("caravana exited %d; stderr:\n%s", code, stderr.String())
		}
	})
	base := "http://" + addr
	waitHealthy(t, base)

	deployApp := func(t *testing.T, image string, port int) (int, labDeployment, []byte) {
		t.Helper()
		resp, err := http.Post(base+"/v1/apps/whoami/deployments", "application/json; charset=UTF-8",
			strings.NewReader(fmt.Sprintf(labApp, image, port)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var d labDeployment
		_ = json.Unmarshal(raw, &d)
		return resp.StatusCode, d, raw
	}
	waitFinal := func(t *testing.T, id string, within time.Duration) labDeployment {
		t.Helper()
		deadline := time.Now().Add(within)
		for {
			resp, err := http.Get(base + "/v1/apps/whoami/deployments/" + id)
			if err == nil {
				var d labDeployment
				_ = json.NewDecoder(resp.Body).Decode(&d)
				resp.Body.Close()
				switch d.Status {
				case "healthy", "failed", "reverted", "superseded":
					return d
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("deployment %s not final after %v", id, within)
			}
			time.Sleep(time.Second)
		}
	}

	// US1 / SC-001: 202 within 10 s, healthy, answering over HTTPS, digest in Nomad.
	start := time.Now()
	status, first, raw := deployApp(t, "traefik/whoami:latest", 80)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, raw)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("deploy answered after %v, want under 10s", took)
	}
	if final := waitFinal(t, first.ID, 3*time.Minute); final.Status != "healthy" {
		t.Fatalf("first deploy ended %s: %s", final.Status, final.Error)
	}
	if err := callWhoami(); err != nil {
		t.Errorf("https://whoami.localhost:8443: %v", err)
	}
	job, _, err := nomad.Jobs().Info("whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	if image := job.TaskGroups[0].Tasks[0].Config["image"]; !strings.HasPrefix(fmt.Sprint(image), "traefik/whoami@sha256:") {
		t.Errorf("Nomad's image = %v, want a digest", image)
	}

	// US2 / SC-003: the same payload again is a new record and job version.
	status, second, raw := deployApp(t, "traefik/whoami:latest", 80)
	if status != http.StatusAccepted || second.ID == first.ID || *second.JobVersion != *first.JobVersion+1 {
		t.Fatalf("second deploy: %d %s", status, raw)
	}
	if final := waitFinal(t, second.ID, 3*time.Minute); final.Status != "healthy" {
		t.Fatalf("second deploy ended %s: %s", final.Status, final.Error)
	}

	// US3 / SC-004: a health check that can't pass reverts; the app keeps answering.
	var calls, failures atomic.Int64
	stopCalling := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopCalling:
				return
			case <-time.After(500 * time.Millisecond):
			}
			calls.Add(1)
			if callWhoami() != nil {
				failures.Add(1)
			}
		}
	}()
	status, broken, raw := deployApp(t, "traefik/whoami:latest", 81)
	if status != http.StatusAccepted {
		t.Fatalf("broken deploy: %d %s", status, raw)
	}
	// Nomad fails it at the progress deadline (10 min, set by the translator).
	final := waitFinal(t, broken.ID, 12*time.Minute)
	time.Sleep(5 * time.Second) // keep calling a little after the revert
	close(stopCalling)
	if final.Status != "reverted" || !strings.Contains(final.Error, "rolling back to job version") {
		t.Errorf("broken deploy ended %s (%q), want reverted with Nomad's rollback description", final.Status, final.Error)
	}
	if failures.Load() != 0 {
		t.Errorf("%d of %d calls failed during the broken rollout", failures.Load(), calls.Load())
	}
	if job, _, err := nomad.Jobs().Info("whoami", nil); err == nil && job.Meta["caravana.deployment"] != second.ID {
		t.Errorf("after the revert Nomad runs deployment %q, want the last healthy one %q", job.Meta["caravana.deployment"], second.ID)
	}

	// US4 / SC-005: a missing tag fails before anything is registered.
	before, _, _ := nomad.Jobs().Info("whoami", nil)
	status, _, raw = deployApp(t, "traefik/whoami:does-not-exist", 80)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("missing tag: %d %s, want 422", status, raw)
	}
	after, _, _ := nomad.Jobs().Info("whoami", nil)
	if before != nil && after != nil && *before.Version != *after.Version {
		t.Errorf("job version moved from %d to %d", *before.Version, *after.Version)
	}
}

func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("caravana not healthy: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// labClient reaches https://whoami.localhost:8443 through the lab's port on
// 127.0.0.1, with Traefik's self-signed certificate.
var labClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "whoami.localhost"}, // the lab's cert is self-signed
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:8443")
		},
	},
}

func callWhoami() error {
	resp, err := labClient.Get("https://whoami.localhost:8443/")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
