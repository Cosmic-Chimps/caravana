package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The host's capacity (feature 025): what the newest stored host sample says,
// the filesystems it watches, how many more apps like the usual one fit, and
// seven days of memory. It reads the store only and never calls Nomad, so the
// Apps home's Platform panel doesn't wait on it.

// HostResource is one resource of the host: what is used, what is reserved
// (Nomad places by reservation) and the total. Each is nil when the sampler
// couldn't read it. Memory is in megabytes, CPU in MHz.
type HostResource struct {
	Used, Reserved, Total *int64
}

// HostCPU is the host's CPU with its cores, for the vCPU readout.
type HostCPU struct {
	HostResource
	Cores *int64
}

// HostDisk is one watched filesystem of the newest sample.
type HostDisk struct {
	Mount string
	// Holds says what is on it: "nomad" (copies' files and host volumes),
	// "docker" (images), or both.
	Holds                   []string
	UsedMB, TotalMB, FreeMB int64
	// UsedPercent is used / (used + free), what df prints.
	UsedPercent float64
	// State is the meter's: ok below 85 %, warning from 85, failed from 95,
	// and at least warning while the filesystem's disk_nearly_full item is open
	// (it stays open down to 83 %).
	State metrics.DiskLevel
}

// HostRoom is "room for about N more apps like X". Known is false when the
// host's totals or reservations weren't sampled, and nothing can be said.
type HostRoom struct {
	Known bool
	metrics.RoomResult
	// LikeAppName is the yardstick app's name (LikeApp is its ID).
	LikeAppName string
	// MemoryUsedMB and MemoryTotalMB, CPUUsedMHz and CPUTotalMHz are what the room is counted over: the sums
	// of the ready servers' newest samples (feature 031; with one server, that server's own, as Memory and
	// CPU say). nil when no sample had them. A page that says "memory in use is already at 89%" beside the
	// room, or the cluster's use in a stats line, reads these, never one server's.
	MemoryUsedMB, MemoryTotalMB, CPUUsedMHz, CPUTotalMHz *int64
}

// HostHistory is the host's memory over seven days, an hour per point (the
// median of the hour's rows); nil is a gap.
type HostHistory struct {
	Points                     []time.Time
	MemoryUsed, MemoryReserved []*float64
}

// HostView is what getHost answers.
type HostView struct {
	// Server is the name of the server the numbers are of: the one asked for, or the one Caravana runs on.
	// "" when the cluster's servers are not known (Nomad could not be read and no tick has read it).
	Server string
	// OSRead says the view is of the server Caravana runs on, the only one whose operating system
	// Caravana reads (feature 027: the OS block, boots and Caravana's versions). Node and Reboot are
	// nil when it is false.
	OSRead bool
	// SampledAt is the newest sample's time; nil when the host was never read.
	SampledAt *time.Time
	// Stale says the newest sample is older than 5 minutes: the numbers are
	// the last known ones, not now's.
	Stale    bool
	Memory   HostResource
	CPU      HostCPU
	Disks    []HostDisk
	Room     HostRoom
	History  HostHistory
	Baseline MetricsBaseline
	Sources  SamplerSources
	// Node is what Nomad says about the node, from the sampler's newest tick
	// (feature 027); nil before its first reading, and after a restart until the
	// next tick.
	Node *HostNode
	// Reboot is what the host's files say about a pending reboot, from the
	// sampler's newest tick; nil before its first reading (feature 027).
	Reboot *hostos.Reboot
}

// hostHistoryHours is the history's span: seven days of hourly points.
const hostHistoryHours = 7 * 24

