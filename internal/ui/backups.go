package ui

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Backups tab (features 010, 016 and 021): the app's backed-up services as
// rows that open (the last 30 days as a strip, the runs, Restore…, Back up now),
// the services that could be backed up and aren't, with the one action that
// fixes it, and the recent restores. Plain forms; a restore asks for the
// service's name first, in a <details>.

const (
	// backupRunsShown is how many runs a service's row lists before "Show older".
	backupRunsShown = 10
	// stripDays is how many days the restore-points strip covers, today last.
	stripDays = 30
)

// backupsTab is the body of the Backups tab.
type backupsTab struct {
	ID       string
	Revision string
	Defined  bool
	Stopped  bool
	// Destination says a destination is set; Bucket is its bucket's name.
	Destination bool
	Bucket      string
	BackedUp    int // services with a policy
	Failing     int // of those, the ones whose newest run failed
	// Gaps are the services that could be backed up and have no policy.
	Gaps []backupGap
	// Rows are the services with a policy, in the order of the definition.
	Rows []backupSvcRow
	// Unmounted are volumes Nomad still has for the app that no service mounts.
	Unmounted []unmountedVolume
	// MultiServer says the cluster has more than one server, so a volume says which one it is on
	// (feature 031).
	MultiServer bool
	Restores    []restoreRow
	Notice      sentence
	// ConfirmErr is the backup whose restore confirmation was wrong; its row opens.
	ConfirmErr string
	// ShowAll is the service whose whole run list is shown ("Show older").
	ShowAll string
}

// Not is how many services could be backed up and aren't.
func (t backupsTab) Not() int { return len(t.Gaps) }

// backupGap is a service with something to back up and no policy: a database
// image, or a volume. Engine is the policy "Back up daily" sets.
type backupGap struct {
	Service string
	Label   string // the name as the sentence says it: "postgres", or "web · /data"
	Meta    string // "PostgreSQL · 0 copies kept", "Volume · 210 MB"
	Engine  string
	// Confirm is what "Back up daily" says it will do, before it does it.
	Confirm sentence
	// SQLiteFile is the start of the database path the "SQLite file instead"
	// form offers (the first volume's path and "/"), for a service with a
	// volume; empty when the service has none (feature 029).
	SQLiteFile string
}

// backupSvcRow is one backed-up service: the row, and what opens under it.
type backupSvcRow struct {
	Name   string
	Volume bool
	// SQLite says the service backs up one SQLite file (feature 029): it keeps
	// running during a backup, and stops briefly for a restore.
	SQLite bool
	Open   bool
	What   string // "PostgreSQL", "Volume · /data, /cache"
	// Mark is the row's mark: rolling while a run is going, failed when the
	// newest failed, healthy otherwise, idle when it never ran.
	Mark string
	// State is what the row says to a screen reader first: "postgres, last backup failed".
	State    string
	Schedule string // in words, "Every day at 03:00 UTC"
	Keep     string // "7 backups"
	// LastWhen is when the newest run started; LastNow says it is running, and
	// LastNever that nothing ran yet. Took and Stopped are its facts.
	LastWhen  when
	LastClock clockTime
	LastNow   bool
	LastNever bool
	// LastFailed says the newest run failed: its time is followed by "· failed".
	LastFailed bool
	Took       string
	Stopped    string
	Size       string // the newest run's size, the run the time is of; none when it failed
	SizeAll    string // the kept runs' sum, "1.4 GB all"
	Strip      []stripCell
	// StripLabel is the strip as one phrase, read before it by a screen reader (the strip has
	// no role, so its link bars stay reachable).
	StripLabel string
	Failure    sentence
	// VolumeFacts say where each volume of a volume service lives and how big it was
	// measured: "/srv/.../data on this host · 312 MB, measured 3 hours ago".
	VolumeFacts []string
	Runs        []backupRun
	OlderHref   string // "Show older", when there are more runs than are listed
	// RestoreLast is the newest healthy run, for "Restore from the last backup";
	// nil when there is none or a restore isn't possible.
	RestoreLast *backupRun
	CanBackUp   bool

	// Pinned is how many of the service's listed backups are pinned (feature 036); Keep says so.
	Pinned int
	// RestoreOn says the policy has a restore schedule; RestoreLine is "Restores golden every day at
	// 04:00 UTC", SchedLast how the newest scheduled restore went ("last: failed — …"), empty when
	// there is none in the recent restores, and NotPinned that no listed backup carries the label.
	RestoreOn    bool
	RestoreLine  sentence
	SchedLast    string
	NotPinned    bool
	RestoreLabel string
	// CanSchedule says the "Restore on a schedule…" form can be offered: the app is deployed
	// as defined and not stopped. Its inputs are the current schedule and label, or the defaults.
	CanSchedule     bool
	ScheduleInput   string
	LabelInput      string
	DefaultSchedule string
}

