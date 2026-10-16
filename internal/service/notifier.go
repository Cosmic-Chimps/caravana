package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Notification wording (feature 018, T007). Each function turns one fact into
// a notify.Event: a title, one sentence, the raw detail and a link. The
// sentences are the console's own, ported from internal/ui/attention.go
// because service may not import ui, so a message reads like the needs-you
// list it comes from. The functions are pure: base is the console's public
// address (CARAVANA_PUBLIC_URL), and without it the events carry no link.

// maxEventDetail caps the raw detail an event carries. Nomad's and the
// registry's own messages are short, but a backup tool's output may not be, and
// the whole of it stays on the page the link leads to.
const maxEventDetail = 2000

// consoleLink returns base and path joined, or "" when the console's address is
// not known (research R6).
func consoleLink(base, path string) string {
	if base == "" {
		return ""
	}
	return base + path
}

// problemEvent says in words that item it has appeared on the needs-you list.
// at is when the fact happened when the item knows that, else when it was
// noticed.
func problemEvent(it AttentionItem, base string, at time.Time) notify.Event {
	if !it.When.IsZero() {
		at = it.When
	}
	where := it.AppID
	if it.Service != "" {
		where += "/" + it.Service
	}
	ev := notify.Event{
		Type: notify.EventProblemOpened, Kind: string(it.Kind), AppID: it.AppID, Service: it.Service,
		Detail: limitDetail(it.Detail), URL: consoleLink(base, problemPath(it.Kind, it.AppID, it.Deployment)), At: at,
	}
	n := len(it.Copies)

	switch it.Kind {
	case AttentionDeployFailed:
		verb := "failed"
		if it.DeploymentStatus == store.StatusReverted {
			verb = "was reverted"
		}
		label := versionPrefix(it.Version)
		ev.Title = fmt.Sprintf("%s: deploy%s %s", it.AppID, label, verb)
		ev.Text = fmt.Sprintf("%s's deploy%s %s.", it.AppID, label, verb)
	case AttentionOutOfMemory:
		ev.Title = where + ": ran out of memory"
		ev.Text = fmt.Sprintf("%s of %s ran out of memory.", copyCount(n), where)
	case AttentionRestarting:
		ev.Title = where + ": keeps restarting"
		ev.Text = fmt.Sprintf("%s of %s %s restarting.", copyCount(n), where, verbFor(n, "keeps", "keep"))
	case AttentionUnhealthy:
		ev.Title = where + ": failing its health check"
		ev.Text = fmt.Sprintf("%s of %s %s failing its health check.", copyCount(n), where, verbFor(n, "is", "are"))
	case AttentionBackupFailed:
		ev.Title = fmt.Sprintf("%s: the backup of %s failed", it.AppID, it.Service)
		ev.Text = fmt.Sprintf("The latest backup of %s failed.", where)
	case AttentionRestoreFailed:
		ev.Title = fmt.Sprintf("%s: the restore of %s failed", it.AppID, it.Service)
		ev.Text = fmt.Sprintf("The latest restore of %s failed.", where)
	case AttentionNoBackupDestination:
		ev.Title = "Backups have nowhere to go"
		ev.Text = "Backups are set up, but no backup destination is."
	default: // a kind added to the list and not yet worded here: still say something
		ev.Title = fmt.Sprintf("%s: needs you (%s)", where, it.Kind)
		ev.Text = ev.Title + "."
	}
	return ev
}

// resolvedEvent says that a problem already sent has been gone from the
// needs-you list long enough. It reuses the title that was sent, which the
// sent problem keeps for this, and finds the page again from the problem's key.
func resolvedEvent(p store.SentProblem, base string, at time.Time) notify.Event {
	deployment := ""
	if parts := strings.SplitN(p.Key, "|", 4); len(parts) == 4 {
		deployment = parts[3]
	}
	return notify.Event{
		Type: notify.EventProblemResolved, Kind: p.Kind, AppID: p.AppID, Service: p.Service,
		Title: "Resolved: " + p.Summary,
		Text:  "It is no longer on the needs-you list.",
		URL:   consoleLink(base, problemPath(AttentionKind(p.Kind), p.AppID, deployment)),
		At:    at,
	}
}

