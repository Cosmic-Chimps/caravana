package metrics

import (
	"sort"
	"time"
)

// Kind is why a service is called unusual.
type Kind string

const (
	Slower     Kind = "slower"
	MoreErrors Kind = "more_errors"
	Quieter    Kind = "quieter"
	Memory     Kind = "memory"
	Restarting Kind = "restarting"
)

// Level is how bad an issue is.
type Level string

const (
	LevelWarning Level = "warning"
	LevelFailed  Level = "failed"
)

// The thresholds of the rules (contract, research R5).
const (
	// WindowMinutes is the span the rules judge: the last 10 minutes.
	WindowMinutes = 10
	// MinTimedRequests is how many timed requests the slower rule needs.
	MinTimedRequests = 20
	// SlowerFactor and SlowerMarginMs: the slowest 5 % must be above the
	// band's top by more than 50 % and by at least 100 ms.
	SlowerFactor   = 1.5
	SlowerMarginMs = 100.0
	// ErrorPercentMin is the 5xx share the rule needs (above), with at least
	// MinErrors errors in the window, and above the band's top.
	ErrorPercentMin = 1.0
	MinErrors       = 5
	// QuieterMinLowPerMin: the quieter rule needs a band bottom of at least
	// one request a minute; traffic then must be under half of it.
	QuieterMinLowPerMin = 1.0
	QuieterFraction     = 0.5
	// MemoryPercent: memory above 90 % of the limit.
	MemoryPercent = 90.0
	// RestartsInHour: this many restarts in an hour make a service restarting.
	RestartsInHour = 2
	// FailedErrorPercent and FailedMemoryPercent make an issue failed.
	FailedErrorPercent  = 10.0
	FailedMemoryPercent = 98.0
	// MaxDistance caps a distance (a value of 0 against a band is not
	// infinite, it is "100 times").
	MaxDistance = 100.0
	// SinceLimit is how far back "it started at" walks.
	SinceLimit = 6 * time.Hour
	// sinceSlowerMinutes is the span of the slower rule's per-minute check:
	// the 5 minutes ending at the minute, to have enough requests.
	sinceSlowerMinutes = 5
)

// Window is what the last 10 minutes' rows add up to; the issues' detail
// lines and the level read it.
type Window struct {
	// Rows is how many minute rows fell in the window.
	Rows int
	// HasHTTP says there is a request count and the seconds it covers.
	HasHTTP  bool
	Requests int64
	Errors   int64
	// ErrorPercent is Errors / Requests × 100 (0 with no requests).
	ErrorPercent float64
	// RequestsPerSec is Requests over the seconds covered (0 without HTTP numbers).
	RequestsPerSec float64
	// RequestsPerMin is the same in the unit of the quieter rule.
	RequestsPerMin float64
	// Timed is how many requests the p95 rests on (the merged buckets' total).
	Timed int64
	// P95Millis is the slowest 5 % of the merged buckets; HasP95 false means
	// no timed request. P95Over says it fell above the highest bound.
	P95Millis float64
	P95Over   bool
	HasP95    bool
	// MemoryPercent is the median of memory / limit over the rows that have
	// both, in percent; HasMemory false means no row had both.
	MemoryPercent float64
	HasMemory     bool
}

// WindowOf adds up the rows with now−10m < TS <= now.
func WindowOf(rows []Row, now time.Time) Window {
	from := now.Add(-WindowMinutes * time.Minute)
	var in []Row
	for _, r := range rows {
		if r.TS.After(from) && !r.TS.After(now) {
			in = append(in, r)
		}
	}
	w := Window{Rows: len(in)}
	if len(in) == 0 {
		return w
	}
	f := Fold(in).Row
	if f.Requests != nil && f.HTTPSecs != nil && *f.HTTPSecs > 0 {
		w.HasHTTP = true
		w.Requests = *f.Requests
		w.RequestsPerSec = float64(*f.Requests) / float64(*f.HTTPSecs)
		w.RequestsPerMin, _ = perMinute(f)
		if f.Errors != nil {
			w.Errors = *f.Errors
		}
		if w.Requests > 0 {
			w.ErrorPercent = float64(w.Errors) * 100 / float64(w.Requests)
		}
	}
	w.Timed = f.Hist.Buckets.Total()
	w.P95Millis, w.P95Over, w.HasP95 = P95Millis(f)
	var ratios []float64
	for _, r := range in {
		if v, ok := memoryPercent(r); ok {
			ratios = append(ratios, v)
		}
	}
	if len(ratios) > 0 {
		w.MemoryPercent, w.HasMemory = median(ratios), true
	}
	return w
}

