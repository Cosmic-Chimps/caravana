package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// SyncOptions are the flags of `caravana node sync`.
type SyncOptions struct {
	// Renew renews the Nomad certificate now, whatever its age.
	Renew bool
	// PrintChecks prints the checks the console reports, one line each, for the installer to show.
	PrintChecks bool
}

// Sync asks the console for this server's state and applies it (contracts/node.md §3 and §6), in
// this order: peers and servers, the role, the certificate. A console that can't be reached or
// answers 5xx (or anything but 200 and 401), and a 401 (this server was removed), print a line on
// standard error and return nil: nothing changed, the timer tries again. An answer that fails
// validation changes nothing and is an error.
func (a *Agent) Sync(ctx context.Context, o SyncOptions) error {
	env, joined, err := a.LoadNodeEnv()
	if err != nil {
		return fmt.Errorf("read node.env: %w", err)
	}
	if !joined {
		a.out("not registered yet")
		return nil
	}
	st, err := a.LoadState()
	if err != nil {
		return err
	}
	report := a.Report(ctx, st)
	status, body, err := a.postJSON(ctx, env.URL, "/v1/node/sync", env.Token, report)
	if err != nil {
		a.warn("caravana node sync: the console can't be reached: %v", err)
		return nil
	}
	switch {
	case status == http.StatusUnauthorized:
		a.warn("caravana node sync: the console refused this server's token: it was removed from the cluster. Nothing changed.")
		return nil
	case status != http.StatusOK:
		a.warn("caravana node sync: the console answered HTTP %d. Nothing changed.", status)
		return nil
	}
	_, public, err := a.ensureMeshKeyPublic()
	if err != nil {
		return err
	}
	caPEM, _, err := readIfExists(a.path(caCert))
	if err != nil {
		return err
	}
	ans, err := ParseSync(body, Expect{State: &st, OwnPublicKey: public, CA: caPEM, Now: a.now()})
	if err != nil {
		return err
	}
	if o.PrintChecks {
		for _, c := range ans.Checks {
			word := "open"
			if c.OK {
				word = "ok"
			}
			a.out("check %s %s %s", clean(c.Name), word, clean(c.Detail))
		}
		a.out("state %s", clean(ans.State))
	}

	err = a.applySync(ctx, env, st, ans, caPEM, o.Renew)
	var soft *softError
	if errors.As(err, &soft) {
		a.warn("caravana node sync: %s", soft.msg)
		return nil
	}
	return err
}

// ensureMeshKeyPublic reads mesh.key and returns its public key. It never makes a key: a server
// that synced without one is broken.
func (a *Agent) ensureMeshKeyPublic() (private, public string, err error) {
	private, err = a.readMeshKey()
	if err != nil {
		return "", "", err
	}
	public, err = mesh.PublicKey(private)
	if err != nil {
		return "", "", errors.New("mesh.key isn't a WireGuard key")
	}
	return private, public, nil
}

// applySync does §6's three steps with a validated answer.
func (a *Agent) applySync(ctx context.Context, env NodeEnv, st State, ans *Answer, caPEM []byte, renew bool) error {
	priv, _, err := a.ensureMeshKeyPublic()
	if err != nil {
		return err
	}

	// 1. Peers and servers.
	next := st
	next.Pool = ans.Pool
	next.Servers = append([]int(nil), ans.Servers...)
	next.Peers = append([]mesh.Peer(nil), ans.Peers...)
	sort.Ints(next.Servers)
	sort.SliceStable(next.Peers, func(i, j int) bool { return next.Peers[i].Index < next.Peers[j].Index })
	peersChanged := !samePeers(st.Peers, next.Peers)
	otherChanged := !reflect.DeepEqual(sorted(st.Servers), sorted(next.Servers)) || st.Pool != next.Pool
	if peersChanged {
		if err := a.wg(ctx, "syncconf", priv, next.Peers); err != nil {
			return err
		}
	}
	if peersChanged || otherChanged {
		if err := a.SaveState(next); err != nil {
			return err
		}
		// A changed list of servers re-renders nomad.hcl without restarting Nomad: a client learns
		// its servers from the ones it knows, and the file is for its next start.
		if _, err := a.renderNomadChanged(next); err != nil {
			return err
		}
	}
	st = next

	// 2. The role: a client that is to be a Nomad server.
	if st.Role == RoleClient && ans.Role == RoleServer {
		return a.promote(ctx, env, st, ans.GossipKey, caPEM)
	}

	// 3. Renewal: asked for, wanted by the console, or due by the certificate's own date.
	if renew || ans.Renew || a.certificateEndsSoon() {
		return a.renewCertificate(ctx, env, st, caPEM)
	}
	return nil
}

