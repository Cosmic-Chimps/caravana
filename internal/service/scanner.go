package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// Image scanning (feature 044). The scanner is Grype, run as one Nomad batch job
// per scan (nomadjob.ScanJob), one at a time in the whole installation. The
// store's scan_runs rows are the queue: queued, then running, then ok, failed or
// skipped. Everything here does nothing while the add-on isn't installed
// (addons.go, D15).
//
// Nothing the scanner prints is stored, logged or put in an error: a result is
// parsed into fixed fields (vulns.Parse), and a failure is one of a few classes
// worded here (constitution VII).

// The words of a failed or skipped scan, in Caravana's own (D5, D7).
const (
	scanReasonDatabase    = "the vulnerability database couldn't be downloaded or loaded"
	scanReasonDatabaseOld = "the vulnerability database is more than 5 days old"
	scanReasonRegistry    = "the registry refused the credentials"
	scanReasonImage       = "the image couldn't be read from its registry"
	scanReasonNoResult    = "the scanner stopped without a result"
	scanReasonNomad       = "Nomad refused the scan"
	scanReasonNomadDown   = "Nomad couldn't be reached"
	scanReasonStart       = "the scan couldn't be started"
	scanReasonTooLong     = "took longer than 20 minutes"
	scanReasonRestarted   = "Caravana restarted during the scan"
	scanReasonNoSBOM      = "the kept SBOM was missing, so the image is scanned again from its registry"
)

// scanDiskNeededMB is the free disk a scan needs on its server: the database is
// 3.5 GB, and about twice that for a moment while a new one replaces the old.
const scanDiskNeededMB = 4096

// scanSampleStale is how old a host sample may be and still say how much room a
// server has (D7).
const scanSampleStale = 5 * time.Minute

// defaultScanLimit is how long a scan may run before it is stopped.
const defaultScanLimit = 20 * time.Minute

// scanFreshFor is how long a result counts as fresh for a deploy (D8): a deploy
// that brings an image scanned within it doesn't queue another scan.
const scanFreshFor = 24 * time.Hour

// The wake keys of the scanner: scansKey says the queue or the add-on changed,
// scanJobsKey that a scan's Nomad job moved (see events.go).
const (
	scansKey    = "scans"
	scanJobsKey = "scan-jobs"
)

// scanState is the scanner's in-memory part. The timings are fields so tests
// can shorten them; they are read under mu, since Start's goroutines are already
// going when a test sets them.
type scanState struct {
	// addonMu serializes the add-on's install and remove, so two can't both pass
	// the state check.
	addonMu sync.Mutex

	mu      sync.Mutex
	limit   time.Duration // how long a scan may run
	poll    time.Duration // the longest between two reads of the scan's job
	idle    time.Duration // the longest the runner waits for work
	current *runningScan  // the scan the runner is executing
	// removing is true while the add-on is being removed: no scan starts.
	removing bool
	// unpurged holds the jobs whose purge failed; the runner tries again.
	unpurged  map[string]bool
	lastDaily time.Time // when the runner last looked at the daily pass (wall time)
}

// runningScan is the scan being executed, so a removal can stop it.
type runningScan struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{} // closed when its execution is over
}

func newScanState() scanState {
	return scanState{limit: defaultScanLimit, poll: 5 * time.Second, idle: 30 * time.Second, unpurged: map[string]bool{}}
}

func (s *Service) scanTimings() (limit, poll, idle time.Duration) {
	s.scan.mu.Lock()
	defer s.scan.mu.Unlock()
	return s.scan.limit, s.scan.poll, s.scan.idle
}

// scansChanged tells open pages the scanner's state or its queue moved.
func (s *Service) scansChanged() {
	s.wakes.wake(scansKey)
	s.publish(Change{What: ChangeScans})
}

// scanUse is one service of an app's live deployment and the image it runs.
type scanUse struct {
	App, Service, Image string
	Stopped             bool
}

// scannable says the image is pinned by digest, which is all that is scanned.
func scannable(image string) bool { return strings.Contains(image, "@sha256:") }

