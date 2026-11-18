package deploytest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// The event stream (GET /v1/event/stream): newline-delimited JSON messages
// {"Index":n,"Events":[...]}, flushed one by one, with "{}" heartbeats about
// every second. The fake keeps its last 100 events, replays those with an
// index at or above the one a request asks for, then sends new ones as they
// happen, so a client that reconnects with its last index plus one loses
// nothing it could lose with a real Nomad.
//
// The fake emits what the service listens to, from the setters that change it:
//   - Allocation events when a job's copies change (SetCopies, a stop, a run's
//     life, a restart, a purge), with payload {"Allocation":{"ID","JobID"}};
//   - Deployment events when a rollout's state changes (Script, Default, a new
//     version, a cancelled one, SetDeployment, ClearDeployment, and each
//     deployments read that moves a script to a different state), with payload
//     {"Deployment":{"ID","JobID","JobVersion","Status"}};
//   - Job events when a job is registered, stopped, purged or ends (Key is the
//     job ID), with payload {"Job":{"ID"}};
//   - Node events from the node setters, with payload {"Node":{"ID"}}.
// Down closes the open streams and refuses new ones. EventStreamRequests counts
// the connections opened.

// eventBuffer is how many events the fake remembers for a client that comes
// back, like Nomad's in-memory buffer (which holds many more).
const eventBuffer = 100

// eventHeartbeat is the gap between the "{}" messages that keep a stream alive.
const eventHeartbeat = time.Second

// bufferedEvent is one event with the index the fake gave it.
type bufferedEvent struct {
	index uint64
	event streamEvent
}

// streamEvent is the wire form of api.Event, which the fake writes itself.
type streamEvent struct {
	Topic   string
	Type    string
	Key     string
	Index   uint64
	Payload map[string]any
}

// eventFake is the fake's event log. The Nomad mutex guards it.
type eventFake struct {
	index    uint64
	buf      []bufferedEvent
	changed  chan struct{} // closed, then replaced, on every event and on Down/Up
	requests int
	refuse   int // non-zero: every stream request answers it
}

// emit adds an event to the log and wakes the open streams. n.mu must be held.
func (n *Nomad) emit(topic, typ, key string, payload map[string]any) {
	n.ev.index++
	n.ev.buf = append(n.ev.buf, bufferedEvent{index: n.ev.index, event: streamEvent{
		Topic: topic, Type: typ, Key: key, Index: n.ev.index, Payload: payload}})
	if len(n.ev.buf) > eventBuffer {
		n.ev.buf = n.ev.buf[len(n.ev.buf)-eventBuffer:]
	}
	n.wakeEvents()
}

// wakeEvents releases the open streams so they look again. n.mu must be held.
func (n *Nomad) wakeEvents() {
	if n.ev.changed == nil {
		n.ev.changed = make(chan struct{})
	}
	close(n.ev.changed)
	n.ev.changed = make(chan struct{})
}

// emitAllocs says jobID's copies changed: one event per copy it has, or one
// with no copy ID when it has none left. n.mu must be held.
func (n *Nomad) emitAllocs(jobID string) {
	list := n.allocs[jobID]
	if len(list) == 0 {
		n.emit(string(api.TopicAllocation), "AllocationUpdated", jobID, map[string]any{
			"Allocation": map[string]any{"ID": "", "JobID": jobID}})
		return
	}
	for _, s := range list {
		n.emit(string(api.TopicAllocation), "AllocationUpdated", s.ID, map[string]any{
			"Allocation": map[string]any{"ID": s.ID, "JobID": jobID}})
	}
}

// emitJob says a job was registered, stopped, purged or changed. n.mu must be held.
func (n *Nomad) emitJob(jobID, typ string) {
	n.emit(string(api.TopicJob), typ, jobID, map[string]any{"Job": map[string]any{"ID": jobID}})
}

// emitDeployment says the rollout of jobID at version is now at status. n.mu must be held.
func (n *Nomad) emitDeployment(jobID string, version uint64, status string) {
	id := jobID + "-deployment-" + strconv.FormatUint(version, 10)
	n.emit(string(api.TopicDeployment), "DeploymentStatusUpdate", id, map[string]any{
		"Deployment": map[string]any{"ID": id, "JobID": jobID, "JobVersion": version, "Status": status}})
}

// emitNode says a node changed. n.mu must be held.
func (n *Nomad) emitNode(id string) {
	n.emit(string(api.TopicNode), "NodeEvent", id, map[string]any{"Node": map[string]any{"ID": id}})
}

// RefuseEventStream makes every event stream request answer status (0 undoes
// it), as a token without the topics' read capability would (403): Caravana
// then has to manage with its slow polls alone.
func (n *Nomad) RefuseEventStream(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ev.refuse = status
}

// EventStreamRequests counts the event stream connections opened so far.
func (n *Nomad) EventStreamRequests() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ev.requests
}

// serveEvents answers GET /v1/event/stream.
func (n *Nomad) serveEvents(w http.ResponseWriter, r *http.Request) {
	var index uint64
	if v := r.URL.Query().Get("index"); v != "" {
		index, _ = strconv.ParseUint(v, 10, 64)
	}
	topics := map[string]bool{}
	for _, t := range r.URL.Query()["topic"] {
		name, _, _ := strings.Cut(t, ":")
		topics[name] = true
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	n.mu.Lock()
	n.ev.requests++
	if n.ev.refuse != 0 {
		status := n.ev.refuse
		n.mu.Unlock()
		http.Error(w, "Permission denied", status)
		return
	}
	next := index // events at or above this index are still to be sent
	if index == 0 {
		next = n.ev.index + 1 // from now on; a real Nomad replays its buffer, which tests needn't see
	}
	n.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{}) // a first heartbeat, so the client sees the headers at once
	flusher.Flush()

	beat := time.NewTicker(eventHeartbeat)
	defer beat.Stop()
	for {
		n.mu.Lock()
		if n.down {
			n.mu.Unlock()
			return
		}
		var events []streamEvent
		var last uint64
		for _, b := range n.ev.buf {
			if b.index < next {
				continue
			}
			last = b.index
			if len(topics) == 0 || topics[b.event.Topic] {
				events = append(events, b.event)
			}
		}
		if last >= next {
			next = last + 1
		}
		if n.ev.changed == nil {
			n.ev.changed = make(chan struct{})
		}
		changed := n.ev.changed
		n.mu.Unlock()

		if len(events) > 0 {
			if err := enc.Encode(map[string]any{"Index": last, "Events": events}); err != nil {
				return
			}
			flusher.Flush()
		}
		select {
		case <-changed:
		case <-beat.C:
			if err := enc.Encode(map[string]any{}); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
