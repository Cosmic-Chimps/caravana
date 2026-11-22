package api

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Deploy tokens (feature 020). An API token may carry a scope: one app name in
// one project. With such a token only the operations in scopeRules answer, and
// only for the app in the scope; everything else is 403 token_scope, before any
// handler runs. The rule is default-deny: a route that has no entry in
// scopeRules is out of reach of a scoped token, so a new route is safe until
// someone decides otherwise (and TestScopedTokenWalksEveryRoute makes them
// decide in the test too).

// scopeFinder says which app a request is about, for a route a deploy token may
// use on its own app: the project and the app's name. ok is false when the
// request doesn't say (a bad ID, a body that isn't JSON), which counts as out of
// scope.
type scopeFinder func(r *http.Request) (project, name string, ok bool)

// scopeRule decides whether a deploy token for sc may make this request. A rule
// reads only what the request itself says (its path and body), never the
// stored state: the answer is the same whether or not the thing exists.
type scopeRule func(r *http.Request, sc service.TokenScope) bool

// ownApp is the rule of most routes: the request names, through find, the one
// app in the scope.
func ownApp(find scopeFinder) scopeRule {
	return func(r *http.Request, sc service.TokenScope) bool {
		project, name, ok := find(r)
		return ok && project == sc.Project && name == sc.App
	}
}

// scopeRules is the allowlist, keyed by "METHOD pattern" as routes() writes
// them. It is the table of specs/020-deploy-from-ci/contracts/api.md, with the
// three calls of previews (specs/049-previews/contracts/api.md): a deploy token
// may open and close a preview of its own project, and delete its own app only
// inside one.
var scopeRules = map[string]scopeRule{
	// The app named by {id}.
	"GET /v1/apps/{id}":                            ownApp(pathID),
	"GET /v1/apps/{id}/compose":                    ownApp(pathID),
	"GET /v1/apps/{id}/deployments":                ownApp(pathID),
	"GET /v1/apps/{id}/deployments/{deploymentId}": ownApp(pathID),
	"POST /v1/apps/{id}/deployments":               ownApp(pathID),
	"POST /v1/apps/{id}/compose":                   ownApp(pathID),
	"POST /v1/apps/{id}/promote/preview":           ownApp(pathID),
	"POST /v1/apps/{id}/promote":                   ownApp(pathID),
	"POST /v1/apps/{id}/secrets/refresh":           ownApp(pathID),
	// createApp: the app named by the body's id.
	"POST /v1/apps": ownApp(bodyID),
	// The place calls: the project in the path, the app by name.
	"POST /v1/projects/{project}/environments/{environment}/apps":       ownApp(pathProjectBodyName),
	"GET /v1/projects/{project}/environments/{environment}/apps/{name}": ownApp(pathProjectPathName),
	// Previews (feature 049). Opening one: the project is the token's and the
	// body's name is a preview name (pr-<n>), nothing else.
	"POST /v1/projects/{project}/environments": func(r *http.Request, sc service.TokenScope) bool {
		var in struct {
			Name string `json:"name"`
		}
		return r.PathValue("project") == sc.Project && peekBody(r, &in) && app.IsPreviewEnvironment(in.Name)
	},
	// Closing one's environment: the project is the token's and the
	// environment is a preview. Emptiness is the handler's (409 not_empty).
	"DELETE /v1/projects/{project}/environments/{environment}": func(r *http.Request, sc service.TokenScope) bool {
		return r.PathValue("project") == sc.Project && app.IsPreviewEnvironment(r.PathValue("environment"))
	},
	// Deleting its own app, only in a preview: in staging or production it is
	// the owner's to delete.
	"DELETE /v1/apps/{id}": func(r *http.Request, sc service.TokenScope) bool {
		place, err := app.PlaceOf(r.PathValue("id"))
		return err == nil && place.Project == sc.Project && place.Name == sc.App && app.IsPreviewEnvironment(place.Environment)
	},
}

// pathID reads the app from the {id} of the path.
func pathID(r *http.Request) (project, name string, ok bool) {
	return placeOfID(r.PathValue("id"))
}

// bodyID reads the app from the "id" of the JSON body.
func bodyID(r *http.Request) (project, name string, ok bool) {
	var in struct {
		ID string `json:"id"`
	}
	if !peekBody(r, &in) {
		return "", "", false
	}
	return placeOfID(in.ID)
}

// pathProjectBodyName reads the project from the path and the app's name from
// the "appName" of the JSON body.
func pathProjectBodyName(r *http.Request) (project, name string, ok bool) {
	var in struct {
		AppName string `json:"appName"`
	}
	if !peekBody(r, &in) {
		return "", "", false
	}
	return r.PathValue("project"), in.AppName, true
}

// pathProjectPathName reads the project and the app's name from the path.
func pathProjectPathName(r *http.Request) (project, name string, ok bool) {
	return r.PathValue("project"), r.PathValue("name"), true
}

// placeOfID is the project and name an app ID spells (app.PlaceOf).
func placeOfID(id string) (project, name string, ok bool) {
	p, err := app.PlaceOf(id)
	if err != nil {
		return "", "", false
	}
	return p.Project, p.Name, true
}

// peekBody reads the JSON body into v, which names only the fields of
// interest, and puts the body back for the handler. It decodes with the same
// library as decodeJSON, which is case-sensitive and refuses duplicate names,
// so the handler reads the same value. A body that is too large, or isn't JSON
// for v, reports false: whatever the request is, it is not shown to be in scope.
func peekBody(r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxBodyBytes {
		return false
	}
	return json.Unmarshal(body, v) == nil
}

// scopeAllows is the check of a deploy token against a route: nil when the
// route is in scopeRules and its rule accepts the request for the scope, else
// the 403.
func scopeAllows(r *http.Request, rt route, sc service.TokenScope) error {
	if rule, listed := scopeRules[rt.method+" "+rt.path]; listed && rule(r, sc) {
		return nil
	}
	return &Error{Status: http.StatusForbidden, Code: codeTokenScope, Message: sc.Describe()}
}
