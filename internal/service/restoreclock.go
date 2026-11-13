package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Restore schedules (feature 036). A service whose backup policy has
// `restore: {schedule, from}` gets the backup pinned under `from` restored at
// each slot of the schedule. Caravana keeps the clock, inside the pass of the
// volume-backup scheduler (schedule.go), so no token is needed and no Nomad job
// holds one. A slot is the same restore the owner's Restore starts
// (restoreBackup), recorded with trigger "schedule" and the label asked for.
//
// Every due slot leaves one record, so the owner can see what happened:
//
//   - the restore itself, running and then succeeded or failed, as for the
//     owner's;
//   - failed, when the slot was refused before anything started (no backup is
//     pinned under the label, retention deleted it, the engine changed, no
//     running copy, no destination, Nomad's refusal);
//   - skipped, when a backup or restore of the service was running: the slot
//     isn't tried again, since a restore that came an hour late would replace
//     newer data the owner didn't expect to lose.
//
// A stopped app's slots leave nothing and wait, as its backups do: the app
// starts again within the hour and the latest slot runs once.

// SetBackupScanGap sets how often the scheduler of backups and restores looks
// for due slots, instead of every 30 seconds. It is for the lab, whose steps
// can't wait that long, and must be called before Start.
func (s *Service) SetBackupScanGap(d time.Duration) {
	if d > 0 {
		s.scheduleGap = d
	}
}

// Words of the records a slot leaves when it is refused. They are Caravana's
// own and never hold a secret.
const (
	restoreSkippedBusy = "a backup or restore of %s was running"
	restoreNoCopy      = "the service has no running copy"
	restoreNoPin       = "no backup of %s is pinned as %s"
	restoreExpiredPin  = "the backup pinned as %s was deleted by retention"
	restoreEngineMoved = "the backup pinned as %s was made by the %s engine; %s now backs up with %s"
)

// runDueRestores is one pass over the restore schedules: it runs the slot of
// each service that owes one. noted is the pass's memory of what it logged, as
// for runDueVolumeBackups.
func (s *Service) runDueRestores(ctx context.Context, noted map[string]string) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		s.log.Warn("schedule restores: list apps", "error", err)
		return
	}
	now := s.clock.Now()
	for _, rec := range apps {
		if rec.StoppedAt != nil {
			continue // a stopped app's restores wait for Start: no run, and nothing recorded
		}
		for _, svc := range rec.App.Services {
			if svc.Backup == nil || svc.Backup.Restore == nil {
				continue
			}
			key := "restore:" + rec.App.ID + "/" + svc.Name
			restore := svc.Backup.Restore
			// The slots up to the newest scheduled restore, of any outcome, are
			// done, and so are those before the app was last saved: a schedule
			// counts from when it was asked for. Unlike a backup's, a restore's
			// base is the later of the two, so an edit that makes a slot of the
			// last hour match never replaces the service's data at once.
			base := rec.UpdatedAt
			last, found, err := s.store.LastScheduledRestore(ctx, rec.App.ID, svc.Name)
			if err != nil {
				s.log.Warn("schedule restores: read restores", "app", rec.App.ID, "service", svc.Name, "error", err)
				continue
			}
			if found && last.After(base) {
				base = last
			}
			slot, due, skipped := dueSlot(restore.Schedule, base, now)
			switch {
			case skipped:
				s.noteOnce(noted, key, "skipped:"+base.String(), "a restore slot was missed and is too old to run; waiting for the next one",
					"app", rec.App.ID, "service", svc.Name, "schedule", restore.Schedule)
			case due:
				s.scheduledRestore(ctx, rec, svc, slot)
			}
		}
	}
}

