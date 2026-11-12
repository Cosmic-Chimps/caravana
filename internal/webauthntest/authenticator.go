// Package webauthntest is a software authenticator for tests: it makes the JSON a browser posts
// after navigator.credentials.create() and .get(), from the options a server sent. It is what
// feature 033's unit tests and lab step drive, so no browser or device is needed.
//
// It signs with ES256 (a P-256 key from crypto/ecdsa) and answers with "none" attestation. A few
// public fields are knobs for the failures a server must refuse: the sign count, the user
// verification flag, the user handle, and the relying party ID and origin it signs for.
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Authenticator flag bits (WebAuthn §6.1).
const (
	flagUserPresent    = 0x01
	flagUserVerified   = 0x04
	flagBackupEligible = 0x08
	flagBackupState    = 0x10
	flagAttested       = 0x40
)

// Authenticator is one passkey held by a pretend device: one key pair and one credential ID.
type Authenticator struct {
	key          *ecdsa.PrivateKey
	credentialID []byte
	aaguid       [16]byte

	// RPID is the relying party ID it signs for (the hash in every authenticator data). New sets it;
	// change it to sign for another domain than the one the options name.
	RPID string
	// UserHandle is the user.id the credential was made for. Create takes it from the options; set it
	// to answer with another one. Empty: Get leaves userHandle out of its answer.
	UserHandle []byte
	// Counter is the sign count the next Get writes, and Get adds one after it, so a new
	// authenticator counts 1, 2, 3, ... Set it to repeat a count or go backwards. Create writes 0.
	Counter uint32
	// UserVerified is the UV flag of what it signs: true by default, false for a device that only
	// checked the user was present.
	UserVerified bool
	// BackupEligible and BackupState are the BE and BS flags (a synced passkey has both).
	BackupEligible bool
	BackupState    bool
	// Transports are reported by Create. New sets "internal".
	Transports []string
}

// New returns an authenticator with a fresh key and credential ID that signs for rpID, with user
// verification on and a sign count starting at 1. It panics if the system has no randomness, which
// is fine in a test helper and never happens.
func New(rpID string) *Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("webauthntest: generate key: %v", err)) // a test helper: crypto/rand does not fail
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		panic(fmt.Sprintf("webauthntest: credential ID: %v", err))
	}
	return &Authenticator{
		key: key, credentialID: id, RPID: rpID, Counter: 1, UserVerified: true, Transports: []string{"internal"},
	}
}

// CredentialID is the credential's ID, as the server stores it.
func (a *Authenticator) CredentialID() []byte { return append([]byte(nil), a.credentialID...) }

// PrivateKeySentinel is the private key's bytes written the ways a leak could show them: lower-case
// hex, standard base64 and URL-safe base64. A test searches pages, logs and the database for each.
func (a *Authenticator) PrivateKeySentinel() []string {
	d, err := a.key.Bytes() // the private scalar, 32 bytes
	if err != nil {
		return nil
	}
	return []string{
		hex.EncodeToString(d),
		base64.StdEncoding.EncodeToString(d),
		base64.RawURLEncoding.EncodeToString(d),
	}
}

// creationOptions is the part of PublicKeyCredentialCreationOptions that Create reads.
type creationOptions struct {
	Challenge string `json:"challenge"`
	User      struct {
		ID string `json:"id"`
	} `json:"user"`
}

// requestOptions is the part of PublicKeyCredentialRequestOptions that Get reads.
type requestOptions struct {
	Challenge string `json:"challenge"`
}

// unwrap accepts the options as the server sends them, either the publicKey object itself or
// {"publicKey": {...}}, and returns the object.
func unwrap(optionsJSON []byte) ([]byte, error) {
	var outer struct {
		PublicKey json.RawMessage `json:"publicKey"`
	}
	if err := json.Unmarshal(optionsJSON, &outer); err != nil {
		return nil, fmt.Errorf("webauthntest: options are not JSON: %w", err)
	}
	if len(outer.PublicKey) > 0 {
		return outer.PublicKey, nil
	}
	return optionsJSON, nil
}

