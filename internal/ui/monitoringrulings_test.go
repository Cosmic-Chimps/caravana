package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The HTTP charts mark only the deploys of apps with a domain (an app with no traffic says nothing
// about requests); the Memory chart marks them all. Deploys within 5 minutes of each other share one
// marker, labelled "10 deploys", whose title lists them.
func TestMonDeployMarkers(t *testing.T) {
	rng := metrics.Range6h
	v := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), HasHTTP: true,
		Baseline: service.MetricsBaseline{State: metrics.Full}}
	v.Deploys = append(v.Deploys,
		service.MetricDeploy{AppName: "shop", Version: 3, EndedAt: monNow.Add(-90 * time.Minute), HasDomain: true},
		service.MetricDeploy{AppName: "worker", Version: 7, EndedAt: monNow.Add(-60 * time.Minute)}, // no domain
	)
	for i := 0; i < 10; i++ { // ten deploys of a no-domain app, a minute apart
		v.Deploys = append(v.Deploys, service.MetricDeploy{AppName: "batch", Version: uint64(i + 1), EndedAt: monNow.Add(-time.Duration(40-i) * time.Minute)})
	}
	v.Requests, v.Errors, v.P95, v.Memory = monSeries(72, 5), monSeries(72, 0), monSeries(72, 90), monSeries(72, 300)
	charts := monBuild(v, false).charts()
	http, memory := charts[0].Plot, charts[3].Plot
	if len(http.Deploys) != 1 || !strings.Contains(http.Deploys[0].Title, "shop v3 rolled out at") || strings.Join(http.Labels, "|") != "shop v3" {
		t.Errorf("the HTTP chart marks %+v labels %v, want only shop v3", http.Deploys, http.Labels)
	}
	if len(memory.Deploys) != 3 || strings.Join(memory.Labels, "|") != "10 deploys|and 2 more" {
		t.Fatalf("the Memory chart marks %d, labels %v, want shop, worker and one marker of 10 deploys", len(memory.Deploys), memory.Labels)
	}
	crowd := memory.Deploys[2].Title
	if !strings.HasPrefix(crowd, "10 deploys rolled out:\n") || strings.Count(crowd, "\n") != 10 || !strings.Contains(crowd, "batch v1 at ") || !strings.Contains(crowd, "batch v10 at ") {
		t.Errorf("the crowd's title = %q", crowd)
	}
	// Two deploys, each its own marker, are labelled one by one.
	if got := deployLabels(groupDeploys(v.Deploys[:2])); strings.Join(got, "|") != "shop v3|worker v7" {
		t.Errorf("two markers = %v", got)
	}
}

func TestGroupDeploys(t *testing.T) {
	at := func(min int) service.MetricDeploy {
		return service.MetricDeploy{AppName: "a", EndedAt: monNow.Add(time.Duration(min) * time.Minute)}
	}
	got := groupDeploys([]service.MetricDeploy{at(0), at(3), at(5), at(11), at(30), at(33)})
	if len(got) != 3 || len(got[0]) != 3 || len(got[1]) != 1 || len(got[2]) != 2 {
		t.Errorf("groups = %v: within 5 minutes of the deploy before is one marker", got)
	}
	if groupDeploys(nil) != nil {
		t.Error("no deploys, no markers")
	}
}

// The hover's "usually" follows the hovered point: each point's own words are in the plot's data, and
// the head has a slot for them.
func TestMonHoverUsuals(t *testing.T) {
	rng := metrics.Range6h
	v := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), HasHTTP: true, Baseline: service.MetricsBaseline{State: metrics.Full}}
	req := monSeries(72, 100)
	for i := 10; i <= 12; i++ {
		req.Points[i].Low, req.Points[i].High, req.Points[i].Usual, req.Points[i].Days = fl(80), fl(120), fl(100+float64(i)), 3
	}
	req.Usual = fl(111)
	v.Requests, v.Errors, v.P95, v.Memory = req, monSeries(72, 0.3), monSeries(72, 90), monSeries(72, 300)
	charts := monBuild(v, false).charts()
	cells := strings.Split(charts[0].Plot.Usuals, ",")
	if len(cells) != 72 || cells[10] != "usually 110.0" || cells[12] != "usually 112.0" || cells[0] != "" || cells[13] != "" {
		t.Errorf("per-point usuals = %d cells, %q %q %q", len(cells), cells[0], cells[10], cells[12])
	}
	if charts[3].Plot.Usuals != "" || charts[3].HasUsually {
		t.Error("the Memory chart has no usually")
	}
	if !charts[0].HasUsually || charts[0].Usually != "usually 111.0" {
		t.Errorf("head = %q (slot %v)", charts[0].Usually, charts[0].HasUsually)
	}
	// The markup: the slot exists even with no band now, and the plot carries the words.
	s := newTestServer(t, newUIEnv(t, true, ""))
	var sb strings.Builder
	if err := s.pages["monitoring"].ExecuteTemplate(&sb, "mon-chart", charts[0]); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	if !strings.Contains(out, `data-usuals="`) || !strings.Contains(out, `data-mon-usually>usually 111.0</span>`) {
		t.Errorf("chart markup lacks the usuals:\n%.600s", out)
	}
}

