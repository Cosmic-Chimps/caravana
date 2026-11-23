package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// backupJSON is one backup (schema Backup). Engine, StoppedSeconds and Volumes
// are feature 016's: only a volume run has the last two.
type backupJSON struct {
	ID      string `json:"id"`
	Service string `json:"service"`
	Engine  string `json:"engine"`
	Status  string `json:"status"`
	// Trigger is who started the run: timer (its schedule) or owner (Back up
	// now).
	Trigger    string     `json:"trigger"`
	Bytes      int64      `json:"bytes"`
	Message    string     `json:"message,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// StoppedSeconds is how long the service was stopped, a pointer so that a
	// run without the measurement leaves it out instead of saying 0.
	StoppedSeconds *float64 `json:"stoppedSeconds,omitempty"`
	// Volumes is one entry per archive, omitted
	// when empty so that a Postgres backup does not carry the field.
	Volumes []backupVolumeJSON `json:"volumes,omitempty"`
	// Pin is the label the backup is pinned under, left out when it isn't
	// (feature 036).
	Pin      string     `json:"pin,omitempty"`
	PinnedAt *time.Time `json:"pinnedAt,omitempty"`
	// Destination is where the backup went (feature 052), left out for a record from before
	// destinations had names. Expired says retention deleted it from there: it is listed while a
	// copy holds it. Copies are the copies of it to other destinations.
	Destination string           `json:"destination,omitempty"`
	Expired     bool             `json:"expired,omitempty"`
	Copies      []backupCopyJSON `json:"copies,omitempty"`
}

// backupCopyJSON is one copy of a backup to another destination (schema BackupCopy).
type backupCopyJSON struct {
	Destination string     `json:"destination"`
	Status      string     `json:"status"`
	Object      string     `json:"object,omitempty"`
	Bytes       int64      `json:"bytes"`
	Message     string     `json:"message,omitempty"`
	Expired     bool       `json:"expired"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

// backupVolumeJSON is one volume's archive in a volume backup (schema
// BackupVolume).
type backupVolumeJSON struct {
	Name string `json:"name"`
	// Bytes is the archive's size in the destination.
	Bytes int64 `json:"bytes"`
	// SizeBytes is the volume's data when it was archived. The store keeps
	// kilobytes, which is what du reports.
	SizeBytes int64 `json:"sizeBytes"`
}

func toBackupJSON(b store.BackupRecord) backupJSON {
	out := backupJSON{ID: b.ID, Service: b.Service, Engine: engineOrPostgres(b.Engine), Status: b.Status, Trigger: triggerOrTimer(b.Trigger),
		Bytes: b.Bytes, Message: b.Message, StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, StoppedSeconds: seconds(b.Stopped),
		Pin: b.Pin, PinnedAt: b.PinnedAt, Destination: b.Destination, Expired: b.Expired}
	for _, v := range b.Volumes {
		out.Volumes = append(out.Volumes, backupVolumeJSON{Name: v.Name, Bytes: v.Bytes, SizeBytes: v.SizeKB * 1024})
	}
	for _, c := range b.Copies {
		out.Copies = append(out.Copies, backupCopyJSON{Destination: c.Destination, Status: c.Status, Object: c.Object, Bytes: c.Bytes,
			Message: c.Message, Expired: c.Expired, FinishedAt: c.FinishedAt})
	}
	return out
}

// engineOrPostgres is the engine of a record: the store defaults it when it
// saves, but a record built by hand may not have one.
func engineOrPostgres(engine string) string {
	if engine == "" {
		return app.BackupPostgres
	}
	return engine
}

// triggerOrTimer is the trigger of a record: the store defaults it when it
// saves, but a record built by hand may not have one.
func triggerOrTimer(trigger string) string {
	if trigger == "" {
		return store.BackupTriggerTimer
	}
	return trigger
}

// seconds is d in seconds, or nil when d is nil.
func seconds(d *time.Duration) *float64 {
	if d == nil {
		return nil
	}
	s := d.Seconds()
	return &s
}

// restoreJSON is one restore (schema Restore).
type restoreJSON struct {
	ID             string     `json:"id"`
	Service        string     `json:"service"`
	BackupID       string     `json:"backupId"`
	Engine         string     `json:"engine"`
	Status         string     `json:"status"`
	Message        string     `json:"message,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	StoppedSeconds *float64   `json:"stoppedSeconds,omitempty"`
	// Trigger is who started it: owner or schedule. Pin is, for a scheduled
	// restore, the label it restored from (feature 036).
	Trigger string `json:"trigger"`
	Pin     string `json:"pin,omitempty"`
	// Destination is where the restore read the backup from (feature 052).
	Destination string `json:"destination,omitempty"`
}

func toRestoreJSON(r store.RestoreRecord) restoreJSON {
	return restoreJSON{ID: r.ID, Service: r.Service, BackupID: r.BackupID, Engine: engineOrPostgres(r.Engine), Status: r.Status,
		Message: r.Message, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, StoppedSeconds: seconds(r.Stopped),
		Trigger: restoreTriggerOrOwner(r.Trigger), Pin: r.Pin, Destination: r.Destination}
}

// restoreTriggerOrOwner is the trigger of a restore record: the store defaults
// it when it saves, but a record built by hand may not have one.
func restoreTriggerOrOwner(trigger string) string {
	if trigger == "" {
		return store.RestoreTriggerOwner
	}
	return trigger
}

// listBackups answers GET /v1/apps/{id}/backups.
func (s *server) listBackups(w http.ResponseWriter, r *http.Request) error {
	backups, restores, err := s.svc.ListBackups(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	out := struct {
		Backups  []backupJSON  `json:"backups"`
		Restores []restoreJSON `json:"restores"`
	}{Backups: []backupJSON{}, Restores: []restoreJSON{}}
	for _, b := range backups {
		out.Backups = append(out.Backups, toBackupJSON(b))
	}
	for _, x := range restores {
		out.Restores = append(out.Restores, toRestoreJSON(x))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createBackup answers POST /v1/apps/{id}/backups: a backup of one service
// now. 202: it runs in the background; list the backups to follow it.
func (s *server) createBackup(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Service string `json:"service"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	b, err := s.svc.BackUpNow(r.Context(), r.PathValue("id"), in.Service)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toBackupJSON(b))
	return nil
}

