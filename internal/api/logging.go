package api

import (
	"net/http"
	"time"
)

// logRequests logs one line per request: method, path, status and duration.
//
// It deliberately reads nothing else from the request. Headers, query
// strings and bodies can carry tokens and secrets, and the safest way to keep
// them out of the log is never to touch them here.
func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}

// statusRecorder remembers the status code written through it. Embedding the
// ResponseWriter passes every other method through unchanged.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
