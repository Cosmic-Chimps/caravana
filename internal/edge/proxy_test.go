package edge

import (
	"net"
	"net/netip"
	"testing"
)

func TestProxyOf(t *testing.T) {
	host := addrs("152.53.190.88")
	tests := []struct {
		name string
		seen []netip.Addr
		want string
	}{
		{"cloudflare IPv4", addrs("188.114.96.3", "104.21.71.169", "172.67.147.113"), ProxyCloudflare},
		{"cloudflare IPv6", addrs("2a06:98c1:3120::3"), ProxyCloudflare},
		{"cloudflare both families", addrs("104.21.71.169", "2a06:98c1:3120::3"), ProxyCloudflare},
		{"the host's own address among cloudflare ones", addrs("152.53.190.88", "188.114.96.3"), ProxyCloudflare},
		{"mixed with another foreign address", addrs("188.114.96.3", "198.51.100.9"), ""},
		{"not cloudflare", addrs("198.51.100.9"), ""},
		{"only the host's own address", addrs("152.53.190.88"), ""},
		{"nothing seen", nil, ""},
		{"just outside a range", addrs("188.114.112.1"), ""},
		{"IPv4 in IPv6 form", []netip.Addr{netip.MustParseAddr("::ffff:188.114.96.3")}, ProxyCloudflare},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProxyOf(tt.seen, host); got != tt.want {
				t.Errorf("ProxyOf = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyDNSSetsProxy(t *testing.T) {
	host := addrs("152.53.190.88")
	answer := func(ips ...string) Answer {
		var a Answer
		for _, s := range ips {
			a.Addrs = append(a.Addrs, net.IPAddr{IP: net.ParseIP(s)})
		}
		return a
	}
	for _, tt := range []struct {
		name   string
		answer Answer
		status string
		proxy  string
	}{
		{"cloudflare", answer("188.114.96.3", "2a06:98c1:3120::3"), DNSElsewhere, ProxyCloudflare},
		{"mixed", answer("188.114.96.3", "198.51.100.9"), DNSElsewhere, ""},
		{"the host's own", answer("152.53.190.88"), DNSOK, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyDNS([]Answer{tt.answer}, host)
			if got.Status != tt.status || got.Proxy != tt.proxy {
				t.Errorf("status %q proxy %q, want %q %q", got.Status, got.Proxy, tt.status, tt.proxy)
			}
		})
	}
}

func TestFronted(t *testing.T) {
	v4 := addrs("152.53.190.88")
	both := addrs("152.53.190.88", "2001:db8::1")
	tests := []struct {
		name     string
		seen     []netip.Addr
		expected []netip.Addr
		want     bool
	}{
		{"host and cloudflare IPv4", addrs("152.53.190.88", "188.114.96.3"), v4, true},
		{"host and cloudflare IPv6", addrs("152.53.190.88", "2a06:98c1:3120::3"), v4, true},
		{"host and an unknown address", addrs("152.53.190.88", "198.51.100.9"), v4, false},
		{"host IPv4 and an unknown AAAA", addrs("152.53.190.88", "2001:db8::9"), v4, false},
		{"unknown only", addrs("198.51.100.9"), v4, true},
		{"cloudflare only", addrs("188.114.96.3"), v4, true},
		{"nothing seen", nil, v4, true},
		{"host only", addrs("152.53.190.88"), v4, true},
		{"host IPv6 and an unknown address", addrs("2001:db8::1", "198.51.100.9"), both, false},
		{"IPv4-in-IPv6 form of the host and an unknown address", []netip.Addr{netip.MustParseAddr("::ffff:152.53.190.88"), netip.MustParseAddr("198.51.100.9")}, v4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DNS{Status: DNSElsewhere, Seen: tt.seen, Expected: tt.expected}
			if got := d.Fronted(); got != tt.want {
				t.Errorf("Fronted = %v, want %v", got, tt.want)
			}
		})
	}
}
