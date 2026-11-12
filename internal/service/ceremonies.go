package service

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkey ceremonies (feature 033, research R7). A ceremony is one begin-and-finish
// exchange with a browser: the server makes a challenge, the browser has the
// authenticator sign it, and the server checks the answer. What the server must
// remember between the two calls is kept here, in memory: a restart only drops
// ceremonies under way, and nobody has anything to lose by starting again.

type ceremonyKind string

const (
	ceremonySignIn   ceremonyKind = "signin"   // discoverable sign-in on /login
	ceremonyConfirm  ceremonyKind = "confirm"  // "Confirm it's you", for a signed-in session
	ceremonyRegister ceremonyKind = "register" // adding a passkey, for a confirmed session
)

const (
	ceremonyLife       = 5 * time.Minute // a ceremony is good for this long
	ceremonyPerAddress = 8               // pending ceremonies one address may hold
	ceremonyTotal      = 512             // and all addresses together; the oldest go first
	ceremonyIDBytes    = 16              // 128 bits of randomness
)

// RelyingParty is the web address passkeys belong to: the domain a browser
// addressed and the origin it signs for, for example "console.example.com" and
// "https://console.example.com".
type RelyingParty struct{ ID, Origin string }

// ceremony is what a begin leaves for its finish.
type ceremony struct {
	kind    ceremonyKind
	session webauthn.SessionData
	rp      RelyingParty
	address string // the client address that asked: the per-address cap counts these
	// sessionHash is the hash of the session cookie that started a confirm or
	// register ceremony; it can't be finished by another session. Empty for a sign-in.
	sessionHash string
	expires     time.Time
	seq         uint64 // order of creation, for dropping the oldest
}

// ceremonies is the table of pending ceremonies. It is safe for concurrent use.
type ceremonies struct {
	mu   sync.Mutex
	byID map[string]*ceremony
	seq  uint64
}

// add stores c and returns the ceremony ID, a random 128-bit value the browser
// sends back. Expired ceremonies are dropped first; then the address's oldest
// goes when it already holds 8, and the oldest of all when 512 are pending.
func (t *ceremonies) add(now time.Time, c ceremony) (string, error) {
	raw := make([]byte, ceremonyIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate ceremony ID: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(raw)

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byID == nil {
		t.byID = map[string]*ceremony{}
	}
	for key, old := range t.byID {
		if !now.Before(old.expires) {
			delete(t.byID, key)
		}
	}
	// Making room for the new one: with 8 held already, the address's oldest is
	// dropped (so the cap after the add is 8), and likewise for the total.
	sameAddress := func(old *ceremony) bool { return old.address == c.address }
	for t.count(sameAddress) >= ceremonyPerAddress {
		t.dropOldest(sameAddress)
	}
	anyAddress := func(*ceremony) bool { return true }
	for t.count(anyAddress) >= ceremonyTotal {
		t.dropOldest(anyAddress)
	}
	t.seq++
	c.seq = t.seq
	c.expires = now.Add(ceremonyLife)
	t.byID[id] = &c
	return id, nil
}

// take removes the ceremony and returns it, whatever its state: a ceremony is
// single use, so even a finish that fails has used it up. ok is false for an ID
// that is unknown, was used, or has expired.
func (t *ceremonies) take(now time.Time, id string) (ceremony, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, found := t.byID[id]
	if !found {
		return ceremony{}, false
	}
	delete(t.byID, id)
	if !now.Before(c.expires) {
		return ceremony{}, false
	}
	return *c, true
}

// pending counts the ceremonies held, for tests.
func (t *ceremonies) pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byID)
}

// count counts the ceremonies that match. The caller holds t.mu.
func (t *ceremonies) count(match func(*ceremony) bool) int {
	n := 0
	for _, c := range t.byID {
		if match(c) {
			n++
		}
	}
	return n
}

// dropOldest removes the oldest ceremony that matches. The caller holds t.mu.
func (t *ceremonies) dropOldest(match func(*ceremony) bool) {
	var oldestID string
	var oldest uint64
	for id, c := range t.byID {
		if match(c) && (oldestID == "" || c.seq < oldest) {
			oldestID, oldest = id, c.seq
		}
	}
	delete(t.byID, oldestID)
}
