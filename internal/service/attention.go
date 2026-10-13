package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// AttentionKind says what needs the owner (research R9).
type AttentionKind string

// The kinds of item on the needs-you list.
const (
	AttentionDeployFailed        AttentionKind = "deploy_failed"         // the latest deployment failed or was reverted
	AttentionOutOfMemory         AttentionKind = "out_of_memory"         // a copy was killed for passing its memory ceiling
	AttentionRestarting          AttentionKind = "restarting"            // a copy keeps restarting
	AttentionUnhealthy           AttentionKind = "unhealthy"             // a copy fails its health check, outside a rollout
	AttentionBackupFailed        AttentionKind = "backup_failed"         // a service's newest backup failed
	AttentionRestoreFailed       AttentionKind = "restore_failed"        // a service's newest restore failed
	AttentionNoBackupDestination AttentionKind = "no_backup_destination" // backups are asked for and nowhere to put them
)

// restartingAt is how many restarts make a copy "keep restarting". One or two
// are an ordinary hiccup; Nomad's own restart policy stops at a few more.
const restartingAt = 3

// AttentionItem is one thing that needs the owner. It holds the facts; the
// console turns them into a sentence and the API sends them as they are.
type AttentionItem struct {
	Kind       AttentionKind
	AppID      string   // "" for no_backup_destination
	Service    string   // "" for deploy_failed and no_backup_destination
	Copies     []string // the copy IDs it is about (copy kinds)
	Deployment string   // deploy_failed: the deployment ID
	Version    *uint64  // deploy_failed: its job version, when registered
	When       time.Time
	Detail     string // the raw fact: Nomad's description, the deploy or backup error
}

// Attention is the needs-you list.
type Attention struct {
	Items         []AttentionItem // newest first
	CopiesUnknown bool            // Nomad couldn't be read: the copy kinds are missing
}

// Attention builds the needs-you list from what is stored and what Nomad
// says now (research R9). Nothing is kept: an item is there while its
// condition is, so it needs no acknowledging and goes by itself. When Nomad
// can't be read the error is logged and the list says its copies are unknown,
// as ListApps does for statuses.
func (s *Service) Attention(ctx context.Context) (Attention, error) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return Attention{}, fmt.Errorf("attention: %w", err)
	}
	latest, err := s.store.LatestDeployments(ctx)
	if err != nil {
		return Attention{}, fmt.Errorf("attention: %w", err)
	}
	backups, restores, err := s.store.LatestBackupRuns(ctx)
	if err != nil {
		return Attention{}, fmt.Errorf("attention: %w", err)
	}
	_, destErr := s.store.GetBackupDestination(ctx)
	if destErr != nil && !errors.Is(destErr, store.ErrNotFound) { // not found is "no destination"
		return Attention{}, fmt.Errorf("attention: %w", destErr)
	}
	copies, copiesErr := s.deployer.AllCopies(ctx)
	if copiesErr != nil {
		s.log.Warn("can't read copies from Nomad; the needs-you list leaves them out", "error", copiesErr)
	}
	return attentionOf(apps, latest, copies, copiesErr, backups, restores, destErr == nil), nil
}

