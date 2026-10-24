package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Schedules (feature 022): the workspace page /schedules (every schedule as a
// row that opens, with its runs as marks and bars), the run's page with its
// output, and the posts that run, pause, resume, delete and stop. The form is
// in schedulesform.go. Every time on these pages is a clock time in UTC
// (view.go's clockTime); localtime.js adds the viewer's local time beside it.
//
// A run's output is drawn only on its own page, which is never cached: no row,
// title, notice or live event holds it.

const (
	// stripRuns is how many runs a row's strip and bars show.
	stripRuns = 14
	// barSteps is how many heights a run's bar can have: 4px each, 56px the tallest.
	barSteps = 14
	// sameWayRuns is how many of the newest ended runs the failure count looks at.
	sameWayRuns = 4
)

// schedulesPage is a page of the schedules template set: the list, a run's
// page or the form; exactly one is set.
type schedulesPage struct {
	pageData
	List *schedListView
	Run  *runView
	Form *schedForm
}

// schedListView is the Schedules list: the head, the filter, a notice and the
// live region.
type schedListView struct {
	Filter schedFilterView
	Notice sentence
	// Alert is a refused post (409, 502), drawn above the table.
	Alert sentence
	// NewHref is New schedule, with the app the list is filtered to.
	NewHref string
	schedulesRegion
}

// schedulesRegion is the live region: the count line, the table (or the empty
// state) and the footer. /parts/schedules draws it alone.
type schedulesRegion struct {
	Count       countLine
	Rows        []schedRow
	Empty       *emptyView
	NoMatch     bool
	ClearHref   string
	Channels    bool // a notification channel exists: the footer says failed runs reach it
	RefreshHref string
}

// countLine is "7 schedules · 1 failing · next run 14:15 UTC".
type countLine struct {
	Total   string
	Failing string
	// Next is the earliest next run of the rows that aren't paused; nil is
	// "no runs scheduled".
	Next *clockTime
}

// schedFilterView is the filter row: a project select (only with more than
// one project), an app select, Apply and Clear.
type schedFilterView struct {
	Projects []selectOption
	Apps     []selectOption
	Clear    bool
}

// schedRow is one schedule: the summary row and its detail.
type schedRow struct {
	ID     string
	Anchor string // "schedule-<id>", the <details>'s stable id
	Name   string // "rates-sync", or "db backup" for a built-in
	Open   bool
	// Mark is the row's mark: rolling while a run goes, idle when paused,
	// else the newest run's (healthy, failed, warning for a skipped one, idle
	// for none or one stopped by the owner).
	Mark  string
	State string // what a screen reader hears first: "rates-sync, last run failed"
	Pill  *statusView
	// App, Service and Command make the line under the name: "booking-api ·
	// web · node scripts/sync-rates.js". Service is set only when the app has several.
	App, Service, Command string
	Words                 sentence // "Every 15 minutes", or "On `5 4 * * 1`"
	Cron                  string
	Paused                bool
	Next                  *clockTime
	NextText              string // "paused", "—": what stands where there is no next run
	Last                  lastRunView
	Strip                 []stripMark
	Detail                schedDetail
}

// lastRunView is the Last run column: a run going ("started 14:00:02 UTC" over
// a bar), or the newest run's time over how long it took.
type lastRunView struct {
	Running bool
	Started clockTime
	// BarPct is how far through its usual duration the run is (0-100); HasBar
	// is false when there is no usual duration yet.
	BarPct int
	HasBar bool
	Never  bool
	When   clockTime
	Failed bool
	Note   string // "took 5 min", "stopped at 5 min"
}

// stripMark is one run in the row's strip: its mark, linking to its page.
type stripMark struct {
	Mark, Href, Title, Word string
}

// runBar is one run in the detail's "How long each run took": Kind is ok,
// failed, warn (stopped at the limit), skipped (a stub) or running; Step is its
// height in 4px steps (1-14).
type runBar struct {
	Kind  string
	Step  int
	Title string
}

// fact is one line of the detail's facts grid.
type fact struct {
	Key   string
	Value sentence
	Break bool // the value breaks anywhere (a command)
}

// schedDetail is what opens under a row.
type schedDetail struct {
	ID         string
	State      sentence
	StateQuiet bool // paused, a skip note: fg-2
	Bars       []runBar
	Usual      string // "usually 48 s · stops at 5 min"
	Facts      []fact
	Builtin    bool
	// The buttons. StopRun is the run "Stop this run" stops ("" when none goes);
	// RunNow is offered when no run goes, primary when the last run failed.
	StopRun     string
	RunNow      bool
	RunPrimary  bool
	LogsHref    string
	Paused      bool
	EditHref    string
	BackupsHref string // a built-in's "Change in Backups"
}

// ---- Building the rows (pure) --------------------------------------------------------

// runMarkOf is a run's mark: a disc for ok, a square for a failure, a
// diamond for a skipped slot, a ring for one stopped by the owner, a half disc
// while it goes.
func runMarkOf(status string) string {
	switch status {
	case store.RunOK:
		return "healthy"
	case store.RunFailed, store.RunStoppedAtLimit, store.RunCouldNotStart:
		return "failed"
	case store.RunSkipped:
		return "warning"
	case store.RunStopped:
		return "idle"
	}
	return "rolling"
}

// runFailed says a run counts as failed: what the row's pill, its sort and the
// needs-you list call a failed last run.
func runFailed(status string) bool {
	return status == store.RunFailed || status == store.RunStoppedAtLimit || status == store.RunCouldNotStart
}

// runOpen says a run is going.
func runOpen(status string) bool { return status == store.RunStarting || status == store.RunRunning }

// runWord is a run's outcome in a word or two, for a mark's title.
func runWord(status string) string {
	switch status {
	case store.RunOK:
		return "ok"
	case store.RunFailed:
		return "failed"
	case store.RunStoppedAtLimit:
		return "stopped at the limit"
	case store.RunCouldNotStart:
		return "couldn't start"
	case store.RunSkipped:
		return "skipped"
	case store.RunStopped:
		return "stopped"
	}
	return "running"
}

// scheduleName is how a schedule is named on the page: a built-in is
// "<service> backup".
func scheduleName(sc service.Schedule) string {
	if sc.Builtin {
		return sc.Service + " backup"
	}
	return sc.Name
}

// scheduleHref is the page a schedule's row is on, opened.
func scheduleHref(id string) string {
	return "/schedules?open=" + url.QueryEscape(id) + "#schedule-" + id
}

// runHref is where a run's mark links: its own page, or for a built-in's run
// (a backup), its row on the app's Backups tab.
func runHref(sc service.Schedule, runID string) string {
	if sc.Builtin {
		return "/apps/" + url.PathEscape(sc.App) + "/backups#backup-" + runID
	}
	return "/schedules/" + url.PathEscape(sc.ID) + "/runs/" + url.PathEscape(runID)
}

// commandLine is a command as one line a shell would read back the same: words
// with spaces or quotes in single quotes.
func commandLine(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		if w != "" && !strings.ContainsAny(w, " \t\n'\"\\$`|&;<>()*?[]{}#~") {
			out[i] = w
			continue
		}
		out[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}

// builtinCommand is a built-in schedule's command in words: "pg_dump →
// backups", "volumes → backups"; bucket is the backup destination's ("" when
// none is set).
func builtinCommand(engine, bucket string) string {
	what := "pg_dump"
	if engine == app.BackupVolume {
		what = "volumes"
	}
	if bucket == "" {
		return what + ", no destination set"
	}
	return what + " → " + bucket
}

// wordsOf is a schedule's words as a sentence: "On `<cron>`" sets the cron in mono.
func wordsOf(words string) sentence {
	if rest, ok := strings.CutPrefix(words, "On `"); ok {
		return sentence{plain("On "), mono(strings.TrimSuffix(rest, "`"))}
	}
	return sentence{plain(words)}
}

// memoryText is "512 MB" or "1 GB".
func memoryText(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

// placeText is "booking / production".
func placeText(sc service.Schedule) string { return sc.Project + " / " + sc.Environment }

// appText is the app as a schedule's row names it: its name, which is its ID
// in Default › production.
func appText(sc service.Schedule) string {
	if sc.AppName != "" {
		return sc.AppName
	}
	return sc.App
}

// runDuration is how long a run took, or has been going; false for a skipped
// slot.
func runDuration(now time.Time, r service.ScheduleRun) (time.Duration, bool) {
	switch {
	case r.Status == store.RunSkipped:
		return 0, false
	case r.DurationSeconds != nil:
		return time.Duration(*r.DurationSeconds) * time.Second, true
	case runOpen(r.Status):
		from := r.StartedAt
		if r.TaskStartedAt != nil {
			from = *r.TaskStartedAt
		}
		return max(now.Sub(from), 0), true
	}
	return 0, false
}

// runSentence is what one run came to, in a sentence (the detail's state and
// the run page's): "Exited with code 1.", "Stopped at the 5 min limit.",
// "Finished in 48 s.", "Skipped: the run before was still going.",
// "Couldn't start: Nomad refused the job.".
func runSentence(now time.Time, sc service.Schedule, r service.ScheduleRun) sentence {
	switch r.Status {
	case store.RunOK:
		if d, ok := runDuration(now, r); ok {
			return plainS("Finished in " + durationText(d) + ".")
		}
		return plainS("Finished.")
	case store.RunStoppedAtLimit:
		return plainS("Stopped at the " + durationText(time.Duration(sc.StopAfterSeconds)*time.Second) + " limit.")
	case store.RunFailed:
		switch {
		case r.ExitCode != nil:
			return plainS(fmt.Sprintf("Exited with code %d.", *r.ExitCode))
		case sc.Builtin:
			if c := causeOf(r.Error); c != "" {
				return plainS("The backup failed. " + c)
			}
			return plainS("The backup failed.")
		case r.Error != "":
			return plainS(upperFirst(strings.TrimSuffix(r.Error, ".")) + ".")
		}
		return plainS("Failed.")
	case store.RunCouldNotStart:
		reason := r.Error
		if reason == "" || reason == "could not start" {
			reason = "Nomad couldn't start its copy"
		}
		return join(plainS("Couldn't start: "), backticked(strings.TrimSuffix(reason, ".")), plainS("."))
	case store.RunSkipped:
		reason := r.Error
		if reason == "" {
			reason = "the run before was still going"
		}
		return plainS("Skipped: " + reason + ".")
	case store.RunStopped:
		switch r.Error {
		case "", "stopped by the owner":
			return plainS("Stopped by you.")
		}
		return plainS("Stopped: " + strings.TrimSuffix(r.Error, ".") + ".")
	case store.RunStarting:
		return plainS("Starting.")
	}
	if d, ok := runDuration(now, r); ok {
		return plainS("Running for " + durationText(d) + ".")
	}
	return plainS("Running.")
}

// backticked is text in which the service wrote a program between backticks
// ("no `sh` in the image"), as a sentence that draws the program in mono.
func backticked(text string) sentence {
	var out sentence
	for {
		before, rest, found := strings.Cut(text, "`")
		if !found {
			break
		}
		code, after, closed := strings.Cut(rest, "`")
		if !closed {
			break
		}
		if before != "" {
			out = append(out, plain(before))
		}
		if code != "" {
			out = append(out, mono(code))
		}
		text = after
	}
	if text != "" {
		out = append(out, plain(text))
	}
	return out
}

// detailState is the sentence that opens a row's detail, and whether it is
// quiet (fg-2). runs are the newest 14, newest first.
func detailState(now time.Time, sc service.Schedule, runs []service.ScheduleRun) (sentence, bool) {
	if sc.Builtin {
		return plainS("Made by Backups. Change the time or how many are kept in the app's Backups tab."), false
	}
	if i := slices.IndexFunc(runs, func(r service.ScheduleRun) bool { return runOpen(r.Status) }); i >= 0 {
		d, _ := runDuration(now, runs[i])
		s := "Running for " + durationText(d) + "."
		if sc.UsualSeconds != nil {
			s += " Usually takes " + durationText(time.Duration(*sc.UsualSeconds)*time.Second) + "."
		}
		return plainS(s), false
	}
	if sc.Paused && sc.PausedAt != nil {
		return plainS("Paused " + relative(now, *sc.PausedAt) + "."), true
	}
	if last := sc.LastRun; last != nil && runFailed(last.Status) {
		cause := runSentence(now, sc, *last)
		same, ended := 0, 0
		for _, r := range runs {
			if r.Status == store.RunSkipped || runOpen(r.Status) {
				continue
			}
			if ended == sameWayRuns {
				break
			}
			ended++
			if runFailed(r.Status) && runSentence(now, sc, r).String() == cause.String() {
				same++
			}
		}
		if same >= 2 {
			cause = join(cause, plainS(fmt.Sprintf(" %d of the last %d runs failed the same way.", same, ended)))
		}
		return cause, false
	}
	if skips := skippedRuns(runs); len(skips) > 0 {
		newest := skips[0]
		reason := newest.Error
		if reason == "" {
			reason = "the run before was still going"
		}
		times := "once,"
		if len(skips) > 1 {
			times = fmt.Sprintf("%d times, the last", len(skips))
		}
		return plainS(fmt.Sprintf("Skipped %s %s, because %s.", times, relative(now, newest.StartedAt), reason)), true
	}
	if last := sc.LastRun; last != nil {
		if last.Status == store.RunOK {
			if d, ok := runDuration(now, *last); ok {
				return plainS("The last run finished in " + durationText(d) + "."), false
			}
			return plainS("The last run finished."), false
		}
		return join(plainS("The last run: "), lowerSentence(runSentence(now, sc, *last))), false
	}
	if sc.Paused {
		return plainS("Paused."), true
	}
	return plainS("It hasn't run yet."), true
}

// lowerSentence is s with its first letter in lower case.
func lowerSentence(s sentence) sentence {
	if len(s) == 0 || s[0].Mono {
		return s
	}
	out := slices.Clone(s)
	if t := out[0].Text; t != "" {
		out[0].Text = strings.ToLower(t[:1]) + t[1:]
	}
	return out
}

// skippedRuns are the skipped slots among runs, newest first.
func skippedRuns(runs []service.ScheduleRun) []service.ScheduleRun {
	var out []service.ScheduleRun
	for _, r := range runs {
		if r.Status == store.RunSkipped {
			out = append(out, r)
		}
	}
	return out
}

// runBars are the runs as bars, oldest first: a bar's height is its duration
// against the longest of them, at least one step; a skipped slot is a stub.
func runBars(now time.Time, runs []service.ScheduleRun) []runBar {
	var longest time.Duration
	for _, r := range runs {
		if d, ok := runDuration(now, r); ok && d > longest {
			longest = d
		}
	}
	out := make([]runBar, 0, len(runs))
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		when := clockOf(now, r.StartedAt).Text
		bar := runBar{Kind: "ok", Step: 1}
		switch {
		case r.Status == store.RunSkipped:
			bar.Kind, bar.Title = "skipped", when+" · skipped"
			out = append(out, bar)
			continue
		case runOpen(r.Status):
			bar.Kind = "running"
		case r.Status == store.RunStoppedAtLimit:
			bar.Kind = "warn"
		case runFailed(r.Status):
			bar.Kind = "failed"
		}
		d, _ := runDuration(now, r)
		if longest > 0 {
			bar.Step = min(max(int((d*barSteps+longest/2)/longest), 1), barSteps)
		}
		bar.Title = when + " · " + durationText(d)
		if bar.Kind != "ok" {
			bar.Title += " · " + runWord(r.Status)
		}
		out = append(out, bar)
	}
	return out
}

// scheduleRowOf builds one schedule's row. runs are its newest 14, newest
// first; bucket is the backup destination's ("" for none); open says the row
// is drawn open.
func scheduleRowOf(now time.Time, sc service.Schedule, runs []service.ScheduleRun, several bool, bucket string, open bool) schedRow {
	row := schedRow{ID: sc.ID, Anchor: "schedule-" + sc.ID, Name: scheduleName(sc), Open: open, App: appText(sc),
		Words: wordsOf(sc.Words), Cron: sc.Cron, Paused: sc.Paused}
	if several {
		row.Service = sc.Service
	}
	if sc.Builtin {
		row.Command = builtinCommand(sc.Engine, bucket)
	} else {
		row.Command = commandLine(sc.Command)
	}

	// The mark and the pill.
	switch {
	case sc.RunningNow > 0:
		row.Mark, row.Pill = "rolling", &statusView{Kind: "rolling", Word: "running now"}
	case sc.Paused:
		row.Mark, row.Pill = "idle", &statusView{Kind: "idle", Word: "paused"}
	case len(runs) == 0:
		row.Mark = "idle"
	default:
		row.Mark = runMarkOf(runs[0].Status)
	}
	if row.Pill == nil && sc.LastRun != nil && runFailed(sc.LastRun.Status) {
		row.Pill = &statusView{Kind: "failed", Word: "last run failed"}
	}
	switch {
	case row.Pill != nil:
		row.State = row.Name + ", " + row.Pill.Word
	case len(runs) == 0:
		row.State = row.Name + ", never run"
	default:
		row.State = row.Name + ", last run " + runWord(runs[0].Status)
	}

	// Next.
	switch {
	case sc.Paused:
		row.NextText = "paused"
	case sc.NextRunAt != nil:
		row.Next = new(clockOf(now, *sc.NextRunAt))
	default:
		row.NextText = "—"
	}

	// Last run.
	if i := slices.IndexFunc(runs, func(r service.ScheduleRun) bool { return runOpen(r.Status) }); i >= 0 {
		r := runs[i]
		row.Last = lastRunView{Running: true, Started: clockSecondsOf(now, r.StartedAt)}
		if sc.UsualSeconds != nil && *sc.UsualSeconds > 0 {
			d, _ := runDuration(now, r)
			row.Last.HasBar = true
			row.Last.BarPct = min(int(d*100/(time.Duration(*sc.UsualSeconds)*time.Second)), 100)
		}
	} else if last := sc.LastRun; last != nil {
		row.Last = lastRunView{When: clockOf(now, last.StartedAt), Failed: runFailed(last.Status)}
		d, ok := runDuration(now, *last)
		switch {
		case last.Status == store.RunStoppedAtLimit:
			row.Last.Note = "stopped at " + durationText(time.Duration(sc.StopAfterSeconds)*time.Second)
		case ok && last.Status != store.RunCouldNotStart:
			row.Last.Note = "took " + durationText(d)
		case last.Status == store.RunCouldNotStart:
			row.Last.Note = "couldn't start"
		}
	} else {
		row.Last.Never = true
	}

	// The strip, oldest first.
	for i := min(len(runs), stripRuns) - 1; i >= 0; i-- {
		r := runs[i]
		title := clockOf(now, r.StartedAt).Text + " · " + runWord(r.Status)
		if d, ok := runDuration(now, r); ok && !runOpen(r.Status) {
			title += " · " + durationText(d)
		}
		row.Strip = append(row.Strip, stripMark{Mark: runMarkOf(r.Status), Href: runHref(sc, r.ID), Title: title, Word: runWord(r.Status)})
	}

	row.Detail = detailOf(now, sc, runs, bucket)
	return row
}

// detailOf builds what opens under a schedule's row.
func detailOf(now time.Time, sc service.Schedule, runs []service.ScheduleRun, bucket string) schedDetail {
	d := schedDetail{ID: sc.ID, Builtin: sc.Builtin, Paused: sc.Paused}
	d.State, d.StateQuiet = detailState(now, sc, runs)
	d.Bars = runBars(now, runs[:min(len(runs), stripRuns)])

	// "no runs yet" only when there are none; runs that never finished ok leave the usual time unknown.
	usual := "no runs yet"
	if len(runs) > 0 || sc.LastRun != nil {
		usual = "no completed runs"
	}
	if sc.UsualSeconds != nil {
		usual = "usually " + durationText(time.Duration(*sc.UsualSeconds)*time.Second)
	}
	if !sc.Builtin {
		usual += " · stops at " + durationText(time.Duration(sc.StopAfterSeconds)*time.Second)
	}
	d.Usual = usual

	runsIn := sentence{mono(sc.Service), plain(" of "), mono(appText(sc))}
	if sc.Builtin {
		d.Facts = []fact{
			{Key: "command", Value: plainS(builtinCommand(sc.Engine, bucket))},
			{Key: "runs in", Value: runsIn},
			{Key: "project", Value: plainS(placeText(sc))},
		}
		d.BackupsHref = "/apps/" + url.PathEscape(sc.App) + "/backups#svc-" + sc.Service
	} else {
		still := "skip the next run"
		if !sc.SkipIfRunning {
			still = "start another"
		}
		d.Facts = []fact{
			{Key: "command", Value: plainS(commandLine(sc.Command)), Break: true},
			{Key: "runs in", Value: runsIn},
			{Key: "stops after", Value: plainS(durationText(time.Duration(sc.StopAfterSeconds) * time.Second))},
			{Key: "if still running", Value: plainS(still)},
			{Key: "memory", Value: plainS(memoryText(sc.MemoryMB))},
			{Key: "project", Value: plainS(placeText(sc))},
		}
		d.EditHref = "/schedules/" + url.PathEscape(sc.ID) + "/edit"
		if i := slices.IndexFunc(runs, func(r service.ScheduleRun) bool { return runOpen(r.Status) }); i >= 0 {
			d.StopRun = runs[i].ID
		} else {
			d.RunNow = true
			d.RunPrimary = sc.LastRun != nil && runFailed(sc.LastRun.Status)
		}
	}
	if sc.LastRun != nil {
		d.LogsHref = runHref(sc, sc.LastRun.ID)
	}
	return d
}

// countOf is the count line of the listed schedules.
func countOf(now time.Time, list []service.Schedule) countLine {
	c := countLine{Total: plural(len(list), "schedule"), Failing: "none failing"}
	failing := 0
	var next *time.Time
	for _, sc := range list {
		if sc.LastRun != nil && runFailed(sc.LastRun.Status) {
			failing++
		}
		if !sc.Paused && sc.NextRunAt != nil && (next == nil || sc.NextRunAt.Before(*next)) {
			next = sc.NextRunAt
		}
	}
	if failing > 0 {
		c.Failing = fmt.Sprintf("%d failing", failing)
	}
	if next != nil {
		c.Next = new(clockOf(now, *next))
	}
	return c
}

// ---- Loading --------------------------------------------------------------------------

// schedulesQuery is what ?project=, ?app= and ?open= (or ?created=) ask for.
type schedulesQuery struct {
	Project, App, Open string
}

func schedulesQueryOf(q url.Values) schedulesQuery {
	open := q.Get("open")
	if c := q.Get("created"); c != "" {
		open = c
	}
	return schedulesQuery{Project: strings.TrimSpace(q.Get("project")), App: strings.TrimSpace(q.Get("app")), Open: open}
}

// filterQuery is the query string of the filter alone ("" for none).
func (q schedulesQuery) filterQuery() string {
	v := url.Values{}
	if q.Project != "" {
		v.Set("project", q.Project)
	}
	if q.App != "" {
		v.Set("app", q.App)
	}
	return v.Encode()
}

// loadSchedules reads the schedules the filter keeps, with their runs, and
// builds the region.
func (s *server) loadSchedules(ctx context.Context, q schedulesQuery) (schedulesRegion, error) {
	list, err := s.svc.ListSchedules(ctx, service.ScheduleFilter{App: q.App, Project: q.Project})
	if err != nil {
		return schedulesRegion{}, err
	}
	bucket := ""
	if dest, err := s.svc.GetBackupDestination(ctx); err == nil {
		bucket = dest.Bucket
	} else if !errors.Is(err, store.ErrNotFound) {
		return schedulesRegion{}, err
	}
	apps, err := s.svc.ListApps(ctx)
	if err != nil {
		return schedulesRegion{}, err
	}
	services := map[string]int{}
	for _, a := range apps {
		services[a.App.ID] = len(a.App.Services)
	}
	channels, err := s.svc.NotificationChannels(ctx)
	if err != nil {
		return schedulesRegion{}, err
	}

	now := s.now()
	region := schedulesRegion{Count: countOf(now, list), Channels: len(channels) > 0, RefreshHref: "/parts/schedules"}
	if fq := q.filterQuery(); fq != "" {
		region.RefreshHref += "?" + fq
	}
	explicit := q.Open != "" && slices.ContainsFunc(list, func(sc service.Schedule) bool { return sc.ID == q.Open })
	for i, sc := range list {
		runs, err := s.svc.ListScheduleRuns(ctx, sc.ID, stripRuns)
		if err != nil {
			return schedulesRegion{}, err
		}
		// The first row opens by itself when its last run failed, unless the
		// address asks for another.
		open := sc.ID == q.Open || !explicit && i == 0 && sc.LastRun != nil && runFailed(sc.LastRun.Status)
		region.Rows = append(region.Rows, scheduleRowOf(now, sc, runs, services[sc.App] > 1, bucket, open))
	}
	if len(list) == 0 {
		if q.filterQuery() != "" {
			region.NoMatch, region.ClearHref = true, "/schedules"
		} else {
			region.Empty = &emptyView{Title: "No schedules yet.",
				Text:        "A schedule runs a command in a fresh copy of an app on a timer, then stops.",
				ActionLabel: "New schedule", ActionHref: "/schedules/new"}
		}
	}
	return region, nil
}

// schedulesFilterOf builds the filter row: the project select only when there
// is more than one project, the app select with every app (of the project
// when one is chosen).
func (s *server) schedulesFilterOf(ctx context.Context, q schedulesQuery) (schedFilterView, error) {
	projects, err := s.svc.Projects(ctx)
	if err != nil {
		return schedFilterView{}, err
	}
	apps, err := s.svc.ListApps(ctx)
	if err != nil {
		return schedFilterView{}, err
	}
	f := schedFilterView{Clear: q.filterQuery() != ""}
	if len(projects) > 1 {
		f.Projects = append(f.Projects, selectOption{Value: "", Label: "All projects", Selected: q.Project == ""})
		for _, p := range projects {
			f.Projects = append(f.Projects, selectOption{Value: p.ID, Label: p.DisplayName, Selected: q.Project == p.ID})
		}
	}
	f.Apps = append(f.Apps, selectOption{Value: "", Label: "All apps", Selected: q.App == ""})
	for _, a := range apps {
		if q.Project != "" && a.Place.Project != q.Project {
			continue
		}
		f.Apps = append(f.Apps, selectOption{Value: a.App.ID, Label: appLabel(a.AppRecord), Selected: q.App == a.App.ID})
	}
	return f, nil
}

// appLabel is an app as a select names it: its ID in Default › production,
// else "project / environment / name".
func appLabel(rec store.AppRecord) string {
	p := rec.Place
	if p.Name == "" || p.Project == app.DefaultProject && p.Environment == app.DefaultEnvironment {
		return rec.App.ID
	}
	return p.Project + " / " + p.Environment + " / " + p.Name
}

// ---- Handlers -------------------------------------------------------------------------

// showSchedules answers GET /schedules.
func (s *server) showSchedules(w http.ResponseWriter, r *http.Request, session string) {
	q := schedulesQueryOf(r.URL.Query())
	var notice sentence
	switch {
	case r.URL.Query().Get("created") != "":
		if sc, err := s.svc.GetSchedule(r.Context(), r.URL.Query().Get("created")); err == nil {
			notice = sentence{mono(sc.Name), plain(" was created.")}
			if sc.NextRunAt != nil {
				notice = join(notice, plainS(" Its first run is at "+clockOf(s.now(), *sc.NextRunAt).Text+"."))
			}
		}
	case r.URL.Query().Get("saved") != "":
		if sc, err := s.svc.GetSchedule(r.Context(), r.URL.Query().Get("saved")); err == nil {
			notice = sentence{mono(sc.Name), plain(" was saved.")}
			q.Open = sc.ID
		}
	case labelName.MatchString(r.URL.Query().Get("deleted")):
		notice = sentence{mono(r.URL.Query().Get("deleted")), plain(" was deleted, with its runs.")}
	}
	s.renderSchedules(w, r, http.StatusOK, q, notice, nil)
}

// renderSchedules draws the page, with a notice or a refusal.
func (s *server) renderSchedules(w http.ResponseWriter, r *http.Request, status int, q schedulesQuery, notice, alert sentence) {
	ctx := r.Context()
	region, err := s.loadSchedules(ctx, q)
	if err != nil {
		s.serverError(w, "list schedules", err)
		return
	}
	filter, err := s.schedulesFilterOf(ctx, q)
	if err != nil {
		s.serverError(w, "list apps", err)
		return
	}
	head, err := s.shellPage(r, "Schedules", navSchedules, crumb{Label: "Schedules"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live"
	list := &schedListView{Filter: filter, Notice: notice, Alert: alert, NewHref: "/schedules/new", schedulesRegion: region}
	if q.App != "" {
		list.NewHref += "?app=" + url.QueryEscape(q.App)
	}
	s.render(w, status, "schedules", schedulesPage{pageData: head, List: list})
}

// partSchedules answers GET /parts/schedules: the count line, the table and
// the footer, with the filter of the page.
func (s *server) partSchedules(w http.ResponseWriter, r *http.Request, session string) {
	q := schedulesQueryOf(r.URL.Query())
	q.Open = ""
	region, err := s.loadSchedules(r.Context(), q)
	if err != nil {
		s.log.Error("list schedules", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "schedules", "schedules-region", region)
}

// ---- The run's page -------------------------------------------------------------------

// outLine is one line of a run's output.
type outLine struct {
	Text   string
	Stderr bool
}

// runView is a run's own page: what it came to, and its output.
type runView struct {
	Name     string
	Title    clockTime
	Sentence sentence
	Started  clockTime
	Took     string
	Service  string
	App      string
	Version  string
	Running  bool
	Lines    []outLine
	// Cut is the line that says the start of the output was dropped; "" when nothing was.
	Cut string
	// Empty is what stands in the output's place: "No output.", or why there is none.
	Empty string
}

// outputLines splits a stream's output into lines; the last newline ends a
// line, it doesn't start an empty one.
func outputLines(out []byte, stderr bool) []outLine {
	text := strings.TrimSuffix(string(out), "\n")
	if text == "" {
		return nil
	}
	var lines []outLine
	for l := range strings.SplitSeq(text, "\n") {
		lines = append(lines, outLine{Text: strings.TrimSuffix(l, "\r"), Stderr: stderr})
	}
	return lines
}

// cutLine says which streams lost their start.
func cutLine(stdout, stderr bool) string {
	which := ""
	switch {
	case stdout && stderr:
		which = "stdout and stderr"
	case stdout:
		which = "stdout"
	case stderr:
		which = "stderr"
	default:
		return ""
	}
	return "The first lines of " + which + " were dropped; the last 64 KiB of each stream is kept."
}

// showRun answers GET /schedules/{id}/runs/{runId}. A built-in schedule's run
// is a backup: its page is the app's Backups tab.
func (s *server) showRun(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id, runID := r.PathValue("id"), r.PathValue("runId")
	sc, err := s.svc.GetSchedule(ctx, id)
	if errors.Is(err, service.ErrScheduleNotFound) {
		s.scheduleNotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "read schedule", err)
		return
	}
	if sc.Builtin {
		http.Redirect(w, r, runHref(sc, runID), http.StatusSeeOther)
		return
	}
	run, err := s.svc.GetScheduleRun(ctx, id, runID)
	if errors.Is(err, service.ErrRunNotFound) {
		s.message(w, http.StatusNotFound, "Not found", "That run isn't kept.",
			"A schedule keeps its last 100 runs. Go back to Schedules to see the others.")
		return
	}
	if err != nil {
		s.serverError(w, "read run", err)
		return
	}

	now := s.now()
	page := runView{Name: sc.Name, Title: clockTitleOf(now, run.StartedAt), Sentence: runSentence(now, sc, run),
		Started: clockSecondsOf(now, run.StartedAt), Service: sc.Service, App: appText(sc), Running: runOpen(run.Status)}
	if d, ok := runDuration(now, run); ok && !page.Running {
		page.Took = durationText(d)
	}
	if run.JobVersion != nil {
		page.Version = fmt.Sprintf("v%d", *run.JobVersion)
	}
	switch {
	case page.Running:
		page.Empty = "The output is kept when the run ends."
	case run.Status == store.RunSkipped || run.Status == store.RunCouldNotStart:
		page.Empty = "No output: the command didn't run."
	case !run.OutputKept:
		page.Empty = "No output was kept: Caravana couldn't read it before the copy was gone."
	default:
		stdout, cutOut, err := s.svc.GetScheduleRunOutput(ctx, id, runID, "stdout")
		if err != nil && !errors.Is(err, service.ErrOutputNotKept) {
			s.serverError(w, "read run output", err)
			return
		}
		stderr, cutErr, err := s.svc.GetScheduleRunOutput(ctx, id, runID, "stderr")
		if err != nil && !errors.Is(err, service.ErrOutputNotKept) {
			s.serverError(w, "read run output", err)
			return
		}
		page.Lines = append(outputLines(stdout, false), outputLines(stderr, true)...)
		page.Cut = cutLine(cutOut, cutErr)
		if len(page.Lines) == 0 {
			page.Empty = "No output."
		}
	}

	head, err := s.shellPage(r, sc.Name+" run", navSchedules, crumb{Label: "Schedules", Href: "/schedules"}, crumb{Label: sc.Name})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	// The crumb names the schedule, and leads back to its row (a run has no
	// crumb of its own: the title says which it is).
	head.Shell.Crumbs[len(head.Shell.Crumbs)-1].Href = scheduleHref(sc.ID)
	s.render(w, http.StatusOK, "schedules", schedulesPage{pageData: head, Run: &page})
}

// scheduleNotFound is the page for a schedule that doesn't exist (any more).
func (s *server) scheduleNotFound(w http.ResponseWriter, r *http.Request) {
	s.message(w, http.StatusNotFound, "Not found", "That schedule doesn't exist.",
		"It may have been deleted, or its app was. Go back to Schedules.")
}

// ---- The posts ------------------------------------------------------------------------

// runSchedule answers POST /schedules/{id}/run: Run now.
func (s *server) runSchedule(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	_, err := s.svc.RunScheduleNow(r.Context(), id)
	s.afterSchedulePost(w, r, id, err)
}

// pauseSchedule answers POST /schedules/{id}/pause.
func (s *server) pauseSchedule(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	_, err := s.svc.PauseSchedule(r.Context(), id)
	s.afterSchedulePost(w, r, id, err)
}

// resumeSchedule answers POST /schedules/{id}/resume.
func (s *server) resumeSchedule(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	_, err := s.svc.ResumeSchedule(r.Context(), id)
	s.afterSchedulePost(w, r, id, err)
}

// stopScheduleRun answers POST /schedules/{id}/runs/{runId}/stop: Stop this run.
func (s *server) stopScheduleRun(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	_, err := s.svc.StopScheduleRun(r.Context(), id, r.PathValue("runId"))
	s.afterSchedulePost(w, r, id, err)
}

// deleteSchedule answers POST /schedules/{id}/delete: the schedule goes, with
// its runs, and the page says so.
func (s *server) deleteSchedule(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	sc, err := s.svc.GetSchedule(r.Context(), id)
	if err == nil {
		err = s.svc.DeleteSchedule(r.Context(), id)
	}
	if err != nil {
		s.afterSchedulePost(w, r, id, err)
		return
	}
	http.Redirect(w, r, "/schedules?deleted="+url.QueryEscape(sc.Name), http.StatusSeeOther)
}

// afterSchedulePost answers a post on a schedule: back to its row, open, when
// it worked (303), else the list again with the reason (409 for a refusal,
// 502 when Nomad couldn't be used), never a redirect.
func (s *server) afterSchedulePost(w http.ResponseWriter, r *http.Request, id string, err error) {
	if err == nil {
		http.Redirect(w, r, scheduleHref(id), http.StatusSeeOther)
		return
	}
	if errors.Is(err, service.ErrScheduleNotFound) {
		s.scheduleNotFound(w, r)
		return
	}
	name := id
	if sc, gerr := s.svc.GetSchedule(r.Context(), id); gerr == nil {
		name = scheduleName(sc)
	}
	status, alert := http.StatusConflict, sentence(nil)
	switch {
	case errors.Is(err, service.ErrRunGoing):
		alert = sentence{plain("A run of "), mono(name), plain(" is going, and it skips a run while one goes (or 3 are going). Stop it first, or wait for it to end.")}
	case errors.Is(err, service.ErrAppStopped):
		alert = sentence{plain("The app of "), mono(name), plain(" is stopped: start it first.")}
	case errors.Is(err, service.ErrNoLiveDeployment):
		alert = sentence{mono(name), plain(" can't run now: its app has no live version (it was never deployed healthy, or a deploy is going). Deploy it, or wait for the deploy to end.")}
	case errors.Is(err, service.ErrScheduleBuiltin):
		alert = sentence{mono(name), plain(" is made by Backups: change it in the app's Backups tab.")}
	case errors.Is(err, service.ErrRunNotRunning), errors.Is(err, service.ErrRunNotFound):
		alert = sentence{plain("That run of "), mono(name), plain(" has already ended.")}
	default:
		s.log.Error("change a schedule", "schedule", id, "error", err)
		status = http.StatusBadGateway
		alert = sentence{plain("Caravana couldn't do that to "), mono(name), plain(": Nomad couldn't be reached, or refused. Nothing was changed; try again.")}
	}
	s.renderSchedules(w, r, status, schedulesQuery{Open: id}, nil, alert)
}
