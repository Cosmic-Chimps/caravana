// Package deploytest is a fake Nomad for tests: a real HTTP server that
// answers the four endpoints Caravana's deploy flow uses, so tests exercise
// the official Nomad client end to end. Product code never imports it.
//
// Rollouts are scripted: Script(version, states...) makes each deployments
// request for that job version return the next state, the last one repeating.
package deploytest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

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
}

// New starts a fake Nomad that closes when the test ends.
func New(t testing.TB) *Nomad {
	n := &Nomad{
		index:   1,
		jobs:    map[string][]*api.Job{},
		scripts: map[string]map[uint64][]api.Deployment{},
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
		// Close the connection without an answer: the client sees a network
		// error, as when Nomad is unreachable.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if token != "" && r.Header.Get("X-Nomad-Token") != token {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
	}

	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
		n.register(w, r)
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
	n.registrations++
	n.writeJSON(w, api.JobRegisterResponse{
		EvalID:         fmt.Sprintf("eval-%d", n.index),
		JobModifyIndex: *j.JobModifyIndex,
		Warnings:       n.warnings,
	})
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Nomad-Index", strconv.FormatUint(n.index, 10))
	w.Header().Set("X-Nomad-LastContact", "0")
	w.Header().Set("X-Nomad-KnownLeader", "true")
	_ = json.NewEncoder(w).Encode(v)
}
