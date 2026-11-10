package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// The two interfaces a server has (contracts/node.md §1).
const (
	appInterface  = "caravana0"     // a dummy interface: Nomad's network_interface, the app ports
	meshInterface = "caravana-mesh" // WireGuard
)

// Up makes the server's network from state.json, with no network needed: the caravana0 dummy
// interface with its app address, the caravana-mesh WireGuard interface with its mesh address and
// its peers, and the routes through the mesh. It is idempotent. Without state.json it does nothing
// (the host isn't set up yet).
func (a *Agent) Up(ctx context.Context) error {
	st, err := a.LoadState()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	priv, err := a.readMeshKey()
	if err != nil {
		return err
	}
	addrs, err := mesh.Of(st.Index)
	if err != nil {
		return err
	}

	if err := a.ensureLink(ctx, appInterface, "dummy"); err != nil {
		return err
	}
	if _, err := a.run(ctx, "ip", "address", "replace", addrs.App.String()+"/32", "dev", appInterface); err != nil {
		return fmt.Errorf("address %s: %w", appInterface, err)
	}
	if _, err := a.run(ctx, "ip", "link", "set", appInterface, "up"); err != nil {
		return fmt.Errorf("bring %s up: %w", appInterface, err)
	}

	if err := a.ensureLink(ctx, meshInterface, "wireguard"); err != nil {
		return err
	}
	if _, err := a.run(ctx, "ip", "address", "replace", addrs.Mesh.String()+"/32", "dev", meshInterface); err != nil {
		return fmt.Errorf("address %s: %w", meshInterface, err)
	}
	if err := a.wg(ctx, "setconf", priv, st.Peers); err != nil {
		return err
	}
	if _, err := a.run(ctx, "ip", "link", "set", meshInterface, "up"); err != nil {
		return fmt.Errorf("bring %s up: %w", meshInterface, err)
	}
	for _, route := range mesh.Routes(st.Index) {
		if _, err := a.run(ctx, "ip", "route", "replace", route.String(), "dev", meshInterface); err != nil {
			return fmt.Errorf("route %s: %w", route, err)
		}
	}
	return nil
}

// ensureLink makes an interface of this type when `ip link show` says there is none.
func (a *Agent) ensureLink(ctx context.Context, name, kind string) error {
	if _, err := a.run(ctx, "ip", "link", "show", "dev", name); err == nil {
		return nil
	}
	if _, err := a.run(ctx, "ip", "link", "add", name, "type", kind); err != nil {
		return fmt.Errorf("make %s: %w", name, err)
	}
	return nil
}

// wg gives the mesh interface its key and peers: `wg setconf` (all of it) or `wg syncconf` (what
// differs). The config text, which holds the private key, goes to a root-only temporary file in the
// agent's folder that is removed afterwards; only its path is an argument, so no key is ever on a
// command line.
func (a *Agent) wg(ctx context.Context, sub, privateKey string, peers []mesh.Peer) error {
	if err := a.ensureAgentDir(); err != nil {
		return err
	}
	suffix, err := randomHex(8)
	if err != nil {
		return err
	}
	path := a.path(agentDir + "/wg-" + suffix + ".conf")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write the WireGuard config: %w", err)
	}
	defer os.Remove(path)
	if _, err := f.WriteString(mesh.Config(privateKey, peers)); err != nil {
		f.Close()
		return fmt.Errorf("write the WireGuard config: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write the WireGuard config: %w", err)
	}
	if _, err := a.run(ctx, "wg", sub, meshInterface, path); err != nil {
		return fmt.Errorf("wg %s: %w", sub, err)
	}
	return nil
}

// readMeshKey reads mesh.key.
func (a *Agent) readMeshKey() (string, error) {
	b, err := os.ReadFile(a.path(meshKey))
	if err != nil {
		return "", fmt.Errorf("read mesh.key: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// ensureMeshKey makes mesh.key when it is missing and returns the private and public keys.
func (a *Agent) ensureMeshKey() (private, public string, err error) {
	if b, ok, err := readIfExists(a.path(meshKey)); err != nil {
		return "", "", fmt.Errorf("read mesh.key: %w", err)
	} else if ok {
		private = strings.TrimSpace(string(b))
		public, err = mesh.PublicKey(private)
		if err != nil {
			return "", "", fmt.Errorf("mesh.key isn't a WireGuard key")
		}
		return private, public, nil
	}
	private, public, err = mesh.NewKey()
	if err != nil {
		return "", "", err
	}
	if err := a.ensureAgentDir(); err != nil {
		return "", "", err
	}
	if err := writeFile(a.path(meshKey), []byte(private+"\n"), 0o600); err != nil {
		return "", "", fmt.Errorf("write mesh.key: %w", err)
	}
	return private, public, nil
}
