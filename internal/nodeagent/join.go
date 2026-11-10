package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
)

// JoinOptions are the flags of `caravana node join`.
type JoinOptions struct {
	URL             string
	Token           string // the join token; never printed or stored
	Address         string // the address other servers reach this one at; empty lets the console see it
	Insecure        bool   // the console's certificate isn't verified (recorded in node.env)
	CPUTotalCompute int
}

type joinRequest struct {
	Token     string `json:"token"`
	PublicKey string `json:"publicKey"`
	CSR       string `json:"csr"`
	Address   string `json:"address,omitempty"`
	Facts     Facts  `json:"facts"`
}

// Join registers this server with the console and makes its files (contracts/node.md §3). When
// node.env exists it has joined already: it renders nomad.hcl, runs Up and makes no call. On a
// refusal it writes nothing but mesh.key (a retry reuses it) and returns the console's message.
func (a *Agent) Join(ctx context.Context, o JoinOptions) error {
	if _, joined, err := a.LoadNodeEnv(); err != nil {
		return fmt.Errorf("read node.env: %w", err)
	} else if joined {
		st, err := a.LoadState()
		if err != nil {
			return fmt.Errorf("this server has joined, but %w", err)
		}
		if o.CPUTotalCompute > 0 && o.CPUTotalCompute != st.CPUTotalCompute {
			st.CPUTotalCompute = o.CPUTotalCompute
			if err := a.SaveState(st); err != nil {
				return err
			}
		}
		if err := a.renderNomad(st); err != nil {
			return err
		}
		if err := a.Up(ctx); err != nil {
			return err
		}
		a.out("This server has joined already as %s.", st.Name)
		return nil
	}

	if err := ValidConsoleURL(o.URL); err != nil {
		return err
	}
	if !ValidJoinToken(o.Token) {
		return errors.New("the join token isn't one: it is cvjn_ and 43 characters")
	}
	if o.Address != "" {
		if _, err := netip.ParseAddr(o.Address); err != nil {
			return errors.New("--address must be an IPv4 or IPv6 address")
		}
	}

	_, public, err := a.ensureMeshKey()
	if err != nil {
		return err
	}
	nomadKey, csr, err := clusterca.NewKeyAndCSR()
	if err != nil {
		return err
	}
	facts, _ := a.facts(ctx)
	status, body, err := a.postJSON(ctx, o.URL, "/v1/join", "", joinRequest{
		Token: o.Token, PublicKey: public, CSR: string(csr), Address: o.Address, Facts: facts,
	})
	if err != nil {
		return fmt.Errorf("reach the console at %s: %w", o.URL, err)
	}
	if status != http.StatusOK {
		return consoleError(status, body)
	}
	ans, err := ParseJoin(body, Expect{OwnPublicKey: public, Key: nomadKey, Now: a.now()})
	if err != nil {
		return err
	}

	st := State{
		Version: stateVersion, Index: ans.Index, Name: ans.Name, Pool: ans.Pool, Role: ans.Role,
		Servers: ans.Servers, Peers: ans.Peers, CPUTotalCompute: o.CPUTotalCompute,
	}
	if err := st.Validate(); err != nil {
		return err
	}

	// Everything is validated: now write. node.env goes last, because it is what says "joined".
	if err := writeFile(a.path(caCert), []byte(ans.CA), 0o644); err != nil {
		return fmt.Errorf("write ca.pem: %w", err)
	}
	if err := writeFile(a.path(agentKey), nomadKey, 0o600); err != nil {
		return fmt.Errorf("write agent-key.pem: %w", err)
	}
	if err := writeFile(a.path(agentCert), []byte(ans.Certificate), 0o644); err != nil {
		return fmt.Errorf("write agent.pem: %w", err)
	}
	if ans.Role == RoleServer {
		if err := writeFile(a.path(gossipKey), []byte(ans.GossipKey+"\n"), 0o600); err != nil {
			return fmt.Errorf("write gossip.key: %w", err)
		}
	}
	if err := a.SaveState(st); err != nil {
		return err
	}
	if err := a.renderNomad(st); err != nil {
		return err
	}
	if err := a.SaveNodeEnv(NodeEnv{URL: strings.TrimRight(o.URL, "/"), Token: ans.NodeToken, Insecure: o.Insecure}); err != nil {
		return err
	}
	if err := a.Up(ctx); err != nil {
		return err
	}
	a.out("Joined the cluster as %s.", st.Name)
	return nil
}

// renderNomad writes /etc/nomad.d/nomad.hcl (0600) for this state, when it changed. It says nothing
// to Nomad: restarting is the caller's decision.
func (a *Agent) renderNomad(st State) error {
	_, err := a.renderNomadChanged(st)
	return err
}

func (a *Agent) renderNomadChanged(st State) (bool, error) {
	key := ""
	if st.Role == RoleServer {
		b, err := os.ReadFile(a.path(gossipKey))
		if err != nil {
			return false, fmt.Errorf("read gossip.key: %w", err)
		}
		key = strings.TrimSpace(string(b))
	}
	text, err := renderNomad(st, key)
	if err != nil {
		return false, err
	}
	changed, err := writeIfChanged(a.path(nomadConf), []byte(text), 0o600)
	if err != nil {
		return false, fmt.Errorf("write nomad.hcl: %w", err)
	}
	return changed, nil
}
