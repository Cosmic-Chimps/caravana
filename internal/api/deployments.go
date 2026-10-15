package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// deployWriteDeadline replaces the server's 30 s write timeout for deploy
// requests: they may wait behind another deploy of the same app before their
// own 25 s of work (research R11).
const deployWriteDeadline = 60 * time.Second

// List limits for GET /v1/apps/{id}/deployments.
const (
	defaultListLimit = 20
	maxListLimit     = 100
)

// deploymentJSON is a deployment as the API shows it (schema Deployment).
type deploymentJSON struct {
	ID         string            `json:"id"`
	AppID      string            `json:"appId"`
	Trigger    app.Trigger       `json:"trigger"`
	Status     store.Status      `json:"status"`
	JobVersion *uint64           `json:"jobVersion,omitempty"`
	Digests    map[string]string `json:"digests"`
	Warnings   []app.Warning     `json:"warnings"`
	Error      string            `json:"error,omitempty"`
	// Source is the deployment this one redeployed.
	Source string `json:"source,omitempty"`
	// DefinitionRecorded says whether it can be redeployed (when healthy) and
	// exported: deployments from before feature 011 can't.
	DefinitionRecorded bool `json:"definitionRecorded"`
	// ComposeKept says getDeploymentCompose has a file to answer: the
	// deployment was made from a Compose file.
	ComposeKept bool      `json:"composeKept"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Progress is only set while the deployment runs, and only by the reads
	// that look it up (getDeployment, and the newest running one in listDeployments).
	Progress *progressJSON `json:"progress,omitempty"`
}

// progressJSON is a rollout's progress as the API shows it (schema Progress).
type progressJSON struct {
	Wanted         int    `json:"wanted"`
	Placed         int    `json:"placed"`
	Healthy        int    `json:"healthy"`
	Unhealthy      int    `json:"unhealthy"`
	CanariesWanted int    `json:"canariesWanted"`
	Promoted       bool   `json:"promoted"`
	Description    string `json:"description,omitempty"`
}

func toProgressJSON(p service.Progress) *progressJSON {
	return &progressJSON{Wanted: p.Wanted, Placed: p.Placed, Healthy: p.Healthy, Unhealthy: p.Unhealthy,
		CanariesWanted: p.CanariesWanted, Promoted: p.Promoted, Description: p.Description}
}

// withProgress adds the rollout's progress to a running deployment. Progress
// is a bonus: when Nomad can't be read the deployment is still answered, so
// the failure is logged and the field left out.
func (s *server) withProgress(r *http.Request, rec store.DeploymentRecord, d *deploymentJSON) {
	if rec.Status != store.StatusRunning {
		return
	}
	p, err := s.svc.Progress(r.Context(), rec)
	if err != nil {
		s.log.Warn("can't read rollout progress; answering without it", "deployment", rec.Deployment.ID, "error", err)
		return
	}
	if p != nil {
		d.Progress = toProgressJSON(*p)
	}
}

func toDeploymentJSON(rec store.DeploymentRecord) deploymentJSON {
	d := deploymentJSON{
		ID:         rec.Deployment.ID,
		AppID:      rec.AppID,
		Trigger:    rec.Deployment.Trigger,
		Status:     rec.Status,
		JobVersion: rec.JobVersion,
		Digests:    rec.Deployment.Digests,
		Warnings:   rec.Warnings,
		Error:      rec.Error,
		Source:     rec.Source,
		CreatedAt:  rec.CreatedAt,
		UpdatedAt:  rec.UpdatedAt,
	}
	d.DefinitionRecorded = rec.Definition != nil
	d.ComposeKept = rec.Compose != nil
	// Never null in the JSON: clients can always iterate them.
	if d.Digests == nil {
		d.Digests = map[string]string{}
	}
	if d.Warnings == nil {
		d.Warnings = []app.Warning{}
	}
	return d
}

// deploymentPath is a deployment's URL, sent as Location.
func deploymentPath(appID, deploymentID string) string {
	return "/v1/apps/" + appID + "/deployments/" + deploymentID
}

// ifMatch reads the optional If-Match header of a deploy: the revision the
// client read (from getApp's ETag), as the service's condition. The value is
// one quoted revision; a weak tag (W/"x") counts as the same revision. An
// absent header is no condition. Anything else (a list, an unquoted value,
// `*`) is 400 naming the header, so a client never believes it is protected
// when it isn't.
func ifMatch(r *http.Request) (service.Condition, error) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		return service.Condition{}, nil
	}
	bad := func(msg string) error {
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "If-Match", Message: msg}
	}
	if len(values) > 1 {
		return service.Condition{}, bad("send one revision, as getApp's ETag gives it")
	}
	tag := strings.TrimPrefix(strings.TrimSpace(values[0]), "W/")
	quoted := len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"'
	rev := strings.Trim(tag, `"`)
	if !quoted || rev == "" || strings.ContainsAny(rev, `",`) {
		return service.Condition{}, bad(`must be one quoted revision, like "0123456789abcdef"`)
	}
	return service.Condition{IfRevision: rev}, nil
}

