package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"time"
)

// lookupTimeout is how long each resolver of the real set has.
const lookupTimeout = 2 * time.Second

// publicResolvers are asked directly, so that a stale answer in the host's own
// cache doesn't decide.
var publicResolvers = []string{"1.1.1.1:53", "8.8.8.8:53"}

// timeLimited gives each lookup of r its own timeout.
type timeLimited struct {
	r Resolver
	d time.Duration
}

func (t timeLimited) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	ctx, cancel := context.WithTimeout(ctx, t.d)
	defer cancel()
	return t.r.LookupIPAddr(ctx, host)
}

// SystemAndPublicResolvers is the host's system resolver plus Cloudflare's
// and Google's public ones, each with a 2 second timeout. Check asks them in
// parallel and compares what each one said.
func SystemAndPublicResolvers() Set {
	set := Set{timeLimited{net.DefaultResolver, lookupTimeout}}
	for _, addr := range publicResolvers {
		set = append(set, timeLimited{&net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: lookupTimeout}
				return d.DialContext(ctx, network, addr)
			},
		}, lookupTimeout})
	}
	return set
}

// TLSDialer is the real Dialer: a handshake with SNI and no verification
// (Check verifies by hand). Nothing is sent after the handshake.
type TLSDialer struct {
	// Timeout is the limit of the connection and the handshake; zero means 3 s.
	Timeout time.Duration
	// RootCAs are the roots Check trusts; nil means the system's.
	RootCAs *x509.CertPool
}

// Roots makes TLSDialer a RootsProvider.
func (d TLSDialer) Roots() *x509.CertPool { return d.RootCAs }

// DialTLS connects to addr and handshakes with serverName.
func (d TLSDialer) DialTLS(ctx context.Context, addr, serverName string) (*tls.ConnectionState, error) {
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		// Check verifies the chain and the name itself, so a wrong or untrusted
		// certificate is a fact to report and not a failed handshake.
		Config: &tls.Config{ServerName: serverName, InsecureSkipVerify: true},
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tls dial %s: %w", addr, err)
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	return &state, nil
}
