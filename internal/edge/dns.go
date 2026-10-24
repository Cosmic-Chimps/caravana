package edge

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
)

// DNS statuses.
const (
	DNSOK        = "ok"        // every resolver that answered points at this host
	DNSElsewhere = "elsewhere" // a resolver answered, and not only with this host's addresses
	DNSNone      = "none"      // every resolver that answered says the name has no record
	DNSUnknown   = "unknown"   // no resolver answered, or this host's addresses aren't known
)

// DNS is what the resolvers say about a host.
type DNS struct {
	Status string
	// Seen are the addresses the resolvers answered, sorted, without repeats.
	Seen []netip.Addr
	// Expected are this host's own addresses, as the caller gave them.
	Expected []netip.Addr
}

// Resolver looks a host up. *net.Resolver satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Answer is one resolver's reply to a lookup.
type Answer struct {
	Addrs []net.IPAddr
	Err   error
}

// Set is several resolvers asked at the same time. It is a Resolver itself (the
// union of what they found), but Check asks it through LookupEach, so that it
// can tell what each one said.
type Set []Resolver

// LookupEach asks every resolver in parallel and returns their answers in
// order.
func (s Set) LookupEach(ctx context.Context, host string) []Answer {
	answers := make([]Answer, len(s))
	var wg sync.WaitGroup
	for i, r := range s {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i].Addrs, answers[i].Err = r.LookupIPAddr(ctx, host)
		}()
	}
	wg.Wait()
	return answers
}

// LookupIPAddr returns the addresses any resolver found, or the first error
// when none found any.
func (s Set) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	var all []net.IPAddr
	var firstErr error
	for _, a := range s.LookupEach(ctx, host) {
		all = append(all, a.Addrs...)
		if a.Err != nil && firstErr == nil {
			firstErr = a.Err
		}
	}
	if len(all) == 0 {
		return nil, firstErr
	}
	return all, nil
}

// lookupEach asks r, one answer per resolver when r is a Set.
func lookupEach(ctx context.Context, r Resolver, host string) []Answer {
	if m, ok := r.(interface {
		LookupEach(ctx context.Context, host string) []Answer
	}); ok {
		return m.LookupEach(ctx, host)
	}
	addrs, err := r.LookupIPAddr(ctx, host)
	return []Answer{{Addrs: addrs, Err: err}}
}

// checkDNS compares what the resolvers say about host with expected, this
// host's own addresses.
//
// A resolver counts as having answered when it returned addresses, or said the
// name doesn't exist (NXDOMAIN or no data); a timeout or any other error is
// no answer. Then:
//
//   - none of them answered: unknown;
//   - all that answered say "no such name": none;
//   - expected is empty (this host's addresses aren't known): ok when every
//     address seen is loopback (a *.localhost name, the lab's case), else
//     unknown, because there is nothing to compare with;
//   - otherwise ok only when every resolver that returned addresses returned
//     nothing but expected ones, and elsewhere when any returned an address
//     that isn't expected. That includes a stale AAAA beside a right A: Let's
//     Encrypt tries IPv6 first, so the old address is where visitors go. It
//     also means expected must list all of this host's addresses.
//
// A resolver that says "no such name" while another has addresses is read as
// the first one lagging behind a change, and isn't counted either way.
func checkDNS(ctx context.Context, r Resolver, host string, expected []netip.Addr) DNS {
	return classifyDNS(lookupEach(ctx, r, host), expected)
}

func classifyDNS(answers []Answer, expected []netip.Addr) DNS {
	res := DNS{Status: DNSUnknown, Expected: expected}

	var positive [][]netip.Addr // one list per resolver that returned addresses
	negative := 0
	for _, a := range answers {
		var dnsErr *net.DNSError
		switch {
		case a.Err == nil && len(a.Addrs) > 0:
			positive = append(positive, normalize(a.Addrs))
		case a.Err == nil, errors.As(a.Err, &dnsErr) && dnsErr.IsNotFound:
			negative++
		}
		// Any other error: the resolver didn't answer.
	}
	switch {
	case len(positive) == 0 && negative == 0:
		return res
	case len(positive) == 0:
		res.Status = DNSNone
		return res
	}

	for _, addrs := range positive {
		res.Seen = append(res.Seen, addrs...)
	}
	slices.SortFunc(res.Seen, netip.Addr.Compare)
	res.Seen = slices.Compact(res.Seen)

	if len(expected) == 0 {
		if slices.ContainsFunc(res.Seen, func(a netip.Addr) bool { return !a.IsLoopback() }) {
			return res // unknown: nothing to compare with
		}
		res.Status = DNSOK
		return res
	}
	want := make([]netip.Addr, len(expected))
	for i, a := range expected {
		want[i] = a.Unmap()
	}
	res.Status = DNSOK
	for _, addrs := range positive {
		for _, a := range addrs {
			if !slices.Contains(want, a) {
				res.Status = DNSElsewhere
				return res
			}
		}
	}
	return res
}

// normalize turns resolver addresses into netip.Addr, with IPv4-in-IPv6 forms
// as plain IPv4 and no zone.
func normalize(in []net.IPAddr) []netip.Addr {
	out := make([]netip.Addr, 0, len(in))
	for _, a := range in {
		if addr, ok := netip.AddrFromSlice(a.IP); ok {
			out = append(out, addr.Unmap())
		}
	}
	return out
}
