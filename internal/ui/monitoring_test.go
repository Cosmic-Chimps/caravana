package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 025, page 8: /monitoring. The words and shapes are built from a service.MetricsView, so
// most of these tests build one by hand and read what the builder says; a few go through the page
// on a real store to prove the routes, the policy and the links.

var monNow = time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)

func fl(v float64) *float64 { return &v }
func i64(v int64) *int64    { return &v }

// monViewOf6h is a place with `n` points of the 6 h range ending at monNow.
func monPoints(rng metrics.Range) []time.Time {
	from := rng.From(monNow)
	out := make([]time.Time, rng.N())
	for i := range out {
		out[i] = from.Add(time.Duration(i) * rng.Step)
	}
	return out
}

// monSeries is a series of n points: every point has the value, and a stretch has a band.
func monSeries(n int, value float64) service.MetricSeries {
	s := service.MetricSeries{Points: make([]service.SeriesPoint, n)}
	for i := range s.Points {
		s.Points[i].Value = fl(value)
	}
	s.Now = fl(value)
	return s
}

// monBuild makes a builder over a view, with the places of one project and one environment.
func monBuild(v service.MetricsView, multiPlace bool) *monBuilder {
	pl := places{Projects: []service.Project{{ID: "default", DisplayName: "Default", Environments: []service.Environment{{Project: "default", Name: "production"}}}}}
	if multiPlace {
		pl = places{Projects: []service.Project{{ID: "shop", DisplayName: "Shop", Environments: []service.Environment{
			{Project: "shop", Name: "production"}, {Project: "shop", Name: "staging"}}}}}
	}
	q := monReq{Project: "default", Environment: "production", Range: v.Range, Sort: service.SortUnusual}
	if multiPlace {
		q.Project = "shop"
	}
	b := &monBuilder{now: monNow, q: q, pl: pl, view: v, multi: map[string]bool{}, kinds: map[serviceRef]map[metrics.Kind]bool{}}
	perApp := map[string]int{}
	for _, sv := range v.Services {
		perApp[sv.AppID]++
	}
	for id, n := range perApp {
		b.multi[id] = n > 1
	}
	return b
}

