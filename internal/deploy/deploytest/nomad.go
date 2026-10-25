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
//
// Variables (secrets) are kept in memory: GET, PUT and DELETE /v1/var/{path}
// and GET /v1/vars?prefix=. Like Nomad, the fake refuses a variable with no
// items, one over 64 KiB, or a path Nomad wouldn't accept. Variable(path) and
// SetVariable(path, items) let tests read and change them directly.
//
// Allocation stats: SetStats(allocID, usage) sets what
// GET /v1/client/allocation/{id}/stats answers (404 until set), SetStatsDelay
// holds each answer back, and StatsRequests and MaxStatsInFlight report how
// they were asked.
//
// Dynamic host volumes are kept in memory too: PUT /v1/volume/host/create,
// GET and DELETE /v1/volume/host/{id}, GET /v1/volumes?type=host&prefix=. A
// new volume is "pending" and becomes "ready" the second time it is read, as
// a real one is after a few seconds. Like Nomad, a name can't be created
// twice, and a volume can't be deleted while copies of a job that mounts it
// exist. HostVolumes() names them for tests, and AddHostVolume makes one as if
// a deploy had.
//
// Copies' files and restarts (allocfs.go), for volume backups: files in a
// copy's directory are kept per copy and path (WriteAllocFile, AllocFile) and
// read the way GET /v1/client/fs/cat does. Restarting a copy's tasks
// (PUT /v1/client/allocation/{id}/restart) is recorded (Restarts), adds the
// task events Nomad does, and runs the hook OnRestart sets, which is how a test
// plays the caravana-volumes task: it writes the result file there.
package deploytest

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
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
	stops         map[string]int // deregisters without purge, by job ID

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

	// The rotated log files of copies (logfiles.go): by copy and stream, then
	// file index.
	logFS        map[logKey]map[int]*logFile
	fileRequests []FileRequest

	evals      []string                     // job ID of each eval forcePeriodic made, by index
	vars       map[string]map[string]string // variable items, by path
	varsStatus int                          // non-zero: every variables request answers it

	stats         map[string]api.AllocResourceUsage // by copy ID
	statsDelay    time.Duration
	statsRequests int
	statsInFlight int
	statsMax      int

	volumes      map[string]*hostVolume // by ID
	volumeStatus int                    // non-zero: every host volume request answers it

	// Copies' files and restarts (allocfs.go).
	files         map[string]map[string]string // text by path, by copy ID
	fsStatus      int                          // non-zero: every file read answers it
	restarts      []RestartRequest
	restartStatus int // non-zero: every restart answers it
	onRestart     func(copyID string)
	restartStop   time.Duration // how long a restarted main task stays stopped

	// Commands run in copies, and the checks of copies (exec.go).
	execs      []ExecRequest
	execStatus int // non-zero: every exec answers it before the upgrade
	onExec     func(ExecRequest) ExecReply
	checks     map[string]api.AllocCheckStatuses // by copy ID
}

