package service

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A chart's head is the last 10 minutes at every range, not the range's last point: the same
// number on 1 h, 6 h, 24 h and 7 d, and none when the window has nothing.
func TestMetricsChartHeadIsTheLastTenMinutes(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	f.startedAt(now.Add(-2 * time.Hour))
	var rows []store.MetricsSample
	for i := 60; i >= 1; i-- {
		ts := now.Truncate(time.Minute).Add(-time.Duration(i) * time.Minute)
		if i > 10 {
			rows = append(rows, mem(f.http(web, ts, 60, 600, 6, fastBuckets), 300, 512)) // 10 a second an hour ago
		} else {
			rows = append(rows, mem(f.http(web, ts, 60, 120, 0, fastBuckets), 400, 512)) // 2 a second lately
		}
	}
	f.minute(rows...)

	for _, rng := range metrics.Ranges() {
		v := f.read(rng)
		if v.Requests.Now == nil || *v.Requests.Now != 2 {
			t.Errorf("%s: requests head = %v, want 2 (the last 10 minutes)", rng.Name, val(v.Requests.Now))
		}
		if v.Errors.Now == nil || *v.Errors.Now != 0 {
			t.Errorf("%s: errors head = %v, want 0", rng.Name, val(v.Errors.Now))
		}
		if v.P95.Now == nil || *v.P95.Now > 100 {
			t.Errorf("%s: slowest 5%% head = %v, want under 100 ms", rng.Name, val(v.P95.Now))
		}
		if v.Memory.Now == nil || *v.Memory.Now != 400 {
			t.Errorf("%s: memory head = %v, want 400", rng.Name, val(v.Memory.Now))
		}
	}

	// A sampler that stopped 20 minutes ago leaves no head.
	f.e.svc.clock.(*fakeClock).Set(now.Add(20 * time.Minute))
	if v := f.read(metrics.Range1h); v.Requests.Now != nil || v.Memory.Now != nil || v.P95.Now != nil {
		t.Errorf("stale heads = %v %v %v, want none", val(v.Requests.Now), val(v.Memory.Now), val(v.P95.Now))
	}
}

// A deploy is marked as of an app with a domain or not: only the first belongs on the HTTP charts.
func TestMetricsDeploysSayWhetherTheAppHasADomain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute).Add(5*time.Minute + 30*time.Second)
	f := newMetricsFixture(t, now)
	batch := appWith("batch", 1)
	batch.Services[0].Domains = nil
	f.save(batch)
	f.startedAt(now.Add(-2 * time.Hour))
	finalDeployment(t, f.e.store, "whoami", "d-web", store.StatusHealthy, 1)
	finalDeployment(t, f.e.store, "batch", "d-batch", store.StatusHealthy, 4)

	got := map[string]bool{}
	for _, d := range f.read(metrics.Range6h).Deploys {
		got[d.AppID] = d.HasDomain
	}
	if len(got) != 2 || !got["whoami"] || got["batch"] {
		t.Errorf("deploys have domains = %v, want whoami true and batch false", got)
	}
}
