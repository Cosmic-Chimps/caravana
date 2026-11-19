package deploy

import (
	"context"
	"time"

	"github.com/hashicorp/nomad/api"
)

// Node is one Nomad node as the servers feature (031) needs it: who it is, whether it takes work
// and the address it is reached at for app ports. Nomad's types stay in this package
// (constitution VI), so everything here is a plain value.
type Node struct {
	ID, Name string
	// Status is Nomad's own word: "ready", "down", "initializing" or "disconnected".
	Status string
	// Eligible is false for a node Nomad was told not to place work on; Draining is true while its
	// work is being moved off.
	Eligible, Draining bool
	Pool               string
	// AppAddress is the address of the node's caravana0 interface ("10.99.0.3"), which tells which
	// server it is (mesh.IndexOfApp); "" when Nomad fingerprinted no such interface (the lab's node).
	AppAddress string
	MemoryMB   int // what Nomad offers for placement
	CPUMHz     int
	Cores      int
	// KeptCopies and DisconnectedSince are set only while Status is "disconnected" (Nomad stopped
	// hearing the node but its copies have a disconnect policy, see nomadjob.disconnectStrategy).
	// KeptCopies is how many copies Nomad still keeps for it: client status "unknown" with desired
	// status "run", which is to say copies that run there as far as anyone knows. Once lost_after
	// has passed Nomad marks them lost, the count falls to 0 and the node is down in all but name
	// (Nomad leaves its status at "disconnected" until it reconnects). DisconnectedSince is when
	// Nomad called the node disconnected. Both are zero when Nomad can't be asked for them.
	KeptCopies        int
	DisconnectedSince time.Time
}

// Down says Nomad has lost the node: down, or disconnected (it missed its heartbeats and the job
// allows it to come back).
func (n Node) Down() bool { return n.Status == "down" || n.Status == "disconnected" }

// Holding says Nomad has lost touch with the node but keeps its copies for it: the node is
// disconnected and at least one copy is still "unknown", within its lost_after. Nothing has been
// replaced for a service that can't start elsewhere and the copies may come back untouched, so
// this is a cut to ride out, not a server to give up on. A disconnected node with no kept copy
// has passed lost_after: it is down for every purpose that matters.
func (n Node) Holding() bool { return n.Status == "disconnected" && n.KeptCopies > 0 }

// Nodes lists every Nomad node, oldest first. A 403 is ErrNodeReadDenied (the token's policy has no
// node:read); any other failure is ErrNomadUnavailable.
func (d *Deployer) Nodes(ctx context.Context) ([]Node, error) {
	q := (&api.QueryOptions{Params: map[string]string{"resources": "true"}}).WithContext(ctx)
	stubs, _, err := d.nomad.Nodes().List(q)
	if err != nil {
		return nil, d.nodeError("list nodes", err)
	}
	out := make([]Node, 0, len(stubs))
	for i := len(stubs) - 1; i >= 0; i-- { // List sorts newest first
		stub := stubs[i]
		n := Node{
			ID: stub.ID, Name: stub.Name, Status: stub.Status, Pool: stub.NodePool,
			Eligible: stub.SchedulingEligibility != "ineligible", Draining: stub.Drain,
		}
		res := stub.NodeResources
		if res == nil && stub.Status != "down" {
			// The list leaves the resources out on a Nomad that doesn't honour the parameter.
			if info, err := d.nodeInfo(ctx, stub.ID); err == nil {
				res = info.NodeResources
			}
		}
		if res != nil {
			n.AppAddress = privateAddress(res)
			n.MemoryMB, n.CPUMHz, n.Cores = int(res.Memory.MemoryMB), int(res.Cpu.CpuShares), int(res.Cpu.TotalCpuCores)
		}
		if stub.Status == "disconnected" {
			d.fillDisconnected(ctx, &n)
		}
		out = append(out, n)
	}
	return out, nil
}

// fillDisconnected sets KeptCopies and DisconnectedSince of a disconnected node. It reads the node
// and its allocations fresh, not through nodeInfo's cache: the cached record is from before the
// status changed. A failure leaves the fields zero: the list must not fail because a detail of one
// node couldn't be read, and a node with no kept copies reads as down, the safe side.
//
// DisconnectedSince is the record's StatusUpdatedAt, which Nomad's state store sets whenever it
// writes the node's status. The node's event list says "Node heartbeat missed" too, but it keeps
// only the newest ten and may hold the same event twice, so it is no steadier; it is the
// fallback for a record without the time.
func (d *Deployer) fillDisconnected(ctx context.Context, n *Node) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	if info, _, err := d.nomad.Nodes().Info(n.ID, q); err == nil && info != nil {
		switch {
		case info.StatusUpdatedAt > 0:
			n.DisconnectedSince = time.Unix(info.StatusUpdatedAt, 0).UTC()
		default:
			for _, e := range info.Events {
				if e != nil && e.Message == heartbeatMissed && e.Timestamp.After(n.DisconnectedSince) {
					n.DisconnectedSince = e.Timestamp.UTC()
				}
			}
		}
	}
	allocs, _, err := d.nomad.Nodes().Allocations(n.ID, q)
	if err != nil {
		return
	}
	for _, a := range allocs {
		if a != nil && a.ClientStatus == api.AllocClientStatusUnknown &&
			(a.DesiredStatus == "" || a.DesiredStatus == api.AllocDesiredStatusRun) {
			n.KeptCopies++
		}
	}
}

// heartbeatMissed is the message of the node event Nomad writes when a node stops answering.
const heartbeatMissed = "Node heartbeat missed"
