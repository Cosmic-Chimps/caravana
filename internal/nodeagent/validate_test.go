package nodeagent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
)

const ownKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func syncState() *State {
	return &State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleClient, Servers: []int{1}}
}

func syncAnswer() map[string]any {
	return map[string]any{
		"name": "fsn-2", "index": 3, "pool": "default", "role": "client", "servers": []int{1},
		"peers": []any{peerJSON(1, peerKey1, "203.0.113.4:51820"), peerJSON(2, peerKey2, "198.51.100.7:51820")},
		"renew": false, "state": "ready", "extra": "unknown fields are ignored",
	}
}

func asJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Each rule of contracts/node.md §5 is a case: the answer is refused, the error names the field, and
// it never holds a key.
func TestParseSyncRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m map[string]any)
		state  *State
		field  string // "" means the answer is accepted
	}{
		{"valid", func(m map[string]any) {}, nil, ""},
		{"index 0", func(m map[string]any) { m["index"] = 0 }, nil, "index"},
		{"index 64", func(m map[string]any) { m["index"] = 64 }, nil, "index"},
		{"index not this server's", func(m map[string]any) { m["index"] = 4 }, nil, "index"},
		{"name not this server's", func(m map[string]any) { m["name"] = "other" }, nil, "name"},
		{"invalid name", func(m map[string]any) { m["name"] = "Bad Name" }, &State{Version: 1, Index: 3, Name: "Bad Name", Pool: "default", Role: RoleClient}, "name"},
		{"invalid pool", func(m map[string]any) { m["pool"] = "No Good" }, nil, "pool"},
		{"role", func(m map[string]any) { m["role"] = "boss" }, nil, "role"},
		{"a server goes back to client", func(m map[string]any) {}, &State{Version: 1, Index: 3, Name: "fsn-2", Pool: "default", Role: RoleServer, Servers: []int{1, 3}}, "role"},
		{"peer index 0", func(m map[string]any) { m["peers"] = []any{peerJSON(0, peerKey1, "203.0.113.4:51820")} }, nil, "peers[0].index"},
		{"peer index 64", func(m map[string]any) { m["peers"] = []any{peerJSON(64, peerKey1, "203.0.113.4:51820")} }, nil, "peers[0].index"},
		{"peer is itself", func(m map[string]any) { m["peers"] = []any{peerJSON(3, peerKey1, "203.0.113.4:51820")} }, nil, "peers[0].index"},
		{"peer twice", func(m map[string]any) {
			m["peers"] = []any{peerJSON(1, peerKey1, "203.0.113.4:51820"), peerJSON(1, peerKey2, "203.0.113.5:51820")}
		}, nil, "peers[1].index"},
		{"peer key invalid", func(m map[string]any) { m["peers"] = []any{peerJSON(1, "not-a-key", "203.0.113.4:51820")} }, nil, "peers[0].publicKey"},
		{"peer key is its own", func(m map[string]any) { m["peers"] = []any{peerJSON(1, ownKey, "203.0.113.4:51820")} }, nil, "peers[0].publicKey"},
		{"endpoint a name", func(m map[string]any) { m["peers"] = []any{peerJSON(1, peerKey1, "fsn-1.example.com:51820")} }, nil, "peers[0].endpoint"},
		{"endpoint loopback", func(m map[string]any) { m["peers"] = []any{peerJSON(1, peerKey1, "127.0.0.1:51820")} }, nil, "peers[0].endpoint"},
		{"endpoint no port", func(m map[string]any) { m["peers"] = []any{peerJSON(1, peerKey1, "203.0.113.4")} }, nil, "peers[0].endpoint"},
		{"no servers", func(m map[string]any) { m["servers"] = []int{} }, nil, "servers"},
		{"a server nobody knows", func(m map[string]any) { m["servers"] = []int{1, 9} }, nil, "servers"},
		{"servers with itself and a peer", func(m map[string]any) { m["servers"] = []int{1, 2, 3} }, nil, ""},
		{"server role needs a gossip key", func(m map[string]any) { m["role"] = "server" }, nil, "gossipKey"},
		{"gossip key of 10 bytes", func(m map[string]any) { m["role"] = "server"; m["gossipKey"] = "MTIzNDU2Nzg5MA==" }, nil, "gossipKey"},
		{"gossip key not base64", func(m map[string]any) { m["role"] = "server"; m["gossipKey"] = "!!!" }, nil, "gossipKey"},
		{"gossip key of 16 bytes", func(m map[string]any) { m["role"] = "server"; m["gossipKey"] = "MTIzNDU2Nzg5MDEyMzQ1Ng==" }, nil, ""},
		{"gossip key of 32 bytes", func(m map[string]any) { m["role"] = "server"; m["gossipKey"] = sentinelGossip }, nil, ""},
		{"index is a string", func(m map[string]any) { m["index"] = "3" }, nil, "index"},
		{"peers is a string", func(m map[string]any) { m["peers"] = "x" }, nil, "peers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := syncAnswer()
			tt.mutate(m)
			st := tt.state
			if st == nil {
				st = syncState()
			}
			_, err := ParseSync(asJSON(t, m), Expect{State: st, OwnPublicKey: ownKey, Now: t0})
			if tt.field == "" {
				if err != nil {
					t.Fatalf("refused a valid answer: %v", err)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("err = %v, want a FieldError for %s", err, tt.field)
			}
			if fe.Field != tt.field {
				t.Errorf("field = %q, want %q (%v)", fe.Field, tt.field, err)
			}
			for _, secret := range []string{peerKey1, peerKey2, ownKey, sentinelGossip} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error quotes a key: %v", err)
				}
			}
		})
	}
}

