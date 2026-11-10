package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// InitFirstOptions are the flags of `caravana node init-first`.
type InitFirstOptions struct {
	// Address is the address other servers reach this one at. It is checked and used by the console
	// side (the installer passes it on to `caravana server adopt-first`); no file here holds it.
	Address         string
	CPUTotalCompute int
}

// InitFirst makes server 1 offline: mesh.key, state.json, the cluster's CA, this server's server
// certificate, gossip.key and nomad.hcl, then runs Up. It makes only what is missing, so a second
// run changes nothing. ca-key.pem stays in the agent's folder until the installer has put it in a
// Nomad Variable and shredded it.
func (a *Agent) InitFirst(ctx context.Context, o InitFirstOptions) error {
	if _, err := netip.ParseAddr(o.Address); err != nil {
		return errors.New("--address must be an IPv4 or IPv6 address")
	}
	if err := a.ensureAgentDir(); err != nil {
		return err
	}
	if _, _, err := a.ensureMeshKey(); err != nil {
		return err
	}

	st, err := a.LoadState()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		host := ""
		if out, err := a.run(ctx, "uname", "-n"); err == nil {
			host = string(out)
		}
		st = State{
			Version: stateVersion, Index: 1, Name: NameOfHost(host, 1), Pool: "default", First: true,
			Role: RoleServer, Servers: []int{1}, CPUTotalCompute: o.CPUTotalCompute,
		}
		if err := a.SaveState(st); err != nil {
			return err
		}
	case err != nil:
		return err
	case !st.First || st.Index != 1:
		return errors.New("this server joined another cluster, so it can't be a first server")
	case o.CPUTotalCompute > 0 && o.CPUTotalCompute != st.CPUTotalCompute:
		st.CPUTotalCompute = o.CPUTotalCompute
		if err := a.SaveState(st); err != nil {
			return err
		}
	}

	if err := a.ensureCA(); err != nil {
		return err
	}
	if _, ok, err := readIfExists(a.path(gossipKey)); err != nil {
		return err
	} else if !ok {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("make the gossip key: %w", err)
		}
		if err := writeFile(a.path(gossipKey), []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
			return fmt.Errorf("write gossip.key: %w", err)
		}
	}
	if err := a.renderNomad(st); err != nil {
		return err
	}
	return a.Up(ctx)
}

// ensureCA makes the CA and this server's certificate when there is no ca.pem. ca.pem is written
// last, so a run that stopped half way leaves no ca.pem and can start again. When ca.pem exists but
// agent.pem doesn't, the CA's key may be in Nomad already and gone from here, so it stops.
func (a *Agent) ensureCA() error {
	_, haveCA, err := readIfExists(a.path(caCert))
	if err != nil {
		return err
	}
	_, haveCert, err := readIfExists(a.path(agentCert))
	if err != nil {
		return err
	}
	if haveCA {
		if !haveCert {
			return errors.New("ca.pem exists but agent.pem doesn't: the cluster's CA key may be stored in Nomad already, so this server's certificate can't be made here")
		}
		return nil
	}

	now := a.now()
	caPEM, caKeyPEM, err := clusterca.New(now)
	if err != nil {
		return err
	}
	keyPEM, csr, err := clusterca.NewKeyAndCSR()
	if err != nil {
		return err
	}
	addrs, err := mesh.Of(1)
	if err != nil {
		return err
	}
	certPEM, err := clusterca.Sign(caPEM, caKeyPEM, csr, clusterca.Server, addrs.Mesh, now)
	if err != nil {
		return err
	}
	if err := writeFile(a.path(caKeyFile), caKeyPEM, 0o600); err != nil {
		return fmt.Errorf("write ca-key.pem: %w", err)
	}
	if err := writeFile(a.path(agentKey), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write agent-key.pem: %w", err)
	}
	if err := writeFile(a.path(agentCert), certPEM, 0o644); err != nil {
		return fmt.Errorf("write agent.pem: %w", err)
	}
	if err := writeFile(a.path(caCert), caPEM, 0o644); err != nil {
		return fmt.Errorf("write ca.pem: %w", err)
	}
	return nil
}

