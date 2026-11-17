package nomadjob

import (
	"maps"
	"slices"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Placement (feature 042). A server's name and labels are Nomad node
// metadata that Caravana writes (dynamic metadata, through the API); a
// service's placement rules become constraints and affinities on its group
// that read them. The keys live here, in the pure package, so the translator
// and the code that writes the metadata can't drift apart.
const (
	// ServerMetaKey is the node metadata key that holds a managed server's
	// name. Caravana writes it on every managed server, which is what lets
	// "avoid" be a != test (a node without the key is never avoided).
	ServerMetaKey = "caravana.server"
	// LabelMetaPrefix starts the metadata key of each label of a server.
	LabelMetaPrefix = "caravana.label."
	// ServerAttribute is how a constraint reads ServerMetaKey.
	ServerAttribute = "${meta." + ServerMetaKey + "}"
)

// LabelMetaKey is the node metadata key that holds the label name.
func LabelMetaKey(name string) string { return LabelMetaPrefix + name }

// LabelAttribute is how a constraint or affinity reads the label name.
func LabelAttribute(name string) string { return "${meta." + LabelMetaKey(name) + "}" }

// placementBlocks is the service's placement as the group's constraints and
// affinities, or nothing for a service without any. Requirements come first,
// by label name, then the servers to avoid, by name, so the same service
// always gives the same job; preferences keep their order and weight (50 when
// a preference sets none).
func placementBlocks(svc app.Service) ([]*api.Constraint, []*api.Affinity) {
	p := svc.Placement
	if p == nil {
		return nil, nil
	}
	var constraints []*api.Constraint
	for _, name := range slices.Sorted(maps.Keys(p.Require)) {
		constraints = append(constraints, &api.Constraint{LTarget: LabelAttribute(name), RTarget: p.Require[name], Operand: "="})
	}
	for _, server := range slices.Sorted(slices.Values(p.Avoid)) {
		constraints = append(constraints, &api.Constraint{LTarget: ServerAttribute, RTarget: server, Operand: "!="})
	}
	var affinities []*api.Affinity
	for _, pref := range p.Prefer {
		affinities = append(affinities, &api.Affinity{
			LTarget: LabelAttribute(pref.Label),
			RTarget: pref.Value,
			Operand: "=",
			Weight:  new(int8(pref.WeightOrDefault())),
		})
	}
	return constraints, affinities
}