// liveUses lists every service of every app's live deployment (the newest
// healthy one) with its image, apps and services in order. An app that never
// had a healthy deployment, or has no definition, has none.
func (s *Service) liveUses(ctx context.Context) ([]scanUse, error) {
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	var out []scanUse
	for _, rec := range recs {
		dep, err := s.store.LatestHealthyDeployment(ctx, rec.App.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(dep.Deployment.Digests))
		for name := range dep.Deployment.Digests {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			if image := dep.Deployment.Digests[name]; scannable(image) {
				out = append(out, scanUse{App: rec.App.ID, Service: name, Image: image, Stopped: rec.StoppedAt != nil})
			}
		}
	}
	return out, nil
}

// distinctImages lists the images of uses, each once, in order of first use.
func distinctImages(uses []scanUse) []string {
	var out []string
	for _, u := range uses {
		if !slices.Contains(out, u.Image) {
			out = append(out, u.Image)
		}
	}
	return out
}

// queueScan queues a scan of image for trigger, unless one is already queued or
// running for it: then it returns that run. It refuses with ErrScannerNotInstalled
// while the add-on isn't installed.
func (s *Service) queueScan(ctx context.Context, image, trigger string) (store.ScanRun, error) {
	installed, err := s.scannerInstalled(ctx)
	if err != nil {
		return store.ScanRun{}, err
	}
	if !installed {
		return store.ScanRun{}, ErrScannerNotInstalled
	}
	return s.queueRun(ctx, image, trigger, store.ScanSourceRegistry)
}

// queueRun inserts the run without asking whether the add-on is installed: the
// install's own database run is queued while it is still installing.
func (s *Service) queueRun(ctx context.Context, image, trigger, source string) (store.ScanRun, error) {
	id, err := newID("")
	if err != nil {
		return store.ScanRun{}, err
	}
	// The row's creation time is the store's clock, which orders the queue.
	run, created, err := s.store.QueueScanRun(ctx, store.ScanRun{ID: id, Image: image, Trigger: trigger, Source: source})
	if err != nil {
		return store.ScanRun{}, err
	}
	if created {
		s.scansChanged()
	}
	return run, nil
}

// ScanWait returns the run once it has ended (ok, failed or skipped), waiting
// for it as long as ctx lives. A run that doesn't exist is store.ErrNotFound.
// The deploy gate waits on a scan with it.
func (s *Service) ScanWait(ctx context.Context, runID string) (store.ScanRun, error) {
	for {
		woke := s.wakes.channel(scansKey) // taken before the read, so a wake during it isn't lost
		run, err := s.store.GetScanRun(ctx, runID)
		if err != nil {
			return store.ScanRun{}, err
		}
		if run.Status != store.ScanQueued && run.Status != store.ScanRunning {
			return run, nil
		}
		select {
		case <-woke:
		case <-time.After(time.Second):
		case <-ctx.Done():
			return store.ScanRun{}, ctx.Err()
		}
	}
}

// runScanner is the scanner's goroutine: it runs the queue one scan at a time,
// and once a minute checks whether the daily pass is due. It sleeps when the
// queue is empty and wakes when something is queued.
func (s *Service) runScanner(ctx context.Context) {
	for {
		woke := s.wakes.channel(scansKey)
		s.drainScans(ctx)
		s.checkDaily(ctx)
		_, _, idle := s.scanTimings()
		select {
		case <-ctx.Done():
			return
		case <-woke:
		case <-time.After(idle):
		}
	}
}

// drainScans runs queued scans until none is left, the add-on isn't installed (or
// installing, for its database run) or ctx ends.
func (s *Service) drainScans(ctx context.Context) {
	s.retryScanPurges(ctx)
	for ctx.Err() == nil {
		state, err := s.scannerState(ctx)
		if err != nil {
			s.log.Warn("read the image scanner's state", "error", err)
			return
		}
		if state != store.AddonInstalled && state != store.AddonInstalling {
			return
		}
		run, err := s.store.NextQueuedScanRun(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			s.log.Warn("read the scan queue", "error", err)
			return
		}
		if state == store.AddonInstalling && run.Source != store.ScanSourceDatabase {
			return // only the install's own run goes before the install ends
		}
		if !s.executeScan(ctx, run) {
			return
		}
	}
}

