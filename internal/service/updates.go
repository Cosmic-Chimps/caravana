package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Updates (feature 027 page 3): what is waiting for Caravana and for the
// server's OS, and the two checks the owner can ask for. Nothing here installs,
// upgrades or reboots anything, and nothing names a vulnerability.
//
//   - A newer Caravana release is found by asking GitHub's releases API once a
//     day (research R4). The request carries a User-Agent and the stored ETag and
//     nothing else; the owner's setting turns it off, and then no request is made.
//   - The OS's waiting updates, reboot and automatic-update settings come from the
//     files the sampler reads (hostosstate.go).
//   - Package names come from `pro security-status`, only when the owner asks,
//     one check at a time, and are kept in memory for an hour.

// The release check's addresses and limits.
const (
	// DefaultReleaseURL is GitHub's answer for the latest release (never a draft
	// or a pre-release) of Caravana.
	DefaultReleaseURL = "https://api.github.com/repos/Cosmic-Chimps/caravana/releases/latest"
	// defaultReleaseDownloadURL and defaultReleaseRawURL are where a release's
	// installer and its Traefik job are read from to find which Nomad and Traefik
	// it pins: <download>/<tag>/install.sh and <raw>/<tag>/deploy/install/traefik.nomad.hcl.
	defaultReleaseDownloadURL = "https://github.com/Cosmic-Chimps/caravana/releases/download"
	defaultReleaseRawURL      = "https://raw.githubusercontent.com/Cosmic-Chimps/caravana"
	// releasePageURL is where a release's page is when GitHub doesn't say.
	releasePageURL = "https://github.com/Cosmic-Chimps/caravana/releases/tag/"

	releaseRequestTimeout = 10 * time.Second
	releaseFirstCheck     = time.Minute      // a minute after Start
	defaultReleaseGap     = 24 * time.Hour   // then once a day, with up to a twenty-fourth more
	releaseResponseLimit  = 1 << 20          // what is read of GitHub's answer
	releaseBodyLimit      = 16 << 10         // what is kept of the release notes
	pinFileLimit          = 512 << 10        // what is read of the installer or the job
	releaseNotesLines     = 6                // bullet lines of the notes the page shows
	releaseNoteLength     = 240              // characters of one of them
	checkMinGap           = 10 * time.Minute // between two checks of one kind
	packageNamesFor       = time.Hour        // how long package names are kept
	packageNamesShown     = 500
	historyShown          = 10 // entries of Past updates
)

// The refusals of a check. A caller tests them with errors.Is.
var (
	// ErrCheckTooSoon: the same check ran less than 10 minutes ago, or is running.
	// The error is a *CheckTooSoonError that says when it may run again.
	ErrCheckTooSoon = errors.New("that check ran less than 10 minutes ago")
	// ErrCheckDisabled: the owner turned the release check off.
	ErrCheckDisabled = errors.New("the release check is turned off")
	// ErrCheckUnavailable: the check can't run here (a development build has no
	// release to compare with; a host without pro or not Ubuntu has no package
	// names). The error is a *CheckUnavailableError with a plain sentence.
	ErrCheckUnavailable = errors.New("that check can't run here")
)

// CheckTooSoonError is ErrCheckTooSoon with the time the check may run again.
type CheckTooSoonError struct{ Again time.Time }

func (e *CheckTooSoonError) Error() string {
	return "That check ran less than 10 minutes ago. It can run again at " + e.Again.UTC().Format("15:04") + " UTC."
}

// Is makes errors.Is(err, ErrCheckTooSoon) true.
func (e *CheckTooSoonError) Is(target error) bool { return target == ErrCheckTooSoon }

// CheckUnavailableError is ErrCheckUnavailable with the reason in Caravana's
// words, ready to show.
type CheckUnavailableError struct{ Reason string }

func (e *CheckUnavailableError) Error() string { return e.Reason }

// Is makes errors.Is(err, ErrCheckUnavailable) true.
func (e *CheckUnavailableError) Is(target error) bool { return target == ErrCheckUnavailable }

