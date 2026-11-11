package nodeagent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

func (a *testAgent) writeState(t *testing.T, st State) (private string) {
	t.Helper()
	if err := a.SaveState(st); err != nil {
		t.Fatal(err)
	}
	priv, _, err := a.ensureMeshKey()
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestUpWithoutStateDoesNothing(t *testing.T) {
	a := newTestAgent(t, nil)
	if err := a.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.runner.calls) != 0 {
		t.Errorf("ran %v", a.runner.commands())
	}
}

func TestUpCommands(t *testing.T) {
	peers := []mesh.Peer{{Index: 1, PublicKey: peerKey1, Endpoint: "203.0.113.4:51820"}}
	tests := []struct {
		name  string
		state State
		want  []string
	}{
		{
			"index 1", State{Index: 1, Name: "hel-1", Pool: "default", First: true, Role: RoleServer, Servers: []int{1}},
			[]string{
				"ip link show dev caravana0",
				"ip link add caravana0 type dummy",
				"ip address replace 10.99.0.1/32 dev caravana0",
				"ip link set caravana0 up",
				"ip link show dev caravana-mesh",
				"ip link add caravana-mesh type wireguard",
				"ip address replace 10.98.0.1/32 dev caravana-mesh",
				"wg setconf caravana-mesh <conf>",
				"ip link set caravana-mesh up",
				"ip route replace 10.98.0.0/24 dev caravana-mesh",
				"ip route replace 10.99.0.0/24 dev caravana-mesh",
				"ip route replace 10.97.0.0/16 dev caravana-mesh",
			},
		},
		{
			"index 3", State{Index: 3, Name: "fsn-2", Pool: "default", Role: RoleClient, Servers: []int{1}, Peers: peers},
			[]string{
				"ip link show dev caravana0",
				"ip link add caravana0 type dummy",
				"ip address replace 10.99.0.3/32 dev caravana0",
				"ip link set caravana0 up",
				"ip link show dev caravana-mesh",
				"ip link add caravana-mesh type wireguard",
				"ip address replace 10.98.0.3/32 dev caravana-mesh",
				"wg setconf caravana-mesh <conf>",
				"ip link set caravana-mesh up",
				"ip route replace 10.98.0.0/24 dev caravana-mesh",
				"ip route replace 10.99.0.0/24 dev caravana-mesh",
				"ip route replace 10.97.0.0/16 dev caravana-mesh",
				"ip route replace 172.26.64.0/20 dev caravana-mesh",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAgent(t, nil)
			priv := a.writeState(t, tt.state)
			if err := a.Up(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := a.runner.commands(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}

			// The key is in the root-only temporary file, never in an argument, and the file is gone.
			wgs := a.runner.wgCalls()
			if len(wgs) != 1 {
				t.Fatalf("%d wg calls", len(wgs))
			}
			if !strings.Contains(wgs[0].conf, "PrivateKey = "+priv) || wgs[0].confMode != 0o600 {
				t.Errorf("config file mode %v, holds the key: %v", wgs[0].confMode, strings.Contains(wgs[0].conf, priv))
			}
			if strings.Contains(a.runner.allArgs(), priv) {
				t.Error("the private key is in a command's arguments")
			}
			if got := mesh.Config(priv, tt.state.Peers); wgs[0].conf != got {
				t.Errorf("the config text differs from mesh.Config:\n%s", wgs[0].conf)
			}
			if strings.Contains(wgs[0].args[2], priv) || !strings.HasPrefix(wgs[0].args[2], a.file("etc/caravana-node")+"/wg-") {
				t.Errorf("the config path is %s", wgs[0].args[2])
			}
			if a.exists(strings.TrimPrefix(wgs[0].args[2], a.root)) {
				t.Error("the temporary config file wasn't removed")
			}
		})
	}
}

// A second run finds the interfaces and makes none.
func TestUpIsIdempotent(t *testing.T) {
	a := newTestAgent(t, nil)
	a.writeState(t, State{Index: 2, Name: "fsn-1", Pool: "default", Role: RoleClient, Servers: []int{1}})
	if err := a.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := a.runner.commands()
	a.runner.reset()
	if err := a.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := a.runner.commands()
	for _, c := range second {
		if strings.Contains(c, "link add") {
			t.Errorf("the second run made an interface: %s", c)
		}
	}
	if len(second) != len(first)-2 {
		t.Errorf("second run has %d commands, first %d", len(second), len(first))
	}
}

func TestUpNeedsMeshKey(t *testing.T) {
	a := newTestAgent(t, nil)
	if err := a.SaveState(State{Index: 2, Name: "fsn-1", Pool: "default", Role: RoleClient, Servers: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Up(context.Background()); err == nil {
		t.Error("Up ran without mesh.key")
	}
}
