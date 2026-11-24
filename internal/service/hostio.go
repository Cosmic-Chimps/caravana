package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Network and disk I/O per server (feature 055, plan D1-D12). Each managed server's node agent reports
// cumulative counters from /proc in its sync (the io block of the report). The console keeps the
// previous sample of each server in memory and turns two of them into rates; the sampler (025) writes
// each server's newest rates into its host row once a minute; Host and the Monitoring and server pages
// read them back. Nothing here reads Nomad or the host: an unmanaged node (the lab's) has no agent, so no
// rates, and says so.

// The limits and the timings of the rates.
const (
	// maxIONames is the most interfaces and the most devices a report holds.
	maxIOInterfaces = 4
	maxIODevices    = 4
	// maxIOName is the longest interface or device name.
	maxIOName = 64
	// ioClockSlack is how far an io block's time may be from the console's before the block is dropped.
	ioClockSlack = 10 * time.Minute
	// ioMinGap and ioMaxGap bound the time between two samples a rate is made from: closer than 20 seconds
	// the counters have barely moved (and the timer isn't that quick), further than 2 minutes the average
	// says little.
	ioMinGap = 20 * time.Second
	ioMaxGap = 120 * time.Second
	// ioStaleAfter is how old a server's newest rates or last report may be before the page stops showing
	// them as now's.
	ioStaleAfter = 3 * time.Minute
)

// The reasons a server has no I/O numbers to show.
const (
	// IONoAgent: the server isn't managed by Caravana, so nothing reports for it.
	IONoAgent = "no_agent"
	// IOAgentTooOld: the agent syncs but its report has no io block (an older agent, or /proc unreadable).
	IOAgentTooOld = "agent_too_old"
	// IOStale: the agent hasn't synced for a while; Since says since when.
	IOStale = "stale"
	// IOWaiting: the agent syncs and the first rates aren't ready (one sample so far, a gap too long, or a
	// counter that reset).
	IOWaiting = "waiting"
)

// NodeIO is the io block of a sync's report (agent contract: nodeagent.IOReport): counters since boot, read
// at At (Unix milliseconds).
type NodeIO struct {
	At    int64        `json:"at"`
	Net   []NodeIONet  `json:"net"`
	Disks []NodeIODisk `json:"disks"`
	// Invalid is set when the block didn't decode (a counter that isn't a number): it is dropped, and the
	// sync that carried it goes on.
	Invalid bool `json:"-"`
}

// NodeIONet is one interface's bytes since boot. Kind is "default" (the default route's interface) or "mesh".
type NodeIONet struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	RxBytes int64  `json:"rxBytes"`
	TxBytes int64  `json:"txBytes"`
}

// NodeIODisk is one device's bytes read and written and milliseconds busy since boot. Holds is "nomad",
// "docker" or both.
type NodeIODisk struct {
	Name       string   `json:"name"`
	Holds      []string `json:"holds"`
	ReadBytes  int64    `json:"readBytes"`
	WriteBytes int64    `json:"writeBytes"`
	BusyMs     int64    `json:"busyMs"`
}

// UnmarshalJSON reads the block and, when it doesn't read, marks it invalid instead of failing the whole
// report: a block the console can't use is dropped, never a reason to refuse a sync.
func (n *NodeIO) UnmarshalJSON(b []byte) error {
	type plain NodeIO
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		*n = NodeIO{Invalid: true}
		return nil
	}
	*n = NodeIO(p)
	return nil
}

// checkIO says why an io block can't be used, "" when it can. It names the first thing wrong and never the
// value (a name is the agent's text).
func checkIO(io *NodeIO, now time.Time) string {
	switch {
	case io.Invalid:
		return "the block didn't decode"
	case io.At <= 0 || io.At < now.Add(-ioClockSlack).UnixMilli() || io.At > now.Add(ioClockSlack).UnixMilli():
		return "the time is more than 10 minutes from the console's"
	case len(io.Net) > maxIOInterfaces:
		return "too many interfaces"
	case len(io.Disks) > maxIODevices:
		return "too many devices"
	}
	seen := map[string]bool{}
	for _, n := range io.Net {
		if checkTextMax("name", n.Name, maxIOName) != nil || n.Name == "" || seen["net:"+n.Name] {
			return "an interface name isn't usable"
		}
		seen["net:"+n.Name] = true
		if n.Kind != "default" && n.Kind != "mesh" {
			return "an interface kind isn't known"
		}
		if n.RxBytes < 0 || n.TxBytes < 0 {
			return "a counter is negative"
		}
	}
	for _, d := range io.Disks {
		if checkTextMax("name", d.Name, maxIOName) != nil || d.Name == "" || seen["disk:"+d.Name] {
			return "a device name isn't usable"
		}
		seen["disk:"+d.Name] = true
		if len(d.Holds) > 2 || slices.ContainsFunc(d.Holds, func(h string) bool { return h != "nomad" && h != "docker" }) {
			return "a device holds something unknown"
		}
		if d.ReadBytes < 0 || d.WriteBytes < 0 || d.BusyMs < 0 {
			return "a counter is negative"
		}
	}
	return ""
}

