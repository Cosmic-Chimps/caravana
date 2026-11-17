package deploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Placement and drain (feature 042): a server's labels as Nomad node metadata, the refusal of a
// placement rule no server can satisfy, and the queued copies a rule left unplaced. Moving work off
// a server is in nodework.go.

var (
	// ErrLabelsPending says Nomad accepted a node's new metadata but its servers haven't shown it
	// yet (research R1): a deploy that plans at once may still see the old labels.
	ErrLabelsPending = errors.New("nomad has not shown the new labels yet")
	// ErrNodeNotFound is a call about a node Nomad doesn't know.
	ErrNodeNotFound = errors.New("node not found")
	// ErrPlacementImpossible is a deploy whose placement rules no server that accepts work
	// satisfies. The error is a *PlacementError, which names the field.
	ErrPlacementImpossible = errors.New("no server satisfies the placement rules")
)

// PlacementError is the refusal of a deploy whose placement rule fits no server. Field is the
// rule's field ("services[0].placement.require.gpu").
type PlacementError struct {
	Field   string
	Message string
}

func (e *PlacementError) Error() string { return e.Field + ": " + e.Message }

// Unwrap makes errors.Is(err, ErrPlacementImpossible) true.
func (e *PlacementError) Unwrap() error { return ErrPlacementImpossible }

// metaOf is the part of a node's metadata Caravana owns: the server name and the labels, by label
// name. server is "" when the node has no caravana.server key.
func metaOf(meta map[string]string) (server string, labels map[string]string) {
	labels = map[string]string{}
	for k, v := range meta {
		if name, ok := strings.CutPrefix(k, nomadjob.LabelMetaPrefix); ok {
			labels[name] = v
		}
	}
	return meta[nomadjob.ServerMetaKey], labels
}

// NodeLabels returns the server name and the labels in a node's metadata, as Nomad's servers show
// them. It reads the cached node record (at most ten minutes old, fresh after SetNodeLabels), so it
// is cheap enough for a list; FreshNodeLabels asks Nomad.
func (d *Deployer) NodeLabels(ctx context.Context, nodeID string) (server string, labels map[string]string, err error) {
	node, err := d.nodeInfo(ctx, nodeID)
	if err != nil {
		return "", nil, err
	}
	server, labels = metaOf(node.Meta)
	return server, labels, nil
}

// FreshNodeLabels is NodeLabels read from Nomad now, for the reconcile that must see a label
// somebody changed outside Caravana.
func (d *Deployer) FreshNodeLabels(ctx context.Context, nodeID string) (server string, labels map[string]string, err error) {
	d.forgetNodeInfo(nodeID)
	return d.NodeLabels(ctx, nodeID)
}

