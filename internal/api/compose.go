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
