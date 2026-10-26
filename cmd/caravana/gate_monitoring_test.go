package main

// The monitoring gate step (feature 025, quickstart §6), through the API with
// the gate's token. It is in its own file only because gate_test.go is long;
// runGate lists it after logs. The console step console-monitoring shares the
// types and the edge client below.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// gateMetricPoint is a point of a MetricSeries; a nil value is a gap.
type gateMetricPoint struct {
	Value *float64 `json:"value"`
	Low   *float64 `json:"low"`
	High  *float64 `json:"high"`
	Usual *float64 `json:"usual"`
	Days  int      `json:"days"`
}

// gateMetricSeries is a MetricSeries.
type gateMetricSeries struct {
	Points       []gateMetricPoint `json:"points"`
	Now          *float64          `json:"now"`
	Usual        *float64          `json:"usual"`
	UsualDays    int               `json:"usualDays"`
	UnusualSince *time.Time        `json:"unusualSince"`
}

// gateSources is a MetricsSources.
type gateSources struct {
	Traefik struct {
		State string     `json:"state"`
		At    *time.Time `json:"at"`
	} `json:"traefik"`
	Nomad struct {
		State string     `json:"state"`
		At    *time.Time `json:"at"`
	} `json:"nomad"`
}

// gateMetricIssue is a MetricIssue.
type gateMetricIssue struct {
	AppID      string    `json:"appId"`
	AppName    string    `json:"appName"`
	Service    string    `json:"service"`
	Kind       string    `json:"kind"`
	Level      string    `json:"level"`
	Value      float64   `json:"value"`
	Usual      *float64  `json:"usual"`
	Since      time.Time `json:"since"`
	Distance   float64   `json:"distance"`
	DeployNear *struct {
		AppName  string `json:"appName"`
		Version  uint64 `json:"version"`
		Relation string `json:"relation"`
		Minutes  *int   `json:"minutes"`
	} `json:"deployNear"`
	Window struct {
		Requests     int64   `json:"requests"`
		Errors       int64   `json:"errors"`
		ErrorPercent float64 `json:"errorPercent"`
	} `json:"window"`
}

// gateServiceMetrics is a ServiceMetrics.
type gateServiceMetrics struct {
	AppID     string   `json:"appId"`
	AppName   string   `json:"appName"`
	Service   string   `json:"service"`
	State     string   `json:"state"`
	HasDomain bool     `json:"hasDomain"`
	Requests  *float64 `json:"requests"`
	Errors    *float64 `json:"errors"`
	P95       *float64 `json:"p95"`
	MemoryMB  *int64   `json:"memoryMb"`
	Unusual   bool     `json:"unusual"`
}

// gateMetrics is the answer of getMetrics and getAppMetrics.
type gateMetrics struct {
	Range  string      `json:"range"`
	Step   int         `json:"step"`
	Points []time.Time `json:"points"`
	Series struct {
		Requests    gateMetricSeries `json:"requests"`
		Errors      gateMetricSeries `json:"errors"`
		P95         gateMetricSeries `json:"p95"`
		Memory      gateMetricSeries `json:"memory"`
		MemoryLimit []*float64       `json:"memoryLimit"`
		HasHTTP     bool             `json:"hasHttp"`
	} `json:"series"`
	Deploys []struct {
		AppID   string    `json:"appId"`
		AppName string    `json:"appName"`
		Version uint64    `json:"version"`
		EndedAt time.Time `json:"endedAt"`
	} `json:"deploys"`
	Issues   []gateMetricIssue    `json:"issues"`
	Services []gateServiceMetrics `json:"services"`
	Baseline struct {
		Since    *time.Time `json:"since"`
		State    string     `json:"state"`
		FullDays int        `json:"fullDays"`
	} `json:"baseline"`
	Sources gateSources `json:"sources"`
}

// service is the row of one app's service, or nil.
func (m gateMetrics) service(appID, name string) *gateServiceMetrics {
	for i := range m.Services {
		if m.Services[i].AppID == appID && m.Services[i].Service == name {
			return &m.Services[i]
		}
	}
	return nil
}

