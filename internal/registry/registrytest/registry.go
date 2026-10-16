// Package registrytest is a fake container registry for tests: a real HTTP
// server that answers manifest requests the way Docker Hub and GHCR do,
// including the anonymous token handshake, and can ask for a login (Basic, or
// Bearer with a token service that checks it) the way a private registry
// does. Product code never imports it.
package registrytest

import (
	"crypto/sha256"
	"encoding/base64"
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
	loginHeader  string   // the Authorization value a login must send; "" means no login is needed
	tokenScopes  []string // the scope of every token request, in order
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

// Host is the server's address as an image reference writes a registry host:
// "127.0.0.1:<port>". A loopback host needs no registry.Client.Endpoints
// entry, because the client speaks plain HTTP to it.
func (s *Server) Host() string { return strings.TrimPrefix(s.srv.URL, "http://") }

// Endpoints maps Docker Hub and GHCR to this server, for
// registry.Client.Endpoints.
func (s *Server) Endpoints() map[string]string {
	return map[string]string{"docker.io": s.srv.URL, "ghcr.io": s.srv.URL}
}

// RequireLogin makes the registry ask for this user name and secret (an empty
// user name turns it off). With RequireToken on, the token service wants them
// as Basic auth and refuses a token request without them; otherwise manifest
// requests are challenged with `Basic realm="test"`, like a registry:2 with
// htpasswd.
func (s *Server) RequireLogin(user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loginHeader = ""
	if user != "" {
		s.loginHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
}

// TokenScopes returns the scope of every token request so far, in order; ""
// is a request with no scope.
func (s *Server) TokenScopes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokenScopes...)
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

// add serves m for repo:tag and, as a real registry does, for
// repo@<its digest>.
func (s *Server) add(repo, tag string, m manifest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifests[key{repo, tag}] = m
	s.manifests[key{repo, m.digest}] = m
}

// Delete stops serving repo:ref, a tag or a digest, as if it were deleted
// from the registry.
func (s *Server) Delete(repo, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.manifests, key{repo, ref})
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
	// A registry names its API version on every answer.
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
		s.serveBase(w, r)
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
	failStatus := s.fails[k]
	m, found := s.manifests[k]
	s.mu.Unlock()

	if s.challenge(w, r, k.repo) {
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

// challenge answers a request that isn't logged in with the registry's 401
// and its WWW-Authenticate header, and reports whether it did. repo is the
// repository asked for, "" for the base endpoint.
func (s *Server) challenge(w http.ResponseWriter, r *http.Request, repo string) bool {
	s.mu.Lock()
	needToken, loginHeader := s.requireToken, s.loginHeader
	given := r.Header.Get("Authorization")
	tokenOK := s.tokens[strings.TrimPrefix(given, "Bearer ")]
	s.mu.Unlock()

	switch {
	case needToken && !tokenOK:
		scope := ""
		if repo != "" {
			scope = fmt.Sprintf(`,scope="repository:%s:pull"`, repo)
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test"%s`, s.srv.URL, scope))
	case !needToken && loginHeader != "" && given != loginHeader:
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
	default:
		return false
	}
	writeRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	return true
}

// serveBase answers GET /v2/, the API's base endpoint: 200 once logged in.
func (s *Server) serveBase(w http.ResponseWriter, r *http.Request) {
	if s.challenge(w, r, "") {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := r.URL.Query().Get("scope")
	s.tokenScopes = append(s.tokenScopes, scope)
	if s.failToken != 0 {
		writeRegistryError(w, s.failToken, "DENIED", "token refused")
		return
	}
	if r.URL.Query().Get("service") != "test" || (scope != "" && !strings.HasPrefix(scope, "repository:")) {
		writeRegistryError(w, http.StatusBadRequest, "BAD_REQUEST", "missing service or scope")
		return
	}
	if s.loginHeader != "" && r.Header.Get("Authorization") != s.loginHeader {
		writeRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "incorrect username or password")
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
