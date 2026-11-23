package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Copies of backups (feature 052, plan D7). When a backup first succeeds,
// Caravana queues a copy of it to each destination its policy names in copyTo.
// Each copy is a one-off Nomad job (nomadjob.BackupCopyJob) that runs rclone
// between the two destinations, started and settled by copyPass at the end of
// every pass of the backup watcher, so a restart of Caravana loses nothing: the
// rows are in SQLite and the jobs in Nomad.

const (
	// maxRunningCopies is how many copy jobs run at once.
	maxRunningCopies = 2
	// copyLimit is how long a copy may run before it is failed.
	copyLimit = 6 * time.Hour
)

// queueCopies queues a pending copy of the backup rec, which has just succeeded
// and was recorded, to each destination in its service's current copyTo, and
// wakes the pass that starts them. A service without a policy or with no copies
// queues nothing. An error is logged: the backup itself is safe.
func (s *Service) queueCopies(ctx context.Context, rec store.BackupRecord) {
	if rec.Status != store.BackupSucceeded {
		return
	}
	_, svc, err := s.backedUpService(ctx, rec.AppID, rec.Service)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("queue the copies of a backup", "app", rec.AppID, "backup", rec.ID, "error", err)
		}
		return
	}
	if len(svc.Backup.CopyTo) == 0 {
		return
	}
	if err := s.store.QueueBackupCopies(ctx, rec.AppID, rec.ID, svc.Backup.CopyTo); err != nil {
		s.log.Warn("queue the copies of a backup", "app", rec.AppID, "backup", rec.ID, "error", err)
		return
	}
	s.wakes.wake(backupsKey)
}

// copyPass settles the copies that were running, then starts pending ones while
// fewer than maxRunningCopies run. Errors are logged; the next pass tries again.
func (s *Service) copyPass(ctx context.Context) {
	s.copyMu.Lock()
	defer s.copyMu.Unlock()
	s.runCopyPass(ctx)
}

// runCopyPass is copyPass for a caller that holds copyMu (a test that keeps the
// watcher's own passes out while it plays Nomad).
func (s *Service) runCopyPass(ctx context.Context) {
	open, err := s.store.OpenBackupCopies(ctx)
	if err != nil {
		s.log.Warn("backup copies: list the open ones", "error", err)
		return
	}
	if len(open) == 0 {
		return
	}
	running := 0
	for _, c := range open {
		if c.Status != store.CopyRunning {
			continue
		}
		if s.settleCopy(ctx, c) {
			running++
		}
	}
	for _, c := range open {
		if running >= maxRunningCopies {
			break
		}
		if c.Status != store.CopyPending {
			continue
		}
		if s.startCopy(ctx, c) {
			running++
		}
	}
}

// settleCopy reads how a running copy is doing. A final outcome is saved and the
// job purged; it reports whether the copy is still running.
func (s *Service) settleCopy(ctx context.Context, c store.BackupCopy) (stillRunning bool) {
	res, err := s.deployer.BackupCopyOutcome(ctx, c.JobID, s.copyKeys(ctx, c)...)
	if err != nil {
		s.log.Warn("backup copies: read a copy", "app", c.AppID, "backup", c.BackupID, "destination", c.Destination, "error", err)
		return true
	}
	if res.Status == deploy.RunRunning {
		if c.StartedAt == nil || s.clock.Now().Sub(*c.StartedAt) <= copyLimit {
			return true
		}
		res = deploy.BackupCopyResult{Status: deploy.RunFailed, Message: fmt.Sprintf("the copy didn't finish in %s", copyLimit)}
	}
	now := s.clock.Now().UTC()
	c.FinishedAt = &now
	if res.Status == deploy.RunSucceeded {
		c.Status, c.Object, c.Bytes, c.Message = store.CopySucceeded, res.Object, res.Bytes, res.Message
	} else {
		c.Status, c.Message = store.CopyFailed, res.Message
	}
	if err := s.store.SaveBackupCopy(ctx, c); err != nil {
		s.log.Warn("backup copies: save a copy", "app", c.AppID, "backup", c.BackupID, "destination", c.Destination, "error", err)
		return true
	}
	if len(res.Expired) > 0 {
		ids := make([]string, len(res.Expired))
		for i, name := range res.Expired {
			ids[i] = expiredRecordID(c.Service, name)
		}
		if err := s.store.ExpireBackupCopies(ctx, c.AppID, c.Destination, ids); err != nil {
			s.log.Warn("backup copies: expire", "app", c.AppID, "destination", c.Destination, "error", err)
		}
	}
	if err := s.deployer.PurgeBackupCopy(ctx, c.JobID); err != nil {
		s.log.Warn("backup copies: purge a copy's job", "app", c.AppID, "job", c.JobID, "error", err)
	}
	s.log.Info("backup copy finished", "app", c.AppID, "service", c.Service, "backup", c.BackupID, "destination", c.Destination, "status", c.Status)
	s.backupsChanged(c.AppID)
	return false
}

// copyKeys are the two destinations of a copy, with their secret keys, so that
// no message of the copy can carry one. A destination that is gone or can't be
// read is left out.
func (s *Service) copyKeys(ctx context.Context, c store.BackupCopy) []deploy.BackupDestination {
	var out []deploy.BackupDestination
	for _, name := range s.copySourceNames(ctx, c) {
		if d, ok, err := s.destinationNamed(ctx, name); err == nil && ok {
			out = append(out, d)
		}
	}
	return out
}

// copySourceNames are the names of the destinations a copy reads and writes:
// where its backup went, and the copy's own.
func (s *Service) copySourceNames(ctx context.Context, c store.BackupCopy) []string {
	names := []string{c.Destination}
	if b, err := s.store.GetBackup(ctx, c.AppID, c.BackupID); err == nil && b.Destination != "" {
		names = append(names, b.Destination)
	}
	return names
}