// restoreBackup answers POST /v1/apps/{id}/backups/{backupId}/restore. The body is optional:
// {"from": "<destination>"} names the place to read the backup from (feature 052).
func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		From string `json:"from"`
	}
	if _, err := decodeOptionalJSON(r, &in); err != nil {
		return err
	}
	x, err := s.svc.Restore(r.Context(), r.PathValue("id"), r.PathValue("backupId"), in.From)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toRestoreJSON(x))
	return nil
}

// pinBackup answers PUT /v1/apps/{id}/backups/{backupId}/pin.
func (s *server) pinBackup(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Label string `json:"label"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	b, err := s.svc.PinBackup(r.Context(), r.PathValue("id"), r.PathValue("backupId"), in.Label)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toBackupJSON(b))
	return nil
}

// unpinBackup answers DELETE /v1/apps/{id}/backups/{backupId}/pin.
func (s *server) unpinBackup(w http.ResponseWriter, r *http.Request) error {
	b, err := s.svc.UnpinBackup(r.Context(), r.PathValue("id"), r.PathValue("backupId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toBackupJSON(b))
	return nil
}

// backupError maps the backup errors to their codes; mapError calls it.
func backupError(err error) *Error {
	switch {
	case errors.Is(err, service.ErrBackupRunning):
		return &Error{Status: http.StatusConflict, Code: codeBackupRunning, Message: err.Error()}
	case errors.Is(err, service.ErrNoRunningCopy):
		return &Error{Status: http.StatusConflict, Code: codeNoRunningCopy, Message: "The service has no running copy. Deploy it first."}
	case errors.Is(err, service.ErrNoBackupDestination):
		return &Error{Status: http.StatusConflict, Code: codeNoBackupDestination, Message: err.Error() + "; set one with PUT /v1/backups/destination"}
	case errors.Is(err, service.ErrBackupMissing):
		return &Error{Status: http.StatusConflict, Code: codeBackupMissing, Message: err.Error()}
	case errors.Is(err, service.ErrDestinationNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeDestinationNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrDestinationIsDefault):
		return &Error{Status: http.StatusConflict, Code: codeDestinationIsDefault, Message: err.Error()}
	}
	var inUse *service.DestinationInUseError
	if errors.As(err, &inUse) {
		return &Error{Status: http.StatusConflict, Code: codeDestinationInUse, Message: err.Error()}
	}
	return nil
}
