package metrics_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
)

// scaleRows makes n minute rows, the newest at ruleNow, from f(i) where i is
// 0 for the newest.
func scaleRows(n int, f func(i int, at time.Time) metrics.Row) []metrics.Row {
	var rows []metrics.Row
	for i := n - 1; i >= 0; i-- {
		rows = append(rows, f(i, ruleNow.Add(-time.Duration(i)*time.Minute)))
	}
	return rows
}

// perMin is n rows of perMin requests a minute, each taking sec seconds.
func perMinRows(n int, perMin int64, sec float64) []metrics.Row {
	return scaleRows(n, func(_ int, at time.Time) metrics.Row {
		return minuteRow(at, perMin, 0, histAt(sec, perMin))
	})
}

// cpuRows is n rows with the CPU used and reserved.
func cpuRows(n int, used, reserved int64) []metrics.Row {
	return scaleRows(n, func(_ int, at time.Time) metrics.Row {
		return metrics.Row{TS: at, Minutes: 1, CPUMHz: p64(used), CPUReservedMHz: p64(reserved)}
	})
}

func requestsRule() metrics.ScaleRule {
	return metrics.ScaleRule{Min: 1, Max: 3, Kind: metrics.ScaleOnRequests, Target: 5, Cooldown: 5 * time.Minute}
}

func cpuRule() metrics.ScaleRule {
	return metrics.ScaleRule{Min: 1, Max: 3, Kind: metrics.ScaleOnCPU, Target: 70, Cooldown: 5 * time.Minute}
}

// shortCooldown is the requests rule with a cooldown of a minute, so a change
// two minutes ago is over.
func shortCooldown() metrics.ScaleRule {
	r := requestsRule()
	r.Cooldown = time.Minute
	return r
}

func p95Rule() metrics.ScaleRule {
	return metrics.ScaleRule{Min: 1, Max: 3, Kind: metrics.ScaleOnP95, Target: 300, Cooldown: 5 * time.Minute}
}

