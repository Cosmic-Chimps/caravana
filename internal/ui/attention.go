package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// attentionRow is one line of the needs-you list: a mark, one sentence, when
// it happened and the one action that leads to the fix (FR-019).
type attentionRow struct {
	Mark   string // the status kind of the mark: failed or warning
	Text   sentence
	When   when
	Action string
	Href   string
}

// attentionRegion is what the "attention" region draws: the items, or one
// line saying nothing needs the owner. Unknown is set when Nomad couldn't be
// read, so the copy items may be missing and "nothing needs you" would be a
// guess.
type attentionRegion struct {
	Rows    []attentionRow
	Unknown bool
}

// loadAttention reads the needs-you list and turns it into the region. The
// items come back too, for the Apps headline, which counts the apps in them.
func (s *server) loadAttention(r *http.Request, pl places, sums []service.AppSummary) (attentionRegion, []service.AttentionItem, error) {
	att, err := s.svc.Attention(r.Context())
	if err != nil {
		return attentionRegion{}, nil, err
	}
	now := s.now()
	byID := make(map[string]service.AppSummary, len(sums))
	for _, sum := range sums {
		byID[sum.App.ID] = sum
	}
	region := attentionRegion{Unknown: att.CopiesUnknown}
	for _, it := range att.Items {
		var again *uint64
		if sum, ok := byID[it.AppID]; ok && it.Kind == service.AttentionDeployFailed && sum.LastDeployment != nil &&
			sum.LastDeployment.Deployment.ID == it.Deployment {
			// A reverted deploy says what the version that runs repeats, as the app's page does.
			again = s.repeatedFor(r.Context(), sum.LastDeployment)
		}
		region.Rows = append(region.Rows, s.attentionRowAgain(now, it, pl, byID, again))
	}
	return region, att.Items, nil
}

// attentionRow says one item in the console's words. The API sends facts;
// the sentence, the mark and the link are the console's (research R9, R12). sums
// are the apps' summaries by ID, for what runs now (nil knows nothing).
func (s *server) attentionRow(now time.Time, it service.AttentionItem, pl places, sums map[string]service.AppSummary) attentionRow {
	return s.attentionRowAgain(now, it, pl, sums, nil)
}

// attentionRowAgain is attentionRow for an item whose deployment was reverted:
// again is the deployment of the owner's that the running version repeats, or nil.
func (s *server) attentionRowAgain(now time.Time, it service.AttentionItem, pl places, sums map[string]service.AppSummary, again *uint64) attentionRow {
	row := attentionRow{Mark: "failed"}
	if !it.When.IsZero() {
		row.When = whenOf(now, &it.When, "")
	}
	appHref := "/apps/" + url.PathEscape(it.AppID)
	place := app.Place{Project: it.Project, Environment: it.Environment, Name: it.AppName}
	where := pl.label(place, it.AppID, "/"+it.Service)

	switch it.Kind {
	case service.AttentionDeployFailed:
		// The sentence is the one the app's summary says, with the app in front. What runs
		// now comes from the app's summary, when Nomad could be read.
		rec := store.DeploymentRecord{Status: it.DeploymentStatus, JobVersion: it.Version, Error: it.Detail}
		var now runningNow // an app missing from the summaries: nothing is said about what runs
		if sum, ok := sums[it.AppID]; ok {
			now = runningOf(sum.Status).repeating(again)
		}
		row.Text = join(pl.label(place, it.AppID, ""), plainS(": "), deployFailedSentence(rec, now))
		row.Action = "View deployment"
		row.Href = s.deploymentsHref(it.AppID)
		if s.hasTab("deployments") {
			row.Href += "#deployment-" + it.Deployment
		}
	case service.AttentionOutOfMemory:
		row.Text = join(plainS(copyCount(len(it.Copies))+" of "), where, plainS(" ran out of memory."))
		row.Action = "Raise memory"
		row.Href = appHref + "/configure#" + inputID(it.Service, "memory")
	case service.AttentionRestarting:
		row.Mark = "warning"
		row.Text = join(plainS(copyCount(len(it.Copies))+" of "), where, plainS(" "+verbFor(len(it.Copies), "keeps", "keep")+" restarting."))
		row.Action, row.Href = "View logs", appHref+logsQuery(it)
	case service.AttentionUnhealthy:
		row.Mark = "warning"
		row.Text = join(plainS(copyCount(len(it.Copies))+" of "), where, plainS(" "+verbFor(len(it.Copies), "is", "are")+" failing its health check."))
		row.Action, row.Href = "View logs", appHref+logsQuery(it)
	case service.AttentionBackupFailed:
		row.Text = join(plainS("The latest backup of "), where, plainS(" failed."))
		row.Action, row.Href = "Open backups", appHref+"/backups"
	case service.AttentionRestoreFailed:
		row.Text = join(plainS("The latest restore of "), where, plainS(" failed."))
		row.Action, row.Href = "Open backups", appHref+"/backups"
	case service.AttentionNoBackupDestination:
		row.Mark = "warning"
		row.Text = sentence{plain("Backups are set up, but no backup destination is.")}
		row.Action, row.Href = "Set destination", "/settings/backup-storage"
	case service.AttentionScheduleFailed:
		// Detail is the run's sentence in Caravana's words ("Exited with code 3."), never its output.
		row.Text = join(pl.label(place, it.AppID, ""), plainS(": the last run of "), sentence{mono(it.Schedule)}, plainS(" failed. "+it.Detail))
		row.Action, row.Href = "Open schedule", scheduleHref(it.ScheduleID)
	case service.AttentionScheduleSkipping:
		row.Mark = "warning"
		row.Text = join(pl.label(place, it.AppID, ""), plainS(": "+it.Detail))
		row.Action, row.Href = "Open schedule", scheduleHref(it.ScheduleID)
	case service.AttentionDiskNearlyFull:
		row.Mark = "warning"
		if it.Level == metrics.DiskFailed {
			row.Mark = "failed"
		}
		row.Text = diskSentence(it)
		row.Action, row.Href = "Open monitoring", "/monitoring#host"
	case service.AttentionDomainDNSElsewhere:
		row.Text = domainElsewhereSentence(pl.label(place, it.AppID, ""), it)
		row.Action, row.Href = "Open domain", domainHref(it.Domain)
	case service.AttentionDomainCertExpiring:
		row.Mark = "warning"
		row.Text = domainExpiringSentence(pl.label(place, it.AppID, ""), now, it)
		row.Action, row.Href = "Open domain", domainHref(it.Domain)
	}
	return row
}

