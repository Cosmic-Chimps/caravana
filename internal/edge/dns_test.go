package edge

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

func TestCheckDNS(t *testing.T) {
	const host = "shop.example.com"
	here := map[string][]string{host: {"203.0.113.7"}}
	elsewhere := map[string][]string{host: {"198.51.100.9"}}
	nxdomain := map[string][]string{}
	failing := map[string]error{host: errors.New("i/o timeout")}

	tests := []struct {
		name         string
		resolvers    []edgetest.FakeResolver
		expected     []netip.Addr
		want         string
		wantSeen     []netip.Addr
		wantExpected []netip.Addr
	}{
		{
			name:      "every resolver points here",
			resolvers: []edgetest.FakeResolver{{Hosts: here}, {Hosts: here}, {Hosts: here}},
			expected:  addrs("203.0.113.7"), want: DNSOK, wantSeen: addrs("203.0.113.7"),
		},
		{
			name:      "one resolver points elsewhere",
			resolvers: []edgetest.FakeResolver{{Hosts: here}, {Hosts: elsewhere}, {Hosts: here}},
			expected:  addrs("203.0.113.7"), want: DNSElsewhere, wantSeen: addrs("198.51.100.9", "203.0.113.7"),
		},
		{
			name:      "all point elsewhere",
			resolvers: []edgetest.FakeResolver{{Hosts: elsewhere}},
			expected:  addrs("203.0.113.7"), want: DNSElsewhere, wantSeen: addrs("198.51.100.9"),
		},
		{
			name:      "right A and a stale AAAA",
			resolvers: []edgetest.FakeResolver{{Hosts: map[string][]string{host: {"203.0.113.7", "2001:db8::1"}}}},
			expected:  addrs("203.0.113.7"), want: DNSElsewhere, wantSeen: addrs("203.0.113.7", "2001:db8::1"),
		},
		{
			name:      "A and AAAA both this host's",
			resolvers: []edgetest.FakeResolver{{Hosts: map[string][]string{host: {"203.0.113.7", "2001:db8::1"}}}},
			expected:  addrs("203.0.113.7", "2001:db8::1"), want: DNSOK, wantSeen: addrs("203.0.113.7", "2001:db8::1"),
		},
		{
			name:      "IPv4-mapped IPv6 answer is the IPv4 address",
			resolvers: []edgetest.FakeResolver{{Hosts: map[string][]string{host: {"::ffff:203.0.113.7"}}}},
			expected:  addrs("203.0.113.7"), want: DNSOK, wantSeen: addrs("203.0.113.7"),
		},
		{
			name:      "NXDOMAIN on all",
			resolvers: []edgetest.FakeResolver{{Hosts: nxdomain}, {Hosts: nxdomain}},
			expected:  addrs("203.0.113.7"), want: DNSNone,
		},
		{
			name:      "NXDOMAIN on one while the others have the record is lag",
			resolvers: []edgetest.FakeResolver{{Hosts: nxdomain}, {Hosts: here}},
			expected:  addrs("203.0.113.7"), want: DNSOK, wantSeen: addrs("203.0.113.7"),
		},
		{
			name:      "NXDOMAIN and a timeout is none",
			resolvers: []edgetest.FakeResolver{{Hosts: nxdomain}, {Errors: failing}},
			expected:  addrs("203.0.113.7"), want: DNSNone,
		},
		{
			name:      "a timeout on one, the other answers",
			resolvers: []edgetest.FakeResolver{{Errors: failing}, {Hosts: here}},
			expected:  addrs("203.0.113.7"), want: DNSOK, wantSeen: addrs("203.0.113.7"),
		},
		{
			name:      "no resolver answered",
			resolvers: []edgetest.FakeResolver{{Errors: failing}, {Errors: failing}},
			expected:  addrs("203.0.113.7"), want: DNSUnknown,
		},
		{
			name:      "this host's addresses are not known",
			resolvers: []edgetest.FakeResolver{{Hosts: here}},
			expected:  nil, want: DNSUnknown, wantSeen: addrs("203.0.113.7"),
		},
		{
			name:      "loopback with nothing to compare with is ok (the lab)",
			resolvers: []edgetest.FakeResolver{{Hosts: map[string][]string{host: {"127.0.0.1", "::1"}}}},
			expected:  nil, want: DNSOK, wantSeen: addrs("127.0.0.1", "::1"),
		},
		{
			name:      "loopback beside a public address is unknown",
			resolvers: []edgetest.FakeResolver{{Hosts: map[string][]string{host: {"127.0.0.1", "203.0.113.7"}}}},
			expected:  nil, want: DNSUnknown, wantSeen: addrs("127.0.0.1", "203.0.113.7"),
		},
		{
			name:      "loopback when this host's address is known is elsewhere",
			resolvers: []edgetest.FakeResolver{{Hosts: map[string][]string{host: {"127.0.0.1"}}}},
			expected:  addrs("203.0.113.7"), want: DNSElsewhere, wantSeen: addrs("127.0.0.1"),
		},
		{
			name:      "no resolvers at all",
			resolvers: nil,
			expected:  addrs("203.0.113.7"), want: DNSUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := make(Set, len(tt.resolvers))
			for i, r := range tt.resolvers {
				set[i] = r
			}
			got := checkDNS(context.Background(), set, host, tt.expected)
			if got.Status != tt.want {
				t.Errorf("status = %q, want %q (seen %v)", got.Status, tt.want, got.Seen)
			}
			if !slices.Equal(got.Seen, tt.wantSeen) {
				t.Errorf("seen = %v, want %v", got.Seen, tt.wantSeen)
			}
			if !slices.Equal(got.Expected, tt.expected) {
				t.Errorf("expected = %v, want %v", got.Expected, tt.expected)
			}
		})
	}
}

