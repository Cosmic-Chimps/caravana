package metrics

import (
	"slices"
	"time"
)

// The autoscaler's rule (feature 043). It reads a service's minute rows and
// says one step up, one step down or nothing. It is pure: the service layer
// reads the rows, calls DecideScale and carries out the answer.

// The kinds of target a ScaleRule can keep the copies near. They are the
// words of app.ScaleTarget.Kind; this package doesn't import app.
const (
	ScaleOnCPU      = "cpu"             // percent of the CPU the copies reserve
	ScaleOnRequests = "requestsPerCopy" // requests a second per copy
	ScaleOnP95      = "p95"             // the slowest 5 %, in milliseconds
)

// The windows and margins of the rule.
const (
	// ScaleUpSamples is how many of the newest minute rows a step up reads,
	// and ScaleUpMinSamples how many of them must have a value.
	ScaleUpSamples    = 3
	ScaleUpMinSamples = 2
	// ScaleDownSamples is how many of the newest minute rows a step down
	// reads; every one must have a value.
	ScaleDownSamples = 5
	// ScaleDownMargin: after a step down the value, projected on one copy
	// fewer, must stay under this share of the target, so the service
	// doesn't step down and straight back up.
	ScaleDownMargin = 0.8
	// ScaleP95DownFraction: a p95 target steps down only when the slowest 5 %
	// are under this share of the target. Taking a copy away makes the rest
	// slower in a way that can't be projected, so the margin is wide.
	ScaleP95DownFraction = 0.5
)

// ScaleMove is what DecideScale says to do.
type ScaleMove string

const (
	MoveHold  ScaleMove = ""      // change nothing
	MoveUp    ScaleMove = "up"    // one copy more
	MoveDown  ScaleMove = "down"  // one copy fewer
	MoveBound ScaleMove = "bound" // the count is outside Min to Max: go to the nearest bound
)

// Why DecideScale held (ScaleDecision.Hold).
const (
	HoldCooldown   = "cooldown"    // a step or a deploy was too recent
	HoldFewSamples = "few_samples" // not enough rows since the last change
	HoldNoValue    = "no_value"    // rows without the numbers the target needs
	HoldInBand     = "in_band"     // the value is where it should be
	HoldAtMax      = "at_max"      // over the target, but no copy more is allowed
	HoldAtMin      = "at_min"      // no copy fewer is allowed
)

// ScaleRule is one service's scaling settings as the rule reads them.
type ScaleRule struct {
	Min, Max int
	// Kind is ScaleOnCPU, ScaleOnRequests or ScaleOnP95; Target is its value
	// (percent, requests a second per copy, milliseconds).
	Kind     string
	Target   float64
	Cooldown time.Duration
}

// ScaleInput is everything DecideScale needs.
type ScaleInput struct {
	Rule ScaleRule
	// Count is the group's count now.
	Count int
	// Rows are the service's minute rows, in any order.
	Rows []Row
	// LastChange is the newest scale step (any outcome) or the end of the
	// newest deploy; the zero time means none. Rows from before it describe
	// other copies and are ignored.
	LastChange time.Time
	Now        time.Time
}

// ScaleDecision is DecideScale's answer.
type ScaleDecision struct {
	Move ScaleMove
	// To is the new count; it is Count when Move is MoveHold.
	To int
	// Value is what the window measured: percent for cpu, requests a second
	// per copy for requestsPerCopy, milliseconds for p95. For a step down it
	// is the value now, not the projected one. 0 when nothing was measured.
	Value float64
	// Samples is how many rows the decision read.
	Samples int
	// Hold says why nothing changed; it is for tests and the debug log.
	Hold string
}

// DecideScale applies the rules in order:
//  1. a count outside Min to Max goes to the nearest bound at once, with no
//     cooldown and no metrics;
//  2. inside the cooldown after the last change, nothing happens;
//  3. only rows from the last change on count, newest first;
//  4. up: the newest ScaleUpSamples rows that have a value (at least
//     ScaleUpMinSamples) have a value over the target, and Count is under Max;
//  5. down: the newest ScaleDownSamples rows all have a value, the value on
//     one copy fewer is under the target by a margin, and Count is over Min;
//  6. otherwise nothing.
func DecideScale(in ScaleInput) ScaleDecision {
	r := in.Rule
	hold := func(why string, v float64, n int) ScaleDecision {
		return ScaleDecision{To: in.Count, Value: v, Samples: n, Hold: why}
	}

	switch {
	case in.Count < r.Min:
		return ScaleDecision{Move: MoveBound, To: r.Min}
	case in.Count > r.Max:
		return ScaleDecision{Move: MoveBound, To: r.Max}
	}
	if !in.LastChange.IsZero() && in.Now.Sub(in.LastChange) < r.Cooldown {
		return hold(HoldCooldown, 0, 0)
	}
	if r.Target <= 0 || in.Count < 1 {
		return hold(HoldNoValue, 0, 0)
	}

	rows := rowsSince(in.Rows, in.LastChange)

	// Up.
	var value float64
	upRows := newestWithValue(r.Kind, rows, ScaleUpSamples)
	upMeasured := false
	if len(upRows) >= ScaleUpMinSamples {
		if m, ok := measure(r.Kind, upRows, in.Count, MinTimedRequests); ok {
			value, upMeasured = m, true
			if value > r.Target {
				if in.Count < r.Max {
					return ScaleDecision{Move: MoveUp, To: in.Count + 1, Value: value, Samples: len(upRows)}
				}
				return hold(HoldAtMax, value, len(upRows))
			}
		}
	}

	// Down. Every one of the newest rows must have a value: a gap in the
	// numbers is not a quiet minute.
	if len(rows) < ScaleDownSamples {
		return hold(holdWithout(upMeasured, len(upRows), len(rows)), value, len(upRows))
	}
	downRows := rows[:ScaleDownSamples]
	for _, row := range downRows {
		if !hasValue(r.Kind, row) {
			return hold(HoldNoValue, value, len(downRows))
		}
	}
	// For p95, a window with no timed request is 0 ms, not a gap.
	now, ok := measure(r.Kind, downRows, in.Count, 0)
	if !ok {
		return hold(HoldNoValue, value, len(downRows))
	}
	if in.Count <= r.Min {
		return hold(HoldAtMin, now, len(downRows))
	}
	if scaleDownHolds(r, downRows, now, in.Count) {
		return ScaleDecision{Move: MoveDown, To: in.Count - 1, Value: now, Samples: len(downRows)}
	}
	return hold(HoldInBand, now, len(downRows))
}

