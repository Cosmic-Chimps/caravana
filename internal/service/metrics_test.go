package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The page's reads over rows written the way the sampler writes them: the
// tests seed the ring buffer and move the clock, so every number is computed
// by the code under test and nothing is read from a live source.

const (
	fastBuckets = "0,0,0,0,100"      // p95 inside (50 ms, 100 ms]: 95 ms
	slow20      = "0,0,0,0,0,0,0,20" // one minute of 20 requests in (0.5 s, 1 s]
	fast20      = "0,0,0,0,20"       // one minute of 20 requests in (50 ms, 100 ms]
)

type metricsFixture struct {
	t      *testing.T
	e      *env
	ctx    context.Context
	now    time.Time
	layout int64
}

// newMetricsFixture makes an env whose service clock says now, with the
// whoami app (web with a domain) stored. Pass a now with whole seconds.
func newMetricsFixture(t *testing.T, now time.Time) *metricsFixture {
	t.Helper()
	f := &metricsFixture{t: t, e: newEnv(t), ctx: context.Background(), now: now.UTC()}
	f.e.svc.clock.(*fakeClock).Set(f.now)
	id, err := f.e.store.EnsureLayout(f.ctx, metrics.TraefikBounds)
	if err != nil {
		t.Fatal(err)
	}
	f.layout = id
	if _, err := f.e.store.SaveApp(f.ctx, whoami(), nil); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *metricsFixture) save(a app.App) {
	f.t.Helper()
	if _, err := f.e.store.SaveApp(f.ctx, a, nil); err != nil {
		f.t.Fatal(err)
	}
}

