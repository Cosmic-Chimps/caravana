package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Promote and copy (feature 019). The body of all three is {"to": "<environment>"}:
// an environment of the app's own project. Everything goes through the service.

// codes of the refusals only these operations make.
const (
	codeAppNotInEnvironment     = "app_not_in_environment"
	codePromotionRemovesService = "promotion_removes_service"
)

// promotionRequest is the body of previewPromotion, promoteApp and copyApp
// (schema PromotionRequest).
type promotionRequest struct {
	To string `json:"to"`
}

// serviceChangeJSON is what a promotion does to one service (schema
// PromotionServiceChange).
type serviceChangeJSON struct {
	Name       string   `json:"name"`
	ImageFrom  string   `json:"imageFrom,omitempty"`
	ImageTo    string   `json:"imageTo"`
	DigestFrom string   `json:"digestFrom,omitempty"`
	DigestTo   string   `json:"digestTo"`
	Changed    []string `json:"changed"`
}

// promotionPreviewJSON is what promoting would deploy (schema PromotionPreview).
type promotionPreviewJSON struct {
	From            string              `json:"from"`
	To              string              `json:"to"`
	FromEnvironment string              `json:"fromEnvironment"`
	ToEnvironment   string              `json:"toEnvironment"`
	FromDeployment  string              `json:"fromDeployment"`
	Services        []serviceChangeJSON `json:"services"`
	Added           []string            `json:"added"`
	Warnings        []app.Warning       `json:"warnings"`
	TargetRevision  string              `json:"targetRevision"`
}

func toPromotionPreviewJSON(p service.PromotionPreview) promotionPreviewJSON {
	j := promotionPreviewJSON{
		From: p.From, To: p.To, FromEnvironment: p.FromEnvironment, ToEnvironment: p.ToEnvironment,
		FromDeployment: p.FromDeployment, Services: make([]serviceChangeJSON, 0, len(p.Services)),
		Added: p.Added, Warnings: p.Warnings, TargetRevision: p.TargetRevision,
	}
	for _, c := range p.Services {
		changed := c.Changed
		if changed == nil {
			changed = []string{} // never null: clients can always iterate
		}
		j.Services = append(j.Services, serviceChangeJSON{Name: c.Name, ImageFrom: c.ImageFrom, ImageTo: c.ImageTo,
			DigestFrom: c.DigestFrom, DigestTo: c.DigestTo, Changed: changed})
	}
	if j.Added == nil {
		j.Added = []string{}
	}
	if j.Warnings == nil {
		j.Warnings = []app.Warning{}
	}
	return j
}

// missingSecretJSON is a secret a copy needs a value for (schema MissingSecret).
type missingSecretJSON struct {
	Service string `json:"service"`
	Name    string `json:"name"`
}

// copyResultJSON is the answer of copyApp (schema CopyResult): the new app as
// getApp shows it, and the secrets it has no value for yet.
type copyResultJSON struct {
	appRecordJSON
	MissingSecrets []missingSecretJSON `json:"missingSecrets"`
}

// promotionFailure maps the refusals of promoting and copying to their
// answers; any other error passes on for mapError. Those with a field of their
// own (the target environment, a service) say which; a field error on "to" is
// 422 like the other names of this API's place calls, where a field error on
// anything else keeps the status every deploy gives it.
func promotionFailure(err error) error {
	var re *service.RefusalError
	if errors.As(err, &re) {
		switch {
		case errors.Is(err, service.ErrAppNotInEnvironment):
			return &Error{Status: http.StatusNotFound, Code: codeAppNotInEnvironment, Field: re.Field, Message: err.Error()}
		case errors.Is(err, service.ErrPromotionRemovesService):
			return &Error{Status: http.StatusConflict, Code: codePromotionRemovesService, Field: re.Field, Message: err.Error()}
		case errors.Is(err, service.ErrAppExists):
			return &Error{Status: http.StatusConflict, Code: codeAppExists, Field: re.Field, Message: err.Error()}
		}
	}
	var fe *app.FieldError
	if errors.As(err, &fe) && fe.Field == "to" {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	}
	return err
}

// previewPromotion answers POST /v1/apps/{id}/promote/preview: what promoting
// would deploy, with the target's revision. It changes nothing.
func (s *server) previewPromotion(w http.ResponseWriter, r *http.Request) error {
	var in promotionRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	p, err := s.svc.PreviewPromotion(r.Context(), r.PathValue("id"), in.To)
	if err != nil {
		return promotionFailure(err)
	}
	writeJSON(w, http.StatusOK, toPromotionPreviewJSON(p))
	return nil
}

// promoteApp answers POST /v1/apps/{id}/promote with 202 once Nomad has
// accepted the target's job, like a deploy. An If-Match header is the revision
// of the target the caller read (previewPromotion gives it): 412 app_changed
// when the target moved. The Location is the target's deployment.
func (s *server) promoteApp(w http.ResponseWriter, r *http.Request) error {
	// Best effort, as for deployApp: a writer that can't change deadlines keeps the server's.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	ctx, err := withOrigin(r)
	if err != nil {
		return err
	}
	var in promotionRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	cond, err := ifMatch(r)
	if err != nil {
		return err
	}
	rec, err := s.svc.Promote(ctx, r.PathValue("id"), in.To, cond, app.TriggerManual)
	if err != nil {
		return promotionFailure(err)
	}
	w.Header().Set("Location", deploymentPath(rec.AppID, rec.Deployment.ID))
	writeJSON(w, http.StatusAccepted, toDeploymentJSON(rec))
	return nil
}

// copyApp answers POST /v1/apps/{id}/copy with 201: the app in the other
// environment, not deployed, and the secrets it needs values for.
func (s *server) copyApp(w http.ResponseWriter, r *http.Request) error {
	var in promotionRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	rec, missing, err := s.svc.CopyApp(r.Context(), r.PathValue("id"), in.To)
	if err != nil {
		return promotionFailure(err)
	}
	out := copyResultJSON{appRecordJSON: toAppRecordJSON(rec), MissingSecrets: make([]missingSecretJSON, 0, len(missing))}
	for _, m := range missing {
		out.MissingSecrets = append(out.MissingSecrets, missingSecretJSON{Service: m.Service, Name: m.Name})
	}
	w.Header().Set("Location", "/v1/apps/"+rec.App.ID)
	w.Header().Set("ETag", `"`+rec.Revision+`"`)
	writeJSON(w, http.StatusCreated, out)
	return nil
}
