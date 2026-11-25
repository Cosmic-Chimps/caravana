package certs

import (
	"bytes"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
)

var (
	now    = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	before = now.Add(-24 * time.Hour)
	after  = now.Add(60 * 24 * time.Hour)
)

func parse(t *testing.T, cert, key []byte, roots *x509.CertPool) (Parsed, error) {
	t.Helper()
	return Parse(cert, key, Options{Roots: roots, Now: now})
}

// wantError checks err is an *Error on field whose message holds each of parts.
func wantError(t *testing.T, err error, field string, parts ...string) {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want an *Error on %q", err, field)
	}
	if ce.Field != field {
		t.Errorf("field = %q, want %q (message %q)", ce.Field, field, ce.Message)
	}
	for _, p := range parts {
		if !strings.Contains(ce.Message, p) {
			t.Errorf("message %q lacks %q", ce.Message, p)
		}
	}
}

func decodeCerts(t *testing.T, data []byte) []*x509.Certificate {
	t.Helper()
	var out []*x509.Certificate
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return out
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
}

func TestParseValidChains(t *testing.T) {
	root := certstest.NewCA(t, "Test Root", certstest.Organization("Acme Trust"))
	inter := certstest.NewIntermediate(t, root, "Test Intermediate", certstest.Organization("Acme Issuing"))
	rsaRoot := certstest.NewCA(t, "RSA Root", certstest.RSA())

	tests := []struct {
		name     string
		leaf     func() *certstest.Leaf
		chain    func(l *certstest.Leaf) []byte
		keyInput func(l *certstest.Leaf) []byte
		keyType  string
		trusted  bool
		roots    *x509.CertPool
		issuer   string
		certs    int // certificates in ChainPEM
	}{
		{
			name:     "ECDSA P-256 under an intermediate, trusted, PKCS#8",
			leaf:     func() *certstest.Leaf { return certstest.NewLeaf(t, inter, []string{"a.example.test"}, before, after) },
			chain:    func(l *certstest.Leaf) []byte { return certstest.Chain(l.Cert, inter.Cert) },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-256", trusted: true, roots: certstest.Pool(root), issuer: "Acme Issuing", certs: 2,
		},
		{
			name:     "trusted, root given too and dropped",
			leaf:     func() *certstest.Leaf { return certstest.NewLeaf(t, inter, []string{"a.example.test"}, before, after) },
			chain:    func(l *certstest.Leaf) []byte { return certstest.Chain(l.Cert, inter.Cert, root.Cert) },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.SEC1) },
			keyType:  "ECDSA P-256", trusted: true, roots: certstest.Pool(root), issuer: "Acme Issuing", certs: 2,
		},
		{
			name:     "trusted, root not given",
			leaf:     func() *certstest.Leaf { return certstest.NewLeaf(t, inter, []string{"a.example.test"}, before, after) },
			chain:    func(l *certstest.Leaf) []byte { return certstest.Chain(l.Cert, inter.Cert) },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-256", trusted: true, roots: certstest.Pool(root), issuer: "Acme Issuing", certs: 2,
		},
		{
			name:     "trusted, signed by a root directly",
			leaf:     func() *certstest.Leaf { return certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after) },
			chain:    func(l *certstest.Leaf) []byte { return l.PEM() },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-256", trusted: true, roots: certstest.Pool(root), issuer: "Acme Trust", certs: 1,
		},
		{
			name: "private CA root given, not in the roots: untrusted but complete",
			leaf: func() *certstest.Leaf {
				return certstest.NewLeaf(t, rsaRoot, []string{"a.example.test"}, before, after)
			},
			chain:    func(l *certstest.Leaf) []byte { return certstest.Chain(l.Cert, rsaRoot.Cert) },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-256", trusted: false, roots: certstest.Pool(root), issuer: "RSA Root", certs: 1,
		},
		{
			name: "private CA with the system roots (nil)",
			leaf: func() *certstest.Leaf {
				return certstest.NewLeaf(t, rsaRoot, []string{"a.example.test"}, before, after)
			},
			chain:    func(l *certstest.Leaf) []byte { return certstest.Chain(l.Cert, rsaRoot.Cert) },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-256", trusted: false, roots: nil, issuer: "RSA Root", certs: 1,
		},
		{
			name: "self-signed leaf",
			leaf: func() *certstest.Leaf {
				return certstest.NewSelfSigned(t, []string{"a.example.test"}, before, after, certstest.Organization("Me"))
			},
			chain:    func(l *certstest.Leaf) []byte { return l.PEM() },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-256", trusted: false, roots: certstest.Pool(root), issuer: "Me", certs: 1,
		},
		{
			name: "RSA 2048, PKCS#1",
			leaf: func() *certstest.Leaf {
				return certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after, certstest.RSA())
			},
			chain:    func(l *certstest.Leaf) []byte { return l.PEM() },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS1) },
			keyType:  "RSA 2048", trusted: true, roots: certstest.Pool(root), issuer: "Acme Trust", certs: 1,
		},
		{
			name: "RSA 2048, PKCS#8",
			leaf: func() *certstest.Leaf {
				return certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after, certstest.RSA())
			},
			chain:    func(l *certstest.Leaf) []byte { return l.PEM() },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "RSA 2048", trusted: true, roots: certstest.Pool(root), issuer: "Acme Trust", certs: 1,
		},
		{
			name: "ECDSA P-384",
			leaf: func() *certstest.Leaf {
				return certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after, certstest.Curve(elliptic.P384()))
			},
			chain:    func(l *certstest.Leaf) []byte { return l.PEM() },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.SEC1) },
			keyType:  "ECDSA P-384", trusted: true, roots: certstest.Pool(root), issuer: "Acme Trust", certs: 1,
		},
		{
			name: "ECDSA P-521",
			leaf: func() *certstest.Leaf {
				return certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after, certstest.Curve(elliptic.P521()))
			},
			chain:    func(l *certstest.Leaf) []byte { return l.PEM() },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-521", trusted: true, roots: certstest.Pool(root), issuer: "Acme Trust", certs: 1,
		},
		{
			name: "no EKU extension is fine",
			leaf: func() *certstest.Leaf {
				return certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after, certstest.ExtKeyUsages())
			},
			chain:    func(l *certstest.Leaf) []byte { return l.PEM() },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-256", trusted: true, roots: certstest.Pool(root), issuer: "Acme Trust", certs: 1,
		},
		{
			name: "any EKU is fine",
			leaf: func() *certstest.Leaf {
				return certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after, certstest.ExtKeyUsages(x509.ExtKeyUsageAny))
			},
			chain:    func(l *certstest.Leaf) []byte { return l.PEM() },
			keyInput: func(l *certstest.Leaf) []byte { return l.KeyPEM(t, certstest.PKCS8) },
			keyType:  "ECDSA P-256", trusted: true, roots: certstest.Pool(root), issuer: "Acme Trust", certs: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := tt.leaf()
			p, err := parse(t, tt.chain(l), tt.keyInput(l), tt.roots)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if p.KeyType != tt.keyType || p.Trusted != tt.trusted || p.Issuer != tt.issuer {
				t.Errorf("KeyType, Trusted, Issuer = %q, %v, %q; want %q, %v, %q",
					p.KeyType, p.Trusted, p.Issuer, tt.keyType, tt.trusted, tt.issuer)
			}
			if p.Fingerprint != Fingerprint(l.Cert) || len(p.Fingerprint) != 64 || p.Fingerprint != strings.ToLower(p.Fingerprint) {
				t.Errorf("Fingerprint = %q", p.Fingerprint)
			}
			if !p.NotBefore.Equal(before) || !p.NotAfter.Equal(after) || p.NotAfter.Location() != time.UTC {
				t.Errorf("dates = %v, %v", p.NotBefore, p.NotAfter)
			}
			got := decodeCerts(t, p.ChainPEM)
			if len(got) != tt.certs {
				t.Fatalf("ChainPEM holds %d certificates, want %d", len(got), tt.certs)
			}
			if !got[0].Equal(l.Cert) {
				t.Error("ChainPEM doesn't start with the leaf")
			}
			// The stored key is one block, and it reads back as the leaf's key.
			kb, rest := pem.Decode(p.KeyPEM)
			if kb == nil || len(bytes.TrimSpace(rest)) != 0 {
				t.Fatalf("KeyPEM = %q, want exactly one block", p.KeyPEM)
			}
			// Normalized output goes back in unchanged, except where the owner's own root was dropped.
			if tt.trusted || tt.certs == 1 && p.Issuer == p.Subject {
				if _, err := parse(t, p.ChainPEM, p.KeyPEM, tt.roots); err != nil {
					t.Errorf("the normalized chain and key are not accepted again: %v", err)
				}
			}
		})
	}
}

