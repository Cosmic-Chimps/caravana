package ui

import (
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The charts of the Monitoring page: the console's second SVG family (the Logs histogram was the
// first). A chart is an <svg viewBox="0 0 300 120" preserveAspectRatio="none">: its shapes are
// elements with attributes (points, x, width) and its colours are classes, so the page needs no
// style attribute, and every stroke is non-scaling, so the squeeze doesn't thicken it. Drawn in
// this order: gridlines, the band, the unusual stretch, gaps, deploy markers, the series, the
// crosshair. A hidden table carries the same numbers for a screen reader.

// The plot's box, in the SVG's own units. The series stays 4 units off the top and bottom.
const (
	plotW   = 300.0
	plotH   = 120.0
	plotPad = 4.0
)

// monChart is one chart panel.
type monChart struct {
	ID, Title string
	// Value and Unit are the head's number ("127", "req/s"); Warn puts it in the warn hue;
	// Usually is "usually 120" ("" when there is no band or the chart has none).
	Value, Unit string
	Warn        bool
	Usually     string
	Plot        *monPlot
	Empty       *monEmpty
	AxisStart   string
	AxisMid     string
	Captions    []monCaption
}

// monEmpty says why a chart has nothing to draw, in the plot's place.
type monEmpty struct {
	Mark string // "" or idle
	Text monSentence
}

// monCaption is a line under a chart's times.
type monCaption struct {
	Mark string
	Text monSentence
}

// monPoly is a polygon (the band) with its title.
type monPoly struct {
	Points string
	Title  string
}

// monRect is a rect that spans the plot's height from X, Width wide.
type monRect struct {
	X, W  string
	Title string
}

// monMark is a deploy's vertical line.
type monMark struct {
	X     string
	Title string
}

// monLine is a series: one polyline per unbroken stretch, all with the same title.
type monLine struct {
	Class    string // cv-mnline or cv-mnline-ref
	Segments []string
	Title    string
}

// monPlot is the SVG of a chart. The Hover fields are the data-attributes monitoring.js reads: the
// first point's time, the step in seconds, each point's number (empty for a gap) and the unit.
type monPlot struct {
	Label   string
	Bands   []monPoly
	Warn    *monRect
	Gaps    []monRect
	Deploys []monMark
	Labels  []string
	Lines   []monLine
	Table   monPlotTable

	Hover  bool
	T0     string
	Step   int
	Values string
	Unit   string
	Date   bool
}

// monPlotTable is the hidden table of a plot.
type monPlotTable struct {
	Caption string
	Head    string // the value column's title
	Rows    []monPlotRow
}

type monPlotRow struct{ Time, Value string }

// geom maps points and values onto the plot's box.
type geom struct {
	n    int
	ymax float64
}

func (g geom) x(i int) float64 {
	if g.n < 2 {
		return 0
	}
	return float64(i) / float64(g.n-1) * plotW
}

func (g geom) y(v float64) float64 {
	if g.ymax <= 0 {
		return plotH - plotPad
	}
	return plotH - plotPad - min(v, g.ymax)/g.ymax*(plotH-2*plotPad)
}

// metricSpec says how one metric reads: how its numbers are written in the head, the title and
// the hover, and what its band is called.
type metricSpec struct {
	id, title string
	series    service.MetricSeries
	http      bool
	// conv turns a stored number into the displayed one (megabytes into gigabytes), num writes it,
	// and unit names it in titles, the hidden table and the hover.
	conv func(float64) float64
	num  func(float64) string
	unit string
	// head writes the head's value and unit from the newest number.
	head func(now float64, over bool) (value, unit string)
	// usual writes "usually ..." for the band's median; nil for a chart without one.
	usual func(float64) string
	warn  bool
}

func identity(v float64) float64 { return v }

// charts builds the four panels.
func (b *monBuilder) charts() []monChart {
	v := b.view
	useGB, limit := b.memoryScale()
	memConv, memNum, memUnit := identity, func(f float64) string { return strconv.Itoa(int(f + 0.5)) }, "MB"
	if useGB {
		memConv = func(f float64) float64 { return f / 1024 }
		memNum, memUnit = func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }, "GB"
	}
	limitWords := ""
	if limit > 0 {
		limitWords = " of " + memNum(memConv(limit)) + " limit"
	}
	specs := []metricSpec{
		{id: "requests", title: "Requests", series: v.Requests, http: true, conv: identity, num: rateText, unit: "req/s",
			head:  func(now float64, _ bool) (string, string) { return rateText(now), "req/s" },
			usual: rateText},
		{id: "errors", title: "Errors", series: v.Errors, http: true, conv: identity, num: percentText, unit: "%",
			head:  func(now float64, _ bool) (string, string) { return percentText(now), "% 5xx" },
			usual: percentText, warn: v.Errors.UnusualSince != nil},
		{id: "p95", title: "Slowest 5%", series: v.P95, http: true, conv: identity,
			num: func(f float64) string { return strconv.Itoa(int(f + 0.5)) }, unit: "ms",
			head: func(now float64, over bool) (string, string) {
				n, u := msText(now)
				if over {
					n = "over " + n
				}
				return n, u
			},
			usual: func(f float64) string { return msWords(f) }, warn: v.P95.UnusualSince != nil},
		{id: "memory", title: "Memory", series: v.Memory, conv: memConv, num: memNum, unit: memUnit,
			head: func(now float64, _ bool) (string, string) { return memNum(memConv(now)), memUnit + limitWords }},
	}
	out := make([]monChart, 0, len(specs))
	for _, sp := range specs {
		out = append(out, b.chart(sp))
	}
	return out
}

