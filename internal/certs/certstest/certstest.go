// Package certstest makes certificate authorities, intermediates and leaves for tests: ECDSA P-256
// by default, RSA on request. Other packages' tests reuse it (the service's upload tests, the
// lab's), so a chain is a few lines.
package certstest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// Format is how a private key is written in PEM.
type Format int

const (
	// PKCS8 is "PRIVATE KEY".
	PKCS8 Format = iota
	// PKCS1 is "RSA PRIVATE KEY" (RSA keys only).
	PKCS1
	// SEC1 is "EC PRIVATE KEY" (ECDSA keys only).
	SEC1
)

type config struct {
	rsaBits      int
	curve        elliptic.Curve
	organization string
	commonName   string
	hasCN        bool
	ekus         []x509.ExtKeyUsage
	hasEKUs      bool
}

// Option changes how a certificate and its key are made.
type Option func(*config)

// RSA makes a 2048-bit RSA key.
func RSA() Option { return func(c *config) { c.rsaBits = 2048 } }

// RSABits makes an RSA key of that size (1024 for a key that is too small).
func RSABits(bits int) Option { return func(c *config) { c.rsaBits = bits } }

// Curve makes an ECDSA key on curve (P-256 by default).
func Curve(curve elliptic.Curve) Option { return func(c *config) { c.curve = curve } }

// Organization sets the subject's organization.
func Organization(o string) Option { return func(c *config) { c.organization = o } }

// CommonName sets a leaf's common name (its first name by default; "" for none).
func CommonName(cn string) Option { return func(c *config) { c.commonName, c.hasCN = cn, true } }

// ExtKeyUsages sets a leaf's extended key usages (server authentication by default; none for no extension).
func ExtKeyUsages(u ...x509.ExtKeyUsage) Option {
	return func(c *config) { c.ekus, c.hasEKUs = u, true }
}

func apply(opts []Option) config {
	c := config{curve: elliptic.P256()}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func (c config) key(t testing.TB) crypto.Signer {
	t.Helper()
	if c.rsaBits > 0 {
		k, err := rsa.GenerateKey(rand.Reader, c.rsaBits)
		if err != nil {
			t.Fatalf("certstest: RSA key: %v", err)
		}
		return k
	}
	k, err := ecdsa.GenerateKey(c.curve, rand.Reader)
	if err != nil {
		t.Fatalf("certstest: ECDSA key: %v", err)
	}
	return k
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		t.Fatalf("certstest: serial: %v", err)
	}
	return n
}

// Authority is a certificate authority: a root, or an intermediate.
type Authority struct {
	Cert *x509.Certificate
	Key  crypto.Signer
}

// The CA's validity covers any date a test uses.
var (
	caNotBefore = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	caNotAfter  = time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)
)

// NewCA makes a self-signed root named cn.
func NewCA(t testing.TB, cn string, opts ...Option) *Authority {
	t.Helper()
	return newAuthority(t, nil, cn, apply(opts))
}

// NewIntermediate makes an intermediate signed by parent.
func NewIntermediate(t testing.TB, parent *Authority, cn string, opts ...Option) *Authority {
	t.Helper()
	return newAuthority(t, parent, cn, apply(opts))
}

func newAuthority(t testing.TB, parent *Authority, cn string, c config) *Authority {
	t.Helper()
	key := c.key(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             caNotBefore,
		NotAfter:              caNotAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	if c.organization != "" {
		tmpl.Subject.Organization = []string{c.organization}
	}
	signer, signerCert := key, tmpl
	if parent != nil {
		signer, signerCert = parent.Key, parent.Cert
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, key.Public(), signer)
	if err != nil {
		t.Fatalf("certstest: CA %q: %v", cn, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("certstest: CA %q: %v", cn, err)
	}
	return &Authority{Cert: cert, Key: key}
}

// PEM is the authority's certificate.
func (a *Authority) PEM() []byte { return certPEM(a.Cert) }

// Pool is a certificate pool that trusts these authorities.
func Pool(auths ...*Authority) *x509.CertPool {
	p := x509.NewCertPool()
	for _, a := range auths {
		p.AddCert(a.Cert)
	}
	return p
}

// Leaf is a server certificate and its key.
type Leaf struct {
	Cert *x509.Certificate
	Key  crypto.Signer
}

// NewLeaf makes a certificate for names (DNS names), signed by ca, valid from notBefore to notAfter.
func NewLeaf(t testing.TB, ca *Authority, names []string, notBefore, notAfter time.Time, opts ...Option) *Leaf {
	t.Helper()
	return newLeaf(t, ca, names, notBefore, notAfter, apply(opts))
}

// NewSelfSigned makes a leaf that signed itself.
func NewSelfSigned(t testing.TB, names []string, notBefore, notAfter time.Time, opts ...Option) *Leaf {
	t.Helper()
	return newLeaf(t, nil, names, notBefore, notAfter, apply(opts))
}

func newLeaf(t testing.TB, ca *Authority, names []string, notBefore, notAfter time.Time, c config) *Leaf {
	t.Helper()
	key := c.key(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		DNSNames:              names,
	}
	switch {
	case c.hasCN:
		tmpl.Subject.CommonName = c.commonName
	case len(names) > 0:
		tmpl.Subject.CommonName = names[0]
	}
	if c.organization != "" {
		tmpl.Subject.Organization = []string{c.organization}
	}
	if c.hasEKUs {
		tmpl.ExtKeyUsage = c.ekus
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	signer, signerCert := key, tmpl
	if ca != nil {
		signer, signerCert = ca.Key, ca.Cert
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, key.Public(), signer)
	if err != nil {
		t.Fatalf("certstest: leaf %v: %v", names, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("certstest: leaf %v: %v", names, err)
	}
	return &Leaf{Cert: cert, Key: key}
}

// PEM is the leaf's certificate alone.
func (l *Leaf) PEM() []byte { return certPEM(l.Cert) }

// KeyPEM is the leaf's private key in the format asked for. It fails the test when the format
// doesn't fit the key's type.
func (l *Leaf) KeyPEM(t testing.TB, f Format) []byte {
	t.Helper()
	switch f {
	case PKCS8:
		der, err := x509.MarshalPKCS8PrivateKey(l.Key)
		if err != nil {
			t.Fatalf("certstest: PKCS#8: %v", err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	case PKCS1:
		k, ok := l.Key.(*rsa.PrivateKey)
		if !ok {
			t.Fatalf("certstest: PKCS#1 is for RSA keys, this one is %T", l.Key)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	case SEC1:
		k, ok := l.Key.(*ecdsa.PrivateKey)
		if !ok {
			t.Fatalf("certstest: SEC 1 is for ECDSA keys, this one is %T", l.Key)
		}
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			t.Fatalf("certstest: SEC 1: %v", err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	}
	t.Fatalf("certstest: unknown key format %d", f)
	return nil
}

// Chain joins certificates' PEM in the order given.
func Chain(certs ...*x509.Certificate) []byte {
	var out []byte
	for _, c := range certs {
		out = append(out, certPEM(c)...)
	}
	return out
}

// EncryptedKeyPEM is a block that looks like a password-protected key ("ENCRYPTED PRIVATE KEY").
// Its bytes are not a real key; it is for the refusal.
func EncryptedKeyPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("not a key, only a placeholder")})
}

// LegacyEncryptedKeyPEM is a traditional key block with Proc-Type 4,ENCRYPTED. Its bytes are not a real key.
func LegacyEncryptedKeyPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00112233445566778899AABBCCDDEEFF"},
		Bytes:   []byte("not a key, only a placeholder"),
	})
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}
