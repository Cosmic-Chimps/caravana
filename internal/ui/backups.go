package ui

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Backups tab (feature 010): each backed-up database with "Back up now",
// its backups with "Restore", and the recent restores. Plain forms; a restore
// asks for the service's name first, in a <details>.

// backupsTab is the body of the Backups tab.
type backupsTab struct {
	ID          string
	Destination bool // a destination is set
	// Volumes is the Volumes section (feature 016), first on the tab; none when
	// the app has no volume, and then the section isn't drawn.
	Volumes []volumeRow
	// VolumeHint says a mounted volume isn't backed up, which the section
	// answers with a link to Configure.
	VolumeHint bool
	Services   []backupService
	Backups    []backupRow
	Restores   []restoreRow
	Notice     sentence
	ConfirmErr string // the backup whose restore confirmation was wrong
}

// volumeRow is one row of the Volumes section.
type volumeRow struct {
	Name string
	// Mounts is "service:/path" for each mount, with "(read-only)" when so. A
	// volume no service mounts has none, and says so.
	Mounts    []string
	Unmounted bool
	// HostPath is empty when Nomad couldn't be read.
	HostPath string
	// Size is "312 MB", empty while the volume has never been measured; then
	// Measured says when it was.
	Size     string
	Measured when
	// BackedUp says a volume backup covers it, on Schedule (in words). Last is
	// when its newest run started and LastStatus how it went; HasLast is false
	// until one ran.
	BackedUp   bool
	Schedule   string
	HasLast    bool
	Last       when
	LastStatus statusView
}

// backupService is one service with a backup policy: a database, or (feature
// 016) a service whose volumes are archived while it is stopped.
type backupService struct {
	Name, Schedule string
	Keep           int
	// Volume says the engine is "volume". Its words differ: the schedule is
	// told as a sentence, and the service stops while a run lasts.
	Volume bool
	// ScheduleText is the schedule in words, for a volume service.
	ScheduleText string
	// LastStopped is how long the service was stopped by its newest measured
	// run, as "4 s"; empty until a run has been measured.
	LastStopped string
}

// backupRow is one backup.
type backupRow struct {
	ID, Service, Status, Size, Message string
	Started                            when
	CanRestore                         bool
	// Volume says the backup is of a volume service; Stopped is "stopped 4 s"
	// once its run was measured.
	Volume  bool
	Stopped string
}

// restoreRow is one restore. Stopped is "stopped 4 s" for a volume restore
// once it was measured.
type restoreRow struct {
	Service, BackupID, Status, Message string
	Created                            when
	Stopped                            string
}

// backupsPage draws the Backups tab with the given status.
func (s *server) backupsPage(w http.ResponseWriter, r *http.Request, status int, id string, tab backupsTab) {
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
	_, destErr := s.svc.GetBackupDestination(ctx)
	if destErr != nil && !errors.Is(destErr, store.ErrNotFound) {
		s.serverError(w, "read backup destination", destErr)
		return
	}
	head, err := s.shellPage(r, id+" backups", navProjects, s.appCrumbs(r.Context(), id)...)
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
	tab.Volumes, tab.VolumeHint = volumeRows(s.now(), volumes)

	tab.ID, tab.Destination = id, destErr == nil
	for _, svc := range sum.App.Services {
		if svc.Backup == nil {
			continue
		}
		bs := backupService{Name: svc.Name, Schedule: svc.Backup.ScheduleOrDefault(), Keep: svc.Backup.KeepOrDefault()}
		if svc.Backup.Engine == app.BackupVolume {
			bs.Volume, bs.ScheduleText = true, scheduleText(bs.Schedule)
			for _, b := range backups { // newest first
				if b.Service == svc.Name && b.Stopped != nil {
					bs.LastStopped = stoppedText(*b.Stopped)
					break
				}
			}
		}
		tab.Services = append(tab.Services, bs)
	}
	now := s.now()
	for _, b := range backups {
		row := backupRow{ID: b.ID, Service: b.Service, Status: b.Status, Message: b.Message,
			Size: sizeText(b.Bytes, b.Status), Started: whenOf(now, &b.StartedAt, ""),
			CanRestore: b.Status == store.BackupSucceeded && tab.Destination, Volume: b.Engine == app.BackupVolume}
		if b.Stopped != nil {
			row.Stopped = "stopped " + stoppedText(*b.Stopped)
		}
		tab.Backups = append(tab.Backups, row)
	}
	for _, x := range restores {
		row := restoreRow{Service: x.Service, BackupID: x.BackupID, Status: x.Status,
			Message: x.Message, Created: whenOf(now, &x.CreatedAt, "")}
		if x.Stopped != nil {
			row.Stopped = "stopped " + stoppedText(*x.Stopped)
		}
		tab.Restores = append(tab.Restores, row)
	}
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(r.Context(), sum, "backups"), Backups: &tab})
}

// volumeRows turns the service's volumes into the section's rows, and says
// whether to show the hint that a mounted volume isn't backed up. It is pure,
// so the table test calls it with a fixed now.
func volumeRows(now time.Time, volumes []service.VolumeView) (rows []volumeRow, hint bool) {
	for _, v := range volumes {
		row := volumeRow{Name: v.Name, Unmounted: !v.Mounted, HostPath: v.HostPath, BackedUp: v.BackedUp}
		for _, m := range v.Mounts {
			text := m.Service + ":" + m.Path
			if m.ReadOnly {
				text += " (read-only)"
			}
			row.Mounts = append(row.Mounts, text)
		}
		if v.Size != nil {
			row.Size, row.Measured = humanBytes(v.Size.Bytes), whenOf(now, &v.Size.MeasuredAt, "")
		}
		if v.BackedUp {
			row.Schedule = scheduleText(v.Schedule)
			if v.LastBackup != nil {
				row.HasLast = true
				row.Last = whenOf(now, &v.LastBackup.StartedAt, "")
				row.LastStatus = backupStatus(v.LastBackup.Status)
			}
		} else if v.Mounted {
			hint = true
		}
		rows = append(rows, row)
	}
	return rows, hint
}

// backupStatus is a backup's status as a mark and a word.
func backupStatus(status string) statusView {
	switch status {
	case store.BackupSucceeded:
		return statusView{Kind: "healthy", Word: "succeeded"}
	case store.BackupFailed:
		return statusView{Kind: "failed", Word: "failed"}
	}
	return statusView{Kind: "rolling", Word: "running"}
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

// sizeText is a backup's size, or nothing until it has one.
func sizeText(n int64, status string) string {
	switch {
	case status != store.BackupSucceeded:
		return ""
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
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
