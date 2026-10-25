package deploy

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"
)

// ErrNodeReadDenied is the Err of a host read Nomad refused because the
// token's policy has no node:read (feature 025): a token made before it. It
// also matches ErrNomadUnavailable, like the other capability errors, and
// says what to do.
var ErrNodeReadDenied = fmt.Errorf("%w: Nomad refused to show the host: its policy needs the node:read capability; run the installer again to update it",
	ErrNomadUnavailable)

// nodeInfoTTL is how long a node's resources (its totals, which change only
// when the agent is reconfigured) are kept before Nomad is asked again.
const nodeInfoTTL = 10 * time.Minute

// Host is what Nomad says about the node Caravana's apps run on: its memory
// and CPU now, its totals, what Nomad holds back for itself, and the
// filesystems that matter to Caravana (research R3). The sizes are in MB and
// MHz; Nomad's bytes are rounded to the nearest MB.
type Host struct {
	NodeID   string
	NodeName string

	MemoryUsedMB  int // the operating system's view: total less free, buffers and cache
	MemoryTotalMB int
	CPUUsedMHz    int // Nomad's last collection over all cores: a point sample
	CPUTotalMHz   int
	CPUCores      int

	// ReservedCPUMHz and ReservedMemoryMB are what the node's own
	// configuration keeps back from placement (0 when it keeps none). They are
	// not the apps' reservations; Reservations adds those.
	ReservedCPUMHz   int
	ReservedMemoryMB int

	Disks []Disk
}

// Disk is one filesystem of the host and what Caravana keeps on it.
type Disk struct {
	Mount string // the mount point, as Nomad reports it: "/", "/var/lib/docker"
	// HoldsNomad is true for the filesystem of Nomad's data directory (the
	// copies' directories, logs and dynamic host volumes), HoldsDocker for the
	// one that has Docker's root (images and writable layers). One filesystem
	// can hold both.
	HoldsNomad, HoldsDocker bool
	UsedMB, TotalMB, FreeMB int // FreeMB is what a user who isn't root may still write
}

// Holds is HoldsNomad and HoldsDocker as the store keeps them: "nomad",
// "docker" or "nomad,docker".
func (d Disk) Holds() string {
	switch {
	case d.HoldsNomad && d.HoldsDocker:
		return "nomad,docker"
	case d.HoldsNomad:
		return "nomad"
	case d.HoldsDocker:
		return "docker"
	}
	return ""
}

// hostCache keeps what the deploy package remembers between calls of
// HostStats and Reservations. Its zero value is ready to use.
type hostCache struct {
	mu sync.Mutex
	// now is the clock; nil means time.Now. Tests set it.
	now   func() time.Time
	nodes map[string]cachedNode    // by node ID
	tasks map[jobVersion]taskHooks // by job ID and version
}

