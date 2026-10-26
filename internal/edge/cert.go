package edge

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"
)

// Certificate statuses.
const (
	CertIssued   = "issued"   // a trusted certificate for the host
	CertPending  = "pending"  // Traefik's own certificate while a resolver is set: ACME isn't done
	CertSelf     = "self"     // Traefik's own certificate and no resolver (the lab, or --acme none)
	CertExpiring = "expiring" // issued, and within the warning window (or past it)
	CertError    = "error"    // the handshake failed, or the chain isn't one to trust
	CertNone     = "none"     // not probed: the app isn't served
)

// traefikDefaultCN is the common name of the certificate Traefik answers with
// for a name it has no certificate for.
const traefikDefaultCN = "TRAEFIK DEFAULT CERT"

// TraefikOwn is the Issuer of Traefik's own certificate.
const TraefikOwn = "Traefik's own"

// renewBefore is how long before NotAfter Traefik renews a certificate: its
// default for a 90-day Let's Encrypt one.
const renewBefore = 30 * 24 * time.Hour

// Certificate is what the TLS handshake for a host showed.
type Certificate struct {
	Status string
	// Issuer is the issuer's organization, or its common name; TraefikOwn for
	// Traefik's default certificate. Empty when nothing was presented.
	Issuer string
	// NotAfter is the leaf's expiry, zero for Traefik's own certificate.
	NotAfter time.Time
	// RenewsAt is when Traefik should renew: NotAfter minus 30 days. Zero unless
	// the certificate is issued or expiring.
	RenewsAt time.Time
}

// Valid says Traefik presented a trusted certificate for the host (issued, or
// expiring but still the issuer's): proof that the issuer reached this host.
func (c Certificate) Valid() bool {
	return c.Status == CertIssued || c.Status == CertExpiring
}

// Dialer makes a TLS handshake with serverName at addr and returns what the
// peer presented. It must not verify the certificate: Check does, by hand, so
// that "untrusted" and "wrong name" are separate facts.
type Dialer interface {
	DialTLS(ctx context.Context, addr, serverName string) (*tls.ConnectionState, error)
}

// RootsProvider is implemented by a Dialer that wants Check to trust other
// roots than the system's (a test's own CA, a host with a private one).
type RootsProvider interface {
	Roots() *x509.CertPool
}

// checkCertificate dials the edge for host and classifies what it presents.
// resolverSet says whether Traefik has a certificate resolver; warn is the
// window before NotAfter in which a certificate counts as expiring.
//
//   - dial error, or no certificate presented: error;
//   - Traefik's default certificate (its common name, or a self-signed one whose
//     names don't include host): pending with a resolver, else self;
//   - names include host and the chain verifies against the roots (the other
//     presented certificates as intermediates): issued, or expiring when
//     NotAfter - now <= warn, which includes one that already expired;
//   - anything else (a name that doesn't match, a chain that doesn't verify,
//     like a staging issuer's): error, with the issuer kept.
func checkCertificate(ctx context.Context, d Dialer, edge, host string, resolverSet bool, warn time.Duration, now time.Time) Certificate {
	state, err := d.DialTLS(ctx, edge, host)
	if err != nil {
		return Certificate{Status: CertError}
	}
	return classifyCertificate(state.PeerCertificates, rootsOf(d), host, resolverSet, warn, now)
}

func rootsOf(d Dialer) *x509.CertPool {
	if p, ok := d.(RootsProvider); ok {
		return p.Roots()
	}
	return nil // the system's
}

func classifyCertificate(chain []*x509.Certificate, roots *x509.CertPool, host string, resolverSet bool, warn time.Duration, now time.Time) Certificate {
	if len(chain) == 0 {
		return Certificate{Status: CertError}
	}
	leaf := chain[0]
	nameErr := leaf.VerifyHostname(host)

	if leaf.Subject.CommonName == traefikDefaultCN ||
		(bytes.Equal(leaf.RawIssuer, leaf.RawSubject) && nameErr != nil) {
		status := CertSelf
		if resolverSet {
			status = CertPending
		}
		return Certificate{Status: status, Issuer: TraefikOwn}
	}

	cert := Certificate{Issuer: issuerName(leaf), NotAfter: leaf.NotAfter.UTC()}
	if nameErr != nil {
		cert.Status = CertError
		return cert
	}

	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		intermediates.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	var invalid x509.CertificateInvalidError
	switch {
	case err == nil:
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired && now.After(leaf.NotAfter):
		// Expired is the far end of expiring: the renewal never came.
		cert.Status = CertExpiring
		cert.RenewsAt = cert.NotAfter.Add(-renewBefore)
		return cert
	default:
		cert.Status = CertError
		return cert
	}

	cert.Status = CertIssued
	if leaf.NotAfter.Sub(now) <= warn {
		cert.Status = CertExpiring
	}
	cert.RenewsAt = cert.NotAfter.Add(-renewBefore)
	return cert
}

// issuerName names who issued the leaf: its organization when it has one
// ("Let's Encrypt", "(STAGING) Let's Encrypt"), else its common name.
func issuerName(leaf *x509.Certificate) string {
	if len(leaf.Issuer.Organization) > 0 {
		return leaf.Issuer.Organization[0]
	}
	return leaf.Issuer.CommonName
}
