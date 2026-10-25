// Package metricstest has a fake metrics source for tests of the sampler: it
// serves a fixture that a test changes between ticks.
package metricstest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Source serves a body, or an error, set by the test.
type Source struct {
	mu    sync.Mutex
	body  string
	err   error
	calls int
}

// New serves body until Set or Fail changes it.
func New(body string) *Source { return &Source{body: body} }

// Set changes what the next scrape returns and clears any error.
func (s *Source) Set(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body, s.err = body, nil
}

// Fail makes the next scrapes return err (use metrics.ErrOff or
// metrics.ErrUnreachable) until Set is called.
func (s *Source) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Calls is how many scrapes were asked for.
func (s *Source) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Scrape implements metrics.Source.
func (s *Source) Scrape(ctx context.Context) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(strings.NewReader(s.body)), nil
}

// Counter is one line of a fixture: a Traefik service's requests and its
// timed requests, which Text turns into the exposition text.
type Counter struct {
	Service  string
	Code     string
	Method   string
	Protocol string
	Requests int
	// Buckets are cumulative counts for the bounds in Text's argument, then
	// +Inf last; nil means no histogram lines.
	Buckets []int
}

// Text writes a scrape of Traefik's service families for the given bounds
// (the le values as text, without +Inf) and counters, with HELP and TYPE
// lines and a start time.
func Text(start float64, bounds []string, counters ...Counter) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP process_start_time_seconds Start time of the process.\n# TYPE process_start_time_seconds gauge\nprocess_start_time_seconds %g\n", start)
	b.WriteString("# HELP traefik_service_requests_total How many HTTP requests processed on a service.\n# TYPE traefik_service_requests_total counter\n")
	for _, c := range counters {
		fmt.Fprintf(&b, "traefik_service_requests_total{code=%q,method=%q,protocol=%q,service=%q} %d\n",
			c.Code, c.Method, c.Protocol, c.Service, c.Requests)
	}
	b.WriteString("# HELP traefik_service_request_duration_seconds How long it took to process the request on a service.\n# TYPE traefik_service_request_duration_seconds histogram\n")
	for _, c := range counters {
		if c.Buckets == nil {
			continue
		}
		for i, v := range c.Buckets {
			le := "+Inf"
			if i < len(bounds) {
				le = bounds[i]
			}
			fmt.Fprintf(&b, "traefik_service_request_duration_seconds_bucket{code=%q,le=%q,method=%q,protocol=%q,service=%q} %d\n",
				c.Code, le, c.Method, c.Protocol, c.Service, v)
		}
	}
	return b.String()
}
