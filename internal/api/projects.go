package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Projects and environments (feature 019). Everything here goes through the
// service. Where a request names a project, environment or app name and it is
// not a label, the answer is 422 invalid_field, as for the registry calls
// (registryInvalid does that); a body that can't be read is 400, as ever.

// environmentJSON is one environment of a project (schema Environment).
type environmentJSON struct {
	Project  string `json:"project"`
	Name     string `json:"name"`
	Position int    `json:"position"`
	Apps     int    `json:"apps"`
}

// projectJSON is a project with its environments (schema Project).
type projectJSON struct {
	Name         string            `json:"name"`
	DisplayName  string            `json:"displayName"`
	Environments []environmentJSON `json:"environments"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

func toEnvironmentJSON(e service.Environment) environmentJSON {
	return environmentJSON{Project: e.Project, Name: e.Name, Position: e.Position, Apps: e.Apps}
}

func toProjectJSON(p service.Project) projectJSON {
	envs := make([]environmentJSON, 0, len(p.Environments))
	for _, e := range p.Environments {
		envs = append(envs, toEnvironmentJSON(e))
	}
	return projectJSON{Name: p.ID, DisplayName: p.DisplayName, Environments: envs, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// projectPath is a project's URL, for Location.
func projectPath(name string) string { return "/v1/projects/" + name }

// placeError maps the service's refusals of a project or environment to their
// answers. The field is the one the service says named the place ("id" for an
// app ID, "to" for a promotion target) and otherwise the path segment it is.
// It returns nil for any other error.
func placeError(err error) *Error {
	var pe *service.PlaceError
	field := func(fallback string) string {
		if errors.As(err, &pe) {
			return pe.Field
		}
		return fallback
	}
	switch {
	case errors.Is(err, service.ErrProjectNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeProjectNotFound, Field: field("project"), Message: err.Error()}
	case errors.Is(err, service.ErrEnvironmentNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeEnvironmentNotFound, Field: field("environment"), Message: err.Error()}
	case errors.Is(err, service.ErrProjectExists):
		return &Error{Status: http.StatusConflict, Code: codeProjectExists, Field: "name", Message: err.Error()}
	case errors.Is(err, service.ErrEnvironmentExists):
		return &Error{Status: http.StatusConflict, Code: codeEnvironmentExists, Field: "name", Message: err.Error()}
	case errors.Is(err, service.ErrNotEmpty):
		return &Error{Status: http.StatusConflict, Code: codeNotEmpty, Message: err.Error()}
	case errors.Is(err, service.ErrProtected):
		return &Error{Status: http.StatusConflict, Code: codeProtected, Message: err.Error()}
	}
	return nil
}

// listProjects answers GET /v1/projects: Default first, then by creation.
func (s *server) listProjects(w http.ResponseWriter, r *http.Request) error {
	projects, err := s.svc.Projects(r.Context())
	if err != nil {
		return err
	}
	out := make([]projectJSON, 0, len(projects))
	for _, p := range projects {
		out = append(out, toProjectJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createProject answers POST /v1/projects with 201: the project, with its
// production environment.
func (s *server) createProject(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	p, err := s.svc.CreateProject(r.Context(), in.Name, in.DisplayName)
	if err != nil {
		return registryInvalid(err)
	}
	w.Header().Set("Location", projectPath(p.ID))
	writeJSON(w, http.StatusCreated, toProjectJSON(p))
	return nil
}

// getProject answers GET /v1/projects/{project}.
func (s *server) getProject(w http.ResponseWriter, r *http.Request) error {
	p, err := s.svc.Project(r.Context(), r.PathValue("project"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toProjectJSON(p))
	return nil
}

// updateProject answers PATCH /v1/projects/{project}: the display name, the
// only thing about a project that can change (its name is part of its apps' IDs).
func (s *server) updateProject(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		DisplayName *string `json:"displayName"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.DisplayName == nil {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: "displayName", Message: "is required"}
	}
	p, err := s.svc.SetProjectDisplayName(r.Context(), r.PathValue("project"), *in.DisplayName)
	if err != nil {
		return registryInvalid(err)
	}
	writeJSON(w, http.StatusOK, toProjectJSON(p))
	return nil
}

