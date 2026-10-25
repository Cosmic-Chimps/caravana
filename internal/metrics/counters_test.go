package metrics_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/metrics/metricstest"
)

var bounds = strings.Split(metrics.TraefikBounds, ",")

const svc = "shop-web@nomad"

func snap(t *testing.T, text string) metrics.Snapshot {
	t.Helper()
	s, err := metrics.ReadSnapshot(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var layout = mustLayoutT()

// cumulative makes the 12 cumulative counts (11 bounds and +Inf) of n requests
// that all took sec seconds.
func cumulative(sec float64, n int) []int {
	out := make([]int, len(layout)+1)
	for i := range out {
		if i == len(layout) || sec <= layout[i] {
			out[i] = n
		}
	}
	return out
}

func TestDeltaRules(t *testing.T) {
	key := func(code string) metrics.SeriesKey {
		return metrics.SeriesKey{Name: metrics.NameRequests, Service: svc, Code: code, Method: "GET", Protocol: "http"}
	}
	mk := func(start float64, hasStart bool, kv map[metrics.SeriesKey]float64) metrics.Snapshot {
		return metrics.Snapshot{Series: kv, Start: start, HasStart: hasStart}
	}
	tests := []struct {
		name   string
		prev   metrics.Snapshot
		now    metrics.Snapshot
		gap    time.Duration
		ok     bool
		want   map[metrics.SeriesKey]float64
		reason string
	}{
		{
			name: "plain growth",
			prev: mk(0, false, map[metrics.SeriesKey]float64{key("200"): 10}),
			now:  mk(0, false, map[metrics.SeriesKey]float64{key("200"): 25}),
			gap:  time.Minute, ok: true,
			want: map[metrics.SeriesKey]float64{key("200"): 15},
		},
		{
			name: "no change is zero, not absent",
			prev: mk(0, false, map[metrics.SeriesKey]float64{key("200"): 10}),
			now:  mk(0, false, map[metrics.SeriesKey]float64{key("200"): 10}),
			gap:  time.Minute, ok: true,
			want: map[metrics.SeriesKey]float64{key("200"): 0},
		},
		{
			name: "a reset: the counter restarted at zero, the delta is the new value",
			prev: mk(0, false, map[metrics.SeriesKey]float64{key("200"): 1000}),
			now:  mk(0, false, map[metrics.SeriesKey]float64{key("200"): 7}),
			gap:  time.Minute, ok: true,
			want: map[metrics.SeriesKey]float64{key("200"): 7},
		},
		{
			name: "a new series counts from zero",
			prev: mk(0, false, map[metrics.SeriesKey]float64{key("200"): 10}),
			now:  mk(0, false, map[metrics.SeriesKey]float64{key("200"): 10, key("500"): 3}),
			gap:  time.Minute, ok: true,
			want: map[metrics.SeriesKey]float64{key("200"): 0, key("500"): 3},
		},
		{
			name: "a series that went away is dropped",
			prev: mk(0, false, map[metrics.SeriesKey]float64{key("200"): 10, key("500"): 3}),
			now:  mk(0, false, map[metrics.SeriesKey]float64{key("200"): 11}),
			gap:  time.Minute, ok: true,
			want: map[metrics.SeriesKey]float64{key("200"): 1},
		},
		{
			name: "a new start time marks every series reset, even a bigger one",
			prev: mk(100, true, map[metrics.SeriesKey]float64{key("200"): 10}),
			now:  mk(160, true, map[metrics.SeriesKey]float64{key("200"): 12}),
			gap:  time.Minute, ok: true,
			want: map[metrics.SeriesKey]float64{key("200"): 12},
		},
		{
			name: "the same start time changes nothing",
			prev: mk(100, true, map[metrics.SeriesKey]float64{key("200"): 10}),
			now:  mk(100, true, map[metrics.SeriesKey]float64{key("200"): 12}),
			gap:  time.Minute, ok: true,
			want: map[metrics.SeriesKey]float64{key("200"): 2},
		},
		{
			name: "a start time on one side only is not a restart",
			prev: mk(0, false, map[metrics.SeriesKey]float64{key("200"): 10}),
			now:  mk(160, true, map[metrics.SeriesKey]float64{key("200"): 12}),
			gap:  time.Minute, ok: true,
			want: map[metrics.SeriesKey]float64{key("200"): 2},
		},
		{name: "window: 49 s only re-baselines", gap: 49 * time.Second, ok: false},
		{name: "window: 50 s counts", gap: 50 * time.Second, ok: true, want: map[metrics.SeriesKey]float64{}},
		{name: "window: 90 s counts", gap: 90 * time.Second, ok: true, want: map[metrics.SeriesKey]float64{}},
		{name: "window: 91 s (a missed minute) only re-baselines", gap: 91 * time.Second, ok: false},
		{name: "window: a negative gap (the clock moved back)", gap: -time.Minute, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := metrics.Delta(tt.prev, tt.now, tt.gap)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Fatalf("%v = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestSnapshotKeepsOnlyWhatIsRead(t *testing.T) {
	text := metricstest.Text(1700000000, bounds,
		metricstest.Counter{Service: svc, Code: "200", Method: "GET", Protocol: "http", Requests: 5, Buckets: cumulative(0.002, 5)})
	text += "go_goroutines 12\ntraefik_entrypoint_requests_total{code=\"200\",entrypoint=\"web\"} 9\ntraefik_service_requests_total{service=\"x\",code=\"200\"} NaN\n"
	s := snap(t, text)
	if !s.HasStart || s.Start != 1700000000 {
		t.Fatalf("start = %v %v", s.Start, s.HasStart)
	}
	for k := range s.Series {
		if k.Name != metrics.NameRequests && k.Name != metrics.NameBucket {
			t.Fatalf("unexpected series %v", k)
		}
		if k.Service == "x" {
			t.Fatal("a NaN value was kept")
		}
	}
	if len(s.Series) != 1+len(bounds)+1 {
		t.Fatalf("series = %d", len(s.Series))
	}
}

// Two minutes of Traefik: the sampler's whole numeric path, from text to a
// p95, with a fake source changed between ticks.
func TestTwoScrapesToAMinute(t *testing.T) {
	const other = "other-api@nomad"
	src := metricstest.New(metricstest.Text(1700000000, bounds,
		metricstest.Counter{Service: svc, Code: "200", Method: "GET", Protocol: "http", Requests: 100, Buckets: cumulative(0.002, 100)},
		metricstest.Counter{Service: other, Code: "200", Method: "GET", Protocol: "http", Requests: 10, Buckets: cumulative(0.5, 10)},
	))
	prev, err := metrics.Read(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}

	// 60 s later: 20 more fast 200s, 5 slow (3 s) 200s, 2 fast 502s, 4
	// websocket 101s that count as requests but never in latency.
	fast := cumulative(0.002, 120)
	slow := cumulative(3, 5)
	both := make([]int, len(fast))
	for i := range both {
		both[i] = fast[i] + slow[i]
	}
	src.Set(metricstest.Text(1700000000, bounds,
		metricstest.Counter{Service: svc, Code: "200", Method: "GET", Protocol: "http", Requests: 125, Buckets: both},
		metricstest.Counter{Service: svc, Code: "502", Method: "GET", Protocol: "http", Requests: 2, Buckets: cumulative(0.002, 2)},
		metricstest.Counter{Service: svc, Code: "101", Method: "GET", Protocol: "websocket", Requests: 4, Buckets: cumulative(30, 4)},
		metricstest.Counter{Service: other, Code: "200", Method: "GET", Protocol: "http", Requests: 10, Buckets: cumulative(0.5, 10)},
		metricstest.Counter{Service: "unused@file", Code: "200", Method: "GET", Protocol: "http", Requests: 1},
	))
	now, err := metrics.Read(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	deltas, ok := metrics.Delta(prev, now, time.Minute)
	if !ok {
		t.Fatal("window refused")
	}
	min := metrics.TraefikMinute(deltas)

	m := min[svc]
	if m.Requests != 25+2+4 || m.Errors != 2 {
		t.Fatalf("requests %d errors %d", m.Requests, m.Errors)
	}
	// Latency: 25 http timed 200s (20 fast, 5 slow) and 2 fast 502s; the
	// websocket requests are out of it.
	if got := m.Latency.Buckets.Total(); got != 27 {
		t.Fatalf("timed = %d, want 27", got)
	}
	if !m.Latency.Layout.Equal(layout) {
		t.Fatalf("layout = %v", m.Latency.Layout)
	}
	// 22 of 27 are in the first bucket, 5 in (2.5, 5]: rank 25.65 is among the slow ones.
	a, ok := metrics.Quantile(m.Latency.Layout, m.Latency.Buckets, 0.95)
	if !ok || a.Over || a.Seconds < 2.5 || a.Seconds > 5 {
		t.Fatalf("p95 = %+v", a)
	}
	// The other service had no new requests: present with zero counts and no histogram.
	o, present := min[other]
	if !present || o.Requests != 0 || o.Latency.Buckets != nil {
		t.Fatalf("other = %+v present %v", o, present)
	}
	// A series Caravana has no use for is carried (the sampler's key table drops it).
	if _, ok := min["unused@file"]; !ok {
		t.Fatal("unmapped services are the caller's to ignore")
	}

	// The stored text form round-trips.
	text := m.Latency.Buckets.String()
	back, err := metrics.ParseBuckets(text, m.Latency.Layout)
	if err != nil || back.String() != text {
		t.Fatalf("round trip: %q %v", back, err)
	}
}

func TestTraefikMinuteResetThenCounts(t *testing.T) {
	// A Traefik restart between the scrapes: the new counters are all the
	// minute saw.
	prev := snap(t, metricstest.Text(100, bounds,
		metricstest.Counter{Service: svc, Code: "200", Method: "GET", Protocol: "http", Requests: 5000, Buckets: cumulative(0.04, 5000)}))
	now := snap(t, metricstest.Text(190, bounds,
		metricstest.Counter{Service: svc, Code: "200", Method: "GET", Protocol: "http", Requests: 30, Buckets: cumulative(0.04, 30)}))
	d, ok := metrics.Delta(prev, now, time.Minute)
	if !ok {
		t.Fatal("refused")
	}
	m := metrics.TraefikMinute(d)[svc]
	if m.Requests != 30 || m.Latency.Buckets.Total() != 30 {
		t.Fatalf("minute = %+v", m)
	}
}

func TestTraefikMinuteNonMonotonicScrape(t *testing.T) {
	// Buckets are not read atomically: le=0.1 reads 9 while le=0.05 reads 10.
	// The running maximum lifts it, so no bucket goes negative.
	d := map[metrics.SeriesKey]float64{}
	add := func(le string, v float64) {
		d[metrics.SeriesKey{Name: metrics.NameBucket, Service: svc, Code: "200", Method: "GET", Protocol: "http", Le: le}] = v
	}
	add("0.05", 10)
	add("0.1", 9)
	add("0.25", 12)
	add("+Inf", 12)
	m := metrics.TraefikMinute(d)[svc]
	if m.Latency.Layout.String() != "0.05,0.1,0.25" {
		t.Fatalf("layout = %v", m.Latency.Layout)
	}
	if got := m.Latency.Buckets.String(); got != "10,0,2" {
		t.Fatalf("buckets = %q", got)
	}
}

func TestTraefikMinuteWithoutInfLine(t *testing.T) {
	d := map[metrics.SeriesKey]float64{}
	for le, v := range map[string]float64{"0.1": 3, "1": 5} {
		d[metrics.SeriesKey{Name: metrics.NameBucket, Service: svc, Code: "200", Method: "GET", Protocol: "grpc", Le: le}] = v
	}
	m := metrics.TraefikMinute(d)[svc]
	if got := m.Latency.Buckets.String(); got != "3,2" {
		t.Fatalf("buckets = %q (nothing may be invented above the top bound)", got)
	}
}

func TestTraefikMinuteGrpcCountsInLatency(t *testing.T) {
	d := map[metrics.SeriesKey]float64{
		{Name: metrics.NameBucket, Service: svc, Code: "200", Method: "POST", Protocol: "grpc", Le: "1"}:    4,
		{Name: metrics.NameBucket, Service: svc, Code: "200", Method: "POST", Protocol: "grpc", Le: "+Inf"}: 4,
		{Name: metrics.NameBucket, Service: svc, Code: "200", Method: "GET", Protocol: "sse", Le: "1"}:      99,
		{Name: metrics.NameBucket, Service: svc, Code: "200", Method: "GET", Protocol: "sse", Le: "+Inf"}:   99,
	}
	if got := metrics.TraefikMinute(d)[svc].Latency.Buckets.Total(); got != 4 {
		t.Fatalf("timed = %d, want 4", got)
	}
}

func TestTraefikMinuteOnlyWebsocketHasNoHistogram(t *testing.T) {
	d := map[metrics.SeriesKey]float64{
		{Name: metrics.NameRequests, Service: svc, Code: "101", Method: "GET", Protocol: "websocket"}:        3,
		{Name: metrics.NameBucket, Service: svc, Code: "101", Method: "GET", Protocol: "websocket", Le: "1"}: 3,
	}
	m := metrics.TraefikMinute(d)[svc]
	if m.Requests != 3 || m.Latency.Buckets != nil || m.Latency.Layout != nil {
		t.Fatalf("minute = %+v", m)
	}
}

func TestTraefikMinuteErrorsAre5xx(t *testing.T) {
	d := map[metrics.SeriesKey]float64{}
	for code, v := range map[string]float64{"200": 90, "404": 5, "499": 1, "500": 2, "502": 1, "504": 1} {
		d[metrics.SeriesKey{Name: metrics.NameRequests, Service: svc, Code: code, Method: "GET", Protocol: "http"}] = v
	}
	m := metrics.TraefikMinute(d)[svc]
	if m.Requests != 100 || m.Errors != 4 {
		t.Fatalf("requests %d errors %d", m.Requests, m.Errors)
	}
}

func mustLayoutT() metrics.Layout {
	l, err := metrics.ParseLayout(metrics.TraefikBounds)
	if err != nil {
		panic(err)
	}
	return l
}
