package service

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The volumes panel (feature 016, research R9 and R10): what an app's volumes
// are, where they live, how big they are and whether they are backed up. The
// definition and the store answer most of it; Nomad adds the host paths, the
// volumes no service mounts any more, and (through the copies' caravana-volumes
// tasks) fresh sizes. The panel must still answer when Nomad can't be read.

// How the sizes are read from the running copies: one small file each, with a
// short wait so one stuck node can't hold the page, and a few at a time so an
// app with many volume services doesn't open many connections at once.
const (
	volumeSizeTimeout  = 2 * time.Second
	volumeSizeParallel = 4
)

// VolumeMount is one place a volume is mounted: a service and the path it sees.
type VolumeMount struct {
	Service  string
	Path     string
	ReadOnly bool
}

// VolumeSize is the last measured size of a volume.
type VolumeSize struct {
	Bytes      int64
	MeasuredAt time.Time
}

// VolumeBackup is the newest volume backup of the service that holds a volume.
type VolumeBackup struct {
	ID        string
	Status    string // store.BackupRunning, BackupSucceeded or BackupFailed
	StartedAt time.Time
}

// VolumeView is one volume of an app, for the console and the API.
type VolumeView struct {
	// Name is the volume's name in the app's definition.
	Name string
	// Mounted is false for a volume Nomad still has for the app that no service
	// mounts: it stays until the app is deleted. Its Mounts are empty.
	Mounted bool
	Mounts  []VolumeMount
	// HostPath is the directory on the node. Empty when Nomad couldn't be read.
	HostPath string
	// Size is nil until the volume has been measured.
	Size *VolumeSize
	// BackedUp says the service that mounts the volume has a volume backup.
	// Then Schedule is its cron expression (the default spelled out) and Keep
	// how many it keeps, and LastBackup is its newest run, nil until it ran.
	BackedUp   bool
	Schedule   string
	Keep       int
	LastBackup *VolumeBackup
}

// Volumes lists the volumes of an app: those in its stored definition, then
// those Nomad has for it that no service mounts. An unknown app is
// store.ErrNotFound.
//
// Sizes are read from each running copy of a service with volumes and stored
// first, so the panel shows the newest measurement it can reach; if a copy or
// Nomad can't be read, the last stored size stays. A Nomad that fails leaves
// the host paths empty and the unmounted volumes out; nothing here fails the
// page for it.
func (s *Service) Volumes(ctx context.Context, appID string) ([]VolumeView, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}

	hostVols, err := s.deployer.HostVolumes(ctx, appID)
	if err != nil {
		s.log.Warn("volumes: read host volumes", "app", appID, "error", err)
		hostVols = nil
	}
	s.refreshVolumeSizes(ctx, rec.App)

	sizes, err := s.store.VolumeSizes(ctx, appID)
	if err != nil {
		return nil, err
	}
	backups, err := s.store.ListBackups(ctx, appID, 0)
	if err != nil {
		return nil, err
	}
	return volumeViews(rec.App, hostVols, sizes, backups), nil
}

// refreshVolumeSizes reads the sizes file of every running copy of a service
// with volumes and stores what it finds. Failures are logged, not returned: the
// stored sizes are a good enough answer.
//
// A size's time is when its copy last started a task (the first start, or the
// newest restart, whichever is later), which is when the caravana-volumes task
// measured; stamping it with the time of the read would make an old size look
// fresh. A copy with neither time is stamped now.
func (s *Service) refreshVolumeSizes(ctx context.Context, a app.App) {
	withVolumes := map[string]app.Service{}
	for _, svc := range a.Services {
		if len(svc.Volumes) > 0 {
			withVolumes[svc.Name] = svc
		}
	}
	if len(withVolumes) == 0 {
		return
	}
	copies, err := s.deployer.Copies(ctx, a.ID)
	if err != nil {
		s.log.Warn("volumes: list copies", "app", a.ID, "error", err)
		return
	}
	var running []deploy.Copy
	for _, c := range copies {
		if _, ok := withVolumes[c.Service]; ok && (c.Status == deploy.CopyHealthy || c.Status == deploy.CopyUnhealthy) {
			running = append(running, c)
		}
	}

	// A buffered channel is a counting semaphore in Go: sending takes a place
	// (and waits when all are taken), receiving gives it back. Each goroutine
	// writes only its own slot of results, so no lock is needed.
	places := make(chan struct{}, volumeSizeParallel)
	results := make([]map[string]int64, len(running))
	var wg sync.WaitGroup
	for i, c := range running {
		wg.Add(1)
		go func() {
			defer wg.Done()
			places <- struct{}{}
			defer func() { <-places }()
			readCtx, cancel := context.WithTimeout(ctx, volumeSizeTimeout)
			defer cancel()
			got, err := s.deployer.VolumeSizes(readCtx, c.ID)
			if err != nil {
				s.log.Warn("volumes: read sizes", "app", a.ID, "copy", c.ID, "error", err)
				return
			}
			results[i] = got
		}()
	}
	wg.Wait()

	for i, c := range running {
		at := measuredAt(c, s.clock.Now())
		for _, v := range withVolumes[c.Service].Volumes {
			kb, ok := results[i][v.Name]
			if !ok {
				continue
			}
			if err := s.store.SaveVolumeSize(ctx, a.ID, v.Name, kb, at); err != nil {
				s.log.Warn("volumes: save size", "app", a.ID, "volume", v.Name, "error", err)
			}
		}
	}
}

