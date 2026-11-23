package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Building from a Git repository (feature 050, specs/050-build-from-git/contracts/api.md): the build
// of a deployment, the Git access of a built service and the installation's build settings. Nothing
// here ever holds the private half of a deploy key, a token or a line of a build's output outside
// getDeploymentBuild, which is sent with Cache-Control: no-store.

// The codes only these operations give. invalid_field, not_found and secret_store_unavailable are the
// API's own.
const (
	codeGitAccessMissing     = "git_access_missing"
	codeGitHostKeyUnknown    = "git_host_key_unknown"
	codeBuildRegistryMissing = "build_registry_missing"
	codeBuildNotFound        = "build_not_found"
	codeGitAccessNotFound    = "git_access_not_found"
)

// buildError maps the refusals of a built service and of the build reads; mapError calls it. The
// field is the one the service names (services[i].build.access, .hostKey, services[i].image).
func buildError(err error) *Error {
	field := ""
	var re *service.RefusalError
	if errors.As(err, &re) {
		field = re.Field
	}
	switch {
	case errors.Is(err, service.ErrGitAccessMissing):
		return &Error{Status: http.StatusConflict, Code: codeGitAccessMissing, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrGitHostKeyUnknown):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeGitHostKeyUnknown, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrBuildRegistryMissing):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeBuildRegistryMissing, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrBuildNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeBuildNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrGitAccessNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeGitAccessNotFound, Message: err.Error()}
	}
	return nil
}

// buildSummaryJSON is schema BuildSummary: a build without its output.
type buildSummaryJSON struct {
	ID         string    `json:"id"`
	Service    string    `json:"service"`
	Status     string    `json:"status"`
	Repository string    `json:"repository"`
	Branch     string    `json:"branch"`
	Commit     string    `json:"commit,omitempty"`
	Author     string    `json:"author,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	Image      string    `json:"image,omitempty"`
	Digest     string    `json:"digest,omitempty"`
	Error      string    `json:"error,omitempty"`
	QueuedAt   time.Time `json:"queuedAt"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	EndedAt    time.Time `json:"endedAt,omitzero"`
}

// toBuildSummaryJSON is nil for a deployment that didn't build.
func toBuildSummaryJSON(b *store.BuildRecord) *buildSummaryJSON {
	if b == nil {
		return nil
	}
	return &buildSummaryJSON{ID: b.ID, Service: b.Service, Status: b.Status, Repository: b.Repository,
		Branch: b.Branch, Commit: b.Commit, Author: b.Author, Subject: b.Subject, Image: b.Image,
		Digest: b.Digest, Error: b.Error, QueuedAt: b.QueuedAt, StartedAt: b.StartedAt, EndedAt: b.EndedAt}
}

// buildJSON is schema Build: the summary with the build's output (masked, its last 64 KiB).
type buildJSON struct {
	buildSummaryJSON
	Log    string `json:"log"`
	LogCut bool   `json:"logCut"`
}

