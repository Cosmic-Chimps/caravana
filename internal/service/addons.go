package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Add-ons (feature 044, D15): a small record, not a plugin system. An add-on is
// something the owner installs and removes from the console; until then it does
// nothing, costs nothing and pulls nothing. Today there is one, the image
// scanner (scanner.go). A later add-on adds a name and its own install and
// remove.

// The errors of the add-ons.
var (
	// ErrAddonBusy: the add-on is installing or being removed; wait for it to
	// finish.
	ErrAddonBusy = errors.New("the add-on is being installed or removed; wait for it to finish")
	// ErrAddonNotFound: Caravana has no add-on of that name.
	ErrAddonNotFound = errors.New("there is no add-on of that name")
	// ErrScannerNotInstalled: what an action of the image scanner needs, and the
	// add-on isn't installed.
	ErrScannerNotInstalled = errors.New("the image scanner isn't installed: install it in Operator › Security first")
)

// AddonStateAvailable is the state of an add-on with no record: it can be installed.
const AddonStateAvailable = "available"

// What the image scanner costs, as the console says it before the owner installs
// it (research R1): the database on one server's disk, and a download a day.
const (
	scannerDiskBytes     = 3500 << 20
	scannerDownloadBytes = 204 << 20
)

// The words of a failed install or remove, in Caravana's own.
const (
	reasonInstallRestarted  = "Caravana restarted during the install"
	reasonRemoveRestarted   = "Caravana restarted during the removal"
	reasonVolumeCreate      = "the scanner's volume couldn't be created"
	reasonVolumeDelete      = "the scanner's volume couldn't be deleted"
	reasonScannerRemoved    = "the image scanner was removed"
	reasonInstallNoDatabase = "the scanner didn't finish downloading its database"
	// removeStopWait is how long a removal waits for the scan going to stop.
	removeStopWait = 2 * time.Minute
)

// AddonView is an add-on as the console and the API show it.
type AddonView struct {
	// Name is the add-on's ID ("image-scanner") and Title what it is called.
	Name, Title string
	// State is available (no record), installing, installed, failed or removing.
	State string
	// Version is the version Caravana pins; for an add-on that isn't installed,
	// the one it would install.
	Version     string
	InstalledAt time.Time
	// Reason is why a failed install or removal failed.
	Reason string
	// DiskBytes and DownloadBytesPerDay are what it costs, for the page that
	// offers to install it.
	DiskBytes, DownloadBytesPerDay int64
	// Server is the server that holds the scanner's database and DatabaseBuilt
	// the build date of the newest database a scan used; both are empty until
	// known and only for an installed add-on.
	Server        string
	DatabaseBuilt time.Time
}

// Addons lists the add-ons Caravana knows, installed or not.
func (s *Service) Addons(ctx context.Context) ([]AddonView, error) {
	v, err := s.imageScannerView(ctx)
	if err != nil {
		return nil, err
	}
	return []AddonView{v}, nil
}

// Addon returns one add-on, or ErrAddonNotFound.
func (s *Service) Addon(ctx context.Context, name string) (AddonView, error) {
	if name != store.AddonImageScanner {
		return AddonView{}, fmt.Errorf("add-on %q: %w", name, ErrAddonNotFound)
	}
	return s.imageScannerView(ctx)
}

// InstallAddon installs the named add-on (ErrAddonNotFound for another name).
func (s *Service) InstallAddon(ctx context.Context, name string) (AddonView, error) {
	if name != store.AddonImageScanner {
		return AddonView{}, fmt.Errorf("add-on %q: %w", name, ErrAddonNotFound)
	}
	return s.InstallImageScanner(ctx)
}

// RemoveAddon removes the named add-on (ErrAddonNotFound for another name).
func (s *Service) RemoveAddon(ctx context.Context, name string) (AddonView, error) {
	if name != store.AddonImageScanner {
		return AddonView{}, fmt.Errorf("add-on %q: %w", name, ErrAddonNotFound)
	}
	return s.RemoveImageScanner(ctx)
}