// SetNodeLabels makes the node's metadata hold the server name and exactly these labels: it writes
// caravana.server and caravana.label.<name> for each label, and unsets every caravana.label.* key
// the node has that labels doesn't, all in one write (the API unsets several keys at once, which
// the CLI's flag doesn't, research R1). Nothing is written when the node already has them.
//
// A write reaches Nomad's servers a few seconds after the node accepts it, and the scheduler only
// sees it then, so SetNodeLabels waits until the node's record shows the metadata, for at most
// Timing.LabelWait, and returns ErrLabelsPending when it doesn't: the write was accepted. A 403 is
// the capability error that says to run the installer again (the policy needs node:write).
func (d *Deployer) SetNodeLabels(ctx context.Context, nodeID, server string, labels map[string]string) error {
	node, err := d.freshNode(ctx, nodeID)
	if err != nil {
		return err
	}
	want := map[string]*string{nomadjob.ServerMetaKey: &server}
	for name, value := range labels {
		want[nomadjob.LabelMetaKey(name)] = &value
	}
	for key := range node.Meta {
		if strings.HasPrefix(key, nomadjob.LabelMetaPrefix) {
			if _, keep := want[key]; !keep {
				want[key] = nil // unset
			}
		}
	}
	if metaMatches(node.Meta, want) {
		return nil
	}
	_, err = d.nomad.Nodes().Meta().Apply(&api.NodeMetaApplyRequest{NodeID: nodeID, Meta: want},
		(&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		switch statusOf(err) {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
		}
		return d.capabilityError("write the node's metadata", "node:write", err)
	}

	timing := d.Timing.withDefaults()
	waitCtx, cancel := context.WithTimeout(ctx, timing.LabelWait)
	defer cancel()
	defer d.forgetNodeInfo(nodeID) // the cached record is older than the write
	for {
		node, err := d.freshNode(waitCtx, nodeID)
		switch {
		case err == nil && metaMatches(node.Meta, want):
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		case waitCtx.Err() != nil:
			return fmt.Errorf("%w: %s after %s", ErrLabelsPending, nodeID, timing.LabelWait)
		}
		if !sleep(waitCtx, timing.PollGap) && ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// freshNode reads the node's record from Nomad, not from the cache. An unknown node is
// ErrNodeNotFound.
func (d *Deployer) freshNode(ctx context.Context, nodeID string) (*api.Node, error) {
	node, _, err := d.nomad.Nodes().Info(nodeID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
		}
		return nil, d.nodeError("read node "+nodeID, err)
	}
	return node, nil
}

// metaMatches says the node's metadata holds every key of want with its value, and none that want
// unsets.
func metaMatches(meta map[string]string, want map[string]*string) bool {
	for key, value := range want {
		have, ok := meta[key]
		if value == nil && ok || value != nil && (!ok || have != *value) {
			return false
		}
	}
	return true
}

// SetNodeEligible tells Nomad whether the scheduler may place new work on the node. A 403 is the
// capability error (node:write).
func (d *Deployer) SetNodeEligible(ctx context.Context, nodeID string, eligible bool) error {
	_, err := d.nomad.Nodes().ToggleEligibility(nodeID, eligible, (&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
		}
		return d.capabilityError("change a node's eligibility", "node:write", err)
	}
	return nil
}

// checkPlacement plans the job when some service of the app has placement rules, and refuses the
// deploy when a group fails to place because of Caravana's own constraints alone (research R3):
// every node it looked at was filtered by a `${meta.caravana.…}` constraint and none was merely
// full. Lack of room is not a refusal (the deploy goes on, as it does for an app without rules),
// and neither is a failure that other constraints explain (a volume that isn't made yet).
//
// The refusal names the rule's field: services[i].placement.require.<label> for a label no server
// that accepts work has, services[i].placement.avoid when every such server is avoided.
func (d *Deployer) checkPlacement(ctx context.Context, job *api.Job, a app.App) error {
	if !slices.ContainsFunc(a.Services, func(s app.Service) bool { return s.Placement != nil }) {
		return nil
	}
	resp, _, err := d.nomad.Jobs().PlanOpts(job, &api.PlanOptions{Diff: false}, (&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		return d.registerError(a.ID, err)
	}
	for i, svc := range a.Services {
		if svc.Placement == nil {
			continue
		}
		if field, msg, ok := placementRefusal(i, resp.FailedTGAllocs[svc.Name]); ok {
			return &PlacementError{Field: field, Message: msg}
		}
	}
	return nil
}

// placementRefusal reads a failed group's metric: ok when Caravana's constraints, and only they,
// explain why no node was a candidate.
func placementRefusal(i int, m *api.AllocationMetric) (field, message string, ok bool) {
	if m == nil || m.NodesExhausted > 0 || len(m.DimensionExhausted) > 0 || len(m.ConstraintFiltered) == 0 {
		return "", "", false
	}
	for text := range m.ConstraintFiltered {
		if !strings.HasPrefix(text, "${meta.caravana.") {
			return "", "", false
		}
	}
	// The same text always parses to the same refusal; the first key in order keeps the answer
	// the same from one plan to the next.
	for _, text := range slices.Sorted(maps.Keys(m.ConstraintFiltered)) {
		if name, value, found := labelConstraint(text); found {
			return fmt.Sprintf("services[%d].placement.require.%s", i, name),
				fmt.Sprintf("no server that accepts work has the label %s=%s", name, value), true
		}
		if strings.HasPrefix(text, nomadjob.ServerAttribute+" !=") {
			return fmt.Sprintf("services[%d].placement.avoid", i), "every server that accepts work is avoided", true
		}
	}
	return "", "", false
}

// labelConstraint reads "${meta.caravana.label.<name>} = <value>", the text Nomad gives a label
// constraint that filtered nodes.
func labelConstraint(text string) (name, value string, ok bool) {
	rest, found := strings.CutPrefix(text, "${meta."+nomadjob.LabelMetaPrefix)
	if !found {
		return "", "", false
	}
	name, rest, found = strings.Cut(rest, "}")
	if !found {
		return "", "", false
	}
	value, found = strings.CutPrefix(strings.TrimSpace(rest), "=")
	if !found {
		return "", "", false
	}
	return name, strings.TrimSpace(value), true
}

// QueuedCopies returns, by job ID and group, how many copies Nomad has queued: wanted, with no
// node to put them on. Only non-zero entries are listed. It reads the job list with its summaries
// in one request.
func (d *Deployer) QueuedCopies(ctx context.Context) (map[string]map[string]int, error) {
	stubs, _, err := d.nomad.Jobs().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list jobs", err)
	}
	out := map[string]map[string]int{}
	for _, stub := range stubs {
		if stub.JobSummary == nil {
			continue
		}
		for group, s := range stub.JobSummary.Summary {
			if s.Queued == 0 {
				continue
			}
			if out[stub.ID] == nil {
				out[stub.ID] = map[string]int{}
			}
			out[stub.ID][group] = s.Queued
		}
	}
	return out, nil
}
