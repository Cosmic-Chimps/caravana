package metrics_test

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
)

// ruleNow is a whole minute; the window is the 10 minute rows ending at it.
var ruleNow = base.Add(14 * time.Hour)

// window makes the 10 minute rows ending at ruleNow, each with perMin requests
// and errPerMin 5xx, every request taking sec seconds.
func window(perMin, errPerMin int64, sec float64) []metrics.Row {
	var rows []metrics.Row
	for i := 9; i >= 0; i-- {
		rows = append(rows, minuteRow(ruleNow.Add(-time.Duration(i)*time.Minute), perMin, errPerMin, histAt(sec, perMin)))
	}
	return rows
}

// withMemory sets memory and limit on every row.
func withMemory(rows []metrics.Row, mem, limit int64) []metrics.Row {
	for i := range rows {
		rows[i].MemoryMB, rows[i].MemoryLimitMB = p64(mem), p64(limit)
	}
	return rows
}

func band(low, high, usual float64) metrics.BandRange {
	return metrics.BandRange{Low: low, High: high, Usual: usual, Days: 7}
}

func kinds(is []metrics.Issue) []metrics.Kind {
	var out []metrics.Kind
	for _, i := range is {
		out = append(out, i.Kind)
	}
	return out
}

func has(is []metrics.Issue, k metrics.Kind) (metrics.Issue, bool) {
	for _, i := range is {
		if i.Kind == k {
			return i, true
		}
	}
	return metrics.Issue{}, false
}

func TestSlowerHolds(t *testing.T) {
	tests := []struct {
		name      string
		p95, high float64
		want      bool
	}{
		{"exactly 50% above", 300, 200, false},
		{"just over 50% above", 300.01, 200, true},
		{"just under 50% above", 299.99, 200, false},
		{"under the band's top", 100, 200, false},
		{"margin: 100 ms above the top exactly", 150, 50, true},
		{"margin: just under 100 ms above", 149.99, 50, false},
		{"margin: well over 50% but under 100 ms", 120, 50, false},
		{"a band of 0", 100, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := metrics.SlowerHolds(tc.p95, tc.high); got != tc.want {
				t.Errorf("SlowerHolds(%v, %v) = %v", tc.p95, tc.high, got)
			}
		})
	}
}

func TestMoreErrorsHolds(t *testing.T) {
	tests := []struct {
		name      string
		pct, high float64
		want      bool
	}{
		{"exactly 1%", 1, 0, false},
		{"just over 1%", 1.01, 0, true},
		{"just under 1%", 0.99, 0, false},
		{"equal to the band's top", 5, 5, false},
		{"just over the band's top", 5.01, 5, true},
		{"over 1% but under the top", 2, 3, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := metrics.MoreErrorsHolds(tc.pct, tc.high); got != tc.want {
				t.Errorf("MoreErrorsHolds(%v, %v) = %v", tc.pct, tc.high, got)
			}
		})
	}
}

func TestQuieterHolds(t *testing.T) {
	tests := []struct {
		name        string
		perMin, low float64
		want        bool
	}{
		{"exactly half", 5, 10, false},
		{"just under half", 4.99, 10, true},
		{"just over half", 5.01, 10, false},
		{"stopped", 0, 10, true},
		{"the bottom is exactly 1 a minute", 0.49, 1, true},
		{"the bottom is exactly 1, half", 0.5, 1, false},
		{"the bottom is just under 1 a minute", 0, 0.99, false},
		{"no traffic usually", 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := metrics.QuieterHolds(tc.perMin, tc.low); got != tc.want {
				t.Errorf("QuieterHolds(%v, %v) = %v", tc.perMin, tc.low, got)
			}
		})
	}
}

