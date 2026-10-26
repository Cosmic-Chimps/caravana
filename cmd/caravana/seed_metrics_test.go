package main

// Seeded monitoring history (feature 025): the band needs earlier days, and the
// lab has only minutes. The console step console-monitoring writes two earlier
// days for its own services.
//
// Both write through the store beside the running server (store.OpenShared, as
// the admin CLI opens it) and move metrics_state.started_at back with one SQL
// statement: the store has no call that moves it, on purpose (the server only
// ever writes it once).

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// traefikBounds are the bucket bounds the lab's and the installer's Traefik
// use (--metrics.prometheus.buckets), in the form the store keeps a layout.
const traefikBounds = "0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10"

// openSharedStore opens the store of a data directory that a running server
// holds, as `caravana admin` does.
func openSharedStore(t *testing.T, dataDir string) *store.Store {
	t.Helper()
	st, err := store.OpenShared(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("open the store in %s: %v", dataDir, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seedShape gives the row of one service for the period starting at ts that
// lasts mins minutes (1 for a minute row, 10 for a slot row).
type seedShape func(ts time.Time, mins int) store.MetricsSample

// seedRows writes rows of one series from from (inclusive) to until
// (exclusive), every step, with the resolution res (store.MinuteRes or
// store.SlotRes). It writes in one transaction per call.
func seedRows(t *testing.T, st *store.Store, series int64, res int, from, until time.Time, shape seedShape) int {
	t.Helper()
	step := time.Duration(res) * time.Second
	var rows store.MetricsRows
	for ts := from.Truncate(step); ts.Before(until); ts = ts.Add(step) {
		m := shape(ts, res/60)
		m.Series, m.TS, m.Minutes = series, ts, res/60
		rows.Samples = append(rows.Samples, m)
	}
	tick := store.MetricsTick{} // At zero: no start written, no prune
	if res == store.SlotRes {
		tick.Fold = rows
	} else {
		tick.Minute = rows
	}
	if err := st.WriteMetricsTick(context.Background(), tick); err != nil {
		t.Fatalf("seed rows of series %d: %v", series, err)
	}
	return len(rows.Samples)
}

// setMetricsStart moves the start of the baseline (metrics_state.started_at)
// to at, creating the row when no tick has written it yet. The format is the
// store's (nine fraction digits, UTC).
func setMetricsStart(t *testing.T, dataDir string, at time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "caravana.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	text := at.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	if _, err := db.Exec(`INSERT INTO metrics_state (id, started_at) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET started_at = excluded.started_at`, text); err != nil {
		t.Fatalf("move the start of the baseline: %v", err)
	}
}

// seedLayout is the store's ID for traefikBounds.
func seedLayout(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id, err := st.EnsureLayout(context.Background(), traefikBounds)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// spread splits n requests over the buckets by the fractions given (from the
// first bucket, le 0.005 s), the remainder to the largest share, in the stored
// form (per-bucket counts, trailing zeros cut).
func spread(n int64, fractions ...float64) string {
	b := make(metrics.Buckets, len(fractions))
	var given int64
	largest := 0
	for i, f := range fractions {
		b[i] = int64(math.Floor(float64(n) * f))
		given += b[i]
		if f > fractions[largest] {
			largest = i
		}
	}
	b[largest] += n - given
	return b.String()
}
