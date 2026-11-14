package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/nodeagent"
)

const (
	nodeTestJoinToken = "cvjn_s3cretSentinelJoinToken0123456789abcdefgh12"
	nodeTestPeerKey   = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo="
)

// nodeRun runs `caravana node args...` in-process under a root folder and returns the exit code and
// both outputs.
func nodeRun(t *testing.T, root string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		if k == "CARAVANA_NODE_ROOT" {
			return root
		}
		return ""
	}
	code = run(context.Background(), append([]string{"node"}, args...), getenv, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestNodeUsageAndUnknownCommand(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := nodeRun(t, root); code != exitUsage || !strings.Contains(stderr, "usage: caravana node") {
		t.Errorf("no command: %d %q", code, stderr)
	}
	for _, h := range []string{"help", "--help", "-h"} {
		if code, stdout, _ := nodeRun(t, root, h); code != exitOK || !strings.Contains(stdout, "init-first") || !strings.Contains(stdout, "leave") {
			t.Errorf("%s: %d %q", h, code, stdout)
		}
	}
	if code, _, stderr := nodeRun(t, root, "frobnicate"); code != exitUsage || !strings.Contains(stderr, `unknown command "frobnicate"`) {
		t.Errorf("unknown: %d %q", code, stderr)
	}
	if code, _, _ := nodeRun(t, root, "up", "extra"); code != exitUsage {
		t.Errorf("an extra argument: %d", code)
	}
	if code, _, _ := nodeRun(t, root, "sync", "--nope"); code != exitUsage {
		t.Errorf("an unknown flag: %d", code)
	}
}

func TestRootHelpListsNode(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"help"}, noEnv, strings.NewReader(""), &out, &out); code != 0 || !strings.Contains(out.String(), "node") || !strings.Contains(out.String(), "admin") {
		t.Errorf("help: %d %q", code, out.String())
	}
}

func TestNodeRefusesWhenNotRoot(t *testing.T) {
	old := geteuid
	geteuid = func() int { return 1000 }
	defer func() { geteuid = old }()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"node", "up"}, noEnv, strings.NewReader(""), &out, &errb)
	if code != exitFailed || !strings.Contains(errb.String(), "run it as root") {
		t.Errorf("not root: %d %q", code, errb.String())
	}
	// ... but help and usage errors don't need root.
	if code := run(context.Background(), []string{"node", "help"}, noEnv, strings.NewReader(""), &out, &errb); code != exitOK {
		t.Errorf("help as non-root: %d", code)
	}
	// CARAVANA_NODE_ROOT lifts it, for tests.
	if code, _, _ := nodeRun(t, t.TempDir(), "up"); code != exitOK {
		t.Errorf("up with a root folder: %d", code)
	}
}

func TestNodeCommandsWithNothingSetUp(t *testing.T) {
	root := t.TempDir()
	if code, stdout, _ := nodeRun(t, root, "up"); code != exitOK || stdout != "" {
		t.Errorf("up without state: %d %q", code, stdout)
	}
	if code, stdout, _ := nodeRun(t, root, "sync"); code != exitOK || !strings.Contains(stdout, "not registered yet") {
		t.Errorf("sync without node.env: %d %q", code, stdout)
	}
	if code, _, stderr := nodeRun(t, root, "show", "name"); code != exitFailed || stderr == "" {
		t.Errorf("show without state: %d %q", code, stderr)
	}
	if code, _, _ := nodeRun(t, root, "show"); code != exitUsage {
		t.Errorf("show without a field: %d", code)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("a no-op made files: %v", entries)
	}
}

func TestNodeShow(t *testing.T) {
	root := t.TempDir()
	a := &nodeagent.Agent{Root: root}
	if err := a.SaveState(nodeagent.State{Index: 2, Name: "fsn-1", Pool: "default", Role: nodeagent.RoleClient, Servers: []int{1}}); err != nil {
		t.Fatal(err)
	}
	// The WireGuard key for the RFC 7748 vector: its public key is known.
	if err := os.WriteFile(filepath.Join(root, "etc/caravana-node/mesh.key"), []byte("dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"name": "fsn-1", "index": "2", "role": "client", "first": "false",
		"public-key": "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo=",
	} {
		code, stdout, stderr := nodeRun(t, root, "show", field)
		if code != exitOK || stdout != want+"\n" {
			t.Errorf("show %s = %d %q %q", field, code, stdout, stderr)
		}
	}
	if code, _, _ := nodeRun(t, root, "show", "nope"); code != exitFailed {
		t.Errorf("show nope: %d", code)
	}
}

func TestNodeJoinFlagChecks(t *testing.T) {
	good := "https://caravana.example.com"
	tests := map[string][]string{
		"no flags":        {"join"},
		"no token":        {"join", "--url", good},
		"no url":          {"join", "--token", nodeTestJoinToken},
		"http remote":     {"join", "--url", "http://caravana.example.com", "--token", nodeTestJoinToken},
		"a path":          {"join", "--url", good + "/console", "--token", nodeTestJoinToken},
		"short token":     {"join", "--url", good, "--token", "cvjn_short"},
		"wrong prefix":    {"join", "--url", good, "--token", "cvnd_" + strings.Repeat("a", 43)},
		"address a name":  {"join", "--url", good, "--token", nodeTestJoinToken, "--address", "fsn.example.com"},
		"negative cpu":    {"join", "--url", good, "--token", nodeTestJoinToken, "--cpu-total-compute", "-1"},
		"init-first none": {"init-first"},
		"init-first name": {"init-first", "--address", "caravana.example.com"},
		"issuer http":     {"init-first", "--address", "203.0.113.4", "--issuer", "http://caravana.example.com"},
		"issuer path":     {"init-first", "--address", "203.0.113.4", "--issuer", "https://caravana.example.com/x"},
		"issuer ip":       {"init-first", "--address", "203.0.113.4", "--issuer", "https://203.0.113.4"},
	}
	for name, args := range tests {
		root := t.TempDir()
		code, stdout, stderr := nodeRun(t, root, args...)
		if code != exitUsage {
			t.Errorf("%s: exit %d, want %d (%q)", name, code, exitUsage, stderr)
		}
		// The token is never echoed, and nothing was made.
		if strings.Contains(stdout+stderr, nodeTestJoinToken) {
			t.Errorf("%s: the token was printed: %s", name, stderr)
		}
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Errorf("%s: files were made: %v", name, entries)
		}
	}
}

// A server that joined already needs no options: `join` without flags renders and runs up, and
// that makes no call. Here Up has no state's mesh.key to read, so it fails after the render.
func TestNodeJoinWithNodeEnvNeedsNoFlags(t *testing.T) {
	root := t.TempDir()
	a := &nodeagent.Agent{Root: root}
	if err := a.SaveState(nodeagent.State{Index: 3, Name: "fsn-2", Pool: "default", Role: nodeagent.RoleClient, Servers: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveNodeEnv(nodeagent.NodeEnv{URL: "https://caravana.example.com", Token: "cvnd_" + strings.Repeat("a", 43)}); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := nodeRun(t, root, "join")
	if code == exitUsage {
		t.Errorf("a joined server was asked for flags: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/nomad.d/nomad.hcl")); err != nil {
		t.Errorf("join didn't render nomad.hcl: %v", err)
	}
}