// executeScan runs one scan to its end and records it. It returns false when
// the runner should stop draining (shutdown, or the store failed).
func (s *Service) executeScan(ctx context.Context, run store.ScanRun) bool {
	runCtx, cancel := context.WithCancel(ctx)
	cur := &runningScan{id: run.ID, cancel: cancel, done: make(chan struct{})}
	s.scan.mu.Lock()
	if s.scan.removing {
		s.scan.mu.Unlock()
		cancel()
		return false
	}
	s.scan.current = cur
	s.scan.mu.Unlock()
	defer func() {
		cancel()
		s.scan.mu.Lock()
		s.scan.current = nil
		s.scan.mu.Unlock()
		close(cur.done)
	}()

	bg := context.WithoutCancel(ctx)
	isDB := run.Source == store.ScanSourceDatabase
	now := func() time.Time { return s.clock.Now().UTC() }

	// Where the scan reads from: a daily rescan of an image whose SBOM is kept
	// reads the SBOM (no pull, no credentials, D17).
	source := run.Source
	if !isDB {
		source = store.ScanSourceRegistry
		if prior, err := s.store.GetImageScan(runCtx, run.Image); err == nil && run.Trigger == store.ScanTriggerDaily && prior.SBOMKept {
			source = store.ScanSourceSBOM
		}
	}

	// Room (D7): a scan that would starve the server is skipped, and says so.
	if !isDB {
		if reason := s.scanRoom(runCtx); reason != "" {
			if err := s.store.EndScanRun(bg, run.ID, store.ScanSkipped, reason, "", time.Time{}, now()); err != nil {
				s.log.Warn("record a skipped scan", "error", err)
				return false
			}
			s.recordTry(bg, run.Image, store.ScanSkipped, reason)
			s.scanDone(bg, run.Image)
			return true
		}
	}
	if err := s.store.StartScanRun(runCtx, run.ID, source, now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true // failed by a removal while it waited
		}
		if runCtx.Err() == nil {
			s.log.Warn("start a scan", "error", err)
		}
		return false
	}
	s.scansChanged()

	jobID := nomadjob.ScanJobID(run.ID)
	reason, result, class := s.scanJobRun(runCtx, run, source, jobID)

	// The scan is stopped by a removal or by shutdown: leave it to them.
	if runCtx.Err() != nil {
		if ctx.Err() == nil { // not shutdown: a removal
			s.abandonScan(bg, jobID)
		}
		return ctx.Err() == nil
	}
	s.purgeScan(bg, jobID)

	// A rescan from a kept SBOM that isn't there is made again from the registry.
	if reason != "" && source == store.ScanSourceSBOM && class == deploy.ScanSBOMMissing {
		if err := s.store.EndScanRun(bg, run.ID, store.ScanFailed, scanReasonNoSBOM, "", time.Time{}, now()); err != nil {
			s.log.Warn("settle a scan", "error", err)
			return false
		}
		if err := s.store.ClearSBOMKept(bg, run.Image); err != nil {
			s.log.Warn("clear a kept SBOM", "error", err)
		}
		if _, err := s.queueRun(bg, run.Image, run.Trigger, store.ScanSourceRegistry); err != nil {
			s.log.Warn("queue the scan again from the registry", "error", err)
		}
		s.scanDone(bg, run.Image)
		return true
	}

	if reason != "" {
		if err := s.store.EndScanRun(bg, run.ID, store.ScanFailed, reason, "", time.Time{}, now()); err != nil {
			s.log.Warn("settle a scan", "error", err)
			return false
		}
		s.recordTry(bg, run.Image, store.ScanFailed, reason)
		s.scanDone(bg, run.Image)
		return true
	}
	if isDB {
		if err := s.store.EndScanRun(bg, run.ID, store.ScanOK, "", "", time.Time{}, now()); err != nil {
			s.log.Warn("settle a scan", "error", err)
			return false
		}
		s.scanDone(bg, run.Image)
		return true
	}
	// A scan of the registry wrote the image's SBOM beside its findings.
	if err := s.store.SaveScanResult(bg, run.Image, result, true, now()); err != nil {
		s.log.Warn("store a scan's result", "error", err)
		_ = s.store.EndScanRun(bg, run.ID, store.ScanFailed, scanReasonNoResult, "", time.Time{}, now())
		return false
	}
	if err := s.store.EndScanRun(bg, run.ID, store.ScanOK, "", result.Scanner, result.DBBuilt, now()); err != nil {
		s.log.Warn("settle a scan", "error", err)
		return false
	}
	s.scanDone(bg, run.Image)
	return true
}