func (f *metricsFixture) series(appID, service string) int64 {
	f.t.Helper()
	id, err := f.e.store.EnsureSeries(f.ctx, appID, service)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

// startedAt writes the first tick, so the baseline counts from at.
func (f *metricsFixture) startedAt(at time.Time) {
	f.t.Helper()
	if err := f.e.store.WriteMetricsTick(f.ctx, store.MetricsTick{At: at}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *metricsFixture) minute(rows ...store.MetricsSample) {
	f.t.Helper()
	if err := f.e.store.WriteMetricsTick(f.ctx, store.MetricsTick{Minute: store.MetricsRows{Samples: rows}}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *metricsFixture) slots(rows ...store.MetricsSample) {
	f.t.Helper()
	if err := f.e.store.WriteMetricsTick(f.ctx, store.MetricsTick{Fold: store.MetricsRows{Samples: rows}}); err != nil {
		f.t.Fatal(err)
	}
}

// http is a row with HTTP numbers: secs seconds covered, requests, errors and
// the buckets' text.
func (f *metricsFixture) http(series int64, ts time.Time, secs, requests, errs int64, buckets string) store.MetricsSample {
	m := store.MetricsSample{
		Series: series, TS: ts.UTC(), Minutes: int(secs / 60), HTTPSecs: &secs, Requests: &requests, Errors: &errs,
		Layout: &f.layout,
	}
	if m.Minutes == 0 {
		m.Minutes = 1
	}
	if buckets != "" {
		m.Buckets = &buckets
	}
	return m
}

// mem adds memory numbers to a row.
func mem(m store.MetricsSample, used, limit int64) store.MetricsSample {
	m.MemoryMB, m.MemoryLimitMB = &used, &limit
	return m
}

// slotsOnEarlierDays writes a 10-minute row at each of the slots on days
// 1..days before, for the slots covering [from, to].
func (f *metricsFixture) slotsOnEarlierDays(series int64, days int, from, to time.Time, row func(series int64, ts time.Time) store.MetricsSample) {
	f.t.Helper()
	var rows []store.MetricsSample
	for d := 1; d <= days; d++ {
		for t := from.Truncate(metrics.SlotLength); !t.After(to); t = t.Add(metrics.SlotLength) {
			rows = append(rows, row(series, t.Add(-time.Duration(d)*24*time.Hour)))
		}
	}
	f.slots(rows...)
}

func (f *metricsFixture) read(rng metrics.Range) MetricsView {
	f.t.Helper()
	v, err := f.e.svc.Metrics(f.ctx, MetricsQuery{Range: rng})
	if err != nil {
		f.t.Fatalf("Metrics: %v", err)
	}
	return v
}

func val(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestMetricsPlaceAndSort(t *testing.T) {
	f := newMetricsFixture(t, time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC))
	ctx := f.ctx
	if _, err := f.e.svc.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		q     MetricsQuery
		field string // the PlaceError's field, or the FieldError's
	}{
		{"unknown project", MetricsQuery{Project: "nope", Environment: "production"}, "project"},
		{"unknown environment", MetricsQuery{Project: "shop", Environment: "staging"}, "environment"},
		{"unknown sort", MetricsQuery{Sort: "loudest"}, "sort"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.e.svc.Metrics(ctx, tc.q)
			var pe *PlaceError
			var fe *app.FieldError
			switch {
			case errors.As(err, &pe):
				if pe.Field != tc.field {
					t.Errorf("PlaceError field = %q, want %q", pe.Field, tc.field)
				}
			case errors.As(err, &fe):
				if fe.Field != tc.field {
					t.Errorf("FieldError field = %q, want %q", fe.Field, tc.field)
				}
			default:
				t.Fatalf("err = %v, want a *PlaceError or *app.FieldError on %s", err, tc.field)
			}
		})
	}

	if _, err := f.e.svc.Metrics(ctx, MetricsQuery{Project: "shop", Environment: "production"}); err != nil {
		t.Errorf("an empty environment of a real project: %v", err)
	}
	if _, err := f.e.svc.AppMetrics(ctx, "ghost", metrics.Range6h, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("AppMetrics of an unknown app = %v, want store.ErrNotFound", err)
	}
	for name, want := range map[string]MetricsSort{"": SortUnusual, "unusual": SortUnusual, "traffic": SortTraffic, "memory": SortMemory} {
		if got, ok := ParseMetricsSort(name); !ok || got != want {
			t.Errorf("ParseMetricsSort(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	if _, ok := ParseMetricsSort("name"); ok {
		t.Error("ParseMetricsSort accepted name")
	}
}

// Nothing sampled yet: every point is a gap, nothing is called unusual, the
// baseline is being built, and the answer has the shape of the range.
func TestMetricsNothingSampled(t *testing.T) {
	f := newMetricsFixture(t, time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC))
	for _, rng := range metrics.Ranges() {
		v := f.read(rng)
		if len(v.Points) != rng.N() || len(v.Requests.Points) != rng.N() || len(v.MemoryLimit) != rng.N() {
			t.Fatalf("%s: %d points, %d values; want %d", rng.Name, len(v.Points), len(v.Requests.Points), rng.N())
		}
		if !v.Points[len(v.Points)-1].Equal(rng.From(f.now).Add(rng.Span - rng.Step)) {
			t.Errorf("%s: the last point = %v", rng.Name, v.Points[len(v.Points)-1])
		}
		for _, p := range v.Requests.Points {
			if p.Value != nil || p.Low != nil {
				t.Fatalf("%s: a point of nothing = %+v", rng.Name, p)
			}
		}
		if v.Requests.Now != nil || len(v.Issues) != 0 || v.Baseline.State != metrics.Building || v.Baseline.Since != nil {
			t.Errorf("%s: now %v, issues %v, baseline %+v", rng.Name, v.Requests.Now, v.Issues, v.Baseline)
		}
		if len(v.Services) != 1 || v.Services[0].State != ServiceHealthy || v.Services[0].Requests != nil || !v.HasHTTP {
			t.Errorf("%s: services = %+v", rng.Name, v.Services)
		}
	}
}

// Points sum a counter and take the median of a gauge, a gap stays a gap, and
// the band is the same slot on the earlier days: absent while the baseline is
// built, "over N days" after.
func TestMetricsPointsAndBand(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")

	// 12 minutes of 120 requests a minute (2 a second) with a gap at 11:55.
	var rows []store.MetricsSample
	for i := 12; i >= 1; i-- {
		ts := now.Truncate(time.Minute).Add(-time.Duration(i) * time.Minute)
		if ts.Minute() == 55 {
			continue
		}
		rows = append(rows, mem(f.http(web, ts, 60, 120, 0, fastBuckets), 100+int64(i), 512))
	}
	f.minute(rows...)

	t.Run("while the baseline is built", func(t *testing.T) {
		f.startedAt(now.Add(-2 * time.Hour))
		v := f.read(metrics.Range1h)
		last := v.Requests.Points[len(v.Requests.Points)-1]
		if last.Value != nil { // the minute in progress has no row yet
			t.Errorf("the point of the minute in progress = %v, want a gap", *last.Value)
		}
		at := func(h, m int) SeriesPoint {
			for i, p := range v.Points {
				if p.Hour() == h && p.Minute() == m {
					return v.Requests.Points[i]
				}
			}
			t.Fatalf("no point at %02d:%02d", h, m)
			return SeriesPoint{}
		}
		if p := at(11, 58); val(p.Value) != 2 || p.Low != nil || p.Days != 0 {
			t.Errorf("11:58 = %+v, want 2 req/s and no band", p)
		}
		if p := at(11, 55); p.Value != nil {
			t.Errorf("the missing minute = %v, want a gap", *p.Value)
		}
		if v.Requests.Now == nil || *v.Requests.Now != 2 || v.Requests.Usual != nil || v.Requests.UsualDays != 0 {
			t.Errorf("now = %v usual %v days %d", v.Requests.Now, v.Requests.Usual, v.Requests.UsualDays)
		}
		if v.Memory.Now == nil || *v.Memory.Now != 105 || val(v.MemoryLimit[len(v.MemoryLimit)-2]) != 512 {
			t.Errorf("memory head = %v (the median of the last 10 minutes: 105), limit %v", v.Memory.Now, v.MemoryLimit[len(v.MemoryLimit)-2])
		}
		if v.Baseline.State != metrics.Building || v.Baseline.FullDays != 0 {
			t.Errorf("baseline = %+v", v.Baseline)
		}
	})
}

func TestMetricsBandPartial(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	f.startedAt(now.Add(-3*24*time.Hour - time.Hour))

	f.minute(f.http(web, now.Truncate(time.Minute).Add(-time.Minute), 60, 120, 0, fastBuckets))
	// The same slots on 3 earlier days: 60, 90 and 120 requests a minute.
	per := map[int]int64{1: 60, 2: 90, 3: 120}
	for d, n := range per {
		ts := now.Add(-5 * time.Minute).Truncate(metrics.SlotLength).Add(-time.Duration(d) * 24 * time.Hour)
		f.slots(f.http(web, ts, 600, n*10, 0, fastBuckets))
	}

	v := f.read(metrics.Range1h)
	if v.Baseline.State != metrics.Partial || v.Baseline.FullDays != 3 {
		t.Fatalf("baseline = %+v, want partial over 3 days", v.Baseline)
	}
	// The newest point with a value is the minute before: its slot has the three days.
	r := v.Requests
	if r.Now == nil || *r.Now != 2 || r.UsualDays != 3 || r.Usual == nil || *r.Usual != 1.5 {
		t.Fatalf("now %v usual %v over %d days; want 2, 1.5, 3", r.Now, r.Usual, r.UsualDays)
	}
	p := r.Points[len(r.Points)-2]
	if val(p.Low) != 1 || val(p.High) != 2 || val(p.Usual) != 1.5 || p.Days != 3 {
		t.Errorf("band = %v..%v usual %v over %d days; want 1..2, 1.5, 3 (per second)", val(p.Low), val(p.High), val(p.Usual), p.Days)
	}
}

// The environment's slowest 5 % comes from the services' merged buckets, not
// from a mean of their slowest 5 %, and a place's totals add up its services.
func TestMetricsTotalsMergeBuckets(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	a := appWith("whoami", 1)
	a.Services = append(a.Services, appWith("blog", 1).Services[0])
	a.Services[1].Name = "api" // a second service with a domain
	a.Services[1].Domains = []app.Domain{{Host: "api.localhost", Port: "http"}}
	a.Services[1].Ports = []app.Port{{Name: "http", ContainerPort: 80}}
	f.save(a)
	web, api2 := f.series("whoami", "web"), f.series("whoami", "api")
	ts := now.Truncate(time.Minute).Add(-time.Minute)

	// web: 1,000 fast requests; api: 10 slow ones (p95 above 1 s). Merged, the
	// slowest 5 % is still fast: the 10 are 1 % of the 1,010.
	web1 := f.http(web, ts, 60, 1000, 0, "0,0,0,0,1000")
	api1 := f.http(api2, ts, 60, 10, 10, "0,0,0,0,0,0,0,0,0,0,0,10")
	web1.MemoryMB, api1.MemoryMB = new(int64(300)), new(int64(200))
	f.minute(web1, api1)

	v := f.read(metrics.Range1h)
	if v.P95.Now == nil || *v.P95.Now > 100 || v.P95.Over {
		t.Errorf("the total slowest 5%% = %v (over %v), want a value under 100 ms from merged buckets", v.P95.Now, v.P95.Over)
	}
	if v.Requests.Now == nil || *v.Requests.Now != 1010.0/60 {
		t.Errorf("total requests = %v, want %v (summed over one minute, not over two services' seconds)", v.Requests.Now, 1010.0/60)
	}
	if v.Errors.Now == nil || val(v.Errors.Now) < 0.99 || val(v.Errors.Now) > 1 {
		t.Errorf("total errors = %v, want 10/1010 percent", v.Errors.Now)
	}
	if v.Memory.Now == nil || *v.Memory.Now != 500 {
		t.Errorf("total memory = %v, want the sum 500", v.Memory.Now)
	}
	byName := map[string]ServiceMetrics{}
	for _, s := range v.Services {
		byName[s.Service] = s
	}
	if p := byName["api"].P95; p == nil || *p < 1000 {
		t.Errorf("the api's own slowest 5%% = %v, want over 1 s", p)
	}
}

func TestMetricsSlowerIssue(t *testing.T) {
	for _, tc := range []struct {
		name       string
		deployAt   time.Duration // the clock's distance from the real time the deployment ended at
		relation   metrics.Relation
		minutesMin int
	}{
		{"a deploy 10 minutes before it started", 20 * time.Minute, metrics.StartedAfter, 9},
		{"a deploy that rolled out after it started", 5 * time.Minute, metrics.StartedBefore, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			real := time.Now().UTC()
			now := real.Truncate(time.Minute).Add(tc.deployAt + 30*time.Second)
			f := newMetricsFixture(t, now)
			web := f.series("whoami", "web")
			f.startedAt(now.Add(-4 * 24 * time.Hour))
			finalDeployment(t, f.e.store, "whoami", "dep-1", store.StatusHealthy, 24)

			// The usual: fast, every slot of the last hour on 3 earlier days.
			f.slotsOnEarlierDays(web, 3, now.Add(-time.Hour), now, func(series int64, ts time.Time) store.MetricsSample {
				return f.http(series, ts, 600, 200, 0, fastBuckets)
			})
			// The last 20 minutes: fast, then slow for the last 9.
			slowFrom := now.Truncate(time.Minute).Add(-9 * time.Minute)
			var rows []store.MetricsSample
			for ts := now.Truncate(time.Minute).Add(-20 * time.Minute); ts.Before(now.Truncate(time.Minute)); ts = ts.Add(time.Minute) {
				b := fast20
				if !ts.Before(slowFrom) {
					b = slow20
				}
				rows = append(rows, f.http(web, ts, 60, 20, 0, b))
			}
			f.minute(rows...)

			v := f.read(metrics.Range6h)
			if len(v.Issues) != 1 {
				t.Fatalf("issues = %+v, want one", v.Issues)
			}
			is := v.Issues[0]
			if is.Kind != metrics.Slower || is.Level != metrics.LevelWarning || is.AppID != "whoami" || is.Service != "web" || is.AppName != "whoami" {
				t.Errorf("issue = %+v", is)
			}
			if is.Value < 800 || is.Usual == nil || *is.Usual < 90 || *is.Usual > 100 || is.Window.Requests == 0 {
				t.Errorf("value %v usual %v window %+v", is.Value, is.Usual, is.Window)
			}
			if !is.Since.Equal(slowFrom) || is.SinceCapped {
				t.Errorf("since = %v capped %v, want %v", is.Since, is.SinceCapped, slowFrom)
			}
			if is.DeployNear == nil || is.DeployNear.Relation != tc.relation || is.DeployNear.Version != 24 || is.DeployNear.AppName != "whoami" || is.DeployNear.DeploymentID != "dep-1" {
				t.Fatalf("deploy clause = %+v, want %s whoami v24", is.DeployNear, tc.relation)
			}
			if tc.relation == metrics.StartedAfter && (is.DeployNear.Minutes < tc.minutesMin || is.DeployNear.Minutes > 11) {
				t.Errorf("minutes after = %d, want about 10", is.DeployNear.Minutes)
			}
			if len(v.Deploys) != 1 || v.Deploys[0].Version != 24 {
				t.Errorf("deploys = %+v, want the one marker", v.Deploys)
			}
			sv := v.Services[0]
			if sv.State != ServiceUnusual || !sv.Unusual || sv.Level != metrics.LevelWarning || sv.Distance == nil || *sv.Distance != is.Distance ||
				sv.UsualP95 == nil {
				t.Errorf("service = %+v", sv)
			}
			// The same slowdown is the chart's warn rectangle, on the totals.
			if v.P95.UnusualSince == nil || !v.P95.UnusualSince.Equal(slowFrom) || v.Errors.UnusualSince != nil {
				t.Errorf("unusual since: p95 %v errors %v, want %v and none", v.P95.UnusualSince, v.Errors.UnusualSince, slowFrom)
			}
			// The app's own read gives the same issue and its own deploys.
			av, err := f.e.svc.AppMetrics(f.ctx, "whoami", metrics.Range6h, SortUnusual)
			if err != nil || len(av.Issues) != 1 || len(av.Deploys) != 1 {
				t.Errorf("AppMetrics = %+v, %v", av.Issues, err)
			}
		})
	}
}

// No deploy clause when no healthy deploy is near, and not for one that
// failed.
func TestMetricsNoDeployClause(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute).Add(90*time.Minute + 30*time.Second)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	f.startedAt(now.Add(-4 * 24 * time.Hour))
	finalDeployment(t, f.e.store, "whoami", "dep-bad", store.StatusFailed, 3)
	finalDeployment(t, f.e.store, "whoami", "dep-old", store.StatusHealthy, 2) // 90 minutes ago

	f.slotsOnEarlierDays(web, 2, now.Add(-time.Hour), now, func(series int64, ts time.Time) store.MetricsSample {
		return f.http(series, ts, 600, 200, 0, fastBuckets)
	})
	var rows []store.MetricsSample
	for ts := now.Truncate(time.Minute).Add(-9 * time.Minute); ts.Before(now.Truncate(time.Minute)); ts = ts.Add(time.Minute) {
		rows = append(rows, f.http(web, ts, 60, 20, 0, slow20))
	}
	f.minute(rows...)
	v := f.read(metrics.Range1h)
	if len(v.Issues) != 1 || v.Issues[0].DeployNear != nil {
		t.Fatalf("issues = %+v, want one with no deploy clause", v.Issues)
	}
	if len(v.Deploys) != 0 {
		t.Errorf("deploys = %+v: markers show the ones that ended in the hour, and none did", v.Deploys)
	}
}

