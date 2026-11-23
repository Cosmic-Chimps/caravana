package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Orphan volumes (feature 045, plan D3): a Nomad host volume that no stored app declares. Dropping a
// volume from a definition leaves it, with its data, on its server (feature 016's warning says so); a
// failed delete of an app can too. Nothing deletes one by itself: the owner types its name, and may have
// it archived to the backup destination first.

// The refusals of DeleteOrphanVolume. A caller tests them with errors.Is.
var (
	// ErrVolumeNotFound: Nomad has no host volume with that name.
	ErrVolumeNotFound = errors.New("no such volume")
	// ErrVolumeNotOrphan: a stored app declares the volume. A *VolumeNotOrphanError.
	ErrVolumeNotOrphan = errors.New("an app declares the volume")
	// ErrVolumeInUse: a copy still claims the volume.
	ErrVolumeInUse = errors.New("a copy still uses the volume")
	// ErrDeletionRunning: a deletion of that volume is already going.
	ErrDeletionRunning = errors.New("a deletion of that volume is already going")
)

// VolumeNotOrphanError is ErrVolumeNotOrphan with the app that declares the volume.
type VolumeNotOrphanError struct{ Volume, App string }

func (e *VolumeNotOrphanError) Error() string {
	if e.App == "" && nomadjob.OwnVolume(e.Volume) {
		if e.Volume == nomadjob.ScanVolume {
			return "The volume " + e.Volume + " is Caravana's own (the image scanner's database): remove the image scanner in Operator › Security to delete it."
		}
		return "The volume " + e.Volume + " is Caravana's own (an app's build cache): it is deleted with the app."
	}
	return "App " + e.App + " declares the volume " + e.Volume + ": delete it with the app, or drop it from the definition first."
}

// Is makes errors.Is(err, ErrVolumeNotOrphan) true.
func (e *VolumeNotOrphanError) Is(target error) bool { return target == ErrVolumeNotOrphan }

// orphanVolume is a host volume that no stored app declares, with the app its name reads as.
type orphanVolume struct {
	Stub deploy.HostVolumeStub
	// App is the app ID the volume's name "<app>_<volume>" reads as, "" when it doesn't read as one.
	App string
	// AppExists says that app is stored.
	AppExists bool
}

// classifyOrphans picks the volumes no stored app declares (pure). A volume is declared when some app has a
// service volume whose nomadjob.VolumeName is its name; the others are orphans, by name order. The app a
// name reads as is the part before its first "_" (app IDs never contain one) when that is an app ID.
func classifyOrphans(stubs []deploy.HostVolumeStub, apps []store.AppRecord) []orphanVolume {
	declared := map[string]bool{}
	exists := map[string]bool{}
	for _, rec := range apps {
		exists[rec.App.ID] = true
		for _, svc := range rec.App.Services {
			for _, v := range svc.Volumes {
				declared[nomadjob.VolumeName(rec.App.ID, v.Name)] = true
			}
		}
	}
	var out []orphanVolume
	for _, stub := range stubs {
		// Caravana's own volumes (the image scanner's database, feature 044) belong to no app and are
		// never orphans: the add-on's Remove deletes them.
		if declared[stub.Name] || nomadjob.OwnVolume(stub.Name) {
			continue
		}
		o := orphanVolume{Stub: stub}
		if id, _, ok := strings.Cut(stub.Name, "_"); ok {
			if _, err := app.PlaceOf(id); err == nil {
				o.App, o.AppExists = id, exists[id]
			}
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stub.Name < out[j].Stub.Name })
	return out
}

// OrphanVolume is one orphan volume as the console and the API show it.
type OrphanVolume struct {
	// Name is Nomad's name for it, <app>_<volume>; ID is Nomad's ID, which is also its directory's name on
	// the server.
	Name, ID string
	// App is the app the name reads as, "" when it doesn't read as one; Place is where that app is and
	// AppExists says it is still stored.
	App       string
	Place     app.Place
	AppExists bool
	// Server is the name of the server that holds it.
	Server string
	// Bytes is nil until the server's agent has measured the directory; MeasuredAt is when.
	Bytes      *int64
	MeasuredAt time.Time
	// LastDeployedAt is when the newest deployment whose definition declared the volume was made; zero when
	// that isn't known (the app was deleted, or no recorded deployment had it).
	LastDeployedAt time.Time
	// InUse says a copy claims the volume now, so it can't be deleted.
	InUse bool
}