func TestDecideScale(t *testing.T) {
	// One request a second is 60 a minute: the rule's target of 5 is 300.
	gap := func(rows []metrics.Row, i int) []metrics.Row {
		rows[i] = metrics.Row{TS: rows[i].TS, Minutes: 1} // a minute with no numbers
		return rows
	}
	spike := func() []metrics.Row {
		rows := perMinRows(5, 60, 0.05)
		rows[2] = minuteRow(rows[2].TS, 1200, 0, histAt(0.05, 1200))
		return rows
	}
	tests := []struct {
		name       string
		rule       metrics.ScaleRule
		count      int
		rows       []metrics.Row
		lastChange time.Duration // before Now; 0 = none
		wantMove   metrics.ScaleMove
		wantTo     int
		wantHold   string
		wantValue  float64 // 0 = not checked
		wantRows   int     // 0 = not checked
	}{
		// 1. Bounds, at once.
		{name: "count over max goes to max, even in cooldown", rule: requestsRule(), count: 5,
			rows: perMinRows(5, 60, 0.05), lastChange: time.Minute, wantMove: metrics.MoveBound, wantTo: 3},
		{name: "count under min goes to min with no rows", rule: func() metrics.ScaleRule { r := requestsRule(); r.Min = 2; return r }(),
			count: 1, wantMove: metrics.MoveBound, wantTo: 2},

		// 2. Cooldown.
		{name: "inside the cooldown", rule: requestsRule(), count: 1, rows: perMinRows(10, 600, 0.05),
			lastChange: 5*time.Minute - time.Second, wantTo: 1, wantHold: metrics.HoldCooldown},
		{name: "cooldown over to the second", rule: requestsRule(), count: 1, rows: perMinRows(10, 600, 0.05),
			lastChange: 5 * time.Minute, wantMove: metrics.MoveUp, wantTo: 2},

		// 3. Rows from before the last change are ignored.
		{name: "quiet rows from before the change don't let it go down", rule: shortCooldown(), count: 2,
			rows: perMinRows(10, 0, 0.05), lastChange: 2 * time.Minute, wantHold: metrics.HoldInBand, wantTo: 2},
		{name: "the same rows with no change recorded go down", rule: requestsRule(), count: 2,
			rows: perMinRows(10, 0, 0.05), wantMove: metrics.MoveDown, wantTo: 1},
		{name: "hot rows from before the change don't count", rule: shortCooldown(), count: 1,
			rows: func() []metrics.Row {
				rows := perMinRows(10, 600, 0.05) // hot
				for i := range rows {
					if rows[i].TS.After(ruleNow.Add(-2 * time.Minute)) {
						rows[i] = minuteRow(rows[i].TS, 0, 0, metrics.Histogram{}) // quiet since the change
					}
				}
				return rows
			}(), lastChange: 2 * time.Minute, wantHold: metrics.HoldInBand, wantTo: 1},

		// 4. Up, requests per copy.
		{name: "above the target goes up", rule: requestsRule(), count: 1, rows: perMinRows(3, 360, 0.05),
			wantMove: metrics.MoveUp, wantTo: 2, wantValue: 6, wantRows: 3},
		{name: "per copy: 600 a second on 2 copies is 5 each, not above", rule: requestsRule(), count: 2, rows: perMinRows(3, 600, 0.05),
			wantTo: 2, wantHold: metrics.HoldInBand},
		{name: "exactly at the target holds", rule: requestsRule(), count: 1, rows: perMinRows(3, 300, 0.05),
			wantTo: 1, wantHold: metrics.HoldInBand, wantValue: 5},
		{name: "two rows with a value are enough", rule: requestsRule(), count: 1,
			rows: gap(perMinRows(3, 360, 0.05), 1), wantMove: metrics.MoveUp, wantTo: 2, wantRows: 2},
		{name: "one row is not enough", rule: requestsRule(), count: 1, rows: perMinRows(1, 600, 0.05),
			wantTo: 1, wantHold: metrics.HoldFewSamples},
		{name: "a row with no value is skipped for one further back", rule: requestsRule(), count: 1,
			rows: gap(perMinRows(4, 360, 0.05), 0), wantMove: metrics.MoveUp, wantTo: 2, wantRows: 3},
		{name: "above the target at max holds", rule: requestsRule(), count: 3, rows: perMinRows(3, 1200, 0.05),
			wantTo: 3, wantHold: metrics.HoldAtMax, wantValue: 6.666666666666667},
		{name: "unordered rows", rule: requestsRule(), count: 1, rows: func() []metrics.Row {
			rows := perMinRows(3, 360, 0.05)
			rand.New(rand.NewSource(1)).Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
			return rows
		}(), wantMove: metrics.MoveUp, wantTo: 2},
		{name: "no rows", rule: requestsRule(), count: 2, wantTo: 2, wantHold: metrics.HoldFewSamples},

		// 5. Down, requests per copy.
		{name: "low enough goes down", rule: requestsRule(), count: 3, rows: perMinRows(5, 300, 0.05),
			wantMove: metrics.MoveDown, wantTo: 2, wantValue: 5.0 / 3, wantRows: 5},
		{name: "projected exactly at 80 % of the target holds", rule: requestsRule(), count: 3, rows: perMinRows(5, 480, 0.05),
			wantTo: 3, wantHold: metrics.HoldInBand},
		{name: "projected just under 80 % goes down", rule: requestsRule(), count: 3, rows: perMinRows(5, 479, 0.05),
			wantMove: metrics.MoveDown, wantTo: 2},
		{name: "one busy minute stops a step down", rule: requestsRule(), count: 3, rows: spike(),
			wantTo: 3, wantHold: metrics.HoldInBand},
		{name: "a zero request window goes down", rule: requestsRule(), count: 2, rows: perMinRows(5, 0, 0.05),
			wantMove: metrics.MoveDown, wantTo: 1, wantValue: 0},
		{name: "four rows are one too few", rule: requestsRule(), count: 3, rows: perMinRows(4, 0, 0.05),
			wantTo: 3, wantHold: metrics.HoldInBand},
		{name: "one missing row in five", rule: requestsRule(), count: 3, rows: gap(perMinRows(5, 0, 0.05), 2),
			wantTo: 3, wantHold: metrics.HoldNoValue},
		{name: "at min holds", rule: requestsRule(), count: 1, rows: perMinRows(5, 0, 0.05),
			wantTo: 1, wantHold: metrics.HoldAtMin},
		{name: "min equals max, hot", rule: metrics.ScaleRule{Min: 2, Max: 2, Kind: metrics.ScaleOnRequests, Target: 5, Cooldown: time.Minute},
			count: 2, rows: perMinRows(5, 1200, 0.05), wantTo: 2, wantHold: metrics.HoldAtMax},
		{name: "min equals max, quiet", rule: metrics.ScaleRule{Min: 2, Max: 2, Kind: metrics.ScaleOnRequests, Target: 5, Cooldown: time.Minute},
			count: 2, rows: perMinRows(5, 0, 0.05), wantTo: 2, wantHold: metrics.HoldAtMin},

		// cpu.
		{name: "cpu above the target goes up", rule: cpuRule(), count: 1, rows: cpuRows(3, 800, 1000),
			wantMove: metrics.MoveUp, wantTo: 2, wantValue: 80},
		{name: "cpu at the target holds", rule: cpuRule(), count: 1, rows: cpuRows(3, 700, 1000), wantTo: 1, wantHold: metrics.HoldInBand},
		{name: "cpu is the aggregate, not the mean of the rows", rule: cpuRule(), count: 1, rows: []metrics.Row{
			{TS: ruleNow, Minutes: 1, CPUMHz: p64(900), CPUReservedMHz: p64(1000)},
			{TS: ruleNow.Add(-time.Minute), Minutes: 1, CPUMHz: p64(1000), CPUReservedMHz: p64(10000)},
			{TS: ruleNow.Add(-2 * time.Minute), Minutes: 1, CPUMHz: p64(1000), CPUReservedMHz: p64(10000)},
		}, wantTo: 1, wantHold: metrics.HoldInBand, wantValue: 2900.0 * 100 / 21000},
		{name: "cpu low goes down", rule: cpuRule(), count: 2, rows: cpuRows(5, 400, 2000),
			wantMove: metrics.MoveDown, wantTo: 1, wantValue: 20},
		{name: "cpu projected over the margin holds", rule: cpuRule(), count: 2, rows: cpuRows(5, 560, 2000), // 28 % x2 = 56 = 80 % of 70
			wantTo: 2, wantHold: metrics.HoldInBand},
		{name: "cpu with no reservation has no value", rule: cpuRule(), count: 1, rows: cpuRows(5, 800, 0),
			wantTo: 1, wantHold: metrics.HoldNoValue},

		// p95.
		{name: "p95 above the target goes up", rule: p95Rule(), count: 1, rows: perMinRows(3, 10, 1.0),
			wantMove: metrics.MoveUp, wantTo: 2},
		{name: "p95 under the target holds", rule: p95Rule(), count: 1, rows: perMinRows(3, 10, 0.2),
			wantTo: 1, wantHold: metrics.HoldInBand},
		{name: "p95 needs 20 timed requests", rule: p95Rule(), count: 1, rows: perMinRows(3, 6, 1.0),
			wantTo: 1, wantHold: metrics.HoldNoValue},
		{name: "p95 low goes down", rule: p95Rule(), count: 2, rows: perMinRows(5, 10, 0.05),
			wantMove: metrics.MoveDown, wantTo: 1},
		{name: "p95 under the target but not under half holds", rule: p95Rule(), count: 2, rows: perMinRows(5, 10, 0.2),
			wantTo: 2, wantHold: metrics.HoldInBand},
		{name: "p95 with http numbers and no timed request is 0 ms", rule: p95Rule(), count: 2,
			rows: scaleRows(5, func(_ int, at time.Time) metrics.Row {
				return metrics.Row{TS: at, Minutes: 1, HTTPSecs: p64(60), Requests: p64(0)}
			}), wantMove: metrics.MoveDown, wantTo: 1, wantValue: 0},
		{name: "p95 at max holds", rule: p95Rule(), count: 3, rows: perMinRows(3, 10, 1.0), wantTo: 3, wantHold: metrics.HoldAtMax},

		// Guard.
		{name: "no target holds", rule: metrics.ScaleRule{Min: 1, Max: 3, Kind: metrics.ScaleOnCPU, Cooldown: time.Minute}, count: 2,
			rows: cpuRows(5, 900, 1000), wantTo: 2, wantHold: metrics.HoldNoValue},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := metrics.ScaleInput{Rule: tc.rule, Count: tc.count, Rows: tc.rows, Now: ruleNow}
			if tc.lastChange > 0 {
				in.LastChange = ruleNow.Add(-tc.lastChange)
			}
			got := metrics.DecideScale(in)
			if got.Move != tc.wantMove || got.To != tc.wantTo || got.Hold != tc.wantHold {
				t.Fatalf("DecideScale = {Move %q To %d Hold %q} (Value %v, Samples %d), want {Move %q To %d Hold %q}",
					got.Move, got.To, got.Hold, got.Value, got.Samples, tc.wantMove, tc.wantTo, tc.wantHold)
			}
			if tc.wantValue != 0 && (got.Value < tc.wantValue-1e-6 || got.Value > tc.wantValue+1e-6) {
				t.Errorf("Value = %v, want %v", got.Value, tc.wantValue)
			}
			if tc.wantRows != 0 && got.Samples != tc.wantRows {
				t.Errorf("Samples = %d, want %d", got.Samples, tc.wantRows)
			}
		})
	}
}