// memoryScale says whether the memory chart is written in gigabytes (the limit, or the biggest
// number when there is none, reaches 1 GB) and what the limit is now (0 when none is known).
func (b *monBuilder) memoryScale() (useGB bool, limit float64) {
	for i := len(b.view.MemoryLimit) - 1; i >= 0; i-- {
		if l := b.view.MemoryLimit[i]; l != nil {
			limit = *l
			break
		}
	}
	biggest := limit
	for _, p := range b.view.Memory.Points {
		if p.Value != nil {
			biggest = max(biggest, *p.Value)
		}
	}
	return biggest >= 1024, limit
}

// rangeWords is how a heading says the range: "hour", "6 hours", "24 hours", "7 days".
func rangeWords(r metrics.Range) string {
	switch r.Name {
	case "1h":
		return "hour"
	case "6h":
		return "6 hours"
	case "24h":
		return "24 hours"
	}
	return "7 days"
}

// axisTime is a time on a chart's axis, in UTC: "08:02", and with the weekday when the range is a
// day or more.
func axisTime(t time.Time, dated bool) string {
	if dated {
		return t.UTC().Format("Mon 15:04")
	}
	return t.UTC().Format("15:04")
}

// chart builds one panel from a metric.
func (b *monBuilder) chart(sp metricSpec) monChart {
	v := b.view
	c := monChart{ID: "mon-chart-" + sp.id, Title: sp.title, Unit: sp.unit, Value: "—", Warn: sp.warn}
	pts := sp.series.Points
	n := len(pts)

	vals := make([]*float64, n)
	hasValue := false
	for i, p := range pts {
		vals[i] = p.Value
		hasValue = hasValue || p.Value != nil
	}
	if !hasValue || n < 2 {
		c.Empty = b.emptyChart(sp)
		return c
	}
	if sp.series.Now != nil {
		c.Value, c.Unit = sp.head(*sp.series.Now, sp.series.Over)
	}
	if sp.usual != nil && sp.series.Usual != nil && b.view.Baseline.State != metrics.Building {
		c.Usually = "usually " + sp.usual(*sp.series.Usual)
	}

	dated := v.Range.Step >= time.Hour || v.Range.Span >= 24*time.Hour
	times := v.Points
	g := geom{n: n}
	g.ymax = 0
	for _, p := range pts {
		if p.Value != nil {
			g.ymax = max(g.ymax, *p.Value)
		}
		if p.High != nil {
			g.ymax = max(g.ymax, *p.High)
		}
	}
	g.ymax *= 1.08

	lo, hi, latest, haveLatest := rangeOf(vals, sp.conv)
	plot := &monPlot{
		Label: sp.title + " over the last " + rangeWords(v.Range),
		T0:    times[0].UTC().Format(time.RFC3339), Step: int(v.Range.Step / time.Second), Unit: sp.unit,
		Date: dated, Hover: true,
	}
	if sp.series.Now != nil {
		plot.Label += ", " + sp.num(sp.conv(*sp.series.Now)) + " " + sp.unit + " now"
	}

	// The band: one polygon per stretch of points that have one.
	plot.Bands = b.bands(g, pts, sp)

	// The unusual stretch, from when the rule began to now.
	tx := func(t time.Time) float64 {
		span := times[n-1].Sub(times[0])
		if span <= 0 {
			return 0
		}
		return min(plotW, max(0, float64(t.Sub(times[0]))/float64(span)*plotW))
	}
	if sp.series.UnusualSince != nil {
		x := tx(*sp.series.UnusualSince)
		plot.Warn = &monRect{X: units(x), W: units(plotW - x), Title: "Unusual since " + clockOf(b.now, *sp.series.UnusualSince).Text}
	}
	plot.Gaps = gapsOf(vals, g, times)

	// Deploys: a dashed line each, and at most a couple of labels.
	for _, d := range v.Deploys {
		if d.EndedAt.Before(times[0]) || d.EndedAt.After(v.At) {
			continue
		}
		plot.Deploys = append(plot.Deploys, monMark{X: units(tx(d.EndedAt)), Title: d.AppName + " v" + strconv.FormatUint(d.Version, 10) + " rolled out at " + clockOf(b.now, d.EndedAt).Text})
	}
	plot.Labels = deployLabels(v.Deploys, times[0], v.At)

	title := ""
	if haveLatest {
		title = "min " + sp.num(lo) + ", max " + sp.num(hi) + ", latest " + sp.num(latest) + " " + sp.unit
	}
	plot.Lines = []monLine{{Class: "cv-mnline", Segments: segments(vals, g.x, g.y), Title: title}}

	// The numbers: for the hover and for the hidden table.
	cells := make([]string, n)
	plot.Table = monPlotTable{Caption: sp.title + " in each point of the range, times in UTC", Head: sp.title + " (" + sp.unit + ")"}
	layout := "15:04"
	if dated {
		layout = "2006-01-02 15:04"
	}
	for i, p := range pts {
		row := monPlotRow{Time: times[i].UTC().Format(layout), Value: "no reading"}
		if p.Value != nil {
			cells[i] = sp.num(sp.conv(*p.Value))
			row.Value = cells[i]
		}
		plot.Table.Rows = append(plot.Table.Rows, row)
	}
	plot.Values = strings.Join(cells, ",")
	c.Plot = plot

	c.AxisStart = axisTime(times[0], dated) + " UTC"
	c.AxisMid = axisTime(times[n/2], dated)
	c.Captions = b.captions(sp)
	return c
}