func TestMetricsMoreErrorsQuieterAndLevels(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	a := appWith("whoami", 1)
	a.Services = append(a.Services, appWith("x", 1).Services[0])
	a.Services[1].Name = "quiet"
	a.Services[1].Domains = []app.Domain{{Host: "quiet.localhost", Port: "http"}}
	a.Services[1].Ports = []app.Port{{Name: "http", ContainerPort: 80}}
	f.save(a)
	web, quiet := f.series("whoami", "web"), f.series("whoami", "quiet")
	f.startedAt(now.Add(-4 * 24 * time.Hour))

	usual := func(series int64, ts time.Time) store.MetricsSample {
		return f.http(series, ts, 600, 6000, 0, fastBuckets)
	}
	f.slotsOnEarlierDays(web, 3, now.Add(-time.Hour), now, usual)
	f.slotsOnEarlierDays(quiet, 3, now.Add(-time.Hour), now, usual)

	var rows []store.MetricsSample
	for ts := now.Truncate(time.Minute).Add(-9 * time.Minute); ts.Before(now.Truncate(time.Minute)); ts = ts.Add(time.Minute) {
		rows = append(rows, f.http(web, ts, 60, 600, 90, fastBuckets)) // 15 % errors
		rows = append(rows, f.http(quiet, ts, 60, 60, 0, fastBuckets)) // 1 a second against 10
	}
	f.minute(rows...)
	// Both services serve: a deployment ended healthy and each has a healthy copy.
	finalDeployment(t, f.e.store, "whoami", "d1", store.StatusHealthy, 0)
	f.e.nomad.SetCopies("whoami", runningStub("web", 0), runningStub("quiet", 0))

	v := f.read(metrics.Range6h)
	if len(v.Issues) != 2 {
		t.Fatalf("issues = %+v, want two", v.Issues)
	}
	kinds := map[string]MetricIssue{}
	for _, is := range v.Issues {
		kinds[is.Service] = is
	}
	if e := kinds["web"]; e.Kind != metrics.MoreErrors || e.Level != metrics.LevelFailed || e.Value != 15 || e.Window.Errors != 810 {
		t.Errorf("web = %+v, want more errors, failed (above 10 %%), 15 %%", e)
	}
	if q := kinds["quiet"]; q.Kind != metrics.Quieter || q.Level != metrics.LevelWarning || q.Value != 1 ||
		q.Usual == nil || *q.Usual != 10 || len(q.Domains) != 1 || q.Domains[0] != "quiet.localhost" {
		t.Errorf("quiet = %+v, want quieter, 1 req/s usually 10, its domain", q)
	}
	// Furthest past its rule first: errors at 15 against a top of 0 is capped
	// at 100; quieter is 10 / 1 = 10.
	if v.Issues[0].Service != "web" || v.Issues[0].Distance != metrics.MaxDistance || v.Issues[1].Distance != 10 {
		t.Errorf("order/distances = %s %v, %s %v", v.Issues[0].Service, v.Issues[0].Distance, v.Issues[1].Service, v.Issues[1].Distance)
	}
	if v.Services[0].Service != "web" || v.Services[0].Level != metrics.LevelFailed || v.Services[1].Level != metrics.LevelWarning {
		t.Errorf("unusual first, by distance: %+v", v.Services)
	}
	if v.Errors.UnusualSince == nil {
		t.Error("the Errors chart has no warn rectangle")
	}
}

