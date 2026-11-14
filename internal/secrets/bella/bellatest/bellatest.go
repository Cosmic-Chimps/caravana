// Package bellatest is a fake Bella Baxter for tests and the lab (feature 037).
// It speaks what Bella's Go SDK speaks: the HMAC signature on every request and
// the end-to-end encrypted answer of a zero-knowledge environment (ECDH P-256,
// HKDF-SHA256, AES-256-GCM), so the adapter is tested against the same wire
// format as the real thing. Product code never imports it.
package bellatest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Server is a fake Bella serving one environment of one project. Make it with
// New and stop it with Close.
type Server struct {
	srv *httptest.Server

	keyID         string
	signingSecret []byte
	apiKey        string
	devicePub     []byte // the registered device key: a raw 65-byte P-256 point
	privatePEM    string

	mu       sync.Mutex
	secrets  map[string]string
	version  int64
	down     bool
	status   int // when not 0, every read answers it
	plain    bool
	tamper   bool
	delay    time.Duration
	project  string
	env      string
	reads    int
	requests int
}

// New starts a fake with the project "shop", the environment "production" and
// no secrets, and a fresh API key and device key pair.
func New() *Server {
	keyID := randomHex(16)
	secret := randomHex(32)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err) // crypto/rand failing: nothing to recover
	}
	ecdhPriv, err := priv.ECDH()
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		panic(err)
	}
	s := &Server{
		keyID:         keyID,
		signingSecret: mustDecodeHex(secret),
		apiKey:        "bax-" + keyID + "-" + secret,
		devicePub:     ecdhPriv.PublicKey().Bytes(),
		privatePEM:    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		secrets:       map[string]string{},
		version:       1,
		project:       "shop",
		env:           "production",
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// URL is the server's address (http://127.0.0.1:port, a loopback address).
func (s *Server) URL() string { return s.srv.URL }

// APIKey is the API key the server accepts.
func (s *Server) APIKey() string { return s.apiKey }

// PrivateKeyPEM is the device private key (PKCS#8 PEM) registered for the API
// key, as Bella shows it when it makes the key.
func (s *Server) PrivateKeyPEM() string { return s.privatePEM }

// Project and Environment are the slugs the server serves; any other is 404.
func (s *Server) Project() string     { return s.project }
func (s *Server) Environment() string { return s.env }

// GeneratePrivateKeyPEM makes a PKCS#8 P-256 key the server doesn't know, for
// the wrong-device-key case.
func GeneratePrivateKeyPEM() string {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// Set makes the environment have name with value, and moves its version.
func (s *Server) Set(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[name] = value
	s.version++
}

// Delete removes name.
func (s *Server) Delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets, name)
	s.version++
}

// SetDown makes every read answer 503 (true) or work again (false).
func (s *Server) SetDown(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = v
}

// SetStatus makes every read answer that status with a problem document (401,
// 403, 404, 500, ...); 0 turns it off.
func (s *Server) SetStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
}

// SetPlaintext makes the server answer a read with plain JSON instead of an
// envelope, as a stripped header or a regression would.
func (s *Server) SetPlaintext(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plain = v
}

// SetTamper makes the server flip a bit of the ciphertext, so the GCM tag
// fails.
func (s *Server) SetTamper(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tamper = v
}

// SetDelay makes the server wait before it answers a read.
func (s *Server) SetDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

// Reads counts the reads the server answered with the secrets (an envelope,
// or plain JSON under SetPlaintext). A refused or failed read isn't counted.
func (s *Server) Reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// Requests counts every request that reached the server, answered or not.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	down, status, plain, tamper, delay := s.down, s.status, s.plain, s.tamper, s.delay
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if down {
		problem(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if !s.signed(r) {
		problem(w, http.StatusUnauthorized, "invalid-signature")
		return
	}
	want := fmt.Sprintf("/api/v1/projects/%s/environments/%s/secrets", s.project, s.env)
	if r.Method != http.MethodGet || r.URL.Path != want {
		problem(w, http.StatusNotFound, "not-found")
		return
	}
	if status != 0 {
		problem(w, status, "forced")
		return
	}
	clientPub, ok := rawPublicKey(r.Header.Get("X-E2E-Public-Key"))
	if !ok || !bytes.Equal(clientPub, s.devicePub) {
		problem(w, http.StatusForbidden, "zke-required")
		return
	}

	s.mu.Lock()
	body, err := json.Marshal(map[string]any{
		"environmentSlug": s.env,
		"secrets":         s.secrets,
		"version":         s.version,
	})
	s.mu.Unlock()
	if err != nil {
		problem(w, http.StatusInternalServerError, "encode")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if plain {
		_, _ = w.Write(body)
	} else {
		env, err := seal(clientPub, body, tamper)
		if err != nil {
			problem(w, http.StatusInternalServerError, "encrypt")
			return
		}
		_, _ = w.Write(env)
	}
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
}

// signed checks the HMAC the SDK puts on every request:
// METHOD \n path \n sorted query \n timestamp \n sha256 hex of the body.
func (s *Server) signed(r *http.Request) bool {
	if r.Header.Get("X-Bella-Key-Id") != s.keyID {
		return false
	}
	ts := r.Header.Get("X-Bella-Timestamp")
	t, err := time.Parse("2006-01-02T15:04:05Z", ts)
	if err != nil || time.Since(t) > 5*time.Minute || time.Until(t) > 5*time.Minute {
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	var parts []string
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	sort.Strings(parts)
	sum := sha256.Sum256(body)
	toSign := r.Method + "\n" + r.URL.Path + "\n" + strings.Join(parts, "&") + "\n" + ts + "\n" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, s.signingSecret)
	mac.Write([]byte(toSign))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(r.Header.Get("X-Bella-Signature")))
}

// rawPublicKey returns the 65-byte point at the end of a base64 SPKI.
func rawPublicKey(b64 string) ([]byte, bool) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(der) < 65 {
		return nil, false
	}
	return der[len(der)-65:], true
}

// seal encrypts plaintext to the client's public key the way Bella does: an
// ephemeral server key, ECDH, HKDF-SHA256 (32 zero bytes of salt, info
// "bella-e2ee-v1"), AES-256-GCM with the tag sent apart.
func seal(clientPub, plaintext []byte, tamper bool) ([]byte, error) {
	pub, err := ecdh.P256().NewPublicKey(clientPub)
	if err != nil {
		return nil, err
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, make([]byte, 32), []byte("bella-e2ee-v1")), key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	ct, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	if tamper && len(ct) > 0 {
		ct[0] ^= 0x01
	}
	spki, err := x509.MarshalPKIXPublicKey(eph.PublicKey())
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"encrypted":       true,
		"algorithm":       "ECDH-P256-HKDF-SHA256-AES256GCM",
		"serverPublicKey": base64.StdEncoding.EncodeToString(spki),
		"nonce":           base64.StdEncoding.EncodeToString(nonce),
		"tag":             base64.StdEncoding.EncodeToString(tag),
		"ciphertext":      base64.StdEncoding.EncodeToString(ct),
	})
}

func problem(w http.ResponseWriter, status int, kind string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "https://bella.test/problems/" + kind, "title": kind, "status": status,
	})
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
