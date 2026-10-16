package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// registryJSON is a registry Caravana has credentials for (schema Registry).
// The password is not a field here, so no answer can carry it.
type registryJSON struct {
	Host      string            `json:"host"`
	Username  string            `json:"username"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
	LastTest  *registryTestJSON `json:"lastTest,omitempty"`
}

// registryTestJSON is the outcome of the last test of a registry's credentials.
type registryTestJSON struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Message string    `json:"message,omitempty"`
}

func toRegistryJSON(r store.Registry) registryJSON {
	out := registryJSON{Host: r.Host, Username: r.Username, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if r.LastTest != nil {
		out.LastTest = &registryTestJSON{At: r.LastTest.At, OK: r.LastTest.OK, Message: r.LastTest.Message}
	}
	return out
}

// registryInvalid answers a field error of the registry calls as 422, as the
// contract says, where the rest of the API answers 400. Other errors pass
// through for mapError.
func registryInvalid(err error) error {
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	}
	return err
}

// listRegistries answers GET /v1/registries.
func (s *server) listRegistries(w http.ResponseWriter, r *http.Request) error {
	regs, err := s.svc.Registries(r.Context())
	if err != nil {
		return err
	}
	out := make([]registryJSON, 0, len(regs))
	for _, reg := range regs {
		out = append(out, toRegistryJSON(reg))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// putRegistry answers PUT /v1/registries/{host}. A body without password keeps
// the secret stored before. {host} may hold a port ("registry.example.com:5000"):
// the mux's wildcard takes everything up to the next "/".
func (s *server) putRegistry(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username string  `json:"username"`
		Password *string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	reg, err := s.svc.SaveRegistry(r.Context(), r.PathValue("host"), in.Username, in.Password)
	if err != nil {
		return registryInvalid(err)
	}
	writeJSON(w, http.StatusOK, toRegistryJSON(reg))
	return nil
}

// deleteRegistry answers DELETE /v1/registries/{host}.
func (s *server) deleteRegistry(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteRegistry(r.Context(), r.PathValue("host")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// testRegistry answers POST /v1/registries/{host}/test. A refusal is an
// outcome in lastTest, so the answer is 200 either way.
func (s *server) testRegistry(w http.ResponseWriter, r *http.Request) error {
	reg, err := s.svc.TestRegistry(r.Context(), r.PathValue("host"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toRegistryJSON(reg))
	return nil
}
