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

// `node space` (feature 045) reads Docker through the real runner: with no state, or with no Docker,
// it says so and fails, and it takes no argument.
func TestNodeSpace(t *testing.T) {
	root := t.TempDir()
	if code, stdout, stderr := nodeRun(t, root, "space"); code != exitFailed || stdout != "" || stderr == "" {
		t.Errorf("space without state: %d %q %q", code, stdout, stderr)
	}
	if code, _, _ := nodeRun(t, root, "space", "extra"); code != exitUsage {
		t.Errorf("space with an argument: %d", code)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("space made files: %v", entries)
	}
}

const nodeTestStore = `{"le": {"Account": {"PrivateKey": "s3cret-account-sentinel"}, "Certificates": [
  {"domain": {"main": "a.example.com"}, "certificate": "s3cret-certificate-sentinel", "key": "s3cret-key-sentinel"},
  {"domain": {"main": "b.example.com", "sans": ["www.b.example.com"]}, "certificate": "x", "key": "y"},
  {"domain": {"main": "other.test"}, "certificate": "x", "key": "y"}]}}`

func TestNodeDropCertificates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "var/lib/caravana-host/traefik")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "acme.json")
	if err := os.WriteFile(path, []byte(nodeTestStore), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// A dry run says what it would drop and writes nothing. Names are lower-cased and an empty entry is skipped.
	code, stdout, stderr := nodeRun(t, root, "drop-certificates", "--dry-run", "--names", "A.example.com,*.b.example.com,")
	if code != exitOK || stdout != "a.example.com\n" || stderr != "" || read() != nodeTestStore {
		t.Fatalf("dry run: %d %q %q", code, stdout, stderr)
	}
	code, stdout, _ = nodeRun(t, root, "drop-certificates", "--names", "a.example.com,*.example.com,*.b.example.com,")
	if code != exitOK || stdout != "a.example.com, b.example.com+www.b.example.com\n" {
		t.Fatalf("drop: %d %q", code, stdout)
	}
	if got := read(); strings.Contains(got, "a.example.com") || strings.Contains(got, "b.example.com") ||
		!strings.Contains(got, "other.test") || !strings.Contains(got, "s3cret-account-sentinel") {
		t.Errorf("store after the drop:\n%s", got)
	}
	for _, out := range []string{stdout, stderr} {
		if strings.Contains(out, "sentinel") {
			t.Errorf("output holds store content: %q", out)
		}
	}
	// Again: nothing left, one empty line.
	if code, stdout, _ = nodeRun(t, root, "drop-certificates", "--names", "a.example.com"); code != exitOK || stdout != "\n" {
		t.Errorf("nothing to drop: %d %q", code, stdout)
	}
	// A server with no store folder has nothing to drop either.
	if code, stdout, _ = nodeRun(t, t.TempDir(), "drop-certificates", "--names", "a.example.com"); code != exitOK || stdout != "\n" {
		t.Errorf("no folder: %d %q", code, stdout)
	}
}

func TestNodeDropCertificatesRefusals(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "var/lib/caravana-host/traefik")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	usage := map[string][]string{
		"no names":        {"drop-certificates"},
		"empty names":     {"drop-certificates", "--names", " , "},
		"a bare label":    {"drop-certificates", "--names", "a.example.com,localhost"},
		"a path":          {"drop-certificates", "--names", "a.example.com/x"},
		"a short wild":    {"drop-certificates", "--names", "*.com"},
		"an argument":     {"drop-certificates", "--names", "a.example.com", "extra"},
		"an unknown flag": {"drop-certificates", "--names", "a.example.com", "--all"},
	}
	for name, args := range usage {
		if code, stdout, stderr := nodeRun(t, root, args...); code != exitUsage || stdout != "" || stderr == "" {
			t.Errorf("%s: %d %q %q", name, code, stdout, stderr)
		}
	}
	// The usage error names the entry's number, not its text.
	if _, _, stderr := nodeRun(t, root, "drop-certificates", "--names", "a.example.com,not_a_host"); !strings.Contains(stderr, "entry 2") || strings.Contains(stderr, "not_a_host") {
		t.Errorf("usage error: %q", stderr)
	}
	// A file that isn't a store is exit 1, naming the file and nothing of what it holds.
	if err := os.WriteFile(filepath.Join(dir, "acme.json"), []byte("s3cret-not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := nodeRun(t, root, "drop-certificates", "--names", "a.example.com")
	if code != exitFailed || !strings.Contains(stderr, "acme.json") || strings.Contains(stderr, "s3cret") {
		t.Errorf("a bad store: %d %q", code, stderr)
	}
	// Not root, and no test root: refused like the other commands.
	old := geteuid
	geteuid = func() int { return 1000 }
	defer func() { geteuid = old }()
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"node", "drop-certificates", "--names", "a.example.com"}, noEnv, strings.NewReader(""), &out, &errb); code != exitFailed || !strings.Contains(errb.String(), "run it as root") {
		t.Errorf("not root: %d %q", code, errb.String())
	}
}
