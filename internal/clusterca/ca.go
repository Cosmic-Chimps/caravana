// Package clusterca is the cluster's certificate authority: Caravana holds the CA and signs the
// certificates Nomad's servers and clients use for RPC (feature 031, research D3). It is pure: it
// makes and reads PEM text and does no file or network I/O. A server makes its own key and sends a
// certificate request, so a private key never leaves its server.
package clusterca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"time"
)

// Kind is what a certificate is for. Nomad's verify_server_hostname makes a client certificate
// unable to act as a server, which is why the two have different names.
type Kind int

const (
	// Server is a certificate for a Nomad server.
	Server Kind = iota + 1
	// Client is a certificate for a Nomad client.
	Client
)

// DNSName is the name Nomad looks for in a certificate of this kind.
func (k Kind) DNSName() string {
	if k == Server {
		return "server.global.nomad"
	}
	return "client.global.nomad"
}

const (
	caLifetime   = 10 * 365 * 24 * time.Hour
	leafLifetime = 90 * 24 * time.Hour
	// skew is how far back a certificate starts, so a clock a little behind the CA's still accepts it.
	skew = 5 * time.Minute
)

// New makes a CA: an ECDSA P-256 key and a self-signed certificate called "Caravana cluster CA",
// valid for 10 years. The key is PEM, "EC PRIVATE KEY".
func New(now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("make the CA's key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Caravana cluster CA"},
		NotBefore:             now.Add(-skew),
		NotAfter:              now.Add(caLifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign the CA certificate: %w", err)
	}
	keyPEM, err = encodeECKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

// NewKeyAndCSR makes a server's own key (ECDSA P-256, PEM "EC PRIVATE KEY") and a certificate
// request for it. The request's subject and extensions mean nothing to Sign; it only needs the key.
func NewKeyAndCSR() (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("make a key: %w", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "caravana node"},
	}, key)
	if err != nil {
		return nil, nil, fmt.Errorf("make a certificate request: %w", err)
	}
	keyPEM, err = encodeECKey(key)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// Sign makes a certificate of this kind for the key in a certificate request. It lasts 90 days and
// starts 5 minutes back. Its names are the kind's DNS name and localhost; its addresses are
// 127.0.0.1 and the server's mesh address. The request's signature is checked and its subject and
// extensions are ignored: nothing in it decides what the certificate says. Only ECDSA P-256 and
// Ed25519 keys are signed. No error holds a key.
func Sign(caCert, caKey, csrPEM []byte, kind Kind, meshIP netip.Addr, now time.Time) ([]byte, error) {
	if kind != Server && kind != Client {
		return nil, errors.New("sign: unknown kind of certificate")
	}
	if !meshIP.IsValid() {
		return nil, errors.New("sign: no mesh address")
	}
	ca, err := ParseCertificate(caCert)
	if err != nil {
		return nil, fmt.Errorf("sign: the CA certificate: %w", err)
	}
	if !ca.IsCA {
		return nil, errors.New("sign: the CA certificate isn't a CA")
	}
	signer, err := parseKey(caKey)
	if err != nil {
		return nil, fmt.Errorf("sign: the CA's key: %w", err)
	}
	if !samePublic(signer.Public(), ca.PublicKey) {
		return nil, errors.New("sign: the CA's key isn't the CA certificate's")
	}
	pub, err := csrPublicKey(csrPEM)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	name := kind.DNSName()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name, "localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IP(meshIP.Unmap().AsSlice())},
		NotBefore:             now.Add(-skew),
		NotAfter:              now.Add(leafLifetime),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, signer)
	if err != nil {
		return nil, fmt.Errorf("sign the certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// Check says certPEM is a certificate of this kind that this CA signed, that keyPEM is its key, that
// it is valid at now and that it has the kind's DNS name.
func Check(caCert, certPEM, keyPEM []byte, kind Kind, now time.Time) error {
	if kind != Server && kind != Client {
		return errors.New("unknown kind of certificate")
	}
	ca, err := ParseCertificate(caCert)
	if err != nil {
		return fmt.Errorf("the CA certificate: %w", err)
	}
	cert, err := ParseCertificate(certPEM)
	if err != nil {
		return fmt.Errorf("the certificate: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: now,
		DNSName:     kind.DNSName(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("the certificate isn't valid for a %s of this cluster: %w", kindWord(kind), err)
	}
	// X509KeyPair compares the certificate's public key with the private one's.
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return errors.New("the certificate isn't for this key")
	}
	return nil
}

func kindWord(k Kind) string {
	if k == Server {
		return "server"
	}
	return "client"
}

// NotAfter is when the certificate ends.
func NotAfter(certPEM []byte) (time.Time, error) {
	c, err := ParseCertificate(certPEM)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}

// ParseCertificate reads exactly one PEM certificate and nothing else.
func ParseCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	if len(trimSpace(rest)) != 0 {
		return nil, errors.New("more than one PEM block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("not a valid certificate")
	}
	return c, nil
}

// CheckCA says certPEM is one certificate that is a CA and is valid at now.
func CheckCA(certPEM []byte, now time.Time) error {
	c, err := ParseCertificate(certPEM)
	if err != nil {
		return err
	}
	if !c.IsCA || !c.BasicConstraintsValid {
		return errors.New("the certificate isn't a CA")
	}
	if now.Before(c.NotBefore) || now.After(c.NotAfter) {
		return errors.New("the CA certificate isn't valid now")
	}
	return nil
}

// Equal says two PEM certificates are the same certificate, whatever their whitespace.
func Equal(a, b []byte) bool {
	ca, err := ParseCertificate(a)
	if err != nil {
		return false
	}
	cb, err := ParseCertificate(b)
	if err != nil {
		return false
	}
	return ca.Equal(cb)
}

func csrPublicKey(csrPEM []byte) (crypto.PublicKey, error) {
	block, rest := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("the certificate request isn't PEM")
	}
	if len(trimSpace(rest)) != 0 {
		return nil, errors.New("the certificate request has more than one PEM block")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, errors.New("the certificate request isn't valid")
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, errors.New("the certificate request's signature is wrong")
	}
	switch k := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return nil, errors.New("the certificate request's key must be ECDSA P-256 or Ed25519")
		}
	case ed25519.PublicKey:
	default:
		return nil, errors.New("the certificate request's key must be ECDSA P-256 or Ed25519")
	}
	return csr.PublicKey, nil
}

// parseKey reads a PEM private key (SEC 1 "EC PRIVATE KEY" or PKCS 8 "PRIVATE KEY"). Errors say what
// was wrong with the PEM, never what it held.
func parseKey(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("not a valid EC key")
		}
		return k, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("not a valid PKCS 8 key")
		}
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
	}
	return nil, errors.New("not a signing key")
}

func samePublic(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	e, ok := a.(equaler)
	return ok && e.Equal(b)
}

func encodeECKey(k *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, fmt.Errorf("encode a key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func newSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("make a serial number: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == '\n' || b[0] == '\r' || b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	return b
}
