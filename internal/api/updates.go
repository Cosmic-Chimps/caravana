package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// The updates codes (feature 027 page 3).
const (
	codeCheckTooSoon     = "check_too_soon"
	codeCheckDisabled    = "check_disabled"
	codeCheckUnavailable = "check_unavailable"
)

// updatesError maps the check refusals to their codes; mapError calls it.
func updatesError(err error) *Error {
	switch {
	case errors.Is(err, service.ErrCheckTooSoon):
		return &Error{Status: http.StatusTooManyRequests, Code: codeCheckTooSoon, Field: "what", Message: err.Error()}
	case errors.Is(err, service.ErrCheckDisabled):
		return &Error{Status: http.StatusConflict, Code: codeCheckDisabled, Field: "what", Message: err.Error()}
	case errors.Is(err, service.ErrCheckUnavailable):
		return &Error{Status: http.StatusConflict, Code: codeCheckUnavailable, Field: "what", Message: err.Error()}
	}
	return nil
}

// updatesJSON is what getUpdates answers (schema Updates).
type updatesJSON struct {
	Caravana caravanaUpdateJSON `json:"caravana"`
	OS       osUpdatesJSON      `json:"os"`
	History  []updateEventJSON  `json:"history"`
	Checking checkingJSON       `json:"checking"`
}

// caravanaUpdateJSON is the Caravana block (schema UpdatesCaravana). Latest and
// what goes with it are left out unless a newer release is known.
type caravanaUpdateJSON struct {
	Current      string     `json:"current"`
	Latest       string     `json:"latest,omitempty"`
	ReleasedAt   *time.Time `json:"releasedAt,omitempty"`
	URL          string     `json:"url,omitempty"`
	Notes        []string   `json:"notes"`
	Pins         *pinsJSON  `json:"pins,omitempty"`
	PinsText     string     `json:"pinsText,omitempty"`
	Install      string     `json:"install,omitempty"`
	CheckEnabled bool       `json:"checkEnabled"`
	CheckedAt    *time.Time `json:"checkedAt"`
	CheckedOKAt  *time.Time `json:"checkedOkAt"`
	CheckError   string     `json:"checkError"`
}

type pinsJSON struct {
	Nomad   *pinJSON `json:"nomad,omitempty"`
	Traefik *pinJSON `json:"traefik,omitempty"`
}

type pinJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type osUpdatesJSON struct {
	Readable   bool            `json:"readable"`
	Distro     distroJSON      `json:"distro"`
	Updates    updateCountJSON `json:"updates"`
	Reboot     rebootJSON      `json:"reboot"`
	Unattended unattendedJSON  `json:"unattended"`
}

type distroJSON struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type updateCountJSON struct {
	Known             bool       `json:"known"`
	Total             int        `json:"total"`
	Security          int        `json:"security"`
	CheckedAt         *time.Time `json:"checkedAt"`
	Stale             bool       `json:"stale"`
	PackagesAvailable bool       `json:"packagesAvailable"`
	Packages          []string   `json:"packages"`
	PackagesKernel    bool       `json:"packagesKernel"`
	PackagesCheckedAt *time.Time `json:"packagesCheckedAt"`
	PackagesError     string     `json:"packagesError"`
}

type rebootJSON struct {
	Required        bool       `json:"required"`
	Since           *time.Time `json:"since"`
	Packages        []string   `json:"packages"`
	RunningKernel   string     `json:"runningKernel"`
	InstalledKernel string     `json:"installedKernel"`
	// InstalledAt and InstalledBy say which apt run installed a package the reboot marker names.
	InstalledAt *time.Time `json:"installedAt"`
	InstalledBy string     `json:"installedBy"`
}

// unattendedJSON has null fields for what couldn't be read.
type unattendedJSON struct {
	Enabled        *bool  `json:"enabled"`
	AutoReboot     *bool  `json:"autoReboot"`
	AutoRebootTime string `json:"autoRebootTime"`
}

type updateEventJSON struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Text   string    `json:"text"`
	Detail string    `json:"detail"`
}

type checkingJSON struct {
	Release  bool `json:"release"`
	Packages bool `json:"packages"`
}

// timePtr is a pointer to t for a JSON null in place of the zero time.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func toCaravanaUpdateJSON(c service.CaravanaUpdate) caravanaUpdateJSON {
	out := caravanaUpdateJSON{
		Current: c.Current, Latest: c.Latest, ReleasedAt: timePtr(c.ReleasedAt), URL: c.URL, Notes: []string{},
		PinsText: c.PinsText, Install: c.Install, CheckEnabled: c.CheckEnabled, CheckedAt: timePtr(c.CheckedAt),
		CheckedOKAt: timePtr(c.CheckedOKAt), CheckError: c.CheckError,
	}
	for _, n := range c.Notes {
		out.Notes = append(out.Notes, n.Text)
	}
	for _, p := range c.Pins {
		if out.Pins == nil {
			out.Pins = &pinsJSON{}
		}
		switch p.Name {
		case "Nomad":
			out.Pins.Nomad = &pinJSON{From: p.From, To: p.To}
		case "Traefik":
			out.Pins.Traefik = &pinJSON{From: p.From, To: p.To}
		}
	}
	return out
}

