package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The host's numbers with several servers (feature 031, tasks T020 to T025): the sampler writes a row
// for each ready node, Host reads one server's, the room is the cluster's, and a disk item is one per
// server and mount.

// serversLab is a cluster of two ready servers ("hel-1", the one Caravana runs on, and "two") whose
// nodes both report stats, on a sampler that ticks when the test says so.
type serversLab struct {
	*clusterEnv
	now time.Time
}

// statsAt is a node's stats: memory used in MB and the root filesystem usedPercent full.
func statsAt(usedMemMB uint64, usedPercent int) api.HostStats {
	const size = 100 << 30
	used := uint64(size / 100 * usedPercent)
	root := api.HostDiskStats{Device: "/dev/vda1", Mountpoint: "/", Size: size, Used: used, Available: size - used}
	alloc := root
	alloc.Device, alloc.Mountpoint = "", ""
	return api.HostStats{
		Memory:           &api.HostMemoryStats{Total: 4096 << 20, Used: usedMemMB << 20},
		CPUTicksConsumed: 400,
		DiskStats:        []*api.HostDiskStats{&root},
		AllocDirStats:    &alloc,
	}
}

func newServersLab(t *testing.T) *serversLab {
	t.Helper()
	l := &serversLab{clusterEnv: newClusterEnv(t), now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	_, two := l.join(t, "two", "")
	l.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	if _, err := l.svc.NodeSync(context.Background(), two.NodeToken, healthyReport(1)); err != nil {
		t.Fatal(err)
	}
	l.nomad.SetHostStatsOf("node-1", statsAt(1000, 50))
	l.nomad.SetHostStatsOf("node-2", statsAt(2000, 50))
	l.svc.metricsGap = time.Minute
	l.svc.clock.(*fakeClock).Set(l.now)
	return l
}

// tick runs the sampler's tick n (as samplerLab.tick does) and moves the clock to it.
func (l *serversLab) tick(t *testing.T, n int) {
	t.Helper()
	at := labBase.Add(time.Duration(n)*time.Minute + 2*time.Second)
	l.svc.clock.(*fakeClock).Set(at)
	if err := l.svc.sampleTick(context.Background(), at); err != nil {
		t.Fatalf("tick %d: %v", n, err)
	}
}

func (l *serversLab) hostRowsAt(t *testing.T, ts time.Time) []store.HostSample {
	t.Helper()
	rows, err := l.store.HostRows(context.Background(), store.MinuteRes, ts, ts.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func nodesOf(rows []store.HostSample) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Node)
	}
	return out
}

func TestSamplerReadsEveryReadyNode(t *testing.T) {
	ctx := context.Background()
	l := newServersLab(t)
	l.tick(t, 1)

	if got := nodesOf(l.hostRowsAt(t, rowTS(1))); !slices.Equal(got, []string{"node-1", "node-2"}) {
		t.Fatalf("host rows of tick 1 are of %v, want both nodes", got)
	}
	disks, err := l.store.DiskRows(ctx, store.MinuteRes, rowTS(1), rowTS(1).Add(time.Second))
	if err != nil || len(disks) != 2 || disks[0].Node != "node-1" || disks[1].Node != "node-2" {
		t.Fatalf("disk rows = %+v, %v; want one for each node", disks, err)
	}

	// Host of each server is its own; the default is the one Caravana runs on.
	def, err := l.svc.Host(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	one, err := l.svc.Host(ctx, firstServerName)
	if err != nil {
		t.Fatal(err)
	}
	two, err := l.svc.Host(ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	if def.Server != firstServerName || one.Server != firstServerName || two.Server != "two" {
		t.Errorf("servers = %q, %q, %q", def.Server, one.Server, two.Server)
	}
	if *one.Memory.Used != 1000 || *two.Memory.Used != 2000 || *def.Memory.Used != 1000 {
		t.Errorf("memory used = %d (default), %d, %d; want 1000, 1000 and 2000", *def.Memory.Used, *one.Memory.Used, *two.Memory.Used)
	}
	if !def.OSRead || !one.OSRead || two.OSRead {
		t.Errorf("OSRead = %v, %v, %v; want only the server Caravana runs on", def.OSRead, one.OSRead, two.OSRead)
	}
	if one.Node == nil || two.Node != nil || two.Reboot != nil {
		t.Errorf("the node block is the local server's only: %+v and %+v", one.Node, two.Node)
	}
	if len(two.Disks) != 1 || two.Disks[0].UsedPercent < 49.9 || two.Disks[0].UsedPercent > 50.1 {
		t.Errorf("two's disks = %+v", two.Disks)
	}
	// The room is the cluster's, whichever server is looked at: both servers' 4096 MB.
	for name, v := range map[string]HostView{"default": def, "first": one, "two": two} {
		if !v.Room.Known || v.Room.FreeMemoryMB != 8192 {
			t.Errorf("%s: room = %+v, want the cluster's 8192 MB free", name, v.Room)
		}
	}

	// An unknown name.
	if _, err := l.svc.Host(ctx, "nope"); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("Host(nope) = %v, want ErrServerNotFound", err)
	}
	if _, err := l.svc.HostCopies(ctx, "nope"); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("HostCopies(nope) = %v, want ErrServerNotFound", err)
	}
}

