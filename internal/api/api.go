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
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/secrets"
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
	// public routes skip authentication: the health check, which answers
	// load balancers, and the webhook call, which has its own token.
	public bool
	// quiet routes check a browser session without renewing its idle time:
	// they serve streams that stay open for as long as someone watches, and a
	// forgotten tab must not keep a session alive for ever (research R11).
	quiet bool
}

type server struct {
	svc     *service.Service
	auth    *service.Auth
	store   *store.Store // for the health check only; everything else goes through svc
	version string       // answered by the health check
	proxies httpx.Proxies
	csrf    *http.CrossOriginProtection // for requests authenticated by a session cookie
	log     *slog.Logger

	// The timings of event streams; zero means the defaults (20 s and 30 s).
	// Tests set short ones.
	keepalive, recheck time.Duration
}

func (s *server) routes() []route {
	return []route{
		{http.MethodGet, "/v1/health", s.health, true, false},
		{http.MethodGet, "/v1/apps", s.listApps, false, false},
		{http.MethodPost, "/v1/apps", s.createApp, false, false},
		{http.MethodGet, "/v1/deployments", s.listRecentDeployments, false, false},
		{http.MethodGet, "/v1/attention", s.listAttention, false, false},
		{http.MethodGet, "/v1/projects", s.listProjects, false, false},
		{http.MethodPost, "/v1/projects", s.createProject, false, false},
		{http.MethodGet, "/v1/projects/{project}", s.getProject, false, false},
		{http.MethodPatch, "/v1/projects/{project}", s.updateProject, false, false},
		{http.MethodDelete, "/v1/projects/{project}", s.deleteProject, false, false},
		{http.MethodPost, "/v1/projects/{project}/environments", s.createEnvironment, false, false},
		{http.MethodDelete, "/v1/projects/{project}/environments/{environment}", s.deleteEnvironment, false, false},
		{http.MethodGet, "/v1/projects/{project}/environments/{environment}/apps", s.listEnvironmentApps, false, false},
		{http.MethodPost, "/v1/projects/{project}/environments/{environment}/apps", s.createEnvironmentApp, false, false},
		{http.MethodGet, "/v1/projects/{project}/environments/{environment}/apps/{name}", s.getEnvironmentApp, false, false},
		{http.MethodGet, "/v1/apps/{id}", s.getApp, false, false},
		{http.MethodDelete, "/v1/apps/{id}", s.deleteApp, false, false},
		{http.MethodGet, "/v1/apps/{id}/copies", s.listCopies, false, false},
		{http.MethodGet, "/v1/apps/{id}/volumes", s.listVolumes, false, false},
		{http.MethodPost, "/v1/apps/{id}/copies/{copyId}/run", s.runCommand, false, false},
		{http.MethodPost, "/v1/apps/{id}/stop", s.stopApp, false, false},
		{http.MethodPost, "/v1/apps/{id}/start", s.startApp, false, false},
		{http.MethodPost, "/v1/apps/{id}/restart", s.restartApp, false, false},
		{http.MethodGet, "/v1/apps/{id}/actions", s.listAppActions, false, false},
		{http.MethodGet, "/v1/apps/{id}/logs", s.getLogs, false, true},
		{http.MethodPost, "/v1/apps/{id}/deployments", s.deploy, false, false},
		{http.MethodGet, "/v1/apps/{id}/compose", s.getCompose, false, false},
		{http.MethodPost, "/v1/apps/{id}/compose", s.deployCompose, false, false},
		{http.MethodPost, "/v1/apps/{id}/compose/preview", s.previewCompose, false, false},
		{http.MethodPost, "/v1/apps/{id}/promote/preview", s.previewPromotion, false, false},
		{http.MethodPost, "/v1/apps/{id}/promote", s.promoteApp, false, false},
		{http.MethodPost, "/v1/apps/{id}/copy", s.copyApp, false, false},
		{http.MethodGet, "/v1/backups/destination", s.getBackupDestination, false, false},
		{http.MethodPut, "/v1/backups/destination", s.setBackupDestination, false, false},
		{http.MethodGet, "/v1/registries", s.listRegistries, false, false},
		{http.MethodPut, "/v1/registries/{host}", s.putRegistry, false, false},
		{http.MethodDelete, "/v1/registries/{host}", s.deleteRegistry, false, false},
		{http.MethodPost, "/v1/registries/{host}/test", s.testRegistry, false, false},
		{http.MethodGet, "/v1/notifications/channels", s.listNotificationChannels, false, false},
		{http.MethodPost, "/v1/notifications/channels", s.createNotificationChannel, false, false},
		{http.MethodPatch, "/v1/notifications/channels/{id}", s.updateNotificationChannel, false, false},
		{http.MethodDelete, "/v1/notifications/channels/{id}", s.deleteNotificationChannel, false, false},
		{http.MethodPost, "/v1/notifications/channels/{id}/test", s.testNotificationChannel, false, false},
		{http.MethodGet, "/v1/notifications/channels/{id}/deliveries", s.listNotificationDeliveries, false, false},
		{http.MethodPost, "/v1/notifications/channels/{id}/secret", s.rotateNotificationSecret, false, false},
		{http.MethodGet, "/v1/apps/{id}/backups", s.listBackups, false, false},
		{http.MethodPost, "/v1/apps/{id}/backups", s.createBackup, false, false},
		{http.MethodPost, "/v1/apps/{id}/backups/{backupId}/restore", s.restoreBackup, false, false},
		{http.MethodGet, "/v1/apps/{id}/deployments", s.listDeployments, false, false},
		{http.MethodGet, "/v1/apps/{id}/deployments/{deploymentId}", s.getDeployment, false, false},
		{http.MethodGet, "/v1/apps/{id}/deployments/{deploymentId}/compose", s.getDeploymentCompose, false, false},
		{http.MethodPost, "/v1/apps/{id}/deployments/{deploymentId}/redeploy", s.redeployDeployment, false, false},
		{http.MethodGet, "/v1/apps/{id}/deployments/{deploymentId}/jobspec", s.exportDeployment, false, false},
		{http.MethodGet, "/v1/apps/{id}/jobspec", s.exportApp, false, false},
		{http.MethodPost, "/v1/apps/{id}/webhook", s.createWebhookToken, false, false},
		{http.MethodGet, "/v1/apps/{id}/webhook", s.getWebhook, false, false},
		{http.MethodDelete, "/v1/apps/{id}/webhook", s.deleteWebhookToken, false, false},
		{http.MethodPut, "/v1/account/password", s.changePassword, false, false},
		{http.MethodPost, "/v1/tokens", s.createToken, false, false},
		{http.MethodGet, "/v1/tokens", s.listTokens, false, false},
		{http.MethodDelete, "/v1/tokens/{tokenId}", s.revokeToken, false, false},
		s.webhookRoute(),
	}
}

