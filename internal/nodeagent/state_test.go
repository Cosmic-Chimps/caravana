package nodeagent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

func TestStateRoundTrip(t *testing.T) {
	a := newTestAgent(t, nil)
	if _, err := a.LoadState(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing state.json is %v, want ErrNotExist", err)
	}
	st := State{Index: 3, Name: "fsn-2", Pool: "default", Role: RoleClient, Servers: []int{1},
		Peers: []mesh.Peer{{Index: 1, PublicKey: peerKey1, Endpoint: "203.0.113.4:51820"}}}
	if err := a.SaveState(st); err != nil {
		t.Fatal(err)
	}
	got, err := a.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Index != 3 || got.Name != "fsn-2" || len(got.Peers) != 1 || got.Peers[0] != st.Peers[0] {
		t.Errorf("round trip = %+v", got)
	}
	if m := a.mode(t, "etc/caravana-node/state.json"); m != 0o600 {
		t.Errorf("state.json mode = %v", m)
	}
	if m := a.mode(t, "etc/caravana-node"); m != 0o700 {
		t.Errorf("the agent's folder mode = %v", m)
	}
	entries, _ := os.ReadDir(a.file("etc/caravana-node"))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
	// nil lists are written as [], as the contract's example has them.
	if err := a.SaveState(State{Index: 1, Name: "hel-1", Pool: "default", First: true, Role: RoleServer}); err != nil {
		t.Fatal(err)
	}
	if raw := a.read(t, "etc/caravana-node/state.json"); !strings.Contains(raw, `"servers": []`) || !strings.Contains(raw, `"peers": []`) {
		t.Errorf("nil lists aren't []:\n%s", raw)
	}
}

func TestLoadStateRefusesBadState(t *testing.T) {
	a := newTestAgent(t, nil)
	dir := a.file("etc/caravana-node")
	_ = os.MkdirAll(dir, 0o700)
	tests := map[string]string{
		"not json":    `{`,
		"old version": `{"version": 2, "index": 1, "name": "a", "pool": "default", "role": "server"}`,
		"index 0":     `{"version": 1, "index": 0, "name": "a", "pool": "default", "role": "server"}`,
		"index 64":    `{"version": 1, "index": 64, "name": "a", "pool": "default", "role": "server"}`,
		"bad name":    `{"version": 1, "index": 1, "name": "A b", "pool": "default", "role": "server"}`,
		"bad pool":    `{"version": 1, "index": 1, "name": "a", "pool": "", "role": "server"}`,
		"bad role":    `{"version": 1, "index": 1, "name": "a", "pool": "default", "role": "boss"}`,
	}
	for name, content := range tests {
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := a.LoadState(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNodeEnv(t *testing.T) {
	a := newTestAgent(t, nil)
	if _, ok, err := a.LoadNodeEnv(); ok || err != nil {
		t.Fatalf("no node.env: ok=%v err=%v", ok, err)
	}
	in := NodeEnv{URL: "https://caravana.example.com", Token: sentinelNodeToken, Insecure: true}
	if err := a.SaveNodeEnv(in); err != nil {
		t.Fatal(err)
	}
	if m := a.mode(t, "etc/caravana-node/node.env"); m != 0o600 {
		t.Errorf("node.env mode = %v", m)
	}
	raw := a.read(t, "etc/caravana-node/node.env")
	for _, want := range []string{"CARAVANA_URL=https://caravana.example.com\n", "CARAVANA_NODE_TOKEN=" + sentinelNodeToken + "\n", "CARAVANA_INSECURE=1\n"} {
		if !strings.Contains(raw, want) {
			t.Errorf("node.env lacks %q:\n%s", want, raw)
		}
	}
	got, ok, err := a.LoadNodeEnv()
	if err != nil || !ok || got != in {
		t.Errorf("read back %+v %v %v", got, ok, err)
	}
}

// The installer's env_value rule: the first NAME= line, one leading and one trailing quote removed.
func TestEnvValue(t *testing.T) {
	tests := []struct{ content, name, want string }{
		{"A=1\nB=2\n", "B", "2"},
		{"A='x y'\n", "A", "x y"},
		{"A=1\nA=2\n", "A", "1"},
		{"XA=1\nA=2\n", "A", "2"},
		{"A=\n", "A", ""},
		{"B=1\n", "A", ""},
		{"A=it's\n", "A", "it's"},
		{"A=1\r\n", "A", "1"},
	}
	for _, tt := range tests {
		if got := envValue(tt.content, tt.name); got != tt.want {
			t.Errorf("envValue(%q, %s) = %q, want %q", tt.content, tt.name, got, tt.want)
		}
	}
	for v, want := range map[string]string{"plain/value:1": "plain/value:1", "has space": "'has space'", "": ""} {
		if got, err := envQuote(v); err != nil || got != want {
			t.Errorf("envQuote(%q) = %q, %v", v, got, err)
		}
	}
	if _, err := envQuote("it's"); err == nil {
		t.Error("a value with a single quote was written")
	}
}

func TestValidNameAndNameOfHost(t *testing.T) {
	for _, s := range []string{"a", "fsn-2", "0abc", strings.Repeat("a", 63)} {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false", s)
		}
	}
	for _, s := range []string{"", "-a", "a-", "A", "a_b", "a.b", strings.Repeat("a", 64)} {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true", s)
		}
	}
	tests := []struct {
		host  string
		index int
		want  string
	}{
		{"Fsn-2.example.com", 2, "fsn-2"},
		{"my_host", 2, "my-host"},
		{"  HEL1 \n", 1, "hel1"},
		{"--x--", 3, "x"},
		{"", 4, "server-4"},
		{".example.com", 5, "server-5"},
		{"___", 6, "server-6"},
		{strings.Repeat("a", 70) + ".x", 7, strings.Repeat("a", 63)},
		{strings.Repeat("a", 62) + "_b", 7, strings.Repeat("a", 62)},
	}
	for _, tt := range tests {
		got := NameOfHost(tt.host, tt.index)
		if got != tt.want || !ValidName(got) {
			t.Errorf("NameOfHost(%q, %d) = %q, want %q", tt.host, tt.index, got, tt.want)
		}
	}
}

func TestPathsAreUnderTheRoot(t *testing.T) {
	a := &Agent{Root: "/tmp/root"}
	for _, p := range []string{stateFile, meshKey, nodeEnv, gossipKey, caKeyFile, caCert, agentCert, agentKey, nomadConf, nomadDataDir} {
		if got := a.path(p); !strings.HasPrefix(got, "/tmp/root/") {
			t.Errorf("path(%s) = %s", p, got)
		}
	}
}