// The sentences of the checks, the page's words.
const (
	reasonDevBuild    = "This is a development build, which has no release to compare with."
	reasonUnreadable  = "caravana can't read this server's pending updates (not Ubuntu or Debian)."
	reasonNotUbuntu   = "Package names come from Ubuntu's pro command, and this server isn't Ubuntu."
	reasonNoPro       = "Package names come from Ubuntu's pro command, which this server doesn't have."
	failPackagesSlow  = "Ubuntu's pro command didn't answer in 2 minutes."
	failPackagesBad   = "Ubuntu's pro command gave an answer caravana couldn't read."
	failPackagesOther = "Ubuntu's pro command failed."
	failGitHubDown    = "GitHub didn't answer"
	failGitHubLimit   = "GitHub is limiting requests from this host"
	failGitHubNone    = "GitHub has no release of caravana yet"
	failGitHubBad     = "GitHub's answer wasn't a release"
)

// ProRunner reads package names: the host's pro command. hostos.Pro is the real
// one; tests give a fake. Its output is data from the host: it is never logged,
// stored or put in an error.
type ProRunner interface {
	// Available says there is a pro command to run.
	Available() bool
	// Run runs `pro security-status --format json` and returns its output.
	Run(ctx context.Context) ([]byte, error)
}

// updatesState is what the checks keep between calls.
type updatesState struct {
	// releaseMu is held for the length of a release check, so two never run at
	// once (the daily one and Check now).
	releaseMu sync.Mutex

	mu               sync.Mutex // guards the rest
	releaseStarted   time.Time  // when the newest release check started
	packagesStarted  time.Time  // when the newest package check started
	checkingRelease  bool
	checkingPackages bool
	names            packageNames
}

// packageNames is the result of the newest package check, kept for an hour.
type packageNames struct {
	At       time.Time // when the check finished; zero when there is none
	Packages []string
	Kernel   bool   // a kernel image is among the packages
	Err      string // why the check failed, in Caravana's words; empty when it worked
}

// SetProRunner sets what reads package names (hostos.Pro in production). Nil
// means there is none: the check is unavailable. Call it before Start.
func (s *Service) SetProRunner(p ProRunner) { s.pro = p }

// SetReleaseURL sets the address the release check asks, in place of GitHub's:
// tests and the lab give a fake server. Call it before Start.
func (s *Service) SetReleaseURL(u string) {
	if u != "" {
		s.releaseURL = u
	}
}

// SetReleasePinURLs sets where a release's installer and Traefik job are read
// from (<download>/<tag>/install.sh and <raw>/<tag>/deploy/install/traefik.nomad.hcl),
// in place of GitHub's. Empty keeps the default. Call it before Start.
func (s *Service) SetReleasePinURLs(download, raw string) {
	if download != "" {
		s.releaseDownloadURL = strings.TrimRight(download, "/")
	}
	if raw != "" {
		s.releaseRawURL = strings.TrimRight(raw, "/")
	}
}

// SetReleaseCheckGap sets the time between two release checks (a day by
// default, with up to a twenty-fourth of it added at random); a gap shorter than
// a minute also brings the first check forward. Call it before Start; a value
// that isn't positive keeps the default.
func (s *Service) SetReleaseCheckGap(d time.Duration) {
	if d > 0 {
		s.releaseGap = d
	}
}

// SetReleaseCheck stores the owner's setting: whether Caravana asks GitHub for
// new releases. Off means no request at all.
func (s *Service) SetReleaseCheck(ctx context.Context, enabled bool) error {
	if err := s.store.SetReleaseCheckEnabled(ctx, enabled); err != nil {
		return err
	}
	s.publish(Change{What: ChangeUpdates})
	return nil
}

// UpdatesView is what the Updates page and getUpdates show.
type UpdatesView struct {
	Caravana CaravanaUpdate
	OS       OSUpdates
	// History is Past updates: Caravana's version changes, the server's boots and
	// the OS's package runs, newest first, at most 10.
	History []UpdateEvent
	// Checking says which checks are running now.
	Checking struct{ Release, Packages bool }
}

