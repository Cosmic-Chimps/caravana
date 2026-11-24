package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/nodeagent"
)

// Feature 055: getHost's io block. A server whose agent reports counters has rates once two reports are in
// (the numbers, the reason null, per interface and device), one whose agent reports nothing says why, and
// a block the console can't read never fails the sync.

func TestGetHostIO(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	j := addServer(t, e, `{"name": "fsn-io"}`)
	root := t.TempDir()
	agent := &nodeagent.Agent{
		Root: root, Runner: &fakeHost{hostname: "fsn-io.example.com"}, HTTP: nodeagent.NewHTTPClient(false),
		Version: "0.0.1-io", NomadAddr: localNomad(t).URL,
	}
	if err := agent.Join(context.Background(), nodeagent.JoinOptions{URL: e.RawURL, Token: j.Token, Address: "203.0.113.9"}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(root, "etc/caravana-node/node.env"))
	auth := "Bearer " + regexp.MustCompile(`cvnd_[A-Za-z0-9_-]{43}`).FindString(string(env))

	sync := func(io any) int {
		report := map[string]any{
			"agent": "0.0.1", "nomad": "2.0.7", "docker": "27.5.1", "dockerRunning": true, "diskFreeMb": 1, "diskTotalMb": 2,
			"memoryMb": 3, "kernel": "6", "role": "client", "nomadLeader": true, "serverRunning": false, "reach": []any{},
		}
		if io != nil {
			report["io"] = io
		}
		b, _ := json.Marshal(report)
		status, _, _ := callWith(t, auth, http.MethodPost, e.RawURL+"/v1/node/sync", "application/json", string(b))
		return status
	}
	block := func(at time.Time, rx, tx, read, write, busy int64) map[string]any {
		return map[string]any{"at": at.UnixMilli(),
			"net": []any{
				map[string]any{"name": "eth0", "kind": "default", "rxBytes": rx, "txBytes": tx},
				map[string]any{"name": "caravana-mesh", "kind": "mesh", "rxBytes": 7, "txBytes": 9}},
			"disks": []any{map[string]any{"name": "vda1", "holds": []string{"nomad", "docker"}, "readBytes": read, "writeBytes": write, "busyMs": busy}}}
	}
	io := func() map[string]any { return getJSON(t, e, "/v1/host?server=fsn-io", "Host")["io"].(map[string]any) }

	// No sync yet with an io block: the agent syncs without one (an older agent).
	if status := sync(nil); status != http.StatusOK {
		t.Fatalf("sync without io = %d", status)
	}
	if got := io(); got["reason"] != "agent_too_old" || got["at"] != nil {
		t.Errorf("io = %v, want reason agent_too_old", got)
	}

	// Two reports 30 s apart make rates; a bad block between them is dropped and the sync still answers 200.
	now := time.Now()
	if status := sync(block(now.Add(-50*time.Second), 1_000_000, 100_000, 5_000_000, 6_000_000, 1000)); status != http.StatusOK {
		t.Fatalf("first sync = %d", status)
	}
	if got := io(); got["reason"] != "waiting" {
		t.Errorf("after one report: io = %v, want waiting", got)
	}
	if status := sync(map[string]any{"at": "yesterday", "net": 4}); status != http.StatusOK {
		t.Errorf("a block that doesn't read failed the sync: %d", status)
	}
	if got := io(); got["reason"] != "agent_too_old" {
		t.Errorf("after a dropped block: io = %v", got)
	}
	sync(block(now.Add(-50*time.Second), 1_000_000, 100_000, 5_000_000, 6_000_000, 1000))
	if status := sync(block(now.Add(-20*time.Second), 4_000_000, 400_000, 8_000_000, 12_000_000, 16_000)); status != http.StatusOK {
		t.Fatalf("second sync = %d", status)
	}
	got := io()
	if got["reason"] != nil || got["at"] == nil {
		t.Fatalf("io = %v, want known", got)
	}
	net, disk := got["network"].(map[string]any), got["disk"].(map[string]any)
	if net["rxBytesPerSec"] != float64(100_000) || net["txBytesPerSec"] != float64(10_000) {
		t.Errorf("network = %v", net)
	}
	if disk["readBytesPerSec"] != float64(100_000) || disk["writeBytesPerSec"] != float64(200_000) || disk["busyPercent"] != float64(50) {
		t.Errorf("disk = %v", disk)
	}
	if ifaces := net["interfaces"].([]any); len(ifaces) != 2 || ifaces[0].(map[string]any)["name"] != "eth0" || ifaces[1].(map[string]any)["kind"] != "mesh" {
		t.Errorf("interfaces = %v", net["interfaces"])
	}
	if devs := disk["devices"].([]any); len(devs) != 1 || devs[0].(map[string]any)["name"] != "vda1" {
		t.Errorf("devices = %v", disk["devices"])
	}

	// The server Caravana runs on is a node with an agent row too, but has reported nothing here.
	if first := getJSON(t, e, "/v1/host", "Host")["io"].(map[string]any); first["reason"] == nil {
		t.Errorf("the first server's io = %v, want a reason", first)
	}
}

// A Nomad node Caravana doesn't manage (the lab's) has no agent: no_agent, every number null.
func TestGetHostIOWithoutAnAgent(t *testing.T) {
	e := monEnv(t)
	io := getJSON(t, e, "/v1/host", "Host")["io"].(map[string]any)
	if io["reason"] != "no_agent" {
		t.Errorf("io = %v, want no_agent", io)
	}
	net, disk := io["network"].(map[string]any), io["disk"].(map[string]any)
	for _, v := range []any{net["rxBytesPerSec"], net["txBytesPerSec"], disk["readBytesPerSec"], disk["writeBytesPerSec"], disk["busyPercent"]} {
		if v != nil {
			t.Errorf("a number without an agent: %v in %v", v, io)
		}
	}
}