// getDeploymentBuild answers GET /v1/apps/{id}/deployments/{deploymentId}/build. The output of a
// build can hold anything its Dockerfile prints, so the answer is never cached.
func (s *server) getDeploymentBuild(w http.ResponseWriter, r *http.Request) error {
	b, err := s.svc.GetBuild(r.Context(), r.PathValue("id"), r.PathValue("deploymentId"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, buildJSON{buildSummaryJSON: *toBuildSummaryJSON(&b), Log: b.Log, LogCut: b.LogCut})
	return nil
}

// gitAccessJSON is schema GitAccess: what is safe to show of a service's Git access. The private
// key and the token are never in it.
type gitAccessJSON struct {
	Kind        string    `json:"kind"`
	PublicKey   string    `json:"publicKey,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Username    string    `json:"username,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitzero"`
}

func toGitAccessJSON(v service.GitAccessView) gitAccessJSON {
	return gitAccessJSON{Kind: v.Kind, PublicKey: v.PublicKey, Fingerprint: v.Fingerprint,
		Username: v.Username, CreatedAt: v.CreatedAt}
}

// getGitAccess answers GET /v1/apps/{id}/services/{service}/git-access.
func (s *server) getGitAccess(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.GitAccess(r.Context(), r.PathValue("id"), r.PathValue("service"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toGitAccessJSON(v))
	return nil
}

// createDeployKey answers POST /v1/apps/{id}/services/{service}/git-access/deploy-key with 201: a
// new key pair, whose public half goes to the repository's deploy keys.
func (s *server) createDeployKey(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.NewDeployKey(r.Context(), r.PathValue("id"), r.PathValue("service"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, toGitAccessJSON(v))
	return nil
}

// gitTokenRequest is schema GitTokenRequest.
type gitTokenRequest struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// putGitToken answers PUT /v1/apps/{id}/services/{service}/git-access/token. The token is kept in
// Nomad's variables and never answered.
func (s *server) putGitToken(w http.ResponseWriter, r *http.Request) error {
	var in gitTokenRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	v, err := s.svc.SetGitToken(r.Context(), r.PathValue("id"), r.PathValue("service"), in.Username, in.Token)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toGitAccessJSON(v))
	return nil
}

// deleteGitAccess answers DELETE /v1/apps/{id}/services/{service}/git-access.
func (s *server) deleteGitAccess(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.RemoveGitAccess(r.Context(), r.PathValue("id"), r.PathValue("service")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// buildSettingsJSON is schema BuildSettings, in the request and the answer. The request needs every
// field, so the pointers tell a missing one from a zero (cacheMB 0 is "no cache").
type buildSettingsJSON struct {
	MemoryMB       int `json:"memoryMB"`
	CPUMHz         int `json:"cpuMHz"`
	TimeoutMinutes int `json:"timeoutMinutes"`
	Concurrency    int `json:"concurrency"`
	CacheMB        int `json:"cacheMB"`
}

type buildSettingsRequest struct {
	MemoryMB       *int `json:"memoryMB"`
	CPUMHz         *int `json:"cpuMHz"`
	TimeoutMinutes *int `json:"timeoutMinutes"`
	Concurrency    *int `json:"concurrency"`
	CacheMB        *int `json:"cacheMB"`
}

func toBuildSettingsJSON(s service.BuildSettings) buildSettingsJSON {
	return buildSettingsJSON{MemoryMB: s.MemoryMB, CPUMHz: s.CPUMHz, TimeoutMinutes: s.TimeoutMinutes,
		Concurrency: s.Concurrency, CacheMB: s.CacheMB}
}

// getBuildSettings answers GET /v1/builds/settings.
func (s *server) getBuildSettings(w http.ResponseWriter, r *http.Request) error {
	set, err := s.svc.BuildSettings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toBuildSettingsJSON(set))
	return nil
}

// putBuildSettings answers PUT /v1/builds/settings: every field is required, and a value out of its
// range is 400 invalid_field naming it.
func (s *server) putBuildSettings(w http.ResponseWriter, r *http.Request) error {
	var in buildSettingsRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		v    *int
	}{{"memoryMB", in.MemoryMB}, {"cpuMHz", in.CPUMHz}, {"timeoutMinutes", in.TimeoutMinutes},
		{"concurrency", in.Concurrency}, {"cacheMB", in.CacheMB}} {
		if f.v == nil {
			return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: f.name, Message: "is required"}
		}
	}
	saved, err := s.svc.SetBuildSettings(r.Context(), service.BuildSettings{MemoryMB: *in.MemoryMB, CPUMHz: *in.CPUMHz,
		TimeoutMinutes: *in.TimeoutMinutes, Concurrency: *in.Concurrency, CacheMB: *in.CacheMB})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toBuildSettingsJSON(saved))
	return nil
}
