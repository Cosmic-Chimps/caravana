package metrics_test

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
)

func TestBaselineStates(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	tests := []struct {
		name string
		now  time.Time
		want metrics.BaselineState
	}{
		{"the first minute", start, metrics.Building},
		{"just under a full day", start.Add(day - time.Second), metrics.Building},
		{"a full day", start.Add(day), metrics.Partial},
		{"six days and a bit", start.Add(7*day - time.Second), metrics.Partial},
		{"seven days", start.Add(7 * day), metrics.Full},
		{"a month", start.Add(30 * day), metrics.Full},
		{"a clock before the start", start.Add(-time.Hour), metrics.Building},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := metrics.Baseline(start, tc.now); got != tc.want {
				t.Errorf("Baseline = %s, want %s", got, tc.want)
			}
		})
	}
	if got := metrics.Baseline(time.Time{}, start); got != metrics.Building {
		t.Errorf("no start time: %s", got)
	}
}

// slotRow is a 10-minute row: n requests and e errors over 600 s, every
// request taking sec seconds, using mem MB.
func slotRow(at time.Time, n, e int64, sec float64, mem int64) metrics.Row {
	return metrics.Row{
		TS: at, Minutes: 10, HTTPSecs: p64(600), Requests: p64(n), Errors: p64(e),
		Hist: histAt(sec, n), MemoryMB: p64(mem),
	}
}

func TestBandOverSevenDays(t *testing.T) {
	at := base.Add(10*24*time.Hour + 14*time.Hour + 20*time.Minute) // today 14:20
	var rows []metrics.Row
	// Days 1..5 at 14:20: 600, 1200, 3000, 1800, 2400 requests in 10 minutes
	// (60, 120, 300, 180, 240 per minute), memory 100..500 MB.
	reqs := []int64{600, 1200, 3000, 1800, 2400}
	for d, n := range reqs {
		rows = append(rows, slotRow(at.Add(-time.Duration(d+1)*24*time.Hour), n, n/100, 0.04, int64(100*(d+1))))
	}
	// Not in the band: today's own slot, day 8, and other slots of day 1.
	rows = append(rows,
		slotRow(at, 99999, 0, 5, 9999),
		slotRow(at.Add(-8*24*time.Hour), 99999, 0, 5, 9999),
		slotRow(at.Add(-24*time.Hour-10*time.Minute), 99999, 0, 5, 9999),
		slotRow(at.Add(-24*time.Hour+10*time.Minute), 99999, 0, 5, 9999),
	)
	b := metrics.Band(rows, at.Add(3*time.Minute), metrics.SlotLength) // any instant of the slot
	check := func(name string, got metrics.BandRange, low, high, usual float64, days int) {
		t.Helper()
		if got.Low != low || got.High != high || got.Usual != usual || got.Days != days || !got.Has() {
			t.Errorf("%s = %+v, want low %v high %v usual %v days %d", name, got, low, high, usual, days)
		}
	}
	check("requests per minute", b.RequestsPerMin, 60, 300, 180, 5)
	check("error percent", b.ErrorPercent, 1, 1, 1, 5)
	check("memory", b.MemoryMB, 100, 500, 300, 5)
	if b.Days() != 5 {
		t.Errorf("Days() = %d, want 5", b.Days())
	}
	if !b.P95Millis.Has() || b.P95Millis.Low <= 25 || b.P95Millis.High > 50 || b.P95Millis.Days != 5 {
		t.Errorf("p95 band = %+v, want 5 days within (25, 50] ms", b.P95Millis)
	}
}

func TestBandMedianOfEvenDays(t *testing.T) {
	at := base.Add(5*24*time.Hour + 9*time.Hour)
	rows := []metrics.Row{
		slotRow(at.Add(-24*time.Hour), 600, 0, 0.04, 100),
		slotRow(at.Add(-48*time.Hour), 1200, 0, 0.04, 200),
	}
	b := metrics.Band(rows, at, metrics.SlotLength)
	if b.MemoryMB.Usual != 150 || b.RequestsPerMin.Usual != 90 || b.MemoryMB.Days != 2 {
		t.Errorf("band = %+v", b)
	}
}

func TestBandNoHistory(t *testing.T) {
	at := base.Add(3 * 24 * time.Hour)
	if b := metrics.Band(nil, at, metrics.SlotLength); b.Days() != 0 || b.MemoryMB.Has() || b.P95Millis.Has() {
		t.Errorf("a band from nothing: %+v", b)
	}
	// Only today's rows, or rows more than 7 days back: no band either.
	rows := []metrics.Row{slotRow(at, 600, 0, 0.04, 100), slotRow(at.Add(-8*24*time.Hour), 600, 0, 0.04, 100)}
	if b := metrics.Band(rows, at, metrics.SlotLength); b.Days() != 0 {
		t.Errorf("rows outside the 7 earlier days made a band: %+v", b)
	}
}

func TestBandSevenDaysEdge(t *testing.T) {
	at := base.Add(20*24*time.Hour + 6*time.Hour)
	rows := []metrics.Row{slotRow(at.Add(-7*24*time.Hour), 600, 0, 0.04, 100)}
	if b := metrics.Band(rows, at, metrics.SlotLength); b.Days() != 1 {
		t.Errorf("day 7 counts: %+v", b)
	}
}

