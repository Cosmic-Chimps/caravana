package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Network and disk I/O per server (feature 055).

var ioBase = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// ioAt is a block read at t with the given counters: the default interface, the mesh and one device.
func ioAt(t time.Time, rx, tx, meshRx, meshTx, read, write, busyMs int64) *NodeIO {
	return &NodeIO{At: t.UnixMilli(),
		Net:   []NodeIONet{{Name: "eth0", Kind: "default", RxBytes: rx, TxBytes: tx}, {Name: "caravana-mesh", Kind: "mesh", RxBytes: meshRx, TxBytes: meshTx}},
		Disks: []NodeIODisk{{Name: "sda1", Holds: []string{"nomad", "docker"}, ReadBytes: read, WriteBytes: write, BusyMs: busyMs}}}
}

func TestIORates(t *testing.T) {
	prev := ioAt(ioBase, 1_000_000, 500_000, 100, 200, 10_000, 20_000, 1000)
	t.Run("30 seconds", func(t *testing.T) {
		cur := ioAt(ioBase.Add(30*time.Second), 4_000_000, 2_000_000, 3100, 6200, 310_000, 620_000, 7000)
		r, ok := ioRates(prev, cur)
		if !ok {
			t.Fatal("no rates")
		}
		if *r.NetRxBps != 100_000 || *r.NetTxBps != 50_000 {
			t.Errorf("net = %d in, %d out; want 100000 and 50000 (the default interface's)", *r.NetRxBps, *r.NetTxBps)
		}
		if len(r.Net) != 2 || r.Net[1].Kind != "mesh" || r.Net[1].RxBps != 100 || r.Net[1].TxBps != 200 {
			t.Errorf("per interface = %+v", r.Net)
		}
		if *r.DiskReadBps != 10_000 || *r.DiskWriteBps != 20_000 || *r.DiskBusyPercent != 20 {
			t.Errorf("disk = %d read, %d write, %d%% busy; want 10000, 20000 and 20", *r.DiskReadBps, *r.DiskWriteBps, *r.DiskBusyPercent)
		}
	})
	t.Run("rounding and the cap", func(t *testing.T) {
		cur := ioAt(ioBase.Add(45*time.Second), 1_000_001, 500_000, 100, 200, 10_000, 20_000, 1000+90_000)
		r, _ := ioRates(prev, cur)
		if *r.NetRxBps != 0 { // 1 byte in 45 s is 0.02 B/s
			t.Errorf("rx = %d", *r.NetRxBps)
		}
		if *r.DiskBusyPercent != 100 { // 90 s of busy time in 45 s can't be: capped
			t.Errorf("busy = %d, want it capped at 100", *r.DiskBusyPercent)
		}
	})
	t.Run("too close, too far, not forward", func(t *testing.T) {
		for name, gap := range map[string]time.Duration{"19 s": 19 * time.Second, "121 s": 121 * time.Second, "same time": 0, "backwards": -30 * time.Second} {
			if _, ok := ioRates(prev, ioAt(ioBase.Add(gap), 2_000_000, 600_000, 200, 300, 20_000, 30_000, 2000)); ok {
				t.Errorf("%s: rates were made", name)
			}
		}
		for name, gap := range map[string]time.Duration{"20 s": 20 * time.Second, "120 s": 120 * time.Second} {
			if _, ok := ioRates(prev, ioAt(ioBase.Add(gap), 2_000_000, 600_000, 200, 300, 20_000, 30_000, 2000)); !ok {
				t.Errorf("%s: no rates, the bounds are inclusive", name)
			}
		}
	})
	t.Run("a counter that went down is a reset", func(t *testing.T) {
		// The default interface reset (a driver reload): its headline is gone, the mesh and the disk stay.
		cur := ioAt(ioBase.Add(30*time.Second), 10, 10, 3100, 6200, 310_000, 620_000, 7000)
		r, ok := ioRates(prev, cur)
		if !ok || r.NetRxBps != nil || r.NetTxBps != nil || len(r.Net) != 1 || r.Net[0].Kind != "mesh" || r.DiskReadBps == nil {
			t.Errorf("after an interface reset: ok=%v %+v", ok, r)
		}
		// Only the transmit counter went down: still a reset of that interface.
		cur = ioAt(ioBase.Add(30*time.Second), 4_000_000, 10, 3100, 6200, 310_000, 620_000, 7000)
		if r, _ := ioRates(prev, cur); r.NetRxBps != nil {
			t.Errorf("a reset of tx only still gave rx: %+v", r)
		}
		// A reboot resets every counter: nothing to say.
		cur = ioAt(ioBase.Add(30*time.Second), 1, 1, 1, 1, 1, 1, 1)
		if _, ok := ioRates(prev, cur); ok {
			t.Error("rates after a reboot")
		}
	})
	t.Run("an interface or device that wasn't in the first sample", func(t *testing.T) {
		cur := ioAt(ioBase.Add(30*time.Second), 4_000_000, 2_000_000, 3100, 6200, 310_000, 620_000, 7000)
		cur.Disks = append(cur.Disks, NodeIODisk{Name: "sdb", Holds: []string{"docker"}, ReadBytes: 5, WriteBytes: 5, BusyMs: 5})
		r, _ := ioRates(prev, cur)
		if len(r.Disks) != 1 || r.Disks[0].Name != "sda1" {
			t.Errorf("disks = %+v", r.Disks)
		}
	})
	t.Run("the busiest device and the sum over devices", func(t *testing.T) {
		p := ioAt(ioBase, 0, 0, 0, 0, 100, 200, 0)
		p.Disks = append(p.Disks, NodeIODisk{Name: "sdb", Holds: []string{"docker"}, ReadBytes: 1000, WriteBytes: 2000, BusyMs: 0})
		c := ioAt(ioBase.Add(30*time.Second), 0, 0, 0, 0, 3100, 6200, 3000)
		c.Disks = append(c.Disks, NodeIODisk{Name: "sdb", Holds: []string{"docker"}, ReadBytes: 31000, WriteBytes: 62000, BusyMs: 15000})
		r, _ := ioRates(p, c)
		if *r.DiskReadBps != 100+1000 || *r.DiskWriteBps != 200+2000 || *r.DiskBusyPercent != 50 {
			t.Errorf("read %d write %d busy %d; want the sums 1100 and 2200 and the busiest's 50", *r.DiskReadBps, *r.DiskWriteBps, *r.DiskBusyPercent)
		}
	})
	t.Run("a counter near 2^63 doesn't overflow the rate", func(t *testing.T) {
		p := ioAt(ioBase, 1<<62, 0, 0, 0, 0, 0, 0)
		c := ioAt(ioBase.Add(30*time.Second), 1<<62+(1<<61), 0, 0, 0, 0, 0, 0)
		r, ok := ioRates(p, c)
		if !ok || *r.NetRxBps <= 0 {
			t.Errorf("rate = %+v", r)
		}
	})
}

