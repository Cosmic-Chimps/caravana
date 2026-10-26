package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Monitoring and capacity (feature 025): getMetrics, getAppMetrics and
// getHost. Every answer is computed by the same service call the console makes,
// from the stored samples, so Nomad or Traefik being down never fails a read:
// `sources` says what the sampler couldn't read. A gap is JSON null, never 0
// (a nil pointer encodes as null, and a list is always written as a list).

// metricsError maps the errors of the metrics reads. A place error comes first
// (it can wrap store.ErrNotFound too), then a field error as 422 (the service
// checks sort again), then an unknown app as 404 app_not_found.
func metricsError(err error) error {
	if e := placeError(err); e != nil {
		return e
	}
	var fe *app.FieldError
	switch {
	case errors.As(err, &fe):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	case errors.Is(err, store.ErrNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeAppNotFound, Field: "app", Message: err.Error()}
	}
	return err
}

// metricsParams reads range and sort. An empty value is the default (6h,
// unusual); anything else that isn't a name is 422 invalid_field.
func metricsParams(r *http.Request) (metrics.Range, service.MetricsSort, error) {
	v := r.URL.Query()
	rng := metrics.Range6h
	if name := v.Get("range"); name != "" {
		var ok bool
		if rng, ok = metrics.ParseRange(name); !ok {
			return rng, "", invalidMetricsField("range", "use 1h, 6h, 24h or 7d")
		}
	}
	sortBy, ok := service.ParseMetricsSort(v.Get("sort"))
	if !ok {
		return rng, "", invalidMetricsField("sort", "use unusual, traffic or memory")
	}
	return rng, sortBy, nil
}

func invalidMetricsField(field, message string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: field, Message: message}
}

// The JSON of the reads (schemas Metrics, MetricSeries, MetricPoint,
// MetricDeploy, MetricIssue, ServiceMetrics, MetricsBaseline, MetricsSources
// and Host).

type metricPointJSON struct {
	Value *float64 `json:"value"`
	Low   *float64 `json:"low"`
	High  *float64 `json:"high"`
	Usual *float64 `json:"usual"`
	Days  int      `json:"days"`
}

type metricSeriesJSON struct {
	Points       []metricPointJSON `json:"points"`
	Now          *float64          `json:"now"`
	Over         bool              `json:"over"`
	Usual        *float64          `json:"usual"`
	UsualDays    int               `json:"usualDays"`
	UnusualSince *time.Time        `json:"unusualSince"`
}

type metricsSeriesSetJSON struct {
	Requests    metricSeriesJSON `json:"requests"`
	Errors      metricSeriesJSON `json:"errors"`
	P95         metricSeriesJSON `json:"p95"`
	Memory      metricSeriesJSON `json:"memory"`
	MemoryLimit []*float64       `json:"memoryLimit"`
	HasHTTP     bool             `json:"hasHttp"`
}

type metricDeployJSON struct {
	AppID        string    `json:"appId"`
	AppName      string    `json:"appName"`
	DeploymentID string    `json:"deploymentId"`
	Version      uint64    `json:"version"`
	EndedAt      time.Time `json:"endedAt"`
}

type deployNearJSON struct {
	metricDeployJSON
	Relation string `json:"relation"`
	Minutes  *int   `json:"minutes,omitempty"` // "after" only
}

type issueWindowJSON struct {
	Requests          int64   `json:"requests"`
	Errors            int64   `json:"errors"`
	ErrorPercent      float64 `json:"errorPercent"`
	RequestsPerSecond float64 `json:"requestsPerSecond"`
}

type lastExitJSON struct {
	Copy     string    `json:"copy"`
	ExitCode int       `json:"exitCode"`
	At       time.Time `json:"at"`
}

type metricIssueJSON struct {
	AppID       string          `json:"appId"`
	AppName     string          `json:"appName"`
	Service     string          `json:"service"`
	Kind        string          `json:"kind"`
	Level       string          `json:"level"`
	Value       float64         `json:"value"`
	Usual       *float64        `json:"usual"`
	LimitMB     *float64        `json:"limitMb"`
	Restarts    *int            `json:"restarts"`
	Since       time.Time       `json:"since"`
	SinceCapped bool            `json:"sinceCapped"`
	Distance    float64         `json:"distance"`
	DeployNear  *deployNearJSON `json:"deployNear,omitempty"`
	Window      issueWindowJSON `json:"window"`
	LastExit    *lastExitJSON   `json:"lastExit,omitempty"`
	Domains     []string        `json:"domains,omitempty"`
}

