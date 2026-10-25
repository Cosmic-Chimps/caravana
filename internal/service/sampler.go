package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The sampler (feature 025). Once a minute it reads four sources, each on its
// own (one that fails leaves its columns NULL and never stops the others):
//
//   - Traefik's Prometheus numbers (requests, 5xx answers, latency buckets),
//   - what Nomad has reserved for each running copy,
//   - what each copy uses now (CPU and memory), and the copies' restarts,
//   - the host (memory, CPU, the watched filesystems).
//
// It writes one row per stored service (and one for the host and each
// filesystem) in one transaction, folds a 10-minute slot into its own row as
// the slot ends, prunes what is past its keep time every 10 minutes and tells
// open pages with ChangeMetrics. A gap is NULL, never zero.
//
// Between ticks it keeps, in memory only, what a delta needs: the previous
// Traefik scrape and when it was read, and each copy's restarts. A restart of
// Caravana loses them: its first minute has CPU and memory but no HTTP numbers
// and no restarts.

const (
	// defaultMetricsGap is the time between two ticks.
	defaultMetricsGap = time.Minute
	// defaultSamplerOffset is how long after a whole minute a tick runs, so
	// the counts of that minute are all in Traefik's numbers.
	defaultSamplerOffset = 2 * time.Second
	// samplerBudget is how long one tick may take, every source together.
	samplerBudget = 20 * time.Second
	// samplerPruneEvery is how often the prune runs.
	samplerPruneEvery = 10 * time.Minute
)

// SamplerState is what the sampler's last tick found when it read a source.
type SamplerState string

// The states of a source. Traefik is ok, off (it answers 404: its exporter is
// off, an old host that needs the installer run again), unreachable or
// disabled (no URL is set). Nomad is ok, denied (a 403 on the node reads: the
// policy predates feature 025) or unreachable.
const (
	SamplerUnknown     SamplerState = "unknown" // no tick has read it yet
	SamplerOK          SamplerState = "ok"
	SamplerOff         SamplerState = "off"
	SamplerUnreachable SamplerState = "unreachable"
	SamplerDenied      SamplerState = "denied"
	SamplerDisabled    SamplerState = "disabled"
)

// SamplerSources is the outcome of the last tick, for the page's notices.
type SamplerSources struct {
	Traefik SamplerState
	Nomad   SamplerState
	// At is when the last tick ran (the zero time before the first one).
	At time.Time
}

// copyMark is what the sampler remembers of a copy to see what changed.
type copyMark struct {
	crashes int  // Restarts less RequestedRestarts, the restarts nobody asked for
	failed  bool // the copy had failed
}

// samplerState is the sampler's settings and what it keeps between ticks.
// Everything but sources is touched only by the tick.
type samplerState struct {
	traefik    metrics.Source // nil: the Traefik numbers are off (no URL set)
	dockerRoot string
	offset     time.Duration

	prevSnap  *metrics.Snapshot // the last successful scrape
	prevAt    time.Time
	marks     map[string]copyMark // by copy ID; nil until the first read of the copies
	warned    map[string]bool     // collisions already logged
	lastFold  time.Time           // start of the newest slot this process folded
	lastPrune time.Time

	mu      sync.Mutex // guards sources
	sources SamplerSources
}

// SetMetricsSampleGap sets the time between two ticks of the sampler (a minute
// by default); the lab shortens it. Call it before Start; a value that isn't
// positive keeps the default.
func (s *Service) SetMetricsSampleGap(d time.Duration) {
	if d > 0 {
		s.metricsGap = d
	}
}

// SetTraefikMetricsURL sets where Traefik serves its Prometheus numbers
// (CARAVANA_TRAEFIK_METRICS_URL). Empty turns the request numbers off: the
// source reads "disabled". Call it before Start.
func (s *Service) SetTraefikMetricsURL(u string) {
	if u == "" {
		s.sampler.traefik = nil
		return
	}
	s.sampler.traefik = metrics.HTTPSource{URL: u}
}

// SetDockerRoot sets Docker's data directory (CARAVANA_DOCKER_ROOT), so the
// host's filesystem that holds the images is watched. Empty leaves it out.
// Call it before Start.
func (s *Service) SetDockerRoot(p string) { s.sampler.dockerRoot = p }