// deleteProject answers DELETE /v1/projects/{project}.
func (s *server) deleteProject(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteProject(r.Context(), r.PathValue("project")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// createEnvironment answers POST /v1/projects/{project}/environments with 201.
func (s *server) createEnvironment(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	env, err := s.svc.CreateEnvironment(r.Context(), r.PathValue("project"), in.Name)
	if err != nil {
		return registryInvalid(err)
	}
	writeJSON(w, http.StatusCreated, toEnvironmentJSON(env))
	return nil
}

// deleteEnvironment answers DELETE
// /v1/projects/{project}/environments/{environment}.
func (s *server) deleteEnvironment(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteEnvironment(r.Context(), r.PathValue("project"), r.PathValue("environment")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// listEnvironmentApps answers GET
// /v1/projects/{project}/environments/{environment}/apps.
func (s *server) listEnvironmentApps(w http.ResponseWriter, r *http.Request) error {
	project, environment := r.PathValue("project"), r.PathValue("environment")
	// ListAppsIn treats "" as "any", which a path segment never is.
	sums, err := s.svc.ListAppsIn(r.Context(), project, environment)
	if err != nil {
		return err
	}
	out := make([]appSummaryJSON, 0, len(sums))
	for _, sum := range sums {
		out = append(out, toAppSummaryJSON(sum))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// environmentAppRequest is the body of createEnvironmentApp (schema
// EnvironmentAppRequest): an App, plus the name it has in the environment.
// json/v2 reads the embedded App's fields as if they were this struct's own.
type environmentAppRequest struct {
	app.App
	AppName string `json:"appName"`
}

// createEnvironmentApp answers POST
// /v1/projects/{project}/environments/{environment}/apps exactly as createApp
// does (202 and the first deployment), for the ID the three names make.
func (s *server) createEnvironmentApp(w http.ResponseWriter, r *http.Request) error {
	// Best effort, as for createApp: a writer that can't change deadlines keeps the server's.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	var in environmentAppRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	place, err := s.svc.ResolvePlace(r.Context(), r.PathValue("project"), r.PathValue("environment"), in.AppName)
	var fe *app.FieldError
	if errors.As(err, &fe) {
		// The name came in the body, not the path.
		return registryInvalid(app.NewFieldError("appName", "%s", fe.Message))
	}
	if err != nil {
		return err
	}
	id := place.ID()
	switch in.App.ID {
	case "", id:
	default:
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "id",
			Message: fmt.Sprintf("%q doesn't match the ID of this place (%q)", in.App.ID, id)}
	}
	a := in.App
	a.ID = id
	rec, err := s.svc.CreateApp(r.Context(), a, app.TriggerManual)
	if err != nil {
		return err
	}
	w.Header().Set("Location", deploymentPath(rec.AppID, rec.Deployment.ID))
	writeJSON(w, http.StatusAccepted, toDeploymentJSON(rec))
	return nil
}

// getEnvironmentApp answers GET
// /v1/projects/{project}/environments/{environment}/apps/{name} as getApp does.
func (s *server) getEnvironmentApp(w http.ResponseWriter, r *http.Request) error {
	place, err := s.svc.ResolvePlace(r.Context(), r.PathValue("project"), r.PathValue("environment"), r.PathValue("name"))
	if err != nil {
		return registryInvalid(err) // a name that isn't a label is 422, as everywhere here
	}
	rec, err := s.svc.GetApp(r.Context(), place.ID())
	if err != nil {
		return err
	}
	if rec.Place != place {
		// An app from before projects whose ID happens to spell this place
		// (an ID like "x--y--z" in Default) is not in it.
		return fmt.Errorf("app %q is not in %s/%s: %w", place.ID(), place.Project, place.Environment, store.ErrNotFound)
	}
	writeAppRecord(w, rec)
	return nil
}