func TestBandSkipsNULLsPerMetric(t *testing.T) {
	at := base.Add(5*24*time.Hour + 9*time.Hour)
	noHTTP := metrics.Row{TS: at.Add(-24 * time.Hour), Minutes: 10, MemoryMB: p64(100)} // a service without a domain
	noMem := metrics.Row{TS: at.Add(-48 * time.Hour), Minutes: 10, HTTPSecs: p64(600), Requests: p64(600), Errors: p64(0), Hist: histAt(0.04, 600)}
	zeroReq := metrics.Row{TS: at.Add(-72 * time.Hour), Minutes: 10, HTTPSecs: p64(600), Requests: p64(0), Errors: p64(0), MemoryMB: p64(300)}
	b := metrics.Band([]metrics.Row{noHTTP, noMem, zeroReq}, at, metrics.SlotLength)
	if b.MemoryMB.Days != 2 || b.MemoryMB.Low != 100 || b.MemoryMB.High != 300 {
		t.Errorf("memory band = %+v (the row without memory is skipped)", b.MemoryMB)
	}
	// Requests: 600/10min and 0; a quiet day is a value (0), a NULL is not.
	if b.RequestsPerMin.Days != 2 || b.RequestsPerMin.Low != 0 || b.RequestsPerMin.High != 60 {
		t.Errorf("requests band = %+v", b.RequestsPerMin)
	}
	// Error percent: the day with 0 requests has no share, so one day.
	if b.ErrorPercent.Days != 1 {
		t.Errorf("error band = %+v, want 1 day (no requests, no share)", b.ErrorPercent)
	}
	if b.P95Millis.Days != 1 {
		t.Errorf("p95 band = %+v, want 1 day (no timed request, no p95)", b.P95Millis)
	}
}

func TestBandMergesSixSlotsPerDayForAnHour(t *testing.T) {
	at := base.Add(10*24*time.Hour + 14*time.Hour) // today 14:00, a 7 d point
	var rows []metrics.Row
	// Day 1: six slots of 600 requests (3600 in the hour, 60 a minute), memory
	// 100 except one slot at 700. Day 2: six slots of 1200 (120 a minute).
	for i := 0; i < 6; i++ {
		s := time.Duration(i) * 10 * time.Minute
		mem := int64(100)
		if i == 3 {
			mem = 700
		}
		rows = append(rows, slotRow(at.Add(-24*time.Hour+s), 600, 0, 0.04, mem))
		rows = append(rows, slotRow(at.Add(-48*time.Hour+s), 1200, 0, 0.04, 200))
	}
	rows = append(rows, slotRow(at.Add(-24*time.Hour+time.Hour), 99999, 0, 0.04, 9999)) // the next hour
	b := metrics.Band(rows, at, time.Hour)
	if b.RequestsPerMin.Days != 2 || b.RequestsPerMin.Low != 60 || b.RequestsPerMin.High != 120 {
		t.Errorf("requests band = %+v, want 60..120 over 2 days (six slots merged per day)", b.RequestsPerMin)
	}
	// Memory of a day is the median of its six slots: 100, not the 700 spike.
	if b.MemoryMB.Low != 100 || b.MemoryMB.High != 200 {
		t.Errorf("memory band = %+v", b.MemoryMB)
	}
	// The same rows asked for as a 10-minute point see one slot only.
	b10 := metrics.Band(rows, at, metrics.SlotLength)
	if b10.RequestsPerMin.Low != 60 || b10.MemoryMB.Days != 2 {
		t.Errorf("10-minute band = %+v", b10)
	}
}

func TestBandForFinePointsUsesTheirSlot(t *testing.T) {
	at := base.Add(5*24*time.Hour + 9*time.Hour + 20*time.Minute)
	rows := []metrics.Row{slotRow(at.Add(-24*time.Hour), 600, 0, 0.04, 100)}
	for _, step := range []time.Duration{time.Minute, 5 * time.Minute} {
		// 09:27 is in the 09:20 slot.
		b := metrics.Band(rows, at.Add(7*time.Minute), step)
		if b.MemoryMB.Days != 1 {
			t.Errorf("step %v: %+v", step, b)
		}
	}
}

func TestHistoryAnswersManyPoints(t *testing.T) {
	at := base.Add(5*24*time.Hour + 9*time.Hour)
	rows := []metrics.Row{
		slotRow(at.Add(-24*time.Hour), 600, 0, 0.04, 100),
		slotRow(at.Add(-24*time.Hour+10*time.Minute), 600, 0, 0.04, 200),
	}
	h := metrics.NewHistory(rows)
	if got := h.Band(at, metrics.SlotLength).MemoryMB.Low; got != 100 {
		t.Errorf("first slot: %v", got)
	}
	if got := h.Band(at.Add(10*time.Minute), metrics.SlotLength).MemoryMB.Low; got != 200 {
		t.Errorf("second slot: %v", got)
	}
}