func TestMonReqOf(t *testing.T) {
	q, bad := monReqOf(url.Values{})
	if bad != "" || q.Project != "default" || q.Environment != "production" || q.Range.Name != "6h" || q.Sort != service.SortUnusual {
		t.Errorf("defaults = %+v %q", q, bad)
	}
	if got := q.href(); got != "/monitoring" {
		t.Errorf("default href = %q", got)
	}
	q, bad = monReqOf(url.Values{"project": {"shop"}, "environment": {"staging"}, "range": {"7d"}, "sort": {"memory"}})
	if bad != "" || q.href() != "/monitoring?environment=staging&project=shop&range=7d&sort=memory" {
		t.Errorf("href = %q %q", q.href(), bad)
	}
	// A link changes one thing and keeps the others.
	if got := q.href(monWithRange(metrics.Range1h)); got != "/monitoring?environment=staging&project=shop&range=1h&sort=memory" {
		t.Errorf("range link = %q", got)
	}
	if got := q.href(monWithSort(service.SortUnusual)); strings.Contains(got, "sort=") {
		t.Errorf("the default sort is left out: %q", got)
	}
	for _, v := range []url.Values{{"range": {"2h"}}, {"sort": {"loudest"}}} {
		if _, bad := monReqOf(v); bad == "" {
			t.Errorf("%v wasn't refused", v)
		}
	}
	for in, want := range map[string]string{"1h": "1 h", "6h": "6 h", "24h": "24 h", "7d": "7 d"} {
		r, _ := metrics.ParseRange(in)
		if got := monRangeLabel(r); got != want {
			t.Errorf("label(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestMonNumberWords(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 0.04: "0", 0.4: "0.4", 4: "4", 9.96: "10", 96.4: "96", 127.5: "128"} {
		if got := rateText(in); got != want {
			t.Errorf("rateText(%v) = %q, want %q", in, got, want)
		}
	}
	if n, u := msText(820); n != "820" || u != "ms" {
		t.Errorf("msText(820) = %s %s", n, u)
	}
	if n, u := msText(1250); n != "1.3" && n != "1.2" || u != "s" {
		t.Errorf("msText(1250) = %s %s", n, u)
	}
	for in, want := range map[int64]string{256: "256 MB", 1024: "1 GB", 1536: "1.5 GB", 3686: "3.6 GB"} {
		if got := sizeText(in); got != want {
			t.Errorf("sizeText(%d) = %q, want %q", in, got, want)
		}
	}
}

// The fixed sentences of the issues panel, kind by kind, with the numbers in weight 500.
func TestMonIssueSentences(t *testing.T) {
	since := monNow.Add(-2 * time.Minute)
	deploy := func(rel metrics.Relation, minutes int) *service.DeployNear {
		return &service.DeployNear{MetricDeploy: service.MetricDeploy{AppName: "supplier-gw", Version: 24}, Relation: rel, Minutes: minutes}
	}
	restarts3 := 3
	tests := []struct {
		name     string
		is       service.MetricIssue
		text     string
		detail   string
		strong   []string
		action   string
		actionTo string
		mark     string
	}{
		{"slower", service.MetricIssue{AppID: "booking-api", AppName: "booking-api", Kind: metrics.Slower, Level: metrics.LevelWarning,
			Value: 820, Usual: fl(180), Since: since, Window: service.IssueWindow{ErrorPercent: 2.4, RequestsPerSec: 96}},
			"booking-api is slower than usual. The slowest 5% of requests take 820 ms, usually 180 ms. It started at 13:58 UTC.",
			"2.4% of requests return 5xx · 96 req/s", []string{"820 ms"}, "Show booking-api errors", "/logs?app=booking-api&levels=error", "warning"},
		{"slower after a deploy", service.MetricIssue{AppID: "booking-api", AppName: "booking-api", Kind: metrics.Slower, Level: metrics.LevelWarning,
			Value: 820, Usual: fl(180), Since: since, DeployNear: deploy(metrics.StartedAfter, 2)},
			"booking-api is slower than usual. The slowest 5% of requests take 820 ms, usually 180 ms. It started at 13:58 UTC. It started 2 minutes after supplier-gw v24 rolled out.",
			"0.0% of requests return 5xx · 0 req/s", []string{"820 ms"}, "Show booking-api errors", "/logs?app=booking-api&levels=error", "warning"},
		{"slower before a deploy", service.MetricIssue{AppID: "booking-api", AppName: "booking-api", Kind: metrics.Slower, Level: metrics.LevelWarning,
			Value: 1500, Since: since, DeployNear: deploy(metrics.StartedBefore, 0)},
			"booking-api is slower than usual. The slowest 5% of requests take 1.5 s. It started at 13:58 UTC. It started before supplier-gw v24 rolled out.",
			"0.0% of requests return 5xx · 0 req/s", []string{"1.5 s"}, "Show booking-api errors", "/logs?app=booking-api&levels=error", "warning"},
		{"more errors", service.MetricIssue{AppID: "booking-api", AppName: "booking-api", Kind: metrics.MoreErrors, Level: metrics.LevelFailed,
			Value: 12.4, Usual: fl(0.3), Since: since, Window: service.IssueWindow{Errors: 41}},
			"booking-api returns more errors than usual: 12.4% of requests, usually 0.3%. It started at 13:58 UTC.",
			"41 errors in the last 10 minutes", []string{"12.4%"}, "Show booking-api errors", "/logs?app=booking-api&levels=error", "failed"},
		{"quieter", service.MetricIssue{AppID: "booking-api", AppName: "booking-api", Kind: metrics.Quieter, Level: metrics.LevelWarning,
			Value: 3, Usual: fl(96), Since: since, Domains: []string{"api.example.com", "b.example.com"}},
			"booking-api has much less traffic than usual: 3 req/s, usually 96. It started at 13:58 UTC.",
			"Its domains: api.example.com, b.example.com", []string{"3 req/s"}, "Show booking-api logs", "/logs?app=booking-api", "warning"},
		{"quieter after a deploy", service.MetricIssue{AppID: "booking-api", AppName: "booking-api", Kind: metrics.Quieter, Level: metrics.LevelWarning,
			Value: 3, Usual: fl(96), Since: since, DeployNear: deploy(metrics.StartedAfter, 2)},
			"booking-api has much less traffic than usual: 3 req/s, usually 96. It started at 13:58 UTC. It started 2 minutes after supplier-gw v24 rolled out.",
			"", []string{"3 req/s"}, "Show booking-api logs", "/logs?app=booking-api", "warning"},
		{"memory", service.MetricIssue{AppID: "metabase", AppName: "metabase", Kind: metrics.Memory, Level: metrics.LevelFailed,
			Value: 98, LimitMB: fl(1024), Restarts: &restarts3, LastExit: &service.LastExit{Copy: "e4d1aaaa-0000", ExitCode: 137, At: monNow.Add(-4 * time.Minute)}},
			"metabase is using 98% of its 1 GB memory limit. It restarted 3 times in the last hour.",
			"copy e4d1aaaa · exit 137 last at 13:56 UTC", []string{"98%"}, "Show metabase logs", "/logs?app=metabase", "failed"},
		{"restarting", service.MetricIssue{AppID: "metabase", AppName: "metabase", Kind: metrics.Restarting, Level: metrics.LevelWarning, Value: 2},
			"metabase restarted 2 times in the last hour.", "", []string{}, "Show metabase logs", "/logs?app=metabase", "warning"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := monBuild(service.MetricsView{Range: metrics.Range6h, At: monNow}, false)
			got := b.issue(tc.is)
			if got.Text.String() != tc.text {
				t.Errorf("text:\n got %q\nwant %q", got.Text.String(), tc.text)
			}
			if got.Detail.String() != tc.detail {
				t.Errorf("detail = %q, want %q", got.Detail.String(), tc.detail)
			}
			var strong []string
			for _, p := range got.Text {
				if p.Strong {
					strong = append(strong, p.Text)
				}
			}
			if strings.Join(strong, "|") != strings.Join(tc.strong, "|") {
				t.Errorf("numbers in weight = %v, want %v", strong, tc.strong)
			}
			if got.Action.String() != tc.action || got.ActionHref != tc.actionTo || got.Mark != tc.mark {
				t.Errorf("action %q to %q mark %q, want %q to %q mark %q", got.Action.String(), got.ActionHref, got.Mark, tc.action, tc.actionTo, tc.mark)
			}
		})
	}

	// An app with several services is named "app/service"; a place that isn't home is in the action's link.
	b := monBuild(service.MetricsView{Range: metrics.Range6h, At: monNow, Services: []service.ServiceMetrics{{AppID: "hollo", Service: "web"}, {AppID: "hollo", Service: "db"}}}, true)
	got := b.issue(service.MetricIssue{AppID: "hollo", AppName: "hollo", Service: "web", Kind: metrics.Restarting, Value: 2})
	if got.Text.String() != "hollo/web restarted 2 times in the last hour." || got.ActionHref != "/logs?app=hollo&project=shop" {
		t.Errorf("multi-service issue = %q to %q", got.Text.String(), got.ActionHref)
	}
	// A restart or a deploy older than the 6 hours the run is walked back along is "more than 6 hours".
	capped := monBuild(service.MetricsView{Range: metrics.Range6h, At: monNow}, false).issue(service.MetricIssue{AppID: "a", AppName: "a", Kind: metrics.Slower, Value: 500, SinceCapped: true})
	if !strings.Contains(capped.Text.String(), "It started more than 6 hours ago.") {
		t.Errorf("capped = %q", capped.Text.String())
	}
}

func TestMonCalmLine(t *testing.T) {
	svc := []service.ServiceMetrics{{AppID: "a", Service: "web"}}
	since := monNow.Add(-3 * time.Hour)
	for _, tc := range []struct {
		name  string
		view  service.MetricsView
		multi bool
		want  string
		mark  string
	}{
		{"one place", service.MetricsView{Services: svc, Baseline: service.MetricsBaseline{State: metrics.Partial}}, false, "Every app is within its usual range.", "healthy"},
		{"several places", service.MetricsView{Services: svc, Baseline: service.MetricsBaseline{State: metrics.Full}}, true, "Everything in Shop / production is within its usual range.", "healthy"},
		{"building", service.MetricsView{Services: svc, Baseline: service.MetricsBaseline{State: metrics.Building, Since: &since}}, false, "The usual range is still being learned: started 2026-10-25 UTC.", "idle"},
		{"building, nothing sampled", service.MetricsView{Services: svc, Baseline: service.MetricsBaseline{State: metrics.Building}}, false, "The usual range is still being learned: nothing has been sampled yet.", "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.view.Range, tc.view.At = metrics.Range6h, monNow
			calm := monBuild(tc.view, tc.multi).calm()
			if calm == nil || calm.Text.String() != tc.want || calm.Mark != tc.mark {
				t.Errorf("calm = %+v, want %q (%s)", calm, tc.want, tc.mark)
			}
		})
	}
	if monBuild(service.MetricsView{Range: metrics.Range6h, At: monNow}, false).calm() != nil {
		t.Error("with no service there is nothing to call calm")
	}

	// A place in which nothing serves says so (○) instead of calling its numbers usual.
	down := func(app, word string) service.ServiceMetrics {
		return service.ServiceMetrics{AppID: app, AppName: app, Service: "web", Standing: service.ServiceStanding{Mark: "idle", Word: word, Down: true}}
	}
	for _, tc := range []struct {
		name  string
		svcs  []service.ServiceMetrics
		multi bool
		want  string
	}{
		{"stopped, several places", []service.ServiceMetrics{down("web", "stopped")}, true, "Nothing is running in Shop / production: web is stopped."},
		{"stopped, one place", []service.ServiceMetrics{down("web", "stopped")}, false, "Nothing is running: web is stopped."},
		{"two, not the same way", []service.ServiceMetrics{down("a", "stopped"), down("b", "not running")}, false, "Nothing is running: a is stopped, b is not running."},
		{"five", []service.ServiceMetrics{down("a", "stopped"), down("b", "stopped"), down("c", "never deployed"), down("d", "stopped"), down("e", "stopped")}, false,
			"Nothing is running: a is stopped, b is stopped, c was never deployed and 2 more."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calm := monBuild(service.MetricsView{Range: metrics.Range6h, At: monNow, Services: tc.svcs, Baseline: service.MetricsBaseline{State: metrics.Full}}, tc.multi).calm()
			if calm == nil || calm.Text.String() != tc.want || calm.Mark != "idle" {
				t.Errorf("calm = %+v, want %q (idle)", calm, tc.want)
			}
		})
	}
	// One that serves keeps the usual line.
	mixed := []service.ServiceMetrics{down("a", "stopped"), {AppID: "b", AppName: "b", Service: "web"}}
	if calm := monBuild(service.MetricsView{Range: metrics.Range6h, At: monNow, Services: mixed, Baseline: service.MetricsBaseline{State: metrics.Full}}, false).calm(); calm == nil || calm.Mark != "healthy" {
		t.Errorf("a place with a service serving: calm = %+v", calm)
	}
}