// issue is the issue of that kind about the app, or nil.
func (m gateMetrics) issue(appID, kind string) *gateMetricIssue {
	for i := range m.Issues {
		if m.Issues[i].AppID == appID && m.Issues[i].Kind == kind {
			return &m.Issues[i]
		}
	}
	return nil
}

// gateHost is the answer of getHost.
type gateHost struct {
	Memory struct {
		Used     *int64 `json:"used"`
		Reserved *int64 `json:"reserved"`
		Total    *int64 `json:"total"`
	} `json:"memory"`
	CPU struct {
		Used     *int64 `json:"used"`
		Reserved *int64 `json:"reserved"`
		Total    *int64 `json:"total"`
		Cores    *int64 `json:"cores"`
	} `json:"cpu"`
	Disks []struct {
		Mount       string   `json:"mount"`
		Holds       []string `json:"holds"`
		UsedMB      int64    `json:"usedMb"`
		TotalMB     int64    `json:"totalMb"`
		FreeMB      int64    `json:"freeMb"`
		UsedPercent float64  `json:"usedPercent"`
		State       string   `json:"state"`
	} `json:"disks"`
	Room struct {
		Count     *int    `json:"count"`
		LikeApp   *string `json:"likeApp"`
		MemoryMB  *int64  `json:"memoryMb"`
		CPUMhz    *int64  `json:"cpuMhz"`
		LimitedBy *string `json:"limitedBy"`
	} `json:"room"`
	History struct {
		Points         []time.Time `json:"points"`
		MemoryUsed     []*float64  `json:"memoryUsed"`
		MemoryReserved []*float64  `json:"memoryReserved"`
	} `json:"history"`
	SampledAt *time.Time  `json:"sampledAt"`
	Stale     bool        `json:"stale"`
	Sources   gateSources `json:"sources"`
}

// edgeClient calls one of the apps' hosts through Traefik the way a browser
// would reach the node (hostStatus's way: the lab's 127.0.0.1:8443, or the
// host's public IP on 443, with the host as server name), keeping its
// connections open for a load. It returns the client and the base URL.
func (g *gate) edgeClient(host string) (*http.Client, string) {
	addr, base := labEdgeAddress, "https://"+host+":8443"
	if g.target.Public != nil {
		addr, base = net.JoinHostPort(g.target.Public.IP, "443"), "https://"+host
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: host}, // routing only, as hostStatus
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			MaxIdleConnsPerHost: 2,
		},
	}, base
}

// edgeCount counts what a load sent: answers by status (0 for a call that got
// no answer: a timeout or a refused connection).
type edgeCount struct {
	mu       sync.Mutex
	byStatus map[int]int
}

// call sends one request and counts its answer.
func (e *edgeCount) call(client *http.Client, method, url, body string) int {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	status := 0
	if err == nil {
		if resp, err := client.Do(req); err == nil {
			status = resp.StatusCode
			// Read to the end, so the connection is kept for the next call.
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
	e.mu.Lock()
	if e.byStatus == nil {
		e.byStatus = map[int]int{}
	}
	e.byStatus[status]++
	e.mu.Unlock()
	return status
}

// routed is how many calls reached the app's service (anything but no answer
// and Traefik's own 404 for a host it has no router for): what Traefik counts
// on the service (research R8.2).
func (e *edgeCount) routed() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for status, c := range e.byStatus {
		if status != 0 && status != http.StatusNotFound {
			n += c
		}
	}
	return n
}

// snapshot is the counts so far, for a failure message.
func (e *edgeCount) snapshot() map[int]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[int]int{}
	for k, v := range e.byStatus {
		out[k] = v
	}
	return out
}

// metricsOf reads getMetrics or getAppMetrics (path with its query) with the
// gate's token, and the raw answer.
func (g *gate) metricsOf(t *testing.T, path string) (gateMetrics, []byte) {
	t.Helper()
	raw := g.sendWant(t, http.StatusOK, http.MethodGet, path, "")
	var m gateMetrics
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("GET %s: %v: %.300s", path, err, raw)
	}
	return m, raw
}

// hostOf reads getHost with the gate's token.
func (g *gate) hostOf(t *testing.T) (gateHost, []byte) {
	t.Helper()
	raw := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/host", "")
	var h gateHost
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatalf("GET /v1/host: %v: %.300s", err, raw)
	}
	return h, raw
}

