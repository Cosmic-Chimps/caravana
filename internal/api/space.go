package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Reclaim space (feature 045, specs/045-reclaim-space/contracts/api.md): what a server's disk holds that
// Caravana can give back (images no copy uses, Docker's leftovers, the build cache, volumes no app declares),
// the clean-up of a server's unused images, the deletion of an orphan volume, and the settings of the daily
// clean-up. A session or a full API token may call them; a deploy token is refused by the scope middleware's
// default deny, since none of these routes is in scopeRules. Every answer, an error too, is `no-store`: what
// a server holds changes by the minute.

// The codes only these operations give. no_backup_destination, server_not_found and invalid_field are
// the API's own.
const (
	codeNoAgent          = "no_agent"
	codeCleanupRunning   = "cleanup_running"
	codeKeepListTooLong  = "keep_list_too_long"
	codeCleanupNotFound  = "cleanup_not_found"
	codeVolumeNotFound   = "volume_not_found"
	codeVolumeNotOrphan  = "volume_not_orphan"
	codeVolumeInUse      = "volume_in_use"
	codeDeletionRunning  = "deletion_running"
	codeDeletionNotFound = "deletion_not_found"
)

// spaceError maps the refusals of the space operations; mapError calls it. The field is the one the
// service names (the server's or the volume's name, or the ID), and "name" when it names none.
func spaceError(err error) *Error {
	field := "name"
	var re *service.RefusalError
	if errors.As(err, &re) {
		field = re.Field
	}
	switch {
	case errors.Is(err, service.ErrNoAgent):
		return &Error{Status: http.StatusConflict, Code: codeNoAgent, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrCleanupRunning):
		return &Error{Status: http.StatusConflict, Code: codeCleanupRunning, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrKeepListTooLong):
		return &Error{Status: http.StatusConflict, Code: codeKeepListTooLong, Message: err.Error()}
	case errors.Is(err, service.ErrCleanupNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeCleanupNotFound, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrVolumeNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeVolumeNotFound, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrVolumeNotOrphan):
		return &Error{Status: http.StatusConflict, Code: codeVolumeNotOrphan, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrVolumeInUse):
		return &Error{Status: http.StatusConflict, Code: codeVolumeInUse, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrDeletionRunning):
		return &Error{Status: http.StatusConflict, Code: codeDeletionRunning, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrDeletionNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeDeletionNotFound, Field: field, Message: err.Error()}
	}
	return nil
}

// serverSpaceJSON is schema ServerSpace. images, dockerVolumes and buildCacheBytes are null when the
// server's images aren't known (imagesUnavailable says why); lastCleanup and openCleanup are null for none.
type serverSpaceJSON struct {
	Server             string              `json:"server"`
	Managed            bool                `json:"managed"`
	ReclaimableBytes   int64               `json:"reclaimableBytes"`
	Images             *spaceImagesJSON    `json:"images"`
	ImagesUnavailable  string              `json:"imagesUnavailable"`
	DockerVolumes      *spaceCountJSON     `json:"dockerVolumes"`
	BuildCacheBytes    *int64              `json:"buildCacheBytes"`
	OrphanVolumes      []orphanVolumeJSON  `json:"orphanVolumes"`
	OrphansUnavailable bool                `json:"orphanVolumesUnavailable"`
	LastCleanup        *cleanupJSON        `json:"lastCleanup"`
	OpenCleanup        *cleanupJSON        `json:"openCleanup"`
	Settings           cleanupSettingsJSON `json:"settings"`
}

// spaceImagesJSON is schema SpaceImages.
type spaceImagesJSON struct {
	Count       int       `json:"count"`
	Bytes       int64     `json:"bytes"`
	UnusedCount int       `json:"unusedCount"`
	UnusedBytes int64     `json:"unusedBytes"`
	Truncated   bool      `json:"truncated"`
	MeasuredAt  time.Time `json:"measuredAt"`
}

type spaceCountJSON struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// orphanVolumeJSON is schema OrphanVolume. bytes, measuredAt and lastDeployedAt are null when not known;
// app, project and environment are "" when the volume's name doesn't read as an app's.
type orphanVolumeJSON struct {
	Name           string     `json:"name"`
	App            string     `json:"app"`
	Project        string     `json:"project"`
	Environment    string     `json:"environment"`
	AppExists      bool       `json:"appExists"`
	Bytes          *int64     `json:"bytes"`
	MeasuredAt     *time.Time `json:"measuredAt"`
	LastDeployedAt *time.Time `json:"lastDeployedAt"`
	InUse          bool       `json:"inUse"`
	ID             string     `json:"id"`
}