// memoryPercent is memory / limit in percent for a row that has both.
func memoryPercent(r Row) (float64, bool) {
	if r.MemoryMB == nil || r.MemoryLimitMB == nil || *r.MemoryLimitMB <= 0 {
		return 0, false
	}
	return float64(*r.MemoryMB) * 100 / float64(*r.MemoryLimitMB), true
}

// Facts is everything Judge needs about one service (or the environment's
// totals) at one moment.
type Facts struct {
	// Now is the moment judged. Rows are the minute rows; the ones with
	// Now−10m < TS <= Now are the window.
	Now  time.Time
	Rows []Row
	// Band is the band of the slot covering Now − 5 minutes
	// (Band(slotRows, Now.Add(-5*time.Minute), SlotLength)).
	Band  Bands
	State BaselineState
	// Restarts is the restarts nobody asked for in the last hour, from the
	// live copies; RestartsKnown false (Nomad could not be read) leaves the
	// restarting rule unjudged.
	Restarts      int
	RestartsKnown bool
	// RollingOut: the app's newest deployment is pending or running; the HTTP
	// rules are skipped. Stopped apps are not judged at all.
	RollingOut bool
	Stopped    bool
}

// Issue is one unusual thing about a service.
type Issue struct {
	Kind  Kind
	Level Level
	// Value is the kind's number: the slowest 5 % in ms (slower), the 5xx
	// share in percent (more_errors), requests per second (quieter), memory
	// as a percent of the limit (memory), the restarts (restarting).
	Value float64
	// Usual is the band's median in Value's unit (0 when the kind has no band).
	Usual float64
	// Low and High are the band's bottom and top the rule compared with, in
	// Value's unit (0 when the kind has no band).
	Low, High float64
	Restarts  int
	Distance  float64
	Window    Window
}

// Judge applies the five rules and returns the issues in the order slower,
// more_errors, quieter, memory, restarting. A stopped app has none. A
// rolling-out app skips the three HTTP rules, and so does a Building baseline
// (they compare with the band, which does not exist yet); the HTTP rules also
// need a band at that time of day. Memory and restarting are absolute.
func Judge(f Facts) []Issue {
	if f.Stopped {
		return nil
	}
	w := WindowOf(f.Rows, f.Now)
	var out []Issue
	add := func(k Kind, value, usual, low, high float64, restarts int) {
		out = append(out, Issue{
			Kind: k, Level: LevelOf(w), Value: value, Usual: usual, Low: low, High: high,
			Restarts: restarts, Distance: distance(k, value, low, high, restarts), Window: w,
		})
	}

	if !f.RollingOut && f.State != Building && w.HasHTTP {
		b := f.Band
		if b.P95Millis.Has() && w.HasP95 && w.Timed >= MinTimedRequests && SlowerHolds(w.P95Millis, b.P95Millis.High) {
			add(Slower, w.P95Millis, b.P95Millis.Usual, b.P95Millis.Low, b.P95Millis.High, 0)
		}
		if b.ErrorPercent.Has() && w.Requests > 0 && w.Errors >= MinErrors && MoreErrorsHolds(w.ErrorPercent, b.ErrorPercent.High) {
			add(MoreErrors, w.ErrorPercent, b.ErrorPercent.Usual, b.ErrorPercent.Low, b.ErrorPercent.High, 0)
		}
		if b.RequestsPerMin.Has() && QuieterHolds(w.RequestsPerMin, b.RequestsPerMin.Low) {
			add(Quieter, w.RequestsPerSec, b.RequestsPerMin.Usual/60, b.RequestsPerMin.Low/60, b.RequestsPerMin.High/60, 0)
		}
	}
	if w.HasMemory && w.MemoryPercent > MemoryPercent {
		add(Memory, w.MemoryPercent, 0, 0, 0, 0)
	}
	if f.RestartsKnown && f.Restarts >= RestartsInHour {
		add(Restarting, float64(f.Restarts), 0, 0, 0, f.Restarts)
	}
	return out
}

