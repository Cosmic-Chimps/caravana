package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// ErrNoPublicNetwork is a deploy that publishes a port when no ready server
// offers the host network that carries published ports (spec 041). The error
// is a *PublicNetworkError, which names the field.
var ErrNoPublicNetwork = errors.New("no server offers published ports")

// PublicNetworkError is the refusal of a deploy that publishes a port the
// cluster can't open. Field is the first published port's field
// ("services[0].ports[1].published").
type PublicNetworkError struct {
	Field   string
	Message string
}

func (e *PublicNetworkError) Error() string { return e.Field + ": " + e.Message }

// Unwrap makes errors.Is(err, ErrNoPublicNetwork) true.
func (e *PublicNetworkError) Unwrap() error { return ErrNoPublicNetwork }

// firstPublished is the field of the first published port of a, or "" when it
// publishes none.
func firstPublished(a app.App) string {
	for i, svc := range a.Services {
		if f := firstPublishedOf(i, svc); f != "" {
			return f
		}
	}
	return ""
}

// firstPublishedOf is the field of service i's first published port.
func firstPublishedOf(i int, svc app.Service) string {
	for j, p := range svc.Ports {
		if p.IsPublished() {
			return fmt.Sprintf("services[%d].ports[%d].published", i, j)
		}
	}
	return ""
}

// checkPublicNetwork returns the IDs of the nodes that can open a's published
// ports: ready, eligible and with the host network "public" in their client
// configuration, oldest first. It returns nil and no error for an app that
// publishes nothing, and a *PublicNetworkError when it publishes and no node
// qualifies. The node records are read fresh (not cached): a server whose
// configuration just gained the network must be seen at once.
func (d *Deployer) checkPublicNetwork(ctx context.Context, a app.App) ([]string, error) {
	field := firstPublished(a)
	if field == "" {
		return nil, nil
	}
	nodes, err := d.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, n := range nodes {
		if n.Status != "ready" || !n.Eligible || n.Draining {
			continue
		}
		info, _, err := d.nomad.Nodes().Info(n.ID, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			return nil, d.nodeError("read node "+n.Name, err)
		}
		if _, ok := info.HostNetworks[mesh.PublicHostNetwork]; ok {
			ids = append(ids, n.ID)
		}
	}
	if len(ids) == 0 {
		return nil, &PublicNetworkError{Field: field,
			Message: "no server offers published ports: rerun the installer on the first server"}
	}
	return ids, nil
}
