package service

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func hostRow(node string, ts time.Time, used, reserved, total int64) store.HostSample {
	cpuUsed, cpuReserved, cpuTotal, cores := int64(1200), int64(300), int64(4000), int64(4)
	return store.HostSample{
		Node: node, TS: ts, MemoryUsedMB: &used, MemoryReservedMB: &reserved, MemoryTotalMB: &total,
		CPUUsedMHz: &cpuUsed, CPUReservedMHz: &cpuReserved, CPUTotalMHz: &cpuTotal, CPUCores: &cores,
	}
}

func diskRow(node string, ts time.Time, mount, holds string, used, free int64) store.DiskSample {
	return store.DiskSample{Node: node, TS: ts, Mount: mount, Holds: holds, UsedMB: used, FreeMB: free, TotalMB: used + free}
}

// reservation is a service's row with copies running and what they reserve.
func reservation(series int64, ts time.Time, copies, memMB, cpuMHz int64) store.MetricsSample {
	return store.MetricsSample{Series: series, TS: ts, Minutes: 1, Copies: &copies, MemoryReservedMB: &memMB, CPUReservedMHz: &cpuMHz}
}

func (f *metricsFixture) host(rows store.MetricsRows) {
	f.t.Helper()
	if err := f.e.store.WriteMetricsTick(f.ctx, store.MetricsTick{Minute: rows}); err != nil {
		f.t.Fatal(err)
	}
}

func TestHostNeverSampled(t *testing.T) {
	f := newMetricsFixture(t, time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC))
	v, err := f.e.svc.Host(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.SampledAt != nil || v.Stale || len(v.Disks) != 0 || v.Room.Known || v.Memory.Total != nil {
		t.Errorf("host = %+v, want nothing", v)
	}
	if len(v.History.Points) != 168 || len(v.History.MemoryUsed) != 168 || v.History.MemoryUsed[167] != nil {
		t.Errorf("history = %d points, last %v; want 168 gaps", len(v.History.Points), v.History.MemoryUsed[167])
	}
	if v.Baseline.State != metrics.Building || v.Sources.Nomad != SamplerUnknown {
		t.Errorf("baseline %+v sources %+v", v.Baseline, v.Sources)
	}
}

