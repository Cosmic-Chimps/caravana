package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// composePreviewJSON is the answer of previewCompose (schema ComposePreview).
type composePreviewJSON struct {
	App      app.App       `json:"app"`
	Warnings []app.Warning `json:"warnings"`
}

// deployCompose answers POST /v1/apps/{id}/compose: it turns the Compose
// file into an app and deploys it like deployApp does, answering 202 with the
// deployment once Nomad has accepted the job. If-Match makes it an edit of
// the app at that revision (412 app_changed otherwise); If-None-Match: *
// makes it a create (409 app_exists when the ID is taken). Not both.
func (s *server) deployCompose(w http.ResponseWriter, r *http.Request) error {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	id := r.PathValue("id")
	cond, err := ifMatch(r)
	if err != nil {
		return err
	}
	ifNew, err := ifNoneMatch(r)
	if err != nil {
		return err
	}
	if ifNew && cond.IfRevision != "" {
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "If-None-Match",
			Message: "can't be sent with If-Match: one says the app exists, the other that it doesn't"}
	}
	cond.IfNew = ifNew
	var req compose.Request
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	rec, err := s.svc.DeployComposeIf(r.Context(), id, req, cond, app.TriggerManual)
	if err != nil {
		return err
	}
	w.Header().Set("Location", deploymentPath(id, rec.Deployment.ID))
	writeJSON(w, http.StatusAccepted, toDeploymentJSON(rec))
	return nil
}

// getCompose answers GET /v1/apps/{id}/compose: the Compose file and overlay
// the app was last deployed from, with the app's revision as the ETag. Secret
// values are never kept, so each secret has its name only; sent back to
// deployCompose (with If-Match), the answer keeps every current value. An app
// deployed from an image or a definition has none: 404 compose_not_kept.
func (s *server) getCompose(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.svc.GetApp(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if rec.Compose == nil {
		return &Error{Status: http.StatusNotFound, Code: codeComposeNotKept, Message: "This app's Compose file wasn't kept."}
	}
	w.Header().Set("ETag", `"`+rec.Revision+`"`)
	writeJSON(w, http.StatusOK, rec.Compose)
	return nil
}

// getDeploymentCompose answers
// GET /v1/apps/{id}/deployments/{deploymentId}/compose: the file one past
// deployment was made from, as getCompose shows it. A deployment made from an
// image or a definition (or before the file was kept) has none.
func (s *server) getDeploymentCompose(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.svc.GetDeployment(r.Context(), r.PathValue("id"), r.PathValue("deploymentId"))
	if err != nil {
		return err
	}
	if rec.Compose == nil {
		return &Error{Status: http.StatusNotFound, Code: codeComposeNotKept, Message: "This deployment wasn't made from a Compose file."}
	}
	writeJSON(w, http.StatusOK, rec.Compose)
	return nil
}

// previewCompose answers POST /v1/apps/{id}/compose/preview: the app the
// file becomes and its warnings. Nothing is saved or deployed.
func (s *server) previewCompose(w http.ResponseWriter, r *http.Request) error {
	var req compose.Request
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	a, warnings, err := s.svc.PreviewCompose(r.Context(), r.PathValue("id"), req)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, composePreviewJSON{App: a, Warnings: warnings})
	return nil
}
