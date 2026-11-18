package deploytest

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// What feature 042 asks of Nomad: a node's dynamic metadata (labels), its scheduling eligibility,
// "stop and reschedule" of a copy, a job plan and the queued count in a job's summary. A test sets
// the world with the knobs below and reads what was asked with the accessors.

// placementFake is the fake's state for these endpoints.
type placementFake struct {
	meta       map[string]*nodeMeta // dynamic metadata, by node ID
	metaDelay  time.Duration
	metaWrites int
	writeFail  int // non-zero: metadata and eligibility writes answer it

	eligibility []EligibilityRequest

	stops      []string // copy IDs stopped, in order
	stopFail   int      // non-zero: every allocation stop answers it
	moveSet    bool
	moveTo     string // node the replacement of a stopped copy is placed on
	moveToName string
	startDelay time.Duration
	previous   map[string]string // replacement copy ID -> the copy it replaces
	moved      int               // replacements made

	plans    int
	planFail map[string]api.AllocationMetric // by group
	planBody []*api.Job

	queued map[string]map[string]int // job ID -> group -> queued copies
}

// nodeMeta is one node's dynamic metadata as Nomad's servers show it. A write reaches the servers
// after the configured delay: until then Info still answers visible.
type nodeMeta struct {
	visible map[string]string
	next    map[string]string // what the servers will show at `at`; nil when nothing is pending
	at      time.Time
}

func (m *nodeMeta) resolve(now time.Time) {
	if m.next != nil && !now.Before(m.at) {
		m.visible, m.next = m.next, nil
	}
}

// latest is the metadata including what hasn't reached the servers yet.
func (m *nodeMeta) latest() map[string]string {
	if m.next != nil {
		return m.next
	}
	return m.visible
}

// EligibilityRequest is one eligibility change the fake answered.
type EligibilityRequest struct {
	Node     string
	Eligible bool
}

// SetNodeMetaDelay makes a node's dynamic metadata reach Nomad's servers (the node record Info
// answers) d after it was written, as research R1 observed (8 to 10 s). 0, the default, is at once.
func (n *Nomad) SetNodeMetaDelay(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.place.metaDelay = d
}

// SetNodeMeta replaces the node's dynamic metadata at once, as if it had been written long ago.
func (n *Nomad) SetNodeMeta(id string, meta map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.emitNode(id)
	n.metaOf(id).visible, n.metaOf(id).next = maps.Clone(meta), nil
}

// SetNodeStaticMeta sets the node's static metadata (its configuration's meta block).
func (n *Nomad) SetNodeStaticMeta(id string, meta map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.emitNode(id)
	if node := n.findNode(id); node != nil {
		node.Meta = maps.Clone(meta)
	}
}

// NodeMeta returns the node's metadata, static and dynamic, as the latest write left it (a write
// the delay still holds back is included), or nil for an unknown node.
func (n *Nomad) NodeMeta(id string) map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.findNode(id) == nil {
		return nil
	}
	return n.nodeMetaLocked(id)
}

// NodeMetaWrites counts the metadata writes answered.
func (n *Nomad) NodeMetaWrites() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.place.metaWrites
}

// FailNodeWrites makes every node metadata and eligibility write answer status (0 undoes it): 403
// is a token whose policy has no node:write.
func (n *Nomad) FailNodeWrites(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.place.writeFail = status
}

// FailAllocStops makes every allocation stop answer status (0 undoes it): 403 is a token with no
// alloc-lifecycle capability.
func (n *Nomad) FailAllocStops(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.place.stopFail = status
}

// EligibilityRequests returns the eligibility writes answered, oldest first.
func (n *Nomad) EligibilityRequests() []EligibilityRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.place.eligibility)
}

// MoveCopiesTo makes a stopped copy's replacement land on this node (its ID and name), as Nomad's
// scheduler places it on an eligible node. Without it the replacement stays pending, as when no
// node takes the work.
func (n *Nomad) MoveCopiesTo(nodeID, nodeName string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.place.moveSet, n.place.moveTo, n.place.moveToName = true, nodeID, nodeName
}

// SetStartDelay sets how long a replacement copy stays pending before it runs (0: at once).
func (n *Nomad) SetStartDelay(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.place.startDelay = d
}

// StoppedCopies returns the IDs of the copies stopped through the allocation stop, oldest first.
func (n *Nomad) StoppedCopies() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.place.stops)
}

// SetPlanFailure makes a job plan fail to place group because every one of count nodes failed the
// constraint with this text (Nomad's own text, "${meta.caravana.label.ssd} = true"). Groups the
// plan's job doesn't have are left out of the answer.
func (n *Nomad) SetPlanFailure(group, constraint string, count int) {
	n.SetPlanFailureMetric(group, api.AllocationMetric{
		NodesEvaluated: count, NodesFiltered: count,
		ConstraintFiltered: map[string]int{constraint: count},
	})
}