// Create answers a registration like a browser does: the JSON of the PublicKeyCredential that
// navigator.credentials.create() returned, with every binary field in base64url. origin is what the
// browser puts in the client data, for example "https://console.example.com". It takes the
// credential's user handle from the options.
func (a *Authenticator) Create(optionsJSON []byte, origin string) ([]byte, error) {
	raw, err := unwrap(optionsJSON)
	if err != nil {
		return nil, err
	}
	var opts creationOptions
	if err := json.Unmarshal(raw, &opts); err != nil {
		return nil, fmt.Errorf("webauthntest: creation options: %w", err)
	}
	if opts.Challenge == "" {
		return nil, errors.New("webauthntest: the options have no challenge")
	}
	handle, err := base64.RawURLEncoding.DecodeString(opts.User.ID)
	if err != nil {
		return nil, fmt.Errorf("webauthntest: user.id is not base64url: %w", err)
	}
	a.UserHandle = handle

	// Authenticator data with attested credential data (WebAuthn §6.5.1).
	data := a.authData(0, true)
	data = append(data, a.aaguid[:]...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(a.credentialID)))
	data = append(data, a.credentialID...)
	cose, err := a.coseKey()
	if err != nil {
		return nil, err
	}
	data = append(data, cose...)

	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": data})
	if err != nil {
		return nil, fmt.Errorf("webauthntest: attestation object: %w", err)
	}
	clientData, err := clientDataJSON("webauthn.create", opts.Challenge, origin)
	if err != nil {
		return nil, err
	}

	id := b64(a.credentialID)
	return json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key", "authenticatorAttachment": "platform",
		"response": map[string]any{
			"clientDataJSON":    b64(clientData),
			"attestationObject": b64(attestation),
			"transports":        a.Transports,
		},
		"clientExtensionResults": map[string]any{},
	})
}

// Get answers a sign-in like a browser does: the JSON of the PublicKeyCredential that
// navigator.credentials.get() returned. It signs the options' challenge with the sign count in
// Counter, which it then increases by one.
func (a *Authenticator) Get(optionsJSON []byte, origin string) ([]byte, error) {
	raw, err := unwrap(optionsJSON)
	if err != nil {
		return nil, err
	}
	var opts requestOptions
	if err := json.Unmarshal(raw, &opts); err != nil {
		return nil, fmt.Errorf("webauthntest: request options: %w", err)
	}
	if opts.Challenge == "" {
		return nil, errors.New("webauthntest: the options have no challenge")
	}

	data := a.authData(a.Counter, false)
	a.Counter++
	clientData, err := clientDataJSON("webauthn.get", opts.Challenge, origin)
	if err != nil {
		return nil, err
	}

	// The signature covers the authenticator data followed by the hash of the client data.
	hash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte(nil), data...), hash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		return nil, fmt.Errorf("webauthntest: sign: %w", err)
	}

	response := map[string]any{
		"clientDataJSON":    b64(clientData),
		"authenticatorData": b64(data),
		"signature":         b64(sig),
	}
	if len(a.UserHandle) > 0 {
		response["userHandle"] = b64(a.UserHandle)
	}
	id := b64(a.credentialID)
	return json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key", "authenticatorAttachment": "platform",
		"response":               response,
		"clientExtensionResults": map[string]any{},
	})
}

// authData is the authenticator data up to the sign count: the RP ID hash, the flags, the counter.
func (a *Authenticator) authData(counter uint32, attested bool) []byte {
	flags := byte(flagUserPresent)
	if a.UserVerified {
		flags |= flagUserVerified
	}
	if a.BackupEligible {
		flags |= flagBackupEligible
	}
	if a.BackupState {
		flags |= flagBackupState
	}
	if attested {
		flags |= flagAttested
	}
	hash := sha256.Sum256([]byte(a.RPID))
	data := append([]byte(nil), hash[:]...)
	data = append(data, flags)
	return binary.BigEndian.AppendUint32(data, counter)
}

// coseKey is the public key as a COSE_Key (RFC 9053): EC2, ES256, curve P-256.
func (a *Authenticator) coseKey() ([]byte, error) {
	point, err := a.key.PublicKey.Bytes() // 0x04, then X and Y, 32 bytes each
	if err != nil {
		return nil, fmt.Errorf("webauthntest: public key: %w", err)
	}
	x, y := point[1:33], point[33:65]
	out, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		return nil, fmt.Errorf("webauthntest: cose key: %w", err)
	}
	return out, nil
}

// clientDataJSON is what the browser signs over for the page: the ceremony's type, the challenge
// the server chose (still in base64url), and the page's origin.
func clientDataJSON(kind, challenge, origin string) ([]byte, error) {
	out, err := json.Marshal(map[string]any{
		"type": kind, "challenge": challenge, "origin": origin, "crossOrigin": false,
	})
	if err != nil {
		return nil, fmt.Errorf("webauthntest: client data: %w", err)
	}
	return out, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
