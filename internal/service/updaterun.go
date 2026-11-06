package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Updating Caravana from the console (feature 030). The console never gets root. It records a
// request, a row in SQLite and a small file in its own data directory that holds an ID and a
// version; a root timer on the host (the installer's caravana-update) picks the file up, checks
// it, and runs that release's checksum-verified installer, which upgrades with rollback. The
// updater writes its status in its own root-owned directory, which Caravana can read and not
// write. This file is the Caravana side of contracts/updater.md: the request, the status parser,
// the watcher that follows a run across Caravana's own restart, and the automatic updates.

// The timing of the updater's run, from the contract (research R7).
const (
	// updateStartDeadline is how long a request may wait for the updater's first status.
	updateStartDeadline = 5 * time.Minute
	// updateRunDeadline is how long a run may stay "running" (the updater's own service
	// timeout is 30 minutes).
	updateRunDeadline = 45 * time.Minute
	// defaultUpdateWatchGap is how often the watcher reads the updater's status.
	defaultUpdateWatchGap = 5 * time.Second
	// updateStatusLimit is the most that is read of the status file.
	updateStatusLimit = 4 << 10
	// updateReasonLimit is the longest reason that is kept, in bytes.
	updateReasonLimit = 300
	// updateRecent is how long after it finished a run is still the one the page shows.
	updateRecent = 24 * time.Hour
)

// The sentences of an update's refusals and of a run that was never heard from, the page's words.
const (
	reasonLostStart = "The server's updater didn't start it within 5 minutes. Is caravana-update.timer running? (systemctl status caravana-update.timer)"
	reasonLostEnd   = "The updater stopped without saying how it went."

	reasonAutoNeedsCheck   = "It needs the release check: turn on Check GitHub for new releases."
	reasonAutoNeedsUpdater = "It needs the server's updater: run the installer once on the server."
	reasonAutoDevBuild     = "This is a development build."
)

// The refusals of RequestUpdate. A caller tests them with errors.Is.
var (
	// ErrUpdateUnavailable: a development build, or the updater isn't installed. The error is
	// an *UpdateUnavailableError with the sentence to show.
	ErrUpdateUnavailable = errors.New("an update can't be started here")
	// ErrUpdateRunning: a run is requested or running. The error is an *UpdateRunningError.
	ErrUpdateRunning = errors.New("an update is already under way")
	// ErrUpdateNotOffered: the version isn't the newest release Caravana has seen, or isn't
	// newer than the running one. The error is an *UpdateNotOfferedError with the sentence.
	ErrUpdateNotOffered = errors.New("that version isn't offered")
)

// UpdateUnavailableError is ErrUpdateUnavailable with the reason in Caravana's words, ready to
// show.
type UpdateUnavailableError struct{ Reason string }

func (e *UpdateUnavailableError) Error() string { return e.Reason }

// Is makes errors.Is(err, ErrUpdateUnavailable) true.
func (e *UpdateUnavailableError) Is(target error) bool { return target == ErrUpdateUnavailable }

// UpdateRunningError is ErrUpdateRunning with the version of the run that is open.
type UpdateRunningError struct{ Version string }

func (e *UpdateRunningError) Error() string {
	return "An update to " + e.Version + " is already under way."
}

// Is makes errors.Is(err, ErrUpdateRunning) true.
func (e *UpdateRunningError) Is(target error) bool { return target == ErrUpdateRunning }

// UpdateNotOfferedError is ErrUpdateNotOffered with the reason in Caravana's words.
type UpdateNotOfferedError struct{ Reason string }

func (e *UpdateNotOfferedError) Error() string { return e.Reason }

// Is makes errors.Is(err, ErrUpdateNotOffered) true.
func (e *UpdateNotOfferedError) Is(target error) bool { return target == ErrUpdateNotOffered }

// updaterState is what the update runs keep between calls.
type updaterState struct {
	// mu makes "is a run open?" and "insert the run" one step, and guards the settings below.
	mu          sync.Mutex
	dir         string        // the updater's directory; empty means no updater is known
	requestPath string        // where the request file is written; empty means none can be
	gap         time.Duration // between two reads of the updater's status
	lastAuto    time.Time     // the minute the automatic check last ran for
}