func TestParseNames(t *testing.T) {
	root := certstest.NewCA(t, "Root")
	l := certstest.NewLeaf(t, root, []string{"B.Example.test", "*.example.test", "a.example.test", "b.example.test"}, before, after, certstest.CommonName(""))
	p, err := parse(t, l.PEM(), l.KeyPEM(t, certstest.PKCS8), certstest.Pool(root))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"*.example.test", "a.example.test", "b.example.test"}; !slices.Equal(p.Names, want) {
		t.Errorf("Names = %v, want %v (lower case, sorted, no duplicates)", p.Names, want)
	}
	// No common name: the subject is the first name.
	if p.Subject != "b.example.test" {
		t.Errorf("Subject = %q, want the first name, lower case", p.Subject)
	}
	cn := certstest.NewLeaf(t, root, []string{"x.example.test"}, before, after, certstest.CommonName("My Site"))
	p, err = parse(t, cn.PEM(), cn.KeyPEM(t, certstest.PKCS8), certstest.Pool(root))
	if err != nil || p.Subject != "My Site" {
		t.Errorf("Subject = %q, %v; want the common name", p.Subject, err)
	}
}

func TestParseOrderAndPlacement(t *testing.T) {
	root := certstest.NewCA(t, "Root")
	inter := certstest.NewIntermediate(t, root, "Inter")
	l := certstest.NewLeaf(t, inter, []string{"a.example.test"}, before, after)
	roots := certstest.Pool(root)
	key := l.KeyPEM(t, certstest.PKCS8)

	t.Run("shuffled chain comes out in chain order", func(t *testing.T) {
		p, err := parse(t, certstest.Chain(root.Cert, inter.Cert, l.Cert), key, roots)
		if err != nil {
			t.Fatal(err)
		}
		got := decodeCerts(t, p.ChainPEM)
		if len(got) != 2 || !got[0].Equal(l.Cert) || !got[1].Equal(inter.Cert) {
			t.Errorf("ChainPEM = %d certificates, want leaf then intermediate", len(got))
		}
	})
	t.Run("the key inside the certificate input", func(t *testing.T) {
		in := append(append(certstest.Chain(l.Cert), key...), certstest.Chain(inter.Cert)...)
		if _, err := parse(t, in, nil, roots); err != nil {
			t.Errorf("Parse: %v", err)
		}
	})
	t.Run("certificates also in the key input", func(t *testing.T) {
		if _, err := parse(t, l.PEM(), append(key, certstest.Chain(inter.Cert)...), roots); err != nil {
			t.Errorf("Parse: %v", err)
		}
	})
	t.Run("the same certificate twice", func(t *testing.T) {
		p, err := parse(t, certstest.Chain(l.Cert, l.Cert, inter.Cert), key, roots)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(decodeCerts(t, p.ChainPEM)); n != 2 {
			t.Errorf("ChainPEM holds %d certificates, want 2", n)
		}
	})
	t.Run("text around the blocks is fine", func(t *testing.T) {
		in := append([]byte("Bag Attributes\n    friendlyName: x\n"), certstest.Chain(l.Cert, inter.Cert)...)
		if _, err := parse(t, in, key, roots); err != nil {
			t.Errorf("Parse: %v", err)
		}
	})
}