// The chart's SVG: gridlines, the band between the earlier days' lowest and highest, the warn
// rect, gaps that are never zero, deploy markers with their labels, a polyline with its title and a
// hidden table with every number.
func TestMonChartShapes(t *testing.T) {
	rng := metrics.Range6h
	v := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), HasHTTP: true,
		Baseline: service.MetricsBaseline{State: metrics.Partial, FullDays: 3},
		Deploys: []service.MetricDeploy{
			{AppName: "supplier-gw", Version: 24, EndedAt: monNow.Add(-30 * time.Minute)},
		}}
	req := monSeries(72, 100)
	for i := 10; i <= 40; i++ {
		req.Points[i].Low, req.Points[i].High, req.Points[i].Usual, req.Points[i].Days = fl(80), fl(120), fl(100), 3
	}
	for i := 50; i < 55; i++ { // five readings missing in a row
		req.Points[i].Value = nil
	}
	req.Usual, req.UsualDays = fl(100), 3
	v.Requests = req
	slow := monSeries(72, 180)
	since := monNow.Add(-20 * time.Minute)
	slow.UnusualSince = &since
	v.P95 = slow
	v.Errors = monSeries(72, 0.3)
	v.Memory = monSeries(72, 3600)
	v.MemoryLimit = make([]*float64, 72)
	for i := range v.MemoryLimit {
		v.MemoryLimit[i] = fl(5530)
	}

	charts := monBuild(v, false).charts()
	if len(charts) != 4 {
		t.Fatalf("%d charts", len(charts))
	}
	c := charts[0]
	if c.Title != "Requests" || c.Plot == nil || c.Value != "100" || c.Unit != "req/s" || c.Usually != "usually 100" || c.Warn {
		t.Errorf("requests head = %+v", c)
	}
	p := c.Plot
	if len(p.Bands) != 1 || !strings.Contains(p.Bands[0].Title, "the same hour on the 3 days there are: 80 to 120 req/s") {
		t.Errorf("band = %+v", p.Bands)
	}
	// The polygon has a vertex on top and one under for each point of the stretch: 31 and 31.
	if got := len(strings.Fields(p.Bands[0].Points)); got != 62 {
		t.Errorf("band polygon has %d vertices, want 62", got)
	}
	if len(p.Gaps) != 1 || !strings.Contains(p.Gaps[0].Title, "No readings from") {
		t.Errorf("gaps = %+v", p.Gaps)
	}
	if len(p.Lines) != 1 || len(p.Lines[0].Segments) != 2 {
		t.Fatalf("lines = %+v: the series should break at the gap", p.Lines)
	}
	if want := "min 100, max 100, latest 100 req/s"; p.Lines[0].Title != want {
		t.Errorf("line title = %q, want %q", p.Lines[0].Title, want)
	}
	if len(p.Deploys) != 1 || !strings.Contains(p.Deploys[0].Title, "supplier-gw v24 rolled out at 13:30 UTC") {
		t.Errorf("deploys = %+v", p.Deploys)
	}
	if len(p.Labels) != 1 || p.Labels[0] != "supplier-gw v24" {
		t.Errorf("labels = %v", p.Labels)
	}
	if len(p.Table.Rows) != 72 || p.Table.Rows[50].Value != "no reading" {
		t.Errorf("hidden table: %d rows, row 50 = %+v", len(p.Table.Rows), p.Table.Rows[50])
	}
	if cells := strings.Split(p.Values, ","); len(cells) != 72 || cells[50] != "" || cells[0] != "100" {
		t.Errorf("hover values = %d cells, %q %q", len(cells), cells[0], cells[50])
	}
	if p.T0 != v.Points[0].Format(time.RFC3339) || p.Step != 300 || p.Date {
		t.Errorf("hover data = %s %d %v", p.T0, p.Step, p.Date)
	}
	if c.AxisStart != "08:05 UTC" || c.AxisMid != axisTime(v.Points[36], false) {
		t.Errorf("axis = %q %q", c.AxisStart, c.AxisMid)
	}
	if len(c.Captions) != 0 {
		t.Errorf("captions on a partial baseline = %+v", c.Captions)
	}

	// Slowest 5%: the warn rect from when the rule began, and the head in the warn hue.
	s := charts[2]
	if !s.Warn || s.Plot.Warn == nil || s.Value != "180" || s.Unit != "ms" {
		t.Errorf("slowest 5%% = %+v warn %+v", s, s.Plot.Warn)
	}
	// 20 minutes of a 6 h range that ends at point 71: x = 300 - 20/355*300 ≈ 283.
	if x := s.Plot.Warn.X; !strings.HasPrefix(x, "283") && !strings.HasPrefix(x, "282") {
		t.Errorf("warn rect starts at x=%s", x)
	}
	// Errors have no warn rect, Memory is against its limit in GB.
	if charts[1].Plot.Warn != nil {
		t.Error("errors chart has a warn rect")
	}
	m := charts[3]
	if m.Value != "3.5" || m.Unit != "GB of 5.4 limit" || m.Usually != "" {
		t.Errorf("memory head = %q %q %q", m.Value, m.Unit, m.Usually)
	}

	// An hour of a 7 d range gets a weekday on its axis and the band's thin caption.
	rng = metrics.Range7d
	v.Range, v.Points = rng, monPoints(rng)
	v.Baseline = service.MetricsBaseline{State: metrics.Full}
	week := monSeries(168, 10)
	v.Requests, v.Errors, v.P95, v.Memory = week, week, week, week
	c = monBuild(v, false).charts()[0]
	if !c.Plot.Date || !strings.Contains(c.AxisStart, "UTC") || len(c.Captions) != 1 ||
		c.Captions[0].Text.String() != "The band thins to the left: older hours have fewer earlier days kept." {
		t.Errorf("7 d chart = date %v axis %q captions %+v", c.Plot.Date, c.AxisStart, c.Captions)
	}
}