func TestMetricsMemoryRestartingAndNomadDown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	f.startedAt(now.Add(-time.Hour)) // building: only the absolute rules

	var rows []store.MetricsSample
	for ts := now.Truncate(time.Minute).Add(-9 * time.Minute); ts.Before(now.Truncate(time.Minute)); ts = ts.Add(time.Minute) {
		rows = append(rows, mem(f.http(web, ts, 60, 0, 0, ""), 950, 1000))
	}
	f.minute(rows...)

	exited := time.Now().Add(-2 * time.Minute)
	terminated := &api.TaskEvent{Type: api.TaskTerminated, Time: exited.UnixNano(), Details: map[string]string{"exit_code": "137"}}
	crashing := func(id string, restarts uint64) api.AllocationListStub {
		return api.AllocationListStub{ID: id, TaskGroup: "web", JobVersion: 0, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {
				Restarts: restarts, LastRestart: time.Now().Add(-time.Minute), Events: []*api.TaskEvent{terminated},
			}}}
	}

	t.Run("memory and restarting with the last exit", func(t *testing.T) {
		f.e.nomad.SetCopies("whoami", crashing("e4d1aaaa", 3))
		v := f.read(metrics.Range6h)
		if len(v.Issues) != 2 {
			t.Fatalf("issues = %+v, want memory and restarting", v.Issues)
		}
		by := map[metrics.Kind]MetricIssue{}
		for _, is := range v.Issues {
			by[is.Kind] = is
		}
		m, r := by[metrics.Memory], by[metrics.Restarting]
		if m.Value != 95 || m.LimitMB == nil || *m.LimitMB != 1000 || m.Restarts == nil || *m.Restarts != 3 || m.Level != metrics.LevelWarning {
			t.Errorf("memory = %+v", m)
		}
		if r.Value != 3 || r.Restarts == nil || *r.Restarts != 3 || r.Usual != nil {
			t.Errorf("restarting = %+v", r)
		}
		for _, is := range []MetricIssue{m, r} {
			if is.LastExit == nil || is.LastExit.ExitCode != 137 || is.LastExit.Copy != "e4d1aaaa" || is.LastExit.At.Sub(exited.UTC()) > time.Second {
				t.Errorf("%s last exit = %+v", is.Kind, is.LastExit)
			}
		}
		if v.Issues[0].Kind != metrics.Restarting { // 1 + 3/10 beats 95/90
			t.Errorf("first = %s, want restarting (distance %v against %v)", v.Issues[0].Kind, by[metrics.Restarting].Distance, m.Distance)
		}
		if v.Services[0].Copies != 1 {
			t.Errorf("copies = %d, want the one live copy", v.Services[0].Copies)
		}
	})

	t.Run("two restarts is the earlier warning, one is nothing", func(t *testing.T) {
		f.e.nomad.SetCopies("whoami", crashing("c1", 2))
		if v := f.read(metrics.Range6h); !hasKind(v.Issues, metrics.Restarting) {
			t.Errorf("two restarts: issues = %+v", v.Issues)
		}
		f.e.nomad.SetCopies("whoami", crashing("c1", 1))
		if v := f.read(metrics.Range6h); hasKind(v.Issues, metrics.Restarting) {
			t.Errorf("one restart: issues = %+v", v.Issues)
		}
	})

	t.Run("Nomad down leaves restarting unjudged and the rest true", func(t *testing.T) {
		f.e.nomad.SetCopies("whoami", crashing("c1", 5))
		f.e.nomad.Down()
		defer f.e.nomad.Up()
		v := f.read(metrics.Range6h)
		if hasKind(v.Issues, metrics.Restarting) {
			t.Errorf("issues = %+v: restarting judged with Nomad down", v.Issues)
		}
		if !hasKind(v.Issues, metrics.Memory) {
			t.Errorf("issues = %+v: the stored rows still say memory", v.Issues)
		}
		for _, is := range v.Issues {
			if is.Kind == metrics.Memory && (is.Restarts != nil || is.LastExit != nil) {
				t.Errorf("memory = %+v: restarts and last exit are unknown, not 0", is)
			}
		}
	})
}

