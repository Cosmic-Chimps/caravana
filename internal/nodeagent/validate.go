package nodeagent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// maxAnswer is the most of the console's answer that is read (contracts/node.md §5).
const maxAnswer = 256 << 10

// FieldError is the refusal of an answer: it names the field and says what was wrong in fixed words,
// and never quotes a key, a token or a certificate.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("the console's answer is refused: %s: %s", e.Field, e.Reason)
}

func refuse(field, reason string) error { return &FieldError{Field: field, Reason: reason} }

// Check is one of a joining server's checks, as the console reports it.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Answer is what the console answers to a join, a sync or a certificate call. Fields a call doesn't
// answer stay empty.
type Answer struct {
	Name        string      `json:"name"`
	Index       int         `json:"index"`
	Pool        string      `json:"pool"`
	Role        string      `json:"role"`
	Servers     []int       `json:"servers"`
	Peers       []mesh.Peer `json:"peers"`
	CA          string      `json:"ca"`
	Certificate string      `json:"certificate"`
	NodeToken   string      `json:"nodeToken"`
	GossipKey   string      `json:"gossipKey"`
	Renew       bool        `json:"renew"`
	State       string      `json:"state"`
	Checks      []Check     `json:"checks"`
}

// Expect is what an answer is checked against.
type Expect struct {
	// State is what the agent last applied; nil on a join.
	State *State
	// OwnPublicKey is this server's WireGuard public key: no peer may have it.
	OwnPublicKey string
	// CA is ca.pem as it is on disk (sync and certificate answers). A join has none: its answer brings it.
	CA []byte
	// Key is the private key (PEM) the certificate must belong to (join and certificate answers).
	Key []byte
	// Kind is the kind of certificate a certificate answer must have.
	Kind clusterca.Kind
	// Now is the time certificates are checked at.
	Now time.Time
}

var nodeTokenRE = regexp.MustCompile(`^cvnd_[A-Za-z0-9_-]{43}$`)

// ParseJoin validates the answer to POST /v1/join: everything of a sync answer, plus the CA, a
// certificate for the key the agent just made, and the node token.
func ParseJoin(body []byte, ex Expect) (*Answer, error) {
	ans, err := decode(body)
	if err != nil {
		return nil, err
	}
	ex.State = nil
	if err := validateCommon(ans, ex, ans.Index); err != nil {
		return nil, err
	}
	if err := clusterca.CheckCA([]byte(ans.CA), ex.Now); err != nil {
		return nil, refuse("ca", caReason)
	}
	if err := checkCert(ans.Certificate, []byte(ans.CA), ex.Key, kindOf(ans.Role), ex.Now); err != nil {
		return nil, err
	}
	if !nodeTokenRE.MatchString(ans.NodeToken) {
		return nil, refuse("nodeToken", "must be cvnd_ and 43 characters of A-Z a-z 0-9 _ -")
	}
	return ans, nil
}

// ParseSync validates the answer to POST /v1/node/sync. Its index and name must be the ones in
// state.json, and a server doesn't go back to client.
func ParseSync(body []byte, ex Expect) (*Answer, error) {
	if ex.State == nil {
		return nil, errors.New("a sync answer is checked against state.json")
	}
	ans, err := decode(body)
	if err != nil {
		return nil, err
	}
	if ans.Index != ex.State.Index {
		return nil, refuse("index", "isn't this server's")
	}
	if ans.Name != ex.State.Name {
		return nil, refuse("name", "isn't this server's")
	}
	if err := validateCommon(ans, ex, ex.State.Index); err != nil {
		return nil, err
	}
	if ans.CA != "" && !clusterca.Equal([]byte(ans.CA), ex.CA) {
		return nil, refuse("ca", "isn't the cluster's CA this server has")
	}
	return ans, nil
}

