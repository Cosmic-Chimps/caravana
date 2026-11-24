package nodeagent

import (
	"context"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Network and disk I/O (feature 055). The agent reads the kernel's cumulative counters from /proc and
// puts them in the report; it never computes a rate, because it runs once every 30 seconds and keeps
// nothing between runs. The console keeps the previous sample of each server and divides.
//
// Only what an owner wants to see is reported: the network interface of the default route (what the
// internet sees of the server), the mesh interface when there is one (what the servers say to each
// other), and the block devices that hold Nomad's data and Docker's root. Never every interface: the
// veth, docker and cni ones change with every copy and say nothing about the server.

// The limits of the io block (the console refuses more, and drops the block).
const (
	// MaxIOInterfaces and MaxIODevices are the most entries of each kind a report holds.
	MaxIOInterfaces = 4
	MaxIODevices    = 4
	// sectorBytes is the unit of /proc/diskstats' sector counters, whatever the device's own sector.
	sectorBytes = 512
	// defaultDockerRoot is where Docker keeps its data when `docker info` doesn't say.
	defaultDockerRoot = "/var/lib/docker"
)

// IOReport is the report's io block: counters since boot, read at At (Unix milliseconds).
type IOReport struct {
	At    int64        `json:"at"`
	Net   []NetCounter `json:"net"`
	Disks []DiskIO     `json:"disks"`
}

// NetCounter is one network interface's bytes since boot. Kind is "default" (the interface of the
// default route) or "mesh" (caravana-mesh).
type NetCounter struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	RxBytes int64  `json:"rxBytes"`
	TxBytes int64  `json:"txBytes"`
}

// DiskIO is one block device's counters since boot: bytes read and written, and the milliseconds it was
// busy (the kernel's io_ticks). Holds says what is on it: "nomad", "docker" or both.
type DiskIO struct {
	Name       string   `json:"name"`
	Holds      []string `json:"holds"`
	ReadBytes  int64    `json:"readBytes"`
	WriteBytes int64    `json:"writeBytes"`
	BusyMs     int64    `json:"busyMs"`
}

// netStat is one line of /proc/net/dev.
type netStat struct {
	Name             string
	RxBytes, TxBytes uint64
}

// parseNetDev reads /proc/net/dev: two header lines, then "  name: rx_bytes packets errs drop fifo frame
// compressed multicast tx_bytes ..." with sixteen counters. The counters may touch the colon when they
// are wide ("eth0:1234"). A line without a colon, or with fewer than sixteen counters, is skipped.
func parseNetDev(text string) []netStat {
	var out []netStat
	for _, line := range strings.Split(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if name == "" || len(f) < 16 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, netStat{Name: name, RxBytes: rx, TxBytes: tx})
	}
	return out
}

// diskStat is one line of /proc/diskstats.
type diskStat struct {
	Major, Minor                         int
	Name                                 string
	ReadSectors, WriteSectors, BusyTicks uint64
}

// parseDiskstats reads /proc/diskstats: "major minor name rd_ios rd_merges rd_sectors rd_ticks wr_ios
// wr_merges wr_sectors wr_ticks in_flight io_ticks ...". A line with fewer than thirteen fields is skipped.
func parseDiskstats(text string) []diskStat {
	var out []diskStat
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 13 {
			continue
		}
		major, err1 := strconv.Atoi(f[0])
		minor, err2 := strconv.Atoi(f[1])
		rd, err3 := strconv.ParseUint(f[5], 10, 64)
		wr, err4 := strconv.ParseUint(f[9], 10, 64)
		busy, err5 := strconv.ParseUint(f[12], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
			continue
		}
		out = append(out, diskStat{Major: major, Minor: minor, Name: f[2], ReadSectors: rd, WriteSectors: wr, BusyTicks: busy})
	}
	return out
}

// mountEntry is one line of /proc/self/mountinfo: the device's "major:minor" and where it is mounted.
type mountEntry struct {
	MajMin string
	Mount  string
}

// parseMountinfo reads /proc/self/mountinfo: "id parent major:minor root mountpoint options [optional
// fields] - fstype source superoptions". The mount point escapes space, tab, newline and backslash as
// octal (\040). A line that doesn't have the shape is skipped.
func parseMountinfo(text string) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		if _, _, ok := strings.Cut(f[2], ":"); !ok {
			continue
		}
		out = append(out, mountEntry{MajMin: f[2], Mount: unescapeMount(f[4])})
	}
	return out
}

// unescapeMount undoes the kernel's octal escapes in a mount point.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// deviceOf finds the "major:minor" of the mount that holds path: the mount with the longest mount point
// that is path or a folder above it. A later line wins over an earlier one at the same mount point (a
// mount laid over another).
func deviceOf(mounts []mountEntry, path string) (string, bool) {
	best, found := -1, ""
	for _, m := range mounts {
		mp := strings.TrimRight(m.Mount, "/")
		if mp != "" && path != mp && !strings.HasPrefix(path, mp+"/") {
			continue
		}
		if len(mp) >= best {
			best, found = len(mp), m.MajMin
		}
	}
	return found, best >= 0
}

