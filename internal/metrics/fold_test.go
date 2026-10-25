package metrics_test

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
)

// base is a UTC midnight, so slot arithmetic in the tests is easy to read.
var base = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

func p64(v int64) *int64 { return &v }

// histAt is the histogram of n requests that all took sec seconds, in the
// Traefik layout.
func histAt(sec float64, n int64) metrics.Histogram {
	b := make(metrics.Buckets, len(layout)+1)
	i := len(layout)
	for j, bound := range layout {
		if sec <= bound {
			i = j
			break
		}
	}
	b[i] = n
	return metrics.Histogram{Layout: layout, Buckets: b.Trim()}
}

// minuteRow is a minute with http numbers: n requests and e errors in 60 s.
func minuteRow(at time.Time, n, e int64, h metrics.Histogram) metrics.Row {
	return metrics.Row{TS: at, Minutes: 1, HTTPSecs: p64(60), Requests: p64(n), Errors: p64(e), Hist: h}
}

func TestFoldCounters(t *testing.T) {
	rows := []metrics.Row{
		minuteRow(base.Add(time.Minute), 10, 1, histAt(0.04, 10)),
		minuteRow(base.Add(2*time.Minute), 20, 2, histAt(0.04, 20)),
		{TS: base.Add(3 * time.Minute), Minutes: 1, Restarts: p64(1)},
	}
	f := metrics.Fold(rows)
	r := f.Row
	if !r.TS.Equal(base) {
		t.Errorf("TS = %v, want the slot's start %v", r.TS, base)
	}
	if r.Minutes != 3 {
		t.Errorf("Minutes = %d, want 3", r.Minutes)
	}
	for _, c := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"requests", r.Requests, 30}, {"errors", r.Errors, 3}, {"http_secs", r.HTTPSecs, 120}, {"restarts", r.Restarts, 1},
	} {
		if c.got == nil || *c.got != c.want {
			t.Errorf("%s = %v, want %d", c.name, c.got, c.want)
		}
	}
	if got := r.Hist.Buckets.Total(); got != 30 {
		t.Errorf("buckets total = %d, want 30", got)
	}
	if f.SkippedLayouts != 0 {
		t.Errorf("skipped = %d", f.SkippedLayouts)
	}
}

func TestFoldNullWhenEveryRowIs(t *testing.T) {
	// No http numbers at all (a service without a domain): the counters stay
	// NULL, they do not become 0.
	rows := []metrics.Row{
		{TS: base, Minutes: 1, MemoryMB: p64(100)},
		{TS: base.Add(time.Minute), Minutes: 1, MemoryMB: p64(120)},
	}
	r := metrics.Fold(rows).Row
	if r.Requests != nil || r.Errors != nil || r.HTTPSecs != nil || r.Restarts != nil {
		t.Errorf("counters should be NULL: %+v", r)
	}
	if len(r.Hist.Buckets) != 0 {
		t.Errorf("buckets should be NULL: %+v", r.Hist)
	}
	if r.CPUMHz != nil || r.Copies != nil {
		t.Errorf("gauges nobody had should be NULL")
	}
	// A zero is a value: one row with 0 requests makes the sum 0, not NULL.
	r = metrics.Fold([]metrics.Row{{TS: base, Requests: p64(0)}, {TS: base.Add(time.Minute)}}).Row
	if r.Requests == nil || *r.Requests != 0 {
		t.Errorf("a zero request count is a value, got %v", r.Requests)
	}
}

func TestFoldGaugesMedian(t *testing.T) {
	tests := []struct {
		name string
		mem  []*int64
		want *int64
	}{
		{"odd", []*int64{p64(300), p64(100), p64(200)}, p64(200)},
		{"even takes the mean, rounded half up", []*int64{p64(100), p64(201), p64(300), p64(150)}, p64(176)},
		{"NULLs are skipped", []*int64{nil, p64(100), nil, p64(300), p64(200)}, p64(200)},
		{"one value", []*int64{nil, p64(7)}, p64(7)},
		{"all NULL", []*int64{nil, nil}, nil},
		{"a spike does not move the median", []*int64{p64(100), p64(100), p64(100), p64(5000), p64(100)}, p64(100)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rows []metrics.Row
			for i, m := range tc.mem {
				rows = append(rows, metrics.Row{TS: base.Add(time.Duration(i) * time.Minute), MemoryMB: m, CPUMHz: m, MemoryLimitMB: m, Copies: m, MemoryReservedMB: m, CPUReservedMHz: m})
			}
			r := metrics.Fold(rows).Row
			for name, got := range map[string]*int64{"memory": r.MemoryMB, "cpu": r.CPUMHz, "limit": r.MemoryLimitMB, "copies": r.Copies, "memres": r.MemoryReservedMB, "cpures": r.CPUReservedMHz} {
				switch {
				case tc.want == nil && got != nil:
					t.Errorf("%s = %d, want NULL", name, *got)
				case tc.want != nil && (got == nil || *got != *tc.want):
					t.Errorf("%s = %v, want %d", name, got, *tc.want)
				}
			}
		})
	}
}

