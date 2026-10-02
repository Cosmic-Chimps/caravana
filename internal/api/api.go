// Package api is Caravana's public HTTP API, described by api/openapi.yaml.
//
// Every response is JSON. Handlers return an error instead of writing error
// responses themselves; one adapter turns it into the standard body
// {"error": {"code", "message", "field"}} with the right status.
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"path"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// handlerFunc is an HTTP handler that may fail. Returning an error is the Go
// way to do what a C# exception filter does: the adapter maps it to a status.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// route is one endpoint. The routes table builds the mux and is what the
// contract test compares with api/openapi.yaml.
type route struct {
	method, path string
	handle       handlerFunc
}

type server struct {
	svc   *service.Service
	store *store.Store // for the health check only; everything else goes through svc
	log   *slog.Logger
}

func (s *server) routes() []route {
	return []route{
		{http.MethodGet, "/v1/health", s.health},
		{http.MethodGet, "/v1/apps/{id}", s.getApp},
		{http.MethodPost, "/v1/apps/{id}/deployments", s.deploy},
		{http.MethodGet, "/v1/apps/{id}/deployments", s.listDeployments},
		{http.MethodGet, "/v1/apps/{id}/deployments/{deploymentId}", s.getDeployment},
	}
}

// New returns the API's HTTP handler. Apps and deployments go through svc,
// the same layer the UI and the webhook use (constitution V); st is only
// pinged by the health check.
func New(svc *service.Service, st *store.Store, log *slog.Logger) http.Handler {
	s := &server{svc: svc, store: st, log: log}
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.Handle(rt.method+" "+rt.path, s.handle(rt.handle))
	}
	return s.logRequests(s.jsonOnly(mux))
}

// jsonOnly sits in front of the mux so the API never answers with a redirect
// or a plain-text error. The standard mux redirects unclean paths and answers
// unknown paths and wrong methods in plain text.
func (s *server) jsonOnly(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "//v1/health", "/v1/health/" and "/v1/../v1/health" would get a 301
		// from the mux. path.Clean also strips a trailing slash, which no
		// route has.
		if path.Clean(r.URL.Path) != r.URL.Path {
			writeError(w, &Error{Status: http.StatusNotFound, Code: codeNotFound, Message: "no such endpoint"})
			return
		}

		// An empty pattern means no route matched: the mux would answer 404,
		// or 405 if the path exists for other methods. Let it decide, then
		// replace its plain-text answer with JSON.
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		rec := &headerRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		if rec.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", rec.header.Get("Allow"))
			writeError(w, &Error{Status: http.StatusMethodNotAllowed, Code: codeMethodNotAllowed,
				Message: r.Method + " is not allowed here"})
			return
		}
		writeError(w, &Error{Status: http.StatusNotFound, Code: codeNotFound, Message: "no such endpoint"})
	})
}

// headerRecorder captures the status and headers a handler writes, and
// throws the body away.
type headerRecorder struct {
	header http.Header
	status int
}

func (h *headerRecorder) Header() http.Header         { return h.header }
func (h *headerRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (h *headerRecorder) WriteHeader(status int)      { h.status = status }

// handle adapts a handlerFunc to http.Handler, writing a returned error, or
// a panic, as the standard error body.
func (s *server) handle(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("handler panicked", "method", r.Method, "path", r.URL.Path, "panic", v)
				writeError(w, internalError())
			}
		}()
		if err := h(w, r); err != nil {
			writeError(w, s.toAPIError(r, err))
		}
	}
}

// toAPIError maps any error to the *Error the client sees. A failed deploy
// also points at the deployment record it left (FR-022a). Unexpected errors
// are logged and hidden behind a generic message.
func (s *server) toAPIError(r *http.Request, err error) *Error {
	e := s.mapError(r, err)
	var deployErr *service.DeployError
	if errors.As(err, &deployErr) {
		e.Location = deploymentPath(deployErr.AppID, deployErr.DeploymentID)
	}
	return e
}

func (s *server) mapError(r *http.Request, err error) *Error {
	var apiErr *Error
	var fieldErr *app.FieldError
	var imageErr *deploy.ImageError
	switch {
	case errors.As(err, &apiErr):
		return apiErr
	case errors.As(err, &fieldErr):
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: fieldErr.Field, Message: fieldErr.Message}
	case errors.Is(err, store.ErrNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeNotFound, Message: err.Error()}
	case errors.As(err, &imageErr) && errors.Is(err, deploy.ErrImageNotFound):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeImageNotFound, Field: imageErr.Field, Message: err.Error()}
	case errors.As(err, &imageErr):
		return &Error{Status: http.StatusBadGateway, Code: codeRegistryUnavailable, Field: imageErr.Field, Message: err.Error()}
	case errors.Is(err, deploy.ErrJobConflict):
		return &Error{Status: http.StatusConflict, Code: codeJobConflict, Message: err.Error()}
	case errors.Is(err, deploy.ErrNomadRejected):
		return &Error{Status: http.StatusBadGateway, Code: codeNomadRejected, Message: err.Error()}
	case errors.Is(err, deploy.ErrNomadUnavailable):
		return &Error{Status: http.StatusBadGateway, Code: codeNomadUnavailable, Message: err.Error()}
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		return internalError()
	}
}

func internalError() *Error {
	return &Error{Status: http.StatusInternalServerError, Code: codeInternal, Message: "internal error"}
}
