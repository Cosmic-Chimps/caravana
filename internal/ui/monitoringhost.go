package ui

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The host (feature 025): the "This host" section of /monitoring (#host), and the meters, the
// room sentence and the seven-day memory chart that the Apps home's Platform panel shares. All of
// it comes from service.Host, which reads the store and never calls Nomad, so a page that draws it
// doesn't wait on the orchestrator. One host today (Phase 3 adds more): "1 server".

// roomTitle is the room sentence's hover: what the count is, and what it leaves out (ruling 7).
const roomTitle = "Nomad's count of what fits. Caravana's and the host's own processes aren't reserved."

// monMeter is the Meter of the handoff, as an SVG: a track, what is reserved hatched under what is
// used, and a readout. Lengths are attributes in a 0-100 box, so no style attribute is needed. Warn
// puts the fill and the readout in the warn hue (CPU above 90 %; memory and disks from 85 %); a meter
// in a state also has its mark ahead of the readout (◆ from 85 %, ■ from 95 %), so the hue is never
// the only sign.
type monMeter struct {
	Label       string
	HasReserved bool
	ResW        string
	Fill        string
	Read        string
	Warn        bool
	Mark        string // "", warning or failed
	MarkWord    string // "nearly full", "full": for a screen reader
	Title       string
	// Split is a readout of "used · reserved of total": too long for the bar's row, so it
	// is drawn under the bar.
	Split bool
}

// monHostLine is a line about the host's readings: a problem, with its mark.
type monHostLine struct {
	Mark string
	Text monSentence
}

// monRoom is the room sentence; Warn puts it in the warn hue ("No room…").
type monRoom struct {
	Text  monSentence
	Warn  bool
	Title string
}

// monHostChart is the seven-day memory chart: what is used (solid) and what is reserved (dashed).
type monHostChart struct {
	Plot      *monPlot
	AxisStart string
	AxisMid   string
}

// monHostView is what #host draws (and /parts/monitoring/host alone).
type monHostView struct {
	Meta   string
	Lines  []monHostLine
	Meters []monMeter
	Room   *monRoom
	Chart  *monHostChart
}

// partMonitoringHost answers GET /parts/monitoring/host: the section alone.
func (s *server) partMonitoringHost(w http.ResponseWriter, r *http.Request, session string) {
	host, err := s.svc.Host(r.Context())
	if err != nil {
		s.partFailed(w, "read the host", err)
		return
	}
	s.renderPart(w, "monitoring", "mon-host", hostViewOf(s.now().UTC(), host))
}

// hostViewOf builds the section from the host's numbers read at now.
func hostViewOf(now time.Time, h service.HostView) *monHostView {
	v := &monHostView{Meta: "1 server"}
	v.Lines = hostLines(now, h)
	if h.SampledAt != nil {
		v.Meters = hostMeters(h)
		v.Room = roomOf(h)
	}
	v.Chart = hostChartOf(now, h)
	return v
}

// hostLines are the problems with reading the host: Nomad's refusal or silence, a host that was
// never read, and a newest sample that is more than 5 minutes old.
func hostLines(now time.Time, h service.HostView) []monHostLine {
	var out []monHostLine
	switch h.Sources.Nomad {
	case service.SamplerDenied:
		out = append(out, monHostLine{Mark: "failed", Text: nomadDeniedSentence()})
	case service.SamplerUnreachable:
		out = append(out, monHostLine{Mark: "failed", Text: msentence(mtext("Can't read Nomad right now, so these numbers are the last ones sampled."))})
	}
	switch {
	case h.SampledAt == nil:
		if h.Sources.Nomad != service.SamplerDenied {
			out = append(out, monHostLine{Mark: "idle", Text: msentence(mtext("The host hasn't been read yet. Its numbers appear a minute after Caravana starts."))})
		}
	case h.Stale:
		out = append(out, monHostLine{Mark: "failed", Text: msentence(mtext("The newest reading of the host is from "), mclock(clockOf(now, *h.SampledAt)), mtext(". The numbers here are the last known ones."))})
	}
	return out
}

// hostMeters are the meters of the newest sample: memory, CPU and each watched filesystem. A
// number the sampler couldn't read leaves its meter out.
func hostMeters(h service.HostView) []monMeter {
	var out []monMeter
	if m := memoryMeter(h); m != nil {
		out = append(out, *m)
	}
	if m := cpuMeter(h); m != nil {
		out = append(out, *m)
	}
	return append(out, diskMeters(h)...)
}

// The host memory meter's thresholds are the disk's: warn from 85 % used, failed from 95 %, under 85 %
// the fill is the plain fg-2. (CPU keeps the Meter's 90 %.) The memory used here is Nomad's host
// `Memory.Used`, which leaves out the page cache, as the apps' numbers do.
const (
	meterWarnPercent = metrics.DiskWarnPercent
	meterFailPercent = metrics.DiskFailPercent
)

// meterFill is how much of a 0-100 box used takes of total.
func meterFill(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return max(0, min(100, used/total*100))
}

// vcpuText is an amount of CPU in vCPU, written as amountText does, except that a little use is
// "<0.01" (a running app never reads as idle); exactly 0 stays "0".
func vcpuText(v float64) string {
	if v > 0 && v < 0.01 {
		return "<0.01"
	}
	return amountText(v)
}

// amountText is a number as the page writes an amount: whole from 10, one decimal from 1, up to
// two under 1 (a small app's 0.04 is not "0"): "12", "1.2", "0.1", "0.04".
func amountText(v float64) string {
	switch {
	case v >= 10:
		return strconv.Itoa(int(v + 0.5))
	case v >= 1:
		return trim1(v)
	}
	return strconv.FormatFloat(float64(int(v*100+0.5))/100, 'f', -1, 64)
}

// coreMHzOf is the MHz of one core of the host, 0 when the cores or the total weren't read.
func coreMHzOf(c service.HostCPU) float64 {
	if c.Total == nil || c.Cores == nil || *c.Cores <= 0 {
		return 0
	}
	return float64(*c.Total) / float64(*c.Cores)
}

// memoryMeter reads "1.7G used · 1.3G reserved of 1.9G": what is used and what Nomad has set
// aside (hatched, drawn above the fill) against the total. The mark and the warn hue follow
// what is used.
func memoryMeter(h service.HostView) *monMeter {
	r := h.Memory
	if r.Used == nil || r.Total == nil || *r.Total <= 0 {
		return nil
	}
	used, total := float64(*r.Used), float64(*r.Total)
	m := &monMeter{Label: "mem", Fill: units(meterFill(used, total)), Split: true}
	m.Read = gbText(used) + "G used"
	if r.Reserved != nil {
		m.Read += " · " + gbText(float64(*r.Reserved)) + "G reserved"
	}
	m.Read += " of " + gbText(total) + "G"
	switch pct := used / total * 100; {
	case pct >= meterFailPercent:
		m.Warn, m.Mark, m.MarkWord = true, "failed", "nearly all in use"
	case pct >= meterWarnPercent:
		m.Warn, m.Mark, m.MarkWord = true, "warning", "mostly in use"
	}
	m.Title = sizeText(*r.Used) + " in use"
	if r.Reserved != nil {
		m.HasReserved, m.ResW = true, units(meterFill(float64(*r.Reserved), total))
		m.Title += ", " + sizeText(*r.Reserved) + " reserved"
	}
	m.Title += " of " + sizeText(*r.Total)
	return m
}

// cpuMeter reads "1.2 used · 0.8 reserved of 2 vCPU" (in MHz when the cores aren't known).
func cpuMeter(h service.HostView) *monMeter {
	r := h.CPU
	if r.Used == nil || r.Total == nil || *r.Total <= 0 {
		return nil
	}
	used, total := float64(*r.Used), float64(*r.Total)
	m := &monMeter{Label: "cpu", Fill: units(meterFill(used, total)), Split: true}
	m.Warn = used/total*100 > metrics.MemoryPercent
	// amount writes a number of MHz in the one unit the page uses: vCPU, or MHz without cores.
	perCore := coreMHzOf(r)
	amount := func(mhz float64) string {
		if perCore > 0 {
			return vcpuText(mhz / perCore)
		}
		return strconv.Itoa(int(mhz + 0.5))
	}
	unit := " MHz"
	if perCore > 0 {
		unit = " vCPU"
	}
	m.Read = amount(used) + " used"
	m.Title = amount(used) + unit + " in use"
	if r.Reserved != nil {
		m.HasReserved, m.ResW = true, units(meterFill(float64(*r.Reserved), total))
		m.Read += " · " + amount(float64(*r.Reserved)) + " reserved"
		m.Title += ", " + amount(float64(*r.Reserved)) + unit + " reserved"
	}
	m.Read += " of " + amount(total) + unit
	m.Title += " of " + amount(total) + unit
	return m
}

// diskMeters is one meter per watched filesystem, "18/40G", with its mount in the readout when
// there is more than one. ◆ at 85 % and ■ at 95 %.
func diskMeters(h service.HostView) []monMeter {
	var out []monMeter
	for _, d := range h.Disks {
		m := monMeter{Label: "disk", Fill: units(meterFill(d.UsedPercent, 100)), Read: trim1(float64(d.UsedMB)/1024) + "/" + trim1(float64(d.TotalMB)/1024) + "G"}
		if len(h.Disks) > 1 {
			m.Read = d.Mount + " " + m.Read
		}
		switch d.State {
		case metrics.DiskWarning:
			m.Warn, m.Mark, m.MarkWord = true, "warning", "nearly full"
		case metrics.DiskFailed:
			m.Warn, m.Mark, m.MarkWord = true, "failed", "full"
		}
		m.Title = d.Mount + ": " + strconv.Itoa(int(d.UsedPercent)) + "% used, " + sizeText(d.FreeMB) + " left"
		out = append(out, m)
	}
	return out
}

// roomOf is the room sentence: how many more apps like the usual one fit, "No room" naming what
// ran out, or what is free when nothing runs yet. Nil when the totals or reservations weren't read.
// Room counts reservations, so when the memory in use is already at 85 % or more the sentence says
// so after the count: ", but memory in use is already at 89%."
func roomOf(h service.HostView) *monRoom {
	r := h.Room
	if !r.Known {
		return nil
	}
	out := &monRoom{Title: roomTitle}
	name := r.LikeAppName
	if name == "" {
		name = r.LikeApp
	}
	switch {
	case !r.HasYardstick:
		out.Text = msentence(mtext("Nothing is running yet: " + sizeText(r.FreeMemoryMB) + " of memory is free to reserve."))
	case r.Count > 0:
		cpu := strconv.FormatInt(r.CPUMHz, 10) + " MHz"
		if perCore := coreMHzOf(h.CPU); perCore > 0 {
			cpu = vcpuText(float64(r.CPUMHz)/perCore) + " vCPU"
		}
		end := "."
		if h.Memory.Used != nil && h.Memory.Total != nil && *h.Memory.Total > 0 {
			if pct := float64(*h.Memory.Used) / float64(*h.Memory.Total) * 100; pct >= meterWarnPercent {
				end = ", but memory in use is already at " + strconv.Itoa(int(pct)) + "%."
			}
		}
		out.Text = msentence(mtext("Room for about "+strconv.Itoa(r.Count)+" more apps like "), mname(name),
			mtext(" ("+sizeText(r.MemoryMB)+", "+cpu+" each)"+end))
	default:
		what := "memory"
		if r.LimitedBy == metrics.ResourceCPU {
			what = "CPU"
		}
		out.Warn = true
		out.Text = msentence(mtext("No room for another app like "), mname(name), mtext(": "+what+" is fully reserved."))
	}
	return out
}

// hostChartOf is the seven-day memory chart, or nil when no hour has a reading.
func hostChartOf(now time.Time, h service.HostView) *monHostChart {
	hist := h.History
	n := len(hist.Points)
	if n < 2 {
		return nil
	}
	have := false
	top := 0.0
	for i := range hist.Points {
		for _, p := range []*float64{hist.MemoryUsed[i], hist.MemoryReserved[i]} {
			if p != nil {
				have, top = true, max(top, *p)
			}
		}
	}
	if !have {
		return nil
	}
	if h.Memory.Total != nil {
		top = max(top, float64(*h.Memory.Total))
	}
	// The scale: gigabytes from a gigabyte up; the top gridline is a round number.
	useGB := top >= 1024
	conv := identity
	if useGB {
		conv = func(mb float64) float64 { return mb / 1024 }
	}
	ymax, topLine := scaleOf(top*1.04, 0, conv)
	g := geom{n: n, ymax: ymax}

	plot := &monPlot{Label: "Memory over the last 7 days: what is used and what is reserved", Scale: memoryScaleLabel(useGB, topLine)}
	plot.Gaps = gapsOf(hist.MemoryUsed, g, hist.Points)
	words := func(label string, vals []*float64) string {
		lo, hi, latest, ok := rangeOf(vals, identity)
		if !ok {
			return ""
		}
		return label + ": min " + sizeText(int64(lo+0.5)) + ", max " + sizeText(int64(hi+0.5)) + ", latest " + sizeText(int64(latest+0.5))
	}
	plot.Lines = []monLine{
		{Class: "cv-mnline", Segments: segments(hist.MemoryUsed, g.x, g.y), Title: words("used", hist.MemoryUsed)},
		{Class: "cv-mnline-ref", Segments: segments(hist.MemoryReserved, g.x, g.y), Title: words("reserved", hist.MemoryReserved)},
	}
	plot.Table = monPlotTable{Caption: "Memory in each hour of the last 7 days, times in UTC", Head: "Used", Head2: "Reserved"}
	cell := func(p *float64) string {
		if p == nil {
			return "no reading"
		}
		return sizeText(int64(*p + 0.5))
	}
	for i, at := range hist.Points {
		plot.Table.Rows = append(plot.Table.Rows, monPlotRow{Time: at.UTC().Format("2006-01-02 15:04"), Value: cell(hist.MemoryUsed[i]), Value2: cell(hist.MemoryReserved[i])})
	}
	return &monHostChart{Plot: plot, AxisStart: axisTime(hist.Points[0], true) + " UTC", AxisMid: axisTime(hist.Points[n/2], true)}
}

// ---- the Apps home's Platform panel -----------------------------------------------------------

// monPlatform is the Platform panel of the Apps home's rail: whether the host can be read, the
// memory meter, the fuller disk's meter and the room sentence. It links to /monitoring#host.
type monPlatform struct {
	Mark   string // healthy, failed or idle
	Line   string
	Meters []monMeter
	Room   *monRoom
}

// platformOf builds the panel from the host's numbers. A host that can't be read (Nomad refuses or
// is silent, or the newest reading is more than 5 minutes old) says so and draws no meters: the
// home is for what is true now. One that was never read says it is waiting.
func platformOf(h service.HostView) *monPlatform {
	switch {
	case h.SampledAt == nil && h.Sources.Nomad != service.SamplerDenied && h.Sources.Nomad != service.SamplerUnreachable:
		return &monPlatform{Mark: "idle", Line: "Waiting for the first reading of the host"}
	case h.SampledAt == nil || h.Stale || h.Sources.Nomad == service.SamplerDenied || h.Sources.Nomad == service.SamplerUnreachable:
		return &monPlatform{Mark: "failed", Line: "Can't read the host's numbers"}
	}
	p := &monPlatform{Mark: "healthy", Line: "1 server up"}
	if m := memoryMeter(h); m != nil {
		p.Meters = append(p.Meters, *m)
	}
	if len(h.Disks) > 0 {
		fuller := h.Disks[0]
		for _, d := range h.Disks[1:] {
			if d.UsedPercent > fuller.UsedPercent {
				fuller = d
			}
		}
		// One disk meter, so its readout carries no mount.
		p.Meters = append(p.Meters, diskMeters(service.HostView{Disks: []service.HostDisk{fuller}})...)
	}
	p.Room = roomOf(h)
	return p
}

// partPlatform answers GET /parts/platform: the Platform panel alone.
func (s *server) partPlatform(w http.ResponseWriter, r *http.Request, session string) {
	p, err := s.loadPlatform(r)
	if err != nil {
		s.log.Error("read the host", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "apps", "platform-region", p)
}

// loadPlatform reads the host for the panel (the store only, never Nomad).
func (s *server) loadPlatform(r *http.Request) (*monPlatform, error) {
	h, err := s.svc.Host(r.Context())
	if err != nil {
		return nil, err
	}
	return platformOf(h), nil
}