// A step never goes past a bound, whatever the load.
func TestDecideScaleNeverLeavesTheBounds(t *testing.T) {
	for _, rows := range [][]metrics.Row{perMinRows(10, 6000, 5), perMinRows(10, 0, 0.01)} {
		for count := 1; count <= 3; count++ {
			d := metrics.DecideScale(metrics.ScaleInput{Rule: requestsRule(), Count: count, Rows: rows, Now: ruleNow})
			if d.To < 1 || d.To > 3 {
				t.Errorf("count %d: To = %d outside 1..3", count, d.To)
			}
			if d.Move != metrics.MoveHold && d.To != count+map[metrics.ScaleMove]int{metrics.MoveUp: 1, metrics.MoveDown: -1}[d.Move] {
				t.Errorf("count %d: Move %q To %d is not one step", count, d.Move, d.To)
			}
		}
	}
}

func TestOverTargetAtMax(t *testing.T) {
	tests := []struct {
		name   string
		rule   metrics.ScaleRule
		count  int
		rows   []metrics.Row
		want   bool
		wantAt float64
	}{
		{name: "at max and over", rule: requestsRule(), count: 3, rows: perMinRows(10, 1200, 0.05), want: true, wantAt: 20.0 / 3},
		{name: "at max and under", rule: requestsRule(), count: 3, rows: perMinRows(10, 600, 0.05)},
		{name: "exactly at the target is not over", rule: requestsRule(), count: 3, rows: perMinRows(10, 900, 0.05)},
		{name: "below max", rule: requestsRule(), count: 2, rows: perMinRows(10, 1200, 0.05)},
		{name: "five rows are enough", rule: requestsRule(), count: 3, rows: perMinRows(5, 1200, 0.05), want: true},
		{name: "four rows are not", rule: requestsRule(), count: 3, rows: perMinRows(4, 1200, 0.05)},
		{name: "rows older than the window don't count", rule: requestsRule(), count: 3, rows: perMinRows(14, 1200, 0.05)[:6]},
		{name: "cpu over at max", rule: cpuRule(), count: 3, rows: cpuRows(6, 800, 1000), want: true, wantAt: 80},
		{name: "cpu under at max", rule: cpuRule(), count: 3, rows: cpuRows(6, 600, 1000)},
		{name: "p95 over at max", rule: p95Rule(), count: 3, rows: perMinRows(6, 10, 1.0), want: true},
		{name: "p95 with too few timed requests", rule: p95Rule(), count: 3, rows: perMinRows(6, 3, 1.0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := metrics.OverTargetAtMax(metrics.ScaleInput{Rule: tc.rule, Count: tc.count, Rows: tc.rows, Now: ruleNow})
			if ok != tc.want {
				t.Fatalf("OverTargetAtMax = (%v, %v), want ok %v", v, ok, tc.want)
			}
			if tc.wantAt != 0 && (v < tc.wantAt-1e-6 || v > tc.wantAt+1e-6) {
				t.Errorf("value = %v, want %v", v, tc.wantAt)
			}
		})
	}
}