// A lone Resolver (not a Set) is one answer.
func TestCheckDNSWithOneResolver(t *testing.T) {
	r := edgetest.FakeResolver{Hosts: map[string][]string{"a.example.com": {"203.0.113.7"}}}
	got := checkDNS(context.Background(), r, "a.example.com", addrs("203.0.113.7"))
	if got.Status != DNSOK {
		t.Errorf("status = %q, want ok", got.Status)
	}
	gone := checkDNS(context.Background(), r, "b.example.com", addrs("203.0.113.7"))
	if gone.Status != DNSNone {
		t.Errorf("status = %q, want none", gone.Status)
	}
}

// A localhost name is local by definition (RFC 6761): no resolver is asked, and
// the answer is ok whatever this host's addresses are, the lab's case.
func TestCheckDNSLocalhostNames(t *testing.T) {
	r := edgetest.FakeResolver{Errors: map[string]error{"x.localhost": errors.New("resolver asked")}}
	for _, host := range []string{"x.localhost", "a.b.localhost", "localhost", "X.LOCALHOST", "x.localhost."} {
		got := checkDNS(context.Background(), r, host, addrs("203.0.113.7"))
		if got.Status != DNSOK || len(got.Seen) != 2 || !got.Seen[0].IsLoopback() {
			t.Errorf("%s: %+v, want ok with the loopback addresses", host, got)
		}
	}
	if got := checkDNS(context.Background(), r, "x.localhost.example.com", addrs("203.0.113.7")); got.Status == DNSOK {
		t.Errorf("x.localhost.example.com was taken for a localhost name: %+v", got)
	}
}

func TestSetLookupIPAddr(t *testing.T) {
	boom := errors.New("boom")
	set := Set{
		edgetest.FakeResolver{Errors: map[string]error{"a.example.com": boom}},
		edgetest.FakeResolver{Hosts: map[string][]string{"a.example.com": {"203.0.113.7"}}},
	}
	got, err := set.LookupIPAddr(context.Background(), "a.example.com")
	if err != nil || len(got) != 1 {
		t.Errorf("LookupIPAddr = %v, %v, want the one address", got, err)
	}
	if _, err := (Set{edgetest.FakeResolver{Errors: map[string]error{"a.example.com": boom}}}).LookupIPAddr(context.Background(), "a.example.com"); !errors.Is(err, boom) {
		t.Errorf("error = %v, want boom", err)
	}
}