// backupRun is one backup run of a service, as the row lists it.
type backupRun struct {
	ID, Service, Status, Mark, Size, Took, Stopped, Message string
	// Cause is a plain reason for a failed run (causeOf), led before the detail in Message.
	Cause string
	When  when
	// Clock is when the run started as a clock time (feature 022): what the
	// row, the Restore button and its confirmation say.
	Clock      clockTime
	CanRestore bool
	Volume     bool
	SQLite     bool
	// File is the database a sqlite restore replaces, as the service sees it.
	File string
	// ByYou says the owner started the run (Back up now), not its schedule.
	ByYou bool
	// Pin is the label the run is pinned under (feature 036), "" when it isn't. CanPin says Pin… is
	// offered: a succeeded run that isn't pinned.
	Pin    string
	CanPin bool
}

// stripCell is one day of the restore-points strip. Kind is healthy (a day with
// a healthy run is restorable), failed (only failed runs), running (the day's
// newest run is going) or none (a day with no run: a gap). Capped says a healthy
// day's newest run failed: its bar has a failed cap. A day with a run links to
// the run the bar stands for.
type stripCell struct {
	Kind   string
	Capped bool
	Href   string
	Title  string
}

// unmountedVolume is a volume Nomad has for the app that no service mounts.
type unmountedVolume struct {
	Name, HostPath, Size string
	// Where is "this host", or the name of the server when the cluster has several.
	Where string
}

// restoreRow is one restore. Stopped is "stopped 4 s" for a volume restore
// once it was measured.
type restoreRow struct {
	Service, BackupID, Status, Mark, Took, Message string
	Created                                        when
	CreatedClock                                   clockTime
	Stopped                                        string
	// Scheduled says the service's restore schedule started it, from the backup pinned under Pin
	// (feature 036); else the owner did.
	Scheduled bool
	Pin       string
}

// restoreAskView is what the restore confirmation draws: the run it restores,
// whose form it is (Prefix keeps the IDs of two forms for one run apart) and
// whether the last answer said the typed name was wrong.
type restoreAskView struct {
	AppID  string
	Run    backupRun
	Prefix string
	Failed bool
	// When is the run's time, as the button says it: "26 minutes ago".
	When string
}

// restoreAskFor is the template function {{restoreAsk $ . ""}}.
func restoreAskFor(tab backupsTab, run backupRun, prefix string) restoreAskView {
	return restoreAskView{AppID: tab.ID, Run: run, Prefix: prefix, Failed: prefix == "" && tab.ConfirmErr == run.ID, When: run.Clock.Text}
}

// backupEngineFor says how a service could be backed up, from what it is: a
// database image Caravana can dump (its repository is postgres, and it has a port
// to dump over), else, with a volume, the volume engine. ok is false for a
// service with neither, which has nothing the console can offer. A Postgres
// service on a volume gets the dump, which doesn't stop it.
func backupEngineFor(svc app.Service) (engine string, ok bool) {
	if ref, err := registry.Parse(svc.Image); err == nil && len(svc.Ports) > 0 && (ref.Repository == "postgres" || strings.HasSuffix(ref.Repository, "/postgres")) {
		return app.BackupPostgres, true
	}
	if len(svc.Volumes) > 0 {
		return app.BackupVolume, true
	}
	return "", false
}

// engineText is a policy's engine as Configure lists it: the engine, and for
// a sqlite backup its file ("sqlite /data/app.db").
func engineText(b app.Backup) string {
	if b.Engine == app.BackupSQLite && b.File != "" {
		return b.Engine + " " + b.File
	}
	return b.Engine
}

// mountsOf is the paths a service mounts its volumes at: "/data, /cache".
func mountsOf(svc app.Service) string {
	paths := make([]string, len(svc.Volumes))
	for i, v := range svc.Volumes {
		paths[i] = v.Path
	}
	return strings.Join(paths, ", ")
}

// upperFirst is s with its first letter in capitals: "every day" is "Every day".
func upperFirst(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+len(string(r)):]
	}
	return s
}