type serviceMetricsJSON struct {
	AppID          string     `json:"appId"`
	AppName        string     `json:"appName"`
	Service        string     `json:"service"`
	State          string     `json:"state"`
	Level          *string    `json:"level"`
	Copies         int        `json:"copies"`
	HasDomain      bool       `json:"hasDomain"`
	Requests       *float64   `json:"requests"`
	Errors         *float64   `json:"errors"`
	P95            *float64   `json:"p95"`
	P95Over        bool       `json:"p95Over"`
	UsualP95       *float64   `json:"usualP95"`
	CPU            []*float64 `json:"cpu"`
	CPUReservedMhz *int64     `json:"cpuReservedMhz"`
	MemoryMB       *int64     `json:"memoryMb"`
	MemoryLimitMB  *int64     `json:"memoryLimitMb"`
	Restarts       *int64     `json:"restarts"`
	Unusual        bool       `json:"unusual"`
	Distance       *float64   `json:"distance"`
	HTTPShared     *string    `json:"httpShared"`
}

type baselineJSON struct {
	Since    *time.Time `json:"since"`
	State    string     `json:"state"`
	FullDays int        `json:"fullDays"`
}

type sourceJSON struct {
	State string     `json:"state"`
	At    *time.Time `json:"at"`
}

type sourcesJSON struct {
	Traefik sourceJSON `json:"traefik"`
	Nomad   sourceJSON `json:"nomad"`
}

type metricsJSON struct {
	Range    string               `json:"range"`
	Step     int                  `json:"step"`
	Points   []time.Time          `json:"points"`
	Series   metricsSeriesSetJSON `json:"series"`
	Deploys  []metricDeployJSON   `json:"deploys"`
	Issues   []metricIssueJSON    `json:"issues"`
	Services []serviceMetricsJSON `json:"services"`
	Baseline baselineJSON         `json:"baseline"`
	Sources  sourcesJSON          `json:"sources"`
}

func toSeriesJSON(s service.MetricSeries) metricSeriesJSON {
	out := metricSeriesJSON{
		Points: make([]metricPointJSON, 0, len(s.Points)),
		Now:    s.Now, Over: s.Over, Usual: s.Usual, UsualDays: s.UsualDays,
	}
	if s.UnusualSince != nil {
		out.UnusualSince = new(s.UnusualSince.UTC())
	}
	for _, p := range s.Points {
		out.Points = append(out.Points, metricPointJSON{Value: p.Value, Low: p.Low, High: p.High, Usual: p.Usual, Days: p.Days})
	}
	return out
}

func toMetricDeployJSON(d service.MetricDeploy) metricDeployJSON {
	return metricDeployJSON{AppID: d.AppID, AppName: d.AppName, DeploymentID: d.DeploymentID, Version: d.Version, EndedAt: d.EndedAt.UTC()}
}

func toIssueJSON(is service.MetricIssue) metricIssueJSON {
	out := metricIssueJSON{
		AppID: is.AppID, AppName: is.AppName, Service: is.Service, Kind: string(is.Kind), Level: string(is.Level),
		Value: is.Value, Usual: is.Usual, LimitMB: is.LimitMB, Restarts: is.Restarts,
		Since: is.Since.UTC(), SinceCapped: is.SinceCapped, Distance: is.Distance,
		Window: issueWindowJSON{Requests: is.Window.Requests, Errors: is.Window.Errors,
			ErrorPercent: is.Window.ErrorPercent, RequestsPerSecond: is.Window.RequestsPerSec},
		Domains: is.Domains,
	}
	if n := is.DeployNear; n != nil {
		out.DeployNear = &deployNearJSON{metricDeployJSON: toMetricDeployJSON(n.MetricDeploy), Relation: string(n.Relation)}
		if n.Relation == metrics.StartedAfter {
			out.DeployNear.Minutes = new(n.Minutes)
		}
	}
	if x := is.LastExit; x != nil {
		out.LastExit = &lastExitJSON{Copy: x.Copy, ExitCode: x.ExitCode, At: x.At.UTC()}
	}
	return out
}

