package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The run output's sentinel: it must be on the run's page and on no other page.
const runSentinel = "s3cret-sentinel-022-output"

// schedEnv has whoami (one service, healthy at v7) and blog (two services) to
// run schedules in.
func schedEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
	e.copies("whoami", "web", 7, 2, 0)
	e.seed(t, blogApp(), seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 4})
	return e
}

// schedule creates a schedule through the service.
func (e *consoleEnv) schedule(t *testing.T, appID, name, cron string, command ...string) service.Schedule {
	t.Helper()
	sc, err := e.Svc.CreateSchedule(context.Background(), service.NewSchedule{App: appID, Name: name, Cron: cron, Command: command, StopAfterSeconds: 300})
	if err != nil {
		t.Fatalf("CreateSchedule(%s): %v", name, err)
	}
	return sc
}

// pastRun is one ended run, as the watcher leaves it.
type pastRun struct {
	ID       string
	Status   string
	Ago      time.Duration // how long before now it started
	Took     time.Duration
	Exit     *int
	Error    string
	Stdout   string
	Stderr   string
	Cut      bool
	NoOutput bool
}

// runs stores ended runs of a schedule, oldest first as given.
func (e *consoleEnv) runs(t *testing.T, sc service.Schedule, runs ...pastRun) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, r := range runs {
		started := now.Add(-r.Ago)
		rec := store.ScheduleRunRecord{ID: r.ID, ScheduleID: sc.ID, AppID: sc.App, Service: sc.Service, Trigger: service.RunTriggerTimer,
			Command: sc.Command, Status: store.RunStarting, StartedAt: started, JobVersion: new(7)}
		if r.Status == store.RunSkipped {
			rec.Status, rec.Error, rec.FinishedAt, rec.JobVersion = store.RunSkipped, r.Error, &started, nil
			if err := e.Store.InsertRun(ctx, rec); err != nil {
				t.Fatalf("InsertRun(%s): %v", r.ID, err)
			}
			continue
		}
		if err := e.Store.InsertRun(ctx, rec); err != nil {
			t.Fatalf("InsertRun(%s): %v", r.ID, err)
		}
		st := store.RunSettlement{Status: r.Status, ExitCode: r.Exit, Error: r.Error, TaskStartedAt: &started, FinishedAt: started.Add(r.Took),
			Stdout: []byte(r.Stdout), Stderr: []byte(r.Stderr), StdoutCut: r.Cut, OutputKept: !r.NoOutput}
		if err := e.Store.SettleRun(ctx, r.ID, st); err != nil {
			t.Fatalf("SettleRun(%s): %v", r.ID, err)
		}
	}
}

