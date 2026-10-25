package metrics

import (
	"sort"
	"time"
)

// SlotLength is the span a folded row covers.
const SlotLength = 10 * time.Minute

// Row is one service's stored row for a slot: a minute (Minutes is 1) or the
// fold of a 10-minute slot. It mirrors store.MetricsSample field by field, so
// the service maps one to the other directly: a nil pointer is NULL, and the
// layout ID and bucket text of the store become a parsed Hist (the zero Hist,
// with no layout, is NULL buckets).
type Row struct {
	TS      time.Time // the slot's start, UTC
	Minutes int

	// Counters: sums over the slot.
	HTTPSecs *int64 // seconds the HTTP counts cover
	Requests *int64
	Errors   *int64 // 5xx
	Restarts *int64
	Hist     Histogram

	// Gauges: the median over the slot.
	CPUMHz           *int64
	CPUReservedMHz   *int64
	MemoryMB         *int64
	MemoryLimitMB    *int64
	MemoryReservedMB *int64
	Copies           *int64
}

// Folded is a fold's row and what it had to leave out.
type Folded struct {
	Row Row
	// SkippedLayouts counts the rows whose buckets were left out because their
	// layout is not the fold's: Traefik's buckets were changed between rows.
	SkippedLayouts int
}

// Fold turns the rows of a slot (usually its minute rows) into one row.
// Counters (requests, errors, restarts, http_secs) are summed, and are NULL
// when every row's is. Buckets are summed per bound within one layout, the one
// of the newest row that has buckets (the current Traefik setting); rows of
// another layout are not added and are counted in SkippedLayouts. Gauges are
// the median of the rows that have a value. Minutes adds up the rows'
// Minutes (a row that says 0 counts as 1). The row's TS is the start of the
// 10-minute slot of the earliest row; no rows give the zero Row.
func Fold(rows []Row) Folded {
	if len(rows) == 0 {
		return Folded{}
	}
	var out Row
	first := rows[0].TS
	for _, r := range rows {
		if r.TS.Before(first) {
			first = r.TS
		}
		out.Minutes += max(r.Minutes, 1)
	}
	out.TS = first.UTC().Truncate(SlotLength)

	out.HTTPSecs = sumOf(rows, func(r Row) *int64 { return r.HTTPSecs })
	out.Requests = sumOf(rows, func(r Row) *int64 { return r.Requests })
	out.Errors = sumOf(rows, func(r Row) *int64 { return r.Errors })
	out.Restarts = sumOf(rows, func(r Row) *int64 { return r.Restarts })

	out.CPUMHz = medianOf(rows, func(r Row) *int64 { return r.CPUMHz })
	out.CPUReservedMHz = medianOf(rows, func(r Row) *int64 { return r.CPUReservedMHz })
	out.MemoryMB = medianOf(rows, func(r Row) *int64 { return r.MemoryMB })
	out.MemoryLimitMB = medianOf(rows, func(r Row) *int64 { return r.MemoryLimitMB })
	out.MemoryReservedMB = medianOf(rows, func(r Row) *int64 { return r.MemoryReservedMB })
	out.Copies = medianOf(rows, func(r Row) *int64 { return r.Copies })

	// The layout of the newest row that has buckets.
	var current Layout
	var newest time.Time
	for _, r := range rows {
		if len(r.Hist.Layout) > 0 && len(r.Hist.Buckets) > 0 && (current == nil || r.TS.After(newest)) {
			current, newest = r.Hist.Layout, r.TS
		}
	}
	var skipped int
	if current != nil {
		hs := make([]Histogram, 0, len(rows))
		for _, r := range rows {
			if len(r.Hist.Buckets) > 0 {
				hs = append(hs, r.Hist)
			}
		}
		sum, n := Merge(current, hs...)
		skipped = n
		if len(sum) > 0 {
			out.Hist = Histogram{Layout: current, Buckets: sum}
		}
	}
	return Folded{Row: out, SkippedLayouts: skipped}
}

func sumOf(rows []Row, get func(Row) *int64) *int64 {
	var total int64
	seen := false
	for _, r := range rows {
		if v := get(r); v != nil {
			total += *v
			seen = true
		}
	}
	if !seen {
		return nil
	}
	return &total
}

// medianOf is the median of the non-NULL values; an even count takes the mean
// of the two in the middle, rounded half up.
func medianOf(rows []Row, get func(Row) *int64) *int64 {
	var vs []int64
	for _, r := range rows {
		if v := get(r); v != nil {
			vs = append(vs, *v)
		}
	}
	if len(vs) == 0 {
		return nil
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i] < vs[j] })
	n := len(vs)
	m := vs[n/2]
	if n%2 == 0 {
		a, b := vs[n/2-1], vs[n/2]
		m = a + (b-a+1)/2
	}
	return &m
}

// Range is one of the four ranges the page offers.
type Range struct {
	Name string
	Span time.Duration
	// Step is one point's span.
	Step time.Duration
	// FromSlots says the points are read from 10-minute rows (24 h and 7 d);
	// otherwise from minute rows (1 h and 6 h).
	FromSlots bool
}

