package deploy

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

func TestHostNodeFacts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 12, 0, 30, 0, time.UTC)

	t.Run("an ARM node has no CPU model and no per-core speed", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		d.cache.now = func() time.Time { return now }
		labNode(nomad, deploytest.LabHostStats())
		events := []*api.NodeEvent{{Subsystem: "Cluster", Message: "Node registered", Timestamp: now.Add(-48 * time.Hour)}}
		nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", events)
		nomad.SetNodeNetworks(deploytest.Caravana0Networks("10.99.0.1"))

		h, err := d.HostStats(ctx, "", "/var/lib/docker")
		if err != nil {
			t.Fatal(err)
		}
		want := NodeFacts{
			Name: "lab", State: "ready", Pool: "default",
			OS: "ubuntu", OSVersion: "26.04", Kernel: "7.0.0-14-generic", Arch: "arm64",
			NomadVersion: "2.0.1", DockerVersion: "29.1.3",
			Cores: 2, TotalMHz: 4000, MemoryMB: 1949, Address: "10.99.0.1",
			LastHeartbeat: now,
			Events:        []NodeEvent{{Time: now.Add(-48 * time.Hour), Subsystem: "Cluster", Message: "Node registered"}},
		}
		if !reflect.DeepEqual(h.Node, want) {
			t.Errorf("Node = %+v\nwant %+v", h.Node, want)
		}
		if h.Node.CPUModel != "" || h.Node.CoreMHz != 0 {
			t.Errorf("an ARM node's model %q and speed %d must stay empty", h.Node.CPUModel, h.Node.CoreMHz)
		}
	})

	t.Run("a node on eth0 only (the lab) has no private address, though Nomad has an ip-address attribute", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", nil)
		nomad.SetNodeNetworks(deploytest.LabNodeNetworks())
		if got := deploytest.LabNodeAttributes()["unique.network.ip-address"]; got != "192.168.5.15" {
			t.Fatalf("the fixture's attribute = %q", got)
		}
		h, err := d.HostStats(ctx, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if h.Node.Address != "" {
			t.Errorf("Address = %q, want none: eth0 is not caravana0", h.Node.Address)
		}
	})

	t.Run("an x86 node has both", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		nomad.SetNodeFacts(deploytest.X86NodeAttributes(), "default", nil)
		nomad.SetNodeNetworks(deploytest.Caravana0Networks("10.99.0.1"))
		h, err := d.HostStats(ctx, "", "")
		if err != nil {
			t.Fatal(err)
		}
		n := h.Node
		if n.CPUModel != "AMD EPYC 7763 64-Core Processor" || n.CoreMHz != 2400 || n.Cores != 4 || n.TotalMHz != 9600 ||
			n.Arch != "amd64" || n.OSVersion != "24.04" || n.Address != "10.99.0.1" {
			t.Errorf("Node = %+v", n)
		}
	})

	t.Run("a node with no attributes shows only what is known", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		h, err := d.HostStats(ctx, "", "")
		if err != nil {
			t.Fatal(err)
		}
		n := h.Node
		if n.OS != "" || n.Kernel != "" || n.NomadVersion != "" || n.Address != "" || n.CPUModel != "" {
			t.Errorf("Node = %+v, want the attributes empty", n)
		}
		// What the resources say still fills the numbers.
		if n.Cores != 2 || n.TotalMHz != 4000 || n.MemoryMB != 1949 || n.Name != "lab" {
			t.Errorf("Node = %+v, want cores, MHz, memory and name from the resources", n)
		}
	})

	t.Run("states: drain and ineligibility come from the list, not the cached record", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		h, _ := d.HostStats(ctx, "", "")
		if h.Node.State != "ready" {
			t.Fatalf("state = %q, want ready", h.Node.State)
		}
		nomad.SetNodeScheduling(false, "ineligible")
		if h, _ = d.HostStats(ctx, "", ""); h.Node.State != "ineligible" {
			t.Errorf("state = %q, want ineligible", h.Node.State)
		}
		nomad.SetNodeScheduling(true, "ineligible")
		if h, _ = d.HostStats(ctx, "", ""); h.Node.State != "draining" {
			t.Errorf("state = %q, want draining", h.Node.State)
		}
		nomad.SetNodeScheduling(false, "eligible")
		if h, _ = d.HostStats(ctx, "", ""); h.Node.State != "ready" {
			t.Errorf("state = %q, want ready again", h.Node.State)
		}
	})
}

func TestBootTime(t *testing.T) {
	read := time.Date(2026, 10, 30, 12, 0, 30, 400_000_000, time.UTC)
	tests := []struct {
		uptime uint64
		want   time.Time
	}{
		{0, time.Date(2026, 10, 30, 12, 0, 30, 0, time.UTC)},
		{30, time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)},
		{8 * 24 * 3600, time.Date(2026, 10, 22, 12, 0, 30, 0, time.UTC)},
	}
	for _, tt := range tests {
		if got := bootTime(read, tt.uptime); !got.Equal(tt.want) {
			t.Errorf("bootTime(uptime %d) = %v, want %v", tt.uptime, got, tt.want)
		}
	}

	d, _, nomad := newTestDeployer(t)
	d.cache.now = func() time.Time { return read }
	stats := deploytest.LabHostStats()
	stats.Uptime = 86400
	labNode(nomad, stats)
	h, err := d.HostStats(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 29, 12, 0, 30, 0, time.UTC); !h.BootTime.Equal(want) {
		t.Errorf("Host.BootTime = %v, want %v", h.BootTime, want)
	}
	// No uptime from Nomad: no boot time, not the Unix epoch plus the clock.
	labNode(nomad, deploytest.LabHostStats())
	if h, _ = d.HostStats(context.Background(), "", ""); !h.BootTime.IsZero() {
		t.Errorf("Host.BootTime = %v without an uptime, want zero", h.BootTime)
	}
}

