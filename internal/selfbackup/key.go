package selfbackup

import (
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The recovery key (plan D3): "cvrk_" and the lowercase base32, with no padding, of the 32-byte X25519
// private key followed by the first 4 bytes of its SHA-256, so a mistyped key is refused as mistyped and
// not as the wrong key. It is shown once; nothing stores it.
const keyPrefix = "cvrk_"

var keyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// The two ways a typed key is refused.
var (
	// ErrKeyFormat: it isn't a recovery key at all.
	ErrKeyFormat = errors.New("that isn't a recovery key (they start with cvrk_)")
	// ErrKeyChecksum: it is the shape of one but has a typo.
	ErrKeyChecksum = errors.New("the recovery key has a typo: its check characters don't match")
)

// RecoveryKey is the private half of a backup key.
type RecoveryKey struct {
	private *ecdh.PrivateKey
}

// NewKey makes a key from 32 bytes read from random (crypto/rand.Reader in production).
func NewKey(random io.Reader) (RecoveryKey, error) {
	seed := make([]byte, 32)
	if _, err := io.ReadFull(random, seed); err != nil {
		return RecoveryKey{}, fmt.Errorf("make a recovery key: %w", err)
	}
	return keyFromSeed(seed)
}

func keyFromSeed(seed []byte) (RecoveryKey, error) {
	priv, err := ecdh.X25519().NewPrivateKey(seed)
	if err != nil {
		return RecoveryKey{}, fmt.Errorf("make a recovery key: %w", err)
	}
	return RecoveryKey{private: priv}, nil
}

// String is the key as the owner keeps it. It is the only place the private key is written out.
func (k RecoveryKey) String() string {
	seed := k.private.Bytes()
	sum := sha256.Sum256(seed)
	return keyPrefix + strings.ToLower(keyEncoding.EncodeToString(append(seed, sum[:4]...)))
}

// ParseKey reads a typed key. Case, spaces and line breaks don't matter (a key is easier to copy in
// groups), so a key written on paper in four lines still works.
func ParseKey(s string) (RecoveryKey, error) {
	s = strings.ToLower(strings.Join(strings.Fields(s), ""))
	rest, ok := strings.CutPrefix(s, keyPrefix)
	if !ok {
		return RecoveryKey{}, ErrKeyFormat
	}
	raw, err := keyEncoding.DecodeString(strings.ToUpper(rest))
	if err != nil || len(raw) != 36 {
		return RecoveryKey{}, ErrKeyFormat
	}
	seed, check := raw[:32], raw[32:]
	if sum := sha256.Sum256(seed); string(sum[:4]) != string(check) {
		return RecoveryKey{}, ErrKeyChecksum
	}
	return keyFromSeed(seed)
}

// Public is the public key Caravana stores and each backup is encrypted to: 32 bytes.
func (k RecoveryKey) Public() []byte { return k.private.PublicKey().Bytes() }

// Fingerprint is the first 16 hex digits of the SHA-256 of the public key: how a key is named
// everywhere (a backup's header, the page, an error).
func (k RecoveryKey) Fingerprint() string { return Fingerprint(k.Public()) }

// Fingerprint names a public key.
func Fingerprint(public []byte) string {
	sum := sha256.Sum256(public)
	return hex.EncodeToString(sum[:8])
}

// ValidPublic checks that b is an X25519 public key.
func ValidPublic(b []byte) error {
	if _, err := ecdh.X25519().NewPublicKey(b); err != nil {
		return fmt.Errorf("not an X25519 public key: %w", err)
	}
	return nil
}