// failCopy saves the pending copy c as failed, with the reason in Caravana's words.
func (s *Service) failCopy(ctx context.Context, c store.BackupCopy, message string) {
	now := s.clock.Now().UTC()
	c.Status, c.Message, c.FinishedAt = store.CopyFailed, message, &now
	if err := s.store.SaveBackupCopy(ctx, c); err != nil {
		s.log.Warn("backup copies: fail a copy", "app", c.AppID, "backup", c.BackupID, "destination", c.Destination, "error", err)
		return
	}
	s.log.Info("backup copy failed", "app", c.AppID, "service", c.Service, "backup", c.BackupID, "destination", c.Destination, "message", message)
	s.backupsChanged(c.AppID)
}

// startCopy starts the job of a pending copy. It reports whether a job now runs.
// A copy that can't be made at all (its backup is gone, a destination was
// removed, the policy is gone) is failed with the reason; one that fails for
// want of Nomad or of a pin list stays pending for the next pass.
func (s *Service) startCopy(ctx context.Context, c store.BackupCopy) bool {
	b, err := s.store.GetBackup(ctx, c.AppID, c.BackupID)
	if err != nil {
		s.log.Warn("backup copies: read the backup", "app", c.AppID, "backup", c.BackupID, "error", err)
		return false
	}
	if b.Status != store.BackupSucceeded {
		s.failCopy(ctx, c, "the backup didn't succeed")
		return false
	}
	_, svc, err := s.backedUpService(ctx, c.AppID, c.Service)
	if errors.Is(err, store.ErrNotFound) {
		s.failCopy(ctx, c, "the service no longer has a backup policy")
		return false
	}
	if err != nil {
		s.log.Warn("backup copies: read the service", "app", c.AppID, "service", c.Service, "error", err)
		return false
	}
	// Where the backup is: its own destination, or for a record from before
	// destinations had names, where the policy sends backups now.
	sourceName := b.Destination
	if sourceName == "" {
		if sourceName, _, err = s.backupPlaces(ctx, c.AppID, *svc.Backup); err != nil {
			s.failCopy(ctx, c, "the backup's destination is unknown")
			return false
		}
	}
	if b.Expired {
		s.failCopy(ctx, c, fmt.Sprintf("the backup was deleted from %s by retention before it was copied", sourceName))
		return false
	}
	if sourceName == c.Destination {
		s.failCopy(ctx, c, fmt.Sprintf("the backup is already in %s", c.Destination))
		return false
	}
	source, ok, err := s.destinationNamed(ctx, sourceName)
	if err != nil {
		s.log.Warn("backup copies: read a destination", "destination", sourceName, "error", err)
		return false
	}
	if !ok {
		s.failCopy(ctx, c, fmt.Sprintf("the destination %s was removed", sourceName))
		return false
	}
	target, ok, err := s.destinationNamed(ctx, c.Destination)
	if err != nil {
		s.log.Warn("backup copies: read a destination", "destination", c.Destination, "error", err)
		return false
	}
	if !ok {
		s.failCopy(ctx, c, fmt.Sprintf("the destination %s was removed", c.Destination))
		return false
	}
	// The backup's path below the source's own base is the same below the target's.
	rel, under := strings.CutPrefix(b.Object, deploy.DestinationBase(source)+"/")
	if !under || rel == "" {
		s.failCopy(ctx, c, fmt.Sprintf("the backup isn't under the bucket and prefix %s has now", sourceName))
		return false
	}
	pins, err := s.pinStamps(ctx, c.AppID, c.Service)
	if err != nil {
		s.log.Warn("backup copies: read the pins", "app", c.AppID, "service", c.Service, "error", err)
		return false
	}
	kind := nomadjob.CopyKindFile
	if strings.HasSuffix(b.Object, "/") {
		kind = nomadjob.CopyKindDir
	}
	spec := deploy.BackupCopySpec{
		AppID: c.AppID, Service: c.Service, Source: source, Target: target,
		SrcObject: b.Object, DestObject: deploy.DestinationBase(target) + "/" + rel,
		DestPath: deploy.BackupPath(c.AppID, c.Service, target),
		Kind:     kind, Ext: nomadjob.ObjectExt(b.Engine), Keep: svc.Backup.CopyKeepOrDefault(), Pinned: pins,
	}
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.log.Warn("backup copies: make an ID", "error", err)
		return false
	}
	spec.ID = hex.EncodeToString(raw[:])

	jobID, err := s.deployer.StartBackupCopy(ctx, spec)
	if err != nil {
		if errors.Is(err, deploy.ErrNomadRejected) {
			s.failCopy(ctx, c, "Nomad refused the copy: "+err.Error())
			return false
		}
		s.log.Warn("backup copies: start a copy", "app", c.AppID, "backup", c.BackupID, "destination", c.Destination, "error", err)
		return false
	}
	now := s.clock.Now().UTC()
	c.Status, c.JobID, c.StartedAt = store.CopyRunning, jobID, &now
	if err := s.store.SaveBackupCopy(ctx, c); err != nil {
		s.log.Warn("backup copies: record a started copy", "app", c.AppID, "job", jobID, "error", err)
		// The job runs with no record; the next pass would start a second one. Take it back.
		if perr := s.deployer.PurgeBackupCopy(ctx, jobID); perr != nil {
			s.log.Warn("backup copies: purge a copy's job", "job", jobID, "error", perr)
		}
		return false
	}
	s.log.Info("backup copy started", "app", c.AppID, "service", c.Service, "backup", c.BackupID, "destination", c.Destination, "job", jobID)
	s.backupsChanged(c.AppID)
	return true
}