// Host reads one server's numbers from the store. server is the server's name; "" is the server
// Caravana runs on. A name no server has is ErrServerNotFound. A server that has no Nomad node yet (it
// is joining) gives a view with no numbers.
func (s *Service) Host(ctx context.Context, server string) (HostView, error) {
	now := s.clock.Now().UTC()
	baseline, err := s.metricsBaseline(ctx, now)
	if err != nil {
		return HostView{}, err
	}
	dir, err := s.directory(ctx)
	if err != nil {
		return HostView{}, fmt.Errorf("read the host: %w", err)
	}
	node, target, err := s.hostTarget(ctx, dir, server)
	if err != nil {
		return HostView{}, err
	}
	view := HostView{Baseline: baseline, Sources: s.SamplerSources(), Server: node.Name}
	// The OS block is the server Caravana runs on. With no nodes known the view is that server's, as
	// it was before there were several.
	view.OSRead = target == targetNewest || node.Local
	if view.OSRead {
		view.Node, view.Reboot = s.hostNode(), s.hostReboot()
	}

	if target == targetNoNode {
		view.History, err = s.hostHistory(ctx, now, "")
		if err != nil {
			return HostView{}, err
		}
		return view, nil // joined, but Nomad has no node for it yet: nothing was sampled
	}
	hist, err := s.hostHistory(ctx, now, node.ID)
	if err != nil {
		return HostView{}, err
	}
	view.History = hist

	var h store.HostSample
	if node.ID != "" {
		h, err = s.store.NewestHostOf(ctx, node.ID)
	} else {
		h, err = s.store.NewestHost(ctx)
	}
	if errors.Is(err, store.ErrNotFound) {
		return view, nil // never sampled: no numbers, and the page says so
	}
	if err != nil {
		return HostView{}, fmt.Errorf("read the host: %w", err)
	}
	at := h.TS
	view.SampledAt, view.Stale = &at, metrics.Stale(h.TS, now)
	view.Memory = HostResource{Used: h.MemoryUsedMB, Reserved: h.MemoryReservedMB, Total: h.MemoryTotalMB}
	view.CPU = HostCPU{HostResource: HostResource{Used: h.CPUUsedMHz, Reserved: h.CPUReservedMHz, Total: h.CPUTotalMHz}, Cores: h.CPUCores}

	disks, err := s.store.DiskRows(ctx, store.MinuteRes, h.TS, h.TS.Add(time.Second))
	if err != nil {
		return HostView{}, fmt.Errorf("read the host: %w", err)
	}
	var mine []store.DiskSample
	for _, d := range disks {
		if d.Node == h.Node {
			mine = append(mine, d)
		}
	}
	// While a disk_nearly_full item is open the meter shows its state, so the
	// meter and the needs-you item never disagree (an item opened at 86 % stays
	// open, a warning, until the disk reads under 83 %). The item isn't judged
	// from a stale sample, so a stale host's meters go by the percent alone.
	var history []store.DiskSample
	if !view.Stale {
		if history, err = s.diskHistory(ctx, now, h.TS, mine); err != nil {
			return HostView{}, fmt.Errorf("read the host: %w", err)
		}
	}
	for _, d := range mine {
		pct := metrics.UsedPercent(d.UsedMB, d.FreeMB)
		state := metrics.DiskState(pct, false).State
		if !view.Stale {
			if open, _ := diskRun(d.Mount, d, history); open {
				state = metrics.DiskState(pct, true).Level
			}
		}
		view.Disks = append(view.Disks, HostDisk{
			Mount: d.Mount, Holds: strings.Split(d.Holds, ","), UsedMB: d.UsedMB, TotalMB: d.TotalMB, FreeMB: d.FreeMB,
			UsedPercent: pct, State: state,
		})
	}

	view.Room, err = s.hostRoom(ctx, dir, h)
	if err != nil {
		return HostView{}, err
	}
	return view, nil
}

// hostTargetKind says how a read found its node.
type hostTargetKind int

const (
	// targetNode: the directory has the node.
	targetNode hostTargetKind = iota
	// targetNewest: no server was asked for and the directory has none (Nomad couldn't be read and
	// nothing was remembered; a test with no nodes): the read is of the newest sample, as it was
	// before there were several servers.
	targetNewest
	// targetNoNode: the named server has a row and no Nomad node yet.
	targetNoNode
)

// hostTarget finds the node a Host or HostCopies read is about. server "" is the server Caravana
// runs on; a name no server has is ErrServerNotFound.
func (s *Service) hostTarget(ctx context.Context, dir serverDirectory, server string) (serverNode, hostTargetKind, error) {
	if server == "" {
		if n, ok := dir.local(); ok {
			return n, targetNode, nil
		}
		return serverNode{}, targetNewest, nil
	}
	if n, ok := dir.byName(server); ok {
		return n, targetNode, nil
	}
	// A server that joined and has no node yet is a server with nothing to show.
	if _, err := s.store.ServerByName(ctx, server); err == nil {
		return serverNode{Name: server}, targetNoNode, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return serverNode{}, targetNode, fmt.Errorf("read the host: %w", err)
	}
	return serverNode{}, targetNode, &RefusalError{Field: "server", Err: ErrServerNotFound}
}

