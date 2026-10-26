package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The page's reads (feature 025). Every number here is computed on read from
// the rows the sampler stored; Nomad is asked only for what is live now (the
// copies, for "restarting" and the last exit), and when it can't answer the
// rest of the page is still true. A gap is a nil pointer, never a zero.

// MetricsSort is how the apps table is ordered.
type MetricsSort string

// The sorts of the apps table.
const (
	// SortUnusual lists unusual services first, the furthest past its rule
	// first, then the apps rolling out, then the rest by name.
	SortUnusual MetricsSort = "unusual"
	// SortTraffic lists the busiest first (requests per second).
	SortTraffic MetricsSort = "traffic"
	// SortMemory lists the one using the most memory first.
	SortMemory MetricsSort = "memory"
)

// ParseMetricsSort reads a sort's name; the empty name is SortUnusual.
func ParseMetricsSort(name string) (MetricsSort, bool) {
	switch MetricsSort(name) {
	case "", SortUnusual:
		return SortUnusual, true
	case SortTraffic, SortMemory:
		return MetricsSort(name), true
	}
	return "", false
}

// MetricsQuery asks for a place's numbers. An empty Project or Environment is
// Default or production; a zero Range is 6 h; an empty Sort is SortUnusual.
type MetricsQuery struct {
	Project, Environment string
	Range                metrics.Range
	Sort                 MetricsSort
}

// SeriesPoint is one point of a chart: the value and the band (the same UTC
// slot on the earlier days). Low, High and Usual are nil with no earlier day
// and while the baseline is being built; Days is how many earlier days the
// band rests on.
type SeriesPoint struct {
	Value, Low, High, Usual *float64
	Days                    int
}

// MetricSeries is one chart: the place's (or the app's) totals per point.
type MetricSeries struct {
	Points []SeriesPoint
	// Now is the newest point that has a value, within the last few minutes;
	// nil when none (a sampler that stopped doesn't leave a stale "now").
	Now *float64
	// Over is for the slowest 5 % only: Now is beyond the highest bucket
	// bound, so the page says "over 10 s".
	Over bool
	// Usual and UsualDays are the band's median and its days at Now's point.
	Usual     *float64
	UsualDays int
	// UnusualSince is when the chart's rule began to hold, for Errors and the
	// slowest 5 % (the warn rectangle); nil when the chart is within its band.
	UnusualSince *time.Time
}

// MetricDeploy is a deployment that ended healthy in the range, for the
// charts' markers.
type MetricDeploy struct {
	AppID, AppName, DeploymentID string
	Version                      uint64
	EndedAt                      time.Time
}

// DeployNear is the deploy clause of an issue: a deploy that ended within 15
// minutes before it started (Relation after, Minutes whole minutes between)
// or the first that ended after it started (Relation before).
type DeployNear struct {
	MetricDeploy
	Relation metrics.Relation
	Minutes  int
}

// IssueWindow is what the last 10 minutes added up to, for an issue's detail
// line.
type IssueWindow struct {
	Requests, Errors             int64
	ErrorPercent, RequestsPerSec float64
}

// LastExit is the service task's newest exit on a live copy.
type LastExit struct {
	Copy     string // the allocation ID
	ExitCode int
	At       time.Time
}

// MetricIssue is one unusual service, with the facts of its sentence.
type MetricIssue struct {
	AppID, AppName, Service string
	Kind                    metrics.Kind
	Level                   metrics.Level
	// Value is in the kind's unit: ms (slower), percent of requests (more
	// errors), requests per second (quieter), percent of the limit (memory),
	// restarts in the last hour (restarting).
	Value float64
	// Usual is the band's median in Value's unit; nil for memory and restarting.
	Usual *float64
	// LimitMB is the memory limit, for memory.
	LimitMB *float64
	// Restarts is the restarts nobody asked for in the last hour (memory and
	// restarting); nil when Nomad couldn't be read.
	Restarts *int
	// Since is when it started: the earliest minute of the unbroken run, at
	// most 6 hours back (SinceCapped says it reaches the limit). For
	// restarting, which has no run to walk, it is the start of the hour
	// counted.
	Since       time.Time
	SinceCapped bool
	Distance    float64
	DeployNear  *DeployNear
	Window      IssueWindow
	LastExit    *LastExit // memory and restarting, when known
	Domains     []string  // quieter: the service's domains
}

// ServiceState is the mark of a row of the apps table.
type ServiceState string

