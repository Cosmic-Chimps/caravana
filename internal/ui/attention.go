package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
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
	}
	return row
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