// ---- the rates --------------------------------------------------------------------------------

// IONetRate is one interface's rates, in bytes per second.
type IONetRate struct {
	Name, Kind   string
	RxBps, TxBps int64
}

// IODiskRate is one device's rates: bytes per second and the share of the time it was busy (0 to 100).
type IODiskRate struct {
	Name        string
	Holds       []string
	ReadBps     int64
	WriteBps    int64
	BusyPercent int64
}

// IORates are the rates between two samples of one server. The headline numbers are nil when what they
// are made of isn't there: the network ones are the default-route interface's alone (the mesh's bytes
// leave on that interface too, so adding them would count them twice), the disk ones are summed over the
// watched devices (busy is the busiest one's).
type IORates struct {
	// At is the console's time when the rates were made.
	At    time.Time
	Net   []IONetRate
	Disks []IODiskRate

	NetRxBps, NetTxBps        *int64
	DiskReadBps, DiskWriteBps *int64
	DiskBusyPercent           *int64
}

// ioRates makes the rates between prev and cur, when the samples are 20 to 120 seconds apart. An
// interface or device whose counter went down (a reset: a reboot, a driver reloaded) or that isn't in
// both samples has no rate. ok is false when no rate could be made at all.
func ioRates(prev, cur *NodeIO) (r IORates, ok bool) {
	gap := time.Duration(cur.At-prev.At) * time.Millisecond
	if gap < ioMinGap || gap > ioMaxGap {
		return IORates{}, false
	}
	ms := float64(cur.At - prev.At)
	perSec := func(delta int64) int64 { return int64(math.Round(float64(delta) * 1000 / ms)) }

	for _, c := range cur.Net {
		p := slices.IndexFunc(prev.Net, func(n NodeIONet) bool { return n.Name == c.Name && n.Kind == c.Kind })
		if p < 0 || c.RxBytes < prev.Net[p].RxBytes || c.TxBytes < prev.Net[p].TxBytes {
			continue
		}
		rate := IONetRate{Name: c.Name, Kind: c.Kind, RxBps: perSec(c.RxBytes - prev.Net[p].RxBytes), TxBps: perSec(c.TxBytes - prev.Net[p].TxBytes)}
		r.Net = append(r.Net, rate)
		if c.Kind == "default" {
			r.NetRxBps, r.NetTxBps = new(rate.RxBps), new(rate.TxBps)
		}
	}
	var read, write, busy int64
	for _, c := range cur.Disks {
		p := slices.IndexFunc(prev.Disks, func(d NodeIODisk) bool { return d.Name == c.Name })
		if p < 0 || c.ReadBytes < prev.Disks[p].ReadBytes || c.WriteBytes < prev.Disks[p].WriteBytes || c.BusyMs < prev.Disks[p].BusyMs {
			continue
		}
		rate := IODiskRate{Name: c.Name, Holds: slices.Clone(c.Holds),
			ReadBps: perSec(c.ReadBytes - prev.Disks[p].ReadBytes), WriteBps: perSec(c.WriteBytes - prev.Disks[p].WriteBytes),
			BusyPercent: min(100, int64(math.Round(float64(c.BusyMs-prev.Disks[p].BusyMs)*100/ms)))}
		r.Disks = append(r.Disks, rate)
		read, write, busy = read+rate.ReadBps, write+rate.WriteBps, max(busy, rate.BusyPercent)
	}
	if len(r.Disks) > 0 {
		r.DiskReadBps, r.DiskWriteBps, r.DiskBusyPercent = &read, &write, &busy
	}
	if len(r.Net) == 0 && len(r.Disks) == 0 {
		return IORates{}, false
	}
	return r, true
}

// ---- what the console keeps -------------------------------------------------------------------

// serverIO is what is kept of one server's agent.
type serverIO struct {
	lastSync time.Time // the console's time of the newest sync
	hasIO    bool      // the newest sync had a usable io block
	prev     *NodeIO   // the newest usable block, the next sync's starting point
	rates    *IORates  // the newest rates; nil while there are none
}