// SetPlanFailureMetric makes a job plan fail to place group with this metric (for an exhausted
// node, a mix of constraints and so on).
func (n *Nomad) SetPlanFailureMetric(group string, m api.AllocationMetric) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.place.planFail == nil {
		n.place.planFail = map[string]api.AllocationMetric{}
	}
	n.place.planFail[group] = m
}

// ClearPlanFailures makes every plan place everything again.
func (n *Nomad) ClearPlanFailures() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.place.planFail = nil
}

// PlanRequests counts the job plans answered.
func (n *Nomad) PlanRequests() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.place.plans
}

// PlannedJob returns the job of the newest plan request, or nil when there was none.
func (n *Nomad) PlannedJob() *api.Job {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.place.planBody) == 0 {
		return nil
	}
	return n.place.planBody[len(n.place.planBody)-1]
}

// SetQueued sets the queued copies of the job's group in its summary, as the job list shows them
// (0 removes the entry).
func (n *Nomad) SetQueued(jobID, group string, queued int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.place.queued == nil {
		n.place.queued = map[string]map[string]int{}
	}
	if queued == 0 {
		delete(n.place.queued[jobID], group)
		return
	}
	if n.place.queued[jobID] == nil {
		n.place.queued[jobID] = map[string]int{}
	}
	n.place.queued[jobID][group] = queued
}

// metaOf returns the node's dynamic metadata record, made on first use. n.mu must be held.
func (n *Nomad) metaOf(id string) *nodeMeta {
	if n.place.meta == nil {
		n.place.meta = map[string]*nodeMeta{}
	}
	m := n.place.meta[id]
	if m == nil {
		m = &nodeMeta{visible: map[string]string{}}
		n.place.meta[id] = m
	}
	return m
}

// findNode returns the node with this ID (the one SetNode made, or one AddNode made), or nil.
// n.mu must be held.
func (n *Nomad) findNode(id string) *api.Node {
	if n.host.set && n.host.node.ID == id {
		return &n.host.node
	}
	for _, x := range n.host.extra {
		if x.ID == id {
			return x
		}
	}
	return nil
}

// nodeRecord is the node as Info answers it: its static metadata with the dynamic metadata the
// servers show now. n.mu must be held.
func (n *Nomad) nodeRecord(node *api.Node) *api.Node {
	out := *node
	m := n.place.meta[node.ID]
	if m == nil {
		return &out
	}
	m.resolve(time.Now())
	merged := maps.Clone(node.Meta)
	if merged == nil {
		merged = map[string]string{}
	}
	maps.Copy(merged, m.visible)
	out.Meta = merged
	return &out
}

// serveNodeMeta answers POST /v1/client/metadata: merge the request's keys into the node's dynamic
// metadata (a null value unsets the key).
func (n *Nomad) serveNodeMeta(w http.ResponseWriter, r *http.Request) {
	var req api.NodeMetaApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.place.writeFail != 0 {
		http.Error(w, "Permission denied", n.place.writeFail)
		return
	}
	if n.findNode(req.NodeID) == nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	n.place.metaWrites++
	m := n.metaOf(req.NodeID)
	m.resolve(time.Now())
	next := maps.Clone(m.latest())
	for k, v := range req.Meta {
		if v == nil {
			delete(next, k)
		} else {
			next[k] = *v
		}
	}
	if n.place.metaDelay == 0 {
		m.visible, m.next = next, nil
	} else {
		m.next, m.at = next, time.Now().Add(n.place.metaDelay)
	}
	n.emitNode(req.NodeID)
	n.writeJSON(w, api.NodeMetaResponse{Meta: n.nodeMetaLocked(req.NodeID), Dynamic: stringPointers(next)})
}

// nodeMetaLocked is NodeMeta for a caller that holds n.mu.
func (n *Nomad) nodeMetaLocked(id string) map[string]string {
	node := n.findNode(id)
	out := maps.Clone(node.Meta)
	if out == nil {
		out = map[string]string{}
	}
	if m := n.place.meta[id]; m != nil {
		maps.Copy(out, m.latest())
	}
	return out
}

func stringPointers(in map[string]string) map[string]*string {
	out := make(map[string]*string, len(in))
	for k, v := range in {
		out[k] = &v
	}
	return out
}

// serveEligibility answers PUT /v1/node/{id}/eligibility.
func (n *Nomad) serveEligibility(w http.ResponseWriter, r *http.Request, id string) {
	var req api.NodeUpdateEligibilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.place.writeFail != 0 {
		http.Error(w, "Permission denied", n.place.writeFail)
		return
	}
	node := n.findNode(id)
	if node == nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	node.SchedulingEligibility = req.Eligibility
	n.place.eligibility = append(n.place.eligibility, EligibilityRequest{Node: id, Eligible: req.Eligibility == api.NodeSchedulingEligible})
	n.emitNode(id)
	n.writeJSON(w, api.NodeEligibilityUpdateResponse{})
}