// cleanupJSON is schema Cleanup. startedAt and finishedAt are null until set.
type cleanupJSON struct {
	ID             string     `json:"id"`
	Server         string     `json:"server"`
	Trigger        string     `json:"trigger"`
	DockerVolumes  bool       `json:"dockerVolumes"`
	Status         string     `json:"status"`
	RequestedAt    time.Time  `json:"requestedAt"`
	StartedAt      *time.Time `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
	ImagesRemoved  int        `json:"imagesRemoved"`
	BytesFreed     int64      `json:"bytesFreed"`
	VolumesRemoved int        `json:"volumesRemoved"`
	Error          string     `json:"error"`
}

// volumeDeletionJSON is schema VolumeDeletion. finishedAt is null until the deletion ends.
type volumeDeletionJSON struct {
	ID            string     `json:"id"`
	Volume        string     `json:"volume"`
	App           string     `json:"app"`
	Server        string     `json:"server"`
	BackUpFirst   bool       `json:"backUpFirst"`
	Status        string     `json:"status"`
	RequestedAt   time.Time  `json:"requestedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	ArchiveObject string     `json:"archiveObject"`
	Bytes         int64      `json:"bytes"`
	Error         string     `json:"error"`
}

// cleanupSettingsJSON is schema CleanupSettings.
type cleanupSettingsJSON struct {
	Automatic       bool `json:"automatic"`
	KeepDeployments int  `json:"keepDeployments"`
}

func toCleanupJSON(c store.ImageCleanup) cleanupJSON {
	return cleanupJSON{ID: c.ID, Server: c.ServerName, Trigger: c.Trigger, DockerVolumes: c.DockerVolumes, Status: c.Status,
		RequestedAt: c.RequestedAt.UTC(), StartedAt: timePtr(c.StartedAt), FinishedAt: timePtr(c.FinishedAt),
		ImagesRemoved: c.ImagesRemoved, BytesFreed: c.BytesFreed, VolumesRemoved: c.VolumesRemoved, Error: c.Error}
}

func cleanupPtr(c *store.ImageCleanup) *cleanupJSON {
	if c == nil {
		return nil
	}
	out := toCleanupJSON(*c)
	return &out
}

func toVolumeDeletionJSON(d store.VolumeDeletion) volumeDeletionJSON {
	return volumeDeletionJSON{ID: d.ID, Volume: d.Volume, App: d.AppID, Server: d.ServerName, BackUpFirst: d.BackUpFirst,
		Status: d.Status, RequestedAt: d.RequestedAt.UTC(), FinishedAt: timePtr(d.FinishedAt),
		ArchiveObject: d.ArchiveObject, Bytes: d.Bytes, Error: d.Error}
}

func toCleanupSettingsJSON(c store.CleanupSettings) cleanupSettingsJSON {
	return cleanupSettingsJSON{Automatic: c.Automatic, KeepDeployments: c.KeepDeployments}
}

func toOrphanVolumeJSON(o service.OrphanVolume) orphanVolumeJSON {
	out := orphanVolumeJSON{Name: o.Name, App: o.App, Project: o.Place.Project, Environment: o.Place.Environment,
		AppExists: o.AppExists, Bytes: o.Bytes, LastDeployedAt: timePtr(o.LastDeployedAt), InUse: o.InUse, ID: o.ID}
	if o.Bytes != nil {
		out.MeasuredAt = timePtr(o.MeasuredAt)
	}
	return out
}

func toServerSpaceJSON(v service.SpaceView) serverSpaceJSON {
	out := serverSpaceJSON{
		Server: v.Server, Managed: v.Managed, ReclaimableBytes: v.ReclaimableBytes,
		ImagesUnavailable: v.ImagesUnavailable, BuildCacheBytes: v.BuildCacheBytes,
		OrphanVolumes: make([]orphanVolumeJSON, 0, len(v.Orphans)), OrphansUnavailable: v.OrphansUnavailable,
		LastCleanup: cleanupPtr(v.LastCleanup), OpenCleanup: cleanupPtr(v.OpenCleanup),
		Settings: toCleanupSettingsJSON(v.Settings),
	}
	if im := v.Images; im != nil {
		out.Images = &spaceImagesJSON{Count: im.Count, Bytes: im.Bytes, UnusedCount: im.UnusedCount, UnusedBytes: im.UnusedBytes,
			Truncated: im.Truncated, MeasuredAt: im.MeasuredAt.UTC()}
	}
	if dv := v.DockerVolumes; dv != nil {
		out.DockerVolumes = &spaceCountJSON{Count: dv.Count, Bytes: dv.Bytes}
	}
	for _, o := range v.Orphans {
		out.OrphanVolumes = append(out.OrphanVolumes, toOrphanVolumeJSON(o))
	}
	return out
}