// SamplerSources is what the sampler's last tick found at each source: Traefik
// and Nomad. Before the first tick both are SamplerUnknown.
func (s *Service) SamplerSources() SamplerSources {
	st := &s.sampler
	st.mu.Lock()
	defer st.mu.Unlock()
	out := st.sources
	if out.Traefik == "" {
		out.Traefik = SamplerUnknown
	}
	if out.Nomad == "" {
		out.Nomad = SamplerUnknown
	}
	return out
}

// runSampler ticks every gap, at the whole gap plus a couple of seconds on the
// wall clock (so rows line up across restarts), until ctx ends. The first tick
// is the first whole gap after the start: nothing is read while Nomad may
// still be coming up.
func (s *Service) runSampler(ctx context.Context, gap time.Duration) {
	for {
		now := s.clock.Now()
		next := now.Truncate(gap).Add(gap + s.sampler.offset)
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := s.sampleTick(ctx, next); err != nil && ctx.Err() == nil {
			s.log.Warn("sample metrics", "error", err)
		}
	}
}

// samplerReads is what the four sources gave in one tick.
type samplerReads struct {
	traefikState SamplerState
	minutes      map[string]metrics.Minute // by Traefik service; nil when there are no HTTP numbers
	httpSecs     int64                     // seconds the counts cover

	reserved    deploy.Reservations
	reservedErr error

	copies    map[string][]deploy.Copy // by job ID
	copiesErr error
	usage     map[string]deploy.CopyUsage

	host    deploy.Host
	hostErr error
}

// serviceRef names one stored service.
type serviceRef struct{ app, service string }

// sampleTick is one tick at now: read the sources, write the rows. The rows'
// time is the whole gap that ended at now, which the counts cover.
func (s *Service) sampleTick(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, samplerBudget)
	defer cancel()
	st := &s.sampler
	gap := s.metricsGap
	now = now.UTC()
	boundary := now.Truncate(gap)
	ts := boundary.Add(-gap)

	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return fmt.Errorf("sample metrics: %w", err)
	}
	stored := map[string]map[string]bool{} // app ID -> service -> true
	for _, rec := range apps {
		stored[rec.App.ID] = map[string]bool{}
		for _, svc := range rec.App.Services {
			stored[rec.App.ID][svc.Name] = true
		}
	}
	labels := s.trafficLabels(apps)

	// The four sources, each on its own.
	var r samplerReads
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); s.readTraefik(ctx, now, gap, &r) }()
	go func() {
		defer wg.Done()
		r.reserved, r.reservedErr = s.deployer.Reservations(ctx)
	}()
	go func() { defer wg.Done(); s.readCopies(ctx, stored, &r) }()
	go func() {
		defer wg.Done()
		r.host, r.hostErr = s.deployer.HostStats(ctx, st.dockerRoot)
	}()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err() // shutting down or out of budget: write nothing
	}

	nomad := SamplerOK
	for _, e := range []error{r.reservedErr, r.copiesErr, r.hostErr} {
		switch {
		case e == nil:
		case errors.Is(e, deploy.ErrNodeReadDenied):
			nomad = SamplerDenied
		case nomad != SamplerDenied:
			nomad = SamplerUnreachable
		}
	}

	layouts := map[string]int64{}
	layoutID := func(l metrics.Layout) (int64, error) {
		key := l.String()
		if id, ok := layouts[key]; ok {
			return id, nil
		}
		id, err := s.store.EnsureLayout(ctx, key)
		if err == nil {
			layouts[key] = id
		}
		return id, err
	}

	restarts := s.restartDeltas(&r)
	var minute store.MetricsRows
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			series, err := s.store.EnsureSeries(ctx, rec.App.ID, svc.Name)
			if err != nil {
				return fmt.Errorf("sample metrics: %w", err)
			}
			m := store.MetricsSample{Series: series, TS: ts, Minutes: 1}
			label := trafficLabel(rec.App.ID, svc.Name)
			if len(svc.Domains) > 0 && r.minutes != nil && labels[label] == (serviceRef{rec.App.ID, svc.Name}) {
				if err := fillHTTP(&m, r.minutes[label], r.httpSecs, layoutID); err != nil {
					return fmt.Errorf("sample metrics: %w", err)
				}
			}
			fillNomad(&m, deploy.ServiceKey{JobID: rec.App.ID, Group: svc.Name}, &r)
			if v, ok := restarts[serviceRef{rec.App.ID, svc.Name}]; ok {
				m.Restarts = new(int64(v))
			} else if restarts != nil {
				m.Restarts = new(int64(0))
			}
			minute.Samples = append(minute.Samples, m)
		}
	}
	if r.hostErr == nil {
		minute.Hosts = append(minute.Hosts, hostSample(ts, &r))
		for _, d := range r.host.Disks {
			minute.Disks = append(minute.Disks, store.DiskSample{
				Node: r.host.NodeID, TS: ts, Mount: d.Mount, Holds: d.Holds(),
				UsedMB: int64(d.UsedMB), TotalMB: int64(d.TotalMB), FreeMB: int64(d.FreeMB),
			})
		}
	}

	tick := store.MetricsTick{At: now, Minute: minute}
	if slot := boundary.Truncate(metrics.SlotLength).Add(-metrics.SlotLength); slot.After(st.lastFold) {
		fold, err := s.foldSlot(ctx, slot, minute, layoutID)
		if err != nil {
			return fmt.Errorf("sample metrics: %w", err)
		}
		tick.Fold = fold
		st.lastFold = slot
	}
	if st.lastPrune.IsZero() || now.Sub(st.lastPrune) >= samplerPruneEvery {
		tick.Prune = true
		st.lastPrune = now
	}
	if err := s.store.WriteMetricsTick(ctx, tick); err != nil {
		return fmt.Errorf("sample metrics: %w", err)
	}

	st.mu.Lock()
	st.sources = SamplerSources{Traefik: r.traefikState, Nomad: nomad, At: now}
	st.mu.Unlock()
	s.publish(Change{What: ChangeMetrics})
	return nil
}