// measuredAt is when the copy's caravana-volumes task last ran: its tasks'
// first start or their newest restart, whichever is later; now when the copy
// has neither.
func measuredAt(c deploy.Copy, now time.Time) time.Time {
	var at time.Time
	for _, t := range []*time.Time{c.StartedAt, c.LastRestart} {
		if t != nil && t.After(at) {
			at = *t
		}
	}
	if at.IsZero() {
		return now.UTC()
	}
	return at.UTC()
}

// volumeViews puts the panel together from what is known. It is pure: no I/O,
// so the table test calls it directly.
//   - a is the stored definition: its volumes come first, in the order the
//     services list them, each with every mount;
//   - hostVols are Nomad's volumes for the app by name (nil when unreadable):
//     they give the host paths, and a volume no service mounts is added after
//     the others, sorted by name, as not mounted;
//   - sizes are the stored measurements by volume name;
//   - backups are the app's backup records, newest first. Only a record of the
//     volume engine counts: a Postgres service's volume is not backed up by it.
func volumeViews(a app.App, hostVols map[string]deploy.HostVolumeInfo, sizes map[string]store.VolumeSize, backups []store.BackupRecord) []VolumeView {
	var out []VolumeView
	index := map[string]int{} // volume name -> position in out
	for _, svc := range a.Services {
		for _, v := range svc.Volumes {
			i, seen := index[v.Name]
			if !seen {
				i = len(out)
				index[v.Name] = i
				out = append(out, VolumeView{Name: v.Name, Mounted: true})
			}
			out[i].Mounts = append(out[i].Mounts, VolumeMount{Service: svc.Name, Path: v.Path, ReadOnly: v.ReadOnly})
			if hasVolumeBackup(svc) && !out[i].BackedUp {
				out[i].BackedUp = true
				out[i].Schedule, out[i].Keep = svc.Backup.ScheduleOrDefault(), svc.Backup.KeepOrDefault()
				out[i].LastBackup = lastVolumeBackup(backups, svc.Name)
			}
		}
	}

	var unmounted []string
	for name := range hostVols {
		if _, ok := index[name]; !ok {
			unmounted = append(unmounted, name)
		}
	}
	slices.Sort(unmounted)
	for _, name := range unmounted {
		index[name] = len(out)
		out = append(out, VolumeView{Name: name})
	}

	for i := range out {
		out[i].HostPath = hostVols[out[i].Name].HostPath
		if size, ok := sizes[out[i].Name]; ok {
			out[i].Size = &VolumeSize{Bytes: size.KB * 1024, MeasuredAt: size.MeasuredAt}
		}
	}
	return out
}

// lastVolumeBackup is the newest backup of the service made by the volume
// engine, whatever its outcome: a failed one is what the owner needs to see.
// backups are newest first. Nil when the service has none.
func lastVolumeBackup(backups []store.BackupRecord, service string) *VolumeBackup {
	for _, b := range backups {
		if b.Service == service && b.Engine == app.BackupVolume {
			return &VolumeBackup{ID: b.ID, Status: b.Status, StartedAt: b.StartedAt}
		}
	}
	return nil
}