// attentionOf applies the rules of data-model "Attention item". It is pure:
// no I/O, so the table test calls it directly. copiesErr is non-nil when
// Nomad couldn't be read; copies is then ignored and the list says so.
//
// A copy gives at most one item, the first that fits of out of memory,
// restarting and unhealthy, and copies of the version a failed deployment
// registered give none: the failed deploy is the one problem. Copy items of
// one kind in one service become one item.
func attentionOf(apps []store.AppRecord, latest map[string]store.DeploymentRecord,
	copies map[string][]Copy, copiesErr error,
	backups []store.BackupRecord, restores []store.RestoreRecord, hasDestination bool) Attention {

	out := Attention{CopiesUnknown: copiesErr != nil}
	known := map[string]store.AppRecord{} // the apps, by ID
	var noDestination *time.Time          // the newest update of an app that wants backups

	for _, rec := range apps {
		a := rec.App
		known[a.ID] = rec
		d, hasLatest := latest[a.ID]
		failed := hasLatest && (d.Status == store.StatusFailed || d.Status == store.StatusReverted)
		rollingOut := hasLatest && (d.Status == store.StatusPending || d.Status == store.StatusRunning)

		if failed {
			out.Items = append(out.Items, AttentionItem{
				Kind: AttentionDeployFailed, AppID: a.ID, Deployment: d.Deployment.ID,
				Version: d.JobVersion, When: d.UpdatedAt, Detail: d.Error,
			})
		}
		for _, svc := range a.Services {
			if svc.Backup != nil && !hasDestination && (noDestination == nil || rec.UpdatedAt.After(*noDestination)) {
				t := rec.UpdatedAt
				noDestination = &t
			}
		}

		if copiesErr != nil {
			continue
		}
		// Merged copy items, in the order their services first show up.
		var merged []*AttentionItem
		find := func(kind AttentionKind, service string) *AttentionItem {
			for _, it := range merged {
				if it.Kind == kind && it.Service == service {
					return it
				}
			}
			it := &AttentionItem{Kind: kind, AppID: a.ID, Service: service}
			merged = append(merged, it)
			return it
		}
		for _, c := range copies[a.ID] {
			if c.Terminal || (failed && d.JobVersion != nil && c.Version == *d.JobVersion) {
				continue
			}
			var kind AttentionKind
			switch {
			case c.OutOfMemory:
				kind = AttentionOutOfMemory
			case c.Restarts >= restartingAt:
				kind = AttentionRestarting
			case c.Status == deploy.CopyUnhealthy && !rollingOut:
				kind = AttentionUnhealthy
			default:
				continue
			}
			it := find(kind, c.Service)
			it.Copies = append(it.Copies, c.ID)
			if when := copyTime(c, kind); len(it.Copies) == 1 || when.After(it.When) {
				it.When, it.Detail = when, c.Description
			}
		}
		for _, it := range merged {
			out.Items = append(out.Items, *it)
		}
	}

	for _, b := range backups {
		rec, ok := known[b.AppID]
		if ok && b.Status == store.BackupFailed && keepsBackups(rec, b.Service) {
			out.Items = append(out.Items, AttentionItem{
				Kind: AttentionBackupFailed, AppID: b.AppID, Service: b.Service,
				When: finishedOr(b.FinishedAt, b.StartedAt), Detail: b.Message,
			})
		}
	}
	for _, r := range restores {
		rec, ok := known[r.AppID]
		if ok && r.Status == store.BackupFailed && keepsBackups(rec, r.Service) {
			out.Items = append(out.Items, AttentionItem{
				Kind: AttentionRestoreFailed, AppID: r.AppID, Service: r.Service,
				When: finishedOr(r.FinishedAt, r.CreatedAt), Detail: r.Message,
			})
		}
	}
	if noDestination != nil {
		out.Items = append(out.Items, AttentionItem{Kind: AttentionNoBackupDestination, When: *noDestination})
	}

	slices.SortStableFunc(out.Items, func(a, b AttentionItem) int {
		return cmp.Or(
			b.When.Compare(a.When), // newest first
			strings.Compare(a.AppID, b.AppID),
			strings.Compare(a.Service, b.Service),
			strings.Compare(string(a.Kind), string(b.Kind)),
		)
	})
	return out
}

// copyTime is when a copy's problem happened: its last restart for the
// restart kinds, its start for an unhealthy copy (nothing says when it
// turned unhealthy). A copy with neither is the zero time.
func copyTime(c Copy, kind AttentionKind) time.Time {
	var t *time.Time
	switch {
	case kind != AttentionUnhealthy && c.LastRestart != nil:
		t = c.LastRestart
	default:
		t = c.StartedAt
	}
	if t == nil {
		return time.Time{}
	}
	return *t
}

// keepsBackups reports whether the app's service still has a backup policy:
// a failed run of a policy since removed has nothing left to fix.
func keepsBackups(rec store.AppRecord, service string) bool {
	for _, svc := range rec.App.Services {
		if svc.Name == service {
			return svc.Backup != nil
		}
	}
	return false
}

func finishedOr(finished *time.Time, started time.Time) time.Time {
	if finished != nil {
		return *finished
	}
	return started
}
