package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Secret providers and Refresh secrets (feature 037). No answer here carries a
// credential or a resolved value: the types below have no property for one.

const (
	codeSecretNotFound         = "secret_not_found"
	codeSecretProviderRefused  = "secret_provider_refused"
	codeSecretProviderUnavail  = "secret_provider_unavailable"
	codeSecretProviderInUse    = "secret_provider_in_use"
	codeSecretProviderNotFound = "secret_provider_not_found"
	codeNoSecretReferences     = "no_secret_references"
)

// secretProvidersError maps the refusals of references and providers to their
// codes; mapError calls it.
func secretProvidersError(err error) *Error {
	var ref *service.ReferenceError
	switch {
	case errors.As(err, &ref):
		// The message names the provider and the path in Caravana's words;
		// the field says which reference.
		e := &Error{Field: ref.Field, Message: err.Error()}
		switch {
		case errors.Is(err, service.ErrSecretNotFound):
			e.Status, e.Code = http.StatusUnprocessableEntity, codeSecretNotFound
		case errors.Is(err, secrets.ErrSourceRefused):
			e.Status, e.Code = http.StatusUnprocessableEntity, codeSecretProviderRefused
		default: // secrets.ErrSourceUnavailable
			e.Status, e.Code = http.StatusBadGateway, codeSecretProviderUnavail
		}
		return e
	case errors.Is(err, service.ErrSecretProviderInUse):
		return &Error{Status: http.StatusConflict, Code: codeSecretProviderInUse, Message: err.Error()}
	case errors.Is(err, service.ErrNoSecretReferences):
		return &Error{Status: http.StatusConflict, Code: codeNoSecretReferences, Message: err.Error()}
	}
	return nil
}

// providerNotFound is the 404 of the provider routes, for a name that isn't
// one; other errors pass through for mapError.
func providerNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return &Error{Status: http.StatusNotFound, Code: codeSecretProviderNotFound, Message: err.Error()}
	}
	return err
}

// secretProviderJSON is a provider (schema SecretProvider).
type secretProviderJSON struct {
	Name      string                   `json:"name"`
	Kind      store.SecretProviderKind `json:"kind"`
	Bella     bellaSettingsJSON        `json:"bella"`
	Allowed   []string                 `json:"allowed"`
	UsedBy    []string                 `json:"usedBy"`
	LastTest  *secretProviderTestJSON  `json:"lastTest,omitempty"`
	CreatedAt time.Time                `json:"createdAt"`
	UpdatedAt time.Time                `json:"updatedAt"`
}

// bellaSettingsJSON is the part of a provider that is not secret (schema
// BellaSettings).
type bellaSettingsJSON struct {
	URL         string `json:"url"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

// secretProviderTestJSON is the outcome of the last test (schema SecretProviderTest).
type secretProviderTestJSON struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	Count   int       `json:"count"`
}

func toSecretProviderJSON(p service.SecretProviderInfo) secretProviderJSON {
	out := secretProviderJSON{
		Name: p.Name, Kind: p.Kind,
		Bella:   bellaSettingsJSON{URL: p.URL, Project: p.Project, Environment: p.Environment},
		Allowed: p.Allowed, UsedBy: p.UsedBy,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	// Never null in the JSON: clients can always iterate them.
	if out.Allowed == nil {
		out.Allowed = []string{}
	}
	if out.UsedBy == nil {
		out.UsedBy = []string{}
	}
	if p.LastTest != nil {
		out.LastTest = &secretProviderTestJSON{At: p.LastTest.At, OK: p.LastTest.OK, Message: p.LastTest.Message, Count: p.LastTest.Count}
	}
	return out
}

// listSecretProviders answers GET /v1/secret-providers.
func (s *server) listSecretProviders(w http.ResponseWriter, r *http.Request) error {
	list, err := s.svc.SecretProviders(r.Context())
	if err != nil {
		return err
	}
	out := make([]secretProviderJSON, 0, len(list))
	for _, p := range list {
		out = append(out, toSecretProviderJSON(p))
	}
	writeJSON(w, http.StatusOK, struct {
		Providers []secretProviderJSON `json:"providers"`
	}{out})
	return nil
}

// putSecretProvider answers PUT /v1/secret-providers/{name}: 201 for a new
// provider, 200 for one replaced. A body without apiKey or privateKey keeps the
// credentials stored before.
func (s *server) putSecretProvider(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Kind       store.SecretProviderKind `json:"kind"`
		Bella      bellaSettingsJSON        `json:"bella"`
		Allowed    []string                 `json:"allowed"`
		APIKey     *string                  `json:"apiKey"`
		PrivateKey *string                  `json:"privateKey"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	info, created, err := s.svc.SaveSecretProvider(r.Context(), service.SecretProviderInput{
		Name: r.PathValue("name"), Kind: in.Kind,
		URL: in.Bella.URL, Project: in.Bella.Project, Environment: in.Bella.Environment,
		Allowed: in.Allowed, APIKey: in.APIKey, PrivateKey: in.PrivateKey,
	})
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toSecretProviderJSON(info))
	return nil
}

// deleteSecretProvider answers DELETE /v1/secret-providers/{name}.
func (s *server) deleteSecretProvider(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteSecretProvider(r.Context(), r.PathValue("name")); err != nil {
		return providerNotFound(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// testSecretProvider answers POST /v1/secret-providers/{name}/test. A refusal
// is an outcome in lastTest, so the answer is 200 either way.
func (s *server) testSecretProvider(w http.ResponseWriter, r *http.Request) error {
	info, err := s.svc.TestSecretProvider(r.Context(), r.PathValue("name"))
	if err != nil {
		return providerNotFound(err)
	}
	writeJSON(w, http.StatusOK, toSecretProviderJSON(info))
	return nil
}

// refreshSecrets answers POST /v1/apps/{id}/secrets/refresh: 200
// {"changed": false} when every reference still has the value the app has, else
// 202 with the deployment that rolls the new values out.
func (s *server) refreshSecrets(w http.ResponseWriter, r *http.Request) error {
	ctx, err := withOrigin(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	res, err := s.svc.RefreshSecrets(ctx, id, app.TriggerManual)
	if err != nil {
		return err
	}
	if !res.Changed {
		writeJSON(w, http.StatusOK, struct {
			Changed bool `json:"changed"`
		}{false})
		return nil
	}
	w.Header().Set("Location", deploymentPath(id, res.Deployment.Deployment.ID))
	writeJSON(w, http.StatusAccepted, struct {
		Changed    bool           `json:"changed"`
		Deployment deploymentJSON `json:"deployment"`
	}{true, toDeploymentJSON(*res.Deployment)})
	return nil
}