// webhookRoute is the one route the webhook-only listener serves.
func (s *server) webhookRoute() route {
	return route{http.MethodPost, "/v1/webhooks/{id}", s.callWebhook, true, false}
}

// Deps is what the API needs: both services (apps and deployments, then the
// admin, tokens and sessions), the store for the health check, and the
// trusted proxies that decide which forwarded headers to believe.
type Deps struct {
	Service *service.Service
	Auth    *service.Auth
	Store   *store.Store // only pinged by the health check
	Proxies httpx.Proxies
	Log     *slog.Logger
	// Version is the running build's version, answered by the health check.
	Version string
}

// New returns the API's HTTP handler. Everything goes through the service
// layer, the same one the UI and the webhook use (constitution V). Every
// route except the public ones needs credentials. It doesn't log requests:
// whoever mounts it wraps it in httpx.LogRequests, so pages and API share one
// request log.
func New(d Deps) http.Handler {
	s := newServer(d)
	return s.handler(s.routes())
}

// newServer builds the server's state. Tests use it to change the timings.
func newServer(d Deps) *server {
	return &server{svc: d.Service, auth: d.Auth, store: d.Store, proxies: d.Proxies, log: d.Log, version: d.Version,
		csrf: http.NewCrossOriginProtection()}
}

// NewWebhook returns the handler for the webhook-only listener: only
// POST /v1/webhooks/{id}, with the same JSON errors as New and its own request
// log. Every other path answers 404, so this listener can be exposed while
// the rest of the API stays private (research R7).
func NewWebhook(svc *service.Service, log *slog.Logger) http.Handler {
	s := &server{svc: svc, log: log}
	return httpx.LogRequests(log, s.handler([]route{s.webhookRoute()}))
}