// trafficLabel is the name Traefik gives a service Caravana made: the job's
// router and service names come from "<app ID>-<service>", and the Nomad
// provider adds its own suffix.
func trafficLabel(appID, service string) string { return appID + "-" + service + "@nomad" }

// trafficLabels maps each Traefik service label to the stored service with a
// domain it names. A label two services would map to ("a-b"/"c" and "a"/"b-c")
// is attributed to neither, and logged once.
func (s *Service) trafficLabels(apps []store.AppRecord) map[string]serviceRef {
	out := map[string]serviceRef{}
	clash := map[string]bool{}
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			if len(svc.Domains) == 0 {
				continue
			}
			label := trafficLabel(rec.App.ID, svc.Name)
			if _, taken := out[label]; taken {
				clash[label] = true
			}
			out[label] = serviceRef{rec.App.ID, svc.Name}
		}
	}
	st := &s.sampler
	if st.warned == nil {
		st.warned = map[string]bool{}
	}
	for label := range clash {
		delete(out, label)
		if !st.warned[label] {
			st.warned[label] = true
			s.log.Warn("two services map to one Traefik service; the request numbers are attributed to neither", "traefikService", label)
		}
	}
	return out
}

// readTraefik scrapes Traefik and turns the difference from the previous
// successful scrape into a minute's counts. The baseline moves on every
// successful scrape, so a missed minute (the gap outside 50-90 s) only
// re-baselines, and so does the first scrape after a start.
func (s *Service) readTraefik(ctx context.Context, now time.Time, gap time.Duration, r *samplerReads) {
	st := &s.sampler
	if st.traefik == nil {
		r.traefikState = SamplerDisabled
		return
	}
	snap, err := metrics.Read(ctx, st.traefik)
	switch {
	case errors.Is(err, metrics.ErrOff):
		r.traefikState = SamplerOff
		return
	case err != nil:
		r.traefikState = SamplerUnreachable
		if ctx.Err() == nil {
			s.log.Warn("read Traefik's metrics", "error", err)
		}
		return
	}
	r.traefikState = SamplerOK
	prev, prevAt := st.prevSnap, st.prevAt
	st.prevSnap, st.prevAt = &snap, now
	if prev == nil {
		return
	}
	// metrics.Delta trusts a gap of 50-90 s, a minute's tick. A gap set
	// shorter (the lab's 10 s) is judged by the same ratio.
	elapsed := now.Sub(prevAt)
	scaled := time.Duration(float64(elapsed) * float64(defaultMetricsGap) / float64(gap))
	deltas, ok := metrics.Delta(*prev, snap, scaled)
	if !ok {
		return
	}
	r.minutes = metrics.TraefikMinute(deltas)
	r.httpSecs = int64(elapsed.Seconds() + 0.5)
}

