package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The domain watcher (feature 023, plan "Cache and watcher"). It keeps
// domain_checks fresh, which is what the Domains pages and the needs-you list
// read: every stored domain of a served app is checked every domainIdleGap (15
// minutes), and every domainWatchGap (a minute) while a domains page is open.
// At most domainProbeParallel checks run at once. A domain whose app is
// stopped or has no healthy deployment isn't probed: its row says not_served.
// A row whose host no app has anymore is deleted. When a pass changed any
// row's state, the pages are told (ChangeDomains), and so is the needs-you
// list (ChangeAttention). Like the other watchers it keeps nothing in
// memory but when the last pass ran, so a restart only delays the next one.

const (
	// defaultDomainIdleGap is how often every domain is checked while no
	// domains page is open.
	defaultDomainIdleGap = 15 * time.Minute
	// defaultDomainWatchGap is how often they are checked while one is, and
	// how often the watcher looks at the clock.
	defaultDomainWatchGap = time.Minute
	// domainProbeParallel is how many checks may run at once (each dials the
	// edge and asks three resolvers).
	domainProbeParallel = 8
)

// SetDomainWatchGaps sets how often every domain is checked (idle, 15 minutes
// by default) and how often while a domains page is open (watched, a minute);
// the lab shortens them. Call it before Start: the watcher looks at the clock
// every watched gap as it was at Start. A value that isn't positive keeps the
// default.
func (s *Service) SetDomainWatchGaps(idle, watched time.Duration) {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	if idle > 0 {
		s.domainIdleGap = idle
	}
	if watched > 0 {
		s.domainWatchGap = watched
	}
}

// MarkDomainsWatched says a domains page is open: until done is called, every
// domain is checked every watched gap instead of the idle one. A page's live
// stream calls it when it opens and calls done when it ends, as the Running
// tab's stream reads usage only while it is open. done is safe to call more
// than once.
func (s *Service) MarkDomainsWatched() (done func()) {
	s.domainMu.Lock()
	s.domainsWatching++
	s.domainMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.domainMu.Lock()
			s.domainsWatching--
			s.domainMu.Unlock()
		})
	}
}

// domainWatchState is whether a domains page is open now, and the two gaps.
func (s *Service) domainWatchState() (watched bool, idle, watch time.Duration) {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	return s.domainsWatching > 0, s.domainIdleGap, s.domainWatchGap
}

// watchDomains runs the watcher until ctx ends. It looks at the clock every
// watched gap and runs a pass when one is due (domainPassDue). The first look
// is one gap after start, so nothing dials out at start, and finds a pass due.
func (s *Service) watchDomains(ctx context.Context) {
	_, _, watch := s.domainWatchState()
	t := time.NewTicker(watch)
	defer t.Stop()
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		last = s.domainWatchStep(ctx, last)
	}
}

// domainWatchStep runs a pass when one is due at the clock's time, and returns
// when the last pass started.
func (s *Service) domainWatchStep(ctx context.Context, last time.Time) time.Time {
	now := s.clock.Now()
	watched, idle, watch := s.domainWatchState()
	if !domainPassDue(now, last, watched, idle, watch) {
		return last
	}
	if err := s.checkDomainTargets(ctx, DomainFilter{}, true); err != nil {
		s.log.Warn("check domains", "error", err)
	}
	return now
}

// domainPassDue says whether a pass is due at now when the last one started
// at last (zero: none yet): once the gap that applies has passed, the watched
// one while a page is open. Half a watched gap of slack keeps a tick that
// comes a moment early from waiting a whole extra gap.
func domainPassDue(now, last time.Time, watched bool, idle, watch time.Duration) bool {
	if last.IsZero() {
		return true
	}
	gap := idle
	if watched {
		gap = watch
	}
	return now.Sub(last)+watch/2 >= gap
}

// RefreshDomains checks every domain that passes f now, as the watcher would,
// and saves what it finds: the API's listDomains with fresh=true. Errors are
// Domains'.
func (s *Service) RefreshDomains(ctx context.Context, f DomainFilter) error {
	return s.checkDomainTargets(ctx, f, false)
}

// checkDomainTargets is one pass over the hosts that pass f. Each host is
// checked once (the first app that claims it is its row's), at most
// domainProbeParallel at a time; a host whose app isn't served gets a
// not_served row and no probe. With prune, the rows of hosts that no app has
// anymore are deleted. Every app whose rows changed state is told about.
// A row that can't be saved is logged; the next pass tries again.
func (s *Service) checkDomainTargets(ctx context.Context, f DomainFilter, prune bool) error {
	targets, err := s.domainTargets(ctx, f)
	if err != nil {
		return err
	}
	checks, err := s.store.ListDomainChecks(ctx)
	if err != nil {
		return fmt.Errorf("check domains: %w", err)
	}
	warn, err := s.warnWindow(ctx)
	if err != nil {
		return fmt.Errorf("check domains: %w", err)
	}
	before := make(map[string]store.DomainCheckRecord, len(checks))
	for _, c := range checks {
		before[c.Host] = c
	}

	var mu sync.Mutex
	changed := map[string]bool{} // app IDs whose rows changed state
	note := func(rec store.DomainCheckRecord) {
		prev, had := before[rec.Host]
		if had && prev.State == rec.State && prev.AppID == rec.AppID {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		changed[rec.AppID] = true
		if had {
			changed[prev.AppID] = true
		}
	}
	save := func(rec store.DomainCheckRecord) {
		if err := s.store.UpsertDomainCheck(ctx, rec); err != nil {
			s.log.Warn("save a domain's check", "domain", rec.Host, "app", rec.AppID, "error", err)
			return
		}
		note(rec)
	}

	seen := map[string]bool{}
	var probe []domainTarget
	for _, t := range targets {
		if seen[t.host] {
			continue
		}
		seen[t.host] = true
		if t.served() {
			probe = append(probe, t)
			continue
		}
		// Not probed: Traefik isn't meant to serve it. A row that already
		// says so is left as it is.
		if prev, ok := before[t.host]; ok && prev.State == string(edge.StateNotServed) &&
			prev.AppID == t.rec.App.ID && prev.Service == t.service.Name {
			continue
		}
		save(recordOf(t, edge.Facts{
			DNS:         edge.DNS{Status: edge.DNSUnknown},
			Certificate: edge.Certificate{Status: edge.CertNone},
			CheckedAt:   s.clock.Now().UTC(),
		}, edge.StateNotServed))
	}

	if len(probe) > 0 {
		expected := s.hostAddresses(ctx)
		es := s.edgeSettings()
		slots := make(chan struct{}, domainProbeParallel)
		var wg sync.WaitGroup
		for _, t := range probe {
			if ctx.Err() != nil {
				break
			}
			slots <- struct{}{}
			wg.Go(func() {
				defer func() { <-slots }()
				f := s.probe(ctx, es, t.host, true, expected, warn)
				save(recordOf(t, f, stateFor(t, f)))
			})
		}
		wg.Wait()
	}

	if prune {
		for _, c := range checks {
			if seen[c.Host] {
				continue
			}
			if err := s.store.DeleteDomainCheck(ctx, c.Host); err != nil {
				s.log.Warn("delete a domain's check", "domain", c.Host, "error", err)
				continue
			}
			changed[c.AppID] = true
		}
	}

	for appID := range changed {
		s.domainsChanged(appID)
	}
	if len(changed) > 0 {
		// A state is what the needs-you list's domain kinds come from.
		s.publish(Change{What: ChangeAttention})
	}
	return nil
}