// deployEvent says how a deployment ended: deploy.succeeded for healthy,
// deploy.failed for failed, reverted and superseded. ok is false for a record
// that has not ended, which has nothing to tell yet.
func deployEvent(rec store.DeploymentRecord, base string) (ev notify.Event, ok bool) {
	label := versionPrefix(rec.JobVersion)
	var typ, short, long string // short ends the title, long ends the sentence
	switch rec.Status {
	case store.StatusHealthy:
		typ, short, long = notify.EventDeploySucceeded, "", "is healthy"
	case store.StatusFailed:
		typ, short, long = notify.EventDeployFailed, "failed", "failed"
	case store.StatusReverted:
		typ, short, long = notify.EventDeployFailed, "was reverted", "was reverted"
	case store.StatusSuperseded:
		typ, short, long = notify.EventDeployFailed, "was replaced", "was replaced by a newer deploy before it ended"
	default:
		return notify.Event{}, false
	}
	title := fmt.Sprintf("%s: deploy%s %s", rec.AppID, label, short)
	if rec.Status == store.StatusHealthy {
		title = fmt.Sprintf("%s: deployed%s", rec.AppID, label)
	}
	return notify.Event{
		Type: typ, AppID: rec.AppID, Title: title,
		Text:       fmt.Sprintf("%s's deploy%s %s%s.", rec.AppID, label, long, startedBy(rec.Deployment.Trigger)),
		Detail:     limitDetail(rec.Error),
		Deployment: rec.Deployment.ID, Version: rec.JobVersion, Outcome: string(rec.Status),
		Trigger: string(rec.Deployment.Trigger),
		URL:     consoleLink(base, deploymentPath(rec.AppID, rec.Deployment.ID)),
		At:      rec.UpdatedAt,
	}, true
}

// startedBy says in words what started a deployment, to follow a sentence:
// ", started from the console". An unknown trigger says nothing.
func startedBy(t app.Trigger) string {
	switch t {
	case app.TriggerConsole:
		return ", started from the console"
	case app.TriggerManual:
		return ", started by the API"
	case app.TriggerWebhook:
		return ", started by its webhook"
	}
	return ""
}

// problemPath is the console page about a needs-you item (research R6).
func problemPath(kind AttentionKind, appID, deployment string) string {
	switch kind {
	case AttentionNoBackupDestination:
		return "/settings#backup-destination"
	case AttentionDeployFailed:
		return deploymentPath(appID, deployment)
	case AttentionBackupFailed, AttentionRestoreFailed:
		return "/apps/" + url.PathEscape(appID) + "/backups"
	}
	return "/apps/" + url.PathEscape(appID)
}

// deploymentPath is a deployment's entry on the app's Deployments tab. Without
// a deployment ID it is the tab.
func deploymentPath(appID, deployment string) string {
	path := "/apps/" + url.PathEscape(appID) + "/deployments"
	if deployment != "" {
		path += "#deployment-" + deployment
	}
	return path
}

// versionPrefix is " v8" for a Nomad job version, to follow "deploy", or "" for
// a deployment that never got one (it failed before Nomad accepted the job).
func versionPrefix(v *uint64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf(" v%d", *v)
}

// copyCount is "1 copy" or "3 copies" (the console's helper of the same name).
func copyCount(n int) string {
	if n == 1 {
		return "1 copy"
	}
	return fmt.Sprintf("%d copies", n)
}

