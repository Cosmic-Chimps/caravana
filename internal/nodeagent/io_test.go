package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func procFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/proc/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseNetDev(t *testing.T) {
	got := parseNetDev(procFixture(t, "net_dev"))
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if want := []string{"lo", "eth0", "docker0", "vethab12cd", "caravana-mesh", "caravana0"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("interfaces = %v, want %v (the header and the line without a colon are skipped)", names, want)
	}
	for _, s := range got {
		if s.Name == "eth0" && (s.RxBytes != 8123456789 || s.TxBytes != 2345678901) {
			t.Errorf("eth0 = %+v", s)
		}
	}

	for name, tc := range map[string]struct {
		in     string
		rx, tx uint64
		ok     bool
	}{
		"counters touching the colon": {"eth0:123 1 0 0 0 0 0 0 456 1 0 0 0 0 0 0", 123, 456, true},
		"odd spacing and a tab":       {"   eth1:\t  7   1 0 0 0 0 0 0\t\t8 1 0 0 0 0 0 0", 7, 8, true},
		"near 2^64":                   {"eth0: 18446744073709551615 1 0 0 0 0 0 0 18446744073709551614 1 0 0 0 0 0 0", 18446744073709551615, 18446744073709551614, true},
		"over 2^64":                   {"eth0: 18446744073709551616 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0", 0, 0, false},
		"too few counters":            {"eth0: 1 2 3", 0, 0, false},
		"not a number":                {"eth0: x 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0", 0, 0, false},
		"no name":                     {": 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0", 0, 0, false},
		"empty":                       {"", 0, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := parseNetDev(tc.in)
			if !tc.ok {
				if len(s) != 0 {
					t.Fatalf("parsed %+v", s)
				}
				return
			}
			if len(s) != 1 || s[0].RxBytes != tc.rx || s[0].TxBytes != tc.tx {
				t.Fatalf("parsed %+v, want rx %d tx %d", s, tc.rx, tc.tx)
			}
		})
	}
}

func TestParseDiskstats(t *testing.T) {
	got := parseDiskstats(procFixture(t, "diskstats"))
	byName := map[string]diskStat{}
	for _, d := range got {
		byName[d.Name] = d
	}
	if len(got) != 7 {
		t.Fatalf("%d devices, want 7 (the short line is skipped): %+v", len(got), got)
	}
	sda := byName["sda"]
	if sda.Major != 8 || sda.Minor != 0 || sda.ReadSectors != 12000000 || sda.WriteSectors != 8000000 || sda.BusyTicks != 150000 {
		t.Errorf("sda = %+v", sda)
	}
	// A kernel with fewer columns (13 fields) still works; fewer than that is skipped.
	if d := parseDiskstats("8 0 sda 1 2 3 4 5 6 7 8 9 10"); len(d) != 1 || d[0].ReadSectors != 3 || d[0].WriteSectors != 7 || d[0].BusyTicks != 10 {
		// fields: major minor name rd_ios rd_merges rd_sectors rd_ticks wr_ios wr_merges wr_sectors wr_ticks in_flight io_ticks
		t.Errorf("a short line = %+v", d)
	}
	if d := parseDiskstats("8 0 sda 1 2 3 4 5 6 7 8 9"); len(d) != 0 {
		t.Errorf("12 fields = %+v", d)
	}
	if d := parseDiskstats("8 0 sda 1 2 x 4 5 6 7 8 9 10"); len(d) != 0 {
		t.Errorf("a counter that isn't a number = %+v", d)
	}
}

