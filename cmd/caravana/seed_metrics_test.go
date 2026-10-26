package main

// Seeded monitoring history (feature 025): the band needs earlier days, and the
// lab has only minutes. The console step console-monitoring writes two earlier
// days for its own services, and TestSeedMetricsForReview writes a week for
// every stored service with a domain of a review instance, so the designer sees
// a full band without waiting a week.
//
// Both write through the store beside the running server (store.OpenShared, as
// the admin CLI opens it) and move metrics_state.started_at back with one SQL
// statement: the store has no call that moves it, on purpose (the server only
// ever writes it once).

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
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

// TestSeedMetricsForReview writes a week (CARAVANA_SEED_DAYS, 1 to 7) of
// plausible history into a review instance's store, so /monitoring shows the
// band, "usually" and "over N days" at once:
//
//	CARAVANA_SEED_METRICS=./data/caravana.db go test ./cmd/caravana -run TestSeedMetricsForReview -v
//
// For every stored service with a domain: a 10-minute row for each slot of the
// days and a minute row for each minute of the last 48 hours (the minute rows'
// keep time), with more traffic by day than by night (UTC), a little noise,
// latencies whose slowest 5% sits around 150 ms and a few 5xx; and the host's
// rows. Nothing is written at or after the instance's own first sample, so
// its real numbers stay; started_at moves back one day more than the days
// seeded. The server may keep running. Opt-in: skipped unless the variable is
// set.
func TestSeedMetricsForReview(t *testing.T) {
	path := os.Getenv("CARAVANA_SEED_METRICS")
	if path == "" {
		t.Skip("set CARAVANA_SEED_METRICS to a caravana.db (and optionally CARAVANA_SEED_DAYS, default 7) to seed a review instance")
	}
	days := 7
	if v := os.Getenv("CARAVANA_SEED_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 7 {
			t.Fatalf("CARAVANA_SEED_DAYS=%q: want a whole number from 1 to 7", v)
		}
		days = n
	}
	dataDir := filepath.Dir(path)
	ctx := context.Background()
	st := openSharedStore(t, dataDir)

	now := time.Now().UTC()
	until := now
	if started, err := st.MetricsStartedAt(ctx); err == nil && started.Before(until) {
		until = started
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	from := now.Add(-time.Duration(days) * 24 * time.Hour).Truncate(10 * time.Minute)
	minutesFrom := now.Add(-store.MinuteKeep).Truncate(time.Minute)
	if minutesFrom.Before(from) {
		minutesFrom = from
	}
	layout := seedLayout(t, st)

	apps, err := st.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	services := 0
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			if len(svc.Domains) == 0 {
				continue
			}
			series, err := st.EnsureSeries(ctx, rec.App.ID, svc.Name)
			if err != nil {
				t.Fatal(err)
			}
			h := fnv.New64a()
			h.Write([]byte(rec.App.ID + "/" + svc.Name))
			seed := h.Sum64()
			rng := rand.New(rand.NewPCG(seed, 25))
			base := 20 + float64(seed%100)      // requests a minute at the day's peak: 20-119
			memBase := 30 + float64(seed>>8%60) // MB at night
			limit := int64(1024)                // the default ceiling (CARAVANA_DEFAULT_MEMORY_MAX_MB)
			if svc.Resources.MemoryMaxMB > 0 {
				limit = int64(svc.Resources.MemoryMaxMB)
			}
			copies := int64(1)
			if svc.Replicas != nil && *svc.Replicas > 0 {
				copies = int64(*svc.Replicas)
			}
			shape := func(ts time.Time, mins int) store.MetricsSample {
				day := dayFactor(ts)
				noise := 0.9 + 0.2*rng.Float64()
				reqs := int64(math.Round(base * day * noise * float64(mins)))
				errs := int64(math.Round(float64(reqs) * 0.002 * 2 * rng.Float64()))
				secs := int64(mins * 60)
				// Busier hours are a little slower: some of the 50-100 ms share moves up.
				up := 0.04 * day
				buckets := spread(reqs, 0.05, 0.15, 0.35, 0.25, 0.13-up, 0.06+up, 0.01)
				return store.MetricsSample{
					HTTPSecs: &secs, Requests: &reqs, Errors: &errs, Layout: &layout, Buckets: &buckets,
					CPUMHz:           new(int64(math.Round((15 + 85*day) * noise * float64(copies)))),
					CPUReservedMHz:   new(100 * copies),
					MemoryMB:         new(int64(math.Round((memBase + 25*day) * float64(copies)))),
					MemoryLimitMB:    new(limit * copies),
					MemoryReservedMB: new(int64(256) * copies),
					Copies:           &copies,
					Restarts:         new(int64(0)),
				}
			}
			n := 0
			if from.Before(until) {
				n += seedRows(t, st, series, store.SlotRes, from, until.Truncate(10*time.Minute), shape)
			}
			if minutesFrom.Before(until) {
				n += seedRows(t, st, series, store.MinuteRes, minutesFrom, until.Truncate(time.Minute), shape)
			}
			services++
			t.Logf("%s/%s: %d rows, about %.0f requests a minute at the peak", rec.App.ID, svc.Name, n, base)
		}
	}

	// The host: what it is now, if it was ever read, with use following the day.
	host := store.HostSample{Node: "seeded", MemoryTotalMB: new(int64(3891)), MemoryReservedMB: new(int64(1312)),
		CPUTotalMHz: new(int64(4000)), CPUReservedMHz: new(int64(1730)), CPUCores: new(int64(4))}
	if newest, err := st.NewestHost(ctx); err == nil {
		host = newest
	}
	hostRows := func(res int, from, until time.Time) store.MetricsRows {
		var rows store.MetricsRows
		step := time.Duration(res) * time.Second
		rng := rand.New(rand.NewPCG(25, 25))
		for ts := from.Truncate(step); ts.Before(until); ts = ts.Add(step) {
			day := dayFactor(ts)
			h := host
			h.TS = ts
			if h.MemoryTotalMB != nil {
				h.MemoryUsedMB = new(int64(float64(*h.MemoryTotalMB) * (0.35 + 0.2*day + 0.03*rng.Float64())))
			}
			if h.CPUTotalMHz != nil {
				h.CPUUsedMHz = new(int64(float64(*h.CPUTotalMHz) * (0.05 + 0.25*day + 0.03*rng.Float64())))
			}
			rows.Hosts = append(rows.Hosts, h)
		}
		return rows
	}
	if from.Before(until) {
		if err := st.WriteMetricsTick(ctx, store.MetricsTick{Fold: hostRows(store.SlotRes, from, until.Truncate(10*time.Minute))}); err != nil {
			t.Fatal(err)
		}
	}
	if minutesFrom.Before(until) {
		if err := st.WriteMetricsTick(ctx, store.MetricsTick{Minute: hostRows(store.MinuteRes, minutesFrom, until.Truncate(time.Minute))}); err != nil {
			t.Fatal(err)
		}
	}

	start := now.Add(-time.Duration(days+1) * 24 * time.Hour)
	if until.Before(start) {
		start = until // an instance older than the days asked: its own start stays
	}
	setMetricsStart(t, dataDir, start)
	t.Logf("seeded %d services with a domain over %d days (rows before %s), the host, and the baseline's start at %s",
		services, days, until.Format(time.RFC3339), start.Format(time.RFC3339))
}

// dayFactor is how busy the hour is, 0.35 at 03:00 UTC to 1 at 15:00 UTC.
func dayFactor(ts time.Time) float64 {
	h := float64(ts.UTC().Hour()) + float64(ts.UTC().Minute())/60
	return 0.35 + 0.65*(0.5-0.5*math.Cos(2*math.Pi*(h-3)/24))
}
