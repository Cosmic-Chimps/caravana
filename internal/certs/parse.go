package certs

import (
	"bytes"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	// maxInput is the most each of the two inputs of Parse may hold.
	maxInput = 64 << 10
	// maxStored is the most the normalized chain and key may hold together: they go to one Nomad
	// Variable, which is limited to 64 KiB, with room for its other items.
	maxStored = 48 << 10
	// minRSABits is the smallest RSA key Parse accepts.
	minRSABits = 2048
)

// Options are what Parse needs from outside.
type Options struct {
	// Roots are the roots a chain is trusted by. Nil means the system's.
	Roots *x509.CertPool
	// Now is the moment the dates are judged at. Zero means time.Now().
	Now time.Time
}

// Parsed is a checked certificate and what is public about it.
type Parsed struct {
	// Names are the leaf's DNS names: lower case, sorted, without duplicates.
	Names []string
	// Subject is the leaf's common name, else its first name.
	Subject string
	// Issuer is the issuer's organization, else its common name.
	Issuer string
	// NotBefore and NotAfter are the leaf's dates, in UTC.
	NotBefore, NotAfter time.Time
	// Fingerprint is the SHA-256 of the leaf's DER, lower-case hex.
	Fingerprint string
	// KeyType is "RSA 2048" or "ECDSA P-256".
	KeyType string
	// Trusted says the chain ends at a root in Options.Roots (the system's when nil).
	Trusted bool
	// ChainPEM is the leaf, then its intermediates in chain order. A self-signed root is left out
	// unless it is the leaf itself.
	ChainPEM []byte
	// KeyPEM is the private key's own PEM block, re-encoded.
	KeyPEM []byte
}

