package metrics_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/metrics/metricstest"
)

func TestHTTPSourceReadsTheText(t *testing.T) {
	var accept string
	body := metricstest.Text(5, bounds, metricstest.Counter{Service: svc, Code: "200", Method: "GET", Protocol: "http", Requests: 3})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		io.WriteString(w, body)
	}))
	defer srv.Close()

	s, err := metrics.Read(t.Context(), metrics.HTTPSource{URL: srv.URL + "/metrics"})
	if err != nil {
		t.Fatal(err)
	}
	if accept != "text/plain;version=0.0.4" {
		t.Fatalf("Accept = %q", accept)
	}
	if len(s.Series) != 1 || !s.HasStart {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestHTTPSourceStatuses(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, metrics.ErrOff},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status) }))
		_, err := metrics.HTTPSource{URL: srv.URL}.Scrape(t.Context())
		srv.Close()
		if !errors.Is(err, tt.want) {
			t.Errorf("%d: err = %v, want %v", tt.status, err, tt.want)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	_, err := metrics.HTTPSource{URL: srv.URL}.Scrape(t.Context())
	if err == nil || errors.Is(err, metrics.ErrOff) || errors.Is(err, metrics.ErrUnreachable) || !strings.Contains(err.Error(), "500") {
		t.Fatalf("500: err = %v", err)
	}
}

func TestHTTPSourceRefusedIsUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	_, err = metrics.HTTPSource{URL: "http://" + addr + "/metrics"}.Scrape(t.Context())
	if !errors.Is(err, metrics.ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPSourceTimeoutIsUnreachable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	src := metrics.HTTPSource{URL: srv.URL, Client: &http.Client{Timeout: 50 * time.Millisecond}}
	_, err := src.Scrape(t.Context())
	if !errors.Is(err, metrics.ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPSourceCanceledIsNotUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := metrics.HTTPSource{URL: srv.URL}.Scrape(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPSourceNoURLInTheError(t *testing.T) {
	_, err := metrics.HTTPSource{URL: "http://127.0.0.1:1/secret-path?token=sentinel"}.Scrape(t.Context())
	if err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("err = %v", err)
	}
	_, err = metrics.HTTPSource{URL: "://bad\x7f"}.Scrape(t.Context())
	if err == nil || strings.Contains(err.Error(), "bad") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPSourceBodyLimit(t *testing.T) {
	line := "# " + strings.Repeat("x", 1000) + "\n"
	write := func(n int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			for i := 0; i < n; i += len(line) {
				io.WriteString(w, line)
			}
		}
	}
	over := httptest.NewServer(write(metrics.MaxBody + 4096))
	defer over.Close()
	body, err := metrics.HTTPSource{URL: over.URL, Client: &http.Client{Timeout: 10 * time.Second}}.Scrape(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if _, err := io.Copy(io.Discard, body); !errors.Is(err, metrics.ErrTooLarge) {
		t.Fatalf("over the limit: err = %v", err)
	}

	// Exactly the limit is fine.
	exact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.CopyN(w, zeros{}, metrics.MaxBody)
	}))
	defer exact.Close()
	body2, err := metrics.HTTPSource{URL: exact.URL, Client: &http.Client{Timeout: 10 * time.Second}}.Scrape(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer body2.Close()
	n, err := io.Copy(io.Discard, body2)
	if err != nil || n != metrics.MaxBody {
		t.Fatalf("exact: n = %d err = %v", n, err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = '#'
	}
	return len(p), nil
}

func TestFakeSource(t *testing.T) {
	src := metricstest.New("a 1\n")
	body, err := src.Scrape(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body)
	body.Close()
	if string(b) != "a 1\n" {
		t.Fatalf("body = %q", b)
	}
	src.Fail(metrics.ErrOff)
	if _, err := src.Scrape(t.Context()); !errors.Is(err, metrics.ErrOff) {
		t.Fatalf("err = %v", err)
	}
	src.Set("b 2\n")
	if _, err := metrics.Read(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	if src.Calls() != 3 {
		t.Fatalf("calls = %d", src.Calls())
	}
}
