// Package mesh holds the addresses of a Caravana cluster's private network and the text WireGuard
// reads. It is pure: no files, no network, no programs (a test forbids those imports).
//
// Every server has an index, 1 to MaxIndex. The first server is 1. Every address of a server
// derives from its index, so no address ever comes from the console (feature 031, node.md §1).
package mesh

import (
	"fmt"
	"net/netip"
)

const (
	// MaxIndex is the highest server index: the mesh is a /24 and the container subnets are /22s of a /16.
	MaxIndex = 63
	// Port is the UDP port of every server's WireGuard interface.
	Port = 51820
)

// Addresses are what a server's index gives it.
type Addresses struct {
	Mesh       netip.Addr   // 10.98.0.N on caravana-mesh, a /32
	App        netip.Addr   // 10.99.0.N on caravana0, a /32; Nomad's network_interface
	Containers netip.Prefix // the server's Nomad bridge subnet
}

// Of returns the addresses of the server with this index. The first server keeps Nomad's default
// container subnet, so nothing already running on it moves; server N >= 2 gets 10.97.(4N).0/22.
func Of(index int) (Addresses, error) {
	if index < 1 || index > MaxIndex {
		return Addresses{}, fmt.Errorf("server index %d is outside 1 to %d", index, MaxIndex)
	}
	a := Addresses{
		Mesh: netip.AddrFrom4([4]byte{10, 98, 0, byte(index)}),
		App:  netip.AddrFrom4([4]byte{10, 99, 0, byte(index)}),
	}
	if index == 1 {
		a.Containers = netip.MustParsePrefix("172.26.64.0/20")
	} else {
		a.Containers = netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 97, byte(4 * index), 0}), 22)
	}
	return a, nil
}

// IndexOfApp reads a server's index from its app address: 10.99.0.N gives N.
func IndexOfApp(addr netip.Addr) (int, bool) {
	addr = addr.Unmap()
	if !addr.Is4() {
		return 0, false
	}
	b := addr.As4()
	if b[0] != 10 || b[1] != 99 || b[2] != 0 || b[3] < 1 || b[3] > MaxIndex {
		return 0, false
	}
	return int(b[3]), true
}

// Routes are the prefixes a server sends through caravana-mesh: every other server's mesh and app
// addresses and container subnets. The first server's container subnet is local to it, so only
// servers N >= 2 route it.
func Routes(index int) []netip.Prefix {
	if index < 1 || index > MaxIndex {
		return nil
	}
	routes := []netip.Prefix{
		netip.MustParsePrefix("10.98.0.0/24"),
		netip.MustParsePrefix("10.99.0.0/24"),
		netip.MustParsePrefix("10.97.0.0/16"),
	}
	if index >= 2 {
		routes = append(routes, netip.MustParsePrefix("172.26.64.0/20"))
	}
	return routes
}