// A head with no number is "—" alone, never "—%" or "—MB".
func TestMonHeadWithNoNumber(t *testing.T) {
	rng := metrics.Range6h
	base := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), HasHTTP: true, Baseline: service.MetricsBaseline{State: metrics.Full}}
	series := monSeries(72, 5)
	series.Now = nil // the sampler stopped: points, but no number in the last 10 minutes
	base.Requests, base.Errors, base.P95, base.Memory = series, series, series, series
	for _, c := range monBuild(base, false).charts() {
		if c.Value != "—" || c.Unit != "" {
			t.Errorf("%s head = %q %q, want — alone", c.Title, c.Value, c.Unit)
		}
	}
	empty := service.MetricSeries{Points: make([]service.SeriesPoint, 72)}
	base.Requests, base.Errors, base.P95, base.Memory = empty, empty, empty, empty
	for _, c := range monBuild(base, false).charts() {
		if c.Value != "—" || c.Unit != "" {
			t.Errorf("empty %s head = %q %q, want — alone", c.Title, c.Value, c.Unit)
		}
	}
}

// Restarts warn from the rule's threshold (2), not from one.
func TestMonRestartsWarnFromTwo(t *testing.T) {
	v := service.MetricsView{Range: metrics.Range6h, At: monNow, Services: []service.ServiceMetrics{
		{AppID: "a", AppName: "a", Service: "web", Restarts: i64(0)},
		{AppID: "b", AppName: "b", Service: "web", Restarts: i64(1)},
		{AppID: "c", AppName: "c", Service: "web", Restarts: i64(2)},
	}}
	got := map[string]monNum{}
	for _, r := range monBuild(v, false).table().Rows {
		got[r.Name] = r.Restarts
	}
	if got["a"].Warn || got["b"].Warn || !got["c"].Warn || got["b"].Text != "1" {
		t.Errorf("restarts = %+v, want a warning from 2", got)
	}
}

// Logs links carry the service, with the app, from an issue and from a row.
func TestMonLogsLinksCarryTheService(t *testing.T) {
	b := monBuild(service.MetricsView{Range: metrics.Range6h, At: monNow, Services: []service.ServiceMetrics{{AppID: "shop", AppName: "shop", Service: "api"}}}, false)
	issue := b.issue(service.MetricIssue{AppID: "shop", AppName: "shop", Service: "api", Kind: metrics.Restarting, Value: 2})
	if issue.ActionHref != "/logs?app=shop&service=shop%2Fapi" {
		t.Errorf("issue action = %q", issue.ActionHref)
	}
	if row := b.table().Rows[0]; row.LogsHref != "/logs?app=shop&service=shop%2Fapi" {
		t.Errorf("row logs = %q", row.LogsHref)
	}
	errs := b.issue(service.MetricIssue{AppID: "shop", AppName: "shop", Service: "api", Kind: metrics.MoreErrors, Value: 5})
	if errs.ActionHref != "/logs?app=shop&levels=error&service=shop%2Fapi" {
		t.Errorf("errors action = %q", errs.ActionHref)
	}
}

// The ARIA table has as many cells as headers in every row: the logs link sits in a cell.
func TestMonitoringTableCellsMatchHeaders(t *testing.T) {
	e := monEnv(t)
	_, body := e.get(t, "/monitoring")
	heads := strings.Count(body, `role="columnheader"`)
	row := regexp.MustCompile(`(?s)<div class="cv-mnrow" role="row" id="mon-row-whoami-web">(.*?</a></div>)\n</div>`).FindStringSubmatch(body)
	if row == nil {
		t.Fatalf("no row in the page:\n%s", body[strings.Index(body, `cv-mntable`):][:600])
	}
	if cells := strings.Count(row[1], `role="cell"`); cells != heads || heads != 9 {
		t.Errorf("%d cells for %d headers, want 9 and 9", cells, heads)
	}
	if !strings.Contains(row[1], `<div role="cell" class="cv-mnlogscell"><a href="/logs?app=whoami&amp;service=whoami%2Fweb" id="mon-row-whoami-web-logs"`) {
		t.Errorf("the logs link isn't in a cell with its service: %s", row[1])
	}
}
