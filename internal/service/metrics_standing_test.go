package service

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A row's mark is the app's status first: what the Apps home shows for the app, seen from one service.
func TestStandingOf(t *testing.T) {
	rec := func(stopped bool, replicas ...int) store.AppRecord {
		r := store.AppRecord{App: appWith("shop", replicas...)}
		if stopped {
			at := time.Now()
			r.StoppedAt = &at
		}
		return r
	}
	dep := func(status store.Status) *store.DeploymentRecord {
		d := store.DeploymentRecord{}
		d.Status = status
		return &d
	}
	healthy := func(group string) Copy { return copyOfVersion(group, 1, deploy.CopyHealthy) }
	tests := []struct {
		name   string
		rec    store.AppRecord
		svc    string
		latest *store.DeploymentRecord
		copies []Copy
		known  bool
		want   ServiceStanding
	}{
		{"healthy", rec(false, 1), "web", dep(store.StatusHealthy), []Copy{healthy("web")}, true, ServiceStanding{Mark: "healthy", Word: "healthy"}},
		{"stopped", rec(true, 1), "web", dep(store.StatusHealthy), nil, true, ServiceStanding{Mark: "idle", Word: "stopped", Down: true}},
		{"not running", rec(false, 1), "web", dep(store.StatusHealthy), nil, true, ServiceStanding{Mark: "failed", Word: "not running", Down: true}},
		{"never deployed", rec(false, 1), "web", nil, nil, true, ServiceStanding{Mark: "idle", Word: "never deployed", Down: true}},
		{"rolling out", rec(false, 1), "web", dep(store.StatusRunning), []Copy{healthy("web")}, true, ServiceStanding{Mark: "rolling", Word: "rolling out"}},
		{"deploy failed but copies serve", rec(false, 1), "web", dep(store.StatusFailed), []Copy{healthy("web")}, true, ServiceStanding{Mark: "failed", Word: "deploy failed"}},
		{"some copies short", rec(false, 2), "web", dep(store.StatusHealthy), []Copy{healthy("web")}, true, ServiceStanding{Mark: "warning", Word: "1 of 2 healthy"}},
		{"another service has none", rec(false, 1, 1), "worker", dep(store.StatusHealthy), []Copy{healthy("web")}, true, ServiceStanding{Mark: "failed", Word: "not running", Down: true}},
		{"its sibling serves", rec(false, 1, 1), "web", dep(store.StatusHealthy), []Copy{healthy("web")}, true, ServiceStanding{Mark: "warning", Word: "1 of 2 healthy"}},
		{"nomad unreadable claims nothing", rec(false, 1), "web", dep(store.StatusHealthy), nil, false, ServiceStanding{Mark: "healthy", Word: "healthy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := standingOf(tt.rec, tt.svc, tt.latest, tt.copies, tt.known); got != tt.want {
				t.Errorf("standing = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A service that serves nothing gets no "quieter" issue (its cause is on the needs-you list), while
// the other rules still apply to it; the same traffic from a serving service is an issue.
func TestMetricsQuieterNeedsAServingService(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	f.startedAt(now.Add(-4 * 24 * time.Hour))
	f.slotsOnEarlierDays(web, 3, now.Add(-time.Hour), now, func(series int64, ts time.Time) store.MetricsSample {
		return f.http(series, ts, 600, 6000, 0, fastBuckets)
	})
	var rows []store.MetricsSample
	for ts := now.Truncate(time.Minute).Add(-9 * time.Minute); ts.Before(now.Truncate(time.Minute)); ts = ts.Add(time.Minute) {
		rows = append(rows, mem(f.http(web, ts, 60, 60, 0, fastBuckets), 960, 1000)) // quiet, and at 96 % memory
	}
	f.minute(rows...)
	finalDeployment(t, f.e.store, "whoami", "d1", store.StatusHealthy, 0)

	f.e.nomad.SetCopies("whoami") // nothing runs
	v := f.read(metrics.Range6h)
	if len(v.Issues) != 1 || v.Issues[0].Kind != metrics.Memory {
		t.Fatalf("not running: issues = %+v, want only memory", v.Issues)
	}
	if sv := v.Services[0]; sv.Standing.Mark != "failed" || sv.Standing.Word != "not running" || !sv.Standing.Down {
		t.Errorf("not running: standing = %+v", sv.Standing)
	}

	f.e.nomad.SetCopies("whoami", runningStub("web", 0), runningStub("web", 0)) // whoami asks for two
	v = f.read(metrics.Range6h)
	kinds := map[metrics.Kind]bool{}
	for _, is := range v.Issues {
		kinds[is.Kind] = true
	}
	if !kinds[metrics.Quieter] || !kinds[metrics.Memory] || v.Services[0].Standing.Mark != "healthy" {
		t.Errorf("serving: issues = %+v standing = %+v, want quieter and memory", v.Issues, v.Services[0].Standing)
	}
}