// scheduledRestore runs one due slot of svc's restore schedule (see the file
// comment for what it records).
func (s *Service) scheduledRestore(ctx context.Context, rec store.AppRecord, svc app.Service, slot time.Time) {
	appID, label := rec.App.ID, svc.Backup.Restore.From
	refuse := func(backupID, status, message string) {
		s.recordRefusedRestore(ctx, appID, svc, backupID, label, status, message, slot)
	}

	b, err := s.store.PinnedBackup(ctx, appID, svc.Name, label)
	switch {
	case errors.Is(err, store.ErrNotFound):
		refuse("", store.BackupFailed, fmt.Sprintf(restoreNoPin, svc.Name, label))
		return
	case err != nil:
		// Nothing is recorded: the slot is still owed, and the next pass tries again.
		s.log.Warn("scheduled restore: read the pinned backup", "app", appID, "service", svc.Name, "label", label, "error", err)
		return
	case b.Expired || b.Status != store.BackupSucceeded:
		refuse(b.ID, store.BackupFailed, fmt.Sprintf(restoreExpiredPin, label))
		return
	case b.Engine != svc.Backup.Engine:
		refuse(b.ID, store.BackupFailed, fmt.Sprintf(restoreEngineMoved, label, b.Engine, svc.Name, svc.Backup.Engine))
		return
	}

	started, err := s.restoreBackup(ctx, appID, b, store.RestoreTriggerSchedule, label)
	switch {
	case err == nil:
		s.log.Info("scheduled restore started", "app", appID, "service", svc.Name, "label", label, "backup", b.ID,
			"restore", started.ID, "slot", slot)
	case errors.Is(err, ErrAppStopped):
		// Stopped between the pass's look and now: the slot waits for Start.
	case errors.Is(err, ErrBackupRunning):
		refuse(b.ID, store.RestoreSkipped, fmt.Sprintf(restoreSkippedBusy, svc.Name))
	case errors.Is(err, ErrNoRunningCopy):
		refuse(b.ID, store.BackupFailed, restoreNoCopy)
	case errors.Is(err, ErrNoBackupDestination):
		refuse(b.ID, store.BackupFailed, ErrNoBackupDestination.Error())
	default:
		refuse(b.ID, store.BackupFailed, err.Error())
	}
}

// recordRefusedRestore writes the record of a slot that started nothing: failed
// with the reason, or skipped. The backup is the one the label named, "" when
// none was pinned.
func (s *Service) recordRefusedRestore(ctx context.Context, appID string, svc app.Service, backupID, label, status, message string, slot time.Time) {
	now := s.clock.Now().UTC()
	r := store.RestoreRecord{ID: strings.ToLower(rand.Text()), AppID: appID, Service: svc.Name, BackupID: backupID,
		Engine: svc.Backup.Engine, Status: status, Message: message, CreatedAt: now, FinishedAt: &now,
		Trigger: store.RestoreTriggerSchedule, Pin: label}
	if err := s.store.SaveRestore(ctx, r); err != nil {
		s.log.Warn("scheduled restore: record the refusal", "app", appID, "service", svc.Name, "error", err)
		return
	}
	s.log.Info("scheduled restore didn't start", "app", appID, "service", svc.Name, "label", label, "slot", slot,
		"status", status, "reason", message)
	s.backupsChanged(appID)
}

// restoreLabelWarnings is the warning of a deploy whose restore schedule names
// a label no backup of the service is pinned under yet (or whose pinned backup
// retention has already deleted): the schedule is accepted, since the owner
// may pin the backup next, but each slot is refused until there is one. A store
// error adds no warning: it is logged, and the slot will say what it finds.
func (s *Service) restoreLabelWarnings(ctx context.Context, a app.App) []app.Warning {
	var out []app.Warning
	for i, svc := range a.Services {
		if svc.Backup == nil || svc.Backup.Restore == nil {
			continue
		}
		label := svc.Backup.Restore.From
		b, err := s.store.PinnedBackup(ctx, a.ID, svc.Name, label)
		if err == nil && !b.Expired {
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("check the label of a restore schedule", "app", a.ID, "service", svc.Name, "error", err)
			continue
		}
		out = append(out, app.Warning{
			Field: fmt.Sprintf("services[%d].backup.restore.from", i),
			Message: fmt.Sprintf("no backup of %s is pinned as %s yet: each scheduled restore is refused until one is "+
				"(pin one on the Backups tab, or PUT /v1/apps/%s/backups/<backupId>/pin)", svc.Name, label, a.ID),
		})
	}
	return out
}
