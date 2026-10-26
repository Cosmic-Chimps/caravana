package metrics

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// TraefikBounds is the layout Caravana asks Traefik for
// (--metrics.prometheus.buckets), in seconds.
const TraefikBounds = "0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10"

// Layout is a histogram's finite upper bounds in seconds, ascending. The
// bucket above the last bound (+Inf) is implied.
type Layout []float64

// ParseLayout reads the comma-separated form stored in a layout row.
func ParseLayout(s string) (Layout, error) {
	if s == "" {
		return nil, fmt.Errorf("layout: empty")
	}
	parts := strings.Split(s, ",")
	l := make(Layout, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return nil, fmt.Errorf("layout: bad bound %q", p)
		}
		if n := len(l); n > 0 && v <= l[n-1] {
			return nil, fmt.Errorf("layout: bound %q is not above the one before", p)
		}
		l = append(l, v)
	}
	return l, nil
}

// String is the stored form: bounds in seconds, comma-separated, shortest
// decimal ("0.005,0.01,...").
func (l Layout) String() string {
	parts := make([]string, len(l))
	for i, v := range l {
		parts[i] = strconv.FormatFloat(v, 'g', -1, 64)
	}
	return strings.Join(parts, ",")
}

// Equal says whether two layouts have the same bounds.
func (l Layout) Equal(o Layout) bool {
	if len(l) != len(o) {
		return false
	}
	for i := range l {
		if l[i] != o[i] {
			return false
		}
	}
	return true
}

// Buckets are per-bucket counts (not cumulative): index i counts the requests
// above bound i-1 and up to bound i, and the entry after the last bound counts
// the rest (+Inf). Trailing zeros may be cut, so a Buckets can be shorter than
// its layout's len+1; the missing entries are zero.
type Buckets []int64

// Total is the number of timed requests.
func (b Buckets) Total() int64 {
	var t int64
	for _, c := range b {
		t += c
	}
	return t
}

// Trim cuts trailing zeros. A histogram with no requests is nil.
func (b Buckets) Trim() Buckets {
	n := len(b)
	for n > 0 && b[n-1] == 0 {
		n--
	}
	if n == 0 {
		return nil
	}
	return b[:n]
}

// String is the stored form: counts comma-separated, trailing zeros cut,
// empty text for no requests.
func (b Buckets) String() string {
	b = b.Trim()
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = strconv.FormatInt(c, 10)
	}
	return strings.Join(parts, ",")
}

// ParseBuckets reads the stored form for layout l. Empty text is no
// requests; more counts than l has buckets, or a negative or non-integer
// count, is an error.
func ParseBuckets(s string, l Layout) (Buckets, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > len(l)+1 {
		return nil, fmt.Errorf("buckets: %d counts for a layout of %d buckets", len(parts), len(l)+1)
	}
	b := make(Buckets, len(parts))
	for i, p := range parts {
		c, err := strconv.ParseInt(p, 10, 64)
		if err != nil || c < 0 {
			return nil, fmt.Errorf("buckets: bad count %q", p)
		}
		b[i] = c
	}
	return b.Trim(), nil
}

// FromCumulative turns a scrape's cumulative counts (one per bound, then
// +Inf) into per-bucket counts. Traefik's buckets are not read atomically, so
// a count can read below the one before it: a running maximum makes the series
// monotonic first, as Prometheus does. Counts are rounded to whole requests.
func FromCumulative(cum []float64) Buckets {
	b := make(Buckets, len(cum))
	var prev int64
	for i, v := range cum {
		c := int64(0)
		if v > 0 && !math.IsNaN(v) {
			c = int64(math.Round(v))
		}
		if c < prev {
			c = prev
		}
		b[i] = c - prev
		prev = c
	}
	return b.Trim()
}

// Histogram is one row's buckets with the layout they are counts for.
type Histogram struct {
	Layout  Layout
	Buckets Buckets
}

// Merge adds the buckets of every histogram that has layout l. A histogram of
// another layout (Traefik's buckets were changed between two rows) is skipped,
// as is one with more counts than l has buckets; skipped says how many.
func Merge(l Layout, hs ...Histogram) (sum Buckets, skipped int) {
	sum = make(Buckets, len(l)+1)
	for _, h := range hs {
		if !h.Layout.Equal(l) || len(h.Buckets) > len(sum) {
			skipped++
			continue
		}
		for i, c := range h.Buckets {
			sum[i] += c
		}
	}
	return sum.Trim(), skipped
}

// Answer is a quantile in seconds. Over is set when the rank fell in the +Inf
// bucket: Seconds is then the highest finite bound and the truth is "over
// that".
type Answer struct {
	Seconds float64
	Over    bool
}

// Quantile is Prometheus' histogram_quantile over per-bucket counts: the rank
// is q times the total, the first bucket whose cumulative count reaches it
// holds the answer, and the answer is interpolated linearly inside that bucket
// from its lower bound (0 for the first). A rank in +Inf is the highest finite
// bound with Over. No requests, an empty layout or q outside 0..1 gives
// ok == false.
func Quantile(l Layout, b Buckets, q float64) (a Answer, ok bool) {
	total := b.Total()
	if total <= 0 || len(l) == 0 || len(b) > len(l)+1 || math.IsNaN(q) || q < 0 || q > 1 {
		return Answer{}, false
	}
	rank := q * float64(total)
	var cum int64
	for i, c := range b {
		prev := cum
		cum += c
		// An empty bucket never holds the rank (only q == 0 would reach one).
		if c == 0 || float64(cum) < rank {
			continue
		}
		if i >= len(l) {
			return Answer{Seconds: l[len(l)-1], Over: true}, true
		}
		lower := 0.0
		if i > 0 {
			lower = l[i-1]
		}
		return Answer{Seconds: lower + (l[i]-lower)*(rank-float64(prev))/float64(c)}, true
	}
	// Unreachable for q <= 1 (the last cumulative count is the total); the
	// highest bound is the honest answer if floating point ever got here.
	return Answer{Seconds: l[len(l)-1], Over: true}, true
}
