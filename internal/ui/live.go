package ui

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The defaults for a live stream (research R1, R11, R12).
const (
	defaultCoalesce  = 500 * time.Millisecond // at most one event per kind in this time
	defaultKeepalive = 20 * time.Second       // a comment line, so proxies keep an idle stream
	defaultRecheck   = 30 * time.Second       // how often the session is checked again
	defaultUsage     = 5 * time.Second        // how often the Running tab reads CPU and memory (research R7)
)

// liveSettings are the stream's timings and its source of notices. The zero
// value means the defaults and the real Service; tests set short timings and
// a fake source. (C#: an options object whose unset fields mean "default".)
type liveSettings struct {
	coalesce, keepalive, recheck, usageEvery time.Duration
	subscribe                                func(appID string) (<-chan service.Change, func())
	// watchDomains says a domains page is open until the returned done is
	// called (Service.MarkDomainsWatched when nil).
	watchDomains func() (done func())
	// watchLogs says a Logs page with Live on is open on a place until the
	// returned done is called (Service.WatchLogs when nil).
	watchLogs func(project, environment string) (done func())
}

func (l liveSettings) coalesceEvery() time.Duration  { return orDefault(l.coalesce, defaultCoalesce) }
func (l liveSettings) keepaliveEvery() time.Duration { return orDefault(l.keepalive, defaultKeepalive) }
func (l liveSettings) recheckEvery() time.Duration   { return orDefault(l.recheck, defaultRecheck) }
func (l liveSettings) usageInterval() time.Duration  { return orDefault(l.usageEvery, defaultUsage) }

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// liveRoute is what makes one /live route differ from another.
type liveRoute struct {
	// AppID limits the notices to one app's ("" means every app's).
	AppID string
	// Kinds are the notices that become events; the others are ignored. The
	// event's name is the kind's name ("deployments", "copies", ...).
	Kinds []service.ChangeKind
	// Start, when set, runs in its own goroutine once "sync" is sent, and
	// writes extra events (the Running tab's usage ticks) through ev, which
	// is safe to use from several goroutines. ctx ends when the stream ends,
	// and the stream waits for Start to return before it closes, so Start
	// must stop when ctx does.
	Start func(ctx context.Context, ev *httpx.EventWriter)
	// WatchDomains says the page shows domains (feature 023): while the stream
	// is open the domain watcher checks every minute, not every 15.
	WatchDomains bool
	// WatchLogs, when set, says the page follows an environment's output (feature 024): it runs once
	// the session is checked, and the returned done when the stream ends. While it is open the
	// service follows the copies of the place and sends the logs notice when an app wrote.
	WatchLogs func() (done func())
}

