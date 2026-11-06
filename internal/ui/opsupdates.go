package ui

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Operator Updates page (feature 027, page 3): /ops/updates, what is waiting for Caravana and
// for the server's OS, and the exact commands to run on the server. It reads only internal/service.
// Every sentence of a banner, a refusal, a failed check or a past update is the service's own; what
// is the console's is the layout and the few sentences that only say where a thing is.
//
// Nothing here installs, upgrades or reboots anything: the page says what to run (contracts/
// page3-updates.md). Package names and the release check start in the background and the live
// "updates" notice redraws the page when they finish; without script a reload does.

// upNamesShown is how many package names the Ubuntu updates row lists before "and N more".
const upNamesShown = 12

// upRebootNamesShown is how many packages the Reboot row names before "and N more".
const upRebootNamesShown = 3

// upPastShown is how many entries Past updates lists (the service already cuts them at ten).
const upPastShown = 10

// The commands of How to apply. The installer's one is the release's own (service.CaravanaUpdate.Install).
const (
	upOSCommand     = "sudo apt update && sudo apt upgrade"
	upRebootCommand = "sudo reboot"
)

// ---- what the page draws ----------------------------------------------------------------------

// opsUpdatesPage is the whole page. Refusal is the notice of a check that was turned down, drawn
// outside the live regions so that a refresh never takes it away; Saved says the release-check
// setting was just saved.
type opsUpdatesPage struct {
	pageData
	Refusal string
	Saved   bool
	Data    upData
}

// upData is what one read gives the page and its part. Top is drawn into the main live region;
// the rest sit outside it and are swapped by id (the two read-only settings, the line after the
// release-check form, Past updates and the sidebar's mark).
type upData struct {
	Banners []upBanner
	// Run is the update run panel (feature 030): the open run, or the newest one that finished within a
	// day; nil draws nothing. It sits between the banners and Available, outside #up-top, and polls its
	// own part while it is open.
	Run     *upRun
	Avail   upAvailable
	Apply   upApply
	Auto    []upSetting
	Checked upChecked
	// Caravana is the Automatic caravana updates row: its controls (drawn once with the page) and the
	// line under it (swapped by id).
	Caravana upAutoUpdate
	Past     []upPast
	Mark     navMark
}

// upRun is the run panel: a banner row that says what the update of caravana is doing, or how it ended.
// Explain is the state's own sentence; Reason is the updater's one line (plain text); Log is the path of
// the installer's log on the server, "" when there is none to point at.
type upRun struct {
	Open     bool
	Mark     string
	MarkWord string
	Line     sentence
	Explain  string
	Reason   string
	Log      string
	// Meta is who asked and when ("from the console · asked "), then Asked, the time.
	Meta  sentence
	Asked when
	// Again is the link of Try again…, set for a run that ended without the update and a release that is
	// still newer.
	Again string
}

// upAutoUpdate is the Automatic caravana updates row. Disabled turns the checkbox and the hour off (no
// updater: a disabled control isn't posted, so the form carries no marker then). Blocked is why it
// can't run, in the service's words; else Next is when the next one is due, Enabled says "on", and
// "off" is the rest.
type upAutoUpdate struct {
	Enabled  bool
	Disabled bool
	Hour     int
	Hours    []upHour
	Hint     sentence
	Blocked  string
	Next     when
}

// upHour is one option of the hour select.
type upHour struct {
	Value    int
	Label    string
	Selected bool
}

// upUpdate is the action of the caravana row: the link to the confirmation page, or (Missing) the
// sentence that says to run the installer once.
type upUpdate struct {
	Version string
	Href    string
	Missing bool
}

// navMark is the little mark after the sidebar's Updates item: ■ a reboot is needed, ◆ security
// updates are waiting, else the count of what is available, else nothing. Word is its sentence for a
// screen reader and the hover. Dim sets a count in fg-3: the package lists are stale, so the count may
// be out of date and is never ◆.
type navMark struct {
	Text string
	Word string
	Dim  bool
	// Hue is the status hue the mark wears: "failed" for ■, "warning" for ◆ (shape plus colour, as
	// every other mark); empty for a count, which stays fg-2.
	Hue string
}

// upBanner is a needs-you item of the page: the mark, the item's own sentence (the service's parts, the
// ones the needs-you list and the feed draw), what follows if it is left, and (always) the link to How
// to apply.
type upBanner struct {
	Mark    string
	Line    sentence
	Explain string
}

// upAvailable is the Available section. Meta is the count or "Nothing waiting. ..."; Fault is the
// sentence of a check that failed (GitHub), drawn in fg-3 after Meta and MetaSep (" · ", or " " after
// a Meta that is a sentence of its own); Notice is the stale-list sentence when the list has no row
// of its own to carry it.
type upAvailable struct {
	Meta    string
	MetaSep string
	Fault   string
	Notice  sentence
	Rows    []upRow
	// OSMessage is the one sentence an unreadable OS gets ("caravana can't read this server's pending
	// updates (not Ubuntu or Debian).") with its ○ mark, in place of the OS rows.
	OSMessage string
}

// upRow is one row of Available. Kind is "release", "ubuntu" or "reboot". Dim sets the row's text in
// fg-3 (a list Ubuntu hasn't refreshed in two days).
type upRow struct {
	Kind     string
	Mark     string
	MarkWord string // the mark in words, for a screen reader
	Name     string
	Sub      upSub
	Change   upChange
	Dim      bool
	// Notes are the lines of the notes column: a release's bullets and what it pins, a stale list's
	// sentence, the reboot's consequence.
	Notes []upNote
	// Link is the release's page, after its notes.
	Link *upLink
	// Hint is what the Ubuntu updates row says where Check package names would be, on a host that
	// can't give names.
	Hint sentence
	// Names is the Ubuntu updates row's package names.
	Names *upNames
	// Update is the caravana row's action (feature 030), after the notes and the release link.
	Update *upUpdate
}

// upChange is the change column: words and tokens. A token (a version pair, a package name) never
// breaks inside, and lines break only at the spaces between them.
type upChange []upTok

