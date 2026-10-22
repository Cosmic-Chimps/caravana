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
	// Gaps are the services that could be backed up and have no policy.
	Gaps []backupGap
	// Rows are the services with a policy, in the order of the definition.
	Rows []backupSvcRow
	// Unmounted are volumes Nomad still has for the app that no service mounts.
	Unmounted []unmountedVolume
	Restores  []restoreRow
	Notice    sentence
	// ConfirmErr is the backup whose restore confirmation was wrong; its row opens.
	ConfirmErr string
	// ShowAll is the service whose whole run list is shown ("Show older").
	ShowAll string
}

// Counts is the line under the head: "2 backed up · 1 not · stored in <bucket>".
func (t backupsTab) Counts() string {
	where := "no destination"
	if t.Destination {
		where = "stored in " + t.Bucket
	}
	return fmt.Sprintf("%d backed up · %d not · %s", t.BackedUp, len(t.Gaps), where)
}

// backupGap is a service with something to back up and no policy: a database
// image, or a volume. Engine is the policy "Back up daily" sets.
type backupGap struct {
	Service string
	Label   string // the name as the sentence says it: "postgres", or "web · /data"
	Meta    string // "PostgreSQL · 0 copies kept", "Volume · 210 MB"
	Engine  string
}

// backupSvcRow is one backed-up service: the row, and what opens under it.
type backupSvcRow struct {
	Name   string
	Volume bool
	Open   bool
	What   string // "PostgreSQL", "Volume · /data, /cache"
	// Mark is the row's mark: rolling while a run is going, failed when the
	// newest failed, healthy otherwise, idle when it never ran.
	Mark     string
	MarkText string // the mark's meaning, for a screen reader
	Schedule string // in words, "Every day at 03:00 UTC"
	Keep     string // "7 backups"
	// LastWhen is when the newest run started; LastNow says it is running, and
	// LastNever that nothing ran yet. Took and Stopped are its facts.
	LastWhen  when
	LastNow   bool
	LastNever bool
	Took      string
	Stopped   string
	Size      string // the newest healthy run's size
	SizeAll   string // the kept runs' sum, "1.4 GB all"
	Strip     []stripCell
	Failure   sentence
	// VolumeFacts say where each volume of a volume service lives and how big it was
	// measured: "/srv/.../data on this host · 312 MB, measured 3 hours ago".
	VolumeFacts []string
	Runs        []backupRun
	OlderHref   string // "Show older", when there are more runs than are listed
	// RestoreLast is the newest healthy run, for "Restore from the last backup";
	// nil when there is none or a restore isn't possible.
	RestoreLast *backupRun
	CanBackUp   bool
}

// backupRun is one backup run of a service, as the row lists it.
type backupRun struct {
	ID, Service, Status, Mark, Size, Took, Stopped, Message string
	When                                                    when
	CanRestore                                              bool
	Volume                                                  bool
}

// stripCell is one day of the restore-points strip. Kind is healthy, failed,
// running or none (a day with no run: a gap). A day with a run links to it.
type stripCell struct {
	Kind  string
	Href  string
	Title string
}

// unmountedVolume is a volume Nomad has for the app that no service mounts.
type unmountedVolume struct {
	Name, HostPath, Size string
}

// restoreRow is one restore. Stopped is "stopped 4 s" for a volume restore
// once it was measured.
type restoreRow struct {
	Service, BackupID, Status, Mark, Took, Message string
	Created                                        when
	Stopped                                        string
}

// restoreAskView is what the restore confirmation draws: the run it restores,
// whose form it is (Prefix keeps the IDs of two forms for one run apart) and
// whether the last answer said the typed name was wrong.
type restoreAskView struct {
	AppID  string
	Run    backupRun
	Prefix string
	Failed bool
}