// The newest sample, its filesystems with their states, and the room for more
// apps by reservation, all from the store: Nomad being down changes nothing.
func TestHostFromTheStore(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	f.save(appWith("blog", 1))
	f.save(appWith("stopped", 1))
	web, blog, stopped := f.series("whoami", "web"), f.series("blog", "web"), f.series("stopped", "web")
	if err := f.e.store.SetAppStopped(f.ctx, "stopped", &now); err != nil {
		t.Fatal(err)
	}
	ts := now.Truncate(time.Minute).Add(-time.Minute)
	f.host(store.MetricsRows{
		Hosts: []store.HostSample{hostRow("n1", ts.Add(-time.Minute), 1000, 900, 4096), hostRow("n1", ts, 1946, 1000, 4096)},
		Disks: []store.DiskSample{
			diskRow("n1", ts.Add(-time.Minute), "/", "nomad,docker", 1, 99),
			diskRow("n1", ts, "/", "nomad,docker", 18432, 22528),
			diskRow("n1", ts, "/data", "nomad", 9000, 1000), // 90 %
		},
		// whoami reserves 256 MB and 100 MHz, blog 512 and 200; the stopped app's
		// numbers must not count; the yardstick of the two is the lower middle.
		Samples: []store.MetricsSample{reservation(web, ts, 2, 256, 100), reservation(blog, ts, 1, 512, 200), reservation(stopped, ts, 0, 999, 999)},
	})
	f.e.nomad.Down() // the Platform panel never waits on Nomad

	v, err := f.e.svc.Host(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.SampledAt == nil || !v.SampledAt.Equal(ts) || v.Stale {
		t.Errorf("sampledAt %v stale %v, want %v fresh", v.SampledAt, v.Stale, ts)
	}
	if *v.Memory.Used != 1946 || *v.Memory.Reserved != 1000 || *v.Memory.Total != 4096 {
		t.Errorf("memory = %d/%d/%d", *v.Memory.Used, *v.Memory.Reserved, *v.Memory.Total)
	}
	if *v.CPU.Used != 1200 || *v.CPU.Reserved != 300 || *v.CPU.Total != 4000 || *v.CPU.Cores != 4 {
		t.Errorf("cpu = %+v", v.CPU)
	}
	if len(v.Disks) != 2 {
		t.Fatalf("disks = %+v, want the two of the newest sample only", v.Disks)
	}
	root, data := v.Disks[0], v.Disks[1]
	if root.Mount != "/" || len(root.Holds) != 2 || root.Holds[0] != "nomad" || root.Holds[1] != "docker" || root.UsedMB != 18432 ||
		root.FreeMB != 22528 || root.TotalMB != 40960 || root.UsedPercent != 45 || root.State != metrics.DiskOK {
		t.Errorf("/ = %+v", root)
	}
	if data.Mount != "/data" || data.UsedPercent != 90 || data.State != metrics.DiskWarning || len(data.Holds) != 1 {
		t.Errorf("/data = %+v", data)
	}
	r := v.Room
	if !r.Known || !r.HasYardstick {
		t.Fatalf("room = %+v", r)
	}
	// Two running apps reserving 256 and 512 MB (a service's row is already
	// summed over its copies): the lower middle is 256, whoami.
	if r.LikeApp != "whoami" || r.LikeAppName != "whoami" || r.MemoryMB != 256 || r.CPUMHz != 100 || r.LimitedBy != metrics.ResourceMemory {
		t.Errorf("yardstick = %s %q %d MB %d MHz by %s", r.LikeApp, r.LikeAppName, r.MemoryMB, r.CPUMHz, r.LimitedBy)
	}
	if want := (4096 - 1000) / 256; r.Count != want || r.FreeMemoryMB != 3096 {
		t.Errorf("room = %d more with %d MB free, want %d and 3096", r.Count, r.FreeMemoryMB, want)
	}
}

func TestHostStaleAndRoomCases(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	web := f.series("whoami", "web")
	ts := time.Date(2026, 10, 25, 11, 55, 30, 0, time.UTC).Truncate(time.Minute) // 11:55
	f.host(store.MetricsRows{Hosts: []store.HostSample{hostRow("n1", ts, 1946, 1000, 4096)}})

	t.Run("exactly 5 minutes old is fresh, one second more is stale", func(t *testing.T) {
		f.e.svc.clock.(*fakeClock).Set(ts.Add(5 * time.Minute))
		if v, _ := f.e.svc.Host(f.ctx); v.Stale {
			t.Error("a sample 5 minutes old is stale")
		}
		f.e.svc.clock.(*fakeClock).Set(ts.Add(5*time.Minute + time.Second))
		v, _ := f.e.svc.Host(f.ctx)
		if !v.Stale || v.SampledAt == nil || *v.Memory.Used != 1946 {
			t.Errorf("stale = %v, the last known numbers must stay: %+v", v.Stale, v.Memory)
		}
	})

	t.Run("nothing running: no yardstick, the free memory to reserve", func(t *testing.T) {
		f.e.svc.clock.(*fakeClock).Set(ts.Add(time.Minute))
		v, _ := f.e.svc.Host(f.ctx)
		if !v.Room.Known || v.Room.HasYardstick || v.Room.FreeMemoryMB != 3096 {
			t.Errorf("room = %+v", v.Room)
		}
	})

	t.Run("a service with no copies isn't running", func(t *testing.T) {
		f.host(store.MetricsRows{Samples: []store.MetricsSample{reservation(web, ts, 0, 256, 100)}})
		v, _ := f.e.svc.Host(f.ctx)
		if v.Room.HasYardstick {
			t.Errorf("room = %+v", v.Room)
		}
	})

	t.Run("reservations that weren't sampled leave the room unknown", func(t *testing.T) {
		g := newMetricsFixture(t, now)
		row := hostRow("n1", ts, 1946, 0, 4096)
		row.MemoryReservedMB = nil // Nomad's allocation list failed that minute
		g.host(store.MetricsRows{Hosts: []store.HostSample{row}})
		v, err := g.e.svc.Host(g.ctx)
		if err != nil || v.Room.Known || v.Memory.Reserved != nil || *v.Memory.Used != 1946 {
			t.Errorf("host = %+v, %v; want the room unknown and reserved nil", v, err)
		}
	})

	t.Run("full: no room", func(t *testing.T) {
		g := newMetricsFixture(t, now)
		s := g.series("whoami", "web")
		g.host(store.MetricsRows{
			Hosts:   []store.HostSample{hostRow("n1", ts, 1946, 4200, 4096)}, // reserved above the total
			Samples: []store.MetricsSample{reservation(s, ts, 1, 256, 100)},
		})
		v, _ := g.e.svc.Host(g.ctx)
		if !v.Room.HasYardstick || v.Room.Count != 0 || v.Room.LimitedBy != metrics.ResourceMemory || v.Room.FreeMemoryMB != 0 {
			t.Errorf("room = %+v, want 0 and no free memory", v.Room)
		}
	})
}

// Seven days of hourly points: the median of an hour's slots, the slot in
// progress folded from its minute rows, and a gap where nothing was sampled.
func TestHostHistory(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 24, 30, 0, time.UTC)
	f := newMetricsFixture(t, now)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 25, h, m, 0, 0, time.UTC) }
	slot := func(ts time.Time, used, reserved int64) store.HostSample {
		return hostRow("n1", ts, used, reserved, 4096)
	}

	if err := f.e.store.WriteMetricsTick(f.ctx, store.MetricsTick{Fold: store.MetricsRows{Hosts: []store.HostSample{
		slot(at(10, 0), 100, 50), slot(at(10, 10), 300, 50), slot(at(10, 20), 200, 50), // 10:00 hour: median 200
		slot(at(9, 0), 10, 5), // 09:00 hour: one slot
		slot(at(11, 0), 400, 60), slot(at(11, 10), 410, 60), slot(at(11, 20), 420, 60),
		slot(at(11, 30), 430, 60), slot(at(11, 40), 440, 60), slot(at(11, 50), 450, 60), // median of 6: 425
	}}}); err != nil {
		t.Fatal(err)
	}
	// The slot in progress, 12:20, has no row of its own: three minute rows.
	f.host(store.MetricsRows{Hosts: []store.HostSample{
		slot(at(12, 20), 500, 70), slot(at(12, 21), 700, 70), slot(at(12, 22), 600, 70),
	}})

	v, err := f.e.svc.Host(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	h := v.History
	if len(h.Points) != 168 || !h.Points[167].Equal(at(12, 0)) || !h.Points[0].Equal(at(12, 0).Add(-167*time.Hour)) {
		t.Fatalf("points %d from %v to %v", len(h.Points), h.Points[0], h.Points[167])
	}
	point := func(hour int) int { return 167 - (12 - hour) }
	for _, tc := range []struct {
		hour           int
		used, reserved float64
	}{{9, 10, 5}, {10, 200, 50}, {11, 425, 60}, {12, 600, 70}} {
		i := point(tc.hour)
		if val(h.MemoryUsed[i]) != tc.used || val(h.MemoryReserved[i]) != tc.reserved {
			t.Errorf("%02d:00 = %v / %v, want %v / %v", tc.hour, val(h.MemoryUsed[i]), val(h.MemoryReserved[i]), tc.used, tc.reserved)
		}
	}
	if h.MemoryUsed[point(8)] != nil || h.MemoryUsed[0] != nil {
		t.Errorf("an hour nothing was sampled in must be a gap, got %v", h.MemoryUsed[point(8)])
	}
}