// A node that is down, or whose stats can't be read, leaves its own row out and no other's.
func TestSamplerOneNodeDownLeavesTheOthersAlone(t *testing.T) {
	ctx := context.Background()
	l := newServersLab(t)
	l.tick(t, 1)

	l.nomad.SetNodeStatusOf("node-2", "down")
	l.tick(t, 2)
	if got := nodesOf(l.hostRowsAt(t, rowTS(2))); !slices.Equal(got, []string{"node-1"}) {
		t.Errorf("tick 2 wrote rows for %v, want only the node that is up", got)
	}
	two, err := l.svc.Host(ctx, "two")
	if err != nil || two.SampledAt == nil || !two.SampledAt.Equal(rowTS(1)) {
		t.Errorf("Host(two) = %+v, %v; want its last sample, from tick 1", two, err)
	}

	// A ready node whose stats fail: its row is missing, the rest are written, the tick doesn't fail.
	l.nomad.SetNodeStatusOf("node-2", "ready")
	l.nomad.AddNode("node-3", "three", "ready", "") // no stats: Nomad answers 404 for its client
	l.tick(t, 3)
	if got := nodesOf(l.hostRowsAt(t, rowTS(3))); !slices.Equal(got, []string{"node-1", "node-2"}) {
		t.Errorf("tick 3 wrote rows for %v, want the two that could be read", got)
	}
	// The sampler's outcome is about the server Caravana runs on.
	if src := l.svc.SamplerSources(); src.Nomad != SamplerOK {
		t.Errorf("Nomad's state = %q, want ok: the failing node is not the server Caravana runs on", src.Nomad)
	}
}

func TestHostCopiesOfAServer(t *testing.T) {
	ctx := context.Background()
	l := newServersLab(t)
	saveApp(t, l.env, appWith("blog", 1))
	run := api.AllocClientStatusRunning
	onNode := func(id, nodeID, nodeName string) api.AllocationListStub {
		s := hostStub(id, "web", run, api.AllocDesiredStatusRun, map[string][3]int{"web": {100, 64, 256}})
		s.NodeID, s.NodeName = nodeID, nodeName
		return s
	}
	l.nomad.SetCopies("blog", onNode("c-here", "node-1", firstServerName), onNode("c-there", "node-2", "two"))

	names := func(server string) []string {
		got, err := l.svc.HostCopies(ctx, server)
		if err != nil {
			t.Fatalf("HostCopies(%q): %v", server, err)
		}
		var out []string
		for _, c := range got {
			out = append(out, c.ID)
		}
		return out
	}
	if got := names(""); !slices.Equal(got, []string{"c-here"}) {
		t.Errorf("the default server's copies = %v", got)
	}
	if got := names("two"); !slices.Equal(got, []string{"c-there"}) {
		t.Errorf("two's copies = %v", got)
	}
}

// diskItemsOfCluster is the disk items of the needs-you list, by key.
func diskItemsOfCluster(t *testing.T, l *serversLab) (Attention, map[string]AttentionItem) {
	t.Helper()
	att, err := l.svc.Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]AttentionItem{}
	for _, it := range att.Items {
		if it.Kind == AttentionDiskNearlyFull {
			out[problemKey(it)] = it
		}
	}
	return att, out
}

func TestDiskItemsPerServer(t *testing.T) {
	l := newServersLab(t)
	l.nomad.SetHostStatsOf("node-2", statsAt(2000, 90))
	l.tick(t, 1)

	att, items := diskItemsOfCluster(t, l)
	if att.DisksUnknown || len(att.DisksUnjudged) != 0 {
		t.Fatalf("disks unknown = %v, unjudged = %v; want both servers judged", att.DisksUnknown, att.DisksUnjudged)
	}
	it, ok := items["disk_nearly_full||two|/"]
	if len(items) != 1 || !ok || it.Server != "two" || !it.Remote || it.Level != "warning" {
		t.Fatalf("items = %+v, want one for two with the key disk_nearly_full||two|/", items)
	}
	if want := "The disk at / on two is 90% full: 10 GB left."; it.Detail != want {
		t.Errorf("detail = %q, want %q", it.Detail, want)
	}
	if got := plainText(ProblemParts(it, func(s string) string { return s })); got != it.Detail {
		t.Errorf("parts say %q, the item says %q", got, it.Detail)
	}
	ev := problemEvent(it, "https://c.test", l.now)
	if ev.Server != "two" || ev.Title != "Disk nearly full on two: / is 90% full" {
		t.Errorf("event = %+v", ev)
	}

	// The server Caravana runs on keeps the key it had with one server, and names itself now.
	l.nomad.SetHostStatsOf("node-1", statsAt(1000, 96))
	l.tick(t, 2)
	_, items = diskItemsOfCluster(t, l)
	first, ok := items["disk_nearly_full|||/"]
	if len(items) != 2 || !ok || first.Server != firstServerName || first.Remote || first.Level != "failed" {
		t.Fatalf("items = %+v, want the first server's under disk_nearly_full|||/ as well", items)
	}
	if problemSubject(first) != "/" {
		t.Errorf("subject = %q", problemSubject(first))
	}
}

