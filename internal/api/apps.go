package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
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
	// ComposeKept says getCompose has a file to answer: the app was last
	// deployed from a Compose file.
	ComposeKept bool `json:"composeKept"`
	// Defined says the app has services to deploy. An app created without
	// deploying it may have none yet (feature 019.5).
	Defined bool `json:"defined"`
	// Project, Environment and AppName say where the app is (feature 019);
	// the app's ID is these three written out.
	Project     string `json:"project"`
	Environment string `json:"environment"`
	AppName     string `json:"appName"`
	// StoppedAt is when the app was stopped (feature 020.5); absent while it
	// isn't. A stopped app refuses every deploy.
	StoppedAt *time.Time `json:"stoppedAt,omitempty"`
}

// getApp answers GET /v1/apps/{id}: the app as it was last sent, with its
// revision as the ETag header and as a field.
func (s *server) getApp(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.svc.GetApp(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeAppRecord(w, rec)
	return nil
}

func toAppRecordJSON(rec store.AppRecord) appRecordJSON {
	return appRecordJSON{App: rec.App, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, Revision: rec.Revision,
		ComposeKept: rec.Compose != nil, Defined: rec.App.Defined(),
		Project: rec.Place.Project, Environment: rec.Place.Environment, AppName: rec.Place.Name,
		StoppedAt: rec.StoppedAt}
}

// writeAppRecord answers 200 with a stored app and its revision as the ETag.
func writeAppRecord(w http.ResponseWriter, rec store.AppRecord) {
	w.Header().Set("ETag", `"`+rec.Revision+`"`)
	writeJSON(w, http.StatusOK, toAppRecordJSON(rec))
}

// writeCreatedApp answers 201 for an app saved without deploying it: the
// record, its revision as the ETag, and the app's own URL as Location.
func writeCreatedApp(w http.ResponseWriter, rec store.AppRecord) {
	w.Header().Set("ETag", `"`+rec.Revision+`"`)
	w.Header().Set("Location", "/v1/apps/"+rec.App.ID)
	writeJSON(w, http.StatusCreated, toAppRecordJSON(rec))
}

// deployFlag reads the optional ?deploy= of the create operations: true (the
// default) saves and deploys, false saves only. Anything else is 400 naming
// the parameter, so a typo never deploys by surprise.
func deployFlag(r *http.Request) (bool, error) {
	switch v := r.URL.Query().Get("deploy"); v {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "deploy",
			Message: fmt.Sprintf("must be true or false, got %q", v)}
	}
}

// appStatusJSON is an app's status (schema AppStatus).
type appStatusJSON struct {
	State         service.AppState `json:"state"`
	WantedCopies  int              `json:"wantedCopies"`
	HealthyCopies int              `json:"healthyCopies"`
	LiveVersion   *uint64          `json:"liveVersion,omitempty"`
	// Services is the same two counts per service, in the app's order.
	Services []serviceStatusJSON `json:"services,omitempty"`
}

// serviceStatusJSON is one service's copies (schema ServiceStatus).
type serviceStatusJSON struct {
	Name          string `json:"name"`
	WantedCopies  int    `json:"wantedCopies"`
	HealthyCopies int    `json:"healthyCopies"`
}

func toAppStatusJSON(st service.AppStatus) appStatusJSON {
	j := appStatusJSON{State: st.State, WantedCopies: st.WantedCopies, HealthyCopies: st.HealthyCopies, LiveVersion: st.LiveVersion}
	for _, s := range st.Services {
		j.Services = append(j.Services, serviceStatusJSON{Name: s.Name, WantedCopies: s.Wanted, HealthyCopies: s.Healthy})
	}
	return j
}

// appSummaryJSON is an app with its status (schema AppSummary).
type appSummaryJSON struct {
	App            app.App         `json:"app"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	ComposeKept    bool            `json:"composeKept"`
	Defined        bool            `json:"defined"`
	Project        string          `json:"project"`
	Environment    string          `json:"environment"`
	AppName        string          `json:"appName"`
	StoppedAt      *time.Time      `json:"stoppedAt,omitempty"`
	Status         appStatusJSON   `json:"status"`
	LastDeployment *deploymentJSON `json:"lastDeployment,omitempty"`
}

func toAppSummaryJSON(sum service.AppSummary) appSummaryJSON {
	j := appSummaryJSON{
		App:         sum.App,
		CreatedAt:   sum.CreatedAt,
		UpdatedAt:   sum.UpdatedAt,
		ComposeKept: sum.Compose != nil,
		Defined:     sum.App.Defined(),
		Project:     sum.Place.Project,
		Environment: sum.Place.Environment,
		AppName:     sum.Place.Name,
		StoppedAt:   sum.StoppedAt,
		Status:      toAppStatusJSON(sum.Status),
	}
	if sum.LastDeployment != nil {
		d := toDeploymentJSON(*sum.LastDeployment)
		j.LastDeployment = &d
	}
	return j
}

// listApps answers GET /v1/apps: every app by ID, with its status, or those of
// one project and/or environment (?project=, ?environment=; a project or
// environment that doesn't exist is 404, not an empty list). When Nomad can't
// be read the statuses are unknown and the answer is still 200.
func (s *server) listApps(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	sums, err := s.svc.ListAppsIn(r.Context(), q.Get("project"), q.Get("environment"))
	if err != nil {
		return err
	}
	out := struct {
		Apps []appSummaryJSON `json:"apps"`
	}{Apps: make([]appSummaryJSON, 0, len(sums))}
	for _, sum := range sums {
		out.Apps = append(out.Apps, toAppSummaryJSON(sum))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createApp answers POST /v1/apps: it saves a new app and deploys it, like
// deployApp with a body, but an ID that is already stored is 409 app_exists
// and nothing changes. The app's ID comes from the body, since there is none
// in the path. With ?deploy=false it only saves (201 and the app, no
// deployment), and the body may leave the services out.
func (s *server) createApp(w http.ResponseWriter, r *http.Request) error {
	// Best effort, as for deployApp: a writer that can't change deadlines keeps the server's.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	deployNow, err := deployFlag(r)
	if err != nil {
		return err
	}
	var a app.App
	if err := decodeJSON(r, &a); err != nil {
		return err
	}
	if !deployNow {
		rec, err := s.svc.CreateAppOnly(r.Context(), a)
		if err != nil {
			return err
		}
		writeCreatedApp(w, rec)
		return nil
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
