package service

import (
	"fmt"
	"net/url"
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
