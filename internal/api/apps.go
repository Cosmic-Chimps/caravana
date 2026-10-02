package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// appRecordJSON is a stored app (schema AppRecord). The app is wrapped, not
// flattened, so a client can send "app" back unchanged as a deploy body.
type appRecordJSON struct {
	App       app.App   `json:"app"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// getApp answers GET /v1/apps/{id}: the app as it was last sent.
func (s *server) getApp(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.svc.GetApp(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, appRecordJSON{App: rec.App, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt})
	return nil
}