// rowOf is a schedule's <details> on the page, or "".
func rowOf(body, id string) string {
	start := strings.Index(body, `<details class="cv-sdetails" id="schedule-`+id+`"`)
	if start < 0 {
		return ""
	}
	rest := body[start:]
	// A row ends where the next row (or the table) does; its own confirmations are <details> too.
	end := strings.Index(rest[1:], `<details class="cv-sdetails"`)
	if end < 0 {
		end = strings.Index(rest, `<div class="cv-sfoot">`)
	} else {
		end++
	}
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// schedSummary is the summary row of a schedule's <details>.
func schedSummary(row string) string {
	if i := strings.Index(row, "</summary>"); i >= 0 {
		return row[:i]
	}
	return row
}

func TestClockOf(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) // a Friday
	tests := []struct {
		at          time.Time
		text, local string
	}{
		{time.Date(2026, 10, 23, 14, 15, 0, 0, time.UTC), "14:15 UTC", localTime},
		{time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC), "tomorrow 02:00 UTC", localTime},
		{time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC), "yesterday 14:00 UTC", localTime},
		{time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC), "in 2 d · Sun 03:00 UTC", localWeekday},
		{time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC), "3 d ago · Tue 09:30 UTC", localWeekday},
		{time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC), "2026-11-01 06:00 UTC", localDate},
		{time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC), "2026-10-10 06:00 UTC", localDate},
		// Another zone is read as the same moment in UTC.
		{time.Date(2026, 10, 24, 0, 30, 0, 0, time.FixedZone("CEST", 2*3600)), "22:30 UTC", localTime},
	}
	for _, tt := range tests {
		got := clockOf(now, tt.at)
		if got.Text != tt.text || got.Local != tt.local || !strings.HasSuffix(got.ISO, "Z") {
			t.Errorf("clockOf(%s) = %+v, want %q (%s)", tt.at, got, tt.text, tt.local)
		}
	}
	if got := clockSecondsOf(now, time.Date(2026, 10, 23, 14, 0, 2, 0, time.UTC)); got.Text != "14:00:02 UTC" || got.Local != localSeconds {
		t.Errorf("clockSecondsOf = %+v", got)
	}
	if got := clockDateOf(now, time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)); got.Text != "yesterday 14:00 UTC" || got.Local != localDate {
		t.Errorf("clockDateOf = %+v", got)
	}
	for at, want := range map[time.Time]string{
		time.Date(2026, 10, 23, 14, 15, 0, 0, time.UTC): "14:15",
		time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC):   "tomorrow 03:00",
		time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC):   "Mon 03:00",
		time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC):    "2026-11-01 06:00",
	} {
		if got := clockCompactOf(now, at); got.Text != want {
			t.Errorf("clockCompactOf(%s) = %q, want %q", at, got.Text, want)
		}
	}
}

func TestCommandLineAndDefaultName(t *testing.T) {
	for _, tt := range []struct {
		words []string
		line  string
		name  string
	}{
		{[]string{"node", "scripts/sync-rates.js"}, "node scripts/sync-rates.js", "sync-rates"},
		{[]string{"sh", "-c", "echo hi; exit 3"}, "sh -c 'echo hi; exit 3'", "echo-hi-exit-3"},
		{[]string{"echo", "it's"}, `echo 'it'\''s'`, "it-s"},
		{[]string{"/usr/bin/Backup_Thing.PY"}, "/usr/bin/Backup_Thing.PY", "backup-thing"},
		{[]string{"x", ""}, "x ''", "x"},
	} {
		if got := commandLine(tt.words); got != tt.line {
			t.Errorf("commandLine(%q) = %q, want %q", tt.words, got, tt.line)
		}
		// The line reads back as the same words.
		if back, err := splitCommand(commandLine(tt.words)); err != nil || strings.Join(back, "|") != strings.Join(tt.words, "|") {
			t.Errorf("splitCommand(commandLine(%q)) = %q, %v", tt.words, back, err)
		}
		if got := defaultScheduleName(tt.words); got != tt.name && tt.words[len(tt.words)-1] != "" {
			t.Errorf("defaultScheduleName(%q) = %q, want %q", tt.words, got, tt.name)
		}
	}
}

