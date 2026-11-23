package api

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Backup destinations (feature 052). Everything here goes through the service. A destination's secret
// key is write-only: no answer, error or log line holds it.

// backupDestinationJSON is a destination without its secret key (schema BackupDestination).
type backupDestinationJSON struct {
	Name        string    `json:"name"`
	Default     bool      `json:"default"`
	Endpoint    string    `json:"endpoint"`
	Region      string    `json:"region"`
	Bucket      string    `json:"bucket"`
	Prefix      string    `json:"prefix"`
	AccessKeyID string    `json:"accessKeyId"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// LastTest is the outcome of the last test, absent until one ran. UsedBy is what uses the
	// destination, and is only in the list.
	LastTest *destinationTestJSON `json:"lastTest,omitempty"`
	UsedBy   []destinationUseJSON `json:"usedBy,omitempty"`
}

// destinationTestJSON is the outcome of a test of a destination (schema BackupDestinationTest).
type destinationTestJSON struct {
	OK       bool      `json:"ok"`
	Message  string    `json:"message"`
	TestedAt time.Time `json:"testedAt"`
}

// destinationUseJSON is one user of a destination (schema BackupDestinationUse): a service's policy
// (app, service and role "destination" or "copy") or a project whose default it is.
type destinationUseJSON struct {
	App     string `json:"app,omitempty"`
	Service string `json:"service,omitempty"`
	Role    string `json:"role,omitempty"`
	Project string `json:"project,omitempty"`
}

func toDestinationTestJSON(t store.DestinationTest) destinationTestJSON {
	return destinationTestJSON{OK: t.OK, Message: t.Message, TestedAt: t.TestedAt}
}

func toDestinationJSON(d store.BackupDestination) backupDestinationJSON {
	out := backupDestinationJSON{Name: d.Name, Default: d.IsDefault, Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket,
		Prefix: d.Prefix, AccessKeyID: d.AccessKeyID, UpdatedAt: d.UpdatedAt}
	if d.Test != nil {
		t := toDestinationTestJSON(*d.Test)
		out.LastTest = &t
	}
	return out
}

// destinationInput is the body of putBackupDestination and setBackupDestination (schema
// BackupDestinationRequest and BackupDestinationPut). A body without secretAccessKey keeps the
// secret set before.
type destinationInput struct {
	Endpoint        string  `json:"endpoint"`
	Region          string  `json:"region"`
	Bucket          string  `json:"bucket"`
	Prefix          string  `json:"prefix"`
	AccessKeyID     string  `json:"accessKeyId"`
	SecretAccessKey *string `json:"secretAccessKey"`
	Default         *bool   `json:"default"`
}

func (in destinationInput) toService() service.DestinationInput {
	return service.DestinationInput{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix,
		AccessKeyID: in.AccessKeyID, SecretAccessKey: in.SecretAccessKey, Default: in.Default}
}

// getBackupDestination answers GET /v1/backups/destination: the default destination.
func (s *server) getBackupDestination(w http.ResponseWriter, r *http.Request) error {
	d, err := s.svc.GetBackupDestination(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDestinationJSON(d))
	return nil
}

// setBackupDestination answers PUT /v1/backups/destination: it writes the default destination, or
// makes "main" when there is none. It takes no `default`.
func (s *server) setBackupDestination(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Endpoint        string  `json:"endpoint"`
		Region          string  `json:"region"`
		Bucket          string  `json:"bucket"`
		Prefix          string  `json:"prefix"`
		AccessKeyID     string  `json:"accessKeyId"`
		SecretAccessKey *string `json:"secretAccessKey"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	d, err := s.svc.SetBackupDestination(r.Context(), service.DestinationInput{Endpoint: in.Endpoint, Region: in.Region,
		Bucket: in.Bucket, Prefix: in.Prefix, AccessKeyID: in.AccessKeyID, SecretAccessKey: in.SecretAccessKey})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDestinationJSON(d))
	return nil
}

// listBackupDestinations answers GET /v1/backups/destinations, ordered by name.
func (s *server) listBackupDestinations(w http.ResponseWriter, r *http.Request) error {
	list, err := s.svc.ListBackupDestinations(r.Context())
	if err != nil {
		return err
	}
	out := make([]backupDestinationJSON, 0, len(list))
	for _, v := range list {
		d := toDestinationJSON(v.BackupDestination)
		for _, u := range v.UsedBy {
			d.UsedBy = append(d.UsedBy, destinationUseJSON{App: u.App, Service: u.Service, Role: u.Role, Project: u.Project})
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// putBackupDestination answers PUT /v1/backups/destinations/{name}: 201 with Location when it made
// the destination, 200 when it changed one.
func (s *server) putBackupDestination(w http.ResponseWriter, r *http.Request) error {
	var in destinationInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	name := r.PathValue("name")
	_, lookup := s.svc.GetBackupDestinationNamed(r.Context(), name)
	created := errors.Is(lookup, service.ErrDestinationNotFound)
	d, err := s.svc.SaveBackupDestination(r.Context(), name, in.toService())
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", "/v1/backups/destinations/"+url.PathEscape(name))
	}
	writeJSON(w, status, toDestinationJSON(d))
	return nil
}

// deleteBackupDestination answers DELETE /v1/backups/destinations/{name} with 204.
func (s *server) deleteBackupDestination(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteBackupDestination(r.Context(), r.PathValue("name")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// testBackupDestination answers POST /v1/backups/destinations/{name}/test: 200 with the outcome,
// whether the bucket said yes or no.
func (s *server) testBackupDestination(w http.ResponseWriter, r *http.Request) error {
	t, err := s.svc.TestBackupDestination(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDestinationTestJSON(t))
	return nil
}
