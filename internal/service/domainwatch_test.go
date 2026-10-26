package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// domainNotices drains the notices received so far and returns the apps of
// the ChangeDomains ones, sorted, without repeats.
func domainNotices(changes <-chan Change) []string {
	var apps []string
	for {
		select {
		case c := <-changes:
			if c.What == ChangeDomains && !slices.Contains(apps, c.AppID) {
				apps = append(apps, c.AppID)
			}
		default:
			slices.Sort(apps)
			return apps
		}
	}
}

// dialedHosts are the server names the fake edge was asked for, sorted.
func dialedHosts(d *edgetest.FakeDialer) []string {
	var out []string
	for _, line := range d.Dialed() {
		_, host, _ := strings.Cut(line, " ")
		out = append(out, host)
	}
	slices.Sort(out)
	return out
}

// A pass probes the served apps' domains only, writes not_served rows for the others without
// probing, deletes the rows of hosts no app has, and tells the pages only when a state changed.
func TestDomainWatchPass(t *testing.T) {
	ctx := context.Background()
	resolver := edgetest.FakeResolver{Hosts: map[string][]string{
		"shop.example.com": {hostV4.String()},
		"api.example.com":  {hostV4.String()},
		"old.example.com":  {hostV4.String()},
	}}
	dialer := edgeOf(t, "shop.example.com", "api.example.com")
	e := domainEnv(t, resolver, dialer)
	seedDomainApp(t, e, domainApp("shop", app.Domain{Host: "shop.example.com"}, app.Domain{Host: "api.example.com"}), true)
	seedDomainApp(t, e, domainApp("old", app.Domain{Host: "old.example.com"}), true)
	stopDomainApp(t, e, "old")
	seedDomainApp(t, e, domainApp("fresh", app.Domain{Host: "fresh.example.com"}), false)
	cacheRow(t, e, "gone.example.com", "gone", edge.DNSOK, edge.CertIssued) // its app was deleted

	changes, unsubscribe := e.svc.Subscribe("")
	defer unsubscribe()

	if err := e.svc.checkDomainTargets(ctx, DomainFilter{}, true); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if got, want := dialedHosts(dialer), []string{"api.example.com", "shop.example.com"}; !slices.Equal(got, want) {
		t.Errorf("dialed %v, want only the served app's domains %v", got, want)
	}
	rows, err := e.store.ListDomainChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.Host+" "+r.AppID+" "+r.State+" "+r.CertStatus)
	}
	want := []string{
		"api.example.com shop live issued",
		"fresh.example.com fresh not_served none",
		"old.example.com old not_served none",
		"shop.example.com shop live issued",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if apps := domainNotices(changes); !slices.Equal(apps, []string{"fresh", "gone", "old", "shop"}) {
		t.Errorf("notices for %v, want every app whose rows changed", apps)
	}

	// The same facts again: no state changed, no notice, and the not_served rows aren't rewritten.
	oldRow, _ := e.store.GetDomainCheck(ctx, "old.example.com")
	e.svc.clock.(*fakeClock).Advance(time.Minute)
	if err := e.svc.checkDomainTargets(ctx, DomainFilter{}, true); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if apps := domainNotices(changes); len(apps) != 0 {
		t.Errorf("a pass that changed nothing told %v", apps)
	}
	if again, _ := e.store.GetDomainCheck(ctx, "old.example.com"); !again.CheckedAt.Equal(oldRow.CheckedAt) {
		t.Errorf("the not_served row was rewritten: %v, then %v", oldRow.CheckedAt, again.CheckedAt)
	}
	if row, _ := e.store.GetDomainCheck(ctx, "shop.example.com"); !row.CheckedAt.After(oldRow.CheckedAt) {
		t.Errorf("the served row wasn't checked again: %v", row.CheckedAt)
	}

	// The DNS of one host moves elsewhere, and Traefik has no certificate for it any more (with a
	// valid one it would be a proxy in front, and live): only its app is told.
	resolver.Hosts["api.example.com"] = []string{"198.51.100.9"}
	delete(dialer.Chains, "api.example.com")
	e.svc.SetEdge(resolver, nil)
	if err := e.svc.checkDomainTargets(ctx, DomainFilter{}, true); err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if apps := domainNotices(changes); !slices.Equal(apps, []string{"shop"}) {
		t.Errorf("notices for %v, want [shop]", apps)
	}
	if row, _ := e.store.GetDomainCheck(ctx, "api.example.com"); row.State != string(edge.StateDNSElsewhere) ||
		!slices.Equal(row.DNSSeen, []string{"198.51.100.9"}) || !slices.Equal(row.DNSExpected, []string{"203.0.113.1", "2001:db8::1"}) {
		t.Errorf("the row = %+v", row)
	}

	// RefreshDomains checks one app's hosts now, and deletes nothing.
	cacheRow(t, e, "gone.example.com", "gone", edge.DNSOK, edge.CertIssued)
	before := len(dialer.Dialed())
	if err := e.svc.RefreshDomains(ctx, DomainFilter{App: "shop"}); err != nil {
		t.Fatalf("RefreshDomains: %v", err)
	}
	if n := len(dialer.Dialed()) - before; n != 2 {
		t.Errorf("RefreshDomains dialed %d hosts, want shop's 2", n)
	}
	if _, err := e.store.GetDomainCheck(ctx, "gone.example.com"); err != nil {
		t.Errorf("RefreshDomains deleted a row: %v", err)
	}
	if err := e.svc.RefreshDomains(ctx, DomainFilter{App: "nope"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RefreshDomains of an app that doesn't exist: %v", err)
	}
}

// slowDialer counts the dials going at once and keeps each going a while.
type slowDialer struct {
	inner edge.Dialer
	hold  time.Duration

	mu       sync.Mutex
	now, max int
	total    int
}

func (d *slowDialer) DialTLS(ctx context.Context, addr, serverName string) (*tls.ConnectionState, error) {
	d.mu.Lock()
	d.now++
	d.total++
	d.max = max(d.max, d.now)
	d.mu.Unlock()
	time.Sleep(d.hold)
	d.mu.Lock()
	d.now--
	d.mu.Unlock()
	return d.inner.DialTLS(ctx, addr, serverName)
}

// At most eight checks run at once.
func TestDomainWatchCap(t *testing.T) {
	var domains []app.Domain
	for i := range 20 {
		domains = append(domains, app.Domain{Host: fmt.Sprintf("d%02d.example.com", i)})
	}
	d := &slowDialer{inner: edgeOf(t), hold: 20 * time.Millisecond}
	e := domainEnv(t, edgetest.FakeResolver{}, d)
	seedDomainApp(t, e, domainApp("many", domains...), true)
	if err := e.svc.checkDomainTargets(context.Background(), DomainFilter{}, true); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.total != 20 || d.max > domainProbeParallel || d.max < 2 {
		t.Errorf("%d dials, at most %d at once; want 20, at most %d (and some at once)", d.total, d.max, domainProbeParallel)
	}
	if rows, _ := e.store.ListDomainChecks(context.Background()); len(rows) != 20 {
		t.Errorf("%d rows, want 20", len(rows))
	}
}

// When a pass is due: at once the first time, then after the idle gap, or the watched one while a
// domains page is open, with half a watched gap of slack for a tick that comes early.
func TestDomainPassDue(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	idle, watch := 15*time.Minute, time.Minute
	tests := []struct {
		name    string
		last    time.Time
		since   time.Duration
		watched bool
		want    bool
	}{
		{"never", time.Time{}, 0, false, true},
		{"idle, a minute", t0, time.Minute, false, false},
		{"idle, 14 minutes", t0, 14 * time.Minute, false, false},
		{"idle, a tick early", t0, 15*time.Minute - time.Second, false, true},
		{"idle, 15 minutes", t0, 15 * time.Minute, false, true},
		{"watched, 20 s", t0, 20 * time.Second, true, false},
		{"watched, a tick early", t0, 59 * time.Second, true, true},
		{"watched, a minute", t0, time.Minute, true, true},
	}
	for _, tt := range tests {
		if got := domainPassDue(t0.Add(tt.since), tt.last, tt.watched, idle, watch); got != tt.want {
			t.Errorf("%s: due = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// The watcher's step with a fake clock: a pass the first time, none a minute later, one a minute
// later while a domains page is watched, and none once it closes until the idle gap has passed.
func TestDomainWatchStep(t *testing.T) {
	ctx := context.Background()
	dialer := edgeOf(t, "shop.example.com")
	e := domainEnv(t, edgetest.FakeResolver{Hosts: map[string][]string{"shop.example.com": {hostV4.String()}}}, dialer)
	e.svc.SetDomainWatchGaps(15*time.Minute, time.Minute)
	seedDomainApp(t, e, domainApp("shop", app.Domain{Host: "shop.example.com"}), true)
	clock := e.svc.clock.(*fakeClock)

	passes := func() int { return len(dialer.Dialed()) }
	step := func(last time.Time, wantPasses int, what string) time.Time {
		t.Helper()
		last = e.svc.domainWatchStep(ctx, last)
		if passes() != wantPasses {
			t.Fatalf("%s: %d passes, want %d", what, passes(), wantPasses)
		}
		return last
	}

	last := step(time.Time{}, 1, "the first step")
	if !last.Equal(clock.Now()) {
		t.Errorf("the step returned %v, want the pass's time %v", last, clock.Now())
	}
	clock.Advance(time.Minute)
	last = step(last, 1, "a minute later, nobody watching")

	done := e.svc.MarkDomainsWatched()
	other := e.svc.MarkDomainsWatched()
	clock.Advance(time.Minute)
	last = step(last, 2, "a minute later, watched")
	done()
	done() // a second call changes nothing: the other page is still open
	clock.Advance(time.Minute)
	last = step(last, 3, "a minute later, one page still open")
	other()

	clock.Advance(time.Minute)
	last = step(last, 3, "a minute later, no page open")
	clock.Advance(14 * time.Minute)
	step(last, 4, "the idle gap later")
}

// The gaps' setters keep the defaults for a value that isn't positive.
func TestSetDomainWatchGaps(t *testing.T) {
	s := &Service{domainIdleGap: defaultDomainIdleGap, domainWatchGap: defaultDomainWatchGap}
	s.SetDomainWatchGaps(0, -time.Second)
	if s.domainIdleGap != 15*time.Minute || s.domainWatchGap != time.Minute {
		t.Errorf("gaps = %v, %v; want the defaults", s.domainIdleGap, s.domainWatchGap)
	}
	s.SetDomainWatchGaps(time.Minute, 2*time.Second)
	if s.domainIdleGap != time.Minute || s.domainWatchGap != 2*time.Second {
		t.Errorf("gaps = %v, %v", s.domainIdleGap, s.domainWatchGap)
	}
}
