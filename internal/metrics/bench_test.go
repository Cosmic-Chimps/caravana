package metrics_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
)

// scrapeOf makes a scrape of about 550 KB: 30 services with the finer
// buckets and a few code/method combinations each, plus the entrypoint and Go
// runtime families Caravana skips.
func scrapeOf() string {
	var b strings.Builder
	b.WriteString("# HELP go_goroutines Number of goroutines.\n# TYPE go_goroutines gauge\ngo_goroutines 40\n")
	b.WriteString("process_start_time_seconds 1.7e9\n")
	for s := 0; s < 30; s++ {
		for _, c := range []struct{ code, method string }{{"200", "GET"}, {"304", "GET"}, {"404", "GET"}, {"200", "POST"}, {"500", "GET"}, {"200", "HEAD"}, {"302", "GET"}, {"201", "POST"}} {
			svc := fmt.Sprintf("app%d--production--web-web@nomad", s)
			fmt.Fprintf(&b, "traefik_service_requests_total{code=%q,method=%q,protocol=\"http\",service=%q} %d\n", c.code, c.method, svc, 100+s)
			for _, le := range append(strings.Split(metrics.TraefikBounds, ","), "+Inf") {
				fmt.Fprintf(&b, "traefik_service_request_duration_seconds_bucket{code=%q,le=%q,method=%q,protocol=\"http\",service=%q} %d\n", c.code, le, c.method, svc, 90+s)
			}
			fmt.Fprintf(&b, "traefik_service_request_duration_seconds_sum{code=%q,method=%q,protocol=\"http\",service=%q} 12.5\n", c.code, c.method, svc)
			fmt.Fprintf(&b, "traefik_service_request_duration_seconds_count{code=%q,method=%q,protocol=\"http\",service=%q} %d\n", c.code, c.method, svc, 90+s)
			for _, e := range []string{"web", "websecure"} {
				fmt.Fprintf(&b, "traefik_entrypoint_requests_total{code=%q,entrypoint=%q,method=%q,protocol=\"http\"} %d\n", c.code, e, c.method, 7)
			}
		}
	}
	return b.String()
}

func BenchmarkParseText500KB(b *testing.B) {
	text := scrapeOf()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := metrics.ReadSnapshot(strings.NewReader(text)); err != nil {
			b.Fatal(err)
		}
	}
}

// TestParseSpeed holds the 50 ms budget of a 500 KB scrape (the benchmark
// above measures it; this fails when it is missed).
func TestParseSpeed(t *testing.T) {
	text := scrapeOf()
	if len(text) < 500_000 {
		t.Fatalf("the synthetic scrape is only %d bytes", len(text))
	}
	best := time.Hour
	for i := 0; i < 5; i++ {
		start := time.Now()
		snap, err := metrics.ReadSnapshot(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d < best {
			best = d
		}
		if len(snap.Series) == 0 {
			t.Fatal("nothing read")
		}
	}
	// A loaded CI box or -race is slower than a quiet one: the budget is
	// the spec's, with room for the race detector.
	limit := 50 * time.Millisecond
	if raceOn {
		limit *= 8
	}
	if best > limit {
		t.Fatalf("parsing %d bytes took %v, budget %v", len(text), best, limit)
	}
}
