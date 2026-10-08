package api

import (
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// redeployDeployment answers POST /v1/apps/{id}/deployments/{deploymentId}/redeploy:
// deploy that past healthy deployment again, with its definition and pinned
// images. Like deploy, it answers 202 once Nomad has accepted the job.
func (s *server) redeployDeployment(w http.ResponseWriter, r *http.Request) error {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	id := r.PathValue("id")
	rec, err := s.svc.RedeployVersion(r.Context(), id, r.PathValue("deploymentId"), app.TriggerManual)
	if err != nil {
		return err
	}
	w.Header().Set("Location", deploymentPath(id, rec.Deployment.ID))
	writeJSON(w, http.StatusAccepted, toDeploymentJSON(rec))
	return nil
}

// exportApp answers GET /v1/apps/{id}/jobspec: the live version as HCL.
func (s *server) exportApp(w http.ResponseWriter, r *http.Request) error {
	return s.writeExport(w, r, "")
}

// exportDeployment answers GET /v1/apps/{id}/deployments/{deploymentId}/jobspec.
func (s *server) exportDeployment(w http.ResponseWriter, r *http.Request) error {
	return s.writeExport(w, r, r.PathValue("deploymentId"))
}

// writeExport sends the jobspec as a plain-text download. Its errors are
// JSON, like every other error.
func (s *server) writeExport(w http.ResponseWriter, r *http.Request, deploymentID string) error {
	name, hcl, err := s.svc.Export(r.Context(), r.PathValue("id"), deploymentID)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(hcl) // the status is sent; a failed write can't be reported
	return nil
}

// versionError maps why a past deployment can't be redeployed or exported.
func versionError(err error) *Error {
	switch {
	case errors.Is(err, service.ErrDeploymentNotHealthy):
		return &Error{Status: http.StatusConflict, Code: codeDeploymentNotHealthy, Message: err.Error()}
	case errors.Is(err, service.ErrDefinitionNotRecorded):
		return &Error{Status: http.StatusConflict, Code: codeDefinitionNotRecorded, Message: err.Error()}
	case errors.Is(err, service.ErrNoLiveDeployment):
		return &Error{Status: http.StatusConflict, Code: codeNoLiveDeployment, Message: err.Error()}
	}
	return nil
}
