package clusterca

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net/netip"
	"strings"
	"testing"
	"time"
)

var (
	t0     = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	meshIP = netip.MustParseAddr("10.98.0.3")
)

func newCA(t *testing.T) (cert, key []byte) {
	t.Helper()
	cert, key, err := New(t0)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func signFor(t *testing.T, caCert, caKey []byte, kind Kind) (cert, key []byte) {
	t.Helper()
	key, csr, err := NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	cert, err = Sign(caCert, caKey, csr, kind, meshIP, t0)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func TestNewCA(t *testing.T) {
	cert, key := newCA(t)
	c, err := ParseCertificate(cert)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject.CommonName != "Caravana cluster CA" || !c.IsCA || !c.BasicConstraintsValid {
		t.Errorf("not the CA the contract describes: %+v", c.Subject)
	}
	if c.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		t.Errorf("key usage = %v", c.KeyUsage)
	}
	if got := c.NotAfter.Sub(t0); got < 3649*24*time.Hour || got > 3651*24*time.Hour {
		t.Errorf("the CA lasts %v, want about 10 years", got)
	}
	if _, ok := c.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Errorf("the CA's key is %T, want ECDSA", c.PublicKey)
	}
	if !bytes.Contains(key, []byte("EC PRIVATE KEY")) {
		t.Error("the key isn't PEM EC PRIVATE KEY")
	}
	if err := CheckCA(cert, t0); err != nil {
		t.Errorf("CheckCA: %v", err)
	}
}

func TestSignServerAndClient(t *testing.T) {
	caCert, caKey := newCA(t)
	ca, _ := ParseCertificate(caCert)
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	for _, kind := range []Kind{Server, Client} {
		certPEM, keyPEM := signFor(t, caCert, caKey, kind)
		cert, err := ParseCertificate(certPEM)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: t0, DNSName: kind.DNSName()}); err != nil {
			t.Errorf("kind %d doesn't verify as %s: %v", kind, kind.DNSName(), err)
		}
		if cert.Subject.CommonName != kind.DNSName() || cert.DNSNames[0] != kind.DNSName() || cert.DNSNames[1] != "localhost" {
			t.Errorf("names = %q %v", cert.Subject.CommonName, cert.DNSNames)
		}
		if len(cert.IPAddresses) != 2 || cert.IPAddresses[0].String() != "127.0.0.1" || cert.IPAddresses[1].String() != "10.98.0.3" {
			t.Errorf("addresses = %v", cert.IPAddresses)
		}
		if cert.NotBefore != t0.Add(-5*time.Minute) || cert.NotAfter != t0.Add(90*24*time.Hour) {
			t.Errorf("lifetime = %v to %v", cert.NotBefore, cert.NotAfter)
		}
		if cert.KeyUsage != x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment {
			t.Errorf("key usage = %v", cert.KeyUsage)
		}
		if len(cert.ExtKeyUsage) != 2 || cert.IsCA {
			t.Errorf("extended usage %v, IsCA %v", cert.ExtKeyUsage, cert.IsCA)
		}
		if err := Check(caCert, certPEM, keyPEM, kind, t0); err != nil {
			t.Errorf("Check kind %d: %v", kind, err)
		}
		if got, err := NotAfter(certPEM); err != nil || !got.Equal(t0.Add(90*24*time.Hour)) {
			t.Errorf("NotAfter = %v, %v", got, err)
		}
	}

	// A client certificate fails the server name.
	clientPEM, _ := signFor(t, caCert, caKey, Client)
	cc, _ := ParseCertificate(clientPEM)
	if _, err := cc.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: t0, DNSName: "server.global.nomad"}); err == nil {
		t.Error("a client certificate verified as a server")
	}
}

func TestSignIgnoresTheRequestsSubject(t *testing.T) {
	caCert, caKey := newCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "server.global.nomad"},
		DNSNames: []string{"evil.example.com"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	certPEM, err := Sign(caCert, caKey, csr, Client, meshIP, t0)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := ParseCertificate(certPEM)
	if cert.Subject.CommonName != "client.global.nomad" || len(cert.DNSNames) != 2 {
		t.Errorf("the request decided the certificate: %q %v", cert.Subject.CommonName, cert.DNSNames)
	}
}

func TestSignEd25519(t *testing.T) {
	caCert, caKey := newCA(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_ = pub
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, priv)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	if _, err := Sign(caCert, caKey, csr, Server, meshIP, t0); err != nil {
		t.Errorf("an Ed25519 request was refused: %v", err)
	}
}