// CaravanaUpdate is the Caravana row: the running version, the newer release if
// there is one, and the state of the check.
type CaravanaUpdate struct {
	// Current is the running version without a leading v ("dev" for a plain go
	// build, "" before SetVersion).
	Current string
	// Latest is the newer release's version, only when it is newer than Current
	// and the check has seen it; empty otherwise.
	Latest     string
	ReleasedAt time.Time
	URL        string
	// Notes are the first six bullet lines of the release's notes.
	Notes []string
	// Pins are the Nomad and Traefik versions this Caravana runs and the newer
	// release's, only where they differ and both are known.
	Pins []VersionPin
	// PinsText says the same in a sentence: "Updates Nomad 2.0.7 → 2.1.0 and
	// Traefik 3.7.13 → 3.8.0."; empty when no pin changes.
	PinsText string
	// Install is the command that installs Latest, as the release publishes it;
	// empty without a Latest.
	Install      string
	CheckEnabled bool
	// CheckedAt is the last attempt, successful or not; zero before the first.
	CheckedAt time.Time
	// CheckError is why the last attempt failed, in Caravana's words; empty when
	// it worked.
	CheckError string
}

// VersionPin is one part Caravana's installer pins and the versions around a
// release: Name is "Nomad" or "Traefik".
type VersionPin struct{ Name, From, To string }

// OSUpdates is what the server's own files say.
type OSUpdates struct {
	// Readable is false on a host that isn't Debian or Ubuntu.
	Readable bool
	Distro   hostos.Distro
	// Updates are the waiting updates. Known is false when the host has no such
	// file (Debian) or it said nothing.
	Updates UpdateCounts
	Reboot  RebootView
	// Unattended is how the host updates itself; nil fields couldn't be read.
	Unattended hostos.Unattended
}

// UpdateCounts are the waiting updates, with the names when they were asked for.
type UpdateCounts struct {
	Known           bool
	Total, Security int
	CheckedAt       time.Time
	// Stale says the list is older than 48 hours: it is shown, with its age, and
	// no needs-you item is made from it.
	Stale bool
	// PackagesAvailable says Check package names can run here (Ubuntu with pro).
	PackagesAvailable bool
	// Packages are the security packages by name, from the newest package check
	// (at most 500, kept an hour); PackagesKernel says a kernel image is among
	// them; PackagesCheckedAt is when it ran, zero when there is no result;
	// PackagesError is why it failed, in Caravana's words.
	Packages          []string
	PackagesKernel    bool
	PackagesCheckedAt time.Time
	PackagesError     string
}

// RebootView is the reboot row: whether one is needed and the kernels around it.
type RebootView struct {
	Required bool
	Since    time.Time
	Packages []string
	// RunningKernel is the node's kernel; InstalledKernel the newest installed.
	RunningKernel, InstalledKernel string
}

// UpdateEvent is one row of Past updates.
type UpdateEvent struct {
	At time.Time
	// Kind is "version" (Caravana changed version), "os_packages" (a package run)
	// or "boot" (the server restarted).
	Kind   string
	Text   string
	Detail string
}

// Updates reads the Updates page: the release row from the store, the OS from
// the sampler's newest reading (read now when it hasn't made one yet), the
// node's kernel and the history. It never calls GitHub or runs pro.
func (s *Service) Updates(ctx context.Context) (UpdatesView, error) {
	rc, err := s.store.ReleaseCheck(ctx)
	if err != nil {
		return UpdatesView{}, err
	}
	events, err := s.store.HostEvents(ctx, "", historyShown)
	if err != nil {
		return UpdatesView{}, err
	}
	now := s.clock.Now().UTC()
	node := s.hostNode()
	state, _, read := s.osState()
	if !read {
		state = hostos.ReadState(s.hostRoot)
	}

	var v UpdatesView
	v.Caravana = s.caravanaUpdate(rc, node)
	v.OS = s.osUpdates(state, node, now)
	v.History = updateHistory(events, node)

	u := &s.updates
	u.mu.Lock()
	v.Checking.Release, v.Checking.Packages = u.checkingRelease, u.checkingPackages
	u.mu.Unlock()
	return v, nil
}

