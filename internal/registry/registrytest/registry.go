// Package registrytest is a fake container registry for tests: a real HTTP
// server that answers manifest requests the way Docker Hub and GHCR do,
// including the anonymous token handshake. Product code never imports it.
package registrytest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Media types the fake serves.
const (
	MediaTypeIndex    = "application/vnd.oci.image.index.v1+json"
	MediaTypeManifest = "application/vnd.oci.image.manifest.v1+json"
)

type key struct{ repo, ref string }

type manifest struct {
	mediaType string
	digest    string
	body      []byte
	noHeader  bool // leave out Docker-Content-Digest
}

// Server is a fake registry. Its zero value isn't usable; call New.
type Server struct {
	srv *httptest.Server

	mu           sync.Mutex
	manifests    map[key]manifest
	fails        map[key]int
	holds        map[key]chan struct{}
	requireToken bool
	failToken    int
	tokens       map[string]bool
	requests     int
}

// New starts a fake registry that closes when the test ends.
func New(t testing.TB) *Server {
	s := &Server{
		manifests: map[key]manifest{},
		fails:     map[key]int{},
		holds:     map[key]chan struct{}{},
		tokens:    map[string]bool{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	// Cleanups run last-registered first: release held requests, then close.
	t.Cleanup(s.srv.Close)
	t.Cleanup(s.releaseAll)
	return s
}

// URL is the server's base URL.
func (s *Server) URL() string { return s.srv.URL }

// Endpoints maps both supported registries to this server, for
// registry.Client.Endpoints.
func (s *Server) Endpoints() map[string]string {
	return map[string]string{"docker.io": s.srv.URL, "ghcr.io": s.srv.URL}
}

// AddIndex serves a multi-architecture index for repo:tag with this digest
// ("sha256:<64 hex>").
func (s *Server) AddIndex(repo, tag, digest string) {
	s.add(repo, tag, manifest{mediaType: MediaTypeIndex, digest: digest, body: []byte(`{"schemaVersion":2}`)})
}

// AddManifest serves a single-architecture manifest for repo:tag.
func (s *Server) AddManifest(repo, tag, digest string) {
	s.add(repo, tag, manifest{mediaType: MediaTypeManifest, digest: digest, body: []byte(`{"schemaVersion":2}`)})
}

// AddWithoutDigestHeader serves a manifest for repo:tag without the
// Docker-Content-Digest header, as some registries do, and returns the
// digest of its body.
func (s *Server) AddWithoutDigestHeader(repo, tag string) string {
	body := []byte(fmt.Sprintf(`{"schemaVersion":2,"tag":%q}`, tag))
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	s.add(repo, tag, manifest{mediaType: MediaTypeManifest, digest: digest, body: body, noHeader: true})
	return digest
}

// AddBadDigestHeader serves repo:tag with a Docker-Content-Digest header that
// isn't a sha256 digest.
func (s *Server) AddBadDigestHeader(repo, tag string) {
	s.add(repo, tag, manifest{mediaType: MediaTypeManifest, digest: "md5:abc", body: []byte(`{}`)})
}

func (s *Server) add(repo, tag string, m manifest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifests[key{repo, tag}] = m
}

// Fail makes every manifest request for repo:tag answer status.
func (s *Server) Fail(repo, tag string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[key{repo, tag}] = status
}

// RequireToken makes manifest requests need a bearer token from the token
// endpoint, announced with a WWW-Authenticate challenge, like Docker Hub.
func (s *Server) RequireToken(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requireToken = on
}

// FailToken makes the token endpoint answer status.
func (s *Server) FailToken(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failToken = status
}

// Hold blocks manifest requests for repo:tag until release is called.
func (s *Server) Hold(repo, tag string) (release func()) {
	ch := make(chan struct{})
	s.mu.Lock()
	s.holds[key{repo, tag}] = ch
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		// Only the first release (or the test's cleanup) closes the channel.
		if s.holds[key{repo, tag}] == ch {
			delete(s.holds, key{repo, tag})
			close(ch)
		}
	}
}

// Requests counts manifest requests (HEAD and GET) so far.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *Server) releaseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, ch := range s.holds {
		close(ch)
		delete(s.holds, k)
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		s.serveToken(w, r)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/")
	i := strings.LastIndex(rest, "/manifests/")
	if !ok || i < 0 || (r.Method != http.MethodHead && r.Method != http.MethodGet) {
		http.NotFound(w, r)
		return
	}
	k := key{repo: rest[:i], ref: rest[i+len("/manifests/"):]}

	s.mu.Lock()
	s.requests++
	hold := s.holds[k]
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}

	s.mu.Lock()
	needToken := s.requireToken
	failStatus := s.fails[k]
	m, found := s.manifests[k]
	authorized := s.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	s.mu.Unlock()

	if needToken && !authorized {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(
			`Bearer realm="%s/token",service="test",scope="repository:%s:pull"`, s.srv.URL, k.repo))
		writeRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	if failStatus != 0 {
		writeRegistryError(w, failStatus, "FAILED", "scripted failure")
		return
	}
	if !found || !strings.Contains(r.Header.Get("Accept"), m.mediaType) {
		writeRegistryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}

	w.Header().Set("Content-Type", m.mediaType)
	if !m.noHeader {
		w.Header().Set("Docker-Content-Digest", m.digest)
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(m.body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(m.body)
	}
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failToken != 0 {
		writeRegistryError(w, s.failToken, "DENIED", "token refused")
		return
	}
	if r.URL.Query().Get("service") != "test" || !strings.HasPrefix(r.URL.Query().Get("scope"), "repository:") {
		writeRegistryError(w, http.StatusBadRequest, "BAD_REQUEST", "missing service or scope")
		return
	}
	token := fmt.Sprintf("test-token-%d", len(s.tokens)+1)
	s.tokens[token] = true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
}

func writeRegistryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"code": code, "message": message}},
	})
}
