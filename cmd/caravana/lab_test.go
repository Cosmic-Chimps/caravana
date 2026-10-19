package main

// The lab test runs Caravana in-process against the local Nomad lab. It is
// opt-in, so CI never needs the lab:
//
//	deploy/dev/lab.sh up
//	eval "$(deploy/dev/lab.sh env)"
//	CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m
//
// It runs the console steps (console_lab_test.go), then the gate steps in
// gate_test.go. It takes about 12 minutes: the broken deploy only fails at
// Nomad's progress deadline (10 minutes), because Nomad keeps rescheduling
// the unhealthy allocation until then. TestConsoleLab runs the console steps
// alone, in a minute or two.

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// labEnv is Caravana running in-process next to the lab.
type labEnv struct {
	target gateTarget // what the gate steps need
	nomad  *api.Client
}

// startLab skips unless CARAVANA_LAB=1, then starts Caravana in-process with
// the webhook-only listener, makes the admin and an API token, and returns
// where everything is.
func startLab(t *testing.T) labEnv {
	t.Helper()
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

	// Caravana itself, in-process, with the webhook-only listener. It gets the
	// scoped token, as on the host; the test's own client keeps the lab's
	// management token for its checks.
	scoped := labCaravanaToken(t, nomad)
	getenv := func(key string) string {
		if key == "NOMAD_TOKEN" {
			return scoped
		}
		if key == "CARAVANA_NOTIFY_RESOLVE_AFTER" {
			// The console-notifications step waits for "resolved"; the default is 5 minutes.
			return "10s"
		}
		return os.Getenv(key)
	}
	addr, hookAddr, dataDir := freeAddr(t), freeAddr(t), t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	exit := make(chan int, 1)
	go func() {
		exit <- runServer(ctx, []string{"-listen", addr, "-webhook-listen", hookAddr, "-data-dir", dataDir}, getenv, &stderr)
	}()
	t.Cleanup(func() {
		cancel()
		if code := <-exit; code != 0 {
			t.Errorf("caravana exited %d; stderr:\n%s", code, stderr.String())
		}
	})
	waitHealthy(t, "http://"+addr)

	return labEnv{
		nomad: nomad,
		target: gateTarget{
			API:      "http://" + addr,
			APIToken: labAPIToken(t, ctx, dataDir),
			Webhook:  "http://" + hookAddr,
			// The lab runs Caravana with its webhook-only listener.
			WebhookOnly: true,
			AppHost:     "whoami.localhost",
			AppURL:      "https://whoami.localhost:8443/",
			App:         labClient,
			Nomad:       nomad,
			RestartNomad: func(t *testing.T) {
				t.Helper()
				out, err := exec.Command("limactl", "shell", "--workdir", "/", "caravana-lab",
					"sudo", "systemctl", "restart", "nomad").CombinedOutput()
				if err != nil {
					t.Fatalf("restart the lab's Nomad agent: %v: %s", err, out)
				}
				waitFor(t, "the lab's Nomad agent to answer again", 2*time.Minute, func() bool {
					_, err := nomad.Agent().Self()
					return err == nil
				})
				time.Sleep(10 * time.Second) // the client restores its allocations
			},
		},
	}
}

// TestLab runs the console steps (console_lab_test.go), then the gate steps,
// against the lab.
func TestLab(t *testing.T) {
	env := startLab(t)
	if !t.Run("console", func(t *testing.T) { runConsole(t, env.target, labEmail, labPassword) }) {
		return // the gate starts from a clean whoami; a broken console is worth knowing first
	}
	runGate(t, env.target)
}

// TestConsoleLab runs only the console steps, which take a minute or two
// instead of TestLab's twelve.
func TestConsoleLab(t *testing.T) {
	env := startLab(t)
	runConsole(t, env.target, labEmail, labPassword)
}

// labCaravanaToken does what deploy/install/install.sh does on the host: it applies
// the policy in deploy/install/caravana-policy.hcl as "caravana" and makes a client
// token with only that policy, deleted when the test ends. Every lab run thus
// checks the current policy file, so a missing capability fails here and not
// first on a real host.
func labCaravanaToken(t *testing.T, nomad *api.Client) string {
	t.Helper()
	rules, err := os.ReadFile("../../deploy/install/caravana-policy.hcl")
	if err != nil {
		t.Fatal(err)
	}
	policy := &api.ACLPolicy{
		Name:        "caravana",
		Description: "Caravana registers jobs and follows deployments",
		Rules:       string(rules),
	}
	if _, err := nomad.ACLPolicies().Upsert(policy, nil); err != nil {
		t.Fatalf("apply the caravana policy (does NOMAD_TOKEN hold the lab's management token?): %v", err)
	}
	token, _, err := nomad.ACLTokens().Create(&api.ACLToken{
		Name:     "caravana-lab-test",
		Type:     "client",
		Policies: []string{"caravana"},
	}, nil)
	if err != nil {
		t.Fatalf("create the scoped token: %v", err)
	}
	t.Cleanup(func() { _, _ = nomad.ACLTokens().Delete(token.AccessorID, nil) })
	return token.SecretID
}

// The admin the lab test creates, as the owner would on the host.
const (
	labEmail    = "lab@example.com"
	labPassword = "a lab password, long enough"
)

// labAPIToken does what the owner does on the host, in-process and while the
// server holds the data directory (which exercises the CLI's shared open):
// create the admin, with the password from a pipe, and an API token named lab.
// It returns the token.
func labAPIToken(t *testing.T, ctx context.Context, dataDir string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	args := func(command ...string) []string { return append(command, "-data-dir", dataDir) }

	code := runAdmin(ctx, args("init", "--email", labEmail), os.Getenv,
		strings.NewReader(labPassword+"\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("caravana admin init exited %d: %s", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = runAdmin(ctx, args("create-token", "--name", "lab"), os.Getenv, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("caravana admin create-token exited %d: %s", code, errOut.String())
	}
	return strings.TrimSpace(out.String())
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