// The states of a service.
const (
	ServiceUnusual    ServiceState = "unusual"
	ServiceRollingOut ServiceState = "rolling_out"
	ServiceHealthy    ServiceState = "healthy"
	ServiceStopped    ServiceState = "stopped"
)

// ServiceMetrics is one row of the apps table.
type ServiceMetrics struct {
	AppID, AppName, Service string
	State                   ServiceState
	// Level is the worst level of its issues when it is unusual.
	Level     metrics.Level
	Copies    int
	HasDomain bool
	// Requests (per second), Errors (percent) and P95 (ms) are over the last
	// 10 minutes, as the issues' values are; nil without HTTP numbers. UsualP95 is the band's median now, nil while
	// the baseline is being built.
	Requests, Errors, P95, UsualP95 *float64
	P95Over                         bool
	// CPU is MHz per point, the sparkline.
	CPU            []*float64
	CPUReservedMHz *int64
	// MemoryMB and MemoryLimitMB are the newest minute's.
	MemoryMB, MemoryLimitMB *int64
	// Restarts is the sum over the range.
	Restarts *int64
	Unusual  bool
	Distance *float64
	// HTTPShared names the other service when Traefik's names collide, so the
	// numbers are attributed to neither.
	HTTPShared string
}

// MetricsBaseline says how much history the band rests on.
type MetricsBaseline struct {
	// Since is the first tick ever; nil when nothing was sampled.
	Since *time.Time
	State metrics.BaselineState
	// FullDays is the whole days since Since (0 before the first).
	FullDays int
}

// MetricsView is what getMetrics and getAppMetrics answer.
type MetricsView struct {
	Range  metrics.Range
	At     time.Time // the moment the answer is for
	Points []time.Time
	// The charts, for the place's (or the app's) totals.
	Requests, Errors, P95, Memory MetricSeries
	MemoryLimit                   []*float64
	// HasHTTP is false when no app of the place has a domain.
	HasHTTP  bool
	Deploys  []MetricDeploy
	Issues   []MetricIssue // the furthest past its rule first
	Services []ServiceMetrics
	Baseline MetricsBaseline
	// Sources is what the sampler's last tick could read.
	Sources SamplerSources
}

// liveRowAge is how old the newest row may be to count as "now".
const liveRowAge = 5 * time.Minute

// minuteReach is how far back the minute rows are read: the rules' walk back
// (6 h) and a margin.
const minuteReach = metrics.SinceLimit + time.Hour

// slotReach is how far back the 10-minute rows are read: all that is kept.
const slotReach = store.SlotKeep + time.Hour

// Metrics reads the numbers of the apps of a place. The place must exist
// (*PlaceError, field project or environment); an invalid sort is a
// *app.FieldError on sort. Nothing here fails because Nomad or Traefik is
// down: the rows are stored, and Sources says what the sampler couldn't read.
func (s *Service) Metrics(ctx context.Context, q MetricsQuery) (MetricsView, error) {
	project, environment := q.Project, q.Environment
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	if err := s.requireEnvironment(ctx, project, environment); err != nil {
		field := "environment"
		if errors.Is(err, ErrProjectNotFound) {
			field = "project"
		}
		if errors.Is(err, ErrProjectNotFound) || errors.Is(err, ErrEnvironmentNotFound) {
			return MetricsView{}, &PlaceError{Field: field, Err: err}
		}
		return MetricsView{}, err
	}
	recs, err := s.store.ListAppsIn(ctx, project, environment)
	if err != nil {
		return MetricsView{}, fmt.Errorf("read metrics: %w", err)
	}
	return s.metricsOf(ctx, recs, project, environment, "", q.Range, q.Sort)
}

// AppMetrics is Metrics for one app: the series are its totals and the
// deploys its own. An unknown app is store.ErrNotFound.
func (s *Service) AppMetrics(ctx context.Context, appID string, rng metrics.Range, sort MetricsSort) (MetricsView, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return MetricsView{}, err
	}
	return s.metricsOf(ctx, []store.AppRecord{rec}, rec.Place.Project, rec.Place.Environment, appID, rng, sort)
}

// svcRows is one stored service with its rows.
type svcRows struct {
	rec    store.AppRecord
	svc    app.Service
	minute []metrics.Row // the last minuteReach, oldest first
	slots  []metrics.Row // the stored 10-minute rows, oldest first
}