// ParseCertificate validates the answer to POST /v1/node/certificate: a certificate of the kind
// asked for, signed by the CA this server has, for the key it just made.
func ParseCertificate(body []byte, ex Expect) (*Answer, error) {
	ans, err := decode(body)
	if err != nil {
		return nil, err
	}
	if ans.CA != "" && !clusterca.Equal([]byte(ans.CA), ex.CA) {
		return nil, refuse("ca", "isn't the cluster's CA this server has")
	}
	if err := checkCert(ans.Certificate, ex.CA, ex.Key, ex.Kind, ex.Now); err != nil {
		return nil, err
	}
	return ans, nil
}

func kindOf(role string) clusterca.Kind {
	if role == RoleServer {
		return clusterca.Server
	}
	return clusterca.Client
}

func checkCert(cert string, ca, key []byte, kind clusterca.Kind, now time.Time) error {
	if cert == "" {
		return refuse("certificate", "is missing")
	}
	if err := clusterca.Check(ca, []byte(cert), key, kind, now); err != nil {
		return refuse("certificate", "isn't a valid certificate of this cluster for this key and role")
	}
	return nil
}

const caReason = "must be one PEM certificate that is a CA and valid now"

// decode reads the JSON. A decoding error names the field when Go knows it and says nothing of the
// value.
func decode(body []byte) (*Answer, error) {
	if len(body) > maxAnswer {
		return nil, refuse("body", "is larger than 256 KiB")
	}
	var ans Answer
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&ans); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return nil, refuse(te.Field, "has the wrong type")
		}
		return nil, refuse("body", "isn't JSON of the expected shape")
	}
	return &ans, nil
}

// validateCommon applies the rules of §5 the three answers share. own is this server's index.
func validateCommon(ans *Answer, ex Expect, own int) error {
	if ans.Index < 1 || ans.Index > mesh.MaxIndex {
		return refuse("index", fmt.Sprintf("must be 1 to %d", mesh.MaxIndex))
	}
	if !ValidName(ans.Name) {
		return refuse("name", "isn't a valid name")
	}
	if !ValidName(ans.Pool) {
		return refuse("pool", "isn't a valid name")
	}
	if ans.Role != RoleClient && ans.Role != RoleServer {
		return refuse("role", "must be client or server")
	}
	if ex.State != nil && ex.State.Role == RoleServer && ans.Role == RoleClient {
		return refuse("role", "a server doesn't go back to client")
	}
	seen := map[int]bool{} // the peers' indexes
	for i, p := range ans.Peers {
		field := fmt.Sprintf("peers[%d]", i)
		switch {
		case p.Index < 1 || p.Index > mesh.MaxIndex:
			return refuse(field+".index", fmt.Sprintf("must be 1 to %d", mesh.MaxIndex))
		case p.Index == own:
			return refuse(field+".index", "is this server's own")
		case seen[p.Index]:
			return refuse(field+".index", "is listed twice")
		case !mesh.ValidPublicKey(p.PublicKey):
			return refuse(field+".publicKey", "isn't a WireGuard public key")
		case ex.OwnPublicKey != "" && p.PublicKey == ex.OwnPublicKey:
			return refuse(field+".publicKey", "is this server's own")
		}
		if _, err := mesh.ParseEndpoint(p.Endpoint); err != nil {
			return refuse(field+".endpoint", "isn't an IP address and a port another server can be reached at")
		}
		seen[p.Index] = true
	}
	if len(ans.Servers) == 0 {
		return refuse("servers", "is empty")
	}
	for _, s := range ans.Servers {
		if s != own && !seen[s] {
			return refuse("servers", "names a server that is neither this one nor a peer")
		}
	}
	if ans.Role == RoleServer {
		raw, err := base64.StdEncoding.DecodeString(ans.GossipKey)
		if err != nil || (len(raw) != 16 && len(raw) != 24 && len(raw) != 32) {
			return refuse("gossipKey", "must be base64 of 16, 24 or 32 bytes on a Nomad server")
		}
	}
	return nil
}