// The list: a row per state (never run, ok, failed, paused, skipped), the count line, the strip,
// the detail's sentence and buttons, the sidebar item, and every time a UTC clock.
func TestSchedulesPage(t *testing.T) {
	e := schedEnv(t)
	never := e.schedule(t, "whoami", "never", "0 3 * * *", "echo", "never")
	good := e.schedule(t, "whoami", "good", "*/15 * * * *", "node", "scripts/sync-rates.js")
	e.runs(t, good, pastRun{ID: "g1", Status: store.RunOK, Ago: 3 * time.Hour, Took: 40 * time.Second},
		pastRun{ID: "g2", Status: store.RunOK, Ago: 2 * time.Hour, Took: 48 * time.Second},
		pastRun{ID: "g3", Status: store.RunOK, Ago: time.Hour, Took: 50 * time.Second})
	bad := e.schedule(t, "blog", "bad", "0 4 * * *", "sh", "-c", "exit 1")
	e.runs(t, bad, pastRun{ID: "b1", Status: store.RunOK, Ago: 4 * time.Hour, Took: time.Second},
		pastRun{ID: "b2", Status: store.RunFailed, Ago: 3 * time.Hour, Took: time.Second, Exit: new(1), Error: "exit 1"},
		pastRun{ID: "b3", Status: store.RunFailed, Ago: 2 * time.Hour, Took: time.Second, Exit: new(1), Error: "exit 1"},
		pastRun{ID: "b4", Status: store.RunFailed, Ago: time.Hour, Took: time.Second, Exit: new(1), Error: "exit 1"})
	paused := e.schedule(t, "whoami", "paused", "0 5 * * *", "true")
	if _, err := e.Svc.PauseSchedule(context.Background(), paused.ID); err != nil {
		t.Fatal(err)
	}
	skipped := e.schedule(t, "whoami", "skipper", "0 6 * * *", "sleep", "9")
	e.runs(t, skipped, pastRun{ID: "s1", Status: store.RunOK, Ago: 2 * time.Hour, Took: time.Minute},
		pastRun{ID: "s2", Status: store.RunSkipped, Ago: time.Hour, Error: "the run before was still going"})

	resp, body := e.get(t, "/schedules")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /schedules = %d:\n%s", resp.StatusCode, text(body))
	}
	got := text(body)
	for _, want := range []string{
		"Schedules", "Commands that run on a timer in a fresh copy of an app, then stop. Times are UTC.", "New schedule",
		"5 schedules · 1 failing · next run", "Schedule Runs Next Last run Last 14 runs",
		"Last 14 runs read oldest to newest.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	// No channel yet: the footer doesn't say where failed runs go.
	if strings.Contains(got, "Failed runs are sent to your channels") {
		t.Error("the footer names channels that don't exist")
	}
	if !strings.Contains(body, `<a href="/schedules" class="cv-nav-item" aria-current="page">`) {
		t.Error("Schedules isn't the current sidebar item")
	}
	// The failed one comes first and is open; the paused one last.
	if i, j := strings.Index(body, `id="schedule-`+bad.ID+`"`), strings.Index(body, `id="schedule-`+paused.ID+`"`); i < 0 || j < i ||
		strings.Index(body, `id="schedule-`+never.ID+`"`) > j {
		t.Error("rows aren't failed first and paused last")
	}

	rows := map[string]struct {
		id   string
		want []string
	}{
		"never": {never.ID, []string{"cv-mark-idle", "never", "whoami · echo never", "Every day at 03:00 UTC", "0 3 * * *", "It hasn't run yet.", "no runs yet · stops at 5 min", "Run now"}},
		"good": {good.ID, []string{"cv-mark-healthy", "Every 15 minutes", "took 50 s", "usually 48 s · stops at 5 min",
			"The last run: finished in 50 s.", "Logs of last run", "Pause", "Edit", "Delete…", "Its runs' history goes with it."}},
		"bad": {bad.ID, []string{"cv-mark-failed", "last run failed", "blog · web · sh -c 'exit 1'", "Exited with code 1. 3 of the last 4 runs failed the same way.",
			"web of blog", "skip the next run", "256 MB", "default / production"}},
		"paused":  {paused.ID, []string{"cv-mark-idle", "paused", "Paused just now.", "Resume"}},
		"skipper": {skipped.ID, []string{"cv-mark-warning", "Skipped once, ", " ago, because the run before was still going."}},
	}
	for name, r := range rows {
		row := rowOf(body, r.id)
		if row == "" {
			t.Errorf("no row for %s", name)
			continue
		}
		for _, want := range r.want {
			if !strings.Contains(row, want) && !strings.Contains(text(row), want) {
				t.Errorf("row %s lacks %q:\n%s", name, want, text(row))
			}
		}
	}
	if !strings.Contains(rowOf(body, bad.ID), ` open>`) || strings.Contains(rowOf(body, good.ID), ` open>`) {
		t.Error("the failed first row should be the only one open")
	}
	// Run now is primary on the failed one, secondary elsewhere; a paused one has Resume and no Pause.
	if !strings.Contains(rowOf(body, bad.ID), `cv-btn cv-btn-primary cv-btn-sm"><svg`) || strings.Contains(rowOf(body, good.ID), `cv-btn-primary`) {
		t.Error("Run now should be primary only after a failed run")
	}
	if p := rowOf(body, paused.ID); !strings.Contains(p, `/schedules/`+paused.ID+`/resume`) || strings.Contains(p, `/pause"`) {
		t.Error("a paused schedule offers Resume, not Pause")
	}
	if !strings.Contains(text(schedSummary(rowOf(body, paused.ID))), "paused") || strings.Contains(schedSummary(rowOf(body, paused.ID)), `<time`) {
		t.Error("a paused schedule's Next is the word paused")
	}
	// The strip: oldest first, each mark a link to its run's page with a title.
	strip := schedSummary(rowOf(body, bad.ID))
	marks := regexp.MustCompile(`<a href="/schedules/[^/]+/runs/(b\d)" class="cv-sstrip-mark" title="[^"]+ · (ok|failed)`).FindAllStringSubmatch(strip, -1)
	if len(marks) != 4 || marks[0][1] != "b1" || marks[3][1] != "b4" || marks[0][2] != "ok" {
		t.Errorf("the strip = %v", marks)
	}
	if !strings.Contains(strip, `role="group" aria-label="Last runs, oldest first"`) {
		t.Error("the strip isn't named")
	}
	// The bars: one per run, the failed ones in the failed hue, the skipped one a stub.
	if n := strings.Count(rowOf(body, bad.ID), `cv-sbar cv-sbar-failed`); n != 3 {
		t.Errorf("%d failed bars, want 3", n)
	}
	if !strings.Contains(rowOf(body, skipped.ID), `cv-sbar cv-sbar-skipped"`) {
		t.Error("the skipped run has no stub")
	}
	checkClocks(t, "/schedules", body)
}

