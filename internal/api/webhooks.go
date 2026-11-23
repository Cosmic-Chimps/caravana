package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
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
	// Skipped says why a push to an app that builds from Git built nothing (feature 050).
	Skipped string `json:"skipped,omitempty"`
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
// comes from ?token=, Authorization: Bearer or X-Gitlab-Token. Only after it's
// accepted is the body read, so a caller without a token can't make Caravana
// read anything; a body over the limit fails before any deploy. An app with no
// built service ignores the body; one with a built service reads it as a push
// (feature 050): a push to another branch, a deleted branch or a ping builds
// nothing and answers 200 with `skipped`, or 204 when the app has no
// deployment to show.
func (s *server) callWebhook(w http.ResponseWriter, r *http.Request) error {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	id := r.PathValue("id")
	if err := s.svc.CheckWebhookToken(r.Context(), id, webhookToken(r)); err != nil {
		return err
	}
	// After the token, so a caller without one learns nothing about the headers.
	ctx, err := withOrigin(r)
	if err != nil {
		return err
	}
	body, err := readBody(w, r, service.MaxPushBody)
	if err != nil {
		return err
	}

	res, err := s.svc.WebhookPush(ctx, id, r.Header, body)
	if err != nil {
		return err
	}
	if res.Skipped != "" && res.Deployment.Deployment.ID == "" {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	status := http.StatusOK
	if res.Changed {
		status = http.StatusAccepted
		w.Header().Set("Location", deploymentPath(id, res.Deployment.Deployment.ID))
	}
	writeJSON(w, status, webhookResultJSON{Changed: res.Changed, Deployment: toDeploymentJSON(res.Deployment), Skipped: res.Skipped})
	return nil
}

// webhookToken reads the token from the query string or the Authorization
// header, and from X-Gitlab-Token (GitLab's secret token field) when neither
// is given. Both present with different values gives "", which never matches.
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
	case header != "":
		return header
	default:
		return strings.TrimSpace(r.Header.Get("X-Gitlab-Token"))
	}
}

// readBody reads the request body, up to limit bytes, whatever its
// Content-Type: senders post their own payloads, which only a push webhook
// of an app that builds reads.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return nil, &Error{Status: http.StatusRequestEntityTooLarge, Code: codeBodyTooLarge,
			Message: fmt.Sprintf("request body is larger than %d bytes", limit)}
	case err != nil:
		return nil, &Error{Status: http.StatusBadRequest, Code: codeBadRequest, Message: "could not read the request body"}
	}
	return body, nil
}
