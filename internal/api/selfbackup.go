package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Caravana's own backup (feature 063): the state of the daily backup, its settings, the recovery key and the
// runs. Admins read and ask for a backup now; owners change the settings and make the key. A deploy token is
// refused by the scope middleware's default deny, and every answer is Cache-Control: no-store. Nothing here
// holds a key, a variable or a byte of a backup: the one exception is createRecoveryKey's answer, which
// carries the recovery key itself, once.

// The codes of the self-backup calls.
const (
	codeSelfBackupRunning     = "self_backup_running"
	codeSelfBackupUnavailable = "self_backup_unavailable"
	codeSelfBackupNotFound    = "self_backup_not_found"
	codeRecoveryKeyExists     = "recovery_key_exists"
)

// selfBackupError maps the self-backup refusals to their codes; mapError calls it.
func selfBackupError(err error) *Error {
	var un *service.SelfBackupUnavailableError
	var re *service.RefusalError
	switch {
	case errors.Is(err, service.ErrSelfBackupRunning):
		return &Error{Status: http.StatusConflict, Code: codeSelfBackupRunning, Message: err.Error()}
	case errors.As(err, &un):
		return &Error{Status: http.StatusConflict, Code: codeSelfBackupUnavailable, Message: err.Error()}
	case errors.Is(err, service.ErrRecoveryKeyExists):
		return &Error{Status: http.StatusConflict, Code: codeRecoveryKeyExists, Message: err.Error()}
	case errors.Is(err, service.ErrSelfBackupNotFound):
		field := "id"
		if errors.As(err, &re) {
			field = re.Field
		}
		return &Error{Status: http.StatusNotFound, Code: codeSelfBackupNotFound, Field: field, Message: err.Error()}
	}
	return nil
}

// selfBackupRunJSON is schema SelfBackupRun. endedAt is null while it runs; object is empty until it succeeds.
type selfBackupRunJSON struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	Trigger        string     `json:"trigger"`
	Actor          string     `json:"actor"`
	StartedAt      time.Time  `json:"startedAt"`
	EndedAt        *time.Time `json:"endedAt"`
	Destination    string     `json:"destination"`
	Object         string     `json:"object"`
	Bytes          int64      `json:"bytes"`
	KeyFingerprint string     `json:"keyFingerprint"`
	Version        string     `json:"version"`
	Schema         int        `json:"schema"`
	Warning        string     `json:"warning"`
	Error          string     `json:"error"`
}

func toSelfBackupRunJSON(b store.SelfBackup) selfBackupRunJSON {
	return selfBackupRunJSON{ID: b.ID, Status: b.Status, Trigger: b.Trigger, Actor: b.Actor, StartedAt: b.StartedAt.UTC(),
		EndedAt: timePtr(b.EndedAt), Destination: b.Destination, Object: b.Object, Bytes: b.Bytes, KeyFingerprint: b.KeyFingerprint,
		Version: b.Version, Schema: b.Schema, Warning: b.Warning, Error: b.Error}
}

func selfBackupRunPtr(b *store.SelfBackup) *selfBackupRunJSON {
	if b == nil {
		return nil
	}
	out := toSelfBackupRunJSON(*b)
	return &out
}

// selfBackupSettingsJSON is schema SelfBackupSettings.
type selfBackupSettingsJSON struct {
	Enabled bool `json:"enabled"`
	Hour    int  `json:"hour"`
	Keep    int  `json:"keep"`
}

// selfBackupKeyJSON is what the answer says about the recovery key: its fingerprint and when it was made.
type selfBackupKeyJSON struct {
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

// selfBackupsJSON is schema SelfBackups. key is null until a recovery key was made, destination null while
// there is no default backup destination, and unavailable null while a backup can be made.
type selfBackupsJSON struct {
	Settings      selfBackupSettingsJSON `json:"settings"`
	Key           *selfBackupKeyJSON     `json:"key"`
	Destination   *string                `json:"destination"`
	Unavailable   *string                `json:"unavailable"`
	Running       *selfBackupRunJSON     `json:"running"`
	Last          *selfBackupRunJSON     `json:"last"`
	LastSucceeded *selfBackupRunJSON     `json:"lastSucceeded"`
}

func toSelfBackupsJSON(st service.SelfBackupStatus) selfBackupsJSON {
	out := selfBackupsJSON{
		Settings: selfBackupSettingsJSON{Enabled: st.Settings.Enabled, Hour: st.Settings.Hour, Keep: st.Settings.Keep},
		Running:  selfBackupRunPtr(st.Running), Last: selfBackupRunPtr(st.Last), LastSucceeded: selfBackupRunPtr(st.LastSucceeded),
	}
	if st.KeyFingerprint != "" {
		out.Key = &selfBackupKeyJSON{Fingerprint: st.KeyFingerprint, CreatedAt: st.KeyCreatedAt.UTC()}
	}
	if st.Destination != "" {
		out.Destination = &st.Destination
	}
	if st.Unavailable != "" {
		out.Unavailable = &st.Unavailable
	}
	return out
}

// getSelfBackups answers GET /v1/self-backups.
func (s *server) getSelfBackups(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	st, err := s.svc.SelfBackups(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toSelfBackupsJSON(st))
	return nil
}

// putSelfBackupSettings answers PUT /v1/self-backups/settings with the state as it is now. Every property is
// optional; a missing one stays as it is. An hour outside 0 to 23 or a keep outside 1 to 90 is a 422.
func (s *server) putSelfBackupSettings(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	var in struct {
		Enabled *bool `json:"enabled"`
		Hour    *int  `json:"hour"`
		Keep    *int  `json:"keep"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	st, err := s.svc.SetSelfBackupSettings(r.Context(), service.SelfBackupSettingsInput{Enabled: in.Enabled, Hour: in.Hour, Keep: in.Keep})
	if err != nil {
		return auditError(err)
	}
	writeJSON(w, http.StatusOK, toSelfBackupsJSON(st))
	return nil
}

// createRecoveryKey answers POST /v1/self-backups/recovery-key with 201 and the key, once. The body is
// optional: {"replace": true} makes a new key when one exists.
func (s *server) createRecoveryKey(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	var in struct {
		Replace bool `json:"replace"`
	}
	if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
	}
	res, err := s.svc.CreateRecoveryKey(r.Context(), in.Replace)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, struct {
		RecoveryKey string `json:"recoveryKey"`
		Fingerprint string `json:"fingerprint"`
	}{res.Key, res.Fingerprint})
	return nil
}

// startSelfBackup answers POST /v1/self-backups/runs with 202 and the run, running, and its Location.
func (s *server) startSelfBackup(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	run, err := s.svc.RequestSelfBackup(r.Context())
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/self-backups/runs/"+run.ID)
	writeJSON(w, http.StatusAccepted, toSelfBackupRunJSON(run))
	return nil
}

// listSelfBackupRuns answers GET /v1/self-backups/runs: the newest 30, newest first.
func (s *server) listSelfBackupRuns(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	list, err := s.svc.SelfBackupRuns(r.Context())
	if err != nil {
		return err
	}
	out := struct {
		Runs []selfBackupRunJSON `json:"runs"`
	}{Runs: make([]selfBackupRunJSON, 0, len(list))}
	for _, b := range list {
		out.Runs = append(out.Runs, toSelfBackupRunJSON(b))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// getSelfBackupRun answers GET /v1/self-backups/runs/{id}.
func (s *server) getSelfBackupRun(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	b, err := s.svc.SelfBackupRun(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toSelfBackupRunJSON(b))
	return nil
}
