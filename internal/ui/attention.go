package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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
func (s *server) loadAttention(r *http.Request) (attentionRegion, []service.AttentionItem, error) {
	att, err := s.svc.Attention(r.Context())
	if err != nil {
		return attentionRegion{}, nil, err
	}
	now := s.now()
	region := attentionRegion{Unknown: att.CopiesUnknown}
	for _, it := range att.Items {
		region.Rows = append(region.Rows, s.attentionRow(r.Context(), now, it))
	}
	return region, att.Items, nil
}

// attentionRow says one item in the console's words. The API sends facts;
// the sentence, the mark and the link are the console's (research R9, R12).
func (s *server) attentionRow(ctx context.Context, now time.Time, it service.AttentionItem) attentionRow {
	row := attentionRow{Mark: "failed"}
	if !it.When.IsZero() {
		row.When = whenOf(now, &it.When, "")
	}
	app := "/apps/" + url.PathEscape(it.AppID)
	where := mono(it.AppID + "/" + it.Service)

	switch it.Kind {
	case service.AttentionDeployFailed:
		verb := "failed"
		if rec, err := s.svc.GetDeployment(ctx, it.AppID, it.Deployment); err == nil && rec.Status == store.StatusReverted {
			verb = "was reverted"
		}
		row.Text = sentence{mono(it.AppID), plain("'s deploy ")}
		if v := versionLabel(it.Version); v != "" {
			row.Text = append(row.Text, mono(v), plain(" "))
		}
		row.Text = append(row.Text, plain(verb+detailTail(it.Detail)))
		row.Action = "View deployment"
		row.Href = s.deploymentsHref(it.AppID)
		if s.hasTab("deployments") {
			row.Href += "#deployment-" + it.Deployment
		}
	case service.AttentionOutOfMemory:
		row.Text = sentence{plain(copyCount(len(it.Copies)) + " of "), where, plain(" ran out of memory.")}
		row.Action = "Raise memory"
		row.Href = app + "/configure#" + inputID(it.Service, "memory")
	case service.AttentionRestarting:
		row.Mark = "warning"
		row.Text = sentence{plain(copyCount(len(it.Copies)) + " of "), where, plain(" " + verbFor(len(it.Copies), "keeps", "keep") + " restarting.")}
		row.Action, row.Href = "View logs", app+logsQuery(it)
	case service.AttentionUnhealthy:
		row.Mark = "warning"
		row.Text = sentence{plain(copyCount(len(it.Copies)) + " of "), where, plain(" " + verbFor(len(it.Copies), "is", "are") + " failing its health check.")}
		row.Action, row.Href = "View logs", app+logsQuery(it)
	case service.AttentionBackupFailed:
		row.Text = sentence{plain("The latest backup of "), where, plain(" failed.")}
		row.Action, row.Href = "Open backups", app+"/backups"
	case service.AttentionRestoreFailed:
		row.Text = sentence{plain("The latest restore of "), where, plain(" failed.")}
		row.Action, row.Href = "Open backups", app+"/backups"
	case service.AttentionNoBackupDestination:
		row.Mark = "warning"
		row.Text = sentence{plain("Backups are set up, but no backup destination is.")}
		row.Action, row.Href = "Set destination", "/settings#backup-destination"
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

// maxDetail is how much of a raw error the sentence carries; the whole of it
// is on the deployment the action leads to.
const maxDetail = 200

// detailTail is ": <detail>." for a deploy's error, on one line and cut when
// long, or "." when there is none.
func detailTail(detail string) string {
	d := strings.Join(strings.Fields(detail), " ")
	if d == "" {
		return "."
	}
	if r := []rune(d); len(r) > maxDetail {
		d = string(r[:maxDetail]) + "…"
	}
	return ": " + strings.TrimRight(d, ".") + "."
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
	region, _, err := s.loadAttention(r)
	if err != nil {
		s.log.Error("read what needs you", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "apps", "attention-region", region)
}