// diskSentence is "The disk at /var/lib/docker is 87% full: 1.1 GB left." with the mount in mono:
// the words are notify.DiskTail's, so the console, the API's detail and the notifications say the
// same thing. What is on the disk (Nomad's data, Docker's images) is the API's `holds`; the row
// has no second line for it.
func diskSentence(it service.AttentionItem) sentence {
	return sentence{plain("The disk at "), mono(it.Mount), plain(notify.DiskTail(it.UsedPercent, it.FreeBytes))}
}

// domainElsewhereSentence is "blog: bi.example.com points at 185.199.108.153,
// which isn't this host." (the app first, as every item has it) with the names
// set apart; without an address it says "another host".
func domainElsewhereSentence(app sentence, it service.AttentionItem) sentence {
	if it.AddressSeen == "" {
		return join(app, plainS(": "), sentence{mono(it.Domain), plain(" points at another host.")})
	}
	return join(app, plainS(": "), sentence{mono(it.Domain), plain(" points at "), mono(it.AddressSeen), plain(", which isn't this host.")})
}

// domainExpiringSentence is "blog: the certificate for bi.example.com expires
// in 6 days and hasn't been renewed." (or "has expired", "expires today").
func domainExpiringSentence(app sentence, now time.Time, it service.AttentionItem) sentence {
	head, tail := join(app, plainS(": the certificate for ")), " and hasn't been renewed."
	switch {
	case it.ExpiresAt == nil:
		return join(head, sentence{mono(it.Domain), plain(" is about to expire" + tail)})
	case !it.ExpiresAt.After(now):
		return join(head, sentence{mono(it.Domain), plain(" has expired" + tail)})
	}
	days := int(it.ExpiresAt.Sub(now) / (24 * time.Hour))
	when := "expires in " + plural(days, "day")
	if days == 0 {
		when = "expires today"
	}
	return join(head, sentence{mono(it.Domain), plain(" " + when + tail)})
}

// verbFor picks the verb for n copies: "keeps" for one, "keep" for more.
func verbFor(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// logsQuery is the Logs tab, on the first copy the item names.
func logsQuery(it service.AttentionItem) string {
	if len(it.Copies) == 0 {
		return "/logs"
	}
	return "/logs?copy=" + url.QueryEscape(it.Copies[0])
}

// headlineWords says in a few words what is wrong with an app whose only
// trouble is an item on the list, to follow its name in the Apps headline.
func headlineWords(it service.AttentionItem) string {
	switch it.Kind {
	case service.AttentionDeployFailed:
		return "has a failed deploy"
	case service.AttentionOutOfMemory:
		return "has a service that ran out of memory"
	case service.AttentionRestarting:
		return "has a service that keeps restarting"
	case service.AttentionUnhealthy:
		return "has a service failing its health check"
	case service.AttentionBackupFailed:
		return "has a failed backup"
	case service.AttentionRestoreFailed:
		return "has a failed restore"
	case service.AttentionScheduleFailed:
		return "has a schedule whose last run failed"
	case service.AttentionScheduleSkipping:
		return "has a schedule being skipped"
	case service.AttentionDomainDNSElsewhere:
		return "has a domain pointing elsewhere"
	case service.AttentionDomainCertExpiring:
		return "has a certificate expiring"
	case service.AttentionDiskNearlyFull:
		return "is on a disk that is nearly full"
	}
	return fmt.Sprintf("needs you (%s)", it.Kind)
}

// partAttention answers GET /parts/attention: the needs-you region alone.
func (s *server) partAttention(w http.ResponseWriter, r *http.Request, session string) {
	pl, err := s.loadPlaces(r.Context())
	if err != nil {
		s.log.Error("read projects", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sums, err := s.svc.ListApps(r.Context())
	if err != nil {
		s.log.Error("list apps", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	region, _, err := s.loadAttention(r, pl, sums)
	if err != nil {
		s.log.Error("read what needs you", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "apps", "attention-region", region)
}