func TestMonChartCrowdedDeploysAndBuilding(t *testing.T) {
	rng := metrics.Range6h
	v := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), HasHTTP: true,
		Baseline: service.MetricsBaseline{State: metrics.Building, Since: new(monNow.Add(-5 * time.Hour))}}
	for i := 0; i < 4; i++ {
		v.Deploys = append(v.Deploys, service.MetricDeploy{AppName: "app", Version: uint64(i + 1), EndedAt: monNow.Add(-time.Duration(60-i) * time.Minute)})
	}
	v.Requests, v.Errors, v.P95, v.Memory = monSeries(72, 5), monSeries(72, 0), monSeries(72, 90), monSeries(72, 300)
	c := monBuild(v, false).charts()[0]
	if got := strings.Join(c.Plot.Labels, " | "); got != "app v4 | and 3 more" {
		t.Errorf("crowded labels = %q", got)
	}
	if len(c.Plot.Deploys) != 4 {
		t.Errorf("a marker for each deployment: %d", len(c.Plot.Deploys))
	}
	if len(c.Captions) != 1 || c.Captions[0].Text.String() != "Building the baseline: started 2026-10-25 UTC. The usual range appears after a full day." {
		t.Errorf("captions = %+v", c.Captions)
	}
	if c.Usually != "" || len(c.Plot.Bands) != 0 {
		t.Errorf("no band or usually while building: %q %v", c.Usually, c.Plot.Bands)
	}
}