// ifNoneMatch reads the optional If-None-Match header of a create: only `*`
// (create only if the ID is free) is allowed; any other value is 400 naming
// the header.
func ifNoneMatch(r *http.Request) (bool, error) {
	values := r.Header.Values("If-None-Match")
	if len(values) == 0 {
		return false, nil
	}
	if len(values) > 1 || strings.TrimSpace(values[0]) != "*" {
		return false, &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "If-None-Match",
			Message: "only * is supported (create only if the app doesn't exist)"}
	}
	return true, nil
}

// deploy answers POST /v1/apps/{id}/deployments. With a body it saves and
// deploys the app; with an empty body it redeploys the stored one. It answers
// 202 once Nomad has accepted the job; the client follows the Location.
//
// With a body, an If-Match header makes the save conditional on the stored
// app's revision (412 app_changed when it moved). Without a body the header
// is ignored: a redeploy changes no definition, so there is nothing to protect.
func (s *server) deploy(w http.ResponseWriter, r *http.Request) error {
	// Best effort: a writer that can't change deadlines keeps the server's.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deployWriteDeadline))

	id := r.PathValue("id")
	var a app.App
	present, err := decodeOptionalJSON(r, &a)
	if err != nil {
		return err
	}

	var rec store.DeploymentRecord
	if present {
		var cond service.Condition
		if cond, err = ifMatch(r); err != nil {
			return err
		}
		switch a.ID {
		case "":
			a.ID = id
		case id:
		default:
			return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "id",
				Message: fmt.Sprintf("%q doesn't match the app ID in the path (%q)", a.ID, id)}
		}
		rec, err = s.svc.DeployIf(r.Context(), a, cond, app.TriggerManual)
	} else {
		rec, err = s.svc.Redeploy(r.Context(), id, app.TriggerManual)
	}
	if err != nil {
		return err
	}

	w.Header().Set("Location", deploymentPath(id, rec.Deployment.ID))
	writeJSON(w, http.StatusAccepted, toDeploymentJSON(rec))
	return nil
}

// listLimit reads the optional ?limit= of a list route: 1 to maxListLimit,
// defaultListLimit when absent.
func listLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return defaultListLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxListLimit {
		return 0, &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "limit",
			Message: fmt.Sprintf("must be a whole number from 1 to %d, got %q", maxListLimit, v)}
	}
	return n, nil
}

// deploymentList is the DeploymentList schema.
func deploymentList(recs []store.DeploymentRecord) []deploymentJSON {
	list := make([]deploymentJSON, 0, len(recs))
	for _, rec := range recs {
		list = append(list, toDeploymentJSON(rec))
	}
	return list
}

// writeDeploymentList answers with the DeploymentList schema.
func writeDeploymentList(w http.ResponseWriter, list []deploymentJSON) {
	writeJSON(w, http.StatusOK, struct {
		Deployments []deploymentJSON `json:"deployments"`
	}{Deployments: list})
}

// listDeployments answers GET /v1/apps/{id}/deployments?limit=.
func (s *server) listDeployments(w http.ResponseWriter, r *http.Request) error {
	limit, err := listLimit(r)
	if err != nil {
		return err
	}
	recs, err := s.svc.ListDeployments(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		return err
	}
	list := deploymentList(recs)
	// Only the newest running deployment: older ones that still say running
	// are about to be superseded, and each lookup is a Nomad request.
	for i, rec := range recs {
		if rec.Status == store.StatusRunning {
			s.withProgress(r, rec, &list[i])
			break
		}
	}
	writeDeploymentList(w, list)
	return nil
}

// listRecentDeployments answers GET /v1/deployments?limit=: the newest
// deployments across all apps.
func (s *server) listRecentDeployments(w http.ResponseWriter, r *http.Request) error {
	limit, err := listLimit(r)
	if err != nil {
		return err
	}
	recs, err := s.svc.RecentDeployments(r.Context(), limit)
	if err != nil {
		return err
	}
	writeDeploymentList(w, deploymentList(recs))
	return nil
}

// getDeployment answers GET /v1/apps/{id}/deployments/{deploymentId}.
func (s *server) getDeployment(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.svc.GetDeployment(r.Context(), r.PathValue("id"), r.PathValue("deploymentId"))
	if err != nil {
		return err
	}
	d := toDeploymentJSON(rec)
	s.withProgress(r, rec, &d)
	writeJSON(w, http.StatusOK, d)
	return nil
}
