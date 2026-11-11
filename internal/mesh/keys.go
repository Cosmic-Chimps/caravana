package mesh

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// keyLen is the size of a WireGuard key (X25519), in bytes.
const keyLen = 32

// NewKey makes a WireGuard key pair, both base64 as `wg genkey` and `wg pubkey` write them. The
// private key never leaves the server that made it.
func NewKey() (private, public string, err error) {
	var k [keyLen]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", "", fmt.Errorf("make a WireGuard key: %w", err)
	}
	// Clamping, as wg genkey does (RFC 7748 §5).
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	private = base64.StdEncoding.EncodeToString(k[:])
	public, err = PublicKey(private)
	if err != nil {
		return "", "", err
	}
	return private, public, nil
}

// PublicKey is the public key of a private one, as `wg pubkey` prints it. The error never holds the
// private key.
func PublicKey(private string) (string, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(private)
	if err != nil || len(raw) != keyLen {
		return "", fmt.Errorf("the private key isn't a WireGuard key (base64 of %d bytes)", keyLen)
	}
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("compute the public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// ValidPublicKey says s is 44 characters of base64 that decode to 32 bytes.
func ValidPublicKey(s string) bool {
	if len(s) != 44 {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	return err == nil && len(raw) == keyLen
}
