package nodeagent

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const testIssuer = "https://console.example.com"

const testGossip = "dGVzdC1nb3NzaXAta2V5LTMyLWJ5dGVzLWxvbmchIQ=="

func renderCases() map[string]struct {
	state State
	key   string
} {
	return map[string]struct {
		state State
		key   string
	}{
		"first":              {State{Version: 1, Index: 1, Name: "hel-1", Pool: "default", First: true, Role: RoleServer, Servers: []int{1}}, testGossip},
		"first-with-servers": {State{Version: 1, Index: 1, Name: "hel-1", Pool: "default", First: true, Role: RoleServer, Servers: []int{3, 1, 2}}, testGossip},
		"client":             {State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleClient, Servers: []int{1}}, testGossip},
		"server":             {State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleServer, Servers: []int{1, 2, 3}}, testGossip},
		"first-issuer":       {State{Version: 1, Index: 1, Name: "hel-1", Pool: "default", First: true, Role: RoleServer, Servers: []int{1}, Issuer: testIssuer}, testGossip},
		"server-issuer":      {State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleServer, Servers: []int{1, 2, 3}, Issuer: testIssuer}, testGossip},
		"client-cpu":         {State{Version: 1, Index: 2, Name: "fsn-1", Pool: "builders", Role: RoleClient, Servers: []int{1}, CPUTotalCompute: 4000}, testGossip},
	}
}

func TestRenderNomadGolden(t *testing.T) {
	for name, c := range renderCases() {
		t.Run(name, func(t *testing.T) {
			got := RenderNomad(c.state, c.key)
			golden := filepath.Join("testdata", name+".golden.hcl")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("RenderNomad differs from %s:\n%s", golden, got)
			}
		})
	}
}

// The gossip key is written on a Nomad server only.
func TestRenderNomadGossipOnlyOnServers(t *testing.T) {
	for name, c := range renderCases() {
		got := RenderNomad(c.state, c.key)
		if has := strings.Contains(got, testGossip); has != (c.state.Role == RoleServer) {
			t.Errorf("%s: gossip key present = %v for role %s", name, has, c.state.Role)
		}
	}
}

func TestRenderNomadShape(t *testing.T) {
	cases := renderCases()
	first := RenderNomad(cases["first"].state, testGossip)
	for _, want := range []string{"bootstrap_expect = 1", `caravana_ingress = "true"`, `host_volume "traefik-acme"`} {
		if !strings.Contains(first, want) {
			t.Errorf("the first server's file has no %q", want)
		}
	}
	for _, not := range []string{"server_join", "bridge_network_subnet", "\nname ", "servers  "} {
		if strings.Contains(first, not) {
			t.Errorf("the lone first server's file has %q", not)
		}
	}
	if !strings.Contains(RenderNomad(cases["first-with-servers"].state, testGossip), `retry_join = ["10.98.0.2:4648", "10.98.0.3:4648"]`) {
		t.Error("the first server doesn't retry_join the others once there are some")
	}
	client := RenderNomad(cases["client"].state, testGossip)
	for _, want := range []string{`name       = "fsn-2"`, `bridge_network_subnet = "10.97.12.0/22"`, `servers               = ["10.98.0.1:4647"]`} {
		if !strings.Contains(client, want) {
			t.Errorf("the client's file has no %q", want)
		}
	}
	for _, not := range []string{"\nserver {", "caravana_ingress", "traefik-acme", "cpu_total_compute"} {
		if strings.Contains(client, not) {
			t.Errorf("the client's file has %q", not)
		}
	}
	if !strings.Contains(RenderNomad(cases["client-cpu"].state, ""), "cpu_total_compute     = 4000") {
		t.Error("cpu_total_compute missing")
	}
	server := RenderNomad(cases["server"].state, testGossip)
	if strings.Contains(server, "bootstrap_expect") || !strings.Contains(server, `retry_join = ["10.98.0.1:4648", "10.98.0.2:4648"]`) {
		t.Errorf("a joined Nomad server's server block is wrong:\n%s", server)
	}
	if got := RenderNomad(State{Index: 99}, ""); !strings.HasPrefix(got, "# caravana node:") {
		t.Errorf("a bad index renders a real file: %q", got)
	}
}

// What the old template set on a first server is still set: the client settings, the acl, docker
// and telemetry blocks. The old template goes away with the installer's rewrite (phase D); this
// test then has nothing to compare and skips.
func TestRenderNomadFirstMatchesTodaysTemplate(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("..", "..", "deploy", "install", "nomad.hcl.tpl"))
	if err != nil {
		t.Skipf("deploy/install/nomad.hcl.tpl is gone: %v", err)
	}
	got := RenderNomad(renderCases()["first"].state, testGossip)
	for _, header := range []string{`acl {`, `plugin "docker" {`, `telemetry {`, `host_volume "traefik-acme" {`} {
		if a, b := normBlock(string(old), header), normBlock(got, header); a == "" || a != b {
			t.Errorf("block %q differs:\n old: %q\n new: %q", header, a, b)
		}
	}
	for _, line := range []string{`cni_path = "/opt/cni/bin"`, `network_interface = "caravana0"`, `datacenter = "dc1"`, `data_dir = "/opt/nomad/data"`, `bind_addr = "127.0.0.1"`} {
		if !strings.Contains(norm(string(old)), line) || !strings.Contains(norm(got), line) {
			t.Errorf("setting %q isn't in both", line)
		}
	}
	// Every comment of the old file that explains a setting is carried over.
	for _, phrase := range []string{
		"DNAT rules that a host firewall can't block",
		"Traefik keeps its Let's Encrypt certificates here",
		"Nomad's stats hook doesn't collect task stats unless this is set",
		"No metrics sink is configured",
	} {
		if !strings.Contains(string(old), phrase) || !strings.Contains(got, phrase) {
			t.Errorf("comment %q isn't in both", phrase)
		}
	}
}

var spaces = regexp.MustCompile(`\s+`)

func norm(s string) string { return spaces.ReplaceAllString(s, " ") }

// normBlock is the block that starts with header, braces matched, whitespace squeezed ("" if none).
func normBlock(s, header string) string {
	i := strings.Index(s, header)
	if i < 0 {
		return ""
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return norm(s[i : j+1])
			}
		}
	}
	return ""
}

// A Nomad server's own client must not list servers: it would heartbeat to a listed server, and when
// that one is lost the node is marked down and its copies are replaced (cluster test, 2026-11-10). Only
// a pure client lists the Nomad servers.
func TestOnlyAClientListsServers(t *testing.T) {
	listed := regexp.MustCompile(`(?m)^\s*servers\s*=`)
	for name, c := range renderCases() {
		got := listed.MatchString(RenderNomad(c.state, c.key))
		want := c.state.Role == RoleClient
		if got != want {
			t.Errorf("%s: client.servers listed = %v, want %v", name, got, want)
		}
	}
}
