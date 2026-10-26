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
// puts the fill and the readout in the warn hue (above 90 %, or a disk past its threshold); a disk
// in a state also has its mark ahead of the readout, so the hue is never the only sign.
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
		v.Room = roomOf(h.Room)
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

// meterFill is how much of a 0-100 box used takes of total.
func meterFill(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return max(0, min(100, used/total*100))
}

// memoryMeter reads "1.9/3.8G": used over total, with what is reserved hatched.
func memoryMeter(h service.HostView) *monMeter {
	r := h.Memory
	if r.Used == nil || r.Total == nil || *r.Total <= 0 {
		return nil
	}
	used, total := float64(*r.Used), float64(*r.Total)
	m := &monMeter{Label: "mem", Fill: units(meterFill(used, total)), Read: gbText(used) + "/" + gbText(total) + "G"}
	m.Warn = used/total*100 > metrics.MemoryPercent
	m.Title = sizeText(*r.Used) + " in use"
	if r.Reserved != nil {
		m.HasReserved, m.ResW = true, units(meterFill(float64(*r.Reserved), total))
		m.Title += ", " + sizeText(*r.Reserved) + " reserved"
	}
	m.Title += " of " + sizeText(*r.Total)
	return m
}

// cpuMeter reads "1.2/4 vCPU" (in MHz when the cores aren't known).
func cpuMeter(h service.HostView) *monMeter {
	r := h.CPU
	if r.Used == nil || r.Total == nil || *r.Total <= 0 {
		return nil
	}
	used, total := float64(*r.Used), float64(*r.Total)
	m := &monMeter{Label: "cpu", Fill: units(meterFill(used, total))}
	m.Warn = used/total*100 > metrics.MemoryPercent
	if r.Cores != nil && *r.Cores > 0 {
		perCore := total / float64(*r.Cores)
		m.Read = trim1(used/perCore) + "/" + strconv.FormatInt(*r.Cores, 10) + " vCPU"
	} else {
		m.Read = strconv.FormatInt(*r.Used, 10) + "/" + strconv.FormatInt(*r.Total, 10) + " MHz"
	}
	m.Title = strconv.FormatInt(*r.Used, 10) + " MHz in use"
	if r.Reserved != nil {
		m.HasReserved, m.ResW = true, units(meterFill(float64(*r.Reserved), total))
		m.Title += ", " + strconv.FormatInt(*r.Reserved, 10) + " MHz reserved"
	}
	m.Title += " of " + strconv.FormatInt(*r.Total, 10) + " MHz"
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
func roomOf(r service.HostRoom) *monRoom {
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
		out.Text = msentence(mtext("Room for about "+strconv.Itoa(r.Count)+" more apps like "), mname(name),
			mtext(" ("+sizeText(r.MemoryMB)+", "+strconv.FormatInt(r.CPUMHz, 10)+" MHz each)."))
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
	g := geom{n: n, ymax: top * 1.04}

	plot := &monPlot{Label: "Memory over the last 7 days: what is used and what is reserved"}
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
	p.Room = roomOf(h.Room)
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