// imageScannerView reads the add-on's record and what the page shows beside it.
func (s *Service) imageScannerView(ctx context.Context) (AddonView, error) {
	v := AddonView{
		Name: store.AddonImageScanner, Title: "Image scanner", State: AddonStateAvailable,
		Version: nomadjob.DefaultScannerVersion, DiskBytes: scannerDiskBytes, DownloadBytesPerDay: scannerDownloadBytes,
	}
	row, err := s.store.GetAddon(ctx, store.AddonImageScanner)
	if errors.Is(err, store.ErrNotFound) {
		return v, nil
	}
	if err != nil {
		return AddonView{}, err
	}
	v.State, v.Version, v.InstalledAt, v.Reason = string(row.State), row.Version, row.InstalledAt, row.Reason
	if row.State == store.AddonInstalled {
		s.fillScannerFacts(ctx, &v)
	}
	return v, nil
}

// fillScannerFacts adds the server that holds the database and the newest
// database date. Neither is worth failing the read for: Nomad down leaves the
// server empty.
func (s *Service) fillScannerFacts(ctx context.Context, v *AddonView) {
	if runs, err := s.store.ListScanRuns(ctx, 0); err == nil {
		for _, r := range runs {
			if r.Status == store.ScanOK && !r.DBBuilt.IsZero() {
				v.DatabaseBuilt = r.DBBuilt
				break
			}
		}
	}
	if node, found, err := s.deployer.ScanVolumeNode(ctx); err == nil && found {
		if dir, err := s.directory(ctx); err == nil {
			if n, ok := dir.byID(node); ok {
				v.Server = n.Name
			}
		}
	}
}

// scannerState is the add-on's state; "" when it has no record.
func (s *Service) scannerState(ctx context.Context) (store.AddonState, error) {
	row, err := s.store.GetAddon(ctx, store.AddonImageScanner)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.State, nil
}

// scannerInstalled says the add-on is installed: the only state in which the
// scanner queues, runs, gates and judges anything (D15).
func (s *Service) scannerInstalled(ctx context.Context) (bool, error) {
	st, err := s.scannerState(ctx)
	return st == store.AddonInstalled, err
}

// InstallImageScanner installs the image scanner: it checks the server that will
// hold the database has the room, creates the scanner's volume, downloads the
// database with a run of the scanner, and then queues a scan of every image an
// app runs. It answers at once with the add-on installing; the rest happens in
// the background and ends in installed or failed (with the reason). An add-on
// that is installing or being removed is ErrAddonBusy; one already installed is
// left alone. A failed one can be tried again.
func (s *Service) InstallImageScanner(ctx context.Context) (AddonView, error) {
	s.scan.addonMu.Lock()
	defer s.scan.addonMu.Unlock()
	switch st, err := s.scannerState(ctx); {
	case err != nil:
		return AddonView{}, err
	case st == store.AddonInstalling || st == store.AddonRemoving:
		return AddonView{}, ErrAddonBusy
	case st == store.AddonInstalled:
		return s.imageScannerView(ctx)
	}
	if err := s.store.SaveAddon(ctx, store.Addon{Name: store.AddonImageScanner, State: store.AddonInstalling,
		Version: nomadjob.DefaultScannerVersion}); err != nil {
		return AddonView{}, err
	}
	s.scansChanged()
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.runInstall(s.background())
	}()
	return s.imageScannerView(ctx)
}

// runInstall is the install's background half.
func (s *Service) runInstall(ctx context.Context) {
	fail := func(reason string) {
		s.log.Warn("the image scanner install failed", "reason", reason)
		s.settleAddon(ctx, store.AddonFailed, reason, time.Time{})
	}
	// The same room check every scan makes, on the server that will hold the
	// database; the words are the same.
	if reason := s.scanRoomBeforeInstall(ctx); reason != "" {
		fail(reason)
		return
	}
	if _, err := s.deployer.EnsureScanVolume(ctx); err != nil {
		if ctx.Err() != nil {
			return // shutting down: the next start settles the install
		}
		s.log.Warn("create the scanner's volume", "error", err)
		fail(reasonVolumeCreate)
		return
	}
	run, err := s.queueRun(ctx, "", store.ScanTriggerInstall, store.ScanSourceDatabase)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("queue the scanner's database run", "error", err)
			fail(reasonInstallNoDatabase)
		}
		return
	}
	done, err := s.ScanWait(ctx, run.ID)
	if err != nil {
		return // shutting down
	}
	if done.Status != store.ScanOK {
		reason := done.Reason
		if reason == "" {
			reason = reasonInstallNoDatabase
		}
		fail(reason)
		return
	}
	s.settleAddon(ctx, store.AddonInstalled, "", s.clock.Now().UTC())
	// Every image an app runs gets a first scan.
	uses, err := s.liveUses(ctx)
	if err != nil {
		s.log.Warn("list the images to scan after the install", "error", err)
		return
	}
	for _, image := range distinctImages(uses) {
		if _, err := s.queueScan(ctx, image, store.ScanTriggerInstall); err != nil {
			s.log.Warn("queue a scan after the install", "error", err)
			return
		}
	}
}

