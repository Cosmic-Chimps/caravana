package service

import (
	"context"
	"sync"
	"time"
)

// ChangeKind says which part of the world a Change is about. Open pages use
// it to refresh only the regions that show it.
type ChangeKind string

// The kinds of change notice (research R5, data-model "Who publishes what").
const (
	ChangeApps        ChangeKind = "apps"        // the list of apps or an app's status in it
	ChangeActivity    ChangeKind = "activity"    // the recent deployments across apps
	ChangeApp         ChangeKind = "app"         // one app's header and settings
	ChangeDeployments ChangeKind = "deployments" // one app's deployment list
	ChangeCopies      ChangeKind = "copies"      // one app's running copies
	ChangeDeleted     ChangeKind = "deleted"     // one app is gone
	ChangeAttention   ChangeKind = "attention"   // what needs the owner, across apps
	ChangeActions     ChangeKind = "actions"     // one app's list of actions (stop, start, restart, run); never their content
	ChangeSchedules   ChangeKind = "schedules"   // an app's schedules or their runs; never a run's output
)

// Change is a notice that something changed. It says what, never the new
// value: the reader fetches that itself.
type Change struct {
	AppID string // "" for notices about all apps ("apps", "activity")
	What  ChangeKind
}

// subscriberBuffer is how many notices one subscriber may fall behind by.
const subscriberBuffer = 16

// subscriber is one Subscribe call.
type subscriber struct {
	appID string
	ch    chan Change
}

// defaultWatchIdle is how long the copies watcher keeps running after the
// last subscriber leaves, so a page reload doesn't stop and restart it.
const defaultWatchIdle = 30 * time.Second

// changeHub fans notices out to subscribers and owns the lifecycle of the
// copies watcher. The zero value is ready to use.
type changeHub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}

	// stopWatcher is non-nil while the copies watcher runs. idle is non-nil
	// while it waits to stop because the last subscriber left.
	stopWatcher context.CancelFunc
	idle        *time.Timer
}

// Subscribe returns a channel of notices for appID and a function that ends
// the subscription. An appID of "" subscribes to every app's notices,
// including the ones about all apps; any other appID gets only that app's.
//
// Notices are dropped, never queued, when the reader is behind: a dropped
// notice is harmless, because the next one (or the page's own timer)
// refreshes the same regions. The function closes the channel and is safe to
// call more than once. (C# comparison: a bounded Channel<T> with
// BoundedChannelFullMode.DropWrite.)
func (s *Service) Subscribe(appID string) (<-chan Change, func()) {
	sub := &subscriber{appID: appID, ch: make(chan Change, subscriberBuffer)}
	bg := s.background() // before taking the hub's lock: the two locks are never held in the other order

	s.changes.mu.Lock()
	if s.changes.subs == nil {
		s.changes.subs = map[*subscriber]struct{}{}
	}
	s.changes.subs[sub] = struct{}{}
	s.ensureWatcher(bg)
	s.changes.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.changes.mu.Lock()
			delete(s.changes.subs, sub)
			s.scheduleWatcherStop()
			s.changes.mu.Unlock()
			// publish holds the same lock while sending, and the subscriber
			// is gone from the map by now, so nobody can send on a closed
			// channel.
			close(sub.ch)
		})
	}
	return sub.ch, unsubscribe
}

// publish delivers c to every matching subscriber without ever waiting for
// one: a full channel just misses the notice.
func (s *Service) publish(c Change) {
	s.changes.mu.Lock()
	defer s.changes.mu.Unlock()
	for sub := range s.changes.subs {
		if sub.appID != "" && sub.appID != c.AppID {
			continue
		}
		select {
		case sub.ch <- c:
		default:
		}
	}
}

// deploymentChanged publishes what a deployment record's creation or update
// changes: the app's deployments and header, and the lists across apps.
func (s *Service) deploymentChanged(appID string) {
	s.publish(Change{AppID: appID, What: ChangeDeployments})
	s.publish(Change{AppID: appID, What: ChangeApp})
	s.publish(Change{What: ChangeApps})
	s.publish(Change{What: ChangeActivity})
	s.publish(Change{What: ChangeAttention})
}

// appChanged publishes that an app was saved: its header and settings, and
// the list of apps.
func (s *Service) appChanged(appID string) {
	s.publish(Change{AppID: appID, What: ChangeApp})
	s.publish(Change{What: ChangeApps})
}

// ensureWatcher makes sure the copies watcher is running and not about to
// stop. The hub's lock must be held. The watcher follows every allocation in
// Nomad with one blocking query (research R6), so it only runs while someone
// is listening: the first subscriber starts it, and it stops watchIdle after
// the last one leaves.
func (s *Service) ensureWatcher(bg context.Context) {
	h := &s.changes
	if h.idle != nil {
		h.idle.Stop()
		h.idle = nil
	}
	if h.stopWatcher != nil {
		return
	}
	ctx, cancel := context.WithCancel(bg)
	h.stopWatcher = cancel
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.deployer.WatchCopies(ctx, s.copiesChanged)
	}()
}

// scheduleWatcherStop stops the watcher after watchIdle if nobody has
// subscribed again by then. The hub's lock must be held.
func (s *Service) scheduleWatcherStop() {
	h := &s.changes
	if len(h.subs) > 0 || h.stopWatcher == nil || h.idle != nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(s.watchIdle, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.idle != timer || len(h.subs) > 0 {
			return // someone came back, or a newer timer owns this
		}
		h.idle = nil
		h.stopWatcher()
		h.stopWatcher = nil
	})
	h.idle = timer
}

// copiesChanged turns the watcher's job IDs into notices: a job is an app,
// so each one's copies and header change, and the list of apps once.
func (s *Service) copiesChanged(jobIDs []string) {
	for _, id := range jobIDs {
		s.publish(Change{AppID: id, What: ChangeCopies})
		s.publish(Change{AppID: id, What: ChangeApp})
	}
	s.publish(Change{What: ChangeApps})
	s.publish(Change{What: ChangeAttention})
}
