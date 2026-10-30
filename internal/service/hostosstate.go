package service

import (
	"context"
	"encoding/json"
	"io/fs"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// What the host's operating system says about itself (feature 027): the reboot
// marker and apt's history of package runs, read by the sampler's tick from
// plain files (internal/hostos). The reboot state is kept in memory, like the
// node's facts: the files are the record, and a restart reads them again. Each
// new apt run becomes one host event.

// osHistoryRuns is how many of the newest apt runs a tick reads. A host that
// updates itself daily gets a handful a week; the rest of the month is for the
// page that lists past updates.
const osHistoryRuns = 10

// SetHostRoot sets the file system the host's operating system files are read
// from (apt's history, the reboot marker). The default is the host's own
// (hostos.Root); the lab and tests give a folder of fixtures, and
// CARAVANA_HOST_ROOT points it at one. The files are only ever read. Call it
// before Start.
func (s *Service) SetHostRoot(fsys fs.FS) {
	if fsys != nil {
		s.hostRoot = fsys
	}
}

// hostReboot is a copy of the newest reboot reading, nil before the first tick.
func (s *Service) hostReboot() *hostos.Reboot {
	st := &s.sampler
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.rebootRead {
		return nil
	}
	r := st.reboot
	r.Packages = slices.Clone(r.Packages)
	return &r
}

// sameReboot says two readings tell the page the same thing.
func sameReboot(a, b hostos.Reboot) bool {
	return a.Required == b.Required && a.Since.Equal(b.Since) && a.InstalledKernel == b.InstalledKernel &&
		slices.Equal(a.Packages, b.Packages)
}

// noteOS reads the host's files for one tick: it keeps the reboot state, records
// each package run it hasn't recorded, and tells open pages when either changed.
// A file that can't be read leaves things as they were; one that isn't there (a
// host that isn't Debian or Ubuntu) is the same as an empty history.
func (s *Service) noteOS(ctx context.Context) {
	st := &s.sampler
	changed := false

	reboot := hostos.ReadReboot(s.hostRoot)
	st.mu.Lock()
	if !st.rebootRead || !sameReboot(st.reboot, reboot) {
		st.reboot, st.rebootRead = reboot, true
		changed = true
	}
	st.mu.Unlock()

	runs, err := hostos.ReadHistory(s.hostRoot, osHistoryRuns)
	if err != nil {
		s.log.Warn("read the host's package history", "error", err)
	}
	// Oldest first, so the rows are written in the order things happened. A run
	// at or before the newest one this process has recorded is skipped without
	// touching the store; the store's key stops the rest from repeating after a
	// restart.
	newest := st.osSeen
	failed := false
	for _, run := range slices.Backward(runs) {
		if !run.Start.After(st.osSeen) {
			continue
		}
		added, err := s.recordPackageRun(ctx, run, reboot)
		if err != nil {
			failed = true
			continue
		}
		changed = changed || added
		if !failed {
			newest = run.Start
		}
	}
	st.osSeen = newest
	if changed {
		s.publish(Change{What: ChangeHost})
	}
}

// osPackagesDetail is the JSON of an os_packages host event.
type osPackagesDetail struct {
	Packages int    `json:"packages"`
	By       string `json:"by"` // "unattended" or "hand"
	Reboot   bool   `json:"reboot,omitempty"`
}

// recordPackageRun writes a host event for an apt run that changed packages.
// The event says a reboot is needed when the run installed a package the reboot
// marker names and the marker is not older than the run: the marker of a past
// boot can't belong to a run since. A run that changed no package (an
// autoremove) is not an update and is not recorded.
func (s *Service) recordPackageRun(ctx context.Context, run hostos.Run, reboot hostos.Reboot) (bool, error) {
	if run.Packages() == 0 {
		return false, nil
	}
	d := osPackagesDetail{Packages: run.Packages(), By: "hand"}
	if run.Automatic {
		d.By = "unattended"
	}
	d.Reboot = reboot.Required && !reboot.Since.Before(run.Start) && reboot.ListedIn(run)
	detail, err := json.Marshal(d)
	if err != nil {
		return false, err
	}
	added, err := s.store.AppendHostEvent(ctx, store.HostEvent{
		Kind: store.HostEventOSPackages, At: run.Start.UTC(), Detail: string(detail),
		Key: "os|" + run.Start.UTC().Format(time.RFC3339),
	})
	if err != nil {
		s.log.Warn("record a package run", "error", err)
		return false, err
	}
	return added, nil
}