// checkClocks fails for a clock element that isn't a <time> with a Z datetime and
// UTC text, and for a page that has none. Times in a list (the preview's next
// runs) say UTC once, after the list.
func checkClocks(t *testing.T, page, body string) {
	t.Helper()
	group := regexp.MustCompile(`(?s)<span class="cv-clock-group" data-clock-group>.*?</span>`)
	inGroups := strings.Count(strings.Join(group.FindAllString(body, -1), ""), `class="cv-clock"`)
	all := regexp.MustCompile(`<time class="cv-clock" datetime="([^"]+)"[^>]*>([^<]*)</time>`).FindAllStringSubmatch(body, -1)
	if len(all) == 0 {
		t.Errorf("%s has no clock times", page)
	}
	if n := strings.Count(body, `class="cv-clock"`); n != len(all) {
		t.Errorf("%s: %d clock elements, %d of them <time> with a datetime", page, n, len(all))
	}
	utc := 0
	for _, m := range all {
		if _, err := time.Parse(time.RFC3339, m[1]); err != nil || !strings.HasSuffix(m[1], "Z") {
			t.Errorf("%s: datetime %q isn't RFC 3339 in UTC", page, m[1])
		}
		if strings.HasSuffix(m[2], " UTC") {
			utc++
		}
	}
	if utc != len(all)-inGroups {
		t.Errorf("%s: %d of %d clock times end in UTC (%d are in a list)", page, utc, len(all), inGroups)
	}
}