// recordTry records a failed or skipped try on the image's row, when the run is
// of an image (the install's database run isn't).
func (s *Service) recordTry(ctx context.Context, image, status, reason string) {
	if image == "" {
		return
	}
	if err := s.store.RecordScanTry(ctx, image, status, reason, s.clock.Now().UTC()); err != nil {
		s.log.Warn("record a scan's outcome", "error", err)
	}
}

// scanDone tells the pages and the waiters that a scan ended: the pages of every
// app that runs the image, and the lists.
func (s *Service) scanDone(ctx context.Context, image string) {
	s.wakes.wake(scansKey)
	s.publish(Change{What: ChangeScans})
	s.publish(Change{What: ChangeAttention})
	if image == "" {
		return
	}
	uses, err := s.liveUses(ctx)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, u := range uses {
		if u.Image == image && !seen[u.App] {
			seen[u.App] = true
			s.publish(Change{AppID: u.App, What: ChangeScans})
		}
	}
}

// scanJobRun starts the scan's job, waits for it and reads what it found. reason
// is empty when it succeeded: then result holds the findings (none for a
// database run). Otherwise reason is one of the words above and class the
// scanner's failure class, when it gave one. A cancelled runCtx returns early
// with no reason.
func (s *Service) scanJobRun(ctx context.Context, run store.ScanRun, source, jobID string) (reason string, result vulns.Result, class string) {
	isDB := source == store.ScanSourceDatabase
	set, err := s.store.GetScanSettings(ctx)
	if err != nil {
		return scanReasonStart, result, ""
	}
	nodeID, err := s.deployer.EnsureScanVolume(ctx)
	if err != nil {
		return nomadReason(err), result, ""
	}
	var private map[string]bool
	if source == store.ScanSourceRegistry {
		if private, err = s.privateRegistries(ctx); err != nil {
			return scanReasonStart, result, ""
		}
	}
	job := s.deployer.ScanJob(run.ID, nomadjob.ScanSource{Image: run.Image, FromSBOM: source == store.ScanSourceSBOM},
		nomadjob.ScanOptions{NodeID: nodeID, DatabaseURL: set.DatabaseURL, UpdateOnly: isDB}, private)
	if err := s.deployer.StartScan(ctx, job, private); err != nil {
		return nomadReason(err), result, ""
	}

	st, reason := s.awaitScan(ctx, jobID)
	if reason != "" || ctx.Err() != nil {
		return reason, result, ""
	}
	exited := st.ExitCode != nil && *st.ExitCode == 0 || st.ExitCode == nil && st.ClientStatus == "complete"
	if !exited || st.OOMKilled || st.StartFailed {
		if st.OOMKilled || st.StartFailed {
			return scanReasonNoResult, result, deploy.ScanOther
		}
		class = s.deployer.ScanFailure(ctx, st.AllocID)
		return scanFailureWords(class), result, class
	}
	if isDB {
		return "", result, ""
	}
	text, found, err := s.deployer.ScanResult(ctx, st.AllocID)
	if err != nil || !found {
		return scanReasonNoResult, result, ""
	}
	result, err = vulns.Parse(run.Image, text)
	if err != nil {
		// The error names a line of the scanner's output: not logged.
		return scanReasonNoResult, vulns.Result{}, ""
	}
	return "", result, ""
}