// hostIOState is every server's, by server index. It is in memory only: after a restart the first sync is
// a starting point and the rates come with the second.
type hostIOState struct {
	mu      sync.Mutex
	servers map[int]*serverIO
}

// noteIO records a sync of server index at now, with the report's io block (nil when it had none). A block
// that doesn't pass checkIO is dropped (the reason is returned for the log) and counts as none.
func (st *hostIOState) noteIO(index int, io *NodeIO, now time.Time) (dropped string) {
	if io != nil {
		dropped = checkIO(io, now)
		if dropped != "" {
			io = nil
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.servers == nil {
		st.servers = map[int]*serverIO{}
	}
	sv := st.servers[index]
	if sv == nil {
		sv = &serverIO{}
		st.servers[index] = sv
	}
	sv.lastSync, sv.hasIO = now, io != nil
	if io == nil {
		sv.prev, sv.rates = nil, nil
		return dropped
	}
	sv.rates = nil // what was known before isn't, unless this pair makes new rates
	if sv.prev != nil {
		if r, ok := ioRates(sv.prev, io); ok {
			r.At = now
			sv.rates = &r
		}
	}
	sv.prev = io
	return dropped
}

// forget drops a server's numbers (it was removed).
func (st *hostIOState) forget(index int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.servers, index)
}

// view says what a server's I/O is now, for the pages and the API: the rates when they are fresh, else why
// there are none. index 0 is a node Caravana doesn't manage.
func (st *hostIOState) view(index int, now time.Time) HostIO {
	if index <= 0 {
		return HostIO{Reason: IONoAgent}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	sv := st.servers[index]
	switch {
	case sv == nil:
		return HostIO{Reason: IOWaiting}
	case now.Sub(sv.lastSync) > ioStaleAfter:
		since := sv.lastSync
		return HostIO{Reason: IOStale, Since: &since}
	case !sv.hasIO:
		return HostIO{Reason: IOAgentTooOld}
	case sv.rates == nil || now.Sub(sv.rates.At) > ioStaleAfter:
		return HostIO{Reason: IOWaiting}
	}
	r := sv.rates
	at := r.At
	return HostIO{At: &at, Net: slices.Clone(r.Net), Disks: slices.Clone(r.Disks),
		NetRxBps: r.NetRxBps, NetTxBps: r.NetTxBps, DiskReadBps: r.DiskReadBps, DiskWriteBps: r.DiskWriteBps,
		DiskBusyPercent: r.DiskBusyPercent}
}

// HostIO is what Host says about a server's network and disk I/O now. Reason is "" when the numbers are
// known, else one of IONoAgent, IOAgentTooOld, IOStale and IOWaiting; Since is when a stale agent last
// reported. A headline number is nil when it isn't known (never 0).
type HostIO struct {
	Reason string
	Since  *time.Time
	// At is when the rates were made.
	At    *time.Time
	Net   []IONetRate
	Disks []IODiskRate

	NetRxBps, NetTxBps        *int64
	DiskReadBps, DiskWriteBps *int64
	DiskBusyPercent           *int64
}

// Known says the rates are fresh.
func (h HostIO) Known() bool { return h.Reason == "" }

// ioColumns puts a server's fresh headline rates in a host row; a server with none leaves NULLs.
func (st *hostIOState) ioColumns(index int, now time.Time, h *store.HostSample) {
	v := st.view(index, now)
	if !v.Known() {
		return
	}
	h.NetRxBps, h.NetTxBps = v.NetRxBps, v.NetTxBps
	h.DiskReadBps, h.DiskWriteBps, h.DiskBusyPct = v.DiskReadBps, v.DiskWriteBps, v.DiskBusyPercent
}

// ioOfServer is HostIO of the server at index, read now.
func (s *Service) ioOfServer(index int) HostIO { return s.hostIO.view(index, s.clock.Now().UTC()) }

// ---- the series Monitoring draws --------------------------------------------------------------

// HostIOPoint is one step of the I/O chart: each number the median of the host rows that fall in the step,
// nil for a gap (no agent, or no row).
type HostIOPoint struct {
	T                                                              time.Time
	NetRxBps, NetTxBps, DiskReadBps, DiskWriteBps, DiskBusyPercent *float64
}

// HostIOSeries is a server's network and disk rates over a range of Monitoring. Known is false when no
// point has a number, so the section says why instead of drawing an empty chart.
type HostIOSeries struct {
	Range  metrics.Range
	Points []HostIOPoint
	Known  bool
}

// HostIOSeries reads a server's I/O rates over rng from the host rows the sampler wrote: minute rows for
// the 1 h and 6 h ranges, 10-minute rows (plus the slot still open) for 24 h and 7 d. server "" is the
// server Caravana runs on.
func (s *Service) HostIOSeries(ctx context.Context, server string, rng metrics.Range) (HostIOSeries, error) {
	now := s.clock.Now().UTC()
	dir, err := s.directory(ctx)
	if err != nil {
		return HostIOSeries{}, fmt.Errorf("read the host: %w", err)
	}
	node, target, err := s.hostTarget(ctx, dir, server)
	if err != nil {
		return HostIOSeries{}, err
	}
	out := HostIOSeries{Range: rng, Points: make([]HostIOPoint, rng.N())}
	from := rng.From(now)
	for i := range out.Points {
		out.Points[i].T = from.Add(time.Duration(i) * rng.Step)
	}
	if target == targetNoNode {
		return out, nil
	}
	to := now.Add(time.Minute)
	var rows []store.HostSample
	if rng.FromSlots {
		stored, err := s.store.HostRows(ctx, store.SlotRes, from, to)
		if err != nil {
			return HostIOSeries{}, fmt.Errorf("read the host: %w", err)
		}
		minutes, err := s.store.HostRows(ctx, store.MinuteRes, now.Add(-2*time.Hour), to)
		if err != nil {
			return HostIOSeries{}, fmt.Errorf("read the host: %w", err)
		}
		have := map[string]bool{}
		for _, r := range stored {
			have[r.Node+"|"+r.TS.Format(time.RFC3339)] = true
		}
		var pending []store.HostSample
		for _, m := range minutes {
			if !have[m.Node+"|"+m.TS.Truncate(metrics.SlotLength).Format(time.RFC3339)] {
				pending = append(pending, m)
			}
		}
		rows = append(stored, foldHostSlots(pending)...)
	} else {
		rows, err = s.store.HostRows(ctx, store.MinuteRes, from, to)
		if err != nil {
			return HostIOSeries{}, fmt.Errorf("read the host: %w", err)
		}
	}
	nodeID := node.ID
	if nodeID == "" && len(rows) > 0 {
		nodeID = rows[len(rows)-1].Node
	}
	groups := make([][]store.HostSample, rng.N())
	for _, r := range rows {
		if r.Node != nodeID || r.TS.Before(from) {
			continue
		}
		if i := int(r.TS.Sub(from) / rng.Step); i < len(groups) {
			groups[i] = append(groups[i], r)
		}
	}
	pick := func(g []store.HostSample, get func(store.HostSample) *int64) *float64 {
		var vs []int64
		for _, h := range g {
			if v := get(h); v != nil {
				vs = append(vs, *v)
			}
		}
		if m := medianInt(vs); m != nil {
			return new(float64(*m))
		}
		return nil
	}
	for i, g := range groups {
		p := &out.Points[i]
		p.NetRxBps = pick(g, func(h store.HostSample) *int64 { return h.NetRxBps })
		p.NetTxBps = pick(g, func(h store.HostSample) *int64 { return h.NetTxBps })
		p.DiskReadBps = pick(g, func(h store.HostSample) *int64 { return h.DiskReadBps })
		p.DiskWriteBps = pick(g, func(h store.HostSample) *int64 { return h.DiskWriteBps })
		p.DiskBusyPercent = pick(g, func(h store.HostSample) *int64 { return h.DiskBusyPct })
		if p.NetRxBps != nil || p.NetTxBps != nil || p.DiskReadBps != nil || p.DiskWriteBps != nil || p.DiskBusyPercent != nil {
			out.Known = true
		}
	}
	return out, nil
}

// foldHostSlots folds minute rows into one row per node and 10-minute slot, as the sampler's fold does.
func foldHostSlots(rows []store.HostSample) []store.HostSample {
	bySlot := map[time.Time][]store.HostSample{}
	for _, r := range rows {
		slot := r.TS.Truncate(metrics.SlotLength)
		bySlot[slot] = append(bySlot[slot], r)
	}
	slots := make([]time.Time, 0, len(bySlot))
	for sl := range bySlot {
		slots = append(slots, sl)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Before(slots[j]) })
	var out []store.HostSample
	for _, sl := range slots {
		out = append(out, foldHosts(sl, bySlot[sl])...)
	}
	return out
}