// holdWithout is the reason when there aren't enough rows for a step down:
// the up window was measured and in band, or it was not: too few rows with a
// value (few_samples when every row has one, no_value when some lack it), or
// enough rows but too few timed requests for a p95.
func holdWithout(measured bool, upRows, rows int) string {
	switch {
	case measured:
		return HoldInBand
	case upRows >= ScaleUpMinSamples:
		return HoldNoValue
	case upRows == rows:
		return HoldFewSamples
	default:
		return HoldNoValue
	}
}

// scaleDownHolds reports whether the window is low enough for one copy fewer.
// cpu and requests: the window's value, projected on Count-1 copies, is under
// the target with the margin, and no single row, projected, is over the target
// (one busy minute stops a step down). p95: the window's p95 is under half the
// target.
func scaleDownHolds(r ScaleRule, rows []Row, value float64, count int) bool {
	if r.Kind == ScaleOnP95 {
		return value < r.Target*ScaleP95DownFraction
	}
	project := float64(count) / float64(count-1)
	if value*project >= r.Target*ScaleDownMargin {
		return false
	}
	for _, row := range rows {
		if v, ok := measure(r.Kind, []Row{row}, count, 0); ok && v*project > r.Target {
			return false
		}
	}
	return true
}

// OverTargetAtMax is the needs-you rule: the service already runs Rule.Max
// copies and its value over the last WindowMinutes minutes, from at least 5
// rows that have one, is above the target. value is what was measured. The
// cooldown and LastChange don't apply: it is about the service being stuck,
// not about a step.
func OverTargetAtMax(in ScaleInput) (value float64, ok bool) {
	r := in.Rule
	if in.Count < r.Max || r.Target <= 0 || in.Count < 1 {
		return 0, false
	}
	from := in.Now.Add(-WindowMinutes * time.Minute)
	var win []Row
	for _, row := range in.Rows {
		if row.TS.After(from) && !row.TS.After(in.Now) && hasValue(r.Kind, row) {
			win = append(win, row)
		}
	}
	if len(win) < 5 {
		return 0, false
	}
	v, ok := measure(r.Kind, win, in.Count, MinTimedRequests)
	if !ok || v <= r.Target {
		return v, false
	}
	return v, true
}

// rowsSince is the rows with TS at or after since (all when since is zero),
// newest first.
func rowsSince(rows []Row, since time.Time) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if since.IsZero() || !r.TS.Before(since) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Row) int { return b.TS.Compare(a.TS) })
	return out
}

// newestWithValue is the newest n rows that have a value for the kind.
func newestWithValue(kind string, rows []Row, n int) []Row {
	var out []Row
	for _, r := range rows {
		if hasValue(kind, r) {
			out = append(out, r)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// hasValue reports whether a row has the numbers the kind needs: both CPU
// figures with a reservation (cpu), or an HTTP count over a span (requests
// and p95; a p95 row without timed requests is the value 0 ms).
func hasValue(kind string, r Row) bool {
	if kind == ScaleOnCPU {
		return r.CPUMHz != nil && r.CPUReservedMHz != nil && *r.CPUReservedMHz > 0
	}
	return r.Requests != nil && r.HTTPSecs != nil && *r.HTTPSecs > 0
}

// measure is the kind's value over rows that all have one: the aggregate, not
// the mean of the rows, so a busy minute weighs what it did. cpu is Σ CPUMHz /
// Σ reserved × 100; requestsPerCopy is Σ requests / Σ seconds / count; p95 is
// the slowest 5 % of the folded buckets in ms. A p95 needs minTimed timed
// requests; below that it is not measured (ok false), except with minTimed 0,
// where no timed request at all is 0 ms. A p95 over the highest bucket bound
// is that bound.
func measure(kind string, rows []Row, count, minTimed int) (value float64, ok bool) {
	switch kind {
	case ScaleOnCPU:
		var used, reserved int64
		for _, r := range rows {
			used += *r.CPUMHz
			reserved += *r.CPUReservedMHz
		}
		if reserved <= 0 {
			return 0, false
		}
		return float64(used) * 100 / float64(reserved), true
	case ScaleOnRequests:
		var reqs, secs int64
		for _, r := range rows {
			reqs += *r.Requests
			secs += *r.HTTPSecs
		}
		if secs <= 0 || count < 1 {
			return 0, false
		}
		return float64(reqs) / float64(secs) / float64(count), true
	case ScaleOnP95:
		f := Fold(rows).Row
		timed := f.Hist.Buckets.Total()
		if timed == 0 && minTimed == 0 {
			return 0, true
		}
		if timed < int64(max(minTimed, 1)) {
			return 0, false
		}
		ms, _, ok := P95Millis(f)
		return ms, ok
	}
	return 0, false
}