func TestParseMountinfo(t *testing.T) {
	mounts := parseMountinfo(procFixture(t, "mountinfo"))
	if len(mounts) != 5 {
		t.Fatalf("%d mounts: %+v", len(mounts), mounts)
	}
	if mounts[2].Mount != "/mnt/with space" || mounts[2].MajMin != "8:1" {
		t.Errorf("the escaped mount point = %+v", mounts[2])
	}
	for in, want := range map[string]string{
		`/plain`:         "/plain",
		`/a\040b\011c`:   "/a b\tc",
		`/back\134slash`: `/back\slash`,
		`/trailing\04`:   `/trailing\04`,
		`/bad\9zz`:       `/bad\9zz`,
	} {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeviceOf(t *testing.T) {
	mounts := parseMountinfo(procFixture(t, "mountinfo"))
	for path, want := range map[string]string{
		"/opt/nomad/data":             "8:1",   // under /
		"/":                           "8:1",   // the root itself
		"/var/lib/docker":             "253:0", // the longest mount point
		"/var/lib/docker/overlay2/x":  "253:0",
		"/var/lib/docker-other":       "8:1", // a name that merely starts the same is not under the mount
		"/mnt/with space/data":        "8:1",
		"/snap/core/1234/bin":         "7:0",
		"/var/lib/dockerx/deep/path/": "8:1",
	} {
		got, ok := deviceOf(mounts, path)
		if !ok || got != want {
			t.Errorf("deviceOf(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
	if _, ok := deviceOf(nil, "/opt"); ok {
		t.Error("no mounts found a device")
	}
	// A later mount at the same point wins (one laid over another).
	over := []mountEntry{{"8:1", "/"}, {"8:2", "/data"}, {"8:3", "/data"}}
	if got, _ := deviceOf(over, "/data/x"); got != "8:3" {
		t.Errorf("an overmount = %q", got)
	}
}

func TestNetCounters(t *testing.T) {
	stats := parseNetDev(procFixture(t, "net_dev"))
	got := netCounters(stats, "eth0")
	want := []NetCounter{
		{Name: "eth0", Kind: "default", RxBytes: 8123456789, TxBytes: 2345678901},
		{Name: "caravana-mesh", Kind: "mesh", RxBytes: 5000000000, TxBytes: 6000000000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("net = %+v, want %+v", got, want)
	}
	// Never the virtual ones, even when a default route names them; a missing mesh is left out.
	for _, iface := range []string{"lo", "docker0", "vethab12cd", "caravana0", "nomad", "cni0", "br-1234", "", "bad name!", "nope"} {
		for _, c := range netCounters(stats, iface) {
			if c.Kind == "default" {
				t.Errorf("default %q reported %+v", iface, c)
			}
		}
	}
	if got := netCounters(stats[:2], "eth0"); len(got) != 1 || got[0].Kind != "default" {
		t.Errorf("without a mesh interface: %+v", got)
	}
	// The default route may be the mesh itself on a odd host: it is reported once, as the mesh.
	if got := netCounters(stats, "caravana-mesh"); len(got) != 1 || got[0].Kind != "mesh" {
		t.Errorf("default = mesh: %+v", got)
	}
	// A counter that can't travel as a JSON number leaves its interface out.
	big := []netStat{{Name: "eth0", RxBytes: 1 << 63, TxBytes: 1}}
	if got := netCounters(big, "eth0"); len(got) != 0 {
		t.Errorf("a counter over 2^63 was sent: %+v", got)
	}
}

func TestDiskCounters(t *testing.T) {
	mounts := parseMountinfo(procFixture(t, "mountinfo"))
	stats := parseDiskstats(procFixture(t, "diskstats"))

	// Nomad's data on / (sda1) and Docker's root on 253:0, which is a zram device here: skipped.
	got := diskCounters(mounts, stats, []pathHold{{nomadDataDir, "nomad"}, {"/var/lib/docker", "docker"}})
	want := []DiskIO{{Name: "sda1", Holds: []string{"nomad"}, ReadBytes: 11990000 * 512, WriteBytes: 7999000 * 512, BusyMs: 149990}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("disks = %+v, want %+v", got, want)
	}

	// Both on one device: one entry that holds both, in a fixed order.
	got = diskCounters(mounts, stats, []pathHold{{"/srv/docker/x", "docker"}, {"/", "nomad"}})
	if len(got) != 1 || got[0].Name != "sda1" || !reflect.DeepEqual(got[0].Holds, []string{"docker", "nomad"}) {
		t.Errorf("one device = %+v", got)
	}
	got = diskCounters(mounts, stats, []pathHold{{"/opt/nomad/data", "nomad"}, {"/srv/docker", "docker"}})
	if len(got) != 1 || !reflect.DeepEqual(got[0].Holds, []string{"docker", "nomad"}) {
		t.Errorf("both on the root: %+v", got)
	}

	// Two devices, ordered by name; a loop device, a mount that isn't in diskstats and counters that
	// overflow are left out.
	m2 := []mountEntry{{"8:1", "/"}, {"252:0", "/data"}, {"7:0", "/loop"}, {"0:44", "/btrfs"}, {"8:16", "/huge"}}
	s2 := append(stats, diskStat{Major: 8, Minor: 16, Name: "sdb", ReadSectors: 1 << 60, WriteSectors: 1, BusyTicks: 1})
	got = diskCounters(m2, s2, []pathHold{{"/data", "docker"}, {"/opt", "nomad"}, {"/loop/x", "nomad"}, {"/btrfs", "nomad"}, {"/huge", "docker"}})
	var names []string
	for _, d := range got {
		names = append(names, d.Name)
	}
	if !reflect.DeepEqual(names, []string{"sda1", "vda"}) {
		t.Errorf("devices = %v (loop, unknown and overflowing ones are skipped)", names)
	}
}

func TestDockerRoot(t *testing.T) {
	a := newTestAgent(t, nil)
	ctx := context.Background()
	if got := a.dockerRoot(ctx); got != "/var/lib/docker" {
		t.Errorf("no answer: %q", got)
	}
	a.runner.dockerRootDir = "/mnt/data/docker\n"
	if got := a.dockerRoot(ctx); got != "/mnt/data/docker" {
		t.Errorf("answer: %q", got)
	}
	a.runner.dockerRootDir = "27.5.1\n" // not a path
	if got := a.dockerRoot(ctx); got != "/var/lib/docker" {
		t.Errorf("not a path: %q", got)
	}
	a.runner.failOn["docker info"] = errors.New("docker is down")
	if got := a.dockerRoot(ctx); got != "/var/lib/docker" {
		t.Errorf("docker down: %q", got)
	}
}

func (a *testAgent) writeProc(t *testing.T) {
	t.Helper()
	a.writeFiles(t, map[string]string{
		"proc/net/dev":         procFixture(t, "net_dev"),
		"proc/diskstats":       procFixture(t, "diskstats"),
		"proc/self/mountinfo":  procFixture(t, "mountinfo"),
		"proc/meminfo":         "MemTotal:        4010116 kB\n",
		"opt/nomad/data/.keep": "",
	})
}

func TestReportHasTheIOBlock(t *testing.T) {
	a := newTestAgent(t, nil)
	a.writeProc(t)
	st := State{Version: 1, Index: 1, Name: "one", Pool: "default", Role: RoleServer, Servers: []int{1}, First: true}

	r := a.Report(context.Background(), st)
	if r.IO == nil {
		t.Fatal("no io block")
	}
	if r.IO.At != t0.UnixMilli() {
		t.Errorf("at = %d", r.IO.At)
	}
	// The interface is the default route's (eth0 in the fake runner), the mesh is there, and nothing else.
	if len(r.IO.Net) != 2 || r.IO.Net[0].Name != "eth0" || r.IO.Net[0].Kind != "default" || r.IO.Net[1].Name != "caravana-mesh" {
		t.Errorf("net = %+v", r.IO.Net)
	}
	if len(r.IO.Disks) != 1 || r.IO.Disks[0].Name != "sda1" {
		t.Errorf("disks = %+v", r.IO.Disks)
	}

	b, _ := json.Marshal(r)
	for _, want := range []string{`"io":{"at":`, `"rxBytes":8123456789`, `"txBytes":2345678901`, `"readBytes":6138880000`, `"busyMs":149990`, `"holds":["nomad"]`, `"kind":"mesh"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the report JSON lacks %s: %s", want, b)
		}
	}

	// The state's public interface wins over asking `ip`, so the route isn't read.
	a.runner.reset()
	st.PublicInterface = "caravana-mesh"
	r = a.Report(context.Background(), st)
	if r.IO == nil || len(r.IO.Net) != 1 || r.IO.Net[0].Kind != "mesh" {
		t.Errorf("public interface = mesh: %+v", r.IO)
	}
	for _, c := range a.runner.calls {
		if c.name == "ip" {
			t.Errorf("ip was asked although the state names the interface: %s", c)
		}
	}
}

// A host whose /proc can't be read, or the proc files of another system, never fails the report: the
// block is absent.
func TestReportWithoutIO(t *testing.T) {
	a := newTestAgent(t, nil)
	st := State{Version: 1, Index: 1, Name: "one", Pool: "default", Role: RoleServer, Servers: []int{1}, First: true}
	if r := a.Report(context.Background(), st); r.IO != nil {
		t.Errorf("no /proc: io = %+v", r.IO)
	}
	b, _ := json.Marshal(a.Report(context.Background(), st))
	if strings.Contains(string(b), `"io"`) {
		t.Errorf("an absent block is in the JSON: %s", b)
	}

	// Only the network file: the network is reported, the disks are an empty list.
	a.writeFiles(t, map[string]string{"proc/net/dev": procFixture(t, "net_dev")})
	r := a.Report(context.Background(), st)
	if r.IO == nil || len(r.IO.Net) != 2 || r.IO.Disks == nil || len(r.IO.Disks) != 0 {
		t.Errorf("only net: %+v", r.IO)
	}

	// Files that hold nothing the parsers know.
	a.writeFiles(t, map[string]string{"proc/net/dev": "garbage\n", "proc/diskstats": "garbage\n", "proc/self/mountinfo": "garbage\n"})
	if r := a.Report(context.Background(), st); r.IO != nil {
		t.Errorf("garbage: io = %+v", r.IO)
	}
}