// restoreAskFor is the template function {{restoreAsk $ . ""}}.
func restoreAskFor(tab backupsTab, run backupRun, prefix string) restoreAskView {
	return restoreAskView{AppID: tab.ID, Run: run, Prefix: prefix, Failed: prefix == "" && tab.ConfirmErr == run.ID}
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
				gap.Meta = "Volume"
				if size, ok := serviceVolumeBytes(svc.Name, volumes); ok {
					gap.Meta += " · " + humanBytes(size)
				}
			} else {
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
		row := backupRowOf(now, a.ID, svc, runs, volumes, tab)
		row.Open = len(tab.Rows) == 0 || svc.Name == tab.ShowAll || slices.ContainsFunc(row.Runs, func(r backupRun) bool { return r.ID == tab.ConfirmErr })
		tab.Rows = append(tab.Rows, row)
	}

	for _, v := range volumes {
		if !v.Mounted {
			u := unmountedVolume{Name: v.Name, HostPath: v.HostPath}
			if v.Size != nil {
				u.Size = humanBytes(v.Size.Bytes)
			}
			tab.Unmounted = append(tab.Unmounted, u)
		}
	}
	for _, x := range restores {
		row := restoreRow{Service: x.Service, BackupID: x.BackupID, Status: x.Status, Message: x.Message, Created: whenOf(now, &x.CreatedAt, "")}
		row.Mark = runMark(x.Status)
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
	}
	return "rolling"
}

// markText is what a mark says in words, for a screen reader.
func markText(mark string) string {
	switch mark {
	case "healthy":
		return "healthy"
	case "failed":
		return "failed"
	case "rolling":
		return "running"
	}
	return "never ran"
}

// backupRowOf builds one backed-up service's row. runs are its backups, newest first.
func backupRowOf(now time.Time, appID string, svc app.Service, runs []store.BackupRecord, volumes []service.VolumeView, tab backupsTab) backupSvcRow {
	b := svc.Backup
	row := backupSvcRow{Name: svc.Name, Volume: b.Engine == app.BackupVolume, Mark: "idle", LastNever: true,
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
				fact = v.HostPath + " on this host"
			}
			if v.Size != nil {
				fact += " · " + humanBytes(v.Size.Bytes) + ", measured " + relative(now, v.Size.MeasuredAt)
			}
			row.VolumeFacts = append(row.VolumeFacts, fact)
		}
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
			When: whenOf(now, &r.StartedAt, ""), Volume: r.Engine == app.BackupVolume,
			CanRestore: r.Status == store.BackupSucceeded && tab.Destination && !tab.Stopped}
		if r.Status == store.BackupSucceeded {
			run.Size = humanBytes(r.Bytes)
		}
		if r.FinishedAt != nil {
			run.Took = durationText(r.FinishedAt.Sub(r.StartedAt))
		}
		if r.Stopped != nil {
			run.Stopped = "stopped " + stoppedText(*r.Stopped)
		}
		if i == 0 {
			row.Mark, row.LastNever, row.LastNow = run.Mark, false, r.Status == store.BackupRunning
			row.LastWhen, row.Took, row.Stopped = run.When, run.Took, run.Stopped
			if r.Status == store.BackupFailed {
				row.Failure = sentence{plain("The last backup failed " + relative(now, r.StartedAt))}
				if msg := strings.TrimSuffix(strings.TrimSpace(r.Message), "."); msg != "" {
					row.Failure = append(row.Failure, plain(": "+msg))
				}
				row.Failure = append(row.Failure, plain("."))
			}
		}
		if row.Size == "" && r.Status == store.BackupSucceeded {
			row.Size = run.Size
		}
		if row.RestoreLast == nil && run.CanRestore {
			last := run
			row.RestoreLast = &last
		}
		if i < backupRunsShown || svc.Name == tab.ShowAll {
			row.Runs = append(row.Runs, run)
		}
	}
	if len(runs) > backupRunsShown && svc.Name != tab.ShowAll {
		row.OlderHref = "/apps/" + appID + "/backups?all=" + svc.Name + "#svc-" + svc.Name
	}
	row.MarkText = markText(row.Mark)
	row.Strip = restoreStrip(now, runs)
	return row
}