// handler builds the HTTP handler for a route table.
func (s *server) handler(routes []route) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range routes {
		h := rt.handle
		if !rt.public {
			h = s.authenticate(h, rt.quiet)
		}
		mux.Handle(rt.method+" "+rt.path, s.handle(h))
	}
	return s.jsonOnly(mux)
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
	if e := backupError(err); e != nil {
		return e
	}
	if e := actionError(err); e != nil {
		return e
	}
	if e := versionError(err); e != nil {
		return e
	}
	if e := placeError(err); e != nil {
		return e
	}
	switch {
	case errors.As(err, &apiErr):
		return apiErr
	case errors.As(err, &fieldErr):
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: fieldErr.Field, Message: fieldErr.Message}
	case errors.Is(err, service.ErrUnauthorized), errors.Is(err, service.ErrNotAuthenticated):
		return &Error{Status: http.StatusUnauthorized, Code: codeUnauthorized, Message: err.Error(), Authenticate: "Bearer"}
	case errors.Is(err, service.ErrWrongPassword):
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "currentPassword", Message: err.Error()}
	case errors.Is(err, service.ErrAppExists):
		return &Error{Status: http.StatusConflict, Code: codeAppExists, Field: "id", Message: err.Error()}
	case errors.Is(err, service.ErrAppNotDefined):
		return &Error{Status: http.StatusConflict, Code: codeAppNotDefined, Message: err.Error()}
	case errors.Is(err, service.ErrAppChanged):
		return &Error{Status: http.StatusPreconditionFailed, Code: codeAppChanged, Message: err.Error()}
	case errors.Is(err, service.ErrNotAWebhook):
		return &Error{Status: http.StatusConflict, Code: codeNotAWebhook, Message: err.Error()}
	case errors.Is(err, service.ErrTokenNameTaken):
		return &Error{Status: http.StatusConflict, Code: codeTokenNameTaken, Field: "name", Message: err.Error()}
	case errors.Is(err, store.ErrNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeNotFound, Message: err.Error()}
	case errors.As(err, &imageErr) && errors.Is(err, deploy.ErrImageNotFound):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeImageNotFound, Field: imageErr.Field, Message: err.Error()}
	case errors.As(err, &imageErr) && errors.Is(err, deploy.ErrRegistryAuth):
		// The stored credentials are what to fix, so it is the request's to
		// answer (422), like a missing image, and not a registry failure (502).
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeRegistryAuthFailed, Field: imageErr.Field, Message: err.Error()}
	case errors.As(err, &imageErr):
		return &Error{Status: http.StatusBadGateway, Code: codeRegistryUnavailable, Field: imageErr.Field, Message: err.Error()}
	case errors.Is(err, deploy.ErrJobConflict):
		return &Error{Status: http.StatusConflict, Code: codeJobConflict, Message: err.Error()}
	case errors.Is(err, deploy.ErrNomadRejected):
		return &Error{Status: http.StatusBadGateway, Code: codeNomadRejected, Message: err.Error()}
	case errors.Is(err, deploy.ErrNomadUnavailable):
		return &Error{Status: http.StatusBadGateway, Code: codeNomadUnavailable, Message: err.Error()}
	case errors.Is(err, secrets.ErrUnavailable):
		return &Error{Status: http.StatusBadGateway, Code: codeSecretStoreUnavail, Message: err.Error()}
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		return internalError()
	}
}

func internalError() *Error {
	return &Error{Status: http.StatusInternalServerError, Code: codeInternal, Message: "internal error"}
}