// hostRoom counts what fits by reservation, for the cluster: the totals of every ready server in the
// pool "default" (the newest host sample of each) less what is reserved, against the yardstick app
// of the apps running as the samples' neighbours say (the newest minute row of each stored service,
// its reservations summed per app). A server with no complete sample adds nothing. With no nodes
// known, or one, the room is h's alone, as it was with one server. viewed is the sample the page is
// about; it sets the time the apps' rows are read at when the cluster has no newer sample.
func (s *Service) hostRoom(ctx context.Context, dir serverDirectory, viewed store.HostSample) (HostRoom, error) {
	free, err := s.clusterFree(ctx, dir, viewed)
	if err != nil {
		return HostRoom{}, err
	}
	if !free.known {
		return HostRoom{}, nil
	}
	h := store.HostSample{TS: free.at}
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return HostRoom{}, fmt.Errorf("read the host: %w", err)
	}
	series, err := s.store.ListMetricsSeries(ctx)
	if err != nil {
		return HostRoom{}, fmt.Errorf("read the host: %w", err)
	}
	ids := map[serviceRef]int64{}
	for _, m := range series {
		ids[serviceRef{m.AppID, m.Service}] = m.ID
	}

	var apps []metrics.AppReservation
	names := map[string]string{}
	for _, rec := range recs {
		if rec.StoppedAt != nil {
			continue
		}
		var sum metrics.AppReservation
		running := false
		for _, svc := range rec.App.Services {
			id, ok := ids[serviceRef{rec.App.ID, svc.Name}]
			if !ok {
				continue
			}
			rows, err := s.store.MinuteRows(ctx, id, h.TS.Add(-liveRowAge), h.TS.Add(time.Minute))
			if err != nil {
				return HostRoom{}, fmt.Errorf("read the host: %w", err)
			}
			if len(rows) == 0 {
				continue
			}
			r := rows[len(rows)-1] // the newest
			if r.Copies == nil || *r.Copies <= 0 {
				continue
			}
			running = true
			if r.MemoryReservedMB != nil {
				sum.MemoryMB += *r.MemoryReservedMB
			}
			if r.CPUReservedMHz != nil {
				sum.CPUMHz += *r.CPUReservedMHz
			}
		}
		if running {
			sum.App = rec.App.ID
			apps = append(apps, sum)
			names[rec.App.ID] = rec.Place.Name
		}
	}

	res := metrics.Room(apps, free.memoryMB, free.cpuMHz)
	return HostRoom{Known: true, RoomResult: res, LikeAppName: names[res.LikeApp],
		MemoryUsedMB: free.memUsed, MemoryTotalMB: free.memTotal, CPUUsedMHz: free.cpuUsed, CPUTotalMHz: free.cpuTotal}, nil
}

// clusterFreeRoom is what the cluster has left to reserve.
type clusterFreeRoom struct {
	known            bool
	memoryMB, cpuMHz int64
	at               time.Time // the newest sample that counted
	// The sums of used and total over the servers that counted, for the cluster's percentages.
	memUsed, memTotal, cpuUsed, cpuTotal *int64
}

// clusterFree sums, over the ready servers of the pool "default", the totals less the reservations of
// each one's newest sample. Without a directory (or with one node) it is viewed's own, as before.
func (s *Service) clusterFree(ctx context.Context, dir serverDirectory, viewed store.HostSample) (clusterFreeRoom, error) {
	var rows []store.HostSample
	if len(dir.nodes) <= 1 {
		rows = []store.HostSample{viewed}
	} else {
		all, err := s.store.NewestHosts(ctx)
		if err != nil {
			return clusterFreeRoom{}, fmt.Errorf("read the host: %w", err)
		}
		for _, h := range all {
			if n, ok := dir.byID(h.Node); ok && n.Ready && n.Pool == "default" {
				rows = append(rows, h)
			}
		}
	}
	var out clusterFreeRoom
	add := func(sum **int64, n int64) {
		if *sum == nil {
			*sum = new(int64)
		}
		**sum += n
	}
	for _, h := range rows {
		if h.MemoryUsedMB != nil && h.MemoryTotalMB != nil {
			add(&out.memUsed, *h.MemoryUsedMB)
			add(&out.memTotal, *h.MemoryTotalMB)
		}
		if h.CPUUsedMHz != nil && h.CPUTotalMHz != nil {
			add(&out.cpuUsed, *h.CPUUsedMHz)
			add(&out.cpuTotal, *h.CPUTotalMHz)
		}
		if h.MemoryTotalMB == nil || h.MemoryReservedMB == nil || h.CPUTotalMHz == nil || h.CPUReservedMHz == nil {
			continue
		}
		out.known = true
		out.memoryMB += *h.MemoryTotalMB - *h.MemoryReservedMB
		out.cpuMHz += *h.CPUTotalMHz - *h.CPUReservedMHz
		if h.TS.After(out.at) {
			out.at = h.TS
		}
	}
	return out, nil
}