// Show returns one fact for the installer: public-key, name, index, role or first.
func (a *Agent) Show(field string) (string, error) {
	if field == "public-key" {
		priv, err := a.readMeshKey()
		if err != nil {
			return "", err
		}
		return mesh.PublicKey(priv)
	}
	st, err := a.LoadState()
	if err != nil {
		return "", err
	}
	switch field {
	case "name":
		return st.Name, nil
	case "index":
		return fmt.Sprint(st.Index), nil
	case "role":
		return st.Role, nil
	case "first":
		return fmt.Sprint(st.First), nil
	}
	return "", fmt.Errorf("unknown field %q: public-key, name, index, role or first", field)
}

// ShowFields are the fields Show knows.
var ShowFields = []string{"public-key", "name", "index", "role", "first"}

// Leave takes this server out of the cluster on its own side: it stops and disables Nomad, deletes
// the mesh and app interfaces, and removes the agent's folder and Nomad's certificates. It refuses
// on the first server. The console still has the server until it is removed there.
func (a *Agent) Leave(ctx context.Context) error {
	if st, err := a.LoadState(); err == nil {
		if st.First {
			return errors.New("this is the first server: it can't leave (use the installer's --uninstall)")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Nomad leaves the cluster before it stops: SIGTERM is its signal for that (leave_on_terminate in
	// nomad.hcl). A Nomad server tells the others it is going, so the leader takes it out of raft.
	// Stopping it without that left a cluster of two Nomad servers with no quorum (research L16).
	a.leaveNomad(ctx)
	// Each step is tried even when an earlier one failed, so a half-left server can finish leaving.
	var problems []string
	for _, cmd := range [][]string{
		{"systemctl", "disable", "--now", "nomad"},
		{"ip", "link", "delete", meshInterface},
		{"ip", "link", "delete", appInterface},
	} {
		if _, err := a.run(ctx, cmd[0], cmd[1:]...); err != nil {
			problems = append(problems, err.Error())
		}
	}
	for _, dir := range []string{agentDir, tlsDir} {
		if err := os.RemoveAll(a.path(dir)); err != nil {
			return fmt.Errorf("remove %s: %w", dir, err)
		}
	}
	if len(problems) > 0 {
		a.warn("caravana node leave: some steps found nothing to do: %s", strings.Join(problems, "; "))
	}
	a.out("This server left the cluster. Remove it in the console too.")
	return nil
}

// leaveWait is how long Nomad gets to leave the cluster and exit after SIGTERM, and leaveStep how
// often that is checked.
const (
	leaveWait = 30 * time.Second
	leaveStep = time.Second
)

// leaveNomad sends Nomad SIGTERM and waits until its unit is no longer active. A Nomad that isn't
// running, or that doesn't stop in time, is left to the stop that follows: leaving is best effort,
// and the server goes out either way.
func (a *Agent) leaveNomad(ctx context.Context) {
	if _, err := a.run(ctx, "systemctl", "is-active", "--quiet", "nomad"); err != nil {
		return // not running: nothing to leave
	}
	if _, err := a.run(ctx, "systemctl", "kill", "--signal=SIGTERM", "nomad"); err != nil {
		a.warn("caravana node leave: couldn't ask Nomad to leave the cluster: %v", err)
		return
	}
	for waited := time.Duration(0); waited < leaveWait; waited += leaveStep {
		if _, err := a.run(ctx, "systemctl", "is-active", "--quiet", "nomad"); err != nil {
			return // it left and exited
		}
		if a.Sleep != nil {
			a.Sleep(leaveStep)
		} else {
			time.Sleep(leaveStep)
		}
	}
	a.warn("caravana node leave: Nomad didn't exit within %s of being asked to leave; it is stopped now", leaveWait)
}
