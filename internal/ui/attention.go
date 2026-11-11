package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
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
	return s.loadAttentionWhere(r, pl, sums, nil)
}

// loadAttentionWhere is loadAttention for the items keep accepts (nil keeps them
// all): the project page's rail lists the items of one environment.
func (s *server) loadAttentionWhere(r *http.Request, pl places, sums []service.AppSummary, keep func(service.AttentionItem) bool) (attentionRegion, []service.AttentionItem, error) {
	att, err := s.svc.Attention(r.Context())
	if err != nil {
		return attentionRegion{}, nil, err
	}
	if keep != nil {
		att.Items = slices.DeleteFunc(slices.Clone(att.Items), func(it service.AttentionItem) bool { return !keep(it) })
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

// attentionRow says one item in the console's words. The API sends facts; the sentence's words are the
// service's (service.ProblemParts, the one source the activity feed and the host pages' banners draw
// too), and the mark, the action and the link are the console's (research R9, R12). sums are the apps'
// summaries by ID, for what runs now (nil knows nothing).
func (s *server) attentionRow(now time.Time, it service.AttentionItem, pl places, sums map[string]service.AppSummary) attentionRow {
	return s.attentionRowAgain(now, it, pl, sums, nil)
}

// attentionRowAgain is attentionRow for an item whose deployment was reverted:
// again is the deployment of the owner's that the running version repeats, or nil.
func (s *server) attentionRowAgain(now time.Time, it service.AttentionItem, pl places, sums map[string]service.AppSummary, again *uint64) attentionRow {
	row := attentionRow{Mark: "failed", Text: problemSentence(it, pl)}
	if !it.When.IsZero() {
		row.When = whenOf(now, &it.When, "")
	}
	appHref := "/apps/" + url.PathEscape(it.AppID)

	switch it.Kind {
	case service.AttentionDeployFailed:
		// The sentence is the service's, with what runs now added when Nomad could be read: that comes
		// from the app's summary.
		var running runningNow // an app missing from the summaries: nothing is said about what runs
		if sum, ok := sums[it.AppID]; ok {
			running = runningOf(sum.Status).repeating(again)
		}
		row.Text = join(row.Text, runningWords(running))
		row.Action = "View deployment"
		row.Href = s.deploymentsHref(it.AppID)
		if s.hasTab("deployments") {
			row.Href += "#deployment-" + it.Deployment
		}
	case service.AttentionOutOfMemory:
		row.Action = "Raise memory"
		row.Href = appHref + "/configure#" + inputID(it.Service, "memory")
	case service.AttentionRestarting:
		row.Mark = "warning"
		row.Action, row.Href = "View logs", appHref+logsQuery(it)
	case service.AttentionUnhealthy:
		row.Mark = "warning"
		row.Action, row.Href = "View logs", appHref+logsQuery(it)
	case service.AttentionBackupFailed:
		row.Action, row.Href = "Open backups", appHref+"/backups"
	case service.AttentionRestoreFailed:
		row.Action, row.Href = "Open backups", appHref+"/backups"
	case service.AttentionNoBackupDestination:
		row.Mark = "warning"
		row.Action, row.Href = "Set destination", "/settings/backup-storage"
	case service.AttentionScheduleFailed:
		row.Action, row.Href = "Open schedule", scheduleHref(it.ScheduleID)
	case service.AttentionScheduleSkipping:
		row.Mark = "warning"
		row.Action, row.Href = "Open schedule", scheduleHref(it.ScheduleID)
	case service.AttentionDiskNearlyFull:
		row.Mark = "warning"
		if it.Level == metrics.DiskFailed {
			row.Mark = "failed"
		}
		row.Action, row.Href = "Open monitoring", "/monitoring#host"
		if it.Server != "" { // a cluster of several: the server's own numbers (feature 031)
			row.Href = "/monitoring?server=" + url.QueryEscape(it.Server) + "#host"
		}
	case service.AttentionDomainDNSElsewhere:
		row.Action, row.Href = "Open domain", domainHref(it.Domain)
	case service.AttentionDomainCertExpiring:
		row.Mark = "warning"
		row.Action, row.Href = "Open domain", domainHref(it.Domain)
	case service.AttentionOSRebootRequired:
		row.Action, row.Href = "Open updates", "/ops/updates"
	case service.AttentionOSSecurityUpdates:
		row.Mark = "warning"
		row.Action, row.Href = "Open updates", "/ops/updates"
	case service.AttentionServerDown:
		row.Action, row.Href = "Open server", serverHref(it.Server)
	case service.AttentionServerCertExpiring:
		row.Mark = "warning"
		row.Action, row.Href = "Open server", serverHref(it.Server)
	case service.AttentionCaravanaUpdateFailed:
		// A rolled back, refused or lost update is a warning; one that failed is a failure.
		row.Mark = "warning"
		if it.Level == metrics.DiskFailed {
			row.Mark = "failed"
		}
		row.Action, row.Href = "Open updates", "/ops/updates"
	}
	return row
}

// problemSentence is an item's words: the service's parts, drawn by the "sentence" template. Every
// place that says what a needs-you item is (the list, the host pages' banners) calls it.
func problemSentence(it service.AttentionItem, pl places) sentence {
	out := sentenceOfParts(service.ProblemParts(it, pl.display))
	if it.Kind == service.AttentionOSRebootRequired {
		// The kernels and packages are versions and names: they never break inside.
		for i := range out {
			out[i].Nowrap = out[i].Mono
		}
	}
	return out
}

// sentenceOfParts is parts of the service as a sentence of the console.
func sentenceOfParts(parts []service.TextPart) sentence {
	out := make(sentence, 0, len(parts))
	for _, p := range parts {
		out = append(out, part{Text: p.Text, Mono: p.Mono, Muted: p.Muted})
	}
	return out
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

// serverHref is a server's page (feature 031).
func serverHref(name string) string { return "/ops/servers/" + url.PathEscape(name) }