func TestParseRefusals(t *testing.T) {
	root := certstest.NewCA(t, "Root")
	inter := certstest.NewIntermediate(t, root, "Acme Issuing CA")
	otherRoot := certstest.NewCA(t, "Other Root")
	roots := certstest.Pool(root)
	good := certstest.NewLeaf(t, inter, []string{"a.example.test"}, before, after)
	goodKey := good.KeyPEM(t, certstest.PKCS8)
	goodChain := certstest.Chain(good.Cert, inter.Cert)
	other := certstest.NewLeaf(t, root, []string{"b.example.test"}, before, after)

	tests := []struct {
		name      string
		cert, key []byte
		field     string
		parts     []string
	}{
		{"nothing at all", nil, nil, "certificate", []string{"no certificate"}},
		{"no key", goodChain, nil, "key", []string{"no private key found"}},
		{"two keys", goodChain, append(append([]byte{}, goodKey...), other.KeyPEM(t, certstest.PKCS8)...), "key", []string{"more than one"}},
		{"the key of another certificate", goodChain, other.KeyPEM(t, certstest.PKCS8), "key", []string{"doesn't match any certificate"}},
		{"encrypted PKCS#8 key", goodChain, certstest.EncryptedKeyPEM(), "key", []string{"encrypted", "openssl pkey -in key.pem -out plain.pem"}},
		{"encrypted legacy key", goodChain, certstest.LegacyEncryptedKeyPEM(), "key", []string{"encrypted", "decrypt it first"}},
		{"not PEM in the certificate", []byte("hello"), goodKey, "certificate", []string{"isn't PEM"}},
		{"not PEM in the key", goodChain, []byte("hello"), "key", []string{"isn't PEM"}},
		{"trailing junk", append(append([]byte{}, goodChain...), "tail"...), goodKey, "certificate", []string{"isn't PEM"}},
		{"another block type", append(append([]byte{}, goodChain...), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte{1}})...), goodKey, "certificate", []string{"CERTIFICATE REQUEST"}},
		{"a public key block", goodChain, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte{1}}), "key", []string{"PUBLIC KEY"}},
		{"a garbage certificate", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}}), goodKey, "certificate", []string{"can't be read"}},
		{"a garbage key", goodChain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}}), "key", []string{"can't be read"}},
		{"leaf without its issuer", good.PEM(), goodKey, "certificate", []string{"incomplete", "Acme Issuing CA", "intermediate"}},
		{"intermediate not signed by a root we trust", goodChain, goodKey, "", nil}, // placeholder: replaced below
		{"an unrelated certificate", append(append([]byte{}, goodChain...), other.PEM()...), goodKey, "certificate", []string{"b.example.test", "isn't part of"}},
		{"a root that isn't this chain's", append(append([]byte{}, goodChain...), otherRoot.PEM()...), goodKey, "certificate", []string{"Other Root", "isn't part of"}},
		{"too large certificate input", bytes.Repeat([]byte("x"), 64<<10+1), goodKey, "certificate", []string{"64 KiB"}},
		{"too large key input", goodChain, bytes.Repeat([]byte("x"), 64<<10+1), "key", []string{"64 KiB"}},
	}
	for _, tt := range tests {
		if tt.field == "" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(t, tt.cert, tt.key, roots)
			wantError(t, err, tt.field, tt.parts...)
		})
	}

	t.Run("a chain that doesn't verify and has no given root", func(t *testing.T) {
		stranger := certstest.NewLeaf(t, inter, []string{"a.example.test"}, before, after)
		_, err := parse(t, certstest.Chain(stranger.Cert, inter.Cert), stranger.KeyPEM(t, certstest.PKCS8), certstest.Pool(otherRoot))
		wantError(t, err, "certificate", "incomplete", "Root")
	})
}