func TestNodeFactsOfADownNode(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	if _, err := d.NodeFacts(ctx); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("no node: err = %v, want ErrNomadUnavailable", err)
	}
	nomad.SetNode("node-1", "lab", "down", deploytest.LabNodeResources(), nil)
	nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", nil)
	nomad.SetHostStats(deploytest.LabHostStats())

	// The stats of a down node can't be read: HostStats fails, NodeFacts doesn't.
	if _, err := d.HostStats(ctx, "", ""); !errors.Is(err, ErrNomadUnavailable) {
		t.Fatalf("HostStats of a down node: err = %v", err)
	}
	f, err := d.NodeFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.State != "down" || f.Name != "lab" || f.Kernel != "7.0.0-14-generic" || f.Cores != 2 {
		t.Errorf("down node = %+v", f)
	}

	// Nomad's own time for the change of status is the node's StatusAt, even when the record was cached while it was up.
	d2, _, nomad2 := newTestDeployer(t)
	nomad2.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	nomad2.SetNodeFacts(deploytest.LabNodeAttributes(), "default", nil)
	nomad2.SetHostStats(deploytest.LabHostStats())
	if h, err := d2.HostStats(ctx, "", ""); err != nil || !h.Node.StatusAt.IsZero() {
		t.Fatalf("a ready node with no status time: %+v, %v", h.Node.StatusAt, err)
	}
	lost := time.Date(2026, 10, 30, 14, 2, 0, 0, time.UTC)
	nomad2.SetNode("node-1", "lab", "down", deploytest.LabNodeResources(), nil)
	nomad2.SetNodeFacts(deploytest.LabNodeAttributes(), "default", nil)
	nomad2.SetNodeStatusAt(lost)
	if f2, err := d2.NodeFacts(ctx); err != nil || f2.State != "down" || !f2.StatusAt.Equal(lost) {
		t.Errorf("a node that went down after its record was cached: StatusAt = %v, %v; want %v", f2.StatusAt, err, lost)
	}

	nomad.FailNodeReads(403)
	if _, err := d.NodeFacts(ctx); !errors.Is(err, ErrNodeReadDenied) {
		t.Errorf("a token without node:read: err = %v, want ErrNodeReadDenied", err)
	}
}

func TestNodeState(t *testing.T) {
	tests := []struct {
		name string
		node api.Node
		want string
	}{
		{"ready", api.Node{Status: "ready", SchedulingEligibility: "eligible"}, "ready"},
		{"no eligibility given", api.Node{Status: "ready"}, "ready"},
		{"down wins over a drain", api.Node{Status: "down", Drain: true, SchedulingEligibility: "ineligible"}, "down"},
		{"initializing", api.Node{Status: "initializing"}, "initializing"},
		{"draining by flag", api.Node{Status: "ready", Drain: true}, "draining"},
		{"draining by strategy", api.Node{Status: "ready", DrainStrategy: &api.DrainStrategy{}}, "draining"},
		{"ineligible", api.Node{Status: "ready", SchedulingEligibility: "ineligible"}, "ineligible"},
	}
	for _, tt := range tests {
		if got := nodeState(&tt.node); got != tt.want {
			t.Errorf("%s: nodeState = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTraefikImage(t *testing.T) {
	ctx := context.Background()

	t.Run("the image of the traefik job, kept for 10 minutes", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		clock := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		d.cache.now = func() time.Time { return clock }
		nomad.SetTraefikJob("traefik:v3.7.13")
		got, err := d.TraefikImage(ctx)
		if err != nil || got != "traefik:v3.7.13" {
			t.Fatalf("TraefikImage() = %q, %v", got, err)
		}
		reads := nomad.JobInfoRequests()
		nomad.SetTraefikJob("traefik:v3.8.0") // a new job version
		if got, _ = d.TraefikImage(ctx); got != "traefik:v3.7.13" || nomad.JobInfoRequests() != reads {
			t.Errorf("within 10 minutes: %q after %d reads (was %d), want the cached answer", got, nomad.JobInfoRequests(), reads)
		}
		clock = clock.Add(11 * time.Minute)
		if got, _ = d.TraefikImage(ctx); got != "traefik:v3.8.0" {
			t.Errorf("after 10 minutes: %q, want traefik:v3.8.0", got)
		}
	})

	t.Run("no traefik job is an empty image, not an error", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if got, err := d.TraefikImage(ctx); err != nil || got != "" {
			t.Errorf("TraefikImage() = %q, %v; want empty and no error", got, err)
		}
	})

	t.Run("Nomad down is an error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		if _, err := d.TraefikImage(ctx); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("err = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestImageVersion(t *testing.T) {
	tests := []struct{ ref, want string }{
		{"traefik:v3.7.13", "v3.7.13"},
		{"traefik:v3.7.13@sha256:abc", "v3.7.13"},
		{"traefik", ""},
		{"traefik@sha256:abc", ""},
		{"localhost:5000/traefik", ""},
		{"localhost:5000/traefik:v3", "v3"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ImageVersion(tt.ref); got != tt.want {
			t.Errorf("ImageVersion(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}
