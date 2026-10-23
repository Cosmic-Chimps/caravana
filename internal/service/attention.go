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
	AttentionScheduleFailed      AttentionKind = "schedule_failed"       // a schedule's newest real run failed (feature 022)
	AttentionScheduleSkipping    AttentionKind = "schedule_skipping"     // a schedule's newest 3 slots were skipped: a warning
)

// skippingAt is how many skipped slots in a row make a schedule an item: each
// run takes longer than the time between runs.
const skippingAt = 3

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
	// Schedule and ScheduleID name the schedule of the schedule kinds; Service
	// is then the schedule's service.
	Schedule, ScheduleID string
	// ExitCode is a failed run's exit code, when it exited with one
	// (schedule_failed). Notifications carry it; the API's item doesn't.
	ExitCode *int
	When     time.Time
	// Detail is the raw fact: Nomad's description, the deploy or backup error,
	// or, for the schedule kinds, a sentence in Caravana's words. Never a
	// run's output.
	Detail string
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
	scheds, err := s.scheduleFactsOf(ctx)
	if err != nil {
		return Attention{}, fmt.Errorf("attention: %w", err)
	}
	copies, copiesErr := s.deployer.AllCopies(ctx)
	if copiesErr != nil {
		s.log.Warn("can't read copies from Nomad; the needs-you list leaves them out", "error", copiesErr)
	}
	return attentionOf(time.Now(), apps, latest, copies, copiesErr, backups, restores, destErr == nil, scheds), nil
}

// scheduleFacts is what the schedule kinds are derived from (feature 022,
// research R5): the stored schedules, each one's newest run that ended and
// was neither skipped nor stopped (store.LatestScheduleRuns), and when the
// newest skipped slot was for each schedule whose newest 3 slots were skipped.
type scheduleFacts struct {
	Schedules []store.ScheduleRecord
	Latest    []store.ScheduleRunRecord
	Skipping  map[string]time.Time // schedule ID → its newest skipped slot
}

// scheduleFactsOf reads the scheduleFacts: one query for the schedules, one
// for their newest runs, and one per unpaused schedule for its skip streak
// (and one more when the streak is 3, for its time).
func (s *Service) scheduleFactsOf(ctx context.Context) (scheduleFacts, error) {
	scheds, err := s.store.ListSchedules(ctx, "")
	if err != nil {
		return scheduleFacts{}, err
	}
	latest, err := s.store.LatestScheduleRuns(ctx)
	if err != nil {
		return scheduleFacts{}, err
	}
	out := scheduleFacts{Schedules: scheds, Latest: latest, Skipping: map[string]time.Time{}}
	for _, sc := range scheds {
		if sc.PausedAt != nil {
			continue // a paused schedule skips nothing more
		}
		n, err := s.store.SkipStreak(ctx, sc.ID)
		if err != nil {
			return scheduleFacts{}, err
		}
		if n < skippingAt {
			continue
		}
		newest, err := s.store.ListRuns(ctx, sc.ID, 1)
		if err != nil {
			return scheduleFacts{}, err
		}
		if len(newest) == 1 {
			out.Skipping[sc.ID] = finishedOr(newest[0].FinishedAt, newest[0].StartedAt)
		}
	}
	return out, nil
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
	backups []store.BackupRecord, restores []store.RestoreRecord, hasDestination bool, scheds scheduleFacts) Attention {

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
	out.Items = append(out.Items, scheduleItems(known, scheds)...)

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
			strings.Compare(a.ScheduleID, b.ScheduleID),
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

// scheduleItems are the schedule kinds (feature 022, research R5). A schedule
// whose newest run that ended, and was neither skipped nor stopped, failed (an
// exit code, its limit, it couldn't start) is schedule_failed: a slot skipped
// or a run the owner stopped since neither hides nor clears it, and a later ok
// run clears it. A schedule whose newest 3 slots were skipped is
// schedule_skipping (facts.Skipping holds unpaused schedules only). A schedule
// or an app that no longer exists has no item. The words are Caravana's own,
// never a run's output.
func scheduleItems(apps map[string]store.AppRecord, facts scheduleFacts) []AttentionItem {
	byID := make(map[string]store.ScheduleRecord, len(facts.Schedules))
	for _, sc := range facts.Schedules {
		byID[sc.ID] = sc
	}
	var items []AttentionItem
	for _, r := range facts.Latest {
		sc, ok := byID[r.ScheduleID]
		if _, known := apps[sc.AppID]; !ok || !known || !runFailed(r.Status) {
			continue
		}
		items = append(items, AttentionItem{
			Kind: AttentionScheduleFailed, AppID: sc.AppID, Service: sc.Service, Schedule: sc.Name, ScheduleID: sc.ID,
			ExitCode: failedExitCode(r), When: finishedOr(r.FinishedAt, r.StartedAt), Detail: runFailedSentence(r, sc.StopAfter),
		})
	}
	for _, sc := range facts.Schedules {
		at, skipping := facts.Skipping[sc.ID]
		if _, known := apps[sc.AppID]; !skipping || !known {
			continue
		}
		items = append(items, AttentionItem{
			Kind: AttentionScheduleSkipping, AppID: sc.AppID, Service: sc.Service, Schedule: sc.Name, ScheduleID: sc.ID,
			When: at,
			Detail: fmt.Sprintf("%s has been skipped %d times: each run takes longer than the time between runs.",
				sc.Name, skippingAt),
		})
	}
	return items
}

// failedExitCode is the exit code a failed run exited with, or nil: a run
// stopped at its limit or that couldn't start has none worth telling, and the
// code of a run stopped outside Caravana is the signal's, not the command's.
func failedExitCode(r store.ScheduleRunRecord) *int {
	if r.Status != store.RunFailed || r.ExitCode == nil || r.Error != fmt.Sprintf("exit %d", *r.ExitCode) {
		return nil
	}
	return r.ExitCode
}

// runFailedSentence says why a failed run failed, as the schedule's detail on
// the Schedules page does: "Exited with code 3.", "Stopped at the 5 min
// limit.", "Couldn't start: Nomad refused the job." Another failure is the
// run's own words as a sentence ("Ran out of memory.").
func runFailedSentence(r store.ScheduleRunRecord, limit time.Duration) string {
	switch {
	case failedExitCode(r) != nil:
		return fmt.Sprintf("Exited with code %d.", *r.ExitCode)
	case r.Status == store.RunStoppedAtLimit:
		return "Stopped at the " + limitWords(limit) + " limit."
	case r.Status == store.RunCouldNotStart && (r.Error == "" || r.Error == "could not start"):
		return "Couldn't start."
	case r.Status == store.RunCouldNotStart:
		return "Couldn't start: " + strings.TrimSuffix(r.Error, ".") + "."
	case r.Error == "":
		return "Failed."
	}
	return strings.ToUpper(r.Error[:1]) + strings.TrimSuffix(r.Error[1:], ".") + "."
}

// limitWords is a run's limit as the Schedules page writes it: "5 min", "2 h".
func limitWords(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d min", d/time.Minute)
	}
	return d.String()
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
