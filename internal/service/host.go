package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
}

// HostHistory is the host's memory over seven days, an hour per point (the
// median of the hour's rows); nil is a gap.
type HostHistory struct {
	Points                     []time.Time
	MemoryUsed, MemoryReserved []*float64
}

// HostView is what getHost answers.
type HostView struct {
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
}

// hostHistoryHours is the history's span: seven days of hourly points.
const hostHistoryHours = 7 * 24

// Host reads the host's numbers from the store.
func (s *Service) Host(ctx context.Context) (HostView, error) {
	now := s.clock.Now().UTC()
	baseline, err := s.metricsBaseline(ctx, now)
	if err != nil {
		return HostView{}, err
	}
	view := HostView{Baseline: baseline, Sources: s.SamplerSources()}

	hist, err := s.hostHistory(ctx, now)
	if err != nil {
		return HostView{}, err
	}
	view.History = hist

	h, err := s.store.NewestHost(ctx)
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

	view.Room, err = s.hostRoom(ctx, h)
	if err != nil {
		return HostView{}, err
	}
	return view, nil
}

// hostRoom counts what fits by reservation: the totals of the newest host
// sample less what is reserved, against the yardstick app of the apps running
// as the same sample's neighbours say (the newest minute row of each stored
// service, its reservations summed per app).
func (s *Service) hostRoom(ctx context.Context, h store.HostSample) (HostRoom, error) {
	if h.MemoryTotalMB == nil || h.MemoryReservedMB == nil || h.CPUTotalMHz == nil || h.CPUReservedMHz == nil {
		return HostRoom{}, nil
	}
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

	res := metrics.Room(apps, *h.MemoryTotalMB-*h.MemoryReservedMB, *h.CPUTotalMHz-*h.CPUReservedMHz)
	return HostRoom{Known: true, RoomResult: res, LikeAppName: names[res.LikeApp]}, nil
}

// hostSlot is one 10-minute value of the host's memory.
type hostSlot struct {
	at             time.Time
	used, reserved *int64
}

// hostHistory makes the seven days of hourly memory: the stored 10-minute rows,
// plus the slot still in progress (or one a restart left unfolded) folded from
// its minute rows, each hour the median of its slots.
func (s *Service) hostHistory(ctx context.Context, now time.Time) (HostHistory, error) {
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
	node := ""
	if len(minutes) > 0 {
		node = minutes[len(minutes)-1].Node
	} else {
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