// readCopies lists the copies of the stored apps and reads what the running
// ones use.
func (s *Service) readCopies(ctx context.Context, stored map[string]map[string]bool, r *samplerReads) {
	all, err := s.deployer.AllCopies(ctx)
	if err != nil {
		r.copiesErr = err
		return
	}
	r.copies = map[string][]deploy.Copy{}
	var running []deploy.Copy
	for job, copies := range all {
		services, ok := stored[job]
		if !ok {
			continue
		}
		for _, c := range copies {
			if !services[c.Service] {
				continue
			}
			r.copies[job] = append(r.copies[job], c)
			if !c.Terminal {
				running = append(running, c)
			}
		}
	}
	r.usage = s.deployer.Usage(ctx, running)
}

// restartDeltas is, per stored service, the restarts nobody asked for since
// the previous tick (Restarts less RequestedRestarts, per copy) plus one for
// each copy that has failed since. It is nil on the first read after a start,
// which has nothing to compare with, and when the copies weren't read; a nil
// map leaves every service's restarts NULL. The marks move on whenever the
// copies were read.
func (s *Service) restartDeltas(r *samplerReads) map[serviceRef]int {
	st := &s.sampler
	if r.copiesErr != nil {
		return nil
	}
	baseline := st.marks != nil
	out := map[serviceRef]int{}
	marks := map[string]copyMark{}
	for job, copies := range r.copies {
		for _, c := range copies {
			now := copyMark{crashes: max(c.Restarts-c.RequestedRestarts, 0), failed: c.Status == deploy.CopyFailed}
			marks[c.ID] = now
			if !baseline {
				continue
			}
			prev, seen := st.marks[c.ID]
			delta := max(now.crashes-prev.crashes, 0) // a copy not seen before counts all it has
			if now.failed && !(seen && prev.failed) {
				delta++
			}
			out[serviceRef{job, c.Service}] += delta
		}
	}
	st.marks = marks
	if !baseline {
		return nil
	}
	return out
}

// fillHTTP sets a row's HTTP columns from a Traefik service's minute. A
// service Traefik didn't list had no request in it: zeros, not a gap.
func fillHTTP(m *store.MetricsSample, mn metrics.Minute, secs int64, layoutID func(metrics.Layout) (int64, error)) error {
	m.HTTPSecs, m.Requests, m.Errors = &secs, new(mn.Requests), new(mn.Errors)
	if len(mn.Latency.Buckets) == 0 {
		return nil
	}
	id, err := layoutID(mn.Latency.Layout)
	if err != nil {
		return err
	}
	text := mn.Latency.Buckets.String()
	m.Layout, m.Buckets = &id, &text
	return nil
}

// fillNomad sets a row's columns that come from Nomad: the reservations, the
// copies and what they use. A source that wasn't read leaves its columns NULL.
// A service with no live copy has copies 0 and nothing reserved, and no memory
// limit, CPU or memory (nothing was measured).
func fillNomad(m *store.MetricsSample, key deploy.ServiceKey, r *samplerReads) {
	if r.reservedErr == nil {
		sum := r.reserved.ByKey[key]
		m.Copies = new(int64(sum.Copies))
		m.CPUReservedMHz = new(int64(sum.CPUMHz))
		m.MemoryReservedMB = new(int64(sum.MemoryMB))
		if sum.Copies > 0 {
			var limit int64
			for _, c := range r.reserved.Copies {
				if c.JobID == key.JobID && c.Group == key.Group {
					limit += int64(c.ServiceMemoryLimitMB())
				}
			}
			m.MemoryLimitMB = &limit
		}
	}
	if r.copiesErr != nil {
		return
	}
	var cpu, mem int64
	measured := false
	for _, c := range r.copies[key.JobID] {
		if c.Service != key.Group || c.Terminal {
			continue
		}
		u, read := r.usage[c.ID]
		if !read {
			continue // not started: nothing to measure yet
		}
		if u.Err != nil {
			return // a copy without a value makes the sum a gap, not a smaller number
		}
		cpu += int64(u.CPUMHz)
		mem += int64(u.MemoryMB)
		measured = true
	}
	if measured {
		m.CPUMHz, m.MemoryMB = &cpu, &mem
	}
}

