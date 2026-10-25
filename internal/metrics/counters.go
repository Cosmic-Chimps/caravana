package metrics

import (
	"io"
	"math"
	"sort"
	"strconv"
	"time"
)

// The metric names Caravana reads.
const (
	NameRequests  = "traefik_service_requests_total"
	NameBucket    = "traefik_service_request_duration_seconds_bucket"
	NameStartTime = "process_start_time_seconds"
)

// The window between two successful scrapes a delta is trusted for. A gap
// shorter than MinGap (a double tick) or longer than MaxGap (a missed minute)
// only re-baselines: those counts would not be one minute's.
const (
	MinGap = 50 * time.Second
	MaxGap = 90 * time.Second
)

// SeriesKey names one counter series by the labels Caravana keeps.
type SeriesKey struct {
	Name     string
	Service  string
	Code     string
	Method   string
	Protocol string
	Le       string // buckets only, as Traefik wrote it ("0.005", "+Inf")
}

// Snapshot is what one scrape holds of the families Caravana reads. Lines that
// differ only in a label Caravana doesn't keep are added together.
type Snapshot struct {
	Series map[SeriesKey]float64
	// Start is process_start_time_seconds, when Traefik exposes it.
	Start    float64
	HasStart bool
}

// NewSnapshot keeps the request and bucket counters (and the start time) of
// the samples. A NaN or infinite value is ignored (the +Inf bucket's name is
// its le label, not its value).
func NewSnapshot(samples []Sample) Snapshot {
	snap := Snapshot{Series: make(map[SeriesKey]float64, len(samples))}
	for _, s := range samples {
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		switch s.Name {
		case NameStartTime:
			snap.Start, snap.HasStart = s.Value, true
		case NameRequests, NameBucket:
			k := SeriesKey{
				Name:     s.Name,
				Service:  s.Labels["service"],
				Code:     s.Labels["code"],
				Method:   s.Labels["method"],
				Protocol: s.Labels["protocol"],
				Le:       s.Labels["le"],
			}
			snap.Series[k] += s.Value
		}
	}
	return snap
}

// ReadSnapshot parses a scrape and keeps what Caravana reads.
func ReadSnapshot(r io.Reader) (Snapshot, error) {
	samples, err := ParseText(r, NameRequests, NameBucket, NameStartTime)
	if err != nil {
		return Snapshot{}, err
	}
	return NewSnapshot(samples), nil
}

// Delta is what each series counted between two successful scrapes gap apart,
// by Prometheus' rules for a counter: a value lower than before means the
// counter restarted at zero, so the delta is the new value; a series the
// previous scrape didn't have counts from zero; a series that went away is
// dropped. A change of the start time means Traefik restarted, so every series
// counts from zero. The bool is false when gap is outside MinGap..MaxGap:
// nothing is counted and the caller keeps now as the new baseline.
func Delta(prev, now Snapshot, gap time.Duration) (map[SeriesKey]float64, bool) {
	if gap < MinGap || gap > MaxGap {
		return nil, false
	}
	restarted := prev.HasStart && now.HasStart && prev.Start != now.Start
	out := make(map[SeriesKey]float64, len(now.Series))
	for k, v := range now.Series {
		p, seen := prev.Series[k]
		if restarted || !seen || v < p {
			out[k] = v
			continue
		}
		out[k] = v - p
	}
	return out, true
}

// Minute is one Traefik service's minute: requests and 5xx answers (every
// protocol), and the latency histogram of the protocols that end (http, grpc).
type Minute struct {
	Requests int64
	Errors   int64
	// Latency is empty (nil Buckets) when no request was timed.
	Latency Histogram
}

// timedProtocol says whether a protocol's requests count in latency.
// Websocket and SSE last as long as the connection, so they never do.
func timedProtocol(p string) bool { return p == "http" || p == "grpc" }

// TraefikMinute sums a Delta's series per Traefik service (the label as
// written, "<app>-<service>@nomad"): requests, 5xx answers, and the buckets of
// the http and grpc protocols, made monotonic by a running maximum and stored
// per bucket. Traefik gives every series of a histogram the same bounds, so
// the layout is the service's finite bounds. A service present with zero
// counts is kept: it is not the same as one that is absent.
func TraefikMinute(deltas map[SeriesKey]float64) map[string]Minute {
	reqs := make(map[string]float64)
	errs := make(map[string]float64)
	cum := make(map[string]map[float64]float64) // service -> le -> cumulative count
	seen := make(map[string]bool)
	for k, v := range deltas {
		if k.Service == "" {
			continue
		}
		seen[k.Service] = true
		switch k.Name {
		case NameRequests:
			reqs[k.Service] += v
			if len(k.Code) > 0 && k.Code[0] == '5' {
				errs[k.Service] += v
			}
		case NameBucket:
			if !timedProtocol(k.Protocol) {
				continue
			}
			le, err := strconv.ParseFloat(k.Le, 64)
			if err != nil || math.IsNaN(le) {
				continue
			}
			m := cum[k.Service]
			if m == nil {
				m = make(map[float64]float64)
				cum[k.Service] = m
			}
			m[le] += v
		}
	}
	out := make(map[string]Minute, len(seen))
	for svc := range seen {
		m := Minute{Requests: roundCount(reqs[svc]), Errors: roundCount(errs[svc])}
		if c := cum[svc]; len(c) > 0 {
			var layout Layout
			for le := range c {
				if !math.IsInf(le, 0) {
					layout = append(layout, le)
				}
			}
			sort.Float64s(layout)
			counts := make([]float64, 0, len(layout)+1)
			for _, le := range layout {
				counts = append(counts, c[le])
			}
			// The +Inf count is the total timed; with no +Inf line the top
			// bound's count stands in, so nothing is invented above it.
			if inf, ok := c[math.Inf(1)]; ok {
				counts = append(counts, inf)
			} else if len(counts) > 0 {
				counts = append(counts, counts[len(counts)-1])
			}
			if b := FromCumulative(counts); len(layout) > 0 && len(b) > 0 {
				m.Latency = Histogram{Layout: layout, Buckets: b}
			}
		}
		out[svc] = m
	}
	return out
}

func roundCount(v float64) int64 {
	if v <= 0 || math.IsNaN(v) {
		return 0
	}
	return int64(math.Round(v))
}
