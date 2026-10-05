// Package deploytest is a fake Nomad for tests: a real HTTP server that
// answers the endpoints Caravana's deploy flow uses, so tests exercise
// the official Nomad client end to end. Product code never imports it.
//
// Rollouts are scripted: Script(version, states...) makes each deployments
// request for that job version return the next state, the last one repeating.
// Like Nomad, registering a newer version cancels every older rollout that
// hasn't ended.
//
// Allocations ("copies") are scripted too: SetCopies(jobID, stubs...) replaces a
// job's allocations and moves the allocations index, which blocking queries on
// GET /v1/allocations wait on.
//
// Deleting a job (DELETE /v1/job/{id}?purge=true) removes it with its rollouts
// and allocations; Purged(jobID) says it was done.
//
// Task output ("logs") is scripted with WriteLog(copyID, stream, text). The
// fake answers GET /v1/client/fs/logs/{id} the way a Nomad client does: a stream
// of JSON frames, each carrying a chunk of the file and the offset after it,
// with "{}" heartbeats while following.
package deploytest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// Nomad is a fake Nomad server. Its zero value isn't usable; call New.
type Nomad struct {
	srv *httptest.Server

	mu            sync.Mutex
	index         uint64
	jobs          map[string][]*api.Job // versions, oldest first
	scripts       map[string]map[uint64][]api.Deployment
	defaultStates []api.Deployment
	rejectStatus  int
	rejectBody    string
	failVersions  int
	token         string
	down          bool
	registrations int
	warnings      string
	purged        map[string]bool

	// Allocations have their own index, so the deployments endpoint's
	// bookkeeping can't wake a blocking query that waits for allocations.
	allocs        map[string][]*api.AllocationListStub // by job ID
	allocIndex    uint64
	allocChanged  chan struct{} // closed, then replaced, on every change (and on Down/Up)
	allocRequests int           // GET /v1/allocations requests answered

	// Task output, by copy ID and stream. logChanged is closed, then
	// replaced, on every write (and on Down), like allocChanged.
	logs         map[logKey][]byte
	logChanged   chan struct{}
	logFrameSize int // 0 = one frame per read
	logRequests  []LogRequest
	logOpen      int // follow streams still connected
}

// logKey names one output file: a copy's stdout or stderr.
type logKey struct{ copyID, stream string }

// LogRequest is one request the fake answered for task output.
type LogRequest struct {
	Copy, Task, Stream, Origin string
	Offset                     int64
	Follow                     bool
}

// New starts a fake Nomad that closes when the test ends.
func New(t testing.TB) *Nomad {
	n := &Nomad{
		index:   1,
		jobs:    map[string][]*api.Job{},
		scripts: map[string]map[uint64][]api.Deployment{},
		purged:  map[string]bool{},

		allocs:       map[string][]*api.AllocationListStub{},
		allocIndex:   1,
		allocChanged: make(chan struct{}),

		logs:       map[logKey][]byte{},
		logChanged: make(chan struct{}),
	}
	n.srv = httptest.NewServer(http.HandlerFunc(n.serve))
	t.Cleanup(n.srv.Close)
	return n
}

// Addr is the server's address, for NOMAD_ADDR.
func (n *Nomad) Addr() string { return n.srv.URL }

// Script sets the rollout states of jobID at jobVersion. Only Status and
// StatusDescription need to be set; the rest is filled in.
func (n *Nomad) Script(jobID string, jobVersion uint64, states ...api.Deployment) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.scripts[jobID] == nil {
		n.scripts[jobID] = map[uint64][]api.Deployment{}
	}
	n.scripts[jobID][jobVersion] = states
}

// Default sets the rollout states for every registered version without a
// script of its own. With no default, such a version never gets a deployment.
func (n *Nomad) Default(states ...api.Deployment) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.defaultStates = states
}

