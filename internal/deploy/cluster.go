package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The cluster's own secrets (feature 031, constitution VII): the CA that signs every server's Nomad
// certificate and the gossip key, both in Nomad Variables of Caravana's own. The installer puts them
// there when it turns a host into the first server; Caravana reads them to sign certificates for
// joining servers and to hand the gossip key to a server that becomes a Nomad server.
const (
	clusterCAPath     = "caravana/cluster/ca"     // items cert and key
	clusterGossipPath = "caravana/cluster/gossip" // item key
)

// ErrNoCluster is returned when the cluster's variables aren't there: the host wasn't turned into a
// first server yet (run the installer again).
var ErrNoCluster = errors.New("the cluster's certificate authority isn't set up")

// ClusterCA returns the CA's certificate and private key (PEM). The key is returned to the caller
// that signs with it and must go nowhere else: it is never logged or put in an error.
func (d *Deployer) ClusterCA(ctx context.Context) (certPEM, keyPEM []byte, err error) {
	items, found, err := d.readVariableIfAny(ctx, clusterCAPath)
	if err != nil {
		return nil, nil, err // names the variable, never its items
	}
	if !found || strings.TrimSpace(items["cert"]) == "" || strings.TrimSpace(items["key"]) == "" {
		return nil, nil, fmt.Errorf("read %s: %w", clusterCAPath, ErrNoCluster)
	}
	return []byte(items["cert"]), []byte(items["key"]), nil
}

// GossipKey returns Nomad's gossip encryption key. The installer stores gossip.key as the file has
// it, with a trailing newline, so the value is trimmed.
func (d *Deployer) GossipKey(ctx context.Context) (string, error) {
	items, found, err := d.readVariableIfAny(ctx, clusterGossipPath)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(items["key"])
	if !found || key == "" {
		return "", fmt.Errorf("read %s: %w", clusterGossipPath, ErrNoCluster)
	}
	return key, nil
}