// renewBefore is how long before a certificate's end the agent renews it (contracts/node.md §6).
const renewBefore = 30 * 24 * time.Hour

// certificateEndsSoon says agent.pem ends in under renewBefore. The agent checks this itself, so a
// renewal doesn't depend on the console saying so. A certificate that can't be read is left to the
// console's answer.
func (a *Agent) certificateEndsSoon() bool {
	b, ok, err := readIfExists(a.path(agentCert))
	if err != nil || !ok {
		return false
	}
	end, err := clusterca.NotAfter(b)
	if err != nil {
		return false
	}
	return end.Sub(a.now()) < renewBefore
}

// promote makes this client a Nomad server: a new key and certificate request, a server certificate
// from the console, gossip.key, nomad.hcl, and one restart of Nomad. Its running copies keep running.
func (a *Agent) promote(ctx context.Context, env NodeEnv, st State, gossip string, caPEM []byte) error {
	keyPEM, certPEM, err := a.newCertificate(ctx, env, st, clusterca.Server, caPEM)
	if err != nil {
		return err
	}
	if err := writeFile(a.path(agentKey), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write agent-key.pem: %w", err)
	}
	if err := writeFile(a.path(agentCert), certPEM, 0o644); err != nil {
		return fmt.Errorf("write agent.pem: %w", err)
	}
	if err := writeFile(a.path(gossipKey), []byte(gossip+"\n"), 0o600); err != nil {
		return fmt.Errorf("write gossip.key: %w", err)
	}
	st.Role = RoleServer
	if err := a.SaveState(st); err != nil {
		return err
	}
	if _, err := a.renderNomadChanged(st); err != nil {
		return err
	}
	if _, err := a.run(ctx, "systemctl", "restart", "nomad"); err != nil {
		return fmt.Errorf("restart nomad: %w", err)
	}
	a.out("Promoted to a Nomad server.")
	return nil
}

// renewCertificate makes a new key and certificate, writes the key and then the certificate, and
// has Nomad reload them (no restart: research L11).
func (a *Agent) renewCertificate(ctx context.Context, env NodeEnv, st State, caPEM []byte) error {
	kind := clusterca.Client
	if st.Role == RoleServer {
		kind = clusterca.Server
	}
	keyPEM, certPEM, err := a.newCertificate(ctx, env, st, kind, caPEM)
	if err != nil {
		return err
	}
	if err := writeFile(a.path(agentKey), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write agent-key.pem: %w", err)
	}
	if err := writeFile(a.path(agentCert), certPEM, 0o644); err != nil {
		return fmt.Errorf("write agent.pem: %w", err)
	}
	if _, err := a.run(ctx, "systemctl", "reload", "nomad"); err != nil {
		return fmt.Errorf("reload nomad: %w", err)
	}
	a.out("Renewed this server's certificate.")
	return nil
}

// newCertificate makes a key and certificate request and has the console sign it. Nothing is written.
// A console that can't be reached, or refuses, or fails, is a soft error.
func (a *Agent) newCertificate(ctx context.Context, env NodeEnv, st State, kind clusterca.Kind, caPEM []byte) (keyPEM, certPEM []byte, err error) {
	if len(caPEM) == 0 {
		return nil, nil, errors.New("ca.pem is missing")
	}
	keyPEM, csr, err := clusterca.NewKeyAndCSR()
	if err != nil {
		return nil, nil, err
	}
	status, body, err := a.postJSON(ctx, env.URL, "/v1/node/certificate", env.Token, map[string]string{"csr": string(csr)})
	if err != nil {
		return nil, nil, &softError{"the console can't be reached for a certificate: " + err.Error()}
	}
	if status != http.StatusOK {
		return nil, nil, &softError{fmt.Sprintf("the console answered HTTP %d to the certificate request. Nothing changed.", status)}
	}
	ans, err := ParseCertificate(body, Expect{CA: caPEM, Key: keyPEM, Kind: kind, Now: a.now()})
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, []byte(ans.Certificate), nil
}

func samePeers(a, b []mesh.Peer) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]mesh.Peer(nil), a...)
	y := append([]mesh.Peer(nil), b...)
	sort.SliceStable(x, func(i, j int) bool { return x[i].Index < x[j].Index })
	sort.SliceStable(y, func(i, j int) bool { return y[i].Index < y[j].Index })
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func sorted(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	if out == nil {
		out = []int{}
	}
	return out
}