func hasKind(issues []MetricIssue, k metrics.Kind) bool {
	for _, is := range issues {
		if is.Kind == k {
			return true
		}
	}
	return false
}

// A stopped app isn't judged, a rolling-out app skips the HTTP rules, and the
// three sorts order the table.
func TestMetricsStatesAndSorts(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	f.save(appWith("busy", 1))
	f.save(appWith("calm", 1))
	f.save(appWith("rolling", 1))
	f.save(appWith("stopped", 1))
	f.startedAt(now.Add(-time.Hour))
	last := now.Truncate(time.Minute).Add(-time.Minute)
	row := func(app string, reqs, memUsed int64) store.MetricsSample {
		return mem(f.http(f.series(app, "web"), last, 60, reqs, 0, fastBuckets), memUsed, 1000)
	}
	f.minute(row("busy", 600, 100), row("calm", 60, 400), row("rolling", 6, 950), row("stopped", 6, 990), row("whoami", 0, 50))

	finalDeployment(t, f.e.store, "rolling", "r1", store.StatusRunning, 2)
	if err := f.e.store.SetAppStopped(f.ctx, "stopped", &now); err != nil {
		t.Fatal(err)
	}

	names := func(v MetricsView) []string {
		var out []string
		for _, s := range v.Services {
			out = append(out, s.AppID)
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	v := f.read(metrics.Range1h)
	states := map[string]ServiceState{}
	for _, s := range v.Services {
		states[s.AppID] = s.State
	}
	want := map[string]ServiceState{"busy": ServiceHealthy, "calm": ServiceHealthy, "whoami": ServiceHealthy, "rolling": ServiceUnusual, "stopped": ServiceStopped}
	for k, w := range want {
		if states[k] != w {
			t.Errorf("%s is %s, want %s", k, states[k], w)
		}
	}
	// rolling is above 90 % memory, so it is unusual even while it rolls out;
	// a stopped app at 99 % is not judged at all.
	if got := names(v); !eq(got, []string{"rolling", "busy", "calm", "stopped", "whoami"}) {
		t.Errorf("unusual first = %v", got)
	}
	if len(v.Issues) != 1 || v.Issues[0].AppID != "rolling" || v.Issues[0].Kind != metrics.Memory {
		t.Errorf("issues = %+v", v.Issues)
	}
	for sortBy, want := range map[MetricsSort][]string{
		SortTraffic: {"busy", "calm", "rolling", "stopped", "whoami"},
		SortMemory:  {"stopped", "rolling", "calm", "busy", "whoami"},
	} {
		got, err := f.e.svc.Metrics(f.ctx, MetricsQuery{Range: metrics.Range1h, Sort: sortBy})
		if err != nil {
			t.Fatal(err)
		}
		if !eq(names(got), want) {
			t.Errorf("%s = %v, want %v", sortBy, names(got), want)
		}
	}

	// Rolling out, with a slow window: no HTTP issue (the rules skip it).
	if rolling := findService(v, "rolling"); rolling.Requests != nil && *rolling.Requests <= 0 {
		t.Errorf("rolling requests = %v", *rolling.Requests)
	}
}

func findService(v MetricsView, appID string) ServiceMetrics {
	for _, s := range v.Services {
		if s.AppID == appID {
			return s
		}
	}
	return ServiceMetrics{}
}

// Two services whose Traefik names are the same are attributed to neither and
// each says which other one it is.
func TestMetricsTrafficCollisions(t *testing.T) {
	mk := func(id, svc string) store.AppRecord {
		a := appWith(id, 1)
		a.Services[0].Name = svc
		return store.AppRecord{App: a}
	}
	got := trafficCollisions([]store.AppRecord{mk("a-b", "c"), mk("a", "b-c"), mk("solo", "web")})
	if got[serviceRef{"a-b", "c"}] != "a/b-c" || got[serviceRef{"a", "b-c"}] != "a-b/c" || len(got) != 2 {
		t.Errorf("collisions = %v", got)
	}
}

// The extracted counting rule: unrequested restarts of copies that restarted
// within the hour, plus one per copy that failed within it.
func TestCrashesInHour(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	tests := []struct {
		name   string
		copies []Copy
		want   int
	}{
		{"none", nil, 0},
		{"a copy that restarted 3 times a minute ago", []Copy{{Restarts: 3, LastRestart: ago(time.Minute)}}, 3},
		{"the backups' restarts don't count", []Copy{{Restarts: 4, RequestedRestarts: 3, LastRestart: ago(time.Minute)}}, 1},
		{"restarted long ago: run cleanly since", []Copy{{Restarts: 9, LastRestart: ago(61 * time.Minute)}}, 0},
		{"just under the hour counts", []Copy{{Restarts: 2, LastRestart: ago(time.Hour)}}, 2},
		{"no time known counts for nothing", []Copy{{Restarts: 2}}, 0},
		{"a copy that failed within the hour adds one", []Copy{{Status: deploy.CopyFailed, Terminal: true, FinishedAt: ago(10 * time.Minute)}}, 1},
		{"one that failed long ago isn't considered", []Copy{{Status: deploy.CopyFailed, Terminal: true, FinishedAt: ago(2 * time.Hour), Restarts: 5}}, 0},
		{"a stopped copy never counts", []Copy{{Status: deploy.CopyStopped, Terminal: true, Restarts: 5, LastRestart: ago(time.Minute)}}, 0},
		{"a loop of failed and fresh copies", []Copy{
			{Status: deploy.CopyFailed, Terminal: true, FinishedAt: ago(5 * time.Minute)},
			{Status: deploy.CopyFailed, Terminal: true, FinishedAt: ago(20 * time.Minute)},
			{Status: deploy.CopyHealthy, Restarts: 1, LastRestart: ago(time.Minute)},
		}, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := crashesInHour(now, tc.copies); got != tc.want {
				t.Errorf("crashesInHour = %d, want %d", got, tc.want)
			}
		})
	}
}

// The 24 h and 7 d ranges read the stored 10-minute rows, and the slot still in
// progress (no row of its own yet) comes from its minute rows.
func TestMetricsSlotRanges(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 4, 30, 0, time.UTC) // the 12:00 slot is in progress
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	f.slots(f.http(web, time.Date(2026, 10, 25, 11, 50, 0, 0, time.UTC), 600, 1200, 0, fastBuckets)) // 2 a second
	f.minute(
		f.http(web, time.Date(2026, 10, 25, 12, 2, 0, 0, time.UTC), 60, 300, 0, fastBuckets), // 5 a second
		f.http(web, time.Date(2026, 10, 25, 12, 3, 0, 0, time.UTC), 60, 300, 0, fastBuckets),
	)
	v := f.read(metrics.Range24h)
	if len(v.Points) != 144 {
		t.Fatalf("%d points, want 144", len(v.Points))
	}
	n := len(v.Requests.Points)
	if val(v.Requests.Points[n-1].Value) != 5 || val(v.Requests.Points[n-2].Value) != 2 || v.Requests.Points[n-3].Value != nil {
		t.Errorf("last points = %v %v %v, want a gap, 2, 5", val(v.Requests.Points[n-3].Value), val(v.Requests.Points[n-2].Value), val(v.Requests.Points[n-1].Value))
	}
	if v.Requests.Now == nil || *v.Requests.Now != 5 {
		t.Errorf("now = %v, want the slot in progress", v.Requests.Now)
	}
	v7 := f.read(metrics.Range7d)
	if len(v7.Points) != 168 || val(v7.Requests.Points[167].Value) != 5 || val(v7.Requests.Points[166].Value) != 2 {
		t.Errorf("7d: %d points, last hours %v %v; want 168, 2 then 5", len(v7.Points), val(v7.Requests.Points[166].Value), val(v7.Requests.Points[167].Value))
	}
	if v.Sources.Traefik != SamplerUnknown || v.Sources.Nomad != SamplerUnknown {
		t.Errorf("sources = %+v before any tick, want unknown", v.Sources)
	}
}

