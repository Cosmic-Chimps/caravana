package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

func TestNiceCeil(t *testing.T) {
	for in, want := range map[float64]float64{
		0: 1, 0.03: 0.03, 0.31: 0.4, 1: 1, 1.01: 1.2, 3.03: 4, 99.5: 100, 100: 100, 149.3: 150, 410: 500, 799: 800, 800: 800, 801: 1000, 12000: 12000,
	} {
		if got := niceCeil(in); got < want*(1-1e-9) || got > want*(1+1e-9) {
			t.Errorf("niceCeil(%v) = %v, want %v", in, got, want)
		}
	}
}

// The scale: the top gridline is a round number of the displayed unit, labelled; a chart that stays
// calm (Errors under 5%) doesn't zoom into its noise; and the series keeps below the plot's top.
func TestMonChartScale(t *testing.T) {
	rng := metrics.Range6h
	v := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), HasHTTP: true,
		Baseline: service.MetricsBaseline{State: metrics.Partial, FullDays: 3}}
	req := monSeries(72, 100)
	for i := 10; i <= 40; i++ {
		req.Points[i].Low, req.Points[i].High, req.Points[i].Usual, req.Points[i].Days = fl(80), fl(120), fl(100), 3
	}
	v.Requests = req
	v.P95 = monSeries(72, 180)
	v.Errors = monSeries(72, 0.3)
	v.Memory = monSeries(72, 3600)
	v.MemoryLimit = make([]*float64, 72)
	for i := range v.MemoryLimit {
		v.MemoryLimit[i] = fl(5530)
	}
	charts := monBuild(v, false).charts()
	for i, want := range []string{"100 req/s", "5%", "150 ms", "3G"} {
		if got := charts[i].Plot.Scale; got != want {
			t.Errorf("%s scale = %q, want %q", charts[i].Title, got, want)
		}
	}
	// The value 100 on a scale whose top gridline is 100 draws on that gridline (y=30).
	if !strings.Contains(charts[0].Plot.Lines[0].Segments[0], ",30") {
		t.Errorf("requests at 100 aren't on the top gridline: %.80s", charts[0].Plot.Lines[0].Segments[0])
	}

	// More errors than the floor: the scale follows the data; MB below a gigabyte.
	v.Errors = monSeries(72, 8)
	v.Memory = monSeries(72, 300)
	for i := range v.MemoryLimit {
		v.MemoryLimit[i] = fl(512)
	}
	charts = monBuild(v, false).charts()
	if got := charts[1].Plot.Scale; got != "8%" {
		t.Errorf("errors at 8%% scale = %q", got)
	}
	if got := charts[3].Plot.Scale; got != "250M" {
		t.Errorf("memory in MB scale = %q", got)
	}

	// A chart of nothing but zero is still scaled, from 1.
	v.Requests = monSeries(72, 0)
	if got := monBuild(v, false).charts()[0].Plot.Scale; got != "1 req/s" {
		t.Errorf("a flat zero scale = %q", got)
	}
}

// The scale label and the band's edges are in the page's markup.
func TestMonChartMarkup(t *testing.T) {
	rng := metrics.Range6h
	v := service.MetricsView{Range: rng, At: monNow, Points: monPoints(rng), HasHTTP: true,
		Baseline: service.MetricsBaseline{State: metrics.Partial, FullDays: 3}}
	req := monSeries(72, 100)
	for i := 10; i <= 40; i++ {
		req.Points[i].Low, req.Points[i].High, req.Points[i].Usual, req.Points[i].Days = fl(80), fl(120), fl(100), 3
	}
	req.Points[60].Low, req.Points[60].High, req.Points[60].Usual, req.Points[60].Days = fl(90), fl(110), fl(100), 3 // a lone point
	v.Requests = req
	v.P95, v.Errors, v.Memory = monSeries(72, 180), monSeries(72, 0.3), monSeries(72, 100)
	c := monBuild(v, false).charts()[0]
	if len(c.Plot.Bands) != 2 {
		t.Fatalf("bands = %+v", c.Plot.Bands)
	}
	long := c.Plot.Bands[0]
	if got := len(strings.Fields(long.Top)); got != 31 || len(strings.Fields(long.Bottom)) != 31 {
		t.Errorf("edges have %d and %d vertices, want 31 each", got, len(strings.Fields(long.Bottom)))
	}
	if lone := c.Plot.Bands[1]; len(strings.Fields(lone.Top)) != 2 {
		t.Errorf("a lone point's edge = %q, want a short stroke", lone.Top)
	}

	var sb strings.Builder
	s := newTestServer(t, newUIEnv(t, true, ""))
	if err := s.pages["monitoring"].ExecuteTemplate(&sb, "mon-plot", c.Plot); err != nil {
		t.Fatal(err)
	}
	svg := sb.String()
	polygon, edge := strings.Index(svg, `<polygon class="cv-mnband"`), strings.Index(svg, `<polyline class="cv-mnband-edge"`)
	if polygon < 0 || edge < polygon {
		t.Errorf("the band's edges don't follow its polygon:\n%s", svg)
	}
	if n := strings.Count(svg, `class="cv-mnband-edge"`); n != 4 {
		t.Errorf("%d band edges, want a high and a low for each of 2 stretches", n)
	}
	if !regexp.MustCompile(`<span class="cv-mnscale" aria-hidden="true">100 req/s</span>`).MatchString(svg) {
		t.Errorf("no scale label:\n%s", svg)
	}
	if regexp.MustCompile(monNoStyle).MatchString(svg) {
		t.Error("the plot has a style attribute")
	}
}

// The host's memory chart has a scale too, in gigabytes.
func TestHostChartScale(t *testing.T) {
	now := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	h := hostSample(now)
	n := 168
	h.History = service.HostHistory{Points: make([]time.Time, n), MemoryUsed: make([]*float64, n), MemoryReserved: make([]*float64, n)}
	for i := range h.History.Points {
		h.History.Points[i] = now.Truncate(time.Hour).Add(time.Duration(i-n+1) * time.Hour)
		h.History.MemoryUsed[i], h.History.MemoryReserved[i] = fl(2000), fl(1024)
	}
	if got := hostViewOf(now, h).Chart.Plot.Scale; got != "4G" {
		t.Errorf("host chart scale = %q, want 4G", got)
	}
}