// Parse checks an uploaded chain and key and returns what Traefik needs and what the console
// shows. Both inputs may hold certificates (in any order) and the key may be in either of them.
// The hostname is not checked: which hosts the certificate serves is its names.
//
// Every refusal is an *Error naming "certificate" or "key"; none holds key bytes.
func Parse(certificatePEM, keyPEM []byte, opts Options) (Parsed, error) {
	if len(certificatePEM) > maxInput {
		return Parsed{}, fieldError("certificate", "is larger than 64 KiB")
	}
	if len(keyPEM) > maxInput {
		return Parsed{}, fieldError("key", "is larger than 64 KiB")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	var certs []*x509.Certificate
	var keyBlocks []*pem.Block
	for _, in := range []struct {
		field string
		data  []byte
	}{{"certificate", certificatePEM}, {"key", keyPEM}} {
		c, k, err := readBlocks(in.field, in.data)
		if err != nil {
			return Parsed{}, err
		}
		certs = append(certs, c...)
		keyBlocks = append(keyBlocks, k...)
	}
	if len(certs) == 0 {
		return Parsed{}, fieldError("certificate", "no certificate found")
	}
	if len(keyBlocks) == 0 {
		return Parsed{}, fieldError("key", "no private key found")
	}
	if len(keyBlocks) > 1 {
		return Parsed{}, fieldError("key", "more than one private key found: give the key of this certificate only")
	}
	key, keyType, err := parseKey(keyBlocks[0])
	if err != nil {
		return Parsed{}, err
	}

	certs = dedupe(certs)
	leafAt := slices.IndexFunc(certs, func(c *x509.Certificate) bool { return samePublicKey(c.PublicKey, key.Public()) })
	if leafAt < 0 {
		return Parsed{}, fieldError("key", "this key doesn't match any certificate given")
	}
	leaf := certs[leafAt]
	pool := slices.Delete(slices.Clone(certs), leafAt, leafAt+1)

	if err := checkLeaf(leaf, now); err != nil {
		return Parsed{}, err
	}
	chain, trusted, err := buildChain(leaf, pool, opts.Roots, now)
	if err != nil {
		return Parsed{}, err
	}

	p := Parsed{
		Names:       leafNames(leaf),
		Subject:     subjectName(leaf),
		Issuer:      IssuerName(leaf),
		NotBefore:   leaf.NotBefore.UTC(),
		NotAfter:    leaf.NotAfter.UTC(),
		Fingerprint: Fingerprint(leaf),
		KeyType:     keyType,
		Trusted:     trusted,
		KeyPEM:      pem.EncodeToMemory(&pem.Block{Type: keyBlocks[0].Type, Bytes: keyBlocks[0].Bytes}),
	}
	stored := chain
	if len(stored) > 1 && selfSigned(stored[len(stored)-1]) {
		stored = stored[:len(stored)-1]
	}
	for _, c := range stored {
		p.ChainPEM = append(p.ChainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	if len(p.ChainPEM)+len(p.KeyPEM) > maxStored {
		return Parsed{}, fieldError("certificate", "the chain and key together are larger than 48 KiB")
	}
	return p, nil
}

// Fingerprint is the SHA-256 of a certificate's DER, lower-case hex: how Caravana recognises an
// uploaded certificate in a handshake.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// IssuerName names who issued a certificate: the issuer's organization when it has one, else its
// common name.
func IssuerName(c *x509.Certificate) string {
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return c.Issuer.CommonName
}

// readBlocks reads every PEM block of one input. Certificates are parsed; private keys are
// returned as blocks. An encrypted key, any other kind of block and bytes that aren't PEM are
// refused, naming field.
func readBlocks(field string, data []byte) (certs []*x509.Certificate, keys []*pem.Block, err error) {
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			if len(bytes.TrimSpace(rest)) > 0 {
				return nil, nil, fieldError(field, "contains data that isn't PEM (expected blocks that start with -----BEGIN)")
			}
			return certs, keys, nil
		}
		if strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED") || block.Type == "ENCRYPTED PRIVATE KEY" {
			return nil, nil, fieldError("key", "the key is encrypted: decrypt it first (openssl pkey -in key.pem -out plain.pem)")
		}
		switch block.Type {
		case "CERTIFICATE":
			c, perr := x509.ParseCertificate(block.Bytes)
			if perr != nil {
				return nil, nil, fieldError(field, "contains a certificate that can't be read")
			}
			certs = append(certs, c)
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			keys = append(keys, block)
		default:
			return nil, nil, fieldError(field, fmt.Sprintf("contains a %q block, which isn't a certificate or a private key", block.Type))
		}
	}
}

// parseKey reads a private key block and checks its type and size. Whatever the block's label
// says, PKCS#8, PKCS#1 and SEC 1 are all tried, since files are often mislabelled.
func parseKey(block *pem.Block) (crypto.Signer, string, error) {
	var key any
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		key = k
	} else {
		return nil, "", fieldError("key", "the private key can't be read: it should be PKCS#8, PKCS#1 (RSA) or SEC 1 (EC) in PEM")
	}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		bits := k.N.BitLen()
		if bits < minRSABits {
			return nil, "", fieldError("key", fmt.Sprintf("an RSA key of %d bits is too small: use at least %d bits", bits, minRSABits))
		}
		return k, fmt.Sprintf("RSA %d", bits), nil
	case *ecdsa.PrivateKey:
		switch k.Curve {
		case elliptic.P256(), elliptic.P384(), elliptic.P521():
			return k, "ECDSA " + k.Curve.Params().Name, nil
		}
		return nil, "", fieldError("key", fmt.Sprintf("an ECDSA key on curve %s isn't supported: use P-256, P-384 or P-521", k.Curve.Params().Name))
	default:
		return nil, "", fieldError("key", fmt.Sprintf("a %s key isn't supported: use RSA (2048 bits or more) or ECDSA (P-256, P-384, P-521)", keyKind(key)))
	}
}

func keyKind(key any) string {
	switch key.(type) {
	case ed25519.PrivateKey:
		return "Ed25519"
	case *ecdh.PrivateKey:
		return "ECDH"
	}
	return "this kind of"
}

