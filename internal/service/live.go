package service

import (
	"context"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// Live logs (feature 024, research R7): while a Logs page with Live on is
// open, Caravana follows the output of the place's running copies and tells
// the page when something new was written, so that it fetches the region
// again. The notice says that, and for which app, never what: a line is the
// app's output and is not read, stored, logged or sent (constitution VII). The
// follow callback below doesn't even look at the line.

// ChangeLogs is the notice that an app of a watched place wrote output.
const ChangeLogs ChangeKind = "logs" // an app wrote output; never a line of it

const (
	// defaultLogNoticeGap is the most often a place sends a notice.
	defaultLogNoticeGap = 2 * time.Second
	// defaultLogRelistGap is how often the copies of a watched place are
	// listed again, to follow the new ones and let go of the ended ones.
	defaultLogRelistGap = 30 * time.Second
)

// logWatcher is the places being watched. The zero value is ready to use.
type logWatcher struct {
	mu        sync.Mutex
	places    map[string]*watchedPlace // "<project>/<environment>"
	noticeGap time.Duration            // 0 means the default
	relistGap time.Duration
}

// watchedPlace is one place followed because pages watch it.
type watchedPlace struct {
	pages  int
	cancel context.CancelFunc
}

// SetLogWatchGaps sets how often a watched place sends a notice (2 seconds by
// default) and how often its copies are listed again (30 seconds); tests shorten
// them. Call it before WatchLogs. A value that isn't positive keeps the default.
func (s *Service) SetLogWatchGaps(notice, relist time.Duration) {
	s.logWatch.mu.Lock()
	defer s.logWatch.mu.Unlock()
	if notice > 0 {
		s.logWatch.noticeGap = notice
	}
	if relist > 0 {
		s.logWatch.relistGap = relist
	}
}

// WatchLogs says a Logs page with Live on is open on the place (an empty
// project or environment means Default and production): until done is called,
// its running copies' streams are followed and a ChangeLogs notice goes out
// for each app that wrote, at most once per 2 seconds per place. Pages of the
// same place share one set of followers, which end when the last page's done
// is called. A page's stream calls it when it opens and done when it ends;
// done is safe to call more than once.
//
// Following needs no place that exists: one that doesn't has no copies, and a
// failure to list them is logged (the error, never a line) and tried again.
func (s *Service) WatchLogs(project, environment string) (done func()) {
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	key := project + "/" + environment

	w := &s.logWatch
	w.mu.Lock()
	if w.places == nil {
		w.places = map[string]*watchedPlace{}
	}
	p := w.places[key]
	if p == nil {
		bg := s.background()
		if bg == nil {
			bg = context.Background()
		}
		ctx, cancel := context.WithCancel(bg)
		p = &watchedPlace{cancel: cancel}
		w.places[key] = p
		gaps := [2]time.Duration{orGap(w.noticeGap, defaultLogNoticeGap), orGap(w.relistGap, defaultLogRelistGap)}
		s.watchers.Add(1)
		go func() {
			defer s.watchers.Done()
			s.followPlace(ctx, project, environment, gaps[0], gaps[1])
		}()
	}
	p.pages++
	w.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			p.pages--
			if p.pages == 0 {
				p.cancel()
				delete(w.places, key)
			}
		})
	}
}

func orGap(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// followPlace follows every running copy of the place until ctx ends: both
// streams of each, from now on, listing the copies again every relist.
func (s *Service) followPlace(ctx context.Context, project, environment string, notice, relist time.Duration) {
	n := &logNotice{s: s, gap: notice, dirty: map[string]bool{}}
	defer n.stop()
	f := &logFollowers{s: s, notice: n, running: map[string]*logFollower{}}
	defer f.stopAll()

	list := func() {
		apps, err := s.logApps(ctx, EnvLogQuery{Project: project, Environment: environment})
		var copies []logCopy
		if err == nil {
			copies, err = s.logCopiesOf(ctx, EnvLogQuery{}, apps)
		}
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("list copies to follow logs", "project", project, "environment", environment, "error", err)
			}
			return
		}
		f.reconcile(ctx, copies)
	}

	list()
	t := time.NewTicker(relist)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			list()
		}
	}
}