func (c *hostCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// cachedNode is a node's resources and when they were read.
type cachedNode struct {
	at   time.Time
	node *api.Node
}

// HostStats reads the node Caravana's apps run on: Nodes().List (the first
// node that is ready: Phase 2.6 has one), its resources (Info, kept for 10
// minutes) and its stats. dockerRoot is Docker's data directory
// (CARAVANA_DOCKER_ROOT); "" leaves Docker's filesystem out.
//
// The disks are the two filesystems that matter and no other. Nomad's data
// is AllocDirStats, which carries no mount name: its mount is the DiskStats
// entry with the same size and (within 1 %) the same use. Docker's root is
// the entry with the longest mount point that is a path prefix of dockerRoot.
// Both on one filesystem is one Disk that holds both. A mount that is neither
// (a cloud-init CD, /boot) is never listed.
//
// A 403 is ErrNodeReadDenied; any other failure is ErrNomadUnavailable.
func (d *Deployer) HostStats(ctx context.Context, dockerRoot string) (Host, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	nodes, _, err := d.nomad.Nodes().List(q)
	if err != nil {
		return Host{}, d.nodeError("list nodes", err)
	}
	var stub *api.NodeListStub
	for _, n := range nodes {
		if n.Status == "ready" {
			stub = n
			break
		}
	}
	if stub == nil {
		return Host{}, fmt.Errorf("%w: Nomad has no node that is ready", ErrNomadUnavailable)
	}

	info, err := d.nodeInfo(ctx, stub.ID)
	if err != nil {
		return Host{}, err
	}
	stats, err := d.nomad.Nodes().Stats(stub.ID, q)
	if err != nil {
		return Host{}, d.nodeError("read the host's stats", err)
	}
	if stats.Memory == nil {
		return Host{}, fmt.Errorf("%w: Nomad's stats for node %s have no memory", ErrNomadUnavailable, stub.ID)
	}

	h := Host{
		NodeID:       stub.ID,
		NodeName:     info.Name,
		MemoryUsedMB: mbOf(stats.Memory.Used),
		CPUUsedMHz:   int(stats.CPUTicksConsumed + 0.5),
	}
	if r := info.NodeResources; r != nil {
		h.MemoryTotalMB = int(r.Memory.MemoryMB)
		h.CPUTotalMHz = int(r.Cpu.CpuShares)
		h.CPUCores = int(r.Cpu.TotalCpuCores)
	}
	if h.MemoryTotalMB == 0 {
		h.MemoryTotalMB = mbOf(stats.Memory.Total)
	}
	if r := info.ReservedResources; r != nil {
		h.ReservedCPUMHz = int(r.Cpu.CpuShares)
		h.ReservedMemoryMB = int(r.Memory.MemoryMB)
	}
	h.Disks = watchedDisks(stats, dockerRoot)
	return h, nil
}

// nodeInfo is Nodes().Info, kept for nodeInfoTTL.
func (d *Deployer) nodeInfo(ctx context.Context, id string) (*api.Node, error) {
	c := &d.cache
	c.mu.Lock()
	hit, ok := c.nodes[id]
	fresh := ok && c.clock().Sub(hit.at) < nodeInfoTTL
	c.mu.Unlock()
	if fresh {
		return hit.node, nil
	}
	node, _, err := d.nomad.Nodes().Info(id, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.nodeError("read the host's resources", err)
	}
	c.mu.Lock()
	if c.nodes == nil {
		c.nodes = map[string]cachedNode{}
	}
	c.nodes[id] = cachedNode{at: c.clock(), node: node}
	c.mu.Unlock()
	return node, nil
}

// nodeError is callError for a call that needs node:read.
func (d *Deployer) nodeError(action string, err error) error {
	if statusOf(err) == http.StatusForbidden {
		return fmt.Errorf("%w (%s)", ErrNodeReadDenied, action)
	}
	return d.callError(action, err)
}

// mbOf is bytes in MB, to the nearest.
func mbOf(bytes uint64) int { return int((bytes + 1<<19) >> 20) }

// watchedDisks picks the filesystems of Nomad's data and of Docker's root out
// of a node's stats, ordered by mount.
func watchedDisks(stats *api.HostStats, dockerRoot string) []Disk {
	var out []Disk
	add := func(s *api.HostDiskStats, nomad, docker bool) {
		for i := range out {
			if out[i].Mount == s.Mountpoint {
				out[i].HoldsNomad = out[i].HoldsNomad || nomad
				out[i].HoldsDocker = out[i].HoldsDocker || docker
				return
			}
		}
		out = append(out, Disk{
			Mount: s.Mountpoint, HoldsNomad: nomad, HoldsDocker: docker,
			UsedMB: mbOf(s.Used), TotalMB: mbOf(s.Size), FreeMB: mbOf(s.Available),
		})
	}
	if a := stats.AllocDirStats; a != nil {
		if m := mountOfAllocDir(stats.DiskStats, a); m != nil {
			add(m, true, false)
		}
	}
	if m := mountOfPath(stats.DiskStats, dockerRoot); m != nil {
		add(m, false, true)
	}
	slices.SortFunc(out, func(a, b Disk) int { return strings.Compare(a.Mount, b.Mount) })
	return out
}

// mountOfAllocDir finds the mount of Nomad's data: the entry with the same
// size as AllocDirStats whose use is within 1 % of AllocDirStats' own, the
// closest use first, then the shortest mount point. Nomad measures
// AllocDirStats in the same collection as the mounts, so on the right one the
// two agree to the byte; the tolerance is for a write between two reads.
func mountOfAllocDir(disks []*api.HostDiskStats, alloc *api.HostDiskStats) *api.HostDiskStats {
	var best *api.HostDiskStats
	var bestGap uint64
	for _, m := range disks {
		if m == nil || m.Size != alloc.Size {
			continue
		}
		gap := absDiff(m.Used, alloc.Used)
		if gap*100 > alloc.Used {
			continue
		}
		if best == nil || gap < bestGap || (gap == bestGap && len(m.Mountpoint) < len(best.Mountpoint)) {
			best, bestGap = m, gap
		}
	}
	return best
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// mountOfPath finds the mount that holds p: the entry with the longest mount
// point that is a path prefix of it ("/var" holds "/var/lib/docker", "/va"
// doesn't). A p that isn't absolute, or has no mount, gives nil.
func mountOfPath(disks []*api.HostDiskStats, p string) *api.HostDiskStats {
	if p == "" || !path.IsAbs(p) {
		return nil
	}
	p = path.Clean(p)
	var best *api.HostDiskStats
	for _, m := range disks {
		if m == nil || m.Mountpoint == "" {
			continue
		}
		mount := path.Clean(m.Mountpoint)
		holds := mount == "/" || p == mount || strings.HasPrefix(p, mount+"/")
		if holds && (best == nil || len(mount) > len(path.Clean(best.Mountpoint))) {
			best = m
		}
	}
	return best
}