// A chart with nothing to draw says why in one sentence, and Traefik's three states are worded.
func TestMonEmptyCharts(t *testing.T) {
	rng := metrics.Range6h
	empty := service.MetricSeries{Points: make([]service.SeriesPoint, 72)}
	base := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), Requests: empty, Errors: empty, P95: empty, Memory: empty}

	tests := []struct {
		name  string
		view  service.MetricsView
		multi bool
		http  string
		mark  string
	}{
		{"no domain, one place", base, false, "No HTTP traffic: no app has a domain.", ""},
		{"no domain, several places", base, true, "No HTTP traffic: no app in Shop / production has a domain.", ""},
		{"traefik off", with(base, func(v *service.MetricsView) { v.HasHTTP, v.Sources.Traefik = true, service.SamplerOff }), false,
			"Traefik isn't serving its request numbers on this host. Run the installer again to turn them on.", "idle"},
		{"traefik unreachable", with(base, func(v *service.MetricsView) { v.HasHTTP, v.Sources.Traefik = true, service.SamplerUnreachable }), false,
			"Can't read Traefik's numbers right now.", "idle"},
		{"traefik disabled", with(base, func(v *service.MetricsView) { v.HasHTTP, v.Sources.Traefik = true, service.SamplerDisabled }), false,
			"Request numbers are turned off: Caravana has no address for Traefik's metrics.", "idle"},
		{"nothing yet", with(base, func(v *service.MetricsView) { v.HasHTTP, v.Sources.Traefik = true, service.SamplerOK }), false,
			"Nothing has been sampled in this range yet.", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			charts := monBuild(tc.view, tc.multi).charts()
			for _, c := range charts[:3] {
				if c.Plot != nil || c.Empty == nil || c.Empty.Text.String() != tc.http || c.Empty.Mark != tc.mark || c.Value != "—" {
					t.Errorf("%s = plot %v empty %+v value %q, want %q (%s)", c.Title, c.Plot != nil, c.Empty, c.Value, tc.http, tc.mark)
				}
			}
			if m := charts[3]; m.Plot != nil || m.Empty == nil || m.Empty.Text.String() != "Nothing has been sampled in this range yet." {
				t.Errorf("memory = %+v", m.Empty)
			}
		})
	}

	// Traefik down with history still drawn: the plot stays, and a caption says what is wrong.
	v := with(base, func(v *service.MetricsView) {
		v.HasHTTP, v.Sources.Traefik = true, service.SamplerUnreachable
		v.Requests = monSeries(72, 4)
	})
	c := monBuild(v, false).charts()[0]
	if c.Plot == nil || len(c.Captions) == 0 || c.Captions[len(c.Captions)-1].Text.String() != "Can't read Traefik's numbers right now." {
		t.Errorf("history with Traefik down = plot %v captions %+v", c.Plot != nil, c.Captions)
	}
}

func with(v service.MetricsView, f func(*service.MetricsView)) service.MetricsView {
	f(&v)
	return v
}

var (
	rollingStanding = service.ServiceStanding{Mark: "rolling", Word: "rolling out"}
	stoppedStanding = service.ServiceStanding{Mark: "idle", Word: "stopped", Down: true}
)