func TestCheckIO(t *testing.T) {
	now := ioBase
	good := func() *NodeIO { return ioAt(now, 1, 1, 1, 1, 1, 1, 1) }
	if why := checkIO(good(), now); why != "" {
		t.Fatalf("a good block: %s", why)
	}
	for name, mutate := range map[string]func(*NodeIO){
		"invalid":          func(io *NodeIO) { io.Invalid = true },
		"zero time":        func(io *NodeIO) { io.At = 0 },
		"a clock far back": func(io *NodeIO) { io.At = now.Add(-11 * time.Minute).UnixMilli() },
		"a clock ahead":    func(io *NodeIO) { io.At = now.Add(11 * time.Minute).UnixMilli() },
		"five interfaces": func(io *NodeIO) {
			for i := 0; i < 3; i++ {
				io.Net = append(io.Net, NodeIONet{Name: "x" + string(rune('a'+i)), Kind: "mesh"})
			}
		},
		"five devices": func(io *NodeIO) {
			for i := 0; i < 4; i++ {
				io.Disks = append(io.Disks, NodeIODisk{Name: "d" + string(rune('a'+i))})
			}
		},
		"an empty name":       func(io *NodeIO) { io.Net[0].Name = "" },
		"a long name":         func(io *NodeIO) { io.Net[0].Name = strings.Repeat("a", 65) },
		"a control character": func(io *NodeIO) { io.Disks[0].Name = "sd\x00a" },
		"a twin interface":    func(io *NodeIO) { io.Net[1].Name = io.Net[0].Name },
		"a twin device":       func(io *NodeIO) { io.Disks = append(io.Disks, io.Disks[0]) },
		"an unknown kind":     func(io *NodeIO) { io.Net[0].Kind = "wifi" },
		"a negative rx":       func(io *NodeIO) { io.Net[0].RxBytes = -1 },
		"a negative busy":     func(io *NodeIO) { io.Disks[0].BusyMs = -1 },
		"an unknown holds":    func(io *NodeIO) { io.Disks[0].Holds = []string{"root"} },
		"three holds":         func(io *NodeIO) { io.Disks[0].Holds = []string{"nomad", "docker", "nomad"} },
	} {
		io := good()
		mutate(io)
		if why := checkIO(io, now); why == "" {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(why, "sd\x00a") {
			t.Errorf("%s: the reason holds the agent's text: %q", name, why)
		}
	}
}

// A block that doesn't decode never fails the report.
func TestNodeIODecodesLeniently(t *testing.T) {
	for name, body := range map[string]string{
		"a counter that isn't a number": `{"agent":"1","io":{"at":1,"net":[{"name":"eth0","kind":"default","rxBytes":"x"}],"disks":[]}}`,
		"a counter over 2^63":           `{"agent":"1","io":{"at":1,"net":[{"name":"eth0","kind":"default","rxBytes":18446744073709551615}],"disks":[]}}`,
		"the wrong shape":               `{"agent":"1","io":[1,2]}`,
	} {
		var r NodeReport
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Errorf("%s: the report didn't decode: %v", name, err)
			continue
		}
		if r.IO == nil || !r.IO.Invalid || r.Agent != "1" {
			t.Errorf("%s: io = %+v", name, r.IO)
		}
	}
	var r NodeReport
	if err := json.Unmarshal([]byte(`{"agent":"1"}`), &r); err != nil || r.IO != nil {
		t.Errorf("no io: %+v, %v", r.IO, err)
	}
}