func TestParseKeyTypes(t *testing.T) {
	root := certstest.NewCA(t, "Root")
	roots := certstest.Pool(root)

	small := certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after, certstest.RSABits(1024))
	_, err := parse(t, small.PEM(), small.KeyPEM(t, certstest.PKCS1), roots)
	wantError(t, err, "key", "RSA key of 1024 bits", "2048")

	p224 := certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after, certstest.Curve(elliptic.P224()))
	_, err = parse(t, p224.PEM(), p224.KeyPEM(t, certstest.SEC1), roots)
	wantError(t, err, "key", "P-224")
}

func TestParseLeafRefusals(t *testing.T) {
	root := certstest.NewCA(t, "Root")
	roots := certstest.Pool(root)
	check := func(t *testing.T, l *certstest.Leaf) error {
		t.Helper()
		_, err := parse(t, l.PEM(), l.KeyPEM(t, certstest.PKCS8), roots)
		return err
	}

	t.Run("no DNS names", func(t *testing.T) {
		wantError(t, check(t, certstest.NewLeaf(t, root, nil, before, after, certstest.CommonName("example.test"))),
			"certificate", "no DNS names", "subject alternative names")
	})
	t.Run("client authentication only", func(t *testing.T) {
		wantError(t, check(t, certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after,
			certstest.ExtKeyUsages(x509.ExtKeyUsageClientAuth))), "certificate", "server authentication")
	})
	t.Run("not valid yet", func(t *testing.T) {
		wantError(t, check(t, certstest.NewLeaf(t, root, []string{"a.example.test"}, now.Add(48*time.Hour), after)),
			"certificate", "isn't valid yet", "2026-11-14")
	})
	t.Run("expired", func(t *testing.T) {
		wantError(t, check(t, certstest.NewLeaf(t, root, []string{"a.example.test"}, now.Add(-90*24*time.Hour), now.Add(-time.Hour))),
			"certificate", "expired on 2026-11-12")
	})
	t.Run("ends exactly now is expired", func(t *testing.T) {
		wantError(t, check(t, certstest.NewLeaf(t, root, []string{"a.example.test"}, before, now)), "certificate", "expired")
	})
	t.Run("Now defaults to the clock", func(t *testing.T) {
		l := certstest.NewLeaf(t, root, []string{"a.example.test"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		if _, err := Parse(l.PEM(), l.KeyPEM(t, certstest.PKCS8), Options{Roots: roots}); err != nil {
			t.Errorf("Parse with the zero Now: %v", err)
		}
	})
}

func TestParseStoredSizeLimit(t *testing.T) {
	// A chain of 110 intermediates is about 50 KiB of PEM: over what one Nomad Variable should hold.
	root := certstest.NewCA(t, "Root")
	parent := root
	var inters []*x509.Certificate
	for i := 0; i < 110; i++ {
		parent = certstest.NewIntermediate(t, parent, "Inter")
		inters = append(inters, parent.Cert)
	}
	l := certstest.NewLeaf(t, parent, []string{"a.example.test"}, before, after)
	slices.Reverse(inters)
	chain := certstest.Chain(append([]*x509.Certificate{l.Cert}, inters...)...)
	if len(chain) > 64<<10 {
		t.Fatalf("test chain is %d bytes, over the input limit itself", len(chain))
	}
	_, err := parse(t, chain, l.KeyPEM(t, certstest.PKCS8), certstest.Pool(root))
	wantError(t, err, "certificate", "48 KiB")
}

func TestErrorsNeverHoldKeyBytes(t *testing.T) {
	root := certstest.NewCA(t, "Root")
	l := certstest.NewLeaf(t, root, []string{"a.example.test"}, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	keyPEM := l.KeyPEM(t, certstest.PKCS8)
	block, _ := pem.Decode(keyPEM)
	for _, key := range [][]byte{keyPEM, certstest.EncryptedKeyPEM(), []byte("junk")} {
		_, err := parse(t, l.PEM(), key, certstest.Pool(root))
		if err == nil {
			t.Fatal("want an error")
		}
		if strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(block.Bytes)) || strings.Contains(err.Error(), string(block.Bytes)) {
			t.Errorf("error holds key material: %v", err)
		}
	}
}

func TestIssuerName(t *testing.T) {
	root := certstest.NewCA(t, "Root CN")
	withOrg := certstest.NewCA(t, "Root CN", certstest.Organization("Acme"))
	a := certstest.NewLeaf(t, root, []string{"a.example.test"}, before, after)
	b := certstest.NewLeaf(t, withOrg, []string{"a.example.test"}, before, after)
	if got := IssuerName(a.Cert); got != "Root CN" {
		t.Errorf("IssuerName without an organization = %q", got)
	}
	if got := IssuerName(b.Cert); got != "Acme" {
		t.Errorf("IssuerName with an organization = %q", got)
	}
}

// TestPure keeps the package free of Caravana's other packages and of Nomad.
func TestPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, "github.com/cosmic-chimps/caravana/") || strings.HasPrefix(path, "github.com/hashicorp/nomad") {
				t.Errorf("%s imports %q, which a pure package must not", name, path)
			}
		}
	}
	if checked < 4 {
		t.Errorf("checked %d files, want at least 4", checked)
	}
}