// SetUpdaterDir sets the updater's directory (CARAVANA_UPDATER_DIR,
// /var/lib/caravana-update on a host). Caravana only reads it. Empty means no updater is known,
// which is what tests and a development run without the installer get. Call it before Start.
func (s *Service) SetUpdaterDir(dir string) {
	s.updater.mu.Lock()
	defer s.updater.mu.Unlock()
	s.updater.dir = dir
}

// SetUpdateRequestPath sets where the request file is written: <data dir>/update-request, the
// only place Caravana can write that root reads. Call it before Start.
func (s *Service) SetUpdateRequestPath(path string) {
	s.updater.mu.Lock()
	defer s.updater.mu.Unlock()
	s.updater.requestPath = path
}

// SetUpdateWatchGap sets how often the watcher reads the updater's status (5 s by default). Call
// it before Start; a value that isn't positive keeps the default.
func (s *Service) SetUpdateWatchGap(d time.Duration) {
	if d > 0 {
		s.updater.gap = d
	}
}

// updaterPaths returns the updater's directory and the request path as set.
func (s *Service) updaterPaths() (dir, request string) {
	s.updater.mu.Lock()
	defer s.updater.mu.Unlock()
	return s.updater.dir, s.updater.requestPath
}

// UpdaterInstalled says the installer's marker is there: a regular file `updater` in the updater's
// directory. Without it a request would sit unread.
func (s *Service) UpdaterInstalled() bool {
	dir, request := s.updaterPaths()
	if dir == "" || request == "" {
		return false
	}
	fi, err := os.Lstat(filepath.Join(dir, "updater"))
	return err == nil && fi.Mode().IsRegular()
}

// releaseShaped is the pattern of a version in a request: what tools/release.sh accepts and the
// updater checks again before it uses it.
var releaseShaped = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// newUpdateRunID is 16 random bytes as 32 lowercase hex characters, the request's ID.
func newUpdateRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make an update run ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// RequestUpdate records an update of Caravana to version and writes the request file the server's
// updater looks for. trigger is store.UpdateByConsole, UpdateByAPI (tokenName is the API token's
// name) or UpdateByAutomatic. It returns the new run, in the state requested. A leading "v" is
// dropped.
//
// It refuses (see the typed errors) a version that isn't X.Y.Z[-suffix] (an *app.FieldError on
// `version`), a development build or a host without the updater (ErrUpdateUnavailable), a second
// request while one run is open (ErrUpdateRunning), and a version that isn't newer than the running
// one or isn't the newest release the check has seen (ErrUpdateNotOffered). The row is written
// first and the file second; when the file can't be written the row is removed again and the error
// is returned.
func (s *Service) RequestUpdate(ctx context.Context, version, trigger, tokenName string) (store.UpdateRun, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !releaseShaped.MatchString(version) {
		return store.UpdateRun{}, app.NewFieldError("version", "must be a release version such as 0.17.0")
	}
	switch trigger {
	case store.UpdateByConsole, store.UpdateByAPI, store.UpdateByAutomatic:
	default:
		return store.UpdateRun{}, fmt.Errorf("request an update: unknown trigger %q", trigger)
	}
	current := strings.TrimPrefix(s.runningVersion, "v")

	s.updater.mu.Lock()
	defer s.updater.mu.Unlock()

	rc, err := s.store.ReleaseCheck(ctx)
	if err != nil {
		return store.UpdateRun{}, err
	}
	if !hostos.IsReleaseVersion(current) {
		return store.UpdateRun{}, &UpdateUnavailableError{reasonDevBuild}
	}
	if !s.updaterInstalledLocked() {
		return store.UpdateRun{}, &UpdateUnavailableError{updaterMissing(rc.LatestTag)}
	}
	open, ok, err := s.store.OpenUpdateRun(ctx)
	if err != nil {
		return store.UpdateRun{}, err
	}
	if ok {
		return store.UpdateRun{}, &UpdateRunningError{Version: open.Version}
	}
	switch latest := strings.TrimPrefix(rc.LatestTag, "v"); {
	case version == current:
		return store.UpdateRun{}, &UpdateNotOfferedError{"caravana " + current + " already runs here."}
	case !hostos.ReleaseNewer(version, current):
		return store.UpdateRun{}, &UpdateNotOfferedError{"caravana " + current + " runs here, and " + version + " isn't newer."}
	case latest == "":
		return store.UpdateRun{}, &UpdateNotOfferedError{"caravana hasn't seen a release yet. Check for releases first."}
	case version != latest:
		return store.UpdateRun{}, &UpdateNotOfferedError{version + " isn't the newest release caravana has seen (" + latest + "). Check for releases first."}
	}

	id, err := newUpdateRunID()
	if err != nil {
		return store.UpdateRun{}, err
	}
	run, err := s.store.InsertUpdateRun(ctx, store.UpdateRun{
		ID: id, Version: version, FromVersion: current, Trigger: trigger, TokenName: tokenName,
		RequestedAt: s.clock.Now().UTC(), State: store.UpdateRequested,
	})
	if err != nil {
		return store.UpdateRun{}, err
	}
	if err := writeUpdateRequest(s.updater.requestPath, id, version); err != nil {
		// The file is what makes the run happen: without it the row would only wait for the
		// 5-minute timeout, so it goes.
		if derr := s.store.DeleteUpdateRun(context.WithoutCancel(ctx), id); derr != nil {
			s.log.Warn("remove an update run whose request file couldn't be written", "run", id, "error", derr)
		}
		return store.UpdateRun{}, err
	}
	s.publish(Change{What: ChangeUpdates})
	return run, nil
}