func TestFoldSkipsOtherLayouts(t *testing.T) {
	old := metrics.Histogram{Layout: metrics.Layout{0.1, 1}, Buckets: metrics.Buckets{5, 5}}
	rows := []metrics.Row{
		minuteRow(base, 10, 0, old),                               // an older layout
		minuteRow(base.Add(time.Minute), 10, 0, histAt(0.04, 10)), // the newest layout wins
		minuteRow(base.Add(2*time.Minute), 10, 0, histAt(3, 10)),
	}
	f := metrics.Fold(rows)
	if f.SkippedLayouts != 1 {
		t.Errorf("skipped = %d, want 1", f.SkippedLayouts)
	}
	if got := f.Row.Hist.Buckets.Total(); got != 20 {
		t.Errorf("buckets total = %d, want 20 (the old layout's 10 left out)", got)
	}
	if !f.Row.Hist.Layout.Equal(layout) {
		t.Errorf("layout = %v", f.Row.Hist.Layout)
	}
	// Requests still count every row's: only the buckets are skipped.
	if *f.Row.Requests != 30 {
		t.Errorf("requests = %d, want 30", *f.Row.Requests)
	}
}

func TestFoldEmpty(t *testing.T) {
	if f := metrics.Fold(nil); f.Row.Minutes != 0 || !f.Row.TS.IsZero() {
		t.Errorf("Fold(nil) = %+v", f)
	}
}

func TestFoldMinutesOfSlotRows(t *testing.T) {
	// Folding 10-minute rows (a 7 d point) adds their minutes.
	rows := []metrics.Row{{TS: base, Minutes: 10}, {TS: base.Add(10 * time.Minute), Minutes: 7}, {TS: base.Add(20 * time.Minute)}}
	if got := metrics.Fold(rows).Row.Minutes; got != 18 {
		t.Errorf("Minutes = %d, want 18 (a row that says 0 counts as 1)", got)
	}
}

func TestRangeNumbers(t *testing.T) {
	want := map[string]struct {
		n    int
		step time.Duration
		slot bool
	}{
		"1h": {60, time.Minute, false}, "6h": {72, 5 * time.Minute, false},
		"24h": {144, 10 * time.Minute, true}, "7d": {168, time.Hour, true},
	}
	if len(metrics.Ranges()) != len(want) {
		t.Fatalf("Ranges() = %d", len(metrics.Ranges()))
	}
	for name, w := range want {
		r, ok := metrics.ParseRange(name)
		if !ok {
			t.Fatalf("ParseRange(%q) refused", name)
		}
		if r.N() != w.n || r.Step != w.step || r.FromSlots != w.slot {
			t.Errorf("%s: n=%d step=%v slots=%v, want %+v", name, r.N(), r.Step, r.FromSlots, w)
		}
	}
	if _, ok := metrics.ParseRange("2h"); ok {
		t.Error(`ParseRange("2h") should refuse`)
	}
}

func TestRangeFromEndsAtNow(t *testing.T) {
	now := base.Add(13*time.Hour + 47*time.Minute + 20*time.Second)
	for _, r := range metrics.Ranges() {
		from := r.From(now)
		last := from.Add(time.Duration(r.N()-1) * r.Step)
		if now.Before(last) || !now.Before(last.Add(r.Step)) {
			t.Errorf("%s: the last point [%v, +%v) must contain now %v", r.Name, last, r.Step, now)
		}
		if from.Truncate(r.Step) != from {
			t.Errorf("%s: from %v is not aligned to %v", r.Name, from, r.Step)
		}
	}
}