// restoreStrip is the last 30 days as cells, oldest first and today last. A day
// is the UTC day. A day with a run being made is running; else a day with a
// healthy run is that run (the newest healthy one); else one with only failed
// runs is failed; a day with none is a gap. A cell with a run links to its row.
func restoreStrip(now time.Time, runs []store.BackupRecord) []stripCell {
	cells := make([]stripCell, stripDays)
	for i := range cells {
		cells[i].Kind = "none"
	}
	day := func(t time.Time) time.Time {
		y, m, d := t.UTC().Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	today := day(now)
	rank := map[string]int{"none": 0, "failed": 1, "healthy": 2, "running": 3}
	chosen := make([]int, stripDays) // the run each cell shows, by index into runs; runs are newest first
	for i, r := range runs {
		ago := int(today.Sub(day(r.StartedAt)) / (24 * time.Hour))
		if ago < 0 || ago >= stripDays {
			continue
		}
		at := stripDays - 1 - ago
		kind := runMark(r.Status)
		if kind == "rolling" {
			kind = "running"
		}
		// Newest first: the first run of a kind is the newest of it, and a better kind replaces a worse one.
		if rank[kind] > rank[cells[at].Kind] {
			cells[at].Kind, chosen[at] = kind, i
		}
	}
	for at := range cells {
		if cells[at].Kind == "none" {
			continue
		}
		r := runs[chosen[at]]
		cells[at].Href = "#backup-" + r.ID
		when := "today"
		if ago := stripDays - 1 - at; ago > 0 {
			when = strconv.Itoa(ago) + " d ago"
		}
		title := when + " · " + r.StartedAt.UTC().Format("15:04")
		switch cells[at].Kind {
		case "healthy":
			title += " · " + humanBytes(r.Bytes)
		case "failed":
			title += " · failed"
		default:
			title += " · running"
		}
		cells[at].Title = title
	}
	return cells
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
		s.serverError(w, "read app", err)
		return
	}
	backups, restores, err := s.svc.ListBackups(ctx, id)
	if err != nil {
		s.serverError(w, "list backups", err)
		return
	}
	var dest *store.BackupDestination
	switch d, destErr := s.svc.GetBackupDestination(ctx); {
	case destErr == nil:
		dest = &d
	case !errors.Is(destErr, store.ErrNotFound):
		s.serverError(w, "read backup destination", destErr)
		return
	}
	head, err := s.shellPage(r, id+" backups", navProjects, s.appCrumbs(ctx, id)...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id

	volumes, err := s.svc.Volumes(ctx, id)
	if err != nil {
		s.serverError(w, "read volumes", err)
		return
	}
	if extra.ShowAll == "" {
		extra.ShowAll = r.URL.Query().Get("all")
	}
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

// scheduleText tells a cron expression in words when it is a plain one: "every
// day at 03:00 UTC" for "0 3 * * *", "every hour at :15" for "15 * * * *". Any
// other expression is shown as it is, so nothing is ever described wrongly.
func scheduleText(spec string) string {
	f := strings.Fields(spec)
	if len(f) == 5 && f[2] == "*" && f[3] == "*" && f[4] == "*" {
		minute, errMinute := strconv.Atoi(f[0])
		hour, errHour := strconv.Atoi(f[1])
		switch {
		case errMinute != nil || minute < 0 || minute > 59:
		case f[1] == "*":
			return fmt.Sprintf("every hour at :%02d", minute)
		case errHour == nil && hour >= 0 && hour <= 23:
			return fmt.Sprintf("every day at %02d:%02d UTC", hour, minute)
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
		s.serverError(w, "read app", err)
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
			s.serverError(w, "set a backup policy", err)
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
	// A backup's ID is "<service>-<unix seconds>", and a service's name may
	// hold "-" too ("uptime-kuma-1792033207"), so the service ends at the last one.
	service := backupID
	if i := strings.LastIndex(backupID, "-"); i >= 0 {
		service = backupID[:i]
	}
	if r.PostForm.Get("confirm") != service {
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

// refuseBackup draws the tab again with what went wrong.
func (s *server) refuseBackup(w http.ResponseWriter, r *http.Request, id string, err error, tab backupsTab) {
	status := http.StatusConflict
	switch {
	case errors.Is(err, service.ErrAppStopped):
		tab.Notice = sentence{plain(appStoppedText)}
	case errors.Is(err, service.ErrBackupRunning):
		tab.Notice = sentence{plain("A backup or restore of this service is running. Try again when it ends.")}
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
