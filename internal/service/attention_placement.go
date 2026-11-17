package service

import (
	"context"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// copies_unplaced (feature 042): a service that has placement rules and whose job Nomad says has
// copies queued, wanted with no node to put them on. Derived on every read from the job summaries.
// Only a service with rules is judged: a queued copy of any other service is a shortage of room,
// which is no placement problem. A stopped app has no copies wanted, so it gives none.
//
// The key is copies_unplaced|<app>|<service>|: the queued count changes as Nomad retries, and is
// still the one problem.

// placementItems reads Nomad's queued copies, only when some app has a service with placement. known
// is false when Nomad couldn't be read: the kind is then not judged either way.
func (s *Service) placementItems(ctx context.Context, apps []store.AppRecord, now time.Time) (items []AttentionItem, known bool) {
	want := false
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			want = want || (svc.Placement != nil && rec.StoppedAt == nil)
		}
	}
	if !want {
		return nil, true
	}
	queued, err := s.deployer.QueuedCopies(ctx)
	if err != nil {
		s.log.Warn("can't read queued copies from Nomad; the needs-you list leaves copies_unplaced out", "error", err)
		return nil, false
	}
	return placementItemsOf(now, apps, queued), true
}

// placementItemsOf is the pure part: queued is the job summaries' non-zero queued counts, by job ID
// and group.
func placementItemsOf(now time.Time, apps []store.AppRecord, queued map[string]map[string]int) []AttentionItem {
	var items []AttentionItem
	for _, rec := range apps {
		if rec.StoppedAt != nil {
			continue
		}
		for _, svc := range rec.App.Services {
			n := queued[rec.App.ID][svc.Name]
			if svc.Placement == nil || n == 0 {
				continue
			}
			it := AttentionItem{Kind: AttentionCopiesUnplaced, AppID: rec.App.ID, Service: svc.Name, Queued: n,
				Level: metrics.DiskFailed, When: now,
				Project: rec.Place.Project, Environment: rec.Place.Environment, AppName: rec.Place.Name}
			it.Detail = plainText(copiesUnplacedParts(it, placeParts(func(s string) string { return s }, it, slashed(it.Service))))
			items = append(items, it)
		}
	}
	return items
}

// copiesUnplacedParts is the item's one wording. where is the service's place (ProblemParts has it).
func copiesUnplacedParts(it AttentionItem, where []TextPart) []TextPart {
	out := append([]TextPart{}, where...)
	return append(out, txt(": "+copyCount(it.Queued)+" can't be placed: no server that accepts work matches its placement rules, or none has room."))
}