func TestJudgeSlowerNeedsTwentyTimedRequests(t *testing.T) {
	b := metrics.Bands{P95Millis: band(100, 200, 150)}
	// total requests spread over the window, all in the (0.25, 0.5] s bucket:
	// p95 = 250 + 250 * 19/20 = 487.5 ms, at any total.
	rows := func(total int) []metrics.Row {
		out := window(0, 0, 0.4)
		for i := 0; i < total; i++ {
			r := &out[i%10]
			*r.Requests++
			if len(r.Hist.Buckets) == 0 {
				r.Hist.Buckets = make(metrics.Buckets, 7)
			}
			r.Hist.Buckets[6]++
		}
		return out
	}
	for _, n := range []int{19, 20, 21} {
		is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rows(n), Band: b, State: metrics.Full})
		i, got := has(is, metrics.Slower)
		if want := n >= 20; got != want {
			t.Errorf("%d timed requests: slower = %v, want %v (%v)", n, got, want, kinds(is))
		}
		if n == 20 {
			if i.Value < 487.4 || i.Value > 487.6 || i.Usual != 150 || i.High != 200 || i.Low != 100 {
				t.Errorf("issue = %+v, want value 487.5 against 100..200 usual 150", i)
			}
			if d := i.Distance; d < 2.43 || d > 2.44 {
				t.Errorf("distance = %v, want value/high = 2.4375", d)
			}
			if i.Level != metrics.LevelWarning {
				t.Errorf("level = %s", i.Level)
			}
		}
	}
}

func TestJudgeSlowerUsesMergedBucketsNotMinuteMedians(t *testing.T) {
	// Nine quiet-fast minutes and one minute of 100 slow requests: the median
	// minute is fast, the merged window has 5% above 500 ms (950th of 1000 is
	// in the slow bucket), so the slowest 5% is slow.
	rows := window(100, 0, 0.01)
	rows[4] = minuteRow(rows[4].TS, 100, 0, histAt(0.8, 100)) // (0.5, 1] s
	b := metrics.Bands{P95Millis: band(5, 20, 10)}
	is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rows, Band: b, State: metrics.Full})
	i, ok := has(is, metrics.Slower)
	if !ok {
		t.Fatalf("a burst of slow requests should count: %v", kinds(is))
	}
	if i.Value < 749 || i.Value > 751 { // 500 + 500 × 50/100
		t.Errorf("p95 = %v, want 750 ms", i.Value)
	}
}

func TestJudgeMoreErrorsThresholds(t *testing.T) {
	run := func(perMin int64, errs []int64, bd metrics.BandRange) ([]metrics.Issue, metrics.Window) {
		rows := window(perMin, 0, 0.04)
		for i, e := range errs {
			rows[i].Errors = p64(e)
		}
		is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rows, Band: metrics.Bands{ErrorPercent: bd}, State: metrics.Full})
		return is, metrics.WindowOf(rows, ruleNow)
	}
	tests := []struct {
		name   string
		perMin int64
		errs   []int64 // per minute from the oldest
		band   metrics.BandRange
		want   bool
	}{
		{"10 of 1000 is exactly 1%", 100, []int64{10}, band(0, 0.3, 0.1), false},
		{"11 of 1000 is just over 1%", 100, []int64{11}, band(0, 0.3, 0.1), true},
		{"9 of 1000 is just under 1%", 100, []int64{9}, band(0, 0.3, 0.1), false},
		{"5 errors of 100 (5%): at the count", 10, []int64{5}, band(0, 0.3, 0.1), true},
		{"4 errors of 100 (4%): just under the count", 10, []int64{4}, band(0, 0.3, 0.1), false},
		{"6 errors of 100", 10, []int64{3, 3}, band(0, 0.3, 0.1), true},
		{"5% against a band that tops at 5%", 10, []int64{5}, band(0, 5, 1), false},
		{"6% against a band that tops at 5%", 10, []int64{6}, band(0, 5, 1), true},
		{"no band at that time of day", 10, []int64{9}, metrics.BandRange{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is, w := run(tc.perMin, tc.errs, tc.band)
			i, got := has(is, metrics.MoreErrors)
			if got != tc.want {
				t.Fatalf("more_errors = %v, want %v (window %+v)", got, tc.want, w)
			}
			if got && (i.Value != w.ErrorPercent || i.High != tc.band.High) {
				t.Errorf("issue = %+v", i)
			}
		})
	}
}

