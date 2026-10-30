package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// nodeLab is a sampler lab whose node reports the lab's machine and whose
// Traefik runs v3.7.13.
func nodeLab(t *testing.T) *samplerLab {
	t.Helper()
	l := newSamplerLab(t, discardLogger())
	l.nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", []*api.NodeEvent{
		{Subsystem: "Cluster", Message: "Node registered", Timestamp: labBase.Add(-24 * time.Hour)},
	})
	l.nomad.SetTraefikJob("traefik:v3.7.13")
	return l
}

// setUptime makes the node report that it has been up for d.
func setUptime(l *samplerLab, d time.Duration) {
	stats := deploytest.LabHostStats()
	stats.Uptime = uint64(d / time.Second)
	l.nomad.SetHostStats(stats)
}

func bootEvents(t *testing.T, st *store.Store) []store.HostEvent {
	t.Helper()
	got, err := st.HostEvents(context.Background(), store.HostEventBoot, 50)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestHostNodeFromTheSampler(t *testing.T) {
	ctx := context.Background()
	l := nodeLab(t)
	setUptime(l, 8*24*time.Hour)

	if v, err := l.svc.Host(ctx); err != nil || v.Node != nil {
		t.Fatalf("Host before a tick: Node = %+v, %v; want nil", v.Node, err)
	}

	l.tick(t, 1)
	v, err := l.svc.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := v.Node
	if n == nil {
		t.Fatal("Host after a tick has no node")
	}
	if n.Name != "lab" || n.State != "ready" || n.Pool != "default" || n.OS != "ubuntu" || n.OSVersion != "26.04" ||
		n.Kernel != "7.0.0-14-generic" || n.Arch != "arm64" || n.NomadVersion != "2.0.1" || n.DockerVersion != "29.1.3" ||
		n.Cores != 2 || n.TotalMHz != 4000 || n.MemoryMB != 1949 || n.Address != "192.168.5.15" {
		t.Errorf("node = %+v", n)
	}
	if n.TraefikVersion != "v3.7.13" {
		t.Errorf("Traefik version = %q, want v3.7.13", n.TraefikVersion)
	}
	if len(n.Events) != 1 || n.Events[0].Message != "Node registered" {
		t.Errorf("events = %+v", n.Events)
	}
	want := time.Now().Add(-8 * 24 * time.Hour)
	if d := n.BootTime.Sub(want).Abs(); d > 10*time.Second {
		t.Errorf("boot time = %v, want about %v", n.BootTime, want)
	}

	// The view hands out a copy: changing it changes nothing in the sampler.
	n.Events[0].Message = "changed"
	if again, _ := l.svc.Host(ctx); again.Node.Events[0].Message != "Node registered" {
		t.Error("a caller changed the sampler's node")
	}

	// Traefik's job not readable keeps the last known version.
	l.nomad.Down()
	l.tick2(t, 2) // Nomad is down: the tick may fail to read everything but must not blank the node
	l.nomad.Up()
	if again, _ := l.svc.Host(ctx); again.Node == nil || again.Node.TraefikVersion != "v3.7.13" || again.Node.Kernel == "" {
		t.Errorf("node after a tick with Nomad down = %+v, want the last known facts", again.Node)
	}
}

// tick2 is tick for a tick that may report errors from its sources (it never
// fails the test): the sampler writes what it could read.
func (l *samplerLab) tick2(t *testing.T, n int) {
	t.Helper()
	_ = l.svc.sampleTick(context.Background(), labBase.Add(time.Duration(n)*time.Minute+2*time.Second))
}

func TestHostBootEvents(t *testing.T) {
	l := nodeLab(t)
	notices, stop := l.svc.Subscribe("")
	defer stop()

	// The first boot ever seen is recorded.
	setUptime(l, 5*time.Hour)
	l.tick(t, 1)
	got := bootEvents(t, l.store)
	if len(got) != 1 {
		t.Fatalf("boot events after the first tick = %+v, want one", got)
	}
	first := got[0].At
	if d := first.Sub(time.Now().Add(-5 * time.Hour)).Abs(); d > 10*time.Second {
		t.Errorf("boot recorded at %v, want about 5 hours ago", first)
	}
	if got[0].Detail != "{}" {
		t.Errorf("boot detail = %q", got[0].Detail)
	}
	sawHost := false
drain:
	for {
		select {
		case c := <-notices:
			sawHost = sawHost || c.What == ChangeHost
		default:
			break drain
		}
	}
	if !sawHost {
		t.Error("no ChangeHost notice after a boot event")
	}

	// A drift of a few seconds between readings is the same boot.
	for i, up := range []time.Duration{5*time.Hour + 3*time.Second, 5*time.Hour - 4*time.Second, 5*time.Hour + 59*time.Second} {
		setUptime(l, up)
		l.tick(t, 2+i)
	}
	if got := bootEvents(t, l.store); len(got) != 1 {
		t.Errorf("boot events after readings within a minute = %+v, want still one", got)
	}

	// A restart of Caravana loses what the tick remembers: the stored event is
	// the reference, and the same boot is not recorded again.
	l.svc.sampler.bootRef = time.Time{}
	l.tick(t, 6)
	if got := bootEvents(t, l.store); len(got) != 1 {
		t.Errorf("boot events after a Caravana restart = %+v, want still one", got)
	}

	// A reboot moves the boot time by hours: a second event.
	setUptime(l, 40*time.Second)
	l.tick(t, 7)
	got = bootEvents(t, l.store)
	if len(got) != 2 {
		t.Fatalf("boot events after a reboot = %+v, want two", got)
	}
	if got[0].At.Sub(first) < 4*time.Hour {
		t.Errorf("the newest boot is at %v, first at %v; want hours later", got[0].At, first)
	}

	// A node that reports no uptime records nothing and keeps the last boot time.
	l.nomad.SetHostStats(deploytest.LabHostStats())
	l.tick(t, 8)
	if got := bootEvents(t, l.store); len(got) != 2 {
		t.Errorf("boot events after a reading with no uptime = %+v, want two", got)
	}
}

func TestHostNodeDown(t *testing.T) {
	ctx := context.Background()
	l := nodeLab(t)
	setUptime(l, time.Hour)
	l.tick(t, 1)
	before, _ := l.svc.Host(ctx)

	// Nomad lists the node as down: its stats can't be read, its record can.
	l.nomad.SetNode("node-1", "lab", "down", deploytest.LabNodeResources(), nil)
	l.nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", nil)
	l.tick2(t, 2)

	v, err := l.svc.Host(ctx)
	if err != nil || v.Node == nil {
		t.Fatalf("Host = %+v, %v", v.Node, err)
	}
	if v.Node.State != "down" || v.Node.Name != "lab" || v.Node.Kernel != "7.0.0-14-generic" {
		t.Errorf("down node = %+v", v.Node)
	}
	if !v.Node.BootTime.Equal(before.Node.BootTime) || v.Node.TraefikVersion != "v3.7.13" {
		t.Errorf("down node lost its boot time %v (was %v) or Traefik version %q", v.Node.BootTime, before.Node.BootTime, v.Node.TraefikVersion)
	}
	if got := bootEvents(t, l.store); len(got) != 1 {
		t.Errorf("a down node recorded boot events %+v, want only the first", got)
	}
}

func TestHostNodeTokenWithoutNodeRead(t *testing.T) {
	ctx := context.Background()
	l := nodeLab(t)
	setUptime(l, time.Hour)
	l.nomad.FailNodeReads(403)
	l.tick2(t, 1)
	if v, _ := l.svc.Host(ctx); v.Node != nil {
		t.Errorf("node = %+v with node:read denied, want nil", v.Node)
	}
	if got := l.svc.SamplerSources(); got.Nomad != SamplerDenied {
		t.Errorf("Nomad source = %s, want denied", got.Nomad)
	}
}

func versionEvents(t *testing.T, st *store.Store) []struct{ From, To string } {
	t.Helper()
	events, err := st.HostEvents(context.Background(), store.HostEventVersion, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []struct{ From, To string }
	for i := len(events) - 1; i >= 0; i-- { // oldest first
		var d struct{ From, To string }
		if err := json.Unmarshal([]byte(events[i].Detail), &d); err != nil {
			t.Fatalf("version detail %q: %v", events[i].Detail, err)
		}
		out = append(out, d)
	}
	return out
}

func TestRecordVersion(t *testing.T) {
	ctx := context.Background()
	l := newSamplerLab(t, discardLogger())
	clock := l.svc.clock.(*fakeClock)
	notices, stop := l.svc.Subscribe("")
	defer stop()

	type v struct{ From, To string }
	check := func(step string, want ...v) {
		t.Helper()
		got := versionEvents(t, l.store)
		if len(got) != len(want) {
			t.Fatalf("%s: version events = %+v, want %+v", step, got, want)
		}
		for i := range got {
			if got[i].From != want[i].From || got[i].To != want[i].To {
				t.Fatalf("%s: version events = %+v, want %+v", step, got, want)
			}
		}
	}

	l.svc.recordVersion(ctx) // no version set: nothing
	check("unset")

	l.svc.SetVersion("0.1.0")
	l.svc.recordVersion(ctx)
	check("first start", v{"", "0.1.0"})
	select {
	case c := <-notices:
		if c.What != ChangeHost {
			t.Errorf("notice = %+v, want ChangeHost", c)
		}
	case <-time.After(time.Second):
		t.Error("no ChangeHost notice after a version event")
	}

	clock.Advance(time.Hour)
	l.svc.recordVersion(ctx) // the same version again
	check("same version", v{"", "0.1.0"})

	clock.Advance(time.Hour)
	l.svc.SetVersion("0.2.0")
	l.svc.recordVersion(ctx)
	check("upgrade", v{"", "0.1.0"}, v{"0.1.0", "0.2.0"})

	// A rollback to a version seen before is a change too.
	clock.Advance(time.Hour)
	l.svc.SetVersion("0.1.0")
	l.svc.recordVersion(ctx)
	check("rollback", v{"", "0.1.0"}, v{"0.1.0", "0.2.0"}, v{"0.2.0", "0.1.0"})
}

// Start records the running version before anything else runs.
func TestStartRecordsTheVersion(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	svc := New(st, newDeployer(t, e.registry, e.nomad), e.secrets, discardLogger())
	svc.SetVersion("1.2.3")
	svc.metricsGap = time.Hour
	svc.scheduleGap, svc.scheduleScanGap, svc.scheduleWatchGap = time.Hour, time.Hour, time.Hour
	svc.domainIdleGap, svc.domainWatchGap = time.Hour, time.Hour
	svc.notifyGap, svc.senderGap = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); svc.Wait() })

	got := versionEvents(t, st)
	if len(got) != 1 || got[0].To != "1.2.3" {
		t.Errorf("version events after Start = %+v, want one to 1.2.3", got)
	}
}