// The apps table's Requests, Errors and Slowest 5 % are the last 10 minutes',
// the same numbers the issue and the chart head read, not the range's average.
func TestMetricsTableIsTheLastTenMinutes(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	f.startedAt(now.Add(-4 * 24 * time.Hour))
	last := now.Truncate(time.Minute).Add(-time.Minute)
	// Fifty minutes of busy, fast traffic (2 req/s, no errors), then ten quiet
	// ones with errors and slow answers (0.5 req/s, 20 % 5xx).
	var rows []store.MetricsSample
	for i := range 60 {
		ts := last.Add(-time.Duration(59-i) * time.Minute)
		if i < 50 {
			rows = append(rows, f.http(web, ts, 60, 120, 0, "0,0,0,0,120"))
		} else {
			rows = append(rows, f.http(web, ts, 60, 30, 6, "0,0,0,0,0,0,0,30"))
		}
	}
	f.minute(rows...)

	v := f.read(metrics.Range6h)
	sv := findService(v, "whoami")
	if sv.Requests == nil || *sv.Requests != 0.5 {
		t.Errorf("requests = %v, want 0.5/s (the last 10 minutes), not the range's 1.83", val(sv.Requests))
	}
	if sv.Errors == nil || *sv.Errors != 20 {
		t.Errorf("errors = %v, want 20 %% of the window's requests", val(sv.Errors))
	}
	if sv.P95 == nil || *sv.P95 < 500 {
		t.Errorf("slowest 5%% = %v, want the window's (over 500 ms), not the range's fast mix", val(sv.P95))
	}
	// The same window is the chart head's "now" and the issue's number.
	if w := metrics.WindowOf(rows2(rows, f), now); w.RequestsPerSec != *sv.Requests || w.ErrorPercent != *sv.Errors {
		t.Errorf("window %+v disagrees with the row", w)
	}
}

