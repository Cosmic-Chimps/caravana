package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Add-ons (feature 044, D15): something the owner installs and removes. Today the
// image scanner is the only one. No answer here holds anything the add-on's
// software printed: the types below have no property for it.

const (
	codeAddonBusy           = "addon_busy"
	codeAddonNotFound       = "addon_not_found"
	codeScannerNotInstalled = "scanner_not_installed"
	codeImageVulnerable     = "image_vulnerable"
)

// scansError maps the refusals of add-ons, scans and the deploy gate to their
// codes; mapError calls it.
func scansError(err error) *Error {
	var vuln *service.ImageVulnerableError
	switch {
	case errors.As(err, &vuln):
		// The message names the advisory, the package and the versions in
		// Caravana's words; the field says which service's image.
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeImageVulnerable, Field: vuln.Field, Message: err.Error()}
	case errors.Is(err, service.ErrAddonBusy):
		return &Error{Status: http.StatusConflict, Code: codeAddonBusy, Message: err.Error()}
	case errors.Is(err, service.ErrAddonNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeAddonNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrScannerNotInstalled):
		return &Error{Status: http.StatusConflict, Code: codeScannerNotInstalled, Message: err.Error()}
	}
	return nil
}

// addonJSON is an add-on (schema Addon). InstalledAt, Server and DatabaseBuilt
// are null until known.
type addonJSON struct {
	Name          string        `json:"name"`
	Title         string        `json:"title"`
	State         string        `json:"state"`
	Version       string        `json:"version"`
	InstalledAt   *time.Time    `json:"installedAt"`
	Reason        string        `json:"reason"`
	Cost          addonCostJSON `json:"cost"`
	Server        string        `json:"server"`
	DatabaseBuilt *time.Time    `json:"databaseBuilt"`
}

// addonCostJSON is what an add-on costs (schema AddonCost).
type addonCostJSON struct {
	DiskBytes           int64 `json:"diskBytes"`
	DownloadBytesPerDay int64 `json:"downloadBytesPerDay"`
}

func toAddonJSON(a service.AddonView) addonJSON {
	return addonJSON{
		Name: a.Name, Title: a.Title, State: a.State, Version: a.Version, InstalledAt: timePtr(a.InstalledAt),
		Reason: a.Reason, Cost: addonCostJSON{DiskBytes: a.DiskBytes, DownloadBytesPerDay: a.DownloadBytesPerDay},
		Server: a.Server, DatabaseBuilt: timePtr(a.DatabaseBuilt),
	}
}

// listAddons answers GET /v1/addons: every add-on Caravana knows, installed or
// not (state `available` when not).
func (s *server) listAddons(w http.ResponseWriter, r *http.Request) error {
	list, err := s.svc.Addons(r.Context())
	if err != nil {
		return err
	}
	out := make([]addonJSON, 0, len(list))
	for _, a := range list {
		out = append(out, toAddonJSON(a))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// installAddon answers PUT /v1/addons/{name} with 202 and the add-on
// installing: the install goes on in the background and ends in `installed` or
// `failed` (with the reason), which listAddons shows. One already installed is
// answered as it is. 404 addon_not_found for another name; 409 addon_busy while
// it is installing or being removed.
func (s *server) installAddon(w http.ResponseWriter, r *http.Request) error {
	a, err := s.svc.InstallAddon(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toAddonJSON(a))
	return nil
}

// removeAddon answers DELETE /v1/addons/{name} with 202 and the add-on
// removing; the removal goes on in the background. One that isn't installed is
// answered as available. 404 addon_not_found; 409 addon_busy.
func (s *server) removeAddon(w http.ResponseWriter, r *http.Request) error {
	a, err := s.svc.RemoveAddon(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toAddonJSON(a))
	return nil
}
