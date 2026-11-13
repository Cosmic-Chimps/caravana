package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

// The template codes (feature 035).
const (
	codeTemplateNotFound    = "template_not_found"
	codeTemplateUnavailable = "template_unavailable"
)

// templatesError maps the template refusals to their codes; mapError calls it.
func templatesError(err error) *Error {
	var unavailable *service.TemplateUnavailableError
	switch {
	case errors.Is(err, service.ErrTemplateNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeTemplateNotFound, Field: "name", Message: err.Error()}
	case errors.As(err, &unavailable):
		return &Error{Status: http.StatusConflict, Code: codeTemplateUnavailable, Field: "name", Message: err.Error()}
	}
	return nil
}

// templateCatalogJSON is the state of the catalog in use (schema TemplateCatalog).
type templateCatalogJSON struct {
	Version         string     `json:"version"`
	Source          string     `json:"source"`
	EmbeddedVersion string     `json:"embeddedVersion"`
	Checking        bool       `json:"checking"`
	CheckedAt       *time.Time `json:"checkedAt"`
	CheckedOKAt     *time.Time `json:"checkedOkAt"`
	FetchedAt       *time.Time `json:"fetchedAt"`
	Error           string     `json:"error"`
}

func toTemplateCatalogJSON(v service.TemplateCatalogView) templateCatalogJSON {
	return templateCatalogJSON{Version: v.Version, Source: v.Source, EmbeddedVersion: v.EmbeddedVersion,
		Checking: v.Checking, CheckedAt: timePtr(v.CheckedAt), CheckedOKAt: timePtr(v.CheckedOKAt),
		FetchedAt: timePtr(v.FetchedAt), Error: v.Error}
}

// templateSummaryJSON is a template in a list (schema TemplateSummary).
type templateSummaryJSON struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Upstream    string   `json:"upstream"`
	HasLogo     bool     `json:"hasLogo"`
	Available   bool     `json:"available"`
	Unavailable string   `json:"unavailable"`
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func toTemplateSummaryJSON(t service.TemplateInfo) templateSummaryJSON {
	return templateSummaryJSON{Name: t.Name, Title: t.Title, Version: t.Version, Description: t.Description,
		Tags: nonNil(t.Tags), Upstream: t.Upstream, HasLogo: t.Logo != "", Available: t.Available, Unavailable: t.Unavailable}
}

// templateJSON is one template (schema Template): the manifest as the catalog
// has it, with its Compose text and placeholders, and no logo (a route of its own).
type templateJSON struct {
	Name        string                     `json:"name"`
	Title       string                     `json:"title"`
	Version     string                     `json:"version"`
	Description string                     `json:"description"`
	Tags        []string                   `json:"tags"`
	Upstream    string                     `json:"upstream"`
	AppName     string                     `json:"appName"`
	Notes       string                     `json:"notes,omitempty"`
	Requires    string                     `json:"requires,omitempty"`
	Broken      string                     `json:"broken,omitempty"`
	Lab         bool                       `json:"lab"`
	Inputs      []templates.Input          `json:"inputs"`
	Generated   []templates.Generated      `json:"generated"`
	Services    map[string]compose.Overlay `json:"services"`
	Compose     string                     `json:"compose"`
	HasLogo     bool                       `json:"hasLogo"`
	Available   bool                       `json:"available"`
	Unavailable string                     `json:"unavailable"`
}

func toTemplateJSON(t service.TemplateInfo) templateJSON {
	services := t.Services
	if services == nil {
		services = map[string]compose.Overlay{}
	}
	return templateJSON{Name: t.Name, Title: t.Title, Version: t.Version, Description: t.Description,
		Tags: nonNil(t.Tags), Upstream: t.Upstream, AppName: t.AppName, Notes: t.Notes, Requires: t.Requires,
		Broken: t.Broken, Lab: t.InLab(), Inputs: nonNil(t.Inputs), Generated: nonNil(t.Generated),
		Services: services, Compose: t.Compose, HasLogo: t.Logo != "", Available: t.Available, Unavailable: t.Unavailable}
}