// getServerSpace answers GET /v1/servers/{name}/space.
func (s *server) getServerSpace(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	v, err := s.svc.ServerSpace(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toServerSpaceJSON(v))
	return nil
}

// cleanServer answers POST /v1/servers/{name}/cleanups with 202: the clean-up is requested, and the
// server's agent runs it at its next sync. The body, which may be left out, says whether Docker's
// leftover volumes go too.
func (s *server) cleanServer(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	var in struct {
		DockerVolumes bool `json:"dockerVolumes"`
	}
	if _, err := decodeOptionalJSON(r, &in); err != nil {
		return err
	}
	name := r.PathValue("name")
	c, err := s.svc.RequestCleanup(r.Context(), name, store.CleanupByOwner, in.DockerVolumes)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/servers/"+name+"/cleanups/"+c.ID)
	writeJSON(w, http.StatusAccepted, toCleanupJSON(c))
	return nil
}

// listServerCleanups answers GET /v1/servers/{name}/cleanups: the newest 20, newest first.
func (s *server) listServerCleanups(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	list, err := s.svc.ServerCleanups(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	out := struct {
		Cleanups []cleanupJSON `json:"cleanups"`
	}{Cleanups: make([]cleanupJSON, 0, len(list))}
	for _, c := range list {
		out.Cleanups = append(out.Cleanups, toCleanupJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// getServerCleanup answers GET /v1/servers/{name}/cleanups/{id}.
func (s *server) getServerCleanup(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	c, err := s.svc.ServerCleanup(r.Context(), r.PathValue("name"), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toCleanupJSON(c))
	return nil
}

// deleteOrphanVolume answers POST /v1/volumes/{name}/deletions: 200 with the record when the volume is
// gone, 202 with its Location when it is being archived to the backup destination first. The owner
// confirms by sending the volume's name.
func (s *server) deleteOrphanVolume(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	var in struct {
		Confirm     string `json:"confirm"`
		BackUpFirst bool   `json:"backUpFirst"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	name := r.PathValue("name")
	d, err := s.svc.DeleteOrphanVolume(r.Context(), name, in.Confirm, in.BackUpFirst, app.TriggerManual)
	if err != nil {
		return err
	}
	if d.Status == store.DeletionArchiving {
		w.Header().Set("Location", "/v1/volumes/"+name+"/deletions/"+d.ID)
		writeJSON(w, http.StatusAccepted, toVolumeDeletionJSON(d))
		return nil
	}
	writeJSON(w, http.StatusOK, toVolumeDeletionJSON(d))
	return nil
}

// getVolumeDeletion answers GET /v1/volumes/{name}/deletions/{id}.
func (s *server) getVolumeDeletion(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	d, err := s.svc.GetVolumeDeletion(r.Context(), r.PathValue("name"), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toVolumeDeletionJSON(d))
	return nil
}

// getCleanupSettings answers GET /v1/cleanup/settings.
func (s *server) getCleanupSettings(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	c, err := s.svc.CleanupSettings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toCleanupSettingsJSON(c))
	return nil
}

// putCleanupSettings answers PUT /v1/cleanup/settings with the settings as they are now. Both fields are
// required: a missing one is not "off" or "zero".
func (s *server) putCleanupSettings(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	var in struct {
		Automatic       *bool `json:"automatic"`
		KeepDeployments *int  `json:"keepDeployments"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Automatic == nil {
		return app.NewFieldError("automatic", "is required")
	}
	if in.KeepDeployments == nil {
		return app.NewFieldError("keepDeployments", "is required")
	}
	c, err := s.svc.PutCleanupSettings(r.Context(), *in.Automatic, *in.KeepDeployments)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toCleanupSettingsJSON(c))
	return nil
}
