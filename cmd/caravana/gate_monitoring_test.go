package main

// The answers of the monitoring reads (feature 025) and an edge client for a
// load through Traefik, for the console step console-monitoring.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
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

// issue is the issue of that kind about the app, or nil.
func (m gateMetrics) issue(appID, kind string) *gateMetricIssue {
	for i := range m.Issues {
		if m.Issues[i].AppID == appID && m.Issues[i].Kind == kind {
			return &m.Issues[i]
		}
	}
	return nil
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