// backupsTabOf builds the tab's body: the services of the app, its runs and
// restores (newest first, as ListBackups gives them), its volumes and the
// destination (nil when none is set). extra carries what the answer to a post
// adds. It is pure.
func backupsTabOf(now time.Time, sum service.AppSummary, backups []store.BackupRecord, restores []store.RestoreRecord,
	volumes []service.VolumeView, dest *store.BackupDestination, extra backupsTab) backupsTab {
	a := sum.App
	tab := extra
	tab.ID, tab.Revision, tab.Defined, tab.Stopped = a.ID, sum.Revision, a.Defined(), sum.StoppedAt != nil
	if dest != nil {
		tab.Destination, tab.Bucket = true, dest.Bucket
	}

	for _, svc := range a.Services {
		if svc.Backup == nil {
			engine, ok := backupEngineFor(svc)
			if !ok {
				continue
			}
			gap := backupGap{Service: svc.Name, Label: svc.Name, Engine: engine}
			if engine == app.BackupVolume {
				gap.Label = svc.Name + " · " + mountsOf(svc)
				gap.Confirm = sentence{plain("Backs up "), mono(mountsOf(svc)), plain(" every day at 03:00 UTC and keeps 7. The service stops for a few seconds each time. This deploys the app.")}
				gap.Meta = "Volume"
				if size, ok := serviceVolumeBytes(svc.Name, volumes); ok {
					gap.Meta += " · " + humanBytes(size)
				}
				gap.SQLiteFile = svc.Volumes[0].Path + "/"
			} else {
				gap.Confirm = sentence{plain("Backs up the database every day at 03:00 UTC and keeps 7. This deploys the app.")}
				gap.Meta = "PostgreSQL · 0 copies kept"
			}
			tab.Gaps = append(tab.Gaps, gap)
			continue
		}
		tab.BackedUp++
		var runs []store.BackupRecord
		for _, b := range backups {
			if b.Service == svc.Name {
				runs = append(runs, b)
			}
		}
		row := backupRowOf(now, a.ID, svc, runs, restores, volumes, tab)
		if row.Mark == "failed" {
			tab.Failing++
		}
		row.Open = len(tab.Rows) == 0 || svc.Name == tab.ShowAll || slices.ContainsFunc(row.Runs, func(r backupRun) bool { return r.ID == tab.ConfirmErr })
		tab.Rows = append(tab.Rows, row)
	}

	for _, v := range volumes {
		if !v.Mounted {
			u := unmountedVolume{Name: v.Name, HostPath: v.HostPath, Where: whereIs(tab.MultiServer, v.Server)}
			if v.Size != nil {
				u.Size = humanBytes(v.Size.Bytes)
			}
			tab.Unmounted = append(tab.Unmounted, u)
		}
	}
	for _, x := range restores {
		row := restoreRow{Service: x.Service, BackupID: x.BackupID, Status: x.Status, Message: x.Message, Created: whenOf(now, &x.CreatedAt, ""),
			CreatedClock: clockOf(now, x.CreatedAt)}
		row.Mark = runMark(x.Status)
		row.Scheduled, row.Pin = x.Trigger == store.RestoreTriggerSchedule, x.Pin
		if x.FinishedAt != nil {
			row.Took = durationText(x.FinishedAt.Sub(x.CreatedAt))
		}
		if x.Stopped != nil {
			row.Stopped = "stopped " + stoppedText(*x.Stopped)
		}
		tab.Restores = append(tab.Restores, row)
	}
	return tab
}

// whereIs says where a volume lives: "this host" with one server, else the server's name (or "this host" when
// the volume's server isn't known).
func whereIs(multi bool, server string) string {
	if multi && server != "" {
		return server
	}
	return "this host"
}

// serviceVolumeBytes is the measured size of the volumes a service mounts, and
// whether any of them has been measured.
func serviceVolumeBytes(svcName string, volumes []service.VolumeView) (int64, bool) {
	var total int64
	known := false
	for _, v := range volumes {
		if v.Size == nil || !slices.ContainsFunc(v.Mounts, func(m service.VolumeMount) bool { return m.Service == svcName }) {
			continue
		}
		total, known = total+v.Size.Bytes, true
	}
	return total, known
}

// runMark is a run's mark: a disc for one that went well, a square for one that
// failed, a half disc while it is going.
func runMark(status string) string {
	switch status {
	case store.BackupSucceeded:
		return "healthy"
	case store.BackupFailed:
		return "failed"
	case store.RestoreSkipped:
		return "idle" // it did nothing: the idle ring, not a failure
	}
	return "rolling"
}

// stateText is what a row says to a screen reader before its visible text: the
// service and how its last backup went.
func stateText(name, mark string) string {
	switch mark {
	case "healthy":
		return name + ", last backup succeeded"
	case "failed":
		return name + ", last backup failed"
	case "rolling":
		return name + ", backup running"
	}
	return name + ", never backed up"
}

// causeTable is the plain reasons a backup run fails for, as the rclone and S3
// errors say them: the first entry with a word in the message wins, so a
// specific cause beats the retries that ran out around it. A message with none
// of these words has no cause: its detail stands alone.
var causeTable = []struct {
	cause string
	words []string
}{
	{"The destination refused the connection.", []string{"connection refused"}},
	{"The destination's host name wasn't found.", []string{"no such host"}},
	{"The destination refused the access key.", []string{"accessdenied", "access denied", "invalidaccesskeyid", "signaturedoesnotmatch", "statuscode: 403", "status code: 403", "403 forbidden"}},
	{"The bucket wasn't found at the destination.", []string{"nosuchbucket", "bucket does not exist", "specified bucket"}},
	// The SDK gives up after its retries when the destination never answers well: a timeout, or no answer at all.
	{"The destination didn't answer in time.", []string{"i/o timeout", "timed out", "timeout", "deadline exceeded", "exceeded maximum number of attempt"}},
}