func TestHostIOState(t *testing.T) {
	var st hostIOState
	at := func(sec int) time.Time { return ioBase.Add(time.Duration(sec) * time.Second) }
	block := func(sec int, rx int64) *NodeIO { return ioAt(at(sec), rx, 0, 0, 0, 0, 0, 0) }

	if v := st.view(0, at(0)); v.Reason != IONoAgent || v.Known() {
		t.Errorf("an unmanaged node: %+v", v)
	}
	if v := st.view(2, at(0)); v.Reason != IOWaiting {
		t.Errorf("before any sync: %+v", v)
	}

	// The first sample is a starting point; the second, 30 s later, makes rates.
	if why := st.noteIO(2, block(0, 0), at(0)); why != "" {
		t.Fatal(why)
	}
	if v := st.view(2, at(0)); v.Reason != IOWaiting {
		t.Errorf("one sample: %+v", v)
	}
	st.noteIO(2, block(30, 3000), at(30))
	v := st.view(2, at(31))
	if !v.Known() || *v.NetRxBps != 100 || v.At == nil || !v.At.Equal(at(30)) {
		t.Fatalf("two samples: %+v", v)
	}

	// Fresh until 3 minutes after the rates were made; then the agent is stale, and says since when.
	if v := st.view(2, at(30+180)); !v.Known() {
		t.Errorf("at 3 minutes: %+v", v)
	}
	if v := st.view(2, at(30+181)); v.Reason != IOStale || v.Since == nil || !v.Since.Equal(at(30)) {
		t.Errorf("after 3 minutes: %+v", v)
	}

	// A reset of the default interface: the new pair has no network headline (never the old one), the rest stays.
	st.noteIO(2, block(60, 10), at(60))
	if v := st.view(2, at(61)); !v.Known() || v.NetRxBps != nil {
		t.Errorf("after a reset: %+v", v)
	}
	// A sample 10 s after the last is too close for a rate: the page waits, and the next pair is made from it.
	st.noteIO(2, block(70, 10), at(70))
	if v := st.view(2, at(71)); v.Reason != IOWaiting {
		t.Errorf("after a sample too close to the last: %+v", v)
	}
	st.noteIO(2, block(90, 3010), at(90))
	if v := st.view(2, at(91)); !v.Known() || *v.NetRxBps != 150 {
		t.Errorf("after the pair 20 s later: %+v", v)
	}

	// A gap longer than 2 minutes between syncs: no rates for that pair.
	st.noteIO(2, block(300, 9000), at(300))
	if v := st.view(2, at(301)); v.Reason != IOWaiting {
		t.Errorf("after a long gap: %+v", v)
	}

	// An agent that syncs without a block; then one whose block is dropped (a clock far off).
	st.noteIO(3, nil, at(0))
	if v := st.view(3, at(1)); v.Reason != IOAgentTooOld {
		t.Errorf("no io block: %+v", v)
	}
	bad := block(0, 0)
	bad.At = at(0).Add(-time.Hour).UnixMilli()
	if why := st.noteIO(3, bad, at(30)); why == "" {
		t.Error("a block an hour off was kept")
	}
	if v := st.view(3, at(31)); v.Reason != IOAgentTooOld {
		t.Errorf("a dropped block: %+v", v)
	}
	// And a block after none starts again.
	st.noteIO(3, block(60, 0), at(60))
	st.noteIO(3, block(90, 300), at(90))
	if v := st.view(3, at(91)); !v.Known() {
		t.Errorf("after the agent updated: %+v", v)
	}

	st.forget(3)
	if v := st.view(3, at(91)); v.Reason != IOWaiting {
		t.Errorf("a forgotten server: %+v", v)
	}
}