func TestSignRefuses(t *testing.T) {
	caCert, caKey := newCA(t)
	_, goodCSR, _ := NewKeyAndCSR()

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, rsaKey)
	rsaCSR := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: rsaDER})

	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, p384)
	p384CSR := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: p384DER})

	// A request whose signature was damaged.
	block, _ := pem.Decode(goodCSR)
	damaged := append([]byte(nil), block.Bytes...)
	damaged[len(damaged)-3] ^= 0xff
	badSig := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: damaged})

	otherCA, otherKey := newCA(t)
	_ = otherCA

	tests := []struct {
		name         string
		ca, key, csr []byte
		kind         Kind
		ip           netip.Addr
	}{
		{"rsa key", caCert, caKey, rsaCSR, Server, meshIP},
		{"p-384 key", caCert, caKey, p384CSR, Server, meshIP},
		{"bad signature", caCert, caKey, badSig, Server, meshIP},
		{"two blocks", caCert, caKey, append(append([]byte(nil), goodCSR...), goodCSR...), Server, meshIP},
		{"not pem", caCert, caKey, []byte("hello"), Server, meshIP},
		{"a certificate, not a request", caCert, caKey, caCert, Server, meshIP},
		{"another CA's key", caCert, otherKey, goodCSR, Server, meshIP},
		{"no mesh address", caCert, caKey, goodCSR, Server, netip.Addr{}},
		{"unknown kind", caCert, caKey, goodCSR, Kind(9), meshIP},
		{"key as CA", caKey, caKey, goodCSR, Server, meshIP},
	}
	for _, tt := range tests {
		if _, err := Sign(tt.ca, tt.key, tt.csr, tt.kind, tt.ip, t0); err == nil {
			t.Errorf("%s: Sign accepted it", tt.name)
		}
	}
}

func TestCheckRefuses(t *testing.T) {
	caCert, caKey := newCA(t)
	certPEM, keyPEM := signFor(t, caCert, caKey, Server)
	otherCA, otherCAKey := newCA(t)
	otherCert, _ := signFor(t, otherCA, otherCAKey, Server)
	_, anotherKey, _ := NewKeyAndCSR()

	tests := []struct {
		name  string
		ca    []byte
		cert  []byte
		key   []byte
		kind  Kind
		now   time.Time
		fails bool
	}{
		{"good", caCert, certPEM, keyPEM, Server, t0, false},
		{"another CA", otherCA, certPEM, keyPEM, Server, t0, true},
		{"signed by another CA", caCert, otherCert, keyPEM, Server, t0, true},
		{"another key", caCert, certPEM, anotherKey, Server, t0, true},
		{"expired", caCert, certPEM, keyPEM, Server, t0.Add(91 * 24 * time.Hour), true},
		{"not yet valid", caCert, certPEM, keyPEM, Server, t0.Add(-time.Hour), true},
		{"a server's certificate as a client's", caCert, certPEM, keyPEM, Client, t0, true},
	}
	for _, tt := range tests {
		err := Check(tt.ca, tt.cert, tt.key, tt.kind, tt.now)
		if (err != nil) != tt.fails {
			t.Errorf("%s: Check = %v, want failure %v", tt.name, err, tt.fails)
		}
	}
	if err := Check(caCert, certPEM, keyPEM, Kind(0), t0); err == nil {
		t.Error("an unknown kind passed")
	}
}

func TestErrorsNeverHoldAKey(t *testing.T) {
	caCert, caKey := newCA(t)
	_, csr, _ := NewKeyAndCSR()
	_, err := Sign(caCert, []byte("-----BEGIN EC PRIVATE KEY-----\nc2VjcmV0LXNlbnRpbmVs\n-----END EC PRIVATE KEY-----\n"), csr, Server, meshIP, t0)
	if err == nil {
		t.Fatal("accepted a bad key")
	}
	if strings.Contains(err.Error(), "c2VjcmV0") || strings.Contains(err.Error(), string(caKey[:30])) {
		t.Errorf("the error holds key text: %v", err)
	}
}

func TestEqualAndCheckCA(t *testing.T) {
	caCert, caKey := newCA(t)
	if !Equal(caCert, append([]byte("\n"), caCert...)) {
		t.Error("the same certificate with other whitespace isn't equal")
	}
	other, _ := newCA(t)
	if Equal(caCert, other) || Equal(caCert, []byte("x")) {
		t.Error("different certificates are equal")
	}
	leaf, _ := signFor(t, caCert, caKey, Client)
	if err := CheckCA(leaf, t0); err == nil {
		t.Error("a leaf is a CA")
	}
	if err := CheckCA(caCert, t0.Add(11*365*24*time.Hour)); err == nil {
		t.Error("an expired CA passed")
	}
	if err := CheckCA(append(append([]byte(nil), caCert...), caCert...), t0); err == nil {
		t.Error("two certificates passed")
	}
}