// awaitScan reads the job until it ends or the limit passes (then it is
// stopped). reason is set for a job that is gone or timed out; a cancelled ctx
// returns an empty reason and the caller looks at ctx.
func (s *Service) awaitScan(ctx context.Context, jobID string) (deploy.RunState, string) {
	limit, poll, _ := s.scanTimings()
	timeout := time.NewTimer(limit)
	defer timeout.Stop()
	for {
		woke := s.wakes.channel(scanJobsKey)
		st, err := s.deployer.ScanState(ctx, jobID)
		switch {
		case ctx.Err() != nil:
			return deploy.RunState{}, ""
		case errors.Is(err, deploy.ErrRunGone):
			return st, scanReasonNoResult
		case err == nil && st.Terminal:
			return st, ""
		}
		// Any other error is a Nomad that didn't answer this time: ask again.
		select {
		case <-ctx.Done():
			return deploy.RunState{}, ""
		case <-woke:
		case <-time.After(poll):
		case <-timeout.C:
			stop := context.WithoutCancel(ctx)
			if err := s.deployer.StopScan(stop, jobID); err != nil && !errors.Is(err, deploy.ErrRunGone) {
				s.log.Warn("stop a scan that ran too long", "error", err)
			}
			return st, scanReasonTooLong
		}
	}
}

// nomadReason is the word for Nomad failing to take a scan.
func nomadReason(err error) string {
	if errors.Is(err, deploy.ErrNomadUnavailable) {
		return scanReasonNomadDown
	}
	return scanReasonNomad
}

// scanFailureWords is the sentence for one of the scanner's failure classes.
func scanFailureWords(class string) string {
	switch class {
	case deploy.ScanDBDownload:
		return scanReasonDatabase
	case deploy.ScanDBOld:
		return scanReasonDatabaseOld
	case deploy.ScanRegistryAuth:
		return scanReasonRegistry
	case deploy.ScanImageRead:
		return scanReasonImage
	}
	return scanReasonNoResult
}

// purgeScan erases a scan's job and variable. A job that can't be erased is
// remembered and tried again by the runner.
func (s *Service) purgeScan(ctx context.Context, jobID string) {
	if err := s.deployer.PurgeScan(ctx, jobID); err != nil {
		s.log.Warn("purge a scan's job; it is tried again", "job", jobID, "error", err)
		s.scan.mu.Lock()
		s.scan.unpurged[jobID] = true
		s.scan.mu.Unlock()
	}
}

// abandonScan stops and erases the job of a scan a removal cancelled.
func (s *Service) abandonScan(ctx context.Context, jobID string) {
	if err := s.deployer.StopScan(ctx, jobID); err != nil && !errors.Is(err, deploy.ErrRunGone) {
		s.log.Warn("stop a scan that was cancelled", "job", jobID, "error", err)
	}
	s.purgeScan(ctx, jobID)
}

// retryScanPurges tries again the purges that failed.
func (s *Service) retryScanPurges(ctx context.Context) {
	s.scan.mu.Lock()
	jobs := make([]string, 0, len(s.scan.unpurged))
	for id := range s.scan.unpurged {
		jobs = append(jobs, id)
	}
	s.scan.mu.Unlock()
	for _, id := range jobs {
		if err := s.deployer.PurgeScan(ctx, id); err == nil {
			s.scan.mu.Lock()
			delete(s.scan.unpurged, id)
			s.scan.mu.Unlock()
		}
	}
}

// purgeLeftScanJobs purges the jobs of the scans a removal ended and any whose
// purge had failed, so the volume can be deleted.
func (s *Service) purgeLeftScanJobs(ctx context.Context) {
	s.retryScanPurges(ctx)
}

// stopCurrentScan cancels the scan being executed and waits, up to wait, for its
// execution to end (it stops and purges its job as it goes).
func (s *Service) stopCurrentScan(wait time.Duration) {
	s.scan.mu.Lock()
	cur := s.scan.current
	s.scan.mu.Unlock()
	if cur == nil {
		return
	}
	cur.cancel()
	select {
	case <-cur.done:
	case <-time.After(wait):
		s.log.Warn("a scan didn't stop in time for the removal of the image scanner")
	}
}

