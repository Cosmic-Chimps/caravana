package api

import (
	"net/http"

	"github.com/cosmic-chimps/caravana/internal/httpx"
)

// changePasswordRequest is schema ChangePasswordRequest.
type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// changePassword answers PUT /v1/account/password. Every session ends except
// the one making the request, when it was made with a session cookie; API
// tokens keep working.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request) error {
	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	// Without an Authorization header, authenticate accepted the session
	// cookie: that session is the caller's, and it stays.
	keep := ""
	if len(r.Header.Values("Authorization")) == 0 {
		keep, _ = httpx.SessionCookie(r, s.proxies.HTTPS(r))
	}
	if err := s.auth.ChangePassword(r.Context(), req.CurrentPassword, req.NewPassword, keep); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