// settleAddon writes the add-on's final state of an install or a removal.
func (s *Service) settleAddon(ctx context.Context, state store.AddonState, reason string, installedAt time.Time) {
	ctx = context.WithoutCancel(ctx)
	row := store.Addon{Name: store.AddonImageScanner, State: state, Version: nomadjob.DefaultScannerVersion,
		Reason: reason, InstalledAt: installedAt}
	if prev, err := s.store.GetAddon(ctx, store.AddonImageScanner); err == nil && installedAt.IsZero() {
		row.InstalledAt = prev.InstalledAt
	}
	if err := s.store.SaveAddon(ctx, row); err != nil {
		s.log.Error("record the state of the image scanner", "state", state, "error", err)
		return
	}
	s.scansChanged()
}

// RemoveImageScanner removes the image scanner: queued scans are dropped, a
// running one is stopped, the scanner's volume (the database and the SBOMs) is
// deleted, and every stored result and run goes. It answers at once with the
// add-on removing. An add-on with no record is already removed and is returned
// as available; one installing or being removed is ErrAddonBusy. A failed removal
// leaves the add-on failed and can be tried again.
func (s *Service) RemoveImageScanner(ctx context.Context) (AddonView, error) {
	s.scan.addonMu.Lock()
	defer s.scan.addonMu.Unlock()
	switch st, err := s.scannerState(ctx); {
	case err != nil:
		return AddonView{}, err
	case st == "":
		return s.imageScannerView(ctx)
	case st == store.AddonInstalling || st == store.AddonRemoving:
		return AddonView{}, ErrAddonBusy
	}
	if err := s.store.SaveAddon(ctx, store.Addon{Name: store.AddonImageScanner, State: store.AddonRemoving,
		Version: nomadjob.DefaultScannerVersion}); err != nil {
		return AddonView{}, err
	}
	s.scan.mu.Lock()
	s.scan.removing = true
	s.scan.mu.Unlock()
	s.scansChanged()
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.runRemove(s.background())
	}()
	return s.imageScannerView(ctx)
}

// runRemove is the removal's background half.
func (s *Service) runRemove(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	defer func() {
		s.scan.mu.Lock()
		s.scan.removing = false
		s.scan.mu.Unlock()
	}()
	fail := func(reason string) {
		s.log.Warn("the image scanner removal failed", "reason", reason)
		s.settleAddon(ctx, store.AddonFailed, reason, time.Time{})
	}
	// Nothing queued will run, and the run going is stopped and its job purged
	// before the volume it holds is deleted.
	if _, err := s.store.FailOpenScanRuns(ctx, true, false, reasonScannerRemoved, s.clock.Now().UTC()); err != nil {
		s.log.Warn("drop the queued scans", "error", err)
	}
	s.stopCurrentScan(removeStopWait)
	if _, err := s.store.FailOpenScanRuns(ctx, true, true, reasonScannerRemoved, s.clock.Now().UTC()); err != nil {
		s.log.Warn("settle the scans still open", "error", err)
	}
	s.purgeLeftScanJobs(ctx)
	if err := s.deployer.DeleteScanVolume(ctx); err != nil {
		s.log.Warn("delete the scanner's volume", "error", err)
		fail(reasonVolumeDelete)
		return
	}
	if err := s.store.ForgetScans(ctx); err != nil {
		s.log.Warn("forget the stored scans", "error", err)
		fail("the stored scans couldn't be deleted")
		return
	}
	if err := s.store.DeleteAddon(ctx, store.AddonImageScanner); err != nil {
		s.log.Error("delete the image scanner's record", "error", err)
		return
	}
	s.scansChanged()
	s.publish(Change{What: ChangeAttention})
}