// A running run: the row says so with its start and Stop this run; the stop is a post back to the row.
func TestSchedulesRunningAndStop(t *testing.T) {
	e := schedEnv(t)
	sc := e.schedule(t, "whoami", "slow", "0 4 * * *", "sleep", "180")
	run, err := e.Svc.RunScheduleNow(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("RunScheduleNow: %v", err)
	}
	body := e.pageOf(t, "/schedules")
	row := rowOf(body, sc.ID)
	for _, want := range []string{"cv-mark-rolling", "running now", "started", "Running for", "Stop this run", "The run is stopped and counted as stopped by you.", "Stop the run"} {
		if !strings.Contains(text(row), want) && !strings.Contains(row, want) {
			t.Errorf("the running row lacks %q:\n%s", want, text(row))
		}
	}
	if strings.Contains(row, "/run\"") {
		t.Error("a running row offers Run now")
	}
	if !regexp.MustCompile(`started <time class="cv-clock" datetime="[^"]+Z"[^>]*data-local="time-s">\d\d:\d\d:\d\d UTC</time>`).MatchString(row) {
		t.Errorf("the start isn't a clock with seconds:\n%s", row)
	}
	// Run now while it goes (it skips while one runs): refused with the reason, 409, on the list.
	resp, page := e.postForm(t, "/schedules/"+sc.ID+"/run", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "A run of slow is going") {
		t.Errorf("Run now while running = %d:\n%s", resp.StatusCode, text(page))
	}
	resp, _ = e.postForm(t, "/schedules/"+sc.ID+"/runs/"+run.ID+"/stop", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/schedules?open="+sc.ID+"#schedule-"+sc.ID {
		t.Errorf("Stop this run = %d to %q", resp.StatusCode, location(t, resp))
	}
	// The run's page says it still runs; its output comes when it ends.
	page = e.pageOf(t, "/schedules/"+sc.ID+"/runs/"+run.ID)
	if !strings.Contains(text(page), "Still running: reload for more.") {
		t.Errorf("the running run's page:\n%s", text(page))
	}
}