// causeOf is the plain reason for a failed run's message, or "".
func causeOf(message string) string {
	msg := strings.ToLower(message)
	for _, c := range causeTable {
		for _, w := range c.words {
			if strings.Contains(msg, w) {
				return c.cause
			}
		}
	}
	return ""
}

// backupRowOf builds one backed-up service's row. runs are its backups, newest first.
func backupRowOf(now time.Time, appID string, svc app.Service, runs []store.BackupRecord, restores []store.RestoreRecord,
	volumes []service.VolumeView, tab backupsTab) backupSvcRow {
	b := svc.Backup
	row := backupSvcRow{Name: svc.Name, Volume: b.Engine == app.BackupVolume, SQLite: b.Engine == app.BackupSQLite, Mark: "idle", LastNever: true,
		Schedule: upperFirst(scheduleText(b.ScheduleOrDefault())), Keep: plural(b.KeepOrDefault(), "backup"),
		CanBackUp: tab.Destination && !tab.Stopped}
	if row.Volume {
		row.What = "Volume · " + mountsOf(svc)
		for _, v := range volumes {
			if !slices.ContainsFunc(v.Mounts, func(m service.VolumeMount) bool { return m.Service == svc.Name }) || v.HostPath == "" && v.Size == nil {
				continue
			}
			fact := v.Name
			if v.HostPath != "" {
				fact = v.HostPath + " on " + whereIs(tab.MultiServer, v.Server)
			}
			if v.Size != nil {
				fact += " · " + humanBytes(v.Size.Bytes) + ", measured " + relative(now, v.Size.MeasuredAt)
			}
			row.VolumeFacts = append(row.VolumeFacts, fact)
		}
	} else if row.SQLite {
		row.What = "SQLite · " + b.File
	} else {
		row.What = "PostgreSQL"
	}

	var all int64
	sawSize := false
	for _, r := range runs {
		if r.Status == store.BackupSucceeded {
			all, sawSize = all+r.Bytes, true
		}
	}
	if sawSize {
		row.SizeAll = humanBytes(all) + " all"
	}

	for i, r := range runs {
		run := backupRun{ID: r.ID, Service: r.Service, Status: r.Status, Mark: runMark(r.Status), Message: r.Message,
			When: whenOf(now, &r.StartedAt, ""), Clock: clockOf(now, r.StartedAt), Volume: r.Engine == app.BackupVolume,
			SQLite: r.Engine == app.BackupSQLite, File: b.File,
			CanRestore: r.Status == store.BackupSucceeded && tab.Destination && !tab.Stopped,
			ByYou:      r.Trigger == store.BackupTriggerOwner,
			Pin:        r.Pin, CanPin: r.Status == store.BackupSucceeded && r.Pin == ""}
		if r.Pin != "" {
			row.Pinned++
		}
		if r.Status == store.BackupSucceeded {
			run.Size = humanBytes(r.Bytes)
		}
		if r.Status == store.BackupFailed {
			run.Cause = causeOf(r.Message)
		}
		if r.FinishedAt != nil {
			run.Took = durationText(r.FinishedAt.Sub(r.StartedAt))
		}
		if r.Stopped != nil {
			run.Stopped = "stopped " + stoppedText(*r.Stopped)
		}
		if i == 0 {
			row.Mark, row.LastNever, row.LastNow = run.Mark, false, r.Status == store.BackupRunning
			row.LastWhen, row.LastClock, row.Took, row.Stopped, row.Size = run.When, run.Clock, run.Took, run.Stopped, run.Size
			row.LastFailed = r.Status == store.BackupFailed
			if r.Status == store.BackupFailed {
				// The error is in the run's row, once.
				row.Failure = sentence{plain("The last backup failed " + relative(now, r.StartedAt) + ".")}
			}
		}
		if row.RestoreLast == nil && run.CanRestore {
			last := run
			row.RestoreLast = &last
		}
		// A pinned run is always listed: it is the one a restore schedule may need.
		if i < backupRunsShown || svc.Name == tab.ShowAll || r.Pin != "" {
			row.Runs = append(row.Runs, run)
		}
	}
	if len(runs) > backupRunsShown && svc.Name != tab.ShowAll {
		row.OlderHref = "/apps/" + appID + "/backups?all=" + svc.Name + "#svc-" + svc.Name
	}
	row.State = stateText(svc.Name, row.Mark)
	if row.Pinned > 0 {
		row.Keep += ", plus " + strconv.Itoa(row.Pinned) + " pinned"
	}
	restoreScheduleOf(&row, now, svc, runs, restores, tab)
	row.Strip = restoreStrip(now, runs)
	row.StripLabel = stripLabel(now, runs)
	return row
}

