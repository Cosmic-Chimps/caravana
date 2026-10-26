// Package edge answers "is this domain really working?" from two facts Caravana
// can measure itself: what DNS says about the host, and what certificate
// Traefik's edge presents for it. It is pure: the lookups and the TLS dial go
// through the Resolver and Dialer seams (edgetest has fakes), and it imports
// nothing of Nomad or the service layer.
package edge

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

// checkTimeout bounds each of the two checks. The real resolvers and the
// dialer have shorter timeouts of their own; this only stops a fake or a
// custom seam from hanging a check.
const checkTimeout = 6 * time.Second

// Facts are the measurements for one host at one moment.
type Facts struct {
	DNS         DNS
	Certificate Certificate
	CheckedAt   time.Time
}

// Check measures host: its DNS against expected (this host's own addresses) and
// the certificate Traefik presents at edge (host:port) for it, in parallel.
// resolverSet says whether Traefik has a certificate resolver, warn is the
// expiry window, and now stamps CheckedAt and decides what is expired. A
// failed lookup or dial is a fact (unknown, error), not an error return.
func Check(ctx context.Context, r Resolver, d Dialer, edge, host string, expected []netip.Addr, resolverSet bool, warn time.Duration, now time.Time) Facts {
	facts := Facts{CheckedAt: now}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		facts.DNS = checkDNS(ctx, r, host, expected)
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		facts.Certificate = checkCertificate(ctx, d, edge, host, resolverSet, warn, now)
	}()
	wg.Wait()
	return facts
}
