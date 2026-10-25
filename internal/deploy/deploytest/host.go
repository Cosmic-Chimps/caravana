package deploytest

import (
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// The node and what a client reports about it (feature 025): GET /v1/nodes,
// GET /v1/node/{id} and GET /v1/client/stats?node_id=. A test sets the node
// with SetNode and what it measures with SetHostStats, and makes the reads
// answer 403 as a token without node:read gets, with FailNodeReads.

// hostFake is the fake's node.
type hostFake struct {
	set      bool
	node     api.Node
	status   string
	stats    api.HostStats
	failWith int // non-zero: every node read answers it
	infoReqs int
	listReqs int
	statReqs int
}

// SetNode sets the one node: its ID, name, status ("ready", "down"), what it
// offers (Info's NodeResources) and what its configuration holds back
// (ReservedResources, which may be nil).
func (n *Nomad) SetNode(id, name, status string, res api.NodeResources, reserved *api.NodeReservedResources) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.host.set = true
	n.host.status = status
	n.host.node = api.Node{ID: id, Name: name, Status: status, NodeResources: &res, ReservedResources: reserved}
}

// SetHostStats sets what GET /v1/client/stats answers.
func (n *Nomad) SetHostStats(s api.HostStats) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.host.stats = s
}

// FailNodeReads makes every node read answer status (0 undoes it): 403 is a
// token whose policy has no node:read.
func (n *Nomad) FailNodeReads(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.host.failWith = status
}

// NodeRequests counts the node reads answered so far: the list, Info and
// stats.
func (n *Nomad) NodeRequests() (list, info, stats int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.host.listReqs, n.host.infoReqs, n.host.statReqs
}

// serveNode answers the three node reads.
func (n *Nomad) serveNode(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	h := &n.host
	switch {
	case r.URL.Path == "/v1/nodes":
		h.listReqs++
	case r.URL.Path == "/v1/client/stats":
		h.statReqs++
	default:
		h.infoReqs++
	}
	if h.failWith != 0 {
		http.Error(w, "Permission denied", h.failWith)
		return
	}
	if !h.set {
		if r.URL.Path == "/v1/nodes" {
			n.writeJSON(w, []*api.NodeListStub{})
			return
		}
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	switch {
	case r.URL.Path == "/v1/nodes":
		n.writeJSON(w, []*api.NodeListStub{{
			ID: h.node.ID, Name: h.node.Name, Status: h.status,
			NodeResources: h.node.NodeResources, ReservedResources: h.node.ReservedResources,
		}})
	case r.URL.Path == "/v1/client/stats":
		if r.URL.Query().Get("node_id") != h.node.ID {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		n.writeJSON(w, h.stats)
	default:
		if strings.TrimPrefix(r.URL.Path, "/v1/node/") != h.node.ID {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		n.writeJSON(w, h.node)
	}
}

// LabHostStats is a client's stats as the lab showed them (research R3, R8):
// a 2 GB VM whose root filesystem holds everything, with /boot, /boot/efi
// and the cloud-init CD (/mnt/lima-cidata, always 100 % full) beside it.
// AllocDirStats is the root filesystem's numbers with no mount name.
func LabHostStats() api.HostStats {
	root := api.HostDiskStats{Device: "/dev/vda1", Mountpoint: "/",
		Size: 19682557952, Used: 6233407488, Available: 13432373248, UsedPercent: 31.69}
	alloc := root
	alloc.Device, alloc.Mountpoint = "", ""
	return api.HostStats{
		Memory:           &api.HostMemoryStats{Total: 2044227584, Available: 176529408, Used: 1761398784, Free: 38780928},
		CPUTicksConsumed: 484.8484,
		DiskStats: []*api.HostDiskStats{
			&root,
			{Device: "/dev/vda13", Mountpoint: "/boot", Size: 1000000000, Used: 100000000, Available: 900000000, UsedPercent: 10},
			{Device: "/dev/vda15", Mountpoint: "/boot/efi", Size: 100000000, Used: 6000000, Available: 94000000, UsedPercent: 6},
			{Device: "/dev/vdb", Mountpoint: "/mnt/lima-cidata", Size: 19922944, Used: 19922944, Available: 0, UsedPercent: 100},
		},
		AllocDirStats: &alloc,
	}
}

// LabNodeResources is what the lab's node offers: 4000 MHz over 2 cores
// (cpu_total_compute) and 1949 MB.
func LabNodeResources() api.NodeResources {
	return api.NodeResources{
		Cpu:    api.NodeCpuResources{CpuShares: 4000, TotalCpuCores: 2},
		Memory: api.NodeMemoryResources{MemoryMB: 1949},
	}
}

// AllocUsage is the stats of a running copy: each task's CPU ticks, and its
// memory as cgroups v2 reports it, Usage with the page cache in it (Cache),
// both in bytes, with the time of the collection set.
func AllocUsage(tasks map[string]TaskUsage) api.AllocResourceUsage {
	out := api.AllocResourceUsage{
		Timestamp: time.Now().UnixNano(),
		Tasks:     map[string]*api.TaskResourceUsage{},
	}
	for name, u := range tasks {
		out.Tasks[name] = &api.TaskResourceUsage{ResourceUsage: &api.ResourceUsage{
			CpuStats: &api.CpuStats{TotalTicks: u.CPUTicks},
			MemoryStats: &api.MemoryStats{
				Usage: u.UsageBytes, Cache: u.CacheBytes, RSS: u.RSSBytes,
				Measured: []string{"Usage", "Cache"},
			},
		}}
	}
	return out
}

// TaskUsage is one task's numbers for AllocUsage.
type TaskUsage struct {
	CPUTicks                         float64
	UsageBytes, CacheBytes, RSSBytes uint64
}

// DeadAllocUsage is what Nomad answers for a copy that isn't running (lab,
// research R8.11): a 200 with no time and one zeroed entry per task, with
// nothing measured.
func DeadAllocUsage(tasks ...string) api.AllocResourceUsage {
	out := api.AllocResourceUsage{Tasks: map[string]*api.TaskResourceUsage{}}
	for _, name := range tasks {
		out.Tasks[name] = &api.TaskResourceUsage{ResourceUsage: &api.ResourceUsage{
			CpuStats:    &api.CpuStats{},
			MemoryStats: &api.MemoryStats{},
		}}
	}
	return out
}