// restoreScheduleOf fills the row's restore schedule (feature 036): the line that says it, how the
// newest scheduled restore went, a note when no listed backup carries the label, and what the
// "Restore on a schedule…" form starts with. runs are the service's listed backups, restores the
// app's recent ones, newest first.
func restoreScheduleOf(row *backupSvcRow, now time.Time, svc app.Service, runs []store.BackupRecord, restores []store.RestoreRecord, tab backupsTab) {
	row.CanSchedule = tab.Defined && !tab.Stopped
	row.ScheduleInput, row.LabelInput, row.DefaultSchedule = "0 4 * * *", "golden", "0 4 * * *"
	r := svc.Backup.Restore
	if r == nil {
		return
	}
	row.RestoreOn, row.RestoreLabel = true, r.From
	row.ScheduleInput, row.LabelInput = r.Schedule, r.From
	row.RestoreLine = sentence{plain("Restores "), mono(r.From), plain(" " + scheduleText(r.Schedule))}
	row.NotPinned = !slices.ContainsFunc(runs, func(b store.BackupRecord) bool { return b.Pin == r.From })
	for _, x := range restores {
		if x.Service != svc.Name || x.Trigger != store.RestoreTriggerSchedule {
			continue
		}
		switch {
		case x.Status == store.BackupSucceeded:
			row.SchedLast = "last: " + relative(now, x.CreatedAt) + ", succeeded"
		case x.Status == store.BackupRunning:
			row.SchedLast = "last: running now"
		case x.Message != "":
			row.SchedLast = "last: " + x.Status + " — " + x.Message
		default:
			row.SchedLast = "last: " + x.Status
		}
		break
	}
}

// stripDay is the UTC day of t, as its midnight.
func stripDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// daysAgo is how many UTC days before now t is.
func daysAgo(now, t time.Time) int { return int(stripDay(now).Sub(stripDay(t)) / (24 * time.Hour)) }

// restoreStrip is the last 30 days as cells, oldest first and today last. A day
// is the UTC day. A day whose newest run is going is running; a day with any
// healthy run is restorable, a healthy bar (capped in failed when that day's
// newest run failed); a day with only failed runs is failed; a day with none is
// a gap. A cell with a run links to its row: the newest healthy one for a healthy
// bar. Runs are newest first.
func restoreStrip(now time.Time, runs []store.BackupRecord) []stripCell {
	cells := make([]stripCell, stripDays)
	newest := make([]int, stripDays)  // index into runs of the day's newest run, -1 for none
	healthy := make([]int, stripDays) // and of its newest healthy one
	count := make([]int, stripDays)
	for i := range cells {
		cells[i].Kind, newest[i], healthy[i] = "none", -1, -1
	}
	for i, r := range runs {
		ago := daysAgo(now, r.StartedAt)
		if ago < 0 || ago >= stripDays {
			continue
		}
		at := stripDays - 1 - ago
		count[at]++
		if newest[at] < 0 {
			newest[at] = i
		}
		if healthy[at] < 0 && r.Status == store.BackupSucceeded {
			healthy[at] = i
		}
	}
	for at := range cells {
		if newest[at] < 0 {
			continue
		}
		last := runs[newest[at]]
		shown := newest[at]
		switch {
		case last.Status == store.BackupRunning:
			cells[at].Kind = "running"
		case healthy[at] >= 0:
			cells[at].Kind, shown = "healthy", healthy[at]
			cells[at].Capped = last.Status == store.BackupFailed
		default:
			cells[at].Kind = "failed"
		}
		cells[at].Href = "#backup-" + runs[shown].ID
		when := "today"
		if ago := stripDays - 1 - at; ago > 0 {
			when = strconv.Itoa(ago) + " d ago"
		}
		title := when + " · " + runs[shown].StartedAt.UTC().Format("15:04")
		if count[at] > 1 {
			// More than one run that day: say how many and how the last went.
			how := map[string]string{store.BackupSucceeded: "healthy", store.BackupFailed: "failed", store.BackupRunning: "running"}[last.Status]
			title = when + " · " + strconv.Itoa(count[at]) + " runs, the last " + how
		} else {
			switch cells[at].Kind {
			case "healthy":
				title += " · " + humanBytes(last.Bytes)
			case "failed":
				title += " · failed"
			default:
				title += " · running"
			}
		}
		cells[at].Title = title
	}
	return cells
}