// rows2 reads stored samples as metrics rows, to compare the table with the
// pure window.
func rows2(in []store.MetricsSample, f *metricsFixture) []metrics.Row {
	layouts, err := f.e.store.MetricsLayouts(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	out := make([]metrics.Row, 0, len(in))
	for _, m := range in {
		out = append(out, samplerRow(m, layouts))
	}
	return out
}

// A row's number is in warn hue only when its issue's rule holds: the issue's
// own value is the row's.
func TestMetricsTableMatchesItsIssue(t *testing.T) {
	real := time.Now().UTC()
	now := real.Truncate(time.Minute).Add(20*time.Minute + 30*time.Second)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	f.startedAt(now.Add(-4 * 24 * time.Hour))
	f.slotsOnEarlierDays(web, 3, now.Add(-time.Hour), now, func(series int64, ts time.Time) store.MetricsSample {
		return f.http(series, ts, 600, 200, 0, fastBuckets)
	})
	slowFrom := now.Truncate(time.Minute).Add(-9 * time.Minute)
	var rows []store.MetricsSample
	for ts := now.Truncate(time.Minute).Add(-20 * time.Minute); ts.Before(now.Truncate(time.Minute)); ts = ts.Add(time.Minute) {
		b := fast20
		if !ts.Before(slowFrom) {
			b = slow20
		}
		rows = append(rows, f.http(web, ts, 60, 20, 0, b))
	}
	f.minute(rows...)
	v := f.read(metrics.Range6h)
	if len(v.Issues) != 1 {
		t.Fatalf("issues = %+v", v.Issues)
	}
	is, sv := v.Issues[0], v.Services[0]
	if sv.P95 == nil || *sv.P95 != is.Value {
		t.Errorf("row slowest 5%% = %v, issue says %v", val(sv.P95), is.Value)
	}
	if sv.Requests == nil || *sv.Requests != is.Window.RequestsPerSec {
		t.Errorf("row requests = %v, issue window %v", val(sv.Requests), is.Window.RequestsPerSec)
	}
}
