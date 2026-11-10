package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

func TestParsers(t *testing.T) {
	free, total := parseDF("Filesystem     1M-blocks  Used Available Capacity Mounted on\n/dev/vda1         190000 30000    160000      16% /\n")
	if free != 160000 || total != 190000 {
		t.Errorf("parseDF = %d, %d", free, total)
	}
	for _, bad := range []string{"", "only a header\n", "h\nshort row\n", "h\nx y z w v u\n"} {
		if f, tt := parseDF(bad); f != 0 || tt != 0 {
			t.Errorf("parseDF(%q) = %d, %d", bad, f, tt)
		}
	}
	for in, want := range map[string]string{
		"Nomad v2.0.7\nBuildDate 2026\n": "2.0.7",
		"Nomad v2.0.7+ent\n":             "2.0.7",
		"Nomad v1.9.3 (abcdef)":          "1.9.3",
		"":                               "",
	} {
		if got := parseNomadVersion(in); got != want {
			t.Errorf("parseNomadVersion(%q) = %q, want %q", in, got, want)
		}
	}
	if got := parseMemTotalMB("MemFree: 1 kB\nMemTotal:        4010116 kB\n"); got != 3916 {
		t.Errorf("parseMemTotalMB = %d", got)
	}
	if parseMemTotalMB("nothing") != 0 || parseMemTotalMB("MemTotal: x kB") != 0 {
		t.Error("parseMemTotalMB accepted junk")
	}
}

func TestReport(t *testing.T) {
	a := newTestAgent(t, nil)
	// A real data directory, so df is asked about it; a memory file under the root.
	a.writeFiles(t, map[string]string{"opt/nomad/data/.keep": "", "proc/meminfo": "MemTotal:        4010116 kB\n"})
	st := State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleServer, Servers: []int{1, 3},
		Peers: []mesh.Peer{{Index: 2, PublicKey: peerKey2, Endpoint: "198.51.100.7:51820"}, {Index: 1, PublicKey: peerKey1, Endpoint: "203.0.113.4:51820"}}}
	a.runner.pingFail["10.98.0.2"] = true
	a.nomad.peers = []string{"10.98.0.1:4647", "10.98.0.3:4647"}

	r := a.Report(context.Background(), st)
	if r.Agent != "0.18.0" || r.Nomad != "2.0.7" || r.Docker != "27.5.1" || !r.DockerRunning ||
		r.DiskFreeMB != 160000 || r.DiskTotalMB != 190000 || r.MemoryMB != 3916 || r.Kernel != "6.8.0-134-generic" ||
		r.Role != "server" || !r.NomadLeader || !r.ServerRunning {
		t.Errorf("report = %+v", r)
	}
	if len(r.Reach) != 2 || r.Reach[0] != (Reach{1, true}) || r.Reach[1] != (Reach{2, false}) {
		t.Errorf("reach = %+v", r.Reach)
	}
	if r.CertificateNotAfter != "" {
		t.Errorf("a certificate end without a certificate: %s", r.CertificateNotAfter)
	}

	// The df call asked about the data directory under the root.
	var asked string
	for _, c := range a.runner.calls {
		if c.name == "df" {
			asked = c.args[len(c.args)-1]
		}
	}
	if asked != a.file("opt/nomad/data") {
		t.Errorf("df asked about %q", asked)
	}

	// Its JSON has the fields of api.md §4.
	b, _ := json.Marshal(r)
	for _, f := range []string{"agent", "nomad", "docker", "dockerRunning", "diskFreeMb", "diskTotalMb", "memoryMb", "kernel", "role", "nomadLeader", "serverRunning", "reach"} {
		if !strings.Contains(string(b), `"`+f+`"`) {
			t.Errorf("the report has no %q: %s", f, b)
		}
	}
}

func TestReportWhenThingsAreMissing(t *testing.T) {
	a := newTestAgent(t, nil)
	a.nomad.srv.Close() // no local Nomad
	st := State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleServer, Servers: []int{1, 3}}
	r := a.Report(context.Background(), st)
	if r.NomadLeader || r.ServerRunning || len(r.Reach) != 0 {
		t.Errorf("report = %+v", r)
	}
	// A server that isn't among Nomad's peers isn't running.
	b := newTestAgent(t, nil)
	b.nomad.peers = []string{"10.98.0.1:4647"}
	if r := b.Report(context.Background(), st); r.ServerRunning || !r.NomadLeader {
		t.Errorf("report = %+v", r)
	}
	// No leader known.
	b.nomad.leader = ""
	if r := b.Report(context.Background(), st); r.NomadLeader {
		t.Error("an empty leader is a leader")
	}
}

// Peers are pinged eight at a time, not all at once and not one by one.
func TestReportPingsEightAtATime(t *testing.T) {
	a := newTestAgent(t, nil)
	var peers []mesh.Peer
	for i := 1; i <= 20; i++ {
		peers = append(peers, mesh.Peer{Index: i + 1, PublicKey: peerKey1, Endpoint: fmt.Sprintf("203.0.113.%d:51820", i)})
	}
	r := a.Report(context.Background(), State{Version: 1, Index: 1, Name: "a", Pool: "default", Role: RoleClient, Peers: peers})
	if len(r.Reach) != 20 {
		t.Fatalf("%d results", len(r.Reach))
	}
	for i, x := range r.Reach {
		if x.Index != i+2 || !x.OK {
			t.Errorf("reach[%d] = %+v", i, x)
		}
	}
	if a.runner.maxPings > 8 || a.runner.maxPings < 2 {
		t.Errorf("up to %d pings at once", a.runner.maxPings)
	}
	// One ping: -c 1 -W 2 and the mesh address.
	for _, c := range a.runner.calls {
		if c.name == "ping" && (len(c.args) != 5 || c.args[0] != "-c" || c.args[1] != "1" || c.args[2] != "-W" || c.args[3] != "2") {
			t.Errorf("ping args = %v", c.args)
		}
	}
}
