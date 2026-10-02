package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// webhookTokenJSON is a newly created token (schema WebhookToken).
type webhookTokenJSON struct {
	Token     string    `json:"token"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
}

// webhookStatusJSON is schema WebhookStatus. CreatedAt is a pointer so it is
// left out when there is no token.
type webhookStatusJSON struct {
	Enabled   bool       `json:"enabled"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}

// webhookResultJSON is schema WebhookResult.
type webhookResultJSON struct {
	Changed    bool           `json:"changed"`
	Deployment deploymentJSON `json:"deployment"`
}

// createWebhookToken answers POST /v1/apps/{id}/webhook: a new token, shown
// only in this response; any earlier one stops working.
func (s *server) createWebhookToken(w http.ResponseWriter, r *http.Request) error {
	tok, err := s.svc.CreateWebhookToken(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, webhookTokenJSON{Token: tok.Token, Path: tok.Path, CreatedAt: tok.CreatedAt})
	return nil
}

// getWebhook answers GET /v1/apps/{id}/webhook: whether there is a token,
// never the token itself.
func (s *server) getWebhook(w http.ResponseWriter, r *http.Request) error {
	st, err := s.svc.WebhookStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	out := webhookStatusJSON{Enabled: st.Enabled}
	if st.Enabled {
		out.CreatedAt = &st.CreatedAt
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// deleteWebhookToken answers DELETE /v1/apps/{id}/webhook.
func (s *server) deleteWebhookToken(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteWebhookToken(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// callWebhook answers POST /v1/webhooks/{id} (research R4, R5). The token
// comes from ?token= or Authorization: Bearer. Only after it's accepted is
// the body read (and thrown away), so a caller without a token can't make
// Caravana read anything; a body over the limit fails before any deploy.
func (s *server) callWebhook(w http.ResponseWriter, r *http.Request) error {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	id := r.PathValue("id")
	if err := s.svc.CheckWebhookToken(r.Context(), id, webhookToken(r)); err != nil {
		return err
	}
	if err := drainBody(w, r); err != nil {
		return err
	}

	res, err := s.svc.WebhookRedeploy(r.Context(), id)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if res.Changed {
		status = http.StatusAccepted
		w.Header().Set("Location", deploymentPath(id, res.Deployment.Deployment.ID))
	}
	writeJSON(w, status, webhookResultJSON{Changed: res.Changed, Deployment: toDeploymentJSON(res.Deployment)})
	return nil
}

// webhookToken reads the token from the query string or the Authorization
// header. Both present with different values gives "", which never matches.
func webhookToken(r *http.Request) string {
	query := r.URL.Query().Get("token")
	header := ""
	if scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		header = strings.TrimSpace(value)
	}
	switch {
	case query != "" && header != "" && query != header:
		return ""
	case query != "":
		return query
	default:
		return header
	}
}

// drainBody reads and discards the request body, up to maxBodyBytes,
// whatever its Content-Type: senders post their own payloads, which the
// webhook ignores.
func drainBody(w http.ResponseWriter, r *http.Request) error {
	_, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return &Error{Status: http.StatusRequestEntityTooLarge, Code: codeBodyTooLarge,
			Message: fmt.Sprintf("request body is larger than %d bytes", maxBodyBytes)}
	case err != nil:
		return &Error{Status: http.StatusBadRequest, Code: codeBadRequest, Message: "could not read the request body"}
	}
	return nil
}
