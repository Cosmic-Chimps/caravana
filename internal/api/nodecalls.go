package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// The node calls (feature 031, contracts/api.md §4): what a server's agent says to Caravana. They
// have credentials of their own and so are public routes of the table, outside authenticate:
//
//   - POST /v1/join carries the join token in its body; failed tries are limited per address;
//   - POST /v1/node/sync and POST /v1/node/certificate carry the server's node token as
//     Authorization: Bearer cvnd_…, and nothing else is accepted on them (an API token, a session
//     or a deploy token is the same 401 as a wrong token).
//
// A node token is accepted nowhere else: authenticate looks tokens up in the API tokens' table, where
// no node token is. They are served on the main listener only, never on the webhook-only one. Every
// answer is no-store, a body is at most 64 KiB, and nothing here logs a body or a header (the request
// log reads neither).

// maxNodeBody is the largest body of a node call.
const maxNodeBody = 64 << 10

// decodeNodeJSON reads the JSON body of a node call: at most 64 KiB (413 body_too_large), then the
// same rules as every other body.
func decodeNodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNodeBody))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return &Error{Status: http.StatusRequestEntityTooLarge, Code: codeBodyTooLarge,
			Message: fmt.Sprintf("request body is larger than %d bytes", maxNodeBody)}
	case err != nil:
		return &Error{Status: http.StatusBadRequest, Code: codeBadRequest, Message: "could not read the request body"}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return decodeJSON(r, v)
}

// nodeToken reads the node token: exactly one Authorization header, a Bearer one. A missing, doubled
// or other credential is the one 401.
func nodeToken(r *http.Request) (string, error) {
	if len(r.Header.Values("Authorization")) != 1 {
		return "", service.ErrNodeToken
	}
	return bearerToken(r), nil
}

// peerJSON is a peer in an answer: the agent reads index, publicKey and endpoint; the name is for
// people.
type peerJSON struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
	Endpoint  string `json:"endpoint"`
}

func toPeersJSON(ps []service.NodePeer) []peerJSON {
	out := make([]peerJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, peerJSON{Index: p.Index, Name: p.Name, PublicKey: p.PublicKey, Endpoint: p.Endpoint})
	}
	return out
}

func serversOrEmpty(s []int) []int {
	if s == nil {
		return []int{}
	}
	return s
}

// joinRequestJSON is the body of POST /v1/join.
type joinRequestJSON struct {
	Token     string            `json:"token"`
	PublicKey string            `json:"publicKey"`
	CSR       string            `json:"csr"`
	Address   string            `json:"address"`
	Facts     service.NodeFacts `json:"facts"`
}

// joinServer answers POST /v1/join.
func (s *server) joinServer(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	var in joinRequestJSON
	if err := decodeNodeJSON(w, r, &in); err != nil {
		return err
	}
	address := s.proxies.ClientAddress(r)
	try, err := s.auth.BeginJoin(r.Context(), address)
	if err != nil {
		if errors.Is(err, service.ErrSigninLimited) {
			return &Error{Status: http.StatusTooManyRequests, Code: codeTooManyAttempts,
				Message: "Too many failed tries from this address. Wait a few minutes and make a new command in the console."}
		}
		return err
	}
	defer try.Release()

	ans, err := s.svc.Join(r.Context(), service.JoinRequest{Token: in.Token, PublicKey: in.PublicKey, CSR: in.CSR,
		Address: in.Address, Facts: in.Facts}, remoteIP(address))
	switch {
	case errors.Is(err, service.ErrJoinToken):
		// The only failure that counts: someone tried a token that isn't one.
		_ = try.Failed(r.Context())
		return err
	case err != nil:
		return unprocessable(err)
	}
	_ = try.Succeeded(r.Context())
	writeJSON(w, http.StatusOK, struct {
		Name        string     `json:"name"`
		Index       int        `json:"index"`
		Pool        string     `json:"pool"`
		Role        string     `json:"role"`
		Servers     []int      `json:"servers"`
		CA          string     `json:"ca"`
		Certificate string     `json:"certificate"`
		NodeToken   string     `json:"nodeToken"`
		Peers       []peerJSON `json:"peers"`
	}{ans.Name, ans.Index, ans.Pool, ans.Role, serversOrEmpty(ans.Servers), ans.CA, ans.Certificate, ans.NodeToken, toPeersJSON(ans.Peers)})
	return nil
}

// remoteIP is the address of a join call as a literal, or "" when the limit's key is an IPv6 /64
// prefix: an endpoint needs the address itself, so the server must say it (--address).
func remoteIP(key string) string {
	if key == "unknown" || strings.Contains(key, "/") {
		return ""
	}
	return key
}

// syncNode answers POST /v1/node/sync.
func (s *server) syncNode(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	token, err := nodeToken(r)
	if err != nil {
		return err
	}
	var in service.NodeReport
	if err := decodeNodeJSON(w, r, &in); err != nil {
		return err
	}
	ans, err := s.svc.NodeSync(r.Context(), token, in)
	if err != nil {
		return unprocessable(err)
	}
	type checkOut = serverCheckJSON
	out := struct {
		Name      string     `json:"name"`
		Index     int        `json:"index"`
		Pool      string     `json:"pool"`
		Role      string     `json:"role"`
		Servers   []int      `json:"servers"`
		Peers     []peerJSON `json:"peers"`
		GossipKey string     `json:"gossipKey,omitempty"`
		Renew     bool       `json:"renew"`
		State     string     `json:"state"`
		Checks    []checkOut `json:"checks"`
	}{ans.Name, ans.Index, ans.Pool, ans.Role, serversOrEmpty(ans.Servers), toPeersJSON(ans.Peers), ans.GossipKey, ans.Renew, ans.State,
		toServerChecksJSON(ans.Checks)}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// renewNodeCertificate answers POST /v1/node/certificate.
func (s *server) renewNodeCertificate(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	token, err := nodeToken(r)
	if err != nil {
		return err
	}
	var in struct {
		CSR string `json:"csr"`
	}
	if err := decodeNodeJSON(w, r, &in); err != nil {
		return err
	}
	cert, ca, err := s.svc.NodeCertificate(r.Context(), token, in.CSR)
	if err != nil {
		return unprocessable(err)
	}
	writeJSON(w, http.StatusOK, struct {
		Certificate string `json:"certificate"`
		CA          string `json:"ca"`
	}{cert, ca})
	return nil
}
