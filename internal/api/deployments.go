package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// deployWriteDeadline replaces the server's 30 s write timeout for deploy
// requests: they may wait behind another deploy of the same app before their
// own 25 s of work (research R11).
const deployWriteDeadline = 60 * time.Second

// List limits for GET /v1/apps/{id}/deployments.
const (
	defaultListLimit = 20
	maxListLimit     = 100
)

// deploymentJSON is a deployment as the API shows it (schema Deployment).
type deploymentJSON struct {
	ID         string            `json:"id"`
	AppID      string            `json:"appId"`
	Trigger    app.Trigger       `json:"trigger"`
	Status     store.Status      `json:"status"`
	JobVersion *uint64           `json:"jobVersion,omitempty"`
	Digests    map[string]string `json:"digests"`
	Warnings   []app.Warning     `json:"warnings"`
	Error      string            `json:"error,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

func toDeploymentJSON(rec store.DeploymentRecord) deploymentJSON {
	d := deploymentJSON{
		ID:         rec.Deployment.ID,
		AppID:      rec.AppID,
		Trigger:    rec.Deployment.Trigger,
		Status:     rec.Status,
		JobVersion: rec.JobVersion,
		Digests:    rec.Deployment.Digests,
		Warnings:   rec.Warnings,
		Error:      rec.Error,
		CreatedAt:  rec.CreatedAt,
		UpdatedAt:  rec.UpdatedAt,
	}
	// Never null in the JSON: clients can always iterate them.
	if d.Digests == nil {
		d.Digests = map[string]string{}
	}
	if d.Warnings == nil {
		d.Warnings = []app.Warning{}
	}
	return d
}

// deploymentPath is a deployment's URL, sent as Location.
func deploymentPath(appID, deploymentID string) string {
	return "/v1/apps/" + appID + "/deployments/" + deploymentID
}

// deploy answers POST /v1/apps/{id}/deployments. With a body it saves and
// deploys the app; with an empty body it redeploys the stored one. It answers
// 202 once Nomad has accepted the job; the client follows the Location.
func (s *server) deploy(w http.ResponseWriter, r *http.Request) error {
	// Best effort: a writer that can't change deadlines keeps the server's.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	id := r.PathValue("id")
	var a app.App
	present, err := decodeOptionalJSON(r, &a)
	if err != nil {
		return err
	}

	var rec store.DeploymentRecord
	if present {
		switch a.ID {
		case "":
			a.ID = id
		case id:
		default:
			return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "id",
				Message: fmt.Sprintf("%q doesn't match the app ID in the path (%q)", a.ID, id)}
		}
		rec, err = s.svc.Deploy(r.Context(), a, app.TriggerManual)
	} else {
		rec, err = s.svc.Redeploy(r.Context(), id, app.TriggerManual)
	}
	if err != nil {
		return err
	}

	w.Header().Set("Location", deploymentPath(id, rec.Deployment.ID))
	writeJSON(w, http.StatusAccepted, toDeploymentJSON(rec))
	return nil
}

// listDeployments answers GET /v1/apps/{id}/deployments?limit=.
func (s *server) listDeployments(w http.ResponseWriter, r *http.Request) error {
	limit := defaultListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "limit",
				Message: fmt.Sprintf("must be a whole number from 1 to %d, got %q", maxListLimit, v)}
		}
		limit = n
	}
	recs, err := s.svc.ListDeployments(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		return err
	}
	out := struct {
		Deployments []deploymentJSON `json:"deployments"`
	}{Deployments: make([]deploymentJSON, 0, len(recs))}
	for _, rec := range recs {
		out.Deployments = append(out.Deployments, toDeploymentJSON(rec))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// getDeployment answers GET /v1/apps/{id}/deployments/{deploymentId}.
func (s *server) getDeployment(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.svc.GetDeployment(r.Context(), r.PathValue("id"), r.PathValue("deploymentId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDeploymentJSON(rec))
	return nil
}