// hostSample is the host's row for a tick. What is reserved is the sum over
// every live copy on the node, the ones of jobs Caravana didn't make too, plus
// what the node's own configuration holds back; NULL when the copies weren't
// listed.
func hostSample(ts time.Time, r *samplerReads) store.HostSample {
	h := r.host
	out := store.HostSample{
		Node: h.NodeID, TS: ts,
		MemoryUsedMB: new(int64(h.MemoryUsedMB)), MemoryTotalMB: new(int64(h.MemoryTotalMB)),
		CPUUsedMHz: new(int64(h.CPUUsedMHz)), CPUTotalMHz: new(int64(h.CPUTotalMHz)),
		CPUCores: new(int64(h.CPUCores)),
	}
	if r.reservedErr == nil {
		cpu, mem := h.ReservedCPUMHz, h.ReservedMemoryMB
		for _, c := range r.reserved.Copies {
			if c.NodeID == h.NodeID {
				cpu += c.CPUMHz
				mem += c.MemoryMB
			}
		}
		out.CPUReservedMHz, out.MemoryReservedMB = new(int64(cpu)), new(int64(mem))
	}
	return out
}

// foldSlot folds the 10-minute slot starting at slot: each series' minute rows
// (those stored and this tick's, which aren't written yet), the host's and the
// filesystems'. A series with no row in the slot gets none.
func (s *Service) foldSlot(ctx context.Context, slot time.Time, tickRows store.MetricsRows, layoutID func(metrics.Layout) (int64, error)) (store.MetricsRows, error) {
	end := slot.Add(metrics.SlotLength)
	in := func(t time.Time) bool { return !t.Before(slot) && t.Before(end) }
	bounds, err := s.store.MetricsLayouts(ctx)
	if err != nil {
		return store.MetricsRows{}, err
	}
	all, err := s.store.ListMetricsSeries(ctx)
	if err != nil {
		return store.MetricsRows{}, err
	}
	var out store.MetricsRows
	for _, series := range all {
		stored, err := s.store.MinuteRows(ctx, series.ID, slot, end)
		if err != nil {
			return store.MetricsRows{}, err
		}
		byTS := map[int64]store.MetricsSample{}
		for _, m := range stored {
			byTS[m.TS.Unix()] = m
		}
		for _, m := range tickRows.Samples {
			if m.Series == series.ID && in(m.TS) {
				byTS[m.TS.Unix()] = m
			}
		}
		if len(byTS) == 0 {
			continue
		}
		rows := make([]metrics.Row, 0, len(byTS))
		for _, m := range byTS {
			rows = append(rows, samplerRow(m, bounds))
		}
		folded := metrics.Fold(rows).Row
		folded.TS = slot
		sample, err := samplerSample(series.ID, folded, layoutID)
		if err != nil {
			return store.MetricsRows{}, err
		}
		out.Samples = append(out.Samples, sample)
	}

	hosts, err := s.store.HostRows(ctx, store.MinuteRes, slot, end)
	if err != nil {
		return store.MetricsRows{}, err
	}
	for _, h := range tickRows.Hosts {
		if in(h.TS) {
			hosts = append(hosts, h)
		}
	}
	out.Hosts = foldHosts(slot, hosts)
	disks, err := s.store.DiskRows(ctx, store.MinuteRes, slot, end)
	if err != nil {
		return store.MetricsRows{}, err
	}
	for _, d := range tickRows.Disks {
		if in(d.TS) {
			disks = append(disks, d)
		}
	}
	out.Disks = foldDisks(slot, disks)
	return out, nil
}

// samplerRow turns a stored row into the pure package's. A row whose layout or
// buckets can't be read keeps its numbers and loses only the buckets.
func samplerRow(m store.MetricsSample, layouts map[int64]string) metrics.Row {
	r := metrics.Row{
		TS: m.TS, Minutes: m.Minutes,
		HTTPSecs: m.HTTPSecs, Requests: m.Requests, Errors: m.Errors, Restarts: m.Restarts,
		CPUMHz: m.CPUMHz, CPUReservedMHz: m.CPUReservedMHz, MemoryMB: m.MemoryMB,
		MemoryLimitMB: m.MemoryLimitMB, MemoryReservedMB: m.MemoryReservedMB, Copies: m.Copies,
	}
	if m.Layout == nil || m.Buckets == nil {
		return r
	}
	text, ok := layouts[*m.Layout]
	if !ok {
		return r
	}
	layout, err := metrics.ParseLayout(text)
	if err != nil {
		return r
	}
	if b, err := metrics.ParseBuckets(*m.Buckets, layout); err == nil && len(b) > 0 {
		r.Hist = metrics.Histogram{Layout: layout, Buckets: b}
	}
	return r
}

