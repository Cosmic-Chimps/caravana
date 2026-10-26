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