// ---- through the service ----------------------------------------------------------------------

// syncIO makes a server's agent sync with an io block, the console's clock at at.
func (l *serversLab) syncIO(t *testing.T, token string, at time.Time, io *NodeIO) {
	t.Helper()
	l.svc.clock.(*fakeClock).Set(at)
	r := healthyReport(1, 2)
	r.IO = io
	if _, err := l.svc.NodeSync(context.Background(), token, r); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func TestSyncMakesRatesAndTheSamplerWritesThem(t *testing.T) {
	ctx := context.Background()
	l := newServersLab(t)

	// Server "two" syncs twice, 31 s apart, around where tick 1 will run; the first server's agent syncs
	// without an io block (an older agent).
	t1 := labBase.Add(time.Minute)
	first, second := t1.Add(-30*time.Second), t1.Add(time.Second)
	l.syncIO(t, l.twoToken, first, ioAt(first, 1_000_000, 0, 0, 0, 0, 0, 0))
	l.syncIO(t, l.twoToken, second, ioAt(second, 1_000_000+3_100_000, 310_000, 0, 0, 62_000_000, 31_000_000, 15_500))
	l.syncIO(t, l.firstToken, second, nil)
	l.tick(t, 1)

	rows := l.hostRowsAt(t, rowTS(1))
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	var one, two store.HostSample
	for _, r := range rows {
		if r.Node == "node-1" {
			one = r
		} else {
			two = r
		}
	}
	// 3.1 MB in 31 s is 100000 B/s, and so on.
	if two.NetRxBps == nil || *two.NetRxBps != 100_000 || *two.NetTxBps != 10_000 || *two.DiskReadBps != 2_000_000 ||
		*two.DiskWriteBps != 1_000_000 || *two.DiskBusyPct != 50 {
		t.Errorf("two's row = rx %v tx %v read %v write %v busy %v", two.NetRxBps, two.NetTxBps, two.DiskReadBps, two.DiskWriteBps, two.DiskBusyPct)
	}
	// The first server has no numbers: NULL, never 0.
	if one.NetRxBps != nil || one.NetTxBps != nil || one.DiskReadBps != nil || one.DiskWriteBps != nil || one.DiskBusyPct != nil {
		t.Errorf("the server without an io block has numbers: %+v", one)
	}

	// Host: known for two, the agent too old for the first, no agent for a node Caravana has no row for.
	v2, err := l.svc.Host(ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	if !v2.IO.Known() || *v2.IO.NetRxBps != 100_000 || len(v2.IO.Net) != 2 || len(v2.IO.Disks) != 1 || v2.IO.Disks[0].Name != "sda1" {
		t.Errorf("two's io = %+v", v2.IO)
	}
	if v1, _ := l.svc.Host(ctx, ""); v1.IO.Reason != IOAgentTooOld {
		t.Errorf("the first server's io = %+v", v1.IO)
	}
	l.nomad.AddNode("node-9", "nine", "ready", "")
	l.nomad.SetHostStatsOf("node-9", statsAt(100, 10))
	if v9, err := l.svc.Host(ctx, "nine"); err != nil || v9.IO.Reason != IONoAgent {
		t.Errorf("an unmanaged node: %+v, %v", v9.IO, err)
	}

	// The stored report has no counters (they live in memory).
	row, _ := l.store.ServerByName(ctx, "two")
	if strings.Contains(row.Report, "rxBytes") || strings.Contains(row.Report, `"io"`) {
		t.Errorf("the stored report holds the io block: %s", row.Report)
	}

	// Four minutes with no sync: the next rows are NULL for two as well.
	l.tick(t, 5)
	for _, r := range l.hostRowsAt(t, rowTS(5)) {
		if r.NetRxBps != nil || r.DiskBusyPct != nil {
			t.Errorf("a stale server's row has numbers: %+v", r)
		}
	}
	if v, _ := l.svc.Host(ctx, "two"); v.IO.Reason != IOStale {
		t.Errorf("two after 4 minutes of silence: %+v", v.IO)
	}
}

// A report whose io block is bad is stored without it and the sync goes on.
func TestSyncDropsABadIOBlock(t *testing.T) {
	l := newServersLab(t)
	at := labBase.Add(time.Minute)
	bad := ioAt(at, 1, 1, 1, 1, 1, 1, 1)
	bad.Net[0].Kind = "wifi"
	l.syncIO(t, l.twoToken, at, bad)
	l.syncIO(t, l.twoToken, at.Add(30*time.Second), bad)
	if v, _ := l.svc.Host(context.Background(), "two"); v.IO.Reason != IOAgentTooOld {
		t.Errorf("io = %+v", v.IO)
	}
}

func TestFoldHostsMediansIOColumns(t *testing.T) {
	slot := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	i := func(v int64) *int64 { return &v }
	rows := []store.HostSample{
		{Node: "n", TS: slot, NetRxBps: i(100), DiskBusyPct: i(10)},
		{Node: "n", TS: slot.Add(time.Minute), NetRxBps: i(300)},
		{Node: "n", TS: slot.Add(2 * time.Minute), NetRxBps: i(200)},
		{Node: "n", TS: slot.Add(3 * time.Minute)}, // a minute with no agent
		{Node: "m", TS: slot}, // a node with none at all
	}
	got := foldHosts(slot, rows)
	if len(got) != 2 {
		t.Fatalf("folded = %+v", got)
	}
	m, n := got[0], got[1]
	if m.Node != "m" || m.NetRxBps != nil || m.DiskReadBps != nil {
		t.Errorf("a node with no values folds to NULLs: %+v", m)
	}
	if n.NetRxBps == nil || *n.NetRxBps != 200 || n.DiskBusyPct == nil || *n.DiskBusyPct != 10 || n.NetTxBps != nil {
		t.Errorf("n = rx %v busy %v tx %v; want the median of the minutes that have a value", n.NetRxBps, n.DiskBusyPct, n.NetTxBps)
	}
}

func TestHostIOSeries(t *testing.T) {
	ctx := context.Background()
	l := newServersLab(t)
	i := func(v int64) *int64 { return &v }
	// Minute rows for node-2: values for 20 minutes, a minute with none, then two more.
	base := time.Date(2026, 11, 24, 11, 0, 0, 0, time.UTC)
	var hosts []store.HostSample
	for m := 0; m < 23; m++ {
		h := store.HostSample{Node: "node-2", TS: base.Add(time.Duration(m) * time.Minute)}
		if m != 20 {
			h.NetRxBps, h.NetTxBps = i(int64(1000*(m+1))), i(10)
			h.DiskReadBps, h.DiskWriteBps, h.DiskBusyPct = i(5), i(7), i(int64(m))
		}
		hosts = append(hosts, h)
	}
	hosts = append(hosts, store.HostSample{Node: "node-1", TS: base, NetRxBps: i(999999)})
	if err := l.store.WriteMetricsTick(ctx, store.MetricsTick{At: base.Add(23 * time.Minute), Minute: store.MetricsRows{Hosts: hosts}}); err != nil {
		t.Fatal(err)
	}
	l.svc.clock.(*fakeClock).Set(base.Add(22*time.Minute + 30*time.Second))

	s, err := l.svc.HostIOSeries(ctx, "two", metrics.Range1h)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Points) != 60 || !s.Known {
		t.Fatalf("%d points, known %v", len(s.Points), s.Known)
	}
	byTime := map[time.Time]HostIOPoint{}
	for _, p := range s.Points {
		byTime[p.T] = p
	}
	if p := byTime[base.Add(5*time.Minute)]; p.NetRxBps == nil || *p.NetRxBps != 6000 || *p.DiskBusyPercent != 5 {
		t.Errorf("minute 5 = %+v", p)
	}
	if p, ok := byTime[base.Add(20*time.Minute)]; !ok || p.NetRxBps != nil || p.DiskBusyPercent != nil {
		t.Errorf("a minute with no numbers is a gap: %+v", p)
	}
	if p, ok := byTime[base.Add(-10*time.Minute)]; !ok || p.NetRxBps != nil {
		t.Errorf("a minute with no row is a gap: %+v", p)
	}
	// The 6 h range takes the median of each five minutes; node-1's row never counts for two.
	s6, err := l.svc.HostIOSeries(ctx, "two", metrics.Range6h)
	if err != nil || len(s6.Points) != 72 {
		t.Fatalf("6h: %d points, %v", len(s6.Points), err)
	}
	for _, p := range s6.Points {
		if p.T.Equal(base) && (p.NetRxBps == nil || *p.NetRxBps != 3000) {
			t.Errorf("the first five minutes = %+v", p)
		}
	}
	// 24 h: the slot in progress is folded from its minute rows.
	s24, err := l.svc.HostIOSeries(ctx, "two", metrics.Range24h)
	if err != nil || !s24.Known {
		t.Fatalf("24h: known %v, %v", s24.Known, err)
	}
	// A server with no rows of its own is not known; one that doesn't exist is an error.
	if _, err := l.svc.HostIOSeries(ctx, "nope", metrics.Range1h); err == nil {
		t.Error("an unknown server was read")
	}
}
