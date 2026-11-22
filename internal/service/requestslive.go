package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/cosmic-chimps/caravana/internal/accesslog"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Live requests (feature 047, plan D6): while a Requests page with Live on is
// open, Caravana follows Traefik's stdout and tells the page when one of the
// place's apps was asked something, so that it fetches the region again. The
// notice says that, and for which app, never what: the follower reads only the
// service name of a line to find the app, and the line itself is neither kept,
// logged nor sent (constitution VII).

// ChangeRequests is the notice that a request reached an app of a watched
// place.
const ChangeRequests ChangeKind = "requests" // an app was asked something; never a line of it

// WatchRequests says a Requests page with Live on is open on the place (an
// empty project or environment means Default and production): until done is
// called, Traefik's stdout is followed and a ChangeRequests notice goes out for
// each app that was asked, at most once per 2 seconds per place. Pages of the
// same place share one follower, which ends when the last page's done is
// called; done is safe to call more than once.
//
// The same gaps as WatchLogs' apply (SetLogWatchGaps). Following needs no place
// that exists, and no Traefik: failures to list apps or find Traefik are logged
// (the error, never a line) and tried again.
func (s *Service) WatchRequests(project, environment string) (done func()) {
	return s.watchPlace(&s.requestWatch, project, environment, s.followRequests)
}

// requestFollow is the follower of one Traefik allocation.
type requestFollow struct {
	alloc  string
	cancel context.CancelFunc
	done   chan struct{} // closed when the read returned
}

// followRequests follows Traefik's running allocation until ctx ends. The apps
// are listed again, and the allocation looked up again, every relist and when
// Nomad's events say a copy changed (a Traefik restart makes a new allocation),
// never twice within notice.
func (s *Service) followRequests(ctx context.Context, project, environment string, notice, relist time.Duration) {
	n := &logNotice{s: s, gap: notice, what: ChangeRequests, dirty: map[string]bool{}}
	defer n.stop()

	var owners atomic.Pointer[accesslog.Owners]
	var cur *requestFollow
	stop := func() {
		if cur != nil {
			cur.cancel()
			<-cur.done
			cur = nil
		}
	}
	defer stop()

	refresh := func() {
		recs, err := s.requestPlaceApps(ctx, project, environment)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("list apps to follow requests", "project", project, "environment", environment, "error", err)
			}
			return
		}
		o := ownersOfRecords(recs)
		owners.Store(&o)
	}
	ensure := func() {
		if cur != nil {
			select {
			case <-cur.done: // the read ended: Traefik restarted or Nomad dropped the stream
				cur = nil
			default:
			}
		}
		proxy, err := s.deployer.ProxyCopy(ctx)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return
		default:
			if cur != nil {
				stop() // Traefik is gone
			}
			if !errors.Is(err, deploy.ErrNoProxy) {
				s.log.Warn("find Traefik to follow requests", "error", err)
			}
			return
		}
		if cur != nil && cur.alloc == proxy.ID {
			return
		}
		stop()
		fctx, cancel := context.WithCancel(ctx)
		f := &requestFollow{alloc: proxy.ID, cancel: cancel, done: make(chan struct{})}
		cur = f
		go func() {
			defer close(f.done)
			defer cancel()
			t := logTarget{copy: deploy.Copy{ID: proxy.ID, Service: deploy.ProxyTask}, stream: deploy.LogStdout}
			// The callback reads the service name and nothing else of the line.
			err := s.readStream(fctx, t, 0, true, func(l deploy.LogLine) {
				name, ok := accesslog.ServiceOf(l.Text)
				if !ok {
					return
				}
				o := owners.Load()
				if o == nil {
					return
				}
				if own, ok := o.Of(name); ok {
					n.arrived(own.App)
				}
			})
			if err != nil && fctx.Err() == nil {
				s.log.Warn("follow requests", "error", err)
			}
		}()
	}

	for {
		// Taken before the listing, so a change that lands during it isn't lost.
		woken := s.wakes.channel(copiesKey)
		listed := time.Now()
		refresh()
		ensure()
		timer := time.NewTimer(relist)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-woken:
			timer.Stop()
			sleepCtx(ctx, notice-time.Since(listed))
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// requestPlaceApps lists the apps of a place that exists; one that doesn't has
// none.
func (s *Service) requestPlaceApps(ctx context.Context, project, environment string) ([]store.AppRecord, error) {
	return s.logApps(ctx, EnvLogQuery{Project: project, Environment: environment})
}
