package deploytest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// What feature 043 asks of Nomad: POST /v1/job/{id}/scale. Like Nomad it makes
// a new job version with the group's count changed, keeps the job's meta, and
// answers the new JobModifyIndex. It does not move any copy: a test sets the
// copies with SetCopies and the deployment with SetDeployment.

// ScaleRequest is one scale call as the fake received it.
type ScaleRequest struct {
	Job, Group string
	Count      int
	Message    string
	Meta       map[string]any
}

// Scales returns every scale request the fake accepted, in order.
func (n *Nomad) Scales() []ScaleRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ScaleRequest(nil), n.scales...)
}

// RejectScale makes the next scale request answer status with body, once.
func (n *Nomad) RejectScale(status int, body string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.scaleStatus, n.scaleBody = status, body
}

func (n *Nomad) scale(w http.ResponseWriter, r *http.Request) {
	id, _ := jobPath(r)
	var req api.ScalingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Count == nil {
		http.Error(w, "bad scale request", http.StatusBadRequest)
		return
	}
	group := req.Target["Group"]

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.scaleStatus != 0 {
		status, body := n.scaleStatus, n.scaleBody
		n.scaleStatus, n.scaleBody = 0, ""
		http.Error(w, body, status)
		return
	}
	versions := n.jobs[id]
	if len(versions) == 0 {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	next := *versions[len(versions)-1]
	found := false
	next.TaskGroups = make([]*api.TaskGroup, len(next.TaskGroups))
	for i, g := range versions[len(versions)-1].TaskGroups {
		c := *g // the older version keeps its own count
		if c.Name != nil && *c.Name == group {
			c.Count = new(int(*req.Count))
			found = true
		}
		next.TaskGroups[i] = &c
	}
	if !found {
		http.Error(w, fmt.Sprintf("task group %q not found", group), http.StatusBadRequest)
		return
	}
	j := n.store(&next)
	n.cancelOlder(*j.ID, *j.Version)
	n.scales = append(n.scales, ScaleRequest{Job: id, Group: group, Count: int(*req.Count), Message: strings.TrimSpace(req.Message), Meta: req.Meta})
	n.writeJSON(w, api.JobRegisterResponse{
		EvalID:         fmt.Sprintf("eval-%d", n.index),
		JobModifyIndex: *j.JobModifyIndex,
	})
}