func toServiceMetricsJSON(sv service.ServiceMetrics) serviceMetricsJSON {
	out := serviceMetricsJSON{
		AppID: sv.AppID, AppName: sv.AppName, Service: sv.Service, State: string(sv.State),
		Copies: sv.Copies, HasDomain: sv.HasDomain,
		Requests: sv.Requests, Errors: sv.Errors, P95: sv.P95, P95Over: sv.P95Over, UsualP95: sv.UsualP95,
		CPU: sv.CPU, CPUReservedMhz: sv.CPUReservedMHz, MemoryMB: sv.MemoryMB, MemoryLimitMB: sv.MemoryLimitMB,
		Restarts: sv.Restarts, Unusual: sv.Unusual, Distance: sv.Distance,
	}
	if out.CPU == nil {
		out.CPU = []*float64{}
	}
	if sv.Unusual {
		out.Level = new(string(sv.Level))
	}
	if sv.HTTPShared != "" {
		out.HTTPShared = new(sv.HTTPShared)
	}
	return out
}

func toBaselineJSON(b service.MetricsBaseline) baselineJSON {
	out := baselineJSON{State: string(b.State), FullDays: b.FullDays}
	if b.Since != nil {
		out.Since = new(b.Since.UTC())
	}
	return out
}

// toSourcesJSON says when the sampler's last tick ran at each source; both
// are null before the first tick.
func toSourcesJSON(s service.SamplerSources) sourcesJSON {
	var at *time.Time
	if !s.At.IsZero() {
		at = new(s.At.UTC())
	}
	return sourcesJSON{
		Traefik: sourceJSON{State: string(s.Traefik), At: at},
		Nomad:   sourceJSON{State: string(s.Nomad), At: at},
	}
}

func toMetricsJSON(v service.MetricsView) metricsJSON {
	out := metricsJSON{
		Range: v.Range.Name, Step: int(v.Range.Step / time.Second),
		Points: make([]time.Time, 0, len(v.Points)),
		Series: metricsSeriesSetJSON{
			Requests: toSeriesJSON(v.Requests), Errors: toSeriesJSON(v.Errors), P95: toSeriesJSON(v.P95), Memory: toSeriesJSON(v.Memory),
			MemoryLimit: v.MemoryLimit, HasHTTP: v.HasHTTP,
		},
		Deploys:  make([]metricDeployJSON, 0, len(v.Deploys)),
		Issues:   make([]metricIssueJSON, 0, len(v.Issues)),
		Services: make([]serviceMetricsJSON, 0, len(v.Services)),
		Baseline: toBaselineJSON(v.Baseline), Sources: toSourcesJSON(v.Sources),
	}
	if out.Series.MemoryLimit == nil {
		out.Series.MemoryLimit = []*float64{}
	}
	for _, p := range v.Points {
		out.Points = append(out.Points, p.UTC())
	}
	for _, d := range v.Deploys {
		out.Deploys = append(out.Deploys, toMetricDeployJSON(d))
	}
	for _, is := range v.Issues {
		out.Issues = append(out.Issues, toIssueJSON(is))
	}
	for _, sv := range v.Services {
		out.Services = append(out.Services, toServiceMetricsJSON(sv))
	}
	return out
}

// getMetrics answers GET /v1/metrics: the numbers of one environment's apps.
// project and environment may be left out for Default › production, as the
// service reads them.
func (s *server) getMetrics(w http.ResponseWriter, r *http.Request) error {
	rng, sortBy, err := metricsParams(r)
	if err != nil {
		return err
	}
	v := r.URL.Query()
	view, err := s.svc.Metrics(r.Context(), service.MetricsQuery{
		Project: v.Get("project"), Environment: v.Get("environment"), Range: rng, Sort: sortBy,
	})
	if err != nil {
		return metricsError(err)
	}
	writeJSON(w, http.StatusOK, toMetricsJSON(view))
	return nil
}

// getAppMetrics answers GET /v1/apps/{id}/metrics: the same for one app's
// services, with the app's totals as the series.
func (s *server) getAppMetrics(w http.ResponseWriter, r *http.Request) error {
	rng, sortBy, err := metricsParams(r)
	if err != nil {
		return err
	}
	view, err := s.svc.AppMetrics(r.Context(), r.PathValue("id"), rng, sortBy)
	if err != nil {
		return metricsError(err)
	}
	writeJSON(w, http.StatusOK, toMetricsJSON(view))
	return nil
}