// recoverScans settles what a process that ended left: a scan that was running
// is failed and its job erased (its output died with the process), and an
// install or removal that was going is failed, so the owner can try again.
// Queued scans stay queued.
func (s *Service) recoverScans(ctx context.Context) error {
	now := s.clock.Now().UTC()
	row, err := s.store.GetAddon(ctx, store.AddonImageScanner)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return err
	case row.State == store.AddonInstalling || row.State == store.AddonRemoving:
		row.Reason = reasonInstallRestarted
		if row.State == store.AddonRemoving {
			row.Reason = reasonRemoveRestarted
		}
		row.State, row.UpdatedAt = store.AddonFailed, now
		if err := s.store.SaveAddon(ctx, row); err != nil {
			return fmt.Errorf("settle the image scanner's install: %w", err)
		}
	}
	runs, err := s.store.FailOpenScanRuns(ctx, false, true, scanReasonRestarted, now)
	if err != nil {
		return fmt.Errorf("settle the scans running at the last stop: %w", err)
	}
	for _, r := range runs {
		s.recordTry(ctx, r.Image, store.ScanFailed, scanReasonRestarted)
		s.purgeScan(ctx, nomadjob.ScanJobID(r.ID))
	}
	return nil
}

// scanAfterDeploy queues a scan of each image a deployment that has just ended
// healthy brought, unless that image was scanned within a day (D8). It runs in
// the watcher that recorded the outcome, never fails it, and does nothing while
// the add-on isn't installed.
func (s *Service) scanAfterDeploy(rec store.DeploymentRecord) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.background()), 10*time.Second)
	defer cancel()
	installed, err := s.scannerInstalled(ctx)
	if err != nil {
		s.log.Warn("after a deploy: read the image scanner's state", "deployment", rec.Deployment.ID, "error", err)
		return
	}
	if !installed {
		return
	}
	var queued []string
	for _, image := range rec.Deployment.Digests {
		if !scannable(image) || slices.Contains(queued, image) {
			continue
		}
		queued = append(queued, image)
		if prior, err := s.store.GetImageScan(ctx, image); err == nil && prior.HasResult() &&
			s.clock.Now().Sub(prior.ScannedAt) < scanFreshFor {
			continue
		}
		if _, err := s.queueScan(ctx, image, store.ScanTriggerDeploy); err != nil {
			s.log.Warn("after a deploy: queue a scan", "deployment", rec.Deployment.ID, "error", err)
		}
	}
}

// dailyDue says the daily pass should run: it is at or after the hour (UTC) and
// the pass hasn't run today.
func dailyDue(now time.Time, hour int, last time.Time) bool {
	now = now.UTC()
	if now.Hour() < hour {
		return false
	}
	if last.IsZero() {
		return true
	}
	ly, lm, ld := last.UTC().Date()
	ny, nm, nd := now.Date()
	return ly != ny || lm != nm || ld != nd
}

// checkDaily runs the daily pass when it is due. The runner calls it on every
// round; it looks at the store at most once a minute.
func (s *Service) checkDaily(ctx context.Context) {
	s.scan.mu.Lock()
	if time.Since(s.scan.lastDaily) < time.Minute {
		s.scan.mu.Unlock()
		return
	}
	s.scan.lastDaily = time.Now()
	s.scan.mu.Unlock()

	installed, err := s.scannerInstalled(ctx)
	if err != nil || !installed {
		return
	}
	set, err := s.store.GetScanSettings(ctx)
	if err != nil {
		s.log.Warn("daily scans: read the settings", "error", err)
		return
	}
	now := s.clock.Now().UTC()
	if !dailyDue(now, set.DailyHour, set.LastDaily) {
		return
	}
	if err := s.dailyPass(ctx, now); err != nil && ctx.Err() == nil {
		s.log.Warn("the daily image scans", "error", err)
	}
}

// RunDailyScansForTests runs the daily pass now, whatever the hour and the day
// of the last one: the lab stands the clock in for it. It needs the add-on
// installed.
func (s *Service) RunDailyScansForTests(ctx context.Context) error {
	installed, err := s.scannerInstalled(ctx)
	if err != nil {
		return err
	}
	if !installed {
		return ErrScannerNotInstalled
	}
	return s.dailyPass(ctx, s.clock.Now().UTC())
}