// orphansOn lists the orphan volumes on one Nomad node, each read once more for the copies that claim it,
// its size from sizes (by the volume's ID) and when the deployment last had it. A Nomad that can't be read
// is an error.
func (s *Service) orphansOn(ctx context.Context, nodeID, serverName string, sizes map[string]int64, measuredAt time.Time) ([]OrphanVolume, error) {
	stubs, err := s.deployer.AllHostVolumes(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	var out []OrphanVolume
	for _, o := range classifyOrphans(stubs, apps) {
		if o.Stub.NodeID != nodeID {
			continue
		}
		v := OrphanVolume{Name: o.Stub.Name, ID: o.Stub.ID, App: o.App, AppExists: o.AppExists, Server: serverName}
		if o.App != "" {
			v.Place, _ = app.PlaceOf(o.App)
			v.LastDeployedAt = s.lastDeployedWith(ctx, o.App, strings.TrimPrefix(o.Stub.Name, o.App+"_"))
		}
		if b, ok := sizes[o.Stub.ID]; ok {
			v.Bytes, v.MeasuredAt = &b, measuredAt
		}
		if detail, err := s.deployer.HostVolumeDetail(ctx, o.Stub.ID); err == nil {
			v.InUse = len(detail.Allocations) > 0
		} else if !errors.Is(err, deploy.ErrVolumeNotFound) {
			s.log.Warn("orphan volumes: read a volume", "volume", o.Stub.Name, "error", err)
		}
		out = append(out, v)
	}
	return out, nil
}

// lastDeployedWith is when the newest deployment of the app whose recorded definition declares the
// volume was made; zero when there is none (the app is gone or never had it).
func (s *Service) lastDeployedWith(ctx context.Context, appID, volume string) time.Time {
	deps, err := s.store.ListDeployments(ctx, appID, 0)
	if err != nil {
		return time.Time{}
	}
	for _, d := range deps { // newest first
		if d.Definition == nil {
			continue
		}
		for _, svc := range d.Definition.Services {
			if slices.ContainsFunc(svc.Volumes, func(v app.Volume) bool { return v.Name == volume }) {
				return d.CreatedAt
			}
		}
	}
	return time.Time{}
}

// newDeletionID is "vd_" and 26 lowercase base32 characters.
func newDeletionID() string { return "vd_" + strings.ToLower(rand.Text()) }

// DeleteOrphanVolume deletes a host volume no stored app declares, and the data in it, through Nomad. The
// owner confirms by typing its name (confirm, a field error on confirm when it differs). The volume must
// exist (ErrVolumeNotFound), be an orphan (ErrVolumeNotOrphan) and be claimed by no copy (ErrVolumeInUse);
// one deletion per volume at a time (ErrDeletionRunning). backUpFirst archives it to the backup destination
// before deleting (ErrNoBackupDestination without one): the answer is the record, archiving, and a goroutine
// archives, deletes and settles it (archiveThenDelete). The deletion is recorded, for the feed, whether or
// not it works. trigger says who asked (console or manual).
func (s *Service) DeleteOrphanVolume(ctx context.Context, name, confirm string, backUpFirst bool, trigger app.Trigger) (store.VolumeDeletion, error) {
	if confirm != name {
		return store.VolumeDeletion{}, app.NewFieldError("confirm", "type the volume's name, %s, to confirm", name)
	}
	stubs, err := s.deployer.AllHostVolumes(ctx)
	if err != nil {
		return store.VolumeDeletion{}, err
	}
	idx := slices.IndexFunc(stubs, func(v deploy.HostVolumeStub) bool { return v.Name == name })
	if idx < 0 {
		return store.VolumeDeletion{}, &RefusalError{Field: "name", Err: ErrVolumeNotFound}
	}
	stub := stubs[idx]
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return store.VolumeDeletion{}, err
	}
	orphans := classifyOrphans([]deploy.HostVolumeStub{stub}, apps)
	if len(orphans) == 0 {
		return store.VolumeDeletion{}, &RefusalError{Field: "name", Err: &VolumeNotOrphanError{Volume: name, App: appDeclaring(name, apps)}}
	}
	detail, err := s.deployer.HostVolumeDetail(ctx, stub.ID)
	if errors.Is(err, deploy.ErrVolumeNotFound) {
		return store.VolumeDeletion{}, &RefusalError{Field: "name", Err: ErrVolumeNotFound}
	}
	if err != nil {
		return store.VolumeDeletion{}, err
	}
	if len(detail.Allocations) > 0 {
		return store.VolumeDeletion{}, &RefusalError{Field: "name", Err: ErrVolumeInUse}
	}
	var dest deploy.BackupDestination
	if backUpFirst {
		var ok bool
		if dest, ok, err = s.backupDestination(ctx); err != nil {
			return store.VolumeDeletion{}, err
		} else if !ok {
			return store.VolumeDeletion{}, ErrNoBackupDestination
		}
	}

	s.spaceMu.Lock()
	if s.deleting[name] {
		s.spaceMu.Unlock()
		return store.VolumeDeletion{}, &RefusalError{Field: "name", Err: ErrDeletionRunning}
	}
	s.deleting[name] = true
	s.spaceMu.Unlock()
	handedOver := false
	release := func() {
		s.spaceMu.Lock()
		delete(s.deleting, name)
		s.spaceMu.Unlock()
	}
	defer func() {
		if !handedOver {
			release()
		}
	}()

	status := store.DeletionDeleting
	if backUpFirst {
		status = store.DeletionArchiving
	}
	server, bytes := s.volumeFacts(ctx, stub)
	d, err := s.store.CreateVolumeDeletion(ctx, store.VolumeDeletion{
		ID: newDeletionID(), Volume: name, AppID: orphans[0].App, ServerName: server, Trigger: string(trigger),
		RequestedAt: s.clock.Now().UTC(), Status: status, Bytes: bytes, BackUpFirst: backUpFirst,
	})
	if err != nil {
		return store.VolumeDeletion{}, fmt.Errorf("delete volume %s: %w", name, err)
	}
	if backUpFirst {
		// The archive can take many minutes: the record is the answer, and the goroutine holds the volume's
		// slot in s.deleting until it ends. It runs under the process's context, so a shutdown stops it and
		// leaves the record open for failInterruptedDeletions at the next start.
		handedOver = true
		s.watchers.Add(1)
		go func() {
			defer s.watchers.Done()
			defer release()
			s.archiveThenDelete(s.background(), d, stub, dest)
		}()
		s.publish(Change{What: ChangeServers})
		return d, nil
	}
	err = s.deployer.DeleteHostVolume(ctx, stub.ID)
	if errors.Is(err, deploy.ErrVolumeInUse) {
		err = &RefusalError{Field: "name", Err: ErrVolumeInUse}
	}
	status, reason := store.DeletionDone, ""
	if err != nil {
		status, reason = store.DeletionFailed, truncateBytes(err.Error(), 300)
	}
	if serr := s.store.SettleVolumeDeletion(context.WithoutCancel(ctx), d.ID, status, s.clock.Now().UTC(), reason); serr != nil {
		s.log.Warn("delete volume: settle the record", "volume", name, "error", serr)
	}
	s.publish(Change{What: ChangeServers})
	if err != nil {
		return store.VolumeDeletion{}, err
	}
	return s.store.GetVolumeDeletion(ctx, d.ID)
}