func dedupe(certs []*x509.Certificate) []*x509.Certificate {
	var out []*x509.Certificate
	for _, c := range certs {
		if !slices.ContainsFunc(out, func(o *x509.Certificate) bool { return bytes.Equal(o.Raw, c.Raw) }) {
			out = append(out, c)
		}
	}
	return out
}

func samePublicKey(a any, b crypto.PublicKey) bool {
	eq, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && eq.Equal(b)
}

// checkLeaf checks the leaf is one a server can present now.
func checkLeaf(leaf *x509.Certificate, now time.Time) error {
	if len(leaf.DNSNames) == 0 {
		return fieldError("certificate", "it has no DNS names (subject alternative names)")
	}
	if len(leaf.ExtKeyUsage) > 0 && !slices.ContainsFunc(leaf.ExtKeyUsage, func(u x509.ExtKeyUsage) bool {
		return u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny
	}) {
		return fieldError("certificate", "it isn't made for servers (its extended key usage doesn't include server authentication)")
	}
	if now.Before(leaf.NotBefore) {
		return fieldError("certificate", "it isn't valid yet: it starts on "+leaf.NotBefore.UTC().Format(time.DateOnly))
	}
	if !leaf.NotAfter.After(now) {
		return fieldError("certificate", "it expired on "+leaf.NotAfter.UTC().Format(time.DateOnly))
	}
	return nil
}

// buildChain follows the given certificates from the leaf up, each to the one that signed it,
// until a self-signed certificate or one whose issuer wasn't given. It is complete when it ends
// at a self-signed certificate (a root the owner gave), or when part of it verifies against roots.
// Every certificate given must be part of the chain.
func buildChain(leaf *x509.Certificate, pool []*x509.Certificate, roots *x509.CertPool, now time.Time) (chain []*x509.Certificate, trusted bool, err error) {
	chain = []*x509.Certificate{leaf}
	for cur := leaf; !selfSigned(cur); {
		next := slices.IndexFunc(pool, func(c *x509.Certificate) bool {
			return bytes.Equal(c.RawSubject, cur.RawIssuer) && cur.CheckSignatureFrom(c) == nil
		})
		if next < 0 {
			break
		}
		cur = pool[next]
		chain = append(chain, cur)
		pool = slices.Delete(pool, next, next+1)
	}

	for _, c := range chain {
		if _, verr := c.Verify(x509.VerifyOptions{
			Roots:       roots,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		}); verr == nil {
			trusted = true
			break
		}
	}
	last := chain[len(chain)-1]
	if !trusted && !selfSigned(last) {
		return nil, false, fieldError("certificate", fmt.Sprintf(
			"the chain is incomplete: the certificate of %s is missing; add your issuer's intermediate certificates", issuerLabel(last)))
	}
	if len(pool) > 0 {
		return nil, false, fieldError("certificate", fmt.Sprintf(
			"the certificate of %s isn't part of this certificate's chain; remove it", subjectLabel(pool[0])))
	}
	return chain, trusted, nil
}

// selfSigned says c carries its own signature: a root, or a self-signed leaf.
func selfSigned(c *x509.Certificate) bool {
	return bytes.Equal(c.RawIssuer, c.RawSubject) &&
		c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
}

func issuerLabel(c *x509.Certificate) string {
	if c.Issuer.CommonName != "" {
		return c.Issuer.CommonName
	}
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return "its issuer"
}

func subjectLabel(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	if len(c.Subject.Organization) > 0 {
		return c.Subject.Organization[0]
	}
	return "an unnamed certificate"
}

func subjectName(leaf *x509.Certificate) string {
	if leaf.Subject.CommonName != "" {
		return leaf.Subject.CommonName
	}
	return strings.ToLower(leaf.DNSNames[0])
}

func leafNames(leaf *x509.Certificate) []string {
	names := make([]string, 0, len(leaf.DNSNames))
	for _, n := range leaf.DNSNames {
		names = append(names, strings.ToLower(n))
	}
	sort.Strings(names)
	return slices.Compact(names)
}