// requestsIn is how many requests the points of a 1 h read add up to: each
// point is requests per second over its minute, so value × step.
func requestsIn(m gateMetrics) int {
	sum := 0.0
	for _, p := range m.Series.Requests.Points {
		if p.Value != nil {
			sum += *p.Value * float64(m.Step)
		}
	}
	return int(math.Round(sum))
}

// allocatedGauges reads nomad.client.allocated.cpu and .memory from Nomad's
// own metrics (the agent's /v1/metrics, with the test's Nomad token), as the
// spike did (research R8.7).
func (g *gate) allocatedGauges(t *testing.T) (cpu, mem float64, ok bool) {
	t.Helper()
	raw, err := g.target.Nomad.Operator().Metrics(nil)
	if err != nil {
		t.Fatalf("read Nomad's metrics: %v", err)
	}
	var sum api.MetricsSummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatalf("read Nomad's metrics: %v", err)
	}
	var haveCPU, haveMem bool
	for _, gv := range sum.Gauges {
		switch gv.Name {
		case "nomad.client.allocated.cpu":
			cpu, haveCPU = float64(gv.Value), true
		case "nomad.client.allocated.memory":
			mem, haveMem = float64(gv.Value), true
		}
	}
	return cpu, mem, haveCPU && haveMem
}