// archiveThenDelete archives the volume to dest, then deletes it, and settles the record d (archiving) as
// done or failed. A failed archive leaves the volume as it was. A context that ends first (the process is
// shutting down) leaves the record open: the next start settles it as failed.
func (s *Service) archiveThenDelete(ctx context.Context, d store.VolumeDeletion, stub deploy.HostVolumeStub, dest deploy.BackupDestination) {
	settle := func(status, reason string) {
		// Not the archive's context: the record is settled even as the process stops.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.store.SettleVolumeDeletion(sctx, d.ID, status, s.clock.Now().UTC(), truncateBytes(reason, 300)); err != nil {
			s.log.Warn("delete volume: settle the record", "volume", d.Volume, "error", err)
		}
		s.publish(Change{What: ChangeServers})
	}
	object, archiveBytes, err := s.deployer.ArchiveVolume(ctx, d.Volume, d.AppID, dest)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.log.Warn("delete volume: the archive failed", "volume", d.Volume, "error", err)
		settle(store.DeletionFailed, err.Error()+"; the volume was not deleted")
		return
	}
	// The record keeps the volume's own size when the agent measured it; the archive's is the only size
	// there is otherwise.
	size := d.Bytes
	if size == 0 {
		size = archiveBytes
	}
	if err := s.store.SetVolumeDeletionDeleting(context.WithoutCancel(ctx), d.ID, object, size); err != nil {
		s.log.Warn("delete volume: record the archive", "volume", d.Volume, "error", err)
	}
	s.publish(Change{What: ChangeServers})
	err = s.deployer.DeleteHostVolume(ctx, stub.ID)
	if err != nil && ctx.Err() != nil {
		return
	}
	if errors.Is(err, deploy.ErrVolumeInUse) {
		err = &RefusalError{Field: "name", Err: ErrVolumeInUse}
	}
	if err != nil {
		settle(store.DeletionFailed, "the archive was made at "+object+", but the volume was not deleted: "+err.Error())
		return
	}
	settle(store.DeletionDone, "")
}

