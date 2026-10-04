package main

// The lab test runs Caravana in-process against the local Nomad lab. It is
// opt-in, so CI never needs the lab:
//
//	deploy/dev/lab.sh up
//	eval "$(deploy/dev/lab.sh env)"
//	CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m
//
// It runs the gate steps in gate_test.go. It takes about 12 minutes: the
// broken deploy only fails at Nomad's progress deadline (10 minutes),
// because Nomad keeps rescheduling the unhealthy allocation until then.

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
)

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

	// Caravana itself, in-process, with the webhook-only listener.
	addr, hookAddr, dataDir := freeAddr(t), freeAddr(t), t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	exit := make(chan int, 1)
	go func() {
		exit <- runServer(ctx, []string{"-listen", addr, "-webhook-listen", hookAddr, "-data-dir", dataDir}, os.Getenv, &stderr)
	}()
	t.Cleanup(func() {
		cancel()
		if code := <-exit; code != 0 {
			t.Errorf("caravana exited %d; stderr:\n%s", code, stderr.String())
		}
	})
	waitHealthy(t, "http://"+addr)

	runGate(t, gateTarget{
		API:      "http://" + addr,
		APIToken: labAPIToken(t, ctx, dataDir),
		Webhook:  "http://" + hookAddr,
		AppHost:  "whoami.localhost",
		AppURL:   "https://whoami.localhost:8443/",
		App:      labClient,
		Nomad:    nomad,
	})
}

// labAPIToken does what the owner does on the host, in-process and while the
// server holds the data directory (which exercises the CLI's shared open):
// create the admin, with the password from a pipe, and an API token named lab.
// It returns the token.
func labAPIToken(t *testing.T, ctx context.Context, dataDir string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	args := func(command ...string) []string { return append(command, "-data-dir", dataDir) }

	code := runAdmin(ctx, args("init", "--email", "lab@example.com"), os.Getenv,
		strings.NewReader("a lab password, long enough\n"), &out, &errOut)
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
