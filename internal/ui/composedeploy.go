package ui

import (
	"errors"
	"net/http"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// showComposeDeploy answers GET /apps/{id}/compose: the Deploy from Compose
// form, with the app's revision in it and nothing previewed yet.
func (s *server) showComposeDeploy(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	rec, err := s.svc.GetApp(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	v := newComposeView(id)
	v.Draft.Revision = rec.Revision
	s.drawCompose(w, r, http.StatusOK, v)
}

// previewComposeDeploy answers POST /apps/{id}/compose/preview.
func (s *server) previewComposeDeploy(w http.ResponseWriter, r *http.Request, session string) {
	s.composeStep(w, r, r.PathValue("id"), false)
}

// deployFromCompose answers POST /apps/{id}/compose.
func (s *server) deployFromCompose(w http.ResponseWriter, r *http.Request, session string) {
	s.composeStep(w, r, r.PathValue("id"), true)
}