func TestJudgeQuieter(t *testing.T) {
	b := metrics.Bands{RequestsPerMin: band(10, 120, 60)}
	for _, tc := range []struct {
		name   string
		perMin int64
		low    float64
		want   bool
	}{
		{"half of the bottom", 5, 10, false},
		{"just under half", 4, 10, true},
		{"stopped", 0, 10, true},
		{"a bottom under 1 a minute", 0, 0.9, false},
		{"a bottom of 1 a minute, stopped", 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bb := b
			bb.RequestsPerMin.Low = tc.low
			is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: window(tc.perMin, 0, 0.04), Band: bb, State: metrics.Full})
			i, got := has(is, metrics.Quieter)
			if got != tc.want {
				t.Fatalf("quieter = %v, want %v", got, tc.want)
			}
			if got {
				// Value and usual are per second: 4 a minute against 60 usual.
				if want := float64(tc.perMin) / 60; i.Value != want || i.Usual != 1 {
					t.Errorf("issue = %+v, want value %v usual 1", i, want)
				}
			}
		})
	}
	// A distance of band bottom / value; a value of 0 is the cap.
	is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: window(2, 0, 0.04), Band: b, State: metrics.Full})
	if i, _ := has(is, metrics.Quieter); i.Distance != 5 {
		t.Errorf("distance = %v, want 10 / 2 = 5", i.Distance)
	}
	is = metrics.Judge(metrics.Facts{Now: ruleNow, Rows: window(0, 0, 0.04), Band: b, State: metrics.Full})
	if i, _ := has(is, metrics.Quieter); i.Distance != metrics.MaxDistance {
		t.Errorf("distance with no traffic = %v, want %v", i.Distance, metrics.MaxDistance)
	}
}

func TestJudgeMemory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mem, limit int64
		want       bool
		level      metrics.Level
	}{
		{"exactly 90%", 900, 1000, false, ""},
		{"just over 90%", 901, 1000, true, metrics.LevelWarning},
		{"just under 90%", 899, 1000, false, ""},
		{"exactly 98%", 980, 1000, true, metrics.LevelWarning},
		{"just over 98%", 981, 1000, true, metrics.LevelFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := withMemory(window(10, 0, 0.04), tc.mem, tc.limit)
			// Memory is absolute: no band, and a Building baseline, still judge it.
			is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rows, State: metrics.Building})
			i, got := has(is, metrics.Memory)
			if got != tc.want {
				t.Fatalf("memory = %v, want %v", got, tc.want)
			}
			if got {
				if i.Level != tc.level {
					t.Errorf("level = %s, want %s", i.Level, tc.level)
				}
				if want := float64(tc.mem) * 100 / float64(tc.limit); i.Value != want {
					t.Errorf("value = %v, want %v", i.Value, want)
				}
				if want := i.Value / 90; i.Distance != want {
					t.Errorf("distance = %v, want %v", i.Distance, want)
				}
			}
		})
	}
}

func TestJudgeMemoryIsTheMedian(t *testing.T) {
	mk := func(high int) []metrics.Row {
		rows := withMemory(window(10, 0, 0.04), 500, 1000)
		for i := 0; i < high; i++ {
			rows[i].MemoryMB = p64(950)
		}
		return rows
	}
	for high, want := range map[int]bool{1: false, 4: false, 5: false, 6: true, 10: true} {
		is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: mk(high), State: metrics.Full})
		if _, got := has(is, metrics.Memory); got != want {
			t.Errorf("%d of 10 minutes at 95%%: memory = %v, want %v", high, got, want)
		}
	}
	// Rows without a limit or without memory are not in the median.
	rows := withMemory(window(10, 0, 0.04), 950, 1000)
	rows[0].MemoryLimitMB, rows[1].MemoryMB = nil, nil
	if _, got := has(metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rows, State: metrics.Full}), metrics.Memory); !got {
		t.Error("NULL rows should be skipped, the rest are over 90%")
	}
	rows = window(10, 0, 0.04) // no memory at all: nothing to judge
	if _, got := has(metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rows, State: metrics.Full}), metrics.Memory); got {
		t.Error("no memory numbers, no memory issue")
	}
}

