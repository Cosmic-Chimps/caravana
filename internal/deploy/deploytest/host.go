package deploytest

import (
	"net/http"
	"slices"
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
	failWith int         // non-zero: every node read answers it
	extra    []*api.Node // the nodes AddNode made, oldest first
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

// SetNodeFacts sets what the node record says about the machine (feature 027):
// its attributes (see LabNodeAttributes), its node pool and its events. It
// changes nothing else about the node; call SetNode first.
func (n *Nomad) SetNodeFacts(attrs map[string]string, pool string, events []*api.NodeEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.host.node.Attributes = attrs
	n.host.node.NodePool = pool
	n.host.node.Events = events
}

// SetNodeStatusAt sets when Nomad last changed the node's status (the node record's
// StatusUpdatedAt): for a node that is down, when it was marked down. Call SetNode first.
func (n *Nomad) SetNodeStatusAt(t time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.host.node.StatusUpdatedAt = t.Unix()
}

// SetNodeNetworks sets the interfaces the node record lists (NodeResources.Networks, as Nomad
// fingerprints them). Call SetNode first.
func (n *Nomad) SetNodeNetworks(nets []*api.NetworkResource) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.host.node.NodeResources != nil {
		n.host.node.NodeResources.Networks = nets
	}
}

// LabNodeNetworks is what the lab's node lists: Lima's eth0, no caravana0 (the lab doesn't make it).
func LabNodeNetworks() []*api.NetworkResource {
	return []*api.NetworkResource{
		{Mode: "bridge"},
		{Mode: "host", Device: "eth0", IP: "192.168.5.15", CIDR: "192.168.5.15/32"},
	}
}

// Caravana0Networks is what an installed host lists: the private interface alone, at ip.
func Caravana0Networks(ip string) []*api.NetworkResource {
	return []*api.NetworkResource{
		{Mode: "bridge"},
		{Mode: "host", Device: "caravana0", IP: ip, CIDR: ip + "/32"},
	}
}

// SetNodeScheduling sets the node's drain flag and scheduling eligibility
// ("eligible" or "ineligible"), as the node list and Info answer them. Call
// SetNode first.
func (n *Nomad) SetNodeScheduling(drain bool, eligibility string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.host.node.Drain = drain
	n.host.node.SchedulingEligibility = eligibility
	if drain {
		n.host.node.DrainStrategy = &api.DrainStrategy{}
	} else {
		n.host.node.DrainStrategy = nil
	}
}

// SetTraefikJob registers a job "traefik" whose one task runs image, as the
// installer's Traefik job does.
func (n *Nomad) SetTraefikJob(image string) {
	n.AddForeignJob(&api.Job{
		ID: new("traefik"), Name: new("traefik"),
		TaskGroups: []*api.TaskGroup{{
			Name:  new("traefik"),
			Tasks: []*api.Task{{Name: "traefik", Driver: "docker", Config: map[string]any{"image": image}}},
		}},
	})
}