// logFollower is one stream being followed.
type logFollower struct{ cancel context.CancelFunc }

// logFollowers are the streams a watched place follows, one reader each.
type logFollowers struct {
	s      *Service
	notice *logNotice

	mu      sync.Mutex
	running map[string]*logFollower // "<copy ID>/<stream>"
	wg      sync.WaitGroup
}

// reconcile starts the streams of the running copies that aren't followed and
// lets go of those whose copy is gone or ended.
func (f *logFollowers) reconcile(ctx context.Context, copies []logCopy) {
	want := map[string]bool{}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range copies {
		if c.Gone || c.Terminal {
			continue
		}
		for _, st := range []deploy.LogStream{deploy.LogStdout, deploy.LogStderr} {
			key := c.ID + "/" + string(st)
			want[key] = true
			if f.running[key] == nil && ctx.Err() == nil {
				f.startLocked(ctx, key, c, st)
			}
		}
	}
	for key, r := range f.running {
		if !want[key] {
			r.cancel()
			delete(f.running, key)
		}
	}
}

// startLocked follows one stream. The entry removes itself when the read
// returns (the copy ended, or Nomad dropped the stream), so that the next
// listing starts it again if the copy still runs.
func (f *logFollowers) startLocked(ctx context.Context, key string, c logCopy, st deploy.LogStream) {
	r := &logFollower{}
	var rctx context.Context
	rctx, r.cancel = context.WithCancel(ctx)
	f.running[key] = r
	f.wg.Go(func() {
		// The callback never looks at the line.
		err := f.s.readStream(rctx, logTarget{copy: c.Copy, stream: st}, 0, true, func(deploy.LogLine) {
			f.notice.arrived(c.app)
		})
		f.mu.Lock()
		if f.running[key] == r {
			delete(f.running, key)
		}
		f.mu.Unlock()
		r.cancel()
		if err != nil && ctx.Err() == nil {
			f.s.log.Warn("follow logs", "copy", c.short, "error", err)
		}
	})
}

// stopAll lets go of every stream and waits for the readers to return.
func (f *logFollowers) stopAll() {
	f.mu.Lock()
	for _, r := range f.running {
		r.cancel()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

// logNotice turns "an app wrote something" into at most one notice per gap:
// the first after a quiet gap goes at once, the rest wait for the gap to pass
// and go together, one per app.
type logNotice struct {
	s   *Service
	gap time.Duration

	mu      sync.Mutex
	dirty   map[string]bool // app IDs that wrote since the last notice
	last    time.Time       // when the last notice went
	timer   *time.Timer     // set while a notice waits for the gap
	stopped bool
}

// arrived records that appID wrote a line. It never sees the line.
func (n *logNotice) arrived(appID string) {
	n.mu.Lock()
	if n.stopped {
		n.mu.Unlock()
		return
	}
	n.dirty[appID] = true
	if n.timer != nil {
		n.mu.Unlock()
		return
	}
	if wait := n.gap - time.Since(n.last); wait > 0 {
		n.timer = time.AfterFunc(wait, n.fire)
		n.mu.Unlock()
		return
	}
	apps := n.takeLocked()
	n.mu.Unlock()
	n.send(apps)
}

// fire is the timer's: the waiting notice goes.
func (n *logNotice) fire() {
	n.mu.Lock()
	n.timer = nil
	if n.stopped {
		n.mu.Unlock()
		return
	}
	apps := n.takeLocked()
	n.mu.Unlock()
	n.send(apps)
}

func (n *logNotice) takeLocked() []string {
	apps := make([]string, 0, len(n.dirty))
	for id := range n.dirty {
		apps = append(apps, id)
	}
	clear(n.dirty)
	n.last = time.Now()
	return apps
}

func (n *logNotice) send(apps []string) {
	for _, id := range apps {
		n.s.publish(Change{AppID: id, What: ChangeLogs})
	}
}

// stop ends the notices: nothing is sent after it returns.
func (n *logNotice) stop() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stopped = true
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
}