// caravanaUpdate builds the Caravana row from the stored result of the check.
func (s *Service) caravanaUpdate(rc store.ReleaseCheck, node *HostNode) CaravanaUpdate {
	current := strings.TrimPrefix(s.runningVersion, "v")
	c := CaravanaUpdate{
		Current: current, CheckEnabled: rc.Enabled, CheckedAt: rc.CheckedAt, CheckError: rc.Error,
	}
	if !hostos.ReleaseNewer(rc.LatestTag, current) {
		return c
	}
	c.Latest = strings.TrimPrefix(rc.LatestTag, "v")
	c.ReleasedAt, c.URL = rc.PublishedAt, rc.URL
	c.Notes = releaseNotes(rc.Body)
	c.Install = "curl -fsSL " + s.downloadBase() + "/" + rc.LatestTag + "/install.sh | sudo bash"
	if node != nil {
		c.Pins = pinsOf(node, rc)
	}
	c.PinsText = pinsText(c.Pins)
	return c
}

// downloadBase is the address a release's files are under, for the install
// command: GitHub's, which is what a host can reach.
func (s *Service) downloadBase() string { return defaultReleaseDownloadURL }

// pinsOf lists the pins a release changes: the version the node runs against the
// one the release's installer or job names. A pin that isn't read on either side
// is left out.
func pinsOf(node *HostNode, rc store.ReleaseCheck) []VersionPin {
	var out []VersionPin
	for _, p := range []VersionPin{
		{"Nomad", strings.TrimPrefix(node.NomadVersion, "v"), strings.TrimPrefix(rc.NomadPin, "v")},
		{"Traefik", strings.TrimPrefix(node.TraefikVersion, "v"), strings.TrimPrefix(rc.TraefikPin, "v")},
	} {
		if p.From != "" && p.To != "" && p.From != p.To {
			out = append(out, p)
		}
	}
	return out
}

// pinsText is "Updates Nomad 2.0.7 → 2.1.0 and Traefik 3.7.13 → 3.8.0."
func pinsText(pins []VersionPin) string {
	if len(pins) == 0 {
		return ""
	}
	parts := make([]string, len(pins))
	for i, p := range pins {
		parts[i] = p.Name + " " + p.From + " → " + p.To
	}
	return "Updates " + strings.Join(parts, " and ") + "."
}

// releaseNotes is the first six bullet lines of a release's notes, each without
// its marker and cut at 240 characters.
func releaseNotes(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, " \t\r") // only a top-level bullet: an indented one belongs to the one above
		text, ok := strings.CutPrefix(line, "- ")
		if !ok {
			text, ok = strings.CutPrefix(line, "* ")
		}
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if r := []rune(text); len(r) > releaseNoteLength {
			text = string(r[:releaseNoteLength]) + "…"
		}
		if text != "" {
			out = append(out, text)
		}
		if len(out) == releaseNotesLines {
			break
		}
	}
	return out
}

// osUpdates builds the OS block from a reading of the host's files.
func (s *Service) osUpdates(state hostos.State, node *HostNode, now time.Time) OSUpdates {
	o := OSUpdates{Readable: state.Readable, Distro: state.Distro, Unattended: state.Unattended}
	o.Updates = UpdateCounts{
		Known: state.Updates.Known, Total: state.Updates.Total, Security: state.Updates.Security,
		CheckedAt: state.Updates.CheckedAt, Stale: state.Updates.Known && state.Updates.Stale(now),
		PackagesAvailable: s.packagesCheckable(state) == nil,
	}
	u := &s.updates
	u.mu.Lock()
	if n := u.names; !n.At.IsZero() && now.Sub(n.At) < packageNamesFor {
		o.Updates.Packages = slices.Clone(n.Packages)
		o.Updates.PackagesKernel, o.Updates.PackagesCheckedAt, o.Updates.PackagesError = n.Kernel, n.At, n.Err
	}
	u.mu.Unlock()

	o.Reboot = RebootView{Required: state.Reboot.Required, Since: state.Reboot.Since,
		Packages: slices.Clone(state.Reboot.Packages), InstalledKernel: state.Reboot.InstalledKernel}
	if node != nil {
		o.Reboot.RunningKernel = node.Kernel
	}
	return o
}