func TestJudgeRestarting(t *testing.T) {
	for _, tc := range []struct {
		restarts int
		known    bool
		want     bool
	}{
		{1, true, false}, {2, true, true}, {3, true, true}, {9, false, false}, {0, true, false},
	} {
		is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: window(10, 0, 0.04), State: metrics.Building, Restarts: tc.restarts, RestartsKnown: tc.known})
		i, got := has(is, metrics.Restarting)
		if got != tc.want {
			t.Errorf("%d restarts (known %v): restarting = %v, want %v", tc.restarts, tc.known, got, tc.want)
		}
		if got && (i.Restarts != tc.restarts || i.Distance != 1+float64(tc.restarts)/10) {
			t.Errorf("issue = %+v", i)
		}
	}
}

func TestJudgeLevelFailed(t *testing.T) {
	bd := metrics.Bands{ErrorPercent: band(0, 0.3, 0.1)}
	// 10% of 1000: warning; 10.1%: failed (errors above 10%).
	for _, tc := range []struct {
		errs int64
		want metrics.Level
	}{{100, metrics.LevelWarning}, {101, metrics.LevelFailed}} {
		rows := window(100, 0, 0.04)
		rows[0].Errors = p64(tc.errs)
		is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rows, Band: bd, State: metrics.Full})
		i, ok := has(is, metrics.MoreErrors)
		if !ok || i.Level != tc.want {
			t.Errorf("%d errors of 1000: %+v, want level %s", tc.errs, i, tc.want)
		}
	}
	// The mark is the service's: memory at 99% makes a slower issue failed too.
	rows := withMemory(window(10, 0, 5), 990, 1000)
	is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rows, Band: metrics.Bands{P95Millis: band(1, 10, 5)}, State: metrics.Full})
	if i, ok := has(is, metrics.Slower); !ok || i.Level != metrics.LevelFailed {
		t.Errorf("slower with memory over 98%%: %+v", i)
	}
}

