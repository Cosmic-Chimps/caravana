package deploytest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// Files in a copy's allocation directory, and restarts of its tasks: the two
// ways Caravana talks to a copy's caravana-volumes task (feature 016). A test
// plays the task by hooking the restart and writing the result file.

// WriteAllocFile puts text at path in the copy's allocation directory, as the
// caravana-volumes task would (for example "alloc/data/caravana/web-1.result"
// or "caravana-volumes/local/request.env"). A leading "/" is ignored, as Nomad
// does. The copy need not exist yet; reading it needs the copy.
func (n *Nomad) WriteAllocFile(copyID, path, text string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.files[copyID] == nil {
		n.files[copyID] = map[string]string{}
	}
	n.files[copyID][strings.TrimPrefix(path, "/")] = text
}

// AllocFile returns the text at path in the copy's allocation directory, and
// whether there is a file.
func (n *Nomad) AllocFile(copyID, path string) (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	text, ok := n.files[copyID][strings.TrimPrefix(path, "/")]
	return text, ok
}

// RemoveAllocFile deletes the file, if there is one.
func (n *Nomad) RemoveAllocFile(copyID, path string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.files[copyID], strings.TrimPrefix(path, "/"))
}

// FailFS makes every file read answer status (0 undoes it), as Nomad does
// when the token's policy has no read-fs capability (403).
func (n *Nomad) FailFS(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fsStatus = status
}

// serveCat answers GET /v1/client/fs/cat/{id}?path=: the file's text, 404 when
// the copy or the file isn't there.
func (n *Nomad) serveCat(w http.ResponseWriter, r *http.Request, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fsStatus != 0 {
		http.Error(w, "Permission denied", n.fsStatus)
		return
	}
	if n.copyStub(id) == nil {
		http.Error(w, "alloc not found", http.StatusNotFound)
		return
	}
	text, ok := n.files[id][strings.TrimPrefix(r.URL.Query().Get("path"), "/")]
	if !ok {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(text))
}

// copyStub returns the allocation with this ID, or nil. n.mu must be held.
func (n *Nomad) copyStub(id string) *api.AllocationListStub {
	for _, list := range n.allocs {
		for _, s := range list {
			if s.ID == id {
				return s
			}
		}
	}
	return nil
}

// RestartRequest is one restart the fake answered.
type RestartRequest struct {
	Copy     string
	AllTasks bool
	Task     string // set when one task was asked for
}

// Restarts returns the restart requests answered so far, oldest first.
func (n *Nomad) Restarts() []RestartRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.restarts)
}

// OnRestart sets what happens when a copy's tasks are restarted: fn is called
// with the copy's ID after the restart is recorded, and the restart request
// is answered when it returns. So fn can play the caravana-volumes task
// (WriteAllocFile of the result), or block to hold the restart open. It runs
// without the fake's lock, so it may call the fake's other methods. nil
// removes the hook.
func (n *Nomad) OnRestart(fn func(copyID string)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onRestart = fn
}

// SetRestartStop sets how long a restarted copy's service task stays stopped,
// as its events tell (2 s by default): the time between its Terminated event
// and its next Started event.
func (n *Nomad) SetRestartStop(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.restartStop = d
}

// FailRestart makes every restart answer status (0 undoes it), as Nomad does
// when the token's policy has no alloc-lifecycle capability (403).
func (n *Nomad) FailRestart(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.restartStatus = status
}

// restartMessage is what Nomad's event says when the user restarts all of an
// allocation's tasks (lab finding L5, research R11).
const restartMessage = "User requested all tasks to restart"

// serveRestart answers PUT /v1/client/allocation/{id}/restart. Like Nomad, it
// gives every task of the copy a "Restart Signaled" event, and the service's
// own task (the one named like its group) a "Terminated" event and, restartStop
// later, a "Started" one. The copy changes, so its index moves and blocking
// queries wake.
func (n *Nomad) serveRestart(w http.ResponseWriter, r *http.Request, id string) {
	var req api.AllocationRestartRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	n.mu.Lock()
	if n.restartStatus != 0 {
		status := n.restartStatus
		n.mu.Unlock()
		http.Error(w, "Permission denied", status)
		return
	}
	stub := n.copyStub(id)
	if stub == nil {
		n.mu.Unlock()
		http.Error(w, "Unknown allocation", http.StatusNotFound)
		return
	}
	n.restarts = append(n.restarts, RestartRequest{Copy: id, AllTasks: req.AllTasks, Task: req.TaskName})
	n.recordRestartEvents(stub, time.Now())
	hook := n.onRestart
	n.mu.Unlock()

	if hook != nil {
		hook(id)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.writeJSON(w, struct{}{})
}

// recordRestartEvents replaces the copy's stub with one whose tasks carry the
// restart's events, and moves the allocations index. n.mu must be held.
func (n *Nomad) recordRestartEvents(stub *api.AllocationListStub, now time.Time) {
	states := make(map[string]*api.TaskState, len(stub.TaskStates)+1)
	for name, ts := range stub.TaskStates {
		if ts == nil {
			ts = &api.TaskState{}
		}
		c := *ts
		c.Events = slices.Clone(ts.Events)
		states[name] = &c
	}
	if states[stub.TaskGroup] == nil {
		states[stub.TaskGroup] = &api.TaskState{State: "running"}
	}
	for name, ts := range states {
		ts.Events = append(ts.Events, &api.TaskEvent{Type: api.TaskRestartSignal, Time: now.UnixNano(),
			DisplayMessage: restartMessage})
		if name == stub.TaskGroup {
			ts.Events = append(ts.Events,
				&api.TaskEvent{Type: api.TaskTerminated, Time: now.UnixNano(), DisplayMessage: "Exit Code: 0"},
				&api.TaskEvent{Type: api.TaskStarted, Time: now.Add(n.restartStop).UnixNano(), DisplayMessage: "Task started by client"})
		}
	}

	n.allocIndex++
	next := *stub
	next.TaskStates = states
	next.ModifyIndex = n.allocIndex
	list := n.allocs[stub.JobID]
	for i, s := range list {
		if s == stub {
			list[i] = &next
		}
	}
	n.wake()
}

// AddHostVolume makes a ready host volume called name, as a deploy would, and
// returns its path on the node. A volume with that name is returned as it is.
func (n *Nomad) AddHostVolume(name string) (hostPath string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, v := range n.volumes {
		if v.vol.Name == name {
			return v.vol.HostPath
		}
	}
	n.index++
	id := fmt.Sprintf("vol-%d", n.index)
	v := api.HostVolume{
		ID: id, Name: name, Namespace: "default", PluginID: "mkdir", NodeID: "node-1",
		HostPath: "/opt/nomad/data/host_volumes/" + id, State: api.HostVolumeStateReady,
	}
	n.volumes[id] = &hostVolume{vol: v, reads: 2}
	return v.HostPath
}
