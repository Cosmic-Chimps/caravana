package certs

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	maxServer = 512
	maxCAPEM  = 32 << 10
	// minHMACBytes is the shortest EAB HMAC key accepted, decoded.
	minHMACBytes = 16
)

// ValidServer checks the address of an ACME directory: https, a host, no user info, no fragment,
// at most 512 printable ASCII characters, and none that could end a quoted string or start an
// expansion where Traefik's job renders it (space, quotes, backslash, braces, $).
func ValidServer(address string) error {
	if address == "" {
		return fieldError("server", "is required")
	}
	if len(address) > maxServer {
		return fieldError("server", "is longer than 512 characters")
	}
	for _, r := range address {
		if r < 0x21 || r > 0x7e {
			return fieldError("server", "may only hold printable ASCII characters without spaces")
		}
		if strings.ContainsRune("\"'`\\{}$", r) {
			return fieldError("server", fmt.Sprintf("may not contain %q", r))
		}
	}
	u, err := url.Parse(address)
	if err != nil {
		return fieldError("server", "isn't a valid address")
	}
	switch {
	case u.Scheme != "https":
		return fieldError("server", "must be an https:// address")
	case u.Hostname() == "":
		return fieldError("server", "has no host")
	case u.User != nil:
		return fieldError("server", "may not hold a user name or password")
	case u.Fragment != "" || strings.Contains(address, "#"):
		return fieldError("server", "may not have a fragment (#...)")
	}
	return nil
}

var eabKeyIDRE = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{1,256}$`)

// ValidEABKeyID checks an External Account Binding key ID.
func ValidEABKeyID(id string) error {
	if !eabKeyIDRE.MatchString(id) {
		return fieldError("eabKeyId", "must be 1 to 256 letters, digits or . _ ~ + / = -")
	}
	return nil
}

var hmacReplacer = strings.NewReplacer("+", "-", "/", "_")

// NormalizeHMAC takes an EAB HMAC key as the issuer shows it (base64url or standard base64, with
// or without padding) and returns it as base64url without padding, which is what Traefik reads.
// The key must decode to at least 16 bytes. Nothing of the key is in the error.
func NormalizeHMAC(key string) (string, error) {
	key = strings.TrimRight(strings.TrimSpace(key), "=")
	raw, err := base64.RawURLEncoding.DecodeString(hmacReplacer.Replace(key))
	if err != nil {
		return "", fieldError("eabHmacKey", "isn't base64: copy the key exactly as the issuer shows it")
	}
	if len(raw) < minHMACBytes {
		return "", fieldError("eabHmacKey", "is too short to be an HMAC key: it must decode to at least 16 bytes")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// NormalizeCAPEM checks the CA certificate of a private ACME issuer: one or more certificates in
// PEM that parse, and nothing else, at most 32 KiB. It returns them re-encoded.
func NormalizeCAPEM(s string) (string, error) {
	if len(s) > maxCAPEM {
		return "", fieldError("caCertificate", "is larger than 32 KiB")
	}
	var out bytes.Buffer
	rest := []byte(s)
	n := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			if len(bytes.TrimSpace(rest)) > 0 {
				return "", fieldError("caCertificate", "contains data that isn't a PEM certificate")
			}
			break
		}
		if block.Type != "CERTIFICATE" {
			return "", fieldError("caCertificate", fmt.Sprintf("contains a %q block: only certificates belong here", block.Type))
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return "", fieldError("caCertificate", "contains a certificate that can't be read")
		}
		out.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}))
		n++
	}
	if n == 0 {
		return "", fieldError("caCertificate", "has no certificate")
	}
	// pem.Decode skips text it doesn't understand, including a block that isn't at the start of its
	// line. Only whitespace may differ between what came in and what goes out, so nothing is
	// dropped quietly.
	if stripSpace(out.String()) != stripSpace(s) {
		return "", fieldError("caCertificate", "contains data that isn't a PEM certificate")
	}
	return out.String(), nil
}

func stripSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}