// upTok is a piece of a change: a token or plain words.
type upTok struct {
	Text  string
	Token bool
}

// upNote is one line of the notes column. Bullet puts a • before it. A release bullet with a bold
// lead has Lead (drawn 500, fg-1), then ": " when Colon, else a space, then Text (fg-2).
type upNote struct {
	Bullet bool
	Lead   sentence
	Colon  bool
	Text   sentence
}

// upLink is a link out of the page; it opens in the same tab, like the console's other links.
type upLink struct {
	Label string
	Href  string
}

// upSub is the 12px line under a row's name: a label, then a time ("released Oct 28", "checked by
// Ubuntu 3 h ago", "needed since 18:11 UTC").
type upSub struct {
	Label string
	When  when
}

// upCheck is a check's button, or its running state: the form posts what to check.
type upCheck struct {
	What     string // "release" or "packages"
	Label    string
	Checking string // the sentence of a check in progress; the button is gone then
	Failed   string // why the last check failed, in the service's words
}

// upNames is the package names of the Ubuntu updates row: the names (and when they were read), the
// running state, or the button that asks for them; none of them when the host can't give names.
type upNames struct {
	Checking string
	Names    []string
	More     int
	Checked  when
	Empty    string
	Failed   string
	Button   *upCheck
}

// upApply is the How to apply section: only the steps that apply, in order.
type upApply struct {
	Steps []upStep
}

// upStep is one step: its number, the sentence that tells what to run, the command, and (reboot) the
// consequence line. CopyLabel is the Copy button's name for a screen reader ("Copy the reboot command").
type upStep struct {
	N         int
	ID        string // the command box's id, "up-cmd-os", for the copy button
	CopyLabel string
	Title     sentence
	Command   string
	Warn      string
}

// upSetting is a read-only setting of the host: its label, hint and value.
type upSetting struct {
	Label string
	Hint  string
	Value string
}

// upChecked is the release-check setting and what follows its form: whether the check is on (the
// checkbox, drawn once with the page: a refresh never replaces a box someone is changing), then
// "checked 14 h ago", "off" or "not checked yet", why the last check failed, and the Check now
// button or its running state (swapped by id): the page's only Check now.
type upChecked struct {
	Enabled bool
	Text    string
	When    when
	Failed  string
	Check   *upCheck
}

// upPast is one row of Past updates.
type upPast struct {
	Mark   string
	Date   when
	Text   sentence
	Detail sentence
}

// ---- handlers ---------------------------------------------------------------------------------

// showOpsUpdates answers GET /ops/updates.
func (s *server) showOpsUpdates(w http.ResponseWriter, r *http.Request, session string) {
	s.drawOpsUpdates(w, r, http.StatusOK, "")
}

