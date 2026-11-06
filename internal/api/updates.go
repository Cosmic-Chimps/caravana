package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The updates codes (feature 027 page 3).
const (
	codeCheckTooSoon     = "check_too_soon"
	codeCheckDisabled    = "check_disabled"
	codeCheckUnavailable = "check_unavailable"

	// The update codes (feature 030).
	codeUpdateUnavailable = "update_unavailable"
	codeUpdateRunning     = "update_running"
	codeUpdateNotOffered  = "update_not_offered"
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
	case errors.Is(err, service.ErrUpdateUnavailable):
		return &Error{Status: http.StatusConflict, Code: codeUpdateUnavailable, Field: "version", Message: err.Error()}
	case errors.Is(err, service.ErrUpdateRunning):
		return &Error{Status: http.StatusConflict, Code: codeUpdateRunning, Message: err.Error()}
	case errors.Is(err, service.ErrUpdateNotOffered):
		return &Error{Status: http.StatusConflict, Code: codeUpdateNotOffered, Field: "version", Message: err.Error()}
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
	// Updating from the console (feature 030).
	Updater    updaterJSON    `json:"updater"`
	Run        *updateRunJSON `json:"run"`
	AutoUpdate autoUpdateJSON `json:"autoUpdate"`
}

// updaterJSON is schema UpdaterState.
type updaterJSON struct {
	Installed   bool   `json:"installed"`
	Unavailable string `json:"unavailable"`
}

// autoUpdateJSON is schema AutoUpdate. Next is null unless an automatic request is due.
type autoUpdateJSON struct {
	Enabled bool       `json:"enabled"`
	Hour    int        `json:"hour"`
	Next    *time.Time `json:"next"`
}

// updateRunJSON is schema UpdateRun. StartedAt and FinishedAt are null until known; Log is where
// the updater keeps the installer's output on the server (root only), empty until it started the run.
type updateRunJSON struct {
	ID          string     `json:"id"`
	Version     string     `json:"version"`
	FromVersion string     `json:"fromVersion"`
	Trigger     string     `json:"trigger"`
	TokenName   string     `json:"tokenName"`
	State       string     `json:"state"`
	Reason      string     `json:"reason"`
	RequestedAt time.Time  `json:"requestedAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
	Log         string     `json:"log"`
}

func toUpdateRunJSON(r store.UpdateRun) updateRunJSON {
	return updateRunJSON{
		ID: r.ID, Version: r.Version, FromVersion: r.FromVersion, Trigger: r.Trigger, TokenName: r.TokenName,
		State: r.State, Reason: r.Reason, RequestedAt: r.RequestedAt.UTC(),
		StartedAt: timePtr(r.StartedAt), FinishedAt: timePtr(r.FinishedAt), Log: r.Log,
	}
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
	SecuritySince     *time.Time `json:"securitySince"`
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
	// State is an update run's final state, for the kind "update"; left out for the others.
	State string `json:"state,omitempty"`
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
		Updater:    updaterJSON{Installed: c.Updater.Installed, Unavailable: c.Updater.Unavailable},
		AutoUpdate: autoUpdateJSON{Enabled: c.Auto.Enabled, Hour: c.Auto.Hour, Next: timePtr(c.Auto.Next)},
	}
	if c.Run != nil {
		run := toUpdateRunJSON(*c.Run)
		out.Run = &run
	}
	for _, n := range c.Notes {
		out.Notes = append(out.Notes, n.String())
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
				Stale: o.Updates.Stale, SecuritySince: timePtr(o.Updates.SecuritySince), PackagesAvailable: o.Updates.PackagesAvailable, Packages: o.Updates.Packages,
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
		out.History = append(out.History, updateEventJSON{At: h.At.UTC(), Kind: h.Kind, Text: h.Text, Detail: h.Detail, State: h.State})
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

// updateSettingsRequest is schema UpdateSettingsRequest. Every field is a pointer so that leaving
// one out (keep it) is told from false and from 0.
type updateSettingsRequest struct {
	CheckEnabled   *bool `json:"checkEnabled"`
	AutoUpdate     *bool `json:"autoUpdate"`
	AutoUpdateHour *int  `json:"autoUpdateHour"`
}

// putUpdateSettings answers PUT /v1/updates/settings: whether Caravana asks GitHub for new releases
// and whether it installs patch releases by itself, and at which UTC hour. Every field is optional,
// and a missing one keeps its value. The answer is the Caravana block as it is now. A field of the
// wrong type, or an hour outside 0-23, is 422 invalid_field, naming it.
func (s *server) putUpdateSettings(w http.ResponseWriter, r *http.Request) error {
	var in updateSettingsRequest
	if err := decodeJSON(r, &in); err != nil {
		var e *Error
		if errors.As(err, &e) && e.Code == codeInvalidField {
			switch e.Field {
			case "checkEnabled", "autoUpdate":
				return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: e.Field, Message: "must be true or false"}
			case "autoUpdateHour":
				return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: e.Field, Message: "must be an hour from 0 to 23"}
			}
		}
		return err
	}
	err := s.svc.PutUpdateSettings(r.Context(), service.UpdateSettings{
		CheckEnabled: in.CheckEnabled, AutoUpdate: in.AutoUpdate, AutoUpdateHour: in.AutoUpdateHour,
	})
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	}
	if err != nil {
		return err
	}
	v, err := s.svc.Updates(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toUpdatesJSON(v).Caravana)
	return nil
}

// requestUpdateRequest is schema RequestUpdateRequest.
type requestUpdateRequest struct {
	Version string `json:"version"`
}

// requestUpdate answers POST /v1/updates/runs: asks the server's updater to install a newer Caravana
// release. It records the request and answers 202 with the run in the state requested; the
// updater picks the request up within a minute, and the run is followed in getUpdates
// (caravana.run) and listUpdateRuns. The trigger is "console" for a signed-in session and "api" for
// an API token, whose name is recorded.
//
// 422 invalid_field (version) when it isn't X.Y.Z[-suffix]; 409 update_unavailable on a
// development build or a host without the updater, update_running while a run is open,
// update_not_offered when the version isn't the newest release seen or isn't newer than the
// running one. Deploy tokens are refused.
func (s *server) requestUpdate(w http.ResponseWriter, r *http.Request) error {
	var in requestUpdateRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	in.Version = strings.TrimSpace(in.Version)
	if in.Version == "" {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: "version", Message: "is required, such as 0.17.0"}
	}
	trigger, name := store.UpdateByConsole, ""
	if tn := service.TokenName(r.Context()); tn != "" {
		trigger, name = store.UpdateByAPI, tn
	}
	run, err := s.svc.RequestUpdate(r.Context(), in.Version, trigger, name)
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toUpdateRunJSON(run))
	return nil
}

// listUpdateRuns answers GET /v1/updates/runs: the update runs, newest first, at most 20.
func (s *server) listUpdateRuns(w http.ResponseWriter, r *http.Request) error {
	runs, err := s.svc.ListUpdateRuns(r.Context(), 20)
	if err != nil {
		return err
	}
	out := struct {
		Runs []updateRunJSON `json:"runs"`
	}{Runs: make([]updateRunJSON, 0, len(runs))}
	for _, run := range runs {
		out.Runs = append(out.Runs, toUpdateRunJSON(run))
	}
	writeJSON(w, http.StatusOK, out)
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
