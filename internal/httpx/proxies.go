// Package httpx holds the small HTTP helpers both the API and the pages need:
// who the client is behind a proxy, the session cookie, and the request log.
// Keeping them here means neither of those packages imports the other.
package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Proxies is the parsed CARAVANA_TRUSTED_PROXIES list: the addresses whose
// X-Forwarded-For and X-Forwarded-Proto headers Caravana believes. The zero
// value (nobody) is the safe default.
type Proxies []netip.Prefix

// ParseProxies reads a comma-separated list of IP addresses and CIDR ranges.
// A bare address becomes a /32 (or /128). Blank entries are skipped; an entry
// that is neither an address nor a range is an error naming it.
func ParseProxies(s string) (Proxies, error) {
	var out Proxies
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			p, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("%q is not an IP address or CIDR range", entry)
			}
			out = append(out, netip.PrefixFrom(p.Addr().Unmap(), unmapBits(p)).Masked())
			continue
		}
		a, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR range", entry)
		}
		a = a.Unmap().WithZone("")
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// unmapBits is the prefix length of p once its address is unmapped: an
// IPv4-mapped IPv6 range like ::ffff:10.0.0.0/104 is the IPv4 range /8.
func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		return p.Bits() - 96
	}
	return p.Bits()
}

func (p Proxies) trusts(a netip.Addr) bool {
	for _, prefix := range p {
		if prefix.Contains(a) {
			return true
		}
	}
	return false
}

// peer is the address of the connection itself.
func peer(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}

// ClientAddress is the address the sign-in limit counts: the IPv4 address, or
// the IPv6 /64 as a prefix string (one host can use a whole /64).
//
// Forwarded headers count only when the connection comes from a trusted
// proxy; then the client is the right-most X-Forwarded-For entry that isn't
// itself a trusted proxy (the entries to its left were written by the client
// and can be anything). If every entry is trusted, it is the left-most one.
// A malformed entry makes the answer the proxy's own address, which can only
// make clients share a limit, never dodge one.
func (p Proxies) ClientAddress(r *http.Request) string {
	from, ok := peer(r)
	if !ok {
		return "unknown"
	}
	if !p.trusts(from) {
		return reduce(from)
	}

	var entries []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		for e := range strings.SplitSeq(line, ",") {
			entries = append(entries, strings.TrimSpace(e))
		}
	}
	client := from
	for i := len(entries) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(entries[i])
		if err != nil {
			return reduce(from)
		}
		client = a.Unmap().WithZone("")
		if !p.trusts(client) {
			break
		}
	}
	return reduce(client)
}

// reduce turns an address into what the limit counts.
func reduce(a netip.Addr) string {
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}

// HTTPS reports whether the client reached Caravana over HTTPS: the
// connection itself is TLS, or a trusted proxy says so in X-Forwarded-Proto.
func (p Proxies) HTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	from, ok := peer(r)
	if !ok || !p.trusts(from) {
		return false
	}
	values := r.Header.Values("X-Forwarded-Proto")
	if len(values) == 0 {
		return false
	}
	// The right-most value is the one the nearest proxy wrote.
	last := values[len(values)-1]
	if i := strings.LastIndex(last, ","); i >= 0 {
		last = last[i+1:]
	}
	// A websocket upgrade arrives with "wss" from Traefik, not "https": the
	// terminal's request over TLS (seen on a host, 2026-10-19, where it read as
	// plain HTTP and lost its __Host- cookie).
	proto := strings.TrimSpace(last)
	return strings.EqualFold(proto, "https") || strings.EqualFold(proto, "wss")
}
