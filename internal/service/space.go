package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// Reclaiming space (feature 045): which images a server must keep. The agent removes the images no
// container uses and the console doesn't ask to keep, so this file is the keep rule: the digests that
// a rollback, a stopped app's Start, a deploy under way or a helper task would otherwise pull again.

// maxKeepDigests is the most digests a clean-up request names; the agent refuses more (contracts/agent.md §2).
const maxKeepDigests = 4096

// ErrKeepListTooLong: the digests to keep don't fit in a clean-up request. Lowering how many
// deployments keep their images is the owner's way out.
var ErrKeepListTooLong error = sentence("Caravana keeps too many images to name them all to a server (at most 4096): lower how many deployments keep their images in the clean-up settings.")

// pinnedDigestRE finds the digest of an image reference pinned by digest: repo@sha256:<64 hex>.
var pinnedDigestRE = regexp.MustCompile(`@(sha256:[0-9a-f]{64})$`)

// digestOf is the digest of a pinned image reference; false for a tag or anything else.
func digestOf(image string) (string, bool) {
	m := pinnedDigestRE.FindStringSubmatch(image)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// keepFrom is the keep rule, pure (spec 045, plan D3). The digests kept are those of
//   - for each app, its newest n healthy deployments that recorded their definition (what a rollback
//     deploys again, with no tag lookup);
//   - every deployment still pending or running (its images are pulling or running);
//   - every stored app's current definition that names an image by digest (a stopped app starts again
//     from its last deployment's digests, and an app stored with a digest runs it);
//   - the helper images (the names sidecar, the backup tool, the SQLite sidecar).
//
// deps holds each app's deployments newest first. The answer is sorted and has each digest once;
// more than maxKeepDigests is ErrKeepListTooLong.
func keepFrom(apps []store.AppRecord, deps map[string][]store.DeploymentRecord, helpers []string, n int) ([]string, error) {
	keep := map[string]bool{}
	add := func(image string) {
		if d, ok := digestOf(image); ok {
			keep[d] = true
		}
	}
	for _, h := range helpers {
		add(h)
	}
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			add(svc.Image)
		}
		healthy := 0
		for _, d := range deps[rec.App.ID] {
			switch d.Status {
			case store.StatusPending, store.StatusRunning:
			case store.StatusHealthy:
				if d.Definition == nil || healthy >= n {
					continue
				}
				healthy++
			default:
				continue
			}
			for _, image := range d.Deployment.Digests {
				add(image)
			}
			if d.Definition != nil {
				for _, svc := range d.Definition.Services {
					add(svc.Image)
				}
			}
		}
	}
	if len(keep) > maxKeepDigests {
		return nil, ErrKeepListTooLong
	}
	out := make([]string, 0, len(keep))
	for d := range keep {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// keepDigests reads what keepFrom needs from the store: the settings' number of deployments, every
// app, each app's newest healthy deployments and every deployment under way. One read per app, which
// is fine once a day (and when the owner asks) for hundreds of apps.
func (s *Service) keepDigests(ctx context.Context) ([]string, error) {
	settings, err := s.store.CleanupSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("keep list: %w", err)
	}
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("keep list: %w", err)
	}
	unfinished, err := s.store.ListUnfinishedDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("keep list: %w", err)
	}
	deps := map[string][]store.DeploymentRecord{}
	for _, d := range unfinished {
		deps[d.AppID] = append(deps[d.AppID], d)
	}
	for _, rec := range apps {
		healthy, err := s.store.ListHealthyDeployments(ctx, rec.App.ID, settings.KeepDeployments)
		if err != nil {
			return nil, fmt.Errorf("keep list: %w", err)
		}
		deps[rec.App.ID] = slices.Concat(deps[rec.App.ID], healthy)
	}
	return keepFrom(apps, deps, s.deployer.HelperImages(), settings.KeepDeployments)
}

// The words that say why a server's images aren't known.
const (
	imagesNoAgent     = "no agent"
	imagesNoInventory = "no inventory yet"
)

// SpaceImage is one image of a server's Docker as the space read shows it.
type SpaceImage struct {
	ID      string
	Tags    []string
	Digests []string
	Bytes   int64
	InUse   bool
	// Kept says one of its digests is in the keep list: a rollback, a stopped app or a helper needs it.
	Kept bool
}

// ImagesSpace is what a server's images add up to.
type ImagesSpace struct {
	Count, UnusedCount int
	Bytes, UnusedBytes int64
	// Truncated says the agent named only the largest 1000 images of more.
	Truncated bool
	// MeasuredAt is when the server's agent last reported.
	MeasuredAt time.Time
	// Unused are the images a clean-up would remove (no container uses them and none is kept), largest first.
	Unused []SpaceImage
}

// SpaceView is a server's space read: the images, Docker's leftovers, the build cache and the orphan
// volumes, with the last clean-up (feature 045, contracts/api.md ServerSpace).
type SpaceView struct {
	Server  string
	Managed bool
	// Images is nil when the server's images aren't known, and ImagesUnavailable says why ("no agent",
	// "no inventory yet", or the keep list's sentence).
	Images            *ImagesSpace
	ImagesUnavailable string
	// DockerVolumes and BuildCacheBytes are nil without an inventory.
	DockerVolumes   *NodeCount
	BuildCacheBytes *int64
	// Orphans are the volumes on this server that no stored app declares. OrphansUnavailable says Nomad
	// couldn't be read, so none could be listed.
	Orphans            []OrphanVolume
	OrphansUnavailable bool
	// ReclaimableBytes is what Clean up now and deleting the orphans would free: the unused images,
	// Docker's leftover volumes, the build cache, and the orphans that are measured and not in use.
	ReclaimableBytes int64
	// LastCleanup is the newest clean-up that ended; OpenCleanup the one under way. nil for none.
	LastCleanup, OpenCleanup *store.ImageCleanup
	Settings                 store.CleanupSettings
}

