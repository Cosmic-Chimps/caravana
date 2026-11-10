package deploy

import (
	"context"

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
}

// Down says Nomad has lost the node: down, or disconnected (it missed its heartbeats and the job
// allows it to come back).
func (n Node) Down() bool { return n.Status == "down" || n.Status == "disconnected" }

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
		out = append(out, n)
	}
	return out, nil
}