// listTemplates answers GET /v1/templates: the catalog in use and its
// templates, filtered by ?q= (a case-insensitive part of the title, description
// or a tag) and ?tag= (exact).
func (s *server) listTemplates(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	list, err := s.svc.ListTemplates(r.Context(), q.Get("q"), q.Get("tag"))
	if err != nil {
		return err
	}
	out := struct {
		Catalog   templateCatalogJSON   `json:"catalog"`
		Templates []templateSummaryJSON `json:"templates"`
	}{Catalog: toTemplateCatalogJSON(list.Catalog), Templates: make([]templateSummaryJSON, 0, len(list.Templates))}
	for _, t := range list.Templates {
		out.Templates = append(out.Templates, toTemplateSummaryJSON(t))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// getTemplate answers GET /v1/templates/{name}.
func (s *server) getTemplate(w http.ResponseWriter, r *http.Request) error {
	t, err := s.svc.Template(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toTemplateJSON(t))
	return nil
}

// getTemplateLogo answers GET /v1/templates/{name}/logo: the template's SVG,
// served so that it runs nothing (research R8).
func (s *server) getTemplateLogo(w http.ResponseWriter, r *http.Request) error {
	t, err := s.svc.Template(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	if t.Logo == "" {
		return &Error{Status: http.StatusNotFound, Code: codeTemplateNotFound, Field: "name",
			Message: "The " + t.Name + " template has no logo."}
	}
	h := w.Header()
	h.Set("Content-Type", templates.LogoContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", templates.LogoCSP)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(t.Logo))
	return nil
}

// templateInstallRequest is the body of previewTemplate and installTemplate
// (schema TemplateInstallRequest).
type templateInstallRequest struct {
	ID     string            `json:"id"`
	Inputs map[string]string `json:"inputs"`
	// WithoutBackups leaves the template's backup policies out, each with a
	// warning. The owner's explicit choice for an installation with no backup
	// destination yet.
	WithoutBackups bool `json:"withoutBackups"`
}

func (r templateInstallRequest) options() service.TemplateInstallOptions {
	return service.TemplateInstallOptions{WithoutBackups: r.WithoutBackups}
}

func (s *server) readInstallRequest(r *http.Request) (templateInstallRequest, error) {
	var req templateInstallRequest
	if err := decodeJSON(r, &req); err != nil {
		return req, err
	}
	if req.ID == "" {
		return req, &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "id", Message: "is required"}
	}
	return req, nil
}

// previewTemplate answers POST /v1/templates/{name}/preview: the app the
// template makes with these inputs, its warnings and the rendered file.
// Nothing is generated, saved or deployed.
func (s *server) previewTemplate(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	req, err := s.readInstallRequest(r)
	if err != nil {
		return err
	}
	p, err := s.svc.PreviewTemplate(r.Context(), r.PathValue("name"), req.ID, req.Inputs, req.options())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		App      app.App       `json:"app"`
		Warnings []app.Warning `json:"warnings"`
		Compose  string        `json:"compose"`
	}{p.App, nonNil(p.Warnings), p.Compose})
	return nil
}

// installTemplate answers POST /v1/templates/{name}/install: it renders the
// template and deploys it as a new app, like deployCompose with If-None-Match: *,
// and answers as it does. The answer may hold Team-only passwords, shown once.
func (s *server) installTemplate(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))
	ctx, err := withOrigin(r)
	if err != nil {
		return err
	}
	req, err := s.readInstallRequest(r)
	if err != nil {
		return err
	}
	res, err := s.svc.InstallTemplate(ctx, r.PathValue("name"), req.ID, req.Inputs, req.options(), app.TriggerManual)
	if err != nil {
		return err
	}
	writeDeployResult(w, req.ID, res)
	return nil
}

// checkTemplates answers POST /v1/templates/check: starts a check of the
// catalog repository and answers 202 at once.
func (s *server) checkTemplates(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.CheckTemplates(r.Context()); err != nil {
		var soon *service.CheckTooSoonError
		switch {
		case errors.As(err, &soon):
			if wait := time.Until(soon.Again); wait > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			}
			return &Error{Status: http.StatusTooManyRequests, Code: codeCheckTooSoon, Message: err.Error()}
		case errors.Is(err, service.ErrCheckDisabled):
			return &Error{Status: http.StatusConflict, Code: codeCheckDisabled, Message: err.Error()}
		}
		return err
	}
	view, err := s.svc.TemplateCatalog(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, struct {
		Catalog templateCatalogJSON `json:"catalog"`
	}{toTemplateCatalogJSON(view)})
	return nil
}

// putTemplateSettings answers PUT /v1/templates/settings: whether Caravana
// checks the catalog repository. Off means no request at all.
func (s *server) putTemplateSettings(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Checking *bool `json:"checking"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Checking == nil {
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "checking", Message: "is required"}
	}
	if err := s.svc.SetTemplateChecking(r.Context(), *in.Checking); err != nil {
		return err
	}
	view, err := s.svc.TemplateCatalog(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toTemplateCatalogJSON(view))
	return nil
}

// templateRef is the template an app came from (schema TemplateRef).
type templateRefJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// templateRefOf is nil for an app that wasn't installed from a template.
func templateRefOf(t store.AppTemplate) *templateRefJSON {
	if !t.Set() {
		return nil
	}
	return &templateRefJSON{Name: t.Name, Version: t.Version}
}