func TestParseSyncBody(t *testing.T) {
	big := append([]byte(`{"name":"`), make([]byte, 300<<10)...)
	for _, body := range [][]byte{[]byte("not json"), []byte(`[]`), big} {
		if _, err := ParseSync(body, Expect{State: syncState(), Now: t0}); err == nil {
			t.Errorf("accepted %.20q", body)
		}
	}
	if _, err := ParseSync([]byte(`{}`), Expect{Now: t0}); err == nil {
		t.Error("a sync answer was checked without a state")
	}
}

func TestParseSyncCA(t *testing.T) {
	c := newConsole(t)
	other, _, _ := clusterca.New(t0)
	m := syncAnswer()
	m["ca"] = string(other)
	if _, err := ParseSync(asJSON(t, m), Expect{State: syncState(), CA: c.caCert, Now: t0}); err == nil {
		t.Error("a different CA in a sync answer was accepted")
	}
	m["ca"] = string(c.caCert)
	if _, err := ParseSync(asJSON(t, m), Expect{State: syncState(), CA: c.caCert, Now: t0}); err != nil {
		t.Errorf("the same CA was refused: %v", err)
	}
}

func TestParseJoinRules(t *testing.T) {
	c := newConsole(t)
	key, csr, _ := clusterca.NewKeyAndCSR()
	otherCA, otherKey, _ := clusterca.New(t0)
	good := func() map[string]any {
		m := syncAnswer()
		delete(m, "extra")
		m["ca"] = string(c.caCert)
		m["certificate"] = c.sign(string(csr), clusterca.Client, 3)
		m["nodeToken"] = sentinelNodeToken
		return m
	}
	wrongKeyCert := func() string {
		_, other, _ := clusterca.NewKeyAndCSR()
		return c.sign(string(other), clusterca.Client, 3)
	}
	serverCert := c.sign(string(csr), clusterca.Server, 3)
	foreign, _ := clusterca.Sign(otherCA, otherKey, csr, clusterca.Client, mustAddr("10.98.0.3"), t0)

	tests := []struct {
		name   string
		mutate func(m map[string]any)
		field  string
	}{
		{"valid", func(m map[string]any) {}, ""},
		{"no certificate", func(m map[string]any) { delete(m, "certificate") }, "certificate"},
		{"certificate for another key", func(m map[string]any) { m["certificate"] = wrongKeyCert() }, "certificate"},
		{"a server certificate for a client", func(m map[string]any) { m["certificate"] = serverCert }, "certificate"},
		{"signed by another CA", func(m map[string]any) { m["certificate"] = string(foreign) }, "certificate"},
		{"no CA", func(m map[string]any) { delete(m, "ca") }, "ca"},
		{"the CA is a leaf", func(m map[string]any) { m["ca"] = m["certificate"] }, "ca"},
		{"token wrong prefix", func(m map[string]any) { m["nodeToken"] = "cvjn_" + strings.Repeat("a", 43) }, "nodeToken"},
		{"token too short", func(m map[string]any) { m["nodeToken"] = "cvnd_abc" }, "nodeToken"},
		{"token bad characters", func(m map[string]any) { m["nodeToken"] = "cvnd_" + strings.Repeat("a", 42) + "!" }, "nodeToken"},
		{"no token", func(m map[string]any) { delete(m, "nodeToken") }, "nodeToken"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := good()
			tt.mutate(m)
			_, err := ParseJoin(asJSON(t, m), Expect{OwnPublicKey: ownKey, Key: key, Now: t0})
			if tt.field == "" {
				if err != nil {
					t.Fatalf("refused a valid join answer: %v", err)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != tt.field {
				t.Fatalf("err = %v, want a FieldError for %s", err, tt.field)
			}
			if strings.Contains(err.Error(), sentinelNodeToken) || strings.Contains(err.Error(), "BEGIN") {
				t.Errorf("the error quotes a token or certificate: %v", err)
			}
		})
	}
	// A server's join answer must have a server certificate and a gossip key.
	m := good()
	m["role"] = "server"
	m["gossipKey"] = sentinelGossip
	if _, err := ParseJoin(asJSON(t, m), Expect{OwnPublicKey: ownKey, Key: key, Now: t0}); err == nil {
		t.Error("a server answer with a client certificate was accepted")
	}
}

func TestParseCertificate(t *testing.T) {
	c := newConsole(t)
	key, csr, _ := clusterca.NewKeyAndCSR()
	cert := c.sign(string(csr), clusterca.Server, 3)
	ok := func(m map[string]any, kind clusterca.Kind, k []byte) error {
		_, err := ParseCertificate(asJSON(t, m), Expect{CA: c.caCert, Key: k, Kind: kind, Now: t0})
		return err
	}
	if err := ok(map[string]any{"certificate": cert, "ca": string(c.caCert)}, clusterca.Server, key); err != nil {
		t.Errorf("valid answer refused: %v", err)
	}
	if err := ok(map[string]any{"certificate": cert}, clusterca.Client, key); err == nil {
		t.Error("a server certificate for a client was accepted")
	}
	other, _, _ := clusterca.New(t0)
	if err := ok(map[string]any{"certificate": cert, "ca": string(other)}, clusterca.Server, key); err == nil {
		t.Error("another CA was accepted")
	}
	anotherKey, _, _ := clusterca.NewKeyAndCSR()
	if err := ok(map[string]any{"certificate": cert}, clusterca.Server, anotherKey); err == nil {
		t.Error("a certificate for another key was accepted")
	}
}