// DropFindingForTests deletes the stored findings of image for one advisory and
// returns how many it dropped. The lab uses it as the stand-in for "the advisory
// didn't exist when the image was scanned": the next scan of the unchanged digest
// finds it again, as a new finding. It is for the lab only; no route, page or
// command calls it.
func (s *Service) DropFindingForTests(ctx context.Context, image, advisory string) (int, error) {
	return s.store.DeleteImageFinding(ctx, image, advisory)
}

// dailyPass queues a rescan of every image an app that isn't stopped runs
// (an image with a kept SBOM is rescanned from it when the run starts), prunes
// what no app runs and writes the day.
func (s *Service) dailyPass(ctx context.Context, now time.Time) error {
	uses, err := s.liveUses(ctx)
	if err != nil {
		return err
	}
	var running []scanUse
	for _, u := range uses {
		if !u.Stopped {
			running = append(running, u)
		}
	}
	for _, image := range distinctImages(running) {
		if _, err := s.queueScan(ctx, image, store.ScanTriggerDaily); err != nil {
			return err
		}
	}
	s.pruneScans(ctx)
	return s.store.SetLastDaily(ctx, now)
}

// RequestScan queues a scan of the images an app runs now (the owner's "Scan
// now"), or of every app's when appID is empty, and returns the runs (the ones
// already queued or running are returned as they are). It reads the registry
// again, not a kept SBOM. ErrScannerNotInstalled while the add-on isn't
// installed; an app that doesn't exist is store.ErrNotFound and one with no live
// deployment ErrNoLiveDeployment.
func (s *Service) RequestScan(ctx context.Context, appID string) ([]store.ScanRun, error) {
	installed, err := s.scannerInstalled(ctx)
	if err != nil {
		return nil, err
	}
	if !installed {
		return nil, ErrScannerNotInstalled
	}
	if appID != "" {
		if _, err := s.store.GetApp(ctx, appID); err != nil {
			return nil, err
		}
	}
	uses, err := s.liveUses(ctx)
	if err != nil {
		return nil, err
	}
	var mine []scanUse
	for _, u := range uses {
		if appID == "" || u.App == appID {
			mine = append(mine, u)
		}
	}
	if appID != "" && len(mine) == 0 {
		return nil, fmt.Errorf("app %q: %w", appID, ErrNoLiveDeployment)
	}
	var runs []store.ScanRun
	for _, image := range distinctImages(mine) {
		run, err := s.queueScan(ctx, image, store.ScanTriggerOwner)
		if err != nil {
			return runs, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// scanKeep lists the images whose stored scans stay (D10): those of every app's
// live deployment (a stopped app's too), of deployments still pending or running,
// and of scans queued or running.
func (s *Service) scanKeep(ctx context.Context) ([]string, error) {
	uses, err := s.liveUses(ctx)
	if err != nil {
		return nil, err
	}
	keep := distinctImages(uses)
	unfinished, err := s.store.ListUnfinishedDeployments(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range unfinished {
		for _, image := range d.Deployment.Digests {
			if !slices.Contains(keep, image) {
				keep = append(keep, image)
			}
		}
	}
	open, err := s.store.OpenScanRuns(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range open {
		if r.Image != "" && !slices.Contains(keep, r.Image) {
			keep = append(keep, r.Image)
		}
	}
	return keep, nil
}

// pruneScans deletes the stored scans of images nothing runs. It is best-effort:
// a failure is logged, and the next daily pass tries again. It does nothing while
// the add-on isn't installed.
func (s *Service) pruneScans(ctx context.Context) {
	if installed, err := s.scannerInstalled(ctx); err != nil || !installed {
		return
	}
	keep, err := s.scanKeep(ctx)
	if err == nil {
		_, err = s.store.PruneImageScans(ctx, keep)
	}
	if err != nil && ctx.Err() == nil {
		s.log.Warn("prune the stored image scans", "error", err)
	}
}