// verbFor picks the verb for n copies: "keeps" for one, "keep" for more.
func verbFor(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// limitDetail cuts a raw reason at maxEventDetail characters.
func limitDetail(detail string) string {
	if r := []rune(detail); len(r) > maxEventDetail {
		return string(r[:maxEventDetail]) + "…"
	}
	return detail
}

// The notifier (feature 018, research R1). The needs-you list is derived on
// every read and keeps nothing, so "it appeared" and "it went away" are found
// by comparing it with sent_problems, the table of what was already sent. That
// table survives a restart, which is what keeps a problem from being sent twice.

// summaryAbove is how many problems one check may send one by one. More than
// that and a node restart would bury the owner, so the check sends one summary
// per channel instead (FR-011).
const summaryAbove = 10

// summaryListed is how many problems a summary names; the rest are counted.
const summaryListed = 20

// problemKey is the identity of a needs-you item: its kind, app, service and,
// for a failed deploy, the deployment. Copy IDs are left out on purpose: a crash
// loop swaps its copies and is still one problem.
func problemKey(it AttentionItem) string {
	return strings.Join([]string{string(it.Kind), it.AppID, it.Service, it.Deployment}, "|")
}

// isCopyKind says whether a kind of item comes from Nomad's allocations, which
// can't be judged while Nomad can't be read. It takes the kind as the list
// writes it or as the table stores it (a plain string).
func isCopyKind[K ~string](kind K) bool {
	switch AttentionKind(kind) {
	case AttentionOutOfMemory, AttentionRestarting, AttentionUnhealthy:
		return true
	}
	return false
}

// problemChanges is what one comparison of the list with the table finds.
type problemChanges struct {
	Opened   []AttentionItem     // on the list, never sent: send "opened"
	Resolved []store.SentProblem // sent, and gone for resolveAfter: send "resolved"
	Seen     []string            // keys sent before and on the list now: move their last-seen time
	Dropped  []string            // keys of apps that no longer exist: forget, say nothing
}

// diffProblems compares the needs-you list now with the problems already sent.
// It is pure, so the table test calls it with whatever it needs.
//
//   - An item on the list and not in prev is opened; one in both is seen.
//   - A sent problem off the list for resolveAfter or more since it was last seen
//     is resolved. A younger one is kept as it is, so an item that flaps doesn't
//     send "resolved" and then "opened" again.
//   - When copiesKnown is false (Nomad couldn't be read) the copy kinds are not
//     judged: not opened, and not resolved, so an outage doesn't send a burst of
//     "resolved".
//   - apps holds the IDs of the apps that exist. A problem of an app that is gone
//     is dropped without a message, and an item of one is not opened: it can only
//     be an app deleted while this check was running.
func diffProblems(prev []store.SentProblem, now []AttentionItem, copiesKnown bool, apps map[string]bool,
	at time.Time, resolveAfter time.Duration) problemChanges {

	sent := make(map[string]bool, len(prev))
	for _, p := range prev {
		sent[p.Key] = true
	}

	var out problemChanges
	present := map[string]bool{} // the keys on the list that this check judges
	for _, it := range now {
		key := problemKey(it)
		switch {
		case present[key]: // the same item twice: one problem
			continue
		case it.AppID != "" && !apps[it.AppID]:
			continue
		case !copiesKnown && isCopyKind(it.Kind):
			continue
		}
		present[key] = true
		if sent[key] {
			out.Seen = append(out.Seen, key)
		} else {
			out.Opened = append(out.Opened, it)
		}
	}

	for _, p := range prev {
		switch {
		case present[p.Key]:
		case p.AppID != "" && !apps[p.AppID]:
			out.Dropped = append(out.Dropped, p.Key)
		case !copiesKnown && isCopyKind(p.Kind):
			// Not judged: it may well still be there.
		case at.Sub(p.LastSeenAt) >= resolveAfter:
			out.Resolved = append(out.Resolved, p)
		}
	}
	return out
}

// runNotifier checks the needs-you list every gap until ctx ends (research R1).
//
// It is woken by a timer alone. The console's own refresh comes from the change
// hub, but subscribing to it would keep the copies watcher (one blocking query
// on every allocation in Nomad) running for as long as Caravana does, where it
// now only runs while a page is open. A check every 30 seconds is well inside
// "within a minute or two" (FR-003), and the first one waits a gap, so a
// Caravana that starts before Nomad does doesn't look at a cluster that isn't
// up yet.
func (s *Service) runNotifier(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.checkProblems(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("check the needs-you list for notifications; trying again", "error", err)
			}
		}
	}
}

