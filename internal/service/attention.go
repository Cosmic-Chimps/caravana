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

// recentFailure is how long a copy that failed still counts as a problem. Nomad
// restarts a copy twice, then fails it and schedules a replacement after a
// growing delay (30 s up to an hour), so a crash loop shows as failed copies
// and fresh ones, and no one copy restarts three times.
const recentFailure = time.Hour

// AttentionItem is one thing that needs the owner. It holds the facts; the
// console turns them into a sentence and the API sends them as they are.
type AttentionItem struct {
	Kind       AttentionKind
	AppID      string   // "" for no_backup_destination
	Service    string   // "" for deploy_failed and no_backup_destination
	Copies     []string // the copy IDs it is about (copy kinds)
	Deployment string   // deploy_failed: the deployment ID
	// Project, Environment and AppName are where AppID is, from the stored place
	// (empty without an AppID).
	Project, Environment, AppName string
	// DeploymentStatus is the deployment's final status, failed or reverted,
	// for deploy_failed.
	DeploymentStatus store.Status
	Version          *uint64 // deploy_failed: its job version, when registered
	When             time.Time
	Detail           string // the raw fact: Nomad's description, the deploy or backup error
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
	return attentionOf(time.Now(), apps, latest, copies, copiesErr, backups, restores, destErr == nil), nil
}

// attentionOf applies the rules of data-model "Attention item". It is pure:
// no I/O, so the table test calls it directly. copiesErr is non-nil when
// Nomad couldn't be read; copies is then ignored and the list says so.
//
// A copy gives at most one item, the first that fits of out of memory,
// restarting and unhealthy, and copies of the version a failed deployment
// registered give none: the failed deploy is the one problem. Copy items of
// one kind in one service become one item.
func attentionOf(now time.Time, apps []store.AppRecord, latest map[string]store.DeploymentRecord,
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
				DeploymentStatus: d.Status, Version: d.JobVersion, When: d.UpdatedAt, Detail: d.Error,
			})
		}
		for _, svc := range a.Services {
			if svc.Backup != nil && !hasDestination && (noDestination == nil || rec.UpdatedAt.After(*noDestination)) {
				t := rec.UpdatedAt
				noDestination = &t
			}
		}

		// A stopped app (feature 020.5) has no copies on purpose: its ended
		// copies are no crash, and the copy kinds have nothing to say.
		if copiesErr != nil || rec.StoppedAt != nil {
			continue
		}
		var skip *uint64 // the job version a failed deployment registered
		if failed {
			skip = d.JobVersion
		}
		out.Items = append(out.Items, copyItems(now, a.ID, copies[a.ID], skip, rollingOut)...)
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

	for i, it := range out.Items {
		if rec, ok := known[it.AppID]; ok {
			out.Items[i].Project, out.Items[i].Environment, out.Items[i].AppName = rec.Place.Project, rec.Place.Environment, rec.Place.Name
		}
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

// copyItems turns one app's copies into items, per service (data-model
// "Attention item"). Considered are the copies that haven't ended, and the
// ones that failed within recentFailure: stopped copies never count, and
// neither do copies of skip, the version a failed deployment registered (that
// deploy is the one problem). In a service:
//
//   - copies killed for memory are one out_of_memory item;
//   - of the others, restarts plus one per failed copy reaching restartingAt
//     is one restarting item over the copies that restarted or failed. The
//     restarts Caravana asked for (a volume backup's) don't count, and
//     neither do those of a copy that hasn't restarted within recentFailure;
//   - a copy failing its health check, outside a rollout, that is in neither
//     is one unhealthy item. A copy gives at most one item.
func copyItems(now time.Time, appID string, copies []Copy, skip *uint64, rollingOut bool) []AttentionItem {
	var services []string // in the order they first show up
	byService := map[string][]Copy{}
	for _, c := range copies {
		failedRecently := c.Status == deploy.CopyFailed && c.FinishedAt != nil && now.Sub(*c.FinishedAt) <= recentFailure
		if (c.Terminal && !failedRecently) || (skip != nil && c.Version == *skip) {
			continue
		}
		if _, ok := byService[c.Service]; !ok {
			services = append(services, c.Service)
		}
		byService[c.Service] = append(byService[c.Service], c)
	}

	var items []AttentionItem
	for _, name := range services {
		oom := AttentionItem{Kind: AttentionOutOfMemory, AppID: appID, Service: name}
		restarting := AttentionItem{Kind: AttentionRestarting, AppID: appID, Service: name}
		unhealthy := AttentionItem{Kind: AttentionUnhealthy, AppID: appID, Service: name}
		restarts := 0
		for _, c := range byService[name] {
			// The restarts nobody asked for. A volume backup restarts the
			// copy's tasks, and three nightly backups must not make a healthy
			// app look like a crash loop. Both counts are sums over the same
			// tasks (see deploy.Copy), so the difference is the crashes.
			crashes := max(c.Restarts-c.RequestedRestarts, 0)
			// A copy's restarts add up over its whole life. One that last
			// restarted more than recentFailure ago has run cleanly since, so
			// its old restarts are no loop any more (as for failed copies).
			if c.Status != deploy.CopyFailed && (c.LastRestart == nil || now.Sub(*c.LastRestart) > recentFailure) {
				crashes = 0
			}
			switch {
			case c.OutOfMemory:
				oom.add(c, eventTime(c))
			case crashes > 0 || c.Status == deploy.CopyFailed:
				restarts += crashes
				if c.Status == deploy.CopyFailed {
					restarts++
				}
				restarting.add(c, eventTime(c))
			}
		}
		if restarts < restartingAt {
			restarting.Copies = nil // too few to be a loop: those copies are judged on their health alone
		}
		for _, c := range byService[name] {
			if c.Status == deploy.CopyUnhealthy && !rollingOut && !c.OutOfMemory && !slices.Contains(restarting.Copies, c.ID) {
				start := time.Time{}
				if c.StartedAt != nil {
					start = *c.StartedAt
				}
				unhealthy.add(c, start)
			}
		}
		for _, it := range []AttentionItem{oom, restarting, unhealthy} {
			if len(it.Copies) > 0 {
				items = append(items, it)
			}
		}
	}
	return items
}

// add puts a copy on a copy item. The item is dated by its newest copy, and
// says what Nomad says of that one.
func (it *AttentionItem) add(c Copy, when time.Time) {
	it.Copies = append(it.Copies, c.ID)
	if len(it.Copies) == 1 || when.After(it.When) {
		it.When, it.Detail = when, c.Description
	}
}

// eventTime is when a copy last restarted or ended, or started when neither
// has happened; the zero time when none is known.
func eventTime(c Copy) time.Time {
	var newest time.Time
	for _, t := range []*time.Time{c.LastRestart, c.FinishedAt} {
		if t != nil && t.After(newest) {
			newest = *t
		}
	}
	if newest.IsZero() && c.StartedAt != nil {
		return *c.StartedAt
	}
	return newest
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