// appDeclaring is the ID of the app whose definition declares the volume, "" when none does.
func appDeclaring(volume string, apps []store.AppRecord) string {
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			for _, v := range svc.Volumes {
				if nomadjob.VolumeName(rec.App.ID, v.Name) == volume {
					return rec.App.ID
				}
			}
		}
	}
	return ""
}

// volumeFacts is the name of the server a volume is on and its size as that server's agent last measured
// it: "" and 0 when either isn't known.
func (s *Service) volumeFacts(ctx context.Context, stub deploy.HostVolumeStub) (server string, bytes int64) {
	dir, err := s.directory(ctx)
	if err != nil {
		return "", 0
	}
	node, ok := dir.byID(stub.NodeID)
	if !ok {
		return "", 0
	}
	row, err := s.store.ServerByName(ctx, node.Name)
	if err != nil {
		return node.Name, 0
	}
	if report, ok := parseReport(row.Report); ok && report.Space != nil {
		for _, v := range report.Space.Volumes {
			if v.ID == stub.ID {
				return node.Name, v.Bytes
			}
		}
	}
	return node.Name, 0
}

// GetVolumeDeletion reads one deletion of a volume. One that doesn't exist, or is of another volume, is
// ErrDeletionNotFound.
func (s *Service) GetVolumeDeletion(ctx context.Context, name, id string) (store.VolumeDeletion, error) {
	d, err := s.store.GetVolumeDeletion(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && d.Volume != name) {
		return store.VolumeDeletion{}, &RefusalError{Field: "id", Err: ErrDeletionNotFound}
	}
	return d, err
}

// ErrDeletionNotFound: no deletion has that ID for that volume.
var ErrDeletionNotFound = errors.New("no such deletion")

// failInterruptedDeletions settles the deletions a previous process left open: nobody is left to finish
// them, and what happened to the volume isn't known, so the record says it failed. A deletion that was to
// back up first may have left its archive job running in Nomad with the destination's keys in the job's
// variable: both are removed (best effort, only logged when Nomad refuses).
func (s *Service) failInterruptedDeletions(ctx context.Context) error {
	open, err := s.store.OpenVolumeDeletions(ctx)
	if err != nil {
		return fmt.Errorf("settle interrupted deletions: %w", err)
	}
	for _, d := range open {
		if d.BackUpFirst {
			if err := s.deployer.RemoveArchive(ctx, d.Volume); err != nil {
				s.log.Warn("settle interrupted deletion: remove the archive job", "volume", d.Volume, "error", err)
			}
		}
		if err := s.store.SettleVolumeDeletion(ctx, d.ID, store.DeletionFailed, time.Now().UTC(), "Caravana restarted during it"); err != nil {
			return fmt.Errorf("settle interrupted deletion %s: %w", d.ID, err)
		}
	}
	return nil
}