// SetHostStats sets what GET /v1/client/stats answers (Uptime included).
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
	if !h.set && len(h.extra) == 0 {
		if r.URL.Path == "/v1/nodes" {
			n.writeJSON(w, []*api.NodeListStub{})
			return
		}
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	switch {
	case r.URL.Path == "/v1/nodes":
		stubs := []*api.NodeListStub{}
		if h.set {
			stubs = append(stubs, &api.NodeListStub{
				ID: h.node.ID, Name: h.node.Name, Status: h.status, CreateIndex: 1,
				Drain: h.node.Drain, SchedulingEligibility: h.node.SchedulingEligibility, NodePool: h.node.NodePool,
				NodeResources: h.node.NodeResources, ReservedResources: h.node.ReservedResources,
			})
		}
		for i, x := range h.extra {
			stubs = append(stubs, &api.NodeListStub{
				ID: x.ID, Name: x.Name, Status: x.Status, CreateIndex: uint64(i + 2),
				Drain: x.Drain, SchedulingEligibility: x.SchedulingEligibility, NodePool: x.NodePool,
				NodeResources: x.NodeResources, ReservedResources: x.ReservedResources,
			})
		}
		n.writeJSON(w, stubs)
	case r.URL.Path == "/v1/client/stats":
		if !h.set || r.URL.Query().Get("node_id") != h.node.ID {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		n.writeJSON(w, h.stats)
	default:
		id := strings.TrimPrefix(r.URL.Path, "/v1/node/")
		if h.set && id == h.node.ID {
			n.writeJSON(w, h.node)
			return
		}
		for _, x := range h.extra {
			if x.ID == id {
				n.writeJSON(w, x)
				return
			}
		}
		http.Error(w, "node not found", http.StatusNotFound)
	}
}

// AddNode adds a node beside the one SetNode makes, as a joined server's Nomad client would
// register: its ID, name, status ("ready", "down") and the app address of its caravana0
// interface ("10.99.0.3"; "" for none). It offers 4000 MHz on 2 cores and 4096 MB. Adding an ID
// again replaces the node.
func (n *Nomad) AddNode(id, name, status, appAddress string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	res := &api.NodeResources{
		Cpu:    api.NodeCpuResources{CpuShares: 4000, TotalCpuCores: 2},
		Memory: api.NodeMemoryResources{MemoryMB: 4096},
	}
	if appAddress != "" {
		res.Networks = Caravana0Networks(appAddress)
	}
	node := &api.Node{ID: id, Name: name, Status: status, NodePool: "default", SchedulingEligibility: "eligible", NodeResources: res}
	for i, x := range n.host.extra {
		if x.ID == id {
			n.host.extra[i] = node
			return
		}
	}
	n.host.extra = append(n.host.extra, node)
}

// SetNodeStatusOf changes the status of a node AddNode made ("ready", "down", "disconnected").
func (n *Nomad) SetNodeStatusOf(id, status string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, x := range n.host.extra {
		if x.ID == id {
			x.Status = status
		}
	}
}

// SetNodeEligibilityOf makes a node AddNode made eligible or not ("eligible", "ineligible") and
// sets its drain flag.
func (n *Nomad) SetNodeEligibilityOf(id, eligibility string, drain bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, x := range n.host.extra {
		if x.ID == id {
			x.SchedulingEligibility, x.Drain = eligibility, drain
		}
	}
}

// RemoveNode takes a node AddNode made out of Nomad's list.
func (n *Nomad) RemoveNode(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.host.extra = slices.DeleteFunc(n.host.extra, func(x *api.Node) bool { return x.ID == id })
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

// LabNodeAttributes is what the lab's node reports about the machine (Ubuntu
// 26.04 on a 2-core ARM virtual machine: no CPU model and no per-core MHz).
func LabNodeAttributes() map[string]string {
	return map[string]string{
		"os.name": "ubuntu", "os.version": "26.04",
		"kernel.version": "7.0.0-14-generic", "kernel.arch": "arm64",
		"nomad.version": "2.0.1", "driver.docker.version": "29.1.3",
		"cpu.numcores": "2", "cpu.totalcompute": "4000",
		"unique.network.ip-address": "192.168.5.15",
	}
}

// X86NodeAttributes is a small x86 server's: like the lab's, with the CPU's
// model and per-core MHz that x86 hosts report.
func X86NodeAttributes() map[string]string {
	a := LabNodeAttributes()
	a["os.version"], a["kernel.version"], a["kernel.arch"] = "24.04", "6.8.0-88-generic", "amd64"
	a["cpu.numcores"], a["cpu.totalcompute"] = "4", "9600"
	a["cpu.modelname"], a["cpu.frequency"] = "AMD EPYC 7763 64-Core Processor", "2400"
	a["unique.network.ip-address"] = "10.0.0.7"
	return a
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
