package service

import (
	"context"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// Nomad's event stream (deploy.FollowEvents) is how Caravana learns that
// something changed: one subscription, started with the service, replaces the
// fast polls of rollouts, copies, schedule runs and backups. Each of those still
// polls slowly as a safety net, since an event can be missed (a dropped
// connection, a Nomad that restarted), and the stream says so with a Resync
// batch, on which everything is looked at again.
//
// What follows is only the translation of a batch into notices for the open
// pages and wakes for the watchers. An event is a hint: nothing is read from it
// but job IDs, and nothing of it is logged.

// followEvents is the goroutine Start launches.
func (s *Service) followEvents(ctx context.Context) {
	s.deployer.FollowEvents(ctx, s.onEvents, s.eventsFailed)
}

// eventsFailed logs the first failure after a time the stream worked. The
// field eventsLost is true between a failure and the Resync that ends it, so
// the log says once that the stream is lost and once that it is back, not on
// each retry.
func (s *Service) eventsFailed(err error) {
	if !s.eventsLost.Swap(true) {
		s.log.Warn("lost Nomad's event stream; watchers fall back to their slow polls until it is back", "error", err)
	}
}

// onEvents turns a batch into notices and wakes.
func (s *Service) onEvents(b deploy.EventBatch) {
	if b.Resync && s.eventsLost.Swap(false) {
		s.log.Info("following Nomad's events again")
	}

	if len(b.Copies) > 0 {
		s.copiesChanged(b.Copies)
		s.wakes.wake(copiesKey)
	}
	for _, id := range b.Rollouts {
		s.wakes.wake(rolloutKey(id))
	}
	for _, id := range b.Deployments {
		// The history's rollout progress. A copy's health and its rollout's count
		// usually move in the same event batch, and the regions that show progress
		// refresh on a copies notice too, so that one is enough then: a second
		// notice would make the page read Nomad twice.
		if !slices.Contains(b.Copies, id) {
			s.publish(Change{AppID: id, What: ChangeDeployments})
		}
	}
	if b.Nodes {
		s.publish(Change{What: ChangeServers})
	}

	// The helper jobs are named after what they are (constitution II).
	for _, id := range append(append([]string(nil), b.Copies...), b.Jobs...) {
		switch {
		case strings.Contains(id, "_run-"):
			s.wakes.wake(runsKey)
		case strings.Contains(id, "_backup"), strings.Contains(id, "_restore"):
			s.wakes.wake(backupsKey)
		}
	}

	if b.Resync {
		s.resynced()
	}
}

// resynced is what a stream that (re)connected needs: events may have been
// missed, so every watcher looks now and every open page refreshes each region
// that Nomad moves.
func (s *Service) resynced() {
	s.wakes.wakeAll() // every rollout, the copies followers, the run and backup watchers

	ctx := s.background()
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("after Nomad's events came back: list apps to refresh the pages", "error", err)
		}
	}
	for _, rec := range apps {
		s.publish(Change{AppID: rec.App.ID, What: ChangeCopies})
		s.publish(Change{AppID: rec.App.ID, What: ChangeDeployments})
	}
	s.publish(Change{What: ChangeApps})
	s.publish(Change{What: ChangeAttention})
	s.publish(Change{What: ChangeServers})
	s.resyncs.Add(1)
}