// spaceImages is the images part of the read (pure): the report's inventory against the keep list. An
// image is unused when no container uses it and none of its digests is kept.
func spaceImages(sp *NodeSpace, keep []string, seen time.Time) *ImagesSpace {
	kept := make(map[string]bool, len(keep))
	for _, d := range keep {
		kept[d] = true
	}
	out := &ImagesSpace{Truncated: sp.ImagesTruncated, MeasuredAt: seen}
	for _, img := range sp.Images {
		out.Count++
		out.Bytes += img.Bytes
		isKept := slices.ContainsFunc(img.Digests, func(d string) bool { return kept[d] })
		if img.InUse || isKept {
			continue
		}
		out.UnusedCount++
		out.UnusedBytes += img.Bytes
		out.Unused = append(out.Unused, SpaceImage{ID: img.ID, Tags: img.Tags, Digests: img.Digests, Bytes: img.Bytes})
	}
	sortedByBytes(out.Unused)
	return out
}

// ServerSpace reads a server's space: what its agent reported against the keep rule, and the orphan
// volumes Nomad has on its node. An unmanaged node (the lab's) has no agent, so its images are "no
// agent", and its orphan volumes are still listed. An unknown server is ErrServerNotFound. A Nomad that
// can't be read leaves the orphans out and says so.
func (s *Service) ServerSpace(ctx context.Context, name string) (SpaceView, error) {
	view, err := s.Server(ctx, name)
	if err != nil {
		return SpaceView{}, err
	}
	settings, err := s.store.CleanupSettings(ctx)
	if err != nil {
		return SpaceView{}, err
	}
	out := SpaceView{Server: view.Name, Managed: view.Managed, Settings: settings, ImagesUnavailable: imagesNoAgent}

	var sizes map[string]int64
	var measuredAt time.Time
	if view.Managed {
		row, err := s.store.ServerByName(ctx, name)
		if err != nil {
			return SpaceView{}, fmt.Errorf("server space: %w", err)
		}
		report, hasReport := parseReport(row.Report)
		switch {
		case !hasReport:
		case report.Space == nil:
			out.ImagesUnavailable = imagesNoInventory
		default:
			sizes, measuredAt = s.fillFromReport(ctx, &out, report.Space, row.SeenAt)
		}
		if err := s.fillCleanups(ctx, &out, row.Index); err != nil {
			return SpaceView{}, err
		}
	}

	if view.NodeID != "" {
		orphans, err := s.orphansOn(ctx, view.NodeID, view.Name, sizes, measuredAt)
		if err != nil {
			s.log.Warn("server space: list the orphan volumes", "server", name, "error", err)
			out.OrphansUnavailable = true
		}
		out.Orphans = orphans
	}
	for _, o := range out.Orphans {
		if o.Bytes != nil && !o.InUse {
			out.ReclaimableBytes += *o.Bytes
		}
	}
	return out, nil
}

// fillFromReport sets the images, Docker's leftovers and the build cache from the agent's report, and
// returns its volume sizes (by the volume's ID) and when they were measured. A keep list that is too long
// leaves the images unread and says so; any other failure to compute it is logged and does the same.
func (s *Service) fillFromReport(ctx context.Context, out *SpaceView, sp *NodeSpace, seen time.Time) (map[string]int64, time.Time) {
	keep, err := s.keepDigests(ctx)
	switch {
	case errors.Is(err, ErrKeepListTooLong):
		out.ImagesUnavailable = err.Error()
	case err != nil:
		s.log.Warn("server space: compute the keep list", "error", err)
		out.ImagesUnavailable = imagesNoInventory
	default:
		out.Images, out.ImagesUnavailable = spaceImages(sp, keep, seen), ""
		out.ReclaimableBytes += out.Images.UnusedBytes
	}
	dv, cache := sp.DockerVolumes, sp.BuildCacheBytes
	out.DockerVolumes, out.BuildCacheBytes = &dv, &cache
	out.ReclaimableBytes += dv.Bytes + cache

	sizes := make(map[string]int64, len(sp.Volumes))
	for _, v := range sp.Volumes {
		sizes[v.ID] = v.Bytes
	}
	var measured time.Time
	if t, err := time.Parse(time.RFC3339, sp.VolumesMeasuredAt); err == nil {
		measured = t.UTC()
	}
	return sizes, measured
}

// fillCleanups sets the server's newest ended clean-up and the open one.
func (s *Service) fillCleanups(ctx context.Context, out *SpaceView, serverIdx int) error {
	list, err := s.store.ListImageCleanups(ctx, serverIdx, cleanupHistory)
	if err != nil {
		return fmt.Errorf("server space: %w", err)
	}
	for i := range list { // newest first
		switch {
		case store.CleanupOpen(list[i].Status) && out.OpenCleanup == nil:
			out.OpenCleanup = &list[i]
		case !store.CleanupOpen(list[i].Status) && out.LastCleanup == nil:
			out.LastCleanup = &list[i]
		}
	}
	return nil
}
