package metrics_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
)

// The lab's fixture: Traefik 3.7.13's /metrics from the spike (R8), with five
// services, the protocols http, sse and grpc, codes 200 and 503 and the gRPC
// status "2". These tests are the first table of the package.
const fixturePath = "testdata/traefik.prom"

func readFixture(t *testing.T) metrics.Snapshot {
	t.Helper()
	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := metrics.ReadSnapshot(f)
	if err != nil {
		t.Fatalf("the lab's scrape must parse: %v", err)
	}
	return s
}

// raised copies s and adds the amounts to its series (a series s lacks is
// created: Traefik makes one the first time a code, method or protocol is seen).
func raised(s metrics.Snapshot, add map[metrics.SeriesKey]float64) metrics.Snapshot {
	out := metrics.Snapshot{Series: make(map[metrics.SeriesKey]float64, len(s.Series)), Start: s.Start, HasStart: s.HasStart}
	for k, v := range s.Series {
		out.Series[k] = v
	}
	for k, v := range add {
		out.Series[k] += v
	}
	return out
}

func TestLabFixtureParses(t *testing.T) {
	s := readFixture(t)
	if !s.HasStart || s.Start < 1.7e9 {
		t.Fatalf("start = %v %v", s.Start, s.HasStart)
	}
	services := map[string]bool{}
	protocols := map[string]bool{}
	for k := range s.Series {
		services[k.Service] = true
		protocols[k.Protocol] = true
	}
	want := []string{"blog-web@nomad", "hollo-web@nomad", "lab023-web@nomad", "spike025-web@nomad", "whoami-whoami@nomad"}
	if len(services) != len(want) {
		t.Fatalf("services = %v", services)
	}
	for _, w := range want {
		if !services[w] {
			t.Fatalf("missing %s in %v", w, services)
		}
	}
	for _, p := range []string{"http", "sse", "grpc"} {
		if !protocols[p] {
			t.Fatalf("no %s series in %v", p, protocols)
		}
	}
	// The entrypoint and router families are not kept.
	for k := range s.Series {
		if k.Name != metrics.NameRequests && k.Name != metrics.NameBucket {
			t.Fatalf("unexpected family %q", k.Name)
		}
	}
}

func TestLabFixtureNextMinute(t *testing.T) {
	const spike = "spike025-web@nomad"
	prev := readFixture(t)

	req := func(service, code, method, protocol string) metrics.SeriesKey {
		return metrics.SeriesKey{Name: metrics.NameRequests, Service: service, Code: code, Method: method, Protocol: protocol}
	}
	add := map[metrics.SeriesKey]float64{
		// spike025: 10 more 200s and 3 more 503s over http; two sse streams
		// (a 200, and a 503 on a series the scrape didn't have yet); five
		// grpc calls of status 2 and six of status 5 (a new series).
		req(spike, "200", "GET", "http"): 10,
		req(spike, "503", "GET", "http"): 3,
		req(spike, "200", "GET", "sse"):  2,
		req(spike, "503", "GET", "sse"):  4,
		req(spike, "2", "POST", "grpc"):  5,
		req(spike, "5", "POST", "grpc"):  6,
		// whoami: 7 more 200s, with no bucket change (an old scrape's shape).
		req("whoami-whoami@nomad", "200", "GET", "http"): 7,
	}
	// The latency of each timed batch: from le (a bound) up, cumulative.
	les := append(strings.Split(metrics.TraefikBounds, ","), "+Inf")
	cum := func(code, method, protocol string, from string, n float64) {
		on := false
		for _, le := range les {
			if le == from {
				on = true
			}
			if on {
				add[metrics.SeriesKey{Name: metrics.NameBucket, Service: spike, Code: code, Method: method, Protocol: protocol, Le: le}] = n
			}
		}
	}
	cum("200", "GET", "http", "0.1", 10) // 10 requests of 50-100 ms
	cum("503", "GET", "http", "0.5", 3)  // 3 of 250-500 ms
	cum("2", "POST", "grpc", "1", 5)     // 5 of 500 ms-1 s
	cum("5", "POST", "grpc", "1", 6)     // 6 of 500 ms-1 s
	cum("200", "GET", "sse", "10", 2)    // a stream: never in latency
	cum("503", "GET", "sse", "10", 4)

	now := raised(prev, add)
	deltas, ok := metrics.Delta(prev, now, time.Minute)
	if !ok {
		t.Fatal("window refused")
	}
	minute := metrics.TraefikMinute(deltas)

	for _, svc := range []string{"blog-web@nomad", "hollo-web@nomad", "lab023-web@nomad"} {
		m, present := minute[svc]
		if !present || m.Requests != 0 || m.Errors != 0 || m.Latency.Buckets != nil {
			t.Fatalf("%s = %+v present %v", svc, m, present)
		}
	}
	w := minute["whoami-whoami@nomad"]
	if w.Requests != 7 || w.Errors != 0 || w.Latency.Buckets != nil {
		t.Fatalf("whoami = %+v", w)
	}

	m := minute[spike]
	// Requests count every protocol: 10 + 3 + 2 + 4 + 5 + 6.
	if m.Requests != 30 {
		t.Fatalf("requests = %d, want 30", m.Requests)
	}
	// 5xx from http and sse only: 3 + 4. The grpc status 5 is not a server error.
	if m.Errors != 7 {
		t.Fatalf("errors = %d, want 7", m.Errors)
	}
	// Latency from http and grpc only: 10 + 3 + 5 + 6.
	if got := m.Latency.Buckets.Total(); got != 24 {
		t.Fatalf("timed = %d, want 24", got)
	}
	if !m.Latency.Layout.Equal(layout) {
		t.Fatalf("layout = %v", m.Latency.Layout)
	}
	// Per bucket: (0.05,0.1] has 10, (0.25,0.5] has 3, (0.5,1] has 11.
	want := map[int]int64{4: 10, 6: 3, 7: 11}
	for i, c := range m.Latency.Buckets {
		if c != want[i] {
			t.Fatalf("bucket %d = %d, want %d (all %v)", i, c, want[i], m.Latency.Buckets)
		}
	}
	// Rank 0.95 * 24 = 22.8 is in (0.5, 1]: 0.5 + 0.5 * (22.8 - 13) / 11.
	a, ok := metrics.Quantile(m.Latency.Layout, m.Latency.Buckets, 0.95)
	if !ok || a.Over || a.Seconds < 0.5 || a.Seconds > 1 {
		t.Fatalf("p95 = %+v", a)
	}
	if want := 0.5 + 0.5*(22.8-13)/11; a.Seconds < want-1e-9 || a.Seconds > want+1e-9 {
		t.Fatalf("p95 = %v, want %v", a.Seconds, want)
	}

	// The scrape itself, read as a first minute from zero (a restarted
	// Traefik), is a bounded p95 too.
	first, ok := metrics.Delta(metrics.Snapshot{}, prev, time.Minute)
	if !ok {
		t.Fatal("window refused")
	}
	f := metrics.TraefikMinute(first)[spike]
	if f.Requests != 14+1+1+2+2 {
		t.Fatalf("first minute requests = %d", f.Requests)
	}
	if f.Errors != 2 {
		t.Fatalf("first minute errors = %d (the grpc status 2 is not one)", f.Errors)
	}
	if a, ok := metrics.Quantile(f.Latency.Layout, f.Latency.Buckets, 0.95); !ok || a.Over || a.Seconds <= 0 || a.Seconds > 2.5 {
		t.Fatalf("first minute p95 = %+v %v", a, ok)
	}
}
