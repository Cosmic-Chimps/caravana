package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	Services    []backupService
	Backups     []backupRow
	Restores    []restoreRow
	Notice      sentence
	ConfirmErr  string // the backup whose restore confirmation was wrong
}

// backupService is one database with a backup policy.
type backupService struct {
	Name, Schedule string
	Keep           int
}

// backupRow is one backup.
type backupRow struct {
	ID, Service, Status, Size, Message string
	Started                            when
	CanRestore                         bool
}

// restoreRow is one restore.
type restoreRow struct {
	Service, BackupID, Status, Message string
	Created                            when
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
	head, err := s.shellPage(r, id+" backups", "apps",
		crumb{Label: "Apps", Href: "/apps"}, crumb{Label: id, Href: "/apps/" + id, Mono: true}, crumb{Label: "Backups"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id

	tab.ID, tab.Destination = id, destErr == nil
	for _, svc := range sum.App.Services {
		if svc.Backup != nil {
			tab.Services = append(tab.Services, backupService{Name: svc.Name, Schedule: svc.Backup.ScheduleOrDefault(), Keep: svc.Backup.KeepOrDefault()})
		}
	}
	now := s.now()
	for _, b := range backups {
		tab.Backups = append(tab.Backups, backupRow{ID: b.ID, Service: b.Service, Status: b.Status, Message: b.Message,
			Size: sizeText(b.Bytes, b.Status), Started: whenOf(now, &b.StartedAt, ""),
			CanRestore: b.Status == store.BackupSucceeded && tab.Destination})
	}
	for _, x := range restores {
		tab.Restores = append(tab.Restores, restoreRow{Service: x.Service, BackupID: x.BackupID, Status: x.Status,
			Message: x.Message, Created: whenOf(now, &x.CreatedAt, "")})
	}
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(sum, "backups"), Backups: &tab})
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
	service, _, _ := strings.Cut(backupID, "-")
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
	case errors.Is(err, service.ErrBackupRunning):
		tab.Notice = sentence{plain("A backup or restore of this service is running. Try again when it ends.")}
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