// emptyChart says why a chart has nothing to draw.
func (b *monBuilder) emptyChart(sp metricSpec) *monEmpty {
	if sp.http {
		if !b.view.HasHTTP {
			if w := b.where(); w != nil {
				return &monEmpty{Text: append(append(msentence(mtext("No HTTP traffic: no app in ")), w...), mtext(" has a domain."))}
			}
			return &monEmpty{Text: msentence(mtext("No HTTP traffic: no app has a domain."))}
		}
		if s := traefikSentence(b.view.Sources.Traefik); s != nil {
			return &monEmpty{Mark: "idle", Text: s}
		}
	}
	return &monEmpty{Text: msentence(mtext("Nothing has been sampled in this range yet."))}
}

// captions are the lines under a chart's times: the baseline being built, the 7 d range's thin
// band, and what is wrong with Traefik when the chart has history to show anyway.
func (b *monBuilder) captions(sp metricSpec) []monCaption {
	var out []monCaption
	if b.view.Baseline.State == metrics.Building {
		text := "Building the baseline"
		if st := b.baselineStarted(); st != "" {
			text += ": started " + st + "."
		} else {
			text += "."
		}
		out = append(out, monCaption{Text: msentence(mtext(text + " The usual range appears after a full day."))})
	} else if b.view.Range.Name == metrics.Range7d.Name {
		out = append(out, monCaption{Text: msentence(mtext("The band thins to the left: older hours have fewer earlier days kept."))})
	}
	if sp.http {
		if s := traefikSentence(b.view.Sources.Traefik); s != nil {
			out = append(out, monCaption{Mark: "idle", Text: s})
		}
	}
	return out
}