// partOpsUpdates answers GET /parts/ops/updates: the main region, and by id the read-only
// settings, the line after the release-check form, Past updates and the sidebar's mark.
func (s *server) partOpsUpdates(w http.ResponseWriter, r *http.Request, session string) {
	d, err := s.loadOpsUpdates(r)
	if err != nil {
		s.log.Error("draw the updates page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "opsupdates", "up-part", d)
}

// partOpsUpdatesRun answers GET /parts/ops/updates/run, which the run panel asks for every 5 seconds while
// its run is open: the panel itself, and (once the run is finished) the main region, the banners, the
// settings' lines, Past updates and the sidebar's mark by id, so that the caravana row and its step agree
// with the version that now runs.
func (s *server) partOpsUpdatesRun(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	v, err := s.svc.Updates(ctx)
	if err != nil {
		s.log.Error("read the update run", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := s.now().UTC()
	d := upData{Run: upRunOf(now, v)}
	if d.Run != nil && !d.Run.Open {
		att, err := s.svc.Attention(ctx)
		if err != nil {
			s.log.Error("read the update run", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		d = upDataOf(now, v, att)
	}
	s.renderPart(w, "opsupdates", "up-runpart", d)
}

// drawOpsUpdates draws the page with the given status; refusal, when set, is the sentence of a check
// that was turned down.
func (s *server) drawOpsUpdates(w http.ResponseWriter, r *http.Request, status int, refusal string) {
	d, err := s.loadOpsUpdates(r)
	if err != nil {
		s.serverError(w, r, "draw the updates page", err)
		return
	}
	page, err := s.shellPage(r, "Updates", navOpsUpdates, crumb{Label: "Updates"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page.LiveURL = "/live?watch=ops"
	if d.Run != nil && d.Run.Open {
		// Without script the page reloads itself while an update is under way (base.html draws the meta).
		page.Refresh = upRefreshSeconds
	}
	s.render(w, status, "opsupdates", opsUpdatesPage{pageData: page, Refusal: refusal,
		Saved: r.URL.Query().Get("saved") == "1", Data: d})
}

// checkOpsUpdates answers POST /ops/updates/check: it starts the release check or the package-name
// check and goes back to the page (303); the result arrives on the live notice. A check that is
// turned down draws the page again with the service's sentence and the status the API gives it,
// never a redirect.
func (s *server) checkOpsUpdates(w http.ResponseWriter, r *http.Request, session string) {
	var err error
	switch what := r.PostFormValue("what"); what {
	case "release":
		err = s.svc.CheckRelease(r.Context())
	case "packages":
		err = s.svc.CheckPackages(r.Context())
	default:
		s.drawOpsUpdates(w, r, http.StatusUnprocessableEntity, "Choose what to check: the release or the package names.")
		return
	}
	switch {
	case err == nil:
		http.Redirect(w, r, "/ops/updates#available", http.StatusSeeOther)
	case errors.Is(err, service.ErrCheckTooSoon):
		s.drawOpsUpdates(w, r, http.StatusTooManyRequests, refusalWords(err))
	case errors.Is(err, service.ErrCheckDisabled), errors.Is(err, service.ErrCheckUnavailable):
		s.drawOpsUpdates(w, r, http.StatusConflict, refusalWords(err))
	default:
		s.serverError(w, r, "start a check for updates", err)
	}
}

// refusalWords is a refusal's own sentence with a capital and a full stop: the service words some of
// them as a clause ("the release check is turned off").
func refusalWords(err error) string {
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(text)
	text = string(unicode.ToUpper(r)) + text[n:]
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return text
}

// upBadHour is the refusal of an hour the select can't have sent.
const upBadHour = "Choose an hour from 00:00 UTC to 23:00 UTC."

// upRefreshSeconds is how often a page without script reloads itself while an update is under way.
const upRefreshSeconds = 15

// saveOpsUpdateSettings answers POST /ops/updates/settings: what caravana owns about updates, whether it
// asks GitHub for new releases and (feature 030) whether it installs patch releases by itself, and at
// which UTC hour. A checked box is on and a missing one is off, so the form always posts both boxes'
// states, with the hidden marker autoForm that says it had them: a form without the marker (an older
// one) leaves the automatic setting alone, and so does one whose controls were disabled (a disabled
// control posts nothing, and that isn't the owner turning it off).
func (s *server) saveOpsUpdateSettings(w http.ResponseWriter, r *http.Request, session string) {
	check := r.PostFormValue("checkEnabled") != ""
	in := service.UpdateSettings{CheckEnabled: &check}
	if r.PostFormValue("autoForm") != "" {
		on := r.PostFormValue("autoUpdate") != ""
		in.AutoUpdate = &on
		if raw := strings.TrimSpace(r.PostFormValue("autoUpdateHour")); raw != "" {
			hour, err := strconv.Atoi(raw)
			if err != nil {
				s.drawOpsUpdates(w, r, http.StatusUnprocessableEntity, upBadHour)
				return
			}
			in.AutoUpdateHour = &hour
		}
	}
	err := s.svc.PutUpdateSettings(r.Context(), in)
	var fe *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/ops/updates?saved=1#how", http.StatusSeeOther)
	case errors.As(err, &fe):
		s.drawOpsUpdates(w, r, http.StatusUnprocessableEntity, upBadHour)
	default:
		s.serverError(w, r, "save the update settings", err)
	}
}

// ---- loader -----------------------------------------------------------------------------------

// loadOpsUpdates reads the updates and the needs-you items and builds what the page and its part draw.
func (s *server) loadOpsUpdates(r *http.Request) (upData, error) {
	ctx := r.Context()
	v, err := s.svc.Updates(ctx)
	if err != nil {
		return upData{}, err
	}
	att, err := s.svc.Attention(ctx)
	if err != nil {
		return upData{}, err
	}
	return upDataOf(s.now().UTC(), v, att), nil
}

// upDataOf builds everything the page and its parts draw from one read of the updates and of the
// needs-you items.
func upDataOf(now time.Time, v service.UpdatesView, att service.Attention) upData {
	return upData{
		Banners:  upBanners(att, places{}),
		Run:      upRunOf(now, v),
		Avail:    upAvailableOf(now, v),
		Apply:    upApplyOf(v),
		Auto:     upSettings(v.OS),
		Checked:  upCheckedOf(now, v),
		Caravana: upAutoUpdateOf(now, v.Caravana),
		Past:     upPastOf(now, v.History),
		Mark:     updatesMarkOf(now, v),
	}
}

// updatesMarkOf is the sidebar's mark: the same facts the page's rows say, so the two never differ.
func updatesMarkOf(now time.Time, v service.UpdatesView) navMark {
	u := v.OS.Updates
	switch {
	case v.OS.Reboot.Required:
		return navMark{Text: "■", Word: "reboot needed", Hue: "failed"}
	case u.Known && u.Security > 0 && !u.Stale:
		return navMark{Text: "◆", Word: "security updates waiting", Hue: "warning"}
	}
	if n := len(upAvailableOf(now, v).Rows); n > 0 {
		// A stale list says the count may be out of date: fg-3, and never ◆.
		return navMark{Text: strconv.Itoa(n), Word: plural(n, "update") + " available", Dim: v.OS.Readable && u.Known && u.Stale}
	}
	return navMark{}
}

// updatesMark reads the updates for the shell's sidebar. A read that fails draws no mark: the page
// around it is worth more than a mark.
func (s *server) updatesMark(r *http.Request) navMark {
	if s.svc == nil {
		return navMark{}
	}
	v, err := s.svc.Updates(r.Context())
	if err != nil {
		s.log.Warn("can't read the updates for the sidebar", "error", err)
		return navMark{}
	}
	return updatesMarkOf(s.now().UTC(), v)
}

// ---- banners ----------------------------------------------------------------------------------

// upBanners are the needs-you items about the OS, ■ the reboot before ◆ the updates. The sentence is
// the item's own (service.ProblemParts, with the kernels and packages in mono), so the list, the feed
// and this page say the same.
func upBanners(att service.Attention, pl places) []upBanner {
	var out []upBanner
	for _, kind := range []service.AttentionKind{service.AttentionOSRebootRequired, service.AttentionOSSecurityUpdates} {
		for _, it := range att.Items {
			if it.Kind != kind {
				continue
			}
			b := upBanner{Mark: "failed", Line: problemSentence(it, pl)}
			if kind == service.AttentionOSRebootRequired {
				b.Explain = "Until it reboots, the server keeps running the old version of what was updated."
				if kernelPending(it.InstalledKernel, it.RunningKernel) {
					b.Explain = "Until it reboots, the server keeps running the old kernel."
				}
			} else {
				b.Mark = "warning"
				b.Explain = "Until they are installed, the server keeps running the old versions."
			}
			out = append(out, b)
		}
	}
	return out
}

// kernelPending says a kernel newer than the running one is installed.
func kernelPending(installed, running string) bool {
	return installed != "" && running != "" && hostos.KernelNewer(installed, running)
}

// kernelNumbers matches the numbers a kernel release starts with: "6.8.0-149" of "6.8.0-149-generic".
var kernelNumbers = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*-[0-9]+`)

// kernelNumber is a kernel release without its flavour. A release that doesn't start like one is
// returned as it is.
func kernelNumber(release string) string {
	if m := kernelNumbers.FindString(release); m != "" {
		return m
	}
	return release
}

// ---- Available --------------------------------------------------------------------------------

// The marks' order in Available: ■ before ◆ before ○.
var upMarkRank = map[string]int{"failed": 0, "warning": 1, "idle": 2}

// upAvailableOf builds Available: the caravana release, the OS's waiting updates and the reboot,
// sorted ■, ◆, ○. GitHub that can't be reached leaves the release row out and says so in the meta.
func upAvailableOf(now time.Time, v service.UpdatesView) upAvailable {
	var rows []upRow
	c := v.Caravana
	released := c.Latest != "" && c.CheckError == ""
	if released {
		rows = append(rows, releaseRow(now, v))
	}
	o := v.OS
	var notice sentence
	if o.Readable && o.Updates.Known && o.Updates.Total > 0 {
		rows = append(rows, ubuntuRow(now, v))
	} else if o.Readable && o.Updates.Known && o.Updates.Stale {
		notice = staleWords(now, o.Updates.CheckedAt)
	}
	if o.Reboot.Required {
		rows = append(rows, rebootRow(now, o))
	}
	slices.SortStableFunc(rows, func(a, b upRow) int { return cmp.Compare(upMarkRank[a.Mark], upMarkRank[b.Mark]) })

	a := upAvailable{Rows: rows, Notice: notice}
	if !o.Readable {
		a.OSMessage = "caravana can't read this server's pending updates (not Ubuntu or Debian)."
	}
	switch {
	case len(rows) > 0:
		a.Meta = plural(len(rows), "update")
		// With something else listed, Ubuntu's own part is said too: a clean list is a fact the owner
		// would otherwise have to guess at.
		if o.Readable && o.Updates.Known && o.Updates.Total == 0 && !o.Updates.Stale {
			a.Meta += " · Ubuntu has nothing waiting"
			if !o.Updates.CheckedAt.IsZero() {
				a.Meta += " (checked " + agoFull(now, o.Updates.CheckedAt) + ")"
			}
		}
	case a.OSMessage == "":
		a.Meta = "Nothing waiting."
		if at := oldestCheck(v); !at.IsZero() {
			a.Meta += " Checked " + agoFull(now, at) + "."
		}
	}
	if c.CheckError != "" {
		// "Last checked" is the last check that worked, since that is what the sentence claims.
		a.Fault = "Couldn't check GitHub yet."
		if !c.CheckedOKAt.IsZero() {
			a.Fault = "Couldn't check GitHub (last checked " + agoFull(now, c.CheckedOKAt) + ")."
		}
		switch {
		case a.Meta == "":
		case strings.HasSuffix(a.Meta, "."):
			a.MetaSep = " "
		default:
			a.MetaSep = " · "
		}
	}
	return a
}

// oldestCheck is the oldest of the two checks' times: Ubuntu's list and the release check (only
// while it is on). Zero when neither has run.
func oldestCheck(v service.UpdatesView) time.Time {
	var oldest time.Time
	for _, t := range []time.Time{v.OS.Updates.CheckedAt, releaseCheckedAt(v.Caravana)} {
		if !t.IsZero() && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
	}
	return oldest
}

func releaseCheckedAt(c service.CaravanaUpdate) time.Time {
	if !c.CheckEnabled {
		return time.Time{}
	}
	return c.CheckedOKAt
}

// staleWords is the sentence of a list Ubuntu hasn't refreshed in two days, with the command in mono.
func staleWords(now, checked time.Time) sentence {
	age := now.Sub(checked)
	days := max(int(age/(24*time.Hour)), 2)
	return sentence{plain("Ubuntu's list is " + plural(days, "day") + " old. Run "), mono("sudo apt update"), plain(" on the server to refresh it.")}
}

// noteCodeNowrap is the longest code span of the release notes that is kept on one line ("--domain" never
// breaks at its hyphen); a longer one keeps normal breaking, so it can't overflow a narrow column.
const noteCodeNowrap = 24

// noteCode keeps the notes' short code spans whole (designer's last look on page 3).
func noteCode(s sentence) sentence {
	for i := range s {
		if s[i].Mono && utf8.RuneCountInString(s[i].Text) <= noteCodeNowrap {
			s[i].Nowrap = true
		}
	}
	return s
}

func releaseRow(now time.Time, v service.UpdatesView) upRow {
	c := v.Caravana
	row := upRow{Kind: "release", Mark: "idle", MarkWord: "update available", Name: "caravana", Change: upChange{{Text: c.Current + " → " + c.Latest, Token: true}}}
	for _, n := range c.Notes {
		note := upNote{Bullet: true, Colon: n.Colon, Text: noteCode(backticked(n.Text))}
		if n.Lead != "" {
			note.Lead = noteCode(backticked(n.Lead))
		}
		row.Notes = append(row.Notes, note)
	}
	if !c.ReleasedAt.IsZero() {
		row.Sub = upSub{Label: "released", When: when{Text: shortDate(now, c.ReleasedAt), Exact: c.ReleasedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
			ISO: c.ReleasedAt.UTC().Format(time.RFC3339)}}
	}
	if pins := pinsSentence(c.Pins); len(pins) > 0 {
		row.Notes = append(row.Notes, upNote{Text: pins})
	}
	if c.URL != "" {
		row.Link = &upLink{Label: "Release notes on GitHub", Href: c.URL}
	}
	row.Update = updateAction(c)
	return row
}

// updateAction is what the caravana row offers (feature 030): the link to the confirmation page when the
// updater is installed and no run is open; the sentence that says to run the installer once when the
// updater isn't there; nothing for a development build, which has no release to compare with, or
// while a run is open (the run panel says what happens).
func updateAction(c service.CaravanaUpdate) *upUpdate {
	switch {
	case !hostos.IsReleaseVersion(c.Current):
		return nil
	case c.Run != nil && !store.UpdateRunFinal(c.Run.State):
		return nil
	case !c.Updater.Installed:
		return &upUpdate{Version: c.Latest, Missing: true}
	}
	return &upUpdate{Version: c.Latest, Href: upConfirmHref(c.Latest)}
}

// upConfirmHref is the confirmation page of an update to version.
func upConfirmHref(version string) string {
	return "/ops/updates/caravana?version=" + url.QueryEscape(version)
}

// pinsSentence is "Updates Nomad 2.0.7 → 2.1.0 and Traefik 3.7.13 → 3.8.0." with each version pair in
// mono, so a pair never breaks inside. The words are the service's PinsText.
func pinsSentence(pins []service.VersionPin) sentence {
	if len(pins) == 0 {
		return nil
	}
	out := sentence{plain("Updates ")}
	for i, p := range pins {
		if i > 0 {
			out = append(out, plain(" and "))
		}
		out = append(out, plain(p.Name+" "), part{Text: p.From + " → " + p.To, Mono: true, Nowrap: true})
	}
	return append(out, plain("."))
}

// releaseCheck is the Check now button, or its running state. It is nil for a check that can't be asked
// for (the check is off).
func releaseCheck(v service.UpdatesView) *upCheck {
	if !v.Caravana.CheckEnabled {
		return nil
	}
	if v.Checking.Release {
		return &upCheck{What: "release", Checking: "Checking GitHub…"}
	}
	return &upCheck{What: "release", Label: "Check now"}
}

func ubuntuRow(now time.Time, v service.UpdatesView) upRow {
	u := v.OS.Updates
	row := upRow{Kind: "ubuntu", Mark: "idle", MarkWord: "update available", Name: "Ubuntu updates", Dim: u.Stale,
		Change: upChange{{Text: plural(u.Total, "package") + " · " + strconv.Itoa(u.Security) + " security"}}}
	if u.Security > 0 && !u.Stale {
		row.Mark, row.MarkWord = "warning", "security updates"
	}
	if !u.CheckedAt.IsZero() {
		row.Sub = upSub{Label: "checked by Ubuntu", When: whenAgo(now, u.CheckedAt)}
	}
	if u.Stale {
		row.Notes = append(row.Notes, upNote{Text: staleWords(now, u.CheckedAt)})
	} else if hint := securityHint(now, v.OS); hint != nil {
		row.Notes = append(row.Notes, upNote{Text: hint})
	}
	row.Names = namesOf(now, v)
	if row.Names == nil {
		// No pro to ask: say how to see the names, where Check package names would be.
		row.Hint = sentence{plain("Run "), mono("apt list --upgradable"), plain(" on the server to see which packages.")}
	}
	return row
}

// securityHint is the first line of the Ubuntu row's notes while the host's automatic updates are on
// and security updates wait: it says only what is known. The counts don't say which origins
// unattended-upgrades allows, so the page never claims that it will install these particular ones, nor
// how many are left for the owner. Inside the service's 24-hour wait it says they usually install by
// themselves; after it (when the needs-you item opens) it says they have not, and where to look.
// nil without automatic updates, or without security updates.
func securityHint(now time.Time, o service.OSUpdates) sentence {
	if o.Unattended.Enabled == nil || !*o.Unattended.Enabled || o.Updates.Security == 0 {
		return nil
	}
	if o.Updates.SecurityGraceOver(now) {
		return sentence{plain("Still waiting after a day, although Ubuntu's automatic updates are on. Check "),
			mono("/var/log/unattended-upgrades/"), plain(" on the server.")}
	}
	return plainS("Ubuntu's automatic updates are on, so security updates usually install by themselves overnight.")
}

// whenAgo is a time in the short words of a dense column, with its exact UTC time on hover.
func whenAgo(now, t time.Time) when {
	t = t.UTC()
	return when{Text: agoWords(now, t), Exact: t.Format("2006-01-02 15:04:05 UTC"), ISO: t.Format(time.RFC3339)}
}

// namesOf is the package-name part of the Ubuntu row: the running state, the names, the reason a
// check failed (with the button to try again), the button, or nothing on a host that can't give names.
func namesOf(now time.Time, v service.UpdatesView) *upNames {
	u := v.OS.Updates
	switch {
	case v.Checking.Packages:
		return &upNames{Checking: "Checking which packages… this can take a minute."}
	case !u.PackagesCheckedAt.IsZero() && u.PackagesError == "":
		n := &upNames{Checked: whenAgo(now, u.PackagesCheckedAt)}
		n.Names = u.Packages[:min(len(u.Packages), upNamesShown)]
		n.More = len(u.Packages) - len(n.Names)
		if len(u.Packages) == 0 {
			n.Empty = "Ubuntu's pro lists no security packages."
		}
		return n
	case !u.PackagesAvailable:
		return nil
	}
	n := &upNames{Button: &upCheck{What: "packages", Label: "Check package names"}}
	if !u.PackagesCheckedAt.IsZero() {
		n.Failed = u.PackagesError
	}
	return n
}

func rebootRow(now time.Time, o service.OSUpdates) upRow {
	r := o.Reboot
	note := "Rebooting stops every app for about a minute; Nomad starts them again when the server is back."
	if by := installedBy(r); by != "" {
		note = by + " " + note
	}
	row := upRow{Kind: "reboot", Mark: "failed", MarkWord: "reboot needed", Name: "Reboot", Change: rebootChange(r),
		Notes: []upNote{{Text: plainS(note)}}}
	if !r.Since.IsZero() {
		row.Sub = upSub{Label: "needed since", When: sinceWhen(now, r.Since)}
	}
	return row
}

// sinceWhen is the reboot row's time, said once: "18:11 UTC" when it is from today (UTC), else the
// date as the console says it in dense places ("Oct 28"). The full UTC time is its title (the page's
// script adds the viewer's local one). It is plain text, so it never wraps in the 240px column.
func sinceWhen(now, t time.Time) when {
	t = t.UTC()
	text := shortDate(now, t)
	if t.Format("2006-01-02") == now.UTC().Format("2006-01-02") {
		text = t.Format("15:04") + " UTC"
	}
	return when{Text: text, Exact: t.Format("2006-01-02 15:04:05 UTC"), ISO: t.Format(time.RFC3339)}
}

// installedBy is "Installed by Ubuntu's automatic updates." (or "Installed by hand."): the apt run that
// installed what the reboot is for, which the service matches against the marker's list as the history
// does. The row's sub says when the reboot became needed, so the date isn't repeated here. "" when no
// run is known.
func installedBy(r service.RebootView) string {
	if r.InstalledAt.IsZero() {
		return ""
	}
	if r.InstalledBy == "unattended" {
		return "Installed by Ubuntu's automatic updates."
	}
	return "Installed by hand."
}

// rebootChange is the Reboot row's change: the two kernels, or the packages that asked for it. Versions
// and package names are tokens.
func rebootChange(r service.RebootView) upChange {
	if kernelPending(r.InstalledKernel, r.RunningKernel) {
		return upChange{{Text: "kernel "}, {Text: kernelNumber(r.RunningKernel) + " → " + kernelNumber(r.InstalledKernel), Token: true}}
	}
	n := len(r.Packages)
	if n == 0 {
		return upChange{{Text: "needed"}}
	}
	shown := r.Packages[:min(n, upRebootNamesShown)]
	var out upChange
	for i, p := range shown {
		if i > 0 {
			out = append(out, upTok{Text: ", "})
		}
		out = append(out, upTok{Text: p, Token: true})
	}
	if n > len(shown) {
		out = append(out, upTok{Text: " and " + strconv.Itoa(n-len(shown)) + " more"})
	}
	return out
}

// ---- How to apply -----------------------------------------------------------------------------

// upApplyOf lists the steps that apply, in the order to run them: the OS updates, then caravana's
// installer, then the reboot, last.
func upApplyOf(v service.UpdatesView) upApply {
	var steps []upStep
	add := func(s upStep) {
		s.N = len(steps) + 1
		steps = append(steps, s)
	}
	o := v.OS
	if o.Readable && o.Updates.Known && o.Updates.Total > 0 {
		add(upStep{ID: "up-cmd-os", CopyLabel: "Copy the install command", Title: plainS("Install the waiting updates:"), Command: upOSCommand})
	}
	if c := v.Caravana; c.Latest != "" && c.CheckError == "" && c.Install != "" {
		title := sentence{plain("Run the installer of "), mono(c.Latest),
			plain(". It upgrades caravana, and Nomad and Traefik when their versions change; your apps keep running, and it puts "),
			mono(c.Current), plain(" back if "), mono(c.Latest), plain(" fails its health check.")}
		if c.Updater.Installed {
			// The updater is there: the console can ask for it, and the installer is the other way.
			title = sentence{plain("Update from the caravana row above, or run the installer of "), mono(c.Latest), plain(" on the server:")}
		} else {
			title = append(title, plain(" Running it once also adds the updater, so later updates can start from this page."))
		}
		add(upStep{ID: "up-cmd-caravana", CopyLabel: "Copy the installer command", Command: c.Install, Title: title})
	}
	if o.Reboot.Required {
		add(upStep{ID: "up-cmd-reboot", CopyLabel: "Copy the reboot command", Title: plainS("Reboot the server:"), Command: upRebootCommand,
			Warn: "Every app stops for about a minute. The console comes back when the server does."})
	}
	return upApply{Steps: steps}
}

// ---- How this server updates ------------------------------------------------------------------

const upSettingHint = "Set on the server, in unattended-upgrades."

// unreadable is what a setting says when its file can't be read.
const unreadable = "caravana can't read this setting."

// upSettings are the two read-only settings of the host.
func upSettings(o service.OSUpdates) []upSetting {
	auto := upSetting{Label: "Ubuntu's automatic updates", Hint: upSettingHint, Value: unreadable}
	reboot := upSetting{Label: "Automatic reboot", Hint: upSettingHint, Value: unreadable}
	if !o.Readable {
		return []upSetting{auto, reboot}
	}
	if e := o.Unattended.Enabled; e != nil {
		auto.Value = "Off"
		if *e {
			auto.Value = "On: security updates install by themselves."
		}
	}
	if r := o.Unattended.AutoReboot; r != nil {
		reboot.Value = "Off: reboots wait for you."
		if *r {
			reboot.Value = "On, right after an update that needs one."
			if t := o.Unattended.AutoRebootTime; t != "" && t != "now" {
				reboot.Value = "On, at " + t + " server time"
			}
		}
	}
	return []upSetting{auto, reboot}
}

// upCheckedOf is the line after the release-check form.
func upCheckedOf(now time.Time, v service.UpdatesView) upChecked {
	c := v.Caravana
	out := upChecked{Enabled: c.CheckEnabled, Failed: c.CheckError, Check: releaseCheck(v)}
	switch {
	case !c.CheckEnabled:
		// The dense mono line says when the last good check was, in short units; plain "off" when
		// there never was one.
		out.Text, out.Failed = "off", ""
		if !c.CheckedOKAt.IsZero() {
			out.Text = "off · last checked"
			out.When = whenAgo(now, c.CheckedOKAt)
		}
	case c.CheckedAt.IsZero():
		out.Text = "not checked yet"
	default:
		out.Text = "checked"
		out.When = whenAgo(now, c.CheckedAt)
	}
	return out
}

// ---- Past updates -----------------------------------------------------------------------------

// upPastOf are the rows of Past updates, newest first as the service gives them.
// versionToken is a version in Past updates' words, or a pair of them: a number with a dot, then
// anything a version holds ("0.12.0-dev.023", "6.8.0-149", "2:1.2.3~ubuntu1"), optionally " → " and another.
// A count ("14 packages") has no dot and stays plain.
var versionToken = regexp.MustCompile(`\d+\.[0-9A-Za-z.~+:-]*[0-9A-Za-z~+]( → \d+\.[0-9A-Za-z.~+:-]*[0-9A-Za-z~+])?`)

// versionTokens draws a Past updates line with each version, or version pair, as one mono token that never
// breaks at its hyphen (designer's approval of page 3: the same class as review fix 2).
func versionTokens(text string) sentence {
	var out sentence
	last := 0
	for _, m := range versionToken.FindAllStringIndex(text, -1) {
		if m[0] > last {
			out = append(out, plain(text[last:m[0]]))
		}
		out = append(out, part{Text: text[m[0]:m[1]], Mono: true, Nowrap: true})
		last = m[1]
	}
	if last < len(text) {
		out = append(out, plain(text[last:]))
	}
	return out
}

func upPastOf(now time.Time, history []service.UpdateEvent) []upPast {
	var out []upPast
	for _, e := range history {
		if len(out) == upPastShown {
			break
		}
		t := e.At.UTC()
		date := shortDate(now, t)
		mark := "healthy"
		switch e.Kind {
		case "boot":
			mark = "idle"
		case "update":
			mark = upRunMark(e.State)
		}
		out = append(out, upPast{Mark: mark, Text: versionTokens(e.Text), Detail: versionTokens(e.Detail),
			Date: when{Text: date, Exact: t.Format("2006-01-02 15:04:05 UTC"), ISO: t.Format(time.RFC3339)}})
	}
	return out
}

// upRunMark is the mark of an update run in a state: ● updated, ■ failed, ◆ rolled back, refused or lost,
// ◐ while it is open.
func upRunMark(state string) string {
	switch state {
	case store.UpdateUpdated:
		return "healthy"
	case store.UpdateFailed:
		return "failed"
	case store.UpdateRequested, store.UpdateRunning:
		return "rolling"
	}
	return "warning"
}

// ---- the run panel ----------------------------------------------------------------------------

// upRunOf builds the run panel from the view's run, nil when there is none to show.
func upRunOf(now time.Time, v service.UpdatesView) *upRun {
	c := v.Caravana
	r := c.Run
	if r == nil {
		return nil
	}
	ver := part{Text: r.Version, Mono: true, Nowrap: true}
	pair := ver
	if r.FromVersion != "" {
		pair = part{Text: r.FromVersion + " → " + r.Version, Mono: true, Nowrap: true}
	}
	out := &upRun{Open: !store.UpdateRunFinal(r.State), Mark: upRunMark(r.State), Reason: r.Reason}
	out.MarkWord = map[string]string{"rolling": "update under way", "healthy": "updated", "failed": "failed", "warning": "needs a look"}[out.Mark]
	switch r.State {
	case store.UpdateRequested:
		out.Line = sentence{plain("Waiting for the server's updater to start the update to "), ver, plain(".")}
		out.Explain = "It looks for a request every minute."
	case store.UpdateRunning:
		out.Line = sentence{plain("Updating caravana "), pair, plain(".")}
		out.Explain = "The console stops for about a minute while the new version starts. This page comes back by itself; your apps keep running."
	case store.UpdateUpdated:
		out.Line = sentence{plain("caravana "), ver, plain(" is running.")}
		out.Explain = "Updated"
		if r.FromVersion != "" {
			out.Explain += " from " + r.FromVersion
		}
		out.Explain += " in " + service.UpdateRunDuration(*r) + "."
	case store.UpdateRolledBack:
		out.Line = sentence{plain("caravana "), ver, plain(" didn't start, so "), part{Text: r.FromVersion, Mono: true, Nowrap: true}, plain(" runs again with its data as it was.")}
		out.Log = r.Log
	case store.UpdateFailed:
		out.Line = sentence{plain("The update to caravana "), ver, plain(" failed.")}
		out.Log = r.Log
	case store.UpdateRefused:
		out.Line = sentence{plain("The server's updater didn't start the update to "), ver, plain(".")}
	case store.UpdateLost:
		out.Line = sentence{plain("The update to caravana "), ver, plain(" didn't finish.")}
	}
	if r.State == store.UpdateRolledBack && r.FromVersion == "" {
		out.Line = sentence{plain("caravana "), ver, plain(" didn't start, so the version before it runs again with its data as it was.")}
	}
	if out.Reason != "" && !strings.ContainsAny(out.Reason[len(out.Reason)-1:], ".!?") {
		out.Reason += "."
	}

	asked := r.RequestedAt.UTC()
	switch r.Trigger {
	case store.UpdateByAutomatic:
		out.Meta = sentence{plain("automatic, at ")}
	case store.UpdateByAPI:
		out.Meta = sentence{plain("through the API by "), mono(r.TokenName), plain(" · asked ")}
		if r.TokenName == "" {
			out.Meta = sentence{plain("through the API · asked ")}
		}
	default:
		out.Meta = sentence{plain("from the console · asked ")}
	}
	out.Asked = when{Text: runClock(now, asked), Exact: asked.Format("2006-01-02 15:04:05 UTC"), ISO: asked.Format(time.RFC3339)}

	if !out.Open && r.State != store.UpdateUpdated && c.Latest != "" && c.Updater.Unavailable == "" {
		out.Again = upConfirmHref(c.Latest)
	}
	return out
}

// runClock is a time of the run panel: "14:02 UTC" for today (UTC), else with its date ("Nov 5, 14:02 UTC").
func runClock(now, t time.Time) string {
	t = t.UTC()
	if t.Format("2006-01-02") == now.UTC().Format("2006-01-02") {
		return t.Format("15:04") + " UTC"
	}
	return shortDate(now, t) + ", " + t.Format("15:04") + " UTC"
}

// ---- Automatic caravana updates ---------------------------------------------------------------

// patchSeries is "0.16" of "0.16.2": the release line the automatic setting stays on.
var patchSeries = regexp.MustCompile(`^(\d+\.\d+)\.\d+`)

// upAutoUpdateOf builds the Automatic caravana updates row.
func upAutoUpdateOf(now time.Time, c service.CaravanaUpdate) upAutoUpdate {
	a := upAutoUpdate{Enabled: c.Auto.Enabled, Disabled: !c.Updater.Installed, Hour: c.Auto.Hour, Blocked: c.Auto.Blocked}
	a.Hint = sentence{plain("Only patch releases")}
	if m := patchSeries.FindStringSubmatch(c.Current); m != nil {
		a.Hint = append(a.Hint, plain(" ("), part{Text: m[1] + ".x", Mono: true}, plain(")"))
	}
	a.Hint = append(a.Hint, plain(", which fix without changing how things work. A newer minor release waits for you, and a version that failed is never tried again by itself."))
	for h := range 24 {
		a.Hours = append(a.Hours, upHour{Value: h, Label: fmt.Sprintf("%02d:00 UTC", h), Selected: h == c.Auto.Hour})
	}
	if !c.Auto.Next.IsZero() && a.Blocked == "" {
		t := c.Auto.Next.UTC()
		a.Next = when{Text: shortDate(now, t) + ", " + t.Format("15:04") + " UTC", Exact: t.Format("2006-01-02 15:04:05 UTC"), ISO: t.Format(time.RFC3339)}
	}
	return a
}

// ---- the confirmation page --------------------------------------------------------------------

// upConfirmPage is the page that asks before caravana is updated (contracts/page-updates.md §2): the
// Stop page's shape. Refusal takes the panel's and the button's place.
type upConfirmPage struct {
	pageData
	Headline sentence
	Version  string
	Paras    []sentence
	Note     string
	Refusal  sentence
}

// upConfirmOf builds the panel's sentences for an update from current to version, with the pins the
// release changes.
func upConfirmOf(c service.CaravanaUpdate, version string) upConfirmPage {
	cur := c.Current
	p := upConfirmPage{Version: version, Headline: sentence{plain("Update caravana to "), part{Text: version, Mono: true, Nowrap: true}}}
	ver := part{Text: version, Mono: true, Nowrap: true}
	p.Paras = []sentence{
		{plain("caravana "), part{Text: cur + " → " + version, Mono: true, Nowrap: true},
			plain(". The server's updater starts it within a minute, as root, from the installer: the console only asks.")},
		plainS("The console stops for about a minute while the new version starts. Your apps keep running and keep answering."),
	}
	for _, pin := range c.Pins {
		pair := part{Text: pin.From + " → " + pin.To, Mono: true, Nowrap: true}
		switch pin.Name {
		case "Nomad":
			p.Paras = append(p.Paras, sentence{plain("Nomad "), pair, plain(" is installed in the same run. Nomad restarts; apps keep running.")})
		case "Traefik":
			p.Paras = append(p.Paras, sentence{plain("Traefik "), pair, plain(" is installed in the same run. Traefik restarts, so every app's address stops answering for a few seconds.")})
		}
	}
	if len(c.Pins) == 0 {
		p.Paras = append(p.Paras, plainS("Nomad and Traefik stay as they are, unless the installer changes their settings."))
	}
	p.Paras = append(p.Paras, sentence{plain("If "), ver, plain(" doesn't answer its health check within a minute, "),
		part{Text: cur, Mono: true, Nowrap: true}, plain(" comes back with its data as it was, and this page says why.")})
	p.Note = "OS updates and the reboot aren't part of it: they stay on the server."
	return p
}

// showOpsUpdateConfirm answers GET /ops/updates/caravana?version=: what the update does, and the button
// that asks for it. A version the service wouldn't accept right now is refused with its own sentence
// (409), so the page never offers a button that would only be turned down.
func (s *server) showOpsUpdateConfirm(w http.ResponseWriter, r *http.Request, session string) {
	s.drawOpsUpdateConfirm(w, r, strings.TrimSpace(r.URL.Query().Get("version")), nil)
}

// requestOpsUpdate answers POST /ops/updates/caravana: the update is requested and the browser goes to
// the run panel (303). A refusal draws the confirmation page again with the service's sentence and the
// status the API gives it, never a redirect.
func (s *server) requestOpsUpdate(w http.ResponseWriter, r *http.Request, session string) {
	version := strings.TrimSpace(r.PostFormValue("version"))
	_, err := s.svc.RequestUpdate(r.Context(), version, store.UpdateByConsole, "")
	if err == nil {
		http.Redirect(w, r, "/ops/updates#run", http.StatusSeeOther)
		return
	}
	s.drawOpsUpdateConfirm(w, r, version, err)
}

// drawOpsUpdateConfirm draws the confirmation page. refused is why a post was turned down; with none, the
// page asks the service whether the version would be accepted.
func (s *server) drawOpsUpdateConfirm(w http.ResponseWriter, r *http.Request, version string, refused error) {
	ctx := r.Context()
	if refused == nil {
		refused = s.svc.CheckUpdateOffer(ctx, version)
	}
	v, err := s.svc.Updates(ctx)
	if err != nil {
		s.serverError(w, r, "draw the update confirmation", err)
		return
	}
	version = strings.TrimPrefix(version, "v")
	page := upConfirmOf(v.Caravana, version)
	status := http.StatusOK
	if refused != nil {
		var fe *app.FieldError
		switch {
		case errors.Is(refused, service.ErrUpdateUnavailable), errors.Is(refused, service.ErrUpdateRunning), errors.Is(refused, service.ErrUpdateNotOffered):
			status = http.StatusConflict
			page.Refusal = versionTokens(refused.Error())
		case errors.As(refused, &fe):
			status = http.StatusUnprocessableEntity
			page.Refusal = plainS(refusalWords(errors.New(fe.Message)))
		default:
			s.serverError(w, r, "request an update of caravana", refused)
			return
		}
		page.Paras, page.Note = nil, ""
	}
	head, err := s.shellPage(r, "Update caravana", navOpsUpdates, crumb{Label: "Updates", Href: "/ops/updates"}, crumb{Label: "Update caravana"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page.pageData = head
	s.render(w, status, "opsupdatesconfirm", page)
}