// skipDevice says a block device isn't a disk worth reporting: loop, RAM and zram devices.
func skipDevice(name string) bool {
	return strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram")
}

// skipInterface says an interface is never reported even when the default route names it: the
// loopback and the virtual ones that come and go with the copies.
func skipInterface(name string) bool {
	if name == "lo" || name == "nomad" || name == "caravana0" {
		return true
	}
	for _, p := range []string{"veth", "docker", "cni", "br-"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// fitsInt64 says a counter can travel as a JSON number the console reads.
func fitsInt64(v uint64) bool { return v <= math.MaxInt64 }

// netCounters picks the interfaces to report from /proc/net/dev's lines: the one of the default route
// (when it isn't a virtual one) and the mesh interface. A counter too big to send leaves its interface out.
func netCounters(stats []netStat, defaultIface string) []NetCounter {
	var out []NetCounter
	add := func(name, kind string) {
		for _, s := range stats {
			if s.Name == name && fitsInt64(s.RxBytes) && fitsInt64(s.TxBytes) {
				out = append(out, NetCounter{Name: name, Kind: kind, RxBytes: int64(s.RxBytes), TxBytes: int64(s.TxBytes)})
				return
			}
		}
	}
	if defaultIface != "" && defaultIface != meshInterface && !skipInterface(defaultIface) && interfaceRE.MatchString(defaultIface) {
		add(defaultIface, "default")
	}
	add(meshInterface, "mesh")
	return out
}

// pathHold says what a path holds: "nomad" for Nomad's data directory, "docker" for Docker's root.
type pathHold struct{ Path, What string }

// diskCounters picks the devices to report: one entry per device that holds one of the paths (and
// what is on it, "nomad" or "docker"), found by the mount that holds the path. A device is left out when
// it isn't in /proc/diskstats (btrfs and overlay report no device there), is a loop, RAM or zram device,
// or has counters too big to send.
func diskCounters(mounts []mountEntry, stats []diskStat, holds []pathHold) []DiskIO {
	byDev := map[string]*DiskIO{}
	var order []string
	for _, h := range holds {
		majmin, ok := deviceOf(mounts, h.Path)
		if !ok {
			continue
		}
		var st *diskStat
		for i := range stats {
			if strconv.Itoa(stats[i].Major)+":"+strconv.Itoa(stats[i].Minor) == majmin {
				st = &stats[i]
				break
			}
		}
		if st == nil || skipDevice(st.Name) {
			continue
		}
		if st.ReadSectors > math.MaxInt64/sectorBytes || st.WriteSectors > math.MaxInt64/sectorBytes || !fitsInt64(st.BusyTicks) {
			continue
		}
		d, ok := byDev[st.Name]
		if !ok {
			d = &DiskIO{Name: st.Name, ReadBytes: int64(st.ReadSectors) * sectorBytes,
				WriteBytes: int64(st.WriteSectors) * sectorBytes, BusyMs: int64(st.BusyTicks)}
			byDev[st.Name] = d
			order = append(order, st.Name)
		}
		if !slices.Contains(d.Holds, h.What) {
			d.Holds = append(d.Holds, h.What)
		}
	}
	out := make([]DiskIO, 0, len(order))
	for _, n := range order {
		slices.Sort(byDev[n].Holds)
		out = append(out, *byDev[n])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// dockerRoot is Docker's data directory: what `docker info` says when it is an absolute path, else the
// default.
func (a *Agent) dockerRoot(ctx context.Context) string {
	out, err := a.run(ctx, "docker", "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		return defaultDockerRoot
	}
	root := strings.TrimSpace(string(out))
	if !strings.HasPrefix(root, "/") || strings.ContainsAny(root, "\n\x00") || len(root) > 4096 {
		return defaultDockerRoot
	}
	return root
}

// ioReport reads the counters. It is nil when /proc can't give anything useful: a failed read never
// fails the sync, the report just has no io block.
func (a *Agent) ioReport(ctx context.Context, st State) *IOReport {
	at := a.now().UnixMilli()
	r := &IOReport{At: at, Net: []NetCounter{}, Disks: []DiskIO{}}

	if b, err := os.ReadFile(a.path("/proc/net/dev")); err == nil {
		iface := st.PublicInterface
		if iface == "" {
			iface = a.defaultRouteInterface(ctx)
		}
		r.Net = netCounters(parseNetDev(string(b)), iface)
	}
	mounts, err1 := os.ReadFile(a.path("/proc/self/mountinfo"))
	stats, err2 := os.ReadFile(a.path("/proc/diskstats"))
	if err1 == nil && err2 == nil {
		holds := []pathHold{{nomadDataDir, "nomad"}, {a.dockerRoot(ctx), "docker"}}
		r.Disks = diskCounters(parseMountinfo(string(mounts)), parseDiskstats(string(stats)), holds)
	}
	if len(r.Net) == 0 && len(r.Disks) == 0 {
		return nil
	}
	return r
}
