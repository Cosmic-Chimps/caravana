package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Previews (feature 049): a preview is an environment named pr-<n> and the apps
// in it. The settings, the list and closing one whole are for full tokens and
// the console; a deploy token only creates and deletes its own preview, through
// the calls it already had (see scope.go). No answer here holds the preview
// password or its hash.

// The codes only previews give.
const (
	codePreviewLimit    = "preview_limit"
	codePreviewsOpen    = "previews_open"
	codeNotAPreview     = "not_a_preview"
	codePreviewPassword = "preview_password"
)

// previewsError maps the preview errors to their codes; mapError calls it.
func previewsError(err error) *Error {
	var limit *service.PreviewLimitError
	var open *service.PreviewsOpenError
	var password *service.PreviewPasswordError
	switch {
	case errors.As(err, &limit):
		return &Error{Status: http.StatusConflict, Code: codePreviewLimit, Field: "name", Message: limit.Error()}
	case errors.As(err, &open):
		return &Error{Status: http.StatusConflict, Code: codePreviewsOpen, Field: "removePassword", Message: open.Error()}
	case errors.Is(err, service.ErrNotAPreview):
		return &Error{Status: http.StatusConflict, Code: codeNotAPreview, Field: "environment", Message: err.Error()}
	case errors.As(err, &password):
		return &Error{Status: http.StatusConflict, Code: codePreviewPassword, Field: "host", Message: password.Error()}
	}
	return nil
}

// previewSettingsJSON is a project's preview settings (schema PreviewSettings).
type previewSettingsJSON struct {
	Limit           int  `json:"limit"`
	ExpireAfterDays int  `json:"expireAfterDays"`
	PasswordSet     bool `json:"passwordSet"`
}

func toPreviewSettingsJSON(s service.PreviewSettings) previewSettingsJSON {
	return previewSettingsJSON{Limit: s.Limit, ExpireAfterDays: s.ExpireAfterDays, PasswordSet: s.PasswordSet}
}

// previewDeploymentJSON is an app's newest deployment as a preview shows it.
type previewDeploymentJSON struct {
	ID        string      `json:"id"`
	Status    string      `json:"status"`
	CreatedAt time.Time   `json:"createdAt"`
	Trigger   app.Trigger `json:"trigger"`
	TokenName string      `json:"tokenName,omitempty"`
	Origin    *originJSON `json:"origin,omitempty"`
}

// previewAppJSON is one app of a preview (schema PreviewApp).
type previewAppJSON struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	State          string                 `json:"state"`
	Addresses      []string               `json:"addresses"`
	MemoryMB       int                    `json:"memoryMB"`
	LastDeployment *previewDeploymentJSON `json:"lastDeployment,omitempty"`
}

// previewJSON is one open preview (schema Preview).
type previewJSON struct {
	Environment string           `json:"environment"`
	Number      int              `json:"number"`
	CreatedAt   time.Time        `json:"createdAt"`
	ExpiresAt   time.Time        `json:"expiresAt"`
	Apps        []previewAppJSON `json:"apps"`
}

// previewListJSON is the answer of getPreviews (schema PreviewList).
type previewListJSON struct {
	Project  string              `json:"project"`
	Settings previewSettingsJSON `json:"settings"`
	Previews []previewJSON       `json:"previews"`
}

func toPreviewListJSON(l service.PreviewList) previewListJSON {
	out := previewListJSON{Project: l.Project, Settings: toPreviewSettingsJSON(l.Settings), Previews: make([]previewJSON, 0, len(l.Previews))}
	for _, p := range l.Previews {
		pj := previewJSON{Environment: p.Environment, Number: p.Number, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
			Apps: make([]previewAppJSON, 0, len(p.Apps))}
		for _, a := range p.Apps {
			aj := previewAppJSON{ID: a.ID, Name: a.Name, State: string(a.State), Addresses: a.Addresses, MemoryMB: a.MemoryMB}
			if aj.Addresses == nil {
				aj.Addresses = []string{}
			}
			if d := a.LastDeployment; d != nil {
				aj.LastDeployment = &previewDeploymentJSON{ID: d.Deployment.ID, Status: string(d.Status), CreatedAt: d.CreatedAt,
					Trigger: d.Deployment.Trigger, TokenName: d.Deployment.TokenName, Origin: toOriginJSON(d.Deployment.Origin)}
			}
			pj.Apps = append(pj.Apps, aj)
		}
		out.Previews = append(out.Previews, pj)
	}
	return out
}

// getPreviews answers GET /v1/projects/{project}/previews.
func (s *server) getPreviews(w http.ResponseWriter, r *http.Request) error {
	list, err := s.svc.Previews(r.Context(), r.PathValue("project"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toPreviewListJSON(list))
	return nil
}

// previewSettingsResultJSON is the answer of putPreviewSettings (schema
// PreviewSettingsResult): the settings and what to know about the change.
type previewSettingsResultJSON struct {
	previewSettingsJSON
	Warnings []app.Warning `json:"warnings"`
}

// putPreviewSettings answers PUT /v1/projects/{project}/previews/settings. Every
// field of the body is optional; an absent one keeps what the project has.
func (s *server) putPreviewSettings(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Limit           *int    `json:"limit"`
		ExpireAfterDays *int    `json:"expireAfterDays"`
		Password        *string `json:"password"`
		RemovePassword  bool    `json:"removePassword"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.svc.SetPreviewSettings(r.Context(), r.PathValue("project"), service.PreviewSettingsInput{
		Limit: in.Limit, ExpireAfterDays: in.ExpireAfterDays, Password: in.Password, RemovePassword: in.RemovePassword})
	if err != nil {
		return registryInvalid(err)
	}
	out := previewSettingsResultJSON{previewSettingsJSON: toPreviewSettingsJSON(res.Settings), Warnings: res.Warnings}
	if out.Warnings == nil {
		out.Warnings = []app.Warning{}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// closePreview answers DELETE /v1/projects/{project}/previews/{environment}
// with 204: every app of the preview is deleted, then the environment.
func (s *server) closePreview(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.ClosePreview(r.Context(), r.PathValue("project"), r.PathValue("environment"), service.CloseByOwner); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
