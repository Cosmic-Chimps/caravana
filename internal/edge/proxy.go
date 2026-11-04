package edge

import (
	"net/netip"
	"slices"
)

// ProxyCloudflare is DNS.Proxy's value for addresses inside Cloudflare's ranges.
const ProxyCloudflare = "Cloudflare"

// cloudflareRanges are Cloudflare's published address ranges, hard-coded so the
// package stays pure (no network call). Source: https://www.cloudflare.com/ips-v4
// and https://www.cloudflare.com/ips-v6, read 2026-10-26. A name behind
// Cloudflare's proxy ("orange cloud") resolves to these, not to the host.
var cloudflareRanges = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
)

func mustPrefixes(in ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(in))
	for i, s := range in {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func inCloudflare(a netip.Addr) bool {
	return slices.ContainsFunc(cloudflareRanges, func(p netip.Prefix) bool { return p.Contains(a.Unmap()) })
}

// ProxyOf names the proxy the addresses in seen that aren't expected (this
// host's own) belong to: "Cloudflare" when there is at least one such address
// and every one is inside Cloudflare's ranges, else "". A mix of Cloudflare and
// other foreign addresses is not one proxy, so it is "".
func ProxyOf(seen, expected []netip.Addr) string {
	want := make([]netip.Addr, len(expected))
	for i, a := range expected {
		want[i] = a.Unmap()
	}
	foreign := 0
	for _, a := range seen {
		a = a.Unmap()
		if slices.Contains(want, a) {
			continue
		}
		if !inCloudflare(a) {
			return ""
		}
		foreign++
	}
	if foreign == 0 {
		return ""
	}
	return ProxyCloudflare
}

// Fronted says the addresses DNS answered can all be something in front of
// this host, so a trusted certificate for the host proves they forward here.
// It is true when every foreign address is Cloudflare's (ProxyOf), when none of
// the addresses seen is one of this host's own (another CDN, an address we
// don't know), when the answer holds nothing but this host's own addresses, and
// when nothing was seen.
//
// It is false when the answer holds one of this host's own addresses and an
// address that is neither this host's nor a known proxy's. A proxy never hands
// out the origin's own address, so that mix is not a proxy: an old A record
// left beside the new one, or an AAAA record to another server. Some visitors
// reach that other host, whatever the certificate says. Like ProxyOf it is a
// pure function of Seen and Expected, so a stored check recomputes it.
func (d DNS) Fronted() bool {
	want := make([]netip.Addr, len(d.Expected))
	for i, a := range d.Expected {
		want[i] = a.Unmap()
	}
	here, stranger := false, false
	for _, a := range d.Seen {
		a = a.Unmap()
		switch {
		case slices.Contains(want, a):
			here = true
		case !inCloudflare(a):
			stranger = true
		}
	}
	return !(here && stranger)
}