// The run's page: the output (only here), the cut line, no-store; a skipped run and "No output.".
func TestScheduleRunPage(t *testing.T) {
	e := schedEnv(t)
	sc := e.schedule(t, "whoami", "hello", "0 3 * * *", "sh", "-c", "echo $X")
	e.runs(t, sc,
		pastRun{ID: "r1", Status: store.RunFailed, Ago: 26 * time.Hour, Took: 3 * time.Second, Exit: new(2), Error: "exit 2",
			Stdout: "first line\n" + runSentinel + "\n", Stderr: "oops <b>not markup</b>\n", Cut: true},
		pastRun{ID: "r2", Status: store.RunOK, Ago: 2 * time.Hour, Took: 48 * time.Second},
		pastRun{ID: "r3", Status: store.RunSkipped, Ago: time.Hour, Error: "the run before was still going"})

	resp, body := e.get(t, "/schedules/"+sc.ID+"/runs/r1")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("run page = %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	got := text(body)
	for _, want := range []string{"hello · yesterday", "UTC", "Exited with code 2.", "started", "took 3 s · web of whoami · v7",
		"Output", "The first lines of stdout were dropped; the last 64 KiB of each stream is kept.",
		"first line", runSentinel, "Schedules /hello"} {
		if !strings.Contains(got, want) {
			t.Errorf("the run page lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(body, "<b>not markup</b>") ||
		!strings.Contains(body, `<span class="cv-log-stream">stderr</span><span class="cv-log-text">oops &lt;b&gt;not markup&lt;/b&gt;</span>`) {
		t.Error("stderr isn't marked, or the output was drawn as markup")
	}
	if !strings.Contains(body, `<a href="/schedules?open=`+sc.ID+`#schedule-`+sc.ID+`" class="cv-crumb">hello</a>`) {
		t.Error("the crumb doesn't lead back to the schedule's row")
	}
	if !regexp.MustCompile(`<h1 class="cv-headline"><span class="cv-inline-mono">hello</span> · <time class="cv-clock" datetime="[^"]+Z"[^>]* data-local="date">yesterday \d\d:\d\d UTC</time></h1>`).MatchString(body) {
		t.Errorf("the title isn't the name and a clock:\n%s", body)
	}
	checkClocks(t, "the run page", body)

	if got := text(e.pageOf(t, "/schedules/"+sc.ID+"/runs/r2")); !strings.Contains(got, "Finished in 48 s.") || !strings.Contains(got, "No output.") {
		t.Errorf("an ok run without output:\n%s", got)
	}
	if got := text(e.pageOf(t, "/schedules/"+sc.ID+"/runs/r3")); !strings.Contains(got, "Skipped: the run before was still going.") ||
		!strings.Contains(got, "No output: the command didn't run.") {
		t.Errorf("a skipped run:\n%s", got)
	}
	if resp, _ := e.get(t, "/schedules/"+sc.ID+"/runs/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown run = %d", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/schedules/nope/runs/r1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown schedule = %d", resp.StatusCode)
	}

	// The output is on the run's page only: not in the list, its live region, the Configure panel, the
	// home, a needs-you item, or the log.
	for _, path := range []string{"/schedules", "/parts/schedules", "/apps/whoami/configure", "/apps", "/parts/attention",
		"/schedules/" + sc.ID + "/edit"} {
		if _, body := e.get(t, path); strings.Contains(body, runSentinel) {
			t.Errorf("%s holds the run's output", path)
		}
	}
	if strings.Contains(e.Logs.String(), runSentinel) {
		t.Error("the log holds the run's output")
	}
}

// A service's backup is a built-in row: its name, its engine's words, the Backups sentence, the
// marks linking to the Backups tab, Change in Backups and none of Run now, Pause, Edit, Delete.
func TestSchedulesBuiltin(t *testing.T) {
	e := backupsEnv(t, true) // duo, its web service backed up with pg_dump to the bucket "backups"
	now := time.Now().UTC()
	fin := now.Add(-time.Hour + 30*time.Second)
	if err := e.Store.SaveBackup(context.Background(), store.BackupRecord{AppID: "duo", ID: "bk1", Service: "web", Engine: app.BackupPostgres,
		Status: store.BackupSucceeded, Bytes: 1 << 20, StartedAt: now.Add(-time.Hour), FinishedAt: &fin}); err != nil {
		t.Fatal(err)
	}
	id := "backup:duo:web"
	body := e.pageOf(t, "/schedules")
	row := rowOf(body, id)
	if row == "" {
		t.Fatalf("no built-in row:\n%s", text(body))
	}
	for _, want := range []string{"web backup", "duo · web · pg_dump → backups", "Every day at 03:00 UTC",
		"Made by Backups. Change the time or how many are kept in the app's Backups tab.", "Change in Backups", "Logs of last run", "usually 30 s"} {
		if !strings.Contains(text(row), want) {
			t.Errorf("the built-in row lacks %q:\n%s", want, text(row))
		}
	}
	for _, gone := range []string{"/run\"", "/pause\"", "/edit\"", "/delete\"", "Delete…", "stops at"} {
		if strings.Contains(row, gone) {
			t.Errorf("the built-in row has %q", gone)
		}
	}
	if !strings.Contains(row, `<a href="/apps/duo/backups#backup-bk1" class="cv-sstrip-mark"`) || !strings.Contains(row, `href="/apps/duo/backups#svc-web"`) {
		t.Errorf("the built-in's marks or Change in Backups don't lead to the Backups tab:\n%s", row)
	}
	// Its run's address is the Backups tab; its form is refused; its posts are refused with the reason.
	resp, _ := e.get(t, "/schedules/"+url.PathEscape(id)+"/runs/bk1")
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duo/backups#backup-bk1" {
		t.Errorf("a built-in's run page = %d to %q", resp.StatusCode, location(t, resp))
	}
	if resp, body := e.get(t, "/schedules/"+url.PathEscape(id)+"/edit"); resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "made by Backups") {
		t.Errorf("a built-in's edit = %d:\n%s", resp.StatusCode, text(body))
	}
	for _, act := range []string{"run", "pause", "resume", "delete"} {
		resp, page := e.postForm(t, "/schedules/"+url.PathEscape(id)+"/"+act, url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "web backup is made by Backups: change it in the app's Backups tab.") {
			t.Errorf("%s on a built-in = %d:\n%s", act, resp.StatusCode, text(page))
		}
	}
	// The Backups tab's times are clocks too (feature 022).
	checkClocks(t, "the Backups tab", e.pageOf(t, "/apps/duo/backups"))
}

// The posts: Run now, Pause, Resume and Delete answer 303 back to the row; a stopped app's Run now
// is refused with the reason and a 409, on the list, never a redirect.
func TestSchedulePosts(t *testing.T) {
	e := schedEnv(t)
	sc := e.schedule(t, "whoami", "tick", "0 3 * * *", "date")
	back := "/schedules?open=" + sc.ID + "#schedule-" + sc.ID
	for _, act := range []string{"pause", "resume", "run"} {
		resp, body := e.postForm(t, "/schedules/"+sc.ID+"/"+act, url.Values{})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != back {
			t.Errorf("%s = %d to %q:\n%s", act, resp.StatusCode, location(t, resp), text(body))
		}
	}
	runs, err := e.Svc.ListScheduleRuns(context.Background(), sc.ID, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("after Run now: %v, %v", runs, err)
	}
	// The address opens the row.
	if body := e.pageOf(t, back); !strings.Contains(rowOf(body, sc.ID), ` open>`) {
		t.Error("?open= didn't open the row")
	}

	other := e.schedule(t, "blog", "other", "0 3 * * *", "date")
	if err := e.Store.SetAppStopped(context.Background(), "blog", new(time.Now())); err != nil {
		t.Fatalf("SetAppStopped: %v", err)
	}
	resp, body := e.postForm(t, "/schedules/"+other.ID+"/run", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The app of other is stopped: start it first.") {
		t.Errorf("Run now on a stopped app = %d:\n%s", resp.StatusCode, text(body))
	}

	resp, _ = e.postForm(t, "/schedules/"+sc.ID+"/delete", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/schedules?deleted=tick" {
		t.Fatalf("delete = %d to %q", resp.StatusCode, location(t, resp))
	}
	if got := text(e.pageOf(t, "/schedules?deleted=tick")); !strings.Contains(got, "tick was deleted, with its runs.") {
		t.Errorf("after delete:\n%s", got)
	}
	if resp, _ := e.postForm(t, "/schedules/"+sc.ID+"/pause", url.Values{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("pause of a deleted schedule = %d", resp.StatusCode)
	}
}

// The empty list, the filter (the app select, a project select with more than one project), and the
// live region as a part of its own.
func TestSchedulesEmptyFilterAndPart(t *testing.T) {
	e := schedEnv(t)
	got := text(e.pageOf(t, "/schedules"))
	for _, want := range []string{"0 schedules · none failing · no runs scheduled",
		"No schedules yet. A schedule runs a command in a fresh copy of an app on a timer, then stops."} {
		if !strings.Contains(got, want) {
			t.Errorf("the empty page lacks %q:\n%s", want, got)
		}
	}
	a := e.schedule(t, "whoami", "a", "0 3 * * *", "date")
	b := e.schedule(t, "blog", "b", "0 3 * * *", "date")
	body := e.pageOf(t, "/schedules?app=blog")
	if rowOf(body, a.ID) != "" || rowOf(body, b.ID) == "" || !strings.Contains(text(body), "1 schedule · none failing") {
		t.Errorf("the app filter:\n%s", text(body))
	}
	if !strings.Contains(body, `<option value="blog" selected>blog</option>`) || strings.Contains(body, `name="project"`) ||
		!strings.Contains(body, `href="/schedules/new?app=blog"`) || !strings.Contains(body, `hx-get="/parts/schedules?app=blog"`) {
		t.Errorf("the filter row with one project:\n%s", body)
	}
	if got := text(e.pageOf(t, "/schedules?app=nope")); !strings.Contains(got, "No schedule matches.") {
		t.Errorf("a filter that keeps nothing:\n%s", got)
	}
	if _, err := e.Svc.CreateProject(context.Background(), "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if body := e.pageOf(t, "/schedules"); !strings.Contains(body, `name="project"`) || !strings.Contains(body, `<option value="shop">Shop</option>`) {
		t.Error("no project select with two projects")
	}

	resp, part := e.get(t, "/parts/schedules")
	if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") || !strings.Contains(part, `id="schedule-`+a.ID+`"`) ||
		!strings.Contains(part, "2 schedules") {
		t.Errorf("/parts/schedules = %d:\n%s", resp.StatusCode, part)
	}
	if body := e.pageOf(t, "/schedules"); !strings.Contains(body, `hx-trigger="sse:schedules, sse:sync"`) || !strings.Contains(body, `sse-connect="/live"`) {
		t.Error("the list isn't a live region on the schedules notice")
	}
}

// The workspace stream forwards the schedules notice, which the list refreshes on.
func TestLiveForwardsSchedules(t *testing.T) {
	e := schedEnv(t)
	ls := openStream(t, e.URL, "/live", e.Cookie.Value)
	ls.expect("sync")
	e.schedule(t, "whoami", "noticed", "0 3 * * *", "date")
	for {
		if ev := ls.next(); ev == "schedules" {
			return
		}
	}
}

// Configure's Schedules panel: a line per schedule (built-in ones too), or "No schedules.", and the
// links to the list and the form, both for this app.
func TestConfigureSchedulesPanel(t *testing.T) {
	e := schedEnv(t)
	got := text(e.pageOf(t, "/apps/whoami/configure"))
	if !strings.Contains(got, "Schedules No schedules. Manage schedules New schedule") {
		t.Errorf("the empty panel:\n%s", got)
	}
	e.schedule(t, "whoami", "rates-sync", "*/15 * * * *", "node", "sync.js")
	body := e.pageOf(t, "/apps/whoami/configure")
	if !strings.Contains(text(body), "rates-sync · every 15 minutes") || !strings.Contains(body, `href="/schedules?app=whoami"`) ||
		!strings.Contains(body, `href="/schedules/new?app=whoami"`) {
		t.Errorf("the panel:\n%s", text(body))
	}
}

// The shell loads localtime.js, which is ours, listed with its sum, and served; and a page without
// it (what the server sends) already reads right: UTC times, "Times are UTC.".
func TestLocalTimeScript(t *testing.T) {
	e := schedEnv(t)
	body := e.pageOf(t, "/schedules")
	src := regexp.MustCompile(`<script src="(/static/js/localtime\.js\?v=[0-9a-f]{12})" defer></script>`).FindStringSubmatch(body)
	if src == nil {
		t.Fatal("the page doesn't load localtime.js")
	}
	resp, js := e.get(t, src[1])
	if resp.StatusCode != http.StatusOK || !strings.Contains(js, "cv-clock-local") || !strings.Contains(js, `" local"`) {
		t.Errorf("GET %s = %d", src[1], resp.StatusCode)
	}
	// It names no zone and puts text in as text.
	if strings.Contains(js, "innerHTML") || strings.Contains(js, "timeZoneName") {
		t.Error("localtime.js writes markup or names the zone")
	}
	content, err := fs.ReadFile(staticFS, "static/js/localtime.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	versions, err := os.ReadFile("../../tools/ui-vendor.versions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(versions), "localtime.js ours - - "+hex.EncodeToString(sum[:])) {
		t.Error("tools/ui-vendor.versions doesn't list localtime.js with its sum")
	}
	if !strings.Contains(body, `<p class="cv-lede" data-local-lede>Commands that run on a timer in a fresh copy of an app, then stop. Times are UTC.</p>`) {
		t.Error("the lede isn't marked for the script, or doesn't say UTC without it")
	}
}

// The deployment entry's Live stage is a clock time too (feature 022), keeping its title.
func TestLiveStageIsAClock(t *testing.T) {
	e := historyEnv(t) // d1, d2 healthy
	body := e.pageOf(t, "/apps/blog/deployments")
	d2 := entryOf(t, body, "d2")
	if !regexp.MustCompile(`<span title="[^"]+ UTC"><time class="cv-clock" datetime="[^"]+Z" title="[^"]+" data-local="time-s">\d\d:\d\d:\d\d UTC</time></span>`).MatchString(d2) {
		t.Errorf("the Live stage isn't a clock with its title:\n%s", d2)
	}
	checkClocks(t, "the Deployments tab", body)
}
