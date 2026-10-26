package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Two markers closer than a label is wide become one, whose label counts both and whose title lists
// all; markers far apart stay two. At 24 h the plot is 300 units for 24 hours, so 2% is 29 minutes.
func TestMonDeployMarkersMergeWhenTheirLabelsWouldCollide(t *testing.T) {
	rng := metrics.Range24h
	build := func(deploys ...service.MetricDeploy) *monPlot {
		v := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), HasHTTP: true,
			Baseline: service.MetricsBaseline{State: metrics.Full}}
		v.Deploys = deploys
		v.Requests, v.Errors, v.P95, v.Memory = monSeries(len(monPoints(rng)), 5), monSeries(len(monPoints(rng)), 0), monSeries(len(monPoints(rng)), 90), monSeries(len(monPoints(rng)), 300)
		return monBuild(v, false).charts()[3].Plot
	}
	burst := func(app string, ago time.Duration, n int) (out []service.MetricDeploy) {
		for i := 0; i < n; i++ { // a minute apart: one marker
			out = append(out, service.MetricDeploy{AppName: app, Version: uint64(i + 1), EndedAt: monNow.Add(-ago + time.Duration(i)*time.Minute)})
		}
		return out
	}
	// Two clusters of 3, the second 29 minutes (2% of 24 h) after the first.
	near := build(append(burst("a", 10*time.Hour, 3), burst("b", 10*time.Hour-29*time.Minute, 3)...)...)
	if len(near.Deploys) != 1 || strings.Join(near.Labels, "|") != "6 deploys" {
		t.Fatalf("near clusters: %d markers, labels %v, want one marker labelled 6 deploys", len(near.Deploys), near.Labels)
	}
	for _, want := range []string{"6 deploys rolled out:", "a v1 at ", "b v3 at "} {
		if !strings.Contains(near.Deploys[0].Title, want) {
			t.Errorf("merged title lacks %q: %q", want, near.Deploys[0].Title)
		}
	}
	// Two clusters of 3 eight hours apart stay two, each "3 deploys".
	far := build(append(burst("a", 12*time.Hour, 3), burst("b", 4*time.Hour, 3)...)...)
	if len(far.Deploys) != 2 || strings.Join(far.Labels, "|") != "3 deploys|3 deploys" {
		t.Errorf("far clusters: %d markers, labels %v, want two", len(far.Deploys), far.Labels)
	}
}

// A stopped app has no "now": its row shows a dash in every value cell but Restarts, and the CPU
// sparkline stays as history.
func TestMonStoppedRowHasNoNow(t *testing.T) {
	cpu := []*float64{fl(10), fl(20), fl(30)}
	v := service.MetricsView{Range: metrics.Range6h, At: monNow, Services: []service.ServiceMetrics{
		{AppID: "old", AppName: "old", Service: "web", State: service.ServiceStopped, HasDomain: true,
			Standing: service.ServiceStanding{Mark: "idle", Word: "stopped", Down: true},
			Requests: fl(0.4), Errors: fl(1), P95: fl(11), CPU: cpu, CPUReservedMHz: i64(100),
			MemoryMB: i64(100), MemoryLimitMB: i64(256), Restarts: i64(2)},
	}}
	b := monBuild(v, false)
	b.coreMHz = 100
	row := b.table().Rows[0]
	for name, n := range map[string]monNum{"requests": row.Requests, "errors": row.Errors, "p95": row.P95} {
		if n.Text != "—" || !n.None {
			t.Errorf("%s of a stopped app = %+v, want a dash", name, n)
		}
	}
	if row.Mem.Known {
		t.Errorf("memory of a stopped app = %+v, want a dash", row.Mem)
	}
	if row.Spark == nil || len(row.Spark.Segments) == 0 || row.Spark.Read != "—" {
		t.Errorf("sparkline = %+v, want the history line with no figure", row.Spark)
	}
	if row.Restarts.Text != "2" {
		t.Errorf("restarts = %+v, want its own number", row.Restarts)
	}
}

// A running app that uses a little CPU never reads as idle: under 0.01 vCPU is "<0.01", exactly 0 is "0".
func TestMonCPUUnderAHundredth(t *testing.T) {
	b := &monBuilder{coreMHz: 1000}
	if got := b.cpuRead(5, i64(50)); got != "<0.01/0.05 vCPU" {
		t.Errorf("read = %q", got)
	}
	if got := b.cpuRead(0, i64(50)); got != "0/0.05 vCPU" {
		t.Errorf("read at exactly 0 = %q", got)
	}
	if got := b.cpuTitle(5, i64(50)); got != "<0.01 of 0.05 vCPU reserved, now" {
		t.Errorf("title = %q", got)
	}
	if got := vcpuText(0.004); got != "<0.01" {
		t.Errorf("vcpuText = %q", got)
	}
}