// stream answers a /live… request. It is the loop every such route shares:
//
//  1. check the session quietly (a stream must not renew it, research R11);
//     no valid session gets a stream that says "ended" at once;
//  2. subscribe to notices, then send "sync", so none is missed in between and
//     the page refreshes every region once on each (re)connect;
//  3. forward notices of the route's kinds as events, at most one per kind
//     per coalesce interval, a keepalive comment while idle, and a session
//     re-check every recheck interval, which ends the stream with "ended";
//  4. return when the client leaves, the server shuts down (the request's
//     context is cancelled, cmd/caravana's BaseContext), or a write fails.
//
// "ended" is a normal 200 stream that closes because the SSE extension
// retries any error status forever; the page closes its stream on "ended".
func (s *server) stream(w http.ResponseWriter, r *http.Request, route liveRoute) {
	value, hasCookie := httpx.SessionCookie(r, s.proxies.HTTPS(r))
	check := func(ctx context.Context) error {
		if !hasCookie {
			return service.ErrNotAuthenticated
		}
		return s.auth.CheckSessionQuiet(ctx, value)
	}

	err := check(r.Context())
	if err != nil && !errors.Is(err, service.ErrNotAuthenticated) {
		s.log.Error("check session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ev, serr := httpx.Stream(w)
	if serr != nil {
		s.log.Error("start live stream", "path", r.URL.Path, "error", serr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err != nil {
		_ = ev.Event("ended", nil)
		return
	}

	subscribe := s.live.subscribe
	if subscribe == nil {
		subscribe = s.svc.Subscribe
	}
	changes, unsubscribe := subscribe(route.AppID)
	defer unsubscribe()
	if route.WatchDomains {
		watch := s.live.watchDomains
		if watch == nil {
			watch = s.svc.MarkDomainsWatched
		}
		done := watch()
		defer done()
	}

	if route.WatchLogs != nil {
		done := route.WatchLogs()
		defer done()
	}

	if err := ev.Event("sync", nil); err != nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	var extra sync.WaitGroup
	defer func() { // cancel first, then wait, so Start sees ctx end
		cancel()
		extra.Wait()
	}()
	if route.Start != nil {
		extra.Go(func() { route.Start(ctx, ev) })
	}

	s.liveLoop(ctx, ev, changes, check, route.Kinds)
}

// liveLoop is the select loop of step 3 and 4. It returns when the stream is
// over; the caller unsubscribes.
func (s *server) liveLoop(ctx context.Context, ev *httpx.EventWriter, changes <-chan service.Change,
	check func(context.Context) error, kinds []service.ChangeKind) {
	co := newCoalescer(s.live.coalesceEvery())
	pending := time.NewTimer(time.Hour)
	pending.Stop()
	keepalive := time.NewTicker(s.live.keepaliveEvery())
	defer keepalive.Stop()
	recheck := time.NewTicker(s.live.recheckEvery())
	defer recheck.Stop()

	// send writes the events the coalescer lets through and arms the timer
	// for the ones it holds back. A false result means the client is gone.
	send := func(now time.Time, ready []service.ChangeKind) bool {
		for _, k := range ready {
			if ev.Event(string(k), nil) != nil {
				return false
			}
		}
		if wait, ok := co.nextWait(now); ok {
			pending.Reset(wait)
		}
		return true
	}

	for {
		select {
		case c, ok := <-changes:
			if !ok {
				return
			}
			if !slices.Contains(kinds, c.What) {
				continue
			}
			now := time.Now()
			var ready []service.ChangeKind
			if co.offer(c.What, now) {
				ready = append(ready, c.What)
			}
			if !send(now, ready) {
				return
			}
		case <-pending.C:
			now := time.Now()
			if !send(now, co.due(now)) {
				return
			}
		case <-keepalive.C:
			if ev.Comment("keepalive") != nil {
				return
			}
		case <-recheck.C:
			err := check(ctx)
			switch {
			case err == nil:
			case errors.Is(err, service.ErrNotAuthenticated):
				_ = ev.Event("ended", nil)
				return
			case ctx.Err() != nil:
				return
			default:
				// The store failed, not the session: a hiccup is not a reason
				// to close every open page. Try again next time.
				s.log.Warn("re-check live session", "error", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// coalescer lets one notice of a kind through at once and holds the rest of
// that kind back until gap has passed since the last one sent, then lets a
// single event through for all of them. It never reads the clock itself
// (callers pass the time), so it is simple to test.
type coalescer struct {
	gap     time.Duration
	last    map[service.ChangeKind]time.Time
	pending []service.ChangeKind // in the order they arrived, without duplicates
}

func newCoalescer(gap time.Duration) *coalescer {
	return &coalescer{gap: gap, last: map[service.ChangeKind]time.Time{}}
}

// offer reports whether a notice of kind k should become an event right now.
// If not, k is remembered and comes out of due later.
func (c *coalescer) offer(k service.ChangeKind, now time.Time) bool {
	if slices.Contains(c.pending, k) {
		return false // already waiting; this notice joins it
	}
	if last, ok := c.last[k]; ok && now.Sub(last) < c.gap {
		c.pending = append(c.pending, k)
		return false
	}
	c.last[k] = now
	return true
}

// due returns the held-back kinds whose gap has passed, and marks them sent.
func (c *coalescer) due(now time.Time) []service.ChangeKind {
	var ready []service.ChangeKind
	c.pending = slices.DeleteFunc(c.pending, func(k service.ChangeKind) bool {
		if now.Sub(c.last[k]) < c.gap {
			return false
		}
		c.last[k] = now
		ready = append(ready, k)
		return true
	})
	return ready
}

// nextWait is how long until the first held-back kind may be sent; false when
// nothing is held back.
func (c *coalescer) nextWait(now time.Time) (time.Duration, bool) {
	wait, found := time.Duration(0), false
	for _, k := range c.pending {
		w := max(c.last[k].Add(c.gap).Sub(now), 0)
		if !found || w < wait {
			wait, found = w, true
		}
	}
	return wait, found
}
