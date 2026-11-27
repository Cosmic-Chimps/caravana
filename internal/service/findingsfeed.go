package service

import (
	"fmt"
	"sort"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The activity feed tells image findings per app (task 9h5g, the designer's rule): one line when an app's
// first fixable finding appears ("uptime-kuma: 9 fixable critical vulnerabilities found.") and one when
// its last one goes, never a line per package. A package that appears or goes while the app already has
// findings adds no line. The needs-you items, the table of sent problems and the notifications stay one per
// package: only the feed's history is per app, so the check writes these events itself and the store
// writes none of the per-package ones (ProblemCheck.Quiet).

// findingEvents are the feed's events about image findings for one check. prev is the table of sent
// problems before it, changes what diffProblems found. An app whose problems are dropped (it is gone)
// leaves no event. places and display word the app as the feed does.
func findingEvents(prev []store.SentProblem, changes problemChanges, places map[string]app.Place,
	display func(string) string, at time.Time) []store.ProblemEvent {

	type tally struct {
		before, resolved int
		opened           []AttentionItem
	}
	by := map[string]*tally{}
	get := func(appID string) *tally {
		t := by[appID]
		if t == nil {
			t = &tally{}
			by[appID] = t
		}
		return t
	}
	dropped := map[string]bool{}
	for _, key := range changes.Dropped {
		dropped[key] = true
	}
	for _, p := range prev {
		if AttentionKind(p.Kind) == AttentionImageVulnerable && p.AppID != "" && !dropped[p.Key] {
			get(p.AppID).before++
		}
	}
	for _, it := range changes.Opened {
		if it.Kind == AttentionImageVulnerable && it.AppID != "" {
			get(it.AppID).opened = append(get(it.AppID).opened, it)
		}
	}
	for _, p := range changes.Resolved {
		if AttentionKind(p.Kind) == AttentionImageVulnerable && p.AppID != "" {
			get(p.AppID).resolved++
		}
	}

	ids := make([]string, 0, len(by))
	for id := range by {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []store.ProblemEvent
	for _, id := range ids {
		t := by[id]
		after := t.before - t.resolved + len(t.opened)
		who := labelParts(display, places[id], id, "")
		e := store.ProblemEvent{Key: store.ImageFindingsKey(id), Kind: string(AttentionImageVulnerable), AppID: id, At: at}
		var sentence []TextPart
		switch {
		case t.before == 0 && after > 0:
			var critical, high int
			for _, it := range t.opened {
				if it.Level == metrics.DiskFailed {
					critical++
				} else {
					high++
				}
			}
			e.Change, sentence = store.ProblemOpened, findingsFoundParts(who, critical, high)
		case t.before > 0 && after == 0:
			e.Change, sentence = store.ProblemResolved, findingsGoneParts(who)
		default:
			continue
		}
		e.Sentence, e.Summary = encodeParts(sentence), plainText(sentence)
		out = append(out, e)
	}
	return out
}

// findingsFoundParts is the feed's line when an app's first fixable findings appear, counting packages as
// the needs-you list's Security group does: "uptime-kuma: 9 fixable critical vulnerabilities found."
func findingsFoundParts(who []TextPart, critical, high int) []TextPart {
	var words string
	switch {
	case high == 0:
		words = vulnerabilities(critical, "fixable critical") + " found."
	case critical == 0:
		words = vulnerabilities(high, "fixable high") + " found."
	default:
		words = fmt.Sprintf("%s found: %d critical, %d high.", vulnerabilities(critical+high, "fixable"), critical, high)
	}
	return parts(who, ": "+words)
}

// findingsGoneParts is the sentence of the line when an app's last fixable finding is gone ("uptime-kuma has no fixable
// vulnerabilities left."); the feed puts
// "Resolved: " before it, as for every resolved problem.
func findingsGoneParts(who []TextPart) []TextPart {
	return parts(who, " has no fixable vulnerabilities left.")
}

// vulnerabilities is "9 fixable critical vulnerabilities", or "1 fixable critical vulnerability".
func vulnerabilities(n int, adjectives string) string {
	if n == 1 {
		return "1 " + adjectives + " vulnerability"
	}
	return fmt.Sprintf("%d %s vulnerabilities", n, adjectives)
}