func (s *Service) metricsOf(ctx context.Context, recs []store.AppRecord, project, environment, onlyApp string, rng metrics.Range, sortBy MetricsSort) (MetricsView, error) {
	if rng.Name == "" {
		rng = metrics.Range6h
	}
	if sortBy == "" {
		sortBy = SortUnusual
	}
	if _, ok := ParseMetricsSort(string(sortBy)); !ok {
		return MetricsView{}, app.NewFieldError("sort", "must be unusual, traffic or memory")
	}
	now := s.clock.Now().UTC()

	rows, err := s.metricRows(ctx, recs, now)
	if err != nil {
		return MetricsView{}, err
	}
	baseline, err := s.metricsBaseline(ctx, now)
	if err != nil {
		return MetricsView{}, err
	}
	latest, err := s.store.LatestDeployments(ctx)
	if err != nil {
		return MetricsView{}, fmt.Errorf("read metrics: %w", err)
	}
	allApps, err := s.store.ListApps(ctx)
	if err != nil {
		return MetricsView{}, fmt.Errorf("read metrics: %w", err)
	}
	deploys, details, err := s.healthyDeploys(ctx, project, environment, onlyApp, recs, rng, now)
	if err != nil {
		return MetricsView{}, err
	}
	byJob, copiesErr := s.deployer.AllCopies(ctx)
	if copiesErr != nil {
		s.log.Warn("can't read copies from Nomad; the monitoring page leaves restarts out", "error", copiesErr)
	}

	view := MetricsView{
		Range: rng, At: now, Baseline: baseline, Sources: s.SamplerSources(),
		Points: make([]time.Time, rng.N()),
	}
	from := rng.From(now)
	for i := range view.Points {
		view.Points[i] = from.Add(time.Duration(i) * rng.Step)
	}
	for _, r := range rows {
		if len(r.svc.Domains) > 0 {
			view.HasHTTP = true
		}
	}
	view.Deploys = deploysIn(details, from)

	// The totals: the services' rows added up per slot, so the environment's
	// slowest 5 % comes from merged buckets and its band from its own totals.
	var minutes, slots [][]metrics.Row
	for _, r := range rows {
		minutes, slots = append(minutes, r.minute), append(slots, r.slots)
	}
	totalMinute, totalSlots := combineRows(minutes), combineRows(slots)
	totalHist := metrics.NewHistory(totalSlots)
	charts := chartsOf(rng, from, now, totalMinute, totalSlots, totalHist, baseline.State)
	view.Requests, view.Errors, view.P95, view.Memory = charts.requests, charts.errors, charts.p95, charts.memory
	view.MemoryLimit = charts.limit
	if iss := metrics.Judge(metrics.Facts{
		Now: now, Rows: totalMinute, Band: bandsAt(totalHist, now.Add(-5*time.Minute), baseline.State), State: baseline.State,
	}); len(iss) > 0 {
		for _, is := range iss {
			since := sinceOf(is.Kind, totalMinute, now, totalHist, baseline.State)
			switch is.Kind {
			case metrics.MoreErrors:
				view.Errors.UnusualSince = &since.at
			case metrics.Slower:
				view.P95.UnusualSince = &since.at
			}
		}
	}

	collisions := trafficCollisions(allApps)
	var exits map[ServiceKey]LastExit // read once, when an issue wants it
	exitsRead := false
	var services []ServiceMetrics
	for _, r := range rows {
		sv, issues := s.judgeService(r, rng, from, now, baseline, latest, byJob, copiesErr == nil, details, deploys)
		sv.HTTPShared = collisions[serviceRef{r.rec.App.ID, r.svc.Name}]
		for _, is := range issues {
			if (is.Kind == metrics.Memory || is.Kind == metrics.Restarting) && !exitsRead {
				exitsRead = true
				exits = s.lastExits(ctx)
			}
		}
		for i := range issues {
			if x, ok := exits[ServiceKey{r.rec.App.ID, r.svc.Name}]; ok && (issues[i].Kind == metrics.Memory || issues[i].Kind == metrics.Restarting) {
				issues[i].LastExit = &x
			}
		}
		services = append(services, sv)
		view.Issues = append(view.Issues, issues...)
	}
	slices.SortStableFunc(view.Issues, func(a, b MetricIssue) int {
		return cmp.Or(cmp.Compare(b.Distance, a.Distance), strings.Compare(a.AppID, b.AppID), strings.Compare(a.Service, b.Service))
	})
	sortServices(services, sortBy)
	view.Services = services
	return view, nil
}