// rangeOf is the lowest, highest and newest displayed number of a series.
func rangeOf(vals []*float64, conv func(float64) float64) (lo, hi, latest float64, ok bool) {
	for _, p := range vals {
		if p == nil {
			continue
		}
		v := conv(*p)
		if !ok {
			lo, hi = v, v
		}
		lo, hi, latest, ok = min(lo, v), max(hi, v), v, true
	}
	return lo, hi, latest, ok
}

// bands makes the band's polygons: for each stretch of points that have a band, the highest value
// of the same hour on the earlier days along the top and the lowest back along the bottom. Its
// title says how many days it rests on.
func (b *monBuilder) bands(g geom, pts []service.SeriesPoint, sp metricSpec) []monPoly {
	var out []monPoly
	days := 0
	lo, hi := 0.0, 0.0
	first := true
	var tops, bottoms []string
	flush := func() {
		if len(tops) == 0 {
			return
		}
		// bottoms were collected left to right; the polygon returns right to left.
		poly := append([]string{}, tops...)
		for i := len(bottoms) - 1; i >= 0; i-- {
			poly = append(poly, bottoms[i])
		}
		out = append(out, monPoly{Points: strings.Join(poly, " ")})
		tops, bottoms = nil, nil
	}
	for i, p := range pts {
		if p.Low == nil || p.High == nil {
			flush()
			continue
		}
		tops = append(tops, units(g.x(i))+","+units(g.y(*p.High)))
		bottoms = append(bottoms, units(g.x(i))+","+units(g.y(*p.Low)))
		days = max(days, p.Days)
		if first {
			lo, hi, first = *p.Low, *p.High, false
		}
		lo, hi = min(lo, *p.Low), max(hi, *p.High)
	}
	flush()
	if len(out) == 0 {
		return nil
	}
	title := "the same hour on the last 7 days"
	if days < metrics.BandDays {
		title = "the same hour on the " + strconv.Itoa(days) + " days there are"
		if days == 1 {
			title = "the same hour on the 1 day there is"
		}
	}
	title += ": " + sp.num(sp.conv(lo)) + " to " + sp.num(sp.conv(hi)) + " " + sp.unit
	for i := range out {
		out[i].Title = title
	}
	return out
}

// gapMinRun is how many missing points in a row make a stretch drawn as a gap.
const gapMinRun = 3

// gapsOf lists the long stretches with no reading, as rects between the readings on either side.
func gapsOf(vals []*float64, g geom, times []time.Time) []monRect {
	var out []monRect
	n := len(vals)
	for i := 0; i < n; {
		if vals[i] != nil {
			i++
			continue
		}
		j := i
		for j+1 < n && vals[j+1] == nil {
			j++
		}
		if j-i+1 >= gapMinRun {
			x0, x1 := 0.0, plotW
			from, to := times[i], times[j]
			if i > 0 {
				x0 = g.x(i - 1)
			}
			if j < n-1 {
				x1 = g.x(j + 1)
			}
			out = append(out, monRect{X: units(x0), W: units(x1 - x0), Title: "No readings from " + from.UTC().Format("15:04") + " to " + to.UTC().Format("15:04") + " UTC"})
		}
		i = j + 1
	}
	return out
}

// deployLabels are the labels over the plot: each deployment's own when there are one or two,
// else the newest and "and N more".
func deployLabels(deploys []service.MetricDeploy, from, to time.Time) []string {
	var in []service.MetricDeploy
	for _, d := range deploys {
		if !d.EndedAt.Before(from) && !d.EndedAt.After(to) {
			in = append(in, d)
		}
	}
	label := func(d service.MetricDeploy) string { return d.AppName + " v" + strconv.FormatUint(d.Version, 10) }
	switch {
	case len(in) == 0:
		return nil
	case len(in) <= 2:
		var out []string
		for _, d := range in {
			out = append(out, label(d))
		}
		return out
	}
	return []string{label(in[len(in)-1]), "and " + strconv.Itoa(len(in)-1) + " more"}
}
