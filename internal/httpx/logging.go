package httpx

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// LogRequests logs one line per request: method, path, status and duration.
//
// It deliberately reads nothing else from the request. Headers, cookies,
// query strings and bodies can carry tokens and secrets, and the safest way
// to keep them out of the log is never to touch them here.
func LogRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
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

// Unwrap lets http.ResponseController reach the real ResponseWriter, so a
// handler can change its own write deadline.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack lets a handler take over the connection, which a websocket upgrade
// does. The recorder embeds the ResponseWriter as an interface, so without
// this method the writer would no longer be an http.Hijacker and every upgrade
// would fail. The log line says 101 (Switching Protocols) for a connection
// taken over, since the handler then writes its own answer to the connection
// and WriteHeader never runs.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err == nil {
		r.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}