// metricRows reads every stored service's rows with one pass over the series.
func (s *Service) metricRows(ctx context.Context, recs []store.AppRecord, now time.Time) ([]svcRows, error) {
	series, err := s.store.ListMetricsSeries(ctx)
	if err != nil {
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	ids := map[serviceRef]int64{}
	for _, m := range series {
		ids[serviceRef{m.AppID, m.Service}] = m.ID
	}
	layouts, err := s.store.MetricsLayouts(ctx)
	if err != nil {
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	to := now.Add(time.Minute)
	var out []svcRows
	for _, rec := range recs {
		for _, svc := range rec.App.Services {
			r := svcRows{rec: rec, svc: svc}
			if id, ok := ids[serviceRef{rec.App.ID, svc.Name}]; ok {
				mins, err := s.store.MinuteRows(ctx, id, now.Add(-minuteReach), to)
				if err != nil {
					return nil, fmt.Errorf("read metrics: %w", err)
				}
				sl, err := s.store.SlotRows(ctx, id, now.Add(-slotReach), to)
				if err != nil {
					return nil, fmt.Errorf("read metrics: %w", err)
				}
				for _, m := range mins {
					r.minute = append(r.minute, samplerRow(m, layouts))
				}
				for _, m := range sl {
					r.slots = append(r.slots, samplerRow(m, layouts))
				}
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// metricsBaseline reads when sampling began and says how much history there is.
func (s *Service) metricsBaseline(ctx context.Context, now time.Time) (MetricsBaseline, error) {
	started, err := s.store.MetricsStartedAt(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return MetricsBaseline{State: metrics.Building}, nil
	}
	if err != nil {
		return MetricsBaseline{}, fmt.Errorf("read metrics: %w", err)
	}
	b := MetricsBaseline{Since: &started, State: metrics.Baseline(started, now)}
	if d := now.Sub(started); d > 0 {
		b.FullDays = int(d / (24 * time.Hour))
	}
	return b, nil
}

// deployKey finds a deploy's details again from the pure package's Deploy.
type deployKey struct {
	app     string
	version uint64
	end     time.Time
}

// healthyDeploys lists the deployments of the place (or of one app) that ended
// healthy, from the earliest of the range's start and what an issue that began
// six hours ago may name. deploys feed metrics.DeployNear; details name them.
func (s *Service) healthyDeploys(ctx context.Context, project, environment, onlyApp string, recs []store.AppRecord, rng metrics.Range, now time.Time) ([]metrics.Deploy, map[deployKey]MetricDeploy, error) {
	from := rng.From(now)
	if reach := now.Add(-metrics.SinceLimit - metrics.DeployWindow); reach.Before(from) {
		from = reach
	}
	list, err := s.store.ListDeploymentsIn(ctx, project, environment, from, now)
	if err != nil {
		return nil, nil, fmt.Errorf("read metrics: %w", err)
	}
	names := map[string]string{}
	for _, rec := range recs {
		names[rec.App.ID] = rec.Place.Name
	}
	var deploys []metrics.Deploy
	details := map[deployKey]MetricDeploy{}
	for _, d := range list {
		if d.Status != store.StatusHealthy || d.JobVersion == nil || d.UpdatedAt.Before(from) || d.UpdatedAt.After(now) {
			continue
		}
		if onlyApp != "" && d.AppID != onlyApp {
			continue
		}
		end := d.UpdatedAt.UTC()
		name := names[d.AppID]
		if name == "" {
			name = d.Place.Name
		}
		deploys = append(deploys, metrics.Deploy{App: d.AppID, Version: *d.JobVersion, End: end})
		details[deployKey{d.AppID, *d.JobVersion, end}] = MetricDeploy{
			AppID: d.AppID, AppName: name, DeploymentID: d.Deployment.ID, Version: *d.JobVersion, EndedAt: end,
		}
	}
	return deploys, details, nil
}

// deploysIn lists the markers: the healthy ends since from, oldest first.
func deploysIn(details map[deployKey]MetricDeploy, from time.Time) []MetricDeploy {
	var out []MetricDeploy
	for _, d := range details {
		if !d.EndedAt.Before(from) {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b MetricDeploy) int {
		return cmp.Or(a.EndedAt.Compare(b.EndedAt), strings.Compare(a.AppID, b.AppID), cmp.Compare(a.Version, b.Version))
	})
	return out
}

// ServiceKey names one service of one app.
type ServiceKey struct{ AppID, Service string }

// lastExits reads the newest exit of each live service's task from Nomad's
// allocations (one request plus the cached job lifecycles). A failure leaves
// the issues without it.
func (s *Service) lastExits(ctx context.Context) map[ServiceKey]LastExit {
	res, err := s.deployer.Reservations(ctx)
	if err != nil {
		s.log.Warn("can't read the copies' last exits from Nomad", "error", err)
		return nil
	}
	out := map[ServiceKey]LastExit{}
	for _, c := range res.Copies {
		if c.ExitCode == nil || c.ExitedAt == nil {
			continue
		}
		k := ServiceKey{c.JobID, c.Group}
		if old, ok := out[k]; !ok || c.ExitedAt.After(old.At) {
			out[k] = LastExit{Copy: c.CopyID, ExitCode: *c.ExitCode, At: c.ExitedAt.UTC()}
		}
	}
	return out
}

// combineRows adds the services' rows up per time: the counters are summed,
// the gauges too (memory of a place is the sum of its services'), the buckets
// merged, and the seconds the HTTP counts cover are the longest of them (every
// service's counts cover the same minute, so summing the seconds would divide
// the requests by the number of services). A column is nil when every row's
// is. The result is sorted by time.
func combineRows(perService [][]metrics.Row) []metrics.Row {
	at := map[time.Time][]metrics.Row{}
	for _, rows := range perService {
		for _, r := range rows {
			at[r.TS] = append(at[r.TS], r)
		}
	}
	out := make([]metrics.Row, 0, len(at))
	for ts, rs := range at {
		out = append(out, combineAt(ts, rs))
	}
	slices.SortFunc(out, func(a, b metrics.Row) int { return a.TS.Compare(b.TS) })
	return out
}

func combineAt(ts time.Time, rs []metrics.Row) metrics.Row {
	out := metrics.Row{TS: ts}
	sum := func(get func(metrics.Row) *int64) *int64 {
		var total int64
		seen := false
		for _, r := range rs {
			if v := get(r); v != nil {
				total += *v
				seen = true
			}
		}
		if !seen {
			return nil
		}
		return &total
	}
	for _, r := range rs {
		out.Minutes = max(out.Minutes, r.Minutes)
		if r.HTTPSecs != nil && (out.HTTPSecs == nil || *r.HTTPSecs > *out.HTTPSecs) {
			out.HTTPSecs = new(*r.HTTPSecs)
		}
	}
	out.Requests = sum(func(r metrics.Row) *int64 { return r.Requests })
	out.Errors = sum(func(r metrics.Row) *int64 { return r.Errors })
	out.Restarts = sum(func(r metrics.Row) *int64 { return r.Restarts })
	out.CPUMHz = sum(func(r metrics.Row) *int64 { return r.CPUMHz })
	out.CPUReservedMHz = sum(func(r metrics.Row) *int64 { return r.CPUReservedMHz })
	out.MemoryMB = sum(func(r metrics.Row) *int64 { return r.MemoryMB })
	out.MemoryLimitMB = sum(func(r metrics.Row) *int64 { return r.MemoryLimitMB })
	out.MemoryReservedMB = sum(func(r metrics.Row) *int64 { return r.MemoryReservedMB })
	out.Copies = sum(func(r metrics.Row) *int64 { return r.Copies })

	var hists []metrics.Histogram
	var layout metrics.Layout
	for _, r := range rs {
		if len(r.Hist.Buckets) > 0 {
			hists = append(hists, r.Hist)
			if layout == nil {
				layout = r.Hist.Layout
			}
		}
	}
	if layout != nil {
		if b, _ := metrics.Merge(layout, hists...); len(b) > 0 {
			out.Hist = metrics.Histogram{Layout: layout, Buckets: b}
		}
	}
	return out
}

// bandsAt is the band at a moment, or none while the baseline is being built:
// there are no earlier days to speak of, and "usually" would claim too much.
func bandsAt(h metrics.History, at time.Time, state metrics.BaselineState) metrics.Bands {
	if state == metrics.Building {
		return metrics.Bands{}
	}
	return h.Band(at, metrics.SlotLength)
}

// charts is a set of points read as the page's four charts, plus the CPU and
// restarts a services row needs.
type charts struct {
	requests, errors, p95, memory MetricSeries
	limit                         []*float64
	cpu                           []*float64
	restarts                      *int64
	// whole is every point folded into one row: the range's totals.
	whole metrics.Row
}

// chartsOf makes the points of rng from the rows (minute rows for 1 h and 6 h,
// the stored 10-minute rows plus the slot in progress for 24 h and 7 d) and the
// band of each.
func chartsOf(rng metrics.Range, from, now time.Time, minute, slots []metrics.Row, hist metrics.History, state metrics.BaselineState) charts {
	src := minute
	if rng.FromSlots {
		src = metrics.Slots(slots, minute)
	}
	pts := metrics.Points(src, from, rng.Step, rng.N())

	var c charts
	n := len(pts)
	c.requests.Points = make([]SeriesPoint, n)
	c.errors.Points = make([]SeriesPoint, n)
	c.p95.Points = make([]SeriesPoint, n)
	c.memory.Points = make([]SeriesPoint, n)
	c.limit = make([]*float64, n)
	c.cpu = make([]*float64, n)
	bands := make([]metrics.Bands, n)
	var nonGap []metrics.Row
	for i, p := range pts {
		if p.Gap() {
			continue
		}
		nonGap = append(nonGap, p.Row)
		b := bandsAt(hist, p.T, state)
		bands[i] = b
		if v, ok := p.RequestsPerSec(); ok {
			c.requests.Points[i] = bandPoint(v, b.RequestsPerMin, 60)
		} else {
			c.requests.Points[i] = bandPoint(0, b.RequestsPerMin, 60)
			c.requests.Points[i].Value = nil
		}
		if v, ok := p.ErrorPercent(); ok {
			c.errors.Points[i] = bandPoint(v, b.ErrorPercent, 1)
		} else {
			c.errors.Points[i] = bandPoint(0, b.ErrorPercent, 1)
			c.errors.Points[i].Value = nil
		}
		if v, _, ok := p.P95(); ok {
			c.p95.Points[i] = bandPoint(v, b.P95Millis, 1)
		} else {
			c.p95.Points[i] = bandPoint(0, b.P95Millis, 1)
			c.p95.Points[i].Value = nil
		}
		if p.Row.MemoryMB != nil {
			c.memory.Points[i] = bandPoint(float64(*p.Row.MemoryMB), b.MemoryMB, 1)
		} else {
			c.memory.Points[i] = bandPoint(0, b.MemoryMB, 1)
			c.memory.Points[i].Value = nil
		}
		c.limit[i] = int64Ptr(p.Row.MemoryLimitMB)
		c.cpu[i] = int64Ptr(p.Row.CPUMHz)
	}
	c.whole = metrics.Fold(nonGap).Row
	if len(nonGap) == 0 {
		c.whole = metrics.Row{}
	}
	if c.whole.Restarts != nil {
		c.restarts = c.whole.Restarts
	}

	// "Now" is the newest point with a value, if it is a recent one.
	live := now.Add(-liveRowAge - rng.Step)
	idx := -1
	for i := n - 1; i >= 0 && pts[i].T.After(live); i-- {
		if !pts[i].Gap() {
			idx = i
			break
		}
	}
	if idx >= 0 {
		p := pts[idx]
		c.requests.Now, c.requests.Usual, c.requests.UsualDays = nowOf(c.requests.Points[idx])
		c.errors.Now, c.errors.Usual, c.errors.UsualDays = nowOf(c.errors.Points[idx])
		c.p95.Now, c.p95.Usual, c.p95.UsualDays = nowOf(c.p95.Points[idx])
		c.memory.Now = c.memory.Points[idx].Value
		c.memory.UsualDays = c.memory.Points[idx].Days
		if _, over, ok := p.P95(); ok && c.p95.Now != nil {
			c.p95.Over = over
		}
	}
	return c
}

func nowOf(p SeriesPoint) (value, usual *float64, days int) { return p.Value, p.Usual, p.Days }

// bandPoint makes a point of value v with the band b divided by div (the
// requests' band is per minute and the chart per second).
func bandPoint(v float64, b metrics.BandRange, div float64) SeriesPoint {
	p := SeriesPoint{Value: &v}
	if b.Has() {
		p.Low, p.High, p.Usual = new(b.Low/div), new(b.High/div), new(b.Usual/div)
		p.Days = b.Days
	}
	return p
}

func int64Ptr(v *int64) *float64 {
	if v == nil {
		return nil
	}
	return new(float64(*v))
}

// sinceResult is when an unusual run began.
type sinceResult struct {
	at     time.Time
	capped bool
}

// sinceOf is metrics.Since with the fallback: when the newest minute can't be
// judged to still break the rule, the start of the 10-minute window.
func sinceOf(k metrics.Kind, minute []metrics.Row, now time.Time, hist metrics.History, state metrics.BaselineState) sinceResult {
	at, capped, ok := metrics.Since(k, minute, now, func(t time.Time) metrics.Bands { return bandsAt(hist, t, state) })
	if !ok {
		return sinceResult{at: now.Add(-metrics.WindowMinutes * time.Minute)}
	}
	return sinceResult{at: at, capped: capped}
}

// judgeService applies the five rules to one service and makes its table row.
func (s *Service) judgeService(r svcRows, rng metrics.Range, from, now time.Time, baseline MetricsBaseline,
	latest map[string]store.DeploymentRecord, byJob map[string][]Copy, copiesKnown bool,
	details map[deployKey]MetricDeploy, deploys []metrics.Deploy) (ServiceMetrics, []MetricIssue) {

	id := r.rec.App.ID
	hist := metrics.NewHistory(r.slots)
	state := baseline.State
	stopped := r.rec.StoppedAt != nil
	rolling := false
	if d, ok := latest[id]; ok {
		rolling = d.Status == store.StatusPending || d.Status == store.StatusRunning
	}

	var mine []Copy
	for _, c := range byJob[id] {
		if c.Service == r.svc.Name {
			mine = append(mine, c)
		}
	}
	restarts := crashesInHour(now, mine)
	known := copiesKnown && !stopped

	ch := chartsOf(rng, from, now, r.minute, r.slots, hist, state)
	sv := ServiceMetrics{
		AppID: id, AppName: r.rec.Place.Name, Service: r.svc.Name, State: ServiceHealthy,
		HasDomain: len(r.svc.Domains) > 0, CPU: ch.cpu, Restarts: ch.restarts,
	}
	if stopped {
		sv.State = ServiceStopped
	} else if rolling {
		sv.State = ServiceRollingOut
	}
	if sv.HasDomain {
		// The table's HTTP numbers are the last 10 minutes', the same window
		// the issues and the chart heads read, so a row never disagrees with
		// its own issue. Only the CPU sparkline keeps the range.
		w := metrics.WindowOf(r.minute, now)
		if w.HasHTTP {
			sv.Requests = new(w.RequestsPerSec)
			if w.Requests > 0 {
				sv.Errors = new(w.ErrorPercent)
			}
		}
		if w.HasP95 {
			sv.P95, sv.P95Over = new(w.P95Millis), w.P95Over
		}
		if b := bandsAt(hist, now.Add(-5*time.Minute), state); b.P95Millis.Has() {
			sv.UsualP95 = new(b.P95Millis.Usual)
		}
	}
	live, hasNewest := newestRow(r.minute, now)
	if hasNewest {
		sv.MemoryMB, sv.MemoryLimitMB = live.MemoryMB, live.MemoryLimitMB
		sv.CPUReservedMHz = live.CPUReservedMHz
	}
	switch {
	case stopped:
	case copiesKnown:
		for _, c := range mine {
			if !c.Terminal {
				sv.Copies++
			}
		}
	case hasNewest && live.Copies != nil:
		sv.Copies = int(*live.Copies)
	}

	found := metrics.Judge(metrics.Facts{
		Now: now, Rows: r.minute, Band: bandsAt(hist, now.Add(-5*time.Minute), state), State: state,
		Restarts: restarts, RestartsKnown: known, RollingOut: rolling, Stopped: stopped,
	})
	var issues []MetricIssue
	limit, hasLimit := limitOf(r.minute)
	for _, is := range found {
		mi := MetricIssue{
			AppID: id, AppName: r.rec.Place.Name, Service: r.svc.Name, Kind: is.Kind, Level: is.Level,
			Value: is.Value, Distance: is.Distance,
			Window: IssueWindow{
				Requests: is.Window.Requests, Errors: is.Window.Errors,
				ErrorPercent: is.Window.ErrorPercent, RequestsPerSec: is.Window.RequestsPerSec,
			},
		}
		switch is.Kind {
		case metrics.Slower, metrics.MoreErrors, metrics.Quieter:
			mi.Usual = new(is.Usual)
		case metrics.Memory:
			if hasLimit {
				mi.LimitMB = new(limit)
			}
			if known {
				mi.Restarts = new(restarts)
			}
		case metrics.Restarting:
			mi.Restarts = new(is.Restarts)
		}
		if is.Kind == metrics.Quieter {
			for _, d := range r.svc.Domains {
				mi.Domains = append(mi.Domains, d.Host)
			}
		}
		if is.Kind == metrics.Restarting {
			mi.Since = now.Add(-recentFailure)
		} else {
			sr := sinceOf(is.Kind, r.minute, now, hist, state)
			mi.Since, mi.SinceCapped = sr.at, sr.capped
		}
		if near, ok := metrics.DeployNear(mi.Since, now, deploys); ok {
			if d, found := details[deployKey{near.App, near.Version, near.End}]; found {
				mi.DeployNear = &DeployNear{MetricDeploy: d, Relation: near.Relation, Minutes: near.Minutes}
			}
		}
		issues = append(issues, mi)
	}
	if len(issues) > 0 {
		sv.State, sv.Unusual, sv.Level = ServiceUnusual, true, metrics.LevelWarning
		worst := 0.0
		for _, is := range issues {
			worst = max(worst, is.Distance)
			if is.Level == metrics.LevelFailed {
				sv.Level = metrics.LevelFailed
			}
		}
		sv.Distance = &worst
	}
	return sv, issues
}

// newestRow is the newest minute row if it is recent enough to be "now".
func newestRow(rows []metrics.Row, now time.Time) (metrics.Row, bool) {
	if len(rows) == 0 {
		return metrics.Row{}, false
	}
	r := rows[len(rows)-1]
	if now.Sub(r.TS) > liveRowAge {
		return metrics.Row{}, false
	}
	return r, true
}

// limitOf is the memory limit of the newest minute row that has one, in
// megabytes.
func limitOf(rows []metrics.Row) (float64, bool) {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].MemoryLimitMB != nil {
			return float64(*rows[i].MemoryLimitMB), true
		}
	}
	return 0, false
}

// sortServices orders the apps table.
func sortServices(list []ServiceMetrics, by MetricsSort) {
	name := func(a, b ServiceMetrics) int {
		return cmp.Or(strings.Compare(a.AppID, b.AppID), strings.Compare(a.Service, b.Service))
	}
	// descending puts the larger value first and a missing one last.
	descending := func(a, b float64, aok, bok bool) int {
		switch {
		case aok && bok:
			return cmp.Compare(b, a)
		case aok:
			return -1
		case bok:
			return 1
		}
		return 0
	}
	slices.SortStableFunc(list, func(a, b ServiceMetrics) int {
		switch by {
		case SortTraffic:
			av, bv := deref(a.Requests), deref(b.Requests)
			return cmp.Or(descending(av.v, bv.v, av.ok, bv.ok), name(a, b))
		case SortMemory:
			av, bv := derefInt(a.MemoryMB), derefInt(b.MemoryMB)
			return cmp.Or(descending(av.v, bv.v, av.ok, bv.ok), name(a, b))
		}
		rank := func(s ServiceMetrics) int {
			switch s.State {
			case ServiceUnusual:
				return 0
			case ServiceRollingOut:
				return 1
			}
			return 2
		}
		ad, bd := deref(a.Distance), deref(b.Distance)
		if rank(a) == 0 && rank(b) == 0 {
			return cmp.Or(descending(ad.v, bd.v, ad.ok, bd.ok), name(a, b))
		}
		return cmp.Or(cmp.Compare(rank(a), rank(b)), name(a, b))
	})
}

type maybe struct {
	v  float64
	ok bool
}

func deref(p *float64) maybe {
	if p == nil {
		return maybe{}
	}
	return maybe{*p, true}
}

func derefInt(p *int64) maybe {
	if p == nil {
		return maybe{}
	}
	return maybe{float64(*p), true}
}

// trafficCollisions finds the services whose Traefik name another service
// shares ("a-b"/"c" and "a"/"b-c" are both "a-b-c"): the sampler attributes
// their numbers to neither, and the page says which other service it is.
func trafficCollisions(apps []store.AppRecord) map[serviceRef]string {
	byLabel := map[string][]serviceRef{}
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			if len(svc.Domains) > 0 {
				l := trafficLabel(rec.App.ID, svc.Name)
				byLabel[l] = append(byLabel[l], serviceRef{rec.App.ID, svc.Name})
			}
		}
	}
	out := map[serviceRef]string{}
	for _, refs := range byLabel {
		if len(refs) < 2 {
			continue
		}
		for _, r := range refs {
			for _, other := range refs {
				if other != r {
					out[r] = other.app + "/" + other.service
					break
				}
			}
		}
	}
	return out
}
