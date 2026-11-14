package api

import (
	"errors"
	"net/http"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// The cluster's discovery documents (spec 038): Nomad's OpenID configuration and its keys, served on
// the console's own domain so a verifier of workload identity tokens (Bella) can fetch them over
// https. They are public by design (the issuer and the public keys), need no credentials, live
// outside /v1 because the path is fixed by OIDC, and are served on the main listener only.

const (
	codeIssuerNotSet = "issuer_not_set"

	// discoveryCache is how long a verifier may keep a document.
	discoveryCache = "public, max-age=60"
)

// getOpenIDConfiguration answers GET /.well-known/openid-configuration.
func (s *server) getOpenIDConfiguration(w http.ResponseWriter, r *http.Request) error {
	return s.serveDiscovery(w, r, service.DiscoveryConfig)
}

// getJWKS answers GET /.well-known/jwks.json.
func (s *server) getJWKS(w http.ResponseWriter, r *http.Request) error {
	return s.serveDiscovery(w, r, service.DiscoveryJWKS)
}

// serveDiscovery passes one of Nomad's documents on as Nomad sent it. Without an issuer it is a 404;
// a document that can't be read is a 502 whose message is fixed: Nomad's own text is never repeated.
func (s *server) serveDiscovery(w http.ResponseWriter, r *http.Request, document string) error {
	body, err := s.svc.ClusterDiscovery(r.Context(), document)
	switch {
	case errors.Is(err, service.ErrIssuerNotSet):
		return &Error{Status: http.StatusNotFound, Code: codeIssuerNotSet,
			Message: "this installation has no issuer: set a public https address for the console"}
	case err != nil:
		s.log.Warn("discovery: Nomad's document couldn't be read", "document", document, "error", err)
		return &Error{Status: http.StatusBadGateway, Code: codeNomadUnavailable,
			Message: "Nomad's discovery document couldn't be read"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", discoveryCache)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}
