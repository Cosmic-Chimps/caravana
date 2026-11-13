package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Pinned backups (feature 036). A pin is a label on a backup: retention never
// deletes a pinned backup and counts only the unpinned ones against keep, and a
// service's restore schedule names the label of the backup it restores. The
// store holds the labels; each engine's retention is told the pinned
// timestamps when it runs (a Postgres job reads them from its variable, a
// volume run from its request, a sqlite upload from its arguments), so they
// must never be older than the store's. That is what the app's deploy lock is
// for here: a pin change takes it without waiting and is refused while a deploy
// or a volume run holds it, so no retention runs with a stale list.
//
// The names say pin and label, never "pinned" alone: deploy.go has a type
// called pinned, for pinned image digests.

// backupStamp is the timestamp of a backup: the unix seconds at the end of its
// ID ("<service>-<unix seconds>"), which every engine also puts in the name of
// the object it uploads. ok is false for an ID that isn't of that shape.
func backupStamp(b store.BackupRecord) (stamp string, ok bool) {
	stamp, ok = strings.CutPrefix(b.ID, b.Service+"-")
	if !ok || stamp == "" || strings.ContainsFunc(stamp, func(r rune) bool { return r < '0' || r > '9' }) {
		return "", false
	}
	return stamp, true
}

// pinStamps returns the timestamps of the service's pinned backups that
// retention may not delete (the unexpired ones), oldest first: what the
// engines' retention is given.
func (s *Service) pinStamps(ctx context.Context, appID, service string) ([]string, error) {
	all, err := s.pinStampsByService(ctx, appID)
	return all[service], err
}

// pinStampsByService is pinStamps for every service of the app.
func (s *Service) pinStampsByService(ctx context.Context, appID string) (map[string][]string, error) {
	pinned, err := s.store.PinnedBackups(ctx, appID)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, b := range pinned {
		if stamp, ok := backupStamp(b); ok {
			out[b.Service] = append(out[b.Service], stamp)
		}
	}
	for _, stamps := range out {
		// Equal-length digit strings sort as numbers; length first covers the rest.
		slices.SortFunc(stamps, func(a, b string) int {
			if len(a) != len(b) {
				return len(a) - len(b)
			}
			return strings.Compare(a, b)
		})
	}
	return out, nil
}

// PinBackup pins a succeeded backup under label. A label is unique per
// service, so the backup that held it loses it, and a backup that had another
// label gets this one instead. The errors are an *app.FieldError for a bad
// label (field "label"), store.ErrNotFound for an unknown app or backup or one
// that can't be pinned (it didn't succeed, or retention deleted it),
// ErrAppBusy while a deploy or a volume run holds the app, and
// ErrBackupRunning while a backup or restore of the service runs.
func (s *Service) PinBackup(ctx context.Context, appID, backupID, label string) (store.BackupRecord, error) {
	if !app.ValidLabel(label) {
		return store.BackupRecord{}, app.NewFieldError("label",
			"%q must be 1 to 32 lowercase letters, digits or hyphens, starting and ending with a letter or digit", label)
	}
	b, err := s.store.GetBackup(ctx, appID, backupID)
	if err != nil {
		return store.BackupRecord{}, err
	}
	if b.Status != store.BackupSucceeded || b.Expired {
		return store.BackupRecord{}, fmt.Errorf("backup %s can't be pinned: it %s: %w", backupID,
			map[bool]string{true: "was deleted by retention", false: "didn't succeed"}[b.Expired], store.ErrNotFound)
	}
	if _, _, err := s.backedUpService(ctx, appID, b.Service); err != nil {
		return store.BackupRecord{}, err
	}
	state, err := s.tryLockApp(appID)
	if err != nil {
		return store.BackupRecord{}, err
	}
	defer state.mu.Unlock()
	if err := s.refuseIfBackupBusy(ctx, appID, b.Service); err != nil {
		return store.BackupRecord{}, err
	}

	// What the label and the backup held before, to put back if Nomad refuses.
	var holder store.BackupRecord
	if prev, err := s.store.PinnedBackup(ctx, appID, b.Service, label); err == nil {
		holder = prev
	}
	if _, err := s.store.PinBackup(ctx, appID, backupID, label, s.clock.Now().UTC()); err != nil {
		return store.BackupRecord{}, err
	}
	if err := s.publishPins(ctx, appID, b.Service); err != nil {
		s.restorePins(ctx, b, holder)
		return store.BackupRecord{}, err
	}
	s.backupsChanged(appID)
	return s.store.GetBackup(ctx, appID, backupID)
}

// UnpinBackup takes the label off a backup. A backup with no label is returned
// as it is. The errors are PinBackup's, but for the label.
func (s *Service) UnpinBackup(ctx context.Context, appID, backupID string) (store.BackupRecord, error) {
	b, err := s.store.GetBackup(ctx, appID, backupID)
	if err != nil {
		return store.BackupRecord{}, err
	}
	if b.Pin == "" {
		return b, nil
	}
	state, err := s.tryLockApp(appID)
	if err != nil {
		return store.BackupRecord{}, err
	}
	defer state.mu.Unlock()
	if err := s.refuseIfBackupBusy(ctx, appID, b.Service); err != nil {
		return store.BackupRecord{}, err
	}
	if err := s.store.UnpinBackup(ctx, appID, backupID); err != nil {
		return store.BackupRecord{}, err
	}
	if err := s.publishPins(ctx, appID, b.Service); err != nil {
		s.restorePins(ctx, b, store.BackupRecord{})
		return store.BackupRecord{}, err
	}
	s.backupsChanged(appID)
	return s.store.GetBackup(ctx, appID, backupID)
}

// refuseIfBackupBusy is ErrBackupRunning when a backup or restore of the
// service is running. A service that no longer has a backup policy has no run
// to wait for.
func (s *Service) refuseIfBackupBusy(ctx context.Context, appID, service string) error {
	_, svc, err := s.backedUpService(ctx, appID, service)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	busy, err := s.backupBusy(ctx, appID, svc)
	if err != nil {
		return err
	}
	if busy {
		return ErrBackupRunning
	}
	return nil
}

// publishPins tells the service's engine about the pins the store holds now.
// Only a Postgres backup keeps a copy of them in Nomad (its job's variable);
// the other engines are given the list with each run, so there is nothing to
// publish.
func (s *Service) publishPins(ctx context.Context, appID, service string) error {
	_, svc, err := s.backedUpService(ctx, appID, service)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !postgresBackup(svc) {
		return nil
	}
	stamps, err := s.pinStamps(ctx, appID, service)
	if err != nil {
		return err
	}
	return s.deployer.SetBackupPins(ctx, appID, service, stamps)
}

// restorePins puts the store's pins back as they were before a change that
// Nomad couldn't follow: b gets its old label (or none), and holder, the
// backup that held the label that moved, gets it back. A failure here is
// logged: the store and Nomad then disagree until the next deploy writes the
// list again.
func (s *Service) restorePins(ctx context.Context, b, holder store.BackupRecord) {
	var err error
	if b.Pin == "" {
		err = s.store.UnpinBackup(ctx, b.AppID, b.ID)
	} else {
		_, err = s.store.PinBackup(ctx, b.AppID, b.ID, b.Pin, *b.PinnedAt)
	}
	if err == nil && holder.ID != "" && holder.ID != b.ID {
		_, err = s.store.PinBackup(ctx, holder.AppID, holder.ID, holder.Pin, *holder.PinnedAt)
	}
	if err != nil {
		s.log.Error("put the pins back after Nomad refused the list; the next deploy writes it again", "app", b.AppID, "backup", b.ID, "error", err)
	}
}
