// Package edgetest has fakes for the edge package's seams, and the certificates
// to put in them. It imports only the standard library, so edge's own tests can
// use it.
package edgetest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// FakeResolver answers from maps. A host in Errors fails with that error, one in
// Hosts answers with those addresses, and any other host is not found
// (NXDOMAIN), as is one in NotFound.
type FakeResolver struct {
	Hosts    map[string][]string // host -> addresses, v4 or v6
	NotFound map[string]bool
	Errors   map[string]error
}

// LookupIPAddr implements edge.Resolver.
func (f FakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if err := f.Errors[host]; err != nil {
		return nil, err
	}
	addrs, ok := f.Hosts[host]
	if !ok || f.NotFound[host] {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	out := make([]net.IPAddr, len(addrs))
	for i, a := range addrs {
		out[i] = net.IPAddr{IP: net.ParseIP(a)}
	}
	return out, nil
}

// FakeDialer presents chains from a map. A host in Errors fails with that
// error; a host that is in neither map gets Traefik's default certificate if
// Default is set, else a refused connection.
type FakeDialer struct {
	Chains  map[string][]*x509.Certificate // host -> the certificates presented, leaf first
	Errors  map[string]error
	Default []*x509.Certificate // presented for any other host
	RootCAs *x509.CertPool      // what edge.Check trusts; nil means the system's

	mu     sync.Mutex
	dialed []string
}

// DialTLS implements edge.Dialer.
func (f *FakeDialer) DialTLS(_ context.Context, addr, serverName string) (*tls.ConnectionState, error) {
	f.mu.Lock()
	f.dialed = append(f.dialed, addr+" "+serverName)
	f.mu.Unlock()
	if err := f.Errors[serverName]; err != nil {
		return nil, err
	}
	if chain, ok := f.Chains[serverName]; ok {
		return &tls.ConnectionState{PeerCertificates: chain}, nil
	}
	if f.Default != nil {
		return &tls.ConnectionState{PeerCertificates: f.Default}, nil
	}
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errConnRefused}
}

var errConnRefused = errors.New("connection refused")

// Roots implements edge.RootsProvider.
func (f *FakeDialer) Roots() *x509.CertPool { return f.RootCAs }

// Dialed lists "<addr> <serverName>" for every dial so far.
func (f *FakeDialer) Dialed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dialed...)
}

// CA is a certificate authority made for a test.
type CA struct {
	Cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// NewCA makes a CA whose certificates name org as their issuer.
func NewCA(t testing.TB, org string) *CA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{Organization: []string{org}, CommonName: org + " Root"},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	return &CA{Cert: sign(t, tmpl, tmpl, &key.PublicKey, key), key: key}
}

// Pool is a pool trusting only this CA.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.Cert)
	return p
}

// Issue makes a leaf for the names, valid from notBefore to notAfter.
func (c *CA) Issue(t testing.TB, names []string, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key := newKey(t)
	cn := ""
	if len(names) > 0 {
		cn = names[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	return sign(t, tmpl, c.Cert, &key.PublicKey, c.key)
}

// TraefikDefault is the certificate Traefik presents for a name it has no
// certificate for: self-signed, common name TRAEFIK DEFAULT CERT, no names.
func TraefikDefault(t testing.TB) *x509.Certificate {
	t.Helper()
	return selfSigned(t, "TRAEFIK DEFAULT CERT", nil)
}

// SelfSigned is a self-signed leaf with the common name cn and the names.
func SelfSigned(t testing.TB, cn string, names []string) *x509.Certificate {
	t.Helper()
	return selfSigned(t, cn, names)
}

func selfSigned(t testing.TB, cn string, names []string) *x509.Certificate {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	return sign(t, tmpl, tmpl, &key.PublicKey, key)
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func sign(t testing.TB, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