type hostResourceJSON struct {
	Used     *int64 `json:"used"`
	Reserved *int64 `json:"reserved"`
	Total    *int64 `json:"total"`
}

type hostCPUJSON struct {
	hostResourceJSON
	Cores *int64 `json:"cores"`
}

type hostDiskJSON struct {
	Mount       string   `json:"mount"`
	Holds       []string `json:"holds"`
	UsedMB      int64    `json:"usedMb"`
	TotalMB     int64    `json:"totalMb"`
	FreeMB      int64    `json:"freeMb"`
	UsedPercent float64  `json:"usedPercent"`
	State       string   `json:"state"`
}

type hostRoomJSON struct {
	Count        *int    `json:"count"`
	LikeApp      *string `json:"likeApp"`
	LikeAppName  *string `json:"likeAppName"`
	MemoryMB     *int64  `json:"memoryMb"`
	CPUMhz       *int64  `json:"cpuMhz"`
	LimitedBy    *string `json:"limitedBy"`
	FreeMemoryMB *int64  `json:"freeMemoryMb"`
}

type hostHistoryJSON struct {
	Points         []time.Time `json:"points"`
	MemoryUsed     []*float64  `json:"memoryUsed"`
	MemoryReserved []*float64  `json:"memoryReserved"`
}

type hostJSON struct {
	Memory    hostResourceJSON `json:"memory"`
	CPU       hostCPUJSON      `json:"cpu"`
	Disks     []hostDiskJSON   `json:"disks"`
	Room      hostRoomJSON     `json:"room"`
	History   hostHistoryJSON  `json:"history"`
	SampledAt *time.Time       `json:"sampledAt"`
	Stale     bool             `json:"stale"`
	Baseline  baselineJSON     `json:"baseline"`
	Sources   sourcesJSON      `json:"sources"`
}

func toHostJSON(h service.HostView) hostJSON {
	out := hostJSON{
		Memory: hostResourceJSON{Used: h.Memory.Used, Reserved: h.Memory.Reserved, Total: h.Memory.Total},
		CPU:    hostCPUJSON{hostResourceJSON{Used: h.CPU.Used, Reserved: h.CPU.Reserved, Total: h.CPU.Total}, h.CPU.Cores},
		Disks:  make([]hostDiskJSON, 0, len(h.Disks)),
		History: hostHistoryJSON{
			Points: make([]time.Time, 0, len(h.History.Points)), MemoryUsed: h.History.MemoryUsed, MemoryReserved: h.History.MemoryReserved,
		},
		Stale: h.Stale, Baseline: toBaselineJSON(h.Baseline), Sources: toSourcesJSON(h.Sources),
	}
	if out.History.MemoryUsed == nil {
		out.History.MemoryUsed = []*float64{}
	}
	if out.History.MemoryReserved == nil {
		out.History.MemoryReserved = []*float64{}
	}
	if h.SampledAt != nil {
		out.SampledAt = new(h.SampledAt.UTC())
	}
	for _, p := range h.History.Points {
		out.History.Points = append(out.History.Points, p.UTC())
	}
	for _, d := range h.Disks {
		holds := d.Holds
		if holds == nil {
			holds = []string{}
		}
		out.Disks = append(out.Disks, hostDiskJSON{Mount: d.Mount, Holds: holds, UsedMB: d.UsedMB, TotalMB: d.TotalMB, FreeMB: d.FreeMB,
			UsedPercent: d.UsedPercent, State: string(d.State)})
	}
	if rm := h.Room; rm.Known {
		out.Room.FreeMemoryMB = new(rm.FreeMemoryMB)
		if rm.HasYardstick {
			out.Room.Count = new(rm.Count)
			out.Room.LikeApp, out.Room.LikeAppName = new(rm.LikeApp), new(rm.LikeAppName)
			out.Room.MemoryMB, out.Room.CPUMhz = new(rm.MemoryMB), new(rm.CPUMHz)
			out.Room.LimitedBy = new(string(rm.LimitedBy))
		}
	}
	return out
}

// getHost answers GET /v1/host: the host's memory, CPU and disks, the room
// for more apps, and seven days of memory. It reads the store only.
func (s *server) getHost(w http.ResponseWriter, r *http.Request) error {
	view, err := s.svc.Host(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toHostJSON(view))
	return nil
}
