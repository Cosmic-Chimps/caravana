package nodeagent

import (
	"strings"
	"testing"
)

// Spec 041: the first server declares the host network "public" on the interface of its default
// route, and no other server does.

func TestInitFirstDetectsThePublicInterface(t *testing.T) {
	a := newTestAgent(t, nil)
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4"}); err != nil {
		t.Fatal(err)
	}
	st, err := a.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.PublicInterface != "eth0" {
		t.Errorf("PublicInterface = %q, want eth0", st.PublicInterface)
	}
	hcl := a.read(t, "etc/nomad.d/nomad.hcl")
	if !strings.Contains(hcl, `host_network "public" {`) || !strings.Contains(hcl, `interface = "eth0"`) {
		t.Errorf("nomad.hcl has no public host network:\n%s", hcl)
	}

	// A host whose route changed gets the new interface, and one with no route keeps the old.
	a.runner.defaultRoute = "default via 192.0.2.1 dev ens3 proto static\ndefault via 10.0.0.1 dev wlan0 metric 600\n"
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4"}); err != nil {
		t.Fatal(err)
	}
	if st, _ = a.LoadState(); st.PublicInterface != "ens3" {
		t.Errorf("after a new route PublicInterface = %q, want ens3 (the first line)", st.PublicInterface)
	}
	a.runner.defaultRoute = ""
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4"}); err != nil {
		t.Fatal(err)
	}
	if st, _ = a.LoadState(); st.PublicInterface != "ens3" {
		t.Errorf("with no route PublicInterface = %q, want ens3 kept", st.PublicInterface)
	}
}

func TestInitFirstWithoutADefaultRoute(t *testing.T) {
	a := newTestAgent(t, nil)
	a.runner.defaultRoute = ""
	if err := a.InitFirst(ctx(), InitFirstOptions{Address: "203.0.113.4"}); err != nil {
		t.Fatal(err)
	}
	if hcl := a.read(t, "etc/nomad.d/nomad.hcl"); strings.Contains(hcl, "host_network") {
		t.Errorf("nomad.hcl declares a host network without a route:\n%s", hcl)
	}
}

func TestDefaultRouteInterfaceReading(t *testing.T) {
	tests := []struct{ name, out, want string }{
		{"usual", "default via 192.0.2.1 dev eth0 proto dhcp src 192.0.2.5 metric 100\n", "eth0"},
		{"no route", "", ""},
		{"no dev word", "default via 192.0.2.1\n", ""},
		{"a name Linux doesn't allow", "default via 192.0.2.1 dev a-name-over-fifteen-characters\n", ""},
		{"a name with a quote", "default via 192.0.2.1 dev eth\"0\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAgent(t, nil)
			a.runner.defaultRoute = tt.out
			if got := a.defaultRouteInterface(ctx()); got != tt.want {
				t.Errorf("= %q, want %q", got, tt.want)
			}
		})
	}
}

// Only the first server renders the network, whatever its state says.
func TestPublicNetworkOnlyOnTheFirstServer(t *testing.T) {
	cases := renderCases()
	if got := RenderNomad(cases["first-public"].state, testGossip); !strings.Contains(got, `host_network "public"`) {
		t.Error("the first server doesn't declare the public network")
	}
	if got := RenderNomad(cases["client-public"].state, testGossip); strings.Contains(got, "host_network") {
		t.Errorf("a joined server declares a host network:\n%s", got)
	}
	if got := RenderNomad(cases["first"].state, testGossip); strings.Contains(got, "host_network") {
		t.Error("a state with no interface declares a host network")
	}
}

func TestStateValidatePublicInterface(t *testing.T) {
	st := renderCases()["first"].state
	for iface, ok := range map[string]bool{"": true, "eth0": true, "enp0s3.100": true, "br_x-1": true,
		"a-name-over-fifteen-characters": false, `eth"0`: false, "eth 0": false, "eth0\n": false} {
		st.PublicInterface = iface
		if err := st.Validate(); (err == nil) != ok {
			t.Errorf("Validate with interface %q = %v, want ok=%v", iface, err, ok)
		}
	}
}