// stripLabel is the strip as one phrase: "30 days: 2 backups, 1 failed, the last 26
// minutes ago". Runs are newest first.
func stripLabel(now time.Time, runs []store.BackupRecord) string {
	total, failed := 0, 0
	for _, r := range runs {
		if ago := daysAgo(now, r.StartedAt); ago < 0 || ago >= stripDays {
			continue
		}
		total++
		if r.Status == store.BackupFailed {
			failed++
		}
	}
	if total == 0 {
		return "30 days: no backups"
	}
	label := "30 days: " + plural(total, "backup")
	if failed > 0 {
		label += ", " + strconv.Itoa(failed) + " failed"
	}
	return label + ", the last " + relative(now, runs[0].StartedAt)
}

// backupsPage draws the Backups tab with the given status.
func (s *server) backupsPage(w http.ResponseWriter, r *http.Request, status int, id string, extra backupsTab) {
	ctx := r.Context()
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	backups, restores, err := s.svc.ListBackups(ctx, id)
	if err != nil {
		s.serverError(w, r, "list backups", err)
		return
	}
	var dest *store.BackupDestination
	switch d, destErr := s.svc.GetBackupDestination(ctx); {
	case destErr == nil:
		dest = &d
	case !errors.Is(destErr, store.ErrNotFound):
		s.serverError(w, r, "read backup destination", destErr)
		return
	}
	head, err := s.shellPage(r, id+" backups", navProjects, s.appCrumbs(ctx, id)...)
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id

	volumes, err := s.svc.Volumes(ctx, id)
	if err != nil {
		s.serverError(w, r, "read volumes", err)
		return
	}
	if extra.ShowAll == "" {
		extra.ShowAll = r.URL.Query().Get("all")
	}
	extra.MultiServer = s.multiServer(ctx)
	tab := backupsTabOf(s.now(), sum, backups, restores, volumes, dest, extra)
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(ctx, sum, "backups"), Backups: &tab})
}

// humanBytes is a size as people read it: "312 MB", "1.5 GB", "4 KB". Under 10
// of a unit it keeps one decimal (and drops a ".0"); from 10 up, whole units.
func humanBytes(n int64) string {
	if n < 1<<10 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v, i := float64(n)/(1<<10), 0
	for v >= 1023.5 && i < len(units)-1 { // 1023.6 KB would print as "1024 KB"
		v /= 1 << 10
		i++
	}
	if v < 9.95 { // 9.96 would print as "10.0"
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + " " + units[i]
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
}

// weekdays are the days of a cron expression's fifth field, 0 (or 7) being Sunday.
var weekdays = [...]string{"Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays"}

// scheduleText tells a cron expression in words when it is a plain one: "every
// day at 03:00 UTC" for "0 3 * * *", "Sundays at 04:00 UTC" for "0 4 * * 0",
// "every 6 hours" for "0 */6 * * *", "every hour at :15" for "15 * * * *". Any
// other expression is shown as it is, so nothing is ever described wrongly.
func scheduleText(spec string) string {
	f := strings.Fields(spec)
	if len(f) == 5 && f[2] == "*" && f[3] == "*" {
		minute, errMinute := strconv.Atoi(f[0])
		hour, errHour := strconv.Atoi(f[1])
		goodMinute := errMinute == nil && minute >= 0 && minute <= 59
		goodHour := errHour == nil && hour >= 0 && hour <= 23
		switch {
		case !goodMinute:
		case f[4] == "*" && f[1] == "*":
			return fmt.Sprintf("every hour at :%02d", minute)
		case f[4] == "*" && goodHour:
			return fmt.Sprintf("every day at %02d:%02d UTC", hour, minute)
		case f[4] == "*" && minute == 0 && strings.HasPrefix(f[1], "*/"):
			if n, err := strconv.Atoi(f[1][2:]); err == nil && n >= 2 && n <= 23 {
				return fmt.Sprintf("every %d hours", n)
			}
		case goodHour:
			if day, err := strconv.Atoi(f[4]); err == nil && day >= 0 && day <= 7 {
				return fmt.Sprintf("%s at %02d:%02d UTC", weekdays[day%7], hour, minute)
			}
		}
	}
	return "on " + spec + " (UTC)"
}

// stoppedText is how long the service was stopped, in whole seconds ("4 s"),
// or "under 1 s" for a stop that was shorter.
func stoppedText(d time.Duration) string {
	if d < 500*time.Millisecond {
		return "under 1 s"
	}
	return fmt.Sprintf("%d s", int(math.Round(d.Seconds())))
}

// showBackups answers GET /apps/{id}/backups.
func (s *server) showBackups(w http.ResponseWriter, r *http.Request, session string) {
	s.backupsPage(w, r, http.StatusOK, r.PathValue("id"), backupsTab{})
}

// backUpNow answers POST /apps/{id}/backups: one service's backup, now.
func (s *server) backUpNow(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	_, err := s.svc.BackUpNow(r.Context(), id, r.PostForm.Get("service"))
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/backups", http.StatusSeeOther)
		return
	}
	s.refuseBackup(w, r, id, err, backupsTab{})
}