// checkProblems is one pass of the notifier: read the needs-you list, work out
// what is new and what is over, and in one transaction record that and queue a
// message for each channel that wants problems. Nothing is sent here: the
// sender does that, so a slow receiver can't hold a check up.
//
// When anything fails nothing is recorded, and the next check finds the same
// changes again.
func (s *Service) checkProblems(ctx context.Context) error {
	att, err := s.Attention(ctx)
	if err != nil {
		return err
	}
	prev, err := s.store.ListSentProblems(ctx)
	if err != nil {
		return err
	}
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return err
	}
	channels, err := s.store.ListNotificationChannels(ctx)
	if err != nil {
		return err
	}

	exists := make(map[string]bool, len(apps))
	for _, rec := range apps {
		exists[rec.App.ID] = true
	}
	now := s.clock.Now().UTC()
	changes := diffProblems(prev, att.Items, !att.CopiesUnknown, exists, now, s.resolveAfter)

	check := store.ProblemCheck{Seen: changes.Seen, SeenAt: now, Removed: slices.Clone(changes.Dropped)}
	var opened, resolved []notify.Event
	for _, it := range changes.Opened {
		ev := problemEvent(it, s.publicURL, now)
		opened = append(opened, ev)
		check.Opened = append(check.Opened, store.SentProblem{
			Key: problemKey(it), Kind: string(it.Kind), AppID: it.AppID, Service: it.Service,
			OpenedAt: now, LastSeenAt: now, Summary: ev.Title,
		})
	}
	for _, p := range changes.Resolved {
		resolved = append(resolved, resolvedEvent(p, s.publicURL, now))
		check.Removed = append(check.Removed, p.Key)
	}

	events := append(summarize(opened, "new problems", notify.EventProblemOpened, s.publicURL, now),
		summarize(resolved, "problems resolved", notify.EventProblemResolved, s.publicURL, now)...)
	for _, ch := range channelsWanting(channels, EventProblems) {
		for _, ev := range events {
			d, err := newDelivery(ch.ID, ev, now)
			if err != nil {
				return err
			}
			check.Deliveries = append(check.Deliveries, d)
		}
	}
	if err := s.store.ApplyProblemCheck(ctx, check); err != nil {
		return err
	}
	if len(check.Deliveries) > 0 {
		s.queue.poke() // send them now, not at the sender's next look
	}
	return nil
}

// summarize returns events as they are, or, when there are more than
// summaryAbove of them, one summary event listing them (FR-011). word is what
// the summary's title counts: "3 new problems".
func summarize(events []notify.Event, word, typ, base string, at time.Time) []notify.Event {
	if len(events) <= summaryAbove {
		return events
	}
	var lines []string
	for i, ev := range events {
		if i == summaryListed {
			lines = append(lines, fmt.Sprintf("…and %d more.", len(events)-summaryListed))
			break
		}
		lines = append(lines, "- "+ev.Title)
	}
	return []notify.Event{{
		Type:  typ,
		Title: fmt.Sprintf("%d %s", len(events), word),
		Text:  strings.Join(lines, "\n"),
		URL:   consoleLink(base, "/"),
		At:    at,
	}}
}

// channelsWanting returns the channels that are not paused and want event
// (EventProblems, EventDeployFailed or EventDeploySucceeded).
func channelsWanting(channels []store.NotificationChannel, event string) []store.NotificationChannel {
	var out []store.NotificationChannel
	for _, ch := range channels {
		if !ch.Paused && slices.Contains(ch.Events, event) {
			out = append(out, ch)
		}
	}
	return out
}

// newDelivery makes a pending delivery of ev to a channel, due at once. The
// payload is the event as JSON, with no secret in it; the sender renders it
// again for the channel's kind.
func newDelivery(channelID string, ev notify.Event, at time.Time) (store.Delivery, error) {
	id, err := newID("ndl_")
	if err != nil {
		return store.Delivery{}, err
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return store.Delivery{}, fmt.Errorf("encode a notification: %w", err)
	}
	return store.Delivery{
		ID: id, ChannelID: channelID, EventType: ev.Type, Title: ev.Title, Payload: string(payload),
		Status: store.DeliveryPending, NextAt: at, CreatedAt: at,
	}, nil
}
