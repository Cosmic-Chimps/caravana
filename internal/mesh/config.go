package mesh

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Peer is another server as this one needs to know it.
type Peer struct {
	Index     int    `json:"index"`
	PublicKey string `json:"publicKey"`
	Endpoint  string `json:"endpoint"` // "ip:51820", a literal address
}

// ParseEndpoint reads "ip:port". It takes a literal address only, never a name (so nothing is
// resolved on the way), and refuses an address nobody could reach a server at: loopback,
// unspecified, multicast or link-local.
func ParseEndpoint(s string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("an endpoint is an IP address and a port, like 203.0.113.4:%d", Port)
	}
	addr := ap.Addr().Unmap()
	switch {
	case ap.Port() == 0:
		return netip.AddrPort{}, fmt.Errorf("the endpoint has no port")
	case addr.Zone() != "":
		return netip.AddrPort{}, fmt.Errorf("the endpoint's address has a zone")
	case addr.IsLoopback(), addr.IsUnspecified(), addr.IsMulticast(),
		addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast():
		return netip.AddrPort{}, fmt.Errorf("the endpoint's address isn't one another server can reach")
	}
	return netip.AddrPortFrom(addr, ap.Port()), nil
}

// Config is the text `wg setconf` and `wg syncconf` read: the interface with its private key and
// listen port, then a [Peer] per peer, sorted by index. A peer's allowed addresses are its mesh
// /32, its app /32 and its container subnet, all derived from its index.
//
// A peer whose index, public key or endpoint is invalid is left out, so a value that was never
// checked can't put a line of its own into the file. Callers validate first and refuse the whole
// answer (nodeagent's validate.go).
func Config(privateKey string, peers []Peer) string {
	sorted := make([]Peer, len(peers))
	copy(sorted, peers)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Index < sorted[j].Index })

	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", privateKey)
	fmt.Fprintf(&b, "ListenPort = %d\n", Port)
	for _, p := range sorted {
		addrs, err := Of(p.Index)
		if err != nil || !ValidPublicKey(p.PublicKey) {
			continue
		}
		ep, err := ParseEndpoint(p.Endpoint)
		if err != nil {
			continue
		}
		b.WriteString("\n[Peer]\n")
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		fmt.Fprintf(&b, "Endpoint = %s\n", ep)
		fmt.Fprintf(&b, "AllowedIPs = %s/32, %s/32, %s\n", addrs.Mesh, addrs.App, addrs.Containers)
		b.WriteString("PersistentKeepalive = 25\n")
	}
	return b.String()
}
