package api

import (
	"context"
	"net/http"
	"time"
)

// healthTimeout bounds how long a health check waits for the store.
const healthTimeout = 2 * time.Second

// health answers GET /v1/health: 200 when the store is reachable, 503 when
// it isn't.
func (s *server) health(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		s.log.Warn("health: store unreachable", "error", err)
		return &Error{Status: http.StatusServiceUnavailable, Code: codeUnavailable, Message: "store is not reachable"}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
	return nil
}