// samplerSample is the reverse of samplerRow for a folded row.
func samplerSample(series int64, r metrics.Row, layoutID func(metrics.Layout) (int64, error)) (store.MetricsSample, error) {
	m := store.MetricsSample{
		Series: series, TS: r.TS, Minutes: r.Minutes,
		HTTPSecs: r.HTTPSecs, Requests: r.Requests, Errors: r.Errors, Restarts: r.Restarts,
		CPUMHz: r.CPUMHz, CPUReservedMHz: r.CPUReservedMHz, MemoryMB: r.MemoryMB,
		MemoryLimitMB: r.MemoryLimitMB, MemoryReservedMB: r.MemoryReservedMB, Copies: r.Copies,
	}
	if len(r.Hist.Layout) > 0 && len(r.Hist.Buckets) > 0 {
		id, err := layoutID(r.Hist.Layout)
		if err != nil {
			return store.MetricsSample{}, err
		}
		text := r.Hist.Buckets.String()
		m.Layout, m.Buckets = &id, &text
	}
	return m, nil
}

// foldHosts folds each node's minute rows of a slot into one row: the median
// of each column (they are gauges).
func foldHosts(slot time.Time, rows []store.HostSample) []store.HostSample {
	byNode := map[string][]store.HostSample{}
	var nodes []string
	for _, h := range rows {
		if _, ok := byNode[h.Node]; !ok {
			nodes = append(nodes, h.Node)
		}
		byNode[h.Node] = append(byNode[h.Node], h)
	}
	sort.Strings(nodes)
	var out []store.HostSample
	for _, node := range nodes {
		hs := byNode[node]
		pick := func(get func(store.HostSample) *int64) *int64 {
			var vs []int64
			for _, h := range hs {
				if v := get(h); v != nil {
					vs = append(vs, *v)
				}
			}
			return samplerMedian(vs)
		}
		out = append(out, store.HostSample{
			Node: node, TS: slot,
			MemoryUsedMB:     pick(func(h store.HostSample) *int64 { return h.MemoryUsedMB }),
			MemoryReservedMB: pick(func(h store.HostSample) *int64 { return h.MemoryReservedMB }),
			MemoryTotalMB:    pick(func(h store.HostSample) *int64 { return h.MemoryTotalMB }),
			CPUUsedMHz:       pick(func(h store.HostSample) *int64 { return h.CPUUsedMHz }),
			CPUReservedMHz:   pick(func(h store.HostSample) *int64 { return h.CPUReservedMHz }),
			CPUTotalMHz:      pick(func(h store.HostSample) *int64 { return h.CPUTotalMHz }),
			CPUCores:         pick(func(h store.HostSample) *int64 { return h.CPUCores }),
		})
	}
	return out
}

// foldDisks folds each filesystem's minute rows of a slot into one row: the
// median of its sizes, and the newest row's holds.
func foldDisks(slot time.Time, rows []store.DiskSample) []store.DiskSample {
	type key struct{ node, mount string }
	by := map[key][]store.DiskSample{}
	var keys []key
	for _, d := range rows {
		k := key{d.Node, d.Mount}
		if _, ok := by[k]; !ok {
			keys = append(keys, k)
		}
		by[k] = append(by[k], d)
	}
	slices.SortFunc(keys, func(a, b key) int {
		if a.node != b.node {
			return strings.Compare(a.node, b.node)
		}
		return strings.Compare(a.mount, b.mount)
	})
	var out []store.DiskSample
	for _, k := range keys {
		ds := by[k]
		var used, total, free []int64
		newest := ds[0]
		for _, d := range ds {
			used, total, free = append(used, d.UsedMB), append(total, d.TotalMB), append(free, d.FreeMB)
			if d.TS.After(newest.TS) {
				newest = d
			}
		}
		out = append(out, store.DiskSample{
			Node: k.node, TS: slot, Mount: k.mount, Holds: newest.Holds,
			UsedMB: *samplerMedian(used), TotalMB: *samplerMedian(total), FreeMB: *samplerMedian(free),
		})
	}
	return out
}

// samplerMedian is the median of vs, as metrics.Fold takes it: an even count
// gives the mean of the two in the middle, rounded half up. No values, nil.
func samplerMedian(vs []int64) *int64 {
	if len(vs) == 0 {
		return nil
	}
	slices.Sort(vs)
	n := len(vs)
	m := vs[n/2]
	if n%2 == 0 {
		a, b := vs[n/2-1], vs[n/2]
		m = a + (b-a+1)/2
	}
	return &m
}
