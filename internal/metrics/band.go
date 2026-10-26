package metrics

import (
	"sort"
	"time"
)

// BaselineState says how much history the band can rest on.
type BaselineState string

const (
	// Building: under one full day since the first sample. No band, no
	// "usually"; only the absolute rules (memory, restarting) may fire.
	Building BaselineState = "building"
	// Partial: 1 to 6 full days. The band is drawn from the days there are.
	Partial BaselineState = "partial"
	// Full: 7 full days or more.
	Full BaselineState = "full"
)

// Baseline is the state at now for a first sample at startedAt
// (metrics_state.started_at). A zero or future startedAt is Building.
func Baseline(startedAt, now time.Time) BaselineState {
	if startedAt.IsZero() || now.Before(startedAt) {
		return Building
	}
	days := int(now.Sub(startedAt) / (24 * time.Hour))
	switch {
	case days < 1:
		return Building
	case days < 7:
		return Partial
	default:
		return Full
	}
}

// BandDays is how many earlier days a band looks at.
const BandDays = 7

// BandRange is one metric's band at one moment: the lowest, highest and
// median value of the same UTC slot on the earlier days that had one. Days is
// how many had; 0 means no band.
type BandRange struct {
	Low, High, Usual float64
	Days             int
}

// Has says the band exists.
func (b BandRange) Has() bool { return b.Days > 0 }

// Bands is the band of each metric at one moment.
type Bands struct {
	// RequestsPerMin is requests per minute (the unit of the quieter rule;
	// the chart divides by 60 for requests per second).
	RequestsPerMin BandRange
	// ErrorPercent is the 5xx share of requests, in percent.
	ErrorPercent BandRange
	// P95Millis is the slowest 5 % in milliseconds (a value above the highest
	// bucket bound counts as that bound).
	P95Millis BandRange
	// MemoryMB is the memory used.
	MemoryMB BandRange
}

// Days is the most days any metric's band rests on: "over N days".
func (b Bands) Days() int {
	return max(b.RequestsPerMin.Days, b.ErrorPercent.Days, b.P95Millis.Days, b.MemoryMB.Days)
}

// History is a series' 10-minute rows ready to be asked for bands at many
// moments (a chart asks once per point).
type History struct{ rows []Row }

// NewHistory sorts a copy of the 10-minute rows.
func NewHistory(slotRows []Row) History {
	rows := append([]Row(nil), slotRows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].TS.Before(rows[j].TS) })
	return History{rows: rows}
}

// between returns the rows with from <= TS < to.
func (h History) between(from, to time.Time) []Row {
	lo := sort.Search(len(h.rows), func(i int) bool { return !h.rows[i].TS.Before(from) })
	hi := sort.Search(len(h.rows), func(i int) bool { return !h.rows[i].TS.Before(to) })
	return h.rows[lo:hi]
}

// Band is the band at the point starting at `at` that spans step: for each of
// the 7 days before, the rows of the same UTC time of day are folded into one
// value per metric, and the band is the min, max and median of those values.
// A point of 10 minutes or less looks at its 10-minute slot; a 7 d point
// (an hour) merges its six slots of each earlier day. Days with a NULL for a
// metric are skipped for it.
func Band(slotRows []Row, at time.Time, step time.Duration) Bands {
	return NewHistory(slotRows).Band(at, step)
}

// Band is the package function's body, over rows sorted once.
func (h History) Band(at time.Time, step time.Duration) Bands {
	window := max(step, SlotLength)
	start := at.UTC().Truncate(window)
	var reqs, errs, p95s, mems []float64
	for d := 1; d <= BandDays; d++ {
		from := start.Add(-time.Duration(d) * 24 * time.Hour)
		day := h.between(from, from.Add(window))
		if len(day) == 0 {
			continue
		}
		r := Fold(day).Row
		if v, ok := perMinute(r); ok {
			reqs = append(reqs, v)
		}
		if v, ok := ErrorPercent(r); ok {
			errs = append(errs, v)
		}
		if v, _, ok := P95Millis(r); ok {
			p95s = append(p95s, v)
		}
		if r.MemoryMB != nil {
			mems = append(mems, float64(*r.MemoryMB))
		}
	}
	return Bands{
		RequestsPerMin: rangeOf(reqs),
		ErrorPercent:   rangeOf(errs),
		P95Millis:      rangeOf(p95s),
		MemoryMB:       rangeOf(mems),
	}
}

// perMinute is Σrequests / Σhttp_secs × 60, computed so that 1 request in 60
// seconds is exactly 1 per minute.
func perMinute(r Row) (float64, bool) {
	if r.Requests == nil || r.HTTPSecs == nil || *r.HTTPSecs <= 0 {
		return 0, false
	}
	return float64(*r.Requests) * 60 / float64(*r.HTTPSecs), true
}

func rangeOf(vs []float64) BandRange {
	if len(vs) == 0 {
		return BandRange{}
	}
	return BandRange{Low: minOf(vs), High: maxOf(vs), Usual: median(vs), Days: len(vs)}
}

func minOf(vs []float64) float64 {
	m := vs[0]
	for _, v := range vs[1:] {
		m = min(m, v)
	}
	return m
}

func maxOf(vs []float64) float64 {
	m := vs[0]
	for _, v := range vs[1:] {
		m = max(m, v)
	}
	return m
}

// median of a non-empty slice; an even count is the mean of the two in the
// middle. It sorts a copy.
func median(vs []float64) float64 {
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