// updaterInstalledLocked is UpdaterInstalled for a caller that holds updater.mu.
func (s *Service) updaterInstalledLocked() bool {
	if s.updater.dir == "" || s.updater.requestPath == "" {
		return false
	}
	fi, err := os.Lstat(filepath.Join(s.updater.dir, "updater"))
	return err == nil && fi.Mode().IsRegular()
}

// updaterMissing is the sentence for a host without the updater. latest is the newest release
// seen, with or without its v.
func updaterMissing(latest string) string {
	what := "the newest release"
	if l := strings.TrimPrefix(latest, "v"); l != "" {
		what = l
	}
	return "This server's updater isn't installed. Run the installer of " + what +
		" once on the server; it adds the updater, and later updates can start here."
}

// writeUpdateRequest writes the request file atomically with mode 0600: a temporary file beside
// it, then a rename. The content is exactly two lines.
func writeUpdateRequest(path, id, version string) error {
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("write the update request: %w", err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write the update request: %w", err)
	}
	_, err = fmt.Fprintf(f, "ID=%s\nVERSION=%s\n", id, version)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write the update request: %w", err)
	}
	return nil
}

// --- the updater's status ---------------------------------------------------------------------

// updaterStatus is what the updater's `status` file says (contracts/updater.md).
type updaterStatus struct {
	ID, Version, From string
	// State is running, updated, rolled_back, failed or refused; empty when the file named
	// another state.
	State             string
	Started, Finished time.Time // zero when missing or malformed
	// Reason is one line of printable ASCII, at most 300 bytes.
	Reason string
}

// parseUpdaterStatus reads KEY=value lines, as text. An unknown key is ignored, a malformed time
// leaves the time zero, a state outside the five is left empty and the reason is cut to printable
// ASCII and 300 bytes. It never fails: a file that says nothing gives an empty status.
func parseUpdaterStatus(data []byte) updaterStatus {
	var st updaterStatus
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ID":
			st.ID = value
		case "VERSION":
			st.Version = value
		case "FROM":
			st.From = value
		case "STATE":
			switch value {
			case store.UpdateRunning, store.UpdateUpdated, store.UpdateRolledBack, store.UpdateFailed, store.UpdateRefused:
				st.State = value
			}
		case "STARTED":
			st.Started = parseStatusTime(value)
		case "FINISHED":
			st.Finished = parseStatusTime(value)
		case "REASON":
			st.Reason = cleanReason(value)
		}
	}
	return st
}

func parseStatusTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// cleanReason keeps the printable ASCII of v and cuts it to 300 bytes: it is shown on a page and
// sent in a message, so it is made safe here whatever the file held.
func cleanReason(v string) string {
	var b strings.Builder
	for i := 0; i < len(v) && b.Len() < updateReasonLimit; i++ {
		if c := v[i]; c >= 0x20 && c <= 0x7e {
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

// readUpdaterStatus reads <dir>/status: a regular file of at most 4 KiB. ok is false when there
// is none, when it isn't a plain file, when it is larger than the updater ever writes or when it
// can't be read.
func readUpdaterStatus(dir string) (updaterStatus, bool) {
	if dir == "" {
		return updaterStatus{}, false
	}
	path := filepath.Join(dir, "status")
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return updaterStatus{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return updaterStatus{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, updateStatusLimit+1))
	if err != nil || len(data) > updateStatusLimit {
		return updaterStatus{}, false
	}
	return parseUpdaterStatus(data), true
}

// --- the watcher -------------------------------------------------------------------------------

// watchUpdates follows the open update run: once at start (a run the last process left is picked
// up again, which is how an update is followed across Caravana's own restart) and then every gap.
// Each pass also looks, once a minute, for an automatic update that is due.
func (s *Service) watchUpdates(ctx context.Context, gap time.Duration) {
	for {
		s.updatePass(ctx)
		t := time.NewTimer(gap)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// updatePass is one pass of the watcher: settle the open run from the updater's status, then, once
// a minute, the automatic check.
func (s *Service) updatePass(ctx context.Context) {
	if err := s.followUpdateRun(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("follow the update run", "error", err)
	}
	now := s.clock.Now().UTC()
	s.updater.mu.Lock()
	minute := now.Truncate(time.Minute)
	due := !minute.Equal(s.updater.lastAuto)
	s.updater.lastAuto = minute
	s.updater.mu.Unlock()
	if due {
		if err := s.autoUpdate(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("automatic update", "error", err)
		}
	}
}

// followUpdateRun reads the open run and the updater's status and records what changed: running,
// a final state (the request file is removed and the page and the needs-you list are told) or the
// two "lost" cases (nothing started within 5 minutes; nothing finished within 45).
func (s *Service) followUpdateRun(ctx context.Context) error {
	run, ok, err := s.store.OpenUpdateRun(ctx)
	if err != nil || !ok {
		return err
	}
	dir, request := s.updaterPaths()
	now := s.clock.Now().UTC()
	st, haveStatus := readUpdaterStatus(dir)
	if haveStatus && st.ID != run.ID {
		haveStatus = false // an older run's status
	}

	switch {
	case haveStatus && st.State != "" && st.State != store.UpdateRunning:
		started := st.Started
		if started.IsZero() {
			started = run.StartedAt
		}
		finished := st.Finished
		if finished.IsZero() {
			finished = now
		}
		logPath := ""
		if st.State != store.UpdateRefused && !started.IsZero() {
			logPath = filepath.Join(dir, "logs", run.ID+".log")
		}
		return s.settleUpdateRun(ctx, run, st.State, started, finished, st.Reason, logPath, request)

	case haveStatus && st.State == store.UpdateRunning:
		started := st.Started
		if started.IsZero() {
			started = now
		}
		if run.State != store.UpdateRunning {
			if err := s.store.SetUpdateRunState(ctx, run.ID, store.UpdateRunning, started, time.Time{}, "", ""); err != nil {
				return err
			}
			s.publish(Change{What: ChangeUpdates})
			run.State, run.StartedAt = store.UpdateRunning, started
		}
		if now.Sub(started) >= updateRunDeadline {
			return s.settleUpdateRun(ctx, run, store.UpdateLost, started, now, reasonLostEnd,
				filepath.Join(dir, "logs", run.ID+".log"), request)
		}

	case run.State == store.UpdateRunning && !run.StartedAt.IsZero():
		// Seen running and the status has gone (the file is gone, or another run's has replaced
		// it): the 45 minutes count from the start.
		if now.Sub(run.StartedAt) >= updateRunDeadline {
			return s.settleUpdateRun(ctx, run, store.UpdateLost, run.StartedAt, now, reasonLostEnd,
				filepath.Join(dir, "logs", run.ID+".log"), request)
		}

	case run.State == store.UpdateRequested:
		if now.Sub(run.RequestedAt) >= updateStartDeadline {
			return s.settleUpdateRun(ctx, run, store.UpdateLost, time.Time{}, now, reasonLostStart, "", request)
		}
	}
	return nil
}

// settleUpdateRun records a run's final state, removes the request file, and tells the pages and
// the needs-you list. A store error is returned and the next pass tries again; the file is only
// removed once the record is written, so nothing is lost on the way.
func (s *Service) settleUpdateRun(ctx context.Context, run store.UpdateRun, state string, started, finished time.Time,
	reason, logPath, requestPath string) error {
	if err := s.store.SetUpdateRunState(ctx, run.ID, state, started, finished, reason, logPath); err != nil {
		return err
	}
	if requestPath != "" {
		if err := os.Remove(requestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("remove the update request file", "error", err)
		}
	}
	s.publish(Change{What: ChangeUpdates})
	s.publish(Change{What: ChangeAttention})
	return nil
}

// --- automatic updates --------------------------------------------------------------------------

// autoUpdateInput is what autoUpdateDue needs to know.
type autoUpdateInput struct {
	Now          time.Time // read as UTC
	Enabled      bool      // the owner's setting
	Hour         int       // the UTC hour
	CheckEnabled bool      // the release check is on
	Running      string    // the running version
	Latest       string    // the newest release the check has seen, with or without its v
	Installed    bool      // the updater is installed
	Open         bool      // a run is requested or running
	Ran          bool      // a run was ever made for Latest, in any state
}

// plainVersion matches an X.Y.Z with no suffix and captures its major.minor.
var plainVersion = regexp.MustCompile(`^([0-9]+\.[0-9]+)\.[0-9]+$`)

// isPatchOf says latest is a patch release of running: the same major and minor, a newer patch, and
// no suffix on either.
func isPatchOf(latest, running string) bool {
	l, r := plainVersion.FindStringSubmatch(latest), plainVersion.FindStringSubmatch(running)
	return l != nil && r != nil && l[1] == r[1] && hostos.ReleaseNewer(latest, running)
}

// autoStanding is autoUpdateDue without the hour: every other condition holds. It returns
// the version that would be requested.
func autoStanding(in autoUpdateInput) (version string, ok bool) {
	latest := strings.TrimPrefix(in.Latest, "v")
	running := strings.TrimPrefix(in.Running, "v")
	if !in.Enabled || !in.CheckEnabled || !in.Installed || in.Open || in.Ran ||
		!hostos.IsReleaseVersion(running) || !isPatchOf(latest, running) {
		return "", false
	}
	return latest, true
}

// autoUpdateDue says whether the automatic request is due now (FR-008): the setting is on, the
// clock is in the set UTC hour, the release check is on, the updater is installed, no run is open,
// no run was ever made for that version, and the newest release is a patch release of the running
// one. It returns the version to request.
func autoUpdateDue(in autoUpdateInput) (version string, ok bool) {
	if in.Now.UTC().Hour() != in.Hour {
		return "", false
	}
	return autoStanding(in)
}

// autoUpdateNext is when the next automatic request is made: now while the clock is in the set
// hour, else the next time it reaches that hour. ok is false when none is due (autoStanding).
func autoUpdateNext(in autoUpdateInput) (at time.Time, ok bool) {
	if _, ok := autoStanding(in); !ok {
		return time.Time{}, false
	}
	now := in.Now.UTC()
	if now.Hour() == in.Hour {
		return now, true
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), in.Hour, 0, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next, true
}

// autoInput reads what autoUpdateDue needs from the store and the service.
func (s *Service) autoInput(ctx context.Context, rc store.ReleaseCheck, now time.Time) (autoUpdateInput, error) {
	in := autoUpdateInput{
		Now: now, Enabled: rc.AutoUpdate, Hour: rc.AutoUpdateHour, CheckEnabled: rc.Enabled,
		Running: s.runningVersion, Latest: rc.LatestTag, Installed: s.UpdaterInstalled(),
	}
	_, open, err := s.store.OpenUpdateRun(ctx)
	if err != nil {
		return in, err
	}
	in.Open = open
	if latest := strings.TrimPrefix(rc.LatestTag, "v"); latest != "" {
		ran, err := s.store.UpdateRunExists(ctx, latest)
		if err != nil {
			return in, err
		}
		in.Ran = ran
	}
	return in, nil
}

// autoUpdate makes the automatic request when it is due. A refusal that only says "not now" (a run
// opened between the check and the request) is not an error.
func (s *Service) autoUpdate(ctx context.Context) error {
	rc, err := s.store.ReleaseCheck(ctx)
	if err != nil || !rc.AutoUpdate {
		return err
	}
	in, err := s.autoInput(ctx, rc, s.clock.Now().UTC())
	if err != nil {
		return err
	}
	version, ok := autoUpdateDue(in)
	if !ok {
		return nil
	}
	_, err = s.RequestUpdate(ctx, version, store.UpdateByAutomatic, "")
	switch {
	case err == nil:
		s.log.Info("automatic update requested", "version", version)
		return nil
	case errors.Is(err, ErrUpdateRunning), errors.Is(err, ErrUpdateNotOffered), errors.Is(err, ErrUpdateUnavailable):
		return nil
	}
	return err
}

// SetAutoUpdate stores the owner's automatic-update setting: whether Caravana asks for patch
// releases by itself, and the UTC hour (0-23) it does. An hour outside 0-23 is an *app.FieldError
// on `autoUpdateHour`.
func (s *Service) SetAutoUpdate(ctx context.Context, enabled bool, hour int) error {
	if hour < 0 || hour > 23 {
		return app.NewFieldError("autoUpdateHour", "must be an hour from 0 to 23")
	}
	if err := s.store.SetAutoUpdate(ctx, enabled, hour); err != nil {
		return err
	}
	s.publish(Change{What: ChangeUpdates})
	return nil
}

// UpdateSettings is what the owner can change about updates; a nil field keeps its value.
type UpdateSettings struct {
	CheckEnabled   *bool // ask GitHub for new releases (the release check)
	AutoUpdate     *bool // install patch releases by themselves
	AutoUpdateHour *int  // the UTC hour, 0-23
}

// PutUpdateSettings stores the settings that are set. An hour outside 0-23 is an *app.FieldError on
// `autoUpdateHour` and nothing is stored.
func (s *Service) PutUpdateSettings(ctx context.Context, in UpdateSettings) error {
	if in.AutoUpdateHour != nil && (*in.AutoUpdateHour < 0 || *in.AutoUpdateHour > 23) {
		return app.NewFieldError("autoUpdateHour", "must be an hour from 0 to 23")
	}
	if in.CheckEnabled != nil {
		if err := s.SetReleaseCheck(ctx, *in.CheckEnabled); err != nil {
			return err
		}
	}
	if in.AutoUpdate != nil || in.AutoUpdateHour != nil {
		rc, err := s.store.ReleaseCheck(ctx)
		if err != nil {
			return err
		}
		enabled, hour := rc.AutoUpdate, rc.AutoUpdateHour
		if in.AutoUpdate != nil {
			enabled = *in.AutoUpdate
		}
		if in.AutoUpdateHour != nil {
			hour = *in.AutoUpdateHour
		}
		return s.SetAutoUpdate(ctx, enabled, hour)
	}
	return nil
}

// ListUpdateRuns returns the update runs, newest first, at most limit (20 when limit is under 1).
func (s *Service) ListUpdateRuns(ctx context.Context, limit int) ([]store.UpdateRun, error) {
	if limit < 1 {
		limit = 20
	}
	return s.store.UpdateRuns(ctx, limit)
}