// The apps table's rows: marks, the words under a name, dashes, warn values, the sparkline's title,
// the memory meter and the logs link.
func TestMonTableRows(t *testing.T) {
	rng := metrics.Range6h
	cpu := make([]*float64, 72)
	for i := range cpu {
		cpu[i] = fl(40)
	}
	cpu[10], cpu[11] = nil, nil
	v := service.MetricsView{Range: rng, At: monNow, Services: []service.ServiceMetrics{
		{AppID: "booking-api", AppName: "booking-api", Service: "web", State: service.ServiceUnusual, Level: metrics.LevelWarning, Copies: 2, HasDomain: true,
			Requests: fl(96), Errors: fl(2.4), P95: fl(820), UsualP95: fl(180), CPU: cpu, CPUReservedMHz: i64(100),
			MemoryMB: i64(700), MemoryLimitMB: i64(1024), Restarts: i64(0)},
		{AppID: "worker", AppName: "worker", Service: "web", State: service.ServiceRollingOut, Copies: 1, Standing: rollingStanding,
			CPU: cpu, MemoryMB: i64(930), MemoryLimitMB: i64(1000), Restarts: i64(2)},
		{AppID: "old", AppName: "old", Service: "web", State: service.ServiceStopped, Standing: stoppedStanding, HasDomain: true, Requests: fl(0.4), Errors: fl(0), P95: fl(11)},
		{AppID: "hollo", AppName: "hollo", Service: "web", State: service.ServiceHealthy, Copies: 1, HasDomain: true, Requests: fl(8), Errors: fl(0), P95: fl(290)},
		{AppID: "hollo", AppName: "hollo", Service: "db", State: service.ServiceHealthy, Copies: 1},
		{AppID: "files", AppName: "files", Service: "site", State: service.ServiceHealthy, Standing: service.ServiceStanding{Mark: "failed", Word: "not running", Down: true}, HasDomain: true},
		{AppID: "feed", AppName: "feed", Service: "web", State: service.ServiceUnusual, Level: metrics.LevelWarning, Standing: service.ServiceStanding{Mark: "failed", Word: "deploy failed"}, Copies: 1},
		{AppID: "quiet", AppName: "quiet", Service: "web", State: service.ServiceUnusual, Level: metrics.LevelWarning, Standing: service.ServiceStanding{Mark: "healthy", Word: "healthy"}, Copies: 1, HasDomain: true},
	}, Issues: []service.MetricIssue{
		{AppID: "booking-api", Service: "web", Kind: metrics.Slower}, {AppID: "booking-api", Service: "web", Kind: metrics.MoreErrors},
		{AppID: "worker", Service: "web", Kind: metrics.Memory},
	}}
	b := monBuild(v, false)
	b.coreMHz = 100
	for _, is := range v.Issues {
		ref := serviceRef{is.AppID, is.Service}
		if b.kinds[ref] == nil {
			b.kinds[ref] = map[metrics.Kind]bool{}
		}
		b.kinds[ref][is.Kind] = true
	}
	tbl := b.table()
	if tbl.Meta.String() != "7 apps" {
		t.Errorf("meta = %q", tbl.Meta.String())
	}
	if len(tbl.Sorts) != 3 || tbl.Sorts[0].Label != "unusual first" || !tbl.Sorts[0].On || tbl.Sorts[1].Href != "/monitoring?sort=traffic" || tbl.Sorts[2].Label != "most memory" {
		t.Errorf("sorts = %+v", tbl.Sorts)
	}
	rows := map[string]monRow{}
	for _, r := range tbl.Rows {
		rows[r.Name] = r
	}
	api := rows["booking-api"]
	if api.Mark != "warning" || api.Copies != "2 copies" || api.Requests.Text != "96/s" || api.Errors.Text != "2.4%" || !api.Errors.Warn ||
		api.P95.Text != "820 ms" || !api.P95.Warn || api.P95.Usual != "usually 180" || api.Requests.Warn {
		t.Errorf("booking-api = %+v", api)
	}
	if api.Spark == nil || api.Spark.Title != "0.4 of 1 core reserved, now" || len(api.Spark.Segments) != 2 {
		t.Errorf("sparkline = %+v", api.Spark)
	}
	if api.Mem.Read != "0.7 / 1.0 GB" || api.Mem.Warn || api.Restarts.Text != "0" || api.Restarts.Warn {
		t.Errorf("memory %+v restarts %+v", api.Mem, api.Restarts)
	}
	if api.Href != "/apps/booking-api" || api.LogsHref != "/logs?service=booking-api%2Fweb" || api.LogsName != "Logs of booking-api" {
		t.Errorf("links = %q %q %q", api.Href, api.LogsHref, api.LogsName)
	}
	w := rows["worker"]
	if w.Mark != "rolling" || w.Copies != "1 copy · no domain" || !w.Requests.None || w.Requests.Text != "—" || w.P95.Usual != "" ||
		!w.Mem.Warn || w.Mem.Read != "930 / 1000 MB" || !w.Restarts.Warn || w.Restarts.Text != "2" {
		t.Errorf("worker = %+v", w)
	}
	if old := rows["old"]; old.Mark != "idle" || old.Copies != "stopped" || old.MarkWord != "stopped" || old.Requests.Text != "0.4/s" {
		t.Errorf("stopped = %+v", old)
	}
	// The mark is the app's status first: not running and deploy failed are ■ (a service that is down
	// is never ◆ "unusual"), a rolling one ◐, and ◆ is only for a serving service whose numbers are
	// unusual, as the Apps home has them.
	for name, want := range map[string][2]string{
		"files": {"failed", "not running"}, "feed": {"failed", "deploy failed"}, "worker": {"rolling", "rolling out"},
		"old": {"idle", "stopped"}, "quiet": {"warning", "unusual"}, "booking-api": {"warning", "unusual"}, "hollo / web": {"healthy", "healthy"},
	} {
		if r := rows[name]; r.Mark != want[0] || r.MarkWord != want[1] {
			t.Errorf("%s mark = %s %q, want %s %q", name, r.Mark, r.MarkWord, want[0], want[1])
		}
	}
	// An app with several services has a row for each, named "app / service".
	if _, ok := rows["hollo / web"]; !ok {
		t.Errorf("rows = %v", rows)
	}
	if db := rows["hollo / db"]; db.Copies != "1 copy · no domain" || db.Spark != nil || db.Mem.Known || db.Restarts.Text != "—" || !db.Restarts.None {
		t.Errorf("db = %+v", db)
	}
}