// serveAllocStop answers PUT /v1/allocation/{id}/stop, Nomad's "stop and reschedule": the copy is
// told to stop (it ends at once here) and the scheduler makes a replacement of the same job and
// group, which is pending until SetStartDelay passed and sits on the node MoveCopiesTo names (or on
// none).
func (n *Nomad) serveAllocStop(w http.ResponseWriter, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.place.stopFail != 0 {
		http.Error(w, "Permission denied", n.place.stopFail)
		return
	}
	old := n.copyStub(id)
	if old == nil {
		http.Error(w, "alloc not found", http.StatusNotFound)
		return
	}
	n.place.stops = append(n.place.stops, id)
	n.allocIndex++
	list := n.allocs[old.JobID]
	stopped := *old
	stopped.DesiredStatus, stopped.ClientStatus, stopped.ClientDescription = "stop", "complete", "alloc is being migrated"
	stopped.ModifyIndex = n.allocIndex
	n.place.moved++
	repl := api.AllocationListStub{
		ID:            fmt.Sprintf("%s-moved-%d", id, n.place.moved),
		Namespace:     old.Namespace,
		Name:          old.Name,
		JobID:         old.JobID,
		JobType:       old.JobType,
		JobVersion:    old.JobVersion,
		TaskGroup:     old.TaskGroup,
		DesiredStatus: "run",
		ClientStatus:  "pending",
		CreateIndex:   n.allocIndex,
		ModifyIndex:   n.allocIndex,
		CreateTime:    time.Now().UnixNano(),
	}
	stopped.NextAllocation = repl.ID
	if n.place.previous == nil {
		n.place.previous = map[string]string{}
	}
	n.place.previous[repl.ID] = id
	if n.place.moveSet {
		repl.NodeID, repl.NodeName = n.place.moveTo, n.place.moveToName
	}
	for i, s := range list {
		if s == old {
			list[i] = &stopped
		}
	}
	n.allocs[old.JobID] = append(list, &repl)
	n.wake()
	n.emitAllocs(old.JobID)
	if n.place.moveSet {
		if d := n.place.startDelay; d > 0 {
			time.AfterFunc(d, func() { n.startReplacement(repl.JobID, repl.ID) })
		} else {
			n.runReplacement(repl.JobID, repl.ID)
		}
	}
	n.writeJSON(w, api.AllocStopResponse{EvalID: "eval-" + repl.ID})
}

func (n *Nomad) startReplacement(jobID, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.runReplacement(jobID, id)
}

// runReplacement makes the copy run and healthy. n.mu must be held.
func (n *Nomad) runReplacement(jobID, id string) {
	list := n.allocs[jobID]
	for i, s := range list {
		if s.ID != id {
			continue
		}
		n.allocIndex++
		next := *s
		next.ClientStatus = "running"
		next.ModifyIndex = n.allocIndex
		next.TaskStates = map[string]*api.TaskState{s.TaskGroup: {State: "running", StartedAt: time.Now()}}
		next.DeploymentStatus = &api.AllocDeploymentStatus{Healthy: new(true)}
		list[i] = &next
		n.wake()
		n.emitAllocs(jobID)
		return
	}
}

// servePlan answers PUT /v1/job/{id}/plan: nothing fails to place unless SetPlanFailure said so.
func (n *Nomad) servePlan(w http.ResponseWriter, r *http.Request) {
	var req api.JobPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Job == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.place.plans++
	n.place.planBody = append(n.place.planBody, req.Job)
	resp := api.JobPlanResponse{JobModifyIndex: n.index}
	for _, tg := range req.Job.TaskGroups {
		if tg.Name == nil {
			continue
		}
		if m, ok := n.place.planFail[*tg.Name]; ok {
			if resp.FailedTGAllocs == nil {
				resp.FailedTGAllocs = map[string]*api.AllocationMetric{}
			}
			m := m
			resp.FailedTGAllocs[*tg.Name] = &m
		}
	}
	n.writeJSON(w, resp)
}

// jobSummary is the summary the job list carries for a job, or nil when SetQueued set none.
// n.mu must be held.
func (n *Nomad) jobSummary(id string) *api.JobSummary {
	groups := n.place.queued[id]
	if len(groups) == 0 {
		return nil
	}
	s := &api.JobSummary{JobID: id, Namespace: "default", Summary: map[string]api.TaskGroupSummary{}}
	for g, q := range groups {
		s.Summary[g] = api.TaskGroupSummary{Queued: q}
	}
	return s
}

// isAllocStop is the route test of the stop endpoint.
func isAllocStop(r *http.Request) bool {
	return (r.Method == http.MethodPut || r.Method == http.MethodPost) &&
		strings.HasPrefix(r.URL.Path, "/v1/allocation/") && strings.HasSuffix(r.URL.Path, "/stop")
}
