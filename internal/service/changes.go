package service

import "sync"

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
	ChangeDomains     ChangeKind = "domains"     // a domain's state from its checks; never a password or a hash
	ChangeMetrics     ChangeKind = "metrics"     // the sampler wrote a tick; never a number
	ChangeHost        ChangeKind = "host"        // a host event was recorded (a boot, a new Caravana version); never its detail
	ChangeServers     ChangeKind = "servers"     // a server was added, joined, became ready, was removed or got a role; never a token, key or address
	ChangeUpdates     ChangeKind = "updates"     // what is waiting changed: the OS's files, a release or package check, the check setting; never a name or a count
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

// changeHub fans notices out to subscribers. The zero value is ready to use.
// The notices themselves come from Nomad's event stream (events.go) and from
// the service's own writes.
type changeHub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
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

	s.changes.mu.Lock()
	if s.changes.subs == nil {
		s.changes.subs = map[*subscriber]struct{}{}
	}
	s.changes.subs[sub] = struct{}{}
	s.changes.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.changes.mu.Lock()
			delete(s.changes.subs, sub)
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

// copiesChanged turns the job IDs of changed copies into notices: a job is an app,
// so each one's copies and header change, and the list of apps once.
func (s *Service) copiesChanged(jobIDs []string) {
	for _, id := range jobIDs {
		s.publish(Change{AppID: id, What: ChangeCopies})
		s.publish(Change{AppID: id, What: ChangeApp})
	}
	s.publish(Change{What: ChangeApps})
	s.publish(Change{What: ChangeAttention})
}
