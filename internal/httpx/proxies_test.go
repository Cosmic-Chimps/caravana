package httpx

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func mustProxies(t *testing.T, s string) Proxies {
	t.Helper()
	p, err := ParseProxies(s)
	if err != nil {
		t.Fatalf("ParseProxies(%q): %v", s, err)
	}
	return p
}

// request builds a request from the given peer with the given headers
// (name, value pairs).
func request(peerAddr string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.RemoteAddr = peerAddr
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	return r
}

func TestParseProxies(t *testing.T) {
	tests := []struct {
		in      string
		wantLen int
		wantErr string // substring of the error, "" for none
	}{
		{"", 0, ""},
		{"  ", 0, ""},
		{"10.0.0.1", 1, ""},
		{"10.0.0.0/8, 192.168.1.5 ,::1", 3, ""},
		{"fd00::/8", 1, ""},
		{"10.0.0.1,,", 1, ""},
		{"10.0.0.1,traefik", 0, `"traefik"`},
		{"10.0.0.0/33", 0, `"10.0.0.0/33"`},
		{"300.1.1.1", 0, `"300.1.1.1"`},
	}
	for _, tc := range tests {
		got, err := ParseProxies(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseProxies(%q) error = %v, want one naming %s", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil || len(got) != tc.wantLen {
			t.Errorf("ParseProxies(%q) = %v, %v; want %d entries", tc.in, got, err, tc.wantLen)
		}
	}

	// A bare IP is a single-address range.
	p := mustProxies(t, "10.0.0.1")
	if p.trusts(mustAddr("10.0.0.2")) || !p.trusts(mustAddr("10.0.0.1")) {
		t.Error("a bare IP should trust exactly that address")
	}
}

func TestClientAddress(t *testing.T) {
	traefik := mustProxies(t, "10.0.0.5, 10.0.1.0/24")

	tests := []struct {
		name    string
		proxies Proxies
		req     *http.Request
		want    string
	}{
		{"direct client", nil, request("203.0.113.9:5000"), "203.0.113.9"},
		{"untrusted peer spoofing the header", traefik,
			request("203.0.113.9:5000", "X-Forwarded-For", "198.51.100.1"), "203.0.113.9"},
		{"no trusted proxies at all", nil,
			request("203.0.113.9:5000", "X-Forwarded-For", "198.51.100.1"), "203.0.113.9"},
		{"trusted peer, one entry", traefik,
			request("10.0.0.5:5000", "X-Forwarded-For", "198.51.100.1"), "198.51.100.1"},
		{"trusted peer, client-supplied prefix is ignored", traefik,
			request("10.0.0.5:5000", "X-Forwarded-For", "6.6.6.6, 198.51.100.1"), "198.51.100.1"},
		{"trusted peer, chain through another proxy", traefik,
			request("10.0.0.5:5000", "X-Forwarded-For", "6.6.6.6, 198.51.100.1, 10.0.1.7"), "198.51.100.1"},
		{"entries split over two header lines", traefik,
			request("10.0.0.5:5000", "X-Forwarded-For", "6.6.6.6", "X-Forwarded-For", "198.51.100.1"), "198.51.100.1"},
		{"everything in the chain is trusted", traefik,
			request("10.0.0.5:5000", "X-Forwarded-For", "10.0.1.3, 10.0.1.7"), "10.0.1.3"},
		{"trusted peer without the header", traefik, request("10.0.0.5:5000"), "10.0.0.5"},
		{"malformed entry falls back to the proxy", traefik,
			request("10.0.0.5:5000", "X-Forwarded-For", "198.51.100.1, not-an-ip"), "10.0.0.5"},
		{"IPv6 client counts per /64", nil,
			request("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:5000"), "2001:db8:1:2::/64"},
		{"two hosts in one /64 share it", nil,
			request("[2001:db8:1:2:1::1]:5000"), "2001:db8:1:2::/64"},
		{"IPv6 behind a trusted proxy", traefik,
			request("10.0.0.5:5000", "X-Forwarded-For", "2001:db8:ffff:1::7"), "2001:db8:ffff:1::/64"},
		{"IPv4-mapped IPv6 is IPv4", nil, request("[::ffff:203.0.113.9]:5000"), "203.0.113.9"},
		{"unparsable peer", nil, request("@"), "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.proxies.ClientAddress(tc.req); got != tc.want {
				t.Errorf("ClientAddress = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHTTPS(t *testing.T) {
	traefik := mustProxies(t, "10.0.0.5")

	tests := []struct {
		name    string
		proxies Proxies
		req     *http.Request
		want    bool
	}{
		{"plain connection", nil, request("203.0.113.9:1"), false},
		{"untrusted peer claiming https", traefik,
			request("203.0.113.9:1", "X-Forwarded-Proto", "https"), false},
		{"trusted proxy says https", traefik,
			request("10.0.0.5:1", "X-Forwarded-Proto", "https"), true},
		{"trusted proxy says http", traefik,
			request("10.0.0.5:1", "X-Forwarded-Proto", "http"), false},
		{"trusted proxy, chain ends in https", traefik,
			request("10.0.0.5:1", "X-Forwarded-Proto", "http, https"), true},
		{"trusted proxy, chain ends in http", traefik,
			request("10.0.0.5:1", "X-Forwarded-Proto", "https, http"), false},
		{"trusted proxy, no header", traefik, request("10.0.0.5:1"), false},
		// Traefik writes the websocket scheme on an upgrade (the terminal).
		{"trusted proxy says wss", traefik,
			request("10.0.0.5:1", "X-Forwarded-Proto", "wss"), true},
		{"trusted proxy says ws", traefik,
			request("10.0.0.5:1", "X-Forwarded-Proto", "ws"), false},
		{"untrusted peer claiming wss", traefik,
			request("203.0.113.9:1", "X-Forwarded-Proto", "wss"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.proxies.HTTPS(tc.req); got != tc.want {
				t.Errorf("HTTPS = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("a TLS connection", func(t *testing.T) {
		r := request("203.0.113.9:1")
		r.TLS = &tls.ConnectionState{}
		if !Proxies(nil).HTTPS(r) {
			t.Error("HTTPS = false on a TLS connection")
		}
	})
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