// While a disk_nearly_full item is open, the host's meter shows its state, so
// the meter and the needs-you item never disagree: opened at 86 %, the disk
// reads warning at 84 % (the item stays open down to 83 %), failed from 95 %,
// and ok again once the item resolves under 83 %. A disk that was never at
// 85 % is ok at 84 %.
func TestHostDiskStateFollowsTheOpenItem(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		percents []float64
		want     metrics.DiskLevel
		item     bool
	}{
		{"never at 85 is ok at 84", []float64{80, 84}, metrics.DiskOK, false},
		{"86 then 84 stays warning", []float64{86, 84}, metrics.DiskWarning, true},
		{"86 then 83 stays warning", []float64{86, 83}, metrics.DiskWarning, true},
		{"86 then 82 is ok", []float64{86, 82}, metrics.DiskOK, false},
		{"85 is warning", []float64{84, 85}, metrics.DiskWarning, true},
		{"96 is failed", []float64{86, 96}, metrics.DiskFailed, true},
		{"96 then 94 is warning again", []float64{96, 94}, metrics.DiskWarning, true},
		{"96 then 84 is warning", []float64{96, 84}, metrics.DiskWarning, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMetricsFixture(t, now)
			ts := now.Truncate(time.Minute).Add(-time.Minute)
			var hosts []store.HostSample
			var disks []store.DiskSample
			for i, p := range tc.percents {
				at := ts.Add(-time.Duration(len(tc.percents)-1-i) * time.Minute)
				used := int64(p * 10)
				hosts = append(hosts, hostRow("n1", at, 1000, 900, 4096))
				disks = append(disks, diskRow("n1", at, "/", "nomad,docker", used, 1000-used))
			}
			f.host(store.MetricsRows{Hosts: hosts, Disks: disks})
			v, err := f.e.svc.Host(f.ctx)
			if err != nil || len(v.Disks) != 1 {
				t.Fatalf("Host = %+v, %v", v.Disks, err)
			}
			if v.Disks[0].State != tc.want {
				t.Errorf("meter state = %q, want %q", v.Disks[0].State, tc.want)
			}
			att, err := f.e.svc.Attention(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			var open metrics.DiskLevel
			for _, it := range att.Items {
				if it.Kind == AttentionDiskNearlyFull {
					open = it.Level
				}
			}
			if (open != "") != tc.item || (tc.item && open != v.Disks[0].State) {
				t.Errorf("the item's level %q and the meter's %q disagree", open, v.Disks[0].State)
			}
		})
	}
}
