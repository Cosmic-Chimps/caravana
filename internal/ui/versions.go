package ui

import (
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// ---- Past versions: redeploy and export (feature 011) -------------------------------

// sourceLink points at the deployment a redeploy came from, in the same history.
type sourceLink struct {
	ID    string
	Short string
}

// redeployView is a history entry's "Redeploy this version" confirmation.
// Removed lists the secrets the redeploy would delete; Error is set when a
// post was refused, which opens the confirmation again.
type redeployView struct {
	Action  string
	Removed []string
	Error   sentence
}

// addVersionActions adds what a past deployment offers: where it came from,
// downloading the Compose file it was made from (any that kept one, whether or
// not its definition was recorded), redeploying it (healthy ones with a
// recorded definition) and exporting it (any with a recorded definition).
// current is the stored app.
func addVersionActions(e *historyEntry, rec store.DeploymentRecord, current app.App) {
	if rec.Source != "" {
		e.Source = &sourceLink{ID: rec.Source, Short: shortID(rec.Source)}
	}
	base := "/apps/" + rec.AppID + "/deployments/" + rec.Deployment.ID
	if rec.Compose != nil {
		e.ComposeHref = base + "/compose"
	}
	if rec.Definition == nil {
		return
	}
	e.ExportHref = base + "/jobspec"
	if rec.Status != store.StatusHealthy {
		return
	}
	e.Redeploy = &redeployView{Action: base + "/redeploy"}
	for _, w := range service.RemovedSecrets(current, *rec.Definition) {
		e.Redeploy.Removed = append(e.Redeploy.Removed, w.Message)
	}
}

// redeployVersion answers POST /apps/{id}/deployments/{deploymentId}/redeploy:
// deploy that past version again. Success goes back to the Deployments tab,
// where the new deployment is first. A refusal draws the tab again with the
// reason in that entry's confirmation.
func (s *server) redeployVersion(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id, depID := r.PathValue("id"), r.PathValue("deploymentId")
	_, err := s.svc.RedeployVersion(ctx, id, depID, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments", http.StatusSeeOther)
		return
	}

	var status int
	var why sentence
	if reason, ok := versionReason(err); ok {
		status, why = http.StatusConflict, sentence{plain(reason)}
	} else {
		var ok bool
		if status, why, ok = refuseDeploy(&deployForm{}, err); !ok {
			if errors.Is(err, store.ErrNotFound) {
				s.message(w, http.StatusNotFound, "Not found", "There's no such deployment.",
					"Go back to the Deployments tab; the deployment may belong to another app.")
				return
			}
			s.serverError(w, "redeploy a past version", err)
			return
		}
	}

	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	s.deploymentsPage(w, r, status, sum, deployFormOf(sum.App), &refusedRedeploy{ID: depID, Why: why})
}

// refusedRedeploy is a refused redeploy, shown in its entry.
type refusedRedeploy struct {
	ID  string
	Why sentence
}

// exportLive answers GET /apps/{id}/jobspec; exportVersion answers
// GET /apps/{id}/deployments/{deploymentId}/jobspec. Both are downloads.
func (s *server) exportLive(w http.ResponseWriter, r *http.Request, session string) {
	s.export(w, r, "")
}

func (s *server) exportVersion(w http.ResponseWriter, r *http.Request, session string) {
	s.export(w, r, r.PathValue("deploymentId"))
}

func (s *server) export(w http.ResponseWriter, r *http.Request, deploymentID string) {
	id := r.PathValue("id")
	name, hcl, err := s.svc.Export(r.Context(), id, deploymentID)
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		_, _ = w.Write(hcl)
	case errors.Is(err, service.ErrNoLiveDeployment), errors.Is(err, service.ErrDefinitionNotRecorded):
		reason, _ := versionReason(err)
		s.message(w, http.StatusConflict, "Nothing to export", reason,
			"Deploy the app, and export it once the deployment is healthy.")
	case errors.Is(err, store.ErrNotFound):
		s.message(w, http.StatusNotFound, "Not found", "There's no such app or deployment.", "Go back to the Apps page.")
	default:
		s.serverError(w, "export a jobspec", err)
	}
}

// downloadCompose answers GET /apps/{id}/deployments/{deploymentId}/compose:
// the Compose file the deployment was made from, as stored (feature 015), so
// a history entry offers exactly what its version was deployed from. The
// stored copy has no secret values. A deployment with no file, or one of
// another app, is the not-found page.
func (s *server) downloadCompose(w http.ResponseWriter, r *http.Request, session string) {
	id, depID := r.PathValue("id"), r.PathValue("deploymentId")
	rec, err := s.svc.GetDeployment(r.Context(), id, depID)
	switch {
	case err == nil && rec.Compose != nil:
		// App and deployment IDs are made of letters, digits and dashes, so the
		// name needs no escaping inside its quotes.
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+id+"-"+depID+`.compose.yaml"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(rec.Compose.Compose))
	case err == nil, errors.Is(err, store.ErrNotFound):
		s.message(w, http.StatusNotFound, "Not found", "There's no Compose file for that deployment.",
			"Only deployments made from a Compose file have one. Go back to the Deployments tab.")
	default:
		s.serverError(w, "download a Compose file", err)
	}
}

// versionReason says, as a sentence, why a past deployment can't be
// redeployed or exported, for the reasons the service gives.
func versionReason(err error) (string, bool) {
	for _, reason := range []error{service.ErrDeploymentNotHealthy, service.ErrDefinitionNotRecorded, service.ErrNoLiveDeployment} {
		if errors.Is(err, reason) {
			text := reason.Error()
			return strings.ToUpper(text[:1]) + text[1:] + ".", true
		}
	}
	return "", false
}