func toUpdatesJSON(v service.UpdatesView) updatesJSON {
	o := v.OS
	out := updatesJSON{
		Caravana: toCaravanaUpdateJSON(v.Caravana),
		OS: osUpdatesJSON{
			Readable: o.Readable, Distro: distroJSON{ID: o.Distro.ID, Version: o.Distro.Version},
			Updates: updateCountJSON{
				Known: o.Updates.Known, Total: o.Updates.Total, Security: o.Updates.Security, CheckedAt: timePtr(o.Updates.CheckedAt),
				Stale: o.Updates.Stale, PackagesAvailable: o.Updates.PackagesAvailable, Packages: o.Updates.Packages,
				PackagesKernel: o.Updates.PackagesKernel, PackagesCheckedAt: timePtr(o.Updates.PackagesCheckedAt),
				PackagesError: o.Updates.PackagesError,
			},
			Reboot: rebootJSON{Required: o.Reboot.Required, Since: timePtr(o.Reboot.Since), Packages: o.Reboot.Packages,
				RunningKernel: o.Reboot.RunningKernel, InstalledKernel: o.Reboot.InstalledKernel,
				InstalledAt: timePtr(o.Reboot.InstalledAt), InstalledBy: o.Reboot.InstalledBy},
			Unattended: unattendedJSON{Enabled: o.Unattended.Enabled, AutoReboot: o.Unattended.AutoReboot,
				AutoRebootTime: o.Unattended.AutoRebootTime},
		},
		History:  make([]updateEventJSON, 0, len(v.History)),
		Checking: checkingJSON{Release: v.Checking.Release, Packages: v.Checking.Packages},
	}
	if out.OS.Updates.Packages == nil {
		out.OS.Updates.Packages = []string{}
	}
	if out.OS.Reboot.Packages == nil {
		out.OS.Reboot.Packages = []string{}
	}
	for _, h := range v.History {
		out.History = append(out.History, updateEventJSON{At: h.At.UTC(), Kind: h.Kind, Text: h.Text, Detail: h.Detail})
	}
	return out
}

// getUpdates answers GET /v1/updates: what is waiting for Caravana and for the
// server's OS, and Past updates. It reads the store and the sampler's last
// reading of the host's files; it never calls GitHub or runs a program.
func (s *server) getUpdates(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.Updates(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toUpdatesJSON(v))
	return nil
}

// updateSettingsRequest is schema UpdateSettingsRequest. CheckEnabled is a
// pointer so that leaving it out is told from false.
type updateSettingsRequest struct {
	CheckEnabled *bool `json:"checkEnabled"`
}

// putUpdateSettings answers PUT /v1/updates/settings: whether Caravana asks
// GitHub for new releases. The answer is the Caravana block as it is now. A
// body without a boolean checkEnabled is 422 invalid_field, naming it.
func (s *server) putUpdateSettings(w http.ResponseWriter, r *http.Request) error {
	var in updateSettingsRequest
	if err := decodeJSON(r, &in); err != nil {
		var e *Error
		if errors.As(err, &e) && e.Field == "checkEnabled" && (e.Code == codeInvalidField) {
			return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: "checkEnabled", Message: "must be true or false"}
		}
		return err
	}
	if in.CheckEnabled == nil {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: "checkEnabled", Message: "is required: true or false"}
	}
	if err := s.svc.SetReleaseCheck(r.Context(), *in.CheckEnabled); err != nil {
		return err
	}
	v, err := s.svc.Updates(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toUpdatesJSON(v).Caravana)
	return nil
}

// checkUpdatesRequest is schema CheckUpdatesRequest.
type checkUpdatesRequest struct {
	What string `json:"what"`
}

// checkUpdates answers POST /v1/updates/check: starts the release check
// (what "release") or the package-name check (what "packages") and answers 202
// at once; the result comes with getUpdates. A check may run once in 10
// minutes (429 check_too_soon), the release check only while the owner has it
// on (409 check_disabled) and on a release build, the package check only on
// Ubuntu with pro (409 check_unavailable).
func (s *server) checkUpdates(w http.ResponseWriter, r *http.Request) error {
	var in checkUpdatesRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	var err error
	switch in.What {
	case "release":
		err = s.svc.CheckRelease(r.Context())
	case "packages":
		err = s.svc.CheckPackages(r.Context())
	default:
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: "what", Message: `must be "release" or "packages"`}
	}
	if err != nil {
		return err
	}
	v, err := s.svc.Updates(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, struct {
		Checking checkingJSON `json:"checking"`
	}{checkingJSON{Release: v.Checking.Release, Packages: v.Checking.Packages}})
	return nil
}