// The ranges (ruling 5): 1 h is 60 one-minute points, 6 h 72 five-minute
// points, 24 h 144 ten-minute points and 7 d 168 one-hour points.
var (
	Range1h  = Range{Name: "1h", Span: time.Hour, Step: time.Minute}
	Range6h  = Range{Name: "6h", Span: 6 * time.Hour, Step: 5 * time.Minute}
	Range24h = Range{Name: "24h", Span: 24 * time.Hour, Step: SlotLength, FromSlots: true}
	Range7d  = Range{Name: "7d", Span: 7 * 24 * time.Hour, Step: time.Hour, FromSlots: true}
)

// Ranges lists them shortest first.
func Ranges() []Range { return []Range{Range1h, Range6h, Range24h, Range7d} }

// ParseRange finds a range by its name ("1h", "6h", "24h", "7d").
func ParseRange(name string) (Range, bool) {
	for _, r := range Ranges() {
		if r.Name == name {
			return r, true
		}
	}
	return Range{}, false
}

// N is the number of points.
func (r Range) N() int { return int(r.Span / r.Step) }

// From is the start of the first point when now is the newest moment: the
// last point is the one that contains now (the slot in progress), so a chart
// always ends at "now".
func (r Range) From(now time.Time) time.Time {
	return now.UTC().Truncate(r.Step).Add(r.Step - r.Span)
}

// Point is one step of a range: the fold of the rows that fall in
// [T, T+step).
type Point struct {
	T time.Time
	// Rows is how many rows the point has; 0 is a gap (not a zero).
	Rows int
	// Row is those rows folded. A column that is nil is a gap for that
	// metric: no row had a value.
	Row Row
	// SkippedLayouts as in Folded.
	SkippedLayouts int
}

// Gap says no row fell in the point.
func (p Point) Gap() bool { return p.Rows == 0 }

// RequestsPerSec is the requests over the seconds the rows' counts cover.
func (p Point) RequestsPerSec() (float64, bool) { return RatePerSec(p.Row) }

// ErrorPercent is the 5xx share of the requests, in percent. It has no value
// without requests.
func (p Point) ErrorPercent() (float64, bool) { return ErrorPercent(p.Row) }

// P95 is the slowest 5 % of the point's requests, in milliseconds.
func (p Point) P95() (ms float64, over, ok bool) { return P95Millis(p.Row) }

// Points makes n points of step from `from`, each the fold of the rows with
// from+i*step <= TS < from+(i+1)*step: counters summed, gauges the median,
// buckets merged. A point with no row is a gap. Rows outside the n points are
// ignored; the rows need not be sorted.
func Points(rows []Row, from time.Time, step time.Duration, n int) []Point {
	from = from.UTC()
	groups := make([][]Row, n)
	for _, r := range rows {
		if r.TS.Before(from) {
			continue
		}
		i := int(r.TS.Sub(from) / step)
		if i >= n {
			continue
		}
		groups[i] = append(groups[i], r)
	}
	out := make([]Point, n)
	for i, g := range groups {
		p := Point{T: from.Add(time.Duration(i) * step), Rows: len(g)}
		if len(g) > 0 {
			f := Fold(g)
			p.Row, p.SkippedLayouts = f.Row, f.SkippedLayouts
			p.Row.TS = p.T
		}
		out[i] = p
	}
	return out
}

// Slots adds the slot in progress to the stored 10-minute rows. A slot is
// folded at the first tick after it ends, so the newest slot has minute rows
// and no 10-minute row yet: each slot that has minute rows and no stored row
// gets the fold of its minute rows. The result is sorted by time.
func Slots(slotRows, minuteRows []Row) []Row {
	have := make(map[time.Time]bool, len(slotRows))
	out := make([]Row, 0, len(slotRows)+1)
	for _, r := range slotRows {
		have[r.TS.UTC().Truncate(SlotLength)] = true
		out = append(out, r)
	}
	pending := make(map[time.Time][]Row)
	for _, r := range minuteRows {
		s := r.TS.UTC().Truncate(SlotLength)
		if !have[s] {
			pending[s] = append(pending[s], r)
		}
	}
	for s, rs := range pending {
		f := Fold(rs).Row
		f.TS = s
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out
}

// RatePerSec is Σrequests / Σhttp_secs. It has no value when either is NULL
// or the rows cover no seconds.
func RatePerSec(r Row) (float64, bool) {
	if r.Requests == nil || r.HTTPSecs == nil || *r.HTTPSecs <= 0 {
		return 0, false
	}
	return float64(*r.Requests) / float64(*r.HTTPSecs), true
}

// ErrorPercent is Σerrors / Σrequests × 100. It has no value without both
// counters or with no requests.
func ErrorPercent(r Row) (float64, bool) {
	if r.Errors == nil || r.Requests == nil || *r.Requests <= 0 {
		return 0, false
	}
	return float64(*r.Errors) / float64(*r.Requests) * 100, true
}

// P95Millis is the slowest 5 % of the row's merged buckets in milliseconds.
// over is set when the rank fell above the highest bound: ms is then that
// bound and the truth is "over that". No timed request gives ok == false.
func P95Millis(r Row) (ms float64, over, ok bool) {
	a, ok := Quantile(r.Hist.Layout, r.Hist.Buckets, 0.95)
	if !ok {
		return 0, false, false
	}
	return a.Seconds * 1000, a.Over, true
}