// setBackupPolicy answers POST /apps/{id}/backups/policy: "Back up daily" on a
// service with something to back up and no policy. It sets the policy the
// service's kind calls for (an engine, 03:00 UTC, the last 7) and deploys, on the
// condition that the app still has the revision the page was drawn from.
func (s *server) setBackupPolicy(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "set the backup")
	if !ok {
		return
	}
	rec, err := s.svc.GetApp(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("service"))
	i, found := serviceNamed(rec.App, name)
	if !found {
		s.backupsPage(w, r, http.StatusUnprocessableEntity, id, backupsTab{Notice: sentence{plain("This app has no service "), mono(name), plain(".")}})
		return
	}
	engine, can := backupEngineFor(rec.App.Services[i])
	if !can {
		s.backupsPage(w, r, http.StatusUnprocessableEntity, id, backupsTab{Notice: sentence{mono(name), plain(" has no database or volume to back up.")}})
		return
	}
	policy := app.Backup{Engine: engine, Schedule: app.DefaultBackupSchedule, Keep: app.DefaultBackupKeep}
	// "Back up a SQLite file instead" (feature 029) names the engine and the
	// file; the model checks the file, and a refusal is drawn as the notice.
	if r.PostForm.Get("engine") == app.BackupSQLite {
		policy.Engine, policy.File = app.BackupSQLite, strings.TrimSpace(r.PostForm.Get("file"))
	}
	deployment, err := s.svc.SetBackupPolicy(ctx, id, name, policy, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}
	edited := rec.App
	edited.Services = slices.Clone(edited.Services)
	edited.Services[i].Backup = &policy
	var fieldErr *app.FieldError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case errors.Is(err, service.ErrAppChanged):
		s.backupsPage(w, r, http.StatusConflict, id, backupsTab{Notice: sentence{plain(changedSincePage)}})
	case errors.As(err, &fieldErr) && fieldErr.Field == "service":
		s.backupsPage(w, r, http.StatusConflict, id, backupsTab{Notice: sentence{plain(fieldErr.Message + ". Reload the page.")}})
	default:
		status, notice, ok := refusalOf(edited, err)
		if !ok {
			s.serverError(w, r, "set a backup policy", err)
			return
		}
		s.backupsPage(w, r, status, id, backupsTab{Notice: notice})
	}
}

// restoreBackup answers POST /apps/{id}/backups/{backupId}/restore. The form
// must carry the service's name as "confirm", exactly.
func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request, session string) {
	id, backupID := r.PathValue("id"), r.PathValue("backupId")
	if !s.readForm(w, r) {
		return
	}
	if r.PostForm.Get("confirm") != serviceOfBackup(backupID) {
		s.backupsPage(w, r, http.StatusUnprocessableEntity, id, backupsTab{ConfirmErr: backupID})
		return
	}
	_, err := s.svc.Restore(r.Context(), id, backupID)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/backups", http.StatusSeeOther)
		return
	}
	s.refuseBackup(w, r, id, err, backupsTab{})
}

// serviceOfBackup is the service a backup ID belongs to: the ID is "<service>-<unix seconds>", and a
// service's name may hold "-" too, so the service ends at the last one.
func serviceOfBackup(backupID string) string {
	if i := strings.LastIndex(backupID, "-"); i >= 0 {
		return backupID[:i]
	}
	return backupID
}

// pinBackup answers POST /apps/{id}/backups/{backupId}/pin (feature 036): the run gets the label of
// the form. The label moves from the backup that had it, as the sentence under Pin… says.
func (s *server) pinBackup(w http.ResponseWriter, r *http.Request, session string) {
	id, backupID := r.PathValue("id"), r.PathValue("backupId")
	if !s.readForm(w, r) {
		return
	}
	if _, err := s.svc.PinBackup(r.Context(), id, backupID, strings.TrimSpace(r.PostForm.Get("label"))); err != nil {
		s.refuseBackup(w, r, id, err, backupsTab{})
		return
	}
	http.Redirect(w, r, "/apps/"+id+"/backups#svc-"+serviceOfBackup(backupID), http.StatusSeeOther)
}

// unpinBackup answers POST /apps/{id}/backups/{backupId}/unpin.
func (s *server) unpinBackup(w http.ResponseWriter, r *http.Request, session string) {
	id, backupID := r.PathValue("id"), r.PathValue("backupId")
	if !s.readForm(w, r) {
		return
	}
	if _, err := s.svc.UnpinBackup(r.Context(), id, backupID); err != nil {
		s.refuseBackup(w, r, id, err, backupsTab{})
		return
	}
	http.Redirect(w, r, "/apps/"+id+"/backups#svc-"+serviceOfBackup(backupID), http.StatusSeeOther)
}

