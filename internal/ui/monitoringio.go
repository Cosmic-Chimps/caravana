package ui

import (
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Network and disk throughput on Monitoring (feature 055): two small charts in the "This host" section,
// over the page's range, drawn with the same chart helpers as the memory chart. Network is the bytes in
// (solid) and out (dashed) of the server's default-route interface; disk is the bytes read (solid) and
// written (dashed) over the devices that hold Nomad's data and Docker's root. A minute with no reading is
// a gap, never a zero. A server without an agent has no charts, and the section says why in one line.

// monIOChart is one of the two charts: a title, the key of its two lines, the plot and the times.
type monIOChart struct {
	ID, Title string
	Solid     string // the key of the solid line
	Dashed    string // the key of the dashed line
	Plot      *monPlot
	AxisStart string
	AxisMid   string
}

// rateUnit picks the unit a chart's numbers are written in from its largest: B/s, KB/s or MB/s.
func rateUnit(top float64) (name string, div float64) {
	switch {
	case top >= 1<<20:
		return "MB/s", 1 << 20
	case top >= 1<<10:
		return "KB/s", 1 << 10
	}
	return "B/s", 1
}

// ioChartOf draws one chart from two series of bytes per second; nil when neither has a reading.
func ioChartOf(id, title, label, solid, dashed string, times []time.Time, a, b []*float64, dated bool) *monIOChart {
	top, have := 0.0, false
	for i := range times {
		for _, p := range []*float64{a[i], b[i]} {
			if p != nil {
				have, top = true, max(top, *p)
			}
		}
	}
	n := len(times)
	if !have || n < 2 {
		return nil
	}
	unit, div := rateUnit(top)
	conv := func(v float64) float64 { return v / div }
	// A calm chart doesn't zoom into its noise: the top gridline is at least 1 of the unit.
	ymax, topLine := scaleOf(top*1.04, 1, conv)
	g := geom{n: n, ymax: ymax}

	plot := &monPlot{Label: label, Scale: amountText(topLine) + " " + unit}
	plot.Gaps = gapsOf(a, g, times)
	words := func(name string, vals []*float64) string {
		lo, hi, latest, ok := rangeOf(vals, identity)
		if !ok {
			return ""
		}
		return name + ": min " + bytesRateText(int64(lo+0.5)) + ", max " + bytesRateText(int64(hi+0.5)) + ", latest " + bytesRateText(int64(latest+0.5))
	}
	plot.Lines = []monLine{
		{Class: "cv-mnline", Segments: segments(a, g.x, g.y), Title: words(solid, a)},
		{Class: "cv-mnline-ref", Segments: segments(b, g.x, g.y), Title: words(dashed, b)},
	}
	plot.Table = monPlotTable{Caption: title + ", times in UTC", Head: solid, Head2: dashed}
	cell := func(p *float64) string {
		if p == nil {
			return "no reading"
		}
		return bytesRateText(int64(*p + 0.5))
	}
	layout := "15:04"
	if dated {
		layout = "2006-01-02 15:04"
	}
	for i, at := range times {
		plot.Table.Rows = append(plot.Table.Rows, monPlotRow{Time: at.UTC().Format(layout), Value: cell(a[i]), Value2: cell(b[i])})
	}
	return &monIOChart{ID: id, Title: title, Solid: solid, Dashed: dashed, Plot: plot,
		AxisStart: axisTime(times[0], dated) + " UTC", AxisMid: axisTime(times[n/2], dated)}
}

// hostIOChartsOf builds the section's I/O part: the two charts when the series has a reading, else the
// one line that says why there is none.
func hostIOChartsOf(now time.Time, host service.HostView, series service.HostIOSeries) (charts []*monIOChart, line *monHostLine) {
	if !series.Known {
		text := "Network and disk throughput appear a minute or two after the server's agent has reported twice."
		if !host.IO.Known() {
			text = ioWhy(now, host.IO).String()
		}
		return nil, &monHostLine{Mark: "idle", Text: msentence(mtext(text))}
	}
	n := len(series.Points)
	times := make([]time.Time, n)
	cols := [4][]*float64{}
	for i := range cols {
		cols[i] = make([]*float64, n)
	}
	for i, p := range series.Points {
		times[i] = p.T
		cols[0][i], cols[1][i], cols[2][i], cols[3][i] = p.NetRxBps, p.NetTxBps, p.DiskReadBps, p.DiskWriteBps
	}
	dated := series.Range.Step >= time.Hour || series.Range.Span >= 24*time.Hour
	words := rangeWords(series.Range)
	if c := ioChartOf("mon-io-net", "Network, last "+words, "Network over the last "+words+": bytes in and out of the default interface",
		"in", "out", times, cols[0], cols[1], dated); c != nil {
		charts = append(charts, c)
	}
	if c := ioChartOf("mon-io-disk", "Disk, last "+words, "Disk over the last "+words+": bytes read and written",
		"read", "write", times, cols[2], cols[3], dated); c != nil {
		charts = append(charts, c)
	}
	return charts, nil
}
