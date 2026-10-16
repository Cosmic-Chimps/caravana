package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// volumeJSON is one volume of an app (schema AppVolume; the contract's Volume
// is the one in an app's definition).
type volumeJSON struct {
	Name    string            `json:"name"`
	Mounted bool              `json:"mounted"`
	Mounts  []volumeMountJSON `json:"mounts"`
	// HostPath is left out when Nomad couldn't be read.
	HostPath string `json:"hostPath,omitempty"`
	// Size and LastBackup are pointers: nil leaves them out, which says "never
	// measured" and "no backup yet" better than a zero would.
	Size       *volumeSizeJSON       `json:"size,omitempty"`
	BackedUp   bool                  `json:"backedUp"`
	LastBackup *volumeLastBackupJSON `json:"lastBackup,omitempty"`
}

type volumeMountJSON struct {
	Service  string `json:"service"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly"`
}

type volumeSizeJSON struct {
	Bytes      int64     `json:"bytes"`
	MeasuredAt time.Time `json:"measuredAt"`
}

type volumeLastBackupJSON struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
}

func toVolumeJSON(v service.VolumeView) volumeJSON {
	out := volumeJSON{Name: v.Name, Mounted: v.Mounted, Mounts: []volumeMountJSON{}, HostPath: v.HostPath, BackedUp: v.BackedUp}
	for _, m := range v.Mounts {
		out.Mounts = append(out.Mounts, volumeMountJSON{Service: m.Service, Path: m.Path, ReadOnly: m.ReadOnly})
	}
	if v.Size != nil {
		out.Size = &volumeSizeJSON{Bytes: v.Size.Bytes, MeasuredAt: v.Size.MeasuredAt}
	}
	if v.LastBackup != nil {
		out.LastBackup = &volumeLastBackupJSON{ID: v.LastBackup.ID, Status: v.LastBackup.Status, StartedAt: v.LastBackup.StartedAt}
	}
	return out
}

// listVolumes answers GET /v1/apps/{id}/volumes: what the console's Volumes
// section shows. A Nomad that can't be read doesn't fail it (the host paths are
// left out), so it is 404 for an unknown app and otherwise 200.
func (s *server) listVolumes(w http.ResponseWriter, r *http.Request) error {
	views, err := s.svc.Volumes(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	out := make([]volumeJSON, 0, len(views))
	for _, v := range views {
		out = append(out, toVolumeJSON(v))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