// With one server an item doesn't name a server, and its key is what it always was.
func TestDiskItemOfOneServerIsUnchanged(t *testing.T) {
	e := newEnv(t)
	e.nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	e.nomad.SetHostStats(statsAt(1000, 90))
	e.svc.metricsGap = time.Minute
	at := labBase.Add(time.Minute + 2*time.Second)
	e.svc.clock.(*fakeClock).Set(at)
	if err := e.svc.sampleTick(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	att, err := e.svc.Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var disks []AttentionItem
	for _, it := range att.Items {
		if it.Kind == AttentionDiskNearlyFull {
			disks = append(disks, it)
		}
	}
	if len(disks) != 1 || disks[0].Server != "" || disks[0].Remote || problemKey(disks[0]) != "disk_nearly_full|||/" ||
		disks[0].Detail != "The disk at / is 90% full: 10 GB left." {
		t.Errorf("disk items = %+v", disks)
	}
}

// A server whose newest sample is over 5 minutes old isn't judged, whatever the others say.
func TestDiskItemsOfAStaleServerAreNotJudged(t *testing.T) {
	l := newServersLab(t)
	l.nomad.SetHostStatsOf("node-2", statsAt(2000, 90))
	l.tick(t, 1)

	// Two stops being sampled (down); the first server keeps being sampled.
	l.nomad.SetNodeStatusOf("node-2", "down")
	for n := 2; n <= 8; n++ {
		l.tick(t, n)
	}
	att, items := diskItemsOfCluster(t, l)
	if att.DisksUnknown || !slices.Equal(att.DisksUnjudged, []string{"two"}) {
		t.Errorf("unknown = %v, unjudged = %v; want only two unjudged", att.DisksUnknown, att.DisksUnjudged)
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want none: two's disk is not judged from a stale sample", items)
	}

	// diffProblems keeps a problem sent for two, and resolves one for the first server that is gone.
	sentTwo := store.SentProblem{Key: "disk_nearly_full||two|/", Kind: string(AttentionDiskNearlyFull), LastSeenAt: l.now.Add(-time.Hour)}
	sentFirst := store.SentProblem{Key: "disk_nearly_full|||/", Kind: string(AttentionDiskNearlyFull), LastSeenAt: l.now.Add(-time.Hour)}
	j := judged{Copies: true, Disks: !att.DisksUnknown, OS: true, Servers: true, DisksUnjudged: att.DisksUnjudged}
	got := diffProblems([]store.SentProblem{sentTwo, sentFirst}, nil, j, nil, l.now, 5*time.Minute)
	if len(got.Resolved) != 1 || got.Resolved[0].Key != sentFirst.Key {
		t.Errorf("resolved %+v, want only the first server's", got.Resolved)
	}

	// Every server stale: nothing is judged.
	l.svc.clock.(*fakeClock).Advance(time.Hour)
	att, _ = diskItemsOfCluster(t, l)
	if !att.DisksUnknown {
		t.Errorf("DisksUnknown is false with every sample stale")
	}
}

// The AppVolume's server is the node the volume is on.
func TestVolumeNamesItsServer(t *testing.T) {
	ctx := context.Background()
	l := newServersLab(t)
	setDestination(t, l.env)
	l.nomad.AddHostVolumeOn("whoami_data", "node-2")
	deployHealthy(t, l.env, kuma(false))

	got, err := l.svc.Volumes(ctx, "whoami")
	if err != nil || len(got) != 1 {
		t.Fatalf("Volumes = %+v, %v", got, err)
	}
	if got[0].Name != "data" || got[0].Server != "two" {
		t.Errorf("volume = %+v, want data on two", got[0])
	}
}

// ListServers' numbers are each server's own, from the newest rows of its node; a server with none has
// nil, never 0.
func TestServerNumbersAreEachServersOwn(t *testing.T) {
	ctx := context.Background()
	l := newServersLab(t)
	l.nomad.AddNode("node-3", "three", "ready", "") // a node with no row and no stats
	l.tick(t, 1)

	one, err := l.svc.Server(ctx, firstServerName)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := l.svc.Server(ctx, "two")
	three, _ := l.svc.Server(ctx, "three")
	if one.Memory == nil || one.Memory.Used != 1000 || two.Memory == nil || two.Memory.Used != 2000 {
		t.Errorf("memory = %+v and %+v, want 1000 and 2000 used", one.Memory, two.Memory)
	}
	if len(two.Disks) != 1 || two.Disks[0].Mount != "/" {
		t.Errorf("two's disks = %+v", two.Disks)
	}
	if three.Memory != nil || three.CPU != nil || three.Disks != nil {
		t.Errorf("three has no sample: memory %+v, cpu %+v, disks %+v; want nil", three.Memory, three.CPU, three.Disks)
	}
}