// updateHistory turns host events into Past updates' rows, newest first.
func updateHistory(events []store.HostEvent, node *HostNode) []UpdateEvent {
	var out []UpdateEvent
	firstBoot := true
	for _, e := range events {
		row := UpdateEvent{At: e.At.UTC(), Kind: e.Kind}
		switch e.Kind {
		case store.HostEventVersion:
			var d struct{ From, To string }
			if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
				continue
			}
			if d.From == "" {
				row.Text, row.Detail = "caravana "+d.To, "first start recorded"
			} else {
				row.Text = "caravana " + d.From + " → " + d.To
			}
		case store.HostEventOSPackages:
			var d osPackagesDetail
			if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
				continue
			}
			row.Text = fmt.Sprintf("Ubuntu updates · %d %s", d.Packages, packagesWord(d.Packages))
			by := "by hand"
			if d.By == "unattended" {
				by = "by Ubuntu's automatic updates"
			}
			row.Detail = by + " · no reboot"
			if d.Reboot {
				row.Detail = by + " · a reboot is needed"
			}
		case store.HostEventBoot:
			row.Text = "The server restarted"
			if firstBoot && node != nil && node.Kernel != "" {
				row.Detail = "kernel " + kernelShort(node.Kernel) + " since then"
			}
			firstBoot = false
		default:
			continue
		}
		out = append(out, row)
	}
	return out
}

func packagesWord(n int) string {
	if n == 1 {
		return "package"
	}
	return "packages"
}