// hostSlot is one 10-minute value of the host's memory.
type hostSlot struct {
	at             time.Time
	used, reserved *int64
}

// hostHistory makes the seven days of hourly memory: the stored 10-minute rows,
// plus the slot still in progress (or one a restart left unfolded) folded from
// its minute rows, each hour the median of its slots. nodeID is the node's; "" takes the node of the
// newest row.
func (s *Service) hostHistory(ctx context.Context, now time.Time, nodeID string) (HostHistory, error) {
	const hour = time.Hour
	from := now.Truncate(hour).Add(hour - hostHistoryHours*hour)
	out := HostHistory{
		Points:         make([]time.Time, hostHistoryHours),
		MemoryUsed:     make([]*float64, hostHistoryHours),
		MemoryReserved: make([]*float64, hostHistoryHours),
	}
	for i := range out.Points {
		out.Points[i] = from.Add(time.Duration(i) * hour)
	}

	to := now.Add(time.Minute)
	stored, err := s.store.HostRows(ctx, store.SlotRes, from, to)
	if err != nil {
		return HostHistory{}, fmt.Errorf("read the host: %w", err)
	}
	minutes, err := s.store.HostRows(ctx, store.MinuteRes, now.Add(-2*hour), to)
	if err != nil {
		return HostHistory{}, fmt.Errorf("read the host: %w", err)
	}
	if len(stored) == 0 && len(minutes) == 0 {
		return out, nil
	}
	node := nodeID
	switch {
	case node != "":
	case len(minutes) > 0:
		node = minutes[len(minutes)-1].Node
	default:
		node = stored[len(stored)-1].Node
	}

	var slots []hostSlot
	have := map[time.Time]bool{}
	for _, r := range stored {
		if r.Node == node {
			slots = append(slots, hostSlot{r.TS, r.MemoryUsedMB, r.MemoryReservedMB})
			have[r.TS.Truncate(metrics.SlotLength)] = true
		}
	}
	pending := map[time.Time][]store.HostSample{}
	for _, r := range minutes {
		if slot := r.TS.Truncate(metrics.SlotLength); r.Node == node && !have[slot] {
			pending[slot] = append(pending[slot], r)
		}
	}
	for slot, rs := range pending {
		var used, reserved []int64
		for _, r := range rs {
			if r.MemoryUsedMB != nil {
				used = append(used, *r.MemoryUsedMB)
			}
			if r.MemoryReservedMB != nil {
				reserved = append(reserved, *r.MemoryReservedMB)
			}
		}
		slots = append(slots, hostSlot{slot, medianInt(used), medianInt(reserved)})
	}

	perHour := make([][]hostSlot, hostHistoryHours)
	for _, sl := range slots {
		i := int(sl.at.Sub(from) / hour)
		if sl.at.Before(from) || i >= hostHistoryHours {
			continue
		}
		perHour[i] = append(perHour[i], sl)
	}
	for i, group := range perHour {
		var used, reserved []int64
		for _, sl := range group {
			if sl.used != nil {
				used = append(used, *sl.used)
			}
			if sl.reserved != nil {
				reserved = append(reserved, *sl.reserved)
			}
		}
		out.MemoryUsed[i], out.MemoryReserved[i] = int64Ptr(medianInt(used)), int64Ptr(medianInt(reserved))
	}
	return out, nil
}

// medianInt is the median of vs (an even count gives the mean of the two in
// the middle); nil for no values.
func medianInt(vs []int64) *int64 {
	if len(vs) == 0 {
		return nil
	}
	s := slices.Clone(vs)
	slices.Sort(s)
	n := len(s)
	m := s[n/2]
	if n%2 == 0 {
		m = s[n/2-1] + (s[n/2]-s[n/2-1]+1)/2
	}
	return &m
}
