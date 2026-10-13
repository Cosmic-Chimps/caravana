package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// appRecordJSON is a stored app (schema AppRecord). The app is wrapped, not
// flattened, so a client can send "app" back unchanged as a deploy body.
type appRecordJSON struct {
	App       app.App   `json:"app"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Revision names this exact stored app; a client sends it back as
	// If-Match to deploy a change only if nobody changed the app meanwhile.
	Revision string `json:"revision"`
}

// getApp answers GET /v1/apps/{id}: the app as it was last sent, with its
// revision as the ETag header and as a field.
func (s *server) getApp(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.svc.GetApp(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+rec.Revision+`"`)
	writeJSON(w, http.StatusOK, appRecordJSON{App: rec.App, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, Revision: rec.Revision})
	return nil
}

// appStatusJSON is an app's status (schema AppStatus).
type appStatusJSON struct {
	State         service.AppState `json:"state"`
	WantedCopies  int              `json:"wantedCopies"`
	HealthyCopies int              `json:"healthyCopies"`
	LiveVersion   *uint64          `json:"liveVersion,omitempty"`
}

// appSummaryJSON is an app with its status (schema AppSummary).
type appSummaryJSON struct {
	App            app.App         `json:"app"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	Status         appStatusJSON   `json:"status"`
	LastDeployment *deploymentJSON `json:"lastDeployment,omitempty"`
}

// listApps answers GET /v1/apps: every app by ID, with its status. When
// Nomad can't be read the statuses are unknown and the answer is still 200.
func (s *server) listApps(w http.ResponseWriter, r *http.Request) error {
	sums, err := s.svc.ListApps(r.Context())
	if err != nil {
		return err
	}
	out := struct {
		Apps []appSummaryJSON `json:"apps"`
	}{Apps: make([]appSummaryJSON, 0, len(sums))}
	for _, sum := range sums {
		j := appSummaryJSON{
			App:       sum.App,
			CreatedAt: sum.CreatedAt,
			UpdatedAt: sum.UpdatedAt,
			Status: appStatusJSON{
				State:         sum.Status.State,
				WantedCopies:  sum.Status.WantedCopies,
				HealthyCopies: sum.Status.HealthyCopies,
				LiveVersion:   sum.Status.LiveVersion,
			},
		}
		if sum.LastDeployment != nil {
			d := toDeploymentJSON(*sum.LastDeployment)
			j.LastDeployment = &d
		}
		out.Apps = append(out.Apps, j)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createApp answers POST /v1/apps: it saves a new app and deploys it, like
// deployApp with a body, but an ID that is already stored is 409 app_exists
// and nothing changes. The app's ID comes from the body, since there is none
// in the path.
func (s *server) createApp(w http.ResponseWriter, r *http.Request) error {
	// Best effort, as for deployApp: a writer that can't change deadlines keeps the server's.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	var a app.App
	if err := decodeJSON(r, &a); err != nil {
		return err
	}
	rec, err := s.svc.CreateApp(r.Context(), a, app.TriggerManual)
	if err != nil {
		return err
	}
	w.Header().Set("Location", deploymentPath(rec.AppID, rec.Deployment.ID))
	writeJSON(w, http.StatusAccepted, toDeploymentJSON(rec))
	return nil
}

// deleteApp answers DELETE /v1/apps/{id}: it stops and purges the app's Nomad
// job, then erases the app. If Nomad can't do its part, nothing is erased and
// the answer is 502, so the call can be repeated.
func (s *server) deleteApp(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteApp(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