func TestJudgeStateAndFlags(t *testing.T) {
	// A service that breaks every rule at once.
	slowAndFailing := window(10, 8, 5)
	for i := range slowAndFailing {
		slowAndFailing[i].MemoryMB, slowAndFailing[i].MemoryLimitMB = p64(990), p64(1000)
	}
	bands := metrics.Bands{
		RequestsPerMin: band(100, 200, 150), ErrorPercent: band(0, 0.3, 0.1), P95Millis: band(5, 20, 10),
	}
	facts := func(f func(*metrics.Facts)) metrics.Facts {
		x := metrics.Facts{Now: ruleNow, Rows: slowAndFailing, Band: bands, State: metrics.Full, Restarts: 3, RestartsKnown: true}
		f(&x)
		return x
	}
	tests := []struct {
		name string
		f    func(*metrics.Facts)
		want []metrics.Kind
	}{
		{"full", func(*metrics.Facts) {}, []metrics.Kind{metrics.Slower, metrics.MoreErrors, metrics.Quieter, metrics.Memory, metrics.Restarting}},
		{"partial judges like full", func(x *metrics.Facts) { x.State = metrics.Partial }, []metrics.Kind{metrics.Slower, metrics.MoreErrors, metrics.Quieter, metrics.Memory, metrics.Restarting}},
		{"building: only memory and restarting", func(x *metrics.Facts) { x.State = metrics.Building }, []metrics.Kind{metrics.Memory, metrics.Restarting}},
		{"rolling out: no HTTP rules", func(x *metrics.Facts) { x.RollingOut = true }, []metrics.Kind{metrics.Memory, metrics.Restarting}},
		{"stopped: not judged at all", func(x *metrics.Facts) { x.Stopped = true }, nil},
		{"no bands: only the absolute rules", func(x *metrics.Facts) { x.Band = metrics.Bands{} }, []metrics.Kind{metrics.Memory, metrics.Restarting}},
		{"Nomad unreadable: restarting unjudged", func(x *metrics.Facts) { x.RestartsKnown = false }, []metrics.Kind{metrics.Slower, metrics.MoreErrors, metrics.Quieter, metrics.Memory}},
		{"no rows at all", func(x *metrics.Facts) { x.Rows = nil }, []metrics.Kind{metrics.Restarting}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kinds(metrics.Judge(facts(tc.f)))
			if len(got) != len(tc.want) {
				t.Fatalf("kinds = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("kinds = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestJudgeWindowEdges(t *testing.T) {
	bd := metrics.Bands{RequestsPerMin: band(10, 120, 60)}
	rows := window(0, 0, 0.04) // a service with no traffic: quieter
	judge := func(rs []metrics.Row) bool {
		_, got := has(metrics.Judge(metrics.Facts{Now: ruleNow, Rows: rs, Band: bd, State: metrics.Full}), metrics.Quieter)
		return got
	}
	if !judge(rows) {
		t.Fatal("the plain window should be quieter")
	}
	// The row at now−10m is outside the window, the row at now is inside;
	// a row after now is not yet.
	busy := minuteRow(ruleNow.Add(-10*time.Minute), 1000, 0, histAt(0.04, 1000))
	future := minuteRow(ruleNow.Add(time.Minute), 1000, 0, histAt(0.04, 1000))
	if !judge(append(append([]metrics.Row(nil), rows...), busy, future)) {
		t.Error("rows outside (now−10m, now] must not count")
	}
	w := metrics.WindowOf(append(append([]metrics.Row(nil), rows...), busy, future), ruleNow)
	if w.Rows != 10 {
		t.Errorf("window rows = %d, want 10", w.Rows)
	}
	// A window with no HTTP numbers (no domain, Traefik unread) judges nothing HTTP.
	noHTTP := withMemory(window(0, 0, 0.04), 100, 1000)
	for i := range noHTTP {
		noHTTP[i].HTTPSecs, noHTTP[i].Requests, noHTTP[i].Errors, noHTTP[i].Hist = nil, nil, nil, metrics.Histogram{}
	}
	if is := metrics.Judge(metrics.Facts{Now: ruleNow, Rows: noHTTP, Band: bd, State: metrics.Full}); len(is) != 0 {
		t.Errorf("issues without HTTP numbers: %v", kinds(is))
	}
}

func TestSortByDistance(t *testing.T) {
	is := []metrics.Issue{{Kind: metrics.Memory, Distance: 1.2}, {Kind: metrics.Slower, Distance: 4}, {Kind: metrics.Restarting, Distance: 1.3}}
	metrics.SortByDistance(is)
	if is[0].Kind != metrics.Slower || is[1].Kind != metrics.Restarting || is[2].Kind != metrics.Memory {
		t.Errorf("order: %v", kinds(is))
	}
}

func TestDistanceCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		i    metrics.Issue
		want float64
	}{
		{"slower", metrics.Issue{Kind: metrics.Slower, Value: 820, High: 200}, 4.1},
		{"errors with a band top of 0", metrics.Issue{Kind: metrics.MoreErrors, Value: 2, High: 0}, metrics.MaxDistance},
		{"quieter", metrics.Issue{Kind: metrics.Quieter, Value: 2, Low: 10}, 5},
		{"quieter at 0", metrics.Issue{Kind: metrics.Quieter, Value: 0, Low: 10}, metrics.MaxDistance},
		{"memory", metrics.Issue{Kind: metrics.Memory, Value: 99}, 1.1},
		{"restarting", metrics.Issue{Kind: metrics.Restarting, Restarts: 5}, 1.5},
		{"capped", metrics.Issue{Kind: metrics.Slower, Value: 1e6, High: 1}, metrics.MaxDistance},
	} {
		if got := metrics.Distance(tc.i); got != tc.want {
			t.Errorf("%s: Distance = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// --- Since ---

// run makes minute rows from `from` (n of them), each made by f(i).
func run(from time.Time, n int, f func(i int) metrics.Row) []metrics.Row {
	var rows []metrics.Row
	for i := 0; i < n; i++ {
		r := f(i)
		r.TS = from.Add(time.Duration(i) * time.Minute)
		r.Minutes = 1
		rows = append(rows, r)
	}
	return rows
}

func memRow(mem int64) metrics.Row {
	return metrics.Row{MemoryMB: p64(mem), MemoryLimitMB: p64(1000)}
}

func noBand(time.Time) metrics.Bands { return metrics.Bands{} }

func TestSinceMemoryRun(t *testing.T) {
	start := ruleNow.Add(-30 * time.Minute)
	// 18 normal minutes, then 12 above 90%.
	rows := run(start, 30, func(i int) metrics.Row {
		if i < 18 {
			return memRow(500)
		}
		return memRow(950)
	})
	at, capped, ok := metrics.Since(metrics.Memory, rows, ruleNow, noBand)
	if !ok || capped || !at.Equal(start.Add(18*time.Minute)) {
		t.Errorf("Since = %v capped=%v ok=%v, want %v", at, capped, ok, start.Add(18*time.Minute))
	}
}

func TestSinceMemoryNoDataNeitherBreaksNorEnds(t *testing.T) {
	start := ruleNow.Add(-30 * time.Minute)
	// normal 0..9, high 10..14, NO DATA 15..19 (NULL memory), high 20..29.
	rows := run(start, 30, func(i int) metrics.Row {
		switch {
		case i < 10:
			return memRow(500)
		case i < 15, i >= 20:
			return memRow(950)
		default:
			return metrics.Row{CPUMHz: p64(1)}
		}
	})
	at, _, ok := metrics.Since(metrics.Memory, rows, ruleNow, noBand)
	if !ok || !at.Equal(start.Add(10*time.Minute)) {
		t.Errorf("a no-data stretch must not end the run: got %v ok=%v, want %v", at, ok, start.Add(10*time.Minute))
	}
	// And a missing row (a minute the sampler didn't write) is the same.
	gapped := append(append([]metrics.Row(nil), rows[:15]...), rows[20:]...)
	at, _, ok = metrics.Since(metrics.Memory, gapped, ruleNow, noBand)
	if !ok || !at.Equal(start.Add(10*time.Minute)) {
		t.Errorf("missing rows: got %v ok=%v", at, ok)
	}
}

func TestSinceNewestMinuteWithinIsNoRun(t *testing.T) {
	rows := run(ruleNow.Add(-5*time.Minute), 6, func(i int) metrics.Row {
		if i == 5 {
			return memRow(500) // the newest minute is back to normal
		}
		return memRow(950)
	})
	if _, _, ok := metrics.Since(metrics.Memory, rows, ruleNow, noBand); ok {
		t.Error("when the newest judged minute does not break there is no run to date")
	}
	if _, _, ok := metrics.Since(metrics.Restarting, rows, ruleNow, noBand); ok {
		t.Error("restarting has no rows to walk")
	}
	if _, _, ok := metrics.Since(metrics.Memory, nil, ruleNow, noBand); ok {
		t.Error("no rows, no run")
	}
}

func TestSinceCapAtSixHours(t *testing.T) {
	// Eight hours of memory over 90%: the walk stops at six and says so.
	start := ruleNow.Add(-8 * time.Hour)
	rows := run(start, 8*60+1, func(int) metrics.Row { return memRow(950) })
	at, capped, ok := metrics.Since(metrics.Memory, rows, ruleNow, noBand)
	if !ok || !capped {
		t.Fatalf("Since = %v capped=%v ok=%v, want capped", at, capped, ok)
	}
	if at.Before(ruleNow.Add(-6 * time.Hour)) {
		t.Errorf("at = %v goes past the 6 hour limit", at)
	}
	// A run that began 2 hours ago is not capped.
	rows = run(ruleNow.Add(-2*time.Hour), 121, func(int) metrics.Row { return memRow(950) })
	at, capped, ok = metrics.Since(metrics.Memory, rows, ruleNow, noBand)
	if !ok || capped || !at.Equal(ruleNow.Add(-2*time.Hour)) {
		t.Errorf("a 2 hour run: %v capped=%v ok=%v", at, capped, ok)
	}
	// A run that ended 7 hours ago and broke again: not capped, ended inside the limit.
	rows = run(ruleNow.Add(-7*time.Hour), 7*60+1, func(i int) metrics.Row {
		if i < 60 {
			return memRow(500)
		}
		return memRow(950)
	})
	_, capped, _ = metrics.Since(metrics.Memory, rows, ruleNow, noBand)
	if !capped {
		t.Error("the normal hour is beyond the limit, so the run is still capped")
	}
}

func TestSinceMoreErrorsAgainstEachMinutesBand(t *testing.T) {
	start := ruleNow.Add(-20 * time.Minute)
	// 2% errors every minute; the band's top is 5% for the first 10 minutes
	// (so they don't break), 0.3% after (so they do).
	rows := run(start, 20, func(int) metrics.Row {
		return metrics.Row{HTTPSecs: p64(60), Requests: p64(100), Errors: p64(2)}
	})
	bandAt := func(t time.Time) metrics.Bands {
		if t.Before(start.Add(10 * time.Minute)) {
			return metrics.Bands{ErrorPercent: band(0, 5, 1)}
		}
		return metrics.Bands{ErrorPercent: band(0, 0.3, 0.1)}
	}
	at, _, ok := metrics.Since(metrics.MoreErrors, rows, ruleNow, bandAt)
	if !ok || !at.Equal(start.Add(10*time.Minute)) {
		t.Errorf("Since = %v ok=%v, want the minute the band changed, %v", at, ok, start.Add(10*time.Minute))
	}
	// Minutes with no requests and minutes with no band are neutral.
	rows[12] = metrics.Row{TS: rows[12].TS, HTTPSecs: p64(60), Requests: p64(0), Errors: p64(0)}
	at, _, ok = metrics.Since(metrics.MoreErrors, rows, ruleNow, bandAt)
	if !ok || !at.Equal(start.Add(10*time.Minute)) {
		t.Errorf("a minute with no requests ended the run: %v", at)
	}
	if _, _, ok := metrics.Since(metrics.MoreErrors, rows, ruleNow, noBand); ok {
		t.Error("without a band nothing can be judged")
	}
}

func TestSinceQuieter(t *testing.T) {
	start := ruleNow.Add(-15 * time.Minute)
	// 100 a minute for 5 minutes, then 3 a minute (band bottom 10, half is 5).
	rows := run(start, 15, func(i int) metrics.Row {
		n := int64(3)
		if i < 5 {
			n = 100
		}
		return metrics.Row{HTTPSecs: p64(60), Requests: p64(n)}
	})
	bandAt := func(time.Time) metrics.Bands { return metrics.Bands{RequestsPerMin: band(10, 200, 100)} }
	at, _, ok := metrics.Since(metrics.Quieter, rows, ruleNow, bandAt)
	if !ok || !at.Equal(start.Add(5*time.Minute)) {
		t.Errorf("Since = %v ok=%v, want %v", at, ok, start.Add(5*time.Minute))
	}
}

func TestSinceSlowerLooksAtFiveMinutes(t *testing.T) {
	start := ruleNow.Add(-20 * time.Minute)
	bandAt := func(time.Time) metrics.Bands { return metrics.Bands{P95Millis: band(10, 20, 15)} }
	// 10 requests a minute: 40 ms for 10 minutes, then 800 ms. The 5 minutes
	// ending at minute 10 hold one slow and four fast minutes: 10 of 50 requests
	// are slow, more than 5%, so the merged p95 is slow and the run starts at
	// minute 10; minute 9's window (all fast) does not break and ends the walk.
	rows := run(start, 20, func(i int) metrics.Row {
		sec := 0.04
		if i >= 10 {
			sec = 0.8
		}
		return metrics.Row{HTTPSecs: p64(60), Requests: p64(10), Hist: histAt(sec, 10)}
	})
	at, _, ok := metrics.Since(metrics.Slower, rows, ruleNow, bandAt)
	if !ok || !at.Equal(start.Add(10*time.Minute)) {
		t.Errorf("Since = %v ok=%v, want %v", at, ok, start.Add(10*time.Minute))
	}
	// Under 20 timed requests in the 5 minutes (3 a minute: 15) is no data,
	// not a break and not an end.
	thin := run(start, 20, func(i int) metrics.Row {
		if i < 10 {
			return metrics.Row{HTTPSecs: p64(60), Requests: p64(3), Hist: histAt(0.8, 3)}
		}
		return metrics.Row{HTTPSecs: p64(60), Requests: p64(10), Hist: histAt(0.8, 10)}
	})
	at, _, ok = metrics.Since(metrics.Slower, thin, ruleNow, bandAt)
	// Minute 10's window (minutes 6..10) holds 3x4 + 10 = 22 timed requests and
	// breaks; minute 9's holds 15, which is no data, so the run goes on to the
	// start of the rows without ending: the earliest breaking minute is 10.
	if !ok || !at.Equal(start.Add(10*time.Minute)) {
		t.Errorf("Since = %v ok=%v, want %v", at, ok, start.Add(10*time.Minute))
	}
	if _, _, ok := metrics.Since(metrics.Slower, thin[:10], ruleNow, bandAt); ok {
		t.Error("only thin minutes: nothing could be judged")
	}
}

// --- DeployNear ---

func TestDeployNear(t *testing.T) {
	start := ruleNow.Add(-8 * time.Minute) // 13:52
	now := ruleNow
	d := func(app string, v uint64, end time.Time) metrics.Deploy {
		return metrics.Deploy{App: app, Version: v, End: end}
	}
	tests := []struct {
		name    string
		deploys []metrics.Deploy
		ok      bool
		app     string
		rel     metrics.Relation
		mins    int
		less    bool
	}{
		{"none", nil, false, "", "", 0, false},
		{"2 minutes before the start", []metrics.Deploy{d("gw", 24, start.Add(-2*time.Minute))}, true, "gw", metrics.StartedAfter, 2, false},
		{"exactly 15 minutes before", []metrics.Deploy{d("gw", 24, start.Add(-15*time.Minute))}, true, "gw", metrics.StartedAfter, 15, false},
		{"just over 15 minutes before: too old", []metrics.Deploy{d("gw", 24, start.Add(-15*time.Minute-time.Second))}, false, "", "", 0, false},
		{"at the start itself", []metrics.Deploy{d("gw", 24, start)}, true, "gw", metrics.StartedAfter, 0, true},
		{"59 seconds before", []metrics.Deploy{d("gw", 24, start.Add(-59*time.Second))}, true, "gw", metrics.StartedAfter, 0, true},
		{"60 seconds before", []metrics.Deploy{d("gw", 24, start.Add(-time.Minute))}, true, "gw", metrics.StartedAfter, 1, false},
		{"the newest of two before wins", []metrics.Deploy{d("a", 1, start.Add(-10*time.Minute)), d("b", 2, start.Add(-3*time.Minute))}, true, "b", metrics.StartedAfter, 3, false},
		{"before beats after", []metrics.Deploy{d("after", 9, start.Add(2*time.Minute)), d("gw", 24, start.Add(-14*time.Minute))}, true, "gw", metrics.StartedAfter, 14, false},
		{"only one after the start", []metrics.Deploy{d("gw", 24, start.Add(3*time.Minute))}, true, "gw", metrics.StartedBefore, 0, false},
		{"the first after wins", []metrics.Deploy{d("late", 2, start.Add(5*time.Minute)), d("early", 1, start.Add(time.Minute))}, true, "early", metrics.StartedBefore, 0, false},
		{"after, but not after now", []metrics.Deploy{d("gw", 24, now.Add(time.Second))}, false, "", "", 0, false},
		{"after, ending exactly now", []metrics.Deploy{d("gw", 24, now)}, true, "gw", metrics.StartedBefore, 0, false},
		{"older than the window is ignored beside a later one", []metrics.Deploy{d("old", 1, start.Add(-time.Hour)), d("gw", 24, start.Add(4*time.Minute))}, true, "gw", metrics.StartedBefore, 0, false},
		{"a tie goes to the app name", []metrics.Deploy{d("b", 1, start.Add(-time.Minute)), d("a", 1, start.Add(-time.Minute))}, true, "a", metrics.StartedAfter, 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := metrics.DeployNear(start, now, tc.deploys)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, n)
			}
			if !ok {
				return
			}
			if n.App != tc.app || n.Relation != tc.rel || n.Minutes != tc.mins || n.LessThanMinute != tc.less {
				t.Errorf("near = %+v, want app %s %s %d min less=%v", n, tc.app, tc.rel, tc.mins, tc.less)
			}
		})
	}
	// The version comes through.
	n, _ := metrics.DeployNear(start, now, []metrics.Deploy{d("gw", 24, start.Add(-time.Minute))})
	if n.Version != 24 || !n.End.Equal(start.Add(-time.Minute)) {
		t.Errorf("deploy facts lost: %+v", n)
	}
}