// SlowerHolds: the slowest 5 % (ms) is above the band's top by more than 50 %
// and by at least 100 ms.
func SlowerHolds(p95Millis, bandHigh float64) bool {
	return p95Millis > SlowerFactor*bandHigh && p95Millis-bandHigh >= SlowerMarginMs
}

// MoreErrorsHolds: the 5xx share (percent) is above 1 % and above the band's
// top. The count of errors is the caller's (the window needs at least 5).
func MoreErrorsHolds(errorPercent, bandHigh float64) bool {
	return errorPercent > ErrorPercentMin && errorPercent > bandHigh
}

// QuieterHolds: the band's bottom is at least one request a minute and the
// traffic (requests per minute) is under half of it.
func QuieterHolds(perMin, bandLowPerMin float64) bool {
	return bandLowPerMin >= QuieterMinLowPerMin && perMin < bandLowPerMin*QuieterFraction
}

// LevelOf is failed when the window's 5xx share is above 10 % or its memory
// above 98 % of the limit, else warning.
func LevelOf(w Window) Level {
	if (w.HasHTTP && w.Requests > 0 && w.ErrorPercent > FailedErrorPercent) ||
		(w.HasMemory && w.MemoryPercent > FailedMemoryPercent) {
		return LevelFailed
	}
	return LevelWarning
}

// Distance is how far past its rule an issue is, to sort the worst first:
// value / band top (slower, more_errors; the band's top at 0 gives
// MaxDistance), band bottom / value (quieter, value 0 gives MaxDistance),
// memory percent / 90, and 1 + restarts / 10. It is capped at MaxDistance.
// The band numbers are in the issue's own unit (Issue.Value's).
func Distance(i Issue) float64 {
	return distance(i.Kind, i.Value, i.Low, i.High, i.Restarts)
}

func distance(k Kind, value, low, high float64, restarts int) float64 {
	var d float64
	switch k {
	case Slower, MoreErrors:
		d = MaxDistance
		if high > 0 {
			d = value / high
		}
	case Quieter:
		d = MaxDistance
		if value > 0 {
			d = low / value
		}
	case Memory:
		d = value / MemoryPercent
	case Restarting:
		d = 1 + float64(restarts)/10
	}
	return min(d, MaxDistance)
}

// SortByDistance orders issues worst first (stable).
func SortByDistance(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Distance > issues[j].Distance })
}

// Since walks back from now over the minute rows, at most SinceLimit, and
// returns the earliest minute of the unbroken run in which the minute still
// breaks the kind's rule, judged against that minute's own band (bandAt gives
// the band of the slot a minute is in). A minute that cannot be judged (no
// row values, no band at that hour, too few requests for slower) neither
// breaks nor ends the run; a minute that is judged and does not break ends it.
// capped says the run reaches the limit, so the page reads "for more than 6
// hours". ok is false when the newest judged minute does not break (the
// caller then falls back to the window's start), for restarting (which has no
// rows to walk) and when nothing could be judged.
//
// Per minute: slower looks at the 5 minutes ending there (merged buckets, at
// least 20 timed requests); more errors and quieter at that minute alone (the
// window's "at least 5 errors" is not asked of a single minute); memory at the
// minute's own memory / limit.
func Since(k Kind, rows []Row, now time.Time, bandAt func(time.Time) Bands) (at time.Time, capped, ok bool) {
	if k == Restarting {
		return time.Time{}, false, false
	}
	limit := now.Add(-SinceLimit)
	var rs []Row
	for _, r := range rows {
		if r.TS.After(limit) && !r.TS.After(now) {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].TS.Before(rs[j].TS) })

	var earliest time.Time
	ended := false
	for i := len(rs) - 1; i >= 0; i-- {
		v := verdict(k, rs, i, bandAt)
		if v == noData {
			continue
		}
		if v == within {
			ended = true
			break
		}
		earliest = rs[i].TS
	}
	if earliest.IsZero() {
		return time.Time{}, false, false
	}
	// The run is capped when it was never ended and the rows reach back to
	// the limit (the oldest row is within a minute of it).
	capped = !ended && len(rs) > 0 && rs[0].TS.Sub(limit) <= time.Minute
	return earliest, capped, true
}

