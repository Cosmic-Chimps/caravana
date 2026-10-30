package deploy

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// The node's own facts (feature 027): what the server page shows about the
// machine Caravana's apps run on. They come from the node record HostStats
// already caches for 10 minutes and from the stats call the sampler already
// makes every minute: no new endpoint and no new capability (research R1).

// NodeFacts is what Nomad says about the node itself. A fact Nomad doesn't
// report is empty or zero, never a made-up value, and the page shows only what
// is known.
type NodeFacts struct {
	Name string
	// State is "ready", "draining", "ineligible", "down" or "initializing",
	// worked out from the node's status, its drain and its eligibility.
	State string
	Pool  string // the node pool, "default"

	OS, OSVersion string // "ubuntu", "24.04"
	Kernel, Arch  string // the running kernel's release and its architecture
	NomadVersion  string
	DockerVersion string

	Cores    int
	TotalMHz int
	// CPUModel and CoreMHz are absent on the ARM hosts' virtual machines.
	CPUModel string
	CoreMHz  int
	MemoryMB int

	// Address is the node's private address; the public one is the console's
	// host, which Nomad doesn't know.
	Address string
	// LastHeartbeat is the last time Caravana heard from the node: the moment
	// its stats were read or, when they weren't, when its status last changed.
	LastHeartbeat time.Time
	// Events are the node's own events (registered, heartbeat missed, back), in
	// the order Nomad keeps them: oldest first.
	Events []NodeEvent
	// TraefikVersion is the tag of the traefik job's image. The node's record
	// doesn't have it: the service fills it from TraefikImage.
	TraefikVersion string
}

// NodeEvent is one entry of a node's event list, in Nomad's words.
type NodeEvent struct {
	Time      time.Time
	Subsystem string
	Message   string
}

// nodeFacts reads a node record. seen is when the node last answered (zero
// when that isn't known).
func nodeFacts(n *api.Node, seen time.Time) NodeFacts {
	a := n.Attributes
	f := NodeFacts{
		Name: n.Name, State: nodeState(n), Pool: n.NodePool,
		OS: a["os.name"], OSVersion: a["os.version"], Kernel: a["kernel.version"], Arch: a["kernel.arch"],
		NomadVersion: a["nomad.version"], DockerVersion: a["driver.docker.version"],
		Cores:    attrNumber(a["cpu.numcores"]),
		TotalMHz: attrNumber(a["cpu.totalcompute"]),
		CPUModel: a["cpu.modelname"], CoreMHz: attrNumber(a["cpu.frequency"]),
		Address: a["unique.network.ip-address"],
	}
	if r := n.NodeResources; r != nil {
		if f.Cores == 0 {
			f.Cores = int(r.Cpu.TotalCpuCores)
		}
		if f.TotalMHz == 0 {
			f.TotalMHz = int(r.Cpu.CpuShares)
		}
		f.MemoryMB = int(r.Memory.MemoryMB)
	}
	switch {
	case !seen.IsZero():
		f.LastHeartbeat = seen
	case n.StatusUpdatedAt > 0:
		f.LastHeartbeat = time.Unix(n.StatusUpdatedAt, 0).UTC()
	}
	for _, e := range n.Events {
		if e != nil {
			f.Events = append(f.Events, NodeEvent{Time: e.Timestamp.UTC(), Subsystem: e.Subsystem, Message: e.Message})
		}
	}
	return f
}

// nodeState is the one word for a node: Nomad's down and initializing win over
// the rest, then a drain, then ineligibility.
func nodeState(n *api.Node) string {
	switch {
	case n.Status == "down":
		return "down"
	case n.Status == "initializing":
		return "initializing"
	case n.Drain || n.DrainStrategy != nil:
		return "draining"
	case n.SchedulingEligibility == "ineligible":
		return "ineligible"
	}
	return "ready"
}

// attrNumber reads a Nomad attribute that holds a number ("4000", "2399.998")
// in whole units, rounded: 0 when it is missing or isn't one.
func attrNumber(s string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0
	}
	return int(f + 0.5)
}

// bootTime is when the node started: the moment its stats were read less its
// uptime, in whole seconds. Nomad's own stats carry a timestamp too, which the
// Go client doesn't expose; the moment Caravana got the answer is within a few
// seconds of it, and the sampler only treats a move of over a minute as a boot.
func bootTime(read time.Time, uptimeSeconds uint64) time.Time {
	return read.Add(-time.Duration(uptimeSeconds) * time.Second).UTC().Truncate(time.Second)
}

// NodeFacts reads the node without its stats: the first node that is ready,
// else the first node there is, so a node that is down still shows what it is.
// It is the fallback when HostStats can't read a node's stats.
func (d *Deployer) NodeFacts(ctx context.Context) (NodeFacts, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	nodes, _, err := d.nomad.Nodes().List(q)
	if err != nil {
		return NodeFacts{}, d.nodeError("list nodes", err)
	}
	var stub *api.NodeListStub
	for _, n := range nodes {
		if n != nil && (stub == nil || (stub.Status != "ready" && n.Status == "ready")) {
			stub = n
		}
	}
	if stub == nil {
		return NodeFacts{}, fmt.Errorf("%w: Nomad has no node", ErrNomadUnavailable)
	}
	info, err := d.nodeInfo(ctx, stub.ID)
	if err != nil {
		return NodeFacts{}, err
	}
	// The list's status is current; the cached record may be 10 minutes old.
	f := nodeFacts(info, time.Time{})
	f.State = nodeState(&api.Node{Status: stub.Status, Drain: stub.Drain, SchedulingEligibility: stub.SchedulingEligibility})
	return f, nil
}

// cachedImage is the traefik job's image and when it was read.
type cachedImage struct {
	at    time.Time
	image string
}

// TraefikImage is the image of the traefik job's main task as the job has it
// ("traefik:v3.7.13"), read from Nomad and kept for 10 minutes like the node's
// resources. It is "" with no error when there is no such job or it names no
// image: Traefik may be run another way. ImageVersion gives the tag.
func (d *Deployer) TraefikImage(ctx context.Context) (string, error) {
	c := &d.cache
	c.mu.Lock()
	hit := c.traefik
	fresh := !hit.at.IsZero() && c.clock().Sub(hit.at) < nodeInfoTTL
	c.mu.Unlock()
	if fresh {
		return hit.image, nil
	}
	job, _, err := d.nomad.Jobs().Info("traefik", (&api.QueryOptions{}).WithContext(ctx))
	image := ""
	switch {
	case err == nil:
		image = jobImage(job, "traefik")
	case statusOf(err) == http.StatusNotFound:
		// no such job: image stays empty
	default:
		return "", d.callError("read the traefik job", err)
	}
	c.mu.Lock()
	c.traefik = cachedImage{at: c.clock(), image: image}
	c.mu.Unlock()
	return image, nil
}

// jobImage is the docker image of the task named task, else of the first task
// that has one.
func jobImage(job *api.Job, task string) string {
	first := ""
	for _, g := range job.TaskGroups {
		if g == nil {
			continue
		}
		for _, t := range g.Tasks {
			if t == nil {
				continue
			}
			img, _ := t.Config["image"].(string)
			if img == "" {
				continue
			}
			if t.Name == task {
				return img
			}
			if first == "" {
				first = img
			}
		}
	}
	return first
}

// ImageVersion is the tag of an image reference: "v3.7.13" for
// "traefik:v3.7.13" and for "traefik:v3.7.13@sha256:…"; "" when it has none (a
// bare name, or a digest alone). A port in the registry's host is not a tag.
func ImageVersion(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return ""
	}
	return ref[i+1:]
}