// ---- the page --------------------------------------------------------------------------

const (
	monNoStyle  = `\sstyle\s*=`
	monNoScript = `<script(\s[^>]*)?>[^<]`
)

// monEnv has whoami, a stored app with a domain, and two hours of samples written the way the
// sampler writes them: 600 requests a minute, 1 000 MB used of 1 024.
func monEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: "console", Status: store.StatusHealthy, Version: 3})
	ctx := context.Background()
	layout, err := e.Store.EnsureLayout(ctx, metrics.TraefikBounds)
	if err != nil {
		t.Fatal(err)
	}
	series, err := e.Store.EnsureSeries(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	var rows []store.MetricsSample
	secs, reqs, errs := int64(60), int64(600), int64(0)
	buckets := "0,0,0,0,100"
	for i := 90; i >= 1; i-- {
		rows = append(rows, store.MetricsSample{Series: series, TS: now.Add(-time.Duration(i) * time.Minute), Minutes: 1, HTTPSecs: &secs, Requests: &reqs,
			Errors: &errs, Layout: &layout, Buckets: &buckets, MemoryMB: i64(1000), MemoryLimitMB: i64(1024), CPUMHz: i64(40), CPUReservedMHz: i64(100), Copies: i64(1)})
	}
	if err := e.Store.WriteMetricsTick(ctx, store.MetricsTick{At: now.Add(-3 * time.Hour), Minute: store.MetricsRows{Samples: rows}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestMonitoringPage(t *testing.T) {
	e := monEnv(t)
	resp, body := e.get(t, "/monitoring")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /monitoring = %d:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)
	for _, want := range []string{
		"Monitoring", "How your apps are doing compared with their usual week. The shaded band is the same hour over the last 7 days.",
		"Requests", "Errors", "Slowest 5%", "Memory", "Apps 1 app",
		"Sampled every minute and kept for 8 days. Requests come from Traefik, memory and CPU from Nomad.",
		"Long-lived connections, such as websockets and streams, count as requests but not in Slowest 5%.",
		"whoami is using 98% of its 1 GB memory limit.", "Show whoami logs",
		"Building the baseline: started",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	for _, want := range []string{
		`<a href="/monitoring?range=1h" class="cv-seg-item"`, `<a href="/monitoring" class="cv-seg-item cv-on" aria-current="true">6 h</a>`,
		`<a href="/monitoring?sort=traffic" class="cv-seg-item">most traffic</a>`,
		`hx-get="/parts/monitoring" hx-trigger="sse:metrics"`, `id="mon-live"`, `data-live`, `sse-connect="/live?watch=monitoring"`,
		`id="mon-chart-requests"`, `id="mon-chart-errors"`, `id="mon-chart-p95"`, `id="mon-chart-memory"`,
		`<svg class="cv-mnsvg" viewBox="0 0 300 120" preserveAspectRatio="none"`, `class="cv-mnline"`, `class="cv-mngrid"`,
		`<a href="/apps/whoami" class="cv-mnname-text">whoami</a>`, `aria-label="Logs of whoami"`, `<table class="sr-only">`,
		`<script src="/static/js/monitoring.js?v=`, `data-mon-plot`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// The place bars have one choice each, so there are none; the range bar has four.
	if strings.Contains(body, `aria-label="Project"`) || strings.Contains(body, `aria-label="Environment"`) {
		t.Error("place bars with a single place")
	}
	for _, label := range []string{">1 h<", ">6 h<", ">24 h<", ">7 d<"} {
		if !strings.Contains(body, label) {
			t.Errorf("range bar lacks %q", label)
		}
	}
	// The sidebar's item, after Logs, marked as current.
	if !strings.Contains(body, `<a href="/monitoring" class="cv-nav-item" aria-current="page">`) {
		t.Error("the sidebar doesn't mark Monitoring")
	}
	// The policy forbids both: no style attribute, and no inline script.
	if regexp.MustCompile(monNoStyle).MatchString(body) {
		t.Errorf("the page has a style attribute:\n%s", regexp.MustCompile(monNoStyle).FindString(body))
	}
	if regexp.MustCompile(monNoScript).MatchString(body) {
		t.Errorf("the page has an inline script:\n%s", regexp.MustCompile(monNoScript).FindString(body))
	}
	// No text is fg-4: none of the page's classes asks for it.
	if strings.Contains(body, "text-fg-4") {
		t.Error("the page has text in fg-4")
	}
}

func TestMonitoringPartAndRanges(t *testing.T) {
	e := monEnv(t)
	for _, rng := range []string{"1h", "6h", "24h", "7d"} {
		resp, body := e.get(t, "/parts/monitoring?range="+rng)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("part %s = %d", rng, resp.StatusCode)
		}
		if strings.Contains(body, "<html") || strings.Contains(body, `class="cv-shell"`) {
			t.Errorf("part %s draws a whole page", rng)
		}
		if !strings.Contains(body, `id="mon-chart-requests"`) || !strings.Contains(body, `cv-mntable"`) {
			t.Errorf("part %s lacks the charts or the table:\n%s", rng, text(body))
		}
		if regexp.MustCompile(monNoStyle).MatchString(body) {
			t.Errorf("part %s has a style attribute", rng)
		}
		page, pageBody := e.get(t, "/monitoring?range="+rng)
		if page.StatusCode != 200 || !strings.Contains(pageBody, `hx-get="/parts/monitoring`+map[bool]string{true: "", false: "?range=" + rng}[rng == "6h"]+`"`) {
			t.Errorf("the page for %s doesn't refresh its own range:\n%s", rng, pageBody[:min(len(pageBody), 600)])
		}
	}
	// 24 h and 7 d are dated: their axis names the weekday.
	if _, body := e.get(t, "/parts/monitoring?range=7d"); !regexp.MustCompile(`<span>(Mon|Tue|Wed|Thu|Fri|Sat|Sun) \d\d:\d\d UTC</span>`).MatchString(body) {
		t.Error("the 7 d axis has no weekday")
	}
	// A bad value is refused, not guessed at; an unknown place is 404.
	for path, status := range map[string]int{
		"/monitoring?range=2h": 400, "/monitoring?sort=loudest": 400, "/monitoring?project=nope": 404, "/monitoring?environment=nope": 404,
		"/parts/monitoring?range=2h": 400, "/parts/monitoring?project=nope": 404,
	} {
		if resp, _ := e.get(t, path); resp.StatusCode != status {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, status)
		}
	}
	// Signed out: the page goes to sign-in, the part says so.
	if resp, _ := e.do(t, request{path: "/monitoring"}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("signed out = %d", resp.StatusCode)
	}
}

// The place bars appear with more than one choice, and every link keeps the other filters.
func TestMonitoringPlaceBarsAndLinks(t *testing.T) {
	e := monEnv(t)
	ctx := context.Background()
	if _, err := e.Svc.CreateEnvironment(ctx, "default", "staging"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	_, body := e.get(t, "/monitoring?range=24h&sort=traffic")
	for _, want := range []string{
		`aria-label="Project"`, `aria-label="Environment"`,
		`<a href="/monitoring?range=24h&amp;sort=traffic" class="cv-seg-item cv-on" aria-current="true">production</a>`,
		`<a href="/monitoring?environment=staging&amp;range=24h&amp;sort=traffic" class="cv-seg-item">staging</a>`,
		`<a href="/monitoring?project=shop&amp;range=24h&amp;sort=traffic"`, // the other project, its first environment
		`<a href="/monitoring?range=1h&amp;sort=traffic" class="cv-seg-item">1 h</a>`,
		`<a href="/monitoring?range=24h" class="cv-seg-item">unusual first</a>`,
		`<a href="/monitoring?range=24h&amp;sort=memory" class="cv-seg-item">most memory</a>`,
		`hx-get="/parts/monitoring?range=24h&amp;sort=traffic"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// With several places the sentences name where: the meta line and the empty chart.
	_, other := e.get(t, "/monitoring?environment=staging")
	if got := text(other); !strings.Contains(got, "0 in Default / staging") || !strings.Contains(got, "No HTTP traffic: no app in Default / staging has a domain.") {
		t.Errorf("staging page:\n%s", got)
	}
}

func TestMonitoringScriptAsset(t *testing.T) {
	e := monEnv(t)
	resp, js := e.get(t, "/static/js/monitoring.js")
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("monitoring.js = %d as %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{`"use strict";`, "data-mon-plot", "data-mon-cross", "UTC · ", " local", "textContent", "setAttribute", "pointermove", "pointerout"} {
		if !strings.Contains(js, want) {
			t.Errorf("monitoring.js lacks %q", want)
		}
	}
	for _, not := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "eval(", "document.write", ".style", "fetch(", "XMLHttpRequest"} {
		if strings.Contains(js, not) {
			t.Errorf("monitoring.js has %q", not)
		}
	}
}

// The page's stream is the workspace stream with the metrics notice: it opens, says sync, and
// stays quiet until the sampler writes.
func TestMonitoringLiveRoute(t *testing.T) {
	e := monEnv(t)
	ls := openStream(t, e.URL, "/live?watch=monitoring", e.Cookie.Value)
	if ls.resp.StatusCode != http.StatusOK || ls.resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %q", ls.resp.StatusCode, ls.resp.Header.Get("Content-Type"))
	}
	ls.expect("sync")
	// A signed-out stream is told it ended.
	gone := openStream(t, e.URL, "/live?watch=monitoring", "")
	gone.expect("ended")
}
