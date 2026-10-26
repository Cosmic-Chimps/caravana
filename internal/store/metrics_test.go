package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// 2026-10-24 12:00 UTC: a minute and a 10-minute slot start.
var metricsT0 = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func i64(v int64) *int64   { return &v }
func str(v string) *string { return &v }

// testApp is the whoami app under another ID.
func testApp(id string) app.App {
	a := apptest.Whoami().App
	a.ID = id
	return a
}

func fullSample(series int64, ts time.Time, layout int64) MetricsSample {
	return MetricsSample{
		Series: series, TS: ts, Minutes: 1,
		HTTPSecs: i64(60), Requests: i64(120), Errors: i64(3), Layout: &layout, Buckets: str("100,15,5,0,0,0,0,0,0,0,0,0"),
		CPUMHz: i64(250), CPUReservedMHz: i64(500), MemoryMB: i64(180), MemoryLimitMB: i64(1024),
		MemoryReservedMB: i64(256), Copies: i64(2), Restarts: i64(1),
	}
}

func TestEnsureSeriesAndLayout(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	a, err := s.EnsureSeries(ctx, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.EnsureSeries(ctx, "shop", "web")
	if err != nil || again != a {
		t.Fatalf("second EnsureSeries = %d, %v; want %d", again, err, a)
	}
	b, err := s.EnsureSeries(ctx, "shop", "db")
	if err != nil || b == a {
		t.Fatalf("EnsureSeries of another service = %d, %v; want a new ID", b, err)
	}
	list, err := s.ListMetricsSeries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []MetricsSeries{{ID: b, AppID: "shop", Service: "db"}, {ID: a, AppID: "shop", Service: "web"}}
	if !reflect.DeepEqual(list, want) {
		t.Errorf("series = %+v, want %+v", list, want)
	}

	l1, err := s.EnsureLayout(ctx, "0.1,1")
	if err != nil {
		t.Fatal(err)
	}
	if l2, err := s.EnsureLayout(ctx, "0.1,1"); err != nil || l2 != l1 {
		t.Fatalf("same bounds gave layout %d, %v; want %d", l2, err, l1)
	}
	l3, err := s.EnsureLayout(ctx, "0.1,1,10")
	if err != nil || l3 == l1 {
		t.Fatalf("new bounds gave layout %d, %v; want a new ID", l3, err)
	}
	got, err := s.MetricsLayouts(ctx)
	if err != nil || !reflect.DeepEqual(got, map[int64]string{l1: "0.1,1", l3: "0.1,1,10"}) {
		t.Errorf("layouts = %v, %v", got, err)
	}
}

func TestMetricsTickRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	series, _ := s.EnsureSeries(ctx, "shop", "web")
	layout, _ := s.EnsureLayout(ctx, "0.1,1")

	if _, err := s.MetricsStartedAt(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MetricsStartedAt before any tick = %v, want ErrNotFound", err)
	}
	if _, err := s.NewestHost(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NewestHost before any tick = %v, want ErrNotFound", err)
	}

	host := HostSample{
		Node: "n1", TS: metricsT0, MemoryUsedMB: i64(900), MemoryReservedMB: i64(700), MemoryTotalMB: i64(2048),
		CPUUsedMHz: i64(300), CPUReservedMHz: i64(400), CPUTotalMHz: i64(4000), CPUCores: i64(2),
	}
	disks := []DiskSample{
		{Node: "n1", TS: metricsT0, Mount: "/var/lib/docker", Holds: "docker", UsedMB: 10, TotalMB: 100, FreeMB: 85},
		{Node: "n1", TS: metricsT0, Mount: "/", Holds: "nomad", UsedMB: 20, TotalMB: 200, FreeMB: 170},
	}
	sample := fullSample(series, metricsT0, layout)
	if err := s.WriteMetricsTick(ctx, MetricsTick{
		At:     metricsT0.Add(2 * time.Second),
		Minute: MetricsRows{Samples: []MetricsSample{sample}, Hosts: []HostSample{host}, Disks: disks},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.MinuteRows(ctx, series, metricsT0, metricsT0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []MetricsSample{sample}) {
		t.Errorf("minute rows = %+v, want %+v", got, sample)
	}
	if slots, err := s.SlotRows(ctx, series, metricsT0.Add(-time.Hour), metricsT0.Add(time.Hour)); err != nil || len(slots) != 0 {
		t.Errorf("slot rows = %v, %v; want none", slots, err)
	}
	// `to` is exclusive, `from` inclusive.
	if rows, _ := s.MinuteRows(ctx, series, metricsT0.Add(time.Second), metricsT0.Add(time.Hour)); len(rows) != 0 {
		t.Errorf("a range starting after the row returned %d rows", len(rows))
	}
	if rows, _ := s.MinuteRows(ctx, series, metricsT0.Add(-time.Hour), metricsT0); len(rows) != 0 {
		t.Errorf("a range ending at the row's start returned %d rows", len(rows))
	}

	hosts, err := s.HostRows(ctx, MinuteRes, metricsT0, metricsT0.Add(time.Minute))
	if err != nil || !reflect.DeepEqual(hosts, []HostSample{host}) {
		t.Errorf("host rows = %+v, %v; want %+v", hosts, err, host)
	}
	newest, err := s.NewestHost(ctx)
	if err != nil || !reflect.DeepEqual(newest, host) {
		t.Errorf("NewestHost = %+v, %v; want %+v", newest, err, host)
	}
	gotDisks, err := s.DiskRows(ctx, MinuteRes, metricsT0, metricsT0.Add(time.Minute))
	if err != nil || !reflect.DeepEqual(gotDisks, []DiskSample{disks[1], disks[0]}) { // by mount
		t.Errorf("disk rows = %+v, %v", gotDisks, err)
	}

	// A later minute becomes the newest host row.
	later := host
	later.TS = metricsT0.Add(time.Minute)
	later.MemoryUsedMB = i64(950)
	if err := s.WriteMetricsTick(ctx, MetricsTick{At: later.TS, Minute: MetricsRows{Hosts: []HostSample{later}}}); err != nil {
		t.Fatal(err)
	}
	if newest, _ := s.NewestHost(ctx); !reflect.DeepEqual(newest, later) {
		t.Errorf("NewestHost = %+v, want %+v", newest, later)
	}

	// started_at is written once, by the first tick, and never moves.
	started, err := s.MetricsStartedAt(ctx)
	if err != nil || !started.Equal(metricsT0.Add(2*time.Second)) {
		t.Errorf("MetricsStartedAt = %v, %v; want the first tick's time", started, err)
	}
}

func TestMetricsNullStaysNull(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	series, _ := s.EnsureSeries(ctx, "shop", "web")

	// Only the CPU was read; the HTTP numbers, memory, copies and restarts were not.
	sample := MetricsSample{Series: series, TS: metricsT0, Minutes: 1, CPUMHz: i64(0)}
	host := HostSample{Node: "n1", TS: metricsT0, CPUTotalMHz: i64(4000)}
	if err := s.WriteMetricsTick(ctx, MetricsTick{
		At: metricsT0, Minute: MetricsRows{Samples: []MetricsSample{sample}, Hosts: []HostSample{host}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.MinuteRows(ctx, series, metricsT0, metricsT0.Add(time.Minute))
	if err != nil || len(got) != 1 {
		t.Fatalf("rows = %v, %v", got, err)
	}
	if !reflect.DeepEqual(got[0], sample) {
		t.Errorf("got %+v, want %+v", got[0], sample)
	}
	if got[0].Requests != nil || got[0].Copies != nil || got[0].Buckets != nil || got[0].Restarts != nil {
		t.Errorf("a column nobody wrote was read back as a value: %+v", got[0])
	}
	if got[0].CPUMHz == nil || *got[0].CPUMHz != 0 {
		t.Errorf("a real zero CPU read back as %v, want 0", got[0].CPUMHz)
	}

	// In the file itself, NULL and not 0.
	var nulls int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM metrics_samples
		WHERE http_secs IS NULL AND requests IS NULL AND errors IS NULL AND layout IS NULL AND buckets IS NULL
		AND memory_mb IS NULL AND copies IS NULL AND restarts IS NULL AND cpu_mhz = 0`).Scan(&nulls); err != nil || nulls != 1 {
		t.Errorf("rows with NULLs = %d, %v; want 1", nulls, err)
	}
	if hosts, _ := s.HostRows(ctx, MinuteRes, metricsT0, metricsT0.Add(time.Minute)); len(hosts) != 1 || hosts[0].MemoryUsedMB != nil {
		t.Errorf("host row = %+v, want a nil memory", hosts)
	}
}

func TestMetricsRefoldReplacesTheSlot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	series, _ := s.EnsureSeries(ctx, "shop", "web")
	layout, _ := s.EnsureLayout(ctx, "0.1,1")

	first := fullSample(series, metricsT0, layout)
	first.Minutes = 9
	first.Requests = i64(900)
	firstHost := HostSample{Node: "n1", TS: metricsT0, MemoryUsedMB: i64(800)}
	firstDisk := DiskSample{Node: "n1", TS: metricsT0, Mount: "/", Holds: "nomad", UsedMB: 1, TotalMB: 10, FreeMB: 9}
	write := func(sample MetricsSample, host HostSample, disk DiskSample) {
		t.Helper()
		if err := s.WriteMetricsTick(ctx, MetricsTick{
			At:   metricsT0.Add(10 * time.Minute),
			Fold: MetricsRows{Samples: []MetricsSample{sample}, Hosts: []HostSample{host}, Disks: []DiskSample{disk}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	write(first, firstHost, firstDisk)

	// The refold has all ten minutes, and fewer facts known.
	second := MetricsSample{Series: series, TS: metricsT0, Minutes: 10, Requests: i64(1000)}
	secondHost := HostSample{Node: "n1", TS: metricsT0, MemoryUsedMB: nil, CPUUsedMHz: i64(5)}
	secondDisk := firstDisk
	secondDisk.UsedMB = 5
	write(second, secondHost, secondDisk)

	rows, err := s.SlotRows(ctx, series, metricsT0, metricsT0.Add(10*time.Minute))
	if err != nil || len(rows) != 1 {
		t.Fatalf("slot rows = %v, %v; want one", rows, err)
	}
	if !reflect.DeepEqual(rows[0], second) {
		t.Errorf("refolded row = %+v, want %+v (nothing of the first fold left)", rows[0], second)
	}
	hosts, _ := s.HostRows(ctx, SlotRes, metricsT0, metricsT0.Add(10*time.Minute))
	if len(hosts) != 1 || !reflect.DeepEqual(hosts[0], secondHost) {
		t.Errorf("refolded host = %+v, want %+v", hosts, secondHost)
	}
	disks, _ := s.DiskRows(ctx, SlotRes, metricsT0, metricsT0.Add(10*time.Minute))
	if len(disks) != 1 || disks[0].UsedMB != 5 {
		t.Errorf("refolded disks = %+v, want used 5", disks)
	}
	// The minute resolution is untouched by a fold.
	if rows, _ := s.MinuteRows(ctx, series, metricsT0.Add(-time.Hour), metricsT0.Add(time.Hour)); len(rows) != 0 {
		t.Errorf("a fold wrote %d minute rows", len(rows))
	}
}

func TestMetricsPruneBounds(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	series, _ := s.EnsureSeries(ctx, "shop", "web")
	now := metricsT0

	minuteEdge := now.Add(-MinuteKeep)
	slotEdge := now.Add(-SlotKeep)
	rowsAt := func(edge time.Time) MetricsRows {
		var r MetricsRows
		for _, ts := range []time.Time{edge.Add(-time.Minute), edge, edge.Add(time.Minute)} {
			r.Samples = append(r.Samples, MetricsSample{Series: series, TS: ts, Minutes: 1, CPUMHz: i64(1)})
			r.Hosts = append(r.Hosts, HostSample{Node: "n1", TS: ts, CPUCores: i64(2)})
			r.Disks = append(r.Disks, DiskSample{Node: "n1", TS: ts, Mount: "/", Holds: "nomad", UsedMB: 1, TotalMB: 2, FreeMB: 1})
		}
		return r
	}
	// Without Prune nothing goes, however old.
	if err := s.WriteMetricsTick(ctx, MetricsTick{At: now, Minute: rowsAt(minuteEdge), Fold: rowsAt(slotEdge)}); err != nil {
		t.Fatal(err)
	}
	all := func() (minutes, slots []time.Time) {
		t.Helper()
		far := now.Add(-100 * 24 * time.Hour)
		m, _ := s.MinuteRows(ctx, series, far, now)
		sl, _ := s.SlotRows(ctx, series, far, now)
		for _, r := range m {
			minutes = append(minutes, r.TS)
		}
		for _, r := range sl {
			slots = append(slots, r.TS)
		}
		return minutes, slots
	}
	if m, sl := all(); len(m) != 3 || len(sl) != 3 {
		t.Fatalf("before the prune: %d minute rows and %d slot rows, want 3 and 3", len(m), len(sl))
	}

	if err := s.WriteMetricsTick(ctx, MetricsTick{At: now, Prune: true}); err != nil {
		t.Fatal(err)
	}
	minutes, slots := all()
	wantMinutes := []time.Time{minuteEdge, minuteEdge.Add(time.Minute)}
	wantSlots := []time.Time{slotEdge, slotEdge.Add(time.Minute)}
	if !reflect.DeepEqual(minutes, wantMinutes) {
		t.Errorf("minute rows after prune = %v, want the edge (inclusive) and after: %v", minutes, wantMinutes)
	}
	if !reflect.DeepEqual(slots, wantSlots) {
		t.Errorf("slot rows after prune = %v, want %v", slots, wantSlots)
	}

	far := now.Add(-100 * 24 * time.Hour)
	for res, edge := range map[int]time.Time{MinuteRes: minuteEdge, SlotRes: slotEdge} {
		hosts, _ := s.HostRows(ctx, res, far, now)
		disks, _ := s.DiskRows(ctx, res, far, now)
		if len(hosts) != 2 || !hosts[0].TS.Equal(edge) {
			t.Errorf("host rows of res %d after prune = %+v, want the edge and after", res, hosts)
		}
		if len(disks) != 2 || !disks[0].TS.Equal(edge) {
			t.Errorf("disk rows of res %d after prune = %+v, want the edge and after", res, disks)
		}
	}
}

func TestDeleteAppDeletesItsMetrics(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, id := range []string{"shop", "blog"} {
		if _, err := s.SaveApp(ctx, testApp(id), nil); err != nil {
			t.Fatal(err)
		}
	}
	shopWeb, _ := s.EnsureSeries(ctx, "shop", "web")
	shopDB, _ := s.EnsureSeries(ctx, "shop", "db")
	blogWeb, _ := s.EnsureSeries(ctx, "blog", "web")
	var rows MetricsRows
	for _, id := range []int64{shopWeb, shopDB, blogWeb} {
		rows.Samples = append(rows.Samples, MetricsSample{Series: id, TS: metricsT0, Minutes: 1, CPUMHz: i64(1)})
	}
	rows.Hosts = []HostSample{{Node: "n1", TS: metricsT0, CPUCores: i64(2)}}
	if err := s.WriteMetricsTick(ctx, MetricsTick{At: metricsT0, Minute: rows, Fold: rows}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteApp(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListMetricsSeries(ctx)
	if len(list) != 1 || list[0].AppID != "blog" {
		t.Errorf("series after delete = %+v, want blog's only", list)
	}
	var samples int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM metrics_samples").Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if samples != 2 { // blog's minute row and slot row
		t.Errorf("samples left = %d, want blog's 2", samples)
	}
	if got, _ := s.MinuteRows(ctx, blogWeb, metricsT0, metricsT0.Add(time.Minute)); len(got) != 1 {
		t.Errorf("blog's minute rows = %d, want 1", len(got))
	}
	if hosts, _ := s.HostRows(ctx, MinuteRes, metricsT0, metricsT0.Add(time.Minute)); len(hosts) != 1 {
		t.Errorf("the host's rows went with the app: %d", len(hosts))
	}
	// The same service name starts a fresh series.
	if id, err := s.EnsureSeries(ctx, "shop", "web"); err != nil || id == shopWeb {
		t.Errorf("EnsureSeries after delete = %d, %v; want a new series", id, err)
	}
}

func TestMigration0019OnAStoreWith0018Data(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// A store as release 0018 left it: migrations 1-18 and some data.
	old := fstest.MapFS{}
	all := testMigrations(t)
	files, err := fs.Glob(all, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f, "0019_") {
			continue
		}
		b, err := fs.ReadFile(all, f)
		if err != nil {
			t.Fatal(err)
		}
		old[f] = &fstest.MapFile{Data: b}
	}
	db, err := openDB(filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := migrate(ctx, db, old); err != nil || n != len(old) {
		t.Fatalf("migrate to 0018 = %d, %v; want %d", n, err, len(old))
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO domain_checks (host, app_id, service, state, dns_status, cert_status, checked_at)
		VALUES ('a.example.com', 'shop', 'web', 'live', 'ok', 'issued', '2026-10-24T12:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open on a 0018 store: %v", err)
	}
	defer s.Close()
	if s.MigrationsApplied() != 1 {
		t.Errorf("MigrationsApplied = %d, want only 0019", s.MigrationsApplied())
	}
	if checks, err := s.ListDomainChecks(ctx); err != nil || len(checks) != 1 {
		t.Errorf("0018's data after the migration: %v, %v", checks, err)
	}
	// The tables work, and are empty.
	if list, err := s.ListMetricsSeries(ctx); err != nil || len(list) != 0 {
		t.Errorf("series = %v, %v; want none", list, err)
	}
	if _, err := s.MetricsStartedAt(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("MetricsStartedAt = %v, want ErrNotFound", err)
	}
	id, err := s.EnsureSeries(ctx, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetricsTick(ctx, MetricsTick{At: metricsT0,
		Minute: MetricsRows{Samples: []MetricsSample{{Series: id, TS: metricsT0, Minutes: 1, Copies: i64(1)}}}}); err != nil {
		t.Fatal(err)
	}
}

// TestMetricsSize writes what 20 services keep (48 hours of minute rows and 8
// days of 10-minute rows) and measures the file after a VACUUM.
func TestMetricsSize(t *testing.T) {
	if testing.Short() {
		t.Skip("writes about 100,000 rows")
	}
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const services = 20
	layout, err := s.EnsureLayout(ctx, "0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, services)
	for i := range ids {
		if ids[i], err = s.EnsureSeries(ctx, "project--production--app"+strconv.Itoa(i), "web"); err != nil {
			t.Fatal(err)
		}
	}
	row := func(id int64, ts time.Time, n int64, minutes int) MetricsSample {
		return MetricsSample{
			Series: id, TS: ts, Minutes: minutes,
			HTTPSecs: i64(60 * int64(minutes)), Requests: i64(1200 + n%97), Errors: i64(n % 5), Layout: &layout,
			Buckets: str("812,260,95,30,12,6,3,1,1,0,0,0"),
			CPUMHz:  i64(120 + n%40), CPUReservedMHz: i64(500), MemoryMB: i64(180 + n%30), MemoryLimitMB: i64(1024),
			MemoryReservedMB: i64(256), Copies: i64(2), Restarts: i64(0),
		}
	}

	start := metricsT0.Add(-SlotKeep)
	// Eight days of 10-minute rows, a day to a transaction.
	for day := range 8 {
		var rows MetricsRows
		for slot := range 144 {
			ts := start.Add(time.Duration(day*144+slot) * 10 * time.Minute)
			for i, id := range ids {
				rows.Samples = append(rows.Samples, row(id, ts, int64(day*144+slot+i), 10))
			}
			rows.Hosts = append(rows.Hosts, HostSample{Node: "n1", TS: ts, MemoryUsedMB: i64(900), MemoryReservedMB: i64(700),
				MemoryTotalMB: i64(2048), CPUUsedMHz: i64(300), CPUReservedMHz: i64(400), CPUTotalMHz: i64(4000), CPUCores: i64(2)})
			rows.Disks = append(rows.Disks, DiskSample{Node: "n1", TS: ts, Mount: "/", Holds: "nomad,docker", UsedMB: 9000, TotalMB: 40000, FreeMB: 31000})
		}
		if err := s.WriteMetricsTick(ctx, MetricsTick{At: metricsT0, Fold: rows}); err != nil {
			t.Fatal(err)
		}
	}
	// Forty-eight hours of minute rows, six hours to a transaction.
	minuteStart := metricsT0.Add(-MinuteKeep)
	for block := range 8 {
		var rows MetricsRows
		for m := range 360 {
			ts := minuteStart.Add(time.Duration(block*360+m) * time.Minute)
			for i, id := range ids {
				rows.Samples = append(rows.Samples, row(id, ts, int64(block*360+m+i), 1))
			}
			rows.Hosts = append(rows.Hosts, HostSample{Node: "n1", TS: ts, MemoryUsedMB: i64(900), MemoryReservedMB: i64(700),
				MemoryTotalMB: i64(2048), CPUUsedMHz: i64(300), CPUReservedMHz: i64(400), CPUTotalMHz: i64(4000), CPUCores: i64(2)})
			rows.Disks = append(rows.Disks, DiskSample{Node: "n1", TS: ts, Mount: "/", Holds: "nomad,docker", UsedMB: 9000, TotalMB: 40000, FreeMB: 31000})
		}
		if err := s.WriteMetricsTick(ctx, MetricsTick{At: metricsT0, Minute: rows}); err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM metrics_samples").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if want := services * (2880 + 1152); count != want {
		t.Fatalf("sample rows = %d, want %d", count, want)
	}

	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, name := range []string{dbFile, dbFile + "-wal"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			total += info.Size()
		}
	}
	t.Logf("%d sample rows, plus host and disk rows: %.1f MB on disk after VACUUM", count, float64(total)/1e6)
	if total >= 10_000_000 {
		t.Errorf("the store is %d bytes, want under 10 MB", total)
	}
}
