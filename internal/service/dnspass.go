package service

import (
	"context"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// defaultDNSPassGap is how often the pass looks for records to remove.
const defaultDNSPassGap = 10 * time.Minute

// SetDNSPassGap sets how often the DNS pass runs (10 minutes by default). Call
// it before Start; a value that isn't positive keeps the default.
func (s *Service) SetDNSPassGap(d time.Duration) {
	if d > 0 {
		s.dnsGap = d
	}
}

// runDNSPass runs the pass at start and then every gap.
func (s *Service) runDNSPass(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		s.dnsPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// dnsPass removes, at the provider, the records of rows whose app no longer
// exists or no longer has the host (a removal that failed when it was made, or
// that a restart interrupted), and drops their rows. A failure is logged and
// the row kept for the next pass. It never makes a record, and it never
// touches a zone that was removed: that zone's rows were dropped with it.
//
// The rows are read before the apps: a deploy saves its app, with the new host,
// before it makes the record, so a row read first is never newer than the app.
func (s *Service) dnsPass(ctx context.Context) {
	rows, err := s.store.ListDNSRecords(ctx)
	if err != nil || len(rows) == 0 {
		if err != nil {
			s.log.Warn("DNS pass: list the records Caravana made", "error", err)
		}
		return
	}
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		s.log.Warn("DNS pass: list the apps", "error", err)
		return
	}
	hosts := map[string]map[string]bool{}
	for _, rec := range apps {
		set := map[string]bool{}
		for _, h := range dnsHostsOf(rec.App) {
			set[h.Host] = true
		}
		hosts[rec.App.ID] = set
	}
	stale := slices.DeleteFunc(rows, func(r store.DNSRecord) bool { return hosts[r.AppID][r.Host] })
	if len(stale) == 0 {
		return
	}
	s.removeDNSRows(ctx, stale)
	for _, r := range stale {
		s.domainsChanged(r.AppID)
	}
}
