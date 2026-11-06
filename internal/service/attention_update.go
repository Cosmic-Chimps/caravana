package service

import (
	"cmp"
	"context"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The update item on the needs-you list (feature 030): an update of Caravana that didn't go
// through. Like the rest of the list it is derived on every read and keeps nothing: it is there
// while the newest finished run is rolled back, failed, refused or lost and the running version is
// still older than that run's, so a later run that succeeds (it becomes the newest) and an update
// made by hand (the running version catches up) both end it. A failed update is never retried by
// itself, so the item stays until the owner acts.

// updateFailedTitle is the title of the item and of its notification.
const updateFailedTitle = "caravana update failed"

// updateItems is the update item for the list: none, or one for the newest finished run. It comes
// from the store alone, so it is always judged.
func (s *Service) updateItems(ctx context.Context) ([]AttentionItem, error) {
	run, ok, err := s.store.LatestFinishedUpdateRun(ctx)
	if err != nil || !ok {
		return nil, err
	}
	if it, ok := updateItemOf(run, s.runningVersion); ok {
		return []AttentionItem{it}, nil
	}
	return nil, nil
}

// updateItemOf is the pure part: the item for a finished run, and false when the run went through
// or the running version is at least the run's. A running version that isn't a release version (a
// development build, or none set) can't be compared, so it has no item: no update was ever asked
// of such a build.
func updateItemOf(run store.UpdateRun, running string) (AttentionItem, bool) {
	switch run.State {
	case store.UpdateRolledBack, store.UpdateFailed, store.UpdateRefused, store.UpdateLost:
	default:
		return AttentionItem{}, false
	}
	if !hostos.ReleaseNewer(run.Version, running) {
		return AttentionItem{}, false
	}
	it := AttentionItem{
		Kind: AttentionCaravanaUpdateFailed, Level: metrics.DiskWarning, When: run.FinishedAt,
		UpdateRunID: run.ID, UpdateVersion: run.Version, UpdateFrom: cmp.Or(run.FromVersion, strings.TrimPrefix(running, "v")),
		UpdateState: run.State, UpdateReason: run.Reason,
	}
	if it.When.IsZero() {
		it.When = run.RequestedAt
	}
	if run.State == store.UpdateFailed {
		it.Level = metrics.DiskFailed
	}
	it.Detail = plainText(updateFailedParts(it))
	return it, true
}

// updateFailedParts is the item's one wording, by the run's state, with the versions in mono:
//
//	rolled_back: caravana 0.17.0 didn't start, so 0.16.0 runs again.
//	failed:      The update to caravana 0.17.0 failed: <reason>.
//	refused:     The server's updater refused caravana 0.17.0: <reason>.
//	lost:        The update to caravana 0.17.0 didn't finish: <reason>.
//
// An unknown state is worded as lost; an empty reason leaves out the colon.
func updateFailedParts(it AttentionItem) []TextPart {
	reason := func() []TextPart {
		r := strings.TrimRight(strings.TrimSpace(it.UpdateReason), ".")
		if r == "" {
			return parts(".")
		}
		return parts(": " + r + ".")
	}
	switch it.UpdateState {
	case store.UpdateRolledBack:
		if it.UpdateFrom == "" {
			return parts("caravana ", mono(it.UpdateVersion), " didn't start, so the old version runs again.")
		}
		return parts("caravana ", mono(it.UpdateVersion), " didn't start, so ", mono(it.UpdateFrom), " runs again.")
	case store.UpdateFailed:
		return parts("The update to caravana ", mono(it.UpdateVersion), " failed", reason())
	case store.UpdateRefused:
		return parts("The server's updater refused caravana ", mono(it.UpdateVersion), reason())
	}
	return parts("The update to caravana ", mono(it.UpdateVersion), " didn't finish", reason())
}