// Succeed is the usual default: the rollout is running once, then successful.
func (n *Nomad) Succeed() {
	n.Default(
		api.Deployment{Status: api.DeploymentStatusRunning, StatusDescription: "Deployment is running"},
		api.Deployment{Status: api.DeploymentStatusSuccessful, StatusDescription: "Deployment completed successfully"},
	)
}

// Rolling is a rollout state that is running and carries the counts of its
// task groups, for tests of rollout progress. description is Nomad's status
// description.
func Rolling(description string, groups map[string]api.DeploymentState) api.Deployment {
	d := api.Deployment{Status: api.DeploymentStatusRunning, StatusDescription: description}
	if len(groups) > 0 {
		d.TaskGroups = make(map[string]*api.DeploymentState, len(groups))
		for name, g := range groups {
			d.TaskGroups[name] = &g // a copy: g is a fresh variable on each pass
		}
	}
	return d
}

// AddForeignJob stores a job as if someone registered it outside Caravana.
func (n *Nomad) AddForeignJob(job *api.Job) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.store(job)
}

// RejectNext makes the next register request answer status with body.
func (n *Nomad) RejectNext(status int, body string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rejectStatus, n.rejectBody = status, body
}

// FailVersions makes the next count versions requests answer 500.
func (n *Nomad) FailVersions(count int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failVersions = count
}

// RegisterWarnings sets the Warnings string register responses carry.
func (n *Nomad) RegisterWarnings(w string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.warnings = w
}

// RequireToken makes every request need X-Nomad-Token: token (403 otherwise).
func (n *Nomad) RequireToken(token string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.token = token
}

// Down makes the server drop every connection, as if Nomad were unreachable.
func (n *Nomad) Down() { n.setDown(true) }

// Up undoes Down.
func (n *Nomad) Up() { n.setDown(false) }

func (n *Nomad) setDown(v bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.down = v
	n.wake() // a blocking query that is waiting re-checks and drops its connection
	n.wakeLogs()
}

// Registrations counts successful register requests.
func (n *Nomad) Registrations() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.registrations
}

// Job returns the latest version of jobID, or nil.
func (n *Nomad) Job(jobID string) *api.Job {
	n.mu.Lock()
	defer n.mu.Unlock()
	versions := n.jobs[jobID]
	if len(versions) == 0 {
		return nil
	}
	return versions[len(versions)-1]
}

// store adds job as the next version of its ID. n.mu must be held.
func (n *Nomad) store(job *api.Job) *api.Job {
	n.index++
	j := *job
	versions := n.jobs[*j.ID]
	version := uint64(len(versions))
	j.Version = &version
	j.JobModifyIndex = new(n.index)
	j.ModifyIndex = new(n.index)
	if len(versions) == 0 {
		j.CreateIndex = new(n.index)
	} else {
		j.CreateIndex = versions[0].CreateIndex
	}
	if j.Stop == nil {
		j.Stop = new(false)
	}
	n.jobs[*j.ID] = append(versions, &j)
	return &j
}