// monitoringStep (feature 025): the three reads against a real host's numbers.
// A busybox app gate025 with a domain, then:
//
//  1. right after its first deploy, getAppMetrics?range=1h has null (not 0) in
//     every point before the app's first sample, and "value":null in the JSON;
//  2. a load of known size through Traefik; once the rows are in, the points of
//     getAppMetrics add up to what was sent within a few, with no 5xx, a
//     slowest 5% and Traefik's source ok; getMetrics for the place lists the
//     service with a domain and its numbers;
//  3. getHost: memory, CPU and a disk non-zero; Nomad's source ok, so the
//     scoped token Caravana runs with reads the node (node:read); reserved
//     equal to Nomad's nomad.client.allocated.cpu and .memory;
//  4. a deploy token (scoped to gate025) is 403 token_scope on the three
//     reads; range=2h and sort=x are 422 invalid_field naming the field.
//
// The lab samples every 10 s (startLab), a host every minute; the waits allow
// for both.
func (g *gate) monitoringStep(t *testing.T) {
	const id = "gate025"
	const place = "project=default&environment=production"
	host := g.extraHost(id)

	tokenID := ""
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
		if tokenID != "" {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/tokens/"+tokenID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil && strings.Contains(err.Error(), "404")
	})
	t.Cleanup(remove)

	// The reads answer before the app exists.
	if m, _ := g.metricsOf(t, "/v1/metrics?"+place); m.Range != "6h" || len(m.Points) != 72 || m.Step != 300 {
		t.Errorf("getMetrics with no range = range %q, %d points of %d s; want 6h, 72 of 300", m.Range, len(m.Points), m.Step)
	}

	// 1. The app, and its first minute: nothing sampled is null, never 0.
	created := time.Now().UTC()
	status, body := g.send(t, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(actionsWeb(host, 1)))
	var d gateDeployment
	if err := json.Unmarshal(body, &d); err != nil || status != http.StatusAccepted || d.ID == "" {
		t.Fatalf("deployApp %s = %d: %.300s", id, status, body)
	}
	if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	first, raw := g.metricsOf(t, "/v1/apps/"+id+"/metrics?range=1h")
	if first.Range != "1h" || first.Step != 60 || len(first.Points) != 60 {
		t.Fatalf("getAppMetrics?range=1h = range %q, %d points of %d s; want 1h, 60 of 60", first.Range, len(first.Points), first.Step)
	}
	before := 0
	for i, at := range first.Points {
		if !at.Add(time.Minute).Before(created.Add(-time.Minute)) {
			break // the minute the app was made in, and later, may have rows
		}
		before++
		for name, s := range map[string]gateMetricSeries{"requests": first.Series.Requests, "errors": first.Series.Errors,
			"p95": first.Series.P95, "memory": first.Series.Memory} {
			if v := s.Points[i].Value; v != nil {
				t.Errorf("%s at %s (before %s existed) = %v, want null", name, at.Format(time.RFC3339), id, *v)
			}
		}
	}
	if before == 0 || !strings.Contains(string(raw), `"value":null`) {
		t.Errorf("getAppMetrics right after the first deploy: %d points before the app, \"value\":null in the answer: %v; want both",
			before, strings.Contains(string(raw), `"value":null`))
	}

	// 2. A load of known size. The minute the app's first row is in may be
	// a part of a minute, which a point's value × 60 would overstate, so the
	// calls wait for a whole minute of rows first.
	time.Sleep(time.Until(created.Add(70 * time.Second)))
	client, base := g.edgeClient(host)
	defer client.CloseIdleConnections()
	var sent edgeCount
	waitFor(t, "https://"+host+"/ to answer 200 through Traefik", time.Minute, func() bool {
		return sent.call(client, http.MethodGet, base+"/", "") == http.StatusOK
	})
	const calls = 240
	loopStart := time.Now()
	for range calls {
		sent.call(client, http.MethodGet, base+"/", "")
		time.Sleep(200 * time.Millisecond)
	}
	loopEnd := time.Now()
	routed := sent.routed()
	t.Logf("the load: %d calls in %s, answers by status %v (%d reached the service)", calls,
		loopEnd.Sub(loopStart).Round(time.Second), sent.snapshot(), routed)

	// The rows: the minute the load ended in must be whole, and its row written.
	var got gateMetrics
	counted := 0
	if !waitUntil(4*time.Minute, func() bool {
		if time.Since(loopEnd) < 75*time.Second {
			return false
		}
		got, _ = g.metricsOf(t, "/v1/apps/"+id+"/metrics?range=1h")
		counted = requestsIn(got)
		return got.Series.Requests.Now != nil && counted >= routed-3
	}) {
		t.Logf("the requests counted stayed at %d of %d sent", counted, routed)
	}
	if tolerance := max(3, routed/50); counted < routed-tolerance || counted > routed+tolerance {
		t.Errorf("getAppMetrics' requests add up to %d, the load sent %d that reached the service (within %d wanted)", counted, routed, tolerance)
	}
	for i, p := range got.Series.Requests.Points {
		at := got.Points[i]
		if at.After(loopStart.Add(time.Minute)) && at.Add(time.Minute).Before(loopEnd.Add(-time.Minute)) && p.Value == nil {
			t.Errorf("requests at %s, during the load, is a gap", at.Format(time.RFC3339))
		}
	}
	// Errors are a share of the requests: 0 where there were requests (the app
	// answers 200), and a gap where there were none.
	withErrors := 0
	for i, p := range got.Series.Errors.Points {
		reqs := got.Series.Requests.Points[i].Value
		switch {
		case reqs != nil && *reqs > 0 && (p.Value == nil || *p.Value != 0):
			t.Errorf("errors at %s = %v with %v req/s, want 0", got.Points[i].Format(time.RFC3339), p.Value, *reqs)
		case reqs != nil && *reqs > 0:
			withErrors++
		}
	}
	if withErrors == 0 {
		t.Errorf("no point has requests and an error share")
	}
	t.Logf("the points add up to %d requests (%d sent); %d points with requests, each 0%% errors", counted, routed, withErrors)
	hasP95 := false
	for _, p := range got.Series.P95.Points {
		hasP95 = hasP95 || p.Value != nil
	}
	if !hasP95 || !got.Series.HasHTTP {
		t.Errorf("getAppMetrics: a slowest 5%% point %v, hasHttp %v; want both", hasP95, got.Series.HasHTTP)
	}
	if got.Sources.Traefik.State != "ok" || got.Sources.Nomad.State != "ok" {
		t.Errorf("sources = traefik %q, nomad %q; want ok and ok", got.Sources.Traefik.State, got.Sources.Nomad.State)
	}
	if len(got.Services) != 1 || got.Services[0].AppID != id || !got.Services[0].HasDomain {
		t.Errorf("getAppMetrics' services = %+v, want one, %s's web with a domain", got.Services, id)
	}
	placeRead, _ := g.metricsOf(t, "/v1/metrics?"+place+"&range=1h&sort=traffic")
	if sv := placeRead.service(id, "web"); sv == nil || !sv.HasDomain || sv.Requests == nil || *sv.Requests <= 0 || sv.MemoryMB == nil {
		t.Errorf("getMetrics for the place: %s's row = %+v, want a domain, requests above 0 and memory", id, sv)
	}
	if !placeRead.Series.HasHTTP || placeRead.Baseline.Since == nil {
		t.Errorf("getMetrics: hasHttp %v, baseline since %v; want true and a time", placeRead.Series.HasHTTP, placeRead.Baseline.Since)
	}

	// 3. The host.
	h, _ := g.hostOf(t)
	if h.SampledAt == nil || h.Stale || h.Sources.Nomad.State != "ok" {
		t.Errorf("getHost: sampled %v, stale %v, nomad %q; want a fresh sample read with the scoped token (node:read)", h.SampledAt, h.Stale, h.Sources.Nomad.State)
	}
	for name, v := range map[string]*int64{"memory used": h.Memory.Used, "memory total": h.Memory.Total, "memory reserved": h.Memory.Reserved,
		"cpu used": h.CPU.Used, "cpu total": h.CPU.Total, "cpu reserved": h.CPU.Reserved, "cores": h.CPU.Cores} {
		if v == nil || *v <= 0 {
			t.Errorf("getHost: %s = %v, want a number above 0", name, v)
		}
	}
	if len(h.Disks) == 0 || h.Disks[0].TotalMB <= 0 || h.Disks[0].UsedMB <= 0 || len(h.Disks[0].Holds) == 0 {
		t.Errorf("getHost: disks = %+v, want a watched filesystem with its sizes", h.Disks)
	}
	if h.Room.Count == nil || h.Room.LikeApp == nil {
		t.Errorf("getHost: room = %+v, want a count and a yardstick (apps run)", h.Room)
	}
	if len(h.History.Points) != len(h.History.MemoryUsed) || len(h.History.Points) == 0 {
		t.Errorf("getHost: the history has %d points and %d memory values", len(h.History.Points), len(h.History.MemoryUsed))
	}
	var gCPU, gMem float64
	if !waitUntil(2*time.Minute, func() bool {
		c1, m1, ok := g.allocatedGauges(t)
		if !ok {
			return false
		}
		h, _ = g.hostOf(t)
		c2, m2, _ := g.allocatedGauges(t)
		gCPU, gMem = c2, m2
		return c1 == c2 && m1 == m2 && h.CPU.Reserved != nil && h.Memory.Reserved != nil &&
			float64(*h.CPU.Reserved) == c2 && float64(*h.Memory.Reserved) == m2
	}) {
		t.Errorf("getHost reserved = %v MHz, %v MB; Nomad's allocated gauges = %v MHz, %v MB; want them equal",
			deref(h.CPU.Reserved), deref(h.Memory.Reserved), gCPU, gMem)
	} else {
		t.Logf("reserved %d MHz, %d MB: Nomad's allocated gauges say the same", *h.CPU.Reserved, *h.Memory.Reserved)
	}

	// 4. Refusals.
	madeRaw := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"gate025-ci","scope":{"project":"default","app":"`+id+`"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(madeRaw, &made); err != nil || made.Token == "" {
		t.Fatalf("createToken with a scope: %v (token made: %v)", err, made.Token != "")
	}
	tokenID = made.ID
	for _, path := range []string{"/v1/metrics?" + place, "/v1/apps/" + id + "/metrics", "/v1/host"} {
		status, body := g.sendAs(t, made.Token, http.MethodGet, path, "")
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if status != http.StatusForbidden || e.Error.Code != "token_scope" {
			t.Errorf("a deploy token on GET %s = %d %s, want 403 token_scope", strings.SplitN(path, "?", 2)[0], status, e.Error.Code)
		}
	}
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "range", http.MethodGet, "/v1/metrics?"+place+"&range=2h", "")
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "range", http.MethodGet, "/v1/apps/"+id+"/metrics?range=2h", "")
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "sort", http.MethodGet, "/v1/metrics?"+place+"&sort=x", "")
	g.refusal(t, http.StatusNotFound, "app_not_found", "", http.MethodGet, "/v1/apps/nope025/metrics", "")
}

// deref is a number for a failure message: "null" for none.
func deref(p *int64) any {
	if p == nil {
		return "null"
	}
	return *p
}