func TestPointsSumsAndGaps(t *testing.T) {
	from := base.Add(10 * time.Minute)
	step := 5 * time.Minute
	rows := []metrics.Row{
		// point 0: two rows
		minuteRow(from, 60, 6, histAt(0.04, 60)),
		minuteRow(from.Add(time.Minute), 120, 0, histAt(0.04, 120)),
		// point 1: nothing (a gap, not a zero)
		// point 2: one row with a zero count
		minuteRow(from.Add(10*time.Minute), 0, 0, metrics.Histogram{}),
		// before and after the range: ignored
		minuteRow(from.Add(-time.Minute), 999, 0, histAt(1, 999)),
		minuteRow(from.Add(15*time.Minute), 999, 0, histAt(1, 999)),
	}
	pts := metrics.Points(rows, from, step, 3)
	if len(pts) != 3 {
		t.Fatalf("points = %d", len(pts))
	}
	if !pts[0].T.Equal(from) || !pts[2].T.Equal(from.Add(10*time.Minute)) {
		t.Errorf("point times: %v %v", pts[0].T, pts[2].T)
	}
	if pts[0].Gap() || pts[0].Rows != 2 {
		t.Errorf("point 0: %+v", pts[0])
	}
	rate, ok := pts[0].RequestsPerSec()
	if !ok || rate != 1.5 { // 180 requests over 120 seconds covered
		t.Errorf("rate = %v %v, want 1.5", rate, ok)
	}
	pct, ok := pts[0].ErrorPercent()
	if !ok || pct != 6.0/180*100 {
		t.Errorf("error percent = %v %v", pct, ok)
	}
	if ms, over, ok := pts[0].P95(); !ok || over || ms <= 25 || ms > 50 {
		t.Errorf("p95 = %v over=%v ok=%v, want within (25, 50] ms", ms, over, ok)
	}
	if !pts[1].Gap() {
		t.Errorf("point 1 should be a gap: %+v", pts[1])
	}
	if _, ok := pts[1].RequestsPerSec(); ok {
		t.Error("a gap has no rate")
	}
	if pts[2].Gap() {
		t.Error("a row with zero requests is not a gap")
	}
	if rate, ok := pts[2].RequestsPerSec(); !ok || rate != 0 {
		t.Errorf("a quiet minute is a rate of 0, got %v %v", rate, ok)
	}
	if _, _, ok := pts[2].P95(); ok {
		t.Error("no timed request, no p95")
	}
	if _, ok := pts[2].ErrorPercent(); ok {
		t.Error("no requests, no error percent")
	}
}

func TestPointsRateOverSecondsCovered(t *testing.T) {
	// A 10-minute row that holds 3 minutes of data covers 180 s, not 600.
	rows := []metrics.Row{{TS: base, Minutes: 3, HTTPSecs: p64(180), Requests: p64(180)}}
	pts := metrics.Points(rows, base, 10*time.Minute, 1)
	if rate, ok := pts[0].RequestsPerSec(); !ok || rate != 1 {
		t.Errorf("rate = %v %v, want 1", rate, ok)
	}
}

func TestPointsGaugesMedianNullIsGap(t *testing.T) {
	rows := []metrics.Row{
		{TS: base, MemoryMB: p64(100)},
		{TS: base.Add(time.Minute), MemoryMB: p64(900)},
		{TS: base.Add(2 * time.Minute), MemoryMB: p64(120)},
		{TS: base.Add(5 * time.Minute)}, // a row, but memory NULL: that metric is a gap
	}
	pts := metrics.Points(rows, base, 5*time.Minute, 2)
	if pts[0].Row.MemoryMB == nil || *pts[0].Row.MemoryMB != 120 {
		t.Errorf("memory median = %v, want 120", pts[0].Row.MemoryMB)
	}
	if pts[1].Gap() || pts[1].Row.MemoryMB != nil {
		t.Errorf("point 1 has a row but no memory: %+v", pts[1])
	}
}

func TestPointsUnsortedAndExactEdges(t *testing.T) {
	// The first instant of a point belongs to it; the instant of its end to the next.
	rows := []metrics.Row{
		minuteRow(base.Add(5*time.Minute), 2, 0, metrics.Histogram{}),
		minuteRow(base, 1, 0, metrics.Histogram{}),
		minuteRow(base.Add(10*time.Minute), 4, 0, metrics.Histogram{}),
	}
	pts := metrics.Points(rows, base, 5*time.Minute, 2)
	if *pts[0].Row.Requests != 1 || *pts[1].Row.Requests != 2 {
		t.Errorf("requests per point = %d %d, want 1 2", *pts[0].Row.Requests, *pts[1].Row.Requests)
	}
}

func TestSlotsReadsTheSlotInProgress(t *testing.T) {
	done := metrics.Row{TS: base, Minutes: 10, HTTPSecs: p64(600), Requests: p64(600)}
	// Minutes of the folded slot are not added a second time; the minutes of
	// the slot in progress (no stored row yet) become one row.
	minutes := []metrics.Row{
		minuteRow(base.Add(3*time.Minute), 99, 0, metrics.Histogram{}),
		minuteRow(base.Add(10*time.Minute), 60, 0, metrics.Histogram{}),
		minuteRow(base.Add(11*time.Minute), 60, 0, metrics.Histogram{}),
	}
	got := metrics.Slots([]metrics.Row{done}, minutes)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if !got[0].TS.Equal(base) || *got[0].Requests != 600 {
		t.Errorf("stored slot was changed: %+v", got[0])
	}
	if !got[1].TS.Equal(base.Add(10*time.Minute)) || *got[1].Requests != 120 || got[1].Minutes != 2 || *got[1].HTTPSecs != 120 {
		t.Errorf("slot in progress: %+v", got[1])
	}
	// Those rows feed a 24 h-style point directly.
	pts := metrics.Points(got, base, 10*time.Minute, 2)
	if r, ok := pts[1].RequestsPerSec(); !ok || r != 1 {
		t.Errorf("rate of the slot in progress = %v %v, want 1", r, ok)
	}
}
