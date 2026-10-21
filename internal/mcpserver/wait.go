package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// finalStatuses are the deployment statuses that don't change any more.
var finalStatuses = []string{"healthy", "failed", "reverted", "superseded"}

// deploymentView is the part of a deployment the wait reads: its status and
// how far the rollout has got.
type deploymentView struct {
	Status   string `json:"status"`
	Progress *struct {
		Wanted  int `json:"wanted"`
		Placed  int `json:"placed"`
		Healthy int `json:"healthy"`
	} `json:"progress"`
}

// message is the progress notification's text: the status and the counts,
// never Nomad's own description, which the agent can read in the answer.
func (v deploymentView) message(id string) string {
	msg := fmt.Sprintf("deployment %s: %s", id, v.Status)
	if p := v.Progress; p != nil && p.Wanted > 0 {
		msg += fmt.Sprintf(", %d of %d copies healthy", p.Healthy, p.Wanted)
	}
	return msg
}

// waitForDeployment reads the deployment every poll interval until its status
// is final or the timeout passes, and answers the last deployment it read with
// "waitEnded" saying which. When the client sent a progress token it is told
// after each read, so a long wait is not silent.
func (h *handler) waitForDeployment(ctx context.Context, req *mcp.CallToolRequest, in waitInput) (string, error) {
	seconds := in.TimeoutSeconds
	if seconds == 0 {
		seconds = defaultWaitSecs
	}
	deadline := time.NewTimer(time.Duration(seconds) * h.tuning.waitUnit)
	defer deadline.Stop()
	token := req.Params.GetProgressToken()

	for reads := 1; ; reads++ {
		r, err := h.api.do(ctx, call{op: opGetDeployment, params: []string{"id", in.App, "deploymentId", in.Deployment}})
		if err != nil {
			return "", err
		}
		var view deploymentView
		if err := json.Unmarshal(r.body, &view); err != nil {
			return "", &toolError{Code: "bad_answer", Text: "Caravana's answer for the deployment was not JSON"}
		}
		if token != nil {
			// Best effort: a client that went away ends the wait through ctx.
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      float64(reads),
				Message:       view.message(in.Deployment),
			})
		}
		if slices.Contains(finalStatuses, view.Status) {
			return withField(r.body, "waitEnded", "final")
		}

		pause := time.NewTimer(h.tuning.poll)
		select {
		case <-ctx.Done():
			pause.Stop()
			return "", ctx.Err()
		case <-deadline.C:
			pause.Stop()
			return withField(r.body, "waitEnded", "timeout")
		case <-pause.C:
		}
	}
}