// labelSentence is what a label that isn't one is told.
const labelSentence = "A label is 1 to 32 lowercase letters, digits or hyphens, starting and ending with a letter or digit."

// setRestoreSchedule answers POST /apps/{id}/backups/restore-schedule (feature 036): a service's
// restore schedule is set, or with an empty schedule removed, as a deploy of the stored app on the
// revision the page was drawn from. The schedule and the label are given together or not at all.
func (s *server) setRestoreSchedule(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "set the restore schedule")
	if !ok {
		return
	}
	rec, err := s.svc.GetApp(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("service"))
	i, found := serviceNamed(rec.App, name)
	if !found || rec.App.Services[i].Backup == nil {
		s.backupsPage(w, r, http.StatusUnprocessableEntity, id, backupsTab{Notice: sentence{plain("This app has no backed-up service "), mono(name), plain(".")}})
		return
	}

	var restore *app.RestoreSchedule
	spec, from := strings.TrimSpace(r.PostForm.Get("schedule")), strings.TrimSpace(r.PostForm.Get("from"))
	switch {
	case r.PostForm.Get("stop") != "" || spec == "" && from == "":
		// Stop restoring: "Stop restoring" or an emptied form.
	case spec == "" || from == "":
		s.backupsPage(w, r, http.StatusUnprocessableEntity, id, backupsTab{Notice: sentence{plain("Give both a schedule and a label, or neither.")}})
		return
	case !app.ValidLabel(from):
		s.backupsPage(w, r, http.StatusUnprocessableEntity, id, backupsTab{Notice: sentence{plain(labelSentence)}})
		return
	default:
		restore = &app.RestoreSchedule{Schedule: spec, From: from}
	}

	deployment, err := s.svc.SetRestoreSchedule(ctx, id, name, restore, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}
	edited := rec.App
	edited.Services = slices.Clone(edited.Services)
	policy := *edited.Services[i].Backup
	policy.Restore = restore
	edited.Services[i].Backup = &policy
	var fieldErr *app.FieldError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case errors.Is(err, service.ErrAppChanged):
		s.backupsPage(w, r, http.StatusConflict, id, backupsTab{Notice: sentence{plain(changedSincePage)}})
	case errors.Is(err, service.ErrAppStopped):
		s.backupsPage(w, r, http.StatusConflict, id, backupsTab{Notice: sentence{plain(appStoppedText)}})
	case errors.As(err, &fieldErr) && fieldErr.Field == "service":
		s.backupsPage(w, r, http.StatusConflict, id, backupsTab{Notice: sentence{plain(fieldErr.Message + ". Reload the page.")}})
	case errors.As(err, &fieldErr) && strings.HasSuffix(fieldErr.Field, ".backup.restore.schedule"):
		s.backupsPage(w, r, http.StatusUnprocessableEntity, id, backupsTab{Notice: sentence{plain("The schedule " + fieldErr.Message)}})
	default:
		status, notice, ok := refusalOf(edited, err)
		if !ok {
			s.serverError(w, r, "set a restore schedule", err)
			return
		}
		s.backupsPage(w, r, status, id, backupsTab{Notice: notice})
	}
}

// refuseBackup draws the tab again with what went wrong.
func (s *server) refuseBackup(w http.ResponseWriter, r *http.Request, id string, err error, tab backupsTab) {
	status := http.StatusConflict
	switch {
	case errors.Is(err, service.ErrAppStopped):
		tab.Notice = sentence{plain(appStoppedText)}
	case errors.Is(err, service.ErrBackupRunning):
		tab.Notice = sentence{plain("A backup or restore of this service is running. Try again when it ends.")}
	case errors.Is(err, service.ErrAppBusy):
		tab.Notice = sentence{plain("The app is busy: a deploy or a backup run has it. Try again when it ends.")}
	case errors.Is(err, service.ErrNoRunningCopy):
		tab.Notice = sentence{plain("The service has no running copy. Start it with a deploy first.")}
	case errors.Is(err, service.ErrNoBackupDestination):
		tab.Notice = sentence{plain("No backup destination is set. Set one in Settings first.")}
	case errors.Is(err, store.ErrNotFound):
		if _, appErr := s.svc.GetApp(r.Context(), id); errors.Is(appErr, store.ErrNotFound) {
			s.appNotFound(w, r, id)
			return
		}
		status = http.StatusNotFound
		tab.Notice = sentence{plain(err.Error())}
	default:
		var fe *app.FieldError
		if errors.As(err, &fe) {
			status, tab.Notice = http.StatusUnprocessableEntity, sentence{plain(fe.Message)}
			break
		}
		s.log.Warn("backup from the console failed", "app", id, "error", err)
		status, tab.Notice = http.StatusBadGateway, sentence{plain("Nomad couldn't be reached or refused it: " + err.Error())}
	}
	s.backupsPage(w, r, status, id, tab)
}