// hostVolume is one dynamic host volume and how often it was read.
type hostVolume struct {
	vol   api.HostVolume
	reads int
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
		stops:   map[string]int{},

		allocs:       map[string][]*api.AllocationListStub{},
		allocIndex:   1,
		allocChanged: make(chan struct{}),

		logs:       map[logKey][]byte{},
		logChanged: make(chan struct{}),

		vars:    map[string]map[string]string{},
		stats:   map[string]api.AllocResourceUsage{},
		volumes: map[string]*hostVolume{},

		files:       map[string]map[string]string{},
		restartStop: 2 * time.Second,
		checks:      map[string]api.AllocCheckStatuses{},
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

	n.mu.Lock()
	varsStatus := n.varsStatus
	n.mu.Unlock()
	isVars := strings.HasPrefix(r.URL.Path, "/v1/var/") || r.URL.Path == "/v1/vars"
	switch {
	case isVars && varsStatus != 0:
		http.Error(w, "Permission denied", varsStatus)
	case strings.HasPrefix(r.URL.Path, "/v1/volume/host/") || r.URL.Path == "/v1/volumes":
		n.hostVolume(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/var/"):
		n.variable(w, r, strings.TrimPrefix(r.URL.Path, "/v1/var/"))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/vars":
		n.listVariables(w, r.URL.Query().Get("prefix"))
	case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
		n.register(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/job/"):
		n.purge(w, r, strings.TrimPrefix(r.URL.Path, "/v1/job/"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/fs/logs/"):
		n.serveLogs(w, r, strings.TrimPrefix(r.URL.Path, "/v1/client/fs/logs/"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/fs/cat/"):
		n.serveCat(w, r, strings.TrimPrefix(r.URL.Path, "/v1/client/fs/cat/"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/fs/ls/"):
		n.serveLs(w, r, strings.TrimPrefix(r.URL.Path, "/v1/client/fs/ls/"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/fs/stat/"):
		n.serveStat(w, r, strings.TrimPrefix(r.URL.Path, "/v1/client/fs/stat/"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/fs/readat/"):
		n.serveReadAt(w, r, strings.TrimPrefix(r.URL.Path, "/v1/client/fs/readat/"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/allocation/") && strings.HasSuffix(r.URL.Path, "/exec"):
		n.serveExec(w, r, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/client/allocation/"), "/exec"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/allocation/") && strings.HasSuffix(r.URL.Path, "/checks"):
		n.serveChecks(w, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/client/allocation/"), "/checks"))
	case (r.Method == http.MethodPut || r.Method == http.MethodPost) && strings.HasPrefix(r.URL.Path, "/v1/client/allocation/") && strings.HasSuffix(r.URL.Path, "/restart"):
		n.serveRestart(w, r, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/client/allocation/"), "/restart"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/client/allocation/") && strings.HasSuffix(r.URL.Path, "/stats"):
		n.serveStats(w, r, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/client/allocation/"), "/stats"))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/allocations":
		n.allocations(w, r, "")
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/allocation/"):
		n.allocation(w, strings.TrimPrefix(r.URL.Path, "/v1/allocation/"))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs":
		n.listJobs(w, r.URL.Query().Get("prefix"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/evaluation/"):
		n.evaluation(w, strings.TrimPrefix(r.URL.Path, "/v1/evaluation/"))
	case (r.Method == http.MethodPut || r.Method == http.MethodPost) && strings.HasSuffix(r.URL.Path, "/periodic/force"):
		id, _ := jobPath(r)
		n.forcePeriodic(w, strings.TrimSuffix(id, "/periodic/force"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/job/"):
		// Job IDs of periodic runs contain "/", which the client sends
		// escaped: split on the escaped path, then unescape the ID.
		id, sub := jobPath(r)
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
// 404. With purge=true the job is erased. Without it the job is only stopped,
// as "nomad job stop" does (feature 020.5): its latest version keeps its place
// with Stop set, and its copies end (client status "complete"). Stops(jobID)
// counts the stops that reached the fake.
func (n *Nomad) purge(w http.ResponseWriter, r *http.Request, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.jobs[id]) == 0 {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	n.index++
	if r.URL.Query().Get("purge") != "true" {
		versions := n.jobs[id]
		stopped := *versions[len(versions)-1]
		stopped.Stop = new(true)
		stopped.JobModifyIndex = new(n.index)
		stopped.ModifyIndex = new(n.index)
		versions[len(versions)-1] = &stopped
		n.stopCopies(id)
		n.stops[id]++
		n.writeJSON(w, api.JobDeregisterResponse{EvalID: fmt.Sprintf("eval-%d", n.index), JobModifyIndex: n.index})
		return
	}
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

// stopCopies ends every copy of the job, as Nomad does when it is stopped.
// n.mu must be held.
func (n *Nomad) stopCopies(jobID string) {
	list := n.allocs[jobID]
	if len(list) == 0 {
		return
	}
	n.allocIndex++
	for i, s := range list {
		next := *s
		if killed := n.killedRunStates(jobID, s); killed != nil {
			next.TaskStates = killed
		}
		next.ClientStatus = api.AllocClientStatusComplete
		next.DesiredStatus = api.AllocDesiredStatusStop
		next.ModifyIndex = n.allocIndex
		list[i] = &next
	}
	n.wake()
}

// killedRunStates is the task states of a batch job's copy that was still
// going when the job was stopped: its service task (the first of the job) is
// dead with the Terminated event of a killed task (exit code 143). It is nil
// for any other copy, which is left as it is. n.mu must be held.
func (n *Nomad) killedRunStates(jobID string, s *api.AllocationListStub) map[string]*api.TaskState {
	versions := n.jobs[jobID]
	if len(versions) == 0 || versions[len(versions)-1].Type == nil || *versions[len(versions)-1].Type != "batch" {
		return nil
	}
	switch s.ClientStatus {
	case api.AllocClientStatusComplete, api.AllocClientStatusFailed, api.AllocClientStatusLost:
		return nil
	}
	group := versions[len(versions)-1].TaskGroups[0]
	task := group.Tasks[0].Name
	if s.TaskStates[task] == nil {
		return nil
	}
	states := maps.Clone(s.TaskStates)
	was := *states[task]
	was.State = "dead"
	was.Events = append(slices.Clone(was.Events), terminated(143, false))
	states[task] = &was
	return states
}

// Stops counts the stops (a deregister without purge) the fake answered for
// jobID. A stopped job has Job(jobID).Stop true until it is registered again.
func (n *Nomad) Stops(jobID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.stops[jobID]
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

// variablePathRE is Nomad's rule for a variable path (nomad/structs).
var variablePathRE = regexp.MustCompile(`^[a-zA-Z0-9-_~/]{1,128}$`)

// maxVariableSize is Nomad's cap on a variable's keys plus values.
const maxVariableSize = 64 * 1024

// Variable returns a copy of the items at path, or nil when there is no
// variable there.
func (n *Nomad) Variable(path string) map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return maps.Clone(n.vars[path])
}

// SetVariable stores items at path, as if written outside Caravana; nil
// items delete it.
func (n *Nomad) SetVariable(path string, items map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if items == nil {
		delete(n.vars, path)
		return
	}
	n.vars[path] = maps.Clone(items)
}

// jobPath splits /v1/job/<id>/<sub> on the escaped path, so an ID with "/"
// in it (a periodic run) stays whole.
func jobPath(r *http.Request) (id, sub string) {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/v1/job/")
	rawID, sub, _ := strings.Cut(rest, "/")
	id, err := url.PathUnescape(rawID)
	if err != nil {
		id = rawID
	}
	if strings.HasSuffix(r.URL.Path, "/periodic/force") {
		return id + "/periodic/force", ""
	}
	return id, sub
}

// runClock is the time of the next periodic run the fake makes: each force
// takes the next second, so two forces never share a run (the real Nomad
// would, within one second; the service refuses that case).
var runClock = time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC).Unix()

// listJobs answers GET /v1/jobs?prefix=: the latest version of each job whose
// ID starts with prefix.
func (n *Nomad) listJobs(w http.ResponseWriter, prefix string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := []*api.JobListStub{}
	for _, id := range slices.Sorted(maps.Keys(n.jobs)) {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		j := n.jobs[id][len(n.jobs[id])-1]
		stub := &api.JobListStub{ID: id, Name: id, Status: "running", Periodic: j.Periodic != nil}
		if j.ParentID != nil {
			stub.ParentID = *j.ParentID
		}
		if j.Status != nil {
			stub.Status = *j.Status
		}
		if j.Type != nil {
			stub.Type = *j.Type
		}
		out = append(out, stub)
	}
	n.writeJSON(w, out)
}

// forcePeriodic answers PUT /v1/job/<id>/periodic/force: a new run
// "<id>/periodic-<unix seconds>", running, with one pending copy.
func (n *Nomad) forcePeriodic(w http.ResponseWriter, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	versions := n.jobs[id]
	if len(versions) == 0 || versions[len(versions)-1].Periodic == nil {
		http.Error(w, "job not found or not periodic", http.StatusNotFound)
		return
	}
	runClock++
	child := *versions[len(versions)-1]
	child.ID = new(fmt.Sprintf("%s/periodic-%d", id, runClock))
	child.ParentID = new(id)
	child.Periodic = nil
	child.Status = new("running")
	n.store(&child)
	n.allocs[*child.ID] = []*api.AllocationListStub{{ID: *child.ID + "-alloc", JobID: *child.ID, Namespace: "default",
		TaskGroup: "backup", ClientStatus: "pending"}}
	n.allocIndex++
	n.wake()
	n.evals = append(n.evals, *child.ID)
	n.writeJSON(w, struct{ EvalID string }{fmt.Sprintf("eval-%d", len(n.evals)-1)})
}

// evaluation answers GET /v1/evaluation/<id> for the evals forcePeriodic made.
func (n *Nomad) evaluation(w http.ResponseWriter, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var i int
	if _, err := fmt.Sscanf(id, "eval-%d", &i); err != nil || i < 0 || i >= len(n.evals) {
		http.Error(w, "eval not found", http.StatusNotFound)
		return
	}
	n.writeJSON(w, &api.Evaluation{ID: id, JobID: n.evals[i], Status: "complete"})
}

// Runs returns the IDs of jobID's periodic runs, oldest first.
func (n *Nomad) Runs(jobID string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, id := range slices.Sorted(maps.Keys(n.jobs)) {
		if strings.HasPrefix(id, jobID+"/periodic-") {
			out = append(out, id)
		}
	}
	return out
}

// FinishRun ends a batch job (a periodic run or a restore): the job is dead
// and its copy complete (ok) or failed (failedTask marked failed), with
// stdout and stderr as its output, so the outcome reads as Nomad's would.
func (n *Nomad) FinishRun(jobID string, ok bool, failedTask, stdout, stderr string) {
	n.mu.Lock()
	versions := n.jobs[jobID]
	if len(versions) == 0 {
		n.mu.Unlock()
		panic("deploytest: FinishRun of an unknown job " + jobID)
	}
	versions[len(versions)-1].Status = new("dead")
	allocID := jobID + "-alloc"
	tasks := versions[len(versions)-1].TaskGroups[0].Tasks
	states := map[string]*api.TaskState{}
	for _, t := range tasks {
		st := &api.TaskState{State: "dead", Failed: !ok && t.Name == failedTask}
		if st.Failed {
			st.Events = []*api.TaskEvent{{Type: "Terminated", DisplayMessage: "Exit Code: 1, Exit Message: \"Docker container exited with non-zero exit code: 1\""}}
		}
		states[t.Name] = st
	}
	status := "complete"
	if !ok {
		status = "failed"
	}
	n.allocs[jobID] = []*api.AllocationListStub{{ID: allocID, JobID: jobID, Namespace: "default",
		TaskGroup: *versions[len(versions)-1].TaskGroups[0].Name, ClientStatus: status, TaskStates: states}}
	n.allocIndex++
	n.wake()
	n.mu.Unlock()
	// The fake keeps output per copy, not per task: enough for a reader that
	// asks for one task's stdout or stderr.
	if stdout != "" {
		n.WriteLog(allocID, "stdout", stdout)
	}
	if stderr != "" {
		n.WriteLog(allocID, "stderr", stderr)
	}
}

// Schedule runs (feature 022) are batch jobs registered like any other. A test
// plays one's life with StartRun (running), FinishRunExit (ended with an exit
// code), OOMRun, FailRunStart and SetRunLogs. Each works on the run's newest
// copy, or makes it ("<job>-alloc"): the service's task is the job's first
// task, the others (the names sidecar) just end. Deregistering a run (Stops,
// Purged) ends its copy with the exit code 143 a killed task has.

// runCopy is how a test ends or advances a run: the copy's status and what its
// service task did.
type runCopy struct {
	status  string // the client status
	state   string // the service task's state: running or dead
	failed  bool   // the service task failed
	started bool   // the service task started
	events  []*api.TaskEvent
}

// setRunCopy makes jobID's newest copy (or its first one) look like c. The job
// must exist (the fake panics otherwise: a test bug). n.mu must not be held.
func (n *Nomad) setRunCopy(jobID string, c runCopy) {
	n.mu.Lock()
	defer n.mu.Unlock()
	versions := n.jobs[jobID]
	if len(versions) == 0 {
		panic("deploytest: the run's job is unknown: " + jobID)
	}
	job := versions[len(versions)-1]
	group := job.TaskGroups[0]
	if c.status == api.AllocClientStatusComplete || c.status == api.AllocClientStatusFailed {
		job.Status = new("dead")
	} else {
		job.Status = new("running")
	}
	states := map[string]*api.TaskState{}
	for i, t := range group.Tasks {
		if i > 0 {
			states[t.Name] = &api.TaskState{State: "dead"}
			continue
		}
		st := &api.TaskState{State: c.state, Failed: c.failed, Events: c.events}
		if c.started {
			st.StartedAt = time.Now().Add(-time.Second).UTC()
		}
		states[t.Name] = st
	}
	id, desired := jobID+"-alloc", api.AllocDesiredStatusRun
	if list := n.allocs[jobID]; len(list) > 0 {
		id, desired = list[len(list)-1].ID, list[len(list)-1].DesiredStatus
	}
	n.allocIndex++
	n.allocs[jobID] = []*api.AllocationListStub{{ID: id, JobID: jobID, Namespace: "default", TaskGroup: *group.Name,
		ClientStatus: c.status, DesiredStatus: desired, TaskStates: states,
		CreateIndex: n.allocIndex, ModifyIndex: n.allocIndex}}
	n.wake()
}

// StartRun makes the run's copy running: its service task started.
func (n *Nomad) StartRun(jobID string) {
	n.setRunCopy(jobID, runCopy{status: api.AllocClientStatusRunning, state: "running", started: true,
		events: []*api.TaskEvent{{Type: api.TaskStarted}}})
}

// FinishRunExit ends the run with the service task's exit code: its copy is
// complete when exitCode is 0 and failed otherwise, and its newest Terminated
// event carries the code as Nomad's does (Details["exit_code"]).
func (n *Nomad) FinishRunExit(jobID string, exitCode int) {
	status := api.AllocClientStatusComplete
	if exitCode != 0 {
		status = api.AllocClientStatusFailed
	}
	n.setRunCopy(jobID, runCopy{status: status, state: "dead", failed: exitCode != 0, started: true,
		events: []*api.TaskEvent{{Type: api.TaskStarted}, terminated(exitCode, false)}})
}

// OOMRun ends the run as the kernel ending it for passing its memory ceiling:
// exit code 137 and oom_killed on the Terminated event.
func (n *Nomad) OOMRun(jobID string) {
	n.setRunCopy(jobID, runCopy{status: api.AllocClientStatusFailed, state: "dead", failed: true, started: true,
		events: []*api.TaskEvent{{Type: api.TaskStarted}, terminated(137, true)}})
}

// FailRunStart ends the run without its task ever starting: a Driver Failure
// event and a failed copy.
func (n *Nomad) FailRunStart(jobID string) {
	n.FailRunStartWith(jobID, "Failed to pull `busybox:nope`: unauthorized")
}

// FailRunStartWith is FailRunStart with the driver's message, as the docker
// driver words it (it is the event's DisplayMessage and its driver_error).
func (n *Nomad) FailRunStartWith(jobID, message string) {
	n.setRunCopy(jobID, runCopy{status: api.AllocClientStatusFailed, state: "dead", failed: true,
		events: []*api.TaskEvent{{Type: api.TaskDriverFailure, DisplayMessage: message,
			Details: map[string]string{"driver_error": message}}}})
}

// terminated is the event Nomad adds when a task exits.
func terminated(exitCode int, oom bool) *api.TaskEvent {
	e := &api.TaskEvent{Type: api.TaskTerminated,
		Details: map[string]string{"exit_code": strconv.Itoa(exitCode), "signal": "0", "oom_killed": strconv.FormatBool(oom)}}
	e.DisplayMessage = fmt.Sprintf("Exit Code: %d", exitCode)
	return e
}

// SetRunLogs sets what the run's copy printed, replacing what was there. The
// copy is the one StartRun, FinishRunExit and the others make ("<job>-alloc");
// it has to exist when the output is read.
func (n *Nomad) SetRunLogs(jobID string, stdout, stderr []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	id := jobID + "-alloc"
	if list := n.allocs[jobID]; len(list) > 0 {
		id = list[len(list)-1].ID
	}
	n.logs[logKey{id, "stdout"}] = append([]byte(nil), stdout...)
	n.logs[logKey{id, "stderr"}] = append([]byte(nil), stderr...)
	n.wakeLogs()
}

// HostVolumes returns the names of the host volumes, sorted.
func (n *Nomad) HostVolumes() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var names []string
	for _, v := range n.volumes {
		names = append(names, v.vol.Name)
	}
	slices.Sort(names)
	return names
}

// FailVolumes makes every host volume request answer status (0 undoes it).
func (n *Nomad) FailVolumes(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.volumeStatus = status
}

// hostVolume answers the host volume endpoints.
func (n *Nomad) hostVolume(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.volumeStatus != 0 {
		http.Error(w, "Permission denied", n.volumeStatus)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/volume/host/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/volumes":
		out := []*api.HostVolumeStub{}
		for _, id := range slices.Sorted(maps.Keys(n.volumes)) {
			v := n.volumes[id].vol
			if strings.HasPrefix(v.Name, r.URL.Query().Get("prefix")) {
				out = append(out, &api.HostVolumeStub{Namespace: v.Namespace, ID: v.ID, Name: v.Name, PluginID: v.PluginID, NodeID: v.NodeID, State: v.State})
			}
		}
		n.writeJSON(w, out)
	case r.Method == http.MethodPut && id == "create":
		var req api.HostVolumeCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Volume == nil || req.Volume.Name == "" {
			http.Error(w, "bad host volume request", http.StatusBadRequest)
			return
		}
		for _, v := range n.volumes {
			if v.vol.Name == req.Volume.Name {
				http.Error(w, fmt.Sprintf("could not place volume %q: no node meets constraints: 1 nodes had existing volume", v.vol.Name), http.StatusInternalServerError)
				return
			}
		}
		n.index++
		v := *req.Volume
		v.ID = fmt.Sprintf("vol-%d", n.index)
		v.NodeID, v.HostPath, v.State = "node-1", "/opt/nomad/data/host_volumes/"+v.ID, api.HostVolumeStatePending
		n.volumes[v.ID] = &hostVolume{vol: v}
		n.writeJSON(w, api.HostVolumeCreateResponse{Volume: &v})
	case r.Method == http.MethodGet:
		hv, ok := n.volumes[id]
		if !ok {
			http.Error(w, "volume not found", http.StatusNotFound)
			return
		}
		if hv.reads++; hv.reads > 1 {
			hv.vol.State = api.HostVolumeStateReady
		}
		v := hv.vol
		n.writeJSON(w, &v)
	case r.Method == http.MethodDelete:
		hv, ok := n.volumes[id]
		if !ok {
			http.Error(w, "volume not found", http.StatusNotFound)
			return
		}
		if job := n.jobMounting(hv.vol.Name); job != "" && len(n.allocs[job]) > 0 {
			http.Error(w, fmt.Sprintf("volume %s in use by allocations: [%s]", id, n.allocs[job][0].ID), http.StatusBadRequest)
			return
		}
		n.index++
		delete(n.volumes, id)
		n.writeJSON(w, struct{}{})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// jobMounting is the job whose latest version mounts the host volume name,
// or "". n.mu must be held.
func (n *Nomad) jobMounting(name string) string {
	for id, versions := range n.jobs {
		job := versions[len(versions)-1]
		for _, tg := range job.TaskGroups {
			for _, v := range tg.Volumes {
				if v != nil && v.Source == name {
					return id
				}
			}
		}
	}
	return ""
}

// FailVariables makes every variables request answer status (0 undoes it),
// as Nomad does when the token's policy has no variables rules (403).
func (n *Nomad) FailVariables(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.varsStatus = status
}

// VariablePaths returns every variable path, sorted.
func (n *Nomad) VariablePaths() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Sorted(maps.Keys(n.vars))
}

// variable answers GET, PUT and DELETE /v1/var/{path}.
func (n *Nomad) variable(w http.ResponseWriter, r *http.Request, path string) {
	if !variablePathRE.MatchString(path) {
		http.Error(w, fmt.Sprintf("invalid path %q", path), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		n.mu.Lock()
		defer n.mu.Unlock()
		items, ok := n.vars[path]
		if !ok {
			http.Error(w, "variable not found", http.StatusNotFound)
			return
		}
		n.writeJSON(w, &api.Variable{Namespace: "default", Path: path, Items: maps.Clone(items)})
	case http.MethodPut:
		var v api.Variable
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			http.Error(w, "bad variable", http.StatusBadRequest)
			return
		}
		if len(v.Items) == 0 {
			http.Error(w, "empty variables are invalid", http.StatusBadRequest)
			return
		}
		size := 0
		for k, val := range v.Items {
			size += len(k) + len(val)
		}
		if size > maxVariableSize {
			http.Error(w, "variables are limited to 64KiB in total size", http.StatusBadRequest)
			return
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		n.index++
		n.vars[path] = maps.Clone(v.Items)
		n.writeJSON(w, &api.Variable{Namespace: "default", Path: path, Items: v.Items})
	case http.MethodDelete:
		n.mu.Lock()
		defer n.mu.Unlock()
		n.index++
		delete(n.vars, path)
		w.Header().Set("X-Nomad-Index", strconv.FormatUint(n.index, 10))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// listVariables answers GET /v1/vars?prefix=: the metadata of every variable
// whose path starts with prefix, sorted by path.
func (n *Nomad) listVariables(w http.ResponseWriter, prefix string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := []*api.VariableMetadata{}
	for _, path := range slices.Sorted(maps.Keys(n.vars)) {
		if strings.HasPrefix(path, prefix) {
			out = append(out, &api.VariableMetadata{Namespace: "default", Path: path})
		}
	}
	n.writeJSON(w, out)
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

// SetStats sets what the copy's stats request answers: the usage of the
// allocation and of each of its tasks. A copy with no stats answers 404, like
// Nomad does for an allocation its node doesn't have.
func (n *Nomad) SetStats(copyID string, usage api.AllocResourceUsage) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stats[copyID] = usage
}

// SetStatsDelay holds every stats answer back by d (until the request is
// cancelled), so tests can see requests overlap or time out.
func (n *Nomad) SetStatsDelay(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.statsDelay = d
}

// StatsRequests counts the stats requests answered or cancelled so far.
func (n *Nomad) StatsRequests() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.statsRequests
}

// MaxStatsInFlight is the most stats requests that were open at the same time.
func (n *Nomad) MaxStatsInFlight() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.statsMax
}

// serveStats answers GET /v1/client/allocation/{id}/stats.
func (n *Nomad) serveStats(w http.ResponseWriter, r *http.Request, id string) {
	n.mu.Lock()
	n.statsRequests++
	n.statsInFlight++
	n.statsMax = max(n.statsMax, n.statsInFlight)
	delay := n.statsDelay
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.statsInFlight--
		n.mu.Unlock()
	}()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	n.mu.Lock()
	usage, ok := n.stats[id]
	n.mu.Unlock()
	if !ok {
		http.Error(w, "allocation not found", http.StatusNotFound)
		return
	}
	n.writeJSON(w, usage)
}