func (n *Nomad) serve(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	down, token := n.down, n.token
	n.mu.Unlock()
	if down {
		drop(w)
		return
	}
	if token != "" && r.Header.Get("X-Nomad-Token") != token {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
	}

	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
		n.register(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/job/"):
		n.purge(w, r, strings.TrimPrefix(r.URL.Path, "/v1/job/"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/fs/logs/"):
		n.serveLogs(w, r, strings.TrimPrefix(r.URL.Path, "/v1/client/fs/logs/"))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/allocations":
		n.allocations(w, r, "")
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/allocation/"):
		n.allocation(w, strings.TrimPrefix(r.URL.Path, "/v1/allocation/"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/job/"):
		rest := strings.TrimPrefix(r.URL.Path, "/v1/job/")
		id, sub, _ := strings.Cut(rest, "/")
		switch sub {
		case "":
			n.info(w, id)
		case "versions":
			n.versions(w, id)
		case "deployments":
			n.deployments(w, id)
		case "allocations":
			n.allocations(w, r, id)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (n *Nomad) register(w http.ResponseWriter, r *http.Request) {
	var req api.JobRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Job == nil || req.Job.ID == nil {
		http.Error(w, "bad register request", http.StatusBadRequest)
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.rejectStatus != 0 {
		status, body := n.rejectStatus, n.rejectBody
		n.rejectStatus, n.rejectBody = 0, ""
		http.Error(w, body, status)
		return
	}
	if req.EnforceIndex {
		var current uint64
		if versions := n.jobs[*req.Job.ID]; len(versions) > 0 {
			current = *versions[len(versions)-1].JobModifyIndex
		}
		if current != req.JobModifyIndex {
			http.Error(w, fmt.Sprintf("Enforcing job modify index %d: job exists with conflicting job modify index: %d",
				req.JobModifyIndex, current), http.StatusInternalServerError)
			return
		}
	}
	j := n.store(req.Job)
	n.cancelOlder(*j.ID, *j.Version)
	n.registrations++
	n.writeJSON(w, api.JobRegisterResponse{
		EvalID:         fmt.Sprintf("eval-%d", n.index),
		JobModifyIndex: *j.JobModifyIndex,
		Warnings:       n.warnings,
	})
}

// Purged says whether jobID was deleted with purge=true. It stays true after
// the job is gone, which is how a test knows the delete reached Nomad.
func (n *Nomad) Purged(jobID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.purged[jobID]
}

// purge answers DELETE /v1/job/{id}. Like Nomad, a job it doesn't have is a
// 404. Without purge=true the job would only be stopped, which Caravana never
// asks for, so the fake refuses it instead of pretending.
func (n *Nomad) purge(w http.ResponseWriter, r *http.Request, id string) {
	if r.URL.Query().Get("purge") != "true" {
		http.Error(w, "the fake only purges: purge=true is required", http.StatusBadRequest)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.jobs[id]) == 0 {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	n.index++
	delete(n.jobs, id)
	delete(n.scripts, id)
	if len(n.allocs[id]) > 0 {
		delete(n.allocs, id)
		n.allocIndex++
		n.wake()
	}
	n.purged[id] = true
	n.writeJSON(w, api.JobDeregisterResponse{EvalID: fmt.Sprintf("eval-%d", n.index), JobModifyIndex: n.index})
}

func (n *Nomad) info(w http.ResponseWriter, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	versions := n.jobs[id]
	if len(versions) == 0 {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	n.writeJSON(w, versions[len(versions)-1])
}

func (n *Nomad) versions(w http.ResponseWriter, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failVersions > 0 {
		n.failVersions--
		http.Error(w, "scripted versions failure", http.StatusInternalServerError)
		return
	}
	versions := n.jobs[id]
	if len(versions) == 0 {
		http.Error(w, "job versions not found", http.StatusNotFound)
		return
	}
	resp := api.JobVersionsResponse{}
	for i := len(versions) - 1; i >= 0; i-- { // newest first, like Nomad
		resp.Versions = append(resp.Versions, versions[i])
	}
	n.writeJSON(w, resp)
}

// cancelOlder does what Nomad does when a newer job version registers: every
// older version's rollout that hasn't ended is cancelled. Versions that never
// had a rollout keep having none. n.mu must be held.
func (n *Nomad) cancelOlder(jobID string, newest uint64) {
	for v := uint64(0); v < newest; v++ {
		states, ok := n.scripts[jobID][v]
		if !ok {
			states = n.defaultStates
		}
		if len(states) == 0 {
			continue
		}
		switch states[0].Status {
		case api.DeploymentStatusSuccessful, api.DeploymentStatusFailed, api.DeploymentStatusCancelled:
			continue
		}
		if n.scripts[jobID] == nil {
			n.scripts[jobID] = map[uint64][]api.Deployment{}
		}
		n.scripts[jobID][v] = []api.Deployment{{
			Status:            api.DeploymentStatusCancelled,
			StatusDescription: "Cancelled due to newer version of job",
		}}
	}
}

// deployments returns one deployment per registered version that has
// states, each at its current step, and moves every script one step on.
func (n *Nomad) deployments(w http.ResponseWriter, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.index++
	out := []*api.Deployment{}
	for _, job := range n.jobs[id] {
		v := *job.Version
		states, ok := n.scripts[id][v]
		if !ok {
			states = n.defaultStates
		}
		if len(states) == 0 {
			continue
		}
		d := states[0]
		// Each version keeps its own copy of the remaining steps, so a
		// default script runs once per version. The last step repeats.
		if n.scripts[id] == nil {
			n.scripts[id] = map[uint64][]api.Deployment{}
		}
		if len(states) > 1 {
			n.scripts[id][v] = states[1:]
		} else {
			n.scripts[id][v] = states
		}
		d.ID = id + "-deployment-" + strconv.FormatUint(v, 10)
		d.JobID = id
		d.JobVersion = v
		out = append(out, &d)
	}
	n.writeJSON(w, out)
}

// writeJSON answers with v and the query headers the Nomad client requires.
// n.mu must be held.
func (n *Nomad) writeJSON(w http.ResponseWriter, v any) {
	n.writeJSONAt(w, n.index, v)
}

// writeJSONAt is writeJSON with the index the answer reports. n.mu must be
// held.
func (n *Nomad) writeJSONAt(w http.ResponseWriter, index uint64, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Nomad-Index", strconv.FormatUint(index, 10))
	w.Header().Set("X-Nomad-LastContact", "0")
	w.Header().Set("X-Nomad-KnownLeader", "true")
	_ = json.NewEncoder(w).Encode(v)
}

// drop closes the connection without an answer: the client sees a network
// error, as when Nomad is unreachable.
func drop(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
	w.WriteHeader(http.StatusBadGateway)
}

// wake releases every blocking query so it looks again. n.mu must be held.
func (n *Nomad) wake() {
	close(n.allocChanged)
	n.allocChanged = make(chan struct{})
}

// SetCopies replaces jobID's allocations with allocs and moves the
// allocations index, which wakes blocking queries. Fields left empty are
// filled in: ID ("<job>-alloc-<position>", so the same position keeps its ID
// from one call to the next), JobID, Namespace and the indexes.
//
// Like Nomad, an allocation's ModifyIndex moves only when it changed: setting
// the same stubs again changes nothing and doesn't wake anyone. Calling it
// with no allocs removes them all.
func (n *Nomad) SetCopies(jobID string, allocs ...api.AllocationListStub) {
	n.mu.Lock()
	defer n.mu.Unlock()

	old := map[string]*api.AllocationListStub{}
	for _, a := range n.allocs[jobID] {
		old[a.ID] = a
	}
	next := n.allocIndex + 1
	out := make([]*api.AllocationListStub, 0, len(allocs))
	for i := range allocs {
		a := allocs[i]
		if a.ID == "" {
			a.ID = fmt.Sprintf("%s-alloc-%d", jobID, i)
		}
		a.JobID = jobID
		if a.Namespace == "" {
			a.Namespace = "default"
		}
		a.CreateIndex, a.ModifyIndex = 0, 0
		if prev, ok := old[a.ID]; ok {
			p := *prev
			p.CreateIndex, p.ModifyIndex = 0, 0
			if reflect.DeepEqual(p, a) {
				out = append(out, prev) // unchanged: keep its indexes
				continue
			}
			a.CreateIndex = prev.CreateIndex
		} else {
			a.CreateIndex = next
		}
		a.ModifyIndex = next
		out = append(out, &a)
	}

	changed := len(out) != len(n.allocs[jobID])
	for i := range out {
		if !changed && out[i] != n.allocs[jobID][i] {
			changed = true
		}
	}
	if len(out) == 0 {
		delete(n.allocs, jobID)
	} else {
		n.allocs[jobID] = out
	}
	if changed {
		n.allocIndex = next
		n.wake()
	}
}

// AllocationRequests counts the GET /v1/allocations requests answered, for
// tests that check a watcher has stopped asking.
func (n *Nomad) AllocationRequests() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.allocRequests
}

// allocations answers GET /v1/allocations (every job's, jobID "") and
// GET /v1/job/{id}/allocations, with Nomad's blocking-query rule: when the
// request's index is older than the allocations index, answer at once;
// otherwise wait for a change or for the wait time, then answer.
func (n *Nomad) allocations(w http.ResponseWriter, r *http.Request, jobID string) {
	var index uint64
	if v := r.URL.Query().Get("index"); v != "" {
		index, _ = strconv.ParseUint(v, 10, 64)
	}
	wait := 5 * time.Minute // Nomad's default
	if v := r.URL.Query().Get("wait"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			wait = d
		}
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()

	for {
		n.mu.Lock()
		if n.down {
			n.mu.Unlock()
			drop(w)
			return
		}
		if index == 0 || n.allocIndex > index {
			n.answerAllocations(w, jobID)
			n.mu.Unlock()
			return
		}
		changed := n.allocChanged
		n.mu.Unlock()

		select {
		case <-changed:
		case <-deadline.C:
			n.mu.Lock()
			down := n.down
			if !down {
				n.answerAllocations(w, jobID)
			}
			n.mu.Unlock()
			if down {
				drop(w)
			}
			return
		case <-r.Context().Done():
			return
		}
	}
}

// answerAllocations writes the stubs of jobID (every job's when ""). n.mu
// must be held.
func (n *Nomad) answerAllocations(w http.ResponseWriter, jobID string) {
	stubs := []*api.AllocationListStub{}
	if jobID != "" {
		stubs = append(stubs, n.allocs[jobID]...)
	} else {
		n.allocRequests++
		for _, list := range n.allocs {
			stubs = append(stubs, list...)
		}
	}
	n.writeJSONAt(w, n.allocIndex, stubs)
}

// allocation answers GET /v1/allocation/{id} with a full allocation built
// from the stub.
func (n *Nomad) allocation(w http.ResponseWriter, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, list := range n.allocs {
		for _, s := range list {
			if s.ID != id {
				continue
			}
			n.writeJSONAt(w, n.allocIndex, &api.Allocation{
				ID:                    s.ID,
				Namespace:             s.Namespace,
				EvalID:                s.EvalID,
				Name:                  s.Name,
				NodeID:                s.NodeID,
				NodeName:              s.NodeName,
				JobID:                 s.JobID,
				TaskGroup:             s.TaskGroup,
				DesiredStatus:         s.DesiredStatus,
				DesiredDescription:    s.DesiredDescription,
				ClientStatus:          s.ClientStatus,
				ClientDescription:     s.ClientDescription,
				TaskStates:            s.TaskStates,
				DeploymentStatus:      s.DeploymentStatus,
				FollowupEvalID:        s.FollowupEvalID,
				PreemptedAllocations:  s.PreemptedAllocations,
				PreemptedByAllocation: s.PreemptedByAllocation,
				CreateIndex:           s.CreateIndex,
				ModifyIndex:           s.ModifyIndex,
				AllocModifyIndex:      s.ModifyIndex,
				CreateTime:            s.CreateTime,
				ModifyTime:            s.ModifyTime,
			})
			return
		}
	}
	http.Error(w, "alloc not found", http.StatusNotFound)
}

// wakeLogs releases every followed log stream so it looks again. n.mu must be
// held.
func (n *Nomad) wakeLogs() {
	close(n.logChanged)
	n.logChanged = make(chan struct{})
}

// WriteLog appends text to a copy's output. stream is "stdout" or "stderr".
// Streams that follow the output send it at once; text is written as given,
// so a line ends only where text has its own newline.
func (n *Nomad) WriteLog(copyID, stream, text string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	k := logKey{copyID, stream}
	n.logs[k] = append(n.logs[k], text...)
	n.wakeLogs()
}

// SetLogFrameSize makes every log frame at most size bytes, so tests can split
// lines (and characters) across frames. 0, the default, sends what there is in
// one frame.
func (n *Nomad) SetLogFrameSize(size int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.logFrameSize = size
}

// LogRequests returns the log requests answered so far, oldest first.
func (n *Nomad) LogRequests() []LogRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]LogRequest(nil), n.logRequests...)
}

// OpenLogStreams counts the followed log streams still connected, for tests
// that check a cancelled reader let go of its connection.
func (n *Nomad) OpenLogStreams() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.logOpen
}

// logHeartbeat is how often a followed stream sends "{}" when idle. Nomad's
// is 10 s; the fake's is short so a reader that never notices them is caught.
const logHeartbeat = 50 * time.Millisecond

// serveLogs answers GET /v1/client/fs/logs/{id}: Nomad's frame stream. The
// query says which task and stream (type), where to start (origin "end" with
// an offset means "that many bytes before the end", "start" an absolute
// position) and whether to follow. Without follow the stream ends at the end
// of the output; with it, new output is sent as it is written. Each frame
// carries the file's name and the offset after its data, as Nomad's do (the
// offset is where the NEXT byte is, not where this data began).
func (n *Nomad) serveLogs(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	req := LogRequest{Copy: id, Task: q.Get("task"), Stream: q.Get("type"), Origin: q.Get("origin"),
		Follow: q.Get("follow") == "true"}
	req.Offset, _ = strconv.ParseInt(q.Get("offset"), 10, 64)

	n.mu.Lock()
	known := false
	for _, list := range n.allocs {
		for _, a := range list {
			known = known || a.ID == id
		}
	}
	if !known {
		n.mu.Unlock()
		http.Error(w, "alloc not found", http.StatusNotFound)
		return
	}
	if req.Stream != "stdout" && req.Stream != "stderr" {
		n.mu.Unlock()
		http.Error(w, "log type must be stdout or stderr", http.StatusBadRequest)
		return
	}
	n.logRequests = append(n.logRequests, req)
	key := logKey{id, req.Stream}
	size := len(n.logs[key])
	var pos int
	switch req.Origin {
	case "end":
		pos = max(size-int(req.Offset), 0)
	case "start":
		pos = min(int(req.Offset), size)
	default:
		n.mu.Unlock()
		http.Error(w, "invalid origin", http.StatusBadRequest)
		return
	}
	if req.Follow {
		n.logOpen++
		defer func() {
			n.mu.Lock()
			n.logOpen--
			n.mu.Unlock()
		}()
	}
	n.mu.Unlock()

	file := "alloc/logs/" + req.Task + "." + req.Stream + ".0"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(v any) bool {
		if enc.Encode(v) != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	heartbeat := time.NewTicker(logHeartbeat)
	defer heartbeat.Stop()
	for {
		n.mu.Lock()
		down, frameSize := n.down, n.logFrameSize
		data := append([]byte(nil), n.logs[key][min(pos, len(n.logs[key])):]...)
		changed := n.logChanged
		n.mu.Unlock()
		if down {
			drop(w)
			return
		}

		for len(data) > 0 {
			chunk := data
			if frameSize > 0 && len(chunk) > frameSize {
				chunk = chunk[:frameSize]
			}
			data = data[len(chunk):]
			pos += len(chunk)
			if !send(api.StreamFrame{Offset: int64(pos), Data: chunk, File: file}) {
				return
			}
		}
		if !req.Follow {
			return
		}

	wait:
		for {
			select {
			case <-changed:
				break wait
			case <-heartbeat.C:
				if !send(struct{}{}) {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	}
}
