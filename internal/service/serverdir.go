package service

import (
	"context"
	"fmt"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Which Nomad node is which server (feature 031). The host numbers (the sampler, Host, HostCopies,
// the disk items) are kept by Nomad node ID, and the owner knows servers by name, so a small
// directory joins the two. It is built from Nomad's nodes and the stored server rows and is
// remembered by the sampler, so a read while Nomad is down still has names.

// serverNode is one Nomad node as the host numbers see it.
type serverNode struct {
	ID string // Nomad's node ID
	// Name is the server's name: its row's when a row belongs to the node (found by the node's app
	// address), else the node's own name.
	Name string
	// NodeName is the node's own name in Nomad, which a copy carries (deploy.Copy.Node).
	NodeName string
	// Local is true for the server Caravana runs on.
	Local bool
	// Ready is Nomad's word "ready": a node that is down, disconnected or still initializing is not.
	Ready bool
	// Pool is the node's pool; apps are placed in "default".
	Pool string
}

// serverDirectory lists the Nomad nodes as servers, oldest first.
type serverDirectory struct{ nodes []serverNode }

// byName finds a node by its server's name.
func (d serverDirectory) byName(name string) (serverNode, bool) {
	for _, n := range d.nodes {
		if n.Name == name && name != "" {
			return n, true
		}
	}
	return serverNode{}, false
}

// byID finds a node by its Nomad ID.
func (d serverDirectory) byID(id string) (serverNode, bool) {
	for _, n := range d.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return serverNode{}, false
}

// local is the server Caravana runs on; ok is false when the directory is empty.
func (d serverDirectory) local() (serverNode, bool) {
	for _, n := range d.nodes {
		if n.Local {
			return n, true
		}
	}
	return serverNode{}, false
}

// multi says the cluster has more than one server, so a name is worth saying.
func (d serverDirectory) multi() bool { return len(d.nodes) > 1 }

// buildServerDirectory is the pure part. The server Caravana runs on is the first server's row
// mapped to its node by app address when there is one; else the only node; else the first node
// that is ready (the dev lab has one node and no rows).
func buildServerDirectory(rows []store.Server, nodes []deploy.Node) serverDirectory {
	out := make([]serverNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, serverNode{ID: n.ID, Name: n.Name, NodeName: n.Name, Ready: n.Status == "ready", Pool: n.Pool})
	}
	local := -1
	for _, r := range rows {
		node := nodeOf(nodes, r.Index)
		if node == nil {
			continue
		}
		for i := range out {
			if out[i].ID == node.ID {
				out[i].Name = r.Name
				if r.First {
					local = i
				}
			}
		}
	}
	if local < 0 && len(out) == 1 {
		local = 0
	}
	for i := 0; local < 0 && i < len(out); i++ {
		if out[i].Ready {
			local = i
		}
	}
	if local >= 0 {
		out[local].Local = true
	}
	return serverDirectory{nodes: out}
}

// directory reads Nomad's nodes and the server rows. When Nomad can't be read it gives the
// last directory the sampler or an earlier read made (empty before any); only a store error is
// returned.
func (s *Service) directory(ctx context.Context) (serverDirectory, error) {
	nodes, err := s.deployer.Nodes(ctx)
	if err != nil {
		st := &s.sampler
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.dir, nil
	}
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return serverDirectory{}, fmt.Errorf("list servers: %w", err)
	}
	dir := buildServerDirectory(rows, nodes)
	st := &s.sampler
	st.mu.Lock()
	st.dir = dir
	st.mu.Unlock()
	return dir, nil
}