type judged int

const (
	noData judged = iota
	within
	breaks
)

func verdict(k Kind, rs []Row, i int, bandAt func(time.Time) Bands) judged {
	r := rs[i]
	switch k {
	case Slower:
		b := bandAt(r.TS).P95Millis
		from := r.TS.Add(-sinceSlowerMinutes * time.Minute)
		var win []Row
		for j := i; j >= 0 && rs[j].TS.After(from); j-- {
			win = append(win, rs[j])
		}
		f := Fold(win).Row
		ms, _, has := P95Millis(f)
		if !b.Has() || !has || f.Hist.Buckets.Total() < MinTimedRequests {
			return noData
		}
		return boolVerdict(SlowerHolds(ms, b.High))
	case MoreErrors:
		b := bandAt(r.TS).ErrorPercent
		pct, has := ErrorPercent(r)
		if !b.Has() || !has {
			return noData
		}
		return boolVerdict(MoreErrorsHolds(pct, b.High))
	case Quieter:
		b := bandAt(r.TS).RequestsPerMin
		v, has := perMinute(r)
		if !b.Has() || !has {
			return noData
		}
		return boolVerdict(QuieterHolds(v, b.Low))
	case Memory:
		pct, has := memoryPercent(r)
		if !has {
			return noData
		}
		return boolVerdict(pct > MemoryPercent)
	}
	return noData
}

func boolVerdict(b bool) judged {
	if b {
		return breaks
	}
	return within
}

// Deploy is a deployment that ended healthy.
type Deploy struct {
	App     string
	Version uint64
	// End is when it took all the traffic (the deployment's UpdatedAt at healthy).
	End time.Time
}

// Relation says how a deploy sits against the start of an issue.
type Relation string

const (
	// StartedAfter: the issue started after the deploy rolled out ("It
	// started 2 minutes after X v24 rolled out").
	StartedAfter Relation = "after"
	// StartedBefore: the deploy rolled out after the issue started ("It
	// started before X v24 rolled out").
	StartedBefore Relation = "before"
)

// DeployWindow is how long before an issue's start a deploy still counts.
const DeployWindow = 15 * time.Minute

// Near is the deploy clause of an issue.
type Near struct {
	Deploy
	Relation Relation
	// Minutes is the whole minutes between the deploy's end and the start
	// (StartedAfter only); LessThanMinute says it was 0, which the sentence
	// writes "less than a minute after".
	Minutes        int
	LessThanMinute bool
}

// DeployNear finds the deploy to name for an issue that started at start:
// the newest one that ended within 15 minutes before it (end in
// [start−15m, start]), else the first one that ended after the start and not
// after now. Neither gives ok == false. Ties go to the app name, then the
// version.
func DeployNear(start, now time.Time, deploys []Deploy) (Near, bool) {
	var before, after *Deploy
	for i := range deploys {
		d := &deploys[i]
		switch {
		case !d.End.After(start) && !d.End.Before(start.Add(-DeployWindow)):
			if before == nil || later(*d, *before) {
				before = d
			}
		case d.End.After(start) && !d.End.After(now):
			if after == nil || earlier(*d, *after) {
				after = d
			}
		}
	}
	if before != nil {
		m := int(start.Sub(before.End) / time.Minute)
		return Near{Deploy: *before, Relation: StartedAfter, Minutes: m, LessThanMinute: m == 0}, true
	}
	if after != nil {
		return Near{Deploy: *after, Relation: StartedBefore}, true
	}
	return Near{}, false
}

// later orders by end time (newer first), then app and version.
func later(a, b Deploy) bool {
	if !a.End.Equal(b.End) {
		return a.End.After(b.End)
	}
	if a.App != b.App {
		return a.App < b.App
	}
	return a.Version > b.Version
}

func earlier(a, b Deploy) bool {
	if !a.End.Equal(b.End) {
		return a.End.Before(b.End)
	}
	if a.App != b.App {
		return a.App < b.App
	}
	return a.Version < b.Version
}
