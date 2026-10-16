package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// backupDestinationJSON is the destination without its secret key (schema
// BackupDestination).
type backupDestinationJSON struct {
	Endpoint    string    `json:"endpoint"`
	Region      string    `json:"region"`
	Bucket      string    `json:"bucket"`
	Prefix      string    `json:"prefix"`
	AccessKeyID string    `json:"accessKeyId"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func toDestinationJSON(d store.BackupDestination) backupDestinationJSON {
	return backupDestinationJSON{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: d.Prefix,
		AccessKeyID: d.AccessKeyID, UpdatedAt: d.UpdatedAt}
}

// backupJSON is one backup (schema Backup). Engine, StoppedSeconds and Volumes
// are feature 016's: only a volume run has the last two.
type backupJSON struct {
	ID         string     `json:"id"`
	Service    string     `json:"service"`
	Engine     string     `json:"engine"`
	Status     string     `json:"status"`
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
	out := backupJSON{ID: b.ID, Service: b.Service, Engine: engineOrPostgres(b.Engine), Status: b.Status, Bytes: b.Bytes,
		Message: b.Message, StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, StoppedSeconds: seconds(b.Stopped)}
	for _, v := range b.Volumes {
		out.Volumes = append(out.Volumes, backupVolumeJSON{Name: v.Name, Bytes: v.Bytes, SizeBytes: v.SizeKB * 1024})
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
}

func toRestoreJSON(r store.RestoreRecord) restoreJSON {
	return restoreJSON{ID: r.ID, Service: r.Service, BackupID: r.BackupID, Engine: engineOrPostgres(r.Engine), Status: r.Status,
		Message: r.Message, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, StoppedSeconds: seconds(r.Stopped)}
}

// getBackupDestination answers GET /v1/backups/destination.
func (s *server) getBackupDestination(w http.ResponseWriter, r *http.Request) error {
	d, err := s.svc.GetBackupDestination(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDestinationJSON(d))
	return nil
}

// setBackupDestination answers PUT /v1/backups/destination. A body without
// secretAccessKey keeps the secret set before.
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

// restoreBackup answers POST /v1/apps/{id}/backups/{backupId}/restore.
func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request) error {
	x, err := s.svc.Restore(r.Context(), r.PathValue("id"), r.PathValue("backupId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toRestoreJSON(x))
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
	}
	return nil
}