// kernelNumbers matches the numbers a kernel release starts with: "6.8.0-149"
// of "6.8.0-149-generic".
var kernelNumbers = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*-[0-9]+`)

// kernelShort is a kernel release without its flavour: "6.8.0-149-generic" is
// "6.8.0-149". A release that doesn't start like one is returned as it is.
func kernelShort(release string) string {
	if m := kernelNumbers.FindString(release); m != "" {
		return m
	}
	return release
}

// --- the checks ---------------------------------------------------------------------------

// osNow is the sampler's newest reading of the OS, or one made now when it
// hasn't made one yet.
func (s *Service) osNow() hostos.State {
	if state, _, ok := s.osState(); ok {
		return state
	}
	return hostos.ReadState(s.hostRoot)
}

// packagesCheckable is nil when Check package names can run here, else the
// refusal with its reason.
func (s *Service) packagesCheckable(state hostos.State) error {
	switch {
	case !state.Readable:
		return &CheckUnavailableError{reasonUnreadable}
	case state.Distro.ID != "ubuntu":
		return &CheckUnavailableError{reasonNotUbuntu}
	case s.pro == nil || !s.pro.Available():
		return &CheckUnavailableError{reasonNoPro}
	}
	return nil
}

// CheckPackages starts the package-name check (`pro security-status`) and
// returns at once: the result arrives on the Updates read and the "updates"
// notice. It is refused with ErrCheckUnavailable on a host without pro or not
// Ubuntu, and with ErrCheckTooSoon within 10 minutes of the last one (which also
// holds while one runs: they take up to two minutes). Nothing pro prints is
// logged or stored; the names are kept in memory for an hour.
func (s *Service) CheckPackages(ctx context.Context) error {
	if err := s.packagesCheckable(s.osNow()); err != nil {
		return err
	}
	now := s.clock.Now()
	u := &s.updates
	u.mu.Lock()
	if !u.packagesStarted.IsZero() && now.Sub(u.packagesStarted) < checkMinGap {
		again := u.packagesStarted.Add(checkMinGap)
		u.mu.Unlock()
		return &CheckTooSoonError{Again: again}
	}
	u.packagesStarted, u.checkingPackages = now, true
	u.mu.Unlock()
	s.publish(Change{What: ChangeUpdates})

	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.runPackageCheck(s.background())
	}()
	return nil
}

// runPackageCheck runs pro and keeps what it said, or why it didn't.
func (s *Service) runPackageCheck(ctx context.Context) {
	out, err := s.pro.Run(ctx)
	res := packageNames{At: s.clock.Now().UTC()}
	switch {
	case err == nil:
		st, perr := hostos.ParseSecurityStatus(out)
		if perr != nil {
			res.Err = failPackagesBad
			break
		}
		res.Packages, res.Kernel = st.Packages, st.Kernel
		if len(res.Packages) > packageNamesShown {
			res.Packages = res.Packages[:packageNamesShown]
		}
	case errors.Is(err, context.DeadlineExceeded):
		res.Err = failPackagesSlow
	case errors.Is(err, hostos.ErrProUnavailable):
		res.Err = reasonNoPro
	case ctx.Err() != nil:
		// Shutting down: nothing to say, and the next start has no result either.
		s.updates.mu.Lock()
		s.updates.checkingPackages = false
		s.updates.mu.Unlock()
		return
	default:
		res.Err = failPackagesOther
		s.log.Warn("read package names with pro failed") // no error text: it is pro's own words
	}
	u := &s.updates
	u.mu.Lock()
	u.names, u.checkingPackages = res, false
	u.mu.Unlock()
	s.publish(Change{What: ChangeUpdates})
}

// CheckRelease starts a check of GitHub's latest release now and returns at
// once; the result arrives on the Updates read and the "updates" notice. It is
// refused with ErrCheckDisabled while the owner has the release check off (and
// then makes no request), with ErrCheckUnavailable for a build that isn't a
// release version, and with ErrCheckTooSoon within 10 minutes of the last
// check.
func (s *Service) CheckRelease(ctx context.Context) error {
	rc, err := s.store.ReleaseCheck(ctx)
	if err != nil {
		return err
	}
	switch {
	case !rc.Enabled:
		return ErrCheckDisabled
	case !hostos.IsReleaseVersion(s.runningVersion):
		return &CheckUnavailableError{reasonDevBuild}
	}
	now := s.clock.Now()
	u := &s.updates
	u.mu.Lock()
	last := rc.CheckedAt
	if u.releaseStarted.After(last) {
		last = u.releaseStarted
	}
	if u.checkingRelease || (!last.IsZero() && now.Sub(last) < checkMinGap) {
		again := last.Add(checkMinGap)
		u.mu.Unlock()
		return &CheckTooSoonError{Again: again}
	}
	u.releaseStarted, u.checkingRelease = now, true
	u.mu.Unlock()
	s.publish(Change{What: ChangeUpdates})

	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.releaseCheck(s.background())
	}()
	return nil
}

// runReleaseCheck checks first after a minute (sooner when the gap is shorter)
// and then every gap with up to a twenty-fourth of it added, until ctx ends. A
// pass does nothing while the check is off.
func (s *Service) runReleaseCheck(ctx context.Context, gap time.Duration) {
	wait := min(releaseFirstCheck, gap)
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		u := &s.updates
		u.mu.Lock()
		u.releaseStarted = s.clock.Now()
		u.mu.Unlock()
		s.releaseCheck(ctx)
		wait = gap + rand.N(gap/24+1)
	}
}

// releaseCheck is one check, whoever asked for it: it records the result and
// tells open pages. The owner's setting is read first and decides whether any
// request is made at all.
func (s *Service) releaseCheck(ctx context.Context) {
	u := &s.updates
	u.releaseMu.Lock()
	defer u.releaseMu.Unlock()
	defer func() {
		u.mu.Lock()
		u.checkingRelease = false
		u.mu.Unlock()
		s.publish(Change{What: ChangeUpdates})
	}()
	switch err := s.fetchAndRecordRelease(ctx); {
	case err == nil, errors.Is(err, ErrCheckDisabled), errors.Is(err, ErrCheckUnavailable):
	case ctx.Err() != nil:
	default:
		s.log.Warn("check for a new release", "error", err)
	}
}

// fetchAndRecordRelease makes the request, works out what it means and writes
// the row, with the message about a new release in the same transaction.
func (s *Service) fetchAndRecordRelease(ctx context.Context) error {
	rc, err := s.store.ReleaseCheck(ctx)
	if err != nil {
		return err
	}
	if !rc.Enabled {
		return ErrCheckDisabled
	}
	if !hostos.IsReleaseVersion(s.runningVersion) {
		return &CheckUnavailableError{reasonDevBuild}
	}
	now := s.clock.Now().UTC()
	rc.CheckedAt = now
	current := strings.TrimPrefix(s.runningVersion, "v")

	rel, ferr := s.fetchRelease(ctx, rc.ETag)
	switch {
	case ferr != nil:
		var f *checkFailure
		if errors.As(ferr, &f) {
			rc.Error = f.words
		} else {
			rc.Error = failGitHubDown
		}
	case rel.notModified:
		rc.Error = ""
	case rel.ignored:
		rc.ETag, rc.Error = rel.etag, ""
	default:
		if rel.tag != rc.LatestTag {
			rc.NomadPin, rc.TraefikPin = "", ""
		}
		rc.LatestTag, rc.PublishedAt, rc.URL, rc.Body = rel.tag, rel.published, rel.url, rel.body
		rc.ETag, rc.Error = rel.etag, ""
	}

	// The pins of a release that is newer than this one, read once.
	if ferr == nil && hostos.ReleaseNewer(rc.LatestTag, current) && rc.NomadPin == "" && rc.TraefikPin == "" {
		rc.NomadPin, rc.TraefikPin = s.fetchPins(ctx, rc.LatestTag)
	}

	var deliveries []store.Delivery
	if ferr == nil && hostos.ReleaseNewer(rc.LatestTag, current) && rc.LatestTag != rc.NotifiedTag {
		deliveries, err = s.releaseDeliveries(ctx, rc, current, now)
		if err != nil {
			return err
		}
		rc.NotifiedTag = rc.LatestTag
	}
	if err := s.store.SaveReleaseResult(ctx, rc, deliveries); err != nil {
		return err
	}
	if len(deliveries) > 0 {
		s.queue.poke()
	}
	if ferr != nil && ctx.Err() == nil {
		return ferr
	}
	return nil
}

// releaseDeliveries is the message "caravana 0.16.0 is out. You run 0.15.0." for
// every channel that isn't paused: it is news, not a problem, so it doesn't
// depend on what a channel asked for.
func (s *Service) releaseDeliveries(ctx context.Context, rc store.ReleaseCheck, current string, now time.Time) ([]store.Delivery, error) {
	channels, err := s.store.ListNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	latest := strings.TrimPrefix(rc.LatestTag, "v")
	ev := releaseEvent(latest, current, rc.URL, now)
	var out []store.Delivery
	for _, ch := range channels {
		if ch.Paused {
			continue
		}
		d, err := newDelivery(ch.ID, ev, now)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// releaseEvent says in words that a newer release is out. url is the release's
// page, which the message links to.
func releaseEvent(latest, current, url string, at time.Time) notify.Event {
	return notify.Event{
		Type: notify.EventCaravanaRelease, Title: notify.ReleaseTitle(latest), Text: notify.ReleaseSentence(latest, current),
		Release: &notify.Release{Current: current, Latest: latest, URL: url}, URL: url, At: at,
	}
}

// checkFailure is a failed release check, with the words that are stored and
// shown. It never holds GitHub's own text.
type checkFailure struct{ words string }

func (f *checkFailure) Error() string { return f.words }

// releaseInfo is what a good answer from GitHub says.
type releaseInfo struct {
	tag       string // v0.16.0
	url       string
	body      string
	published time.Time
	etag      string
	// notModified is a 304; ignored is a draft or a pre-release, which is never
	// the answer for "latest" but is not trusted if it is.
	notModified, ignored bool
}

// releaseJSON is the part of GitHub's release that is read.
type releaseJSON struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// get makes a GET with Caravana's one header and returns the response; the
// caller closes the body. etag, when set, goes as If-None-Match.
func (s *Service) get(ctx context.Context, address, etag string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, releaseRequestTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("User-Agent", "caravana/"+strings.TrimPrefix(s.runningVersion, "v"))
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := s.releaseClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	// The body is read before the timeout is released.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// fetchRelease asks for the latest release. Every failure is a *checkFailure
// with fixed words.
func (s *Service) fetchRelease(ctx context.Context, etag string) (releaseInfo, error) {
	resp, err := s.get(ctx, s.releaseURL, etag)
	if err != nil {
		return releaseInfo{}, &checkFailure{failGitHubDown}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return releaseInfo{notModified: true}, nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return releaseInfo{}, &checkFailure{failGitHubLimit}
	case resp.StatusCode == http.StatusNotFound:
		return releaseInfo{}, &checkFailure{failGitHubNone}
	case resp.StatusCode != http.StatusOK:
		return releaseInfo{}, &checkFailure{failGitHubDown}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, releaseResponseLimit+1))
	if err != nil {
		return releaseInfo{}, &checkFailure{failGitHubDown}
	}
	if len(data) > releaseResponseLimit {
		return releaseInfo{}, &checkFailure{failGitHubBad}
	}
	var rj releaseJSON
	if err := json.Unmarshal(data, &rj); err != nil {
		return releaseInfo{}, &checkFailure{failGitHubBad}
	}
	etagOut := resp.Header.Get("ETag")
	if len(etagOut) > 200 || strings.ContainsAny(etagOut, "\r\n") {
		etagOut = ""
	}
	if rj.Draft || rj.Prerelease {
		return releaseInfo{ignored: true, etag: etagOut}, nil
	}
	if !hostos.IsReleaseVersion(rj.TagName) {
		return releaseInfo{}, &checkFailure{failGitHubBad}
	}
	info := releaseInfo{tag: rj.TagName, url: pageURL(rj.HTMLURL, rj.TagName), published: rj.PublishedAt.UTC(), etag: etagOut,
		body: cutBytes(rj.Body, releaseBodyLimit)}
	return info, nil
}

// pageURL is the release's page when GitHub gave an http or https address, else
// the page Caravana knows it is at. It is shown as a link, so no other scheme
// gets through.
func pageURL(given, tag string) string {
	if u, err := url.Parse(given); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		return u.String()
	}
	return releasePageURL + tag
}

// cutBytes cuts s at n bytes, on a character boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r == utf8.RuneError && size == 1 {
			s = s[:len(s)-1] // the cut fell inside a character
			continue
		}
		break
	}
	return s
}

var (
	nomadPinLine   = regexp.MustCompile(`(?m)^NOMAD_VERSION=([0-9][0-9A-Za-z.\-]*)[ \t]*$`)
	traefikPinLine = regexp.MustCompile(`(?m)^[ \t]*image[ \t]*=[ \t]*"traefik:v?([0-9][0-9A-Za-z.\-]*)"`)
)

// fetchPins reads which Nomad and Traefik a release pins: the NOMAD_VERSION
// line of its install.sh and the image of its traefik.nomad.hcl. Either may be
// empty when it can't be read; the notes then say nothing about it.
func (s *Service) fetchPins(ctx context.Context, tag string) (nomad, traefik string) {
	read := func(address string) string {
		resp, err := s.get(ctx, address, "")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ""
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, pinFileLimit))
		if err != nil {
			return ""
		}
		return string(data)
	}
	if m := nomadPinLine.FindStringSubmatch(read(s.releaseDownloadURL + "/" + tag + "/install.sh")); m != nil {
		nomad = m[1]
	}
	if m := traefikPinLine.FindStringSubmatch(read(s.releaseRawURL + "/" + tag + "/deploy/install/traefik.nomad.hcl")); m != nil {
		traefik = m[1]
	}
	return nomad, traefik
}

// newReleaseClient is the client the release check uses: three redirects at the
// most (a release file is served from another host), never from https to http.
func newReleaseClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != via[0].URL.Scheme {
				return errors.New("a redirect changed the scheme")
			}
			return nil
		},
	}
}
